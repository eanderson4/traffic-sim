// nats-client.ts — the browser's NATS connection (ADR-0006 §8: browsers
// over the server's WebSocket listener, binary frames). Thin wrapper around
// nats.ws: subscribe the run's snapshot subject (TSSF) and signal-program
// subject (TSSG, M9) and hand raw payload bytes + headers to the decoders;
// surface connection state for the status line. Reconnect is nats.ws's job
// (default jittered backoff); frames arriving while disconnected are simply
// lost — the live plane is at-most-once by design, and the signal table's
// catch-up cadence plus the request/reply resync (ADR-0016) re-converges a
// reconnected client.

import { connect, Empty, RequestStrategy, type MsgHdrs, type NatsConnection } from "nats.ws";

export interface SnapSubscription {
  // Null in baked mode (ADR-0023: subscribeBaked has no connection — the
  // ADR-0016 request/reply pull no-ops on the caller's null guard, which
  // is safe because the baked TSSG set is delivered complete at attach).
  nc: NatsConnection | null;
  close: () => Promise<void>;
}

export async function subscribeSnapshots(
  servers: string,
  run: string,
  onFrame: (data: Uint8Array) => void,
  onSignals: (data: Uint8Array, headers: MsgHdrs | undefined) => void,
  onStatus: (connected: boolean, detail: string) => void,
): Promise<SnapSubscription> {
  const nc = await connect({ servers, name: "viz" });
  onStatus(true, `connected to ${servers}`);
  const sub = nc.subscribe(`ts.${run}.state.snap`);
  void (async () => {
    for await (const msg of sub) {
      onFrame(msg.data);
    }
  })().catch(() => {
    // Subscription loop ending abnormally; the status iterator below reports.
  });
  const sigSub = nc.subscribe(`ts.${run}.state.sig`);
  void (async () => {
    for await (const msg of sigSub) {
      onSignals(msg.data, msg.headers);
    }
  })().catch(() => {});
  void (async () => {
    for await (const s of nc.status()) {
      if (s.type === "disconnect") onStatus(false, "disconnected");
      if (s.type === "reconnect") onStatus(true, `reconnected to ${servers}`);
      if (s.type === "error") onStatus(false, `error: ${String(s.data)}`);
    }
    onStatus(false, "connection closed");
  })().catch(() => {});
  return {
    nc,
    close: async () => {
      sub.unsubscribe();
      sigSub.unsubscribe();
      await nc.close();
    },
  };
}

// requestSignalTable asks the run for the full signal-table chunk set on a
// private reply inbox (ADR-0016 §3: ts.{run}.state.sig.req) — the
// pull-when-ready catch-up, fired on attach and after a detected chunk
// gap. Reply chunks feed the caller's DEDICATED accumulator (broadcast
// rounds can't reset the pull mid-flight). Best-effort: no responder (an
// old engine) or a reply timeout just falls back to the broadcast
// cadence.
// Returns true when a COMPLETE chunk set was delivered (the sig_chunk
// header's i/n bounds N; unchunked = 1). False = no responder, timeout,
// or a truncated set — the caller's bounded retry depends on it (a
// silently swallowed empty reply would leave a paused-replay client
// without signals forever, sol review).
export async function requestSignalTable(
  nc: NatsConnection,
  run: string,
  onChunk: (data: Uint8Array, headers: MsgHdrs | undefined) => boolean,
): Promise<boolean> {
  let have = 0;
  let want = Infinity;
  try {
    const iter = await nc.requestMany(`ts.${run}.state.sig.req`, Empty, {
      strategy: RequestStrategy.Timer,
      maxWait: 10_000,
    });
    // Early-out once the full chunk set arrived: the sig_chunk header's
    // i/n tells us N, and waiting out the full 10 s window would stall
    // the resync budget for no reason (no end-of-replies marker exists).
    for await (const m of iter) {
      // Count only chunks the client actually accepted — an undecodable
      // reply must read as incomplete so the caller retries (sol review).
      if (!onChunk(m.data, m.headers)) continue;
      have++;
      const c = m.headers?.get("sig_chunk");
      if (c) {
        const n = Number(c.split("/")[1]);
        if (Number.isFinite(n) && n > 0) want = n;
      } else {
        want = Math.min(want, 1); // unchunked table: the single message is all
      }
      if (have >= want) break;
    }
  } catch {
    // No responder or reply timeout — the cadence broadcast remains.
  }
  return want !== Infinity && have >= want;
}
