# Standards & Patterns: Bus Operations

> Source: academic research + standards documents + pattern identification | Researched: 2026-08-24 | Git HEAD: 2bc98de

Each section states the field knowledge, then connects it to our
implementation (`engine/…` references are to Git HEAD 2bc98de; see
implementation.md for the full seam trace).

---

## 1. Bus bunching and headway instability — why equal headways are unstable

**Newell & Potts (1964)** is the founding analysis: an idealized route,
evenly spaced stops, identical inter-stop running times, steady passenger
arrivals. A bus delayed at a stop finds *more* passengers accumulated, so it
dwells longer and falls further behind; the following bus finds *fewer*,
dwells less, and catches up. Adjacent buses therefore alternate between
running ahead and behind, and the deviation amplifies stop over stop — the
schedule is self-undermining. Loading time being a non-decreasing function of
headway is the whole mechanism.
Sources: Newell, G.F. & Potts, R.B., "Maintaining a bus schedule", *Proc.
2nd Australian Road Research Board Conference, Melbourne, 1964* — as cited
and re-derived in [Daganzo's headway-management report (Rosap/NTL)](https://rosap.ntl.bts.gov/view/dot/35029/dot_35029_DS1.pdf) and
[Bus Bunching Along a Corridor Served by Two Lines (White Rose PDF)](https://eprints.whiterose.ac.uk/id/eprint/103483/1/Bunching%20and%20CL%20-%20Part%20B-WRR.pdf).

**The modern framing: equal-headway instability is generic, not
Chicago-specific.** Gershenson & Pineda (2009) prove via a homogeneous
multi-agent simulation that equal headways are unstable *without any
asymmetry* — no bad driver, no bad stop required — and that "headway
instability" is the normal state of an uncontrolled high-frequency line.
Their follow-up work evaluates self-organizing holding rules that beat
schedule-following on both wait and travel time.
Sources: [Gershenson & Pineda 2009, PLoS ONE 4(10):e7292](https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0007292);
[Gershenson 2011, PLoS ONE 6(6):e21469](https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0021469);
[Carreón et al. 2017 (headway-based model with boarding/alighting regulation), PLoS ONE 12(12):e0190100](https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0190100).

**Minimal models lineage (validation oracles for us).** O'Loan, Evans &
Cates (1998) define the Bus Route Model — a driven-diffusive CA ring with
dwell ∝ waiting passengers — and map its jamming transition; Nagatani's
time-headway car-following bus models (2000–01) show kinetic clustering with
continuous dynamics; Luo et al. (2012) add open boundaries. These are the
"ring-road experiment" equivalents for buses: simple systems with known
phase behavior that our engine should reproduce before being trusted on
real networks.
Sources: [O'Loan et al., Phys. Rev. E 58:1404 (1998)](https://link.aps.org/doi/10.1103/PhysRevE.58.1404) ([arXiv:cond-mat/9712243](https://arxiv.org/abs/cond-mat/9712243));
Nagatani 2000, Physica A 287:302–312, cited in [arXiv:1711.05884](https://arxiv.org/pdf/1711.05884);
Luo et al. 2012, TR-C 25:202–213, cited in [arXiv:2006.13532](https://arxiv.org/pdf/2006.13532).

**Control literature (what a reference controller would implement):**

- **Holding control.** The dominant family. Daganzo (2009) computes holding
  times from *real-time headway* at control points — headway-based, no
  schedule needed — and shows it beats schedule-adherence holding on both
  speed and wait. Xuan, Argote & Daganzo (2011) recast it as optimal linear
  control with performance analysis ("simple" holding laws degrade
  gracefully under uncertainty). Eberlein, Wilson & Bernstein (2001) posed
  the rolling-horizon holding problem with real-time information.
  Sources: Daganzo 2009, TR-B 43(10):913–921, cited in [eScholarship qt0551g0zw](https://escholarship.org/content/qt0551g0zw/qt0551g0zw_noSplash_a321ce86ef93f601803daea3b8d4b2bf.pdf);
  [Xuan et al. 2011, TR-B 45(1)](https://www.sciencedirect.com/science/article/pii/S0191261511001093);
  Eberlein et al. 2001, cited in [Robust dynamic bus controls (TR-B 2019)](https://www.sciencedirect.com/science/article/pii/S0191261518305095).
- **Schedule abandonment / self-coordination.** Bartholdi & Eisenstein
  (2012): hold each bus at control points by a time *proportional to its
  trailing headway*; headways self-equalize with no schedule and no target
  headway at all. Deployed on Georgia Tech's campus route (2012) — the
  existence proof that the law works against real drivers and real traffic.
  Sources: [Bartholdi & Eisenstein 2012, TR-B 46(4):481–491](https://econpapers.repec.org/RePEc:eee:transb:v:46:y:2012:i:4:p:481-491) ([author PDF via Gwern](https://gwern.net/doc/reinforcement-learning/multi-agent/2012-bartholdi.pdf));
  [Building a Self-Organizing Urban Bus Route (deployment)](https://www.researchgate.net/scientific-contributions/Donald-D-Eisenstein-12558373).
- **Stop-skipping, boarding limits, cruising speed.** Stop-skipping and
  boarding-limited dynamics (cap boardings at a stop to protect headway)
  appear throughout the holding literature as the more aggressive levers;
  Daganzo & Pilachowski (2011) use *cruising-speed modulation* between stops
  instead of holding. Note the two-way-looking refinement: headway
  equalization that considers both neighbors beats leader-only rules.
  Sources: [Daganzo & Pilachowski 2011, cited in TR-B 2021 corridor study](https://www.sciencedirect.com/science/article/pii/S0968090X21002953);
  [Two-way-looking self-equalizing headway control, TR-B 2020](https://www.sciencedirect.com/science/article/abs/pii/S0191261516308074);
  boarding-limit model in [Carreón et al. 2017](https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0190100).
- **Learning-based.** Multi-agent DRL holding is now a crowded literature
  (Wang & Sun 2020 and successors), all evaluated on bespoke line
  simulators — a niche our recorded-replay A/B harness directly attacks.
  Source: [Wang & Sun 2020, TR-C 116](https://www.sciencedirect.com/science/article/abs/pii/S0968090X20305763).

**Connection to our implementation:** bunching requires exactly three
ingredients — (a) multiple buses on a line (a demand `Flow` with
`vtypes: {bus: 1}` and a period, `engine/scenario/scenario.go:122-144`),
(b) dwell that grows with accumulated demand (a boarding-rate function in
the bus controller, §2 below), and (c) no holding policy. All three are
client-side; the kernel needs nothing. The **headway CV and bunching rate
are measurable from ADR-0014 trip records** (`engine/metrics.go:135-154`,
`TypeName` group-by) or directly off the recorded trajectories — and the
O'Loan/Nagatani ring models give cheap validation fixtures analogous to the
existing Sugiyama ring (`engine/network.go:143-147`, exercised by
`engine/sugiyama_test.go`). A holding controller
is a cruise-setpoint client (implementation.md §2.1): Daganzo's law, Xuan's
linear gain, and Bartholdi–Eisenstein are ~20-line policies over
`ts.{run}.state.snap` positions plus a line table.

## 2. Dwell-time models

**Definition (TCQSM):** dwell time is "the time a bus spends serving
passenger movements, including the time required to open and close the bus
doors and boarding lost time" — and may constitute up to ~26% of total
running time on urban routes.
Source: [Effect of Fare Policies on Dwell Time (CMU Mobility21, quoting TCRP R165/TCQSM 3rd ed.)](https://mobility21.cmu.edu/wp-content/uploads/2018/08/2017_FareDwell_TRR.pdf).

**The canonical regression form** is linear in passenger counts with a dead
time:

```
dwell = t_dead + t_b · max(boardings at busiest door)
              + t_a · (alightings at busiest door)     [+ crowding terms]
```

fitted from AVL/APC/AFC data (e.g. Hans et al. 2015; Sun et al. 2014).
Sources: [Estimation of Bus Connection Risk review (CORE PDF)](https://core.ac.uk/download/250143731.pdf);
[Sun et al. 2014, Models of bus boarding and alighting dynamics, TR-A (author PDF)](https://lijunsun.github.io/files/papers/2014-TRA-Dwell.pdf).

**Published service-time values** (*Characteristics of Bus Rapid Transit
for Decision-Making* (CBRT), p. 3-25, as reproduced in the FTA iBRT report
below; TCRP Report 118 is a separate Practitioner's Guide that expanded on
CBRT):

| Fare payment | Boarding s/pax (observed range) | Default |
|---|---|---|
| Pre-payment (off-board) | 2.25–2.75 | 2.5 |
| Smart card | 3.0–3.7 | 3.5 |
| Single ticket/token | 3.4–3.6 | 3.5 |
| Exact change | — | 4.0 |
| Swipe/dip card | — | 4.2 |
| Alighting, rear door | 1.4–2.7 | 2.1 |
| Alighting, front door | 2.6–3.7 | 3.3 |

Source: [Quantifying the Benefits of BRT Elements (FTA iBRT report PDF)](https://www.bettertransport.info/brt/FTA%20iBRT%20FINAL%20REPORT%20508%20Compliant.pdf).
A zero-fare synthesis cites 4.5 s cash / 2.8 s smart card / 1.8 s no-payment
per boarding: [WFRC Regional Zero-Fare Study](https://wfrc.utah.gov/Studies/ZeroFareTransit/ZeroFareTransitStudy_FinalReport.pdf).

**Fare collection, floor level, crowding (Tirachini 2013; 200+ citations):**
payment medium is a first-order dwell variable; low-floor buses and
off-board payment cut seconds per passenger; dwell ≈ 15% of total travel
time under on-board payment in the Sydney dataset. Crowding raises per-pax
times nonlinearly: 2.3 s/pax boarding uncrowded → 2.9 and 4.4 s/pax with 10
and 15 through-standees per door. Values used in practice: t_alight ≈ 1.3 s,
t_dead ≈ 5.2 s (low-floor, magnetic prepaid card) in a Stockholm terminal
microsimulation.
Sources: [Tirachini 2013, TRR 2351 (TRID record)](https://trid.trb.org/View/1246890);
[crowding figures via Tirachini et al., ResearchGate](https://www.researchgate.net/publication/264967732);
[Microsimulation of bus terminals, WSC 2020 (INFORMS PDF)](https://www.informs-sim.org/wsc20papers/141.pdf).

**Connection to our implementation:** the dwell *decision* lives in the bus
controller, so the model is a policy function `dwell = f(boardings,
alightings, fare regime)` evaluated client-side, with the constants above as
the default calibration and the crowding term as the bunching amplifier
(this is precisely Newell & Potts's loading-time feedback, parameterized).
Boarding counts themselves come from scenario demand (stop-level boarding
rates) or GTFS+APC calibration; the kernel sees only the resulting recorded
cruise-setpoint intents. TCQSM's "up to 26% of running time" is the sanity
check on any scenario we author.

## 3. Schedule- vs headway-based operation

- **Practice rule:** low-frequency service (headways ≳ 10–15 min) is
  managed to the *schedule* (passengers time their arrivals); high-frequency
  service is managed to *headways* (passengers arrive ~randomly, so regular
  spacing beats timetable adherence). TCRP Synthesis 155 documents US
  agencies running headway-based service with peak targets of 6–15 minutes
  and the supporting ITS practice (stage vehicles, supervisor dashboards).
  Source: [TCRP Synthesis 155 — Intelligent Transportation Systems in Headway-Based Bus Service (NAP, 2021)](https://nap.nationalacademies.org/read/26163/chapter/5).
- The FTA's BRT characterization defines schedule-based control as
  regulating to published times at timepoints vs. headway-based regulating
  spacing. Source: [FTA, Characteristics of BRT for Decision-Making 2009](https://www.transit.dot.gov/sites/fta.dot.gov/files/CBRT_2009_Update_0.pdf).
- A 12-bus/h service "falls into the frequent-service paradigm … more
  important to provide regularly-spaced service" (citing Hounsell &
  Shrestha 2012), with TSP priority conditioned on headway regularity rather
  than schedule adherence. Source: [Narayan thesis (Knoop group)](https://www.victorknoop.eu/research/theses/2022_Thesis_narayan.pdf).

**Connection:** the schedule-vs-headway choice is *scenario content + bus
controller policy* — the line table the controller carries (timepoints vs.
target headway), and which feedback it computes. Both are replayable
verbatim. The interesting experiment this architecture uniquely enables:
same network, same demand, three controllers (schedule-adherence holding,
Daganzo headway holding, Bartholdi–Eisenstein) in a paired-seed bracket —
the whatif/sigctl-bracket pattern (`scripts/sigctl-bracket.py`) applied to
transit control laws.

## 4. The bus stop as a capacity bottleneck

**TCQSM / HCM bus-stop capacity.** The loading-area (berth) capacity in
buses/h, TCQSM 2nd ed.:

```
B_l = 3600 · (g/C) / (t_c + t_d·(g/C) + Z·c_v·t_d)
```

g/C = effective green ratio (1.0 unsignalized), t_c = clearance time (~10 s
on-line), t_d = mean dwell, c_v = dwell-time coefficient of variation, Z =
normal variate for the tolerated failure rate (e.g. 10% ⇒ Z≈1.28). Worked
TCQSM example: g/C 0.45, t_d 30 s, c_v 0.6, 10% failure ⇒ capacity in the
tens of buses/h — stops saturate fast.
Sources: [TCQSM 2nd ed. Part 4 (TRB PDF)](https://onlinepubs.trb.org/onlinepubs/tcrp/docs/tcrp100/Part4.pdf);
[TCQSM worked example (tcrp_webdoc_6-b)](https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_webdoc_6-b.pdf);
[BC Transit design guide quoting the formula](https://www.bctransit.com/wp-content/uploads/2024/04/BCT-On-street-Infrastructure-Design-Guide-2024.pdf).

**Effective berths are sub-additive.** Multiple loading areas do not scale
linearly because a dwelling bus blocks access to upstream berths: HCM
Table 12-19 gives 2 berths = 1.75×, 3 = 2.25× a single berth (simulation
validation: 1.83×, 2.43×); TCQSM 3rd ed. illustrates "bus stop failure"
cascades.
Sources: [TCRP Report 26-A — Operational Analysis of Bus Lanes on Arterials](https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_rpt_26-a.pdf);
[TCQSM 3rd ed., ch. 7 (NAP)](https://nap.nationalacademies.org/read/24766/chapter/7).

**In-lane (on-line) vs pullout (bus bay) stops.** In-lane stops block the
travel lane during dwell (a capacity hit to general traffic, a speed benefit
to the bus — no re-entry merge); pullouts remove the bus from traffic but
impose re-entry delay and failure (yielding to traffic when merging back),
plus right-of-way cost. Stop placement near intersections splits near-side
(upstream) vs far-side (downstream): far-side pairs with TSP and preserves
intersection capacity; near-side lets buses dwell through red time.
Sources: [Pierce Transit Bus Stop Guidelines — in-lane vs pull-out](https://hdp-us-prod-app-rideprt-engage-files.s3.us-west-2.amazonaws.com/6117/6417/8635/PRT_Bus_Stop_Guidelines_3.1_final.pdf);
[Capacity approximations for near- and far-side bus stops in dedicated bus lanes, TR-B 2019](https://www.sciencedirect.com/science/article/pii/S0191261518305411).

**Connection:** an in-lane stop's blocking effect is *emergent* in our
engine — a dwelling bus is a stopped vehicle and the lane behind it queues
or lane-changes (implementation.md §6) — so the HCM berth-capacity behavior
should be *reproduced, not implemented*: a validation target is that a
single-berth in-lane stop under a bus flow saturates at roughly the TCQSM
formula's B_l. Pullouts are a *later* modeling layer: a short auxiliary lane
segment off the main line with re-entry merge — buildable from existing
lane/topology primitives, with the merge-gap behavior the engine already
has. Berth blocking (sub-additive effective berths) emerges once a stop has
>1 bus-length of standing room — a stop *area* (laneId, s-range) rather than
a point, matching SUMO's `startPos/endPos` design (competitors.md §SUMO).

## 5. Bus-lane design practice and warrants

- **NACTO Transit Street Design Guide** is the US design reference:
  dedicated *curbside* vs *offset* (one lane off the curb) bus lanes, with
  turn-management guidance (prohibit or separate right turns across the
  lane), bus bulbs (curb extensions that keep stops in-lane while preserving
  parking), and optional soft/hard separation with passing gaps.
  Source: [NACTO — Dedicated Curbside/Offset Bus Lanes](https://nacto.org/publication/urban-street-design-guide/street-design-elements/transit-streets/dedicated-curbside-offset-bus-lanes/);
  lane typology summary in [eScholarship qt3p17j577](https://escholarship.org/content/qt3p17j577/qt3p17j577.pdf).
- **Width warrants:** offset bus lane min 10 ft; curbside 11 ft min / 12 ft
  preferred (LA Complete Streets; Ann Arbor manual concurs).
  Sources: [LA Complete Streets Design Guide](https://planning.lacity.gov/odocument/c9596f05-0f3a-4ada-93aa-e70bbde68b0b/Complete_Street_Design_Guide.pdf);
  [Ann Arbor Downtown Street Design Manual](https://a2dda.org/wp-content/uploads/2023/08/A2DDA_StreetDesignManual_2022_FINAL-DRAFTlr.pdf).
- **Application warrant (LA):** offset bus lanes "on multi-lane arterial
  corridors where major transit service suffers from poor on-time
  performance as a result of peak hour congestion" — i.e. the warrant is a
  *measured* reliability problem, which is exactly a simulation A/B output.
- **Chicago practice:** Loop Link (2015, dedicated lanes + queue jumps,
  prepaid boarding later); Ashland/Western express service with TSP
  (2015–2018); a proposed Ashland BRT. Sources: [Streetsblog — Loop Link scope](https://chi.streetsblog.org/2015/09/14/despite-reduced-features-loop-link-should-still-prove-the-benefits-of-brt);
  [City of Chicago press release, Aug 2015](https://www.chicago.gov/city/en/depts/mayor/press_room/press_releases/2015/august/mayor-emanuel--cta-announce-faster-bus-service-for-ashland--west.html).

**Connection:** the entire design vocabulary (curbside/offset, queue jump,
bus bulb) reduces in our world to (a) a class mask on lanes (implementation.md
§3), (b) a stop area on a lane, and (c) short signal interventions (queue
jump = an early-release phase via `signal_set`, i.e. TSP's red truncation).
The bus-lane A/B experiment — "this arterial, with and without converting
the curb lane" — is a scenario overlay (ADR-0012 kustomize variants) over
one network, which is why the class mask must be a *network/scenario* datum,
not hard-coded.

## 6. Transit signal priority (TSP) practice

- **NTCIP 1211 is real**: *Object Definitions for Signal Control and
  Prioritization (SCP)* — the joint AASHTO/ITE/NEMA standard defining the
  data objects for priority-request generators (on the vehicle/transit
  management side) and priority-request servers (in the signal controller):
  request, check-in/check-out, grant, and status objects.
  Sources: [TCRP — Transit Signal Priority: Current State of the Practice (NAP, citing NTCIP 1211)](https://nap.nationalacademies.org/read/25816/chapter/3);
  [FHWA — Transit Signal Priority Research Tools (Li et al. 2008; reviews NTCIP 1211 SCP and defines SCP request scenarios)](https://rosap.ntl.bts.gov/view/dot/16521/dot_16521_DS1.pdf).
- **Primitive interventions:** green extension (hold the green past its
  point for an approaching bus) and red truncation / early green (shorten
  the conflicting phase or the current red); phase insertion/rotation and
  adaptive priority as the aggressive tier. FHWA's Signal Timing Manual
  ch. 9 stresses TSP alters timing *without abandoning coordination* —
  side-street phases are retimed, not skipped.
  Sources: [FHWA Signal Timing Manual ch. 9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm);
  [MWCOG TSP logic memo (green extension grant logic)](https://www.mwcog.org/uploads/committee-documents/vlpzw1020060303150522.pdf);
  [PSTA Central Ave BRT ConOps (red truncation, pedestrian-clearance constraints)](https://www.psta.net/media/4503/central-ave-brt-concept-of-operations_v60_28jan2020.pdf).
- **Conditional vs unconditional:** conditional grants priority only to
  buses that are late (or loaded, or off-headway); unconditional grants on
  detection. Conditional dominates US practice because it caps the
  cross-street cost.
- **Chicago is a conditional-TSP city:** "communicat[e] with signal
  controllers to hold green lights longer *if the buses are running late*"
  — Jeffery Jump pilot 2014 (7 intersections), South Ashland 2016 (40
  intersections), Western Ave through 2018–19 (100+), with a centralized TSP
  program (CCTSP, ATCMTD-funded) in design as of FY2024.
  Sources: [CTA FY17 Budget Book (Ashland/Western Express TSP)](https://www.transitchicago.com/assets/1/28/FY17_Budget_Book_FINAL.pdf);
  [Active Transportation Alliance — Speeding up Buses (deployment counts)](https://activetrans.org/wp-content/uploads/2024/12/SpeedingupBuses.pdf);
  [CTA FY2024 Budget Book (CCTSP)](https://www.transitchicago.com/assets/1/6/FY_2024_Budget_Book_(Web_Version).pdf).

**Connection:** implementation.md §4 maps each primitive onto the ADR-0037
machine: green extension = hold renewal on the serving phase; red truncation
= the sigctl phase-walk issued early; conditionality = client predicate over
the bus's schedule state. NTCIP 1211's generator/server split is isomorphic
to our bus-controller/signal-controller split over NATS — the "priority
request" is not a message we need, because the TSP controller *is* the
signal controller; but a two-controller variant (bus agent emits requests, a
junction agent grants) would replicate 1211's architecture exactly and is
the right shape if requests should be auditable objects in the record. The
starvation rails (`engine/sigctl.go:63-86`) bound the cross-street blast
radius that FHWA ch. 9 worries about.

## 7. CTA data sources for validation

- **GTFS (static):** full bus+rail schedule — stops, routes, trips,
  stop_times, calendar, frequencies, transfers — updated at service changes,
  free under the Developer License Agreement. This is the source for line
  geometry, stop spacing, scheduled headways, and timepoints.
  Source: [CTA Developer Center — GTFS](https://www.transitchicago.com/developers/gtfs/).
- **Bus Tracker API (bustime, proprietary — not GTFS-RT):** REST API (XML or
  JSON; v3 current, doc rev. 2025-04-21) with `getvehicles` (live positions
  per route), `getpredictions`, routes/directions/stops/patterns; positions
  updated roughly once a minute; free API key, Developer License Agreement
  and Terms of Use required.
  Sources: [CTA Developer Center — Bus Tracker](https://www.transitchicago.com/developers/bustracker/);
  [Bus Tracker API Developer Guide v3.0 (PDF, 2025-04-21)](https://www.transitchicago.com/assets/1/6/cta_Bus_Tracker_API_Developer_Guide_and_Documentation_2025-04-21.pdf).
- **Official CTA GTFS-RT feed (beta):** CTA publishes one at
  [transitdata.transitchicago.com](https://transitdata.transitchicago.com/) —
  service alerts, trip updates, and vehicle positions, in .pb and JSON,
  free API key required; announced in CTA's FY2024 Budget Book. It is NOT
  yet listed on the Developer Center (whose offerings are Train Tracker
  API, Bus Tracker API, Customer Alerts API, GTFS static, displays, open
  data sets — [CTA Developer Center](https://www.transitchicago.com/developers/)),
  which is how this research pass missed it; the site 403s headless
  fetches via bot protection, so the endpoints were not verified
  first-hand. Bustime polling archives remain a usable supplement
  (archived polls reconstruct trajectories/headways/dwells); third-party
  GTFS-RT converters exist for CTA rail (ctatt-gtfsrealtime precedent —
  [kurtraschke.com](https://kurtraschke.com/2015/01/legacy-avl-export)).
  (Sister-region contrast: Metra publishes official GTFS-RT — positions,
  trip updates, alerts — [Metra GTFS-RT endpoints](https://github.com/cailinpitt/chicago-transit-insights/blob/main/docs/METRA.md).)
  **Open question:** whether GTFS-RT VehiclePositions covers L RAIL, not
  just buses — unverified first-hand (needs a keyed fetch). A Mobility
  Database / OpenMobilityData cross-check is optional corroboration.
- **Validation uses:** bustime position archives give per-route speed
  profiles, stop-to-stop runtimes, dwell-at-stop estimates (position
  stationary intervals), and empirical headway distributions — the
  calibration corpus for the §2 dwell model and the bunching baseline.
  Ridership/boarding counts (for boarding rates) live in CTA's open data
  portal sets (ridership reports), listed on the same Developer Center page.

## Design patterns identified

- **Policy/mechanism split (the whole topic in one pattern):** the kernel
  owns mechanics (physics, guardrails, recorded intents); bus *policy* —
  stops, dwell, holding, TSP conditionality — is client-side. This is
  ADR-0008's doctrine applied to transit, and it is what makes control-law
  experiments replayable. The field analog: NTCIP 1211's generator/server
  separation (§6).
- **Detector pattern:** sigctl's virtual stop-line loops (fixed zones over
  TSSF positions) are the simulation equivalent of physical loops; TSP's
  check-in/check-out zones are the same shape with a class filter and an ETA
  (`engine/natsio/sigctl/sigctl.go:9-16`, `:160-223`).
- **Stop as area, not point** (SUMO/TCQSM): a (laneId, s-range) stop model
  gets berth blocking and multiple-berth capacity effects emergently; a
  point model can never represent them. Worth adopting from the start even
  in the "in-lane dwell first" milestone.
- **Control-point holding** (transit ops doctrine): hold only at a few
  designated stops (control points), not every stop — bounds passenger
  delay from holding. Maps to a sparse subset of the stop table in the
  controller's line definition.
- **Starvation rails as the TSP safety case:** bounded holds with logged
  lapse events (ADR-0037) are exactly the "do not wreck coordination" rule
  from FHWA STM ch. 9, enforced mechanically rather than by tuning.
