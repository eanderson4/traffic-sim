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

// sigState returns the light state now in force for the approach served by
// the internal lane l (its program's current phase, its link's char).
func (e *Engine) sigState(l *Lane) SigState {
	p := l.Signal
	if p == nil || len(p.phaseTicks) == 0 {
		return SigOff
	}
	st := p.Phases[p.phaseAt(e.Tick)].State
	if l.LinkIdx >= len(st) {
		return SigOff // link without a state char: uncontrolled
	}
	return mapSigChar(st[l.LinkIdx])
}

// clearanceSeconds is the red-clearance window (the all-red clearance
// concept: the dilemma zone empties legally, then red is near-absolute).
// Compiled to ticks per program (compileTicks) — dt is a scenario
// parameter (ADR-0005), never a constant.
const clearanceSeconds = 3.0

// sigInClearance reports whether the link's red phase began within the
// clearance window directly after an amber phase — a real amber→red
// transition (never at run start, and never for green→red programs).
// Stateless: derived from the program and the tick, so keyframe restore
// replays it bit-exactly (no latch to persist).
func (e *Engine) sigInClearance(l *Lane) bool {
	p := l.Signal
	if p == nil {
		return false
	}
	_, elapsed := p.phaseAtElapsed(e.Tick)
	if elapsed > p.clearance || e.Tick < elapsed {
		return false
	}
	onset := e.Tick - elapsed
	if onset == 0 {
		return false
	}
	prev := p.Phases[p.phaseAt(onset-1)].State
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
