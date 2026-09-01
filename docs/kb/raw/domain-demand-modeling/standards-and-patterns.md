# Standards & Patterns: Demand Modeling

> Source: academic research + pattern identification | Researched: 2026-08-24

The established theory and data infrastructure for "where trips come from,
where they go, and how we know the weights are right" — each item connected
to traffic-sim's implementation ([implementation.md](./implementation.md)).

---

## 1. The classic four-step model — and what survives at lane-level micro

The field's reference structure since the 1950s: **trip generation** (how
many trips leave/enter each zone) → **trip distribution** (which origins
connect to which destinations) → **mode choice** (by what mode) →
**assignment** (by which route).
Source: [Forecasting Urban Traffic in France (HAL open PDF)](https://enpc.hal.science/hal-01071139/document);
textbook reference: Ortúzar & Willumsen, *Modelling Transport* (print);
[NPTEL lecture notes (digimat PDF)](http://www.digimat.in/nptel/courses/video/105105208/lec59.pdf).

Mapping onto our machinery:

| Step | Classic form | traffic-sim today | Where |
|---|---|---|---|
| Generation | zonal productions/attractions from land use × rates | per-lane rates: portal class table scaled to `--total`; residential/workplace floor area | `scripts/chicago/mkod.py:926-936,1183-1212` |
| Distribution | gravity/IO model → OD matrix | per-flow **weighted destination draw** (floor-area weights; zone pins; reachability filter) — no impedance function | `engine/scenario/scenario.go:131-136`, `scripts/chicago/mkod.py:830-884` |
| Mode choice | logit over generalized cost | **absent** — all demand is road vehicles (car/truck mix) | `scripts/chicago/mkod.py:26-33` (acknowledged) |
| Assignment | (stochastic) user equilibrium / DTA | microscopic simulation: static next-hop tables + ADR-0036 epoch-adaptive rerouting + lateral guardrail | `docs/kb/decisions/ADR-0036-congestion-adaptive-routing.md:20-44` |

What survives at lane-level micro granularity:

- **Generation survives almost intact**, just at finer grain: rates × land-
  use intensity is exactly what buildings.py/mkod do, with floor area as the
  intensity measure (the ITE independent variable, §2).
- **Distribution changes form, not content**: a doubly-constrained matrix is
  replaced by per-origin destination *distributions*. Our form is a
  production-anchored attraction model (singly constrained: origins are
  rate-controlled, destinations are shares). Nothing enforces that
  destinations receive their "designed" volumes — the realized district
  shares mkod prints are an output, not a constraint
  (`scripts/chicago/mkod.py:1245-1268`).
- **Mode choice is the missing step**, and it is load-bearing for Chicago:
  downtown transit share is "very high" (mkod docstring), so car demand =
  person demand × mode share is not derivable inside our model today. It
  must enter as an exogenous factor on rates (from CTPP/NHTS/CMAP) until
  domain-multimodal-transit lands.
- **Assignment is *better* than the classic**: the microsim itself is a
  dynamic assignment with per-vehicle heterogeneity; ADR-0036 adds the
  congestion-responsive re-planning the equilibrium loop approximates.
  Its measured limit — "rerouting spreads congestion, it does not create
  capacity" — is the classical result that assignment cannot fix
  oversaturated demand, which is why demand levels are the calibration
  target.

## 2. Trip generation rates: ITE and NHTS

**ITE Trip Generation Manual** (12th ed. 2025; 11th ed. 2021): the US
practice standard. Trips are estimated per land-use code (several hundred
codes) as rate × independent variable (1,000 sq ft gross floor area,
dwelling units, employees…), reporting fitted-curve equations, weighted
average rates, R², and data-plot ranges, split by daily / AM peak / PM
peak / peak-hour-of-generator. Known weaknesses: suburban-biased samples,
weak R² on many codes, no mode split.
Source: [What's Changed in the ITE Trip Generation Manual 12th Edition (Gorove Slade, 2025-11-20)](https://goroveslade.com/whats-changed-in-the-ite-trip-generation-manual-12th-edition-and-why-it-matters-for-your-project/).

**NHTS** (National Household Travel Survey, FHWA/ORNL): the national
household travel diary — trips by purpose, mode, time of day, household
attributes; the source for trip rates per household and time-of-day
distributions. The 2016/17 cycle surveyed all 50 states with a 24-hour
travel day; **NextGen NHTS** adds a passively-derived national OD data
program (multimodal OD products).
Source: [NHTS](https://nhts.ornl.gov/),
[NextGen NHTS National OD Data](https://nhts.ornl.gov/od/),
[FHWA NHTS report example (PDF)](https://nhts.ornl.gov/assets/FHWA_NHTS_Report_3D_Final_021119.pdf).

**Connection to our implementation:** buildings.py's
`floor_area_m2 = footprint × levels` (`scripts/chicago/buildings.py:82`) is
an OSM-derived stand-in for ITE's GFA independent variable; mkod's
rate-per-floor-area scaling (`scripts/chicago/mkod.py:1184-1212`) is one
ITE-style rate applied to one land-use class pair (residential→origin,
workplace→destination). What is missing vs the standard: purpose splits
beyond work (shop/school/escort/maintenance/discretionary — CMAP's ABM
uses ten activity types), per-class rates differentiated by land-use code
(our `workplace` lumps office/retail/hotel/hospital), and any calibration
of the rate itself. The profiles library
(`scripts/chicago/profiles-am.json:17-38`) is our time-of-day factor table;
NHTS is the obvious empirical source to re-anchor its fractions (see §6).

## 3. Destination choice models: gravity, radiation, intervening opportunities

- **Gravity model**: T_ij ∝ P_i · A_j · f(c_ij), f a deterrence function of
  generalized cost (exponential/power). The doubly-constrained form
  (Furness/IPF balancing to match O_i and D_j marginals) is the
  distribution step of every 4-step deployment.
  Source: [Spatial Interaction (UCGIS GIS&T BoK AM-03-010)](https://gistbok-ltb.ucgis.org/current/concept/AM-03-010).
- **Intervening opportunities** (Stouffer 1940): the probability a trip
  ends at a destination depends on the opportunities *nearer* than it, not
  on distance per se.
  Source: referenced in [EPJ Data Science — commuting flow and jump model](https://epjdatascience.springeropen.com/articles/10.1140/epjdatascience/s13688-018-0167-3).
- **Radiation model** (Simini et al., *Nature* 2012): parameter-free
  formulation of intervening opportunities —
  T_ij = T_i · m_i m_j / ((m_i + s_ij)(m_i + m_j + s_ij)), where s_ij is
  the population in the circle between i and j. Absorbs trip distribution
  into population geometry alone.
  Source: [Gravity model explained by the radiation model (PLOS ONE)](https://journals.plos.org/plosone/article/file?type=printable&id=10.1371/journal.pone.0218028).
- **Head-to-head evidence**: Lenormand et al. (2016) tested gravity vs IO
  laws against empirical commuting data — "the gravity law performs better
  than the intervening opportunities laws"; Masucci et al. (PRE 2013) showed the winner depends
  on scale and heterogeneity (radiation better at coarse/heterogeneous
  scales, gravity with calibrated deterrence better locally).
  Source: [Systematic comparison of trip distribution laws and models (ScienceDirect)](https://www.sciencedirect.com/science/article/abs/pii/S0966692315002422),
  [Masucci et al. PRE 88, 022812 (via ACM reference list)](https://dl.acm.org/doi/10.1145/3209811.3209868).

**Connection to our implementation:** our destination draw has **no
impedance term at all** — weight = floor area (filtered to reachable,
optionally zone-pinned), independent of the origin's position
(`scripts/chicago/mkod.py:700-721,830-832`). In model terms we run a pure
attraction-proportional allocation, which is the gravity model's A_j term
with f(c_ij) ≡ 1. The reachability filter (`can_reach`,
`scripts/chicago/mkod.py:124-159`) is a topological 0/1 deterrence; the
`--dest-zone-share` pins (350-405) are manual marginals. For a cordon crop
whose origins are mostly *at its boundary*, deterrence matters less than
for a full region — most portal→CBD trips have similar remaining distance —
but for residential interior origins it is first-order: a resident is
equally likely to be assigned a job across the crop as next door, which
inflates mean trip length and therefore VMT and fleet size at constant
rate. The empirical deterrence function to calibrate against is in LODES
(§4) as the home→work distance distribution.

## 4. Calibration data sources (with access notes)

**Census LEHD / LODES** — the anchor dataset for home↔work flows. Origin-
Destination Employment Statistics: block-level counts of jobs by worker
home block ↔ work block (OD table), plus Residence Area Characteristics
(RAC) and Workplace Area Characteristics (WAC), annual 2002-2023 for all
50 states, with earnings/industry/age segments. Free bulk CSV download and
the OnTheMap web app. Caveat: LODES is a *partially synthetic* dataset
(noise-infused for disclosure protection; block-level cells are estimates,
aggregate before trusting). Cook County / CMAP region fully covered.
Source: [Census LEHD data (LODES)](https://lehd.ces.census.gov/data/#lodes),
[Census CES Research Report 2023 (PDF) — "partially synthetic", OnTheMap](https://www2.census.gov/library/publications/2024/research/2023_CES_Research_Report.pdf),
[dataset structure doc (Dataplex)](https://docs.dataplex-consulting.com/data-catalog/census-lehd-lodes-dataset).
**Access:** public, no login, FTP/HTTPS bulk; state-partnered so Illinois
coverage is current. For us: provides (a) production counts per block to
rate residential origins, (b) attraction counts per block to rate
workplace destinations, (c) the OD *matrix* itself for distribution
calibration/validation — replacing three of buildings.py's proxies with
counts of actual jobs/residents. It does not carry time-of-day, mode, or
non-work purposes.

**CMAP Data Hub** — the region MPO's open catalog: Land Use Inventory
(parcel-level land-use classes, the disambiguator for OSM `building=yes`),
trip-generation zones, community data snapshots, freight clusters.
Source: [CMAP Data Hub (referenced from ArcGIS item)](https://www.arcgis.com/home/item.html?id=c7959bd1c0084edba3264099deeaf365),
[BTAA Geoportal CMAP records](https://geo.btaa.org/catalog/4c75874452ab408092eab69ffca4948a_4).
**Access:** public downloads (ArcGIS feature layers/shapefiles).

**IDOT traffic counts** — AADT from IDOT's Traffic Count Program,
published on the IDOT Open Data Portal as a feature layer (2024 vintage
noted) and the gettingaroundillinois map. AADT is a *daily* figure;
converting to an AM-peak rate needs a K-factor (design-hour share) and a
directional split — both derivable from continuous-count stations where
available.
Source: [Annual Average Daily Traffic [Illinois] (BTAA Geoportal, IDOT Open Data Portal)](https://geo.btaa.org/catalog/fe639dd2b39c4ef68e0f9973bdc6737a_0).
**Access:** public feature layer/download.

**City of Chicago Average Daily Traffic Counts** — ADT by street segment,
two distinct products under one label: (a) the current camera-derived feed —
collected from 800+ traffic/speed-enforcement cameras 24/7, summarized and
assigned to segments by CDOT, covering **October 2020 onward**, each record a
**30-day rolling average**; and (b) a separate historical CDOT study, an
annual series back to 2006.
Source: [City of Chicago — Average Daily Traffic Counts (data.gov catalog)](http://catalog.data.gov/dataset/average-daily-traffic-counts),
[CDOT dataset page](https://www.chicago.gov/city/en/depts/cdot/dataset/average_daily_trafficcounts.html).
**Access:** public (Socrata API + CSV). This is the arterial-grid
counterpart to IDOT's freeway AADT — exactly the counts our `--total`
(arterial target) should reconcile against.

**CTA ridership** — daily boarding totals (system, by route, by L station
entry) back to 2001 on the City of Chicago data portal; Bus Tracker /
Train Tracker APIs for realtime.
Source: [CTA Ridership — Daily Boarding Totals (data.gov)](http://catalog.data.gov/dataset/cta-ridership-daily-boarding-totals),
[CTA Developer Center](https://www.transitchicago.com/developers/).
**Access:** public. For demand modeling this is the *mode-share residual*:
CBD-bound commuters minus CTA boardings ≈ the auto demand our model must
produce (also the domain-multimodal-transit validation corpus).

**NGSIM / TGSIM (micro behavior + corridor rates)** — NGSIM is the classic
vehicle-trajectory set (covered in domain-trajectory-datasets). TGSIM
(FHWA, 2024) is directly Chicago-relevant: helicopter/UAS trajectories on
**I-90/I-94 (the Kennedy) and I-294**, moving + stationary bottlenecks —
the KB already names "TGSIM I-90/94 per-lane rates" as the first real
calibration target (`docs/kb/articles/chicago-metro.md:276-278`).
Source: [Third Generation Simulation Data (data.gov)](https://catalog.data.gov/dataset/third-generation-simulation-data-tgsim),
[TGSIM I-90/I-94 Moving Trajectories](http://catalog.data.gov/dataset/third-generation-simulation-data-tgsim-i-90-i-94-moving-trajectories),
[TGSIM project report (NTL PDF)](https://rosap.ntl.bts.gov/view/dot/74647/dot_74647_DS1.pdf).

**NextGen NHTS OD** — passively-derived multimodal national OD data
products (2020-2024), a newer complement to LODES with all purposes/modes
but coarser privacy-preserving geography.
Source: [NextGen NHTS National OD Data](https://nhts.ornl.gov/od/),
[Maryland Transportation Institute NextGen NHTS](https://mti.umd.edu/nhts).

## 5. OD estimation from link counts (the entropy-maximization family)

The inverse problem — given link counts, find the most likely OD matrix —
has a 45-year literature: van Zuylen & Willumsen (1980) formulated the
**maximum-entropy** "most likely trip matrix" consistent with observed
link volumes; Cascetta (1984) generalized least squares combining counts
with survey priors; Fisk (1988/89) embedded it in a user-equilibrium
assignment (bi-level); Spiess's gradient method and Maher's Bayesian
inference are the other main branches. Every commercial ODME (Visum
TFlowFuzzy, Aimsun OD adjustment) and SUMO's routeSampler descends from
this family.
Source: [Abrahamsson 1998 survey, "Estimation of Origin-Destination Matrices Using Traffic Counts" (IIASA PDF)](https://pure.iiasa.ac.at/id/eprint/5627/1/IR-98-021.pdf),
[Sherali et al., partial link volumes (ScienceDirect)](https://www.sciencedirect.com/science/article/pii/S0191261502000735),
[generalized TFlowFuzzy study (ResearchGate)](https://www.researchgate.net/publication/288143019_A_study_on_the_generalized_TFlowFuzzy_O-D_estimation).

**Connection to our implementation:** this is the formal version of what
the Kennedy tuning did by hand — adjust demand knobs until sim outputs
match observed speeds/volumes (`data/scenarios/chi-loop-od-30m/README.md:97-136`
is one manual iteration of a bi-level loop). Two properties of the family
matter for us: (1) the problem is **underspecified** — many OD matrices
reproduce the same counts; the maximum-entropy/prior-matrix machinery
exists to pick one defensibly, which argues for keeping the
buildings/LODES-derived structure as the *prior* and letting counts adjust
it, never deriving demand from counts alone; (2) fitting against
*congested* counts needs the assignment inside the loop (bi-level) — for
us the assignment is the microsim itself, so one outer iteration is a full
bracket run.

## 6. Temporal demand profiles (time-of-day factors)

Practice standards: the **K-factor** (proportion of AADT in the design
hour, classically the 30th-highest hour) and directional D-factor convert
AADT into peak-hour design volumes (AASHTO Green Book); the **peak-hour
factor (PHF)** converts within-peak 15-minute peaking (HCM); time-of-day
*demand profiles* (hourly factors by day type) come from continuous count
stations and household surveys (NHTS time-of-day distributions). Validation
guidance (UK WebTAG M3; Australian ATAP) requires models to reproduce
count profiles *by time period*, not just daily totals.
Source: [WebTAG Unit M3 referenced in validation criteria (Spelthorne SHAR PDF)](https://www.spelthorne.gov.uk/sites/default/files/migration/media/25326/Spelthorne-Local-Plan-Strategic-Highway-Assessment-Report-Technical-Annex/pdf/Doc02_Spelthorne_Local_Plan_SHAR_Technical_Annex_Final.pdf),
[ATAP — travel demand modelling §5.8 assignment validation](https://www.atap.gov.au/tools-techniques/travel-demand-modelling/5-model-development),
[NHTS](https://nhts.ornl.gov/).

**Connection to our implementation:** the ADR-0028 profile library
(`scripts/chicago/profiles-am.json:17-38`) is our factor table — fractions
of peak per 600 s step, per trip purpose. The shapes are explicitly
"napkin-anchored to the usual diurnal shapes, NOT calibrated counts"
(`scripts/chicago/profiles-am.json:9-11`). The empirical upgrade path is
direct: IDOT continuous-count hourly profiles for the freeway shapes,
NHTS time-of-day for purpose splits, City ADT camera time series for
arterial shapes. One structural finding already embedded: the kennedy-am
profile exists because "a 10-minute peak cannot build a queue, a 30-minute
plateau can" (`scripts/chicago/profiles-am-kennedy.json:14-21`) — temporal
shape is a congestion variable, not just a realism one. Note also the
departure-adjustment conservation point from competitors.md: our fractions
scale each flow's peak rate, so changing the library changes the vehicle
total; nothing pins a daily total.

## 7. Validation practice: how you know the weights replicate the real world

The standard acceptance battery from FHWA/WebTAG/ATAP practice:

- **GEH statistic** — GEH = √(2(m−c)²/(m+c)) per link, model volume m vs
  count c; the widespread acceptance bar is GEH < 5 for ≥85% of counted
  links (UK DMRB/WebTAG lineage), used identically in US microsim
  calibration and in Visum TFlowFuzzy convergence ("until a maximum of GEH
  became below 5").
  Source: [Calibration and Validation — Aimsun Next User Manual (GEH + FHWA)](https://docs.aimsun.com/next/26.0.0/UsersManual/CalibrationAndValidationOfAimsunModels.html),
  [TFlowFuzzy case study (CORE PDF)](https://core.ac.uk/download/pdf/6751565.pdf).
- **Screenline/cordon totals** — assigned vs observed flows summed over
  screenlines (checks the *trip matrix*, as opposed to link-level routing):
  WebTAG M3 prescribes screenline and link comparisons by time period.
  Source: [WebTAG M3 criteria as applied (Spelthorne PDF)](https://www.spelthorne.gov.uk/sites/default/files/migration/media/25326/Spelthorne-Local-Plan-Strategic-Highway-Assessment-Report-Technical-Annex/pdf/Doc02_Spelthorne_Local_Plan_SHAR_Technical_Annex_Final.pdf).
- **VMT and volume distribution checks** — modeled vs observed VMT shares
  by county/facility type, link-volume scatter (R²), %RMSE by volume bin
  vs published targets (the Florida DOT targets CMAP benchmarks against).
  Source: [CMAP ABM report — Highway Assignment tab](https://cmap-repos.github.io/cmap_abm_report/).
- **Speed / travel-time validation** — corridor travel times and speed
  distributions vs probe data (INRIX/StreetLight/ATRI truck speeds); our
  README's corridor table vs "report's peak truck speed" is an informal
  version (`data/scenarios/chi-loop-od-30m/README.md:61-75`).
  Source: [NJ DOT traffic study — StreetLight speed validation + FHWA microsim guidelines (PDF)](https://dot.nj.gov/transportation/works/rockfall/pdf/Traffic_Study.pdf).
- **Governing manuals** — FHWA *Traffic Analysis Toolbox Vol. III*
  (microsimulation calibration) and FHWA's *Travel Model Validation and
  Reasonableness Checking Manual* (TMaC) are the US reference processes;
  both are cited as the calibration frameworks in DOT study after study.
  Source: [New Tecumseth TMP — FHWA + UK calibration criteria summary (PDF)](https://www.newtecumseth.ca/wp-content/uploads/2026/06/NTTMP_FinalReport_withAppendices_compiled-2022-04-12.pdf),
  [Tennessee MUG guidelines 2016 (PDF)](https://tnmug.utk.edu/wp-content/uploads/sites/10/2022/11/Guidelines-Updated-2016.pdf).

**Connection to our implementation:** the ADR-0030 run report already
computes the sim-side halves of the VMT/speed distribution checks
(density/speed distributions in lane-km and VMT shares, per corridor and
district — `docs/kb/decisions/ADR-0030-run-report-protocol.md:35-62`).
What does not exist: a counts layer in the scenario (observed volumes by
lane/portal with tolerances), a GEH/screenline comparator in tooling, and
any acceptance threshold. mkod's `observe()` district-share print
(`scripts/chicago/mkod.py:834-850`) is the demand-side screenline concept
in embryo — it reconciles what the file *aims* where, against which a
LODES district-flow table would be the observed side.

## 8. Design patterns identified in our implementation

### Authoring-time / run-time split (the recipe pattern)
Demand realism lives entirely in generator scripts; the engine consumes a
declarative artifact. "The generator is a script, not engine code: demand
generation is scenario authoring, and its output — a demand YAML — is the
reviewable artifact" (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:280-282`).
Same posture as SUMO's toolchain and eqasim's pipelines; the hash-chain
difference is ours: the artifact rides the ADR-0012 content hash into run
identity, so the recipe's *output* is pinned even when the recipe is not.

### Layered primitives, sim seconds only
Flows, slices, vtype mixes, destination weights — no OD-matrix compile
step, no analytic functions, no wall clock (ADR-0012 §3). The field norm
for time-varying demand (piecewise-constant slices) was adopted
deliberately (`docs/kb/decisions/ADR-0012-scenario-format.md:23-31,65-83`).

### Deterministic sampling discipline
Keyed per-vehicle streams, sorted-key weighted draws (float
non-associativity), draw-order conservation for backward compatibility,
idempotent request IDs, failover-invisible restart
(`engine/natsio/demand/director.go:381-509`). This is what makes "the
sampler never re-runs on replay" cheap: verbs are data, not behavior.

### Fail-loud validation doctrine
Zero weights are errors; dead profile rules are fatal; truncated programs
are refused; two sources of truth for a rate or a shape are refused
(`engine/scenario/scenario.go:323-360,676-774`;
`scripts/chicago/mkod.py:335-347,638-649,901-921`). The stated reason,
everywhere: a silent demand failure reads as a clean measurement of a
scenario that never ran (the KB's silent-fidelity-failure canon).

### Realized-demand self-reporting ("measure the file, not the flags")
After the ADR-0028 correction (authored vs written demand diverged four
ways at once), every total mkod reports is integrated from the slices as
written, and the demand file's header carries the realized peak, vehicle
count, knobs, and ordered rules (`scripts/chicago/mkod.py:1214-1342`;
`docs/kb/decisions/ADR-0028-demand-profile-library.md:123-175`).

### Mass-balance closure (through traffic as the drain)
A demand program without boundary-exit destinations has no equilibrium at
any horizon ("a bathtub with the taps open",
`scripts/chicago/mkod.py:723-740`). Through shares (45% arterial / 75%
freeway) close the balance; the vehicle-count integral is what gets
reconciled against completed+stranded+active downstream.

### Perimeter control as demand control (MFD-fed hysteresis gate)
ADR-0039's density gate is network-fundamental-diagram perimeter control
in the Daganzo/Geroliminis lineage: hold demand outside the protected zone
to stay on the free-flow side of the discharge crest (measured here at
≈5.7k veh/h around 4,800-5,100 active). Hysteresis prevents flicker; the
deferral clock separates *deferred* from *deleted* demand after
measurement showed deletion cost completions 1:1
(`docs/kb/decisions/ADR-0039-perimeter-demand-metering.md:16-114`;
`engine/spawn.go:318-357`; `engine/director.go:310-345`).
