# Competitors: Bus Operations (buses in mixed traffic and dedicated lanes)

> Source: web research + codebase analysis | Researched: 2026-08-24 | Git HEAD: 2bc98de

## Competitive Landscape

Bus operation in a microscopic simulator splits into four capabilities:
(1) a bus vehicle class that shares lanes with cars, (2) stop infrastructure
with dwell behavior, (3) dedicated-lane enforcement (class-restricted lanes),
(4) transit signal priority. Every mainstream microscopic simulator ships all
four as *kernel-side* features; transit-specific meso/macro tools add
schedule and person modeling. This project's niche — a live external
controller bus with recorded, bit-exact-replayable intents — means (2) and
(4) would be implemented as *client-side controllers* over the wire, a
division of labor none of the incumbents have. The honest trade: incumbents
give you buses in an afternoon; this project makes bus *control policy* a
first-class, replayable experiment.

---

## SUMO (Eclipse, open source, EPL-2.0)

The reference implementation for open microscopic bus operations.

- **Bus stops as lane areas.** `<busStop id lane startPos endPos [lines]
  [personCapacity] [parkingLength]/>` — a stop is an *area on a lane*, not a
  point, with `friendlyPos` correction, optional `access` child elements for
  pedestrian access from other lanes, and an `emptyColor` "virtual stop"
  variant for on-demand service.
  Source: [SUMO Public Transport docs](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)
- **Stop behavior on the vehicle/route, not the stop.** Vehicles carry
  `<stop busStop=... duration=... until=.../>` elements; `duration` is a
  minimum halt, `until` is a scheduled earliest departure ("Vehicles … cannot
  leave a stop before the until-time … but they may still be delayed due to
  traffic"). Further schedule attributes: `arrival` (computes
  `arrivalDelay`), `extension` (max boarding overrun), `line`, `tripId`,
  `started`/`ended` for real-data replay.
  Source: [SUMO Public Transport — further schedule attributes](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)
- **Lines, flows, looped routes.** Schedules come from per-vehicle stops,
  flows (first vehicle's `until` times shift by `period × vehicleIndex`), or
  routes with `repeat`/`cycleTime` that keep one vehicle looping so "delays
  from one loop [carry] over into the next iteration" — SUMO's built-in
  bunching-perpetuation mechanism. `gtfs2pt.py` imports GTFS; `ptlines2flows.py`
  synthesizes schedules from stop sequences by running a background
  simulation.
  Source: [SUMO Public Transport — schedules](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)
- **Class-restricted lanes.** SUMO's `vClass` system (`passenger`, `bus`,
  `coach`, `tram`, `rail_urban`, `rail`, `taxi`, `hov`, `custom1/2`, …) is
  *the* lane-permission mechanism: lanes carry `allow`/`disallow` class
  lists, vTypes bind a class, and routing + lane changing respect it
  ("a road with three lanes, where the rightmost may only be used by taxis
  or busses").
  Source: [SUMO Definition of Vehicles, Vehicle Types, and Routes](https://sumo.dlr.de/docs/Definition_of_Vehicles,_Vehicle_Types,_and_Routes.html)
- **TSP.** No built-in TSP controller; done via TraCI against actuated
  signal programs or custom controllers in the literature (e.g. the FHWA TSP
  research tooling lineage uses VISSIM; SUMO studies implement priority in
  TraCI).

**vs this project:**
- SUMO's stops, schedules, and line identity are **kernel content**; ours
  would be **controller policy over recorded intents** (implementation.md
  §2). SUMO's model is richer out of the box (berth areas, boarding
  `extension`, stop output attributes); ours makes dwell *control* (holding
  strategies) the experiment rather than an engine feature — and replayable
  without re-running the controller, which SUMO cannot do with a TraCI
  client in the loop.
- Class masks: SUMO **enforces** `allow`/`disallow` in routing and lane
  changing; our netimport parses the same SUMO `allow`/`disallow` attributes
  (`engine/netimport/netimport.go:115-124`) and **discards** them after a
  keep/drop filter (`netimport.go:597-624`) — bus lanes currently compile as
  general-purpose lanes. The SUMO vClass vocabulary is the obvious template
  for our mask (implementation.md §3).
- SUMO's persons/intermodal model (personCapacity, access lanes, GTFS import)
  is far ahead; we have no person concept (metrics are vehicle-first,
  `engine/metrics.go:135-154`).

## MATSim (open source, Java, agent-based)

Transit as a *schedule-driven, person-serving* layer over a queue model.

- **TransitSchedule is first-class data:** stop facilities → transit lines →
  transit routes, each route holding "the sequence of stop facilities with
  the expected arrival and departure offsets, the sequence of links … and
  the departure times of all the services". Per-stop builder API:
  `arrivalOffset`, `departureOffset`, `allowBoarding`, `allowAlighting`,
  `awaitDepartureTime`.
  Sources: [Using smartcard data for agent-based transport simulation (ETH)](https://www.research-collection.ethz.ch/bitstream/handle/20.500.11850/114761/ab1143.pdf), [MATSim TransitRouteStopImpl.Builder doxygen](https://www.matsim.org/doxygen/classorg_1_1matsim_1_1pt_1_1transit_schedule_1_1_transit_route_stop_impl_1_1_builder.html)
- **Vehicles block traffic.** In the mobsim queue model, PT vehicles run on
  network links and queue like cars (link free-flow time + queue discharge),
  so bus–car interaction exists but at queue-model, not car-following,
  fidelity: no lane changing, no mixed-length gap dynamics, no junction
  conflict model.
- **Persons are the point:** agents ride buses as part of daily plans, with
  boarding/alighting events, transfers, and mode *choice* across replanning
  iterations — person-delay and mode share are native outputs.
  Source: [MATSim transit description (ETH collection)](https://www.research-collection.ethz.ch/bitstream/handle/20.500.11850/114761/ab1143.pdf)
- Dwell: from the schedule offsets (departure − arrival), optionally with
  `awaitDeparture` so vehicles hold until the scheduled departure; passenger
  interaction-time settings can add per-boarding/alighting seconds.

**vs this project:** Complementary more than competing. MATSim answers
"who rides, and is the schedule worth it" at city scale with person agents;
we answer "what does this bus do to this intersection, and does this control
law work" at lane-level physics. Our dwell would emerge from a controller +
recorded intents with car-following-fidelity blocking; MATSim's is a
schedule offset in a queue model. MATSim has no signal-priority control
loop over live state comparable to ADR-0037; we have no persons. The
person-delay tie-in (implementation.md §5) is where we'd borrow MATSim's
framing without its iteration machinery.

## Aimsun Next (commercial, TSS)

- **Transit plans** = lines (routes, reserved lanes, bus stops) + timetables
  with time slices; vehicles of configurable type per departure; stops have
  three modeled types covering in-lane and bay/pullout variants; boarding
  cost functions per line; dwell influenced by modeled passenger activity.
  Sources: [Aimsun Next — Microscopic Simulator Transit Modeling](https://docs.aimsun.com/next/23.0.0/UsersManual/MicrosimulationPublicTransportModelling.html), [Aimsun Next — Transit Stop editor](https://docs.aimsun.com/next/23.0.0/UsersManual/PTStopEditing.html)
- Static and dynamic PT *assignment* for demand; C++/Python API
  (`AKIPTGetNumberLines`, transit-vehicle info with
  `reactionTimeAtStop`, `maxCapacity`, lane-changing behavior parameters).
  Source: [Aimsun Next API — Managing Transit](https://docs.aimsun.com/next/24.0.2/UsersManual/ApiManagePublicTransport.html)

**vs this project:** The full turnkey bus model — stop geometry types,
reserved lanes, timetable fleets, person costs — behind a commercial
license, with control extension via a proprietary API. We match the physics
(lane-level micro with junction conflicts, which Aimsun also has) but not
the transit content; what Aimsun cannot give is a recorded, bit-exact,
externally-reviewable control experiment (our ADR-0005/ADR-0013 pipeline) or
an open codebase.

## PTV Vissim / Viswalk (commercial, PTV)

- PT lines with timetables and routes; stops with **dwell-time
  distributions** (empirical, e.g. normal/erlang by boarding counts from APC
  data) or, with Viswalk, explicit pedestrian boarding/alighting driving the
  dwell endogenously. KPI outputs include lateness and travel time.
  Sources: [PTV Vissim FAQs](https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs), [Modelling pedestrians at PT stops in Vissim (modelling.group)](https://www.modelling.group/blog/vissim-public-transport-stops-with-pedestrians), [VISSIM dwell-time configuration study (U. Alberta thesis)](https://era.library.ualberta.ca/items/ff67263b-a255-43a1-8ee2-1b2e0b0ff798/view/dafff801-75c5-44c0-8e39-672315acd36a/Sikder_Rajib_201501_MSc.pdf)
- TSP via VAP (vehicle-actuated programming) — the FHWA research tooling for
  TSP logic evaluation was built against VISSIM+VAP (green extension, red
  truncation).
  Source: [Transit Signal Priority Research Tools (FHWA, Li et al. 2008)](https://rosap.ntl.bts.gov/view/dot/16521/dot_16521_DS1.pdf)

**vs this project:** Vissim's dwell-from-passengers (Viswalk) is the
high-fidelity pole of stop modeling; our equivalent would be a boarding-rate
function in the bus controller (standards-and-patterns.md §2) — cheaper,
coarser, but replay-pinned. Vissim's VAP TSP is a script inside a commercial
signal emulator; our TSP is a NATS client against `signal_set` with the
starvation rails and recorded verbs (implementation.md §4). Wiedemann
car-following vs our IDM is a wash; class-lane enforcement exists in both
worlds (theirs native, ours to build).

## Open bus-bunching simulation lineage (academic)

The bunching literature runs on **bespoke minimal simulators**, not the
packages above:

- **Bus Route Model** (O'Loan, Evans, Cates 1998): a driven-diffusive
  cellular-automaton ring — buses hop between stops, dwell ∝ waiting
  passengers — showing a jamming (bunching) phase transition; the canonical
  physics model.
  Source: [Phys. Rev. E 58, 1404](https://link.aps.org/doi/10.1103/PhysRevE.58.1404), [arXiv:cond-mat/9712243](https://arxiv.org/abs/cond-mat/9712243)
- **Nagatani's car-following bus-route models** (2000–2001): buses with
  time-headway car-following between stops; kinetic clustering and bunching
  transitions. Source: [Nagatani 2000, Physica A 287:302 (cited in arXiv:1711.05884)](https://arxiv.org/pdf/1711.05884)
- **Gershenson & Pineda's headway-instability simulator** (2009, PLoS ONE,
  open access): homogeneous multi-agent model proving equal headways are
  unstable *without* any asymmetry, plus self-organizing hold rules that
  outperform schedule-following.
  Source: [PLoS ONE 4(10): e7292](https://journals.plos.org/plosone/article?id=10.1371/journal.pone.0007292)
- **Control-law evaluation sims:** Daganzo's headway-holding evaluations and
  the DRL wave (e.g. Wang & Sun's multi-agent DRL holding, TR-C 116, 2020)
  are all custom discrete-time line simulators.
  Source: [Wang & Sun 2020](https://www.sciencedirect.com/science/article/abs/pii/S0968090X20305763)

**vs this project:** These models prove theory but cannot touch geometry,
signals, or mixed traffic — the things that decide whether a holding
strategy survives a Chicago arterial. Our pitch inverts it: the bunching
mechanism (dwell ∝ accumulated boardings, headway feedback) emerges from
real stops on a real network, and the holding controller is a swappable
NATS client measured by the ADR-0014 metric kernel with paired seeds —
the evaluation rigor the DRL papers build bespoke harnesses for.

## Positioning Summary

| Capability | SUMO | MATSim | Aimsun | Vissim | traffic-sim (today) |
|---|---|---|---|---|---|
| Bus vehicle class in mixed micro traffic | ✔ vType+vClass | queue-model | ✔ | ✔ | ✔ trivial (Truck precedent, `engine/vehicle.go:40`) |
| Stop infrastructure | ✔ kernel (areas, lines, access) | ✔ schedule facilities | ✔ 3 types | ✔ + Viswalk peds | ✘ (would be controller+geometry, impl §2) |
| Dwell model | duration/until/extension | schedule offsets | pax activity + cost fns | distributions / pedestrians | ✘ (cruise-0 intent; boarding fn = client policy) |
| Class-restricted lanes | ✔ enforced in routing+LC | n/a | ✔ reserved lanes | ✔ | ✘ parsed, discarded at import (impl §3) |
| TSP | via TraCI | ✘ | via API | ✔ VAP | ✔ channel ready (`signal_set`, ADR-0037); controller to write |
| Schedule/headway control | `until` adherence | schedule + awaitDeparture | timetables | timetables | ✘→ client policy (holding-control reference impl candidate) |
| Persons / person-delay | ✔ intermodal | ✔✔ (the point) | ✔ | ✔ (Viswalk) | ✘ (occupancy attribute candidate, impl §5) |
| Live external control + verified replay | TraCI, not recorded | ✘ | API, not recorded | COM, not recorded | ✔✔ (the differentiator) |
| License | EPL-2.0 | GPL-ish/Java | commercial | commercial | (project license; OSS) |
