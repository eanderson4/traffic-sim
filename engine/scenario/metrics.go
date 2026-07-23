package scenario

// metrics.go — the metrics/*.yaml binding grammar (ADR-0014 §5 concretizing
// ADR-0012 §5): measurement sets binding element lanes to metric field
// groups over a time window, plus the trips switch. Parsing follows the
// demand-part discipline exactly: strict decoding, hand-written semantic
// checks against the effective network and the tick grid, canonical-YAML
// hashing. Tick conversion is ADR-0014 §3's pin: window seconds must be
// integral multiples of the tick length — non-multiples fail at load.

import (
	"errors"
	"fmt"
	"math"
	"os"

	"traffic-sim/engine"
)

// MetricsFile is one metrics/*.yaml part (format_version 1).
type MetricsFile struct {
	FormatVersion int           `yaml:"format_version"`
	Sets          []MetricsSet  `yaml:"sets"`
	Trips         *MetricsTrips `yaml:"trips,omitempty"`
}

// MetricsSet is one measurement set: a stable id, a flat list of element
// lane IDs, the metric groups to compute, and the window in sim seconds.
type MetricsSet struct {
	ID       string        `yaml:"id"`
	Elements []string      `yaml:"elements"`
	Metrics  []string      `yaml:"metrics"`
	Window   MetricsWindow `yaml:"window"`
}

// MetricsWindow is the set's measurement window in sim seconds. PeriodS is
// required; BeginS defaults to 0; EndS of 0 (or absent) means the run
// horizon.
type MetricsWindow struct {
	PeriodS float64 `yaml:"period_s"`
	BeginS  float64 `yaml:"begin_s,omitempty"`
	EndS    float64 `yaml:"end_s,omitempty"`
}

// MetricsTrips is the trips switch: the mapping present (possibly empty)
// enables per-vehicle trip records; absent disables them (ADR-0014 §5).
type MetricsTrips struct{}

// metricNames is the §5 vocabulary of selectable field groups.
var metricNames = map[string]bool{"edie": true, "occupancy": true, "stops": true, "time_loss": true}

// groups maps the set's metric names onto the kernel's group toggles.
func (ms *MetricsSet) groups() engine.MetricGroups {
	var g engine.MetricGroups
	for _, name := range ms.Metrics {
		switch name {
		case "edie":
			g.Edie = true
		case "occupancy":
			g.Occupancy = true
		case "stops":
			g.Stops = true
		case "time_loss":
			g.TimeLoss = true
		}
	}
	return g
}

// loadMetricsFile parses one metrics part from disk (see parseMetrics).
func loadMetricsFile(path string, lanes map[string]bool, dt float64) (*MetricsFile, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return parseMetrics(data, lanes, dt)
}

// parseMetrics is the byte-level core of loadMetricsFile — variant loading
// validates inherited base parts against the EFFECTIVE lanes and tick grid
// through the same validateMetrics, mirroring the demand pattern.
func parseMetrics(data []byte, lanes map[string]bool, dt float64) (*MetricsFile, []byte, error) {
	var mf MetricsFile
	if err := strictDecode(data, &mf); err != nil {
		return nil, nil, err
	}
	if err := validateMetrics(&mf, lanes, dt); err != nil {
		return nil, nil, err
	}
	return &mf, canonicalYAML(&mf), nil
}

// validateMetrics is the strict semantic fence for one metrics part:
// unknown fields and scalar coercions are already fenced by strictDecode;
// here: format version, non-empty everything, unique ids, known element
// lanes (the effective network's), the §5 metric vocabulary, finite window
// numbers, and the ADR-0014 §3 tick-multiple pin. lanes == nil skips the
// lane check (standalone use).
func validateMetrics(mf *MetricsFile, lanes map[string]bool, dt float64) error {
	if mf.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported format_version %d (loader supports %d)", mf.FormatVersion, FormatVersion)
	}
	if len(mf.Sets) == 0 {
		return errors.New("no sets")
	}
	setIDs := make(map[string]bool, len(mf.Sets))
	for i, ms := range mf.Sets {
		where := fmt.Sprintf("set %d (%s)", i, ms.ID)
		if ms.ID == "" {
			return fmt.Errorf("set %d: missing id", i)
		}
		if setIDs[ms.ID] {
			return fmt.Errorf("%s: duplicate set id", where)
		}
		setIDs[ms.ID] = true
		if len(ms.Elements) == 0 {
			return fmt.Errorf("%s: empty elements (name at least one element lane)", where)
		}
		seen := make(map[string]bool, len(ms.Elements))
		for _, el := range ms.Elements {
			if seen[el] {
				return fmt.Errorf("%s: duplicate element lane %q", where, el)
			}
			seen[el] = true
			if lanes != nil && !lanes[el] {
				return fmt.Errorf("%s: element %q is not a lane of the network", where, el)
			}
		}
		if len(ms.Metrics) == 0 {
			return fmt.Errorf("%s: empty metrics (name at least one of edie, occupancy, stops, time_loss)", where)
		}
		seenM := make(map[string]bool, len(ms.Metrics))
		for _, name := range ms.Metrics {
			if !metricNames[name] {
				return fmt.Errorf("%s: unknown metric %q (want edie|occupancy|stops|time_loss)", where, name)
			}
			if seenM[name] {
				return fmt.Errorf("%s: duplicate metric %q", where, name)
			}
			seenM[name] = true
		}
		w := ms.Window
		if err := finite(where+" window", w.PeriodS, w.BeginS, w.EndS); err != nil {
			return err
		}
		if w.PeriodS <= 0 {
			return fmt.Errorf("%s: window.period_s must be > 0", where)
		}
		if w.BeginS < 0 {
			return fmt.Errorf("%s: window.begin_s must be >= 0", where)
		}
		if w.EndS < 0 {
			return fmt.Errorf("%s: window.end_s must be >= 0 (0 or absent = run horizon)", where)
		}
		if w.EndS > 0 && w.EndS <= w.BeginS {
			return fmt.Errorf("%s: window.end_s %v not after begin_s %v", where, w.EndS, w.BeginS)
		}
		if _, err := ticksOf(where+" window.period_s", w.PeriodS, dt); err != nil {
			return err
		}
		if _, err := ticksOf(where+" window.begin_s", w.BeginS, dt); err != nil {
			return err
		}
		if w.EndS > 0 {
			if _, err := ticksOf(where+" window.end_s", w.EndS, dt); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateMetricSetIDs fences duplicate set ids ACROSS parts — set ids are
// stable within the scenario (ADR-0014 §5), and two parts declaring the
// same id would collide on the wire subjects.
func validateMetricSetIDs(files []*MetricsFile) error {
	seen := map[string]bool{}
	for _, mf := range files {
		for _, ms := range mf.Sets {
			if seen[ms.ID] {
				return fmt.Errorf("set id %q declared by more than one metrics part — set ids are scenario-stable (ADR-0014 §5)", ms.ID)
			}
			seen[ms.ID] = true
		}
	}
	return nil
}

// ticksOf converts a sim-seconds value to an integral tick count per
// ADR-0014 §3: the value must be an integral multiple of dt — non-multiples
// fail loud at load, because interval boundaries must land exactly on the
// tick grid. The comparison rounds first: float division of nice decimals
// lands within 1e-9 of the integer (0.3/0.1 = 2.9999999999999996), and a
// genuine non-multiple (0.25/0.1 = 2.5) is never that close.
func ticksOf(name string, seconds, dt float64) (uint64, error) {
	f := seconds / dt
	n := math.Round(f)
	if math.Abs(f-n) > 1e-9*math.Max(1, math.Abs(n)) {
		return 0, fmt.Errorf("%s: %v s is not an integral multiple of the tick length %v s (ADR-0014 §3)", name, seconds, dt)
	}
	return uint64(n), nil
}

// tickSeconds is the scenario's effective tick length: the manifest's
// params.dt, else the engine default (mirrors RunSpec).
func (s *Scenario) tickSeconds() float64 {
	if s.Manifest.Params.Dt != 0 {
		return s.Manifest.Params.Dt
	}
	return engine.DefaultParams().Dt
}

// MetricConfig assembles the scenario's measurement plan (ADR-0014 §5): the
// parsed metrics parts mapped onto the kernel's tick-based config, or the
// zero-authoring default (every lane, all four groups at 900 s, trips on)
// when the manifest declares no metrics parts. Load already validated
// everything, so the mapping is total.
//
// Tick mapping (§3): a window boundary at x seconds maps to tick x/dt. The
// kernel accumulates observation ticks (BeginTick, LastTick] — tick T
// covers sim time (T−1)·dt … T·dt — so the seconds window [begin_s, end_s)
// covers exactly the observation ticks whose covered instant T·dt falls in
// (begin_s, end_s]: BeginTick = begin_s/dt, LastTick = end_s/dt (inclusive;
// 0 = horizon). Trip records are enabled when ANY metrics part carries the
// trips mapping.
func (s *Scenario) MetricConfig(net *engine.Network) engine.KernelConfig {
	if len(s.Metrics) == 0 {
		return engine.DefaultKernelConfig(net, s.tickSeconds())
	}
	dt := s.tickSeconds()
	cfg := engine.KernelConfig{}
	for _, mf := range s.Metrics {
		if mf.Trips != nil {
			cfg.Trips = true
		}
		for _, ms := range mf.Sets {
			set := engine.MetricSetConfig{
				ID:          ms.ID,
				LaneIDs:     append([]string{}, ms.Elements...),
				Groups:      ms.groups(),
				BeginTick:   uint64(math.Round(ms.Window.BeginS / dt)),
				PeriodTicks: uint64(math.Round(ms.Window.PeriodS / dt)),
			}
			if ms.Window.EndS > 0 {
				set.LastTick = uint64(math.Round(ms.Window.EndS / dt))
			}
			cfg.Sets = append(cfg.Sets, set)
		}
	}
	return cfg
}
