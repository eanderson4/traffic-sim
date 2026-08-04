#!/usr/bin/env python3
"""Read one Bottleneck Town run and say what happened on the corridor.

    town_report.py --network <net.json> --metrics <run.json> [--warmup 6000]

whatif.py answers "is arm A better than arm B"; this answers "is the
baseline actually a congested corridor, and where". It reports, over the
measurement window only:

  * network mean speed by Edie's definition (sum_dist / sum_time), the same
    definition whatif.py ranks on;
  * per-corridor speed and vehicle-distance, including the new road in the
    arms that have one — the SHARE of network vehicle-distance a new road
    carries is the number that says whether the router used it at all;
  * per-signalized-junction approach queues (mean occupied metres, from the
    time-weighted occupancy) and delay (time loss per vehicle served).

Pure stdlib.
"""
import argparse
import collections
import json


def corridor_of(lane_id, section):
    if section.startswith("j:"):
        return "junction interiors"
    road, tail = section.split("_")
    if road == "main":
        return "Main St eastbound" if tail[0] == "f" else "Main St westbound"
    if road == "byp":
        return "*** bypass-north ***"
    if road == "con":
        return "*** connector-south ***"
    return "cross streets"


def aggregate(net_path, paths, warmup):
    """Corridor vehicle-distance shares pooled over several runs (seeds)."""
    net = json.load(open(net_path))
    lanes = {L["id"]: L for L in net["lanes"]}
    dist = collections.Counter()
    time = collections.Counter()
    for p in paths:
        m = json.load(open(p))
        for iv in m["intervals"]:
            if iv["begin_tick"] < warmup:
                continue
            c = corridor_of(iv["lane_id"], lanes[iv["lane_id"]]["section"])
            dist[c] += iv["sum_dist_m"]
            time[c] += iv["sum_time_s"]
    td = sum(dist.values())
    print(f"pooled over {len(paths)} run(s): network "
          f"{td / sum(time.values()) * 3.6:.2f} km/h, {td / 1000:.0f} veh-km")
    for c in sorted(dist, key=lambda k: -dist[k]):
        print(f"  {c:24} {dist[c] / time[c] * 3.6:7.2f} km/h "
              f"{dist[c] / 1000:9.1f} veh-km {100 * dist[c] / td:6.2f}% of network")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--network", required=True)
    ap.add_argument("--metrics", required=True, nargs="+")
    ap.add_argument("--warmup", type=int, default=6000)
    args = ap.parse_args()
    if len(args.metrics) > 1:
        aggregate(args.network, args.metrics, args.warmup)
        return
    args.metrics = args.metrics[0]

    net = json.load(open(args.network))
    lanes = {L["id"]: L for L in net["lanes"]}
    m = json.load(open(args.metrics))
    dt = m["dt"]

    dist = collections.Counter()
    time = collections.Counter()
    occ_len = collections.Counter()   # occupancy-weighted metres, per lane
    tl = collections.Counter()
    win = collections.Counter()
    for iv in m["intervals"]:
        if iv["begin_tick"] < args.warmup:
            continue
        lid = iv["lane_id"]
        c = corridor_of(lid, lanes[lid]["section"])
        dist[c] += iv["sum_dist_m"]
        time[c] += iv["sum_time_s"]
        span = (iv["end_tick"] - iv["begin_tick"]) * dt
        occ_len[lid] += iv.get("occupancy", 0.0) * lanes[lid]["length"] * span
        tl[lid] += iv.get("time_loss_s", 0.0)
        win[lid] += span

    td, tt = sum(dist.values()), sum(time.values())
    print(f"network mean speed  {td / tt * 3.6:6.2f} km/h   "
          f"vehicle-distance {td / 1000:8.1f} km   vehicle-hours {tt / 3600:6.2f}")
    print(f"\n{'corridor':26} {'km/h':>7} {'veh-km':>9} {'share of veh-km':>16}")
    for c in sorted(dist, key=lambda k: -dist[k]):
        print(f"  {c:24} {dist[c] / time[c] * 3.6:7.2f} {dist[c] / 1000:9.1f} "
              f"{100 * dist[c] / td:15.1f}%")

    # Signalized approaches: the last road lane before each junction.
    approach = collections.defaultdict(list)
    for L in net["lanes"]:
        if not L.get("internal"):
            continue
        j = L.get("junction")
        if not j or not j.startswith("J"):
            continue
        for up in net["lanes"]:
            if L["id"] in up.get("successors", ()):
                approach[(j, up["section"])].append(up["id"])
    print(f"\n{'signalized approach':34} {'queue m':>9} {'delay s/veh':>12} "
          f"{'veh/h':>8}")
    for (j, sec), ids in sorted(approach.items()):
        ids = sorted(set(ids))
        q = sum(occ_len[i] / win[i] for i in ids if win[i])
        loss = sum(tl[i] for i in ids)
        # vehicles served on the approach = distance / lane length, summed
        flow = 0.0
        for i in ids:
            per = [iv for iv in m["intervals"]
                   if iv["lane_id"] == i and iv["begin_tick"] >= args.warmup]
            dd = sum(iv["sum_dist_m"] for iv in per)
            if win[i]:
                flow += dd / lanes[i]["length"] / win[i] * 3600
        n = flow * sum(win[i] for i in ids) / len(ids) / 3600 if ids else 0
        print(f"  {j} <- {sec:27} {q:9.1f} {loss / n if n else 0:12.1f} "
              f"{flow:8.0f}")

    # Main Street by lane index — the widening arm's honesty check: an added
    # lane that carries no vehicle-distance has not widened anything.
    bylane = collections.Counter()
    for iv in m["intervals"]:
        if iv["begin_tick"] < args.warmup:
            continue
        L = lanes[iv["lane_id"]]
        if L["section"].split("_")[0] != "main":
            continue
        bylane[L["edgeIndex"]] += iv["sum_dist_m"]
    mt = sum(bylane.values())
    print("\nMain Street vehicle-distance by lane index (0 = kerbside):")
    for i in sorted(bylane):
        print(f"  lane {i}: {bylane[i] / 1000:8.1f} km  {100 * bylane[i] / mt:5.1f}%")

    tot = m["totals"]
    dem = tot.get("demand")
    print(f"\ntrips: {tot['completed_trips']} completed, "
          f"{tot['active_at_horizon']} active at horizon")
    if dem:
        print(f"demand delivered: {100 * dem['delivered_frac']:.2f}%")


if __name__ == "__main__":
    main()
