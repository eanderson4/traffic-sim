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
// Adaptive mode (ADR-0036, Params.AdaptiveRouting): the weights stop being
// free-flow constants. Each lane carries ttEMA, a smoothed travel time fed
// by vehicle dwell samples and relaxed back toward free flow every tick
// (engine.go), frozen into ttSnap at each 60-second epoch rollover, and the
// memoized tables gain an epoch stamp — accessing a stale table recomputes
// it over the frozen weights, immediately and unmetered, so every table in
// play is always current-epoch and a restore rebuilds exactly what the
// live engine served. Recomputation is damped by hysteresis against the
// STATIC free-flow table (a lane keeps its free-flow next-hop unless the
// new path beats it by more than max(30 s, 15%), so a shared table cannot
// flip the whole flow between two arms every epoch) and guarded by a
// constructive reachability check (rerouteTable). The static reference is
// what makes every table a pure function of (frozen weights, network) —
// no hysteresis history to keyframe. The lateral-depth tables below are
// topology-based and do NOT participate. Stamps and table caches stay
// derived state; ttSnap is keyframed. With the flag off none of this runs
// and every byte is what the static builder produced.
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

// routeWeight is the edge weight the table builder uses: the lane's
// EPOCH-FROZEN travel time (ttSnap, snapshotted from the EMA at each epoch
// rollover, engine.go) while AdaptiveRouting is on — the freeze is what
// makes a table a pure function of (keyframed state, network), so a
// mid-epoch restore recomputes exactly the table the live engine was
// serving. With the flag off it is free-flow time, and the builder runs
// once per destination and never again.
func (e *Engine) routeWeight(l *Lane) float64 {
	if e.Params.AdaptiveRouting {
		return e.ttSnap[l.Index]
	}
	return freeFlowTime(l)
}

// routeTable returns — computing and memoizing on first use — the next-hop
// table toward the destination lane at destIdx: tab[i] is the index of the
// successor of lane i on the shortest-FREE-FLOW-TIME path to the destination,
// −1 when lane i cannot reach it (or is the destination itself).
//
// Adaptive mode (ADR-0036 §2): the memoized entry carries an epoch stamp
// (epoch = tick / e.routeEpochTicks). Serving a stale entry recomputes it
// over the epoch-frozen weights, IMMEDIATELY and unmetered — the same rule
// as a first-use build. A budget that served stale tables under pressure
// was designed here and rejected in review: tables from different epochs
// coexisting is what a mid-run restore cannot reproduce (the old freeze is
// gone), so every table in play is always current-epoch — a pure function
// of (frozen weights, network), bit-exact across restore. The price is an
// epoch-boundary CPU spike of one Dijkstra per destination actually asked
// for that epoch, on the tick it is first asked.
func (e *Engine) routeTable(destIdx int) []int32 {
	if t, ok := e.routeTabs[destIdx]; ok {
		if !e.Params.AdaptiveRouting {
			return t
		}
		epoch := e.Tick / e.routeEpochTicks
		if e.routeEpochs[destIdx] >= epoch {
			return t
		}
		t = e.rerouteTable(destIdx)
		e.routeTabs[destIdx] = t
		e.routeEpochs[destIdx] = epoch
		return t
	}
	var next []int32
	if e.Params.AdaptiveRouting {
		// First-use builds go through the SAME hysteretic construction as
		// stale recomputes: after a mid-epoch restore every cache is cold,
		// and a raw Dijkstra would take hops the uninterrupted run's
		// hysteresis rejected — a replay divergence on marginal congestion
		// (external review, 2026-07-30). At epoch 0 the frozen weights ARE
		// free flow, so this reduces exactly to the static table.
		next = e.rerouteTable(destIdx)
	} else {
		next, _ = e.routeDijkstra(destIdx, e.routeWeight)
	}
	if e.routeTabs == nil {
		e.routeTabs = map[int][]int32{}
	}
	e.routeTabs[destIdx] = next
	if e.routeEpochs != nil {
		e.routeEpochs[destIdx] = e.Tick / e.routeEpochTicks
	}
	return next
}

// staticRouteTable memoizes the FREE-FLOW next-hop table toward destIdx —
// the hysteresis reference for adaptive recomputes. The network is
// immutable for the run, so this never invalidates; it is exactly the
// table the flag-off builder produces.
func (e *Engine) staticRouteTable(destIdx int) []int32 {
	if t, ok := e.staticTabs[destIdx]; ok {
		return t
	}
	next, _ := e.routeDijkstra(destIdx, freeFlowTime)
	if e.staticTabs == nil {
		e.staticTabs = map[int][]int32{}
	}
	e.staticTabs[destIdx] = next
	return next
}

// rerouteTable recomputes the next-hop table toward destIdx over the
// epoch-frozen travel-time weights and reconciles it with the STATIC
// free-flow table under hysteresis (ADR-0036 §2): lane i keeps its
// free-flow next-hop unless the new path beats the free-flow path's cost
// (under CURRENT weights) by more than max(30 s, 15%). Without the margin
// the whole flow flips between two near-equal arms every epoch
// (braess-style oscillation); with it only clearly-better alternatives
// divert, and traffic falls back to the free-flow routes as congestion
// clears.
//
// The reference is the static table, not the previous epoch's adaptive
// table, for one reason above all: replay. A table is then a PURE FUNCTION
// of (epoch-frozen weights, network) — no hysteresis history to keyframe,
// and a restore at any tick recomputes exactly what the live engine served
// (external review, 2026-07-30).
//
// ACYCLICITY IS CONSTRUCTED, not argued. Splicing individual Dijkstra hops
// into the static tree can in principle close a cycle (the additive 30 s
// margin defeats the proportional-telescoping argument, and a free-flow
// loop would feed floored dwell samples that never break it — both review
// findings). So a candidate hop is installed only if the MIXED chain from
// i — with every candidate before i already installed — still reaches the
// destination within one hop per lane. Candidates install in fixed lane
// order; the induction is that reachability holds for the static table
// and each accepted splice preserves it, so the served table is always a
// functional graph whose every path terminates at the destination.
// Costs are the REALIZED ones: the margin is earned by the mixed chain
// the vehicle will actually drive, not the fresh Dijkstra's idealized
// path (downstream lanes whose own margins did not clear keep static
// hops, so the two can differ — review round 4).
func (e *Engine) rerouteTable(destIdx int) []int32 {
	fresh, _ := e.routeDijkstra(destIdx, e.routeWeight)
	next := e.staticRouteTable(destIdx)
	mixed := make([]int32, len(next))
	copy(mixed, next)
	for i := range fresh {
		if fresh[i] == next[i] || fresh[i] < 0 {
			continue
		}
		// Two walks per candidate. The STATIC chain gives the cost to beat
		// (the free-flow path under current weights, same convention as the
		// builder: successors summed, destination included, i excluded).
		oldCost := math.Inf(1)
		if j := int(next[i]); j >= 0 {
			cost, reached := 0.0, false
			for hops := 0; hops <= len(next); hops++ {
				cost += e.routeWeight(e.Net.Lanes[j])
				if j == destIdx {
					reached = true
					break
				}
				if j = int(next[j]); j < 0 {
					break
				}
			}
			if reached {
				oldCost = cost
			}
		}
		// The MIXED chain with the candidate installed gives both the
		// reachability guarantee and the REALIZED cost: downstream lanes
		// whose own margins did not clear keep static hops, so the path
		// the vehicle actually drives can cost more than the fresh
		// Dijkstra's idealized one — the margin must be earned by the
		// path that will be served (review round 4). The candidate is
		// installed BEFORE the walk and reverted on failure: validating
		// with i's stale static hop still in place lets a chain that
		// returns to i escape through it and read as reaching the
		// destination, and the install then closes a real cycle (review
		// round 5).
		mixed[i] = fresh[i]
		j, reached := int(fresh[i]), false
		mixedCost := math.Inf(1)
		{
			cost := 0.0
			for hops := 0; hops <= len(mixed); hops++ {
				cost += e.routeWeight(e.Net.Lanes[j])
				if j == destIdx {
					reached = true
					break
				}
				if j = int(mixed[j]); j < 0 {
					break
				}
			}
			if reached {
				mixedCost = cost
			}
		}
		if !reached {
			mixed[i] = next[i] // the splice strands i or closes a cycle
			continue
		}
		if !math.IsInf(oldCost, 1) {
			margin := 30.0
			if m := 0.15 * oldCost; m > margin {
				margin = m
			}
			if mixedCost >= oldCost-margin {
				mixed[i] = next[i] // not CLEARLY better: keep the free-flow hop
				continue
			}
		}
	}
	return mixed
}

// routeDijkstra runs the reverse Dijkstra toward destIdx over the given
// edge weight and returns the next-hop table and the cost-to-destination
// of every lane. This is the static builder's exact algorithm — same
// predecessor order, same heap tie-breaks, same strict-< relaxation — so
// with weights ≡ free flow its tables are the static ones bit-for-bit.
//
// Reverse relaxation: forward edge p→u costs the travel time of the lane
// ENTERED. Measured on chi-loop-urban (2026-07): length weights route
// traffic through short alleys in preference to faster arterials,
// concentrating flow on exactly the links that saturate first.
func (e *Engine) routeDijkstra(destIdx int, weight func(*Lane) float64) (next []int32, dist []float64) {
	n := len(e.Net.Lanes)
	preds := e.routePreds()
	dist = make([]float64, n)
	next = make([]int32, n)
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
		w := dist[u] + weight(e.Net.Lanes[u])
		for _, p := range preds[u] {
			pi := int(p)
			if !done[pi] && w < dist[pi] {
				dist[pi] = w
				next[pi] = int32(u)
				heap.Push(h, routeItem{idx: pi, dist: w})
			}
		}
	}
	return next, dist
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
