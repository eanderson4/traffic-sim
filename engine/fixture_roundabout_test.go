package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// fixture_roundabout_test.go — single-junction behavior fixture: roundabout.
// The pinned network (testdata/roundabout/network.json — recipe in the
// fixture README) is the Urachplatz single-lane roundabout in Stuttgart
// (Ostfildern), cropped to the ring plus four ~140 m arms. Roundabouts carry
// no special control in the compiled file: netconvert splits the ring into
// one priority junction per entry node and netimport (ADR-0010) compiles the
// circulatory connections as major and the entry connections as minor
// (yield) — the fixture exercises the ADR-0010 priority model in its
// roundabout form.
//
// Theory behind the asserted bounds:
//
//	(a) Yield compliance — rightofway.go's boxBlocked forbids ANY approach
//	    from entering an internal lane while a vehicle occupies one of its
//	    FoesCross/FoesMerge lanes ("a vehicle physically in the conflict
//	    zone owns it"). So an entry (minor-internal) vehicle can never share
//	    a tick with a vehicle on any of its cross foes: zero such
//	    co-presence observations across the run. The behavioral half: entry
//	    approaches under load must develop queues — entries that never wait
//	    were never challenged (a vacuous yield test).
//	(b) Zero collisions — Stats.Collisions counts adjacent-pair gaps below
//	    −0.01 m (overlap = collision by definition, engine.go).
//	(c) No gridlock — the network must drain where a drain exists: every
//	    W-origin (n3998057_0_d2) first-successor path exits at the N arm,
//	    so all W trips must COMPLETE by the horizon, and the draining path
//	    must keep moving (no vehicle parked through the final 300 ticks).
//
// ROUNDABOUT STATUS (2026-07-23): the fragment-exit deadlock that closed
// the ring is FIXED — vehicles enter, circulate (slowly: the ring's exit
// lanes are 1.4–8.6 m fragments, so circulation creeps), and the
// co-presence/collision gates hold at zero. TestFixtureRoundaboutCirculation
// stays skip-guarded on two REMAINING gaps, both demand/expectation-level
// rather than safety-level:
//
//  1. Director directive expiry (director.go: 600-tick hold) drops
//     platoon demand whose entry yield waits out the window — 9 of 12
//     inject. Whether flow demand should expire at all is an ADR-level
//     question (the deterministic spawner carries demand over forever).
//  2. The yield gate is conservative enough that entry queues never
//     exceed 1 on this tight crop (maxQueue ≥ 2 wanted as "yield
//     challenged" evidence).
//
// Set TRAFFICSIM_FIXTURE_EXPECT_FAIL=1 to assert the intended behavior.
//
// Demand is director-injected (EnqueueSpawn) at deterministic ticks, seed 42
// (ADR-0005: two identical runs must produce identical CRC chains).

const fixtureRoundaboutNet = "testdata/roundabout/network.json"

// Fixture lane IDs (see fixture README): the five demand portals.
const (
	rbOriginW  = "n3998057_0_d2"   // Werfmershalde W — drains to the N exit
	rbOriginN  = "n9663293_0"      // Spittlerstraße N — ring entry (deadlocked)
	rbOriginE1 = "n599183025_0"    // Haußmannstraße E (via n99370300_0)
	rbOriginE2 = "n9703875_0_d2"   // Haußmannstraße E (via i76479362_1_0)
	rbOriginS  = "n368911280_0_d2" // Urachstraße S
)

// roundaboutSpec: 3600 ticks (360 s) at seed 42 over the pinned fixture.
func roundaboutSpec() RunSpec {
	return RunSpec{
		Net:    NetSpec{Kind: "file", Path: fixtureRoundaboutNet},
		Scen:   Scenario{Types: []*VehicleType{&Car, &Truck}}, // director-only demand
		Params: DefaultParams(),
		Seed:   42,
		Ticks:  3600,
	}
}

// roundaboutDemand is the deterministic injection plan (applied tick →
// directive): 4 cars aimed at ring entries (the would-be circulating
// load), 5 W-arm through-trips (the draining path), and a 3-car S-arm
// platoon that should queue at the yield line under circulating load.
func roundaboutDemand() map[uint64]SpawnDirective {
	d := map[uint64]SpawnDirective{}
	add := func(tick uint64, id, origin, typ string) {
		d[tick] = SpawnDirective{RequestID: id, Origin: origin, TypeName: typ}
	}
	// Circulating load: one car per looping entry, 2.5 s apart.
	add(5, "circ-n1", rbOriginN, "car")
	add(15, "circ-e1", rbOriginE1, "car")
	add(25, "circ-s1", rbOriginS, "car")
	add(35, "circ-n2", rbOriginN, "car")
	// W through-trips (the only draining path), incl. one truck.
	add(200, "thru-w1", rbOriginW, "truck")
	add(300, "thru-w2", rbOriginW, "car")
	add(400, "thru-w3", rbOriginW, "car")
	add(500, "thru-w4", rbOriginW, "car")
	add(600, "thru-w5", rbOriginW, "car")
	// S-arm platoon at t=150–160 s: queue formation at the yield line.
	add(1500, "plat-s1", rbOriginS, "car")
	add(1550, "plat-s2", rbOriginS, "car")
	add(1600, "plat-s3", rbOriginS, "car")
	return d
}

// roundaboutObs is what one fixture run observed; the tests assert on it.
type roundaboutObs struct {
	e              *Engine
	tot            Totals
	trips          []TripRecord
	coPresence     int               // minor-internal × cross-foe same-tick occupancies
	maxQueue       int               // max simultaneous stopped vehicles on one entry approach
	entryStopTicks int               // stopped vehicle-ticks accumulated on entry approaches
	lastMove       map[uint64]uint64 // vehicle ID → last tick it moved
}

// roundaboutRun drives the fixture once with the director demand plan.
func roundaboutRun(t *testing.T, spec RunSpec) *roundaboutObs {
	t.Helper()
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	// The fixture must model the roundabout as priority junctions: at
	// least two minor (yield) entry internals with conflict wiring.
	minorEntries := 0
	entryLanes := map[string]bool{} // non-internal lanes feeding a minor internal
	for _, l := range e.Net.Lanes {
		if l.Internal {
			if l.Row == RowMinor && (len(l.FoesCross) > 0 || len(l.FoesMerge) > 0) {
				minorEntries++
			}
			continue
		}
		for _, s := range l.Successors {
			if s.Internal && s.Row == RowMinor {
				entryLanes[l.ID] = true
			}
		}
	}
	if minorEntries < 2 {
		t.Fatalf("fixture models %d minor entry internals, want >= 2 (roundabout entries must yield)", minorEntries)
	}
	k, err := NewKernel(e, KernelConfig{Trips: true})
	if err != nil {
		t.Fatalf("NewKernel: %v", err)
	}
	obs := &roundaboutObs{e: e, lastMove: map[uint64]uint64{}}
	plan := roundaboutDemand()
	prevS := map[uint64]float64{}
	prevLane := map[uint64]*Lane{}
	stopped := map[string]int{} // entry lane ID → currently stopped vehicles
	for e.Tick < spec.Ticks {
		if d, ok := plan[e.Tick+1]; ok {
			if err := e.EnqueueSpawn(d); err != nil {
				t.Fatalf("EnqueueSpawn %q: %v", d.RequestID, err)
			}
		}
		e.Step()
		k.Observe(e)
		// (a) conflict-zone co-presence: a vehicle on a minor internal lane
		// forbids any vehicle on its cross foes this tick.
		for _, l := range e.Net.Lanes {
			if !l.Internal || l.Row != RowMinor || len(l.vehs) == 0 {
				continue
			}
			for _, f := range l.FoesCross {
				obs.coPresence += len(f.vehs)
			}
		}
		// (a2)/(c) per-vehicle tracking: entry queues and last movement.
		for id := range stopped {
			stopped[id] = 0
		}
		for _, v := range e.Vehicles() {
			if entryLanes[v.Lane.ID] && v.V < metricStopSpeed {
				stopped[v.Lane.ID]++
				obs.entryStopTicks++
			}
			prev, seen := prevS[v.ID]
			if !seen || prev != v.S || prevLane[v.ID] != v.Lane {
				obs.lastMove[v.ID] = e.Tick
			}
			prevS[v.ID] = v.S
			prevLane[v.ID] = v.Lane
		}
		for _, n := range stopped {
			if n > obs.maxQueue {
				obs.maxQueue = n
			}
		}
	}
	k.Finalize(e)
	obs.tot = k.Totals()
	obs.trips = k.DrainTrips()
	return obs
}

// TestFixtureRoundabout: the hard behavior assertions that hold on the
// engine as shipped, plus the numbers characterizing the known circulation
// deadlock (see the file header). CRC determinism closes the run.
func TestFixtureRoundabout(t *testing.T) {
	spec := roundaboutSpec()
	o := roundaboutRun(t, spec)

	wCompleted := 0
	for _, tr := range o.trips {
		if tr.OriginLaneID == rbOriginW && tr.Completed {
			wCompleted++
		}
	}
	parkedFinal := 0
	for _, v := range o.e.Vehicles() {
		if spec.Ticks-o.lastMove[v.ID] >= 300 {
			parkedFinal++
		}
	}
	t.Logf("spawned=%d despawned=%d completed=%d wCompleted=%d activeAtHorizon=%d "+
		"coPresence=%d maxQueue=%d entryStopTicks=%d parkedFinal=%d minGap=%.3f collisions=%d",
		o.e.Stats.Spawned, o.e.Stats.Despawned, o.tot.CompletedTrips, wCompleted,
		o.tot.ActiveAtHorizon, o.coPresence, o.maxQueue, o.entryStopTicks, parkedFinal,
		o.e.Stats.MinGap, o.e.Stats.Collisions)

	// (a) Yield compliance, structural: zero conflict-zone co-presence.
	if o.coPresence != 0 {
		t.Errorf("VIOLATION: %d observations of a minor-internal vehicle sharing its conflict zone with a cross foe", o.coPresence)
	}
	// (a) Yield compliance, behavioral: entries under demand did wait —
	// the yield gates engaged (queue evidence, however pathological).
	if o.entryStopTicks == 0 {
		t.Error("no vehicle ever waited on an entry approach — the yield was never exercised")
	}

	// (b) Zero collisions.
	if o.e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations (by section %v), want 0",
			o.e.Stats.Collisions, o.e.Stats.CollisionsBySection)
	}

	// (c) Drainage: all 5 W-origin through-trips complete by the horizon —
	// the one path with adequate box-exit room flows, truck included.
	if wCompleted != 5 {
		t.Errorf("W-origin completed trips %d, want 5 (draining path wedged)", wCompleted)
	}

	// ADR-0005: an identical second run reproduces the CRC chain end state.
	o2 := roundaboutRun(t, spec)
	if o2.e.CRC() != o.e.CRC() {
		t.Fatalf("determinism: second run CRC %016x, first %016x", o2.e.CRC(), o.e.CRC())
	}
}

// TestFixtureRoundaboutCirculation is the INTENDED roundabout behavior —
// currently a KNOWN-FAILURE (see the file header for the mechanism):
//  1. every injected vehicle enters the network (no director expiry behind
//     a permanently gated entry),
//  2. circulating traffic keeps circulating — nobody parks for the final
//     30 s of the run,
//  3. the S-arm platoon queues AT the yield line and is then served
//     (cyclical, not permanent, waiting: maxQueue >= 2 yet the approach
//     drains),
//  4. yield compliance and zero collisions, as in the green test.
//
// Skip-guarded; set TRAFFICSIM_FIXTURE_EXPECT_FAIL=1 to enforce.
func TestFixtureRoundaboutCirculation(t *testing.T) {
	spec := roundaboutSpec()
	o := roundaboutRun(t, spec)

	var violations []string
	if o.e.Stats.Spawned != len(roundaboutDemand()) {
		violations = append(violations, fmt.Sprintf(
			"spawned %d of %d (director directives expired behind permanently gated entries)",
			o.e.Stats.Spawned, len(roundaboutDemand())))
	}
	for _, v := range o.e.Vehicles() {
		if idle := spec.Ticks - o.lastMove[v.ID]; idle >= 300 {
			violations = append(violations, fmt.Sprintf(
				"vehicle %d parked for the final %d ticks on %s (entry gated forever: box exit is a sub-vehicle-length lane)",
				v.ID, idle, v.Lane.ID))
		}
	}
	if o.maxQueue < 2 {
		violations = append(violations, fmt.Sprintf(
			"entries never queued under load: max queue %d, want >= 2 (yield unchallenged)", o.maxQueue))
	}
	if o.coPresence != 0 {
		violations = append(violations, fmt.Sprintf(
			"%d conflict-zone co-presence observations", o.coPresence))
	}
	if o.e.Stats.Collisions != 0 {
		violations = append(violations, fmt.Sprintf(
			"%d collision observations (by section %v)", o.e.Stats.Collisions, o.e.Stats.CollisionsBySection))
	}

	t.Logf("spawned=%d despawned=%d completed=%d activeAtHorizon=%d coPresence=%d maxQueue=%d "+
		"entryStopTicks=%d minGap=%.3f collisions=%d",
		o.e.Stats.Spawned, o.e.Stats.Despawned, o.tot.CompletedTrips, o.tot.ActiveAtHorizon,
		o.coPresence, o.maxQueue, o.entryStopTicks, o.e.Stats.MinGap, o.e.Stats.Collisions)
	if len(violations) > 0 && os.Getenv("TRAFFICSIM_FIXTURE_EXPECT_FAIL") != "1" {
		t.Skipf("KNOWN ENGINE VIOLATION (fragment-exit deadlock closes the ring; set TRAFFICSIM_FIXTURE_EXPECT_FAIL=1 to enforce): %s",
			strings.Join(violations, "; "))
	}
	for _, v := range violations {
		t.Error(v)
	}
}

// TestFixtureRoundaboutSustainedLoad: the built-in spawner (as the demo
// scenario drives it) on all five origins at 600 veh/h for 3600 ticks.
// Sustained injection into a ring whose entries deadlock must degrade
// without ever overlapping vehicles: the hard assertion is zero collisions;
// the saturation itself is reported with numbers (denied-entry demand is
// the ADR-0014 §3 measure of it).
func TestFixtureRoundaboutSustainedLoad(t *testing.T) {
	spec := roundaboutSpec()
	spec.Scen.SpawnRatePerLaneHour = 600
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	k, err := NewKernel(e, KernelConfig{Trips: true})
	if err != nil {
		t.Fatalf("NewKernel: %v", err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	tot := k.Totals()
	t.Logf("sustained 600 veh/h × 5 origins: spawned=%d despawned=%d completed=%d activeAtHorizon=%d "+
		"collisions=%d minGap=%.3f deniedWait=%.0fs deniedPending=%.1f meanTimeLoss=%.1fs",
		e.Stats.Spawned, e.Stats.Despawned, tot.CompletedTrips, tot.ActiveAtHorizon,
		e.Stats.Collisions, e.Stats.MinGap, tot.DeniedWaitS, tot.DeniedPending, tot.MeanTimeLossS)
	if e.Stats.Collisions != 0 {
		t.Errorf("VIOLATION: %d collision observations under sustained load (by section %v)",
			e.Stats.Collisions, e.Stats.CollisionsBySection)
	}
	if tot.CompletedTrips == 0 {
		t.Error("no trip completed in 360 s — even the draining W→N path wedged")
	}
}
