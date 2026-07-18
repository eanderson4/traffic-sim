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
		if !idm {
			continue
		}
		e.tryLaneChange(v)
	}
}

// tryLaneChange evaluates the reference lateral policy (symmetric MOBIL,
// Kesting/Treiber/Helbing 2007 — see policy.go) for v and executes the
// chosen hop. Gather and decision are the shared PolicyCtx path, so the
// external default driver reaches the same decisions from its observations.
func (e *Engine) tryLaneChange(v *Vehicle) {
	d := e.PolicyContext(v).DecideLaneChange(e.Params, v.rng)
	if d != 0 {
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
