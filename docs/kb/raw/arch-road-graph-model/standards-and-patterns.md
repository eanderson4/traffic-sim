# Standards & Patterns: Road Graph Model

> Source: standards documents, academic papers + pattern identification |
> Researched: 2026-07-16

## Formalisms & Standards

### ASAM OpenDRIVE (OpenX suite) — road-network geometry standard
- One reference line per road; lanes, elevation and signals attached via `s/t`
  coordinates; roads linked by predecessor/successor at road and lane level;
  junctions composed of connecting-roads ("the only roads within ASAM OpenDRIVE
  with overlapping surfaces") ([ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).
- Licensing: the specification is a **free download** and openly implementable
  ("The download of the standard ASAM OpenDRIVE is free of charge"); governance is
  the ASAM e.V. consortium (BMW, Mercedes-Benz, Porsche, AVL, dSPACE, Vector,
  Continental, ...); the OpenX suite is in maintenance mode with change requests
  open to members ([ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).
- Scope boundary: it is a *map* format — right-of-way logic is only represented as
  signals/signs records; there is "a traffic rule identifier, but without a
  unified method or structure to represent traffic or behavior rules at all"
  ([BSSD paper, arXiv:2202.05211](https://ar5iv.labs.arxiv.org/html/2202.05211)).

### ASAM OpenCRG — road surface companion (out of scope, noted)
- Curved-regular-grid elevation/friction data along a reference line; ASCII or
  binary with clear-text headers; inherited from VIRES, ASAM-maintained since
  2018 ([ASAM OpenCRG](https://www.asam.net/standards/detail/opencrg/)).
- For tire/vibration/3D-rendering fidelity — below VISION's lane-level bar.

### Lanelet2 primitives (de-facto HD-map formalism)
- Points (sole position owners), linestrings, lanelets (atomic: traffic rules and
  topological relations constant within), areas, regulatory elements; lanelets
  may overlap; boundary *type* encodes lane-change permission; successive
  lanelets share boundary endpoints ([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).
- Three layers: physical (observable), relational (lanelets/areas/regElems),
  topological (derived passability network per participant) — representation is
  deliberately separated from interpretation ([paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).

### CommonRoad scenario format
- Lanelet network + obstacles + planning problem, XML (2020a) and protobuf (2024)
  ([paper PDF](https://mediatum.ub.tum.de/doc/1379638/776321.pdf),
  [commonroad-io](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html)).

### GMNS — General Modeling Network Specification
- Zephyr Foundation + FHWA; CSV tables for routable networks with time-varying
  attributes; first public release January 2020; aimed at sharing between
  planning/operations tools ([TRID](https://trid.trb.org/View/1909441),
  [spec](https://zephyr-data-specs.github.io/GMNS/)).

### SUMO network schemas (de-facto micro-sim standard)
- `.net.xml` validated by `net_file.xsd`; plain-XML authoring files each have XSDs
  (nodes/edges/types/connections/tllogic) ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
  [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- Junction-type enum as the right-of-way taxonomy: `priority`, `priority_stop`,
  `allway_stop`, `traffic_light`, `traffic_light_right_on_red`,
  `traffic_light_unregulated`, `right_before_left`, `left_before_right`, `zipper`,
  `unregulated`, `rail_signal`, `rail_crossing`, `dead_end`
  ([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
- The "Road Intersection Model in SUMO" (Krajzewicz et al.) formalizes the
  approach: vehicles register their approach, the junction decides pass/brake via
  a right-of-way matrix over crossing connections, internal lanes give conflicts
  physical extent ([CORE PDF](https://core.ac.uk/download/pdf/31007126.pdf)).

### The OSM data model (upstream of everything)
- Ways/nodes/relations with free-form tags: lane counts, `turn:lanes`,
  restriction relations spanning multiple ways, signal nodes placed *ahead* of the
  intersection — the messy input every importer heuristically compiles
  ([A/B Street retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html),
  [SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- Deep treatment is [[integration-osm-extraction]]'s scope.

## Design Patterns Identified

### Lane-as-atom, edge-as-container
All lane-level sims make the lane the navigable and occupancy unit; edges group
lanes for shared attributes (priority class, name, permissions)
([SUMO](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
[OpenDRIVE](https://www.asam.net/standards/detail/opendrive/),
[Lanelet2](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).

### Typed junction → compiled right-of-way matrix
Declare *what kind* of junction this is (priority, all-way stop, signal, zipper,
roundabout-arm); a build-time compiler derives per-connection conflict/response
data; the runtime only evaluates precomputed bitsets/lists against gap parameters
([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
[Krajzewicz PDF](https://core.ac.uk/download/pdf/31007126.pdf)).
Benefits: fast runtime decisions, inspectable compiled artifact, type-level test
surface.

### Internal lanes with real geometry
Give in-junction trajectories physical existence (polyline, length, speed limit),
so vehicles can queue, block, and wait *inside* the box; add internal wait
positions for protected-but-yielding maneuvers ([SUMO Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
Conflict detection then falls out of geometry: intersecting internal-lane shapes
is "static and can be computed as such" ([Paderborn PDF](https://digital.ub.uni-paderborn.de/hs/content/titleinfo/3512356/full.pdf)).

### Geometry-by-reference (single owner of position)
Points/geometry stored once; topology holds references (Lanelet2 points;
OpenDRIVE `s/t` on one reference line; SUMO per-lane polylines are the weaker
variant). Guarantees consistency under edit and gives a natural provenance anchor
([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf),
[ASAM OpenDRIVE](https://www.asam.net/standards/detail/opendrive/)).

### Two-representation (authoring ↔ compiled) with lossless converter
Human-editable plain files ⇄ machine-optimized compiled network, converted both
ways "without information loss"; the compiled artifact is *never* hand-edited
([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
This is netconvert's best idea, separable from its heuristic baggage.

### Connection guessing + explicit override
Generate lane-to-lane connections heuristically, but let the user patch any of
them (`.con.xml` patch files; `reset="true"` re-guess; A/B Street's
"filter-out-but-never-orphan" fallback)
([PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html),
[issue #67](https://github.com/a-b-street/osm2streets/issues/67)).

### Movement bundling for signal logic
Lane-level turns are grouped into per-road-segment *movements* for signal phases —
the granularity signals and metrics actually want
([issue #67](https://github.com/a-b-street/osm2streets/issues/67)).

### Provenance preservation through the pipeline
Carry source IDs into the compiled graph (SUMO `origID` params, OSM-way→edge
`#n`/`-` naming, cluster naming; `location` block with offset/bounds/projection)
([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
[SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).

### Roundabout-as-annotation, not type
SUMO keeps `<roundabout nodes edges>` as *metadata* that influences right-of-way
(inside always wins) and lane-changing — the junction objects stay ordinary
([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html),
[PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).

### Interpreted rules vs compiled rules (the Lanelet2 alternative)
Keep rules as inspectable objects linked to their physical source and answer
queries per participant at runtime — slower, but verifiable and
country/participant-parameterized ([Lanelet2 paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf)).

## Anti-patterns (documented failures)

1. **Hand-editing the compiled network artifact** — subtle inter-dependencies
   (junction logic vs connections vs internal lanes) silently corrupt
   ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).
2. **Leaving dual-carriageway junction clusters unjoined** — "high risk of low
   throughput, jams and even deadlocks"; invalid two-leg left-turn trajectories;
   long vehicles blocking each other mid-cluster
   ([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
   [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
3. **Trusting imported defaults**: typemap values "set-up ad-hoc and are not yet
   verified"; guessed node types "may not be the one you intended"
   ([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
   [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html)).
4. **Partial turn-lane marking → through-lane assumption** — netconvert's
   documented misinterpretation of half-tagged roads
   ([OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
5. **Broken turn generation surfacing as gridlock** — "broken intersection
   geometry causing impossible turn conflicts" was a top A/B Street deadlock
   cause ([retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html)).
6. **Equal-priority standoffs** — vehicles on three equal-priority approaches all
   believed they had right-of-way and deadlocked a junction (mailing-list war
   story; fix: distinct edge priorities)
   ([sumo-user msg04310](https://www.eclipse.org/lists/sumo-user/msg04310.html)).
7. **Lane-change only at intersections** (A/B Street's original dodge) — moved
   lane choice into turns to skip mid-road lane-changing; produced illegal-looking
   weaves and route-following failures ([discrete-event article](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html)).
8. **Orphaning lanes when filtering turns** — a lane with no outgoing turn
   corrupts routing; generators must always leave a fallback
   ([issue #67](https://github.com/a-b-street/osm2streets/issues/67)).
9. **Coupling lane width to a constant per edge** — OpenDRIVE's polynomial widths
   don't survive; SUMO users are told to split the edge to change widths
   ([sumo-user msg14614](https://www.eclipse.org/lists/sumo-user/msg14614.html)).
10. **Route-length corruption when flattening junctions** —
    `--no-internal-links` must patch edge lengths to junction-center distance or
    routes come out systematically short ([Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).

## Empirical anchors

- Junction scale ceiling: 256 connections/junction in SUMO (64 before 0.25.0)
  ([SUMO Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html)).
- SUMO defaults worth inheriting or overriding knowingly: lane width 3.2 m;
  connection `visibility` 4.5 m (100 m for zipper); turn-speed factor 5.5 in
  `sqrt(radius × factor)`; junction-join search distance 10 m; TLS-guess distance
  25 m ([SublaneModel](https://sumo.dlr.de/docs/Simulation/SublaneModel.html),
  [PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html),
  [OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
  [Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html)).
- MATSim storage cell: 7.5 m per vehicle per lane for link capacity
  ([evacuation manual PDF](https://data.bris.ac.uk/datasets/333uc5aebpzfz25mhmd83yt3yk/Manual_Agent_Based%20MATSim.pdf)).
- CARLA waypoint identity: hash of `(road_id, section_id, lane_id, s)`; 2 cm
  dedup granularity ([CARLA core map](https://carla.readthedocs.io/en/latest/core_map/)).
- OpenDRIVE current version 1.9.0 (May 2026), but field tools/datasets still
  standardize on v1.4 ([ASAM](https://www.asam.net/standards/detail/opendrive/),
  [CARLA core map](https://carla.readthedocs.io/en/latest/core_map/),
  [inD format PDF](https://levelxdata.com/wp-content/uploads/2024/03/inD-Format_1_1.pdf)).

## Open Questions

- Does a per-connection conflict matrix scale to our largest target networks
  without memory blowup? (SUMO's bitsets are junction-local — fine — but our
  Go representation should be sized.)
- Can we compile right-of-way for *all* VISION junction types (4-way stop,
  signal, roundabout, yield, uncontrolled, ramps) from one small rule engine,
  or does the 4-way stop need arrival-order state the matrix can't express?
  (SUMO implements `allway_stop` behaviorally beyond the static matrix —
  confirm exact mechanism before ADR.)
- Protobuf vs Go-native gob/flatbuffers for the compiled network — benchmark
  with real network sizes; CommonRoad's protobuf move suggests protobuf is the
  community legible choice.
- How much of netconvert's OSM heuristics do we re-implement vs shell out to
  netconvert/osm2streets as a preprocessor? → [[integration-osm-extraction]].
- Lane widths: constant per lane (SUMO) or per-lane piecewise (cheap middle
  ground short of OpenDRIVE polynomials)?

## Master source list

SUMO: [Road Networks](https://sumo.dlr.de/docs/Networks/SUMO_Road_Networks.html) ·
[PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html) ·
[Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html) ·
[OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html) ·
[Import overview](https://sumo.dlr.de/docs/Networks/Import.html) ·
[SublaneModel](https://sumo.dlr.de/docs/Simulation/SublaneModel.html) ·
[Krajzewicz intersection model](https://core.ac.uk/download/pdf/31007126.pdf) ·
[msg04310](https://www.eclipse.org/lists/sumo-user/msg04310.html) ·
[msg14614](https://www.eclipse.org/lists/sumo-user/msg14614.html) —
A/B Street: [osm2streets README](https://github.com/a-b-street/osm2streets) ·
[issue #67](https://github.com/a-b-street/osm2streets/issues/67) ·
[discrete-event sim](https://a-b-street.github.io/docs/tech/trafficsim/discrete_event/index.html) ·
[retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html) ·
[intersection geometry](https://a-b-street.github.io/docs/tech/map/geometry/index.html) —
ASAM: [OpenDRIVE](https://www.asam.net/standards/detail/opendrive/) ·
[OpenCRG](https://www.asam.net/standards/detail/opencrg/) —
[CARLA core map](https://carla.readthedocs.io/en/latest/core_map/) ·
[esmini](https://github.com/esmini/esmini) —
Lanelet2: [paper](https://www.mrt.kit.edu/z/publ/download/2018/Poggenhans2018Lanelet2.pdf) ·
[routing](https://index.ros.org/p/lanelet2_routing/) —
CommonRoad: [paper](https://mediatum.ub.tum.de/doc/1379638/776321.pdf) ·
[io docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html) —
GMNS: [spec](https://zephyr-data-specs.github.io/GMNS/) ·
[TRID](https://trid.trb.org/View/1909441) ·
[simwrapper/gmns](https://github.com/simwrapper/gmns) —
MATSim: [book extract](https://www.matsim.org/files/book/partOne-latest.pdf) ·
[OsmNetworkReader](https://www.matsim.org/doxygen/classorg_1_1matsim_1_1core_1_1utils_1_1io_1_1_osm_network_reader.html) ·
[pt2matsim](https://github.com/matsim-org/pt2matsim/blob/master/src/main/java/org/matsim/pt2matsim/config/OsmConverterConfigGroup.java) ·
[storage cell](https://data.bris.ac.uk/datasets/333uc5aebpzfz25mhmd83yt3yk/Manual_Agent_Based%20MATSim.pdf) —
Aimsun: [Node editing](https://docs.aimsun.com/next/22.0.4/UsersManual/NodeEditing.html) ·
[Vissim importer](https://docs.aimsun.com/next/24.0.3/UsersManual/VissimImporter.html) —
Vissim: [PTV priority rules](https://cgi.ptvgroup.com/vision-help/VISSIM_2023_ENG/Content/5_Netzbearbeiten/Querverkehrsstoerungen_Aufbau.htm) ·
[ETH conflict areas](https://ethz.ch/content/dam/ethz/special-interest/baug/ivt/ivt-dam/publications/students/601-700/sa680.pdf) ·
[Chalmers](https://publications.lib.chalmers.se/records/fulltext/250879/250879.pdf) ·
[de Jong thesis](https://www.victorknoop.eu/research/theses/MScThesis_EM_deJong.pdf) ·
[SJSU](https://transweb.sjsu.edu/sites/default/files/1712-Pande-Assessing-Complete-Street-Strategies.pdf) —
[MITSIM thesis](http://dspace.mit.edu/bitstream/handle/1721.1/10360/37511992-MIT.pdf?sequence=2) ·
[Paderborn IMS](https://digital.ub.uni-paderborn.de/hs/content/titleinfo/3512356/full.pdf) ·
[BSSD](https://ar5iv.labs.arxiv.org/html/2202.05211) ·
[inD format](https://levelxdata.com/wp-content/uploads/2024/03/inD-Format_1_1.pdf) ·
[osm-to-xodr](https://github.com/RISE-Dependable-Transport-Systems/osm-to-xodr)
