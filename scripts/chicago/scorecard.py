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

    # Network totals come from the authoritative totals object (ADR-0014 §3
    # — accumulation happens once in the kernel). Summing interval records
    # would double-count as soon as measurement sets overlap, and the
    # optional groups (stops, time_loss_s) are omitted when off.
    tot = m["totals"]
    dist, time_s, loss = tot["vmt"], tot["vht"], tot["total_time_loss_s"]

    # Per-lane queue locations come from the interval records of ONE set —
    # the one covering the most distinct lanes (record COUNT would favor
    # high-frequency narrow sets); aggregating across sets would
    # double-count.
    lanes_by_set = {}
    sets_with_tl = set()
    for iv in m["intervals"]:
        lanes_by_set.setdefault(iv["set_id"], set()).add(iv["lane_id"])
        if "time_loss_s" in iv:
            sets_with_tl.add(iv["set_id"])
    if not lanes_by_set:
        print("no interval records in this metrics file")
        return
    # The queue ranking needs time_loss_s — a set with that group disabled
    # would silently report zeros, so prefer sets that carry it (ADR-0014
    # permits either optional group to be off).
    pool = sets_with_tl or lanes_by_set.keys()
    set_id = max(pool, key=lambda s: (len(lanes_by_set[s]), s))
    last_interval_by_lane = collections.Counter()
    max_end = 0
    stops = 0
    for iv in m["intervals"]:
        if iv["set_id"] != set_id:
            continue
        max_end = max(max_end, iv["end_tick"])
        stops += iv.get("stops", 0)
    win_begin = 0
    for iv in m["intervals"]:
        if iv["set_id"] == set_id and iv["end_tick"] == max_end:
            last_interval_by_lane[iv["lane_id"]] += iv.get("time_loss_s", 0)
            win_begin = iv["begin_tick"]

    vht_h = time_s / 3600
    mean_kmh = 3.6 * dist / time_s if time_s > 0 else 0
    print(f"ticks={m['ticks']} dt={m['dt']}")
    print(f"VMT={dist/1000:,.0f} km  VHT={vht_h:,.1f} h  mean speed={mean_kmh:.1f} km/h")
    print(f"time loss={loss/3600:,.1f} h  delay share={loss/time_s*100:.1f}%  stops={int(stops):,} (set {set_id})")
    print(f"last interval [{win_begin},{max_end}) top-{args.top} lanes by time loss (set {set_id}):")
    for lane, tl in last_interval_by_lane.most_common(args.top):
        print(f"  {lane:32s} {tl:8.0f} s")


if __name__ == "__main__":
    main()
