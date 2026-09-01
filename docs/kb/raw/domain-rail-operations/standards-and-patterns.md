# Standards & Patterns: Rail Operations

> Source: web research tied to implementation.md surfaces | Researched: 2026-08-24
> Each section connects to the codebase surface it would land on.

## 1. What netconvert emits for rail (the input contract we inherit)

netconvert's default OSM typemap (`osmNetconvert.typ.xml`) distinguishes five
railway types — **tram, subway, light_rail, rail, highspeed** — and sets the
edge `allow` to the matching vehicle classes; electrified track also permits
`rail_electric` and `rail_fast`. Optional `osmNetconvertRailUsage.typ.xml`
adds usage (main/branch/industrial/…). Parallel tracks are modeled as
**distinct edges**, never multi-lane edges; bidirectional single track is a
pair of geometry-reversed "superposed" edges marked `bidi`. Rail switches are
ordinary junctions; `rail_signal` and `rail_crossing` are junction *types*.
([SUMO Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html))

Our importer reads `allow` as a class whitelist already
(`engine/netimport/netimport.go:599-624`) — the data rail arrives in is the
same attribute road class restrictions use. The importer's `<edge>`/`<lane>`
structs (netimport.go:107-124) need no new fields to *see* rail lanes; the
decision is whether to keep them (implementation.md §6). For mid-track block
signals, SUMO's own placement guidance is the pattern we would follow:
split the edge at the signal location, or declare the node a signal type and
move the stop line upstream (`endOffset`) — either way **a signal exists only
at a node**, which matches our format's signals-bind-to-internal-lanes rule
(netimport.go:402-436) once we synthesize one-lane junctions at block
boundaries (decision D6).

## 2. Fixed-block signaling

- Track is divided into **blocks**; occupancy is detected (track circuits);
  a wayside/cab **aspect** tells the driver the state of the block(s) ahead.
  Classic 3-aspect progression: green (≥2 blocks clear) / amber (1 block
  clear, prepare to stop at next) / red (stop) — exactly the CTA ABS aspect
  set documented for the State Street Subway, with track trips for
  enforcement and **overlapped circuits** so a train overrunning one signal
  still has room to stop ([chicago-l.org signals](https://www.chicago-l.org/operations/signals/signals.html)).
- Block layout is derived from **braking distance**: each block must be at
  least the stopping distance from line speed at the worst (wet-rail) braking
  rate, plus safety overlap; more aspects (4-aspect: double-yellow) let the
  same braking distance span shorter blocks → shorter headway. Block size vs.
  capacity trade-off and aspect progression:
  [moving-blocks fundamental-diagram paper, ScienceDirect 2021](https://www.sciencedirect.com/science/article/pii/S0968090X21000188);
  blocking-time stairway method (the time-over-distance occupancy graph used
  to derive minimum headways):
  [RWTH capacity thesis](https://publications.rwth-aachen.de/record/713848/files/713848.pdf).
- Map to our engine: a block = a lane chain between two synthetic signal
  junctions; occupancy = lane.vehs membership (front bumper — with the §1.3
  tail caveat, implementation.md L2); aspect progression = external
  controller state machine issuing `signal_set` (sigctl.go:168). The kernel's
  green-never-enter-a-blocked-box rule (rightofway.go:261-288) is the home
  signal's absolute-stop semantics for free.

## 3. Moving block / CBTC

- CBTC per **IEEE 1474.1**: continuous automatic train control with
  high-resolution train location *independent of track circuits*,
  continuous bidirectional train↔wayside communication, and train-borne +
  wayside processors ([ETSI TR 103 442, quoting IEEE 1474.1](https://www.etsi.org/deliver/etsi_tr/103400_103499/103442/01.01.01_60/tr_103442v010101p.pdf)).
  Moving block: the safe separation is a braking-distance envelope behind the
  leader that moves continuously, eliminating block granularity; continuous
  cab signaling already shortens headways by removing signal-sighting time
  and adapting to actual braking distance
  ([TU Braunschweig RTC eBook](https://leopard.tu-braunschweig.de/servlets/MCRFileNodeServlet/dbbs_derivate_00055797/eBook%20RTC%201-0.pdf)).
  Deployed systems advertise **90 s headways**
  ([Siemens Trainguard MT](https://assets.new.siemens.com/siemens/assets/api/uuid:7c741747-6f44-4775-ad1f-609067bf9138/31-mass-transit-brochure-newbrand-2021-v2.pdf)).
- Map to our engine: SUMO models moving block as "disable the block guard,
  let the safe-stop car-follow model keep distance"
  ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html)) — and
  that is *precisely* our ADR-0025 safety gate (engine.go:780-813: the
  discrete Krauss bound `v_max = −b·Δt + √((b·Δt)² + v_lead² + 2·b·gap)`)
  with b = rail emergency decel. **The kernel's moving-block approximation
  already exists**; fixed-block discipline is what must be added, and it can
  live in the controller.

## 4. Minimum headway theory — and what Loop headways imply

Canonical fixed-block minimum headway (station-stop bound dominates on
metros; see [TCRP Report 13 review of North American rail transit capacity
methodologies](https://onlinepubs.trb.org/onlinepubs/tcrp/tcrp_rpt_13-e.pdf),
blocking-time method per
[RWTH](https://publications.rwth-aachen.de/record/713848/files/713848.pdf)):

```
H_block   ≈ t_sight/switch + v/b + (L_train + L_overlap)/v          (wayside)
H_station ≈ t_dwell + t_approach + t_clear                          (station)
H_line     = max(H_block, H_station, H_junction)                    (line)
```

CTA Loop numbers plugged in (consist 117 m — §6; v ≈ 15 m/s through Loop
curves; service b ≈ 1.0 m/s², a typical metro value — *unverified for CTA
specifically, see open questions*):

- braking distance v²/2b ≈ 112 m; braking time 15 s; consist clear time
  ≈ 8 s; with ~10–15 s operating margins H_block ≈ 35–45 s.
- Station bound with a 20–30 s dwell, approach+reoccupation margins:
  H_station ≈ 60–120 s (fixed-block, manually driven).
- Observed scheduled headways on Loop trunks: ~3–8 min (180–480 s) — CTA's
  own service standard allows peak rail headways of 3–15 min
  ([Boston MPO peer comparison of CTA standards](https://www.bostonmpo.org/data/calendar/pdfs/2014/MPO_0904_Core_Efficiencies_Study_Chapter2.pdf);
  [CTA Service Standards, May 2023](https://www.transitchicago.com/file.aspx?DocumentId=8041)
  — rail OTP is measured against *scheduled headway*, not timetable points).

**Derivation the ADR can quote:** the Loop runs at roughly 2–4× its
block-limited minimum headway, so plain-line block capacity is *not* the
binding constraint — the binds are (a) station dwell/reoccupation, (b) the
flat junctions where lines merge (Tower 18, Tower 12 — §5), and (c) terminal
turnbacks. A simulation that gets dwells and interlocking conflicts right
will reproduce published run times; one that only gets blocks right will
overstate capacity. GTFS validation targets (§8) are therefore dwell- and
junction-sensitive — a convenient match to our engine's strengths.

## 5. Interlocking basics

- An **interlocking** is "an arrangement of signals and signal appliances so
  interconnected that their movements must succeed each other in proper
  sequence" ([AREMA definition via transittraining.net course preview](https://www.transittraining.net/images/uploads/document_previews/Course_106_Introduction_and_Overview_of_Interlockings_PREVIEW.pdf)).
- **Route locking**: electric locking preventing movement of any interlocked
  switch/movable-point frog/derail in a selected route while the route is
  set and occupied ([AREMA C&S Manual 1.1.1 table, reproduced in Metrolinx
  General Instructions](https://assets.metrolinx.com/image/upload/v1737995080/Documents/Engineering/General_Instructions_GI.pdf)).
- **Approach locking**: route locking that comes into force when a train
  occupies the approach section — a cleared signal cannot be taken away and
  the route released instantly once a train is approaching; a time release
  must run ([Pachl railway glossary](http://www.joernpachl.de/glossary.htm)).
- **Home (absolute) vs automatic (permissive) signals**: CTA practice
  illustrates both — interlocking home signals are absolute ("stop and
  stay"), automatic block signals are permissive (stop, then proceed per the
  number-plate rule; the 1959 CTA pamphlet's "X plate" distinction)
  ([chicago-l.org signals](https://www.chicago-l.org/operations/signals/signals.html)).
  Our `sigGate` red is absolute (a wall, no stop-and-proceed) — permissive
  stop-and-proceed would be a controller behavior, not a kernel state.
- **Tower 18** (Lake & Wells, NW Loop corner) and **Tower 12** (Van Buren &
  Wells, SW corner) are the Loop's two remaining flat junctions where lines
  merge/cross; CTA's own track-renewal materials name both
  ([CTA Loop Track Renewal](https://www.transitchicago.com/looptrackrenewal/);
  histories: [tower18](https://www.chicago-l.org/operations/towers/tower18.html),
  [tower12](https://www.chicago-l.org/operations/towers/tower12.html) —
  Tower 12's 2006 Pink Line change put scheduled moves through the junction
  that needed "a measure of human decision-making when routing trains",
  i.e. schedule priority is literally dispatched by hand there).
- Map to our engine: route locking ≈ holding competing approaches red until
  the protected train's lane chain clears (controller-side, via `signal_set`
  bounded holds — the ADR-0037 lapse machinery doubles as the time release);
  approach locking has no kernel analog and would be controller policy
  (don't cancel a hold once a train is within braking distance — the
  controller knows v, gap, and b). See implementation.md §3-§4 for what
  breaks if you try to use rowGate instead.

## 6. CTA operating characteristics (scenario parameters)

- **Cars**: all series 48 ft (14.6 m) long, stainless steel,
  semi-permanently coupled into **married pairs**; train lengths 2–8 cars by
  demand and platform length; design allows 10-car consists
  ([CTA Rail Car Maintenance Plan, NTSB docket](https://data.ntsb.gov/Docket/Document/docBLOB?ID=40423963&FileExtension=.PDF&FileName=Rail%20Car%20Maintenance-Master.PDF)).
  5000-series: 48'0" long, 8'8"–9'4" wide, 12'0" high
  ([chicago-l.org 5000-series roster](https://www.chicago-l.org/trains/roster/5000mkII.html)).
  ⇒ consist lengths 29 m (2-car) to **117 m (8-car)**; 117 m is the spike
  number used throughout implementation.md.
- **Signaling**: most of the system runs **ATC** — audio-frequency track
  circuits, continuous cab signal with speed commands, overspeed enforcement
  ([NTSB RAB1102](https://jonroma.net/media/rail/accident/usa/ntsb/RAB1102.pdf);
  [NTSB RAR7710: Loop operated by cab signal indications of automatic block +
  ATC with overspeed](https://www.ntsb.gov/investigations/AccidentReports/Reports/RAR7710.pdf));
  the Dearborn Subway and Congress branch still run legacy 3-aspect ABS with
  track trips; wayside trip stops enforce red at interlockings
  ([NTSB RAR1501 §2.4.2](https://www.ntsb.gov/investigations/accidentreports/reports/rar1501.pdf));
  the Loop's train-control system is being replaced
  ([CTA press release](https://www.transitchicago.com/cta-to-replace-signal-and-train-control-systems-on-loop-elevated-lines/)).
  ⇒ CTA is *closer to moving-block-ish cab signaling than to wayside fixed
  block* — which flatters our safety-gate approximation (§3).
- **Service**: Red/Blue run 24 h ([CTA service overview](https://www.transitchicago.com/welcome/));
  headway standards §4. Scheduled dwells: not published as such — the
  10–30 s figure is the standard metro planning band and CTA run times imply
  it; **mark unverified, GTFS stop_times give the empirical distribution** (§8).
- **Platforms**: consist length is limited by platform length (maintenance
  plan above); most CTA platforms berth 8 cars ≈ 117 m + tolerance.

## 7. FRA grade-crossing rules at gate-as-signal resolution

(Deferrable edge — notes for the signal-phase model only.)

- **49 CFR 234.225**: an active warning system must provide **no less than
  20 s of warning** before the crossing is occupied by rail traffic
  ([FRA/FHWA Highway-Rail Crossing Handbook, 3rd ed.](https://railroads.dot.gov/sites/fra.dot.gov/files/2024-10/GXHandbook2019FRAFHWA.pdf),
  also quoted in [VT thesis on crossing timing](https://vtechworks.lib.vt.edu/bitstreams/c1a39712-d142-4a3a-ae9c-2d4042354af9/download)).
- **49 CFR 234.223**: gate arm starts downward **≥3 s after** flashing lights
  begin; reaches horizontal before the train arrives (same sources).
- The Handbook's design factors (track clearance distance +1 s per 10 ft,
  approach speed, constant-warning-time train detection) map onto: trigger
  radius on the rail approach (controller watches train position/speed over
  TSSF), gate-down = red phase with a ≥20 s minimum pre-arrival hold, gate-up
  = phase release after the consist's *tail* clears (SUMO's `opening-delay`
  3 s is the same idea — competitors.md §1).
- Chicago relevance: the L is grade-separated (no crossings on the Loop);
  Metra/UP and a few CTA fringe crossings exist. **Defer as documented.**

## 8. GTFS as scenario source and validation corpus

- CTA publishes a full GTFS schedule feed — tables include stops, routes,
  trips, **stop_times**, calendar, calendar_dates, **frequencies**, transfers
  ([CTA GTFS developer page](https://www.transitchicago.com/developers/gtfs/);
  table list corroborated by [ITS International on CTA's passenger-information
  systems](https://www.itsinternational.com/feature/pioneering-new-passenger-information-systems)).
- Pattern (proven by MATSim, competitors.md §4): GTFS trips → spawn schedule
  (director spawn verbs per trip), stop_times → authored dwell points,
  frequencies.txt → headway-based demand where explicit trips are absent;
  shapes.txt → route lane chains on the imported rail graph.
- Validation use: published **run times between stops and scheduled headways
  are the ground truth** the simulated rail layer should reproduce (paired
  with §4's derivation: errors will concentrate in dwells and junction
  conflicts). scheduleStats.py is SUMO's equivalent checker
  ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html)) — a
  small `gtfs-check.py` against our trip records (ADR-0014) would be the
  same instrument.
- ODbL/licensing: GTFS feeds are data, not code; CTA's developer terms apply.
  Scenario manifests would reference derived dwell/headway tables, matching
  the recipe-not-file posture of ADR-0009.

## Anti-patterns to avoid (connected to implementation.md)

- **Driving trains with unmodified car IDM**: equilibrium gap s0+vT puts
  trains ~26 m apart at 15 m/s — inside any safe separation. The kernel gate
  rescues *physics*, not *operations*; block discipline is controller policy
  (implementation.md §1.5).
- **Modeling a hold as "controller keeps accel 0"** at an interlocking:
  capacity-blocked + head-of-lane + 300 s = strand removal
  (implementation.md §2). Holds belong on the signal plane (red aspect),
  which ADR-0034 explicitly exempts.
- **Treating the consist as a point for junction occupancy**: SUMO documents
  train-length effects as first-order (block clear times, min speed over all
  occupied edges); our front-bumper model needs either the exit-room rule's
  implicit cover or an explicit fix (implementation.md L2).
- **Importing rail without a class mask**: one shared lane graph with no
  class field means cars on track and trains on road (implementation.md §6).

## Open questions (unverifiable in this pass)

1. CTA service/emergency braking rates and accel curves per series — needed
   for the train VehicleType (a, b, V0). No public spec found; 5000-series
   AC traction suggests a ≈ 1.0–1.3 m/s² but **unverified**.
2. Actual CTA block lengths / signal spacing on the Loop (needed for
   synthetic-junction placement, decision D6). Possibly derivable from the
   ATC replacement RFP docs; not found in this pass.
3. Scheduled dwell distributions per station — recoverable from CTA GTFS
   stop_times deltas (arrival vs departure fields where both are given), not
   yet computed.
4. Whether netconvert OSM import of the Loop yields single-lane edges per
   track for the two-track elevated structure (expected yes per the
   distinct-edges convention) — check at import spike time.
