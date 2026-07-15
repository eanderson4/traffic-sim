# Prior Art Survey: Engine Time Model

> Source: web research | Researched: 2026-07-15
> "Competitors" here = systems whose time-model choices we can steal from or be
> warned by: traffic simulators, game servers, deterministic RTS engines, and
> hybrid co-simulation frameworks.

## Traffic / driving simulators

### SUMO — fixed step, decoupled decision cadence, client-barrier trap
- **Fixed steps, 1 s default**, `--step-length` accepts 0.001–1.0 s; car-following
  models "aren't validated above 1 second"
  ([Basic Definition](https://sumo.dlr.de/docs/Simulation/Basic_Definition.html)).
- `--default.action-step-length` decouples decision frequency from integration
  frequency (auto-switches to ballistic integration) — the designers concluded
  "how often the world integrates" and "how often agents decide" are separate knobs
  ([same](https://sumo.dlr.de/docs/Simulation/Basic_Definition.html)).
- **TraCI is client-driven barrier stepping**: "the simulation does not advance to
  the next step until all clients have called the 'simulationStep' command."
  Multi-client determinism via explicit `SetOrder` total ordering
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
- **The TraCI trap (measured)**: "TraCI communicates over sockets and this
  communication is slow" — position retrieval on a 9,000-vehicle scenario: **90 s
  with TraCI vs 8 s without** (11×); remedies are subscriptions or in-process
  libsumo ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html),
  [FAQ](https://sumo.dlr.de/docs/FAQ.html)).
- Determinism: deterministic by default (fixed seed 23423, decoupled MT19937
  streams); documented breaks: threaded rerouting (fixed 2024 via hash-based RNG,
  [#10292](https://github.com/eclipse-sumo/sumo/issues/10292)), `--random`,
  platform libm (`log`/EIDM), Proj transforms
  ([Randomness](https://sumo.dlr.de/docs/Simulation/Randomness.html)).
- Speed: ~80k–700k vehicle-updates/s on desktop; runtime inversely proportional to
  step length ([FAQ](https://sumo.dlr.de/docs/FAQ.html)).
- **vs traffic-sim (us):** blocking the tick on external clients is the failure we
  must not port to NATS. Async intents + tick-boundary application avoids it, at
  the price of open-system determinism (see synthesis).

### MATSim — "event-based" is a myth; QSim is time-stepped, events are OUTPUT
- QSim `timeStepSize` **defaults to 1.0 s**
  ([QSimConfigGroup doxygen](https://www.matsim.org/doxygen/classorg_1_1matsim_1_1core_1_1config_1_1groups_1_1_q_sim_config_group.html));
  it's a queue model (link traversal + queues, no in-link dynamics).
- Its famous **events are telemetry records** (LinkEnterEvent etc.) consumed by
  EventHandlers — the observation stream, not the scheduler
  ([EventHandler doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1events_1_1handler_1_1_event_handler.html);
  [HERMES post](https://matsim.org/news/2020/introducing-hermes/) explicitly
  contrasts QSim "process each link and agent at each iteration" with event-driven).
  Parallel events processing keeps a per-step barrier
  ([SimStepParallelEventsManagerImpl](https://www.matsim.org/apidocs/core/0.3.0/org/matsim/core/events/parallelEventsHandler/SimStepParallelEventsManagerImpl.html)).
- **HERMES**, the event-driven QSim replacement: ~2.5× faster on a 1M-agent Swiss
  scenario (3:33 vs 8:45 min/iteration), much of it from data-layout work — and it
  **dropped dynamic routing, within-day replanning, and traffic signals** because
  event-driven scheduling made reactive features harder
  ([Introducing HERMES](https://matsim.org/news/2020/introducing-hermes/)).
- Distributed QSim: spatial partitions, barrier sync per time step
  ([Dobler 2010](https://www.strc.ch/2010/Dobler.pdf);
  [Laudan et al. 2024](https://svn.vsp.tu-berlin.de/repos/public-svn/publications/vspwp/2024/24-10/LaudanEtAl2024DistributeQSim_accepted.pdf)).
- **vs us:** MATSim IS our proposed shape — tick-driven authoritative mobsim
  emitting an event stream as the public interface. Events-as-observations over
  NATS ≠ event-driven time advance; MATSim proves they're orthogonal. HERMES is
  the strongest single data point *against* pure DES for our use case (it costs
  exactly the reactive features we need: signals, human input).

### Aimsun Next — production hybrid: DES meso + fixed-tick micro
- Meso: true DES — "time is moved to the next event where the event is a vehicle
  entering or leaving a section or node"; event list sorted by (time, priority)
  ([meso docs](https://docs.aimsun.com/next/24.0.0/UsersManual/MesoDiscreteSimulation.html)).
- Micro: fixed cycle, "Simulation Step can range from 0.1 to 1.5 seconds"
  ([vehicle movement](https://docs.aimsun.com/next/22.0.1/UsersManual/MicrosimulationModellingVehicleMovement.html)),
  and itself documented as "event scheduling + activity scanning" hybrid
  (scheduled events fire at tick boundaries, then the vehicle sweep)
  ([micro process](https://docs.aimsun.com/next/22.0.1/UsersManual/MicrosimulationProcess.html)).
- Hybrid sync: "a general clock module is used to generate synchronization events
  for the mesoscopic model"; meso→micro transfers retry next tick when no space;
  micro→meso uses dummy vehicles placed via a triangular fundamental diagram
  ([hybrid manual](https://docs.aimsun.com/next/24.0.2/UsersManual/HybridSimulator.html),
  [tech note](https://www.aimsun.com/technical-notes/hybrid-simulation/)).
- **vs us:** proof the hybrid works in production — and note the FD-based boundary
  glue connects to [[domain-macroscopic-flow-models]]. If we ever bolt an LTM
  preview/meso layer onto the micro engine, this is the integration pattern.

### PTV Vissim — sub-second fixed steps, seed-sweep methodology
- Time-step based; "simulation resolution" = time steps per simulated second.
  WisDOT documents default 10 (100 ms), typical 5–10; ODOT mandates exactly 10
  ([WisDOT defaults sheet](https://wisconsindot.gov/dtsdManuals/traffic-ops/manuals-and-standards/teops/16-20att6.3.pdf),
  [ODOT protocol](https://www.oregon.gov/odot/Planning/Documents/APMv2_Add15A.pdf)).
  (The software's full settable range is not documented in these public sources
  ⚠ — check the PTV manual before citing one.)
- Same seed + same inputs → identical results; standard DOT practice is ~10 runs
  with different seeds, averaged
  ([microsimulation.pub](https://www.microsimulation.pub/articles/00219),
  [MassDOT guidance](https://www.mass.gov/doc/massdot-roundabout-vissim-microsimulation-guidance/download)).
- **vs us:** institutionalized (scenario, seed) as the run key for batch
  comparison — our scenario-comparison workflow should adopt seed sweeps from
  day one. Multicore determinism policy not publicly documented (⚠ open).

### CARLA — why interactive + reproducible forces synchronous + fixed
- Two orthogonal switches: fixed vs variable `fixed_delta_seconds`; synchronous
  (server waits for client tick) vs asynchronous. "In synchronous mode, always use
  a fixed time-step... Physics will not be reliable" otherwise. Physics
  substepping: default ≤10 substeps of ≤10 ms
  ([synchrony & time-step docs](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/)).
- Traffic Manager determinism "in synchronous mode only... In asynchronous mode...
  determinism cannot be achieved"; seed must be re-set after world reload
  ([TM docs](https://carla.readthedocs.io/en/latest/adv_traffic_manager/)).
- Recorder/replay = server-side **state log re-application**, no re-execution
  determinism claim ([recorder docs](https://carla.readthedocs.io/en/latest/adv_recorder/)).
- **vs us:** (1) lockstep with slow clients kills faster-than-realtime — same
  lesson as TraCI; (2) state-log replay is a legitimate fallback design that works
  even without bit-exact re-execution.

## Game / realtime servers

### Valve Source — the canonical authoritative tick server
- 66.67 Hz default tick (15 ms); per tick: process queued user commands, physics
  step, game rules, state update. Client input rate (`cl_cmdrate` ~30/s) and
  snapshot rate (`cl_updaterate` default 20/s) are decoupled from tick rate.
  Entity interpolation ~100 ms (`cl_interp`); lag compensation rewinds targets
  ([Source Multiplayer Networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking);
  ⚠ page 403s to fetchers — quotes verified via
  [mirror gist](https://gist.github.com/CoolOppo/fe0586836de3fb2f90f9); eyeball the
  live page before citing as primary).
- **vs us:** the sim-tick ≠ input-rate ≠ snapshot-rate decoupling is directly our
  NATS design; lag compensation is a shooter need we get to skip (late intents
  read as reaction time).

### Overwatch — fixed 16 ms command frames + adaptive client time dilation
- 62.5 Hz ("63 tick"); "the fixed simulation rate is non-negotiable for
  determinism." Server-side per-client input buffer kept healthy by telling the
  client to tick slightly faster/slower (flow control as clock skew); sliding
  window of unacked inputs heals packet loss; predict-everything + rollback on
  misprediction ([GDC talk](https://www.gdcvault.com/play/1024001/-Overwatch-Gameplay-Architecture-and),
  [edgegap write-up](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback)).
- **vs us:** the input-buffer-health feedback loop is the mature answer to "human
  intents over a jittery network into a fixed tick."

### CS2 sub-tick — the modern hybrid in shooters
- Fixed 64 Hz simulation, but each input carries a precise wall-time stamp so the
  server resolves *when within the tick* it happened
  ([Dexerto explainer](https://www.dexerto.com/counter-strike-2/counter-strike-2-sub-tick-updates-explained-2094004/)).
- **vs us:** if intra-tick ordering of intents ever matters (two cars claim the
  same gap), timestamped intents resolved within a fixed tick is the pattern.

### Factorio — deterministic lockstep, input-log replay, desync CRCs
- All clients simulate; server arbitrates InputActions onto ticks; 60 UPS; desync =
  state CRC mismatch; "Networking-, latency or performance problems do not cause
  desyncs" ([FFF-302](https://www.factorio.com/blog/post/fff-302),
  [Desynchronization](https://wiki.factorio.com/Desynchronization)).
- Replay = input log; **no skip-ahead, no rewind**, breaks on version/mod change
  ([Replay system](https://wiki.factorio.com/Replay_system)).
- Uses **doubles, not fixed-point math** (positions are fixed-point integers);
  determinism via controlled compilation
  ([forum t=52747](https://forums.factorio.com/viewtopic.php?t=52747),
  [Data types](https://wiki.factorio.com/Data_types)).
- **vs us:** we need only replay determinism (single authority), not lockstep
  determinism — but their arbiter role, CRC discipline, and replay limitations
  transfer directly.

### Age of Empires — "1500 Archers on a 28.8" (lockstep origins)
- Commands scheduled for turn N+2; turn length adapts to slowest machine + ping;
  desyncs from *any* divergence ("A deer slightly out of alignment... minutes
  later a villager would path a tiny bit off"); checksums everywhere
  ([Game Developer article](https://www.gamedeveloper.com/programming/1500-archers-on-a-28-8-network-programming-in-age-of-empires-and-beyond)).

### EVE Online — 1 Hz tick + Time Dilation
- 1 Hz server tick; under overload, **Time Dilation** stretches wall time (10%
  floor), broadcast to clients as a scalar
  ([Server tick](https://wiki.eveuniversity.org/Server_tick),
  [Time dilation](https://wiki.eveuniversity.org/Time_dilation)).
- **vs us:** strongest precedent that tick↔wall-clock ratio is a published,
  dynamic quantity — our realtime pacing and k× batch modes are the same scalar.

### Nakama (Go) — authoritative match loop, closest Go prior art
- Configurable tick rate; "Client messages are buffered by the server in the order
  received and, when the next match loop runs, are handed off as a batch"; one
  node/goroutine owns match state; overflow messages may be dropped; guidance:
  lowest acceptable tick rate (tick rate trades against matches-per-core)
  ([Heroic Labs docs](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/)).
- **vs us:** buffer-between-ticks → batch-apply at tick boundary is exactly a
  NATS-subscription → intent-queue → engine-tick pipeline; single-writer goroutine
  owning world state is the Go idiom for it.

### Others (calibration points)
- Minecraft: 20 TPS fixed; overload → realtime slips ([minecraft.wiki](https://minecraft.wiki/w/Tick)).
- Ebitengine (Go): fixed logic TPS (default 60) decoupled from render FPS
  ([SetTPS](https://pkg.go.dev/github.com/hajimehoshi/ebiten/v2#SetTPS)).
- Gymnasium: `step()` on demand, no wall clock in semantics — the unpaced/batch
  pole ([Env API](https://gymnasium.farama.org/api/env/)).
- NATS + game servers: thin prior art; EventStack's GamingAPI uses JetStream as
  event log/async command delivery but doesn't address tick loops
  ([eventstack.tech](https://www.eventstack.tech/posts/nats-and-game-servers)).
  **Genuinely under-documented territory — our write-up is near the frontier.**

## Hybrid co-simulation framework

### mosaik 3.0 — reference design for "tick physics + event controllers"
- Redesigned scheduler "to efficiently combine time-stepped and discrete event
  simulation": typed components (time-based / event-based / hybrid), integer time
  base with `time_resolution`, `max_advance` lookahead grants, superdense
  same-time iteration for controller convergence (cap 100), and async
  `set_event()` for human-in-the-loop input injection
  ([arXiv:2410.16937](https://arxiv.org/abs/2410.16937)).

## Positioning Summary

| System | Time model | Tick/step | External clients | Determinism | Replay |
|---|---|---|---|---|---|
| SUMO | fixed step | 1 s default (≥1 ms) | TraCI barrier (slow) | default-on, seeded streams | re-run with same inputs |
| MATSim QSim | fixed step | 1 s | — (batch) | single-thread det.; parallel via barriers | re-run |
| Aimsun | DES meso + fixed micro | events / 0.1–1.5 s | — | seeded, no strong public guarantee | re-run |
| Vissim | fixed step | ~100 ms | COM/API | same-seed identical | re-run |
| CARLA | fixed or variable × sync/async | e.g. 50 ms + substeps | sync mode blocks on client | sync+fixed+seed only | **state log** re-application |
| Source/Overwatch/CS2 | fixed tick | 15–16 ms | input queues at tick boundary | server-authoritative | demo files |
| Factorio/AoE | fixed tick lockstep | 16.7 ms / ~200 ms turns | inputs scheduled onto ticks | bit-exact, CRC-checked | **input log** |
| EVE | fixed tick | 1 s (dilatable) | — | — | — |
| Nakama | fixed tick | configurable | batch-applied buffered msgs | app's problem | — |
| mosaik 3.0 | hybrid scheduler | integer time units | typed components + lookahead | — | — |
| **traffic-sim (us)** | **hybrid: fixed tick core + event edges (leading hypothesis, ADR-0005)** | TBD (see synthesis) | async NATS intents, tick-boundary apply | replay determinism (same binary/arch) + state CRC | JetStream: snapshot keyframes + arbitrated intent log |
