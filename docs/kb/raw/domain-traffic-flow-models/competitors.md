# Simulator Implementations: Traffic Flow Models (Microscopic)

> Source: web research | Researched: 2026-07-15
> How SUMO / Vissim / Aimsun implement car-following, lane changing, and
> intersection behavior — what to copy, what to avoid.

## SUMO

### Car-following
- Default model Krauss (Gipps family, see implementation.md §3); alternatives
  shipped: KraussOrig1, IDM, IDMM, EIDM, Wiedemann, W99, ACC, CACC
  ([vehicle docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)).
- Defaults (passenger): accel 2.6, decel 4.5, emergencyDecel 9.0, sigma 0.5,
  tau 1.0 s, minGap 2.5 m, length 5 m; speedFactor ~ N(1, 0.1)
  ([defaults](https://sumo.dlr.de/docs/Vehicle_Type_Parameter_Defaults.html)).
- Position update is plain Euler; SUMO's IDM has an internal sub-step
  (`stepping` default 0.25 s). `actionStepLength` decouples decision cadence.
- minGap is separate from vehicle length (vs IDM's bumper-to-bumper s0) — ⚠ gap
  convention must be pinned in our vehicle-model ADR.

### Lane changing — LC2013 (Erdmann 2015)
Primary: [DLR preprint](https://elib.dlr.de/102254/1/Springer-SUMOs_Lane_changing_model.pdf).
Replaced DK2008 after concrete failures (motorway-split jams, merge-induced
breakdown, missed turn lanes, single-lane roundabout usage).

- **Four motivations, strict priority**: strategic (reach a lane connected to
  the route) > cooperative (help an urgent changer) > tactical/speed-gain >
  regulatory/keep-right. Right checked before left each step.
- **Dead-lane machinery**: per lane, precompute `bestLanes` (drivable distance
  without changing), `occupation`, `bestLaneOffset`. Urgency when
  `d − o < lookAheadSpeed × |bestLaneOffset| × f`, f = 10 (left) / 20 (right),
  empirically tuned. Historical speed enters lookAheadSpeed so stopped vehicles
  keep urgency.
- **Speed adjustment & cooperation**: blocked changers adjust own speed and
  *request* blockers to decelerate (justified as turn-signal reaction).
  Mainline vehicles only cooperate with on-ramp mergers above 27 m/s —
  protects motorway flow from ramp-demand collapse.
- **Deadlock prevention**: counterLaneChange resolution (blocking follower
  slows near dead-ends so the other completes first); space reservation before
  multi-lane changes (20 m per extra change right, 40 m left — admittedly
  ad hoc: "Eventually they should be … subject to rigorous calibration",
  Erdmann §4.4); don't enter a continuing dead lane to jump queues.
- **Accumulators** prevent oscillation: signed `speedGainProbability`,
  `keepRightProbability` build across steps, halve on sign mismatch.
- **Roundabout special case**: vehicles not yet at their exit edge are forced
  toward the inner lane *against* strategic logic — otherwise a 2-lane ring
  degrades to 1-lane throughput; accepts occasional stranded vehicles.
- Validation numbers: Braunschweig waiting time 89.73→46.66 s; wrong-lane
  teleports 845→7; jam teleports 464→9. Benchmark: 0.26 lane changes per
  veh-km (Lee et al., US highways).
- Parameters: lcStrategic/lcCooperative/lcSpeedGain/lcKeepRight (all 1.0),
  lcAssertive, lcOvertakeRight. **SL2015 sublane model exists for continuous
  lateral dynamics — the door we deliberately don't open** (no in-lane
  swerving per VISION); `--lanechange.duration` is the cheaper middle ground
  ([SublaneModel](https://sumo.dlr.de/docs/Simulation/SublaneModel.html)).

### Junction model
([Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html),
[PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html),
[net format](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html))
- **Internal lanes** = real geometry inside the box (waiting, blocking,
  collisions possible); internal junctions give left-turners a mid-box waiting
  position. `--no-internal-links` = teleport across (no blocking).
- **Junction types**: priority, priority_stop, right_before_left,
  left_before_right, allway_stop, zipper (late merge, 100 m visibility),
  unregulated, traffic_light (+right_on_red), rail types.
- **Right-of-way as data**: per connection a link index; per link
  `<request index response="BITS" foes="BITS" cont>`: `foes` = geometric
  conflicts (symmetric, immutable), `response` = must-yield policy bits
  (junction type + signal state overlay). Bitstrings read right-to-left (a
  documented footgun). Link states M/m/G/g summarize static priority.
- **Gap policy knobs**: `jmTimegapMinor` (default 1 s), `jmStoplineGap`,
  `jmIgnoreFoeProb`, `jmDriveAfterRedTime`; minor links brake to the stop line
  until within `visibilityDistance` (default 4.5 m; 100 m zipper).
- **Impatience**: `max(0, min(1, base + waitingTime/timeToMaxImpatience))`,
  default 180 s to max. At 1.0 the driver takes any collision-safe gap even if
  a foe must brake hard; at 0 only no-slowdown maneuvers. SUMO's
  anti-starvation device = stochastic gap acceptance by waiting.
- **Keep-clear**: no-block heuristic, `keepClear` attr,
  `jmIgnoreKeepClearTime`, `--ignore-junction-blocker <t>` (drive around a
  blocker after t — "real-world circumvention").
- **Turn speed**: `speedLimit = sqrt(radius × 5.5)` inside the box — copy this;
  radius-derived caps make intersection trajectories look right.
- **AWSC**: every vehicle stops ≥1 step, then first-come-first-served by
  stop-line arrival; in-box conflicts resolved by "time of entering, speed,
  right of way rules". No explicit yield-to-the-right on simultaneous arrival —
  ties fall to internal ordering (a gap we can do better on, deterministically).

### Teleporting (the escape hatch we can't use)
([Why Vehicles are teleporting](https://sumo.dlr.de/docs/Simulation/Why_Vehicles_are_teleporting.html))
Vehicle waiting > `--time-to-teleport` (default 300 s) jumps to the next free
edge on its route. Diagnosed causes: wrong lane / yield / jam / blocked.
Erdmann treats teleport counts as *defect telemetry* to drive to zero, not a
feature. Criticism: breaks conservation, corrupts local density/queue metrics.
**A multiplayer authoritative engine cannot teleport visible vehicles** — see
standards-and-patterns.md for the prevention-over-cure policy.

### Known merge/diverge failure modes (GitHub issues)
- Stop at end of acceleration lane (pessimistic anticipation); SUMO's
  documented mitigation: draw acceleration lanes longer than reality
  ([Motorways](https://sumo.dlr.de/docs/Simulation/Motorways.html)).
- Counter-lane-change deadlock near lane ends
  ([#5124](https://github.com/eclipse-sumo/sumo/issues/5124)); LC2013 merge-area
  misbehavior ([#12120](https://github.com/eclipse-sumo/sumo/issues/12120));
  missed exits in dense traffic ([#12758](https://github.com/eclipse-sumo/sumo/issues/12758));
  historical ramp right-of-way inversion ([eclipse/sumo#18](https://github.com/eclipse/sumo/issues/18)).
- Weaving fix: two rightmost mainline lanes both target the off-ramp so
  exiters keep route continuity (Motorways doc).

## PTV Vissim (brief)

- Car-following: Wiedemann 74 (urban) / 99 (freeway) psycho-physical models —
  see implementation.md §6 for why we skip them.
- Intersections: two mechanisms — **priority rules** (stop line + conflict
  markers; min gap time typ. 2–4 s + min headway distance) and **conflict
  areas** (auto-detected overlap; front/rear gap around foe occupancy +
  avoid-blocking %). Priority rules give narrower, repeatable gap
  distributions; conflict areas model anticipation
  ([TRB EC083](https://onlinepubs.trb.org/Onlinepubs/circulars/ec083/59_Baredpaper.pdf)).
- Roundabout capacity calibrated by tuning gap parameters to HCM/NCHRP curves
  ([MassDOT guidance](https://www.mass.gov/doc/massdot-roundabout-vissim-microsimulation-guidance/download)).

## Aimsun (brief)

Give-way model: gap acceptance with **initial safety margin** decaying to
**final safety margin** after `maxGiveWayTime × per-vehicle factor` — the same
impatience-decay idea as SUMO with explicit start/end margins
([micro manual](https://docs.aimsun.com/next/22.0.1/UsersManual/MicrosimulationModellingVehicleMovement.html);
meso uses a simplified version).

## MOBIL (reference model, not a product)

See implementation.md §9. Operational-layer only; needs a strategic wrapper.
traffic-simulation.de runs the ACC variant of IDM + MOBIL live.

## Convergent design (all three products agree)

1. Conflict geometry precomputed as **data** (foes matrix / conflict areas).
2. Per-driver **gap policy** with **waiting-time decay** (starvation-proof).
3. Engine-level **collision backstop** independent of policy.
4. Discrete, instant lane transitions at fixed steps are validated practice
   (MOBIL Δt-insensitive; SUMO default; Vissim time-stepped).

## Positioning: traffic-sim (us)

- Car-following: IDM family (interpretable parameters, string-instability
  realism) with IIDM/ACC fixes and emergency cap; ballistic integrator + stop
  override at 100 ms tick; Newell as the validation-oracle controller.
- Lane change: MOBIL core (acceleration-based safety unifies with junction
  enforcement) + SUMO-style strategic layer over our lane graph (bestLanes,
  urgency, cooperation-as-intent over NATS).
- Junctions: SUMO's foes/response factoring as engine-owned map data; FIFO
  stop queues with explicit deterministic tie-breaks; impatience decay as
  controller policy; no teleporting — prevention + telemetry + physical-only
  resolutions.
