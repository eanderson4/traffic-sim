package natsio

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// probe.go — client-side readiness probes against the embedded broker.
// Kept inside natsio so cmd/* never touches nats.go directly (the repo's
// dependency boundary: all NATS client code lives in this package).

// ProbeRunMeta connects to the broker at wsURL as a NATS-over-WebSocket
// client (the same path the viz takes) and returns nil only when the KV
// run registry holds {run}/meta with status "running" created at or after
// notBeforeUnix. Any listener that answers the port but cannot produce a
// LIVE run — a foreign engine, another session's broker, a finished run
// of the same id (Finish rewrites meta, it never deletes), a plain HTTP
// server — is an error, never readiness.
//
// The primary ownership proof is upstream of this probe: demosrv spawns
// live demos with a per-spawn UNIQUE run id, so a foreign broker can
// never hold our key at all. The notBeforeUnix check (RunMeta.CreatedUnix
// is already contract edge metadata) is the belt to those suspenders,
// closing the case where run ids are reused across sessions.
func ProbeRunMeta(wsURL, run string, notBeforeUnix int64) error {
	nc, err := nats.Connect(wsURL, nats.Timeout(2*time.Second), nats.NoReconnect())
	if err != nil {
		return err
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	kv, err := js.KeyValue(RegistryBucket)
	if err != nil {
		return err
	}
	e, err := kv.Get(run + "/meta")
	if err != nil {
		return fmt.Errorf("KV get %s/meta: %w", run, err)
	}
	var meta RunMeta
	if err := json.Unmarshal(e.Value(), &meta); err != nil {
		return fmt.Errorf("KV %s/meta unmarshal: %w", run, err)
	}
	if meta.Status != StatusRunning {
		return fmt.Errorf("KV %s/meta status %q (want %q) — stale or foreign registry entry", run, meta.Status, StatusRunning)
	}
	// 5 s tolerance for clock skew only. The foreign runs this guards
	// against are minutes to hours stale (a long-running other session);
	// a same-id run that FINISHED within the tolerance window can still
	// false-pass, which the supervisor's post-probe child-death recheck
	// catches. A fully atomic proof needs a per-spawn nonce in meta — an
	// ADR-0006 payload change, deferred.
	if meta.CreatedUnix < notBeforeUnix-5 {
		return fmt.Errorf("KV %s/meta created %d, before our spawn (%d) — foreign run", run, meta.CreatedUnix, notBeforeUnix)
	}
	return nil
}
