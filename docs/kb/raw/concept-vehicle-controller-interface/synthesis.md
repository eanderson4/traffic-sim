# Synthesis: Vehicle & Controller Interface

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds a future ADR on the engine↔controller contract (next available number).
> This synthesis recommends; the ADR decides.

## Summary

The research question: what should the message contract between the engine and
its heterogeneous controllers (AI policy, scripted scenario, human with arcade
controls) carry — observations in, intents out — under ADR-0005's async,
tick-boundary-applied, never-blocking time model?

The surveyed systems (SUMO/TraCI, CARLA+TM, MATSim, SMARTS, highway-env, Flow,
OpenSCENARIO, game-networking practice) converge on a consistent picture. Intent
vocabularies form a ladder from raw actuation to declarative constraints, and
one engine can host several rungs if the rung is *declared at attach* (SMARTS'
~10 action spaces). The durable core is four orthogonal axes — longitudinal,
lateral (lane), routing/junction choice, signals. Observation design ranges from
TraCI's everything-by-RPC (a measured 11× slowdown) to declared per-agent
windows (SMARTS, highway-env). The sharpest lessons are failures: blocking
barriers (TraCI, CARLA sync), sticky last-control on disconnect (CARLA),
no-ownership multi-client collisions (CARLA walkers), client-discretionary
safety bitmasks (SUMO speedMode), and silent protocol renumbering (TraCI 0x41 →
0xb1). ADR-0005's async intent model already dodges the first failure class;
the contract must be designed to dodge the rest.

## Source Files

- [Mechanics: intent ladders, observation models, handoff, clamping, versioning](./implementation.md)
- [Prior art survey](./competitors.md)
- [Standards, named patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions

### 1. One intent message, four orthogonal axes; acceleration as the longitudinal primitive
**Choice:** A single `Intent` message type per vehicle per tick carrying:
longitudinal intent (target acceleration in m/s² as the canonical field, with an
optional speed-setpoint variant), lateral intent (lane-change request: left /
right / none, plus explicit turn choice at junctions), routing intent
(next-link/destination choice), and signal state. All fields optional; absent =
no change. AI (IDM+MOBIL), scripted, and human controllers all emit this one
type; the human's keyboard maps to the same axes (highway-env `manual_control`
precedent).
**Why:** IDM-family car-following natively outputs acceleration
([[domain-traffic-flow-models]]), and Flow demonstrated an entire RL research
program on accel-only controllers. Five meta-actions sufficed for both RL and
keyboard play in highway-env; junctions add the routing axis MATSim isolates as
`chooseNextLinkId()`. Raw throttle/brake (CARLA `VehicleControl`) forces
per-vehicle powertrain knowledge onto the controller and has no lane-level
meaning; speed-setpoints (TraCI `setSpeed`) need an engine-side servo whose
tuning then becomes part of the contract — keep it as a convenience variant,
not the primitive.
**Trade-off:** Controllers wanting trajectory tracking (SMARTS MPC/Trajectory
rungs) must compile trajectories to per-tick intents client-side. Fine: that is
what their PD/MPC trackers already do.
**Field context:** [implementation §1](./implementation.md) (the ladder),
[competitors: highway-env, Flow](./competitors.md).

### 2. Observations: declared area-of-interest window; own vehicle exact, neighbors capped
**Choice:** At attach, the controller declares its observation window (radius,
max neighbor count, features). Per tick the engine publishes: own-vehicle state
exact (position, speed, lane, route, signals) + neighbor list within the window
(id, class, relative position/speed, signals), sorted and capped, with a
presence/count convention. Neighbor noise/fidelity degradation is a
scenario/vehicle-sensor property (CARLA seeded-noise precedent) — reserve the
field, don't ship noisy models in v1. No per-object query RPC on the hot path.
**Why:** TraCI's everything-by-RPC is the measured performance trap (25k
vehicles/s polling, 90 s vs 8 s); SMARTS (`NeighborhoodVehicles(radius=50)`) and
highway-env (fixed V×F array, ego row 0, presence flag) prove capped windows
support real policies. Ego-relative sorted lists make the controller's job and
the determinism story simpler.
**Trade-off:** The window shape becomes contract surface; window delivery
bandwidth interacts with NATS subject design and snapshot rate — sized jointly
with [[arch-state-authority]] and [[arch-nats-backbone]], not here.
**Field context:** [implementation §3](./implementation.md).

### 3. Attach handshake: version + controller type + dialect/cadence + claims; exclusive ownership
**Choice:** Controllers attach with a hello declaring contract version,
controller type (ai / scripted / human), intent dialect options and decision
cadence (in ticks), and requested vehicle claims. The engine grants claims
exclusively — one vehicle has exactly one controller at a time; a controller may
hold many vehicles (TM precedent) though humans normally hold one. Ownership
transfers are engine-arbitrated events (bubble precedent), never client
assumptions.
**Why:** SMARTS proves declaration-at-attach supports heterogeneous I/O without
widening the wire format; OSI/SRMD proves capability advertisement is standard
practice. CARLA's walkers ("each client is only aware of the ones it is in
charge of" → collisions) and TraCI's order-only multi-client mode are the
documented cost of no ownership.
**Trade-off:** Claim arbitration adds engine bookkeeping (claim table in world
state, logged for replay). Small compared to conflict resolution without it.
**Field context:** [implementation §6–§7](./implementation.md),
[standards: capability advertisement, bubbles](./standards-and-patterns.md).

### 4. Disconnect = modeled fallback, never sticky control
**Choice:** Heartbeat expectation (intent or keepalive every k ticks). On
timeout, ownership reverts and the vehicle falls back to the engine's default
controller (IDM+MOBIL) — or to a minimum-risk brake-to-stop where scenario
config says so (ToC/MRM precedent). The fallback event is logged in the intent
stream so replay reproduces it.
**Why:** CARLA's two documented failure modes are sticky-last-control (vehicles
holding final `VehicleControl`, issue #7626) and no-fallback (TM shutdown leaves
vehicles "immobile on the map"). SUMO's `setSpeed(-1)` revert, Flow's
`None`-means-SUMO-drives, and the ToC Device's MRM all show revert-to-model is
the mature default.
**Trade-off:** A momentarily disconnected human's car driving on IDM may surprise
other humans; MRM braking is the alternative default. Make it scenario-config.
**Field context:** [implementation §6](./implementation.md),
[standards: ToC state machine](./standards-and-patterns.md).

### 5. Engine clamps all intents; no state-assertion verbs on the controller channel
**Choice:** Every intent passes an engine-side guard chain: clamp accel to the
claimed vehicle's capability envelope (accel/decel/maxSpeed from its vType-like
bundle — Flow's `feasible_accel`), enforce collision-avoidance and right-of-way
(Flow `instantaneous`/`safe_velocity`; SUMO's default-on checks), ignore
unavailable lane requests (highway-env's unavailable→IDLE). Teleports and other
physics-bypassing verbs (`moveToXY`, `set_location`) do not exist on this
channel; they belong to the scenario director channel
([[concept-scenario-format]]). Safety overrides are never a client flag — only
the scenario/authority may relax checks.
**Why:** The clamp is simultaneously physics honesty and the multiplayer
anti-cheat boundary (Gambetta: intents, not state assertions). SUMO's
client-discretionary speedMode bitmask (with published "run a red light"
recipes) is the wrong default for networked humans; CARLA's RSS restrictor and
Flow's failsafe list show guard chains work in production.
**Trade-off:** Controllers can't be told "you are unsafe, here's why" without an
extra feedback field — add an applied-vs-requested echo (or clamp flag) in
observations; cheap and invaluable for debugging.
**Field context:** [implementation §5](./implementation.md),
[standards: failsafe chain, never-trust-client](./standards-and-patterns.md).

### 6. Versioning: negotiated at handshake, add-only evolution, stable semantics
**Choice:** Contract version is a first-class field of the hello; the engine
accepts or rejects. Field semantics never change in place — new verbs/fields are
additive; deprecations span a full release cycle. Version everything the
contract depends on (schema, vehicle capability bundle, scenario) in the
run/replay metadata.
**Why:** TraCI silently renumbered variable IDs (0x41 → 0xb1 broke clients
unnoticed) and broke the whole protocol at 1.0.0; CARLA's egg-pinning and
version-mismatch warnings are a chronic support burden; SMARTS versions agents
in the registry name itself. Deterministic replay (ADR-0005) makes contract
version part of the replay key anyway.
**Trade-off:** Add-only growth means carrying deprecated fields — the standard
protobuf price, worth paying.
**Field context:** [implementation §8](./implementation.md),
[standards: anti-patterns](./standards-and-patterns.md).

### 7. Decision cadence is per-controller, declared in ticks
**Choice:** Controllers declare their decision cadence as a multiple of the
world tick (e.g. human 5 ticks ≈ 500 ms reaction, RL 1–2 ticks, scripted 1
tick). Between decisions the last intent persists *by design* (documented
semantics), which also defines what a late intent does. ADR-0005's 1-tick
minimum latency is the floor, not the only value.
**Why:** SUMO per-vehicle `setActionStepLength`, highway-env `policy_frequency`,
and CARLA per-sensor `sensor_tick` all decouple decision rate from integration
rate; SUMO added action steps precisely because how often agents decide is a
modeling parameter separate from how often the world integrates
([Basic Definition](https://sumo.dlr.de/docs/Simulation/Basic_Definition.html)).
Humans need a multi-tick reaction budget, not 100 ms.
**Trade-off:** "Last intent persists" must be specified per axis (persistent
speed-setpoint vs one-shot lane request) — specify in the ADR, don't improvise.
**Field context:** [implementation §9](./implementation.md).

## Compare/Contrast: Us vs the Field

| Dimension | TraCI | CARLA+TM | MATSim | SMARTS | highway-env/Flow | us (proposed) |
|---|---|---|---|---|---|---|
| Intent rung(s) | speed/accel/lane/route | throttle-brake-steer + TM knobs | engine pulls next link | ~10 declared spaces | meta-actions / accel-only | **4 axes, accel primitive** |
| Observations | RPC + subscriptions (11× pain) | snapshots + noise-modeled sensors | in-process Java | declared sensors/windows | fixed V×F window | **declared AoI window on bus** |
| Coupling | blocking barrier | blocking (sync) / lost cmds (async) | same process | in-process | in-process | **async NATS, tick-boundary apply** |
| Ownership | none (client order) | port/TM registry; walker collisions | 1 agent : 1 vehicle | per-agent + bubbles | per-ego | **exclusive claim, engine-arbitrated** |
| Disconnect | `setSpeed(-1)` revert | sticky/immobile hazards | n/a | zoo policies persist | `None` → SUMO | **fallback AI or MRM, logged** |
| Safety | client bitmask (offable) | RSS restrictor (optional) | engine-internal | engine-internal | failsafe chain | **always-on engine clamp chain** |
| Versioning | byte IDs, broken 1.0.0 | egg/hash warnings | none (in-tree churn) | versioned zoo locators | gym env versions | **handshake-negotiated, add-only** |

## The Genuine Gap (again)

A controller contract designed *as a versioned, streaming, multiplayer wire
protocol* is essentially undocumented. TraCI is an RPC library that accreted
over 15 years; CARLA's API is a client library around per-call RPC; SMARTS'
interface machinery is in-process Python; MATSim's is Java interfaces. Nobody
publishes: the handshake grammar, ownership/claim semantics, intent schema with
evolution rules, AoI-window subscription format, and disconnect-fallback
contract for a sim where humans and AIs share vehicles over a message bus. The
NATS-backed version of this (tick-stamped intents, JetStream intent log,
KV-held claim table) has no written prior art at all — same gap [[arch-time-model]]
found for the loop itself. This project gets to write it.

## Open Questions

- AoI window sizing (radius, count, feature set) vs NATS payload and tick budget
  → benchmark with [[arch-nats-backbone]]; interacts with snapshot rate from
  [[arch-state-authority]].
- ~~Model introspection in engine observations~~ **RESOLVED 2026-07-17
  review:** no introspection field in the engine observation contract (the
  policy no longer lives in-engine after the external-default-driver decision).
  Instead the default driver exposes an external **introspection interface over
  NATS** (request/reply, off the hot path): any client asks "given this vehicle
  state, what would you do?" — a pure function of state + policy, queryable
  from anywhere; serves debug tooling, RL harnesses, and viz. Optionally log
  the side channel on the record plane later if replay-side debugging needs it.
- ~~Persistent-vs-one-shot semantics per intent axis (see decision 7)~~
  **RESOLVED 2026-07-17 review** — per-axis table for the contract ADR:
  accel = one-shot per tick (refreshed every tick); speed setpoint = persistent
  (cruise semantics); lane change = one-shot, expires if infeasible; turn at
  junction = one-shot, held until consumed; routing = persistent; signals =
  persistent state. Transport-level hold-last (1–2 ticks on message loss, per
  [[arch-state-authority]]) is orthogonal to these semantics.
- **Default controller = external process over NATS (2026-07-17 review, supersedes
  the in-process sketch above):** the default driver (IDM + MOBIL + router,
  `destination` parameter) runs as a normal controller process on the NATS
  contract — dogfoods the contract end-to-end and ships as the reference
  controller implementation. Jobs: ambient-traffic generator, handoff source
  when a human claims a vehicle, orphan re-claimer (engine releases claims on
  disconnect, publishes unclaimed-vehicle events; the default driver re-claims
  per capacity policy). Seeded from the scenario seed like everything else;
  replay never runs it (recorded intents come from the JetStream log).
- **No in-engine driving fallback at all (2026-07-17 review, supersedes the MRM
  sketch above):** every vehicle is always driven via the contract. Orphans from
  a dead controller are bridged by transport hold-last (already specified) and
  re-claimed by the default driver within a few ticks via unclaimed-vehicle
  events. If the **default driver itself** dies, it is treated as critical
  infrastructure: engine gates the tick loop on its health (grace of a few
  ticks, covered by hold-last) and **pauses the run** — supervisor restarts the
  driver, sim resumes. Pause/resume events are recorded on the JetStream record
  plane; tick determinism is unaffected (pause is dead wall-clock time between
  ticks, invisible to sim state). Edge: mass-orphan events beyond the default
  driver's absorb capacity are re-claimed gradually (hold-last bridges; log the
  event). The default driver deploys supervised (restart policy, liveness over
  NATS) alongside the engine.
- **Default-driver fleet for failover (2026-07-17 review):** run N replicas;
  partitioning is emergent via exclusive claims (each replica claims from the
  unclaimed pool up to a cap — no leader election, no assigned shards). One
  replica's death = a routine mass-orphan event absorbed by peers running with
  headroom (size for one full peer loss). The pause trigger generalizes to
  "available claim capacity < demand for T ticks." Constraint this imposes:
  policy RNG must be seeded **per vehicle** (keyed off vehicle ID, per the
  ADR-0005 stream discipline), never per process — then which replica drives a
  vehicle is behaviorally invisible and live-run determinism survives
  failover. NATS itself is the remaining SPOF → supervised as critical too.
- ~~How scripted scenarios drive vehicles~~ **RESOLVED 2026-07-17 review:**
  both. Scripted vehicles are ordinary controllers emitting ordinary 4-axis
  intents (uniformity, dogfooding). Scenario effects — spawn/despawn, teleport,
  timed/conditional triggers (the OpenSCENARIO verbs) — go to a **director**
  client holding elevated grants in the attach handshake; engine-arbitrated,
  recorded on the record plane for replay. Topology: engine + privileged role
  clients (director; the elevated-grants pattern extends to other privileged
  roles later) + ordinary controllers (drivers). The "traffic-god" player is a
  human director. → [[concept-scenario-format]].
- ~~Turn-signal semantics~~ **RESOLVED 2026-07-17 review:** informational in v1
  (turn signals appear in other controllers' observations; not binding at
  junctions). Binding semantics can be revisited once
  [[arch-road-graph-model]]'s junction representation lands.
- ~~Human multi-vehicle claims~~ **RESOLVED 2026-07-17 review:** allowed — one
  controller may claim many vehicles (the default-driver fleet relies on this
  already; the "traffic-god" player is the chaos-demo case).

## Connections to Other Topics

- **Decided by:** [[arch-time-model]] (ADR-0005) — async intents, tick-stamped,
  batch-applied, 1-tick latency as reaction time; this topic fills in what those
  intents *are*.
- **Constrains:** [[arch-nats-backbone]] (subjects: hello/claims, intents,
  observations; JetStream intent log carries the fallback events too; version
  fields everywhere), [[arch-state-authority]] (AoI windows derived from the
  single-writer world state; observation snapshot derivation),
  [[concept-scenario-format]] (scripted controllers, director channel,
  OpenSCENARIO verb mapping, scenario-config fallback policy).
- **Depends on:** [[domain-traffic-flow-models]] (IDM+MOBIL as the built-in
  fallback controller and reference AI dialect), [[arch-road-graph-model]]
  (junction turn-choice and lane-target representation).
- **Relates to:** [[domain-signal-control]] (signal states inside the AoI
  observation; controllers must see the same lights humans do),
  [[domain-simulator-landscape]] (positions this contract against the field),
  [[domain-congestion-metrics]] (applied-vs-requested echoes double as
  controller-quality telemetry).
