package engine

import (
	"fmt"
	"math"
)

// director.go — runtime director spawn directives (ADR-0008 §5 director
// role; scenario-format §3: a runtime demand director issues spawn verbs,
// recorded on the record plane so replay never re-runs the sampler). The
// kernel side is a deterministic injection queue that reuses the Spawner's
// mechanics exactly: the same origin-clearance rule (8+0.8·v buffer), the
// same density cap, per-vehicle keyed streams via e.newVehicle(), the
// desired-speed factor drawn from the incoming vehicle's own stream, and
// the same entry-speed rule. Only the schedule source differs: directives
// arrive over the wire (the Spawner's schedule is part of the spec).
//
// Blocked-origin policy: HOLD-AND-RETRY, bounded and deterministic — the
// same "unmet demand carries over" semantics as the Spawner's blocked
// entries. A directive whose earliest tick has passed retries every tick
// until the origin clears, up to DirectorSpawnHoldTicks past its earliest
// tick; then it expires (dropped, no injection). Expiry is a pure function
// of tick + queue state, so replay re-derives it without a record.
//
// Tick-order point: directives are applied at the tick boundary in phase 1
// (events), immediately AFTER the deterministic spawner and BEFORE buffered
// intents — a fixed, documented point (engine.go Step). A directive
// enqueued between ticks T and T+1 is stamped with tick T+1 (its
// applied_tick, the same convention as intents) and first eligible for
// injection at that tick.

// DirectorSpawnHoldTicks bounds how long a queued directive whose earliest
// tick has passed waits for origin clearance or density headroom before
// expiring: 600 ticks = 60 sim seconds at the validated dt=0.1.
//
// This once claimed a stale verb was safe to drop because "the director
// re-samples demand continuously, so stale arrivals are superseded demand,
// not a backlog to flush". That is not what the director does: flowSampler
// walks a FIXED arrival schedule (demand/director.go), so a dropped verb is
// a vehicle the scenario asked for and never got — lost demand, not
// superseded demand. Believing otherwise is what let chi-loop-urban lose
// 15,052 of 17,998 requested vehicles with every metric reading clean.
//
// Expiry is still the right policy for an origin that stays blocked — the
// alternative is an unbounded queue that injects a rush-hour's worth of
// backlog the instant a jam clears. But the loss is now COUNTED
// (Engine.DirExpired and friends) rather than silent, and the counters
// separate a blocked origin from a verb that arrived too late to try.
const DirectorSpawnHoldTicks = 600

// MaxRequestIDBytes bounds the director-assigned verb idempotency key, in
// BYTES: the TSKF keyframe codec length-prefixes it with a u16 (the
// director spawn queue since v3, the signal override table since v7), so
// an over-long id would marshal a truncated length next to the full
// string and make the keyframe unreadable. Enforced here, at the kernel
// enqueue — EVERY caller is guarded, not just the NATS contract layer
// (which keeps its own wire-facing check against this same constant).
const MaxRequestIDBytes = 65535

// SpawnDirective is the kernel form of a director spawn verb: where and
// what to inject, and not before which tick. RequestID is the director-
// assigned idempotency key (dedup lives in the contract layer, which
// records only first-seen verbs; the kernel carries it for the CRC and
// keyframe so pending state round-trips bit-exactly).
type SpawnDirective struct {
	RequestID    string // director-assigned idempotency key
	Origin       string // origin lane ID (a network origin, unless OffsetM > 0)
	TypeName     string // scenario vehicle-type name
	EarliestTick uint64 // not-before tick (sim ticks, ADR-0005)
	// Destination is an optional route destination lane id (ADR-0021),
	// applied as the vehicle's Route axis at injection. The kernel follows
	// it (routing.go) and ENDS the trip there (boundaries()). Empty leaves
	// the vehicle unrouted, exactly as before — controllers may still set
	// the axis later over the intent plane.
	Destination string
	// OffsetM is the injection position along the origin lane, in meters
	// from its start. Zero is the portal semantics (inject at the lane
	// start) and is the only value a network ORIGIN lane accepts. A
	// positive offset is the INTERIOR-origin opt-in (ADR-0021: a garage or
	// driveway mid-block): it admits any lane, and the offset being
	// explicit is what keeps a mistyped portal id from silently becoming a
	// mid-network injection. Interior entries are clearance-checked behind
	// as well as ahead.
	OffsetM float64
}

// TickedSpawn is an accepted directive with its resolution and applied_tick
// (parallel to TickedIntent for intents). It is the record-plane shape:
// replay re-enqueues the directive at Tick and the deterministic queue
// reproduces the identical injection.
type TickedSpawn struct {
	Tick    uint64 // tick the engine accepted the directive (applied_tick)
	TypeIdx int    // resolved index into the scenario type list
	LaneIdx int    // resolved origin lane index (Network.Lanes)
	SpawnDirective
}

// EnqueueSpawn validates a directive and buffers it for application at the
// next tick boundary. Unknown origin lane, a lane that is not a spawn
// origin, an unknown vehicle type, or a RequestID already live in the queue
// is rejected with a reason; the run is unaffected. RequestID uniqueness
// among live entries is ENGINE-enforced here (the NATS contract layer
// dedups first, so this only ever fires on local-enqueue misuse). Note the
// compatibility edge: the duplicate rejection means pre-change LOCAL-harness
// recordings with simultaneously-live duplicate RequestIDs are retroactively
// unreplayable (wire-produced recordings can't contain them — the contract
// layer's first-seen dedup drops them before the kernel). Empty-string IDs
// remain accepted, but two live ""s now collide and reject as duplicates.
// Like EnqueueIntent, it must be called only from the goroutine that owns
// the engine, between Steps.
func (e *Engine) EnqueueSpawn(d SpawnDirective) error {
	if len(d.RequestID) > MaxRequestIDBytes {
		return fmt.Errorf("request_id too long: %d bytes, want ≤ %d (keyframe codec limit)", len(d.RequestID), MaxRequestIDBytes)
	}
	var lane *Lane
	switch {
	case d.OffsetM < 0 || math.IsNaN(d.OffsetM):
		return fmt.Errorf("bad injection offset %v (must be ≥ 0)", d.OffsetM)
	case d.OffsetM > 0:
		// Interior origin (ADR-0021): any lane, but the offset must leave
		// the vehicle wholly on it — an injection point past the lane end
		// would cross out on its first boundaries() pass, and one at the
		// very end is the junction mouth the offset exists to avoid.
		lane = e.Net.LaneByID(d.Origin)
		if lane == nil {
			return fmt.Errorf("unknown origin lane %q", d.Origin)
		}
		if d.OffsetM >= lane.Length {
			return fmt.Errorf("injection offset %.2f m is past the end of lane %q (%.2f m)", d.OffsetM, d.Origin, lane.Length)
		}
	default:
		lane = e.Net.OriginByID(d.Origin)
		if lane == nil {
			if e.Net.LaneByID(d.Origin) == nil {
				return fmt.Errorf("unknown origin lane %q", d.Origin)
			}
			return fmt.Errorf("lane %q is not a spawn origin (interior injection needs an explicit offset_m)", d.Origin)
		}
	}
	if d.Destination != "" {
		dest := e.Net.LaneByID(d.Destination)
		if dest == nil {
			return fmt.Errorf("unknown destination lane %q", d.Destination)
		}
		// Arrival is detected in boundaries() by S > Lane.Length, but an
		// EndWall lane brakes its traffic to a stop BEFORE that line, so a
		// vehicle routed to one parks at the wall and never arrives — it
		// stays in the world forever, inflating every occupancy metric.
		// Reject it loudly rather than leak vehicles. No city import can hit
		// this (netimport marks dead ends as exits, and chi-loop-urban has
		// zero EndWall lanes); the synthetic fixtures are what have them.
		if dest.EndWall {
			return fmt.Errorf("destination lane %q ends in a wall: a vehicle routed there stops short of the lane end and can never arrive", d.Destination)
		}
	}
	ti := -1
	for i, t := range e.scen.Types {
		if t.Name == d.TypeName {
			ti = i
			break
		}
	}
	if ti < 0 {
		return fmt.Errorf("unknown vehicle type %q", d.TypeName)
	}
	for _, q := range e.dirQueue {
		if q.RequestID == d.RequestID {
			return fmt.Errorf("duplicate spawn request id %q (already in the injection queue)", d.RequestID)
		}
	}
	for _, q := range e.dirNew {
		if q.RequestID == d.RequestID {
			return fmt.Errorf("duplicate spawn request id %q (already buffered for this boundary)", d.RequestID)
		}
	}
	e.dirNew = append(e.dirNew, TickedSpawn{
		TypeIdx: ti, LaneIdx: lane.Index,
		SpawnDirective: SpawnDirective{
			RequestID:    d.RequestID,
			Origin:       d.Origin,
			TypeName:     d.TypeName,
			EarliestTick: d.EarliestTick,
			Destination:  d.Destination,
			OffsetM:      d.OffsetM,
		},
	})
	return nil
}

// InteriorInjection records one ADR-0021 mid-lane injection performed
// during the last Step. The metric kernel needs it for two things a
// first observation cannot otherwise distinguish: the vehicle did NOT
// cross a boundary to reach a non-origin lane (so it must not be counted
// as a dropped crossing), and its first tick of travel is v.S − S, not the
// whole lane prefix v.S. Derived per-tick state — never serialized, not in
// the CRC, reused by the next Step exactly like AppliedSpawns.
type InteriorInjection struct {
	ID uint64
	S  float64 // injection position along the lane (meters)
}

// InteriorInjections returns the mid-lane injections performed during the
// last Step. Portal injections (S = 0) are never listed — the first
// observation of one is unambiguous already.
func (e *Engine) InteriorInjections() []InteriorInjection { return e.interiorInj }

// AppliedSpawns returns the directives accepted into the injection queue
// during the last Step (stamped with their applied_tick) — the record plane
// logs exactly these. The slice is reused by the next Step.
func (e *Engine) AppliedSpawns() []TickedSpawn { return e.appliedSpawns }

// PendingSpawns reports the injection-queue depth (accepted, not yet
// injected or expired). Test/observability support.
func (e *Engine) PendingSpawns() int { return len(e.dirQueue) }

// stepDirectorSpawns runs the director injection queue at the events phase
// of Step, after the deterministic spawner. Newly buffered directives are
// stamped with the current tick and join the FIFO queue; then every
// eligible directive is attempted in queue order (one blocked origin never
// holds back another directive — per-directive independence, mirroring the
// Spawner's per-origin states).
func (e *Engine) stepDirectorSpawns() {
	e.appliedSpawns = e.appliedSpawns[:0]
	e.interiorInj = e.interiorInj[:0]
	if len(e.dirNew) > 0 {
		for _, d := range e.dirNew {
			d.Tick = e.Tick
			e.dirQueue = append(e.dirQueue, d)
			e.appliedSpawns = append(e.appliedSpawns, d)
			e.SpawnLog = append(e.SpawnLog, d)
		}
		e.dirNew = e.dirNew[:0]
	}
	if len(e.dirQueue) == 0 {
		return
	}
	kept := e.dirQueue[:0]
	for _, d := range e.dirQueue {
		if d.EarliestTick > e.Tick {
			kept = append(kept, d)
			continue
		}
		if e.Tick > d.EarliestTick+DirectorSpawnHoldTicks {
			e.DirExpired++
			if e.DirExpiredByLane == nil {
				e.DirExpiredByLane = map[string]int{}
			}
			e.DirExpiredByLane[d.Origin]++
			if e.DirFirstExpire == 0 {
				e.DirFirstExpire = e.Tick
			}
			// Dead on arrival: the directive was ALREADY past its hold window
			// when it entered the queue, so it never got a single injection
			// attempt. That separates "the origin was blocked for 60 s" from
			// "the verb showed up too late to matter" — different bugs, and
			// the second one is invisible in every other counter.
			if d.Tick > d.EarliestTick+DirectorSpawnHoldTicks {
				e.DirDeadOnArrival++
			}
			continue // expired: stale demand dropped (deterministic)
		}
		lane := e.Net.Lanes[d.LaneIdx]
		// Density cap — exactly the Spawner's rule.
		if target := e.scen.DensityTargetPerKm; target > 0 &&
			float64(len(e.order))/e.Net.TotalLaneKm() >= target {
			kept = append(kept, d)
			continue
		}
		// Injection safety — exactly the Spawner's rule (shared helper).
		// The probe is a lightweight stand-in (not ID-streamed); its ID is
		// the max uint64 so the mutual-yield tie-breaks (foe.ID < v.ID) read
		// as "loses to every real vehicle" — conservative, and deterministic.
		// Route must be on the probe: gateTarget walks successors via
		// pickSuccessor, which follows the ROUTE where there is one, so a
		// route-less probe checks the signal on the default branch while the
		// vehicle injectDirective creates takes the routed one. On a city
		// network those are different internal lanes — the probe would clear
		// an injection against the wrong light.
		probe := &Vehicle{ID: ^uint64(0), Lane: lane, S: d.OffsetM, Type: e.scen.Types[d.TypeIdx], Route: d.Destination}
		speed, ok := e.injectionPlan(probe)
		if !ok {
			kept = append(kept, d)
			continue
		}
		e.injectDirective(&d, lane, speed)
		e.DirInjected++
		e.DirLastInject = e.Tick
	}
	e.dirQueue = kept
}

// injectDirective performs the actual injection at the speed injectionPlan
// computed: a fresh per-vehicle keyed stream via e.newVehicle() and the
// desired-speed factor from that stream. Type comes from the directive —
// the director already sampled the type mix, so no draw is consumed here
// beyond F.
func (e *Engine) injectDirective(d *TickedSpawn, lane *Lane, speed float64) {
	v := e.newVehicle()
	v.Type, v.TypeIdx = e.scen.Types[d.TypeIdx], d.TypeIdx
	v.F = 1 + e.Params.SpeedFactorSigma*v.rng.Norm()
	if v.F < 0.8 {
		v.F = 0.8
	} else if v.F > 1.3 {
		v.F = 1.3
	}
	v.Lane = lane
	v.S = d.OffsetM
	v.Route = d.Destination              // routing axis set at birth (ADR-0021)
	v.V = math.Min(speed, v.v0eff(lane)) // the plan is F-free; apply the driver's own factor
	v.Cooldown = e.Params.SpawnCooldown
	v.laneEntryTick = e.Tick // ADR-0036 dwell clock starts at injection
	e.register(v)
	e.Stats.Spawned++
	if d.OffsetM > 0 {
		e.interiorInj = append(e.interiorInj, InteriorInjection{ID: v.ID, S: d.OffsetM})
	}
}
