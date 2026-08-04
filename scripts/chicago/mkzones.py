#!/usr/bin/env python3
"""Map compiled network lanes to NAMED DISTRICTS (the Loop, the South Side, ...).

    mkzones.py --network chi-loop-urban.json --zones scripts/chicago/zones.geojson \\
        --out data/networks/chi-loop-urban/zones.json

WHY
--------------------------------------------------------------------------
Two questions need districts and neither can be asked without them:

  * "How much traffic is headed for the LOOP versus everywhere else?"
    Destinations are currently weighted by workplace floor area alone, which
    produces whatever share the building data happens to imply and offers no
    knob. The single largest lever on a Chicago AM peak — how much of the
    region is trying to reach the CBD — is therefore not adjustable.
    `mkod.py --dest-zones` reads this file to make it adjustable.

  * "WHERE is the congestion?" A corridor answers that along the expressways
    and nowhere else; 81% of the network is unnamed arterial grid, and
    "arterial grid" as one 1,779 lane-km bucket is not a location.

This is deliberately the same shape as corridors.json (schema_version,
network, labels, lanes: {lane_id: key}) so every consumer that already reads
a lane->group map reads this one unchanged.

PROJECTION
--------------------------------------------------------------------------
zones.geojson is lon/lat. Lane shapes are netconvert's local metres. The
network carries the exact transform in `provenance` — a proj4 string and the
netOffset netconvert subtracted — so the conversion is read from the network
being mapped rather than assumed. A network imported with a different
projection maps correctly; one carrying no projection is refused rather than
silently mapped through the wrong datum, which would place every lane in the
wrong district while producing a perfectly plausible-looking file.

A lane is assigned by its MIDPOINT. Lanes straddling a boundary therefore
land in exactly one district, which is what makes the shares sum to 1.

Requires pyproj (tools/sumo-venv), as extract.py and boundaries.py do.
"""
import argparse
import json
import math
import sys

# pyproj is imported inside main() so the geometry below stays importable —
# and testable — under a plain python3 that has no venv.


def midpoint(ln):
    """The lane's ARCLENGTH midpoint — half its length along the polyline.

    Not the middle shape point. Vertices are not spaced evenly: netconvert
    emits them where a road changes direction, so on a lane with one elbow
    near its start the middle vertex sits near the start. The two-point lane
    is the case that bites — `s[len(s) // 2]` is `s[1]`, the lane's far END,
    so a two-point lane crossing a district boundary is assigned to the
    district it arrives in rather than the one it mostly lies in. On the
    Chicago network that moves 188 lane assignments, which shifts both the
    district report and `mkod.py --dest-zones` destination shares.

    The docstring above promises assignment by midpoint and the shares
    summing to 1; this is what makes that true.
    """
    s = ln.get("shape") or []
    if not s:
        return None
    if len(s) == 1:
        return tuple(s[0])
    segs = [math.dist(s[i], s[i + 1]) for i in range(len(s) - 1)]
    half = sum(segs) / 2.0
    if half <= 0.0:
        return tuple(s[0])  # zero-length polyline: every point is the same
    run = 0.0
    for i, seg in enumerate(segs):
        if run + seg >= half:
            t = (half - run) / seg if seg > 0.0 else 0.0
            ax, ay = s[i][0], s[i][1]
            bx, by = s[i + 1][0], s[i + 1][1]
            return (ax + (bx - ax) * t, ay + (by - ay) * t)
        run += seg
    return tuple(s[-1])


def in_ring(pt, ring):
    """Ray-casting point-in-polygon over one closed ring.

    Stdlib rather than shapely: these are simple convex-ish district
    polygons with no holes, and the whole dependency would buy nothing that
    a half-open crossing test does not already get right.
    """
    x, y = pt
    inside = False
    n = len(ring)
    for i in range(n):
        x1, y1 = ring[i]
        x2, y2 = ring[(i + 1) % n]
        # Half-open in y: a vertex exactly on the scan line counts once, so
        # a point is never claimed by two districts sharing that edge.
        if (y1 > y) != (y2 > y):
            xc = x1 + (y - y1) * (x2 - x1) / (y2 - y1)
            if x < xc:
                inside = not inside
    return inside


def in_polygon(pt, coords):
    """GeoJSON Polygon: ring 0 is the exterior, the rest are holes."""
    if not coords or not in_ring(pt, coords[0]):
        return False
    return not any(in_ring(pt, hole) for hole in coords[1:])


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--network", required=True)
    ap.add_argument("--zones", required=True, help="zones.geojson (lon/lat)")
    ap.add_argument("--out", required=True)
    ap.add_argument("--kinds", default="district",
                    help="comma-separated GeoJSON `kind` values to use. "
                         "Default `district` only: the corridor polygons in "
                         "the same file overlap the districts, and a lane in "
                         "two groups breaks every share this file feeds.")
    args = ap.parse_args()

    try:
        from pyproj import Transformer
    except ImportError:
        sys.exit("mkzones: needs pyproj — run under tools/sumo-venv/bin/python")

    with open(args.network) as f:
        net = json.load(f)
    prov = net.get("provenance") or {}
    proj, off = prov.get("projection"), prov.get("netOffset")
    if not proj or not off:
        sys.exit(f"mkzones: {args.network} carries no projection/netOffset in "
                 f"provenance; refusing to guess the datum")
    tf = Transformer.from_crs("EPSG:4326", proj, always_xy=True)

    with open(args.zones) as f:
        gj = json.load(f)
    kinds = {k.strip() for k in args.kinds.split(",") if k.strip()}

    polys, labels = [], {}
    for feat in gj["features"]:
        p = feat.get("properties") or {}
        if p.get("kind") not in kinds:
            continue
        g = feat.get("geometry") or {}
        if g.get("type") == "Polygon":
            rings = [g["coordinates"]]
        elif g.get("type") == "MultiPolygon":
            rings = g["coordinates"]
        else:
            continue
        # Project every ring once, into the network's own local metres.
        local = []
        for poly in rings:
            local.append([[tuple(a + b for a, b in zip(tf.transform(lon, lat), off))
                           for lon, lat in ring] for ring in poly])
        polys.append((p["name"], local))
        labels[p["name"]] = p.get("label", p["name"])
    if not polys:
        sys.exit(f"mkzones: no features of kind {sorted(kinds)} in {args.zones}")

    lane2z, counts = {}, {}
    overlapping = 0
    for ln in net["lanes"]:
        pt = midpoint(ln)
        if pt is None:
            continue
        hit = [name for name, geoms in polys
               if any(in_polygon(pt, poly) for poly in geoms)]
        if not hit:
            continue
        if len(hit) > 1:
            overlapping += 1
        lane2z[ln["id"]] = hit[0]
        counts[hit[0]] = counts.get(hit[0], 0) + 1

    if overlapping:
        # Not fatal, but it means the shares this file feeds are ambiguous:
        # the same lane-km is inside two districts and only one gets it.
        print(f"WARNING: {overlapping:,} lanes fall in more than one zone; "
              f"the first match wins. Fix the polygons or the shares will "
              f"not mean what they say.", file=sys.stderr)

    total = len(net["lanes"])
    print(f"zones: {len(lane2z):,} of {total:,} lanes assigned "
          f"({100 * len(lane2z) / total:.1f}%)", file=sys.stderr)
    for name, n in sorted(counts.items(), key=lambda kv: -kv[1]):
        print(f"  {name:20s} {n:7,} lanes", file=sys.stderr)
    unplaced = total - len(lane2z)
    if unplaced:
        print(f"  {'(outside all zones)':20s} {unplaced:7,} lanes",
              file=sys.stderr)

    with open(args.out, "w") as f:
        json.dump({"schema_version": 1,
                   "network": net.get("name"),
                   "source": args.zones,
                   "labels": labels,
                   "lanes": lane2z}, f)
    print(f"wrote {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
