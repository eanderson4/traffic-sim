# Implementation: what the existing viz already provides a 3D renderer

> Source: codebase tracing | Researched: 2026-08-24 | Git HEAD: 2bc98de

Traces every asset the current viz produces that a rung-2 three.js renderer
would consume, with `file:line` and WHY analysis, then enumerates the gaps a
3D path must fill itself. Read against the topic registry entry
(`docs/kb/INDEX.md:93`) and the topic's research hints
(`docs/kb/.kb-meta.json:249-255`).

## 0. TL;DR

The viz's data plane is already renderer-agnostic: everything funnels through
two binary frame decoders (`tssf.ts`, `tssg.ts`), an interpolation buffer
(`snapshots.ts`), and a signal-derivation chain (`tssg.ts` + `signals.ts`)
that are all DOM-free and MapLibre-free. The baked shim (`baked.ts`)
deliberately re-encodes TSRB chunks into *synthetic TSSF v1 bytes* so that
"the render loop runs byte-for-byte untouched" (`viz/src/tsrb.ts:2-5`). A
three.js page is a new consumer behind the same seam — no pipeline change.
The real gaps are: no z/elevation anywhere in the data model, no speed on
the wire (client-derived), baked-mode region streaming that is keyed to a
MapLibre viewport (not to a 3D camera), no vehicle model assets, no 3D
dependency at all, and no server-side scoring endpoint for quiz
interactivity.

## 1. The frame interface: what a frame actually carries

### TSSF v1 (live wire; also the shim's output format)

`viz/src/tssf.ts:4-6` — header 24 B + 24 B/vehicle:
`id u64 | x f32 | y f32 | angle f32 | class f32`.

- `x/y` are the network's **local metric frame** (north-up, +x east),
  `viz/src/tssf.ts:8-10`. This is the single most important fact for a 3D
  path: positions arrive already metric, so a three.js scene can place
  vehicles with `position.set(x, 0, -y)` and never touch a projection.
- `angle` is the **lane tangent** in radians, 0 = +x/east, CCW positive
  (`viz/src/tssf.ts:8`). Under the x→x, y→−z mapping the vehicle forward
  vector is `(cos θ, 0, −sin θ)`; the `rotation.y` constant that produces
  it depends on the fleet models' forward-axis convention (+x-forward:
  `rotation.y = θ`; glTF-style +z-forward: `θ + π/2`) — a one-line spec
  for the fleet topic, verified against the shipped models, not something
  to guess here.
- `class` is the **scenario vehicle-type index** (0 = car, 1 = truck),
  `viz/src/tssf.ts:10` — the per-class model lookup key.
- Vehicle `id` is sequential from 1 (`viz/src/tssf.ts:11-12`) — stable
  instance-slot key for an InstancedMesh pool and for trailer state
  (artic.ts already keys on it).
- Position is the **front bumper**, not the centroid:
  `viz/src/main.ts:1354-1355` ("The wire position is the FRONT BUMPER
  (engine Project: s is front-bumper arc length)"). Model anchoring must
  shift back half the body length along the heading, exactly as main.ts
  does for glyphs (`viz/src/main.ts:1356-1362`).
- **No speed, no z, no dimensions on the wire.** Speed is derived
  client-side from snapshot displacement over sim time
  (`viz/src/snapshots.ts:11-15,129-153`); a 3D renderer that needs wheel
  spin or brake-light triggers inherits the same derivation.

### TSRB v1 (baked chunks; decoded then re-encoded)

`viz/src/tsrb.ts:7-9` — header 20 B + **14 B/vehicle**:
`id u32 | x u32 | y u32 | angle u8 | class u8`.

- Quantization: x/y to 0.1 m steps biased by `index.json.quant.origin`,
  decode `c = origin + q × step` (`viz/src/tsrb.ts:71-77`);
  angle to 256 steps ≈ 1.4° (`viz/src/tsrb.ts:75`). The contract pins
  decode as `q × 2π/256` with **floor** on encode
  (`contracts/baked-replay-v1.md:28-29`). 1.4° yaw error is invisible at
  glyph scale but worth knowing if a 3D camera sits at windshield height —
  still fine (≈2.5 cm lateral at 1 m ahead).
- `decodeTsrbChunk` (`viz/src/tsrb.ts:46-84`) returns the *same*
  `VehicleRecord[]` shape as TSSF; `encodeTssf` (`viz/src/tsrb.ts:90-109`)
  re-encodes to exact TSSF v1 bytes. WHY: everything downstream (buffer,
  artic, HUD) was designed to run "byte-for-byte untouched"
  (ADR-0023 §6), and a 3D renderer gets the same deal — consume
  `VehicleRecord` or raw TSSF bytes, never TSRB specifics.
- Bake cadence is **2 Hz** (`bakeEveryTicks: 5` at dt 0.1) with a terminal
  off-stride frame (`contracts/baked-replay-v1.md:33-40`). At 2 Hz the
  500 ms frame interval exceeds SnapshotBuffer's 250 ms default, so baked
  mode resizes the buffer to `1.25 × frameInterval / speed`
  (`viz/src/main.ts:444-445, 1318-1319`; setter at
  `viz/src/snapshots.ts:65-67`). A 3D page reusing SnapshotBuffer must copy
  this sizing rule or vehicles will stutter-hold every frame.

## 2. The baked transport shim: the exact consumer interface

`viz/src/baked.ts` is the ADR-0023 §6 shim. Its public surface:

```ts
subscribeBaked(indexUrl, onFrame, onSignals, onStatus, opts)
  → Promise<BakedSubscription>
```

(`viz/src/baked.ts:784-795`), mirroring `subscribeSnapshots(ws, run, …)`
(`viz/src/nats-client.ts:21-60`). The `BakedSubscription` interface
(`viz/src/baked.ts:360-373`):

- `nc: null` — the ADR-0016 signal-table pull no-ops on existing null
  guards (`viz/src/nats-client.ts:13-19`).
- `close()`; `fetchFn` — a FetchLike stub answering ReplayPanel's exact
  `/api/replay/*` routes (`viz/src/baked.ts:739-776`; panel shape at
  `viz/src/replaypanel.ts:33-52`), so **play/pause/speed/seek UI already
  works** against static files.
- `setViewport(bounds, zoom)` — **the one MapLibre-coupled input**
  (`viz/src/baked.ts:366-368, 490-497`): it drives z11-region subscription
  and the z13 vehicle gate (`BAKED_VEHICLE_GATE_ZOOM`,
  `viz/src/baked.ts:44`). A 3D page has no MapLibre bounds; it would feed
  the shim a synthetic viewport (the hero scene's WGS84 bbox at zoom ≥ 13)
  or bypass `BakedSession` and call `decodeTsrbChunk` directly with its own
  scheduler (see gap G4).
- `laneRatiosAt(tick)` — TSRL congestion lookup
  (`viz/src/baked.ts:369-372, 572-592`); a 3D hero scene can paint road
  ribbons by congestion ratio from this, reusing the exact lane-id key
  space.

WHY analysis: the shim was built so that *"tssf.ts, SnapshotBuffer, the
artic channel, and the render loop run untouched"* (header comment,
`viz/src/baked.ts:10-13`). That design goal is precisely what makes a 3D
renderer cheap to add: the transport, the clock, the control plane, the
signal table, and the interpolation machinery are all already solved,
tested (`viz/test/`), and DOM-free. The 3D page is a new *render target*,
not a new pipeline.

The index manifest (`BakedIndex`, `viz/src/baked.ts:73-91`) hands the 3D
page its scene setup: `frame` (projection + netOffset), `bounds` (WGS84),
`dt`, `tickStart/tickEnd`, `quant`, `network` (pmtiles OR geojson),
`furniture`, `signals`, `laneIds`, `regions[]`. Verified against a real
bake: `data/baked/bases42x27k/39a579974e87/index.json` — version 1,
dt 0.1, UTM zone 16 frame, 4 regions, GeoJSON network mode, tickEnd 27000.

## 3. Signal state: TSSG → per-head color at any tick

- `tssg.ts` decodes the **program table, never per-tick states**: light
  state is a pure function of tick count (`viz/src/tssg.ts:8-10`).
  `phaseIndexAt` (`viz/src/tssg.ts:237-248`) mirrors the kernel's integer
  math; `sigColorOf` (`viz/src/tssg.ts:261-273`) maps tlLogic chars to
  green/amber/red/off.
- `signals.ts` resolves programs to **head points** by clustering
  signal-bound internal lanes: one head per (program, state-column,
  approach) with a 75 m radius + 45° bearing gate
  (`viz/src/signals.ts:101-115, 147-199`); each head carries `x, y`
  (local metric frame, centroid set back 3.5 m toward its approach,
  `viz/src/signals.ts:52`), a stop-bar line (`viz/src/signals.ts:39-44`),
  and its `program` + representative `linkIdx`.
  `headStatesAtTick(heads, tick)` (`viz/src/signals.ts:204-210`) is the
  per-tick color map a 3D signal-head model would consume directly — same
  inputs (tick + TSSG table), no MapLibre anywhere in the derivation.
- The 2D sprite (housing + 3 lenses, `viz/src/signalhead.ts:16-32`) is
  MapLibre-specific, but its *art direction* (red top / amber mid / green
  bottom, dim unlit lenses) and the theme colors (`signalGreen/Amber/Red`
  in `viz/src/theme.ts:21-23`) carry over to a modeled head.
- In baked mode, heads come pre-derived as `furniture.geojson` (metric
  coords; ADR-0023 §1, manifest field at `viz/src/baked.ts:86`), produced
  by `viz/scripts/bake-furniture.mjs` running the same TS derivations in
  node. A 3D page consumes furniture + TSSG and re-derives colors per tick
  — no geometry clustering needed at runtime.
- ADR-0037's runtime `signal_set` verb (`{signal, phase, hold_ticks}`,
  `docs/kb/decisions/ADR-0037-runtime-signal-control.md:91-95`) is the
  engine-side channel a quiz submission would ultimately ride; today no
  baked artifact can express a *mutated* program (TSSG is static per run —
  ADR-0023 §10, `docs/kb/decisions/ADR-0023-baked-replay-pipeline.md:579-581`).

## 4. Vehicle presentation data: dims, palette, trailer model

- **Class dimensions** (the 3D model scale table): `GLYPHS` in
  `viz/src/theme.ts:144-147` — car 5×2 m, truck 12×2.5 m, mirroring the
  engine's authoritative `VehicleType` geometry (`engine/vehicle.go`,
  cited from `viz/src/theme.ts:139-143`). Only two classes exist today;
  the fleet topic (integration-3d-vehicle-fleet) extends this.
- **Palette**: `THEMES.navy/paper` (`viz/src/theme.ts:56-119`) — every
  color the map uses, incl. per-class glyph colors
  (`glyphColors: {0, 1}`, `viz/src/theme.ts:87,117`) and signal colors.
  A 3D scene reusing this palette keeps the two renderers visually
  continuous; theme.ts is pure data + DOM-free by design
  (`viz/src/theme.ts:7-8`), so a three.js entry can import it directly.
- **Heading convention bridge**: `vehicleBearingDeg`
  (`viz/src/theme.ts:173-176`) documents the wire→MapLibre conversion;
  the three.js conversion is simpler (metric frame, no mercator).
- **Trailer articulation**: `artic.ts` is the client-side single-track
  trailer model (`viz/src/artic.ts:1-13`): tractor 4 m + trailer 8 m
  (`TRACTOR_M/TRAILER_M`, `viz/src/theme.ts:152-153`), integrated in sim
  time with substep cap, glitch clamp, seek reset
  (`viz/src/artic.ts:92-177`). It is pure math keyed on vehicle id,
  returning tractor+trailer poses in the local metric frame
  (`TrailerPose`, `viz/src/artic.ts:83-90`) — directly consumable by two
  instanced meshes (tractor pool, trailer pool) in three.js. WHY it
  matters: it is the proof that presentation-layer kinematics belong in
  the client (the engine simulates rigid bodies); a 3D renderer inherits
  this pattern for any future articulation/bogie needs.

## 5. Projection: what a three.js camera actually needs

`viz/src/proj.ts:14-17` — `LocalFrame = { projection, netOffset }`;
`makeProjector` (`viz/src/proj.ts:45-90`) is the inverse-UTM (Snyder
series) to WGS84. Analysis for 3D:

- **A hero scene needs no geographic projection at all.** TSSF/TSRB x/y
  are already metric, north-up (`contracts/network-format-v1.md:73`:
  shapes are `[x, y]` pairs in the local metric frame). The 3D scene can
  be built entirely in that frame: ground plane z=0, x→x, y→−z (or a
  z-up scene graph), camera in metres. proj.ts is only needed if the
  scene must align with a geographic underlay (PMTiles, imagery) — the
  hero/quiz scenes have flat stylized ground, so the entire Snyder series
  drops out.
- The one projection-touching input is `index.json.bounds` (WGS84) — only
  needed if the 3D page feeds `setViewport` a bbox (see §2). Region keys
  are z11 web-mercator tiles (`viz/src/baked.ts:249-284`), so even that is
  two small closed-form functions, already exported.
- Camera math for an orbit/hero view is plain three.js (fov ~40–60°,
  near ~0.5 m, far ~2 km for a one-junction scene); nothing in the
  existing viz constrains it — main.ts's MapLibre camera params
  (`?center/?zoom/?bearing/?pitch`, `viz/src/config.ts:21-69`) are
  MapLibre-shaped but establish the *deep-link-to-an-intervention*
  pattern a 3D page should mirror (e.g. `?bake=…&focus=<junction>`).

## 6. The artifact contract a 3D page consumes

`contracts/baked-replay-v1.md` (whole file, 126 lines):

- **TSRB** vehicle chunks, brotli-precompressed, always fetched whole
  (`:17-21`); 120-frame (60 s at 2 Hz) windows, contiguous per region
  (`:73-81`).
- **TSRL** lane-speed chunks, 0.2 Hz, sparse, `ratio_q = round(clamp(meanSpeed/limit,0,1.5)×170)`
  (`:42-63`) — enough to paint congestion-colored road ribbons in 3D.
- **index.json** field list (`:83-96`).
- **Content keys**: `baked/{run}/{hash12}/`, immutable forever except
  no-cache index (`:102-124`) — a 3D page's assets (GLBs) should follow
  the same immutable-content-key posture.
- TSSG framing via `signals.chunkBytes` (`:91-92`).
- The bake tool is real and shipped: `engine/cmd/bake/` (bake.go,
  chunks.go, index.go + tests).
- What the contract does NOT have: z, speed, per-tick signal tables
  (static per run), vehicle dims (class index only), lane-width-per-frame
  (static, from the network export).

## 7. The ADR-0003 ladder and the VISION boundary

- ADR-0003 (`docs/kb/decisions/ADR-0003-maplibre-vis.md:16-20`): MapLibre
  primary, vanilla TS, no UI framework without an ADR; **deck.gl is the
  pre-approved escalation** "if and when MapLibre-native layers can't
  handle the animated vehicle count". Its Consequences already contemplate
  a 3D client: "a future Three.js driver-view client is an **additive new
  consumer, not a rewrite**" (`:25-26`). A rung-2 hero renderer therefore
  needs an **ADR-0003 addendum** (3D standalone page for baked hero/quiz
  only; MapLibre stays the live/analytical renderer) — exactly the
  boundary the topic registry states (`docs/kb/INDEX.md:93`).
- The measured escalation ladder (rung 0–3) lives in
  `docs/kb/raw/integration-maplibre-realtime/synthesis.md:101-140`:
  rung 2 = deck.gl `MapboxOverlay` at ≥ ~10k animated vehicles, rung 3 =
  full binary pipeline into Float32Array attributes. **The hero viz is not
  on that ladder** — it is not escaping MapLibre's vehicle-count limits
  (a hero scene is ~10² vehicles), it is escaping its *dimensionality*.
  The addendum should say so explicitly, or rung-2's meaning forks.
- VISION.md:98 non-goal is "**Photorealistic** 3D or driving-game physics
  (lane-level fidelity is the bar)" — a stylized low-poly hero renderer is
  not photorealistic and changes no dynamics; the addendum should cite
  this line to show non-contradiction.
- Dependency posture: `viz/package.json:17-27` — only `maplibre-gl
  5.24.0`, `nats.ws 1.30.3`, `pmtiles ^4.4.1` (+ dev typescript, vite,
  @types/geojson, @types/node).
  **No three/deck/babylon anywhere** (verified `viz/node_modules`: only
  those three runtime deps). AGENTS.md's dependency rule ("justify
  dependencies", approved exceptions are confined and documented) means
  `three` needs its own justification block in the addendum, mirroring
  ADR-0023's pmtiles justification (`docs/kb/decisions/ADR-0023-baked-replay-pipeline.md:470-474`).

## 8. The quiz surface this interactivity extends

`scripts/chicago/mkquiz.py`:

- Builds a **self-contained static page** (no CDN, no fetch, no build
  step — `:15-17`) from curate.py shortlists; four option cards per
  scenario, reveal gating, keyboard nav (`:212-328`).
- The only runtime link out is **"Watch the baseline running"**
  (`:343-346`), composed as an absolute `/app.html?bake=…&center=…&zoom=…`
  URL (`:231-235`) — the deep-link pattern a 3D quiz page should accept
  verbatim (swap `/app.html` for the new page).
- Baselines are verified at build time against `--baked-root`
  (`:541-556`) — a 3D page consuming the same bake URLs inherits that
  freshness gate.
- Bottleneck-town is the natural first quiz-3D scenario: a fictitious
  four-signal arterial where signals are the only bottleneck (`:33-37`),
  with retime-short and green-wave arms — i.e. **the arms are signal
  timing edits**, exactly what the interactive adjust-submit-score loop
  manipulates.
- **The gap**: the page is static and the scoring is precomputed offline
  (curate.py numbers, `:7-11`). An interactive "adjust timing → submit →
  scored → watch the run" loop needs (a) a mutation interface (signal
  timing editor), (b) a server that runs the sim with the edit — demosrv's
  HTTP surface today is demo/replay lifecycle only
  (`engine/cmd/demosrv/main.go:399-427`), with no scenario-mutation or
  ad-hoc-run endpoint — and (c) a way to watch the result: either a live
  ws session (ADR-0020's auth precondition blocks *public* exposure) or a
  fresh bake. The engine-side mutation channel exists (ADR-0037
  `signal_set`, or a scenario overlay with modified `tlLogic` for a
  fresh run); the HTTP/bake orchestration does not.

## 9. Build shape: how a three.js page slots in

- `viz/vite.config.ts:10-19` — already a **multi-page build**:
  `index.html` (splash), `app.html` (map), `demos.html` (menu). A hero
  page is one more `input` entry (`hero.html` + `src/hero-main.ts`), zero
  config invention. vite code-splits per page, so the map app never pays
  for three.js.
- `mkquiz.py` ships its page via `viz/public/quiz.html`
  (`scripts/chicago/mkquiz.py:11-14`) — static files in `viz/public/` are
  copied to dist. A built (non-static) hero page goes through vite inputs
  instead, like app.html.
- TS strict mode, pnpm, `node --test test/*.test.ts` with the
  DOM-free/DOM-shell module split convention (e.g. replaypanel.ts:28-31,
  demos.ts:7-8) — the 3D page's pure parts (frame stepping, camera-path
  evaluation, timing-edit model) should follow the same split so node
  --test reaches them.
- Existing headless harness: `viz/scripts/screenshot.mjs:22-35` drives
  headless Chrome over raw CDP (node ≥22 global WebSocket + fetch, **no
  puppeteer dependency**) with SwiftShader (`--use-angle=swiftshader
  --enable-unsafe-swiftshader`) for GPU-less rendering. Its header
  (`:3-6`) records the key lesson: *virtual-time budgets race ahead of
  maplibre's workers and the wall-clock-paced stream* — a three.js page
  without MapLibre workers, fed by a tick-stepped (not wall-clock) frame
  source, removes both races and makes deterministic beginFrame capture
  tractable (see standards-and-patterns.md §Capture).

## 10. The gap list: what a 3D path must fill itself

- **G1 — z/elevation: the world is flat.** Lane shapes are 2D `[x,y]`
  (`contracts/network-format-v1.md:73`); `GeoJSONLaneProperties`
  (`engine/geojson.go:28-37`) has no elevation/level field; grep of
  `engine/netimport/` finds no bridge/layer/tunnel/elevation extraction
  (only unrelated "level" matches). The Chicago L structure is *rail*,
  not road — not in the road network at all (rail ops are a separate
  PENDING topic, `docs/kb/INDEX.md:76`). So: **flat ground in v1**; any
  future grade separation needs an importer pass for OSM `bridge`/`layer`
  tags plus a network-format extension — a deliberate deferral, not an
  oversight.
- **G2 — vehicle models.** Nothing 3D exists; class dims + palette are
  the only anchors (`viz/src/theme.ts:144-147`). Fleet production is the
  sibling topic (integration-3d-vehicle-fleet).
- **G3 — no 3D dependency.** `three` must be added + justified (§7).
- **G4 — a scene-shaped (not viewport-shaped) frame source.**
  `BakedSession.setViewport` takes MapLibre bounds+zoom
  (`viz/src/baked.ts:490-497`) and gates vehicles below z13. Options:
  (a) feed it the scene bbox once at z≥13 — works, but drags the
  region-barrier/stall machinery into a context where all chunks are
  local; (b) bypass BakedSession, use `loadBakedIndex` +
  `decodeTsrbChunk` directly with a tick-stepped scheduler — cleaner for
  deterministic capture (seek(t)→render→grab), at the cost of
  re-implementing ~100 lines of chunk-window selection
  (`chunkForTick`, `viz/src/baked.ts:335-341`). For one-junction hero
  scenes (pods have 1–4 regions; bases42x27k has 4) both are cheap;
  the capture pipeline decision (synthesis D4) pushes toward (b) for
  the recorder and (a) for the interactive page.
- **G5 — road/furniture geometry at hero scale.** In PMTiles bakes the
  browser no longer holds full lane polylines (ADR-0023 §1 furniture
  note, `:121-136`); small-network bakes ship `network.geojson`
  (manifest-verified for bases42x27k). A hero scene needs ribbon
  geometry (extruded lane polylines), stop bars, head positions — all
  derivable from the metric GeoJSON export or furniture.geojson; for
  city-scale bakes a new baked artifact (or the ADR-0018 chunked export)
  must supply it. Building overlays are 2D footprints without heights
  (mkquiz context; `viz/src/theme.ts:34-37` building colors) — extrude
  flat or fixed-height.
- **G6 — signal mutation + scoring path** (§8): no endpoint, no mutable
  TSSG in baked artifacts (ADR-0023 §10).
- **G7 — a camera-path/clip format** — nothing exists; deterministic
  camera keyed to sim tick is greenfield (see standards-and-patterns.md
  §Camera).
- **G8 — HUD/quiz chrome for the 3D page** — new but small; the theme
  tokens (`viz/src/theme.ts:41-46` hudBg/hudBorder/hudText…) and the
  `?bare=1` clean-canvas pattern (`viz/app.html:26-27`) carry over.
