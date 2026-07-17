# Mechanics: MapLibre Realtime Viz

> Source: web research (greenfield — no viz client exists; this file collects the
> *mechanisms* the realtime client is built from — MapLibre GL JS source/update
> machinery, then the deck.gl escalation machinery — to be re-audited against a
> real client once ADR-0003's escalation criteria are benchmarked)
> | Researched: 2026-07-17 | Git HEAD: 6efd963

## 1. The rendering pipeline: where realtime data enters

MapLibre GL JS renders from *sources* (GeoJSON, vector tiles, raster, image)
through *layers* (style rules referencing a source). A GeoJSON source is not a
draw list: on every data change it re-parses the data in a web worker and
rebuilds a tile index before any layer sees it. From the
[GeoJSONSource API docs](https://maplibre.org/maplibre-gl-js/docs/API/classes/GeoJSONSource/):

- `_updateWorkerData` is "Responsible for invoking WorkerSource's
  `geojson.loadData` target, which handles loading the geojson data and
  preparing to serve it up as tiles, **using geojson-vt or supercluster as
  appropriate**." Plain sources index with geojson-vt; `cluster: true` sources
  use supercluster (MapLibre's fork `@maplibre/geojson-vt` hosts both index
  types).
- `setData(data: string | GeoJSON)` — "Sets the GeoJSON data and re-renders
  the map… The latter [a URL] is preferable in the case of large GeoJSON
  files." There is **no typed-array or binary input path**: a GeoJSON object or
  a URL, nothing else. (Binary formats exist only for vector sources — MVT,
  and MLT via `encoding: 'mlt'` since MapLibre 5.12.0, per the MapLibre
  CHANGELOG.)
- Source defaults matter at our scale: GeoJSON sources re-tile to
  `maxzoom` **18** by default, `tolerance` 0.375, `buffer` 128
  ([style spec sources](https://maplibre.org/maplibre-style-spec/sources/)).
  Every `setData` redoes that work.

Mapbox's official performance model
([Mapbox GL JS performance guide](https://docs.mapbox.com/help/troubleshooting/mapbox-gl-js-performance/))
makes the cost structure explicit — MapLibre inherited this architecture
whole, and the guidance is cited by MapLibre users as applying unchanged:

> "render time = constant time + [number of sources × per source time] +
> [number of layers × per layer time] + [number of vertices × per vertex
> time]" … "source update time … is a function of the number of layers that
> use the updated source and the number of vertices in the features it
> contains." … "**Use a separate GeoJSON source for data that needs to be
> updated rapidly.** When using a GeoJSON source, any update to it requires
> Mapbox GL JS to reprocess the entire set of data."

That last sentence is the architectural rule the whole realtime design hangs
on: **fast-moving data gets its own small source; never update a big static
source frequently.**

**Analysis:** the pipeline explains every performance pathology below. Our
three data channels (static road network, 1 Hz congestion metrics, 10 Hz
vehicle positions) have wildly different update rates, so they belong in
different sources with different update mechanisms — this is not an
optimization, it is the documented cost model.

## 2. `setData` cost: the wall, with numbers

`setData` replaces and re-indexes the *entire* dataset per call, however small
the change. Measured failures, oldest to newest:

- [mapbox-gl-js issue #1391](https://github.com/mapbox/mapbox-gl-js/issues/1391)
  (2015, by Mapbox's own peterqliu) — "setData() and smart diffing":
  > "Regardless of how new data differs from the old, this involves a full
  > rerender … 1. This is slow. 2. Makes continuity difficult. **If we want to
  > show a vehicle marker moving down the street, it involves creating and
  > destroying markers in a repeated fashion**, rather than animating one
  > marker to new positions."

  Vehicle animation was the motivating broken use case *nine years ago*.
- [maplibre-gl-js issue #106](https://github.com/maplibre/maplibre-gl-js/issues/106)
  (2021) — "GeoJSON Source `setData` can take surprisingly long". The
  canonical catastrophic measurement. ~200 LineStrings × ~4,500 coordinates
  each (~900k coordinates), `setData` 5×/second:
  > "When passing a GeoJSON object to `setData`, it uses `JSON.stringify` on
  > the main thread to send data to a worker … The `JSON.stringify` for said
  > data will take about **200ms** each time. We experimentally did
  > `JSON.parse` on that string … and assume that it also takes about
  > **200ms** … At 5 updates per second, it's impossible for the worker to
  > catch up, and the main thread blocks forever. The website becomes
  > unresponsive."

  The same thread floats the fixes that later shipped: GeoJSON diffs,
  binary/memory-optimized coordinates, per-worker isolation.
- Even *tiny* data at moderate rates hurts:
  [Stack Overflow 61264144](https://stackoverflow.org.cn/questions/61264144) —
  `setData` every 50 ms (20 Hz) with **only 25 points** pushed a laptop to
  200% CPU vs 25% idle.
- [maplibre-gl-js discussion #6159](https://github.com/maplibre/maplibre-gl-js/discussions/6159) —
  symbol layers refreshed at 20–60 Hz via `setData` + `setLayoutProperty`:
  "map user interaction start getting sluggish … Can I rely on maplibre's
  rendering engine for such high refresh rates, or is it better to bypass
  maplibre and implement that functionality in the WebGL context directly?"
- Basemap cost is not free either:
  [discussion #3590](https://github.com/maplibre/maplibre-gl-js/discussions/3590) —
  a team doing 60 Hz animation measured "**10 sources are insanely slow, with
  each GeoJSON source occasionally taking up about 1 millisecond**
  (`coveringTiles` shows up in profiler)" — 10 ms of a 16 ms frame budget —
  plus `updatePlacement` (CPU label placement) as another bottleneck. Verdict
  from the field: "Avoiding large GeoJSON sources and reducing the
  source-count. Each source has high overhead during rendering."

Secondary pathologies: symbol/label flicker on frequent setData
([mapbox #5716](https://github.com/mapbox/mapbox-gl-js/issues/5716),
[#6652](https://github.com/mapbox/mapbox-gl-js/issues/6652)); progressive lag
on ever-growing geometry ([mapbox #7624](https://github.com/mapbox/mapbox-gl-js/issues/7624));
and last-call-wins coalescing — if a worker update is in flight, pending
updates are overwritten (intermediate states silently dropped; cancellation
added in MapLibre 2.2.0-pre.1 via PR #1102 caused its own "stuck source"
regression, [issue #1693](https://github.com/maplibre/maplibre-gl-js/issues/1693)).

**Analysis:** two distinct costs are being paid per `setData` — main-thread
JSON serialization and worker re-tiling — and both scale with *total source
size*, not delta size. For us: 10k vehicles × ~20 numbers each is small in
JSON terms (~a few hundred KB), so 10 Hz `setData` on a *vehicles-only* source
is plausibly survivable; but there is no published benchmark at exactly that
shape, and the 20–60 Hz reports (#6159) say per-frame full updates are
already past the comfort zone. This is precisely what `updateData` was built
to fix.

## 3. `updateData`: differential GeoJSON updates (MapLibre 3.0.0+)

- Origin: [issue #1236 "Partial updates for GeoJSON sources"](https://github.com/maplibre/maplibre-gl-js/issues/1236)
  (2022), motivated by *medium-to-high-scale animated GeoJSON* — "A good
  example of this data is https://opensky-network.org/ (Live airplane
  tracking)". Names the three bottlenecks verbatim: sending the entire
  GeoJSON string on every update; reprojecting/simplifying/retiling every
  feature when only a subset changed; invalidating all tiles when only a
  subset changed.
- Shipped in **MapLibre 3.0.0** (May 2023) via
  [PR #1605](https://github.com/maplibre/maplibre-gl-js/pull/1605): "Add the
  ability to send partial updates to a geojson source … useful for large
  sources that get frequent small updates … we can save a dramatic amount
  of time updating the source" (their example: 11.5 ms → 1.8 ms per update,
  mostly skipped JSON.stringify/parse). Perf follow-through in 5.11.0 (#6562, small
  diffs), 5.13.0 (#6668, plus a `waitForCompletion` option, #6688), 5.14.0
  (#6738 "Make GeoJSONSource#setData faster", #6772) — all MapLibre
  CHANGELOG.
- Contract
  ([GeoJSONSource#updateData](https://maplibre.org/maplibre-gl-js/docs/API/classes/GeoJSONSource/#updatedata)):
  "For sources with lots of features, this method can be used to make updates
  more quickly. **This approach requires unique IDs for every feature in the
  source**" — via feature `id` or `promoteId`. "It is an error to call
  updateData on a source that did not have unique IDs … Updates are applied
  on a best-effort basis; updating an ID that does not exist will not result
  in an error." Diff shape: `{removeAll?, removed?: id[], add?: Feature[],
  update?: [{id, newGeometry?, removeAllProperties?, removeProperties?,
  addOrUpdateProperties?}]}` — i.e. exactly the spawn/move/despawn triple a
  sim emits. Semantics gotchas from
  [discussion #5565](https://github.com/maplibre/maplibre-gl-js/discussions/5565):
  `update` will not create missing features; `removeAll` overrides the other
  fields.
- **Regression tail (matters for version pinning):**
  [#6730](https://github.com/maplibre/maplibre-gl-js/issues/6730) — undefined
  property values threw (internal MVT encoding forbids undefined), fixed in
  5.15.0; [#7315](https://github.com/maplibre/maplibre-gl-js/issues/7315) —
  5.20.0+ "updateData() ignores feature property updates when using (string)
  promoteId … Workaround: downgrade to 5.19.0, or reference features via
  their native `id`";
  [#7257](https://github.com/maplibre/maplibre-gl-js/issues/7257) —
  "MapLibre >= 5.20 Doesn't Update GeoJSON layers using Diff Update".
  Both 5.20 regressions were closed-as-completed in March 2026 (#7257 fixed
  in 5.20.2, #7315 fixed via #7320 in 5.21.1) — pin ≥5.21.1 rather than
  following the issue-era downgrade workaround.
- Mapbox's fork has a *different* API for the same idea: `dynamic: true` on
  the source (mapbox-gl-js 3.4.0, May 2024), and it throws with
  `cluster: true` ([mapbox #13245](https://github.com/mapbox/mapbox-gl-js/issues/13245)).
  In current MapLibre the diff path is plumbed into the clustered index too
  (`ClusterTileIndex` implements `updateIndex`), but cluster+diff is a
  verify-before-you-rely area.

**Analysis:** `updateData` is the difference between "10 Hz vehicles work" and
"10 Hz vehicles melt the main thread", and its diff vocabulary (add / update
geometry / update properties / remove by id) maps 1:1 onto our snapshot
stream (spawn / move / metric-change / despawn, vehicle id = feature id). The
5.20 regressions say: pin a verified release (≥5.21.1)
and keep a microbenchmark in CI, because this API is young enough to
break between minors.

## 4. feature-state: restyle without re-parse

- Semantics ([Map#setFeatureState](https://maplibre.org/maplibre-gl-js/docs/API/classes/Map/#setfeaturestate)):
  per-feature runtime key-value state, merged into the feature, keyed by
  `feature.id` (supplied in data, via `promoteId`, or auto via `generateId`).
  "If you change feature data using `setData(..)`, you may need to re-apply
  state taking into account updated `id` values." Appeared in mapbox-gl-js
  0.46.0 (June 2018), inherited by MapLibre; `promoteId` is documented in
  the style spec as "A property to use as a feature id (for feature state)".
- Why it's fast — the official statement
  ([Mapbox performance guide](https://docs.mapbox.com/help/troubleshooting/mapbox-gl-js-performance/)):
  > "Updating data is costly and can negatively impact performance. The
  > `feature-state` expression allows you to insert new data into a feature
  > at runtime … use the `map.setFeatureState` method to **avoid re-parsing
  > all the geometries at each state change**."

- Constraints that shape our design:
  - Only paint properties marked "Supports feature-state" (`line-color`,
    `line-width`, `line-opacity`, `circle-color`, `circle-radius`,
    `fill-color`, `fill-opacity` all do —
    [style spec layers](https://maplibre.org/maplibre-style-spec/layers/)).
  - **Not usable in `filter` expressions** ("The `feature-state` expression
    is not supported in filter expressions") — it can restyle but never
    hide/show, and it can never change geometry. It cannot move a vehicle.
  - The sanctioned large-scale use is exactly ours: Mapbox's
    [Data Joins guide](https://labs.mapbox.com/education/impact-tools/data-joins/)
    joins an external data stream onto static vector tiles via `promoteId` +
    per-feature `setFeatureState`.
- No documented maximum state count and no published benchmark at 10k+
  features/second — an evidence gap (see synthesis). Known bug class:
  [#4499](https://github.com/maplibre/maplibre-gl-js/issues/4499) —
  "setFeatureState changes type of properties from feature."

**Analysis:** feature-state is the designed channel for our **1 Hz congestion
coloring**: the road network geometry loads once, and each metrics snapshot
becomes N `setFeatureState` calls keyed by segment id, with `line-color`
driven by `['interpolate', …, ['feature-state', 'speed']]`. It is *not* a
vehicle-position channel (can't move geometry) and it must be re-applied if
the underlying source is ever `setData`'d — so the road source must be
load-once (or reload-only-on-scenario-change), which fits
[[arch-road-graph-model]]'s geometry/topology separation.

## 5. Moving thousands of vehicles: the three strategies, with ceilings

**(a) One `maplibregl.Marker` per vehicle — DOM, disqualified early.**
[Mapbox's markers guide](https://docs.mapbox.com/mapbox-gl-js/guides/add-your-data/markers/):
"Markers create DOM elements, so performance may degrade with large numbers
of markers. **For scenarios with 100+ markers, consider: Using Style
Layers**…". Measured: [react-map-gl #750](https://github.com/visgl/react-map-gl/issues/750) —
"more than 200 markers … the map start moving very slow when dragging."
DOM markers also always render above GL layers and are re-positioned by the
main thread every camera frame. Ceiling: hundreds, not thousands.

**(b) GeoJSON source + circle/symbol layer — GPU, the MapLibre-native
default.** One draw call per tile per layer; data-driven styling gives
`icon-rotate` (heading), `circle-color`/`circle-radius` (speed/class) for
free. Static ceiling is enormous (the geojson-vt demo slices a 100 MB / 5.4M
point dataset on the fly) — *the binding constraint is update rate × source
size*, per §2. The official animation examples only ever animate **one**
feature ([Animate a point](https://maplibre.org/maplibre-gl-js/docs/examples/animate-a-point/)
does rAF-rate `setData` on a 1-point source; [live realtime data](https://maplibre.org/maplibre-gl-js/docs/examples/add-live-realtime-data/)
uses a 2 s interval). With `updateData` (§3) this strategy plausibly covers
our target range, unproven above the low thousands at 10 Hz.

**(c) Custom WebGL layer or deck.gl — instanced draw call, the escape
hatch.** See §6 and §8.

**The practical ladder (evidence → rungs):** ≤ ~100 → DOM markers fine;
hundreds–few thousand moving features at ≤10 Hz → GeoJSON + symbol/circle +
`updateData`; ≥ ~10k moving features, or ≥30 Hz animation, or per-frame full
`setData` stutter measured → custom layer / deck.gl. This ladder *is* the
ADR-0003 escalation criterion in draft form; synthesis §4 makes it explicit.

## 6. Interpolation: smoothing 10 Hz snapshots

- There is no published official "max setData rate" — the safe rate is a
  function of source size (§2). The official examples span 10 ms–2 s update
  intervals, all on single-feature sources.
- The standard fleet pattern (used by opensky-network — the motivating case
  for updateData — and GTFS-RT vehicle maps): **receive snapshots at 10 Hz →
  lerp positions client-side inside `requestAnimationFrame` → write
  interpolated positions once per frame via `updateData`** (vehicle id =
  feature id) or into a custom layer's vertex buffer. This decouples render
  smoothness (60 fps) from network rate (10 Hz) and is exactly the
  snapshot-interpolation pattern from multiplayer game networking — with the
  ~200–300 ms interpolation buffer [[arch-time-model]] already budgets for
  viz clients.
- Gotcha: per-frame camera ops and full-map repaints multiply cost (§2,
  #3590); every interpolated frame still repaints the *whole* map, so the
  basemap's own frame cost sets the practical animation ceiling even when
  vehicle updates are cheap.

## 7. Congestion heatmaps on road geometry: what each mechanism actually does

- **Data-driven `line-color` — the workhorse.** `line-color` supports
  feature-state and interpolate expressions
  ([style spec](https://maplibre.org/maplibre-style-spec/layers/)), so
  per-segment congestion = `['interpolate', ['linear'], ['get','speed'], …]`
  or `['feature-state','speed']` — green→red over our Edie-derived speeds
  ([[domain-congestion-metrics]]). This *is* the "congestion heatmap on road
  geometry" mechanism.
- **`line-gradient` is a trap for this use.** Style spec: "Can only be used
  with GeoJSON sources that specify `"lineMetrics": true`" and
  "data-driven styling: **Not supported**." It paints a gradient along *one
  line's own length* (progress 0→1) — right for a single route trace, wrong
  for a network of independently colored segments.
- **The `heatmap` layer is point-density only.** `heatmap-weight` = "how much
  an individual **point** contributes"; color maps `["heatmap-density"]`.
  [mapbox #10097](https://github.com/mapbox/mapbox-gl-js/issues/10097):
  "**There's no way to make a 'line heatmap'** the same way the point heatmap
  works at the moment." For heat on roads: fat semi-transparent data-driven
  line layer ("traffic casing"), optionally blurred; or sample road midpoints
  as weighted points into a real heatmap layer (good zoomed-out density view);
  or deck.gl.
- **Join artifacts:** independently colored adjacent LineStrings show gaps/
  overlaps at joints; `"line-cap": "round"` smooths over discontinuities
  between separate lines with coincident end points (with overlap-at-joints
  downside) — [mapbox #8698](https://github.com/mapbox/mapbox-gl-js/issues/8698).
- **Update mechanics at 1 Hz:** do *not* `setData` a large static road
  GeoJSON every second (§2's deadlock was exactly heavy lines at 5 Hz). The
  options, in order of preference: (a) feature-state with `promoteId` on the
  load-once road source (§4); (b) split a small dynamic overlay source
  holding only congested segments (the perf guide's sanctioned pattern);
  (c) `updateData` property-only diffs (mind the 5.20 regressions, §3).
- **Large-static-source hygiene**
  ([MapLibre "Tips for Large GeoJSON Datasets"](https://www.maplibre.org/maplibre-gl-js/docs/guides/large-data/)):
  lower source `maxZoom` ("For most point sources, a maxZoom value of 12
  strikes a good balance"), layer `minZoom`, reduce coordinate precision
  (~6 decimals), chunk static vs live data ("if one part of the dataset has
  live updates and the rest is largely static, it could make sense to place
  these two parts into separate chunks"), allow-overlap to skip collision
  checks. A city-scale OSM network as one GeoJSON source is fine *if* it is
  treated as static.

## 8. Custom layers: WebGL2 inside the map, and the repaint pitfall

- API ([CustomLayerInterface](https://maplibre.org/maplibre-gl-js/docs/API/interfaces/CustomLayerInterface/),
  since mapbox 0.50.0 / 2018): implement `render` (optionally `prerender`,
  `onAdd`), drive frames with `Map.triggerRepaint`, handle
  `webglcontextlost/restored`. `renderingMode: "3d"` shares the depth buffer
  with other layers. MapLibre v3+ hands you a **WebGL2** context (the
  official example uses `#version 300 es`). Blending expects premultiplied
  alpha.
- **The pitfall: repaint granularity.** `triggerRepaint` repaints the
  *entire map*, every frame — there is no partial-layer refresh.
  [mapbox #8159](https://github.com/mapbox/mapbox-gl-js/issues/8159): a
  shader running at "60fps 4% CPU outside of Mapbox … end up having to slow
  it down to only **15fps, 40% CPU** when used as a layer in Mapbox due to
  the map under being fairly complex."
- Hand-rolled custom layer vs deck.gl: choose hand-rolled when the draw is
  one instanced point/quad pass (our vehicle dots) and zero deps matter;
  choose deck.gl when picking, aggregation, or path/trip layers enter the
  picture. ADR-0003 pre-approves the deck.gl path; a bespoke custom layer is
  the lighter middle rung the ADR didn't enumerate.

## 9. deck.gl mechanics (the escalation machinery)

- **What it is:** framework-agnostic WebGL2/WebGPU visualization from the
  vis.gl consortium; no React required (pure-JS `Deck` class). v9 line: v9.0
  (2024-03, luma.gl v9, TS types default), v9.1 (2025-01, MapLibre v5 globe
  support), v9.2 (2025-10), v9.3 (2026-04) ([What's New](https://deck.gl/docs/whats-new)).
- **Why it scales where MapLibre's pipeline doesn't — instancing.** The IEEE
  VIS 2019 paper ([arXiv 1910.08865](https://ar5iv.labs.arxiv.org/html/1910.08865))
  names the "primitive-instancing-layering (PIL) paradigm": "the primitives
  are instanced by mapping the attributes of each datum to visual channels
  such as position, size, color, angle … Instanced rendering executes the
  same drawing commands many times in a row … very efficient when rendering a
  large number of glyphs with very few API calls." One instanced draw call
  for the whole fleet vs re-tiled GeoJSON per update.
- **Documented ceilings** ([performance guide](https://deck.gl/docs/developer-guide/performance)):
  "most basic layers (like ScatterplotLayer) render fluidly at 60 FPS during
  pan and zoom operations **up to about 1M (one million) data items**, with
  framerates dropping into low double digits (10-20FPS) when the data sets
  approach 10M items" (2015 MBP); crash zone 10M–100M items from 1 GB
  allocation caps; picking distinguishes 16M items/layer; ~100–few hundred
  layers per app, "not designed to be used with thousands of layers."
- **The trap symmetric to MapLibre's:** "When the data prop changes, the
  layer will recalculate all of its GPU buffers … the most expensive
  operation that a layer does … if your data prop is updated frequently (e.g.
  animations), **'stutter' can be visible even for layers with just a few
  thousand items**." Naive GeoJSON-in/data-churn deck.gl is *also* slow — the
  documented mitigations are: `dataComparator`/`updateTriggers` (invalidate
  only changed attributes), constant accessors over callbacks ("99% of the
  CPU time that deck.gl spends in updating buffers is calling the accessors"),
  and **binary attributes**: `data: {length, attributes: {getPosition:
  {value: Float32Array, size: 2}}}` — "Such attributes, if prepared
  carefully, can be directly utilized by the GPU, thus bypassing the
  CPU-bound attribute generation completely. This technique offers the
  maximum performance possible in terms of data throughput." Buffers may come
  from "protobuf, Arrow or simply a custom binary blob", and worker
  Transferables make the handoff ~free. Primitive layers only — composite
  layers (GeoJsonLayer, HeatmapLayer) preprocess and lose the zero-copy path.
  v7.2 added `_dataDiff` for partial attribute updates.
- **Animation:** [Animations and Transitions](https://deck.gl/docs/developer-guide/animations-and-transitions) —
  attribute props (`getPosition`) can **transition on the GPU**: "animating
  the position of 1M point cloud involves 3M float64 or 6M float32 numbers …
  updated efficiently in parallel and without leaving the GPU memory." Caveat
  for spawns/despawns: "objects are identified by their index in the data
  array … if objects are inserted or removed, the transition will not look
  as expected" (stable ordering required). And the flagship use case is ours:
  "The most powerful way to create animations with deck.gl is to … update
  the layers' props on every frame … **An example of this kind of
  application is autonomous vehicle visualization, where car pose, LIDAR
  point clouds … are streamed in from a server many times a second.**"
- **Layers for us:** ScatterplotLayer (the 1M-item reference), IconLayer
  (pre-packed `iconAtlas` + `iconMapping` = "the most efficient way to load
  icons"; per-vehicle `getAngle` heading, tinting; binary `getIcon` needs
  integer mapping keys), PathLayer (congestion segments; binary form needs
  flat positions + `startIndices` + `_pathType: 'open'` to skip processing),
  TripsLayer (see §10), HeatmapLayer (GPU KDE: "50-100 ms for 2048x2048
  texture, but only 5-7ms for 512x512"; iOS Safari 8-bit fallback),
  ScreenGridLayer ("best used with small data set" — re-aggregates on every
  pan/zoom). GeoJsonLayer is composite — convenient for the *static* network,
  wrong for per-frame vehicles.
- **MapLibre integration** ([Using with MapLibre](https://deck.gl/docs/developer-guide/base-maps/using-with-maplibre),
  [MapboxOverlay API](https://deck.gl/docs/api-reference/mapbox/overview)):
  `MapboxOverlay` in **overlaid** mode (default; separate canvas in the
  controls container; MapLibre controls keep working) or **interleaved** mode
  ("renders deck.gl layers into the WebGL2 context created by MapLibre …
  requires WebGL2 and therefore only works with `maplibre-gl@>3`"; per-layer
  `beforeId` slips deck layers under basemap labels; terrain "partially
  supported … z=0 rendered at sea level"). Overlaid is the right default for
  vehicles-on-top; interleaved matters only for label z-ordering or 3D
  occlusion. Interleaved mode is itself built on the §8 custom-layer API.

## 10. Replay and trip animation: TripsLayer and the kepler pattern

- [TripsLayer](https://deck.gl/docs/api-reference/geo-layers/trips-layer):
  "renders animated paths that represent vehicle trips." Data = per-trip
  waypoints (`getPath`) + one timestamp per waypoint (`getTimestamps`);
  animation driven by the scalar `currentTime` prop (shader-side reveal — no
  per-frame attribute regen), `trailLength`/`fadeTrail` for the tail.
  **Format caveat:** "Because timestamps are stored as 32-bit floating
  numbers, raw unix epoch time can not be used" — rebase to offsets.
  Suitability: designed for *replay of complete trips* (all waypoints known
  up front), not live-appended tails — which matches our JetStream replay
  mode ([[arch-nats-backbone]]): fetch a recorded run, build trip arrays,
  scrub `currentTime`.
- kepler.gl's Trip layer productizes exactly this: GeoJSON LineStrings with a
  4th coordinate element `[longitude, latitude, altitude, timestamp]`, plus
  trail-length slider and animation speed control
  ([kepler.gl docs](https://docs.kepler.gl/docs/user-guides/c-types-of-layers/k-trip)).
  The UX is the reference for our replay scrubber; the dependency (React +
  Redux) is disqualified by ADR-0003 — steal the UX, not the code.

## 11. Version floor and pinning implications

- Everything MapLibre-side except `updateData` works ≥ 2.x; `updateData`
  needs ≥ 3.0.0; the 5.11–5.14 perf fixes argue for 5.x; the 5.20
  diff-update regressions (#7315, #7257 — both fixed in 5.20.2 / 5.21.1)
  argue for pinning a verified release (≥5.21.1) and CI-benchmarking the
  exact call
  pattern (promoteId + property updates at 10 Hz) before upgrade.
- deck.gl needs maplibre-gl v3+ (interleaved needs WebGL2); MapLibre v6
  pre-releases drop WebGL1 and move to ESM
  ([MapLibre newsletter, April 2026](https://maplibre.org/news/2026-05-02-maplibre-newsletter-april-2026/)) —
  fine for us (WebGL2-only, vanilla TS, pnpm/ESM).

## 12. Implications for our design (mechanics → architecture)

1. **Three sources, three mechanisms:** (a) road network — load-once GeoJSON
   (or vector tiles later) with `promoteId` on segment id, `maxZoom` tuned,
   ~6-decimal precision; (b) congestion — `setFeatureState` per metrics
   snapshot driving `line-color` (never `setData` on the network source at
   1 Hz); (c) vehicles — dedicated small GeoJSON source, `updateData` diffs
   per interpolated frame, vehicle id = feature id.
2. **The wire format is not GeoJSON.** GeoJSON is a client-local interchange
   format. The NATS vehicle subject should carry binary SoA frames
   (Float32Array positions/angles + ids) so the *same* payload can feed
   `updateData` (converted client-side, cheap at low thousands) or deck.gl
   attributes (zero-copy) — deciding the format once at the contract level
   ([[arch-nats-backbone]] decision 7) instead of per-client.
3. **Interpolation lives client-side** with the 200–300 ms buffer from
   [[arch-time-model]]; render loop at rAF, network at 10 Hz, metrics at 1 Hz.
4. **Escalation is a ladder, not a switch** — §5's rungs, with the measured
   triggers from §2/§9. The first escalation (custom WebGL layer) stays
   dependency-free; deck.gl is the second.
5. **Replay mode reuses everything**: recorded run → trip arrays → TripsLayer
   (timestamps rebased) or the same vehicle source driven by the replayer's
   tick pacing ([[arch-nats-backbone]] decision 4).
