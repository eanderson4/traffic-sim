package natsio

import (
	"fmt"
	"testing"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// server.go — the embedded in-process broker (ADR-0006 §8): DontListen +
// in-process pipe connections, JetStream on a temp store dir. Integration
// tests never touch an external broker or a socket; docker-compose remains
// the dev/demo deployment (ADR-0004).

// TestServer is an embedded NATS server for tests and benchmarks.
type TestServer struct {
	NS *server.Server
}

// NewTestServer starts an in-process NATS server with JetStream enabled
// (file storage under a test-managed temp dir). Works with *testing.T and
// *testing.B.
func NewTestServer(tb testing.TB) *TestServer {
	tb.Helper()
	opts := &server.Options{
		DontListen: true,
		JetStream:  true,
		StoreDir:   tb.TempDir(),
	}
	ns, err := server.NewServer(opts)
	if err != nil {
		tb.Fatalf("nats-server NewServer: %v", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		tb.Fatal("nats-server not ready")
	}
	tb.Cleanup(ns.Shutdown)
	return &TestServer{NS: ns}
}

// Connect opens an in-process client connection (net.Pipe, no TCP).
func (s *TestServer) Connect(tb testing.TB) *nats.Conn {
	tb.Helper()
	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(s.NS))
	if err != nil {
		tb.Fatalf("in-process connect: %v", err)
	}
	tb.Cleanup(nc.Close)
	return nc
}

// JetStream opens a JetStream context on a fresh in-process connection.
func (s *TestServer) JetStream(tb testing.TB) (*nats.Conn, nats.JetStreamContext) {
	tb.Helper()
	nc := s.Connect(tb)
	js, err := nc.JetStream()
	if err != nil {
		tb.Fatalf("JetStream context: %v", err)
	}
	return nc, js
}

// subjects.go-equivalent: the ADR-0006 §3 taxonomy helpers live here so
// every subject string in the system has exactly one definition.
// Taxonomy: {ns}.{run}.{plane}.>{ids}; ticks/schema versions ride in
// headers/payloads, never in subjects.

const (
	// Namespace is the left-most token of every subject (ADR-0006 §3).
	Namespace = "ts"
	// SchemaVersion is the wire-format version stamped on every message
	// header and negotiated in the attach hello. Version 2 (M4): the
	// 4-axis intent frame, observation frames, contract events, and
	// TSKF keyframe v2 (ADR-0008).
	SchemaVersion = 2
)

// SubjectStateSnap is the live-plane snapshot subject: ts.{run}.state.snap.
func SubjectStateSnap(run string) string { return Namespace + "." + run + ".state.snap" }

// SubjectCtlIntent is one controller's intent subject: ts.{run}.ctl.intent.{controller_id}.
func SubjectCtlIntent(run, ctl string) string {
	return Namespace + "." + run + ".ctl.intent." + ctl
}

// SubjectCtlIntentAll is the engine's intent subscription: ts.{run}.ctl.intent.>.
func SubjectCtlIntentAll(run string) string { return Namespace + "." + run + ".ctl.intent.>" }

// SubjectCtlAck is the per-controller applied_tick echo: ts.{run}.ctl.ack.{controller_id}.
func SubjectCtlAck(run, ctl string) string { return Namespace + "." + run + ".ctl.ack." + ctl }

// Contract-plane subjects (ADR-0008 §3–§4): the attach handshake
// (request/reply), claim/release (request/reply; release may be
// fire-and-forget), heartbeats, per-controller observations, and the
// contract event feed.
func SubjectCtlHello(run string) string { return Namespace + "." + run + ".ctl.hello" }
func SubjectCtlClaim(run, ctl string) string {
	return Namespace + "." + run + ".ctl.claim." + ctl
}
func SubjectCtlClaimAll(run string) string { return Namespace + "." + run + ".ctl.claim.>" }
func SubjectCtlRelease(run, ctl string) string {
	return Namespace + "." + run + ".ctl.release." + ctl
}
func SubjectCtlReleaseAll(run string) string { return Namespace + "." + run + ".ctl.release.>" }
func SubjectCtlHeartbeat(run, ctl string) string {
	return Namespace + "." + run + ".ctl.heartbeat." + ctl
}
func SubjectCtlHeartbeatAll(run string) string { return Namespace + "." + run + ".ctl.heartbeat.>" }

// SubjectCtlObs is the per-controller observation subject:
// ts.{run}.ctl.obs.{controller_id} — one binary observation frame per tick.
func SubjectCtlObs(run, ctl string) string { return Namespace + "." + run + ".ctl.obs." + ctl }

// Contract events out: ts.{run}.ctl.events.{claim,release,unclaimed,pause,resume}.
func SubjectCtlEventsAll(run string) string { return Namespace + "." + run + ".ctl.events.>" }
func SubjectEventClaim(run string) string   { return Namespace + "." + run + ".ctl.events.claim" }
func SubjectEventRelease(run string) string { return Namespace + "." + run + ".ctl.events.release" }
func SubjectEventUnclaimed(run string) string {
	return Namespace + "." + run + ".ctl.events.unclaimed"
}
func SubjectEventPause(run string) string { return Namespace + "." + run + ".ctl.events.pause" }

// SubjectDriveIntrospect is the default driver's introspection side channel
// (request/reply, off the hot path): ts.{run}.drive.introspect.
func SubjectDriveIntrospect(run string) string { return Namespace + "." + run + ".drive.introspect" }

// Record-plane subjects (engine sole writer): ts.{run}.log.{intent,keyframe,crc,event}.
func SubjectLogIntent(run string) string   { return Namespace + "." + run + ".log.intent" }
func SubjectLogKeyframe(run string) string { return Namespace + "." + run + ".log.keyframe" }
func SubjectLogCRC(run string) string      { return Namespace + "." + run + ".log.crc" }
func SubjectLogEvent(run string) string    { return Namespace + "." + run + ".log.event" }
func SubjectLogAll(run string) string      { return Namespace + "." + run + ".log.>" }

// StreamName is the per-run log stream (ADR-0006 §5: one log stream per
// run). Stream names are single tokens; run ids are validated accordingly.
func StreamName(run string) string { return "TS_LOG_" + run }

// RegistryBucket is the KV run registry (ADR-0006 §1: metadata plane).
// Keys: {run}/meta (run spec + status), {run}/state (latest state pointer).
const RegistryBucket = "ts_runs"

// validToken enforces the single-token discipline for ids embedded in
// subjects/stream names (no dots, wildcards, or whitespace).
func validToken(kind, s string) error {
	if s == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return fmt.Errorf("%s %q: character %q not allowed (single NATS token: [A-Za-z0-9_-])", kind, s, c)
		}
	}
	return nil
}
