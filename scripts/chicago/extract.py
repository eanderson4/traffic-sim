#!/usr/bin/env python3
"""Extract one Chicago zone from a regional OSM PBF.

Spatial extract of a zones.geojson polygon, buffered (in meters, UTM 16N) so
zone-edge roads keep their approaches, keeping only motor-relevant objects:
highway ways whose geometry intersects the buffered polygon, plus turn
restrictions on those ways. BackReferenceWriter completes referenced
nodes/members from the source PBF.

Usage:
  extract.py --pbf illinois.osm.pbf --zones zones.geojson --zone loop \
      --buffer-m 3000 --out loop.osm

Requires: pyosmium, shapely, pyproj (tools/sumo-venv).
"""
import argparse
import json
import os
import re
import sys

import osmium
from pyproj import Transformer
from shapely.geometry import LineString, mapping, shape
from shapely.ops import transform
from shapely.prepared import prep

TO_UTM = Transformer.from_crs("EPSG:4326", "EPSG:32616", always_xy=True).transform
TO_WGS = Transformer.from_crs("EPSG:32616", "EPSG:4326", always_xy=True).transform


class RoadWriter:
    """Keeps polygon-intersecting highway ways and their turn restrictions."""

    def __init__(self, writer, poly, highway_re):
        self.w = writer
        self.poly = prep(poly)
        self.bounds = poly.bounds  # (minx, miny, maxx, maxy) lon/lat
        self.highway_re = highway_re
        self.kept_way_ids = set()
        self.ways = 0
        self.rels = 0

    def _hits(self, coords):
        minx, miny, maxx, maxy = self.bounds
        xs = [c[0] for c in coords]
        ys = [c[1] for c in coords]
        if max(xs) < minx or min(xs) > maxx or max(ys) < miny or min(ys) > maxy:
            return False
        return self.poly.intersects(LineString(coords))

    def way(self, way):
        hwy = way.tags.get("highway")
        if hwy is None or not self.highway_re.fullmatch(hwy):
            return
        coords = [(n.lon, n.lat) for n in way.nodes if n.location.valid()]
        if len(coords) < 2 or not self._hits(coords):
            return
        self.w.add(way)
        self.kept_way_ids.add(way.id)
        self.ways += 1

    def relation(self, rel):
        if rel.tags.get("type") != "restriction":
            return
        if any(m.type == "w" and m.ref in self.kept_way_ids for m in rel.members):
            self.w.add(rel)
            self.rels += 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pbf", required=True)
    ap.add_argument("--zones", required=True)
    ap.add_argument("--zone", required=True)
    ap.add_argument("--buffer-m", type=float, default=3000)
    ap.add_argument("--highway-regex", default=".*",
                    help="keep ways whose highway tag matches (e.g. "
                         "'motorway|trunk|primary|secondary|tertiary|.*_link')")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    with open(args.zones) as f:
        zones = json.load(f)
    feat = next((ft for ft in zones["features"]
                 if ft["properties"]["name"] == args.zone), None)
    if feat is None:
        sys.exit(f"extract: no zone named {args.zone!r} in {args.zones}")

    poly = shape(feat["geometry"])
    if args.buffer_m > 0:
        poly = transform(TO_WGS, transform(TO_UTM, poly).buffer(args.buffer_m))
    else:
        poly = poly.buffer(0)  # clean potential self-intersections

    if os.path.exists(args.out):
        os.remove(args.out)  # stale output from a failed run
    hwy_re = re.compile(args.highway_regex)
    writer = osmium.BackReferenceWriter(args.out, args.pbf, remove_tags=False)
    roads = RoadWriter(writer, poly, hwy_re)
    fp = osmium.FileProcessor(args.pbf).with_locations()
    try:
        for obj in fp:
            if obj.is_way():
                roads.way(obj)
            elif obj.is_relation():
                roads.relation(obj)
    finally:
        writer.close()
    print(f"extract: zone={args.zone} buffer_m={args.buffer_m} "
          f"ways={roads.ways} restrictions={roads.rels} -> {args.out}")


if __name__ == "__main__":
    main()
