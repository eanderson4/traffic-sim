# ADR-0003: MapLibre-first visualization, no UI frameworks by default

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

Primary visualization needs are GIS-shaped: OSM-derived road networks, congestion
heatmaps painted on road geometry, animated vehicle positions, interactive
inspection. Past experience on other projects: heavyweight frameworks (e.g. React
Three Fiber) were adopted and then stripped out weeks later. A 3D driver-view client
may exist someday but is not a current requirement.

## Decision

- **MapLibre GL JS** is the primary rendering layer, driven by **vanilla TypeScript**.
- No UI framework (React etc.) without a new ADR justifying it.
- **deck.gl** (framework-agnostic WebGL overlay for MapLibre) is the pre-approved
  escalation path *if and when* MapLibre-native layers can't handle the animated
  vehicle count — adopt based on measured need, not anticipation.

## Consequences

- OSM basemaps, camera controls, and data-driven road styling come for free.
- Visualizers consume the same NATS streams as controllers (ADR-0002), so a future
  Three.js driver-view client is an additive new consumer, not a rewrite.
- Escalation criteria to deck.gl to be documented in `integration-maplibre-realtime`
  research.
