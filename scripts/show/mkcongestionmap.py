#!/usr/bin/env python3
"""Render a network as a congestion map: every lane coloured by how fast it
actually ran, drawn from a run's own metrics.

    mkcongestionmap.py --network <net.json> --metrics <metrics.json> \\
        --out docs/show/img/chi-congestion.png [--warmup-tick N]

WHY THIS EXISTS. The quiz shows a guest four options and asks which one
helps. For the two authored pods a plan view answers "what am I looking at"
instantly (mkoptiondiag.py). The Chicago cut had no equivalent, and it is
the one scenario where the picture carries the argument: 55,555 lanes is not
a diagram, it is a city, and the thing worth seeing is WHERE it is slow.

DRAWN FROM THE RUN, NEVER HAND-COLOURED. The colour of every lane is its own
Edie mean speed over the measurement window divided by its posted limit.
Nothing here decides what "congested" looks like — the run does. A
hand-tinted map of the Loop would be a drawing of an opinion.

Edie's definition, per lane: sum of distance covered on that lane divided by
sum of time spent on it, over the intervals after --warmup-tick. That is the
only defensible way to average a speed over a space-time region, and it is
the same definition the show's corridor tables use (corridorspeed.py).

TWO RULES, SHARED WITH THE OTHER DERIVED TOOLING, because a map is checked
against those tables and a silent difference would be blamed on the traffic:
drop `partial` intervals (ADR-0014 §3 requires it, and a truncated window
skews a lane's colour) and refuse to sum across measurement sets (ADR-0014
permits overlapping sets; summing double-counts vehicle-time). corridorspeed.py
and whatif.py — which produces the A/B tables the map sits beside — enforce
both as of the commit that added this note; before it they enforced neither,
which is why every table predating it is being re-measured rather than
re-labelled.

THE PARTIAL RULE BITES HERE, and it is why the sidecar records
last_interval_end. The shipped Chicago run emits intervals [4000,7000),
[7000,10000) and [10000,12000), and the last is flagged partial because the
horizon cut it short — so the honest window for a 12,000-tick run is
4,000-10,000, not 4,000-12,000. Any table quoting the horizon as its window
was produced before these tools agreed and needs re-measuring, not
re-labelling.

THREE STATES, NOT TWO. A lane the set measured and found empty is drawn in
the "empty" colour rather than the free-flow one: an unused lane is not a fast
lane, and colouring it green would paint most of a city extract green. A lane
the set never measured is drawn DARKER still, because "watched, no traffic"
and "nobody watched" are different claims and one colour for both lets a
partial measurement read as a quiet city — the absent-vs-empty failure in
docs/kb/articles/concepts/silent-fidelity-failures.md. For the same reason the
--min-coverage numerator counts lanes the set MEASURED, not lanes that
happened to carry traffic, so a genuinely quiet run is not rejected as
unmeasured.

RASTER, NOT SVG, AND THAT IS A SIZE DECISION. A 55k-lane SVG is 10-20 MB and
the quiz page inlines its diagrams. A PNG of the same map is ~200 KB.

DEPENDENCY NOTE (Pillow). AGENTS.md's "standard library first; justify
dependencies" is written for the Go engine, but the spirit applies here, so:
Pillow is the ONLY non-stdlib import in this file, nothing else in the repo
depends on it, and it is confined to a presentation script that the engine,
the contract and the A/B harness never import. Rasterising 55k polylines by
hand into a PPM would avoid it and cost a few hundred lines of pixel-pushing
that nobody would review carefully. The canonical build tolerates its
absence: scripts/show/build-quiz.sh takes CHI_SKIP_MAP=1 and mkquiz.py ships
the Chicago card text-only, so a checkout without Pillow still builds the
page. If a second consumer ever appears, that is the point to pin it
properly rather than now.

The warmup cut is not cosmetic. A run starts from an empty network and
fills; intervals from that period describe a road with nothing on it, so
including them makes every lane look faster the shorter the run was. Quote
the tick alongside the picture.
"""
import argparse
import collections
import hashlib
import json
import os
import sys

try:
    from PIL import Image, ImageDraw
except ImportError:
    sys.exit("mkcongestionmap: needs Pillow (pip install Pillow)")

# Ratio of actual speed to posted limit -> colour, as half-open bands
# [previous, this). Deliberately coarse and ordered worst-first: the eye
# should find the jams, not admire a gradient. Five bands because that is
# about how many a viewer can hold at a glance; the boundaries are round
# numbers, not calibrated thresholds, and nothing downstream keys off them.
BANDS = [
    (0.20, (176, 22, 26)),    # < 20% of limit — stopped
    (0.40, (214, 78, 32)),    # crawling
    (0.60, (232, 152, 42)),   # slow
    (0.80, (206, 200, 62)),   # below limit but moving
    (10.0, (72, 156, 92)),    # at or above limit
]
EMPTY = (48, 54, 62)          # MEASURED, and no traffic in the window
# Not in the measurement set at all — nothing is known about this lane. A
# distinct, darker grey than EMPTY on purpose: "we watched this road and it
# was empty" and "nobody watched this road" are different claims, and drawing
# them in one colour is how a partial measurement reads as a quiet network.
UNMEASURED = (28, 32, 38)
BG = (13, 17, 23)             # matches the quiz page --bg

# Refuse when more than this share of MEASURED lanes are unknown to the
# network. Not zero: a cut can legitimately trim a lane the run still
# reported (a boundary lane clipped out of the drawn network), and failing
# on one stray id would make the guard the thing people work around. A
# fifth is far above that and far below the wrong-cut case, which puts most
# of the run's lanes outside the network.
MAX_FOREIGN_SHARE = 0.2


def lane_speeds(metrics_path, warmup_tick, set_id=None, end_tick=None):
    """(speeds, measured) — Edie mean speed (m/s), and what was measured.

    Two return values because ABSENT and EMPTY are different facts and this
    map must not draw them the same way. `speeds` carries only lanes with
    vehicle-time, since a lane with none has no mean speed to take. `measured`
    is every lane the set reported AT ALL, including those that reported zero
    traffic. A lane outside the set is unmeasured — nothing is known about it;
    a lane in the set with no vehicle-time was watched and found empty, which
    is a measurement. Collapsing the two renders a quiet street identically to
    one nobody looked at, and — worse — made the coverage check below count
    OCCUPIED lanes as its numerator, so a legitimately quiet run failed
    --min-coverage as though it had not been measured. That is the
    absent-vs-empty failure in docs/kb/articles/concepts/
    silent-fidelity-failures.md, in the tool whose job is to show fidelity.

    ONE measurement set only. ADR-0014 allows overlapping sets over the same
    lanes with different windows; summing across them would count the same
    vehicle-time twice and quietly bias the colour. If the file carries more
    than one set the caller must name which, rather than getting a blend.

    The warmup cut drops any interval whose begin_tick is before it, so the
    effective start is the first interval boundary at or after --warmup-tick,
    not the tick itself: an aggregate cannot be split after the fact. The
    --end-tick cut is the same rule at the other end: an interval is kept
    only if it ENDS at or before it, so a longer run drawn against a shorter
    published window measures that window rather than everything it happens
    to contain.
    """
    with open(metrics_path) as f:
        doc = json.load(f)
    intervals = doc.get("intervals") or []
    sets = {iv.get("set_id") for iv in intervals}
    if not sets:
        sys.exit(f"mkcongestionmap: {metrics_path} has no intervals — "
                 f"the run wrote no measurements to draw")
    named = sorted(x for x in sets if x)
    if set_id is None:
        if len(sets) > 1:
            # len(sets), not len(named): an unset id is a distinct set for
            # double-counting purposes even though it has no name to print.
            sys.exit(f"mkcongestionmap: {metrics_path} carries {len(sets)} "
                     f"measurement sets (named: {named}); pass --set to pick "
                     f"one (summing them double-counts vehicle-time)")
        set_id = next(iter(sets))
    elif set_id not in sets:
        sys.exit(f"mkcongestionmap: no set {set_id!r} in {metrics_path} "
                 f"(has {named})")
    acc = collections.defaultdict(lambda: [0.0, 0.0])
    first_used = last_used = None
    for iv in intervals:
        if iv.get("set_id") != set_id:
            continue
        # ADR-0014: derived tooling drops PARTIAL intervals. A partial is a
        # window the horizon cut short, so its sum_time is truncated while
        # its sum_dist is whatever happened to be covered — including it
        # makes a lane's colour depend on where the run stopped.
        if iv.get("partial"):
            continue
        if iv.get("begin_tick", 0) < warmup_tick:
            continue
        et = iv.get("end_tick", 0)
        if end_tick is not None and et > end_tick:
            continue
        lid = iv.get("lane_id")
        if not lid:
            continue
        bt = iv.get("begin_tick", 0)
        if first_used is None or bt < first_used:
            first_used = bt
        if last_used is None or et > last_used:
            last_used = et
        a = acc[lid]
        a[0] += iv["sum_dist_m"]
        a[1] += iv["sum_time_s"]
    # acc holds every lane the set reported. Only those with vehicle-time get
    # a speed; the rest are measured-and-empty, and stay in `measured`.
    return ({lid: d / t for lid, (d, t) in acc.items() if t > 0},
            set(acc), set_id, first_used, last_used)


def colour_for(speed_ms, limit_ms):
    if limit_ms <= 0:
        return EMPTY
    r = speed_ms / limit_ms
    for hi, rgb in BANDS:
        if r < hi:
            return rgb
    return BANDS[-1][1]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--network", required=True)
    ap.add_argument("--metrics", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--warmup-tick", type=int, default=0,
                    help="drop intervals BEGINNING before this tick; the cut "
                         "lands on an interval boundary, not on the tick")
    ap.add_argument("--end-tick", type=int, default=None,
                    help="drop intervals ENDING after this tick. Pass the "
                         "horizon the published numbers were measured over: "
                         "a metrics file from a longer run otherwise draws a "
                         "different window than the table beside it claims.")
    ap.add_argument("--set", dest="set_id", default=None,
                    help="measurement set to read when the file has more "
                         "than one (ADR-0014 allows overlapping sets)")
    ap.add_argument("--width", type=int, default=1600)
    ap.add_argument("--margin", type=int, default=12)
    ap.add_argument("--note", default="",
                    help="caveat to carry with the picture, e.g. the run's "
                         "uncontrolled-coasting share. serve prints fidelity "
                         "figures it does NOT write into -metrics-out, so "
                         "they cannot be picked up automatically; a map that "
                         "asserts how fast the city ran should disclose them "
                         "rather than let the colours imply a clean run.")
    ap.add_argument("--run-label", default="",
                    help="human name for the run this map draws (scenario, "
                         "seed, ticks). The metrics document carries no run "
                         "identity of its own, so without this the sidecar "
                         "can only offer a content hash.")
    ap.add_argument("--min-limit", type=float, default=0.1,
                    help="ignore lanes with a lower posted limit (m/s)")
    ap.add_argument("--min-coverage", type=float, default=0.25,
                    help="refuse when a smaller share of the network's "
                         "drawable lanes was measured — the signature of a "
                         "run from a different, smaller cut of the same city")
    args = ap.parse_args()

    with open(args.network) as f:
        net = json.load(f)
    lanes = net["lanes"]
    speeds, measured_ids, used_set, first_tick, last_tick = lane_speeds(
        args.metrics, args.warmup_tick, args.set_id, args.end_tick)

    # Bind the metrics to the network. Lane ids are content-derived, so two
    # Chicago cuts SHARE them where they share geometry — chi-loop-cbd's
    # lanes are a subset of chi-loop-urban's. Draw one cut's run against the
    # other's network and every fail-closed guard here still passes: the
    # overlap measures, the rest goes grey, and the result reads as a quiet
    # city rather than as the wrong file. That is the same class of mistake
    # this script exists to prevent, so it is checked rather than trusted.
    # The check has to run BOTH ways, and the second way is the one that
    # bites. Metrics-not-in-network catches a big run drawn on a small cut.
    # The opposite — a SMALL cut's run drawn on the big network — is the
    # subset case, where every measured id is legitimately present, nothing
    # is foreign, and the map paints the CBD normally while the rest of
    # Chicago goes grey: a picture of a city where only downtown has cars.
    # One-directional checking would pass it.
    net_ids = {ln.get("id") for ln in lanes}
    foreign = [lid for lid in measured_ids if lid not in net_ids]
    if measured_ids and len(foreign) > len(measured_ids) * MAX_FOREIGN_SHARE:
        sys.exit(f"mkcongestionmap: {len(foreign)} of {len(measured_ids)} measured "
                 f"lanes are absent from {args.network} — these metrics are "
                 f"very likely from a different network cut (e.g. "
                 f"{sorted(foreign)[:3]}). Pass the network the run used.")
    # Coverage is a heuristic, not identity: metrics carry no network digest
    # (engine/metricsjson.go stamps none), so there is nothing to compare.
    #
    # It asks "did this run MEASURE this network", which is the question the
    # guard needs, and it used to ask "did this run put traffic on this
    # network" — a different question with a much noisier answer, since how
    # much of a city is busy depends on the demand rather than on whether the
    # right file was passed. The shipped Chicago map measures 55,555 of 55,555
    # eligible lanes (100%) of which 24,593 carried traffic (44%); under the
    # old occupied-based numerator the same correct pairing scored 44%, so the
    # floor had to sit below the busiest fraction of any legitimate run and
    # a quiet-but-correct run could fail outright. Measured-based, a correct
    # pairing scores ~100% and a wrong cut scores the share of lanes the two
    # cuts happen to have in common — chi-loop-cbd's are a few percent of
    # chi-loop-urban's. The 25% floor is now conservative rather than
    # finely balanced, and is left there deliberately: a corridor study drawn
    # on a full-city network is legitimate and says so with --min-coverage.
    eligible = sum(1 for ln in lanes
                   if len(ln.get("shape") or ()) >= 2
                   and ln.get("speedLimit", 0.0) >= args.min_limit)
    # Numerator counts lanes that are BOTH measured and in this network. Up
    # to MAX_FOREIGN_SHARE of the measured set can be foreign without
    # tripping the check above, and counting those toward coverage of a
    # network they are not in would overstate it by exactly the amount that
    # matters.
    resident = len(measured_ids) - len(foreign)
    cover = resident / eligible if eligible else 0.0
    if cover < args.min_coverage:
        sys.exit(f"mkcongestionmap: only {resident:,} of {eligible:,} "
                 f"drawable lanes ({cover:.1%}) were measured, below "
                 f"--min-coverage {args.min_coverage:.0%}. A run of a SMALLER "
                 f"cut drawn on a bigger network looks exactly like this — "
                 f"its lane ids are all valid here, so nothing reads as "
                 f"foreign, and the rest of the map goes grey as though the "
                 f"city were empty. Pass the network the run used, or "
                 f"--min-coverage if the sparsity is real.")

    # Bounds over ALL lane geometry, including lanes later skipped by
    # --min-limit. Slightly looser framing than strictly necessary, and
    # deliberately so: the frame then does not shift when the filter does,
    # so two maps of the same network stay comparable.
    xs, ys = [], []
    for ln in lanes:
        for x, y in ln.get("shape", ()):
            xs.append(x)
            ys.append(y)
    if not xs:
        sys.exit("mkcongestionmap: network has no lane geometry")
    minx, maxx, miny, maxy = min(xs), max(xs), min(ys), max(ys)
    span_x, span_y = maxx - minx, maxy - miny
    if span_x <= 0 or span_y <= 0:
        sys.exit("mkcongestionmap: degenerate network bounds")

    W = args.width
    H = max(1, int(round(W * span_y / span_x)))
    img = Image.new("RGB", (W + 2 * args.margin, H + 2 * args.margin), BG)
    dr = ImageDraw.Draw(img)
    sx = W / span_x
    sy = H / span_y

    def project(p):
        # y flips: metric frame is north-up, image rows run downward.
        return (args.margin + (p[0] - minx) * sx,
                args.margin + (maxy - p[1]) * sy)

    # Draw the unmeasured network first so measured lanes sit on top of it —
    # otherwise a busy arterial disappears under the empty side street that
    # happens to be drawn later.
    # THREE states, drawn bottom to top so the busiest road wins the pixel:
    # 0 unmeasured (nothing known), 1 measured-and-empty (watched, no traffic),
    # 2 occupied (a speed to colour by). Two states collapsed 0 into 1.
    drawn = measured = empty_measured = unmeasured = 0
    for pas in (0, 1, 2):
        for ln in lanes:
            shape = ln.get("shape") or ()
            if len(shape) < 2:
                continue
            lid = ln["id"]
            sp = speeds.get(lid)
            state = 2 if sp is not None else (1 if lid in measured_ids else 0)
            if state != pas:
                continue
            lim = ln.get("speedLimit", 0.0)
            if lim < args.min_limit:
                continue
            col = UNMEASURED if state == 0 else EMPTY if state == 1 else colour_for(sp, lim)
            dr.line([project(p) for p in shape], fill=col, width=1)
            drawn += 1
            if state == 2:
                measured += 1
            elif state == 1:
                empty_measured += 1
            else:
                unmeasured += 1

    # FAIL CLOSED on a map with nothing on it. The realistic way to get here
    # is a --warmup-tick at or past the run's last interval — e.g. the 6,000
    # default applied to a 6,000-tick file — which yields an all-grey image
    # that is indistinguishable on screen from a genuinely quiet network. An
    # empty map under the right filename is the same failure as a stale one.
    if measured == 0:
        window = (f"at or after tick {args.warmup_tick}" if args.end_tick is None
                  else f"in ticks {args.warmup_tick}-{args.end_tick}")
        sys.exit(f"mkcongestionmap: no lane carried traffic {window} in "
                 f"{args.metrics} (set {used_set!r}) — every lane would draw "
                 f"empty. Check the window against the run's horizon; "
                 f"refusing to write a blank map.")

    img.save(args.out)

    # Provenance sidecar. A picture that asserts "this is how fast the city
    # ran" has to be able to say WHICH run, or it is a drawing again — and
    # the failure is silent, because a stale PNG under the right filename
    # looks exactly like a fresh one. mkquiz.py reads this to caption the
    # slide; anything else can diff it to notice drift.
    # Repo-relative where possible: an absolute path leaks the local
    # username into a checked-in file and, worse, names a directory that will
    # not exist for whoever reads it. The digest is what actually identifies
    # the run — the metrics document itself carries no seed, scenario hash or
    # run id (engine/metricsjson.go), so a content hash is the only handle.
    repo = os.path.dirname(os.path.dirname(os.path.dirname(
        os.path.abspath(__file__))))

    def rel(pth):
        a = os.path.abspath(pth)
        if a.startswith(repo + os.sep):
            return os.path.relpath(a, repo)
        # Outside the repo (a scratch dir, someone else's checkout): keep the
        # basename only. The full path would name a directory the reader does
        # not have AND leak the local username into a checked-in file, while
        # adding nothing — metrics_sha256 and run_label are what identify the
        # run.
        return os.path.basename(a)

    h = hashlib.sha256()
    with open(args.metrics, "rb") as f:
        for blk in iter(lambda: f.read(1 << 20), b""):
            h.update(blk)
    prov = {
        "network": rel(args.network),
        "metrics": rel(args.metrics),
        "metrics_sha256": h.hexdigest(),
        "metrics_bytes": os.path.getsize(args.metrics),
        "run_label": args.run_label,
        "note": args.note,
        "set_id": used_set,
        "warmup_tick": args.warmup_tick,
        # What was actually measured. The cut lands on an interval boundary,
        # so the requested tick and the effective one differ whenever the
        # request falls mid-interval; captioning the request would overstate
        # precision the aggregate does not have.
        "first_interval_tick": first_tick,
        "end_tick": args.end_tick,
        "last_interval_end": last_tick,
        "width": args.width,
        "lanes_drawn": drawn,
        "lanes_with_traffic": measured,
        # The three render states, so a reader of the sidecar can tell a quiet
        # network from a partly-measured one without opening the PNG.
        "lanes_measured_empty": empty_measured,
        "lanes_unmeasured": unmeasured,
        "measured_lanes_in_set": len(measured_ids),
        "bands": [[hi, list(rgb)] for hi, rgb in BANDS],
        "empty_rgb": list(EMPTY),
        "unmeasured_rgb": list(UNMEASURED),
    }
    with open(args.out + ".json", "w") as f:
        json.dump(prov, f, indent=2, sort_keys=True)
        f.write("\n")
    print(f"[congestion] {drawn} lanes drawn: {measured} carried traffic, "
          f"{empty_measured} measured-empty, {unmeasured} unmeasured "
          f"after tick {args.warmup_tick} -> {args.out} "
          f"({img.size[0]}x{img.size[1]})", file=sys.stderr)

    # The legend belongs next to the picture wherever it is used, so print
    # the bands rather than burning them into the raster at a fixed size.
    lo = 0.0
    parts = []
    for hi, rgb in BANDS[:-1]:
        parts.append(f"{lo:.0%}-{hi:.0%} {rgb}")
        lo = hi
    parts.append(f">={lo:.0%} {BANDS[-1][1]}")
    print("bands (share of posted limit, half-open): " + ", ".join(parts)
          + f", measured-empty {EMPTY}, unmeasured {UNMEASURED}")


if __name__ == "__main__":
    main()
