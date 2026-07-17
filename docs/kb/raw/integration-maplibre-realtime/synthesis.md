# Synthesis: MapLibre Realtime Viz

> Researched: 2026-07-17 | Git HEAD: 6efd963 | Status: complete
> Feeds the escalation-criteria documentation ADR-0003 deferred to this topic,
> and constrains the message contracts of the future observability ADR
> ([[domain-congestion-metrics]]). This synthesis recommends; the ADRs decide.

## Summary

The question was how to render live vehicle positions (thousands at 10 Hz)
and congestion heatmaps (1 Hz, per road segment) in MapLibre GL JS, and when
plain MapLibre stops sufficing. The answer is unusually crisp: the wall is
not GPU rendering — it is JSON. MapLibre's GeoJSON pipeline re-stringifies,
re-parses, and re-tiles the *entire* source on every `setData`, which breaks
at animation rates in the low thousands of moving features (the documented
catastrophic case: ~900k coordinates at 5 Hz wedges the main thread
permanently). MapLibre's own escape hatches — `updateData` diffs (3.0.0+)
and feature-state — map 1:1 onto our spawn/move/despawn stream and our
metrics-on-static-geometry join, respectively. deck.gl's instanced layers fed
by typed-array attributes hold 60 FPS at ~1M items, which is why the
escalation trigger should be defined as *vehicle count × update rate through
the JSON path* — and why the NATS vehicle subject should be specified as
binary SoA frames from day one even if the first client is MapLibre-only.
ADR-0003's MapLibre-first stance survives contact with the evidence.

## Source Files

- [Mechanics: setData/updateData/feature-state, markers, custom layers, deck.gl, TripsLayer](./implementation.md)
- [Prior art survey: sumo-gui, OTFVis, VIA, A/B Street, kepler.gl, streetscape.gl/XVIZ, GTFS-RT deployments](./competitors.md)
- [Standards, formalisms, named patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions

### 1. Three data channels, three update mechanisms — never one GeoJSON blob
**Choice:** (a) **Road network**: load-once GeoJSON source (vector tiles if
it ever outgrows it) with `promoteId` on segment id, source `maxZoom` lowered
from the default 18, ~6-decimal coordinate precision. (b) **Congestion**:
`setFeatureState` per metrics snapshot (~1 Hz) keyed by segment id, with
`line-color` driven by `['interpolate', …, ['feature-state', 'speed']]`.
(c) **Vehicles**: a dedicated small GeoJSON source; each interpolated frame
applies a `updateData` diff (`add`/`update` geometry/`remove` by id), vehicle
id = feature id.
**Why:** Mapbox's official cost model — source-update time scales with
*layers-on-source × total vertices*, and "any update to it requires …
reprocess the entire set of data"; the sanctioned fix is "a separate GeoJSON
source for data that needs to be updated rapidly"
([performance guide](https://docs.mapbox.com/help/troubleshooting/mapbox-gl-js-performance/)).
feature-state exists precisely to "avoid re-parsing all the geometries at
each state change" (same guide);
[updateData](https://maplibre.org/maplibre-gl-js/docs/API/classes/GeoJSONSource/#updatedata)
exists precisely to skip the full re-stringify/re-parse
([PR #1605](https://github.com/maplibre/maplibre-gl-js/pull/1605) reports
11.5 ms → 1.8 ms per update in their example) and its
diff vocabulary is exactly our stream's spawn/move/despawn.
**Trade-off:** Three sources + client-side diff computation (snapshot N vs
N+1) to maintain; best-effort id semantics mean missing ids fail *silently*
(gray road, stale dot) — needs a contract test on id stability.
**Field context:** [implementation §1–§4](./implementation.md).

### 2. Client-side snapshot interpolation decouples 10 Hz network from 60 fps render
**Choice:** Vehicles render inside `requestAnimationFrame`, lerping between
the two latest snapshots behind the ~200–300 ms buffer [[arch-time-model]]
budgeted; `updateData` (or attribute writes, post-escalation) happens once
per rAF frame, never per incoming message.
**Why:** There is no safe fixed `setData` rate — the documented failures
scale with source size, not rate alone: 25 points at 20 Hz pegged a laptop
CPU ([SO 61264144](https://stackoverflow.org.cn/questions/61264144)); 20–60 Hz
symbol updates measurably degrade interaction
([discussion #6159](https://github.com/maplibre/maplibre-gl-js/discussions/6159)).
Interpolation is the standard multiplayer-game pattern (snapshot
interpolation, [standards](./standards-and-patterns.md)); OTFVis's ~30 fps
live cap is the same trade from the sim side
([competitors](./competitors.md)).
**Trade-off:** Viz lags the authoritative tick by the buffer length — fine
for observers, and human controllers already live with engine-side latency
per ADR-0005; extrapolation is rejected (replay must show what *was*
simulated).
**Field context:** [implementation §6](./implementation.md).

### 3. Congestion heatmap = data-driven line casing on the static network; not the `heatmap` layer, not `line-gradient`
**Choice:** The zoomed-in congestion view is a fat semi-transparent `line`
layer over the road source, colored by feature-state-joined Edie speed
([[domain-congestion-metrics]]) with `line-cap: round` at joints; the
zoomed-out overview may sample segment midpoints as weighted points into a
real `heatmap` layer (a density view, clearly labeled as such).
**Why:** The measure lives *on the geometry* (per-segment speed/density), so
it is categorical coloring, not KDE — the `heatmap` layer is point-density
only ("There's no way to make a 'line heatmap'",
[mapbox #10097](https://github.com/mapbox/mapbox-gl-js/issues/10097)) and
`line-gradient` has no data-driven styling and paints along one line's own
length ([style spec](https://maplibre.org/maplibre-style-spec/layers/)).
Using KDE on road congestion would invent smoothing artifacts and
misrepresent queue boundaries — a data-integrity problem. Join artifacts at
segment boundaries are the known cosmetic cost
([mapbox #8698](https://github.com/mapbox/mapbox-gl-js/issues/8698)).
**Trade-off:** Two heatmap implementations (casing + optional overview KDE)
and the `line-cap` overlap artifact; acceptable.
**Field context:** [implementation §7](./implementation.md),
[standards §KDE-vs-casing](./standards-and-patterns.md).

### 4. The deck.gl escalation ladder (the ADR-0003 deliverable)
**Choice:** Escalate on measured need, in this order:
- **Rung 0 — MapLibre-native as in decision 1.** Comfort zone: thousands of
  segments at 1 Hz; low thousands of vehicles at 10 Hz.
- **Rung 1 — measured stutter in `updateData` at 10 Hz** (frame drops in the
  viz microbenchmark at target fleet size): reduce rate, zoom-gate the
  vehicle layer (city zoom = heatmap only), or drop to a hand-rolled WebGL2
  custom layer for the vehicle pass — one instanced point/quad draw, zero
  new deps. Budget warning: `triggerRepaint` repaints the *whole* map — a
  60 fps/4% CPU standalone shader became 15 fps/40% CPU inside mapbox
  ([mapbox #8159](https://github.com/mapbox/mapbox-gl-js/issues/8159)) —
  and the basemap alone can't hold 60 fps fullscreen on most machines
  ([discussion #3590](https://github.com/maplibre/maplibre-gl-js/discussions/3590)).
- **Rung 2 — ≥ ~10k animated vehicles or picking/aggregation needs:**
  deck.gl `MapboxOverlay` (**overlaid** mode; interleaved only if vehicles
  must sit under labels or occlude in 3D), ScatterplotLayer/IconLayer with
  `dataComparator` + `updateTriggers` discipline. Documented comfort zone:
  **~1M instanced items at 60 FPS**
  ([deck.gl performance](https://deck.gl/docs/developer-guide/performance)).
  Naive deck.gl is *also* slow — "stutter … even for layers with just a few
  thousand items" if the `data` prop churns — so the binary-attribute
  discipline is part of the adoption, not an optimization.
- **Rung 3 — full binary pipeline:** web worker packs NATS SoA frames
  straight into Float32Array attributes (Transferables, zero-copy),
  `_dataDiff` for partial updates, GPU attribute transitions interpolating
  between snapshots ("1M point cloud … 6M float32 … without leaving the GPU
  memory", [animations docs](https://deck.gl/docs/developer-guide/animations-and-transitions)).
**Why:** This is exactly ADR-0003's "adopt based on measured need, not
anticipation", now with the rungs and numbers the ADR asked this topic to
document. The crossover evidence: MapLibre's documented failure at ~900k
coordinates/5 Hz ([maplibre #106](https://github.com/maplibre/maplibre-gl-js/issues/106))
vs deck.gl's documented 1M@60FPS — comparable item counts at 12× the rate,
with deck.gl's degraded zone at ~11× the items: roughly one to two orders of
magnitude of headroom at the same task shape.
**Trade-off:** deck.gl adds a large dependency and a second rendering model;
picking must be re-implemented (MapLibre's `queryRenderedFeatures` is lost
for deck layers); spawn/despawn breaks GPU transitions ("objects are
identified by their index … if objects are inserted or removed, the
transition will not look as expected") — stable fleet ordering required.
**Field context:** [implementation §5, §8, §9](./implementation.md).

### 5. Wire format: binary SoA on the vehicle subject from day one; GeoJSON is client-local
**Choice:** The NATS vehicle snapshot subject carries binary
structure-of-arrays frames (ids + Float32 x/y + angle + class), not GeoJSON;
the MapLibre client converts to diffs (cheap at low thousands), the future
deck.gl client consumes near-zero-copy. Road network and metrics stay
id-keyed JSON (rates are low).
**Why:** GeoJSON-on-the-wire forces the expensive JSON parse *before* any
rendering choice; SoA makes the cheapest consumer free and the MapLibre
consumer pay one `map()`. deck.gl quantifies the JSON tax: 1.57M vertices
parsed in 4261 ms binary vs 9202 ms non-binary
([v8.4 notes](https://deck.gl/docs/whats-new)); XVIZ independently chose
JSON-or-binary-GLB over sockets for the same reason
([XVIZ concepts](https://github.com/uber/xviz/blob/master/docs/overview/concepts.md)).
This slots into [[arch-nats-backbone]]'s contract decision (WebSocket binary
frames, AsyncAPI-declared) without changing its taxonomy.
**Trade-off:** Hand-rolled framing (or a schema tool — flatbuffers/protobuf
decision deferred to the contract ADR); AsyncAPI documents binary payloads
less ergonomically than JSON.
**Field context:** [implementation §12](./implementation.md),
[standards §SoA](./standards-and-patterns.md).

### 6. Replay mode reuses the live path; TripsLayer/kepler UX for scrubbing
**Choice:** Replaying a recorded run = the same vehicle source driven by the
replayer's tick pacing ([[arch-nats-backbone]] decision 4); for whole-run
overview, build per-trip waypoint arrays and use TripsLayer (timestamps
rebased — "raw unix epoch time can not be used", float32) with a kepler.gl
Trip-layer-style scrubber (trail length, speed control) — the UX to steal,
not the React+Redux dependency.
**Why:** kepler.gl's Trip layer is the productized reference
([docs](https://docs.kepler.gl/docs/user-guides/c-types-of-layers/k-trip));
TripsLayer animates via a scalar `currentTime` with shader-side reveal —
no per-frame attribute regen. Both assume *complete* trips (replay), which
matches JetStream playback; live tails stay on the decision-1 path.
**Trade-off:** Two replay presentations (live-path re-simulation vs
trip-overview); fine — they answer different questions (verify vs show).
**Field context:** [implementation §10](./implementation.md).

### 7. Version pinning and test posture
**Choice:** Pin MapLibre to a CI-verified 5.x (≥5.21.1, verified against
our call pattern); the viz microbenchmark (fleet size ×
rate grid over `updateData` + feature-state) runs in CI and is the input to
any rung transition in decision 4. WebGL2-only is fine (MapLibre v3+,
deck.gl interleaved requirement, MapLibre v6 prereleases drop WebGL1).
**Why:** `updateData` had a 5.20 regression tail — 5.20.0 ignored property
updates via string promoteId
([#7315](https://github.com/maplibre/maplibre-gl-js/issues/7315)), ≥5.20 diff
updates not rendering
([#7257](https://github.com/maplibre/maplibre-gl-js/issues/7257)); both were
closed-as-completed in March 2026 and fixed in 5.20.2 / 5.21.1 — but the API
is young enough that it remains our load-bearing surface to guard with a
benchmark. There are *no published benchmarks* of feature-state
or updateData throughput at our shape; we must produce our own.
**Trade-off:** Upgrade cadence gated on a benchmark suite; acceptable.
**Field context:** [implementation §11](./implementation.md),
[standards §gaps](./standards-and-patterns.md).

## Compare/Contrast: Our Approach vs the Field

| Dimension | sumo-gui | OTFVis | VIA | A/B Street | kepler.gl | streetscape.gl/XVIZ | us (recommended) |
|---|---|---|---|---|---|---|---|
| Moving-agent pipeline | in-proc GL canvas | byte-buffer + quadtree → JOGL | file → desktop GL | in-proc glium | file → deck.gl | socket → deck.gl | **NATS WS → updateData → (escalate) instanced attrs** |
| Basemap | none | layers | backgrounds | own/shim | Mapbox | Mapbox | **MapLibre-native OSM** |
| Congestion coloring | ~40 lane schemes | none | link aggregation | some | ramps | n/a | **feature-state line casing from Edie metrics** |
| Live rate | real-time stepping | ~30 fps cap | post-hoc | real-time | post-hoc | real-time (dead) | **10 Hz snapshots + client lerp** |
| Scale ceiling | OSM-net slowdowns | "millions" claim | large scenarios | city-scale | millions of points | "100k+ geometries" | **~1M @60fps ceiling (deck.gl rung)** |
| Framework deps | FOX | JOGL | Java | Rust/wasm | React+Redux | React | **vanilla TS (ADR-0003)** |
| Maintained | yes | yes | yes (commercial) | yes | yes | **no (~2022)** | — |

## The Genuine Gap

[[arch-nats-backbone]] found nobody has published a NATS-backed authoritative
sim with deterministic replay; this topic finds the client-side twin: **no
published benchmarks or reference clients exist for streaming authoritative
simulation state at 10 Hz into a web GIS renderer.** Every MapLibre update
number in this research comes from issue reports, not controlled
measurements; feature-state throughput at 10k features/tick is undocumented;
GTFS-RT deployments update every ~15–60 s (150–600× slower than our 10 Hz); and the one system
built for exactly this shape (streetscape.gl/XVIZ) is unmaintained. Our viz
microbenchmark (MapLibre 5.x, `updateData` vs feature-state vs deck.gl
attributes, 1k→100k vehicles × 1/10/30 Hz) and the escalation-criteria
write-up are, again, near the frontier — and are cheap to run *before*
locking decision 4's rung thresholds.

## Open Questions

- Exact fleet ceiling of `updateData` at 10 Hz (1k/5k/10k/50k vehicles ×
  1/10/30 Hz grid) — microbenchmark during viz bring-up; determines rung-1
  trigger numerically.
- feature-state throughput at ~5–10k segments × 1 Hz — same benchmark; if it
  stutters, fall back to the split-overlay-source pattern
  ([standards](./standards-and-patterns.md) anti-patterns).
- Vehicle picking (click → details) after deck.gl escalation: deck.gl picking
  (16M items/layer) vs maintaining a parallel invisible MapLibre source —
  decide when rung 2 is reached.
- Interpolation buffer length for the *multiplayer* use case: does the viz
  buffer add perceptibly to human-controller loop latency, or does the
  controller path bypass interpolation entirely? (with [[arch-state-authority]])
- Binary framing tool for the SoA vehicle subject: hand-rolled header +
  typed arrays vs flatbuffers/protobuf — deferred to the message-contract ADR
  ([[arch-nats-backbone]] open item).
- Zoom-gating policy: at what zoom do vehicles cede to pure heatmap? UX +
  benchmark decision; affects how big a fleet rung 0 actually serves.
- City-scale static network: GeoJSON load-once vs MVT/MLT vector tiles —
  depends on [[integration-osm-extraction]] region sizes; GeoJSON first.

## Connections to Other Topics

- **Decides:** the escalation criteria ADR-0003 explicitly deferred to this
  topic ("Escalation criteria to deck.gl to be documented in
  `integration-maplibre-realtime` research") — decision 4.
- **Honors constraints from:** ADR-0003 (MapLibre-first, vanilla TS, no
  framework — kepler.gl/streetscape.gl surveyed and disqualified as
  dependencies; deck.gl kept framework-agnostic), [[arch-time-model]] /
  ADR-0005 (viz lags the authoritative tick by the interpolation buffer;
  replay shows simulated, not extrapolated, state).
- **Constrains:** [[arch-nats-backbone]] (vehicle subject = binary SoA frames
  at 10 Hz, ids in payload, one multiplexed subject — never subject-per-
  vehicle; WebSocket binary confirmed sufficient shape), the observability
  ADR from [[domain-congestion-metrics]] (heatmap input = per-segment Edie
  speed/density keyed by segment id at ~1 Hz; metric event ids must be
  stable road-graph ids end-to-end or the feature-state join silently
  breaks), [[concept-scenario-format]] (replay recordings must support
  building per-trip arrays for scrubber mode).
- **Depends on:** [[arch-road-graph-model]] (stable segment ids + geometry
  precision for the static source), [[arch-state-authority]] (future
  area-of-interest filtering of the snapshot subject; controller-latency
  question), [[integration-osm-extraction]] (region size → static source
  format).
- **Relates to:** [[domain-simulator-landscape]] (sumo-gui/VIA/OTFVis as the
  incumbent viz practice we're diverging from), [[domain-congestion-metrics]]
  (the numbers the heatmap shows), [[concept-vehicle-controller-interface]]
  (vehicle class/heading fields the marker styling consumes).
