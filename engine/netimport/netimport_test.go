package netimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"traffic-sim/engine"
)

func convertFixture(t *testing.T) (*engine.NetFile, *Report) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "corridor.net.xml"))
	if err != nil {
		t.Fatal(err)
	}
	nf, rep, err := Convert(data, Options{Name: "corridor", SourceFile: "corridor.net.xml", Source: "netimport (test)"})
	if err != nil {
		t.Fatal(err)
	}
	return nf, rep
}

func laneByID(nf *engine.NetFile, id string) *engine.NetLane {
	for i := range nf.Lanes {
		if nf.Lanes[i].ID == id {
			return &nf.Lanes[i]
		}
	}
	return nil
}

// The fixture converts: durable IDs assigned, lanes/connections/geometry
// parsed, provenance and guessed flags carried, non-motor lanes skipped,
// signalized junctions reported-unmodeled.
func TestConvertFixture(t *testing.T) {
	nf, rep := convertFixture(t)

	if len(nf.Lanes) != 8 {
		t.Fatalf("%d lanes, want 8 (5 normal + 3 internal; rail edge and sidewalk lane skipped)", len(nf.Lanes))
	}
	// Durable IDs: n<edge>_<lane> normal, i<edge>_<lane> internal.
	wantIDs := []string{"nE0_0", "nE0_1", "nE1_0", "nE1_1", "nE2_0", "iJ1_0_0", "iJ1_1_0", "iJ1_2_0"}
	for i, id := range wantIDs {
		if nf.Lanes[i].ID != id {
			t.Errorf("lane %d id = %s, want %s (file order: normal edges, then internal)", i, nf.Lanes[i].ID, id)
		}
	}

	e00 := laneByID(nf, "nE0_0")
	if e00.Length != 197 || e00.SpeedLimit != 13.89 || e00.Width != 3.5 {
		t.Errorf("nE0_0 attrs = len %v, speed %v, width %v", e00.Length, e00.SpeedLimit, e00.Width)
	}
	if len(e00.Shape) != 2 || e00.Shape[0] != [2]float64{0, 3.25} || e00.Shape[1] != [2]float64{197, 3.25} {
		t.Errorf("nE0_0 shape = %v", e00.Shape)
	}
	if e00.Edge != "E0" || e00.EdgeIndex != 0 || e00.Section != "E0" {
		t.Errorf("nE0_0 edge/index/section = %q/%d/%q", e00.Edge, e00.EdgeIndex, e00.Section)
	}
	if !e00.Origin || e00.Exit || e00.Internal {
		t.Errorf("nE0_0 flags = origin %v exit %v internal %v", e00.Origin, e00.Exit, e00.Internal)
	}
	if e00.Source == nil || e00.Source.Edge != "E0" || e00.Source.Lane != 0 || len(e00.Source.Guessed) != 0 {
		t.Errorf("nE0_0 provenance = %+v", e00.Source)
	}

	// Successor wiring through the internal lanes, ordered left-to-right:
	// nE0_1's left turn (dir=l) must precede its straight (dir=s).
	if got := laneByID(nf, "nE0_0").Successors; len(got) != 1 || got[0] != "iJ1_0_0" {
		t.Errorf("nE0_0 successors = %v, want [iJ1_0_0]", got)
	}
	if got := laneByID(nf, "nE0_1").Successors; len(got) != 2 || got[0] != "iJ1_2_0" || got[1] != "iJ1_1_0" {
		t.Errorf("nE0_1 successors = %v, want [iJ1_2_0 iJ1_1_0] (leftmost first)", got)
	}
	if got := laneByID(nf, "iJ1_2_0").Successors; len(got) != 1 || got[0] != "nE2_0" {
		t.Errorf("iJ1_2_0 successors = %v, want [nE2_0]", got)
	}

	// Internal lane marking: no lateral group, junction section, geometry flagged guessed.
	il := laneByID(nf, "iJ1_0_0")
	if !il.Internal || il.Edge != "" || il.Section != "j:J1" {
		t.Errorf("iJ1_0_0 internal/edge/section = %v/%q/%q", il.Internal, il.Edge, il.Section)
	}
	if il.Source == nil || len(il.Source.Guessed) != 1 || il.Source.Guessed[0] != "internal-geometry" {
		t.Errorf("iJ1_0_0 guessed = %+v", il.Source)
	}

	// Missing width → SUMO default, flagged guessed.
	e20 := laneByID(nf, "nE2_0")
	if e20.Width != defaultLaneWidth || len(e20.Source.Guessed) != 1 || e20.Source.Guessed[0] != "width" {
		t.Errorf("nE2_0 width/guessed = %v/%v", e20.Width, e20.Source.Guessed)
	}

	// Origins/exits fall out of the graph.
	for id, wantExit := range map[string]bool{"nE1_0": true, "nE1_1": true, "nE2_0": true} {
		if laneByID(nf, id).Exit != wantExit {
			t.Errorf("%s exit = %v", id, laneByID(nf, id).Exit)
		}
	}
	if rep.Origins != 2 || rep.Exits != 3 {
		t.Errorf("report origins/exits = %d/%d, want 2/3", rep.Origins, rep.Exits)
	}

	// Non-motor filtering and the unmodeled-signal report.
	if len(rep.SkippedEdges) != 1 || rep.SkippedEdges[0] != "R0" {
		t.Errorf("skipped edges = %v, want [R0]", rep.SkippedEdges)
	}
	foundE12 := false
	for _, s := range rep.SkippedLanes {
		if s == "E1_2" {
			foundE12 = true
		}
	}
	if !foundE12 {
		t.Errorf("E1_2 (sidewalk lane) not in skipped lanes %v", rep.SkippedLanes)
	}
	if len(rep.SignalizedJunctions) != 1 || rep.SignalizedJunctions[0] != "J2" {
		t.Errorf("signalized junctions = %v, want [J2]", rep.SignalizedJunctions)
	}
	if rep.Connections != 3 || rep.InternalLanes != 3 || rep.Lanes != 5 {
		t.Errorf("report counts = %d conn, %d internal, %d lanes", rep.Connections, rep.InternalLanes, rep.Lanes)
	}

	// Network-level provenance from the location element.
	if nf.Provenance == nil || nf.Provenance.Projection == "" || nf.Provenance.NetOffset != [2]float64{-100, -50} {
		t.Errorf("provenance = %+v", nf.Provenance)
	}
}

// The converted fixture compiles and runs: two runs with the same seed
// produce identical per-tick CRC sequences, and replay verifies (ADR-0005).
func TestConvertedNetworkDeterminism(t *testing.T) {
	nf, _ := convertFixture(t)
	data, err := json.MarshalIndent(nf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "corridor.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: path},
		Scen:   engine.Scenario{SpawnRatePerLaneHour: 1200, DensityTargetPerKm: 120},
		Params: engine.DefaultParams(),
		Seed:   7,
		Ticks:  1500,
	}
	e1, log1, err := engine.Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	e2, log2, err := engine.Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(log1.CRCs) != len(log2.CRCs) {
		t.Fatalf("CRC count %d vs %d", len(log1.CRCs), len(log2.CRCs))
	}
	for i := range log1.CRCs {
		if log1.CRCs[i] != log2.CRCs[i] {
			t.Fatalf("run divergence at tick %d: %016x vs %016x", i+1, log1.CRCs[i], log2.CRCs[i])
		}
	}
	if _, err := engine.Replay(log1); err != nil {
		t.Fatalf("replay: %v", err)
	}
	// Traffic sanity: vehicles crossed the junction interior and left the map.
	if e1.Stats.Spawned == 0 || e1.Stats.Despawned == 0 {
		t.Errorf("spawned %d despawned %d — the corridor did not flow", e1.Stats.Spawned, e1.Stats.Despawned)
	}
	if e1.Stats.Collisions != 0 {
		t.Errorf("%d collisions", e1.Stats.Collisions)
	}
	if e2.Stats.Spawned != e1.Stats.Spawned || e2.Stats.Despawned != e1.Stats.Despawned {
		t.Error("spawn/despawn counts differ between identical runs")
	}
	t.Logf("converted corridor: spawned %d, despawned %d, live %d, lane changes %d, min gap %.2f, crc %016x",
		e1.Stats.Spawned, e1.Stats.Despawned, len(e1.Vehicles()), e1.Stats.LaneChanges, e1.Stats.MinGap, e1.CRC())
}

// Malformed input and a lane without a usable shape fail loudly.
func TestConvertRejects(t *testing.T) {
	if _, _, err := Convert([]byte("<net><edge"), Options{}); err == nil {
		t.Error("truncated XML accepted")
	}
	noShape := `<net><edge id="E0" from="J0" to="J1"><lane id="E0_0" index="0" speed="13.89" length="100"/></edge></net>`
	if _, _, err := Convert([]byte(noShape), Options{}); err == nil {
		t.Error("lane without shape accepted")
	}
	empty := `<net></net>`
	if _, _, err := Convert([]byte(empty), Options{}); err == nil {
		t.Error("empty net accepted")
	}
}
