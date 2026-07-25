package driver

import (
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// driver_test.go — the per-tick routing budget (Config.RouteBudgetPerTick,
// driver.go routeStep): an observation carrying more routable vehicles
// than the budget resolves only N destinations; the rest stay COMPLETELY
// untouched (no wantRoute entry, no routed mark) and are resolved by
// subsequent observations. Runs against an in-process NATS server so the
// real onObs (including intent publish) is exercised.

func startTestBroker(t *testing.T) (*server.Server, *nats.Conn) {
	t.Helper()
	ns, err := server.NewServer(&server.Options{DontListen: true})
	if err != nil {
		t.Fatalf("nats-server NewServer: %v", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server not ready")
	}
	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns))
	if err != nil {
		t.Fatalf("in-process connect: %v", err)
	}
	t.Cleanup(func() {
		nc.Close()
		ns.Shutdown()
	})
	return ns, nc
}

// intentCounter tallies RouteSet intents on the driver's intent subject.
type intentCounter struct {
	mu       sync.Mutex
	routeSet int
	total    int
}

func (c *intentCounter) subscribe(t *testing.T, nc *nats.Conn, run, id string) {
	t.Helper()
	_, err := nc.Subscribe(natsio.SubjectCtlIntent(run, id), func(m *nats.Msg) {
		in, ok := natsio.DecodeIntent(m.Data)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.total++
		if ok && in.RouteSet {
			c.routeSet++
		}
	})
	if err != nil {
		t.Fatalf("subscribe intents: %v", err)
	}
}

func (c *intentCounter) waitRouteSet(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		got := c.routeSet
		c.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("RouteSet intents = %d, want %d", c.routeSet, want)
}

// A vehicle that MOVES between first observation and budget admission
// must draw from the FROZEN first-observation lane, not its current one
// — that is what keeps the draw a pure function of (seed, id) rather
// than controller timing (ADR-0007/0008 failover, sol review).
func TestRouteBudgetFrozenLane(t *testing.T) {
	_, nc := startTestBroker(t)
	net := loadStopControl(t)

	originIdx := -1
	for _, o := range net.Origins {
		if _, ok := pickExit(net, newExitCache(), 42, 7, o.Index); ok {
			originIdx = o.Index
			break
		}
	}
	if originIdx < 0 {
		t.Fatal("no fixture origin routes vehicle 7")
	}

	d := &Driver{
		nc: nc,
		cfg: Config{
			Run:                "t",
			ExitRouting:        true,
			RouteBudgetPerTick: 1,
		}.withDefaults(),
		id:        "drv",
		spec:      engine.RunSpec{Seed: 42},
		types:     []*engine.VehicleType{&engine.Car},
		net:       net,
		fleet:     map[uint64]bool{},
		pending:   map[uint64]bool{},
		routed:    map[uint64]bool{},
		streams:   map[uint64]*engine.Stream{},
		wantRoute: map[uint64]string{},
		wantLane:  map[uint64]int{},
		done:      make(chan struct{}),
	}

	// Tick 1: two egos on the origin lane in frame slice order; budget 1
	// admits the first slice entry (6), vehicle 7 is deferred.
	egos := []natsio.ObsEgo{{ID: 6, LaneIdx: originIdx}, {ID: 7, LaneIdx: originIdx}}
	d.onObs(&nats.Msg{Data: natsio.EncodeObs(1, 0, egos, nil)})
	if _, ok := d.wantRoute[7]; ok {
		t.Fatal("vehicle 7 routed in tick 1 past the budget of 1")
	}

	// Tick 2: vehicle 7 has MOVED (different lane) — the draw must still
	// use the tick-1 lane.
	moved := (originIdx + 1) % len(net.Lanes)
	egos = []natsio.ObsEgo{{ID: 6, LaneIdx: originIdx}, {ID: 7, LaneIdx: moved}}
	d.onObs(&nats.Msg{Data: natsio.EncodeObs(2, 0, egos, nil)})
	want, ok := pickExit(net, newExitCache(), 42, 7, originIdx)
	if !ok {
		t.Fatal("pickExit from the frozen lane fails")
	}
	if got := d.wantRoute[7]; got != want {
		t.Fatalf("deferred draw = %q, want %q (frozen tick-1 lane, not the moved lane)", got, want)
	}
}

func TestRouteBudgetPerTick(t *testing.T) {
	_, nc := startTestBroker(t)
	net := loadStopControl(t) // destinations_test.go fixture

	// An origin where every test vehicle draws a reachable exit (per
	// TestPickExitRouteFound, fixture origins are all-or-nothing).
	originIdx := -1
	for _, o := range net.Origins {
		okAll := true
		for vid := uint64(1); vid <= 5; vid++ {
			if _, ok := pickExit(net, newExitCache(), 42, vid, o.Index); !ok {
				okAll = false
				break
			}
		}
		if okAll {
			originIdx = o.Index
			break
		}
	}
	if originIdx < 0 {
		t.Fatal("no fixture origin routes all five test vehicles")
	}

	d := &Driver{
		nc: nc,
		cfg: Config{
			Run:                "t",
			ExitRouting:        true,
			RouteBudgetPerTick: 2,
		}.withDefaults(),
		id:        "drv",
		spec:      engine.RunSpec{Seed: 42},
		types:     []*engine.VehicleType{&engine.Car},
		net:       net,
		fleet:     map[uint64]bool{},
		pending:   map[uint64]bool{},
		routed:    map[uint64]bool{},
		streams:   map[uint64]*engine.Stream{},
		wantRoute: map[uint64]string{},
		done:      make(chan struct{}),
	}
	var counter intentCounter
	counter.subscribe(t, nc, "t", "drv")

	egos := make([]natsio.ObsEgo, 5)
	for i := range egos {
		egos[i] = natsio.ObsEgo{ID: uint64(i + 1), LaneIdx: originIdx}
	}
	obs := func(tick uint64) *nats.Msg {
		return &nats.Msg{Data: natsio.EncodeObs(tick, 0, egos, nil)}
	}

	// Tick 1: budget 2 → exactly two destinations resolved and sent; the
	// other three egos carry no route state (only their first-observation
	// lane in wantLane — the frozen draw input). (RouteSet intent counts
	// include the unconfirmed re-sends: an unresolved-but-sent destination
	// is re-published every obs until the echo confirms — unmetered, cheap
	// string copies — so cumulative counts below are 2, 2+2·2, 6+5.)
	d.onObs(obs(1))
	counter.waitRouteSet(t, 2)
	if got := len(d.wantRoute); got != 2 {
		t.Fatalf("after tick 1: %d wantRoute entries, want 2 (over-budget egos must stay untouched)", got)
	}
	if got := len(d.routed); got != 0 {
		t.Fatalf("after tick 1: %d routed marks, want 0 (nothing echoed yet)", got)
	}

	// Tick 2: two more resolved (2 re-sends + 2 new = 4 this tick, 6
	// total); tick 3: the last one (2+2 re-sends + 1 new = 5, 11 total).
	d.onObs(obs(2))
	counter.waitRouteSet(t, 6)
	if got := len(d.wantRoute); got != 4 {
		t.Fatalf("after tick 2: %d wantRoute entries, want 4", got)
	}
	d.onObs(obs(3))
	counter.waitRouteSet(t, 11)
	if got := len(d.wantRoute); got != 5 {
		t.Fatalf("after tick 3: %d wantRoute entries, want 5", got)
	}

	// Tick 4: the obs echoes the assigned destinations (engine applied
	// them) — every vehicle flips to routed and no new Route intent goes
	// out.
	for i := range egos {
		egos[i].Route = d.wantRoute[egos[i].ID]
	}
	d.onObs(obs(4))
	if got := len(d.routed); got != 5 {
		t.Fatalf("after echo: %d routed, want 5", got)
	}
	if got := len(d.wantRoute); got != 0 {
		t.Fatalf("after echo: %d wantRoute entries, want 0", got)
	}
	// The intent counter is written on a subscriber goroutine: the count
	// must hold STEADY at 11 through a quiet interval before we declare
	// no extra RouteSet was sent — a single lucky read false-passes (sol
	// review).
	stable := 0
	final := -1
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		counter.mu.Lock()
		final = counter.routeSet
		counter.mu.Unlock()
		if final > 11 {
			t.Fatalf("echo tick sent %d extra RouteSet intents, want 0", final-11)
		}
		if final == 11 {
			stable++
			if stable >= 10 { // ~100 ms quiet
				break
			}
		} else {
			stable = 0
		}
		time.Sleep(10 * time.Millisecond)
	}
	if final != 11 {
		t.Fatalf("routeSet counter = %d, want 11 (initial sends lost?)", final)
	}
}
