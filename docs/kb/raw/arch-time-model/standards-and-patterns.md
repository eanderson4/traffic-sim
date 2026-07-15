# Standards & Patterns: Engine Time Model

> Source: academic research + pattern identification | Researched: 2026-07-15

## Formalisms

### Discrete-Event Simulation (classic)
Future event list as priority queue; clock jumps to next event; three world views
(event scheduling / activity scanning / process interaction) and the three-phase
combination (Tocher/Pidd)
([Wikipedia DES](https://en.wikipedia.org/wiki/Discrete-event_simulation),
[Banks et al. ch.3](https://santoshhiremath.weebly.com/uploads/6/7/0/5/67052617/unit2.pdf),
[Vangheluwe notes](https://www.cs.mcgill.ca/~hv/classes/MS/discreteEvent.pdf),
[Pidd](https://journals.sagepub.com/doi/10.1177/003754979306000603)).
Fixed-increment advance is the textbook choice when state changes depend on many
simultaneous, continuously interacting conditions — our car-following case.

### Combined continuous–discrete simulation (lineage)
GASP IV (Pritsker & Hurst 1973) popularized combining the frameworks: continuous
variables integrated between discrete events; *state events* (threshold crossings)
schedule discrete events
([Simulation journal](https://journals.sagepub.com/doi/10.1177/003754977302100302),
[history](https://www.simio.com/case-studies/history-of-simulation-modeling)).
SIMAN/Arena inherit it
([SIMAN](https://www.researchgate.net/publication/41448270_The_simulation_language_SIMAN_on_microcomputers_and_mainframes)).

### DEVS and DEV&DESS (Zeigler)
DEV&DESS is the formalism for combined discrete-event + differential-equation
models; plain DEVS can embed it — hybrid models are expressible "within a purely
discrete event computational framework"
([DEVS](https://en.wikipedia.org/wiki/DEVS),
[Embedding DEV&DESS in DEVS](https://www.researchgate.net/publication/228848250_Embedding_DEVDESS_in_DEVS)).
**Theory takeaway: a fixed-tick loop is a special case of DES where one recurring
event advances the world by Δt. The question is which layer owns the clock.**

### HLA time management (IEEE 1516) — the distributed-time standard
Designed to federate time-stepped, event-driven, optimistic, and wallclock-paced
simulators in one execution
([Fujimoto & Weatherly](https://sites.cc.gatech.edu/computing/pads/PAPERS/HLA_Time_Mgmt_DIS.pdf)):
- **Receive Order (RO)** = messages delivered as they arrive — low latency, no
  causality guarantee. **Timestamp Order (TSO)** = RTI delivers in timestamp order
  via conservative synchronization.
- **Lookahead**: a federate with lookahead L promises no event earlier than C+L;
  conservative performance depends on it
  ([lookahead explainer](https://schollii2.wordpress.com/2008/01/16/concept-of-lookahead-in-hla/)).
- Conservative (Chandy–Misra–Bryant null messages) vs optimistic (Jefferson Time
  Warp rollback) sync ([same paper](https://sites.cc.gatech.edu/computing/pads/PAPERS/HLA_Time_Mgmt_DIS.pdf)).

**Mapping to NATS:** core NATS delivery is RO. Any causal guarantee is built on
top: tick-stamped intents + engine-side deterministic ordering = a poor-man's TSO
with the tick as implicit lookahead. A fixed-tick engine has natural lookahead =
1 tick — exactly why time-stepped federates are HLA's easy case and why MATSim
distributes QSim with simple per-step conservative sync. Time Warp rollback is
overkill for us.

## Design Patterns Identified

### Tick-authoritative core with scheduled-event list (three-phase inside the loop)
Aimsun micro's documented shape ("event scheduling + activity scanning"): process
scheduled events bound to this tick (signal phase changes, spawns), then sweep all
vehicles. Recommended internal shape for our engine.

### Events-as-observations (MATSim), not events-as-scheduler
The public interface is a stream of tick-stamped event records emitted by a
time-stepped core. Analysis/scoring/viz hang off the stream. Orthogonal to how
time advances. Maps directly to NATS subjects.

### Single-writer state ownership
Overwatch/Factorio/Nakama converge on one owner of sim state with message passing
at tick boundaries. Go idiom: one goroutine owns the world; NATS subscription
callbacks only enqueue intents
([Nakama](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/)).

### Engine-as-arbiter + arbitrated input log (Factorio)
Consume intents in arrival/stream order, assign each an execution tick, durably log
`(intent, applied_tick)`, apply at tick boundaries. Replay = re-consume the log.
Determinism of the *closed system* (engine + recorded inputs), not the open one
([FFF-302](https://www.factorio.com/blog/post/fff-302)).

### Keyframe snapshots + event log (event sourcing)
Fowler: rebuild state by replaying events; snapshots are the standard optimization
([Event Sourcing](https://martinfowler.com/eaaDev/EventSourcing.html)). JetStream
`DeliverByStartSequence` + periodic snapshot messages = seek/scrub; Factorio's
no-scrub replay is the anti-pattern this avoids.

### Counter-based (hash) RNG for parallel determinism
SUMO's fix for threaded nondeterminism: replace shared-RNG draws with a pure hash
of (seed, edge, vehicle, step) ([sumo#10292](https://github.com/eclipse-sumo/sumo/issues/10292)).
Stateless, order-independent, parallelizable — the right pattern for Go workers.

### Pacing as a driver policy (strategy pattern around Tick)
Unpaced (Gymnasium `step()`), paced (Nakama/Ebiten sleep-until-deadline), stepped
(debugger). Overload → published time-dilation scalar (EVE). JetStream mirrors the
split broker-side: `ReplayInstant` vs `ReplayOriginal`
([JetStream consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)).

## Anti-patterns (documented failures)

1. **Blocking the tick on external clients** — SUMO TraCI barrier (11× slowdown
   measured), CARLA synchronous mode tied to slowest client. Kills
   faster-than-realtime.
2. **Variable timestep in dynamics** — framerate-dependent physics
   ([Fiedler](https://gafferongames.com/post/fix_your_timestep/)).
3. **Wall clock inside sim math** — breaks replay; tick count is the clock
   (Minecraft, Factorio, all lockstep engines).
4. **Float time base** — accumulating float seconds drifts; use integer ticks
   (mosaik `time_resolution`; SUMO uses ms internally ⚠ verify).
5. **Shared/global RNG across concerns or threads** — SUMO's bug class; also
   library-internal RNG (Fiedler's ODE seed pin).
6. **Map iteration in sim logic (Go-specific)** — deliberately randomized order.
7. **Unbounded reconnect intent bursts through a fan-out broker** — Factorio
   megapacket cascade ([FFF-302](https://www.factorio.com/blog/post/fff-302)).
8. **Pure DES for dense continuous dynamics** — FEL degenerates into an O(log n)
   tick (§theory); HERMES had to drop signals/replanning to stay event-driven.

## Empirical anchors

- Tick rates in the wild: shooters 60–128 Hz; Minecraft 20 Hz; EVE 1 Hz; traffic
  micro 0.1–1.5 s (Aimsun), 100 ms typical (Vissim), 1 s default (SUMO, MATSim);
  CARLA 50 ms world + ≤10 ms physics substeps.
- Car-following step: SUMO validates models only up to 1 s steps; Vissim DOT
  practice is 10 steps/s. Our lane-level fidelity likely wants **100 ms world
  tick** territory (final number belongs in ADR-0005 with
  [[domain-traffic-flow-models]] stability input).
- Client interpolation buffers: ~100 ms (Source), ~3× snapshot interval (Fiedler).
- Input delay: ~100 ms playout (Fiedler), 2 turns ≈ 400 ms (AoE RTS), half-RTT+1
  frame (Overwatch).

## Open Questions

- Vissim multicore determinism policy (official docs paywalled).
- Aimsun's formal determinism guarantee — not stated in public manual pages.
- SUMO's internal integer time representation — implied ms, not explicitly
  documented in current docs (⚠ verify before citing).
- MATSim parallel QSim bit-determinism in current releases.
- NATS core round-trip latency vs a 50–100 ms tick budget on localhost/compose —
  needs our own benchmark.
- JetStream publish-ack throughput when persisting per-tick intent batches at high
  faster-than-realtime multipliers — no prior art found.
- Go GC pause jitter inside fixed-tick loops at scale — folklore only, benchmark.

## Master source list

Fiedler: [Fix Your Timestep](https://gafferongames.com/post/fix_your_timestep/) ·
[Deterministic Lockstep](https://gafferongames.com/post/deterministic_lockstep/) ·
[Snapshot Interpolation](https://gafferongames.com/post/snapshot_interpolation/) —
[Valve Source networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)
([mirror](https://gist.github.com/CoolOppo/fe0586836de3fb2f90f9)) —
[Overwatch GDC](https://www.gdcvault.com/play/1024001/-Overwatch-Gameplay-Architecture-and)
([edgegap](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback)) —
[1500 Archers](https://www.gamedeveloper.com/programming/1500-archers-on-a-28-8-network-programming-in-age-of-empires-and-beyond) —
Factorio [FFF-302](https://www.factorio.com/blog/post/fff-302) /
[FFF-188](https://factorio.com/blog/post/fff-188) /
[Desynchronization](https://wiki.factorio.com/Desynchronization) /
[Replay system](https://wiki.factorio.com/Replay_system) /
[FP forum](https://forums.factorio.com/viewtopic.php?t=52747) —
[Dawson FP determinism](https://randomascii.wordpress.com/2013/07/16/floating-point-determinism/) —
Go: [spec FP operators](https://go.dev/ref/spec#Floating_point_operators) ·
[#17895](https://github.com/golang/go/issues/17895) ·
[#71204](https://github.com/golang/go/issues/71204) ·
[#25819](https://github.com/golang/go/issues/25819) ·
[#1564](https://github.com/golang/go/issues/1564) —
[Gymnasium Env](https://gymnasium.farama.org/api/env/) —
[Nakama authoritative](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/) —
[Ebiten SetTPS](https://pkg.go.dev/github.com/hajimehoshi/ebiten/v2#SetTPS) —
[JetStream consumers](https://docs.nats.io/nats-concepts/jetstream/consumers) —
[Fowler Event Sourcing](https://martinfowler.com/eaaDev/EventSourcing.html) —
EVE [Server tick](https://wiki.eveuniversity.org/Server_tick) /
[Time dilation](https://wiki.eveuniversity.org/Time_dilation) —
[Minecraft Tick](https://minecraft.wiki/w/Tick) —
[CS2 sub-tick](https://www.dexerto.com/counter-strike-2/counter-strike-2-sub-tick-updates-explained-2094004/) —
[Gambetta](https://www.gabrielgambetta.com/entity-interpolation.html) —
SUMO [Basic Definition](https://sumo.dlr.de/docs/Simulation/Basic_Definition.html) /
[Randomness](https://sumo.dlr.de/docs/Simulation/Randomness.html) /
[TraCI](https://sumo.dlr.de/docs/TraCI/index.html) /
[FAQ](https://sumo.dlr.de/docs/FAQ.html) /
[#10292](https://github.com/eclipse-sumo/sumo/issues/10292) —
MATSim [QSimConfigGroup](https://www.matsim.org/doxygen/classorg_1_1matsim_1_1core_1_1config_1_1groups_1_1_q_sim_config_group.html) /
[HERMES](https://matsim.org/news/2020/introducing-hermes/) /
[Dobler 2010](https://www.strc.ch/2010/Dobler.pdf) /
[book (open access)](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf) —
Aimsun [meso](https://docs.aimsun.com/next/24.0.0/UsersManual/MesoDiscreteSimulation.html) /
[hybrid](https://docs.aimsun.com/next/24.0.2/UsersManual/HybridSimulator.html) /
[micro process](https://docs.aimsun.com/next/22.0.1/UsersManual/MicrosimulationProcess.html) —
[Burghout thesis](https://www.kth.se/polopoly_fs/1.742065.1600688570!/hybrid%20mesoscopic.pdf) —
Vissim [WisDOT](https://wisconsindot.gov/dtsdManuals/traffic-ops/manuals-and-standards/teops/16-20att6.3.pdf) /
[microsimulation.pub](https://www.microsimulation.pub/articles/00219) —
CARLA [synchrony](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/) /
[TM determinism](https://carla.readthedocs.io/en/latest/adv_traffic_manager/) /
[recorder](https://carla.readthedocs.io/en/latest/adv_recorder/) —
[mosaik 3.0](https://arxiv.org/abs/2410.16937) —
[HLA time mgmt](https://sites.cc.gatech.edu/computing/pads/PAPERS/HLA_Time_Mgmt_DIS.pdf) —
[DES](https://en.wikipedia.org/wiki/Discrete-event_simulation) /
[DEVS](https://en.wikipedia.org/wiki/DEVS) /
[DEV&DESS embedding](https://www.researchgate.net/publication/228848250_Embedding_DEVDESS_in_DEVS) —
[simultaneous-event ordering](https://arxiv.org/pdf/2105.00069)
