package engine

import "testing"

// scriptIntents enqueues a fixed intent schedule (tick → intents, in
// enqueue order) before each Step, mimicking a bus draining its buffer.
// The schedule itself is deterministic, so runs over it are comparable.
func runWithScript(t *testing.T, spec RunSpec, script map[uint64][]KeyedIntent) (*Engine, *RunLog) {
	t.Helper()
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		for _, k := range script[e.Tick+1] {
			e.EnqueueIntent(k)
		}
		e.Step()
	}
	return e, &RunLog{Spec: spec, Intents: e.IntentLog, CRCs: e.CRCs}
}

// lanedropTestScript exercises both intent axes plus interleavings: two
// controllers, out-of-order arrival within a tick (deterministic (ctrl,seq)
// order must win), an intent for a never-existent vehicle, and intents
// straddling a mid-run keyframe boundary. The lanedrop spawner needs ~25
// ticks to put the first vehicles on the road, so the schedule starts at
// tick 100 (≈12 vehicles live) and reaches past a tick-150 keyframe.
func lanedropTestScript() map[uint64][]KeyedIntent {
	return map[uint64][]KeyedIntent{
		100: {
			{Controller: "beta", Seq: 2, Intent: Intent{VehicleID: 5, Accel: -3, AccelSet: true}},
			{Controller: "alpha", Seq: 1, Intent: Intent{VehicleID: 3, Accel: 1.5, AccelSet: true}},
			{Controller: "beta", Seq: 1, Intent: Intent{VehicleID: 5, Accel: 2, AccelSet: true}},
		},
		101: {
			{Controller: "alpha", Seq: 2, Intent: Intent{VehicleID: 3, LaneDelta: 1}},
			{Controller: "beta", Seq: 3, Intent: Intent{VehicleID: 999, Accel: -2, AccelSet: true}}, // unknown vehicle: logged no-op
		},
		160: {
			{Controller: "alpha", Seq: 3, Intent: Intent{VehicleID: 8, Accel: -4, AccelSet: true, LaneDelta: -1}},
		},
		250: {
			{Controller: "beta", Seq: 4, Intent: Intent{VehicleID: 3, Accel: 3, AccelSet: true}},
		},
	}
}

// Intents must actually change the trajectory (the CRC chain reacts).
func TestIntentsChangeTrajectory(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 300, 11)
	_, plainLog, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, steeredLog := runWithScript(t, spec, lanedropTestScript())
	if equalCRCs(plainLog.CRCs, steeredLog.CRCs) {
		t.Fatal("intents had no effect on the CRC sequence")
	}
	if len(steeredLog.Intents) != 7 {
		t.Fatalf("arbitrated log has %d entries, want 7", len(steeredLog.Intents))
	}
}

// Application order at a tick boundary is (Controller, Seq) — not arrival
// order (ADR-0006 §4).
func TestIntentApplicationOrder(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 120, 11)
	_, log := runWithScript(t, spec, lanedropTestScript())
	var got []KeyedIntent
	for _, ti := range log.Intents {
		if ti.Tick == 100 {
			got = append(got, ti.KeyedIntent)
		}
	}
	want := []KeyedIntent{
		{Controller: "alpha", Seq: 1},
		{Controller: "beta", Seq: 1},
		{Controller: "beta", Seq: 2},
	}
	if len(got) != len(want) {
		t.Fatalf("tick 100 applied %d intents, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Controller != want[i].Controller || got[i].Seq != want[i].Seq {
			t.Fatalf("application order [%d] = %s:%d, want %s:%d",
				i, got[i].Controller, got[i].Seq, want[i].Controller, want[i].Seq)
		}
	}
}

// The same script replayed through the log must reproduce the run exactly.
func TestReplayWithIntents(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 300, 11)
	_, log := runWithScript(t, spec, lanedropTestScript())
	relog, err := Replay(log)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	assertEqualCRCs(t, log.CRCs, relog.CRCs)
	if len(relog.Intents) != len(log.Intents) {
		t.Fatalf("replayed log has %d intents, want %d", len(relog.Intents), len(log.Intents))
	}
}

// Two runs over the same script are identical — the intent path adds no
// nondeterminism of its own.
func TestIntentRunDeterminism(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 300, 11)
	_, a := runWithScript(t, spec, lanedropTestScript())
	_, b := runWithScript(t, spec, lanedropTestScript())
	assertEqualCRCs(t, a.CRCs, b.CRCs)
}

// A commanded accel outside the physical envelope is clamped, not applied
// verbatim (engine-side clamp, ADR-0008 §5).
func TestIntentAccelClamp(t *testing.T) {
	spec, _ := DefaultSpec("straight", 2, 1)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	v := e.AddInitialVehicle(e.Net.Lanes[0], 0, 100, 20, 1)
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: Intent{VehicleID: v.ID, Accel: -100, AccelSet: true}})
	e.Step()
	if v.Acc != -emergencyDecel {
		t.Fatalf("clamped accel = %v, want %v", v.Acc, -emergencyDecel)
	}
	// One-shot semantics: the override does not leak into the next tick.
	e.Step()
	if v.reqAccOK {
		t.Fatal("accel override persisted past its tick")
	}
}

// A commanded hop must move the vehicle when safe and be refused (expired,
// not deferred) when the target gap is too small.
func TestForcedLaneChange(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 2, 1)
	spec.Scen.SpawnRatePerLaneHour = 0 // quiet network: only the placed vehicles
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	a0 := e.Net.LaneByID("A0")
	a1 := e.Net.LaneByID("A1")
	mover := e.AddInitialVehicle(a0, 0, 200, 20, 1)
	blocker := e.AddInitialVehicle(a1, 0, 205, 20, 1) // 0 m lateral gap at the hop point

	// Refused: the blocker sits exactly at the target position.
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: Intent{VehicleID: mover.ID, LaneDelta: 1}})
	e.Step()
	if mover.Lane != a0 {
		t.Fatal("hop into an occupied slot was executed")
	}
	if blocker.Lane != a1 {
		t.Fatal("blocker moved unexpectedly")
	}

	// Accepted once the target is clear; the hop is an instant lane change
	// with the usual cooldown.
	e2, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	m2 := e2.AddInitialVehicle(e2.Net.LaneByID("A0"), 0, 200, 20, 1)
	e2.AddInitialVehicle(e2.Net.LaneByID("A1"), 0, 400, 20, 1)
	e2.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: Intent{VehicleID: m2.ID, LaneDelta: 1}})
	e2.Step()
	if m2.Lane != e2.Net.LaneByID("A1") {
		t.Fatal("commanded hop into a clear lane was refused")
	}
	if m2.Cooldown != e2.Params.LCCooldown {
		t.Fatalf("cooldown = %d, want %d", m2.Cooldown, e2.Params.LCCooldown)
	}
}

// Keyframe round-trip: marshal mid-run, restore, continue — the CRC chain
// must match the uninterrupted run tick for tick (ADR-0005 seek semantics).
func TestKeyframeRestoreContinuesRun(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 300, 11)
	script := lanedropTestScript()

	// Reference: full run.
	_, full := runWithScript(t, spec, script)

	// Interrupted: run to 150, marshal, restore, run on to 300.
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 150 {
		for _, k := range script[e.Tick+1] {
			e.EnqueueIntent(k)
		}
		e.Step()
	}
	kf, err := e.MarshalState()
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	t.Logf("keyframe at tick 150: %d vehicles, %d bytes", len(e.Vehicles()), len(kf))

	restored, err := RestoreState(spec, kf)
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if restored.CRC() != e.CRC() || restored.Tick != e.Tick {
		t.Fatalf("restored state diverges at tick 150: crc %016x vs %016x", restored.CRC(), e.CRC())
	}
	for restored.Tick < 300 {
		for _, k := range script[restored.Tick+1] {
			restored.EnqueueIntent(k)
		}
		restored.Step()
	}
	// e.CRCs holds ticks 1..150; restored.CRCs holds 151..300.
	got := append(append([]uint64{}, e.CRCs...), restored.CRCs...)
	assertEqualCRCs(t, full.CRCs, got)
	if n := len(e.IntentLog) + len(restored.IntentLog); n != 7 {
		t.Fatalf("arbitrated log across the keyframe boundary has %d entries, want 7", n)
	}
}

// Restoring from a truncated or foreign payload must fail cleanly.
func TestKeyframeRejectsGarbage(t *testing.T) {
	spec, _ := DefaultSpec("ring", 10, 1)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	e.Step()
	kf, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreState(spec, kf[:len(kf)/2]); err == nil {
		t.Fatal("truncated keyframe accepted")
	}
	if _, err := RestoreState(spec, []byte("not a keyframe")); err == nil {
		t.Fatal("garbage accepted as keyframe")
	}
}

// The tick-0 keyframe (initial state) must restore too — the record plane
// anchors every run with one so any target tick has a keyframe ≤ target.
func TestKeyframeAtTickZero(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 100, 5)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	kf, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreState(spec, kf)
	if err != nil {
		t.Fatalf("RestoreState(tick 0): %v", err)
	}
	for restored.Tick < 100 {
		restored.Step()
	}
	_, full, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CRC() != full.CRCs[99] {
		t.Fatalf("tick-0 restore diverged: %016x vs %016x", restored.CRC(), full.CRCs[99])
	}
}
