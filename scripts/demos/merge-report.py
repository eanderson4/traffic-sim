#!/usr/bin/env python3
"""Read a merge-scenario metrics JSON and answer the merge questions.

Companion to scripts/demos/merge-pod.py. Three reports, all from one
`serve -metrics-out` file — nothing here re-runs a simulation:

  --wave      the space-time diagram: mainline cell x 60 s interval, mean
              speed by Edie's definition. A backward-propagating shockwave
              is a diagonal band of low speeds running UP-LEFT.
  --lanes     per-lane vehicle-distance share over the measurement window.
              This is the trap-4 check: a new lane that carries ~nothing is
              a router finding, not a traffic finding.
  --summary   the numbers a traffic engineer asks for: bottleneck throughput
              past the merge (veh/h), queue extent, network mean speed.

    python3 scripts/demos/merge-report.py run.json --wave --warmup 3000
"""
import argparse
import json
import re


def cells(metrics):
    """Mainline cell ids in geographic order, upstream first."""
    ids = {r["lane_id"] for r in metrics["intervals"]}
    ups = sorted({lid.rsplit("_", 1)[0] for lid in ids
                  if re.fullmatch(r"up\d+_\w+", lid)})
    tail = [s for s in ("mrg", "aux", "dn0", "dn1")
            if any(lid.startswith(s + "_") for lid in ids)]
    return ups + tail


def edie(rows):
    d = sum(r["sum_dist_m"] for r in rows)
    t = sum(r["sum_time_s"] for r in rows)
    return (d, t, d / t * 3.6 if t else float("nan"))


def wave(m, args):
    """Space-time diagram of the mainline, one column per 250 m cell."""
    secs = cells(m)
    grid = {}
    for r in m["intervals"]:
        lid = r["lane_id"]
        sec, idx = lid.rsplit("_", 1)
        if sec not in secs:
            continue
        if args.lane is not None and idx != args.lane:
            continue
        grid.setdefault((r["begin_tick"], sec), []).append(r)
    ticks = sorted({k[0] for k in grid})
    lane_note = "all lanes" if args.lane is None else f"lane index {args.lane}"
    print(f"# space-time mean speed (km/h, Edie), {lane_note}; "
          f"'.' = no vehicle-time in the cell")
    print(f"{'tick':>6} {'sim s':>6}  " + " ".join(f"{s[-4:]:>4}" for s in secs))
    for t in ticks:
        row = []
        for s in secs:
            rows = grid.get((t, s))
            row.append(f"{edie(rows)[2]:4.0f}" if rows and edie(rows)[1] else "   .")
        print(f"{t:6d} {t * m['dt']:6.0f}  " + " ".join(row))


def lane_shares(m, args):
    """Per-lane vehicle-distance share (trap 4: is the new lane used?)."""
    per = {}
    for r in m["intervals"]:
        if r["begin_tick"] < args.warmup:
            continue
        a = per.setdefault(r["lane_id"], [0.0, 0.0])
        a[0] += r["sum_dist_m"]
        a[1] += r["sum_time_s"]
    total = sum(v[0] for v in per.values())
    print(f"# vehicle-distance share by lane, from tick {args.warmup} "
          f"(total {total/1000:,.0f} veh-km)")
    print(f"{'lane':12} {'veh-km':>10} {'share':>7} {'km/h':>7}")
    for lid in sorted(per):
        d, t = per[lid]
        print(f"{lid:12} {d/1000:10.1f} {100*d/total:6.2f}% "
              f"{d/t*3.6 if t else float('nan'):7.1f}")


def summary(m, args):
    per_iv = [r for r in m["intervals"] if r["begin_tick"] >= args.warmup]
    d, t, net = edie(per_iv)
    secs = cells(m)
    print(f"network mean speed (Edie)      {net:8.2f} km/h")
    print(f"vehicle-distance in window     {d/1000:8.1f} veh-km")
    print(f"active at horizon              {m['totals']['active_at_horizon']:8d}")
    dem = m["totals"].get("demand") or {}
    if dem:
        print(f"demand delivered               {100*dem['delivered_frac']:8.2f}% "
              f"(injected {dem['injected']}, expired {dem['expired']})")

    # Bottleneck throughput: flow past the FIRST cell downstream of the
    # merge, summed over its lanes. q is Edie's flow for the cell, so the
    # sum over lanes is the section's vehicles/hour.
    down = "aux" if "aux" in secs else "dn0"
    ivs = [r for r in per_iv if r["lane_id"].startswith(down + "_")]
    nper = len({r["begin_tick"] for r in ivs})
    q = sum(r["q"] or 0 for r in ivs) / max(nper, 1) * 3600
    print(f"bottleneck throughput ({down})   {q:8.0f} veh/h "
          f"past the merge, {len({r['lane_id'] for r in ivs})} lanes")

    # Queue extent: contiguous run of mainline cells, upstream from the
    # merge, whose window-mean speed sits below --queue-kmh.
    ups = [s for s in secs if s.startswith("up")]
    speed = {}
    for s in ups:
        speed[s] = edie([r for r in per_iv if r["lane_id"].startswith(s + "_")])[2]
    n = 0
    for s in reversed(ups):
        if speed[s] < args.queue_kmh:
            n += 1
        else:
            break
    print(f"queue extent (< {args.queue_kmh:.0f} km/h mean) {n*250:8d} m upstream "
          f"of the merge, of {len(ups)*250} m available")
    trips = [tr for tr in m.get("trips", ())
             if tr["entry_tick"] >= args.warmup and tr.get("completed")]
    if trips:
        mean_loss = sum(tr["time_loss_s"] for tr in trips) / len(trips)
        print(f"completed trips in window      {len(trips):8d}")
        print(f"mean time loss                 {mean_loss:8.1f} s")


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("metrics")
    ap.add_argument("--warmup", type=int, default=3000)
    ap.add_argument("--wave", action="store_true")
    ap.add_argument("--lanes", action="store_true")
    ap.add_argument("--summary", action="store_true")
    ap.add_argument("--lane", default=None,
                    help="restrict --wave to one lane suffix: 0 = right "
                         "mainline, 1 = left, 2 = added lane, a = accel")
    ap.add_argument("--queue-kmh", type=float, default=60.0)
    args = ap.parse_args()
    with open(args.metrics) as f:
        m = json.load(f)
    if not (args.wave or args.lanes or args.summary):
        args.summary = True
    if args.summary:
        summary(m, args)
    if args.wave:
        wave(m, args)
    if args.lanes:
        lane_shares(m, args)


if __name__ == "__main__":
    main()
