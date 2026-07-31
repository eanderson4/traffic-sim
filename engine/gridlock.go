package engine

import "math"

// gridlock.go — the bounded escape from a gridlock cycle (ADR-0034).
//
// Gridlock is a CIRCULAR WAIT on lane capacity: vehicle A cannot move until
// there is room on the lane ahead, whose head cannot move until there is
// room on the lane ahead of IT, around a closed cycle of lanes back to A.
// Every gate in the kernel is local and every one of them is answering
// correctly — "there is no room" is true at each link. The cycle is a
// property of the whole ring, and nothing that reasons one junction at a
// time can see it. Once it closes it is permanent: no amount of extra time
// and no reduction in demand recovers it, because nobody in the cycle can
// move until somebody in the cycle moves.
//
// Measured on the 90-minute chi-loop-urban baseline (2026-07-28): 427 lanes
// end frozen with vehicles aboard and zero distance travelled over the final
// 5-minute interval — 403 ordinary road lanes against 24 junction internals.
// The frozen mass is on ROADS, standing in legitimate queues. This is not a
// junction-entry defect; it is a circular wait, and engine/gridlock_test.go
// reproduces it in a four-junction ring where no box is ever blocked.
//
// Prevention in the general case is the deadlock-avoidance problem: to know
// that admitting one more vehicle cannot close a cycle, a junction would
// have to reason about every cycle through it. Real traffic engineering does
// not do this either; it reduces the probability (don't block the box,
// signal metering) and accepts that gridlock happens. So does this kernel.
//
// What a SIMULATOR cannot accept is a network that stops for good: a run
// that gridlocks at minute 40 reports nothing about minutes 40–90, and the
// failure is silent — the totals still add up, the vehicles are all still
// there, and the numbers look like severe congestion rather than a stopped
// model. The escape below makes that failure loud, bounded, and countable
// instead.
//
// The rule: a vehicle that has been stopped for StrandAfterS seconds AND is
// the head of its lane — nothing ahead of it to wait for except the junction
// itself — is removed from the network and counted as STRANDED. Removing one
// vehicle from a cycle unstops the cycle: the lane behind it gains room, its
// follower moves, and the wave runs the whole way round. In the ring fixture
// one removal drains all eighteen vehicles.
//
// Three properties make this safe to leave on:
//
//   - It is inert on a healthy network. Nothing strands unless something has
//     been motionless for five minutes with an open road ahead of it, which
//     no signal cycle and no ordinary queue produces. Every M1–M3 CRC
//     fixture is bit-identical with it enabled.
//   - It is deterministic (ADR-0005): a tick counter, a fixed sweep over
//     e.order, no clock and no RNG.
//   - It is never silent. Stranded vehicles are counted network-wide and by
//     section, and their trip records are emitted as INCOMPLETE and flagged
//     (ADR-0014), so a run that strands can never be read as a run that
//     merely queued.
//
// The head-of-lane condition is what aims it at the cycle. A vehicle in the
// middle of a queue is stopped for a reason the kernel already understands —
// the vehicle in front of it — and removing it would neither unlock anything
// nor be defensible. Only the vehicle at the front is waiting on the
// junction.
//
// After a strand, every vehicle on that lane has its stuck timer reset. Not
// a nicety: without it a permanently sealed road (a genuine dead end, not a
// cycle) would strand its head, find the second vehicle ALREADY past the
// threshold, strand it on the same tick, and flush the whole queue in one
// go. With the reset the cost of a sealed road is one vehicle per
// StrandAfterS, which is a bleed the counter makes visible rather than an
// evaporation.

// stuckSpeed is the speed below which a vehicle counts as stopped for the
// stuck timer (m/s). Matches metricStopSpeed's intent: stopped means
// stopped, not crawling.
const stuckSpeed = 0.1

// strandStuck advances every vehicle's stuck timer and removes those that
// have been stopped at the head of their lane for longer than StrandAfterS.
// Called once per tick after the lane-change phase, so the timer reflects
// this tick's completed motion and a vehicle that changed lanes out of a
// jam is judged in the lane it actually ended up in.
func (e *Engine) strandStuck() {
	e.StrandedIDs = e.StrandedIDs[:0]
	if e.Params.StrandAfterS <= 0 || e.Params.Dt <= 0 {
		return // escape disabled, or a degenerate tick length to divide by
	}
	// Rounded, not truncated. StrandAfterS/Dt is a float ratio, and
	// truncating it strands a full tick EARLY whenever the quotient lands
	// just below an integer: 0.3/0.1 is 2.9999999999999996 in float64, so
	// truncation gives 2 ticks — a threshold 33% shorter than the one asked
	// for. Rounding lands on the nearest representable tick instead, so the
	// error is at most half a tick in either direction rather than a whole
	// one downward. The grid quantisation itself is unavoidable (1 s at
	// dt=0.3 can only be 0.9 s or 1.2 s), and nearest is the least wrong
	// choice available.
	//
	// Both reviewers flagged this line and both asserted the DEFAULT was
	// affected — that 300/0.1 truncates to 2999. It does not: 300/0.1 is
	// exactly 3000.0 in float64, as are 5/0.1, 45/0.1, 2.5/0.1 and 60/0.05.
	// The bug is real but confined to ratios like 0.3/0.1, which is why the
	// test pins THAT case and not the headline one. Two independent reviewers
	// repeating an unverified claim is a good reminder to check the
	// arithmetic rather than the consensus.
	limit := uint64(math.Round(e.Params.StrandAfterS / e.Params.Dt))
	if limit == 0 {
		return
	}
	stranded := false
	for _, v := range e.order {
		if v.V >= stuckSpeed {
			v.stuckTicks = 0
			continue
		}
		v.stuckTicks++
		if v.stuckTicks < limit {
			continue
		}
		lane := v.Lane
		if !e.jammedAtJunction(v, lane) {
			continue
		}
		// Off the lane immediately, not at the end of the sweep: later
		// vehicles in this same tick's sweep read lane.vehs for their own
		// head test and their own boxBlocked, and must not see a vehicle
		// that is already gone. The strand IS a lane departure: fold the
		// capped dwell sample into the adaptive EMA first (ADR-0036 §1) —
		// the failure mode the feature exists to route around is exactly
		// the one that must leave congestion evidence.
		e.noteLaneLeave(v)
		lane.vehs = removeVehicle(lane.vehs, v)
		v.Lane = nil
		stranded = true
		e.Stats.Stranded++
		if e.Stats.StrandedBySection == nil {
			e.Stats.StrandedBySection = map[string]int{}
		}
		e.Stats.StrandedBySection[lane.Section]++
		e.StrandedIDs = append(e.StrandedIDs, v.ID)
		e.resetStuckBehind(lane)
	}
	if stranded {
		e.dropDespawned()
	}
}

// jammedAtJunction reports whether v is waiting on a JUNCTION it cannot
// clear, rather than on the vehicle in front of it. Two conditions:
//
//   - v is the head of its lane (the last entry of the s-sorted occupancy).
//     Anything behind the head is stopped for a reason the kernel already
//     models — the vehicle ahead — and removing it would unlock nothing.
//   - the junction wait is the kind that closes a cycle: either the box v
//     is routed into is BLOCKED (a foe inside it, or no room on the far
//     side for v to clear it), or v is itself INSIDE a box whose routed
//     exit chain has no room for it — the same test, from the far side.
//     This is the whole discriminator.
//
// The second condition is not a refinement, it is the definition. Without
// it the rule fires on ordinary queueing: measured on the I-80 M3 fixture —
// a freeway with a growing queue and no gridlock anywhere — head-of-lane
// alone stranded 8 vehicles and broke the stop-and-go wave structure the
// scenario exists to validate. Those heads were stopped behind the queue on
// the next lane, which is car-following doing its job. A queue that is
// merely long is not a jam the model needs rescuing from; a queue whose head
// cannot enter the junction in front of it might be.
//
// A red light is deliberately NOT a trigger, at EITHER arm: a red at v's
// own stop line holds it via sigGate with boxBlocked false, and a red one
// stub past the box caps the exit walk's room as a holdSeal, which both
// arms discard. A signal queue — however punishing the cycle — never
// reaches the escape. A signal that never goes green is a broken program,
// a different pathology with its own diagnosis.
func (e *Engine) jammedAtJunction(v *Vehicle, lane *Lane) bool {
	if a := lane.vehs; len(a) == 0 || a[len(a)-1] != v {
		return false
	}
	if len(lane.Successors) == 0 {
		return false // a dead end; the wall clamp (WallHits) owns that
	}
	next := e.pickSuccessor(lane, v)
	if next.Internal {
		blocked, holdSeal := e.boxWalk(v, next)
		return blocked && !holdSeal
	}
	if !lane.Internal {
		return false
	}
	// v is INSIDE a box (lane.Internal) and its routed exit is an ordinary
	// road: the entry-time gate no longer owns this wait — v is already
	// in. The wait that seals a box for good is the exit chain having no
	// room for v at all, which is the entry gate's own test run from v's
	// current lane. Measured on base34 (ADR-0034 consequences): the 5
	// lanes still frozen at the horizon were all internal — boxes occupied
	// for the rest of the run, every crossing movement through them
	// blocked, and this condition is what reaches them.
	//
	// Two refinements, both review-found: a HOLDING stop line is not a
	// seal (a downstream red capping the room is stop-line starvation, the
	// signal's domain — the doctrine above holds at this arm exactly as at
	// the entry arm), and the walk's first hop is the crossing that will
	// CONSUME v's held turn, so it runs with turnSpent=false (moot on
	// netimport networks, whose internal lanes are single-successor —
	// movement per internal lane — but controllers can set HeldTurn any
	// time, and the engine must not classify against a branch the vehicle
	// will not take).
	blocked, holdSeal := e.exitWalk(v, lane, false, false)
	return blocked && !holdSeal
}

// resetStuckBehind clears the stuck timer of every vehicle on lane and on
// the lanes feeding it, walking Prevs breadth-first up to maxLaneHops.
//
// This is what keeps the escape MINIMAL rather than blunt. Everything queued
// behind the stranded vehicle has been stopped exactly as long as it was and
// is exactly as eligible; without the reset, a frozen cycle loses every head
// on the same tick — measured on the ring fixture, eight removals where one
// unlocks the ring. The backward closure is the set that was waiting on the
// vehicle just removed, so giving it another StrandAfterS costs a delay and
// buys the difference between "one car was taken out of the jam" and "the
// jam was deleted".
//
// Backward, not forward, and bounded: a cycle closes on itself within the
// hop limit (the measured Chicago cycle is 8 lanes), while a long arterial
// queue is reset only as far back as the limit reaches — vehicles further
// upstream keep their timers and remain eligible, which is correct. They are
// waiting on a different piece of road.
func (e *Engine) resetStuckBehind(lane *Lane) {
	seen := map[*Lane]bool{lane: true}
	work := []*Lane{lane}
	for hops := 0; hops < maxLaneHops && len(work) > 0; hops++ {
		var next []*Lane
		for _, l := range work {
			for _, v := range l.vehs {
				v.stuckTicks = 0
			}
			for _, p := range l.Prevs {
				if !seen[p] {
					seen[p] = true
					next = append(next, p)
				}
			}
		}
		work = next
	}
}

// dropDespawned compacts e.order and the ID index after vehicles have had
// their lane cleared. Shared by the boundary despawn and the strand.
func (e *Engine) dropDespawned() {
	kept := e.order[:0]
	for _, v := range e.order {
		if v.Lane != nil {
			kept = append(kept, v)
		}
	}
	e.order = kept
	e.index = make(map[uint64]*Vehicle, len(kept))
	for _, v := range kept {
		e.index[v.ID] = v
	}
}
