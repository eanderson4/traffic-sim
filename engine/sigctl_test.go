package engine

import (
	"fmt"
	"strings"
	"testing"
)

// sigctl_test.go — kernel side of runtime signal control (ADR-0037,
// milestone 1): the signal_set verb's validation and rails, the override
// derivation in sigState, determinism under a verb schedule, keyframe
// restore bit-exactness across a held phase, the keyframe version floor
// (no-verb runs marshal exactly as before), and the guarantee that a held
// green still cannot enter a blocked box. Fixtures reuse signal_test.go's
// compiled-NetFile idiom (sigNetFile/sigSpec); the two-approach junction
// below is the smallest network with a movement to starve and a cross
// movement to keep flowing.

// sig2NetFile is a signalized junction J with two independent movements:
// approach nA_0 feeds internal lane iJ_0 (link 0) draining to exit nX_0,
// approach nB_0 feeds iJ_1 (link 1) draining to nY_0. No shared exits and
// no declared foes: the fixture is about queues and holds, not conflicts.
func sig2NetFile(phases []NetSignalPhase) *NetFile {
	link0, link1 := 0, 1
	return &NetFile{
		Version: 1,
		Name:    "sig2",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 300, SpeedLimit: 13.89, Origin: true, Successors: []string{"iJ_0"}},
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", TL: "J", TLLink: &link0, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
			{ID: "nB_0", Section: "B", Length: 300, SpeedLimit: 13.89, Origin: true, Successors: []string{"iJ_1"}},
			{ID: "iJ_1", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J", TL: "J", TLLink: &link1, Successors: []string{"nY_0"}},
			{ID: "nY_0", Section: "Y", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
		Signals: []NetSignal{{ID: "J", Junction: "J", Offset: 0, Phases: phases}},
	}
}

// laneOf is shared test plumbing (defined alongside the other fixtures);
// queueOn counts the vehicles standing or rolling on one lane.
func queueOn(e *Engine, lane *Lane) int {
	n := 0
	for _, v := range e.Vehicles() {
		if v.Lane == lane {
			n++
		}
	}
	return n
}

// TestSignalSetChainClamp: the starvation bound is CUMULATIVE across an
// uninterrupted same-phase hold chain — a controller renewing the same
// phase every 100 ticks gets clamped at first-hold-start + bound, and
// exactly one lapse event fires there. (Program cycle 200 ticks; the held
// phase 2 is red where the schedule would be green at the bound, so the
// resume is visible.)
func TestSignalSetChainClamp(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")
	bound := uint64(e.signalHoldMaxTicks()) // 3000 at dt 0.1
	chainEnd := 5 + bound                   // the chain starts at tick 5
	// Renew the same phase every 100 ticks, each asking for 500: without
	// the chain clamp the horizon would slide forever.
	var lapses []SigLapse
	for e.Tick < chainEnd+200 {
		if (e.Tick-4)%100 == 0 { // applies at 5, 105, …, 3005
			if err := e.EnqueueSignal(SignalDirective{RequestID: fmt.Sprintf("r%d", e.Tick), Signal: "J", Phase: 2, HoldTicks: 500}); err != nil {
				t.Fatal(err)
			}
		}
		e.Step()
		lapses = append(lapses, e.LapsedSignals()...)
		if len(lapses) > 1 {
			t.Fatalf("tick %d: %d lapse events, want at most 1", e.Tick, len(lapses))
		}
		if e.Tick == chainEnd-1 {
			if st := e.sigState(l); st != SigRed {
				t.Fatalf("tick %d: state %v — the chain should still be held at its last tick", e.Tick, st)
			}
		}
		if e.Tick == chainEnd {
			if st := e.sigState(l); st == SigRed {
				t.Fatalf("tick %d: still held — the renewals extended past the chain bound", e.Tick)
			}
		}
	}
	if len(lapses) != 1 {
		t.Fatalf("%d lapse events, want exactly 1", len(lapses))
	}
	if lapses[0].Since != 5 || lapses[0].Until != chainEnd || lapses[0].Phase != 2 {
		t.Errorf("lapse = %+v, want chain [5, %d) phase 2", lapses[0], chainEnd)
	}
	// The record carries the EFFECTIVE hold: the renewal applied at 2905
	// asked for 500 but was chain-clamped to 100, and the renewal at the
	// bound (3005) enforced nothing — recorded as an explicit 0. (The
	// renewals at 3105+ start a NEW chain after the lapse's fixed-time
	// gap, so locate the two by their applied ticks.)
	recorded := map[uint64]uint64{}
	for _, d := range e.SigLog {
		recorded[d.Tick] = d.HoldTicks
	}
	if got := recorded[chainEnd-100]; got != 100 {
		t.Errorf("clamped renewal recorded hold %d, want 100 (3005 − 2905)", got)
	}
	if got := recorded[chainEnd]; got != 0 {
		t.Errorf("declined-at-bound renewal recorded hold %d, want 0 (accepted, enforced nothing)", got)
	}
}

// TestSignalSetRenewalAtBound: a renewal landing exactly AT the chain's
// bound extends nothing and the lapse still fires exactly once (the
// renewal is accepted and recorded — the command reached the engine — but
// the rail declines to install it).
func TestSignalSetRenewalAtBound(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")
	bound := uint64(e.signalHoldMaxTicks())
	for e.Tick < 4 {
		e.Step()
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: "h1", Signal: "J", Phase: 2, HoldTicks: bound}); err != nil {
		t.Fatal(err)
	}
	chainEnd := uint64(5) + bound // applied at 5, clamped to [5, 3005)
	var lapses []SigLapse
	for e.Tick < chainEnd+50 {
		if e.Tick == chainEnd-1 {
			// Applies at chainEnd — exactly the bound.
			if err := e.EnqueueSignal(SignalDirective{RequestID: "h2", Signal: "J", Phase: 2, HoldTicks: 500}); err != nil {
				t.Fatal(err)
			}
		}
		e.Step()
		lapses = append(lapses, e.LapsedSignals()...)
		if e.Tick == chainEnd+10 {
			// No live hold past the bound: the only entry is the lapsed
			// one, retained for the clearance window.
			if h := e.sigOv["J"]; len(h) != 1 || h[0].until != chainEnd {
				t.Fatalf("tick %d: history = %+v, want just the lapsed entry", e.Tick, h)
			}
		}
	}
	if len(lapses) != 1 || lapses[0].Until != chainEnd || lapses[0].Since != 5 {
		t.Fatalf("lapses = %+v, want exactly one at the bound %d (chain start 5)", lapses, chainEnd)
	}
	if len(e.SigLog) != 2 {
		t.Fatalf("SigLog %d, want 2 — the renewal at the bound is still recorded as applied", len(e.SigLog))
	}
	if got := e.SigLog[1].HoldTicks; got != 0 {
		t.Errorf("declined renewal recorded hold %d, want 0 (accepted, enforced nothing)", got)
	}
	// No live hold past the bound: the lapsed entry swept itself after its
	// retention window, leaving the table empty.
	if h := e.sigOv["J"]; len(h) != 0 {
		t.Fatalf("history = %+v at run end, want empty (lapsed entry swept)", h)
	}
	if st := e.sigState(l); st == SigRed {
		t.Errorf("tick %d: still red past the bound — the renewal extended the chain", e.Tick)
	}
}

// TestSignalSetAlternatingPhasesNotClamped: alternating between DIFFERENT
// phases is real control, not a renewal — each command starts a new
// chain, so a long alternating program never trips the cumulative bound
// and never fires a lapse.
func TestSignalSetAlternatingPhasesNotClamped(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")
	bound := uint64(e.signalHoldMaxTicks())
	verbs := 0
	for e.Tick < bound+1000 {
		if (e.Tick-4)%100 == 0 { // applies at 5, 105, …
			phase := (verbs % 2) * 2 // 0, 2, 0, 2, …
			if err := e.EnqueueSignal(SignalDirective{RequestID: fmt.Sprintf("a%d", e.Tick), Signal: "J", Phase: phase, HoldTicks: 500}); err != nil {
				t.Fatal(err)
			}
			verbs++
		}
		e.Step()
		if len(e.LapsedSignals()) != 0 {
			t.Fatalf("tick %d: lapse %+v — alternating phases must not be chain-clamped", e.Tick, e.LapsedSignals())
		}
	}
	// Well past what a single chain's bound would allow (3005), the last
	// command is still in force: the run ends mid-hold by construction.
	if st := e.sigState(l); st != SigGreen && st != SigRed {
		t.Fatalf("tick %d: state %v — the alternating program lost control", e.Tick, st)
	}
	if len(e.sigOv["J"]) == 0 {
		t.Fatal("no override in force at run end — the alternating program was clamped")
	}
}

// TestSignalSetDuplicateAfterRestore: duplicate detection rides the
// keyframed override history, so a warm restore does not open an
// idempotency window — the contract layer's reply cache (Contract.verbs)
// is per-process and empty after a restore, and a retried request_id must
// still be rejected rather than re-applying the command (which would
// reset since/until and change the trajectory).
func TestSignalSetDuplicateAfterRestore(t *testing.T) {
	nf := sig2NetFile([]NetSignalPhase{{30.0, "Gr"}, {30.0, "rG"}})
	spec := sigSpec(t, nf, 0)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 4 {
		e.Step()
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: "x", Signal: "J", Phase: 1, HoldTicks: 500}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 100 { // mid-hold ([5, 505))
		e.Step()
	}
	kf, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreState(spec, kf)
	if err != nil {
		t.Fatal(err)
	}
	before := append([]sigOverride(nil), restored.sigOv["J"]...)
	if len(before) != 1 {
		t.Fatalf("restored history = %+v, want the one held override", before)
	}
	err = restored.EnqueueSignal(SignalDirective{RequestID: "x", Signal: "J", Phase: 1, HoldTicks: 500})
	if err == nil || !strings.Contains(err.Error(), "duplicate signal request id") {
		t.Fatalf("retried request_id after restore: err = %v, want the duplicate rejection", err)
	}
	after := restored.sigOv["J"]
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("override changed by the rejected retry: before %+v, after %+v", before, after)
	}
}

// TestRequestIDBoundKernel: the u16 length-prefix bound (TSKF director
// queue v3, signal overrides v7) is enforced at the KERNEL enqueue, not
// only at the NATS boundary — every caller is guarded, or an over-long id
// would marshal a truncated length and corrupt the keyframe.
func TestRequestIDBoundKernel(t *testing.T) {
	e, err := NewEngine(sigSpec(t, sig2NetFile([]NetSignalPhase{{30.0, "Gr"}, {30.0, "rG"}}), 10))
	if err != nil {
		t.Fatal(err)
	}
	tooLong := strings.Repeat("x", MaxRequestIDBytes+1)
	atBound := strings.Repeat("x", MaxRequestIDBytes)
	if err := e.EnqueueSignal(SignalDirective{RequestID: tooLong, Signal: "J", Phase: 0}); err == nil ||
		!strings.Contains(err.Error(), "too long") {
		t.Errorf("EnqueueSignal over-long id: err = %v, want the length rejection", err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: tooLong, Origin: "nA_0", TypeName: "car"}); err == nil ||
		!strings.Contains(err.Error(), "too long") {
		t.Errorf("EnqueueSpawn over-long id: err = %v, want the length rejection", err)
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: atBound, Signal: "J", Phase: 0}); err != nil {
		t.Errorf("EnqueueSignal at the exact bound: %v, want accepted", err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: atBound, Origin: "nA_0", TypeName: "car"}); err != nil {
		t.Errorf("EnqueueSpawn at the exact bound: %v, want accepted", err)
	}
}

// TestSignalSetSameBoundarySupersede: two verbs for the same program
// applying at one boundary — the second supersedes the first before it
// governs a single tick. The first was accepted and is logged, so its
// record entry must show what was enforced: hold 0, the
// accepted-but-enforced-nothing marker (the declined-renewal semantics).
func TestSignalSetSameBoundarySupersede(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")
	for e.Tick < 4 {
		e.Step()
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: "v1", Signal: "J", Phase: 2, HoldTicks: 100}); err != nil {
		t.Fatal(err)
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: "v2", Signal: "J", Phase: 0, HoldTicks: 40}); err != nil {
		t.Fatal(err)
	}
	e.Step() // tick 5: both apply
	applied := e.AppliedSignals()
	if len(applied) != 2 {
		t.Fatalf("%d directives applied, want 2", len(applied))
	}
	if applied[0].RequestID != "v1" || applied[0].HoldTicks != 0 {
		t.Errorf("superseded verb recorded as %+v, want v1 with hold 0 (enforced nothing)", applied[0])
	}
	if applied[1].RequestID != "v2" || applied[1].HoldTicks != 40 {
		t.Errorf("superseding verb recorded as %+v, want v2 with its effective hold 40", applied[1])
	}
	if e.SigLog[0].HoldTicks != 0 || e.SigLog[1].HoldTicks != 40 {
		t.Errorf("SigLog = %+v, want the same 0/40 stamping", e.SigLog)
	}
	// Only the second override exists, and it governs.
	h := e.sigOv["J"]
	if len(h) != 1 || h[0].phase != 0 || h[0].since != 5 || h[0].until != 45 {
		t.Fatalf("history = %+v, want just v2's [5, 45)", h)
	}
	if st := e.sigState(l); st != SigGreen {
		t.Errorf("tick 5: state %v, want v2's held green", st)
	}
}

// TestSignalSetValidation: the verb's accept/reject surface — unknown
// program, phase out of range, the default hold (one cycle), the clamp at
// the starvation bound, and same-boundary request-id dedup.
func TestSignalSetValidation(t *testing.T) {
	nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0) // cycle 20 ticks
	e, err := NewEngine(sigSpec(t, nf, 10))
	if err != nil {
		t.Fatal(err)
	}

	if err := e.EnqueueSignal(SignalDirective{RequestID: "x", Signal: "nope", Phase: 0}); err == nil ||
		!strings.Contains(err.Error(), "unknown signal program") {
		t.Errorf("unknown program: err = %v", err)
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: "x", Signal: "J", Phase: 3}); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Errorf("phase past the list: err = %v", err)
	}
	if err := e.EnqueueSignal(SignalDirective{RequestID: "x", Signal: "J", Phase: -1}); err == nil ||
		!strings.Contains(err.Error(), "out of range") {
		t.Errorf("negative phase: err = %v", err)
	}

	// Default hold: one cycle of the commanded program (20 ticks here).
	if err := e.EnqueueSignal(SignalDirective{RequestID: "d", Signal: "J", Phase: 2}); err != nil {
		t.Fatalf("default-hold verb: %v", err)
	}
	// Same-boundary duplicate request id rejects (the contract layer dedups
	// first; this is the kernel's own backstop, EnqueueSpawn's precedent).
	if err := e.EnqueueSignal(SignalDirective{RequestID: "d", Signal: "J", Phase: 1}); err == nil ||
		!strings.Contains(err.Error(), "duplicate signal request id") {
		t.Errorf("same-boundary duplicate: err = %v", err)
	}
	// A hold past the bound is clamped, not rejected.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "c", Signal: "J", Phase: 1, HoldTicks: 1 << 40}); err != nil {
		t.Fatalf("clamped verb: %v", err)
	}
	e.Step()
	applied := e.AppliedSignals()
	if len(applied) != 2 {
		t.Fatalf("%d directives applied, want 2", len(applied))
	}
	if applied[0].Tick != 1 || applied[0].HoldTicks != 0 {
		t.Errorf("default hold: applied tick %d hold %d, want tick 1 hold 0 — the same-boundary supersede below dropped it before it governed a tick (the effective-hold record shows what was enforced: nothing)",
			applied[0].Tick, applied[0].HoldTicks)
	}
	// The default hold itself, on an installed verb: one cycle of the
	// commanded program (20 ticks here).
	eDef, err := NewEngine(sigSpec(t, nf, 10))
	if err != nil {
		t.Fatal(err)
	}
	if err := eDef.EnqueueSignal(SignalDirective{RequestID: "d", Signal: "J", Phase: 2}); err != nil {
		t.Fatal(err)
	}
	eDef.Step()
	if got := eDef.AppliedSignals()[0].HoldTicks; got != 20 {
		t.Errorf("installed default hold = %d, want 20 (one cycle)", got)
	}
	// The clamp bound is SIM seconds compiled onto the run's tick grid:
	// 300 s at this spec's dt (0.1) is 3000 ticks.
	maxHold := e.signalHoldMaxTicks()
	if maxHold != 3000 {
		t.Fatalf("hold bound at dt 0.1 = %d ticks, want 3000 (300 s)", maxHold)
	}
	if applied[1].HoldTicks != maxHold {
		t.Errorf("clamped hold = %d, want %d", applied[1].HoldTicks, maxHold)
	}
	// Two live verbs for the same signal in one boundary: the last wins
	// (wholesale replacement — and the empty-interval loser is dropped,
	// having governed no tick), here the clamped phase-1 hold.
	hist, ok := e.sigOv["J"]
	if !ok || len(hist) != 1 {
		t.Fatalf("override history = %v, want exactly one entry (the same-boundary loser governed no tick)", hist)
	}
	ov := hist[0]
	if ov.phase != 1 || ov.since != 1 || ov.until != 1+maxHold {
		t.Errorf("override = {phase %d, [%d, %d)}, want {phase 1, [1, %d)}",
			ov.phase, ov.since, ov.until, 1+maxHold)
	}

	// The bound tracks dt: at dt 0.25 the same 300 sim-seconds is 1200
	// ticks, and the clamp follows.
	spec25 := sigSpec(t, nf, 10)
	spec25.Params.Dt = 0.25
	e25, err := NewEngine(spec25)
	if err != nil {
		t.Fatal(err)
	}
	if got := e25.signalHoldMaxTicks(); got != 1200 {
		t.Fatalf("hold bound at dt 0.25 = %d ticks, want 1200 (300 s)", got)
	}
	if err := e25.EnqueueSignal(SignalDirective{RequestID: "c", Signal: "J", Phase: 0, HoldTicks: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	e25.Step()
	if got := e25.AppliedSignals()[0].HoldTicks; got != 1200 {
		t.Errorf("dt 0.25: clamped hold = %d ticks, want 1200", got)
	}
}

// TestSignalSetOverridesState: a commanded phase replaces the fixed-time
// derivation for exactly the hold's span — including through a fixed-time
// phase change that never shows — and the lapse returns the program to the
// fixed-time schedule at the tick the schedule itself dictates.
func TestSignalSetOverridesState(t *testing.T) {
	// Green [0,100), amber [100,130), red [130,200), wrapping.
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {3.0, "y"}, {7.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")

	// Hold RED from tick 1 for 150 ticks — straight through the fixed-time
	// green [0,100) of the first full cycle — lapsing at 151, where the
	// schedule itself sits in red [130,200): the light stays red across the
	// lapse, so the EVENT is the proof the rail fired.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "h1", Signal: "J", Phase: 2, HoldTicks: 150}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 250 {
		e.Step()
		switch {
		case e.Tick >= 1 && e.Tick <= 150:
			if st := e.sigState(l); st != SigRed {
				t.Fatalf("tick %d: state %v during hold, want red", e.Tick, st)
			}
		case e.Tick == 151:
			// Lapse: the fixed-time program resumes wherever the tick puts
			// it — red here, so no visible change; the EVENT is the proof.
			lapses := e.LapsedSignals()
			if len(lapses) != 1 || lapses[0].Signal != "J" || lapses[0].Phase != 2 ||
				lapses[0].Since != 1 || lapses[0].Until != 151 || lapses[0].RequestID != "h1" {
				t.Fatalf("lapse event = %+v, want one J/phase-2/[1,151)/h1 lapse", lapses)
			}
		default:
			if len(e.LapsedSignals()) != 0 {
				t.Fatalf("tick %d: unexpected lapse %+v", e.Tick, e.LapsedSignals())
			}
		}
	}
	// Post-lapse the derivation is the schedule's again: tick 250 is 50
	// ticks into cycle 2's green [200,300).
	if st := e.sigState(l); st != SigGreen {
		t.Errorf("tick %d: state %v after lapse, want green (fixed-time resumed)", e.Tick, st)
	}
}

// TestSignalSetDeterminism: two runs with the same verb schedule produce
// identical per-tick CRCs — the verb's effect is fixed by its applied
// tick, nothing else — and re-derive the identical lapse events (the
// record-plane signal_lapse is emitted from exactly these).
func TestSignalSetDeterminism(t *testing.T) {
	nf := sig2NetFile([]NetSignalPhase{{30.0, "Gr"}, {30.0, "rG"}})
	run := func() ([]uint64, []SigLapse) {
		spec := sigSpec(t, nf, 1200)
		spec.Scen = Scenario{SpawnRates: map[string]float64{"nA_0": 900, "nB_0": 600}}
		e, err := NewEngine(spec)
		if err != nil {
			t.Fatal(err)
		}
		var lapses []SigLapse
		for e.Tick < spec.Ticks {
			switch e.Tick {
			case 4: // applies at tick 5
				if err := e.EnqueueSignal(SignalDirective{RequestID: "h1", Signal: "J", Phase: 1, HoldTicks: 500}); err != nil {
					t.Fatal(err)
				}
			case 700: // a second command (default hold), applies at 701
				if err := e.EnqueueSignal(SignalDirective{RequestID: "h2", Signal: "J", Phase: 0}); err != nil {
					t.Fatal(err)
				}
			}
			e.Step()
			lapses = append(lapses, e.LapsedSignals()...)
			assertNoNaN(t, e)
		}
		if e.Stats.Collisions != 0 {
			t.Fatalf("%d collision observations, want 0", e.Stats.Collisions)
		}
		return e.CRCs, lapses
	}
	a, la := run()
	b, lb := run()
	if len(a) != len(b) {
		t.Fatalf("crc counts %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same schedule diverged at tick %d: crc %016x vs %016x", i+1, a[i], b[i])
		}
	}
	if len(la) != 1 || la[0].Until != 505 { // h1's bound: applied 5 + 500
		t.Fatalf("lapses = %+v, want the single h1 hold lapsing at 505", la)
	}
	if len(la) != len(lb) {
		t.Fatalf("lapse counts %d vs %d", len(la), len(lb))
	}
	for i := range la {
		if la[i] != lb[i] {
			t.Fatalf("lapse %d re-derived differently: %+v vs %+v", i, la[i], lb[i])
		}
	}
}

// TestSignalSetKeyframeResume: a keyframe taken mid-hold restores the
// override bit-exactly (TSKF v7) — the continued run matches the
// uninterrupted one's CRCs, across the lapse and after. Both flag regimes:
// a flag-off run holding an override marshals v7 WITHOUT the v6 lane
// section (the flags word carries presence now), and restores flag-off.
func TestSignalSetKeyframeResume(t *testing.T) {
	nf := sig2NetFile([]NetSignalPhase{{30.0, "Gr"}, {30.0, "rG"}})
	for _, adaptive := range []bool{true, false} {
		spec := sigSpec(t, nf, 1200)
		spec.Scen = Scenario{SpawnRates: map[string]float64{"nA_0": 900, "nB_0": 600}}
		spec.Params.AdaptiveRouting = adaptive

		full, err := NewEngine(spec)
		if err != nil {
			t.Fatal(err)
		}
		for full.Tick < 4 {
			full.Step()
		}
		if err := full.EnqueueSignal(SignalDirective{RequestID: "h1", Signal: "J", Phase: 1, HoldTicks: 500}); err != nil {
			t.Fatal(err)
		}
		for full.Tick < 250 { // mid-hold (hold spans [5, 505))
			full.Step()
		}
		kf, err := full.MarshalState()
		if err != nil {
			t.Fatalf("MarshalState: %v", err)
		}
		if ver := keyframeVersionOf(kf); ver != keyframeSignalVersion {
			t.Fatalf("adaptive=%v: mid-hold keyframe is v%d, want v%d", adaptive, ver, keyframeSignalVersion)
		}
		for full.Tick < spec.Ticks {
			full.Step()
		}

		restored, err := RestoreState(spec, kf)
		if err != nil {
			t.Fatalf("adaptive=%v: RestoreState: %v", adaptive, err)
		}
		if got := len(restored.sigOv); got != 1 {
			t.Fatalf("adaptive=%v: restored with %d overrides, want 1", adaptive, got)
		}
		for restored.Tick < spec.Ticks {
			restored.Step()
		}
		if len(restored.CRCs) != len(full.CRCs[250:]) {
			t.Fatalf("adaptive=%v: restored run has %d CRCs, want %d", adaptive, len(restored.CRCs), len(full.CRCs[250:]))
		}
		for i := range restored.CRCs {
			if restored.CRCs[i] != full.CRCs[250+i] {
				t.Fatalf("adaptive=%v: post-restore divergence at tick %d: crc %016x, want %016x",
					adaptive, 251+i, restored.CRCs[i], full.CRCs[250+i])
			}
		}
		// The hold lapsed at 505 in both; the table swept itself after the
		// clearance retention, so a keyframe past that is back below v7.
		kf2, err := full.MarshalState()
		if err != nil {
			t.Fatal(err)
		}
		if ver := keyframeVersionOf(kf2); ver >= keyframeSignalVersion {
			t.Errorf("adaptive=%v: post-lapse keyframe is v%d, want below v%d (table empty)",
				adaptive, ver, keyframeSignalVersion)
		}
	}
}

// TestSignalKeyframeVersionFloor: a run that never receives a signal verb
// marshals at exactly the pre-ADR-0037 version for its state — v2 for a
// plain flag-off fixture, v6 with adaptive routing on — and a held
// override is the only thing that lifts a keyframe to v7.
func TestSignalKeyframeVersionFloor(t *testing.T) {
	nf := sig2NetFile([]NetSignalPhase{{30.0, "Gr"}, {30.0, "rG"}})

	plain := sigSpec(t, nf, 50)
	plain.Params.AdaptiveRouting = false
	e, err := NewEngine(plain)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 50 {
		e.Step()
	}
	kf, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if ver := keyframeVersionOf(kf); ver != 2 {
		t.Errorf("no-verb flag-off keyframe is v%d, want v2 (the pre-ADR-0037 bytes)", ver)
	}

	adaptive := sigSpec(t, nf, 50) // AdaptiveRouting on by default
	ea, err := NewEngine(adaptive)
	if err != nil {
		t.Fatal(err)
	}
	for ea.Tick < 50 {
		ea.Step()
	}
	kfa, err := ea.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if ver := keyframeVersionOf(kfa); ver != keyframeAdaptiveVersion {
		t.Errorf("no-verb flag-on keyframe is v%d, want v%d", ver, keyframeAdaptiveVersion)
	}
}

// TestSignalSetLaneSectionGuard: a v7 payload written flag-ON carries the
// adaptive lane section and must refuse a flag-OFF restore (the state the
// run's routing depends on would be silently dropped); a v7 payload
// written flag-OFF restores flag-ON through the migration notice, exactly
// like a pre-v6 payload — and with the same dwell-clock seeding: the
// flag-off run has no ttEMA, so its laneEntryTicks are not keyframed, and
// the restore seeds every dwell clock at the restore tick ("no evidence
// yet") rather than importing a pre-restore lane entry that would poison
// the vehicle's first post-restore dwell sample.
func TestSignalSetLaneSectionGuard(t *testing.T) {
	nf := sig2NetFile([]NetSignalPhase{{30.0, "Gr"}, {30.0, "rG"}})
	mid := func(spec RunSpec) []byte {
		e, err := NewEngine(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.EnqueueSignal(SignalDirective{RequestID: "h1", Signal: "J", Phase: 1, HoldTicks: 500}); err != nil {
			t.Fatal(err)
		}
		for e.Tick < 100 {
			e.Step()
		}
		if len(e.Vehicles()) == 0 {
			t.Fatal("no vehicles live at the keyframe — the dwell-clock assertions are vacuous")
		}
		kf, err := e.MarshalState()
		if err != nil {
			t.Fatal(err)
		}
		if ver := keyframeVersionOf(kf); ver != keyframeSignalVersion {
			t.Fatalf("mid-hold keyframe is v%d, want v%d", ver, keyframeSignalVersion)
		}
		return kf
	}

	on := sigSpec(t, nf, 0)
	on.Scen = Scenario{SpawnRates: map[string]float64{"nA_0": 1200, "nB_0": 1200}}
	off := sigSpec(t, nf, 0)
	off.Scen = on.Scen
	off.Params.AdaptiveRouting = false

	if _, err := RestoreState(off, mid(on)); err == nil ||
		!strings.Contains(err.Error(), "AdaptiveRouting off") {
		t.Errorf("flag-on v7 into flag-off spec: err = %v, want refusal", err)
	}
	e2, err := RestoreState(on, mid(off))
	if err != nil {
		t.Fatalf("flag-off v7 into flag-on spec: %v (the ADR-0036 migration path)", err)
	}
	if e2.RestoreNotice == "" {
		t.Error("flag-off v7 into flag-on spec: no RestoreNotice — the regime switch must be loud")
	}
	// The flag-off payload carried no dwell clocks: every vehicle's clock
	// seeds at the restore tick, so its first post-restore dwell sample is
	// bounded by the post-restore dwell — never a run-long poison sample
	// from a pre-restore lane entry.
	for _, v := range e2.Vehicles() {
		if v.laneEntryTick != 100 {
			t.Errorf("vehicle %d: laneEntryTick = %d after flag-off→flag-on restore, want the restore tick 100", v.ID, v.laneEntryTick)
		}
	}
	// Control: the flag-ON payload DOES carry the clocks — a flag-on
	// restore keeps the real (pre-restore) lane entries.
	e3, err := RestoreState(on, mid(on))
	if err != nil {
		t.Fatalf("flag-on v7 into flag-on spec: %v", err)
	}
	kept := false
	for _, v := range e3.Vehicles() {
		if v.laneEntryTick < 100 {
			kept = true
			break
		}
	}
	if !kept {
		t.Error("flag-on restore seeded every dwell clock at the restore tick — the keyframed laneEntryTicks were dropped")
	}
}

// TestSignalSetSupersedeClearanceAmber: a held AMBER superseded by a
// commanded red is an amber→red transition — the clearance window must
// apply, answered from the SUPERSEDED override's retained history, not
// from the fixed schedule (which is mid-green here and would miss it).
func TestSignalSetSupersedeClearanceAmber(t *testing.T) {
	// Green [0,100), amber [100,130), red [130,200); clearance 30 ticks.
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {3.0, "y"}, {7.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")

	for e.Tick < 4 {
		e.Step()
	}
	// Hold AMBER from tick 5 — while the fixed schedule is green.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-amber", Signal: "J", Phase: 1, HoldTicks: 100}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 10 {
		e.Step()
	}
	// Supersede with RED, applied at tick 11: the amber hold truncates to
	// [5,11) and the red hold begins.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-red", Signal: "J", Phase: 2, HoldTicks: 100}); err != nil {
		t.Fatal(err)
	}
	e.Step() // tick 11: the red onset
	if st := e.sigState(l); st != SigRed {
		t.Fatalf("tick 11: state %v, want held red", st)
	}
	// The red began at 11 directly after a HELD amber — the clearance
	// window must be open (the fixed schedule's green at onset−1 would
	// have closed it).
	if !e.sigInClearance(l) {
		t.Fatal("tick 11: no clearance after held-amber→red — the superseded amber was dropped from the lookback")
	}
	// The superseded entry is retained through the clearance window, fires
	// no lapse event (it ended by replacement, not by its bound)...
	if len(e.sigOv["J"]) != 2 {
		t.Fatalf("tick 11: history has %d entries, want 2 (superseded amber retained)", len(e.sigOv["J"]))
	}
	if len(e.LapsedSignals()) != 0 {
		t.Fatalf("tick 11: lapse %+v for a superseded hold — replacement is not a lapse", e.LapsedSignals())
	}
	// ...is still visible mid-window...
	for e.Tick < 20 {
		e.Step()
		if !e.sigInClearance(l) {
			t.Fatalf("tick %d: clearance closed early (elapsed %d ≤ 30)", e.Tick, e.Tick-11)
		}
	}
	// ...and lapses out of BOTH the window and the table on schedule:
	// clearance ends 30 ticks after the onset, retention at Until+30 = 41.
	for e.Tick < 45 {
		e.Step()
	}
	if e.sigInClearance(l) {
		t.Error("tick 45: clearance still open 34 ticks after the red onset")
	}
	if len(e.sigOv["J"]) != 1 {
		t.Fatalf("tick 45: history has %d entries, want 1 (superseded amber swept past its retention)", len(e.sigOv["J"]))
	}
	if st := e.sigState(l); st != SigRed {
		t.Errorf("tick 45: state %v, want still-held red", st)
	}
}

// TestSignalSetSupersedeClearanceGreen: a held GREEN superseded by a
// commanded red is a green→red transition — NO clearance, even though the
// fixed schedule happens to be amber at onset−1 (which would grant a
// spurious one if the lookback fell through to the schedule).
func TestSignalSetSupersedeClearanceGreen(t *testing.T) {
	// Green [0,100), amber [100,130), red [130,200).
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {3.0, "y"}, {7.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")

	for e.Tick < 4 {
		e.Step()
	}
	// Hold GREEN from tick 5, through the schedule's own amber [100,130).
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-green", Signal: "J", Phase: 0, HoldTicks: 200}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 109 {
		e.Step()
	}
	// Supersede with RED, applied at tick 110 — the fixed schedule is amber
	// at 109, so a lookback that fell through to the schedule would open a
	// spurious clearance window.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-red", Signal: "J", Phase: 2, HoldTicks: 50}); err != nil {
		t.Fatal(err)
	}
	e.Step() // tick 110: the red onset
	if st := e.sigState(l); st != SigRed {
		t.Fatalf("tick 110: state %v, want held red", st)
	}
	for e.Tick < 115 {
		if e.sigInClearance(l) {
			t.Fatalf("tick %d: spurious clearance after held-green→red — the lookback read the schedule's amber, not the held green", e.Tick)
		}
		e.Step()
	}
}

// TestSignalSetLapseClearanceAmber: a held AMBER that lapses while the
// fixed schedule is already red gets its FULL clearance window from the
// lapse tick — the displayed amber→red transition happens at the override's
// until, not at the schedule's historical red onset. The old path measured
// the window from the schedule onset and closed it 20 ticks early, which
// can stop a legally committed vehicle.
func TestSignalSetLapseClearanceAmber(t *testing.T) {
	// Green [0,100), amber [100,130), red [130,330); clearance 30 ticks.
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {3.0, "y"}, {20.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")

	for e.Tick < 4 {
		e.Step()
	}
	// Hold AMBER [5,150): it lapses at 150, where the fixed schedule has
	// been red since 130 — the schedule's red onset is 20 ticks BEFORE the
	// displayed transition.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-amber", Signal: "J", Phase: 1, HoldTicks: 145}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 200 {
		e.Step()
		switch {
		case e.Tick >= 5 && e.Tick <= 149:
			if st := e.sigState(l); st != SigAmber {
				t.Fatalf("tick %d: state %v during the hold, want amber", e.Tick, st)
			}
		case e.Tick == 150:
			// The lapse: display goes held-amber → schedule-red.
			if st := e.sigState(l); st != SigRed {
				t.Fatalf("tick 150: state %v after lapse, want red (fixed-time resumed)", st)
			}
			if len(e.LapsedSignals()) != 1 || e.LapsedSignals()[0].Until != 150 {
				t.Fatalf("tick 150: lapse = %+v, want the amber hold lapsing at 150", e.LapsedSignals())
			}
			fallthrough
		case e.Tick <= 180:
			// The full window from the LAPSE tick (150..180): the schedule's
			// red onset (130) must not shorten it — 165 and 180 are past the
			// window the schedule's onset would give (closes at 160).
			if !e.sigInClearance(l) {
				t.Fatalf("tick %d: clearance closed — the window was measured from the schedule's red onset (130), not the lapse (150)", e.Tick)
			}
		case e.Tick == 181:
			if e.sigInClearance(l) {
				t.Fatal("tick 181: clearance still open 31 ticks after the displayed red onset")
			}
		}
	}
}

// TestSignalSetLapseClearanceGreen: a held GREEN that lapses onto a
// schedule whose red began BEFORE the hold started must NOT inherit
// clearance from the schedule's earlier amber — the displayed transition
// is green→red at the lapse tick, and the resumed red is absolute for
// anything the wall can stop.
func TestSignalSetLapseClearanceGreen(t *testing.T) {
	// Green [0,100), amber [100,130), red [130,330); clearance 30 ticks.
	nf := sigNetFile([]NetSignalPhase{{10.0, "G"}, {3.0, "y"}, {20.0, "r"}}, 0)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")

	for e.Tick < 139 {
		e.Step()
	}
	// Hold GREEN [140,151): the fixed schedule turned red at 130 — BEFORE
	// the hold began — so at the lapse (151) the schedule's red onset is 21
	// ticks old with its amber right behind it. A lookback that fell
	// through to the schedule would open a spurious clearance window
	// (elapsed 21 ≤ 30, prev = schedule amber).
	if err := e.EnqueueSignal(SignalDirective{RequestID: "hold-green", Signal: "J", Phase: 0, HoldTicks: 11}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 170 {
		e.Step()
		switch {
		case e.Tick >= 140 && e.Tick <= 150:
			if st := e.sigState(l); st != SigGreen {
				t.Fatalf("tick %d: state %v during the hold, want green", e.Tick, st)
			}
		case e.Tick == 151:
			if st := e.sigState(l); st != SigRed {
				t.Fatalf("tick 151: state %v after lapse, want red (fixed-time resumed)", st)
			}
			if len(e.LapsedSignals()) != 1 || e.LapsedSignals()[0].Until != 151 {
				t.Fatalf("tick 151: lapse = %+v, want the green hold lapsing at 151", e.LapsedSignals())
			}
			fallthrough
		case e.Tick <= 160:
			// Green→red at the lapse: NO clearance, even though the
			// schedule's amber [100,130) sits within the window of its own
			// red onset.
			if e.sigInClearance(l) {
				t.Fatalf("tick %d: spurious clearance after held-green→lapse — the window inherited the schedule's amber", e.Tick)
			}
		}
	}
}

// sig4NetFile is a single-approach junction whose program splits red
// across two consecutive phases — green [0,50), amber [50,60), red-A
// [60,70), red-B [70,120): two different phase indices showing the SAME
// state for the approach, with red-A (10 ticks) shorter than the
// clearance window (30). This is the shape that makes phase-index merging
// and displayed-state merging disagree.
func sig4NetFile() *NetFile {
	return sigNetFile([]NetSignalPhase{{5.0, "G"}, {1.0, "y"}, {1.0, "r"}, {5.0, "r"}}, 0)
}

// TestSignalSetLapseClearanceOffsetWrap: an offset program sits mid-phase
// at tick 0 (the fixed phase's onset wraps before the run start), and a
// hold lapsing inside that first partial phase still moved the DISPLAY at
// its until — the clearance window must open there, not be denied by the
// wrapped onset (which reads as "no transition" without the guard).
func TestSignalSetLapseClearanceOffsetWrap(t *testing.T) {
	// Cycle 20 ticks, offset 5: tick 0 is 15 ticks into the cycle — RED,
	// wrapped from the previous cycle; the schedule's first genuine onset
	// is phase 0 at tick 5. Ticks 0–4 are the wrapped red.
	nf := sigNetFile([]NetSignalPhase{{1.0, "G"}, {0.3, "y"}, {0.7, "r"}}, 0.5)
	e, err := NewEngine(sigSpec(t, nf, 0))
	if err != nil {
		t.Fatal(err)
	}
	l := e.Net.LaneByID("iJ_0")
	if st := e.sigState(l); st != SigRed {
		t.Fatalf("tick 0: state %v, want the wrapped red — fixture broken", st)
	}
	// Hold AMBER from tick 1 for 2 ticks; it lapses at 3, back onto the
	// wrapped red — a displayed amber→red transition at 3, inside the
	// first partial cycle.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "h", Signal: "J", Phase: 1, HoldTicks: 2}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 10 {
		e.Step()
		switch {
		case e.Tick == 1 || e.Tick == 2:
			if st := e.sigState(l); st != SigAmber {
				t.Fatalf("tick %d: state %v, want held amber", e.Tick, st)
			}
		case e.Tick == 3 || e.Tick == 4:
			if st := e.sigState(l); st != SigRed {
				t.Fatalf("tick %d: state %v after the lapse, want the wrapped red", e.Tick, st)
			}
			if !e.sigInClearance(l) {
				t.Fatalf("tick %d: clearance denied — the onset wrapped to 0 instead of opening at the lapse (3)", e.Tick)
			}
			if e.Tick == 3 {
				if len(e.LapsedSignals()) != 1 || e.LapsedSignals()[0].Until != 3 {
					t.Fatalf("tick 3: lapse = %+v, want the amber hold lapsing at 3", e.LapsedSignals())
				}
			}
		}
	}
}

// TestSignalSetSameStateLapseKeepsClearance: across a lapse (or a
// supersession) between two phases that BOTH show red for the approach,
// the red span is not re-onseted — the displayed red began when the held
// red-A replaced the amber at tick 55, so the clearance window runs
// [55, 85] regardless of the phase-index changes at 70 (fixed red-B) and
// 75/70 (the override boundaries). The round-3 index-based merge closed
// the window at each boundary.
func TestSignalSetSameStateLapseKeepsClearance(t *testing.T) {
	t.Run("lapse", func(t *testing.T) {
		e, err := NewEngine(sigSpec(t, sig4NetFile(), 0))
		if err != nil {
			t.Fatal(err)
		}
		l := e.Net.LaneByID("iJ_0")
		for e.Tick < 54 {
			e.Step()
		}
		// Hold red-A from tick 55 (mid fixed amber) through tick 74; the
		// lapse at 75 lands in fixed red-B (onset 70) — same displayed
		// state throughout.
		if err := e.EnqueueSignal(SignalDirective{RequestID: "h", Signal: "J", Phase: 2, HoldTicks: 20}); err != nil {
			t.Fatal(err)
		}
		for e.Tick < 90 {
			e.Step()
			if st := e.sigState(l); e.Tick >= 55 && st != SigRed {
				t.Fatalf("tick %d: state %v, want red (held red-A, then fixed red-B)", e.Tick, st)
			}
			switch {
			case e.Tick >= 55 && e.Tick <= 85:
				if !e.sigInClearance(l) {
					t.Fatalf("tick %d: clearance closed — the red span was re-onseted at a same-state boundary (displayed red since 55, window to 85)", e.Tick)
				}
			case e.Tick == 86:
				if e.sigInClearance(l) {
					t.Fatal("tick 86: clearance still open 31 ticks after the displayed red onset")
				}
			}
		}
	})
	t.Run("supersede", func(t *testing.T) {
		e, err := NewEngine(sigSpec(t, sig4NetFile(), 0))
		if err != nil {
			t.Fatal(err)
		}
		l := e.Net.LaneByID("iJ_0")
		for e.Tick < 54 {
			e.Step()
		}
		if err := e.EnqueueSignal(SignalDirective{RequestID: "h1", Signal: "J", Phase: 2, HoldTicks: 100}); err != nil {
			t.Fatal(err)
		}
		for e.Tick < 69 {
			e.Step()
		}
		// Supersede red-A with red-B, applied at tick 70 — same displayed
		// state, so no new onset and no lapse event.
		if err := e.EnqueueSignal(SignalDirective{RequestID: "h2", Signal: "J", Phase: 3, HoldTicks: 50}); err != nil {
			t.Fatal(err)
		}
		for e.Tick < 90 {
			e.Step()
			if e.Tick == 70 && len(e.LapsedSignals()) != 0 {
				t.Fatalf("tick 70: lapse %+v for a superseded hold — replacement is not a lapse", e.LapsedSignals())
			}
			switch {
			case e.Tick >= 55 && e.Tick <= 85:
				if !e.sigInClearance(l) {
					t.Fatalf("tick %d: clearance closed at the supersession — same-state replacement is not a display change (window to 85)", e.Tick)
				}
			case e.Tick == 86:
				if e.sigInClearance(l) {
					t.Fatal("tick 86: clearance still open 31 ticks after the displayed red onset")
				}
			}
		}
	})
}

// TestSignalSetSameStateCommittedVehicle: an amber-committed vehicle
// (v² > 2·d·B at the decision point — legally unable to stop) crossing
// DURING red-B, past the phase-index boundary, keeps its clearance: the
// red wall must not recapture it. Gate-level assertion with a vehicle
// placed in the committed band (2·d·B < v² ≤ 2·d·emergencyDecel): with
// the window open it is released; with the window wrongly closed the
// stale-red rule holds it (64 ≤ 180).
func TestSignalSetSameStateCommittedVehicle(t *testing.T) {
	e, err := NewEngine(sigSpec(t, sig4NetFile(), 0))
	if err != nil {
		t.Fatal(err)
	}
	a := e.Net.LaneByID("nA_0")
	iJ0 := e.Net.LaneByID("iJ_0")
	for e.Tick < 54 {
		e.Step()
	}
	// Hold red-A [55,75); lapse at 75 into fixed red-B — the display has
	// been red since 55 (after amber), so at tick 80 the clearance window
	// is still open.
	if err := e.EnqueueSignal(SignalDirective{RequestID: "h", Signal: "J", Phase: 2, HoldTicks: 20}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < 80 {
		e.Step()
	}
	if !e.sigInClearance(iJ0) {
		t.Fatal("tick 80: clearance closed across the red-A→red-B boundary — fixture broken")
	}
	// Committed: v² = 64 > 2·10·1.67 ≈ 33.4 — it cannot stop comfortably,
	// so clearance releases it.
	committed := &Vehicle{Lane: a, V: 8, Type: e.scen.Types[0], F: 1}
	if _, held := e.sigGate(committed, iJ0, 10); held {
		t.Error("tick 80: the red wall recaptured an amber-committed vehicle inside the clearance window")
	}
	// Control: a vehicle that CAN stop comfortably (v² = 4 ≤ 33.4) is
	// still held — the window releases only the committed.
	able := &Vehicle{Lane: a, V: 2, Type: e.scen.Types[0], F: 1}
	if _, held := e.sigGate(able, iJ0, 10); !held {
		t.Error("tick 80: a comfortably-stoppable vehicle was released on red — clearance over-releases")
	}
}

// TestSignalSetHeldGreenBoxBlocked: a controller can extend green, but
// green never means enter a box you cannot exit — the ADR-0010 box checks
// gate a held green exactly as they gate a scheduled one. The program here
// is solid red; ONLY the verb makes the light green, so the verb is
// load-bearing in both directions.
func TestSignalSetHeldGreenBoxBlocked(t *testing.T) {
	// The fixed-time program is solid red for the whole run (its sole green
	// phase sits past the horizon at [6000,6010)); ONLY the verb makes the
	// light green, so the verb is load-bearing in both directions.
	nf := sigNetFile([]NetSignalPhase{{600.0, "r"}, {1.0, "G"}}, 0)
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

	if err := e.EnqueueSignal(SignalDirective{RequestID: "g1", Signal: "J", Phase: 1, HoldTicks: 150}); err != nil {
		t.Fatal(err)
	}
	sawGreen := false
	for e.Tick < 200 {
		e.Step()
		assertNoNaN(t, e)
		if e.Tick >= 1 && e.Tick <= 150 {
			if st := e.sigState(iJ0); st != SigGreen {
				t.Fatalf("tick %d: held phase shows %v, want green (the verb must be in force)", e.Tick, st)
			}
			sawGreen = true
		}
		if v.Lane == iJ0 {
			t.Fatalf("tick %d: entered a box whose exit cannot receive it — held green or not", e.Tick)
		}
	}
	if !sawGreen {
		t.Fatal("the held green never showed — the verb did nothing")
	}
	if v.V != 0 {
		t.Errorf("held vehicle did not brake to a stop (v=%.2f)", v.V)
	}
	if e.Stats.Collisions != 0 {
		t.Errorf("%d collision observations, want 0", e.Stats.Collisions)
	}
}
