package engine

import (
	"math"
	"testing"
)

func assertNoNaN(t *testing.T, e *Engine) {
	t.Helper()
	for _, v := range e.Vehicles() {
		if math.IsNaN(v.S) || math.IsNaN(v.V) || math.IsInf(v.S, 0) || math.IsInf(v.V, 0) {
			t.Fatalf("tick %d: vehicle %d non-finite state s=%v v=%v", e.Tick, v.ID, v.S, v.V)
		}
		if v.V < 0 {
			t.Fatalf("tick %d: vehicle %d negative speed %v", e.Tick, v.ID, v.V)
		}
	}
}

// IDM sanity (1): a lone vehicle on an empty road relaxes to its desired
// speed. Free IDM from rest reaches ~0.98·v0 in ≈70 s at a = 0.73 (≈57 s to
// 0.99·v0 at the old a = 1.0), so 800 ticks (80 s) leaves margin; 5 km of
// lane is enough headroom.
func TestFreeFlowReachesDesiredSpeed(t *testing.T) {
	spec, _ := DefaultSpec("straight", 800, 1)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	e.AddInitialVehicle(e.Net.Lanes[0], 0, 0, 0, 1)
	for e.Tick < spec.Ticks {
		e.Step()
		assertNoNaN(t, e)
	}
	v := e.Vehicles()[0]
	v0 := 33.3
	if v.V < 0.98*v0 || v.V > v0+1e-9 {
		t.Errorf("free-flow speed = %.4f m/s, want within [0.98, 1.0]·%.1f", v.V, v0)
	}
}

// IDM sanity (2): a platoon of cars closing on a slower truck converges to a
// finite following speed (the truck's 22.2 m/s) with strictly positive gaps.
// The approach from 15 m/s stays damped even at the instability-capable
// car calibration (a = 0.73): the unstable window sits at congested
// densities, not on this free-flow approach.
func TestPlatoonConverges(t *testing.T) {
	spec, _ := DefaultSpec("straight", 1200, 1)
	spec.Scen.Types = []*VehicleType{&Car, &Truck}
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	lane := e.Net.Lanes[0]
	e.AddInitialVehicle(lane, 1, 400, 15, 1) // truck leader, V0 = 22.2 m/s
	followers := make([]*Vehicle, 0, 8)
	for i := 0; i < 8; i++ {
		// 10 m bumper-to-bumper gaps, all at 15 m/s
		followers = append(followers, e.AddInitialVehicle(lane, 0, 400-Truck.Length-10-float64(i)*15, 15, 1))
	}
	for e.Tick < spec.Ticks {
		e.Step()
		assertNoNaN(t, e)
	}
	if e.Stats.MinGap <= 0 {
		t.Errorf("platoon produced a non-positive gap: min %.4f m", e.Stats.MinGap)
	}
	want := Truck.V0
	for _, v := range followers {
		if math.Abs(v.V-want) > 0.05*want {
			t.Errorf("follower %d speed %.3f, want ≈%.3f (truck-limited following)", v.ID, v.V, want)
		}
	}
}
