package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signal_test.go — kernel enforcement of fixed-time signal programs
// (ADR-0011): phase advancement on exact tick boundaries, red/amber/green
// gating at the stop line, the off/blinking fallback to the priority model,
// green still honoring the box checks, keyframe round-trip, and determinism.
// Fixtures are compiled NetFiles written to temp files, so the loader
// extension is exercised end to end.

// sigNetFile is a signalized junction J: approach nA_0 feeds the internal
// lane iJ_0 (link 0 of program "J") draining to exit nX_0.
func sigNetFile(phases []NetSignalPhase, offset float64) *NetFile {
	link := 0
	return &NetFile{
		Version: 1,
		Name:    "sig",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 200, SpeedLimit: 13.89, Origin: true, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", TL: "J", TLLink: &link, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
		Signals: []NetSignal{{ID: "J", Junction: "J", Offset: offset, Phases: phases}},
	}
}

func sigSpec(t *testing.T, nf *NetFile, ticks uint64) RunSpec {
	t.Helper()
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return RunSpec{
		Net:    NetSpec{Kind: "file", Path: path},
		Params: DefaultParams(),
		Seed:   1,
		Ticks:  ticks,
	}
}

// The compiled program wires (program, link) onto the internal lane and
// compiles second durations onto the tick grid at engine build.
func TestSignalProgramWiring(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	n, err := CompileNet(nf)
	if err != nil {
		t.Fatal(err)
	}
	l := n.LaneByID("iJ_0")
	if l.Signal == nil {
		t.Fatal("iJ_0: no signal program wired")
	}
	if l.LinkIdx != 0 {
		t.Errorf("iJ_0 link = %d, want 0", l.LinkIdx)
	}
	e, err := NewEngine(sigSpec(t, nf, 10))
	if err != nil {
		t.Fatal(err)
	}
	p := e.Net.LaneByID("iJ_0").Signal
	want := []uint64{10, 3, 7}
	if len(p.phaseTicks) != 3 {
		t.Fatalf("phaseTicks = %v, want %v", p.phaseTicks, want)
	}
	for i, w := range want {
		if p.phaseTicks[i] != w {
			t.Errorf("phase %d = %d ticks, want %d (dt 0.1 s)", i, p.phaseTicks[i], w)
		}
	}
	if p.cycle != 20 {
		t.Errorf("cycle = %d ticks, want 20", p.cycle)
	}
}

// The phase index is a pure function of the tick count: exact boundaries,
// cycle wrap, and the SUMO offset semantics (phase 0 begins at offsetTicks).
func TestSignalPhaseBoundaries(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	p := e.Net.LaneByID("iJ_0").Signal
	cases := []struct {
		tick uint64
		want int
	}{
		{0, 0}, {9, 0}, {10, 1}, {12, 1}, {13, 2}, {19, 2}, // first cycle
		{20, 0}, {29, 0}, {30, 1}, {33, 2}, {39, 2}, {40, 0}, // wraps
	}
	for _, c := range cases {
		if got := p.phaseAt(c.tick); got != c.want {
			t.Errorf("phaseAt(%d) = %d, want %d", c.tick, got, c.want)
		}
	}

	// Offset 0.5 s (5 ticks): the program is 5 ticks into the cycle at
	// tick 5; tick 0 sits 5 ticks before the end of the previous cycle.
	nfOff := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0.5)
	eOff, err := NewEngine(sigSpec(t, nfOff, 0))
	if err != nil {
		t.Fatal(err)
	}
	pOff := eOff.Net.LaneByID("iJ_0").Signal
	if got := pOff.phaseAt(0); got != 2 {
		t.Errorf("offset program phaseAt(0) = %d, want 2 (wrapped into previous cycle)", got)
	}
	if got := pOff.phaseAt(5); got != 0 {
		t.Errorf("offset program phaseAt(5) = %d, want 0 (phase 0 begins at the offset)", got)
	}

	// The engine-facing state follows the same boundaries as ticks advance.
	l := e.Net.LaneByID("iJ_0")
	if st := e.sigState(l); st != SigGreen {
		t.Errorf("tick 0: state = %v, want green", st)
	}
	for e.Tick < 10 {
		e.Step()
	}
	if st := e.sigState(l); st != SigAmber {
		t.Errorf("tick 10: state = %v, want amber", st)
	}
	for e.Tick < 13 {
		e.Step()
	}
	if st := e.sigState(l); st != SigRed {
		t.Errorf("tick 13: state = %v, want red", st)
	}
	for e.Tick < 20 {
		e.Step()
	}
	if st := e.sigState(l); st != SigGreen {
		t.Errorf("tick 20: state = %v, want green (wrapped)", st)
	}
}

// Conservative state-char mapping: only g/G/y/r exert control.
func TestSignalCharMapping(t *testing.T) {
	want := map[byte]SigState{
		'g': SigGreen, 'G': SigGreen, 'y': SigAmber, 'r': SigRed,
		'o': SigOff, 'O': SigOff, 'u': SigOff, 'x': SigOff,
	}
	for c, w := range want {
		if got := mapSigChar(c); got != w {
			t.Errorf("mapSigChar(%q) = %v, want %v", c, got, w)
		}
	}
}

// Red holds a vehicle at the stop line; it proceeds on green.
func TestSignalRedHolds(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{10.0, "r"}, {15.0, "G"}}, 0) // red through tick 99
	e, err := NewEngine(sigSpec(t, nf, 300))
	if err != nil {
		t.Fatal(err)
	}
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	x := laneOf(t, e, "nX_0")
	v := e.AddInitialVehicle(a, 0, 170, 13.89, 1) // 30 m from the line

	stoppedAtLine, crossed := false, false
	for e.Tick < 300 {
		e.Step()
		assertNoNaN(t, e)
		if e.Tick < 100 && (v.Lane == iJ0 || v.Lane == x) {
			t.Fatalf("tick %d: entered the junction on red", e.Tick)
		}
		if v.Lane == a && v.V == 0 && a.Length-v.S <= v.Type.S0+1.5 {
			stoppedAtLine = true
		}
		if v.Lane == x {
			crossed = true
		}
	}
	if !stoppedAtLine {
		t.Error("vehicle never stopped at the line on red")
	}
	if !crossed {
		t.Error("vehicle did not proceed on green")
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// Amber, able to stop comfortably: the vehicle brakes to the line and holds.
func TestSignalAmberStopIfAble(t *testing.T) {
	// Green ticks 1–50, amber 51–150, red 151–200, green again from 201.
	nf := sigNetFile([]NetSignalPhase{{5.0, "G"}, {10.0, "y"}, {5.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 300))
	if err != nil {
		t.Fatal(err)
	}
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	x := laneOf(t, e, "nX_0")
	v := e.AddInitialVehicle(a, 0, 100, 10, 1) // reaches the decision zone during amber, able to stop

	stopped, crossed := false, false
	for e.Tick < 300 {
		e.Step()
		assertNoNaN(t, e)
		if e.Tick <= 200 && (v.Lane == iJ0 || v.Lane == x) {
			t.Fatalf("tick %d: entered the junction on amber/red though able to stop", e.Tick)
		}
		if v.Lane == a && v.V == 0 {
			stopped = true
		}
		if v.Lane == x {
			crossed = true
		}
	}
	if !stopped {
		t.Error("amber: comfortable vehicle never stopped")
	}
	if !crossed {
		t.Error("vehicle did not proceed on the next green")
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// Amber, unable to stop comfortably: the vehicle is committed and proceeds.
func TestSignalAmberCommitted(t *testing.T) {
	// Green ticks 1–50, amber 51–150, red 151–200.
	nf := sigNetFile([]NetSignalPhase{{5.0, "G"}, {10.0, "y"}, {5.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 200))
	if err != nil {
		t.Fatal(err)
	}
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	x := laneOf(t, e, "nX_0")
	v := e.AddInitialVehicle(a, 0, 120, 13.89, 1) // 80 m out at 13.89 m/s: amber catches it too close to stop

	enteredOnAmber, crossed, minV := false, false, 1e9
	for e.Tick < 200 {
		e.Step()
		assertNoNaN(t, e)
		if v.Lane == a && v.V < minV {
			minV = v.V
		}
		if v.Lane == iJ0 && e.Tick > 50 && e.Tick <= 150 {
			enteredOnAmber = true
		}
		if v.Lane == x {
			crossed = true
		}
	}
	if !enteredOnAmber {
		t.Error("committed vehicle did not proceed on amber")
	}
	if minV < 10 {
		t.Errorf("committed vehicle braked to %.2f m/s on the approach (should flow through)", minV)
	}
	if !crossed {
		t.Error("committed vehicle did not cross")
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// Green never means enter a box you cannot exit: with the exit lane full,
// the vehicle holds at the line even on green.
func TestSignalGreenBoxBlocked(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{60.0, "G"}}, 0)
	nf.Lanes[2] = NetLane{ID: "nX_0", Section: "X", Length: 8, SpeedLimit: 13.89, EndWall: true}
	e, err := NewEngine(sigSpec(t, nf, 200))
	if err != nil {
		t.Fatal(err)
	}
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	ex := laneOf(t, e, "nX_0")
	e.AddInitialVehicle(ex, 0, 8, 0, 1) // standing at the wall: 3 m of room < 5+2
	v := e.AddInitialVehicle(a, 0, 150, 13.89, 1)

	for e.Tick < 200 {
		e.Step()
		assertNoNaN(t, e)
		if v.Lane == iJ0 {
			t.Fatalf("tick %d: entered a box whose exit cannot receive it (green or not)", e.Tick)
		}
	}
	if v.V != 0 {
		t.Errorf("held vehicle did not brake to a stop (v=%.2f)", v.V)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// Off/blinking state chars exert no control: the approach falls back to the
// priority model — here RowNone, free traversal exactly as pre-signal.
func TestSignalOffFallback(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{60.0, "o"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 100))
	if err != nil {
		t.Fatal(err)
	}
	a := laneOf(t, e, "nA_0")
	x := laneOf(t, e, "nX_0")
	v := e.AddInitialVehicle(a, 0, 150, 13.89, 1)

	crossed, minV := false, 1e9
	for e.Tick < 100 {
		e.Step()
		assertNoNaN(t, e)
		if v.Lane == a && v.V < minV {
			minV = v.V
		}
		if v.Lane == x {
			crossed = true
		}
	}
	if !crossed {
		t.Error("off-signal approach did not traverse freely")
	}
	if minV < 13 {
		t.Errorf("off-signal approach braked to %.2f m/s (should flow ungated)", minV)
	}
}

// Phase state derives from the tick count, so a keyframe round-trip
// preserves it bit-exactly: same light, same continuation CRCs.
func TestSignalSaveLoad(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	spec := sigSpec(t, nf, 60)
	spec.Scen = Scenario{SpawnRatePerLaneHour: 800}

	full, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for full.Tick < 25 { // into the red window of cycle 2
		full.Step()
	}
	kf, err := full.MarshalState()
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	for full.Tick < 60 {
		full.Step()
	}

	restored, err := RestoreState(spec, kf)
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	rl, fl := restored.Net.LaneByID("iJ_0"), full.Net.LaneByID("iJ_0")
	if got, want := restored.sigState(rl), full.sigState(fl); got != want {
		t.Fatalf("restored light = %v, want %v at tick %d", got, want, full.Tick)
	}
	for restored.Tick < 60 {
		restored.Step()
	}
	if len(restored.CRCs) != len(full.CRCs[25:]) {
		t.Fatalf("restored run has %d CRCs, want %d", len(restored.CRCs), len(full.CRCs[25:]))
	}
	for i := range restored.CRCs {
		if restored.CRCs[i] != full.CRCs[25+i] {
			t.Fatalf("post-restore divergence at tick %d: crc %016x, want %016x",
				26+i, restored.CRCs[i], full.CRCs[25+i])
		}
	}
}

// Same seed, same signalized fixture → identical run CRC.
func TestSignalDeterminism(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	run := func() uint64 {
		spec := sigSpec(t, nf, 400)
		spec.Scen = Scenario{SpawnRatePerLaneHour: 800}
		e, err := NewEngine(spec)
		if err != nil {
			t.Fatal(err)
		}
		for e.Tick < 400 {
			e.Step()
			assertNoNaN(t, e)
		}
		if e.Stats.Collisions != 0 {
			t.Fatalf("%d collision observations, want 0", e.Stats.Collisions)
		}
		return e.CRC()
	}
	if a, b := run(), run(); a != b {
		t.Errorf("same seed diverged: crc %016x vs %016x", a, b)
	}
}

// Loader validation for the signals extension.
func TestSignalLoaderValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(nf *NetFile)
		want   string
	}{
		{"tlLink without tl", func(nf *NetFile) { nf.Lanes[1].TL = "" }, "must appear together"},
		{"tl without tlLink", func(nf *NetFile) { nf.Lanes[1].TLLink = nil }, "must appear together"},
		{"tl on normal lane", func(nf *NetFile) {
			link := 0
			nf.Lanes[0].TL, nf.Lanes[0].TLLink = "J", &link
		}, "non-internal"},
		{"unknown program", func(nf *NetFile) { nf.Lanes[1].TL = "nope" }, "unknown signal program"},
		{"tlLink out of range", func(nf *NetFile) { *nf.Lanes[1].TLLink = 1 }, "out of range"},
		{"duplicate program", func(nf *NetFile) { nf.Signals = append(nf.Signals, nf.Signals[0]) }, "duplicate id"},
		{"no phases", func(nf *NetFile) { nf.Signals[0].Phases = nil }, "no phases"},
		{"bad duration", func(nf *NetFile) { nf.Signals[0].Phases[0].Duration = 0 }, "duration"},
		{"ragged states", func(nf *NetFile) { nf.Signals[0].Phases[1].State = "yy" }, "ragged"},
		{"binding without programs", func(nf *NetFile) { nf.Signals = nil }, "without any signals program"},
	}
	for _, c := range cases {
		nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}}, 0)
		c.mutate(nf)
		if _, err := CompileNet(nf); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention %q", c.name, err, c.want)
		}
	}
}
