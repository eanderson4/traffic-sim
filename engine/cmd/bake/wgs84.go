package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"traffic-sim/engine"
)

// wgs84.go — the PMTiles feed (ADR-0023 §1, §7): the network exported as
// WGS84 GeoJSON for the EXTERNAL tippecanoe binary, plus the invocation
// wrapper. The export is newline-delimited GeoJSON (one feature per line —
// tippecanoe reads it natively and it streams at city scale, where a
// single document hits the same V8-era size walls ADR-0018 chunked). Each
// feature carries the standard lane property block (engine/geojson.go)
// PLUS the client-derived properties pre-computed (edgeB, §7) and a
// per-feature "tippecanoe": {"minzoom": N} member (tippecanoe's documented
// GeoJSON extension) stamped from the network's own attributes.

// minzoomPolicy records the road-class minzoom rule (ADR-0023 §7) as a
// string — an ingredient of both content keys.
const minzoomPolicy = "internal:13;speedLimit>=22:8;speedLimit>=12:11;else:13"

// pinnedTippecanoe is the tippecanoe version the PMTiles step is pinned
// to. The binary's --version output rides the per-city content key, so
// tiles produced by a different build never share a URL (ADR-0023 §8).
const pinnedTippecanoe = "2.78.0 (felt/tippecanoe)"

// tippecanoeArgs is the exact flag set (recorded here, in README.md, and
// in the per-city content key). Every behavior that destroys per-lane
// identity is disabled — lane ids are the congestion feature-state key; a
// dropped or merged lane is a lane that never colors (ADR-0023 §1):
//
//	-Z8 -z13            zoom range (per-feature minzooms sit inside it)
//	--no-feature-limit  never drop features at low zooms
//	--no-tile-size-limit  never drop features to fit a tile
//	--no-line-simplification  never coalesce/simplify lane geometry
//	--preserve-input-order  no reordering coalescing
//	--no-tile-stats     deterministic output (no stats block)
//	-l lanes            the single layer the manifest names
var tippecanoeArgs = []string{
	"-Z", "8", "-z", "13",
	"--no-feature-limit",
	"--no-tile-size-limit",
	"--no-line-simplification",
	"--preserve-input-order",
	"--no-tile-stats",
	"-l", "lanes",
	"--force",
}

// laneMinzoom stamps the road-class minzoom from the lane's own
// attributes (ADR-0023 §7): freeway-class (speedLimit ≥ 22 m/s) → z8;
// arterial (≥ 12 m/s) → z11; everything else → z13; junction internals →
// z13 (the viz's internal-layer gate).
func laneMinzoom(nl *engine.NetLane) int {
	if nl.Internal {
		return 13
	}
	if nl.SpeedLimit >= 22 {
		return 8
	}
	if nl.SpeedLimit >= 12 {
		return 11
	}
	return 13
}

// edgeBoundaryIDs ports the viz's edges.ts rule: the min- and max-index
// lane of every edge group carries the group's outer casing; ungrouped
// lanes (no edge) are always boundaries.
func edgeBoundaryIDs(nf *engine.NetFile) map[string]bool {
	type group struct {
		min, max     int
		minID, maxID string
	}
	groups := map[string]*group{}
	out := map[string]bool{}
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		if nl.Edge == "" {
			out[nl.ID] = true
			continue
		}
		g, ok := groups[nl.Edge]
		if !ok {
			groups[nl.Edge] = &group{min: nl.EdgeIndex, max: nl.EdgeIndex, minID: nl.ID, maxID: nl.ID}
			continue
		}
		if nl.EdgeIndex < g.min {
			g.min, g.minID = nl.EdgeIndex, nl.ID
		}
		if nl.EdgeIndex > g.max {
			g.max, g.maxID = nl.EdgeIndex, nl.ID
		}
	}
	for _, g := range groups {
		out[g.minID] = true
		out[g.maxID] = true
	}
	return out
}

// wgs84Feature is one exported lane feature.
type wgs84Feature struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Tippecanoe struct {
		Minzoom int `json:"minzoom"`
	} `json:"tippecanoe"`
	Properties wgs84Properties `json:"properties"`
	Geometry   struct {
		Type        string       `json:"type"`
		Coordinates [][2]float64 `json:"coordinates"`
	} `json:"geometry"`
}

// wgs84Properties is the GeoJSON lane property block (engine/geojson.go)
// PLUS the baked edgeB (the casing paint's boolean-get defaults to true —
// an unstamped network renders full-opacity casing everywhere, ADR-0023
// §7 review S3).
type wgs84Properties struct {
	ID         string  `json:"id"`
	SpeedLimit float64 `json:"speedLimit"`
	Width      float64 `json:"width"`
	Internal   bool    `json:"internal"`
	Edge       string  `json:"edge,omitempty"`
	EdgeIndex  int     `json:"edgeIndex"`
	Junction   string  `json:"junction,omitempty"`
	Row        string  `json:"row,omitempty"`
	EdgeB      bool    `json:"edgeB"`
}

// exportNetworkWGS84 writes the network as newline-delimited WGS84 GeoJSON
// (one Feature per line) for tippecanoe. proj projects local-frame
// coordinates to (lng, lat).
func exportNetworkWGS84(nf *engine.NetFile, proj engine.Projector, w io.Writer) error {
	if nf.Version != 1 {
		return fmt.Errorf("wgs84 export: unsupported network version %d (want 1)", nf.Version)
	}
	edgeB := edgeBoundaryIDs(nf)
	var line []byte
	for i := range nf.Lanes {
		nl := &nf.Lanes[i]
		width := nl.Width
		if width == 0 {
			width = 3.5 // the loader's default; keep the file and engine in agreement
		}
		var f wgs84Feature
		f.Type = "Feature"
		f.ID = nl.ID
		f.Tippecanoe.Minzoom = laneMinzoom(nl)
		f.Properties = wgs84Properties{
			ID: nl.ID, SpeedLimit: nl.SpeedLimit, Width: width,
			Internal: nl.Internal, Edge: nl.Edge, EdgeIndex: nl.EdgeIndex,
			Junction: nl.Junction, Row: nl.Row,
			EdgeB: edgeB[nl.ID],
		}
		f.Geometry.Type = "LineString"
		f.Geometry.Coordinates = make([][2]float64, len(nl.Shape))
		for j, pt := range nl.Shape {
			lng, lat := proj(pt[0], pt[1])
			f.Geometry.Coordinates[j] = [2]float64{lng, lat}
		}
		var err error
		line, err = json.Marshal(f)
		if err != nil {
			return fmt.Errorf("wgs84 export lane %s: %w", nl.ID, err)
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return err
		}
	}
	return nil
}

// findTippecanoe locates the pinned external binary. The error is the
// user-facing install instruction, not a bare "not found".
func findTippecanoe() (string, error) {
	bin, err := exec.LookPath("tippecanoe")
	if err != nil {
		return "", fmt.Errorf("tippecanoe not found in PATH — install tippecanoe (pinned: %s; see engine/cmd/bake/README.md) to bake network.pmtiles, or bake small networks with -net-format geojson", pinnedTippecanoe)
	}
	return bin, nil
}

// tippecanoeVersion returns the binary's version banner (an ingredient of
// the per-city content key).
func tippecanoeVersion(bin string) (string, error) {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("tippecanoe --version: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runTippecanoe runs the binary over the WGS84 NDJSON export into
// outPath, capturing stderr for the error report.
func runTippecanoe(bin, inPath, outPath string) error {
	args := append(append([]string{}, tippecanoeArgs...), "-o", outPath, inPath)
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tippecanoe: %w\n%s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
