# Mechanics: Engine Time Model

> Source: web research (greenfield — no engine code exists; this file collects the
> *mechanisms* a time model is built from, to be re-audited against real code once
> ADR-0005 is Accepted and the loop exists) | Researched: 2026-07-15 | Git HEAD: c7a1056

## 1. The fixed-timestep pattern (Fiedler, canonical)

From Glenn Fiedler's "Fix Your Timestep!" ([gafferongames.com](https://gafferongames.com/post/fix_your_timestep/)):

- **Variable timestep is rejected outright** for anything with dynamics: feeding
  measured frame time into the integrator makes behavior framerate-dependent — from
  subtle "feel" differences up to "your spring simulation exploding to infinity."
  Car-following models (IDM etc.) are stiff ODEs with the same stability limits.
- **Fixed timestep with accumulator**: wall clock *produces* time, the sim *consumes*
  it in whole `dt` increments; remainder persists across frames:

  ```
  accumulator += frameTime
  while accumulator >= dt:
      integrate(state, t, dt); accumulator -= dt; t += dt
  ```

  Only whole fixed-size steps are ever taken — this is the property that makes a sim
  reproducible.
- **Spiral of death**: if simulating X sim-seconds costs Y > X wall-seconds, the
  accumulator grows without bound. Mitigations: per-tick cost well under budget;
  clamp the accumulator as a safety valve (drop time — acceptable for rendering,
  NOT for an authoritative deterministic sim; see overload policies in §5).
- **Render interpolation**: the leftover `accumulator/dt` fraction blends previous
  and current states *for display only*. Implication for us: the engine never
  interpolates; the visualization client interpolates between two published
  snapshots. Sim tick rate ≠ snapshot publish rate ≠ render FPS.

## 2. DES machinery (what a discrete-event core is made of)

- **Future event list (FEL)**: priority queue sorted by event time; clock jumps to
  the next event; "no change in the system is assumed to occur" between events
  ([Wikipedia DES](https://en.wikipedia.org/wiki/Discrete-event_simulation)).
- Next-event advance "skips inactive periods"; fixed-increment advance "is slower
  but sometimes necessary" when state changes depend on many simultaneous,
  continuously interacting conditions (Banks et al. ch. 3,
  [lecture PDF](https://santoshhiremath.weebly.com/uploads/6/7/0/5/67052617/unit2.pdf);
  [Vangheluwe McGill notes](https://www.cs.mcgill.ca/~hv/classes/MS/discreteEvent.pdf)).
- **Three world views** + the three-phase approach (Tocher/Pidd): A-phase advance
  clock to next bound event; B-phase execute bound activities; C-phase repeatedly
  scan conditional activities until none fire
  ([Pidd, Simulation](https://journals.sagepub.com/doi/10.1177/003754979306000603)).
- **Simultaneous-event tie-breaking must be explicitly deterministic**: Aimsun sorts
  its event list by (time, priority) with priorities putting signal changes and
  arrivals before statistics ([Aimsun meso docs](https://docs.aimsun.com/next/24.0.0/UsersManual/MesoDiscreteSimulation.html));
  a from-scratch engine needs (time, priority, stable sequence id). See also
  [arXiv:2105.00069](https://arxiv.org/pdf/2105.00069) on unbiased deterministic
  total ordering.
- **When DES loses — the degenerate-FEL argument**: if N vehicles all update every
  Δt (car-following evaluated continuously), every vehicle has an event every Δt
  anyway; the FEL becomes a priority-queue-mediated tick with O(log n) insert/pop
  per vehicle-update and no skipped time — strictly worse than an array sweep.
  Standard textbook reasoning, corroborated by revealed preference: every surveyed
  microscopic simulator uses fixed steps for the car-following layer
  (see competitors.md).

## 3. Hybrid integration idioms (tick core + event edges)

Three independently-invented versions of the same idiom — **the DES side subscribes
to the tick; the tick side consumes DES outputs at tick boundaries**:

1. **Aimsun clock module**: "a general clock module is used to generate
   synchronization events for the mesoscopic model" — the micro tick is injected
   into the meso event list as a recurring event
   ([Aimsun hybrid tech note](https://www.aimsun.com/technical-notes/hybrid-simulation/),
   [manual](https://docs.aimsun.com/next/24.0.2/UsersManual/HybridSimulator.html)).
2. **Burghout's Mezzo/MiMe**: the event-based meso model "books an event at (t+0.1)
   in the event list to look again for a message from Mitsim" (fixed 0.1 s micro)
   ([KTH thesis PDF](https://www.kth.se/polopoly_fs/1.742065.1600688570!/hybrid%20mesoscopic.pdf)).
3. **Aimsun micro itself** is documented as "a hybrid simulation process, combining
   an event scheduling approach with activity scanning": each cycle processes
   scheduled unconditional events (signal changes) first, then scans/updates all
   vehicles ([Aimsun micro process](https://docs.aimsun.com/next/22.0.1/UsersManual/MicrosimulationProcess.html)).
   I.e. even a "fixed-tick" simulator internally keeps a scheduled-event list that
   fires at tick boundaries — likely the right internal shape for our engine
   (signal phase changes, spawn schedules as events; vehicle sweep as the tick).

Formal grounding: DEVS can embed DEV&DESS (combined discrete+continuous) — a
fixed-tick loop is formally a DES whose recurring event advances the world by Δt
([Embedding DEV&DESS in DEVS](https://www.researchgate.net/publication/228848250_Embedding_DEVDESS_in_DEVS),
[DEVS](https://en.wikipedia.org/wiki/DEVS)). The design question is not either/or
but *which layer owns the clock*.

**mosaik 3.0** (smart-grid co-simulation) is the best-documented modern hybrid
scheduler ([arXiv:2410.16937](https://arxiv.org/abs/2410.16937)):
- Three component types: *time-based* (fixed intervals, persistent outputs),
  *event-based* (stepped on input arrival or scheduled event, transient outputs),
  *hybrid* (per-attribute).
- **`max_advance` lookahead grants**: scheduler tells each component how far it can
  advance without expecting new inputs — fixed a mosaik-2 pain point of "unnecessary
  self-stepping to avoid missing messages."
- **Async `set_event()` for Human-in-the-Loop**: an asynchronous real-world input is
  mapped onto a scheduled event at the next safe simulation time — exactly our
  human-driver-intent problem.
- **Integer time base** with declared `time_resolution` (e.g. 0.001 = 1 ms per unit)
  — never float seconds for the clock.

## 4. Decoupled cadences (proven separable knobs)

Prior art separates four rates that naive designs conflate:

| Cadence | Prior art |
|---|---|
| Physics integration step | CARLA physics substepping: `fixed_delta_seconds ≤ max_substeps × max_substep_delta_time`, substep "ideally below 0.01" s ([CARLA docs](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/)) |
| Agent decision step | SUMO `--default.action-step-length`: car-following/lane-change decisions at coarser intervals than integration; setting it switches to ballistic integration ([SUMO docs](https://sumo.dlr.de/docs/Simulation/Basic_Definition.html)); Vissim "simulation resolution", default 10 steps/s, typical 5–10 ([WisDOT defaults](https://wisconsindot.gov/dtsdManuals/traffic-ops/manuals-and-standards/teops/16-20att6.3.pdf)) |
| Snapshot publish rate | Source engine `cl_updaterate` default 20/s vs 66.67 Hz sim tick ([Valve wiki](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), [mirror](https://gist.github.com/CoolOppo/fe0586836de3fb2f90f9)) |
| Render FPS | Client-side interpolation from snapshot buffer (Fiedler, Valve, [Gambetta](https://www.gabrielgambetta.com/entity-interpolation.html)) |

## 5. Pacing drivers: one step function, three wrappers

The pattern that falls out of Gymnasium + Nakama/Ebiten + Fiedler triangulation:
a step function `Tick(n)` with **zero time syscalls inside**, wrapped by
interchangeable drivers:

- **Unpaced**: `for { Tick(n++) }` flat out — headless batch. Gymnasium's
  `Env.step()` is the purest expression: "Run one timestep of the environment's
  dynamics using the agent actions"; no wall clock in the semantics at all
  ([Gymnasium API](https://gymnasium.farama.org/api/env/)).
- **Paced**: sleep-until-deadline at 1× (or k×). Nakama: "Your tick rate represents
  the desired frequency (per second) at which the server calls the match loop
  function"; each loop must finish before the next is scheduled
  ([Heroic Labs docs](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/)).
  Ebitengine: fixed TPS default 60, decoupled from render FPS
  ([SetTPS](https://pkg.go.dev/github.com/hajimehoshi/ebiten/v2#SetTPS)).
- **Stepped**: advance on external command (RL training, debugger, TraCI-style —
  but see the TraCI trap in competitors.md).

**Overload policies observed** (when a paced tick can't finish in budget):
1. Let realtime slip — sim runs slow (Minecraft: "actions are timed based on tick
   count rather than on real time," [minecraft.wiki](https://minecraft.wiki/w/Tick);
   Factorio auto-slows below 60 UPS, [wiki](https://wiki.factorio.com/Time)).
2. Formalize the slip: EVE Online Time Dilation stretches wall time down to a 10%
   floor, broadcast to clients as a scalar — strongest prior art for "the tick is
   the unit of time; the wall-clock mapping is elastic"
   ([EVE Uni: Server tick](https://wiki.eveuniversity.org/Server_tick),
   [Time dilation](https://wiki.eveuniversity.org/Time_dilation)).
3. Drop time (clamp accumulator) — rendering only, never authoritative sim
   ([Fiedler](https://gafferongames.com/post/fix_your_timestep/)).

Constraint: you cannot run faster than realtime *with* humans in the loop — human
intents are produced in wall time. Mode switching (batch → paced when the first
human controller subscribes) is a driver-layer policy.

## 6. What determinism demands (mechanism checklist)

Each observed in ≥2 surveyed systems:

1. **Fixed tick size and count; no wall clock in sim math**
   ([Fiedler](https://gafferongames.com/post/fix_your_timestep/)).
2. **Fixed iteration order over entities**, including in parallel code —
   deterministic partitioning and reduction order (AoE desyncs from stray
   divergence, [1500 Archers](https://www.gamedeveloper.com/programming/1500-archers-on-a-28-8-network-programming-in-age-of-empires-and-beyond);
   Factorio "deterministic multithreading" pain, [FFF-188](https://factorio.com/blog/post/fff-188);
   MATSim parallel QSim "consistent event ordering within timesteps",
   [Dobler 2010](https://www.strc.ch/2010/Dobler.pdf)).
3. **Seeded, stream-per-concern RNG; never a shared global, never library-internal
   RNG** — SUMO keeps decoupled Mersenne Twister streams (loading, flows, dynamics,
   devices; default seed 23423) so "loading vehicles does not affect simulation
   behavior of earlier vehicles" ([SUMO Randomness](https://sumo.dlr.de/docs/Simulation/Randomness.html));
   Fiedler had to pin ODE's internal constraint-order RNG
   ([lockstep](https://gafferongames.com/post/deterministic_lockstep/)).
   SUMO's threading fix is the gold pattern: replace shared-RNG draws with a pure
   hash of (seed, edge id, vehicle id, step) — **stateless counter-based
   randomness**, ideal for parallel Go workers
   ([sumo#10292](https://github.com/eclipse-sumo/sumo/issues/10292)).
4. **Inputs enter the sim only at tick boundaries, tagged with the tick they apply
   to** (Valve command queue, AoE turn N+2 scheduling, Factorio's server assigning
   each InputAction its execution tick — [FFF-302](https://www.factorio.com/blog/post/fff-302)).
5. **Bit-exact arithmetic** scoped to a declared envelope (see §7).
6. **Continuous state checksums** to detect divergence: AoE compared world/object/
   pathing checksums across machines; Factorio CRCs game state (desync = state CRC
   mismatch, "Networking-, latency or performance problems do not cause desyncs")
   ([Factorio wiki](https://wiki.factorio.com/Desynchronization)). For us: a periodic
   state CRC published with snapshots verifies a JetStream replay reproduced history.

**Scoping note (big simplification for us):** a singularly authoritative engine
needs **replay determinism** (same binary + same input log → same states), not
**lockstep determinism** (independent machines agreeing in parallel). That is
Dawson's easiest category — "same binary, same processor"
([Dawson, Floating-Point Determinism](https://randomascii.wordpress.com/2013/07/16/floating-point-determinism/)).

## 7. Floating-point determinism in Go (the sharp edges)

- **Go spec explicitly documents FMA fusion latitude**: "An implementation may
  combine multiple floating-point operations into a single fused operation ...
  An explicit floating-point type conversion rounds to the precision of the target
  type, preventing fusion." FMA allowed for `r = x*y + z`; disallowed for
  `r = float64(x*y) + z` ([Go spec, FP operators](https://go.dev/ref/spec#Floating_point_operators)).
- **The hazard is asymmetric by architecture**: the compiler pattern-matches FMA on
  arm64/ppc64/s390x/riscv64 but **never on amd64** (FMA not guaranteed present on
  all amd64) — so amd64 vs arm64 builds diverge on any un-fenced `x*y + z`
  ([golang/go#71204](https://github.com/golang/go/issues/71204),
  [#17895](https://github.com/golang/go/issues/17895),
  real-world bite: [#44528](https://github.com/golang/go/issues/44528)).
  Fences: `float64(x*y) + z` at every fusable site (error-prone), or use
  `math.FMA` everywhere to force uniform fusion
  ([#25819](https://github.com/golang/go/issues/25819)).
- **`math` transcendentals are not bit-stable** across architectures or Go versions
  (per-arch assembly + Go fallbacks; even same-arch variants have differed —
  [#1564](https://github.com/golang/go/issues/1564)). Avoid `math.Sin/Cos/Exp/Pow`
  in sim-state math or vendor a pure-Go pinned implementation.
- **Go integer arithmetic is fully deterministic** including signed overflow
  ([Go spec, integer overflow](https://go.dev/ref/spec#Integer_overflow)) — int64
  fixed-point state is a clean escape hatch in Go specifically.
- **Good news**: no x87 per-thread precision problem (amd64 baseline is SSE2,
  [Go wiki MinimumRequirements](https://go.dev/wiki/MinimumRequirements)), no
  fast-math flags, no value-changing reassociation beyond documented fusion.
- **The #1 practical Go determinism bug is not FP**: map iteration order is
  deliberately randomized. Never iterate a map in sim logic; keep sorted slices.
- **Factorio does NOT use fixed-point math** (common claim, verified false):
  doubles + controlled compilation; their only FP desync ever was a compiler
  codegen difference (float promoted to double on one compiler)
  ([forum t=52747](https://forums.factorio.com/viewtopic.php?t=52747)). Map
  *positions* are fixed-point integers ([Data types](https://wiki.factorio.com/Data_types)).
- SUMO documents the same class of hazard in C++: platform libm differences
  (`log`, EIDM model) and Proj geo transforms break cross-platform reproducibility
  ([SUMO Randomness](https://sumo.dlr.de/docs/Simulation/Randomness.html)).

**Practical envelope choice:** if replays are verified on the same GOARCH as
recorded, float64 is fine as-is. Cross-arch replay requires fencing FMA, vendoring
transcendentals, or int64 fixed-point state — decide in ADR-0005 or a follow-up.

## 8. Replay: input log vs state log, and the JetStream fit

- **Input log + deterministic sim** (Fiedler lockstep, AoE, Factorio): tiny —
  "bandwidth is proportional to the size of the input, not the number of objects"
  ([deterministic lockstep](https://gafferongames.com/post/deterministic_lockstep/)).
  Costs: seek requires re-simulating from the start; any nondeterminism or version
  change corrupts everything downstream. Factorio replays have **no skip-ahead and
  no rewind** and break on version/mod changes
  ([Replay system](https://wiki.factorio.com/Replay_system)) — the cautionary tale.
- **State log** (snapshot interpolation): large (Fiedler measured ~11.6 Mbit/s for
  a small scene) but seekable and robust to sim changes
  ([snapshot interpolation](https://gafferongames.com/post/snapshot_interpolation/)).
  CARLA's recorder takes this path: replay is state re-application, with **no
  re-execution determinism claim at all**
  ([CARLA recorder](https://carla.readthedocs.io/en/latest/adv_recorder/)).
- **Industry-standard hybrid**: periodic keyframe snapshots + input/event log
  between them; seek = load nearest snapshot ≤ t, re-simulate forward
  (Fowler: snapshots as the standard event-sourcing optimization,
  [EventSourcing](https://martinfowler.com/eaaDev/EventSourcing.html)).
- **JetStream maps almost 1:1**
  ([JetStream consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)):
  `DeliverPolicy` (`DeliverAll` = replay from genesis, `DeliverByStartSequence` =
  seek to snapshot, `DeliverNew` = live tail, `DeliverLastPerSubject` =
  materialized view); `ReplayPolicy` `ReplayInstant` vs **`ReplayOriginal`**
  ("messages... pushed to the client at the same rate they were originally
  received") = broker-native realtime replay pacing. Caveats: JetStream sequence
  numbers are per-stream, not per-tick — carry tick in headers/payload; retention
  limits bound how far back seek reaches, so snapshot cadence matters.
- **Determinism transfer rule**: the engine must consume intents in *log order*
  (what the engine actually applied, tagged by tick), never goroutine-arrival
  order. Record the *arbitrated* order — Factorio's server-as-arbiter role
  verbatim ([FFF-302](https://www.factorio.com/blog/post/fff-302)).

## 9. Time-in-messages design

- **Tick numbers identify simulation moments; wall clock only at the edges.**
  Every surveyed system stamps inputs with the sim frame they apply to (Fiedler
  lockstep "input with frame identification"; AoE commands execute at turn N+2;
  Factorio InputActions carry their execution tick; Minecraft times actions "based
  on tick count rather than on real time"). Wall-clock appears only in edge
  metadata: CS2 "sub-tick" attaches precise wall-time to inputs so a fixed 64 Hz
  sim can resolve intra-tick ordering
  ([Dexerto explainer](https://www.dexerto.com/counter-strike-2/counter-strike-2-sub-tick-updates-explained-2094004/));
  Source lag compensation reconstructs a historical server time
  ([Valve mirror](https://gist.github.com/CoolOppo/fe0586836de3fb2f90f9)).
- **Input delay windows**: AoE 2 communication turns (~400 ms, RTS-acceptable);
  Fiedler playout buffer ≈ 100 ms; Overwatch ≈ half-RTT + 1 command frame of
  server-side buffer, kept full by **adaptive client time dilation** (client told
  to tick slightly fast/slow to keep the server's input buffer healthy — flow
  control as clock skew)
  ([edgegap Overwatch deep dive](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback)).
- **Late-input policy menu**: (1) stall the sim (pure lockstep — unacceptable);
  (2) apply at next tick (Nakama buffers and batch-applies; Source command queue);
  (3) rewind/lag-compensate (shooter ballistics dispute resolution); (4) drop
  (Nakama overflow). For a traffic sim, **(2) is sufficient and physically
  defensible** — a human steering intent applying 20–50 ms late reads as reaction
  time; rewind exists for instant-hit weapons, which traffic doesn't have.
  Either way, log `(intent, applied_tick)` as decided by the engine.
- **Client interpolation of snapshots**: render remote entities 1–2 snapshot
  intervals in the past (Source `cl_interp` 100 ms at 20 snapshots/s; Fiedler's
  loss-tolerant sizing ≈ 3× send interval). For MapLibre at ~10 snapshots/s over
  NATS: a ~200–300 ms interpolation buffer in the TS client, with velocity in
  snapshots to enable Hermite interpolation
  ([Fiedler snapshot interpolation](https://gafferongames.com/post/snapshot_interpolation/),
  [Gambetta](https://www.gabrielgambetta.com/entity-interpolation.html)).
- **The megapacket warning** (Factorio FFF-302): clients recovering from lag
  replayed 400+ buffered input actions in one burst; the server fanned the burst
  out to 200+ clients and cascaded disconnects. Same failure shape exists for
  reconnecting NATS controllers replaying buffered intents through a fan-out
  broker — bound intent buffers and rate-limit reconnect bursts
  ([FFF-302](https://www.factorio.com/blog/post/fff-302)).
