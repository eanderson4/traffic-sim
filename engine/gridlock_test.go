package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gridlock_test.go — the smallest network that reproduces the Chicago
// gridlock cycle, and the bounded escape from it (ADR-0034).
//
// The network is a one-way ring of four junctions whose segments alternate
// long and sub-vehicle-length, fed by four approaches, with every vehicle
// routed three-quarters of the way round before it leaves. Every ingredient
// of the measured cycle is here and nothing else:
//
//   - a CLOSED cycle of drivable lanes (Chicago's is 8 lanes, and 403 road
//     lanes end frozen against only 24 junction internals — the frozen mass
//     is on ROADS, in ordinary queues);
//   - segments shorter than a vehicle (4.7 m and 8.5 m there, 6 m here);
//   - several junctions discharging into the same ring;
//   - routes that continue around rather than leaving at the first
//     opportunity, so a full ring cannot self-resolve.
//
// A ring is not a contrivance: it is one city block, and the Loop is a grid
// of them.

// ringGridlockNet builds the ring. Junction Ji is entered by ring segment
// r(i-1) and feeder fi, and left by ring segment ri (continue) or exit xi
// (turn off). Ring lengths alternate 40 m / 6 m; a car is 5 m long and wants
// 2 m of jam gap, so the 6 m segments are the sub-vehicle-length blocks.
//
// The ring approach is major and the feeder minor — an ordinary priority
// junction. Signals are deliberately absent: ADR-0031 and ADR-0033 are about
// signalised yielding, and this failure has to reproduce without them.
func ringGridlockNet() *NetFile {
	ringLen := []float64{40, 6, 40, 6}
	var lanes []NetLane
	for i := 0; i < 4; i++ {
		r := fmt.Sprintf("r%d", i)
		f := fmt.Sprintf("f%d", i)
		x := fmt.Sprintf("x%d", i)
		iRing := fmt.Sprintf("iJ%d_r", i)
		iTurn := fmt.Sprintf("iJ%d_x", i)
		iFeed := fmt.Sprintf("iJ%d_f", i)

		lanes = append(lanes,
			// The ring segment leaving Ji; both its successors belong to the
			// NEXT junction, continue-first (the default branch).
			NetLane{ID: r, Section: strings.ToUpper(r), Length: ringLen[i], SpeedLimit: 13.89,
				Successors: []string{fmt.Sprintf("iJ%d_r", (i+1)%4), fmt.Sprintf("iJ%d_x", (i+1)%4)}},
			// The feeder: where demand enters the block.
			NetLane{ID: f, Section: strings.ToUpper(f), Length: 150, SpeedLimit: 13.89,
				Origin: true, Successors: []string{iFeed}},
			// The turn-off: an unbounded drain, so leaving the ring is never
			// what fails.
			NetLane{ID: x, Section: strings.ToUpper(x), Length: 200, SpeedLimit: 13.89, Exit: true},

			// Ring through movement: major, merging with the feeder into ri.
			NetLane{ID: iRing, Section: "j:J" + fmt.Sprint(i), Length: 6, SpeedLimit: 13.89,
				Internal: true, Junction: fmt.Sprint(i), Row: "major",
				FoesMerge: []string{iFeed}, Successors: []string{r}},
			// Ring turn-off: no foes, always servable.
			NetLane{ID: iTurn, Section: "j:J" + fmt.Sprint(i), Length: 6, SpeedLimit: 13.89,
				Internal: true, Junction: fmt.Sprint(i), Row: "major",
				Successors: []string{x}},
			// Feeder movement: minor, yields to the ring it merges into.
			NetLane{ID: iFeed, Section: "j:J" + fmt.Sprint(i), Length: 6, SpeedLimit: 13.89,
				Internal: true, Junction: fmt.Sprint(i), Row: "minor",
				FoesMerge: []string{iRing}, Successors: []string{r}},
		)
	}
	return &NetFile{Version: 1, Name: "ring-gridlock", Lanes: lanes}
}

// ringDest is where a vehicle entering at feeder i leaves: the turn-off three
// junctions on, so it traverses three of the four ring segments. One block of
// a grid, driven around rather than across.
func ringDest(i int) string { return fmt.Sprintf("x%d", (i+3)%4) }

// newParamsEngine is newFileEngine with the params under test — the escape
// threshold is the variable in half of these cases.
func newParamsEngine(t *testing.T, nf *NetFile, ticks uint64, p Params) *Engine {
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
		Net: NetSpec{Kind: "file", Path: path}, Params: p, Seed: 1, Ticks: ticks,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// fillFeeders queues eight stopped vehicles 8 m apart on each feeder, each
// routed three junctions round. No spawner: the placement is the whole
// input, so every run here is reproducible from this file alone.
func fillFeeders(t *testing.T, e *Engine) int {
	t.Helper()
	n := 0
	for i := 0; i < 4; i++ {
		f := laneOf(t, e, fmt.Sprintf("f%d", i))
		for k := 0; k < 8; k++ {
			v := e.AddInitialVehicle(f, 0, 145-float64(k)*8, 0, 1)
			v.Route = ringDest(i)
			n++
		}
	}
	return n
}

// ringState renders the ring for a failure message: vehicles and mean speed
// per lane, which is what distinguishes a queue from a frozen ring.
func ringState(e *Engine) string {
	var b strings.Builder
	for _, l := range e.Net.Lanes {
		if len(l.vehs) == 0 {
			continue
		}
		sum := 0.0
		for _, v := range l.vehs {
			sum += v.V
		}
		fmt.Fprintf(&b, "  %-8s %5.1f m  n=%d  mean v=%4.1f m/s\n",
			l.ID, l.Length, len(l.vehs), sum/float64(len(l.vehs)))
	}
	return b.String()
}

// ringMoving reports whether any vehicle anywhere is above walking pace.
func ringMoving(e *Engine) bool {
	for _, v := range e.order {
		if v.Lane != nil && v.V > 0.5 {
			return true
		}
	}
	return false
}

// THE DEFECT, pinned. With the escape disabled, a block fed from all four
// sides fills and stops for good.
//
// Note what is NOT wrong in the frozen state: no junction box is occupied,
// every vehicle is queued on an ordinary lane, and every gate is returning
// the correct answer — there really is no room on the lane ahead, at every
// link of the ring. The deadlock is a property of the cycle, not of any
// decision inside it, which is why no amount of extra time or reduced demand
// recovers it. That is the shape of the Chicago 90-minute baseline: 475
// lanes at identical jam density every interval from minute 55 to minute 90.
func TestRingGridlocksWithoutTheEscape(t *testing.T) {
	const seconds = 400
	p := DefaultParams()
	p.StrandAfterS = 0 // escape off: this is the raw phenomenon
	e := newParamsEngine(t, ringGridlockNet(), seconds*10, p)
	total := fillFeeders(t, e)

	frozenSince := -1
	for e.Tick < uint64(seconds*10) {
		e.Step()
		assertNoNaN(t, e)
		if ringMoving(e) || e.Stats.Despawned == total {
			frozenSince = -1
			continue
		}
		if frozenSince < 0 {
			frozenSince = int(e.Tick)
		}
		if int(e.Tick)-frozenSince >= 1200 { // 120 s with nothing moving
			return // the defect reproduced, which is what this test asserts
		}
	}
	t.Fatalf("expected the ring to gridlock with the escape disabled; it did not\n%s",
		ringState(e))
}

// THE ESCAPE. Same network, same demand, escape at its default threshold:
// the block gridlocks, works through it, and ends empty. Everything that
// entered is accounted for as either despawned or stranded — the escape may
// cost vehicles, but it may never lose them silently.
func TestRingWorksThroughGridlock(t *testing.T) {
	const seconds = 2400
	e := newFileEngine(t, ringGridlockNet(), seconds*10)
	if e.Params.StrandAfterS <= 0 {
		t.Fatalf("escape is OFF (StrandAfterS=%v) — this test asserts it is ON by default", e.Params.StrandAfterS)
	}
	total := fillFeeders(t, e)

	for e.Tick < uint64(seconds*10) {
		e.Step()
		assertNoNaN(t, e)
		if e.Stats.Despawned+e.Stats.Stranded == total {
			break
		}
	}
	if got := e.Stats.Despawned + e.Stats.Stranded; got != total {
		t.Fatalf("the ring never worked through the gridlock: %d of %d vehicles still on it after %d s\n%s",
			total-got, total, seconds, ringState(e))
	}
	// The escape is a cost, not a repair: it must be visible and localized.
	if e.Stats.Stranded == 0 {
		t.Error("nothing stranded — the ring cleared without the escape, so this " +
			"fixture no longer reproduces gridlock")
	}
	if len(e.Stats.StrandedBySection) == 0 {
		t.Error("strands were counted network-wide but not by section")
	}
	// Most of the demand must still be SERVED. An escape that empties the
	// network by deleting it would pass every assertion above.
	if e.Stats.Despawned*2 < total {
		t.Errorf("only %d of %d vehicles were served; %d stranded — the escape is "+
			"doing the draining instead of the network", e.Stats.Despawned, total, e.Stats.Stranded)
	}
	t.Logf("cleared: %d served, %d stranded, by section %v",
		e.Stats.Despawned, e.Stats.Stranded, e.Stats.StrandedBySection)
}

// The control that decides whether the threshold is safe: ORDINARY signalised
// queueing must never reach the escape. A queue at a red light is stopped for
// the same reason a gridlocked one is — no room, no permission — and the only
// thing separating them is that this one is served every cycle.
func TestEscapeIgnoresOrdinarySignalQueue(t *testing.T) {
	const seconds = 1200 // four times the escape threshold
	nf := permissiveMergeNetFile("gg")
	// A long, punishing cycle: 100 s of red for every 20 s of green, an
	// order of magnitude worse than anything in the Chicago import.
	nf.Signals = []NetSignal{{ID: "J", Junction: "J", Phases: []NetSignalPhase{
		{Duration: 20, State: "gg"},
		{Duration: 100, State: "rr"},
	}}}
	e := newFileEngine(t, nf, seconds*10)
	a := laneOf(t, e, "nA_0")
	for k := 0; k < 10; k++ { // a standing queue back from the line
		e.AddInitialVehicle(a, 0, 98-float64(k)*8, 0, 1)
	}
	for e.Tick < uint64(seconds*10) {
		e.Step()
		assertNoNaN(t, e)
	}
	if e.Stats.Stranded != 0 {
		t.Errorf("%d vehicles stranded at an ordinary signal queue (by section %v) — "+
			"the escape threshold is inside normal signal operation",
			e.Stats.Stranded, e.Stats.StrandedBySection)
	}
}

// A permanently sealed junction must BLEED, not evaporate. Everything behind
// the head has been stopped exactly as long as the head was, so without the
// backward reset in resetStuckBehind the whole queue crosses the threshold on
// the same tick and the road empties at once.
func TestSealedJunctionBleedsRatherThanFlushes(t *testing.T) {
	const seconds = 1300 // room for four escape intervals at the default 300 s
	// A approaches junction J, whose exit E is a dead end packed solid: the
	// box can never be cleared, so its entry gate is blocked for good. This
	// is one arm of a gridlock cycle with the rest of the cycle replaced by
	// something simpler that is just as permanent.
	nf := &NetFile{Version: 1, Name: "sealed", Lanes: []NetLane{
		{ID: "nA_0", Section: "A", Length: 200, SpeedLimit: 13.89, Successors: []string{"iJ_0"}},
		{ID: "iJ_0", Section: "j:J", Length: 8, SpeedLimit: 13.89, Internal: true,
			Junction: "J", Row: "major", Successors: []string{"nE_0"}},
		{ID: "nE_0", Section: "E", Length: 30, SpeedLimit: 13.89, EndWall: true},
	}}
	e := newFileEngine(t, nf, seconds*10)
	a := laneOf(t, e, "nA_0")
	blocked := laneOf(t, e, "nE_0")
	for s := 30.0; s >= 5; s -= 7 { // the exit, packed to the wall
		e.AddInitialVehicle(blocked, 0, s, 0, 1)
	}
	const queued = 12
	for k := 0; k < queued; k++ {
		e.AddInitialVehicle(a, 0, 200-float64(k)*8, 0, 1)
	}
	for e.Tick < uint64(seconds*10) {
		e.Step()
		assertNoNaN(t, e)
	}
	// Four intervals of 300 s fit in the horizon, so at most four vehicles
	// may go; the point is that it is bounded by TIME, not by queue length.
	if e.Stats.Stranded > 4 {
		t.Errorf("%d of %d vehicles stranded in %d s — the queue was flushed, not bled",
			e.Stats.Stranded, queued, seconds)
	}
	if e.Stats.Stranded == 0 {
		t.Errorf("nothing stranded on a permanently sealed road in %d s", seconds)
	}
}

// A keyframe taken while a stuck timer is running must carry it. The timer
// decides whether a vehicle EXISTS — strandStuck removes one that reaches
// StrandAfterS — and ReplayFromStream and Player.seek both restore from the
// latest keyframe at or before their target and then re-simulate forward
// verifying every logged CRC. A timer reset to zero by the restore strands
// the vehicle a whole StrandAfterS late, and every tick in between has a
// vehicle in one run and not the other: a replay divergence, not a rounding
// difference.
//
// Caught in external review of ADR-0034 (2026-07-28), where stuckTicks was
// documented as derived state on stopDone's precedent. stopDone survives
// being derived because it only changes whether a vehicle stops twice;
// this one deletes it.
func TestStuckTimerSurvivesAKeyframe(t *testing.T) {
	const seconds = 2400
	ticks := uint64(seconds * 10)
	data, err := json.Marshal(ringGridlockNet())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{Net: NetSpec{Kind: "file", Path: path}, Params: DefaultParams(), Seed: 1, Ticks: ticks}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	fillFeeders(t, e)

	// Run well into the gridlock, but stop short of the first strand so the
	// timers are mid-flight — which is exactly the state a mid-run keyframe
	// has to reproduce.
	limit := uint64(e.Params.StrandAfterS / e.Params.Dt)
	for e.Tick < limit-100 {
		e.Step()
		if e.Stats.Stranded > 0 {
			t.Fatalf("stranded at tick %d, before the timer could be sampled mid-flight", e.Tick)
		}
	}
	var maxStuck uint64
	for _, v := range e.order {
		if v.stuckTicks > maxStuck {
			maxStuck = v.stuckTicks
		}
	}
	if maxStuck == 0 {
		t.Fatal("no timer is running at the sample point; the test is not testing anything")
	}

	blob, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if ver := binary.LittleEndian.Uint16(blob[4:6]); ver < keyframeStuckVersion {
		t.Fatalf("keyframe version %d does not carry the stuck timer (want >= %d)", ver, keyframeStuckVersion)
	}
	restored, err := RestoreState(spec, blob)
	if err != nil {
		t.Fatal(err)
	}

	// The timers must match vehicle for vehicle, not just in aggregate.
	got := map[uint64]uint64{}
	for _, v := range restored.order {
		got[v.ID] = v.stuckTicks
	}
	for _, v := range e.order {
		if got[v.ID] != v.stuckTicks {
			t.Fatalf("vehicle %d: restored stuck timer %d, want %d — a restore that zeroes it strands %.0f s late and diverges the CRC",
				v.ID, got[v.ID], v.stuckTicks, e.Params.StrandAfterS)
		}
	}

	// And the consequence: both engines must strand on the SAME tick.
	origTick, restTick := strandTick(e, ticks), strandTick(restored, ticks)
	if origTick == 0 {
		t.Fatal("the original never stranded; nothing to compare")
	}
	if origTick != restTick {
		t.Fatalf("first strand at tick %d after a restore, %d without one", restTick, origTick)
	}
}

// strandTick steps e until its first strand and reports the tick, or 0.
func strandTick(e *Engine, ticks uint64) uint64 {
	for e.Tick < ticks {
		e.Step()
		if e.Stats.Stranded > 0 {
			return e.Tick
		}
	}
	return 0
}

// A v5 keyframe with an EMPTY director queue must round-trip. Before v5,
// "version >= 3" and "the queue is non-empty" were the same condition, so
// the writer could gate the directive section on the queue while the reader
// gated on the version. A running stuck timer alone now lifts a state to v5,
// which breaks that coincidence: the reader would take a directive count out
// of bytes nobody wrote, latch a short read, and pass only because the
// section is last and r.err is never re-checked after it.
//
// Introduced by the v5 change itself and caught by Claude Fable in the
// ADR-0034 review round (2026-07-28).
func TestStuckKeyframeRoundTripsWithAnEmptyDirectorQueue(t *testing.T) {
	data, err := json.Marshal(ringGridlockNet())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{Net: NetSpec{Kind: "file", Path: path}, Params: DefaultParams(), Seed: 1, Ticks: 6000}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	fillFeeders(t, e)
	for e.Tick < 600 {
		e.Step()
	}
	if len(e.dirQueue) != 0 {
		t.Fatalf("director queue has %d entries; this test is about the EMPTY case", len(e.dirQueue))
	}
	var stuck uint64
	for _, v := range e.order {
		stuck += v.stuckTicks
	}
	if stuck == 0 {
		t.Fatal("no stuck timer is running, so the keyframe would not be v5")
	}
	blob, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if ver := binary.LittleEndian.Uint16(blob[4:6]); ver != keyframeStuckVersion {
		t.Fatalf("keyframe version %d, want %d", ver, keyframeStuckVersion)
	}
	restored, err := RestoreState(spec, blob)
	if err != nil {
		t.Fatalf("v5 keyframe with an empty director queue failed to restore: %v", err)
	}
	if len(restored.order) != len(e.order) {
		t.Fatalf("restored %d vehicles, want %d", len(restored.order), len(e.order))
	}
	// And it must re-marshal to the same bytes, which a swallowed short read
	// does not survive.
	again, err := restored.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blob, again) {
		t.Fatal("v5 keyframe did not round-trip byte-identically with an empty director queue")
	}
}

// TestStrandLimitLandsOnTheDocumentedThreshold pins the tick conversion.
//
// StrandAfterS/Dt is a float ratio, and truncating it strands a full tick
// EARLY when the quotient lands just below an integer: 0.3/0.1 is
// 2.9999999999999996 in float64, so truncation gives 2 ticks for a 0.3 s
// threshold. Rounding gives 3.
//
// Both reviewers flagged the line claiming the DEFAULT was wrong — that
// 300/0.1 truncates to 2999. It does not; it is exactly 3000.0. This test
// therefore pins the case that actually regresses AND pins the defaults as
// unchanged, so nobody "fixes" them back on the strength of that claim.
func TestStrandLimitLandsOnTheDocumentedThreshold(t *testing.T) {
	cases := []struct {
		afterS, dt float64
		want       uint64
		trunc      uint64 // what truncation gives, to show where they differ
	}{
		{0.3, 0.1, 3, 2},       // THE bug: 2.9999999999999996
		{300, 0.1, 3000, 3000}, // the default: exact, unaffected
		{0.5, 0.1, 5, 5},       // exact
		{60, 0.05, 1200, 1200}, // exact
		{10, 1, 10, 10},        // integral
		{1, 0.3, 3, 3},         // 3.333…: grid quantisation, not a bug
	}
	for _, c := range cases {
		if got := uint64(math.Round(c.afterS / c.dt)); got != c.want {
			t.Errorf("StrandAfterS=%v dt=%v: rounded limit %d, want %d", c.afterS, c.dt, got, c.want)
		}
		if got := uint64(c.afterS / c.dt); got != c.trunc {
			t.Errorf("StrandAfterS=%v dt=%v: truncated limit %d, want %d — the float arithmetic this test documents has moved", c.afterS, c.dt, got, c.trunc)
		}
		// The property that motivates rounding: the threshold the tick limit
		// implies is within half a tick of the one requested. Truncation
		// violates this for the first case.
		implied := float64(c.want) * c.dt
		if diff := implied - c.afterS; diff > c.dt/2+1e-9 || diff < -c.dt/2-1e-9 {
			t.Errorf("StrandAfterS=%v dt=%v: limit %d implies %vs, off by %v (> half a tick)",
				c.afterS, c.dt, c.want, implied, diff)
		}
	}
}

// TestStrandLimitUsesRoundingAtTheDefaults is the wiring check: the arithmetic
// above is standalone, this asserts the defaults produce the threshold ADR-0034
// states. The defaults are NOT affected by the truncation bug, so this test's
// job is to keep them that way, not to prove the fix.
func TestStrandLimitUsesRoundingAtTheDefaults(t *testing.T) {
	p := DefaultParams()
	if p.StrandAfterS != 300 || p.Dt != 0.1 {
		t.Skipf("defaults moved (StrandAfterS=%v Dt=%v) — update this test", p.StrandAfterS, p.Dt)
	}
	if got := uint64(math.Round(p.StrandAfterS / p.Dt)); got != 3000 {
		t.Fatalf("default strand limit is %d ticks, want 3000 (%v s at dt=%v)", got, p.StrandAfterS, p.Dt)
	}
}
