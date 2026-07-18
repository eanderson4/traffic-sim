package engine

import "testing"

// Ring: 22 vehicles on the 230 m Sugiyama ring must stay collision-free (no
// negative gaps) and keep moving for 3000 ticks at the car defaults. With
// the instability-capable M3 calibration (a = 0.73) the uniform start is an
// exact fixed point — identical vehicles, identical gaps, no noise source on
// a single lane — so it does not drift into a jam; the phantom-jam
// acceptance test injects the perturbation that the fixed point lacks and
// verifies the instability this base case is stable against
// (sugiyama_test.go). Mean speed at the fixed point is the IDM equilibrium
// speed for the 5.45 m gap (≈ 2.2 m/s at the new parameters).
func TestRingStable(t *testing.T) {
	spec, _ := DefaultSpec("ring", 3000, 1)
	e, _, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertNoNaN(t, e)
	if e.Stats.Collisions != 0 {
		t.Errorf("ring: %d collision observations", e.Stats.Collisions)
	}
	if e.Stats.MinGap <= 0 {
		t.Errorf("ring: negative gap over 3000 ticks: min %.4f m", e.Stats.MinGap)
	}
	if n := len(e.Vehicles()); n != 22 {
		t.Errorf("ring: %d vehicles at end, want 22 (closed system)", n)
	}
	// Traffic must keep moving (no gridlock on a stable ring).
	var mean float64
	for _, v := range e.Vehicles() {
		mean += v.V
	}
	mean /= float64(len(e.Vehicles()))
	if mean < 0.5 {
		t.Errorf("ring: mean speed %.3f m/s — traffic seized up", mean)
	}
	t.Logf("ring: min gap %.3f m, final mean speed %.3f m/s", e.Stats.MinGap, mean)
}

// Lane drop: with 3×1500 veh/h demand into a 2-lane bottleneck, density
// builds upstream of the drop — mean speed on section A must end clearly
// below mean speed on section B after 600 ticks (60 s).
func TestLaneDropCongestion(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 600, 1)
	e, _, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertNoNaN(t, e)
	var upSum, dnSum float64
	var upN, dnN int
	for _, v := range e.Vehicles() {
		switch v.Lane.Section {
		case "A":
			upSum += v.V
			upN++
		case "B":
			dnSum += v.V
			dnN++
		}
	}
	if upN < 20 || dnN < 5 {
		t.Fatalf("too few vehicles for a meaningful comparison: upstream %d, downstream %d", upN, dnN)
	}
	up, dn := upSum/float64(upN), dnSum/float64(dnN)
	if up >= dn {
		t.Errorf("no bottleneck signature: upstream mean %.2f m/s >= downstream %.2f m/s", up, dn)
	}
	if up >= 0.8*dn {
		t.Errorf("weak bottleneck signature: upstream %.2f vs downstream %.2f m/s (want upstream < 0.8·downstream)", up, dn)
	}
	t.Logf("lanedrop: upstream %.2f m/s (%d veh), downstream %.2f m/s (%d veh), spawned %d, lane changes %d, min gap %.3f",
		up, upN, dn, dnN, e.Stats.Spawned, e.Stats.LaneChanges, e.Stats.MinGap)
}
