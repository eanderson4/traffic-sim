package natsio

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// roundtrip_test.go — the M3 headline test: a live run over NATS, recorded
// to JetStream as it goes, replays from the stream to an identical CRC
// sequence (ADR-0005 determinism envelope over the wire, ADR-0006 planes).

// scriptedController is a minimal external controller client: it watches
// snapshots and, every cadence ticks, sends one intent for the first live
// vehicle it sees — alternating brake/accelerate, with a lane hop thrown
// in. Arrival timing is deliberately NOT deterministic; the record plane is
// what makes the run reproducible.
func scriptedController(t *testing.T, nc *nats.Conn, run, id string, cadence uint64, stop <-chan struct{}) {
	t.Helper()
	var seq uint64
	sub, err := nc.Subscribe(SubjectStateSnap(run), func(m *nats.Msg) {
		select {
		case <-stop:
			return
		default:
		}
		f, err := ParseFrame(m.Data)
		if err != nil || len(f.Vehicles) == 0 || f.Tick%cadence != 0 {
			return
		}
		seq++
		in := engine.Intent{VehicleID: f.Vehicles[0].ID, AccelSet: true}
		switch seq % 3 {
		case 0:
			in.Accel = -3
		case 1:
			in.Accel = 2
		case 2:
			in.Accel, in.LaneDelta = 0, 1
		}
		_ = nc.Publish(SubjectCtlIntent(run, id), EncodeIntent(in))
	})
	if err != nil {
		t.Errorf("controller subscribe: %v", err)
		return
	}
	defer func() { _ = sub.Unsubscribe() }()
	<-stop
}

// TestLiveRecordReplayDeterminism runs the lanedrop scenario live over the
// bus with a scripted controller, then replays from the JetStream record at
// several seek targets. Every replay must verify against the logged CRCs
// and land on the live run's own CRC chain.
func TestLiveRecordReplayDeterminism(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 300, 7)
	if err != nil {
		t.Fatal(err)
	}
	run := "rt1"

	stop := make(chan struct{})
	ctlConn := srv.Connect(t)
	go scriptedController(t, ctlConn, run, "alpha", 20, stop)
	go scriptedController(t, srv.Connect(t), run, "beta", 30, stop)
	defer close(stop)

	lr, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	e := lr.Engine
	if len(e.IntentLog) == 0 {
		t.Fatal("controller intents never reached the engine")
	}
	t.Logf("live run: %d ticks, %d arbitrated intents, %d log messages (intents=%d keyframes=%d crcs=%d), final crc %016x",
		e.Tick, len(e.IntentLog), lr.Recorder.IntentsWritten+lr.Recorder.KeyframesWritten+lr.Recorder.CRCsWritten,
		lr.Recorder.IntentsWritten, lr.Recorder.KeyframesWritten, lr.Recorder.CRCsWritten, e.CRC())

	meta, err := lr.Registry.Meta(run)
	if err != nil {
		t.Fatalf("registry meta: %v", err)
	}
	if meta.Status != StatusDone {
		t.Fatalf("run status = %q, want done", meta.Status)
	}

	// Seek targets: each lands on a different keyframe anchor (cadence 50,
	// plus the tick-0 anchor) and re-sims forward, CRC-verifying every tick.
	for _, target := range []uint64{49, 99, 175, 299, 300} {
		rep, err := ReplayFromStream(js, meta, target)
		if err != nil {
			t.Fatalf("ReplayFromStream(%d): %v", target, err)
		}
		if rep.FinalCRC != e.CRCs[target-1] {
			t.Fatalf("target %d: replay final crc %016x, live crc %016x", target, rep.FinalCRC, e.CRCs[target-1])
		}
		if rep.ToTick != target {
			t.Fatalf("target %d: replayed to %d", target, rep.ToTick)
		}
		t.Logf("target %d: keyframe@%d (seq %d), %d intents, %d CRCs verified, final %016x",
			target, rep.KeyframeTick, rep.KeyframeSeq, rep.IntentsReplayed, rep.CRCsVerified, rep.FinalCRC)
	}

	// The final-tick replay must have re-applied exactly the intents logged
	// between its keyframe and the target — and the whole arbitrated log is
	// on the stream.
	if lr.Recorder.IntentsWritten != uint64(len(e.IntentLog)) {
		t.Fatalf("stream holds %d intents, engine arbitrated %d", lr.Recorder.IntentsWritten, len(e.IntentLog))
	}

	// A replay past the end of the record must refuse, not fabricate.
	if _, err := ReplayFromStream(js, meta, 301); err == nil {
		t.Fatal("replay beyond the record was accepted")
	}
}

// TestSnapshotAndAckOnLivePlane verifies the live-plane contract from the
// client's seat: snapshot frames decode (tick, count, ids), and the
// per-controller ack echoes the applied_tick.
func TestSnapshotAndAckOnLivePlane(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 60, 5)
	if err != nil {
		t.Fatal(err)
	}
	run := "ack1"

	var snaps atomic.Uint64
	var lastFrame atomic.Value
	sub, err := nc.Subscribe(SubjectStateSnap(run), func(m *nats.Msg) {
		f, err := ParseFrame(m.Data)
		if err != nil {
			t.Errorf("bad snapshot frame: %v", err)
			return
		}
		if h := m.Header.Get("tick"); h == "" {
			t.Error("snapshot missing tick header")
		}
		snaps.Add(1)
		lastFrame.Store(f)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Ack watcher for controller "gamma".
	type ackSeen struct{ tick, applied uint64 }
	ackCh := make(chan ackSeen, 256)
	ackSub, err := nc.Subscribe(SubjectCtlAck(run, "gamma"), func(m *nats.Msg) {
		var p AckPayload
		if err := json.Unmarshal(m.Data, &p); err != nil {
			t.Errorf("bad ack payload: %v", err)
			return
		}
		tick, _ := strconv.ParseUint(m.Header.Get("tick"), 10, 64)
		ackCh <- ackSeen{tick: tick, applied: p.AppliedTick}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ackSub.Unsubscribe() }()

	// Controller "gamma": intent for the first visible vehicle at tick 30.
	stop := make(chan struct{})
	defer close(stop)
	go scriptedController(t, srv.Connect(t), run, "gamma", 30, stop)

	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 30}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	if snaps.Load() != spec.Ticks {
		t.Fatalf("received %d snapshots, want %d (one per tick)", snaps.Load(), spec.Ticks)
	}
	f := lastFrame.Load().(Frame)
	if f.Tick != spec.Ticks {
		t.Fatalf("last snapshot tick = %d, want %d", f.Tick, spec.Ticks)
	}

	// At least one ack, and every ack has applied_tick ≤ its tick (the
	// 1-tick minimum latency of ADR-0005 §3).
	n := len(ackCh)
	if n == 0 {
		t.Fatal("no applied_tick echoes received")
	}
	var last ackSeen
	for i := 0; i < n; i++ {
		a := <-ackCh
		if a.applied == 0 || a.applied > a.tick {
			t.Fatalf("ack with applied_tick %d at tick %d", a.applied, a.tick)
		}
		last = a
	}
	t.Logf("acks received: %d, last applied_tick %d", n, last.applied)
}

// TestCompetingWriterAbortsRun: the engine is the sole writer of
// ts.{run}.log.> and the OCC header enforces it broker-side (ADR-0006 §4).
// A forged write to the log stream must make the next engine write fail and
// the run abort loudly — never silently corrupt the record.
func TestCompetingWriterAbortsRun(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 2000, 9)
	if err != nil {
		t.Fatal(err)
	}
	run := "occ1"

	type outcome struct {
		lr  *LiveRun
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		lr, err := RunLive(nc, js, run, spec, RecorderConfig{CRCEvery: 1})
		done <- outcome{lr, err}
	}()

	// Wait for the record to exist, then forge a competing write (a second
	// "writer" with no OCC headers — the attack the assertion exists for).
	deadline := time.Now().Add(30 * time.Second)
	for {
		info, err := js.StreamInfo(StreamName(run))
		if err == nil && info.State.Msgs >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("record stream never appeared")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := js.Publish(SubjectLogIntent(run), []byte("forged intent")); err != nil {
		t.Fatalf("competing publish: %v", err)
	}

	out := <-done
	if out.err == nil {
		t.Fatal("run survived a competing writer on its log stream")
	}
	if !strings.Contains(out.err.Error(), "record plane failed") {
		t.Fatalf("abort error = %v, want a record-plane failure", out.err)
	}
	t.Logf("run aborted loudly as designed: %v", out.err)

	meta, err := out.lr.Registry.Meta(run)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusAborted || meta.Error == "" {
		t.Fatalf("registry status = %q (error %q), want aborted with reason", meta.Status, meta.Error)
	}
}

// TestSlowConsumerDroppedEngineUnblocked: a lagging live-plane subscriber
// is dropped (client-side pending limits) while the engine keeps ticking
// and healthy subscribers lose nothing (ADR-0006 §6: tolerate, drop,
// resync — never block).
func TestSlowConsumerDroppedEngineUnblocked(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 200, 3)
	if err != nil {
		t.Fatal(err)
	}
	run := "slow1"

	// Lagging subscriber: 4-message pending limit, never drained.
	lagConn := srv.Connect(t)
	lagSub, err := lagConn.SubscribeSync(SubjectStateSnap(run))
	if err != nil {
		t.Fatal(err)
	}
	if err := lagSub.SetPendingLimits(4, 4*4096); err != nil {
		t.Fatal(err)
	}

	// Healthy subscriber on its own connection.
	var got atomic.Uint64
	okConn := srv.Connect(t)
	okSub, err := okConn.Subscribe(SubjectStateSnap(run), func(*nats.Msg) { got.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = okSub.Unsubscribe() }()

	start := time.Now()
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	elapsed := time.Since(start)
	if err := okConn.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := lagConn.Flush(); err != nil {
		t.Fatal(err)
	}

	if got.Load() != spec.Ticks {
		t.Fatalf("healthy subscriber got %d/%d snapshots", got.Load(), spec.Ticks)
	}
	dropped, err := lagSub.Dropped()
	if err != nil {
		t.Fatal(err)
	}
	if dropped == 0 {
		t.Fatal("lagging subscriber was not dropped (pending limits never hit)")
	}
	t.Logf("slow consumer: %d messages dropped, engine ran %d ticks in %v unblocked", dropped, spec.Ticks, elapsed)
}

// TestRunRegistry: the KV registry holds the run's meta (spec + terminal
// status) and a usable late-joiner state pointer (ADR-0006 §1).
func TestRunRegistry(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 20, 4)
	if err != nil {
		t.Fatal(err)
	}
	run := "kv1"

	lr, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 10})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	reg := lr.Registry

	meta, err := reg.Meta(run)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Status != StatusDone {
		t.Fatalf("status = %q, want done", meta.Status)
	}
	if meta.Spec.Seed != spec.Seed || meta.Spec.Ticks != spec.Ticks || meta.Spec.Net.Kind != "lanedrop" {
		t.Fatalf("registry spec mismatch: %+v", meta.Spec)
	}
	if meta.StartedTick != 0 || meta.SchemaVer != SchemaVersion {
		t.Fatalf("meta: started_tick=%d schema=%d", meta.StartedTick, meta.SchemaVer)
	}

	ptr, err := reg.State(run)
	if err != nil {
		t.Fatal(err)
	}
	if ptr.Tick != spec.Ticks {
		t.Fatalf("state pointer tick = %d, want %d", ptr.Tick, spec.Ticks)
	}
	if ptr.CRC != lr.Engine.CRC() {
		t.Fatalf("state pointer crc %016x != engine %016x", ptr.CRC, lr.Engine.CRC())
	}
	// The pointer must name a real keyframe in the stream.
	if ptr.KeyframeSeq == 0 {
		t.Fatal("state pointer keyframe_seq = 0")
	}
}
