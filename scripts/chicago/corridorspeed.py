#!/usr/bin/env python3
"""Report Edie mean speed per NAMED CORRIDOR from a metrics JSON.

    corridorspeed.py metrics.json --corridors corridors.json [--warmup-tick N]

congestion.py already does this and much more, but it resolves lane -> edge ->
OSM way -> street name, so it needs the OSM extract the network was imported
from. When the only question is "did the expressways actually congest", the
corridor map alone answers it: corridors.json already carries the
lane-id -> corridor assignment, so no OSM file is required.

Edie's definition, which is the only defensible way to average a speed over a
region of space-time: sum the distance every vehicle covered inside the
window and divide by the time they spent there. Averaging the per-lane mean
speeds instead would weight a 12 m junction stub the same as 400 m of
mainline.

The warmup cut is not optional and not cosmetic. A run starts from an empty
network and fills; intervals from that period describe a road with nothing on
it, and including them makes every corridor look faster the shorter the run
was. Pass --warmup-tick and quote it next to the number.
"""
import argparse
import json
import sys


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("metrics")
    ap.add_argument("--corridors", required=True)
    ap.add_argument("--warmup-tick", type=int, default=0,
                    help="ignore intervals beginning before this tick")
    ap.add_argument("--set", dest="set_id", default=None,
                    help="measurement set to read when the file has more "
                         "than one (ADR-0014 permits overlapping sets)")
    ap.add_argument("--real", default="",
                    help="optional name=lo-hi[,...] real-world AM peak band "
                         "in km/h, printed as a verdict column")
    args = ap.parse_args()

    with open(args.corridors) as f:
        cmap = json.load(f)
    lane2c = cmap["lanes"]
    labels = cmap.get("labels", {})

    real = {}
    for part in filter(None, args.real.split(",")):
        name, band = part.split("=")
        lo, hi = band.split("-")
        real[name] = (float(lo), float(hi))

    with open(args.metrics) as f:
        m = json.load(f)

    # (distance, time) per corridor, plus a network total over ALL lanes so
    # the corridors can be read against the network they sit in.
    agg = {}
    net_d = net_t = 0.0
    # Which lanes this set actually reported. ADR-0014 permits a set over a
    # SUBSET of the network, and the aggregate below was labelled
    # "NETWORK (all lanes)" regardless — so --set on a corridor-only set
    # presented that corridor's speed and VMT as a whole-network result.
    net_lanes = set()
    intervals = m.get("intervals", [])
    sets = {iv.get("set_id") for iv in intervals}
    if args.set_id is not None:
        if args.set_id not in sets:
            sys.exit(f"corridorspeed: no set {args.set_id!r} in "
                     f"{args.metrics} (has {sorted(x for x in sets if x)})")
        intervals = [iv for iv in intervals if iv.get("set_id") == args.set_id]
    elif len(sets) > 1:
        # ADR-0014 permits overlapping measurement sets over the same lanes
        # with different windows. Summing them counts the same vehicle-time
        # more than once, which silently deflates or inflates a corridor
        # depending on which sets happen to cover it.
        sys.exit(f"corridorspeed: {args.metrics} carries {len(sets)} "
                 f"measurement sets ({sorted(x for x in sets if x)}) — "
                 f"summing them double-counts vehicle-time. Pass --set to "
                 f"pick one.")
    iv_begin = iv_end = None
    for iv in intervals:
        if iv.get("begin_tick", 0) < args.warmup_tick:
            continue
        # ADR-0014 §3: comparison tooling drops partials. The final interval
        # is cut short by the horizon, so including it makes a corridor's
        # speed depend on where the run happened to stop — and it put this
        # table on a different window than the congestion map beside it,
        # which does drop them.
        if iv.get("partial"):
            continue
        # Retained boundaries, not requested ones: the warmup cut lands on an
        # interval boundary, so quoting --warmup-tick as the start claims a
        # precision the aggregate does not have.
        bt = iv.get("begin_tick", 0)
        if iv_begin is None or bt < iv_begin:
            iv_begin = bt
        et = iv.get("end_tick")
        if et is not None and (iv_end is None or et > iv_end):
            iv_end = et
        d = iv.get("sum_dist_m", 0.0)
        t = iv.get("sum_time_s", 0.0)
        net_d += d
        net_t += t
        net_lanes.add(iv.get("lane_id", ""))
        c = lane2c.get(iv.get("lane_id", ""))
        if c is None:
            continue
        a = agg.setdefault(c, [0.0, 0.0])
        a[0] += d
        a[1] += t

    print(f"{'corridor':<42} {'km/h':>7} {'veh-km':>10}   real AM peak")
    rows = sorted(agg.items(), key=lambda kv: kv[1][0] / max(kv[1][1], 1e-9))
    for c, (d, t) in rows:
        kmh = 3.6 * d / t if t > 0 else 0.0
        verdict = ""
        if c in real:
            lo, hi = real[c]
            verdict = (f"{lo:g}-{hi:g} — right" if lo <= kmh <= hi
                       else f"{lo:g}-{hi:g} — TOO FAST" if kmh > hi
                       else f"{lo:g}-{hi:g} — too slow")
        print(f"{labels.get(c, c):<42} {kmh:7.1f} {d/1000:10.0f}   {verdict}")
    if net_t > 0:
        # Name the population honestly. Without a --network to compare against
        # there is no way to know whether this set covered the network, so say
        # what IS known — the lane count the set reported — rather than
        # asserting "all lanes".
        label = (f"MEASURED SET ({len(net_lanes):,} lanes)" if args.set_id is not None
                 else f"ALL MEASURED LANES ({len(net_lanes):,})")
        print(f"{label:<42} {3.6 * net_d / net_t:7.1f} {net_d/1000:10.0f}")
    # State the window rather than letting the reader assume the horizon:
    # dropping the horizon-partial interval ends it at the last complete
    # boundary, which on a 12,000-tick run with 3,000-tick intervals is
    # 10,000.
    if iv_begin is not None and iv_end is not None:
        print(f"\nmeasured over ticks {iv_begin:,}-{iv_end:,} "
              f"(requested warmup {args.warmup_tick:,}; horizon partials "
              f"dropped, ADR-0014 §3)")


if __name__ == "__main__":
    main()
