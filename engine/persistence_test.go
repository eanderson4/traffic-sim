package engine

import (
	"math"
	"testing"
)

// persistence_test.go — unit coverage of the ADR-0008 §2 per-axis
// persistence table and the §4 grant tie-break:
//
//	accel            one-shot per tick          (intent_test.go, M3)
//	speed setpoint   persistent until replaced  (here)
//	lane change      one-shot, expires if infeasible (intent_test.go, M3)
//	turn at junction held until consumed        (here)
//	routing          persistent                 (here)
//	signals          persistent state           (here)
//
// plus the uncontrolled-policy flag (idm harness vs holdlast live mode).

// The cruise setpoint is persistent: set once, the servo tracks it across
// ticks with no further intents; a new setpoint replaces it; a negative
// value clears it; a one-shot accel overrides it for exactly one tick.
func TestSpeedSetpointPersistence(t *testing.T) {
	spec, _ := DefaultSpec("straight", 1, 1)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	v := e.AddInitialVehicle(e.Net.Lanes[0], 0, 100, 20, 1)

	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: Intent{VehicleID: v.ID, SpeedSetpoint: 25, SpeedSet: true}})
	e.Step()
	if !v.CruiseOK || v.Cruise != 25 {
		t.Fatalf("cruise = %v (ok %v), want 25", v.Cruise, v.CruiseOK)
	}
	// Servo from 20 toward 25: clamped to Type.A, monotone, no new intents.
	for i := 0; i < 10; i++ {
		e.Step()
	}
	if v.V <= 20 || v.V > 25 {
		t.Fatalf("V after 10 servo ticks = %v, want (20, 25]", v.V)
	}
	for e.Tick < 400 {
		e.Step()
	}
	if math.Abs(v.V-25) > 0.01 {
		t.Fatalf("V did not converge to setpoint: %v", v.V)
	}

	// Replace: persistent until replaced.
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 2, Intent: Intent{VehicleID: v.ID, SpeedSetpoint: 15, SpeedSet: true}})
	e.Step()
	if !v.CruiseOK || v.Cruise != 15 {
		t.Fatalf("replaced cruise = %v (ok %v), want 15", v.Cruise, v.CruiseOK)
	}

	// A one-shot accel overrides the servo for exactly one tick.
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 3, Intent: Intent{VehicleID: v.ID, Accel: -2, AccelSet: true}})
	e.Step()
	if v.Acc != -2 {
		t.Fatalf("one-shot accel = %v, want −2", v.Acc)
	}
	// Expectations for the NEXT tick must be computed pre-step: computeAccels
	// evaluates from tick-entry state.
	wantServo := v.cruiseAccel(e.Params.Dt)
	e.Step()
	if v.reqAccOK {
		t.Fatal("accel override leaked past its tick")
	}
	if v.Acc != wantServo {
		t.Fatalf("post-override accel = %v, want cruise servo %v", v.Acc, wantServo)
	}

	// Clear with a negative setpoint: back to the harness policy (idm).
	wantIDM := e.accelOnLane(v, v.Lane, e.leader(v))
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 4, Intent: Intent{VehicleID: v.ID, SpeedSetpoint: -1, SpeedSet: true}})
	e.Step()
	if v.CruiseOK {
		t.Fatal("cruise not cleared by negative setpoint")
	}
	if v.Acc != wantIDM {
		t.Fatalf("cleared-cruise accel = %v, want IDM %v", v.Acc, wantIDM)
	}

	// The setpoint clamps to the vehicle's desired-speed envelope.
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 5, Intent: Intent{VehicleID: v.ID, SpeedSetpoint: 1000, SpeedSet: true}})
	e.Step()
	if v.Cruise != v.v0eff(v.Lane) {
		t.Fatalf("setpoint clamp = %v, want v0eff %v", v.Cruise, v.v0eff(v.Lane))
	}
}

// forkNet builds a junction: lane F with two exit successors (left-to-right
// order: L then R) — the turn-at-junction test fixture.
func forkNet() *Network {
	f := &Lane{ID: "F", Section: "F", Index: 0, Length: 100, SpeedLimit: 33.3}
	l := &Lane{ID: "L", Section: "L", Index: 1, Length: 100, SpeedLimit: 33.3, Exit: true}
	r := &Lane{ID: "R", Section: "R", Index: 2, Length: 100, SpeedLimit: 33.3, Exit: true}
	f.Successors = []*Lane{l, r}
	n := &Network{Lanes: []*Lane{f, l, r}, byID: map[string]*Lane{"F": f, "L": l, "R": r}}
	linkPrevs(n)
	return n
}

// A held turn chooses the successor at the junction and is consumed by the
// crossing; with no held turn the default is the first successor.
func TestTurnHeldUntilConsumed(t *testing.T) {
	crossing := func(turn int, turnSet bool) (laneID string, heldAfter int) {
		e := &Engine{
			Params: DefaultParams(),
			Seed:   1,
			Net:    forkNet(),
			index:  map[uint64]*Vehicle{},
			nextID: 1,
			Stats:  Stats{MinGap: math.Inf(1)},
		}
		e.scen.Types = []*VehicleType{&Car}
		v := e.AddInitialVehicle(e.Net.LaneByID("F"), 0, 90, 20, 1)
		in := Intent{VehicleID: v.ID, TurnSet: turnSet, Turn: turn}
		e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: in})
		for i := 0; i < 10 && v.Lane.ID == "F"; i++ {
			e.Step()
			if e.Tick == 1 && v.HeldTurn != turn && turnSet {
				t.Fatalf("held turn lost before the junction: %d, want %d", v.HeldTurn, turn)
			}
		}
		return v.Lane.ID, v.HeldTurn
	}

	lane, held := crossing(-1, true) // right → last successor
	if lane != "R" || held != 0 {
		t.Fatalf("turn right: landed on %s (held %d), want R (0)", lane, held)
	}
	lane, held = crossing(1, true) // left → first successor
	if lane != "L" || held != 0 {
		t.Fatalf("turn left: landed on %s (held %d), want L (0)", lane, held)
	}
	lane, held = crossing(0, false) // no turn → default first
	if lane != "L" || held != 0 {
		t.Fatalf("no turn: landed on %s (held %d), want L (0)", lane, held)
	}
}

// On a single-successor lane the held turn has no choice to make but is
// still consumed by the crossing (it does not leak downstream).
func TestTurnConsumedOnSingleSuccessor(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 2, 1)
	spec.Scen.SpawnRatePerLaneHour = 0
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	a0 := e.Net.LaneByID("A0")
	v := e.AddInitialVehicle(a0, 0, 595, 20, 1)
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: Intent{VehicleID: v.ID, TurnSet: true, Turn: -1}})
	for i := 0; i < 10 && v.Lane == a0; i++ {
		e.Step()
	}
	if v.Lane != e.Net.LaneByID("B0") {
		t.Fatalf("vehicle on %s, want B0", v.Lane.ID)
	}
	if v.HeldTurn != 0 {
		t.Fatalf("held turn = %d after the crossing, want 0 (consumed)", v.HeldTurn)
	}
}

// Routing and signal state are persistent axes: set once, they hold across
// ticks until replaced.
func TestRoutingAndSignalsPersistent(t *testing.T) {
	spec, _ := DefaultSpec("straight", 2, 1)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	v := e.AddInitialVehicle(e.Net.Lanes[0], 0, 100, 20, 1)
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 1, Intent: Intent{
		VehicleID: v.ID, RouteSet: true, Route: "S0", SignalSet: true, Signals: 2,
	}})
	e.Step()
	if v.Route != "S0" || v.Signals != 2 {
		t.Fatalf("after set: route %q signals %d, want S0/2", v.Route, v.Signals)
	}
	for i := 0; i < 5; i++ {
		e.Step()
	}
	if v.Route != "S0" || v.Signals != 2 {
		t.Fatalf("not persistent: route %q signals %d after 5 ticks", v.Route, v.Signals)
	}
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 2, Intent: Intent{
		VehicleID: v.ID, RouteSet: true, Route: "X", SignalSet: true, Signals: 0,
	}})
	e.Step()
	if v.Route != "X" || v.Signals != 0 {
		t.Fatalf("after replace: route %q signals %d, want X/0", v.Route, v.Signals)
	}
	// Signal values clamp to {0..3}.
	e.EnqueueIntent(KeyedIntent{Controller: "c", Seq: 3, Intent: Intent{VehicleID: v.ID, SignalSet: true, Signals: 9}})
	e.Step()
	if v.Signals != 3 {
		t.Fatalf("signals = %d, want clamped 3", v.Signals)
	}
}

// Under the holdlast uncontrolled-policy (live mode) there is no driving
// logic in the engine: vehicles without intents coast (accel exactly 0) and
// never change lanes. Under idm (the harness default) the reference policy
// drives them — the two modes must diverge.
func TestUncontrolledPolicyHoldLast(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 150, 5)
	spec.Scen.UncontrolledPolicy = PolicyHoldLast
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	lastV := map[uint64]float64{}
	for e.Tick < 150 {
		e.Step()
		for _, v := range e.Vehicles() {
			if v.Acc != 0 {
				t.Fatalf("tick %d: vehicle %d accel %v, want 0 (coast)", e.Tick, v.ID, v.Acc)
			}
			if prev, ok := lastV[v.ID]; ok && v.V != prev {
				t.Fatalf("tick %d: vehicle %d V %v → %v while coasting", e.Tick, v.ID, prev, v.V)
			}
			lastV[v.ID] = v.V
		}
	}
	if len(lastV) == 0 {
		t.Fatal("no vehicles spawned")
	}
	if e.Stats.LaneChanges != 0 {
		t.Fatalf("holdlast run had %d lane changes, want 0 (no driving logic in the engine)", e.Stats.LaneChanges)
	}

	idmSpec, _ := DefaultSpec("lanedrop", 150, 5) // default: idm harness
	_, idmLog, err := Run(idmSpec)
	if err != nil {
		t.Fatal(err)
	}
	if equalCRCs(e.CRCs, idmLog.CRCs) {
		t.Fatal("holdlast and idm produced identical trajectories")
	}
}

// Same-tick competing intents resolve by (grant level, then vehicle ID)
// (ADR-0008 §4): the highest-grant intent for a vehicle wins; losers are
// recorded as superseded no-ops. Vehicle ID then makes the log order
// canonical across vehicles.
func TestGrantTieBreak(t *testing.T) {
	spec, _ := DefaultSpec("straight", 2, 1)
	e, err := NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	v1 := e.AddInitialVehicle(e.Net.Lanes[0], 0, 100, 20, 1)
	v2 := e.AddInitialVehicle(e.Net.Lanes[0], 0, 500, 20, 1)

	e.EnqueueIntent(KeyedIntent{Controller: "drv", Seq: 1, Grant: GrantDrive, Intent: Intent{VehicleID: v1.ID, Accel: 0.5, AccelSet: true}})
	e.EnqueueIntent(KeyedIntent{Controller: "dir", Seq: 1, Grant: GrantDirector, Intent: Intent{VehicleID: v1.ID, Accel: -2, AccelSet: true}})
	e.EnqueueIntent(KeyedIntent{Controller: "bbb", Seq: 1, Grant: GrantDrive, Intent: Intent{VehicleID: v2.ID, Accel: -0.5, AccelSet: true}})
	e.EnqueueIntent(KeyedIntent{Controller: "aaa", Seq: 5, Grant: GrantDrive, Intent: Intent{VehicleID: v2.ID, Accel: 0.5, AccelSet: true}})
	e.Step()

	if v1.Acc != -2 {
		t.Fatalf("v1 accel = %v, want −2 (director grant wins)", v1.Acc)
	}
	if v2.Acc != 0.5 {
		t.Fatalf("v2 accel = %v, want 0.5 (equal grants: controller asc first-wins)", v2.Acc)
	}

	got := e.AppliedIntents()
	if len(got) != 4 {
		t.Fatalf("applied %d entries, want 4 (all recorded)", len(got))
	}
	// Canonical order: grant desc (director first), then vehicle asc.
	wantOrder := []struct {
		ctl        string
		superseded bool
	}{
		{"dir", false}, // v1, director grant — winner
		{"drv", true},  // v1, drive grant — superseded
		{"aaa", false}, // v2, controller asc — winner
		{"bbb", true},  // v2 — superseded
	}
	for i, w := range wantOrder {
		if got[i].Controller != w.ctl || got[i].Superseded != w.superseded {
			t.Fatalf("log[%d] = %s superseded=%v, want %s superseded=%v",
				i, got[i].Controller, got[i].Superseded, w.ctl, w.superseded)
		}
	}
	// Superseded intents are recorded in the arbitrated log but must not
	// refresh the per-vehicle winner bookkeeping — a later tick behaves as
	// if only the winner had applied.
	e.Step()
	if v1.reqAccOK || v2.reqAccOK {
		t.Fatal("one-shot accel leaked past its tick")
	}
}
