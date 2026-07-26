// Package scenario loads ADR-0012 scenario directories: a strict-YAML
// manifest (scenario.yaml) referencing network, demand, control, and metrics
// part files — or a variant.yaml overlay materialized against its base
// (ADR-0012 §4, M12) — into an engine.RunSpec plus director demand
// definitions. It also provides the canonical formatter and the content
// hash — the run key is (content-hash, seed).
//
// Dependency note (AGENTS.md "standard library first"): gopkg.in/yaml.v3 is
// the ADR-0012 §2 approved exception, confined to this package exactly like
// the NATS modules are confined to engine/natsio (ADR-0006). The engine
// kernel consumes only the loaded model and stays stdlib-only. Bumping the
// YAML library is a format event: the canonical byte format and the content
// hash both ride on its encoder, and the golden-hash test is the canary.
//
// Validation note (ADR-0012 §2 implementation refinement): the "JSON-Schema
// validation at load" is realized as strict decoding (KnownFields — unknown
// fields are hard errors) plus a schema-aware node-type check (a !!float
// never silently truncates into an int field, a !!bool never coerces into a
// string) plus hand-written semantic checks — no JSON-Schema library
// dependency.
package scenario

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"traffic-sim/engine"
)

// FormatVersion is the only format_version this loader accepts (ADR-0012
// §8: newer is a hard error, never a partial read; N−1 support arrives with
// format_version 2).
const FormatVersion = 1

// ManifestFile is the required manifest name at the scenario directory root.
const ManifestFile = "scenario.yaml"

// hashDomain separates the content-hash protocol from every other sha256 in
// the project and versions it: changing the canonicalization or framing is
// a protocol bump, never a silent rebrand of every scenario (ADR-0012 §6).
const hashDomain = "traffic-sim/scenario-hash/v1\n"

// Manifest is scenario.yaml — identity, engine params, and relative-path
// references to the part files. The directory, never the manifest alone, is
// the unit of authorship, diffing, and hashing (ADR-0012 §1).
type Manifest struct {
	FormatVersion int      `yaml:"format_version"`
	ID            string   `yaml:"id"`
	Seed          uint64   `yaml:"seed"`
	Ticks         uint64   `yaml:"ticks"`
	Params        Params   `yaml:"params,omitempty"`
	Network       string   `yaml:"network"`
	Types         []string `yaml:"types,omitempty"`
	Spawner       *Spawner `yaml:"spawner,omitempty"`
	Demand        []string `yaml:"demand,omitempty"`
	Control       []string `yaml:"control,omitempty"`
	Metrics       []string `yaml:"metrics,omitempty"`
}

// Params carries engine parameters; zero fields take the engine defaults
// (ADR-0005: tick length is a scenario parameter, 100 ms the default).
type Params struct {
	Dt float64 `yaml:"dt,omitempty"`
}

// Spawner configures the kernel's built-in deterministic spawner — the
// flag-era uniform demand made declarative. Absent (or rate 0) = the
// built-in spawner is disabled and demand arrives as director verbs.
type Spawner struct {
	RatePerLaneHour float64 `yaml:"rate_per_lane_h"`
	DensityPerKm    float64 `yaml:"density_per_km,omitempty"`
}

// DemandFile is one demand/*.yaml part: the director flow definitions the
// M10 runtime demand director samples (ADR-0012 §3 — a demand file IS a
// director configuration). Times are SIM SECONDS (never wall clock,
// ADR-0005). M10-era strict-JSON flow files parse once a
// `"format_version": 1` key is added (JSON is a YAML subset; the version
// key is required — strictness is the point).
type DemandFile struct {
	FormatVersion int    `yaml:"format_version"`
	Flows         []Flow `yaml:"flows"`
}

// Flow is one origin's demand program. With Slices, the rate is
// piecewise-constant (0 outside any slice); without, VehPerH holds for the
// whole run. ID is optional today and becomes the overlay-patch anchor
// (ADR-0012 §4) — additive and omitempty, so adding it later never moves an
// existing hash.
type Flow struct {
	ID      string             `yaml:"id,omitempty"`
	Origin  string             `yaml:"origin"`
	VehPerH float64            `yaml:"veh_per_h,omitempty"`
	Spacing string             `yaml:"spacing"`
	VTypes  map[string]float64 `yaml:"vtypes,omitempty"`
	Slices  []Slice            `yaml:"slices,omitempty"`
	UntilS  float64            `yaml:"until_s,omitempty"`
	// Destinations is an optional weighted destination-lane distribution
	// (ADR-0021): the director draws one per vehicle and the engine ends
	// the trip there. Weights are relative, drawn over the SORTED key list
	// exactly like VTypes — Go map order can never reach a sampled path
	// (ADR-0005). Empty leaves the vehicle unrouted, so the driver's own
	// exit routing (ADR-0019) still applies.
	Destinations map[string]float64 `yaml:"destinations,omitempty"`
	// OffsetM is the injection position along the origin lane in meters.
	// Zero (omitted) means a boundary PORTAL and the origin must be one.
	// A positive value is the interior-origin opt-in (ADR-0021): a
	// mid-block garage or driveway. The explicitness is the safety
	// property — a mistyped portal id cannot silently become a
	// mid-network injection.
	OffsetM float64 `yaml:"offset_m,omitempty"`
}

// Slice is one piecewise-constant demand window [StartS, EndS) in sim
// seconds; adjacent windows (EndS == the next StartS) are permitted.
type Slice struct {
	StartS  float64 `yaml:"start_s"`
	EndS    float64 `yaml:"end_s"`
	VehPerH float64 `yaml:"veh_per_h"`
}

// Scenario is a loaded, validated scenario directory.
type Scenario struct {
	Dir      string
	Manifest Manifest
	Demands  []*DemandFile  // aligned with Manifest.Demand
	Metrics  []*MetricsFile // aligned with Manifest.Metrics
	NetPath  string         // resolved network path (absolute)
	lanes    *netLanes
	parts    []hashPart // everything the content hash covers
}

// hashPart is one hashed input: rel is the scenario-relative path (forward
// slashes, cleaned); data is canonical YAML for the manifest, demand, and
// metrics parts and canonical JSON for the network part (so the hash is
// formatting- and checkout-independent), raw bytes for the not-yet-typed
// control parts.
type hashPart struct {
	rel  string
	data []byte
}

// Load reads, validates, and materializes a scenario directory (ADR-0012:
// fail-loud everywhere — unknown fields, bad references, and semantic
// errors are all load-time errors). A directory holding variant.yaml
// instead of scenario.yaml is an overlay (ADR-0012 §4, M12): it is
// materialized against its base and then treated exactly like a
// hand-authored scenario.
func Load(dir string) (*Scenario, error) {
	_, mErr := os.Stat(filepath.Join(dir, ManifestFile))
	_, vErr := os.Stat(filepath.Join(dir, VariantManifest))
	if mErr == nil && vErr == nil {
		return nil, fmt.Errorf("scenario %s: both %s and %s present — a directory is either a scenario or a variant (ADR-0012 §4)", dir, ManifestFile, VariantManifest)
	}
	// Only not-exist means "absent" — an I/O or permission error must not
	// silently reroute manifest dispatch (fail loud, ADR-0012 §2).
	if mErr != nil && !errors.Is(mErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("scenario %s: %w", filepath.Join(dir, ManifestFile), mErr)
	}
	if vErr != nil && !errors.Is(vErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("scenario %s: %w", filepath.Join(dir, VariantManifest), vErr)
	}
	if vErr == nil {
		return loadVariant(dir)
	}
	s := &Scenario{Dir: dir}
	mdata, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("scenario: %w", err)
	}
	var m Manifest
	if err := strictDecode(mdata, &m); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", ManifestFile, err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", ManifestFile, err)
	}
	s.Manifest = m
	// The hashed manifest strips the run coordinates (seed, ticks): the run
	// key is (content-hash, seed), so the seed cannot also be content, and
	// determinism makes a longer run of the same scenario a strict
	// trajectory superset of a shorter one — ticks is recorded metadata,
	// not identity (ADR-0012 §6 addendum). Note the hash covers the
	// DEFAULTED model (e.g. types: [car]) — loader defaults are part of
	// the format, versioned by format_version.
	hm := m
	hm.Seed, hm.Ticks = 0, 0
	s.parts = append(s.parts, hashPart{rel: ManifestFile, data: canonicalYAML(&hm)})

	// Network: parsed and re-emitted as canonical JSON for the hash (git
	// autocrlf/eol rewrites and netimport formatting choices must not move
	// identity); the compile validates the file itself and supplies the
	// origin set for demand validation.
	netPath, err := resolvePart(dir, m.Network)
	if err != nil {
		return nil, fmt.Errorf("scenario %s: network: %w", ManifestFile, err)
	}
	rawNet, err := os.ReadFile(netPath)
	if err != nil {
		return nil, fmt.Errorf("scenario network: %w", err)
	}
	var nf engine.NetFile
	if err := json.Unmarshal(rawNet, &nf); err != nil {
		return nil, fmt.Errorf("scenario network %s: %w", m.Network, err)
	}
	net, err := engine.CompileNet(&nf)
	if err != nil {
		return nil, fmt.Errorf("scenario network %s: %w", m.Network, err)
	}
	s.NetPath = netPath
	s.lanes = newNetLanes(net)
	s.parts = append(s.parts, hashPart{rel: m.Network, data: canonicalJSON(rawNet, m.Network)})

	typeSet := make(map[string]bool, len(m.Types))
	for _, t := range m.Types {
		typeSet[t] = true
	}
	for _, ref := range m.Demand {
		p, err := resolvePart(dir, ref)
		if err != nil {
			return nil, fmt.Errorf("scenario demand: %w", err)
		}
		df, canonical, err := loadDemandFile(p, s.lanes, typeSet)
		if err != nil {
			return nil, fmt.Errorf("scenario demand %s: %w", ref, err)
		}
		s.Demands = append(s.Demands, df)
		s.parts = append(s.parts, hashPart{rel: ref, data: canonical})
	}
	// Control parts are v1 pass-through: existence-checked and hashed as raw
	// bytes (their binding grammar is still pending). Metrics parts are
	// typed (ADR-0014 §5): parsed, validated against the network's lanes
	// and the scenario's tick grid, hashed as canonical YAML — a variant
	// that changes measurement is a different scenario (ADR-0012 §5).
	for _, ref := range m.Control {
		p, err := resolvePart(dir, ref)
		if err != nil {
			return nil, fmt.Errorf("scenario part: %w", err)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("scenario part %s: %w", ref, err)
		}
		s.parts = append(s.parts, hashPart{rel: ref, data: raw})
	}
	laneSet := make(map[string]bool, len(net.Lanes))
	for _, l := range net.Lanes {
		laneSet[l.ID] = true
	}
	for _, ref := range m.Metrics {
		p, err := resolvePart(dir, ref)
		if err != nil {
			return nil, fmt.Errorf("scenario metrics: %w", err)
		}
		mf, canonical, err := loadMetricsFile(p, laneSet, s.tickSeconds())
		if err != nil {
			return nil, fmt.Errorf("scenario metrics %s: %w", ref, err)
		}
		s.Metrics = append(s.Metrics, mf)
		s.parts = append(s.parts, hashPart{rel: ref, data: canonical})
	}
	if err := validateMetricSetIDs(s.Metrics); err != nil {
		return nil, fmt.Errorf("scenario %s: %w", ManifestFile, err)
	}
	if (m.Spawner == nil || m.Spawner.RatePerLaneHour == 0) && len(m.Demand) == 0 {
		return nil, fmt.Errorf("scenario %s: no demand — set spawner.rate_per_lane_h > 0 or demand: (an empty run is not a scenario)", ManifestFile)
	}
	return s, nil
}

// LoadDemandFile parses one demand part standalone (the demand-director's
// input). Semantic checks apply, but origin lanes and the scenario type
// list are unknown here — the engine rejects a bad origin at verb time
// (request/reply, ADR-0006 M10 addendum).
func LoadDemandFile(path string) (*DemandFile, error) {
	df, _, err := loadDemandFile(path, nil, nil)
	return df, err
}

// netLanes is the network view demand validation needs: which lanes are
// boundary portals, and the length of EVERY lane — interior injection
// offsets and destination ids are checked against the whole lane set, not
// just the portals. A nil *netLanes disables the network-dependent checks
// (the network-free load path used by `scenario fmt`).
type netLanes struct {
	origin  map[string]bool
	length  map[string]float64
	endWall map[string]bool
}

// validateDestinations checks a flow's weighted destination distribution
// (ADR-0021): every key must name a lane of the network and every weight
// must be a positive finite number. An empty map is valid and means
// "unrouted" — a zero-weight or negative entry is not, because it would be
// dead config that silently never draws.
func validateDestinations(where string, dests map[string]float64, lanes *netLanes) error {
	if len(dests) == 0 {
		return nil
	}
	names := make([]string, 0, len(dests))
	for n := range dests {
		names = append(names, n)
	}
	sort.Strings(names) // errors must not depend on map order
	for _, n := range names {
		w := dests[n]
		if err := finite(fmt.Sprintf("%s destination %q weight", where, n), w); err != nil {
			return err
		}
		if w <= 0 {
			return fmt.Errorf("%s: destination %q weight must be > 0", where, n)
		}
		if lanes != nil {
			if _, ok := lanes.length[n]; !ok {
				return fmt.Errorf("%s: destination %q is not a lane of the network", where, n)
			}
			// Arrival is S > Lane.Length, but an EndWall lane stops its
			// traffic short of that line, so a vehicle routed to one never
			// arrives and never despawns. Reject the config instead of
			// leaking vehicles for the whole run (director.go applies the
			// same rule to the spawn verb).
			if lanes.endWall[n] {
				return fmt.Errorf("%s: destination %q ends in a wall — a vehicle routed there stops short of the lane end and can never arrive", where, n)
			}
		}
	}
	return nil
}

func newNetLanes(net *engine.Network) *netLanes {
	nl := &netLanes{
		origin:  make(map[string]bool, len(net.Origins)),
		length:  make(map[string]float64, len(net.Lanes)),
		endWall: map[string]bool{},
	}
	for _, l := range net.Origins {
		nl.origin[l.ID] = true
	}
	for _, l := range net.Lanes {
		nl.length[l.ID] = l.Length
		if l.EndWall {
			nl.endWall[l.ID] = true
		}
	}
	return nl
}

func loadDemandFile(path string, lanes *netLanes, typeSet map[string]bool) (*DemandFile, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return parseDemand(data, lanes, typeSet)
}

// parseDemand is the byte-level core of loadDemandFile — variant loading
// feeds it patched documents without touching the base's files on disk.
func parseDemand(data []byte, lanes *netLanes, typeSet map[string]bool) (*DemandFile, []byte, error) {
	var df DemandFile
	if err := strictDecode(data, &df); err != nil {
		return nil, nil, err
	}
	if err := validateDemand(&df, lanes, typeSet); err != nil {
		return nil, nil, err
	}
	return &df, canonicalYAML(&df), nil
}

// RunSpec materializes the scenario into everything a run consumes
// (ADR-0012 §6: the content hash rides in the spec, so the run registry's
// meta entry records (content-hash, seed)).
func (s *Scenario) RunSpec(typeReg map[string]*engine.VehicleType) (engine.RunSpec, error) {
	types := make([]*engine.VehicleType, 0, len(s.Manifest.Types))
	for _, name := range s.Manifest.Types {
		t, ok := typeReg[name]
		if !ok {
			known := make([]string, 0, len(typeReg))
			for k := range typeReg {
				known = append(known, k)
			}
			sort.Strings(known)
			return engine.RunSpec{}, fmt.Errorf("unknown vehicle type %q (known: %s)", name, strings.Join(known, ", "))
		}
		types = append(types, t)
	}
	spec := engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: s.NetPath},
		Scen:   engine.Scenario{Types: types},
		Params: engine.DefaultParams(),
		Seed:   s.Manifest.Seed,
		Ticks:  s.Manifest.Ticks,
		Hash:   s.Hash(),
	}
	if s.Manifest.Params.Dt != 0 {
		spec.Params.Dt = s.Manifest.Params.Dt
	}
	if sp := s.Manifest.Spawner; sp != nil {
		spec.Scen.SpawnRatePerLaneHour = sp.RatePerLaneHour
		spec.Scen.DensityTargetPerKm = sp.DensityPerKm
	}
	return spec, nil
}

// Hash is the scenario's content identity (ADR-0012 §6): a domain-separated
// sha256 over the canonical manifest (run coordinates stripped), demand, and
// metrics bytes, the canonical network JSON, and the raw control bytes, each
// framed by its length-prefixed, slash-normalized scenario-relative path.
// Formatting- and checkout-independent for the typed parts; stable across
// copies, renames, and machines.
func (s *Scenario) Hash() string {
	parts := append([]hashPart{}, s.parts...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].rel < parts[j].rel })
	h := sha256.New()
	io.WriteString(h, hashDomain)
	var lenBuf [8]byte
	frame := func(b []byte) {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(b)))
		h.Write(lenBuf[:])
		h.Write(b)
	}
	for _, p := range parts {
		frame([]byte(p.rel))
		frame(p.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Format rewrites the manifest and demand parts in canonical form —
// alphabetically sorted mapping keys, 2-space indent — operating on the
// parsed node tree so COMMENTS SURVIVE (ADR-0012 §2 chose YAML for
// comments; a formatter that deletes them defeats the choice). Files
// already canonical are left untouched; rewrites are staged to temp files
// and renamed, so a failure never leaves a partially formatted directory.
// Network, control, and metrics parts are untouched (not our formats).
// On a variant directory only the variant's OWN files are formatted
// (variant.yaml and its added demand parts) — never the base's.
func Format(dir string) error {
	if _, err := Load(dir); err != nil {
		return err // fmt never canonicalizes a broken scenario
	}
	targets := []string{ManifestFile}
	_, vErr := os.Stat(filepath.Join(dir, VariantManifest))
	switch {
	case vErr == nil:
		v, err := readVariant(dir)
		if err != nil {
			return err
		}
		targets = append([]string{VariantManifest}, v.Demand...)
	case !errors.Is(vErr, fs.ErrNotExist):
		// Same fail-loud dispatch discipline as Load: only not-exist
		// means "not a variant".
		return fmt.Errorf("scenario %s: %w", filepath.Join(dir, VariantManifest), vErr)
	default:
		m, err := readManifest(dir)
		if err != nil {
			return err
		}
		targets = append(targets, m.Demand...)
	}
	type staged struct {
		final string
		tmp   string
	}
	var files []staged
	for _, rel := range targets {
		final, err := resolvePart(dir, rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(final)
		if err != nil {
			return err
		}
		canonical, err := canonicalizePreservingComments(data)
		if err != nil {
			return fmt.Errorf("fmt %s: %w", rel, err)
		}
		if bytes.Equal(data, canonical) {
			continue
		}
		tmp := final + ".fmt-tmp"
		if err := os.WriteFile(tmp, canonical, 0o644); err != nil {
			return err
		}
		files = append(files, staged{final: final, tmp: tmp})
	}
	for _, f := range files {
		if err := os.Rename(f.tmp, f.final); err != nil {
			return err
		}
	}
	return nil
}

func readManifest(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := strictDecode(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func readVariant(dir string) (*Variant, error) {
	data, err := os.ReadFile(filepath.Join(dir, VariantManifest))
	if err != nil {
		return nil, err
	}
	var v Variant
	if err := strictDecode(data, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// resolvePart joins a scenario-relative part reference to the directory.
// References must be relative, slash-separated, already-clean paths that
// stay inside the directory — lexically (no "..", no absolute, no
// backslashes) AND physically (symlinks may not escape; the directory is
// the unit of sharing, ADR-0012 §1).
func resolvePart(dir, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("empty part reference")
	}
	if strings.ContainsRune(ref, '\\') {
		return "", fmt.Errorf("part %q: use forward slashes (portable refs)", ref)
	}
	if len(ref) > 1 && ref[1] == ':' {
		return "", fmt.Errorf("part %q: drive-qualified paths are not portable refs", ref)
	}
	if path.IsAbs(ref) || path.Clean(ref) != ref || ref == ".." || strings.HasPrefix(ref, "../") {
		return "", fmt.Errorf("part %q: must be a clean relative path inside the scenario directory", ref)
	}
	final := filepath.Join(dir, filepath.FromSlash(ref))
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	realPart, err := filepath.EvalSymlinks(final)
	if err != nil {
		return "", err
	}
	if realPart != realDir && !strings.HasPrefix(realPart, realDir+string(filepath.Separator)) {
		return "", fmt.Errorf("part %q: symlink escapes the scenario directory", ref)
	}
	return final, nil
}

func validateManifest(m *Manifest) error {
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d (loader supports %d)", m.FormatVersion, FormatVersion)
	}
	if m.ID == "" {
		return errors.New("missing id")
	}
	if m.Ticks == 0 {
		return errors.New("missing ticks (the run's tick budget)")
	}
	if m.Network == "" {
		return errors.New("missing network")
	}
	if err := finite("params.dt", m.Params.Dt); err != nil {
		return err
	}
	if m.Params.Dt < 0 || m.Params.Dt > 1.0 {
		return errors.New("params.dt must be in (0, 1.0] seconds (0 = engine default 0.1; ADR-0005 bounds the tick at 1 s)")
	}
	if len(m.Types) == 0 {
		m.Types = []string{"car"}
	}
	seen := make(map[string]bool, len(m.Types))
	for _, t := range m.Types {
		if t == "" || t != strings.TrimSpace(t) {
			return fmt.Errorf("types: %q is not a canonical type name (no surrounding whitespace)", t)
		}
		if seen[t] {
			return fmt.Errorf("types: duplicate %q", t)
		}
		seen[t] = true
	}
	if m.Spawner != nil {
		if err := finite("spawner.rate_per_lane_h", m.Spawner.RatePerLaneHour); err != nil {
			return err
		}
		if err := finite("spawner.density_per_km", m.Spawner.DensityPerKm); err != nil {
			return err
		}
		if m.Spawner.RatePerLaneHour < 0 {
			return errors.New("spawner.rate_per_lane_h must be >= 0 (0 = spawner disabled, director-driven)")
		}
		if m.Spawner.DensityPerKm < 0 {
			return errors.New("spawner.density_per_km must be >= 0 (0 = uncapped)")
		}
	}
	// Part refs are hash frames: duplicates would silently double flows
	// (and under an overlay, patch the first copy only), and a ref naming
	// a manifest would hash the run coordinates as content — both break
	// the directory-as-identity rule (ADR-0012 §1/§6).
	refs := append(append(append([]string{}, m.Demand...), m.Control...), m.Metrics...)
	seenRefs := make(map[string]bool, len(refs)+1)
	seenRefs[m.Network] = true // the network is a hash frame too
	for _, r := range refs {
		if r == ManifestFile || r == VariantManifest {
			return fmt.Errorf("part %q names a manifest file — not a part", r)
		}
		if seenRefs[r] {
			return fmt.Errorf("duplicate part reference %q", r)
		}
		seenRefs[r] = true
	}
	return nil
}

func validateDemand(df *DemandFile, lanes *netLanes, typeSet map[string]bool) error {
	if df.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d (loader supports %d)", df.FormatVersion, FormatVersion)
	}
	if len(df.Flows) == 0 {
		return errors.New("no flows")
	}
	flowIDs := make(map[string]bool, len(df.Flows))
	for i, f := range df.Flows {
		where := fmt.Sprintf("flow %d (%s)", i, f.Origin)
		if f.ID != "" {
			if flowIDs[f.ID] {
				return fmt.Errorf("%s: duplicate flow id %q", where, f.ID)
			}
			flowIDs[f.ID] = true
		}
		if f.Origin == "" {
			return fmt.Errorf("flow %d: missing origin", i)
		}
		if err := finite(where+" offset_m", f.OffsetM); err != nil {
			return err
		}
		switch {
		case f.OffsetM < 0:
			return fmt.Errorf("%s: offset_m must be ≥ 0", where)
		case f.OffsetM > 0:
			// Interior origin (ADR-0021): any lane, but the offset must
			// leave the vehicle wholly on it.
			if lanes != nil {
				ln, ok := lanes.length[f.Origin]
				if !ok {
					return fmt.Errorf("%s: not a lane of the network", where)
				}
				if f.OffsetM >= ln {
					return fmt.Errorf("%s: offset_m %.2f is past the end of the %.2f m lane", where, f.OffsetM, ln)
				}
			}
		default:
			if lanes != nil && !lanes.origin[f.Origin] {
				return fmt.Errorf("%s: not a spawn origin lane of the network (interior injection needs an explicit offset_m)", where)
			}
		}
		if err := validateDestinations(where, f.Destinations, lanes); err != nil {
			return err
		}
		if f.Spacing != "constant" && f.Spacing != "poisson" {
			return fmt.Errorf("%s: spacing %q (want constant|poisson)", where, f.Spacing)
		}
		if err := finite(where+" veh_per_h", f.VehPerH); err != nil {
			return err
		}
		if err := finite(where+" until_s", f.UntilS); err != nil {
			return err
		}
		if len(f.Slices) == 0 {
			if f.VehPerH <= 0 {
				return fmt.Errorf("%s: veh_per_h must be > 0 without slices", where)
			}
		} else {
			if f.VehPerH != 0 {
				return fmt.Errorf("%s: veh_per_h is dead config alongside slices (slices define the whole rate program) — remove it", where)
			}
		}
		prevEnd := 0.0
		for j, sl := range f.Slices {
			if err := finite(fmt.Sprintf("%s slice %d", where, j), sl.StartS, sl.EndS, sl.VehPerH); err != nil {
				return err
			}
			if sl.StartS < 0 || sl.EndS <= sl.StartS {
				return fmt.Errorf("%s slice %d: need 0 <= start_s < end_s", where, j)
			}
			if j > 0 && sl.StartS < prevEnd {
				return fmt.Errorf("%s slice %d: overlaps the previous slice (sorted, non-overlapping windows; adjacency is fine)", where, j)
			}
			if sl.VehPerH < 0 {
				return fmt.Errorf("%s slice %d: veh_per_h must be >= 0", where, j)
			}
			prevEnd = sl.EndS
		}
		if f.UntilS < 0 {
			return fmt.Errorf("%s: until_s must be >= 0", where)
		}
		if len(f.VTypes) == 0 && typeSet != nil && !typeSet["car"] {
			return fmt.Errorf("%s: no vtypes given, so the sampler would emit the default \"car\" — not in the scenario type list", where)
		}
		for name, w := range f.VTypes {
			if err := finite(where+" vtype "+name, w); err != nil {
				return err
			}
			if w <= 0 {
				return fmt.Errorf("%s: vtype %q weight must be > 0", where, name)
			}
			if typeSet != nil && !typeSet[name] {
				return fmt.Errorf("%s: vtype %q is not in the scenario type list", where, name)
			}
		}
	}
	return nil
}

// finite rejects NaN and ±Inf — plain !!float scalars that sail through
// every ordered comparison (IEEE's sharpest footgun in a fail-loud loader).
func finite(name string, vs ...float64) error {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("%s: %v is not a finite number", name, v)
		}
	}
	return nil
}

// strictDecode parses the ADR-0012 strict-YAML subset into v: exactly one
// document, no anchors/aliases/merge keys/custom tags, no unknown fields,
// and no silent scalar coercions (a !!float never truncates into an int
// field, a !!bool never becomes a string — checked against the destination
// schema, because yaml.v3's decoder does both silently).
func strictDecode(data []byte, v any) error {
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		if err == io.EOF {
			return errors.New("empty file")
		}
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("one document per file (strict YAML)")
	}
	if err := rejectNonStrict(&root); err != nil {
		return err
	}
	if err := checkNodeTypes(&root, reflect.TypeOf(v)); err != nil {
		return err
	}
	kf := yaml.NewDecoder(bytes.NewReader(data))
	kf.KnownFields(true)
	if err := kf.Decode(v); err != nil {
		return err
	}
	return nil
}

// rejectNonStrict walks the node tree refusing the YAML features the
// strict subset fences out (ADR-0012 §2): anchors/aliases create invisible
// long-range coupling; merge keys are alias-adjacent; custom tags are
// invisible typing. (Duplicate mapping keys are a yaml.v3 parse error for
// map/struct destinations; yaml.Node destinations — patch sets — are
// covered by rejectDupKeys in variant.go.)
func rejectNonStrict(n *yaml.Node) error {
	if n.Kind == yaml.DocumentNode {
		for _, c := range n.Content {
			if err := rejectNonStrict(c); err != nil {
				return err
			}
		}
		return nil
	}
	if n.Alias != nil {
		return fmt.Errorf("line %d: aliases are not strict YAML", n.Line)
	}
	if n.Anchor != "" {
		return fmt.Errorf("line %d: anchors are not strict YAML", n.Line)
	}
	switch n.Tag {
	case "!!str", "!!int", "!!float", "!!bool", "!!null", "!!map", "!!seq":
	case "!!timestamp":
		return fmt.Errorf("line %d: unquoted %q resolves to a timestamp — quote it (strict YAML admits no implicit typing)", n.Line, n.Value)
	default:
		return fmt.Errorf("line %d: tag %s is not strict YAML (custom tags and merge keys are forbidden)", n.Line, n.Tag)
	}
	for _, c := range n.Content {
		if err := rejectNonStrict(c); err != nil {
			return err
		}
	}
	return nil
}

// checkNodeTypes verifies scalar node tags against the destination Go type
// BEFORE decoding: yaml.v3 silently truncates 1.9 → 1 into int fields and
// stringifies true → "true" into string fields, both of which the strict
// subset classifies as implicit typing. Int destinations require !!int;
// float destinations accept !!int or !!float (a YAML int IS a float);
// string destinations require !!str; bool destinations require !!bool.
func checkNodeTypes(n *yaml.Node, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// A yaml.Node destination (a patch's set) takes the subtree raw —
	// checking it against Node's own struct schema would misreport patch
	// keys that collide with Node field names; the merged document gets
	// the real schema check at apply time.
	if t == reflect.TypeOf(yaml.Node{}) {
		return nil
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return checkNodeTypes(n.Content[0], t)
	case yaml.MappingNode:
		if t.Kind() == reflect.Map {
			vt := t.Elem()
			for i := 0; i+1 < len(n.Content); i += 2 {
				if err := scalarTag(n.Content[i], reflect.TypeOf("")); err != nil {
					return err
				}
				if err := checkNodeTypes(n.Content[i+1], vt); err != nil {
					return err
				}
			}
			return nil
		}
		if t.Kind() != reflect.Struct {
			return nil
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			ft, ok := fields[n.Content[i].Value]
			if !ok {
				continue // unknown field: KnownFields decode reports it
			}
			if err := checkNodeTypes(n.Content[i+1], ft); err != nil {
				return err
			}
		}
		return nil
	case yaml.SequenceNode:
		if t.Kind() == reflect.Slice {
			for _, c := range n.Content {
				if err := checkNodeTypes(c, t.Elem()); err != nil {
					return err
				}
			}
		}
		return nil
	case yaml.ScalarNode:
		return scalarTag(n, t)
	}
	return nil
}

// yamlFields indexes a struct's YAML-visible fields by their resolved key
// (explicit tag, else the lowercased field name — yaml.v3's rule).
func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := make(map[string]reflect.Type, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

func scalarTag(n *yaml.Node, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n.Tag == "!!null" {
		return nil // null into anything omitempty-shaped is the author's business
	}
	want := ""
	switch t.Kind() {
	case reflect.String:
		want = "!!str"
	case reflect.Bool:
		want = "!!bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		want = "!!int"
	case reflect.Float32, reflect.Float64:
		if n.Tag == "!!int" || n.Tag == "!!float" {
			return nil
		}
		want = "!!float"
	default:
		return nil // maps/slices are structural; interface{} accepts anything
	}
	if n.Tag != want {
		return fmt.Errorf("line %d: %q is %s but the schema wants %s here (strict YAML admits no implicit typing — quote strings, use exact numbers)", n.Line, n.Value, n.Tag, want)
	}
	return nil
}

// canonicalizePreservingComments re-emits a YAML document with mapping keys
// sorted alphabetically at every level and 2-space indent, operating on the
// parsed node tree so comments and sequence order survive. This is fmt's
// canonical form; the content hash does NOT depend on it (it hashes the
// struct re-encode).
func canonicalizePreservingComments(data []byte) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	sortMappingKeys(&root)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func sortMappingKeys(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		type kv struct{ k, v *yaml.Node }
		pairs := make([]kv, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			pairs = append(pairs, kv{n.Content[i], n.Content[i+1]})
		}
		sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].k.Value < pairs[j].k.Value })
		for i, p := range pairs {
			n.Content[2*i], n.Content[2*i+1] = p.k, p.v
		}
	}
	for _, c := range n.Content {
		sortMappingKeys(c)
	}
}

// canonicalYAML renders a decoded model for the content hash: struct fields
// in declaration order, map keys sorted (yaml.v3), 2-space indent.
func canonicalYAML(v any) []byte {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("scenario: canonical encode of a validated model: %v", err))
	}
	if err := enc.Close(); err != nil {
		panic(fmt.Sprintf("scenario: canonical encode: %v", err))
	}
	return buf.Bytes()
}

// canonicalJSON re-emits parsed JSON compactly (Go's encoding/json sorts
// map keys and never emits whitespace), so the network part's hash is
// independent of the importer's formatting and of checkout EOL rewrites.
func canonicalJSON(raw []byte, name string) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Load already parsed the file as a NetFile; unreachable in
		// practice — but never panic on author input.
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("scenario: canonical JSON encode of %s: %v", name, err))
	}
	return out
}
