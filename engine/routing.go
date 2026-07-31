package engine

import (
	"container/heap"
	"math"
)

// routing.go — route-axis following. The Route intent (intent.go) is the
// persistent routing axis: a destination lane id, set once by a controller
// and carried on the vehicle (keyframe-persisted, obs-frame-visible). The
// kernel resolves it at every multi-successor lane: the vehicle takes the
// successor on the shortest path (by FREE-FLOW TIME — lane length over speed
// limit) to its destination instead of the leftmost-successor default. A
// held turn intent still wins — an explicit junction choice beats the route
// for that crossing.
//
// Next-hop tables are computed once per destination lane by a reverse
// Dijkstra and memoized on the Engine; the network is immutable for the
// run, so a table never invalidates. The cache is derived state: not
// serialized, not folded into the CRC, recomputed identically on demand.
//
// Determinism (ADR-0005): adjacency walks fixed slice order (predecessor
// lists are built in lane-then-successor order), heap ties break toward
// the lower lane index, equal-cost relaxations keep the first (fixed)
// order's choice, and no map iteration feeds any decision.

// routeNextHop returns the successor of lane lying on the shortest path to
// the route destination destID, or nil when the destination is unknown,
// unreachable from lane, or lane IS the destination (the caller then
// applies its default — a vehicle on its destination lane is done being
// routed).
func (e *Engine) routeNextHop(lane *Lane, destID string) *Lane {
	dest := e.Net.LaneByID(destID)
	if dest == nil || dest == lane {
		return nil
	}
	hop := e.routeTable(dest.Index)[lane.Index]
	if hop < 0 || hop == int32(lane.Index) {
		return nil
	}
	next := e.Net.Lanes[hop]
	// Table entries are successors of lane by construction; verify anyway —
	// a corrupt table must degrade to the caller's default, never teleport
	// the vehicle onto a non-successor lane.
	for _, s := range lane.Successors {
		if s == next {
			return next
		}
	}
	return nil
}

// routeLatDist returns the LATERAL DEPTH of lane toward destID: the minimum
// number of lane changes a vehicle on lane must still make before it sits on
// a lane whose successors reach the destination. 0 means the destination is
// reachable from here by driving alone; −1 means it is unreachable at ANY
// depth.
//
// This is the LATERAL half of route following (ADR-0021). routeNextHop only
// chooses among a lane's successors, so a vehicle that changes into a lane
// the destination is unreachable from has silently abandoned its route —
// measured on chi-loop, 28% of multi-lane positions that can reach a given
// destination have a lateral neighbour that cannot, and route-blind MOBIL
// walked 92% of routed vehicles off their route before they arrived.
//
// The first cut of that fix used the boolean predicate "depth == 0". A
// GRADIENT is what makes recovery work more than one lane out: a vehicle in
// the left lane of a 3-lane arterial whose exit is on the right has NO
// route-reachable neighbour at all, so a predicate leaves it stranded, while
// a gradient walks it across one lane per hop.
//
// An unknown destination reads as depth 0 everywhere, so the lateral
// guardrail degrades to pre-ADR-0021 behavior rather than pinning vehicles in
// lane — the same convention routeNextHop uses.
func (e *Engine) routeLatDist(lane *Lane, destID string) int32 {
	dest := e.Net.LaneByID(destID)
	if dest == nil {
		return 0
	}
	return e.routeLatDepth(dest.Index)[lane.Index]
}

// routeLatDepth returns — computing and memoizing on first use — the lateral
// depth table toward the destination lane at destIdx. One int32 array per
// destination, alongside the next-hop table, on the same never-invalidates
// grounds (the network is immutable for the run).
//
// It is a 0-1 BFS from the destination over the REVERSED lane graph:
// a successor edge p→u costs 0 lane changes (the vehicle just drives it) and
// a lateral link costs 1 (it has to hop). Written in the LAYERED form rather
// than with a deque — expand the 0-cost closure of the current layer, then
// take one lateral step to seed the next — which is the same algorithm and
// the same O(V+E), needs no container/list, and makes the two edge costs
// legible as two loops.
//
// Determinism (ADR-0005): predecessor lists are in fixed lane-then-successor
// order, laterals are visited Left then Right, first assignment wins, and no
// map iteration feeds the result.
func (e *Engine) routeLatDepth(destIdx int) []int32 {
	if t, ok := e.latTabs[destIdx]; ok {
		return t
	}
	n := len(e.Net.Lanes)
	preds := e.routePreds()
	depth := make([]int32, n)
	for i := range depth {
		depth[i] = -1
	}
	depth[destIdx] = 0
	frontier := []int32{int32(destIdx)}
	for d := int32(0); len(frontier) > 0; d++ {
		// 0-cost closure: everything that can DRIVE to this layer is in it.
		layer := frontier
		for i := 0; i < len(layer); i++ {
			for _, p := range preds[layer[i]] {
				if depth[p] < 0 {
					depth[p] = d
					layer = append(layer, p)
				}
			}
		}
		// 1-cost step: the lateral neighbours of the whole layer seed d+1.
		// Lateral links are mutual at every construction site (network.go,
		// netfile.go, scenario_i80.go), so the lanes that can hop INTO u are
		// exactly u.Left and u.Right.
		var next []int32
		for _, u := range layer {
			l := e.Net.Lanes[u]
			for _, nb := range [2]*Lane{l.Left, l.Right} {
				if nb != nil && depth[nb.Index] < 0 {
					depth[nb.Index] = d + 1
					next = append(next, int32(nb.Index))
				}
			}
		}
		frontier = next
	}
	if e.latTabs == nil {
		e.latTabs = map[int][]int32{}
	}
	e.latTabs[destIdx] = depth
	return depth
}

// freeFlowTime is the routing edge weight: seconds to cross the lane at its
// speed limit, empty. A zero/negative limit degrades to the lane's length —
// i.e. an assumed 1 m/s, arbitrary but unreachable: netfile validation
// rejects non-positive limits (netfile.go), and the built-in constructors
// pass a shared positive limit.
func freeFlowTime(l *Lane) float64 {
	if l.SpeedLimit <= 0 {
		return l.Length
	}
	return l.Length / l.SpeedLimit
}

// routeTable returns — computing and memoizing on first use — the next-hop
// table toward the destination lane at destIdx: tab[i] is the index of the
// successor of lane i on the shortest-FREE-FLOW-TIME path to the destination,
// −1 when lane i cannot reach it (or is the destination itself).
func (e *Engine) routeTable(destIdx int) []int32 {
	if t, ok := e.routeTabs[destIdx]; ok {
		return t
	}
	n := len(e.Net.Lanes)
	preds := e.routePreds()
	dist := make([]float64, n)
	next := make([]int32, n)
	for i := range dist {
		dist[i], next[i] = math.Inf(1), -1
	}
	dist[destIdx] = 0
	done := make([]bool, n)
	h := &routeHeap{{idx: destIdx}}
	for h.Len() > 0 {
		u := heap.Pop(h).(routeItem).idx
		if done[u] {
			continue
		}
		done[u] = true
		// Reverse relaxation: forward edge p→u costs the FREE-FLOW TIME of
		// the lane entered (length / speed limit). Measured on chi-loop-urban
		// (2026-07): length weights route traffic through short alleys in
		// preference to faster arterials, concentrating flow on exactly the
		// links that saturate first. A zero/negative limit degrades to the
		// lane's length rather than dividing by zero.
		ul := e.Net.Lanes[u]
		w := dist[u] + freeFlowTime(ul)
		for _, p := range preds[u] {
			pi := int(p)
			if !done[pi] && w < dist[pi] {
				dist[pi] = w
				next[pi] = int32(u)
				heap.Push(h, routeItem{idx: pi, dist: w})
			}
		}
	}
	if e.routeTabs == nil {
		e.routeTabs = map[int][]int32{}
	}
	e.routeTabs[destIdx] = next
	return next
}

// routePreds returns — building once — the predecessor adjacency of the
// lane graph, in fixed lane-then-successor order.
func (e *Engine) routePreds() [][]int32 {
	if e.preds != nil {
		return e.preds
	}
	preds := make([][]int32, len(e.Net.Lanes))
	for _, l := range e.Net.Lanes {
		for _, s := range l.Successors {
			preds[s.Index] = append(preds[s.Index], int32(l.Index))
		}
	}
	e.preds = preds
	return preds
}

// routeItem is a Dijkstra heap entry; ties break toward the lower lane
// index so equal-cost runs pop in a fixed order.
type routeItem struct {
	idx  int
	dist float64
}

type routeHeap []routeItem

func (h routeHeap) Len() int { return len(h) }
func (h routeHeap) Less(i, j int) bool {
	if h[i].dist != h[j].dist {
		return h[i].dist < h[j].dist
	}
	return h[i].idx < h[j].idx
}
func (h routeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *routeHeap) Push(x any)   { *h = append(*h, x.(routeItem)) }
func (h *routeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
