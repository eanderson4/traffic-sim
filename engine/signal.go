package engine

import (
	"fmt"
	"math"
)

// signal.go — fixed-time signal programs (ADR-0011), composing with the
// junction right-of-way guardrail of ADR-0010.
//
// A signal program is DATA: a phase list (durations + per-link state
// strings, the SUMO tlLogic alphabet) compiled from the network file into
// tick counts at engine build. The light state of an approach is a pure
// function of the tick count and the compiled program — no wall clock, no
// RNG, no map iteration — so phase state needs no CRC or keyframe coverage
// of its own: it derives from the tick count, which the keyframe already
// restores bit-exactly (same precedent as the network itself).
//
// Enforcement rides the same shared-path cap as the priority model
// (computeAccels → rowGate): on a signal-controlled approach
//
//	red:   hold at the stop line (virtual stop-line wall) if the vehicle
//	       can stop comfortably before it (v² ≤ 2·d·B — the same criterion
//	       as amber, so amber-committed vehicles are never re-captured),
//	       else it is committed and proceeds through clearance;
//	amber: hold only if the vehicle can stop comfortably before the line
//	       (v² ≤ 2·d·B, the ADR-0010 brake-comfort criterion), else it is
//	       committed and proceeds as on green;
//	green: flow — but the ADR-0010 box checks still gate: never enter a
//	       box a conflicting vehicle occupies or whose exit has no room.
//	off/blinking/absent (o/O/u, missing link): the signal exerts no
//	       control and the approach falls back to the ADR-0010 priority
//	       behavior of its compiled row class (in practice RowNone → free
//	       traversal, exactly the pre-signal semantics).
//
// The data-driven phase representation is the seam for external signal
// algorithms (ADR-0008 §5): a later controller replaces the fixed-time
// derivation of the per-approach state (sigState) with commanded states —
// the enforcement path below is unchanged.
//
// ADR-0037 (milestone 1) builds exactly that seam: sigPhaseAt is the ONE
// derivation point — a commanded override (sigctl.go) in force for a
// program returns the commanded phase, anything else the fixed-time
// schedule — and all three enforcement predicates (sigState, sigPermissive,
// sigInClearance) read the phase in force through it, so a held command
// changes WHICH state string is read and nothing about how states are
// enforced. With no override held, sigPhaseAt IS phaseAtElapsed and the
// byte stream is the pre-ADR-0037 one.

// SigState is the enforced light state of one signal-controlled approach.
type SigState int

const (
	SigOff   SigState = iota // off/blinking/absent: no signal control (M7 fallback)
	SigGreen                 // go (g/G)
	SigAmber                 // amber (y): stop if able
	SigRed                   // red (r): hold at the line
)

// SignalPhase is one phase of a fixed-time program: a duration and the
// per-link state string (one char per signal link, SUMO tlLogic alphabet:
// g/G go, y amber, r red, o/O/u off/blinking).
type SignalPhase struct {
	Duration float64 // s (file form; compiled to ticks at engine build)
	State    string  // per-link states; all phases of a program share the length
}

// SignalProgram is a junction's fixed-time program. Durations ride in
// seconds (practitioner form, matching the source tlLogic); the tick grid
// is compiled once the engine's Δt is known (compileSignalTicks).
type SignalProgram struct {
	ID       string        // program id (the tlLogic id; unique per file)
	Junction string        // junction it serves (informational; = ID for single-junction programs)
	Offset   float64       // s; program starts at sim tick offsetTicks (SUMO offset semantics)
	Phases   []SignalPhase // in file order; cycled

	phaseTicks  []uint64 // per-phase duration in ticks (Σ = cycle)
	cycle       uint64   // total cycle length in ticks
	offsetTicks uint64   // Offset in ticks
	clearance   uint64   // red-clearance window in ticks (clearanceSeconds compiled)
}

// newSignalProgram validates the compiled-file form (netfile.go). State
// strings must all carry the same number of links; durations must be
// positive.
func newSignalProgram(id, junction string, offset float64, phases []SignalPhase) (*SignalProgram, error) {
	if id == "" {
		return nil, fmt.Errorf("signal program: empty id")
	}
	if len(phases) == 0 {
		return nil, fmt.Errorf("signal program %s: no phases", id)
	}
	n := len(phases[0].State)
	p := &SignalProgram{ID: id, Junction: junction, Offset: offset, Phases: phases}
	for i, ph := range phases {
		if ph.Duration <= 0 {
			return nil, fmt.Errorf("signal program %s phase %d: duration %v (want > 0)", id, i, ph.Duration)
		}
		if len(ph.State) != n {
			return nil, fmt.Errorf("signal program %s phase %d: state length %d, want %d (ragged state strings)", id, i, len(ph.State), n)
		}
	}
	return p, nil
}

// compileTicks rounds phase durations and the offset onto the tick grid
// (Δt = Params.Dt). Fail-loud when a phase rounds to zero ticks — the tick
// cannot represent it (ADR-0005: the 100 ms tick matches NTCIP decisecond
// timers exactly; sub-tick phases are a modeling error, not a rounding).
func (p *SignalProgram) compileTicks(dt float64) error {
	if dt <= 0 {
		return fmt.Errorf("signal program %s: non-positive tick length %v", p.ID, dt)
	}
	p.phaseTicks = make([]uint64, len(p.Phases))
	for i, ph := range p.Phases {
		tk := uint64(math.Round(ph.Duration / dt))
		if tk == 0 {
			return fmt.Errorf("signal program %s phase %d: duration %v s rounds to 0 ticks at dt %v", p.ID, i, ph.Duration, dt)
		}
		p.phaseTicks[i] = tk
		p.cycle += tk
	}
	p.offsetTicks = uint64(math.Round(p.Offset / dt))
	p.clearance = uint64(math.Round(clearanceSeconds / dt))
	return nil
}

// phaseAt returns the phase index in force at the given tick count: a pure
// function of the tick and the compiled program (SUMO semantics: the
// program's phase 0 begins at tick offsetTicks; before that the cycle
// wraps). Deterministic: integer arithmetic over the slice in order.
func (p *SignalProgram) phaseAt(tick uint64) int {
	idx, _ := p.phaseAtElapsed(tick)
	return idx
}

// phaseAtElapsed returns the phase index in force and how many ticks ago
// it began — the red-clearance window (sigGate) needs the onset distance.
func (p *SignalProgram) phaseAtElapsed(tick uint64) (int, uint64) {
	x := (tick%p.cycle + p.cycle - p.offsetTicks%p.cycle) % p.cycle
	for i, tk := range p.phaseTicks {
		if x < tk {
			return i, x
		}
		x -= tk
	}
	return len(p.phaseTicks) - 1, 0 // unreachable (x < cycle), kept total
}

// PhaseAt exposes the phase-index derivation (phaseAt) for wire encoders:
// engine/natsio publishes the program TABLE (definitions, not states) on
// ts.{run}.state.sig and clients derive any tick's light states themselves
// (ADR-0006, 2026-07-20 M9 addendum). Same pure integer function.
func (p *SignalProgram) PhaseAt(tick uint64) int { return p.phaseAt(tick) }

// CompiledTicks exposes the tick-grid compilation for wire encoders:
// per-phase durations in ticks (Σ = cycle) and the program offset in ticks.
// The returned slice is a copy; programs stay kernel-owned.
func (p *SignalProgram) CompiledTicks() (phaseTicks []uint64, offsetTicks uint64) {
	out := make([]uint64, len(p.phaseTicks))
	copy(out, p.phaseTicks)
	return out, p.offsetTicks
}

// compileSignalTicks compiles every program of the network onto the tick
// grid; called once by NewEngine after BuildNet. No-op without programs.
func (n *Network) compileSignalTicks(dt float64) error {
	for _, p := range n.Signals {
		if err := p.compileTicks(dt); err != nil {
			return err
		}
	}
	return nil
}

// mapSigChar maps one SUMO tlLogic state char to the enforced state.
// Conservative choices (ADR-0011): only g/G/y/r exert control; everything
// else — o/O (off, blinking), u (red-yellow), unknown chars — means the
// signal exerts no control and the priority model takes over.
func mapSigChar(c byte) SigState {
	switch c {
	case 'g', 'G':
		return SigGreen
	case 'y':
		return SigAmber
	case 'r':
		return SigRed
	}
	return SigOff
}

// sigPhaseAt returns the phase index in force for program p at the given
// tick and how many ticks ago that phase began: the commanded phase while a
// signal_set override covers the tick (ADR-0037 — [since, until), onset at
// the applied tick), otherwise the fixed-time derivation. This is the
// single point where commanded control enters enforcement; with an empty
// override table it reduces to phaseAtElapsed exactly. Deterministic:
// table lookup by program id, then a newest-first scan of that program's
// short history — the entries' held intervals are disjoint (a superseded
// entry's Until truncates to the replacement tick, sigctl.go), so at most
// one covers any tick, including the onset−1 lookbacks into a just-ended
// hold.
func (e *Engine) sigPhaseAt(p *SignalProgram, tick uint64) (int, uint64) {
	if h, ok := e.sigOv[p.ID]; ok {
		for i := len(h) - 1; i >= 0; i-- {
			if ov := h[i]; tick >= ov.since && tick < ov.until {
				return ov.phase, tick - ov.since
			}
		}
	}
	return p.phaseAtElapsed(tick)
}

// sigState returns the light state now in force for the approach served by
// the internal lane l (its program's current phase, its link's char).
func (e *Engine) sigState(l *Lane) SigState {
	p := l.Signal
	if p == nil || len(p.phaseTicks) == 0 {
		return SigOff
	}
	idx, _ := e.sigPhaseAt(p, e.Tick)
	st := p.Phases[idx].State
	if l.LinkIdx >= len(st) {
		return SigOff // link without a state char: uncontrolled
	}
	return mapSigChar(st[l.LinkIdx])
}

// sigPermissive reports whether the approach's green in force RIGHT NOW is
// permissive ('g') rather than protected ('G').
//
// mapSigChar deliberately folds both into SigGreen: for the question it
// answers — may v pass the stop line — they are identical. They are not
// identical for the question of who yields. In the SUMO tlLogic alphabet
// 'G' means the movement holds right of way over its foes, while 'g' means
// it may proceed only after yielding to them: the classic permissive left
// across oncoming traffic, or a right turn merging into a stream that is
// green at the same time. A signal program EXPRESSES the conflict rather
// than separating it in time, and the yielding is the driver's job.
//
// Folding the two together gave every permissive movement protected
// behavior, because rowGate returns before rowConflict on a green signalised
// approach (the light is assumed to have separated the conflict already).
// On this Chicago import that was 2,008 of 13,181 signal links, every one of
// them with foes it never yielded to — 1,460 crossing and 5,384 merge foe
// relations evaluated by nothing. Junction 256591534 alone booked 525,733 of
// the run's 790,454 overlap observations (66%): its phase 0 is "GGgrrrGGg",
// where permissive link 2 and link 6 — green in EVERY phase — discharge into
// the same exit lane on every cycle.
//
// Kept as a separate predicate rather than a fourth SigState so that every
// existing SigGreen comparison keeps its meaning; this asks a different
// question of the same char.
func (e *Engine) sigPermissive(l *Lane) bool {
	p := l.Signal
	if p == nil || len(p.phaseTicks) == 0 {
		return false
	}
	idx, _ := e.sigPhaseAt(p, e.Tick)
	st := p.Phases[idx].State
	if l.LinkIdx >= len(st) {
		return false
	}
	return st[l.LinkIdx] == 'g'
}

// clearanceSeconds is the red-clearance window (the all-red clearance
// concept: the dilemma zone empties legally, then red is near-absolute).
// Compiled to ticks per program (compileTicks) — dt is a scenario
// parameter (ADR-0005), never a constant.
const clearanceSeconds = 3.0

// sigSpanOnset returns the displayed phase at tick x, the onset of the
// displayed span containing it, and whether that onset is an OVERRIDE
// boundary (a hold's applied tick or end). While a hold covers x the span
// is the override's. Otherwise the onset is the LATER of the fixed-time
// onset and the most recent override end ≤ x: a lapse (or a supersession)
// moves the displayed transition to the override's boundary, because that
// is when the light actually changed — the schedule's historical phase
// onset may be long past by then, and measuring the clearance window from
// it would shorten or skip the clearance of an amber held past it (and
// the symmetric green case). With an empty override table this is
// phaseAtElapsed's answer exactly, and the boundary flag is false.
//
// The flag exists because the merge walk (sigDisplayedOnset) merges
// state-equal spans ONLY across override boundaries: fixed-to-fixed phase
// onsets keep the pre-ADR-0037 index-based onset even when two consecutive
// phases show the same state for a lane (SUMO splits reds across phases),
// so a no-verb run stays byte-identical to before — recorded CRC baselines
// included. The legacy clearance of such programs is preserved, not
// endorsed.
func (e *Engine) sigSpanOnset(p *SignalProgram, x uint64) (phase int, onset uint64, overrideBoundary bool) {
	if h, ok := e.sigOv[p.ID]; ok {
		// Newest first: entries are chronological with increasing Untils, so
		// the first entry ending at or before x is the most recent override
		// boundary — the lapse the display last followed.
		for i := len(h) - 1; i >= 0; i-- {
			ov := h[i]
			if x >= ov.since && x < ov.until {
				return ov.phase, ov.since, true
			}
			if ov.until <= x {
				idx, elapsed := p.phaseAtElapsed(x)
				if elapsed > x {
					// The fixed phase's onset wraps before tick 0 (an
					// offset program early in the run). A hold that lapsed
					// inside that first partial phase still moved the
					// DISPLAY at its until — report the lapse, not the
					// fictitious zero onset (which sigInClearance would
					// read as "no transition", denying the clearance).
					if ov.until > 0 {
						return idx, ov.until, true
					}
					return idx, 0, false
				}
				fixedOnset := x - elapsed
				if ov.until > fixedOnset {
					return idx, ov.until, true
				}
				// The fixed onset is a genuine schedule transition — unless
				// the lapse lands exactly on it, in which case the boundary
				// is both.
				return idx, fixedOnset, ov.until == fixedOnset
			}
		}
	}
	idx, elapsed := p.phaseAtElapsed(x)
	if elapsed > x {
		return idx, 0, false
	}
	return idx, x - elapsed, false
}

// sigPhaseState maps one phase's state char for lane l to the enforced
// state — the same mapping sigState applies (out-of-range link: SigOff).
// The clearance onset walk compares these, not phase indices: two
// different phases can show the same state for an approach, and across an
// override boundary between them the lane's display does not change.
func sigPhaseState(l *Lane, phaseIdx int) SigState {
	st := l.Signal.Phases[phaseIdx].State
	if l.LinkIdx >= len(st) {
		return SigOff
	}
	return mapSigChar(st[l.LinkIdx])
}

// sigDisplayedOnset returns the phase in force at the given tick and the
// tick the DISPLAY began showing its state for lane l. Contiguous spans
// whose DISPLAYED STATE FOR THAT LANE (sigPhaseState) is identical merge
// across OVERRIDE boundaries (a hold's start or end): a hold commanding
// the state the display was already in, a lapse back into it, and a
// supersession between two same-state phases are all no display change —
// re-onseting there would close the clearance window early and recapture
// an amber-committed vehicle. Fixed-to-fixed boundaries never merge
// (sigSpanOnset's flag), keeping the no-override path byte-identical. The
// walk is bounded by the clearance window, the only consumer's lookback
// depth.
func (e *Engine) sigDisplayedOnset(l *Lane, tick uint64) (int, uint64) {
	idx, onset, overrideBoundary := e.sigSpanOnset(l.Signal, tick)
	state := sigPhaseState(l, idx)
	for onset > 0 && overrideBoundary && tick-onset <= l.Signal.clearance {
		prevIdx, prevOnset, prevBoundary := e.sigSpanOnset(l.Signal, onset-1)
		if sigPhaseState(l, prevIdx) != state {
			break
		}
		onset, overrideBoundary = prevOnset, prevBoundary
	}
	return idx, onset
}

// sigInClearance reports whether the link's red display began within the
// clearance window directly after an amber display — a real amber→red
// transition (never at run start, and never for green→red programs).
// Derived from the program, the tick, and the (keyframed) override table:
// the onset is the DISPLAYED onset for this lane (sigDisplayedOnset) — an
// override's applied tick while held, the override's until at a lapse,
// merged across boundaries that do not change the lane's displayed
// state — and the phase at onset−1 is read display-aware via sigPhaseAt,
// so a commanded hold is answered for the ticks it covered and a lapse
// opens the window at the moment the light actually changed. A keyframe
// restore replays the window bit-exactly (no latch to persist).
func (e *Engine) sigInClearance(l *Lane) bool {
	p := l.Signal
	if p == nil {
		return false
	}
	_, onset := e.sigDisplayedOnset(l, e.Tick)
	if onset == 0 || e.Tick-onset > p.clearance {
		return false
	}
	prevIdx, _ := e.sigPhaseAt(p, onset-1)
	prev := p.Phases[prevIdx].State
	if l.LinkIdx >= len(prev) {
		return false
	}
	return mapSigChar(prev[l.LinkIdx]) == SigAmber
}

// sigGate evaluates the signal guardrail for v approaching the internal
// lane next, whose stop line is dist ahead of v's front bumper (through
// any fragment stubs — gateTarget). ok=true means v may not enter this
// tick; the returned accel is the virtual stop-line wall, applied by
// rowGate's caller as a cap.
// ok=false on SigOff means "no signal control" — the caller falls through
// to the priority model (the documented off/blinking fallback).
//
// Red holds only a vehicle that can stop COMFORTABLY before the line
// (v² ≤ 2·d·B — the same criterion as amber, so a legally
// amber-committed vehicle is never re-captured by the red wall
// mid-clearance). A vehicle that cannot stop comfortably is committed:
// it crosses during the clearance window instead of sliding
// uncontrollably into the box, and the box checks below still gate it
// (never enter a box a conflicting vehicle occupies or whose exit has no
// room). This is textbook red-clearance behavior — the fixture's earlier
// red "violations" were vehicles the wall could not stop crossing ~0.2 s
// into red.
func (e *Engine) sigGate(v *Vehicle, next *Lane, dist float64) (float64, bool) {
	lane := v.Lane
	wall := func() float64 {
		return idmAccel(v.Type, v.v0eff(lane), v.V, LeaderInfo{OK: true, Gap: dist, V: 0})
	}
	switch e.sigState(next) {
	case SigOff:
		return 0, false
	case SigRed:
		if e.sigInClearance(next) {
			// Clearance window after an amber→red transition: release
			// exactly the vehicles the amber rule committed (the SAME
			// comfort criterion, v² ≤ 2·d·B) — amber-committed traffic is
			// never re-captured by the red wall mid-clearance (Fable S3).
			if v.V*v.V <= 2*dist*v.Type.B {
				return wall(), true
			}
		} else if v.V*v.V <= 2*dist*emergencyDecel {
			// Stale red: hold anything the wall can physically stop.
			return wall(), true
		}
	case SigAmber:
		// Stop if able: hold when the vehicle can stop comfortably before
		// the line (v² ≤ 2·d·B, the ADR-0010 criterion); a vehicle that
		// cannot is committed and proceeds as on green. A vehicle already
		// stopped at the line holds (0 ≤ anything).
		if v.V*v.V <= 2*dist*v.Type.B {
			return wall(), true
		}
	}
	// Green (or committed on amber/red): the light adjudicates conflicts,
	// but green never means enter a box you cannot exit — the ADR-0010 box
	// occupancy and exit-room checks still gate.
	if e.boxBlocked(v, next) {
		return wall(), true
	}
	return 0, false
}
