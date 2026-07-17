# Standards & Patterns: MapLibre Realtime Viz

> Source: academic research + pattern identification | Researched: 2026-07-17

## RFC 7946 — The GeoJSON Format (IETF, 2016)

GeoJSON is the only input format MapLibre's GeoJSON sources accept
(`setData(data: string | GeoJSON)`), so RFC 7946 is the de-facto contract for
our client-local data: WGS84 lon/lat positions ("longitude and latitude in
decimal degrees", §4), Feature/FeatureCollection structures with `id` and
arbitrary `properties` (§3.2). Three implications for us:

- **Precision is a performance lever, not accuracy.** The RFC recommends 6
  decimal places (~10 cm) as a default for "common" cases (§11.2);
  MapLibre's large-data guide independently tells users to cut precision to
  ~6 decimals. Our engine computes in meters at centimeter resolution —
  there is no reason to serialize more.
- **`id` is a first-class member** (RFC §3.2 "If a Feature has a commonly
  used identifier, that identifier SHOULD be included as a member of the
  Feature object with the name of 'id'") — and it is exactly the member
  MapLibre's `updateData`, `feature-state`, and `promoteId` machinery keys
  on. Vehicle id and road-segment id belong there, not only in `properties`.
- **GeoJSON is a document format, not a streaming format**: no partial
  update, no delta, no binary encoding in the standard. Everything realtime
  we layer on it (updateData diffs, SoA binary on the wire) is
  implementation-level, not standards-level.

Source: [RFC 7946](https://datatracker.ietf.org/doc/html/rfc7946);
[GeoJSONSource docs](https://maplibre.org/maplibre-gl-js/docs/API/classes/GeoJSONSource/);
[MapLibre large-data guide](https://www.maplibre.org/maplibre-gl-js/docs/guides/large-data/).

## MVT / MLT / PMTiles — the vector-tile family

- **Mapbox Vector Tile spec (MVT 2.1)** — protobuf-encoded tiled layers,
  the format MapLibre vector sources consume; binary, compact, pre-tiled, but
  *not* dynamically updatable from the client (tiles are produced
  server-side). Our static road network could graduate to MVT if load-once
  GeoJSON ever becomes the bottleneck; the 1 Hz metric overlay and 10 Hz
  vehicles never belong in tiles.
  Source: [MVT spec](https://github.com/mapbox/vector-tile-spec).
- **MapLibre Tiles (MLT)** — MapLibre's next-gen columnar tile format,
  consumed via `encoding: 'mlt'` in vector sources since MapLibre 5.12.0
  (MapLibre CHANGELOG). Worth tracking for the static network; irrelevant to
  live channels.
- **PMTiles** — single-file tile archives over HTTP range requests; a
  serving convenience for static data, not a live-data mechanism.
  Source: [PMTiles spec](https://github.com/protomaps/PMTiles).

**Connection to our implementation:** these formats define the *static*
channel's ceiling; the live channels are deliberately out-of-band (NATS →
client → `updateData`/feature-state/attributes). This is the same
static/dynamic split as [[arch-nats-backbone]]'s KV-latest vs core-live vs
JetStream-record planes, one level up the stack.

## MapLibre Style Spec — the declarative styling contract

The forked-from-Mapbox open style specification is the stable contract our
metric→visual mapping compiles to: sources (with `promoteId`, `generateId`,
`lineMetrics`, cluster options), layers with data-driven paint properties,
and the expression language (`interpolate`, `match`, `get`, `feature-state`,
`heatmap-density`). Versioned semantics we rely on: `line-color` "Supports
feature-state and interpolate expressions"; `feature-state` "is not supported
in filter expressions"; `line-gradient` geojson+`lineMetrics` only with no
data-driven styling. Style JSON is data, not code — a scenario can carry
style fragments (metric → ramp) as configuration, keeping visual encoding
out of the client source. Source:
[style spec](https://maplibre.org/maplibre-style-spec/).

## The PIL paradigm — deck.gl's formal rendering model

From the IEEE VIS 2019 paper *Deck.gl: Large-scale Web-based Visual Analytics
Made Easy* ([arXiv 1910.08865](https://ar5iv.labs.arxiv.org/html/1910.08865)):
the "primitive-instancing-layering (PIL) paradigm" — every layer = one GPU
primitive × per-datum attributes mapped to visual channels ("position, size,
color, angle") × instanced rendering ("executes the same drawing commands
many times in a row … very efficient when rendering a large number of glyphs
with very few API calls"). This is the academic statement of why the
escalation path exists: WebGL instancing is O(draw calls ≈ layers), while
MapLibre's source pipeline is O(total vertices × update rate). Our fleet is
one primitive (instanced quad/point sprite) with attributes
(position, angle, class) — the textbook PIL case.

## XVIZ — a dead but well-designed streaming-viz protocol

Uber's XVIZ (2019, with streetscape.gl) is the only *published protocol
design* for exactly our problem shape — streaming time-stamped scene state
from a server to a WebGL client. Its named concepts
([concepts](https://github.com/uber/xviz/blob/master/docs/overview/concepts.md)
and [conventions](https://github.com/uber/xviz/blob/master/docs/overview/conventions.md)
docs):

- **Stream**: "a sequence of timestamped datums of the same type", path-like
  names (`/vehicle/velocity`). ≈ our NATS subjects with class tokens.
- **Timeslice**: the synchronized cross-stream snapshot (concepts.md's
  "time slices"). ≈ our tick-stamped snapshot.
- **Object ids across streams**: "one stream holds a set of objects, use id
  to distinguish" — and the conventions doc explicitly calls "a stream per
  object" **bad** ("provides no cross stream object linking") — the exact
  subject-per-entity anti-pattern [[arch-nats-backbone]]'s taxonomy also
  avoids (identifiers last, but entity *data* multiplexed, not
  subject-per-vehicle).
- **Metadata message**: streams + stylesheets declared up front ≈ our
  AsyncAPI contract ([[arch-nats-backbone]] decision 7).
- **Sources = logs or live sockets; JSON or binary GLB** ≈ our live vs
  JetStream-replay duality with binary frames.

Why it died: tied to Uber ATG, React-coupled toolkit, unmaintained since
~2022 ([npm](https://www.npmjs.com/~twojtasz-aurora)). The protocol ideas are
sound and map cleanly onto NATS primitives; the implementation is a caution
about coupling a protocol to one employer's program.

## Design Patterns Identified

### Snapshot interpolation (client-side entity interpolation)

The multiplayer-game pattern: server emits discrete snapshots at rate R;
client renders continuously by interpolating between the two most recent
snapshots behind a deliberate delay (our ~200–300 ms buffer from
[[arch-time-model]]). Decouples network jitter/rate from render smoothness.
In our stack: NATS 10 Hz snapshots → rAF lerp → `updateData`/attribute
write per frame. OTFVis's ~30 fps cap ([competitors](./competitors.md)) is
the same pattern with the buffer set to ~0 and the sim doing the
interpolation implicitly. The alternative — extrapolation/dead reckoning —
is wrong for us: the engine is authoritative and replay requires showing
what *was* simulated, not what was predicted.

### Channel splitting by update rate (fast/slow source separation)

Mapbox's official performance rule — "Use a separate GeoJSON source for data
that needs to be updated rapidly" — generalized: partition data by update
frequency and give each partition the cheapest channel that can carry it.
Static network → load-once source (or vector tiles); 1 Hz metrics →
feature-state (no re-parse); 10 Hz positions → small dedicated source with
diffs; ≥30 Hz or ≥10k → instanced attributes. This is just QoS-aware channel
design, the same reasoning [[arch-nats-backbone]] applied to core-vs-
JetStream-vs-KV.

### Data join (feature-state as a runtime join key)

The data-join pattern from BI/mapping: static geometry + dynamic measures
joined at render time by a stable key (`promoteId` = segment id,
`setFeatureState` = the join's value channel). Mapbox formalizes it in their
[Data Joins guide](https://labs.mapbox.com/education/impact-tools/data-joins/).
It is why our road-graph ids must be stable end-to-end: engine lane id →
scenario → metrics event → GeoJSON feature id → feature-state key. A rename
anywhere silently breaks the heatmap (best-effort semantics on both
`updateData` and feature-state mean *no error is raised* for missing ids —
the failure mode is a gray road, not an exception). Contract tests on id
stability belong in the viz client test plan.

### Structure-of-Arrays (SoA) binary frames

Deck.gl's binary-attribute contract (`{length, attributes: {getPosition:
{value: Float32Array, size: 2}}}`) and Arrow/GLB precedents (XVIZ binary,
geoarrow zero-copy "3.2 million points … copies binary buffers directly from
an Arrow JS RecordBatch to the GPU",
[geoarrow/deck.gl-geoarrow](https://github.com/geoarrow/deck.gl-geoarrow))
share one shape: per-field contiguous typed arrays, transferred by reference
(Worker Transferables "at virtually no cost" per the deck.gl perf guide).
Making the NATS vehicle subject SoA binary from day one means the cheapest
consumer (deck.gl) needs zero transformation and the MapLibre consumer pays
one cheap `map()` — vs GeoJSON-on-the-wire forcing the expensive parse
*before* any choice. deck.gl's own MVT numbers quantify the JSON tax: a
1.57M-vertex dataset loaded and rendered (full viewport) in 4261 ms binary
vs 9202 ms non-binary (-53.69%)
([v8.4 release notes](https://deck.gl/docs/whats-new)).

### Level of detail / interest management (renderer-side)

OTFVis's quadtree ("only the smallest set of data necessary to display the
visible sector … is transferred") and sumo-gui's simplification knobs are
the sim-side version of a general pattern: don't ship what isn't visible.
Our equivalents, in ascending effort: MapLibre's own tile pipeline (only
on-screen tiles render — free), source `maxZoom`/layer `minZoom` tuning,
zoom-dependent layers (segments at low zoom, vehicles at high zoom), and
eventually server-side area-of-interest filtering on the snapshot subject —
deferred to [[arch-state-authority]], which owns that decision.

### Heatmap as kernel density (KDE) vs categorical casing

Two mathematically different things both called "heatmap": (1) KDE over
point masses with a weight field (MapLibre `heatmap` layer, deck.gl
HeatmapLayer — Gaussian splat per point, density-normalized color ramp);
(2) per-segment categorical/continuous coloring of a measure that lives *on
the geometry* (our Edie speed/density per lane segment,
[[domain-congestion-metrics]]). Ours is definitionally (2) — the measure has
a location (the segment), not a density to estimate — so the correct
mechanism is data-driven `line-color`/PathLayer, and (1) is only right for
derived point views (e.g., vehicle-density overview at city zoom).
Choosing (1) for road congestion would *invent* smoothing artifacts the
metrics don't have and misrepresent queue boundaries — a data-integrity
issue, not just a visual one.

## Anti-Patterns (documented failure modes, each with a source)

- **Per-frame full `setData` on a non-trivial source.** The documented
  deadlock: ~900k coordinates at 5 Hz → 200 ms main-thread stringify +
  200 ms worker parse per update → "the website becomes unresponsive"
  ([maplibre #106](https://github.com/maplibre/maplibre-gl-js/issues/106)).
  Even 25 points at 20 Hz pegged a CPU
  ([SO 61264144](https://stackoverflow.org.cn/questions/61264144)).
- **DOM `Marker` per vehicle.** Officially degraded past 100
  ([markers guide](https://docs.mapbox.com/mapbox-gl-js/guides/add-your-data/markers/));
  measured drag-lag at 200
  ([react-map-gl #750](https://github.com/visgl/react-map-gl/issues/750)).
- **`line-gradient` for per-segment congestion.** No data-driven styling;
  paints along one line's own length
  ([style spec](https://maplibre.org/maplibre-style-spec/layers/)).
- **`heatmap` layer for road congestion.** Point-density only; "There's no
  way to make a 'line heatmap'"
  ([mapbox #10097](https://github.com/mapbox/mapbox-gl-js/issues/10097)).
- **feature-state on a source you also `setData`.** State must be re-applied
  after data changes; silent desync otherwise
  ([Map#setFeatureState](https://maplibre.org/maplibre-gl-js/docs/API/classes/Map/#setfeaturestate)).
- **Naive deck.gl data churn.** "stutter … even for layers with just a few
  thousand items" when the `data` prop changes per frame without
  `dataComparator`/`updateTriggers`/binary attributes
  ([deck.gl performance](https://deck.gl/docs/developer-guide/performance)).
- **Raw epoch timestamps in TripsLayer.** "Because timestamps are stored as
  32-bit floating numbers, raw unix epoch time can not be used"
  ([TripsLayer docs](https://deck.gl/docs/api-reference/geo-layers/trips-layer)).
- **Subject-per-vehicle on the bus.** XVIZ's explicit warning ("don't make
  one stream per object") matches NATS's subscription-memory guidance in
  [[arch-nats-backbone]]; multiplex by id inside one snapshot message.

## Academic/Field Context: what we could not find (honest gaps)

- No published rigorous benchmark of MapLibre feature-state throughput
  (N features × updates/sec) — the official docs assert the mechanism is
  cheap but give no numbers; every setData number above is from issue
  reports, not controlled benchmarks.
- No published fps curve for `updateData` at fixed feature counts/rates
  (e.g., 1k/5k/10k/50k features at 10 Hz). The API is from 2023; its 5.20.x
  regressions were closed-as-completed in March 2026 (fixed in 5.20.2 /
  5.21.1), but the release line is young enough to keep watching.
- No open-source browser client documented as consuming a *simulation's*
  10 Hz authoritative state stream (as opposed to 15–60 s GTFS-RT). Our
  microbenchmark will be the primary source — same "unwritten frontier"
  situation [[arch-time-model]] and [[arch-nats-backbone]] both reported.
