package natsio

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// contract_test.go — the ADR-0008 contract machinery, exercised with raw
// protocol clients (no driver library): attach handshake, claim
// exclusivity/release/re-claim, observation windows, and hold-last with
// decay. Driver-library end-to-end coverage (failover, pause, differential,
// replay-without-driver, introspection) lives in driver_test.go
// (package natsio_test — it imports engine/natsio/driver).

type liveOutcome struct {
	lr  *LiveRun
	err error
}

type liveHandle struct {
	srv  *TestServer
	nc   *nats.Conn
	js   nats.JetStreamContext
	run  string
	done chan liveOutcome
}

var liveRunCounter atomic.Uint64

// neverTick disables a tick-counted contract threshold in tests that are not
// exercising it (raw protocol clients here do not heartbeat, and a drive
// controller that never claims rightfully trips the pause gate — ADR-0008
// §6 — which is not what these tests are about).
const neverTick = 1 << 40

// startLiveRun launches RunLive in a goroutine and waits for the run to
// appear in the registry (i.e. the loop is up).
func startLiveRun(t *testing.T, spec engine.RunSpec, recCfg RecorderConfig, cc ContractConfig) *liveHandle {
	t.Helper()
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	run := fmt.Sprintf("m4x%d", liveRunCounter.Add(1))
	h := &liveHandle{srv: srv, nc: nc, js: js, run: run, done: make(chan liveOutcome, 1)}
	go func() {
		lr, err := RunLive(nc, js, run, spec, recCfg, cc)
		h.done <- liveOutcome{lr, err}
	}()
	waitFor(t, "run registry meta", 10*time.Second, func() bool {
		kv, err := js.KeyValue(RegistryBucket)
		if err != nil {
			return false
		}
		_, err = kv.Get(run + "/meta")
		return err == nil
	})
	return h
}

// finish awaits the run's completion (bounded).
func (h *liveHandle) finish(t *testing.T) liveOutcome {
	t.Helper()
	select {
	case out := <-h.done:
		return out
	case <-time.After(120 * time.Second):
		t.Fatal("run did not finish in time")
		return liveOutcome{}
	}
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout (%s) waiting for %s", timeout, what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func rawHello(t *testing.T, nc *nats.Conn, run string, req HelloRequest) HelloReply {
	t.Helper()
	data, _ := json.Marshal(req)
	resp, err := nc.Request(SubjectCtlHello(run), data, 2*time.Second)
	if err != nil {
		t.Fatalf("hello request: %v", err)
	}
	var rep HelloReply
	if err := json.Unmarshal(resp.Data, &rep); err != nil {
		t.Fatalf("hello reply: %v", err)
	}
	return rep
}

func rawClaim(t *testing.T, nc *nats.Conn, run, ctl string, ids []uint64) ClaimReply {
	t.Helper()
	data, _ := json.Marshal(ClaimRequest{VehicleIDs: ids})
	resp, err := nc.Request(SubjectCtlClaim(run, ctl), data, 2*time.Second)
	if err != nil {
		t.Fatalf("claim request: %v", err)
	}
	var rep ClaimReply
	if err := json.Unmarshal(resp.Data, &rep); err != nil {
		t.Fatalf("claim reply: %v", err)
	}
	return rep
}

// snapWatch is a snapshot-driven probe: latest tick and the set of vehicle
// ids seen so far.
type snapWatch struct {
	lastTick atomic.Uint64
	count    atomic.Uint64
	mu       sync.Mutex
	ids      map[uint64]bool
}

func watchSnaps(t *testing.T, nc *nats.Conn, run string) *snapWatch {
	t.Helper()
	w := &snapWatch{ids: map[uint64]bool{}}
	sub, err := nc.Subscribe(SubjectStateSnap(run), func(m *nats.Msg) {
		tick, _ := strconv.ParseUint(m.Header.Get("tick"), 10, 64)
		w.lastTick.Store(tick)
		w.count.Add(1)
		f, err := ParseFrame(m.Data)
		if err != nil {
			return
		}
		w.mu.Lock()
		for _, v := range f.Vehicles {
			w.ids[v.ID] = true
		}
		w.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return w
}

// vehicleIDs returns the seen ids, sorted.
func (w *snapWatch) vehicleIDs() []uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]uint64, 0, len(w.ids))
	for id := range w.ids {
		out = append(out, id)
	}
	for i := 0; i+1 < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// The attach handshake: accept with assigned controller id and grants;
// reject version mismatch, grant-less hellos, absurd cadence. The director
// and signal grants exist (verbs land M5+).
func TestAttachHandshake(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 400, 11)
	if err != nil {
		t.Fatal(err)
	}
	h := startLiveRun(t, spec, RecorderConfig{}, ContractConfig{
		PaceFloor: 2 * time.Millisecond, PauseAfterTicks: neverTick, DetachAfterTicks: 500,
	})
	c := h.srv.Connect(t)

	rep := rawHello(t, c, h.run, HelloRequest{
		ContractVersion: 1, Grants: []string{"drive"}, ClaimCapacity: 5,
	})
	if rep.Accepted {
		t.Fatal("stale contract_version accepted")
	}
	t.Logf("version reject reason: %q", rep.Reason)

	rep = rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "ai", CadenceTicks: 1,
		Grants: []string{"drive"}, ClaimCapacity: 5,
		Window: ObsWindow{RadiusM: 100, MaxNeighbors: 4, Features: ObsFeatureNeighbors},
	})
	if !rep.Accepted {
		t.Fatalf("valid hello rejected: %q", rep.Reason)
	}
	if rep.ControllerID == "" || rep.ContractVersion != SchemaVersion || rep.Run != h.run {
		t.Fatalf("reply = %+v", rep)
	}
	if len(rep.Grants) != 1 || rep.Grants[0] != "drive" {
		t.Fatalf("grants = %v, want [drive]", rep.Grants)
	}
	driveID := rep.ControllerID

	rep = rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "script", Grants: []string{"director"},
	})
	if !rep.Accepted {
		t.Fatalf("director grant rejected: %q", rep.Reason)
	}
	rep = rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "signal", Grants: []string{"signal"},
	})
	if !rep.Accepted {
		t.Fatalf("signal stub grant rejected: %q", rep.Reason)
	}

	rep = rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ClaimCapacity: 3,
	})
	if rep.Accepted {
		t.Fatal("grant-less hello accepted")
	}
	rep = rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, Grants: []string{"drive"}, ClaimCapacity: 3,
		CadenceTicks: 999,
	})
	if rep.Accepted {
		t.Fatal("cadence beyond the detach budget accepted")
	}
	rep = rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, Grants: []string{"fly"},
	})
	if rep.Accepted {
		t.Fatal("unknown grant accepted")
	}
	_ = driveID

	if out := h.finish(t); out.err != nil {
		t.Fatalf("run: %v", out.err)
	}
}

// Exclusive per-vehicle claims, engine-arbitrated: a second claim on a
// claimed vehicle is rejected; release frees it (unclaimed event) and a
// peer re-claims. Multi-vehicle claims are allowed.
func TestClaimExclusivity(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 400, 3)
	if err != nil {
		t.Fatal(err)
	}
	h := startLiveRun(t, spec, RecorderConfig{}, ContractConfig{
		PaceFloor: 2 * time.Millisecond, PauseAfterTicks: neverTick, DetachAfterTicks: neverTick,
	})
	c := h.srv.Connect(t)
	snaps := watchSnaps(t, c, h.run)

	a := rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "ai", Grants: []string{"drive"}, ClaimCapacity: 10,
	})
	b := rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "ai", Grants: []string{"drive"}, ClaimCapacity: 10,
	})
	if !a.Accepted || !b.Accepted {
		t.Fatal("attach failed")
	}

	waitFor(t, "two vehicles", 15*time.Second, func() bool { return len(snaps.vehicleIDs()) >= 2 })
	ids := snaps.vehicleIDs()
	v1, v2 := ids[0], ids[1]

	rep := rawClaim(t, c, h.run, a.ControllerID, []uint64{v1})
	if len(rep.Granted) != 1 || rep.Granted[0] != v1 {
		t.Fatalf("A's claim = %+v", rep)
	}
	rep = rawClaim(t, c, h.run, b.ControllerID, []uint64{v1})
	if len(rep.Granted) != 0 {
		t.Fatalf("B claimed A's vehicle: %+v", rep)
	}
	if len(rep.Rejected) != 1 || rep.Rejected[0] != v1 {
		t.Fatalf("B's rejected list = %+v", rep)
	}

	// Multi-vehicle claim (idempotent re-claim of v1 plus v2).
	rep = rawClaim(t, c, h.run, a.ControllerID, []uint64{v1, v2})
	if len(rep.Granted) != 2 {
		t.Fatalf("A's multi-claim = %+v", rep)
	}

	// Unclaimed events on release; B re-claims.
	uncCh := make(chan ContractEvent, 64)
	sub, err := c.Subscribe(SubjectEventUnclaimed(h.run), func(m *nats.Msg) {
		var evt ContractEvent
		if err := json.Unmarshal(m.Data, &evt); err == nil {
			uncCh <- evt
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}

	data, _ := json.Marshal(ReleaseRequest{VehicleIDs: []uint64{v1}})
	if err := c.Publish(SubjectCtlRelease(h.run, a.ControllerID), data); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unclaimed event (release)", 10*time.Second, func() bool {
		for len(uncCh) > 0 {
			evt := <-uncCh
			if evt.Reason == ReasonRelease {
				for _, id := range evt.VehicleIDs {
					if id == v1 {
						return true
					}
				}
			}
		}
		return false
	})

	rep = rawClaim(t, c, h.run, b.ControllerID, []uint64{v1})
	if len(rep.Granted) != 1 || rep.Granted[0] != v1 {
		t.Fatalf("B's re-claim after release = %+v", rep)
	}

	if out := h.finish(t); out.err != nil {
		t.Fatalf("run: %v", out.err)
	}
}

// Observation windows: per tick the engine publishes per-controller
// observations — ego exact per claimed vehicle plus the sorted/capped
// neighbor list and the resolved policy context (feature-gated). An
// attached controller with an empty fleet still gets its tick clock.
func TestObservations(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 400, 5)
	if err != nil {
		t.Fatal(err)
	}
	h := startLiveRun(t, spec, RecorderConfig{}, ContractConfig{
		PaceFloor: 2 * time.Millisecond, PauseAfterTicks: neverTick, DetachAfterTicks: neverTick,
	})
	c := h.srv.Connect(t)
	snaps := watchSnaps(t, c, h.run)
	types := []*engine.VehicleType{&engine.Car}

	a := rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "ai", Grants: []string{"drive"}, ClaimCapacity: 10,
		Window: ObsWindow{RadiusM: 60, MaxNeighbors: 2, Features: ObsFeatureNeighbors | ObsFeaturePolicyCtx},
	})
	b := rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "ai", Grants: []string{"drive"}, ClaimCapacity: 10,
	})
	if !a.Accepted || !b.Accepted {
		t.Fatal("attach failed")
	}

	type obsMsg struct{ data []byte }
	obsA := make(chan []byte, 16)
	obsB := make(chan []byte, 16)
	subA, err := c.Subscribe(SubjectCtlObs(h.run, a.ControllerID), func(m *nats.Msg) { obsA <- m.Data })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subA.Unsubscribe() }()
	subB, err := c.Subscribe(SubjectCtlObs(h.run, b.ControllerID), func(m *nats.Msg) { obsB <- m.Data })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = subB.Unsubscribe() }()

	waitFor(t, "three vehicles", 15*time.Second, func() bool { return len(snaps.vehicleIDs()) >= 3 })
	vid := snaps.vehicleIDs()[0]
	rep := rawClaim(t, c, h.run, a.ControllerID, []uint64{vid})
	if len(rep.Granted) != 1 {
		t.Fatalf("claim = %+v", rep)
	}

	// A's frames carry the claimed ego (exact + policy ctx) and a capped
	// neighbor list.
	var got Obs
	waitFor(t, "observation with claimed ego", 15*time.Second, func() bool {
		for len(obsA) > 0 {
			data := <-obsA
			o, err := DecodeObs(data, types)
			if err != nil {
				t.Errorf("decode obs: %v", err)
				continue
			}
			for _, ego := range o.Egos {
				if ego.ID == vid {
					got = o
					return true
				}
			}
		}
		return false
	})
	if len(got.Egos) != 1 {
		t.Fatalf("egos = %d, want 1", len(got.Egos))
	}
	ego := got.Egos[0]
	if ego.TypeIdx != 0 || ego.LaneIdx < 0 || ego.LaneIdx > 4 || ego.S < 0 || ego.F < 0.8 || ego.F > 1.3 {
		t.Fatalf("ego block insane: %+v", ego)
	}
	if ego.Ctx == nil {
		t.Fatal("policy ctx missing despite the feature flag")
	}
	if ego.Ctx.CurLimit != 33.3 {
		t.Fatalf("ctx cur limit = %v, want 33.3", ego.Ctx.CurLimit)
	}
	if ego.Ctx.CurLead.OK && ego.Ctx.CurLead.Gap < -5 {
		t.Fatalf("ctx leader gap implausible: %+v", ego.Ctx.CurLead)
	}
	if len(got.Neighbors) > 2 {
		t.Fatalf("neighbor list = %d, want ≤ 2 (window cap)", len(got.Neighbors))
	}
	for _, nb := range got.Neighbors {
		if nb.ID == vid {
			t.Fatal("ego appears in its own neighbor list")
		}
	}
	t.Logf("observation: tick %d, ego %+v, neighbors %d", got.Tick, ego, len(got.Neighbors))

	// B (empty fleet) still receives its per-tick clock frame.
	waitFor(t, "empty-fleet observation clock", 15*time.Second, func() bool {
		for len(obsB) > 0 {
			o, err := DecodeObs(<-obsB, types)
			if err == nil && len(o.Egos) == 0 && o.Tick > 0 {
				return true
			}
		}
		return false
	})

	if out := h.finish(t); out.err != nil {
		t.Fatalf("run: %v", out.err)
	}
}

// Hold-last (ADR-0008 §2): when an attached controller misses its expected
// message, the contract layer re-issues the vehicle's last intent for ≤ 2
// ticks (flagged Held in the arbitrated log), then stops — the vehicle
// decays to zero/default until fresh intents resume.
func TestHoldLastThenDecay(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 400, 13)
	if err != nil {
		t.Fatal(err)
	}
	cc := ContractConfig{PaceFloor: 3 * time.Millisecond, PauseAfterTicks: neverTick}
	h := startLiveRun(t, spec, RecorderConfig{}, cc)
	c := h.srv.Connect(t)
	snaps := watchSnaps(t, c, h.run)

	a := rawHello(t, c, h.run, HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "ai", Grants: []string{"drive"}, ClaimCapacity: 10,
	})
	if !a.Accepted {
		t.Fatal("attach failed")
	}
	waitFor(t, "a vehicle", 15*time.Second, func() bool { return len(snaps.vehicleIDs()) >= 1 })
	vid := snaps.vehicleIDs()[0]
	if rep := rawClaim(t, c, h.run, a.ControllerID, []uint64{vid}); len(rep.Granted) != 1 {
		t.Fatalf("claim = %+v", rep)
	}

	// Scripted client: drive vid every obs tick, except an 8-tick skip
	// window where it only heartbeats (stays attached, sends no intent).
	const skipLen = 8
	obsTicks := make(chan uint64, 512)
	sub, err := c.Subscribe(SubjectCtlObs(h.run, a.ControllerID), func(m *nats.Msg) {
		tick, _ := strconv.ParseUint(m.Header.Get("tick"), 10, 64)
		obsTicks <- tick
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		var startSkip uint64
		skipping := false
		for {
			select {
			case <-stop:
				return
			case tick := <-obsTicks:
				switch {
				case !skipping && startSkip == 0 && tick >= 100:
					startSkip, skipping = tick, true
					fallthrough
				case skipping && tick < startSkip+skipLen:
					_ = c.Publish(SubjectCtlHeartbeat(h.run, a.ControllerID), nil)
				case skipping:
					skipping = false
					fallthrough
				default:
					in := engine.Intent{VehicleID: vid, Accel: 0.3, AccelSet: true}
					_ = c.Publish(SubjectCtlIntent(h.run, a.ControllerID), EncodeIntent(in))
				}
			}
		}
	}()

	out := h.finish(t)
	if out.err != nil {
		t.Fatalf("run: %v", out.err)
	}

	// Arbitrated log analysis for vid from this controller.
	var held, freshBefore, freshAfter, heldAfterResume int
	var lastHeldTick uint64
	for _, ti := range out.lr.Engine.IntentLog {
		if ti.Controller != a.ControllerID || ti.Intent.VehicleID != vid {
			continue
		}
		if ti.Held {
			held++
			lastHeldTick = ti.Tick
		} else if lastHeldTick == 0 {
			freshBefore++
		} else {
			freshAfter++
		}
	}
	if held != 2 {
		t.Fatalf("held re-issues = %d, want 2 (≤ 2 ticks of message-loss healing)", held)
	}
	if freshBefore == 0 || freshAfter == 0 {
		t.Fatalf("fresh intents before/after the gap: %d/%d", freshBefore, freshAfter)
	}
	for _, ti := range out.lr.Engine.IntentLog {
		if ti.Controller == a.ControllerID && ti.Intent.VehicleID == vid && ti.Held && ti.Tick > lastHeldTick {
			heldAfterResume++
		}
	}
	t.Logf("vehicle %d: %d fresh before gap, %d held, %d fresh after (last held tick %d)",
		vid, freshBefore, held, freshAfter, lastHeldTick)
}

// Intent codec v2: all four axes round-trip; route caps at intentMaxRoute;
// malformed frames reject.
func TestIntentCodecV2Axes(t *testing.T) {
	full := engine.Intent{
		VehicleID: 42, Accel: -2.5, AccelSet: true,
		SpeedSetpoint: 22.5, SpeedSet: true,
		LaneDelta: -1, Turn: 1, TurnSet: true,
		Route: "B0", RouteSet: true, Signals: 3, SignalSet: true,
	}
	got, ok := DecodeIntent(EncodeIntent(full))
	if !ok || got != full {
		t.Fatalf("full-axes round trip = %+v (ok %v), want %+v", got, ok, full)
	}

	// Route truncation at the cap.
	long := full
	b := make([]byte, 100)
	for i := range b {
		b[i] = 'x'
	}
	long.Route = string(b)
	got, ok = DecodeIntent(EncodeIntent(long))
	if !ok || len(got.Route) != intentMaxRoute {
		t.Fatalf("long route: len %d (ok %v), want %d", len(got.Route), ok, intentMaxRoute)
	}

	// RouteSet with empty route encodes as no-route.
	empty := engine.Intent{VehicleID: 1, RouteSet: true}
	got, ok = DecodeIntent(EncodeIntent(empty))
	if !ok || got.RouteSet || got.Route != "" {
		t.Fatalf("empty route: %+v (ok %v)", got, ok)
	}

	if _, ok := DecodeIntent(make([]byte, intentFixedBytes+3)); ok {
		t.Fatal("route_len mismatch accepted")
	}
}

// Observation frame codec round-trip from a live engine, including the
// resolved policy context (decoded decisions must equal the live ones).
func TestObsFrameRoundTrip(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 80, 2)
	if err != nil {
		t.Fatal(err)
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 80 {
		e.Step()
	}
	vs := e.Vehicles()
	if len(vs) == 0 {
		t.Fatal("no vehicles")
	}
	types := spec.Scen.Types
	if len(types) == 0 {
		types = []*engine.VehicleType{&engine.Car}
	}

	v := vs[len(vs)/2]
	ctx := e.PolicyContext(v)
	egos := []ObsEgo{{
		ID: v.ID, LaneIdx: v.Lane.Index, TypeIdx: v.TypeIdx,
		S: v.S, V: v.V, F: v.F, Acc: v.Acc, Cooldown: v.Cooldown,
		Signals: 2, HeldTurn: -1, Cruise: 20, CruiseOK: true, Route: "B1",
		Ctx: ctx,
	}}
	nbs := []ObsNeighbor{
		{ID: 991, LaneIdx: 1, TypeIdx: 0, S: 12.5, V: 20, F: 1, Signals: 1},
		{ID: 992, LaneIdx: 3, TypeIdx: 0, S: 55, V: 22, F: 1.1, Signals: 0},
	}
	buf := EncodeObs(e.Tick, ObsFeatureNeighbors|ObsFeaturePolicyCtx, egos, nbs)
	got, err := DecodeObs(buf, types)
	if err != nil {
		t.Fatalf("DecodeObs: %v", err)
	}
	if got.Tick != e.Tick || len(got.Egos) != 1 || len(got.Neighbors) != 2 {
		t.Fatalf("frame: tick %d egos %d nbs %d", got.Tick, len(got.Egos), len(got.Neighbors))
	}
	ge := got.Egos[0]
	if ge.ID != v.ID || ge.S != v.S || ge.V != v.V || ge.F != v.F || ge.Signals != 2 ||
		ge.HeldTurn != -1 || !ge.CruiseOK || ge.Cruise != 20 || ge.Route != "B1" {
		t.Fatalf("ego round trip = %+v", ge)
	}
	if got.Neighbors[0].ID != 991 || got.Neighbors[1].Signals != 0 || got.Neighbors[1].F != 1.1 {
		t.Fatalf("neighbors = %+v", got.Neighbors)
	}
	if ge.Ctx == nil {
		t.Fatal("decoded ctx missing")
	}
	if a, w := ge.Ctx.DecideAccel(), ctx.DecideAccel(); a != w {
		t.Fatalf("decoded DecideAccel = %v, live %v", a, w)
	}
	s1, s2 := engine.DeriveStream(1, v.ID), engine.DeriveStream(1, v.ID)
	if d, w := ge.Ctx.DecideLaneChange(e.Params, s1), ctx.DecideLaneChange(e.Params, s2); d != w {
		t.Fatalf("decoded DecideLaneChange = %v, live %v", d, w)
	}
	if ge.Ctx.CurFoll.OK && ge.Ctx.CurFoll.Type != types[ge.Ctx.CurFoll.TypeIdx] {
		t.Fatal("follower type not re-linked to the scenario table")
	}
	// Truncation must fail cleanly.
	if _, err := DecodeObs(buf[:len(buf)-2], types); err == nil {
		t.Fatal("truncated obs accepted")
	}
}

// TestObsOutageStreak pins the distinction the fidelity gates are built on:
// the TOTAL number of lost observation frames does not invalidate a run, the
// longest CONSECUTIVE run of them does. ADR-0008 §2 holds the last intent
// across (cadence−1) + HoldLastTicks ticks, so isolated losses are healed and
// the fleet stays controlled; only a streak that outlives that bridge leaves
// vehicles coasting at Acc = 0. The first cut of this counter equated the two
// and would have refused perfectly good bakes on a single dropped frame.
func TestObsOutageStreak(t *testing.T) {
	c := &Contract{}
	c.cfg.HoldLastTicks = 2
	a := &controller{id: "a", cadence: 1, grants: map[string]bool{"drive": true}}
	b := &controller{id: "b", cadence: 1, grants: map[string]bool{"drive": true}}
	c.ctrls = map[string]*controller{"a": a, "b": b}

	if got, want := c.ObsBridgeTicks(), uint64(2); got != want {
		t.Fatalf("bridge at cadence 1 = %d, want %d", got, want)
	}

	// Six losses, never two in a row: heavily lossy, still never blind.
	for i := 0; i < 6; i++ {
		c.noteObsErr(a)
		c.noteObsOK(a)
	}
	if got := c.ObsWorstOutage(); got != 1 {
		t.Fatalf("worst outage after 6 isolated losses = %d, want 1 — isolated losses must not accumulate into a false outage", got)
	}

	// Three in a row on the OTHER controller: past the 2-tick bridge, and it
	// must be seen even though controller a is healthy. The maximum is over
	// controllers, not a single global counter — one blind controller among
	// several still means part of the fleet drove itself.
	c.noteObsErr(b)
	c.noteObsErr(b)
	c.noteObsErr(b)
	if got := c.ObsWorstOutage(); got != 3 {
		t.Fatalf("worst outage = %d, want 3", got)
	}
	if c.ObsWorstOutage() <= c.ObsBridgeTicks() {
		t.Fatalf("a 3-tick outage must exceed the 2-tick bridge")
	}

	// Recovery does not lower the high-water mark: the run still contained a
	// blind window, and a gate reading this after the fact must still see it.
	c.noteObsOK(b)
	if got := c.ObsWorstOutage(); got != 3 {
		t.Fatalf("worst outage after recovery = %d, want it to stay at 3", got)
	}

	// The bridge does NOT widen with cadence. Hold-last measures from the
	// last fresh intent, so a controller at cadence k that loses r frames
	// spanning its due tick has a k+r gap between fresh intents and falls
	// out of the window as soon as r > HoldLastTicks — independent of k.
	// (cadence−1)+HoldLastTicks is the lucky-alignment figure; a gate has to
	// hold for the unlucky one. Pinned because an earlier cut of this
	// accessor returned the optimistic value and would have called a real
	// outage "absorbed" on any controller slower than cadence 1.
	b.cadence = 5
	if got, want := c.ObsBridgeTicks(), uint64(2); got != want {
		t.Fatalf("bridge with a cadence-5 controller = %d, want %d — cadence must not widen it", got, want)
	}
	a.cadence = 5
	if got, want := c.ObsBridgeTicks(), uint64(2); got != want {
		t.Fatalf("bridge with all controllers at cadence 5 = %d, want %d — cadence must not widen it", got, want)
	}
}
