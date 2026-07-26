#!/usr/bin/env python3
"""Map compiled network lanes to NAMED CHICAGO CORRIDORS.

Congestion reports rank lanes and street names. Neither is the unit a
validation question is asked in: "does our simulation put the congestion on
the Kennedy?" needs the Kennedy as one object, not 119 OSM ways and several
thousand lanes under four different `name` spellings.

This builds that grouping once and writes it as a lookup table
(`corridors.json`: lane id -> corridor key) that `congestion.py --corridors`
consumes. The compiled network carries no street names on lanes at all, so
the join runs lane -> edge -> OSM way id -> name/ref, the same
`netimport` id convention congestion.py uses (way id, optional leading `-`
for the reversed direction, optional `#N` split suffix).

Usage:
  corridors.py --network chi-loop-urban.json --osm loop.osm \\
      --out data/networks/chi-loop-urban/corridors.json

--------------------------------------------------------------------------
WHY NAME MATCHING, AND WHERE IT BREAKS
--------------------------------------------------------------------------
OSM names these corridors inconsistently — the Kennedy alone appears as
"Kennedy Expressway" and "Kennedy Express Reversable Lane" (sic), the Dan
Ryan splits into "(Local)" and "(Express)" carriageways, and the Stevenson
is both "Stevenson Expressway" and "Adlai Stevenson Expressway". Matching is
therefore on a case-folded SUBSTRING of `name`, which absorbs all of those.

`ref` is deliberately NOT used as the primary key: the Kennedy and the Dan
Ryan are the same route (`I 90;I 94`) on opposite sides of downtown and
would collapse into one corridor. Ref is recorded for reporting only.

THE JANE BYRNE INTERCHANGE is not a named way in OSM — it is the ramp
tangle where the Kennedy, Dan Ryan and Eisenhower meet. It is matched
GEOMETRICALLY instead: any lane whose midpoint falls within --byrne-radius
of the interchange centre. Because that box also contains mainline segments
of the three expressways, the interchange claim is applied LAST and does not
steal them — a lane is attributed to the interchange only if it is a link
(ramp) or an unnamed way. That keeps "Kennedy" meaning the Kennedy and
"Jane Byrne" meaning the ramps, which is how the ATRI segments are defined.

Pure stdlib.
"""
import argparse
import json
import math
import re
import sys
import xml.etree.ElementTree as ET

EDGE_RE = re.compile(r"^-?(\d+)(?:#\d+)?$")

# corridor key -> (display label, tuple of case-folded name substrings)
# Ordered: the first match wins, so more specific patterns come first.
CORRIDORS = [
    ("kennedy", "Kennedy Expressway (I-90/94 N)", ("kennedy",)),
    ("dan-ryan", "Dan Ryan Expressway (I-90/94 S)", ("dan ryan",)),
    ("eisenhower", "Eisenhower Expressway (I-290)", ("eisenhower",)),
    ("stevenson", "Stevenson Expressway (I-55)", ("stevenson",)),
    ("lsd", "DuSable Lake Shore Drive (US-41)", ("dusable lake shore drive",)),
]

# Jane Byrne (Circle) Interchange centre, WGS84. I-90/94 x I-290, immediately
# west of the Loop. Used only to define a radius; nothing downstream depends
# on the exact metre.
BYRNE_LAT, BYRNE_LON = 41.8757, -87.6425


def way_attrs(osm_path):
    """way id -> (name, ref, highway). Streaming.

    NOTE the parsing trap: Element.clear() wipes a node's children AND its
    attributes, so clearing every element as it ends destroys the <tag>
    children before they can be read. Only <way> is cleared here.
    """
    out = {}
    for _, el in ET.iterparse(osm_path, events=("end",)):
        if el.tag != "way":
            continue
        tags = {t.get("k"): t.get("v") for t in el.findall("tag")}
        if "highway" in tags:
            out[el.get("id")] = (tags.get("name", ""), tags.get("ref", ""),
                                 tags["highway"])
        el.clear()
    return out


def utm16n(lat, lon):
    """WGS84 -> UTM zone 16N metres. Transverse Mercator, WGS84 ellipsoid.

    Inlined rather than pulled from pyproj so the script stays stdlib-only
    and runs under the system python like the rest of the report chain. The
    network's own projection string (provenance.projection) is
    '+proj=utm +zone=16 +ellps=WGS84 +datum=WGS84'; this reproduces it to
    well under a metre, which is far finer than the radius it feeds.
    """
    a = 6378137.0
    f = 1 / 298.257223563
    k0 = 0.9996
    e2 = f * (2 - f)
    ep2 = e2 / (1 - e2)
    lon0 = math.radians(-87.0)  # zone 16 central meridian
    lat_r, lon_r = math.radians(lat), math.radians(lon)
    N = a / math.sqrt(1 - e2 * math.sin(lat_r) ** 2)
    T = math.tan(lat_r) ** 2
    C = ep2 * math.cos(lat_r) ** 2
    A = math.cos(lat_r) * (lon_r - lon0)
    M = a * ((1 - e2 / 4 - 3 * e2**2 / 64 - 5 * e2**3 / 256) * lat_r
             - (3 * e2 / 8 + 3 * e2**2 / 32 + 45 * e2**3 / 1024) * math.sin(2 * lat_r)
             + (15 * e2**2 / 256 + 45 * e2**3 / 1024) * math.sin(4 * lat_r)
             - (35 * e2**3 / 3072) * math.sin(6 * lat_r))
    x = k0 * N * (A + (1 - T + C) * A**3 / 6
                  + (5 - 18 * T + T**2 + 72 * C - 58 * ep2) * A**5 / 120) + 500000.0
    y = k0 * (M + N * math.tan(lat_r) * (A**2 / 2 + (5 - T + 9 * C + 4 * C**2) * A**4 / 24
              + (61 - 58 * T + T**2 + 600 * C - 330 * ep2) * A**6 / 720))
    return x, y


def lane_midpoint(lane):
    """Midpoint of a lane's shape polyline, in the network's local frame."""
    shape = lane.get("shape") or []
    if not shape:
        return None
    if len(shape) == 1:
        return tuple(shape[0])
    mid = len(shape) // 2
    return tuple(shape[mid])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--network", required=True, help="compiled network JSON")
    ap.add_argument("--osm", required=True, help="the OSM extract it was imported from")
    ap.add_argument("--out", required=True)
    ap.add_argument("--byrne-radius", type=float, default=600.0,
                    help="metres from the Jane Byrne centre within which a "
                         "RAMP or unnamed lane is attributed to the interchange")
    args = ap.parse_args()

    with open(args.network) as f:
        net = json.load(f)
    ways = way_attrs(args.osm)

    off = net.get("provenance", {}).get("netOffset", [0.0, 0.0])
    bx, by = utm16n(BYRNE_LAT, BYRNE_LON)
    bx, by = bx + off[0], by + off[1]

    labels = {k: lab for k, lab, _ in CORRIDORS}
    labels["jane-byrne"] = "Jane Byrne Interchange (I-90/94 x I-290)"

    lane_corr = {}
    stats = {k: {"lanes": 0, "length_m": 0.0, "ways": set()} for k in labels}
    byrne_candidates = []

    for l in net["lanes"]:
        if l.get("internal"):
            continue  # junction interiors: delay belongs to the approach
        m = EDGE_RE.match(l.get("edge", ""))
        name, ref, hwy = ("", "", "")
        if m:
            name, ref, hwy = ways.get(m.group(1), ("", "", ""))
        low = name.casefold()
        hit = None
        for key, _, pats in CORRIDORS:
            if any(p in low for p in pats):
                hit = key
                break
        if hit:
            lane_corr[l["id"]] = hit
            stats[hit]["lanes"] += 1
            stats[hit]["length_m"] += l.get("length", 0.0)
            if m:
                stats[hit]["ways"].add(m.group(1))
            continue
        # Unclaimed. A ramp or unnamed carriageway near the Byrne centre is
        # interchange. Mainline of the three expressways already got claimed
        # above, so this cannot cannibalize them.
        if hwy.endswith("_link") or not name:
            p = lane_midpoint(l)
            if p is not None and math.hypot(p[0] - bx, p[1] - by) <= args.byrne_radius:
                byrne_candidates.append((l, m.group(1) if m else None))

    for l, wid in byrne_candidates:
        lane_corr[l["id"]] = "jane-byrne"
        stats["jane-byrne"]["lanes"] += 1
        stats["jane-byrne"]["length_m"] += l.get("length", 0.0)
        if wid:
            stats["jane-byrne"]["ways"].add(wid)

    doc = {
        "schema_version": 1,
        "network": net.get("name", ""),
        "byrne_radius_m": args.byrne_radius,
        "labels": labels,
        "lanes": lane_corr,
    }
    with open(args.out, "w") as f:
        json.dump(doc, f, indent=1, sort_keys=True)
        f.write("\n")

    total = sum(1 for l in net["lanes"] if not l.get("internal"))
    print(f"corridors: {len(lane_corr):,} of {total:,} external lanes mapped "
          f"-> {args.out}")
    for k in labels:
        s = stats[k]
        print(f"  {labels[k]:42s} {s['lanes']:6,d} lanes  "
              f"{s['length_m']/1000:8.1f} km  {len(s['ways']):4d} ways")
    missing = [k for k in labels if stats[k]["lanes"] == 0]
    if missing:
        print("WARNING: no lanes matched for: " + ", ".join(missing), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
