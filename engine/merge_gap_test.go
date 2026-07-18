package engine

import "testing"

// TestLaneDropOverloadNoOverlap is the regression test for the M2 merge
// gap-enforcement defect: under sustained overload (3×1500 veh/h into a
// 3→2 lane drop, 600 s) the instant-hop merge model produced multi-metre
// negative gaps (M2: min −11.8 m at the I-80 merges; 28 observations in this
// scenario at seed 1, min −4.9 m) — urgency-relaxed b_safe admitted gaps the
// follower could not hold, and hops near lane boundaries never checked
// cross-boundary pairs at all. With the kinematic collision-freedom floor
// (kinGapOK) and boundary-aware neighbor resolution the counter must stay at
// ZERO beyond the −0.01 m epsilon (engine.go collisionGap).
func TestLaneDropOverloadNoOverlap(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 6000, 1)
	e, _, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	assertNoNaN(t, e)
	if e.Stats.Collisions != 0 {
		t.Errorf("merge gap enforcement regressed: %d collision observations (min gap %.3f m), want 0",
			e.Stats.Collisions, e.Stats.MinGap)
	}
	if e.Stats.WallHits != 0 {
		t.Errorf("dropped-lane wall clamped %d vehicles (merge model pathology)", e.Stats.WallHits)
	}
	t.Logf("lanedrop overload: collisions=0 mingap=%.3f m spawned=%d lanechanges=%d",
		e.Stats.MinGap, e.Stats.Spawned, e.Stats.LaneChanges)
}

// kinGapOK is the collision-freedom floor: gap ≥ (v_f²−v_l²)/(2·b_max) plus
// one tick of closing when the follower is faster; no constraint otherwise.
func TestKinGapOK(t *testing.T) {
	const dt = 0.1
	cases := []struct {
		gap, vFoll, vLead float64
		want              bool
	}{
		{0.0, 5, 5, true},      // not closing: static minGap floor suffices
		{0.0, 0, 8, true},      // stopped behind a moving leader
		{5.0, 10, 0, false},    // need 100/18 + 1.0 ≈ 6.6 m behind a stopped leader
		{7.0, 10, 0, true},     //
		{0.5, 10, 9.9, true},   // barely closing: need (100−98.01)/18 + 0.01 ≈ 0.12 m
		{0.05, 10, 9.9, false}, //
		{3.0, 33.3, 29, false}, // highway cut-in: need (1109−841)/18 + 0.43 ≈ 15.3 m
		{16.0, 33.3, 29, true}, //
	}
	for _, c := range cases {
		if got := kinGapOK(c.gap, c.vFoll, c.vLead, dt); got != c.want {
			t.Errorf("kinGapOK(gap=%.2f, vFoll=%.1f, vLead=%.1f) = %v, want %v",
				c.gap, c.vFoll, c.vLead, got, c.want)
		}
	}
}
