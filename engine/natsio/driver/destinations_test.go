package driver

import (
	"testing"

	"traffic-sim/engine"
)

// The pinned stop-control fixture (engine/testdata/stop-control) is a real
// multi-junction grid with 30+ exit lanes above the 30 m fragment cutoff
// and speed limits from 5.56 to 22.22 m/s — enough exits and enough weight
// spread to exercise the weighted draw for real.
const fixtureStopControlNet = "../../testdata/stop-control/network.json"

func loadStopControl(t *testing.T) *engine.Network {
	t.Helper()
	net, err := engine.LoadNetFile(fixtureStopControlNet)
	if err != nil {
		t.Fatal(err)
	}
	return net
}

// exitCandidates: only Exit lanes at or above the fragment cutoff, sorted
// by ID (the fixed order the weighted draw walks). The fixture has exits
// both below and above the cutoff, so the exclusion is exercised for real.
func TestExitCandidates(t *testing.T) {
	net := loadStopControl(t)
	cands := exitCandidates(net)
	if len(cands) == 0 {
		t.Fatal("no candidates")
	}
	short := 0
	for _, l := range net.Lanes {
		if l.Exit && l.Length < minExitLen {
			short++
		}
	}
	if short == 0 {
		t.Fatal("fixture has no sub-cutoff exits — the exclusion is untested")
	}
	for i, l := range cands {
		if !l.Exit || l.Length < minExitLen {
			t.Errorf("candidate %s: exit %v length %.2f", l.ID, l.Exit, l.Length)
		}
		if i > 0 && cands[i-1].ID >= l.ID {
			t.Errorf("candidates not sorted by ID: %s before %s", cands[i-1].ID, l.ID)
		}
	}
}

// The destination draw is a pure function of (run seed, vehicle ID,
// network, current lane): two independent evaluations — what a failover
// replica re-deriving the pick amounts to — agree exactly, and different
// vehicles spread across different exits.
func TestPickExitDeterministic(t *testing.T) {
	net := loadStopControl(t)
	const seed = 42
	from := net.Origins[0].Index
	for vid := uint64(1); vid <= 50; vid++ {
		a, okA := pickExit(net, seed, vid, from)
		b, okB := pickExit(net, seed, vid, from)
		if okA != okB || a != b {
			t.Fatalf("vehicle %d: pick %q (ok %v) != re-pick %q (ok %v)", vid, a, okA, b, okB)
		}
	}
	// Variety: 200 vehicles from the same origin must not all pile onto
	// one exit, and every pick must be an eligible candidate.
	eligible := map[string]bool{}
	for _, l := range exitCandidates(net) {
		eligible[l.ID] = true
	}
	distinct := map[string]int{}
	for vid := uint64(1); vid <= 200; vid++ {
		dest, ok := pickExit(net, seed, vid, from)
		if !ok {
			t.Fatalf("vehicle %d: no destination from origin %s", vid, net.Lanes[from].ID)
		}
		if !eligible[dest] {
			t.Fatalf("vehicle %d: %q is not an eligible exit", vid, dest)
		}
		distinct[dest]++
	}
	if len(distinct) < 2 {
		t.Fatalf("200 vehicles drew only %d distinct destinations: %v", len(distinct), distinct)
	}
}

// Route-found rate: from every spawn origin on the fixture, the large
// majority of vehicles get an assigned exit, and every assigned exit is
// genuinely reachable from that origin. Per origin the outcome must be
// exact: origins that CAN reach an eligible exit route every vehicle;
// origins that reach only sub-cutoff fragment exits get no pick (the
// kernel default applies) rather than a clipped destination.
func TestPickExitRouteFound(t *testing.T) {
	net := loadStopControl(t)
	const seed = 7
	total, found := 0, 0
	for _, origin := range net.Origins {
		reachableEligible := 0
		for _, l := range exitCandidates(net) {
			if l.ID != origin.ID && reachableLanes(net, origin.Index)[l.Index] {
				reachableEligible++
			}
		}
		reachable := 0
		for vid := uint64(1); vid <= 40; vid++ {
			total++
			dest, ok := pickExit(net, seed, vid, origin.Index)
			if !ok {
				continue
			}
			if _, ok := Route(net, origin.Index, dest); !ok {
				t.Fatalf("origin %s vehicle %d: assigned unreachable exit %s",
					origin.ID, vid, dest)
			}
			found++
			reachable++
		}
		t.Logf("origin %s: %d/40 routed (%d eligible exits reachable)", origin.ID, reachable, reachableEligible)
		if reachableEligible > 0 && reachable != 40 {
			t.Errorf("origin %s reaches %d eligible exits but routed only %d/40",
				origin.ID, reachableEligible, reachable)
		}
		if reachableEligible == 0 && reachable != 0 {
			t.Errorf("origin %s reaches no eligible exit but routed %d/40", origin.ID, reachable)
		}
	}
	if rate := float64(found) / float64(total); rate < 0.8 {
		t.Fatalf("route-found rate %.2f < 0.80 (%d/%d)", rate, found, total)
	}
}

// The reroll conditions: a pick from a lane must never be that lane
// itself (rerolled or no pick), and sub-cutoff fragment exits must never
// be drawn no matter where the vehicle stands.
func TestPickExitRerollConditions(t *testing.T) {
	net := loadStopControl(t)
	const seed = 99
	eligible := map[string]bool{}
	for _, l := range exitCandidates(net) {
		eligible[l.ID] = true
	}
	// From each eligible exit lane as the current lane: the draw must
	// reroll away from self (or report no pick).
	for _, l := range exitCandidates(net) {
		for vid := uint64(1); vid <= 20; vid++ {
			if dest, ok := pickExit(net, seed, vid, l.Index); ok && dest == l.ID {
				t.Fatalf("vehicle %d on %s drew its own lane", vid, l.ID)
			}
		}
	}
	// From every origin, every draw lands in the eligible set — fragment
	// exits (some of which out-rank real exits on speed limit) never win.
	for _, origin := range net.Origins {
		for vid := uint64(1); vid <= 40; vid++ {
			if dest, ok := pickExit(net, seed, vid, origin.Index); ok && !eligible[dest] {
				t.Fatalf("origin %s vehicle %d: %q is not an eligible exit", origin.ID, vid, dest)
			}
		}
	}
}
