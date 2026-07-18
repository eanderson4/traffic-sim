# Compiled Network Format v1

- **Status:** v1, current. Written by `engine/cmd/netimport` (the netconvert
  bootstrap, ADR-0009 §1); read by the engine via `NetSpec{Kind: "file"}`
  (`engine/netfile.go`).
- **Migration:** this is the *compiled* lane list of the future scenario
  format — when the authoring ⇄ compiled duality of arch-road-graph-model #5
  lands, v1 networks migrate into the scenario's network part. It is
  deliberately lane-centric: junction typing, conflict sets, and right-of-way
  are **not** here (see Limitations).
- **Licensing:** networks converted from OSM-derived `.net.xml` are ODbL
  Derivative Databases (ADR-0009 §5). The repo distributes the *recipe*
  (below), not the files; `data/` is git-ignored.

## Top level

```json
{
  "version": 1,
  "name": "i280-woodside",
  "provenance": {
    "source": "netimport (netconvert 1.27.1 .net.xml, eclipse-sumo PyPI)",
    "sourceFile": "i280.net.xml",
    "imported": "2026-07-18T01:52:02Z",
    "osmBbox": "37.42,-122.30,37.45,-122.25",
    "projection": "+proj=utm +zone=10 +ellps=WGS84 +datum=WGS84 +units=m +no_defs",
    "netOffset": [-563605.22, -4142389.64],
    "notes": "..."
  },
  "lanes": [ ... ]
}
```

`version` must be `1`. `provenance` is the import recipe: importer stamp,
source file, import date (RFC 3339), the OSM extract bbox `S,W,N,E` when
OSM-derived, and the local metric frame (`projection` + `netOffset`, copied
from the `.net.xml` `location` element). Lane order in the file is canonical:
it fixes `Lane.Index`, which the state CRC hashes — re-imports produce new
files, not edits.

## Lane object

```json
{
  "id": "n27945473_1_0",
  "section": "27945473#1",
  "edge": "27945473#1",
  "edgeIndex": 0,
  "length": 97.5,
  "speedLimit": 29.06,
  "width": 3.5,
  "shape": [[563.2, 12.8], [610.4, 33.9]],
  "latOffset": 0,
  "successors": ["i30677895_0_0"],
  "origin": false, "exit": false, "endWall": false, "internal": false,
  "source": { "edge": "27945473#1", "lane": 0, "guessed": ["width"] }
}
```

| field | semantics |
|---|---|
| `id` | **Our durable ID** (two-tier identity, ADR-0009 §3): `n<edge>_<laneIndex>` for normal lanes, `i<edge>_<laneIndex>` for junction-internal ones, sanitized to `[A-Za-z0-9_]`. Sanitize collisions get a `_d<N>` suffix in document order (e.g. SUMO edges `276909112` and `-276909112`). Source IDs ride in `source`, never in `id`. |
| `section` | Metrics grouping: the source edge id; `j:<junction>` for internal lanes. |
| `edge`, `edgeIndex` | Lateral-chaining group and position within it (**0 = rightmost**, SUMO convention). The loader derives `Left`/`Right` only between *consecutive* `edgeIndex` values of one edge — a filtered-out lane (sidewalk, bike lane) never links across — and only when the two lanes have equal `length`. Internal lanes carry no `edge`: no lane changes inside junctions. |
| `length` | m. `> 0`. |
| `speedLimit` | m/s. `> 0`. |
| `width` | m. Cosmetic in v1 physics; `0` loads as 3.5. |
| `shape` | Per-lane **centerline polyline** as `[x, y]` pairs in the local metric frame (north-up, +x east), ≥ 2 points. Geometry is display-only: it never feeds the dynamics or the CRC. |
| `latOffset` | m, left-positive, applied perpendicular to the local tangent at projection. `0` for netconvert output (its lane shapes are already centerlines); the field exists for future edge-referenced geometry (offset = lane index × width from a shared centerline). |
| `successors` | Lane IDs, **ordered left-to-right** (first = leftmost = the default route; a held turn of +1 takes the first, −1 the last — `pickSuccessor`). netimport ranks by the connection `dir` attribute (`t < l < L < s < R < r`, right-hand traffic), ties keep document order. |
| `origin` | Demand portal: the spawner injects here (`Network.Origins`, file order). Set by netimport on lanes with no predecessors. |
| `exit` | Map edge: vehicles despawn past `length`. Set by netimport on lanes with no successors. |
| `endWall` | Dead end: virtual standing vehicle at `length` (the lanedrop merge primitive). netimport never sets it; for hand-authored files. |
| `internal` | Junction-internal lane (intersection interior): geometry through the junction box, exactly one successor, no lateral neighbors, always `guessed: ["internal-geometry"]` (no OSM element underlies it). |
| `source.guessed` | Every default-filled / heuristic field, by name: `width` (SUMO's 3.2 m default when the attribute is absent), `internal-geometry`. |

End-of-lane contract (loader-enforced, fail-loud): exactly one of
successors / `exit` / `endWall` per lane; no `exit`+`endWall`; no
self-successors (file networks are open — general cycles like roundabout
circuits are allowed; use the in-code `ring` for a closed loop).

## Projection semantics (TSSF v1, values-only)

The snapshot frame (`engine/natsio/frame.go`) projects `(laneId, s)` through
`Lane.Project`: arc-length interpolation over `shape`, angle = segment
tangent in radians (atan2 convention: 0 = +x/east, CCW positive, north-up),
then `latOffset` perpendicular-left. `s` is clamped to the lane; when the
polyline arc length differs from `length` (SUMO occasionally shortens lanes
at junctions), `s` maps **proportionally**, so the lane end always lands on
the polyline end. **Schema unchanged — TSSF stays v1**; only the values
became real. Lanes without polylines (the in-code M1–M3 networks) keep the
placeholder projection (chain-offset × 3.5 m slot).

## Bootstrap recipe (Overpass → netconvert → netimport)

Requirements: a local netconvert. The reference import used a
working-directory-local PyPI install (no system install):

```sh
python3 -m venv tools/sumo-venv
tools/sumo-venv/bin/pip install eclipse-sumo   # provides netconvert 1.27.1
NC=tools/sumo-venv/lib/python3.12/site-packages/sumo/bin/netconvert
```

1. **Fetch the extract** (Overpass, nodes-before-ways union form —
   netconvert's reader needs nodes first; major roads only keeps the
   network corridor-scale):

   ```
   [out:xml][timeout:60];
   (way["highway"~"^(motorway|motorway_link|trunk|trunk_link|primary|primary_link|secondary|secondary_link)$"]
      (S,W,N,E); >;);
   out body;
   ```

   `curl -sG --data-urlencode data@query.txt https://overpass-api.de/api/interpreter -o region.osm`

2. **Compile with netconvert** (metric shapes via `--proj.utm`;
   `--no-turnarounds` is what opens the map boundary — with default
   turnarounds every clipped way ends in a U-turn loop and the network has
   zero demand portals):

   ```sh
   $NC --osm-files region.osm -o region.net.xml --proj.utm --no-turnarounds
   ```

3. **Convert to our format** (`engine/` is its own Go module — run from there):

   ```sh
   cd engine && go run ./cmd/netimport -in ../data/networks/region/region.net.xml \
     -out ../data/networks/region/region.json \
     -name region -source "netimport (netconvert 1.27.1 .net.xml)" \
     -bbox "S,W,N,E" -report ../data/networks/region/import-report.json
   ```

4. **Run:**

   ```sh
   cd engine && go run ./cmd/simrun -netfile ../data/networks/region/region.json -ticks 1200 -seed 1 -rate 600
   ```

Reference import: `data/networks/i280-woodside/` (I-280 @ Woodside Rd,
bbox `37.42,-122.30,37.45,-122.25`, OSM base 2026-07-18): 187 lanes + 188
internal lanes, 375 connections, 15 origins, 24 exits, 12.4 lane-km, 2
signalized junctions (unmodeled, see below). Files kept: `i280.osm`
(pinned extract), `i280.net.xml`, `i280.json`, `import-report.json` (all
< 2 MB; `data/` is git-ignored — the recipe above regenerates everything).

## Limitations (explicit NON-goals for v1)

- **No right-of-way / conflict sets.** Junction traversal is
  connection-following only; the connection `state` attribute (SUMO's
  major/minor `M`/`m`) is parsed-and-ignored. Where two lanes merge into
  one (junction-exit funnels, ramp merges), simultaneous arrivals can
  overlap — the reference import measures a handful of such collision
  observations at priority junctions at corridor demand. This is *the* gap
  the arch-road-graph-model conflict-set work must close (compiled
  response/foes per connection).
- **Signals unmodeled.** Signalized junctions are traversed freely;
  `netimport` lists them in the import report (`signalizedJunctions`).
- **No turn-restriction relations** beyond what `.net.xml` connections
  already encode (netconvert applied them at build time).
- **Right-hand traffic assumed** in successor ordering; left-hand is a
  global import parameter per ADR-0009 §6, not yet implemented.
- **Dead ends despawn.** With `--no-turnarounds`, a lane ending at a
  dead-end junction becomes an `exit` (its reverse direction an `origin`).
- **Re-imports produce new IDs** (ADR-0009 consequences): delta patches are
  valid against their base import only.
- **Map-edge demand is flat.** Every `origin` spawns at the scenario's
  per-lane rate; portal-specific demand lives in the scenario layer
  (`Scenario.SpawnRates` by lane ID), not the network file.
- **Geometry is per-lane polylines by value**, not the synthesis's
  geometry-store-by-reference; v1 file sizes are small enough that the
  trade-off (simplicity) wins. `latOffset` reserves the offset mechanism.

## Loader validation summary

`engine.CompileNet` rejects, with the lane ID in the message: non-1
version; empty network; empty/duplicate IDs; non-positive
`length`/`speedLimit`; negative `width`; shapes with < 2 points; unknown or
self successors; successors on `exit`/`endWall` lanes; dangling lanes (no
successors and neither flag); `exit`+`endWall`; duplicate `edgeIndex`
within an edge; lateral neighbors of unequal length.
