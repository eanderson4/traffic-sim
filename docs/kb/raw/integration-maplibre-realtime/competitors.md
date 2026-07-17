# Competitors: MapLibre Realtime Viz

> Source: web research | Researched: 2026-07-17

## Competitive Landscape

Two landscapes overlap here. (1) **How traffic simulators visualize**: the
incumbents (SUMO, MATSim, CARLA) all render through bespoke native OpenGL
scene graphs fed by in-process state — none has a first-class browser client,
and the third-party attempts to give them one are archived or niche. (2)
**How the web-GIS world renders live fleets**: a MapLibre/Mapbox basemap for
everything static, plus an escalating series of mechanisms for the moving
layer — DOM markers, GeoJSON symbol layers, custom WebGL, deck.gl — with
production GTFS-RT deployments proving the MapLibre + deck.gl combination.
Our position is deliberately inside landscape 2, consuming landscape 1's
lessons: everyone who scales eventually owns a GPU path for agents and keeps
the map as a basemap, and nobody animates a large fleet through a
re-parsed-per-frame document format. VISION.md's use case 1 (compelling
congestion reveals in a browser episode) is what puts us on the web side.

## SUMO sumo-gui

- Same C++ application as the headless sim plus a GUI built on the **FOX
  toolkit with an OpenGL canvas** (confirmed incidentally in
  [sumo#15082](https://github.com/eclipse-sumo/sumo/issues/15082): view
  creation time in `GUIGlChildWindow.cpp`). Renders vehicles as
  triangles/boxes/"simple shapes"/raster bitmaps, with ~35 vehicle coloring
  schemes (speed, waiting time, emissions, angle…) and ~40 lane coloring
  schemes (occupancy, average speed, relative speed, travel time, live
  edgeData) — the native-app equivalent of our data-driven `line-color`
  congestion casing. Interactive: track vehicles, close lanes, switch signal
  programs, scale traffic live. A "delay" control inserts ms between steps
  ("By default the delay is set to 0 … the simulation can run too fast to
  see any vehicles").
- Performance: rendering speed was enough of a pain point to earn "Major
  improvement in rendering speed" in the 2019 changelog
  ([Changes in 2019 releases](https://sumo.dlr.de/docs/ChangeLog/Changes_in_2019_releases.html));
  users still report GUI stepping slowdowns on OSM-scale networks
  ([sumo#3866](https://github.com/eclipse-sumo/sumo/issues/3866)).
  Simplification knobs (constant-size-when-zoomed-out, exaggeration, shape
  scheme downgrade) are their LOD answer.
- **vs this project:** sumo-gui proves the *visual grammar* we want (vehicles
  colored by state, edges colored by live metrics, click-to-inspect) but is
  native-only, single-machine, and visually dated — unusable for the
  math-vs-vibes episode. Its ~40 lane-coloring schemes are a checklist our
  metric-to-style mapping should cover. Source:
  [sumo-gui docs](https://sumo.dlr.de/docs/sumo-gui.html).

## MATSim OTFVis

- Java visualizer "designed to support actual visualization of live
  simulation runs", built on **JOGL** with an OpenGL scene graph
  (MATSim book, OTFVis chapter,
  [PDF](https://ubiquitypress.com/en/chapters/33/files/f85712a1-c7d1-4784-b607-93b600b4b88f.pdf);
  [OTFVis README](https://github.com/matsim-org/matsim-libs/blob/master/contribs/otfvis/README.md)).
  The architecture is the lesson: writer/reader pairs serialize mobsim state
  into a shared byte buffer; a **quadtree does spatial reduction** ("only the
  smallest set of data necessary to display the visible sector of the network
  is transferred"); live mode capped at "about 30 frames/updates per second";
  claims "millions of agents can be displayed in real time" with GPU. Also
  records MVI movie files for post-mortem playback.
- **vs this project:** OTFVis is the closest philosophical prior — a
  *streaming* agent-state channel with interest management (the quadtree)
  between sim and renderer — but Java-desktop and MATSim-coupled. Its
  quadtree idea is our [[arch-state-authority]] area-of-interest fan-out;
  its ~30 fps cap validates our 10 Hz snapshot + client-side interpolation
  (their 33 ms frames are why they need no interpolation; our 100 ms
  snapshots are why we do).

## Simunto VIA

- The de-facto MATSim visualizer: proprietary commercial desktop app (Java,
  Win/macOS/Linux) by Simunto GmbH. Animated vehicle playback, link
  aggregation, network coloring by metric, map backgrounds, movie recorder,
  PT/emissions plugins; "heavily optimized to support large scenarios"
  ([simunto.com/via](https://simunto.com/via/)). Licensing: "A free license
  is available for up to 500 MATSim agents"
  ([MATSim book](https://www.matsim.org/files/book/partOne-latest.pdf):
  "Via is commercial").
- **vs this project:** VIA shows what polished post-hoc MATSim analysis looks
  like (link aggregation + movie export is the workflow our replay +
  before/after reveal must match) and shows the ceiling of the commercial
  desktop model: no live bus, no browser, no open source. Our
  metrics-as-a-stream heatmap is the differentiator
  ([[domain-congestion-metrics]]).

## A/B Street

- Rust traffic sandbox with a **custom 2D OpenGL renderer** ("`widgetry`: a
  GUI and 2D OpenGL rendering library, using glium + winit + glutin"), a
  WebAssembly browser build, and "`piggyback`: a small WebAssembly API to
  layer parts of A/B Street on top of Mapbox or other web maps"
  ([dev guide](https://a-b-street.github.io/docs/tech/dev/index.html)).
  Discrete-event sim; renders agents, signals, edits interactively at city
  scale.
- **vs this project:** proof a bespoke 2D GL renderer can handle city-scale
  agents — but they own the entire stack (no GIS basemap; `piggyback` was an
  afterthought shim). We get the same rendering headroom from
  MapLibre-native layers + deck.gl escalation without owning a renderer. Their
  choice validates the "own the GPU path for agents" conclusion while
  illustrating its cost.

## CARLA (Unreal-based)

- Photorealistic 3D via Unreal Engine (carla-ue5 Traffic Manager docs:
  [link](https://carla-ue5.readthedocs.io/en/latest/adv_traffic_manager/));
  agent counts are orders of magnitude smaller (pain reported at "2617+
  vehicles", [carla#3442](https://github.com/carla-simulator/carla/issues/3442)).
- **vs this project:** irrelevant for fleet-scale 2D viz; relevant only as
  the shape of a hypothetical future driver-view client, which ADR-0003's
  consequences already anticipate (additive NATS consumer, not a rewrite).
  VISION's non-goals explicitly exclude this fidelity class.

## Browser-based SUMO visualizers (the graveyard)

- **sumo-web3d** (Sidewalk Labs): "Web-based 3D visualization of SUMO
  microsimulations using TraCI and three.js", TypeScript + React,
  pip-installable — **archived 2023-04-14**
  ([GitHub](https://github.com/sidewalklabs/sumo-web3d)).
- **sumo3Dviz**: lightweight open-source 3D pipeline converting SUMO outputs
  ([arXiv](https://arxiv.org/html/2604.19194v1)); **SUMO2Unity**:
  SUMO↔Unity bridge ([GitHub](https://github.com/SimuTraffX-Lab/SUMO2Unity)).
  Eclipse's OSM Web Wizard is a scenario *builder*, not a viz engine.
- **vs this project:** everyone who gave SUMO a web client reached for a GPU
  scene layer (three.js/Unity) over a thin state stream (TraCI), and none
  survived maintenance — the per-project bespoke webgl client is a known
  mortality zone. Lesson for us: the durable move is to stand on *maintained*
  rendering infrastructure (MapLibre, deck.gl) and keep our own code to data
  plumbing; the stream contract (NATS subjects) is the part we own long-term.

## deck.gl (the alternative stack we may escalate into)

- Framework-agnostic WebGL2/WebGPU framework (vis.gl; originated at Uber, now
  largely CARTO-maintained); v9.3 current (2026-04). Instanced primitive
  layers fed by typed-array attributes; documented 60 FPS at ~1M items for
  basic layers; `MapboxOverlay` integrates with MapLibre in overlaid or
  interleaved (shared WebGL2 context) modes
  ([performance](https://deck.gl/docs/developer-guide/performance),
  [using with MapLibre](https://deck.gl/docs/developer-guide/base-maps/using-with-maplibre)).
  Notable trap: naive per-frame `data` churn stutters "even for layers with
  just a few thousand items" — the binary-attribute discipline is the real
  adoption cost.
- **vs this project:** not a competitor but the pre-approved escalation
  (ADR-0003). The comparison that matters is *when*: MapLibre-native wins on
  dependencies (zero), basemap integration (native), and picking
  (`queryRenderedFeatures` for free); deck.gl wins on moving-feature ceiling
  (~1M instanced vs low-thousands through the GeoJSON pipeline) and binary
  data paths. The synthesis ladder defines the crossover.

## kepler.gl

- "A data-agnostic, high-performance web-based application for visual
  exploration of large-scale geolocation data sets" — a full React + Redux
  *application* on deck.gl ([docs](https://docs.kepler.gl/)). Its **Trip
  layer** animates GeoJSON LineStrings whose coordinates carry a 4th element
  `[lon, lat, alt, timestamp]`, with trail-length slider, animation speed
  control, and combined time ranges across layers
  ([trip layer docs](https://docs.kepler.gl/docs/user-guides/c-types-of-layers/k-trip)).
- **vs this project:** adopting it means taking React *and* Redux — a direct
  ADR-0003 violation. But the Trip layer is the reference UX for our replay
  mode (recorded run → trips → time scrubber), and the
  timestamp-as-4th-coordinate trick is a compact wire-format idea. Steal the
  UX, not the dependency.

## streetscape.gl / XVIZ (closest prior art, unmaintained)

- Uber's AVS = **XVIZ** (protocol) + **streetscape.gl** (React web toolkit on
  deck.gl), open-sourced Feb 2019
  ([Uber blog](https://www.uber.com/en-US/blog/avs-autonomous-vehicle-visualization/)).
  Claimed scale: "real-time playback and smooth interaction with scenes
  supporting **hundreds of thousands of geometries**." XVIZ shape: world
  state split into *streams* ("a sequence of timestamped datums of the same
  type", path-like names `/vehicle/velocity`); *timeslices* = synchronized
  snapshots across streams; objects linked by `id` across streams (the
  conventions doc explicitly calls "a stream per object" **bad** — it
  "provides no cross stream object linking"); a metadata message describes
  streams and
  stylesheets up front; sources can be logs or **live sockets**; encoding
  JSON or binary GLB
  ([XVIZ concepts](https://github.com/uber/xviz/blob/master/docs/overview/concepts.md),
  [conventions](https://github.com/uber/xviz/blob/master/docs/overview/conventions.md)).
- Maintenance: repos moved to `aurora-opensource` after Uber ATG's
  acquisition; streetscape.gl's GitHub releases end at v1.0.1 (Oct 2019) and
  its npm packages at 1.0.13 (Jun 2022) — effectively unmaintained since
  ~2022 ([npm](https://www.npmjs.com/~twojtasz-aurora),
  [releases](https://github.com/aurora-opensource/streetscape.gl/releases)).
  React-based — ADR-0003 conflict regardless.
- **vs this project:** XVIZ is essentially "subjects + snapshot timeslices +
  typed streams + ids" with a deck.gl renderer — i.e., our
  [[arch-nats-backbone]] taxonomy reinvented four years earlier for the same
  rendering stack. **Mine the protocol design (stream/timeslice/id/metadata
  separation, binary-over-socket), don't adopt the code.** Its death is also
  a data point: a viz protocol tied to one company's AV program died with
  the program; ours is tied to open infra (NATS) and open sim concepts.

## Production fleet maps on our exact stack

- **HDTP** (Codeando México): a GTFS-Realtime visualizer that "combines
  MapLibre with deck.gl to create an interactive map … efficiently displaying
  live vehicle positions" ([MapLibre newsletter, April 2026](https://maplibre.org/news/2026-05-02-maplibre-newsletter-april-2026/)).
  Carris Metropolitana ([GitHub](https://github.com/carrismetropolitana/website))
  runs a large live-vehicle map on MapLibre alone. opensky-network's live
  aircraft map was the motivating case for MapLibre's `updateData`
  ([issue #1236](https://github.com/maplibre/maplibre-gl-js/issues/1236)).
- **vs this project:** existence proofs at both rungs — MapLibre-only for
  transit-scale fleets, MapLibre+deck.gl when the moving layer outgrows the
  GeoJSON pipeline. GTFS-RT fleets update at ~15–60 s, though; our 10 Hz
  × thousands is a harder rate than any cited deployment, which is why the
  escalation criteria must come from our own microbenchmark.

## Positioning Summary

| Dimension | sumo-gui | OTFVis | VIA | A/B Street | kepler.gl | streetscape.gl/XVIZ | us (planned) |
|---|---|---|---|---|---|---|---|
| Renders | agents + edge colors | agents (live stream) | agents + link metrics | agents + edits | trips/points | AV scene streams | vehicles + segment heat |
| Stack | FOX + OpenGL | JOGL scene graph | Java desktop | Rust glium + wasm | React+Redux+deck.gl | React+deck.gl | **MapLibre + vanilla TS → deck.gl** |
| Basemap | none | map layers | map backgrounds | own / Mapbox shim | Mapbox | Mapbox | **OSM/MapLibre native** |
| Live bus | no (in-proc) | in-proc byte buffer | no (files) | no (in-proc) | no (files) | sockets (dead) | **NATS WebSocket** |
| Scale claim | struggles on OSM nets | "millions of agents" | large scenarios | city-scale | millions of points | 100k+ geometries | **~1M ceiling (deck.gl rung)** |
| Browser | no | no | no | wasm build | yes | yes (dead) | **yes, first-class** |
| Open source | GPL | yes (MATSim) | **no** | Apache-2.0 | MIT | Apache-2.0 (unmaintained) | **yes** |
| Our takeaway | lane-color scheme checklist | interest management, 30 fps cap | replay/movie workflow | owning a renderer is viable but costly | replay UX to steal | protocol design to mine | — |
