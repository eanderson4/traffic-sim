package engine

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// defaultCfg is the zero-authoring plan at the tests' dt (0.1 s).
func defaultCfg(n *Network) KernelConfig { return DefaultKernelConfig(n, 0.1) }

// runWithKernel drives a fresh run with a metric kernel attached: Observe
// after every Step, Finalize at the horizon.
func runWithKernel(t *testing.T, spec RunSpec, cfg func(*Network) KernelConfig) (*Engine, *Kernel) {
	t.Helper()
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	k, err := NewKernel(e, cfg(e.Net))
	if err != nil {
		t.Fatalf("NewKernel: %v", err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	return e, k
}

// closeWithin reports whether a and b agree to 1e-9 relative (1e-9 absolute
// near zero) — float summation order differs between the per-tick totals and
// the per-(lane, interval) records over the same segments.
func closeWithin(a, b float64) bool {
	return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b))
}

// The kernel is a read-only observer (ADR-0014 §1): an observed run must
// produce the identical per-tick CRC sequence as an unobserved one.
func TestKernelCRCInvariance(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 800, 42)
	if err != nil {
		t.Fatal(err)
	}
	_, log, err := Run(spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	e, _ := runWithKernel(t, spec, defaultCfg)
	assertEqualCRCs(t, log.CRCs, e.CRCs)
}

// Same seed, two kernels: identical drained records and totals — the
// ADR-0005 discipline applied to derived numbers (ADR-0014 §1).
func TestKernelDeterminism(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 800, 42)
	if err != nil {
		t.Fatal(err)
	}
	run := func() ([]IntervalRecord, []TripRecord, Totals, *Kernel) {
		_, k := runWithKernel(t, spec, defaultCfg)
		return k.DrainIntervals(), k.DrainTrips(), k.Totals(), k
	}
	i1, tr1, to1, k1 := run()
	i2, tr2, to2, _ := run()
	if !reflect.DeepEqual(i1, i2) {
		t.Fatal("interval records differ between identical runs")
	}
	if !reflect.DeepEqual(tr1, tr2) {
		t.Fatal("trip records differ between identical runs")
	}
	if !reflect.DeepEqual(to1, to2) {
		t.Fatal("totals differ between identical runs")
	}
	if len(i1) == 0 || len(tr1) == 0 {
		t.Fatal("expected non-empty record drains")
	}
	// Drains are destructive; Totals is not.
	if n := len(k1.DrainIntervals()); n != 0 {
		t.Fatalf("second DrainIntervals returned %d records, want 0", n)
	}
	if n := len(k1.DrainTrips()); n != 0 {
		t.Fatalf("second DrainTrips returned %d records, want 0", n)
	}
	if got := k1.Totals(); !reflect.DeepEqual(got, to1) {
		t.Fatal("Totals changed after drains")
	}
}

// lightDemandSpec is the straight 5 km exit lane at 180 veh/h with
// homogeneous drivers (σ = 0 ⇒ F = 1): exact free flow at v0.
func lightDemandSpec(t *testing.T, ticks uint64) RunSpec {
	t.Helper()
	spec, err := DefaultSpec("straight", ticks, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 180
	spec.Params.SpeedFactorSigma = 0
	return spec
}

// Free-flow sanity: time-loss ≈ 0, Edie V ≈ desired speed, the q = k·v
// identity holds within the cell, occupancy is a fraction (ADR-0014 §2/§3).
func TestKernelFreeFlow(t *testing.T) {
	spec := lightDemandSpec(t, 3000)
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "main",
				LaneIDs:     []string{"S0"},
				Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
				PeriodTicks: 1000,
			}},
			Trips: true,
		}
	}
	_, k := runWithKernel(t, spec, cfg)
	tot := k.Totals()
	if tot.VHT == 0 {
		t.Fatal("no traffic observed")
	}
	if tot.TotalTimeLossS > 0.02*tot.VHT {
		t.Errorf("free-flow time-loss %v s exceeds 2%% of VHT %v s", tot.TotalTimeLossS, tot.VHT)
	}
	const v0 = 33.3 // Car.V0 == lane limit, F = 1
	checked := 0
	for _, r := range k.DrainIntervals() {
		if r.Partial || r.SumTimeS == 0 {
			continue
		}
		checked++
		if r.V == nil {
			t.Fatalf("interval [%d,%d): V nil with SumTimeS = %v", r.BeginTick, r.EndTick, r.SumTimeS)
		}
		if math.Abs(*r.V-v0)/v0 > 0.02 {
			t.Errorf("interval [%d,%d): V = %v m/s, want ≈ %v", r.BeginTick, r.EndTick, *r.V, v0)
		}
		if !closeWithin(*r.Q, *r.K**r.V) {
			t.Errorf("interval [%d,%d): q = %v but k·v = %v", r.BeginTick, r.EndTick, *r.Q, *r.K**r.V)
		}
		if *r.Occupancy <= 0 || *r.Occupancy >= 1 {
			t.Errorf("interval [%d,%d): occupancy %v outside (0,1)", r.BeginTick, r.EndTick, *r.Occupancy)
		}
	}
	if checked == 0 {
		t.Fatal("no full intervals with traffic")
	}
}

// Horizon mid-trip: every in-network vehicle gets a partial trip record
// (Completed = false, exit tick = horizon), and run totals include the
// unfinished trips — the survivorship-bias guard of ADR-0014 §2/§3.
func TestKernelHorizonPartials(t *testing.T) {
	spec := lightDemandSpec(t, 1200) // crossing the 5 km lane takes ≈ 1500 ticks
	spec.Scen.SpawnRatePerLaneHour = 360
	_, k := runWithKernel(t, spec, defaultCfg)
	tot := k.Totals()
	trips := k.DrainTrips()
	if len(trips) == 0 {
		t.Fatal("no trip records at horizon")
	}
	var partialDist float64
	for _, r := range trips {
		if r.Completed {
			t.Errorf("trip %d completed before the 1200-tick horizon", r.VehicleID)
		}
		if r.ExitTick != spec.Ticks {
			t.Errorf("trip %d: exit tick %d, want horizon %d", r.VehicleID, r.ExitTick, spec.Ticks)
		}
		partialDist += r.DistanceM
	}
	if tot.CompletedTrips != 0 {
		t.Errorf("CompletedTrips = %d, want 0 (no vehicle can cross in time)", tot.CompletedTrips)
	}
	if tot.ActiveAtHorizon != len(trips) {
		t.Errorf("ActiveAtHorizon = %d, want %d partial trips", tot.ActiveAtHorizon, len(trips))
	}
	// The completed-trip distance sum is 0, yet VMT carries everything the
	// active vehicles drove — totals never come from completed trips alone.
	if tot.VMT <= 0 {
		t.Error("VMT = 0 with active vehicles at the horizon")
	}
	if !closeWithin(tot.VMT, partialDist) {
		t.Errorf("VMT %v vs Σ partial trip distance %v", tot.VMT, partialDist)
	}
}

// Oversaturated lanedrop: queues produce stop episodes, stopped time, and
// time-loss; demand the density cap keeps out shows up as denied entry
// (ADR-0014 §3 latent demand).
func TestKernelCongestion(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 3000, 7)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3000
	spec.Scen.DensityTargetPerKm = 80
	_, k := runWithKernel(t, spec, defaultCfg)
	tot := k.Totals()
	var stops int
	var stoppedS float64
	for _, r := range k.DrainTrips() {
		stops += r.Stops
		stoppedS += r.StoppedTimeS
	}
	if stops == 0 {
		t.Error("no stop episodes under overload")
	}
	if stoppedS == 0 {
		t.Error("no stopped time under overload")
	}
	if tot.TotalTimeLossS == 0 {
		t.Error("no time-loss under overload")
	}
	if tot.DeniedWaitS == 0 {
		t.Error("no denied-entry wait under overload")
	}
	if tot.DeniedPending == 0 {
		t.Error("no denied demand pending at the horizon")
	}
	// Multi-origin jitter inverts injection order routinely — the gap
	// detector must reconcile, not overcount. Everything here books cleanly.
	if tot.DroppedCrossings != 0 {
		t.Errorf("DroppedCrossings = %d on a clean congestion run, want 0 (gap reconciliation)", tot.DroppedCrossings)
	}
}

// A lane with zero traffic still emits its interval records, with zero sums
// and V omitted — never zero-filled (ADR-0014 §2).
func TestKernelEmptyInterval(t *testing.T) {
	spec, err := DefaultSpec("straight", 250, 1) // no demand: spawner nil
	if err != nil {
		t.Fatal(err)
	}
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "main",
				LaneIDs:     []string{"S0"},
				Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
				PeriodTicks: 100,
			}},
			Trips: true,
		}
	}
	_, k := runWithKernel(t, spec, cfg)
	recs := k.DrainIntervals()
	if len(recs) != 3 { // [0,100), [100,200), partial [200,250)
		t.Fatalf("got %d interval records, want 3", len(recs))
	}
	for _, r := range recs[:2] {
		if r.Partial {
			t.Errorf("interval [%d,%d) flagged partial at a period boundary", r.BeginTick, r.EndTick)
		}
		if r.SumDistM != 0 || r.SumTimeS != 0 || *r.Stops != 0 || *r.TimeLossS != 0 {
			t.Errorf("interval [%d,%d): nonzero sums on an empty lane: %+v", r.BeginTick, r.EndTick, r)
		}
		if r.Stops == nil || r.TimeLossS == nil {
			t.Errorf("interval [%d,%d): group-on Stops/TimeLossS must be non-nil zeros", r.BeginTick, r.EndTick)
		}
		if r.V != nil {
			t.Errorf("interval [%d,%d): V = %v, want nil when SumTimeS == 0", r.BeginTick, r.EndTick, *r.V)
		}
		if *r.Q != 0 || *r.K != 0 || *r.Occupancy != 0 {
			t.Errorf("interval [%d,%d): q/k/occupancy not exactly 0 on an empty lane", r.BeginTick, r.EndTick)
		}
	}
}

// Lane-crossing conservation (ADR-0014 §3): on a net with lateral hops,
// successor crossings, and despawns, every segment lands in exactly one lane
// accumulator — the per-(lane, interval) sums and the per-vehicle trip
// distances both add up to the network totals.
func TestKernelLaneCrossingConservation(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 2000, 3)
	if err != nil {
		t.Fatal(err)
	}
	e, k := runWithKernel(t, spec, defaultCfg)
	if e.Stats.LaneChanges == 0 {
		t.Fatal("no lane changes in the run — the crossing path went untested")
	}
	tot := k.Totals()
	if tot.CompletedTrips == 0 {
		t.Fatal("no completed trips in the run — the despawn path went untested")
	}
	var sumDist, sumTime float64
	for _, r := range k.DrainIntervals() {
		sumDist += r.SumDistM
		sumTime += r.SumTimeS
	}
	if !closeWithin(sumDist, tot.VMT) {
		t.Errorf("Σ interval distance %v vs VMT %v", sumDist, tot.VMT)
	}
	if !closeWithin(sumTime, tot.VHT) {
		t.Errorf("Σ interval time %v vs VHT %v", sumTime, tot.VHT)
	}
	var tripDist float64
	for _, r := range k.DrainTrips() {
		tripDist += r.DistanceM
	}
	if !closeWithin(tripDist, tot.VMT) {
		t.Errorf("Σ trip distance %v vs VMT %v", tripDist, tot.VMT)
	}
}

// A horizon that is not a period boundary truncates the final interval,
// which is emitted flagged Partial with its actual bounds (ADR-0014 §3).
func TestKernelPartialFinalInterval(t *testing.T) {
	spec := lightDemandSpec(t, 1500)
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "main",
				LaneIDs:     []string{"S0"},
				Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
				PeriodTicks: 1000,
			}},
			Trips: true,
		}
	}
	_, k := runWithKernel(t, spec, cfg)
	var full, partial *IntervalRecord
	recs := k.DrainIntervals()
	for i := range recs {
		switch r := &recs[i]; {
		case r.Partial:
			partial = r
		default:
			full = r
		}
	}
	if full == nil || partial == nil {
		t.Fatalf("want one full and one partial interval, got %+v", recs)
	}
	// Interval k holds observation ticks (kP, (k+1)P] and stamps the
	// sim-time grid: the first full interval covers ticks 1..1000, stamped
	// [0, 1000), duration 100 s.
	if full.BeginTick != 0 || full.EndTick != 1000 {
		t.Errorf("full interval = [%d,%d), want [0,1000)", full.BeginTick, full.EndTick)
	}
	// The partial covers ticks 1001..1500, stamped [1000, 1500): 50 s.
	if partial.BeginTick != 1000 || partial.EndTick != 1500 {
		t.Errorf("partial interval = [%d,%d), want [1000,1500)", partial.BeginTick, partial.EndTick)
	}
	if partial.SumTimeS == 0 {
		t.Error("partial interval lost the traffic observed in it")
	}
}

// Ring wrap + pre-placed vehicles + exact period boundary: the 22-vehicle
// Sugiyama ring wraps S past the lane length every lap (self-successor);
// vehicles are placed before the kernel attaches, so their initial positions
// must not be booked as distance; and a horizon of exactly k·P ticks yields
// exactly k full intervals with no trailing partial.
func TestKernelRingWrap(t *testing.T) {
	spec, err := DefaultSpec("ring", 1200, 42) // 22 pre-placed vehicles, 230 m loop
	if err != nil {
		t.Fatal(err)
	}
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "ring",
				LaneIDs:     []string{"R0"},
				Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
				PeriodTicks: 200,
			}},
			Trips: true,
		}
	}
	_, k := runWithKernel(t, spec, cfg)
	recs := k.DrainIntervals()
	if len(recs) != 6 { // 1200 ticks = 6 × 200, exactly
		t.Fatalf("got %d interval records, want 6 full ones", len(recs))
	}
	const wantTime = 22 * 200 * 0.1 // 22 always-present vehicles × 200 ticks × dt
	for _, r := range recs {
		if r.Partial {
			t.Errorf("interval [%d,%d): partial at an exact period boundary", r.BeginTick, r.EndTick)
		}
		if r.SumDistM < 0 || r.SumTimeS < 0 || *r.TimeLossS < 0 {
			t.Errorf("interval [%d,%d): negative sum: %+v", r.BeginTick, r.EndTick, r)
		}
		if !closeWithin(r.SumTimeS, wantTime) {
			t.Errorf("interval [%d,%d): SumTimeS = %v, want %v (P observations per vehicle)", r.BeginTick, r.EndTick, r.SumTimeS, wantTime)
		}
	}
	// Phantom placement distance would add Σ initial S ≈ 2415 m to the first
	// interval; real travel is ≈ 22 × 2.15 m/s × 20 s ≈ 950 m.
	if recs[0].SumDistM > 1500 {
		t.Errorf("first interval SumDistM = %v m — initial placement booked as distance?", recs[0].SumDistM)
	}
	tot := k.Totals()
	var sumDist, tripDist float64
	for _, r := range recs {
		sumDist += r.SumDistM
	}
	for _, tr := range k.DrainTrips() {
		if tr.Completed {
			t.Error("ring has no exits — no trip should complete")
		}
		if tr.EntryTick != 0 || tr.OriginLaneID != "R0" {
			t.Errorf("pre-placed trip %d: entry %d origin %q, want entry 0 origin R0 (seeded at construction)",
				tr.VehicleID, tr.EntryTick, tr.OriginLaneID)
		}
		tripDist += tr.DistanceM
	}
	if tot.VMT <= 0 {
		t.Error("VMT = 0 on a moving ring")
	}
	if !closeWithin(sumDist, tot.VMT) {
		t.Errorf("Σ interval distance %v vs VMT %v", sumDist, tot.VMT)
	}
	if !closeWithin(tripDist, tot.VMT) {
		t.Errorf("Σ trip distance %v vs VMT %v — wrap corrupts distance", tripDist, tot.VMT)
	}
}

// Despawn-tick accounting: a lone vehicle at constant speed crossing a 500 m
// exit lane must book exactly the lane length — including the final
// last-seen-position → lane-end segment — to its trip and to the totals.
func TestKernelDespawnAccounting(t *testing.T) {
	spec := RunSpec{
		Net:    NetSpec{Kind: "straight", Length: 500},
		Params: DefaultParams(),
		Ticks:  200,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	lane := e.Net.LaneByID("S0")
	e.AddInitialVehicle(lane, 0, 0, 33.3, 1) // v = v0 ⇒ zero accel, constant speed
	k, err := NewKernel(e, DefaultKernelConfig(e.Net, e.Params.Dt))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)

	tot := k.Totals()
	if tot.CompletedTrips != 1 {
		t.Fatalf("CompletedTrips = %d, want 1", tot.CompletedTrips)
	}
	// 150 ticks at 3.33 m/tick = 499.5 m, then the exit segment to 500 m.
	if !closeWithin(tot.VMT, 500) {
		t.Errorf("VMT = %v m, want 500 (lane length)", tot.VMT)
	}
	wantVHT := 150*0.1 + 0.5/33.3 // full ticks + the estimated exit time
	if !closeWithin(tot.VHT, wantVHT) {
		t.Errorf("VHT = %v s, want %v", tot.VHT, wantVHT)
	}
	trips := k.DrainTrips()
	if len(trips) != 1 {
		t.Fatalf("got %d trip records, want 1", len(trips))
	}
	tr := trips[0]
	if !tr.Completed || tr.ExitTick != 151 || tr.EntryTick != 0 {
		t.Errorf("trip = %+v, want Completed with ticks [0,151] (pre-placed ⇒ entry 0)", tr)
	}
	if !closeWithin(tr.DistanceM, 500) {
		t.Errorf("trip DistanceM = %v m, want 500 — exit segment missing?", tr.DistanceM)
	}
	recs := k.DrainIntervals()
	if len(recs) != 1 || !recs[0].Partial {
		t.Fatalf("want one partial interval record, got %+v", recs)
	}
	if !closeWithin(recs[0].SumDistM, 500) || !closeWithin(recs[0].SumTimeS, wantVHT) {
		t.Errorf("interval sums = %v m / %v s, want 500 / %v", recs[0].SumDistM, recs[0].SumTimeS, wantVHT)
	}
}

// Denied-entry guard (ADR-0014 §3): a director directive whose EarliestTick
// is still in the future is scheduled demand, not denied — it accrues no
// wait; an overdue one blocked by the density cap does.
func TestKernelDeniedFutureDirective(t *testing.T) {
	spec := RunSpec{
		Net:    NetSpec{Kind: "lanedrop"},
		Params: DefaultParams(),
		Scen:   Scenario{DensityTargetPerKm: 1}, // 3 lane-km ⇒ 3 vehicles cap
		Ticks:  30,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Fill the density cap so the due directive stays queued.
	for _, id := range []string{"A0", "A1", "A2"} {
		e.AddInitialVehicle(e.Net.LaneByID(id), 0, 100, 10, 1)
	}
	k, err := NewKernel(e, DefaultKernelConfig(e.Net, e.Params.Dt))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "future", Origin: "A0", TypeName: "car", EarliestTick: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{RequestID: "due", Origin: "A1", TypeName: "car", EarliestTick: 0}); err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	tot := k.Totals()
	if w := tot.DeniedByLane["A0"].WaitS; w != 0 {
		t.Errorf("future directive accrued %v s of denied wait, want 0", w)
	}
	if p := tot.DeniedByLane["A0"].Pending; p != 0 {
		t.Errorf("future directive counted %v pending at horizon, want 0", p)
	}
	if w := tot.DeniedByLane["A1"].WaitS; w <= 0 {
		t.Error("overdue, density-blocked directive accrued no denied wait")
	}
	if p := tot.DeniedByLane["A1"].Pending; p != 1 {
		t.Errorf("overdue directive pending = %v, want 1", p)
	}
}

// The default plan (ADR-0014 §5): every lane, sorted IDs, all four field
// groups at 9000 ticks, trips on. Bad plans fail loud at construction.
func TestDefaultKernelConfig(t *testing.T) {
	net, err := BuildNet(NetSpec{Kind: "lanedrop"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultKernelConfig(net, 0.1)
	if !cfg.Trips {
		t.Error("default plan has trips off")
	}
	if len(cfg.Sets) != 1 {
		t.Fatalf("default plan has %d sets, want 1", len(cfg.Sets))
	}
	s := cfg.Sets[0]
	want := []string{"A0", "A1", "A2", "B0", "B1"}
	if !reflect.DeepEqual(s.LaneIDs, want) {
		t.Errorf("default lanes = %v, want %v", s.LaneIDs, want)
	}
	if s.Groups != (MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true}) {
		t.Errorf("default groups = %+v, want all four on", s.Groups)
	}
	if s.PeriodTicks != 9000 {
		t.Errorf("default period = %d ticks, want 9000 (900 s)", s.PeriodTicks)
	}
	// The 900 s window derives from dt (ADR-0005 configurable tick):
	// dt = 0.2 → 4500 ticks.
	if got := DefaultKernelConfig(net, 0.2).Sets[0].PeriodTicks; got != 4500 {
		t.Errorf("default period at dt=0.2 = %d ticks, want 4500", got)
	}

	e, err := NewEngine(RunSpec{Net: NetSpec{Kind: "lanedrop"}, Params: DefaultParams()})
	if err != nil {
		t.Fatal(err)
	}
	bad := DefaultKernelConfig(e.Net, e.Params.Dt)
	bad.Sets[0].LaneIDs = []string{"A0", "NOPE"}
	if _, err := NewKernel(e, bad); err == nil {
		t.Error("unknown lane ID accepted")
	}
	bad = DefaultKernelConfig(e.Net, e.Params.Dt)
	bad.Sets[0].LaneIDs = []string{"A0", "A0"}
	if _, err := NewKernel(e, bad); err == nil {
		t.Error("duplicate lane ID accepted")
	}
	bad = DefaultKernelConfig(e.Net, e.Params.Dt)
	bad.Sets[0].PeriodTicks = 0
	if _, err := NewKernel(e, bad); err == nil {
		t.Error("zero period accepted")
	}
	bad = DefaultKernelConfig(e.Net, e.Params.Dt)
	bad.Sets[0].BeginTick, bad.Sets[0].LastTick = 100, 100
	if _, err := NewKernel(e, bad); err == nil {
		t.Error("LastTick == BeginTick accepted")
	}
	bad = DefaultKernelConfig(e.Net, e.Params.Dt)
	bad.Sets[0].BeginTick, bad.Sets[0].LastTick = 100, 50
	if _, err := NewKernel(e, bad); err == nil {
		t.Error("LastTick < BeginTick accepted")
	}
	e2, err := NewEngine(RunSpec{Net: NetSpec{Kind: "lanedrop"}, Params: DefaultParams()})
	if err != nil {
		t.Fatal(err)
	}
	e2.Net.LaneByID("A0").Length = 0
	if _, err := NewKernel(e2, DefaultKernelConfig(e2.Net, e2.Params.Dt)); err == nil {
		t.Error("zero-length element lane accepted")
	}
}

// First-Step despawn of a pre-placed vehicle: seeded at construction with
// entry tick 0 (origin = dest = construction lane), it must complete
// cleanly on the very first Step — exercising the seeded-state despawn
// path. (The nil-tripState reconstruction branch stays as a defensive
// guard; seeding means this test no longer hits it.)
func TestKernelFirstStepDespawn(t *testing.T) {
	spec := RunSpec{
		Net:    NetSpec{Kind: "straight", Length: 500},
		Params: DefaultParams(),
		Ticks:  10,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// One tick from the lane end at 33.3 m/s (3.33 m/tick): despawns on Step 1.
	e.AddInitialVehicle(e.Net.LaneByID("S0"), 0, 497, 33.3, 1)
	k, err := NewKernel(e, DefaultKernelConfig(e.Net, e.Params.Dt))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	tot := k.Totals()
	if tot.CompletedTrips != 1 {
		t.Fatalf("CompletedTrips = %d, want 1", tot.CompletedTrips)
	}
	if !closeWithin(tot.VMT, 3) {
		t.Errorf("VMT = %v, want 3 (the exit segment)", tot.VMT)
	}
	trips := k.DrainTrips()
	if len(trips) != 1 {
		t.Fatalf("got %d trip records, want 1", len(trips))
	}
	tr := trips[0]
	if !tr.Completed || tr.EntryTick != 0 || tr.ExitTick != 1 {
		t.Errorf("pre-placed trip = %+v, want Completed with ticks [0,1]", tr)
	}
	if tr.OriginLaneID != "S0" || tr.DestLaneID != "S0" {
		t.Errorf("pre-placed trip origin/dest = %q/%q, want S0/S0", tr.OriginLaneID, tr.DestLaneID)
	}
	if !closeWithin(tr.DistanceM, 3) {
		t.Errorf("pre-placed trip DistanceM = %v, want 3", tr.DistanceM)
	}
}

// Crossing time-loss clamp semantics (ADR-0014 §3): ONE clamp per
// vehicle-tick, then a distance-proportional split — the shares are
// non-negative and sum exactly to the clamped tick total. With vdOld = 39
// and vdNew = 43.29 the old side's raw share is negative (−0.00064), so the
// round-2 per-side clamp would inflate the tick total (0.00570 vs the pinned
// 0.00506): this case discriminates the two formulas.
func TestKernelCrossingTimeLossClamp(t *testing.T) {
	prev := &Lane{ID: "old", Length: 600, SpeedLimit: 30}
	cur := &Lane{ID: "new", Length: 600, SpeedLimit: 40}
	prev.Successors = []*Lane{cur}
	k := &Kernel{
		dt: 0.1,
		sets: []*metricSet{{
			cfg: MetricSetConfig{ID: "s", PeriodTicks: 100},
			acc: map[string]*laneAccum{"old": {}, "new": {}},
		}},
	}
	v := &Vehicle{ID: 1, Type: &Car, F: 1.3, S: 3, Lane: cur}
	// vdOld = 1.3 × min(33.3, 30) = 39; vdNew = 1.3 × min(33.3, 40) = 43.29.
	// dOld = 1 m (prevS 599 of 600), dNew = 3 m.
	vdOld, vdNew := 1.3*30.0, 1.3*33.3
	want := 0.1 - (1.0/vdOld + 3.0/vdNew)
	if want <= 0 {
		t.Fatalf("test geometry broken: clamped tick loss %v ≤ 0", want)
	}
	if raw := 0.025 - 1.0/vdOld; raw >= 0 {
		t.Fatalf("test geometry broken: old-side raw share %v ≥ 0 (no discrimination)", raw)
	}
	tl, _, _ := k.splitTick(mLastObs{lane: prev, s: 599}, cur, v, 0.1, 1, nil)
	if !closeWithin(tl, want) {
		t.Errorf("tick time-loss = %v, want single-clamp value %v", tl, want)
	}
	oldShare := k.sets[0].acc["old"].sumTimeLoss
	newShare := k.sets[0].acc["new"].sumTimeLoss
	if oldShare <= 0 {
		t.Errorf("old lane share = %v — per-side clamping would zero it; want proportional share %v", oldShare, want/4)
	}
	if newShare < 0 {
		t.Errorf("new lane share = %v, want ≥ 0", newShare)
	}
	if !closeWithin(oldShare+newShare, tl) {
		t.Errorf("shares %v + %v ≠ tick total %v", oldShare, newShare, tl)
	}
	// Distance/time split is unchanged: 1 m / 3 m, 0.025 s / 0.075 s.
	if d := k.sets[0].acc["old"].sumDist; d != 1 {
		t.Errorf("old lane distance = %v, want 1", d)
	}
	if tm := k.sets[0].acc["new"].sumTime; !closeWithin(tm, 0.075) {
		t.Errorf("new lane time = %v, want 0.075", tm)
	}
}

// crossedChain resolves the TRAVELED chain of a boundary-crossing tick
// (Step moves and crosses boundaries before hopping): direct successor,
// multi-boundary traversal (sub-vehicle-length internal lanes, routine on
// netimport/OSM nets), and crossing+hop all resolve; a genuinely disjoint
// change finds no chain; parallel chains with different booked lengths are
// ambiguous (loud); same-length parallels are not. Hand-wired lanes,
// lateral neighbors deliberately NOT index-consecutive.
func TestCrossedChain(t *testing.T) {
	prev := &Lane{ID: "A0", Index: 0}
	mid := &Lane{ID: "J0", Index: 7, Length: 2} // junction interior, far in file order
	succ := &Lane{ID: "B0", Index: 5}
	hop := &Lane{ID: "B1", Index: 2} // lateral of succ, far from it in file order
	succ.Left = hop
	prev.Successors = []*Lane{mid}
	mid.Successors = []*Lane{succ}

	chain, ambiguous, ok := crossedChain(prev, succ)
	if !ok || ambiguous || len(chain) != 3 || chain[0] != prev || chain[1] != mid || chain[2] != succ {
		t.Errorf("multi-boundary: chain = %v, ambiguous = %v, ok = %v, want [A0 J0 B0] unambiguous", laneIDs(chain), ambiguous, ok)
	}
	chain, ambiguous, ok = crossedChain(prev, mid)
	if !ok || ambiguous || len(chain) != 2 || chain[1] != mid {
		t.Errorf("direct crossing: chain = %v, ambiguous = %v, ok = %v, want [A0 J0] unambiguous", laneIDs(chain), ambiguous, ok)
	}
	chain, ambiguous, ok = crossedChain(prev, hop)
	if !ok || ambiguous || chain[len(chain)-1] != succ {
		t.Errorf("crossing+hop: chain = %v, ambiguous = %v, ok = %v, want arrival B0 (not the hop target)", laneIDs(chain), ambiguous, ok)
	}
	stranger := &Lane{ID: "X", Index: 6}
	if _, _, ok := crossedChain(prev, stranger); ok {
		t.Error("genuinely disjoint lane change resolved a chain")
	}

	// Parallel internals of DIFFERENT lengths to the same arrival: the
	// actual path is unobservable — shortest books, ambiguity is loud.
	p := &Lane{ID: "P", Index: 0, Length: 500}
	m1 := &Lane{ID: "M1", Index: 1, Length: 1}
	m2 := &Lane{ID: "M2", Index: 2, Length: 2}
	q := &Lane{ID: "Q", Index: 3, Length: 500}
	p.Successors = []*Lane{m1, m2}
	m1.Successors = []*Lane{q}
	m2.Successors = []*Lane{q}
	chain, ambiguous, ok = crossedChain(p, q)
	if !ok || !ambiguous {
		t.Errorf("parallel 1 m vs 2 m internals: chain = %v, ambiguous = %v, ok = %v, want shortest chain, ambiguous", laneIDs(chain), ambiguous, ok)
	}
	if chain[1] != m1 {
		t.Errorf("parallel internals: booked mid = %v, want M1 (shortest first)", chain[1].ID)
	}
	// Identical-length parallels book the same VMT but the PER-LANE
	// attribution is still arbitrary — ambiguous too (round-11 semantics).
	m3 := &Lane{ID: "M3", Index: 4, Length: 1}
	m3.Successors = []*Lane{q}
	p.Successors = []*Lane{m1, m3}
	if _, ambiguous, ok = crossedChain(p, q); !ok || !ambiguous {
		t.Errorf("identical-length parallels: ambiguous = %v, ok = %v, want loud (arbitrary attribution)", ambiguous, ok)
	}
	// ≥3 divergent parallels: the enumeration cap truncates — a differing
	// unenumerated chain could exist, so truncation itself is loud.
	m4 := &Lane{ID: "M4", Index: 5, Length: 4}
	m4.Successors = []*Lane{q}
	p.Successors = []*Lane{m1, m2, m4}
	if _, ambiguous, ok = crossedChain(p, q); !ok || !ambiguous {
		t.Errorf("cap-truncated enumeration: ambiguous = %v, ok = %v, want loud", ambiguous, ok)
	}
}

func laneIDs(chain []*Lane) []string {
	ids := make([]string, len(chain))
	for i, l := range chain {
		ids[i] = l.ID
	}
	return ids
}

// A window-truncated final interval streams as soon as its LastTick has been
// observed — it must not wait for the run-wide Finalize.
func TestKernelLastTickStreaming(t *testing.T) {
	spec := lightDemandSpec(t, 400)
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "win",
				LaneIDs:     []string{"S0"},
				Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
				PeriodTicks: 100,
				LastTick:    250,
			}},
			Trips: true,
		}
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKernel(e, cfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	// Before Finalize: both full intervals AND the LastTick-truncated partial
	// must already be drained — the partial closed at tick 250.
	recs := k.DrainIntervals()
	if len(recs) != 3 {
		t.Fatalf("got %d records before Finalize, want 3 (2 full + streamed partial)", len(recs))
	}
	last := recs[2]
	if !last.Partial || last.BeginTick != 200 || last.EndTick != 250 {
		t.Errorf("streamed partial = [%d,%d) partial=%v, want [200,250) partial=true",
			last.BeginTick, last.EndTick, last.Partial)
	}
	k.Finalize(e)
	if n := len(k.DrainIntervals()); n != 0 {
		t.Errorf("Finalize emitted %d more interval records, want 0 (window already streamed)", n)
	}
}

// A second Finalize is a no-op: totals (in particular the denied-pending
// counts) don't double, and no further records are emitted.
func TestKernelFinalizeIdempotent(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 600, 7)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3000
	spec.Scen.DensityTargetPerKm = 80 // overload ⇒ denied demand pending at horizon
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKernel(e, DefaultKernelConfig(e.Net, e.Params.Dt))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	first := k.Totals()
	if first.DeniedPending == 0 {
		t.Fatal("no denied demand pending at horizon — the doubling case went untested")
	}
	if len(k.DrainIntervals()) == 0 || len(k.DrainTrips()) == 0 {
		t.Fatal("first Finalize emitted nothing")
	}
	k.Finalize(e) // no-op
	second := k.Totals()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("Totals changed after a second Finalize:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if n := len(k.DrainIntervals()); n != 0 {
		t.Errorf("second Finalize emitted %d interval records", n)
	}
	if n := len(k.DrainTrips()); n != 0 {
		t.Errorf("second Finalize emitted %d trip records", n)
	}
}

// Multi-boundary traversal in one tick (netimport/OSM nets have
// sub-vehicle-length internal lanes): a 3.33 m move crossing prev → 2 m mid
// → cur books prev's remainder, mid's FULL length, and the arrival
// remainder — distance conserved exactly.
func TestKernelMultiBoundaryConservation(t *testing.T) {
	prev := &Lane{ID: "prev", Length: 500, SpeedLimit: 33.3}
	mid := &Lane{ID: "mid", Length: 2, SpeedLimit: 33.3}
	cur := &Lane{ID: "cur", Length: 500, SpeedLimit: 33.3}
	prev.Successors = []*Lane{mid}
	mid.Successors = []*Lane{cur}
	k := &Kernel{
		dt: 0.1,
		sets: []*metricSet{{
			cfg: MetricSetConfig{ID: "s", PeriodTicks: 100},
			acc: map[string]*laneAccum{"prev": {}, "mid": {}, "cur": {}},
		}},
	}
	v := &Vehicle{ID: 1, Type: &Car, F: 1, S: 0.83, Lane: cur}
	k.splitTick(mLastObs{lane: prev, s: 499.5}, cur, v, 0.1, 1, nil)
	acc := k.sets[0].acc
	if !closeWithin(acc["prev"].sumDist, 0.5) {
		t.Errorf("prev share = %v, want 0.5", acc["prev"].sumDist)
	}
	if acc["mid"].sumDist != 2 {
		t.Errorf("mid share = %v, want its full length 2", acc["mid"].sumDist)
	}
	if !closeWithin(acc["cur"].sumDist, 0.83) {
		t.Errorf("cur share = %v, want 0.83", acc["cur"].sumDist)
	}
	if !closeWithin(k.vmt, 0.5+2+0.83) {
		t.Errorf("VMT = %v, want %v (conserved)", k.vmt, 0.5+2+0.83)
	}
	if !closeWithin(acc["prev"].sumTime+acc["mid"].sumTime+acc["cur"].sumTime, 0.1) {
		t.Error("time shares do not sum to dt")
	}
}

// Despawn after crossing: the exit lane precedes its feeder in file order,
// so boundaries() reprocesses it in the same pass — the vehicle crosses AND
// despawns in one tick, with its last observation on the FEEDER. The trip
// must book the feeder remainder plus the exit lane's full length.
func TestKernelDespawnAfterCrossing(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "cross-despawn",
		Lanes: []NetLane{
			{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {502, 0}}, Exit: true},
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"x_0"}, Origin: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Ticks:  10,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// 0.5 m from the feeder end at 33.3 m/s (3.33 m/tick): crosses onto the
	// 2 m exit lane AND despawns past its end in tick 1.
	e.AddInitialVehicle(e.Net.LaneByID("a_0"), 0, 499.5, 33.3, 1)
	k, err := NewKernel(e, DefaultKernelConfig(e.Net, e.Params.Dt))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	tot := k.Totals()
	if tot.CompletedTrips != 1 {
		t.Fatalf("CompletedTrips = %d, want 1", tot.CompletedTrips)
	}
	const want = 0.5 + 2.0 // feeder remainder + exit lane full length
	if !closeWithin(tot.VMT, want) {
		t.Errorf("VMT = %v, want %v", tot.VMT, want)
	}
	trips := k.DrainTrips()
	if len(trips) != 1 {
		t.Fatalf("got %d trip records, want 1", len(trips))
	}
	if !closeWithin(trips[0].DistanceM, want) {
		t.Errorf("trip DistanceM = %v, want %v — exit chain not booked", trips[0].DistanceM, want)
	}
	if trips[0].DestLaneID != "x_0" {
		t.Errorf("trip dest = %q, want x_0 (the resolved exit lane)", trips[0].DestLaneID)
	}
}

// A vehicle first observed OFF any origin lane spawned and crossed within
// its spawn tick — the traveled chain is unobservable, so the kernel books
// the seen lane but counts a DroppedCrossing.
func TestKernelSpawnThenCross(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "spawn-cross",
		Lanes: []NetLane{
			{ID: "a_0", Section: "a", Length: 2, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {2, 0}}, Successors: []string{"b_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{2, 0}, {502, 0}}, Exit: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Scen:   Scenario{SpawnRatePerLaneHour: 3600},
		Ticks:  30,
	}
	// Every spawn crosses the 2 m origin lane within its spawn tick (entry
	// speed ≥ 8 m/s ⇒ ≥ 0.8 m... at v0 ≥ 26.6 m/s ⇒ ≥ 2.66 m > 2 m).
	_, k := runWithKernel(t, spec, defaultCfg)
	if got := k.Totals().DroppedCrossings; got == 0 {
		t.Error("off-origin first observations not counted — DroppedCrossings = 0")
	}
}

// Branched feeder with TWO reachable short exits: the actual exit lane is
// unobservable post-hoc, so the despawn books only the last-seen lane's
// remainder and counts a DroppedCrossing — never a guessed branch.
func TestKernelDespawnAmbiguousExit(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "ambiguous-exit",
		Lanes: []NetLane{
			{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {502, 0}}, Exit: true},
			{ID: "y_0", Section: "y", Length: 2, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {502, 0}}, Exit: true},
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"x_0", "y_0"}, Origin: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Ticks:  10,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	e.AddInitialVehicle(e.Net.LaneByID("a_0"), 0, 499.5, 33.3, 1)
	k, err := NewKernel(e, defaultCfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	tot := k.Totals()
	if tot.CompletedTrips != 1 {
		t.Fatalf("CompletedTrips = %d, want 1", tot.CompletedTrips)
	}
	if got := tot.DroppedCrossings; got != 1 {
		t.Errorf("DroppedCrossings = %d, want 1 (ambiguous exit)", got)
	}
	trips := k.DrainTrips()
	if len(trips) != 1 {
		t.Fatalf("got %d trip records, want 1", len(trips))
	}
	if !closeWithin(trips[0].DistanceM, 0.5) {
		t.Errorf("trip DistanceM = %v, want 0.5 (prev remainder only — no guessed branch)", trips[0].DistanceM)
	}
	if trips[0].DestLaneID != "a_0" {
		t.Errorf("trip dest = %q, want a_0 (exit lane unresolved — last-seen stands)", trips[0].DestLaneID)
	}
}

// DeniedServed (ADR-0014 §3 "denied-entry count"): vehicles that waited past
// their scheduled/request tick and then left the pending state — spawner
// origins unblocking after an overload, a director directive injected after
// a wait, and a director directive expiring unserved.
func TestKernelDeniedServed(t *testing.T) {
	// Spawner: overload, then the queue clears and origins serve again.
	spec, err := DefaultSpec("lanedrop", 600, 11)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3600
	spec.Scen.DensityTargetPerKm = 1
	_, k := runWithKernel(t, spec, defaultCfg)
	if got := k.Totals().DeniedServed; got == 0 {
		t.Error("spawner: DeniedServed = 0 after overload-then-clear")
	}

	// Director, injected after a wait: cap filled, directive waits, the
	// pre-placed vehicles clear (600 m feeder + 600 m exit at 33.3 m/s ⇒
	// ~184 ticks), the directive injects.
	mkRun := func(secLen float64, target float64, ticks uint64, placeS float64) (*Engine, *Kernel) {
		spec := RunSpec{
			Net:    NetSpec{Kind: "lanedrop", SectionLen: secLen},
			Params: DefaultParams(),
			Scen:   Scenario{DensityTargetPerKm: target},
			Ticks:  ticks,
		}
		e, err := NewEngine(spec)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"A0", "A1", "A2"} {
			e.AddInitialVehicle(e.Net.LaneByID(id), 0, placeS, 33.3, 1)
		}
		k, err := NewKernel(e, defaultCfg(e.Net))
		if err != nil {
			t.Fatal(err)
		}
		if err := e.EnqueueSpawn(SpawnDirective{RequestID: "due", Origin: "A1", TypeName: "car", EarliestTick: 0}); err != nil {
			t.Fatal(err)
		}
		for e.Tick < spec.Ticks {
			e.Step()
			k.Observe(e)
		}
		k.Finalize(e)
		return e, k
	}
	_, k2 := mkRun(600, 1, 220, 590) // clear at ~tick 184 ⇒ directive injects after a wait
	if got := k2.Totals().DeniedByLane["A1"].Served; got != 1 {
		t.Errorf("director injected-after-wait: Served = %d, want 1", got)
	}
	// Director, expired unserved: 60 km lanes and a tiny density target —
	// the cap never clears, so the directive drops after
	// DirectorSpawnHoldTicks (600).
	_, k3 := mkRun(60000, 0.005, 650, 100)
	if got := k3.Totals().DeniedByLane["A1"].Served; got != 1 {
		t.Errorf("director expired: Served = %d, want 1", got)
	}
	if got := k3.Totals().DeniedByLane["A1"].Pending; got != 0 {
		t.Errorf("director expired: Pending = %v, want 0 (dropped demand is wait-without-pending)", got)
	}
}

// The pairing guard: skipped or doubled ticks and observations after
// Finalize panic loudly instead of mis-booking silently.
func TestKernelObservePairingGuard(t *testing.T) {
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("%s: no panic", name)
			}
		}()
		fn()
	}
	spec, err := DefaultSpec("straight", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKernel(e, defaultCfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	e.Step()
	e.Step()
	mustPanic("first Observe not at tick 1", func() { k.Observe(e) })

	e2, _ := NewEngine(spec)
	k2, _ := NewKernel(e2, defaultCfg(e2.Net))
	e2.Step()
	k2.Observe(e2) // tick 1
	e2.Step()
	e2.Step() // tick 3 observed after tick 1
	mustPanic("skipped tick", func() { k2.Observe(e2) })

	e3, _ := NewEngine(spec)
	k3, _ := NewKernel(e3, defaultCfg(e3.Net))
	e3.Step()
	k3.Observe(e3)
	k3.Finalize(e3)
	e3.Step()
	mustPanic("Observe after Finalize", func() { k3.Observe(e3) })

	e4, _ := NewEngine(spec)
	k4, _ := NewKernel(e4, defaultCfg(e4.Net))
	e4.Step()
	k4.Observe(e4)
	e4.Step() // stepped without observing
	mustPanic("Finalize after an unobserved Step", func() { k4.Finalize(e4) })
}

// A genuinely disjoint lane change books the defensive split but is LOUD:
// DroppedCrossings surfaces in Totals.
func TestKernelDroppedCrossings(t *testing.T) {
	prev := &Lane{ID: "prev", Length: 500, SpeedLimit: 33.3}
	cur := &Lane{ID: "cur", Length: 500, SpeedLimit: 33.3} // unrelated: no chain
	k := &Kernel{
		dt: 0.1,
		sets: []*metricSet{{
			cfg: MetricSetConfig{ID: "s", PeriodTicks: 100},
			acc: map[string]*laneAccum{"prev": {}, "cur": {}},
		}},
	}
	v := &Vehicle{ID: 1, Type: &Car, F: 1, S: 3, Lane: cur}
	k.splitTick(mLastObs{lane: prev, s: 499}, cur, v, 0.1, 1, nil)
	if got := k.Totals().DroppedCrossings; got != 1 {
		t.Errorf("DroppedCrossings = %d, want 1", got)
	}
}

// deniedExpect integrates the documented backlog rule per origin from the
// blocked schedule tick (st.tick, frozen while blocked) to the horizon:
// newly overdue b = 1 (the held vehicle; lag 0 here), then b += rate·dt per
// overdue tick; wait accrues dt·b. Returns (wait, pending).
func deniedExpect(t0, horizon uint64, rateAt func(tau uint64) float64, dt float64) (wait, pending float64) {
	b := 1.0
	for tau := t0; tau <= horizon; tau++ {
		if tau > t0 {
			b += rateAt(tau) * dt
		}
		wait += dt * b
	}
	return wait, b
}

// Denied-entry backlog (ADR-0014 §3): a permanently blocked origin at rate r
// accrues wait over the growing INTEGRATED mean-field backlog, not over the
// one held vehicle.
func TestKernelDeniedBacklog(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 300, 11)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3600 // 1 veh/s per origin
	spec.Scen.DensityTargetPerKm = 1      // 3 lane-km ⇒ 3 vehicles, then permanent block
	e, k := runWithKernel(t, spec, defaultCfg)
	tot := k.Totals()
	const dt = 0.1
	blocked := map[string]uint64{}
	for i := range e.spawner.origins {
		st := &e.spawner.origins[i]
		blocked[st.lane.ID] = st.tick // frozen schedule tick while blocked
	}
	seen := 0
	for lane, t0 := range blocked {
		seen++
		ld := tot.DeniedByLane[lane]
		if ld.Pending <= 1 {
			t.Errorf("%s: pending backlog = %v, want > 1 (mean-field, not the one held vehicle)", lane, ld.Pending)
			continue
		}
		want, wantP := deniedExpect(t0, 300, func(uint64) float64 { return 1.0 }, dt)
		if !closeWithin(ld.WaitS, want) {
			t.Errorf("%s: wait %v s, want %v (integrated backlog)", lane, ld.WaitS, want)
		}
		if !closeWithin(ld.Pending, wantP) {
			t.Errorf("%s: pending %v, want %v (integrated backlog at horizon)", lane, ld.Pending, wantP)
		}
	}
	if seen == 0 {
		t.Fatal("no blocked origin — the backlog case went untested")
	}
}

// A DemandSchedule rate change applies PROSPECTIVELY to the denied backlog:
// the rate triples mid-overload and the integrated path bends there — a
// retroactive lag × current-rate formula would revalue the whole history.
func TestKernelDeniedBacklogRateChange(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 300, 11)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3600
	spec.Scen.DensityTargetPerKm = 1
	spec.Scen.DemandSchedule = []DemandStep{{Tick: 100, Scale: 3}}
	e, k := runWithKernel(t, spec, defaultCfg)
	tot := k.Totals()
	const dt = 0.1
	rateAt := func(tau uint64) float64 {
		if tau >= 100 {
			return 3.0
		}
		return 1.0
	}
	seen := 0
	for i := range e.spawner.origins {
		st := &e.spawner.origins[i]
		lane := st.lane.ID
		ld := tot.DeniedByLane[lane]
		if ld.Pending <= 1 {
			continue
		}
		seen++
		want, wantP := deniedExpect(st.tick, 300, rateAt, dt)
		if !closeWithin(ld.WaitS, want) {
			t.Errorf("%s: wait %v s, want %v (rate triples at tick 100)", lane, ld.WaitS, want)
		}
		if !closeWithin(ld.Pending, wantP) {
			t.Errorf("%s: pending %v, want %v", lane, ld.Pending, wantP)
		}
		// The retroactive formula (lag × current rate) must NOT match.
		retro := float64(300-st.tick) * dt * 3.0
		if closeWithin(ld.Pending, retro) {
			t.Errorf("%s: pending %v matches the retroactive revaluation %v — rate change must be prospective", lane, ld.Pending, retro)
		}
	}
	if seen == 0 {
		t.Fatal("no blocked origin — the rate-change case went untested")
	}
}

// Group fields are nil when their group is off, never confusable with a
// genuine zero (which stays non-nil when the group is on).
func TestKernelGroupPointers(t *testing.T) {
	spec := lightDemandSpec(t, 200)
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "edie-only",
				LaneIDs:     []string{"S0"},
				Groups:      MetricGroups{Edie: true},
				PeriodTicks: 100,
			}},
			Trips: true,
		}
	}
	_, k := runWithKernel(t, spec, cfg)
	recs := k.DrainIntervals()
	if len(recs) == 0 {
		t.Fatal("no interval records")
	}
	for _, r := range recs {
		if r.Stops != nil || r.TimeLossS != nil {
			t.Errorf("edie-only set: Stops = %v, TimeLossS = %v, want both nil", r.Stops, r.TimeLossS)
		}
		if r.Q == nil || r.Occupancy != nil {
			t.Errorf("edie-only set: Q = %v, Occupancy = %v, want Q non-nil, Occupancy nil", r.Q, r.Occupancy)
		}
	}
}

// Attaching a kernel after the first Step is a hard error — a mid-run
// attach would book placed positions as traveled distance.
func TestNewKernelAfterStep(t *testing.T) {
	spec, err := DefaultSpec("straight", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	e.Step()
	if _, err := NewKernel(e, DefaultKernelConfig(e.Net, e.Params.Dt)); err == nil {
		t.Error("NewKernel accepted a mid-run attach (tick 1)")
	}
}

// An origin overdue from tick 1 must integrate its backlog from the held
// vehicle (≥ 1) — a phantom injection at the first Observe (unseeded
// pending-ID tracker) would silently decrement it.
func TestKernelDeniedTickOne(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 50, 5)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 36000 // 10 veh/s ⇒ first spawn scheduled at tick 1
	spec.Scen.DensityTargetPerKm = 1
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A0", "A1", "A2"} {
		e.AddInitialVehicle(e.Net.LaneByID(id), 0, 100, 10, 1)
	}
	k, err := NewKernel(e, defaultCfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	tot := k.Totals()
	for _, id := range []string{"A0", "A1", "A2"} {
		ld := tot.DeniedByLane[id]
		want, wantP := deniedExpect(1, 50, func(uint64) float64 { return 10.0 }, 0.1)
		if !closeWithin(ld.WaitS, want) {
			t.Errorf("%s: wait %v s, want %v (backlog ≥ 1 from tick 1 — phantom injection?)", id, ld.WaitS, want)
		}
		if !closeWithin(ld.Pending, wantP) {
			t.Errorf("%s: pending %v, want %v", id, ld.Pending, wantP)
		}
	}
}

// Inject-then-accrue ordering: an overdue vehicle that injects does not
// accrue a further dt of wait — the injection tick accrues the post-service
// backlog. The test replicates the documented order independently and
// asserts the old accrue-first total does NOT match.
func TestKernelDeniedInjectOrdering(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 220, 11)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3600
	spec.Scen.DensityTargetPerKm = 1
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A0", "A1", "A2"} {
		e.AddInitialVehicle(e.Net.LaneByID(id), 0, 590, 33.3, 1)
	}
	k, err := NewKernel(e, defaultCfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	type org struct {
		b, bOld             float64
		overdue, overdueOld bool
		pend, pendOld       uint64
	}
	orgs := map[string]*org{}
	for i := range e.spawner.origins {
		st := &e.spawner.origins[i]
		orgs[st.lane.ID] = &org{pend: st.pend.ID, pendOld: st.pend.ID}
	}
	var wantWait, oldWait float64
	for e.Tick < spec.Ticks {
		e.Step()
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			o := orgs[st.lane.ID]
			overdue := st.tick <= e.Tick
			// Documented order: service first, then accrue.
			if st.pend.ID != o.pend {
				if b := o.b - 1; b > 0 {
					o.b = b
				} else {
					o.b = 0
				}
				o.pend = st.pend.ID
			}
			if overdue {
				if o.overdue {
					o.b += st.rate * 0.1
				} else {
					o.b = 1
				}
				wantWait += 0.1 * o.b
			}
			o.overdue = overdue
			// Old order: accrue with the pre-service backlog, then decrement.
			if overdue {
				if o.overdueOld {
					o.bOld += st.rate * 0.1
				} else {
					o.bOld = 1
				}
				oldWait += 0.1 * o.bOld
			}
			if st.pend.ID != o.pendOld {
				if b := o.bOld - 1; b > 0 {
					o.bOld = b
				} else {
					o.bOld = 0
				}
				o.pendOld = st.pend.ID
			}
			o.overdueOld = overdue
		}
		k.Observe(e)
	}
	k.Finalize(e)
	got := k.Totals().DeniedWaitS
	if !closeWithin(got, wantWait) {
		t.Errorf("DeniedWaitS = %v, want %v (post-service backlog on injection ticks)", got, wantWait)
	}
	if closeWithin(got, oldWait) {
		t.Errorf("DeniedWaitS = %v matches the accrue-first order %v — served vehicles must not accrue", got, oldWait)
	}
}

// Spawn+despawn within one tick is invisible to observation — but vehicle
// IDs are sequential, so the gap before the next observed ID counts it
// loudly via DroppedCrossings.
func TestKernelInvisibleGap(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "invisible-gap",
		Lanes: []NetLane{
			{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{1, 0}, {3, 0}}, Exit: true},
			{ID: "a_0", Section: "a", Length: 1, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {1, 0}}, Successors: []string{"x_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{3, 0}, {503, 0}}, Exit: true, Origin: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Scen:   Scenario{SpawnRatePerLaneHour: 3600},
		Ticks:  100,
	}
	// Homogeneous drivers (F = 1): every a_0 spawn crosses the 1 m origin
	// AND the 2 m exit in its spawn tick (3.33 m) — invisible. b_0 spawns
	// are observed, and their higher IDs expose the gaps.
	spec.Params.SpeedFactorSigma = 0
	e, k := runWithKernel(t, spec, defaultCfg)
	tot := k.Totals()
	// Exact horizon reconciliation: every issued, never-observed,
	// not-pending ID — trailing invisibles included.
	npend := 0
	for i := range e.spawner.origins {
		if e.spawner.origins[i].pend != nil {
			npend++
		}
	}
	want := int(e.nextID) - 1 - len(k.seenIDs) - npend
	if tot.DroppedCrossings != want {
		t.Errorf("DroppedCrossings = %d, want exactly %d (issued %d − observed %d − pending %d)",
			tot.DroppedCrossings, want, e.nextID-1, len(k.seenIDs), npend)
	}
	if want < 2 {
		t.Fatalf("expected ≥ 2 invisible vehicles, got %d — scenario went stale", want)
	}
	if tot.VMT == 0 {
		t.Error("no observed traffic on b_0 — the gap detector had no baseline")
	}
}

// Trailing invisibles: with NO observed vehicle ever (single origin, every
// spawn invisible), the mid-run gap heuristic can never fire — only the
// exact horizon reconciliation counts them.
func TestKernelInvisibleTrailing(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "invisible-trailing",
		Lanes: []NetLane{
			{ID: "x_0", Section: "x", Length: 2, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{1, 0}, {3, 0}}, Exit: true},
			{ID: "a_0", Section: "a", Length: 1, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {1, 0}}, Successors: []string{"x_0"}, Origin: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Scen:   Scenario{SpawnRatePerLaneHour: 3600},
		Ticks:  100,
	}
	spec.Params.SpeedFactorSigma = 0
	e, k := runWithKernel(t, spec, defaultCfg)
	if len(k.seenIDs) != 0 {
		t.Fatalf("expected zero observed vehicles, saw %d", len(k.seenIDs))
	}
	want := int(e.nextID) - 1 - 1 // issued − observed(0) − the one held pend
	if got := k.Totals().DroppedCrossings; got != want {
		t.Errorf("DroppedCrossings = %d, want exactly %d (all spawns invisible, none observed)", got, want)
	}
	if want == 0 {
		t.Fatal("no invisible vehicles — scenario went stale")
	}
}

// Parallel internal lanes feeding one exit: the actual despawn path is
// unobservable — ambiguous resolution books ONLY the last-seen lane's
// remainder (dest stays last-seen) and is loud. Covers divergent lengths,
// identical lengths, and cap truncation.
func TestExitSharesAmbiguousPath(t *testing.T) {
	prev := &Lane{ID: "P", Length: 500, SpeedLimit: 33.3}
	m1 := &Lane{ID: "M1", Length: 1, SpeedLimit: 33.3}
	m2 := &Lane{ID: "M2", Length: 3, SpeedLimit: 33.3}
	x := &Lane{ID: "X", Length: 100, SpeedLimit: 33.3, Exit: true}
	prev.Successors = []*Lane{m1, m2}
	m1.Successors = []*Lane{x}
	m2.Successors = []*Lane{x}
	obs := mLastObs{lane: prev, s: 499, v: 33.3, vt: &Car, f: 1}
	assertConservative := func(name string) {
		t.Helper()
		shares, exit, ok, ambiguous := exitShares(obs)
		if ok || !ambiguous {
			t.Errorf("%s: ok %v ambiguous %v, want conservative+loud", name, ok, ambiguous)
		}
		if exit != nil {
			t.Errorf("%s: exit = %v, want nil (dest stays last-seen)", name, exit.ID)
		}
		var total float64
		for _, sh := range shares {
			total += sh.d
		}
		if !closeWithin(total, 1) {
			t.Errorf("%s: booked %v m, want 1 (prev remainder only)", name, total)
		}
	}
	assertConservative("divergent-length parallels")
	m2.Length = 1 // same total on both chains — attribution still arbitrary
	assertConservative("identical-length parallels")
	// Three divergent paths to the one exit: the enumeration cap truncates —
	// truncation itself is ambiguous (loud).
	m2.Length = 3
	m3 := &Lane{ID: "M3", Length: 5, SpeedLimit: 33.3}
	m3.Successors = []*Lane{x}
	prev.Successors = []*Lane{m1, m2, m3}
	assertConservative("cap-truncated enumeration")

	// A single unambiguous chain still books in full with the exit resolved.
	prev.Successors = []*Lane{m1}
	shares, exit, ok, ambiguous := exitShares(obs)
	if !ok || ambiguous || exit != x {
		t.Errorf("single chain: ok %v ambiguous %v exit %v, want ok, exit X", ok, ambiguous, exit)
	}
	var total float64
	for _, sh := range shares {
		total += sh.d
	}
	if !closeWithin(total, 1+1+100) {
		t.Errorf("single chain: booked %v m, want %v", total, 1.0+1+100)
	}
}

// Replay re-derivation equality (ADR-0014 §1): the same recording replayed
// with a fresh kernel attached re-derives the IDENTICAL metric stream —
// the validity check on the kernel itself.
func TestKernelReplayRederivation(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 800, 7)
	if err != nil {
		t.Fatal(err)
	}
	// Live run with the kernel attached; capture the recording.
	e1, k1 := runWithKernel(t, spec, defaultCfg)
	log := &RunLog{Spec: spec, Intents: e1.IntentLog, Spawns: e1.SpawnLog, CRCs: e1.CRCs}
	int1 := k1.DrainIntervals()
	trips1 := k1.DrainTrips()
	tot1 := k1.Totals()

	// Replay from the recording with a fresh kernel (mirrors Replay()'s
	// re-enqueue loop, plus observation).
	e2, err := NewEngine(log.Spec)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := NewKernel(e2, defaultCfg(e2.Net))
	if err != nil {
		t.Fatal(err)
	}
	ii, si := 0, 0
	for e2.Tick < log.Spec.Ticks {
		for ii < len(log.Intents) && log.Intents[ii].Tick <= e2.Tick+1 {
			if log.Intents[ii].Tick == e2.Tick+1 {
				e2.EnqueueIntent(log.Intents[ii].KeyedIntent)
			}
			ii++
		}
		for si < len(log.Spawns) && log.Spawns[si].Tick <= e2.Tick+1 {
			if log.Spawns[si].Tick == e2.Tick+1 {
				if err := e2.EnqueueSpawn(log.Spawns[si].SpawnDirective); err != nil {
					t.Fatalf("replay spawn: %v", err)
				}
			}
			si++
		}
		e2.Step()
		k2.Observe(e2)
	}
	k2.Finalize(e2)
	assertEqualCRCs(t, log.CRCs, e2.CRCs)
	if !reflect.DeepEqual(int1, k2.DrainIntervals()) {
		t.Fatal("replay re-derived different interval records")
	}
	if !reflect.DeepEqual(trips1, k2.DrainTrips()) {
		t.Fatal("replay re-derived different trip records")
	}
	if !reflect.DeepEqual(tot1, k2.Totals()) {
		t.Fatal("replay re-derived different totals")
	}
}

// Overshoot refund (netimport index ordering — internal lanes sorted LAST,
// boundaries() never re-clamps): a vehicle parked past a tiny internal
// lane's end gets the overshoot refunded next tick, distance-only. Per-lane
// nets over the crossing ticks are exact (each internal lane nets precisely
// its own length) and VMT matches ground-truth travel, not just internal
// consistency.
func TestKernelOvershootRefund(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "overshoot",
		Lanes: []NetLane{
			{ID: "f_0", Section: "f", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"j_0"}, Origin: true},
			{ID: "n_0", Section: "n", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{502.5, 0}, {1002.5, 0}}, Exit: true},
			{ID: "j_0", Section: "j", Length: 0.5, SpeedLimit: 33.3, Width: 3.2, Internal: true,
				Shape: [][2]float64{{500, 0}, {500.5, 0}}, Successors: []string{"m_0"}},
			{ID: "m_0", Section: "j", Length: 2, SpeedLimit: 33.3, Width: 3.2, Internal: true,
				Shape: [][2]float64{{500.5, 0}, {502.5, 0}}, Successors: []string{"n_0"}},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Ticks:  3,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// 0.3 m from the feeder end at a constant 33.3 m/s (3.33 m/tick):
	// tick 1 crosses f_0 and parks at S = 3.03 on 0.5 m j_0; tick 2 crosses
	// j_0 and parks at S = 5.86 on 2 m m_0; tick 3 lands on n_0.
	e.AddInitialVehicle(e.Net.LaneByID("f_0"), 0, 499.7, 33.3, 1)
	k, err := NewKernel(e, defaultCfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	sumByLane := map[string]float64{}
	for _, r := range k.DrainIntervals() {
		sumByLane[r.LaneID] += r.SumDistM
	}
	for lane, want := range map[string]float64{"f_0": 0.3, "j_0": 0.5, "m_0": 2.0} {
		if !closeWithin(sumByLane[lane], want) {
			t.Errorf("%s booked %v m over the run, want exactly %v (its own length)", lane, sumByLane[lane], want)
		}
	}
	const travel = 3 * 3.33 // 3 ticks at constant speed
	tot := k.Totals()
	if !closeWithin(tot.VMT, travel) {
		t.Errorf("VMT = %v, want %v (ground-truth travel — refund conserves)", tot.VMT, travel)
	}
	trips := k.DrainTrips()
	if len(trips) != 1 || !closeWithin(trips[0].DistanceM, travel) {
		t.Errorf("trip distance = %+v, want %v", trips, travel)
	}
}

// Overshoot time/occupancy/time-loss carry (ADR-0014 §3): an interval
// boundary closing right after the refund tick must show the tiny internal
// lane at its TRUE length and time — the refund is exact against the stored
// per-vehicle shares — and no record may carry a negative q.
func TestKernelOvershootTimeCarry(t *testing.T) {
	dir := t.TempDir()
	nf := NetFile{
		Version: 1,
		Name:    "overshoot-time",
		Lanes: []NetLane{
			{ID: "f_0", Section: "f", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"j_0"}, Origin: true},
			{ID: "n_0", Section: "n", Length: 500, SpeedLimit: 33.3, Width: 3.2,
				Shape: [][2]float64{{500.5, 0}, {1000.5, 0}}, Exit: true},
			{ID: "j_0", Section: "j", Length: 0.5, SpeedLimit: 33.3, Width: 3.2, Internal: true,
				Shape: [][2]float64{{500, 0}, {500.5, 0}}, Successors: []string{"n_0"}},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "network.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := RunSpec{
		Net:    NetSpec{Kind: "file", Path: p},
		Params: DefaultParams(),
		Ticks:  2,
	}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Tick 1 crosses f_0 and parks at S = 3.03 on 0.5 m j_0; tick 2 (the
	// refund tick) lands on n_0 — and period 2 closes the interval [0,2)
	// right after, so booking and refund share one interval.
	e.AddInitialVehicle(e.Net.LaneByID("f_0"), 0, 499.7, 33.3, 1)
	cfg := func(*Network) KernelConfig {
		return KernelConfig{
			Sets: []MetricSetConfig{{
				ID:          "all",
				LaneIDs:     []string{"f_0", "j_0", "n_0"},
				Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
				PeriodTicks: 2,
			}},
			Trips: true,
		}
	}
	k, err := NewKernel(e, cfg(e.Net))
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < spec.Ticks {
		e.Step()
		k.Observe(e)
	}
	k.Finalize(e)
	recs := k.DrainIntervals()
	if len(recs) != 3 {
		t.Fatalf("got %d interval records, want 3 (one per lane)", len(recs))
	}
	byLane := map[string]IntervalRecord{}
	for _, r := range recs {
		byLane[r.LaneID] = r
		if r.Q != nil && *r.Q < 0 {
			t.Errorf("lane %s: negative q %v", r.LaneID, *r.Q)
		}
		if r.SumTimeS < 0 || r.SumDistM < 0 {
			t.Errorf("lane %s: negative sums after refund: %+v", r.LaneID, r)
		}
	}
	j := byLane["j_0"]
	if !closeWithin(j.SumDistM, 0.5) {
		t.Errorf("j_0 distance = %v m, want exactly 0.5 (its length)", j.SumDistM)
	}
	if !closeWithin(j.SumTimeS, 0.5/33.3) {
		t.Errorf("j_0 time = %v s, want %v (true traversal time — exact share refund)", j.SumTimeS, 0.5/33.3)
	}
	n := byLane["n_0"]
	if !closeWithin(n.SumDistM, 5.86) {
		t.Errorf("n_0 distance = %v m, want 5.86", n.SumDistM)
	}
	if !closeWithin(n.SumTimeS, 0.1+2.53/33.3) {
		t.Errorf("n_0 time = %v s, want %v (tick + carried overshoot time)", n.SumTimeS, 0.1+2.53/33.3)
	}
}

// Mid-run spawns stamp EntryTick at the START of their entry tick (spawn at
// phase 1, sim time (T−1)·dt), so a trip's duration is whole.
func TestKernelEntryTickMidRunSpawn(t *testing.T) {
	spec := RunSpec{
		Net:    NetSpec{Kind: "straight", Length: 500},
		Params: DefaultParams(),
		Scen:   Scenario{SpawnRatePerLaneHour: 3600},
		Ticks:  200,
	}
	spec.Params.SpeedFactorSigma = 0 // F = 1 ⇒ the lead vehicle runs constant 33.3 m/s
	_, k := runWithKernel(t, spec, defaultCfg)
	trips := k.DrainTrips()
	if len(trips) == 0 || !trips[0].Completed {
		t.Fatalf("got %+v, want a completed lead trip", trips)
	}
	// The lead vehicle is never impeded: S = 3.33 m after the spawn tick's
	// movement, despawn at S > 500 on the 151st tick in-network. EntryTick
	// = spawn tick − 1 (pre-change: spawn tick — one short).
	if got := trips[0].ExitTick - trips[0].EntryTick; got != 151 {
		t.Errorf("lead trip duration = %d ticks (exit %d − entry %d), want 151",
			got, trips[0].ExitTick, trips[0].EntryTick)
	}
}
