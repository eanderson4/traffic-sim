package engine

import (
	"strings"
	"testing"
)

// director_test.go — the kernel side of the runtime demand director
// (scenario-format §3, ADR-0008 §5): verb validation, deterministic
// hold-and-retry injection under the Spawner's own clearance/cap rules,
// bounded expiry, replay determinism, and TSKF v3 seek fidelity.

func directorSpec(t *testing.T, kind string, ticks uint64) RunSpec {
	t.Helper()
	spec, err := DefaultSpec(kind, ticks, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.Types = []*VehicleType{&Car, &Truck}
	return spec
}

// TestDirectorSpawnValidation pins the reject reasons: unknown origin lane,
// a lane that is not a spawn origin, unknown vehicle type.
func TestDirectorSpawnValidation(t *testing.T) {
	e, err := NewEngine(directorSpec(t, "lanedrop", 10))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		d    SpawnDirective
		want string
	}{
		{"unknown lane", SpawnDirective{RequestID: "a", Origin: "Z9", TypeName: "car"}, "unknown origin lane"},
		{"not an origin", SpawnDirective{RequestID: "b", Origin: "B0", TypeName: "car"}, "not a spawn origin"},
		{"unknown type", SpawnDirective{RequestID: "c", Origin: "A0", TypeName: "bus"}, "unknown vehicle type"},
	}
	for _, tc := range cases {
		err := e.EnqueueSpawn(tc.d)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want reason containing %q", tc.name, err, tc.want)
		}
	}
	if e.PendingSpawns() != 0 {
		t.Fatalf("rejected verbs entered the queue: %d pending", e.PendingSpawns())
	}
	// A valid directive is accepted and applied at the next tick boundary.
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "ok", Origin: "A1", TypeName: "truck"}); err != nil {
		t.Fatalf("valid directive rejected: %v", err)
	}
	// RequestID uniqueness among live entries is engine-enforced: the same
	// ID while one is buffered fails loud.
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "ok", Origin: "A1", TypeName: "truck"}); err == nil ||
		!strings.Contains(err.Error(), "duplicate spawn request id") {
		t.Errorf("duplicate RequestID accepted: %v", err)
	}
	e.Step()
	if got := e.AppliedSpawns(); len(got) != 1 || got[0].Tick != 1 || got[0].TypeIdx != 1 {
		t.Fatalf("applied spawns after boundary: %+v", got)
	}
	// …and while one is live in the queue (a not-yet-due directive).
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "hold", Origin: "A1", TypeName: "car", EarliestTick: 1 << 40}); err != nil {
		t.Fatalf("hold directive rejected: %v", err)
	}
	e.Step()
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "hold", Origin: "A1", TypeName: "car", EarliestTick: 1 << 40}); err == nil ||
		!strings.Contains(err.Error(), "duplicate spawn request id") {
		t.Errorf("duplicate RequestID accepted against the live queue: %v", err)
	}
}

// TestDirectorInjection: an unblocked directive injects at its applied tick
// through e.newVehicle() — sequential ID, per-vehicle stream, F drawn from
// that stream, SpawnCooldown, entry speed by the Spawner's rule.
func TestDirectorInjection(t *testing.T) {
	spec := directorSpec(t, "straight", 20)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	d := SpawnDirective{RequestID: "r1", Origin: "S0", TypeName: "truck"}
	if err := e.EnqueueSpawn(d); err != nil {
		t.Fatal(err)
	}
	e.Step() // tick 1: applied; no earliest constraint → injects now
	if len(e.Vehicles()) != 1 {
		t.Fatalf("vehicles after tick 1: %d, want 1", len(e.Vehicles()))
	}
	v := e.Vehicles()[0]
	// Injected at s=0, then integrated within the same tick (the Spawner's
	// own semantics) — so S has advanced one tick by assertion time.
	if v.ID != 1 || v.Type != &Truck || v.TypeIdx != 1 || v.Lane.ID != "S0" || v.S <= 0 || v.S > 3 {
		t.Fatalf("injected vehicle: id=%d type=%v lane=%v s=%v", v.ID, v.Type.Name, v.Lane.ID, v.S)
	}
	// Cooldown is armed at SpawnCooldown and decremented within the same
	// tick (identical to a Spawner-injected vehicle).
	if v.Cooldown != e.Params.SpawnCooldown-1 {
		t.Fatalf("cooldown %d, want %d", v.Cooldown, e.Params.SpawnCooldown-1)
	}
	if v.F < 0.8 || v.F > 1.3 {
		t.Fatalf("F %.3f outside the clamp band", v.F)
	}
	// Free origin: entry speed is the truck's desired speed (F·v0eff).
	if want := v.F * Truck.V0; v.V != want {
		t.Fatalf("entry speed %.4f, want %.4f (F·v0)", v.V, want)
	}
	if e.Stats.Spawned != 1 || e.PendingSpawns() != 0 {
		t.Fatalf("spawned=%d pending=%d", e.Stats.Spawned, e.PendingSpawns())
	}
}

// TestDirectorOriginClearance: a blocked origin holds the directive
// (hold-and-retry) until the Spawner's 8+0.8·v clearance rule passes.
func TestDirectorOriginClearance(t *testing.T) {
	spec := directorSpec(t, "straight", 400)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// A standing vehicle 1 m past its own length: clearance 1 m < 8 m.
	e.AddInitialVehicle(e.Net.Lanes[0], 0, 6, 0, 1)
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "blk", Origin: "S0", TypeName: "car"}); err != nil {
		t.Fatal(err)
	}
	injectedAt := uint64(0)
	for e.Tick < 400 {
		e.Step()
		if e.Tick == 1 && len(e.Vehicles()) != 1 {
			t.Fatal("blocked directive injected despite no clearance")
		}
		if injectedAt == 0 && len(e.Vehicles()) == 2 {
			injectedAt = e.Tick
		}
	}
	if injectedAt == 0 {
		t.Fatal("directive never injected after the origin cleared")
	}
	t.Logf("injected at tick %d once clearance opened", injectedAt)
	if e.PendingSpawns() != 0 {
		t.Fatalf("%d directives still pending", e.PendingSpawns())
	}
}

// TestDirectorDensityCap: the cap rule is the Spawner's — at/above target
// the directive holds; held past the bounded window it expires without
// injecting. Cap 0 means uncapped.
func TestDirectorDensityCap(t *testing.T) {
	spec := directorSpec(t, "straight", 800)
	spec.Scen.DensityTargetPerKm = 0.001 // 5 km lane: one vehicle already exceeds it
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	e.AddInitialVehicle(e.Net.Lanes[0], 0, 100, 10, 1)
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "cap", Origin: "S0", TypeName: "car", EarliestTick: 1}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 100 {
		e.Step()
	}
	if len(e.Vehicles()) != 1 || e.PendingSpawns() != 1 {
		t.Fatalf("cap violated: vehicles=%d pending=%d", len(e.Vehicles()), e.PendingSpawns())
	}
	for e.Tick < uint64(DirectorSpawnHoldTicks)+50 {
		e.Step()
	}
	if len(e.Vehicles()) != 1 || e.PendingSpawns() != 0 {
		t.Fatalf("expiry failed: vehicles=%d pending=%d", len(e.Vehicles()), e.PendingSpawns())
	}
}

// TestDirectorEarliestTick: a directive with a future earliest tick waits,
// then injects at the first tick ≥ earliest.
func TestDirectorEarliestTick(t *testing.T) {
	spec := directorSpec(t, "straight", 100)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "fut", Origin: "S0", TypeName: "car", EarliestTick: 50}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 60 {
		e.Step()
		if e.Tick < 50 && len(e.Vehicles()) != 0 {
			t.Fatalf("injected early at tick %d", e.Tick)
		}
	}
	if len(e.Vehicles()) != 1 {
		t.Fatalf("not injected by tick 60: %d vehicles", len(e.Vehicles()))
	}
}

// TestDirectorReplayDeterminism: directives flow through the RunLog exactly
// like intents — engine.Replay re-enqueues them and reproduces the CRC
// chain bit-identically. Also pins the zero-director-change property: ticks
// before the first directive carry the director-free CRC byte stream.
func TestDirectorReplayDeterminism(t *testing.T) {
	spec := directorSpec(t, "lanedrop", 400)
	plan := map[uint64]SpawnDirective{
		10:  {RequestID: "d1", Origin: "A0", TypeName: "car"},
		30:  {RequestID: "d2", Origin: "A1", TypeName: "truck"},
		120: {RequestID: "d3", Origin: "A0", TypeName: "truck", EarliestTick: 150},
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		if d, ok := plan[e.Tick+1]; ok {
			if err := e.EnqueueSpawn(d); err != nil {
				t.Fatal(err)
			}
		}
		e.Step()
	}
	trucks := 0
	for _, v := range e.Vehicles() {
		if v.TypeIdx == 1 {
			trucks++
		}
	}
	if trucks == 0 {
		t.Fatal("no director-spawned trucks in the live run — test is vacuous")
	}

	// Zero-director-change: ticks before the first directive match the
	// director-free run's CRC stream exactly.
	eFree, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for eFree.Tick < 9 {
		eFree.Step()
	}
	for i := 0; i < 9; i++ {
		if e.CRCs[i] != eFree.CRCs[i] {
			t.Fatalf("tick %d: CRC %016x differs from director-free %016x", i+1, e.CRCs[i], eFree.CRCs[i])
		}
	}

	log := &RunLog{Spec: spec, Intents: e.IntentLog, Spawns: e.SpawnLog, CRCs: e.CRCs}
	relog, err := Replay(log)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(relog.Spawns) != len(log.Spawns) {
		t.Fatalf("replay recorded %d directives, live %d", len(relog.Spawns), len(log.Spawns))
	}
}

// TestDirectorKeyframeResume: TSKF v3 — a keyframe taken while directives
// are still pending restores the queue bit-exactly; the resumed run matches
// the uninterrupted one's CRC at a later tick.
func TestDirectorKeyframeResume(t *testing.T) {
	spec := directorSpec(t, "lanedrop", 400)
	live, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	restoredInjected := false
	var resumed *Engine
	for live.Tick < spec.Ticks {
		next := live.Tick + 1
		switch next {
		case 10:
			// Injected long before the keyframe.
			if err := live.EnqueueSpawn(SpawnDirective{RequestID: "early", Origin: "A0", TypeName: "car"}); err != nil {
				t.Fatal(err)
			}
		case 190:
			// Still pending at the keyframe (earliest tick beyond it).
			if err := live.EnqueueSpawn(SpawnDirective{RequestID: "late", Origin: "A1", TypeName: "truck", EarliestTick: 300}); err != nil {
				t.Fatal(err)
			}
		case 200:
			data, err := live.MarshalState()
			if err != nil {
				t.Fatal(err)
			}
			resumed, err = RestoreState(spec, data)
			if err != nil {
				t.Fatalf("RestoreState: %v", err)
			}
			if resumed.PendingSpawns() != 1 {
				t.Fatalf("restored queue depth %d, want 1", resumed.PendingSpawns())
			}
		}
		live.Step()
		if resumed != nil && !restoredInjected {
			resumed.Step()
		}
	}
	if resumed == nil {
		t.Fatal("no keyframe taken")
	}
	// The resumed engine ran only ticks 201..400 behind the live one; step
	// it the rest of the way.
	if resumed.Tick != live.Tick {
		for resumed.Tick < live.Tick {
			resumed.Step()
		}
	}
	if resumed.CRC() != live.CRC() {
		t.Fatalf("resumed crc %016x, live %016x", resumed.CRC(), live.CRC())
	}
	if resumed.PendingSpawns() != 0 {
		t.Fatalf("restored run still has %d pending directives", resumed.PendingSpawns())
	}
}
