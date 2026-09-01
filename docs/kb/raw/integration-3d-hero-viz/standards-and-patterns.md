# Standards and patterns for the 3D hero viz

> Source: web research + codebase cross-references | Researched: 2026-08-24 | Git HEAD: 2bc98de

Each pattern is stated against our implementation: a three.js page consuming
baked-replay frames (implementation.md), deterministic by construction, with
an eye on reproducible clip capture and the quiz interaction.

## 1. Model format: glTF/GLB, and how to compress it

- **glTF 2.0 is the settled standard** — released as **ISO/IEC
  12113:2022** (Aug 2022), Khronos's "JPEG of 3D"
  ([80.lv on the ISO ratification](https://80.lv/articles/khronos-gltf-2-0-becomes-an-iso-iec-international-standard),
  [Khronos press release](https://www.khronos.org/news/press/khronos-gltf-2.0-released-as-isoiec-international-standard)).
  Spec: <https://registry.khronos.org/glTF/specs/2.0/glTF-2.0.html>.
  Why it won: runtime-delivery-shaped (GPU-ready buffers, PBR materials,
  scene graph), importer support everywhere (three.js `GLTFLoader`,
  Babylon, PlayCanvas, Blender export). For us: the vehicle fleet and
  signal-head/furniture models are GLB files, content-hashed under the
  same immutable-object posture as the bake chunks
  (`contracts/baked-replay-v1.md:102-124`).
- **Compression: meshopt over Draco for this shape.** gltfpack
  ([meshoptimizer.org/gltf](https://meshoptimizer.org/gltf/)) quantizes +
  meshopt-compresses; the gltf-transform docs note *"Meshopt decoding is
  considerably faster than Draco decoding"*
  ([EXTMeshoptCompression](https://gltf-transform.dev/modules/extensions/classes/EXTMeshoptCompression))
  and three.js decodes it via a small WASM `MeshoptDecoder`. Draco
  ([github.com/google/draco](https://github.com/google/draco)) wins
  slightly on ratio for big meshes but ships a larger decoder and slower
  decode — its sweet spot is megabyte meshes, not a fleet of 1–5 KB
  low-poly cars. npm (verified 2026-08-24): `gltfpack@1.2.0`,
  `@gltf-transform/cli@4.4.2` (the authoring-side optimization pipeline:
  prune/dedup/quantize/palette). KTX2/basis textures are unnecessary —
  flat-shaded low-poly lives on **vertex colors and tiny palette
  textures**, so geometry compression is the whole game.
- **Instancing is the fleet API.** three.js `InstancedMesh`: one draw
  call per (geometry × material), fixed-capacity buffer, `count` for
  partial fills ([three.js forum on partial counts](https://discourse.threejs.org/t/directly-remove-instancedmesh-instance/25504)),
  `DynamicDrawUsage` + `needsUpdate` on the instance matrix attribute for
  per-frame rewrites
  ([three.js performance checklist](https://www.mysimulator.uk/content/tutorials/threejs-performance.html),
  [IGC 60fps patterns](https://www.intelligentgraphicandcode.com/development/threejs-interfaces/performance)).
  For us: one InstancedMesh per vehicle class (cars, tractors, trailers —
  the tractor/trailer split already exists as two pools in the 2D path,
  `viz/src/main.ts:1351-1390`), capacity = index-observed max fleet,
  per-frame matrix writes from the same `RenderVehicle` interpolation the
  map consumes (`viz/src/snapshots.ts:24-31`). Per-instance frustum
  culling is a known non-triviality
  ([three.js forum, 2025](https://discourse.threejs.org/t/ideas-on-performing-fast-per-instance-frustum-culling-on-instancedmesh/85156/4))
  — at ~10² instances and one junction it is simply not needed; cull the
  whole mesh or nothing.
- The **static scene is the opposite treatment**: merge road ribbons,
  ground, buildings, signal housings into a handful of static
  BufferGeometries (`BufferGeometryUtils.mergeGeometries`) — the draw-call
  budget should be dominated by nothing; see §6.

## 2. Camera cinematics: deterministic paths keyed to sim tick

- **Exploit determinism.** Our frames are a pure function of (bake, tick):
  a camera path keyed to *tick* (not wall time) replays bit-identically —
  the same property the engine's replay guarantees (ADR-0005) and the quiz
  already relies on. The camera format should therefore store keyframes in
  **sim ticks** with an easing/interpolation rule, evaluated at render
  time against the same interpolated clock the vehicles use
  (`SnapshotBuffer.sample`, `viz/src/snapshots.ts:102-157`).
- **Pattern menu.**
  - *Keyframe tracks* — three.js `AnimationClip`/`KeyframeTrack`
    ([three.js docs](https://threejs.org/docs/#api/en/animation/KeyframeTrack))
    can drive camera position + target (quaternion tracks avoid
    lookAt-flip); or a minimal hand-rolled track (our repo style — the
    artic/trailer model is exactly a hand-rolled kinematic integrator,
    `viz/src/artic.ts:92-177`).
  - *Spline paths* — `CatmullRomCurve3`
    ([three.js docs](https://threejs.org/docs/#api/en/extras/curves/CatmullRomCurve3))
    for fly-throughs; the DEPT agency writeup
    ([Coding a cinematic camera path](https://www.deptagency.com/en-nl/insight/coding-a-cinematic-camera-path/))
    covers the classic gotchas (frame banking, up-vector continuity,
    sampling by arc length not parameter).
  - *Interactive controls* — `OrbitControls` for free orbit
    ([three.js docs](https://threejs.org/docs/#examples/en/controls/OrbitControls));
    `camera-controls` ([github.com/yomotsu/camera-controls](https://github.com/yomotsu/camera-controls))
    adds smooth programmatic transitions between authored poses (the
    three.js-forum-recommended successor for "orbit + smooth moves",
    [thread](https://discourse.threejs.org/t/solved-how-to-add-smooth-transition-to-three-js-orbital-camera/657)).
  - *The sumo3Dviz taxonomy* ([arXiv 2604.19194](https://arxiv.org/html/2604.19194v1),
    §3.3) is the domain-shaped version: **Eulerian** (fixed hero),
    **Lagrangian** (follow a vehicle id — trivial for us: id is stable,
    `viz/src/tssf.ts:11-12`), **Cinematic** (predefined path),
    **Interactive**. Name our camera modes the same way; the terms are
    the domain's.
- **Recommended format shape** (decision candidate D3 in synthesis): a
  small JSON clip document — `{ version, bake, tickStart, tickEnd,
  fps, camera: { mode, keyframes: [{tick, pos, target, fov?, ease?}] } }`
  — stored next to the bake (or inline in a demo page's config), so
  "the cool intersection video" is a *diffable text artifact*, matching
  the repo's everything-reproducible posture (scenario manifests,
  ADR-0012, are the template).

## 3. Headless/offscreen capture pipelines (2026 maintenance check)

The requirement: reproducible clips — same bake + same clip document ⇒
same video bytes. Ranked by fit.

- **(a) CDP beginFrame + virtual time — the deterministic one, and we
  already own the harness.** Chrome's `HeadlessExperimental.beginFrame`
  with `--deterministic-mode --enable-begin-frame-control` replaces the
  browser's render loop with on-demand frames and freezes the page clock
  until you advance it: *"Every run is identical. Same inputs produce
  byte-identical frame sequences"*
  ([Why I Built puppeteer-capture](https://alexey-pelykh.com/blog/why-i-built-puppeteer-capture/)).
  The library wrapper is [puppeteer-capture](https://github.com/alexey-pelykh/puppeteer-capture)
  (npm `puppeteer-capture@1.58.0`, verified 2026-08-24; docs
  [pptr-capture.org](https://pptr-capture.org)) — **but Linux/Windows
  only, not macOS**, requires chrome-headless-shell, and pulls puppeteer
  as a dependency. The repo alternative: `viz/scripts/screenshot.mjs:22-35`
  already drives headless Chrome over **raw CDP with zero npm deps**
  (node ≥22 global WebSocket + fetch, SwiftShader flags), so a beginFrame
  loop is an extension of an in-repo pattern, not a new toolchain. Its
  recorded lesson — virtual time races ahead of MapLibre workers and the
  wall-clock stream (`viz/scripts/screenshot.mjs:3-6`) — does **not**
  apply to a three.js page with no MapLibre and a tick-stepped frame
  source; that is the strongest technical argument for keeping the hero
  renderer MapLibre-free (see implementation.md §9).
- **(b) MediaRecorder + canvas.captureStream — easy, nondeterministic.**
  Real-time capture: frame timing depends on system load; two runs differ
  ([puppeteer-capture blog's framing](https://alexey-pelykh.com/blog/why-i-built-puppeteer-capture/)).
  Frame-by-frame control is a known spec gap
  ([w3c/mediacapture-record#213](https://github.com/w3c/mediacapture-record/issues/213);
  `captureStream(0)` + `requestFrame()` exists per
  [mediacapture-fromelement#43](https://github.com/w3c/mediacapture-fromelement/issues/43)
  but browser quirks persist — Firefox ties capture to rAF
  ([bug 1344524](https://bugzilla.mozilla.org/show_bug.cgi?id=1344524)),
  WebKit has WebGL-canvas capture bugs
  ([170325](https://bugs.webkit.org/show_bug.cgi?id=170325),
  [230613](https://www2.webkit.org/show_bug.cgi?id=230613)).
  Fine for a *user-facing "download a clip"* button later (a
  [devtails walkthrough](https://devtails.xyz/@adam/how-to-record-html-canvas-using-mediarecorder-and-export-as-video)
  shows the minimal shape); wrong tool for the canonical episode videos.
- **(c) Puppeteer native `page.screencast()`** — real-time webm; fps and
  quality knobs documented
  ([ScreenshotOne comparison](https://screenshotone.com/blog/how-to-record-videos-with-puppeteer/));
  known slow-motion/timing artifacts
  ([nebrass.fr fix writeup](https://blog.nebrass.fr/playing-with-puppeteer-fixing-slow-motion-screencasts/)).
  Same nondeterminism class as (b) plus a puppeteer dependency. Skip.
- **(d) headless-gl (npm `gl`)** — WebGL in pure Node. Maintenance
  **revived**: `gl@8.1.6` (verified 2026-08-24), releases as recent as
  Dec 2025 ([releases](https://github.com/stackgl/headless-gl/releases)),
  prebuilt binaries for node 20/22/24, WebGL 1.0.3 conformance +
  *experimental* WebGL2 (`createWebGL2Context`, per the
  [README](https://github.com/stackgl/headless-gl)). ANGLE software
  rendering — slow and *"many orders of magnitude slower than a browser"*
  ([issue #114](https://github.com/stackgl/headless-gl/issues/114)), no
  DOM (no Image/video texture loading). For us: a possible CI render
  smoke (render one frame, hash pixels) but not the clip pipeline —
  real Chrome is faster and we already script it.
- **(e) Remotion** — React-component video authoring
  ([remotion.dev](https://remotion.dev)); the puppeteer-capture author's
  own comparison says it creates video *from React code*, the opposite of
  capturing an existing scene. React is excluded by ADR-0003. Out.
- **Encode**: whatever captures frames hands PNG/raw frames to **ffmpeg**
  (puppeteer-capture's model: ffmpeg from `$FFMPEG`/PATH/`ffmpeg-static`)
  — the same shape as the repo's other media tooling.

## 4. Art direction: low-poly stylized traffic

- **Palette discipline is already the house style** — theme.ts exists so
  "the traffic must stay the loudest thing on the map"
  (`viz/src/theme.ts:26-28`): muted static context (boundaries, water,
  buildings at near-background values, `:67-75`), saturated semantic
  channels (congestion ramp, signal colors, `:61-66`). A 3D scene should
  import THEMES literally (it is DOM-free pure data, `:7-8`) so the two
  renderers read as one product: navy canvas → dark ground plane, quiet
  building masses (`buildingOther` barely above canvas), vehicle bodies in
  `glyphColors`, signals in `signalGreen/Amber/Red`.
- **Flat shading + one soft shadow source.** Low-poly reads "designed"
  with flat-shaded materials (vertex normals per-face) and a single
  directional light with a tight shadow-camera frustum on the junction;
  MeshLambert/Standard-over-PBR where PBR isn't needed (the
  [performance checklist](https://www.mysimulator.uk/content/tutorials/threejs-performance.html)
  's advice, and cheaper). Ambient hemisphere light fills; no textures.
- **Emissive signals.** The lit lens is the story at night-ish navy
  scenes: emissive material + a small bloom pass (or a fake glow sprite —
  cheaper and style-matched to the 2D sprite's glow-margin trick,
  `viz/src/signalhead.ts:18-21`). The 2D head's lens order (red top,
  amber mid, green bottom, `viz/src/signalhead.ts:11`) is the modeling
  spec.
- **Miniature photography** (competitors.md §Cities:Skylines): elevated
  oblique default (~35–50° pitch), shallow DoF to pull focus to the box,
  slightly desaturated ground — [tilt-shift/miniature faking](https://en.wikipedia.org/wiki/Tilt%E2%80%93shift_photography)
  as a legibility device, not decoration.
- **Motion smoothing**: the 2 Hz bake + SnapshotBuffer lerp is adequate
  for pose; if hero close-ups expose heading stepping, sumo3Dviz §4.2's
  documented recipe (angle unwrap → lerp → centred moving average →
  velocity-derived orientation re-estimate, [paper](https://arxiv.org/html/2604.19194v1))
  is the upgrade path — note it *deliberately diverges from recorded
  truth*, so it belongs behind a capture-only flag, never in the
  analytical renderer (same philosophy as snapshots.ts's no-extrapolation
  rule, `viz/src/snapshots.ts:4-6`).

## 5. Input UX: adjusting signal timing over a 3D scene

- **In-scene picking for target selection, overlay for parameter edit.**
  three.js raycasting against the signal-head meshes gives
  click-a-head selection (Traffix proves tap-the-signal is a legible
  verb — competitors.md). The *edit* surface is then an overlay panel,
  because scrubbing numeric splits by dragging in 3D is precision-poor:
  phase list with per-phase duration sliders/steppers, in ticks or
  seconds (our decisecond tick maps losslessly — domain-signal-control
  article, `docs/kb/articles/business-domains/signal-control.md`).
- **Overlay tooling precedent**: lil-gui (`lil-gui@0.21.0`, verified
  2026-08-24; [site](https://lil-gui.georgealways.com/)) is three.js's own
  dat.gui successor ([why three.js switched](https://discourse.threejs.org/t/lib-why-changed-dat-gui-to-lil-gui/62124));
  Tweakpane ([npm](https://www.npmjs.com/package/tweakpane)) is the
  richer alternative. **But** our HUD chrome is already hand-rolled DOM
  with theme tokens (`viz/app.html:28-46`) and the quiz page is
  dependency-free hand-rolled HTML (`scripts/chicago/mkquiz.py:15-17`) —
  a bespoke slider row is ~100 lines and matches both neighbors better
  than a debug-UI library. Recommendation: hand-roll, keep lil-gui for
  the 3D page's own debug camera/art tuning (dev-only, tree-shaken out
  of the shipped page if desired).
- **Submission shape**: the edit is a JSON document
  `{programId, phases: [{durationTicks}], baseBake, submittedAt}` —
  mirroring TSSG's program structure (`viz/src/tssg.ts:35-51`) so the
  server can compile it back into tlLogic/signal_set verbs (ADR-0037,
  `docs/kb/decisions/ADR-0037-runtime-signal-control.md:91-95`) or into a
  scenario overlay for a fresh run. Keep the payload engine-shaped from
  day one; do not invent a presentation-side timing dialect that a
  translator must reinterpret (the mkquiz lesson: numbers live in exactly
  one file, `scripts/chicago/mkquiz.py:7-11`).

## 6. Performance budgeting

- **The shape is trivially inside budget.** ~10² vehicles + one junction
  + static context: three.js at this scale is CPU-bound on matrix writes
  at worst. Reference ceilings: deck.gl documents ~1M instanced items at
  60 FPS ([performance guide](https://deck.gl/docs/developer-guide/performance));
  three.js InstancedMesh handles tens of thousands of instances with the
  dynamic-attribute discipline (§1) — we are three orders of magnitude
  below either.
- **Budget targets** (set them now, they are the acceptance test):
  60 fps at 1080p on a 2021-era iGPU laptop; < 200 draw calls (static
  merged scene ~10–30, instanced fleets 3–6, signal lenses ~1 instanced
  emissive, shadow pass ≈ same again); < 50 MB GPU; first interaction
  < 3 s on conference wifi (the quiz page's own constraint,
  `scripts/chicago/mkquiz.py:15-17`) — which caps GLB fleet + page JS
  (three ~0.6 MB min / ~0.17 MB gzip per the
  [utsubo figure](https://www.utsubo.com/blog/threejs-vs-babylonjs-vs-playcanvas-comparison))
  at a few MB total.
- **LOD**: unnecessary for vehicles at this count (one LOD each);
  buildings can be single-height extrusions. Keep the LOD slot empty —
  documented in the fleet topic as its job if the fleet grows
  (`docs/kb/.kb-meta.json:257-263`).
- **The one real cost is capture**: SwiftShader CPU rasterization for
  beginFrame capture scales with pixels × frames; keep capture
  resolution/framerate in the clip document (§2) so cost is explicit
  and diffable.
