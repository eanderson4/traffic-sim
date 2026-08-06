package engine

import "testing"

// clusterseam_test.go — controlled internal→internal boundaries (the ADR-0038
// cluster seams). Consolidation rewires a deleted sliver's predecessors
// directly into the far junction's internal lanes, so a movement that used to
// approach the far box on a road lane — with the full rowGate — now crosses
// internal→internal. The stop line and the approaching-foe yield discipline
// must apply at that seam exactly as on a road approach (Sol blocker,
// ADR-0038 review).

// seamNetFile chains junction A's internal lane straight into junction B's
// conflicted internal lane: nA → iA (uncontrolled) → iB (class per
// variant) → nX, with iC the conflicting (major) movement fed by nC.
func seamNetFile() *NetFile {
	return &NetFile{
		Version: 1,
		Name:    "cluster-seam",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 100, SpeedLimit: 13.89, Successors: []string{"iA_0"}},
			{ID: "iA_0", Section: "j:A", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "A", Successors: []string{"iB_0"}},
			{ID: "iB_0", Section: "j:B", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "B", Row: "minor",
				FoesCross: []string{"iC_0"}, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
			{ID: "nC_0", Section: "C", Length: 200, SpeedLimit: 13.89, Successors: []string{"iC_0"}},
			{ID: "iC_0", Section: "j:B", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "B", Row: "major",
				FoesCross: []string{"iB_0"}, Successors: []string{"nY_0"}},
			{ID: "nY_0", Section: "Y", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
	}
}

// A vehicle waiting on junction A's internal must YIELD at the seam to a
// major foe committed to the conflicting movement — exactly as it would have
// on the pre-consolidation road sliver. Pre-fix it crossed internal→internal
// with only exitBlocked and entered against the approaching foe.
func TestSeamMinorYieldsToApproachingFoe(t *testing.T) {
	e := newFileEngine(t, seamNetFile(), 400)
	iA := laneOf(t, e, "iA_0")
	iB := laneOf(t, e, "iB_0")
	iC := laneOf(t, e, "iC_0")
	c := laneOf(t, e, "nC_0")

	ego := e.AddInitialVehicle(iA, 0, 5, 8, 1)      // 5 m from the seam
	foe := e.AddInitialVehicle(c, 0, 150, 13.89, 1) // 50 m out, too close to brake comfortably

	yielded, foeInBoxFirst := false, false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		if ego.Lane == iA && ego.V < 3 {
			yielded = true
		}
		if foe.Lane == iC && ego.Lane != iB {
			foeInBoxFirst = true
		}
		if ego.Lane == iB {
			if !foeInBoxFirst {
				t.Fatalf("tick %d: ego crossed the seam before the committed foe reached the box", e.Tick)
			}
			if foe.Lane == iC {
				t.Fatalf("tick %d: ego and foe in the conflict box together", e.Tick)
			}
		}
	}
	if !yielded {
		t.Error("ego never yielded at the seam (speed never shed on iA)")
	}
	if ego.Lane == iA || ego.Lane == iB {
		t.Errorf("starvation: ego stuck at %s after 40 s", ego.Lane.ID)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// Control: with no foe committed, the seam passes freely — the fix must not
// wall every internal→internal crossing.
func TestSeamPassesWithNoConflict(t *testing.T) {
	e := newFileEngine(t, seamNetFile(), 400)
	iA := laneOf(t, e, "iA_0")
	ego := e.AddInitialVehicle(iA, 0, 5, 10, 1)
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
	}
	if ego.Lane == iA {
		t.Error("ego walled at an unconflicted seam")
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// seamStopNetFile puts a STOP-class far internal at the seam, with a long
// enough upstream internal that the vehicle can physically stop there
// (car B=1.67: 8 m/s needs ~19 m).
func seamStopNetFile() *NetFile {
	nf := seamNetFile()
	for i := range nf.Lanes {
		switch nf.Lanes[i].ID {
		case "iA_0":
			nf.Lanes[i].Length = 30
		case "iB_0":
			nf.Lanes[i].Row = "stop"
		}
	}
	return nf
}

// A stop-class far internal crossed internal→internal enforces the full-stop
// duty AT THE SEAM: with no foe at all the vehicle must still reach V=0 on
// the upstream internal (a rolling stop at speed is the round-2 blocker), and
// once the stop is done the seam serves it (no starvation).
func TestSeamStopClassFullStop(t *testing.T) {
	e := newFileEngine(t, seamStopNetFile(), 600)
	iA := laneOf(t, e, "iA_0")
	iB := laneOf(t, e, "iB_0")
	ego := e.AddInitialVehicle(iA, 0, 0, 8, 1)

	fullStop := false
	for e.Tick < 600 {
		e.Step()
		assertNoNaN(t, e)
		if ego.Lane == iA && ego.V == 0 {
			fullStop = true
		}
		if ego.Lane == iB && !fullStop {
			t.Fatalf("tick %d: ego crossed a stop-class seam at speed %v without stopping", e.Tick, ego.V)
		}
	}
	if !fullStop {
		t.Error("ego never reached a full stop at the stop-class seam")
	}
	if ego.Lane == iA || ego.Lane == iB {
		t.Errorf("starvation: ego stuck at %s after the stop duty", ego.Lane.ID)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// seamSignalNetFile is the same seam with junction B signalized: iB permissive
// ('g'), iC protected ('G'), one phase so only the g/G distinction acts
// (permissiveNetFile's construction carried across the seam).
func seamSignalNetFile() *NetFile {
	link0, link1 := 0, 1
	nf := seamNetFile()
	for i := range nf.Lanes {
		switch nf.Lanes[i].ID {
		case "iB_0":
			nf.Lanes[i].Row = ""
			nf.Lanes[i].TL = "B"
			nf.Lanes[i].TLLink = &link0
		case "iC_0":
			nf.Lanes[i].Row = ""
			nf.Lanes[i].TL = "B"
			nf.Lanes[i].TLLink = &link1
		}
	}
	nf.Signals = []NetSignal{{ID: "B", Junction: "B",
		Phases: []NetSignalPhase{{Duration: 300, State: "gG"}}}}
	return nf
}

// A permissive movement crossing the seam must yield to its protected foe
// even though the vehicle approaches from INSIDE an upstream box.
func TestSeamPermissiveYieldsToApproachingFoe(t *testing.T) {
	e := newFileEngine(t, seamSignalNetFile(), 400)
	iA := laneOf(t, e, "iA_0")
	iB := laneOf(t, e, "iB_0")
	iC := laneOf(t, e, "iC_0")
	c := laneOf(t, e, "nC_0")

	ego := e.AddInitialVehicle(iA, 0, 5, 8, 1)
	foe := e.AddInitialVehicle(c, 0, 150, 13.89, 1)

	yielded, foeInBoxFirst := false, false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		if ego.Lane == iA && ego.V < 3 {
			yielded = true
		}
		if foe.Lane == iC && ego.Lane != iB {
			foeInBoxFirst = true
		}
		if ego.Lane == iB {
			if !foeInBoxFirst {
				t.Fatalf("tick %d: permissive ego crossed the seam before the protected foe", e.Tick)
			}
			if foe.Lane == iC {
				t.Fatalf("tick %d: ego and foe in the conflict box together", e.Tick)
			}
		}
	}
	if !yielded {
		t.Error("permissive ego never yielded at the seam")
	}
	if ego.Lane == iA || ego.Lane == iB {
		t.Errorf("starvation: permissive ego stuck at %s after 40 s", ego.Lane.ID)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}
