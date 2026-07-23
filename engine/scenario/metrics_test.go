package scenario

import (
	"reflect"
	"strings"
	"testing"

	"traffic-sim/engine"
)

const goodMetrics = `format_version: 1
sets:
  - id: mainline
    elements: [a_0, b_0]
    metrics: [edie, occupancy, stops, time_loss]
    window: {period_s: 900, begin_s: 0, end_s: 1800}
  - id: entry
    elements: [a_0]
    metrics: [stops]
    window: {period_s: 60, begin_s: 300}
trips: {}
`

// metricsScenario is goodScenario plus a metrics part referenced by the
// manifest.
func metricsScenario(t *testing.T) string {
	t.Helper()
	dir := goodScenario(t)
	writeFile(t, dir, "metrics/main.yaml", goodMetrics)
	writeFile(t, dir, ManifestFile, goodManifest+"metrics:\n  - metrics/main.yaml\n")
	return dir
}

func testTypeReg() map[string]*engine.VehicleType {
	return map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}
}

func metricTestEngine(t *testing.T, s *Scenario) *engine.Engine {
	t.Helper()
	spec, err := s.RunSpec(testTypeReg())
	if err != nil {
		t.Fatalf("RunSpec: %v", err)
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

// The full grammar maps onto the kernel config: ids, element lanes, group
// toggles, the seconds→ticks conversion (ADR-0014 §3), and the trips
// switch — and the kernel accepts the result.
func TestMetricConfigMultiSet(t *testing.T) {
	s, err := Load(metricsScenario(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e := metricTestEngine(t, s)
	cfg := s.MetricConfig(e.Net)
	if !cfg.Trips {
		t.Error("trips: {} present but Trips is off")
	}
	if len(cfg.Sets) != 2 {
		t.Fatalf("got %d sets, want 2", len(cfg.Sets))
	}
	s0 := cfg.Sets[0]
	if s0.ID != "mainline" || !reflect.DeepEqual(s0.LaneIDs, []string{"a_0", "b_0"}) {
		t.Errorf("set 0 = %q %v", s0.ID, s0.LaneIDs)
	}
	if s0.Groups != (engine.MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true}) {
		t.Errorf("set 0 groups = %+v, want all four", s0.Groups)
	}
	// dt = 0.1: begin 0 s → 0, period 900 s → 9000, end 1800 s → LastTick 18000.
	if s0.BeginTick != 0 || s0.PeriodTicks != 9000 || s0.LastTick != 18000 {
		t.Errorf("set 0 window = begin %d period %d last %d, want 0/9000/18000", s0.BeginTick, s0.PeriodTicks, s0.LastTick)
	}
	s1 := cfg.Sets[1]
	if s1.ID != "entry" || s1.Groups != (engine.MetricGroups{Stops: true}) {
		t.Errorf("set 1 = %q %+v", s1.ID, s1.Groups)
	}
	// begin 300 s → 3000, period 60 s → 600, no end_s → LastTick 0 (horizon).
	if s1.BeginTick != 3000 || s1.PeriodTicks != 600 || s1.LastTick != 0 {
		t.Errorf("set 1 window = begin %d period %d last %d, want 3000/600/0", s1.BeginTick, s1.PeriodTicks, s1.LastTick)
	}
	if _, err := engine.NewKernel(e, cfg); err != nil {
		t.Fatalf("kernel rejects the scenario-derived config: %v", err)
	}
}

// No metrics parts: the zero-authoring default (ADR-0014 §5).
func TestMetricConfigDefault(t *testing.T) {
	s, err := Load(goodScenario(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e := metricTestEngine(t, s)
	got := s.MetricConfig(e.Net)
	want := engine.DefaultKernelConfig(e.Net, e.Params.Dt)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MetricConfig = %+v, want the default %+v", got, want)
	}
}

// The strict fence: every malformed metrics part fails loud at load.
func TestMetricsStrictFence(t *testing.T) {
	set := func(body string) string { return "format_version: 1\nsets:\n  - " + body + "\n" }
	cases := []struct {
		name    string
		content string
		want    string // "" means the part must LOAD
	}{
		{"unknown lane", set(`{id: x, elements: [a_0, z_9], metrics: [edie], window: {period_s: 900}}`), "not a lane of the network"},
		{"unknown metric", set(`{id: x, elements: [a_0], metrics: [los], window: {period_s: 900}}`), "unknown metric"},
		{"duplicate set id", "format_version: 1\nsets:\n  - {id: x, elements: [a_0], metrics: [edie], window: {period_s: 900}}\n  - {id: x, elements: [b_0], metrics: [edie], window: {period_s: 900}}\n", "duplicate set id"},
		{"duplicate lane", set(`{id: x, elements: [a_0, a_0], metrics: [edie], window: {period_s: 900}}`), "duplicate element lane"},
		{"non-multiple period", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 0.25}}`), "not an integral multiple"},
		{"non-multiple begin", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 900, begin_s: 0.25}}`), "not an integral multiple"},
		{"non-multiple end", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 900, end_s: 1000.25}}`), "not an integral multiple"},
		{"empty elements", set(`{id: x, elements: [], metrics: [edie], window: {period_s: 900}}`), "empty elements"},
		{"empty metrics", set(`{id: x, elements: [a_0], metrics: [], window: {period_s: 900}}`), "empty metrics"},
		{"duplicate metric", set(`{id: x, elements: [a_0], metrics: [edie, edie], window: {period_s: 900}}`), "duplicate metric"},
		{"unknown field", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 900, bogus: 1}}`), "field bogus not found"},
		{"float into int", "format_version: 1.5\nsets:\n  - {id: x, elements: [a_0], metrics: [edie], window: {period_s: 900}}\n", "wants !!int"},
		{"end not after begin", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 900, begin_s: 900, end_s: 100}}`), "not after"},
		{"no sets", "format_version: 1\nsets: []\n", "no sets"},
		{"missing id", set(`{elements: [a_0], metrics: [edie], window: {period_s: 900}}`), "missing id"},
		{"zero period", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 0}}`), "period_s must be > 0"},
		{"nan period", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: .nan}}`), "not a finite number"},
		// Positive: 0.3 s is 3 ticks at dt 0.1 — float division noise
		// (2.9999999999999996) must not trip the multiple check.
		{"sub-second multiple", set(`{id: x, elements: [a_0], metrics: [edie], window: {period_s: 0.3}}`), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := goodScenario(t)
			writeFile(t, dir, "metrics/main.yaml", c.content)
			writeFile(t, dir, ManifestFile, goodManifest+"metrics:\n  - metrics/main.yaml\n")
			_, err := Load(dir)
			if c.want == "" {
				if err != nil {
					t.Fatalf("valid part rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load error = %v, want substring %q", err, c.want)
			}
		})
	}
}

// Set ids are scenario-stable: two parts declaring the same id collide.
func TestMetricsDuplicateSetIDAcrossParts(t *testing.T) {
	dir := goodScenario(t)
	part := "format_version: 1\nsets:\n  - {id: x, elements: [a_0], metrics: [edie], window: {period_s: 900}}\n"
	writeFile(t, dir, "metrics/a.yaml", part)
	writeFile(t, dir, "metrics/b.yaml", part)
	writeFile(t, dir, ManifestFile, goodManifest+"metrics:\n  - metrics/a.yaml\n  - metrics/b.yaml\n")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "more than one metrics part") {
		t.Fatalf("Load error = %v, want cross-part duplicate set id rejection", err)
	}
}

// Metrics parts are typed hash content: reformatting never moves the hash,
// a semantic change always does (ADR-0012 §5/§6).
func TestMetricsHashCanonical(t *testing.T) {
	dir := metricsScenario(t)
	s1, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1 := s1.Hash()

	// Reordered keys, comments, odd spacing — the same model.
	writeFile(t, dir, "metrics/main.yaml", `# measurement plan
trips: {}
sets:
  - window:
      begin_s: 0
      end_s: 1800
      period_s: 900
    metrics: [edie, occupancy, stops, time_loss]  # all four
    elements: [a_0, b_0]
    id: mainline
  - window: {period_s: 60, begin_s: 300}
    metrics: [stops]
    elements: [a_0]
    id: entry
format_version: 1
`)
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.Hash() != h1 {
		t.Errorf("hash moved under reformatting: %s → %s", h1, s2.Hash())
	}

	writeFile(t, dir, "metrics/main.yaml", strings.Replace(goodMetrics, "period_s: 900", "period_s: 901", 1))
	s3, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s3.Hash() == h1 {
		t.Error("hash unchanged after a window change — measurement is identity (ADR-0012 §5)")
	}
}

// A variant that adds a metrics part loads, yields the parsed config, and
// is a different scenario than its base (the hash covers measurement).
func TestVariantAddsMetricsPart(t *testing.T) {
	baseDir, variantDir := variantPair(t, `format_version: 1
id: with-metrics
base: ../../base
metrics:
  - metrics/added.yaml
`)
	writeFile(t, variantDir, "metrics/added.yaml", `format_version: 1
sets:
  - id: added
    elements: [a_0]
    metrics: [edie]
    window: {period_s: 30}
trips: {}
`)
	v, err := Load(variantDir)
	if err != nil {
		t.Fatalf("Load variant: %v", err)
	}
	e := metricTestEngine(t, v)
	cfg := v.MetricConfig(e.Net)
	if len(cfg.Sets) != 1 || cfg.Sets[0].ID != "added" {
		t.Fatalf("variant config sets = %+v", cfg.Sets)
	}
	if cfg.Sets[0].PeriodTicks != 300 || !cfg.Sets[0].Groups.Edie || cfg.Sets[0].Groups.Stops {
		t.Errorf("variant set = %+v, want edie-only at 300 ticks", cfg.Sets[0])
	}
	if !cfg.Trips {
		t.Error("variant trips: {} present but Trips is off")
	}
	base, err := Load(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	if v.Hash() == base.Hash() {
		t.Error("adding a metrics part did not move the content hash")
	}
}
