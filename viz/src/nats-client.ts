// nats-client.ts — the browser's NATS connection (ADR-0006 §8: browsers
// over the server's WebSocket listener, binary frames). Thin wrapper around
// nats.ws: subscribe the run's snapshot subject and hand raw payload bytes
// to the decoder; surface connection state for the status line. Reconnect
// is nats.ws's job (default jittered backoff); frames arriving while
// disconnected are simply lost — the live plane is at-most-once by design.

import { connect, type NatsConnection } from "nats.ws";

export interface SnapSubscription {
  nc: NatsConnection;
  close: () => Promise<void>;
}

export async function subscribeSnapshots(
  servers: string,
  run: string,
  onFrame: (data: Uint8Array) => void,
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
      await nc.close();
    },
  };
}
