# Stranding 02 — why 1,588 vehicles strand in the valid drain run

Date: 2026-08-05. Artifacts: `data/runs/drain2-base/base-s42.{json,log}` (216,000 ticks,
6 sim-h, proper drivers: coasting 0.01%, collisions 1,263 noise-level, injected 10,583,
completed 8,696, stranded 1,588, active at horizon 299). Scratch: `/tmp/chidiag/`
(`sealtopo.py`, `sealscan.py`, `chaintrace.py`, `drain2.*.json` aggregates).
Follow-up to `analysis/chicago-throughput/diagnosis-01.md`.

**Headline:** stranding is mostly *honest* — it tracks the congestion wave, not a
first-minute geometry failure. But it is amplified by a real import defect
(sub-vehicle-length "sliver" lanes coupling junctions into zero-storage chains), the
escape converts long-but-recoverable queues into incomplete trips, and ~300 vehicles
end the run permanently trapped in a handful of knots the escape cannot unlock.

## 1. Where strands happen

Top sections (log GRIDLOCK list) mapped against the network
(`/tmp/chidiag/sealtopo.py`):

| section | strands | approach lanes | block face | downstream (far side of box) | control |
|---|---|---|---|---|---|
| 1276540715 | 57 | 1 × 34.8 m | 34.8 m | 9.7 m lane → signalized cluster | **unsignalized** (row=major) |
| 48785101#1 | 54 | 2 × 116 m | 116 m | **0.2 m sliver** → j:11060766791 (4.6 m box); other branch 95.9 m | signalized 90 s cycle |
| 1028896255#1 | 41 | 2 × 120 m | 120 m | exits onto 48785101#1's approach (chains!) or 48.2 m lane | signalized 90 s |
| -1285040209#2 | 38 | 1 × 101 m | 101 m | 69 m / 111 m lanes into cluster_258024206 knot | signalized 90 s |
| 1031144476#0 | 38 | 2 × 32.5 m | 32.5 m | 57.5 m / 60.6 m lanes (both jam solid) | signalized 90 s |
| 435381824#0 | 35 | 4 × 28.5 m | 28.5 m | **0.2 m slivers** + 29.3/46.9 m lanes | signalized 108 s |
| 435656622 | 35 | 4 × 25.6 m | 25.6 m | **4.2 m slivers** → j:5701168857; **0.2 m sliver** → j:27440477 | signalized 90 s |
| 24115775#1 | 33 | 1 × 250 m | 250 m | same junction cluster as 435656622 | signalized 90 s |

Geography: all in the near-north/west-loop/CBD band (xy ≈ (6500–8400, 5700–9500)),
the same core districts the chi-half runreport flagged. Common shape: **short urban
block faces (25–120 m) between signalized junctions, with the box exit into
sub-vehicle-length lanes**. Two of the top eight feed the *same* junction cluster
(435656622 + 24115775#1 → cluster_269446276_5701168856); -1285040209#2, -930446695#1
and -24107711#1 all feed cluster_258024206_… — stranding clusters around a few
multi-junction knots, not uniformly across the grid.

**Sliver lanes are a network-wide import artifact**: 1,758 non-internal lanes < 5 m
(937 < 1 m; 1,033 sections contain one; 1,707 of them feed a junction box)
(`/tmp/chidiag` network scan). They come from netconvert splitting junction *clusters*
into separate junctions 0.2–4.2 m apart (divided-road crossings), which SUMO itself
models as one junction. 557 sit in the CBD box. **Correction (2026-08-05, see
sliver-merge-03): the `_d2` lane suffix is NOT a widening clone** — it is
netimport's sanitize-collision suffix for ± edge-id pairs (`518584001` vs
`-518584001`, engine/netimport/netimport.go:498-499), so `n518584001_0_d2` is a
genuine OSM reverse-direction lane. The real clone-a-sliver bug is mknetvariant's
`_w1` widening (7/14/881 sub-5 m clones in the widen1/widen2/gridwiden variant
networks), which does not touch the base network.

## 2. When they strand — "first demand wave" refuted at scale

Strand timing from the trips list (`/tmp/chidiag/drain2.trips.json`): 5 in min 5–10,
11 in min 10–15, then a smooth ramp 17 → 96 peaking at **min 70–75** (after the demand
peak, min 30–40, and after injection stops at min 60), decaying over the drain
(≤3/5min after min ~240). Median time-in-network before stranding: **50 min**
(p10 16 min, p90 132 min). 100% of stranded trips are workplace-bound (0 exit-bound),
matching diagnosis-01.

The "sealed from minute 0–5" hypothesis: an early-seal scan (road lanes ≥10 m with
vehicles aboard at <0.5 m/s through buckets 0 **and** 1, `/tmp/chidiag/sealscan.py`)
finds exactly **7 lanes network-wide**, all singletons in different sections, none in
the top stranding list — consistent with the 5 strands in bucket 1. So early seals
exist but are a handful of pockets, not a systemic first-wave failure.

One partial exception: `48785101#1`'s approaches already crawl at 2.2 m/s in bucket 0
and ≈0 from bucket 1 (occupancy ramps to full by bucket 4–6 and stays full to the
horizon) — that corridor is locally overloaded from the first 7.4k/h slice and never
recovers. But even there, *full* blockage develops over 20–30 min.

Per-section onset (interval series, `/tmp/chidiag/seal_series.json`): persistent
saturation of the top approaches begins bucket 2 (1276540715, min 10–15), buckets 4–6
(48785101#1, 1031144476#0), buckets 6–9 (435656622, 1028896255#1, 294592601#1) —
i.e. **min 10–45, tracking the demand ramp**, not the first wave.

## 3. Why — defect or physics

Seal mechanics (engine/rightofway.go:285-366): the box exit walk needs
vehicle-length + s0 (≈7 m) of room, accumulated through *empty* short lanes; it stops
at the first queue tail (`free += tail.S − tail.length; break`) or the next box (foe
inside → blocked; holding stop line → holdSeal). Consequence: **one vehicle stopped on
a 0.2–4.2 m sliver between two junctions capacity-seals the upstream box** — there is
zero storage between the junctions, so any hiccup at the far junction instantly
becomes a seal of the near one. That is what makes these chains brittle; it is an
import-fidelity defect, not physics. Reconstructed per section:

- **1276540715 (permanent, from min 10)**: approach → unsignalized 0.3 m box →
  **9.7 m** lane (`n1276540716`) that holds exactly one car, which is itself stopped
  at the next signalized cluster. That lane is at occupancy 0.52 *flat for the entire
  6 hours* — a one-car pocket whose leader never leaves. The upstream 34.8 m approach
  (full, occ 0.7 from bucket 2) strands 57 vehicles behind a single trapped car.
- **48785101#1 (permanent, from min 25)**: junction has two branches — one via a
  **0.2 m sliver** into j:11060766791, one into a healthy 95.9 m lane that *flows the
  whole run* (3–15 m/s). The approach lanes fill from bucket 4–6 and never drain (~31
  vehicles aboard at the horizon). Stranded lane-heads were routed into the sealed
  branch while the free branch carried other traffic past them.
- **1028896255#1 → 48785101#1 (chain seal)**: one exit branch of the 1028896255 box
  lands *on the 48785101 approaches*. When 48785101 sealed, 1028896255 sealed behind
  it (from bucket 6–8). Its other branch (48.2 m) flows freely at ~8 m/s all run.
- **435656622 / 24115775#1 (temporary)**: two feeders of one junction cluster, seal
  from bucket 6, **drain at bucket 19–20 (min 95–100)** once demand has been off for
  ~35 min. 35 + 33 vehicles stranded from a queue that would have discharged.
- **435381824#0 (temporary)**: sealed buckets 6–17 (≈0.4 m/s), recovers from
  bucket 18 (13 m/s by min 115). 35 strands from a recoverable queue.
- **294592601#1 (temporary)**: full from min 40–45, drains at min 135–140.

Signal programs are **not** the smoking gun: all sealed clusters run ordinary
~90 s two-phase cycles (42/3/42/3; one 108 s), every movement gets green (checked the
state strings for the involved junctions, incl. 27440477's 6-link program).

The permanently-trapped remnant: at the horizon **299 vehicles (≈326 by occupancy)
sit parked on ~660 lanes**, concentrated in a dozen sections — 48785101#1 (31),
587160710#2 (26), -1221637892#3 (21), -930446695#1 (15), 59421828#0 (14),
1328669512#0 (12), -24107711#1 (11) (`/tmp/chidiag/chaintrace.py`). None are
destination lanes; all are transit approaches feeding signalized clusters in the same
two knots (cluster_27477591/27477592 area and cluster_258024206 area). Five hours
after injection stops they still have not drained *and* the escape has not cleared
them — either true cycles whose feeder queues refill every freed slot (the escape
bleeds one head per 300 s per chain and resets timers 12 hops back), or escape-blind
holds. Smoking-gun anomaly: `n518584001_0_d2` (0.2 m) shows occupancy **25.0 flat for
buckets 60–71** — one vehicle parked for the last hour+ with its forward chain reading
empty at the horizon, never stranding and never moving. The interval data cannot
distinguish the mechanism (a holdSeal-class hold, a merge-funnel deadlock of the
`permissivedeadlock_test.go` class, or a leader across the boundary the occupancy
bookkeeping can't see); resolving it needs a keyframe/state dump, flagged as a
follow-up.

## 4. The escape itself (design notes only)

Current semantics (engine/gridlock.go:81-143): a vehicle stopped > `StrandAfterS`
(300 s, = SUMO `--time-to-teleport` default, engine.go:84-88) at the **head of its
lane**, waiting on a *capacity-sealed* box (reds explicitly excluded), is removed and
counted stranded; trip emitted incomplete. Observed against the data:

- **300 s is not the binding choice.** Stranded queues stood for tens of minutes to
  hours; no plausible threshold (300 s vs 900 s) changes the picture, only the count.
  The 300 s discriminator vs signal cycles (90 s programs → 3.3 cycles) is sound.
- **The removal semantics are doing real damage to trip-level honesty**: several top
  stranding sections are *recoverable* queues (435656622, 435381824#0, 294592601#1
  drain once demand stops) — those ~100+ stranded trips would have completed 30–90 min
  late. "The network drains" and "15% of trips strand" are two faces of the same
  policy. Design options, no code changed:
  - keep removal+incomplete (current): loud and honest at network level, but
    penalizes trips, not the demand/control that created the seal;
  - teleport-to-destination: fakes completions, hides the congestion — violates the
    mission guardrail ("no teleporting");
  - reroute the head: useless at a capacity seal (the destination path itself is
    blocked); the ADR-0036 adaptive layer already routes *approaching* traffic around;
  - prevent the seal: entry metering/gating and actuated signals (ADR-0037
    territory), plus import-level sliver merges — the only option that removes
    strands rather than reclassifying them.
- **Escape-blind pockets exist**: the `holdSeal` exclusion and the leader-owned
  carve-out mean some multi-hour trapped states (the `n518584001_0_d2` vehicle;
  possibly the horizon knots) never become strand-eligible. A vehicle held 5+ h by
  what is nominally a stop line is capacity-sealed in effect; whether the doctrine
  should keep ignoring it is a deliberate-design question for ADR-0034's next
  addendum, not a bug fix.

## Ranked causes

1. **Sliver-coupled junction chains (FIXABLE DEFECT — import/geometry).** 1,758
   sub-5 m road lanes (937 < 1 m) give junction clusters zero storage: one stopped
   vehicle on a sliver capacity-seals the upstream box (`exitWalk` tail rule). Every
   permanent knot and most top stranding sections involve ≤4.2 m far-side lanes.
   SUMO models these clusters as single junctions; netimport split them. Fix direction:
   merge slivers into the box at import (or treat sub-vehicle-length road lanes as
   junction-internal), and add a donor-length guard to mknetvariant's `_w1` widening
   (which cloned 0.2 m lanes in the widen variants — separate small bug).
2. **Genuine 2× oversaturation of the core grid (INHERENT — real physics).** Strands
   track the congestion wave (peak min 70–75, 100% workplace-bound, median 50 min in
   network before stranding). The knots form because the grid was fed ~2× its
   discharge for an hour; real cities gridlock the same way and respond with
   metering/gating — the demand-side lever (ADR-0037), not an engine defect.
3. **Escape semantics convert recoverable delay into trip failure (DESIGN —
   semi-fixable).** Top temporary-seal sections (435656622, 435381824#0, 294592601#1,
   1028896255#1) drained after demand stopped; their ~140+ stranded trips were
   long-delayed, not deadlocked. Consider threshold/semantics review *after* the
   metering work, since prevention shrinks the stranded set directly.
4. **Escape-blind permanent pockets (FIXABLE DEFECT — small but diagnostic).**
   ~300 vehicles trapped at the horizon in a dozen knots; one vehicle parked 1+ h on
   a 0.2 m lane with an empty forward chain, never strand-eligible. Needs a
   keyframe/state dump to pin the hold class (candidate: merge-funnel or holdSeal
   edge in `jammedAtJunction`).
5. **Early-seal pockets (MINOR DEFECT).** Only 7 lanes network-wide seal in the first
   10 minutes (5 strands in min 5–10) — real but negligible; likely the same sliver
   class as (1) at unsignalized micro-junctions (e.g. j:11851144338, row=major,
   0.3 m box).
