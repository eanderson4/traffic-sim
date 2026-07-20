package netimport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// The signal fixture (ADR-0011): junction J1 carries a static tlLogic —
// the program compiles and both internal lanes bind to their link indices.
// Junction J2's tlLogic is actuated — unsupported: reported, its approaches
// stay unsignalized, and J2 remains on the unmodeled list.
func TestSignalFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "signal.net.xml"))
	if err != nil {
		t.Fatal(err)
	}
	nf, rep, err := Convert(data, Options{Name: "signal", SourceFile: "signal.net.xml", Source: "netimport (test)"})
	if err != nil {
		t.Fatal(err)
	}

	// The static program compiles: id, junction, offset, phases in order.
	if len(nf.Signals) != 1 {
		t.Fatalf("%d signal programs, want 1 (J1 only; J2 is actuated)", len(nf.Signals))
	}
	sig := nf.Signals[0]
	if sig.ID != "J1" || sig.Junction != "J1" {
		t.Errorf("program id/junction = %q/%q, want J1/J1", sig.ID, sig.Junction)
	}
	if sig.Offset != 5 {
		t.Errorf("offset = %v, want 5", sig.Offset)
	}
	wantPhases := []engine.NetSignalPhase{
		{Duration: 40, State: "GG"},
		{Duration: 4, State: "yy"},
		{Duration: 6, State: "rr"},
	}
	if len(sig.Phases) != len(wantPhases) {
		t.Fatalf("phases = %v, want %v", sig.Phases, wantPhases)
	}
	for i, w := range wantPhases {
		if sig.Phases[i] != w {
			t.Errorf("phase %d = %+v, want %+v", i, sig.Phases[i], w)
		}
	}

	// Internal lanes bind to (program, link index); tl-bound approaches
	// carry no row class (the light governs, ADR-0010 free-traversal
	// fallback only when the light is off).
	check := func(id, tl string, link int) {
		t.Helper()
		nl := laneByID(nf, id)
		if nl == nil {
			t.Fatalf("lane %s not compiled", id)
		}
		if nl.TL != tl {
			t.Errorf("%s: tl = %q, want %q", id, nl.TL, tl)
		}
		if tl == "" {
			if nl.TLLink != nil {
				t.Errorf("%s: tlLink = %d, want absent", id, *nl.TLLink)
			}
		} else if nl.TLLink == nil || *nl.TLLink != link {
			t.Errorf("%s: tlLink = %v, want %d", id, nl.TLLink, link)
		}
		if nl.Row != "" {
			t.Errorf("%s: row = %q, want \"\" (signal-governed)", id, nl.Row)
		}
	}
	check("iJ1_0_0", "J1", 0)
	check("iJ1_1_0", "J1", 1)
	check("iJ2_0_0", "", 0) // actuated program: unsignalized
	check("iJ2_1_0", "", 0)

	// Report: the program and its links count; J2 stays on the unmodeled
	// list with the unsupported-type warning; J1 drops off it.
	if rep.SignalPrograms != 1 || rep.SignalLinks != 2 {
		t.Errorf("report programs/links = %d/%d, want 1/2", rep.SignalPrograms, rep.SignalLinks)
	}
	if len(rep.SignalizedJunctions) != 1 || rep.SignalizedJunctions[0] != "J2" {
		t.Errorf("unmodeled signalized junctions = %v, want [J2]", rep.SignalizedJunctions)
	}
	found := false
	for _, w := range rep.Warnings {
		if strings.Contains(w, "J2") && strings.Contains(w, "actuated") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning about the unsupported actuated program J2: %v", rep.Warnings)
	}

	// The compiled file must load.
	if _, err := engine.CompileNet(nf); err != nil {
		t.Fatalf("compiled signal fixture does not load: %v", err)
	}
}
