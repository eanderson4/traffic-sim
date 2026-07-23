package main

// geojson.go — per-demo network GeoJSON for the viz. Generated on first
// request from the demo's scenario network file through the engine's OWN
// exporter (engine.WriteGeoJSON — the exact code path of serve's -geojson
// flag: local metric frame + the "frame" foreign member carrying
// projection/netOffset from the network provenance, engine/geojson.go),
// then cached under -netcache. The network path comes from the scenario
// loader (traffic-sim/engine/scenario — its yaml.v3 dependency is the
// ADR-0012 §2 approved exception, confined to that package), never from
// YAML sniffing here. The cache is existence-based: delete a cache file to
// regenerate after editing a network.

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

// path returns the cached GeoJSON file for d, generating it on first use.
func (c *netCache) path(d *Demo) (string, error) {
	path := filepath.Join(c.dir, d.ID+".geojson")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Another request may have generated it while we waited.
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	scen, err := scenario.Load(d.ScenarioDir)
	if err != nil {
		return "", err
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
