#!/usr/bin/env python3
"""Extract water-body polygons covering the Chicago sim area.

Display-only overlay for the viz background map: Lake Michigan, the Chicago
River system, canals, harbors, and significant inland lakes/ponds, clipped to
the zones extent (buffered), simplified, cached as local GeoJSON.

Lake Michigan note: the Geofabrik Illinois extract contains NO natural=coastline
ways at all (the coastline runs over the lake, outside the state polygon Geofabrik
clips to), so the coastline-split approach is impossible here. Instead the lake
is present as multipolygon relation 1205149 (natural=water, water=lake) with its
full member ways, which pyosmium's area assembler handles like any other water
relation. Verified 2026-07-24 against illinois-260723.osm.pbf: assembles as a
valid multipolygon covering the lakefront.

Usage:
  water.py --pbf illinois.osm.pbf --zones zones.geojson \
      --buffer-m 5000 --simplify-m 15 --out water.geojson

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

# OSM water= value -> overlay kind; fallbacks resolved from other tags below.
KIND_MAP = {
    "lake": "lake",
    "pond": "pond",
    "oxbow": "lake",
    "lagoon": "lake",
    "reservoir": "reservoir",
    "river": "river",
    "canal": "canal",
    "harbour": "harbor",
    "basin": "basin",
    "wastewater": "basin",
    "stream_pool": "pond",
}


def kind_of(tags):
    w = tags.get("water")
    if w in KIND_MAP:
        return KIND_MAP[w]
    if tags.get("waterway") == "riverbank":
        return "river"
    if tags.get("landuse") == "reservoir":
        return "reservoir"
    if tags.get("landuse") == "basin":
        return "basin"
    if tags.get("natural") == "bay":
        return "bay"
    return "water"


def is_water(tags):
    return (tags.get("natural") in ("water", "bay")
            or tags.get("waterway") == "riverbank"
            or tags.get("landuse") in ("reservoir", "basin"))


def round_coords(coords, ndigits=6):
    if isinstance(coords[0], (int, float)):
        return [round(c, ndigits) for c in coords]
    return [round_coords(c, ndigits) for c in coords]


class Water:
    def __init__(self, clip):
        self.clip = clip
        self.feats = []

    def area(self, a):
        tags = a.tags
        if not is_water(tags):
            return
        try:
            geom = from_wkb(wkb_factory.create_multipolygon(a))
        except Exception:
            return  # broken multipolygon — skip
        if geom.is_empty or not geom.intersects(self.clip):
            return
        geom = geom.intersection(self.clip)
        if geom.is_empty:
            return
        self.feats.append((tags.get("name", ""), kind_of(tags), geom))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pbf", required=True)
    ap.add_argument("--zones", required=True)
    ap.add_argument("--buffer-m", type=float, default=5000)
    ap.add_argument("--simplify-m", type=float, default=15)
    ap.add_argument("--min-area-m2", type=float, default=2000)
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    with open(args.zones) as f:
        zones = json.load(f)
    clip = unary_union([shape(ft["geometry"]) for ft in zones["features"]])
    clip = transform(TO_WGS, transform(TO_UTM, clip).buffer(args.buffer_m))

    w = Water(clip)
    fp = osmium.FileProcessor(args.pbf).with_areas()
    for obj in fp:
        if obj.is_area():
            w.area(obj)

    feats = []
    for name, kind, geom in w.feats:
        g = transform(TO_UTM, geom)
        if g.area < args.min_area_m2:
            continue  # sliver from clipping or insignificant pond
        g = transform(TO_WGS, g.simplify(args.simplify_m))
        geo = mapping(g)
        geo["coordinates"] = round_coords(geo["coordinates"])
        feats.append({
            "type": "Feature",
            "properties": {"name": name, "kind": kind},
            "geometry": geo,
        })
    doc = {"type": "FeatureCollection",
           "comment": "water bodies intersecting the sim area (OSM, simplified)",
           "features": feats}
    with open(args.out, "w") as f:
        json.dump(doc, f)
    print(f"water: {len(feats)} features -> {args.out}")
    kinds = {}
    for ft in feats:
        kind = ft["properties"]["kind"]
        kinds[kind] = kinds.get(kind, 0) + 1
    for kind, n in sorted(kinds.items()):
        print(f"  {kind}: {n}")
    for ft in sorted(feats, key=lambda f: f["properties"]["name"]):
        p = ft["properties"]
        if p["name"]:
            print(f"  [{p['kind']}] {p['name']}")


if __name__ == "__main__":
    main()
