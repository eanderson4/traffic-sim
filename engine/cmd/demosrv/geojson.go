package main

// geojson.go — per-entry network GeoJSON for the viz. Generated on first
// request from the entry's scenario network file through the engine's OWN
// exporter (engine.WriteGeoJSON — the exact code path of serve's -geojson
// flag: local metric frame + the "frame" foreign member carrying
// projection/netOffset from the network provenance, engine/geojson.go),
// then cached under -netcache. The network path comes from the scenario
// loader (traffic-sim/engine/scenario — its yaml.v3 dependency is the
// ADR-0012 §2 approved exception, confined to that package), never from
// YAML sniffing here.
//
// The cache is keyed by CONTENT identity — {id}.{schema}.{hash12}.geojson,
// hash12 from the scenario's ADR-0012 content hash — not by entry id
// alone: an edit-then-revert of the scenario must not resurrect a stale
// edited network from the cache (the recording hash check would pass
// against the reverted scenario while the viz renders the edited one).
// Any content change simply lands in a NEW cache file; orphans are
// harmless (a dev cache). scenario.Load per request is trivially cheap —
// the same no-memoization discipline as params.go.
//
// CHUNKED SERVING (networks over geojsonChunkThreshold — V8's ~537M-char
// string cap means the browser can never JSON.parse a bigger single
// document): /net/{id}.geojson serves a small MANIFEST instead — a valid
// FeatureCollection with the same "frame" member, an EMPTY features
// array, and a "parts" foreign member listing part URLs in lane order:
//
//	{"type":"FeatureCollection","frame":{...},
//	 "parts":["/net/{id}.geojson.{schema}.{hash12}.part-000", ...], "features":[]}
//
// Each part ({id}.{schema}.{hash}.part-NNN.geojson in the cache) is a
// standalone FeatureCollection under the threshold with a slice of the
// lanes, split at feature boundaries by engine.WriteGeoJSONRange (never
// text-split JSON). The viz fetches parts sequentially and concatenates
// features (viz/src/netload.ts). Manifest mode depends on the same
// geojsonSchemaVersion as single-file mode — parts carry the same
// per-feature schema. Generation writes every part (temp+rename) and the
// manifest LAST, so a killed generation can't serve a half-set: no
// manifest → next request regenerates, orphaned parts are harmless.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

// geojsonSchemaVersion versions the EXPORTER's output schema in the cache
// filename: the scenario content hash covers the network file, NOT the
// exporter, so a WriteGeoJSON change (new/changed properties or geometry)
// would otherwise keep serving stale pre-change cache files. BUMP THIS
// whenever engine.WriteGeoJSON's output schema changes. Superseded files
// become harmless orphans.
const geojsonSchemaVersion = "v2" // v2: + junction/row lane properties (ADR-0010)

// geojsonChunkThreshold is the single-file size limit (256 MiB): past it
// the cache generates part files + a manifest instead of one document.
// Comfortably under V8's max string length (~537M chars), so the browser
// can always parse a part. A var (not a const) so tests can lower it.
var geojsonChunkThreshold = 256 << 20

// geoJSONManifest is the chunked-serving contract (see the header comment).
type geoJSONManifest struct {
	Type     string               `json:"type"` // "FeatureCollection"
	Frame    *engine.GeoJSONFrame `json:"frame,omitempty"`
	Parts    []string             `json:"parts"`
	Features []any                `json:"features"`
}

type netCache struct {
	dir string
	mu  sync.Mutex // generation is rare and cheap; one at a time is plenty
}

// path returns the cached GeoJSON file for a demo or recording id,
// generating it on first use from the entry's scenario directory. Over the
// chunk threshold the returned file is the MANIFEST (the part files sit
// alongside it in the cache).
// errStalePart marks generation/schema mismatches in part requests — the
// ONLY part error the client should retry with a manifest refetch; every
// other failure is operational (500).
var errStalePart = errors.New("stale part generation")

// errNoPart marks a valid-generation but nonexistent part index — a plain
// 404 (bad URL), not an operational failure.
var errNoPart = errors.New("no such part")

// isManifest reports whether a cached network document is a chunk
// MANIFEST (has the parts foreign member) by sniffing its head — used
// for manifest-only response headers (no-store) without a full read.
func isManifest(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return bytes.Contains(buf[:n], []byte(`"parts"`))
}

// netKey is the cache identity: a hash of the exact network BYTES the
// export is generated from (not scenario.Hash of a separate read — a
// mid-generation edit must never cache new bytes under an old key, sol
// review). Reading + hashing even la-lean's network is far cheaper than
// the scenario content hash's canonical re-parse.
func netKey(netPath string) (string, []byte, error) {
	data, err := os.ReadFile(netPath)
	if err != nil {
		return "", nil, fmt.Errorf("netfile: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12], data, nil
}

func (c *netCache) path(id, scenarioDir string) (string, error) {
	scen, err := scenario.Load(scenarioDir)
	if err != nil {
		return "", err
	}
	hash, data, err := netKey(scen.NetPath)
	if err != nil {
		return "", err
	}
	path := filepath.Join(c.dir, id+"."+geojsonSchemaVersion+"."+hash+".geojson")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Another request may have generated it while we waited.
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	var nf engine.NetFile
	if err := json.Unmarshal(data, &nf); err != nil {
		return "", fmt.Errorf("netfile %s: %w", scen.NetPath, err)
	}
	// Generate the full document once to size it (a big-city one-off spike,
	// ~1 GB for la-lean — acceptable, it's cached from then on).
	var buf bytes.Buffer
	if err := engine.WriteGeoJSON(&nf, &buf); err != nil {
		return "", err
	}
	if buf.Len() <= geojsonChunkThreshold {
		// Small net: single-file behavior, byte-for-byte as before.
		if err := writeAtomic(path, buf.Bytes()); err != nil {
			return "", err
		}
		return path, nil
	}
	// Chunked: part files first, manifest LAST (see the header comment).
	nLanes := len(nf.Lanes)
	nParts := buf.Len()/geojsonChunkThreshold + 1
	per := (nLanes + nParts - 1) / nParts
	var partURLs []string
	partIdx := 0
	var writePart func(start, end int) error
	writePart = func(start, end int) error {
		var pbuf bytes.Buffer
		if err := engine.WriteGeoJSONRange(&nf, &pbuf, start, end); err != nil {
			return err
		}
		if pbuf.Len() > geojsonChunkThreshold && end-start > 1 {
			// A feature-heavy range (giant shapes): split finer. A single
			// lane over the threshold is emitted as-is — the client
			// contract is per-document parseability, and no real lane
			// shape approaches 256 MiB.
			mid := start + (end-start)/2
			if err := writePart(start, mid); err != nil {
				return err
			}
			return writePart(mid, end)
		}
		partPath := filepath.Join(c.dir, fmt.Sprintf("%s.%s.%s.part-%03d.geojson", id, geojsonSchemaVersion, hash, partIdx))
		if err := writeAtomic(partPath, pbuf.Bytes()); err != nil {
			return err
		}
		partURLs = append(partURLs, fmt.Sprintf("/net/%s.geojson.%s.%s.part-%03d", id, geojsonSchemaVersion, hash, partIdx))
		partIdx++
		return nil
	}
	for start := 0; start < nLanes; start += per {
		end := start + per
		if end > nLanes {
			end = nLanes
		}
		if err := writePart(start, end); err != nil {
			return "", err
		}
	}
	manifest, err := json.Marshal(geoJSONManifest{
		Type:     "FeatureCollection",
		Frame:    geoJSONFrameOf(&nf),
		Parts:    partURLs,
		Features: []any{},
	})
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, manifest); err != nil {
		return "", err
	}
	return path, nil
}

// part returns the cached part file for a chunked network, generating the
// whole set first when the cache is cold (path writes parts + manifest as
// one generation). wantSchema/wantHash pin the generation from the part
// URL: a mismatch means the exporter schema or the scenario changed since
// the client read the manifest and the part is 404 — the client must
// refetch the manifest rather than mix two generations.
func (c *netCache) part(id, scenarioDir, wantSchema, wantHash string, idx int) (string, error) {
	// Validate the pinned generation BEFORE c.path: a stale request must
	// 404 cheaply, never synchronously regenerate a multi-GB part set.
	scen, err := scenario.Load(scenarioDir)
	if err != nil {
		return "", err
	}
	hash, _, err := netKey(scen.NetPath)
	if err != nil {
		return "", err
	}
	if wantSchema != geojsonSchemaVersion {
		return "", fmt.Errorf("%w: schema %s is stale (current %s) — refetch the manifest", errStalePart, wantSchema, geojsonSchemaVersion)
	}
	if wantHash != hash {
		return "", fmt.Errorf("%w: generation %s is stale (current %s) — refetch the manifest", errStalePart, wantHash, hash)
	}
	if _, err := c.path(id, scenarioDir); err != nil {
		return "", err
	}
	p := filepath.Join(c.dir, fmt.Sprintf("%s.%s.%s.part-%03d.geojson", id, geojsonSchemaVersion, hash, idx))
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("%w %03d for %q", errNoPart, idx, id)
	}
	return p, nil
}

// geoJSONFrameOf mirrors the exporter's frame descriptor for the manifest
// (engine/geojson.go GeoJSONFrame from the network provenance).
func geoJSONFrameOf(nf *engine.NetFile) *engine.GeoJSONFrame {
	p := nf.Provenance
	if p == nil {
		return nil
	}
	return &engine.GeoJSONFrame{Projection: p.Projection, NetOffset: p.NetOffset, OSMBbox: p.OSMBbox}
}

// writeAtomic writes a cache file temp+rename: a killed demosrv must not
// leave a truncated file the next run would happily serve from the cache.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
