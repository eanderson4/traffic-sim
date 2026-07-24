#!/usr/bin/env python3
"""render-city.py — static infrastructure map PNG from a compiled network
(network format v1, local metric frame): one LineCollection per speed
tier for the roads, octagon markers for stop-controlled approaches, dots
for signal lanes. Exists because the live viz can't load the city nets
(562MB+ GeoJSON breaks V8's string cap) — these renders are the ep-03
city shots.

Theme values are DUPLICATED from viz/src/theme.ts (the source of truth —
navy/paper ThemeSpec). One adaptation for print-scale statics, noted
inline: the fast tier takes emphasis colors per theme.

usage: render-city.py <network.json> <out.png> [--theme paper|navy]
       [--dpi 240] [--label]

deps: matplotlib + numpy on the SYSTEM python3 (not the sumo venv).
"""
import argparse
import json
import sys
import time

import matplotlib

matplotlib.use("Agg")  # headless — no display on the render box
import numpy as np
from matplotlib import pyplot as plt
from matplotlib.collections import LineCollection

# --- theme constants (source of truth: viz/src/theme.ts THEMES) ---------
THEMES = {
    "paper": {
        "bg": "#fafafa",  # theme.ts paper.bg
        "casing": "#9a9aa1",  # theme.ts paper.casing
        "fast": "#26262b",  # theme.ts paper.hudText (emphasis tier)
        "stop_face": "#e5484d",  # theme.ts stopFace (semantic, both themes)
        "stop_rim": "#ffffff",  # theme.ts stopRim
        "signal": "#2ecc71",  # theme.ts signalGreen (semantic, both themes)
        "signal_rim": "#26262b",  # thin dark rim: paper.hudText
        "label": "#26262b",  # theme.ts paper.hudText
    },
    "navy": {
        "bg": "#0e1d5c",  # theme.ts navy.bg
        "casing": "#122881",  # theme.ts navy.casing
        "fast": "#7e9dff",  # theme.ts navy.noData (emphasis on dark canvas)
        "stop_face": "#e5484d",
        "stop_rim": "#ffffff",
        "signal": "#2ecc71",
        "signal_rim": "#0a1230",  # thin dark rim: navy.sigHousing
        "label": "#d6e1ff",  # theme.ts navy.hudText
    },
}

FAST_MS = 22.0  # speedLimit (m/s) tier split: ≥ = faster roads, thicker/darker
LONG_PX = 3700  # target long-side pixels at the requested dpi


def kfmt(n: int) -> str:
    return f"{n / 1000:.0f}k" if n >= 1000 else str(n)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("network")
    ap.add_argument("out")
    ap.add_argument("--theme", choices=sorted(THEMES), default="paper")
    ap.add_argument("--dpi", type=int, default=240)
    ap.add_argument("--label", action="store_true")
    args = ap.parse_args()
    t0 = time.time()
    theme = THEMES[args.theme]

    with open(args.network) as f:
        data = json.load(f)
    lanes = data.get("lanes", [])
    name = str(data.get("name") or "city")
    print(f"loaded {len(lanes)} lanes in {time.time() - t0:.1f}s", file=sys.stderr)

    # One pass: non-internal lanes become 2-point segment blocks per speed
    # tier (np.stack per lane, one np.concatenate per tier — no per-lane
    # artist calls); internal lanes only contribute marker points.
    fast_blocks = []
    slow_blocks = []
    stop_xy = []
    sig_xy = []
    sig_junctions = set()
    minx = miny = np.inf
    maxx = maxy = -np.inf
    for lane in lanes:
        shape = lane.get("shape")
        if not shape:
            continue
        if lane.get("internal"):
            p0 = shape[0]
            if lane.get("row") == "stop":
                stop_xy.append(p0)
            tl = lane.get("tl")
            if tl:
                sig_xy.append(p0)
                sig_junctions.add(tl)
            continue
        if len(shape) < 2:
            continue
        pts = np.asarray(shape, dtype=np.float64)
        minx = min(minx, pts[:, 0].min())
        maxx = max(maxx, pts[:, 0].max())
        miny = min(miny, pts[:, 1].min())
        maxy = max(maxy, pts[:, 1].max())
        segs = np.stack([pts[:-1], pts[1:]], axis=1)
        if lane.get("speedLimit", 0) >= FAST_MS:
            fast_blocks.append(segs)
        else:
            slow_blocks.append(segs)
    print(
        f"parsed in {time.time() - t0:.1f}s: {len(fast_blocks)} fast / {len(slow_blocks)} slow lanes, "
        f"{len(stop_xy)} stop approaches, {len(sig_xy)} signal lanes ({len(sig_junctions)} junctions)",
        file=sys.stderr,
    )
    if not fast_blocks and not slow_blocks:
        sys.exit("no road lanes to draw")

    fig_in = LONG_PX / args.dpi
    spanx = max(maxx - minx, 1.0)
    spany = max(maxy - miny, 1.0)
    if spanx >= spany:
        w_in, h_in = fig_in, fig_in * spany / spanx
    else:
        w_in, h_in = fig_in * spanx / spany, fig_in

    fig = plt.figure(figsize=(w_in, h_in), dpi=args.dpi)
    ax = fig.add_axes([0, 0, 1, 1])  # fill the figure: long side ≈ LONG_PX after tight crop
    fig.patch.set_facecolor(theme["bg"])
    ax.set_facecolor(theme["bg"])
    if slow_blocks:
        ax.add_collection(
            LineCollection(
                np.concatenate(slow_blocks),
                colors=theme["casing"],
                linewidths=0.3,
                capstyle="round",
            )
        )
    if fast_blocks:
        ax.add_collection(
            LineCollection(
                np.concatenate(fast_blocks),
                colors=theme["fast"],
                linewidths=0.7,
                capstyle="round",
            )
        )
    print(f"roads drawn in {time.time() - t0:.1f}s", file=sys.stderr)
    if stop_xy:
        pts = np.asarray(stop_xy, dtype=np.float64)
        ax.scatter(
            pts[:, 0],
            pts[:, 1],
            marker=(8, 0),  # octagon
            s=5,
            c=theme["stop_face"],
            edgecolors=theme["stop_rim"],
            linewidths=0.25,
        )
    if sig_xy:
        pts = np.asarray(sig_xy, dtype=np.float64)
        ax.scatter(
            pts[:, 0],
            pts[:, 1],
            marker="o",
            s=2,
            c=theme["signal"],
            edgecolors=theme["signal_rim"],
            linewidths=0.15,
        )

    ax.set_xlim(minx, maxx)
    ax.set_ylim(miny, maxy)
    ax.set_aspect("equal")
    ax.axis("off")

    if args.label:
        disp = name.upper() if len(name) <= 3 else name.title()
        ax.text(
            0.012,
            0.012,
            f"{disp} — {kfmt(len(lanes))} lanes · {kfmt(len(stop_xy))} stop approaches"
            f" · {kfmt(len(sig_junctions))} signals",
            transform=ax.transAxes,
            fontsize=9,
            color=theme["label"],
            alpha=0.85,
            ha="left",
            va="bottom",
            family="monospace",
        )

    fig.savefig(args.out, dpi=args.dpi, bbox_inches="tight", pad_inches=0.02, facecolor=theme["bg"])
    print(f"wrote {args.out} in {time.time() - t0:.1f}s total", file=sys.stderr)


if __name__ == "__main__":
    main()
