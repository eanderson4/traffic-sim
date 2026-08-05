#!/usr/bin/env python3
"""Paired fixed-time vs actuated-signal bracket (ADR-0037, milestone 2).

Runs ONE scenario twice per seed: the fixed-time baseline arm (plain
serve) and the actuated arm (serve -sigctl, the reference gap-out
controller on the signal_set channel), then scores the actuated arm
against the baseline as a paired difference with whatif.py's machinery
(load_metrics, paired_t, the fidelity-gate pattern — run_one is
re-implemented inline because whatif's tags outputs by scenario
basename, identical for both arms of this bracket) — same seed both
arms, so the seed's demand realization cancels. whatif.py's pod model
cannot express this bracket because its arms differ by a serve FLAG,
not by scenario content, and whatif applies identical serve flags to
every arm by design.

    sigctl-bracket.py --scenario data/scenarios/chi-loop-urban-half-base \
        --seeds 4 --ticks 54000 --warmup 6000 --out report.json

Fidelity gates (demand delivery, coasting, blind controller) are
whatif.run_one's — a run that fails them is voided, and any void refuses
the report unless --allow-void, exactly as whatif.
"""
import argparse
import concurrent.futures
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import whatif

ARMS = ("fixed-time", "actuated")


def actuation_evidence(logp):
    """The actuated arm's serve log must show signal verbs APPLIED. A
    clean exit with zero accepted verbs is the silent-no-op failure
    (controller attached late, table never installed, control loop dead,
    every verb rejected) — the run LOOKS like a valid fixed-time run,
    which is exactly what a fidelity gate exists to refuse (whatif.py's
    pattern). Keys on accepted, not verbs: Sent is incremented before
    the publish attempt, so an all-rejected arm would pass a verbs=
    gate (round-3 review). Returns (ok, rejected_count)."""
    try:
        with open(logp) as lf:
            text = lf.read()
    except OSError:
        return False, 0
    m = re.search(r"sigctl: done — verbs=(\d+) accepted=(\d+) rejected=(\d+)", text)
    if m is None:
        return False, 0
    return int(m.group(2)) > 0, int(m.group(3))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--scenario", default="data/scenarios/chi-loop-urban-half-base")
    ap.add_argument("--seeds", type=int, default=4)
    ap.add_argument("--seed-base", type=int, default=1000)
    ap.add_argument("--ticks", type=int, default=54000)
    ap.add_argument("--warmup", type=int, default=6000)
    ap.add_argument("--jobs", type=int, default=4)
    ap.add_argument("--port-base", type=int, default=9600)
    ap.add_argument("--capacity", type=int, default=40000)
    ap.add_argument("--drivers", type=int, default=0)
    ap.add_argument("--serve", default="./serve")
    ap.add_argument("--keep", default=None)
    ap.add_argument("--allow-void", action="store_true")
    ap.add_argument("--out", default=None)
    args = ap.parse_args()

    workdir = args.keep or tempfile.mkdtemp(prefix="sigctl-bracket-")
    os.makedirs(workdir, exist_ok=True)
    seeds = [args.seed_base + i for i in range(args.seeds)]
    print(f"[sigctl-bracket] {len(ARMS)} arms x {len(seeds)} seeds, "
          f"{args.jobs} at a time -> {workdir}", file=sys.stderr)

    # Seed-major, same reasoning as whatif.py: each arm's run for a seed
    # lands in the same wall-clock stretch, so machine load does not map
    # onto arm identity.
    jobs = []
    port = args.port_base
    for s in seeds:
        for arm in ARMS:
            jobs.append((arm, s, port))
            port += 1

    def run(arm, seed, prt):
        # whatif.run_one tags output by scenario basename — identical for
        # both arms here, which raced the two arms onto one metrics path
        # (caught in the smoke: arm assignment of the numbers was
        # completion-order, not truth). Tag per arm.
        extra = ["-sigctl"] if arm == "actuated" else []
        if args.drivers:
            extra = ["-drivers", str(args.drivers)] + extra
        tag = f"sigctl-bracket-{arm}-s{seed}"
        mpath = os.path.join(workdir, f"{tag}.json")
        logp = os.path.join(workdir, f"{tag}.log")
        cmd = [args.serve, "-scenario", args.scenario, "-run", f"sb{prt}",
               "-seed", str(seed), "-ticks", str(args.ticks), "-pace", "0",
               "-capacity", str(args.capacity), "-intent-log=false",
               "-metrics-out", mpath, "-ws", f"127.0.0.1:{prt}"] + extra
        for attempt in range(3):
            with open(logp, "w") as lf:
                rc = subprocess.call(cmd, stdout=lf, stderr=subprocess.STDOUT)
            if rc == 0 and os.path.exists(mpath):
                return True, mpath, logp
            with open(logp) as lf:
                tail = lf.read()[-4000:]
            if not any(t in tail for t in whatif.TRANSIENT) or attempt == 2:
                return False, mpath, logp
            time.sleep(5 + 5 * attempt)
        return False, mpath, logp

    results = {}
    voided = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.jobs) as ex:
        futs = {ex.submit(run, arm, s, p): (arm, s) for arm, s, p in jobs}
        for fut in concurrent.futures.as_completed(futs):
            arm, s = futs[fut]
            ok, mpath, logp = fut.result()
            if ok and arm == "actuated":
                evidenced, rejected = actuation_evidence(logp)
                if not evidenced:
                    rej = f" ({rejected} verbs REJECTED)" if rejected else ""
                    print(f"[sigctl-bracket] VOID {arm} seed {s}: no signal "
                          f"verbs applied{rej} — the actuated arm ran as a "
                          f"silent no-op — see {logp}", file=sys.stderr)
                    ok = False
            if not ok or whatif.fidelity_problems(logp):
                print(f"[sigctl-bracket] VOID {arm} seed {s} — see {logp}",
                      file=sys.stderr)
                voided.append(f"{arm}:s{s}")
                continue
            results[(arm, s)] = whatif.load_metrics(mpath, args.warmup)
            print(f"[sigctl-bracket] done {arm} seed {s}: "
                  f"{results[(arm, s)]['speed_kmh']:.1f} km/h, "
                  f"{results[(arm, s)]['completed']} completed", file=sys.stderr)

    if voided and not args.allow_void:
        print(f"\n[sigctl-bracket] REFUSING TO REPORT: {voided} voided; "
              f"run logs kept at {workdir}", file=sys.stderr)
        sys.exit(2)

    base = {s: results[("fixed-time", s)] for s in seeds if ("fixed-time", s) in results}
    act = {s: results[("actuated", s)] for s in seeds if ("actuated", s) in results}
    metrics = ["speed_kmh", "completed", "mean_trip_s", "mean_time_loss_s",
               "active_at_horizon", "delivered_frac"]
    report = {"scenario": args.scenario, "seeds": seeds, "ticks": args.ticks,
              "warmup": args.warmup, "voided": voided, "metrics": {}}
    print(f"\n=== sigctl bracket: {args.scenario} ({len(act)} paired seeds, "
          f"warmup {args.warmup})")
    print(f"{'metric':22} {'fixed':>10} {'actuated':>10} {'Δ':>10} {'Δ%':>7} "
          f"{'p':>8} {'d':>7}")
    for m in metrics:
        pairs = [(act[s][m], base[s][m]) for s in seeds
                 if s in act and s in base]
        if not pairs:
            continue
        diffs = [a - b for a, b in pairs]
        t, p, d = whatif.paired_t(diffs)
        bm = whatif.mean([b for _, b in pairs])
        am = whatif.mean([a for a, _ in pairs])
        dm = whatif.mean(diffs)
        pct = 100 * dm / bm if bm else float("nan")
        print(f"{m:22} {bm:10.2f} {am:10.2f} {dm:+10.2f} {pct:+6.1f}% "
              f"{p:8.4f} {d:+7.2f}")
        report["metrics"][m] = {"fixed_time": bm, "actuated": am,
                                "delta": dm, "delta_pct": pct, "p": p,
                                "cohen_d": d}
    report["per_seed"] = {f"{arm}:s{s}": results[(arm, s)]
                          for (arm, s) in results}
    if args.out:
        with open(args.out, "w") as f:
            json.dump(report, f, indent=2)
        print(f"\n[sigctl-bracket] wrote {args.out}", file=sys.stderr)
    if not args.keep:
        shutil.rmtree(workdir, ignore_errors=True)


if __name__ == "__main__":
    main()
