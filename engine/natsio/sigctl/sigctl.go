// Package sigctl is the reference actuated signal controller (ADR-0037
// milestone 2): a NATS client that proves the signal_set channel by
// running gap-out actuation against it. It consumes the live plane only —
// the TSSG program table (ts.{run}.state.sig), TSSF snapshots
// (ts.{run}.state.snap), and static scenario geometry — and issues
// signal_set verbs on ts.{run}.ctl.verb.{controller_id} with deterministic
// request ids, exactly like any external client could.
//
// DETECTORS. The snapshot frame carries (id, x, y, angle, class) per
// vehicle — no lane, no s — so approach presence is read the way a
// physical detector reads it: a fixed zone in space. The zone is a
// DetectRadiusM circle around each signal link's stop line (the start of
// the signal-bound internal lane's centerline, from the network file —
// static scenario content, the same bytes the viz renders). Dynamic
// structure (programs, phases, link→lane binding) arrives over the wire;
// only the geometry comes from the file.
//
// CONTROL. Phases are classified movement vs transition: anything
// containing an amber char (even mixed green/amber — one exists in
// chi-loop-urban) or showing no green at all is a transition, never a
// serve or hold target. Per program, per decision cadence: an
// uncontested serving phase is extended; a conflicting call caps the
// extension at max-green-on-call; a gap with a call waiting switches
// after the minimum green. A switch is never a jump: the controller
// walks the program's table order from the current phase to the target,
// commanding every intermediate phase at its NATURAL duration, so the
// program's yellow/all-red intervals are actually simulated (free
// clearance time would inflate the bracket measurement). The kernel's
// starvation chain (M1) gives actuation its hard max-green backstop:
// same-phase renewals accumulate toward the 300 s chain bound, and the
// controller tracks its own verb history, so it computes that bound
// LOCALLY and switches or releases instead of issuing a renewal the rail
// would decline. The controller never relies on the rail to arbitrate
// same-boundary verb sequences — one verb per program per boundary.
//
// TIMING PRECONDITION (round-1 review): self-tracking assumes
// NEXT-BOUNDARY application — a verb sent at snapshot tick T is tracked
// as applied at T+1. Nothing on the wire guarantees that: the verb
// applies at whatever tick the engine's next control drain lands on,
// which paced runs make the next boundary but pace-0 batch runs (the
// bracket's mode) can lag. The skew is conservative — the predicted
// holdUntil ends early, estimated() falls back to the fixed-time
// derivation while the kernel still holds, and the uncontested branch
// can re-command the phase the schedule shows (superseding its own live
// hold) — noise and latency, never free green. The recorded contract
// proposal (a VerbReply applied-tick/effective-hold echo) would remove
// the assumption; see the ADR's M2 notes.
//
// FEEDBACK GAP (documented in the ADR's M2 notes, not worked around
// here): nothing on the live plane tells the controller a hold lapsed or
// a renewal was declined — it PREDICTS both from its own command history,
// which is exact as long as it is the only controller commanding a
// program (the reference deployment shape). What it would ask of the
// contract is in the ADR.
//
// CADENCE (the round-10 event-volume concern): verbs are issued only on
// a decision — a renewal when the running hold has < RenewBelow ticks
// left, a switch when one is warranted — never one-verb-per-tick. A
// phase switch is a supersede, which fires NO lapse event; a lapse fires
// only when a hold is allowed to end (gap-out release or the chain
// bound), i.e. at most one record event per phase service per program.
// Idle programs (no presence anywhere) are left on their fixed-time
// program and cost no verbs at all.
package sigctl

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// Config tunes the controller; zero values take the defaults.
type Config struct {
	Run string
	// CadenceTicks is the decision cadence (default 20 = 2 s at the
	// validated dt): how often each program is re-evaluated. Verbs are
	// issued only when a decision produces one — the cadence bounds
	// reaction latency, not verb volume.
	CadenceTicks uint64
	// HoldTicks is the hold commanded per verb (default 100 = 10 s). It
	// must exceed CadenceTicks + RenewBelow so renewals keep the chain
	// contiguous (a fixed-time gap would end the chain early — harmless,
	// but a needlessly lapsed hold is a needlessly fixed-time junction).
	HoldTicks uint64
	// RenewBelow is the remaining-hold threshold that triggers a renewal
	// (default 30 = 3 s).
	RenewBelow uint64
	// MinGreenTicks bounds phase flicker: no switch away from a phase
	// younger than this (default 100 = 10 s). At light load a shorter
	// min-green lets alternating sparse arrivals ping-pong the junction
	// through its transition phases — measured on the 9,000-tick smoke
	// bracket: min-green 50 cost the actuated arm a third of its network
	// speed against fixed time.
	MinGreenTicks uint64
	// MaxChainTicks is the controller's local copy of the kernel's
	// starvation bound (default: engine.SignalHoldMaxSeconds compiled
	// from the run's dt at attach). The controller computes the chain
	// clamp from its own verb history and never issues a renewal the rail
	// would decline.
	MaxChainTicks uint64
	// DetectRadiusM is the detector radius around each stop line
	// (default 25 m — the first few queue positions; a physical stop-line
	// detector's loop).
	DetectRadiusM float64
	// SwitchMinQueue is the minimum waiting count on a candidate phase's
	// detectors before a switch is made to it (default 1). At light load
	// a single straggler otherwise flips the junction off the major
	// movement and breaks platoon flow — measured on the 9,000-tick smoke
	// bracket, where unconditional switching cost ~40% network speed
	// against fixed time.
	SwitchMinQueue int
	// MaxGreenOnCallTicks caps a phase extension once a conflicting call
	// is waiting (default 200 = 20 s — textbook max-green-with-call):
	// without it an uncontested-looking renew loop holds the major
	// movement toward the 300 s chain bound while cross traffic waits
	// (measured on the smoke bracket: unconditional renewals cost ~15 %
	// network speed at light load). 0 uses the default.
	MaxGreenOnCallTicks uint64
	// Programs, when non-empty, restricts control to the named program
	// ids (test fixtures; default controls every program with usable
	// geometry and ≥ 2 movement phases).
	Programs []string
	Log      *log.Logger
}

func (c *Config) defaults() {
	if c.CadenceTicks == 0 {
		c.CadenceTicks = 20
	}
	if c.HoldTicks == 0 {
		c.HoldTicks = 100
	}
	if c.RenewBelow == 0 {
		c.RenewBelow = 30
	}
	if c.MinGreenTicks == 0 {
		c.MinGreenTicks = 100
	}
	if c.DetectRadiusM == 0 {
		c.DetectRadiusM = 25
	}
	if c.SwitchMinQueue == 0 {
		c.SwitchMinQueue = 1
	}
	if c.MaxGreenOnCallTicks == 0 {
		c.MaxGreenOnCallTicks = 200
	}
}

// Detector is one link's virtual stop-line loop: a point in the
// network's metric frame plus the lane's direction. Only the APPROACH
// side of the stop line counts: a vehicle that has crossed it is being
// served, not waiting — a zone that reaches past the line never gaps out
// (measured on the first smoke: zero switches, renewals holding toward
// the chain bound while cross traffic waited). progID binds the detector
// to its program when the wire table arrives.
type Detector struct {
	Link   int // signal link index within the program
	X, Y   float64
	Dx, Dy float64 // unit direction of travel across the stop line
	progID string
}

// Geom carries the static detector geometry, derived from the scenario's
// network file. Programs without shapes for ANY link get no detectors and
// are left on fixed time (logged at attach).
type Geom struct {
	// ByProgram maps a program id to its link-indexed detector points; a
	// nil entry means that link has no usable shape.
	ByProgram map[string][]*Detector
}

// LoadGeom reads the detector geometry from a network-format-v1 file: one
// point per signal-bound internal lane, the START of its centerline — the
// stop line. The file is static scenario content; everything dynamic
// (programs, phases, the link→lane binding) arrives over the wire.
func LoadGeom(path string) (*Geom, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sigctl geom: %w", err)
	}
	var nf engine.NetFile
	if err := json.Unmarshal(data, &nf); err != nil {
		return nil, fmt.Errorf("sigctl geom %s: %w", path, err)
	}
	shapes := map[string][][2]float64{}
	for _, nl := range nf.Lanes {
		if len(nl.Shape) > 0 {
			shapes[nl.ID] = nl.Shape
		}
	}
	g := &Geom{ByProgram: map[string][]*Detector{}}
	for _, nl := range nf.Lanes {
		if nl.TL == "" || nl.TLLink == nil {
			continue
		}
		sh := shapes[nl.ID]
		if len(sh) < 2 {
			continue // no centerline: this link gets no detector
		}
		dx, dy := sh[1][0]-sh[0][0], sh[1][1]-sh[0][1]
		if n := math.Hypot(dx, dy); n > 0 {
			dx, dy = dx/n, dy/n
		}
		slots := g.ByProgram[nl.TL]
		for len(slots) <= *nl.TLLink {
			slots = append(slots, nil)
		}
		slots[*nl.TLLink] = &Detector{Link: *nl.TLLink, X: sh[0][0], Y: sh[0][1], Dx: dx, Dy: dy}
		g.ByProgram[nl.TL] = slots
	}
	return g, nil
}

// progState is the controller's per-program tracking: its own command
// history, from which the enforced phase and the chain bound are
// PREDICTED (the live plane does not echo overrides — the documented M2
// feedback gap; exact while this controller is the program's only
// commander).
type progState struct {
	prog       natsio.SigProgram
	detectors  []*Detector // link-indexed; nil = no detector on that link
	candidates []int       // phase indices with at least one green link
	green      [][]int     // per candidate phase: its green (g/G) link indices

	myPhase    int    // last commanded phase (-1 = none)
	phaseSince uint64 // when the enforced phase began (best knowledge — renewals do NOT restart it)
	holdUntil  uint64 // my current hold's coverage end (exclusive)
	chainStart uint64 // first-since of my current same-phase chain
	callSince  uint64 // first tick a conflicting call has been waiting (0 = none)
	target     int    // walk destination phase (-1 = not walking)
	seq        int    // verb sequence (request ids)
	lastDecide uint64 // last decision tick
}

// estimated returns the phase enforced at tick as far as this controller
// knows, and when it began: its own command while the hold covers the
// tick, the fixed-time derivation (the wire table's own integer math)
// past it.
func (ps *progState) estimated(tick uint64) (int, uint64) {
	if ps.myPhase >= 0 && tick < ps.holdUntil {
		return ps.myPhase, ps.phaseSince
	}
	// The offset wrap: a wrapped phase began before tick 0, and
	// elapsed > tick would underflow the onset to ~2^64 (and min-green
	// would never pass for the run's first offset ticks). Saturate at 0.
	if el := phaseElapsed(ps.prog, tick); el < tick {
		return ps.prog.PhaseAt(tick), tick - el
	}
	return ps.prog.PhaseAt(tick), 0
}

// phaseElapsed mirrors the kernel's phaseAtElapsed over the wire table:
// how many ticks ago the fixed-time phase in force at tick began.
func phaseElapsed(p natsio.SigProgram, tick uint64) uint64 {
	var cycle uint64
	for _, ph := range p.Phases {
		cycle += uint64(ph.DurationTicks)
	}
	if cycle == 0 {
		return 0
	}
	x := (tick%cycle + cycle - p.OffsetTicks%cycle) % cycle
	for _, ph := range p.Phases {
		if x < uint64(ph.DurationTicks) {
			return x
		}
		x -= uint64(ph.DurationTicks)
	}
	return 0 // unreachable (x < cycle)
}

// decideProgram is the pure decision (unit-tested directly): given the
// per-link detector counts at tick, return the phase to command, the hold
// to ask for, and ok=true when a verb should be sent this decision.
// A switch is never a jump between movement phases: the command is the
// NEXT phase in table order on the way to the target (ps.target tracks
// the walk), and every intermediate phase — the program's yellow/all-red
// transitions included — is commanded at its NATURAL duration, so the
// program's clearance intervals are actually simulated. Skipping them
// would be free clearance time and would inflate any measurement of the
// controller.
func (c *Controller) decideProgram(ps *progState, count []int, tick uint64) (int, uint64, bool) {
	// A walk in progress takes precedence: keep stepping through the
	// table until the target is in force. Walk steps CHAIN SEAMLESSLY:
	// the next step's verb is issued exactly when the current step's hold
	// has one tick left (it applies at the expiry boundary), so from the
	// switch decision to target arrival every tick is under a commanded
	// phase — zero fixed-time fallthrough, no per-step lapse. The cadence
	// gate does not apply mid-walk (dueNow): the controller decides every
	// snapshot while walking.
	if ps.target >= 0 {
		if ps.myPhase == ps.target && tick < ps.holdUntil {
			ps.target = -1 // arrived; the normal logic below holds/renews it
		} else {
			if ps.myPhase >= 0 && tick+1 < ps.holdUntil {
				return 0, 0, false // the current walk step still covers the next boundary
			}
			return c.walkStep(ps, ps.myPhase)
		}
	}
	enforced, since := ps.estimated(tick)
	greenOf := func(phase int) []int {
		for i, cand := range ps.candidates {
			if cand == phase {
				return ps.green[i]
			}
		}
		return nil
	}
	served := greenOf(enforced)
	if served == nil {
		// A transition phase (all red/amber for every link — the
		// clearance intervals): never a target, never cut short.
		return 0, 0, false
	}
	anyPresence := false
	servedCount := 0
	for link, det := range ps.detectors {
		if det == nil || link >= len(count) || count[link] == 0 {
			continue
		}
		anyPresence = true
		for _, gl := range served {
			if gl == link {
				servedCount += count[link]
				break
			}
		}
	}
	if !anyPresence {
		ps.callSince = 0
		return 0, 0, false // idle junction: stay on (or return to) fixed time
	}
	// The waiting call: the strongest presence on any candidate the
	// enforced phase does not serve (lowest-index tie-break).
	best, bestN := -1, 0
	for i, cand := range ps.candidates {
		if cand == enforced {
			continue
		}
		n := 0
		for _, gl := range ps.green[i] {
			if gl < len(count) {
				n += count[gl]
			}
		}
		if n > bestN {
			best, bestN = cand, n
		}
	}
	call := bestN >= c.cfg.SwitchMinQueue
	if call {
		if ps.callSince == 0 {
			ps.callSince = tick
		}
	} else {
		ps.callSince = 0
	}
	// The chain budget: a renewal sent at tick is applied at tick+1 and
	// declined by the rail when chainStart+maxChain ≤ applied. Predict
	// that locally and never send a doomed renewal.
	canExtend := ps.myPhase >= 0 && tick < ps.holdUntil &&
		ps.chainStart+c.cfg.MaxChainTicks > tick+1
	if servedCount > 0 && !call {
		// Uncontested: extend the serving phase freely (the chain rail
		// still bounds it).
		if ps.myPhase < 0 || tick >= ps.holdUntil {
			return enforced, c.cfg.HoldTicks, true // take control from fixed time
		}
		if ps.holdUntil-tick >= c.cfg.RenewBelow {
			return 0, 0, false // hold still fresh: no verb needed
		}
		if canExtend {
			return enforced, c.cfg.HoldTicks, true // renew
		}
		return 0, 0, false
	}
	if servedCount > 0 && call {
		// Contested: the extension is capped by max-green-on-call — and by
		// the rail itself: a hold the chain bound is about to decline is
		// past its cap by definition. Past either cap, switch to the call
		// regardless of the gap — but never by cutting a green younger
		// than the minimum: callSince measures the CALL's age, and on
		// fixed time the schedule may have rotated into this green
		// seconds ago (round-3 review).
		railCapped := ps.myPhase >= 0 && tick < ps.holdUntil && !canExtend
		if tick < ps.callSince+c.cfg.MaxGreenOnCallTicks && !railCapped {
			if ps.myPhase >= 0 && tick < ps.holdUntil && ps.holdUntil-tick < c.cfg.RenewBelow {
				return enforced, c.cfg.HoldTicks, true // renew within the on-call budget
			}
			// Fixed time serving, call waiting, budget left: do nothing —
			// the schedule is already serving and will switch on its own;
			// a takeover here would START a hold against the call.
			return 0, 0, false
		}
		if tick < since+c.cfg.MinGreenTicks {
			return 0, 0, false
		}
		if best >= 0 {
			return c.beginWalk(ps, best, enforced) // a cap reached: serve the call
		}
		return 0, 0, false
	}
	// Gap-out: the served approach shows no presence. Switch to the
	// waiting call, subject to the minimum green.
	if call && tick >= since+c.cfg.MinGreenTicks {
		return c.beginWalk(ps, best, enforced)
	}
	return 0, 0, false
}

// beginWalk starts the transition to a target movement phase: the first
// command is the next phase in TABLE ORDER (a transition phase at its
// natural duration), and later decisions walk on from there. When the
// target is already enforced no walk is needed.
func (c *Controller) beginWalk(ps *progState, target, enforced int) (int, uint64, bool) {
	if target == enforced {
		return target, c.cfg.HoldTicks, true
	}
	ps.target = target
	return c.walkStep(ps, enforced)
}

// walkStep commands the phase after `from` in table order: the target at
// the configured hold, anything between here and there at its NATURAL
// duration (the program's own transition timing).
func (c *Controller) walkStep(ps *progState, from int) (int, uint64, bool) {
	next := (from + 1) % len(ps.prog.Phases)
	if next == ps.target {
		return next, c.cfg.HoldTicks, true
	}
	return next, uint64(ps.prog.Phases[next].DurationTicks), true
}

// Controller is one attached actuated controller.
type Controller struct {
	cfg   Config
	nc    *nats.Conn
	ctlID string
	geom  *Geom
	dt    float64
	log   *log.Logger

	hb      string
	replyTo string

	mu    sync.Mutex
	progs map[string]*progState
	grid  map[[2]int][]*Detector // coarse spatial index over detectors
	acc   []natsio.SigProgram    // open chunk generation (ADR-0016)
	accN  int                    // chunks accumulated in the open generation

	tableReady     chan struct{} // closed when the first COMPLETE table generation installs
	tableReadyOnce sync.Once

	// Evidence counters (replies arrive on a second subscription
	// goroutine; all shared state rides the mutex).
	Sent, Accepted, Rejected    int
	Takeovers, Switches, Renews int // command breakdown: fixed-time take-control / phase change / same-phase extension

	lastSnap time.Time // last snapshot received (the standalone wrapper's run-over watchdog)
}

var discardLog = log.New(io.Discard, "", 0)

// Attach connects to the run, loads the signal table, and starts the
// control loop on the snapshot rhythm. geom supplies the detector
// geometry (LoadGeom); programs it cannot see get no detectors and stay
// fixed-time. The attach barrier pattern matches the demand director's:
// Attach returns after the hello handshake and subscriptions exist, and
// Ready flushes them live.
func Attach(nc *nats.Conn, js nats.JetStreamContext, cfg Config, geom *Geom) (*Controller, error) {
	cfg.defaults()
	lg := cfg.Log
	if lg == nil {
		lg = discardLog
	}

	reg, err := natsio.NewRegistry(js)
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	var meta *natsio.RunMeta
	for i := 0; i < 50; i++ {
		meta, err = reg.Meta(cfg.Run)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("run meta: %w", err)
	}
	dt := meta.Spec.Params.Dt
	if dt <= 0 {
		dt = 0.1
	}

	hello, _ := json.Marshal(natsio.HelloRequest{
		ContractVersion: natsio.SchemaVersion, ControllerType: "director",
		CadenceTicks: 1, Grants: []string{"director"},
	})
	var ctlID string
	for i := 0; i < 50; i++ {
		msg, err := nc.Request(natsio.SubjectCtlHello(cfg.Run), hello, 300*time.Millisecond)
		if err == nil {
			var rep natsio.HelloReply
			if json.Unmarshal(msg.Data, &rep) == nil && rep.Accepted {
				ctlID = rep.ControllerID
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ctlID == "" {
		return nil, fmt.Errorf("sigctl: attach rejected or timed out")
	}

	c := &Controller{
		cfg: cfg, nc: nc, ctlID: ctlID, geom: geom, dt: dt, log: lg,
		progs: map[string]*progState{},
		grid:  map[[2]int][]*Detector{},
		// A complete empty table (no signalized junctions) is still a
		// complete table: Ready must not wait out the timeout on a
		// signal-free run.
		tableReady: make(chan struct{}),
	}
	if c.cfg.MaxChainTicks == 0 {
		// The kernel's starvation bound compiled onto this run's tick grid
		// (the same math as engine.signalHoldMaxTicks).
		c.cfg.MaxChainTicks = max(1, uint64(math.Round(engine.SignalHoldMaxSeconds/dt)))
	}
	c.hb = natsio.SubjectCtlHeartbeat(cfg.Run, ctlID)
	c.replyTo = nats.NewInbox()
	if _, err := nc.Subscribe(c.replyTo, c.onReply); err != nil {
		return nil, fmt.Errorf("sigctl: subscribe replies: %w", err)
	}
	if _, err := nc.Subscribe(natsio.SubjectStateSig(cfg.Run), c.onSigTable); err != nil {
		return nil, fmt.Errorf("sigctl: subscribe signal table: %w", err)
	}
	if _, err := nc.Subscribe(natsio.SubjectStateSnap(cfg.Run), c.onSnapshot); err != nil {
		return nil, fmt.Errorf("sigctl: subscribe snapshots: %w", err)
	}
	// Pull the program table NOW (ADR-0016 request/reply, answered with
	// the full chunk set on the reply inbox): an attach after the tick-0
	// publication otherwise waits out the 20-tick catch-up, controlling
	// nothing. The reply chunks land in onSigTable like any publication.
	tableInbox := nats.NewInbox()
	if _, err := nc.Subscribe(tableInbox, c.onSigTable); err != nil {
		return nil, fmt.Errorf("sigctl: subscribe table reply: %w", err)
	}
	if err := nc.PublishRequest(natsio.SubjectStateSigReq(cfg.Run), tableInbox, nil); err != nil {
		return nil, fmt.Errorf("sigctl: request signal table: %w", err)
	}
	lg.Printf("sigctl: attached as %s (director grant) to run %q (dt=%.3fs, chain bound %d ticks)",
		ctlID, cfg.Run, dt, c.cfg.MaxChainTicks)
	return c, nil
}

// Ready flushes the subscriptions live on the server and then BLOCKS
// until a complete signal-table generation has installed (bounded): the
// attach barrier's "ready" must mean "controlling" — a controller that
// opens the start gate with no table runs the actuated arm as a silent
// no-op. A complete empty table (a signal-free run) releases the wait
// like any other generation.
func (c *Controller) Ready() error {
	if err := c.nc.Flush(); err != nil {
		return err
	}
	return c.awaitTable(10 * time.Second)
}

// awaitTable blocks for the first complete table generation (unit-tested
// directly; Ready's timeout wrapper).
func (c *Controller) awaitTable(timeout time.Duration) error {
	select {
	case <-c.tableReady:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("sigctl: signal table never arrived within %s — attach would mean controlling nothing", timeout)
	}
}

// LastSnapshot returns when the most recent snapshot arrived (zero before
// the first). The standalone wrapper uses it to notice the run has ended:
// snapshots stop when serve finishes.
func (c *Controller) LastSnapshot() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastSnap
}

// Close detaches the control loop and logs the final tally.
func (c *Controller) Close() {
	c.nc.Flush()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.Printf("sigctl: done — verbs=%d accepted=%d rejected=%d programs=%d takeovers=%d switches=%d renews=%d",
		c.Sent, c.Accepted, c.Rejected, len(c.progs), c.Takeovers, c.Switches, c.Renews)
}

// onSigTable installs one chunk or generation of the program table. The
// chi-scale table arrives chunked (ADR-0016: sig_chunk "i/n"); a complete
// generation replaces the program set. Programs keep their command
// history across table republishes (the cadence catch-up is the same
// bytes).
func (c *Controller) onSigTable(m *nats.Msg) {
	f, err := natsio.ParseSignalFrame(m.Data)
	if err != nil {
		c.log.Printf("sigctl: signal frame: %v", err)
		return
	}
	hdr := m.Header.Get("sig_chunk")
	c.mu.Lock()
	defer c.mu.Unlock()
	if hdr == "" || hdr == "1/1" {
		c.installTable(f.Programs) // whole table in one frame
		return
	}
	// Chunked table (ADR-0016): accumulate a generation in 1..n order and
	// install only when it completes. Any gap, regression, or malformed
	// header drops the partial generation — the previous INSTALLED table
	// keeps serving until a complete generation lands.
	var i, n int
	if _, err := fmt.Sscanf(hdr, "%d/%d", &i, &n); err != nil || i < 1 || i > n {
		c.log.Printf("sigctl: bad sig_chunk header %q", hdr)
		c.acc, c.accN = nil, 0
		return
	}
	if i != c.accN+1 {
		if i != 1 {
			c.log.Printf("sigctl: sig_chunk %d/%d out of sequence (have %d) — generation dropped", i, n, c.accN)
			c.acc, c.accN = nil, 0
			return
		}
		c.acc, c.accN = nil, 0 // chunk 1 opens a generation (a partial one is dropped)
	}
	c.acc = append(c.acc, f.Programs...)
	c.accN++
	if i < n {
		return
	}
	c.installTable(c.acc)
	c.acc, c.accN = nil, 0
}

// installTable takes a COMPLETE program table (whole frame or a finished
// chunk generation). Programs already tracked keep their command history
// — the catch-up republication is the same bytes. The first install
// releases Ready's waiters: "attached" means "controlling".
func (c *Controller) installTable(programs []natsio.SigProgram) {
	defer c.tableReadyOnce.Do(func() { close(c.tableReady) })
	for _, p := range programs {
		if _, ok := c.progs[p.ID]; ok {
			continue
		}
		if len(c.cfg.Programs) > 0 {
			want := false
			for _, id := range c.cfg.Programs {
				if id == p.ID {
					want = true
					break
				}
			}
			if !want {
				continue
			}
		}
		ps := newProgState(p, c.geom)
		if ps != nil {
			c.progs[p.ID] = ps
			for _, det := range ps.detectors {
				if det != nil {
					det.progID = p.ID
					cell := [2]int{int(math.Floor(det.X / 50)), int(math.Floor(det.Y / 50))}
					c.grid[cell] = append(c.grid[cell], det)
				}
			}
		}
	}
}

// newProgState builds the per-program control state: the candidate
// MOVEMENT phases and their green links, and the link detectors. Phase
// classification: a phase is a movement phase iff it shows at least one
// green (g/G) AND no amber (y) anywhere; anything containing amber — even
// mixed green/amber — or showing no green at all is a TRANSITION
// (clearance) phase, never a serve or hold target. (A mixed amber/green
// phase exists in the wild: chi-loop-urban program 2169310 — holding one
// for a full hold would stretch a clearance interval into a fake green.)
// nil when the program is not actuatable (fewer than two movement phases,
// or no detector geometry at all).
func newProgState(p natsio.SigProgram, geom *Geom) *progState {
	ps := &progState{prog: p, myPhase: -1, target: -1}
	for i, ph := range p.Phases {
		var green []int
		amber := false
		for link := 0; link < len(ph.State); link++ {
			switch ph.State[link] {
			case 'g', 'G':
				green = append(green, link)
			case 'y':
				amber = true
			}
		}
		if len(green) > 0 && !amber {
			ps.candidates = append(ps.candidates, i)
			ps.green = append(ps.green, green)
		}
	}
	if len(ps.candidates) < 2 {
		return nil
	}
	dets := geom.ByProgram[p.ID]
	if dets == nil {
		return nil
	}
	ps.detectors = dets
	any := false
	for _, d := range dets {
		if d != nil {
			any = true
		}
	}
	if !any {
		return nil
	}
	return ps
}

// presenceCounts bins a snapshot frame's vehicle positions against the
// detector grid, counting vehicles per program per link zone. The scan
// neighborhood derives from the radius (50 m cells): ±1 cell is complete
// only for radius ≤ 50 — a larger flag value must scan wider or detectors
// are silently missed (phantom gap-outs).
func (c *Controller) presenceCounts(f natsio.Frame) map[*progState][]int {
	count := map[*progState][]int{}
	mark := func(d *Detector) {
		ps := c.progs[d.progID]
		if ps == nil {
			return
		}
		pr := count[ps]
		if pr == nil {
			pr = make([]int, len(ps.detectors))
			count[ps] = pr
		}
		if d.Link < len(pr) {
			pr[d.Link]++
		}
	}
	r2 := c.cfg.DetectRadiusM * c.cfg.DetectRadiusM
	ext := int(math.Ceil(c.cfg.DetectRadiusM / 50))
	if ext < 1 {
		ext = 1
	}
	for _, v := range f.Vehicles {
		cx, cy := int(math.Floor(float64(v.X)/50)), int(math.Floor(float64(v.Y)/50))
		for dx := -ext; dx <= ext; dx++ {
			for dy := -ext; dy <= ext; dy++ {
				for _, d := range c.grid[[2]int{cx + dx, cy + dy}] {
					ddx, ddy := float64(v.X)-d.X, float64(v.Y)-d.Y
					if ddx*ddx+ddy*ddy > r2 {
						continue
					}
					// Approach side only: past the stop line is being
					// served, not waiting (2 m tolerance for the bumper at
					// the line).
					if ddx*d.Dx+ddy*d.Dy > 2.0 {
						continue
					}
					mark(d)
				}
			}
		}
	}
	return count
}

// onSnapshot is the control loop: heartbeat, recompute detector presence
// from the frame's positions, and run the per-program decisions on the
// cadence.
func (c *Controller) onSnapshot(m *nats.Msg) {
	f, err := natsio.ParseFrame(m.Data)
	if err != nil {
		return
	}
	_ = c.nc.Publish(c.hb, nil)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastSnap = time.Now()
	if len(c.progs) == 0 {
		return
	}
	count := c.presenceCounts(f)

	// Sorted program order: ProcessControl may drain only a prefix of the
	// buffered verbs at a boundary, so publication order can decide which
	// programs apply this tick versus the next — given identical
	// snapshots, the verb order must be deterministic.
	ids := make([]string, 0, len(c.progs))
	for id := range c.progs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ps := c.progs[id]
		if !c.dueNow(ps, f.Tick) {
			continue
		}
		ps.lastDecide = f.Tick
		if phase, hold, ok := c.decideProgram(ps, count[ps], f.Tick); ok {
			c.send(ps, phase, hold, f.Tick)
		}
	}
}

// dueNow reports whether a program is decided on this snapshot: every
// snapshot while a walk is in progress (a walk step must be issued the
// tick its predecessor's hold expires — cadence-gating a walk would
// lapse every short transition step into fixed-time fallthrough), and on
// the decision cadence otherwise.
func (c *Controller) dueNow(ps *progState, tick uint64) bool {
	if ps.target >= 0 {
		return true
	}
	return ps.lastDecide == 0 || tick-ps.lastDecide >= c.cfg.CadenceTicks
}

// requestID builds the verb's idempotency key: deterministic per program
// and sequence, discriminated by the engine-ASSIGNED controller id
// (unique per attach), so a controller restarting mid-run never collides
// with its previous process's ids — a collision would draw a stale
// cached accepted reply from the run-global dedup while the command
// never applied, and the optimistic tracking would diverge from the
// enforced state.
func (c *Controller) requestID(ps *progState) string {
	return fmt.Sprintf("sigctl-%s-%s-%06d", c.ctlID, ps.prog.ID, ps.seq)
}

// send issues one signal_set verb with the decided hold. State tracking
// updates optimistically at send, assuming next-boundary application (the
// documented precondition: the verb applies at whatever tick the next
// control drain lands on, which paced runs make the next boundary; at
// pace 0 the applied tick can lag a boundary, and the tracking skew
// self-corrects — the ADR's M2 notes). A different phase — or a command
// AFTER the hold's coverage ended — starts a new chain locally, matching
// the kernel's chain semantics exactly (the kernel continues a chain iff
// last.until ≥ the applied tick, so equality CONTINUES: applied >
// holdUntil, never >=, or the controller would believe a fresh chain
// started where the kernel renewed — and issue exactly the doomed renewal
// the design says it never sends). phaseSince moves only when the
// COMMANDED PHASE changes: the minimum green measures the phase's age,
// not the last renewal (the kernel's displayed-onset merging gives the
// same answer — same-state spans merge across renewal boundaries).
func (c *Controller) send(ps *progState, phase int, hold uint64, tick uint64) {
	applied := tick + 1
	switch {
	case ps.myPhase < 0 || applied > ps.holdUntil:
		c.Takeovers++ // taking control from the fixed-time program
	case phase != ps.myPhase:
		c.Switches++
	default:
		c.Renews++
	}
	if phase != ps.myPhase || applied > ps.holdUntil {
		ps.chainStart = applied
		ps.phaseSince = applied
	}
	if phase != ps.myPhase {
		// A new phase gets a fresh on-call budget: the call that forced
		// the switch is being served by it.
		ps.callSince = 0
	}
	ps.myPhase = phase
	ps.holdUntil = applied + hold
	ps.seq++
	req, _ := json.Marshal(natsio.VerbRequest{
		Verb:      "signal_set",
		RequestID: c.requestID(ps),
		Signal:    ps.prog.ID,
		Phase:     &phase,
		HoldTicks: hold,
	})
	c.Sent++
	if err := c.nc.PublishRequest(natsio.SubjectCtlVerb(c.cfg.Run, c.ctlID), c.replyTo, req); err != nil {
		c.log.Printf("sigctl: verb %s/%d: PUBLISH FAILED: %v", ps.prog.ID, phase, err)
	}
}

// onReply reconciles one verb acknowledgement (the demand director's
// inbox pattern: counters plus a loud line for rejections).
func (c *Controller) onReply(msg *nats.Msg) {
	var rep natsio.VerbReply
	ok := json.Unmarshal(msg.Data, &rep) == nil && rep.Accepted
	c.mu.Lock()
	if ok {
		c.Accepted++
	} else {
		c.Rejected++
	}
	c.mu.Unlock()
	if !ok {
		c.log.Printf("sigctl: verb %s REJECTED: %s", rep.RequestID, msg.Data)
	}
}
