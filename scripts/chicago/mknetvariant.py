#!/usr/bin/env python3
"""Produce an INFRASTRUCTURE variant of a compiled network JSON.

The ADR-0012 patch grammar covers demand only; a network change is a whole-
network replacement (ADR-0012 addendum §3). This is the tool that writes the
replacement, so "add a lane to Lake Shore Drive" is a reviewable command
rather than a hand-edited 45 MB file.

    mknetvariant.py --network in.json --out out.json \\
        --corridors corridors.json --add-lane "Lake Shore" --retime 0.66

Operations (composable, applied in the order listed here — note that is
drop-lane BEFORE add-lane, so within one invocation a drop cannot remove
a lane the same invocation added; doing both to one corridor reshuffles
rather than no-ops):

  --retime F            scale every GREEN phase duration by F, holding amber
                        and all-red fixed. F<1 shortens the cycle.
  --speed CORRIDOR=KPH  reset the speed limit on a corridor
  --drop-lane CORRIDOR  narrow every edge of a corridor by one lane (a bus
                        lane or protected bikeway taken from general
                        traffic). Sees only LABELLED lanes: a drop in a
                        LATER invocation than an --add-lane removes the
                        outermost labelled lane, not the synthetic clone.
  --add-lane CORRIDOR   widen every edge of a corridor by one lane;
                        repeat it to widen by two (the second pass clones
                        the first pass's clones, one lateral step further
                        out — a single pass can only ever add one)

WHAT ADDING A LANE ACTUALLY MEANS HERE
--------------------------------------------------------------------------
A compiled lane is a shape (a polyline), a length, a speed limit, and a
successor list. Widening an edge duplicates its outermost lane, offsets the
copy laterally by the lane width, gives it the SAME successors as its donor
(so traffic can leave it), and adds it to the successors of everything that
feeds the donor (so traffic can reach it).

That second wiring step is not optional, and finding that out cost a full
pod run. Without it nothing points at the new lane, so the only way in is a
lane change within the edge itself — and measured on chi-loop-urban, the
added lanes then carried **4.8%** of Lake Shore Drive's vehicle-distance
where a fully used fifth lane on a four-lane road would be ~20%. Every
vehicle arriving from upstream lands in an original lane, and the median
widened edge is 138 m long. A widening that delivers a quarter of its lane
is not a measurement of widening; it is a no-op with a plausible label,
which is the single most dangerous thing this repo can produce.

The honest limitation that REMAINS: the new lane gets no junction-internal
lanes of its own, no signal phase and no conflict set. Turning capacity
through the junction is approximated by letting the existing movements
spread across a wider cross-section. For corridor questions ("does widening
LSD help?") that is the right model; for junction-capacity questions it is
not, and the caller should say so.

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

# ADR-0038 riding-along guard: --add-lane never clones a donor shorter than
# this (m). Sub-threshold lanes are netconvert junction-connector slivers
# (divided-crossing edges clamped between two junction polygons); cloning one
# duplicates a zero-storage capacity-seal trap.
MIN_DONOR_LENGTH_M = 5.0


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
            # An origin or exit lane is a PORTAL: demand flows name it by id,
            # and removing it makes every flow that enters there unloadable
            # ("not a spawn origin lane of the network"). Take the outermost
            # lane that is not one — a real bus-lane conversion also does not
            # delete the point where traffic enters the network.
            cand = [L for L in group if not L.get("origin") and not L.get("exit")]
            if not cand:
                continue
            drop.add(max(cand, key=lambda L: L["edgeIndex"])["id"])
        net["lanes"] = lanes = [L for L in lanes if L["id"] not in drop]
        by_id = {L["id"]: L for L in lanes}
        # A dropped lane must not linger as anybody's successor. Exit lanes
        # carry no successors key at all (they are where vehicles leave the
        # world), so this reads defensively rather than assuming the field.
        for L in lanes:
            succ = L.get("successors")
            if succ:
                L["successors"] = [s for s in succ if s not in drop]

        # Cascade. Removing a lane orphans the junction-internal lanes whose
        # ONLY successor it was, and the loader rejects those outright
        # ("no successors and neither exit nor endWall"). Semantically this
        # is right: taking a lane away also takes away the turn movements
        # that fed only that lane. Iterate to a fixpoint, since removing an
        # internal can orphan the internal feeding it.
        cascaded = 0
        while True:
            dangling = {L["id"] for L in lanes
                        if not L.get("successors")
                        and not L.get("exit") and not L.get("endWall")}
            if not dangling:
                break
            cascaded += len(dangling)
            lanes = [L for L in lanes if L["id"] not in dangling]
            for L in lanes:
                succ = L.get("successors")
                if succ:
                    L["successors"] = [s for s in succ if s not in dangling]
        # Right-of-way conflict lists name other internal lanes, and the
        # loader rejects a reference to one that is gone ("unknown foesCross
        # lane"). Prune both after the cascade has settled — a conflict with
        # a movement that no longer exists is not a conflict.
        alive = {L["id"] for L in lanes}
        for L in lanes:
            for field in ("foesMerge", "foesCross"):
                if field in L:
                    kept = [x for x in L[field] if x in alive]
                    if kept:
                        L[field] = kept
                    else:
                        del L[field]
        net["lanes"] = lanes
        by_id = {L["id"]: L for L in lanes}
        if cascaded:
            log.append(f"drop-lane {keys}: cascaded {cascaded} orphaned "
                       f"junction-internal lanes (turn movements that fed "
                       f"only a dropped lane)")
        log.append(f"drop-lane {keys}: removed {len(drop)} lanes "
                   f"over {len(by_edge)} edges")

    # ---- lane additions -------------------------------------------------
    for name in args.add_lane:
        ids, keys = corridor_lanes(lanes, corridors, name)
        # Corridor membership comes from the label map, which never learns
        # about clones — but a clone belongs to the same EDGE as its donor,
        # and the outermost lane of that edge is the donor for the NEXT
        # widening. Group the whole edge so a repeated --add-lane (widen by
        # two) widens past its own clones; grouping only the labelled lanes
        # would clone the same outermost lane twice under the same _w1 id.
        edges = {L.get("edge") for L in lanes if L["id"] in ids}
        # A labelled lane carrying "edge": null (or no edge key) would put
        # None in the set and match every internal lane (they carry no edge
        # key) into a by_edge[None] group. None in this network today; keep
        # the invariant true rather than observed.
        edges.discard(None)
        by_edge = collections.defaultdict(list)
        for L in lanes:
            # Junction-internal lanes carry no edge key; they are never a
            # widening donor, so they simply cannot match. The group is the
            # WHOLE edge, not just the labelled lanes: the donor of the
            # clone can be an unlabelled outermost lane of a corridor edge
            # — you widen the edge, not the label.
            if L.get("edge") in edges:
                by_edge[L["edge"]].append(L)
        added = []
        feeders = {}
        skipped_short = 0
        for edge, group in sorted(by_edge.items()):
            outer = max(group, key=lambda L: L["edgeIndex"])
            # ADR-0038 riding-along guard: never clone a sub-threshold lane.
            # A donor shorter than one vehicle is a junction-connector sliver
            # (netconvert clamps divided-crossing edges between two junction
            # polygons to as little as 0.2 m); cloning it duplicates a
            # zero-storage trap — widen1/2/gridwiden carried 7/14/881 such
            # _w1 lanes. Toy fixtures without a length field widen as before.
            donor_len = outer.get("length")
            if donor_len is not None and donor_len < MIN_DONOR_LENGTH_M:
                skipped_short += 1
                continue
            w = outer.get("width") or 3.2
            new = json.loads(json.dumps(outer))
            new["edgeIndex"] = outer["edgeIndex"] + 1
            new["id"] = f"{outer['id']}_w1"
            new["shape"] = offset_shape(outer["shape"], -w)
            new["source"] = dict(outer.get("source", {}), synthetic="add-lane")
            added.append(new)
            feeders.setdefault(outer["id"], []).append(new["id"])
        lanes.extend(added)
        net["lanes"] = lanes
        by_id = {L["id"]: L for L in lanes}

        # Make the new lane REACHABLE. Cloning gives it the donor's
        # successors, so traffic can leave it — but nothing points AT it, so
        # the only way in is a lane change within the edge itself. Measured
        # on chi-loop-urban before this: added lanes carried 4.8% of Lake
        # Shore Drive's vehicle-distance where a fully used fifth lane on a
        # four-lane road would be ~20%, because every vehicle arriving from
        # upstream lands in an original lane and the median widened edge is
        # only 138 m long. A widening that delivers a quarter of its lane is
        # not a measurement of widening.
        #
        # So every lane that feeds the donor also feeds the clone. That is
        # the junction-side half of the change, approximated: the movement
        # is allowed to spread across the wider cross-section rather than
        # getting its own signal phase or conflict set.
        wired = 0
        for L in lanes:
            succ = L.get("successors")
            if not succ:
                continue
            extra = [c for s in succ for c in feeders.get(s, ())]
            if extra:
                L["successors"] = succ + [c for c in extra if c not in succ]
                wired += 1
        log.append(f"add-lane {keys}: added {len(added)} lanes over "
                   f"{len(by_edge)} edges; wired into {wired} upstream lanes "
                   f"so arrivals can enter them (turning capacity is "
                   f"approximated, not modelled: no new junction internals)"
                   + (f"; skipped {skipped_short} sub-{MIN_DONOR_LENGTH_M:g} m "
                      f"donor(s) (ADR-0038 sliver guard)" if skipped_short else ""))

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
