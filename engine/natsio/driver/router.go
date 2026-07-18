package driver

import (
	"math"

	"traffic-sim/engine"
)

// router.go — the default driver's routing: Dijkstra over the lane
// successor graph, edge weight = lane length. The engine's networks are
// small; the plain O(V²) form is the right size. The route feeds the
// routing intent axis (destination lane id) and the turn choice at
// junctions (next hop when a lane offers more than one successor).

// Route computes the shortest lane path (by total lane length) from the
// lane at fromIdx to the lane destID. The returned path is lane indices
// including both endpoints; ok=false when the destination is unknown or
// unreachable.
func Route(net *engine.Network, fromIdx int, destID string) ([]int, bool) {
	dest := net.LaneByID(destID)
	if dest == nil || fromIdx < 0 || fromIdx >= len(net.Lanes) {
		return nil, false
	}
	n := len(net.Lanes)
	dist := make([]float64, n)
	prev := make([]int, n)
	done := make([]bool, n)
	for i := range dist {
		dist[i], prev[i] = math.Inf(1), -1
	}
	dist[fromIdx] = 0
	for {
		u := -1
		for i := 0; i < n; i++ {
			if !done[i] && (u < 0 || dist[i] < dist[u]) {
				u = i
			}
		}
		if u < 0 || math.IsInf(dist[u], 1) || u == dest.Index {
			break
		}
		done[u] = true
		for _, succ := range net.Lanes[u].Successors {
			if nd := dist[u] + succ.Length; nd < dist[succ.Index] {
				dist[succ.Index], prev[succ.Index] = nd, u
			}
		}
	}
	if math.IsInf(dist[dest.Index], 1) {
		return nil, false
	}
	var path []int
	for u := dest.Index; u >= 0; u = prev[u] {
		path = append(path, u)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, true
}
