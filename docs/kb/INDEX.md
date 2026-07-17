# Knowledge Base: traffic-sim

> NATS-based traffic simulation engine — real road networks, heterogeneous
> vehicle controllers (AI and human), decision-grade congestion metrics.

## How to Use This KB

This knowledge base is organized as independently researched topics.
- **Raw research** (`raw/`) contains deep, source-attributed findings
- **Articles** (`articles/`) contain distilled, cross-referenced knowledge
- **Decision records** (`decisions/`) capture consequential choices and rationale
- Use `/research-topic [name]` to research individual topics
- Use `/research-topic --all` to research all pending topics
- Use `/distill-kb` to synthesize articles from raw research
- Use `/update-kb` to check freshness and refresh stale topics

The founding document is [`docs/VISION.md`](../VISION.md) — every topic here traces
back to it.

## Topic Registry

### Business Domains
- [x] [Simulator Landscape](raw/domain-simulator-landscape/synthesis.md) — SUMO, MATSim, CARLA, VISSIM: five camps, licensing map, our niche confirmed; recommends MIT/Apache-2.0 license ADR
- [x] [Traffic Flow Models (Microscopic)](raw/domain-traffic-flow-models/synthesis.md) — car-following (IDM, Gipps), lane-changing (MOBIL), gap acceptance, intersection right-of-way; validates the 100 ms tick + ballistic integrator
- [x] [Macroscopic Flow Models](raw/domain-macroscopic-flow-models/synthesis.md) — LWR kinematic-wave PDE, fundamental diagram, shockwaves/rarefactions, Cell Transmission Model; micro↔macro bridge and when macro beats micro
- [x] [Trajectory Datasets & Overhead Analysis](raw/domain-trajectory-datasets/synthesis.md) — real overhead traffic data (NGSIM, highD/inD/rounD drone sets, pNEUMA, I-24 MOTION, ring-road experiments); DIY capture; computing our own waves/FD from trajectories; sim validation targets for intersections and highways
- [x] [Congestion Metrics](raw/domain-congestion-metrics/synthesis.md) — FHWA MOE set + HCM Ch.24 trajectory state machines; recommends trajectory-first metric kernel, LOS as presentation skin, CRN experiment protocol
- [x] [Signal Control](raw/domain-signal-control/synthesis.md) — NEMA dual-ring as internal model; decisecond cabinet timing maps losslessly onto the 100 ms tick; phase changes are engine events

### Architecture
- [x] [Time Model](raw/arch-time-model/synthesis.md) — tick authority vs discrete-event vs hybrid; game-server prior art; determinism/replay implications → feeds ADR-0005
- [x] [NATS Backbone](raw/arch-nats-backbone/synthesis.md) — three-plane split (core live / JetStream record / KV config), OCC headers, subject taxonomy → feeds ADR-0006
- [x] [Road Graph Model](raw/arch-road-graph-model/synthesis.md) — lane-as-atom, compiled junction conflict sets + internal lanes, (laneId, s) occupancy, geometry-by-reference
- [ ] [State Authority](raw/arch-state-authority.md) — authoritative-server patterns from multiplayer games: interest management, prediction, late/dropped controller inputs

### Concepts
- [x] [Vehicle & Controller Interface](raw/concept-vehicle-controller-interface/synthesis.md) — 4-axis intent vocabulary, SMARTS-style attach handshake, exclusive per-vehicle claims, fallback-to-IDM on disconnect
- [x] [Scenario Format](raw/concept-scenario-format/synthesis.md) — manifest-of-parts directory, strict YAML, kustomize-style overlay variants, metrics embedded in scenario

### Integrations
- [ ] [OSM Extraction](raw/integration-osm-extraction.md) — OSM data model (ways, lane tags, turn restrictions); Overpass/osmnx/osm2streets tooling; geometry → lane-graph conversion
- [ ] [MapLibre Realtime Viz](raw/integration-maplibre-realtime.md) — live vehicle positions + congestion heatmaps in MapLibre; update strategies; deck.gl escalation criteria

### Decisions
- [x] [ADR-0001](decisions/ADR-0001-go-engine.md) — Go for the engine core, TypeScript for visualization
- [x] [ADR-0002](decisions/ADR-0002-nats-backbone.md) — NATS as the sole inter-service backbone
- [x] [ADR-0003](decisions/ADR-0003-maplibre-vis.md) — MapLibre-first visualization, no UI frameworks by default
- [x] [ADR-0004](decisions/ADR-0004-local-first.md) — Local-first deployment via docker-compose
- [x] [ADR-0005](decisions/ADR-0005-time-model.md) — Hybrid time model: fixed-tick authoritative core, event-driven edges, tick-count clock, JetStream snapshot+intent-log replay

## Cross-Topic Concerns
(populated by /distill-kb after research)

## Summary
(populated by /distill-kb after research)
