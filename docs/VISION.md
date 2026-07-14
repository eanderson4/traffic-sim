# Traffic Sim — Vision & Guiding Document

> This is the founding document for the project, distilled from the original planning
> session (2026-07-14). It is the root of the knowledge base: every KB topic, ADR, and
> convention should trace back to something here. When vision changes, update this doc
> and record the change as a decision record.

## Mission

Build a standalone, open-source traffic simulation engine that can model real road
networks, host heterogeneous vehicle controllers (AI and human), and produce
decision-grade observability — congestion metrics, comparisons between infrastructure
alternatives, and replayable scenarios.

It must be a *real* piece of software engineering: solid architecture, documented
decisions, and the ability to scale from a single 4-way stop demo to replaying a week
of city traffic.

## Use Cases (in priority order)

### 1. Traffic Planner Game (math-vs-vibes episode)
Present a road situation with a congestion problem and a few candidate upgrades
(add a lane, convert stop sign → roundabout, retime a light). The guest guesses which
upgrade best improves a metric; the sim reveals the ranked answer. Requirements:
- Author scenarios with deliberate congestion issues
- Run baseline + N variants, compare metrics
- Visualize congestion compellingly (road heatmaps, animated vehicles)
- Borrow visual polish from `../math-vs-vibes/design-system`

### 2. Real-World Network Import
Extract small regions from OpenStreetMap, load them as sim road networks, establish a
traffic baseline, and evaluate upgrade paths on real geometry.

### 3. Multiplayer Chaos Demo
Many humans join a live sim as drivers via the message bus, alongside AI traffic.
A capability showcase and a forcing function for the architecture: human input is just
another controller emitting events.

### 4. Civic Advocacy (long-term)
Model a real intersection/corridor, replay a day/week of realistic traffic with and
without a signal-timing change, and present the resulting stats to local traffic
coordinators. Requires deterministic replay and trustworthy metrics.

## Core Concepts

- **Engine** — the authoritative simulation core. Owns world state, advances time,
  resolves physics/conflicts, publishes state. Loadable road networks.
- **Road Network** — a lane-level directed graph. Nodes (intersections/junctions),
  edges (road segments), lanes within edges, and lane connections (turns, merges).
  We simulate lane changes and multi-lane roads; we do NOT simulate continuous
  within-lane swerving. Intersection types are first-class: 4-way stop, signalized,
  roundabout, yield, uncontrolled, highway sections, frontage roads, on/off ramps.
- **Vehicle** — an interface, not a class hierarchy. Different vehicle types expose
  different capabilities (size, acceleration profile, sensors).
- **Controller / Operator** — the brain responsible for one or more vehicles. AI
  policies of varying sophistication, scripted behaviors, or a human with real-time
  controls. Controllers consume engine state and emit intent/control events. The
  engine treats all controllers uniformly — this is *the* central interface.
- **Scenario** — a road network + demand pattern (vehicle spawns/routes) + control
  configuration (signal timings etc.) + metric definitions. Scenarios are diffable so
  "upgrade variants" are first-class.
- **Observability** — metrics as a core subsystem, not an afterthought: throughput,
  delay, queue lengths, travel times, level-of-service — per lane, per intersection,
  per scenario. Everything needed to rank upgrade alternatives and render heatmaps.

## Architecture Principles

1. **NATS is the backbone.** Core NATS for real-time state/control fan-out, JetStream
   for durable event logs (replay!), KV for shared state/config. Controllers and
   visualizers are just NATS clients. This is what lets humans, AIs, and observers
   plug in uniformly and lets the system scale out later.
2. **Authoritative engine, event-driven edges.** Leading hypothesis: the engine runs a
   fixed tick as the single source of truth; controllers emit events asynchronously.
   Final time model is an open research topic (see KB) — decide via ADR after research.
3. **Microservice-shaped, engine-swappable.** Clean service boundaries and message
   contracts so the engine core can be rewritten (Go → Rust, or optimized) without
   touching controllers or visualization.
4. **Go for the engine, TypeScript for visualization/web clients.** Go for the NATS
   story (and open-source credibility with that community); TS where the browser lives.
5. **MapLibre-first visualization.** GIS-native rendering: OSM basemaps, congestion
   heatmaps on road geometry, animated vehicles. deck.gl is the known upgrade path for
   very large animated fleets — adopt only when needed. No frameworks we don't need.
6. **Local-first.** docker-compose for NATS; engine and clients run locally. Nothing
   in the design may preclude hosted deployment later.
7. **Determinism where it counts.** Replay of recorded scenarios must be trustworthy
   enough to support the advocacy use case.

## Engineering Conventions (seed)

- Knowledge base lives in `docs/kb/` — research topics, distilled articles, and
  decision records. See `docs/kb/INDEX.md`.
- Decisions of consequence get an ADR in `docs/kb/decisions/`.
- Agents and humans both follow `AGENTS.md`: consult the KB before designing,
  update it when reality changes.

## Non-Goals (for now)

- Photorealistic 3D or driving-game physics (lane-level fidelity is the bar)
- Continuous within-lane vehicle dynamics (swerving)
- Cloud-scale deployment for the episode
- Pedestrians, cyclists, transit (future candidates, keep the door open)
