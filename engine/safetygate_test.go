package engine

import "testing"

// safetySpec builds a straight-lane run with the holdlast policy — the live
// configuration, where the kernel does NO driving and a vehicle without an
// applied intent coasts.
func safetySpec(t *testing.T, decel float64) RunSpec {
	t.Helper()
	spec, err := DefaultSpec("straight", 2000, 1)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.Types = []*VehicleType{&Car, &Truck}
	spec.Scen.SpawnRatePerLaneHour = 0 // no spawner: this is a two-car fixture
	spec.Scen.UncontrolledPolicy = PolicyHoldLast
	spec.Params.SafetyDecel = decel
	return spec
}

// twoCarApproach places a stopped leader ahead of a fast follower on the
// same lane and steps until either they overlap or the follower stops.
// Returns the closing pair's final gap and the run's collision count.
func twoCarApproach(t *testing.T, decel float64) (gap float64, collisions int, clamped int) {
	t.Helper()
	e, err := NewEngine(safetySpec(t, decel))
	if err != nil {
		t.Fatal(err)
	}
	lane := e.Net.Lanes[0]
	if lane.Length < 300 {
		t.Fatalf("fixture lane too short for the approach: %.0f m", lane.Length)
	}
	lead := e.AddInitialVehicle(lane, 0, 250, 0, 1)    // stopped, 250 m in
	follow := e.AddInitialVehicle(lane, 0, 100, 25, 1) // 25 m/s, 150 m back
	for i := 0; i < 400; i++ {
		e.Step()
		if lead.Lane == nil || follow.Lane == nil {
			break
		}
	}
	if lead.Lane == nil || follow.Lane == nil {
		t.Fatal("a fixture vehicle left the lane — the approach never resolved")
	}
	return lead.S - lead.Type.Length - follow.S, e.Stats.Collisions, e.SafetyOverlapped
}

// TestCoastingRunsIntoStoppedTraffic is the bug, stated as a test. With the
// holdlast policy and no controller attached, computeAccels' default branch
// sets Acc = 0 — a coast with NO car-following term — so a vehicle holds
// its speed straight through whatever is stopped ahead and stays overlapped
// for the rest of the run.
//
// This is not a hypothetical: on chi-loop-urban it produced 20,222,389
// collision observations (≈1,120 interpenetrating pairs at every tick) and
// was indistinguishable from congestion in every metric the run reported.
func TestCoastingRunsIntoStoppedTraffic(t *testing.T) {
	gap, collisions, _ := twoCarApproach(t, 0) // gate disabled
	if gap > 0 {
		t.Fatalf("coasting follower stopped itself (gap %.2f m) — the fixture "+
			"no longer reproduces the uncontrolled-coast failure", gap)
	}
	if collisions == 0 {
		t.Fatal("overlap produced no collision observations — updateStats is not measuring this pair")
	}
	t.Logf("without the gate: final gap %.2f m, %d collision observations", gap, collisions)
}

// TestSafetyGateStopsTheCoaster pins the fix. The gate caps every control
// path, so the SAME uncontrolled vehicle now brakes and stops short.
func TestSafetyGateStopsTheCoaster(t *testing.T) {
	gap, collisions, clamped := twoCarApproach(t, 6)
	if gap < 0 {
		t.Fatalf("safety gate did not prevent the overlap: final gap %.2f m", gap)
	}
	if collisions != 0 {
		t.Errorf("safety gate held the gap but %d collision observations were still booked", collisions)
	}
	if clamped != 0 {
		t.Errorf("gate saw an overlapped pair %d times — with 150 m of room it should never have been late", clamped)
	}
	t.Logf("with the gate: final gap %.2f m", gap)
}

// TestSafetyGateIsDisabledByDefault fences the compatibility promise: the
// gate changes accelerations, so it must not switch itself on and silently
// rewrite recorded CRCs. Live runs opt in.
func TestSafetyGateIsDisabledByDefault(t *testing.T) {
	if d := DefaultParams().SafetyDecel; d != 0 {
		t.Fatalf("DefaultParams().SafetyDecel = %v, want 0 — enabling the gate by "+
			"default changes every existing recording's CRC", d)
	}
}

// TestSafetyGateDoesNotShapeFreeFlow is the "guardrail, not a car-following
// model" property. A gate that binds in ordinary driving would quietly
// become the longitudinal model and mask whatever the controller actually
// asked for, so it must stay slack in free flow and in IDM equilibrium.
func TestSafetyGateDoesNotShapeFreeFlow(t *testing.T) {
	e, err := NewEngine(safetySpec(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	lane := e.Net.Lanes[0]
	// Equilibrium following: gap = s0 + v*T, both at the same speed. This
	// is the tightest spacing IDM ever settles at, so if the gate is slack
	// here it is slack everywhere a working controller operates.
	v := 25.0
	gap := Car.S0 + v*Car.T
	lead := e.AddInitialVehicle(lane, 0, 100+gap+Car.Length, v, 1)
	follow := e.AddInitialVehicle(lane, 0, 100, v, 1)
	a, ok := e.safetyGate(follow)
	if !ok {
		t.Fatal("gate did not resolve a leader for an adjacent pair")
	}
	if a <= 0 {
		t.Errorf("gate caps IDM equilibrium following at %.2f m/s² — it is acting "+
			"as the car-following model, not as a guardrail", a)
	}
	if lead.Lane == nil {
		t.Fatal("fixture leader left the lane")
	}
}

// TestSafetyGateClampsAtSafetyDecel pins that the gate never invents braking
// the vehicle does not have. Asking for more than SafetyDecel is exactly the
// case where a collision may be unavoidable, and the honest response is to
// brake as hard as physics allows and let the overlap be counted — not to
// teleport the vehicle to a safe speed.
func TestSafetyGateClampsAtSafetyDecel(t *testing.T) {
	e, err := NewEngine(safetySpec(t, 6))
	if err != nil {
		t.Fatal(err)
	}
	lane := e.Net.Lanes[0]
	// 30 m/s into a stopped car 2 m ahead: stopping needs 75 m at 6 m/s².
	e.AddInitialVehicle(lane, 0, 100+2+Car.Length, 0, 1)
	follow := e.AddInitialVehicle(lane, 0, 100, 30, 1)
	a, ok := e.safetyGate(follow)
	if !ok {
		t.Fatal("gate did not resolve a leader")
	}
	if a != -e.Params.SafetyDecel {
		t.Errorf("gate returned %.2f m/s², want the clamp at %.2f", a, -e.Params.SafetyDecel)
	}
	if e.SafetyOverlapped != 0 {
		t.Errorf("SafetyOverlapped = %d on a still-positive gap — the counter must "+
			"mean 'the pair is already overlapped', not 'braking hard'", e.SafetyOverlapped)
	}
}
