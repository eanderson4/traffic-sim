package engine

import (
	"testing"
)

// fixture_signalctl_test.go — the ADR-0037 milestone-1 proof fixture: no
// controller (that is milestone 2), just the kernel verb driven by script.
// On the two-approach junction of sig2NetFile the test holds one
// approach RED through what would have been its green window and asserts
// the three properties the design promises: the held approach's queue
// GROWS (the command really starves the movement), the cross movement
// FLOWS (the commanded phase serves it the whole hold), and when the hold
// lapses at its bound the fixed-time program resumes and the queue
// DISCHARGES. The enforcement invariants ride along: zero collisions, and
// the held approach sees red compliance (no uncommitted crossings — the
// signal-4way fixture's criterion (a), which the override path inherits
// unchanged).
//
// Calibration note: discharge from a standing queue through one box runs
// at ~7.5 s/veh in this model (accel-limited crawl from rest, the same
// serialized band the signal-4way fixture documents), so the program is
// asymmetric — 45 s "Gr" / 15 s "rG" — to give the starved approach the
// green share its post-lapse drain needs.

// TestFixtureSignalHoldStarve: see the file comment. The 600-tick cycle
// gives A green [0,450) of each [0,600); the verb holds phase 1 — A red,
// B green — from tick 5 for 900 ticks, i.e. STRAIGHT THROUGH A's
// fixed-time green [600,1050). A's demand (300 veh/h) queues; B's flows.
// Measured on this fixture (seed 1): queue 1 → 8 over the hold, 6
// cross-movement crossings during it, 47 crossings and a queue of 3 after
// the lapse. Assertions carry margin around those numbers; the run is
// deterministic, so they cannot flake — they can only catch a behavior
// change.
func TestFixtureSignalHoldStarve(t *testing.T) {
	nf := sig2NetFile([]NetSignalPhase{{45.0, "Gr"}, {15.0, "rG"}})
	spec := sigSpec(t, nf, 6000)
	spec.Scen = Scenario{SpawnRates: map[string]float64{"nA_0": 300, "nB_0": 300}}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	nA := laneOf(t, e, "nA_0")
	iA := laneOf(t, e, "iJ_0")
	iB := laneOf(t, e, "iJ_1")

	for e.Tick < 4 {
		e.Step()
	}
	// Hold A red from tick 5 through tick 904 (until is exclusive): one and
	// a half cycles, past the point where the fixed-time program would have
	// served A — the starvation the rail exists to bound.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-A-red", Signal: "J", Phase: 1, HoldTicks: 900}); err != nil {
		t.Fatal(err)
	}

	type prevState struct {
		lane *Lane
		s, v float64
	}
	prev := map[uint64]prevState{}
	queueAt100, queueAtLapse := -1, -1
	bCrossDuringHold, aCrossAfterLapse := 0, 0
	lapseSeenAt := uint64(0)
	uncommittedRed := 0

	for e.Tick < spec.Ticks {
		e.Step()
		assertNoNaN(t, e)

		if e.Tick == 100 {
			queueAt100 = queueOn(e, nA)
		}
		if lp := e.LapsedSignals(); len(lp) > 0 {
			if len(lp) != 1 || lp[0].Until != 905 || e.Tick != 905 {
				t.Fatalf("tick %d: lapse = %+v, want the single hold lapsing at 905", e.Tick, lp)
			}
			lapseSeenAt = e.Tick
			queueAtLapse = queueOn(e, nA)
		}

		// The hold must be IN FORCE during A's fixed-time green: at tick
		// 700 the schedule shows A green (phase 0, [600,1050)); the
		// override shows red.
		if e.Tick == 700 {
			if st := e.sigState(iA); st != SigRed {
				t.Fatalf("tick 700: held approach shows %v during its fixed-time green, want red", st)
			}
			if st := e.sigState(iB); st != SigGreen {
				t.Fatalf("tick 700: cross movement shows %v, want green for the whole hold", st)
			}
		}

		for _, v := range e.Vehicles() {
			p, seen := prev[v.ID]
			cur := v.Lane
			prev[v.ID] = prevState{cur, v.S, v.V}
			if !seen || p.lane == cur {
				continue
			}
			// Stop-line crossings into the two internal lanes.
			if cur == iB && e.Tick >= 5 && e.Tick < 905 {
				bCrossDuringHold++
			}
			if cur == iA {
				if e.Tick >= 905 {
					aCrossAfterLapse++
				} else if e.Tick >= 5 {
					// A crossing during the hold is legal only if the
					// vehicle was physically committed (v² > 2·d·B at the
					// previous tick — the fixture's criterion (a)).
					d := p.lane.Length - p.s
					if p.v*p.v <= 2*d*v.Type.B {
						uncommittedRed++
					}
				}
			}
		}
	}

	if lapseSeenAt != 905 {
		t.Fatalf("no lapse event at 905 (saw %d) — the rail did not fire", lapseSeenAt)
	}
	// The queue grew under the hold (measured 1 → 8).
	if queueAt100 > 2 || queueAtLapse < 5 {
		t.Errorf("held approach did not starve as expected: queue %d at tick 100, %d at the lapse (want ≤ 2 → ≥ 5)",
			queueAt100, queueAtLapse)
	}
	// The cross movement flowed through the whole hold (measured 6).
	if bCrossDuringHold < 5 {
		t.Errorf("cross movement starved too: %d crossings during the hold, want ≥ 5", bCrossDuringHold)
	}
	// Red compliance on the held approach: zero uncommitted crossings.
	if uncommittedRed != 0 {
		t.Errorf("%d uncommitted crossings of the held red, want 0", uncommittedRed)
	}
	// The lapse resumes the fixed-time program mid-window — A is green from
	// the lapse tick (905 sits in phase 0, [600,1050)) — and the queue
	// discharges (measured: 47 crossings, queue 8 → 3).
	if aCrossAfterLapse < 20 {
		t.Errorf("held approach did not discharge after the lapse: %d crossings, want ≥ 20", aCrossAfterLapse)
	}
	if end := queueOn(e, nA); end > queueAtLapse/2 {
		t.Errorf("queue did not drain after the lapse: %d at lapse, %d at run end", queueAtLapse, end)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0 (by section %v)", e.Stats.Collisions, e.Stats.CollisionsBySection)
	}
	t.Logf("queue %d@100 → %d@lapse → %d@end; B crossings during hold %d; A crossings after lapse %d",
		queueAt100, queueAtLapse, queueOn(e, nA), bCrossDuringHold, aCrossAfterLapse)
}
