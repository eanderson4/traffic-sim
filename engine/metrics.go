package engine

// metrics.go — the M13 metric kernel (ADR-0014): a read-only observer that
// folds each engine tick into the two ratified primitives — per-vehicle trip
// records and Edie lane-interval records — plus run totals. It follows the
// XTField precedent (engine/xtfield.go): the caller drives Observe(e) once
// per Step and Finalize(e) once at the horizon; the kernel never feeds back
// into world state, so CRC/keyframe/replay semantics are untouched (the
// kernel-attached-vs-free CRC equality regression test lives in
// metrics_test.go). The normative metric definitions are ADR-0014 §3; the
// transport sinks (NATS publisher, file sink) are separate consumers of the
// records drained here — the kernel itself is transport-agnostic (§6).
//
// Determinism discipline (ADR-0005 applies to derived numbers too, ADR-0014
// §1): vehicles are accumulated in e.Vehicles() order (spawn/append order —
// deterministic from the seed), spawner origins and the director queue in
// their fixed orders, and map contents are only ever emitted after sorting —
// floats are non-associative, so no bare map iteration reaches an
// accumulation or emission path.

import (
	"fmt"
	"math"
	"sort"
)

// metricStopSpeed is the ADR-0014 §3 stop pin: a vehicle is stopped in a
// tick when v < 0.1 m/s.
const metricStopSpeed = 0.1

// MetricGroups selects which field groups a measurement set computes and
// emits (ADR-0014 §5's metrics list). Edie is the q/k/v core; the raw sums
// (SumDistM/SumTimeS) are always emitted — downstream seed pairing needs
// them regardless of group selection.
type MetricGroups struct {
	Edie      bool
	Occupancy bool
	Stops     bool
	TimeLoss  bool
}

// MetricSetConfig binds a set of element lanes to a measurement window
// (ADR-0014 §5 concretizing ADR-0012 §5; v1 elements are flat stable lane
// IDs only). The window is in ticks — integral multiples of the tick length
// by construction, satisfying ADR-0014 §3's interval pin. Intervals align to
// tick 0 offset by BeginTick: an observation at tick T covers sim time
// (T−1)·dt … T·dt, so interval k holds the observation ticks
// (BeginTick + k·PeriodTicks, BeginTick + (k+1)·PeriodTicks] — every full
// interval holds exactly PeriodTicks observations — and records stamp the
// aligned time grid [BeginTick + k·PeriodTicks, BeginTick + (k+1)·PeriodTicks).
// Only ticks inside the window (BeginTick, LastTick] accumulate (LastTick ==
// 0 = run horizon). Note the convention asymmetry: the config's LastTick is
// INCLUSIVE (last accumulating tick) while IntervalRecord.EndTick is the
// usual EXCLUSIVE time bound — different names, different bounds.
type MetricSetConfig struct {
	ID          string       // stable set id (unique within a KernelConfig)
	LaneIDs     []string     // element lanes (flat stable IDs, ADR-0012 §5)
	Groups      MetricGroups // which field groups to compute
	BeginTick   uint64       // window start; the first accumulating tick is BeginTick+1
	PeriodTicks uint64       // interval length in ticks
	LastTick    uint64       // last accumulating tick (inclusive); 0 = run horizon
}

// KernelConfig is the whole measurement plan: the interval sets plus whether
// per-vehicle trip records are emitted (ADR-0014 §5's trips switch). Run
// totals are defined either way — they derive from accumulator state, never
// from trip records alone (ADR-0014 §3).
type KernelConfig struct {
	Sets  []MetricSetConfig
	Trips bool
}

// DefaultKernelConfig is the zero-authoring plan (ADR-0014 §5): one set with
// every network lane (sorted IDs), all four field groups at the 900 s window
// — derived from the tick length (900/dt ticks; 9000 at the validated
// dt = 0.1 s, ADR-0005) — plus trip records.
func DefaultKernelConfig(net *Network, dt float64) KernelConfig {
	ids := make([]string, 0, len(net.Lanes))
	for _, l := range net.Lanes {
		ids = append(ids, l.ID)
	}
	sort.Strings(ids)
	return KernelConfig{
		Sets: []MetricSetConfig{{
			ID:          "default",
			LaneIDs:     ids,
			Groups:      MetricGroups{Edie: true, Occupancy: true, Stops: true, TimeLoss: true},
			PeriodTicks: uint64(math.Round(900 / dt)),
		}},
		Trips: true,
	}
}

// IntervalRecord is the lane-interval primitive (ADR-0014 §2): one record
// per (set element, closed interval), self-contained aggregates (never
// cumulative deltas, §4). Q = SumDistM/(L·T) veh/s, K = SumTimeS/(L·T)
// veh/m, V = Q/K m/s — the Edie space-mean speed over the cell, with q = k·v
// holding identically; V is nil when SumTimeS == 0 (omitted, never
// zero-filled, §2). ALL group fields are pointers, nil when their field
// group is off — "not computed" is never confusable with a genuine zero.
//
// BeginTick/EndTick stamp the SIM-TIME tick grid as the half-open range
// [BeginTick, EndTick): the interval covers observation ticks (BeginTick,
// EndTick] — observation tick T covers sim time (T−1)·dt … T·dt — so the
// covered sim time is exactly BeginTick·dt … EndTick·dt and the duration is
// (EndTick − BeginTick)·dt. A final interval truncated by the horizon (or
// by the set's LastTick) is emitted flagged Partial with its actual bounds
// (§3); comparison tooling drops partials.
type IntervalRecord struct {
	SchemaVersion int
	SetID         string
	LaneID        string
	BeginTick     uint64
	EndTick       uint64 // exclusive tick bound
	Partial       bool
	SumDistM      float64  // Σ distance over vehicle-ticks present (m)
	SumTimeS      float64  // Σ time over vehicle-ticks present (s)
	Q             *float64 // veh/s; nil when the edie group is off
	K             *float64 // veh/m; nil when the edie group is off
	V             *float64 // m/s; nil when edie is off OR SumTimeS == 0
	Occupancy     *float64 // time-weighted space fraction (§3); nil when off
	Stops         *int     // stop-episode starts; nil when the stops group is off
	TimeLossS     *float64 // accumulated time-loss; nil when the time_loss group is off
}

// TripRecord is the per-vehicle primitive (ADR-0014 §2): emitted when a
// vehicle leaves the network (Completed = true) and, flagged Completed =
// false with ExitTick = horizon, for every vehicle still in-network at the
// horizon — entered-but-unfinished vehicles are precisely the worst-served,
// and omitting them is the survivorship bias §3 forbids. TimeLossS is SUMO's
// timeLoss: per tick, Δt − distance_moved/v_desired, negative contributions
// clamped to 0, with v_desired = speedFactor × min(vType.v0,
// lane.speedLimit) (§3). DistanceM includes the despawn tick's exit segment
// (last-seen position to the lane end).
type TripRecord struct {
	SchemaVersion int
	VehicleID     uint64
	TypeName      string
	OriginLaneID  string // first-seen lane
	DestLaneID    string // last-seen lane
	EntryTick     uint64 // first observation tick
	ExitTick      uint64 // despawn tick, or the horizon for partials
	DistanceM     float64
	TimeLossS     float64
	Stops         int     // stop-episode starts (§3)
	StoppedTimeS  float64 // total stopped time (§3)
	Completed     bool    // false = horizon partial OR stranded
	// Stranded marks a trip the gridlock escape ended (ADR-0034): the
	// vehicle was removed from a jam it could not leave, at ExitTick,
	// wherever DestLaneID says it stood. Always Completed = false. Distinct
	// from a horizon partial, whose ExitTick is the horizon and which was
	// still in the network when the run ended.
	Stranded bool
}

// LaneDenied is the denied-entry (latent) demand of one origin lane
// (ADR-0014 §3): accumulated wait of vehicles requested but not yet
// injected, plus the demand still pending at the horizon. Pending is the
// mean-field backlog estimate: the spawner holds ONE pending vehicle per
// origin, but schedule lag implies lag × dt × rate requested
// vehicles (the formula, deterministic, includes the held vehicle — it is
// ≥ 1 while the origin is overdue). Director-queue entries count singly
// (each IS one requested vehicle).
type LaneDenied struct {
	WaitS   float64
	Pending float64
	// Served counts vehicles that waited ≥ 1 tick past their
	// scheduled/request tick and then left the pending state (injected or
	// expired) — ADR-0014 §3's "denied-entry count".
	Served int
}

// Totals is the run-total view (ADR-0014 §3): derived from the kernel's
// accumulators over ALL observed vehicle-ticks — unfinished trips contribute
// — never from completed-trip records alone. DeniedByLane carries per-origin
// denied-entry demand; DeniedWaitS/DeniedPending are the network-wide sums.
// DroppedCrossings counts lane changes no successor chain could attribute
// (genuinely disjoint — the defensive fallback books them, but the
// undercount risk is surfaced here, never silent).
type Totals struct {
	CompletedTrips int
	// StrandedTrips counts vehicles the gridlock escape removed (ADR-0034).
	// Disjoint from CompletedTrips and from ActiveAtHorizon. Nonzero means
	// the network gridlocked; StrandedBySection on engine Stats says where.
	StrandedTrips   int
	ActiveAtHorizon int
	VMT             float64 // Σ distance over all observed vehicle-ticks (m)
	VHT             float64 // Σ time over all observed vehicle-ticks (s)
	TotalTimeLossS  float64
	MeanTimeLossS   float64 // TotalTimeLossS per trip (completed + tracked); 0 when none
	DeniedWaitS     float64
	DeniedPending   float64
	DeniedServed    int // vehicles that waited ≥1 tick, then injected/expired (ADR-0014 §3)
	// DroppedCrossings mixes two units (split when the NATS publisher pins
	// the schema): conservative-booking events (~meters of misattributed
	// travel) and unobserved-vehicle estimates (whole invisible trips).
	// Mid-run values are provisional: pending gap IDs reconcile on first
	// observation and at Finalize, so a polling consumer may see a gap that
	// later resolves.
	DroppedCrossings int
	// DeniedByLane is a lookup map: consumers iterating it for emission
	// must sort the keys first (Go map order is nondeterministic).
	DeniedByLane map[string]LaneDenied
}

// Kernel is the M13 metric kernel: a caller-driven, read-only observer over
// engine state. Construct with NewKernel, call Observe after every Step and
// Finalize once at the horizon; pull records with DrainIntervals/DrainTrips
// and the run view with Totals.
type Kernel struct {
	dt    float64
	trips bool
	sets  []*metricSet

	last    map[uint64]mLastObs   // vehicle ID → previous observation
	tstates map[uint64]*tripState // vehicle ID → open trip accumulation

	pendIntervals []IntervalRecord // closed since the last drain
	pendTrips     []TripRecord     // completed since the last drain

	// Run-total accumulators (ADR-0014 §3). VMT/VHT/TotalTimeLossS fold
	// every observed vehicle-tick once, network-wide, independent of set
	// windows — interval records are per-set views over the same per-tick
	// accounting.
	vmt, vht, timeLoss float64
	completedTrips     int
	// strandedTrips counts vehicles the gridlock escape removed (ADR-0034).
	// Disjoint from completedTrips: a strand is not a completion.
	strandedTrips   int
	activeAtHorizon int
	finalized       bool
	observed        bool   // an Observe has landed (pairing guard)
	lastTick        uint64 // last observed tick (pairing guard)
	maxFirstSeenID  uint64 // highest first-observed vehicle ID (invisible-gap detection)
	// pendingGaps holds vehicle IDs skipped by the first-observation
	// sequence but not yet accounted: an ID leaving the set on its first
	// observation was just a late-spawning pending entry (jittered
	// multi-origin schedules invert injection order routinely); IDs still
	// in the set are invisible-vehicle estimates (reported in Totals) until
	// Finalize reconciles them against live pending IDs.
	pendingGaps      map[uint64]bool
	seenIDs          map[uint64]bool // every vehicle ID ever first-observed (exact invisible reconciliation)
	droppedCrossings int
	deniedWait       map[string]float64 // origin lane ID → accumulated wait (veh·s)
	deniedPending    map[string]float64 // origin lane ID → backlog estimate at horizon
	deniedServed     map[string]int     // origin lane ID → waited ≥1 tick, then injected/expired
	spOverdue        map[string]bool    // origin lane ID → was overdue last tick
	spPend           map[string]uint64  // origin lane ID → last-seen pending vehicle ID
	spBacklog        map[string]float64 // origin lane ID → integrated denied backlog (veh)
	dirDue           map[string]string  // due director request ID → lane ID (last tick; RequestID uniqueness among live entries is engine-enforced, EnqueueSpawn)
}

// mLastObs is the previous tick's observation of one vehicle. Speed, type,
// and desired-speed factor ride along so the despawn tick's exit segment can
// be accounted without the vehicle (which is gone by then); dtShare/tlShare
// record what was actually booked to that lane at that observation (time
// share, time-loss share), so an overshoot refund is EXACT against the
// original booking rather than an approximation.
type mLastObs struct {
	lane    *Lane
	s       float64
	v       float64      // speed at that observation (exit-time estimate)
	vt      *VehicleType // length + v0 for the despawn-tick accounting
	f       float64      // per-driver desired-speed factor (v0eff INPUT, not v0eff)
	stopped bool         // v < metricStopSpeed at that observation
	dtShare float64      // time booked to lane at that observation (s)
	tlShare float64      // time-loss booked to lane at that observation (s)
	// route is the vehicle's destination lane id at that observation
	// (ADR-0021), "" when unrouted. The despawn-tick attribution resolves
	// toward THIS lane when set: a routed vehicle's last hop is known
	// exactly, where exitResolve can only guess among reachable exits.
	route string
}

// tripState is the open per-vehicle trip accumulation.
type tripState struct {
	typeName  string
	origin    string
	dest      string
	entryTick uint64
	dist      float64
	timeLoss  float64
	stops     int
	stoppedS  float64
	stranded  bool // removed by the gridlock escape, not driven out (ADR-0034)
}

// laneAccum is the open-interval accumulator for one (set, lane).
type laneAccum struct {
	sumDist     float64
	sumTime     float64
	sumOcc      float64 // Σ time × vehicle length (m·s)
	sumTimeLoss float64
	stopStarts  int
}

// metricSet is one resolved measurement set with a single open interval.
type metricSet struct {
	cfg       MetricSetConfig
	lanes     []*Lane               // element lanes in config order
	acc       map[string]*laneAccum // lane ID → open-interval accumulator
	openFirst uint64                // first observation tick of the open interval
	openLast  uint64                // last observation tick once fully covered
}

// NewKernel builds a kernel over e's network for the given plan. Attach
// before the first Step (enforced — a mid-run attach would book placed
// positions as traveled distance and mis-seed trips), and after any pre-run
// placement: construction is the LAST pre-run mutation point — a vehicle
// added via AddInitialVehicle AFTER NewKernel is treated as a mid-run spawn
// and its placement books as traveled distance. Vehicles already in the
// network are seeded as last-known observations AND as open trips (entry
// tick 0 — they entered before the kernel attached), so their initial
// placement is never booked as traveled distance and their trip records
// share one semantics with mid-run-seen vehicles. Unknown or duplicate lane
// IDs, empty/duplicate set IDs, zero periods, a non-positive-length element
// lane, and a LastTick not after BeginTick fail loud at construction (the
// strict fence of ADR-0014 §5).
func NewKernel(e *Engine, cfg KernelConfig) (*Kernel, error) {
	if e.Tick != 0 {
		return nil, fmt.Errorf("metrics: attach before the first Step (engine already at tick %d)", e.Tick)
	}
	k := &Kernel{
		dt:            e.Params.Dt,
		trips:         cfg.Trips,
		last:          map[uint64]mLastObs{},
		tstates:       map[uint64]*tripState{},
		pendingGaps:   map[uint64]bool{},
		seenIDs:       map[uint64]bool{},
		deniedWait:    map[string]float64{},
		deniedPending: map[string]float64{},
		deniedServed:  map[string]int{},
		spOverdue:     map[string]bool{},
		spPend:        map[string]uint64{},
		spBacklog:     map[string]float64{},
		dirDue:        map[string]string{},
	}
	seen := map[string]bool{}
	for i := range cfg.Sets {
		sc := cfg.Sets[i]
		if sc.ID == "" {
			return nil, fmt.Errorf("metrics: set %d: empty id", i)
		}
		if seen[sc.ID] {
			return nil, fmt.Errorf("metrics: duplicate set id %q", sc.ID)
		}
		seen[sc.ID] = true
		if sc.PeriodTicks == 0 {
			return nil, fmt.Errorf("metrics: set %q: zero period", sc.ID)
		}
		if sc.LastTick > 0 && sc.LastTick <= sc.BeginTick {
			return nil, fmt.Errorf("metrics: set %q: last tick %d not after begin tick %d", sc.ID, sc.LastTick, sc.BeginTick)
		}
		s := &metricSet{
			cfg:       sc,
			acc:       map[string]*laneAccum{},
			openFirst: sc.BeginTick + 1,
			openLast:  sc.BeginTick + sc.PeriodTicks,
		}
		seenLanes := map[string]bool{}
		for _, id := range sc.LaneIDs {
			l := e.Net.LaneByID(id)
			if l == nil {
				return nil, fmt.Errorf("metrics: set %q: unknown lane %q", sc.ID, id)
			}
			if seenLanes[id] {
				return nil, fmt.Errorf("metrics: set %q: duplicate lane %q", sc.ID, id)
			}
			seenLanes[id] = true
			if l.Length <= 0 {
				return nil, fmt.Errorf("metrics: set %q: lane %q has non-positive length %v", sc.ID, id, l.Length)
			}
			s.lanes = append(s.lanes, l)
			s.acc[id] = &laneAccum{}
		}
		k.sets = append(k.sets, s)
	}
	// Seed the denied-demand trackers from spawner state (spawner.init has
	// already run): without the pending-ID seed the first Observe reads a
	// phantom injection and decrements a backlog that was never built.
	if e.spawner != nil {
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			if st.pend != nil {
				k.spPend[st.lane.ID] = st.pend.ID
				k.spOverdue[st.lane.ID] = st.tick <= e.Tick
			}
		}
	}
	for _, v := range e.Vehicles() {
		// dtShare is seeded at a full tick (approximation: no booking was
		// observed — it only matters if the vehicle is parked overshoot).
		k.last[v.ID] = mLastObs{lane: v.Lane, s: v.S, v: v.V, vt: v.Type, f: v.F, stopped: v.V < metricStopSpeed, dtShare: e.Params.Dt, route: v.Route}
		k.seenIDs[v.ID] = true
		if v.ID > k.maxFirstSeenID {
			k.maxFirstSeenID = v.ID // pre-placed IDs seed the gap baseline
		}
		if k.trips {
			k.tstates[v.ID] = &tripState{typeName: v.Type.Name, origin: v.Lane.ID, dest: v.Lane.ID, entryTick: 0}
		}
	}
	// Seeded `stopped` pins the v1 stop semantics: stop counts are episode
	// STARTS that begin in-run — a vehicle stopped at construction accrues
	// stopped TIME but no episode start until it moves and re-stops (the
	// pre-run start of its current episode is unobservable).
	return k, nil
}

// Observe folds one engine tick into the kernel. Call once per Step, after
// the Step — the observation at tick T covers sim time (T−1)·dt … T·dt.
func (k *Kernel) Observe(e *Engine) {
	// Pairing guard: observations must be consecutive from tick 1 — a
	// skipped or doubled tick mis-books despawns and crossings silently.
	switch {
	case k.finalized:
		panic("metrics: Observe called after Finalize")
	case !k.observed && e.Tick != 1:
		panic(fmt.Sprintf("metrics: first Observe at tick %d, want 1 (attach and observe from the first Step)", e.Tick))
	case k.observed && e.Tick != k.lastTick+1:
		panic(fmt.Sprintf("metrics: Observe at tick %d after tick %d — observations must be consecutive", e.Tick, k.lastTick))
	}
	k.observed, k.lastTick = true, e.Tick
	dt := k.dt
	tick := e.Tick
	// ADR-0021 mid-lane injections of THIS tick: a vehicle first observed
	// off an origin lane is otherwise indistinguishable from one that
	// crossed a boundary within its spawn tick, and the two book travel
	// completely differently. Lookup only, never iterated (ADR-0005).
	var injectedAt map[uint64]float64
	if inj := e.InteriorInjections(); len(inj) > 0 {
		injectedAt = make(map[uint64]float64, len(inj))
		for _, in := range inj {
			injectedAt[in.ID] = in.S
		}
	}
	cur := make(map[uint64]mLastObs, len(k.last)+4)
	for _, v := range e.Vehicles() {
		lane := v.Lane
		prev, seen := k.last[v.ID]
		var ts *tripState
		if k.trips {
			ts = k.tripFor(v, tick, lane)
		}
		var tickLoss float64 // this tick's time-loss: one clamp per vehicle-tick (ADR §3)
		shareT, shareTL := dt, 0.0
		split := false
		switch {
		case !seen:
			// First observation: a genuine mid-run spawn (S = 0 injection in
			// phase 1) that has already moved — count dist = S, time = dt.
			// (Vehicles present at NewKernel time are pre-seeded in k.last
			// and never land here.) A vehicle first observed OFF any origin
			// lane spawned AND crossed a boundary within its spawn tick —
			// the traveled chain is unobservable post-hoc, so the tick books
			// wholly to the seen lane and DroppedCrossings makes it loud.
			// Current networks can't reach this: the shortest origin lane
			// (I-280: 13.02 m) far exceeds one tick's travel (≤ ~4.3 m).
			//
			// An ADR-0021 interior injection is the one legitimate way to
			// be first observed off an origin lane. entryS is where it
			// entered, so its first tick of travel is v.S − entryS rather
			// than the whole lane prefix — booking v.S would credit the
			// vehicle with the entire block it never drove, on every one of
			// a scenario's residential flows.
			entryS, interior := injectedAt[v.ID]
			if !interior && e.Net.OriginByID(lane.ID) == nil {
				k.droppedCrossings++
			}
			// Invisible-vehicle detection: vehicle IDs are sequential with
			// pre-assigned IDs on pending entries, so a spawn+despawn within
			// one tick leaves a GAP before this ID. Gaps go into pendingGaps
			// for reconciliation — a late-spawning pending entry (jittered
			// multi-origin schedules invert injection order routinely)
			// removes itself on ITS first observation; what remains is the
			// invisible-vehicle estimate. DETECTS but cannot account the
			// invisible vehicles — loud beats silent.
			if v.ID > k.maxFirstSeenID+1 {
				for id := k.maxFirstSeenID + 1; id < v.ID; id++ {
					k.pendingGaps[id] = true
				}
			}
			delete(k.pendingGaps, v.ID)
			k.seenIDs[v.ID] = true
			if v.ID > k.maxFirstSeenID {
				k.maxFirstSeenID = v.ID
			}
			firstD := v.S - entryS
			if firstD < 0 {
				firstD = 0
			}
			k.addSegment(lane, firstD, dt, tick)
			tickLoss = k.addTimeLoss(lane, dt-firstD/v.v0eff(lane), tick)
			k.addOccupancy(lane, dt*v.Type.Length, tick)
			if ts != nil {
				ts.dist += firstD
			}
		case prev.lane == lane:
			d := v.S - prev.s
			if d < 0 {
				// Self-successor wrap (ring): boundaries() wrapped S by the
				// lane length with the lane pointer unchanged. V ≥ 0
				// always, so a same-lane decrease is exactly a wrap.
				d = lane.Length - prev.s + v.S
				if d < 0 {
					d = 0
				}
			}
			k.addSegment(lane, d, dt, tick)
			// Single-segment degenerate of the ADR §3 pin: the one per-tick
			// clamp IS the per-segment clamp when there is one segment.
			tickLoss = k.addTimeLoss(lane, dt-d/v.v0eff(lane), tick)
			k.addOccupancy(lane, dt*v.Type.Length, tick)
			if ts != nil {
				ts.dist += d
			}
		default:
			// The lane changed between observations. Step integrates
			// movement and boundary crossings BEFORE applying lane changes
			// (engine.go Step phases 4–6), so a lateral hop is an
			// instantaneous boundary event at the tick end: the tick's
			// travel happened on the TRAVELED lane, not necessarily the
			// final one. Assign-where-traveled (ADR-0014 §3) pins
			// distance/time/time-loss/occupancy there. The observation
			// baseline still records (cur, S) — next tick's travel starts
			// on cur.
			if prev.lane.Left == lane || prev.lane.Right == lane {
				// Pure lateral hop: the whole tick was traveled on the
				// pre-hop lane (S is preserved across the hop, mobil.go).
				// NOTE: the booking goes to prev.lane while mLastObs records
				// the hop TARGET — a compound hop+overshoot next tick would
				// refund the target (accepted edge-of-edge approximation).
				d := v.S - prev.s
				if d < 0 {
					// Defensive only: wrap+hop in one tick — unreachable
					// today (the only self-successor net, the ring, is
					// single-lane and has no laterals to hop to).
					d = 0
				}
				k.addSegment(prev.lane, d, dt, tick)
				// Single-segment degenerate of the ADR §3 pin: the one
				// per-tick clamp IS the per-segment clamp here.
				tickLoss = k.addTimeLoss(prev.lane, dt-d/v.v0eff(prev.lane), tick)
				k.addOccupancy(prev.lane, dt*v.Type.Length, tick)
				if ts != nil {
					ts.dist += d
				}
			} else {
				// Boundary crossing — possibly several on a net with
				// sub-vehicle-length internal lanes (netimport/OSM),
				// possibly followed by a same-tick hop.
				tickLoss, shareT, shareTL = k.splitTick(prev, lane, v, dt, tick, ts)
				split = true
			}
		}
		if !split {
			shareTL = tickLoss // single-segment branches: the tick's time-loss books to the observation lane
		}
		k.timeLoss += tickLoss
		if ts != nil {
			ts.timeLoss += tickLoss
		}
		// Stops (§3): an episode is a maximal run of consecutive stopped
		// ticks; count episode starts, attributed to the lane+interval
		// containing the first stopped tick. Note the asymmetry: the stop
		// attributes to the END-of-tick lane while the tick's distance/time
		// follow the traveled lane(s) — the episode is observed there.
		stopped := v.V < metricStopSpeed
		if stopped {
			if ts != nil {
				ts.stoppedS += dt
			}
			if !seen || !prev.stopped {
				k.addStopStart(lane, tick)
				if ts != nil {
					ts.stops++
				}
			}
		}
		cur[v.ID] = mLastObs{lane: lane, s: v.S, v: v.V, vt: v.Type, f: v.F, stopped: stopped, dtShare: shareT, tlShare: shareTL, route: v.Route}
		if ts != nil {
			ts.dest = lane.ID
		}
	}

	// Despawns: IDs seen last tick but gone now left the network this tick.
	// (A vehicle spawned AND despawned within one tick never appears in
	// k.last at all — it is invisible to per-tick observation entirely.
	// Inherent to the observer model, not fixable at this layer. Likewise a
	// vehicle spawned and crossing to a successor within its first tick is
	// attributed wholly to the successor — the origin lane of an injection
	// isn't visible post-hoc; no current network has origin lanes short
	// enough for either case.)
	var gone []uint64
	for id := range k.last {
		if _, ok := cur[id]; !ok {
			gone = append(gone, id)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i] < gone[j] })
	// Vehicles the gridlock escape removed this tick (ADR-0034). They did
	// not drive out of the network, so there is no exit chain to account and
	// no completion to book: the trip closes where the vehicle stood, flagged
	// Stranded, and counts in neither CompletedTrips nor the exit distance.
	// Booking a strand as a despawn would credit it the whole exit-lane
	// length it never drove and hide a gridlock inside the completion rate.
	var stranded map[uint64]bool
	if len(e.StrandedIDs) > 0 {
		stranded = make(map[uint64]bool, len(e.StrandedIDs))
		for _, id := range e.StrandedIDs {
			stranded[id] = true
		}
	}
	for _, id := range gone {
		prev := k.last[id]
		if stranded[id] {
			k.strandedTrips++
			if k.trips {
				if ts := k.tstates[id]; ts != nil {
					ts.stranded = true
					k.emitTrip(id, ts, tick, false)
					delete(k.tstates, id)
				}
			}
			continue
		}
		// Exit-tick travel (exit lanes despawn past Length): the vehicle
		// drove from its last-seen position to the network edge this tick.
		// When the last-seen lane is not an exit lane, the vehicle crossed
		// at least one boundary this tick (netimport lane orderings can
		// reprocess a just-entered exit lane within one boundaries() pass);
		// exitShares books the full chain. The in-network distance is exact
		// (despawn happens past the exit lane's Length, so it is the lane's
		// full length); the in-lane time is estimated from the last
		// observed speed (documented approximation: no sub-tick speed
		// profile is observable).
		shares, exitLane, ok, ambiguous := exitShares(prev)
		if !ok || ambiguous {
			k.droppedCrossings++
		}
		// The time estimate rides the POSITIVE distance only — a negative
		// (refund) share is a correction of last tick's booking, not travel
		// this tick. D keeps the full sum for the trip record.
		var D, dPos float64
		for _, sh := range shares {
			D += sh.d
			if sh.d > 0 {
				dPos += sh.d
			}
		}
		t := dt
		if dPos <= 0 {
			t = 0 // pure overshoot refund: distance-only correction, zero time
		} else if prev.v > 0 && dPos/prev.v < t {
			t = dPos / prev.v
		}
		tl := k.accountShares(shares, prev.vt.Length, t,
			func(l *Lane) float64 { return v0effOf(prev.vt, prev.f, l) }, tick)
		k.timeLoss += tl
		k.completedTrips++
		if k.trips {
			ts := k.tstates[id]
			if ts == nil {
				// Defensive nil-guard: a vehicle in k.last without a trip
				// accumulation (shouldn't happen — NewKernel seeds both).
				// Reconstruct from the seeded observation — origin = dest =
				// the exit lane, entryTick = 0 (entered before the kernel
				// attached). No stop crediting here: both despawn paths
				// share the same exit-tick stop semantics — the final
				// fraction is unobservable.
				ts = &tripState{typeName: prev.vt.Name, origin: prev.lane.ID, dest: prev.lane.ID}
			}
			if exitLane != nil {
				ts.dest = exitLane.ID
			}
			ts.dist += D
			ts.timeLoss += tl
			k.emitTrip(id, ts, tick, true)
			delete(k.tstates, id)
		}
	}
	k.last = cur

	// Denied-entry (latent) demand (§3): vehicles requested but not yet
	// injected accrue wait per origin lane — spawner origins whose scheduled
	// tick has passed (hold-and-retry, spawn.go) and director queue entries
	// past their earliest tick (future directives are scheduled demand, not
	// denied). The spawner holds ONE pending vehicle per origin, but the
	// schedule lag implies a backlog, INTEGRATED per tick so a DemandSchedule
	// rate change applies prospectively only (never revalues already-accrued
	// latent demand): when an origin first becomes overdue, backlog =
	// max(1, lag × dt × rate) — the floor 1 is the held vehicle; each
	// subsequent overdue tick adds rate × dt read at THAT tick; each
	// injection (st.pend's pre-assigned ID changes) subtracts 1, floored at
	// 0 — and while the backlog sits at 0 the origin accrues no wait even
	// if still technically overdue (service has outrun integrated
	// arrivals). The origin accrues dt × backlog per overdue tick. Director
	// entries count singly. DeniedServed counts vehicles that waited ≥ 1
	// tick past their scheduled/request tick and then left the pending
	// state (injected or expired) — spawner: the origin was overdue and its
	// held vehicle injected (the float backlog excess is estimation and is
	// NOT counted); director: a due request ID that leaves the queue. A
	// directive that expires after DirectorSpawnHoldTicks keeps the wait it
	// accrued while held — it WAS denied — and counts as served (expired):
	// dropped demand shows up as wait without pending.
	if e.spawner != nil {
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			if st.pend != nil {
				// Service BEFORE accrual: an overdue vehicle that injects
				// this tick does not accrue a further tick of wait — the
				// tick's accrual below uses the post-service backlog.
				if st.pend.ID != k.spPend[st.lane.ID] {
					if k.spOverdue[st.lane.ID] {
						k.deniedServed[st.lane.ID]++
					}
					if b := k.spBacklog[st.lane.ID] - 1; b > 0 {
						k.spBacklog[st.lane.ID] = b
					} else {
						k.spBacklog[st.lane.ID] = 0
					}
					k.spPend[st.lane.ID] = st.pend.ID
				}
			}
			overdue := st.pend != nil && st.tick <= tick
			if overdue {
				if k.spOverdue[st.lane.ID] {
					k.spBacklog[st.lane.ID] += st.rate * dt
				} else {
					// Newly overdue: the backlog starts at the held vehicle.
					// (The pairing guard means an origin first goes overdue
					// exactly at its scheduled tick — lag is always 0, so
					// there is no lag term to seed; integration does the
					// real work from here.)
					k.spBacklog[st.lane.ID] = 1
				}
				k.deniedWait[st.lane.ID] += dt * k.spBacklog[st.lane.ID]
			}
			k.spOverdue[st.lane.ID] = overdue
		}
	}
	dueNow := make(map[string]string, len(k.dirDue))
	for _, d := range e.dirQueue {
		if d.EarliestTick <= tick {
			laneID := e.Net.Lanes[d.LaneIdx].ID
			k.deniedWait[laneID] += dt
			dueNow[d.RequestID] = laneID
		}
	}
	var servedIDs []string
	for id := range k.dirDue {
		if _, ok := dueNow[id]; !ok {
			servedIDs = append(servedIDs, id)
		}
	}
	sort.Strings(servedIDs)
	for _, id := range servedIDs {
		k.deniedServed[k.dirDue[id]]++
	}
	k.dirDue = dueNow

	// Close intervals whose last observation tick has been seen.
	for _, s := range k.sets {
		s.closeDue(k, tick)
	}
}

// Finalize closes the run at the horizon (ADR-0014 §2/§3): every still-open
// interval is emitted flagged Partial with its actual bounds, every vehicle
// still in-network gets a horizon-partial trip record (Completed = false,
// ExitTick = horizon), and the denied-demand pending counts are stamped.
// Call once, after the last Step/Observe; a second call is a no-op. The
// pairing guard panics if Steps happened between the last Observe and this
// call — they would silently stretch the final interval and mis-stamp trip
// exit ticks.
func (k *Kernel) Finalize(e *Engine) {
	if k.finalized {
		return
	}
	if k.observed && e.Tick != k.lastTick {
		panic(fmt.Sprintf("metrics: Finalize at tick %d but the last Observe was tick %d — observe every Step before finalizing", e.Tick, k.lastTick))
	}
	if !k.observed && e.Tick != 0 {
		panic(fmt.Sprintf("metrics: Finalize at tick %d with no observations", e.Tick))
	}
	k.finalized = true
	tick := e.Tick
	// Horizon overshoot: a vehicle parked past its lane's end at the horizon
	// never gets its next-tick refund — apply it here. Refund the lane the
	// full segment (distance/time/occupancy/time-loss at fraction f of the
	// booked shares); carry the DISTANCE ONLY to the single successor when
	// unambiguous, else drop the carry and count DroppedCrossings (the
	// documented v1 horizon semantics — the carry lane is unobservable).
	if len(k.last) > 0 {
		ids := make([]uint64, 0, len(k.last))
		for id := range k.last {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			prev := k.last[id]
			if prev.s <= prev.lane.Length {
				continue
			}
			over := prev.s - prev.lane.Length
			f := 1.0
			if prev.s > 0 {
				f = over / prev.s
			}
			tRef := f * prev.dtShare
			tlRef := f * prev.tlShare
			k.addSegment(prev.lane, -over, -tRef, tick)
			k.addOccupancy(prev.lane, -tRef*prev.vt.Length, tick)
			k.addTimeLossRaw(prev.lane, -tlRef, tick)
			carried := false
			if len(prev.lane.Successors) == 1 {
				k.addSegment(prev.lane.Successors[0], over, 0, tick)
				carried = true
			} else {
				k.droppedCrossings++
			}
			if k.trips {
				if ts := k.tstates[id]; ts != nil {
					ts.dist -= over
					if carried {
						ts.dist += over
					}
				}
			}
		}
	}
	for _, s := range k.sets {
		end := tick // the horizon is the last covered observation tick
		if s.cfg.LastTick > 0 && s.cfg.LastTick < end {
			end = s.cfg.LastTick
		}
		if s.openFirst <= end {
			// Time-aligned stamp (see closeDue): [openFirst−1, end).
			s.closeInterval(k, s.openFirst-1, end, true)
			s.openFirst = end + 1
		}
	}
	if k.trips {
		ids := make([]uint64, 0, len(k.tstates))
		for id := range k.tstates {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			k.emitTrip(id, k.tstates[id], tick, false)
			delete(k.tstates, id)
		}
	}
	k.activeAtHorizon = len(k.last)
	pending := map[uint64]bool{}
	if e.spawner != nil {
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			if st.pend != nil && st.tick <= tick {
				// Horizon pending = the integrated backlog estimate.
				k.deniedPending[st.lane.ID] += k.spBacklog[st.lane.ID]
			}
			if st.pend != nil {
				// An ID still pending at the horizon is a real future
				// vehicle, never an invisible one. (Director directives
				// carry no pre-assigned vehicle IDs — nothing to exclude.)
				pending[st.pend.ID] = true
				delete(k.pendingGaps, st.pend.ID)
			}
		}
	}
	// Exact invisible-vehicle reconciliation (the kernel is in-package):
	// vehicle IDs are issued sequentially (newVehicle), so every issued
	// but never-first-observed, not-pending ID at the horizon is an
	// invisible same-tick vehicle — including trailing ones the mid-run
	// gap heuristic can't see (no later high ID followed them). The
	// heuristic set is a subset of the exact count, so the counter takes
	// the exact total and the set retires.
	exact := 0
	for id := uint64(1); id < e.nextID; id++ {
		if !k.seenIDs[id] && !pending[id] {
			exact++
		}
	}
	k.droppedCrossings += exact
	k.pendingGaps = map[uint64]bool{}
	for _, d := range e.dirQueue {
		if d.EarliestTick <= tick {
			k.deniedPending[e.Net.Lanes[d.LaneIdx].ID]++
		}
	}
}

// DrainIntervals returns the interval records closed since the last drain,
// sorted by (set id, lane id, begin tick), and clears the pending buffer.
// Totals are accumulator reads and are unaffected by drains.
func (k *Kernel) DrainIntervals() []IntervalRecord {
	recs := k.pendIntervals
	k.pendIntervals = nil
	sort.Slice(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.SetID != b.SetID {
			return a.SetID < b.SetID
		}
		if a.LaneID != b.LaneID {
			return a.LaneID < b.LaneID
		}
		return a.BeginTick < b.BeginTick
	})
	return recs
}

// DrainTrips returns the trip records completed (or horizon-closed) since
// the last drain, sorted by (exit tick, vehicle id), and clears the pending
// buffer.
func (k *Kernel) DrainTrips() []TripRecord {
	recs := k.pendTrips
	k.pendTrips = nil
	sort.Slice(recs, func(i, j int) bool {
		a, b := recs[i], recs[j]
		if a.ExitTick != b.ExitTick {
			return a.ExitTick < b.ExitTick
		}
		return a.VehicleID < b.VehicleID
	})
	return recs
}

// Totals returns the run totals (ADR-0014 §3) as a pure read of the
// accumulators — drains and record emission do not affect it. MeanTimeLossS
// is the total time-loss per trip (completed plus currently tracked), so it
// stays defined when trip-record emission is off. Mid-run values are
// provisional: ActiveAtHorizon stays 0 until Finalize while MeanTimeLossS
// uses the live (completed + tracked) denominator, and pending gap IDs in
// DroppedCrossings reconcile on first observation and at Finalize — a
// polling consumer may see a gap that later resolves.
func (k *Kernel) Totals() Totals {
	t := Totals{
		CompletedTrips:   k.completedTrips,
		StrandedTrips:    k.strandedTrips,
		ActiveAtHorizon:  k.activeAtHorizon,
		VMT:              k.vmt,
		VHT:              k.vht,
		TotalTimeLossS:   k.timeLoss,
		DroppedCrossings: k.droppedCrossings + len(k.pendingGaps),
		DeniedByLane:     map[string]LaneDenied{},
	}
	// Stranded trips belong in the denominator: their time loss is already in
	// k.timeLoss, but the strand takes them out of k.last without adding them
	// to completedTrips, so leaving them out counts their loss against
	// everybody else's trips (ADR-0034). On the 90-minute Chicago baseline
	// that was 1,055 of 10,519 trips — an 11% inflation of the reported mean.
	if n := k.completedTrips + k.strandedTrips + len(k.last); n > 0 {
		t.MeanTimeLossS = k.timeLoss / float64(n)
	}
	ids := make([]string, 0, len(k.deniedWait)+len(k.deniedPending)+len(k.deniedServed))
	seenIDs := map[string]bool{}
	for id := range k.deniedWait {
		seenIDs[id] = true
	}
	for id := range k.deniedPending {
		seenIDs[id] = true
	}
	for id := range k.deniedServed {
		seenIDs[id] = true
	}
	for id := range seenIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ld := LaneDenied{WaitS: k.deniedWait[id], Pending: k.deniedPending[id], Served: k.deniedServed[id]}
		t.DeniedByLane[id] = ld
		t.DeniedWaitS += ld.WaitS
		t.DeniedPending += ld.Pending
		t.DeniedServed += ld.Served
	}
	return t
}

// tripFor returns the open trip accumulation for v, creating it at the first
// observation. A mid-run spawn enters at phase 1 of tick T — sim time
// (T−1)·dt — so EntryTick stamps tick−1 (the observation tick T is its first
// full tick in-network; stamping T would shorten every trip by one tick and
// allow EntryTick == ExitTick). Pre-placed vehicles seed with 0 at
// construction — consistent.
func (k *Kernel) tripFor(v *Vehicle, tick uint64, lane *Lane) *tripState {
	ts, ok := k.tstates[v.ID]
	if !ok {
		ts = &tripState{typeName: v.Type.Name, origin: lane.ID, entryTick: tick - 1}
		k.tstates[v.ID] = ts
	}
	return ts
}

// addSegment folds one (distance, time) segment on lane into the run totals
// and every set accumulator containing that lane at tick.
func (k *Kernel) addSegment(lane *Lane, dist, tim float64, tick uint64) {
	k.vmt += dist
	k.vht += tim
	for _, s := range k.sets {
		if acc := s.accumFor(lane.ID, tick); acc != nil {
			acc.sumDist += dist
			acc.sumTime += tim
		}
	}
}

// addTimeLoss attributes one lane's time-loss share for this tick to that
// lane's interval accumulators (assign-where-it-occurs, ADR-0014 §3) and
// returns it. Non-positive shares attribute nothing; the !(tl > 0) form also
// swallows a NaN (e.g. a degenerate v0eff == 0), so one can never poison the
// accumulators.
func (k *Kernel) addTimeLoss(lane *Lane, tl float64, tick uint64) float64 {
	if !(tl > 0) {
		return 0
	}
	for _, s := range k.sets {
		if acc := s.accumFor(lane.ID, tick); acc != nil {
			acc.sumTimeLoss += tl
		}
	}
	return tl
}

// addOccupancy attributes a vehicle's presence (time × length) to lane.
func (k *Kernel) addOccupancy(lane *Lane, occ float64, tick uint64) {
	for _, s := range k.sets {
		if acc := s.accumFor(lane.ID, tick); acc != nil {
			acc.sumOcc += occ
		}
	}
}

func (k *Kernel) addStopStart(lane *Lane, tick uint64) {
	for _, s := range k.sets {
		if acc := s.accumFor(lane.ID, tick); acc != nil {
			acc.stopStarts++
		}
	}
}

// splitTick accounts a tick in which the vehicle crossed at least one
// lane boundary — on netimport/OSM networks with sub-vehicle-length
// internal lanes it may have traversed several (prev → mid → … → arrival).
// crossedChain resolves the traveled chain; the caller's trip accumulates
// the total. Distance books: prev.Length − prev.s (see the overshoot case
// below) to prev, the FULL length of every intermediate chain lane, and v.S
// (clamped) to the arrival lane. See accountShares for the split semantics.
// A lane change no chain can explain is genuinely disjoint: the defensive
// [prev, cur] split books it anyway and DroppedCrossings makes the
// undercount loud. Returns the tick's clamped time-loss plus the arrival
// lane's booked time/time-loss shares (carried into mLastObs for a future
// overshoot refund).
func (k *Kernel) splitTick(prev mLastObs, cur *Lane, v *Vehicle, dt float64, tick uint64, ts *tripState) (tickLoss, arrT, arrTL float64) {
	chain, ambiguous, ok := crossedChain(prev.lane, cur)
	if !ok {
		k.droppedCrossings++
		chain = []*Lane{prev.lane, cur}
	} else if ambiguous {
		k.droppedCrossings++
	}
	arrival := chain[len(chain)-1]
	d := prev.lane.Length - prev.s
	if d < 0 {
		// Overshoot refund+carry (ADR-0014 §3; documented ONE-TICK
		// attribution lag): the last observation parked the vehicle `over`
		// meters past prev's end (boundaries() never re-clamps; netimport
		// sorts internal lanes last). That tick booked the whole segment to
		// prev.lane; refund fraction f = over/prev.s of it — distance,
		// time, occupancy, and the time-loss share, EXACT against the
		// stored per-vehicle shares — and carry it to the arrival lane
		// (where the vehicle provably was, per this observation).
		over := -d
		f := 1.0
		if prev.s > 0 {
			f = over / prev.s
		}
		tRef := f * prev.dtShare
		tlRef := f * prev.tlShare
		occRef := tRef * v.Type.Length
		k.addSegment(prev.lane, d, -tRef, tick) // refund: distance AND time
		k.addOccupancy(prev.lane, -occRef, tick)
		k.addTimeLossRaw(prev.lane, -tlRef, tick)
		k.addSegment(arrival, 0, tRef, tick) // carry: time only — the distance already rides the arrival's v.S share below
		k.addOccupancy(arrival, occRef, tick)
		k.addTimeLossRaw(arrival, tlRef, tick)
		arrT, arrTL = tRef, tlRef
		// The tick's own travel (past prev's end) splits over the rest of
		// the chain; prev's negative share is already handled.
		shares := make([]laneShare, 0, len(chain))
		var travel float64
		for _, mid := range chain[1 : len(chain)-1] {
			shares = append(shares, laneShare{lane: mid, d: mid.Length})
			travel += mid.Length
		}
		dNew := v.S
		if dNew < 0 {
			dNew = 0
		}
		shares = append(shares, laneShare{lane: arrival, d: dNew})
		travel += dNew
		tickLoss = k.accountShares(shares, v.Type.Length, dt, v.v0eff, tick)
		if travel > 0 {
			arrT += dt * dNew / travel
			arrTL += tickLoss * dNew / travel
		}
		if ts != nil {
			ts.dist += d + travel
		}
		return tickLoss, arrT, arrTL
	}
	shares := make([]laneShare, 0, len(chain))
	shares = append(shares, laneShare{lane: prev.lane, d: d})
	for _, mid := range chain[1 : len(chain)-1] {
		shares = append(shares, laneShare{lane: mid, d: mid.Length})
	}
	if arrival != prev.lane {
		dNew := v.S
		if dNew < 0 {
			dNew = 0
		}
		shares = append(shares, laneShare{lane: arrival, d: dNew})
	}
	tickLoss = k.accountShares(shares, v.Type.Length, dt, v.v0eff, tick)
	if ts != nil {
		for _, sh := range shares {
			ts.dist += sh.d
		}
	}
	// The arrival lane's booked shares (proportional split).
	var D float64
	for _, sh := range shares {
		if sh.d > 0 {
			D += sh.d
		}
	}
	last := shares[len(shares)-1]
	if D > 0 && last.d > 0 {
		arrT = dt * last.d / D
		arrTL = tickLoss * last.d / D
	} else if D == 0 {
		arrT, arrTL = dt, tickLoss // zero-distance rule: all to the last lane
	}
	return tickLoss, arrT, arrTL
}

// addTimeLossRaw adds a SIGNED time-loss delta to lane's accumulators —
// used only by the overshoot refund/carry transfer, where the refund is
// exact against a prior booking (never net-negative within an interval).
func (k *Kernel) addTimeLossRaw(lane *Lane, delta float64, tick uint64) {
	for _, s := range k.sets {
		if acc := s.accumFor(lane.ID, tick); acc != nil {
			acc.sumTimeLoss += delta
		}
	}
}

// laneShare is one lane's distance share of a tick's travel.
type laneShare struct {
	lane *Lane
	d    float64
}

// accountShares books one tick's travel split across lanes (ADR-0014 §3):
// the distance shares are given; time totalT splits proportionally to
// POSITIVE distance (uniform-speed assumption; zero total distance ⇒ all
// time to the LAST lane); occupancy follows the time shares; time-loss is
// ONE clamp per vehicle-tick — tl = totalT − Σ d_i/vd_i over positive
// shares, clamped ≥ 0 — split between the lanes by the same proportion, so
// shares are non-negative AND sum exactly to the tick total.
//
// NEGATIVE shares are overshoot refunds: boundaries() is a single pass with
// no re-clamp, and netimport sorts internal lanes LAST, so an observation
// can park a vehicle past its lane's end (S > Length). The overshoot was
// booked to that lane last tick; the negative share refunds the distance —
// and carries distance ONLY: zero time, no time-loss, no occupancy, and it
// is excluded from the proportional split and the time-loss sum.
// Returns the clamped time-loss.
func (k *Kernel) accountShares(shares []laneShare, vehLen, totalT float64, vd func(*Lane) float64, tick uint64) float64 {
	var D, loss float64
	for _, sh := range shares {
		if sh.d > 0 {
			D += sh.d
			loss += sh.d / vd(sh.lane)
		}
	}
	tickLoss := totalT - loss
	if !(tickLoss > 0) { // single per-tick clamp; also swallows NaN
		tickLoss = 0
	}
	for i, sh := range shares {
		ti, tli := totalT, tickLoss
		switch {
		case D > 0 && sh.d <= 0:
			ti, tli = 0, 0 // overshoot refund: distance only
		case D > 0:
			ti = totalT * sh.d / D
			tli = tickLoss * sh.d / D
		case i < len(shares)-1:
			ti, tli = 0, 0
		}
		k.addSegment(sh.lane, sh.d, ti, tick)
		k.addTimeLoss(sh.lane, tli, tick)
		k.addOccupancy(sh.lane, ti*vehLen, tick)
	}
	return tickLoss
}

// maxCrossDepth caps the successor-chain searches (crossedChain,
// exitResolve) — far past any real internal-lane stack, short of
// pathological graphs.
const maxCrossDepth = 8

// crossedChain finds the successor chain prev → … → arrival the vehicle
// traveled during a lane-changed tick: cur is arrival itself or a lateral
// neighbor of arrival (a same-tick hop after the crossing). Chains are
// enumerated (not first-found): ANY second chain reaching the arrival
// (parallel internal lanes / a diamond) makes the per-lane attribution
// arbitrary — the shortest books and ambiguous=true makes it loud,
// regardless of length equality (same VMT, arbitrary per-lane split).
// Hitting the enumeration or depth cap also sets ambiguous (silent
// truncation would violate the loudness pin). ok = false when no chain
// exists (genuinely disjoint).
func crossedChain(prev, cur *Lane) (chain []*Lane, ambiguous, ok bool) {
	matches := func(l *Lane) bool { return l == cur || l.Left == cur || l.Right == cur }
	chains, truncated := findChains(prev, matches, 3)
	if len(chains) == 0 {
		return nil, false, false
	}
	// ANY second chain reaching the arrival means the per-lane attribution
	// is arbitrary (parallel paths are unobservable post-hoc) — loud
	// regardless of length equality. Truncated enumeration likewise (a
	// differing unenumerated chain could exist).
	ambiguous = truncated || len(chains) > 1
	return chains[0], ambiguous, true
}

// findChains enumerates simple successor chains from `from` to a lane
// satisfying match, depth-capped at maxCrossDepth hops, in fixed Successors
// slice order (deterministic), returned shortest-hop first. A matching lane
// ends its path. Graphs here are tiny (≤ 8 hops, fanout ≤ 3). To keep the
// cap honest it enumerates capN+1 and keeps the first capN: truncated =
// true when a capN+1-th chain exists OR a branch was abandoned at the depth
// cap — the caller treats that as ambiguity (silent truncation would
// violate the loudness pin).
func findChains(from *Lane, match func(*Lane) bool, capN int) (chains [][]*Lane, truncated bool) {
	path := []*Lane{from}
	onPath := map[*Lane]bool{from: true}
	var dfs func(l *Lane)
	dfs = func(l *Lane) {
		if len(chains) > capN {
			return
		}
		if len(path) > maxCrossDepth {
			truncated = true
			return
		}
		for _, s := range l.Successors {
			if onPath[s] {
				continue
			}
			if match(s) {
				chains = append(chains, append(append([]*Lane{}, path...), s))
				continue
			}
			onPath[s] = true
			path = append(path, s)
			dfs(s)
			path = path[:len(path)-1]
			delete(onPath, s)
		}
	}
	dfs(from)
	sort.SliceStable(chains, func(i, j int) bool { return len(chains[i]) < len(chains[j]) })
	if len(chains) > capN {
		return chains[:capN], true
	}
	return chains, truncated
}

// exitShares builds the despawn tick's lane shares: the vehicle drove from
// its last-seen position to the network edge. When the last-seen lane is
// itself the exit, the share is the exact in-network remainder. Otherwise
// the vehicle crossed at least one boundary this tick — but WHICH exit lane
// it left through is unobservable post-hoc (the held turn dies with the
// vehicle). So the chain books ONLY when the resolution is exact and
// unambiguous (exactly one reachable exit AND exactly one chain to it):
// every intermediate lane in full plus the exit lane's full length (despawn
// happens past Length, so the in-network distance is exactly the lane
// length), and exitLane is set for the trip's destination. Anything else —
// zero/multiple reachable exits, a second chain to the one exit, or a
// truncated enumeration — books ONLY the last-seen lane's remainder (the
// trip's dest stays last-seen), matching the loudness: the caller counts a
// DroppedCrossing either way.
func exitShares(prev mLastObs) (shares []laneShare, exitLane *Lane, ok, ambiguous bool) {
	// d may be NEGATIVE when the last observation parked the vehicle past
	// the exit lane's end (no re-clamp in boundaries()) — the overshoot
	// refund, carried distance-only by accountShares.
	d := prev.lane.Length - prev.s
	shares = []laneShare{{lane: prev.lane, d: d}}
	if prev.lane.Exit {
		return shares, prev.lane, true, false
	}
	// A ROUTED vehicle's last hop is known exactly: it despawned at its
	// destination lane's end, whether that lane is an exit (ADR-0019 exit
	// routing) or an interior arrival (ADR-0021). Resolving toward the
	// destination is therefore exact where exitResolve is a guess — it
	// fails outright when several exits are reachable within the depth
	// bound, which on a dense grid is most of them (chi-loop booked 16.9k
	// dropped crossings in 15 sim-minutes). Falls through to the exit
	// search when the destination is not reachable from here — a vehicle
	// that left the network without ever reaching its destination.
	if prev.route != "" {
		if prev.lane.ID == prev.route {
			return shares, prev.lane, true, false
		}
		want := prev.route
		if chain, dest, amb := chainResolve(prev.lane, func(l *Lane) bool { return l.ID == want }); dest != nil {
			if amb {
				return shares, nil, false, true
			}
			for _, l := range chain[1:] {
				shares = append(shares, laneShare{lane: l, d: l.Length})
			}
			return shares, dest, true, false
		}
	}
	chain, exit, amb := exitResolve(prev.lane)
	if exit == nil {
		return shares, nil, false, false
	}
	if amb {
		return shares, nil, false, true
	}
	for _, l := range chain[1:] {
		shares = append(shares, laneShare{lane: l, d: l.Length})
	}
	return shares, exit, true, false
}

// exitResolve finds THE exit lane reachable from `from` within
// maxCrossDepth when exactly one exists, returning the shortest chain to
// it. nil when zero or more than one exit lane is reachable. When several
// chains reach the one exit with different booked length sums (parallel
// internals), ambiguous = true — the shortest books, the caller counts.
func exitResolve(from *Lane) (chain []*Lane, exit *Lane, ambiguous bool) {
	return chainResolve(from, func(l *Lane) bool { return l.Exit })
}

// chainResolve is exitResolve's general form: THE lane matching want that is
// reachable from `from` within maxCrossDepth when exactly one exists, with
// the shortest chain to it. With an id predicate at most one lane can match,
// so the "several targets" rejection only ever fires for the exit search.
func chainResolve(from *Lane, want func(*Lane) bool) (chain []*Lane, target *Lane, ambiguous bool) {
	chains, truncated := findChains(from, want, 3)
	targets := map[*Lane]bool{}
	for _, c := range chains {
		targets[c[len(c)-1]] = true
	}
	if len(targets) != 1 {
		return nil, nil, false
	}
	// ANY second chain to the one exit (or a truncated enumeration) makes
	// the per-lane attribution arbitrary — loud regardless of length
	// equality.
	return chains[0], chains[0][len(chains[0])-1], truncated || len(chains) > 1
}

// emitTrip appends a trip record.
func (k *Kernel) emitTrip(id uint64, ts *tripState, exitTick uint64, completed bool) {
	k.pendTrips = append(k.pendTrips, TripRecord{
		SchemaVersion: 1,
		VehicleID:     id,
		TypeName:      ts.typeName,
		OriginLaneID:  ts.origin,
		DestLaneID:    ts.dest,
		EntryTick:     ts.entryTick,
		ExitTick:      exitTick,
		DistanceM:     ts.dist,
		TimeLossS:     ts.timeLoss,
		Stops:         ts.stops,
		StoppedTimeS:  ts.stoppedS,
		Completed:     completed,
		Stranded:      ts.stranded,
	})
}

// accumFor returns the open-interval accumulator for laneID when tick falls
// inside the set's window, else nil (lane not in the set, or tick outside
// (BeginTick, LastTick]).
func (s *metricSet) accumFor(laneID string, tick uint64) *laneAccum {
	if tick <= s.cfg.BeginTick {
		return nil
	}
	if s.cfg.LastTick > 0 && tick > s.cfg.LastTick {
		return nil
	}
	return s.acc[laneID]
}

// closeDue emits every interval whose last observation tick has been seen:
// the open interval covers observation ticks (openFirst−1, openLast], so it
// closes once tick reaches openLast. Records stamp the SIM-TIME tick grid:
// the interval covering observations (begin, end] stamps [begin, end) —
// BeginTick·dt … EndTick·dt is exactly the covered sim time. A final
// interval truncated by the set's LastTick closes (partial) as soon as
// LastTick itself has been observed — streaming consumers shouldn't wait
// for the run-wide Finalize.
func (s *metricSet) closeDue(k *Kernel, tick uint64) {
	for s.openLast <= tick {
		if s.cfg.LastTick > 0 && s.openLast > s.cfg.LastTick {
			break
		}
		s.closeInterval(k, s.openFirst-1, s.openLast, false)
		s.openFirst = s.openLast + 1
		s.openLast += s.cfg.PeriodTicks
	}
	if s.cfg.LastTick > 0 && tick >= s.cfg.LastTick && s.openFirst <= s.cfg.LastTick {
		s.closeInterval(k, s.openFirst-1, s.cfg.LastTick, true)
		s.openFirst = s.cfg.LastTick + 1
	}
}

// closeInterval emits one record per element lane for the covered
// observation ticks [begin, end) and resets the open accumulators.
func (s *metricSet) closeInterval(k *Kernel, begin, end uint64, partial bool) {
	dur := float64(end-begin) * k.dt
	for _, l := range s.lanes {
		acc := s.acc[l.ID]
		rec := IntervalRecord{
			SchemaVersion: 1,
			SetID:         s.cfg.ID,
			LaneID:        l.ID,
			BeginTick:     begin,
			EndTick:       end,
			Partial:       partial,
			SumDistM:      acc.sumDist,
			SumTimeS:      acc.sumTime,
		}
		denom := l.Length * dur
		if s.cfg.Groups.Edie {
			q := acc.sumDist / denom
			kk := acc.sumTime / denom
			rec.Q, rec.K = &q, &kk
			if acc.sumTime > 0 {
				v := q / kk
				rec.V = &v
			}
		}
		if s.cfg.Groups.Occupancy {
			occ := acc.sumOcc / denom
			rec.Occupancy = &occ
		}
		if s.cfg.Groups.Stops {
			n := acc.stopStarts
			rec.Stops = &n
		}
		if s.cfg.Groups.TimeLoss {
			tl := acc.sumTimeLoss
			rec.TimeLossS = &tl
		}
		k.pendIntervals = append(k.pendIntervals, rec)
		*acc = laneAccum{}
	}
}

// v0effOf is the desired speed of a vehicle with type vt and speed factor f
// on lane — Vehicle.v0eff for a vehicle that is no longer observable
// (despawn-tick accounting).
func v0effOf(vt *VehicleType, f float64, lane *Lane) float64 {
	v0 := vt.V0
	if lane.SpeedLimit < v0 {
		v0 = lane.SpeedLimit
	}
	return f * v0
}
