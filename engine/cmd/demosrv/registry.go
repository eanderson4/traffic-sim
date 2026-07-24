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
	"strings"
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
	// Kind is OUTPUT-ONLY (the /api/demos discriminator, "demo"); Load
	// overwrites whatever the JSON carried.
	Kind string `json:"kind"`
}

// Recording is one VCR menu entry (optional "recordings" array): a durable
// JetStream store written by serve -store, replayed by engine/cmd/replay
// with the live plane under {run}-replay. Store is repo-root-relative like
// ScenarioDir (validated and made absolute at load); ScenarioDir is the
// scenario the recording was made from, reused for the network GeoJSON and
// the dt deep-link hint (the recording's own RunMeta is authoritative at
// playback time — this is the menu-side path).
type Recording struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Blurb       string `json:"blurb"`
	Store       string `json:"store"`
	Run         string `json:"run"`
	ScenarioDir string `json:"scenarioDir"`
	// Kind is OUTPUT-ONLY, like Demo.Kind ("replay").
	Kind string `json:"kind"`
}

// Registry is the top-level demos.json document.
type Registry struct {
	Demos      []*Demo      `json:"demos"`
	Recordings []*Recording `json:"recordings,omitempty"`
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

// recByID looks a recording up for the /api/replay/{id}/ and /net/{id}
// routes.
func (r *Registry) recByID(id string) *Recording {
	for _, rec := range r.Recordings {
		if rec.ID == id {
			return rec
		}
	}
	return nil
}

// LoadRegistry reads and validates the registry: strict JSON (unknown
// fields are errors), unique URL-safe ids ACROSS demos and recordings
// (they share the /net/{id} namespace), existing scenario directories and
// recording store dirs (resolved to absolute paths against the CURRENT
// WORKING DIRECTORY — run demosrv from the repo root or use absolute
// paths), and single-NATS-token run ids OUTSIDE the reserved "-replay"
// namespace (they become NATS subjects inside serve/replay; ADR-0006
// reserves the suffix for replay-plane ids).
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
		return nil, fmt.Errorf("demos registry %s: at least one demo is required (recordings-only registries are not supported)", path)
	}
	seen := make(map[string]bool, len(reg.Demos)+len(reg.Recordings))
	// DEMO run ids must be unique: two demos sharing a run would publish
	// different worlds on the same ts.{run}.* subjects during swaps.
	// Recordings are deliberately NOT checked — a recording whose source
	// run matches a demo's run is the NORMAL relationship (you record a
	// demo's run), the planes are disjoint (ts.foo.* vs ts.foo-replay.*),
	// and the ctl binding compares against the ACTIVE replay, so shared
	// recording run ids are unambiguous.
	seenRuns := make(map[string]bool, len(reg.Demos))
	for i, d := range reg.Demos {
		where := fmt.Sprintf("%s demos[%d]", path, i)
		if err := checkID(seen, &where, d.ID); err != nil {
			return nil, err
		}
		if d.Title == "" {
			return nil, fmt.Errorf("demos registry %s: missing title", where)
		}
		if err := checkRun(where, d.Run); err != nil {
			return nil, err
		}
		if seenRuns[d.Run] {
			return nil, fmt.Errorf("demos registry %s: run %q is already used by another demo (demo run ids must be unique — they share one NATS-subject namespace)", where, d.Run)
		}
		seenRuns[d.Run] = true
		if d.Capacity != nil && *d.Capacity == 0 {
			return nil, fmt.Errorf("demos registry %s: capacity must be > 0", where)
		}
		abs, err := checkDir(where, "scenarioDir", d.ScenarioDir)
		if err != nil {
			return nil, err
		}
		d.ScenarioDir = abs
		d.Kind = "demo"
	}
	for i, rec := range reg.Recordings {
		where := fmt.Sprintf("%s recordings[%d]", path, i)
		if err := checkID(seen, &where, rec.ID); err != nil {
			return nil, err
		}
		if rec.Title == "" {
			return nil, fmt.Errorf("demos registry %s: missing title", where)
		}
		if err := checkRun(where, rec.Run); err != nil {
			return nil, err
		}
		abs, err := checkDir(where, "store", rec.Store)
		if err != nil {
			return nil, err
		}
		rec.Store = abs
		abs, err = checkDir(where, "scenarioDir", rec.ScenarioDir)
		if err != nil {
			return nil, err
		}
		rec.ScenarioDir = abs
		rec.Kind = "replay"
	}
	return &reg, nil
}

// checkRun validates a demo's or recording's run id: a single NATS token
// (it becomes a NATS subject and rides the app URL raw) AND outside the
// reserved replay namespace — ADR-0006 reserves the "-replay" suffix for
// replay-plane ids (natsio.RunLive/NewRecorder refuse such ids at
// activation, so without this check a registry entry would load fine and
// fail only when started; the registry fails loud at load instead).
func checkRun(where, run string) error {
	if !validRunToken(run) {
		return fmt.Errorf("demos registry %s: run %q must match [A-Za-z0-9_-]+ (it becomes a NATS subject and a raw URL query value)", where, run)
	}
	if strings.HasSuffix(run, "-replay") {
		return fmt.Errorf("demos registry %s: run %q must not end in \"-replay\" (reserved replay-plane suffix, ADR-0006)", where, run)
	}
	return nil
}

// checkID validates id and registers it in the seen set (ids are unique
// across demos AND recordings). On success it appends " (id)" to *where
// for the following field errors.
func checkID(seen map[string]bool, where *string, id string) error {
	if !validID(id) {
		return fmt.Errorf("demos registry %s: id %q must match [A-Za-z0-9_-]+ (it appears in URL paths)", *where, id)
	}
	*where += " (" + id + ")"
	if seen[id] {
		return fmt.Errorf("demos registry %s: duplicate id", *where)
	}
	seen[id] = true
	return nil
}

// checkDir validates that p names an existing directory and returns its
// absolute path.
func checkDir(where, field, p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("demos registry %s: %s: %w", where, field, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("demos registry %s: %s %s: %w", where, field, p, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("demos registry %s: %s %s is not a directory", where, field, p)
	}
	return abs, nil
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
