package driver

import (
	"sort"

	"traffic-sim/engine"
)

// destinations.go — per-vehicle exit-destination assignment
// (Config.ExitRouting): each claimed vehicle draws one destination among
// the network's exit lanes, weighted by speed limit (a road-class proxy:
// higher-class exits attract more traffic), so circulation follows
// plausible desire lines instead of the kernel's leftmost-successor
// default. Config.Destination (the single explicit override) wins when
// set; this path only fills the "no destination configured" case.
//
// Determinism (ADR-0007): the draw comes from a FRESH per-vehicle stream
// derived from (run seed, vehicle ID) — never the live policy stream, whose
// draw count depends on how long the vehicle has been driven. The candidate
// sequence is therefore a pure function of seed + ID + network, and a
// failover replica or orphan re-claimer re-derives the identical
// destination whenever the reachability filter and the bounded reroll's
// self-lane check evaluate the same — the common case, and the route
// axis' engine-side persistence covers the rest. No map iteration and no
// wall-clock anywhere: the candidate list is sorted by lane ID before the
// weighted draw.

// minExitLen excludes clipping fragments (netimport edge stubs of a few
// meters) from the candidate pool.
const minExitLen = 30.0

// exitAttempts bounds the reroll loop in pickExit.
const exitAttempts = 8

// exitCandidates lists the network's eligible exit lanes sorted by ID —
// the fixed order the weighted draw walks.
func exitCandidates(net *engine.Network) []*engine.Lane {
	var lanes []*engine.Lane
	for _, l := range net.Lanes {
		if l.Exit && l.Length >= minExitLen {
			lanes = append(lanes, l)
		}
	}
	sort.Slice(lanes, func(i, j int) bool { return lanes[i].ID < lanes[j].ID })
	return lanes
}

// reachableLanes marks every lane reachable from fromIdx through the
// successor graph (BFS over the fixed lane order — deterministic).
func reachableLanes(net *engine.Network, fromIdx int) []bool {
	reach := make([]bool, len(net.Lanes))
	reach[fromIdx] = true
	queue := []int{fromIdx}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		for _, succ := range net.Lanes[u].Successors {
			if !reach[succ.Index] {
				reach[succ.Index] = true
				queue = append(queue, succ.Index)
			}
		}
	}
	return reach
}

// pickExit draws a destination exit lane for vehicleID: speed-limit
// weighted among the eligible exits REACHABLE from the vehicle's current
// lane (unreachable ones are filtered up front — the "no path" case never
// enters the draw), rerolled (bounded by exitAttempts) while the draw
// lands on the current lane itself. ok=false when no candidate qualifies
// — the caller then sends no route and the kernel default applies.
func pickExit(net *engine.Network, seed, vehicleID uint64, fromIdx int) (string, bool) {
	if fromIdx < 0 || fromIdx >= len(net.Lanes) {
		return "", false
	}
	reach := reachableLanes(net, fromIdx)
	var cands []*engine.Lane
	total := 0.0
	for _, l := range exitCandidates(net) {
		if reach[l.Index] {
			cands = append(cands, l)
			total += l.SpeedLimit
		}
	}
	if len(cands) == 0 || total <= 0 {
		return "", false
	}
	s := engine.DeriveStream(seed, vehicleID)
	from := net.Lanes[fromIdx].ID
	for range exitAttempts {
		r := s.Float64() * total
		pick := cands[len(cands)-1]
		for _, l := range cands {
			if r < l.SpeedLimit {
				pick = l
				break
			}
			r -= l.SpeedLimit
		}
		if pick.ID != from {
			return pick.ID, true
		}
	}
	return "", false
}
