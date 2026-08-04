package engine

import "testing"

// permissivegreen_test.go — a permissive green ('g') must yield; a protected
// green ('G') must not (ADR-0011 addendum, 2026-07-28).
//
// The SUMO tlLogic alphabet distinguishes the two, and the distinction is the
// whole content of a permissive movement: the light is green for the turn AND
// green for the stream it crosses, so the signal has expressed the conflict
// rather than separated it, and the driver does the yielding. mapSigChar
// folds both chars into SigGreen — correct for "may I pass the line", wrong
// for "who gives way" — and rowGate used to return before the priority model
// on any green signalised approach. Every permissive movement therefore
// behaved as protected.
//
// On the Chicago import that was 2,008 of 13,181 signal links, all of them
// with foes: junction 256591534 alone booked 66% of a 90-minute run's overlap
// observations, its phase 0 ("GGgrrrGGg") discharging permissive link 2 and
// always-green link 6 into one exit lane every cycle.

// permissiveNetFile is a crossing junction whose two movements are green at
// the SAME time under a single-phase program: iJ_0 (approach A) holds the
// state char at index 0, iJ_1 (approach B) the char at index 1. Passing
// "gG" makes A permissive against a protected B; "GG" makes both protected,
// which is the pre-fix behavior and the control case.
//
// One phase only, so no vehicle is ever amber- or red-committed: whatever the
// gate does here it does because of the g/G distinction and nothing else.
func permissiveNetFile(state string) *NetFile {
	link0, link1 := 0, 1
	return &NetFile{
		Version: 1,
		Name:    "permissive-cross",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 100, SpeedLimit: 13.89, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J",
				TL: "J", TLLink: &link0, FoesCross: []string{"iJ_1"}, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
			{ID: "nB_0", Section: "B", Length: 200, SpeedLimit: 13.89, Successors: []string{"iJ_1"}},
			{ID: "iJ_1", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J",
				TL: "J", TLLink: &link1, FoesCross: []string{"iJ_0"}, Successors: []string{"nY_0"}},
			{ID: "nY_0", Section: "Y", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
		Signals: []NetSignal{{
			ID: "J", Junction: "J",
			Phases: []NetSignalPhase{{Duration: 300, State: state}},
		}},
	}
}

// A permissive approach yields to its crossing foe exactly as a minor
// approach does: it sheds speed at the line, never shares the box, and still
// gets through once the conflict clears.
func TestPermissiveGreenYields(t *testing.T) {
	e := newFileEngine(t, permissiveNetFile("gG"), 400)
	a := laneOf(t, e, "nA_0")
	b := laneOf(t, e, "nB_0")
	iJ0, iJ1 := laneOf(t, e, "iJ_0"), laneOf(t, e, "iJ_1")

	// Same geometry as TestMinorYieldsToMajor: the permissive vehicle is
	// close enough to the line to be gated, the protected one too close to
	// brake comfortably, so they contend for the box on overlapping ticks.
	perm := e.AddInitialVehicle(a, 0, 80, 10, 1)
	prot := e.AddInitialVehicle(b, 0, 150, 13.89, 1)

	yielded, permEnteredBox, protInBoxFirst := false, false, false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		if perm.Lane == a && perm.V < 3 {
			yielded = true
		}
		if prot.Lane == iJ1 && !permEnteredBox {
			protInBoxFirst = true
		}
		if perm.Lane == iJ0 {
			permEnteredBox = true
			if !protInBoxFirst {
				t.Fatalf("tick %d: permissive entered the box before the protected vehicle", e.Tick)
			}
			if prot.Lane == iJ1 {
				t.Fatalf("tick %d: permissive and protected in the conflict box together", e.Tick)
			}
		}
	}
	if !yielded {
		t.Error("permissive vehicle never yielded (speed never shed on the approach)")
	}
	if !permEnteredBox {
		t.Error("starvation: permissive vehicle never entered the junction")
	}
	if perm.Lane == a || perm.Lane == iJ0 {
		t.Errorf("starvation: permissive vehicle stuck at %s after 40 s", perm.Lane.ID)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// The control: with both movements PROTECTED the gate must not yield. This
// is what every permissive movement used to do, so it is also the assertion
// that would have failed had the fix been written as "signalised approaches
// always consult the priority model" — protected green means the light has
// already adjudicated, and re-checking foes there would make signals yield to
// each other and starve.
func TestProtectedGreenDoesNotYield(t *testing.T) {
	e := newFileEngine(t, permissiveNetFile("GG"), 400)
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	v := e.AddInitialVehicle(a, 0, 80, 10, 1)
	e.AddInitialVehicle(laneOf(t, e, "nB_0"), 0, 150, 13.89, 1)

	slowed := false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		if v.Lane == a && v.V < 3 {
			slowed = true
		}
	}
	if slowed {
		t.Error("protected approach yielded at the line; green must not consult the priority model")
	}
	if v.Lane == a || v.Lane == iJ0 {
		t.Errorf("protected vehicle stuck at %s after 40 s", v.Lane.ID)
	}
}

// sigPermissive reads the char in force at the CURRENT tick, not a property
// of the lane: the same link is permissive in one phase and protected in the
// next, and a cached answer would apply the wrong discipline for a whole
// phase. Two phases of 10 s each, link 0 'g' then 'G'.
func TestSigPermissiveFollowsThePhase(t *testing.T) {
	nf := permissiveNetFile("gG")
	nf.Signals[0].Phases = []NetSignalPhase{
		{Duration: 10, State: "gG"},
		{Duration: 10, State: "GG"},
	}
	e := newFileEngine(t, nf, 1)
	iJ0 := laneOf(t, e, "iJ_0")

	// dt = 0.1 s, so phase 0 spans ticks 0-99 and phase 1 ticks 100-199.
	for _, c := range []struct {
		tick uint64
		want bool
	}{{0, true}, {99, true}, {100, false}, {199, false}, {200, true}} {
		e.Tick = c.tick
		if got := e.sigPermissive(iJ0); got != c.want {
			t.Errorf("tick %d: sigPermissive = %v, want %v", c.tick, got, c.want)
		}
		if e.sigState(iJ0) != SigGreen {
			t.Errorf("tick %d: state is not green — the fixture no longer isolates g vs G", c.tick)
		}
	}
	// A link with no state char is not permissive (and exerts no control).
	iJ1 := laneOf(t, e, "iJ_1")
	saved := iJ1.LinkIdx
	iJ1.LinkIdx = 99
	if e.sigPermissive(iJ1) {
		t.Error("out-of-range link reported permissive")
	}
	iJ1.LinkIdx = saved
}
