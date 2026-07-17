# Mechanics: Simulator Landscape

> Source: web research (greenfield — no engine code exists; this file collects the
> *mechanisms* the incumbent simulators are built from: module decomposition, control
> APIs, config/output pipelines, replay machinery — to be mined for patterns and
> re-audited once our engine exists) | Researched: 2026-07-16 | Git HEAD: ae75fba
> Time-model internals (tick loops, sync modes, determinism) are deliberately excluded
> — see [[arch-time-model]] and ADR-0005, which already decided them.

## 1. The tool-suite decomposition (SUMO's included applications)

SUMO is not one program but a package of single-purpose executables glued by shared
XML formats ([SUMO at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)):

- `sumo` (headless sim), `sumo-gui` (OpenGL GUI), `netconvert` (network
  importer/generator: OSM, VISUM, Vissim, Shapefiles, MATSim, OpenDRIVE, XML),
  `netedit` (graphical network editor), `netgenerate` (abstract grids/spiders),
  `duarouter` (fastest-path routing + Dynamic User Assignment), `jtrrouter` (routes
  from junction turning ratios), `dfrouter` (routes from induction-loop counts),
  `marouter` (macroscopic assignment), `od2trips` (O/D matrices → single trips),
  `polyconvert`, `activitygen` (population → demand), `emissionsMap`,
  `emissionsDrivingCycle` — plus dozens of Python "tools" scripts.
- The designers state the rationale explicitly: the software "was split into several
  parts… each is smaller than a monolithic application that does everything,"
  allowing "easier extension" and "faster data structures, each adjusted to the
  current purpose" — with the admitted cost of being "a little bit uncomfortable"
  to use. Notably, Dynamic User Assignment runs as an *external* application
  (`duarouter`), not inside the simulation loop
  ([same page](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)).
- Interoperability rule: "High interoperability through usage of XML-data only."
  The formats are the API between the tools.
- MOSS (2024) reinvented the same shape as separate repos: `mosstool` (map building,
  demand generation, SUMO-format conversion), `routing` (a standalone gRPC A*
  service), simulator core, and web-UI packages
  ([moss GitHub](https://github.com/tsinghua-fib-lab/moss)).

**Mechanism to steal:** engine + network-import + demand-gen + metrics as separate
small binaries/services communicating through versioned artifacts — which is
already our NATS/microservice shape (VISION principle 3). The pain SUMO users feel
is not the split but the *file-based* glue; our glue is the message bus.

## 2. The runtime control API: socket → subscription → in-process

The evolution of SUMO's TraCI is the best-documented lesson in
controller-interface design:

1. **Socket stepping protocol.** TraCI "uses a TCP based client/server
   architecture"; the sim does not advance until every client calls
   `simulationStep` ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
   Multi-client ordering is explicit (`SetOrder`).
2. **The measured wall.** "TraCI communicates over sockets and this communication
   is slow": retrieving positions on a ~9,000-vehicle scenario took **90 s per run
   via TraCI vs 8 s without** (11×); remedies offered are *subscriptions* (server
   pushes a declared set each step instead of client polling) and libsumo
   ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html),
   [SUMO FAQ](https://sumo.dlr.de/docs/FAQ.html)).
3. **In-process escape hatch.** libsumo provides "the same method signatures as in
   the client libraries but avoids the overhead of socket communication," shipped
   as a C++ library with SWIG-generated Java/Python/C# bindings
   ([Libsumo docs](https://sumo.dlr.de/docs/Libsumo.html)). It came with real
   limitations: no sumo-gui on Windows ("highly experimental" elsewhere), no
   multi-client, subscriptions-with-arguments unsupported, stricter typing
   ([same](https://sumo.dlr.de/docs/Libsumo.html)). A third library, libtraci
   (pure client-side C++, API-compatible) was later added to sidestep even those
   ([same](https://sumo.dlr.de/docs/Libsumo.html)).
4. **The protocol became a standard.** Veins implements *its own* TraCI client to
   drive SUMO from OMNeT++ ([sumo#13548](https://github.com/eclipse-sumo/sumo/issues/13548));
   CARLA and MOSAIC both couple to SUMO through TraCI
   ([CARLA core concepts](https://carla.readthedocs.io/en/latest/core_concepts/)).

**Lesson for us:** request/response per-vehicle polling over a socket is the
failure mode; server-push subscriptions are the fix *within* a stepping protocol;
in-process calls are the fix for training loops. Our design should be
subscription/push from day one (NATS subjects ≈ TraCI subscription contexts), and
any future in-process controller path must speak the *same* intent/state contract
— libsumo exists precisely because TraCI's contract was tied to one transport.

## 3. The batch iteration loop as application shape (MATSim)

MATSim's architecture is a co-evolutionary algorithm wrapped around a mobsim
([matsim-libs README](https://github.com/matsim-org/matsim-libs),
[book, open access](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf)):

- "MATSim provides a toolbox… Modules can be replaced by own implementations…
  demand-modeling, agent-based mobility-simulation (traffic flow simulation),
  re-planning, a controller to iteratively run simulations as well as methods to
  analyze the output" ([same](https://github.com/matsim-org/matsim-libs)).
  Each iteration: mobsim executes all agents' plans → scoring → a fraction of
  agents replan → repeat until relaxed (~dynamic user equilibrium).
- The public data interface is the **events stream** (LinkEnterEvent etc.) consumed
  by pluggable EventHandlers — analysis hangs off the stream, not the engine
  ([EventHandler doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1events_1_1handler_1_1_event_handler.html);
  contrast with scheduler in [[arch-time-model]]).
- **No runtime control API exists.** MATSim is batch-only: agents follow plans
  decided by replanning, humans cannot join a run. BEAM inherited this shape
  wholesale: "built around MATSim… with extensive modifications to allow for
  multithreaded within-day simulation," reusing MATSim's "between-iteration
  replanning… to approximate dynamic user equilibrium"
  ([BEAM README](https://github.com/LBNL-UCB-STI/beam)).
- Scale precedent: the HERMES rewrite benchmarked 1M agents for all of Switzerland
  at 3:33 min/iteration vs QSim's 8:45
  ([Introducing HERMES](https://matsim.org/news/2020/introducing-hermes/)).

**Lesson for us:** the events-as-output pattern is exactly NATS-shaped and is the
right metrics architecture. But the iteration loop answers a different question
(long-run equilibrium demand) than ours (per-tick lane-level behavior with live
controllers) — adopting it in v1 would swallow a second research program
(SimMobility's trajectory, §10, warns where that leads).

## 4. Co-simulation federation (MOSAIC, Veins, CARLA bridges)

When one simulator isn't enough, the field reaches for federation:

- **Eclipse MOSAIC** couples SUMO or PHABMACS (traffic), ns-3/OMNeT++/SNS/Cell
  (communication), an Application simulator, and more through a central **Runtime
  Infrastructure (RTI)**: each simulator is wrapped in a "Federate" linked to an
  "Ambassador" with "HLA inspired interfaces"; the RTI owns data exchange and *time
  management* ([VANET review, Springer](https://link.springer.com/content/pdf/10.1186/s13173-021-00113-x.pdf),
  [EURECOM thesis](https://www.eurecom.fr/publication/7713/download/comsys-publi-7713.pdf),
  [eclipse.dev/mosaic](https://eclipse.dev/mosaic/)).
- **Veins** couples SUMO to OMNeT++ bidirectionally over TraCI for V2X research
  ([sumo#13548](https://github.com/eclipse-sumo/sumo/issues/13548)).
- **CARLA ships two co-simulation bridges** — "PTV-Vissim co-simulation" and "SUMO
  co-simulation," both synchronous — precisely because its own Traffic Manager is a
  visual-grade traffic model, not an engineering one
  ([CARLA core concepts](https://carla.readthedocs.io/en/latest/core_concepts/)).

**Lesson for us:** federation is what you build when the monolith can't be opened.
The RTI's real job (time synchronization across simulators) is a *hard* problem —
HLA lookahead machinery ([[arch-time-model]] standards file). Our NATS backbone
with tick-numbered messages and 1-tick lookahead gets the decoupling without an
RTI — but only because one engine owns the world (ADR-0005, ADR-0002).

## 5. Scenario/config as a directory of typed artifacts

The universal pattern is "a scenario = a small set of typed files referenced by a
top-level config," with format choices all over the map (deep dive deferred to
[[concept-scenario-format]]):

- **SUMO**: `.net.xml` (network) + `.rou.xml` (routes/demand) + `.add.xml`
  (detectors, signals) + `.sumocfg` referencing them; everything XML, "XML-data
  only" as a design rule ([SUMO at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)).
- **MATSim**: `config.xml` (modules per subsystem) + `network.xml` + `plans.xml`
  (+ transit vehicles, facilities…); the
  [book (open access)](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf)
  documents the config as per-module sections — a surface that has grown for 15+
  years as every contrib added its own module (see anti-pattern in
  standards-and-patterns.md).
- **CityFlow**: JSON triple — `config.json` pointing at `roadnet.json` +
  `flow.json`; Docker image as the distribution unit
  ([install docs](https://github.com/cityflow-project/CityFlow/blob/master/docs/source/install.rst)).
- **MOSS**: protobuf (`map.pb`, `person.pb` via `cityproto`, which ships
  C/C++/Go/Python/TS bindings) + a YAML config; outputs AVRO or PostgreSQL
  ([moss GitHub](https://github.com/tsinghua-fib-lab/moss)).
- **CARLA**: OpenDRIVE 1.4 `.xodr` maps + UE4 cooked assets; scripted scenarios
  via the Python API, with partial ASAM OpenSCENARIO support through
  ScenarioRunner ([core concepts](https://carla.readthedocs.io/en/latest/core_concepts/),
  [ScenarioRunner OpenSCENARIO support](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).

**Lesson for us:** text, diffable, one-directory-per-scenario is the norm among
open tools (MOSS's protobuf is the outlier, chosen for GPU ingest speed); the
top-config-references-artifacts shape directly supports VISION's "scenarios are
diffable so upgrade variants are first-class."

## 6. Metrics/output pipelines: detector files vs event streams vs databases

- **SUMO** writes everything to files (or a socket), all off by default: FCD
  (floating-car data), tripinfo, vehroutes, lanechange-with-motivation, SSM,
  collision output; simulated **detectors** E1 (inductive loops), E2 (lane-area),
  E3 (multi-entry-exit); edge/lane traffic, emissions, noise, queue output; network
  summary/statistic output. XML by default; `.csv`/`.parquet` selected *by file
  extension*; `.gz` likewise. `--output-prefix` separates repeated runs — the
  seed-sweep mechanism ([Output docs](https://sumo.dlr.de/docs/Simulation/Output/index.html)).
  The step-log prints real-time factor and UPS (vehicle updates/s) — built-in
  performance telemetry ([same](https://sumo.dlr.de/docs/Simulation/Output/index.html)).
- **MATSim** emits one gzipped events stream + final plans; all analysis is
  post-hoc over events ([book](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf)).
- **MOSS** records to AVRO files or PostgreSQL via a `DBRecorder`, with a web UI
  stack (`moss-webui-*`) purely for replay/visualization
  ([moss GitHub](https://github.com/tsinghua-fib-lab/moss)).
- **Proprietary practice** is replication-oriented: run the scenario N times with
  different seeds and average — DOT guidance is ~10 seed runs
  ([microsimulation.pub](https://www.microsimulation.pub/articles/00219)).

**Lesson for us:** the vocabulary to copy is SUMO's detector catalog (E1/E2/E3,
tripinfo, queue) — these are the artifacts traffic engineers already read (deep
dive [[domain-congestion-metrics]]). The *pipeline* to copy is MATSim's: engine
emits an immutable tick-stamped event stream (our JetStream), detectors/aggregators
are downstream consumers, not engine internals.

## 7. Replay & checkpoint mechanisms

- **Re-run is the default everywhere open**: SUMO is "deterministic by default"
  (fixed seed, decoupled RNG streams) so replay = re-execute the scenario
  ([SUMO at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html),
  [Randomness docs](https://sumo.dlr.de/docs/Simulation/Randomness.html)).
- **CARLA's recorder** is a server-side state log "to reenact a simulation with
  exact precision" — re-application of recorded states, not re-execution
  ([core concepts](https://carla.readthedocs.io/en/latest/core_concepts/),
  [recorder docs](https://carla.readthedocs.io/en/latest/adv_recorder/)).
- **MOSS v1.1 added a checkpoint mechanism** "to save the simulation state to CPU
  memory and restore it" — notable because it is (a) recent, (b) motivated by
  long-running GPU batch, (c) still not a seekable replay
  ([moss GitHub](https://github.com/tsinghua-fib-lab/moss)).
- **CityFlow** writes replay files for its web visualizer; correctness replay is
  re-run ([CityFlow GitHub](https://github.com/cityflow-project/CityFlow)).

**Lesson for us:** nobody in the field has ADR-0005's keyframes + arbitrated
intent log. Checkpoints exist (MOSS) and state logs exist (CARLA) but the
deterministic, scrubbable, CRC-verified replay the civic-advocacy use case needs
is genuinely unbuilt prior art (see synthesis "Genuine Gap").

## 8. Performance and parallelism achieved (the numbers ladder)

- **SUMO** (single-threaded C++): "up to 100,000 vehicle updates/s on a 1 GHz
  machine," networks of "several 10,000 edges"
  ([at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)); FAQ quotes
  ~80k–700k vehicle-updates/s on a desktop depending on scenario
  ([FAQ](https://sumo.dlr.de/docs/FAQ.html)).
- **CityFlow** (C++, OpenMP): "more than twenty times faster than SUMO," scaling
  demonstrated 1→8 threads up to a 30×30-grid city roadnet
  ([arXiv:1905.05217](https://arxiv.org/abs/1905.05217),
  [GitHub](https://github.com/cityflow-project/CityFlow)).
- **MOSS** (CUDA): "iterates at 84.09 Hz, achieving 88.92× computational
  acceleration" in a city-scale scenario vs CPU microsim
  ([arXiv:2406.10661 abstract](https://ui.adsabs.harvard.edu/abs/arXiv:2406.10661));
  README rounds this to "100×."
- **MATSim/HERMES**: 2.5× over QSim on 1M-agent Switzerland (3:33 vs 8:45
  min/iteration) ([HERMES post](https://matsim.org/news/2020/introducing-hermes/)).
- **BEAM**: "a single day of mobility and charging for 60,000 agents takes anywhere
  from 15 to 60 minutes" ([arXiv:2301.12901](https://arxiv.org/pdf/2301.12901/v2)).
- **Aimsun**: wins "unprecedented in scale" whole-emirate contracts (Abu Dhabi,
  2021) on meso/hybrid fidelity
  ([Traffic Technology Today](https://www.traffictechnologytoday.com/news/data/aimsun-and-siemens-win-abu-dhabi-simulation-contract-unprecedented-in-scale.html)).

**Lesson for us:** the credibility bar for "fast" has moved from SUMO's ~10⁵
updates/s single-thread to CityFlow's ~20× to MOSS's ~90× on GPU. Our 100 ms tick
+ Go single-writer core targets SUMO/CityFlow territory (10⁴–10⁵ vehicles,
city-district scale) without GPU; MOSS proves the ceiling move exists if ever
needed — but note GPU + our determinism envelope (ADR-0005 §6) is an unverified
combination.

## 9. Extensibility surfaces: plugin folders, devices, SDKs

- **SUMO**: source-level extension (models in C++), runtime extension via TraCI;
  vehicle "devices" (rerouting, transition-of-control, emissions) as opt-in
  per-vehicle behaviors ([eclipse.dev/sumo](https://www.eclipse.dev/sumo/)).
  Third parties fork formats/tools rather than the core.
- **MATSim**: replaceable strategy modules + EventHandlers + a large `contribs/`
  tree; "modules can be replaced by own implementations to test single aspects"
  ([matsim-libs README](https://github.com/matsim-org/matsim-libs)).
- **Aimsun**: two-tier — the **AAPI** (C++/Python callbacks into a running sim:
  read detectors, set signals; e.g. `AKIGetSimulationStepTime()`)
  ([API runtime info](https://docs.aimsun.com/next/24.0.0/UsersManual/ApiRunTimeInformation.html))
  and the **microSDK** to *replace* behavioral models: drop a plugin in a folder,
  declare it in XML, subclass `A2BehavioralModelCreator` returning custom
  car-following/lane-change evaluations
  ([microSDK docs](https://docs.aimsun.com/next/26.0.0/UsersManual/MicroSDKDescription.html)).
- **Vissim**: three documented extension interfaces — **COM** ("allows to read &
  set attributes of Vissim objects or to manipulate them"), **DriverModel.dll**
  ("replace internal car following behavior model of Vissim by own algorithm,"
  optionally lane changing and signal reaction), **DrivingSimulator.dll** (couple
  an external driving simulator — the hook CARLA's co-sim builds on); signals
  scriptable via VAP (vehicle-actuated programming)
  ([PTV CoEXist slides](https://www.rupprecht-consult.eu/fileadmin/user_upload/D2.10-Vissim-extension-new-features-and-improvements_final.pdf),
  [CARLA PTV co-sim docs](https://carla.readthedocs.io/en/latest/adv_ptv/),
  [Wikipedia](https://en.wikipedia.org/wiki/PTV_Vissim)).
- **CARLA**: Python/C++ client API over RPC; Traffic Manager for autopilot fleets;
  sensor blueprints as the extension type
  ([core concepts](https://carla.readthedocs.io/en/latest/core_concepts/)).

**Lesson for us:** there are two distinct extension needs the field separates —
*observe/influence* (AAPI/TraCI tier) and *replace the model* (microSDK tier). Our
controller interface covers the first by design; the second (swap IDM for a custom
car-following per vehicle class) is worth keeping open as a config-level vehicle
type parameter, not a plugin system ([[concept-vehicle-controller-interface]]).

## 10. Demand generation machinery (the hidden half of every simulator)

Every successful tool ships demand tooling alongside the sim, because a network
without demand is unusable:

- **SUMO**: `od2trips`, `duarouter`, `dfrouter` (counts→routes), `activitygen`
  (synthetic population) ([at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)).
- **MATSim/SimMobility**: whole synthetic-population/activity-chain pipelines;
  SimMobility splits it into three products (LT land-use, MT within-day demand,
  ST movement) ([SimMobility README](https://github.com/smart-fm/simmobility-prod),
  [short-term paper](https://core.ac.uk/download/pdf/159995708.pdf)).
- **MOSS**: generates OD matrices with a pre-trained generative neural network from
  globally available inputs (e.g. satellite imagery) — demand synthesis as an ML
  feature ([arXiv:2405.12520](https://arxiv.org/abs/2405.12520)).
- **A/B Street**: imports real OSM networks in minutes and synthesizes demand from
  census/commute data, which is what makes "click a map, get traffic" possible
  ([A/B Street GitHub](https://github.com/a-b-street/abstreet)).

**Lesson for us:** VISION use case 2 (OSM import → baseline traffic) is half
geometry, half demand. Demand generation is a first-class tool in the suite, not
an afterthought; start with OD/count-driven generation (SUMO's `dfrouter`/`od2trips`
shape) and leave activity-based synthesis out of scope ([[concept-scenario-format]],
[[integration-osm-extraction]]).
