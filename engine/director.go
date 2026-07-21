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
// expiring: 600 ticks = 60 sim seconds at the validated dt=0.1. A verb
// this stale is dropped rather than injected — the runtime director
// re-samples demand continuously, so stale arrivals are superseded demand,
// not a backlog to flush (contrast the build-time Spawner, whose schedule
// is the spec and never expires).
const DirectorSpawnHoldTicks = 600

// SpawnDirective is the kernel form of a director spawn verb: where and
// what to inject, and not before which tick. RequestID is the director-
// assigned idempotency key (dedup lives in the contract layer, which
// records only first-seen verbs; the kernel carries it for the CRC and
// keyframe so pending state round-trips bit-exactly).
type SpawnDirective struct {
	RequestID    string // director-assigned idempotency key
	Origin       string // origin lane ID (must be a network origin)
	TypeName     string // scenario vehicle-type name
	EarliestTick uint64 // not-before tick (sim ticks, ADR-0005)
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
// origin, or an unknown vehicle type is rejected with a reason; the run is
// unaffected. Like EnqueueIntent, it must be called only from the goroutine
// that owns the engine, between Steps.
func (e *Engine) EnqueueSpawn(d SpawnDirective) error {
	lane := e.Net.OriginByID(d.Origin)
	if lane == nil {
		if e.Net.LaneByID(d.Origin) == nil {
			return fmt.Errorf("unknown origin lane %q", d.Origin)
		}
		return fmt.Errorf("lane %q is not a spawn origin", d.Origin)
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
	e.dirNew = append(e.dirNew, TickedSpawn{
		TypeIdx: ti, LaneIdx: lane.Index,
		SpawnDirective: SpawnDirective{
			RequestID:    d.RequestID,
			Origin:       d.Origin,
			TypeName:     d.TypeName,
			EarliestTick: d.EarliestTick,
		},
	})
	return nil
}

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
			continue // expired: stale demand dropped (deterministic)
		}
		lane := e.Net.Lanes[d.LaneIdx]
		// Density cap — exactly the Spawner's rule.
		if target := e.scen.DensityTargetPerKm; target > 0 &&
			float64(len(e.order))/e.Net.TotalLaneKm() >= target {
			kept = append(kept, d)
			continue
		}
		var first *Vehicle
		if len(lane.vehs) > 0 {
			first = lane.vehs[0]
		}
		if first != nil {
			// Origin clearance rule (identical to the Spawner's).
			if first.S-first.Type.Length < 8+0.8*first.V {
				kept = append(kept, d)
				continue
			}
		}
		e.injectDirective(&d, lane, first)
	}
	e.dirQueue = kept
}

// injectDirective performs the actual injection: a fresh per-vehicle keyed
// stream via e.newVehicle(), the desired-speed factor from that stream, and
// the Spawner's entry-speed rule (near the leader's speed so the arrival
// never forces an emergency). Type comes from the directive — the director
// already sampled the type mix, so no draw is consumed here beyond F.
func (e *Engine) injectDirective(d *TickedSpawn, lane *Lane, first *Vehicle) {
	v := e.newVehicle()
	v.Type, v.TypeIdx = e.scen.Types[d.TypeIdx], d.TypeIdx
	v.F = 1 + e.Params.SpeedFactorSigma*v.rng.Norm()
	if v.F < 0.8 {
		v.F = 0.8
	} else if v.F > 1.3 {
		v.F = 1.3
	}
	v.Lane = lane
	v.S = 0
	v0 := v.v0eff(lane)
	if first != nil {
		// Enter near the leader's speed so the arrival never forces an
		// emergency (identical to the Spawner's rule).
		v.V = math.Min(v0, math.Max(8, first.V+2))
	} else {
		v.V = v0
	}
	v.Cooldown = e.Params.SpawnCooldown
	e.register(v)
	e.Stats.Spawned++
}
