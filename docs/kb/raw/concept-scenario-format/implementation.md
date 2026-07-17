# Mechanics: Scenario Format

> Source: web research (greenfield — no scenario format exists yet; this file
> collects the *mechanisms* a scenario format is built from, to be re-audited
> against real code once a scenario ADR lands and the loader exists)
> | Researched: 2026-07-16 | Git HEAD: ae75fba

## 1. The composition pattern: one manifest referencing typed part files

Four of five surveyed simulators converge on the same shape: a small
configuration document whose main job is to *point at* content files.

- **SUMO**: a `.sumocfg` `<configuration>` whose `<input>` section holds
  `net-file` (network), `route-files` (demand), `additional-files`
  (detectors, traffic-light programs, TAZ); further sections cover
  `<time>` (begin/end), processing, routing, report, and random-number
  options ([SUMO modeling tutorial, arXiv:2304.05982](https://arxiv.org/html/2304.05982v2),
  [SUMO roadmap slides](https://cst.fee.unicamp.br/sites/default/files/sumo/sumo-roadmap.pdf)).
  The config is a pointer list; the content lives in the referenced files.
- **MATSim**: minimal scenario = `config.xml` + `network.xml` +
  `population.xml`; the config "builds the connection between the user and
  MATSim" and references the other two by path; files may be GZIP-compressed
  transparently (`.xml.gz`)
  ([MATSim book ch. 2, Rieser et al.](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
- **Aimsun Next**: the scenario object "sets the main input and output
  objects for a simulation": traffic demand, transit plan, path assignment
  plan, master control plan, real data set for validation, geometry
  configurations; minimum requirement = base network + traffic demand
  ([Aimsun scenarios docs](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html)).
- **Vissim** is the monolith counter-example: one `.inpx` XML document holds
  network, demand, and evaluation configuration ("The configuration is saved
  to the file .inpx",
  [PTV Vissim 2024 help](https://cgi.ptvgroup.com/vision-help/VISSIM_2024_ENG/Content/11_Auswertungen/Ausw_Ergebnisattr_konfig.htm)),
  with auxiliary files alongside (`.layx` layouts, `.weg`/`.bew` dynamic
  assignment files
  ([STRIDE report](https://www.eng.ufl.edu/stride/wp-content/uploads/sites/153/2021/09/STRIDE-Project-D-Final-Report-Manjunatha-updated.pdf),
  [PTV dynamic-assignment help](https://cgi.ptvgroup.com/vision-help/VISSIM_2025_ENG/Content/6_DynamischeUml/Statische_Routen_erzeugen.htm))).

**Mechanism takeaway:** manifest-of-parts is the field norm; the manifest
also carries run parameters (time window, seeds, step length). The tension:
a scenario shared as "just the .sumocfg" is useless without its parts — the
directory is the real unit.

## 2. Demand primitive A: explicit vehicles, trips, flows (SUMO's ladder)

SUMO's demand ladder runs from fully explicit to fully stochastic
([SUMO demand docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)):

- A **vehicle** = vType (physical + car-following parameters) + route
  (edge list, shareable by id) + departure. `<trip>` replaces the edge list
  with `from`/`to` (optionally `via`) and routes at runtime by fastest
  path; trips can also originate/terminate at traffic assignment zones
  (`fromTaz`/`toTaz`) or junctions.
- A **flow** emits repeated vehicles over `[begin, end)` with one of four
  spacing rules: `vehsPerHour` (equally spaced), `period` (equally spaced,
  or `period="exp(X)"` giving "exponentially distributed time gaps... a
  Poisson process with an expected value of X insertions per second"),
  `probability` p (Bernoulli emission each second), or `number` (fixed
  count, equally spaced).
- **Insertion policies** are first-class attributes: `departLane`
  (`first`/`random`/`free`/`allowed`/`best`/`best_prob`), `departSpeed`
  (`max`/`desired`/`speedLimit`/`last`/`avg`/`random`), `departPos`
  (`base`/`free`/`random_free`...); insertion is delayed if unsafe and
  `--max-depart-delay` can discard blocked vehicles.
- **Footgun:** route files must be sorted by departure time because SUMO
  streams them in `--route-steps` windows (default 200 s); unsorted files
  must be loaded as additional files (whole-file, higher memory) and
  "sumo may enter an infinite loop when given an unsorted route file with
  person definitions" ([same](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)).
- **Driver heterogeneity** lives in the vType: `speedFactor` distributions,
  default passenger `normc(1,0.1,0.2,2)` ≈ 95% of vehicles at 80–120% of
  the speed limit; vClass-specific deviations (trucks 0.05, rail 0.0).
  `--scale` multiplies demand globally, with per-vType override.

## 3. Demand primitive B: OD matrices compiled to trips (od2trips)

The field's standard handling of origin-destination demand is *compile-time
expansion*, not a runtime concept
([od2trips docs](https://sumo.dlr.de/docs/Demand/Importing_O%2FD_Matrices.html)):

- OD matrix cells = vehicle counts from TAZ to TAZ over a time period;
  od2trips converts them into concrete trip lists. TAZs are weighted
  source/sink edge lists whose probabilities are normalized after loading.
- Departure times are drawn **randomly within the cell's time interval**
  (`--spread.uniform` forces even spacing) — the stochasticity is resolved
  at authoring time, so the compiled output is a deterministic input.
- Time-sliced matrix formats exist in three dialects: `tazRelation`
  (intervals per vehicle type), Amitran (millisecond time slices), and the
  PTV V/O text formats (hour.minute periods, global factor).
- Day-shape profiles: `--timeline` splits one matrix by a piecewise share
  curve; `--timeline.day-in-hours` takes exactly 24 hourly shares. The docs
  ship German reference day curves (TGw2_PKW etc., Schmidt & Thomas 1996)
  and weekend scale factors (e.g. NRW highways Saturday 76.1% of weekday).
- Demand heterogeneous *across OD pairs* cannot be expressed as one matrix
  × one timeline: you call od2trips multiple times with `--prefix` and
  concatenate — i.e. heterogeneity is expressed by composing demand files,
  exactly the manifest-of-parts pattern again.
- `route2OD.py` inverts the direction (trips → matrix), useful for
  extracting demand structure from a recorded run.

## 4. Demand primitive C: turning movements and flow ratios

The intersection-scale alternative to OD thinking:

- **jtrrouter** builds routes from traffic volumes plus junction turning
  ratios (`--turn-defaults` defaults to "30,50,20");
  `--randomize-flows` randomizes departure times; `--discount-sources`
  subtracts upstream flow when inserting a new flow
  ([jtrrouter docs](https://sumo.dlr.de/docs/jtrrouter.html)).
- **Aimsun Traffic State**: "a set of flows at every input section in the
  network and a set of turn proportions at every junction for each Vehicle
  Type and Trip Purpose pair"; data requirement is observed section flows
  and turning proportions, and states "can vary across time intervals to
  model the changing patterns of flow during the day"
  ([Aimsun demand overview](https://docs.aimsun.com/next/24.0.3/UsersManual/DemandOverview.html)).
- **Vissim**: vehicle inputs (volumes per entry link) + static routing
  decisions with relative flows per branch; MDOT guidance mandates coding
  inputs in **15-minute demand increments** (hourly acceptable if volumes
  are consistent) with per-input truck percentages
  ([MDOT SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).
  Vissim's dynamic assignment can be frozen into the same static inputs
  ([PTV help](https://cgi.ptvgroup.com/vision-help/VISSIM_2025_ENG/Content/6_DynamischeUml/Statische_Routen_erzeugen.htm)).

**Mechanism takeaway:** three demand idioms — explicit lists, OD cells,
turning fractions — and every tool converts the latter two into the first
(or into per-entry flows). Time variation is universally **piecewise
constant** (time slices), never analytic functions.

## 5. Vehicle-type mix

- SUMO: `vTypeDistribution` samples types per emitted vehicle; per-flow
  `type`; vClass-scaled speed deviations (§2)
  ([SUMO demand docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)).
- Aimsun: demand is sliced by **User Class = Vehicle Type × Trip Purpose**;
  traffic states and OD matrices are per user class
  ([Aimsun demand overview](https://docs.aimsun.com/next/24.0.3/UsersManual/DemandOverview.html)).
- Vissim: vehicle compositions (relative shares of types per input); DOT
  practice codes truck percentages per input location
  ([MDOT SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).

## 6. The MATSim outlier: demand as agent day plans, metric in the config

MATSim replaces all of §2–§4 with a richer, heavier object
([MATSim book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)):

- `population.xml`: persons → plans (exactly one selected) → activities +
  legs (mode + computed route). Demand *is* the population; there are no
  flows or rates.
- After each mobsim run, executed plans are **scored** with the
  Charypar-Nagel utility; replanning mutates a share of agents' plans
  (typically ~10%, across route/departure-time/mode dimensions) and the
  loop iterates until scores stabilize
  ([MATSim book TOC, ch. 3](https://toc.library.ethz.ch/objects/pdf03/e01_978-1-909188-75-4_01.pdf),
  [ETH thesis summary](https://www.research-collection.ethz.ch/server/api/core/bitstreams/c2692f83-861a-4d66-b9db-c7fb761b736f/content)).
- Notably, the *scoring configuration lives inside config.xml*
  (`planCalcScore` activity types and typical durations) — a precedent for
  embedding the evaluation definition inside the scenario.
- Equilibrium comes from agent learning over iterations, not from seed
  sweeps over replications — a different statistical culture from
  microsimulation (§10).

## 7. Control configuration is a separate referenced object

- SUMO: `<tlLogic>` programs (phases with `duration` + signal-state
  strings) defined in additional files; default is a fixed 90 s cycle;
  actuated and NEMA-phase variants exist
  ([Traffic Lights docs](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html),
  [NEMA docs](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- Aimsun: the scenario selects a **Master Control Plan** containing
  per-junction Control Plans; the Synchro exporter reads signal phasing
  out of it ([Synchro importer docs](https://docs.aimsun.com/next/24.0.3/UsersManual/SynchroImporter.html)).
- Vissim: signal heads / priority rules are network objects; node
  evaluation auto-places queue counters "at the first signal head or
  priority rule stop line" ([Vissim user manual](https://pdfcoffee.com/vissim-user-manual-pdf-free.html)).

**Takeaway:** in every surveyed system, signal timing is its own part file
or object referenced by the scenario — never embedded in demand, never
embedded in the network geometry. (Representation of signal programs
themselves belongs to [[domain-signal-control]].)

## 8. Measurement declarations (the underrated part of a scenario)

How each system declares *what to measure*:

- **SUMO meandata**: `<edgeData>`/`<laneData>` elements inside an
  additional file declare measurement sets with aggregation `period`,
  `begin`/`end` windows, edge and vType filters, `excludeEmpty`,
  `minSamples`. Per interval they emit sampledSeconds, traveltime, density,
  occupancy, waitingTime, timeLoss, space-mean speed (slow vehicles weigh
  more — documented), entered/left/departed/arrived, lane-change counts,
  derived flow
  ([meandata docs](https://sumo.dlr.de/docs/Simulation/Output/Lane-_or_Edge-based_Traffic_Measures.html)).
- **Vissim**: the evaluation configuration (which result attributes to
  collect) is saved inside the `.inpx`; node evaluation auto-derives
  travel-time sections, delay segments, and queue counters per turning
  relation ([PTV help](https://cgi.ptvgroup.com/vision-help/VISSIM_2024_ENG/Content/11_Auswertungen/Ausw_Ergebnisattr_konfig.htm),
  [Vissim user manual](https://pdfcoffee.com/vissim-user-manual-pdf-free.html)).
- **Aimsun**: the scenario's "Outputs to Generate" tab steers a results
  database with per-object tables — MISYS (system), MISECT (section),
  MILANE, MITURN, MINODE, MIDETEC (detector), MISUBPATH
  ([output database docs](https://docs.aimsun.com/next/23.0.0/UsersManual/OutputDatabaseDefinition.html)).
- **MATSim**: metrics are post-processing over the events stream plus
  per-iteration link stats (hourly counts and travel times per link) and
  score statistics ([book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
- **OpenSCENARIO XML has no measurement concept at all** — CARLA's
  scenario_runner had to bolt pass/fail criteria on as `StopTrigger`
  `criteria_*` parameter conditions
  ([scenario_runner docs](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).
  The DSL sibling does add KPIs, checks and coverage metrics — but only
  for AV test cases ([ASAM comparison](https://www.asam.net/standards/detail/openscenario-xml/)).

**Takeaway:** SUMO and Vissim/Aimsun prove measurement declarations belong
*inside* the scenario/project definition; OpenSCENARIO proves the pain of
omitting them.

## 9. Variant and experiment mechanisms

- **Aimsun** has the only first-class hierarchy: scenario → experiments
  (component models, algorithms, parameters — "rather than the physical or
  infrastructural aspects") → replications/results, where the replication
  object "contains the random seed information for your simulation and
  then contains the outputs for this instance"; an optional average object
  aggregates replications; documented example runs 20 micro replications
  with distinct seeds. **Geometry configurations** are named alternative
  network variants the scenario can reference
  ([Aimsun scenarios docs](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html)).
- **FHWA methodology** (tool-independent): build baseline demand, then
  "create model sub-variants for each competing alternative", determine
  the required number of runs from output randomness, and apply a
  statistical test to the differences
  ([TAT Vol III 2019, ch. 6](https://ops.fhwa.dot.gov/publications/fhwahop18036/chapter6.htm),
  [introduction](https://ops.fhwa.dot.gov/publications/fhwahop18036/introduction.htm));
  one DOT application computed ≥10 runs for a 95% confidence interval on
  link speeds/flows ([Carolina Crossroads memo](https://www.scdotcarolinacrossroads.com/FEIS-documents/App_D_Alternatives_Traffic_Analysis_Technical_Memo_part_1.pdf)).
- **MATSim**: policy variants = separate config + input files, compared
  via separate output directories; no variant object.
- **SUMO**: no variant mechanism at all — copy the files, change `--seed`
  (default 23423) ([jtrrouter options](https://sumo.dlr.de/docs/jtrrouter.html)).
- **kustomize** (the config-management pole): a `kustomization.yaml`
  manifest lists *resources* (what exists), *generators* (what to create),
  *transformers* (what to change); a **base** + **overlay** produces a
  **variant**; overlays may stack; patches are strategic-merge or
  JSON6902 ([glossary](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/)).
  Deliberately **eschewed**: removal directives, globs, env-var-dependent
  builds, and `${VAR}` templating — "It's no longer data, it's now logic
  that must be compiled. Errors in the output are disconnected from the
  edit that caused it" ([eschewed features](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/)).
- **CUE** (the config-language pole): types are values; overrides are
  disallowed "so the location where a specific value originates is never
  in doubt"; order-independent unification merges constraints from
  multiple stakeholders ([CUE configuration guide](https://cuelang.org/docs/concept/how-cue-enables-configuration/)).

## 10. Serialization syntax trade-offs (through the diffability lens)

- **XML** (SUMO, MATSim, Vissim `.inpx`, OpenSCENARIO, CommonRoad):
  XSD-validatable, attribute-explicit, verbose. Even Vissim's
  proprietary-feeling `.inpx` is XML text — its root carries
  `<network version="603" vissimVersion="11.00 - 09">`
  ([sumo#5915](https://github.com/eclipse-sumo/sumo/issues/5915)).
- **YAML 1.2**: concise, comment-friendly, but implicit typing bites:
  unquoted `NO` parses as `false` (the Norway problem), `9.3` as float —
  "intended behavior according to the YAML 1.2 specification"
  ([StrictYAML rationale](https://hitchdev.com/strictyaml/why/implicit-typing-removed/)).
  Anchors/aliases create invisible long-range coupling inside a file.
- **TOML**: "minimal configuration file format that's easy to read due to
  obvious semantics... designed to map unambiguously to a hash table",
  with native datetimes ([toml.io](https://toml.io/en/)); deep nesting
  (nested tables of tables) gets unwieldy compared to YAML's indent style.
- **JSON**: the machine intersection of the above; no comments.
- Diff-friendliness correlates with one-value-per-line styles and with
  *sorted, stable ordering* of entities — the same discipline SUMO demands
  of route files (§2).

## 11. Versioning and schema evolution mechanisms

- **Version field in the file**: Vissim's `.inpx` root `version` attribute;
  older software reading newer files lists the attributes it cannot read
  and refuses them explicitly ([Vissim 2022 manual](https://pdfcoffee.com/manual-vissim-2022-4-pdf-free.html)).
- **Stability tracks + deprecation rules** (Kubernetes): API elements may
  only be removed by incrementing the API-group version (Rule #1); objects
  must round-trip between served versions without information loss
  (Rule #2); beta versions are deprecated no sooner than 9 months /
  3 releases ([deprecation policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/)).
- **Format epochs**: CommonRoad's reader accepts both the 2020a XML and
  the 2024 protobuf splits of a scenario — two coexisting serializations
  of one model ([CommonRoad io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html)).
- **Shipped migrations**: OpenSCENARIO's 0.9.x→1.0 migration is an XSLT
  stylesheet run with `xsltproc` ([scenario_runner docs](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).
- **Silent drift as the warning**: MATSim's "list of available parameters
  and valid parameter values may vary from release to release", with a
  fullConfig writer as the discovery mechanism
  ([book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).

## 12. Recording/replay artifacts and their relation to scenarios

- **SUMO state save/load**: `--save-state.times`/`--save-state.files`
  dump an XML snapshot, `--load-state` resumes; officially it "should only
  guarantee to work properly if you use the same options (including all
  input files!) you used when saving", and unfinished flows are not fully
  restorable ([sumo#7471](https://github.com/eclipse-sumo/sumo/issues/7471))
  — i.e. the snapshot is *not* self-contained; it implicitly depends on
  the scenario.
- **CARLA recorder**: server-side state log; replay is re-application with
  no re-execution determinism claim
  ([recorder docs](https://carla.readthedocs.io/en/latest/adv_recorder/),
  surveyed in [[arch-time-model]]).
- **Aimsun replication** = the run record: scenario + experiment + seed +
  outputs in one object (§9).
- **Our constraint (ADR-0005):** replay = JetStream stream of snapshot
  keyframes + arbitrated intent log + state CRC; (scenario, seed) is the
  run key. So a recording must minimally bind: scenario identity, seed,
  engine version, stream references. Whether a recording can *spawn* a new
  scenario (keyframe → initial state) is a design decision, not a given —
  SUMO's state-file caveats show the coupling traps.
