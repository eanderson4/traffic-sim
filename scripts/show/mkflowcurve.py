#!/usr/bin/env python3
"""mkflowcurve.py — cumulative input-output (Newell) curves from a metrics file.

Reads one `-metrics-out` JSON (ADR-0014 §6) and emits the per-bin arrival /
departure / strand counts plus the accumulation that follows from them, for
the flow dashboard (viz/public/flow.html).

Why cumulative curves and not just a rate chart: the vertical gap between
the arrival and the departure curve IS the number of vehicles in the network,
and the horizontal gap is the delay each vehicle experiences. Both are read
off one plot. That is the standard queueing diagram, and it is the honest way
to show whether a network is filling, holding, or draining — a rate chart
alone hides accumulation, which is the thing that actually gridlocks.

Accumulation here is derived from the trip ledger, not from a vehicle count
sampled off snapshots: every vehicle in `trips` has an entry_tick and an
exit_tick, so arrivals minus departures minus strands is exact by
construction and cannot drift from the totals block. It is checked against
`totals` before writing.

Three exit channels, kept distinct because conflating them is how a network
that never clears gets reported as one that does:
  completed — reached its destination lane
  stranded  — removed by the gridlock escape (ADR-0034) after being stopped
              at a blocked junction; a failure, not a departure
  active    — still driving when the horizon cut the run

Usage:
  mkflowcurve.py run.metrics.json -o viz/public/chi-flow.json [--bin-s 30]
"""

import argparse
import json
import sys


def build(metrics, bin_s):
    dt = metrics["dt"]
    horizon = metrics["ticks"]
    bin_ticks = max(1, round(bin_s / dt))
    # Bins cover [0, horizon]. The final partial bin is kept: unlike the
    # interval cells in runreport.py, a trip-ledger count is not an average
    # over a window, so a short last bin under-counts rather than distorting.
    #
    # horizon // bin_ticks + 1, NOT ceil(horizon / bin_ticks) + 1. The
    # ceiling form emitted one bin past the end whenever the horizon was not
    # a whole number of bins: its start tick exceeded `ticks`, and consumers
    # scale x against `ticks`, so that point plotted outside the chart. This
    # form makes the last bin the one CONTAINING tick `horizon`, so no bin
    # ever starts after the run ends. Identical when the horizon divides
    # evenly (54,000 ticks / 300 = 181 bins either way).
    nbins = horizon // bin_ticks + 1

    arrivals = [0] * nbins
    completions = [0] * nbins
    strands = [0] * nbins

    def b(tick):
        return min(tick // bin_ticks, nbins - 1)

    n_active = 0
    for t in metrics["trips"]:
        arrivals[b(t["entry_tick"])] += 1
        if t["completed"]:
            completions[b(t["exit_tick"])] += 1
        elif t.get("stranded"):
            # The EXPLICIT flag (engine/metricsjson.go: `stranded,omitempty`,
            # always with completed=false), not `exit_tick < horizon`.
            # Inferring it from the tick misclassifies a vehicle stranded ON
            # the final tick as still-driving, and since the totals
            # cross-check below is exact, that turned a valid metrics document
            # into a refusal. The flag is omitted on ordinary trips, so .get
            # is the correct read rather than a defensive one.
            strands[b(t["exit_tick"])] += 1
        else:
            # Still driving at the cut. It has no exit bin — it must NOT be
            # counted as a departure, or the network would appear to clear.
            n_active += 1

    cum_a = cum_c = cum_s = 0
    bins = []
    for i in range(nbins):
        cum_a += arrivals[i]
        cum_c += completions[i]
        cum_s += strands[i]
        bins.append(
            {
                "tick": i * bin_ticks,
                "min": round(i * bin_ticks * dt / 60.0, 4),
                "arr": arrivals[i],
                "done": completions[i],
                "strand": strands[i],
                "cumArr": cum_a,
                "cumDone": cum_c,
                "cumStrand": cum_s,
                "inNet": cum_a - cum_c - cum_s,
            }
        )

    # Cross-check against the totals the kernel computed independently. A
    # mismatch means the ledger and the counters disagree, which would make
    # every number on the dashboard suspect — refuse rather than publish it.
    tot = metrics["totals"]
    for name, got, want in (
        ("completed", cum_c, tot["completed_trips"]),
        ("stranded", cum_s, tot["stranded_trips"]),
        ("active", n_active, tot["active_at_horizon"]),
    ):
        if got != want:
            sys.exit(
                f"mkflowcurve: {name} from the trip ledger is {got}, "
                f"totals says {want} — refusing to write a report that "
                f"disagrees with its own source"
            )

    # A run with no trips is legal — a horizon too short for the first
    # arrival, or a scenario with no demand. `max()` over it raised a bare
    # ValueError, the one failure in this file that did not explain itself.
    # No entries means the drain never starts, so there is no tick to mark.
    last_entry = max((t["entry_tick"] for t in metrics["trips"]), default=0)
    return {
        "schema_version": 1,
        "dt": dt,
        "ticks": horizon,
        "binTicks": bin_ticks,
        # The tick after which no vehicle ever enters. The drain phase starts
        # here; the dashboard marks it, because "is it draining" is only a
        # meaningful question once nothing more is arriving.
        "lastEntryTick": last_entry,
        "totals": {
            "injected": cum_a,
            "completed": cum_c,
            "stranded": cum_s,
            "activeAtHorizon": n_active,
            "peakInNet": max(x["inNet"] for x in bins),
            "peakInNetTick": max(bins, key=lambda x: x["inNet"])["tick"],
        },
        "bins": bins,
    }


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("metrics")
    ap.add_argument("-o", "--out", required=True)
    ap.add_argument(
        "--bin-s", type=float, default=30.0, help="bin width in simulated seconds"
    )
    a = ap.parse_args()

    with open(a.metrics) as f:
        m = json.load(f)
    if m.get("schema_version") != 1:
        sys.exit(f"mkflowcurve: unsupported metrics schema_version {m.get('schema_version')}")
    out = build(m, a.bin_s)
    with open(a.out, "w") as f:
        json.dump(out, f, separators=(",", ":"))
    t = out["totals"]
    print(
        f"mkflowcurve: {len(out['bins'])} bins of {a.bin_s:g}s — "
        f"{t['injected']:,} injected, {t['completed']:,} completed, "
        f"{t['stranded']:,} stranded, {t['activeAtHorizon']:,} active at the cut; "
        f"peak {t['peakInNet']:,} in network at tick {t['peakInNetTick']:,}"
    )
    print(f"wrote {a.out}")


if __name__ == "__main__":
    main()
