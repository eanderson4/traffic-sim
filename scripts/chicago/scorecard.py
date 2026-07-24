#!/usr/bin/env python3
"""Aggregate an M13 metrics JSON (ADR-0014 §6) into a zone scorecard.

Usage: scorecard.py metrics.json [--top 10]

Prints network totals (VMT, VHT, mean speed, time loss, delay share) and
the top lanes by time loss in the final interval — the queue-location
half of the reasonableness scorecard.
"""
import argparse
import json
import collections


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("metrics")
    ap.add_argument("--top", type=int, default=10)
    args = ap.parse_args()

    with open(args.metrics) as f:
        m = json.load(f)

    dist = time_s = loss = stops = 0.0
    last_interval_by_lane = collections.Counter()
    max_end = 0
    for iv in m["intervals"]:
        dist += iv["sum_dist_m"]
        time_s += iv["sum_time_s"]
        loss += iv["time_loss_s"]
        stops += iv["stops"]
        max_end = max(max_end, iv["end_tick"])
    for iv in m["intervals"]:
        if iv["end_tick"] == max_end:
            last_interval_by_lane[iv["lane_id"]] += iv["time_loss_s"]

    vht_h = time_s / 3600
    mean_kmh = 3.6 * dist / time_s if time_s > 0 else 0
    print(f"ticks={m['ticks']} dt={m['dt']}")
    print(f"VMT={dist/1000:,.0f} km  VHT={vht_h:,.1f} h  mean speed={mean_kmh:.1f} km/h")
    print(f"time loss={loss/3600:,.1f} h  delay share={loss/time_s*100:.1f}%  stops={int(stops):,}")
    print(f"last interval [{max_end-m['ticks']//4},{max_end}) top-{args.top} lanes by time loss:")
    for lane, tl in last_interval_by_lane.most_common(args.top):
        print(f"  {lane:32s} {tl:8.0f} s")


if __name__ == "__main__":
    main()
