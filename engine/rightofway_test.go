package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rightofway_test.go — kernel enforcement of junction right-of-way
// (ADR-0010): minor yields to major, stop approaches stop, no starvation,
// and no entering a box whose exit is full. Networks are compiled NetFiles
// written to temp files, so the loader extension is exercised end to end.

// crossNetFile is a priority junction J: approach A feeds the internal lane
// iJ_0 (class aRow) draining to exit X, approach B feeds iJ_1 (major)
// draining to exit Y; the two internal lanes conflict (crossing).
func crossNetFile(aRow string) *NetFile {
	return &NetFile{
		Version: 1,
		Name:    "row-cross",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 100, SpeedLimit: 13.89, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", Row: aRow, FoesCross: []string{"iJ_1"}, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
			{ID: "nB_0", Section: "B", Length: 200, SpeedLimit: 13.89, Successors: []string{"iJ_1"}},
			{ID: "iJ_1", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", Row: "major", FoesCross: []string{"iJ_0"}, Successors: []string{"nY_0"}},
			{ID: "nY_0", Section: "Y", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
	}
}

// funnelNetFile is a junction whose two approaches (A minor, B major) merge
// through internal lanes into ONE shared exit lane — the junction-exit
// funnel where simultaneous arrivals overlapped before ADR-0010.
func funnelNetFile() *NetFile {
	return &NetFile{
		Version: 1,
		Name:    "row-funnel",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 100, SpeedLimit: 13.89, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", Row: "minor", FoesMerge: []string{"iJ_1"}, Successors: []string{"nE_0"}},
			{ID: "nB_0", Section: "B", Length: 200, SpeedLimit: 13.89, Successors: []string{"iJ_1"}},
			{ID: "iJ_1", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", Row: "major", FoesMerge: []string{"iJ_0"}, Successors: []string{"nE_0"}},
			{ID: "nE_0", Section: "E", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
	}
}

func newFileEngine(t *testing.T, nf *NetFile, ticks uint64) *Engine {
	t.Helper()
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(RunSpec{
		Net:    NetSpec{Kind: "file", Path: path},
		Params: DefaultParams(),
		Seed:   1,
		Ticks:  ticks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func laneOf(t *testing.T, e *Engine, id string) *Lane {
	t.Helper()
	l := e.Net.LaneByID(id)
	if l == nil {
		t.Fatalf("lane %s missing", id)
	}
	return l
}

// A minor-approach vehicle must brake to a stop at its line while a major
// vehicle crosses, enter only after the box is clear, and eventually cross
// (no starvation) — with zero collision observations throughout.
func TestMinorYieldsToMajor(t *testing.T) {
	e := newFileEngine(t, crossNetFile("minor"), 400)
	a := laneOf(t, e, "nA_0")
	b := laneOf(t, e, "nB_0")
	iJ0 := laneOf(t, e, "iJ_0")
	minor := e.AddInitialVehicle(a, 0, 80, 10, 1)     // 20 m from the line
	major := e.AddInitialVehicle(b, 0, 150, 13.89, 1) // 50 m out, too close to brake comfortably

	yielded, minorEnteredBox, majorInBoxFirst := false, false, false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		// Yield signature: the minor sheds its speed toward the line while
		// the conflict lasts (the IDM wall brakes asymptotically — it need
		// not reach exactly 0 before the gate opens).
		if minor.Lane == a && minor.V < 3 {
			yielded = true
		}
		if major.Lane == laneOf(t, e, "iJ_1") && !minorEnteredBox {
			majorInBoxFirst = true
		}
		if minor.Lane == iJ0 {
			minorEnteredBox = true
			if !majorInBoxFirst {
				t.Fatalf("tick %d: minor entered the box before the major vehicle", e.Tick)
			}
			if major.Lane == laneOf(t, e, "iJ_1") {
				t.Fatalf("tick %d: minor and major in the conflict box together", e.Tick)
			}
		}
	}
	if !yielded {
		t.Error("minor vehicle never yielded (speed never shed on the approach)")
	}
	if !minorEnteredBox {
		t.Error("starvation: minor vehicle never entered the junction")
	}
	if minor.Lane == a || minor.Lane == iJ0 {
		t.Errorf("starvation: minor vehicle stuck at %s after 40 s", minor.Lane.ID)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// A stop-approach vehicle must reach a full stop before the line even on an
// empty junction, then proceed once the stop is done.
func TestStopApproachStopsThenProceeds(t *testing.T) {
	e := newFileEngine(t, crossNetFile("stop"), 400)
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	v := e.AddInitialVehicle(a, 0, 50, 13.89, 1)

	fullStop := false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		if v.Lane == iJ0 && !fullStop {
			t.Fatalf("tick %d: stop-approach vehicle entered the junction without stopping", e.Tick)
		}
		if v.Lane == a && v.V == 0 {
			fullStop = true
			if a.Length-v.S > v.Type.S0+1.0 {
				t.Fatalf("tick %d: stopped %.2f m before the line (not AT it)", e.Tick, a.Length-v.S)
			}
		}
	}
	if !fullStop {
		t.Fatal("stop-approach vehicle never stopped")
	}
	if v.Lane == a || v.Lane == iJ0 {
		t.Errorf("vehicle did not proceed after its stop (at %s)", v.Lane.ID)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// Box blocking: a vehicle must not enter a junction whose box exit has no
// room for it (here: a short dead-end exit lane with a vehicle standing at
// the wall, leaving less than one vehicle length behind it).
func TestBoxBlocking(t *testing.T) {
	nf := &NetFile{
		Version: 1,
		Name:    "row-box",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 100, SpeedLimit: 13.89, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", Row: "major", Successors: []string{"nE_0"}},
			{ID: "nE_0", Section: "E", Length: 8, SpeedLimit: 13.89, EndWall: true},
		},
	}
	e := newFileEngine(t, nf, 300)
	a := laneOf(t, e, "nA_0")
	iJ0 := laneOf(t, e, "iJ_0")
	ex := laneOf(t, e, "nE_0")
	e.AddInitialVehicle(ex, 0, 8, 0, 1) // standing at the wall: 3 m of room < 5+2
	v := e.AddInitialVehicle(a, 0, 50, 13.89, 1)

	for e.Tick < 300 {
		e.Step()
		assertNoNaN(t, e)
		if v.Lane == iJ0 {
			t.Fatalf("tick %d: entered a box whose exit cannot receive it", e.Tick)
		}
	}
	if v.V != 0 {
		t.Errorf("held vehicle did not brake to a stop (v=%.2f)", v.V)
	}
	if a.Length-v.S > v.Type.S0+1.5 {
		t.Errorf("held vehicle stopped %.2f m short of the line", a.Length-v.S)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}

// The merge funnel: a major and a minor approach sharing one exit lane.
// The minor can stop comfortably, so the major flows through at speed while
// the minor holds, then merges behind it — no overlap on the shared exit.
func TestMergeFunnel(t *testing.T) {
	e := newFileEngine(t, funnelNetFile(), 400)
	a := laneOf(t, e, "nA_0")
	b := laneOf(t, e, "nB_0")
	ex := laneOf(t, e, "nE_0")
	minor := e.AddInitialVehicle(a, 0, 40, 13.89, 1)  // 60 m out: can yield comfortably
	major := e.AddInitialVehicle(b, 0, 170, 13.89, 1) // 30 m out, committed

	majorMinV := 1e9
	minorOnExit, majorOnExit := false, false
	for e.Tick < 400 {
		e.Step()
		assertNoNaN(t, e)
		if major.Lane != nil && major.V < majorMinV {
			majorMinV = major.V
		}
		if minor.Lane == ex {
			minorOnExit = true
		}
		if major.Lane == ex {
			majorOnExit = true
		}
	}
	if majorMinV < 10 {
		t.Errorf("major vehicle braked to %.2f m/s at its own right-of-way", majorMinV)
	}
	if !minorOnExit || !majorOnExit {
		t.Errorf("vehicles did not both traverse the shared exit (minor %v, major %v)",
			minorOnExit, majorOnExit)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations at the funnel, want 0", e.Stats.Collisions)
	}
	if e.Stats.MinGap < 0 {
		t.Errorf("negative minimum gap %.3f at the funnel", e.Stats.MinGap)
	}
}

// Loader validation for the v1 extension.
func TestRightOfWayLoaderValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(nf *NetFile)
		want   string
	}{
		{"bad row value", func(nf *NetFile) { nf.Lanes[1].Row = "yield" }, "unknown row state"},
		{"row on normal lane", func(nf *NetFile) { nf.Lanes[0].Row = "major" }, "non-internal"},
		{"unknown cross foe", func(nf *NetFile) { nf.Lanes[1].FoesCross = []string{"nope"} }, "unknown foesCross"},
		{"unknown merge foe", func(nf *NetFile) { nf.Lanes[1].FoesMerge = []string{"nope"} }, "unknown foesMerge"},
		{"non-internal foe", func(nf *NetFile) { nf.Lanes[1].FoesCross = []string{"nA_0"} }, "not internal"},
	}
	for _, c := range cases {
		nf := crossNetFile("minor")
		c.mutate(nf)
		if _, err := CompileNet(nf); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention %q", c.name, err, c.want)
		}
	}
}
