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

def load_metrics(path, warmup, corridors=None, set_id=None):
    """Reduce one run's metrics JSON to the comparison scalars.

    Intervals are Edie's definitions per lane per interval, so network speed
    is sum(distance)/sum(time) over the window — NOT a mean of per-lane
    speeds, which would weight an empty side street the same as the Kennedy.

    PARTIAL INTERVALS ARE DROPPED (ADR-0014 §3: comparison tooling drops
    partials). A partial is the final window, cut short by the horizon: it
    is emitted flagged rather than suppressed so tooling can decide, and the
    decision of record is to exclude it. That makes the INTERVAL metrics
    (speed, vmt, corridors) cover a window ending at the last complete
    boundary, which can be well short of the horizon — the shipped Chicago
    run's last interval is [10000,12000) of a 12,000-tick run, so its speed
    window ends at 10,000. The window actually used is returned so the
    report states it instead of implying the horizon.

    TRIP MEANS STAY COMPLETED-ONLY, and that is the opposite of the naive
    survivorship reading. A censored trip's time-loss is TRUNCATED at the
    horizon, so folding it into the mean drags the mean DOWN — and the size
    of that pull scales with active_at_horizon, which is precisely the
    quantity that differs between arms. The arm that jams worst gets the
    biggest downward bias on the metric that is supposed to show it jammed.
    Same reasoning as engine/cmd/metview/main.go's tripBucket, and the
    decision of record: docs/kb/articles/podcast-demo-workqueue.md (item g)
    pins completed-only as v1 semantics, cemented by main_test.go, and says
    changing it needs an ADR-0014 interpretation — with the likely answer
    being a censored-inclusive BOUND alongside, not a redefinition.

    So that is what this does. `mean_time_loss_s` is unchanged. Alongside it,
    `time_loss_bound_s` averages every entered trip's accumulated loss —
    a strict LOWER bound on what the full journeys would have shown, since
    each censored term is truncated — and `censored_frac` states how much of
    the population is censored, which ADR-0014 §2 requires of any
    distribution statistic over trip records. A bound plus its censoring
    share says what the mean alone cannot: whether the comparison is between
    journeys or between jams.

    ONE MEASUREMENT SET, for the same reason mkcongestionmap.py and
    corridorspeed.py refuse otherwise: ADR-0014 permits overlapping sets over
    the same lanes, and summing them counts the same vehicle-time twice.
    """
    with open(path) as f:
        m = json.load(f)

    intervals = m.get("intervals", ())
    sets = {iv.get("set_id") for iv in intervals}
    if set_id is not None:
        if set_id not in sets:
            sys.exit(f"whatif: no measurement set {set_id!r} in {path} "
                     f"(has {sorted(x for x in sets if x)})")
        intervals = [iv for iv in intervals if iv.get("set_id") == set_id]
    elif len(sets) > 1:
        # len(sets), not len(named): an unset id is still a distinct set for
        # double-counting purposes. --set is the way out, matching
        # mkcongestionmap.py and corridorspeed.py rather than making the A/B
        # harness the one tool that cannot read a legal file.
        sys.exit(f"whatif: {path} carries {len(sets)} measurement sets "
                 f"({sorted(x for x in sets if x)}) — summing them "
                 f"double-counts vehicle-time in speed_kmh, vmt_km and every "
                 f"corridor metric. Pass --set to pick one.")

    dist = time_s = 0.0
    iv_begin = iv_end = None
    cdist = collections.Counter()
    ctime = collections.Counter()
    for iv in intervals:
        if iv["begin_tick"] < warmup:
            continue
        if iv.get("partial"):
            continue
        # The window is what was RETAINED, at both edges. Reporting the
        # requested warmup as the start overstates precision the aggregate
        # does not have: the cut lands on an interval boundary, so a warmup
        # falling mid-interval drops that whole interval and the real start
        # is later than the request.
        bt = iv["begin_tick"]
        if iv_begin is None or bt < iv_begin:
            iv_begin = bt
        et = iv.get("end_tick")
        if et is not None and (iv_end is None or et > iv_end):
            iv_end = et
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

    losses, completed, censored, trip_times = [], 0, 0, []
    all_losses = []
    for tr in m.get("trips", ()):
        if tr["entry_tick"] < warmup:
            continue
        # Every entered trip feeds the BOUND; only completed ones feed the
        # mean. See the docstring: censored losses are truncated, so mixing
        # them into the mean biases it low in proportion to how jammed the
        # arm is.
        all_losses.append(tr["time_loss_s"])
        if tr.get("completed"):
            completed += 1
            losses.append(tr["time_loss_s"])
            trip_times.append((tr["exit_tick"] - tr["entry_tick"]) * m["dt"])
        else:
            censored += 1

    out = {
        "speed_kmh": (dist / time_s * 3.6) if time_s else float("nan"),
        "completed": completed,
        "mean_time_loss_s": mean(losses) if losses else float("nan"),
        # Censored-inclusive LOWER bound: every entered trip's accumulated
        # loss, with the unfinished ones truncated at the horizon. Read it
        # against mean_time_loss_s — a large gap means the completed-only
        # mean is describing the traffic that got out.
        "time_loss_bound_s": mean(all_losses) if all_losses else float("nan"),
        # Share of entered trips still in-network at the horizon. ADR-0014 §2
        # requires distribution statistics over trip records to state their
        # censored fraction: at 90% censoring the completed-only mean is a
        # statement about the lucky, not about journeys.
        "censored_frac": (censored / len(all_losses)) if all_losses
                         else float("nan"),
        "mean_trip_s": mean(trip_times) if trip_times else float("nan"),
        "vmt_km": dist / 1000.0,
        "active_at_horizon": m["totals"]["active_at_horizon"],
        # The window the INTERVAL metrics above actually cover — retained
        # boundaries, not the requested ones. Carried per run rather than
        # assumed from --ticks: the horizon is what was asked for, this is
        # what was measured.
        "interval_window": [iv_begin, iv_end],
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


# The fidelity failures that make a run's numbers meaningless rather than
# merely noisy. Every one of these is something `serve` already prints and
# `record-hero.sh` already refuses to bake over — but until 2026-07-27 the A/B
# harness read only the exit code, so a run serve had itself declared void
# could be scored, ranked and shipped in a report. All three read as
# CONGESTION if you only look at the speed:
#
#   * demand that never entered — the cars that could not spawn are exactly
#     the ones that would have queued, so under-delivery makes the network
#     look FASTER the harder you push it;
#   * uncontrolled coasting — no car-following term, so vehicles hold speed
#     into stopped traffic and the overlaps book as collisions;
#   * a controller blind past the hold-last bridge — the frame IS its input,
#     so its whole claimed fleet drove itself.
#
# Substrings, not regexes, and matched against serve's own wording so the
# harness and the bake gate cannot drift apart.
FIDELITY_FAIL = (
    ("of demand never entered the network",
     "demand delivery below threshold — the run did not simulate its scenario"),
    ("the driver could not keep up",
     "uncontrolled coasting — part of the fleet had no car-following control"),
    ("observation frames FAILED to publish",
     "a controller was blind past the hold-last bridge"),
)


def fidelity_problems(logp):
    """Fidelity failures serve reported in this run's log (empty = clean)."""
    try:
        with open(logp) as lf:
            text = lf.read()
    except OSError:
        return []
    return [why for pat, why in FIDELITY_FAIL if pat in text]


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
    ap.add_argument("--drivers", type=int, default=0,
                    help="serve -drivers N: driver controller replicas. The "
                         "observation frame carries every claimed ego in ONE "
                         "message and stops fitting the 4 MiB broker cap near "
                         "10,200 egos PER CONTROLLER, so a run that peaks "
                         "above that goes blind on one replica and its fleet "
                         "coasts — which reads as congestion and voids the "
                         "run. 0 leaves the flag off (serve's own default). "
                         "Size it so peak vehicles / N stays under ~6,000.")
    ap.add_argument("--serve", default="./serve",
                    help="path to a built cmd/serve binary")
    ap.add_argument("--corridors", default=None)
    ap.add_argument("--set", dest="set_id", default=None,
                    help="measurement set to read when a run emits more than "
                         "one (ADR-0014 permits overlapping sets; summing "
                         "them double-counts vehicle-time)")
    ap.add_argument("--exclude", action="append", default=[],
                    help="pod subdirectory that is not an option (e.g. a "
                         "shared ADR-0012 base a variant resolves against)")
    ap.add_argument("--keep", default=None,
                    help="keep run artifacts in this directory instead of a temp dir")
    ap.add_argument("--allow-void", action="store_true",
                    help="score the batch even if some runs failed a fidelity "
                         "gate (under-delivered demand, uncontrolled coasting, "
                         "a blind controller). Off by default because those "
                         "failures all LOOK like congestion; the voided runs "
                         "are recorded in the report either way.")
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
        # Carry the saved void list through a re-score. Re-scoring changes
        # which METRIC is primary, not which runs happened, so dropping this
        # would let `--report --out` launder a --allow-void batch into one
        # that looks clean.
        args.voided = saved.get("voided", [])
        # FAIL CLOSED on a report written before interval_window existed.
        # Every checked-in report predates it, and they are exactly the
        # measurements this tool now rejects: they summed horizon-partial
        # intervals into a window they reported as the horizon. Skipping the
        # window check for records that lack the field would let --report
        # re-rank precisely those runs, and --out would then write them back
        # carrying an empty window as though it had been verified.
        # The window cannot be recovered from the report — it is a property
        # of the metrics files, which --report does not read.
        missing = sorted({v for v, rec in saved["variants"].items()
                          for m in rec["per_seed"].values()
                          if "interval_window" not in m})
        if missing:
            sys.exit(
                f"whatif: {args.report} predates interval-window tracking "
                f"(arms {missing} carry no interval_window), so its numbers "
                f"include the horizon-partial interval that ADR-0014 §3 says "
                f"comparison tooling drops — the defect that makes them "
                f"unpublishable. Re-run the pod; a re-score cannot recover "
                f"the window, because the metrics files are not read here.")
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
    voided = []
    # Every arm gets the SAME controller configuration. Driver count changes
    # how much of the fleet stays under control, so varying it between arms
    # would put a fidelity difference inside the measured difference.
    serve_extra = ["-drivers", str(args.drivers)] if args.drivers else []
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as ex:
        futs = {ex.submit(run_one, args.serve, os.path.join(args.pod, v), s,
                          args.ticks, p, workdir, args.capacity,
                          serve_extra): (v, s)
                for v, s, p in jobs}
        for fut in concurrent.futures.as_completed(futs):
            v, s = futs[fut]
            ok, mpath, logp = fut.result()
            if not ok:
                # An execution failure unbalances the paired comparison in
                # exactly the way a fidelity void does — the seed survives
                # for the other arms and vanishes for this one — so it goes
                # in the same bucket. Skipping it while voiding the other
                # kind would enforce the rule against the milder failure and
                # wave through the worse one.
                print(f"[whatif] VOID {v} seed {s}: run FAILED (nonzero exit "
                      f"or no metrics) — see {logp}", file=sys.stderr)
                voided.append((v, s))
                continue
            # A zero exit is necessary, not sufficient: serve exits 0 on runs
            # it has itself reported as unfidelitous. Scoring one of those
            # would rank a simulation the engine says did not happen.
            bad = fidelity_problems(logp)
            if bad:
                for why in bad:
                    print(f"[whatif] VOID {v} seed {s}: {why} — see {logp}",
                          file=sys.stderr)
                voided.append((v, s))
                continue
            results[(v, s)] = load_metrics(mpath, args.warmup, corridors,
                                           args.set_id)
            print(f"[whatif] done {v} seed {s}: "
                  f"{results[(v, s)]['speed_kmh']:.1f} km/h", file=sys.stderr)

    # Refuse to write a report rather than write a quietly-thinner one. A
    # voided run does not merely weaken the comparison, it UNBALANCES it: the
    # paired test drops that seed for one arm only, and the arms most likely
    # to void are exactly the ones that congest hardest — the effect under
    # test. --allow-void exists for deliberate exploration, and says so in
    # the report so a reader cannot mistake it for a clean batch.
    if voided and not args.allow_void:
        # The workdir is deliberately NOT cleaned here even without --keep:
        # the per-run logs are the only record of WHY each run voided, and
        # the first thing anyone does on seeing this message is open one.
        print(f"\n[whatif] REFUSING TO REPORT: {len(voided)} of {len(jobs)} runs "
              f"were voided on fidelity grounds (listed above). The arms that "
              f"void are the ones that congest, so dropping them biases the "
              f"comparison toward 'no effect'. Fix the run (usually: fewer "
              f"--jobs, more -drivers, or less demand) or pass --allow-void.\n"
              f"[whatif] run logs kept for diagnosis: {workdir}",
              file=sys.stderr)
        sys.exit(2)
    args.voided = [f"{v}:s{s}" for v, s in voided]

    report_pod(results, variants, seeds, args)
    if not args.keep:
        shutil.rmtree(workdir, ignore_errors=True)


def report_pod(results, variants, seeds, args):
    """Rank and score. Split out so --report can re-score a saved run."""
    # Numeric metrics only: everything in `keys` gets averaged across seeds
    # and t-tested, and interval_window is a pair of ticks, not a measurement.
    keys = sorted({k for r in results.values() for k, v in r.items()
                   if isinstance(v, (int, float))})
    base_by_seed = {s: results[(args.baseline, s)] for s in seeds
                    if (args.baseline, s) in results}
    if not base_by_seed:
        sys.exit("baseline produced no successful runs")

    # What the interval metrics actually cover, as opposed to the horizon
    # that was requested. Dropping the horizon-partial interval (ADR-0014 §3)
    # ends the speed window at the last complete boundary, so a 12,000-tick
    # run with 3,000-tick intervals reports speed over 4,000-10,000. Every
    # arm should land on the same window — they share seeds, horizon and
    # interval grid — so a disagreement means the arms are not comparable on
    # these metrics, and it is reported rather than averaged away.
    # sorted() with a key that cannot compare None: an arm whose intervals
    # were ALL dropped reports [None, None], and sorting that against an
    # integer pair raises TypeError — a crash in the very path that exists to
    # report incomparability.
    windows = sorted({tuple(r["interval_window"]) for r in results.values()
                      if r.get("interval_window")},
                     key=lambda w: tuple(-1 if x is None else x for x in w))
    if len(windows) > 1:
        # FAIL CLOSED. A warning here would be printed above a report that
        # still t-tests the arms, labels them UPGRADE/WORSE and writes the
        # JSON — and the whole point of the finding is that those numbers do
        # not describe the same window. This is the same rule the fidelity
        # gate follows: refuse to publish rather than publish with a caveat
        # nobody reads.
        sys.exit(f"whatif: arms measured DIFFERENT interval windows "
                 f"{[list(w) for w in windows]} — interval metrics (speed, "
                 f"vmt_km, corridor:*) are not comparable across them, so "
                 f"ranking them would be meaningless. Re-run the arms that "
                 f"disagree on the same horizon and interval grid.")
    if windows and any(x is None for x in windows[0]):
        sys.exit(f"whatif: no complete measurement interval survived the "
                 f"warmup cut (window {list(windows[0])}) — every interval "
                 f"was either before --warmup {args.warmup} or flagged "
                 f"partial. There is nothing to compare.")

    report = {"pod": args.pod, "baseline": args.baseline, "seeds": seeds,
              "ticks": args.ticks, "warmup": args.warmup,
              # A pair [warmup, last complete interval end], or several if
              # the arms disagreed. NOT the horizon — see above.
              "interval_window": [list(w) for w in windows],
              # Empty on a clean batch. Non-empty only reaches here via
              # --allow-void, and travels with the numbers so a reader of the
              # JSON sees which runs were dropped without re-reading the logs.
              "voided": getattr(args, "voided", []), "variants": {}}

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
    # censored_frac and time_loss_bound_s ride WITH mean_time_loss_s, not
    # only in the JSON: a completed-only mean whose population is 40%
    # censored is a different claim from one at 2%, and the printed table is
    # what gets read aloud and pasted into docs.
    sec = [k for k in ("completed", "mean_time_loss_s", "time_loss_bound_s",
                       "censored_frac", "mean_trip_s",
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
