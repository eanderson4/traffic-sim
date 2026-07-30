# ADR-0030: The standard run report — distributions, not means

- Status: Accepted 2026-07-29; `runreport.py`, `mkzones.py` and the viz
  stats panel shipped. See the review note at the end of Consequences for
  what the first external round found in the implementation.
- Date: 2026-07-28
- Adds: `scripts/runreport.py`, `scripts/chicago/mkzones.py`, and the
  `districts.json` lane→district map. Establishes a versioned report JSON
  consumed by the viz.
- Does **NOT** touch: `contracts/asyncapi.yaml`, the scenario schema, the
  metrics part format (ADR-0014), or the engine. This reads what the kernel
  already emits.

## Context

Every calibration round invented its own numbers, and the two most-quoted
ones were both wrong in the same way — they were **means over heterogeneous
space**, and a mean cannot express "a small part of the road is destroyed".

1. **"Network density is 27% of critical, so we are not congested."** That
   was an average over 2,203 lane-km including every empty residential
   street. The same run had 55 lane-km at or above critical density moving at
   6.3 km/h. Both statements are true. Only one of them is about congestion.

2. **"The corridor mean is 26–41 km/h, inside the Chicago AM band, so the
   expressways are loaded like the real ones."** Measured properly, 68% of
   corridor lane-km-hours in that run sat above 60 km/h and 5.6% below
   20 km/h. The mean was a blend of a few jams and mostly free-flowing road.
   The real Kennedy at 8am is not 5% jammed and 95% empty.

Both survived for weeks because nothing ever printed the distribution, and
because each round's analysis was a throwaway script that answered one
question and was never run again.

## Decision

One tracked tool, `scripts/runreport.py`, is the standard way to ask how a
run performed. Its unit of measurement is the **space-time cell**: one lane
over one metrics interval, carrying lane-km and duration. Every figure is a
share of lane-km-hours or a share of VMT. No statistic in it is an average of
averages.

It reports, in order: the surviving window; totals; the **density
distribution** by band relative to critical; the **speed distribution** by
band, in both lane-km and VMT; per-corridor and per-district tables; the
per-interval curve; and the lanes carrying the delay.

Four conventions are load-bearing, each from a specific past failure:

- **Partial intervals are dropped and the count is printed** (ADR-0014 §3).
- **Denominators are the network's, not the occupied part's.** Otherwise an
  emptying network reads as *denser* the more of it clears.
- **Empty road gets its own bucket.** A lane with no vehicles has no defined
  speed; averaging it in as zero and dropping it silently are both wrong, and
  the empty share is itself the most informative number in a light run.
- **Overlapping measurement sets are refused without `--set`.** ADR-0014
  permits them; summing two double-counts the lanes they share.

**Both lane-km share and VMT share are always shown together**, because
their disagreement is the signal. 30% of lane-km below 20 km/h carrying 5% of
VMT means the jams are real but almost nobody is in them; the reverse means
the network is fine except exactly where everyone is.

### Districts

Corridors locate congestion along the expressways and nowhere else — 81% of
chi-loop-urban is unnamed arterial grid, and "arterial grid" as a single
1,779 lane-km bucket is not a location. `mkzones.py` maps lanes to districts
from a GeoJSON tiling (`districts.geojson`), producing the same shape as
`corridors.json` so every consumer of a lane→group map reads it unchanged.

The districts must **tile**: a lane in two districts breaks every share
computed from them, so the assignment is by lane midpoint, boundary crossings
are counted and warned about, and a network with no projection metadata is
refused rather than mapped through a guessed datum.

The same map serves demand: `mkod.py --dest-zones/--dest-zone-share` uses it
to steer what share of work trips is bound for the CBD. That is deliberate —
"where is congestion" and "where is everyone going" have to be asked in the
same coordinates or the answers cannot be compared.

### Report JSON

`--json` writes a `schema_version: 1` document with the same content, which
the viz stats panel renders. It is derived data and belongs in gitignored
`data/`; the schema is the contract.

## Consequences

Good: one tool, one vocabulary, comparable across runs and across runs of an
A/B. The report is where a fidelity claim gets checked before it is quoted,
and it makes the two errors above impossible to state — there is no
"network mean density" in the output to misread.

Costs and risks:

- **`CRITICAL_K = 25 veh/km/lane` is a rough freeway value applied
  network-wide.** A downtown arterial's critical density is lower. Nothing
  branches on it — it only rescales the band edges — but "% of critical" on
  the arterial grid must not be read as precisely as on a corridor.
- **The band edges are chosen, not derived.** They were picked so the 20 km/h
  queueing line and the 100%-of-critical line each fall on a boundary. A
  different set would tell a different-looking story from the same run.
- **Cells are weighted by lane-km-hours, so a long interval counts more than
  a short one.** With uniform `period_s` (what `mkmetrics.py` emits) this is
  a non-issue; with mixed periods the window shares tilt toward the coarse
  set.
- **Districts are rectangles on Chicago cut lines, not neighbourhood
  outlines.** They are honest about which side of the river or of Congress a
  lane is on, and dishonest about anything finer.

> **What the first implementation got wrong (2026-07-29, external review)**
>
> Four defects, all of the shape this ADR exists to prevent — a number that
> is wrong without looking wrong. Recorded here because the decision above
> reads as though stating the rule were sufficient, and it was not.
>
> - **Lane length was re-derived from `shape` instead of read from the
>   network's `length` field.** On chi-loop-urban 4,697 of 55,555 lanes
>   (8.5%) disagree by more than 5%, and 895 carry a positive length against
>   a polyline measuring exactly 0 m — junction internals emitted with a
>   single point. Those became 0.0 km and fell out of every distribution and
>   every group denominator. The "denominators fixed to the network" rule was
>   honoured in the code that computed the denominator and broken in the code
>   that measured its parts: **the survivorship bias of §3 reached by
>   arithmetic rather than by omission.** The network total barely moves
>   (+0.1 lane-km) because the affected lanes are short, which is exactly why
>   it survived — no headline figure looked wrong.
> - **A measurement set over a SUBSET of the network was reported as the
>   network.** ADR-0014 permits subset sets. Lane-km came from the network
>   file while travel came only from lanes that reported, so the report
>   divided a fraction of the traffic by all of the road. The metrics header
>   carries no `elements` declaration to read, so coverage must be DERIVED
>   from the lanes that appear; it is now printed, carried in the report JSON
>   as `coverage`, used as the fixed denominator, and the distribution scope
>   is labelled `measured subset` rather than `network`.
> - **`time_loss_s` was assumed present.** It is optional — nil when the
>   `time_loss` group is off — so a legal metrics file produced a bare
>   KeyError. Refused by name now: delay is a headline and hotspots are
>   RANKED by it, so a silently-zero report would read as "no delay" instead
>   of "not measured".
> - **A lane was assigned to a district by its middle SHAPE POINT, not its
>   arclength midpoint.** Vertices bunch where a road bends; for a two-point
>   lane `s[len(s) // 2]` is the far END. 188 Chicago lanes change district,
>   which moves both the district table and `mkod.py --dest-zones`
>   destination shares. The prose above already promised assignment "by its
>   MIDPOINT"; the code did something else, and the unit test asserted the
>   endpoint — it pinned the bug, which is how the bug survived.
>
> Still open: the report carries no real network fingerprint, so a consumer
> can project hotspot coordinates against a different network than the report
> was computed on (#86). The `coverage` block's `network` path and
> `network_lanes` count are a weak stand-in, not a hash.

## See also

- [ADR-0014](ADR-0014-observability-metrics.md) — the metrics parts this consumes
- [ADR-0028](ADR-0028-demand-profile-library.md) — demand shapes
- [ADR-0029](ADR-0029-warm-start-from-keyframe.md) — removing the fill from
  the measured window
- [Silent Fidelity Failures](../articles/concepts/silent-fidelity-failures.md)
