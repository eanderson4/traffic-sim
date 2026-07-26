package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// wgs84_test.go — the tippecanoe feed (ADR-0023 §7): WGS84 coordinates,
// per-feature minzoom from the speed thresholds, edgeB, id property.

func testNet() *engine.NetFile {
	return &engine.NetFile{
		Version: 1,
		Provenance: &engine.NetProvenance{
			Projection: "+proj=utm +zone=10",
			NetOffset:  [2]float64{-547000, -4185000},
		},
		Lanes: []engine.NetLane{
			{ID: "freeway_0", Edge: "freeway", EdgeIndex: 0, SpeedLimit: 29, Width: 3.7,
				Shape: [][2]float64{{100, 200}, {150, 250}}},
			{ID: "freeway_1", Edge: "freeway", EdgeIndex: 1, SpeedLimit: 29, Width: 3.7,
				Shape: [][2]float64{{100, 204}, {150, 254}}},
			{ID: "arterial_0", Edge: "arterial", EdgeIndex: 0, SpeedLimit: 15,
				Shape: [][2]float64{{300, 300}, {400, 300}}},
			{ID: "local_0", SpeedLimit: 8,
				Shape: [][2]float64{{500, 500}, {600, 500}}},
			{ID: "j1_0", Internal: true, Junction: "j1", SpeedLimit: 29,
				Shape: [][2]float64{{700, 700}, {710, 710}}},
		},
	}
}

func exportToLines(t *testing.T, nf *engine.NetFile) []map[string]any {
	t.Helper()
	proj, err := engine.MakeProjector(engine.LocalFrame{
		Projection: nf.Provenance.Projection,
		NetOffset:  nf.Provenance.NetOffset,
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := exportNetworkWGS84(nf, proj, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(nf.Lanes) {
		t.Fatalf("%d NDJSON lines, want %d", len(lines), len(nf.Lanes))
	}
	out := make([]map[string]any, len(lines))
	for i, l := range lines {
		if err := json.Unmarshal([]byte(l), &out[i]); err != nil {
			t.Fatalf("line %d not standalone GeoJSON: %v", i, err)
		}
	}
	return out
}

func TestWGS84ExportMinzooms(t *testing.T) {
	feats := exportToLines(t, testNet())
	want := map[string]float64{
		"freeway_0": 8, "freeway_1": 8, "arterial_0": 11, "local_0": 13, "j1_0": 13,
	}
	for _, f := range feats {
		id := f["id"].(string)
		tc, ok := f["tippecanoe"].(map[string]any)
		if !ok {
			t.Fatalf("feature %s has no tippecanoe member", id)
		}
		if tc["minzoom"].(float64) != want[id] {
			t.Fatalf("feature %s minzoom %v, want %v", id, tc["minzoom"], want[id])
		}
	}
}

func TestWGS84ExportEdgeB(t *testing.T) {
	feats := exportToLines(t, testNet())
	// edges.ts: the min- and max-index lane of each group carry the casing;
	// ungrouped lanes are always boundaries. "freeway" has two lanes → both
	// boundaries; "arterial" has one → boundary; local_0 ungrouped →
	// boundary; j1_0 ungrouped (internal, no edge) → boundary.
	want := map[string]bool{
		"freeway_0": true, "freeway_1": true, "arterial_0": true, "local_0": true, "j1_0": true,
	}
	for _, f := range feats {
		props := f["properties"].(map[string]any)
		id := props["id"].(string)
		if props["edgeB"].(bool) != want[id] {
			t.Fatalf("feature %s edgeB %v, want %v", id, props["edgeB"], want[id])
		}
	}

	// A three-lane group: the middle lane is NOT a boundary.
	nf := testNet()
	nf.Lanes = append(nf.Lanes, engine.NetLane{
		ID: "freeway_2", Edge: "freeway", EdgeIndex: 2, SpeedLimit: 29,
		Shape: [][2]float64{{100, 208}, {150, 258}},
	})
	// Insert a middle lane properly: rebuild with indices 0,1,2 where 1 is
	// interior... the group is min/max over edgeIndex, so with three lanes
	// 0 and 2 are boundaries, 1 is not.
	feats = exportToLines(t, nf)
	for _, f := range feats {
		props := f["properties"].(map[string]any)
		if props["id"] == "freeway_1" && props["edgeB"].(bool) {
			t.Fatal("middle lane of a 3-lane group stamped edgeB=true")
		}
		if props["id"] == "freeway_2" && !props["edgeB"].(bool) {
			t.Fatal("outer lane of a 3-lane group not stamped edgeB")
		}
	}
}

func TestWGS84ExportCoordinates(t *testing.T) {
	feats := exportToLines(t, testNet())
	f := feats[0]
	geom := f["geometry"].(map[string]any)
	coords := geom["coordinates"].([]any)
	first := coords[0].([]any)
	lng, lat := first[0].(float64), first[1].(float64)
	// Zone 10 covers lng −126..−120; a Bay-Area-ish network sits well
	// inside it, and NOT in the metric range (hundreds of thousands).
	if lng < -126 || lng > -120 || lat < 30 || lat > 45 {
		t.Fatalf("coordinate (%v, %v) is not plausible WGS84 zone-10", lng, lat)
	}
	// The id property rides as both the feature id and the property
	// (promoteId: "id").
	if f["id"] != "freeway_0" || f["properties"].(map[string]any)["id"] != "freeway_0" {
		t.Fatalf("id not carried: feature id %v", f["id"])
	}
}

func TestFindTippecanoeMessage(t *testing.T) {
	bin, err := findTippecanoe()
	if err == nil {
		t.Skipf("tippecanoe is installed at %s — the missing-binary path does not apply", bin)
	}
	if !strings.Contains(err.Error(), "install tippecanoe") {
		t.Fatalf("error %q does not carry the install instruction", err)
	}
}
