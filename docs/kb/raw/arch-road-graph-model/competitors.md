# Prior Art Survey: Road Graph Model

> Source: web research | Researched: 2026-07-16
> "Competitors" here = systems whose road-network representation we can steal from
> or be warned by: traffic simulators (micro and meso), HD-map frameworks from
> autonomous driving, OSM-derived street models, and interchange standards.

## Traffic simulators

### SUMO (.net.xml + netconvert) — the reference lane-level graph
- Directed graph; unidirectional edges bundle lanes indexed from the **right**
  (`<edgeID>_0`); every lane has speed, length, center-line polyline
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).
- Junctions are typed (`priority`, `priority_stop`, `allway_stop`,
  `traffic_light`, `traffic_light_right_on_red`, `right_before_left`, `zipper`,
  `unregulated`, ...) and carry a **compiled right-of-way matrix**: per-connection
  `request` elements with `response`/`foes` bitsets and a `cont` flag for
  in-junction waiting ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- Intersections are physical: **internal edges/lanes** with geometry, plus
  **internal junctions** for stop-here-then-yield maneuvers; can be flattened with
  `--no-internal-links` (vehicles "jump" the box; route lengths are patched to
  junction-center distance to avoid systematic shortening)
  ([Simulation/Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- Vehicle occupancy: continuous position on lane, ≤1 vehicle per lane laterally,
  instant lane change by default; optional `--lanechange.duration` or the sublane
  model ([SublaneModel](https://sumo.dlr.de/docs/Simulation/SublaneModel.html)).
- Junctions capped at **256 links** (64 pre-0.25.0); metric north-up coordinates
  with recorded offset/projection for back-conversion
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).
- netconvert imports OSM, VISUM, Vissim, OpenDRIVE, MATSim, shapefiles, NavTeq —
  and documents its OSM heuristics' failure modes (junction joining, TLS guessing,
  ramp guessing, ad-hoc typemaps) extensively
  ([Import](https://sumo.dlr.de/docs/Networks/Import.html),
  [OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- **vs traffic-sim (us):** the shape to beat: lane-atom graph + typed junctions +
  compiled conflict matrices + internal lanes maps 1:1 onto VISION's requirements
  (4-way stop, signal, roundabout, yield, ramps all expressible). What not to
  copy: XML verbosity, hand-edit-forbidden compiled artifacts, and heuristic
  imports that silently produce jams.

### A/B Street + osm2streets — OSM-native lane model, hard-won pitfall list
- osm2streets schema: roads = thickened line-strings + left-to-right lane list
  (type, direction, width); intersections = polygons; transformations collapse
  degenerate intersections, merge "sausage links"/dual carriageways and dog-leg
  clusters, snap parallel cycletracks to the main road
  ([osm2streets README](https://github.com/a-b-street/osm2streets)).
- A/B Street turn = `(source lane, destination lane, intersection)`; conflicts
  detected by intersecting turn line-strings; signal logic later re-bundled into
  **movements over directed road segments** because lane granularity "got really
  hard for traffic signals" ([issue #67](https://github.com/a-b-street/osm2streets/issues/67)).
- Sim-side occupancy: per-lane queues with lazily evaluated front-bumper
  positions, laggy heads, and ghosts — continuous-looking behavior on a
  discrete-event engine ([discrete-event article](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html)).
- Documented pitfalls: OSM turn-lane tags "often flat-out wrong" or broken by way
  splits; turn-restriction relations span multiple ways; mandatory lane-changing
  failed where short-road clusters made maneuvers impossible ("many vehicles
  repeatedly attempted lane-changing maneuvers ... impossible to actually pull
  off"); gridlock traced partly to "broken intersection geometry causing
  impossible turn conflicts"; durable user edits across OSM re-imports unsolved
  ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
- Also imported GMNS signal timing — proof CSV interchange formats reach into sim
  internals ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
- **vs us:** the closest cousin in spirit (open source, OSM-first, advocacy use
  case) and the best cautionary tale: network-model bugs surface as *simulation
  gridlock*, and lane-level turn generation from OSM is where the bodies are
  buried. Its queue engine is not our engine (fixed tick, IDM), but its turn
  schema and failure catalog transfer directly.

### MATSim — queue-model minimalism
- Network = nodes + links with `length, freespeed, capacity, permlanes, modes`;
  lanes are a count, not objects; no junction interior, no turn semantics beyond
  allowed next-links ([MATSim book](https://www.matsim.org/files/book/partOne-latest.pdf),
  [OsmNetworkReader doxygen](https://www.matsim.org/doxygen/classorg_1_1matsim_1_1core_1_1utils_1_1io_1_1_osm_network_reader.html)).
- Occupancy = FIFO storage queue: flow capacity + storage from
  length × permlanes / 7.5 m cell ([evacuation manual PDF](https://data.bris.ac.uk/datasets/333uc5aebpzfz25mhmd83yt3yk/Manual_Agent_Based%20MATSim.pdf)).
- pt2matsim parses OSM turn restrictions into `disallowedNextLinks`
  ([pt2matsim config](https://github.com/matsim-org/pt2matsim/blob/master/src/main/java/org/matsim/pt2matsim/config/OsmConverterConfigGroup.java)).
- **vs us:** deliberately too coarse — no lane changes, no intersection
  right-of-way. Useful only as proof of how far a sim scales when the graph is
  minimal, and as an import/export peer (netconvert reads MATSim networks).

### PTV Vissim — links/connectors, painted conflict areas
- Graph built from unidirectional multi-lane **links** and **connectors** ("links
  are blue and connectors violet"); turns can begin at *any* point of a link —
  finer than node-centered models ([Chalmers PDF](https://publications.lib.chalmers.se/records/fulltext/250879/250879.pdf),
  [Aimsun Vissim importer](https://docs.aimsun.com/next/24.0.3/UsersManual/VissimImporter.html)).
- Right-of-way via **conflict areas** (overlap regions typed
  passive/undetermined/1-waits-2/2-waits-1) and **priority rules** (stop line +
  conflict markers, min gap time, min clearance); nodes exist only for assignment
  abstraction ([ETH PDF](https://ethz.ch/content/dam/ethz/special-interest/baug/ivt/ivt-dam/publications/students/601-700/sa680.pdf),
  [PTV help](https://cgi.ptvgroup.com/vision-help/VISSIM_2023_ENG/Content/5_Netzbearbeiten/Querverkehrsstoerungen_Aufbau.htm),
  [de Jong thesis](https://www.victorknoop.eu/research/theses/MScThesis_EM_deJong.pdf)).
- Vehicles move continuously in 2D over link surfaces — lateral position is free
  ([Chalmers PDF](https://publications.lib.chalmers.se/records/fulltext/250879/250879.pdf)).
- **vs us:** the geometry-overlap conflict-area idea is a clean alternative to
  SUMO's bitset matrix, but it presumes free 2D movement; our lane-discrete model
  fits the connection/foe style better. Min-gap/min-clearance parameters are the
  right shape for our yield/stop gap acceptance hooks.

### Aimsun Next — sections, nodes, turnings with control actions
- Sections meet at nodes; turnings carry `None/Yield/Stop/RTOR` actions, turn
  speed, and per-turn geometry; nodes support a "yellow box" keep-clear flag
  ([Node editing](https://docs.aimsun.com/next/22.0.4/UsersManual/NodeEditing.html)).
- **Supernodes** collapse junction clusters into one object with full per-movement
  costs and conflict tables for macro assignment — the same disease netconvert's
  `--junctions.join` treats, different medicine ([Node editing](https://docs.aimsun.com/next/22.0.4/UsersManual/NodeEditing.html)).
- **vs us:** turn-with-action is the minimal viable right-of-way encoding; the
  RTOR action is exactly the kind of per-connection flag VISION's signalized
  intersections need. Supernodes confirm junction clusters are a universal pain.

### MITSIM / TRANSIMS (calibration points)
- MITSIM (1990s): "nodes, links, segments, and lanes" with lane connections,
  lane-use privileges, and turning regulations in one network database built by a
  graphical editor ([MIT thesis PDF](http://dspace.mit.edu/bitstream/handle/1721.1/10360/37511992-MIT.pdf?sequence=2)).
- **vs us:** the 30-year-old schema already had lanes + connections + turn
  regulation; the concepts are stable — only the tooling and formats change.

## HD-map frameworks (autonomous driving)

### ASAM OpenDRIVE — the industry interchange for road geometry
- Reference-line model: one reference line per road, lanes/elevation/signals
  attached in `s/t` coordinates; lane sections; per-lane predecessor/successor
  links; junctions made of **connecting-roads** (the only overlapping-surface
  roads) ([ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).
- XML (`.xodr`, zipped `.xodrz`); current version 1.9.0 (May 2026); spec download
  free of charge; developed by ASAM e.V. members (BMW, Mercedes-Benz, Porsche,
  AVL, Continental, dSPACE, Vector, ...); the OpenX suite is in maintenance mode
  ([ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).
- Users: CARLA (its maps *are* OpenDRIVE 1.4; waypoints address
  `road_id, section_id, lane_id, s`) ([CARLA core map](https://carla.readthedocs.io/en/latest/core_map/)),
  esmini (MPL-2.0 OpenSCENARIO player with an OpenDRIVE RoadManager)
  ([esmini](https://github.com/esmini/esmini)), the levelX inD/rounD/uniD drone
  datasets (OpenDRIVE v1.4 maps shipped) ([inD format PDF](https://levelxdata.com/wp-content/uploads/2024/03/inD-Format_1_1.pdf)),
  and netconvert can import/export it ([SUMO Import](https://sumo.dlr.de/docs/Networks/Import.html)).
- OpenCRG is its road-*surface* companion (curved regular grid elevation/friction)
  — irrelevant to our lane-level fidelity bar ([ASAM OpenCRG](https://www.asam.net/standards/detail/opencrg/)).
- **vs us:** a geometry interchange, not a sim graph: no right-of-way matrices,
  no queue/occupancy semantics, verbose parametric lanes. Adopting it *internally*
  buys complexity we don't need; supporting it as an **import/export target
  later** buys the AV-tooling ecosystem and levelX dataset compatibility
  (see [[domain-trajectory-datasets]]).

### Lanelet2 — lanelets + regulatory elements, the cleanest semantics
- Five primitives: points (the only position owners), linestrings, **lanelets**
  (atomic directed sections bounded by left/right linestrings), areas (undirected),
  and **regulatory elements** referencing rule sources (signs, lights, stop lines)
  and governed lanelets; regElems can be dynamic ([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
- Traffic rules are *interpreted*, not compiled: a module answers "may this
  participant, in this country, pass/change lanes here" per lanelet; routing
  graphs derive following/adjacent/conflicting relations per participant class
  ([paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf),
  [lanelet2_routing](https://index.ros.org/p/lanelet2_routing/)).
- Stored in OSM XML ("the actual data format of a map is considered to be
  irrelevant and interchangeable"); C++ core with Python/ROS bindings
  ([paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
- **vs us:** the intellectual model to steal: *verifiability* (every rule traces
  to an observable element), *separation of representation and interpretation*,
  and geometry-by-reference (points own position). Its full generality (areas,
  arbitrary overlaps) exceeds our needs.

### CommonRoad — benchmark packaging around lanelets
- Scenario = `LaneletNetwork` + obstacles + planning problems; deliberately
  lightweight ("as expressive as ... OpenDRIVE, yet ... lightweight")
  ([CommonRoad paper PDF](https://mediatum.ub.tum.de/doc/1379638/776321.pdf)).
- Format versions: XML (2020a) → **protobuf (2024)**
  ([commonroad-io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html));
  official converters to/from Lanelet2, OpenDRIVE, OSM, SUMO
  ([scenario-designer docs](https://commonroad-scenario-designer.readthedocs.io/en/latest/details/osm/)).
- **vs us:** a scenario container, not a live sim graph — but its lanelet network
  + converter suite makes it the natural bridge if we ever exchange networks with
  the motion-planning community; note [[concept-scenario-format]].

## Interchange standards

### GMNS (General Modeling Network Specification) — CSV for planners
- Zephyr Foundation spec with FHWA support, first release January 2020: a small
  set of human- and machine-readable CSV tables for routable networks, designed
  for multi-resolution/multi-modal models with time-varying attributes
  ([TRID record](https://trid.trb.org/View/1909441),
  [spec](https://zephyr-data-specs.github.io/GMNS/)).
- Ecosystem: NeXTA editor, simwrapper's TypeScript GMNS→GeoJSON parser
  ([simwrapper/gmns](https://github.com/simwrapper/gmns)); A/B Street imported
  GMNS signal timing ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
- **vs us:** link/node-level, not lane-level — too coarse for our graph, but a
  reminder that CSV + stable IDs is how the *planning* world shares networks; a
  possible future export for demand-model coupling (see [[concept-scenario-format]]).

## Positioning Summary

| System | Atom | Junction interior | Right-of-way | Occupancy | Format |
|---|---|---|---|---|---|
| SUMO | lane (in edge) | internal lanes + internal junctions | compiled response/foes bitsets per junction type | continuous s, 1/lane laterally | XML (plain + compiled .net.xml) |
| A/B Street | lane; turn (src,dst,ix) | turn line-strings | conflict via geometry intersection; signals on movements | per-lane queue, lazy position | binary map + JSON interchange |
| MATSim | link | none | none (allowed next-links) | FIFO storage queue | XML (DTD) |
| Vissim | link/connector | none (connectors pass through) | conflict areas + priority rules | continuous 2D | proprietary .inpx |
| Aimsun | section/node/turn | node area + yellow box | per-turn action (Yield/Stop/RTOR) | continuous s (micro) | proprietary |
| OpenDRIVE | lane (in road/lanesection) | connecting-roads + laneLinks | sign/signal records only | n/a (map, not sim) | XML .xodr (ASAM, free) |
| Lanelet2 | lanelet + regElem | lanelets overlap freely | regulatory elements, interpreted per participant | n/a (map) | OSM XML |
| CommonRoad | lanelet network | — | — | n/a (benchmark) | XML 2020a / protobuf 2024 |
| **traffic-sim (us)** | **lane (in edge)** (proposed) | **internal lanes + conflict matrix** (proposed) | **junction-type-compiled rules + per-connection params** (proposed) | **continuous s, discrete lane, LC as maneuver** (per VISION) | **authoring JSON/YAML + compiled binary** (proposed) |
