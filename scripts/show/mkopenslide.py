#!/usr/bin/env python3
"""Render a scenario's opening slide: the network, and where traffic enters
and leaves it.

    mkopenslide.py --pod merge-pod --root data/scenarios/merge-pod/base \\
        --out docs/show/diag

Every result in the show is a difference between two runs of the same
demand, so the demand is the part of the setup an audience has to accept
before any number means anything. "Where do the cars come from?" deserves a
picture, not a paragraph: this draws the base network with a marker at every
portal, sized by its arrival rate, and a marker at every exit.

WHAT THE ENGINE ALREADY EXPOSES, AND WHAT IT DOES NOT. demosrv serves the
resolved demand live at /api/demo/{id}/params and the viz already renders it
— as a text table, capped at four rows with a "+N more" tail
(viz/src/modelpanel.ts). So sources are available at run time and could be
drawn on the live map from data the browser already has. Sinks are not:
flowParams carries origin, rate, spacing and vehicle mix, and deliberately
not destinations. That is why this slide is generated from the scenario on
disk rather than read out of a running engine.

TWO WAYS A SCENARIO CAN END A TRIP, and the difference is worth showing.
Bottleneck Town declares destinations per flow, so its sinks are chosen. The
merge pod declares none at all — every vehicle simply runs until the road
stops — so its sinks are wherever the network happens to end. A slide that
drew both the same way would hide the distinction; this one says which it is.
"""
import argparse
import json
import os
import sys

import yaml

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from mkoptiondiag import (  # noqa: E402
    INK_DIM, INK_GREEN, INK_KEEP, INK_SIG, W, bbox, cycle_len, folder,
    junction_xy, load, path_d, projector, road_lanes,
)

INK_SRC = INK_GREEN
INK_SINK = "#f778ba"
LEGEND_ROW = 17
LEGEND_PAD = 12

# Portals on the same road are separate lanes a few metres apart. Drawn
# individually they stack into one blob whose size means nothing, so they are
# grouped by position and their rates summed — which is also how a planner
# would quote the number: "1,600 veh/h enters here", not "800 and 800".
GROUP_M = 25.0


def flows(root):
    """Every flow across the scenario's demand files."""
    scen = yaml.safe_load(open(os.path.join(root, "scenario.yaml")))
    out = []
    for rel in scen.get("demand", []):
        d = yaml.safe_load(open(os.path.join(root, rel)))
        out.extend(d.get("flows", []))
    return out


def lane_index(net):
    return {ln["id"]: ln for ln in net["lanes"]}


def sources(net, fl):
    """(x, y) -> veh/h entering there."""
    idx = lane_index(net)
    pts = {}
    for f in fl:
        ln = idx.get(f.get("origin"))
        if ln is None:
            continue
        key = (round(ln["shape"][0][0] / GROUP_M), round(ln["shape"][0][1] / GROUP_M))
        p = pts.setdefault(key, {"xy": ln["shape"][0], "rate": 0.0})
        p["rate"] += f.get("veh_per_h", 0.0)
    return list(pts.values())


def sinks(net, fl):
    """Where trips end, and whether the scenario chose those points.

    Declared destinations if the demand names any; otherwise the network's
    own dead ends, because a flow with no destination runs until the road
    stops.
    """
    idx = lane_index(net)
    named = set()
    for f in fl:
        named.update(f.get("destinations", {}))
    declared = bool(named)
    if not declared:
        named = {ln["id"] for ln in net["lanes"] if not ln.get("successors")}
    pts = {}
    # SORTED, because `named` is a set: several lane ends can land in one
    # GROUP_M cell and the last writer wins, so unsorted iteration lets
    # PYTHONHASHSEED decide which point represents the cell. The SVGs are
    # checked in, so that shows up as byte churn on every regeneration.
    for lid in sorted(named):
        ln = idx.get(lid)
        if ln is None:
            continue
        end = ln["shape"][-1]
        pts[(round(end[0] / GROUP_M), round(end[1] / GROUP_M))] = end
    return list(pts.values()), declared


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pod", required=True)
    ap.add_argument("--root", required=True, help="the BASE arm directory")
    ap.add_argument("--net", default="network.json")
    ap.add_argument("--exaggerate", action="store_true")
    ap.add_argument("--fold-y", type=float, default=0.0, metavar="M")
    ap.add_argument("--height", type=int, default=200, metavar="PX")
    ap.add_argument("--peers", nargs="*", default=[],
                    help="sibling arm dirs whose geometry should be included "
                         "in the bounding box, so the opening slide is framed "
                         "identically to the option cards that follow it")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    net = load(os.path.join(args.root, args.net))
    fl = flows(args.root)
    fold = folder(args.fold_y)

    # Frame to the same box as the option diagrams. Otherwise the network
    # visibly jumps size between the opening slide and the cards, and an
    # audience reads that as a different network.
    peers = [road_lanes(load(os.path.join(p, args.net))) for p in args.peers]
    bb = bbox([road_lanes(net)] + peers, fold)
    proj, ratio = projector(bb, fold, args.exaggerate, args.height)

    body = []
    by_edge = {}
    for ln in road_lanes(net):
        by_edge.setdefault(ln.get("edge", ln["id"]), []).append(ln)
    for group in by_edge.values():
        n = max(len(l["shape"]) for l in group)
        same = [l["shape"] for l in group if len(l["shape"]) == n]
        mid = [[sum(s[i][0] for s in same) / len(same),
                sum(s[i][1] for s in same) / len(same)] for i in range(n)]
        # At card scale much of an import projects to nothing: an edge whose
        # projected extent is sub-pixel paints an invisible speck but still
        # costs a path element — on the Chicago imports those fragments
        # (many fully degenerate, every point on the same pixel) were most
        # of the file, and mkquiz inlines whatever this emits.
        pxs = [proj(p) for p in mid]
        xs = [p[0] for p in pxs]
        ys = [p[1] for p in pxs]
        if max(xs) - min(xs) < 0.5 and max(ys) - min(ys) < 0.5:
            continue
        body.append(f'<path d="{path_d(mid, proj)}" stroke="{INK_KEEP}" '
                    f'stroke-width="{round(1.5 + 1.5 * len(group), 1)}"/>')


    # Signal heads on the setup slide, because on a signalised scenario the
    # lights ARE the baseline: three of Bottleneck Town's four options are
    # arguments about them, and a room that has not been told the corridor
    # runs one fixed 86 s plan with every junction starting together has no
    # way to see why coordinating them is a distinct idea from shortening
    # them. Read out of the network's own `signals` block, so the sentence
    # cannot describe a plan the run did not use.
    sigs = net.get("signals", [])
    for s in sigs:
        xy = junction_xy(net, s.get("junction", s["id"]), proj)
        if xy is not None:
            body.append(f'<circle cx="{xy[0]:.1f}" cy="{xy[1]:.1f}" r="3.6" '
                        f'fill="{INK_SIG}"/>')

    src = sources(net, fl)
    snk, declared = sinks(net, fl)
    total = sum(p["rate"] for p in src)
    peak = max((p["rate"] for p in src), default=1.0)

    # Area, not radius, tracks the rate: a portal carrying four times the
    # traffic should look four times as big, and radius-scaling would make
    # it sixteen.
    def radius(rate):
        return 3.0 + 5.0 * (rate / peak) ** 0.5

    # SINKS AS RINGS, SOURCES AS DISCS, because in a town of two-way streets
    # they are mostly the same places: the lane a flow enters on and the lane
    # trips on the opposite carriageway end on share a portal. Filled markers
    # for both means the larger one simply covers the other and the slide
    # silently under-reports its own exits. A ring around a disc reads as
    # "traffic enters and leaves here" with no extra legend.
    at_src = {(round(p["xy"][0] / GROUP_M), round(p["xy"][1] / GROUP_M)):
              radius(p["rate"]) for p in src}
    shared = 0
    for p in snk:
        x, y = proj(p)
        key = (round(p[0] / GROUP_M), round(p[1] / GROUP_M))
        r = at_src.get(key)
        if r is not None:
            shared += 1
        body.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" '
                    f'r="{(r + 3.0) if r else 4.5:.1f}" fill="none" '
                    f'stroke="{INK_SINK}" stroke-width="1.8"/>')
    for p in src:
        x, y = proj(p["xy"])
        body.append(f'<circle cx="{x:.1f}" cy="{y:.1f}" '
                    f'r="{radius(p["rate"]):.1f}" fill="{INK_SRC}"/>')

    spacings = sorted({f.get("spacing", "?") for f in fl})
    y0 = args.height
    legend = [
        f'<circle cx="18" cy="{y0 + 8}" r="5" fill="{INK_SRC}"/>',
        f'<text x="30" y="{y0 + 11}">{len(src)} portals in · '
        f'{total:,.0f} veh/h · {"/".join(spacings)} arrivals</text>',
        f'<circle cx="18" cy="{y0 + 26}" r="4.5" fill="none" '
        f'stroke="{INK_SINK}" stroke-width="1.8"/>',
    ]
    exits_head = (f'{len(snk)} exits'
                  + (f' ({shared} sharing a portal with an entry)'
                     if shared else ''))
    dest_tail = ("destinations declared per flow"
                 if declared else
                 "no destinations declared — trips run to the edge of the network")
    # One row when the line fits the ~80-character budget, two when it does
    # not: overflowing text is not clipped by the viewBox, it runs off the
    # edge of the card and reads as a truncated thought.
    if len(exits_head + ' · ' + dest_tail) <= 80:
        legend.append(f'<text x="30" y="{y0 + 29}">{exits_head} · '
                      f'{dest_tail}</text>')
        sig_y = 44
        exits_rows = 1
    else:
        legend.append(f'<text x="30" y="{y0 + 29}">{exits_head}</text>'
                      f'<text x="30" y="{y0 + 43}">{dest_tail}</text>')
        sig_y = 58
        exits_rows = 2
    if sigs:
        # Derived, never asserted: cycle length, the main-road green share
        # and whether the plans are coordinated all come out of the phase
        # list, so re-timing the pod re-words this line by itself. The green
        # share is only printed when EVERY program agrees on it — authored
        # pods run one uniform plan, but the imports carry dozens of
        # distinct programs and quoting sigs[0]'s green as the network's
        # would be a false aggregate.
        # Rounded like offs below: identical plans whose phase durations
        # sum in a different float order must not read as "mixed".
        cycs = {round(cycle_len(s), 2) for s in sigs}
        firsts = {round(next((p["duration"] for p in s["phases"]
                              if "G" in p["state"] or "g" in p["state"]), 0.0), 2)
                  for s in sigs}
        cyc = next(iter(cycs)) if len(cycs) == 1 else 0.0
        if len(cycs) == 1 and len(firsts) == 1 and cyc:
            first = next(iter(firsts))
            detail = (f'{cyc:g}s cycle, {first:g}s main-road green '
                      f'({100 * first / cyc:.0f}%)')
        elif len(cycs) == 1:
            detail = f'{cyc:g}s cycle'
        else:
            detail = 'mixed programs'
        offs = {round(s.get("offset", 0), 2) for s in sigs}
        coord = ("every junction starts its cycle together — uncoordinated"
                 if offs == {0.0} else
                 "each junction offset from the last — coordinated")
        # Two rows, because one does not fit: at 9px in a 400-wide frame the
        # legend has room for roughly 80 characters and the full sentence is
        # 120. Overflowing text is not clipped by the viewBox — it simply
        # runs off the edge of the card and reads as a truncated thought.
        legend.append(
            f'<circle cx="18" cy="{y0 + sig_y}" r="4" fill="{INK_SIG}"/>'
            f'<text x="30" y="{y0 + sig_y + 3}">{len(sigs)} fixed-time signals · '
            f'{detail}</text>'
            f'<text x="30" y="{y0 + sig_y + 17}">{coord}</text>')

    if ratio >= 1.5 or fold(1e4) != 1e4:
        legend.append(f'<text x="{W - 12}" y="{y0 + 11}" text-anchor="end">'
                      f'schematic · cross-section not to scale</text>')

    # Height follows the rows actually emitted rather than a constant sized
    # for the busiest scenario, so a pod with no signals gets no empty band.
    legend_h = (LEGEND_PAD
                + LEGEND_ROW * (1 + exits_rows + (2 if sigs else 0)))
    svg = (f'<svg xmlns="http://www.w3.org/2000/svg" '
           f'viewBox="0 0 {W} {y0 + legend_h}" width="100%" role="img">'
           f'<g fill="none" stroke-linecap="round" stroke-linejoin="round">'
           + "\n".join(body) + '</g>'
           f'<g fill="{INK_DIM}" font-family="ui-sans-serif,system-ui,'
           f'sans-serif" font-size="9">' + "\n".join(legend) + '</g></svg>')

    os.makedirs(args.out, exist_ok=True)
    path = os.path.join(args.out, f"{args.pod}__setup.svg")
    with open(path, "w") as f:
        f.write(svg)
    print(f"[slide] {args.pod}: {len(src)} portals ({total:,.0f} veh/h), "
          f"{len(snk)} exits, destinations={'declared' if declared else 'implicit'}"
          f" -> {path}")


if __name__ == "__main__":
    main()
