package engine

// fixture_freeway_merge_test.go — single-junction behavior fixture:
// a freeway on-ramp merge (SFO airport on-ramp, OSM way 8921107, 1 lane,
// into US-101 NB, OSM way 256787724, 6 lanes, at OSM node 6862263616;
// see testdata/freeway-merge/README.md). The fixture is the pinned input;
// the network is loaded exactly like serve does (NetSpec{Kind: "file"}).
//
// Asserted behaviors, with the theory behind each bound:
//
//  1. FLOW CONSERVATION — vehicle accounting is closed: over the horizon,
//     injected == despawned + still-live, exactly, at every checkpoint
//     (continuity equation over a closed counting region; any vehicle
//     vanishing or duplicating inside the kernel breaks it). Cross-checked
//     against the ADR-0014 metric kernel's trip records (completed +
//     horizon-partial == injected).
//  2. ZERO COLLISIONS — the IDM/MOBIL stack is collision-free by design
//     (Kesting/Treiber: the safe-following fixed point never inverts
//     order); Stats.Collisions counts adjacent-pair overlaps below
//     -0.01 m and must stay 0 at low AND near-capacity demand.
//  3. MERGE CAPACITY — under overload the downstream flow plateaus at a
//     finite capacity: > 0 (the merge serves traffic) and < 2x the
//     per-lane theoretical max (~2400 veh/h/ln, HCM freeway capacity;
//     2x headroom = "no phantom throughput" tripwire) times the 7
//     downstream lanes.

import (
	"testing"
)

const fixtureMergeNetPath = "testdata/freeway-merge/network.json"

// Pinned lane IDs (fixture README §Key lanes). The merge junction is
// 6862263616 (priority): ramp internal i6862263616_0_0 feeds downstream
// lane 0, mainline internals i6862263616_1_k feed downstream lane k+1.
var (
	mergeMainlineOrigins = []string{
		"n256787724_0", "n256787724_1", "n256787724_2",
		"n256787724_3", "n256787724_4", "n256787724_5",
	}
	mergeRampOrigin = "n8921107_0"
	mergeDownstream = []string{
		"n392505474_0", "n392505474_1", "n392505474_2", "n392505474_3",
		"n392505474_4", "n392505474_5", "n392505474_6",
	}
	mergeInternals = []string{
		"i6862263616_0_0", "i6862263616_1_0", "i6862263616_1_1",
		"i6862263616_1_2", "i6862263616_1_3", "i6862263616_1_4", "i6862263616_1_5",
	}
)

// fixtureMergeSpec builds the run: demand only on the 7 freeway origin
// lanes (per-lane Poisson rates via Scenario.SpawnRates — all other
// origins stay disabled), 90/10 car/truck, fixed seed.
func fixtureMergeSpec(t *testing.T, mainlineRate, rampRate float64, ticks uint64) RunSpec {
	t.Helper()
	rates := map[string]float64{mergeRampOrigin: rampRate}
	for _, id := range mergeMainlineOrigins {
		rates[id] = mainlineRate
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: fixtureMergeNetPath},
		Scen:   Scenario{SpawnRates: rates, Types: []*VehicleType{&Car, &Truck}, TypeWeights: []float64{0.9, 0.1}},
		Params: DefaultParams(),
		Seed:   42,
		Ticks:  ticks,
	}
	// Fixture-drift guard: the merge under test must still be there with
	// the expected shape (6+1 origins, 7 downstream lanes, 7 internals).
	net, err := BuildNet(spec.Net)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	for _, id := range append(append(append([]string{}, mergeMainlineOrigins...), mergeRampOrigin), mergeDownstream...) {
		if net.LaneByID(id) == nil {
			t.Fatalf("fixture drift: lane %s missing", id)
		}
	}
	for _, id := range mergeInternals {
		if net.LaneByID(id) == nil {
			t.Fatalf("fixture drift: merge internal %s missing", id)
		}
	}
	return spec
}

// runFixtureMerge drives the kernel headless with the ADR-0014 metric
// kernel attached (downstream q set + trip records), asserting the closed
// vehicle balance and zero collisions at every 300-tick checkpoint.
func runFixtureMerge(t *testing.T, spec RunSpec) (*Engine, *Kernel) {
	t.Helper()
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	k, err := NewKernel(e, KernelConfig{
		Sets: []MetricSetConfig{{
			ID:          "downstream",
			LaneIDs:     mergeDownstream,
			Groups:      MetricGroups{Edie: true},
			PeriodTicks: 600, // 60 s Edie intervals at dt = 0.1
		}},
		Trips: true,
	})
	if err != nil {
		t.Fatalf("NewKernel: %v", err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
		if e.Tick%300 == 0 {
			if got, want := e.Stats.Spawned, e.Stats.Despawned+len(e.Vehicles()); got != want {
				t.Fatalf("tick %d: flow conservation broken: spawned %d != despawned %d + live %d",
					e.Tick, got, e.Stats.Despawned, len(e.Vehicles()))
			}
		}
	}
	k.Finalize(e)
	assertNoNaN(t, e)
	return e, k
}

// assertMergeClean is the shared (a)+(b) check for one demand level.
func assertMergeClean(t *testing.T, label string, mainlineRate, rampRate float64) {
	t.Helper()
	spec := fixtureMergeSpec(t, mainlineRate, rampRate, 3600)
	e, k := runFixtureMerge(t, spec)

	// Non-vacuous: demand actually flowed through the merge.
	if e.Stats.Spawned == 0 || e.Stats.Despawned == 0 {
		t.Fatalf("%s: vacuous run: spawned %d despawned %d", label, e.Stats.Spawned, e.Stats.Despawned)
	}
	// (a) closed balance at the horizon (checkpoint assertions already
	// covered the run).
	if got, want := e.Stats.Spawned, e.Stats.Despawned+len(e.Vehicles()); got != want {
		t.Errorf("%s: spawned %d != despawned %d + live %d", label, got, e.Stats.Despawned, len(e.Vehicles()))
	}
	// (a) cross-check via trip records: completed + horizon-partial ==
	// injected, and every trip entered on one of the 7 feed lanes.
	trips := k.DrainTrips()
	if len(trips) != e.Stats.Spawned {
		t.Errorf("%s: trip records %d != spawned %d (vehicle lost or duplicated)", label, len(trips), e.Stats.Spawned)
	}
	valid := map[string]bool{mergeRampOrigin: true}
	for _, id := range mergeMainlineOrigins {
		valid[id] = true
	}
	for _, tr := range trips {
		if !valid[tr.OriginLaneID] {
			t.Errorf("%s: trip %d entered on unexpected lane %s", label, tr.VehicleID, tr.OriginLaneID)
		}
	}
	// (b) zero collisions.
	if e.Stats.Collisions != 0 {
		t.Errorf("%s: %d collisions by section %v", label, e.Stats.Collisions, e.Stats.CollisionsBySection)
	}
	tot := k.Totals()
	t.Logf("%s: spawned %d, despawned %d, live %d, collisions %d, min gap %.2f m, denied-wait %.0f veh·s, VMT %.0f m",
		label, e.Stats.Spawned, e.Stats.Despawned, len(e.Vehicles()),
		e.Stats.Collisions, e.Stats.MinGap, tot.DeniedWaitS, tot.VMT)
}

// (a)+(b) at low demand: 600 veh/h/ln mainline + 600 veh/h ramp —
// free-flow merge, well under any capacity estimate.
func TestFixtureFreewayMergeLowDemand(t *testing.T) {
	assertMergeClean(t, "low (600+600 veh/h)", 600, 600)
}

// (a)+(b) at near-capacity demand: 1800 veh/h/ln mainline + 1500 veh/h
// ramp — around the HCM per-lane capacity band, the merge should be
// busy but the car-following stack still collision-free.
func TestFixtureFreewayMergeNearCapacity(t *testing.T) {
	assertMergeClean(t, "near-capacity (1800/ln+1500 veh/h)", 1800, 1500)
}

// (c) MERGE CAPACITY: overload both feeders (2400 veh/h/ln mainline —
// per-lane theoretical max — plus 2000 veh/h ramp). Downstream Edie flow
// must plateau: > 0 and < 2x2400 veh/h/ln x 7 lanes = 33600 veh/h. The
// plateau value itself is reported (t.Log) as the measured merge
// capacity of this junction in this model.
func TestFixtureFreewayMergeCapacityPlateau(t *testing.T) {
	spec := fixtureMergeSpec(t, 2400, 2000, 3600)
	e, k := runFixtureMerge(t, spec)
	if e.Stats.Collisions != 0 {
		t.Errorf("overload: %d collisions by section %v", e.Stats.Collisions, e.Stats.CollisionsBySection)
	}
	// Plateau: total downstream q per full 60 s interval past the 120 s
	// warmup (first exits reach the end of the 721 m downstream edge
	// ~60 s in); the plateau is the max interval sum.
	byInterval := map[uint64]float64{}
	for _, r := range k.DrainIntervals() {
		if r.Partial || r.Q == nil || r.BeginTick < 1200 {
			continue
		}
		byInterval[r.BeginTick] += *r.Q
	}
	if len(byInterval) == 0 {
		t.Fatal("no full downstream intervals past warmup — run too short or demand vacuous")
	}
	plateau := 0.0 // veh/h
	var at uint64
	for begin, q := range byInterval {
		if vph := q * 3600; vph > plateau {
			plateau, at = vph, begin
		}
	}
	const perLaneMax = 2400.0 // veh/h/ln, HCM freeway per-lane theoretical max
	bound := 2 * perLaneMax * float64(len(mergeDownstream))
	t.Logf("overload: downstream plateau %.0f veh/h over %d lanes (interval starting tick %d); bound (0, %.0f)",
		plateau, len(mergeDownstream), at, bound)
	for begin, q := range byInterval {
		t.Logf("  interval @%d: %.0f veh/h", begin, q*3600)
	}
	if plateau <= 0 {
		t.Errorf("merge served no flow under overload: plateau %.0f veh/h", plateau)
	}
	if plateau >= bound {
		t.Errorf("phantom throughput: plateau %.0f veh/h >= 2x theoretical max %.0f veh/h", plateau, bound)
	}
}
