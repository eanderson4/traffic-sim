#!/usr/bin/env python3
"""Build a corridors.json keyed by ROAD CLASS rather than by street name.

    classcorridors.py --network chi-kennedy.json --netxml kennedy.net.xml \\
        --out data/networks/chi-kennedy/corridors.json

corridors.py groups lanes by matching OSM `name` ("Kennedy Expressway"),
which is the right unit for a validation question about a named corridor.
It needs the names to be there: the chi-kennedy extract carries none, and
every named corridor comes back with zero lanes.

Class is the fallback grouping that always exists, because netimport
records `type="highway.motorway"` on every edge. It answers a coarser
question — "what happens if we widen every motorway lane" rather than "what
happens if we widen the Kennedy" — which is the right granularity for a
single-corridor extract where the motorway IS the corridor.

Output is byte-compatible with corridors.py's schema (`lanes`: lane id ->
key, `labels`: key -> human label), so mknetvariant.py, scaledemand.py and
congestion.py all consume it unchanged.

Pure stdlib.
"""
import argparse
import collections
import json
import sys
import xml.etree.ElementTree as ET


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--network", required=True)
    ap.add_argument("--netxml", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--classes",
                    default="motorway,motorway_link,trunk,primary,secondary,tertiary",
                    help="road classes to group (others are left unmapped)")
    args = ap.parse_args()

    want = [c.strip() for c in args.classes.split(",") if c.strip()]

    # edge id -> class, from the netimport source. Edge ids in the compiled
    # network keep the net.xml spelling (including the leading '-' for the
    # reversed direction and the '#N' split suffix), so this joins directly.
    edge_class = {}
    for _, el in ET.iterparse(args.netxml, events=("end",)):
        if el.tag == "edge":
            t = el.get("type", "")
            if t.startswith("highway."):
                edge_class[el.get("id", "")] = t[len("highway."):]
            el.clear()

    with open(args.network) as f:
        net = json.load(f)

    lanes = {}
    counts = collections.Counter()
    km = collections.Counter()
    for L in net["lanes"]:
        cls = edge_class.get(L.get("edge", ""))
        if cls not in want:
            continue
        lanes[L["id"]] = cls
        counts[cls] += 1
        km[cls] += L["length"] / 1000.0

    if not lanes:
        sys.exit("no lanes matched any requested class — check --netxml is the "
                 "extract this network was imported from")

    out = {
        "schema_version": 1,
        "network": net.get("name", ""),
        "grouping": "road class (no OSM names in this extract)",
        "labels": {c: f"{c.replace('_', ' ')} lanes" for c in counts},
        "lanes": lanes,
    }
    with open(args.out, "w") as f:
        json.dump(out, f)

    print(f"[classcorridors] {len(lanes)} of {len(net['lanes'])} lanes mapped "
          f"-> {args.out}", file=sys.stderr)
    for c, n in counts.most_common():
        print(f"  {c:16} {n:6,d} lanes  {km[c]:8.1f} km", file=sys.stderr)


if __name__ == "__main__":
    main()
