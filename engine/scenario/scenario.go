// Package scenario loads ADR-0012 scenario directories: a strict-YAML
// manifest (scenario.yaml) referencing network, demand, control, and metrics
// part files, materialized into an engine.RunSpec plus director demand
// definitions. It also provides the canonical formatter and the content
// hash — the run key is (content-hash, seed).
//
// Dependency note (AGENTS.md "standard library first"): gopkg.in/yaml.v3 is
// the ADR-0012 §2 approved exception, confined to this package exactly like
// the NATS modules are confined to engine/natsio (ADR-0006). The engine
// kernel consumes only the loaded model and stays stdlib-only.
//
// Validation note (ADR-0012 §2 implementation refinement): the "JSON-Schema
// validation at load" is realized as strict decoding (KnownFields — unknown
// fields are hard errors) plus semantic checks, with no JSON-Schema library
// dependency; the canonical formatter doubles as the schema's human face.
package scenario

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// flag-era uniform demand made declarative. Absent = director-driven demand
// only (rate 0 disables the spawner, exactly like -rate 0).
type Spawner struct {
	RatePerLaneHour float64 `yaml:"rate_per_lane_h"`
	DensityPerKm    float64 `yaml:"density_per_km,omitempty"`
}

// DemandFile is one demand/*.yaml part: the director flow definitions the
// M10 runtime demand director samples (ADR-0012 §3 — a demand file IS a
// director configuration). Times are SIM SECONDS (never wall clock,
// ADR-0005). The M10 strict-JSON flow files parse unchanged (JSON is a YAML
// subset).
type DemandFile struct {
	FormatVersion int    `yaml:"format_version"`
	Flows         []Flow `yaml:"flows"`
}

// Flow is one origin's demand program. With Slices, the rate is
// piecewise-constant (0 outside any slice); without, VehPerH holds for the
// whole run.
type Flow struct {
	Origin  string             `yaml:"origin"`
	VehPerH float64            `yaml:"veh_per_h,omitempty"`
	Spacing string             `yaml:"spacing"`
	VTypes  map[string]float64 `yaml:"vtypes,omitempty"`
	Slices  []Slice            `yaml:"slices,omitempty"`
	UntilS  float64            `yaml:"until_s,omitempty"`
}

// Slice is one piecewise-constant demand window [StartS, EndS) in sim
// seconds.
type Slice struct {
	StartS  float64 `yaml:"start_s"`
	EndS    float64 `yaml:"end_s"`
	VehPerH float64 `yaml:"veh_per_h"`
}

// Scenario is a loaded, validated scenario directory.
type Scenario struct {
	Dir      string
	Manifest Manifest
	Demands  []*DemandFile // aligned with Manifest.Demand
	NetPath  string        // resolved network path (absolute)
	origins  map[string]bool
	parts    []hashPart // everything the content hash covers
}

// hashPart is one hashed input: rel is the scenario-relative path; data is
// canonical YAML for the manifest and demand parts (post-fmt, so the hash
// is formatting-independent), raw file bytes for network/control/metrics.
type hashPart struct {
	rel  string
	data []byte
}

// Load reads, validates, and materializes a scenario directory (ADR-0012:
// fail-loud everywhere — unknown fields, bad references, and semantic
// errors are all load-time errors).
func Load(dir string) (*Scenario, error) {
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
	s.parts = append(s.parts, hashPart{rel: ManifestFile, data: canonicalYAML(&m)})

	// Network: raw bytes ride into the hash (the netimport JSON is the
	// importer's artifact, not ours to reformat); the compile validates the
	// file itself and supplies the origin set for demand validation.
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
	s.origins = make(map[string]bool, len(net.Origins))
	for _, l := range net.Origins {
		s.origins[l.ID] = true
	}
	s.parts = append(s.parts, hashPart{rel: m.Network, data: rawNet})

	typeSet := make(map[string]bool, len(m.Types))
	for _, t := range m.Types {
		typeSet[t] = true
	}
	for _, ref := range m.Demand {
		p, err := resolvePart(dir, ref)
		if err != nil {
			return nil, fmt.Errorf("scenario demand: %w", err)
		}
		df, canonical, err := loadDemandFile(p, s.origins, typeSet)
		if err != nil {
			return nil, fmt.Errorf("scenario demand %s: %w", ref, err)
		}
		s.Demands = append(s.Demands, df)
		s.parts = append(s.parts, hashPart{rel: ref, data: canonical})
	}
	// Control and metrics parts are v1 pass-through: existence-checked and
	// hashed (ADR-0012 §5 — the binding grammar lands with the
	// observability ADR; a variant that forgets one is already a broken
	// diff because the hash moves).
	for _, ref := range append(append([]string{}, m.Control...), m.Metrics...) {
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
	if m.Spawner == nil && len(m.Demand) == 0 {
		return nil, fmt.Errorf("scenario %s: no demand — set spawner: or demand: (an empty run is not a scenario)", ManifestFile)
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

func loadDemandFile(path string, origins, typeSet map[string]bool) (*DemandFile, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var df DemandFile
	if err := strictDecode(data, &df); err != nil {
		return nil, nil, err
	}
	if err := validateDemand(&df, origins, typeSet); err != nil {
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

// Hash is the scenario's content identity (ADR-0012 §6): sha256 over the
// canonical manifest and demand bytes plus the raw network/control/metrics
// bytes, each framed by its scenario-relative path. Formatting-independent
// for the YAML parts; stable across copies, renames, and checkouts.
func (s *Scenario) Hash() string {
	parts := append([]hashPart{}, s.parts...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].rel < parts[j].rel })
	h := sha256.New()
	for _, p := range parts {
		io.WriteString(h, p.rel)
		h.Write([]byte{0})
		h.Write(p.data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Format rewrites the manifest and demand parts in canonical form
// (ADR-0012 §2: diffs stay semantic, the hash stays stable). The scenario
// must validate first — fmt never canonicalizes a broken scenario. Network,
// control, and metrics parts are untouched (not our formats).
func Format(dir string) error {
	s, err := Load(dir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), canonicalYAML(&s.Manifest), 0o644); err != nil {
		return err
	}
	for i, ref := range s.Manifest.Demand {
		p, _ := resolvePart(dir, ref) // Load already validated
		if err := os.WriteFile(p, canonicalYAML(s.Demands[i]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// resolvePart joins a scenario-relative part reference to the directory,
// rejecting absolute paths and parent escapes (parts live INSIDE the
// scenario directory — the directory is the unit, ADR-0012 §1).
func resolvePart(dir, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("empty part reference")
	}
	if filepath.IsAbs(ref) {
		return "", fmt.Errorf("part %q: absolute paths are not allowed", ref)
	}
	clean := filepath.Clean(ref)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("part %q: escapes the scenario directory", ref)
	}
	return filepath.Join(dir, clean), nil
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
	if m.Params.Dt < 0 {
		return errors.New("params.dt must be > 0")
	}
	if len(m.Types) == 0 {
		m.Types = []string{"car"}
	}
	for _, t := range m.Types {
		if t = strings.TrimSpace(t); t == "" {
			return errors.New("types: empty type name")
		}
	}
	if m.Spawner != nil {
		if m.Spawner.RatePerLaneHour < 0 {
			return errors.New("spawner.rate_per_lane_h must be >= 0 (0 = spawner disabled, director-driven)")
		}
		if m.Spawner.DensityPerKm < 0 {
			return errors.New("spawner.density_per_km must be >= 0 (0 = uncapped)")
		}
	}
	return nil
}

func validateDemand(df *DemandFile, origins, typeSet map[string]bool) error {
	if df.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d (loader supports %d)", df.FormatVersion, FormatVersion)
	}
	if len(df.Flows) == 0 {
		return errors.New("no flows")
	}
	for i, f := range df.Flows {
		where := fmt.Sprintf("flow %d (%s)", i, f.Origin)
		if f.Origin == "" {
			return fmt.Errorf("flow %d: missing origin", i)
		}
		if origins != nil && !origins[f.Origin] {
			return fmt.Errorf("%s: not a spawn origin lane of the network", where)
		}
		if f.Spacing != "constant" && f.Spacing != "poisson" {
			return fmt.Errorf("%s: spacing %q (want constant|poisson)", where, f.Spacing)
		}
		if len(f.Slices) == 0 && f.VehPerH <= 0 {
			return fmt.Errorf("%s: veh_per_h must be > 0 without slices", where)
		}
		prevEnd := 0.0
		for j, sl := range f.Slices {
			if sl.StartS < 0 || sl.EndS <= sl.StartS {
				return fmt.Errorf("%s slice %d: need 0 <= start_s < end_s", where, j)
			}
			if j > 0 && sl.StartS < prevEnd {
				return fmt.Errorf("%s slice %d: overlaps the previous slice (slices are sorted, non-overlapping windows)", where, j)
			}
			if sl.VehPerH < 0 {
				return fmt.Errorf("%s slice %d: veh_per_h must be >= 0", where, j)
			}
			prevEnd = sl.EndS
		}
		if f.UntilS < 0 {
			return fmt.Errorf("%s: until_s must be >= 0", where)
		}
		for name, w := range f.VTypes {
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

// strictDecode parses the ADR-0012 strict-YAML subset into v: exactly one
// document, no anchors/aliases/merge keys/custom tags, no unknown fields.
// (Duplicate mapping keys are already a yaml.v3 parse error.)
func strictDecode(data []byte, v any) error {
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("one document per file (strict YAML)")
	}
	if err := rejectNonStrict(&root); err != nil {
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
// invisible typing.
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
	switch n.Tag {
	case "!!str", "!!int", "!!float", "!!bool", "!!null", "!!map", "!!seq":
	default:
		return fmt.Errorf("line %d: tag %s is not strict YAML (custom tags and merge keys are forbidden)", n.Line, n.Tag)
	}
	if n.Anchor != "" {
		return fmt.Errorf("line %d: anchors are not strict YAML", n.Line)
	}
	for _, c := range n.Content {
		if err := rejectNonStrict(c); err != nil {
			return err
		}
	}
	return nil
}

// canonicalYAML renders a decoded model in the canonical form: struct
// fields in declaration order, map keys sorted (yaml.v3), 2-space indent.
// Formatting-independent content hashing and semantic diffs both build on
// this.
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
