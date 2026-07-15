# Standards & Patterns: Trajectory Datasets & Overhead Analysis

> Source: web research | Researched: 2026-07-15

## Licensing / Access Patterns (decision-relevant)

| Tier | Datasets | What we can do |
|---|---|---|
| Public domain / CC0 | TGSIM | anything |
| CC BY (attribution) | pNEUMA (BY 4.0), NGSIM (BY-SA 3.0) | redistribute derived data in our OSS repo with attribution (SA: share-alike for NGSIM derivatives) |
| CC BY-ND | AUTOMATUM | use + publish plots; no redistributing modified data |
| Registration, commercial OK | I-24 MOTION | use in episode + OSS; no blanket redistribution |
| Reviewed non-commercial application | levelXdata (highD/inD/rounD/exiD/uniD) | analysis + published plots; NO file redistribution; monetized-video status ambiguous — ask or license |
| Organizations only | Zen Traffic Data | effectively inaccessible to us |

Pattern for the repo: **ship download/conversion scripts, not data** — every
dataset's terms are satisfied if users fetch from the source themselves (the
approach NGSIM/pNEUMA tooling repos take).

## Regulatory: FAA Part 107 (DIY drone capture)

- Part 107 certificate required for non-recreational flying (research/monetized
  content) ([FAA](https://www.faa.gov/uas/commercial_operators)).
- §107.145: no **sustained** flight over moving vehicles without Category 1–4 /
  waiver; brief transit OK; hover beside the road, not over live lanes
  ([Cornell LII](https://www.law.cornell.edu/cfr/text/14/107.145)).
- Waivers via FAA DroneZone ([FAA waivers](https://www.faa.gov/uas/commercial_operators/part_107_waivers)).
- Fixed vantage points (overpass, garage roof) involve no FAA rules at all —
  ordinary photography of public roads; only practical constraints (fencing, don't
  distract drivers, DOT permission only if mounting equipment on their structures).

## Known Error Patterns in Overhead Trajectory Data

1. **Differentiation amplifies noise**: position noise at 10–25 Hz makes raw
   acceleration meaningless (NGSIM's bursty fake accelerations — Coifman & Li 2017,
   [OSU PDF](https://ceg.osu.edu/sites/default/files/2022-06/Coifman_and_Li_2017.pdf)).
   Rule: smooth trajectories before differentiating; prefer integral (Edie)
   quantities.
2. **Occlusion + oblique geometry** (building/pole cameras): missed vehicles
   (⚠ ~11% in one NGSIM camera per Coifman & Li — unverified by audit), trajectory
   overrun of stopped leaders. Drones looking straight
   down largely avoid this — the stated motivation of highD.
3. **Tracking fragmentation** at scale (I-24 MOTION): fix via trajectory stitching /
   "virtual trajectories" ([arXiv:2311.10888](https://arxiv.org/abs/2311.10888)).
4. Every serious dataset ships a cleaning paper — plan for a cleaning stage in our
   pipeline from day one.

## The Empirical Anchors (what our analysis should find)

- Backward wave speed in congestion ≈ **−15 to −20 km/h** (ASM uses −15; NGSIM I-80
  measured ~18; consistent with the stylized-facts literature in
  [[domain-macroscopic-flow-models]]).
- Free-flow perturbations propagate downstream ≈ **+80 km/h** (ASM parameter).
- Ring-road critical density: jam emerges at ≥22 cars on a 230 m track at 30 km/h
  target speed (Sugiyama 2008) — a concrete, reproducible number our engine must hit
  when we rebuild the experiment in sim.

## Methodological Lineage (for the episode narrative)

Treiterer helicopter plots (1974) → Sugiyama ring road (2008) → Stern AV-damping
(2018) → I-24 MOTION instrument + automated wave topology (2024–). Our arc: show the
history, reproduce the ring experiment in our sim, then find the same waves in
NGSIM/I-24 x–t heatmaps with our own Edie-based tooling.

## Open Questions

- levelXdata terms for a monetized-but-educational YouTube episode — ask before
  relying on highD/inD/rounD for episode visuals.
- Which congested NGSIM windows and I-24 INCEPTION segments best fit a first
  wave-measurement exercise — needs hands-on triage after download.
- Whether Coifman & Li's corrected I-80 data is now published and where (was
  "upon publication"); Montanino & Punzo reconstructed CSV mirrors need verifying.
