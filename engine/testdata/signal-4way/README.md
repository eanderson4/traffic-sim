# Fixture: signal-4way

Single signalized urban junction behavior fixture: a tiny Midtown Manhattan
crop centered on the signalized intersection at OSM node **42430333**
(8th Ave × W 41st St area; 40.7548420, -73.9841180). The junction carries a
real alternating fixed-time program (39 G / 6 y / 39 G / 6 y over 14 signal
links); the one-way street grid gives it 3 incoming edges (the fourth arm is
outbound-only). The crop also contains ~19 neighboring signalized junctions,
which the test exercises for network-wide red compliance.

Used by `engine/fixture_signal4way_test.go` (red compliance, saturated
discharge headway, zero collisions).

## Contents

- `network.json` — compiled network (network-format v1): 198 lanes
  (102 junction-internal), 201 connections, 7 origins, 10 exits, 20 fixed-time
  signal programs.
- `import-report.json` — the netimport report.

## Recipe (matches all prior OSM imports; repo distributes recipes, not data)

1. Overpass extract (2026-07-23, endpoint `https://overpass.kumi.systems/api/interpreter`):

   ```
   [out:xml][timeout:90];(way["highway"](40.7539,-73.9854,40.7558,-73.9828);>;);out body;
   ```

2. `tools/sumo-venv/bin/netconvert --osm-files map.osm -o net.net.xml --proj.utm`
   (netconvert 1.27.1, eclipse-sumo PyPI)

3. ```
   cd engine && go run ./cmd/netimport -in net.net.xml \
     -out testdata/signal-4way/network.json \
     -bbox "40.7539,-73.9854,40.7558,-73.9828" -name signal-4way \
     -source "netimport (netconvert 1.27.1 .net.xml, eclipse-sumo PyPI)" \
     -report testdata/signal-4way/import-report.json
   ```

The pinned `network.json` here is the test input — tests never run
netconvert. Source data © OpenStreetMap contributors, ODbL; this is a tiny
extract (~150 × 190 m) with attribution.

## Lanes under test

- Junction under test: `42430333` (program id `42430333`, offset 0,
  phases `39 GGGgrrrrrrGGgg / 6 yyyyrrrrrryyyy / 39 rrrrGGGGGGrrrr / 6 rrrryyyyyyrrrr`).
- Instrumented saturated-discharge approach: origin `n167922072_1_0`
  (157 m, default route) → `i6597464528_2_0` → stop-line stub
  `n167922072_0_0` → internal lane `i42430333_10_0` (tlLink 10: green in
  phase 0, red in phases 2–3) → exit `n1320232776_1_0`.

## Known engine violations pinned by this fixture (2026-07-23)

- **V1** — amber-committed vehicles enter the box up to ~0.2 s into red at
  all-red clearance phases (red wall is not grandfathered): 2 crossings in
  3600 ticks (i3826754271_0_0, i3942050704_0_0, both tick 852).
- **V2** — `boxBlocked`'s exit-room check inspects only the immediate
  successor lane; the crop's 0.2 m exit stubs seal every approach of the
  junction under test (and several neighbors): 0 discharges over 1560 green
  ticks with a standing queue on the approach origin. Saturation headway is
  unmeasurable until the exit-room walk continues through short lanes.

See `engine/fixture_signal4way_test.go` for the numbers; set
`FIXTURE_SIGNAL4WAY_STRICT=1` to enforce all assertions hard.
