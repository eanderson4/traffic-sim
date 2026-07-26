#!/usr/bin/env python3
"""Paired A/B harness for scenario variants — "does this upgrade actually help?"

Runs every variant against every seed, then reports each variant against the
baseline as a PAIRED difference (same seed both arms, so the seed's own
randomness cancels) with a paired t-test and a Cohen's d effect size.

    whatif.py --pod data/scenarios/chi-pod --baseline base --seeds 6 \\
        --ticks 18000 --warmup 6000 --corridors data/networks/x/corridors.json

WHY PAIRED, AND WHY MORE THAN ONE SEED
--------------------------------------------------------------------------
A single run per arm cannot distinguish an upgrade from the seed. Demand is
a Poisson process; two seeds of the SAME scenario differ in mean speed by
several percent on chi-loop-urban, which is the same order as a real but
modest improvement. Reporting one run per option is how you end up telling
someone a no-op is a winner.

Pairing on the seed removes the between-seed variance from the comparison,
which is exactly the variance that would otherwise swamp the effect. The
t-test is then over the per-seed DIFFERENCES.

WHY WARMUP
--------------------------------------------------------------------------
A run starts from an empty network and spends its first minutes filling.
Measured over the fill, every variant looks similar because the network is
uncongested for most of the window. --warmup drops every interval and trip
that begins before the cutoff, so the comparison is over loaded conditions.

Pure stdlib.
"""
import argparse
import collections
import concurrent.futures
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile
import time


# ---------------------------------------------------------------- statistics

def mean(xs):
    return sum(xs) / len(xs) if xs else float("nan")


def stdev(xs):
    """Sample standard deviation (n-1). NaN below two samples."""
    n = len(xs)
    if n < 2:
        return float("nan")
    m = mean(xs)
    return math.sqrt(sum((x - m) ** 2 for x in xs) / (n - 1))


def student_sf(t, df):
    """Two-sided survival function for Student's t.

    Regularized incomplete beta via its continued fraction — the same
    identity scipy.stats.t.sf uses, written out because this repo does not
    take a numeric dependency for one p-value.
    """
    if df <= 0 or math.isnan(t):
        return float("nan")
    x = df / (df + t * t)
    return _betainc(df / 2.0, 0.5, x)


def _betainc(a, b, x):
    """Regularized incomplete beta I_x(a,b), Lentz continued fraction."""
    if x <= 0:
        return 0.0
    if x >= 1:
        return 1.0
    lbeta = (math.lgamma(a + b) - math.lgamma(a) - math.lgamma(b)
             + a * math.log(x) + b * math.log(1 - x))
    # The fraction converges fast only on the near side of the mean; flip
    # when it does not and use the symmetry I_x(a,b) = 1 - I_{1-x}(b,a).
    if x < (a + 1) / (a + b + 2):
        return math.exp(lbeta) * _betacf(a, b, x) / a
    return 1.0 - math.exp(lbeta) * _betacf(b, a, 1 - x) / b


def _betacf(a, b, x, itmax=200, eps=3e-16):
    tiny = 1e-300
    qab, qap, qam = a + b, a + 1.0, a - 1.0
    c, d = 1.0, 1.0 - qab * x / qap
    if abs(d) < tiny:
        d = tiny
    d = 1.0 / d
    h = d
    for m in range(1, itmax + 1):
        m2 = 2 * m
        aa = m * (b - m) * x / ((qam + m2) * (a + m2))
        d = 1.0 + aa * d
        if abs(d) < tiny:
            d = tiny
        c = 1.0 + aa / c
        if abs(c) < tiny:
            c = tiny
        d = 1.0 / d
        h *= d * c
        aa = -(a + m) * (qab + m) * x / ((a + m2) * (qap + m2))
        d = 1.0 + aa * d
        if abs(d) < tiny:
            d = tiny
        c = 1.0 + aa / c
        if abs(c) < tiny:
            c = tiny
        d = 1.0 / d
        delta = d * c
        h *= delta
        if abs(delta - 1.0) < eps:
            break
    return h


def paired_t(diffs):
    """Paired t-test on pre-differenced samples -> (t, p, cohen_d)."""
    n = len(diffs)
    if n < 2:
        return float("nan"), float("nan"), float("nan")
    m, s = mean(diffs), stdev(diffs)
    if s == 0:
        # Identical in every pair: a real (if suspicious) zero-variance
        # result. p=0 when the shift is nonzero, p=1 when nothing moved.
        return float("inf") if m else 0.0, 0.0 if m else 1.0, float("inf") if m else 0.0
    t = m / (s / math.sqrt(n))
    return t, student_sf(t, n - 1), m / s


# ADDED_LANE_SUFFIX must match mknetvariant.py's --add-lane naming.
ADDED_LANE_SUFFIX = "_w1"


# ------------------------------------------------------------------ metrics

def load_metrics(path, warmup, corridors=None):
    """Reduce one run's metrics JSON to the comparison scalars.

    Intervals are Edie's definitions per lane per interval, so network speed
    is sum(distance)/sum(time) over the window — NOT a mean of per-lane
    speeds, which would weight an empty side street the same as the Kennedy.
    """
    with open(path) as f:
        m = json.load(f)

    dist = time_s = 0.0
    cdist = collections.Counter()
    ctime = collections.Counter()
    for iv in m.get("intervals", ()):
        if iv["begin_tick"] < warmup:
            continue
        d, t = iv["sum_dist_m"], iv["sum_time_s"]
        dist += d
        time_s += t
        if corridors:
            lid = iv["lane_id"]
            key = corridors.get(lid)
            if key is None and lid.endswith(ADDED_LANE_SUFFIX):
                # A lane added by mknetvariant --add-lane is not in the base
                # network's corridor lookup, so it would be dropped from the
                # corridor average — silently excluding exactly the capacity
                # the variant added, and biasing the widened arm against
                # itself. Attribute the clone to its donor's corridor.
                key = corridors.get(lid[:-len(ADDED_LANE_SUFFIX)])
            if key:
                cdist[key] += d
                ctime[key] += t

    losses, completed, trip_times = [], 0, []
    for tr in m.get("trips", ()):
        if tr["entry_tick"] < warmup:
            continue
        if tr.get("completed"):
            completed += 1
            losses.append(tr["time_loss_s"])
            trip_times.append((tr["exit_tick"] - tr["entry_tick"]) * m["dt"])

    out = {
        "speed_kmh": (dist / time_s * 3.6) if time_s else float("nan"),
        "completed": completed,
        "mean_time_loss_s": mean(losses) if losses else float("nan"),
        "mean_trip_s": mean(trip_times) if trip_times else float("nan"),
        "vmt_km": dist / 1000.0,
        "active_at_horizon": m["totals"]["active_at_horizon"],
    }
    dem = m["totals"].get("demand")
    out["delivered_frac"] = dem["delivered_frac"] if dem else float("nan")
    for key in cdist:
        out[f"corridor:{key}"] = cdist[key] / ctime[key] * 3.6 if ctime[key] else float("nan")
    return out


# --------------------------------------------------------------------- runs

# Transient startup failures, retried on a fresh port. Each run embeds its
# own NATS server, and past a handful of concurrent runs some lose the race
# to bind or to report ready. Retrying matters more than it looks: a dropped
# run silently shrinks n for ONE arm, which biases a paired comparison
# rather than just weakening it.
TRANSIENT = ("nats-server not ready", "address already in use",
             "bind: ", "connection refused")


def run_one(serve_bin, scenario, seed, ticks, port, workdir, capacity, extra,
            attempts=3):
    """One serve invocation -> parsed metrics path. Returns (ok, path, log)."""
    tag = f"{os.path.basename(scenario)}-s{seed}"
    mpath = os.path.join(workdir, f"{tag}.json")
    logp = os.path.join(workdir, f"{tag}.log")
    for attempt in range(attempts):
        p = port + attempt * 997  # a stride well clear of the pod's own block
        cmd = [serve_bin, "-scenario", scenario, "-run", f"wf{p}",
               "-seed", str(seed), "-ticks", str(ticks), "-pace", "0",
               "-capacity", str(capacity), "-intent-log=false",
               "-metrics-out", mpath, "-ws", f"127.0.0.1:{p}"] + extra
        with open(logp, "w") as lf:
            rc = subprocess.call(cmd, stdout=lf, stderr=subprocess.STDOUT)
        if rc == 0 and os.path.exists(mpath):
            return True, mpath, logp
        with open(logp) as lf:
            tail = lf.read()[-4000:]
        if not any(t in tail for t in TRANSIENT) or attempt == attempts - 1:
            return False, mpath, logp
        time.sleep(5 + 5 * attempt)
    return False, mpath, logp


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report",
                    help="re-score a saved --out report on a different "
                         "--metric instead of running anything. Which metric "
                         "is primary changes the answer — on the CBD grid, "
                         "retiming leaves network mean SPEED flat while "
                         "cutting mean trip time 7.5%, because a spatial mean "
                         "speed is dominated by where vehicles are and a trip "
                         "time by how long they wait.")
    ap.add_argument("--pod", required=False,
                    help="directory of variant scenario dirs (one per option)")
    ap.add_argument("--baseline", required=False,
                    help="variant name inside --pod that every other is compared to")
    ap.add_argument("--seeds", type=int, default=6)
    ap.add_argument("--seed-base", type=int, default=1000)
    ap.add_argument("--ticks", type=int, default=18000)
    ap.add_argument("--warmup", type=int, default=0,
                    help="drop intervals/trips beginning before this tick")
    ap.add_argument("--jobs", type=int, default=6)
    ap.add_argument("--port-base", type=int, default=8600)
    ap.add_argument("--capacity", type=int, default=40000)
    ap.add_argument("--serve", default="./serve",
                    help="path to a built cmd/serve binary")
    ap.add_argument("--corridors", default=None)
    ap.add_argument("--exclude", action="append", default=[],
                    help="pod subdirectory that is not an option (e.g. a "
                         "shared ADR-0012 base a variant resolves against)")
    ap.add_argument("--keep", default=None,
                    help="keep run artifacts in this directory instead of a temp dir")
    ap.add_argument("--out", default=None, help="write the result table as JSON")
    ap.add_argument("--metric", default="speed_kmh",
                    help="primary metric for the verdict column")
    ap.add_argument("--higher-is-better", action="store_true", default=True)
    ap.add_argument("--lower-is-better", dest="higher_is_better",
                    action="store_false")
    args = ap.parse_args()

    if args.report:
        with open(args.report) as f:
            saved = json.load(f)
        seeds = saved["seeds"]
        variants = sorted(saved["variants"])
        results = {}
        for v, rec in saved["variants"].items():
            for s, m in rec["per_seed"].items():
                results[(v, int(s))] = m
        args.pod = saved["pod"]
        args.baseline = saved["baseline"]
        args.warmup = saved.get("warmup", 0)
        args.ticks = saved.get("ticks", 0)
        report_pod(results, variants, seeds, args)
        return

    if not args.pod or not args.baseline:
        sys.exit("--pod and --baseline are required unless --report is given")

    corridors = None
    if args.corridors:
        with open(args.corridors) as f:
            cj = json.load(f)
        corridors = cj["lanes"]

    # A pod may have to physically contain a dir that is NOT an option: an
    # ADR-0012 variant names its base by relative path (`base: ../foo`), so
    # the base must sit beside the variants to resolve.
    skip = set(args.exclude)
    variants = sorted(d for d in os.listdir(args.pod)
                      if os.path.isdir(os.path.join(args.pod, d))
                      and d not in skip)
    if args.baseline not in variants:
        sys.exit(f"baseline {args.baseline!r} not among {variants}")
    seeds = [args.seed_base + i for i in range(args.seeds)]

    workdir = args.keep or tempfile.mkdtemp(prefix="whatif-")
    os.makedirs(workdir, exist_ok=True)
    print(f"[whatif] {len(variants)} variants x {len(seeds)} seeds = "
          f"{len(variants) * len(seeds)} runs, {args.jobs} at a time -> {workdir}",
          file=sys.stderr)

    # SEED-MAJOR, not variant-major. A run's result is not purely a function
    # of its seed: the driver is a separate service, so under CPU contention
    # it falls behind differently and the numbers move. Two runs of a
    # byte-identical scenario on the same 8 seeds differ with a per-seed sd of
    # 0.32% on network speed. That is tolerable as noise, but submitting
    # variant-major makes it BIAS: each arm's runs then occupy a different
    # stretch of a multi-hour schedule, so any drift in machine load maps onto
    # arm identity. Blocking by seed puts every arm's run for a given seed in
    # the same stretch, which is what the paired test already assumes.
    jobs, port = [], args.port_base
    for s in seeds:
        for v in variants:
            jobs.append((v, s, port))
            port += 1

    results = {}
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as ex:
        futs = {ex.submit(run_one, args.serve, os.path.join(args.pod, v), s,
                          args.ticks, p, workdir, args.capacity, []): (v, s)
                for v, s, p in jobs}
        for fut in concurrent.futures.as_completed(futs):
            v, s = futs[fut]
            ok, mpath, logp = fut.result()
            if not ok:
                print(f"[whatif] FAILED {v} seed {s} — see {logp}", file=sys.stderr)
                continue
            results[(v, s)] = load_metrics(mpath, args.warmup, corridors)
            print(f"[whatif] done {v} seed {s}: "
                  f"{results[(v, s)]['speed_kmh']:.1f} km/h", file=sys.stderr)

    report_pod(results, variants, seeds, args)
    if not args.keep:
        shutil.rmtree(workdir, ignore_errors=True)


def report_pod(results, variants, seeds, args):
    """Rank and score. Split out so --report can re-score a saved run."""
    keys = sorted({k for r in results.values() for k in r})
    base_by_seed = {s: results[(args.baseline, s)] for s in seeds
                    if (args.baseline, s) in results}
    if not base_by_seed:
        sys.exit("baseline produced no successful runs")

    report = {"pod": args.pod, "baseline": args.baseline, "seeds": seeds,
              "ticks": args.ticks, "warmup": args.warmup, "variants": {}}

    m = args.metric
    print(f"\n=== {args.pod}  (baseline {args.baseline}, {len(seeds)} seeds, "
          f"warmup {args.warmup} ticks)")
    print(f"{'variant':22} {'n':>2} {m:>12} {'Δ vs base':>11} {'Δ%':>7} "
          f"{'p':>8} {'d':>7}  verdict")
    for v in variants:
        pairs = [(results[(v, s)], base_by_seed[s]) for s in seeds
                 if (v, s) in results and s in base_by_seed]
        if not pairs:
            continue
        vals = [a[m] for a, _ in pairs]
        diffs = [a[m] - b[m] for a, b in pairs]
        bmean = mean([b[m] for _, b in pairs])
        t, p, d = paired_t(diffs)
        dm = mean(diffs)
        pct = 100 * dm / bmean if bmean else float("nan")
        if v == args.baseline:
            verdict = "— baseline —"
        elif not (p == p) or p > 0.05:
            verdict = "no-op (n.s.)"
        else:
            good = dm > 0 if args.higher_is_better else dm < 0
            verdict = "UPGRADE" if good else "WORSE"
        print(f"{v:22} {len(pairs):2d} {mean(vals):12.2f} {dm:+11.2f} "
              f"{pct:+6.1f}% {p:8.4f} {d:+7.2f}  {verdict}")
        report["variants"][v] = {
            "n": len(pairs), "metric": m, "mean": mean(vals),
            "delta": dm, "delta_pct": pct, "p": p, "cohen_d": d,
            "verdict": verdict,
            "per_seed": {str(s): results[(v, s)] for s in seeds
                         if (v, s) in results},
            "all_metrics": {k: mean([results[(v, s)][k] for s in seeds
                                     if (v, s) in results and k in results[(v, s)]])
                            for k in keys},
        }

    # Secondary metrics, unranked — an option that raises speed by shutting
    # traffic out is not an upgrade, and throughput is where that shows.
    print(f"\n--- supporting metrics (means over seeds)")
    sec = [k for k in ("completed", "mean_time_loss_s", "mean_trip_s",
                       "active_at_horizon", "delivered_frac") if k in keys]
    corr = [k for k in keys if k.startswith("corridor:")]
    hdr = sec + corr
    print(f"{'variant':22} " + " ".join(f"{h.replace('corridor:', ''):>16.16}"
                                        for h in hdr))
    for v in variants:
        if v not in report["variants"]:
            continue
        am = report["variants"][v]["all_metrics"]
        print(f"{v:22} " + " ".join(f"{am.get(h, float('nan')):16.2f}"
                                    for h in hdr))

    if args.out:
        with open(args.out, "w") as f:
            json.dump(report, f, indent=2)
        print(f"\n[whatif] wrote {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
