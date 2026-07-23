# MapLibre Realtime Viz

> Three rate-split channels (load-once network, 1 Hz feature-state heatmap, 10 Hz `updateData` vehicle diffs) carry live state into MapLibre; binary SoA wire frames and a measured deck.gl escalation ladder cover scale.

## Overview

The visualization client must render two live things in a browser: thousands of
vehicles moving at 10 Hz and a congestion heatmap over road geometry at ~1 Hz,
on top of a static OSM-derived network — per [ADR-0003](../../decisions/ADR-0003-maplibre-vis.md),
MapLibre GL JS first, vanilla TS, no UI framework, deck.gl only on measured need.

The research produced an unusually crisp conclusion: the scaling wall is not GPU
rendering, it is JSON. MapLibre's GeoJSON pipeline re-stringifies, re-parses, and
re-tiles the *entire* source on every `setData`, which breaks at animation rates
in the low thousands of moving features. MapLibre's own escape hatches —
`updateData` diffs (3.0.0+) and feature-state — map 1:1 onto the engine's
spawn/move/despawn stream and the metrics-on-static-geometry join. deck.gl's
instanced layers fed by typed-array attributes hold 60 FPS at ~1M items, giving
one to two orders of magnitude of headroom when MapLibre-native runs out.

The competitor survey confirms the position: incumbent sim visualizers
(sumo-gui, OTFVis, VIA) are native desktop apps with no browser story; everyone
who gave a sim a web client reached for a GPU scene layer and mostly died
(sumo-web3d, streetscape.gl/XVIZ). The durable move is to stand on maintained
rendering infrastructure (MapLibre, deck.gl) and own only the data plumbing and
the stream contract. See [VISION.md](../../../VISION.md) for why the browser
episode drives this choice.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Static network source | `raw/integration-maplibre-realtime/implementation.md` §1, §7 | Load-once GeoJSON (vector tiles later), `promoteId` on segment id, tuned `maxZoom`, ~6-decimal precision |
| Congestion channel | implementation.md §4 | `setFeatureState` per 1 Hz metrics snapshot keyed by segment id; restyle without re-parse |
| Vehicle channel | implementation.md §3 | Dedicated small GeoJSON source; `updateData` add/update/remove diffs per interpolated frame |
| Interpolation loop | implementation.md §6; [ADR-0005](../../decisions/ADR-0005-time-model.md) | rAF lerp between two latest 10 Hz snapshots behind a 200–300 ms buffer |
| Escalation ladder | synthesis §4; [ADR-0003](../../decisions/ADR-0003-maplibre-vis.md) | Rungs 0–3: MapLibre-native → custom WebGL2 layer → deck.gl → full binary pipeline |
| Vehicle wire format | [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) | Binary SoA frames (~8–16 B/vehicle) on the NATS vehicle subject; AsyncAPI 3.0 contract |
| Replay path | implementation.md §10 | Same vehicle source driven by JetStream playback; TripsLayer + scrubber for whole-run overview |
| Viz microbenchmark | synthesis §7 | Fleet-size × rate grid over `updateData` + feature-state in CI; gates rung transitions |
| Version floor | implementation.md §11 | Pin MapLibre ≥5.21.1 (5.20 diff regressions fixed); WebGL2-only |

## How It Works

### 1. Split channels by update rate — never one GeoJSON blob

Mapbox's official cost model, inherited wholesale by MapLibre: source-update time
scales with *layers-on-source × total vertices*, and "any update to it requires
… reprocess the entire set of data." The sanctioned fix is a separate small
source for rapidly updating data. So the three data channels get three
mechanisms:

- **Road network** (static): one load-once GeoJSON source, `promoteId` on
  segment id, source `maxZoom` lowered from the default 18, ~6-decimal
  coordinates (RFC 7946 §11.2 — ~10 cm; the engine computes in meters, nothing
  more is needed). Vector tiles (MVT/MLT) only if it ever outgrows GeoJSON.
- **Congestion** (1 Hz): N `setFeatureState` calls per metrics snapshot, keyed
  by segment id, with `line-color` driven by
  `['interpolate', …, ['feature-state', 'speed']]`. feature-state exists
  precisely to "avoid re-parsing all the geometries at each state change."
- **Vehicles** (10 Hz): a dedicated small GeoJSON source; each interpolated
  frame applies one `updateData` diff — `add` / `update` geometry / `remove` by
  id, vehicle id = feature id. The diff vocabulary is exactly the sim's
  spawn/move/despawn stream.

### 2. Client-side interpolation decouples 10 Hz network from 60 fps render

There is no safe fixed `setData` rate — failures scale with source size, not
rate alone: ~900k coordinates at 5 Hz cost ~200 ms main-thread stringify +
~200 ms worker parse *per update* and wedged the page permanently
(maplibre #106); 25 points at 20 Hz pegged a laptop CPU; 20–60 Hz symbol
updates measurably degrade interaction. So vehicles render inside
`requestAnimationFrame`, lerping between the two latest snapshots behind the
~200–300 ms buffer [ADR-0005](../../decisions/ADR-0005-time-model.md) budgets
for viz clients; one `updateData` (or attribute write, post-escalation) per rAF
frame, never per incoming message. This is the standard multiplayer-game
snapshot-interpolation pattern; extrapolation is rejected because replay must
show what *was* simulated. The viz therefore always lags the authoritative tick
by the buffer length — fine for observers. `updateData` itself cut 11.5 ms →
1.8 ms per update in MapLibre's own example (PR #1605) and shipped in 3.0.0.

### 3. Congestion heatmap = data-driven line casing, not the `heatmap` layer

The measure (per-segment Edie speed/density, see
[Congestion Metrics](../business-domains/congestion-metrics.md)) lives *on the
geometry* — it is categorical coloring, not kernel density. The zoomed-in view
is a fat semi-transparent `line` layer over the road source, colored by the
feature-state join, `line-cap: round` at joints; a zoomed-out overview may
sample segment midpoints as weighted points into a real `heatmap` layer,
clearly labeled as a density view. Using KDE on road congestion would invent
smoothing artifacts and misrepresent queue boundaries — a data-integrity
problem, not just a visual one.

### 4. The deck.gl escalation ladder (the deliverable ADR-0003 deferred here)

Escalate on measured need, in order:

- **Rung 0 — MapLibre-native** as above. Comfort zone: thousands of segments at
  1 Hz; low thousands of vehicles at 10 Hz.
- **Rung 1 — measured stutter in `updateData` at target fleet size:** reduce
  rate, zoom-gate the vehicle layer (city zoom = heatmap only), or drop to a
  hand-rolled WebGL2 custom layer — one instanced point/quad draw, zero new
  deps.
- **Rung 2 — ≥ ~10k animated vehicles, or picking/aggregation needs:** deck.gl
  `MapboxOverlay` in *overlaid* mode (interleaved only if vehicles must sit
  under labels), ScatterplotLayer/IconLayer with `dataComparator` +
  `updateTriggers` discipline. Documented comfort zone: **~1M instanced items
  at 60 FPS**; low double-digit FPS approaching 10M.
- **Rung 3 — full binary pipeline:** web worker packs NATS SoA frames straight
  into Float32Array attributes (Transferables, zero-copy), `_dataDiff` for
  partial updates, GPU attribute transitions interpolating between snapshots.

The crossover evidence: MapLibre's documented failure at ~900k coordinates /
5 Hz vs deck.gl's documented 1M items at 60 FPS — comparable item counts at
12× the rate. This ladder *is* the escalation criterion
[ADR-0003](../../decisions/ADR-0003-maplibre-vis.md) asked this topic to
document; rung thresholds get pinned by the microbenchmark before adoption.

### 5. Wire format: binary SoA from day one — ratified by ADR-0006

GeoJSON is a client-local interchange format, never the wire format.
[ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) ratified the
research position: the NATS vehicle subject carries binary structure-of-arrays
frames (~8–16 B/vehicle: ids + Float32 x/y + angle + class), one multiplexed
subject, declared in AsyncAPI 3.0. The MapLibre client converts to diffs
(cheap at low thousands); a future deck.gl client consumes near-zero-copy.
GeoJSON-on-the-wire would force the expensive JSON parse *before* any rendering
choice — deck.gl quantifies the tax at 1.57M vertices parsed in 4261 ms binary
vs 9202 ms non-binary (−53.69%). XVIZ independently chose JSON-or-binary-GLB
over sockets for the same reason. Road network and metrics stay id-keyed JSON;
their rates are low.

### 6. Replay reuses the live path

Replaying a recorded run = the same vehicle source driven by the replayer's
tick pacing (JetStream playback per
[ADR-0002](../../decisions/ADR-0002-nats-backbone.md)). For whole-run overview,
build per-trip waypoint arrays and use deck.gl TripsLayer with a kepler.gl
Trip-layer-style scrubber (trail length, speed control) — steal the UX, not the
React+Redux dependency, which ADR-0003 disqualifies. TripsLayer animates via a
scalar `currentTime` with shader-side reveal (no per-frame attribute regen) and
assumes complete trips — which matches JetStream replay exactly.

### 7. Version pinning and test posture

Pin MapLibre to a CI-verified 5.x (≥5.21.1): `updateData` had a 5.20 regression
tail (property updates ignored via string `promoteId`; diff updates not
rendering), fixed in 5.20.2 / 5.21.1. The API is young enough to remain our
load-bearing surface — the viz microbenchmark (fleet size × rate grid over
`updateData` and feature-state) runs in CI and is the required input to any
rung transition. WebGL2-only is fine (MapLibre v3+, deck.gl interleaved
requirement). There are *no published benchmarks* of feature-state or
`updateData` throughput at our shape; we must produce our own.

## Gotchas

- **`setData` is O(total vertices × layers), not O(delta)**: two costs per call — main-thread JSON stringify and worker re-tiling — both scale with whole-source size. The catastrophic case (~900k coords at 5 Hz → permanent main-thread block) is why fast-moving data gets its own small source.
- **`updateData` and feature-state fail silently on missing ids**: best-effort semantics raise no error — the failure mode is a gray road or a stale dot. Engine lane id → scenario → metrics event → GeoJSON feature id → feature-state key must be stable end-to-end, guarded by a contract test.
- **feature-state can't move geometry or filter**: it restyles only ("not supported in filter expressions"), so it can never be the vehicle channel; and it must be re-applied if the source is ever `setData`'d — hence the road source is load-once.
- **`line-gradient` and the `heatmap` layer are traps for per-segment congestion**: `line-gradient` has no data-driven styling and paints along one line's own length; the `heatmap` layer is point-density only ("There's no way to make a 'line heatmap'").
- **`triggerRepaint` repaints the *whole* map**: a custom-layer shader at 60 fps / 4% CPU standalone became 15 fps / 40% CPU inside the map (mapbox #8159); separately, 10 GeoJSON sources cost ~1 ms each per frame — 10 ms of a 16 ms budget. The basemap's own frame cost sets the practical animation ceiling.
- **Naive deck.gl is also slow**: per-frame `data` prop churn stutters "even for layers with just a few thousand items." Binary attributes + `dataComparator`/`updateTriggers` are part of the adoption, not a later optimization.
- **deck.gl GPU transitions break on spawn/despawn**: objects are identified by array index, so inserted/removed vehicles morph wrongly — stable fleet ordering (slot reuse) is required at rung 3.
- **TripsLayer timestamps are float32**: "raw unix epoch time can not be used" — rebase to per-run offsets.
- **DOM `Marker` per vehicle**: officially degraded past 100 markers, measured drag-lag at 200; ceiling is hundreds, never thousands.
- **Subject-per-vehicle on the bus**: XVIZ's conventions explicitly call one-stream-per-object bad (no cross-stream linking); multiplex by id inside one snapshot message.

## Open Questions

- Exact fleet ceiling of `updateData` at 10 Hz (1k/5k/10k/50k vehicles ×
  1/10/30 Hz grid) — microbenchmark during viz bring-up; determines the rung-1
  trigger numerically.
- feature-state throughput at ~5–10k segments × 1 Hz — same benchmark; if it
  stutters, fall back to a split small overlay source of congested segments.
- Vehicle picking (click → details) after deck.gl escalation: deck.gl picking
  (16M items/layer) vs a parallel invisible MapLibre source — decide at rung 2
  (`queryRenderedFeatures` is lost for deck layers).
- Multiplayer latency: does the viz interpolation buffer add perceptibly to the
  human-controller loop, or does the controller path bypass interpolation
  entirely? (with [State Authority](../architecture/state-authority.md))
- Zoom-gating policy: at what zoom do vehicles cede to pure heatmap? UX +
  benchmark decision; affects how big a fleet rung 0 actually serves.
- City-scale static network: load-once GeoJSON vs MVT/MLT vector tiles —
  depends on [OSM Extraction](../integrations/osm-extraction.md) region sizes; GeoJSON first.

## Addendum 2026-07-22: glyph rung — SDF rect vehicles + legend overlay

The rung-0 circle layer gave way to a rotated-rectangle **symbol** layer,
still on the same `updateData`-diffed vehicle source (source, `promoteId`,
and diff channel untouched — a pure layer swap). One white rectangle is
drawn to an offscreen canvas at load and registered with
`map.addImage("veh-rect", img, { sdf: true })`, so a single image serves
every class: `icon-color` tints, `icon-rotate` aims, `icon-size` scales.
Three decisions worth recording:

- **Bearing conversion**: the wire heading is radians CCW from east (engine
  local frame, UTM north-up); MapLibre's `icon-rotate` is degrees CW from
  north. The conversion `90 − angle·180/π` lives in `viz/src/theme.ts` as a
  tested pure function; the layer re-evaluates the same math inline as a
  style expression (expressions can't call back into TS). The rect image is
  drawn vertically (long axis = image north) so the unrotated icon already
  reads as heading north at bearing 0.
- **Screen-space glyphs over true-metric polygons**: at corridor zooms
  (11–15) a real 5 m footprint is sub-pixel to a few px — fidelity would be
  invisible. Class and heading are the data being encoded, so glyphs are
  screen-sized (`icon-size` by zoom, car ≈ 9 px at z14) while the *ratio*
  between classes comes from the engine's real dimensions (Truck/Car =
  12/5 = 2.4× longer). MapLibre rejects `["zoom"]` nested under `["*"]`, so
  the per-class multiplier rides the interpolate stop outputs (composite
  expression).
- **Legend overlay** (`viz/src/legend.ts`): a pure-DOM panel (no MapLibre
  APIs) keyed to the same theme tokens — class swatches at the true length
  ratio, signal green/amber/red, the congestion ramp (per-lane mean
  speed/limit), and a sim clock derived from the render loop's sample tick
  (× dt = 0.1 s). All palette literals moved to `viz/src/theme.ts` so
  swatches can't drift from the map.

Live-viewing the pass surfaced three fixes, landed the same day: (1) the
wire position is the FRONT bumper (`Project(s)`: s is front-bumper arc
length), so the centered glyph anchor is shifted back half the class length
along the heading — queued vehicles now stand bumper-to-tail instead of
overlapping; (2) one image per class at its true aspect ratio (same px/m),
replacing the single-image + per-class icon-size multiplier that made
trucks 2.4× wider and span adjacent lanes; (3) netconvert emits consecutive
duplicate polyline points (4 of 375 lanes on the I-280 import), whose
zero-length segments reported tangent 0 — vehicles flashed due east for a
frame. `Lane.SetShape` now drops consecutive duplicates before building the
arc-length table (display-only, CRC-unaffected).

Later the same day: trucks render ARTICULATED (tractor + trailer, pivot
joint at the hitch). The engine stays a rigid body; the trailer is inferred
client-side (`viz/src/artic.ts`) with the standard single-track trailer
equation — the trailer axle follows the path of its hitch, which is real
kinematics, not decoration (on a turn the trailer cuts inside exactly as a
physical trailer must). No contract change was needed: front-bumper point,
heading, and class already flow on TSSF v1; the trailer rides a second
`updateData`-diffed source under the vehicles layer.

## Addendum 2026-07-23: signal heads + loading overlay

Two live-viewing fixes:

- **`icon-image` cannot read feature-state** (style spec: its expression
  parameters are `zoom` + `feature` only), so a state-switchable ICON is
  impossible — the per-lane stop-line circles became signal HEADS: a
  static housing sprite with three dim lenses (`viz/src/signalhead.ts`)
  plus one circle layer per lens position (red/amber/green), each with a
  fixed viewport-anchored `circle-translate` and `circle-opacity` gated by
  feature-state. One zoom curve drives housing `icon-size` AND lens
  radius/translate so the lit lens always lands on its dim counterpart.
  Points are grouped one head per (program, link index) at the centroid of
  the movement's bound stop-line entries — lanes sharing a state char show
  the same light by construction, and netimport grids bind several fragment
  lanes per movement.
- **Loading overlay** (`#loading` in `viz/index.html`): page load →
  network fetch → ws connect → first snapshot takes noticeable seconds on
  big demos (engine world build precedes the first TSSF frame) and a bare
  "connecting…" HUD read as a hang. A full-screen stage readout lifts on
  the first renderable sample.
- **Edge-group casing** (`viz/src/edges.ts`): the network format's
  lateral-chaining group (`edge` + `edgeIndex`, now exported in the
  GeoJSON lane properties) doubles as the viz's "same road" signal — the
  casing layer draws only each group's outermost lanes (min/max index),
  so zoomed-in a multi-lane road reads as one cased band with per-lane
  congestion stripes inside instead of N independently-cased lines.
  Interior lanes keep a faint 0.15 casing trace for separability.

## Related

- [NATS Backbone](../architecture/nats-backbone.md) — owns the subject taxonomy and JetStream replay the viz consumes; the SoA vehicle frame shape was fixed together with ADR-0006.
- [Time Model](../architecture/time-model.md) — the 100 ms tick and the 200–300 ms interpolation buffer budget this client renders behind.
- [Congestion Metrics](../business-domains/congestion-metrics.md) — per-segment Edie speed/density is what the feature-state heatmap actually shows; metric event ids must be road-graph ids.
- [Simulator Landscape](../business-domains/simulator-landscape.md) — sumo-gui/OTFVis/VIA are the incumbent native viz practice this design diverges from.
- [State Authority](../architecture/state-authority.md) — owns future area-of-interest filtering of the snapshot subject and the multiplayer-latency question.
- [OSM Extraction](../integrations/osm-extraction.md) — region size determines whether the static network source stays GeoJSON or graduates to vector tiles.

---
*Raw research: [raw/integration-maplibre-realtime/](../../raw/integration-maplibre-realtime/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
