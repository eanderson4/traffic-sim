# Standards & Patterns: Vehicle & Controller Interface

> Source: academic research + pattern identification | Researched: 2026-07-16

## Standards & Formalisms

### ASAM OpenSCENARIO — the standardized action vocabulary

The only formal standard that names vehicle-control verbs. Version 1.x (XML)
defines `PrivateAction`s executed per entity: `SpeedAction`,
`LongitudinalDistanceAction`, `LaneChangeAction`, `LaneOffsetAction`,
`SynchronizeAction`, `TeleportAction`, `AssignRouteAction`,
`AcquirePositionAction`, `FollowTrajectoryAction`
([ASAM migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html);
engine support matrices, e.g. [openPASS](https://eclipse.dev/openpass/content/html/user_guide/configs/scenario.html)).
Three structural properties matter for our contract:

- **Dynamics are first-class:** actions carry shape/dimension/duration
  (`dynamicsShape="cubic" dynamicsDimension="rate"`, `duration: 5s`) — an intent
  is (verb, target, dynamics), not just (verb, target)
  ([migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).
- **Controller lifecycle is standardized:** `AssignControllerAction`,
  `ActivateControllerAction`, `OverrideControllerValueAction` — attach/activate/
  override are scenario-level primitives, not engine internals
  ([migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).
- **2.0 pivots from imperative to declarative:** `drive()` with modifiers
  (`speed(50kph)`, `position(behind: ego, 20m)`) specifies constraints the
  simulator resolves; the standard itself calls this "less constrained" than the
  1.x actions — the field is moving from commanding *how* to declaring *what*
  ([migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).

### ASAM OSI `osi3::TrafficCommand` — capability negotiation in the wild

OSI's traffic-command interface pairs actions with per-model metadata declaring
which actions a model supports (`SRMD` files with `ClassificationEntry`
keywords), enabling automated negotiation between scenario engine and agent
models ([edocs.tib.eu paper](https://edocs.tib.eu/files/e01fb24/1884134823.pdf)).
This is capability advertisement as an industry standard practice, not our
invention.

### SUMO vType — de facto capability schema

Length, maxSpeed, accel, decel, minGap, sigma (driver imperfection), tau (desired
headway), speedFactor — the parameter set every SUMO-family tool reuses
([Vehicle Value Retrieval](https://sumo.dlr.de/docs/TraCI/Vehicle_Value_Retrieval.html)).
Flow derives its clamp envelope directly from it
([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).

### Gymnasium Env API — the RL world's contract shape

`observation_space` / `action_space` as declared spaces, `step(action) →
(obs, reward, terminated, truncated, info)`. SMARTS derives per-agent spaces
from the declared `AgentInterface` ([agent docs](https://smarts.readthedocs.io/en/latest/sim/agent.html));
highway-env exposes the same config-driven action/observation types
([actions](https://highway-env.farama.org/actions/index.html),
[observations](https://highway-env.farama.org/observations/index.html)).
Lesson: heterogeneous agents are supported by *declaring* each agent's spaces,
not by widening a common message.

## Named Patterns

### Command pattern (reified intent)

"A command is a *reified method call*" — make the request a first-class object so
it can be queued, logged, timestamped, and routed to any actor; input handlers
and AI both produce commands ([Game Programming Patterns](https://gameprogrammingpatterns.com/command.html)).
For us: the intent message IS a command object — tick-stamped, logged to
JetStream (ADR-0005 replay), routable to any claimed vehicle.

### Authoritative server / never-trust-the-client

Clients send intents, the server computes outcomes, broadcasts state; anti-cheat
follows from never accepting client-computed state
([Gambetta](https://www.gabrielgambetta.com/client-server-game-architecture.html)).
Maps to: engine clamps intents to the vehicle's capability envelope and the
rules of the world (right-of-way, red lights), the clamp being the trust
boundary. SUMO's default-on `speedMode=31`
([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html))
and Flow's failsafe chain
([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py))
are the traffic-sim instances.

### Failsafe chain (ordered guards)

Flow applies an ordered list of guard functions to every requested acceleration
(`instantaneous` → `safe_velocity` → `feasible_accel` → `obey_speed_limit`),
each capable of clipping, each logging when it fires
([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
CARLA's `RssRestrictor` is the same idea as a pluggable filter adapting
`VehicleControl` before application
([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).

### Capability advertisement at attach (handshake)

SMARTS `AgentSpec(interface=AgentInterface(...))` declares sensors + action
space before the agent runs ([agent docs](https://smarts.readthedocs.io/en/latest/sim/agent.html));
CARLA blueprints carry modifiable/immutable attributes
([Python API](https://carla.readthedocs.io/en/latest/python_api/));
OSI/SRMD declares supported actions per model
([edocs.tib.eu](https://edocs.tib.eu/files/e01fb24/1884134823.pdf)).
The contract is negotiated, not assumed.

### Takeover / Minimum-Risk Maneuver state machine

SUMO's ToC Device: take-over request with lead time → if `responseTime >
availableLeadTime`, automated MRM (constant `mrmDecel`) → after transition,
awareness recovers at `recoveryRate`
([ToC Device](https://sumo.dlr.de/docs/ToC_Device.html);
[Frontiers 2025](https://www.frontiersin.org/journals/future-transportation/articles/10.3389/ffutr.2025.1600739/full)).
The generalizable pattern: **a disconnect/handoff is a modeled process, not an
absence of input** — with a safe default behavior (revert-to-AI or brake-to-stop)
as the terminal state.

### Region-triggered control transfer (bubbles)

SMARTS hands vehicles between controllers when they cross a geographic membrane
([paper](https://arxiv.org/pdf/2010.09776.pdf)). Generalizes to: ownership is a
dynamic, engine-arbitrated property with explicit transfer events.

### Pull-based decision interface

MATSim's engine calls `agent.chooseNextLinkId()` at decision points
([doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_driver_agent.html)).
The inverse of pushed intents: decisions are demanded at the moment of need.
Unusable for remote human controllers (blocks on the client — the TraCI trap in
another costume) but clarifies that routing is a per-decision-point query, not a
continuous stream.

### Decoupled cadences (action step / sensor tick / policy frequency)

SUMO per-vehicle `setActionStepLength`
([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)),
CARLA per-sensor `sensor_tick`
([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)),
highway-env `policy_frequency`
([observations docs](https://highway-env.farama.org/observations/index.html)).
Controllers and sensors declare their own rates as multiples of the base step;
the engine never changes its tick for a client.

## Anti-Patterns (documented failures to avoid)

- **Blocking barrier per step.** TraCI: sim halts "until all clients have called
  the 'simulationStep' command"; measured 11× slowdown (90 s vs 8 s on 9,000
  vehicles) ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)). CARLA
  synchronous mode ties the server to client ticks; misconfigured multi-client
  sync deadlocks ("Disable synchronous mode... before the script ends to prevent
  the server blocking, waiting forever for a tick")
  ([Synchrony and time-step](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/),
  [TM docs](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- **State-assertion verbs on the controller channel.** Teleports that skip
  physics and collision checks: TraCI `moveTo`/`moveToXY` ("No collision checks
  are done") ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html));
  CARLA `set_location` ([Python API](https://carla.readthedocs.io/en/latest/python_api/));
  game servers call this a speed-hack vector
  ([Gambetta](https://www.gabrielgambetta.com/client-server-game-architecture.html)).
  Teleport belongs to the scenario director, not controllers.
- **Safety as client-discretionary bitmask.** SUMO `speedMode` lets any client
  disable right-of-way and red-light braking; the docs publish "run a red light"
  recipes ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
  Fine for single-user research, wrong default for multiplayer: overrides must be
  an engine/scenario grant, not a client flag.
- **Sticky last-control on disconnect.** CARLA vehicles keep their final
  `VehicleControl`; TM shutdown leaves vehicles "immobile on the map";
  `set_autopilot(False)` can wedge a vehicle unresponsive to further control
  ([TM docs](https://carla.readthedocs.io/en/latest/adv_traffic_manager/),
  [issue #7626](https://github.com/carla-simulator/carla/issues/7626)).
  Liveness (heartbeat) and a defined fallback are contract requirements.
- **Shared mutable ownership.** CARLA walkers: multi-client sims collide because
  "each client is only aware of the ones it is in charge of"
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/));
  TraCI has no ownership, only client ordering
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
  Exclusive per-vehicle claim is the standard MMO answer.
- **Everything-mutable API sprawl.** TraCI's change-state table mutates color,
  dimensions, emission class, boarding duration at runtime — the contract
  accretes without taxonomy, and per-domain byte IDs silently renumber (0x41 →
  0xb1 incident) ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html),
  [sumo-devel](https://sourceforge.net/p/sumo/mailman/message/27046983/)).
- **Unversioned or hash-versioned clients.** CARLA's per-release `.egg` and
  client/server version-mismatch warning churn
  ([issue #8758](https://github.com/carla-simulator/carla/issues/8758));
  SUMO's protocol break at 1.0.0 requiring exact client match
  ([FAQ](https://sumo.dlr.de/docs/FAQ.html)).
  The standard fixes: negotiate version at handshake, keep field numbers stable
  forever, add-only evolution (protobuf discipline — which TraCI's own docs
  gesture at: "invent a wrapper with Apache Kafka or Google protocol buffers"
  [TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).

## Mapping to Our Constraints (ADR-0005)

- Intents are command objects stamped with **tick numbers**, buffered and
  batch-applied at tick boundaries in deterministic order — the Command pattern
  with the engine as sole consumer; replay = the logged command stream
  (ADR-0005 §3, §5).
- A controller reacting to tick T influences T+1 at earliest = a built-in
  minimum reaction time; SUMO action-step and Flow's `delay` parameter show
  longer, per-controller reaction times are a normal modeling need, declared
  at attach rather than special-cased
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html),
  [base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
- Engine uniformity over controllers (VISION.md) = the Command pattern's
  producer-agnostic stream; AI policy, scripted scenario, and human keyboard
  are three producers of one message type (highway-env's `manual_control`
  precedent: human input is a config flag, not a subsystem
  [actions docs](https://highway-env.farama.org/actions/index.html)).
