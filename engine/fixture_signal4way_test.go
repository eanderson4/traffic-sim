package engine

import (
	"testing"
)

// fixture_signal4way_test.go — single-junction behavior fixture
// "signal-4way": a tiny Midtown Manhattan crop (bbox
// 40.7539,-73.9854,40.7558,-73.9828) centered on the signalized junction
// OSM node 42430333 (8th Ave × W 41st St area), imported per
// testdata/signal-4way/README.md. The junction runs a real alternating
// fixed-time program (39 G / 6 y / 39 G / 6 y over 14 links); the test
// drives the whole crop headless and asserts three textbook behaviors
// (ADR-0005 determinism: fixed seed, hermetic fixture JSON, no netconvert
// at test time).
//
// Asserted behaviors and their theory:
//
//	(a) RED COMPLIANCE — a fixed-time signal's stop line is absolute for
//	    any vehicle that can stop comfortably: while an approach's link
//	    shows red, no UNCOMMITTED vehicle may cross its stop line
//	    (ADR-0011: red = virtual stop-line wall; committed = v² > 2·d·B
//	    at the previous tick, the engine's own enforcement criterion —
//	    textbook clearance treats those crossings as legal). Hard
//	    assertion: zero non-committed red crossings over the whole run,
//	    across EVERY signal-bound internal lane of the crop, not just the
//	    junction under test.
//	(b) DISCHARGE — with oversaturated demand on one approach, the
//	    movement discharges every green window. The textbook saturation
//	    band [1.5, 2.5] s/veh assumes an isolated junction; this crop's
//	    back-to-back boxes (sub-car-length separation) legally serialize
//	    discharge to one vehicle per box traversal under the
//	    don't-block-the-box discipline, so the honest gate is wider:
//	    non-vacuity (≥ 3 discharges per 39 s green window — the sealed
//	    engine produced ZERO) and mean saturated headway in [1.5, 9] s/veh.
//	(c) ZERO COLLISIONS — e.Stats.Collisions == 0 over the run
//	    (engine.go collisionGap: adjacent-pair gap < −0.01 m).
//
// RESOLVED VIOLATIONS (2026-07-23, all three fixture-found bugs):
//
//	V1 (a): amber-committed vehicles were not grandfathered at the red
//	    onset — sigGate's red wall applied unconditionally. Now: the red
//	    wall holds only vehicles the wall can stop (v² ≤ 2·d·emergencyDecel);
//	    committed vehicles proceed through clearance, still box-gated.
//	V2 (b): boxBlocked's exit-room check examined only the internal lane's
//	    IMMEDIATE successor; netimport's sub-vehicle-length exit stubs
//	    (0.2 m) sealed every approach permanently. Now: exit-room walks
//	    short successor chains (exitBlocked), the gate targets the first
//	    CONTROLLED internal lane through fragments and uncontrolled boxes
//	    (gateTarget), and box exits re-check inside the box.
//	V3 (c): origin injection was not collision-free — clearance looked at
//	    the origin lane only and entered at ≥ 8 m/s regardless. Now:
//	    injectionPlan caps entry at braking-safe speed toward the leader
//	    THROUGH the connection or the stop-line wall, and register() makes
//	    same-phase injections visible to each other.

// The junction under test and its instrumented approach (see README).
const (
	fixS4Net      = "testdata/signal-4way/network.json"
	fixS4Approach = "n167922072_0_0" // single-lane stop-line stub feeding link 10
	fixS4Internal = "i42430333_10_0" // signal-bound internal lane (tlLink 10)
	fixS4Origin   = "n167922072_1_0" // origin lane whose default route feeds the approach
	fixS4Ticks    = 3600             // 360 s = 4 full 90 s cycles
	fixS4MinGated = 100              // non-vacuity floor: signal-gated crossings observed
)

func fixtureSignal4WaySpec(ticks uint64) RunSpec {
	return RunSpec{
		Net: NetSpec{Kind: "file", Path: fixS4Net},
		Scen: Scenario{
			// Moderate background demand on every origin; the instrumented
			// approach is oversaturated (3000 veh/h ≫ its green share of
			// capacity ≈ 39/90 × 1800 ≈ 780 veh/h).
			SpawnRatePerLaneHour: 600,
			SpawnRates:           map[string]float64{fixS4Origin: 3000},
			DensityTargetPerKm:   0, // uncapped: the queue must be allowed to build
		},
		Params: DefaultParams(),
		Seed:   42,
		Ticks:  ticks,
	}
}

func TestFixtureSignal4Way(t *testing.T) {
	spec := fixtureSignal4WaySpec(fixS4Ticks)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	i10 := e.Net.LaneByID(fixS4Internal)
	stub := e.Net.LaneByID(fixS4Approach)
	if i10 == nil || stub == nil {
		t.Fatalf("fixture drift: approach %q or internal lane %q missing", fixS4Approach, fixS4Internal)
	}
	if i10.Signal == nil {
		t.Fatalf("fixture drift: %q carries no signal program", fixS4Internal)
	}

	origin := e.Net.LaneByID(fixS4Origin)

	// Per-tick observation state.
	type prevState struct {
		lane *Lane
		s, v float64
	}
	prev := map[uint64]prevState{} // vehicle ID → state at the previous tick
	var redCross []string          // "tick:veh:lane" red-crossing events
	committedCross := 0            // red crossings by physically committed vehicles (legal clearance)
	amberCross := 0                // committed crossings on amber (legal)
	gatedCross := 0                // all stop-line crossings of signal-bound lanes
	var crossings []uint64         // ticks of discharges stub → i10
	prevGreen := false
	var windowFirst []int // crossings index where each green window starts
	greenTicks, queuedGreenTicks := 0, 0

	for e.Tick < spec.Ticks {
		e.Step()
		assertNoNaN(t, e)

		// Green-window bookkeeping for the instrumented approach, plus the
		// starvation evidence: green ticks with a queue waiting upstream.
		g := e.sigState(i10) == SigGreen
		if g {
			greenTicks++
			if queueAtLine(e, origin) {
				queuedGreenTicks++
			}
			if !prevGreen {
				windowFirst = append(windowFirst, len(crossings))
			}
		}
		prevGreen = g

		for _, v := range e.Vehicles() {
			p, seen := prev[v.ID]
			cur := v.Lane
			prev[v.ID] = prevState{cur, v.S, v.V}
			if !seen || p.lane == cur || p.lane.Left == cur || p.lane.Right == cur {
				continue // no boundary crossing (spawn / same lane / lateral hop)
			}
			chain, _, ok := crossedChain(p.lane, cur)
			if !ok || len(chain) < 2 {
				chain = []*Lane{p.lane, cur} // defensive: disjoint hop (none expected)
			}
			for k := 1; k < len(chain); k++ {
				l := chain[k]
				if !l.Internal || l.Signal == nil {
					continue
				}
				gatedCross++
				switch e.sigState(l) {
				case SigRed:
					// Committed at the previous tick = could not have
					// stopped comfortably (v² > 2·d·B — the engine's own
					// enforcement criterion, signal.go); textbook
					// clearance, legal.
					d := p.lane.Length - p.s
					for j := 1; j < k; j++ {
						d += chain[j].Length
					}
					if p.v*p.v > 2*d*v.Type.B {
						committedCross++
					} else {
						redCross = append(redCross, l.ID)
						if len(redCross) <= 10 {
							t.Logf("RED VIOLATION: tick %d vehicle %d entered %s on red UNCOMMITTED (from %s, prevV %.2f, dToLine %.2f)",
								e.Tick, v.ID, l.ID, chain[k-1].ID, p.v, d)
						}
					}
				case SigAmber:
					amberCross++
				}
				// Discharge tap: the instrumented stop line.
				if l == i10 && chain[k-1] == stub {
					crossings = append(crossings, e.Tick)
				}
			}
		}
	}
	t.Logf("run: %d ticks, spawned %d, in-network %d, gated crossings %d (%d on amber, %d committed on red), discharges on instrumented movement %d",
		spec.Ticks, e.Stats.Spawned, len(e.Vehicles()), gatedCross, amberCross, committedCross, len(crossings))

	// (a) RED COMPLIANCE — exactly zero UNCOMMITTED stop-line crossings on
	// red (committed ones are textbook clearance and counted above).
	if gatedCross < fixS4MinGated {
		t.Errorf("(a) non-vacuity: only %d signal-gated crossings observed (want ≥ %d) — the junction barely flowed",
			gatedCross, fixS4MinGated)
	}
	if len(redCross) != 0 {
		t.Errorf("(a) red compliance: %d uncommitted red crossings, want exactly 0 (first: %s)", len(redCross), redCross[0])
	} else {
		t.Logf("(a) red compliance: 0 uncommitted crossings over %d gated crossings (%d committed clearance)", gatedCross, committedCross)
	}

	// (b) DISCHARGE — every green window serves the oversaturated approach.
	// Textbook saturation (1.5–2.5 s/veh) assumes an isolated junction;
	// this crop's back-to-back boxes legally serialize discharge under the
	// don't-block-the-box discipline. Gate: non-vacuity (≥ 3 discharges
	// per 39 s window — the sealed engine produced 0) and mean saturated
	// headway inside the documented serialized band.
	dt := e.Params.Dt
	var headways []float64
	for w, start := range windowFirst {
		end := len(crossings)
		if w+1 < len(windowFirst) {
			end = windowFirst[w+1]
		}
		// Skip the first two headways of the window (startup lost time):
		// pairs (c0,c1) and (c1,c2) are startup; pairs from c2 on are
		// saturated discharge.
		for i := start + 3; i < end; i++ {
			headways = append(headways, float64(crossings[i]-crossings[i-1])*dt)
		}
	}
	if want := 3 * len(windowFirst); len(crossings) < want {
		t.Errorf("(b) discharge starved: %d discharges over %d green windows (%d with a queue at the line), want ≥ %d — the sealed-engine regime",
			len(crossings), len(windowFirst), queuedGreenTicks, want)
	} else if len(headways) > 0 {
		var sum, mn, mx float64
		mn = 1e9
		for _, h := range headways {
			sum += h
			if h < mn {
				mn = h
			}
			if h > mx {
				mx = h
			}
		}
		mean := sum / float64(len(headways))
		if mean < 1.5 || mean > 9.0 {
			t.Errorf("(b) discharge headway outside the serialized-boxes band: mean %.3f s/veh (min %.2f, max %.2f, n=%d), want [1.5, 9] s",
				mean, mn, mx, len(headways))
		} else {
			t.Logf("(b) discharge: mean headway %.3f s/veh (min %.2f, max %.2f, n=%d) over %d green windows, %d discharges",
				mean, mn, mx, len(headways), len(windowFirst), len(crossings))
		}
	}

	// (c) ZERO COLLISIONS — unconditional.
	if e.Stats.Collisions != 0 {
		t.Errorf("(c) collisions: %d collision observations (by section %v), want 0",
			e.Stats.Collisions, e.Stats.CollisionsBySection)
	}
	t.Logf("(c) collisions: %d observations, min gap %.3f m", e.Stats.Collisions, e.Stats.MinGap)
}

// queueAtLine reports whether a vehicle is stopped in the last 10 m of lane
// (or anywhere on it when the lane is shorter than 10 m) — the discharge
// queue pressing the stop line.
func queueAtLine(e *Engine, lane *Lane) bool {
	back := lane.Length - 10
	if back < 0 {
		back = 0
	}
	for _, v := range e.Vehicles() {
		if v.Lane == lane && v.S >= back {
			return true
		}
	}
	return false
}
