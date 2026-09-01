# Competitors: renderers and reference presentations for the 3D hero viz

> Source: web research (npm registry verified 2026-08-24) | Researched: 2026-08-24 | Git HEAD: 2bc98de

Our exact shape: a small scene (one junction + approaches), ~10² moving
entities, stylized low-poly (not photoreal), orbit + cinematic camera,
offscreen deterministic capture, plus in-scene picking for the quiz's
signal-timing interaction. Each entry: strengths/weaknesses *for this use*,
against this project's constraints (vanilla TS per ADR-0003, dependency
justification rule, determinism ethos, baked-replay data plane).

## Renderer candidates

### three.js — the topic's working assumption

- <https://threejs.org/> · <https://github.com/mrdoob/three.js> · npm
  `three@0.185.1` (verified 2026-08-24; ~23 MB unpacked package, of which
  the shipped runtime is a fraction — a widely-cited figure is ~168 kB
  gzipped for the core ([utsubo comparison](https://www.utsubo.com/blog/threejs-vs-babylonjs-vs-playcanvas-comparison))).
- **Strengths for us.** A rendering *library*, not an engine: scene graph,
  materials, loaders, camera, and it "gets out of your way… you build
  everything above the renderer yourself, but nothing fights you" — the
  [Cinevva engine postmortem](https://app.cinevva.com/blog/2026-06-08-why-we-built-our-own-webgpu-engine)
  's exact framing, which matches ADR-0003's no-framework posture (three.js
  is a dependency, not a framework; no component model, no app lifecycle).
  `InstancedMesh` covers the vehicle fleet in one draw call per class;
  `GLTFLoader`/`DRACOLoader`/`MeshoptDecoder` are first-party;
  `OrbitControls` covers the interactive camera; raycasting for picking
  signal heads is built in. Largest community and example corpus — most
  "how do I X in WebGL" answers on the web are three.js-shaped. TypeScript
  types are bundled. WebGPU renderer production-ready since r171
  (Sept 2025) with automatic WebGL2 fallback
  ([utsubo migration guide](https://www.utsubo.com/blog/webgpu-threejs-migration-guide),
  [2026 changes](https://www.utsubo.com/blog/threejs-2026-what-changed)) —
  irrelevant for v1 (WebGL2 is plenty) but means the dependency is on the
  maintained mainline of web 3D.
- **Weaknesses for us.** No engine conveniences we get free elsewhere:
  no entity system (we have our own data plane anyway — a non-loss), no
  built-in video export, shadow/post tuning is manual. Version churn is
  fast (r185 in Aug 2026); pin exactly, as the repo does for maplibre-gl
  (exact `5.24.0` in `viz/package.json:18`).
- **Fit.** The default choice and the one the topic registry already names
  (`docs/kb/.kb-meta.json:255`). Bundle cost lands on one new vite page
  only; the MapLibre app never pays it.

### deck.gl — the KB's pre-sanctioned escalation rung

- <https://deck.gl/> · npm `@deck.gl/core@9.3.10` (verified 2026-08-24) ·
  [TripsLayer docs](https://deck.gl/docs/api-reference/geo-layers/trips-layer) ·
  [performance guide](https://deck.gl/docs/developer-guide/performance)
  (~1M instanced items at 60 FPS documented).
- **Strengths for us.** Already ADR-0003's approved escalation *for the
  MapLibre app* (`docs/kb/decisions/ADR-0003-maplibre-vis.md:18-20`), so
  adopting it for the map needs no new ADR. `MapboxOverlay` interleaves
  with MapLibre (one WebGL context), and TripsLayer renders timestamped
  trajectories with a `currentTime` scrub — a genuine fit for
  "moving dots over a basemap at city scale".
- **Weaknesses for us.** deck.gl is a **geo-visualization** layer system,
  not a 3D scene engine: its camera model is map-shaped (MapView +
  mercator), and while it has FirstPersonView/OrbitView, the moment the
  scene is "modeled signal heads, low-poly vehicles with trailers,
  emissive materials, shadow-mapped ground, GLB assets, cinematic camera
  splines" you are re-implementing three.js inside a layer system whose
  center of gravity is elsewhere. Prior KB measurement also flags the
  discipline cost: naive deck.gl stutters at a few thousand items if the
  `data` prop churns; binary-attribute + `dataComparator` discipline is
  mandatory (`docs/kb/raw/integration-maplibre-realtime/synthesis.md:114-139`),
  and spawn/despawn breaks GPU transitions (stable fleet ordering
  required, same cite). It also does not answer the capture or picking
  stories better than three.js (picking must be re-implemented either way;
  MapLibre's `queryRenderedFeatures` is lost for deck layers — same cite).
- **Fit.** Wrong axis for the hero viz: deck.gl answers "more vehicles on
  the map" (rung 2 of the *map* ladder), not "a modeled 3D junction". Keep
  it reserved for its sanctioned role; do not interleave for hero scenes.

### Babylon.js

- <https://www.babylonjs.com/> · npm `@babylonjs/core@9.22.2` (verified
  2026-08-24) · comparisons: [LogRocket](https://blog.logrocket.com/three-js-vs-babylon-js/),
  [dev.to 360° comparison](https://dev.to/devin-rosario/babylonjs-vs-threejs-the-360deg-technical-comparison-for-production-workloads-2fn6).
- **Strengths for us.** A full engine: built-in scene optimization,
  instrumentation, GUI system, node material editor, strong glTF support,
  arguably better out-of-the-box performance defaults
  ([javascript.plainenglish.io comparison](https://javascript.plainenglish.io/babylon-js-vs-three-js-which-should-you-choose-14faef9f7d78)).
  Microsoft-backed, stable release train.
- **Weaknesses for us.** Heavier (the 360° comparison puts three.js at
  roughly half the gzipped size of Babylon's core) and more *opinionated*:
  engine-shaped APIs (scene optimizer, asset manager, observables) pull
  against the repo's vanilla-TS, library-not-framework posture. Smaller
  community/example corpus than three.js for exactly our query shape
  ("instanced low-poly traffic"). Nothing we need that three.js lacks at
  this scene size.
- **Fit.** Capable but buys nothing here; the weight and the engine-shaped
  mental model are costs without a payoff at 10² entities.

### PlayCanvas

- <https://playcanvas.com/> · [engine page](https://playcanvas.com/products/engine) ·
  npm `playcanvas@2.21.4` (verified 2026-08-24) · MIT, WebGL+WebGPU.
- **Strengths for us.** High-performance open-source engine with an
  editor, glTF-native, early WebGPU/Gaussian-splat adopter
  ([utsubo comparison](https://www.utsubo.com/blog/threejs-vs-babylonjs-vs-playcanvas-comparison)).
  Engine-only consumption (no editor) is supported.
- **Weaknesses for us.** Its value is the editor + entity-component
  workflow for game teams — we would use neither (our "scene" is
  data-generated per bake, not authored in an editor). Smaller community
  than three.js; using it library-style is possible but off its beaten
  path (the [Cinevva postmortem](https://app.cinevva.com/blog/2026-06-08-why-we-built-our-own-webgpu-engine)
  describes the friction of engine-shaped tools when you only want a
  renderer).
- **Fit.** Solves problems we do not have.

## Reference presentations (what to steal for LEGIBILITY, not photorealism)

### A/B Street — the legibility gold standard for sim-as-communication

- <https://github.com/a-b-street/abstreet> ·
  [docs](https://a-b-street.github.io/docs/software/abstreet.html) ·
  [3-year retrospective](https://a-b-street.github.io/docs/project/history/retrospective/index.html).
- What it does: 2D, OSM-based, and *"represents the physical geometry of
  roads and intersections"* — lane polygons, intersection interiors, bus/
  bike/turn/parking lanes, transit stops, signals — with agents rendered
  as colored bodies moving through that true geometry. Intersection
  geometry is first-class (they documented modeling intersection interiors
  rather than collapsing junctions to points).
- Status note (verified 2026-08-24): the project wound down in 2025 into
  successor tools by A/B Street Ltd (Network Planning Workspace, LTN tool,
  od2net, …) — the repo stands as the reference implementation, not an
  active competitor.
- **Steal:** (1) legibility comes from *true geometry + restrained
  palette*, not realism; (2) intersection interiors drawn honestly (the
  conflict zone is where the story is — our compiled internal lanes,
  network-format v1, already give us this geometry); (3) the sim itself is
  the communication artifact (their game framing ≈ our quiz framing —
  they shipped a traffic-management game to teach; we ship a guess-the-
  upgrade quiz).

### SUMO gui and the SUMO 3D ecosystem — the incumbent's cautionary tale

- [Eclipse SUMO](https://eclipse.dev/sumo/) ·
  [sumo3Dviz paper (arXiv 2604.19194, ETH Zürich, Apr 2026)](https://arxiv.org/html/2604.19194v1) ·
  [sumo3Dviz GitHub](https://github.com/DerKevinRiehl/sumo3dviz/).
- sumo-gui: tightly integrated, lightweight, 2D; "restricted camera
  control… minimal visual realism… unsuitable for human-centred user
  studies or cinematic video generation" (sumo3Dviz §2.1). The ecosystem's
  answers are heavyweight: Sumonity/Sumo2Unity/Sumo2Unreal game-engine
  pipelines ("substantial engineering effort… proprietary software…
  reproducibility and portability challenging", §2.3) or CARLA
  co-simulation ("computationally resource-intensive… exceed the
  requirements of most traffic visualisation tasks", §2.3).
- sumo3Dviz is the closest existing thing to our hero viz: pip-installable
  Python, consumes trajectory + signal logs, four camera modes —
  **Eulerian** (fixed external), **Lagrangian** (ego-follow),
  **Cinematic** (predefined 3D camera trajectory), **Interactive** —
  YAML-configured, batch video rendering, three signal-head designs
  (2-lens, 3-lens with auto-interpolated yellow, countdown), 10 car
  models. Its trajectory smoothing is a documented recipe: angle unwrap →
  linear interp → centred moving average (L=21 at 25 Hz) →
  velocity-derived orientation re-estimate (§4.2). Reported render cost:
  "100–500 s per frame … after an initial loading procedure … 20 s" (§5)
  — as printed; almost certainly means per *video sequence* rather than
  per frame, but either way it is an offline CPU-style renderer, not an
  interactive one (flagged as an open question in synthesis).
- **Steal:** the four-mode camera taxonomy (it maps exactly onto our
  hero/interactive/capture needs) and the offline-render-from-logs
  posture. **Avoid:** Python/CPU offline rendering — our baked plane
  already delivers frames to a browser at interactive rates; a WebGL
  renderer gets the same legibility at 60 fps instead of minutes per clip.

### Cities: Skylines-style presentation — the miniature illusion

- What to steal is the *photography*, not the game: shallow depth of
  field + elevated oblique camera + slightly desaturated ground with
  saturated agents reads as a "model world" (tilt-shift/miniature
  faking — [tilt-shift photography, Wikipedia](https://en.wikipedia.org/wiki/Tilt%E2%80%93shift_photography)).
  The look is achievable in three.js with one post pass (bokeh DoF) or
  even a cheap gradient-blur vignette, and it flatters low-poly assets:
  flat-shaded geometry + soft shadows + DoF reads "crafted miniature"
  rather than "missing detail".
- **Steal:** DoF as a legibility tool (focus pulls the eye to the
  junction box), desaturated static context vs saturated moving agents
  (mirrors our theme.ts discipline: "the traffic must stay the loudest
  thing on the map", `viz/src/theme.ts:26-28`), and the elevated-oblique
  default camera (≈35–50° pitch) rather than a driver-POV default.

### Traffix / Mini Motorways — the quiz's UX precedent

- [Traffix (Infinity Games)](https://www.gamespew.com/2021/01/traffic-management-game-traffix-is-challenging-and-fun/),
  [Nintendo Everything coverage](https://nintendoeverything.com/traffic-puzzle-game-traffix-hitting-switch-next-week/);
  [Mini Motorways review](https://thirdcoastreview.com/games-tech/2021/07/19/review-mini-motorways).
- Traffix's entire mechanic is *tap the traffic light to change it* —
  proof that direct in-scene signal manipulation is a legible, shippable
  interaction (our quiz: click a signal head in the 3D scene → adjust its
  timing). Mini Motorways proves the minimalist aesthetic (flat colors,
  tiny vehicles, no textures) sustains a commercial traffic game with "a
  crisp art style and a finely tuned user interface".
- **Steal:** one interaction verb on the signal head itself; no
  chrome-heavy control panels for the primary action.

## Summary table

| candidate | kind | npm (verified 2026-08-24) | fit for one-junction stylized hero | fit for quiz picking | capture story | ADR posture |
|---|---|---|---|---|---|---|
| three.js | render library | `three@0.185.1` | **exact fit** (InstancedMesh, GLB, OrbitControls, raycast) | raycasting built in | any (canvas is yours) | needs addendum + dep justification |
| deck.gl | geo-viz layers | `@deck.gl/core@9.3.10` | wrong axis (map-shaped cameras, no scene-graph modeling) | re-implement picking | same as three (own canvas) but fights the map model | pre-sanctioned **for the map only** |
| Babylon.js | full engine | `@babylonjs/core@9.22.2` | works, ~2× weight, engine-shaped | built-in picking/actions | built-in tools exist | heavier justification burden |
| PlayCanvas | engine+editor | `playcanvas@2.21.4` | works; editor/ECS value unused | entity picking | own canvas | heavier justification burden |

**Bottom line:** three.js as a standalone vite page; deck.gl stays in its
ADR-0003 lane (map-scale escalation, a different problem); Babylon and
PlayCanvas solve engine problems this scene does not have. Presentation
references converge on the same art direction: true geometry + palette
discipline + soft light + miniature photography — legibility over
photorealism, which is also the VISION.md:98 boundary.
