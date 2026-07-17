# Standards & Patterns: Scenario Format

> Source: academic research + standards bodies + pattern identification
> | Researched: 2026-07-16

## Formalisms

### The OD matrix and the TAZ

Origin-destination demand is counts of vehicles between **traffic
assignment/analysis zones (TAZ)** over a time period; a TAZ maps to the
network as weighted source/sink edge sets with normalized probabilities.
The matrix is an *authoring* abstraction — every surveyed tool expands it
into trips or per-entry flows before simulation (od2trips draws departure
times randomly within each cell's period)
([od2trips docs](https://sumo.dlr.de/docs/Demand/Importing_O%2FD_Matrices.html)).
Day-scale matrices are shaped by piecewise hourly **timelines**; the SUMO
docs ship German reference curves (TGw2_PKW etc., Schmidt & Thomas 1996)
and weekend scaling factors (e.g. NRW highways Saturday 76.1%)
([same](https://sumo.dlr.de/docs/Demand/Importing_O%2FD_Matrices.html)).

### Turning-movement counts

The intersection-scale demand formalism: per-approach volumes plus
per-junction turning proportions. Aimsun's Traffic State formalizes it
(flows at every input section × turn proportions at every junction, per
Vehicle Type × Trip Purpose)
([Aimsun demand overview](https://docs.aimsun.com/next/24.0.3/UsersManual/DemandOverview.html));
jtrrouter routes from volumes + turning ratios (default split 30/50/20)
([jtrrouter](https://sumo.dlr.de/docs/jtrrouter.html)); Vissim codes it as
vehicle inputs + static routing decisions with relative flows in
**15-minute increments** (MDOT guidance; hourly only if volumes are
consistent) ([MDOT SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).

### Arrival processes: Poisson vs deterministic headways

Microscopic arrivals are either equally spaced or Poisson. SUMO exposes
both directly: `vehsPerHour`/`period` (deterministic) and
`period="exp(X)"` ("exponentially distributed time gaps... a Poisson
process with an expected value of X insertions per second") or per-second
`probability` ([demand docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)).
Time-varying demand is universally **piecewise constant** — time slices
with different rates (od2trips timelines, Vissim intervals, Aimsun
states) — never continuous rate functions.

### Scenario abstraction levels (PEGASUS / Menzel et al.)

The AV-testing community's vocabulary: **functional** scenarios (semantic,
human-language description), **logical** scenarios (state-space parameter
ranges), **concrete** scenarios (fixed parameter values, executable)
— Menzel, Bagschik & Maurer, IEEE IV 2018
([references in ar5iv:1905.03989](https://ar5iv.labs.arxiv.org/html/1905.03989),
[dissertation summary](https://www.doria.fi/bitstream/handle/10024/193629/asif_ammara.pdf?sequence=2&isAllowed=y)).
OpenSCENARIO XML sits at the concrete level; the DSL targets logical
([ASAM comparison](https://www.asam.net/standards/detail/openscenario-xml/)).
**Mapping for us:** our scenario files are concrete scenarios; a
*parameterized variant* (e.g. demand scale ∈ {0.8, 1.0, 1.2}) is a logical
scenario — worth keeping in the vocabulary even if v1 ships concrete only.

### The FHWA alternatives-analysis process (methodology formalism)

Traffic Analysis Toolbox Vol III (2019 update) defines the comparison
ritual: baseline demand pattern → "model sub-variants for each competing
alternative for each travel condition" → determine required run count
from output randomness → run each variant × required replications →
statistical test on the differences
([ch. 6](https://ops.fhwa.dot.gov/publications/fhwahop18036/chapter6.htm),
[intro](https://ops.fhwa.dot.gov/publications/fhwahop18036/introduction.htm)).
The FHWA FAQ states why: microsimulation is stochastic, single runs are
"not representative" ([FAQ](https://ops.fhwa.dot.gov/trafficanalysistools/faq.htm)).
A state-DOT application of Appendix B computed **≥10 seeded runs for a
95% confidence interval** on link speeds/flows
([Carolina Crossroads memo](https://www.scdotcarolinacrossroads.com/FEIS-documents/App_D_Alternatives_Traffic_Analysis_Technical_Memo_part_1.pdf)).
This is the decision-grade discipline our math-vs-vibes and
civic-advocacy use cases must make *easy*.

## Standards

### ASAM OpenX family

- **OpenSCENARIO XML** (dynamic content, `.xosc`, storyboard of
  stories/acts/sequences, trigger-condition model; v1.4.0, 19 May 2026)
  and **OpenSCENARIO DSL** (constraint-based V&V programming language with
  KPIs/checks/coverage); deliberately parallel standards, alignment "best
  effort", not guaranteed
  ([ASAM datasheet](https://www.asam.net/standards/detail/openscenario-xml/)).
- Companion standards: **OpenDRIVE** (static road network), **OpenCRG**
  (surface profiles) — the split "static network vs dynamic content" is
  itself standardized ([same](https://www.asam.net/standards/detail/openscenario-xml/)).
- Adoption: 26 listed authors from OEMs, Tier-1s, and tool vendors (BMW,
  CARIAD, dSPACE, AVL, Bosch, MathWorks, Tencent, Volvo...)
  ([same](https://www.asam.net/standards/detail/openscenario-xml/)).
- Relevance boundary: maneuver-level test cases for AV validation. It has
  no demand model, no signal timing plans, no congestion metrics — the
  three things our scenario format must have. Heaviness verdict: two
  parallel standards + a programming language, to say nothing a
  flow-with-Poisson-arrivals couldn't.

### CommonRoad format

Single-XML scenario (lanelets + obstacles + planning problem + metadata
tags), versioned epochs (2020a XML, 2024 protobuf), curated public
benchmark suite ([paper](https://mediatum.ub.tum.de/doc/1379638/776321.pdf),
[io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html)).
The tags + benchmark-curation practice is the standard to steal for
cross-project scenario comparability.

### Kubernetes API conventions (versioning standard, transferable)

API-group version tracks (alpha/beta/GA); elements removed only via
version bumps; **round-trip without information loss** between served
versions; deprecation windows (beta: 9 months / 3 releases)
([deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)).
The round-trip rule is the transferable gem: a scenario written at
format v1 must survive load-migrate-save through v2 unchanged in meaning.

## Design Patterns Identified

### Manifest-of-parts (aggregate-root config)

One small manifest referencing typed content files (network, demand,
control, metrics). Observed in SUMO (`.sumocfg`), MATSim (`config.xml`),
Aimsun (scenario object); Vissim is the monolith counter-example
([implementation §1](./implementation.md)). Enables per-file reuse across
variants and per-file diffing.

### Measurement-as-declaration

The metric/measurement spec is part of the scenario, not a post-hoc
script: SUMO meandata sets, Vissim evaluation configuration, Aimsun
outputs-to-generate, MATSim scoring config. OpenSCENARIO's omission
(forcing CARLA's `criteria_*` hack) is the negative control
([implementation §8–§9](./implementation.md)).

### Base + overlay → variant (kustomize)

Variants as named overlays (patches + added resources) over a base,
materialized at build time; no copies
([glossary](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/)).
Patch dialects: strategic-merge, JSON6902, JSON merge patch. Constraint
set that keeps it sane: structured edits only, no removal directives, no
templating, no environment-dependent builds
([eschewed features](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/)).

### Constraints-as-data validation (CUE)

Schema and cross-field constraints as versioned data; overrides
disallowed so every value has one origin; adoptable incrementally against
existing YAML ([CUE guide](https://cuelang.org/docs/concept/how-cue-enables-configuration/)).
The "no overrides" property is what makes overlay application
*inspectable*.

### (scenario, seed) run key + seed sweeps

Vissim DOT practice (~10 seeds averaged), Aimsun replications each
holding their seed, FHWA's required-run-count statistics
([microsimulation.pub](https://www.microsimulation.pub/articles/00219),
[Aimsun scenarios docs](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html),
[TAT Vol III](https://ops.fhwa.dot.gov/publications/fhwahop18036/chapter6.htm)).
Already encoded in ADR-0005; the scenario format must carry the base seed
and the sweep must be tooling, not file copies.

### Content-addressed identity

(Not observed in any surveyed traffic tool.) Kustomize's
"everything explicit in git" discipline implies it: a scenario's identity
should be the hash of its materialized content, so recordings
(ADR-0005: keyframes + intent log) can bind to exactly what was run.
Flagged as a gap, see synthesis.

## Anti-patterns (documented failures)

1. **Copy-paste variants** — the field default (SUMO/MATSim/Vissim): N
   near-identical directories drift apart; FHWA's "sub-variants" are a
   process rule, not a mechanism. Kustomize exists precisely because this
   fails at scale ([eschewed features](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/)).
2. **`${VAR}` templating of data files** — "It's no longer data, it's now
   logic that must be compiled. Errors in the output are disconnected
   from the edit that caused it" ([same](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/)).
   CARLA scenario_runner's Python-defined scenarios are the same trap in
   disguise ([scenario_runner README](https://raw.githubusercontent.com/carla-simulator/scenario_runner/master/README.md)).
3. **YAML implicit typing** — `NO`→`false`, `9.3`→float, `Null`→None;
   spec-compliant and production-burning
   ([StrictYAML rationale](https://hitchdev.com/strictyaml/why/implicit-typing-removed/)).
4. **Compressed primary sources** — MATSim's `.xml.gz` habit makes demand
   files ungreppable, undiffable, unreviewable
   ([book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
5. **GUI-authored monoliths** — Vissim's single `.inpx` (network + demand
   + evaluation in one document) and Aimsun's `.ang`: version control
   can't help inside one giant document
   ([PTV help](https://cgi.ptvgroup.com/vision-help/VISSIM_2024_ENG/Content/11_Auswertungen/Ausw_Ergebnisattr_konfig.htm),
   [Aimsun scenarios docs](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html)).
6. **Silent schema drift** — MATSim parameters "may vary from release to
   release" with discovery by generating a full config
   ([book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
7. **Streaming-order footguns** — SUMO requires route files sorted by
   departure time; unsorted input can infinite-loop with persons
   ([demand docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)).
   Loader must sort or validate, never assume.
8. **Wall-clock/calendar timestamps as sim semantics** — conflicts with
   ADR-0005 (tick count is the clock). Demand times must be sim seconds;
   "7:30 AM" is a presentation alias for t=27000, nothing more.
9. **Metrics as post-hoc scripts** — if the metric definition isn't in
   the scenario, two variants can't be compared apples-to-apples
   (OpenSCENARIO's criteria-bolt-on is the proof by absence,
   [scenario_runner docs](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).

## Empirical anchors

- Flow spacing options: constant rate, `number`, `exp(X)` Poisson,
  per-second Bernoulli probability (SUMO).
- Passenger-car speed-factor default `normc(1,0.1,0.2,2)` ≈ 95% within
  80–120% of the limit; trucks 0.05 deviation, rail 0 (SUMO vType).
- Demand coding granularity: 15-minute increments mandated by MDOT
  Vissim guidance; 24-hour timeline tables for day-scale OD shaping.
- Signal default: fixed 90 s cycle when none specified (SUMO).
- Seed culture: SUMO default seed 23423; Vissim ~10-run sweeps; Aimsun
  worked example = 20 replications; FHWA Appendix B application ≥10 runs
  for 95% CI.
- OpenSCENARIO XML 1.4.0 released 19 May 2026; MATSim minimal config =
  3 files; Vissim inpx root `version="603"` ↔ Vissim 11.

## Open Questions

- Encoding of Aimsun's `.ang` document (text or binary?) — not verified;
  affects only the survey's monolith claim, not our design.
- Whether OD→flows expansion should be a build step (od2trips-style,
  deterministic seeded) or a load-time engine feature; determinism of the
  sampled departure times requires the same seeded-stream discipline as
  the engine ([[arch-time-model]]).
- How variants patch *network topology* (the "add a lane" upgrade): needs
  stable lane IDs through network edits — depends on
  [[arch-road-graph-model]] and [[integration-osm-extraction]].
- Whether measurement sets reference elements by ID, by path expression,
  or by spatial query — interacts with the metric catalog being defined
  in [[domain-congestion-metrics]] (researched concurrently).
- Pack format for sharing a scenario directory (zip? tar? git
  submodule-ish references?) — kustomization as tarball/git-URL is the
  precedent ([glossary](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/)).

## Master source list

SUMO: [demand/routes](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html) ·
[od2trips](https://sumo.dlr.de/docs/Demand/Importing_O%2FD_Matrices.html) ·
[jtrrouter](https://sumo.dlr.de/docs/jtrrouter.html) ·
[meandata](https://sumo.dlr.de/docs/Simulation/Output/Lane-_or_Edge-based_Traffic_Measures.html) ·
[traffic lights](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html) ·
[NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html) ·
[#7471 state files](https://github.com/eclipse-sumo/sumo/issues/7471) ·
[#5915 inpx](https://github.com/eclipse-sumo/sumo/issues/5915) ·
[tutorial arXiv:2304.05982](https://arxiv.org/html/2304.05982v2) ·
[roadmap slides](https://cst.fee.unicamp.br/sites/default/files/sumo/sumo-roadmap.pdf) —
MATSim: [book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf) ·
[book TOC/ch. 3](https://toc.library.ethz.ch/objects/pdf03/e01_978-1-909188-75-4_01.pdf) ·
[ETH thesis](https://www.research-collection.ethz.ch/server/api/core/bitstreams/c2692f83-861a-4d66-b9db-c7fb761b736f/content) —
Vissim: [evaluation config help](https://cgi.ptvgroup.com/vision-help/VISSIM_2024_ENG/Content/11_Auswertungen/Ausw_Ergebnisattr_konfig.htm) ·
[dyn-assignment help](https://cgi.ptvgroup.com/vision-help/VISSIM_2025_ENG/Content/6_DynamischeUml/Statische_Routen_erzeugen.htm) ·
[user manual](https://pdfcoffee.com/vissim-user-manual-pdf-free.html) ·
[2022 manual](https://pdfcoffee.com/manual-vissim-2022-4-pdf-free.html) ·
[MDOT SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf) ·
[microsimulation.pub](https://www.microsimulation.pub/articles/00219) —
Aimsun: [scenarios/experiments](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html) ·
[demand overview](https://docs.aimsun.com/next/24.0.3/UsersManual/DemandOverview.html) ·
[output DB](https://docs.aimsun.com/next/23.0.0/UsersManual/OutputDatabaseDefinition.html) ·
[Synchro](https://docs.aimsun.com/next/24.0.3/UsersManual/SynchroImporter.html) —
OpenSCENARIO/CARLA: [ASAM OSC XML](https://www.asam.net/standards/detail/openscenario-xml/) ·
[scenario_runner OSC support](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/) ·
[scenario_runner README](https://raw.githubusercontent.com/carla-simulator/scenario_runner/master/README.md) ·
[CARLA recorder](https://carla.readthedocs.io/en/latest/adv_recorder/) —
CommonRoad: [paper](https://mediatum.ub.tum.de/doc/1379638/776321.pdf) ·
[io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html) ·
[arXiv:2305.10080](https://arxiv.org/pdf/2305.10080) —
Config mgmt: [kustomize glossary](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/) ·
[eschewed features](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/) ·
[k8s deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/) ·
[CUE configuration](https://cuelang.org/docs/concept/how-cue-enables-configuration/) ·
[toml.io](https://toml.io/en/) ·
[StrictYAML typing](https://hitchdev.com/strictyaml/why/implicit-typing-removed/) —
FHWA: [TAT III ch. 6](https://ops.fhwa.dot.gov/publications/fhwahop18036/chapter6.htm) ·
[TAT III intro](https://ops.fhwa.dot.gov/publications/fhwahop18036/introduction.htm) ·
[FAQ](https://ops.fhwa.dot.gov/trafficanalysistools/faq.htm) ·
[Carolina Crossroads memo](https://www.scdotcarolinacrossroads.com/FEIS-documents/App_D_Alternatives_Traffic_Analysis_Technical_Memo_part_1.pdf) ·
[STRIDE report](https://www.eng.ufl.edu/stride/wp-content/uploads/sites/153/2021/09/STRIDE-Project-D-Final-Report-Manjunatha-updated.pdf) —
Scenario levels: [ar5iv:1905.03989](https://ar5iv.labs.arxiv.org/html/1905.03989) ·
[doria.fi thesis](https://www.doria.fi/bitstream/handle/10024/193629/asif_ammara.pdf?sequence=2&isAllowed=y)
