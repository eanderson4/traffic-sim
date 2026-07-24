#!/usr/bin/env python3
"""Extract administrative boundaries intersecting the Chicago sim area.

Full polygons of every admin boundary (county/city/township level) whose
geometry intersects the zones extent (buffered), per the viz-overlay
decision: display-only, simplified, cached as local GeoJSON.

Usage:
  boundaries.py --pbf illinois.osm.pbf --zones zones.geojson \
      --buffer-m 3000 --simplify-m 200 --out boundaries.geojson

Requires: pyosmium, shapely, pyproj (tools/sumo-venv).
"""
import argparse
import json

import osmium
from pyproj import Transformer
from shapely.geometry import mapping, shape
from shapely.ops import transform, unary_union
from shapely import from_wkb

TO_UTM = Transformer.from_crs("EPSG:4326", "EPSG:32616", always_xy=True).transform
TO_WGS = Transformer.from_crs("EPSG:32616", "EPSG:4326", always_xy=True).transform

wkb_factory = osmium.geom.WKBFactory()


class Boundaries:
    def __init__(self, clip):
        self.clip = clip
        self.feats = []

    def area(self, a):
        tags = a.tags
        if tags.get("boundary") != "administrative":
            return
        level = tags.get("admin_level", "")
        if level not in ("6", "7", "8"):
            return
        name = tags.get("name")
        if not name:
            return
        try:
            geom = from_wkb(wkb_factory.create_multipolygon(a))
        except Exception:
            return  # broken multipolygon — skip
        if geom.is_empty or not geom.intersects(self.clip):
            return
        self.feats.append((name, int(level), geom))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pbf", required=True)
    ap.add_argument("--zones", required=True)
    ap.add_argument("--buffer-m", type=float, default=3000)
    ap.add_argument("--simplify-m", type=float, default=200)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    with open(args.zones) as f:
        zones = json.load(f)
    clip = unary_union([shape(ft["geometry"]) for ft in zones["features"]])
    clip = transform(TO_WGS, transform(TO_UTM, clip).buffer(args.buffer_m))

    b = Boundaries(clip)
    fp = osmium.FileProcessor(args.pbf).with_areas()
    for obj in fp:
        if obj.is_area():
            b.area(obj)

    feats = []
    for name, level, geom in b.feats:
        g = transform(TO_UTM, geom).simplify(args.simplify_m)
        g = transform(TO_WGS, g)
        feats.append({
            "type": "Feature",
            "properties": {"name": name, "admin_level": level},
            "geometry": mapping(g),
        })
    doc = {"type": "FeatureCollection",
           "comment": "admin boundaries intersecting the sim area (OSM, simplified)",
           "features": feats}
    with open(args.out, "w") as f:
        json.dump(doc, f)
    print(f"boundaries: {len(feats)} features -> {args.out}")
    for name, level, _ in sorted(b.feats):
        print(f"  L{level} {name}")


if __name__ == "__main__":
    main()
