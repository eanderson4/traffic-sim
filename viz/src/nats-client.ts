// nats-client.ts — the browser's NATS connection (ADR-0006 §8: browsers
// over the server's WebSocket listener, binary frames). Thin wrapper around
// nats.ws: subscribe the run's snapshot subject (TSSF) and signal-program
// subject (TSSG, M9) and hand raw payload bytes to the decoders; surface
// connection state for the status line. Reconnect is nats.ws's job
// (default jittered backoff); frames arriving while disconnected are simply
// lost — the live plane is at-most-once by design, and the signal table's
// keyframe-cadence republication re-converges a reconnected client.

import { connect, type NatsConnection } from "nats.ws";

export interface SnapSubscription {
  nc: NatsConnection;
  close: () => Promise<void>;
}

export async function subscribeSnapshots(
  servers: string,
  run: string,
  onFrame: (data: Uint8Array) => void,
  onSignals: (data: Uint8Array) => void,
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
      onSignals(msg.data);
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
