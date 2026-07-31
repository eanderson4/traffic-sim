package engine

import (
	"strings"
	"testing"
)

// destination_test.go — ADR-0021: the route destination as a TRIP END
// (arrival despawn) and interior mid-block injection. The three properties
// these pin are (1) an interior destination ends the trip where an exit
// destination always did, (2) every pre-ADR-0021 path stays bit-identical,
// and (3) an interior injection can never be materialized on top of
// traffic behind it.

// destSpec builds the lanedrop network with the in-kernel IDM policy so
// injected vehicles actually drive without a controller attached. The
// topology is what makes it useful here: A0 (600 m, origin, NOT an exit)
// feeds B0 (600 m, exit), so "A0" is a legitimate non-exit destination and
// "B0" is an exit one.
func destSpec(t *testing.T, ticks uint64) RunSpec {
	t.Helper()
	spec, err := DefaultSpec("lanedrop", ticks, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.Types = []*VehicleType{&Car, &Truck}
	spec.Scen.SpawnRatePerLaneHour = 0 // director-only demand
	spec.Scen.UncontrolledPolicy = PolicyIDM
	// These are ADR-0021 director-queue tests on the static-routing
	// baseline; the engine default is adaptive-on (ADR-0036 addendum).
	spec.Params.AdaptiveRouting = false
	return spec
}

// runOne injects one directive and steps to the horizon, returning the tick
// the vehicle left the world (0 if it never did) and the lane it was on the
// tick before.
func runOne(t *testing.T, e *Engine, ticks uint64, d SpawnDirective) (goneAt uint64, lastLane string, maxS float64) {
	t.Helper()
	if err := e.EnqueueSpawn(d); err != nil {
		t.Fatalf("EnqueueSpawn: %v", err)
	}
	seen := false
	for e.Tick < ticks {
		e.Step()
		vs := e.Vehicles()
		switch {
		case len(vs) > 0:
			seen = true
			lastLane = vs[0].Lane.ID
			maxS = vs[0].S
		case seen && goneAt == 0:
			goneAt = e.Tick
		}
	}
	if !seen {
		t.Fatal("directive never injected")
	}
	return goneAt, lastLane, maxS
}

// TestArrivalDespawnAtNonExitDestination: a vehicle routed to a lane that
// is NOT an exit ends its trip at that lane's end instead of crossing on.
// The control run — identical but unrouted — proves the difference is the
// destination and nothing else.
func TestArrivalDespawnAtNonExitDestination(t *testing.T) {
	// Routed to A0, the lane it is born on: the trip ends at A0's end.
	e, err := NewEngine(destSpec(t, 2000))
	if err != nil {
		t.Fatal(err)
	}
	goneAt, lastLane, _ := runOne(t, e, 2000, SpawnDirective{
		RequestID: "arr", Origin: "A0", TypeName: "car", Destination: "A0",
	})
	if goneAt == 0 {
		t.Fatal("routed vehicle never arrived")
	}
	if lastLane != "A0" {
		t.Errorf("last lane before arrival = %q, want A0 (it must not cross into B0)", lastLane)
	}
	if e.Stats.Arrived != 1 || e.Stats.Despawned != 1 {
		t.Errorf("arrived=%d despawned=%d, want 1 and 1 (arrivals are a breakdown of despawns)",
			e.Stats.Arrived, e.Stats.Despawned)
	}

	// Control: same run, no destination — crosses A0 and leaves via B0.
	e2, err := NewEngine(destSpec(t, 2000))
	if err != nil {
		t.Fatal(err)
	}
	goneAt2, lastLane2, _ := runOne(t, e2, 2000, SpawnDirective{
		RequestID: "ctl", Origin: "A0", TypeName: "car",
	})
	if lastLane2 != "B0" {
		t.Errorf("unrouted last lane = %q, want B0", lastLane2)
	}
	if e2.Stats.Arrived != 0 || e2.Stats.Despawned != 1 {
		t.Errorf("unrouted: arrived=%d despawned=%d, want 0 and 1", e2.Stats.Arrived, e2.Stats.Despawned)
	}
	if goneAt2 <= goneAt {
		t.Errorf("unrouted vehicle left at tick %d, arrival at %d — the arrival must come FIRST (half the distance)",
			goneAt2, goneAt)
	}
}

// TestArrivalDespawnExitWins: a destination that IS an exit lane despawns
// through the unchanged exit path — the case ordering in boundaries() is
// what keeps every pre-ADR-0021 recording (where exit routing put an exit
// lane in Route) bit-identical.
func TestArrivalDespawnExitWins(t *testing.T) {
	e, err := NewEngine(destSpec(t, 2000))
	if err != nil {
		t.Fatal(err)
	}
	if _, lastLane, _ := runOne(t, e, 2000, SpawnDirective{
		RequestID: "x", Origin: "A0", TypeName: "car", Destination: "B0",
	}); lastLane != "B0" {
		t.Errorf("last lane = %q, want B0", lastLane)
	}
	if e.Stats.Arrived != 0 {
		t.Errorf("arrived=%d, want 0 — an exit destination is an exit despawn, not an arrival", e.Stats.Arrived)
	}
	if e.Stats.Despawned != 1 {
		t.Errorf("despawned=%d, want 1", e.Stats.Despawned)
	}
}

// TestSpawnDirectiveValidation pins the ADR-0021 reject reasons alongside
// the pre-existing ones.
func TestSpawnDirectiveValidation(t *testing.T) {
	e, err := NewEngine(destSpec(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		d    SpawnDirective
		want string
	}{
		{"interior needs offset", SpawnDirective{RequestID: "a", Origin: "B0", TypeName: "car"},
			"interior injection needs an explicit offset_m"},
		{"offset past lane end", SpawnDirective{RequestID: "b", Origin: "B0", TypeName: "car", OffsetM: 600},
			"past the end of lane"},
		{"negative offset", SpawnDirective{RequestID: "c", Origin: "A0", TypeName: "car", OffsetM: -1},
			"must be ≥ 0"},
		{"unknown interior lane", SpawnDirective{RequestID: "d", Origin: "Z9", TypeName: "car", OffsetM: 5},
			"unknown origin lane"},
		{"unknown destination", SpawnDirective{RequestID: "e", Origin: "A0", TypeName: "car", Destination: "Z9"},
			"unknown destination lane"},
		// A2 is lanedrop's dropped lane (EndWall). Arrival is S > Length,
		// but the wall stops traffic short of it, so a vehicle routed here
		// would park at the wall and never despawn — verified before the
		// guard: alive=1, arrived=0, S=598.09/600, V=0 after 4,000 ticks.
		{"endwall destination", SpawnDirective{RequestID: "f", Origin: "A0", TypeName: "car", Destination: "A2"},
			"ends in a wall"},
	}
	for _, tc := range cases {
		err := e.EnqueueSpawn(tc.d)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want reason containing %q", tc.name, err, tc.want)
		}
	}
	if e.PendingSpawns() != 0 {
		t.Fatalf("rejected verbs entered the queue: %d pending", e.PendingSpawns())
	}
}

// TestInteriorInjectionOffset: a positive offset admits a non-origin lane
// and places the vehicle there.
func TestInteriorInjectionOffset(t *testing.T) {
	e, err := NewEngine(destSpec(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{
		RequestID: "g", Origin: "B0", TypeName: "car", OffsetM: 250,
	}); err != nil {
		t.Fatalf("interior directive rejected: %v", err)
	}
	e.Step()
	vs := e.Vehicles()
	if len(vs) != 1 {
		t.Fatalf("vehicles after tick 1: %d, want 1", len(vs))
	}
	// Injected at the offset, then integrated within the same tick (the
	// Spawner's own semantics), so S has advanced a little past it.
	if vs[0].Lane.ID != "B0" || vs[0].S < 250 || vs[0].S > 254 {
		t.Fatalf("interior vehicle at lane=%s s=%.2f, want B0 just past 250", vs[0].Lane.ID, vs[0].S)
	}
}

// TestInteriorInjectionRearClearance is the safety property: an interior
// injection in front of fast traffic is DENIED (held as demand) rather than
// materialized to be rear-ended. The same directive on an empty lane
// injects immediately — so the denial is the follower, not the offset.
func TestInteriorInjectionRearClearance(t *testing.T) {
	e, err := NewEngine(destSpec(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	// A car 12 m behind the injection point at 30 m/s cannot brake
	// comfortably into that gap.
	e.AddInitialVehicle(e.Net.LaneByID("B0"), 0, 238, 30, 1)
	if err := e.EnqueueSpawn(SpawnDirective{
		RequestID: "blocked", Origin: "B0", TypeName: "car", OffsetM: 250,
	}); err != nil {
		t.Fatal(err)
	}
	e.Step()
	if len(e.Vehicles()) != 1 {
		t.Fatalf("vehicles=%d — the injection was NOT denied despite a fast follower", len(e.Vehicles()))
	}
	if e.PendingSpawns() != 1 {
		t.Fatalf("pending=%d, want 1 (a denied interior entry holds as demand)", e.PendingSpawns())
	}

	// Control: identical directive, empty lane → injects at once.
	e2, err := NewEngine(destSpec(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.EnqueueSpawn(SpawnDirective{
		RequestID: "clear", Origin: "B0", TypeName: "car", OffsetM: 250,
	}); err != nil {
		t.Fatal(err)
	}
	e2.Step()
	if len(e2.Vehicles()) != 1 || e2.PendingSpawns() != 0 {
		t.Fatalf("clear lane: vehicles=%d pending=%d, want 1 and 0", len(e2.Vehicles()), e2.PendingSpawns())
	}
}

// TestInteriorInjectionFootprintOccupied closes the gap the clearance test
// above does NOT cover. Its follower sits 7 m clear of the injected car's
// rear bumper, so it only ever exercises the measured-gap path. The two gap
// searches start from opposite bumpers — leaderAt from v.S, followerAt from
// v.S−Length — which left a window one injected-vehicle length wide where a
// vehicle's front bumper is behind the leader search and ahead of the
// follower search, and the injection was admitted straight on top of it.
//
// Probed at rest via injectionPlan rather than through Step: a stopped
// vehicle is free to MOBIL out of the injection lane, which masks the bug.
func TestInteriorInjectionFootprintOccupied(t *testing.T) {
	const injectAt = 250.0
	// Front-bumper positions bracketing the injected car's [245, 250]
	// footprint: just outside on both sides, and three points inside it.
	for _, tc := range []struct {
		existingS float64
		wantOK    bool
	}{
		{240.0, true},  // fully clear behind — admitted
		{244.9, false}, // rear bumper of the gap: ordinary follower denial
		{245.1, false}, // inside the footprint
		{247.0, false}, // inside the footprint
		{249.9, false}, // inside the footprint
		{250.1, false}, // ahead: caught by the leader rule
	} {
		e, err := NewEngine(destSpec(t, 3))
		if err != nil {
			t.Fatal(err)
		}
		lane := e.Net.LaneByID("B0")
		e.AddInitialVehicle(lane, 0, tc.existingS, 0, 1) // stopped
		_, ok := e.injectionPlan(&Vehicle{Lane: lane, S: injectAt, Type: &Car})
		if ok != tc.wantOK {
			t.Errorf("existing front bumper S=%.2f (occupies [%.2f,%.2f]), injecting [%.2f,%.2f]: admitted=%v, want %v",
				tc.existingS, tc.existingS-Car.Length, tc.existingS,
				injectAt-Car.Length, injectAt, ok, tc.wantOK)
		}
	}
}

// TestKeyframeDirectiveRoundTrip: a queued directive carrying ADR-0021
// fields survives MarshalState/RestoreState, and a queue WITHOUT them still
// writes the v3 encoding — the version gate is what keeps portal-only
// recordings byte-identical.
func TestKeyframeDirectiveRoundTrip(t *testing.T) {
	e, err := NewEngine(destSpec(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	// Far-future earliest tick keeps both entries parked IN the queue.
	if err := e.EnqueueSpawn(SpawnDirective{
		RequestID: "kf", Origin: "B0", TypeName: "truck",
		Destination: "A0", OffsetM: 123.5, EarliestTick: 1 << 40,
	}); err != nil {
		t.Fatal(err)
	}
	e.Step()
	if e.PendingSpawns() != 1 {
		t.Fatalf("pending=%d, want 1", e.PendingSpawns())
	}
	blob, err := e.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if got := keyframeVersionOf(blob); got != keyframeDestVersion {
		t.Errorf("keyframe version %d, want %d for a queue using ADR-0021 fields", got, keyframeDestVersion)
	}
	back, err := RestoreState(destSpec(t, 10), blob)
	if err != nil {
		t.Fatal(err)
	}
	if back.PendingSpawns() != 1 {
		t.Fatalf("restored pending=%d, want 1", back.PendingSpawns())
	}
	got := back.dirQueue[0]
	if got.Destination != "A0" || got.OffsetM != 123.5 || got.Origin != "B0" || got.RequestID != "kf" {
		t.Errorf("restored directive = %+v, want destination A0 / offset 123.5 / origin B0", got.SpawnDirective)
	}

	// A queue of plain portal spawns must still marshal as v3.
	e2, err := NewEngine(destSpec(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.EnqueueSpawn(SpawnDirective{
		RequestID: "plain", Origin: "A0", TypeName: "car", EarliestTick: 1 << 40,
	}); err != nil {
		t.Fatal(err)
	}
	e2.Step()
	blob2, err := e2.MarshalState()
	if err != nil {
		t.Fatal(err)
	}
	if got := keyframeVersionOf(blob2); got != keyframeQueueVersion {
		t.Errorf("keyframe version %d for an ADR-0021-free queue, want %d", got, keyframeQueueVersion)
	}
}

// keyframeVersionOf reads the version field out of a TSKF header (magic u32
// then version u16, little-endian).
func keyframeVersionOf(blob []byte) int {
	if len(blob) < 6 {
		return -1
	}
	return int(blob[4]) | int(blob[5])<<8
}

// TestInteriorInjectionMetricBooking: the metric kernel must book an
// interior injection's FIRST tick as the distance actually travelled from
// the injection point — not the whole lane prefix — and must not count it
// as a dropped crossing. Booking v.S would credit every resident with the
// entire block they never drove (on a 144-flow scenario that is a large,
// silent VMT inflation) and would make dropped_crossings fire once per
// injected vehicle.
func TestInteriorInjectionMetricBooking(t *testing.T) {
	e, err := NewEngine(destSpec(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	k, err := NewKernel(e, KernelConfig{Trips: true})
	if err != nil {
		t.Fatal(err)
	}
	const offset = 250.0
	if err := e.EnqueueSpawn(SpawnDirective{
		RequestID: "m", Origin: "B0", TypeName: "car", OffsetM: offset,
	}); err != nil {
		t.Fatal(err)
	}
	e.Step()
	k.Observe(e)

	vs := e.Vehicles()
	if len(vs) != 1 {
		t.Fatalf("vehicles = %d, want 1", len(vs))
	}
	if got := e.InteriorInjections(); len(got) != 1 || got[0].ID != vs[0].ID || got[0].S != offset {
		t.Fatalf("InteriorInjections() = %+v, want one entry at S=%v", got, offset)
	}
	tot := k.Totals()
	if tot.DroppedCrossings != 0 {
		t.Errorf("dropped_crossings = %d, want 0 — an interior injection is not an unattributable crossing",
			tot.DroppedCrossings)
	}
	// One tick of travel from the injection point, never the 250 m prefix.
	if tot.VMT >= offset {
		t.Errorf("VMT = %.2f m after one tick — the lane prefix was booked as travel (offset %.0f m)", tot.VMT, offset)
	}
	if tot.VMT <= 0 || tot.VMT > 5 {
		t.Errorf("VMT = %.4f m, want one tick of travel (≤ ~4.3 m at dt=0.1)", tot.VMT)
	}

	// A PORTAL injection is unchanged: it enters at S=0, so its first tick
	// books the whole (small) distance it really moved.
	e2, err := NewEngine(destSpec(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	k2, err := NewKernel(e2, KernelConfig{Trips: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.EnqueueSpawn(SpawnDirective{RequestID: "p", Origin: "A0", TypeName: "car"}); err != nil {
		t.Fatal(err)
	}
	e2.Step()
	k2.Observe(e2)
	if got := e2.InteriorInjections(); len(got) != 0 {
		t.Errorf("portal injection listed as interior: %+v", got)
	}
	if t2 := k2.Totals(); t2.VMT != e2.Vehicles()[0].S {
		t.Errorf("portal first-tick VMT = %.4f, want the vehicle's S %.4f", t2.VMT, e2.Vehicles()[0].S)
	}
}

// TestRouteLatDepthTable pins the lateral-depth gradient (ADR-0021) on the
// lanedrop network toward B0. The topology is the exact configuration the
// original one-lane recovery rule could not solve: A0→B0 is the only way to
// the destination, A1→B1 dead-ends at an exit that is not B0, and A2 is the
// dropped lane with no successor at all — so A2's ONLY neighbour, A1, is
// itself off-route.
func TestRouteLatDepthTable(t *testing.T) {
	e, err := NewEngine(destSpec(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lane string
		want int32
		why  string
	}{
		{"B0", 0, "the destination itself"},
		{"A0", 0, "drives straight into B0"},
		{"B1", 1, "one hop right onto B0"},
		{"A1", 1, "one hop right onto A0"},
		{"A2", 2, "two hops right: A2→A1→A0"},
	} {
		got := e.routeLatDist(e.Net.LaneByID(tc.lane), "B0")
		if got != tc.want {
			t.Errorf("latDepth[%s] toward B0 = %d, want %d (%s)", tc.lane, got, tc.want, tc.why)
		}
	}
	// An unknown destination reads as depth 0 everywhere, so the guardrail
	// degrades to pre-ADR-0021 behavior instead of pinning vehicles in lane.
	if got := e.routeLatDist(e.Net.LaneByID("A2"), "nosuchlane"); got != 0 {
		t.Errorf("latDepth toward an unknown destination = %d, want 0", got)
	}
	// A destination unreachable at any depth is −1, not a large number: the
	// guardrail must let those vehicles hop freely.
	if got := e.routeLatDist(e.Net.LaneByID("B0"), "A2"); got != -1 {
		t.Errorf("latDepth[B0] toward the unreachable A2 = %d, want -1", got)
	}
}

// TestRouteRecoveryCrossesTwoLanes: a vehicle born TWO lane changes away
// from its route walks back down the depth gradient and completes the trip.
//
// This is the ADR-0021 open item closed. Under the one-lane rule recovery
// required a neighbour that was ITSELF route-reachable; A2's only neighbour
// A1 is not, so the vehicle never recovered, rode A2 to its EndWall and
// never left the world. Reaching B0 at all is therefore the discriminator,
// not just a nicety.
func TestRouteRecoveryCrossesTwoLanes(t *testing.T) {
	const ticks = 3000
	e, err := NewEngine(destSpec(t, ticks))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.EnqueueSpawn(SpawnDirective{
		RequestID: "rec", Origin: "A2", TypeName: "car", Destination: "B0",
	}); err != nil {
		t.Fatalf("EnqueueSpawn: %v", err)
	}
	var visited []string
	seen, gone := false, false
	for e.Tick < ticks && !gone {
		e.Step()
		vs := e.Vehicles()
		if len(vs) == 0 {
			gone = seen
			continue
		}
		seen = true
		if len(visited) == 0 || visited[len(visited)-1] != vs[0].Lane.ID {
			visited = append(visited, vs[0].Lane.ID)
		}
	}
	if !seen {
		t.Fatal("directive never injected")
	}
	if !gone {
		t.Fatalf("vehicle never left the world; lanes visited: %v "+
			"(stranded on the dropped lane is the pre-fix behavior)", visited)
	}
	want := []string{"A2", "A1", "A0", "B0"}
	if len(visited) != len(want) {
		t.Fatalf("lanes visited = %v, want %v", visited, want)
	}
	for i := range want {
		if visited[i] != want[i] {
			t.Fatalf("lanes visited = %v, want %v", visited, want)
		}
	}
	if e.Stats.LaneChanges != 2 {
		t.Errorf("lanechanges = %d, want exactly 2 (one per depth step, no drift)", e.Stats.LaneChanges)
	}
	if e.Stats.WallHits != 0 {
		t.Errorf("wallhits = %d, want 0 — the vehicle must never reach A2's end wall", e.Stats.WallHits)
	}
	// B0 is an exit, so the trip ends through the unchanged exit path.
	if e.Stats.Despawned != 1 || e.Stats.Arrived != 0 {
		t.Errorf("despawned=%d arrived=%d, want 1 and 0 (an exit destination is an exit despawn)",
			e.Stats.Despawned, e.Stats.Arrived)
	}
}

// TestRouteHopVetoUsesGradient: the guardrail denies a hop that INCREASES
// lateral depth and permits one that does not — including a hop between two
// lanes that are equally far off-route, which the old leave-the-depth-0-set
// rule had nothing to say about.
func TestRouteHopVetoUsesGradient(t *testing.T) {
	e, err := NewEngine(destSpec(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	v := &Vehicle{ID: 999, Type: &Car, Route: "B0"}
	for _, tc := range []struct {
		lane string
		dir  int
		want bool
		why  string
	}{
		{"A0", 1, false, "A0 (depth 0) → A1 (depth 1) leaves the route"},
		{"A1", -1, true, "A1 (depth 1) → A0 (depth 0) descends"},
		{"A1", 1, false, "A1 (depth 1) → A2 (depth 2) climbs"},
		{"A2", -1, true, "A2 (depth 2) → A1 (depth 1) descends"},
		{"A0", -1, true, "no right neighbour: execLaneChange no-ops anyway"},
	} {
		v.Lane = e.Net.LaneByID(tc.lane)
		if got := e.routeHopOK(v, tc.dir); got != tc.want {
			t.Errorf("routeHopOK(%s, %+d) = %v, want %v (%s)", tc.lane, tc.dir, got, tc.want, tc.why)
		}
	}
	// An unrouted vehicle is never vetoed — the pre-ADR-0021 path, which the
	// CRC-pinned fixtures depend on being untouched.
	u := &Vehicle{ID: 1000, Type: &Car, Lane: e.Net.LaneByID("A0")}
	if !e.routeHopOK(u, 1) {
		t.Error("an unrouted vehicle was vetoed")
	}
}
