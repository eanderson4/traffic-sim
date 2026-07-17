# Synthesis: Road Graph Model

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds a future ADR on the network model (unnumbered). This synthesis
> recommends; the ADR decides.

## Summary

The research question was how to represent a lane-level directed road graph —
nodes/edges/lanes/connections, intersection semantics, vehicle occupancy,
geometry vs topology, and file formats. Across ~10 surveyed systems the field
converges hard: **the lane is the navigable atom; edges are attribute
containers; lane-to-lane connections are explicit objects; junctions are typed
and their right-of-way is compiled at build time into per-connection conflict
data; intersection interiors are physical (internal lanes with geometry);
vehicles occupy a continuous longitudinal position on a discrete lane.** SUMO's
.net.xml is the closest full embodiment of VISION's requirements; Lanelet2
contributes the cleanest ideas (geometry-by-reference, rules linked to their
physical source); A/B Street/osm2streets contributes the most honest failure
catalog; OpenDRIVE is an interchange format to import/export later, not an
internal model. Nothing surveyed contradicts VISION's "lane changes, no
within-lane swerving" — that is exactly SUMO's default occupancy point.

## Source Files

- [Mechanics: what a road graph is built from](./implementation.md)
- [Prior art survey](./competitors.md)
- [Standards, formalisms, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions (for the network-model ADR)

### 1. Lane-as-atom internal representation
**Choice:** Directed multigraph: typed junction nodes + unidirectional edges;
each edge bundles an ordered lane list (index 0 = right-most, SUMO convention).
Lanes are first-class objects carrying speed limit, length, geometry reference,
permissions. **Adjacency lives at lane level** as explicit connection objects
`(fromLane, toLane, junction, dir, state, via-internal-lane)`; edge-level
adjacency is derived for routing.
**Why:** Every lane-level system surveyed (SUMO, A/B Street, OpenDRIVE lane
links, Lanelet2) stores or derives lane-level connectivity; MATSim's
link-level-only model is exactly the one that *can't* do lane changes or
intersection right-of-way. Explicit connections are also where per-movement
data (turn speed, visibility, signal binding, RTOR) naturally attaches
(SUMO `.con.xml` attributes, Aimsun turn actions).
**Trade-off:** Two adjacency tiers to maintain (lane + edge), and connection
generation becomes a build step with an orphan-lane invariant ("every lane
should lead somewhere", A/B Street).
**Field context:** [implementation §1–2](./implementation.md).

### 2. Vehicles occupy (lane, s) continuously; lane change is a maneuver, not a trajectory
**Choice:** Position contract = `(laneId, s)` continuous in meters + vehicle
length; laterally a vehicle is on exactly one lane (two during a lane change
with a SUMO-`--lanechange.duration`-style timed transition). No sublane model,
no continuous lateral — matching VISION's explicit exclusion.
**Why:** This is SUMO's default occupancy point and supports the validated
IDM/MOBIL stack from [[domain-traffic-flow-models]] (leader/follower queries
are per-lane sorted lists). The sublane model exists in SUMO only for
motorcycles/lateral phenomena we explicitly don't simulate, and costs runtime.
Queue models (MATSim, A/B Street) sacrifice car-following; continuous-2D
(Vissim) forces painted conflict areas instead of lane logic.
**Trade-off:** Partial-lane-blocking phenomena (wide vehicles straddling lanes,
motorcycle pairs) are unrepresentable — accepted by VISION. Timed lane changes
need A/B-Street-style "ghost" reservation or SUMO-style shadow-lane bookkeeping
in both lanes for the maneuver duration.
**Field context:** [implementation §5](./implementation.md); A/B Street's
laggy-heads/ghosts machinery is the cautionary detail file
([competitors](./competitors.md)).

### 3. Junctions are first-class and typed; right-of-way is compiled, not evaluated
**Choice:** Junction type enum covering VISION's list: `priority` (yield),
`stop_minor` (priority_stop), `allway_stop`, `signalized` (+ RTOR flag),
`roundabout` (annotation over member edges/nodes, inside-wins rule),
`uncontrolled`, `zipper` (merges), `dead_end`; highway sections/ramps/frontage
roads need *no* special junction types — they fall out of edges + zipper/priority
merges. At network build, compile per-connection **conflict sets + priority
class** (SUMO's response/foes bitsets, max 256 connections/junction precedent)
from junction type, edge priorities, and internal-lane geometry intersections.
Physical **internal lanes** with polylines inside the box; optional internal
wait positions for yield-on-green maneuvers; keep-clear (no-block) heuristic
per junction/connection.
**Why:** Compilation moves geometry intersection and rule evaluation to build
time ("the information is static and can be computed as such"), gives a
deterministic, inspectable artifact, and keeps the per-tick runtime to bitset
tests + gap checks. Internal lanes are what make junction blocking, in-box
waiting, and queue spillback *physical* rather than special-cased; SUMO's
`--no-internal-links` mode documents exactly what you lose without them.
**Trade-off:** A build/compiler stage to write and test; arrival-order rules
(4-way stop FIFO) need runtime state beyond the static matrix (SUMO implements
`allway_stop` behaviorally — its exact mechanism is under-documented, see Open
Questions).
**Field context:** [implementation §3–4, §7](./implementation.md).

### 4. Geometry stored once and referenced; (lane, s) is the universal address
**Choice:** A geometry store owns polylines (lane center-lines, junction shapes,
internal-lane curves); topology holds references, never copies. Metric local
coordinates, north-up, with a provenance block (offset, bounds, projection
definition — SUMO's `location` element pattern). All vehicle addressing,
metrics, and messages use `(laneId, s)`.
**Why:** Lanelet2's "points are the only primitives that have position
information" is the strongest statement of the pattern — edits stay consistent,
and rendering/metrics/sim all read one geometry. CARLA independently addresses
vehicles as `(road, section, lane, s)`; our metrics topics need per-lane
geometry for Edie-style x-t fields ([[domain-trajectory-datasets]] precedent)
and MapLibre heatmaps need the same polylines as GeoJSON/vector tiles
([[integration-maplibre-realtime]]).
**Trade-off:** Indirection on every geometry lookup; polyline s↔xy projection
code must be written once, carefully (arc-length parameterization).
**Field context:** [implementation §6](./implementation.md).

### 5. Two-representation file format: authoring JSON/YAML + compiled internal; OpenDRIVE only as later interchange
**Choice:** v1: a human-editable authoring format (YAML or JSON — VISION
requires *diffable* scenario variants) covering nodes/edges/lanes/connections/
junction types, compiled at load into a versioned internal representation
(protobuf or Go-native; decide by benchmark). Keep the compiled artifact
loadable directly for replay determinism. Do NOT adopt OpenDRIVE internally;
reserve it as an import/export target behind the same compiler.
**Why:** netconvert's plain-XML ⇄ .net.xml lossless duality is the proven
pattern — humans author, machines compile, the compiled form is never
hand-edited. OpenDRIVE is a geometry interchange with no right-of-way
semantics ("no unified method to represent traffic rules"), parametric
polynomial lanes we don't need, and XML verbosity; but it is the AV world's
lingua franca (CARLA, esmini, levelX datasets) and free to implement, so the
*import path* has future value. CommonRoad's 2020a-XML → 2024-protobuf
migration shows where the field is going for machine formats.
**Trade-off:** We invent yet another network schema (justified: nothing open is
simultaneously lane-level, right-of-way-complete, human-authorable, and
sim-ready — see The Genuine Gap); YAML authoring needs JSON-Schema-style
validation to avoid the "hand-edited .net.xml" corruption class.
**Field context:** [implementation §10](./implementation.md),
[standards-and-patterns](./standards-and-patterns.md).

### 6. Import is a separate heuristic-compiler stage with provenance and flagged guesses
**Choice (reserved for [[integration-osm-extraction]]):** the OSM importer is a
distinct pipeline stage that (a) preserves source IDs on every element
(`origID` params, SUMO's way→`edge#n`/`-` naming and cluster naming), (b) marks
*every heuristically generated element* (joined junction, guessed signal,
guessed ramp lane, guessed connection) as such in the compiled network,
(c) accepts patch files and re-compiles, (d) emits a warnings taxonomy like
netconvert's. The graph schema must therefore carry provenance + "guessed"
flags from day one.
**Why:** The netconvert/A-B Street literature is unambiguous that import
heuristics are where networks go wrong: unjoined dual-carriageway clusters
cause "low throughput, jams and even deadlocks"; OSM turn-lane tags are "often
flat-out wrong"; partial turn markings get misread as through-lanes; default
type values are confessed guesses; broken turn conflicts surface as *simulation
gridlock* (A/B Street's top failure class). Auditability is the only defense:
every guess must be findable, overridable, and re-compilable.
**Trade-off:** Provenance/flag fields add schema weight; the diff/variant
workflow must decide whether patches live inside the network file or in
scenario overlays ([[concept-scenario-format]]).
**Field context:** [implementation §8–9](./implementation.md).

## Compare/Contrast: Us vs the Field

| Dimension | SUMO | A/B Street | Lanelet2 | OpenDRIVE | MATSim | us (proposed) |
|---|---|---|---|---|---|---|
| Atom | lane in edge | lane + turn | lanelet | lane in road/lanesection | link | **lane in edge** |
| Junction interior | internal lanes + internal junctions | turn line-strings | overlapping lanelets | connecting-roads | none | **internal lanes + wait points** |
| Right-of-way | compiled response/foes per junction type | geometry-intersect conflicts | interpreted regElems per participant | signs/signals records only | none | **compiled conflict sets per junction type** |
| Occupancy | continuous s, 1/lane laterally | queue + lazy position | n/a | n/a | FIFO storage queue | **continuous s, discrete lane** |
| Geometry | per-lane polylines | thickened line-strings | points own position | reference line + s/t | node coords only | **geometry store, referenced** |
| Rules representation | junction type → matrix | turn types + conflicts | source-linked rule objects | n/a | allowed next-links | **junction type → matrix + per-connection params** |
| Format | XML plain ⇄ compiled | binary + JSON | OSM XML | XML .xodr (ASAM) | XML DTD | **YAML authoring ⇄ compiled (proto/native)** |
| OSM path | netconvert heuristics | osm2streets transforms | converters | converters | OsmNetworkReader | **reserved: [[integration-osm-extraction]]** |

## The Genuine Gap (again)

**Right-of-way-as-data is undocumented as an artifact.** SUMO's
response/foes bitset matrix is the only compiled, inspectable intersection
semantics in the open literature — and it is documented as an internal file
format, not a spec; Lanelet2 keeps rules interpreted; everyone else hides it in
code. Nobody publishes a simulator-independent, compiled intersection-conflict
representation. Second: the **netconvert defect catalog** — which import
heuristics fail, how often, and with what downstream sim symptoms — exists only
scattered across SUMO doc pages, mailing-list threads, and one brutally honest
A/B Street retrospective; there is no systematic account. Third: the humble
**4-way stop** (arrival-order FIFO + tie-breaking) is the least-specified
common junction behavior in every public doc set we found. A traffic-sim that
publishes its compiled junction semantics + an importer defect taxonomy would,
again, be writing near the frontier.

## Open Questions

- Exact mechanism of SUMO's `allway_stop` (arrival ordering, tie-breaking,
  creep behavior) — not fully documented in public pages; read the source or
  experiment before the ADR fixes our 4-way-stop semantics.
- Protobuf vs Go-native encoding for the compiled network — benchmark at real
  network sizes; CommonRoad's 2024 protobuf move is the community-legible
  default.
- Lane width model: constant-per-lane (SUMO) vs piecewise segments — OpenDRIVE's
  polynomial widths are the overkill pole ([standards §Anti-patterns 9](./standards-and-patterns.md)).
- Signal-controller ↔ connection binding: SUMO's `tl` + `linkIndex` pattern is
  the obvious template; final shape belongs to [[domain-signal-control]].
- Connection-conflict memory footprint at city scale — size the Go
  representation (bitsets vs sorted id lists) with a benchmark.
- Do frontage roads need anything beyond ordinary parallel edges + ramp
  junctions? (No counter-evidence found; verify with a real example during
  scenario authoring.)

## Connections to Other Topics

- **Decides:** the future network-model ADR — this research is its research gate.
- **Constrains:** [[arch-state-authority]] (world state = this graph + vehicles;
  single-goroutine fixed iteration order over lanes/connections per ADR-0005),
  [[concept-vehicle-controller-interface]] (the `(laneId, s)` position contract
  and lane/connection visibility exposed to controllers),
  [[concept-scenario-format]] (network file as scenario component; diffable
  variants; patches vs overlays), [[integration-maplibre-realtime]] (per-lane
  polylines → vector tiles; per-lane congestion heatmaps),
  [[domain-congestion-metrics]] (per-lane/per-connection/per-junction metric
  anchoring; queue lengths on internal lanes; Edie x-t fields on lane geometry),
  [[domain-signal-control]] (signal-to-connection binding; junction type
  `signalized` + RTOR), [[arch-nats-backbone]] (network distribution to clients:
  large static payload at subscribe time, KV vs stream).
- **Depends on:** ADR-0005 (accepted time model — fixed tick over this graph);
  [[domain-traffic-flow-models]] (IDM leader/follower per lane; MOBIL needs
  connection adjacency; validated 100 ms tick bounds internal-lane traversal
  fidelity).
- **Relates to:** [[domain-macroscopic-flow-models]] (CTM/LTM cells would
  partition the same lane geometry), [[domain-trajectory-datasets]] (levelX
  inD/rounD maps are OpenDRIVE v1.4 — import value; NGSIM I-80 is a fixed
  highway section our highway/ramp types must reproduce),
  [[integration-osm-extraction]] (consumes the provenance/guess-flag schema
  reserved here), [[domain-simulator-landscape]] (overlapping system survey,
  deferred to that topic).
