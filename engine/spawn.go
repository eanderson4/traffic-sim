package engine

import (
	"math"
	"sort"
)

// Scenario describes everything a run consumes beyond the network: spawn
// demand, density cap, initial population, and the vehicle-type mix. It is
// part of the run log, so replay regenerates the identical demand.
type Scenario struct {
	SpawnRatePerLaneHour float64            // per origin lane; 0 disables the spawner
	SpawnRates           map[string]float64 // optional per-origin-lane rates (veh/h) by lane ID; overrides SpawnRatePerLaneHour per lane (lookup only — never iterated, ADR-0005)
	DensityTargetPerKm   float64            // stop injecting at/above this network density (veh/lane-km); 0 = uncapped
	Types                []*VehicleType     // spawn mix; empty → [Car]
	TypeWeights          []float64          // optional spawn weights aligned with Types; nil or wrong length → uniform
	DemandSchedule       []DemandStep       // optional multiplicative demand changes at ticks (sorted); part of the spec, replay-identical
	InitialVehicles      int                // ring: pre-placed evenly spaced vehicles
	// UncontrolledPolicy selects what vehicles WITHOUT an applied controller
	// intent do (ADR-0008 §6 + clarification): PolicyIDM keeps the in-kernel
	// reference IDM/MOBIL harness driving them (physics validation and CI;
	// valid only where no bus is attached — the "" zero value means the
	// M1–M3 test suites stay bit-identical); PolicyHoldLast is live mode,
	// where uncontrolled vehicles coast while the contract layer bridges on
	// hold-last and the default driver re-claims them.
	UncontrolledPolicy string
}

// Uncontrolled-policy values (Scenario.UncontrolledPolicy).
const (
	// PolicyIDM is the headless-harness policy: the kernel's reference
	// IDM/MOBIL drives every vehicle without an applied intent.
	PolicyIDM = "idm"
	// PolicyHoldLast is the live-run policy: no driving logic in the engine —
	// vehicles without an applied intent coast (accel 0, no lane changes)
	// until the contract layer's hold-last and re-claim restore control.
	PolicyHoldLast = "holdlast"
)

// DemandStep scales all origin rates by Scale at Tick (e.g. warm-up overload
// to build a queue, then window demand — the I-80 credibility scenario).
type DemandStep struct {
	Tick  uint64
	Scale float64
}

// Spawner is the deterministic vehicle injector: a fixed schedule per origin
// lane — mean interval from the rate, jittered through the *incoming*
// vehicle's own stream (task spec: spawn jitter goes through the owning
// vehicle's stream). IDs are sequential; a pending entry holds its
// pre-assigned ID and stream until it enters the network.
type Spawner struct {
	target  float64 // veh per lane-km
	sched   []DemandStep
	next    int // next schedule step to apply
	origins []originState
}

type originState struct {
	lane *Lane
	rate float64  // veh/s on this origin lane
	pend *Vehicle // pre-created: owns its stream and sequential ID
	tick uint64   // scheduled spawn tick
}

func newSpawner(sc Scenario, net *Network) *Spawner {
	if len(net.Origins) == 0 {
		return nil
	}
	sp := &Spawner{target: sc.DensityTargetPerKm, sched: sc.DemandSchedule}
	for _, l := range net.Origins {
		r := sc.SpawnRatePerLaneHour
		if v, ok := sc.SpawnRates[l.ID]; ok {
			r = v
		}
		if r <= 0 {
			continue // origin with no demand never spawns
		}
		sp.origins = append(sp.origins, originState{lane: l, rate: r / 3600})
	}
	if len(sp.origins) == 0 {
		return nil
	}
	return sp
}

// init schedules the first spawn per origin lane, staggered uniformly within
// the first mean interval (jitter from each pending vehicle's own stream).
func (sp *Spawner) init(e *Engine) {
	for i := range sp.origins {
		st := &sp.origins[i]
		mean := 1 / (st.rate * e.Params.Dt) // mean interval in ticks
		pend := e.newVehicle()
		st.pend = pend
		st.tick = 1 + uint64(pend.rng.Float64()*mean)
	}
}

// injectionPlan decides whether the pending vehicle v (Lane, Type and the
// injection position S set, not yet registered) may enter this tick, and
// the physical entry cap. ok=false holds the spawn — unmet demand carries
// over. The entry is safe when v can brake COMFORTABLY (its Type.B) to
// whichever constraint is nearer:
//
//   - the nearest leader measured THROUGH the connection (leaderAt from
//     the lane start) — the old rule (8+0.8·v buffer against vehicles ON
//     the origin lane only) never looked past the lane end, so entries at
//     full speed met queues standing in the next lane (the origin-overlap
//     collisions the fixtures counted by origin section);
//   - the junction stop-line wall when the upcoming junction would hold
//     (junctionHold: red/amber light, stop sign, right-of-way conflict,
//     blocked box) — the old rule injected at full desired speed onto
//     netimport's 0.2 m junction-boundary stubs facing a red light, which
//     is where the fixtures' mass red crossings came from.
//
// The entry speed caps at what can still stop short of the constraint:
// v² ≤ v_leader² + 2·B·(gap − s0). There is no speed floor at a PORTAL:
// origin lanes have no predecessors, so nothing can rear-end a slow entry,
// and joining a queue tail at crawl speed is how real queues grow (the
// prototype's 8 m/s floor only starved congested origins). F-free by
// design — the plan may be probed for a vehicle whose desired-speed factor
// has not been drawn (the director probe); callers apply their own F via
// min(cap, v0eff) at injection.
//
// INTERIOR injection (v.S > 0, ADR-0021) has traffic behind it, so the
// portal reasoning above does not hold: the entry additionally requires
// that the nearest follower can brake comfortably to the injected rear
// bumper. Without it a garage exit materializes in front of moving traffic
// and is rear-ended — the one failure mode a portal can't have.
func (e *Engine) injectionPlan(v *Vehicle) (float64, bool) {
	lane := v.Lane
	gap := math.Inf(1)
	vl := 0.0
	// leaderAt from a hair BEFORE the injection point: its strict S > s
	// search would miss a vehicle injected at exactly this S earlier in the
	// same events phase (the director queue's same-tick backlog stack).
	if li := e.leaderAt(lane, v.S-1e-9, nil); li.OK {
		gap = li.Gap
		vl = li.V
	}
	if v.S > 0 && !e.rearClear(v) {
		return 0, false
	}
	if next, dist := e.gateTarget(v); next != nil && dist < gap && e.junctionHold(v, next) {
		gap = dist
		vl = 0
	}
	if math.IsInf(gap, 1) {
		return gap, true // unconstrained: the caller caps at the driver's desired speed
	}
	room := gap - v.Type.S0
	if room <= 0 {
		return 0, false
	}
	return math.Sqrt(vl*vl + 2*v.Type.B*room), true
}

// rearClear reports whether an interior injection of v (Lane and S set,
// not yet registered) leaves its nearest follower able to brake COMFORTABLY
// — the mirror of the leader rule, with the same Type.B/S0 comfort
// criterion applied to the follower's own type. The follower is searched on
// the injection lane first, then through predecessors (prevFollower, same
// sight bounds as the leader walk) so a vehicle about to cross in from
// upstream is not missed.
//
// Note the asymmetry with the leader rule: an unsafe leader gap only CAPS
// the entry speed, but an unsafe follower gap denies the entry outright.
// There is no entry speed that fixes being materialized on top of someone —
// the vehicle simply waits for a gap, which is what a car nosing out of a
// garage actually does. Denials carry over as demand exactly like a blocked
// portal (director.go's hold-and-retry).
func (e *Engine) rearClear(v *Vehicle) bool {
	rear := v.S - v.Type.Length
	// Footprint guard, and it must come FIRST. The two gap searches start
	// from opposite bumpers — leaderAt from v.S, followerAt from rear — so a
	// vehicle whose FRONT bumper lies inside (rear, v.S] is behind the leader
	// search and ahead of the follower search, invisible to both. That window
	// is one injected-vehicle length wide (12 m for a truck) and it is
	// precisely the space the injection is about to occupy, so it is a
	// denial, not a gap to be measured: no entry speed fixes materializing on
	// top of someone. Portals cannot hit this (v.S = 0 puts the window at
	// negative S), which is why the rule lives here and not in injectionPlan.
	if a := v.Lane.vehs; len(a) > 0 {
		if i := sort.Search(len(a), func(i int) bool { return a[i].S > rear }); i < len(a) && a[i].S <= v.S {
			return false
		}
	}
	f, _, gap, ok := e.followerAt(v.Lane, rear)
	if !ok {
		return true // nobody behind within sight
	}
	room := gap - f.Type.S0
	if room <= 0 {
		return false
	}
	// The follower must be able to shed its speed over the room available:
	// v_f² ≤ 2·B_f·room, the leader rule read from the other side.
	return f.V*f.V <= 2*f.Type.B*room
}

// followerAt resolves the nearest follower behind a hypothetical rear
// bumper at `rear` on lane: the last vehicle strictly behind it ON the lane,
// else prevFollower's cross-boundary walk. Gap is the clear distance from
// the follower's front bumper to `rear`.
func (e *Engine) followerAt(lane *Lane, rear float64) (f *Vehicle, fLane *Lane, gap float64, ok bool) {
	a := lane.vehs
	i := sort.Search(len(a), func(i int) bool { return a[i].S > rear })
	if i > 0 {
		b := a[i-1]
		return b, lane, rear - b.S, true
	}
	return e.prevFollower(lane, rear)
}

// step attempts the scheduled spawn on each origin lane, in fixed lane
// order. A blocked entry (density cap, unsafe injection) holds and retries
// next tick — the schedule is demand, and unmet demand carries over.
// DemandSchedule steps fire first; an already-pending spawn keeps its tick
// (future intervals use the new rate).
func (sp *Spawner) step(e *Engine) {
	for sp.next < len(sp.sched) && e.Tick >= sp.sched[sp.next].Tick {
		for i := range sp.origins {
			sp.origins[i].rate *= sp.sched[sp.next].Scale
		}
		sp.next++
	}
	for i := range sp.origins {
		st := &sp.origins[i]
		if e.Tick < st.tick {
			continue
		}
		if sp.target > 0 && float64(len(e.order))/e.Net.TotalLaneKm() >= sp.target {
			continue
		}

		v := st.pend
		if v.Type == nil {
			// Type and desired-speed factor derive from the vehicle's SIDE
			// stream (spawnAttrStream), not its main stream: the draw is
			// idempotent across holds AND keyframe restore — a pend
			// persisted mid-hold (keyframe keeps ID + main stream only)
			// replays the identical Type/F with nothing to persist.
			attr := spawnAttrStream(e.Seed, v.ID)
			types := e.scen.Types
			ti := 0
			if len(types) > 1 {
				// One uniform draw either way; weights only remap it.
				u := attr.Float64()
				if w := e.scen.TypeWeights; len(w) == len(types) {
					tot := 0.0
					for _, x := range w {
						tot += x
					}
					u *= tot
					cum := 0.0
					ti = len(types) - 1
					for i, x := range w {
						cum += x
						if u < cum {
							ti = i
							break
						}
					}
				} else {
					ti = int(u * float64(len(types)))
					if ti >= len(types) {
						ti = len(types) - 1
					}
				}
			}
			v.Type, v.TypeIdx = types[ti], ti
			v.F = 1 + e.Params.SpeedFactorSigma*attr.Norm()
			if v.F < 0.8 {
				v.F = 0.8
			} else if v.F > 1.3 {
				v.F = 1.3
			}
		}
		v.Lane = st.lane
		v.S = 0
		speed, ok := e.injectionPlan(v)
		if !ok {
			continue // unsafe entry this tick: hold, retry next tick
		}
		v.V = math.Min(speed, v.v0eff(st.lane)) // the plan is F-free; apply the driver's own factor
		v.Cooldown = e.Params.SpawnCooldown
		v.laneEntryTick = e.Tick // ADR-0036 dwell clock starts at injection, not at pend creation
		e.register(v)
		e.Stats.Spawned++

		// Schedule this origin's next spawn; the interval jitter is drawn
		// from the NEXT vehicle's own stream.
		nxt := e.newVehicle()
		u := nxt.rng.Float64()
		interval := (1 / (st.rate * e.Params.Dt)) * (1 + e.Params.SpawnJitter*(2*u-1))
		ticks := uint64(interval + 0.5)
		if ticks < 1 {
			ticks = 1
		}
		st.pend = nxt
		st.tick += ticks
	}
}
