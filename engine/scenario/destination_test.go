package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// destination_test.go — ADR-0021 scenario grammar: weighted flow
// destinations and the interior-origin offset opt-in. The strictness is the
// point: a mistyped lane id must fail at LOAD, never at spawn time.

const odDemand = `format_version: 1
flows:
  - id: portal-inbound
    origin: a_0
    veh_per_h: 600
    spacing: poisson
    destinations:
      b_0: 0.75
      a_0: 0.25
  - id: garage
    origin: b_0
    offset_m: 120
    veh_per_h: 30
    spacing: constant
    destinations:
      b_0: 1
`

// TestODDemandLoads: the full ADR-0021 grammar round-trips through load,
// including an interior origin on a lane that is NOT a network portal.
func TestODDemandLoads(t *testing.T) {
	dir := t.TempDir()
	writeNet(t, dir)
	writeFile(t, dir, ManifestFile, goodManifest)
	writeFile(t, dir, "demand/main.yaml", odDemand)
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(s.Demands) != 1 || len(s.Demands[0].Flows) != 2 {
		t.Fatalf("loaded %d demand files", len(s.Demands))
	}
	portal, garage := s.Demands[0].Flows[0], s.Demands[0].Flows[1]
	if got := portal.Destinations["b_0"]; got != 0.75 {
		t.Errorf("portal destination weight b_0 = %v, want 0.75", got)
	}
	if garage.OffsetM != 120 {
		t.Errorf("garage offset_m = %v, want 120", garage.OffsetM)
	}
	if garage.Origin != "b_0" {
		t.Errorf("garage origin = %q, want b_0 (a non-portal lane, admitted by the offset)", garage.Origin)
	}
}

// TestODDemandValidation pins the reject reasons.
func TestODDemandValidation(t *testing.T) {
	cases := []struct {
		name   string
		demand string
		want   string
	}{
		{"unknown destination lane",
			strings.Replace(odDemand, "b_0: 0.75", "nope_0: 0.75", 1),
			"is not a lane of the network"},
		{"zero destination weight",
			strings.Replace(odDemand, "b_0: 0.75", "b_0: 0", 1),
			"weight must be > 0"},
		{"negative destination weight",
			strings.Replace(odDemand, "b_0: 0.75", "b_0: -1", 1),
			"weight must be > 0"},
		{"offset past the lane end",
			strings.Replace(odDemand, "offset_m: 120", "offset_m: 9000", 1),
			"past the end of the"},
		{"negative offset",
			strings.Replace(odDemand, "offset_m: 120", "offset_m: -5", 1),
			"offset_m must be ≥ 0"},
		{"interior origin without an offset",
			strings.Replace(odDemand, "    offset_m: 120\n", "", 1),
			"interior injection needs an explicit offset_m"},
		{"interior origin on an unknown lane",
			strings.Replace(odDemand, "origin: b_0\n    offset_m: 120", "origin: ghost_0\n    offset_m: 120", 1),
			"not a lane of the network"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNet(t, dir)
			writeFile(t, dir, ManifestFile, goodManifest)
			writeFile(t, dir, "demand/main.yaml", tc.demand)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

// TestODDemandHashMoves: adding a destination distribution is CONTENT, so
// it must move the scenario's content hash — otherwise two materially
// different demand programs would share a run identity (ADR-0012 §6).
func TestODDemandHashMoves(t *testing.T) {
	hashOf := func(demand string) string {
		t.Helper()
		dir := t.TempDir()
		writeNet(t, dir)
		writeFile(t, dir, ManifestFile, goodManifest)
		writeFile(t, dir, "demand/main.yaml", demand)
		s, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return s.Hash()
	}
	plain := `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 600
    spacing: poisson
`
	routed := `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 600
    spacing: poisson
    destinations:
      b_0: 1
`
	if hashOf(plain) == hashOf(routed) {
		t.Fatal("destinations did not move the content hash — two different demand programs share a run identity")
	}
}

// TestODDemandRejectsEndWallDestination: a destination lane that ends in a
// wall is rejected at LOAD. Arrival is detected as S > Lane.Length, but an
// EndWall lane brakes its traffic to a stop before that line, so a vehicle
// routed there parks at the wall and never despawns — it would sit in the
// world for the rest of the run inflating every occupancy metric. Verified
// on lanedrop before the guard existed: alive=1, arrived=0, despawned=0,
// S=598.09/600, V=0 after 4,000 ticks.
//
// This fixture carries its own network because the shared writeNet has no
// EndWall lane and other tests count on its exact lane set.
func TestODDemandRejectsEndWallDestination(t *testing.T) {
	dir := t.TempDir()
	nf := engine.NetFile{
		Version: 1,
		Name:    "test",
		Lanes: []engine.NetLane{
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"b_0", "c_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {1000, 0}}, Exit: true},
			{ID: "c_0", Section: "c", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{500, 10}, {1000, 10}}, EndWall: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "network.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ManifestFile, goodManifest)
	writeFile(t, dir, "demand/main.yaml", `format_version: 1
flows:
  - id: to-the-wall
    origin: a_0
    veh_per_h: 600
    spacing: poisson
    destinations:
      c_0: 1
`)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "ends in a wall") {
		t.Fatalf("Load error = %v, want one naming the wall", err)
	}
}
