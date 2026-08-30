# Synthesis: integration-3d-hero-viz

> Researched: 2026-08-24 | Git HEAD: 2bc98de | Status: complete

A rung-2 3D renderer for baked replays: a **standalone three.js page** in
the existing vite multi-page build, consuming the same frame interface the
MapLibre app consumes, for hero intersection views, reproducible cool
videos, and the quiz's adjust-timing → submit → score → watch loop.
MapLibre stays the live/analytical renderer; the 3D page is baked-only and
hero/quiz-scoped, bounded by an ADR-0003 addendum.

## Summary of findings

1. **The data plane is already renderer-agnostic.** The baked shim
   re-encodes TSRB chunks into synthetic TSSF v1 bytes so "the render loop
   runs byte-for-byte untouched" (`viz/src/tsrb.ts:2-5`); tssf.ts,
   snapshots.ts, tssg.ts, signals.ts, artic.ts and theme.ts are all
   DOM-free, MapLibre-free modules. A three.js page is a new render target
   behind `subscribeBaked`'s exact seam — no pipeline change
   ([implementation.md](./implementation.md) §0–§4).
2. **The world is metric and flat — which simplifies everything.** TSSF/
   TSRB x/y are a local metric frame, north-up, +x east
   (`viz/src/tssf.ts:8-10`); a three.js scene can live in it directly and
   skip proj.ts's UTM math entirely. But there is no z/elevation anywhere:
   lane shapes are 2D (`contracts/network-format-v1.md:73`), the GeoJSON
   property block has no level field (`engine/geojson.go:28-37`), and
   netimport extracts no bridge/layer tags. The Chicago L is rail, not
   road — out of the network entirely. Flat ground is v1 by necessity, not
   by taste (implementation.md §10 G1).
3. **three.js is the right renderer; deck.gl answers a different
   question.** deck.gl is geo-visualization layering — the sanctioned
   escalation for *vehicle count on the map* (ADR-0003), not a scene
   engine for modeled junctions; Babylon/PlayCanvas are engines whose
   extras (ECS, editor, GUI) we would not use. npm-verified versions:
   three 0.185.1, @deck.gl/core 9.3.10, @babylonjs/core 9.22.2, playcanvas
   2.21.4 ([competitors.md](./competitors.md)).
4. **Deterministic capture is solved — and we already own the harness.**
   Chrome's `HeadlessExperimental.beginFrame` + virtual time gives
   byte-identical frame sequences (puppeteer-capture's model), and the
   repo already drives headless Chrome over raw CDP with zero npm deps
   (`viz/scripts/screenshot.mjs:22-35`). Its recorded virtual-time lesson
   (races MapLibre workers + wall-clock stream) does not apply to a
   MapLibre-free three.js page fed by a tick-stepped source — the
   strongest technical argument for the standalone-page shape
   ([standards-and-patterns.md](./standards-and-patterns.md) §3).
5. **The quiz loop's missing half is server-side, not client-side.** The
   quiz page is static with precomputed numbers
   (`scripts/chicago/mkquiz.py:7-17`); demosrv's HTTP surface has no
   scenario-mutation/ad-hoc-run endpoint (`engine/cmd/demosrv/main.go:399-427`);
   baked artifacts carry no mutable signal table (ADR-0023 §10). The
   engine mutation channel exists (ADR-0037 `signal_set`
   `{signal, phase, hold_ticks}`); the orchestration does not
   (implementation.md §8).
6. **The presentation references all point one way**: legibility from
   true geometry + palette discipline + soft light + miniature
   photography (A/B Street, Cities:Skylines tilt-shift, Mini Motorways/
   Traffix), explicitly *not* photorealism — which keeps the page inside
   VISION.md:98's non-goal ("photorealistic 3D…") without contortion
   (competitors.md).
7. **Performance is a non-issue at this shape**: ~10² vehicles, one
   junction, merged static geometry + one InstancedMesh per class → tens
   of draw calls; the only real cost is capture-time rasterization
   (standards-and-patterns.md §6).

## Key decision candidates

### D1 — three.js standalone page as rung 2 (vs deck.gl interleave)

- **Choice:** a new vite page (`hero.html` + `src/hero-main.ts`, one more
  `input` in `viz/vite.config.ts:10-19`) with `three` as a page-scoped
  dependency. deck.gl stays reserved for its ADR-0003 role (map-scale
  escalation); no interleave.
- **Why:** the hero viz escapes MapLibre's *dimensionality*, not its
  vehicle-count ceiling — deck.gl interleave would inherit map-shaped
  cameras and a layer system while buying nothing for modeled heads, GLB
  fleets, or cinematic capture. three.js is a library (ADR-0003-compatible
  posture), ~0.17 MB gzip on one page, and the ecosystem default for this
  exact query shape. The standalone page also keeps MapLibre's worker
  pool out of the capture path (finding 4).
- **Trade-off:** a second rendering stack to maintain (but page-isolated;
  the map never loads it); `three` dependency needs the AGENTS.md
  justification block, mirroring ADR-0023's pmtiles precedent
  (`docs/kb/decisions/ADR-0023-baked-replay-pipeline.md:470-474`).
- **Field context:** [competitors.md](./competitors.md) (full table);
  ADR-0003 already contemplates "a future Three.js driver-view client… an
  additive new consumer, not a rewrite" (`docs/kb/decisions/ADR-0003-maplibre-vis.md:25-26`).

### D2 — consume baked.ts's frame interface; no pipeline change

- **Choice:** the 3D page imports `loadBakedIndex`, `decodeTsrbChunk`/
  `encodeTssf`, `SnapshotBuffer`, `tssg.ts`, `signals.ts` (or baked
  furniture), `artic.ts`, `theme.ts` verbatim. Two consumption shapes:
  (a) interactive page → reuse `subscribeBaked`/`BakedSession`, feeding
  `setViewport` the scene bbox at z≥13 (the gate constant is exported,
  `viz/src/baked.ts:44`); (b) capture/quiz driver → a tick-stepped
  scheduler over `decodeTsrbChunk` directly (seek(t) → render → grab),
  reusing `chunkForTick`'s window logic (`viz/src/baked.ts:335-341`).
- **Why:** the shim's design goal was byte-level reuse
  (`viz/src/baked.ts:10-13`); every downstream module is DOM-free and
  node-testable, matching the repo's test convention. No engine, bake,
  or contract change — ADR-0006's contract surface is untouched.
- **Trade-off:** shape (a) drags the region-barrier/stall machinery into
  a context where everything is local; shape (b) re-implements ~100 lines
  of chunk scheduling. Both are small; standardizing on (b) for anything
  driven programmatically keeps determinism obvious.
- **Field context:** [implementation.md](./implementation.md) §2, §10 G4.

### D3 — deterministic camera-path format keyed to sim tick

- **Choice:** a versioned JSON clip document: `{version, bake, tickStart,
  tickEnd, fps, camera: {mode, keyframes: [{tick, pos, target, fov?,
  ease?}]}}`, stored next to the bake or in the demo registry. Modes named
  after the domain taxonomy: eulerian (fixed hero), lagrangian (follow
  vehicle id), cinematic (keyframed path), interactive (orbit). Evaluated
  against the same interpolated sim clock the vehicles use.
- **Why:** our frames are a pure function of (bake, tick) — a tick-keyed
  camera makes clips bit-reproducible, diffable, and reviewable like
  every other artifact in the repo (scenario manifests are the cultural
  template). sumo3Dviz validates the four-mode taxonomy for traffic
  ([arXiv 2604.19194 §3.3](https://arxiv.org/html/2604.19194v1)).
- **Trade-off:** a micro-format to spec and test (it belongs in
  `contracts/` if it crosses the engine↔viz or site↔tool boundary; page-
  local if only the hero page reads it — lean page-local first).
- **Field context:** [standards-and-patterns.md](./standards-and-patterns.md) §2.

### D4 — capture pipeline: in-repo CDP beginFrame, not a recorder dependency

- **Choice:** extend the `viz/scripts/screenshot.mjs` zero-dep CDP pattern
  with `--deterministic-mode --enable-begin-frame-control` and a
  beginFrame loop: load `hero.html?bake=…&clip=…&capture=1`, step ticks,
  grab frames, pipe to ffmpeg. puppeteer-capture (npm 1.58.0) is the
  off-the-shelf equivalent — declined: puppeteer dependency, Linux/
  Windows only (no macOS), chrome-headless-shell requirement, while the
  CDP primitives it wraps are ~200 lines against the harness we own.
- **Why:** byte-identical clips (same bake + clip doc ⇒ same video),
  no new dependency class, and the MapLibre-free page removes the two
  virtual-time races screenshot.mjs documented (`viz/scripts/screenshot.mjs:3-6`).
- **Trade-off:** we own the Chrome-flag quirks (beginFrame is an
  experimental CDP domain; version-pinned Chrome in the capture
  environment is the mitigation — same posture as the pinned tippecanoe
  in ADR-0023 §1). MediaRecorder+captureStream stays available later as
  a *user-facing* "download this clip" nicety — nondeterministic but
  zero-infrastructure ([standards §3(b)](./standards-and-patterns.md)).
- **Field context:** [standards-and-patterns.md](./standards-and-patterns.md) §3;
  [puppeteer-capture design writeup](https://alexey-pelykh.com/blog/why-i-built-puppeteer-capture/).

### D5 — quiz interactivity: 3D scene + overlay controls → submission JSON

- **Choice:** click a modeled signal head (raycast) to select it; edit
  per-phase durations in a hand-rolled overlay panel (theme-matched DOM,
  not lil-gui — the quiz page and HUD are already hand-rolled); submit a
  JSON document in the TSSG program's shape (`{programId, phases:
  [{durationTicks}], baseBake}`). Server scores by running the sim with
  the edit — v1 scope: host-side (demosrv extension or a stage tool),
  not public-self-serve.
- **Why:** Traffix proves tap-the-signal is a legible verb; the TSSG-
  shaped payload avoids a presentation-side timing dialect (mkquiz's
  one-source-of-numbers rule, `scripts/chicago/mkquiz.py:7-11`); ADR-0037's
  `signal_set` is the engine channel for live injection, a scenario
  overlay the channel for fresh runs.
- **Trade-off:** the server half is genuinely new surface (demosrv has no
  mutation endpoint today, `engine/cmd/demosrv/main.go:399-427`), and a
  *public* self-serve loop collides with ADR-0020's parked-live-plane
  precondition — the episode-shaped answer is host-driven on stage, not
  guest-submitted from the internet. Watch-the-result has two honest
  options: live ws on stage, or a bake-on-demand turnaround (minutes).
- **Field context:** [implementation.md](./implementation.md) §8;
  [standards-and-patterns.md](./standards-and-patterns.md) §5.

### D6 — z/elevation: flat ground v1; grade separation deferred

- **Choice:** ground plane at z=0; signal heads on poles at fixed
  heights; buildings as flat extrusions of fixed/storey-estimated height
  when overlays exist. No bridge/layer modeling in v1.
- **Why:** the data does not exist — 2D lane shapes
  (`contracts/network-format-v1.md:73`), no elevation in the export
  property block (`engine/geojson.go:28-37`), no bridge/layer extraction
  in netimport, and the L structure is rail (separate PENDING topic,
  `docs/kb/INDEX.md:76`). A grade-separation pass is an importer +
  network-format change, correctly its own milestone.
- **Trade-off:** hero shots of the actual Loop can't show the L overhead;
  accepted — the first hero subjects are pods (bottleneck-town's
  four-signal arterial) and at-grade Chicago junctions.
- **Field context:** [implementation.md](./implementation.md) §10 G1.

### D7 — ADR-0003 addendum boundary: 3D for baked hero/quiz only

- **Choice:** the addendum says: three.js page is a *baked-replay hero/
  quiz renderer*; MapLibre remains the live and analytical renderer;
  no UI framework; the deck.gl escalation ladder (rung 0–3) is unchanged
  and still about the map's vehicle channel. Cite VISION.md:98 to show
  the non-goal is *photorealistic* 3D, which a stylized legibility-first
  renderer does not approach.
- **Why:** without the boundary sentence, "rung 2" forks in meaning (the
  map ladder's rung 2 is deck.gl; the topic registry uses "rung-2" for
  this 3D page, `docs/kb/INDEX.md:92-93`) — the addendum should fix the
  naming too (suggestion: this is the *hero track*, not a map rung).
- **Trade-off:** none material; one paragraph of ADR plus the `three`
  dependency justification.
- **Field context:** [implementation.md](./implementation.md) §7.

## Renderer comparison (from competitors.md)

| | three.js | deck.gl | Babylon.js | PlayCanvas |
|---|---|---|---|---|
| npm (verified 2026-08-24) | `three@0.185.1` | `@deck.gl/core@9.3.10` | `@babylonjs/core@9.22.2` | `playcanvas@2.21.4` |
| kind | render library | geo-viz layer system | full engine | engine + editor |
| modeled-junction scene graph | yes | no (map-shaped) | yes | yes |
| instancing fleet | InstancedMesh | instanced layers (binary-attr discipline) | built-in | built-in |
| camera cinematics | free 3D (splines, clips, OrbitControls) | map/View-constrained | free 3D | free 3D |
| capture fit (beginFrame, tick-stepped) | **best** (no workers, own canvas) | poor (interleaved w/ MapLibre) | good | good |
| picking for quiz | raycaster | re-implement | built-in | built-in |
| dep posture | 1 page-scoped dep | pre-sanctioned for map only | heavier justification | heavier justification |

## Open questions

- **sumo3Dviz's render cost** is printed as "100–500 s per frame"
  ([arXiv 2604.19194 §5](https://arxiv.org/html/2604.19194v1)) — almost
  certainly per *sequence*, not per frame, but unverified; only matters
  as a cautionary data point (offline CPU rendering vs our 60 fps WebGL).
- **Region coverage for hero scenes**: do the candidate hero bakes (pods,
  chishow cuts) fully cover the hero viewport at all capture ticks, or do
  boundary lanes straddle region edges in a way the tick-stepped consumer
  must union? (bases42x27k has 4 regions; needs a per-bake check at clip
  authoring time.)
- **Bake-on-demand latency for the quiz loop**: how long does a
  bottleneck-town-scale bake take end-to-end (re-sim + chunks + upload)?
  Determines whether "watch your timing run" is live-ws-only on stage or
  a minutes-scale bake turnaround. Unmeasured in the ADR-0023 size table
  (`docs/kb/decisions/ADR-0023-baked-replay-pipeline.md:538-570`).
- **Mutable-signal baked format**: if a scored run is to be *re-watched
  baked*, TSSG-per-run-static (ADR-0023 §10) forces either a fresh bake
  per submission or a TSRB v2 table-generation field. Which path is
  episode-shaped? (Leans fresh bake — content-keyed, no format work.)
- **Signal-head pose for left-hand traffic / divided approaches**:
  signals.ts's clustering is geometry-derived and US-shaped so far; the
  3D head placement inherits its bearing logic — verify on skewed
  Wilshire-style X-junctions before modeling pole positions.
- **Does `three`'s license/size clear the repo bar cleanly?** MIT
  ([github.com/mrdoob/three.js](https://github.com/mrdoob/three.js)) —
  yes in principle; the addendum's justification block should state the
  shipped-byte figure measured at build time, not a blog number.

## Connections

- **[integration-maplibre-realtime](../integration-maplibre-realtime/synthesis.md)** —
  the sibling renderer; escalation ladder rungs 0–3 (its decision 4) stay
  map-scoped; this topic's D7 fixes the "rung 2" naming fork. Shares the
  frame contract (its decision 5: binary SoA from day one — vindicated:
  the 3D page consumes the same TSSF bytes).
- **[integration-3d-vehicle-fleet](../integration-3d-vehicle-fleet/synthesis.md)** (PENDING) —
  the fleet production topic: GLB models, glTF pipeline, trademark
  constraints, agent-parallel production. This topic fixes the *consumer
  shape* the fleet targets: InstancedMesh per class, dims anchored to
  `viz/src/theme.ts:144-147` GLYPHS (engine-authoritative), palette from
  THEMES, meshopt compression, vertex-color/palette-texture art direction.
- **[domain-signal-control](../domain-signal-control/synthesis.md)** —
  the decisecond tick ↔ NEMA timer losslessness is why the quiz edit UX
  can speak in honest units; ADR-0037's `signal_set` verb is the engine
  channel a submission rides; TSSG program shape is the submission
  payload's model.
- **[concept-scenario-format](../concept-scenario-format/synthesis.md)** —
  a scored quiz submission realized as a fresh run is a kustomize-style
  overlay variant (the retime/green-wave arms already are); run identity
  (content-hash, seed) carries over to scored runs unchanged.
- **ADR-0023** (baked replay) — the entire data source;
  `contracts/baked-replay-v1.md` is the read contract. **ADR-0003** — the
  addendum this topic must produce. **ADR-0020** — why the public quiz
  loop can't be live-ws self-serve. **ADR-0037** — the mutation channel.
  **VISION.md:98** — the photorealism boundary that shapes the art
  direction.

## Source links (primary)

- three.js: <https://threejs.org/> · comparison: <https://www.utsubo.com/blog/threejs-vs-babylonjs-vs-playcanvas-comparison> · WebGPU status: <https://www.utsubo.com/blog/threejs-2026-what-changed>
- deck.gl: <https://deck.gl/docs/api-reference/geo-layers/trips-layer> · <https://deck.gl/docs/developer-guide/performance>
- Babylon: <https://blog.logrocket.com/three-js-vs-babylon-js/> · PlayCanvas: <https://playcanvas.com/products/engine> · engine framing: <https://app.cinevva.com/blog/2026-06-08-why-we-built-our-own-webgpu-engine>
- glTF: <https://registry.khronos.org/glTF/specs/2.0/glTF-2.0.html> · ISO: <https://80.lv/articles/khronos-gltf-2-0-becomes-an-iso-iec-international-standard> · meshopt: <https://meshoptimizer.org/gltf/> · <https://gltf-transform.dev/modules/extensions/classes/EXTMeshoptCompression>
- Capture: <https://alexey-pelykh.com/blog/why-i-built-puppeteer-capture/> · <https://github.com/alexey-pelykh/puppeteer-capture> · <https://github.com/w3c/mediacapture-record/issues/213> · <https://github.com/stackgl/headless-gl>
- References: <https://github.com/a-b-street/abstreet> · <https://arxiv.org/html/2604.19194v1> · <https://www.gamespew.com/2021/01/traffic-management-game-traffix-is-challenging-and-fun/> · <https://en.wikipedia.org/wiki/Tilt%E2%80%93shift_photography>
- Camera: <https://github.com/yomotsu/camera-controls> · <https://www.deptagency.com/en-nl/insight/coding-a-cinematic-camera-path/>
