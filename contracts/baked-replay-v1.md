# Baked replay wire formats v1 (ADR-0023)

The static replay plane's engine↔viz artifacts, produced by
`engine/cmd/bake` and consumed by the viz's baked-mode shim
(`?bake=<index.json URL>`). Co-migration per ADR-0006: the in-repo viz is
the only reader. Nothing here rides NATS — `contracts/asyncapi.yaml` is
untouched.

All multi-byte integers are little-endian.

## TSRB v1 — baked vehicle snapshot frame

```
header (20 B): magic u32 "TSRB" | schema_version u16 =1 | flags u16 =0 |
               tick u64 | vehicle_count u32
per vehicle (14 B): id u32 | x u32 | y u32 | angle u8 | class u8
```

- A **chunk** (`frames/{region}/c{seq}.tsrb.br`) is `header+records`
  repeated; `vehicle_count` is the only frame delimiter. Chunks are stored
  brotli-precompressed (`Content-Encoding: br`) and always fetched whole.
- `x`/`y` are quantized to `index.json.quant.xyStepM` (0.1 m) steps in the
  network's LOCAL METRIC FRAME (the frame TSSF uses), biased by
  `index.json.quant.origin`: `c = origin + q × step`. The origin is the
  network bbox min minus a 100 m margin (the margin covers the ≤ one-lane-
  width perpendicular shift of projected vehicle points off the polyline
  bbox; it is carried in the index, so it is decoder-visible).
- `angle`: tangent normalized into [0, 2π), then `floor(n × 256 / 2π)`;
  decode by multiplying back (`q × 2π / 256`). No reserved values.
- `id` is the engine id narrowed to u32, `class` the scenario type index
  narrowed to u8. The bake ABORTS above MaxUint32 / 255 — the narrowing is
  guarded, never assumed.
- Bake rate 2 Hz (`index.json.bakeEveryTicks` = 5 ticks at dt 0.1). Every
  frame is a keyframe. Frames bake at `tickStart + k×stride`, plus a
  TERMINAL frame at `tickEnd` when it is off-stride; `tickEnd` is
  INCLUSIVE.

## TSRL v1 — baked lane-speed aggregate frame

```
header (20 B): magic u32 "TSRL" | schema_version u16 =1 | flags u16 =0 |
               tick u64 | pair_count u32
per pair (5 B): lane_idx u32 | ratio_q u8
```

- Sparse: only lanes with ≥1 vehicle at the aggregate tick. Pairs are
  sorted by `lane_idx`.
- `lane_idx` indexes `lanes.json` — the deduped occupied-lane-id table
  (network-format v1 id strings, first-appearance order).
- `ratio_q = round(clamp(meanSpeed / speedLimit, 0, 1.5) × 170)`: the
  instantaneous per-lane mean speed at the aggregate tick over the
  vehicles on the lane, divided by the lane's speedLimit. (Lanes with
  speedLimit ≤ 0 — none exist in compiled networks — are skipped.)
- Aggregate cadence 0.2 Hz (`index.json.laneEveryFrames` = 10 baked
  frames). Lane→region ownership is by the lane's home tile (its
  midpoint's z11 tile), NOT by vehicle position.

## Regions and chunk windows

- Region key = web-mercator z11 tile, `"z11/{x}/{y}"`; object directories
  replace slashes with dashes (`z11-352-819`). `bbox` in the manifest is
  the tile's WGS84 bounds `[west, south, east, north]`.
- Time window = 120 TSRB frames / 12 TSRL frames (60 s). Each (region,
  window) is ONE object. A region's chunk list is CONTIGUOUS from window
  0: regions discovered mid-bake are backfilled with header-only chunks,
  and a window whose frames are all empty is still a chunk (header-only
  frames are 20 B). A chunk-list gap is a manifest bug.
- Chunk tick coverage is `[tickStart, tickStart + frameCount×stride)`;
  the final chunk may be short.

## index.json

The manifest. Fields per ADR-0023 §5: `version` (1), `run`,
`scenarioHash`, `dt`, `frame` (`projection` + `netOffset`, the projector
input), `bakeEveryTicks`, `laneEveryFrames`, `tickStart`, `tickEnd`
(inclusive), `quant` (`xyStepM` + `origin`), `network` (`pmtiles` absolute
URL + `layer: "lanes"` + `promoteId: "id"`, OR `geojson` + `promoteId`),
`bounds` (WGS84), `overlays` (may be empty), `signals` (`url` +
`chunkBytes` — the concatenated TSSG set's framing: each chunk is a
complete TSSG v1 frame of that byte length), `laneIds` (`lanes.json`),
`regions[]` (`key`, `bbox`, `frames[]`, `lanes[]` — a region may carry
only one list: vehicle frames land where vehicles are, lane speeds where
lane midpoints live, which is why the shim inflates TSRL fetches by one
tile ring).

`furniture.geojson` is produced by the viz-side node step
(`viz/scripts/bake-furniture.mjs`), not by bake; the manifest field is
reserved for it.

## Content keys

- Bake prefix: `baked/{run}/{hash12}/` — sha256 (first 12 hex) over the
  recording stream name + run id + scenario hash + seed + tick horizon +
  the record digest (sha256 over the log messages as consumed, in stream
  order) + overlay bytes + the bake-config digest (cadences, chunk
  lengths, quant step, brotli quality, minzoom policy, format versions,
  bake-tool version).
- `network.pmtiles`: `city/{hash12}/network.pmtiles` — sha256 over the
  network bytes + tippecanoe version + the exact flag set + the minzoom
  policy + the projection + the edgeB rule + the exporter version.
- index.json is fetched `cache: "no-cache"`; all other objects are
  immutable forever. `.tsrb.br`/`.tsrl.br` objects serve with
  `Content-Encoding: br`; `network.pmtiles` serves IDENTITY with Range
  support (ADR-0023 §8's deployment contract).
