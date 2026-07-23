package engine

import (
	"encoding/binary"
	"hash/fnv"
	"math"
	"sort"
)

// Params holds the engine's policy/numerics knobs. Tick length is a
// scenario parameter (ADR-0005), not a constant.
type Params struct {
	Dt               float64 // tick length (s); validated default 0.1 (ADR-0005)
	BSafe            float64 // MOBIL safety bound (m/s²); paper value 4
	Politeness       float64 // MOBIL p; task spec 0.3
	LCThreshold      float64 // MOBIL Δa_th (m/s²); task spec 0.2
	MinGapLC         float64 // hard lateral gap for discretionary changes (m)
	MinGapMerge      float64 // hard lateral gap for mandatory merges (m)
	MergeZone        float64 // distance from a dead-lane end over which merge urgency ramps (m)
	MergeUrgencyGain float64 // bSafe relaxation at full urgency (m/s²)
	LCCooldown       uint64  // ticks a vehicle must wait between lane changes
	SpawnCooldown    uint64  // ticks a fresh spawn may not change lanes
	SpeedFactorSigma float64 // σ of the gaussian desired-speed factor
	SpawnJitter      float64 // ± fraction applied to the mean spawn interval
}

// DefaultParams returns the validated M1 defaults.
func DefaultParams() Params {
	return Params{
		Dt:               0.1, // 100 ms, validated by Kesting & Treiber (ADR-0005)
		BSafe:            4,
		Politeness:       0.3,
		LCThreshold:      0.2,
		MinGapLC:         0.5,
		MinGapMerge:      0.3,
		MergeZone:        200,
		MergeUrgencyGain: 5,
		LCCooldown:       20,
		SpawnCooldown:    10,
		SpeedFactorSigma: 0.1,
		SpawnJitter:      0.3,
	}
}

// Stats accumulates run-wide observables. MinGap/Collisions are the model-
// pathology indicators (negative gap = overlap = collision by definition).
type Stats struct {
	MinGap     float64 // smallest adjacent-pair gap observed over the run (m)
	Collisions int     // adjacent-pair observations below -0.01 m
	// CollisionsBySection localizes collision observations by lane section
	// (diagnostic only — not part of the CRC'd world state).
	CollisionsBySection map[string]int
	LaneChanges         int
	Spawned             int
	Despawned           int
	WallHits            int // vehicles clamped at a dead-lane wall (should stay 0)
}

// Engine is the single-threaded simulation kernel. No goroutines, no wall
// clock, no syscalls: sim time IS the tick counter (ADR-0005).
type Engine struct {
	Params Params
	Seed   uint64
	Net    *Network
	Tick   uint64

	order   []*Vehicle          // all live vehicles, in spawn (append) order
	index   map[uint64]*Vehicle // live vehicles by ID (lookup only — never iterated, ADR-0005)
	nextID  uint64
	scen    Scenario
	spawner *Spawner

	crc  uint64   // rolling chained state CRC (ADR-0005)
	CRCs []uint64 // per-tick CRC sequence — the replay oracle

	intentQ   []KeyedIntent  // buffered, applied at the next tick boundary
	applied   []TickedIntent // applied during the last Step (reused each Step)
	IntentLog []TickedIntent // arbitrated log in application order (ADR-0005)

	dirNew        []TickedSpawn // buffered director directives, applied next boundary
	dirQueue      []TickedSpawn // accepted directives awaiting injection (FIFO)
	appliedSpawns []TickedSpawn // entered the queue during the last Step (reused)
	SpawnLog      []TickedSpawn // every accepted directive, in application order

	Stats Stats
}

// NewEngine builds a kernel from a run spec: network, initial population,
// and spawner schedule, all derived deterministically from the spec + seed.
func NewEngine(spec RunSpec) (*Engine, error) {
	net, err := BuildNet(spec.Net)
	if err != nil {
		return nil, err
	}
	// Fixed-time signal programs (ADR-0011): durations ride in seconds in
	// the file; the tick grid is known only here. No-op without programs.
	if err := net.compileSignalTicks(spec.Params.Dt); err != nil {
		return nil, err
	}
	e := &Engine{
		Params: spec.Params,
		Seed:   spec.Seed,
		Net:    net,
		index:  map[uint64]*Vehicle{},
		nextID: 1,
		scen:   spec.Scen,
		Stats:  Stats{MinGap: math.Inf(1)},
	}
	if len(e.scen.Types) == 0 {
		e.scen.Types = []*VehicleType{&Car}
	}

	// Initial population: the ring starts with InitialVehicles evenly spaced
	// at the IDM equilibrium speed for that gap (uniform-flow fixed point).
	if spec.Net.Kind == "ring" && spec.Scen.InitialVehicles > 0 {
		lane := net.Lanes[0]
		t := e.scen.Types[0]
		n := spec.Scen.InitialVehicles
		spacing := lane.Length / float64(n)
		v0 := t.V0
		if lane.SpeedLimit < v0 {
			v0 = lane.SpeedLimit
		}
		veq := equilibriumSpeed(spacing-t.Length, v0, t)
		for i := 0; i < n; i++ {
			v := e.newVehicle()
			v.Type, v.TypeIdx = t, 0
			v.Lane = lane
			v.S = float64(i) * spacing
			v.V = veq
			v.F = 1
			e.register(v)
		}
	}

	e.spawner = newSpawner(e.scen, net)
	if e.spawner != nil {
		e.spawner.init(e)
	}
	e.rebuildOccupancy()
	return e, nil
}

// newVehicle allocates the next sequential ID and its derived stream.
func (e *Engine) newVehicle() *Vehicle {
	v := &Vehicle{ID: e.nextID, rng: deriveStream(e.Seed, e.nextID)}
	e.nextID++
	return v
}

// register adds v to the live set (order + ID index). Spawn order is NOT ID
// order: the per-origin spawn schedules interleave, so e.order is kept in
// append order — which the CRC's canonical serialization has always used —
// and ID lookup goes through e.index. The vehicle is ALSO inserted into its
// lane's occupancy immediately: lane.vehs is only rebuilt after the events
// phase, and injections later in the same phase (a held directive clearing
// alongside a spawner entry, a backlog of held directives in one tick) must
// see the fresh vehicle or they stack into the same slot (the stop-control
// fixture's identical-position collision clusters).
func (e *Engine) register(v *Vehicle) {
	e.order = append(e.order, v)
	e.index[v.ID] = v
	if v.Lane != nil {
		a := v.Lane.vehs
		i := sort.Search(len(a), func(i int) bool {
			return a[i].S > v.S || (a[i].S == v.S && a[i].ID > v.ID)
		})
		v.Lane.vehs = append(v.Lane.vehs, nil)
		copy(v.Lane.vehs[i+1:], v.Lane.vehs[i:])
		v.Lane.vehs[i] = v
	}
}

// AddInitialVehicle places a vehicle before the run starts (test harness).
// f scales the desired speed (1 = no heterogeneity).
func (e *Engine) AddInitialVehicle(lane *Lane, typeIdx int, s, v, f float64) *Vehicle {
	veh := e.newVehicle()
	veh.Type, veh.TypeIdx = e.scen.Types[typeIdx], typeIdx
	veh.Lane = lane
	veh.S = s
	veh.V = v
	veh.F = f
	e.register(veh)
	e.rebuildOccupancy()
	return veh
}

// Vehicles returns the live vehicles in spawn (append) order — deterministic
// from the complete run inputs (seed, demand, recorded intents). Read-only;
// do not mutate.
func (e *Engine) Vehicles() []*Vehicle { return e.order }

// CRC returns the rolling state CRC after the last executed tick.
func (e *Engine) CRC() uint64 { return e.crc }

// Step advances the simulation exactly one tick. Phase order:
//
//  1. events: deterministic spawner, then director spawn directives
//     (ADR-0005: events fire before the sweep; the director queue's fixed
//     point is documented in director.go)
//  2. intents: buffered controller intents applied in deterministic order
//     (ADR-0006 §4), recorded in the arbitrated log
//  3. IDM acceleration per vehicle from a consistent snapshot (intent
//     overrides where commanded)
//  4. ballistic integration + stopping override (all vehicles move at once)
//  5. lane-boundary handoff: successor crossing, exit despawn, wall clamp
//  6. lane changes: commanded hops first per vehicle, else MOBIL
//  7. metrics + rolling state CRC
func (e *Engine) Step() {
	e.Tick++
	for _, v := range e.order {
		v.reqAccOK, v.reqLane = false, 0
	}
	if e.spawner != nil {
		e.spawner.step(e)
	}
	e.stepDirectorSpawns()
	e.applyIntents()
	e.rebuildOccupancy()
	e.computeAccels()
	e.integrate()
	e.boundaries()
	e.rebuildOccupancy()
	e.laneChanges()
	e.updateStats()
	e.crc = e.computeCRC()
	e.CRCs = append(e.CRCs, e.crc)
}

// rebuildOccupancy re-sorts every lane's vehicle list by s (ties by ID —
// deterministic even in degenerate states).
func (e *Engine) rebuildOccupancy() {
	for _, l := range e.Net.Lanes {
		l.vehs = l.vehs[:0]
	}
	for _, v := range e.order {
		l := v.Lane
		l.vehs = append(l.vehs, v)
	}
	for _, l := range e.Net.Lanes {
		sort.Slice(l.vehs, func(i, j int) bool {
			if l.vehs[i].S != l.vehs[j].S {
				return l.vehs[i].S < l.vehs[j].S
			}
			return l.vehs[i].ID < l.vehs[j].ID
		})
	}
}

// The leader search walks at least baseLaneHops lane boundaries (the M1
// bound — long-lane behavior is unchanged) and beyond them while it has
// not yet seen maxSightM ahead, never past maxLaneHops. Sight must cover
// an emergency stop from the fastest desired speed the type mix allows
// (1.3 × 27.78 ≈ 36 m/s → 72 m at emergencyDecel, plus margin): on
// fragment-dense imported networks four hops can be under 20 m, and
// vehicles at 30+ m/s met standing queues they could no longer brake for
// (fixture collision sections, 2026-07-23).
const baseLaneHops = 4
const maxLaneHops = 12
const maxSightM = 100.0

// leaderAt resolves the car-following leader for a hypothetical vehicle at
// position s on lane: the next vehicle ahead on the lane, else the first
// vehicle on the successor lane with the gap measured through the connection,
// walking further successors if the next lane is empty (bounded by
// maxLaneHops AND maxSightM). skip is excluded
// (a vehicle is never its own leader). On the ring this wraps around; a lone
// ring vehicle resolves to "no leader" (free flow).
func (e *Engine) leaderAt(lane *Lane, s float64, skip *Vehicle) LeaderInfo {
	a := lane.vehs
	i := sort.Search(len(a), func(i int) bool { return a[i].S > s })
	for i < len(a) && a[i] == skip {
		i++
	}
	if i < len(a) {
		l := a[i]
		return LeaderInfo{OK: true, Gap: l.S - l.Type.Length - s, V: l.V}
	}
	dist := lane.Length - s
	cur := lane
	for hops := 0; hops < maxLaneHops && (hops < baseLaneHops || dist < maxSightM); hops++ {
		if len(cur.Successors) == 0 {
			return LeaderInfo{}
		}
		next := cur.Successors[0]
		if len(next.vehs) > 0 {
			l := next.vehs[0]
			if l == skip {
				return LeaderInfo{}
			}
			return LeaderInfo{OK: true, Gap: dist + l.S - l.Type.Length, V: l.V}
		}
		if next == lane {
			return LeaderInfo{} // closed loop with nobody ahead
		}
		dist += next.Length
		cur = next
	}
	return LeaderInfo{}
}

// leader resolves v's actual leader on its current lane.
func (e *Engine) leader(v *Vehicle) LeaderInfo {
	return e.leaderAt(v.Lane, v.S, v)
}

// prevFollower resolves the nearest follower behind a hypothetical rear
// bumper at position rear on lane when no vehicle is behind on the lane
// itself: the last vehicle of the predecessor lane, walking further empty
// predecessors (mirror of leaderAt, same maxLaneHops/maxSightM bounds;
// gaps measured through the connections). Lane-change safety checks use
// it so hops near a lane start cannot land on an unchecked cross-boundary
// follower.
func (e *Engine) prevFollower(lane *Lane, rear float64) (f *Vehicle, fLane *Lane, gap float64, ok bool) {
	dist := rear
	cur := lane
	for hops := 0; hops < maxLaneHops && (hops < baseLaneHops || dist < maxSightM); hops++ {
		if len(cur.Prevs) == 0 {
			return nil, nil, 0, false
		}
		prev := cur.Prevs[0]
		if prev == lane {
			return nil, nil, 0, false // closed loop with nobody behind
		}
		if len(prev.vehs) > 0 {
			f := prev.vehs[len(prev.vehs)-1]
			return f, prev, dist + prev.Length - f.S, true
		}
		dist += prev.Length
		cur = prev
	}
	return nil, nil, 0, false
}

// computeAccels evaluates the longitudinal decision for every vehicle from
// the same snapshot (ID order — the result is order-independent, but
// discipline is cheap). Priority per vehicle (ADR-0008):
//
//  1. a commanded accel intent this tick (clamped to the physics envelope —
//     the engine's MMU-pattern clamp)
//  2. a persistent cruise setpoint (servo)
//  3. the reference IDM policy — ONLY under the idm uncontrolled-policy
//     (the headless harness, ADR-0008 clarification)
//  4. nothing: holdlast-policy vehicles without control coast (no driving
//     logic in the engine in live runs; the contract layer bridges them)
func (e *Engine) computeAccels() {
	idm := e.idmPolicy()
	for _, v := range e.order {
		switch {
		case v.reqAccOK:
			v.Acc = v.intentAccel()
		case v.CruiseOK:
			v.Acc = v.cruiseAccel(e.Params.Dt)
		case idm:
			v.Acc = e.accelOnLane(v, v.Lane, e.leader(v))
		default:
			v.Acc = 0
		}
		// Junction right-of-way guardrail (ADR-0010): caps every control
		// path, so all controllers inherit it.
		if w, ok := e.rowGate(v); ok && w < v.Acc {
			v.Acc = w
		}
	}
}

// idmPolicy reports whether the in-kernel reference policy drives
// uncontrolled vehicles (Scenario.UncontrolledPolicy; ADR-0008 clarification:
// the harness flag exists only where no bus is attached).
func (e *Engine) idmPolicy() bool { return e.scen.UncontrolledPolicy != PolicyHoldLast }

// accelOnLane evaluates the shared longitudinal primitive (policy.go) for v
// on lane: IDM plus, on dead-end lanes, the "virtual standing vehicle at the
// lane end" (KB pattern: merge pressure becomes ordinary car-following
// deceleration). The wall applies to any accel evaluated on that lane, real
// or hypothetical, so lane changes into a dropped lane are also discouraged.
func (e *Engine) accelOnLane(v *Vehicle, lane *Lane, lead LeaderInfo) float64 {
	return accelEval(v.Type, v.v0eff(lane), v.V, v.S, lane.Length, lane.EndWall, lead)
}

// accelPair evaluates f's accel toward an explicitly given leader (used by
// MOBIL hypotheticals), including the lane's wall if any.
func (e *Engine) accelPair(f *Vehicle, lane *Lane, gap, leadV float64, ok bool) float64 {
	return e.accelOnLane(f, lane, LeaderInfo{OK: ok, Gap: gap, V: leadV})
}

// integrate applies the ballistic update with the mandatory stopping
// override (Treiber & Kanagaraj Eq. 15): v' = v + a·Δt; if that would go
// negative, the vehicle stops exactly (x += −v²/2a, v = 0).
func (e *Engine) integrate() {
	dt := e.Params.Dt
	for _, v := range e.order {
		a := v.Acc
		if v.V+a*dt < 0 {
			if a < 0 {
				v.S += -0.5 * v.V * v.V / a
			}
			v.V = 0
		} else {
			v.S += v.V*dt + 0.5*a*dt*dt
			v.V += a * dt
		}
	}
}

// boundaries handles end-of-lane handoff, downstream lanes first so a
// vehicle crosses at most one boundary per tick:
//
//	Exit lane:        despawn (left the network)
//	lane w/ successor: cross over, s -= lane.Length — a held turn intent
//	                  (ADR-0008 §2) chooses the successor and is consumed
//	EndWall lane:     clamp at the wall (pathology guard; counted)
func (e *Engine) boundaries() {
	despawned := false
	for i := len(e.Net.Lanes) - 1; i >= 0; i-- {
		lane := e.Net.Lanes[i]
		for _, v := range e.order {
			if v.Lane != lane || v.S <= lane.Length {
				continue
			}
			switch {
			case lane.Exit:
				v.Lane = nil
				despawned = true
				e.Stats.Despawned++
			case len(lane.Successors) > 0:
				v.S -= lane.Length
				next := pickSuccessor(lane, v.HeldTurn)
				v.Lane = next
				v.HeldTurn = 0 // turn-at-junction is held until consumed
				// Stop-line duty is per junction APPROACH (ADR-0010), and an
				// approach spans its fragment stubs (gateTarget measures the
				// line through them) — reset only when the junction itself
				// is consumed, or stubbed stop junctions double-stop.
				if next.Internal {
					v.stopDone = false
				}
			case lane.EndWall:
				v.S = lane.Length
				v.V = 0
				e.Stats.WallHits++
			}
		}
	}
	if despawned {
		kept := e.order[:0]
		for _, v := range e.order {
			if v.Lane != nil {
				kept = append(kept, v)
			}
		}
		e.order = kept
		e.index = make(map[uint64]*Vehicle, len(kept))
		for _, v := range kept {
			e.index[v.ID] = v
		}
	}
}

// pickSuccessor chooses the next lane at a junction. Successors are ordered
// left-to-right by convention: a held turn of +1 (left) takes the first, −1
// (right) the last; no held turn takes the default first. M1 networks have
// at most one successor, so this is a no-op there.
func pickSuccessor(lane *Lane, heldTurn int) *Lane {
	n := len(lane.Successors)
	switch {
	case heldTurn > 0:
		return lane.Successors[0]
	case heldTurn < 0:
		return lane.Successors[n-1]
	default:
		return lane.Successors[0]
	}
}

// collisionGap is the overlap threshold: gap below this counts as a
// collision observation (same −0.01 m convention as the JS prototype).
const collisionGap = -0.01

// updateStats tracks the minimum adjacent-pair gap over the network,
// including the pair across a lane/successor boundary (e.g. ring wrap).
// Cross-boundary observations are filed under the successor's section — that
// is where the follower is heading and where the leader sits.
func (e *Engine) updateStats() {
	for _, l := range e.Net.Lanes {
		for i := 0; i+1 < len(l.vehs); i++ {
			e.observeGap(l.vehs[i+1].S-l.vehs[i+1].Type.Length-l.vehs[i].S, l.Section)
		}
		if len(l.vehs) == 0 || len(l.Successors) == 0 {
			continue
		}
		succ := l.Successors[0]
		if len(succ.vehs) == 0 {
			continue
		}
		last, first := l.vehs[len(l.vehs)-1], succ.vehs[0]
		if first == last {
			continue // single vehicle on the ring
		}
		e.observeGap((l.Length-last.S)+first.S-first.Type.Length, succ.Section)
	}
}

func (e *Engine) observeGap(g float64, section string) {
	if g < e.Stats.MinGap {
		e.Stats.MinGap = g
	}
	if g < collisionGap {
		e.Stats.Collisions++
		if e.Stats.CollisionsBySection == nil {
			e.Stats.CollisionsBySection = map[string]int{}
		}
		e.Stats.CollisionsBySection[section]++
	}
}

// computeCRC hashes the canonical state serialization with FNV-1a 64:
//
//	prev CRC (rolling chain, ADR-0005) | tick | per vehicle (in spawn
//	order — deterministic from the seed; the CRC's canonical order since
//	M1) : ID, lane index, type index, Float64bits(S), Float64bits(V),
//	Float64bits(F), cooldown, RNG draw count | per origin lane: pending
//	spawn (vehicle ID, scheduled tick) | pending director directives when
//	any (applied tick, lane/type index, earliest tick, request ID — the
//	section is omitted with an empty queue, so director-free runs keep
//	the M3 byte stream).
//
// Fixed iteration order + float64 bits via encoding/binary make the bytes
// canonical; the draw counts catch RNG-consumption divergence between
// replays.
//
// The persistent controller axes (cruise setpoint, held turn, signals,
// route) are deliberately NOT folded in: the CRC stays the M3 trajectory
// oracle so NATS-off baselines keep matching across milestones; controller
// state reaches the CRC through its trajectory effects (S/V/lane) and is
// restored bit-exactly by keyframes (TSKF v2/v3) for seek fidelity.
func (e *Engine) computeCRC() uint64 {
	h := fnv.New64a()
	var b [8]byte
	u := func(x uint64) { binary.LittleEndian.PutUint64(b[:], x); h.Write(b[:]) }
	f := func(x float64) { u(math.Float64bits(x)) }

	u(e.crc)
	u(e.Tick)
	for _, v := range e.order {
		u(v.ID)
		u(uint64(v.Lane.Index))
		u(uint64(v.TypeIdx))
		f(v.S)
		f(v.V)
		f(v.F)
		u(v.Cooldown)
		u(v.rng.Draws())
	}
	if e.spawner != nil {
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			u(st.pend.ID)
			u(st.tick)
		}
	}
	// Pending director directives (only when a director is attached — the
	// fold is skipped entirely with an empty queue, so director-free runs
	// keep the M3 byte stream). Queue order is canonical (FIFO).
	if len(e.dirQueue) > 0 {
		u(uint64(len(e.dirQueue)))
		for _, d := range e.dirQueue {
			u(d.Tick)
			u(uint64(d.LaneIdx))
			u(uint64(d.TypeIdx))
			u(d.EarliestTick)
			h.Write([]byte(d.RequestID))
		}
	}
	return h.Sum64()
}
