#!/usr/bin/env python3
"""Produce an INFRASTRUCTURE variant of a compiled network JSON.

The ADR-0012 patch grammar covers demand only; a network change is a whole-
network replacement (ADR-0012 addendum §3). This is the tool that writes the
replacement, so "add a lane to Lake Shore Drive" is a reviewable command
rather than a hand-edited 45 MB file.

    mknetvariant.py --network in.json --out out.json \\
        --corridors corridors.json --add-lane "Lake Shore" --retime 0.66

Operations (composable, applied in the order listed here):

  --retime F            scale every GREEN phase duration by F, holding amber
                        and all-red fixed. F<1 shortens the cycle.
  --add-lane CORRIDOR   widen every edge of a corridor by one lane
  --drop-lane CORRIDOR  narrow every edge of a corridor by one lane (a bus
                        lane or protected bikeway taken from general traffic)
  --speed CORRIDOR=KPH  reset the speed limit on a corridor

WHAT ADDING A LANE ACTUALLY MEANS HERE
--------------------------------------------------------------------------
A compiled lane is a shape (a polyline), a length, a speed limit, and a
successor list. Widening an edge duplicates its outermost lane, offsets the
copy laterally by the lane width, and gives it the SAME successors as the
lane it was cloned from. That last part is the honest limitation: the new
lane inherits the original's downstream connectivity rather than getting its
own junction-internal lanes, so it adds mainline storage and car-following
capacity but NOT extra turning capacity through the junction. For corridor
questions ("does widening LSD help?") that is the right model; for junction
questions it is not, and the caller should say so.

Offsetting is done on the LOCAL METRIC frame the network is already in
(netimport projects once), so a lateral offset is a plain 2-D normal — no
geodesy required.

Pure stdlib.
"""
import argparse
import collections
import json
import math
import os
import sys


def offset_shape(shape, dist):
    """Offset a polyline by `dist` metres along its left normal.

    Per-segment normals averaged at the joints. Not a true mitre join — at a
    sharp bend the offset polyline pinches — which is cosmetically visible
    only at the vertex and irrelevant to the longitudinal model, which reads
    length and successors, never the shape.
    """
    n = len(shape)
    if n < 2:
        return [list(p) for p in shape]
    normals = []
    for i in range(n - 1):
        (x0, y0), (x1, y1) = shape[i][:2], shape[i + 1][:2]
        dx, dy = x1 - x0, y1 - y0
        L = math.hypot(dx, dy) or 1.0
        normals.append((-dy / L, dx / L))
    out = []
    for i in range(n):
        if i == 0:
            nx, ny = normals[0]
        elif i == n - 1:
            nx, ny = normals[-1]
        else:
            ax, ay = normals[i - 1]
            bx, by = normals[i]
            nx, ny = ax + bx, ay + by
            L = math.hypot(nx, ny) or 1.0
            nx, ny = nx / L, ny / L
        out.append([shape[i][0] + nx * dist, shape[i][1] + ny * dist])
    return out


def corridor_lanes(lanes, corridors, needle):
    """Lane ids in the corridor whose label contains `needle` (case-folded)."""
    lut = corridors["lanes"]
    labels = corridors.get("labels", {})
    keys = [k for k, v in labels.items()
            if needle.casefold() in str(v).casefold()
            or needle.casefold() in k.casefold()]
    if not keys:
        sys.exit(f"no corridor matches {needle!r}; have: {sorted(labels)}")
    want = set(keys)
    return {lid for lid, k in lut.items() if k in want}, keys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--network", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--corridors")
    ap.add_argument("--retime", type=float,
                    help="scale every green phase duration by this factor")
    ap.add_argument("--add-lane", action="append", default=[],
                    metavar="CORRIDOR")
    ap.add_argument("--drop-lane", action="append", default=[],
                    metavar="CORRIDOR")
    ap.add_argument("--speed", action="append", default=[],
                    metavar="CORRIDOR=KPH")
    ap.add_argument("--name", help="overwrite the network's name field")
    args = ap.parse_args()

    with open(args.network) as f:
        net = json.load(f)
    corridors = None
    if args.corridors:
        with open(args.corridors) as f:
            corridors = json.load(f)
    if (args.add_lane or args.drop_lane or args.speed) and not corridors:
        sys.exit("--add-lane/--drop-lane/--speed need --corridors")

    lanes = net["lanes"]
    by_id = {L["id"]: L for L in lanes}
    log = []

    # ---- signal retiming ------------------------------------------------
    if args.retime:
        touched = 0
        for sig in net.get("signals", ()):
            for ph in sig.get("phases", ()):
                st = ph.get("state", "")
                # Green phases only: amber and all-red are safety intervals
                # set by clearance distance, not by demand, and scaling them
                # would be a signal-safety change masquerading as a retime.
                if "G" in st or "g" in st:
                    ph["duration"] = max(4, round(ph["duration"] * args.retime))
                    touched += 1
        log.append(f"retime x{args.retime}: {touched} green phases on "
                   f"{len(net.get('signals', ()))} signals")

    # ---- speed limits ---------------------------------------------------
    for spec in args.speed:
        name, _, kph = spec.partition("=")
        ids, keys = corridor_lanes(lanes, corridors, name)
        mps = float(kph) / 3.6
        for lid in ids:
            if lid in by_id:
                by_id[lid]["speedLimit"] = mps
        log.append(f"speed {keys} -> {kph} km/h on {len(ids)} lanes")

    # ---- lane drops -----------------------------------------------------
    for name in args.drop_lane:
        ids, keys = corridor_lanes(lanes, corridors, name)
        # Outermost lane of each edge, but never the last one: taking an
        # edge to zero lanes severs the corridor instead of narrowing it.
        by_edge = collections.defaultdict(list)
        for L in lanes:
            if L["id"] in ids:
                by_edge[L["edge"]].append(L)
        drop = set()
        for edge, group in by_edge.items():
            if len(group) < 2:
                continue
            drop.add(max(group, key=lambda L: L["edgeIndex"])["id"])
        net["lanes"] = lanes = [L for L in lanes if L["id"] not in drop]
        by_id = {L["id"]: L for L in lanes}
        # A dropped lane must not linger as anybody's successor.
        for L in lanes:
            L["successors"] = [s for s in L["successors"] if s not in drop]
        log.append(f"drop-lane {keys}: removed {len(drop)} lanes "
                   f"over {len(by_edge)} edges")

    # ---- lane additions -------------------------------------------------
    for name in args.add_lane:
        ids, keys = corridor_lanes(lanes, corridors, name)
        by_edge = collections.defaultdict(list)
        for L in lanes:
            if L["id"] in ids:
                by_edge[L["edge"]].append(L)
        added = []
        for edge, group in sorted(by_edge.items()):
            outer = max(group, key=lambda L: L["edgeIndex"])
            w = outer.get("width") or 3.2
            new = json.loads(json.dumps(outer))
            new["edgeIndex"] = outer["edgeIndex"] + 1
            new["id"] = f"{outer['id']}_w1"
            new["shape"] = offset_shape(outer["shape"], -w)
            new["source"] = dict(outer.get("source", {}), synthetic="add-lane")
            added.append(new)
        lanes.extend(added)
        net["lanes"] = lanes
        by_id = {L["id"]: L for L in lanes}
        log.append(f"add-lane {keys}: added {len(added)} lanes "
                   f"over {len(by_edge)} edges (successors inherited — "
                   f"mainline capacity only, no extra turning capacity)")

    if args.name:
        net["name"] = args.name
    prov = net.setdefault("provenance", {})
    prov["variant_of"] = args.network
    prov["variant_ops"] = log

    # Write-then-rename, and it is NOT just crash safety. Scenario dirs
    # hard-link the network to avoid one 45 MB copy per variant, so `open(out,
    # "w")` truncates an inode that may be shared with the source network and
    # every other scenario pointing at it. That is not hypothetical: it
    # silently rewrote data/networks/chi-loop-urban/chi-loop-urban.json into a
    # retimed 55,882-lane variant, through a chain of 23 links, and the only
    # reason it was recoverable is that copies on another filesystem had
    # fallen back to real copies. rename() replaces the directory entry and
    # leaves every other link pointing at the original bytes.
    tmp = args.out + ".tmp"
    with open(tmp, "w") as f:
        json.dump(net, f)
    os.replace(tmp, args.out)
    for line in log:
        print(f"[mknetvariant] {line}", file=sys.stderr)
    print(f"[mknetvariant] wrote {args.out} ({len(net['lanes'])} lanes)",
          file=sys.stderr)


if __name__ == "__main__":
    main()
