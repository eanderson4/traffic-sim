package engine

import (
	"sort"
)

// mobilTieEps is the incentive margin below which two passing candidates are
// considered tied (broken by the vehicle's own RNG stream).
const mobilTieEps = 1e-9

// laneChanges runs the lateral policy once per vehicle per tick,
// front-to-back (descending s, ties by ascending ID), each evaluation seeing
// the state left by earlier changes this tick. One hop per vehicle per tick,
// plus a cooldown, keeps the instant-hop model from oscillating.
//
// Per vehicle: a commanded hop (lateral intent) preempts everything for this
// tick and expires if infeasible (one-shot, ADR-0008 §2); otherwise the
// reference MOBIL policy decides — but only under the idm uncontrolled
// policy. In live runs (holdlast) there is no driving logic in the engine:
// vehicles without a commanded hop keep their lane.
func (e *Engine) laneChanges() {
	idm := e.idmPolicy()
	order := make([]*Vehicle, len(e.order))
	copy(order, e.order)
	sort.Slice(order, func(i, j int) bool {
		if order[i].S != order[j].S {
			return order[i].S > order[j].S
		}
		return order[i].ID < order[j].ID
	})
	for _, v := range order {
		if v.Cooldown > 0 {
			v.Cooldown--
			continue
		}
		if v.reqLane != 0 {
			e.tryForcedLaneChange(v)
			continue
		}
		// Route recovery runs BEFORE the reference policy and in live runs
		// too: a vehicle stranded off its route has no other way back, and
		// in holdlast there is no in-kernel policy to piggyback on.
		if e.tryRouteRecovery(v) {
			continue
		}
		if !idm {
			continue
		}
		e.tryLaneChange(v)
	}
}

// routeHopOK is the lateral half of the route guardrail (ADR-0021): a hop
// that would move a ROUTED vehicle FURTHER from its route is denied. Without
// it the route axis is only advisory — routeNextHop steers at junctions, but
// a route-blind lateral policy (the external driver's MOBIL, which sees no
// route) can strand the vehicle in a lane whose successors never reach the
// destination, and nothing recovers it.
//
// "Further" is measured on the lateral-depth gradient (routing.go): the hop
// is denied when the target lane needs MORE lane changes to get back on
// route than the current one does. The first cut of this guardrail used the
// reachability predicate instead — deny leaving the depth-0 set — which is
// the special case depth[cur] == 0, and which said nothing at all about a
// vehicle already off-route. Sideways moves at equal depth stay legal: MOBIL
// may still pick the faster of two lanes that are equally far from the route.
//
// This caps every control path, commanded or policy, the same shape as the
// ADR-0010 right-of-way guardrail. It is not the engine overruling a
// controller: the route is itself a controller-set axis, so this enforces
// consistency BETWEEN two axes the controller set, and a controller that
// wants the hop can clear the route first.
//
// A vehicle whose destination is unreachable at ANY depth may hop freely —
// vetoing there would pin it in lane forever for no gain.
func (e *Engine) routeHopOK(v *Vehicle, dir int) bool {
	if v.Route == "" {
		return true
	}
	tgt := v.Lane.Left
	if dir < 0 {
		tgt = v.Lane.Right
	}
	if tgt == nil {
		return true // no such neighbour; execLaneChange no-ops anyway
	}
	cur := e.routeLatDist(v.Lane, v.Route)
	if cur < 0 {
		return true // unreachable at any depth: no gradient to protect
	}
	next := e.routeLatDist(tgt, v.Route)
	return next >= 0 && next <= cur
}

// tryRouteRecovery steers a routed vehicle that is off its route back down
// the lateral-depth gradient, subject to the full commanded-hop safety gate.
// It reports whether it took the vehicle's one hop for this tick.
//
// Descending the gradient — rather than requiring a neighbour that is itself
// route-reachable — is what lets recovery cross MORE THAN ONE LANE. A vehicle
// in the left lane of a 3-lane arterial whose exit is on the right has no
// route-reachable neighbour at all; it does have a neighbour one lane change
// closer, and taking that hop this tick puts it one lane change from the
// route next tick. The one-lane rule left exactly that vehicle stranded.
//
// Determinism (ADR-0005): the smaller depth wins, ties break toward the LOWER
// LANE INDEX, and neither RNG nor map iteration is involved — the choice is a
// pure function of (network, lane, destination).
func (e *Engine) tryRouteRecovery(v *Vehicle) bool {
	if v.Route == "" {
		return false
	}
	cur := e.routeLatDist(v.Lane, v.Route)
	if cur <= 0 {
		return false // 0: on route already. −1: unreachable at any depth.
	}
	best, bestDir := int32(-1), 0
	var bestLane *Lane
	for _, dir := range [2]int{1, -1} {
		tgt := v.Lane.Left
		if dir < 0 {
			tgt = v.Lane.Right
		}
		if tgt == nil {
			continue
		}
		d := e.routeLatDist(tgt, v.Route)
		if d < 0 || d >= cur {
			continue
		}
		if bestLane == nil || d < best || (d == best && tgt.Index < bestLane.Index) {
			best, bestDir, bestLane = d, dir, tgt
		}
	}
	if bestLane == nil {
		return false // nothing closer: fall through to ordinary MOBIL
	}
	if e.PolicyContext(v).ForcedFeasible(e.Params, bestDir) {
		e.execLaneChange(v, bestDir)
	}
	// A blocked recovery is not an error: the vehicle retries next tick and,
	// failing that, degrades to the default routing it would have had anyway.
	// Either way it does not also run discretionary MOBIL this tick.
	return true
}

// tryLaneChange evaluates the reference lateral policy (symmetric MOBIL,
// Kesting/Treiber/Helbing 2007 — see policy.go) for v and executes the
// chosen hop. Gather and decision are the shared PolicyCtx path, so the
// external default driver reaches the same decisions from its observations.
func (e *Engine) tryLaneChange(v *Vehicle) {
	d := e.PolicyContext(v).DecideLaneChange(e.Params, v.rng)
	if d != 0 && e.routeHopOK(v, d) {
		e.execLaneChange(v, d)
	}
}

// execLaneChange applies an instant hop toward dir (+1 left, −1 right),
// keeping both lanes' sorted occupancy current for the vehicles evaluated
// later this tick. Callers must have run the safety gates (the reference
// policy or the forced-command gate chain).
func (e *Engine) execLaneChange(v *Vehicle, dir int) {
	cur := v.Lane
	var tgt *Lane
	if dir > 0 {
		tgt = cur.Left
	} else {
		tgt = cur.Right
	}
	if tgt == nil {
		return
	}
	cur.vehs = removeVehicle(cur.vehs, v)
	tgt.vehs = insertVehicle(tgt.vehs, v)
	e.noteLaneLeave(v) // ADR-0036: a lateral hop is a lane departure too
	v.Lane = tgt
	v.Cooldown = e.Params.LCCooldown
	e.Stats.LaneChanges++
}

// kinGapOK is the collision-freedom floor for a hop-created pair under the
// ballistic integrator: with the follower braking at up to emergencyDecel and
// the leader braking no harder, the pair cannot overlap (the Gipps braking
// branch / Krauss v_safe condition, implementation.md §2–3). The one-tick
// closing term covers the finite update time, which acts as a reaction time
// (CACAIE §3.3 via implementation.md §8).
//
// Why MOBIL's b_safe alone is insufficient: ã_n ≥ −b_safe evaluates the
// smooth-IDM acceleration at the hop instant, but IDM is collision-free only
// as an ODE — "discrete time + extreme parameters can overlap"
// (implementation.md §1). An instant hop can drop the follower into a state
// (tiny gap, large Δv) that smooth IDM would never reach on its own and from
// which even −emergencyDecel cannot avoid overlap; the −9 m/s² cap then hides
// the crash behind the 0.1 m gap clamp (M2 merge overlaps, min gap −11.8 m).
// b_safe remains the *acceptability* criterion (comfort); this floor is the
// *physics* backstop and is therefore exempt from merge-urgency relaxation.
func kinGapOK(gap, vFoll, vLead, dt float64) bool {
	if vFoll <= vLead {
		return true // gap grows; the static minGap floor suffices
	}
	return gap >= (vFoll*vFoll-vLead*vLead)/(2*emergencyDecel)+(vFoll-vLead)*dt
}

// neighbors returns the vehicles immediately ahead of (lead) and behind
// (foll) position s on lane l. ex is excluded (it may sit exactly at s).
func neighbors(l *Lane, s float64, ex *Vehicle) (lead, foll *Vehicle) {
	a := l.vehs
	i := sort.Search(len(a), func(i int) bool { return a[i].S > s })
	if i < len(a) {
		lead = a[i]
	}
	if i > 0 {
		foll = a[i-1]
		if foll == ex {
			if i > 1 {
				foll = a[i-2]
			} else {
				foll = nil
			}
		}
	}
	return lead, foll
}

// removeVehicle deletes v from a sorted lane occupancy slice.
func removeVehicle(a []*Vehicle, v *Vehicle) []*Vehicle {
	i := sort.Search(len(a), func(i int) bool { return a[i].S >= v.S })
	for ; i < len(a); i++ {
		if a[i] == v {
			return append(a[:i], a[i+1:]...)
		}
	}
	return a
}

// insertVehicle inserts v into a sorted lane occupancy slice.
func insertVehicle(a []*Vehicle, v *Vehicle) []*Vehicle {
	i := sort.Search(len(a), func(i int) bool {
		if a[i].S != v.S {
			return a[i].S > v.S
		}
		return a[i].ID > v.ID
	})
	return append(a[:i], append([]*Vehicle{v}, a[i:]...)...)
}
