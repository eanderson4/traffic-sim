#!/usr/bin/env python3
"""Paired test between two runs of the SAME arm — a null experiment.

    nulltest.py report-a.json report-b.json [--variant base]

A whatif report's own p-values are only as good as the assumption that a
run is a function of its seed. It is not: the driver is a separate service
over NATS, so under CPU contention it falls behind differently and the
numbers move. This measures that residual directly by running the harness's
own paired test on two independent runs of a byte-identical arm.

Everything it reports is a false positive by construction. p < alpha here
is not a result — it is the harness detecting an effect that cannot exist,
and it bounds what the same test can be trusted to detect elsewhere.

Use it to set the practical-significance floor: an effect smaller than the
per-seed sd reported here is not distinguishable from which machine the run
landed on, whatever its p-value says.
"""
import argparse
import json
import os
import statistics
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from whatif import paired_t  # noqa: E402  (the paired test, not a second copy)

METRICS = ("speed_kmh", "vmt_km", "mean_trip_s", "mean_time_loss_s")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("report_a")
    ap.add_argument("report_b")
    ap.add_argument("--variant", default="base",
                    help="arm present in both reports (default: base)")
    ap.add_argument("--alpha", type=float, default=0.05)
    args = ap.parse_args()

    ra, rb = (json.load(open(p)) for p in (args.report_a, args.report_b))
    for r, p in ((ra, args.report_a), (rb, args.report_b)):
        if args.variant not in r["variants"]:
            sys.exit(f"{p}: no arm {args.variant!r}")
    a = ra["variants"][args.variant]["per_seed"]
    b = rb["variants"][args.variant]["per_seed"]
    seeds = [s for s in a if s in b]
    if len(seeds) < 3:
        sys.exit(f"only {len(seeds)} shared seeds — too few to test")
    if ra.get("ticks") != rb.get("ticks") or ra.get("warmup") != rb.get("warmup"):
        print("nulltest: WARNING — the two runs used different ticks/warmup, "
              "so this measures more than run-to-run drift", file=sys.stderr)

    print(f"null experiment: {args.variant!r} vs itself, "
          f"{len(seeds)} shared seeds")
    print(f"  {args.report_a}\n  {args.report_b}\n")
    print(f"{'metric':20}{'mean Δ%':>10}{'p':>10}{'sd of Δ%':>11}   verdict")
    worst = None
    for m in METRICS:
        if m not in a[seeds[0]] or m not in b[seeds[0]]:
            continue
        xs = [a[s][m] for s in seeds]
        rel = [100 * (b[s][m] - a[s][m]) / a[s][m] for s in seeds]
        _, p, _ = paired_t([b[s][m] - a[s][m] for s in seeds])
        sd = statistics.stdev(rel)
        bad = p <= args.alpha
        # A significant result here means the harness "detected" a difference
        # between a scenario and itself. Report it loudly: every downstream
        # p-value on this metric inherits the same false-positive rate.
        print(f"{m:20}{statistics.mean(rel):10.3f}{p:10.4f}{sd:11.3f}   "
              f"{'FALSE POSITIVE' if bad else 'ok'}")
        if m == "speed_kmh":
            worst = sd
        del xs
    if worst is not None:
        print(f"\nPractical floor implied by this run: effects below ~{worst:.2f}% "
              f"on speed_kmh are\nnot separable from run-to-run drift.")


if __name__ == "__main__":
    main()
