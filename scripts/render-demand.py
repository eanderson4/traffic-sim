#!/usr/bin/env python3
"""render-demand.py — scenario demand overlay on the paper base map: Poisson
origin portals (cobalt dots, sized by veh/h) and exit/sink portals (orange
triangles) with a legend. Companion to render-city.py (same frame/theme);
exists for the phantomjam city pages so a scenario's demand story is visible
without loading the sim.

usage: render-demand.py <network.json> <portals.json> <demand.yaml> <out.png>
       [--buildings buildings.geojson]   (WGS84 polygons, projected into
                                          the network's metric frame)

deps: matplotlib + numpy + pyyaml (+ pyproj with --buildings) on the SYSTEM python3.
"""
import argparse
import json
import sys
import time

import matplotlib

matplotlib.use("Agg")
import numpy as np
import yaml
from matplotlib import pyplot as plt
from matplotlib.collections import LineCollection, PolyCollection
from matplotlib.lines import Line2D

BG = "#fafafa"  # paper.bg — matches render-city.py's base renders
CASING = "#9a9aa1"  # slow tier (paper.casing)
FAST = "#26262b"  # fast tier (paper.hudText emphasis)
FAST_MS = 22.0  # speedLimit tier split, same as render-city.py
ORIGIN = "#2E5BFF"  # math cobalt
SINK = "#FF6230"  # vibes orange
INK = "#1A1C22"


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("network")
    ap.add_argument("portals")
    ap.add_argument("demand")
    ap.add_argument("out")
    ap.add_argument("--buildings", help="WGS84 buildings GeoJSON (projected into the network frame)")
    ap.add_argument("--dpi", type=int, default=200)
    args = ap.parse_args()
    t0 = time.time()

    net = json.load(open(args.network))
    portals = json.load(open(args.portals))
    dem = yaml.safe_load(open(args.demand))

    # optional urban texture: WGS84 footprints → the network's metric frame
    bldg_polys = None
    if args.buildings:
        from pyproj import Transformer

        prov = net["provenance"]
        fwd = Transformer.from_crs("EPSG:4326", prov["projection"], always_xy=True)
        offx, offy = prov["netOffset"]
        raw = json.load(open(args.buildings))["features"]
        polys = []
        for f in raw:
            g = f["geometry"]
            # Polygon: one ring list; MultiPolygon: list of ring lists —
            # take each exterior ring as its own footprint polygon.
            rings = [g["coordinates"][0]] if g["type"] == "Polygon" else [p[0] for p in g["coordinates"]]
            for ring in rings:
                arr = np.asarray(ring, dtype=np.float64)
                if arr.ndim != 2 or arr.shape[0] < 3:
                    continue
                x, y = fwd.transform(arr[:, 0], arr[:, 1])
                polys.append(np.column_stack([x + offx, y + offy]))
        bldg_polys = polys
        print(f"buildings: {len(polys)} footprints projected", file=sys.stderr)

    # base roads in render-city.py's tiered style
    fast_blocks = []
    slow_blocks = []
    minx = miny = np.inf
    maxx = maxy = -np.inf
    for lane in net.get("lanes", []):
        shape = lane.get("shape")
        if not shape or lane.get("internal") or len(shape) < 2:
            continue
        pts = np.asarray(shape, dtype=np.float64)
        minx, maxx = min(minx, pts[:, 0].min()), max(maxx, pts[:, 0].max())
        miny, maxy = min(miny, pts[:, 1].min()), max(maxy, pts[:, 1].max())
        segs = np.stack([pts[:-1], pts[1:]], axis=1)
        (fast_blocks if lane.get("speedLimit", 0) >= FAST_MS else slow_blocks).append(segs)

    # demand rate per origin lane id
    rate = {}
    for f in dem.get("flows", []):
        rate[f["origin"]] = rate.get(f["origin"], 0) + float(f.get("veh_per_h", 0))

    ox, oy, os_ = [], [], []
    n_rated = 0
    for p in portals.get("origins", []):
        r = rate.get(p["id"])
        if r is None:
            continue  # only origins the scenario actually injects on
        n_rated += 1
        ox.append((p["start"][0] + p["end"][0]) / 2)
        oy.append((p["start"][1] + p["end"][1]) / 2)
        os_.append(14 + r / 3)  # area-ish scaling
    ex = [(p["start"][0] + p["end"][0]) / 2 for p in portals.get("exits", [])]
    ey = [(p["start"][1] + p["end"][1]) / 2 for p in portals.get("exits", [])]

    span = max(maxx - minx, maxy - miny)
    fig_w, fig_h = (10, 10 * (maxy - miny) / (maxx - minx)) if (maxx - minx) >= (maxy - miny) else (10 * (maxx - minx) / (maxy - miny), 10)
    fig, ax = plt.subplots(figsize=(fig_w, fig_h), dpi=args.dpi)
    fig.patch.set_facecolor(BG)
    ax.set_facecolor(BG)
    if bldg_polys:
        ax.add_collection(PolyCollection(
            bldg_polys, facecolors="#cfc8b4", edgecolors="none", zorder=0.5))
    if slow_blocks:
        ax.add_collection(LineCollection(np.concatenate(slow_blocks), colors=CASING, linewidths=0.3, capstyle="round", zorder=1))
    if fast_blocks:
        ax.add_collection(LineCollection(np.concatenate(fast_blocks), colors=FAST, linewidths=0.7, capstyle="round", zorder=1))
    if ox:
        ax.scatter(ox, oy, s=os_, c=ORIGIN, alpha=0.75, edgecolors="white", linewidths=0.4, zorder=3)
    if ex:
        ax.scatter(ex, ey, s=7, c=SINK, marker="^", alpha=0.35, linewidths=0, zorder=2)

    total = sum(rate.values())
    legend = [
        Line2D([0], [0], marker="o", color="none", markerfacecolor=ORIGIN, markersize=9,
               label=f"origin portal — Poisson arrivals ({n_rated} lanes, {total:,.0f} veh/h)"),
        Line2D([0], [0], marker="^", color="none", markerfacecolor=SINK, markersize=9,
               label=f"destination sink ({len(ex)} exits)"),
    ]
    leg = ax.legend(handles=legend, loc="lower right", frameon=True, fontsize=9)
    leg.get_frame().set_facecolor(BG)
    leg.get_frame().set_edgecolor(INK)
    ax.set_xlim(minx - span * 0.02, maxx + span * 0.02)
    ax.set_ylim(miny - span * 0.02, maxy + span * 0.02)
    ax.set_aspect("equal")
    ax.axis("off")
    fig.tight_layout(pad=0.3)
    fig.savefig(args.out, facecolor=BG)
    print(f"{n_rated} origins ({total:,.0f} veh/h), {len(ex)} exits; wrote {args.out} in {time.time() - t0:.1f}s", file=sys.stderr)


if __name__ == "__main__":
    main()
