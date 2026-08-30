# Synthesis: Demand Modeling

> Researched: 2026-08-24 | Git HEAD: 2bc98de | Status: complete

## Summary

traffic-sim's demand machinery is architecturally sound and unusually
disciplined — deterministic sampling, replay-proof verbs, fail-loud
validation, generator-side authoring — but its *weights* are declared
napkin math at every layer: portal rates by OSM class scaled to a hand-set
total, destinations proportional to OSM building floor area with **no
distance deterrence and no mode split**, temporal profiles from "the usual
diurnal shapes". The KB's two recorded calibration gaps (expressways run
free — Kennedy 72.1 km/h vs a real 19.1 mph; 71% of building footprints
are bare `building=yes`) are symptoms of one root condition documented
throughout: **nothing in the demand pipeline is fitted to an observed
count**. The field's mature answer is the counts-fitting loop (SUMO
routeSampler, Aimsun/Visum ODME, the entropy-maximization literature), and
the right empirical anchors for Chicago specifically are all public:
Census LEHD/LODES home↔work flows, IDOT + City of Chicago ADT counts, CTA
ridership as the mode-share residual, and TGSIM on the Kennedy itself.
The decision space below is about closing that loop without breaking the
determinism and authoring doctrines that make the current system
trustworthy.

## Source Files

- [Implementation trace](./implementation.md) — engine machinery, Flow
  grammar, director sampling, ADR-0021 OD mechanics, the Chicago generator
  stack, ADR-0039 gate, recorded gaps
- [Competitor analysis](./competitors.md) — SUMO toolchain, MATSim/eqasim,
  Aimsun, Visum TFlowFuzzy, POLARIS, CMAP CT-RAMP, ActivitySim
- [Standards & patterns](./standards-and-patterns.md) — four-step model,
  ITE/NHTS, gravity/radiation/IO, LODES/counts/CTA access notes, OD
  estimation from counts, temporal profiles, GEH/screenline validation

Key external sources: [SUMO Routes from Observation Points](https://sumo.dlr.de/docs/Demand/Routes_from_Observation_Points.html) ·
[od2trips](https://sumo.dlr.de/docs/od2trips.html) ·
[activitygen](https://sumo.dlr.de/docs/Demand/Activity-based_Demand_Generation.html) ·
[Aimsun Static OD Adjustment](https://docs.aimsun.com/next/23.0.0/UsersManual/StaticAdjustment.html) ·
[PTV Visum ODME](https://training.ptvgroup.com/en/courses/tr-t0155-ptv-visum-synthetic-matrix-estimation-odme-in-ptv-visum-us) ·
[POLARIS Chicago (arXiv:2403.14669)](https://arxiv.org/pdf/2403.14669) ·
[CMAP ABM report](https://cmap-repos.github.io/cmap_abm_report/) ·
[eqasim California](https://github.com/eqasim-org/california) ·
[ActivitySim](https://activitysim.github.io/activitysim/) ·
[Census LEHD/LODES](https://lehd.ces.census.gov/data/#lodes) ·
[IDOT AADT (BTAA record)](https://geo.btaa.org/catalog/fe639dd2b39c4ef68e0f9973bdc6737a_0) ·
[City of Chicago ADT](http://catalog.data.gov/dataset/average-daily-traffic-counts) ·
[CTA ridership](http://catalog.data.gov/dataset/cta-ridership-daily-boarding-totals) ·
[TGSIM](https://catalog.data.gov/dataset/third-generation-simulation-data-tgsim) ·
[NextGen NHTS OD](https://nhts.ornl.gov/od/) ·
[Lenormand 2016 trip-distribution comparison](https://www.sciencedirect.com/science/article/abs/pii/S0966692315002422) ·
[Abrahamsson 1998 OD-from-counts survey](https://pure.iiasa.ac.at/id/eprint/5627/1/IR-98-021.pdf) ·
[Aimsun calibration & GEH](https://docs.aimsun.com/next/26.0.0/UsersManual/CalibrationAndValidationOfAimsunModels.html)

---

## Key Architectural Decisions (candidates)

### 1. Keep realism out of the scenario grammar; the generator eats the world

**Choice:** Hold the line ADR-0021 §5 and ADR-0028 drew: the Flow grammar
(origin, rate/slices, spacing, vtypes, destinations, offset_m) is
*sufficient* for every demand model considered here, including a
LODES-anchored one. LODES matrices, mode shares, deterrence functions,
count tolerances, and profile libraries all belong in **generator tooling
and generator inputs**, never in `engine/scenario/` or the engine.
**Why:** the grammar already carries weighted destinations per flow
(`engine/scenario/scenario.go:131-136`) — an OD matrix is representable as
one flow per origin with a distribution, so a matrix type adds a second,
competing demand form (and ADR-0012 §3 already refused an "OD-matrix
compile step"). The generator-side precedent is proven: ADR-0028 added
named temporal shapes with "no engine, schema or contract
change". Keeping the recipe outside the engine preserves (a) replay
determinism — the engine only ever sees sampled primitives; (b) the
content hash as run identity — demand realism changes re-hash scenarios,
engine builds do not; (c) the reviewable artifact — a 136k-line demand
YAML with a self-describing header is diffable in git, a matrix blob is
not.
**Trade-off:** the recipe itself is *not* hashed (only its output), so two
generators producing byte-identical YAML are indistinguishable — accepted
already by ADR-0012; mitigated by mkod's knob-recording header. And every
new realism input (LODES table, count layer) needs a home under
`data/` with provenance, growing the recipe-not-file discipline the OSM
import already practices (ADR-0009 ODbL posture).
**Field context:** identical to SUMO's toolchain philosophy (od2trips,
routeSampler, activitygen are all offline emitters) and eqasim's pipeline
architecture ([competitors.md](./competitors.md)); the hash-pinned
artifact is our addition.

### 2. A LEHD/LODES-anchored OD generator as mkod's successor

**Choice:** Build `scripts/chicago/mklodes.py` (or mkod v2): pull LODES
OD/RAC/WAC for the crop's blocks, aggregate to buildings.py's access lanes
and mkzones' districts, and derive (a) residential origin rates from RAC
(residents by home block), (b) workplace destination weights from WAC
(jobs by work block), (c) the distribution itself from the LODES OD table
(home-block → work-block flows), keeping buildings.py for the
block→lane snapping it already does well. Mode-share and auto-occupancy
factors enter as explicit, documented exogenous inputs (CTPP/NHTS/CMAP),
because LODES counts jobs, not car trips.
**Why:** it replaces the three weakest proxies at once — floor area as
production (71% of footprints are uninformative `building=yes`),
floor area as attraction (Willis Tower takes the 3-storey default —
`scripts/chicago/buildings.py:76-81`), and zero impedance (LODES gives
the empirical home→work distance distribution to calibrate deterrence
against). It is the only public dataset that answers "people coming from
and going to *locations*" at sub-crop granularity for Chicago. CMAP's own
ABM validates its work flows against the same Census lineage (CTPP): both
modeled and observed show ~45% of work trips in the extended modeling
area occurring entirely within Cook County
([CMAP ABM report](https://cmap-repos.github.io/cmap_abm_report/)).
**Trade-off:** LODES is *partially synthetic* (noise-infused; block cells
are estimates — aggregate to districts before trusting), covers only
home↔work (no shopping/school/escort purposes — freight and non-work need
other anchors, e.g. CMAP's csvm for trucks), and lags current development
by ~2 years. It also makes the demand US-specific in tooling (fine —
`scripts/chicago/` already is; the engine grammar stays universal).
**Field context:** this is the land-use-anchored-generator camp grown up:
same architecture as today, counts instead of proxies. POLARIS/CT-RAMP
show the full ABM version; we deliberately take only the *marginals and
the matrix*, not the agent model.

### 3. Per-portal freeway demand anchoring (closing the Kennedy gap)

**Choice:** Replace the hand-tuned `--freeway-scale`/`--corridor-scale`
sweeps with **count-anchored portal rates**: each boundary portal (and
each ramp-share interior injection point) gets a target rate derived from
IDOT AADT × K-factor × directional split (continuous-count stations where
available; TGSIM per-lane rates as the cross-check), with
`--corridor-scale` demoted to a documented scenario-experiment knob.
**Why:** the gap is structural and measured: one global scalar sets every
class by the same factor, so at `--total 16000` the Kennedy's portals get
337 veh/h/lane — "reaching the class rate would need --total ≈ 67,000,
which buries the grid" (`docs/kb/articles/gaps-and-roadmap.md:191-199`).
The manual fix already proved the concept (freeway ×4.15 → Eisenhower
within 4% of its real ATRI truck speed;
`data/scenarios/chi-loop-od-30m/README.md:97-136`) and
`profiles-am-kennedy.json` proves per-corridor profile targeting works.
Counts at the *portal* are the natural anchor because boundary inflow is
the one place our demand has a directly observable real-world counterpart.
**Trade-off:** AADT is a daily figure on a different day-type than our
AM-peak scenario; K/D factors and day-type conversion add assumptions that
must ride in the scenario README. Counts also exist for the *un-cropped*
road — a portal's real volume includes trips that our crop cannot serve
(our through-share handles this today; the anchor should fit inflow to
counts *after* through-share, another small loop). Ramp metering on the
real Kennedy/Dan Ryan means observed mainline volumes are
metered-downstream values — anchor to *demand*, not discharge, or the sim
will under-load.
**Field context:** this is the dfrouter/routeSampler idea (demand from
counts) scoped to where counts exist, avoiding dfrouter's full-coverage
assumption; Visum/Aimsun practice anchors matrices to counts the same
way ([competitors.md](./competitors.md)).

### 4. A weight-calibration protocol (how we would *know* the weights replicate the real world)

**Choice:** Define a standing scorecard, run by tooling over a run
report + demand file, with four reconciliations and explicit thresholds:
(a) **screenlines** — simulated vs observed volumes over a small set of
counted screenlines (IDOT freeway AADT ×K, City camera ADT on arterials),
GEH < 5 on ≥85% as the field-standard bar; (b) **mode-share residual** —
CBD-bound person demand implied by LODES/CTPP minus CTA boardings
(daily/station-entry series) ≈ the auto demand the file should carry into
the core districts; (c) **speed distributions** — ADR-0030 corridor
speed bands vs published anchors (ATRI truck speeds already used
informally in `data/scenarios/chi-loop-od-30m/README.md:61-75`; TGSIM on
the Kennedy for within-crop truth); (d) **VMT/facility shares** — run
report VMT shares by class/district vs CMAP ABM's published VMT shares by
facility type as a coarse regional prior. Fail/pass printed per arm;
thresholds recorded in the scenario README.
**Why:** today calibration is a sequence of one-off README brackets; the
KB records at least three instances where means hid distribution failure
(ADR-0030's founding context). The four checks are independent in the
right way: (a) pins volumes, (b) pins the mode story, (c) pins
congestion *where it should be*, (d) pins spatial spread. GEH/screenline
is the WebTAG/Aimsun acceptance vocabulary; VMT shares is CMAP's own
validation tab.
**Trade-off:** screenline counts on our exact crop geometry need a
lane→count-station matching pass (buildings.py's snapping machinery
reused); GEH on Poisson-sampled microsim volumes needs multiple seeds to
mean anything (the paired-seed protocol already exists, ADR-0014). And a
pass is a *snapshot fit*, not a generalization guarantee — the protocol
should hold out at least one corridor from fitting and check it blind.
**Field context:** FHWA Toolbox Vol. III / TMaC process, WebTAG M3
criteria, CMAP ABM's public validation tabs
([standards-and-patterns.md](./standards-and-patterns.md) §7).

### 5. Temporal profile authoring from observed profiles

**Choice:** Keep the ADR-0028 library *format* exactly as is; re-author
the *fractions* from data: IDOT continuous-count hourly profiles for
freeway shapes, City camera time series for arterial shapes, NHTS
time-of-day by purpose for commute/freight/reverse splits. Add one
conservation rule to the authoring doc: when a profile is re-shaped, the
flow's integrated vehicle total is what downstream comparisons quote
(already mkod's convention — make it the library's contract).
**Why:** the current fractions are explicitly "napkin-anchored… NOT
calibrated" (`scripts/chicago/profiles-am.json:9-11`), and shape is
load-bearing — the kennedy-am plateau exists because "a 10-minute peak
cannot build a queue, a 30-minute plateau can"
(`scripts/chicago/profiles-am-kennedy.json:14-21`); ADR-0028's whole
existence proof is that an unexecuted program and a never-reached peak
produced months of misread speeds. Observed profiles also answer "when
does the drain start" — currently an authoring guess.
**Trade-off:** count-station profiles measure *throughput*, not *demand*;
on congested facilities the observed profile is the capacity-limited
remnant of the demand profile (same metering caveat as candidate 3).
Peak-spreading in observed data (shoulders flattened by delay) must not
be read as demand flatness.
**Field context:** K-factor/D-factor practice for AADT→design-hour
conversion; WebTAG/ATAP time-period validation
([standards-and-patterns.md](./standards-and-patterns.md) §6).

### 6. Add a distance-impedance term to destination weighting — calibrate, don't assume

**Choice:** Extend mkod's destination blend with a deterrence factor —
weight = floor area × f(c), f exponential or power in network distance —
*only after* the LODES home→work distance distribution is available to
fit f against; validate against the radiation model's parameter-free
prediction as a sanity cross-check. Default f ≡ 1 (today's behavior)
until fitted.
**Why:** zero impedance inflates mean trip length at constant rate,
inflating VMT, fleet size, and through-crop exposure — the model can't
distinguish "downtown worker who drives 2 km" from "drives 20 km". The
literature says calibrated gravity beats parameter-free models locally
(Lenormand 2016; Masucci 2013 on scale-dependence), which is exactly our
regime (a few-km crop).
**Trade-off:** one more fitted parameter family with a feedback into
congestion itself (distance should be congested time, which is the
sim's own output — the classic joint distribution/assignment problem;
start with free-flow distance, note the approximation). Also interacts
with `--dest-zone-share` pins, which are manual marginals — pins must
override f, as they override floor area today.
**Field context:** gravity/radiation/IO literature
([standards-and-patterns.md](./standards-and-patterns.md) §3); CMAP's
destination-choice models are estimated on Travel Tracker the same way.

---

## Compare/Contrast: our approach vs the field

| Dimension | traffic-sim today | SUMO toolchain | MATSim/eqasim | Aimsun/Visum | POLARIS/CMAP ABM |
|---|---|---|---|---|---|
| Demand form | per-flow rates + weighted lane destinations | trips/flows from matrices, counts, or activitygen | agent activity plans | OD matrices, count-adjusted | synthetic-population tours |
| Spatial grain | **individual building → individual lane** | TAZ → edge | facility/address | zone → section | subzone/parcel → link |
| Fitted to counts | **no (declared napkin)** | yes (routeSampler/dfrouter) | validated vs surveys | yes (ODME, GEH<5) | yes (public scorecard) |
| Distance deterrence | none (floor-area only) | whatever the input matrix carries | utility with cost | matrix/assignment joint | estimated choice models |
| Mode split | none (car/truck mix only) | external | yes, in-model | external | yes, in-model |
| Temporal shape | profile library (napkin fractions) | per-flow begin/end, time-sliced matrices | activity end-time models | departure adjustment | time-of-day choice models |
| Determinism/replay | **pure function of (defs, seed); verbs recorded** | seeded RNG, route files | seeded; iteration-dependent | n/a (macro) / seeded | seeded |
| Demand feedback on congestion | engine-side perimeter gate only (ADR-0039) | no | between-day re-planning | in ODME loop | between-day |
| Open | yes | EPL-2.0 | GPL | commercial | mixed/open parts |

Where we genuinely lead: spatial grain (building→lane with mid-block
injection and arrival-at-lane trip ends), determinism guarantees, and the
fail-loud/realized-demand reporting discipline (nobody else prints "the
file asks for X vehicles; compare against delivered"). Where the field
leads, unambiguously: fitting weights to observed counts, mode choice,
deterrence, and activity chaining.

## Open Questions

- **POLARIS demand extraction**: are POLARIS's calibrated Chicago OD/trip
  tables (or its ADAPTS destination-choice parameters) obtainable in a
  usable form? If yes, they could *rate* our flows directly (the strongest
  shortcut to a calibrated matrix); if not, LODES is the path. Not
  resolved by public docs — needs an Argonne/CMAP contact or repo dive
  (github presence unclear; [ANL page](https://vms.taps.anl.gov/tools/polaris/)).
- **No public Chicago eqasim/ActivitySim population found.** If one exists
  (Argonne/USDOT), it changes candidate 2's build list. Marked unverifiable
  from this desk.
- **LODES vintage vs the crop's development**: which LODES year best
  matches the OSM extract vintage the network was built from? A mismatch
  shifts district shares (the pin machinery would then fight the matrix).
- **Non-work purposes and freight**: LODES covers only home↔work. What
  anchors shopping/school/escort generation (NHTS rates × households?) and
  the truck share (CMAP csvm outputs?) — or does the model stay
  work-trip-only by declaration (its current de-facto posture)?
- **Congested-distance deterrence**: if candidate 6 lands, should cost be
  free-flow (static, one fit) or skimmed from the previous run's ADR-0036
  travel-time EMA (an outer iteration — the bi-level loop again)?
- **Parking as destination capacity** (ADR-0039 deferred item): does a
  finite-workplace-parking model supersede destination *weights* for the
  CBD core — attraction by jobs but admission by stalls? CT-RAMP models
  CBD free-parking eligibility explicitly; our 400 destination lanes are
  infinite sinks today.
- **Metered-demand anchoring**: Kennedy/Dan Ryan ramp meters make observed
  mainline counts a discharge sample, not a demand sample. Where are the
  on-ramp *queue* counts (if any) to recover demand? Possibly unobservable
  from public data — then portal anchors stay approximate and the profile
  plateau does the loading.
- **Return trips/activity chains**: is transposed-OD re-run (ADR-0021's
  stated trick) sufficient for the PM peak, or does paired AM/PM
  (same vehicles returning) matter for the metrics we quote?
- **`DemandSchedule` global multiplier** (`engine/spawn.go:53-58`): dead
  grammar in the scenario era (spawner-only, pre-slices)?

## Connections to Other Topics

- **domain-multimodal-transit** (PENDING): the mode-choice step this topic
  lacks. CTA ridership is both our mode-share residual (candidate 4b) and
  transit's validation corpus; mode share is where a person-demand model
  becomes a car-demand model. Research order matters: demand weights
  calibrated pre-transit bake in today's exogenous mode factor.
- **domain-congestion-metrics**: ADR-0030's run report is the sim-side
  half of the calibration scorecard (screenlines, VMT shares, speed
  distributions); ADR-0014's paired-seed protocol is what makes GEH-style
  acceptance meaningful on a stochastic sim.
- **domain-trajectory-datasets**: NGSIM/TGSIM supply per-lane rate and
  speed anchors (TGSIM is *on the Kennedy*); Edie's q/k/u is the shared
  language between observed and simulated fundamental diagrams (the
  ADR-0039 MFD measurement is the demand-gate anchor).
- **concept-scenario-format**: the grammar boundary (candidate 1) is an
  ADR-0012 doctrine; any demand-grammar growth (e.g. a counts part, an
  initial-state demand primitive — ADR-0012 §7's deferred item) is a
  scenario-format decision with hash implications.
- **integration-osm-extraction**: buildings.py's classification ceiling is
  an OSM tagging problem (71% `building=yes`, no `residential=*`); the
  residential-street-grid gap is an import class-filter decision
  (ADR-0021 open item); CMAP land-use inventory is the candidate external
  disambiguator that doesn't require re-import.
- **ADR-0036 / adaptive routing**: assignment and demand co-calibrate —
  adaptive rerouting was measured to *not* prevent dead-stop at ~2×
  oversaturation, which bounds what demand levels any routing can excuse.
- **ADR-0039 / perimeter metering**: the gate is the demand-side control
  loop; its deferred list (per-portal ALINEA meters, parking capacity,
  core-district gate signal) is this topic's control-facing roadmap.
