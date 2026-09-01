# Prior Art Survey: Rail Operations

> Source: web research | Researched: 2026-08-24
> "Competitors" here = rail-capable simulators whose designs we can crib from
> or be warned by, ordered by relevance to this project. SUMO is first
> because netconvert already feeds our importer — its rail network
> conventions are the ones we would inherit.

## 1. SUMO rail simulation — the crib-able prior

([SUMO Railways docs](https://sumo.dlr.de/docs/Simulation/Railways.html))

What it does:

- **Network model**: railways import from OSM with vehicle classes already
  set (`allow="rail"`; electrified track also permits `rail_electric`,
  `rail_fast`). OSM railway types distinguished by default:
  tram/subway/light_rail/rail/highspeed. **Parallel tracks must be distinct
  edges**, not multi-lane edges — the convention our lateral model would need.
  Bidirectional single track = two geometry-reversed "superposed" edges
  (`bidi` attribute), with rail signals at both ends restricting to one
  direction at a time. netconvert ships a whole repair toolbox for incomplete
  OSM rail topology (`--railway.topology.repair*`, `--railway.topology.all-bidi`).
- **Rail signals** (`rail_signal` junction type): automatic block signaling —
  guard the block ahead (one train per section, rear-end protection), guard
  flanks (conflicting branches), guard bidirectional sections (head-on), and
  anticipate 2–3-train deadlocks. **Moving-block mode** (`--railsignal-moving-block`
  or per-signal param) disables the fixed-block guard and lets the car-follow
  model keep distance (LZB-like); trams default to moving block.
- **Car-following**: `carFollowModel="Rail"` with named train types
  (Freight, ICE1/ICE3, RB425/628, REDosto7, NGT400, MireoPlus…) modeling
  traction + rolling resistance; `custom` via traction/resistance lookup
  tables (`speedTable`/`tractionTable`/`resistanceTable`) or parameterized
  curves (`maxPower`/`maxTraction`, quadratic resistance). Trains under the
  Rail CFM "always keep enough distance to the leading train to come to a
  safe stop even if the lead train were to stop instantly" — i.e. SUMO's
  primary spacing mechanism is a *safe-braking envelope*, the same idea as
  our ADR-0025 safety gate, not IDM.
- **Train length effects** (explicitly documented): block occupancy (longer
  trains clear blocks later), speed limit = minimum over **all edges the
  train occupies** (road vehicles: front edge only), reversal requires the
  whole consist past the switch. This is exactly the class of issue our
  front-bumper model under-handles (implementation.md §1.3, L1/L2).
- **Schedule constraints**: `railSignalConstraints` — predecessor /
  insertionPredecessor constraints ("train t0 may pass signal A only after
  foe t1 passed signal tl") generated from scheduled stops by
  `generateRailSignalConstraints.py`. This is *schedule-priority at the
  interlocking* as data — the feature our rowGate tie-break (ID order)
  cannot express (implementation.md §3).
- **Rail crossings** (`rail_crossing` junction type): trains always have
  right of way; road gets red until a safe gap. Parameters: `time-gap`
  (default 15 s approaching-train trigger), `space-gap`, `min-green-time`
  (5 s), `opening-delay` (3 s), `opening-time` (3 s red-yellow `u`),
  `yellow-time` (3 s) — a complete, tuned gate-as-signal-state-machine we
  could mirror with ADR-0037 verbs.
- **Deadlocks**: detection via `--time-to-teleport.railsignal-deadlock`
  (with the explicit warning that plain `--time-to-teleport` 300 s must be
  disabled for rail because "trains may sometimes have to wait much longer
  than cars" — direct support for our §2 strand-escape finding), resolution
  by teleport/removal/constraint-deactivation, prevention by loading
  recorded deadlock signal sets next run.
- **Schedules/stops**: trains are ordinary SUMO public transport vehicles
  with stop lists (duration/until), waypoints for pass-through; validation
  tooling (`checkStopOrder.py`, `scheduleStats.py` punctuality,
  `checkReversals.py`) and a GTFS+OSM large-scenario tutorial. Outputs:
  `railsignal-block-output` (driveway/foe/siding topology) and
  `railsignal-vehicle-output` (driveway occupancy enter/leave times) — the
  block-occupancy dataset our metrics kernel has no analog of.
- TraCI exposes `getBlockingVehicles`/`getRivalVehicles`/`getPriorityVehicles`
  per signal — the interlocking-controller API surface, analogous to what an
  external interlocking controller over our TSSF/`signal_set` channel would
  compute itself.

Vs this project: SUMO rail is a *system of record* for how to do block
signaling inside a lane-level microsim — and it required first-class
driveway (Fahrstraße) objects: a route from signal to signal with forward/
flank/bidi conflict sets, computed from the network. Our compiled conflict
sets (FoesCross/FoesMerge per internal lane) are the same idea at junction
granularity; the gap is precisely "driveway = chained lanes between signals
with flank sets" vs "box = one junction". Our advantages: replay-verified
record plane, external-controller architecture (their TraCI control is
in-process), and the Chicago road network already imported. Their advantage
for rail: everything in this section exists and is validated.

## 2. OSRD (Open Source Railway Designer) — the open-source pure-rail suite

([osrd.fr](https://osrd.fr/en/) ·
[github.com/OpenRailAssociation/osrd](https://github.com/OpenRailAssociation/osrd)
· [FOSDEM 2024 talk](https://archive.fosdem.org/2024/schedule/event/fosdem-2024-2052-open-source-railway-designer-osrd-why-sncf-rseau-start-an-open-source-project-/))

What it does: SNCF Réseau's open-source (LGPL) web app for railway
infrastructure design, capacity analysis, timetabling, simulation, and
short-term path request. ~40-person team, EU+French state funded. Microscopic
simulation core with explicit signaling (French + ETCS), rolling-stock
traction curves, conflicts computed over routes, space-time diagrams,
standards import (RailML-ish RailJSON infra format). This is the most
serious *open* rail operations simulator in existence.

Vs this project: nearly disjoint scope — OSRD has no road traffic, no live
controller bus, no replay-verified record; we have no blocks, routes, or
rolling-stock physics. It is the reference for "what a real rail sim's data
model looks like" (infra with routes and signals as first-class objects,
rolling-stock library, timetable as input AND output) and the cautionary
scale tale: doing rail *properly* is a 40-person project. Anything we build
is an approximation layer over a road engine, and the ADR should say so
(that's decision candidate D4/D5 framing). Crib-able: the notion that the
timetable is the scenario (their trains are spawned by timetable, validated
against it — our GTFS-derived demand pattern), and STDCM-style
conflict search as the interlocking brain (we'd approximate with
schedule-priority constraints, SUMO-style).

## 3. OpenTrack — the commercial rail-ops benchmark

([opentrack.ch](https://www.opentrack.ch/opentrack/opentrack_e/opentrack_e.html)
· [Comprail 2004 paper](https://www.opentrack.ch/opentrack/downloads/Comprail.2004.pdf)
· [TRID record](https://trid.trb.org/View/1459669))

What it does: microscopic synchronous rail simulation from ETH IVT (Nash &
Hürlimann), commercial since ~2000, now OpenTrack Railway Technology Ltd.
User-defined train/infrastructure/timetable databases; outputs train graphs,
occupancy diagrams, statistics; used for capacity analysis, timetable
stability, disruption studies. The classic architecture: double-vertex graph
(track + topology), route locking with real interlocking logic, blocking-time
theory made executable. AnyLogic's rail library occupies the adjacent
commercial niche ([UPC punctuality-model thesis comparing
both](https://upcommons.upc.edu/server/api/core/bitstreams/20c61a8b-2a98-48c2-86dc-529e24e86326/content)).

Vs this project: the capacity-analysis incumbent for exactly the questions a
Chicago L layer would answer (headway limits, junction conflicts at Tower 18,
dwell-time sensitivity). Not a competitor for the multimodal/live-controller
story; rather the bar a serious rail answer would be checked against, and the
source of the vocabulary (blocking time, route locking, approach locking)
that standards-and-patterns.md uses. Closed source; nothing to crib but
concepts and validation targets.

## 4. MATSim transit — schedule-driven PT on a road sim (the cautionary analog)

([MATSim book, ch. 7–8](https://www.bookfusion.com/books/145400-the-multi-agent-transport-simulation-matsim)
· [Rieser 2010 via ETH ab1143](https://www.research-collection.ethz.ch/bitstream/handle/20.500.11850/114761/ab1143.pdf)
· [autonomous-transit example (dwell/awaitDeparture note)](https://ethz.ch/content/dam/ethz/special-interest/baug/ivt/ivt-dam/publications/students/601-700/sa601.pdf))

What it does: full public-transport implementation on the *road* network
model: supply = stop facilities + transit lines + routes (stop sequence with
arrival/departure offsets + link path + departure times per service);
vehicles run the schedule on the same links as cars (queue model, not
car-following); dwell = schedule offset with `awaitDeparture` semantics;
minibus contrib for demand-responsive. Rail is typically modeled as
schedule-on-dedicated-links with capacity abstracted — *not* block
signaling.

Vs this project: MATSim is the proof that "transit as scheduled vehicles on
the traffic graph" is the 80% solution for multimodal questions, and its
data model (line → route → stop offsets + departures) is exactly a GTFS
trip. It validates decision candidate D7 (stations/dwells as an authored,
GTFS-derived layer over the lane graph). It also marks the boundary: MATSim
deliberately does not model rail safety systems; for "will the Red Line
clear the Loop by 9am" that's fine, for "what limits Loop capacity" it is
not — which is where our D5/D6 (signal-plane interlockings, block
approximation) sit.

## 5. Others worth one line each

- **RailSys** (RMCon/Introsys, commercial, German) — timetable + infrastructure
  management suite with microscopic simulation; the Deutsche Bahn-grade
  incumbent alongside OpenTrack. ([railsys.de](https://www.rmcon.de/railsys-en/))
- **AnyLogic Rail Library** — commercial agent sim with rail yard/terminal
  libraries; used for punctuality modeling where OpenTrack is too rail-native
  ([UPC thesis](https://upcommons.upc.edu/server/api/core/bitstreams/20c61a8b-2a98-48c2-86dc-529e24e86326/content)).
- **NEMO / derived academic rail sims** — various university microscopic rail
  simulators; none with the tooling gravity of OSRD/OpenTrack. Mark as
  surveyed-but-thin (open question whether any is worth a deeper look).
- **Trainz/DTG/sim games** — physics fidelity without operations modeling;
  irrelevant here except as rendering prior art.

## Takeaways for this project

1. SUMO's rail stack shows the two non-negotiables are **driveways with flank
   sets** and **schedule-priority constraints at signals** — both are
   *controller-plane* features we can approximate externally (signal_set +
   a constraint table) before any kernel change (synthesis D5).
2. SUMO's own deadlock section concedes the 300 s teleport default is wrong
   for rail — independent confirmation of the strand-escape hazard
   (implementation.md §2).
3. SUMO's Rail CFM (safe-stop envelope) ≈ our ADR-0025 gate with rail decel —
   the kernel already has the primary spacing mechanism; what's missing is
   block discipline, which belongs to the controller (synthesis D4).
4. OSRD sets the honesty bar: full rail fidelity is a dedicated product. Our
   layer should target *multimodal interaction* (grade crossings, station
   area traffic, what-if on L headways vs street congestion), not capacity
   certification.
5. MATSim proves the GTFS-as-scenario pattern at scale; their dwell model is
   all most multimodal questions need.
