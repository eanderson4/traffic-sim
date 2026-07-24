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
// The cache is keyed by CONTENT identity — {id}.{hash12}.geojson, hash12
// from the scenario's ADR-0012 content hash — not by entry id alone: an
// edit-then-revert of the scenario must not resurrect a stale edited
// network from the cache (the recording hash check would pass against the
// reverted scenario while the viz renders the edited one). Any content
// change simply lands in a NEW cache file; orphans are harmless (a dev
// cache). scenario.Load per request is trivially cheap — the same
// no-memoization discipline as params.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

type netCache struct {
	dir string
	mu  sync.Mutex // generation is rare and cheap; one at a time is plenty
}

// path returns the cached GeoJSON file for a demo or recording id,
// generating it on first use from the entry's scenario directory.
func (c *netCache) path(id, scenarioDir string) (string, error) {
	scen, err := scenario.Load(scenarioDir)
	if err != nil {
		return "", err
	}
	hash := scen.Hash()
	if len(hash) > 12 {
		hash = hash[:12]
	}
	path := filepath.Join(c.dir, id+"."+hash+".geojson")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Another request may have generated it while we waited.
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	data, err := os.ReadFile(scen.NetPath)
	if err != nil {
		return "", fmt.Errorf("netfile: %w", err)
	}
	var nf engine.NetFile
	if err := json.Unmarshal(data, &nf); err != nil {
		return "", fmt.Errorf("netfile %s: %w", scen.NetPath, err)
	}
	// Atomic write (temp + rename): a killed demosrv must not leave a
	// truncated file the next run would happily serve from the cache.
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if err := engine.WriteGeoJSON(&nf, f); err != nil {
		f.Close()
		os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return path, nil
}
