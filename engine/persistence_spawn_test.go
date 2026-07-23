package engine

import "testing"

// persistence_spawn_test.go — the keyframe/spawner interplay: a pending
// spawn HELD past its scheduled tick at keyframe time (any congested
// origin) must restore and replay bit-identically. Regression test for
// the draw-state loss Fable B1 (2026-07-23): Type/F once came from the
// pend's main stream, which the keyframe format does not persist — a
// restore re-drew from an advanced stream and silently diverged the CRC
// chain. Type/F now derive from the per-vehicle side stream
// (spawnAttrStream), idempotent by construction.

// heldOrigin reports whether any origin's pend is held past its schedule.
func heldOrigin(e *Engine) bool {
	if e.spawner == nil {
		return false
	}
	for i := range e.spawner.origins {
		if e.spawner.origins[i].tick < e.Tick {
			return true
		}
	}
	return false
}

func TestKeyframeRestoreWithHeldSpawn(t *testing.T) {
	// 10 veh/s per lane into the lanedrop: entries back up and holds begin
	// well before the keyframe tick.
	spec, _ := DefaultSpec("lanedrop", 300, 5)
	spec.Scen.SpawnRatePerLaneHour = 36000
	spec.Scen.DensityTargetPerKm = 0

	// Reference: full run.
	full, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for full.Tick < spec.Ticks {
		full.Step()
	}

	// Interrupted: run to 150, keyframe, restore, run on.
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 150 {
		e.Step()
	}
	if !heldOrigin(e) {
		t.Fatalf("vacuous: no held spawn at tick 150 (the test needs one)")
	}
	kf, err := e.MarshalState()
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	restored, err := RestoreState(spec, kf)
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if restored.CRC() != e.CRC() || restored.Tick != e.Tick {
		t.Fatalf("restored state diverges at tick 150: crc %016x vs %016x", restored.CRC(), e.CRC())
	}
	if !heldOrigin(restored) {
		t.Fatalf("restore lost the held spawn's schedule state")
	}
	for restored.Tick < spec.Ticks {
		restored.Step()
	}
	got := append(append([]uint64{}, e.CRCs...), restored.CRCs...)
	assertEqualCRCs(t, full.CRCs, got)
}
