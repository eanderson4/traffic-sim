package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// adaptiveDiamondSpec is routeDiamondSpec with the adaptive flag on.
func adaptiveDiamondSpec(t *testing.T) RunSpec {
	t.Helper()
	spec := routeDiamondSpec(t)
	spec.Params.AdaptiveRouting = true
	return spec
}

// dualExitSpec is the route-diamond geometry with a second exit y_0 off the
// rejoin lane, so two route destinations share the a_0 fork decision —
// two tables that go stale together.
func dualExitSpec(t *testing.T) RunSpec {
	t.Helper()
	nf := NetFile{
		Version: 1,
		Name:    "route-dual-exit",
		Lanes: []NetLane{
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"b_0", "c_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 100, SpeedLimit: 5, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {520, 100}}, Successors: []string{"m_0"}},
			{ID: "c_0", Section: "c", Length: 400, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {520, -100}}, Successors: []string{"m_0"}},
			{ID: "m_0", Section: "m", Length: 50, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{520, 100}, {600, 0}}, Successors: []string{"x_0", "y_0"}},
			{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{600, 0}, {602, 0}}, Exit: true},
			{ID: "y_0", Section: "y", Length: 2, SpeedLimit: 13.9, Width: 3.2,
				Shape: [][2]float64{{600, 0}, {620, 20}}, Exit: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return RunSpec{Net: NetSpec{Kind: "file", Path: p}, Params: DefaultParams(), Ticks: 5}
}

// The end-to-end ADR-0036 loop: congestion on the fast arm poisons its
// travel-time EMA through DWELL SAMPLES, an epoch-boundary recompute sees
// the poisoned weight, and a newly routed vehicle takes the other arm.
//
// The congestion is two crawlers parked at 1 m/s on c_0 (the arm static
// routing prefers): each takes ~400 s to clear the lane, so each departure
// folds a StrandAfterS-capped 300 s sample into c_0's ttEMA (α = 1/8). One
// sample lifts the EMA to ~48 s — deliberately NOT enough to beat
// hysteresis (the old path loses by only ~28 s < 30 s margin); the second
// lifts it to ~79 s, which clears the margin with room. The crawlers are
// placed on c_0 directly so the test does not have to wait out a_0.
func TestAdaptiveDivertsAroundCongestion(t *testing.T) {
	spec := adaptiveDiamondSpec(t)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	a0 := laneOf(t, e, "a_0")
	c0 := laneOf(t, e, "c_0")
	x0 := laneOf(t, e, "x_0")
	// Seed the epoch-0 table the way the run's first routed crossing would.
	e.routeTable(x0.Index)
	for _, s := range []float64{10, 0.5} {
		slow := e.AddInitialVehicle(c0, 0, s, 1, 1)
		slow.Cruise, slow.CruiseOK = 1, true
	}
	// The slow pair leaves c_0 around ticks 3900/4000; the routing weights
	// freeze at each 600-tick rollover, so the diversion becomes routable
	// at the NEXT boundary after the samples land (4200) — tables lag the
	// evidence by up to one epoch, the price of replay-safe weights.
	for e.Tick < 4300 {
		e.Step()
	}
	// The dwell evidence must have landed BEFORE the routing assertion below
	// means anything: two capped 300 s samples at α = 1/8 over the 12 s
	// free-flow time, damped a little by the per-tick relaxation.
	if c0.ttEMA < 50 {
		t.Fatalf("c_0 ttEMA = %.1f s after two capped dwell samples — the congestion signal never arrived", c0.ttEMA)
	}
	if e.ttSnap[c0.Index] < 40 {
		t.Fatalf("c_0 ttSnap = %.1f s — the epoch freeze never picked the evidence up", e.ttSnap[c0.Index])
	}
	w := e.AddInitialVehicle(a0, 0, 499.5, 33.3, 1)
	w.Route = "x_0"
	for i := 0; i < 5 && w.Lane != nil && w.Lane.ID == "a_0"; i++ {
		e.Step()
	}
	if w.Lane == nil || w.Lane.ID != "b_0" {
		t.Fatalf("crossed to %v, want b_0 — a poisoned fast arm should divert traffic to the slow one", w.Lane)
	}
}

// Hysteresis brackets: a poisoned arm whose new path is only MARGINALLY
// better than the old keeps the old next-hop; one that is CLEARLY better
// (beats it by more than max(30 s, 15%)) flips. ttEMA is set directly here
// — the dwell-sampling path that produces it in the wild is pinned end-to-
// end by TestAdaptiveDivertsAroundCongestion; this test isolates the margin.
//
// Bracket arithmetic (paths a_0 → {b_0|c_0} → m_0 → x_0; the m_0 + x_0
// tail, ~3.7 s, is common to both arms): free flow via b_0 is 20 s, via
// c_0 is 12 s. At ttEMA(c_0) = 45 the old path costs ~45.7 vs 23.7 new —
// 22 s better, under the 30 s floor: KEEP. At 60 it is ~59.7 vs 23.7 —
// 36 s better, over the floor: FLIP. (Both survive the ~10% relaxation
// decay over the 602 ticks to the crossing.)
func TestAdaptiveHysteresisMargin(t *testing.T) {
	cases := []struct {
		name string
		tt   float64
		want string
	}{
		{"marginal poison keeps the old hop", 45, "c_0"},
		{"clear poison flips the hop", 60, "b_0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := adaptiveDiamondSpec(t)
			e, err := NewEngine(spec)
			if err != nil {
				t.Fatal(err)
			}
			e.routeTable(laneOf(t, e, "x_0").Index) // epoch-0 table
			laneOf(t, e, "c_0").ttEMA = tc.tt
			for e.Tick < 601 { // into epoch 1: the next access recomputes
				e.Step()
			}
			w := e.AddInitialVehicle(laneOf(t, e, "a_0"), 0, 499.5, 33.3, 1)
			w.Route = "x_0"
			for i := 0; i < 5 && w.Lane != nil && w.Lane.ID == "a_0"; i++ {
				e.Step()
			}
			if w.Lane == nil || w.Lane.ID != tc.want {
				t.Fatalf("ttEMA %.0f: crossed to %v, want %s", tc.tt, w.Lane, tc.want)
			}
		})
	}
}

// The core safety property: flag OFF, nothing about the wire changes. The
// whole suite's CRC fixtures pin the trajectory; here the keyframe itself
// must stay at v5 or below — v6 is written only while AdaptiveRouting is on.
func TestAdaptiveOffStaysBelowV6(t *testing.T) {
	spec := routeDiamondSpec(t)
	spec.Params.AdaptiveRouting = false // explicit: the engine default is ON
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	v := e.AddInitialVehicle(laneOf(t, e, "a_0"), 0, 499.5, 33.3, 1)
	v.Route = "x_0"
	for e.Tick < 10 {
		e.Step()
	}
	data, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if ver := binary.LittleEndian.Uint16(data[4:6]); ver >= keyframeAdaptiveVersion {
		t.Fatalf("flag-off state marshalled as v%d — the adaptive section leaked into a static run", ver)
	}
}

// Flag-ON determinism (ADR-0005, mirror of TestRouteFollowingDeterministic):
// two identical congested adaptive runs produce identical per-tick CRCs —
// dwell samples, epoch freeze, recompute, and hysteresis are all pure
// functions of the keyframed state and the deterministic sweep order.
func TestAdaptiveDeterministic(t *testing.T) {
	run := func(t *testing.T) []uint64 {
		spec := adaptiveDiamondSpec(t)
		e, err := NewEngine(spec)
		if err != nil {
			t.Fatal(err)
		}
		e.routeTable(laneOf(t, e, "x_0").Index)
		slow := e.AddInitialVehicle(laneOf(t, e, "c_0"), 0, 5, 5, 1)
		slow.Cruise, slow.CruiseOK = 5, true
		// Probes crossing in epochs 0, 1, and 2, so the memoized table is
		// built, recomputed on dwell evidence, and recomputed again.
		for _, at := range []uint64{0, 650, 1200} {
			for e.Tick < at {
				e.Step()
			}
			p := e.AddInitialVehicle(laneOf(t, e, "a_0"), 0, 499.5, 33.3, 1)
			p.Route = "x_0"
		}
		for e.Tick < 1400 {
			e.Step()
		}
		return e.CRCs
	}
	a, b := run(t), run(t)
	if len(a) != len(b) {
		t.Fatalf("CRC sequences differ in length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("tick %d: CRC %x != %x — adaptive routing is not deterministic", i, a[i], b[i])
		}
	}
}

// The served table is a functional graph terminating at the destination
// — the constructive acyclicity guarantee (review round 5: validating the
// walk with the candidate's stale static hop still installed lets a chain
// returning to i escape through it, and the install then closes a real
// cycle). Adversarial weights force splices across two interleaved
// corridors; EVERY lane's served chain must reach the destination.
func TestRerouteTableNeverServesACycle(t *testing.T) {
	nf := NetFile{Version: 1, Name: "cycle-hunt", Lanes: []NetLane{
		{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 33.3, Width: 3.2,
			Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"b_0", "c_0"}, Origin: true},
		{ID: "b_0", Section: "b", Length: 100, SpeedLimit: 13.9, Width: 3.2,
			Shape: [][2]float64{{500, 0}, {520, 100}}, Successors: []string{"d_0", "e_0"}},
		{ID: "c_0", Section: "c", Length: 100, SpeedLimit: 13.9, Width: 3.2,
			Shape: [][2]float64{{500, 0}, {520, -100}}, Successors: []string{"e_0", "f_0"}},
		{ID: "d_0", Section: "d", Length: 100, SpeedLimit: 13.9, Width: 3.2,
			Shape: [][2]float64{{520, 100}, {540, 100}}, Successors: []string{"g_0"}},
		{ID: "e_0", Section: "e", Length: 100, SpeedLimit: 13.9, Width: 3.2,
			Shape: [][2]float64{{520, 0}, {540, 0}}, Successors: []string{"g_0", "h_0"}},
		{ID: "f_0", Section: "f", Length: 100, SpeedLimit: 13.9, Width: 3.2,
			Shape: [][2]float64{{520, -100}, {540, -100}}, Successors: []string{"h_0"}},
		{ID: "g_0", Section: "g", Length: 20, SpeedLimit: 27.8, Width: 3.2,
			Shape: [][2]float64{{540, 50}, {560, 50}}, Successors: []string{"x_0"}},
		{ID: "h_0", Section: "h", Length: 400, SpeedLimit: 8.3, Width: 3.2,
			Shape: [][2]float64{{540, -50}, {560, -50}}, Successors: []string{"x_0"}},
		{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 13.9, Width: 3.2,
			Shape: [][2]float64{{560, 0}, {562, 0}}, Exit: true},
	}}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{Net: NetSpec{Kind: "file", Path: p}, Params: DefaultParams(), Ticks: 5}
	spec.Params.AdaptiveRouting = true
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Adversarial frozen weights: static routing loves the short g_0 link
	// (~0.7 s vs h_0's ~48 s); poison g_0 and e_0 so the fresh Dijkstra
	// splices around them at several lanes at once.
	for _, l := range e.Net.Lanes {
		e.ttSnap[l.Index] = freeFlowTime(l)
	}
	e.ttSnap[e.Net.LaneByID("g_0").Index] = 1000
	e.ttSnap[e.Net.LaneByID("e_0").Index] = 500
	tab := e.rerouteTable(e.Net.LaneByID("x_0").Index)
	for i, l := range e.Net.Lanes {
		if l.ID == "x_0" {
			continue
		}
		j, reached := int(tab[i]), false
		for hops := 0; hops <= len(tab); hops++ {
			if j == e.Net.LaneByID("x_0").Index {
				reached = true
				break
			}
			if j < 0 {
				break
			}
			j = int(tab[j])
		}
		if !reached {
			t.Fatalf("lane %s: served chain does not reach x_0 — the table contains a cycle or a strand", l.ID)
		}
	}
}

// First-use builds are hysteretic too (external review, 2026-07-30): a
// destination first ASKED for after the weights carry marginal congestion
// must get the same table a stale recompute would serve — the free-flow
// hop when the margin is not cleared — not the raw Dijkstra hop. This is
// what makes a post-restore cold cache reproduce the uninterrupted run.
// Marginal here means ttEMA(c_0) = 45: raw Dijkstra prefers b_0
// (~23.7 s vs ~45.7 s) but the 30 s hysteresis floor keeps c_0.
func TestAdaptiveFirstUseBuildUsesHysteresis(t *testing.T) {
	spec := adaptiveDiamondSpec(t)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	c0 := laneOf(t, e, "c_0")
	c0.ttEMA = 45 // lands in ttSnap at the epoch-1 rollover
	for e.Tick < 601 {
		e.Step()
	}
	// No table for x_0 has ever been built in this engine: the first ask
	// is a cold build over the poisoned weights.
	hop := e.routeNextHop(laneOf(t, e, "a_0"), "x_0")
	if hop == nil || hop.ID != "c_0" {
		t.Fatalf("cold build hopped to %v, want c_0 — first-use builds must honor hysteresis", hop)
	}
}

// The replay-fidelity property the epoch-freeze and static-reference
// hysteresis exist for (ADR-0036 §2, external review 2026-07-30): a run
// restored from a mid-run keyframe continues bit-exactly. Tables are
// derived, so the restored engine REBUILDS them — reproducibly, because
// they are pure functions of (frozen weights, network): the weights come
// from the keyframed ttSnap, the hysteresis reference is the static table,
// and every build is unmetered so there is no meter to diverge on. With
// hysteresis history (the pre-review design) the restored engine picked
// hops the live engine had rejected, and this test failed.
func TestAdaptiveRestoreIsCRCExact(t *testing.T) {
	spec := adaptiveDiamondSpec(t)
	const split = 800 // mid-epoch-1, after dwell evidence has landed
	full, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	slow := full.AddInitialVehicle(laneOf(t, full, "c_0"), 0, 5, 5, 1)
	slow.Cruise, slow.CruiseOK = 5, true
	for _, at := range []uint64{0, 650, 1200} {
		for full.Tick < at {
			full.Step()
		}
		p := full.AddInitialVehicle(laneOf(t, full, "a_0"), 0, 499.5, 33.3, 1)
		p.Route = "x_0"
	}
	for full.Tick < 1400 {
		full.Step()
	}

	// Re-run to the split, restore, continue: the tail must be bit-exact.
	half, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	slow = half.AddInitialVehicle(laneOf(t, half, "c_0"), 0, 5, 5, 1)
	slow.Cruise, slow.CruiseOK = 5, true
	for _, at := range []uint64{0, 650} {
		for half.Tick < at {
			half.Step()
		}
		p := half.AddInitialVehicle(laneOf(t, half, "a_0"), 0, 499.5, 33.3, 1)
		p.Route = "x_0"
	}
	for half.Tick < split {
		half.Step()
	}
	data, err := half.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	warm, err := RestoreState(spec, data)
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	for warm.Tick < 1200 {
		warm.Step()
	}
	p := warm.AddInitialVehicle(laneOf(t, warm, "a_0"), 0, 499.5, 33.3, 1)
	p.Route = "x_0"
	for warm.Tick < 1400 {
		warm.Step()
	}
	// CRCs are per-Step append-only and NOT restored: warm.CRCs[0] is the
	// first post-restore tick (split+1), full.CRCs[i] is tick i+1.
	for i := split; i < 1400; i++ {
		if full.CRCs[i] != warm.CRCs[i-split] {
			t.Fatalf("tick %d: CRC %x != %x — a mid-run restore diverges under adaptive routing",
				i+1, full.CRCs[i], warm.CRCs[i-split])
		}
	}
}

// Keyframe v6 round-trip: a state carrying a perturbed ttEMA and a running
// dwell clock marshals, restores, and re-marshals byte-identically, and the
// restored engine makes the same next-hop decision the live one does. The
// nLanes guard must reject the same bytes against the wrong spec.
func TestKeyframeV6AdaptiveRoundTrip(t *testing.T) {
	spec := adaptiveDiamondSpec(t)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 30 { // spawn mid-run so laneEntryTick is a nonzero running clock
		e.Step()
	}
	// Perturb BEFORE the first routed crossing: the live engine's memoized
	// table and the restored engine's fresh build then see the same frozen
	// weights. The perturbation is white-box on BOTH the EMA and the
	// epoch-frozen snapshot — in the wild ttSnap only moves at a rollover
	// (TestAdaptiveDivertsAroundCongestion pins that path end-to-end);
	// here the round-trip of both fields is what is under test.
	laneOf(t, e, "c_0").ttEMA = 77.5 // perturbed congestion signal
	e.ttSnap[laneOf(t, e, "c_0").Index] = 77.5
	w := e.AddInitialVehicle(laneOf(t, e, "a_0"), 0, 250, 20, 1)
	w.Route = "x_0"
	for e.Tick < 50 {
		e.Step()
	}

	data, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if ver := binary.LittleEndian.Uint16(data[4:6]); ver != keyframeAdaptiveVersion {
		t.Fatalf("flag-on state marshalled as v%d, want v%d", ver, keyframeAdaptiveVersion)
	}
	warm, err := RestoreState(spec, data)
	if err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	data2, err := warm.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, data2) {
		t.Fatal("v6 round-trip is not byte-identical")
	}
	// The live engine's table (built while w sat routed on a_0, over the
	// perturbed weights) and the restored engine's fresh build must agree,
	// and agree on the diversion the poisoned c_0 implies.
	coldHop := e.routeNextHop(laneOf(t, e, "a_0"), "x_0")
	warmHop := warm.routeNextHop(warm.Net.LaneByID("a_0"), "x_0")
	if coldHop == nil || warmHop == nil || coldHop.ID != warmHop.ID {
		t.Fatalf("next-hop decisions diverge across a restore: %v vs %v", coldHop, warmHop)
	}
	if coldHop.ID != "b_0" {
		t.Fatalf("hop = %s, want b_0 — the restored ttEMA did not weight the recompute", coldHop.ID)
	}
	// The lane section is pinned to the spec's network: the diamond's bytes
	// against the fork's 8-lane network must be rejected, not misread.
	if _, err := RestoreState(routeForkSpec(t), data); err == nil {
		t.Fatal("v6 lane section restored against the wrong network")
	}
}

// Every stale table recomputes on first access in the new epoch,
// unmetered — the budget that served stale tables under pressure was
// rejected in review (stale tables from mixed epochs are exactly what a
// mid-run restore cannot reproduce, ADR-0036 §2). Two destinations share
// the a_0 fork decision here; both askers in the same tick get the fresh,
// diverted table.
func TestRerouteRecomputesEveryStaleTable(t *testing.T) {
	spec := dualExitSpec(t)
	spec.Params.AdaptiveRouting = true
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	a0 := laneOf(t, e, "a_0")
	c0 := laneOf(t, e, "c_0")
	x0 := laneOf(t, e, "x_0")
	y0 := laneOf(t, e, "y_0")
	e.routeTable(x0.Index) // both tables stamped epoch 0
	e.routeTable(y0.Index)
	c0.ttEMA = 100 // poisoned well past the hysteresis margin
	for e.Tick < 601 {
		e.Step()
	}
	// Both vehicles cross a_0 in the SAME tick (same position, same speed);
	// both tables recompute on demand.
	va := e.AddInitialVehicle(a0, 0, 499.5, 33.3, 1)
	va.Route = "x_0"
	vb := e.AddInitialVehicle(a0, 0, 499.5, 33.3, 1)
	vb.Route = "y_0"
	e.Step()
	if va.Lane == nil || va.Lane.ID != "b_0" {
		t.Fatalf("first asker: crossed to %v, want b_0", va.Lane)
	}
	if vb.Lane == nil || vb.Lane.ID != "b_0" {
		t.Fatalf("second asker: crossed to %v, want b_0 — no stale table may be served", vb.Lane)
	}
	if e.routeEpochs[x0.Index] != 1 || e.routeEpochs[y0.Index] != 1 {
		t.Fatalf("epochs = (%d, %d), want (1, 1) — every stale table recomputes on access",
			e.routeEpochs[x0.Index], e.routeEpochs[y0.Index])
	}
}
