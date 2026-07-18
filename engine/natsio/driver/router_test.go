package driver

import (
	"testing"

	"traffic-sim/engine"
)

// The Dijkstra router over the lane successor graph: single-path chains,
// multi-hop chains, unreachable destinations (dead lanes), and the trivial
// same-lane route. (M1 networks are all single-successor chains; the
// shortest-path choice among diamonds is exercised by construction when
// junction networks land — the turn axis' consumer.)
func TestRouteDijkstra(t *testing.T) {
	net, err := engine.BuildNet(engine.NetSpec{Kind: "lanedrop"})
	if err != nil {
		t.Fatal(err)
	}
	idx := func(id string) int { return net.LaneByID(id).Index }

	path, ok := Route(net, idx("A0"), "B0")
	if !ok || len(path) != 2 || path[0] != idx("A0") || path[1] != idx("B0") {
		t.Fatalf("A0→B0 = %v (ok %v)", path, ok)
	}
	path, ok = Route(net, idx("A0"), "A0")
	if !ok || len(path) != 1 || path[0] != idx("A0") {
		t.Fatalf("A0→A0 = %v (ok %v)", path, ok)
	}
	if _, ok := Route(net, idx("A2"), "B0"); ok {
		t.Fatal("A2 (dead lane) → B0 routed")
	}
	if _, ok := Route(net, idx("A0"), "B1"); ok {
		t.Fatal("A0 → B1 routed through the wrong successor")
	}
	if _, ok := Route(net, idx("A0"), "ZZ"); ok {
		t.Fatal("unknown destination routed")
	}

	// Multi-hop on the I-80 chain: U1→A1→B1→D1.
	i80, err := engine.BuildNet(engine.NetSpec{Kind: "i80"})
	if err != nil {
		t.Fatal(err)
	}
	iidx := func(id string) int { return i80.LaneByID(id).Index }
	path, ok = Route(i80, iidx("U1"), "D1")
	if !ok || len(path) != 4 ||
		path[0] != iidx("U1") || path[1] != iidx("A1") || path[2] != iidx("B1") || path[3] != iidx("D1") {
		t.Fatalf("U1→D1 = %v (ok %v)", path, ok)
	}
	if _, ok := Route(i80, iidx("AR"), "D0"); ok {
		t.Fatal("ramp (dead lane) → D0 routed")
	}
}
