#!/usr/bin/env python3
"""Render one diagram per quiz option, from the network that was simulated.

    mkoptiondiag.py --pod merge-pod --root data/scenarios/merge-pod \\
        --base base --arms mainline-lane frontage-road accel-extend ramp-meter \\
        --out docs/show/diag

The quiz asks a guest to choose between four upgrades described in one line
of text each. "Build a bypass north of town" and "build a relief road south
of town" are the same sentence with a compass direction swapped; on camera
nobody can hold the two geometries in their head from words alone. A plan
view answers it instantly.

DRAWN FROM THE SIMULATED NETWORK, NEVER HAND-AUTHORED. mkquiz.py already
refuses to let a number live in two files, because the copy diverges the
first time one is regenerated and the divergence is invisible until someone
quotes it on air. A hand-drawn diagram is the same failure with a longer
fuse: re-run the pod with a different bypass alignment and the picture keeps
showing the old one. So the geometry here is diffed out of the arm's own
network.json, and the signal strips are read out of its `signals` block.

Two kinds of change, so two kinds of picture:

  * GEOMETRY. Tarmac the arm has and base does not is drawn as new; tarmac
    base had and the arm does not is drawn as removed. All arms of a pod
    share ONE bounding box — per-arm autoscaling would zoom the bypass and
    the connector to different sizes and make the shorter road look longer.
  * SIGNAL TIMING. retime-short, green-wave and ramp-meter have networks
    identical to base apart from the `signals` block, so a plan view of
    them IS the baseline picture and says nothing. Those get a timing strip
    instead: one bar per junction over a FIXED wall-clock window, so a
    shorter cycle visibly fits more cycles into the same width and a green
    wave visibly staircases. Shared across the pod for the same reason the
    bbox is.

WHY THE DIFF IS GEOMETRIC AND NOT BY LANE ID. Adding a bypass makes the
generator re-cut Main Street into new segments with new ids, so 90 of the
town's 200 base lane ids "disappear" in an arm that did not touch Main
Street's alignment at all. Diffing ids would paint the entire main road as
demolished. Instead each lane is sampled along its length and matched
against the other arm's tarmac by proximity: a re-segmented road is
retained, and a lane added 3.5 m alongside an existing one is new. The
tolerance therefore has to sit below one lane width, or a widening reads as
no change.

Output is standalone SVG so mkquiz.py can inline it: the quiz page has to
work on conference wifi with no CDN, no fetch and no build step.
"""
import argparse
import json
import math
import os

# Matches the quiz page's palette (mkquiz.py CSS) — the diagrams sit inside
# the option cards, so they have to read as part of that page, not as
# imported figures.
INK_NEW = "#58a6ff"      # what this option adds
INK_GONE = "#f85149"     # what it removes
INK_KEEP = "#3d444d"     # the network both arms share
INK_SIG = "#d29922"      # signal heads
INK_GREEN = "#3fb950"
INK_DIM = "#6e7681"

W, H = 400, 200          # plan-view box
STRIP_H = 78             # timing strip, appended under the plan when present

# Below one lane width (3.5 m), so a lane added alongside an existing one is
# not swallowed as "already there". Above the sub-metre wobble a re-cut
# polyline picks up at a junction stop line.
TOL_M = 2.0
STEP_M = 1.0             # sampling pitch along a lane


def load(path):
    with open(path) as f:
        return json.load(f)


def junction_ids(net):
    """Junction ids this network has signal programs for."""
    return {s.get("junction", s["id"]) for s in net.get("signals", [])}


def road_lanes(net):
    """Lanes to draw: everything except junction interiors.

    The pods emit one internal lane per permitted turn (14 links at a 4-leg
    signal), which at thumbnail scale draws a solid blob over exactly the
    junction the diagram is trying to show.
    """
    jids = junction_ids(net)
    out = []
    for ln in net["lanes"]:
        eid = ln.get("edge", ln["id"])
        if eid.startswith("i"):
            # `iJ1_W0_E0` — internal iff the segment after the leading `i`
            # names a junction this network actually has. Prefix-matching
            # alone would eat a road called `interstate`.
            head = eid[1:].split("_", 1)[0]
            if head in jids:
                continue
        out.append(ln)
    return out


def densify(shape, step=STEP_M):
    """Points along a polyline at a fixed pitch, endpoints included."""
    pts = []
    for (x0, y0), (x1, y1) in zip(shape, shape[1:]):
        d = math.hypot(x1 - x0, y1 - y0)
        n = max(int(d / step), 1)
        for i in range(n):
            t = i / n
            pts.append((x0 + (x1 - x0) * t, y0 + (y1 - y0) * t))
    if shape:
        pts.append(tuple(shape[-1]))
    return pts


class Tarmac:
    """Every point of a network's roadway, in a grid hashed at TOL_M."""

    def __init__(self, lanes):
        self.cells = {}
        for ln in lanes:
            for p in densify(ln["shape"]):
                self.cells.setdefault(
                    (int(p[0] // TOL_M), int(p[1] // TOL_M)), []).append(p)

    def covers(self, pt):
        cx, cy = int(pt[0] // TOL_M), int(pt[1] // TOL_M)
        for dx in (-1, 0, 1):
            for dy in (-1, 0, 1):
                for q in self.cells.get((cx + dx, cy + dy), ()):
                    if math.hypot(q[0] - pt[0], q[1] - pt[1]) <= TOL_M:
                        return True
        return False

    def novel_fraction(self, lane):
        """Share of a lane's length that this network has no tarmac under.

        A share rather than a yes/no: an arm that extends an acceleration
        lane from 300 m to 700 m produces one lane that is 57% new, and
        calling it wholly new or wholly old would both be wrong.
        """
        pts = densify(lane["shape"])
        if not pts:
            return 0.0
        return sum(0 if self.covers(p) else 1 for p in pts) / len(pts)


# A lane counts as changed once this much of it is unmatched. Low, because
# the interesting cases (an extended taper, a re-tied junction approach) are
# partial by nature; the sampling noise floor on a matched lane is ~0.
NOVEL = 0.25


def folder(t):
    """Collapse everything within `t` metres of y=0 onto the corridor, and
    log-compress what lies beyond it.

    Two problems, one transform. The merge pod parks its on-ramp 200 m off
    the mainline — a tidy way to author a long parallel approach, not a
    claim about where the road is — and drawn to scale that 200 m owns the
    whole picture. Meanwhile the exaggeration needed to see anything at all
    across a 5.2 km by 20 m network amplifies a different problem: an edge's
    centreline is the mean of its lanes, so a section carrying three lanes
    sits 1.75 m off one carrying two, and at x100 that becomes a 13 px step.
    The corridor renders as a broken road at every section boundary.

    Since ribbon THICKNESS already carries the lane count, lateral position
    inside a corridor carries nothing — so flatten it. The threshold is
    therefore "how wide is a corridor", and the transform assumes distinct
    corridors are further apart than that, which is why it is opt-in per pod
    rather than always on.
    """
    if not t:
        return lambda y: y
    return lambda y: (0.0 if abs(y) <= t
                      else math.copysign(math.log1p(abs(y) - t), y))


def bbox(lanesets, fold):
    xs, ys = [], []
    for lanes in lanesets:
        for ln in lanes:
            for x, y in ln["shape"]:
                xs.append(x)
                ys.append(fold(y))
    return min(xs), min(ys), max(xs), max(ys)


def projector(bb, fold, exaggerate, h, w=W, pad=12):
    """Map network metres to SVG px.

    `exaggerate` fits the two axes independently. The merge pod is 5.2 km
    long and 20 m wide: at true scale it is a 400x1.5 px sliver in which the
    difference between two lanes and three is invisible. Freeway schematics
    are drawn with cross-section exaggeration for exactly this reason — but
    the drawing then misstates geometry, so the caller captions it.
    """
    x0, y0, x1, y1 = bb
    sx = (w - 2 * pad) / max(x1 - x0, 1e-6)
    sy = (h - 2 * pad) / max(y1 - y0, 1e-6)
    if not exaggerate:
        sx = sy = min(sx, sy)
    ox = pad + ((w - 2 * pad) - (x1 - x0) * sx) / 2
    oy = pad + ((h - 2 * pad) - (y1 - y0) * sy) / 2

    def proj(pt):
        x, y = pt
        # Flip y: network coordinates are metres with y up, SVG has y down.
        return ((x - x0) * sx + ox, (h - oy) - (fold(y) - y0) * sy)
    return proj, (max(sx, sy) / min(sx, sy))


def path_d(shape, proj):
    return "M" + " L".join(f"{x:.1f},{y:.1f}" for x, y in map(proj, shape))


def junction_xy(net, jid, proj):
    """Centroid of a junction's internal lanes — the pods carry no junction
    coordinates, but every internal lane is named for its junction and lies
    inside it."""
    pts = []
    for ln in net["lanes"]:
        eid = ln.get("edge", ln["id"])
        if eid.startswith("i") and eid[1:].split("_", 1)[0] == jid:
            pts.extend(ln["shape"])
    if not pts:
        return None
    return proj((sum(p[0] for p in pts) / len(pts),
                 sum(p[1] for p in pts) / len(pts)))


def sig_key(net):
    """Signal programs reduced to a comparable value, so an arm that only
    retimed is distinguishable from one that only moved tarmac."""
    return json.dumps([
        {"j": s.get("junction", s["id"]), "o": s.get("offset", 0),
         "p": [(p["duration"], p["state"]) for p in s["phases"]]}
        for s in sorted(net.get("signals", []),
                        key=lambda s: s.get("junction", s["id"]))
    ], sort_keys=True)


def cycle_len(sig):
    return sum(p["duration"] for p in sig["phases"])


def timing_strip(net, window, y0):
    """One bar per junction over a fixed wall-clock window, showing ONLY the
    main-road through phase.

    Drawing every green phase was the obvious first cut and it says nothing:
    a 4-phase signal is green for *something* 77% of the cycle, so all four
    arms render as a near-solid bar. The quantity a driver on the main road
    experiences, and the only one a green wave coordinates, is that road's
    own green — which is the first green phase of the cycle in both pods.
    Against that, a shorter cycle reads as more frequent bars and a green
    wave reads as a staircase.
    """
    sigs = sorted(net.get("signals", []),
                  key=lambda s: s.get("junction", s["id"]))
    if not sigs:
        return ""
    pad, lab = 12, 30
    bw = W - 2 * pad - lab
    rowh = min(14, (STRIP_H - 24) / max(len(sigs), 1))
    out = [f'<text x="{pad}" y="{y0 + 9}">'
           f'main-road green · {window:g}s of wall clock</text>']
    for i, s in enumerate(sigs):
        yy = y0 + 16 + i * rowh
        h = rowh - 3
        out.append(f'<text x="{pad}" y="{yy + h - 1}">'
                   f'{s.get("junction", s["id"])}</text>')
        out.append(f'<rect x="{pad + lab}" y="{yy:.1f}" width="{bw}" '
                   f'height="{h:.1f}" rx="1.5" fill="#21262d"/>')
        cyc = cycle_len(s)
        if cyc <= 0:
            continue
        # Offset shifts the program later in wall clock; start one full
        # cycle early so a large offset still paints the left-hand edge of
        # the window instead of leaving it blank.
        t = (s.get("offset", 0) % cyc) - cyc
        first = True
        while t < window:
            for p in s["phases"]:
                d = p["duration"]
                if "G" in p["state"] or "g" in p["state"]:
                    if first:
                        a, b = max(t, 0), min(t + d, window)
                        if b > a:
                            out.append(
                                f'<rect x="{pad + lab + bw * a / window:.1f}" '
                                f'y="{yy:.1f}" '
                                f'width="{bw * (b - a) / window:.1f}" '
                                f'height="{h:.1f}" fill="{INK_GREEN}"/>')
                    first = False
                t += d
            first = True
    return "\n".join(out)


def render(base, arm, bb, fold, window, want_strip, exaggerate, H):
    proj, ratio = projector(bb, fold, exaggerate, H)
    bl, al = road_lanes(base), road_lanes(arm)
    base_tar, arm_tar = Tarmac(bl), Tarmac(al)

    # ONE RIBBON PER EDGE, THICKNESS = LANE COUNT — not one stroke per lane.
    # Drawing lanes individually looks right at true scale and lies under
    # cross-section exaggeration: at the merge pod's x100 the 3.5 m between
    # lane two and lane three becomes 25 px, so "add a third lane" renders
    # as a separate parallel road running alongside the freeway. A ribbon
    # that gets thicker is what adding a lane actually looks like.
    #
    # Thickness is therefore in PIXELS PER LANE, deliberately not in metres:
    # a metric width would inherit the same exaggeration and blow the
    # mainline up into a slab.
    def centreline(lanes):
        """Mean of the parallel lane polylines on an edge.

        Picking one lane instead would put the ribbon on whichever lane
        happened to be first: on a 3-lane merge section that is the y=-3.5
        lane, so the corridor visibly steps sideways at every section
        boundary and reads as a broken road. Lanes on an edge are offsets of
        one alignment and share a point count; the odd flared turn bay does
        not, and is skipped rather than averaged in.
        """
        n = max((len(l["shape"]) for l in lanes), default=0)
        same = [l["shape"] for l in lanes if len(l["shape"]) == n]
        return [[sum(s[i][0] for s in same) / len(same),
                 sum(s[i][1] for s in same) / len(same)] for i in range(n)]

    def ribbons(lanes, other_tar, pin=None):
        by_edge = {}
        for ln in lanes:
            by_edge.setdefault(ln.get("edge", ln["id"]), []).append(ln)
        out = {}
        for eid, group in by_edge.items():
            out[eid] = {
                # Pin to the base alignment where the edge exists in both,
                # so a widened corridor keeps the same centreline in every
                # arm and only its thickness changes. Otherwise adding a
                # lane on one side shifts the mean, and under cross-section
                # exaggeration that shift is larger than the widening.
                "shape": (pin.get(eid) or centreline(group)) if pin
                         else centreline(group),
                "n": len(group),
                "new": sum(1 for l in group
                           if other_tar.novel_fraction(l) >= NOVEL),
            }
        return out

    base_e = ribbons(bl, arm_tar)
    arm_e = ribbons(al, base_tar,
                    pin={k: v["shape"] for k, v in base_e.items()})

    def w_of(n):
        return round(1.5 + 1.5 * n, 1)

    body = []
    # Draw order is reading order: what is unchanged recedes, what was
    # removed sits above it, what this option adds is on top.
    for eid, e in arm_e.items():
        if e["new"]:
            continue
        body.append(f'<path d="{path_d(e["shape"], proj)}" '
                    f'stroke="{INK_KEEP}" stroke-width="{w_of(e["n"])}"/>')
    for eid, e in base_e.items():
        if e["new"] != e["n"]:
            continue
        body.append(f'<path d="{path_d(e["shape"], proj)}" '
                    f'stroke="{INK_GONE}" stroke-width="{w_of(e["n"])}" '
                    f'stroke-dasharray="3 3" opacity=".8"/>')
    added = removed = 0
    for eid, e in arm_e.items():
        if not e["new"]:
            continue
        added += e["new"]
        # A widened edge keeps its full new width in the accent colour, so
        # the eye reads a thicker segment rather than hunting for a hairline
        # of difference against the neighbouring sections.
        body.append(f'<path d="{path_d(e["shape"], proj)}" '
                    f'stroke="{INK_NEW}" stroke-width="{w_of(e["n"])}"/>')
    removed = sum(e["new"] for e in base_e.values() if e["new"] == e["n"])

    # Signal heads, amber where this arm's program differs from base, so a
    # retiming is visible on the plan too and the strip is a detail view
    # rather than the only evidence it happened.
    bsig = {s.get("junction", s["id"]): s for s in base.get("signals", [])}
    for s in arm.get("signals", []):
        jid = s.get("junction", s["id"])
        xy = junction_xy(arm, jid, proj)
        if xy is None:
            continue
        b = bsig.get(jid)
        chg = (b is None
               or [(p["duration"], p["state"]) for p in b["phases"]]
               != [(p["duration"], p["state"]) for p in s["phases"]]
               or b.get("offset", 0) != s.get("offset", 0))
        body.append(f'<circle cx="{xy[0]:.1f}" cy="{xy[1]:.1f}" '
                    f'r="{3.6 if chg else 2.4:.1f}" '
                    f'fill="{INK_SIG if chg else INK_DIM}"/>')

    # Once either the axes are fitted independently or the cross-corridor
    # axis is folded, the drawing no longer states distance truthfully in
    # both directions. Say so on the face of it — the alternative is a
    # picture a traffic engineer would read as a survey. It goes in the
    # reserved band under the plan, not over it: at fold-y the frontage road
    # sits along the bottom edge and a caption there lands on top of it.
    caption = ""
    if ratio >= 1.5 or fold(1e4) != 1e4:
        caption = (f'<text x="{W - 12}" y="{H + 10}" '
                   f'text-anchor="end">schematic · lengths to scale, '
                   f'cross-section not</text>')
    cap_h = 14 if caption else 0

    h = H + cap_h + (STRIP_H if want_strip else 0)
    strip = timing_strip(arm, window, H + cap_h - 4) if want_strip else ""
    # Presentation attributes on a wrapping group rather than a <style>
    # block: the page inlines this SVG, but the same file is also rasterised
    # for the docs by renderers that ignore internal stylesheets — and an
    # unset fill defaults to black, which floods every curved path.
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {h}" '
        f'width="100%" role="img">'
        f'<g fill="none" stroke-linecap="round" stroke-linejoin="round">'
        + "\n".join(body) + '</g>'
        + f'<g fill="{INK_DIM}" font-family="ui-sans-serif,system-ui,'
          f'sans-serif" font-size="9">' + caption + strip + '</g></svg>'
    ), added, removed, ratio


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--pod", required=True, help="quiz key, e.g. merge-pod")
    ap.add_argument("--root", required=True, help="dir holding the arm dirs")
    ap.add_argument("--base", default="base")
    ap.add_argument("--arms", nargs="+", required=True)
    ap.add_argument("--net", default="network.json")
    ap.add_argument("--exaggerate", action="store_true",
                    help="fit the axes independently; for long thin networks "
                         "where true scale hides the cross-section")
    ap.add_argument("--fold-y", type=float, default=0.0, metavar="M",
                    help="compress the cross-corridor axis beyond M metres "
                         "of the centreline (freeway pods park their ramps "
                         "far off the mainline for authoring convenience)")
    ap.add_argument("--height", type=int, default=H, metavar="PX",
                    help="plan-view height; a folded freeway schematic wants "
                         "a short wide strip, a town plan a squarer frame")
    ap.add_argument("--out", required=True)
    args = ap.parse_args()

    base = load(os.path.join(args.root, args.base, args.net))
    arms = {a: load(os.path.join(args.root, a, args.net)) for a in args.arms}
    fold = folder(args.fold_y)

    # One frame and one time window for the whole pod, so the four cards are
    # comparable rather than four independently autoscaled pictures.
    bb = bbox([road_lanes(base)] + [road_lanes(n) for n in arms.values()],
              fold)
    cycles = [cycle_len(s) for n in [base] + list(arms.values())
              for s in n.get("signals", [])]
    window = 2 * max(cycles) if cycles else 0

    os.makedirs(args.out, exist_ok=True)
    bkey = sig_key(base)
    for name, net in arms.items():
        want = bool(net.get("signals")) and sig_key(net) != bkey
        svg, na, nr, ratio = render(base, net, bb, fold, window, want,
                                    args.exaggerate, args.height)
        path = os.path.join(args.out, f"{args.pod}__{name}.svg")
        with open(path, "w") as f:
            f.write(svg)
        print(f"[diag] {args.pod}/{name}: +{na} lanes -{nr} lanes"
              f"{' +timing' if want else ''} "
              f"{'x%.0f ' % ratio if ratio >= 1.5 else ''}-> {path}")


if __name__ == "__main__":
    main()
