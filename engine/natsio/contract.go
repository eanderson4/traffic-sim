package natsio

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// contract.go — the ADR-0008 engine↔controller contract machinery, owned by
// the run loop (wire callbacks only buffer; all state changes happen on the
// run-loop goroutine between ticks):
//
//   - attach handshake (ts.{run}.ctl.hello, request/reply): contract
//     version, controller type, cadence, grants (drive/director/signal),
//     claim capacity, AoI window; engine answers accept/reject + assigned
//     controller_id.
//   - claim registry: exclusive per-vehicle claims, engine-arbitrated;
//     multi-vehicle claims allowed; claim/release/unclaimed events on
//     ts.{run}.ctl.events.*.
//   - hold-last (ADR-0008 §2): a missing expected message from an attached
//     controller re-issues its last intent for the claimed vehicle for ≤
//     HoldLastTicks ticks past the cadence due point, then zero/default.
//     Held re-issues are ordinary entries in the arbitrated log (flagged),
//     so replay reproduces them verbatim.
//   - liveness: an attached controller silent for DetachAfterTicks ticks is
//     detached; its claims release and unclaimed-vehicle events fire
//     (reason "disconnect"). Core NATS has no presence primitive, so the
//     timeout in tick space is the disconnect signal (v1). Paced runs only:
//     the sweep is skipped at PaceFloor == 0, where tick time outruns any
//     client's wall-clock reaction (see the sweep below and the ADR-0006
//     2026-07-24 addendum).
//   - pause gating (ADR-0008 §6): once a drive controller has attached, if
//     available claim capacity < demand (unclaimed vehicles) for
//     PauseAfterTicks consecutive ticks, the run pauses (dead wall-clock
//     time between ticks — invisible to tick determinism) and a pause event
//     lands on the record plane; resume on capacity recovery. A jammed run
//     never meets that resume condition (its active count does not drop),
//     so the gate is not silent: it logs a heartbeat every PauseLogEvery
//     while engaged, and after PauseEscapeAfter of persistent deficit it
//     resumes anyway with a loud log — an escape resume is distinguishable
//     on the record plane by demand > available. The gate re-arms once
//     capacity has actually recovered.

// ContractConfig tunes the contract layer; zero values take the defaults.
type ContractConfig struct {
	// PauseAfterTicks is the consecutive capacity-deficit count that gates
	// the tick loop (ADR-0008 §6; default 5).
	PauseAfterTicks uint64
	// DetachAfterTicks is the silence budget before an attached controller
	// is detached and its claims released (default 10).
	DetachAfterTicks uint64
	// HoldLastTicks is the message-loss healing window past the cadence due
	// point (default 2 — the ADR-0008 §2 "≤ 1–2 ticks").
	HoldLastTicks uint64
	// PaceFloor is the minimum wall time per tick (ADR-0005 §4: pacing is a
	// swappable driver around Tick). 0 = unpaced batch mode. This is never
	// a barrier on controller INPUT — intents apply whenever they arrive
	// and hold-last heals gaps — the engine simply does not outrun its
	// clients. Without it an unpaced loop spins at ~10⁴ ticks/s and even
	// in-process controllers lag dozens of ticks.
	PaceFloor time.Duration
	// PauseLogEvery is the heartbeat cadence while the pause gate is
	// engaged (default 10 s): each beat names demand, spare capacity, and
	// active vehicles. A gated run burns no CPU and publishes nothing —
	// without this a wedged run is indistinguishable from a hung process.
	PauseLogEvery time.Duration
	// PauseEscapeAfter is the maximum dwell before the gate escapes
	// (default 60 s): if demand still exceeds spare capacity, the run
	// resumes anyway with a loud log rather than wedge forever — a jam's
	// active count never drops, so the resume condition can be
	// unreachable. Unclaimed vehicles bridge on hold-last (ADR-0008 §6);
	// the gate re-arms once capacity has actually recovered.
	PauseEscapeAfter time.Duration
	// Log receives the pause-gate heartbeat and escape lines (nil →
	// stderr, matching the replay player).
	Log *log.Logger
	// Observer is an optional read-only per-tick observer on the run loop
	// (RunObserver — the M13 metric kernel). Nil = none. Loop-behavior
	// knobs ride this config (PaceFloor precedent); the observer is not
	// part of the controller contract.
	Observer RunObserver
	// StartGate, when non-nil, parks the tick loop at tick 0 — serving the
	// contract plane (hello/claim/release/heartbeat/verb) but never
	// Stepping — until the channel is closed. This is the embedded-client
	// attach barrier (serve): the run starts only after the in-process
	// driver/demand director have completed their attach handshakes and
	// their subscriptions are live, so tick 0's snapshot and unclaimed
	// pool reach every client and no early ticks are lost to attach
	// latency. Dead wall-clock time like the pause gate: sim time stays
	// frozen, invisible to tick determinism.
	StartGate <-chan struct{}
}

func (c ContractConfig) withDefaults() ContractConfig {
	if c.PauseAfterTicks == 0 {
		c.PauseAfterTicks = 5
	}
	if c.DetachAfterTicks == 0 {
		c.DetachAfterTicks = 10
	}
	if c.HoldLastTicks == 0 {
		c.HoldLastTicks = 2
	}
	if c.PauseLogEvery == 0 {
		c.PauseLogEvery = 10 * time.Second
	}
	if c.PauseEscapeAfter == 0 {
		c.PauseEscapeAfter = 60 * time.Second
	}
	if c.Log == nil {
		c.Log = log.New(os.Stderr, "", 0)
	}
	return c
}

// ObsWindow is the area-of-interest declaration of an attach (ADR-0008 §3).
type ObsWindow struct {
	RadiusM      float64 `json:"radius_m"`
	MaxNeighbors int     `json:"max_neighbors"`
	Features     uint16  `json:"features"` // ObsFeature* bitmask
}

// HelloRequest is the attach handshake payload (ts.{run}.ctl.hello).
type HelloRequest struct {
	ContractVersion int       `json:"contract_version"`
	ControllerType  string    `json:"controller_type"` // ai | scripted | human | default-driver
	CadenceTicks    uint64    `json:"cadence_ticks"`   // decision cadence in ticks (0 → 1)
	Grants          []string  `json:"grants"`          // drive | director | signal
	ClaimCapacity   int       `json:"claim_capacity"`  // max vehicles this controller will hold (drive)
	Claims          []uint64  `json:"claims,omitempty"`
	Window          ObsWindow `json:"obs_window"`
}

// HelloReply is the engine's handshake answer: accept/reject + the assigned
// controller_id (and the claims granted at attach).
type HelloReply struct {
	Accepted        bool     `json:"accepted"`
	ControllerID    string   `json:"controller_id,omitempty"`
	Grants          []string `json:"granted_grants,omitempty"`
	Claims          []uint64 `json:"claims,omitempty"`
	ContractVersion int      `json:"contract_version"`
	Run             string   `json:"run"`
	Tick            uint64   `json:"tick"`
	Reason          string   `json:"reason,omitempty"`
}

// ClaimRequest asks for exclusive claims on vehicles (ts.{run}.ctl.claim.{id}).
type ClaimRequest struct {
	VehicleIDs []uint64 `json:"vehicle_ids"`
}

// ClaimReply reports per-vehicle arbitration results.
type ClaimReply struct {
	Granted  []uint64 `json:"granted"`
	Rejected []uint64 `json:"rejected,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// ReleaseRequest drops claims (ts.{run}.ctl.release.{id}); Detach also ends
// the attachment (the graceful form of disconnect).
type ReleaseRequest struct {
	VehicleIDs []uint64 `json:"vehicle_ids"`
	Detach     bool     `json:"detach,omitempty"`
}

// ContractEvent is the JSON payload of every contract event feed message
// (ts.{run}.ctl.events.*) and of record-plane events (ts.{run}.log.event).
type ContractEvent struct {
	Type         string   `json:"type"` // claim | release | unclaimed | pause | resume
	Tick         uint64   `json:"tick"`
	ControllerID string   `json:"controller_id,omitempty"`
	VehicleIDs   []uint64 `json:"vehicle_ids,omitempty"`
	Reason       string   `json:"reason,omitempty"` // spawn | release | disconnect
	Demand       int      `json:"demand,omitempty"`
	Available    int      `json:"available,omitempty"`
}

// Event types and reasons.
const (
	EventClaim     = "claim"
	EventRelease   = "release"
	EventUnclaimed = "unclaimed"
	EventPause     = "pause"
	EventResume    = "resume"

	ReasonSpawn      = "spawn"
	ReasonRelease    = "release"
	ReasonDisconnect = "disconnect"
)

// controller is one attached controller's registry entry.
type controller struct {
	id       string
	ctype    string
	cadence  uint64
	grant    uint8 // highest grant level (tie-break order)
	grants   map[string]bool
	capacity int
	window   ObsWindow
	claims   map[uint64]bool
	lastSeen uint64 // tick of last wire activity (liveness)
	seq      uint64
}

// holdState is the per-vehicle hold-last bookkeeping: the last fresh intent
// and its application tick. Synthesized re-issues continue the cadence
// window; expiry means zero/default (ADR-0008 §2).
type holdState struct {
	intent    engine.Intent
	lastFresh uint64
	cadence   uint64
	grant     uint8
	ctrlID    string // owning controller; "" once orphaned (disconnect)
	seq       uint64 // synthesized re-issue sequence (orphan path)
}

// ctlReq is a buffered wire request (callbacks → run loop).
type ctlReq struct {
	kind    int // reqHello | reqClaim | reqRelease | reqHeartbeat | reqVerb
	ctl     string
	reply   string
	payload []byte
}

const (
	reqHello = iota
	reqClaim
	reqRelease
	reqHeartbeat
	reqVerb
)

// Contract is the engine side of the ADR-0008 contract.
type Contract struct {
	nc  *nats.Conn
	run string
	cfg ContractConfig
	bus *Bus
	rec *Recorder

	mu   sync.Mutex
	reqs []ctlReq

	ctrls      map[string]*controller
	byVehicle  map[uint64]*controller
	hold       map[uint64]holdState
	legacySeqs map[string]uint64
	announced  map[uint64]bool
	verbs      map[string]VerbReply // director request_id → reply (dedup; lookup only)
	nextID     int
	driveSeen  bool // pause gating arms once a drive controller has attached
	deficit    uint64
	paused     bool
	pausedAt   time.Time // gate engagement (heartbeat/escape dwell clock)
	pauseLogAt time.Time // last heartbeat
	escaped    bool      // gate escaped; re-arms when capacity recovers

	subs []*nats.Subscription

	claimViolations atomic.Uint64 // intents dropped: claim required
	eventsPub       atomic.Uint64
	obsOut          atomic.Uint64
}

// NewContract attaches the contract plane for a run: subscriptions for the
// hello handshake, claim/release, and heartbeats. The recorder is used for
// record-plane pause/resume events (ADR-0008 §6).
func NewContract(nc *nats.Conn, run string, cfg ContractConfig, bus *Bus, rec *Recorder) (*Contract, error) {
	c := &Contract{
		nc:         nc,
		run:        run,
		cfg:        cfg.withDefaults(),
		bus:        bus,
		rec:        rec,
		ctrls:      map[string]*controller{},
		byVehicle:  map[uint64]*controller{},
		hold:       map[uint64]holdState{},
		legacySeqs: map[string]uint64{},
		announced:  map[uint64]bool{},
		verbs:      map[string]VerbReply{},
	}
	bind := func(subject string, kind int, parseCtl bool) error {
		sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
			r := ctlReq{kind: kind, reply: msg.Reply, payload: msg.Data}
			if parseCtl {
				tokens := strings.Split(msg.Subject, ".")
				if len(tokens) != intentSubjectTokens {
					return
				}
				r.ctl = tokens[4]
			}
			c.mu.Lock()
			c.reqs = append(c.reqs, r)
			c.mu.Unlock()
		})
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", subject, err)
		}
		c.subs = append(c.subs, sub)
		return nil
	}
	if err := bind(SubjectCtlHello(run), reqHello, false); err != nil {
		return nil, err
	}
	if err := bind(SubjectCtlClaimAll(run), reqClaim, true); err != nil {
		return nil, err
	}
	if err := bind(SubjectCtlReleaseAll(run), reqRelease, true); err != nil {
		return nil, err
	}
	if err := bind(SubjectCtlHeartbeatAll(run), reqHeartbeat, true); err != nil {
		return nil, err
	}
	if err := bind(SubjectCtlVerbAll(run), reqVerb, true); err != nil {
		return nil, err
	}
	return c, nil
}

// Close detaches the contract subscriptions.
func (c *Contract) Close() {
	for _, sub := range c.subs {
		_ = sub.Unsubscribe()
	}
}

// Paused reports whether the tick loop is currently gated (ADR-0008 §6).
func (c *Contract) Paused() bool { return c.paused }

// DriveControllers counts attached controllers holding the drive grant
// (pacing engages only while controllers exist to keep up with).
func (c *Contract) DriveControllers() int { return c.driveCtlCount() }

// PaceFloor returns the configured minimum wall time per tick (0 = unpaced).
func (c *Contract) PaceFloor() time.Duration { return c.cfg.PaceFloor }

// Stats reports contract counters (observability, not world state).
func (c *Contract) Stats() (claimViolations, events, observations uint64) {
	return c.claimViolations.Load(), c.eventsPub.Load(), c.obsOut.Load()
}

// ProcessControl handles everything the wire brought in since the last
// call — hellos, claims, releases, heartbeats — then runs the liveness
// sweep and, while paused, the resume check. Run-loop only.
func (c *Contract) ProcessControl(e *engine.Engine) error {
	c.mu.Lock()
	reqs := c.reqs
	c.reqs = nil
	c.mu.Unlock()

	for _, r := range reqs {
		switch r.kind {
		case reqHello:
			c.handleHello(e, r)
		case reqClaim:
			c.handleClaim(e, r)
		case reqRelease:
			c.handleRelease(e, r)
		case reqHeartbeat:
			if ctl, ok := c.ctrls[r.ctl]; ok {
				ctl.lastSeen = e.Tick
			}
		case reqVerb:
			c.handleVerb(e, r)
		}
	}

	// Liveness sweep: silence past the budget detaches the controller and
	// releases its claims (the v1 disconnect signal — core NATS has no
	// presence primitive). Skipped in batch mode (PaceFloor == 0): unpaced,
	// tick time outruns any client's wall-clock reaction, so the tick-space
	// silence budget would detach healthy clients — and with no drive
	// controller left to restore capacity, the pause gate then wedges the
	// run for good. Batch mode is for offline runs with embedded clients;
	// wire liveness assumes pacing keeps tick time near wall time.
	if len(c.ctrls) > 0 && c.cfg.PaceFloor > 0 {
		ids := make([]string, 0, len(c.ctrls))
		for id := range c.ctrls {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ctl := c.ctrls[id]
			if e.Tick > ctl.lastSeen && e.Tick-ctl.lastSeen > c.cfg.DetachAfterTicks {
				c.detach(e, ctl, ReasonDisconnect)
			}
		}
	}

	// Resume check: capacity recovered while gated? Otherwise heartbeat,
	// and escape the gate once the deficit has persisted past the dwell —
	// a jam's active count never drops, so capacity recovery can be
	// unreachable and the run would wedge silently.
	if c.paused {
		demand, avail := c.capacity(e)
		if demand <= avail {
			c.paused = false
			if err := c.emitPause(e, EventResume, demand, avail); err != nil {
				return err
			}
		} else {
			now := time.Now()
			if now.Sub(c.pauseLogAt) >= c.cfg.PauseLogEvery {
				c.pauseLogAt = now
				c.cfg.Log.Printf("pause gate: run %q gated at tick %d: demand=%d unclaimed, spare capacity=%d, active vehicles=%d — waiting for drive capacity",
					c.run, e.Tick, demand, avail, len(e.Vehicles()))
			}
			if now.Sub(c.pausedAt) >= c.cfg.PauseEscapeAfter {
				c.paused = false
				c.escaped = true
				c.cfg.Log.Printf("pause gate: run %q escape after %s at tick %d: demand=%d > spare=%d persistently — resuming anyway, unclaimed vehicles bridge on hold-last (ADR-0008 §6)",
					c.run, now.Sub(c.pausedAt).Round(time.Millisecond), e.Tick, demand, avail)
				if err := c.emitPause(e, EventResume, demand, avail); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// handleHello answers one attach handshake.
func (c *Contract) handleHello(e *engine.Engine, r ctlReq) {
	var req HelloRequest
	reply := HelloReply{ContractVersion: SchemaVersion, Run: c.run, Tick: e.Tick}
	defer func() {
		data, _ := json.Marshal(reply)
		if r.reply != "" {
			_ = c.nc.Publish(r.reply, data)
		}
	}()
	if err := json.Unmarshal(r.payload, &req); err != nil {
		reply.Reason = fmt.Sprintf("bad hello payload: %v", err)
		return
	}
	if req.ContractVersion != SchemaVersion {
		reply.Reason = fmt.Sprintf("contract_version %d, engine speaks %d", req.ContractVersion, SchemaVersion)
		return
	}
	cadence := req.CadenceTicks
	if cadence == 0 {
		cadence = 1
	}
	if cadence >= c.cfg.DetachAfterTicks {
		reply.Reason = fmt.Sprintf("cadence_ticks %d must be < detach budget %d", cadence, c.cfg.DetachAfterTicks)
		return
	}
	grants := map[string]bool{}
	grantLevel := uint8(engine.GrantNone)
	for _, g := range req.Grants {
		switch g {
		case "drive":
			grants[g] = true
			if grantLevel < engine.GrantDrive {
				grantLevel = engine.GrantDrive
			}
		case "signal":
			grants[g] = true
			if grantLevel < engine.GrantSignal {
				grantLevel = engine.GrantSignal
			}
		case "director":
			grants[g] = true
			grantLevel = engine.GrantDirector
		default:
			reply.Reason = fmt.Sprintf("unknown grant %q", g)
			return
		}
	}
	if len(grants) == 0 {
		reply.Reason = "no grants requested"
		return
	}
	if grants["drive"] && req.ClaimCapacity < 1 {
		reply.Reason = "drive grant requires claim_capacity ≥ 1"
		return
	}

	c.nextID++
	ctl := &controller{
		id:       fmt.Sprintf("ctl-%d", c.nextID),
		ctype:    req.ControllerType,
		cadence:  cadence,
		grant:    grantLevel,
		grants:   grants,
		capacity: req.ClaimCapacity,
		window:   req.Window,
		claims:   map[uint64]bool{},
		lastSeen: e.Tick,
	}
	c.ctrls[ctl.id] = ctl
	if grants["drive"] {
		c.driveSeen = true
	}
	reply.Accepted = true
	reply.ControllerID = ctl.id
	for g := range grants {
		reply.Grants = append(reply.Grants, g)
	}
	sort.Strings(reply.Grants)

	// Claims requested at attach: exclusive arbitration, best effort.
	for _, vid := range req.Claims {
		if c.tryClaim(e, ctl, vid) {
			reply.Claims = append(reply.Claims, vid)
		}
	}
	if len(reply.Claims) > 0 {
		sort.Slice(reply.Claims, func(i, j int) bool { return reply.Claims[i] < reply.Claims[j] })
		c.publishEvent(SubjectEventClaim(c.run), ContractEvent{
			Type: EventClaim, Tick: e.Tick, ControllerID: ctl.id, VehicleIDs: reply.Claims,
		})
	}
}

// tryClaim grants vid to ctl iff it is live and unclaimed (or already ctl's).
func (c *Contract) tryClaim(e *engine.Engine, ctl *controller, vid uint64) bool {
	owner, claimed := c.byVehicle[vid]
	if claimed && owner != ctl {
		return false
	}
	if e.VehicleByID(vid) == nil {
		return false
	}
	if !claimed {
		c.byVehicle[vid] = ctl
		ctl.claims[vid] = true
		delete(c.announced, vid)
		// A re-claimed orphan's hold window keeps bridging with the new
		// owner's cadence until its first fresh intent arrives.
		if h, ok := c.hold[vid]; ok {
			h.ctrlID = ctl.id
			h.cadence = ctl.cadence
			h.grant = ctl.grant
			h.seq = 0
			c.hold[vid] = h
		}
	}
	return true
}

// handleClaim arbitrates one claim request batch.
func (c *Contract) handleClaim(e *engine.Engine, r ctlReq) {
	ctl, ok := c.ctrls[r.ctl]
	var reply ClaimReply
	defer func() {
		data, _ := json.Marshal(reply)
		if r.reply != "" {
			_ = c.nc.Publish(r.reply, data)
		}
	}()
	if !ok {
		reply.Reason = "not attached"
		return
	}
	ctl.lastSeen = e.Tick
	if !ctl.grants["drive"] {
		reply.Reason = "claim requires the drive grant"
		return
	}
	var req ClaimRequest
	if err := json.Unmarshal(r.payload, &req); err != nil {
		reply.Reason = fmt.Sprintf("bad claim payload: %v", err)
		return
	}
	for _, vid := range req.VehicleIDs {
		if c.tryClaim(e, ctl, vid) {
			reply.Granted = append(reply.Granted, vid)
		} else {
			reply.Rejected = append(reply.Rejected, vid)
		}
	}
	if len(reply.Granted) > 0 {
		sort.Slice(reply.Granted, func(i, j int) bool { return reply.Granted[i] < reply.Granted[j] })
		c.publishEvent(SubjectEventClaim(c.run), ContractEvent{
			Type: EventClaim, Tick: e.Tick, ControllerID: ctl.id, VehicleIDs: reply.Granted,
		})
	}
}

// handleRelease drops the listed claims (and optionally detaches).
func (c *Contract) handleRelease(e *engine.Engine, r ctlReq) {
	ctl, ok := c.ctrls[r.ctl]
	if !ok {
		return
	}
	ctl.lastSeen = e.Tick
	var req ReleaseRequest
	if err := json.Unmarshal(r.payload, &req); err != nil {
		return
	}
	var released []uint64
	for _, vid := range req.VehicleIDs {
		if c.byVehicle[vid] == ctl {
			released = append(released, vid)
			c.releaseOne(ctl, vid)
		}
	}
	if len(released) > 0 {
		sort.Slice(released, func(i, j int) bool { return released[i] < released[j] })
		c.publishEvent(SubjectEventRelease(c.run), ContractEvent{
			Type: EventRelease, Tick: e.Tick, ControllerID: ctl.id, VehicleIDs: released, Reason: ReasonRelease,
		})
		c.publishEvent(SubjectEventUnclaimed(c.run), ContractEvent{
			Type: EventUnclaimed, Tick: e.Tick, ControllerID: ctl.id, VehicleIDs: released, Reason: ReasonRelease,
		})
		for _, vid := range released {
			c.announced[vid] = true
		}
	}
	if req.Detach {
		c.detach(e, ctl, ReasonDisconnect)
	}
}

// handleVerb answers one director verb (ts.{run}.ctl.verb.{id},
// request/reply). The director grant is required; v1 implements "spawn".
// Dedup is by the director-assigned request_id: the first-seen reply is
// remembered and replayed (Duplicate set) for any retry, so a retried verb
// never double-spawns. Accepted verbs are validated and buffered by the
// kernel (engine.EnqueueSpawn) and take effect at the next tick boundary;
// the record plane logs them with that applied_tick.
func (c *Contract) handleVerb(e *engine.Engine, r ctlReq) {
	var reply VerbReply
	defer func() {
		data, _ := json.Marshal(reply)
		if r.reply != "" {
			_ = c.nc.Publish(r.reply, data)
		}
	}()
	// Parse first so every rejection still echoes verb/request_id (the
	// director correlates replies by id, including "not attached" ones).
	var req VerbRequest
	if err := json.Unmarshal(r.payload, &req); err != nil {
		reply.Reason = fmt.Sprintf("bad verb payload: %v", err)
		return
	}
	reply.Verb, reply.RequestID = req.Verb, req.RequestID
	ctl, ok := c.ctrls[r.ctl]
	if !ok {
		reply.Reason = "not attached"
		return
	}
	ctl.lastSeen = e.Tick
	if !ctl.grants["director"] {
		reply.Reason = "verb requires the director grant"
		return
	}
	if req.Verb != "spawn" {
		reply.Reason = fmt.Sprintf("unsupported verb %q (v1: spawn)", req.Verb)
		return
	}
	if req.RequestID == "" {
		reply.Reason = "missing request_id"
		return
	}
	if prev, dup := c.verbs[req.RequestID]; dup {
		reply = prev
		reply.Duplicate = true
		return
	}
	err := e.EnqueueSpawn(engine.SpawnDirective{
		RequestID:    req.RequestID,
		Origin:       req.Origin,
		TypeName:     req.VType,
		EarliestTick: req.EarliestTick,
	})
	if err != nil {
		reply.Reason = err.Error()
	} else {
		reply.Accepted = true
	}
	c.verbs[req.RequestID] = reply
}

// releaseOne drops one claim without emitting events (callers batch them).
func (c *Contract) releaseOne(ctl *controller, vid uint64) {
	delete(c.byVehicle, vid)
	delete(ctl.claims, vid)
	// The orphan keeps its hold-last bridge (ADR-0008 §6) until the window
	// expires or a new owner re-claims it.
	if h, ok := c.hold[vid]; ok {
		h.ctrlID = ""
		c.hold[vid] = h
	}
}

// detach ends an attachment: every claim releases, unclaimed events fire.
func (c *Contract) detach(e *engine.Engine, ctl *controller, reason string) {
	if _, ok := c.ctrls[ctl.id]; !ok {
		return
	}
	delete(c.ctrls, ctl.id)
	var ids []uint64
	for vid := range ctl.claims {
		ids = append(ids, vid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, vid := range ids {
		c.releaseOne(ctl, vid)
		c.announced[vid] = true
	}
	if len(ids) > 0 {
		c.publishEvent(SubjectEventRelease(c.run), ContractEvent{
			Type: EventRelease, Tick: e.Tick, ControllerID: ctl.id, VehicleIDs: ids, Reason: reason,
		})
		c.publishEvent(SubjectEventUnclaimed(c.run), ContractEvent{
			Type: EventUnclaimed, Tick: e.Tick, ControllerID: ctl.id, VehicleIDs: ids, Reason: reason,
		})
	}
}

// DrainIntents drains the wire buffer and returns the keyed intents for the
// next tick boundary: claim-filtered, sequenced, grant-stamped, and extended
// with hold-last re-issues (ADR-0008 §2). Run-loop only.
func (c *Contract) DrainIntents(e *engine.Engine) []engine.KeyedIntent {
	arrivals := c.bus.DrainIntents()
	out := make([]engine.KeyedIntent, 0, len(arrivals))
	fresh := map[uint64]bool{}

	for _, a := range arrivals {
		ctl := c.ctrls[a.Controller]
		if ctl == nil {
			// Legacy unattached publisher (M3 compat): honored only while the
			// target vehicle is unclaimed — claims are exclusive.
			if c.byVehicle[a.Intent.VehicleID] != nil {
				c.claimViolations.Add(1)
				continue
			}
			c.legacySeqs[a.Controller]++
			out = append(out, engine.KeyedIntent{
				Controller: a.Controller, Seq: c.legacySeqs[a.Controller],
				Grant: engine.GrantNone, Intent: a.Intent,
			})
			continue
		}
		ctl.lastSeen = e.Tick
		vid := a.Intent.VehicleID
		switch {
		case ctl.grants["drive"] && c.byVehicle[vid] == ctl:
			// the claim holder's intent for its own vehicle
		case ctl.grant >= engine.GrantDirector:
			// privileged roles pass unfiltered (director verbs land M5+)
		default:
			c.claimViolations.Add(1)
			continue
		}
		ctl.seq++
		out = append(out, engine.KeyedIntent{
			Controller: ctl.id, Seq: ctl.seq, Grant: ctl.grant, Intent: a.Intent,
		})
		if ctl.grants["drive"] && c.byVehicle[vid] == ctl {
			fresh[vid] = true
			c.hold[vid] = holdState{
				intent: a.Intent, lastFresh: e.Tick, cadence: ctl.cadence,
				grant: ctl.grant, ctrlID: ctl.id,
			}
		}
	}

	// Hold-last + cadence persistence: for every tracked vehicle with no
	// fresh intent this drain, re-issue the last intent while inside the
	// window (cadence−1 design-persistence ticks + HoldLastTicks loss
	// healing), then expire to zero/default (ADR-0008 §2).
	if len(c.hold) > 0 {
		ids := make([]uint64, 0, len(c.hold))
		for vid := range c.hold {
			ids = append(ids, vid)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, vid := range ids {
			if fresh[vid] {
				continue
			}
			h := c.hold[vid]
			window := (h.cadence - 1) + c.cfg.HoldLastTicks
			if e.Tick <= h.lastFresh || e.Tick-h.lastFresh > window {
				delete(c.hold, vid)
				continue
			}
			// The vehicle must still be live to matter; despawn cleanup runs
			// in AfterStep, so a stale entry here is harmless but wasted.
			ctl := c.ctrls[h.ctrlID]
			var seq uint64
			if ctl != nil {
				ctl.seq++
				seq = ctl.seq
			} else {
				h.seq++
				seq = h.seq
				c.hold[vid] = h
			}
			out = append(out, engine.KeyedIntent{
				Controller: h.ctrlID, Seq: seq, Grant: h.grant, Held: true, Intent: h.intent,
			})
		}
	}
	return out
}

// AfterStep publishes per-controller observations, sweeps spawn/despawn
// claim bookkeeping, announces newly unclaimed vehicles, and evaluates the
// pause gate. Run-loop only, after the tick's record is safe to write.
func (c *Contract) AfterStep(e *engine.Engine) error {
	// Observations: every attached drive controller gets a frame per tick —
	// it is the controller's tick clock even with an empty fleet.
	if len(c.ctrls) > 0 {
		ids := make([]string, 0, len(c.ctrls))
		for id := range c.ctrls {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			ctl := c.ctrls[id]
			if !ctl.grants["drive"] {
				continue
			}
			c.publishObs(e, ctl)
		}
	}

	// Despawn sweep: dead vehicles drop out of the registry silently.
	live := map[uint64]bool{}
	for _, v := range e.Vehicles() {
		live[v.ID] = true
	}
	for vid, ctl := range c.byVehicle {
		if !live[vid] {
			delete(c.byVehicle, vid)
			delete(ctl.claims, vid)
			delete(c.hold, vid)
			delete(c.announced, vid)
		}
	}

	// Newly unclaimed vehicles (spawns): announce once; controllers also
	// have the snapshot diff as their backstop (core NATS is at-most-once).
	if c.driveCtlCount() > 0 {
		var newUnc []uint64
		for _, v := range e.Vehicles() {
			if c.byVehicle[v.ID] == nil && !c.announced[v.ID] {
				newUnc = append(newUnc, v.ID)
				c.announced[v.ID] = true
			}
		}
		if len(newUnc) > 0 {
			c.publishEvent(SubjectEventUnclaimed(c.run), ContractEvent{
				Type: EventUnclaimed, Tick: e.Tick, VehicleIDs: newUnc, Reason: ReasonSpawn,
			})
		}
	}

	// Pause gate: available claim capacity < demand for PauseAfterTicks
	// consecutive ticks gates the loop; hold-last bridges the grace window.
	// After an escape the gate stays open until capacity has recovered at
	// least once — re-engaging on an unchanged deficit would just dwell
	// and escape again.
	if c.driveSeen && !c.paused {
		demand, avail := c.capacity(e)
		if demand > avail {
			c.deficit++
		} else {
			c.deficit = 0
			c.escaped = false
		}
		if !c.escaped && c.deficit >= c.cfg.PauseAfterTicks {
			c.paused = true
			c.deficit = 0
			c.pausedAt = time.Now()
			c.pauseLogAt = c.pausedAt
			if err := c.emitPause(e, EventPause, demand, avail); err != nil {
				return err
			}
		}
	}
	return nil
}

// driveCtlCount counts attached controllers holding the drive grant.
func (c *Contract) driveCtlCount() int {
	n := 0
	for _, ctl := range c.ctrls {
		if ctl.grants["drive"] {
			n++
		}
	}
	return n
}

// capacity measures the pause-gate quantities: demand = live unclaimed
// vehicles; available = spare claim capacity across attached drive
// controllers (ADR-0008 §6).
func (c *Contract) capacity(e *engine.Engine) (demand, available int) {
	for _, v := range e.Vehicles() {
		if c.byVehicle[v.ID] == nil {
			demand++
		}
	}
	for _, ctl := range c.ctrls {
		if !ctl.grants["drive"] {
			continue
		}
		if spare := ctl.capacity - len(ctl.claims); spare > 0 {
			available += spare
		}
	}
	return demand, available
}

// publishObs builds and publishes one controller's observation frame.
func (c *Contract) publishObs(e *engine.Engine, ctl *controller) {
	ids := make([]uint64, 0, len(ctl.claims))
	for vid := range ctl.claims {
		ids = append(ids, vid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	egos := make([]ObsEgo, 0, len(ids))
	for _, vid := range ids {
		v := e.VehicleByID(vid)
		if v == nil {
			continue // despawned this tick
		}
		ego := ObsEgo{
			ID: v.ID, LaneIdx: v.Lane.Index, TypeIdx: v.TypeIdx,
			S: v.S, V: v.V, F: v.F, Acc: v.Acc,
			Cooldown: v.Cooldown, Signals: v.Signals, HeldTurn: v.HeldTurn,
			Cruise: v.Cruise, CruiseOK: v.CruiseOK, Route: v.Route,
		}
		if ctl.window.Features&ObsFeaturePolicyCtx != 0 {
			ego.Ctx = e.PolicyContext(v)
		}
		egos = append(egos, ego)
	}

	var nbs []ObsNeighbor
	if ctl.window.MaxNeighbors > 0 && len(egos) > 0 && ctl.window.RadiusM > 0 {
		nbs = c.neighborList(e, ctl, egos)
	}

	msg := nats.NewMsg(SubjectCtlObs(c.run, ctl.id))
	msg.Data = EncodeObs(e.Tick, ctl.window.Features, egos, nbs)
	msg.Header.Set(headerTick, fmt.Sprintf("%d", e.Tick))
	msg.Header.Set(headerSchemaVersion, fmt.Sprintf("%d", SchemaVersion))
	if err := c.nc.PublishMsg(msg); err == nil {
		c.obsOut.Add(1)
	}
}

// neighborList builds the sorted/capped raw neighbor list: vehicles not
// claimed by ctl within RadiusM of any of its claimed egos, distance measured
// on the placeholder projection (chain offset × 3.5 m lateral slot — the
// same frame the snapshot uses), sorted by (distance, id), capped.
func (c *Contract) neighborList(e *engine.Engine, ctl *controller, egos []ObsEgo) []ObsNeighbor {
	type cand struct {
		nb   ObsNeighbor
		dist float64
	}
	var cands []cand
	r2 := ctl.window.RadiusM * ctl.window.RadiusM
	for _, w := range e.Vehicles() {
		if ctl.claims[w.ID] {
			continue
		}
		g := c.bus.geoms[w.Lane.Index]
		wx, wy := g.XOff+w.S, g.Y
		best := math.Inf(1)
		for _, ev := range e.Vehicles() {
			if !ctl.claims[ev.ID] {
				continue
			}
			eg := c.bus.geoms[ev.Lane.Index]
			dx, dy := (eg.XOff+ev.S)-wx, eg.Y-wy
			if d := dx*dx + dy*dy; d < best {
				best = d
			}
		}
		if best <= r2 {
			cands = append(cands, cand{ObsNeighbor{
				ID: w.ID, LaneIdx: w.Lane.Index, TypeIdx: w.TypeIdx,
				S: w.S, V: w.V, F: w.F, Signals: w.Signals,
			}, best})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].dist != cands[j].dist {
			return cands[i].dist < cands[j].dist
		}
		return cands[i].nb.ID < cands[j].nb.ID
	})
	if len(cands) > ctl.window.MaxNeighbors {
		cands = cands[:ctl.window.MaxNeighbors]
	}
	out := make([]ObsNeighbor, len(cands))
	for i := range cands {
		out[i] = cands[i].nb
	}
	return out
}

// publishEvent emits a contract event on the live event feed (best effort).
func (c *Contract) publishEvent(subject string, evt ContractEvent) {
	data, _ := json.Marshal(evt)
	msg := nats.NewMsg(subject)
	msg.Data = data
	msg.Header.Set(headerTick, fmt.Sprintf("%d", evt.Tick))
	msg.Header.Set(headerSchemaVersion, fmt.Sprintf("%d", SchemaVersion))
	if err := c.nc.PublishMsg(msg); err == nil {
		c.eventsPub.Add(1)
	}
}

// emitPause records a pause/resume event on the RECORD plane (ADR-0008 §6 —
// pause/resume are recorded and invisible to tick determinism) and mirrors
// it on the live event feed. A failed record write aborts the run loudly
// (ADR-0006 §4), hence the error return.
func (c *Contract) emitPause(e *engine.Engine, kind string, demand, avail int) error {
	evt := ContractEvent{Type: kind, Tick: e.Tick, Demand: demand, Available: avail}
	data, _ := json.Marshal(evt)
	if err := c.rec.LogEvent(e.Tick, data); err != nil {
		return fmt.Errorf("record %s event: %w", kind, err)
	}
	c.publishEvent(SubjectEventPause(c.run), evt)
	return nil
}
