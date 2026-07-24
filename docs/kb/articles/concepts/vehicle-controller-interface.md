# Vehicle & Controller Interface

> The engine↔controller contract: one 4-axis intent message, declared observation windows, exclusive per-vehicle claims, always-on engine clamping, and an external default-driver fleet — zero driving logic in the engine.

## Overview

The controller interface is *the* central interface of traffic-sim ([VISION.md](../../../VISION.md)): AI policies, scripted scenario actors, and humans with arcade controls all drive vehicles through one message contract over NATS. [ADR-0005](../../decisions/ADR-0005-time-model.md) fixed the temporal shape — asynchronous, tick-stamped intents buffered and batch-applied at 100 ms tick boundaries, never blocking the tick. This topic fills in what those intents *are*: the vocabulary in, the observations out, and the lifecycle (attach, claim, hand off, disconnect) around them.

The research surveyed every system with a comparable contract — SUMO/TraCI, CARLA + Traffic Manager, MATSim, SMARTS, highway-env, Flow, OpenSCENARIO, plus game-networking practice. They converge on a four-axis intent vocabulary (longitudinal, lateral, routing, signals) and on declared-at-attach observation windows. The sharpest lessons are measured failures: TraCI's blocking RPC barrier costs 11× (90 s vs 8 s on 9,000 vehicles), CARLA wedges vehicles holding their last control on disconnect, and SUMO lets any client disable safety checks via bitmask. The contract was designed to dodge each.

The 2026-07-17 design review hardened the synthesis into [ADR-0008](../../decisions/ADR-0008-controller-contract.md): one 4-axis Intent with a per-axis persistence table, exclusive per-vehicle claims under grants-based roles (driver / default-driver / director / signal), and — superseding the original in-engine fallback sketch — *all* driving, including fallback, happens through the contract via an external default-driver fleet. The engine contains zero driving logic.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| 4-axis Intent message | [ADR-0008](../../decisions/ADR-0008-controller-contract.md); raw implementation.md §1 | One per-vehicle per-tick command: longitudinal / lateral / routing / signal |
| Per-axis persistence table | [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Which axes refresh each tick vs persist until replaced |
| AoI observation window | raw implementation.md §3 | Declared radius/count/features; ego exact + capped sorted neighbor list |
| Attach handshake & claims | raw implementation.md §6–§7; [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Hello with version, type, cadence, claims; exclusive per-vehicle ownership |
| Grants-based roles | [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Capability tiers: driver / default-driver / director / signal |
| Engine clamp chain | raw implementation.md §5; standards-and-patterns.md (failsafe chain) | Always-on guards: capability envelope, collision avoidance, right-of-way |
| Default-driver fleet | raw synthesis.md (2026-07-17 review); [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | External IDM+MOBIL replicas driving unclaimed vehicles; failover backbone |
| Introspection interface | raw synthesis.md (2026-07-17 review) | Default driver's "what would you do?" request/reply, off the hot path |
| Director channel | raw synthesis.md (2026-07-17 review); [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Spawn/despawn, teleport, triggers — elevated-grants scenario verbs |
| Contract versioning | raw implementation.md §8 | Handshake-negotiated, add-only evolution, replay-keyed |

## How It Works

### 1. One intent message, four orthogonal axes — acceleration is the longitudinal primitive

Every surveyed system's control vocabulary collapses to four axes, and the contract adopts exactly one message type covering all of them (ADR-0008):

- **Longitudinal** — target acceleration in m/s² (canonical), with an optional speed-setpoint variant.
- **Lateral** — lane-change request: left / right / none, plus explicit turn choice at junctions.
- **Routing** — next-link / destination choice.
- **Signal** — turn-signal state (informational in v1: visible in other controllers' observations, not binding at junctions; binding semantics deferred until the road graph's junction representation lands).

All fields optional; absent = no change. AI (IDM+MOBIL), scripted, and human controllers emit the same type — a human's keyboard maps onto the same axes (highway-env `manual_control` precedent: human input is a config flag, not a subsystem).

Evidence for accel-as-primitive: IDM-family car-following natively outputs acceleration; Flow ran an entire RL research program on accel-only controllers; highway-env's five meta-actions sufficed for both RL training and keyboard play. Rejected rungs: raw throttle/brake/steer (CARLA `VehicleControl`) forces per-vehicle powertrain knowledge onto controllers and has no lane-level meaning; speed-setpoint-as-primitive (TraCI `setSpeed`) requires an engine-side servo whose tuning becomes contract surface — kept as a convenience variant only. Controllers wanting trajectory tracking (SMARTS MPC/Trajectory rungs) compile trajectories to per-tick intents client-side, which their PD/MPC trackers already do.

### 2. Per-axis persistence is specified, not improvised

Resolved 2026-07-17, ratified in ADR-0008 — "last intent persists" means something different per axis:

- **Acceleration** — one-shot, refreshed every tick.
- **Speed setpoint** — persistent (cruise semantics).
- **Lane change** — one-shot; expires if infeasible.
- **Turn at junction** — one-shot, held until consumed.
- **Routing** — persistent.
- **Signals** — persistent state.

Transport-level hold-last (re-applying the last message for 1–2 ticks on message loss, per the state-authority design) is orthogonal to these semantics.

### 3. Observations: declared area-of-interest window

At attach, a controller declares its observation window (radius, max neighbor count, features). Per tick the engine publishes its own vehicle's state exactly (position, speed, lane, route, signals) plus a neighbor list within the window — id, class, relative position/speed, signals — sorted, capped, with a presence/count convention. No per-object query RPC on the hot path.

Evidence: TraCI's everything-by-RPC is the measured trap — Bologna 9,000-vehicle position polling took 90 s vs 42 s with subscriptions vs 8 s without TraCI (~25k vehicles/s polling, ~50k subscribed). SMARTS (`NeighborhoodVehicles(radius=50)`) and highway-env (fixed V×F array, ego row 0, presence flag) prove capped windows support real policies. Neighbor noise/fidelity degradation is a scenario/vehicle-sensor property (CARLA's seeded-noise precedent) — the field is reserved, noisy sensor models are not v1.

**Introspection is not an observation field.** TraCI exposes model introspection in-engine (`getSpeedWithoutTraCI`); the 2026-07-17 review instead put it on the default driver as an external request/reply interface over NATS, off the hot path — any client asks "given this vehicle state, what would you do?", a pure function of state + policy serving debug tooling, RL harnesses, and viz.

### 4. Attach, claims, and grants-based roles

Controllers attach with a hello declaring contract version, controller type (ai / scripted / human), intent dialect options, decision cadence (in ticks), and requested vehicle claims. The engine grants claims **exclusively** — one vehicle has exactly one controller at a time; a controller may hold many vehicles (CARLA TM precedent; the default-driver fleet relies on this), including humans (the "traffic-god" player of the chaos demo). Ownership transfers are engine-arbitrated events (SMARTS bubble precedent), never client assumptions; the claim table lives in world state (NATS KV) and is logged for replay.

Roles are grants-based (ADR-0008): **driver** (ordinary 4-axis intents), **default-driver** (fleet driving ambient/unclaimed traffic), **director** (elevated: spawn/despawn, teleport, timed/conditional triggers — the OpenSCENARIO verbs, recorded on the record plane), **signal** (junction signal control). Notably, signal control moved from an engine-internal subsystem to an external grants-based client role in the 2026-07-17 review — the engine arbitrates right-of-way physically but holds no signal-timing logic. Scripted scenario vehicles are ordinary driver controllers emitting ordinary intents (uniformity, dogfooding); scenario *effects* go through the director.

### 5. Disconnect and fallback: no driving logic in the engine at all

ADR-0008, superseding the synthesis's original in-engine MRM (minimum-risk maneuver) sketch: **every vehicle is always driven via the contract.**

1. Heartbeat expectation: intent or keepalive every k ticks. On timeout the engine releases the claim and publishes an unclaimed-vehicle event. (Paced runs only — the sweep is skipped at `PaceFloor == 0`; see the ADR-0006 2026-07-24 addendum.)
2. Transport hold-last bridges the gap (1–2 ticks); a default-driver replica re-claims the orphan within a few ticks per its capacity policy.
3. The **default-driver fleet** is N replicas of an external IDM + MOBIL + router process (scenario-seeded, `destination` parameter) — also the ambient-traffic generator, the handoff source when a human claims a vehicle, and the reference controller implementation. Partitioning is emergent via exclusive claims: each replica claims from the unclaimed pool up to a cap; no leader election, no assigned shards. Fleet is sized to absorb one full peer loss with headroom.
4. If available claim capacity stays below demand for T ticks (worst case: the whole fleet dies), the engine gates the tick loop and **pauses the run** — the supervisor restarts the driver and the sim resumes. Pause/resume events are recorded on the JetStream record plane; pause is dead wall-clock time between ticks, invisible to sim state, so tick determinism is unaffected.
5. Replay never runs the default driver — recorded intents come from the JetStream log.

The constraint this imposes: policy RNG is seeded **per vehicle** (keyed off vehicle ID, per ADR-0005's stream discipline and [ADR-0007](../../decisions/ADR-0007-vehicle-model.md)), never per process — then which replica drives a vehicle is behaviorally invisible and live-run determinism survives failover. NATS itself is the remaining SPOF and is supervised as critical infrastructure too.

The anti-requirements come from CARLA's documented failures: sticky last-control (vehicles holding their final `VehicleControl`, issue #7626) and no-fallback (TM shutdown leaves vehicles "immobile on the map").

### 6. The engine clamps everything; no state-assertion verbs exist

Every intent passes an always-on engine-side guard chain (Flow's failsafe list, almost verbatim: `instantaneous` → `safe_velocity` → `feasible_accel` → `obey_speed_limit`):

- Clamp accel to the claimed vehicle's capability envelope (accel/decel/maxSpeed from its vType-like bundle, ADR-0007).
- Enforce collision avoidance and right-of-way.
- Unavailable lane requests degrade to a safe no-op (highway-env's unavailable→IDLE rule), never an error.

Teleports and other physics-bypassing verbs (TraCI `moveToXY`, CARLA `set_location`) do not exist on the controller channel — they are director-channel verbs requiring elevated grants, engine-arbitrated and recorded for replay. Safety overrides are never a client flag; only the scenario/authority may relax checks. The clamp is simultaneously physics honesty and the multiplayer anti-cheat boundary (game-server doctrine: clients send intents, never state assertions). Observations carry an applied-vs-requested echo (clamp flag) — cheap, and invaluable for debugging and controller-quality telemetry.

### 7. Versioning and cadence

- **Versioning** — contract version is a first-class hello field; the engine accepts or rejects. Evolution is add-only; field semantics never change in place; deprecations span a full release cycle. Schema, vehicle capability bundle, and scenario versions all go into run/replay metadata — deterministic replay (ADR-0005) makes contract version part of the replay key anyway. Scars being avoided: TraCI silently renumbered variable IDs (0x41 → 0xb1 broke a client unnoticed) and broke the whole protocol at 1.0.0; CARLA's per-release `.egg` pinning and version-mismatch warnings are a chronic support burden. (2026-07-24 scope note: this rule versions the WIRE — frames, subjects, field meanings — not the world model; an engine behavior fix that changes trajectories without touching the wire is not a contract-version event. The ratified exception shape is the ADR-0006 2026-07-24 addendum: the `route` axis' documented meaning was unchanged; the engine began honoring it.)
- **Decision cadence** — declared at attach as a multiple of the world tick (human ≈ 5 ticks ≈ 500 ms reaction, RL 1–2 ticks, scripted 1 tick). ADR-0005's 1-tick minimum latency (100 ms at 10 Hz) is the floor, not the only value; SUMO's per-vehicle `setActionStepLength`, highway-env's `policy_frequency`, and CARLA's per-sensor `sensor_tick` all decouple decision rate from integration rate. The engine never changes its tick for a client.

### 8. Transport mapping

Per [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) and the [ADR-0002](../../decisions/ADR-0002-nats-backbone.md) clarification (2026-07-17: small messages only on the hot path, no in-process controller fast path): hellos/claims and intents are core-NATS subjects under the `{ns}.{run}.{plane}.>` taxonomy; the JetStream intent log on the record plane carries fallback and pause/resume events too; the claim table sits in KV. Even the default driver — the most latency-sensitive controller — rides the bus.

## Gotchas

- **Blocking barrier per step**: TraCI halts the sim until every client calls `simulationStep` — measured 11× slowdown (90 s vs 8 s on 9,000 vehicles); CARLA sync mode deadlocks on misconfigured multi-client ticks. ADR-0005's async, tick-boundary-applied model exists precisely to dodge this; never add a "wait for controllers" mode.
- **Sticky last-control on disconnect**: CARLA vehicles keep their final `VehicleControl` and can become unresponsive to further control (issue #7626); TM shutdown leaves vehicles "immobile on the map". Liveness (heartbeat) plus a defined re-claim path is a contract requirement, not an implementation nicety.
- **Shared mutable ownership**: CARLA walkers collide across clients because "each client is only aware of the ones it is in charge of"; TraCI has no ownership, only client ordering. Exclusive per-vehicle claims are the fix.
- **Safety as a client-discretionary bitmask**: SUMO `speedMode` (default 31 = all checks on) and `laneChangeMode` (default 1621) let any client disable right-of-way and red-light braking — the docs publish "run a red light" recipes. Fine for single-user research, wrong for networked humans: overrides are engine/scenario grants only.
- **State-assertion verbs on the controller channel**: TraCI `moveTo`/`moveToXY` — "No collision checks are done" — and CARLA `set_location` are speed-hack vectors in game-server terms. Teleport belongs to the director.
- **Silent protocol renumbering**: TraCI moved `VAR_SPEED_WITHOUT_TRACI` 0x41 → 0xb1 between revisions, breaking a client discovered "by pure luck"; the whole protocol broke at SUMO 1.0.0. Hence handshake-negotiated versions and add-only evolution.
- **Retroactive application semantics**: TraCI had to document its per-step command ordering after the fact because users couldn't predict when commands took effect. The per-axis persistence table (§2) is specified up front for the same reason.
- **Everything-mutable API sprawl**: TraCI's change-state table mutates color, dimensions, emission class, boarding duration at runtime — contracts accrete without a taxonomy. Keep the controller surface to the four axes plus lifecycle.

## Open Questions

- **AoI window sizing** (radius, neighbor count, feature set) vs NATS payload size and tick budget — benchmark at engine bring-up with the NATS backbone topic; interacts with the observation snapshot rate from state authority. Window shape is contract surface, so size it before freezing the schema.
- **Logging the introspection side channel** on the record plane — deferred; add only if replay-side debugging needs it.
- **Binding turn-signal semantics** at junctions — v1 treats signals as informational; revisit once the road graph's junction representation lands.

## Related

- [Time Model](../architecture/time-model.md) — ADR-0005's async, tick-boundary-applied intents are the frame this contract fills in; the intent log is the replay key.
- [NATS Backbone](../architecture/nats-backbone.md) — carries hellos/claims, intents, and observations; the 2026-07-17 clarification put even the default driver on the bus.
- [State Authority](../architecture/state-authority.md) — AoI windows derive from the single-writer world state; transport hold-last bridges controller gaps.
- [Traffic Flow Models (Microscopic)](../business-domains/traffic-flow-models.md) — IDM+MOBIL is the default-driver policy and the reference AI dialect; its native accel output motivates the intent primitive.
- [Scenario Format](../concepts/scenario-format.md) — the director channel, elevated grants, and the OpenSCENARIO verb mapping live there.
- [Signal Control](../business-domains/signal-control.md) — signal states ride inside AoI observations; signal timing is an external grants-based client role, not engine logic.

---
*Raw research: [raw/concept-vehicle-controller-interface](../../raw/concept-vehicle-controller-interface/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
