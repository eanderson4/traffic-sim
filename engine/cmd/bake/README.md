# bake — the ADR-0023 baked replay pipeline (Go side)

One invocation bakes one recording into the static replay artifacts the
public site serves from R2:

```
bake -store DIR -run RUN -out OUTDIR [-overlays DIR] [-net-format auto|geojson|pmtiles]
```

- `-store` is a durable JetStream store dir written by `serve -store`.
  **The recording serve must have exited first** — exactly one broker may
  open a store dir at a time.
- Output lands under `{out}/baked/{run}/{hash12}/` (content-addressed,
  immutable — ADR-0023 §8): `index.json`, `frames/{region}/c*.tsrb.br`,
  `lanes/{region}/c*.tsrl.br`, `signals.tssg`, `lanes.json`, `overlays/*`
  (when `-overlays` is given), and `network.geojson` on the GeoJSON path.
- Divergence policy is the audit path's: any CRC or verb mismatch ABORTS
  the bake and removes the staging dir; a completed bake is provably the
  recorded run at 2 Hz decimation.

Wire formats: `contracts/baked-replay-v1.md` (TSRB v1, TSRL v1,
index.json).

## The PMTiles step (tippecanoe)

City-scale networks (auto: > 100,000 lanes, or `-net-format pmtiles`)
bake `network.pmtiles` with the pinned EXTERNAL binary:

- **tippecanoe 2.78.0** (felt/tippecanoe). Install a release from
  https://github.com/felt/tippecanoe (OS packages build it; do not run the
  install with sudo from bake — bake only *invokes* the binary). The
  binary's `--version` banner rides the per-city content key, so tiles
  produced by a different build never share a URL.
- bake exports the network as newline-delimited **WGS84** GeoJSON
  (`city/{hash12}/network.geojson.ndjson`, kept next to the tiles) with the
  standard lane property block plus `edgeB` and a per-feature
  `"tippecanoe": {"minzoom": N}` member (freeway-class speedLimit ≥ 22 m/s
  → z8; arterial ≥ 12 m/s → z11; everything else and junction internals →
  z13), then runs:

```
tippecanoe -Z 8 -z 13 \
  --no-feature-limit --no-tile-size-limit --no-line-simplification \
  --preserve-input-order --no-tile-stats \
  -l lanes --force -o network.pmtiles network.geojson.ndjson
```

The `--no-*` flags disable every behavior that destroys per-lane identity
(feature dropping, tile-size shedding, line coalescing): lane ids are the
congestion feature-state key — a dropped or merged lane is a lane that
never colors (ADR-0023 §1).

If tippecanoe is absent, bake fails BEFORE the re-simulation with an
"install tippecanoe" message; small networks can always bake with
`-net-format geojson`.

## Notes

- Chunk objects are written brotli-precompressed (quality 9) and served
  with `Content-Encoding: br`; `network.pmtiles` is stored and served
  IDENTITY (the pmtiles client range-requests into it — ADR-0023 §8's
  deployment contract).
- The furniture step (`furniture.geojson`) is the viz-side node script
  (`viz/scripts/bake-furniture.mjs`), not this tool.
- Chunk writes stream per (region, window) — peak memory is one open
  window per region, not the whole frame stream.
