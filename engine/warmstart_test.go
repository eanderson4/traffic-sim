package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// warmstart_test.go — ADR-0029 phase 1. Two properties matter and nothing
// else does:
//
//  1. a warm-started run is BIT-EXACT with the run it was cut out of. If it
//     is not, warm start silently changes the thing being debugged, which is
//     worse than not having it. The oracle is the engine's own rolling CRC
//     chain (engine.go computeCRC), not a hand-rolled comparison.
//  2. a state loaded against a DIFFERENT network fails LOUDLY. Keyframes
//     bind vehicles to lane INDEX, so a shifted index puts a vehicle on a
//     valid wrong lane with no error at all.

// The headline: run N ticks, dump, restore, run M more — the CRC chain must
// match an uninterrupted N+M run tick for tick.
func TestWarmStartRoundTripIsBitExact(t *testing.T) {
	const dumpAt, total = 150, 400
	spec, err := DefaultSpec("lanedrop", total, 11)
	if err != nil {
		t.Fatal(err)
	}

	// Reference: one uninterrupted run.
	_, full, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}

	// Cut: run to dumpAt, save to a file, load it back, run on.
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < dumpAt {
		e.Step()
	}
	path := filepath.Join(t.TempDir(), "state.bin")
	if err := SaveState(path, e, spec, "warm-test"); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	data, meta, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if meta.Tick != dumpAt {
		t.Fatalf("sidecar tick = %d, want %d", meta.Tick, dumpAt)
	}
	if meta.Vehicles != len(e.Vehicles()) || meta.Vehicles == 0 {
		t.Fatalf("sidecar vehicles = %d, want %d (and nonzero — an empty state proves nothing)", meta.Vehicles, len(e.Vehicles()))
	}
	if meta.LaneCount != len(e.Net.Lanes) || meta.LaneFingerprint != laneFingerprintHex(e.Net) {
		t.Fatalf("sidecar network fingerprint wrong: %d lanes %s", meta.LaneCount, meta.LaneFingerprint)
	}

	warm, err := RestoreStateChecked(spec, data, meta)
	if err != nil {
		t.Fatalf("RestoreStateChecked: %v", err)
	}
	if warm.Tick != dumpAt || warm.CRC() != e.CRC() {
		t.Fatalf("restored engine at tick %d crc %016x, want tick %d crc %016x",
			warm.Tick, warm.CRC(), e.Tick, e.CRC())
	}
	for warm.Tick < total {
		warm.Step()
	}
	// e.CRCs covers ticks 1..dumpAt, warm.CRCs covers dumpAt+1..total.
	got := append(append([]uint64{}, e.CRCs...), warm.CRCs...)
	assertEqualCRCs(t, full.CRCs, got)
	if warm.CRC() != full.CRCs[total-1] {
		t.Fatalf("final CRC %016x, want %016x", warm.CRC(), full.CRCs[total-1])
	}
	t.Logf("bit-exact across the cut: %d vehicles, %d state bytes at tick %d", meta.Vehicles, len(data), dumpAt)
}

// netfileSpec writes nf to a temp file and specs a run over it.
func netfileSpec(t *testing.T, nf *NetFile, ticks, seed uint64) RunSpec {
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
		Scen:   Scenario{SpawnRatePerLaneHour: 1800, DensityTargetPerKm: 80},
		Params: DefaultParams(),
		Seed:   seed,
		Ticks:  ticks,
	}
}

// A state saved against one network must not load against another —
// neither a network with a lane added (indices shift) nor one with the same
// lane count and different ids.
func TestWarmStartRefusesForeignNetwork(t *testing.T) {
	base := netfileSpec(t, twoEdgeNetFile(), 400, 3)
	e, err := NewEngine(base)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 300 {
		e.Step()
	}
	if len(e.Vehicles()) == 0 {
		t.Fatal("no vehicles at the cut — the test would not exercise lane binding")
	}
	path := filepath.Join(t.TempDir(), "state.bin")
	if err := SaveState(path, e, base, "guard-test"); err != nil {
		t.Fatal(err)
	}
	data, meta, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	// One lane appended: the lane COUNT differs.
	widened := twoEdgeNetFile()
	widened.Lanes = append(widened.Lanes, NetLane{
		ID: "nE1_2", Section: "E1", Edge: "E1", EdgeIndex: 2,
		Length: 100, SpeedLimit: 13.9, Width: 3.5,
		Shape: [][2]float64{{0, 10.25}, {100, 10.25}}, Exit: true,
	})
	got, err := RestoreStateChecked(netfileSpec(t, widened, 400, 3), data, meta)
	if err == nil {
		t.Fatal("a state loaded against a network with an added lane — vehicles would sit on shifted indices")
	}
	if got != nil {
		t.Fatal("refused load still produced an engine")
	}
	if !strings.Contains(err.Error(), "lanes") {
		t.Fatalf("lane-count mismatch error does not name the difference: %v", err)
	}
	t.Logf("added lane: %v", err)

	// Same lane count, one lane RENAMED: indices still resolve, and every
	// vehicle past the renamed lane would be on a valid wrong lane.
	renamed := twoEdgeNetFile()
	for i := range renamed.Lanes {
		if renamed.Lanes[i].ID == "nE1_1" {
			renamed.Lanes[i].ID = "nE1_9"
		}
		for j, s := range renamed.Lanes[i].Successors {
			if s == "nE1_1" {
				renamed.Lanes[i].Successors[j] = "nE1_9"
			}
		}
	}
	renamedSpec := netfileSpec(t, renamed, 400, 3)
	got, err = RestoreStateChecked(renamedSpec, data, meta)
	if err == nil {
		t.Fatal("a state loaded against a same-size network with different lane ids")
	}
	if got != nil {
		t.Fatal("refused load still produced an engine")
	}
	if !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("id mismatch error does not name the fingerprint: %v", err)
	}
	t.Logf("renamed lane: %v", err)

	// Why the sidecar exists at all: the UNGUARDED path accepts this
	// silently. The keyframe's only lane check is a range check, so a
	// same-size foreign network loads and runs and looks like a result.
	// (If this ever starts failing, keyframes have become network-aware —
	// ADR-0029 decision 1, the lane-id format — and the guard has company.)
	if _, err := RestoreState(renamedSpec, data); err != nil {
		t.Fatalf("unguarded restore now rejects a foreign network (%v) — the silent-corruption hazard this guard exists for may be closed; revisit ADR-0029", err)
	}
}

// A state with no sidecar, or one paired with the wrong sidecar, is refused
// rather than assumed to be fine.
func TestWarmStartRefusesBadSidecar(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 400, 5)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	early, late := filepath.Join(dir, "early.bin"), filepath.Join(dir, "late.bin")
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 100 {
		e.Step()
	}
	if err := SaveState(early, e, spec, "sidecar-test"); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 200 {
		e.Step()
	}
	if err := SaveState(late, e, spec, "sidecar-test"); err != nil {
		t.Fatal(err)
	}

	// Missing sidecar.
	if err := os.Remove(StateMetaPath(early)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadState(early); err == nil {
		t.Fatal("state with no sidecar accepted")
	} else if !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("missing-sidecar error does not say so: %v", err)
	} else {
		t.Logf("missing sidecar: %v", err)
	}

	// Wrong sidecar: the late run's sidecar next to the early state.
	raw, err := os.ReadFile(StateMetaPath(late))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(StateMetaPath(early), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadState(early); err == nil {
		t.Fatal("state paired with another state's sidecar accepted")
	} else {
		t.Logf("mismatched sidecar: %v", err)
	}

	// Unknown format tag: refuse rather than read unknown fields as a pass.
	var m StateMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.Format = "something else"
	bad, _ := json.Marshal(m)
	if err := os.WriteFile(StateMetaPath(late), bad, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadState(late); err == nil {
		t.Fatal("sidecar with an unknown format tag accepted")
	}
}

// The fingerprint must depend on lane ids AND their index order AND their
// lengths, must be stable across calls in a process, and must not be fooled
// by ids that concatenate alike.
func TestLaneFingerprintIsOrderedAndStable(t *testing.T) {
	net := func(ids ...string) *Network {
		n := &Network{}
		for i, id := range ids {
			n.Lanes = append(n.Lanes, &Lane{ID: id, Index: i, Length: 100})
		}
		return n
	}
	a := net("nE0_0", "nE0_1", "nE1_0")
	if LaneFingerprint(a) != LaneFingerprint(net("nE0_0", "nE0_1", "nE1_0")) {
		t.Fatal("fingerprint is not a function of the lane ids alone")
	}
	if LaneFingerprint(a) == LaneFingerprint(net("nE0_1", "nE0_0", "nE1_0")) {
		t.Fatal("fingerprint ignores lane ORDER — index order is the whole binding a keyframe relies on")
	}
	if LaneFingerprint(a) == LaneFingerprint(net("nE0_0", "nE0_1", "nE1_0", "nE1_1")) {
		t.Fatal("fingerprint ignores an appended lane")
	}
	if LaneFingerprint(net("ab", "c")) == LaneFingerprint(net("a", "bc")) {
		t.Fatal("fingerprint concatenates ids without a length prefix")
	}
	// Same lanes, same order, one re-measured. A keyframe stores (lane
	// index, S) and S is a distance along the lane, so identical ids over
	// different geometry is exactly the silent wrong-position case the
	// guard exists for — and the only load-time check is a range check on
	// the index, which this passes.
	b := net("nE0_0", "nE0_1", "nE1_0")
	b.Lanes[1].Length = 100.5
	if LaneFingerprint(a) == LaneFingerprint(b) {
		t.Fatal("fingerprint ignores lane LENGTH — a state restored onto re-measured geometry puts every vehicle at a valid, wrong position")
	}
}

// Tick and byte length do not prove a sidecar and a state file belong
// together: two states saved at the same tick with the same payload size
// pass both checks, and the network fingerprint the sidecar carries is then
// a guard written for somebody else's bytes. Raised as a blocker in external
// review of ADR-0029 (2026-07-28).
func TestWarmStartRefusesASidecarForOtherBytes(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 400, 5)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.bin")
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 100 {
		e.Step()
	}
	if err := SaveState(path, e, spec, "digest-test"); err != nil {
		t.Fatal(err)
	}
	data, _, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same tick, same length, different bytes — everything the pre-digest
	// sidecar checked still matches.
	tampered := append([]byte(nil), data...)
	tampered[len(tampered)-1] ^= 0xff
	if len(tampered) != len(data) {
		t.Fatal("the tamper changed the length; it would be caught by the old check")
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadState(path); err == nil {
		t.Fatal("loaded a state whose sidecar was written for different bytes")
	}
}

// A keyframe stores each vehicle's TYPE ORDINAL, so reordering the scenario's
// type list on the SAME network turns every restored vehicle into a different
// type — with the lane fingerprint still matching, because the network did
// not change. Raised as a blocker in the same review.
func TestWarmStartRefusesAReorderedTypeTable(t *testing.T) {
	if TypeFingerprint([]*VehicleType{{Name: "car"}, {Name: "truck"}}) ==
		TypeFingerprint([]*VehicleType{{Name: "truck"}, {Name: "car"}}) {
		t.Fatal("type fingerprint ignores ORDER, which is the whole binding a keyframe relies on")
	}
	if TypeFingerprint([]*VehicleType{{Name: "car"}}) ==
		TypeFingerprint([]*VehicleType{{Name: "car"}, {Name: "truck"}}) {
		t.Fatal("type fingerprint ignores an appended type")
	}
	if TypeFingerprint([]*VehicleType{{Name: "ab"}, {Name: "c"}}) ==
		TypeFingerprint([]*VehicleType{{Name: "a"}, {Name: "bc"}}) {
		t.Fatal("type fingerprint concatenates names without a length prefix")
	}
	m := &StateMeta{TypeFingerprint: typeFingerprintHex([]*VehicleType{{Name: "car"}, {Name: "truck"}})}
	if err := m.CheckTypes([]*VehicleType{{Name: "car"}, {Name: "truck"}}); err != nil {
		t.Fatalf("same table rejected: %v", err)
	}
	if err := m.CheckTypes([]*VehicleType{{Name: "truck"}, {Name: "car"}}); err == nil {
		t.Fatal("a reordered type table passed the guard")
	}
	// An absent fingerprint is a refusal, not a skip.
	if err := (&StateMeta{}).CheckTypes([]*VehicleType{{Name: "car"}}); err == nil {
		t.Fatal("an empty type fingerprint was treated as a passed guard")
	}
}

// A keyframe must carry stopDone. Sol caught this in review: it was left out
// as derived state on the argument that its worst case is a vehicle stopping
// twice — but ADR-0029's acceptance criterion is BIT-EXACT continuation, and
// stopping twice is a different trajectory.
//
// The reachable case is a vehicle that has discharged its stop duty and is
// already MOVING off the line: on restore stopDone is false, it cannot
// re-satisfy the `V == 0 && dist <= S0+1` test that sets it (it is no longer
// stopped), so the right-of-way gate holds it and it brakes to a second full
// stop the original run never made.
//
// Cut the run at a tick where some vehicle has stopDone set, restore, and
// require the CRC chains to agree tick for tick. Without the field in the
// keyframe this diverges.
func TestStopDoneSurvivesAKeyframe(t *testing.T) {
	const aisleOrigin = "n777610268_1_0"
	spec := stopFixtureSpec()
	plan := func(e *Engine, next uint64) {
		if next%120 == 1 {
			if err := e.EnqueueSpawn(SpawnDirective{
				RequestID: fmt.Sprintf("aisle-%d", next), Origin: aisleOrigin,
				TypeName: "car", EarliestTick: next,
			}); err != nil {
				t.Fatalf("enqueue: %v", err)
			}
		}
	}

	// Cold run: step until a vehicle actually holds stopDone, then snapshot
	// there. Picking a fixed tick would risk cutting where nothing has
	// discharged its duty, and the test would prove nothing.
	cold, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	cut := uint64(0)
	for cold.Tick < spec.Ticks {
		plan(cold, cold.Tick+1)
		cold.Step()
		done := 0
		for _, v := range cold.order {
			if v.stopDone {
				done++
			}
		}
		if done > 0 && cut == 0 {
			cut = cold.Tick
			break
		}
	}
	if cut == 0 {
		t.Fatal("no vehicle ever discharged its stop duty — the fixture proves nothing about stopDone")
	}

	data, err := cold.MarshalState()
	if err != nil {
		t.Fatalf("MarshalState: %v", err)
	}
	if ver := binary.LittleEndian.Uint16(data[4:6]); ver < keyframeStuckVersion {
		t.Fatalf("state at tick %d holds a stopDone vehicle but marshalled as v%d, want >= v%d",
			cut, ver, keyframeStuckVersion)
	}

	warm, err := RestoreState(spec, data)
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	for _, cv := range cold.order {
		var wv *Vehicle
		for _, x := range warm.order {
			if x.ID == cv.ID {
				wv = x
				break
			}
		}
		if wv == nil {
			t.Fatalf("vehicle %d missing after restore", cv.ID)
		}
		if wv.stopDone != cv.stopDone {
			t.Errorf("vehicle %d: stopDone %v after restore, was %v", cv.ID, wv.stopDone, cv.stopDone)
		}
	}

	// The real assertion: the continuation must be the same program. Run
	// both to the horizon and compare the CRC chain tick for tick.
	for cold.Tick < spec.Ticks {
		plan(cold, cold.Tick+1)
		cold.Step()
		plan(warm, warm.Tick+1)
		warm.Step()
		if cold.CRC() != warm.CRC() {
			t.Fatalf("CRC divergence at tick %d (cut at %d): warm %016x vs cold %016x",
				cold.Tick, cut, warm.CRC(), cold.CRC())
		}
	}
	t.Logf("cut at tick %d, matched the cold chain to the horizon (%d ticks)", cut, spec.Ticks-cut)
}
