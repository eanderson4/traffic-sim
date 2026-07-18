# Synthesis: Trajectory Datasets & Overhead Analysis

> Researched: 2026-07-15 | Git HEAD: 071f5ad | Status: complete

## Summary

Real overhead traffic data is abundantly available — from free public-domain
trajectory sets (NGSIM, TGSIM) through the purpose-built I-24 MOTION wave instrument
(free, commercial-OK) to controlled ring-road phantom-jam experiments with open data.
We can find kinematic waves in real data, measure their speeds ourselves with Edie's
definitions + x–t heatmaps, and use the same datasets as validation targets for the
sim — including intersections and roundabouts via the German drone datasets.

## Source Files

- [Analysis methods](./implementation.md) — Edie's definitions, FD construction, x–t heatmaps, ASM, DIY capture + pipelines
- [Datasets catalog](./competitors.md) — NGSIM, TGSIM, levelXdata, pNEUMA, Zen, I-24 MOTION, AUTOMATUM, CitySim, ring experiments
- [Standards & patterns](./standards-and-patterns.md) — licensing tiers, FAA Part 107, error patterns, empirical anchors

## Recommended Plan (priority order)

1. **NGSIM I-80 congested windows** (free, instant, raw video included): first
   "do our own calc" exercise — Edie q/k/u, x–t heatmap, measure the ~18 km/h
   backward wave. Use Edie quantities, never raw accelerations (Coifman & Li).
2. **I-24 MOTION** (register): the definitive wave dataset; reuse the
   arXiv:2409.00326 wave-identification method for scale.
3. **Sugiyama ring experiment**: reproduce in our sim as a validation milestone —
   230 m ring, 22 cars, jam must emerge; Nakayama 2016 shows how to fit a
   car-following model to it. Stern 2018 open data extends this to the AV-damping
   story.
4. **inD/rounD** (apply, non-commercial): intersection and roundabout validation
   targets matching VISION's intersection types. Clarify episode-use terms first.
5. **pNEUMA** (CC BY 4.0): network-scale analysis later; most redistribution-friendly.
6. **DIY capture** (later, fun episode segment): overpass first (no FAA), drone with
   Part 107 + no sustained hover over live lanes; geo-trax pipeline (MIT).

## Key Architectural Implications

- **One Edie implementation, two consumers**: the same q/k/u-from-trajectories code
  must serve (a) real dataset analysis and (b) the sim's observability layer. Design
  the trajectory format so real data and sim output flow through identical analytics
  — this is also the validation mechanism (same metrics, same code, real vs sim).
- **Ship scripts, not data**: repo contains downloaders/converters per dataset;
  respects every license tier.
- **Ring-road scenario as an engine acceptance test**: "22 cars on a 230 m ring at
  30 km/h target must produce a backward-rotating jam; 21 must not" is a falsifiable,
  literature-anchored test of string instability — complements the LWR shock-speed
  oracle from [[domain-macroscopic-flow-models]] (which tests first-order behavior;
  this tests the instability LWR cannot produce).

## Open Questions

- levelXdata non-commercial terms vs monetized educational video — ask.
- Coifman & Li corrected I-80 data location; Montanino & Punzo CSV mirrors.
- Which specific NGSIM window / I-24 segment for the first analysis — triage on
  download.

## M2 validation outcome (2026-07-17)

The NGSIM I-80 17:00–17:15 window was reproduced in the engine (M2; scenario
`engine/scenario_i80.go`, write-up `analysis/ngsim/README.md` § Simulation
validation). Findings that now belong to the knowledge base:

- The M1 kernel (string-stable IDM: a = 1.0, T = 1.5) produces genuine
  stop-and-go structure (4 crossing stripes, go-state q/k matching the real
  field) but its wave speed caps at ≈ −12 km/h vs the −15…−20 km/h anchor.
  Root cause: with the stable calibration, jam troughs creep at the IDM
  *equilibrium* speed (≈ 3.3 m/s); real troughs are sub-equilibrium
  (≈ 2.7 m/s). Wave speed = chord slope between go and jam states, so
  equilibrium troughs structurally bound the slope — closing the gap needs an
  instability-capable calibration (literature highway IDM: a ≈ 0.73–1.0,
  T ≈ 1.6–1.7), i.e. physics work, not scenario tuning.
- Measurement: the variance-scan wave-speed estimator (analysis/ngsim) is
  hijacked by mass-dominated x–t fields (reports ≈ 0 despite stripes); the
  real field is stripe-dominated so the anchor is safe. The FD chord-slope
  estimator is the robust cross-check.
- The M1 merge model produces multi-metre negative gaps under sustained
  overload (≈ 3,000 observations/run at merges; caps funnel discharge at
  ≈ 1,460 vs 1,780 veh/h/lane) — top physics debt for M3.

## M3 update (2026-07-17): physics hardening done

The M3 work closed both debts (details: analysis/ngsim/README.md M3 section;
calibration note: `domain-traffic-flow-models/standards-and-patterns.md`):

- Merge gap enforcement fixed (kinematic collision-freedom floor +
  cross-boundary hop checks): negative gaps ≈ 3,000/run → **0** (min gap
  −11.8 m → +0.39 m); funnel discharge 7,300 → 7,680 veh/h.
- Car defaults recalibrated to the original IDM paper's highway set
  (a = 0.73, T = 1.6, b = 1.67): the −12 km/h structural cap is broken —
  sim wave speed −13.2…−15.4 km/h (scan, seeds 1–5) and −13.7…−15.0 km/h
  (per-wave leg median, seeds 2–5) vs real −18.1 (scan) / **−15.0 (leg
  median, same estimator)**. The robust per-wave measurement has the sim
  matching the real median at the best seeds; residual ≈ 0–1.3 km/h, traced
  to IDM's discharge headway (τ ≈ 1.8 s vs ≈ 1.4 s real — anticipation gap).
  Sugiyama ring acceptance test added (engine/sugiyama_test.go): spontaneous
  jam from noise, −13.7 km/h backward, stable control at lower density.
- Reference boundary condition re-anchored to the real queue's growth
  (sustained 1.20× demand ≈ measured tail-creep shortfall ≈ 1,440–1,500
  veh/h); M2's quasi-stationary window no longer sustains stop-and-go after
  the discharge fix.

## Connections to Other Topics

- **Relates to:** `domain-macroscopic-flow-models` (waves to find; Edie definitions;
  −15 to −20 km/h anchor), `domain-traffic-flow-models` (calibration data for
  car-following; Nakayama's OV-model fit as template)
- **Informs:** `domain-congestion-metrics` (Edie as the metrics primitive),
  `concept-scenario-format` (CitySim ships SUMO/CARLA digital twins per site — a
  precedent for dataset→scenario conversion), `integration-maplibre-realtime`
  (x–t heatmap as a core visual), engine test suite (ring-road acceptance test)
