package engine

// M2 credibility scenario: the NGSIM I-80 Emeryville site, 2005-04-13
// 17:00–17:15 — the window whose Edie x-t field measured a −18.1 km/h
// backward wave (analysis/ngsim/README.md). Geometry and demand are derived
// from the raw trajectories (data/ngsim/i80-1700-1715.csv):
//
//   - Surveyed section: 6 lanes × 1,791 ft ≈ 546 m (lane 1 = HOV/leftmost …
//     lane 6 = rightmost). Lane index 0 here is the RIGHTMOST lane (NGSIM
//     lane 6), matching the lanedrop network's chaining convention.
//   - On-ramp (lane 7 in the data, tracked over y ≈ 223–700 ft) merges into
//     the rightmost lane at y ≈ 700 ft ≈ 213 m.
//   - Demand (veh/h, entries at the upstream boundary ×4): lane 6: 688,
//     lane 5: 972, lane 4: 1056, lane 3: 988, lane 2: 1300, lane 1: 1664;
//     on-ramp: 932. Total ≈ 7,600 veh/h. (Per-lane split approximated by
//     first-seen lane; the total is exact.)
//   - Vehicle mix: 95.2% car, 3.5% truck (1.2% motorcycles folded into cars).
//
// Boundary condition: during the window the section sits inside a spillback
// queue whose bottleneck is DOWNSTREAM of the surveyed area (the field data
// shows section speeds decaying 34 → 9 ft/s from the window start to its
// end; the queue tail creeps backward at ≈ 4 km/h, i.e. the queue GREW
// through the window). Data-honest demand (7,600 veh/h vs an IDM capacity of
// ≈ 1,690 veh/h/lane over 6 lanes) does not overload the section, and an
// on-ramp-only bottleneck was verified to stay free-flowing (i80xt -slow 29).
// A speed-limited downstream segment was also verified to fail: it throttles
// the queue discharge far below capacity, and the wave speed scales with
// that discharge (c ≈ −(l+s0)·q), yielding −3 km/h undulations in a solid
// crawl — not stop-and-go.
//
// The scenario therefore represents the off-site queue as a 6→5 lane drop
// downstream of the surveyed section. With the M3 physics (merge gap
// enforcement + the instability-capable IDM calibration), the funnel's
// sustained discharge measures ≈ 7,680 veh/h (probe: constant 1.20× demand)
// — the M2 merge-overlap losses that throttled it to ≈ 7,300 are gone, so
// M2's quasi-stationary window (data demand ≈ discharge) drains the queue
// instead of sustaining it. The reference run therefore holds demand at
// 1.20× data throughout: the resulting shortfall (9,120 − 7,680 ≈ 1,440
// veh/h) matches the real queue's measured growth (tail creep ≈ 4 km/h into
// the go-state density ≈ shortfall ≈ 1,500 veh/h), so the window runs in the
// real regime — a slowly growing queue with stop-and-go waves sweeping the
// section. These are boundary-condition choices, not physics tuning; see
// analysis/ngsim/README.md (M2/M3 sections) for the measured outcome.
const (
	i80SectionFt  = 1791.0 // surveyed length (ft)
	i80SectionM   = i80SectionFt * 0.3048
	i80MergeM     = 700.0 * 0.3048       // ramp merge position from section start (m)
	i80UpstreamM  = 600.0                // inflow runway upstream of the surveyed section
	i80DownstrM   = 800.0                // downstream section past the drop
	i80SpeedLimit = 65 * 1609.344 / 3600 // 65 mph ≈ 29.06 m/s
	i80Lanes      = 6
	i80DropLanes  = 4 // lanes remaining downstream of the drop
)

// I80Scenario returns the demand and vehicle mix derived from the NGSIM
// 17:00–17:15 raw data (see file header). Rates are per origin lane.
func I80Scenario() Scenario {
	return Scenario{
		SpawnRates: map[string]float64{
			"U0": 688,  // NGSIM lane 6 (rightmost)
			"U1": 972,  // lane 5
			"U2": 1056, // lane 4
			"U3": 988,  // lane 3
			"U4": 1300, // lane 2
			"U5": 1664, // lane 1 (HOV, leftmost)
			"AR": 932,  // on-ramp (NGSIM lane 7)
		},
		Types:       []*VehicleType{&Car, &Truck},
		TypeWeights: []float64{0.965, 0.035},
	}
}

// M3 reference-run constants. The real queue GREW through the window (tail
// creep ≈ 4 km/h — see the header), so the reference holds demand at
// i80RefDemand× data for the whole run (warm-up spin-up + window): the
// resulting discharge shortfall matches the real growth rate. i80RefDrop
// leaves 5 of 6 lanes past the downstream drop — 6→4 over-restricts (deep
// solid crawl), 6→6 stays free-flowing.
//
// 2026-07-23: 1.20 → 1.15 with the injection-safety rewrite (spawn.go
// injectionPlan). The old clearance rule (8+0.8·v buffer, 8 m/s entry
// floor) HELD entries in congestion that the braking-physics rule now
// admits at crawl speed — realized demand at the bottleneck rose at the
// same nominal rate, so the validated structure (2 wave stripes, scan
// −11.5 km/h, FD in range) moved down the demand scale.
const (
	i80RefDemand = 1.15
	i80RefDrop   = 5
)

// I80Spec returns the M3 reference run: the NGSIM I-80 scenario at sustained
// i80RefDemand× data demand (see the header for the boundary-condition
// derivation). ticks includes the warm-up spin-up.
func I80Spec(ticks, warmup uint64, seed uint64) RunSpec {
	scen := I80Scenario()
	for id, r := range scen.SpawnRates {
		scen.SpawnRates[id] = r * i80RefDemand
	}
	return RunSpec{
		Net:    NetSpec{Kind: "i80", DropLanes: i80RefDrop},
		Scen:   scen,
		Params: DefaultParams(),
		Seed:   seed,
		Ticks:  ticks,
	}
}

// buildI80Net constructs the network: upstream runway U (6 lanes) → surveyed
// section A (6 lanes + on-ramp AR, EndWall merge) → B (6 lanes; the leftmost
// past nDrop, B4/B5 at the reference 6→5, are dropped) → D (nDrop lanes,
// Exit) — the lane drop is the spillback boundary condition documented above.
func buildI80Net(spec NetSpec) (*Network, error) {
	limit := spec.SpeedLimit
	if limit == 0 {
		limit = i80SpeedLimit
	}
	down := spec.DownstreamLimit
	if down == 0 {
		down = limit
	}
	nDrop := spec.DropLanes
	if nDrop == 0 {
		nDrop = i80DropLanes
	}

	mk := func(id, section string, length, lim float64) *Lane {
		return &Lane{ID: id, Section: section, Length: length, SpeedLimit: lim}
	}
	ids := func(prefix string, n int) []string {
		s := make([]string, n)
		for i := range s {
			s[i] = prefix + string(rune('0'+i))
		}
		return s
	}

	var lanes []*Lane
	add := func(l *Lane) *Lane {
		l.Index = len(lanes)
		lanes = append(lanes, l)
		return l
	}
	// chain wires lanes[0].Left=lanes[1] … lanes[n-1] leftmost (lanes[0] is
	// the rightmost lane), matching the lanedrop convention.
	chain := func(ls ...*Lane) {
		for i := 0; i+1 < len(ls); i++ {
			ls[i].Left, ls[i+1].Right = ls[i+1], ls[i]
		}
	}

	uIDs, aIDs, bIDs, dIDs := ids("U", i80Lanes), ids("A", i80Lanes), ids("B", i80Lanes), ids("D", nDrop)
	U := make([]*Lane, i80Lanes)
	A := make([]*Lane, i80Lanes)
	B := make([]*Lane, i80Lanes)
	D := make([]*Lane, nDrop)
	for i := 0; i < i80Lanes; i++ {
		U[i] = add(mk(uIDs[i], "i80-up", i80UpstreamM, limit))
	}
	for i := 0; i < i80Lanes; i++ {
		A[i] = add(mk(aIDs[i], "i80-main", i80MergeM, limit))
	}
	ramp := add(mk("AR", "i80-main", i80MergeM, limit))
	ramp.EndWall = true // merge into A0 (rightmost) before the ramp ends
	for i := 0; i < i80Lanes; i++ {
		B[i] = add(mk(bIDs[i], "i80-main", i80SectionM-i80MergeM, limit))
	}
	for i := 0; i < nDrop; i++ {
		D[i] = add(mk(dIDs[i], "i80-down", i80DownstrM, down))
		D[i].Exit = true
	}
	for i := nDrop; i < i80Lanes; i++ {
		B[i].EndWall = true // dropped lanes: mandatory merge before B ends
	}

	chain(U...)
	chain(A...)
	A[0].Right, ramp.Left = ramp, A[0] // ramp runs right of the rightmost lane
	chain(B...)
	chain(D...)
	for i := 0; i < i80Lanes; i++ {
		U[i].Successors = []*Lane{A[i]}
		A[i].Successors = []*Lane{B[i]}
		if i < nDrop {
			B[i].Successors = []*Lane{D[i]}
		}
	}

	origins := make([]*Lane, 0, i80Lanes+1)
	origins = append(origins, U...)
	origins = append(origins, ramp)

	n := &Network{Lanes: lanes, Origins: origins, byID: make(map[string]*Lane, len(lanes))}
	for _, l := range lanes {
		n.byID[l.ID] = l
	}
	return n, nil
}

// I80MeasLanes maps the measurement-zone lane IDs (the surveyed section:
// segments A and B, mainline only — the ramp is excluded, mirroring the real
// field's lane 1–6 filter) to their x offset (m) in the NGSIM window
// coordinate, x = 0 at the upstream end of the surveyed section.
func I80MeasLanes() map[string]float64 {
	m := make(map[string]float64, 2*i80Lanes)
	for i := 0; i < i80Lanes; i++ {
		m["A"+string(rune('0'+i))] = 0
		m["B"+string(rune('0'+i))] = i80MergeM
	}
	return m
}
