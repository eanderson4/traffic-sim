package netimport

// consolidate_test.go — ADR-0038 junction-cluster consolidation.
//
// The fixtures are hand-authored .net.xml docs in the signal.net.xml style.
// Topology per fixture: an arterial E0 enters junction J1, a short connector
// edge S links J1 to junction J2 (the sliver), and E2 leaves J2. J2 is
// signalized with a static tlLogic so the tests pin that signal bindings
// survive consolidation.

import (
	"strings"
	"testing"

	"traffic-sim/engine"
)

// convertXML builds the two-junction fixture with connector length sliverLen.
func convertXML(t *testing.T, doc string) (*engine.NetFile, *Report) {
	t.Helper()
	nf, rep, err := Convert([]byte(doc), Options{Name: "t", SourceFile: "t.net.xml", Source: "netimport (test)"})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return nf, rep
}

const sliverFixture = `<?xml version="1.0" encoding="UTF-8"?>
<net version="1.16">
    <location netOffset="-100.00,-50.00" convBoundary="0.00,0.00,400.00,20.00" origBoundary="-122.001,37.000,-121.998,37.002" projParameter="+proj=utm +zone=10"/>

    <edge id="E0" from="J0" to="J1" priority="3">
        <lane id="E0_0" index="0" speed="13.89" length="100.00" width="3.50" shape="0.00,3.25 100.00,3.25"/>
    </edge>
    <edge id="S" from="J1" to="J2" priority="3">
        <lane id="S_0" index="0" speed="13.89" length="SLIVERLEN" width="3.50" shape="103.00,3.25 SLIVEREND,3.25"/>
    </edge>
    <edge id="E2" from="J2" to="J3" priority="3">
        <lane id="E2_0" index="0" speed="13.89" length="100.00" width="3.50" shape="E2START,3.25 E2END,3.25"/>
    </edge>

    <junction id="J0" type="dead_end" x="0.00" y="3.25" incLanes="" intLanes="" shape=""/>
    <junction id="J1" type="priority" x="101.50" y="3.25" incLanes="E0_0" intLanes=":J1_0_0" shape="100.00,1.50 103.00,1.50 103.00,5.00 100.00,5.00"/>
    <junction id="J2" type="traffic_light" x="J2X,3.25" incLanes="S_0" intLanes=":J2_0_0" shape="J2X0,1.50 J2X1,1.50 J2X1,5.00 J2X0,5.00"/>
    <junction id="J3" type="dead_end" x="J3X,3.25" incLanes="E2_0" intLanes="" shape=""/>

    <edge id=":J1_0" function="internal">
        <lane id=":J1_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="100.00,3.25 103.00,3.25"/>
    </edge>
    <edge id=":J2_0" function="internal">
        <lane id=":J2_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="J2X0,3.25 J2X1,3.25"/>
    </edge>

    <tlLogic id="J2" type="static" programID="0" offset="0">
        <phase duration="40" state="G"/>
        <phase duration="5"  state="r"/>
    </tlLogic>

    <connection from="E0" to="S" fromLane="0" toLane="0" via=":J1_0_0" dir="s" state="M"/>
    <connection from="S" to="E2" fromLane="0" toLane="0" via=":J2_0_0" tl="J2" linkIndex="0" dir="s" state="O"/>
</net>
`

func fixture(sliverLen, sliverEnd, j2x0, j2x1, e2start, e2end, j2x, j3x string) string {
	// Ordered: the longer placeholders must substitute before their prefixes.
	repl := [][2]string{
		{"SLIVERLEN", sliverLen}, {"SLIVEREND", sliverEnd},
		{"J2X0", j2x0}, {"J2X1", j2x1}, {"E2START", e2start}, {"E2END", e2end},
		{"J2X", j2x}, {"J3X", j3x},
	}
	s := sliverFixture
	for _, kv := range repl {
		s = strings.ReplaceAll(s, kv[0], kv[1])
	}
	return s
}

// Two junctions linked by a 2 m sliver: the sliver is deleted, the near
// junction's internal is rewired into the far junction's (signalized)
// internal, and that internal grows by the sliver length and shape while
// keeping its tl binding.
func TestConsolidateTwoJunctions(t *testing.T) {
	nf, rep := convertXML(t, fixture("2.00", "105.00", "105.00", "108.00", "108.00", "208.00", "106.50", "208.00"))

	if got, want := len(rep.ConsolidatedSlivers), 1; got != want {
		t.Fatalf("ConsolidatedSlivers = %v, want 1 entry", rep.ConsolidatedSlivers)
	}
	if rep.ConsolidatedSlivers[0] != "nS_0" {
		t.Errorf("deleted lane = %q, want nS_0", rep.ConsolidatedSlivers[0])
	}
	if laneByID(nf, "nS_0") != nil {
		t.Error("sliver lane nS_0 survived consolidation")
	}
	if len(nf.Lanes) != 4 {
		t.Errorf("%d lanes after consolidation, want 4 (E0, E2, 2 internals)", len(nf.Lanes))
	}

	near := laneByID(nf, "iJ1_0_0")
	if got := near.Successors; len(got) != 1 || got[0] != "iJ2_0_0" {
		t.Errorf("iJ1_0_0 successors = %v, want [iJ2_0_0]", got)
	}
	far := laneByID(nf, "iJ2_0_0")
	if far.Length != 5.0 {
		t.Errorf("iJ2_0_0 length = %v, want 5.0 (3 + 2 sliver)", far.Length)
	}
	wantShape := [][2]float64{{103, 3.25}, {105, 3.25}, {108, 3.25}}
	if len(far.Shape) != len(wantShape) {
		t.Fatalf("iJ2_0_0 shape = %v, want %v", far.Shape, wantShape)
	}
	for i, p := range wantShape {
		if far.Shape[i] != p {
			t.Fatalf("iJ2_0_0 shape = %v, want %v", far.Shape, wantShape)
		}
	}
	if far.TL != "J2" || far.TLLink == nil || *far.TLLink != 0 {
		t.Errorf("iJ2_0_0 signal binding = tl %q link %v, want J2/0", far.TL, far.TLLink)
	}
	if len(nf.Signals) != 1 || nf.Signals[0].ID != "J2" {
		t.Errorf("signals = %+v, want the J2 program retained", nf.Signals)
	}

	// Demand-facing flags untouched: E0 origin, E2 exit, neither deleted.
	if e0 := laneByID(nf, "nE0_0"); e0 == nil || !e0.Origin {
		t.Error("nE0_0 lost or no longer origin")
	}
	if e2 := laneByID(nf, "nE2_0"); e2 == nil || !e2.Exit {
		t.Error("nE2_0 lost or no longer exit")
	}
}

// A 6 m connector is above threshold: nothing consolidates.
func TestConsolidateAboveThresholdIsInert(t *testing.T) {
	nf, rep := convertXML(t, fixture("6.00", "109.00", "109.00", "112.00", "112.00", "212.00", "110.50", "212.00"))
	if len(rep.ConsolidatedSlivers) != 0 {
		t.Fatalf("ConsolidatedSlivers = %v, want none at 6 m", rep.ConsolidatedSlivers)
	}
	if len(nf.Lanes) != 5 {
		t.Errorf("%d lanes, want 5 (untouched)", len(nf.Lanes))
	}
	if got := laneByID(nf, "iJ1_0_0").Successors; len(got) != 1 || got[0] != "nS_0" {
		t.Errorf("iJ1_0_0 successors = %v, want [nS_0]", got)
	}
}

const chainFixture = `<?xml version="1.0" encoding="UTF-8"?>
<net version="1.16">
    <location netOffset="0,0" convBoundary="0.00,0.00,400.00,20.00" origBoundary="0,0,1,1" projParameter="+proj=utm +zone=10"/>

    <edge id="E0" from="J0" to="J1" priority="3">
        <lane id="E0_0" index="0" speed="13.89" length="100.00" width="3.50" shape="0.00,3.25 100.00,3.25"/>
    </edge>
    <edge id="S1" from="J1" to="JM" priority="3">
        <lane id="S1_0" index="0" speed="13.89" length="2.00" width="3.50" shape="103.00,3.25 105.00,3.25"/>
    </edge>
    <edge id="S2" from="JM" to="J2" priority="3">
        <lane id="S2_0" index="0" speed="13.89" length="3.00" width="3.50" shape="108.00,3.25 111.00,3.25"/>
    </edge>
    <edge id="E3" from="J2" to="J3" priority="3">
        <lane id="E3_0" index="0" speed="13.89" length="100.00" width="3.50" shape="114.00,3.25 214.00,3.25"/>
    </edge>

    <junction id="J0" type="dead_end" x="0.00" y="3.25" incLanes="" intLanes="" shape=""/>
    <junction id="J1" type="priority" x="101.50" y="3.25" incLanes="E0_0" intLanes=":J1_0_0" shape="100.00,1.50 103.00,1.50 103.00,5.00 100.00,5.00"/>
    <junction id="JM" type="priority" x="106.50" y="3.25" incLanes="S1_0" intLanes=":JM_0_0" shape="105.00,1.50 108.00,1.50 108.00,5.00 105.00,5.00"/>
    <junction id="J2" type="traffic_light" x="112.50" y="3.25" incLanes="S2_0" intLanes=":J2_0_0" shape="111.00,1.50 114.00,1.50 114.00,5.00 111.00,5.00"/>
    <junction id="J3" type="dead_end" x="214.00" y="3.25" incLanes="E3_0" intLanes="" shape=""/>

    <edge id=":J1_0" function="internal">
        <lane id=":J1_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="100.00,3.25 103.00,3.25"/>
    </edge>
    <edge id=":JM_0" function="internal">
        <lane id=":JM_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="105.00,3.25 108.00,3.25"/>
    </edge>
    <edge id=":J2_0" function="internal">
        <lane id=":J2_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="111.00,3.25 114.00,3.25"/>
    </edge>

    <tlLogic id="J2" type="static" programID="0" offset="0">
        <phase duration="40" state="G"/>
        <phase duration="5"  state="r"/>
    </tlLogic>

    <connection from="E0" to="S1" fromLane="0" toLane="0" via=":J1_0_0" dir="s" state="M"/>
    <connection from="S1" to="S2" fromLane="0" toLane="0" via=":JM_0_0" dir="s" state="M"/>
    <connection from="S2" to="E3" fromLane="0" toLane="0" via=":J2_0_0" tl="J2" linkIndex="0" dir="s" state="O"/>
</net>
`

// A chain of two slivers (2 m then 3 m) consolidates recursively: both are
// deleted, the internals chain through, and each internal grows by the sliver
// it swallowed (iJM +2, iJ2 +3).
func TestConsolidateChainRecursive(t *testing.T) {
	nf, rep := convertXML(t, chainFixture)
	if len(rep.ConsolidatedSlivers) != 2 {
		t.Fatalf("ConsolidatedSlivers = %v, want 2 (chain)", rep.ConsolidatedSlivers)
	}
	if laneByID(nf, "nS1_0") != nil || laneByID(nf, "nS2_0") != nil {
		t.Error("a chain sliver survived")
	}
	if got := laneByID(nf, "iJ1_0_0").Successors; len(got) != 1 || got[0] != "iJM_0_0" {
		t.Errorf("iJ1_0_0 successors = %v, want [iJM_0_0]", got)
	}
	mid := laneByID(nf, "iJM_0_0")
	if got := mid.Successors; len(got) != 1 || got[0] != "iJ2_0_0" {
		t.Errorf("iJM_0_0 successors = %v, want [iJ2_0_0]", got)
	}
	if mid.Length != 5.0 {
		t.Errorf("iJM_0_0 length = %v, want 5.0 (3 + 2)", mid.Length)
	}
	far := laneByID(nf, "iJ2_0_0")
	if far.Length != 6.0 {
		t.Errorf("iJ2_0_0 length = %v, want 6.0 (3 + 3)", far.Length)
	}
	if far.TL != "J2" {
		t.Errorf("iJ2_0_0 lost its signal binding: tl %q", far.TL)
	}
	// Total travel length across the chain is preserved: E0 100 + iJ1 3 +
	// iJM 5 + iJ2 6 + E3 100 = 214, same as the unconsolidated path.
	total := 0.0
	for _, id := range []string{"nE0_0", "iJ1_0_0", "iJM_0_0", "iJ2_0_0", "nE3_0"} {
		total += laneByID(nf, id).Length
	}
	if total != 214.0 {
		t.Errorf("path length = %v, want 214 (length preserved through consolidation)", total)
	}
}

// A short boundary stub feeding a junction is an ORIGIN lane: demand-facing,
// never deleted even though it is short and feeds only internals.
func TestConsolidateNeverDeletesOrigin(t *testing.T) {
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<net version="1.16">
    <location netOffset="0,0" convBoundary="0.00,0.00,400.00,20.00" origBoundary="0,0,1,1" projParameter="+proj=utm +zone=10"/>
    <edge id="S" from="J0" to="J1" priority="3">
        <lane id="S_0" index="0" speed="13.89" length="2.00" width="3.50" shape="0.00,3.25 2.00,3.25"/>
    </edge>
    <edge id="E2" from="J1" to="J3" priority="3">
        <lane id="E2_0" index="0" speed="13.89" length="100.00" width="3.50" shape="5.00,3.25 105.00,3.25"/>
    </edge>
    <junction id="J0" type="dead_end" x="0.00" y="3.25" incLanes="" intLanes="" shape=""/>
    <junction id="J1" type="priority" x="3.50" y="3.25" incLanes="S_0" intLanes=":J1_0_0" shape="2.00,1.50 5.00,1.50 5.00,5.00 2.00,5.00"/>
    <junction id="J3" type="dead_end" x="105.00" y="3.25" incLanes="E2_0" intLanes="" shape=""/>
    <edge id=":J1_0" function="internal">
        <lane id=":J1_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="2.00,3.25 5.00,3.25"/>
    </edge>
    <connection from="S" to="E2" fromLane="0" toLane="0" via=":J1_0_0" dir="s" state="M"/>
</net>
`
	nf, rep := convertXML(t, doc)
	s := laneByID(nf, "nS_0")
	if s == nil {
		t.Fatal("origin stub nS_0 was deleted")
	}
	if !s.Origin {
		t.Error("nS_0 should be flagged origin (no predecessors)")
	}
	if len(rep.ConsolidatedSlivers) != 0 {
		t.Errorf("ConsolidatedSlivers = %v, want none (origin stub is demand-facing)", rep.ConsolidatedSlivers)
	}
}

// fanoutFixture exercises the multi-successor side of consolidation: junction
// J1 has two movement internals exiting on separate slivers into junction J2,
// and the slivers fan out so that AFTER rewiring two J1 internals share a
// far-internal successor at NON-ZERO index — the case the Successors[0]-only
// merge-foe test missed. J2's :J2_1_0 via serves two connections (S1→EY and
// S3→EY), which is also the parallel double-extension case the
// SharedExtensions audit counter exists for.
const fanoutFixture = `<?xml version="1.0" encoding="UTF-8"?>
<net version="1.16">
    <location netOffset="0,0" convBoundary="0.00,0.00,400.00,60.00" origBoundary="0,0,1,1" projParameter="+proj=utm +zone=10"/>

    <edge id="E0" from="J0" to="J1" priority="3">
        <lane id="E0_0" index="0" speed="13.89" length="100.00" width="3.50" shape="0.00,3.25 100.00,3.25"/>
        <lane id="E0_1" index="1" speed="13.89" length="100.00" width="3.50" shape="0.00,6.75 100.00,6.75"/>
    </edge>
    <edge id="S1" from="J1" to="J2" priority="3">
        <lane id="S1_0" index="0" speed="13.89" length="2.00" width="3.50" shape="103.00,3.25 105.00,3.25"/>
    </edge>
    <edge id="S3" from="J1" to="J2" priority="3">
        <lane id="S3_0" index="0" speed="13.89" length="3.00" width="3.50" shape="103.00,6.75 106.00,6.75"/>
    </edge>
    <edge id="EX" from="J2" to="JX" priority="3">
        <lane id="EX_0" index="0" speed="13.89" length="100.00" width="3.50" shape="110.00,0.25 210.00,0.25"/>
    </edge>
    <edge id="EY" from="J2" to="JY" priority="3">
        <lane id="EY_0" index="0" speed="13.89" length="100.00" width="3.50" shape="110.00,5.25 210.00,5.25"/>
    </edge>
    <edge id="EZ" from="J2" to="JZ" priority="3">
        <lane id="EZ_0" index="0" speed="13.89" length="100.00" width="3.50" shape="110.00,10.25 210.00,10.25"/>
    </edge>

    <junction id="J0" type="dead_end" x="0.00" y="5.00" incLanes="" intLanes="" shape=""/>
    <junction id="J1" type="priority" x="101.50" y="5.00" incLanes="E0_0 E0_1" intLanes=":J1_0_0 :J1_2_0" shape="100.00,1.50 103.00,1.50 103.00,8.50 100.00,8.50"/>
    <junction id="J2" type="priority" x="108.00" y="5.00" incLanes="S1_0 S3_0" intLanes=":J2_0_0 :J2_1_0 :J2_2_0" shape="105.00,1.50 110.00,1.50 110.00,11.50 105.00,11.50"/>
    <junction id="JX" type="dead_end" x="210.00" y="0.25" incLanes="EX_0" intLanes="" shape=""/>
    <junction id="JY" type="dead_end" x="210.00" y="5.25" incLanes="EY_0" intLanes="" shape=""/>
    <junction id="JZ" type="dead_end" x="210.00" y="10.25" incLanes="EZ_0" intLanes="" shape=""/>

    <edge id=":J1_0" function="internal">
        <lane id=":J1_0_0" index="0" speed="13.89" length="3.00" width="3.50" shape="100.00,3.25 103.00,3.25"/>
    </edge>
    <edge id=":J1_2" function="internal">
        <lane id=":J1_2_0" index="0" speed="13.89" length="3.00" width="3.50" shape="100.00,6.75 103.00,6.75"/>
    </edge>
    <edge id=":J2_0" function="internal">
        <lane id=":J2_0_0" index="0" speed="13.89" length="5.00" width="3.50" shape="105.00,3.25 110.00,0.25"/>
    </edge>
    <edge id=":J2_1" function="internal">
        <lane id=":J2_1_0" index="0" speed="13.89" length="5.00" width="3.50" shape="105.00,5.25 110.00,5.25"/>
    </edge>
    <edge id=":J2_2" function="internal">
        <lane id=":J2_2_0" index="0" speed="13.89" length="5.00" width="3.50" shape="106.00,6.75 110.00,10.25"/>
    </edge>

    <connection from="E0" to="S1" fromLane="0" toLane="0" via=":J1_0_0" dir="s" state="M"/>
    <connection from="E0" to="S3" fromLane="1" toLane="0" via=":J1_2_0" dir="l" state="M"/>
    <connection from="S1" to="EX" fromLane="0" toLane="0" via=":J2_0_0" dir="l" state="M"/>
    <connection from="S1" to="EY" fromLane="0" toLane="0" via=":J2_1_0" dir="s" state="M"/>
    <connection from="S3" to="EZ" fromLane="0" toLane="0" via=":J2_2_0" dir="l" state="M"/>
    <connection from="S3" to="EY" fromLane="0" toLane="0" via=":J2_1_0" dir="s" state="M"/>
</net>
`

// Fan-out consolidation: both slivers are deleted, the J1 internals inherit
// the slivers' successor fan-outs (a multi-successor internal results), and
// merge foes are computed by successor-set intersection — iJ1_0_0 and
// iJ1_2_0 share iJ2_1_0 at index 1, which first-successor equality misses.
// The shared :J2_1_0 via takes both slivers' lengths and the audit counter
// says so. The result must also pass the engine loader.
func TestConsolidateFanoutMultiSuccessor(t *testing.T) {
	nf, rep := convertXML(t, fanoutFixture)

	if len(rep.ConsolidatedSlivers) != 2 {
		t.Fatalf("ConsolidatedSlivers = %v, want 2 (S1, S3)", rep.ConsolidatedSlivers)
	}
	a := laneByID(nf, "iJ1_0_0")
	if got := a.Successors; len(got) != 2 || got[0] != "iJ2_0_0" || got[1] != "iJ2_1_0" {
		t.Errorf("iJ1_0_0 successors = %v, want [iJ2_0_0 iJ2_1_0] (leftmost first)", got)
	}
	b := laneByID(nf, "iJ1_2_0")
	if got := b.Successors; len(got) != 2 || got[0] != "iJ2_2_0" || got[1] != "iJ2_1_0" {
		t.Errorf("iJ1_2_0 successors = %v, want [iJ2_2_0 iJ2_1_0]", got)
	}
	// Merge-foe intersection: shared iJ2_1_0 sits at index 1 for both.
	if !containsStr(a.FoesMerge, "iJ1_2_0") || !containsStr(b.FoesMerge, "iJ1_0_0") {
		t.Errorf("merge foes = %v / %v, want iJ1_0_0 <-> iJ1_2_0 via shared iJ2_1_0", a.FoesMerge, b.FoesMerge)
	}
	// J2 internals share no exit: no merge foes among them.
	for _, id := range []string{"iJ2_0_0", "iJ2_1_0", "iJ2_2_0"} {
		if got := laneByID(nf, id).FoesMerge; len(got) != 0 {
			t.Errorf("%s FoesMerge = %v, want none (distinct exits)", id, got)
		}
	}
	// The shared via takes only the LONGER sliver's extension (3 m from S3,
	// replacing S1's 2 m — the parallel-extension guard), and the audit
	// counter records the choice.
	if got := laneByID(nf, "iJ2_1_0").Length; got != 8.0 {
		t.Errorf("iJ2_1_0 length = %v, want 8.0 (5 + longer sliver 3)", got)
	}
	if got := laneByID(nf, "iJ2_1_0").Shape; len(got) == 0 || got[0] != [2]float64{103, 6.75} {
		t.Errorf("iJ2_1_0 shape starts %v, want S3's polyline prepended", got)
	}
	if !containsStr(rep.SharedExtensions, "iJ2_1_0") {
		t.Errorf("SharedExtensions = %v, want iJ2_1_0 listed", rep.SharedExtensions)
	}
	// Loader acceptance: a multi-successor internal compiles.
	if _, err := engine.CompileNet(nf); err != nil {
		t.Errorf("CompileNet rejected the consolidated file: %v", err)
	}
}

func containsStr(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
