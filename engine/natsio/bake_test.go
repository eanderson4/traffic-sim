package natsio

import (
	"encoding/json"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// bake_test.go — the strict bake re-sim entry (ADR-0023 §1). Recording
// helpers (startStoreBroker, dialJetStream) live in player_test.go.

// recordShortRun records a short lanedrop run into a fresh store dir and
// shuts the broker down (one broker per store dir).
func recordShortRun(t *testing.T, run string, ticks, seed uint64) string {
	t.Helper()
	storeDir := t.TempDir()
	spec, err := engine.DefaultSpec("lanedrop", ticks, seed)
	if err != nil {
		t.Fatal(err)
	}
	ns := startStoreBroker(t, storeDir)
	nc, js := dialJetStream(t, ns)
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	nc.Close()
	ns.Shutdown()
	ns.WaitForShutdown()
	return storeDir
}

func TestBakeSourceResimulatesStrictly(t *testing.T) {
	const run = "bake1"
	storeDir := recordShortRun(t, run, 120, 7)

	js, shutdown, err := OpenRecordingStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()

	src, err := NewBakeSource(js, run)
	if err != nil {
		t.Fatalf("NewBakeSource: %v", err)
	}
	if src.EndTick() != 120 {
		t.Fatalf("EndTick = %d, want 120", src.EndTick())
	}
	var ticks []uint64
	var lastCRC uint64
	if err := src.Run(func(e *engine.Engine) error {
		ticks = append(ticks, e.Tick)
		lastCRC = e.CRC()
		return nil
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ticks) != 121 || ticks[0] != 0 || ticks[120] != 120 {
		t.Fatalf("sink saw %d ticks, first %d last %d — want 121 ticks 0..120",
			len(ticks), ticks[0], ticks[len(ticks)-1])
	}
	if src.RecordDigest() == ([32]byte{}) {
		t.Fatal("RecordDigest is zero after a completed bake")
	}
	_ = lastCRC

	// The digest is stable: a second bake of the same store consumes the
	// same messages in the same order.
	src2, err := NewBakeSource(js, run)
	if err != nil {
		t.Fatalf("NewBakeSource (second): %v", err)
	}
	if err := src2.Run(func(e *engine.Engine) error { return nil }); err != nil {
		t.Fatalf("Run (second): %v", err)
	}
	if src2.RecordDigest() != src.RecordDigest() {
		t.Fatal("RecordDigest differs across identical bakes")
	}
}

func TestBakeSourceSinkErrorAborts(t *testing.T) {
	const run = "bake2"
	storeDir := recordShortRun(t, run, 50, 3)

	js, shutdown, err := OpenRecordingStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	src, err := NewBakeSource(js, run)
	if err != nil {
		t.Fatalf("NewBakeSource: %v", err)
	}
	wantErr := errTest
	calls := 0
	err = src.Run(func(e *engine.Engine) error {
		calls++
		if e.Tick == 10 {
			return wantErr
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("Run with failing sink: got %v, want %v", err, wantErr)
	}
	if calls != 11 {
		t.Fatalf("sink called %d times — abort at tick 10 should stop at 11 calls", calls)
	}
}

var errTest = errString("sink boom")

type errString string

func (e errString) Error() string { return string(e) }

func TestBakeSourceCRCDivergenceAborts(t *testing.T) {
	const run = "bake3"
	storeDir := recordShortRun(t, run, 60, 11)

	js, shutdown, err := OpenRecordingStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()

	// Poison the registry meta: re-simulating the same record under a
	// DIFFERENT seed must diverge from the logged CRC chain — and the bake
	// must ABORT, never log-and-continue.
	reg, err := NewRegistry(js)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := reg.Meta(run)
	if err != nil {
		t.Fatal(err)
	}
	meta.Spec.Seed++
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.KeyValue(RegistryBucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(run+"/meta", data); err != nil {
		t.Fatal(err)
	}

	src, err := NewBakeSource(js, run)
	if err != nil {
		t.Fatalf("NewBakeSource: %v", err)
	}
	err = src.Run(func(e *engine.Engine) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "CRC divergence") {
		t.Fatalf("Run over poisoned meta: got %v, want a CRC-divergence abort", err)
	}
}
