# ADR-0018: Chunked network GeoJSON for city-scale viz

- **Status:** ACCEPTED
- **Date:** 2026-07-24

## Context

The viz fetches the static network as one GeoJSON document. City-scale
imports (ADR-0017) made that document unparseable in a browser: V8's max
string length (~537M chars) rejects any `response.json()` over it —
sf-lean is 188 MB at ~622 B/lane, la-lean ~860 MB, and full-residential
LA would be ~2 GB. The serving side already solved the same class of
problem twice on other planes (ADR-0015 keyframes, ADR-0016 signal
tables): chunk, never raise a size ceiling.

Options considered:

1. **Chunked manifest over the existing HTTP path** (chosen). Same
   `/net/{id}.geojson` endpoint, same content-keyed dev cache; small
   networks are served byte-identically to before.
2. **Vector tiles (PMTiles/tippecanoe).** The right answer for
   zoom-tiered rendering, but it changes the client's source model and
   the congestion feature-state contract — a much larger migration than
   the immediate parse wall justifies.
3. **Display-filtered lanes (drop residential from the export).** Loses
   the street texture and the stop/signal points that live on minor
   roads — the features the city shots exist to show.

## Decision

When the single-file export would exceed `geojsonChunkThreshold`
(256 MiB — half the V8 cap), demosrv serves a MANIFEST at
`/net/{id}.geojson`: a FeatureCollection with the `frame` foreign
member, an empty `features` array, and a `parts` foreign member listing
part URLs in lane order:

```json
{"type":"FeatureCollection","frame":{...},
 "parts":["/net/{id}.geojson.{schema}.{hash12}.part-000", ...], "features":[]}
```

- Each part is a standalone FeatureCollection with the same `frame` and
  a lane slice under the threshold (recursive halving for feature-heavy
  ranges; a single lane may exceed it — the contract is per-document
  parseability).
- Part URLs carry the exporter schema version AND the network-bytes
  hash (`{schema}.{hash12}` — sha256 over the exact bytes the export is
  generated from, so a mid-generation edit can never cache new bytes
  under an old key; a manifest-only scenario edit produces the identical
  export and intentionally shares the generation): the handler validates
  both and 404s a stale generation, so an exporter deploy or network
  edit between the client's manifest and part fetches refetches the
  manifest instead of silently mixing two generations. Single-file mode
  was atomic per fetch; this preserves that guarantee.
- The viz (`viz/src/netload.ts`) fetches the manifest, then parts
  SEQUENTIALLY (parallel 500 MB downloads would spike the tab), and
  concatenates features; absent `parts` the single-document path is
  unchanged.
- Cache discipline is unchanged: content-keyed filenames, atomic
  temp+rename writes, parts written before the manifest so a killed
  generation never serves a half-set. The schema-version const (`v2`)
  covers the `row`/`junction` lane properties the stop-sign layer
  needs.

## Related: unique live run ids

The same city-scale work hardened the demosrv startup path in ways worth
recording with this ADR's serving story:

- **Live demos spawn with a per-spawn unique run id** (`{run}-{nonce}`):
  the run id is the registry key AND the snapshot-subject namespace, so a
  foreign broker (another session's engine, a stale server on the shared
  ws port) can never hold our key. Readiness probing
  (`natsio.ProbeRunMeta`) thus proves ownership exactly — the recurring
  "viz attaches to the wrong engine" incident class — without a nonce
  contract change. `/api/status` exposes the live run id; the menu's
  running-card deep link threads it.
- **Two-phase readiness**: TCP port probe (fast fail on child death via
  the done channel), then registry identity (status `running` +
  `CreatedUnix` at/after spawn). Identity timeout is 660 s, covering
  la-lean's ~400 s world build; `-attach-timeout 600s` covers the
  embedded driver's first exit-routing pass.

## Consequences

- la-lean (1.38M lanes) loads in the browser in ~150 s (4 parts,
  ~2 GB heap). Full-residential LA (3.1M lanes, ~2 GB of JSON) remains
  out of reach for the live map — its pictures come from
  `scripts/render-city.py` (server-side static renders).
- The known next wall is per-frame state, not the static network: TSSF
  snapshots grow with fleet size (ADR-0016 §8 documents it).
