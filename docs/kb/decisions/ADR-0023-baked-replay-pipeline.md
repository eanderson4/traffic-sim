# ADR-0023: Baked replay pipeline — static replays for the public site

- **Status:** PROPOSED (external design review rounds 1–3 incorporated)
- **Date:** 2026-07-25

## Context

phantomjam.com is a static Cloudflare Pages site. The live-sim server is
parked, and ADR-0020's deployment precondition records WHY the live plane
cannot front the public MVP: the engine's WebSocket broker is
unauthenticated (accepts publishes as well as subscriptions from any
client) and needs an auth/permissions design before it may be exposed
(ADR-0020 Consequences, 2026-07-25). The public demo therefore replays
recordings entirely in the browser: baked artifacts in an R2 bucket
behind `https://data.phantomjam.com`, no server at runtime.

The inputs already exist. The record plane (ADR-0006 §4–§5, ADR-0015) is
a JetStream stream per run — arbitrated intent log, chunked keyframes,
rolling CRC, director verbs (`ts.{run}.log.{intent,keyframe,crc,event,verb}`,
engine/natsio/server.go:152-163) — and `engine/cmd/replay` already
re-simulates it into the live plane for a demo audience
(engine/natsio/player.go:19-25). The viz consumes exactly two wire
formats plus static artifacts: TSSF v1 snapshot frames (24 B header +
24 B/vehicle, engine/natsio/frame.go:37-50; TS mirror viz/src/tssf.ts:14-17),
the TSSG signal-program table (viz/src/tssg.ts, ADR-0016), and the
network as chunked GeoJSON (ADR-0018, viz/src/netload.ts).

Two walls make "just point the viz at the recording" a non-answer:

1. **The live plane's shapes assume a server.** TSSF grows ~24 B/vehicle
   at ~10 Hz — ADR-0016 §8 measures ~1.2 MB/frame at 50k vehicles and
   documents the wall as unsolved; ADR-0018 measures la-lean's network
   GeoJSON at ~860 MB, loadable only via the chunked-manifest path in
   ~150 s. Neither is servable to a casual web visitor.
2. **The viz has no transport abstraction.** main.ts calls
   `subscribeSnapshots(ws, run, onFrame, onSignals, onStatus)`
   (viz/src/nats-client.ts:18-24) and drives replay controls through
   demosrv's HTTP proxy (viz/src/replaypanel.ts:33-52). A baked replay
   must slot into these shapes, not fork the app.

This ADR designs the bake pipeline within decisions already made with
the user: PMTiles for the static network (not per-frame MVT), chunked
full-snapshot frame streams at 1–2 Hz, spatial partitioning by coarse
tile, lane-speed aggregates for the zoomed-out view, and a browser shim
in viz/ implementing the live transport's interface.

Options considered:

1. **Serve the recording's live-plane stream as static files** (capture
   replay's ws output to disk). Rejected: it bakes in the live plane's
   sizes (TSSF at 10 Hz, whole-world frames), and capture-at-playback-
   pace is a needlessly slow, pace-coupled way to re-simulate offline.
2. **Bake offline from the record plane** (chosen). A Go tool re-runs
   the same re-simulation the Player does — keyframe restore + per-tick
   re-enqueue from the log index (player.go:416-440, 461-495) — at
   unlimited speed, with no broker listeners, no pacing, and strict CRC
   verification, writing static artifacts directly.
3. **Vector tiles baked per-frame for vehicles.** Rejected: vehicles are
   a time series, not tile data; per-frame tile pyramids multiply storage
   and throw away the interpolation machinery the viz already has
   (viz/src/snapshots.ts).
4. **Keep chunked GeoJSON for the network in baked mode.** Viable at
   corridor scale, dead at city scale (ADR-0018's own numbers); PMTiles
   additionally buys range-requested viewport fetching, which a manifest
   of 200 MB parts cannot. ADR-0018 remains the live/demosrv path,
   untouched.

## Decision

### 1. Pipeline shape: `engine/cmd/bake` (+ tippecanoe, + a node furniture step)

One invocation bakes one recording:

```
bake -store DIR -run RUN -out OUTDIR [-overlays DIR]
```

`-overlays` points at the demo's static overlay set (the files demosrv
serves at `/overlay/*` — water/boundaries/zones/buildings GeoJSON); when
present, bake copies them into the prefix and lists them in
`index.json.overlays` (item 5). Optional — a demo without overlays bakes
none, matching the live path's 404 semantic.

- **Go, in `engine/cmd/bake`** — the repo's tooling convention
  (engine/cmd/netimport, metview, simrun). It opens the recording store
  with an embedded broker of the same shape cmd/replay uses
  (`DontListen: true, JetStream: true, StoreDir`, main.go:72-81) MINUS
  the WebSocket listener — bake has no browser plane — honors the same
  store exclusivity (the recording serve must have exited, main.go:9-11),
  and re-simulates via a **new exported natsio entry point** that shares
  the Player's re-sim core (keyframe restore, `logIndex` re-enqueue,
  seek) but takes a frame sink instead of a publish bus. nats.go stays
  confined to engine/natsio (AGENTS.md). Divergence policy is the audit
  path's, not the demo's: any CRC or verb mismatch ABORTS the bake
  (player.go:27-31's loud-and-continue is for on-air survival; a
  published artifact must be exact). The bake also ABORTS if any vehicle
  id exceeds MaxUint32 — the TSRB narrowing (item 2) is guarded, never
  assumed.
- **tippecanoe (external binary) for PMTiles**, invoked by bake as a
  subprocess. bake first exports the network as WGS84 GeoJSON — a Go
  port of proj.ts's inverse-UTM projection (~60 lines, stdlib; the live
  export stays metric, engine/geojson.go:9-16) — with the existing lane
  property block (engine/geojson.go:27-36), the client-DERIVED
  properties pre-computed (`edgeB`, item 7), and a per-feature
  `"tippecanoe": {"minzoom": N}` member (tippecanoe's documented GeoJSON
  extension), then runs tippecanoe to one `.pmtiles` per city. Choosing
  an external binary over Go libraries (go-pmtiles + an MVT encoder)
  keeps AGENTS.md's stdlib-first rule for the *tiling* step, and
  zoom-tiering is tippecanoe's core competence. SUMO (tools/sumo-venv)
  is the precedent for heavy external tooling. The tippecanoe VERSION is
  pinned in the bake README and the exact flag set recorded at
  implementation; the flags must disable every behavior that destroys
  per-lane identity (no feature dropping at low zooms, no coalescing/
  merging of lines) — lane ids are the congestion feature-state key, a
  dropped or merged lane is a lane that never colors. Qualification
  criteria (review, 2026-07-26): a tippecanoe build is acceptable iff it
  honors the recorded identity-flag set AND is listed as verified in the
  bake README (today: 2.49.0, 2.78.0) — version-keying prevents cache
  collisions but does not prove an unverified build preserves lane
  identity or minzoom semantics.
- **A node step for derived furniture** (`viz/scripts/bake-furniture.mjs`).
  Signal heads and stop signs are client-side derivations from lane
  geometry + the TSSG table today (signals.ts, stopsign.ts) — and with
  PMTiles the browser no longer HOLDS full lane geometry to derive them
  from. The existing TS derivations are DOM-free by design (node --test
  loads them), so this step runs them verbatim and emits
  `furniture.geojson` (head points + stop-bar lines + stop signs, with
  each head's program binding so the viz joins heads→programs from the
  TSSG table as signals.ts does today). Two constraints (sol/Fable
  round 1): the step runs on the METRIC export — the clustering
  distances are metric — and emits metric coordinates too (the shim
  projects them like everything else, item 6); and at la-lean scale the
  single-document network exceeds V8's ~537M-char string cap
  (netload.ts:10-12), so the step consumes the ADR-0018 chunked export
  part-by-part (or a line-delimited metric dump bake emits for the
  purpose) — never one parsed document.

Output layout (index.json plus chunk/furniture objects under one
content-addressed per-recording prefix, item 8):

```
{prefix}/index.json          manifest (item 5)
{prefix}/furniture.geojson   signal heads, stop bars, stop signs (metric coords)
{prefix}/signals.tssg        the TSSG chunk set, concatenated (framing: item 5)
{prefix}/frames/{region}/c{seq}.tsrb.br   vehicle frame chunks (item 3)
{prefix}/lanes/{region}/c{seq}.tsrl.br    lane-speed chunks (item 4)
{cityPrefix}/network.pmtiles static network, content-keyed per CITY, shared (item 8)
```

### 2. Frame encoding: TSRB v1 — binary, quantized, full snapshots

A new baked-frame format (naming per TSSF/TSSG/TSKF), decoded by a new
viz/src/tsrb.ts mirror. Binary over JSON for the same reason as TSSF
(ADR-0006 §7): ~5× smaller and no parse wall at 50k vehicles/frame, and
the DataView decoder idiom already exists in tssf.ts. Full snapshot per
baked frame — a seek is a chunk fetch, never a re-simulation.

Layout (all little-endian):

```
header (20 B): magic u32 "TSRB" | schema_version u16 =1 | flags u16 |
               tick u64 | vehicle_count u32
per vehicle (14 B): id u32 | x u32 | y u32 | angle u8 | class u8
```

`vehicle_count` mirrors TSSF's self-delimiting header (frame.go:39-41):
a chunk is `header+records` repeated, and the count is the only frame
delimiter (design review B1 — the first draft omitted it and chunks were
unparseable).

- **x/y quantized** to 0.1 m steps in the network's LOCAL METRIC FRAME
  (the same frame TSSF uses, frame.go:43-47 — the shim re-projects
  exactly as it does today), biased by the network bbox origin carried in
  index.json: `q = round((c - origin) / 0.1)`. u32 spans ~429,000 km at
  that quantum — headroom is not a concern at any city scale. Sub-
  lane-width precision; a vehicle never visibly jumps.
- **angle u8**: the tangent is normalized into [0, 2π) FIRST (Go's
  `math.Mod` preserves a negative sign, so a raw `mod` on an atan2
  heading can go negative), then `q = floor(normalized × 256 / 2π)` —
  floor, not round, so the result is 0..255 and never overflows the byte (≈1.4°
  steps; decoder multiplies back, no reserved values). Heading drives
  glyph rotation only; lane-following tangents change smoothly and 1.4°
  is imperceptible at symbol size.
- **id u32, class u8**: engine ids are sequential from 1 (tssf.ts:11-12);
  the bake's MaxUint32 guard (item 1) makes the narrowing fail-loud —
  and class gets the same discipline: the bake ABORTS if any vehicle
  type index exceeds 255 rather than silently truncating.
- 14 B/vehicle vs TSSF's 24 — and the region partitioning (item 3) means
  a browser only ever decodes its viewport's share.

Bake rate **2 Hz** (every 5th tick at dt 0.1). Cadence is DERIVED from the
scenario's dt: `bakeEveryTicks = max(1, round(0.5 / dt))`, giving a baked
rate of `1 / (bakeEveryTicks × dt)`; the bake REJECTS any dt whose derived
rate falls outside the 1–2 Hz band (dt = 0.1 → 5 ticks → 2.0 Hz, valid;
all current scenarios use 0.1). The same rule derives the TSRL cadence
(`laneEveryFrames = 10` baked frames = one aggregate per 5 s at 2 Hz) and
the 120-frame chunk window (60 s at 2 Hz; in ticks it is
`120 × bakeEveryTicks`). — the top of the agreed
1–2 Hz band, because the viz's lerp machinery interpolates between
frames at render rate. One consequence the first draft glossed (review
S8): at 2 Hz the frame interval (500 ms) exceeds SnapshotBuffer's
default 250 ms buffer (config.ts:58), which would lerp 250 ms then hold
starved 250 ms, every frame. **Baked mode sizes the buffer to the
delivery cadence**: `bufferMs = max(250, 1.25 × frameInterval/speed)` —
625 ms at 1×, shrinking with playback speed — so renderAt always trails
the newest frame by more than one interval. `SnapshotBuffer.bufferMs` is
constructor-fixed and readonly today (snapshots.ts:48-57), so baked mode
adds a `setBufferMs` setter (field assignment only — never a rebuild,
which would drop interpolation history mid-play); this is part of the
enumerated viz delta (item 6). Every frame is a keyframe;
seek granularity is the bake stride, and a panel seek to an arbitrary
tick lands on the greatest baked tick ≤ the target (floor), which the
status endpoint then reports.

### 3. Spatial chunking: z11 regions, one R2 object per (region, time-chunk)

- **Region key = web-mercator z11 tile** (`z11/{x}/{y}`). Tiles are
  locally square: ~16 × 16 km at LA's 34°N, so la-lean's bbox spans
  roughly 30 non-empty regions (estimate from the scenario bbox; the
  first real bake replaces it with a count), and a z≥13 viewport
  intersects 1–4. Assignment is per baked frame by the vehicle's
  projected position; a vehicle crossing a boundary simply appears in
  the neighbor region's next full snapshot. Empty regions are omitted
  from the index. The region SET is the UNION of TSRB-occupied tiles
  and TSRL owner tiles (lane-midpoint home tiles) — a boundary-crossing
  lane whose home tile has no vehicles still has its aggregate emitted,
  into its home region (review: occupancy-only region sets could lose it).
- **Time chunk = 120 frames (60 s at 2 Hz).** Each (region, chunk) is
  ONE R2 object, stored brotli-precompressed (`Content-Encoding: br`).
  Object-per-chunk is chosen over one-file-per-region + Range requests
  because CDN on-the-fly compression and byte ranges interact badly,
  pre-compressed objects serve whole-chunk fetches, and per-object
  immutability matches the content-addressed prefix (item 8). The index
  carries chunk URLs, byte sizes, and `frameCount` — chunk tick coverage
  is `[tickStart, tickStart + frameCount×stride)` with the final chunk
  possibly short (review: the first draft's inclusive tick ranges were
  off by one frame per chunk).
- The shim fetches only regions intersecting the viewport
  (`map.getBounds()` → tile set) and merges: the synthetic TSSF frame it
  emits is the union of region frames for that tick. Merging has a
  **completeness barrier** (review B2, round 2): `SnapshotBuffer.push`
  drops duplicate/regressive ticks (snapshots.ts:73-76), so emitting
  tick T without a pending region and re-emitting T when it arrives is
  impossible — the second emission would be dropped and the region's
  vehicles permanently omitted. The rule: the shim emits tick T only
  when every subscribed region can cover T (its chunk fetched, or the
  region known-empty from the index). Initial load and pans stall
  emission while a viewport region's fetch is in flight; a bounded stall
  (2 s) degrades to emitting without the laggard, whose vehicles then
  pop in at the NEXT tick once its chunk lands — a pop at the viewport
  edge under a slow network, never a permanent hole, never a smear.
  Once degraded, the barrier STAYS degraded until the laggard's chunk
  lands — it does not re-arm per tick (a 2 s re-stall per tick would
  drop playback to a slideshow). A region's chunk list in the index is
  CONTIGUOUS over `[tickStart, tickEnd]`: bake writes a chunk for every
  window even when every frame in it is empty (header-only frames are
  20 B), so "known-empty" is decided by the region set alone and a
  chunk-list gap is a manifest bug, loud at bake time — the barrier
  never has to guess (same rule for the TSRL lists).

### 4. Lane-speed aggregates: TSRL v1, sparse, per region

The zoomed-out view colors lanes without fetching vehicle frames. Baked
aggregates REPLACE the viz's client-side derivation in baked mode — and
strictly improve it: congestion.ts re-attaches vehicles to lanes by
nearest-segment and derives speed from displacement (its own header
calls this a proxy, congestion.ts:1-9), while the bake reads (laneId,
speed) directly from engine state — the frame sink taps the shared
re-sim core's engine, a wider surface than the Player's publish bus, so
the sampling rule is stated here: **instantaneous per-lane mean speed at
the aggregate tick** over the vehicles on the lane at that tick (not a
time-window mean — the live view's semantics, and no extra state),
divided by the lane's speedLimit, quantized ratio×170 to u8 (0–1.5
clamp, mirroring laneSpeedRatios, congestion.ts:108). Generation is
deterministic: vehicles are scanned in `Engine.Vehicles()` order
(the engine's deterministic spawn/iteration order, never a Go map), and
(lane_idx, ratio) pairs are emitted sorted by lane_idx.

```
header (20 B): magic u32 "TSRL" | schema_version u16 =1 | flags u16 |
               tick u64 | pair_count u32
per pair (5 B): lane_idx u32 | ratio_q u8
```

Sparse — only lanes with ≥1 vehicle at the aggregate tick. `lane_idx`
indexes a deduped occupied-lane-id table (`lanes.json`, string ids in
the network-format v1 id space). **Lane→region ownership is by the
lane's home tile** (its midpoint's z11 tile), NOT by vehicle position:
every lane has exactly one owner region, ratios never duplicate or need
merge rules, and a lane crossing a tile boundary still colors whole
(review: the first draft left ownership undefined).

Aggregate cadence defaults to **0.2 Hz** (every 10th baked frame — a
congestion heatmap gains nothing from 2 Hz, and this is what makes the
zoomed-out bandwidth story true, review S1); the index's
`laneEveryFrames` field carries the actual value. Chunks use the same
region + 60 s scheme as item 3 (12 aggregate frames per chunk at
0.2 Hz). The shim fetches TSRL for ALL regions when zoomed out (the
whole city view) and for the viewport's region set INFLATED BY ONE TILE
RING when zoomed in — a lane's home tile can sit just outside the
viewport while the lane itself is visible (review, round 2); TSRL
chunks are small, so the halo is cheap insurance against uncolored
edge lanes. Lane coloring in baked mode
ALWAYS reads these streams (one code path, authoritative data); the zoom
gate controls only vehicle dots (item 6).

### 5. index.json

```json
{
  "version": 1,
  "run": "i280-pod-base-15m",
  "scenarioHash": "…",            // ADR-0012 content hash from the run registry meta
  "dt": 0.1,                       // recorded run's authoritative timestep
  "frame": { "projection": "+proj=utm +zone=10", "netOffset": [x, y] },
  "bakeEveryTicks": 5,
  "laneEveryFrames": 10,
  "tickStart": 0, "tickEnd": 9000,
  "quant": { "xyStepM": 0.1, "origin": [minX, minY] },
  "network": { "pmtiles": "https://data.phantomjam.com/city/{hash12}/network.pmtiles",
               "layer": "lanes", "promoteId": "id" },
  "bounds": [west, south, east, north],
  "furniture": "furniture.geojson",
  "overlays": ["overlays/water.geojson", "…"],
  "signals": { "url": "signals.tssg", "chunkBytes": [1234, 5678] },
  "laneIds": "lanes.json",
  "regions": [
    { "key": "z11/352/819",
      "bbox": [west, south, east, north],
      "frames": [ { "tickStart": 0, "frameCount": 120, "url": "frames/z11-352-819/c000.tsrb.br", "bytes": 12345 } ],
      "lanes":  [ { "tickStart": 0, "frameCount": 12,  "url": "lanes/z11-352-819/c000.tsrl.br", "bytes": 6789 } ] }
  ]
}
```

- `frame` is the local-metric-frame descriptor (projection + netOffset,
  network-format v1 provenance) the projector is built from
  (proj.ts:14-17). Without it the PMTiles path cannot place a single
  vehicle or furniture point — main.ts:201-202 throws without
  `net.frame` today (review B2). `bounds` feeds the initial map fit,
  replacing the geometry walk (main.ts:256-257).
- `signals.chunkBytes` is the concatenated TSSG set's framing: TSSG
  chunks are complete v1 frames whose length is only discoverable by a
  full structural parse, so the index carries each chunk's byte length
  and the shim splits before feeding the accumulator (review B3).
- Every frame is a keyframe (item 2), so the chunk table IS the keyframe
  index — seek = find the chunk covering the target tick, fetch, decode,
  emit.
- `tickEnd` is INCLUSIVE — the last baked tick. Frames bake at
  `tickStart + k×stride`, and when tickEnd is not on-stride the bake
  emits a TERMINAL frame at tickEnd anyway (Player parity — it lands
  exactly on the final tick past the decimation stride,
  player.go:295-298); the final chunk carries it, and the seek floor
  treats it as that chunk's last frame. A 9000-tick run at stride 5
  bakes 1801 frames, so the final chunk holds a single frame —
  expected, not corruption. The shim's end semantics mirror the Player's
  end-of-recording hold (player.go:45-50): status reports `done` at
  `tickEnd`, resume 409s until the user seeks back.
- `overlays` lists the demo's static WGS84 overlay docs (water,
  boundaries, zones, buildings — main.ts:209-223 loads them from
  `/overlay/*` today); bake copies them into the prefix when the demo
  has them. Absent = no overlays, exactly the live 404 semantic.

### 6. The browser shim: drop-in transport, plus an enumerated baked-mode delta

The transport seam is narrow and the shim exploits exactly it:

- **Transport**: `subscribeBaked(indexUrl, onFrame, onSignals, onStatus)`
  mirrors `subscribeSnapshots(ws, run, …)` (nats-client.ts:18-24) and
  returns `{ nc: null, close }` — SnapSubscription exposes `nc`
  (nats-client.ts:13-16) and main.ts stores it for the ADR-0016
  request/reply pull (`requestSig`, main.ts:976-977); baked mode leaves
  it null so every pull path no-ops by its existing guards
  (main.ts:347), which is safe because the baked TSSG set is delivered
  complete at attach. It re-encodes merged TSRB records
  into synthetic **TSSF v1 bytes** before calling `onFrame`, so tssf.ts,
  SnapshotBuffer, the artic channel, and the render loop are
  byte-for-byte untouched (main.ts:937-953). `onSignals` feeds the baked
  TSSG chunks (split via `chunkBytes`) with synthetic `sig_chunk`
  headers into the unmodified accumulator (main.ts:382-400); light
  states derive from the tick as today (signals.ts). **The clock never
  depends on vehicle fetches**: below the zoom gate — when no region
  stream is scheduled at all — the shim emits EMPTY synthetic frames
  (`vehicle_count: 0`, 24 B) at the baked cadence, so the sample tick —
  which drives the TSRL applier, signal derivation, and the HUD —
  advances in the zoomed-out view. Empty frames are emitted ONLY there:
  above the gate a region stall stalls the CLOCK (item 3's barrier),
  and the 2 s degrade path emits the partial union of arrived regions —
  never an empty frame, which `push` would count as tick T delivered and
  permanently foreclose the real frame T (review, round 3).
  On seeks: a
  shim-driven seek fires the panel's `onSeeking` hook (the pre-reset,
  main.ts:1048-1053) exactly as the demosrv path does — SeekGate stays
  what it is today, the backstop for non-panel jumps (its forward window
  only trips past 24 sim-seconds, snapshots.ts:184-187).
- **Control plane**: the shim implements play/pause/speed/seek locally
  (a frame scheduler over fetched chunks; seek floors to the baked
  stride, item 2) and exposes it as a `FetchLike` stub
  (replaypanel.ts:49-52) answering the panel's exact routes —
  `/api/replay/status`, `/api/replay/ctl/{pause,resume,speed,seek}` —
  including the ReplayStatus JSON shape (replaypanel.ts:33-45;
  `crcErrors`/`verbErrors` are 0 — a divergence would have aborted the
  bake). The stub ignores the `?run=` binding and echoes the index's run
  id; in baked mode cfg.run is display-only. main.ts injects the stub
  instead of real fetch when `?bake=` is set; ReplayPanel itself is
  unchanged.
- **Config**: `?bake=<index.json URL>` selects the shim; `?ws=` is inert
  in baked mode. The dt comes from the index's `dt` (the same authority
  the replay panel's status probe provides today, main.ts:1068-1078),
  as does the cadence-sized bufferMs (item 2).

The baked-mode delta beyond the shim is enumerated, not waved at
(review S2 — main.ts's startup path assumes full network geometry in
memory):

1. **Congestion channel**: a TSRL-driven applier replaces
   `updateCongestion`'s client derivation (main.ts:1019-1042) — look up
   the GREATEST TSRL aggregate tick ≤ the current playback tick and hold
   it until the next aggregate lands (aggregates exist only every 10th
   baked frame; most ticks have no exact match). On seek, re-resolve
   against the seek target; past the final aggregate, hold it to the
   end. Diff against the previous application, `setFeatureState`.
   LaneIndex, speedLimitByLane, and
   shapeByLane (main.ts:261-275) are not built in baked mode.
2. **Furniture**: signal heads/stop bars/stop signs come from
   `furniture.geojson`, joined to TSSG programs by the baked binding —
   the `signalHeads`/`stopSigns` geometry derivations (main.ts:272-286)
   are skipped. `edgeBoundaries` (main.ts:237-243) is likewise baked in
   (`edgeB`, item 7).
3. **PMTiles source**: the vector source declares `promoteId: "id"` and
   every layer on it carries `"source-layer": "lanes"`; every
   `setFeatureState`/`removeFeatureState` identifier in baked mode
   includes `sourceLayer` (main.ts:1035/1039 omit it — fine for GeoJSON,
   mandatory for vector sources; review B4/S4).
4. **Zoom gate**: the vehicles/trailers layers get a baked-mode minzoom
   of **z13** — the same zoom at which the last road class (residential)
   appears, so dots never float over invisible streets (review: a z12
   gate preceded the z13 residential minzoom). Crossing below the gate
   also CLEARS the applied vehicle diff state; today nothing ever hides
   the layer, so without this the last fetched frame would freeze on
   screen. Below the gate the shim schedules no frame fetches; at/above
   it subscribes the viewport's region set and refetches on pan
   (debounced).
5. **Startup**: bounds from the index, projector from `frame`, overlays
   from `overlays`; the network fetch (netload.ts) is skipped entirely
   on the PMTiles path.
6. **SnapshotBuffer**: gains a `setBufferMs` setter (item 2) so the
   interpolation buffer tracks the baked cadence × playback speed
   without a rebuild; `SnapSubscription.nc`'s type widens to nullable
   (nats-client.ts:13-16 — strict mode rejects the literal otherwise).

### 7. PMTiles network: zoom cutoffs, promoteId, baked properties

- Road-class minzoom defaults, stamped per feature from the compiled
  network's own attributes (speedLimit, internal): **freeway-class
  (speedLimit ≥ 22 m/s) → z8; arterial (≥ 12 m/s) → z11; everything else
  → z13; junction internals → z13** (mirroring the viz's existing
  internal-layer gate, main.ts:594). Starting defaults — verified
  visually on i280 and la-lean at implementation time and tuned once,
  not a per-city knob.
- The tiled features carry the GeoJSON property block
  (engine/geojson.go:27-36) PLUS `edgeB` pre-computed by the same rule
  as edges.ts (the casing paint reads `["case",["boolean",["get","edgeB"],true],…]`,
  main.ts:552 — the boolean-get DEFAULTS to true, so an unstamped city
  network would render full-opacity casing everywhere — review S3).
- Lane identity: the `id` property (network-format v1 durable id)
  survives as a feature property; `promoteId: "id"` makes
  `setFeatureState` color lanes as on the GeoJSON source, tile-split
  pieces of one lane sharing the id and its state.
- The viz gains a `pmtiles` source in baked mode (MapLibre GL 5.24 does
  not bundle the protocol — grep of the vendored dist finds only a doc
  reference — so the small official `pmtiles` JS package becomes a viz
  dependency, registered via `maplibregl.addProtocol`; justified as the
  standard MapLibre companion under ADR-0003's MapLibre-first rule).
  The pmtiles client uses HTTP Range requests against R2 — see the
  deployment contract in item 8.
- Small networks MAY keep plain GeoJSON in baked mode (`network`
  manifest entry carries either `pmtiles` or `geojson`) — i280's 375
  lanes / 287 KB need no tiles; the PMTiles path exists for city scale.
  One rendering contract either way: source id `network`, `promoteId`,
  the same property names (GeoJSON mode derives edgeB client-side as
  today; PMTiles mode reads it baked).

### 8. Naming, pinning, serving

- Bakes are immutable and content-addressed: object keys
  `baked/{run}/{hash12}/…`. hash12 is sha256 over a canonical document
  covering BOTH the bake identity AND the output bytes, computed AFTER
  the outputs exist (bake → digest → key → upload):
  (a) input identity — recording stream name + run id + scenario hash +
  **seed + tick horizon** (run identity is (content-hash, seed),
  ADR-0012; the scenario hash alone excludes seed and ticks) + a
  **record digest** (sha256 over a length-framed sequence of every
  consumed log message: u64be stream sequence, u32be subject length +
  subject, u32be semantic-header block length + block (tick, kf_chunk,
  sig_chunk where present), u32be payload length + payload, in stream
  order — framing makes concatenation ambiguities impossible and binds
  subjects/headers, not just payloads) + **overlay bytes** +
  **bake-config digest** (bake rate, aggregate cadence, chunk length,
  quant step, minzoom map, format versions, bake-tool version);
  (b) output identity — the sha256 of every emitted object (chunks,
  index.json excluded, furniture), sorted by object path. (b) closes
  the GOARCH/brotli-implementation hole: different artifact bytes —
  however produced — always key differently, so an immutable URL never
  silently changes content. index.json is fetched `cache: "no-cache"`
  (netload.ts:61's rule); chunk/furniture/tile objects are immutable
  forever. The site pins the full index URL per replay page.
  **MVP deferral (recorded 2026-07-26, external review):** (b) is NOT
  yet implemented — hash12 covers (a) only, so a re-bake of the same
  recording with different brotli/furniture/tile bytes can collide onto
  one key. Accepted for the episode MVP because every bake to date is
  produced by one tool build and uploaded once; (b) lands before any
  re-bake-over-existing-key workflow. Greppable TODO(MVP-deferred)
  markers in engine/cmd/bake/bake.go.
- `network.pmtiles` is content-keyed per CITY and shared by all
  recordings of that network — la-lean's tiles bake once. Its key covers
  everything the tile bytes depend on: network bytes (ADR-0018's hash12
  rule) + tippecanoe version + the exact flag set + the minzoom policy +
  the projection + the derived-property rule set (`edgeB`) + the
  bake-tool/exporter version (exporter changes move the tile bytes); tiles
  produced by a different tool build or policy MUST NOT share a URL
  (review, round 2). The manifest's `network.pmtiles`
  is therefore an absolute URL, not prefix-relative (item 5).
- **Deployment contract** for `data.phantomjam.com` (R2 + CDN, plumbing
  outside the repo, same split as ADR-0020's manifests note): CORS open
  to the site origin allowing `GET`/`HEAD` and the `Range` request
  header, exposing `ETag`, `Content-Range`, and `Accept-Ranges`; Range
  requests MUST be honored with correct 206 partial-content responses of
  exactly the requested span — the pmtiles client reads the archive by
  byte offsets, so `network.pmtiles` is stored and served IDENTITY (no
  `Content-Encoding`: range offsets into a compressed representation
  don't address the file's logical bytes, and CDNs may transcode; the
  MVT tiles inside are already compressed). The `Content-Encoding: br`
  rule applies ONLY to the pre-compressed chunk objects (`.tsrb.br` /
  `.tsrl.br`), which are always fetched whole. `Cache-Control:
  immutable` on content-keyed objects.

### 9. Size budget (15-min replay, dt 0.1, 9000 ticks, 2 Hz → 1801 frames incl. tick 0)

i280-pod-base (375 lanes, ~180 vehicles — the podcast MVP):

| artifact | raw | brotli (est.) |
|---|---|---|
| frames (TSRB 14 B/veh) | 1801 × 2.5 KB ≈ 4.6 MB | ~1–1.5 MB |
| lane speeds @0.2 Hz (≤180 pairs × 5 B × 180) | ≈ 0.16 MB | negligible |
| network (GeoJSON path, 287 KB) or PMTiles | ≤ 1 MB | — |
| **total bake** | **~6 MB** | **~2–3 MB** |

la-lean (1.38M lanes, ~50k vehicles — the city-scale stress; fleet size
from the demo registry's capacity, an ESTIMATE until the first real
bake):

| artifact | raw | brotli (est.) |
|---|---|---|
| frames, whole city (TSRB) | 1801 × 700 KB ≈ 1.26 GB | ~250–400 MB |
| frames, per (region, 60 s) chunk @ ~3k vehicles | ~5 MB | ~1–2 MB |
| lane speeds @0.2 Hz (~50k pairs × 5 B × 180) | ≈ 45 MB | ~8–15 MB |
| network.pmtiles (zoom-tiered, no dropping) | 100–250 MB (est.) | — |

Without spatial chunking a viewer would need the whole 1.26 GB frame
stream; with it, a z≥13 viewport pulls 1–4 regions ≈ **1–8 MB per
viewing minute at 1×**, and the zoomed-out city view pulls only TSRL ≈
**0.5–1 MB/min across all regions** at the 0.2 Hz cadence. Brotli
figures are estimates — cross-frame redundancy at 2 Hz is high but
unmeasured; the first la-lean bake replaces them with measurements (and
tunes the 60 s chunk length if chunk sizes miss the 1–2 MB sweet spot).
For contrast: the recording store itself is 369 MB for i280-15m, and the
LIVE TSSF plane at 10 Hz would be ~10.8 GB for the same 15 minutes at
la-lean scale (2.16 GB even at the 2 Hz bake cadence — the decimation
alone is 5×).

### 10. What this deliberately does NOT build

- No server at runtime; app.phantomjam.com live-sim stays parked behind
  ADR-0020's auth precondition. The baked plane and the live plane share
  formats only at the TSSF/TSSG seam inside the viz.
- No deltas between frames (full snapshots per frame — seek without
  re-simulation beats the bytes at these scales).
- No mid-run signal-program mutation support: TSSG is static per run
  today (player.go:490-493 notes the future); when tables become mutable
  the bake format gains a per-tick table-generation field in a TSRB v2.
- No retention/lifecycle policy for R2 — bakes are additive and
  content-addressed; garbage collection is a manual, out-of-repo sweep.

## Consequences

- The public MVP ships with zero runtime infrastructure: a Pages site, an
  R2 bucket, and per-recording bakes. ADR-0020's ws-auth work unblocks
  the LIVE chapter later without invalidating anything here — the shim
  and nats-client already coexist behind one call shape.
- The viz's replay UX (ReplayPanel, SeekGate, interpolation, signal
  heads, congestion colors) is reused; the baked path's viz-side work is
  the tsrb/tsrl decoders, the shim + FetchLike control stub, the PMTiles
  source with its sourceLayer branches, the TSRL congestion applier, the
  furniture join, and the z13 vehicle gate — enumerated in item 6 — plus
  one new npm dependency (`pmtiles`), justified in item 7.
- New wire formats TSRB v1 and TSRL v1 plus index.json are engine↔viz
  artifacts under ADR-0006's co-migration rule (the in-repo viz is the
  only reader); they get a `contracts/baked-replay-v1.md` at
  implementation, and `contracts/asyncapi.yaml` is untouched (nothing
  rides NATS).
- Bake correctness is pinned by the record plane itself: the re-sim
  verifies every logged CRC and ABORTS on divergence, so a published
  bake is provably the recorded run at 2 Hz decimation. The one fidelity
  loss is quantization (0.1 m / 1.4°) — below render resolution.
- The known size wall moves but is documented, not solved: a FULL la-lean
  bake is ~0.4–0.7 GB on R2 (fine — storage is cheap and fetches are
  regional), and a viewer scrubbing the whole city zoomed-in still pulls
  per-region chunks on demand. Bakes longer than ~30 min at city scale
  may need a coarser bake rate or longer chunks; that tuning is data for
  the first real bakes.
- engine/cmd/bake becomes the third consumer of the record plane (after
  ReplayFromStream's audit and the Player's demo plane); the shared
  re-sim core is extracted in natsio, keeping nats.go confinement and
  giving all three one CRC/verb/code path.

## Addendum (2026-07-26): the brotli dependency

Flagged in external review — the text above claims the tippecanoe choice
means "no new Go module dependency", and the implementation nonetheless
added one: `github.com/andybalholm/brotli v1.2.0`. The claim was about the
tiling step and has been narrowed accordingly; this records the dependency
that did land, per AGENTS.md's rule that dependencies are justified rather
than assumed.

**What it is for.** The chunk objects ship pre-compressed and are served
with `Content-Encoding: br` (see §"Content-Encoding" above). Brotli is not
in the Go standard library — `compress/flate` and `compress/gzip` are — so
producing them in-process needs either this module or a subprocess.

**Why not the tippecanoe treatment (an external `brotli` binary).** Tiling
is one invocation per city over one file; compression is thousands of
invocations over small buffers in the bake's inner loop, where per-call
process spawn dominates. It is also on the correctness path in a way tiling
is not: the compressed bytes are covered by the content key, so a
brotli-CLI version difference between machines would silently change the
published key. A pinned module makes that a `go.mod` diff.

**Why not gzip instead.** The artifacts are written once and served
indefinitely to every viewer; brotli's ~15–20% edge over gzip on these
chunks is paid for once and recovered on every fetch. Cloudflare serves
`br` natively.

**Confinement**, matching the ADR-0006 and ADR-0012 exceptions: the import
is confined to `engine/cmd/bake` (`chunks.go` and its tests). The kernel
package stays stdlib-only, and `engine/natsio` keeps its own confinement.
The bake CLI is an offline authoring tool — nothing on the live NATS path
links it.

**Consequence to accept.** Anything that reads these chunks needs a brotli
decoder. In the browser that is free (the platform decodes
`Content-Encoding: br`); a non-browser verifier needs its own, which is why
`scripts/serve-baked.py` exists to set the header rather than decompress.

## Addendum (2026-08-05): the phantomjam.com Pages deploy

The first published bakes did not land on R2/`data.phantomjam.com` as
§"Deployment contract" planned — they serve same-origin from the
Cloudflare Pages site (`phantomjam.com/baked/<run>/<hash>/`), staged by
`scripts/show/mksite.sh`. Two deviations from the contract as written:

- **`network.geojson` is stored brotli-compressed under its original
  name**, and the Pages `_headers` (`viz/public/_headers`) answers
  `Content-Encoding: br` for `/baked/*/network.geojson` — the contract's
  "br applies ONLY to chunk objects" no longer holds on this deploy. The
  driver is Pages' 25 MiB per-file cap (chishow's network is 34.8 MB
  raw). The compression happens at STAGING time (mksite step 4), not in
  the bake tool: every staged `network.geojson` is compressed uniformly
  so the header rule is truthful for the small bakes too, and browsers
  decode transparently — `fetch()` consumers see plain JSON.
- **Content-key exception, accepted:** the staged copy inside the
  content-keyed `<hash12>` directory no longer hashes to the key (the
  bake tool writes plain JSON; the staged file is its recompression).
  The decoded content is byte-identical, so the replay semantics the key
  protects are unchanged; a verifier must decompress before hashing.

`Cache-Control: immutable` on `/baked/*` ships in the same `_headers`,
with the manifest excepted (`/baked/*/index.json` gets no Cache-Control
— the viz fetches it no-cache and a stale pinned table must never
survive in a browser cache).
