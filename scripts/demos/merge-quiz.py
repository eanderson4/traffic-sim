#!/usr/bin/env python3
"""Write the Scenario B quiz payload in curate.py's schema, with ALL options.

    python3 scripts/demos/merge-quiz.py \\
        --report docs/show/reports/merge-pod.json \\
        --labels docs/show/labels-merge.json \\
        --out docs/show/quiz/merge-pod.json

WHY THIS EXISTS RATHER THAN `curate.py --json`
--------------------------------------------------------------------------
curate.py builds the shortlist as "the best significant winner, then FILL
the remaining slots with options that are NOT significant wins" — the format
needs exactly one right answer, so it needs plausible losers to hide it
among. On this pod there are none to hide it among: three of the four
options are significant upgrades and the fourth is significantly worse.
`curate.py --pick 4` therefore returns a two-option menu, which is correct
behaviour and the wrong artifact.

That is a finding about this scenario, not a defect: a merge bottleneck is
the one situation where several unrelated interventions all genuinely work,
because all of them relieve the same single constraint. The menu is
presented as four options with one BEST answer rather than one right answer,
and the reveal is the ranking. Every field below is computed exactly as
curate.py computes it — same paired VMT guard, same practical floor, same
verdict correction — so the two agree wherever they overlap.
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", "chicago"))
from curate import paired_t  # noqa: E402  (curate.py is the reference impl)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", required=True)
    ap.add_argument("--labels", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--title", default="The Merge")
    ap.add_argument("--alpha", type=float, default=0.05)
    ap.add_argument("--min-effect", type=float, default=1.0,
                    help="practical floor in percent (curate.py's default)")
    args = ap.parse_args()

    with open(args.report) as f:
        rep = json.load(f)
    with open(args.labels) as f:
        labels = json.load(f)

    base = rep["baseline"]
    metric = next(iter(rep["variants"].values()))["metric"]
    base_seeds = rep["variants"][base].get("per_seed", {})
    opts = {k: v for k, v in rep["variants"].items() if k != base}

    def vmt_test(v):
        diffs = [m["vmt_km"] - base_seeds[sd]["vmt_km"]
                 for sd, m in v.get("per_seed", {}).items()
                 if sd in base_seeds]
        if len(diffs) < 2:
            return None
        _, p, _ = paired_t(diffs)
        return sum(diffs) / len(diffs), p

    def carries(v):
        t = vmt_test(v)
        if t is None:
            return True
        delta, p = t
        return not (delta < 0 and p <= args.alpha)

    def verdict(v):
        delta = v.get("delta_pct") or 0
        if v["verdict"] == "UPGRADE" and abs(delta) < args.min_effect:
            return "no-op (under practical floor)"
        return v["verdict"]

    ranked = sorted(opts.items(),
                    key=lambda kv: -(kv[1].get("delta_pct") or 0))
    winners = [k for k, v in ranked
               if v["verdict"] == "UPGRADE" and v["p"] <= args.alpha
               and abs(v.get("delta_pct") or 0) >= args.min_effect
               and carries(v)]
    payload = {
        "title": args.title,
        "baseline": base,
        "metric": metric,
        "seeds": len(rep["seeds"]),
        "ticks": rep["ticks"],
        "winner": winners[0] if winners else None,
        "options": [{
            "name": k,
            "label": labels.get(k, k),
            "delta_pct": v.get("delta_pct"),
            "p": v.get("p"),
            "cohen_d": v.get("cohen_d"),
            "verdict": verdict(v),
            "carries_traffic": carries(v),
        } for k, v in ranked],
        "all_tested": {k: {"delta_pct": v.get("delta_pct"), "p": v.get("p"),
                           "verdict": v["verdict"]} for k, v in opts.items()},
    }
    with open(args.out, "w") as f:
        json.dump(payload, f, indent=2)
    print(f"[merge-quiz] wrote {args.out} "
          f"({len(payload['options'])} options, winner {payload['winner']!r})",
          file=sys.stderr)


if __name__ == "__main__":
    main()
