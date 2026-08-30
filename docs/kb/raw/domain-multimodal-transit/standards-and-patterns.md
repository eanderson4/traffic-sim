# Standards & Patterns: Multimodal Transit (trains + buses)

> Source: standards research + codebase tracing | Researched: 2026-08-24

Each standard/model below is connected to our implementation with an
explicit comply / deviate / simplify stance. Code citations are to the
working tree at HEAD 2bc98de (+ uncommitted ADR-0039 work) — see
implementation.md for the full trace.

---

## 1. GTFS (static schedule) — the transit authoring source

What it defines: a zipped set of comma-delimited text files. The core
for simulation: `stops.txt` (stop_id, lat/lon, location_type hierarchy —
stop/station/entrance/generic/boarding-area), `routes.txt` (route_id,
`route_type` enum — 0 tram / 1 subway / 2 rail / 3 bus / 11 trolleybus /
12 monorail), `trips.txt` (trip_id, service_id, direction_id, block_id —
"trips made using the same vehicle", shape_id), `stop_times.txt`
(arrival_time, departure_time, stop_sequence — times in HH:MM:SS measured
from noon−12h of the service day, may exceed 24:00), `shapes.txt`
(geospatial vehicle travel path), `frequencies.txt` (headway-based
service, or compressed fixed schedule), `calendar.txt`/`calendar_dates.txt`
(service days). Publishing practice: stable public URL, persistent ids,
feed valid ≥ 7 days out
([GTFS Schedule Reference, rev. 2026-04-27](https://gtfs.org/documentation/schedule/reference/)).

Connection to our implementation — **simplify, through a compiler**:

- GTFS's four core nouns have no counterpart in our demand grammar
  (implementation.md §4.3): Flow is origin-anchored rate demand, not a
  line. So GTFS belongs at the FRONT of a **GTFS→scenario-part compiler**
  (offline tooling, e.g. `scripts/gtfs2transit/`), emitting a new
  `transit/*.yaml` part type the scenario manifest references and hashes
  (ADR-0012 §1 part lists, `engine/scenario/scenario.go:71-73`). This
  matches the project's import philosophy: netconvert/.net.xml →
  netimport → compiled artifact with a report (ADR-0009), never a runtime
  parse of an external format.
- **Time model**: GTFS stop_times are wall-clock service-day times; our
  demand runs in SIM SECONDS with "7:30 AM" as a presentation alias
  (ADR-0012 §3, ratified at `docs/kb/decisions/ADR-0012-scenario-format.md:65-83`,
  permanent refusal of wall-clock at :195-199). The compiler picks a
  service date + window and re-bases times to t=0 sim seconds — exactly
  gtfs2pt.py's `--date` pattern
  ([SUMO GTFS import](https://sumo.dlr.de/docs/Tools/Import/GTFS.html)).
- **Geometry**: GTFS stops are lat/lon points, not (laneId, s). Stop
  placement is a MAP-MATCHING problem (nearest lane in travel direction,
  position along lane) — gtfs2pt.py's three fallback modes (fastest-path
  routing between stops; OSM ptLines+shapes; candidate stops file) are
  the design space ([same](https://sumo.dlr.de/docs/Tools/Import/GTFS.html)).
  shapes.txt gives the alignment to route-match against our lane graph;
  CTA's feed ships shapes.txt (verified 2026-08-24: 52 MB of shapes in
  the 99.6 MB zip; stop_times.txt is 373 MB uncompressed). CTA's
  frequencies.txt is a 42-byte stub — CTA publishes fully scheduled
  service, so the headway-program mapping below is for OTHER agencies
  (or for compressing CTA service into scenario slices ourselves).
- **route_type** maps onto our type registry: 3 (bus) → a `bus`
  VehicleType; 1 (subway — the L's heavy-rail lines in CTA's feed) → the
  rail path (sibling topic `domain-rail-operations`); 0 (tram) is n/a for
  Chicago. Verified first-hand in the 2026-08-24 CTA zip: 133 routes =
  125 bus (type 3) + 8 L lines (type 1: Red, Blue, Brn, G, Org, P, Pink,
  Y). The registry is a hardcoded 2-entry map today
  (`engine/cmd/serve/main.go:154`).
- **frequencies.txt** (headway service) ≈ our Flow `slices` semantics
  (piecewise-constant rates) — the one GTFS construct that maps nearly
  1:1 onto existing grammar, minus line identity.
- **block_id** (same physical vehicle across sequential trips) is the
  GTFS hook for vehicle reuse — matters if the transit controller keeps
  vehicles persistent across trips rather than despawning at terminals.
- Complication to respect: stop_times at non-timepoint stops are
  interpolated estimates, not observations — schedule-based validation
  must weight timepoints (this bit CTA prediction research:
  [michaelslice/CTA-Data-Analysis-Visualization](https://github.com/michaelslice/CTA-Data-Analysis-Visualization)).

Source: <https://gtfs.org/documentation/schedule/reference/>

---

## 2. GTFS Realtime — the validation corpus

What it defines: protobuf feeds of three entity types — **TripUpdates**
(schedule-relationship + per-stop arrival/departure delay or time),
**VehiclePositions** (vehicle id, trip ref, lat/lon or stop_id +
current status, timestamp, congestion_level), and **Alerts**
([GTFS Realtime Reference](https://gtfs.org/documentation/realtime/reference/)).

Connection to our implementation — **comply as consumers, offline**:

- GTFS-RT is our **calibration/validation oracle**, not a runtime input:
  archived VehiclePositions give measured bus run times, dwell times,
  and headway distributions to validate simulated lines against — the
  transit analogue of the NGSIM/drone trajectory corpus
  (`raw/domain-trajectory-datasets/`). TripUpdates give scheduled-vs-actual
  delay for validating dwell+traffic interaction.
- The engine itself never consumes realtime data (determinism,
  ADR-0005); RT archives feed scenario-authoring and post-hoc validation
  tooling, exactly like the `analysis/ngsim` precedent.
- VehiclePositions' vehicle-level trajectories double as a demand
  calibration source for the surrounding car traffic? No — RT covers
  transit vehicles only; car demand calibration stays with
  `domain-demand-modeling` (counts, LEHD). Keep the boundary clean.

Source: <https://gtfs.org/documentation/realtime/reference/>

---

## 3. CTA data offerings + license — the target city's actual feeds

What exists (verified 2026-08-24):

- **Static GTFS**: one zip covering CTA bus AND rail (L) —
  `https://www.transitchicago.com/downloads/sch_data/google_transit.zip`,
  documented at [CTA Developer Center](https://www.transitchicago.com/developers/gtfs/).
  Verified first-hand 2026-08-24 (99.6 MB download): contains agency,
  stops, routes, trips, stop_times (373 MB uncompressed), calendar,
  calendar_dates, shapes (52 MB), transfers, a 42-byte frequencies stub
  (i.e. fully scheduled service, not headway-based), and a copy of
  `developers_license_agreement.htm` inside the zip itself. Mirrored/
  indexed at [transit.land feed f-dp3-cta](https://www.transit.land/feeds/f-dp3-cta)
  ("Use allowed without attribution: Yes; Creating derived products
  allowed: Yes") and [catalog.data.gov](http://catalog.data.gov/dataset/cta-system-information-developer-tool-gtfs-data).
- **Official GTFS-RT** at [transitdata.transitchicago.com](https://transitdata.transitchicago.com/):
  "service alerts, trip updates, and vehicle positions. An API key is
  required." (The page 403s a headless fetch without a key — endpoint
  shape unverified first-hand; open question below.) CTA also still runs
  its proprietary **Bus Tracker API** (locations + predictions, XML/JSON)
  and **Train Tracker API** (arrival predictions)
  ([CTA Developer Center](https://www.transitchicago.com/developers/)).
  Historically the lack of an official RT feed forced community
  converters ([ctatt-gtfsrealtime, kurtraschke.com](https://kurtraschke.com/2015/01/legacy-avl-export))
  — that gap is now closed for bus. The Train Tracker API DOES offer
  vehicle positions: `ttpositions.aspx`
  (`lapi.transitchicago.com/api/1.0/ttpositions.aspx`) returns per-route
  live train runs with per-train lat/lon/heading/next-stop — the
  official rail-position source. What remains unverified first-hand is
  whether GTFS-RT VehiclePositions covers L rail (the GTFS-RT beta site
  403s headless fetches).
- **Metra** (commuter rail — adjacent, useful for grade-crossing and
  regional context): its own GTFS-RT at `gtfspublic.metrarr.com`
  ([Metra GTFS API page](https://metra.com/metra-gtfs-api)).

**License** — [CTA Developer License Agreement and Terms of Use](https://www.transitchicago.com/developers/terms/),
read in full 2026-08-24. Operative terms:

- §I.1: limited, non-exclusive, **revocable** license "to use, reproduce,
  distribute, display, process and create derivative works of CTA Data…
  **for the sole purpose of assisting mass transportation (i.e., bus or
  rail) riders or in furtherance of promoting public transportation**" —
  a PURPOSE restriction, not a blanket open license. A simulation engine
  used for transit advocacy/planning plausibly fits "promoting public
  transportation", but this is a judgment call, not settled law; flag for
  the license ADR.
- §II.3 / §III.3: no selling CTA Data as a standalone product; derivative
  apps stay yours.
- §III.2: caching allowed "to improve your application's user experience"
  with reasonable efforts to keep data up to date.
- §III.6: attribution OPTIONAL ("Data provided by Chicago Transit
  Authority" suggested wording).
- §V: on termination you must DELETE CTA Data and certify it.

Connection to our implementation — **comply via recipe-not-file**:

- The ODbL posture from ADR-0009 (integration-osm-extraction: ship the
  recipe + hashes, not the data) generalizes: CTA GTFS should be fetched
  by a script with a recorded fetch date + content hash, never vendored
  into the repo. The §V deletion-on-termination clause makes vendoring
  affirmatively risky for a public repo. A published baked demo built
  from CTA data is a "derivative work" the license expressly permits —
  but the purpose clause should be quoted in the demo's data credits.
- Scenario provenance: the GTFS fetch (URL, date, zip hash) belongs in
  the transit part's header or the scenario README, paralleling the
  netimport `SourceFile`/`Imported` provenance stamps
  (`engine/netimport/netimport.go:29-35`).

Source: <https://www.transitchicago.com/developers/>,
<https://www.transitchicago.com/developers/terms/>,
<https://transitdata.transitchicago.com/>,
<https://www.transit.land/feeds/f-dp3-cta>

---

## 4. TSP practice & standards — what "priority" concretely means

What the field defines:

- **TSP vs preemption**: TSP "modifies the normal signal operation to
  better accommodate transit vehicles while maintaining the coordinated
  operation and overall signal cycle length" — distinct from emergency
  preemption, which interrupts normal operation entirely
  ([TCRP Synthesis 83, Danaher 2010](https://nacto.org/wp-content/uploads/1-5_Danaher-Bus-and-Rail-Transit-Preferential-Treatments-in-Mixed-Traffic-TCRP-Synthesis-83_2010-sm.pdf)).
- **The canonical strategy set**: green extension (extend the serving
  phase until the transit vehicle clears, capped at a maximum
  extension), red truncation / early green (shorten the preceding
  phases), plus rarer phase insertion / special phases
  ([Garrow 1997, TTI, cited 152×](https://static.tti.tamu.edu/swutc.tamu.edu/publications/technicalreports/472840-00068-1.pdf);
  synthesis in [Kazemzadehazad et al. 2026](https://www.mdpi.com/2413-8851/10/3/132)).
- **Conditional vs unconditional priority**: unconditional serves every
  detected transit vehicle; conditional checks first — typically
  schedule adherence (only late buses get priority), occupancy, or
  headway state — and skips priority for vehicles that don't need it.
  Conditional is the modern default because it bounds the side-street
  and coordination costs (documented across the same TCRP Synthesis 83
  survey of agency practice).
- **The wire standard**: NTCIP 1211, "Object Definitions for Signal
  Control and Prioritization (SCP)" — SNMP-style management objects for
  managing MULTIPLE priority requests across vehicle classes (transit,
  emergency, fleet), priority-request lifecycle
  (request/check-in/serve/check-out), and coordination interaction
  ([NTCIP 1211 v01 PDF](https://www.ntcip.org/file/2025/03/1211v0138p-2008b_Final.pdf);
  explainer: [LYT](https://lyt.ai/blog/ntcip-1211-what-is-it-and-why-does-it-matter);
  research usage: [Li et al. 2008](https://rosap.ntl.bts.gov/view/dot/16521/dot_16521_DS1.pdf)).

Connection to our implementation — **simplify onto the ADR-0037 channel**:

- Green extension IS a `signal_set` hold/renewal on the current phase
  (implementation.md §6); red truncation is a `signal_set` switch to the
  transit phase (the sigctl reference controller's seamless transition
  walk — commanding intermediate clearance phases at natural durations,
  `engine/natsio/sigctl/sigctl.go:18-28` — is exactly the machinery a
  red truncation needs to remain honest).
- Our starvation rails (300 s cumulative chain bound, lapse events)
  already encode TSP's "maintain coordination / bound the cost"
  requirement at the KERNEL level — a nicer posture than field
  controllers, where the bound is cabinet config. The ADR-0037 rail
  scope caveat (per-PHASE bound; alternating phases can starve a
  movement as controller policy — ADR-0037 :311-321) applies to a TSP
  controller verbatim.
- **Conditional priority** is controller-side policy: read schedule
  state (the transit fleet controller knows its own lateness), then
  decide whether to request. No engine change needed — the signal grant
  question (director-grant today, stub `GrantSignal` — ADR-0037 M2
  round-3 note :662-666) is the only contract work, and class-aware
  detection needs only the TSSF class field
  (`engine/natsio/frame.go:41,120`).
- **Deviate from NTCIP 1211**: the SCP object model (request lifecycle
  with check-in/out, multi-class request queues) is a real-cabinet
  integration standard; our engine already resolves competing intents
  deterministically (ADR-0008 tie-breaks) and the verb channel provides
  idempotent request/ack. Adopting 1211's VOCABULARY for a
  `signal_request` verb payload (class, priority level, desired
  treatment, ETA) is worth it if TSP ever shares a cabinet with other
  priority classes; adopting the protocol itself is not — the NATS verb
  channel replaces the transport. Recorded as an open question for the
  bus-operations topic.
- Measured-expectation anchor: our own actuated-control bracket found
  +8.4% speed / +10.8% completions at survivable demand and NO lift to
  the oversaturated discharge ceiling (ADR-0037 M2 :689-701). TSP
  brackets should expect the same two-regime shape — priority helps the
  bus and its riders measurably, it does not repeal the MFD.

Source: <https://nacto.org/wp-content/uploads/1-5_Danaher-Bus-and-Rail-Transit-Preferential-Treatments-in-Mixed-Traffic-TCRP-Synthesis-83_2010-sm.pdf>,
<https://www.ntcip.org/file/2025/03/1211v0138p-2008b_Final.pdf>,
<https://static.tti.tamu.edu/swutc.tamu.edu/publications/technicalreports/472840-00068-1.pdf>

---

## 5. TCQSM — transit capacity & quality of service vocabulary

What it is: TCRP Report 165, *Transit Capacity and Quality of Service
Manual*, 3rd ed. (2013, Kittelson et al.) — the transit counterpart to
the HCM: capacity of bus/rail services and facilities from first
principles (dwell times, berth capacity, signal interaction), and
Quality-of-Service frameworks measured **from the passenger point of
view** (availability, comfort/convenience, reliability). Consistently
TCRP's most-downloaded report; a 4th edition research project (A-47)
was announced FY2022
([TCRP Rpt 165 ch.1 PDF](https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_rpt_165ch-01.pdf),
[front matter](https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_rpt_165fm.pdf),
[TCRP FY2022 announcement](https://onlinepubs.trb.org/onlinepubs/tcrp/docs/TCRP_FY2022_Project_Announcement.pdf)).

Connection to our implementation — **adopt the vocabulary, simplify the
methods**:

- TCQSM is where "person capacity" and "person delay" get their
  operational definitions (vehicles/h × persons/vehicle, dwell-driven
  stop capacity) — the vocabulary our person-weighted metrics should
  speak (implementation.md §5) rather than inventing parallel terms.
  Same posture as ADR-0014 took toward HCM: normative definitions pinned
  in the contract, LOS as a presentation skin
  (`docs/kb/decisions/ADR-0014-observability-metrics.md:172-178`).
- Its bus-stop capacity model (berths, dwell distribution, clearance
  time) and rail capacity model (minimum headway from station dwell +
  signal system) are the analytic ORACLES for our simulated stops/lines
  — same role LWR/CTM plays for road traffic
  (`raw/domain-macroscopic-flow-models/`). Detailed mechanics: siblings
  `domain-bus-operations` / `domain-rail-operations`.
- Deviation to state plainly: TCQSM methods are macroscopic/deterministic
  lookup procedures; ours is a microscopic simulation — TCQSM calibrates
  and sanity-checks, it does not drive dynamics.

Source: <https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_rpt_165ch-01.pdf>

---

## 6. Person-trip vs vehicle-trip metrics — how DOTs actually score transit projects

Field practice (with receipts):

- **Person delay = vehicle delay weighted by occupancy** — the
  definition is older than microsimulation: "To get the people delay, an
  average occupancy per vehicle has to be determined… People delay is,
  therefore, vehicle delay weighted by the average occupancy per
  vehicle" ([TRB Special Report on public transit right-of-way, 1975](https://onlinepubs.trb.org/Onlinepubs/trr/1975/546/546-002.pdf)).
- TSP evaluation is a multiple-criteria problem where "when the transit
  mode share is sufficiently high, increased delay to automobiles when
  transit receives signal priority could result in an overall
  [person-delay reduction]" ([UT Austin CTR 472840-00072-1](https://library.ctr.utexas.edu/digitized/swutc/472840-00072-1.pdf));
  Garrow 1997 measured ~50% per-person delay reduction from priority
  ([same TTI report](https://static.tti.tamu.edu/swutc.tamu.edu/publications/technicalreports/472840-00068-1.pdf)).
- Modern agency guidance formalizes it: "Passenger delay is a separate
  metric that weights the delay by the passenger load or ridership of a
  route" ([SCAG Transit Priority Best Practices, 2024](https://www.scag.ca.gov/sites/default/files/2024-05/3038_scag-rdtlstransitpriority_final.pdf)).
- Project-scoring frameworks weight PERSON throughput/delay directly:
  e.g. a congestion-mitigation scoring table with "Person Throughput"
  and "Person Hours of Delay" each at 50% weight
  ([captured scoring manual](https://manuals.plus/m/fcd268c34f66816095d6c826db54f7fd03a962e1cf780a2415c4bb4d264aa504_optim.pdf)).
- HCM Ch. on evaluating transit priority explicitly frames the trade:
  priority treatments "may result in increased delay to motorists… and
  possibly even other transit passengers" — evaluation must be
  person-based to be honest ([HCM excerpt](https://dl.icdst.org/pdfs/files/a468f662d28114d0e40a650be3333dc7.pdf)).
  Person-based signal timing optimization is an active research line
  ([Christofa/Skabardonis line, eScholarship](https://escholarship.org/content/qt0t4336f3/qt0t4336f3.pdf)).

Connection to our implementation — **comply, as a derived view**:

- The metric-side change is small by design (implementation.md §5.2):
  trip records already carry `TypeName` (`engine/metrics.go:138`); a
  persons-per-class attribute + a weighting pass yields person-delay,
  person-throughput, and the bus-lane story flip without touching the
  kernel, the CRC, or replay. The occupancy WEIGHT itself is a scenario
  input (average persons per transit vehicle per line — from CTA
  ridership stats), not simulated persons; that is the deliberate
  simplification vs SUMO/MATSim person simulation, and it matches how
  DOT practice uses average occupancies anyway.
- The A/B honesty implication: every transit-priority variant report
  should show BOTH vehicle-delay and person-delay tables — the whole
  point of the practice literature is that they can rank alternatives
  differently, and our runreport/whatif tooling currently emits only
  vehicle-denominated tables (ADR-0030).
- Where the occupancy number lives: a `vtypes/*.yaml` attribute
  (deferred since ADR-0012 M11, `docs/kb/decisions/ADR-0012-scenario-format.md:281-282`)
  hashed into scenario identity — so a sensitivity sweep over ridership
  assumptions is just scenario variants. Dynamic per-vehicle loads
  (crush vs empty) are a later refinement; average occupancy answers the
  policy question today.

Source: <https://onlinepubs.trb.org/Onlinepubs/trr/1975/546/546-002.pdf>,
<https://www.scag.ca.gov/sites/default/files/2024-05/3038_scag-rdtlstransitpriority_final.pdf>,
<https://library.ctr.utexas.edu/digitized/swutc/472840-00072-1.pdf>

---

## 7. Mode-share & elasticity napkin literature — coupling transit to car demand

Anchors:

- **Transit service elasticity**: "The elasticity of transit use with
  respect to transit service frequency (called a headway elasticity)
  averages 0.5, with greater effects where service is [better]" — Litman's
  metastudy ([Litman 2004, *Transit Price Elasticities and
  Cross-Elasticities*](https://www.sciencedirect.com/science/article/pii/S1077291X22003861),
  595+ citations). Fare elasticity consensus from the same literature:
  −0.2 to −0.5 short-run, −0.6 to −0.9 long-run
  ([summary in a later dissertation](https://escholarship.org/content/qt9bp6b51x/qt9bp6b51x.pdf)).
  BLS's transit-productivity review echoes the elasticity-estimation
  practice ([BLS MLR](https://www.bls.gov/opub/mlr/2018/article/productivity-in-transit-a-new-measure-of-labor-productivity-for-urban-transit-systems.htm)).
- **Downs–Thomson paradox**: improving road capacity can WORSEN both
  modes — drivers shift from transit, transit frequency/revenue falls,
  and the road re-congests (Downs 1962; Thomson 1977; Mogridge 1990
  London evidence; experimental confirmation
  [Dechenaux 2014](https://www.cambridge.org/core/journals/experimental-economics/article/traffic-congestion-an-experimental-study-of-the-downsthomson-paradox/AB7DC676CCD229FBF13AB1E7DFD44302);
  overview: [White Rose accepted manuscript](https://eprints.whiterose.ac.uk/id/eprint/121540/7/Accepted_2017_TRA_Overcoming%20the%20Downs-Thomson%20Paradox%20by%20transit%20subsidy%20policies.pdf)).
  The symmetric case (Sydney M4 widening vs rail decline) is documented
  with SACTRA 1994 induced-traffic evidence
  ([Zeibots/Petocz 2005](http://cfsites1.uts.edu.au/find/isf/publications/zeibotspetocz2005motorwaycapacity.pdf)).
- Practical implication of the pair: a transit improvement's CAR-traffic
  effect is second-order and demand-elastic; first-order napkin: a
  headway-halving (elasticity ≈ 0.5) lifts ridership ~40%, and only the
  fraction of those riders who were would-be drivers reduces car demand
  (the rest are induced trips + former walk/other).

Connection to our implementation — **apply as authored scenario
variants, never as runtime feedback**:

- Our experiment protocol compares scenario ARMS that share a demand
  program (ADR-0014 §7 paired seeds; ADR-0028's rejection of
  demand-as-a-function-of-live-state). Mode-shift therefore enters as a
  GENERATOR-side rule: "the bus-lane arm reduces car flows on parallel
  corridors by X% per the elasticity napkin" — authored, diffable,
  hashed, and bracketable, exactly the ADR-0028 demand-profile-library
  pattern. This is MATSim's mode-choice loop collapsed into scenario
  authorship (competitors.md §2) — defensible at napkin accuracy and
  honest about its uncertainty because X is an explicit, sweepable
  parameter rather than a hidden model.
- The demand-side attachment point is `domain-demand-modeling`
  (registered sibling) — this topic's deliverable is the RULE that
  transit variants may carry coupled demand deltas, plus the elasticity
  table to justify magnitudes.
- Downs–Thomson is the caution against overclaiming: a transit-priority
  demo that shows person-delay gains is honest; one that promises
  corridor car-congestion relief is making an equilibrium claim our
  single-run experiments cannot support.

Source: <https://www.sciencedirect.com/science/article/pii/S1077291X22003861>,
<https://www.cambridge.org/core/journals/experimental-economics/article/traffic-congestion-an-experimental-study-of-the-downsthomson-paradox/AB7DC676CCD229FBF13AB1E7DFD44302>,
<http://cfsites1.uts.edu.au/find/isf/publications/zeibotspetocz2005motorwaycapacity.pdf>

---

## Design patterns identified (cross-cutting)

- **Import/compile boundary (ETL with a report)** — netconvert→netimport
  for networks, GTFS→transit-part for lines: parse an external format
  once, emit our strict artifact + an honest report of what failed to
  map (netimport's `Report` precedent, `engine/netimport/netimport.go:37-74`;
  gtfs2pt.py's `gtfs_missing.xml`). Keeps the engine stdlib-pure and
  deterministic; keeps provenance auditable.
- **Everything privileged is an external client** — default driver,
  demand director, sigctl; transit fleet + TSP controller follow the
  same grants/verbs pattern (ADR-0008 §5), inheriting idempotency,
  record-plane logging, and replay.
- **Derived views over two primitives** — person-weighting as a metric
  derivation (ADR-0014 §1-2), not simulated persons; mode-share as
  authored demand variants, not runtime agents.
- **Class mask as the network's mode primitive** — SUMO allow/disallow,
  Aimsun reserved lanes, A/B Street lane types all converge on the same
  atom; ours is currently discarded at import (implementation.md §1).
