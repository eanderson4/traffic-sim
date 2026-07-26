Review of ADR-0023 (design round, no files modified). Citations verified against the repo; the shim seam, player re-sim citations, size arithmetic for frames, and the PMTiles/tippecanoe claims check out. Findings, ranked:

## Blockers

**B1 — TSRB has no vehicle_count / frame delimiter; a chunk is unparseable.** ADR-0023:133-137 defines `header (16 B): magic|version|flags|tick` + 14 B records, and ADR-0023:169-170 puts 120 concatenated frames in one object. Without a per-frame count (TSSF has `vehicle_count u32`, frame.go:39-41; the spec's own TSRL has `pair_count u32`, ADR-0023:195-196) the decoder can't find where frame N ends and N+1 begins. Add `vehicle_count u32` (or a chunk-level frame directory, which would also buy in-chunk random access for seek).

**B2 — index.json omits the local-frame descriptor; the PMTiles path can't place vehicles at all.** The shim emits synthetic TSSF with local-metric x/y (ADR-0023:139-145, 246-248) and claims "the shim re-projects exactly as it does today" — but "today" the projector is built from the network GeoJSON's `frame` foreign member: main.ts:201-202 throws without `net.frame`, and proj.ts:14-17 needs `projection` + `netOffset`. In PMTiles mode there is no network GeoJSON, and the index schema (ADR-0023:211-230) carries only `quant.origin`, which is a bbox bias, not a projection. index.json must carry `frame: {projection, netOffset}` (this also unblocks metric `furniture.geojson`, see S5).

## Should-fix

**S1 — Zoomed-out TSRL bandwidth claim is off ~20–40×.** ADR-0023:337-338 claims the city view pulls "≈ 0.25 MB/min across all regions," but the spec's own table (ADR-0023:331) has lane speeds at 450 MB raw / 80–150 MB br per 15 min = 30 MB/min raw, ~5–10 MB/min compressed at the default 2 Hz aggregate cadence (ADR-0023:203-205). Either the default `strideFrames` must be much coarser (~0.1–0.2 Hz is plenty for a congestion heatmap) or the claim corrected; as written the headline justification for "aggregates alone below z12" rests on a wrong number.

**S2 — Baked-mode viz delta is materially understated.** Consequences (ADR-0023:363-366) claims the only additions are decoders + shim + PMTiles source, but main.ts's startup path assumes full network geometry in memory: `LaneIndex` (main.ts:261-266), `speedLimitByLane` (267-270), `shapeByLane`→`signalHeads` (272-275, signals.ts:147), `stopSigns` (279-286), `edgeBoundaries` (237-243), and map `bounds` fitting (256-257). PMTiles mode needs explicit branches: a TSRL-driven congestion applier replacing `updateCongestion` (main.ts:1019-1042), furniture.geojson as the signal/stop-sign source with a head-id→program join (heads carry `program` for `headStatesAtTick`, signals.ts:45,207), bounds from the index, and a minzoom gate on the vehicles layer (none exists today — without it, dots frozen at the last fetched frame persist below z12). Enumerate them now or implementation discovers them one at a time.

**S3 — `edgeB` is client-derived and won't exist in PMTiles features.** The casing paint reads `["get","edgeB"]` (main.ts:552), stamped client-side by `edgeBoundaries` over the full feature set (main.ts:237-248). Item 7's property story (ADR-0023:280-283) covers only geojson.go's block, which has `edge`/`edgeIndex` but not `edgeB`. The coalesce default `true` means city-scale casing regresses to full-opacity everywhere. Bake must stamp `edgeB` (and any other derived props) into the tiled features.

**S4 — `setFeatureState` against a vector source requires `sourceLayer`.** ADR-0023:266-268 says lane coloring rides "the same `setFeatureState({source:"network"},…)` channel" — main.ts:1035,1039 omit `sourceLayer`, which is fine for GeoJSON but mandatory for vector sources; layer specs likewise need `source-layer`. Small, but it breaks the "unchanged channel" claim; note the branch.

**S5 — bake-furniture.mjs input frame and scale are unspecified, and both bite.** The derivations cluster by metric distances (signals.ts head clustering, stopsign approach clustering), so the step must run on the METRIC export, not the WGS84 file bake feeds tippecanoe (ADR-0023:92-97 vs 103-110); output coordinate space is also unstated (metric works iff B2's frame descriptor lands). And at la-lean the single-doc network exceeds V8's ~537M-char string cap — the exact wall netload.ts:10-12 documents — so the node step must consume chunked parts or stream.

**S6 — Concatenated `signals.tssg` isn't self-delimiting at the API the shim uses.** The accumulator takes whole-chunk payloads with synthesized `sig_chunk i/n` headers (ADR-0023:249-251); TSSG chunks are "complete v1 frames" (tssg.ts:3-7) whose length is only discoverable by full structural parse. Store per-chunk byte lengths in the index (or one object per chunk) so the shim can split without parsing.

**S7 — hash12 inputs omit bake parameters.** ADR-0023:299-302 hashes stream name + run id + scenario hash + format version. Rebaking the same run at 1 Hz vs 2 Hz, different chunk length, quant step, or minzoom map produces different bytes under the same "immutable" key. Include a bake-config digest.

**S8 — 2 Hz frames vs the 250 ms interpolation buffer = visible stutter.** `SnapshotBuffer.sample` holds (starved) whenever renderAt passes the newest frame (snapshots.ts:103-105); with 500 ms frame spacing and the default `bufferMs` 250 (config.ts:58, main.ts:303), vehicles lerp for ~250 ms then freeze ~250 ms, every frame. The "0.5 s source spacing still reads smooth" claim (ADR-0023:155-157) silently depends on baked mode raising bufferMs above the frame interval (≥ ~750 ms at 1×, scaled down with playback speed). State it.

## Nits

- ADR-0023:85-86 cites replay main.go:72-81 as "no ws listener" — those options include `Websocket: server.WebsocketOpts{…}` (main.go:76). Bake diverges from replay here; fix the citation.
- ADR-0023:143-144 "u32 spans 429 km": 2³² × 0.1 m ≈ 429,000 km. The 4×-headroom framing is u22 arithmetic. Benign (more headroom), but wrong.
- ADR-0023:163-165 z11 tile "~19.5 × 16 km" at LA: mercator tiles are locally square on the ground — ~16 × 16 km at 34°N.
- ADR-0023:248-249 "SeekGate sees the shim's own seeks as tick jumps… for free" — only backward jumps or forward >24 sim-s trip the gate (snapshots.ts:184-187). Panel-driven seeks are actually covered by `onSeeking` firing pre-POST (main.ts:1048-1053, 1067); the stated mechanism is the backstop, not the cover.
- Item 1's layout (ADR-0023:116) puts `network.pmtiles` inside the per-run content prefix, while item 8 (ADR-0023:307-308) says it's content-keyed per city and shared — the index entry should be the city-keyed URL, not a prefix-relative name.
- Overlays (water/zones/boundaries/buildings — loaded from `/overlay/*` today, main.ts:209-223) appear in neither the artifact layout nor index.json; say where the public page gets them.
- ADR-0023:227 fencepost: `tickStart:0, tickEnd:600` inclusive at stride 5 is 121 frames, not 120; define whether chunk N+1 starts at 600 or 605.
- `?run=` "inert" (ADR-0023:260-261) but ReplayPanel binds `expectedRun: cfg.run` (main.ts:1066) and the HUD displays it — note the stub echoes the index's run and cfg.run should default from it.

## Questions

- Are "~50k vehicles" and "~30 non-empty z11 regions" for la-lean measured from a real run or estimated? Both drive the chunk-size sweet spot and the 1–8 MB/min claim.
- Item 4 says baked congestion is authoritative `(laneId, speed)` "read directly from engine state" — the bake's frame sink sees re-simulated engine state, so this is a new export surface on the shared re-sim core (beyond what Player publishes). Worth one sentence on where lane occupancy/speed is sampled (per-tick mean over the window vs instantaneous at the aggregate tick?), since it changes what the heatmap means.

Everything else held up under verification: player.go re-sim/seek citations (416-440, 461-495), the loud-vs-abort divergence split (player.go:27-31 vs the spec's abort-on-bake policy — right call), TSSF layout and sequential-id claims (frame.go:37-50, tssf.ts:11-17), the `subscribeSnapshots`/`FetchLike` seams (nats-client.ts:18-24, replaypanel.ts:33-52), frame-size arithmetic for TSRB/i280/la-lean, ADR-0016's 1.2 MB wall, ADR-0018's 860 MB / 150 s / hash12 discipline, ADR-0020's unauthenticated-ws precondition, maplibre-gl 5.24.0 lacking a bundled pmtiles protocol (package.json), and tippecanoe's per-feature minzoom extension. The overall shape — offline strict re-sim to static quantized full-snapshot chunks + PMTiles + a transport shim at the narrowest existing seam — is sound and consistent with the repo's decision history.

REVIEW-COMPLETE
