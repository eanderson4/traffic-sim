#!/usr/bin/env python3
"""overpass2osm.py — convert Overpass API JSON to .osm XML suitable for
netconvert --osm-files. Two input shapes are handled:

NEW (node-tags query): elements are BOTH ways (with a `nodes` ref list and
tags, no inline geometry) AND real node elements ({type:"node", id, lat,
lon, optional tags — e.g. highway=stop/traffic_signals with direction).
Real nodes are emitted verbatim (id, lat, lon, <tag> children); every node
element in the input is emitted — netconvert drops unreferenced ones.

OLD (`out geom` style): elements are ways only, each with a `nodes` id
list and a parallel `geometry` lat/lon list; shared node elements are
synthesized (deduped by id) so ways join properly in netconvert.

A way carrying BOTH `nodes` and `geometry` in a new-shape file falls back
to geometry synthesis for any ref not covered by a real node element.
Output preserves input element order (nodes first, then ways).

Input is assumed UNCLIPPED (bbox pulls without `out geom(...)` clipping):
clipped `out geom` ways can carry geometry that doesn't align 1:1 with
node refs, which this script does not handle.

Usage: overpass2osm.py in.json out.osm
"""
import json
import sys
from xml.sax.saxutils import escape

# Attribute values get the quote map too — tag values like name='Foo "Bar"'
# must not break the attribute.
_QUOTES = {'"': "&quot;"}


def _attr(value: object) -> str:
    return escape(str(value), _QUOTES)


def convert(data: dict) -> str:
    real_nodes: dict[int, dict] = {}  # id -> element, first occurrence wins
    synth_nodes: dict[int, tuple[float, float]] = {}  # id -> (lat, lon)
    ways = []
    for el in data.get("elements", []):
        eltype = el.get("type")
        if eltype == "node":
            # Dedupe by id: a stop/signal node that is also a way node can
            # appear twice across the query's union + recursion. Prefer the
            # RICHER element (tags survive regardless of print order) and
            # merge tag dicts rather than dropping one on the floor.
            nid = el.get("id")
            if nid not in real_nodes:
                real_nodes[nid] = el
            elif el.get("tags"):
                prev = real_nodes[nid]
                merged = {**prev.get("tags", {}), **el["tags"]}
                real_nodes[nid] = {**prev, "tags": merged}
        elif eltype == "way":
            refs = el.get("nodes", [])
            geom = el.get("geometry", [])
            if len(refs) == len(geom):
                # Old shape (or mixed): synthesize coords for refs no real
                # node element covers. Real nodes win over synthesis.
                for ref, pt in zip(refs, geom):
                    if ref not in real_nodes and ref not in synth_nodes:
                        synth_nodes[ref] = (pt["lat"], pt["lon"])
            # Mismatched geometry (new shape: no geometry at all) is fine —
            # the way's refs resolve against real node elements.
            ways.append(el)

    lines = ['<?xml version="1.0" encoding="UTF-8"?>\n']
    lines.append('<osm version="0.6" generator="overpass2osm">\n')
    # Loud accounting: a way ref with no node behind it means an incomplete
    # extract (netconvert would silently drop or corrupt the road instead).
    missing = sum(
        1
        for w in ways
        for ref in w.get("nodes", [])
        if ref not in real_nodes and ref not in synth_nodes
    )
    if missing:
        print(f"overpass2osm: ERROR {missing} way refs have no node element (incomplete extract)", file=sys.stderr)
        raise SystemExit(1)
    for el in real_nodes.values():
        tags = el.get("tags", {})
        if not tags:
            lines.append(f'  <node id="{el["id"]}" lat="{el["lat"]}" lon="{el["lon"]}"/>\n')
        else:
            lines.append(f'  <node id="{el["id"]}" lat="{el["lat"]}" lon="{el["lon"]}">\n')
            for k, v in tags.items():
                lines.append(f'    <tag k="{_attr(k)}" v="{_attr(v)}"/>\n')
            lines.append("  </node>\n")
    for nid, (lat, lon) in synth_nodes.items():
        # A way seen BEFORE its real node element synthesized coords for the
        # ref; the real node (emitted above) always wins — emitting both
        # would duplicate the OSM id in the XML.
        if nid in real_nodes:
            continue
        lines.append(f'  <node id="{nid}" lat="{lat}" lon="{lon}"/>\n')
    for way in ways:
        lines.append(f'  <way id="{way["id"]}">\n')
        for ref in way.get("nodes", []):
            lines.append(f'    <nd ref="{ref}"/>\n')
        for k, v in way.get("tags", {}).items():
            lines.append(f'    <tag k="{_attr(k)}" v="{_attr(v)}"/>\n')
        lines.append("  </way>\n")
    lines.append("</osm>\n")
    return "".join(lines)


def main() -> None:
    if len(sys.argv) != 3:
        sys.exit("usage: overpass2osm.py in.json out.osm")
    src, dst = sys.argv[1], sys.argv[2]

    with open(src) as f:
        data = json.load(f)

    xml = convert(data)
    with open(dst, "w") as out:
        out.write(xml)

    n_nodes = xml.count("<node ")
    n_ways = xml.count("<way ")
    print(f"{src}: {n_ways} ways, {n_nodes} nodes -> {dst}")


if __name__ == "__main__":
    main()
