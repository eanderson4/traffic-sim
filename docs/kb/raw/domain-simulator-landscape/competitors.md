# Prior Art Survey: Simulator Landscape

> Source: web research | Researched: 2026-07-16
> "Competitors" here = every simulator whose architecture, API, licensing, or
> community we can steal from or be warned by. Time-model specifics of each system
> were already surveyed in [[arch-time-model]]'s competitors file and are not
> repeated; the focus is everything *else*: module decomposition, APIs, outputs,
> replay, scale, license, governance.

## The incumbent open-source microsim

### SUMO — the 25-year reference implementation (and its API cautionary tale)
- Origins: development started 2000 at ZAIK/DLR to give the research community a
  common, open microscopic platform; pure C++, CLI-first by design
  ([SUMO at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)).
- Architecture: suite of ~14 single-purpose applications + Python tools around
  shared XML formats; DUA runs *outside* the sim binary
  ([same](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)).
- Network/demand: imports OSM, VISUM, Vissim, Shapefiles, MATSim, OpenDRIVE;
  lane-level graph with junction-internal connections (deep dive
  [[arch-road-graph-model]]).
- Control API: TraCI (TCP stepping protocol, subscriptions, multi-client
  `SetOrder`); measured 11× overhead on 9k vehicles; libsumo/libtraci as
  in-process/transport alternatives with documented limitations
  ([TraCI](https://sumo.dlr.de/docs/TraCI/index.html),
  [Libsumo](https://sumo.dlr.de/docs/Libsumo.html)).
- Outputs: richest catalog in the field — FCD, tripinfo, E1/E2/E3 detectors,
  edge/lane measures, queue, emissions, SSM; XML→csv/parquet by extension;
  `--output-prefix` for seed runs
  ([Output docs](https://sumo.dlr.de/docs/Simulation/Output/index.html)).
- Performance: ~100k vehicle updates/s on 1 GHz-era hardware; FAQ range
  80k–700k updates/s desktop; single-threaded
  ([at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html),
  [FAQ](https://sumo.dlr.de/docs/FAQ.html)).
- License: **EPL-2.0** (site also lists GPLv2 compatibility) — weak, file-level
  copyleft ([eclipse.dev/sumo](https://www.eclipse.dev/sumo/)).
- Governance/health: Eclipse Foundation project since the 2010s; roadmap shaped by
  the **openMobility Interest Group**; DLR is the institutional home; annual SUMO
  User Conference with published proceedings since 2013
  ([eclipse.dev/sumo](https://www.eclipse.dev/sumo/),
  [Publications.md](https://github.com/eclipse-sumo/sumo/blob/main/docs/web/docs/Publications.md)).
  Docs (sumo.dlr.de/docs) are the best in the open-source field — a wiki-style
  manual covering every tool, format, and model.
- **vs traffic-sim (us):** steal the tool-suite split, the detector/output
  vocabulary, the docs-as-product discipline, and the institutional-governance
  aspiration. Avoid its per-step socket RPC as the control plane; our NATS
  subscription model is TraCI-subscriptions generalized. Its formats are the
  field's interop gravity well (MOSS ships a SUMO converter) — consider import
  compatibility deliberately ([[arch-road-graph-model]]).

## Agent-based activity simulators (batch equilibrium)

### MATSim — the co-evolutionary toolbox
- Java "toolbox," not a tool: controller loop = mobsim → scoring → replanning, run
  for tens-to-hundreds of iterations until relaxed; every module replaceable
  ([matsim-libs README](https://github.com/matsim-org/matsim-libs),
  [book (open access)](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf)).
- Interface: events stream (LinkEnterEvent…) consumed by EventHandlers; **no
  runtime control API — batch only**
  ([EventHandler doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1events_1_1handler_1_1_event_handler.html)).
- Scale: 1M+ agent national scenarios; HERMES event rewrite 2.5× QSim (3:33 vs
  8:45 min/iter) but dropped signals/replanning
  ([HERMES](https://matsim.org/news/2020/introducing-hermes/)).
- License: **GPL** (v2) ([mvnrepository](https://mvnrepository.com/artifact/org.matsim/matsim/2026.0-2025w26)) —
  strong copyleft; ideas yes, code no.
- Governance/health: TU Berlin VSP + ETH Zürich IVT, 20+ years, weekly snapshots
  (repo pushed the day of this research); the open-access book is the best
  written architecture doc in the field ([GitHub API check](https://api.github.com/repos/matsim-org/matsim-libs)).
- Config: XML with per-module sections; 15 years of module accretion = the
  canonical config-sprawl warning ([book](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf)).
- **vs us:** events-as-output is our metrics architecture (NATS-native). The
  equilibrium iteration loop and activity-based demand are out of scope for us;
  its config sprawl is what our scenario format must avoid
  ([[concept-scenario-format]]).

### BEAM — MATSim fork turned product (energy/autonomy)
- LBNL (+NREL) framework "built around MATSim… with extensive modifications" for
  multithreaded within-day simulation; ride-hail, EV, parking; R5/GTFS transit
  routing ([BEAM README](https://github.com/LBNL-UCB-STI/beam)).
- Runtime: 60k agents / one simulated day ≈ 15–60 min
  ([arXiv:2301.12901](https://arxiv.org/pdf/2301.12901/v2)).
- License: **GPLv3** (2024, UC Regents/LBNL+NREL)
  ([LICENSE](https://raw.githubusercontent.com/LBNL-UCB-STI/beam/develop/LICENSE)).
- **vs us:** proof that a MATSim core can carry a whole policy-analysis product —
  and that the batch-equilibrium shape is the common ancestor of that entire
  family. Not our shape.

### SimMobility — the scope-sink cautionary tale
- MIT ITS Lab + SMART (Singapore); three integrated products: LT (land use,
  days→years), MT (within-day demand), ST (microscopic movement)
  ([README](https://github.com/smart-fm/simmobility-prod),
  [Adnan et al. short-term paper](https://core.ac.uk/download/pdf/159995708.pdf)).
- C++; "millions of agents… second-by-second to year-by-year."
- License: a **custom "SIMMOBILITY Version Control License"** users must accept to
  even access source ([README](https://github.com/smart-fm/simmobility-prod)) —
  a one-off license that chills reuse and contribution.
- **vs us:** two warnings: (1) integrating traffic + land-use + activity modeling
  is a decade-long research program, not a feature; (2) invent your own license
  and the community stays home.

## Proprietary professional tools

### PTV Vissim — the industry-standard microsim
- Since 1992, Karlsruhe; Wiedemann (1974) psycho-physical car-following; Helbing
  social-force pedestrians; links+connectors network with explicit Priority
  Rules / Conflict Areas / Signal Heads; VAP for actuated signals
  ([Wikipedia](https://en.wikipedia.org/wiki/PTV_Vissim)).
- Part of the PTV Vision suite (Visum macro planning, Vistro signal
  optimization); since the Econolite merger, signal products integrate across the
  Umovity family (Vistro ↔ Centracs)
  ([Wikipedia](https://en.wikipedia.org/wiki/PTV_Vissim),
  [Econolite/Umovity](https://www.econolite.com/mobility-tech-update/)).
- Extension: COM automation API + replaceable driver-model DLL; DOT practice is
  ~10 seed runs averaged ([microsimulation.pub](https://www.microsimulation.pub/articles/00219)).
- License: proprietary. Corporate: Bridgepoint majority (Jan 2022), Porsche SE
  shareholder; PTV + Econolite merged under the **Umovity** brand since 2023;
  >2,500 cities use PTV products
  ([PTV news](https://www.ptvgroup.com/en/resources/news/company/trb-2024-ptv-group-presents-integrated-solutions-towards-simulation-real)).
- **vs us:** its institutional practices (seed sweeps, calibration protocols,
  conflict-area junction modeling) are documented in DOT guidance and free to
  adopt. Internals are closed; we learn from its manuals' *concepts*, never code.

### Aimsun Next — three fidelities, one network
- Micro/meso/macro on one network model with hybrid boundaries (FD-based dummy
  vehicles); won Abu Dhabi's whole-emirate model ("unprecedented in scale," 2021)
  ([TTT](https://www.traffictechnologytoday.com/news/data/aimsun-and-siemens-win-abu-dhabi-simulation-contract-unprecedented-in-scale.html)).
- Extension two-tier: AAPI callbacks (C++/Python: read detectors, set signals
  mid-run) + microSDK plugin-folder model replacement (`A2BehavioralModelCreator`,
  XML-declared plugins)
  ([API docs](https://docs.aimsun.com/next/24.0.0/UsersManual/ApiRunTimeInformation.html),
  [microSDK](https://docs.aimsun.com/next/26.0.0/UsersManual/MicroSDKDescription.html)).
- License: proprietary; **Siemens acquired Aimsun in March 2018**
  ([Siemens press release](https://press.siemens.com/global/en/pressrelease/siemens-acquire-aimsun)).
- **vs us:** the one-network/three-fidelities idea maps to a future micro→meso
  (LTM) escalation path ([[domain-macroscopic-flow-models]]); the observe/influence
  vs replace-model API split is a clean lesson for our controller interface.

## Driving simulators (game-engine lineage)

### CARLA — renderer-first research platform
- Server = UE4/C++ (world, actors, blueprints, sensors, Traffic Manager);
  clients = Python/C++ over RPC; maps are OpenDRIVE 1.4; recorder = server-side
  state log; SUMO and Vissim co-simulation bridges shipped
  ([core concepts](https://carla.readthedocs.io/en/latest/core_concepts/)).
- Purpose: training/validation of *autonomous driving stacks* — "flexible
  specification of sensor suites and environmental conditions" + open assets
  ([arXiv:1711.03938](https://arxiv.org/abs/1711.03938)).
- License: **MIT** (code) on top of Unreal Engine (Epic EULA); 14.2k stars,
  pushed the day of this research
  ([GitHub API](https://api.github.com/repos/carla-simulator/carla)).
- Governance: originated at Intel Labs / Computer Vision Center; since 2023 under
  the **Embodied AI Foundation** with CVC
  ([ACM citation record](https://dl.acm.org/doi/full/10.1145/3727875),
  [arXiv:2406.00473](https://arxiv.org/html/2406.00473)).
- **vs us:** deliberately opposite corner of the design space: photorealism +
  sensors vs our lane-level dynamics + metrics. VISION already excludes
  photorealistic 3D (non-goal); CARLA's UE4 build weight and sync-mode coupling
  (see [[arch-time-model]]) confirm ADR-0003 (MapLibre-first) was the cheap
  correct call.

## Co-simulation frameworks

### Eclipse MOSAIC — federation as a product
- RTI (runtime infrastructure) + Ambassador/Federate wrappers with HLA-inspired
  interfaces; couples SUMO/PHABMACS, ns-3/OMNeT++/SNS/Cell, Application sim,
  environment, visualization ([Springer review](https://link.springer.com/content/pdf/10.1186/s13173-021-00113-x.pdf),
  [eclipse.dev/mosaic](https://eclipse.dev/mosaic/)).
- Java; **EPL-2.0** ([GitHub API](https://api.github.com/repos/eclipse-mosaic/mosaic));
  committer team Fraunhofer FOKUS + DCAITI; two releases in 2025 alone (25.1,
  25.2), repo pushed within 24 h of this research
  ([eclipse.dev/mosaic](https://eclipse.dev/mosaic/)).
- **vs us:** the Ambassador pattern ≈ "adapter between a foreign simulator and the
  bus" — worth remembering if we ever ingest an external sim (e.g. replay a SUMO
  scenario into our engine). Building a general RTI ourselves is a trap: time
  synchronization across federates is a research field (HLA), and ADR-0005's
  single-authority model dodges it by design.

## RL-first engines (the 2017–2024 wave)

### CityFlow — data-structure-driven speed
- C++ engine, OpenMP threads (1–8), Python bindings (pybind11); ">20× faster than
  SUMO"; JSON scenario trio; Docker-first install
  ([arXiv:1905.05217](https://arxiv.org/abs/1905.05217),
  [GitHub](https://github.com/cityflow-project/CityFlow)).
- License **Apache-2.0** ([project site](https://cityflow-project.github.io/));
  ~1k stars; last push 2025-08 — alive but slow; a successor (CityFlowER, Dec
  2024) exists in the same org ([GitHub API](https://api.github.com/repos/cityflow-project/CityFlow),
  [org page](https://github.com/orgs/cityflow-project/repositories)).
- **vs us:** the benchmark to beat on CPU. Its entire pitch — "SUMO is not
  scalable to large networks/flows for RL sampling" — validates that
  faster-than-realtime batch is the feature the RL community buys
  ([arXiv abstract](https://arxiv.org/abs/1905.05217)).

### Flow — RL wrapper over TraCI (and its limits)
- Berkeley Mobile Sensing Lab; wraps SUMO (later Aimsun) via TraCI, adds RLlib
  integration and benchmarks (ring road, merge, grid)
  ([Flow GitHub](https://github.com/flow-project/flow),
  [CoRL 2018 benchmarks](https://rise.cs.berkeley.edu/wp-content/uploads/2018/11/Benchmarks-for-reinforcement-learning-in-mixed-autonomy-traffic.pdf)).
- License **MIT** ([LICENSE.md](https://github.com/flow-project/flow/blob/master/LICENSE.md));
  1,188 stars but last push 2024-07 — effectively stale after ~6 years
  ([GitHub API](https://api.github.com/repos/flow-project/flow)).
- **vs us:** an RL framework *over* a socket-stepping sim inherits the sim's
  bottleneck (TraCI 11×). Lesson: put the fast path (unpaced batch driver,
  ADR-0005 §4) in the engine, not in a wrapper.

### MOSS — the GPU ceiling (2024, live)
- CUDA engine: 84.09 Hz city-scale iteration, 88.92× acceleration vs CPU microsim;
  protobuf data model (`cityproto` with **Go bindings**), YAML config, AVRO +
  PostgreSQL outputs, gRPC routing service, v1.1 checkpoint save/restore
  ([GitHub](https://github.com/tsinghua-fib-lab/moss),
  [arXiv:2406.10661](https://ui.adsabs.harvard.edu/abs/arXiv:2406.10661)).
- Toolchain includes **SUMO format conversion** and generative OD synthesis from
  satellite imagery ([arXiv:2405.12520](https://arxiv.org/abs/2405.12520)).
- License **MIT** (2024, FIB LAB Tsinghua)
  ([LICENSE](https://raw.githubusercontent.com/tsinghua-fib-lab/moss/main/LICENSE)).
- **vs us:** where we'd go if 10⁶-vehicle metro scale ever becomes the
  requirement. Note it *still* has no controller bus or seekable replay — scale
  alone didn't produce our features.

## Civic/UX-first

### A/B Street — the UX ambition (and the sim-maintenance warning)
- Rust + WASM: runs in the browser or native, anywhere OSM covers; OSM import +
  synthesized demand; game-like editing (LTNs, bus lanes, 15-min cities);
  Apache-2.0; ~8.1k stars ([GitHub](https://github.com/a-b-street/abstreet),
  [HelloGitHub card](https://hellogithub.com/en/repository/a-b-street/abstreet)).
- Traffic sim is a custom DES "not based on any research papers or existing
  systems" ([DES tech doc](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html)).
- **Sept 2022: the sim was declared unmaintained** — "getting the traffic
  simulation to work reasonably feels endlessly hard"; effort moved to OSM import,
  LTN/15-min tools; by 2025 the project had split into several specialized tools
  under A/B Street Ltd ([issue #996](https://github.com/a-b-street/abstreet/issues/996),
  [README 2025 update](https://github.com/a-b-street/abstreet)).
- **vs us:** adopt the first-run ambition ("pick a place on the map, see traffic
  in minutes") and the advocacy framing (their Laura Adler quote: citizens as
  "active generators of their own urban visions"
  ([README](https://github.com/a-b-street/abstreet))) — it is VISION use case 4
  wearing a UI. Heed the warning: a hand-rolled sim without validated models
  becomes the unmaintainable part; our IDM/MOBIL models are already validated
  ([[domain-traffic-flow-models]]).

## Calibration points

- **OpenTrafficSim** (TU Delft, Java, BSD-style): micro+macro+meta in one
  environment, OpenDRIVE parser, links to external code and driving simulators;
  academically solid, small community
  ([opentrafficsim.org](https://opentrafficsim.org/old/)).
- **CORSIM/Paramics/TransModeler**: proprietary legacy microsims; TRANSIMS open
  ([software table, TU Delft](https://research.tudelft.nl/files/219999918/524589_Fulltext-3.pdf)).
- **OTM-MPI**: first open-source distributed-memory *macroscopic* sim for HPC —
  the scale-out pattern lives at macro fidelity
  ([OSTI/NLR PDF](https://docs.nlr.gov/docs/fy21osti/76996.pdf)).
- **DTALite**: deliberately simple queue-based mesoscopic for fast calibration —
  "fast model evaluation" as a design goal
  ([Zhou & Taylor 2014, cited in T2R2](https://t2r2.star.titech.ac.jp/rrws/file/CTT100929109/ATD100000413/)).

## Positioning Summary

| System | Lang | License | Control API | Scale claim | Replay | Health/governance |
|---|---|---|---|---|---|---|
| SUMO | C++ | EPL-2.0 | TraCI socket (11× pain), libsumo | ~10⁵ veh-updates/s single-thread | re-run (deterministic default) | Eclipse/DLR, 25 yrs, conf. since 2013 |
| MATSim | Java | GPL-2.0 | none (batch) | 1M agents, 3:33 min/iter (HERMES) | re-run + events log | TUB/ETH, 20 yrs, weekly snapshots |
| BEAM | Scala/Java | GPL-3.0 | none (batch) | 60k agents/day in 15–60 min | re-run | LBNL+NREL, active |
| SimMobility | C++ | custom | none | "millions of agents" multi-scale | re-run | MIT/SMART, slow |
| Vissim | closed | proprietary | COM + driver DLL | city networks (DOT practice) | re-run × seeds | Umovity (Bridgepoint/Porsche) |
| Aimsun | closed | proprietary | AAPI + microSDK | whole-emirate meso/hybrid | re-run × seeds | Siemens (2018) |
| CARLA | C++/UE4 | MIT (+UE EULA) | Python/C++ RPC, sync/async | sensor-bound, ~10s FPS rendering | state-log recorder | Embodied AI Fdn, 14k★, active |
| MOSAIC | Java | EPL-2.0 | ambassador/RTI | federation of the above | re-run | Fraunhofer FOKUS/DCAITI, 2 rel. 2025 |
| CityFlow | C++ | Apache-2.0 | in-proc Python | >20× SUMO, 8 threads | replay files for viz | ~1k★, slow since 2019 |
| Flow | Python | MIT | TraCI wrapper | inherits SUMO | re-run | 1,188★, stale since 2024-07 |
| MOSS | CUDA/C++ | MIT | in-proc Python | 84 Hz city-scale, 88.9× | checkpoint v1.1 | Tsinghua FIB, active 2024– |
| A/B Street | Rust | Apache-2.0 | none (embedded) | city districts, browser | re-run | sim unmaintained since 2022 |
| **us** | **Go** | **TBD (ADR)** | **NATS async intents** | **target: SUMO–CityFlow tier** | **keyframes + intent log + CRC (ADR-0005)** | **greenfield** |
