package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoEdgeNetFile is a minimal valid v1 network: two-lane edge E0 feeding
// two-lane edge E1 (exit), mirroring the netimport output shape.
func twoEdgeNetFile() *NetFile {
	mk := func(id, edge string, idx int, y float64, succ []string, origin, exit bool) NetLane {
		return NetLane{
			ID: id, Section: edge, Edge: edge, EdgeIndex: idx,
			Length: 100, SpeedLimit: 13.9, Width: 3.5,
			Shape:      [][2]float64{{0, y}, {100, y}},
			Successors: succ, Origin: origin, Exit: exit,
			Source: &LaneSource{Edge: edge, Lane: idx},
		}
	}
	return &NetFile{
		Version: 1, Name: "two-edge",
		Lanes: []NetLane{
			mk("nE0_0", "E0", 0, 3.25, []string{"nE1_0"}, true, false),
			mk("nE0_1", "E0", 1, 6.75, []string{"nE1_1"}, true, false),
			mk("nE1_0", "E1", 0, 3.25, nil, false, true),
			mk("nE1_1", "E1", 1, 6.75, nil, false, true),
		},
	}
}

// The Kind "file" loader: lanes, links, lateral chaining, origins, geometry.
func TestCompileNetFile(t *testing.T) {
	n, err := CompileNet(twoEdgeNetFile())
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Lanes) != 4 {
		t.Fatalf("%d lanes, want 4", len(n.Lanes))
	}
	for i, id := range []string{"nE0_0", "nE0_1", "nE1_0", "nE1_1"} {
		if n.Lanes[i].ID != id || n.Lanes[i].Index != i {
			t.Fatalf("lane %d = %s (index %d), want %s in file order", i, n.Lanes[i].ID, n.Lanes[i].Index, id)
		}
	}
	a, b := n.LaneByID("nE0_0"), n.LaneByID("nE0_1")
	if a.Left != b || b.Right != a {
		t.Error("E0 lateral chaining wrong (index 0 = rightmost, Left = index+1)")
	}
	if n.LaneByID("nE1_0").Left != n.LaneByID("nE1_1") {
		t.Error("E1 lateral chaining wrong")
	}
	if a.Left.Right != a || b.Right.Left != b {
		t.Error("lateral links not symmetric")
	}
	if len(a.Successors) != 1 || a.Successors[0] != n.LaneByID("nE1_0") {
		t.Error("nE0_0 successor not resolved")
	}
	if len(n.LaneByID("nE1_0").Prevs) != 1 || n.LaneByID("nE1_0").Prevs[0] != a {
		t.Error("Prevs not derived")
	}
	if len(n.Origins) != 2 || n.Origins[0] != a || n.Origins[1] != b {
		t.Error("origins wrong (want [nE0_0 nE0_1] in file order)")
	}
	if !n.LaneByID("nE1_0").Exit {
		t.Error("exit flag lost")
	}
	// Geometry survives the load: mid-lane projection on nE0_1.
	x, y, angle, ok := b.Project(50)
	if !ok || x != 50 || y != 6.75 || angle != 0 {
		t.Errorf("Project(50) = (%v,%v,%v) ok=%v, want (50,6.75,0)", x, y, angle, ok)
	}
	// Default width when omitted.
	nf := twoEdgeNetFile()
	nf.Lanes[0].Width = 0
	n2, err := CompileNet(nf)
	if err != nil {
		t.Fatal(err)
	}
	if n2.LaneByID("nE0_0").Width != 3.5 {
		t.Errorf("default width = %v, want 3.5", n2.LaneByID("nE0_0").Width)
	}
}

// Validation must fail loudly on malformed files.
func TestCompileNetFileRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*NetFile)
		want   string
	}{
		{"version", func(nf *NetFile) { nf.Version = 2 }, "version"},
		{"duplicate id", func(nf *NetFile) { nf.Lanes[1].ID = "nE0_0" }, "duplicate"},
		{"unknown successor", func(nf *NetFile) { nf.Lanes[0].Successors = []string{"nope"} }, "unknown successor"},
		{"successor on exit", func(nf *NetFile) { nf.Lanes[2].Successors = []string{"nE1_1"} }, "exit"},
		{"dangling lane", func(nf *NetFile) { nf.Lanes[2].Exit = false }, "dangling"},
		{"zero length", func(nf *NetFile) { nf.Lanes[0].Length = 0 }, "length"},
		{"zero speed", func(nf *NetFile) { nf.Lanes[0].SpeedLimit = 0 }, "speedLimit"},
		{"negative width", func(nf *NetFile) { nf.Lanes[0].Width = -1 }, "width"},
		{"one-point shape", func(nf *NetFile) { nf.Lanes[0].Shape = [][2]float64{{0, 0}} }, "shape"},
		{"duplicate edge index", func(nf *NetFile) { nf.Lanes[1].EdgeIndex = 0 }, "duplicate lane index"},
		{"unequal lateral lengths", func(nf *NetFile) { nf.Lanes[1].Length = 90 }, "differ in length"},
		{"self successor", func(nf *NetFile) { nf.Lanes[0].Successors = []string{"nE0_0"} }, "self-successor"},
		{"exit and endWall", func(nf *NetFile) { nf.Lanes[2].EndWall = true }, "both exit and endWall"},
		{"no lanes", func(nf *NetFile) { nf.Lanes = nil }, "no lanes"},
	}
	for _, c := range cases {
		nf := twoEdgeNetFile()
		c.mutate(nf)
		if _, err := CompileNet(nf); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention %q", c.name, err, c.want)
		}
	}
}

// Kind "file" through BuildNet + a spawner-driven run on the loaded network:
// vehicles enter at the origins and leave at the exits.
func TestFileNetEndToEnd(t *testing.T) {
	data, err := json.Marshal(twoEdgeNetFile())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: path},
		Scen:   Scenario{SpawnRatePerLaneHour: 900, DensityTargetPerKm: 100},
		Params: DefaultParams(),
		Seed:   3,
		Ticks:  600,
	}
	e, _, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertNoNaN(t, e)
	if e.Stats.Spawned == 0 {
		t.Error("nothing spawned on the file network's origins")
	}
	if e.Stats.Despawned == 0 {
		t.Error("nothing reached the exits after 60 s")
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collisions on a free corridor", e.Stats.Collisions)
	}
	if got := e.Net.LaneByID("nE0_0"); got == nil || got.Length != 100 {
		t.Error("network not loaded from file")
	}
	t.Logf("file net: spawned %d, despawned %d, live %d, min gap %.2f",
		e.Stats.Spawned, e.Stats.Despawned, len(e.Vehicles()), e.Stats.MinGap)
}

// LoadNetFile errors surface the path and parse failures.
func TestLoadNetFileErrors(t *testing.T) {
	if _, err := LoadNetFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("missing file accepted")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(bad, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNetFile(bad); err == nil {
		t.Error("malformed JSON accepted")
	}
}
