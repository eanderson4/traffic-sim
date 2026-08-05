package sigctl

import (
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine/natsio"
)

// sigctl_test.go — the decision core, the transition walk, the chain
// bookkeeping, the chunk accumulator, and the detector scan as pure
// functions (no NATS). The wire integration test is live_run_test.go.

// testProgram is a two-movement actuated program with transition phases:
// green-A [0,100), trans [100,110), green-B [110,210), trans [210,220).
func testProgram() natsio.SigProgram {
	return natsio.SigProgram{
		ID: "J",
		Phases: []natsio.SigPhase{
			{DurationTicks: 100, State: "Gr"},
			{DurationTicks: 10, State: "yr"},
			{DurationTicks: 100, State: "rG"},
			{DurationTicks: 10, State: "ry"},
		},
	}
}

func testProgState() *progState {
	p := testProgram()
	geom := &Geom{ByProgram: map[string][]*Detector{
		"J": {{Link: 0, X: 0, Y: 0, Dx: 1}, {Link: 1, X: 50, Y: 0, Dx: 1}},
	}}
	ps := newProgState(p, geom)
	if ps == nil {
		panic("test program not actuatable")
	}
	return ps
}

func testConfig() Config {
	cfg := Config{}
	cfg.defaults()
	cfg.MinGreenTicks = 50  // the tests' tick arithmetic predates the 100 default
	cfg.MaxChainTicks = 200 // small, so the chain bound is reachable in a test
	return cfg
}

// testController returns a Controller with the test config, a log (send
// logs publish failures; unit tests have no connection), and the shared
// test geometry.
func testController() *Controller {
	return &Controller{cfg: testConfig(), log: discardLog,
		progs: map[string]*progState{}, grid: map[[2]int][]*Detector{}, geom: testGeom(),
		tableReady: make(chan struct{})}
}

func TestDecideProgram(t *testing.T) {
	c := testController()

	// (a) Fixed-time green serving presence, no hold of mine: take control.
	ps := testProgState()
	if ph, hold, ok := c.decideProgram(ps, []int{1, 0}, 10); !ok || ph != 0 || hold != c.cfg.HoldTicks {
		t.Errorf("(a) take control: got (%d, %d, %v), want (0, %d, true)", ph, hold, ok, c.cfg.HoldTicks)
	}

	// (b) My hold is fresh: no verb.
	ps = testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 200, 100
	if ph, _, ok := c.decideProgram(ps, []int{1, 0}, 150); ok {
		t.Errorf("(b) fresh hold: got (%d, true), want no verb (50 ticks left ≥ renew-below 30)", ph)
	}

	// (c) Hold running out with chain budget: renew the enforced phase.
	if ph, _, ok := c.decideProgram(ps, []int{1, 0}, 175); !ok || ph != 0 {
		t.Errorf("(c) renew: got (%d, %v), want (0, true)", ph, ok)
	}

	// (d) Chain bound reached (max-green): never renew into a decline;
	// with a waiting call, switch — through the walk, so the first
	// command is the TRANSITION phase 1 at its natural 10 ticks, not the
	// target.
	ps = testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 310, 100
	if ph, hold, ok := c.decideProgram(ps, []int{1, 1}, 299); !ok || ph != 1 || hold != 10 {
		t.Errorf("(d) rail-cap switch walks: got (%d, %d, %v), want (1, 10, true)", ph, hold, ok)
	}

	// (e) Gap-out: nothing on the served approach, a call waiting,
	// min-green met: switch — again walking, transition phase 1 at 10.
	ps = testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 200, 100
	if ph, hold, ok := c.decideProgram(ps, []int{0, 1}, 160); !ok || ph != 1 || hold != 10 {
		t.Errorf("(e) gap-out switch walks: got (%d, %d, %v), want (1, 10, true)", ph, hold, ok)
	}

	// (f) Same, but the phase is younger than min-green: hold off.
	if ph, _, ok := c.decideProgram(ps, []int{0, 1}, 105); ok {
		t.Errorf("(f) min-green: got (%d, true), want no verb", ph)
	}

	// (g) A transition phase is never a target, presence or not.
	ps = testProgState()
	if ph, _, ok := c.decideProgram(ps, []int{1, 1}, 105); ok {
		// tick 105 is inside transition phase 1 [100,110)
		t.Errorf("(g) transition phase: got (%d, true), want no verb", ph)
	}

	// (h) No presence anywhere: leave the junction alone (idle → fixed time).
	ps = testProgState()
	if ph, _, ok := c.decideProgram(ps, []int{0, 0}, 10); ok {
		t.Errorf("(h) idle: got (%d, true), want no verb", ph)
	}

	// (i) Switch back toward phase 0 from phase 2: the walk steps to
	// phase 3 (transition) at its natural 10 ticks.
	ps = testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 2, 100, 200, 100
	if ph, hold, ok := c.decideProgram(ps, []int{1, 0}, 160); !ok || ph != 3 || hold != 10 {
		t.Errorf("(i) switch back walks: got (%d, %d, %v), want (3, 10, true)", ph, hold, ok)
	}
}

// TestWalkSequence: a switch decision walks the program's table order
// through the transition at its natural duration, arrives at the target,
// and resumes normal hold/renew logic there.
func TestWalkSequence(t *testing.T) {
	c := testController()
	ps := testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 200, 100

	// Gap-out at 160: walk starts — transition phase 1 at 10 ticks.
	ph, hold, ok := c.decideProgram(ps, []int{0, 1}, 160)
	if !ok || ph != 1 || hold != 10 {
		t.Fatalf("walk start: (%d, %d, %v), want (1, 10, true)", ph, hold, ok)
	}
	c.send(ps, ph, hold, 160) // applies at 161, covers [161,171)
	if ps.target != 2 {
		t.Fatalf("walk target = %d, want 2", ps.target)
	}
	// Mid-step at 166 the transition still covers the next boundary (167 <
	// 171): no verb. The step to the target issues at 170 — applied 171,
	// exactly when the transition's hold ends (seamless).
	if _, _, ok := c.decideProgram(ps, []int{0, 1}, 166); ok {
		t.Fatal("verb issued while the transition step still covers the next boundary")
	}
	ph, hold, ok = c.decideProgram(ps, []int{0, 1}, 170)
	if !ok || ph != 2 || hold != c.cfg.HoldTicks {
		t.Fatalf("walk arrival step: (%d, %d, %v), want (2, %d, true)", ph, hold, ok, c.cfg.HoldTicks)
	}
	c.send(ps, ph, hold, 170) // applies at 171, covers [171,271)
	// Arrived: the target clears and the normal logic holds it (fresh
	// hold → no verb).
	if _, _, ok := c.decideProgram(ps, []int{0, 1}, 180); ok {
		t.Fatal("post-arrival: a verb while the target's hold is fresh")
	}
	if ps.target != -1 {
		t.Fatalf("target = %d after arrival, want -1", ps.target)
	}
}

// TestMixedPhaseNeverHeld: a mixed amber/green phase is a transition —
// never a candidate, never held past its natural duration, and the walk
// crosses it at exactly that duration.
func TestMixedPhaseNeverHeld(t *testing.T) {
	p := natsio.SigProgram{
		ID: "M",
		Phases: []natsio.SigPhase{
			{DurationTicks: 100, State: "Gr"},
			{DurationTicks: 10, State: "Gy"}, // mixed: one green, one amber
			{DurationTicks: 100, State: "rG"},
			{DurationTicks: 10, State: "ry"},
		},
	}
	geom := &Geom{ByProgram: map[string][]*Detector{
		"M": {{Link: 0, X: 0, Y: 0, Dx: 1}, {Link: 1, X: 50, Y: 0, Dx: 1}},
	}}
	ps := newProgState(p, geom)
	if ps == nil {
		t.Fatal("program M not actuatable")
	}
	for _, cand := range ps.candidates {
		if cand == 1 || cand == 3 {
			t.Fatalf("candidate list %v contains a transition phase", ps.candidates)
		}
	}
	c := testController()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 200, 100
	// Gap-out toward phase 2: the walk's first step is the MIXED phase 1,
	// commanded at exactly its natural 10 ticks — never longer.
	ph, hold, ok := c.decideProgram(ps, []int{0, 1}, 160)
	if !ok || ph != 1 || hold != 10 {
		t.Fatalf("walk into mixed phase: (%d, %d, %v), want (1, 10, true)", ph, hold, ok)
	}
}

// TestRenewalKeepsPhaseSince: a renewal must not restart the min-green
// clock — phaseSince moves only when the commanded phase changes.
func TestRenewalKeepsPhaseSince(t *testing.T) {
	c := testController()
	ps := testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 200, 100
	c.send(ps, 0, c.cfg.HoldTicks, 175) // renewal, applies at 176
	if ps.phaseSince != 100 {
		t.Fatalf("phaseSince = %d after a renewal, want 100 (min-green measures the phase's age)", ps.phaseSince)
	}
	if ps.holdUntil != 176+c.cfg.HoldTicks {
		t.Fatalf("holdUntil = %d after renewal, want %d", ps.holdUntil, 176+c.cfg.HoldTicks)
	}
	// Gap-out at tick 210: the phase is 110 ticks old, past the 50-tick
	// min-green — legal even though the last renewal was at 176. (Under
	// the round-1 bug the clock restarted per renewal and this was
	// blocked until the hold lapsed.)
	if _, _, ok := c.decideProgram(ps, []int{0, 1}, 210); !ok {
		t.Fatal("gap-out after a renewal blocked by a restarted min-green clock")
	}
}

// TestContestedSwitchMinGreen: an expired on-call budget does NOT let
// the controller cut a green the schedule rotated into seconds ago —
// callSince measures the call's age, not the phase's. The contested
// switch is gated on phase age ≥ min-green, same as the gap-out branch.
func TestContestedSwitchMinGreen(t *testing.T) {
	c := testController()
	ps := testProgState() // fixed time; phase 0 runs [0,100) then [220,320)
	ps.callSince = 10     // a call that has been waiting far past the 200-tick cap
	// Tick 250: fixed phase 0 began at 220 — 30 ticks old, under the
	// 50-tick test min-green. Served presence AND the waiting call.
	if _, _, ok := c.decideProgram(ps, []int{1, 1}, 250); ok {
		t.Fatal("contested switch cut a 30-tick-old green (min-green not consulted)")
	}
	// At 280 the phase is 60 ticks old: the expired cap serves the call —
	// through the walk, so the first command is the transition at its
	// natural 10 ticks.
	if ph, hold, ok := c.decideProgram(ps, []int{1, 1}, 280); !ok || ph != 1 || hold != 10 {
		t.Fatalf("contested switch past min-green: (%d, %d, %v), want (1, 10, true)", ph, hold, ok)
	}
}

// TestChainBoundaryEquality: the kernel continues a chain iff
// last.until ≥ the applied tick — so a renewal with applied == holdUntil
// CONTINUES the chain, and the controller's bookkeeping must match
// (otherwise it believes a fresh chain started and would issue exactly
// the doomed renewal the design forbids).
func TestChainBoundaryEquality(t *testing.T) {
	c := testController()
	ps := testProgState()
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 101, 50
	c.send(ps, 0, c.cfg.HoldTicks, 100) // applies at 101 == holdUntil
	if ps.chainStart != 50 {
		t.Fatalf("chainStart = %d, want 50 — applied == holdUntil continues the chain", ps.chainStart)
	}
	if c.Takeovers != 0 || c.Renews != 1 {
		t.Fatalf("counters takeovers=%d renews=%d, want 0/1", c.Takeovers, c.Renews)
	}
	// One tick later IS a new chain.
	c.send(ps, 0, c.cfg.HoldTicks, 101) // applies at 102 > holdUntil was 101+100=201... no:
	// (after the previous send holdUntil = 201; applied 102 < 201 — a
	// renewal, not a new chain)
	if ps.chainStart != 50 {
		t.Fatalf("chainStart = %d, want 50 (still inside the hold)", ps.chainStart)
	}
}

// TestChunkAccumulator: partial or out-of-order generations never
// install; a complete 1..n generation does; the previously installed
// table survives a broken generation.
func TestChunkAccumulator(t *testing.T) {
	c := testController()
	msg := func(chunkHdr string) *nats.Msg {
		m := &nats.Msg{Data: testSigFrame(t)}
		if chunkHdr != "" {
			m.Header = nats.Header{"sig_chunk": []string{chunkHdr}}
		}
		return m
	}
	// An orphaned final chunk (its generation's earlier chunks never
	// arrived) installs nothing.
	c.onSigTable(msg("2/2"))
	if len(c.progs) != 0 {
		t.Fatalf("orphaned final chunk installed %d programs", len(c.progs))
	}
	// A lone 1/2 installs nothing.
	c.onSigTable(msg("1/2"))
	if len(c.progs) != 0 {
		t.Fatalf("partial generation installed %d programs", len(c.progs))
	}
	// A complete 1/2 + 2/2 installs.
	c.onSigTable(msg("2/2"))
	if len(c.progs) != 1 {
		t.Fatalf("complete generation installed %d programs, want 1", len(c.progs))
	}
	// A broken later generation leaves the installed table alone.
	c.onSigTable(msg("2/3"))
	if len(c.progs) != 1 {
		t.Fatalf("broken generation disturbed the installed table (%d programs)", len(c.progs))
	}
}

// TestDetectorExtent: the scan neighborhood follows the radius — a
// detector 75 m out in cell ±2 is found at radius 75 and missed at
// radius 25.
func TestDetectorExtent(t *testing.T) {
	for _, tc := range []struct {
		radius float64
		want   int
	}{
		{75, 1}, {25, 0},
	} {
		cfg := testConfig()
		cfg.DetectRadiusM = tc.radius
		c := &Controller{cfg: cfg, log: discardLog, progs: map[string]*progState{},
			grid: map[[2]int][]*Detector{}, geom: testGeom()}
		p := testProgram()
		ps := newProgState(p, c.geom)
		det := &Detector{Link: 0, X: 124, Y: 0, Dx: 1, progID: "J"}
		ps.detectors[0] = det
		c.progs["J"] = ps
		c.grid[[2]int{2, 0}] = []*Detector{det}
		// Vehicle at x=49 (cell 0): 75 m behind the detector's stop line
		// (cell 2) — inside a 75 m radius, invisible to a ±1-cell scan.
		f := natsio.Frame{Tick: 1, Vehicles: []natsio.FrameVehicle{{ID: 1, X: 49, Y: 0}}}
		got := c.presenceCounts(f)[ps]
		if tc.want == 1 && (got == nil || got[0] != 1) {
			t.Errorf("radius %v: detector at 75 m not found (count %v)", tc.radius, got)
		}
		if tc.want == 0 && got != nil && got[0] != 0 {
			t.Errorf("radius %v: count %d, want 0 (out of range)", tc.radius, got[0])
		}
	}
}

// The estimated-phase bookkeeping: my hold covers its span, the fixed-time
// derivation takes over past it — the controller's local answer to the
// missing live override echo.
func TestEstimatedPhase(t *testing.T) {
	ps := testProgState()
	if ph, since := ps.estimated(105); ph != 1 || since != 100 {
		t.Errorf("fixed-time estimate at 105: (%d, %d), want (1, 100)", ph, since)
	}
	ps.myPhase, ps.phaseSince, ps.holdUntil = 0, 50, 150
	if ph, since := ps.estimated(105); ph != 0 || since != 50 {
		t.Errorf("held estimate at 105: (%d, %d), want (0, 50)", ph, since)
	}
	if ph, _ := ps.estimated(150); ph != 2 {
		t.Errorf("post-lapse estimate at 150: %d, want 2 (fixed-time resumed)", ph)
	}
}

// TestWalkSeamless: from the switch decision to target arrival, every
// tick is under a commanded phase — each walk step's verb applies exactly
// when its predecessor's hold expires (no fixed-time fallthrough, no
// per-step lapse), and transition/intermediate steps carry their natural
// durations. The program walks movement → 10-tick yellow → 5-tick
// all-red → target.
func TestWalkSeamless(t *testing.T) {
	c := testController()
	p := natsio.SigProgram{ID: "W", Phases: []natsio.SigPhase{
		{DurationTicks: 100, State: "Gr"},
		{DurationTicks: 10, State: "yr"},
		{DurationTicks: 5, State: "rr"},
		{DurationTicks: 100, State: "rG"},
		{DurationTicks: 10, State: "ry"},
	}}
	ps := newProgState(p, &Geom{ByProgram: map[string][]*Detector{
		"W": {{Link: 0, X: 0, Y: 0, Dx: 1}, {Link: 1, X: 50, Y: 0, Dx: 1}},
	}})
	if ps == nil {
		t.Fatal("program W not actuatable")
	}
	ps.myPhase, ps.phaseSince, ps.holdUntil, ps.chainStart = 0, 100, 200, 100
	count := []int{0, 1}

	wantSteps := []struct {
		phase int
		hold  uint64
	}{{1, 10}, {2, 5}, {3, c.cfg.HoldTicks}}
	step := 0
	prevUntil := uint64(0)
	for tick := uint64(160); ; tick++ {
		if tick > 400 {
			t.Fatal("walk never arrived")
		}
		ph, hold, ok := c.decideProgram(ps, count, tick)
		if ps.target == -1 {
			break // arrival cleared the walk; anything after is normal logic
		}
		if !ok {
			continue
		}
		if prevUntil != 0 && tick+1 != prevUntil {
			t.Fatalf("tick %d: step applies at %d, previous hold ended %d — fixed-time fallthrough (and a lapse) inside the walk", tick, tick+1, prevUntil)
		}
		if step >= len(wantSteps) {
			t.Fatalf("extra walk step (%d, %d)", ph, hold)
		}
		if ph != wantSteps[step].phase || hold != wantSteps[step].hold {
			t.Fatalf("walk step %d: (%d, %d), want (%d, %d)", step, ph, hold,
				wantSteps[step].phase, wantSteps[step].hold)
		}
		c.send(ps, ph, hold, tick)
		prevUntil = ps.holdUntil
		step++
	}
	if step != len(wantSteps) {
		t.Fatalf("%d walk steps, want %d", step, len(wantSteps))
	}
}

// TestDueNow: the cadence gate lifts mid-walk (a walk step must be issued
// the tick its predecessor expires) and applies otherwise.
func TestDueNow(t *testing.T) {
	c := testController()
	ps := testProgState()
	ps.lastDecide = 100
	if c.dueNow(ps, 110) { // 10 < cadence 20
		t.Error("not walking: decided inside the cadence window")
	}
	if !c.dueNow(ps, 120) {
		t.Error("not walking: not due at the cadence boundary")
	}
	ps.target = 2
	ps.lastDecide = 119
	if !c.dueNow(ps, 120) {
		t.Error("walking: cadence-gated mid-walk")
	}
}

// TestAwaitTable: attach's readiness is the table, not the flush.
func TestAwaitTable(t *testing.T) {
	c := testController()
	if err := c.awaitTable(10 * time.Millisecond); err == nil {
		t.Fatal("awaitTable succeeded with no table installed")
	}
	go c.installTable(nil) // a complete (empty) generation
	if err := c.awaitTable(5 * time.Second); err != nil {
		t.Fatalf("awaitTable after install: %v", err)
	}
}

// TestRequestIDDiscriminator: two controller processes on one run never
// share an idempotency key.
func TestRequestIDDiscriminator(t *testing.T) {
	c1, c2 := testController(), testController()
	c1.ctlID, c2.ctlID = "ctl-1", "ctl-2"
	ps1, ps2 := testProgState(), testProgState()
	ps1.seq, ps2.seq = 1, 1
	id1, id2 := c1.requestID(ps1), c2.requestID(ps2)
	if id1 == id2 {
		t.Fatalf("identical request ids from two processes: %q", id1)
	}
	if !strings.Contains(id1, "ctl-1") || !strings.Contains(id1, "-000001") {
		t.Errorf("request id %q not in sigctl-{ctl}-{prog}-{seq} form", id1)
	}
}

// TestEstimatedOffsetWrap: an offset program's wrapped phase began before
// tick 0 — the onset saturates at 0 instead of underflowing, so min-green
// can pass in the run's first offset ticks.
func TestEstimatedOffsetWrap(t *testing.T) {
	p := testProgram()
	p.OffsetTicks = 5
	ps := testProgState()
	ps.prog = p
	if _, since := ps.estimated(2); since != 0 {
		t.Errorf("estimated(2) since = %d, want 0 (saturated)", since)
	}
	// Sanity: past the offset the onset is exact again (phase 0 begins at
	// the offset tick 5, so phase 1 [105,115) begins at 105).
	if _, since := ps.estimated(106); since != 105 {
		t.Errorf("estimated(106) since = %d, want 105", since)
	}
}
