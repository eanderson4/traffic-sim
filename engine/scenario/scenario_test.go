package scenario

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// writeNet emits a minimal valid network-format v1 file: one origin lane
// feeding one exit lane.
func writeNet(t *testing.T, dir string) string {
	t.Helper()
	nf := engine.NetFile{
		Version: 1,
		Name:    "test",
		Lanes: []engine.NetLane{
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"b_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {1000, 0}}, Exit: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goodManifest = `format_version: 1
id: test-baseline
seed: 7
ticks: 3000
network: network.json
types: [car, truck]
spawner:
  rate_per_lane_h: 600
  density_per_km: 80
demand:
  - demand/main.yaml
`

const goodDemand = `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 900
    spacing: poisson
    vtypes:
      car: 0.9
      truck: 0.1
`

func goodScenario(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeNet(t, dir)
	writeFile(t, dir, ManifestFile, goodManifest)
	writeFile(t, dir, "demand/main.yaml", goodDemand)
	return dir
}

func TestLoadRunSpec(t *testing.T) {
	dir := goodScenario(t)
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Manifest.ID != "test-baseline" || len(s.Demands) != 1 {
		t.Fatalf("manifest: %+v demands %d", s.Manifest, len(s.Demands))
	}
	reg := map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}
	spec, err := s.RunSpec(reg)
	if err != nil {
		t.Fatalf("RunSpec: %v", err)
	}
	if spec.Seed != 7 || spec.Ticks != 3000 {
		t.Errorf("seed/ticks = %d/%d", spec.Seed, spec.Ticks)
	}
	if spec.Scen.SpawnRatePerLaneHour != 600 || spec.Scen.DensityTargetPerKm != 80 {
		t.Errorf("spawner = %+v", spec.Scen)
	}
	if len(spec.Scen.Types) != 2 || spec.Hash == "" {
		t.Errorf("types = %d, hash = %q", len(spec.Scen.Types), spec.Hash)
	}
	if spec.Net.Kind != "file" || spec.Net.Path == "" {
		t.Errorf("net = %+v", spec.Net)
	}
	if _, err := engine.NewEngine(spec); err != nil {
		t.Fatalf("materialized spec does not build an engine: %v", err)
	}
}

func TestHashStableAcrossFormatting(t *testing.T) {
	dir := goodScenario(t)
	s1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1 := s1.Hash()

	// Reorder keys, add comments and odd spacing — the hash must not move.
	writeFile(t, dir, ManifestFile, `# reordered, commented
ticks: 3000
seed: 7
id: test-baseline
format_version: 1
demand:
  - demand/main.yaml
spawner:
  density_per_km: 80
  rate_per_lane_h: 600
types: [car, truck]
network: network.json
`)
	writeFile(t, dir, "demand/main.yaml", "format_version: 1\nflows:\n  - {origin: a_0, veh_per_h: 900, spacing: poisson, vtypes: {truck: 0.1, car: 0.9}}\n")
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != s2.Hash() {
		t.Errorf("hash moved under reformatting: %s → %s", h1, s2.Hash())
	}

	// A semantic change must move it.
	writeFile(t, dir, "demand/main.yaml", strings.Replace(goodDemand, "900", "901", 1))
	s3, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == s3.Hash() {
		t.Error("hash unchanged after a demand rate change")
	}
}

func TestHashExcludesRunCoordinates(t *testing.T) {
	dir := goodScenario(t)
	s1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1 := s1.Hash()

	// Seed and ticks are run coordinates, not content: changing them (in
	// the file OR by sweep override) must not move the content hash.
	writeFile(t, dir, ManifestFile, strings.NewReplacer("seed: 7", "seed: 99", "ticks: 3000", "ticks: 72000").Replace(goodManifest))
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != s2.Hash() {
		t.Errorf("hash moved with seed/ticks: %s → %s", h1, s2.Hash())
	}
	if s2.Manifest.Seed != 99 || s2.Manifest.Ticks != 72000 {
		t.Errorf("manifest values not loaded: %+v", s2.Manifest)
	}
}

// The golden vector is the canary for the hash protocol
// (traffic-sim/scenario-hash/v1): if this breaks after a dependency or Go
// upgrade, canonicalization changed — a format event, never silent
// (ADR-0012 §8 addendum).
func TestGoldenHash(t *testing.T) {
	dir := goodScenario(t)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	const want = "a09f7f6848ac6dd273bebf24081d4375beedf8f46be2b6e1e89e885fe11ebff2"
	if s.Hash() != want {
		t.Fatalf("hash = %s, want golden %s", s.Hash(), want)
	}
}

func TestFormatPreservesComments(t *testing.T) {
	dir := goodScenario(t)
	writeFile(t, dir, "demand/main.yaml", `# I-280 AM peak, metered approach.
# Rates from the 2026-07 count study.
format_version: 1
flows:
  - origin: a_0  # the only origin
    veh_per_h: 900
    spacing: poisson
    vtypes:
      truck: 0.1
      car: 0.9
`)
	before, _ := Load(dir)
	if err := Format(dir); err != nil {
		t.Fatalf("Format: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "demand/main.yaml"))
	for _, want := range []string{"# I-280 AM peak", "# Rates from", "# the only origin"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("fmt lost comment %q:\n%s", want, data)
		}
	}
	// Keys are canonically sorted (vtypes: car before truck).
	ci, ti := strings.Index(string(data), "car: 0.9"), strings.Index(string(data), "truck: 0.1")
	if ci < 0 || ti < 0 || ci > ti {
		t.Errorf("vtypes not sorted in fmt output:\n%s", data)
	}
	after, _ := Load(dir)
	if before.Hash() != after.Hash() {
		t.Error("hash moved across comment-preserving fmt")
	}
}

func TestScalarCoercionRejected(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"float into int", strings.Replace(goodManifest, "ticks: 3000", "ticks: 1.9", 1), "wants !!int"},
		{"bool into string", strings.Replace(goodManifest, "id: test-baseline", "id: true", 1), "wants !!str"},
		{"string into uint", strings.Replace(goodManifest, "seed: 7", "seed: lots", 1), "wants !!int"},
		{"nan dt", strings.Replace(goodManifest, "ticks: 3000", "ticks: 3000\nparams: {dt: .nan}", 1), "not a finite number"},
		{"dt too large", strings.Replace(goodManifest, "ticks: 3000", "ticks: 3000\nparams: {dt: 2.5}", 1), "params.dt"},
		{"duplicate types", strings.Replace(goodManifest, "types: [car, truck]", "types: [car, car]", 1), "duplicate"},
		{"whitespace type", strings.Replace(goodManifest, "types: [car, truck]", "types: [' car', truck]", 1), "whitespace"},
		{"zero-rate spawner only", "format_version: 1\nid: x\nseed: 1\nticks: 10\nnetwork: network.json\nspawner: {rate_per_lane_h: 0}\n", "no demand"},
		{"backslash ref", strings.Replace(goodManifest, "demand/main.yaml", `demand\main.yaml`, 1), "forward slashes"},
		{"unclean ref", strings.Replace(goodManifest, "demand/main.yaml", "demand/../demand/main.yaml", 1), "clean relative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNet(t, dir)
			writeFile(t, dir, ManifestFile, tc.manifest)
			writeFile(t, dir, "demand/main.yaml", goodDemand)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestDemandCoercionRejected(t *testing.T) {
	cases := []struct {
		name   string
		demand string
		want   string
	}{
		{"nan rate", strings.Replace(goodDemand, "veh_per_h: 900", "veh_per_h: .nan", 1), "not a finite number"},
		{"inf weight", strings.Replace(goodDemand, "car: 0.9", "car: .inf", 1), "not a finite number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNet(t, dir)
			writeFile(t, dir, ManifestFile, goodManifest)
			writeFile(t, dir, "demand/main.yaml", tc.demand)
			if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
	// veh_per_h alongside slices is dead config — fail loud.
	dir := t.TempDir()
	writeNet(t, dir)
	writeFile(t, dir, ManifestFile, goodManifest)
	writeFile(t, dir, "demand/main.yaml", `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 600
    spacing: constant
    slices:
      - {start_s: 0, end_s: 100, veh_per_h: 600}
`)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "dead config") {
		t.Fatalf("veh_per_h+slices: err = %v", err)
	}
	// A flow with no vtypes where "car" is not a scenario type.
	writeFile(t, dir, ManifestFile, strings.Replace(goodManifest, "types: [car, truck]", "types: [truck]", 1))
	writeFile(t, dir, "demand/main.yaml", `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 600
    spacing: constant
`)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "default \"car\"") {
		t.Fatalf("implicit car: err = %v", err)
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	writeNet(t, dir)
	writeFile(t, dir, ManifestFile, goodManifest)
	writeFile(t, dir, "demand/main.yaml", goodDemand)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeFile(t, filepath.Dir(outside), filepath.Base(outside), goodDemand)
	link := filepath.Join(dir, "demand", "link.yaml")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeFile(t, dir, ManifestFile, strings.Replace(goodManifest, "demand/main.yaml", "demand/link.yaml", 1))
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "symlink escapes") {
		t.Fatalf("err = %v, want symlink escape rejection", err)
	}
}

func TestFormatIdempotent(t *testing.T) {
	dir := goodScenario(t)
	if err := Format(dir); err != nil {
		t.Fatalf("Format: %v", err)
	}
	s1, _ := Load(dir)
	b1, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	d1, _ := os.ReadFile(filepath.Join(dir, "demand/main.yaml"))
	if err := Format(dir); err != nil {
		t.Fatalf("Format again: %v", err)
	}
	s2, _ := Load(dir)
	b2, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	d2, _ := os.ReadFile(filepath.Join(dir, "demand/main.yaml"))
	if string(b1) != string(b2) || string(d1) != string(d2) {
		t.Error("fmt is not idempotent")
	}
	if s1.Hash() != s2.Hash() {
		t.Error("hash moved across fmt")
	}
}

func TestStrictYAMLFence(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
	}{
		{"unknown field", strings.Replace(goodManifest, "seed: 7", "seeds: 7", 1), "field seeds not found"},
		{"anchor+alias", "format_version: 1\nid: &x test\nseed: 7\nticks: 3000\nnetwork: network.json\nspawner: {rate_per_lane_h: 600}\nmetrics: [*x]\n", "not strict YAML"},
		{"custom tag", "format_version: 1\nid: !foo test\nseed: 7\nticks: 3000\nnetwork: network.json\nspawner: {rate_per_lane_h: 600}\n", "not strict YAML"},
		{"two documents", goodManifest + "---\nformat_version: 1\n", "one document"},
		{"duplicate part refs", strings.Replace(goodManifest, "demand:\n  - demand/main.yaml\n", "demand:\n  - demand/main.yaml\n  - demand/main.yaml\n", 1), "duplicate part reference"},
		{"demand ref names the manifest", strings.Replace(goodManifest, "demand/main.yaml", ManifestFile, 1), "names a manifest file"},
		{"control ref shadows the network", strings.Replace(goodManifest, "demand:\n  - demand/main.yaml\n", "demand:\n  - demand/main.yaml\ncontrol: [network.json]\n", 1), "duplicate part reference"},
		{"newer version", strings.Replace(goodManifest, "format_version: 1", "format_version: 2", 1), "unsupported format_version 2"},
		{"no demand", "format_version: 1\nid: x\nseed: 1\nticks: 10\nnetwork: network.json\n", "no demand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNet(t, dir)
			writeFile(t, dir, ManifestFile, tc.manifest)
			writeFile(t, dir, "demand/main.yaml", goodDemand)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestDemandValidation(t *testing.T) {
	cases := []struct {
		name   string
		demand string
		want   string
	}{
		{"bad spacing", strings.Replace(goodDemand, "poisson", "bernoulli", 1), "spacing"},
		{"unknown origin", strings.Replace(goodDemand, "a_0", "nope", 1), "not a spawn origin"},
		{"unknown vtype", strings.Replace(goodDemand, "truck", "hovercraft", 1), "not in the scenario type list"},
		{"zero weight", strings.Replace(goodDemand, "0.1", "0", 1), "weight must be > 0"},
		{"no rate", "format_version: 1\nflows:\n  - {origin: a_0, spacing: constant}\n", "veh_per_h"},
		{"overlapping slices", `format_version: 1
flows:
  - origin: a_0
    spacing: constant
    slices:
      - {start_s: 0, end_s: 100, veh_per_h: 600}
      - {start_s: 50, end_s: 200, veh_per_h: 300}
`, "overlaps"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeNet(t, dir)
			writeFile(t, dir, ManifestFile, goodManifest)
			writeFile(t, dir, "demand/main.yaml", tc.demand)
			_, err := Load(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestPathEscapesRejected(t *testing.T) {
	for _, ref := range []string{"../outside.json", "/abs/path.json"} {
		dir := t.TempDir()
		writeNet(t, dir)
		writeFile(t, dir, ManifestFile, strings.Replace(goodManifest, "network.json", ref, 1))
		writeFile(t, dir, "demand/main.yaml", goodDemand)
		if _, err := Load(dir); err == nil {
			t.Errorf("network ref %q accepted", ref)
		}
	}
}

// The M10 strict-JSON demand files parse unchanged through the YAML loader
// (JSON is a YAML subset) — the demand-director's migration path.
func TestJSONDemandStillParses(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "flows.json")
	writeFile(t, dir, "flows.json", `{"format_version": 1, "flows": [{"origin": "a_0", "veh_per_h": 600, "spacing": "poisson", "vtypes": {"car": 1.0}}]}`)
	df, err := LoadDemandFile(p)
	if err != nil {
		t.Fatalf("LoadDemandFile(JSON): %v", err)
	}
	if len(df.Flows) != 1 || df.Flows[0].VehPerH != 600 {
		t.Fatalf("flows = %+v", df.Flows)
	}
}

// Director-driven scenario: no spawner block — the built-in spawner stays
// disabled (rate 0), demand arrives as verbs.
func TestDirectorDrivenScenario(t *testing.T) {
	dir := t.TempDir()
	writeNet(t, dir)
	writeFile(t, dir, ManifestFile, `format_version: 1
id: director-run
seed: 3
ticks: 1000
network: network.json
demand: [demand/main.yaml]
`)
	writeFile(t, dir, "demand/main.yaml", `format_version: 1
flows:
  - origin: a_0
    veh_per_h: 900
    spacing: poisson
`)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.RunSpec(map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Scen.SpawnRatePerLaneHour != 0 {
		t.Errorf("spawner rate = %v, want 0 (director-driven)", spec.Scen.SpawnRatePerLaneHour)
	}
	if len(spec.Scen.Types) != 1 { // default type list
		t.Errorf("default types = %d, want 1", len(spec.Scen.Types))
	}
}
