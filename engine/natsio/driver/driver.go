// Package driver is the external default driver (ADR-0008 §5): the
// reference controller implementation, running as a normal controller
// process on the NATS contract. It attaches with the drive grant, claims
// unclaimed vehicles up to its capacity, and drives them each tick — IDM
// longitudinal + MOBIL lateral computed CLIENT-SIDE from observations,
// reusing the kernel's shared policy functions (engine.PolicyCtx), plus
// destination params for the persistent routing axis (a configured
// override, or per-vehicle drawn exit lanes — destinations.go); the kernel
// follows a set destination at every multi-successor lane
// (engine/routing.go), so no per-junction turn traffic is needed. Policy RNG
// streams are seeded per vehicle, keyed by vehicle ID (ADR-0007) — never
// per process — so fleet failover and orphan re-claim are behaviorally
// invisible, and which replica drives a vehicle does not matter.
//
// Jobs (concept-vehicle-controller-interface, RESOLVED 2026-07-17):
// ambient-traffic generator, handoff source when a human claims a vehicle,
// orphan re-claimer (via unclaimed-vehicle events, with the snapshot diff
// as backstop), and host of the introspection interface — request/reply on
// ts.{run}.drive.introspect answering "given this vehicle state, what would
// you do?" as a pure function of state + policy.
//
// Replicas shard emergently via exclusive claims: each replica claims from
// the unclaimed pool up to its capacity; no leader election, no assigned
// shards. Replay never runs the driver — recorded intents come from the
// JetStream log.
package driver

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// Config is one default-driver replica's setup.
type Config struct {
	// Run is the run id to attach to (required).
	Run string
	// Capacity is the claim capacity: max vehicles this replica holds
	// (default 1000). Fleets size replicas to absorb one full peer loss.
	Capacity int
	// Destination is a lane ID routed toward for every claimed vehicle
	// (the routing axis' destination parameter; "" = none). The explicit
	// override: it wins over ExitRouting when set.
	Destination string
	// ExitRouting assigns each claimed vehicle its own destination among
	// the network's exit lanes (speed-limit weighted, lanes under 30 m
	// excluded), deterministic from the run seed and vehicle ID
	// (ADR-0007) — see destinations.go. Ignored when Destination is set.
	ExitRouting bool
	// RouteBudgetPerTick caps how many vehicles per observation get their
	// routing destination RESOLVED (default 32): the exit draw's
	// first-touch reachability scan is O(network) per NEW origin lane
	// (warm picks are cheap and still pass through the counter), so an
	// unbounded claim burst on a big network stalls the tick loop
	// (measured: the Observe path was synchronous in the tick loop,
	// run.go). Vehicles past the budget keep the kernel's default routing
	// and retry on the next observation. TIMING NOTE (ADR-0008
	// invisibility bound): admission order is per-replica, so a deferred
	// vehicle's route lands up to ceil(unrouted/budget) observations
	// later than an unbudgeted one — ~0.3 sim-s at demo spawn rates. A
	// fork passed in that window means default routing for that leg; the
	// draw itself stays seed-pure (frozen first-observation lane).
	// Strict timing-invariance needs an O(1) pickExit (global
	// exit-reachability), future work.
	RouteBudgetPerTick int
	// IntentBatchOff restores the pre-ADR-0026 wire shape: one 44 B v2
	// message per claimed vehicle per tick. Default ON: the driver
	// aggregates the tick's route-free intents into TSIB batches — one
	// message per cadence tick per controller, split at
	// natsio.TSIBMaxRecords; route-update vehicles still ride standalone
	// v2 (driver.go onObs). Exists for A/B measurement and debugging.
	IntentBatchOff bool
	// Type is the controller_type declared at attach (default
	// "default-driver").
	Type string
	// MetaWait bounds how long New waits for the run's registry meta
	// (default 10 s) — the driver may boot before the engine.
	MetaWait time.Duration
}

func (c Config) withDefaults() Config {
	if c.Capacity <= 0 {
		c.Capacity = 1000
	}
	if c.Type == "" {
		c.Type = "default-driver"
	}
	if c.MetaWait <= 0 {
		c.MetaWait = 10 * time.Second
	}
	if c.RouteBudgetPerTick <= 0 {
		c.RouteBudgetPerTick = 32
	}
	return c
}

// Driver is one default-driver replica: an attached drive-grant controller
// with a claimed fleet.
type Driver struct {
	nc    *nats.Conn
	cfg   Config
	id    string
	spec  engine.RunSpec
	types []*engine.VehicleType
	net   *engine.Network

	mu      sync.Mutex
	fleet   map[uint64]bool // claimed vehicles (reconciled against observations)
	pending map[uint64]bool // claims in flight
	routed  map[uint64]bool // route intent confirmed by the obs echo (persistent axis)
	streams map[uint64]*engine.Stream
	exits   *exitCache // memoized pickExit inputs (candidates, reachability)

	wantRoute map[uint64]string // destination chosen, re-sent until the obs echo confirms
	wantLane  map[uint64]int    // lane at FIRST unrouted observation — the pickExit input (budget-deferred draws stay seed-deterministic)

	subs []*nats.Subscription
	done chan struct{}
	once sync.Once

	lastObsTick      atomic.Uint64 // obs tick at the START of onObs (work begun)
	completedObsTick atomic.Uint64 // obs tick at the END of onObs (response fully published)
	sentIntents      atomic.Uint64
}

// New attaches a default-driver replica to its run: registry meta → hello
// handshake → subscriptions. The replica starts working as messages arrive
// (claims from snapshot diffs and unclaimed events; intents from
// observations).
func New(nc *nats.Conn, js nats.JetStreamContext, cfg Config) (*Driver, error) {
	cfg = cfg.withDefaults()
	if err := nc.Flush(); err != nil {
		return nil, err
	}

	// The run's spec comes from the KV run registry (the record plane pairs
	// registry ↔ log stream): network graph, params, seed, scenario types.
	meta, err := awaitMeta(js, cfg.Run, cfg.MetaWait)
	if err != nil {
		return nil, err
	}
	d := &Driver{
		nc:      nc,
		cfg:     cfg,
		spec:    meta.Spec,
		types:   meta.Spec.Scen.Types,
		fleet:   map[uint64]bool{},
		pending: map[uint64]bool{},
		routed:  map[uint64]bool{},
		streams: map[uint64]*engine.Stream{},

		wantRoute: map[uint64]string{},
		wantLane:  map[uint64]int{},
		done:      make(chan struct{}),
	}
	if len(d.types) == 0 {
		d.types = []*engine.VehicleType{&engine.Car} // the kernel's own default
	}
	if d.net, err = engine.BuildNet(meta.Spec.Net); err != nil {
		return nil, fmt.Errorf("driver: build net: %w", err)
	}

	// Attach handshake: contract version, type, cadence, grants, capacity,
	// AoI window (ADR-0008 §3–§4).
	hello := natsio.HelloRequest{
		ContractVersion: natsio.SchemaVersion,
		ControllerType:  cfg.Type,
		CadenceTicks:    1,
		Grants:          []string{"drive"},
		ClaimCapacity:   cfg.Capacity,
		Window: natsio.ObsWindow{
			RadiusM:      250,
			MaxNeighbors: 0, // the policy ctx carries everything the policy needs
			Features:     natsio.ObsFeaturePolicyCtx,
		},
	}
	payload, _ := json.Marshal(hello)
	// Like awaitMeta, the hello must tolerate an engine that is still coming
	// up: RunLive subscribes the contract plane only AFTER the world build,
	// the recorder, and the bus — on big networks the driver can finish its
	// own BuildNet first and hit an unsubscribed hello subject (fast-fail
	// "no responders"). Retry ONLY that error: the hello is not idempotent
	// (the engine creates a controller on receipt), so retrying a TIMEOUT
	// risks a ghost controller holding claim capacity (sol review); any
	// other failure surfaces immediately.
	helloDeadline := time.Now().Add(cfg.MetaWait)
	var resp *nats.Msg
	var helloErr error
	for {
		// The request timeout is the REMAINING budget (capped at 2 s), and
		// the retry sleep never overshoots the deadline — MetaWait must
		// actually bound the wait (sol review).
		remaining := time.Until(helloDeadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("driver: hello: deadline exhausted (%s): %w", cfg.MetaWait, helloErr)
		}
		resp, helloErr = nc.Request(natsio.SubjectCtlHello(cfg.Run), payload, min(2*time.Second, remaining))
		if helloErr == nil {
			break
		}
		if !errors.Is(helloErr, nats.ErrNoResponders) {
			return nil, fmt.Errorf("driver: hello: %w", helloErr)
		}
		// Always sleep, never hot-spin — capped so the deadline holds.
		time.Sleep(min(250*time.Millisecond, time.Until(helloDeadline)))
	}
	var reply natsio.HelloReply
	if err := json.Unmarshal(resp.Data, &reply); err != nil {
		return nil, fmt.Errorf("driver: hello reply: %w", err)
	}
	if !reply.Accepted {
		return nil, fmt.Errorf("driver: attach rejected: %s", reply.Reason)
	}
	d.id = reply.ControllerID
	d.applyIntentEncodings(reply.IntentEncodings)

	bind := func(subject string, h func(*nats.Msg)) error {
		sub, err := nc.Subscribe(subject, h)
		if err != nil {
			return fmt.Errorf("driver: subscribe %s: %w", subject, err)
		}
		d.subs = append(d.subs, sub)
		return nil
	}
	if err := bind(natsio.SubjectCtlObs(cfg.Run, d.id), d.onObs); err != nil {
		return nil, err
	}
	if err := bind(natsio.SubjectStateSnap(cfg.Run), d.onSnap); err != nil {
		return nil, err
	}
	if err := bind(natsio.SubjectEventUnclaimed(cfg.Run), d.onUnclaimed); err != nil {
		return nil, err
	}
	if err := bind(natsio.SubjectDriveIntrospect(cfg.Run), d.onIntrospect); err != nil {
		return nil, err
	}
	return d, nc.Flush()
}

// awaitMeta polls the run registry for the run's meta (the driver may boot
// before the engine creates the bucket/keys).
func awaitMeta(js nats.JetStreamContext, run string, wait time.Duration) (*natsio.RunMeta, error) {
	deadline := time.Now().Add(wait)
	for {
		kv, err := js.KeyValue(natsio.RegistryBucket)
		if err == nil {
			if entry, err := kv.Get(run + "/meta"); err == nil {
				var meta natsio.RunMeta
				if err := json.Unmarshal(entry.Value(), &meta); err != nil {
					return nil, fmt.Errorf("driver: registry meta corrupt: %w", err)
				}
				return &meta, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("driver: run %q not found in the registry within %s", run, wait)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ID is the engine-assigned controller id.
func (d *Driver) ID() string { return d.id }

// FleetSize is the current claim count.
func (d *Driver) FleetSize() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.fleet)
}

// LastObsTick is the newest observation tick the driver has STARTED working
// on (stamped at the top of onObs, before compute/publish — a begin
// watermark only).
func (d *Driver) LastObsTick() uint64 { return d.lastObsTick.Load() }

// CompletedObsTick is the newest observation tick whose response is FULLY
// PUBLISHED (stamped at the very end of onObs, after the tick's intents —
// batch or per-vehicle v2 — have gone out): the "response complete through
// obs T" watermark. Use it, never LastObsTick, wherever completion
// semantics are meant.
func (d *Driver) CompletedObsTick() uint64 { return d.completedObsTick.Load() }

// SentIntents counts per-vehicle intents published (TSIB records plus
// standalone v2) — the same unit in batch and batch-off mode, so the
// counter is comparable across -intent-batch settings (observability).
func (d *Driver) SentIntents() uint64 { return d.sentIntents.Load() }

// Wait blocks until the driver stops (Close or Kill).
func (d *Driver) Wait() { <-d.done }

// Done returns the stop channel (for select loops; closed by Close/Kill).
func (d *Driver) Done() <-chan struct{} { return d.done }

// Close detaches gracefully: release every claim and end the attachment
// (the supervisor/shutdown path).
func (d *Driver) Close() {
	d.once.Do(func() {
		req, _ := json.Marshal(natsio.ReleaseRequest{Detach: true})
		_ = d.nc.Publish(natsio.SubjectCtlRelease(d.cfg.Run, d.id), req)
		_ = d.nc.Flush()
		d.stop()
	})
}

// Kill stops the driver WITHOUT saying goodbye — claims linger until the
// engine's liveness timeout detaches it (the failover/chaos path: a dead
// replica's orphans are re-claimed by peers via unclaimed-vehicle events).
func (d *Driver) Kill() {
	d.once.Do(d.stop)
}

func (d *Driver) stop() {
	for _, sub := range d.subs {
		_ = sub.Unsubscribe()
	}
	close(d.done)
}

// stream returns the vehicle's per-ID seeded policy stream (ADR-0007).
func (d *Driver) stream(vehicleID uint64) *engine.Stream {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.streams[vehicleID]
	if !ok {
		s = engine.DeriveStream(d.spec.Seed, vehicleID)
		d.streams[vehicleID] = s
	}
	return s
}

// onObs is the driving loop: one observation frame per tick, one intent per
// claimed vehicle per tick — IDM longitudinal + MOBIL lateral computed
// client-side from the resolved policy context, with the routing axis
// (destination param) and informational turn signals. Wire shape (ADR-0026,
// Config.IntentBatchOff): the tick's route-free intents collect in ego
// (observation slice) order and flush as TSIB batches after the loop — the
// batch goes out when the tick's intents are computed, the same moment the
// last per-vehicle v2 would have (no barrier, no deadline); a vehicle with
// a route update that tick rides ONE complete standalone v2 instead.
func (d *Driver) onObs(msg *nats.Msg) {
	obs, err := natsio.DecodeObs(msg.Data, d.types)
	if err != nil {
		return
	}
	d.lastObsTick.Store(obs.Tick)
	params := d.spec.Params
	present := map[uint64]bool{}
	routeBudget := d.cfg.RouteBudgetPerTick
	var batch []engine.Intent // the tick's route-free intents, ego order
	for i := range obs.Egos {
		ego := &obs.Egos[i]
		present[ego.ID] = true
		in := engine.Intent{VehicleID: ego.ID}
		if ego.Ctx != nil {
			in.Accel = ego.Ctx.DecideAccel()
			in.AccelSet = true
			lane := 0
			if ego.Cooldown == 0 {
				lane = ego.Ctx.DecideLaneChange(params, d.stream(ego.ID))
			}
			in.LaneDelta = lane
			in.SignalSet = true
			in.Signals = signalFor(lane)
		}
		d.mu.Lock()
		d.routeStep(&in, ego, &routeBudget)
		d.mu.Unlock()
		switch {
		case d.cfg.IntentBatchOff, in.RouteSet:
			// Batch-off mode is the pre-ADR-0026 stream verbatim. So are
			// route updates in either mode: ONE complete standalone v2
			// carries every axis of that vehicle's tick, and the vehicle is
			// omitted from the batch — splitting fixed axes into TSIB plus
			// route into v2 would create competing same-vehicle intents
			// under first-wins arbitration (ADR-0026).
			d.publishIntent(in)
		default:
			batch = append(batch, in)
		}
	}
	if len(batch) > 0 {
		d.flushBatch(obs.Tick, batch)
	}
	// Completion watermark: the tick's response (batch + any standalone v2)
	// is fully published at this point — stamp AFTER flushBatch/publishIntent,
	// never at the top of the callback.
	d.completedObsTick.Store(obs.Tick)
	// The observation's ego list is the authoritative claim view: dropped
	// members despawned or were released engine-side.
	d.mu.Lock()
	for vid := range d.fleet {
		if !present[vid] {
			delete(d.fleet, vid)
			delete(d.routed, vid)
			delete(d.wantRoute, vid)
			delete(d.wantLane, vid)
		}
	}
	idle := len(d.fleet) == 0
	d.mu.Unlock()
	if idle {
		// Liveness without control traffic: heartbeat so the engine's
		// silence budget does not detach an idle standby replica.
		_ = d.nc.Publish(natsio.SubjectCtlHeartbeat(d.cfg.Run, d.id), nil)
	}
}

// publishIntent sends one standalone per-vehicle v2 intent (the pre-ADR-0026
// wire shape, kept for batch-off mode and route-update ticks).
func (d *Driver) publishIntent(in engine.Intent) {
	if err := d.nc.Publish(natsio.SubjectCtlIntent(d.cfg.Run, d.id), natsio.EncodeIntent(in)); err == nil {
		d.sentIntents.Add(1)
	}
}

// flushBatch publishes the tick's collected intents as TSIB batches — one
// message per cadence tick per controller, split at natsio.TSIBMaxRecords
// (ADR-0026). Records stay in ego order, so engine-assigned seqs are
// monotonic exactly as for the v2 stream; the header tick is the source
// observation tick (informational only).
func (d *Driver) flushBatch(tick uint64, batch []engine.Intent) {
	subject := natsio.SubjectCtlIntent(d.cfg.Run, d.id)
	for len(batch) > 0 {
		n := min(len(batch), natsio.TSIBMaxRecords)
		if err := d.nc.PublishMsg(natsio.NewTSIBMsg(subject, tick, batch[:n])); err == nil {
			d.sentIntents.Add(uint64(n))
		}
		batch = batch[n:]
	}
}

// applyIntentEncodings is the capability fallback (ADR-0026 M4 addendum):
// the engine advertises the intent encodings it accepts in its hello reply
// — a pre-TSIB engine OMITS the field. When batching is on (the default)
// and "tsib" is not advertised, run the session in v2 mode (the
// IntentBatchOff path) with one log line. ("tsib" is the wire contract
// string, pinned by contracts/asyncapi.yaml; unexported in natsio.)
func (d *Driver) applyIntentEncodings(encs []string) {
	if d.cfg.IntentBatchOff || slices.Contains(encs, "tsib") {
		return
	}
	log.Printf("driver %s: engine does not advertise intent_encodings \"tsib\" (pre-TSIB engine); falling back to per-vehicle v2 intents for this session", d.id)
	d.cfg.IntentBatchOff = true
}

// routeStep advances ONE ego's routing axis under the per-tick budget
// (Config.RouteBudgetPerTick). The routing axis' destination parameter is
// persistent — sent once per vehicle; the kernel follows it at every
// multi-successor lane (engine/routing.go), degrading to the successor
// default when the destination is unknown or unreachable. The destination
// is the configured override, or — with ExitRouting — the vehicle's
// per-ID drawn exit lane (tried once either way: the pick is
// deterministic, retrying would not change it). Only the DRAW is metered:
// its first-touch reachability scan is O(network) per origin lane
// (destinations.go exitCache) — the cost that stalled big-city ticks when
// routing ran synchronously in the tick loop, and still worth metering in
// the callback path (a burst of first-touch scans delays every intent
// behind it); the re-send of an unconfirmed destination is a string copy.
// The budget freezes the draw's lane at first observation, so deferred
// vehicles may draw an exit unreachable from a lane they reached since —
// the kernel then degrades to default routing, exactly as for any
// unreachable destination (deterministic either way). A controller that
// fails BEFORE an unrouted vehicle's first draw lets the replacement draw
// from a later lane — a pre-existing limitation of lane-input draws, not
// of the budget; the route, once assigned, round-trips via the obs echo
// and is adopted (ADR-0008 §6). Over budget the
// ego is left unrouted and retried next observation (only the first-obs
// lane is remembered for determinism — unrouted-due-to-budget must not
// read as routed). Caller holds d.mu.
func (d *Driver) routeStep(in *engine.Intent, ego *natsio.ObsEgo, budget *int) {
	if d.routed[ego.ID] {
		return
	}
	// Failover adoption: a re-claimed vehicle may already carry the
	// persistent route its previous controller assigned (the obs frame
	// round-trips it) — keep it. Re-deriving from the current lane would
	// change the destination mid-trip (ADR-0008 §6: failover is
	// behaviorally invisible).
	want, ok := d.wantRoute[ego.ID]
	if !ok {
		want = d.cfg.Destination
		if want == "" {
			want = ego.Route
		}
		if want == "" && d.cfg.ExitRouting {
			// The draw's lane input is captured at FIRST observation, not
			// at budget admission: pickExit(seed, id, lane) must be a pure
			// function of the run, never of controller lag (ADR-0007/0008
			// failover replicas must draw the SAME exit — sol review).
			lane, seen := d.wantLane[ego.ID]
			if !seen {
				if d.wantLane == nil {
					d.wantLane = map[uint64]int{} // tests construct Driver directly
				}
				lane = ego.LaneIdx
				d.wantLane[ego.ID] = lane
			}
			if *budget <= 0 {
				return // past the per-tick routing budget — retry next obs
			}
			*budget--
			want, _ = pickExit(d.net, d.exitCache(), d.spec.Seed, ego.ID, lane)
		}
		d.wantRoute[ego.ID] = want
	}
	// Core NATS publish has no ack: re-send until the obs frame echoes the
	// destination — a dropped one-shot Route intent would leave the
	// vehicle on default routing for life. Mark routed only when the
	// engine confirms (or nothing is wanted).
	if want == "" || ego.Route == want {
		d.routed[ego.ID] = true
		delete(d.wantRoute, ego.ID)
		delete(d.wantLane, ego.ID)
	} else {
		in.RouteSet = true
		in.Route = want
	}
}

// signalFor maps a lane decision to the informational signal axis.
func signalFor(laneDelta int) int {
	switch {
	case laneDelta > 0:
		return 1 // left
	case laneDelta < 0:
		return 2 // right
	default:
		return 0
	}
}

// exitCache lazily memoizes pickExit's O(network) inputs — the candidate
// list and per-lane reachability — under d.mu. The network is immutable
// for the run, and vehicles are first observed on a small set of origin
// lanes, so the memo hit rate is near-total.
func (d *Driver) exitCache() *exitCache {
	if d.exits == nil {
		d.exits = newExitCache()
	}
	return d.exits
}

// onSnap discovers claimable vehicles: the snapshot is the complete id list
// — the self-healing backstop that makes event loss harmless.
func (d *Driver) onSnap(msg *nats.Msg) {
	f, err := natsio.ParseFrame(msg.Data)
	if err != nil {
		return
	}
	ids := make([]uint64, 0, len(f.Vehicles))
	for _, v := range f.Vehicles {
		ids = append(ids, v.ID)
	}
	d.tryClaim(ids)
}

// onUnclaimed reacts to the engine's unclaimed-vehicle events (orphans from
// disconnects and releases; newly spawned vehicles) — the low-latency
// re-claim path (ADR-0008 §6).
func (d *Driver) onUnclaimed(msg *nats.Msg) {
	var evt natsio.ContractEvent
	if err := json.Unmarshal(msg.Data, &evt); err != nil {
		return
	}
	d.tryClaim(evt.VehicleIDs)
}

// tryClaim requests exclusive claims up to remaining capacity. In-flight
// requests dedupe; the snapshot diff retries whatever fails.
func (d *Driver) tryClaim(ids []uint64) {
	d.mu.Lock()
	var ask []uint64
	for _, vid := range ids {
		if d.fleet[vid] || d.pending[vid] {
			continue
		}
		if len(d.fleet)+len(d.pending) >= d.cfg.Capacity {
			break
		}
		d.pending[vid] = true
		ask = append(ask, vid)
	}
	d.mu.Unlock()
	if len(ask) == 0 {
		return
	}
	req, _ := json.Marshal(natsio.ClaimRequest{VehicleIDs: ask})
	resp, err := d.nc.Request(natsio.SubjectCtlClaim(d.cfg.Run, d.id), req, 2*time.Second)
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, vid := range ask {
		delete(d.pending, vid)
	}
	if err != nil {
		return // retried from the next snapshot
	}
	var rep natsio.ClaimReply
	if err := json.Unmarshal(resp.Data, &rep); err != nil {
		return
	}
	for _, vid := range rep.Granted {
		d.fleet[vid] = true
	}
}

// onIntrospect serves the introspection side channel: "given this vehicle
// state, what would you do?" — a pure function of state + policy. The query
// gets a FRESH per-vehicle stream (no side effects on live policy streams).
func (d *Driver) onIntrospect(msg *nats.Msg) {
	var req natsio.IntrospectRequest
	if err := json.Unmarshal(msg.Data, &req); err != nil || req.SchemaVersion != natsio.SchemaVersion {
		return
	}
	ctx, err := req.ToPolicyCtx(d.net, d.types)
	if err != nil {
		return
	}
	reply := natsio.IntrospectReply{
		SchemaVersion: natsio.SchemaVersion,
		Accel:         ctx.DecideAccel(),
	}
	if ctx.Cooldown == 0 {
		reply.LaneDelta = ctx.DecideLaneChange(d.spec.Params, engine.DeriveStream(d.spec.Seed, req.Vehicle.ID))
	}
	reply.Signals = signalFor(reply.LaneDelta)
	data, _ := json.Marshal(reply)
	_ = d.nc.Publish(msg.Reply, data)
}
