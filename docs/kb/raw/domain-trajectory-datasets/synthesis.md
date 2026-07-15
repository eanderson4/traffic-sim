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

## Connections to Other Topics

- **Relates to:** `domain-macroscopic-flow-models` (waves to find; Edie definitions;
  −15 to −20 km/h anchor), `domain-traffic-flow-models` (calibration data for
  car-following; Nakayama's OV-model fit as template)
- **Informs:** `domain-congestion-metrics` (Edie as the metrics primitive),
  `concept-scenario-format` (CitySim ships SUMO/CARLA digital twins per site — a
  precedent for dataset→scenario conversion), `integration-maplibre-realtime`
  (x–t heatmap as a core visual), engine test suite (ring-road acceptance test)
