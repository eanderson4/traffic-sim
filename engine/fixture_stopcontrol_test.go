package engine

import (
	"fmt"
	"sort"
	"testing"
)

// fixture_stopcontrol_test.go — single-junction behavior fixture:
// stop-control. The pinned network (testdata/stop-control/network.json) is a
// tiny Phoenix residential extract (see the fixture README for the recipe)
// whose one modeled stop junction — Amelia Ave × 24th St, netconvert
// junction type priority_stop — has two stop-class approach classes
// (ADR-0010: netimport rowClass maps SUMO state "s" to our "stop"; the
// allway_stop "w" state compiles to the same plain stop by fiat). The test
// exercises what we ship: the engine's RowStop gate in rightofway.go.
//
// Asserted behavior (the M8/M9 guardrails for junctions):
//
//	(a) Stop compliance — the engine's RowStop gate holds a vehicle at the
//	    virtual stop-line wall until a full stop AT the line is reached once
//	    (rightofway.go rowGate: v.V == 0 && lane.Length-v.S <= S0+1.0 sets
//	    stopDone; only then does the approach act as minor). So every vehicle
//	    observed crossing from a stop approach into the junction's internal
//	    lanes MUST have been observed at v < 0.1 m/s (the ADR-0014 §3 stop
//	    pin) with its front within S0+1.0 m of the lane end. A crossing
//	    without that observation is an engine stop-compliance violation.
//	(b) Zero collisions — Stats.Collisions counts adjacent-pair gaps below
//	    −0.01 m (overlap = collision by definition, engine.go).
//	(c) No deadlock/livelock — trips keep completing through the horizon:
//	    completed trip records (ADR-0014 kernel) must appear in the final
//	    third of the run, and junction crossings must be a non-vacuous
//	    fraction of the injected demand.
//
// Demand is director-injected (EnqueueSpawn) at deterministic ticks on the
// origin lane that IS the junction's parking-aisle stop approach
// (n777610268_1_0): the shortest honest demand path through the stop gate —
// spawn, 34 m approach, full stop at the line, proceed, despawn on the
// 36.8 m exit lane. Determinism (ADR-0005): two identical runs must produce
// identical CRC sequences.
//
// RESOLVED (2026-07-23, TestFixtureStopControlCrossTraffic): both
// pathologies this fixture exposed are fixed — origin injection overlaps
// (injectionPlan caps entry at braking-safe speed toward the leader
// THROUGH the connection; register() makes same-phase injections visible)
// and the boxBlocked fragment-exit seal (exitBlocked walks short
// successors; the in-box exit check re-verifies the funnel). The
// assertions run unguarded as regression gates.

const fixtureStopControlNet = "testdata/stop-control/network.json"

// stopFixtureRun drives the fixture with a director demand plan and returns
// the engine, the metric kernel, the number of observed stop-gate crossings,
// and the number of trips completed in the final third of the run.
func stopFixtureRun(t *testing.T, spec RunSpec, plan func(e *Engine, next uint64)) (e *Engine, k *Kernel, crossings, completedLate int, violations []string) {
	t.Helper()
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// Discover the stop junction's approach lanes from the compiled
	// network: non-internal lanes with a stop-class internal successor.
	approaches := map[string]*Lane{}
	junction := ""
	for _, l := range e.Net.Lanes {
		if l.Internal {
			continue
		}
		for _, s := range l.Successors {
			if s.Internal && s.Row == RowStop {
				if junction == "" {
					junction = s.Junction
				}
				if s.Junction != junction {
					t.Fatalf("fixture models more than one stop junction: %s and %s", junction, s.Junction)
				}
				approaches[l.ID] = l
			}
		}
	}
	if len(approaches) < 2 {
		t.Fatalf("fixture has %d stop approaches at junction %q, want >= 2", len(approaches), junction)
	}
	k, err = NewKernel(e, KernelConfig{Trips: true})
	if err != nil {
		t.Fatalf("NewKernel: %v", err)
	}

	// (a) tracking: vehicles must be seen stopped (< 0.1 m/s) within
	// S0+1.0 m of the approach lane end before entering the junction.
	stoppedAtLine := map[uint64]bool{}
	prevLane := map[uint64]*Lane{}
	worstStopDist := 0.0 // largest line distance at which a crossing vehicle stopped
	for e.Tick < spec.Ticks {
		plan(e, e.Tick+1)
		e.Step()
		k.Observe(e)
		live := map[uint64]bool{}
		for _, v := range e.Vehicles() {
			live[v.ID] = true
			if a, ok := approaches[v.Lane.ID]; ok {
				if rem := a.Length - v.S; v.V < metricStopSpeed && rem <= v.Type.S0+1.0+1e-9 {
					stoppedAtLine[v.ID] = true
					if rem > worstStopDist {
						worstStopDist = rem
					}
				}
			}
			if prev, ok := prevLane[v.ID]; ok && approaches[prev.ID] != nil &&
				v.Lane.Internal && v.Lane.Row == RowStop && v.Lane.Junction == junction {
				crossings++
				if !stoppedAtLine[v.ID] {
					violations = append(violations, fmt.Sprintf("vehicle %d crossed %s→%s without an observed stop at the line",
						v.ID, prev.ID, v.Lane.ID))
				}
				delete(stoppedAtLine, v.ID)
			}
			prevLane[v.ID] = v.Lane
		}
		for id := range prevLane {
			if !live[id] {
				delete(prevLane, id)
				delete(stoppedAtLine, id)
			}
		}
	}
	k.Finalize(e)
	tot := k.Totals()
	for _, tr := range k.DrainTrips() {
		if tr.Completed && tr.ExitTick >= 2*spec.Ticks/3 {
			completedLate++
		}
	}
	t.Logf("junction %s: approaches=%d crossings=%d spawned=%d despawned=%d completed=%d completedLateThird=%d deniedPending=%.1f live=%d worstStopDist=%.2fm minGap=%.2fm",
		junction, len(approaches), crossings, e.Stats.Spawned, e.Stats.Despawned,
		tot.CompletedTrips, completedLate, tot.DeniedPending, len(e.Vehicles()), worstStopDist, e.Stats.MinGap)
	return e, k, crossings, completedLate, violations
}

// stopFixtureSpec: 3600 ticks (360 s) at seed 42 over the pinned fixture.
func stopFixtureSpec() RunSpec {
	return RunSpec{
		Net:    NetSpec{Kind: "file", Path: fixtureStopControlNet},
		Scen:   Scenario{}, // director-only demand; no background spawner
		Params: DefaultParams(),
		Seed:   42,
		Ticks:  3600,
	}
}

// TestFixtureStopControl: one car every 12 s through the aisle stop
// approach — sparse enough that the 34 m approach lane clears between
// spawns (leader gone ⇒ clean v0 entry; the 8 m/s entry floor behind a
// stopped leader is a separate hazard, see the cross-traffic test).
// Asserts (a) stop compliance, (b) zero collisions, (c) live throughput at
// the horizon — and ADR-0005 CRC determinism over two identical runs.
func TestFixtureStopControl(t *testing.T) {
	const aisleOrigin = "n777610268_1_0" // the junction's parking-aisle stop approach
	plan := func(e *Engine, next uint64) {
		if next%120 == 1 {
			// EarliestTick anchors the 600-tick hold window (director.go):
			// leaving it zero expires the directive at tick 600 on arrival.
			if err := e.EnqueueSpawn(SpawnDirective{RequestID: fmt.Sprintf("aisle-%d", next), Origin: aisleOrigin, TypeName: "car", EarliestTick: next}); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}
	}
	spec := stopFixtureSpec()
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	if e.Net.OriginByID(aisleOrigin) == nil {
		t.Fatalf("origin lane %s missing from fixture", aisleOrigin)
	}

	e1, k1, crossings, completedLate, violations := stopFixtureRun(t, spec, plan)
	// (a) Stop compliance.
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Errorf("ENGINE BEHAVIOR VIOLATION — stop compliance: %d crossings without an observed stop at the line (first: %s)",
			len(violations), violations[0])
	}
	if crossings < 20 {
		t.Errorf("only %d stop-gate crossings — assertion (a) is vacuous (spawned %d)", crossings, e1.Stats.Spawned)
	}
	// (b) Zero collisions.
	if e1.Stats.Collisions != 0 {
		t.Errorf("ENGINE BEHAVIOR VIOLATION — %d collisions (by section: %v)", e1.Stats.Collisions, e1.Stats.CollisionsBySection)
	}
	// (c) Throughput stays live at the horizon.
	if tot := k1.Totals(); tot.CompletedTrips == 0 {
		t.Error("ENGINE BEHAVIOR VIOLATION — deadlock: zero completed trips at the horizon")
	}
	if completedLate == 0 {
		t.Errorf("ENGINE BEHAVIOR VIOLATION — livelock: no trip completed in the final third (ticks %d..%d)",
			2*spec.Ticks/3, spec.Ticks)
	}

	// ADR-0005: identical spec + seed must reproduce the CRC chain exactly.
	e2, _, _, _, _ := stopFixtureRun(t, spec, plan)
	assertEqualCRCs(t, e1.CRCs, e2.CRCs)
}

// TestFixtureStopControlCrossTraffic is the regression gate for the two
// engine pathologies this fixture surfaced under realistic cross traffic
// (23rd St → alley → Amelia WB stop approach, the aisle stop approach, and
// 24th St major through-flow, ~1 veh per 4.5–15 s per stream), both fixed
// 2026-07-23:
//
//  1. Origin injection was NOT collision-free behind a stopped leader
//     (2709 collisions, min gap −5.00 m, all on the 24th St origin
//     section). Fixed: injectionPlan caps entry at braking-safe speed
//     toward the leader measured THROUGH the connection (or the stop-line
//     wall), and register() makes same-phase injections visible to each
//     other (the identical-position clusters were a same-tick backlog
//     stacking into S=0, invisible to leaderAt's strict S>s search).
//
//  2. Sub-vehicle-length exit lanes permanently gated movements
//     (boxBlocked checked only the immediate successor). Fixed: the
//     exit-room walk continues through short successors (exitBlocked).
func TestFixtureStopControlCrossTraffic(t *testing.T) {
	const (
		wbOrigin    = "n5623027_20_0"     // → alley → Amelia WB stop approach
		aisleOrigin = "n777610268_1_0"    // the aisle stop approach
		majorOrigin = "n436774073_0_0_d2" // 24th St SB, major through-flow
	)
	plan := func(e *Engine, next uint64) {
		spawn := func(id, origin string, at uint64) {
			if err := e.EnqueueSpawn(SpawnDirective{RequestID: id, Origin: origin, TypeName: "car", EarliestTick: at}); err != nil {
				t.Fatalf("enqueue %s: %v", id, err)
			}
		}
		switch {
		case next%60 == 1:
			spawn(fmt.Sprintf("wb-%d", next), wbOrigin, next)
		case next%90 == 31:
			spawn(fmt.Sprintf("aisle-%d", next), aisleOrigin, next)
		case next%45 == 15:
			spawn(fmt.Sprintf("major-%d", next), majorOrigin, next)
		}
	}
	spec := stopFixtureSpec()
	e, k, _, completedLate, violations := stopFixtureRun(t, spec, plan)
	tot := k.Totals()

	// Diagnostic dump (logs only with -v or on failure): where the live
	// vehicles stand at the horizon, and the gate state of each standing
	// head vehicle.
	byLane := map[string][]string{}
	for _, v := range e.Vehicles() {
		byLane[v.Lane.ID] = append(byLane[v.Lane.ID], fmt.Sprintf("#%d s=%.1f/%.1f", v.ID, v.S, v.Lane.Length))
	}
	laneIDs := make([]string, 0, len(byLane))
	for id := range byLane {
		laneIDs = append(laneIDs, id)
	}
	sort.Strings(laneIDs)
	for _, id := range laneIDs {
		t.Logf("horizon lane %s: %v", id, byLane[id])
	}
	for _, v := range e.Vehicles() {
		if v.V > 0 || len(v.Lane.Successors) == 0 {
			continue
		}
		next := pickSuccessor(v.Lane, v.HeldTurn)
		t.Logf("probe #%d on %s s=%.1f/%.1f next=%s row=%v boxBlocked=%v conflict=%v stopDone=%v",
			v.ID, v.Lane.ID, v.S, v.Lane.Length, next.ID, next.Row,
			e.boxBlocked(v, next), e.rowConflict(v, next), v.stopDone)
	}

	if len(violations) > 0 {
		t.Errorf("stop compliance: %d crossings without an observed stop (first: %s)", len(violations), violations[0])
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collisions (by section: %v)", e.Stats.Collisions, e.Stats.CollisionsBySection)
	}
	if tot.CompletedTrips == 0 || completedLate == 0 {
		t.Errorf("deadlock/livelock: completed=%d completedLateThird=%d", tot.CompletedTrips, completedLate)
	}
}
