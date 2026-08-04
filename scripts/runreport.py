#!/usr/bin/env python3
"""runreport.py — the standard report for "how did this run perform, and where?"

    runreport.py metrics.json --network net.json [--corridors corridors.json]
                 [--zones districts.json] [--json out.json] [--top 15]

WHY A STANDARD REPORT
--------------------------------------------------------------------------
Every calibration round so far invented its own numbers, and two of them were
actively misleading:

  * A NETWORK MEAN DENSITY of "27% of critical" was read as "not congested".
    It was an average over 2,203 lane-km including every empty residential
    street. The same run had 55 lane-km sitting at or above critical moving at
    6.3 km/h. A mean cannot express "a small part of the road is destroyed";
    only a distribution can.

  * A CORRIDOR MEAN SPEED "in the 25-45 km/h AM band" was read as "the
    expressway is loaded like the real one". It was a blend of a few jammed
    segments and mostly free-flowing road. The real Kennedy at 8am is not 5%
    jammed and 95% empty.

So the unit of measurement here is the SPACE-TIME CELL: one lane over one
metrics interval. A cell carries lane-km and duration, and every distribution
below is a share of lane-km (or of VMT), never an average of averages. That
is the only framing in which "27%" and "55 lane-km at 6 km/h" stop
contradicting each other.

WHAT IT REPORTS
--------------------------------------------------------------------------
  1. window       which intervals survived, and what fraction of the run
  2. totals       VMT, VHT, Edie speed, trips completed vs still driving
  3. density      share of lane-km by band, relative to critical density
  4. speed        share of lane-km AND share of VMT by speed band
  5. groups       the same, split by corridor and by district
  6. curve        per interval, so fill / peak / drain are separable
  7. hotspots     the lanes carrying the delay, with coordinates

Conventions this file is required to keep (each was learned the hard way):

  * PARTIAL INTERVALS ARE DROPPED (ADR-0014 3) and the count is printed. An
    interval cut short by the horizon has a duration the density denominator
    does not know about.
  * DENOMINATORS ARE FIXED. Lane-km is the lane-km of the network, not of
    whatever happened to be occupied. Otherwise an emptying network looks
    denser as it drains.
  * EMPTY ROAD IS REPORTED, NOT DROPPED. A lane with no vehicles has no
    defined speed, so it gets its own bucket rather than being averaged in as
    a zero or silently excluded.
  * MULTI-SET FILES ARE REFUSED without --set. ADR-0014 permits overlapping
    measurement sets; summing two of them double-counts the overlap.

MEMORY
--------------------------------------------------------------------------
A 90-minute whole-network run is 180 intervals x 55,555 lanes = 10M records
and ~400 MB of JSON. `json.load` on that materialises 10M dicts at once. So
intervals are STREAMED and aggregated in a single pass — nothing here needs
two cells at the same time, and the per-lane and per-interval accumulators
are bounded by the network and the horizon rather than by their product.
"""
import argparse
import collections
import json
import math
import os
import sys

# Roughly the density at capacity for a freeway lane. Used only to express
# density as a fraction of it — nothing branches on the exact value.
CRITICAL_K = 25.0

# Upper edge of each band, as a fraction of CRITICAL_K. "empty" is separate:
# zero vehicles is a different statement from "very light".
K_BANDS = [(0.25, "<25%"), (0.50, "25-50%"), (0.75, "50-75%"),
           (1.00, "75-100%"), (1.50, "100-150%"), (float("inf"), ">150%")]

# Upper edge of each speed band, km/h. The 20 km/h line is the one that
# matters: below it a road is not flowing, it is queueing.
V_BANDS = [(10.0, "<10"), (20.0, "10-20"), (30.0, "20-30"),
           (45.0, "30-45"), (60.0, "45-60"), (float("inf"), "60+")]

STREAM_ABOVE = 200 << 20        # bytes; above this, do not hold the array


def lane_length_m(ln):
    """The lane's length in metres, from the network's OWN `length` field.

    Not re-derived from `shape`. The two disagree: on chi-loop-urban 4,697
    of 55,555 lanes (8.5%) differ by more than 5%, and 895 lanes carry a
    positive length against a degenerate shape whose polyline measures
    exactly 0 m — junction internals emitted with a single point or a
    zero-length segment. Re-deriving makes those lanes 0.0 km, which drops
    them out of every distribution and every group denominator: the
    survivorship bias ADR-0014 §3 forbids, arrived at by arithmetic instead
    of omission. `length` is what the kernel positions vehicles along, so it
    is the only length for which `sum_dist_m / length` is a real occupancy.

    Geometry is the fallback for a network that predates the field, and a
    non-positive result is returned as 0.0 for the caller to refuse.
    """
    v = ln.get("length")
    if v is not None:
        return float(v) if float(v) > 0.0 else 0.0
    s = ln.get("shape") or []
    if len(s) < 2:
        return 0.0
    return sum(math.dist(s[i], s[i + 1]) for i in range(len(s) - 1))


def lane_centre(ln):
    """A point at the lane's arclength midpoint, for map coordinates.

    The middle SHAPE POINT is not the middle of the lane: vertices cluster
    where a road bends, so on a polyline with an elbow the middle vertex
    sits wherever the detail is. A two-point lane is the sharp case —
    `s[len(s) // 2]` is `s[1]`, its far END, which is a different district
    than the lane's middle whenever the lane crosses a boundary.
    """
    s = ln.get("shape") or []
    if not s:
        return None
    if len(s) == 1:
        return tuple(s[0])
    segs = [math.dist(s[i], s[i + 1]) for i in range(len(s) - 1)]
    half = sum(segs) / 2.0
    if half <= 0.0:
        return tuple(s[0])  # degenerate polyline: every point is the same
    run = 0.0
    for i, seg in enumerate(segs):
        if run + seg >= half:
            t = (half - run) / seg if seg > 0.0 else 0.0
            ax, ay = s[i][0], s[i][1]
            bx, by = s[i + 1][0], s[i + 1][1]
            return (ax + (bx - ax) * t, ay + (by - ay) * t)
        run += seg
    return tuple(s[-1])


def band_of(value, bands):
    for edge, name in bands:
        if value < edge:
            return name
    return bands[-1][1]


class Dist:
    """A lane-km-weighted distribution over bands, plus a VMT-weighted one.

    Both are accumulated together because they answer different questions and
    disagreeing with each other is informative: 30% of lane-km below 20 km/h
    with 5% of VMT there means the jams are real but few people are in them;
    the reverse means the network is fine except where everybody is.
    """

    def __init__(self, bands):
        self.bands = [n for _, n in bands]
        self.km = collections.Counter()      # lane-km-hours by band
        self.vmt = collections.Counter()     # veh-km by band
        self.empty_km = 0.0                  # lane-km-hours with no vehicles
        self.total_km = 0.0
        self.total_vmt = 0.0

    def add(self, band, lane_km_h, vmt):
        self.km[band] += lane_km_h
        self.vmt[band] += vmt
        self.total_km += lane_km_h
        self.total_vmt += vmt

    def add_empty(self, lane_km_h):
        self.empty_km += lane_km_h
        self.total_km += lane_km_h

    def rows(self):
        for b in self.bands:
            km = self.km[b]
            yield (b,
                   100 * km / self.total_km if self.total_km else 0.0,
                   100 * self.vmt[b] / self.total_vmt if self.total_vmt else 0.0)

    def as_json(self):
        # `band_order` is explicit because `bands` is an object, and an
        # object's key order is a serialization accident. A consumer that
        # sorts the keys renders "<25%", "100-150%", ">150%" in string order,
        # which is not the axis. Without this every consumer has to duplicate
        # K_BANDS/V_BANDS to get the axis right — the viz panel did.
        return dict(
            empty_pct=100 * self.empty_km / self.total_km if self.total_km else 0.0,
            band_order=list(self.bands),
            bands={n: dict(pct_lane_km=a, pct_vmt=b) for n, a, b in self.rows()})


def bar(pct, width=28):
    return "#" * int(round(width * pct / 100.0))


def print_dist(title, d, unit):
    print(f"\n{title}")
    print(f"  {unit:>10s} {'% lane-km':>10s} {'% VMT':>8s}  ")
    if d.total_km:
        share = 100 * d.empty_km / d.total_km
        print(f"  {'empty':>10s} {share:9.1f}% {'-':>8s}  {bar(share)}")
    for name, kpc, vpc in d.rows():
        print(f"  {name:>10s} {kpc:9.1f}% {vpc:7.1f}%  {bar(kpc)}")


# --------------------------------------------------------------------------
# reading


def stream_intervals(path, chunk=1 << 22):
    """Yield interval objects one at a time without holding the array.

    Assumes `intervals` is a flat array of objects, which is what ADR-0014
    emits. The stdlib decoder is driven over a sliding buffer; a record that
    straddles a chunk boundary pulls more input and retries.
    """
    dec = json.JSONDecoder()
    with open(path) as f:
        buf = ""
        while '"intervals"' not in buf:
            more = f.read(chunk)
            if not more:
                return
            buf += more
        # The key can land in the buffer a chunk before its `[` does, so keep
        # reading until the bracket is actually present rather than assuming
        # one read covers `"intervals": [`.
        at = buf.index('"intervals"') + len('"intervals"')
        while "[" not in buf[at:]:
            more = f.read(chunk)
            if not more:
                return
            buf += more
        buf = buf[buf.index("[", at) + 1:]
        while True:
            buf = buf.lstrip()
            while not buf:
                more = f.read(chunk)
                if not more:
                    return
                buf = more.lstrip()
            if buf[0] == "]":
                return
            if buf[0] == ",":
                buf = buf[1:]
                continue
            try:
                obj, end = dec.raw_decode(buf)
            except ValueError:
                more = f.read(chunk)
                if not more:
                    raise
                buf += more
                continue
            yield obj
            buf = buf[end:]


def load_head(path):
    """Scalars and totals for a file too large to load whole.

    `totals` sits after the huge arrays, so it is found by scanning the tail
    rather than by re-reading the whole file.
    """
    dec = json.JSONDecoder()
    out = {}
    with open(path) as f:
        head = f.read(1 << 20)
    for key in ("ticks", "dt", "schema_version"):
        m = f'"{key}"'
        if m in head:
            i = head.index(":", head.index(m) + len(m)) + 1
            out[key], _ = dec.raw_decode(head[i:].lstrip())
    with open(path, "rb") as f:
        f.seek(max(0, os.path.getsize(path) - (1 << 22)))
        tail = f.read().decode("utf-8", "ignore")
    if '"totals"' in tail:
        i = tail.index(":", tail.index('"totals"') + 8) + 1
        try:
            out["totals"], _ = dec.raw_decode(tail[i:].lstrip())
        except ValueError:
            pass
    out.setdefault("totals", {})
    return out


def open_metrics(path, set_id):
    """(header dict, interval iterator). The iterator is single-use."""
    size = os.path.getsize(path)
    if size < STREAM_ABOVE:
        with open(path) as f:
            m = json.load(f)
        head = {k: v for k, v in m.items() if k != "intervals"}
        head["n_trips"] = len(m.get("trips") or ())
        ivs = iter(m.get("intervals") or ())
    else:
        head = load_head(path)
        # Counting `trips` would mean a second full pass over 400 MB for a
        # number `totals.completed_trips` already contextualises. Skip it and
        # say so rather than print a wrong denominator.
        head["n_trips"] = None
        ivs = stream_intervals(path)
        print(f"runreport: {size / (1 << 20):,.0f} MB metrics file — streaming "
              f"intervals in one pass", file=sys.stderr)

    seen = set()

    def gate():
        for iv in ivs:
            s = iv.get("set_id")
            if set_id is not None:
                if s == set_id:
                    seen.add(s)
                    yield iv
                continue
            if s not in seen:
                seen.add(s)
                if len(seen) > 1:
                    sys.exit(f"runreport: {path} has more than one measurement "
                             f"set ({sorted(x for x in seen if x)}); pass "
                             f"--set to pick one")
            yield iv

    return head, gate(), seen


# --------------------------------------------------------------------------


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("metrics")
    ap.add_argument("--network", required=True,
                    help="network JSON — supplies the FIXED lane-km "
                         "denominator and lane coordinates")
    ap.add_argument("--corridors", default=None,
                    help="corridors.json; adds the per-corridor table and "
                         "the freeway/arterial split")
    ap.add_argument("--zones", default=None,
                    help="districts.json (mkzones.py); adds the per-district "
                         "table. Corridors say where congestion is along the "
                         "expressways; 81%% of the network is unnamed grid and "
                         "districts are the only thing that locates that.")
    ap.add_argument("--set", dest="set_id", default=None)
    ap.add_argument("--warmup-tick", type=int, default=0,
                    help="ignore intervals beginning before this tick")
    ap.add_argument("--top", type=int, default=15,
                    help="how many hotspot lanes to list")
    ap.add_argument("--json", dest="json_out", default=None,
                    help="also write the report as JSON for A/B tooling")
    ap.add_argument("--no-curve", action="store_true")
    args = ap.parse_args()

    with open(args.network) as f:
        net = json.load(f)
    L = {l["id"]: l for l in net["lanes"]}
    length_km = {lid: lane_length_m(ln) / 1000.0 for lid, ln in L.items()}
    net_km = sum(length_km.values())

    lane2c = {}
    if args.corridors:
        with open(args.corridors) as f:
            lane2c = json.load(f)["lanes"]
    lane2z = {}
    if args.zones:
        with open(args.zones) as f:
            lane2z = json.load(f)["lanes"]
    # Corridor lane-km. Provisional: it is the whole corridor's length here,
    # and narrowed to the measured lanes after the pass if the set turns out
    # to cover only a subset (see the coverage reconciliation). It cannot be
    # computed correctly before the pass because which lanes were measured is
    # only known once they have been read.
    fw_km_network = sum(length_km[l] for l in lane2c if l in length_km)
    fw_km = fw_km_network

    # A corridor and a district are the same kind of object here — a named
    # set of lanes — and the questions asked of them are identical.
    groupings = []
    if lane2c:
        groupings.append(("CORRIDORS", lane2c, "(arterial grid)"))
    if lane2z:
        groupings.append(("DISTRICTS", lane2z, "(outside all districts)"))

    head, intervals, seen_sets = open_metrics(args.metrics, args.set_id)
    dt = head["dt"]

    # ---- ONE streaming pass -------------------------------------------
    kd = collections.defaultdict(lambda: Dist(K_BANDS))
    vd = collections.defaultdict(lambda: Dist(V_BANDS))
    agg = {title: collections.defaultdict(lambda: [0.0] * 5)
           for title, _, _ in groupings}
    per_iv = collections.defaultdict(lambda: [0.0] * 6)
    lane_agg = collections.defaultdict(lambda: [0.0, 0.0, 0.0])
    lo = hi = None
    n_cells = dropped = skipped = 0
    tot_d = tot_t = 0.0
    unknown = set()
    # Which lanes this measurement set actually covers. ADR-0014 permits a
    # set over a SUBSET of the network, and a subset changes what every
    # denominator below means: lane-km comes from the network file, while
    # travel and delay come only from lanes that reported. Mixing them
    # reports a fraction of the traffic over all of the road and calls the
    # result "network". Collected here and reconciled after the pass.
    measured = set()
    zero_len = set()

    def scopes(lid):
        if not lane2c:
            return ("network",)
        return ("network", "corridors" if lane2c.get(lid) else "arterial grid")

    for iv in intervals:
        if iv.get("partial"):
            dropped += 1
            continue
        b, e = iv["begin_tick"], iv["end_tick"]
        if b < args.warmup_tick:
            skipped += 1
            continue
        lid = iv["lane_id"]
        km = length_km.get(lid)
        if km is None:
            unknown.add(lid)
            continue
        n_cells += 1
        measured.add(lid)
        # time_loss_s is OPTIONAL: engine/metrics.go declares TimeLossS as a
        # pointer, nil when the time_loss group is off, and the JSON writer
        # omits it. Subscripting it raised a bare KeyError on a perfectly
        # legal metrics file — a traceback where the tool should either work
        # or explain itself. Delay is a headline of this report and a
        # silently-zero one would read as "no delay", so a set without the
        # group is refused by name below rather than reported as fast.
        if "time_loss_s" not in iv:
            sys.exit(
                f"runreport: {args.metrics} interval for lane {lid} has no "
                f"time_loss_s — this measurement set was configured without "
                f"the `time_loss` group, and delay is a headline of this "
                f"report (hotspots are RANKED by it), so reporting zero loss "
                f"would be a wrong answer rather than a missing one. "
                f"Re-run with the time_loss group enabled, or use "
                f"corridorspeed.py for a speed-only question.")
        dist, time_s, loss = iv["sum_dist_m"], iv["sum_time_s"], iv["time_loss_s"]
        tot_d += dist
        tot_t += time_s
        lo = b if lo is None else min(lo, b)
        hi = e if hi is None else max(hi, e)

        a = lane_agg[lid]
        a[0] += dist
        a[1] += time_s
        a[2] += loss

        if km <= 0:
            zero_len.add(lid)
            continue
        dur_s = (e - b) * dt
        lkh = km * dur_s / 3600.0              # lane-km-hours: the cell's size
        # Mean vehicles present = veh-seconds / duration. Density is that per
        # km of this (single) lane, so no lane-count divisor is needed.
        k = (time_s / dur_s) / km if dur_s else 0.0
        over = k >= CRITICAL_K

        if time_s > 0:
            kb = band_of(k / CRITICAL_K, K_BANDS)
            vb = band_of(dist / time_s * 3.6, V_BANDS)
            for s in scopes(lid):
                kd[s].add(kb, lkh, dist / 1000.0)
                vd[s].add(vb, lkh, dist / 1000.0)
        else:
            for s in scopes(lid):
                kd[s].add_empty(lkh)
                vd[s].add_empty(lkh)

        for title, lmap, other in groupings:
            c = agg[title][lmap.get(lid) or other]
            c[0] += dist
            c[1] += time_s
            c[2] += lkh
            c[3] += loss
            if over:
                c[4] += lkh

        if not args.no_curve:
            p = per_iv[(b, e)]
            p[0] += dist
            p[1] += time_s
            if over:
                p[4] += km
            if lane2c.get(lid):
                p[2] += dist
                p[3] += time_s
                if over:
                    p[5] += km

    if lo is None:
        # Distinguish "your --set matched nothing" from "every interval was
        # partial". Both leave the report empty; only one is the user's typo.
        if args.set_id is not None and not seen_sets:
            sys.exit(f"runreport: no set {args.set_id!r} in {args.metrics}")
        sys.exit("runreport: no complete interval survived (ADR-0014 3)")

    # ---- 1. window ------------------------------------------------------
    run_min = head["ticks"] * dt / 60.0
    win_min = (hi - lo) * dt / 60.0
    print(f"RUN  {args.metrics}")
    print(f"  horizon    {head['ticks']:,} ticks = {run_min:.0f} min at dt={dt}")
    print(f"  window     ticks {lo:,}-{hi:,} = {win_min:.1f} min "
          f"({100 * win_min / run_min:.0f}% of the run)")
    print(f"  intervals  {n_cells:,} cells; dropped {dropped:,} partial"
          + (f", skipped {skipped:,} pre-warmup" if skipped else ""))
    if unknown:
        print(f"  WARNING    {len(unknown):,} measured lanes are absent from "
              f"the network file — lane-km for them cannot be counted")

    # ---- coverage: is this set the whole network, or a subset? -----------
    # ADR-0014 §3's fixed-network denominator only holds if the set covers
    # the network. When it does not, every "network" figure below is a subset
    # numerator, and saying so is the difference between a report and a
    # misleading one. The measured lane-km becomes the denominator; the
    # network total is still printed so the gap is visible rather than
    # implied.
    meas_km = sum(length_km[lid] for lid in measured if lid in length_km)
    covers_all = len(measured) >= len(length_km)
    scope_km = net_km if covers_all else meas_km
    if not covers_all:
        # Same correction for the corridor denominator, which the curve's
        # freeway columns divide by: measured corridor lane-km, not all of it.
        fw_km = sum(length_km[l] for l in lane2c
                    if l in length_km and l in measured)
        print(f"  COVERAGE   this set measured {len(measured):,} of "
              f"{len(length_km):,} network lanes ({meas_km:,.1f} of "
              f"{net_km:,.1f} lane-km, {100 * meas_km / net_km:.1f}%) — "
              f"every figure below is over the MEASURED subset, not the "
              f"network. Unmeasured lanes are absent, not empty. Corridor "
              f"and district lane-km are narrowed to match.")
    if zero_len:
        print(f"  WARNING    {len(zero_len):,} measured lanes have "
              f"non-positive length in the network file and are excluded "
              f"from density and speed distributions")

    # ---- 2. totals ------------------------------------------------------
    # TWO POPULATIONS, labelled as two. Travel and Edie speed are summed over
    # the retained post-warmup interval cells — the window printed above.
    # Trips, delay and strands come from the kernel's run-total block, which
    # covers the WHOLE horizon: a trip is counted where it ended, and the
    # ledger has no notion of the window this report happens to be reading.
    # With --warmup-tick the two spans genuinely differ, and a single
    # "TOTALS (window)" heading over both silently combined them. Neither
    # number was wrong; the heading was.
    #
    # Deriving window-scoped trip totals is not available cheaply: it needs a
    # pass over `trips`, which the streaming path deliberately skips (a wrong
    # denominator being worse than an absent one). So the scopes are named
    # rather than reconciled.
    t = head.get("totals") or {}
    same_span = args.warmup_tick <= 0 and lo == 0 and hi >= head["ticks"]
    print(f"\nTOTALS")
    print(f"  network      {net_km:,.1f} lane-km"
          + (f"; {fw_km:,.1f} on named corridors "
             f"({100 * fw_km / net_km:.1f}%)" if fw_km else "")
          + ("" if covers_all else "  [measured subset]"))
    span = "window" if not same_span else "window = whole run"
    print(f"  travel       {tot_d / 1000:,.0f} veh-km over "
          f"{tot_t / 3600:,.0f} veh-h   [{span}]")
    print(f"  Edie speed   {tot_d / tot_t * 3.6 if tot_t else 0:.1f} km/h "
          f"(distance/time)   [{span}]")
    if t:
        inj = head.get("n_trips")
        done = t.get("completed_trips", 0)
        frac = f" ({100 * done / inj:.1f}% of {inj:,} injected)" if inj else ""
        run_tag = "" if same_span else "   [WHOLE RUN, not the window]"
        print(f"  trips        {done:,} completed{frac}, "
              f"{t.get('active_at_horizon', 0):,} still driving at the "
              f"horizon{run_tag}")
        if t.get("mean_time_loss_s") is not None:
            print(f"  delay        {t['mean_time_loss_s']:.0f} s mean loss per "
                  f"trip; {t.get('total_time_loss_s', 0) / 3600:,.0f} veh-h "
                  f"total{run_tag}")
        # Strands are the gridlock indicator (ADR-0034), not a trip outcome:
        # the kernel removed a vehicle that had been motionless for five
        # minutes at a junction it could not enter. Printed on its own line
        # and never folded into the completion rate — a gridlocked run must
        # not read as a congested one.
        if t.get("stranded_trips"):
            print(f"  GRIDLOCK     {t['stranded_trips']:,} vehicles stranded "
                  f"(removed by the escape after being stopped at a blocked "
                  f"junction); see the serve log for the worst sections")

    # ---- 3/4. distributions --------------------------------------------
    for s in ("network", "corridors", "arterial grid"):
        if s not in kd:
            continue
        # The "network" scope is only the network when the set covers it.
        slab = s if (covers_all or s != "network") else "measured subset"
        print_dist(f"DENSITY  [{slab}]  (share of lane-km-hours, "
                   f"critical = {CRITICAL_K:.0f} veh/km/lane)",
                   kd[s], "% of crit")
        print_dist(f"SPEED    [{slab}]  (share of lane-km-hours and of travel)",
                   vd[s], "km/h")

    # ---- 5. groups ------------------------------------------------------
    group_json = {}
    for title, lmap, other in groupings:
        print(f"\n{title}")
        hdr = (f"  {'name':22s} {'lane-km':>8s} {'km/h':>7s} "
               f"{'veh/km/ln':>10s} {'%crit':>6s} {'%km>=crit':>10s} "
               f"{'veh-h lost':>11s}")
        print(hdr)
        print("  " + "-" * (len(hdr) - 2))
        # Static lane-km per group, from the NETWORK — not from the lanes that
        # happened to report. A group whose quiet half never reported would
        # otherwise look shorter, and denser, than it is.
        #
        # "Never reported" and "was never measured" are different facts,
        # though, and only the first one belongs in the denominator. Under a
        # subset set the unmeasured lanes carry no observation at all, so
        # including their length divides a group's traffic by road nobody
        # watched and reports it as less dense than it is. Restricted to the
        # measured lanes in that case; identical to the whole network
        # whenever the set covers it, which is the ordinary case.
        gkm = collections.Counter()
        for lid, km in length_km.items():
            if not covers_all and lid not in measured:
                continue
            gkm[lmap.get(lid) or other] += km
        rows = {}
        for lab, c in sorted(agg[title].items(), key=lambda x: -x[1][3]):
            sp = c[0] / c[1] * 3.6 if c[1] else 0.0
            k = c[1] / 3600.0 / c[2] if c[2] else 0.0
            pct_over = 100 * c[4] / c[2] if c[2] else 0.0
            rows[lab] = dict(lane_km=gkm[lab], kmh=sp, k=k,
                             pct_lane_km_over_critical=pct_over,
                             veh_h_lost=c[3] / 3600.0)
            print(f"  {lab:22s} {gkm[lab]:8.1f} {sp:7.1f} {k:10.1f} "
                  f"{100 * k / CRITICAL_K:5.0f}% {pct_over:9.1f}% "
                  f"{c[3] / 3600:11,.0f}")
        group_json[title.lower()] = rows

    # ---- 6. curve -------------------------------------------------------
    curve = []
    if not args.no_curve:
        print(f"\nCURVE  (per interval — separates fill from peak from drain)")
        hdr = (f"  {'min':>13s} {'all km/h':>9s} {'all k':>7s} "
               f"{'fwy km/h':>9s} {'fwy k':>7s} {'%km>=crit':>10s} "
               f"{'%fwy>=crit':>11s}")
        print(hdr)
        print("  " + "-" * (len(hdr) - 2))
        for (b, e), p in sorted(per_iv.items()):
            dur_s = (e - b) * dt
            # FIXED denominators: the measured lane-km, not the occupied
            # part, so successive rows are comparable. Measured, not
            # network-wide: on a subset set the numerator only covers the
            # lanes in the set, and dividing it by the whole network's
            # lane-km reports a density diluted by road nobody watched.
            # Equal to net_km whenever the set covers the network.
            k = (p[1] / dur_s) / scope_km if scope_km and dur_s else 0.0
            fk = (p[3] / dur_s) / fw_km if fw_km and dur_s else 0.0
            row = dict(begin_min=b * dt / 60, end_min=e * dt / 60,
                       speed=p[0] / p[1] * 3.6 if p[1] else 0.0, k=k,
                       fw_speed=p[2] / p[3] * 3.6 if p[3] else 0.0, fw_k=fk,
                       pct_over_critical=100 * p[4] / scope_km if scope_km else 0.0,
                       pct_fwy_over_critical=100 * p[5] / fw_km if fw_km else 0.0)
            curve.append(row)
            print(f"  {row['begin_min']:5.0f}-{row['end_min']:<7.0f} "
                  f"{row['speed']:9.1f} {k:7.1f} {row['fw_speed']:9.1f} "
                  f"{fk:7.1f} {row['pct_over_critical']:9.2f}% "
                  f"{row['pct_fwy_over_critical']:10.2f}%")

    # ---- 7. hotspots ----------------------------------------------------
    hot = sorted(lane_agg.items(), key=lambda kv: -kv[1][2])[:args.top]
    print(f"\nHOTSPOTS  (lanes by total delay — this is *where* congestion is)")
    hdr = (f"  {'lane':24s} {'corridor':13s} {'district':12s} {'km/h':>6s} "
           f"{'veh-h lost':>11s} {'x,y':>14s}")
    print(hdr)
    print("  " + "-" * (len(hdr) - 2))
    hotspots = []
    for lid, a in hot:
        sp = a[0] / a[1] * 3.6 if a[1] else 0.0
        c = lane_centre(L[lid]) if lid in L else None
        hotspots.append(dict(lane=lid, corridor=lane2c.get(lid),
                             district=lane2z.get(lid), speed=sp,
                             veh_h_lost=a[2] / 3600.0,
                             x=c[0] if c else None, y=c[1] if c else None))
        print(f"  {lid[:24]:24s} {str(lane2c.get(lid, '-'))[:13]:13s} "
              f"{str(lane2z.get(lid, '-'))[:12]:12s} {sp:6.1f} "
              f"{a[2] / 3600:11,.1f} "
              f"{(f'{c[0]:.0f},{c[1]:.0f}' if c else '-'):>14s}")

    if args.json_out:
        # schema_version because this file is a UI contract, not a scratch
        # dump: the viz stats panel binds to these key names.
        out = dict(
            schema_version=1, metrics=args.metrics, ticks=head["ticks"], dt=dt,
            # The constant the density bands were cut against. A consumer
            # cannot render "% of critical" correctly without it, and it must
            # not have to hard-code a copy that silently diverges.
            critical_k=CRITICAL_K,
            window=dict(begin_tick=lo, end_tick=hi, minutes=win_min,
                        dropped_partial=dropped, skipped_warmup=skipped),
            # Whether the distributions describe the network or a subset of
            # it. A consumer that renders "network density" from a subset
            # report is drawing a different claim than the data supports, and
            # this is the only thing in the file that says which it is.
            # `network_lanes` doubles as a weak network identity check: a
            # report rendered against a different network than it was
            # computed on projects hotspot coordinates to the wrong places.
            coverage=dict(network=args.network, network_lanes=len(length_km),
                          measured_lanes=len(measured),
                          network_lane_km=net_km, measured_lane_km=meas_km,
                          covers_network=covers_all,
                          zero_length_lanes=len(zero_len)),
            # Which span each `totals` field covers. The travel figures are
            # summed over the retained post-warmup cells; the trip and delay
            # figures come from the kernel's run-total block and cover the
            # whole horizon regardless of the window. A consumer that renders
            # them under one heading is combining two populations, so the
            # split is stated rather than left to be inferred.
            totals_scope=dict(
                window=["veh_km", "veh_h", "edie_kmh"],
                run=["completed_trips", "active_at_horizon", "injected_trips",
                     "mean_time_loss_s", "total_time_loss_s"],
                static=["lane_km", "corridor_lane_km"],
                window_is_whole_run=same_span),
            totals=dict(lane_km=net_km, corridor_lane_km=fw_km,
                        veh_km=tot_d / 1000, veh_h=tot_t / 3600,
                        edie_kmh=tot_d / tot_t * 3.6 if tot_t else 0.0,
                        completed_trips=t.get("completed_trips"),
                        active_at_horizon=t.get("active_at_horizon"),
                        # Delay was printed but never serialized, so the panel
                        # had to re-derive it by summing a grouping. injected
                        # is None on a streamed file: counting `trips` there
                        # means a second full pass for a denominator, and a
                        # wrong denominator is worse than an absent one.
                        injected_trips=head.get("n_trips"),
                        mean_time_loss_s=t.get("mean_time_loss_s"),
                        total_time_loss_s=t.get("total_time_loss_s")),
            density={s: d.as_json() for s, d in kd.items()},
            speed={s: d.as_json() for s, d in vd.items()},
            groups=group_json, curve=curve, hotspots=hotspots)
        with open(args.json_out, "w") as f:
            json.dump(out, f, indent=1)
        print(f"\nwrote {args.json_out}")


if __name__ == "__main__":
    main()
