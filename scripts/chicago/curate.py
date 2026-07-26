#!/usr/bin/env python3
"""Turn a whatif.py report into the shortlist a guest actually chooses from.

    curate.py --report chi-whatif.json --pick 4 --title "The Loop" \\
        --out docs/show/chi-loop-options.md

THE GAME THIS SERVES
--------------------------------------------------------------------------
The guest is shown 3-4 plausible-sounding upgrades and picks one. For that
to be a game rather than a quiz, the options must be indistinguishable by
reputation and separable only by measurement: exactly one real winner, the
rest no-ops or actively worse, and no way to tell which is which from the
label.

So curation is: take the best SIGNIFICANT option (p < alpha in the helpful
direction), then fill the remaining slots with the most plausible-sounding
options that are NOT significant wins — preferring ones whose point
estimate looks encouraging, because an option that is obviously bad on its
face is not a real choice.

If nothing reaches significance the tool says so instead of promoting the
largest point estimate. A shortlist whose "winner" is noise is worse than
no shortlist: the guest picks it, the reveal says it worked, and the whole
exercise has taught everyone something false.

Effect sizes are reported alongside p, because n is small by construction
(each seed is a full simulation) and |d| >= 0.8 with p just over alpha
means underpowered, not absent.
"""
import argparse
import json
import sys


def fmt(x, nd=2):
    return "—" if x is None or x != x else f"{x:.{nd}f}"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", required=True)
    ap.add_argument("--pick", type=int, default=4)
    ap.add_argument("--alpha", type=float, default=0.05)
    ap.add_argument("--title", default=None)
    ap.add_argument("--out", default=None)
    ap.add_argument("--labels", default=None,
                    help="JSON file mapping variant name -> guest-facing label")
    args = ap.parse_args()

    with open(args.report) as f:
        rep = json.load(f)
    labels = {}
    if args.labels:
        with open(args.labels) as f:
            labels = json.load(f)

    base = rep["baseline"]
    metric = next(iter(rep["variants"].values()))["metric"]
    # whatif.py records the direction in the verdict it already computed, so
    # "helpful" here is read from that rather than re-derived and risking
    # disagreement with the table the numbers came from.
    cands = {k: v for k, v in rep["variants"].items() if k != base}

    winners = sorted((v for v in cands.values()
                      if v["verdict"] == "UPGRADE" and v["p"] <= args.alpha),
                     key=lambda v: v["p"])
    others = sorted((v for v in cands.values() if v["verdict"] != "UPGRADE"),
                    key=lambda v: -abs(v.get("cohen_d") or 0))

    name_of = {id(v): k for k, v in cands.items()}
    lines = []
    t = args.title or rep["pod"]
    lines.append(f"# {t} — upgrade options\n")
    lines.append(f"Baseline `{base}`, {len(rep['seeds'])} paired seeds, "
                 f"{rep['ticks']} ticks, warmup {rep['warmup']}. "
                 f"Primary metric: `{metric}`.\n")

    if not winners:
        lines.append("> **No option reached significance.** Nothing here is a "
                     "demonstrable win at this sample size — do not run this "
                     "set as a game until one is found or more seeds are "
                     "added. The strongest point estimates below are ranked "
                     "by effect size so they can be re-tested, not promoted.\n")
        shortlist = others[:args.pick]
    else:
        win = winners[0]
        shortlist = [win] + [o for o in others
                             if id(o) != id(win)][:args.pick - 1]

    lines.append("| option | Δ vs base | Δ% | p | Cohen's d | verdict |")
    lines.append("|---|---:|---:|---:|---:|---|")
    for v in shortlist:
        n = name_of[id(v)]
        lines.append(f"| {labels.get(n, n)} | {fmt(v['delta'])} | "
                     f"{fmt(v['delta_pct'], 1)}% | {fmt(v['p'], 4)} | "
                     f"{fmt(v['cohen_d'])} | {v['verdict']} |")

    lines.append("\n## Answer key\n")
    for v in shortlist:
        n = name_of[id(v)]
        am = v.get("all_metrics", {})
        note = ""
        # An option that "wins" by carrying less traffic is not an upgrade to
        # the network, and throughput is the only place that shows.
        bt = rep["variants"][base].get("all_metrics", {}).get("completed")
        ct = am.get("completed")
        if bt and ct and ct < bt * 0.97:
            note = (f" — CAUTION: completes {100 * (1 - ct / bt):.0f}% fewer "
                    f"trips than baseline; any gain here is partly from "
                    f"carrying less traffic")
        lines.append(f"- **{labels.get(n, n)}** (`{n}`): {v['verdict']}, "
                     f"{fmt(v['delta_pct'], 1)}% on {metric}, p={fmt(v['p'], 4)}"
                     f"{note}")

    lines.append("\n## Everything tested\n")
    lines.append("| option | Δ% | p | d | verdict |")
    lines.append("|---|---:|---:|---:|---|")
    for n, v in sorted(cands.items(), key=lambda kv: kv[1]["p"]):
        lines.append(f"| {n} | {fmt(v['delta_pct'], 1)}% | {fmt(v['p'], 4)} | "
                     f"{fmt(v['cohen_d'])} | {v['verdict']} |")

    text = "\n".join(lines) + "\n"
    if args.out:
        with open(args.out, "w") as f:
            f.write(text)
        print(f"[curate] wrote {args.out}", file=sys.stderr)
    else:
        print(text)


if __name__ == "__main__":
    main()
