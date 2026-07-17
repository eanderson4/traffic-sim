# Prior Art Survey: Vehicle & Controller Interface

> Source: web research | Researched: 2026-07-16
> "Competitors" here = systems whose engine↔controller contract we can steal from
> or be warned by: traffic simulators (TraCI, MATSim, Vissim), driving simulators
> (CARLA), MARL platforms (SMARTS, highway-env, Flow), scenario standards
> (OpenSCENARIO), and game-networking practice.

## SUMO / TraCI — the incumbent RPC contract

- Binary TCP client/server; 50+ changeable vehicle variables and 70+ retrievable
  ones, from `setSpeed` down to `setColor`, `setEmissionClass`, `setBoardingDuration`
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html),
  [Vehicle Value Retrieval](https://sumo.dlr.de/docs/TraCI/Vehicle_Value_Retrieval.html)).
- Intent vocabulary: speed setpoint (`setSpeed`, held until `-1` reverts),
  smooth `slowDown(v, duration)`, `setAcceleration(a, duration)`,
  `changeLane/changeSublane`, routing (`changeTarget/setRoute/reroute*`),
  `setSignals`, `openGap` (temporary headway adaptation for CACC)
  ([Change Vehicle State](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
- Authority model is a *bitmask, not a boundary*: `speedMode` (default 31 = all
  safety checks on) and `laneChangeMode` (default 1621) let the client selectively
  disable safe speed, accel/decel limits, right-of-way, red-light braking
  ([same](https://sumo.dlr.de/docs/TraCI/Change_Vehicle_State.html)).
- Observations: value retrieval + object subscriptions + context subscriptions
  (objects around an object); model introspection (`getFollowSpeed`,
  `getSecureGap`, `getSpeedWithoutTraCI`)
  ([Vehicle Value Retrieval](https://sumo.dlr.de/docs/TraCI/Vehicle_Value_Retrieval.html)).
- Pain, measured: socket RPC cost — 9,000-vehicle Bologna scenario 90 s with
  polling vs 42 s with subscriptions vs 8 s without TraCI; remedy is in-process
  `libsumo` with identical signatures
  ([TraCI performance](https://sumo.dlr.de/docs/TraCI/index.html)).
- Pain, versioning: protocol break at 1.0.0 ("make sure that TraCI client
  version and SUMO version match", [FAQ](https://sumo.dlr.de/docs/FAQ.html));
  silent variable-ID renumbering 0x41→0xb1 broke a client unnoticed
  ([sumo-devel](https://sourceforge.net/p/sumo/mailman/message/27046983/)).
- Handoff precedent: ToC Device models automated→manual transitions with
  take-over request, lead time, minimum-risk maneuver, and post-handover
  awareness recovery ([ToC Device](https://sumo.dlr.de/docs/ToC_Device.html)).
- **vs traffic-sim (us):** TraCI proves the demand for online vehicle control is
  enormous (Veins, Flow, SMARTS all built on it) and shows exactly what an
  RPC-shaped version of our contract costs. Our async NATS intents keep
  TraCI's vocabulary ideas (speed/lane/route/signals + introspection) but must
  not inherit its barrier stepping, bitmask-discretionary safety, or byte-ID
  versioning.

## CARLA (+ Traffic Manager) — high-fidelity actor/agent API

- Two control regimes: per-vehicle `apply_control(VehicleControl)` raw actuation
  vs `set_autopilot(True, tm_port)` delegating to the Traffic Manager
  ([Python API tutorial](https://carla.readthedocs.io/en/0.9.7/python_api_tutorial/),
  [TM docs](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- TM is a *client-side* fleet controller: staged pipeline (localization,
  collision, traffic-light, motion planner PID → `VehicleControl`, vehicle
  lights), commands batched `apply_batch_sync` per frame; per-vehicle behavior
  knobs (`vehicle_percentage_speed_difference`, `distance_to_leading_vehicle`,
  `force_lane_change`, `ignore_lights_percentage`, `auto_lane_change`)
  ([TM docs](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- Observations = sensors with declared fidelity: GNSS/IMU/LiDAR noise models
  with seeds, `sensor_tick` cadence, lane-invasion/collision/obstacle event
  sensors ([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).
- Multi-controller reality is messy: TM-Server dictates TM-Clients on the same
  port; "only one client should tick" in sync mode; `enable_constant_velocity`
  silently overrides the TM; `set_autopilot(False)` can wedge vehicles holding
  their last control ([issue #7626](https://github.com/carla-simulator/carla/issues/7626));
  client-managed walkers collide across clients because "each client is only
  aware of the ones it is in charge of"
  ([Python API](https://carla.readthedocs.io/en/latest/python_api/)).
- Disconnect default: TM shutdown leaves vehicles "immobile on the map" unless
  the user destroys them ([TM docs](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- Versioning pain is chronic: client/server version-mismatch warnings
  ([issue #8758](https://github.com/carla-simulator/carla/issues/8758)),
  per-release `.egg` pinning, semantic tags renumbered 0.9.13→0.9.14
  ([Sensors reference](https://carla.readthedocs.io/en/latest/ref_sensors/)).
- **vs traffic-sim (us):** CARLA's sensor-declaration model (fidelity as config
  with seeded noise) is the right observation-fidelity precedent. Its failure
  modes — sticky last-control on disconnect, no ownership enforcement between
  controllers, version-mismatch support churn — are our spec's anti-requirements:
  explicit per-vehicle ownership, heartbeat with fallback, and contract version
  negotiated at attach.

## MATSim mobsim — agent as plan-executor, engine pulls decisions

- `MobsimDriverAgent` is a Java interface the *engine calls into*:
  `chooseNextLinkId()` at nodes, `notifyMoveOverNode`, `isWantingToArriveOnCurrentLink`,
  `setVehicle/getVehicle` for agent↔vehicle binding
  ([doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_driver_agent.html)).
- Agents follow day plans (activity/leg sequences); within-day replanning rewrites
  plans of selected agents mid-run ([Padgham & Nagel et al.](https://depositonce.tu-berlin.de/bitstreams/0baf10d3-c7c6-4713-91d5-f0b2f6ec913d/download)).
- One agent = one person = ≤1 vehicle at a time; the physical vehicle is a
  separate `MobsimVehicle` object ([doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_driver_agent.html)).
- No wire protocol, no versioning story: the interface churns in-tree (doxygen
  design comments unresolved since 2012–2014: "May be null", "Should be renamed")
  ([MobsimAgent doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1mobsim_1_1framework_1_1_mobsim_agent.html)).
- **vs traffic-sim (us):** MATSim's `chooseNextLinkId()` is the cleanest existing
  answer to "turn choice at junction" — route decisions are the agent's,
  movement is the engine's. But its pull/in-process shape can't support remote
  human controllers; we need the same decision split pushed over NATS.

## SMARTS — heterogeneous agents as versioned, declared interfaces

- `AgentSpec = AgentInterface + policy`; the interface declares sensors
  (waypoints lookahead, neighborhood radius, grid maps, lidar) AND one of ~10
  `ActionSpaceType`s (Continuous, ActuatorDynamic, Lane, LaneWithContinuousSpeed,
  Direct, MPC, Trajectory, TrajectoryWithTime, TargetPose...)
  ([agent docs](https://smarts.readthedocs.io/en/latest/sim/agent.html),
  [controllers source](https://smarts.readthedocs.io/en/latest/_modules/smarts/core/controllers.html)).
- Agents register in a zoo under versioned locators (`smarts.zoo:name-vX`) and
  can run in separate processes/hosts
  ([agent docs](https://smarts.readthedocs.io/en/latest/sim/agent.html)).
- Bubbles hand background (SUMO) vehicles to zoo agents inside spatiotemporal
  regions and back on exit — heterogeneous controllers trading the same vehicle
  ([paper](https://arxiv.org/pdf/2010.09776.pdf)).
- Under the hood it is SUMO for background traffic + Bullet physics for
  agent vehicles ([review, arXiv:2412.14207](https://arxiv.org/pdf/2412.14207)).
- **vs traffic-sim (us):** SMARTS is the closest existing proof that AI policies
  with wildly different I/O needs can share one engine via *declared* interfaces.
  Our contract should adopt declaration-at-attach (observation window + intent
  dialect + cadence) but with a single canonical intent vocabulary where SMARTS
  has ~10 Python-only action spaces.

## highway-env — the minimal viable intent vocabulary

- Two rungs only: `ContinuousAction` (throttle/steering in [-1,1] mapped to
  ±5 m/s², ±0.785 rad, clipped) and `DiscreteMetaAction`
  `{LANE_LEFT, IDLE, LANE_RIGHT, FASTER, SLOWER}` on top of built-in
  speed/lane-tracking controllers
  ([actions docs](https://highway-env.farama.org/actions/index.html)).
- Unavailable actions degrade to `IDLE`, never error
  ([same](https://highway-env.farama.org/actions/index.html)).
- `manual_control: True` maps arrow keys onto the same ego-vehicle — human
  arcade input is a config flag, not a new subsystem
  ([same](https://highway-env.farama.org/actions/index.html)).
- Observations are fixed-shape: V×F kinematics array (ego row 0, presence flag,
  sorted, ego-relative option, `observe_intentions` option), occupancy grid, TTC,
  lidar ([observations docs](https://highway-env.farama.org/observations/index.html)).
- **vs traffic-sim (us):** evidence that 5 meta-actions + clipping suffice for
  both RL training and keyboard play on highways. Our junctions need one more
  axis (turn/route choice), but the "unavailable → safe no-op" rule and
  human-as-config-flag are directly adoptable.

## Flow (Berkeley) — accel-only controllers with a failsafe chain

- All custom controllers implement `get_accel() → float|None`; `None` = SUMO
  drives this step; vehicles inside junctions always cede to SUMO
  ([base_controller.py](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
- RL and classical controllers (IDMController, FollowerStopper, PISaturation)
  share the base class; controller limits come from the vehicle's
  `car_following_params` (accel/decel), plus optional Gaussian action noise and
  action delay ([same](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
- Failsafe chain — `instantaneous`, `safe_velocity`, `feasible_accel`,
  `obey_speed_limit` — applied in declared order to every requested acceleration
  ([same](https://raw.githubusercontent.com/flow-project/flow/master/flow/controllers/base_controller.py)).
- **vs traffic-sim (us):** Flow is the empirical proof that "acceleration out,
  safety clamps in the engine" supports a real RL research program
  ([Vinitsky dissertation](https://escholarship.org/content/qt0hg266sp/qt0hg266sp.pdf)).
  Its failsafe list is our clamping layer almost verbatim; its
  `None` fallback is the graceful-disconnect primitive.

## OpenSCENARIO / Vissim / Aimsun — scenario & vendor API shapes

- OpenSCENARIO 1.x `PrivateAction` vocabulary: `SpeedAction` (with dynamics
  shape/dimension), `LaneChangeAction` (relative/absolute target lane),
  `LaneOffsetAction`, `SynchronizeAction`, `TeleportAction`, `AssignRouteAction`,
  `AcquirePositionAction`, `FollowTrajectoryAction`, plus controller primitives
  `AssignControllerAction`/`ActivateControllerAction`/`OverrideControllerValueAction`
  ([ASAM migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html);
  supported-action lists in engines, e.g. [openPASS](https://eclipse.dev/openpass/content/html/user_guide/configs/scenario.html)).
- OpenSCENARIO 2.0 pivots to declarative `drive()` + modifiers and notes this is
  "less constrained" than 1.x imperative actions — scenario-as-constraints, the
  engine resolves execution
  ([migration guide](https://www.asam.net/static_downloads/public/asam-openscenario/2.0.0/migration/mg_actions.html)).
- Vissim/Aimsun keep controller replacement in-process: Vissim's external
  controller logic runs via DLL/EXE add-ons (signal controllers; a paid Driving
  Simulator Interface DLL for human-in-the-loop)
  ([PTV FAQ](https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs),
  [CARLA-PTV co-sim](https://carla.readthedocs.io/en/0.9.14/adv_ptv/)).
- **vs traffic-sim (us):** OpenSCENARIO is the standardized superset of our
  scenario-trigger vocabulary ([[concept-scenario-format]]) — but it addresses a
  *scenario engine*, not a live controller. It tells us which verbs survive
  standardization (speed, lane, route, controller override) and warns that
  teleport-style actions are explicitly "non-physical" requests a simulator "may
  or may not be able to fulfill".

## Game industry — commands, authority, anti-cheat

- **Command pattern:** "A command is a *reified method call*" — input handlers
  and AI both emit command objects into a stream; "the AI code simply emits
  `Command` objects", so the player can control any actor and AI can drive the
  player's actor in demo mode ([Game Programming Patterns](https://gameprogrammingpatterns.com/command.html)).
- **Authoritative server:** "don't trust the player"; clients send intents
  ("I want to move one square right"), never state assertions ("I'm at (10,10)")
  ([Gambetta](https://www.gabrielgambetta.com/client-server-game-architecture.html)).
- **vs traffic-sim (us):** VISION.md's "engine treats all controllers uniformly"
  is the Command pattern's AI/input unification; ADR-0005's intent log is the
  command stream persisted. Games add the trust argument the sim literature
  lacks: once humans join over a network, every clamp is also an anti-cheat
  boundary, so clamping belongs in the engine, not in client libraries.
