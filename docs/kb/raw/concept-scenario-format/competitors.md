# Prior Art Survey: Scenario Format

> Source: web research | Researched: 2026-07-16
> "Competitors" here = systems whose scenario-format choices we can steal
> from or be warned by: microscopic traffic simulators, AV scenario
> standards, and — because our differentiator is *diffable variants* — the
> configuration-management toolchain (Kubernetes/kustomize, CUE) that has
> spent a decade on exactly that problem.

## Traffic / driving simulators

### SUMO — manifest + typed XML parts; diffable in principle, no variants

- Composition: `.sumocfg` manifest pointing at `.net.xml` (network),
  `.rou.xml` (demand), `.add.xml` (detectors, traffic-light programs,
  TAZ) ([arXiv:2304.05982](https://arxiv.org/html/2304.05982v2)).
- All files are hand-editable XML with published XSDs; "Almost every file
  used in SUMO package is encoded in XML... SUMO-specific, not following
  any standard" ([roadmap slides](https://cst.fee.unicamp.br/sites/default/files/sumo/sumo-roadmap.pdf)).
- Demand ladder: explicit vehicles/trips → flows (constant, Poisson via
  `period="exp(X)"`, per-second probability) → OD matrices compiled by
  od2trips → turning ratios routed by jtrrouter
  ([demand docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html),
  [od2trips](https://sumo.dlr.de/docs/Demand/Importing_O%2FD_Matrices.html),
  [jtrrouter](https://sumo.dlr.de/docs/jtrrouter.html)).
- Metrics are declared in-scenario via meandata `<edgeData>`/`<laneData>`
  measurement sets (period, windows, filters) in additional files
  ([meandata docs](https://sumo.dlr.de/docs/Simulation/Output/Lane-_or_Edge-based_Traffic_Measures.html)).
- Variants: **none built in** — copy files, edit, change `--seed`
  (default 23423) ([jtrrouter options](https://sumo.dlr.de/docs/jtrrouter.html)).
  Third-party practice is templating scripts outside the tool.
- State save/load exists but is explicitly guaranteed only with the same
  input files ([sumo#7471](https://github.com/eclipse-sumo/sumo/issues/7471)).
- **vs traffic-sim (us):** the file decomposition (net/rou/additional +
  manifest) and the meandata pattern are worth copying almost verbatim;
  the missing variant mechanism and the sorted-file/streaming footguns
  are exactly what our diffability requirement must fix.

### MATSim — demand as agent day plans; the metric *is* in the config

- Minimal scenario = config.xml + network.xml + population.xml, all XML,
  transparently GZIP-compressed (`.xml.gz`)
  ([book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
- Demand = full day plans per agent (activities + legs); no flows, no
  rates — expressiveness is maximal (activity chains) and minimal (rates)
  at the same time.
- Evaluation is embedded in the scenario: the `planCalcScore` config
  section defines activity types/typical durations used to score plans;
  replanning mutates ~10% of agents' plans per iteration until the score
  distribution stabilizes
  ([book TOC ch. 3](https://toc.library.ethz.ch/objects/pdf03/e01_978-1-909188-75-4_01.pdf),
  [ETH thesis](https://www.research-collection.ethz.ch/server/api/core/bitstreams/c2692f83-861a-4d66-b9db-c7fb761b736f/content)).
- Outputs are run artifacts: per-iteration events files, link stats
  (hourly counts/travel times per link), score stats, all under an
  output directory with `ITERS/it.N` subfolders
  ([book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
- Variants = separate config + inputs; comparison = separate output dirs.
  Config parameters "may vary from release to release" (silent drift).
- **vs us:** we don't need agent-day-plan expressiveness (no mode choice,
  no activities — yet), but "the score definition ships inside the
  scenario" is a direct precedent for embedding our metric definitions.
  The `.xml.gz` habit is an anti-model: compressed primary sources can't
  be diffed or reviewed.

### PTV Vissim — monolithic inpx; evaluation config inside the same file

- One `.inpx` XML document holds network, demand, and evaluation
  configuration ("The configuration is saved to the file .inpx",
  [PTV help](https://cgi.ptvgroup.com/vision-help/VISSIM_2024_ENG/Content/11_Auswertungen/Ausw_Ergebnisattr_konfig.htm));
  root tag carries `version` + `vissimVersion`
  ([sumo#5915](https://github.com/eclipse-sumo/sumo/issues/5915)).
- Demand: vehicle inputs per entry link + static routing decisions with
  relative flows, coded in 15-minute demand increments per MDOT guidance,
  with per-input truck percentages
  ([MDOT SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf));
  or OD matrices via the dynamic-assignment module, freezable into static
  inputs ([PTV help](https://cgi.ptvgroup.com/vision-help/VISSIM_2025_ENG/Content/6_DynamischeUml/Statische_Routen_erzeugen.htm)).
- Measurement objects are first-class network elements: travel-time
  sections, data collection points, queue counters, node evaluation
  (which auto-derives the former per turning relation)
  ([Vissim user manual](https://pdfcoffee.com/vissim-user-manual-pdf-free.html)).
- Versioning: newer-version attributes are listed and refused by older
  software ([Vissim 2022 manual](https://pdfcoffee.com/manual-vissim-2022-4-pdf-free.html)).
- Institutionalized seed sweeps: same seed + same inputs → identical
  results; DOT practice ~10 seeds averaged
  ([microsimulation.pub](https://www.microsimulation.pub/articles/00219) —
  via [[arch-time-model]]).
- **vs us:** proof that evaluation-in-scenario is industry standard, and
  the monolith is the cautionary tale — one giant XML in a GUI tool,
  practically undiffable even though it's text. Our (scenario, seed) run
  key is their practice formalized (already in ADR-0005).

### Aimsun Next — the only first-class experiment hierarchy

- One traffic network document (`.ang`) contains everything; on top of it:
  **scenario** (demand + transit + control plan + geometry config + real
  data set) → **experiments** (algorithms/parameters, many per scenario)
  → **replications/results** (each "contains the random seed information
  for your simulation and then contains the outputs for this instance"),
  with an optional average object across replications
  ([scenarios docs](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html)).
- Demand is two-idiom: **Traffic States** (input-section flows + turn
  proportions per junction, per Vehicle Type × Trip Purpose, varying over
  time intervals) or **OD matrices** with route choice (SRC/DUE)
  ([demand overview](https://docs.aimsun.com/next/24.0.3/UsersManual/DemandOverview.html)).
- **Geometry configurations** = named alternative network variants — the
  closest thing in the field to our "upgrade variant" (add a lane, change
  a junction) as a first-class, referenceable object.
- Outputs to a results database with per-object tables (MISYS, MISECT,
  MILANE, MITURN, MINODE, MIDETEC, MISUBPATH)
  ([output DB docs](https://docs.aimsun.com/next/23.0.0/UsersManual/OutputDatabaseDefinition.html)).
- Documented worked example: two experiments (meso DUE, micro SRC) with
  20 seeded replications on one scenario
  ([scenarios docs](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html)).
- **vs us:** the scenario/experiment/replication split and geometry
  configurations are the strongest prior art for variant-first design —
  but it all lives in a GUI document, not diffable text; experiments mix
  "what to run" with "how to run it", whereas our overlay should patch
  only declarative content.

## AV scenario standards

### ASAM OpenSCENARIO — the maneuver-level standard; wrong altitude for us

- XML schema (`.xosc`) describing *dynamic content*: a storyboard of
  stories/acts/sequences with trigger conditions and actions (speed
  change, lane change, route assignment); current version 1.4.0
  (released 19 May 2026)
  ([ASAM datasheet](https://www.asam.net/standards/detail/openscenario-xml/)).
- Reuse via **catalogs** (maneuvers, vehicles, trajectories,
  environments) and **parameterization** of complete scenario
  descriptions "which allows test automation without the need to create a
  large amount of scenario files" ([same](https://www.asam.net/standards/detail/openscenario-xml/)).
- Pairs with OpenDRIVE (road network) and OpenCRG (surface); authored by
  a 26-member consortium (BMW, CARIAD, dSPACE, AVL, Bosch, Five AI,
  Foretellix, MathWorks, Tencent, Volvo...) ([same](https://www.asam.net/standards/detail/openscenario-xml/)).
- Honest determinism disclaimer: "simulation results will not necessarily
  be the same on different simulators" ([same](https://www.asam.net/standards/detail/openscenario-xml/)).
- **No demand, no congestion metrics, no signal timing**: evaluation
  criteria had to be bolted on by CARLA as `criteria_*` StopTriggers
  ([scenario_runner docs](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).
- The sibling **DSL** (OpenSCENARIO 2.0) is a full programming language
  for abstract test spaces with KPIs/checks/coverage — expressive but
  "designed as V&V programming language", i.e. code, not data
  ([ASAM comparison table](https://www.asam.net/standards/detail/openscenario-xml/)).
- Migration by shipped XSLT stylesheets (0.9.x→1.0)
  ([scenario_runner docs](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).
- **vs us:** catalogs + parameterization are a proven reuse mechanism for
  entity definitions (our vTypes/controllers could borrow it), but the
  storyboard answers "what does the ego's neighbor do at t=12.3" — not
  "what demand loads this corridor for a week". Its heaviness for our
  use is precisely the DSL lesson: the moment scenarios become programs,
  they stop being diffable data.

### CARLA scenario_runner — standard consumer + Python escape hatch

- Executes OpenSCENARIO 1.x (partial coverage table: many ✅ but
  SynchronizeAction, VisibilityAction, EntitySelection ❌) and also
  Python-defined scenarios; pass/fail criteria injected as StopTrigger
  parameter conditions ([openscenario support](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).
- Release-versioned against CARLA releases in lockstep (0.9.16 ↔ 0.9.16)
  ([README](https://raw.githubusercontent.com/carla-simulator/scenario_runner/master/README.md)).
- **vs us:** a scenario layer that allows arbitrary Python is an escape
  hatch that destroys reviewability — the same failure mode kustomize
  documented for templating. Our controllers run over NATS instead, so
  the scenario format never needs a code escape hatch
  ([[concept-vehicle-controller-interface]]).

### CommonRoad — single-file scenario + benchmark discipline

- One XML file per scenario: lanelet network + static/dynamic obstacles
  (trajectories, occupancy sets, or probability distributions) + the ego
  vehicle's *planning problem* (initial state, goal regions); meta
  information includes benchmark ID, location, tags
  ([CommonRoad paper](https://mediatum.ub.tum.de/doc/1379638/776321.pdf),
  [arXiv:2305.10080](https://arxiv.org/pdf/2305.10080)).
- Versioned format epochs: 2020a single-XML vs 2024 protobuf split files;
  official readers accept both
  ([commonroad-io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html)).
- **vs us:** tags/metadata + a curated benchmark suite is how scenarios
  become comparable across research groups — cheap to adopt (a `tags:`
  list in our manifest). The "planning problem" embedded in the scenario
  is the AV analog of embedding our metric definitions.

## Configuration management (the variant machinery prior art)

### Kubernetes kustomize — base/overlay/variant as first-class objects

- A **kustomization** (manifest + referenced files) composes resources,
  generators, transformers; an **overlay** references a **base** and
  patches it; the result is a **variant**; overlays can stack and can
  themselves be bases; conventional layout = `base/` + `overlays/{dev,
  staging, prod}` ([glossary](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/)).
- Two patch dialects: strategic-merge patch (partial YAML doc, replaces
  by default, `delete` directive) and JSON6902 (RFC 6902 operation list);
  CRDs fall back to JSON merge patch semantics ([same](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/)).
- Hard-won restrictions: **no removal directives** (compose by addition
  or fork the base), **no `${VAR}` templating** ("It's no longer data,
  it's now logic"), no globs, no env-var-dependent builds — everything
  explicit so the whole config lives reviewably in git
  ([eschewed features](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/)).
- **vs us:** this is the mechanism our "baseline + N upgrade variants"
  wants: baseline scenario as base, each variant an overlay with
  JSON-merge-ish patches and added part files, materialized at load.
  Their eschewed-features list is a ready-made design-constraint list.

### CUE — configuration as constraints

- Types and values are unified; validation (`cue vet`) can be adopted
  incrementally against existing YAML/JSON; **overrides are disallowed**,
  "so the location where a specific value originates is never in doubt";
  unification is order-independent ([CUE guide](https://cuelang.org/docs/concept/how-cue-enables-configuration/)).
- **vs us:** attractive end-state for schema + cross-field constraints
  (e.g. "flow end > begin", "signal green splits sum to cycle"), but a
  full language runtime is heavy for v1; a JSON-Schema-style validator
  over strict YAML captures most of the safety with a fraction of the
  tooling. Cite as the upgrade path, not the choice.

## Positioning Summary

| System | Composition | Syntax | Demand model | Variants | Metrics in scenario | Run record |
|---|---|---|---|---|---|---|
| SUMO | manifest + part files | XML/XSD | vehicles/trips/flows; OD & turns compiled | none (copies + `--seed`) | meandata in additional file | state XML (needs same inputs) |
| MATSim | config + 2+ files | XML(+gz) | agent day plans | copies + output dirs | scoring config in config.xml | output dir w/ ITERS |
| Vissim | one `.inpx` monolith | XML (GUI-authored) | inputs + routing decisions; OD via assignment | none (file copies); seed sweep practice | evaluation config inside `.inpx` | result attrs in file/db |
| Aimsun | `.ang` document + objects | binary-ish GUI doc ⚠ verify | traffic states / OD matrices | **scenario→experiment→replication**; geometry configs | outputs-to-generate per scenario | replication object (seed + outputs) |
| OpenSCENARIO | single `.xosc` + catalogs | XML/XSD | none (maneuver triggers) | parameterization + catalogs | **none** (criteria bolted on) | tool-specific |
| CommonRoad | single XML (+ meta/tags) | XML/XSD (2020a), protobuf (2024) | obstacle trajectories | benchmark suite curation | planning problem embedded | — |
| kustomize | base + overlays | YAML | — | **overlays → variants** | — | — |
| **traffic-sim (us, proposed)** | manifest + part files (+ overlays) | strict YAML ⚠ decide | flows/rates (+OD & turns compiled) | overlay variants, first-class | measurement sets declared in scenario | JetStream stream + manifest (ADR-0005) |

⚠ Aimsun `.ang` encoding was not verified in this research (GUI document;
diffability unknown — flagged in Open Questions).
