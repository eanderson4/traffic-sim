# Road Graph Model

> Lane-as-atom directed multigraph: typed junctions with build-time-compiled right-of-way conflict sets, physical internal lanes, `(laneId, s)` vehicle addressing, and diffable-authoring ⇄ compiled-binary format duality.

## Overview

The road graph is the data structure every other subsystem stands on: the engine's world state is this graph plus the vehicles occupying it, controllers perceive and act through it, metrics anchor to it, and the visualizer renders it. The research question was how to represent a lane-level directed road graph — nodes, edges, lanes, connections, intersection semantics, vehicle occupancy, geometry vs topology, and file formats — surveying ~10 systems (SUMO, A/B Street/osm2streets, MATSim, Vissim, Aimsun, MITSIM, Lanelet2, OpenDRIVE, CommonRoad, GMNS).

The field converges hard on one shape: **the lane is the navigable atom; edges are attribute containers; lane-to-lane connections are explicit objects; junctions are typed and their right-of-way is compiled at build time into per-connection conflict data; intersection interiors are physical internal lanes; vehicles occupy a continuous longitudinal position on a discrete lane.**

SUMO's `.net.xml` is the closest full embodiment of [VISION](../../../VISION.md)'s requirements; Lanelet2 contributes the cleanest ideas (geometry-by-reference, rules linked to their physical source); A/B Street contributes the most honest failure catalog; OpenDRIVE is an interchange format to import/export later, not an internal model. Nothing surveyed contradicts VISION's "lane changes, no within-lane swerving" — that is exactly SUMO's default occupancy point.

Status: the synthesis *recommends*; a network-model ADR (unnumbered, unwritten) decides. Three edges of the topic are already ratified: the position contract by [ADR-0007](../../decisions/ADR-0007-vehicle-model.md), the import pipeline by [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md), and the tick that walks this graph by [ADR-0005](../../decisions/ADR-0005-time-model.md).

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Lane-atom multigraph | raw/arch-road-graph-model/implementation.md §1 | Directed graph: typed junction nodes, unidirectional edges bundling ordered lanes (index 0 = right-most) |
| Lane-to-lane connections | raw/arch-road-graph-model/implementation.md §2 | Explicit turn-fabric objects carrying direction, right-of-way state, speed, visibility, signal binding |
| Typed junctions + conflict matrix | raw/arch-road-graph-model/implementation.md §3–4 | Junction-type enum compiled at build time into per-connection response/foes conflict sets |
| Internal lanes & wait positions | raw/arch-road-graph-model/implementation.md §3 | Physical in-box geometry so vehicles can queue, block, and wait inside junctions |
| Occupancy / position contract | raw/arch-road-graph-model/implementation.md §5; decisions/ADR-0007 | Continuous front-bumper `s` on exactly one lane; lane change as a timed maneuver |
| Geometry store | raw/arch-road-graph-model/implementation.md §6 | Polylines stored once and referenced by topology; metric north-up frame with provenance block |
| Authoring ⇄ compiled formats | raw/arch-road-graph-model/implementation.md §10 | Human-diffable YAML/JSON compiled to a versioned internal binary; OpenDRIVE reserved as interchange |
| Provenance & `guessed` flags | raw/arch-road-graph-model/implementation.md §8–9; decisions/ADR-0009 | Durable IDs, OSM IDs as provenance, every heuristic element flagged and overridable |
| OSM import stage | raw/arch-road-graph-model/implementation.md §9; decisions/ADR-0009 | netconvert bootstrap + own Go importer; netconvert kept permanently as differential-testing oracle |

## How It Works

The recommended design position, with its evidence base.

### 1. Skeleton: lane-as-atom multigraph

- Typed junction nodes + unidirectional edges; each edge bundles an ordered lane list (index 0 = right-most, SUMO convention).
- Lanes are first-class objects: speed limit, length, geometry reference, permissions. Edges carry shared attributes (priority class, name).
- Adjacency lives at **lane level**; edge-level adjacency is derived for routing.
- Evidence: every lane-level system surveyed stores or derives lane-level connectivity; MATSim's link-level-only model is precisely the one that *can't* do lane changes or intersection right-of-way.

### 2. Connections are explicit objects

- Shape: `(fromLane, toLane, junction, dir, state, via-internal-lane)` plus behavior knobs — `visibility` (SUMO default 4.5 m; 100 m for zipper), `speed`, `keepClear`, `contPos`, and a signal binding (`tl` + `linkIndex`).
- This is where per-movement data naturally attaches: turn speed, RTOR, signal phase.
- Completeness invariant: *every lane should lead somewhere* — A/B Street keeps a questionable turn rather than orphaning a lane; SUMO deliberately emits "double connections" where lanes die.

### 3. Junctions are typed; right-of-way is compiled, not evaluated

- Type enum covering VISION's list: `priority` (yield), `stop_minor`, `allway_stop`, `signalized` (+ RTOR flag), `roundabout` (annotation over member edges/nodes, inside-wins rule), `uncontrolled`, `zipper`, `dead_end`.
- Highway sections, ramps, and frontage roads need *no* special junction types — they fall out of edges + zipper/priority merges.
- At network build, compile per-connection **conflict sets + priority class** (SUMO's response/foes bitsets; cap 256 connections/junction since 0.25.0, 64 before) from junction type, edge priorities, and internal-lane geometry intersections.
- The geometry is "static and can be computed as such", so per-tick runtime is bitset tests + gap checks against a deterministic, inspectable artifact.
- Caveat: arrival-order rules (4-way-stop FIFO) need runtime state beyond the static matrix — SUMO implements `allway_stop` behaviorally, and its exact mechanism is under-documented (Open Questions).

### 4. Intersection interiors are physical

- Every connection gets an internal lane with real polyline geometry; vehicles drive it like a normal lane subject to blocking constraints.
- SUMO's `--no-internal-links` mode documents what physical interiors buy: without them vehicles "jump" the box and *cannot block the intersection, wait within it for left turns, or collide in it*.
- Optional internal wait positions (`contPos`) support yield-on-green maneuvers; a keep-clear no-block heuristic stops vehicles entering a junction they can't clear.
- Turn-speed physics: `speedLimit = sqrt(radius × factor)`, factor default 5.5, capped at the mean of source and destination lane speeds.

### 5. Occupancy: `(laneId, s)` — ratified by [ADR-0007](../../decisions/ADR-0007-vehicle-model.md)

- Position is a continuous front-bumper `s` in meters on exactly one lane (two during a timed lane change); bumper-to-bumper gap is used everywhere.
- This is SUMO's default occupancy point and supports the validated IDM+MOBIL stack: leader/follower queries are per-lane sorted lists; MOBIL needs the connection adjacency from §2.
- The sublane model is rejected — it exists for motorcycles/partial overlaps that VISION explicitly excludes, at higher runtime cost.
- Trade-off: timed lane changes need A/B-Street-style ghost reservation or SUMO-style shadow-lane bookkeeping in both lanes for the maneuver duration.

### 6. Geometry stored once and referenced

- A geometry store owns all polylines (lane center-lines, junction shapes, internal-lane curves); topology holds references, never copies — Lanelet2: "points are the only primitives that have position information".
- Coordinates are metric, north-up, with a provenance block (offset, bounds, projection definition — SUMO's `location` element pattern) so data can back-project to WGS84.
- `(laneId, s)` is the universal address across simulation, metrics, and messages (CARLA independently addresses `road, section, lane, s` with 2 cm waypoint dedup).
- Trade-off: polyline s↔xy projection (arc-length parameterization) must be written once, carefully.

### 7. Two-representation file format

- v1: human-editable authoring YAML/JSON (VISION requires *diffable* scenario variants) covering nodes/edges/lanes/connections/junction types, compiled at load into a versioned internal representation.
- The compiled artifact is directly loadable for replay determinism and is *never* hand-edited; the proven pattern is netconvert's lossless plain-XML ⇄ `.net.xml` duality.
- OpenDRIVE is **not** adopted internally — no right-of-way semantics ("no unified method to represent traffic rules"), parametric polynomial lanes we don't need, XML verbosity. But it is the AV world's lingua franca (CARLA, esmini, levelX inD/rounD maps ship v1.4; spec free, current 1.9.0 as of May 2026), so an import/export path behind the same compiler has future value.
- CommonRoad's 2020a-XML → 2024-protobuf migration shows where machine formats are going; authoring files need JSON-Schema-style validation to avoid the hand-edited-`.net.xml` corruption class.

### 8. Import is a separate heuristic-compiler stage — decided by [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md)

- The synthesis reserved this for the OSM-extraction topic; the ADR settled it: bootstrap with netconvert, build our own Go importer, keep netconvert permanently as a differential-testing oracle.
- The graph schema therefore carries from day one: **two-tier identity** (durable IDs; OSM IDs as provenance only), **defaults-first lane inference with `guessed` flags** on every heuristic element, an ODbL recipe-not-file posture, and delta-patch network variants.
- Rationale: import heuristics are where networks go wrong (see Gotchas); auditability is the only defense — every guess must be findable, overridable, and re-compilable.

### Constraints arriving from other ADRs

- [ADR-0005](../../decisions/ADR-0005-time-model.md): the fixed 100 ms tick iterates this graph single-goroutine in fixed order over lanes/connections; the validated tick bounds internal-lane traversal fidelity.
- [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) / [ADR-0002](../../decisions/ADR-0002-nats-backbone.md): the network is a large static payload distributed at subscribe time — it rides the config/control side of the three-plane contract, never the small-messages-only hot path.
- [ADR-0008](../../decisions/ADR-0008-controller-contract.md): signal control is an external grants-based client role, so the graph stores only the connection↔signal *binding* (`tl`+`linkIndex` template) — zero signal logic in the engine.

## Gotchas

- **Hand-editing the compiled artifact**: junction logic, connections, and internal lanes have subtle inter-dependencies that silently corrupt; edit the authoring files and recompile (SUMO: `.net.xml` "is not meant to be edited by hand").
- **Unjoined dual-carriageway clusters**: one crossing imported as 2–4 nodes causes "low throughput, jams and even deadlocks", invalid two-leg left-turn trajectories, and long vehicles blocking each other; netconvert joins within 10 m but admits "some junction clusters are too complex for the heuristic" — and sometimes joins wrongly.
- **Trusting imported defaults**: netconvert's typemap values were "set-up ad-hoc and are not yet verified"; OSM turn-lane tags are "often flat-out wrong" or broken by way splits. This is why ADR-0009 mandates `guessed` flags and a differential oracle.
- **Partial turn-lane markings**: where some lanes have turn markings and others don't, the unmarked lanes are misread as through-lanes — a documented netconvert misinterpretation.
- **Broken turns surface as gridlock**: "broken intersection geometry causing impossible turn conflicts" was a top A/B Street deadlock cause — network-model bugs manifest as *simulation* failures far from their source.
- **Equal-priority standoffs**: vehicles on three equal-priority approaches all believed they had right-of-way and deadlocked a junction (mailing-list war story); edge priorities must break ties.
- **Orphaned lanes**: a lane with no outgoing connection corrupts routing; generators must always leave a fallback (filter but never orphan).
- **Lane-change-only-at-intersections**: A/B Street's original dodge — moving lane choice into turns to skip mid-road lane changing — produced illegal-looking weaves and route-following failures. Mid-edge lane changing is not optional.
- **Flattened junctions corrupt routes**: `--no-internal-links` must patch edge lengths to junction-center distance or routes come out systematically short — and it deletes in-box blocking, waiting, and collision.

## Open Questions

- **SUMO `allway_stop` mechanics**: arrival ordering, tie-breaking, and creep behavior are not fully documented; the static matrix can't express arrival-order state. Read the source or experiment before the network-model ADR fixes 4-way-stop semantics.
- **Protobuf vs Go-native encoding** for the compiled network: benchmark at real network sizes; CommonRoad's 2024 protobuf move is the community-legible default.
- **Lane width model**: constant-per-lane (SUMO's 3.2 m default; changing widths requires splitting the edge) vs piecewise segments; OpenDRIVE's polynomial widths are the overkill pole.
- **Conflict-set memory footprint at city scale**: size the Go representation (bitsets vs sorted id lists) with a benchmark at engine bring-up.
- **Signal ↔ connection binding schema**: `tl`+`linkIndex` is the obvious template; ADR-0008 fixed the controller side (external signal role), but the binding's final shape belongs to the signal-control topic.
- **Frontage roads**: no counter-evidence that they need more than ordinary parallel edges + ramp junctions; verify with a real example during scenario authoring.
- **The network-model ADR itself** remains unwritten — this research is its gate.

## Related

- [Time Model](../architecture/time-model.md) — ADR-0005's fixed 100 ms tick is what walks this graph, in fixed iteration order for determinism.
- [State Authority](../architecture/state-authority.md) — world state = this graph plus the vehicles occupying it; the engine is its sole writer.
- [Traffic Flow Models (Microscopic)](../business-domains/traffic-flow-models.md) — IDM leader/follower queries run on per-lane sorted lists; MOBIL consumes connection adjacency.
- [OSM Extraction](../integrations/osm-extraction.md) — the ADR-0009 import pipeline that consumes the provenance and `guessed`-flag schema reserved here.
- [Scenario Format](../concepts/scenario-format.md) — the compiled network is a diffable scenario component; delta-patch variants build on it.
- [Signal Control](../business-domains/signal-control.md) — owns the final shape of the signal ↔ connection binding the graph reserves.

---
*Raw research: [raw/arch-road-graph-model/](../../raw/arch-road-graph-model/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
