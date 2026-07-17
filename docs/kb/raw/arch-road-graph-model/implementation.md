# Mechanics: Road Graph Model

> Source: web research (greenfield — no engine code exists; this file collects the
> *mechanisms* a lane-level road graph is built from, to be re-audited against real
> code once the network-model ADR is Accepted and the graph exists) | Researched:
> 2026-07-16 | Git HEAD: ae75fba

## 1. The directed-graph substrate: junctions, unidirectional edges, lane bundles

Every surveyed simulator shares the same skeleton: a **directed graph whose nodes
are intersections ("junctions") and whose arcs are unidirectional road segments
("edges"/"links"), each edge carrying an ordered bundle of lanes**.

- SUMO: "a SUMO network is a directed graph. Nodes ... 'junctions' ... represent
  intersections, and 'edges' roads or streets. Note that edges are
  unidirectional." Each edge holds lanes with id `<edgeID>_<i>`, index 0 at the
  **right-most** lane, each lane with its own speed limit, length and polyline
  shape ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).
- MATSim's network is nodes (`id, x, y`) + links (`from, to, length, freespeed,
  capacity, permlanes, modes`) — the coarsest surveyed: lanes are a scalar count,
  not objects, because QSim is a queue model
  ([MATSim book](https://www.matsim.org/files/book/partOne-latest.pdf),
  [OsmNetworkReader doxygen](https://www.matsim.org/doxygen/classorg_1_1matsim_1_1core_1_1utils_1_1io_1_1_osm_network_reader.html)).
- Vissim builds the graph bottom-up: unidirectional multi-lane **links** joined by
  **connectors** "to make a continuous flow between the links and to define turn
  relations in junctions"; nodes are optional abstractions drawn *around* connector
  groups for dynamic assignment
  ([Chalmers PDF](https://publications.lib.chalmers.se/records/fulltext/250879/250879.pdf),
  [de Jong thesis](https://www.victorknoop.eu/research/theses/MScThesis_EM_deJong.pdf)).
- Aimsun inverts Vissim's granularity: roads are **sections** meeting at **nodes**;
  movements through a node are **turnings**. Asymmetry on import: "Vissim Links
  can have turns at any part of the link", Aimsun turns live only at nodes
  ([Aimsun Vissim importer](https://docs.aimsun.com/next/24.0.3/UsersManual/VissimImporter.html)).
- MITSIM already had the same skeleton in the 1990s ("nodes, links, segments, and
  lanes" + lane connections + turning regulations,
  [MIT thesis PDF](http://dspace.mit.edu/bitstream/handle/1721.1/10360/37511992-MIT.pdf?sequence=2)) —
  the concepts are stable; only formats change.
- The **adjacency question is settled by revealed preference**: nobody stores
  adjacency at the edge level alone for simulation. Lane-level connectivity is
  either stored explicitly (SUMO connections, OpenDRIVE lane links, A/B Street
  turns) or derived per road user (Lanelet2 routing graph); edge-level adjacency
  is a routing shortcut (§2).

**Mechanism takeaway for us:** the *lane* is the navigable atom; the edge is a
container for shared attributes (priority class, name, permissions) and rendering.

## 2. Lane-to-lane connections: the turn fabric

Connections ("links", "turns", "movements") say which outgoing lane is reachable
from which incoming lane, and carry the movement's semantics.

- **SUMO `<connection>`**: `from`/`to` edges + `fromLane`/`toLane` indices, a `via`
  internal lane id, `dir` (s/t/l/r/L/R), a `state` encoding right-of-way class
  (`M` major, `m` minor, `=` equal, `-` dead end, plus TLS states `r/g/G/y/Y/o/O`),
  and optional `tl` + `linkIndex` binding it to a traffic-light signal. Behavior
  knobs in plain XML: `visibility` (default 4.5 m; 100 m for zipper), `speed`,
  `pass`, `keepClear`, `contPos` (internal waiting point), custom spline `shape`
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- **A/B Street turn** = `(source lane, destination lane, intersection)` with a type
  (straight/right/left/u-turn) — the intersection id is in the key because
  pedestrian movements need it ([osm2streets issue #67](https://github.com/a-b-street/osm2streets/issues/67)).
  Conflict between turns is computed **geometrically**: "a turn has a line-string
  of the movement through the intersection, and if those intersect, then they
  conflict" ([same](https://github.com/a-b-street/osm2streets/issues/67)).
- **Higher-level bundling**: A/B Street found lane-level turns too fine for signal
  work and grouped them into **movements on a "directed road segment"** (all lanes
  pointing the same way) — "it was better to think about the entire directed road
  segment" ([issue #67](https://github.com/a-b-street/osm2streets/issues/67)).
- **OpenDRIVE**: junctions contain **connecting-roads** linking incoming roads to
  outgoing roads, with per-lane `laneLink` (predecessor/successor lane ids);
  "connecting roads are the only roads within ASAM OpenDRIVE with overlapping
  surfaces" ([ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).
- **Lanelet2**: lanelets chain via shared boundary endpoints ("successive lanelets
  share the end points of the left and right border"); neighbors share a boundary,
  and *the border type expresses whether a lane change is possible*; the routing
  module then derives per-participant routing graphs including **conflicting
  lanelets** ([Poggenhans 2018, Lanelet2 paper PDF](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
- **Completeness invariant**: "every lane should lead somewhere, and should be
  reachable from somewhere" — A/B Street would rather keep a questionable turn
  than orphan a lane ([issue #67](https://github.com/a-b-street/osm2streets/issues/67));
  SUMO deliberately emits "double connections" (two incoming lanes → one outgoing)
  where lanes die ([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).

## 3. Inside the junction: internal lanes and waiting positions

The defining design split: does the interior of an intersection exist in the graph?

- **SUMO internal edges/lanes**: every lane-to-lane connection gets a physical
  internal lane (edge `function="internal"`, id prefixed `:`) with real geometry,
  and vehicles drive on them "just as on normal lanes, albeit subject to some
  blocking constraints". Built with `--no-internal-links`, vehicles instead "jump"
  the junction — "they cannot block the intersection, wait within the intersection
  for left turns nor collide on the intersection"
  ([Simulation/Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- **Internal junctions** (SUMO): a second stopping tier *inside* the box — the
  left-turner waiting for oncoming traffic, the right-turner waiting for the
  crosswalk. The internal lane is split in two; drivers may enter up to the split
  despite foe traffic, but not if the box is blocked or the light red. netconvert
  auto-creates them for streams with a *green minor* phase; position customizable
  via `contPos` ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- **Junction blocking ("keep-clear")**: SUMO's *no-block heuristic* stops vehicles
  entering a junction they can't clear; disablable per node or per connection
  (`keepClear="false"`), or after patience runs out (`jmIgnoreKeepClearTime`);
  deadlocks resolve by teleporting after a timeout
  ([Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- **Turn-speed physics**: SUMO limits internal-lane speed by turning radius,
  `speedLimit = sqrt(radius × factor)` (factor default 5.5), capped at the mean of
  source and destination lane speeds
  ([Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- **Vissim's opposite pole**: no junction objects at all in the physical model —
  connectors continue through the box; conflicts are *areas* painted where
  links/connectors overlap ([de Jong thesis](https://www.victorknoop.eu/research/theses/MScThesis_EM_deJong.pdf),
  [SJSU PDF](https://transweb.sjsu.edu/sites/default/files/1712-Pande-Assessing-Complete-Street-Strategies.pdf)).
- **A/B Street middle ground**: turns own their geometry (Bézier-ish line-strings
  through the box); the intersection is the arbiter that wakes waiting agents on
  signal change ([discrete-event article](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html)).

## 4. Right-of-way: compiled matrices vs evaluated rules

- **SUMO's compiled right-of-way matrix**: per junction, every connection (link)
  gets a `<request>` with two bitsets — `response` (streams that force this link's
  vehicles to stop) and `foes` (all conflicting streams, a superset of response) —
  plus `cont` ("may pass the first stop line to wait within the intersection").
  Bit order: connections sorted clockwise from north, right-most lanes first; read
  **right to left**. Max **256 links per junction** since 0.25.0 (64 before).
  Runtime keys off link state: major links cross without slowdown; minor links
  brake to `visibilityDistance`, then decide
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- **The matrix is compiled from a junction *type***: SUMO's node types are
  `priority`, `priority_stop`, `allway_stop`, `traffic_light`,
  `traffic_light_right_on_red`, `traffic_light_unregulated`, `right_before_left`,
  `left_before_right`, `zipper`, `unregulated`, `rail_signal`, `rail_crossing`,
  `dead_end` ([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
  The compiler: `rightOfWay="default"` sorts incoming edges by
  (priority, speed, lane count) and grants right-of-way to the top two opposing
  edges; `edgePriority` trusts the edge attribute alone; special cases — turning
  priority roads via uniformly raised priorities, "edges within the roundabout
  always get the right of way over edges incoming from the outside", and two lanes
  converging on one target → left wins unless the node is `zipper` (symmetric
  late-merge) ([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- **Vissim's two mechanisms**: *conflict areas* — overlapping link/connector
  regions, each typed `passive` / `undetermined` / `1 waits for 2` / `2 waits for 1`
  ([ETH gating study PDF](https://ethz.ch/content/dam/ethz/special-interest/baug/ivt/ivt-dam/publications/students/601-700/sa680.pdf));
  and *priority rules* — a red stop-line bar plus green conflict-marker bars with
  **min gap time** and **min clearance** checked when a vehicle reaches the stop
  line
  ([PTV Vissim help](https://cgi.ptvgroup.com/vision-help/VISSIM_2023_ENG/Content/5_Netzbearbeiten/Querverkehrsstoerungen_Aufbau.htm)).
- **Aimsun**: turnings carry a control action — `None`, `Yield`, `Stop`, `RTOR`
  (right turn on red) — plus turn speed and a node-level "yellow box" no-stopping
  flag ([Node editing](https://docs.aimsun.com/next/22.0.4/UsersManual/NodeEditing.html)).
- **Lanelet2 regulatory elements**: rules are first-class objects referencing the
  physical source (traffic light, yield sign, stop line) and the lanelets they
  govern; they can be *dynamic* (time-/condition-dependent) and are interpreted
  per country and participant class
  ([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
- Gap acceptance *behavior* on minor links is a car-following-topic concern —
  see [[domain-traffic-flow-models]].

## 5. How vehicles occupy lanes: continuous s, queues, cells, sublanes

- **Continuous longitudinal position, discrete lateral lane** (SUMO default):
  vehicles hold a metric position along a lane; "by default, there is at most one
  vehicle per lane and lane-changes are performed instantly". Two upgrades exist:
  `--lanechange.duration` (constant lateral speed, duration-aware decisions) and
  the full **sublane model** (`--lateral-resolution` divides lanes into stripes,
  continuous lateral motion, shadow-lane bookkeeping) — for motorcycles/partial
  overlaps, at "higher running time"
  ([SublaneModel](https://sumo.dlr.de/docs/Simulation/SublaneModel.html)).
- **Queue + lazy position** (A/B Street): each lane holds a queue; agents schedule
  "best-case" crossing events; exact front-bumper positions are computed lazily by
  walking the queue front-to-back with leader-imposed bounds; **laggy heads** track
  a departed vehicle's rear still occupying the lane; **ghosts** reserve space in
  the old lane during a lane change
  ([discrete-event article](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html)).
- **Storage-capacity queue** (MATSim QSim): a link is a FIFO with flow capacity
  and a space constraint derived from length × permlanes and an effective cell size
  (7.5 m default for cars) — no in-link dynamics at all
  ([MATSim book](https://www.matsim.org/files/book/partOne-latest.pdf),
  [evacuation manual PDF](https://data.bris.ac.uk/datasets/333uc5aebpzfz25mhmd83yt3yk/Manual_Agent_Based%20MATSim.pdf)).
- **Cells** (CTM/LTM) partition lanes into fixed cells — macroscopic, covered by
  [[domain-macroscopic-flow-models]]; **continuous 2D** (Vissim) lets vehicles move
  freely over link surfaces — the high-fidelity pole, and the reason Vissim needs
  painted conflict areas rather than lane logic
  ([Chalmers PDF](https://publications.lib.chalmers.se/records/fulltext/250879/250879.pdf)).

VISION.md fixes our point on this spectrum: lane changes and multi-lane roads, no
within-lane swerving ⇒ continuous `s`, discrete lane index, lane change as a
maneuver (SUMO's simple continuous model is the matching prior art).

## 6. Geometry vs topology separation

- **Single owner of position** (Lanelet2): "points are the only primitives that
  actually have position information"; linestrings order points; lanelets reference
  two boundary linestrings ([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
  Edit a point and every dependent element moves — consistency by construction.
- **Per-lane polylines** (SUMO): each lane stores its center-line `shape`
  (≥2 positions, 2D or 3D); all lanes of an edge share one length even when shapes
  differ; `spreadType` in plain XML says how edge geometry fans out into lanes
  (`right` default, `center`, `roadCenter`)
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- **Parametric reference line** (OpenDRIVE): every road has exactly one reference
  line; lanes/elevation/objects attach via the `s/t` coordinate system; lane widths
  are polynomials in `s` — a capability SUMO's constant-per-edge width lacks
  ("you have to split the edge to change widths",
  [sumo-user msg14614](https://www.eclipse.org/lists/sumo-user/msg14614.html);
  [ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).
- **Thickened line-strings** (osm2streets): a road is a center line thickened into
  a polygon plus a left-to-right lane list (type, direction, width); intersections
  are polygons met by roads at a perpendicular angle
  ([osm2streets README](https://github.com/a-b-street/osm2streets)).
- The **s-coordinate contract**: OpenDRIVE (explicit `s`), CARLA's waypoint API
  (`road_id, section_id, lane_id, s`, hashed into a waypoint id with 2 cm dedup)
  and SUMO all address vehicles longitudinally on a lane
  ([CARLA core map](https://carla.readthedocs.io/en/latest/core_map/)).

## 7. Conflict-point detection: geometry is computed once, then baked

- SUMO precomputes response/foes matrices from internal-lane geometry at network
  build; researchers extending SUMO confirm the static-compute trick: "claims and
  reservations are placed on internal lanes ... two maneuvers conflict if their
  sets intersect ... the information is static and can be computed as such"
  ([Paderborn intersection-management thesis PDF](https://digital.ub.uni-paderborn.de/hs/content/titleinfo/3512356/full.pdf)).
- A/B Street detects turn conflicts by intersecting turn line-strings — coarse but
  adequate for signals and sim ([issue #67](https://github.com/a-b-street/osm2streets/issues/67)).
- Vissim auto-detects overlapping links/connectors as conflict areas
  ([SJSU PDF](https://transweb.sjsu.edu/sites/default/files/1712-Pande-Assessing-Complete-Street-Strategies.pdf));
  Lanelet2's routing module lists conflicting lanelets per participant
  ([paper, Fig. 3](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
- Common shape: **conflict sets are a build-time product of geometry**, stored
  against the connection/turn, not re-derived at runtime.

## 8. IDs, provenance and coordinate systems

- SUMO networks are metric cartesian, north-up, shifted to origin; the `location`
  element records `netOffset`, `convBoundary`, `origBoundary`, `projParameter`
  (PROJ definition or `!`/`-` markers) — enough to back-project to WGS84
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).
- **OSM id mapping**: OSM node 1234 → junction "1234"; way 5677 → edges
  `5677#0..#n` (split at each intersection) with `-` prefix for the reverse
  direction; joined node clusters become `cluster_1_2` with internal edges
  `:cluster_1_2_INDEX`; `--output.original-names` stores per-lane `origID` params
  ([OpenStreetMap import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- Lanelet2 keeps WGS84 lat/lon losslessly in storage and projects to a local
  metric frame (UTM) at load ([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
  A/B Street's unsolved problem is provenance for *edits*: "representing edits
  durably, even when a map is rebuilt from updated OSM data"
  ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
- Cross-reference: OSM's own data model belongs to [[integration-osm-extraction]];
  this topic reserves the provenance fields the importer will need.

## 9. Import pipelines are heuristic compilers (the netconvert lessons)

netconvert is the most battle-tested OSM→sim importer and documents its own
failure modes:

- **Junction joining**: dual-carriageway crossings import as 2–4 nodes;
  `--junctions.join` clusters nodes within `--junctions.join-dist` (default 10 m),
  but "some junction clusters are too complex for the heuristic", and it sometimes
  wrongly joins (counter: `--junctions.join-exclude`). Unjoined clusters cause
  "low throughput, jams and even deadlocks", invalid two-leg left-turn
  trajectories, and long vehicles blocking each other
  ([OpenStreetMap import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
  [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- **TLS and ramp guessing**: OSM tags the signal pole node, not the intersection;
  `--tls.guess-signals` (default dist 25 m) + `--tls.join` compensate; guessed
  lights are prefixed `GS_`, and the docs carry a debugging recipe for
  intersections wrongly left uncontrolled. OSM also often lacks accel lanes;
  `--ramps.guess` fabricates them
  ([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- **The partial-marking trap**: with `--osm.turn-lanes`, "at roads where some
  lanes have turn markings and others do not, the unmarked lanes are interpreted
  as through-lanes. This may not be correct in all cases"
  ([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- **Typemap values are confessed guesses**: "the values in those type maps were
  set-up ad-hoc and are not yet verified" ([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html));
  A/B Street independently reports OSM turn-lane tagging "often flat-out wrong ...
  or broken when ways are split before an intersection"
  ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
- **Hand-editing the compiled artifact is forbidden**: ".net.xml ... is not meant
  to be edited by hand ... there are subtle inter-dependencies" — edit plain XML
  and recompile ([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
  Left-hand traffic flips everything globally (`--lefthand`); CARLA honors
  OpenDRIVE `rule="LHT"` per road
  ([CARLA core map](https://carla.readthedocs.io/en/latest/core_map/)).

## 10. File-format mechanics: authoring vs compiled vs interchange

- **Two-representation pattern (SUMO)**: plain XML (nodes/edges/types/connections,
  meant for humans, XSD-validated) ↔ `.net.xml` (compiled: internal lanes, request
  matrices, TLS logics); netconvert converts between them **without information
  loss** ([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- **XML schemas everywhere**: SUMO `.net.xml` (net_file.xsd), MATSim
  `network_v1.dtd`, OpenDRIVE `.xodr` (+ zipped `.xodrz`), Lanelet2 reuses the OSM
  XML envelope, CommonRoad 2020a XML ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/),
  [CommonRoad paper PDF](https://mediatum.ub.tum.de/doc/1379638/776321.pdf)) —
  while **protobuf is the 2020s move**: CommonRoad's 2024 format reads "XML (2020a)
  or protobuf (2024)" ([commonroad-io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html)).
- **CSV for planning networks**: GMNS is a small set of CSV tables (node, link,
  geometry, movement...), human- and machine-readable, first release January 2020
  ([TRID record](https://trid.trb.org/View/1909441),
  [spec](https://zephyr-data-specs.github.io/GMNS/)).
- **GeoJSON/JSON at the edges**: osm2streets renders lane/intersection polygons to
  GeoJSON for Leaflet/MapLibre ([README](https://github.com/a-b-street/osm2streets));
  A/B Street invented a JSON signal-config interchange keyed to OSM ids
  ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
- **Import breadth as strategy**: netconvert imports OSM, VISUM, Vissim,
  OpenDRIVE, MATSim, shapefiles, NavTeq ([Import overview](https://sumo.dlr.de/docs/Networks/Import.html))
  and *exports* OpenDRIVE — third parties wrap it for OSM→xodr for CARLA
  ([osm-to-xodr](https://github.com/RISE-Dependable-Transport-Systems/osm-to-xodr)).
