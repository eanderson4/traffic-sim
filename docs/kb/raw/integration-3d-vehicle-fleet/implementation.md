# implementation.md — integration-3d-vehicle-fleet

> Source: codebase tracing | Researched: 2026-08-24 | Git HEAD: 2bc98de

What exists today that the 3D vehicle fleet plugs into. Every claim is
`file:line` against HEAD `2bc98de` (committed 2026-08-06; research run
2026-08-24). The fleet's consumer is the planned rung-2 three.js hero
renderer (sibling topic `integration-3d-hero-viz`, registered PENDING in
`docs/kb/INDEX.md:93`); this document traces the substrate the fleet must
match — class identity, dimensions, heading conventions, articulation,
palette, and asset/deploy layout.

## 1. The render data path the fleet feeds

### 1.1 Vehicle identity on the wire is (id, class index) — nothing else

- Live snapshots: TSSF v1 binary SoA frame, `contracts/asyncapi.yaml:1128-1135`
  — per vehicle (24 B): `id u64 | x f32 | y f32 | angle f32 | class f32`,
  where "`class` is the scenario type index carried as f32 (0 = car, 1 = truck)".
- Baked replays: TSRB chunk, `contracts/baked-replay-v1.md:16` — per vehicle
  (14 B): `id u32 | x u32 | y u32 | angle u8 | class u8`; `:30` — "`class` the
  scenario type index".
- **Why it matters:** the contracts pin only an INTEGER. Class *names* are
  not contractual; the index resolves through the scenario manifest's `types`
  list (`engine/scenario/scenario.go:69` — `Types []string
  yaml:"types,omitempty"`), which is resolved against a name → `VehicleType`
  registry at run start (`engine/scenario/scenario.go:393` at HEAD
  2bc98de — `RunSpec(typeReg map[string]*engine.VehicleType)`; :404-405 in
  the uncommitted working tree, ADR-0039 insertions). A 3D fleet therefore
  needs its own **manifest mapping scenario type index (or type name) →
  model**, versioned alongside the scenarios — it cannot be derived from any
  contract document.

### 1.2 Client render model

- `viz/src/snapshots.ts:24-31` — `RenderVehicle { id, x, y, angle, cls,
  speed }`: local metric frame position (front-bumper, see §3), heading in
  radians CCW-from-east, class index, client-derived speed.
- `viz/src/snapshots.ts:11-15` — speed is NOT on the wire; it is derived
  client-side from snapshot displacement over *sim* time. A 3D renderer
  gets speed the same way (it already must, for wheel-spin animation).
- `viz/src/vehicles.ts:36-81` — the vehicle channel: spawn/update/remove
  diffs keyed on vehicle id, carrying `cls`, `angle`, `speed`
  (`vehicles.ts:40`). **Why:** whatever consumes the fleet re-implements
  this per-id lifecycle; the fleet manifest must support hot model lookup by
  class at spawn time and (per `vehicles.ts:62`) class *change* mid-life.

### 1.3 Heading convention

- Wire/engine: radians, CCW from east, local metric frame, UTM north-up
  (`viz/src/theme.ts:167-176`, `viz/src/proj.ts:2`).
- MapLibre conversion: `vehicleBearingDeg` (`viz/src/theme.ts:173-176`) and
  the inline expression `["-", 90, ["*", angle, 180/π]]`
  (`viz/src/main.ts:906,932`).
- **Why:** a three.js consumer converts the same radians into a Y-rotation;
  glTF's convention (+Y up, asset front faces +Z — Khronos spec §3.4) means
  the fleet must pick and document ONE model-forward axis or every model
  will import rotated differently. This is a manifest-level decision, not a
  per-model one.

## 2. Current glyph generation — what the fleet replaces in hero context

- `viz/src/glyphs.ts:41-52` — `makeRectImage`: a white rectangle drawn to an
  offscreen canvas at true aspect ratio, `PX_PER_M = 10`
  (`glyphs.ts:28`), registered with MapLibre `{ sdf: true }`
  (`viz/src/main.ts:887`) so `icon-color` tints and `icon-rotate` aims.
- `viz/src/glyphs.ts:56-64` — `bodyImages()`: THREE bodies — the car whole,
  and the truck split into tractor + trailer images (`TRACTOR_IMAGE_ID`,
  `TRAILER_IMAGE_ID`, `glyphs.ts:31-32`).
- `viz/src/glyphs.ts:17-20` — glyphs are deliberately **screen-space**:
  "legibility wins over scale fidelity … proportions BETWEEN bodies stay
  honest (same px/m for every image)". `ICON_SIZE_STOPS`
  (`glyphs.ts:69-73`) is one zoom curve for all bodies.
- `viz/src/main.ts:877-949` — the two symbol layers (`trailers` UNDER
  `vehicles` so the tractor overlaps the trailer nose at the hitch,
  `main.ts:889-891`), class → image/color via `["match", ["get", "cls"]…]`
  (`main.ts:928,945`).
- **Why:** the glyph system proves the two properties the fleet must
  preserve: (1) class legibility is the primary encoding (color + size
  ratio), and (2) all bodies share one scale authority. In 3D those become:
  shared palette + dimensions anchored to engine class dims (§3). The
  screen-space cheat (glyphs are ~13× oversized at z14, ~4× at z17, for
  legibility)
  does NOT carry into a hero renderer, which renders at true metric scale —
  that is the point of the hero view.

## 3. Authoritative class dimensions (the fleet's scale contract)

- `engine/vehicle.go:8-17` — `VehicleType` carries `Length` and `Width`
  (meters) plus IDM dynamics. Geometry is part of the type, not the
  instance.
- `engine/vehicle.go:33` — `Car = {Name: "car", Length: 5, Width: 2, …}`
  (v0 33.3 m/s highway-calibrated IDM).
- `engine/vehicle.go:40` — `Truck = {Name: "truck", Length: 12, Width:
  2.5, …}`.
- `viz/src/theme.ts:144-147` — `GLYPHS` mirrors these dims
  (cls 0 car 5×2, cls 1 truck 12×2.5) with the explicit invariant that "a
  new class with real dimensions slots in without retuning"
  (`theme.ts:139-143`). `glyphByCls` (`theme.ts:160-165`) looks up **by
  cls, never positionally** — the fleet manifest should copy this rule.
- `engine/vehicle.go:46-53` — vehicle position `S` is the **front-bumper**
  arc-length coordinate. The MapLibre glyph compensates by centering the
  image on the anchor (`viz/src/main.ts:1355-1359` offsets by half length).
- Contemplated classes from sibling research (not yet in engine):
  - **Bus:** 12 m (CTA 40-ft) and/or 18 m articulated, ~2.55 m width —
    `docs/kb/raw/domain-bus-operations/synthesis.md:38-39`.
  - **Rail/El:** 14.6 m married-pair cars, consists to 8 cars / ~117 m —
    `docs/kb/raw/domain-rail-operations/synthesis.md:11,98,114`.
- **Why:** the fleet's per-class bounding boxes are a *hard contract* with
  the simulation, not an art choice: a 5 m sedan model rendered at 12 m
  breaks gap perception (bumper-to-bumper spacing is the engine's core
  convention, `vehicle.go:46-47`). QA gates should check model bbox ==
  class dims within tolerance (±2%), and the model origin convention must
  encode the front-bumper anchor (recommended: origin on the ground plane
  under the front bumper, forward = +Z per glTF §3.4).

## 4. Articulation — what a 3D truck/L-consist analog needs

- The engine simulates every vehicle as a **rigid body**
  (`viz/src/artic.ts:1-5`); articulation is inferred client-side by
  `Articulator` (`viz/src/artic.ts:92-177`) using the single-track trailer
  equation with sim-time substeps (`artic.ts:137-143`), a hitch-angle
  glitch clamp (`artic.ts:60-81`), spawn-aligned state (`artic.ts:110`),
  and `prune()`/`reset()` lifecycle (`artic.ts:163-176`).
- The split: `TRACTOR_M = 4`, `TRAILER_M = 8` (`viz/src/theme.ts:152-153`)
  — the 12 m truck becomes a 4 m tractor + 8 m trailer pivoted at the
  hitch point `H = front − TRACTOR_M·u(θ)` (`artic.ts:149-150`).
- `TrailerPose` (`artic.ts:83-90`) returns BOTH body centers + headings per
  frame — exactly the input a 3D renderer needs to pose two GLB nodes.
- **Why for 3D:** (1) the semi model must be authored as **two meshes with
  a hitch pivot at 4 m from the front bumper**, not one 12 m box — same as
  the glyph split (`glyphs.ts:54-55`); (2) an L consist generalizes the
  same equation as a CHAIN (each car's axle follows its predecessor's
  hitch) — the `Articulator` class is per-vehicle-id state
  (`artic.ts:93`) and the pattern extends per-car; (3) if the rail topic's
  one-`VehicleType`-per-consist design lands (`domain-rail-operations
  synthesis.md:98` — e.g. `l8` at 117 m), the viz still needs per-car
  bends on curves, so multi-car articulation stays a viz-side inference
  exactly like today's trailer. Budget: the equation is O(cars) per frame
  with the same substep discipline (`artic.ts:57-59`).

## 5. Palette the fleet must live in

- `viz/src/theme.ts:56-119` — two shipped themes: `navy` (dark canvas
  `#0e1d5c`) and `paper` (light `#fafafa`), resolved by `?theme=`
  (`theme.ts:125-129`).
- Vehicle colors per class: `glyphColors` — navy: car `#eaf0ff`, truck
  `#ff7d4d` (`theme.ts:87`); paper: car `#26262b`, truck `#2563eb`
  (`theme.ts:117`).
- **Why:** the 2D map tints glyphs BY CLASS from the theme. The 3D fleet
  inverts this: models carry their own per-instance body color (real-world
  fleets are multi-color), but (a) class must stay legible through
  silhouette + proportion instead of color, and (b) the shared palette
  atlas should be chosen to read against BOTH navy and paper canvases if
  the hero renderer is ever composited over the map. The theme table is the
  in-repo precedent for "one place owns every color" — the fleet's palette
  atlas is the 3D analog.

## 6. Static asset layout — where GLBs would live

- `viz/public/` is vite's static dir (copied verbatim into `dist/`): today
  it holds favicons, runreport JSONs, `network.geojson` (34.8 MB),
  `quiz.html`, `flow.html`, `fonts/`, and `_headers`. NO models directory
  exists.
- `viz/vite.config.ts:10-19` — build config declares ONLY the three HTML
  page inputs; "everything else stays vite defaults" (`vite.config.ts:5`).
  Default vite behavior serves `public/*` at `/<path>` — so
  `viz/public/models/**` would serve at `/models/**` with zero config.
- `viz/public/_headers:1-25` — the Cloudflare Pages deploy already
  establishes the binary-asset pattern: brotli precompression served with
  `Content-Encoding: br` (`_headers:5-14`) and content-keyed immutable
  caching for baked artifacts (`_headers:16-18`, "the hash IS the
  version"). A `/models/*` block with immutable caching is the natural
  extension. Note the Pages 25 MiB per-file cap mentioned at
  `_headers:2-4` — a whole low-poly fleet (≲100 KB/model target) is three
  orders of magnitude under it; size is a non-issue, unlike
  `network.geojson`.
- `data/baked/` — content-keyed baked artifacts (ADR-0023); fleet assets
  are NOT baked data (they are app code-adjacent statics), so
  `viz/public/models/` is the right home, not `data/`.
- **Why:** zero-build-step statics + immutable cache headers is the
  repo's established pattern for large binary payloads; GLBs slot into it
  without touching vite config, the bake pipeline, or contracts.

## 7. 3D dependency audit — a green field

- `viz/package.json:17-27` — the ONLY runtime deps are `maplibre-gl
  5.24.0`, `nats.ws 1.30.3`, `pmtiles ^4.4.1`. **No three.js, no glTF
  loader, no 3D asset of any kind anywhere in the repo** (repo-wide grep
  for `three.js|gltf|.glb` hits only docs: `ADR-0003-maplibre-vis.md:26`,
  KB meta, prior maplibre research, and the two 3D research topics —
  `integration-3d-hero-viz/` and this one).
- `docs/kb/decisions/ADR-0003-maplibre-vis.md:26` — the ADR explicitly
  blesses the boundary: "a future Three.js driver-view client is an
  additive new consumer, not a rewrite." The fleet + hero renderer land as
  that additive consumer; the MapLibre app (`app.html`) is untouched.
- `docs/kb/INDEX.md:93` — the hero renderer is scoped to **baked replays**
  (TSRB poses + TSSG signals + network geometry — "renderer is a pure
  consumer", `.kb-meta.json` research hints), perf budget "~10² vehicles +
  one junction". The fleet's perf target is therefore modest (hundreds of
  instances, not tens of thousands) — see standards-and-patterns.md §3.
- **Why:** no incumbent 3D toolchain means the fleet dictates its own
  pipeline choices (GLB + manifest), and the dependency surface stays at
  whatever the hero-viz topic picks (three.js per the registration).

## 8. Test/QA idiom the fleet's gates should follow

- `viz/package.json:12` — `node --test test/*.test.ts`; 25 test files in
  `viz/test/` including `theme.test.ts`, `artic.test.ts`,
  `vehicles.test.ts`, `glyphs` coverage via theme tests.
- Modules keep a "pure + DOM-free so node --test can import it" discipline
  (`theme.ts:6-7`, `artic.ts:26-27`).
- **Why:** the fleet's QA automation (dimension checks, manifest schema
  validation, tri budgets) should be plain `node --test` files in the same
  idiom — no new test framework. GLB parsing for bbox/tri checks needs one
  small dependency or a minimal GLB header parser (GLB layout is trivial:
  12-byte header + JSON chunk, glTF spec §4.4, GLB File Format
  Specification).

## 9. Review-gate implications for fleet files

- `AGENTS.md` review workflow: the external-review pre-commit hook gates
  every commit EXCEPT "documentation and generated content (`docs/`,
  `data/`, **viz dist/node_modules/public**, root Markdown, LICENSE …)".
- **Why:** GLBs and contact-sheet PNGs committed under `viz/public/models/`
  fall in the exempt bucket (like `network.geojson` today), but any
  generator scripts (bpy/parametric sources) and the manifest schema live
  outside `public/` exemptions and would be gated — plan the swarm so
  *source* lands in a gated location (reviewed like code) and *artifacts*
  land in `viz/public/`. The consistency-review pass (see
  standards-and-patterns.md §6) is the asset-world analog of the repo's
  one-round blocker-triage bar.

## 10. Summary of integration surface

| Surface | Today | Fleet requirement |
|---|---|---|
| Class identity | integer index into scenario `types` (`asyncapi.yaml:1135`) | manifest: type name/index → model URL |
| Class dims | engine `vehicle.go:33,40` (5×2, 12×2.5); bus/L contemplated | model bbox == class dims ±2%, real meters |
| Anchor | front-bumper `S` (`vehicle.go:53`) | origin convention encoding front bumper + forward axis |
| Heading | rad CCW-from-east (`theme.ts:167-176`) | single Y-rotation mapping in renderer |
| Articulation | client-side single-track (`artic.ts:92-177`), split 4+8 m (`theme.ts:152-153`) | semi = 2 meshes w/ hitch pivot; L = car chain |
| Palette | `theme.ts:56-119`, per-class `glyphColors` | shared atlas readable on navy + paper |
| Assets home | `viz/public/` statics, `_headers` brotli+immutable | `viz/public/models/`, same cache pattern |
| 3D deps | none (`package.json:17-27`) | GLB self-contained; loader is hero-viz's choice |
| QA idiom | `node --test` (`package.json:12`) | dimension/tri/manifest checks as node tests |
