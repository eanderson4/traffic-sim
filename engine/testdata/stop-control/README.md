# Fixture: stop-control

Single-junction behavior fixture: a priority-stop junction in a Phoenix
residential grid — East Amelia Avenue stopping at North 24th Street (OSM
junction node 41630092, 33.4939, -112.0302). `network.json` is the pinned
test input for `engine/fixture_stopcontrol_test.go`; this README is the
recipe that produced it (the repo distributes recipes, not data — tiny ODbL
extract, © OpenStreetMap contributors, www.openstreetmap.org/copyright).

## Recipe

1. Overpass extract (2026-07-23, overpass-api.de; kumi.systems and
   private.coffee are the retry alternates):

   ```
   [out:xml][timeout:90];(way["highway"](33.490,-112.036,33.494,-112.022);>;);out body;
   ```

2. Junction-type patch (`stopfix.nod.xml`). OSM records `highway=stop`
   nodes on both Amelia approaches of the 24th St junction, but they are
   placed at the stop lines rather than on the junction node, so
   netconvert's default typing yields plain `priority` and the stop control
   is lost. The recipe pins the junction node explicitly:

   ```xml
   <nodes>
     <node id="41630092" type="priority_stop"/>
   </nodes>
   ```

3. netconvert (tools/sumo-venv, Eclipse SUMO netconvert 1.27.1,
   eclipse-sumo PyPI):

   ```
   netconvert --osm-files stopfix-map.osm --node-files stopfix.nod.xml \
     --no-turnarounds --junctions.join -o stopfix.net.xml
   ```

   `--no-turnarounds` matches all prior imports (keeps boundary portals
   open — with turnarounds the grid closes and netimport finds 0 origins /
   0 exits). `--junctions.join` merges the junction cluster around 24th St
   (driveway/footway junctions 7–13 m apart); without it the Amelia
   approaches shred into 0.2–0.5 m lanes.

4. netimport (this repo):

   ```
   cd engine && go run ./cmd/netimport -in /tmp/stopfix.net.xml \
     -out testdata/stop-control/network.json \
     -bbox "33.490,-112.036,33.494,-112.022" -name stop-control \
     -source "netimport (netconvert 1.27.1 .net.xml, eclipse-sumo PyPI)" \
     -report testdata/stop-control/import-report.json
   ```

## What the fixture contains

- 1390 lanes (943 internal), 51 origins, 50 exits, no signal programs.
- Exactly one stop-class junction:
  `cluster_10044055054_10044055055_12273238050_41630092_#1more` (the joined
  cluster containing node 41630092), 12 stop internal lanes (SUMO state
  "s" → our `stop` row class; the allway_stop "w" state compiles to the
  same plain stop by fiat, ADR-0010). 24th St approaches are `major`
  (through) / `minor` (turns).
- Stop approaches: Amelia WB `n5603457_6_0_d2` (40.2 m), Amelia EB
  `n5603457_10_0` (50.9 m), parking aisle `n777610268_1_0` (34.3 m, also a
  network origin — the test's demand portal).
- Origin lanes whose default route crosses the junction: `n5623027_20_0`
  (23rd St, via the WB stop approach), `n777610268_1_0` (aisle stop
  approach), `n436774073_0_0_d2` (24th St SB, major through).

## Known traps baked into this extract (see the test file for details)

- netimport emits sub-vehicle-length lanes where junctions sit close
  together (e.g. `n5603457_5_0_d2`, 3.5 m); the boxBlocked exit-room rule
  can never open against them.
- Director directives without an explicit `EarliestTick` anchor their
  600-tick hold window at tick 0 — they expire on arrival after tick 600.
