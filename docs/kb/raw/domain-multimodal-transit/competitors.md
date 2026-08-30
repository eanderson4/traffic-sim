# Competitors: Multimodal Transit (trains + buses)

> Source: web research + codebase analysis | Researched: 2026-08-24

## Competitive Landscape

Transit modeling splits the simulator world cleanly: **microscopic road
simulators bolt transit on as special vehicles with stops and schedules**
(SUMO, Vissim, Aimsun), while **agent-based demand simulators treat
transit as a first-class mode competing for travelers** (MATSim,
SimMobility). Nobody does both at lane-level fidelity with a live
controller bus and verified replay — which is exactly the quadrant
traffic-sim occupies (see `raw/domain-simulator-landscape/`). The transit
question for this project is therefore not "who do we copy" but "which of
the two layers — transit as vehicles, transit as mode-share — do we
build, and in what order". The competitors below show both layers, their
integration seams, and their known failure modes.

---

## SUMO (Eclipse, EPL-2.0) — the closest prior art, and our import oracle

What it does for transit:

- **Stops as lane-anchored infrastructure**: `<busStop id lane startPos
  endPos>` — an area on a lane, with `personCapacity` (default
  `min(6, length×2.4)`) that can jam the upstream sidewalk when exceeded,
  optional `access` child elements for pedestrian access from other
  lanes, and a `<trainStop>` alias with identical semantics
  ([SUMO Public Transport docs](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)).
- **Schedules via stop attributes**: vehicles/trips/flows carry stop
  lists with `duration` (minimum dwell) and `until` (earliest
  departure — the schedule); flows shift `until` per vehicle; looped
  routes via `repeat`/`cycleTime`. `arrival` enables delay computation
  in stop-output; `extension` caps boarding-driven dwell overrun
  ([same page](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)).
- **Persons and intermodal routing**: full person simulation —
  `personTrip` intermodal router chains walking + transit + car legs
  against the loaded schedule; TraCI has a `person` domain alongside
  `vehicle` ([TraCI docs](https://sumo.dlr.de/docs/TraCI.html)).
  Caveat: stops defined mid-run via TraCI/rerouters are invisible to the
  intermodal router — the schedule must be fully defined at load
  ([PT docs, caution note](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)).
- **GTFS import**: `gtfs2pt.py` converts a GTFS feed for a chosen date
  into stops (.add.xml) + vehicles/routes (.rou.xml); three route-mapping
  modes (fastest-path between stops; OSM ptLines via shapes.txt;
  candidate stops from `--ptstop-output`); mode filtering; a
  `gtfs_missing.xml` report of what failed to map
  ([SUMO GTFS import docs](https://sumo.dlr.de/docs/Tools/Import/GTFS.html)).
  `ptlines2flows.py` synthesizes schedules by running a calibration
  simulation in the background.
- **Rail simulation**: rail signals model block sections / driveway
  protection, with a moving-block option and railSignalConstraints for
  schedule-based train order; v1.22.0 (Feb 2025) cut rail simulation time
  ~50-75% and fixed deadlocks
  ([Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html),
  [1.22.0 release notes](https://www.eclipse.org/lists/sumo-user/msg14601.html)).
  Deep mechanics are the sibling topic `domain-rail-operations`'s beat.
- **Vehicle-class permissions per lane** — the `allow`/`disallow`
  attributes our own netimport reads (`engine/netimport/netimport.go:121-122`)
  are SUMO's class model; SUMO enforces them at runtime (a bus lane
  rejects cars), we compile them away (implementation.md §1).

vs this project:

- **Steal**: the stop-as-lane-area shape (a stop is (laneId, s-range) —
  our addressing already is `(laneId, s)`, so a transit stop is a
  network-annotation, not a new geometry type); the `until`/`duration`
  dwell+schedule pair (maps onto our intents: a held accel=0 with a
  controller-side release time, SIM SECONDS — our demand grammar already
  bans wall clock, ADR-0012); `gtfs2pt.py`'s three-route-mapping fallbacks
  as the design space for our GTFS→scenario compiler; the missing-elements
  report as an honesty pattern (our import-report precedent,
  `engine/netimport/netimport.go:39-74`).
- **Avoid**: TraCI's blocking-barrier control model (11× slowdown —
  ADR-0008 :14-17 already cites it as the failure mode our async intent
  contract exists to avoid); persons-in-the-simulation as a v1 scope
  (SUMO's intermodal person sim is a whole second simulation layered on
  the vehicle sim — our person-weighting can be a metrics derivation,
  implementation.md §5); SUMO's stop definitions living in
  "additional files" outside the route/demand files — our scenario
  manifest-of-parts is cleaner (ADR-0012).
- **Notable**: SUMO is our import oracle (ADR-0009), so every SUMO
  transit feature arrives through .net.xml/additional-file channels we
  already parse or deliberately skip — the `xmlLane.Allow/Disallow`
  discard being the live example.

Source: <https://sumo.dlr.de/docs/Simulation/Public_Transport.html>,
<https://sumo.dlr.de/docs/Tools/Import/GTFS.html>,
<https://sumo.dlr.de/docs/Simulation/Railways.html>,
<https://sumo.dlr.de/docs/TraCI.html>

---

## MATSim (TU Berlin / ETH Zürich, GPL) — the mode-share layer's home

What it does for transit:

- **Agent-based multimodal demand**: every simulated person has a daily
  plan (activity chain) and chooses modes; transit competes with car on
  generalized cost, so mode share is an OUTPUT, not an input. Transit
  vehicles run on the network per a `transitSchedule.xml` (stops, lines,
  departures) with `transitVehicles.xml` capacity/vehicle types; agents
  board/alight, and vehicle capacities bind (a full bus leaves agents
  behind).
- **GTFS→MATSim pipelines**: pt2matsim converts GTFS/HAFAS/OSM into a
  fully network-mapped transit schedule (stop→link matching, route
  paths through the network, validation/editing tools)
  ([pt2matsim GitHub](https://github.com/matsim-org/pt2matsim));
  GTFS2MATSim is the lighter-weight direct converter.
- **Schedule-based routing**: SwissRailRaptor, a RAPTOR-algorithm
  transit router developed by SBB, now MATSim's default transit router
  ([MATSim SBB showcase](https://matsim.org/examples/sbb/),
  [matsim-sbb-extensions](https://github.com/SchweizerischeBundesbahnen/matsim-sbb-extensions)).
- **Adaptive/paratransit supply**: the minibus contribution evolves
  paratransit lines to serve demand where no formal schedule exists
  ([minibus README](https://github.com/matsim-org/matsim-libs/blob/main/contribs/minibus/README.md)).

vs this project:

- **Steal**: the *conceptual layering* — transit supply (lines/schedules)
  and mode choice are separate concerns, and only the supply layer
  belongs in our engine. For mode-share coupling, MATSim proves the
  demand-side feedback loop (better transit → fewer car trips) is a
  demand-program input for us (a scenario VARIANT with shifted flows),
  not an engine mechanism — matching our architecture, where demand is a
  director program and A/B arms must share it (ADR-0028's rejected
  "demand as a function of live state" argument applies verbatim:
  mode-shift-inside-the-run would break replay determinism and arm
  comparability).
- **Avoid**: the agent/population machinery itself (synthetic
  populations, plan scoring, replanning iterations) — that is a
  different simulator category than ours; MATSim's queue-model traffic
  dynamics (no lane-level car-following — its "mobsim" resolution is too
  coarse for our decision-grade lane metrics); GPL licensing means no
  code reuse regardless (our landscape research recommends MIT/Apache —
  `raw/domain-simulator-landscape/`).
- **Use as reference data**: CTA ridership + GTFS give us MATSim-quality
  calibration targets without MATSim.

Source: <https://github.com/matsim-org/pt2matsim>,
<https://matsim.org/examples/sbb/>,
<https://github.com/matsim-org/matsim-libs/blob/main/contribs/minibus/README.md>

---

## PTV Vissim (commercial) — the practitioner benchmark for on-street transit

What it does for transit:

- First-class **PT lines**: fixed routes with timetables, stop dwell
  distributions (boarding/alighting-driven), stops with berth logic;
  vehicles interact microscopically with all traffic
  ([PTV Vissim product page](https://www.ptvgroup.com/en-us/products/ptv-vissim)).
- **Transit signal priority as a shipping feature**: RBC
  (ring-barrier-controller) TSP and preemption — detectors, priority
  requests, green extension/red truncation logic programmed into the
  signal controller model; PTV sells a dedicated training course on it
  ([TR-T0252 course](https://training.ptvgroup.com/en/courses/tr-t0252)).
  Real deployments report e.g. 15% bus travel-time improvement from
  dedicated lanes + signal priority in Marrakesh
  ([PTV case study](https://blog.ptvgroup.com/en/user-insights/from-gridlock-to-flow-ptv-vissim-transforms-traffic-in-marrakesh/)).
- Pedestrian integration via Viswalk (out of our scope).

vs this project:

- **Steal**: the evaluation frame — Vissim's transit pitch IS "test
  bus/tram operations and transit signal priority in a realistic
  microscopic model so you can cut delay, stabilize headways"
  ([Vissim FAQ](https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs)).
  That is precisely our civic-advocacy use case (VISION.md:39-42) with
  transit added; the competitor's existence proof sets the bar for what
  our TSP demo must show.
- **Avoid**: monolithic closed scenario files (our manifest-of-parts is
  the deliberate counter-design, ADR-0012 :20-31 — "Vissim's monolith is
  the negative control"); GUI-first authoring; license cost as a
  barrier to the advocacy audience.
- **Gap we hold**: Vissim has no verified-replay story (recordings as
  first-class, CRC-checked artifacts) and no open contract for external
  controllers — our ADR-0037 channel + bracket tooling is a TSP research
  harness Vissim users script around, not with.

Source: <https://www.ptvgroup.com/en-us/products/ptv-vissim>,
<https://training.ptvgroup.com/en/courses/tr-t0252>,
<https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs>

---

## Aimsun Next (commercial) — micro + demand-model transit in one suite

What it does for transit:

- **Transit lines in the micro sim**: "vehicles which follow fixed
  routes, run to a timetable, use lanes reserved for transit vehicles,
  and stop at transit stops"; a transit plan = lines + timetables
  (departure schedules, stop times, vehicle types per departure)
  ([Aimsun Next manual — Defining Transit](https://docs.aimsun.com/next/26.0.0/UsersManual/PublicTransportEditing.html),
  [Microscopic Simulator Transit Modeling](https://docs.aimsun.com/next/23.0.2/UsersManual/MicrosimulationPublicTransportModelling.html)).
- **Stop types**: normal / bus bay (pullout) / terminal, with length and
  docking capacity — the stop-as-capacity-bottleneck model
  ([Transit Stops manual](https://docs.aimsun.com/next/26.0.0/UsersManual/PTStopEditing.html)).
- **Static transit assignment**: demand-side modeling of which travelers
  use transit, with boarding cost functions — the MATSim-like layer
  inside a micro suite
  ([same manual page](https://docs.aimsun.com/next/26.0.0/UsersManual/PublicTransportEditing.html)).
- Reserved lanes as a lane attribute — the class-mask concept,
  commercially normalized.

vs this project:

- **Steal**: the stop taxonomy (curbside / bay / terminal is the right
  fidelity ladder for a lane-level engine — bay vs curbside changes
  whether a dwelling bus blocks its lane, which is exactly the kind of
  question our engine should answer); per-departure vehicle types (an
  L run vs a bus run from the same line); the transit-plan-as-one-file
  authoring unit (≈ one of our scenario parts).
- **Avoid**: the everything-suite sprawl (three simulation levels in one
  model); static assignment as an engine feature — for us that is
  analysis tooling over scenario variants.

Source: <https://docs.aimsun.com/next/26.0.0/UsersManual/PublicTransportEditing.html>,
<https://docs.aimsun.com/next/26.0.0/UsersManual/PTStopEditing.html>,
<https://docs.aimsun.com/next/23.0.2/UsersManual/MicrosimulationPublicTransportModelling.html>

---

## SimMobility (MIT/SMART) — integrated multi-timescale agent sim with real transit ops

What it does for transit: models "millions of agents, from pedestrians
to drivers, from phones and traffic lights to GPS, from cars to buses
and trains" across long/mid/short-term timescales; mid-term has been
used for rail-transit disruption management studies (Singapore MRT) and
transit pricing studies; open source
([MIT MFC page](https://mfc.mit.edu/simmobility/),
[simmobility-prod GitHub](https://github.com/smart-fm/simmobility-prod),
[Adnan et al. 2016, TRID](https://trid.trb.org/View/1393000),
[NEL disruption study](https://trid.trb.org/view/1439637)).

vs this project: same lesson as MATSim at higher integration cost — the
agent-based demand stack is a different product. **Steal**: transit
disruption/headway studies as a use-case template (our replay + bracket
tooling is well-shaped for "incident on the Red Line, what does the bus
bridge cost the road network"). **Avoid**: the multi-codebase,
distributed-simulation architecture; academic-maintenance risk.

Source: <https://mfc.mit.edu/simmobility/>,
<https://github.com/smart-fm/simmobility-prod>

---

## A/B Street (open source, Apache-2.0) — the advocacy-adjacent cautionary tale

What it does for transit: fixed-route buses on OSM-derived maps with
bus/bike/turn/parking lane types and transit stops rendered; the
advocacy framing ("cities friendlier to walking, biking, and public
transit") matches our civic use case
([abstreet GitHub](https://github.com/a-b-street/abstreet),
[A/B Street docs](https://a-b-street.github.io/docs/software/abstreet.html)).
Transit support is thin: buses are vehicles on fixed routes; no
schedules/ridership/person metrics of consequence.

vs this project: **Steal**: lane-type-first map model (bus lanes are a
lane attribute, editable in the UI) — validation that class masks are
the right primitive. **Avoid**: the all-in-one game binary; A/B Street's
own history shows the advocacy tool absorbs the simulation engine
(funding/scope drift — its retrospectives are candid). Our
replay/metrics discipline is the differentiator.

Source: <https://github.com/a-b-street/abstreet>,
<https://a-b-street.github.io/docs/software/abstreet.html>

---

## Positioning Summary

| | Transit as vehicles (stops, schedules, mixed traffic) | Transit as mode (ridership, mode share) | Lane class masks | GTFS import | TSP | Replay/verification | License |
|---|---|---|---|---|---|---|---|
| **SUMO** | Full — busStop infra, until/duration schedules, flows, rail block model | Person sim + intermodal router (full second sim) | Runtime-enforced allow/disallow | gtfs2pt.py (3 mapping modes) | Via TraCI trafficlight domain | State save/load, no intent-log replay | EPL-2.0 |
| **MATSim** | Schedule-driven transit vehicles w/ capacities | **The core product** (agent mode choice) | Link-level mode restrictions | pt2matsim / GTFS2MATSim | Not the focus | Coarse; stochastic replanning | GPL |
| **Vissim** | Full commercial polish | Add-on assignment | Reserved lanes | Via Visum | **Shipping feature (RBC TSP)** | No verified replay | Commercial |
| **Aimsun** | Lines/timetables/stop berths | Static transit assignment | Reserved lanes | Yes | Controller scripting | No verified replay | Commercial |
| **SimMobility** | Mid/short-term buses+trains | Agent-based, multi-timescale | Yes | Custom | Research code | No | Open (academic) |
| **A/B Street** | Fixed-route buses only | No | Lane types, editable | No | No | Deterministic-ish, no record plane | Apache-2.0 |
| **traffic-sim (today)** | None — types car/truck only, class info discarded at import (implementation.md §1) | None (demand is authored programs) | Not compiled | None | Signal_set channel exists, no priority logic (implementation.md §6) | **Bit-exact intent/verb replay, CRC-verified** | (license ADR pending) |
| **traffic-sim (planned)** | Transit as external fleet controllers + GTFS-compiled scenario parts | As scenario-variant demand programs (MATSim lesson: engine stays out) | Per-lane masks in compiled network (decision candidate) | GTFS→transit part compiler (decision candidate) | sigctl-pattern controller + class-aware virtual detectors | Inherited free by staying external | Same |

The pattern worth naming: every micro competitor eventually grew **both**
layers (Vissim+Visum, Aimsun's suite, SUMO's person sim) and paid for it
in architecture. Our ADR-0008/ADR-0012 discipline — transit supply as
external controllers and scenario parts, mode-share as authored demand
variants — is the way to get both layers' benefits without merging them
into the kernel.
