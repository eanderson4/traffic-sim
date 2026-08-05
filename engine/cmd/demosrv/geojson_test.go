package main

// geojson_test.go — the net cache is keyed by CONTENT identity
// ({id}.{hash12}.geojson), so a scenario edit lands in a new cache file
// and an edit-then-revert serves the ORIGINAL network, never a stale one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"traffic-sim/engine"
)

func TestNetCacheContentKeyed(t *testing.T) {
	scenA := writeScenarioDir(t)          // dt 0.05
	scenB := writeScenarioDirDt(t, "0.1") // manifest-only change (dt), SAME network bytes
	scenC := writeScenarioDirLanes(t, 7)  // different network bytes entirely
	c := &netCache{dir: t.TempDir()}

	pA1, err := c.path("rec1", scenA)
	if err != nil {
		t.Fatalf("path(scenA): %v", err)
	}
	pA2, err := c.path("rec1", scenA)
	if err != nil {
		t.Fatalf("path(scenA) again: %v", err)
	}
	if pA1 != pA2 {
		t.Errorf("unchanged content: %q then %q, want a cache hit", pA1, pA2)
	}
	// The cache key is the network BYTES' hash: a manifest-only edit
	// produces the identical export, so it correctly hits the same entry…
	pB, err := c.path("rec1", scenB)
	if err != nil {
		t.Fatalf("path(scenB): %v", err)
	}
	if pB != pA1 {
		t.Errorf("manifest-only edit: %q, want the shared export %q (identical bytes)", pB, pA1)
	}
	// …while a network edit must land in a NEW cache entry.
	pC, err := c.path("rec1", scenC)
	if err != nil {
		t.Fatalf("path(scenC): %v", err)
	}
	if pC == pA1 {
		t.Errorf("edited network: %q, want a NEW cache entry (not %q)", pC, pA1)
	}
	// The poisoning case: revert to the recorded content and the ORIGINAL
	// file must be served again, not the edited one.
	pA3, err := c.path("rec1", scenA)
	if err != nil {
		t.Fatalf("path(scenA) after scenB: %v", err)
	}
	if pA3 != pA1 {
		t.Errorf("reverted content: %q, want the original %q", pA3, pA1)
	}

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("%d cache files, want exactly 2 (one per content): %v", len(entries), entries)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(pA1) && e.Name() != filepath.Base(pC) {
			t.Errorf("unexpected cache file %q", e.Name())
		}
	}
}

// writeScenarioDirLanes builds an on-disk scenario with an n-lane chain
// (plus network provenance, so the frame descriptor survives into the
// manifest and every part) sized to trip a lowered chunk threshold.
func writeScenarioDirLanes(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	nf := engine.NetFile{
		Version: 1,
		Name:    "chunk-test",
		Provenance: &engine.NetProvenance{
			Projection: "+proj=utm +zone=10 +ellps=WGS84 +datum=WGS84 +units=m +no_defs",
			NetOffset:  [2]float64{-1000, -2000},
		},
	}
	for i := 0; i < n; i++ {
		var succ []string
		if i+1 < n {
			succ = []string{fmt.Sprintf("a_%d", i+1)}
		}
		shape := make([][2]float64, 10)
		for j := range shape {
			shape[j] = [2]float64{float64(i*100 + j*10), float64(j)}
		}
		nf.Lanes = append(nf.Lanes, engine.NetLane{
			ID: fmt.Sprintf("a_%d", i), Section: "a", Length: 90, SpeedLimit: 15,
			Width: 3.2, Shape: shape, Successors: succ, Origin: i == 0, Exit: i == n-1,
		})
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "network.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `format_version: 1
id: chunk-test
seed: 7
ticks: 300
network: network.json
types: [car]
spawner:
  rate_per_lane_h: 600
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// withChunkThreshold lowers the split threshold for the test and restores
// it after (the suite runs serially).
func withChunkThreshold(t *testing.T, v int) {
	t.Helper()
	old := geojsonChunkThreshold
	geojsonChunkThreshold = v
	t.Cleanup(func() { geojsonChunkThreshold = old })
}

// readCollection parses a cached geojson file's shape relevant to the
// chunked contract (foreign members ride the map).
func readCollection(t *testing.T, path string) (features []any, frame map[string]any, raw map[string]any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("%s: not valid JSON: %v", path, err)
	}
	if raw["type"] != "FeatureCollection" {
		t.Fatalf("%s: type = %v", path, raw["type"])
	}
	feats, _ := raw["features"].([]any)
	fr, _ := raw["frame"].(map[string]any)
	return feats, fr, raw
}

// Small nets never chunk: the 2-lane fixture (~700 bytes) under a 6 KiB
// threshold produces one plain collection — no manifest, no part files.
func TestNetCacheSmallNetNoManifest(t *testing.T) {
	withChunkThreshold(t, 6<<10)
	scen := writeScenarioDir(t)
	c := &netCache{dir: t.TempDir()}
	p, err := c.path("d1", scen)
	if err != nil {
		t.Fatal(err)
	}
	feats, _, raw := readCollection(t, p)
	if len(feats) != 2 {
		t.Fatalf("features = %d, want 2 (single-file path)", len(feats))
	}
	if _, ok := raw["parts"]; ok {
		t.Fatal("small net must not carry a parts manifest member")
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".part-") {
			t.Fatalf("small net produced a part file: %s", e.Name())
		}
	}
}

// Chunked contract: over the threshold the main cache file is a MANIFEST
// (frame + empty features + ordered part URLs), every part is a valid
// standalone FeatureCollection under the threshold carrying the frame,
// and the parts reassemble to the full lane list in order.
func TestNetCacheChunked(t *testing.T) {
	withChunkThreshold(t, 6<<10)
	const lanes = 40
	scen := writeScenarioDirLanes(t, lanes)
	c := &netCache{dir: t.TempDir()}

	p, err := c.path("d1", scen)
	if err != nil {
		t.Fatal(err)
	}
	feats, frame, raw := readCollection(t, p)
	if len(feats) != 0 {
		t.Fatalf("manifest features = %d, want 0", len(feats))
	}
	if frame == nil || frame["projection"] == "" {
		t.Fatalf("manifest lost the frame descriptor: %v", frame)
	}
	partURLs, ok := raw["parts"].([]any)
	if !ok || len(partURLs) < 2 {
		t.Fatalf("manifest parts = %v, want ≥ 2 URLs", raw["parts"])
	}

	// The manifest URL shape is /net/{id}.geojson.{hash12}.part-NNN — the
	// hash pins the generation against mid-fetch scenario edits.
	urlRe := regexp.MustCompile(`^/net/d1\.geojson\.(v\d+)\.([0-9a-f]{12})\.part-(\d{3})$`)
	var genHash string
	seen := 0
	for i, u := range partURLs {
		m := urlRe.FindStringSubmatch(u.(string))
		if m == nil {
			t.Fatalf("part %d URL %v does not match the hash-pinned shape", i, u)
		}
		if m[1] != geojsonSchemaVersion {
			t.Fatalf("part URL schema = %s, want %s", m[1], geojsonSchemaVersion)
		}
		if i == 0 {
			genHash = m[2]
		} else if m[2] != genHash {
			t.Fatalf("part URLs mix generations: %s vs %s", genHash, m[2])
		}
		if m[3] != fmt.Sprintf("%03d", i) {
			t.Fatalf("part %d index = %s", i, m[3])
		}
		pp, err := c.part("d1", scen, m[1], genHash, i)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(pp)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > int64(geojsonChunkThreshold) {
			t.Fatalf("part %d is %d bytes, over the %d threshold", i, info.Size(), geojsonChunkThreshold)
		}
		pfeats, pframe, _ := readCollection(t, pp)
		if pframe == nil || pframe["projection"] != frame["projection"] {
			t.Fatalf("part %d lost the frame descriptor", i)
		}
		for _, f := range pfeats {
			id := f.(map[string]any)["id"].(string)
			if want := fmt.Sprintf("a_%d", seen); id != want {
				t.Fatalf("feature %d id = %s, want %s (order not preserved)", seen, id, want)
			}
			seen++
		}
	}
	if seen != lanes {
		t.Fatalf("reassembled %d features, want %d", seen, lanes)
	}
	if _, err := c.part("d1", scen, geojsonSchemaVersion, genHash, len(partURLs)); err == nil {
		t.Fatal("out-of-range part index must fail")
	}
	if _, err := c.part("d1", scen, geojsonSchemaVersion, "000000000000", 0); err == nil {
		t.Fatal("stale-generation part request must fail")
	}
	if _, err := c.part("d1", scen, "v999", genHash, 0); err == nil {
		t.Fatal("stale-schema part request must fail")
	}
}

// The manifest survives JSON re-encoding byte-for-byte (the contract the
// viz parses): type, frame, parts, features — nothing else required.
func TestManifestShape(t *testing.T) {
	m := geoJSONManifest{
		Type:     "FeatureCollection",
		Frame:    &engine.GeoJSONFrame{Projection: "p", NetOffset: [2]float64{1, 2}},
		Parts:    []string{"/net/d1.geojson.v2.0123456789ab.part-000"},
		Features: []any{},
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"type", "frame", "parts", "features"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("manifest missing %q: %s", k, data)
		}
	}
	if feats := raw["features"].([]any); len(feats) != 0 {
		t.Fatalf("features = %v, want []", feats)
	}
}

// HTTP contract: /net/{id}.geojson serves the manifest for a chunked net,
// /net/{id}.geojson.part-NNN serves the part, and a bad part index 404s.
func TestHandleNetChunkedParts(t *testing.T) {
	withChunkThreshold(t, 6<<10)
	scen := writeScenarioDirLanes(t, 40)
	srv, _, _ := newReplayTestServer(t, "http://127.0.0.1:1") // ctl plane unused by /net/
	srv.reg = &Registry{
		Demos: []*Demo{{ID: "d1", Title: "Demo", Run: "r1", ScenarioDir: scen, Kind: "demo"}},
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	get := func(path string) (*http.Response, map[string]any) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&v)
		return resp, v
	}

	resp, manifest := get("/net/d1.geojson")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /net/d1.geojson = %d", resp.StatusCode)
	}
	parts, ok := manifest["parts"].([]any)
	if !ok || len(parts) < 2 {
		t.Fatalf("manifest = %v, want ≥ 2 parts", manifest)
	}
	if feats := manifest["features"].([]any); len(feats) != 0 {
		t.Fatalf("manifest features = %v, want []", feats)
	}

	// Part URLs are served relative to the server root.
	partPath := parts[0].(string)
	resp, part := get(partPath)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/geo+json") {
		t.Fatalf("GET %s = %d %q", partPath, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if len(part["features"].([]any)) == 0 {
		t.Fatalf("part %s has no features", partPath)
	}
	if part["frame"] == nil {
		t.Fatalf("part %s lost the frame descriptor", partPath)
	}

	resp, _ = get("/net/d1.geojson.part-999")
	if resp.StatusCode != 404 {
		t.Fatalf("out-of-range part = %d, want 404", resp.StatusCode)
	}
	resp, _ = get("/net/d1.geojson.part-abc")
	if resp.StatusCode != 404 {
		t.Fatalf("non-numeric part index = %d, want 404", resp.StatusCode)
	}
}
