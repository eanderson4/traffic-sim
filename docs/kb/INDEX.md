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
- [ ] [Simulator Landscape](raw/domain-simulator-landscape.md) — SUMO, MATSim, CARLA, VISSIM: architectures, what to steal, what to avoid
- [ ] [Traffic Flow Models (Microscopic)](raw/domain-traffic-flow-models.md) — car-following (IDM, Gipps), lane-changing (MOBIL), gap acceptance, intersection right-of-way
- [x] [Macroscopic Flow Models](raw/domain-macroscopic-flow-models/synthesis.md) — LWR kinematic-wave PDE, fundamental diagram, shockwaves/rarefactions, Cell Transmission Model; micro↔macro bridge and when macro beats micro
- [x] [Trajectory Datasets & Overhead Analysis](raw/domain-trajectory-datasets/synthesis.md) — real overhead traffic data (NGSIM, highD/inD/rounD drone sets, pNEUMA, I-24 MOTION, ring-road experiments); DIY capture; computing our own waves/FD from trajectories; sim validation targets for intersections and highways
- [ ] [Congestion Metrics](raw/domain-congestion-metrics.md) — level-of-service, delay, queue length, throughput, travel-time reliability; how traffic engineers rank alternatives
- [ ] [Signal Control](raw/domain-signal-control.md) — phases, fixed vs actuated timing, coordination/green waves; foundation for the civic-advocacy use case

### Architecture
- [x] [Time Model](raw/arch-time-model/synthesis.md) — tick authority vs discrete-event vs hybrid; game-server prior art; determinism/replay implications → feeds ADR-0005
- [ ] [NATS Backbone](raw/arch-nats-backbone.md) — subject taxonomy; core NATS vs JetStream vs KV division of labor; replay via streams; backpressure with many controllers
- [ ] [Road Graph Model](raw/arch-road-graph-model.md) — lane-level graph representation: nodes/edges/lanes/connections, turn restrictions; how SUMO/OSM represent this
- [ ] [State Authority](raw/arch-state-authority.md) — authoritative-server patterns from multiplayer games: interest management, prediction, late/dropped controller inputs

### Concepts
- [ ] [Vehicle & Controller Interface](raw/concept-vehicle-controller-interface.md) — vehicle capability model vs operator abstraction; the engine↔controller contract
- [ ] [Scenario Format](raw/concept-scenario-format.md) — scenario definition, variants/diffs, demand patterns, recording format for replay

### Integrations
- [ ] [OSM Extraction](raw/integration-osm-extraction.md) — OSM data model (ways, lane tags, turn restrictions); Overpass/osmnx/osm2streets tooling; geometry → lane-graph conversion
- [ ] [MapLibre Realtime Viz](raw/integration-maplibre-realtime.md) — live vehicle positions + congestion heatmaps in MapLibre; update strategies; deck.gl escalation criteria

### Decisions
- [x] [ADR-0001](decisions/ADR-0001-go-engine.md) — Go for the engine core, TypeScript for visualization
- [x] [ADR-0002](decisions/ADR-0002-nats-backbone.md) — NATS as the sole inter-service backbone
- [x] [ADR-0003](decisions/ADR-0003-maplibre-vis.md) — MapLibre-first visualization, no UI frameworks by default
- [x] [ADR-0004](decisions/ADR-0004-local-first.md) — Local-first deployment via docker-compose
- [ ] [ADR-0005](decisions/ADR-0005-time-model.md) — Time model (PROPOSED — pending `arch-time-model` research)

## Cross-Topic Concerns
(populated by /distill-kb after research)

## Summary
(populated by /distill-kb after research)
