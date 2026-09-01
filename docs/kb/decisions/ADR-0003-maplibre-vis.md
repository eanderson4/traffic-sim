# ADR-0003: MapLibre-first visualization, no UI frameworks by default

- **Status:** Accepted (hero-track exception added 2026-08-29 — see addendum)
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

## Addendum 2026-08-29: 3D "hero track" exception — three.js for standalone baked-replay pages

The signal-timing game (docs/show/signal-game-plan-2026-08-29.md, workstream B)
needs a 3D reveal surface for the recording. The original decision already
contemplated this ("a future Three.js driver-view client is an additive new
consumer, not a rewrite"); this addendum makes it concrete and bounded.

**Decision.** `three` (three.js, WebGL) is approved as a direct viz dependency
for **standalone 3D pages that render baked replays and quiz content only** —
initially `hero.html` (3D baked-replay viewer) and `fleet-review.html` (vehicle-
model review harness). These pages:

- read only static baked artifacts over HTTP (ADR-0023 index.json + TSRB/TSSG
  chunks + network GeoJSON) — **no live NATS data**;
- use the network's local metric frame directly — **no basemap, no map
  projection**;
- load no MapLibre code; MapLibre pages load no three.js code (page-isolated
  stacks, vite multi-page inputs).

**Boundaries.** MapLibre remains the live and analytical renderer; the map
ladder and its deck.gl escalation path are unchanged and still govern the map's
vehicle channel. This is the **hero track**, explicitly NOT a "rung 2" of the
map ladder — rung 2 there remains the deferred deck.gl `MapboxOverlay` rung,
and the research registry's "rung-2" label for this page forks that meaning
(`docs/kb/raw/integration-3d-hero-viz/synthesis.md` D7). Extending three.js to
live data, basemaps, or the main app page requires a further addendum. The
visual target is stylized legibility (flat shading, theme palette), inside
VISION.md's non-goal of *photorealistic* 3D. Vehicle models are code modules
under the contract in `viz/src/fleet3d/SPEC.md`.

**Dependency justification** (AGENTS.md conventions, ADR-0023 §7 pmtiles
precedent): `three` 0.185.1 is the ecosystem-default WebGL library for this
exact shape — a library, not a UI framework, so the no-framework rule stands;
~0.17 MB gzip confined to the hero pages' bundles.
