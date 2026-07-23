package main

// registry.go — the demos registry (default data/scenarios/demos.json; a
// committed template lives beside this tool in demos.example.json): the
// menu's source of truth. Paths inside the registry are REPO-ROOT-relative
// (documented in the tool's package comment); they are validated and made
// absolute at load. Validation is fail-loud — a broken menu entry is a
// config error, not a runtime surprise (the ADR-0012 strict-fence
// precedent: unknown fields, duplicates, and dangling references are all
// load-time errors).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Demo is one menu entry. Seed and Ticks, when present, override the
// scenario manifest for this demo (the deliberate-sweep escape hatch
// serve's -seed/-ticks already provide); absent = the manifest speaks.
// Capacity, when present, sets the driver's claim capacity (-capacity):
// raise it for overload demos — ADR-0008 §6's pause gate wedges a run
// once live unclaimed vehicles exceed spare capacity (found by the
// stress-dtla import: in a jam the active count never drops, so the
// gate never reopens).
type Demo struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Blurb       string   `json:"blurb"`
	Tags        []string `json:"tags"`
	ScenarioDir string   `json:"scenarioDir"`
	Run         string   `json:"run"`
	Seed        *uint64  `json:"seed,omitempty"`
	Ticks       *uint64  `json:"ticks,omitempty"`
	Capacity    *uint64  `json:"capacity,omitempty"`
}

// Registry is the top-level demos.json document.
type Registry struct {
	Demos []*Demo `json:"demos"`
}

// byID looks a demo up for the /api/demo/{id}/ and /net/{id} routes.
func (r *Registry) byID(id string) *Demo {
	for _, d := range r.Demos {
		if d.ID == id {
			return d
		}
	}
	return nil
}

// LoadRegistry reads and validates the registry: strict JSON (unknown
// fields are errors), unique URL-safe ids, existing scenario directories
// (resolved to absolute paths against the CURRENT WORKING DIRECTORY — run
// demosrv from the repo root or use absolute paths), and single-NATS-token
// run ids (they become NATS subjects inside serve).
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("demos registry: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var reg Registry
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("demos registry %s: %w", path, err)
	}
	if len(reg.Demos) == 0 {
		return nil, fmt.Errorf("demos registry %s: no demos", path)
	}
	seen := make(map[string]bool, len(reg.Demos))
	for i, d := range reg.Demos {
		where := fmt.Sprintf("%s demos[%d]", path, i)
		if !validID(d.ID) {
			return nil, fmt.Errorf("demos registry %s: id %q must match [A-Za-z0-9_-]+ (it appears in URL paths)", where, d.ID)
		}
		where += " (" + d.ID + ")"
		if seen[d.ID] {
			return nil, fmt.Errorf("demos registry %s: duplicate id", where)
		}
		seen[d.ID] = true
		if d.Title == "" {
			return nil, fmt.Errorf("demos registry %s: missing title", where)
		}
		if !validRunToken(d.Run) {
			return nil, fmt.Errorf("demos registry %s: run %q must match [A-Za-z0-9_-]+ (it becomes a NATS subject and a raw URL query value)", where, d.Run)
		}
		if d.Capacity != nil && *d.Capacity == 0 {
			return nil, fmt.Errorf("demos registry %s: capacity must be > 0", where)
		}
		abs, err := filepath.Abs(d.ScenarioDir)
		if err != nil {
			return nil, fmt.Errorf("demos registry %s: scenarioDir: %w", where, err)
		}
		st, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("demos registry %s: scenarioDir %s: %w", where, d.ScenarioDir, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("demos registry %s: scenarioDir %s is not a directory", where, d.ScenarioDir)
		}
		d.ScenarioDir = abs
	}
	return &reg, nil
}

// validID gates the id that appears in /api/demo/{id}/start and
// /net/{id}.geojson paths.
func validID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
		if !ok {
			return false
		}
	}
	return true
}

// validRunToken mirrors serve's "run id (single NATS token)" flag contract
// (no dots, wildcards, or whitespace) AND the URL charset: the run id is
// interpolated raw into the app URL query string on both demosrv and the
// menu client, so URL metacharacters (& # % + = ?) would corrupt it —
// URLSearchParams decodes '+' as space and the viz would subscribe to the
// wrong subject (silently blank map). Same charset as validID keeps the
// byte-identical-URL invariant trivially true.
func validRunToken(s string) bool {
	return validID(s)
}
