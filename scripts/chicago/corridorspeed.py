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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("metrics")
    ap.add_argument("--corridors", required=True)
    ap.add_argument("--warmup-tick", type=int, default=0,
                    help="ignore intervals beginning before this tick")
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
    for iv in m.get("intervals", []):
        if iv.get("begin_tick", 0) < args.warmup_tick:
            continue
        d = iv.get("sum_dist_m", 0.0)
        t = iv.get("sum_time_s", 0.0)
        net_d += d
        net_t += t
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
        print(f"{'NETWORK (all lanes)':<42} {3.6 * net_d / net_t:7.1f} {net_d/1000:10.0f}")


if __name__ == "__main__":
    main()
