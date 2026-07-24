#!/usr/bin/env python3
"""overpass-lean.py — drop minor road classes from a full Overpass extract.

The full extracts (motorway..residential + stop/signal nodes) compile to
1–3M-lane networks whose GeoJSON exports exceed what a browser can parse
(V8 string cap ~537M chars; SF full = 563MB). The LEAN variant keeps
arterial-and-up classes (motorway..tertiary + links): signals and
arterial stop junctions survive, the live viz stays loadable.

Node elements referenced by kept ways are kept; tagged nodes
(highway=stop/give_way/traffic_signals) are kept only if a kept way
references them — stop resolution (osm-stop-nodes.py) then runs against
the same way set netconvert sees.

usage: overpass-lean.py in.json out.json [deepest]
  deepest: most minor class to keep (default "tertiary"); e.g. "secondary"
  keeps motorway..secondary + links (for nets still too big for the viz).
"""
import json
import sys

# OSM class hierarchy, most major first; "unclassified" sits between
# tertiary and residential (don't trust the name).
ORDER = ["motorway", "trunk", "primary", "secondary", "tertiary",
         "unclassified", "residential", "living_street"]

def keep_set(deepest: str) -> set:
    if deepest not in ORDER:
        raise SystemExit(f"deepest must be one of {ORDER}")
    keep = set()
    for c in ORDER:
        keep.add(c)
        keep.add(c + "_link")
        if c == deepest:
            break
    return keep

def main(src: str, dst: str, deepest: str) -> None:
    keep = keep_set(deepest)
    with open(src) as f:
        data = json.load(f)
    ways = [e for e in data["elements"]
            if e["type"] == "way" and e.get("tags", {}).get("highway") in keep]
    refs = set()
    for w in ways:
        refs.update(w.get("nodes", []))
    nodes = [e for e in data["elements"]
             if e["type"] == "node" and e["id"] in refs]
    out = {"version": 0.6, "generator": "overpass-lean", "elements": nodes + ways}
    # Carry provenance (osm3s.timestamp_osm_base) forward — import-city.sh
    # stamps the extract date into the network's -source provenance.
    if "osm3s" in data:
        out["osm3s"] = data["osm3s"]
    with open(dst, "w") as f:
        json.dump(out, f)
    stops = sum(1 for n in nodes if n.get("tags", {}).get("highway") == "stop")
    sigs = sum(1 for n in nodes if n.get("tags", {}).get("highway") == "traffic_signals")
    print(f"{src} -> {dst}: {len(ways)} ways, {len(nodes)} nodes, {stops} stops, {sigs} signals")

if __name__ == "__main__":
    if len(sys.argv) not in (3, 4):
        sys.exit(__doc__)
    main(sys.argv[1], sys.argv[2], sys.argv[3] if len(sys.argv) == 4 else "tertiary")
