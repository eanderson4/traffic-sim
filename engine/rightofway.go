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
// class, ties by lower vehicle ID — deterministic, no RNG. Signalized
// approaches (internal lanes carrying a Signal program) are gated by the
// light instead (ADR-0011, engine/signal.go), composing with this same
// gate; an off/blinking light falls back to the priority model here.
// Determinism: the gate iterates only slices in fixed order, reads no wall
// clock, draws no randomness.

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

// rowGate evaluates the right-of-way guardrail for v: ok=true means v may
// not enter the junction this tick; the returned accel is the virtual
// stop-line wall (IDM toward a standing vehicle at the line), applied by
// the caller as a cap on v.Acc. The gate target is the first INTERNAL lane
// reached by following v's picked successors (gateTarget), so approaches
// behind netimport's sub-vehicle-length junction-boundary stubs are gated
// from their real approach lane with the distance measured THROUGH the
// stubs — a 0.2 m stub is not enough road to stop on, and gating only
// there let every approach run the line (fixtures, 2026-07-23).
// Approaching an unmodeled junction (RowNone) is never gated.
// Signal-controlled approaches (target.Signal) are gated by the light
// first (ADR-0011); an off/blinking light exerts no control and falls
// through to the priority model below.
func (e *Engine) rowGate(v *Vehicle) (float64, bool) {
	lane := v.Lane
	if lane == nil || len(lane.Successors) == 0 {
		return 0, false
	}
	if lane.Internal {
		// Inside a box the entry gate no longer applies, but the box EXIT
		// must still take v: a fast vehicle crossing a long box meets
		// whatever entered the shared exit funnel meanwhile (the residual
		// box-interior overlaps after the entry-time fixes). The wall at
		// the lane end holds v INSIDE the box — stopping in the box is a
		// textbook violation, but a safe one; overlapping is worse.
		//
		// Controlled internal→internal boundary (ADR-0038 review finding):
		// consolidation rewires a deleted sliver's predecessors straight into
		// the far box, so a movement that used to approach the far junction
		// on a ROAD lane — where gateTarget ran the full gate — now crosses
		// internal→internal. The stop-line duties apply at that seam exactly
		// as on a road approach: sigGate for signalized far lanes (whose
		// green path runs its own boxBlocked — signal.go's exit-room check
		// DOES apply here), the stop-class full-stop duty for RowStop, and
		// the approaching-foe yield for minor/permissive movements. Only the
		// ROW path's box-occupancy/exit-room half (rowConflict's boxWalk) is
		// omitted, deliberately: exitBlocked below owns room at a row seam,
		// and re-running the entry-arm chain rule there would serialize
		// discharge — the defect the in-box arm was written to avoid.
		if next := e.pickSuccessor(lane, v); next.Internal && e.controlled(next) {
			dist := lane.Length - v.S
			permissive := false
			if next.Signal != nil {
				if w, gated := e.sigGate(v, next, dist); gated {
					return w, true
				}
				permissive = e.sigPermissive(next)
			}
			hold := false
			if next.Row == RowStop && !v.stopDone {
				// Stop-class far lane: the full-stop duty the road-approach
				// path enforces, at the seam. stopDone is consumed by the
				// crossing itself (boundaries resets it entering any
				// internal lane), so a hold skipped here is unrecoverable
				// downstream — a no-foe arrival would roll the stop at speed.
				hold = true
				if v.V == 0 && dist <= v.Type.S0+1.0 {
					v.stopDone = true // reached at the line: serve from the next tick
				}
			} else if (next.Row != RowNone || permissive) && e.foeApproachConflict(v, next) {
				hold = true
			}
			if hold {
				wall := idmAccel(v.Type, v.v0eff(lane), v.V, LeaderInfo{OK: true, Gap: dist, V: 0})
				return wall, true
			}
		}
		if e.exitBlocked(v, lane, true) {
			wall := idmAccel(v.Type, v.v0eff(lane), v.V, LeaderInfo{OK: true, Gap: lane.Length - v.S, V: 0})
			return wall, true
		}
		return 0, false
	}
	next, dist := e.gateTarget(v)
	if next == nil {
		return 0, false
	}
	// A permissive green ('g') is a green that has NOT separated the
	// conflict: the light says go, the foes are green too, and yielding is
	// the driver's job (sigPermissive). Protected green ('G') keeps the
	// original contract — the light adjudicated, so the priority model below
	// is skipped. Without this split every permissive movement behaved as
	// protected and crossed its foes unconditionally.
	permissive := false
	if next.Signal != nil {
		if w, gated := e.sigGate(v, next, dist); gated {
			return w, true
		}
		permissive = e.sigPermissive(next)
	}
	// Signalised internal lanes carry RowNone (the light is their control),
	// so a permissive approach has to be exempted from this early return to
	// reach rowConflict at all. It lands there as a minor approach — RowNone
	// != RowMajor — which is exactly the discipline permissive green means:
	// yield to every crossing and merging foe.
	if next.Row == RowNone && !permissive {
		return 0, false
	}
	hold := true
	if next.Row == RowStop && !v.stopDone {
		// Stop approach: hold until a full stop has been reached AT the line
		// (the wall stops the vehicle within its jam gap of the line; a stop
		// further back is queueing, not the required stop).
		if v.V == 0 && dist <= v.Type.S0+1.0 {
			v.stopDone = true
		}
	} else if !e.rowConflict(v, next) {
		hold = false
	}
	if !hold {
		return 0, false
	}
	wall := idmAccel(v.Type, v.v0eff(lane), v.V, LeaderInfo{OK: true, Gap: dist, V: 0})
	return wall, true
}

// gateTarget resolves the junction v is actually approaching: the first
// internal lane that EXERTS CONTROL this tick along v's picked-successor
// chain, and the distance from v's front bumper to its start, bounded by
// maxLaneHops like leaderAt. Two kinds of lanes are walked THROUGH, their
// lengths accumulated into the distance:
//
//   - non-internal fragments (netimport's 0.2–3.5 m junction-boundary
//     stubs — not enough road to stop on, so gating only there ran every
//     line);
//   - UNCONTROLLED internal lanes (no live signal, RowNone): back-to-back
//     junction crops chain one junction's internal lane straight into the
//     next junction's stop line, and the only gate that can hold that
//     traffic is the downstream one. Holding upstream of the intermediate
//     box is also the textbook "don't block the box" outcome.
//
// nil when the chain reaches no controlled internal lane within sight
// (free road, network exit, hop cap, or beyond maxSightM — the same sight
// bound leaderAt uses; a farther junction will cycle before v arrives).
func (e *Engine) gateTarget(v *Vehicle) (*Lane, float64) {
	cur := v.Lane
	dist := cur.Length - v.S
	// Virtual walk: the held turn is consumed at the first crossing, as in
	// boundaries() — later hops follow the route/default only.
	probe := *v
	for hops := 0; hops < maxLaneHops; hops++ {
		if len(cur.Successors) == 0 {
			return nil, 0
		}
		next := e.pickSuccessor(cur, &probe)
		probe.HeldTurn = 0
		if next.Internal && e.controlled(next) {
			if dist > maxSightM {
				return nil, 0
			}
			return next, dist
		}
		dist += next.Length
		cur = next
		if dist > maxSightM {
			return nil, 0
		}
	}
	return nil, 0
}

// controlled reports whether an internal lane exerts any control this
// tick: a live (non-off) signal state, or a modeled right-of-way class.
func (e *Engine) controlled(l *Lane) bool {
	if l.Signal != nil && e.sigState(l) != SigOff {
		return true
	}
	return l.Row != RowNone
}

// rowConflict reports whether entering the internal lane next would
// conflict: a foe vehicle is inside the box, the exit lane has no room for
// v to clear the box, or an approaching foe is committed to entering.
// Its approaching-foe half is factored out as foeApproachConflict — the
// seam gate (rowGate's internal branch) uses that half without boxWalk;
// any yield-class change must stay in sync between the two.
func (e *Engine) rowConflict(v *Vehicle, next *Lane) bool {
	if e.boxBlocked(v, next) {
		return true
	}
	// Approaching foes: minor approaches check every conflict; major
	// approaches check only same-exit (merge) foes — crossing foes of a
	// major approach are minor themselves and do the yielding.
	minor := next.Row != RowMajor
	for _, f := range next.FoesCross {
		if minor && e.foeApproachBlocks(v, f, minor) {
			return true
		}
	}
	for _, f := range next.FoesMerge {
		if e.foeApproachBlocks(v, f, minor) {
			return true
		}
	}
	return false
}

// boxBlocked reports whether entering the internal lane next is physically
// unsafe regardless of right-of-way: a conflicting vehicle is inside the
// box, or the box exit has no room for v. Shared by the priority model
// (ADR-0010) and the signal guardrail (ADR-0011 — green never means enter
// a box you cannot exit).
func (e *Engine) boxBlocked(v *Vehicle, next *Lane) bool {
	blocked, _ := e.boxWalk(v, next)
	return blocked
}

// boxWalk is boxBlocked with the cause attached (see exitWalk for the
// holdSeal contract): the gridlock escape needs to tell a capacity seal
// from a holding stop line, entry gating does not.
func (e *Engine) boxWalk(v *Vehicle, next *Lane) (blocked, holdSeal bool) {
	// Box occupancy: never enter against conflicting traffic already inside
	// (any class — a vehicle physically in the conflict zone owns it).
	for _, f := range next.FoesCross {
		if len(f.vehs) > 0 {
			return true, false
		}
	}
	for _, f := range next.FoesMerge {
		if len(f.vehs) > 0 {
			return true, false
		}
	}
	return e.exitWalk(v, next, false, true)
}

// exitBlocked reports whether the lane's EXIT chain cannot take v. The
// walk accumulates room through short EMPTY non-internal successors
// exactly like leaderAt walks lanes (maxLaneHops): netimport emits
// sub-vehicle-length exit stubs (0.2–3.5 m) at junction boundaries, and
// checking only the immediate successor sealed every movement of stubbed
// junctions permanently (fixtures, 2026-07-23). The walk STOPS at the
// first internal lane it reaches — the next box's domain, whose own gate
// (entry or in-box) takes over from there:
//
//   - a CONVERGENT/CROSSING foe inside it → blocked (adjacent boxes
//     funnel through shared stubs; this is the simultaneous-entry overlap
//     the entry-time foe check misses across boxes);
//   - a HOLDING stop line (red, stop sign) → room capped at its start
//     (a green light here must not release traffic into a red line one
//     stub away);
//   - otherwise → the room to clear THIS box suffices; pass.
//
// The tail rule differs by discipline: at ENTRY (inBox=false, wrapped by
// boxBlocked with the entry-time foe checks) the whole chain behind the
// first queue tail must fit v — "don't enter a junction you cannot
// clear". IN-BOX (inBox=true) the tail is v's own leader — car-following
// (leaderAt) already brakes for it, and walling the box end against it
// serialized discharge to one vehicle per box traversal.
func (e *Engine) exitBlocked(v *Vehicle, next *Lane, inBox bool) bool {
	blocked, _ := e.exitWalk(v, next, inBox, true)
	return blocked
}

// exitWalk is exitBlocked with the cause attached: the second return
// reports whether the seal — when there is one — is a HOLDING STOP LINE
// (red/amber downstream) rather than capacity. Entry gating treats the
// two identically (both mean "do not enter"); the gridlock escape must
// not — a red that holds an in-box vehicle past the strand threshold is
// stop-line starvation, a signal's domain, and the escape's doctrine is
// that a red light is never a trigger (ADR-0034, jammedAtJunction).
//   - turnSpent says whether v's held turn was already consumed reaching
//     `next`. Every entry-gate caller passes true (the crossing into next
//     spent it, boundaries()); the escape's IN-BOX arm passes false — v is
//     still in the box and the walk's FIRST hop is the crossing that will
//     consume the turn (leaderAt spends it there for the same reason).
func (e *Engine) exitWalk(v *Vehicle, next *Lane, inBox, turnSpent bool) (blocked, holdSeal bool) {
	need := v.Type.Length + v.Type.S0
	free := 0.0
	// dist is v's distance to the lane being examined; it is consumed by
	// the merge-funnel arbitration, which only runs in-box (v.Lane == next
	// there, so next.Length is correct). In the entry case it is unused.
	dist := next.Length - v.S
	cur := next
	// Virtual walk: when turnSpent, reaching `next` already consumed the
	// held turn (the crossing into it spends it, boundaries()) — every hop
	// then follows the route/default only. Otherwise the FIRST pick below
	// honors the held turn and spends it, matching boundaries().
	probe := *v
	if turnSpent {
		probe.HeldTurn = 0
	}
	for hops := 0; hops < maxLaneHops; hops++ {
		if len(cur.Successors) == 0 {
			break
		}
		exit := e.pickSuccessor(cur, &probe) // v's actual route, not the default
		probe.HeldTurn = 0                   // the first hop spent it, if still held
		// A shared funnel (several lanes feeding one) arbitrates
		// simultaneous arrivals itself: no foe set covers cross-junction
		// merges (netimport compiles foes WITHIN a junction only), so a
		// vehicle committed to a sibling branch meets v at the merge point
		// with no gate between them (the 42430333→8375523265 stub overlap).
		// The sibling search walks each branch BACKWARD through short
		// fragment chains (the 0.2 m stubs multiply crossings per tick) —
		// the closest approaching vehicle per branch wins; ties by lower
		// ID. A stopped-far-back sibling (queued, not committed) never
		// blocks. IN-BOX ONLY: at entry the approach's own gate
		// (rowConflict/foe approaches) owns near-merge arbitration, and
		// entry distances would make every converging branch a blocker.
		if inBox && len(exit.Prevs) > 1 && e.mergeThreat(v, cur, exit, dist) {
			return true, false
		}
		if n := len(exit.vehs); n > 0 {
			if inBox {
				return false, false // own-path tail: car-following owns it
			}
			first := exit.vehs[0]
			// Room behind the tail measured from this lane's start; a tail
			// protruding before it (negative) eats the accumulated room.
			free += first.S - first.Type.Length
			break
		}
		if exit.Internal {
			for _, f := range exit.FoesCross {
				if len(f.vehs) > 0 {
					return true, false
				}
			}
			for _, f := range exit.FoesMerge {
				if len(f.vehs) > 0 {
					return true, false
				}
			}
			// A HOLDING stop line caps the room at its start; shallow by
			// design (signal wall states + stop class only): no foe
			// evaluation, so no recursion into rowConflict → boxBlocked.
			if e.downstreamHold(exit) {
				holdSeal = true
				break
			}
			return false, false // the next box's gate takes over from here
		}
		if exit.Exit {
			return false, false // drains out of the network: room is unbounded
		}
		free += exit.Length
		dist += exit.Length
		if free >= need {
			break
		}
		cur = exit
	}
	if free < need {
		return true, holdSeal
	}
	return false, false
}

// mergeThreat reports whether a vehicle on a sibling branch feeding exit
// (any prev except v's own chain node cur) reaches the merge before v,
// whose own distance to the merge is distV. Branches are walked backward
// breadth-first through short fragment chains (bounded by maxSightM and a
// visited set — ring topologies loop) and the closest vehicle per branch
// competes: closer wins, ties by lower ID.
func (e *Engine) mergeThreat(v *Vehicle, cur, exit *Lane, distV float64) bool {
	type item struct {
		lane *Lane
		dist float64 // distance from the lane's END to the merge
	}
	var work []item
	seen := map[*Lane]bool{}
	for _, p := range exit.Prevs {
		if p != cur && !seen[p] {
			seen[p] = true
			work = append(work, item{p, 0})
		}
	}
	for len(work) > 0 {
		it := work[0]
		work = work[1:]
		if n := len(it.lane.vehs); n > 0 {
			last := it.lane.vehs[n-1]
			dOther := it.dist + it.lane.Length - last.S
			if dOther < distV || (dOther == distV && last.ID < v.ID) {
				return true
			}
			continue // closest vehicle on this branch found
		}
		for _, pp := range it.lane.Prevs {
			d := it.dist + it.lane.Length
			// Branches only lengthen backward; past distV no deeper
			// vehicle can win the merge (and rings are visited-guarded).
			if !seen[pp] && d < distV {
				seen[pp] = true
				work = append(work, item{pp, d})
			}
		}
	}
	return false
}

// downstreamHold reports whether an internal lane is a mandatory stop
// point this tick for anything approaching it: a red/amber light.
// Deliberately shallower than junctionHold — no foe or box evaluation —
// so boxBlocked can consult it without recursion. A stop-sign class is
// NOT a hold here: the walk only reaches EMPTY lanes (the tail rule runs
// first), an empty stop line is servable — v proceeds and does its own
// stop there, held by the in-box gate. Capping room at an empty stop line
// sealed stop-controlled fragment chains permanently (Fable round 2).
func (e *Engine) downstreamHold(l *Lane) bool {
	if l.Signal != nil {
		switch e.sigState(l) {
		case SigRed, SigAmber:
			return true
		}
	}
	return false
}

// junctionHold reports whether the junction v approaches (next = its
// gateTarget) would hold a vehicle at the stop line THIS tick — the
// rowGate/sigGate decision with v's dynamics removed (a pending spawn is
// "able to stop" by definition: it can simply not enter) and without the
// wall or the stopDone side effect. injectionPlan uses it to treat the
// stop line as a virtual leader.
func (e *Engine) junctionHold(v *Vehicle, next *Lane) bool {
	if next.Signal != nil {
		switch e.sigState(next) {
		case SigRed, SigAmber:
			return true
		case SigGreen:
			if e.boxBlocked(v, next) {
				return true
			}
		}
		// SigOff: no signal control; fall through to the priority model.
	}
	if next.Row == RowNone {
		return false
	}
	if next.Row == RowStop && !v.stopDone {
		return true
	}
	return e.rowConflict(v, next)
}

// foeApproachConflict is rowConflict's approaching-foe half without the
// box-occupancy/exit-room test (boxWalk): at a controlled internal→internal
// boundary the in-box exit walk (exitBlocked) owns room and funnel
// arbitration, and re-running the entry-arm chain rule there would serialize
// discharge — the defect the in-box arm was written to avoid (ADR-0038
// review). The yield classes mirror rowConflict exactly: minor/stop (and
// permissive green, which reaches here with RowNone) yield to every
// conflicting approach; major checks only committed merge foes.
func (e *Engine) foeApproachConflict(v *Vehicle, next *Lane) bool {
	minor := next.Row != RowMajor
	for _, f := range next.FoesCross {
		if minor && e.foeApproachBlocks(v, f, minor) {
			return true
		}
	}
	for _, f := range next.FoesMerge {
		if e.foeApproachBlocks(v, f, minor) {
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
//
// The foe's SIGNAL is part of its priority class (ADR-0033). Reading only
// foe.Row deadlocks a signalised junction, because a signalised internal
// lane carries RowNone — "unmodeled, has priority" — so two permissive
// greens facing each other each classified the other as a priority foe and
// gave way to it forever. See foeSignalClass.
func (e *Engine) foeApproachBlocks(v *Vehicle, foe *Lane, egoMinor bool) bool {
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
			cls := e.foeSignalClass(foe)
			if cls == foeHeld {
				continue // its light is holding it: it cannot start
			}
			foeYields := foe.Row == RowMinor || foe.Row == RowStop || cls == foeYieldClass
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

// foeSignalClass is what the foe lane's CURRENT signal state says about the
// foe's priority, for a foe stopped at its own line (ADR-0033).
//
//	foeHeld       red: its own light forbids it entering. Yielding to a
//	              vehicle that cannot move is how a permissive movement
//	              stalls for the whole of every red the foe gets.
//	foeYieldClass permissive green ('g'): it is required to yield too, so
//	              this is a mutual yield and the ID tie-break decides.
//	foePriority   protected green, amber, or no signal at all: the caller
//	              falls back to foe.Row, which is the pre-ADR-0033 behaviour.
//
// Amber is deliberately priority, not held: an amber foe at its line may be
// committed and entitled to clear the box.
func (e *Engine) foeSignalClass(foe *Lane) foeClass {
	if foe.Signal == nil {
		return foePriority
	}
	switch e.sigState(foe) {
	case SigRed:
		return foeHeld
	case SigGreen:
		if e.sigPermissive(foe) {
			return foeYieldClass
		}
	}
	return foePriority
}

type foeClass int

const (
	foePriority foeClass = iota
	foeYieldClass
	foeHeld
)
