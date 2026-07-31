package engine

import "fmt"

// Lane is the navigable atom (arch-road-graph-model synthesis): vehicles
// occupy (laneID, s) continuously and are laterally on exactly one lane.
// Lanes are built in code (BuildNet test networks) or loaded from a
// compiled network file (netfile.go, format v1).
type Lane struct {
	ID      string
	Section string // "ring" | "A" | "B" | "main" | edge id — metrics/tests group by this
	Index   int    // fixed position in Network.Lanes; canonical for CRC
	// Length is immutable in production paths after NewEngine (a test may
	// mutate it to provoke a NewKernel reject): TotalLaneKm's cache and
	// routing's memoized tables are filled from it (a mid-run mutation
	// would silently keep the stale values).
	Length     float64
	SpeedLimit float64
	Width      float64 // m; 0 on the in-code test networks (geometry only)

	Successors []*Lane // empty on Exit and EndWall lanes; in-code M1 nets: at most one
	Prevs      []*Lane // reverse links, derived by linkPrevs
	Left       *Lane   // lateral neighbors within the same edge (nil at edges)
	Right      *Lane

	Exit    bool // network exit: vehicles despawn past Length; no wall
	EndWall bool // dead-end lane (dropped lane): virtual standing vehicle at Length

	// Junction right-of-way (ADR-0010, compiled networks only). Internal
	// marks a junction-interior lane; Junction groups it; Row is the
	// priority class of the approach whose connection it serves; FoesCross
	// and FoesMerge are the conflicting interior lanes of the same junction
	// (crossing paths vs. paths merging into the same exit). All empty on
	// the in-code M1–M3 networks and on unmodeled junctions.
	Internal  bool
	Junction  string
	Row       RowState
	FoesCross []*Lane
	FoesMerge []*Lane

	// Fixed-time signal control (ADR-0011, compiled networks only): the
	// program of the junction this internal lane serves and the lane's link
	// index into its phase state strings. Nil on approach lanes, in-code
	// networks, and unsignalized junctions.
	Signal  *SignalProgram
	LinkIdx int

	// Geometry (compiled networks): per-lane centerline polyline in the
	// local metric frame plus a constant lateral offset (left-positive).
	// Display-only — never feeds the dynamics or the CRC. Set via SetShape.
	Shape     []Point
	LatOffset float64
	cum       []float64 // cumulative arc lengths, built by SetShape

	vehs []*Vehicle // occupancy sorted by S ascending; rebuilt every tick

	// ttEMA is the lane's smoothed travel time in seconds (ADR-0036 §1):
	// free-flow time at build, updated by vehicle dwell samples (α = 1/8)
	// and relaxed back toward free flow every tick with a 10-minute time
	// constant. It is the congestion signal the epoch recompute weights its
	// Dijkstra with. State that decides behavior → keyframed (format v6,
	// the stuckTicks precedent); not in the CRC, which stays the trajectory
	// oracle. Zero-valued and unused while Params.AdaptiveRouting is off.
	ttEMA float64
}

// Network is a fixed-order lane list plus spawn origins. Maps here are for
// ID lookup only — simulation logic never iterates them (ADR-0005).
type Network struct {
	Lanes   []*Lane
	Origins []*Lane
	// Signals holds the fixed-time signal programs (ADR-0011) in file
	// order; nil on networks without signalized junctions.
	Signals []*SignalProgram
	byID    map[string]*Lane
	// totalLaneKm caches TotalLaneKm (lazy; callers are run-loop goroutine
	// only — the per-tick density checks). Slice order is fixed, so the
	// cached value is bit-identical to recomputation.
	totalLaneKm    float64
	totalLaneKmSet bool
}

// LaneByID looks up a lane by ID (lookup only, never iterated).
func (n *Network) LaneByID(id string) *Lane { return n.byID[id] }

// OriginByID resolves a spawn origin lane by ID (linear scan of the small,
// fixed Origins list — no map, no iteration-order hazard). Director spawn
// verbs must name an origin, matching the Spawner's domain.
func (n *Network) OriginByID(id string) *Lane {
	for _, l := range n.Origins {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// TotalLaneKm is the total lane length in kilometres (density denominator).
func (n *Network) TotalLaneKm() float64 {
	if !n.totalLaneKmSet {
		var l float64
		for _, ln := range n.Lanes {
			l += ln.Length
		}
		n.totalLaneKm = l / 1000
		n.totalLaneKmSet = true
	}
	return n.totalLaneKm
}

// NetSpec parameterizes the in-code test networks and file-loaded networks.
// It is part of the run log, so replay rebuilds the identical graph.
type NetSpec struct {
	Kind            string  // "ring" | "lanedrop" | "straight" | "i80" | "file"
	Path            string  // file: compiled network JSON (format v1, netfile.go)
	Length          float64 // ring: circumference; straight: length (m). 0 → default
	SectionLen      float64 // lanedrop: length of EACH of the two sections (m). 0 → 600
	SpeedLimit      float64 // m/s. 0 → kind default (ring 30 km/h, i80 65 mph, others 33.3)
	DownstreamLimit float64 // i80: speed limit of the post-drop downstream section (m/s). 0 → same as SpeedLimit
	DropLanes       int     // i80: lanes remaining past the downstream drop. 0 → 4
}

// BuildNet constructs one of the test networks or loads a compiled network:
//
//	ring:     single lane, its own successor (Sugiyama-style closed loop)
//	lanedrop: 3-lane section A feeding 2-lane section B; the leftmost A lane
//	          (A2) is dropped — it has no successor and a virtual wall at its
//	          end, so its traffic must merge into A1
//	straight: single exit lane (test harness for IDM sanity checks)
//	i80:      the NGSIM I-80 credibility scenario (scenario_i80.go)
//	file:     a compiled network JSON (format v1) from Path — e.g. the
//	          netconvert bootstrap's output (engine/cmd/netimport)
//
// Reverse longitudinal links (Lane.Prevs) are derived after construction.
func BuildNet(spec NetSpec) (*Network, error) {
	var n *Network
	switch spec.Kind {
	case "ring":
		length := spec.Length
		if length == 0 {
			length = 230 // Sugiyama ring
		}
		limit := spec.SpeedLimit
		if limit == 0 {
			limit = 30.0 / 3.6 // Sugiyama target speed 30 km/h
		}
		r := &Lane{ID: "R0", Section: "ring", Index: 0, Length: length, SpeedLimit: limit}
		r.Successors = []*Lane{r} // closed loop
		n = &Network{Lanes: []*Lane{r}, byID: map[string]*Lane{"R0": r}}

	case "lanedrop":
		sec := spec.SectionLen
		if sec == 0 {
			sec = 600
		}
		limit := spec.SpeedLimit
		if limit == 0 {
			limit = 33.3
		}
		a0 := &Lane{ID: "A0", Section: "A", Index: 0, Length: sec, SpeedLimit: limit}
		a1 := &Lane{ID: "A1", Section: "A", Index: 1, Length: sec, SpeedLimit: limit}
		a2 := &Lane{ID: "A2", Section: "A", Index: 2, Length: sec, SpeedLimit: limit, EndWall: true}
		b0 := &Lane{ID: "B0", Section: "B", Index: 3, Length: sec, SpeedLimit: limit, Exit: true}
		b1 := &Lane{ID: "B1", Section: "B", Index: 4, Length: sec, SpeedLimit: limit, Exit: true}
		a0.Successors = []*Lane{b0}
		a1.Successors = []*Lane{b1}
		// A2 (leftmost) is the dropped lane: no successor, wall at the end.
		a0.Left, a1.Right = a1, a0
		a1.Left, a2.Right = a2, a1
		b0.Left, b1.Right = b1, b0
		lanes := []*Lane{a0, a1, a2, b0, b1}
		n = &Network{Lanes: lanes, Origins: []*Lane{a0, a1, a2}, byID: make(map[string]*Lane, len(lanes))}
		for _, l := range lanes {
			n.byID[l.ID] = l
		}

	case "straight":
		length := spec.Length
		if length == 0 {
			length = 5000
		}
		limit := spec.SpeedLimit
		if limit == 0 {
			limit = 33.3
		}
		m := &Lane{ID: "S0", Section: "main", Index: 0, Length: length, SpeedLimit: limit, Exit: true}
		n = &Network{Lanes: []*Lane{m}, Origins: []*Lane{m}, byID: map[string]*Lane{"S0": m}}

	case "i80":
		var err error
		n, err = buildI80Net(spec)
		if err != nil {
			return nil, err
		}

	case "file":
		var err error
		n, err = LoadNetFile(spec.Path)
		if err != nil {
			return nil, err
		}

	default:
		return nil, fmt.Errorf("unknown network kind %q (want ring|lanedrop|straight|i80|file)", spec.Kind)
	}
	linkPrevs(n)
	return n, nil
}

// linkPrevs derives the reverse longitudinal links (fixed iteration order, so
// Prevs order is canonical). Lane-change safety checks use them to see
// followers across a lane boundary; M1 networks have at most one predecessor
// per lane, mirroring the single-successor convention.
func linkPrevs(n *Network) {
	for _, l := range n.Lanes {
		for _, succ := range l.Successors {
			succ.Prevs = append(succ.Prevs, l)
		}
	}
}
