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

CHOOSING THE PRIMARY METRIC IS PART OF THE EXPERIMENT
--------------------------------------------------------------------------
On the CBD pod, three defensible metrics named three different winners from
the SAME ten paired seeds:

  mean_time_loss_s -> calm-secondary  (-11.0%, p=0.003)
  mean_trip_s      -> retime-short     (-8.2%, p=0.004)
  speed_kmh        -> cordon-20        (+9.3%, p=0.001)

Two of those three are artifacts:

  * `time_loss` is measured against each lane's FREE-FLOW reference time.
    Lower the posted speed limit and you lower the reference, so a variant
    that makes every trip slower can still book less "loss". calm-secondary
    wins on time loss while its actual trip times are 5.5% WORSE.

  * `mean_trip_s` and `mean_time_loss_s` both average over COMPLETED trips
    only. A variant that changes which trips finish changes the population
    being averaged. retime-short's trips look 8.2% faster while completing
    7.8% fewer of them — it finishes the easy ones and strands the rest.

So the default primary is `speed_kmh`: Edie's definition over every
vehicle-second in the window, which has no completed-trip population and no
free-flow reference. It is still not sufficient alone, because a variant can
raise speed by carrying less traffic — hence the VMT guard below, which is
why cordon-20's "win" is annotated rather than celebrated.
"""
import argparse
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
from whatif import paired_t  # noqa: E402  (the paired test, not a second copy)


def fmt(x, nd=2):
    return "—" if x is None or x != x else f"{x:.{nd}f}"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--report", required=True)
    ap.add_argument("--pick", type=int, default=4)
    ap.add_argument("--alpha", type=float, default=0.05)
    ap.add_argument("--min-effect", type=float, default=1.0,
                    help="practical-significance floor in percent. An option "
                         "must clear BOTH p<alpha and this to be offered as "
                         "the winner; significant-but-smaller ones are "
                         "demoted to fillers rather than presented as wins.")
    ap.add_argument("--title", default=None)
    ap.add_argument("--out", default=None)
    ap.add_argument("--exclude", action="append", default=[],
                    help="variant to leave out of the SHORTLIST (it still "
                         "appears in the full results table). For choosing "
                         "among several valid winners — three scenarios whose "
                         "answer is the same intervention is a worse show than "
                         "three different ones, and the full table keeps the "
                         "choice honest.")
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
    allv = {k: v for k, v in rep["variants"].items() if k != base}
    cands = {k: v for k, v in allv.items() if k not in set(args.exclude)}

    base_vmt = rep["variants"][base].get("all_metrics", {}).get("vmt_km")
    base_seeds = rep["variants"][base].get("per_seed", {})

    def vmt_test(v):
        """Paired test of this option's vehicle-distance against baseline.

        Computed from the per-seed records rather than compared against a
        fixed percentage: whether an option moves less traffic is a question
        the data answers, and a hand-picked threshold either forgives a real
        reduction or condemns noise.
        """
        pairs = [(m.get("vmt_km"), base_seeds.get(sd, {}).get("vmt_km"))
                 for sd, m in v.get("per_seed", {}).items()]
        diffs = [a - b for a, b in pairs if a and b]
        if len(diffs) < 2:
            return None
        _, p, _ = paired_t(diffs)
        return (sum(diffs) / len(diffs), p)

    def carries_its_traffic(v):
        """True unless the option moves SIGNIFICANTLY less vehicle-distance."""
        t = vmt_test(v)
        if t is None:
            return True
        delta, p = t
        return not (delta < 0 and p <= args.alpha)

    # Winners are ranked by whether they carry the traffic FIRST and by p
    # second. An option that raises speed while moving 11% less
    # vehicle-distance has not made the network better at its job, and
    # presenting it as the answer teaches the opposite of the lesson. A
    # guard-failing option can still be the winner if nothing else qualifies,
    # but only behind every option that does.
    # Statistical significance is not practical significance, and with six
    # paired seeds of a low-variance simulation it is cheap: chi-loop-urban's
    # seed-to-seed spread on network speed is ~0.45%, so a 0.5% effect clears
    # p<0.05 comfortably while being invisible to anyone driving in it.
    # An option must clear BOTH bars to be offered as the answer.
    def big_enough(v):
        return abs(v.get("delta_pct") or 0) >= args.min_effect

    winners = sorted((v for v in cands.values()
                      if v["verdict"] == "UPGRADE" and v["p"] <= args.alpha
                      and big_enough(v)),
                     key=lambda v: (not carries_its_traffic(v), v["p"]))
    # Fillers: anything not offered as the winner. That includes options that
    # are statistically significant but below the practical floor — they are
    # exactly the plausible-looking near-misses the format wants.
    others = sorted((v for v in cands.values()
                     if v["verdict"] != "UPGRADE" or not big_enough(v)),
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
        # the network. VMT is the guard rather than completed-trip count,
        # because completions are themselves subject to the survivorship
        # effect described above: vehicle-distance covered in the window is
        # measured over every vehicle, finished or not.
        if not carries_its_traffic(v):
            d, pv = vmt_test(v)
            note = (f" — CAUTION: moves {abs(100 * d / base_vmt):.1f}% less "
                    f"vehicle-distance (p={fmt(pv, 4)}), so this is not a "
                    f"like-for-like comparison: the network is doing less "
                    f"work, not doing it better")
        verdict = v["verdict"]
        if verdict == "UPGRADE" and abs(v.get("delta_pct") or 0) < args.min_effect:
            verdict = (f"statistically real but below the {args.min_effect:g}% "
                       f"practical floor")
        lines.append(f"- **{labels.get(n, n)}** (`{n}`): {verdict}, "
                     f"{fmt(v['delta_pct'], 1)}% on {metric}, p={fmt(v['p'], 4)}"
                     f"{note}")

    lines.append("\n## Everything tested\n")
    lines.append("| option | Δ% | p | d | verdict |")
    lines.append("|---|---:|---:|---:|---|")
    for n, v in sorted(allv.items(), key=lambda kv: kv[1]["p"]):
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
