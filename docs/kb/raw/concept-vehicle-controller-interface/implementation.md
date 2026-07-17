# Mechanics: Vehicle & Controller Interface

> Source: web research (greenfield — no engine or wire protocol exists; this file
> collects the *mechanisms* an engine↔controller contract is built from, to be
> re-audited against real code once the ADR on the controller contract exists)
> | Researched: 2026-07-16 | Git HEAD: ae75fba

## 1. The intent vocabulary ladder (raw actuation → declarative constraint)

Every surveyed system exposes controller input at one or more rungs of the same
ladder. Low rungs give fidelity, high rungs give convenience and safety:

- **Raw actuation.** CARLA's `VehicleControl(throttle, steer, brake, hand_brake,
  reverse, manual_gear_shift, gear)`, applied via `vehicle.apply_control(...)`
  ([Python API tutorial](https://carla.readthedocs.io/en/0.9.7/python_api_tutorial/)).
  SMARTS `ActionSpaceType.Continuous` is the same triple
  `(throttle, brake, steering)` clipped to `[0,1],[0,1],[-1,1]`
  ([controllers source](https://smarts.readthedocs.io/en/latest/_modules/smarts/core/controllers.html)).
  highway-env `ContinuousAction` maps `[-1,1]` onto an acceleration range of
  ±5 m/s² and steering range of ±0.785 rad, with `clip=True` by default
  ([actions docs](https://highway-env.farama.org/actions/index.html)).
- **Kinematic setpoints.** CARLA `set_target_velocity` — "applied before the
  physics step so the resulting velocity will be affected by external forces such
  as friction" — and `enable_constant_velocity`, whose docs warn it "overrides any
  changes in velocity by the TM"
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
  SMARTS `ActionSpaceType.Direct` sets speed directly
  ([controllers source](https://smarts.readthedocs.io/en/latest/_modules/smarts/core/controllers.html)).
  TraCI `setSpeed` holds the set speed across steps (subject to the speedMode
  safety rules); sending `-1` "will revert to its original behavior (using the
  maxSpeed of its vehicle type and following all safety rules)"
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
- **Acceleration.** TraCI `setAcceleration(a, duration)`; Flow's whole controller
  stack is `get_accel() → float m/s²`, with `None` meaning "let SUMO drive this
  step" ([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
  IDM-family models natively output acceleration — [[domain-traffic-flow-models]].
- **Discrete meta-actions.** highway-env `DiscreteMetaAction`:
  `{LANE_LEFT, IDLE, LANE_RIGHT, FASTER, SLOWER}` — setpoints consumed by
  built-in speed/steering controllers; an unavailable action is silently
  equivalent to `IDLE`
  ([actions docs](https://highway-env.farama.org/actions/index.html)).
  SMARTS `ActionSpaceType.Lane`: `{keep_lane, slow_down, change_lane_left,
  change_lane_right}` with hard-coded nominal speeds (15 m/s keep, 12.5 m/s lane
  change) ([controllers source](https://smarts.readthedocs.io/en/latest/_modules/smarts/core/controllers.html)).
- **Lane/route targets.** TraCI `changeLane(index, duration)`,
  `changeSublane(latDist)`, `changeTarget(edgeID)`, `setRoute(edgeList)`,
  `rerouteTraveltime` ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
  MATSim inverts the direction: the engine *asks* the agent `chooseNextLinkId()`
  at each node — a pull interface, not pushed intents
  ([MobsimDriverAgent doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_driver_agent.html)).
- **Trajectories.** SMARTS `Trajectory`, `MPC`, `TrajectoryWithTime`,
  `TargetPose` action spaces (waypoint sequences tracked by PD/MPC controllers)
  ([controllers source](https://smarts.readthedocs.io/en/latest/_modules/smarts/core/controllers.html)).
  OpenSCENARIO `FollowTrajectoryAction`
  ([ASAM migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).
- **Declarative constraints.** OpenSCENARIO 2.0's `drive()` with modifiers —
  `ego.drive() with: speed(50kph); position(behind: car1, 20m)` — the scenario
  states *what*, the simulator resolves *how*; the migration guide notes `drive()`
  "can result in a scenario specification that is less constrained than when
  using the equivalent specialized action"
  ([ASAM migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).

**Reading for us:** the ladder collapses to four orthogonal intent axes that all
surveyed systems share — longitudinal (accel or speed target), lateral (lane
target), routing (next link / route / destination), and signalling (TraCI
`setSignals`, CARLA TM vehicle-lights stage). SMARTS demonstrates that one
engine can host the whole ladder at once by dispatching on the declared action
space; the declaration, not the wire format, carries the rung.

## 2. Application semantics — when an intent takes effect

- **TraCI's documented per-step order:** getters return step *n-1* values;
  `moveTo` applies instantly; `vNext` for step *n* is computed from previous-step
  state ("`traci.vehicle.setSpeed` overrides this"); position integrates (Euler
  or ballistic); `moveToXY` overrides the computed position last
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
  This is exactly our ADR-0005 "intents batch-applied at tick boundaries" — but
  TraCI had to document it retroactively because users couldn't predict it.
- **CARLA batching:** `client.apply_batch(commands)` executes a list "on a single
  simulation step"; `apply_batch_sync` blocks until applied and returns per-command
  responses ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
  The determinism docs warn that in a busy server "single issued commands can
  become lost" unless batched in `apply_batch_sync`
  ([Synchrony and time-step](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/)).
- **CARLA TM's internal pipeline:** staged control loop (localization → collision
  → traffic-light → motion-planner → vehicle-lights) with synchronization
  barriers between stages, ending in a command array batched to the server "to be
  applied in the same frame" — a controller that is itself tick-structured
  ([Traffic Manager](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- **Blocking vs async:** TraCI multi-client mode synchronizes clients "after
  every simulation step... the simulation does not advance to the next step until
  all clients have called the 'simulationStep' command"
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)); CARLA synchronous
  mode waits for `world.tick()`
  ([Synchrony and time-step](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/)).
  Both are the barrier ADR-0005 rejected; see [[arch-time-model]] (11× TraCI slowdown).

## 3. Observation models — what the controller gets to see

- **Full-state RPC (TraCI):** ~70 retrievable vehicle variables (speed, position,
  lane, route, signals, leader via `getLeader(dist)`, neighbors via
  `getNeighbors`, upcoming traffic lights via `getNextTLS`, upcoming links via
  `getNextLinks`, best lanes) ([Vehicle Value Retrieval](https://sumo.dlr.de/docs/TraCI/Vehicle_Value_Retrieval.html)).
  Plus **model introspection**: `getFollowSpeed`, `getSecureGap`, `getStopSpeed`
  ask the car-following model what it would do, and `getSpeedWithoutTraCI`
  returns "the speed that the vehicle would drive if no speed-influencing command
  such as setSpeed or slowDown was given" — the fallback model's counterfactual,
  exposed as a first-class query
  ([Vehicle Value Retrieval](https://sumo.dlr.de/docs/TraCI/Vehicle_Value_Retrieval.html)).
- **Subscriptions:** TraCI object subscriptions and *context* subscriptions
  (values of objects surrounding another object) cut the Bologna 9,000-vehicle
  position-polling benchmark from 90 s to 42 s vs 8 s without TraCI; plain polling
  ~25k vehicles/s, subscriptions ~50k
  ([TraCI performance](https://sumo.dlr.de/docs/TraCI/index.html)).
- **Snapshots + sensors (CARLA):** `WorldSnapshot` of `ActorSnapshot`s once per
  tick; actor getters return "the actor's ... the client received during last
  tick" without calling the simulator
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
  Fidelity is modeled per-sensor with *seeded noise*: GNSS/IMU expose
  `noise_*_stddev`/`noise_*_bias` plus `noise_seed`; LiDAR has
  `dropoff_general_rate` and `noise_stddev`; every sensor has `sensor_tick`
  decoupling its cadence from the world tick
  ([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).
- **Declared observation interfaces (SMARTS):** `AgentInterface` selects sensors —
  `NeighborhoodVehicles(radius=50)`, `Waypoints(lookahead=50)`, grid maps, RGB,
  lidar — and the observation space is *derived from the declaration*; the docs
  warn grid/RGB rendering "may significantly slow down the environment `step()`"
  ([agent docs](https://smarts.readthedocs.io/en/latest/sim/agent.html)).
- **Fixed-shape windows (highway-env):** `KinematicObservation` is a V×F array —
  ego always row 0, `presence` flag disambiguates padding, `vehicles_count` fixes
  the window size, coordinates optionally ego-relative, `order: sorted`,
  optional `observe_intentions` (other vehicles' destinations)
  ([observations docs](https://highway-env.farama.org/observations/index.html)).

**Reading for us:** the spectrum is *everything by RPC* (TraCI) → *per-agent
declared window* (SMARTS/highway-env). AoI-window design belongs to
[[arch-state-authority]]; the contract just needs to carry the window's shape.

## 4. Capability description — what a vehicle IS vs what a driver DOES

- **SUMO vType:** static capability bundle (length, maxSpeed, accel, decel,
  minGap, sigma imperfection, tau headway) retrievable and *mutable at runtime*
  per-vehicle via TraCI (`setAccel`, `setDecel`, `setLength`, `setTau`...);
  changing a vType-derived value copies the type (`typeid@vehid`) so the vehicle
  is decoupled from further type edits
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html),
  [Vehicle Value Retrieval](https://sumo.dlr.de/docs/TraCI/Vehicle_Value_Retrieval.html)).
- **CARLA blueprints:** `ActorBlueprint` = id + attributes, each attribute
  explicitly `is_modifiable` or not, with `recommended_values`; vehicles spawned
  from `world.spawn_actor(blueprint, transform)`
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
- **Vehicle/driver separation is explicit in MATSim:** `VehicleUsingAgent` has
  `setVehicle(MobsimVehicle)` / `getVehicle()` — the agent and the physical
  vehicle are different objects that bind and unbind
  ([MobsimDriverAgent doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_driver_agent.html)).
- **Flow parameterizes controller limits from vehicle params:** `BaseController`
  reads `max_accel`/`max_deaccel` from the vehicle's `car_following_params`, i.e.
  the clamp envelope is a *vehicle capability*, not a controller constant
  ([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).

## 5. Engine authority — clamping, failsafes, and safety overrides

- **SUMO speedMode bitmask** decides which checks stand between a TraCI speed
  command and the vehicle: bit0 regard safe speed, bit1 max acceleration, bit2
  max deceleration, bit3 right-of-way at intersections, bit4 brake-hard for red,
  bit5/bit6 *dis*regard in-intersection right-of-way and speed limit. Default
  31 = all checks on; legacy 0 = all off ("run a red light" recipes documented)
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
  The `laneChangeMode` bitmask (default 1621) arbitrates TraCI lane requests vs
  the lane-change model's own motives per motive class (strategic, cooperative,
  speed-gain, keep-right)
  ([same](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
- **Flow failsafe chain:** controller acceleration passes an ordered list of
  guards — `instantaneous` (stop if collision next step), `safe_velocity`
  (kinematic headway bound with reaction delay term), `feasible_accel` (clamp to
  vehicle accel/decel), `obey_speed_limit` — each printing "clipping applied"
  warnings ([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
- **CARLA RSS:** the `RssRestrictor` uses Responsibility-Sensitive-Safety output
  "to adapt a `carla.VehicleControl` before applying it to a vehicle" — a safety
  filter sitting between controller and actuator
  ([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).
- **The unsafe escape hatches:** TraCI `moveTo`/`moveToXY` — "No collision checks
  are done, this means that moving the vehicle may cause a collision"
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html));
  CARLA `set_location`/`set_transform` teleports
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
  Game servers rejected this whole class: the client says "I want to move one
  square right", never "I'm at (10,10)"
  ([Gambetta](https://www.gabrielgambetta.com/client-server-game-architecture.html)).
- **highway-env clip + IDLE-fallback:** actions are clipped to ranges; unavailable
  discrete actions degrade to `IDLE` rather than erroring
  ([actions docs](https://highway-env.farama.org/actions/index.html)).

## 6. Attach, claim, handoff, disconnect

- **TraCI multi-client handshake:** `--num-clients N`; every client registers an
  integer via `SetOrder`; commands execute lowest-order-first within each step;
  "the simulation will only start once all clients have connected"
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
  No ownership: any client may command any vehicle; precedence is only by order.
- **CARLA TM server/client by port:** first TM on a port becomes TM-Server and
  "will dictate the behavior of all the TM-Clients"; vehicles opt in via
  `set_autopilot(True, tm_port)` ([Traffic Manager](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
  Multi-controller conflicts are documented hazards: `enable_constant_velocity`
  "overrides any changes in velocity by the TM"
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/));
  `set_autopilot(False)` can leave vehicles stuck holding their last
  `VehicleControl` and immune to further control
  ([issue #7626](https://github.com/carla-simulator/carla/issues/7626)).
  Walkers are worse: "the client is in charge of managing pedestrians... if you
  spawn walkers through different clients, collisions may happen, as each client
  is only aware of the ones it is in charge of"
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
- **Graceful release semantics (SUMO):** `setSpeed(-1)` reverts to model behavior;
  `resume` ends a stop; speedMode/laneChangeMode decide whether the internal
  model or the external request wins *while attached*
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
- **ToC Device (SUMO/TransAID):** a full take-over-of-control state machine —
  take-over request via `device.toc.requestToC` with lead time; if
  `responseTime > availableLeadTime` a Minimum-Risk-Maneuver (constant
  `mrmDecel` braking) runs; after handover, driver `awareness` recovers linearly
  at `recoveryRate` ([ToC Device](https://sumo.dlr.de/docs/ToC_Device.html);
  paper via [EasyChair](https://easychair.org/publications/paper/NLJG/open)).
- **Flow's per-step fallback:** returning `None` from `get_accel` cedes the step
  to SUMO; vehicles inside junctions (`edge == ":"`) are *always* ceded
  ([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
- **SMARTS bubbles:** spatiotemporal regions where background-traffic vehicles
  (SUMO-driven) are handed to Social-Agent-Zoo agents as they cross the membrane,
  and handed back on exit — geography-triggered control transfer between
  heterogeneous controllers ([SMARTS paper, CoRL 2020](https://arxiv.org/pdf/2010.09776.pdf)).
- **CARLA TM shutdown:** "the user must destroy the vehicles controlled by it,
  otherwise they will remain immobile on the map" — the absent-fallback default
  ([Traffic Manager](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- **OpenSCENARIO controller actions:** `AssignControllerAction`,
  `ActivateControllerAction`, `OverrideControllerValueAction` make
  attach/activate/override first-class scenario primitives
  ([ASAM migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).

## 7. How many vehicles per controller

- **TraCI:** a client may command any number of vehicles (no ownership at all);
  multi-client ordering via `SetOrder`
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
- **CARLA TM:** one TM controls the whole registered fleet from one client-side
  process (the `generate_traffic.py` example registers every spawned vehicle);
  one ego-agent per client in RL practice
  ([Traffic Manager](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- **SMARTS:** agents are per-ego-vehicle; each agent can run "in a separate
  process... remotely"; bubbles dynamically associate extra social vehicles
  ([paper](https://arxiv.org/pdf/2010.09776.pdf)).
- **MATSim:** one agent = one person = at most one driven vehicle at a time;
  `MobsimVehicle` binding is explicit
  ([MobsimDriverAgent doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_driver_agent.html)).
- **Vision fit:** VISION.md says "one or more vehicles" per controller and
  multiplayer chaos wants humans claiming single cars — the contract should
  permit N:1 with exclusive per-vehicle ownership (the CARLA-walkers collision
  hazard is what happens without exclusivity).

## 8. Versioning mechanics (how these contracts evolve, painfully)

- **TraCI:** binary protocol with per-domain command/variable byte IDs; a version
  handshake command exists (`get version`, 0x00) and the release process includes
  "check whether the TraCI version needs to be incremented"
  ([HowToRelease](https://sumo.dlr.de/docs/Developer/HowToRelease.html)).
  Still, variable IDs have been silently renumbered — `VAR_SPEED_WITHOUT_TRACI`
  moved 0x41 → 0xb1 between revisions, breaking a client "by pure luck" discovered
  late ([sumo-devel mail](https://sourceforge.net/p/sumo/mailman/message/27046983/));
  and the whole protocol changed at SUMO 1.0.0: "Please make sure that TraCI
  client version and SUMO version match"
  ([FAQ](https://sumo.dlr.de/docs/FAQ.html)).
  The TraCI authors themselves note the byte-fiddling could "in the long run" be
  replaced by protobuf/Kafka-style wrappers
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
- **CARLA:** client/server version check prints "WARNING: Version mismatch
  detected... Client API version / Simulator API version" — a persistent support
  burden across issues ([e.g. #8758](https://github.com/carla-simulator/carla/issues/8758),
  [#7981](https://github.com/carla-simulator/carla/issues/7981)); Python clients
  pin per-version `.egg` files (`carla-%d.%d-%s.egg`);
  semantic-segmentation tags changed between 0.9.13 and 0.9.14, noted inline in
  the sensor docs ([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).
- **SMARTS zoo:** agent registration requires a versioned locator string
  `module.path:agent-name-vX` — versioning built into the naming scheme
  ([agent docs](https://smarts.readthedocs.io/en/latest/sim/agent.html)).
- **MATSim:** no wire contract at all — the mobsim API is Java interfaces whose
  own doxygen carries decade-old unresolved design comments ("May be null (e.g.
  for cruising taxi drivers)"; "Should be renamed...")
  ([MobsimAgent doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_agent.html)).

## 9. Decision cadence ≠ tick cadence

- **SUMO action step length:** per-vehicle `setActionStepLength` decouples how
  often a vehicle *decides* from the integration step; retrievable via
  `getActionStepLength`/`getLastActionTime`
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
- **highway-env `policy_frequency`:** the agent acts at e.g. 2 Hz while physics
  runs faster ([observations docs](https://highway-env.farama.org/observations/index.html)).
- **CARLA `sensor_tick`:** each sensor's capture cadence decouples from world
  tick ([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).
- **Fit with ADR-0005:** our 1-tick intent latency *is* a reaction time (min
  100 ms at 10 Hz); letting a controller declare a slower decision cadence (every
  k ticks) models human reaction (~0.5–1 s) without special-casing, exactly the
  SUMO action-step pattern. The engine still applies intents only at tick
  boundaries in deterministic order.
