package engine

import "testing"

// permissivedeadlock_test.go — two permissive greens that are each other's
// foes must not both yield forever (ADR-0033, amending ADR-0031).

// permissiveMergeNetFile is two approaches merging into ONE exit under a
// single-phase program, so each internal lane is the other's merge foe and
// both are green for the whole run. `state` sets the two chars: "gg" makes
// both permissive, which is the deadlock case.
//
// Single phase on purpose: nothing here is ever amber or red, so a vehicle
// that never moves was stopped by the priority model and by nothing else.
func permissiveMergeNetFile(state string) *NetFile {
	link0, link1 := 0, 1
	return &NetFile{
		Version: 1,
		Name:    "permissive-merge",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 100, SpeedLimit: 13.89, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J",
				TL: "J", TLLink: &link0, FoesMerge: []string{"iJ_1"}, Successors: []string{"nX_0"}},
			{ID: "nB_0", Section: "B", Length: 100, SpeedLimit: 13.89, Successors: []string{"iJ_1"}},
			{ID: "iJ_1", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J",
				TL: "J", TLLink: &link1, FoesMerge: []string{"iJ_0"}, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 400, SpeedLimit: 13.89, Exit: true},
		},
		Signals: []NetSignal{{
			ID: "J", Junction: "J",
			Phases: []NetSignalPhase{{Duration: 300, State: state}},
		}},
	}
}

// Two permissive greens facing each other must resolve, not stand off. Each
// is required to yield to the other, so SOMETHING has to break the tie — the
// vehicle-ID rule foeApproachBlocks already uses for minor-vs-minor.
//
// This is the failure ADR-0031 introduced: it routed permissive approaches
// into rowConflict as minor, but foeApproachBlocks reads foe.Row to decide
// whether the foe yields too, and a signalised internal lane carries RowNone
// — "unmodeled, has priority". So each permissive vehicle treated the other
// as a priority foe and gave way to it, forever.
func TestPermissiveGreensDoNotDeadlock(t *testing.T) {
	e := newFileEngine(t, permissiveMergeNetFile("gg"), 1200)
	a, b := laneOf(t, e, "nA_0"), laneOf(t, e, "nB_0")

	// Both stopped at their lines, the standoff geometry exactly.
	va := e.AddInitialVehicle(a, 0, 98, 0, 1)
	vb := e.AddInitialVehicle(b, 0, 98, 0, 1)

	for e.Tick < 1200 {
		e.Step()
		assertNoNaN(t, e)
		if e.Stats.Collisions != 0 {
			t.Fatalf("tick %d: %d collision observations — the tie broke by both going",
				e.Tick, e.Stats.Collisions)
		}
	}
	// Both must have got through. 120 s is ample for two vehicles to cross a
	// 10 m box one after the other.
	if va.Lane == a && vb.Lane == b {
		t.Fatal("DEADLOCK: neither permissive vehicle ever left its approach")
	}
	if va.Lane == a {
		t.Errorf("vehicle A never left its approach (B at %v)", laneID(vb))
	}
	if vb.Lane == b {
		t.Errorf("vehicle B never left its approach (A at %v)", laneID(va))
	}
}

// The control: a permissive approach against a PROTECTED foe still yields.
// Breaking the deadlock must not become "permissive greens stop yielding".
func TestPermissiveStillYieldsToProtectedMergeFoe(t *testing.T) {
	e := newFileEngine(t, permissiveMergeNetFile("gG"), 600)
	a, b := laneOf(t, e, "nA_0"), laneOf(t, e, "nB_0")
	iJ0, iJ1 := laneOf(t, e, "iJ_0"), laneOf(t, e, "iJ_1")

	perm := e.AddInitialVehicle(a, 0, 98, 0, 1) // link 0: 'g'
	prot := e.AddInitialVehicle(b, 0, 98, 0, 1) // link 1: 'G'

	permIn, protIn := false, false
	for e.Tick < 600 {
		e.Step()
		assertNoNaN(t, e)
		if perm.Lane == iJ0 && !protIn {
			permIn = true
		}
		if prot.Lane == iJ1 {
			protIn = true
		}
		if e.Stats.Collisions != 0 {
			t.Fatalf("tick %d: %d collision observations", e.Tick, e.Stats.Collisions)
		}
	}
	if permIn {
		t.Error("the permissive vehicle entered the box before the protected one")
	}
	if perm.Lane == a {
		t.Error("the permissive vehicle never got through at all (starved)")
	}
}

func laneID(v *Vehicle) string {
	if v.Lane == nil {
		return "(despawned)"
	}
	return v.Lane.ID
}
