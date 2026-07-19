package netimport

import (
	"os"
	"testing"

	"traffic-sim/engine"
)

// The corridor fixture's junction J1: two major through connections and one
// minor left turn, parallel paths — classes annotated, no conflicts.
func TestCorridorRightOfWay(t *testing.T) {
	nf, rep := convertFixture(t)
	rows := map[string]string{}
	for _, nl := range nf.Lanes {
		if nl.Internal {
			rows[nl.ID] = nl.Row
			if nl.Junction != "J1" {
				t.Errorf("internal lane %s: junction = %q, want J1", nl.ID, nl.Junction)
			}
			if len(nl.FoesCross) > 0 || len(nl.FoesMerge) > 0 {
				t.Errorf("internal lane %s: foes = %v/%v, want none (parallel paths)",
					nl.ID, nl.FoesCross, nl.FoesMerge)
			}
		}
	}
	want := map[string]string{"iJ1_0_0": "major", "iJ1_1_0": "major", "iJ1_2_0": "minor"}
	for id, w := range want {
		if rows[id] != w {
			t.Errorf("lane %s: row = %q, want %q", id, rows[id], w)
		}
	}
	if rep.YieldApproaches != 1 || rep.StopApproaches != 0 || rep.ConflictPairs != 0 {
		t.Errorf("report yield/stop/conflicts = %d/%d/%d, want 1/0/0",
			rep.YieldApproaches, rep.StopApproaches, rep.ConflictPairs)
	}
}

// The yield fixture: a major through path crossing a minor through path,
// plus a minor ramp merging into the major exit — one crossing pair, one
// merge pair, and one more crossing pair (merge precedence checked).
func TestYieldFixtureConflicts(t *testing.T) {
	data, err := os.ReadFile("testdata/yield.net.xml")
	if err != nil {
		t.Fatal(err)
	}
	nf, rep, err := Convert(data, Options{Name: "yield", SourceFile: "yield.net.xml"})
	if err != nil {
		t.Fatal(err)
	}
	lane := func(id string) *engine.NetLane {
		for i := range nf.Lanes {
			if nf.Lanes[i].ID == id {
				return &nf.Lanes[i]
			}
		}
		t.Fatalf("lane %s not compiled", id)
		return nil
	}
	major := lane("iJ1_0_0")
	if major.Row != "major" || major.Junction != "J1" {
		t.Errorf("iJ1_0_0: row/junction = %q/%q, want major/J1", major.Row, major.Junction)
	}
	if len(major.FoesCross) != 1 || major.FoesCross[0] != "iJ1_1_0" {
		t.Errorf("iJ1_0_0 foesCross = %v, want [iJ1_1_0]", major.FoesCross)
	}
	if len(major.FoesMerge) != 1 || major.FoesMerge[0] != "iJ1_2_0" {
		t.Errorf("iJ1_0_0 foesMerge = %v, want [iJ1_2_0]", major.FoesMerge)
	}
	minor := lane("iJ1_1_0")
	if minor.Row != "minor" {
		t.Errorf("iJ1_1_0: row = %q, want minor", minor.Row)
	}
	if len(minor.FoesCross) != 2 {
		t.Errorf("iJ1_1_0 foesCross = %v, want 2 entries (major path and ramp)", minor.FoesCross)
	}
	ramp := lane("iJ1_2_0")
	if ramp.Row != "minor" {
		t.Errorf("iJ1_2_0: row = %q, want minor", ramp.Row)
	}
	if len(ramp.FoesMerge) != 1 || ramp.FoesMerge[0] != "iJ1_0_0" {
		t.Errorf("iJ1_2_0 foesMerge = %v, want [iJ1_0_0] (merge takes precedence over crossing)",
			ramp.FoesMerge)
	}
	if rep.YieldApproaches != 2 || rep.StopApproaches != 0 || rep.ConflictPairs != 3 {
		t.Errorf("report yield/stop/conflicts = %d/%d/%d, want 2/0/3",
			rep.YieldApproaches, rep.StopApproaches, rep.ConflictPairs)
	}

	// The compiled file must load: foes resolve to internal lanes.
	if _, err := engine.CompileNet(nf); err != nil {
		t.Fatalf("compiled yield fixture does not load: %v", err)
	}
}
