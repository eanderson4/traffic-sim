# Fixture: freeway-merge

Single freeway on-ramp merge: the SFO airport on-ramp (OSM way 8921107,
1 lane) merging into US-101 northbound (OSM way 256787724, 6 lanes) at
OSM node 6862263616 (37.6178015, -122.3987668) — the southern end of the
US-101 / I-380 / SFO interchange area. Downstream is OSM way 392505474
(7 lanes). (The stated search area was 37.615,-122.395,37.635,-122.370;
recon showed the only true mainline+ramp merge nodes in that area sit at
its southern edge — the I-380 junction proper is at ~37.636,-122.401,
outside it — so the crop is centered on this merge node.)

The crop boundary cuts both feeders mid-edge, which is what makes them
demand portals: the mainline upstream edge and the ramp are both `origin`
lanes, the downstream edge's lanes are `exit` lanes.

## Key lanes (pinned by engine/fixture_freeway_merge_test.go)

- Mainline origins: `n256787724_0` … `n256787724_5` (6 × 923 m, 29.1 m/s)
- Ramp origin: `n8921107_0` (538 m, 22.2 m/s)
- Merge internals (junction 6862263616, priority): `i6862263616_0_0`
  (ramp → downstream lane 0), `i6862263616_1_0` … `i6862263616_1_5`
  (mainline through, lane k → downstream lane k+1)
- Downstream exits: `n392505474_0` … `n392505474_6` (7 × 721 m, 29.1 m/s)

## Recipe (2026-07-23)

1. Overpass (served by https://overpass.kumi.systems/api/interpreter):

   ```
   [out:xml][timeout:90];(way["highway"](37.6148,-122.4028,37.6208,-122.3948);>;);out body;
   ```

2. `tools/sumo-venv/bin/netconvert --osm-files map.osm -o net.net.xml --no-turnarounds true`
   (Eclipse SUMO netconvert 1.27.1, eclipse-sumo PyPI)

3. `cd engine && go run ./cmd/netimport -in net.net.xml -out testdata/freeway-merge/network.json \
      -bbox "37.6148,-122.4028,37.6208,-122.3948" -name freeway-merge \
      -source "netimport (netconvert 1.27.1 .net.xml, eclipse-sumo PyPI)" \
      -report testdata/freeway-merge/import-report.json`

   Result: 182 lanes (212 internal), 420 connections, 37 origins, 38 exits.
   One warning from netconvert ("Could not build program '0' for traffic
   light '6585455433'") — the affected surface-street signal is far from
   the merge under test and carries no demand in the fixture tests.

Data: © OpenStreetMap contributors, ODbL 1.0 (tiny extract, recipe
distributed rather than source data; the JSON here is the pinned test
input).
