# Competitors: Demand Modeling

> Source: web research + codebase analysis | Researched: 2026-08-24

Named prior art for modeling "people coming from and going to locations with
weights that replicate the real world", and how each compares to
traffic-sim's approach (per-flow destination distributions authored by
generator scripts, sampled deterministically at runtime — see
[implementation.md](./implementation.md)).

## Competitive landscape

Demand modeling splits into four camps:

1. **Matrix compilers** — you bring an OD matrix (from a planning model or
   survey), the tool turns it into trips (SUMO od2trips, most commercial
   microsim).
2. **Counts-fitters** — you bring link/turn counts, the tool solves for the
   demand that reproduces them (SUMO dfrouter/routeSampler, Aimsun OD
   adjustment, Visum TFlowFuzzy).
3. **Activity-based synthesizers** — you bring census + travel surveys, the
   tool synthesizes a population and its daily activity chains (MATSim +
   eqasim, POLARIS, CMAP CT-RAMP, ActivitySim, SUMO activitygen).
4. **Land-use-anchored generators** — you bring buildings/zoning, trips are
   produced/attracted by land-use intensity (ITE trip rates in practice;
   traffic-sim's buildings.py/mkod.py sits here, alone in the OSS microsim
   world in using raw OSM building floor area directly).

traffic-sim today is camp 4 with a camp-1 grammar (per-flow rates and
weighted destinations, no matrix type) and none of camp 2's machinery. Its
distinctive constraints — replay determinism (demand is a pure function of
(definitions, seed)), generator-side authoring with the demand YAML as the
reviewable artifact, and lane-level (not TAZ-level) spatial resolution —
shape what can be stolen and what must be avoided.

---

## SUMO demand toolchain (Eclipse SUMO, DLR)

The closest architectural sibling: SUMO also keeps demand *generation* in
offline tools that emit demand files the simulator consumes.

- **od2trips** compiles O/D matrices (VISUM/VISSIM O-format, or TAZ-relation
  XML) into individual trips, mapping traffic-analysis zones to network
  edges via `.taz.xml` files. Departure times within a matrix time window
  are drawn uniformly/randomly.
  Source: [od2trips — SUMO Documentation](https://sumo.dlr.de/docs/od2trips.html),
  [Importing O/D Matrices](https://sumo.dlr.de/docs/Demand/Importing_O/D_Matrices.html).
- **jtrrouter** builds routes from flow definitions + junction turning
  ratios — no explicit destinations needed.
  Source: [Introduction to demand modelling in SUMO](https://sumo.sourceforge.net/docs/Demand/Introduction_to_demand_modelling_in_SUMO.html).
- **dfrouter** rebuilds vehicle amounts and routes directly from
  induction-loop measurements: classifies detectors as source/sink/between,
  computes routes between detectors, then flows. Assumes full detector
  coverage of sources/sinks and that source and sink sums match per
  interval; "works best on motorway networks", produces implausible routes
  in meshed city networks.
  Source: [Routes from Observation Points](https://sumo.dlr.de/docs/Demand/Routes_from_Observation_Points.html).
- **flowrouter.py** — dfrouter successor tolerating missing data; finds the
  route set maximizing total flow within the counts, with an
  implausible-route blacklist.
- **jtcrouter.py** — turn-counts → flows + turn ratios → jtrrouter.
- **routeSampler.py** — the counts-fitter that matters: given a *pool* of
  candidate routes (e.g. from randomTrips.py) plus edge counts, turn counts,
  and even OD-counts, it repeatedly selects routes (an integer linear
  program) to match the measurements. Whitelist approach: "generating a
  sufficient set of plausible routes is often easier than listing all
  implausible routes."
  Source: [Routes from Observation Points](https://sumo.dlr.de/docs/Demand/Routes_from_Observation_Points.html),
  [Tools/Turns](https://sumo.dlr.de/docs/Tools/Turns.html).
- **activitygen** — activity-based demand generator: from a population
  description (households, workplaces, schools by age/work status) it
  synthesizes person trips with activities (home → work → shop → home),
  i.e. a camp-3 tool living inside the SUMO suite.
  Source: [Activity-based Demand Generation](https://sumo.dlr.de/docs/Demand/Activity-based_Demand_Generation.html),
  [activitygen](https://sumo.dlr.de/docs/activitygen.html).

**vs this project:**
- Grammar: our `Flow` (rate + slices + poisson/constant) is exactly SUMO's
  `<flow>` (our doc comments cite `period="exp(X)"` equivalence,
  `engine/natsio/demand/director.go:451-454`). Our `slices` ≈ SUMO flows
  with begin/end. Our per-flow `destinations` map ≈ od2trips' TAZ-relation
  output but at *lane* resolution instead of zone resolution — od2trips
  maps whole TAZs to edges; our generator maps individual buildings to
  individual lanes (buildings.py). Nothing in SUMO's grammar pins a trip's
  end to a mid-network lane with arrival-despawn the way ADR-0021 does
  (SUMO vehicles stop at their route's last edge; trips there end at lane
  end too, but SUMO supports stops/parking areas for mid-edge trip ends —
  an open ADR-0021 deferral for us).
- **Steal:** routeSampler's whitelist-ILP pattern is the natural shape of
  our missing counts-fitter: our equivalent of its route pool is the set of
  (origin lane → destination lane) flows; the counts are IDOT/City ADT +
  portal volumes; the solver picks per-flow rates. This would replace
  hand-tuned `--total`/`--freeway-scale`/`--corridor-scale` sweeps.
- **Steal:** activitygen's *concept* (population → activity chains) as a
  far-future return-trips mechanism; not its implementation (zone-level,
  SUMO-network-coupled).
- **Avoid:** dfrouter's full-coverage assumption (our portal inventory will
  never be fully counted) and its source/sink mass-balance assumption per
  interval (ignores travel time — fatal on a 55k-lane crop where AM-peak
  accumulation is the whole point). Also avoid the TAZ abstraction itself:
  our lane-as-atom graph and building-level anchoring are strictly finer.

## MATSim (+ eqasim synthetic-population pipelines)

MATSim (ETH Zürich / TU Berlin, GPL, Java) is the open agent-based
flagship. Demand is not a file of flows: it is a **synthetic population of
agents, each with a daily plan** (activity chain: home–work–shop–home with
locations, modes, end times). A co-evolutionary loop iterates plan
scoring/selection against the network simulation until a user-equilibrium-
like state; plans are the demand *and* the behavior.
Source: [MATSim book (Ubiquity Press)](https://www.bookfusion.com/books/145400-the-multi-agent-transport-simulation-matsim),
[An overview of agent-based traffic simulators (arXiv:2102.07505)](https://arxiv.org/pdf/2102.07505).

The **eqasim** pipelines operationalize population synthesis from open data:
raw census + travel-survey inputs → pipeline → simulation-ready population.
Public pipelines exist for Île-de-France, California (any region in the
state), São Paulo, Switzerland, and others — **no public Chicago/Illinois
eqasim pipeline was found in this research** (open question below).
Source: [eqasim-org/california (GitHub)](https://github.com/eqasim-org/california),
[matsim-ile-de-france population docs](https://github.com/matsim-scenarios/matsim-ile-de-france/blob/develop/docs/population.md).

**vs this project:**
- MATSim's demand is **elastic by construction**: agents re-plan (route,
  mode, departure time) between iterations in response to congestion. We
  rejected demand-as-a-function-of-live-state outright (ADR-0028, composite
  of two separate sentences: "it breaks replay determinism (ADR-0005)" …
  "two arms of an A/B would no longer share a demand program"). Note the
  nuance: MATSim's elasticity is *between-day*
  (iteration to iteration, offline); within a run, plans are fixed. Our
  ADR-0036 adaptive routing is *within-day* — the two are complements, and
  MATSim's loop is exactly the outer calibration loop we would run by hand
  or tooling across runs.
- **Steal:** the eqasim *pipeline* architecture — versioned raw inputs →
  deterministic pipeline → demand artifact — is precisely our
  `scripts/chicago/` posture; adopting its stage discipline (input
  registry, stage outputs, run-to-run diffing) would formalize what
  mkod's header comments do ad hoc.
- **Steal:** plans-with-return-trips as the shape of any future activity
  chain support (ADR-0021's "evening reversal = transposed OD" is the
  primitive version).
- **Avoid:** full agent-based demand. It is a different product (policy
  questions about *people*, not *infrastructure arms*), incompatible with
  our (content-hash, seed) run identity unless the entire population file
  becomes a hashed part — at 10⁷ agents that is MATSim's `.xml.gz` habit,
  which ADR-0012 already refused for diffability reasons.

## Aimsun Next (commercial microsim)

Aimsun's demand practice is the commercial mainstream: an initial OD matrix
(from a 4-step model, an OD survey, or mobile-phone data) is **adjusted to
traffic counts** inside the tool:

- **Static OD Adjustment**: "adjusts an existing OD matrix, using traffic
  counts from detectors… bi-level model solved heuristically by a gradient
  algorithm, and it includes an assignment at each iteration."
  Source: [Static OD Adjustment — Aimsun Next Users Manual](https://docs.aimsun.com/next/23.0.0/UsersManual/StaticAdjustment.html).
- **Dynamic OD Adjustment**: per-time-interval adjustment against dynamic
  assignment paths, gradient descent per interval.
  Source: [Dynamic OD Adjustment](https://docs.aimsun.com/next/22.0.1/UsersManual/DynamicAdjustment.html).
- **Static OD Departure Adjustment**: slices a static matrix into
  time-varying demand while conserving total trips per OD pair (fractions
  sum to 1 over intervals).
  Source: [Static OD Departure Adjustment](https://www.fratar.com.br/manual-aimsun/UsersManual/DepartureAdjustmentAlgorithms.html).
- Calibration guidance pairs these with the GEH statistic and FHWA traffic
  analysis targets.
  Source: [Calibration and Validation — Aimsun Next User Manual](https://docs.aimsun.com/next/26.0.0/UsersManual/CalibrationAndValidationOfAimsunModels.html).

**vs this project:**
- **Steal:** the *bi-level loop* as protocol, not as code: adjust demand
  weights → simulate → compare to counts → repeat. Our inner loop is the
  full microsim (slower than their assignment, but ADR-0014/0030 metrics
  give the comparison layer); the outer loop can live in tooling exactly
  the way mkod sweeps are run today.
- **Steal:** departure adjustment's conservation constraint — time-slicing
  demand while preserving per-pair totals — as the rule any profile
  re-shaping should obey (our ADR-0028 library scales fractions of a peak;
  nothing conserves an authored daily total when shapes change).
- **Avoid:** in-tool matrix editing as the source of truth. Aimsun's
  adjusted matrix lives in the model file; our doctrine keeps the *recipe*
  (generator + inputs) as truth and the demand YAML as a derived artifact.
  The recipe survives re-import; the edited matrix does not.

## PTV Visum / VISSIM (commercial)

Visum's **TFlowFuzzy** is the field's canonical OD-matrix estimation
(ODME): iteratively correct a seed matrix against counted link/turn volumes
within fuzzy tolerances until assigned flows match counts (practitioners
run it "until a maximum of GEH became below 5"). It is the standard bridge
between a planning-model matrix and a microsim-ready demand.
Source: [PTV Visum ODME training](https://training.ptvgroup.com/en/courses/tr-t0155-ptv-visum-synthetic-matrix-estimation-odme-in-ptv-visum-us),
[Lessening bus journey times… TFlow-Fuzzy case study (CORE PDF)](https://core.ac.uk/download/pdf/6751565.pdf),
[PTV big-data/ODME blog](https://blog.ptvgroup.com/en/technologyplus/big-data-ptv-suite-for-large-scale-projects/).

**vs this project:** same lesson as Aimsun, plus one distinct steal —
**tolerances on counts**. TFlowFuzzy treats every count as a fuzzy target
with a confidence width; our hand calibration currently treats all anchors
(IDOT AADT, a cordon count, an ATRI speed) as equally hard. Weighting
anchors by reliability is cheap and honest.

## POLARIS (Argonne National Laboratory)

The Chicago-scale agent-based model: integrated activity-based demand +
dynamic traffic assignment, C++ open source, built and calibrated for the
20-county Chicago region — ≈10.4M travelers, ≈40.8M trips/day, ≈48k-link
network. Population synthesis from ADAPTS; destination and mode choice
models estimated on CMAP's 2007 Travel Tracker survey (10,552 households).
Run times of hours for full-region days on HPC.
Source: [POLARIS at ANL](https://vms.taps.anl.gov/tools/polaris/),
[POLARIS SDK paper (OSTI 1392074)](https://www.osti.gov/servlets/purl/1392074),
[Large-Scale Evaluation… Chicago Region Using POLARIS (arXiv:2403.14669)](https://arxiv.org/pdf/2403.14669),
[Planning Constrained Destination Choice in ADAPTS (WCTRS)](https://wctrs-society.com/wp-content/uploads/abstracts/lisbon/general/02922.pdf).

**vs this project:**
- POLARIS is the single most relevant demand prior art *for Chicago*: its
  calibrated destination-choice and time-of-day models are Chicago's own,
  estimated from Chicago's household survey. If its demand outputs
  (OD-by-purpose-by-time matrices or agent trip tables) are obtainable,
  they could seed a calibrated OD layer for our crops directly — the
  strongest alternative to building our own from LODES (see synthesis).
- **Steal:** destination choice *with a planning constraint* (choice-set
  formation before utility) — conceptually what our reachability filter +
  floor-area weights already approximate crudely.
- **Avoid:** the scope. POLARIS answers regional policy questions at
  link-level dynamics; we answer lane-level controller/infrastructure
  questions on cordon crops. Its demand resolution (TAZ-to-TAZ) is too
  coarse to inject directly; it can only *rate* our flows, not *place*
  them.

## CMAP's own modeling stack (the region's MPO)

CMAP runs **CT-RAMP**, a tour-based activity model over a 21-county area:
PopSynIII population synthesis (8 person types) → long-term location
choice (usual workplace/school) → mobility attributes (incl. *free-parking
eligibility for CBD workers* — the parking lever ADR-0039 defers!) → daily
activity patterns (mandatory/non-mandatory/home) → tour generation →
trip-level mode/time/parking → equilibrium highway + transit assignment.
Validation is public and thorough: journey-to-work flows vs CTPP, VMT
shares by county and facility type, link volumes with RMSE vs Florida DOT
targets, transit boardings within 4% by mode.
Source: [CMAP ABM report](https://cmap-repos.github.io/cmap_abm_report/).
CMAP also open-sources its **commercial services vehicle model** (freight
touring).
Source: [CMAP-REPOS/cmap_csvm (GitHub)](https://github.com/CMAP-REPOS/cmap_csvm).
Regional inputs (land-use inventory, trip-generation zones, community
snapshots) are on the CMAP Data Hub.
Source: [CMAP Data Hub dataset example (BTAA Geoportal record)](https://geo.btaa.org/catalog/4c75874452ab408092eab69ffca4948a_4).

**vs this project:**
- **Steal (the big one):** CT-RAMP's *validation target set* — JTW flows,
  VMT shares by facility type, RMSE-by-volume-bin, transit boardings — is
  a ready-made scorecard for "do our weights replicate the real world",
  and every one of its inputs is public. Our ADR-0030 run report already
  computes the sim-side VMT shares.
- **Steal:** free-parking eligibility as a first-class mobility attribute —
  direct support for the deferred parking-capacity demand model.
- **Steal:** the CMAP land-use inventory as the disambiguator for
  `building=yes` (the 71% residential blind spot) — it classifies parcels
  independently of OSM tagging.
- **Avoid:** nothing structural; CT-RAMP is a different resolution tier.
  Access caveat: model *outputs* (matrices) are not all public; the
  published report is aggregate.

## ActivitySim (open ABM platform)

Consortium-led open-source (Python) activity-based model used by many US
MPOs; synthesizes daily activity plans for synthetic agents and is the de
facto successor platform to bespoke CT-RAMP installations.
Source: [ActivitySim](https://activitysim.github.io/activitysim/),
[ActivitySim white paper (RSG PDF)](https://rsginc.com/wp-content/uploads/2025/10/ActivitySim-White-Paper-2022.pdf).
Related: LBNL's BEAM consumes ActivitySim demand for its agent-based
simulations ([BEAM CORE overview, LBNL PDF](https://eta-publications.lbl.gov/sites/default/files/beam_core_overview_calibration_and_validation_summary_report_v5.pdf)).

**vs this project:** an alternative *source* of synthetic-population demand
if a Chicago ActivitySim implementation appears (none public found); the
same "rate our flows, don't place them" caveat as POLARIS applies.

---

## Positioning summary

| Tool | Demand form | Spatial grain | Fitted to counts? | Elastic demand? | Open? | Take |
|---|---|---|---|---|---|---|
| SUMO od2trips/jtrrouter | OD matrix / flows+turn ratios | TAZ→edge | no | no | EPL-2.0 | grammar parity (we match) |
| SUMO dfrouter/flowrouter/jtcrouter | detector-derived routes | detector points | yes (assumes coverage) | no | EPL-2.0 | avoid coverage assumption |
| SUMO routeSampler | route pool + counts → ILP | edge-level | yes | no | EPL-2.0 | **steal the pattern** |
| SUMO activitygen | population → activity trips | zone/building-ish | no | no | EPL-2.0 | concept for return trips |
| MATSim + eqasim | agent plans (activity chains) | address/facility | validated, not fitted | yes (between-day) | GPL | pipeline discipline; avoid scope |
| Aimsun OD adjustment | seed matrix + counts, bi-level | section | yes | no | commercial | bi-level protocol; GEH |
| Visum TFlowFuzzy | seed matrix + fuzzy counts | link/turn | yes | no | commercial | count tolerances |
| POLARIS (ANL) | activity-based, Chicago-calibrated | TAZ | validated on Travel Tracker | yes | open source | **Chicago demand source candidate** |
| CMAP CT-RAMP / csvm | tour-based ABM + freight | subzone/parcel | yes (public scorecard) | yes | mixed/open parts | validation targets; land-use inventory |
| **traffic-sim** | per-flow rates + weighted destinations, building-anchored | **individual lane / building** | **not yet (napkin-anchored)** | **no (by design, ADR-0028)** | MIT/Apache posture | — |

The empty cell in our row — "fitted to counts: not yet" — is the whole
topic. Every mature toolchain above closes that loop; routeSampler (OSS,
edge-level, whitelist-ILP) is the lightest pattern that fits our
architecture and determinism constraints.
