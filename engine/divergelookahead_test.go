package engine

import "testing"

// divergelookahead_test.go — car-following must look down the branch the
// vehicle is ROUTED to, not Successors[0] (ADR-0032).
//
// leaderAt walked cur.Successors[0] past an empty lane. A vehicle approaching
// a diverge therefore watched the queue on a road it was not taking: on
// chi-loop-urban one such vehicle read a leader 831 m away at 17 m/s down the
// default branch while its own branch held a standing queue 19 m past the
// junction, and its lookahead collapsed 831 m → 19.4 m in the single tick it
// crossed. Stopping from 20 m/s needs 22.2 m. It ended 2.83 m inside the
// queue tail and stayed there 44 s — 55% of that run's collision
// observations, from one pair of cars.

// divergeNetFile is one approach splitting into two branches. DEFAULT
// (Successors[0], via iJ_0) runs to a long empty exit; ROUTED (via iJ_1) runs
// to a short lane where the queue is parked. A vehicle routed to nR_0 must
// brake for the queue on nR_0, which it can only do by looking down its own
// branch.
//
// The approach is 300 m so a vehicle crossing at the 13.89 m/s limit has
// ample room to stop IF it sees the queue: the test distinguishes "saw it
// early and stopped" from "discovered it at the boundary", not marginal
// braking physics.
func divergeNetFile() *NetFile {
	return &NetFile{
		Version: 1,
		Name:    "diverge-lookahead",
		Lanes: []NetLane{
			{ID: "nA_0", Section: "A", Length: 300, SpeedLimit: 13.89,
				Successors: []string{"iJ_0", "iJ_1"}},
			// Default branch: empty all the way out.
			{ID: "iJ_0", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true,
				Junction: "J", Row: "major", Successors: []string{"nD_0"}},
			{ID: "nD_0", Section: "D", Length: 400, SpeedLimit: 13.89, Exit: true},
			// Routed branch: holds the standing queue. EndWall, so the queue
			// STAYS standing — on an exit lane IDM accelerates the queue away
			// and there is nothing left to brake for by the time the ego
			// arrives.
			{ID: "iJ_1", Section: "j:J", Length: 10, SpeedLimit: 13.89, Internal: true,
				Junction: "J", Row: "major", Successors: []string{"nR_0"}},
			{ID: "nR_0", Section: "R", Length: 60, SpeedLimit: 13.89, EndWall: true},
		},
	}
}

// A vehicle routed onto the queued branch brakes for that queue and never
// overlaps it. Pre-fix it followed the empty default branch, arrived at the
// limit, and drove into the tail.
func TestLeaderLookaheadFollowsTheRoutedBranch(t *testing.T) {
	e := newFileEngine(t, divergeNetFile(), 600)
	a := laneOf(t, e, "nA_0")
	r := laneOf(t, e, "nR_0")

	// A standing queue against the wall at the end of the routed branch,
	// leaving ~40 m of room — enough that the junction ENTRY gate
	// (exitBlocked) admits the ego, so what is under test is whether it
	// arrives at a speed it can stop from, not whether it is let in.
	for i := 0; i < 3; i++ {
		e.AddInitialVehicle(r, 0, 58-float64(i)*7, 0, 1)
	}
	// The ego, at the limit, 300 m of approach, routed to the queued branch.
	ego := e.AddInitialVehicle(a, 0, 5, 13.89, 1)
	ego.Route = "nR_0"

	// Its lookahead must resolve the queue while it is still on the
	// approach — that is the fix, stated directly.
	sawQueueOnApproach := false
	for e.Tick < 600 {
		e.Step()
		assertNoNaN(t, e)
		if ego.Lane == a {
			if l := e.leader(ego); l.OK && l.V < 1 && l.Gap < 200 {
				sawQueueOnApproach = true
			}
		}
		if e.Stats.Collisions != 0 {
			t.Fatalf("tick %d: %d collision observations by section %v; ego lane=%s s=%.2f v=%.2f",
				e.Tick, e.Stats.Collisions, e.Stats.CollisionsBySection,
				ego.Lane.ID, ego.S, ego.V)
		}
	}
	if !sawQueueOnApproach {
		t.Error("ego never resolved the standing queue on its own branch while on the approach")
	}
	if ego.Lane != nil && ego.Lane.ID == "nD_0" {
		t.Fatalf("ego took the default branch, not its route: the fixture is wrong")
	}
}

// The default branch is still the answer with no route and no held turn —
// pickSuccessor's fallback — so single-successor networks and unrouted
// vehicles are untouched.
func TestLeaderLookaheadDefaultsWithoutRoute(t *testing.T) {
	e := newFileEngine(t, divergeNetFile(), 10)
	a := laneOf(t, e, "nA_0")
	d := laneOf(t, e, "nD_0")

	// A vehicle parked on the DEFAULT branch, none on the routed one.
	e.AddInitialVehicle(d, 0, 10, 0, 1)
	// Unrouted ego near the end of the approach.
	ego := e.AddInitialVehicle(a, 0, 295, 5, 1)

	lead := e.leader(ego)
	if !lead.OK {
		t.Fatal("unrouted ego resolved no leader; the default branch holds one")
	}
	// 5 m of approach left + 10 m of box + 10 m to the parked car's rear.
	if lead.Gap < 20 || lead.Gap > 30 {
		t.Errorf("gap %.2f m through the default branch, want ~25 m", lead.Gap)
	}
}

// A held turn steers the FIRST hop and is spent there (boundaries() consumes
// it on the crossing), so the walk beyond it follows route/default. HeldTurn
// < 0 takes the last successor: the routed branch here.
func TestLeaderLookaheadHonoursHeldTurn(t *testing.T) {
	e := newFileEngine(t, divergeNetFile(), 10)
	a := laneOf(t, e, "nA_0")
	r := laneOf(t, e, "nR_0")

	e.AddInitialVehicle(r, 0, 10, 0, 1)
	ego := e.AddInitialVehicle(a, 0, 295, 5, 1)
	ego.HeldTurn = -1 // right: the last successor, iJ_1

	lead := e.leader(ego)
	if !lead.OK {
		t.Fatal("held-turn ego resolved no leader down the branch it is turning into")
	}
	if lead.Gap < 20 || lead.Gap > 30 {
		t.Errorf("gap %.2f m through the held-turn branch, want ~25 m", lead.Gap)
	}
}
