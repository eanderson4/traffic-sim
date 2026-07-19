package engine

import "fmt"

// rightofway.go — priority-junction right-of-way enforcement (ADR-0010).
//
// Junction traversal was connection-following only through M6: vehicles
// entered junction-internal lanes freely, so simultaneous arrivals at
// conflict points (crossing paths, junction-exit merge funnels) overlapped.
// This file adds the guardrail, not a gospel: when a vehicle on an approach
// lane would cross into an internal lane it may not enter, its accel is
// capped at the "virtual stop-line wall" — the EndWall mechanism, IDM
// toward a standing vehicle at the lane end — so it brakes smoothly to the
// line and proceeds when the gate opens. The cap lives in computeAccels, the
// shared kernel path, so EVERY controller (harness IDM, cruise servo,
// external drivers via clamped accel intents) inherits it.
//
// Model (per approach, from the connection state of the internal lane it
// feeds — compiled into the network file by netimport):
//
//	major: flows unless the box cannot be cleared (exit lane full or a
//	       conflicting vehicle is inside it) — "don't enter a junction you
//	       can't exit" — or a same-exit (merge) foe is committed to entering.
//	minor: additionally yields to conflicting traffic that entering would
//	       force to brake harder than comfortable (a_req = v²/2d > b).
//	stop:  holds at the line until a full stop is reached there once, then
//	       acts as minor.
//
// Mutual holds (both vehicles stopped at their lines) resolve by priority
// class, ties by lower vehicle ID — deterministic, no RNG. Signals stay
// UNMODELED: traffic_light approaches compile to RowNone and traverse
// freely, exactly as before. Determinism: the gate iterates only slices in
// fixed order, reads no wall clock, draws no randomness.

// RowState is the right-of-way class of a junction approach (the SUMO
// connection state of the internal lane serving it).
type RowState int

const (
	RowNone  RowState = iota // unmodeled: free traversal (signals; pre-extension files)
	RowMajor                 // right of way; enters unless the box cannot be cleared
	RowMinor                 // yields to conflicting traffic
	RowStop                  // must stop at the line once, then acts as minor
)

// ParseRowState parses the compiled-file form: "" (unmodeled), "major",
// "minor", "stop".
func ParseRowState(s string) (RowState, error) {
	switch s {
	case "":
		return RowNone, nil
	case "major":
		return RowMajor, nil
	case "minor":
		return RowMinor, nil
	case "stop":
		return RowStop, nil
	}
	return RowNone, fmt.Errorf("unknown row state %q (want major|minor|stop)", s)
}

// rowGate evaluates the right-of-way guardrail for v at the end of its
// current lane. ok=true means v may not enter the junction this tick; the
// returned accel is the virtual stop-line wall (IDM toward a standing
// vehicle at the lane end), applied by the caller as a cap on v.Acc.
// Approaching an unmodeled junction (RowNone) or a non-internal successor
// is never gated.
func (e *Engine) rowGate(v *Vehicle) (float64, bool) {
	lane := v.Lane
	if lane == nil || lane.Internal || len(lane.Successors) == 0 {
		return 0, false
	}
	next := pickSuccessor(lane, v.HeldTurn)
	if !next.Internal || next.Row == RowNone {
		return 0, false
	}
	hold := true
	if next.Row == RowStop && !v.stopDone {
		// Stop approach: hold until a full stop has been reached AT the line
		// (the wall stops the vehicle within its jam gap of the line; a stop
		// further back is queueing, not the required stop).
		if v.V == 0 && lane.Length-v.S <= v.Type.S0+1.0 {
			v.stopDone = true
		}
	} else if !e.rowConflict(v, next) {
		hold = false
	}
	if !hold {
		return 0, false
	}
	wall := idmAccel(v.Type, v.v0eff(lane), v.V, LeaderInfo{OK: true, Gap: lane.Length - v.S, V: 0})
	return wall, true
}

// rowConflict reports whether entering the internal lane next would
// conflict: a foe vehicle is inside the box, the exit lane has no room for
// v to clear the box, or an approaching foe is committed to entering.
func (e *Engine) rowConflict(v *Vehicle, next *Lane) bool {
	// Box occupancy: never enter against conflicting traffic already inside
	// (any class — a vehicle physically in the conflict zone owns it).
	for _, f := range next.FoesCross {
		if len(f.vehs) > 0 {
			return true
		}
	}
	for _, f := range next.FoesMerge {
		if len(f.vehs) > 0 {
			return true
		}
	}
	// Exit room: don't enter a junction you cannot clear — the box exit
	// must have room for v behind the exit lane's queue TAIL (its first
	// vehicle, the one nearest the box). Room further downstream is
	// irrelevant: entering against a tail sitting at the box exit is how
	// the funnel overlaps happened.
	if len(next.Successors) > 0 {
		exit := next.Successors[0]
		free := exit.Length
		if n := len(exit.vehs); n > 0 {
			first := exit.vehs[0]
			free = first.S - first.Type.Length
		}
		if free < v.Type.Length+v.Type.S0 {
			return true
		}
	}
	// Approaching foes: minor approaches check every conflict; major
	// approaches check only same-exit (merge) foes — crossing foes of a
	// major approach are minor themselves and do the yielding.
	minor := next.Row != RowMajor
	for _, f := range next.FoesCross {
		if minor && foeApproachBlocks(v, f, minor) {
			return true
		}
	}
	for _, f := range next.FoesMerge {
		if foeApproachBlocks(v, f, minor) {
			return true
		}
	}
	return false
}

// foeApproachBlocks reports whether the closest vehicle heading for the foe
// internal lane foe forbids v from entering: a moving foe blocks when
// stopping before the box would force it to brake harder than comfortable
// (a_req = v²/2d > b_comfortable); a stopped foe blocks only when stopped
// at its own line (it can start into the box at any tick), resolved by
// priority class — minor yields to major — and within one class by lower
// vehicle ID (deterministic tie-break).
func foeApproachBlocks(v *Vehicle, foe *Lane, egoMinor bool) bool {
	for _, p := range foe.Prevs {
		n := len(p.vehs)
		if n == 0 {
			continue
		}
		f := p.vehs[n-1] // closest to the box entry
		d := p.Length - f.S
		if f.V == 0 {
			if d > f.Type.S0+1.0 {
				continue // stopped far back: queued, not committed
			}
			// Stopped AT its line: it can start into the box at any tick.
			// Yield-class foes (minor/stop) gate themselves against us;
			// major/unmodeled foes have the priority.
			foeYields := foe.Row == RowMinor || foe.Row == RowStop
			if egoMinor {
				if !foeYields {
					return true // major/unmodeled foe has priority
				}
				if f.ID < v.ID { // mutual yield: lower vehicle ID first
					return true
				}
				continue
			}
			// Ego is major (merge foes are the only ones checked): a
			// yield-class foe holds itself; same class breaks by ID.
			if !foeYields && f.ID < v.ID {
				return true
			}
			continue
		}
		if f.V*f.V > 2*d*f.Type.B {
			return true
		}
	}
	return false
}
