package natsio

import (
	"testing"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// logcursor_test.go — the forward cursor must be an exact substitute for the
// full materialization it replaced (ADR-0024). These tests pin the
// equivalence rather than the mechanism: whatever the cursor does
// internally, tick-for-tick it has to hand the player what indexLogMsgs
// would have.

// recordForCursor records a short run into a fresh store and reopens it with
// a second broker (store exclusivity), returning the reader-side handles.
func recordForCursor(t *testing.T, run string, ticks uint64, recCfg RecorderConfig) (nats.JetStreamContext, string) {
	t.Helper()
	storeDir := t.TempDir()
	spec, err := engine.DefaultSpec("lanedrop", ticks, 7)
	if err != nil {
		t.Fatal(err)
	}
	ns1 := startStoreBroker(t, storeDir)
	nc1, js1 := dialJetStream(t, ns1)
	// Scripted controllers on their own connections: without arbitrated
	// intents in the record these tests compare two empty things and pass
	// vacuously (the guards below catch that, but the point is to exercise
	// the intent path).
	stop := make(chan struct{})
	ctlA, _ := dialJetStream(t, ns1)
	ctlB, _ := dialJetStream(t, ns1)
	go scriptedController(t, ctlA, run, "alpha", 20, stop)
	go scriptedController(t, ctlB, run, "beta", 30, stop)
	lr, err := RunLive(nc1, js1, run, spec, recCfg)
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	close(stop)
	if len(lr.Engine.IntentLog) == 0 {
		t.Fatal("controller intents never reached the engine")
	}
	ctlA.Close()
	ctlB.Close()
	nc1.Close()
	ns1.Shutdown()
	ns1.WaitForShutdown()

	ns2 := startStoreBroker(t, storeDir)
	t.Cleanup(ns2.Shutdown)
	nc2, js2 := dialJetStream(t, ns2)
	t.Cleanup(nc2.Close)
	return js2, StreamName(run)
}

// TestLogCursorMatchesFullIndex walks the cursor tick by tick over a whole
// recording and asserts it yields exactly what indexLogMsgs bucketed — the
// intents in order, the verbs in order, and the CRC presence and value.
func TestLogCursorMatchesFullIndex(t *testing.T) {
	const run = "cursoreq"
	js, stream := recordForCursor(t, run, 240, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1})

	msgs, err := fetchFrom(js, stream, run, 1)
	if err != nil {
		t.Fatalf("fetchFrom: %v", err)
	}
	want, err := indexLogMsgs(msgs, run)
	if err != nil {
		t.Fatalf("indexLogMsgs: %v", err)
	}
	if want.lastTick == 0 {
		t.Fatal("recording produced no ticks")
	}

	cur, err := newLogCursor(js, stream, run, 1)
	if err != nil {
		t.Fatalf("newLogCursor: %v", err)
	}
	defer cur.close()

	var sawIntents, sawCRCs int
	for tick := uint64(1); tick <= want.lastTick; tick++ {
		got, err := cur.records(tick)
		if err != nil {
			t.Fatalf("cursor records(%d): %v", tick, err)
		}
		wantIntents := want.intents[tick]
		if len(got.intents) != len(wantIntents) {
			t.Fatalf("tick %d: cursor has %d intents, index has %d", tick, len(got.intents), len(wantIntents))
		}
		for i := range wantIntents {
			if got.intents[i] != wantIntents[i] {
				t.Fatalf("tick %d intent %d: cursor %+v, index %+v", tick, i, got.intents[i], wantIntents[i])
			}
		}
		sawIntents += len(got.intents)

		wantVerbs := want.verbs[tick]
		if len(got.verbs) != len(wantVerbs) {
			t.Fatalf("tick %d: cursor has %d verbs, index has %d", tick, len(got.verbs), len(wantVerbs))
		}
		for i := range wantVerbs {
			if got.verbs[i].RequestID != wantVerbs[i].RequestID {
				t.Fatalf("tick %d verb %d: cursor %q, index %q", tick, i, got.verbs[i].RequestID, wantVerbs[i].RequestID)
			}
		}

		wantCRC, wantOK := want.crcs[tick]
		if got.hasCRC != wantOK || (wantOK && got.crc != wantCRC) {
			t.Fatalf("tick %d: cursor crc (%016x, %v), index (%016x, %v)", tick, got.crc, got.hasCRC, wantCRC, wantOK)
		}
		if got.hasCRC {
			sawCRCs++
		}
	}
	// Guard against a vacuous pass: an empty record would satisfy every
	// comparison above.
	if sawIntents == 0 || sawCRCs == 0 {
		t.Fatalf("vacuous: walked %d intents and %d CRCs", sawIntents, sawCRCs)
	}
	// Past the end of the record the cursor yields empty records rather
	// than erroring — the player's end-of-record hold depends on it.
	past, err := cur.records(want.lastTick + 1)
	if err != nil {
		t.Fatalf("cursor past end: %v", err)
	}
	if len(past.intents) != 0 || len(past.verbs) != 0 || past.hasCRC {
		t.Fatalf("cursor past end returned content: %+v", past)
	}
}

// TestLogCursorResetRewinds pins the seek path: reset repositions at a
// keyframe's sequence and the cursor then replays the same ticks it already
// served, identically. Without this, a scrub backwards would silently feed
// the engine the wrong tick's intents.
func TestLogCursorResetRewinds(t *testing.T) {
	const run = "cursorrew"
	js, stream := recordForCursor(t, run, 240, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1})

	cur, err := newLogCursor(js, stream, run, 1)
	if err != nil {
		t.Fatalf("newLogCursor: %v", err)
	}
	defer cur.close()

	// First pass over the ticks after the tick-100 keyframe.
	kf, err := findKeyframe(js, stream, run, 100)
	if err != nil {
		t.Fatalf("findKeyframe: %v", err)
	}
	if kf.tick != 100 {
		t.Fatalf("keyframe tick = %d, want 100", kf.tick)
	}
	first := map[uint64]*tickRecords{}
	for tick := uint64(1); tick <= 140; tick++ {
		rec, err := cur.records(tick)
		if err != nil {
			t.Fatalf("records(%d): %v", tick, err)
		}
		if tick > kf.tick {
			first[tick] = rec
		}
	}

	// Rewind to just after the keyframe and re-read the same span.
	if err := cur.reset(kf.seq + 1); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var compared int
	for tick := kf.tick + 1; tick <= 140; tick++ {
		got, err := cur.records(tick)
		if err != nil {
			t.Fatalf("records(%d) after reset: %v", tick, err)
		}
		want := first[tick]
		if len(got.intents) != len(want.intents) {
			t.Fatalf("tick %d after reset: %d intents, first pass had %d", tick, len(got.intents), len(want.intents))
		}
		for i := range want.intents {
			if got.intents[i] != want.intents[i] {
				t.Fatalf("tick %d intent %d differs after reset", tick, i)
			}
		}
		if got.hasCRC != want.hasCRC || got.crc != want.crc {
			t.Fatalf("tick %d crc differs after reset: (%016x,%v) vs (%016x,%v)",
				tick, got.crc, got.hasCRC, want.crc, want.hasCRC)
		}
		compared += len(got.intents)
	}
	if compared == 0 {
		t.Fatal("vacuous: no intents compared across the rewind")
	}
}

// TestLastLoggedTickMatchesIndex pins the cheap tail read that replaced
// scanning the whole record for the highest logged tick.
func TestLastLoggedTickMatchesIndex(t *testing.T) {
	const run = "cursortail"
	js, stream := recordForCursor(t, run, 137, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1})

	msgs, err := fetchFrom(js, stream, run, 1)
	if err != nil {
		t.Fatalf("fetchFrom: %v", err)
	}
	idx, err := indexLogMsgs(msgs, run)
	if err != nil {
		t.Fatalf("indexLogMsgs: %v", err)
	}
	got, ok, err := lastLoggedTick(js, stream, run)
	if err != nil {
		t.Fatalf("lastLoggedTick: %v", err)
	}
	if !ok {
		t.Fatal("lastLoggedTick reports an empty stream")
	}
	if got != idx.lastTick {
		t.Fatalf("lastLoggedTick = %d, full index lastTick = %d", got, idx.lastTick)
	}
}
