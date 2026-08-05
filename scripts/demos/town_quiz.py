#!/usr/bin/env python3
"""Emit the Bottleneck Town quiz payload from a whatif.py report.

    town_quiz.py --report town-main.json --out docs/show/quiz/bottleneck-town.json

Schema-compatible with `scripts/chicago/curate.py --json` (the input
`scripts/chicago/mkquiz.py` consumes): top-level title/baseline/metric/
seeds/ticks/winner, an `options[]` shortlist with name/label/delta_pct/p/
cohen_d/verdict/carries_traffic, and `all_tested` covering every arm that
was measured, including the ones held off the menu.

WHY NOT curate.py ITSELF
--------------------------------------------------------------------------
curate.py builds a shortlist of ONE significant winner plus non-winning
fillers, because the Chicago scenarios are games where exactly one option
works. On this corridor three of the four menu options are real upgrades —
the baseline is far enough past signal capacity that most interventions
help — so there are not enough non-winners to fill curate's slots, and
forcing it would mean hiding real results. The menu here is fixed by hand
(OPTIONS below) and the question is "which helps MOST", not "which helps".
The `carries_traffic` guard, the verdicts and the paired statistics are
read straight out of the whatif report so they cannot drift from it.

Pure stdlib.
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                "..", ""))
from whatif import paired_t  # noqa: E402

# The on-air menu, in the order it should be offered. add-lane is measured
# but deliberately NOT here — see docs/show/bottleneck-town.md.
OPTIONS = [
    ("bypass-north", "Build a bypass north of town — a new road cutting "
                     "across the inside of Main Street's bend"),
    ("connector-south", "Build a relief road south of town — a new road "
                        "linking the south ends of all four cross streets"),
    ("retime-short", "Shorter signal cycles — same phases, 86 s cycle down "
                     "to 66 s"),
    ("green-wave", "Coordinate the lights into a green wave — same hardware, "
                   "same green times, offsets set for an eastbound "
                   "progression"),
]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--title", default="Bottleneck Town")
    ap.add_argument("--alpha", type=float, default=0.05)
    args = ap.parse_args()

    rep = json.load(open(args.report))
    base = rep["baseline"]
    metric = next(iter(rep["variants"].values()))["metric"]
    base_seeds = rep["variants"][base].get("per_seed", {})

    def carries_traffic(v):
        """False only when the option moves SIGNIFICANTLY less traffic."""
        pairs = [(m.get("vmt_km"), base_seeds.get(sd, {}).get("vmt_km"))
                 for sd, m in v.get("per_seed", {}).items()]
        diffs = [a - b for a, b in pairs if a and b]
        if len(diffs) < 2:
            return True
        _, p, _ = paired_t(diffs)
        d = sum(diffs) / len(diffs)
        return not (d < 0 and p <= args.alpha)

    options = []
    for name, label in OPTIONS:
        v = rep["variants"][name]
        options.append({
            "name": name, "label": label,
            "delta_pct": v["delta_pct"], "p": v["p"],
            "cohen_d": v["cohen_d"], "verdict": v["verdict"],
            "carries_traffic": carries_traffic(v),
        })
    ranked = [o for o in options
              if o["verdict"] == "UPGRADE" and o["p"] <= args.alpha
              and o["carries_traffic"]]
    # `or 0` mirrors merge-quiz.py: a voided arm reports a null
    # delta_pct, and sorting None against floats raises.
    ranked.sort(key=lambda o: -(o["delta_pct"] or 0))
    payload = {
        "title": args.title,
        "baseline": base,
        "metric": metric,
        "seeds": len(rep["seeds"]),
        "ticks": rep["ticks"],
        "winner": ranked[0]["name"] if ranked else None,
        "options": options,
        "all_tested": {
            k: {"delta_pct": v["delta_pct"], "p": v["p"],
                "verdict": v["verdict"]}
            for k, v in sorted(rep["variants"].items()) if k != base
        },
    }
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    with open(args.out, "w") as f:
        json.dump(payload, f, indent=2)
    print(f"[town-quiz] winner {payload['winner']!r}, "
          f"{len(options)} options -> {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
