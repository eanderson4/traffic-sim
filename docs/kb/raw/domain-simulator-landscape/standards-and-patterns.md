# Standards & Patterns: Simulator Landscape

> Source: academic research + pattern identification | Researched: 2026-07-16
> Formalisms, interop standards, license taxonomy, named patterns and
> anti-patterns across the simulator field. Time-model standards (DES/DEVS/HLA)
> live in [[arch-time-model]]'s standards file and are not repeated.

## Interop standards actually in force

### ASAM OpenDRIVE — the road-network exchange format
XML standard for lane-level road description; CARLA's native map format ("all
[eight maps] use ASAM OpenDRIVE 1.4"), importable by SUMO's `netconvert` and
parsed by OpenTrafficSim
([CARLA core concepts](https://carla.readthedocs.io/en/latest/core_concepts/),
[SUMO at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html),
[OTS OpenDRIVE parser](https://opentrafficsim.org/docs/1.00.01/ots-parser-opendrive/apidocs/org/opentrafficsim/road/network/factory/opendrive/JunctionTag.html)).
Relevance: OpenDRIVE is the neutral ground between driving-sim and traffic-sim
worlds; our road graph doesn't need to *be* OpenDRIVE, but import compatibility
is cheap credibility ([[arch-road-graph-model]]).

### ASAM OpenSCENARIO — the maneuver/scenario counterpart
Scenario-description standard (XML, plus a 2.x DSL) pairing with OpenDRIVE —
map vs behavior split; CARLA's ScenarioRunner implements partial support
([ScenarioRunner docs](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/),
[ASAM XML standard example](https://publications.pages.asam.net/standards/ASAM_OpenSCENARIO/ASAM_OpenSCENARIO_XML/latest/10_scenario_creation/10_01_description_sample.html)).
Relevance for [[concept-scenario-format]]: the field already separates *network*
from *demand/behavior* artifacts — our scenario directory should too.

### TraCI as a de-facto protocol standard
Not a formal standard, but implemented independently by third parties: Veins
carries "its own TraCI interface implementation" to drive SUMO
([sumo#13548](https://github.com/eclipse-sumo/sumo/issues/13548)); MOSAIC and
CARLA both couple through it. **Lesson: any stepping API we publish becomes a
protocol others will re-implement — version the contract, not the transport.**

### SUMO's XML formats as the gravity well
MOSS's toolchain ships "SUMO format conversion" as a headline feature
([moss GitHub](https://github.com/tsinghua-fib-lab/moss)); SUMO itself imports
MATSim networks ([at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html)).
Twenty-five years of tutorials made `.net.xml`/`.rou.xml` the field's de-facto
scenario interop. Steal *concepts* freely; weigh import compatibility as a
feature decision ([[arch-road-graph-model]], [[integration-osm-extraction]]).

### GTFS (transit) — adjacency note
BEAM's R5 router consumes GTFS for realistic multimodal demand
([BEAM README](https://github.com/LBNL-UCB-STI/beam)). Out of v1 scope (VISION
non-goal: transit), but it is *the* standard if transit demand ever enters.

## License taxonomy (we are open-source — this is binding)

| License class | Projects | What it means for us |
|---|---|---|
| Permissive (MIT/Apache-2.0/BSD) | CARLA (MIT), CityFlow (Apache-2.0), Flow (MIT), MOSS (MIT), A/B Street (Apache-2.0), OpenTrafficSim (BSD-style) | Code borrowable with attribution; docs/models freely reusable |
| Weak copyleft (EPL-2.0) | SUMO, MOSAIC | File-level copyleft ([EPL-2.0 text](https://www.eclipse.org/legal/epl-2.0/)); ideas and formats fine, copying files into a permissive project is not |
| Strong copyleft (GPL-2.0/3.0) | MATSim (GPL-2.0), BEAM (GPLv3) | Read for understanding only; no code, no linking |
| Custom/one-off | SimMobility "Version Control License" | Ambiguity itself is the barrier; avoid inventing our own |
| Proprietary | Vissim, Aimsun, CORSIM, Paramics, TransModeler ([TU Delft table](https://research.tudelft.nl/files/219999918/524589_Fulltext-3.pdf)) | Public manuals/papers/DOT guidance only |

**Synthesis consequence:** our own license choice (no ADR yet) should be
permissive (MIT or Apache-2.0) if we want the RL/benchmark community's adoption
pattern — every post-2017 entrant (CityFlow, Flow, MOSS) chose permissive.

## Governance patterns

- **Institutional home = longevity.** SUMO (DLR → Eclipse Foundation, roadmap via
  openMobility Interest Group, [eclipse.dev/sumo](https://www.eclipse.dev/sumo/)),
  MATSim (TU Berlin/ETH), MOSAIC (Fraunhofer FOKUS + DCAITI), CARLA (Intel →
  Embodied AI Foundation). All 20+ year or foundation-backed projects are alive.
- **Lab project = decay on graduation.** Flow (last push 2024-07), CityFlow
  (sporadic since 2019), SimMobility (slow), A/B Street sim (unmaintained since
  2022). The RL-boom engines died when the papers stopped.
- **Corporate consolidation = docs stay public, roadmaps don't.** Aimsun →
  Siemens (2018); PTV → Bridgepoint/Porsche → Umovity (2023)
  ([Siemens](https://press.siemens.com/global/en/pressrelease/siemens-acquire-aimsun),
  [PTV](https://www.ptvgroup.com/en/resources/news/company/trb-2024-ptv-group-presents-integrated-solutions-towards-simulation-real)).
- **Community assets that predict survival:** SUMO's docs wiki + annual User
  Conference proceedings since 2013
  ([Publications.md](https://github.com/eclipse-sumo/sumo/blob/main/docs/web/docs/Publications.md));
  MATSim's open-access book; CARLA's readthedocs; A/B Street's
  one-click browser build.

## Named patterns (steal)

1. **Tool-suite over shared formats** — many small executables, formats as the
   contract (SUMO's ~14 apps; MOSS's repo constellation). Maps to our
   service-per-concern over NATS subjects ([implementation §1](./implementation.md)).
2. **Events-as-observations** — the engine emits an immutable tick-stamped event
   stream; detectors, metrics, viz are downstream consumers (MATSim). Already
   adopted in [[arch-time-model]]; reaffirmed here as the *metrics* architecture.
3. **Subscription-over-polling** — TraCI subscriptions exist because polling
   measured 11× slower ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)).
   NATS subjects are subscriptions by construction; never design a
   per-vehicle query RPC into the hot path.
4. **In-process escape hatch behind one contract** — libsumo = same API, no
   socket ([Libsumo](https://sumo.dlr.de/docs/Libsumo.html)). If RL training
   ever needs it, offer an in-process controller that speaks the identical
   intent/snapshot contract — contract first, transport second.
5. **Plugin-folder model replacement** — Aimsun microSDK: drop library + XML
   declaration, override car-following/lane-change
   ([microSDK](https://docs.aimsun.com/next/26.0.0/UsersManual/MicroSDKDescription.html)).
   Lighter variant for us: per-vehicle-type model selection in scenario config.
6. **Scenario-as-directory** — top config references typed artifacts (SUMO
   `.sumocfg`, CityFlow `config.json`, MOSS YAML). Diffable, seed-sweepable,
   variant-friendly ([[concept-scenario-format]]).
7. **Replication seed sweeps** — (scenario, seed) as the run key; ~10-run DOT
   practice ([microsimulation.pub](https://www.microsimulation.pub/articles/00219)).
   Already in ADR-0005 consequences; the field validates it as *the* methodology.
8. **Checkpoint/restore for long batch runs** — MOSS v1.1 GPU↔CPU state
   checkpoint ([moss GitHub](https://github.com/tsinghua-fib-lab/moss)); a weak
   form of our keyframe snapshots.
9. **Detector vocabulary** — E1 loop / E2 lane-area / E3 entry-exit + tripinfo +
   queue as the engineer-facing artifact set
   ([SUMO output docs](https://sumo.dlr.de/docs/Simulation/Output/index.html));
   adopt names and semantics in [[domain-congestion-metrics]].
10. **First-run-in-minutes UX** — A/B Street's browser build + OSM-anywhere
    import ([GitHub](https://github.com/a-b-street/abstreet)). The single most
    cited reason people tried it.

## Anti-patterns (documented failures)

1. **Blocking the tick on external clients** — TraCI barrier 11× measured; CARLA
   sync mode same trap. Decided against in ADR-0005 §3; listed here because every
   successor (libsumo, CityFlow, MOSS) exists largely to escape it.
2. **Config sprawl** — MATSim's XML accreted per-module sections for 15+ years
   ([book](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf));
   every contrib adds knobs. Counter-pattern: versioned, minimal, scenario-scoped
   config with explicit defaults ([[concept-scenario-format]]).
3. **Game-engine dependency for a non-rendering product** — CARLA's UE4 coupling
   delivers photorealism at the price of build weight and GPU-bound batch; our
   non-goal list already excludes this (VISION + ADR-0003).
4. **Inventing a license** — SimMobility's custom license correlates with a
   niche, closed-feeling community despite MIT backing
   ([README](https://github.com/smart-fm/simmobility-prod)).
5. **Hand-rolled unvalidated models** — A/B Street's bespoke DES became
   unmaintainable ("endlessly hard") and was abandoned for the tooling around it
   ([issue #996](https://github.com/a-b-street/abstreet/issues/996)).
6. **RL-wrapper-over-slow-API** — Flow over TraCI inherits the 11× wall; the
   fast path belongs in the engine (unpaced driver, ADR-0005 §4), not in a
   wrapper library.
7. **Federation for its own sake** — an RTI/ambassador stack (MOSAIC) exists to
   sync *separate* simulators; adopting it inside one product re-imports HLA
   time-management complexity ADR-0005 deliberately avoids.
8. **Scope sink: land-use integration** — SimMobility's LT/MT/ST trilogy is a
   decade-scale program; VISION's non-goals (land use, activity chains) exist for
   this reason.

## Empirical anchors

- CPU microsim speed ladder: SUMO ~10⁵ veh-updates/s single-thread → CityFlow
  >20× → MOSS 88.9× (GPU, 84 Hz city-scale)
  ([SUMO](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html),
  [CityFlow](https://arxiv.org/abs/1905.05217),
  [MOSS](https://ui.adsabs.harvard.edu/abs/arXiv:2406.10661)).
- Batch agent-sim pace: 1M agents ≈ 3.5 min/iter
  ([HERMES](https://matsim.org/news/2020/introducing-hermes/)); 60k agents/day ≈
  15–60 min ([BEAM survey](https://arxiv.org/pdf/2301.12901/v2)).
- Community size (GitHub API, 2026-07-16): CARLA 14.2k★, A/B Street 8.1k★
  ([HelloGitHub](https://hellogithub.com/en/repository/a-b-street/abstreet)),
  Flow 1.2k★ (last push 2024-07), CityFlow 1.0k★ (last push 2025-08), MATSim
  0.6k★, MOSAIC 113★; CARLA, MATSim and MOSAIC were all pushed within 24 h of
  this research.
- Control-API overhead: 90 s vs 8 s on 9k vehicles
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html)) — the single most
  decision-relevant number in this survey.

## Open Questions

- Vissim/Aimsun internal module decomposition (proprietary; only API surface and
  manuals visible) — flag where inference stops.
- Exact current MATSim config-module count (sprawl quantification) — needs a
  doxygen crawl; qualitative claim is sourced, the number is not.
- MOSS determinism across GPU models/architectures vs our ADR-0005 envelope —
  undocumented upstream.
- Does SUMO `.net.xml` import belong in v1 scope? → [[arch-road-graph-model]] +
  [[integration-osm-extraction]] decision, informed by the gravity-well argument.

## Master source list

SUMO: [at a Glance](https://sumo.dlr.de/docs/SUMO_at_a_Glance.html) ·
[TraCI](https://sumo.dlr.de/docs/TraCI/index.html) ·
[Libsumo](https://sumo.dlr.de/docs/Libsumo.html) ·
[Output](https://sumo.dlr.de/docs/Simulation/Output/index.html) ·
[FAQ](https://sumo.dlr.de/docs/FAQ.html) ·
[Randomness](https://sumo.dlr.de/docs/Simulation/Randomness.html) ·
[eclipse.dev/sumo](https://www.eclipse.dev/sumo/) ·
[Publications](https://github.com/eclipse-sumo/sumo/blob/main/docs/web/docs/Publications.md) —
MATSim: [matsim-libs](https://github.com/matsim-org/matsim-libs) ·
[book](https://library.oapen.org/bitstream/id/859157dd-5478-4089-9fca-b3df7a7a39d4/613715.pdf) ·
[HERMES](https://matsim.org/news/2020/introducing-hermes/) ·
[EventHandler](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1events_1_1handler_1_1_event_handler.html) ·
[GPL via mvnrepository](https://mvnrepository.com/artifact/org.matsim/matsim/2026.0-2025w26) —
CARLA: [paper](https://arxiv.org/abs/1711.03938) ·
[core concepts](https://carla.readthedocs.io/en/latest/core_concepts/) ·
[recorder](https://carla.readthedocs.io/en/latest/adv_recorder/) ·
[governance](https://dl.acm.org/doi/full/10.1145/3727875) —
Aimsun: [AAPI](https://docs.aimsun.com/next/24.0.0/UsersManual/ApiRunTimeInformation.html) ·
[microSDK](https://docs.aimsun.com/next/26.0.0/UsersManual/MicroSDKDescription.html) ·
[Siemens](https://press.siemens.com/global/en/pressrelease/siemens-acquire-aimsun) ·
[Abu Dhabi](https://www.traffictechnologytoday.com/news/data/aimsun-and-siemens-win-abu-dhabi-simulation-contract-unprecedented-in-scale.html) —
Vissim: [Wikipedia](https://en.wikipedia.org/wiki/PTV_Vissim) ·
[Umovity/Bridgepoint](https://www.ptvgroup.com/en/resources/news/company/trb-2024-ptv-group-presents-integrated-solutions-towards-simulation-real) ·
[seed practice](https://www.microsimulation.pub/articles/00219) —
MOSAIC: [eclipse.dev/mosaic](https://eclipse.dev/mosaic/) ·
[VANET review](https://link.springer.com/content/pdf/10.1186/s13173-021-00113-x.pdf) ·
[thesis](https://www.eurecom.fr/publication/7713/download/comsys-publi-7713.pdf) —
CityFlow: [arXiv](https://arxiv.org/abs/1905.05217) ·
[GitHub](https://github.com/cityflow-project/CityFlow) ·
[site/license](https://cityflow-project.github.io/) ·
[install](https://github.com/cityflow-project/CityFlow/blob/master/docs/source/install.rst) —
Flow: [GitHub](https://github.com/flow-project/flow) ·
[LICENSE](https://github.com/flow-project/flow/blob/master/LICENSE.md) ·
[CoRL benchmarks](https://rise.cs.berkeley.edu/wp-content/uploads/2018/11/Benchmarks-for-reinforcement-learning-in-mixed-autonomy-traffic.pdf) ·
[framework paper](https://flow-project.github.io/papers/1710.05465.pdf) —
BEAM: [GitHub](https://github.com/LBNL-UCB-STI/beam) ·
[GPLv3 LICENSE](https://raw.githubusercontent.com/LBNL-UCB-STI/beam/develop/LICENSE) ·
[runtime survey](https://arxiv.org/pdf/2301.12901/v2) —
SimMobility: [GitHub](https://github.com/smart-fm/simmobility-prod) ·
[ST paper](https://core.ac.uk/download/pdf/159995708.pdf) —
MOSS: [arXiv](https://arxiv.org/abs/2405.12520) ·
[benchmarks](https://ui.adsabs.harvard.edu/abs/arXiv:2406.10661) ·
[GitHub](https://github.com/tsinghua-fib-lab/moss) ·
[MIT LICENSE](https://raw.githubusercontent.com/tsinghua-fib-lab/moss/main/LICENSE) —
A/B Street: [GitHub](https://github.com/a-b-street/abstreet) ·
[issue #996](https://github.com/a-b-street/abstreet/issues/996) ·
[DES doc](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html) —
OTS: [opentrafficsim.org](https://opentrafficsim.org/old/) —
[OTM-MPI](https://docs.nlr.gov/docs/fy21osti/76996.pdf) —
[software table](https://research.tudelft.nl/files/219999918/524589_Fulltext-3.pdf) —
[EPL-2.0](https://www.eclipse.org/legal/epl-2.0/)
