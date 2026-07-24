package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// routeForkSpec builds the route-following fixture: origin a_0 feeding a
// three-way fork — b_0 (left), c_0 (through), d_0 (right) — each ending in
// its own exit, plus an ISOLATED exit w_0 nothing can reach. The middle
// successor is the case the ±1 held-turn axis could never express.
func routeForkSpec(t *testing.T) RunSpec {
	t.Helper()
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "route-fork",
		Lanes: []NetLane{
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"b_0", "c_0", "d_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 100, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {520, 100}}, Successors: []string{"x_0"}},
			{ID: "c_0", Section: "c", Length: 100, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {600, 0}}, Successors: []string{"y_0"}},
			{ID: "d_0", Section: "d", Length: 100, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {520, -100}}, Successors: []string{"z_0"}},
			{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{520, 100}, {522, 100}}, Exit: true},
			{ID: "y_0", Section: "y", Length: 2, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{600, 0}, {602, 0}}, Exit: true},
			{ID: "z_0", Section: "z", Length: 2, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{520, -100}, {522, -100}}, Exit: true},
			{ID: "w_0", Section: "w", Length: 2, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{-50, -50}, {-48, -50}}, Exit: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return RunSpec{Net: NetSpec{Kind: "file", Path: p}, Params: DefaultParams(), Ticks: 5}
}

// The persistent Route axis resolves at EVERY multi-successor lane: the
// vehicle takes the successor on the shortest path to its destination,
// with an explicit held turn winning the crossing, and unknown /
// unreachable / arrived destinations degrading to the default first
// successor (chicago-metro review, 2026-07-24: the retired driver-side
// one-shot turn only ever steered the first fork).
func TestPickSuccessorFollowsRoute(t *testing.T) {
	cases := []struct {
		name     string
		route    string
		heldTurn int
		wantLane string
	}{
		{"route picks the middle successor", "y_0", 0, "c_0"},
		{"route picks the left successor", "x_0", 0, "b_0"},
		{"route picks the right successor", "z_0", 0, "d_0"},
		{"held right turn beats the route", "y_0", -1, "d_0"},
		{"held left turn beats the route", "z_0", 1, "b_0"},
		{"no route takes the default first", "", 0, "b_0"},
		{"unknown route takes the default first", "ZZ", 0, "b_0"},
		{"unreachable route takes the default first", "w_0", 0, "b_0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewEngine(routeForkSpec(t))
			if err != nil {
				t.Fatal(err)
			}
			v := e.AddInitialVehicle(e.Net.LaneByID("a_0"), 0, 499.5, 33.3, 1)
			v.Route = tc.route
			v.HeldTurn = tc.heldTurn
			for i := 0; i < 3 && v.Lane != nil && v.Lane.ID == "a_0"; i++ {
				e.Step()
			}
			if v.Lane == nil || v.Lane.ID != tc.wantLane {
				t.Fatalf("route %q held %d: crossed to %v, want %s",
					tc.route, tc.heldTurn, v.Lane, tc.wantLane)
			}
		})
	}
}

// Route following is a pure function of (network, vehicle state): two
// identical runs with routed vehicles produce identical per-tick CRCs.
func TestRouteFollowingDeterministic(t *testing.T) {
	run := func(t *testing.T) []uint64 {
		e, err := NewEngine(routeForkSpec(t))
		if err != nil {
			t.Fatal(err)
		}
		for i, dest := range []string{"x_0", "y_0", "z_0"} {
			veh := e.AddInitialVehicle(e.Net.LaneByID("a_0"), 0, 490-10*float64(i), 33.3, 1)
			veh.Route = dest
		}
		for e.Tick < 5 {
			e.Step()
		}
		return e.CRCs
	}
	a, b := run(t), run(t)
	if len(a) != len(b) {
		t.Fatalf("CRC sequences differ in length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("tick %d: CRC %x != %x — route following is not deterministic", i, a[i], b[i])
		}
	}
}
