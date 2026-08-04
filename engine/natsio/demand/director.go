// Package demand is the reference runtime demand director (ADR-0008 §5,
// ADR-0012 §3) as an embeddable client: it reads demand definitions
// (scenario-format flows — constant or Poisson spacing, piecewise-constant
// slices in SIM SECONDS, per-flow vehicle-type mix), samples arrivals with
// per-vehicle keyed RNG (ADR-0005/ADR-0007 discipline), and issues spawn
// verbs on ts.{run}.ctl.verb.{controller_id} (request/reply, director
// grant). The engine validates and injects through its own deterministic
// path; accepted verbs are recorded on the record plane, so replay never
// re-runs this sampler.
//
// The whole arrival program is a pure function of (demand definitions,
// seed), and request ids are deterministic (f{flow}-{ordinal} with the
// flow index global across all demand files), so a restarted director
// re-issues the identical program and the engine's request-id dedup makes
// the overlap harmless (failover-invisible). Multiple demand files share
// one index space — request ids and RNG keys can never collide across
// files.
//
// Semantics note: a flow's first arrival is at its first window's start
// (t=0 for an unsliced flow), matching SUMO's "flow begins emitting at
// begin" convention; subsequent gaps are exponential for poisson spacing.
// A gap drawn under one slice may land in a later window — the rate in
// force at the DRAW tick governs, which keeps sampling stateless.
//
// cmd/demand-director is the standalone wrapper; serve embeds this package
// when a scenario declares demand parts (seeded by the run seed, so the
// recorded (content-hash, seed) run key covers the demand realization).
package demand

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
	"traffic-sim/engine/scenario"
)

// Config tunes a Director.
type Config struct {
	Run  string
	Seed uint64
	Lead uint64      // send verbs this many ticks ahead of their earliest tick (0 → 30)
	Log  *log.Logger // nil → discard
	// StartTick is the tick the run BEGINS at — nonzero only for a warm
	// started run (ADR-0029 phase 1). Those arrivals already happened: they
	// are baked into the state being restored. Attach advances each flow's
	// sampler past them without publishing, which both avoids dumping the
	// whole backlog as one burst of already-expired verbs at the first
	// snapshot and leaves every sampler at exactly the position the cold run
	// held at that tick — so the program from StartTick on is identical to
	// the cold run's. Zero (the default) skips nothing and is byte-identical
	// to the pre-warm-start behavior.
	StartTick uint64
}

// Director is an attached demand director. The verb loop is driven by the
// engine's snapshot publications (no goroutine of its own beyond NATS
// callbacks).
type Director struct {
	cfg       Config
	nc        *nats.Conn
	ctlID     string
	dt        float64
	endS      float64
	samplers  []*flowSampler
	pending   []pendingArrival
	exhausted []bool
	sub       *nats.Subscription
	hb        string
	log       *log.Logger

	// Verb delivery is ASYNCHRONOUS. It used to be a blocking nc.Request per
	// spawn, and that request cannot return faster than one tick: the
	// contract buffers wire requests in NATS callbacks and drains them once
	// per tick (contract.go), so the reply is a tick away by construction.
	// One blocking send at a time therefore pinned the whole director to ONE
	// spawn per tick — 36,000 veh/h at dt=0.1 — regardless of how much demand
	// the scenario declared.
	//
	// Past that ceiling the failure was silent and irreversible: the sampler
	// backlog grew, and once its lag passed the kernel's hold window every
	// later verb arrived already expired and was dropped without ever being
	// attempted. Measured on chi-loop-urban (41,133 veh/h declared): 17,998
	// verbs accepted, 2,946 vehicles injected, injection dead from tick 3,033
	// to the 18,000-tick horizon and NOT resuming as the network emptied.
	//
	// The kernel side was never the constraint — its drain loop already
	// applies every buffered request in the same tick. So the director now
	// publishes with its own reply inbox and reconciles replies in a
	// callback, exactly like any other event it listens to.
	replyTo string
	rsub    *nats.Subscription

	mu                                          sync.Mutex
	Sent, Accepted, Rejected, Claims, Unclaimed int
}

type pendingArrival struct {
	at    float64
	vtype string
	dest  string // route destination drawn for this arrival ("" = unrouted)
}

var discard = log.New(io.Discard, "", 0)

// Attach connects to the run's registry (waiting for meta), attaches with
// the director grant, and starts sampling/verb submission on the snapshot
// rhythm. dfs are the run's demand definitions, in scenario-manifest order.
func Attach(nc *nats.Conn, js nats.JetStreamContext, cfg Config, dfs []*scenario.DemandFile) (*Director, error) {
	if cfg.Lead == 0 {
		cfg.Lead = 30
	}
	lg := cfg.Log
	if lg == nil {
		lg = discard
	}
	var flows []scenario.Flow
	for _, df := range dfs {
		flows = append(flows, df.Flows...)
	}
	if len(flows) == 0 {
		return nil, fmt.Errorf("demand director: no flows")
	}

	// The run spec supplies the tick length (sim seconds ↔ ticks) and the
	// run's tick budget.
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

	// Attach with the director grant (ADR-0008 §5).
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
		return nil, fmt.Errorf("attach rejected or timed out")
	}

	d := &Director{
		cfg: cfg, nc: nc, ctlID: ctlID,
		dt:   meta.Spec.Params.Dt,
		endS: float64(meta.Spec.Ticks) * meta.Spec.Params.Dt,
		log:  lg,
	}
	for i, f := range flows {
		d.samplers = append(d.samplers, newFlowSampler(f, cfg.Seed, i))
		d.pending = append(d.pending, pendingArrival{})
		d.exhausted = append(d.exhausted, false)
		at, vtype, dest, ok := d.samplers[i].next(d.dt)
		if !ok {
			d.exhausted[i] = true
			continue
		}
		d.pending[i] = pendingArrival{at: at, vtype: vtype, dest: dest}
	}
	// Warm start (ADR-0029 phase 1): fast-forward past arrivals the restored
	// state already contains. The draws are consumed exactly as sending them
	// would have, so each sampler lands on the position the cold run held at
	// StartTick and the remaining program is identical. Without this the
	// first snapshot drains the whole backlog at once — tens of thousands of
	// verbs with earliest ticks already in the past, which the kernel either
	// injects as one impossible burst or expires wholesale.
	if cfg.StartTick > 0 {
		lg.Printf("demand director: warm start at tick %d — skipped %d arrivals already contained in the restored state",
			cfg.StartTick, d.fastForward(cfg.StartTick))
	}
	lg.Printf("demand director: attached as %s (director grant) to run %q (dt=%.3fs, %d ticks, %d flows)",
		ctlID, cfg.Run, d.dt, meta.Spec.Ticks, len(flows))

	// Evidence taps: claim/unclaimed events prove director-spawned vehicles
	// join the default driver's fleet.
	// These fire on NATS callback goroutines while onSnapshot and Close read
	// the same counters under d.mu — so they take the lock too. Unsynchronized
	// increments here are a data race in the Go memory model, not merely a
	// lost-update risk: the read side may never observe them at all.
	_, _ = nc.Subscribe(natsio.SubjectEventClaim(cfg.Run), func(m *nats.Msg) {
		var evt natsio.ContractEvent
		if json.Unmarshal(m.Data, &evt) == nil {
			d.mu.Lock()
			d.Claims += len(evt.VehicleIDs)
			d.mu.Unlock()
		}
	})
	_, _ = nc.Subscribe(natsio.SubjectEventUnclaimed(cfg.Run), func(m *nats.Msg) {
		var evt natsio.ContractEvent
		if json.Unmarshal(m.Data, &evt) == nil && evt.Reason == natsio.ReasonSpawn {
			d.mu.Lock()
			d.Unclaimed += len(evt.VehicleIDs)
			d.mu.Unlock()
		}
	})

	// Heartbeat on the snapshot rhythm: the liveness sweep detaches any
	// controller silent for DetachAfterTicks (10), and a Poisson flow's
	// inter-verb gaps are far longer — heartbeats keep the attachment
	// alive between verbs (their purpose; ADR-0006 M4 addendum).
	// Own reply inbox: verbs are published, not requested, so their
	// acknowledgements arrive here as ordinary events.
	d.replyTo = nats.NewInbox()
	rsub, err := nc.Subscribe(d.replyTo, d.onReply)
	if err != nil {
		return nil, fmt.Errorf("subscribe verb replies: %w", err)
	}
	d.rsub = rsub

	d.hb = natsio.SubjectCtlHeartbeat(cfg.Run, ctlID)
	sub, err := nc.Subscribe(natsio.SubjectStateSnap(cfg.Run), d.onSnapshot)
	if err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	d.sub = sub
	return d, nil
}

// Close detaches the verb loop (the NATS attachment itself ages out via
// liveness) and logs the final tally.
func (d *Director) Close() {
	if d.sub != nil {
		_ = d.sub.Unsubscribe()
	}
	if d.rsub != nil {
		// Flush first: in-flight verbs still owe replies, and dropping the
		// inbox before they land would undercount Accepted for no reason.
		_ = d.nc.Flush()
		_ = d.rsub.Unsubscribe()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.log.Printf("demand director: done — verbs=%d accepted=%d rejected=%d spawn-announcements=%d claims=%d",
		d.Sent, d.Accepted, d.Rejected, d.Unclaimed, d.Claims)
}

// fastForward advances every flow past the arrivals at or before startTick
// WITHOUT publishing them, and reports how many it skipped. It consumes the
// same draws sending them would have, so each sampler lands on exactly the
// position the cold run held at that tick — the program from startTick on is
// then identical to the cold run's, which is the property that makes a warm
// started run comparable to the one it was cut out of.
func (d *Director) fastForward(startTick uint64) int {
	skipped := 0
	for i := range d.samplers {
		for !d.exhausted[i] && uint64(d.pending[i].at/d.dt+0.5) <= startTick {
			at, vtype, dest, ok := d.samplers[i].next(d.dt)
			skipped++
			if !ok {
				d.exhausted[i] = true
				break
			}
			d.pending[i] = pendingArrival{at: at, vtype: vtype, dest: dest}
		}
	}
	return skipped
}

func (d *Director) onSnapshot(m *nats.Msg) {
	f, err := natsio.ParseFrame(m.Data)
	if err != nil {
		return
	}
	_ = d.nc.Publish(d.hb, nil)
	for i := range d.samplers {
		if d.exhausted[i] {
			continue
		}
		// Drain every arrival this flow already owes, not one per
		// snapshot. A single send per snapshot cannot shed a backlog
		// faster than it accrues, so any transient lag became permanent.
		for !d.exhausted[i] {
			at := d.pending[i].at
			if at > d.endS {
				d.exhausted[i] = true
				break
			}
			if f.Tick+d.cfg.Lead < uint64(at/d.dt+0.5) {
				break
			}
			d.send(i)
		}
	}
	if f.Tick%200 == 0 {
		d.mu.Lock()
		sent, acc, rej, claims := d.Sent, d.Accepted, d.Rejected, d.Claims
		d.mu.Unlock()
		d.log.Printf("  tick %d: vehicles=%d verbs=%d accepted=%d rejected=%d claimed=%d",
			f.Tick, len(f.Vehicles), sent, acc, rej, claims)
	}
}

// onReply reconciles one verb acknowledgement off the request path.
func (d *Director) onReply(msg *nats.Msg) {
	var rep natsio.VerbReply
	ok := json.Unmarshal(msg.Data, &rep) == nil && rep.Accepted
	d.mu.Lock()
	if ok {
		d.Accepted++
	} else {
		d.Rejected++
	}
	d.mu.Unlock()
	if !ok {
		d.log.Printf("  verb %s REJECTED: %s", rep.RequestID, msg.Data)
	} else if rep.Duplicate {
		d.log.Printf("  verb %s: duplicate (already applied — restart overlap)", rep.RequestID)
	}
}

func (d *Director) send(flowIdx int) {
	fs := d.samplers[flowIdx]
	atTick := uint64(d.pending[flowIdx].at/d.dt + 0.5)
	req, _ := json.Marshal(natsio.VerbRequest{
		Verb:         "spawn",
		RequestID:    fmt.Sprintf("f%d-%06d", flowIdx, fs.ordinal-1),
		Origin:       fs.flow.Origin,
		VType:        d.pending[flowIdx].vtype,
		EarliestTick: atTick,
		Destination:  d.pending[flowIdx].dest,
		OffsetM:      fs.flow.OffsetM,
	})
	d.mu.Lock()
	d.Sent++
	d.mu.Unlock()
	if err := d.nc.PublishRequest(natsio.SubjectCtlVerb(d.cfg.Run, d.ctlID), d.replyTo, req); err != nil {
		d.log.Printf("  verb f%d-%06d (%s@%s tick %d): PUBLISH FAILED: %v",
			flowIdx, fs.ordinal-1, d.pending[flowIdx].vtype, fs.flow.Origin, atTick, err)
	}
	at, vtype, dest, ok := fs.next(d.dt)
	if !ok {
		d.exhausted[flowIdx] = true
		return
	}
	d.pending[flowIdx] = pendingArrival{at: at, vtype: vtype, dest: dest}
}

// flowSampler walks one flow's arrival program. All draws come from the
// per-vehicle stream keyed (seed, flowKey, ordinal) — the same stream
// supplies the type-mix draw and (for Poisson) the gap draw, mirroring the
// kernel Spawner's "jitter from the incoming vehicle's own stream" rule.
type flowSampler struct {
	flow    scenario.Flow
	seed    uint64
	key     uint64
	ordinal uint64
	nextS   float64 // next arrival, sim seconds
}

// newFlowSampler keys the flow's RNG domain by origin and the flow's FULL
// index in the run's demand-file order (8 bytes — the M10 low-byte version
// collided at 256 flows per origin; the key change resamples streams, which
// is safe because no pinned realization predates it).
func newFlowSampler(f scenario.Flow, seed uint64, idx int) *flowSampler {
	h := fnv.New64a()
	h.Write([]byte("traffic-sim/demand-director/flow"))
	h.Write([]byte(f.Origin))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(idx))
	h.Write(b[:])
	return &flowSampler{flow: f, seed: seed, key: h.Sum64()}
}

// stream returns the ordinal's keyed stream (derived fresh: samplers are
// stateless apart from the schedule, so a restart re-derives identically).
func (fs *flowSampler) stream() *engine.Stream {
	return engine.DeriveStream(fs.seed, fs.key^fs.ordinal)
}

// rateAt is the flow's rate at sim-second t: piecewise-constant with
// slices (0 outside any window), VehPerH for the whole run without.
func rateAt(f *scenario.Flow, tS float64) float64 {
	if len(f.Slices) == 0 {
		return f.VehPerH
	}
	for _, s := range f.Slices {
		if tS >= s.StartS && tS < s.EndS {
			return s.VehPerH
		}
	}
	return 0
}

// next returns the next (arrival sim-seconds, vtype, destination) and
// advances. ok=false when the program is exhausted (past until_s, or a zero
// rate with slices).
//
// Draw ORDER on the per-vehicle stream is gap → vtype → destination
// (ADR-0021): appending the destination draw LAST means a flow that
// declares no destinations consumes exactly the draws it always did, so
// every pinned pre-ADR-0021 realization is unchanged.
func (fs *flowSampler) next(dt float64) (atS float64, vtype, dest string, ok bool) {
	f := fs.flow
	if f.UntilS > 0 && fs.nextS >= f.UntilS {
		return 0, "", "", false
	}
	rate := rateAt(&f, fs.nextS)
	if rate <= 0 {
		if len(f.Slices) == 0 {
			return 0, "", "", false
		}
		// Jump to the next slice window.
		best := math.Inf(1)
		for _, s := range f.Slices {
			if s.EndS > fs.nextS && s.StartS < best {
				best = math.Max(s.StartS, fs.nextS)
			}
		}
		if math.IsInf(best, 1) {
			return 0, "", "", false
		}
		fs.nextS = best
		rate = rateAt(&f, fs.nextS)
		if rate <= 0 {
			return 0, "", "", false
		}
	}
	st := fs.stream()
	gapS := 3600 / rate
	if f.Spacing == "poisson" {
		// Exponential gaps (SUMO period="exp(X)"): -ln(1-u)·mean.
		gapS = -math.Log(1-st.Float64()) * gapS
	}
	vtype = pickType(st, f.VTypes)
	dest = pickWeighted(st, f.Destinations)
	at := fs.nextS
	fs.ordinal++
	fs.nextS = at + gapS
	return at, vtype, dest, true
}

// pickType draws the vehicle type from the per-vehicle stream, weights
// remapping one uniform draw (the kernel Spawner's convention). BOTH the
// total and the cumulative walk accumulate over the sorted key list —
// float addition is non-associative, so Go map order is forbidden anywhere
// near a sampled path (ADR-0005; a director restart must be invisible).
func pickType(st *engine.Stream, weights map[string]float64) string {
	if len(weights) == 0 {
		return "car"
	}
	return weightedDraw(st, weights)
}

// pickWeighted draws a destination lane from the flow's weighted
// distribution, "" when the flow declares none (unrouted — and, crucially,
// NO draw is consumed in that case, so pre-ADR-0021 flows keep their exact
// stream sequence).
func pickWeighted(st *engine.Stream, weights map[string]float64) string {
	if len(weights) == 0 {
		return ""
	}
	return weightedDraw(st, weights)
}

// weightedDraw remaps one uniform draw onto a weighted key set. BOTH the
// total and the cumulative walk accumulate over the SORTED key list — float
// addition is non-associative, so Go map order is forbidden anywhere near a
// sampled path (ADR-0005; a director restart must be invisible).
func weightedDraw(st *engine.Stream, weights map[string]float64) string {
	names := make([]string, 0, len(weights))
	for n := range weights {
		names = append(names, n)
	}
	sort.Strings(names)
	tot := 0.0
	for _, n := range names {
		tot += weights[n]
	}
	u := st.Float64() * tot
	cum := 0.0
	for _, n := range names {
		cum += weights[n]
		if u < cum {
			return n
		}
	}
	return names[len(names)-1]
}
