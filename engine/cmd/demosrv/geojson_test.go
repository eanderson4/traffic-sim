package main

// geojson_test.go — the net cache is keyed by CONTENT identity
// ({id}.{hash12}.geojson), so a scenario edit lands in a new cache file
// and an edit-then-revert serves the ORIGINAL network, never a stale one.

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNetCacheContentKeyed(t *testing.T) {
	scenA := writeScenarioDir(t)          // dt 0.05
	scenB := writeScenarioDirDt(t, "0.1") // same shape, different content hash
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
	pB, err := c.path("rec1", scenB)
	if err != nil {
		t.Fatalf("path(scenB): %v", err)
	}
	if pB == pA1 {
		t.Errorf("edited content: %q, want a NEW cache entry (not %q)", pB, pA1)
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
		if e.Name() != filepath.Base(pA1) && e.Name() != filepath.Base(pB) {
			t.Errorf("unexpected cache file %q", e.Name())
		}
	}
}
