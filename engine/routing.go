package engine

import (
	"container/heap"
	"math"
)

// routing.go — route-axis following. The Route intent (intent.go) is the
// persistent routing axis: a destination lane id, set once by a controller
// and carried on the vehicle (keyframe-persisted, obs-frame-visible). The
// kernel resolves it at every multi-successor lane: the vehicle takes the
// successor on the shortest path (by lane length) to its destination
// instead of the leftmost-successor default. A held turn intent still wins
// — an explicit junction choice beats the route for that crossing.
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

// routeTable returns — computing and memoizing on first use — the next-hop
// table toward the destination lane at destIdx: tab[i] is the index of the
// successor of lane i on the shortest path to the destination, −1 when
// lane i cannot reach it (or is the destination itself).
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
		// Reverse relaxation: forward edge p→u costs u.Length (the lane
		// entered), the same weight convention the retired driver-side
		// router used.
		w := dist[u] + e.Net.Lanes[u].Length
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
