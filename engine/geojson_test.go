package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// geojson_test.go — the viz-facing network export: structure, properties,
// frame descriptor, and (when the reference import is present) the real
// i280 network.

func TestWriteGeoJSON(t *testing.T) {
	nf := &NetFile{
		Version: 1,
		Name:    "toy",
		Provenance: &NetProvenance{
			Projection: "+proj=utm +zone=10 +ellps=WGS84 +datum=WGS84 +units=m +no_defs",
			NetOffset:  [2]float64{-562744.68, -4141511.42},
			OSMBbox:    "37.42,-122.30,37.45,-122.25",
		},
		Lanes: []NetLane{
			{
				ID: "a_0", Length: 10, SpeedLimit: 29.06, Width: 3.2,
				Edge: "a", EdgeIndex: 0,
				Shape: [][2]float64{{0, 0}, {10, 0}},
			},
			{
				ID: "j:i_0", Length: 5, SpeedLimit: 11.18, Internal: true, // width 0 → default 3.5
				Shape: [][2]float64{{10, 0}, {15, 3}},
			},
		},
	}
	var buf bytes.Buffer
	if err := WriteGeoJSON(nf, &buf); err != nil {
		t.Fatalf("WriteGeoJSON: %v", err)
	}
	var got struct {
		Type  string `json:"type"`
		Frame struct {
			Projection string     `json:"projection"`
			NetOffset  [2]float64 `json:"netOffset"`
			OSMBbox    string     `json:"osmBbox"`
		} `json:"frame"`
		Features []struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Properties struct {
				ID         string  `json:"id"`
				SpeedLimit float64 `json:"speedLimit"`
				Width      float64 `json:"width"`
				Internal   bool    `json:"internal"`
				Edge       string  `json:"edge"`
				EdgeIndex  int     `json:"edgeIndex"`
			} `json:"properties"`
			Geometry struct {
				Type        string       `json:"type"`
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if got.Type != "FeatureCollection" {
		t.Fatalf("type = %q, want FeatureCollection", got.Type)
	}
	if got.Frame.Projection != nf.Provenance.Projection || got.Frame.NetOffset != nf.Provenance.NetOffset {
		t.Fatalf("frame descriptor mismatch: %+v", got.Frame)
	}
	if len(got.Features) != 2 {
		t.Fatalf("features = %d, want 2", len(got.Features))
	}
	f0, f1 := got.Features[0], got.Features[1]
	if f0.Type != "Feature" || f0.ID != "a_0" || f0.Properties.ID != "a_0" {
		t.Fatalf("feature 0 identity: %+v", f0)
	}
	if f0.Geometry.Type != "LineString" || len(f0.Geometry.Coordinates) != 2 {
		t.Fatalf("feature 0 geometry: %+v", f0.Geometry)
	}
	if f0.Properties.SpeedLimit != 29.06 || f0.Properties.Width != 3.2 || f0.Properties.Internal {
		t.Fatalf("feature 0 properties: %+v", f0.Properties)
	}
	if f0.Properties.Edge != "a" || f0.Properties.EdgeIndex != 0 {
		t.Fatalf("feature 0 edge group: %+v", f0.Properties)
	}
	if f1.Properties.Width != 3.5 || !f1.Properties.Internal {
		t.Fatalf("feature 1 width default/internal: %+v", f1.Properties)
	}
	if f1.Properties.Edge != "" {
		t.Fatalf("feature 1 (internal) must carry no edge group: %+v", f1.Properties)
	}
}

// TestWriteGeoJSONReferenceNet exports the real i280 network when the
// (git-ignored) data file is present; skipped otherwise.
func TestWriteGeoJSONReferenceNet(t *testing.T) {
	const path = "../data/networks/i280-woodside/i280.json"
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("reference network not present: %v", err)
	}
	var nf NetFile
	if err := json.Unmarshal(data, &nf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var buf bytes.Buffer
	if err := WriteGeoJSON(&nf, &buf); err != nil {
		t.Fatalf("WriteGeoJSON: %v", err)
	}
	var got struct {
		Frame    *GeoJSONFrame `json:"frame"`
		Features []struct {
			ID         string `json:"id"`
			Properties struct {
				ID         string  `json:"id"`
				SpeedLimit float64 `json:"speedLimit"`
			} `json:"properties"`
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal export: %v", err)
	}
	if len(got.Features) != len(nf.Lanes) {
		t.Fatalf("features = %d, want %d (one per lane)", len(got.Features), len(nf.Lanes))
	}
	if got.Frame == nil || got.Frame.Projection == "" {
		t.Fatal("reference export missing frame descriptor")
	}
	seen := map[string]bool{}
	for i, f := range got.Features {
		if f.ID == "" || f.Properties.ID != f.ID {
			t.Fatalf("feature %d id mismatch: %q vs %q", i, f.ID, f.Properties.ID)
		}
		if seen[f.ID] {
			t.Fatalf("duplicate feature id %q", f.ID)
		}
		seen[f.ID] = true
		if len(f.Geometry.Coordinates) < 2 {
			t.Fatalf("feature %q has %d points", f.ID, len(f.Geometry.Coordinates))
		}
		if f.Properties.SpeedLimit <= 0 {
			t.Fatalf("feature %q speedLimit %v", f.ID, f.Properties.SpeedLimit)
		}
	}
}
