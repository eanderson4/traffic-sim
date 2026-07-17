# Trajectory Datasets & Overhead Analysis

> Real overhead trajectory data (NGSIM, I-24 MOTION, drone sets, ring-road experiments) is our calibration/validation corpus; Edie's definitions are the shared analytics primitive for real data and sim output.

## Overview

Traffic-sim needs to prove it reproduces real traffic phenomena — kinematic waves,
phantom jams, intersection queueing — not just plausible-looking motion. This topic
catalogs the real-world overhead trajectory datasets that make that possible, and the
analysis methods (Edie's generalized definitions, fundamental-diagram construction,
x–t heatmaps, adaptive smoothing) we must implement to "do our own calc" on them.

The research conclusion: data is abundantly available at every license tier, from
free public-domain sets (NGSIM, CC0 TGSIM) through the purpose-built I-24 MOTION
instrument (free registration, explicitly commercial-OK) to controlled ring-road
experiments with open data. We can measure wave speeds ourselves in real data and
use the same datasets — and the *same analytics code* — as validation targets for
the sim, including intersections and roundabouts via the German drone datasets.

The architectural consequence is the strongest takeaway: one Edie implementation
serves two consumers (real-data analysis and the sim's observability layer), so the
trajectory format must let real data and sim output flow through identical
analytics. Validation then reduces to "same metrics, same code, real vs sim."

## Key Components

| Component | Location | Purpose |
|---|---|---|
| NGSIM (I-80, US-101, Lankershim, Peachtree) | `raw/domain-trajectory-datasets/competitors.md` §NGSIM | First "do our own calc" dataset; congested windows with published ~18 km/h backward waves; free, CC BY-SA 3.0, raw video public |
| TGSIM | `raw/domain-trajectory-datasets/competitors.md` §TGSIM | NGSIM's 2024 successor; CC0 public domain — most permissive tier |
| I-24 MOTION / INCEPTION | `raw/domain-trajectory-datasets/competitors.md` §I-24 MOTION | Definitive wave dataset: 4.2 mi of freeway, hundreds of stop-and-go waves cataloged; free registration, commercial OK |
| levelXdata family (highD/exiD/rounD/inD/uniD) | `raw/domain-trajectory-datasets/competitors.md` §levelXdata | Intersection/roundabout/merge validation targets matching every VISION intersection type; non-commercial, reviewed application |
| pNEUMA | `raw/domain-trajectory-datasets/competitors.md` §pNEUMA | Network-scale urban drone swarm (~500k trajectories); CC BY 4.0, redistribution-friendly |
| Ring-road experiments (Sugiyama/Tadaki/Nakayama/Stern) | `raw/domain-trajectory-datasets/competitors.md` §Ring-road | Controlled phantom jams; engine acceptance-test and car-following calibration targets; open data |
| Edie's generalized definitions | `raw/domain-trajectory-datasets/implementation.md` §Edie | The q/k/u-from-trajectories primitive shared by real-data analysis and sim observability |
| x–t heatmaps, FD cells, ASM | `raw/domain-trajectory-datasets/implementation.md` §§FD, x–t, ASM | Wave-speed measurement and speed-field reconstruction methods |
| DIY capture + geo-trax pipeline | `raw/domain-trajectory-datasets/implementation.md` §DIY | Own episode footage (overpass first, drone under Part 107); MIT-licensed video→trajectories pipeline |
| Licensing tiers | `raw/domain-trajectory-datasets/standards-and-patterns.md` §Licensing | Access/redistribution rules per dataset; drives the ship-scripts-not-data repo pattern |

## How It Works

Recommended plan, in priority order (synthesis §Recommended Plan):

1. **NGSIM I-80 congested windows** (4:00–4:15pm, 5:00–5:30pm sets): first analysis
   exercise — compute Edie q/k/u, render an x–t heatmap, measure the published
   ~11 mph (~18 km/h) backward wave ourselves. Free, instant, raw overhead video
   included (episode-usable footage). Use Edie quantities, never raw accelerations.
2. **I-24 MOTION**: 40 poles (110–135 ft) with ultra-HD cameras over 4.2 miles of
   I-24 (276 cameras in the 2023 paper, 294 on the current site), ~230M
   vehicle-miles/year. The INCEPTION dataset powered automated detection of hundreds
   of stop-and-go waves — generation, propagation, merging, bifurcation — with a
   public wave-topology gallery. Reuse the arXiv:2409.00326 wave-identification
   method (critical-speed thresholding + graph components on x–t) at scale.
3. **Sugiyama ring experiment as a validation milestone**: reproduce in our sim —
   230 m circular track, 30 km/h target, jam must emerge spontaneously at ≥22 cars.
   Nakayama et al. 2016 fits the optimal-velocity model to this circuit data — a
   direct template for calibrating our car-following dynamics (ADR-0007's IDM
   default) against ring data. Stern et al. 2018 (open data) extends the story: one
   autonomously controlled car in a 22-vehicle ring damps the wave — 40% fuel
   reduction, 15% throughput gain.
4. **inD/rounD** (levelXdata): intersection and roundabout validation targets —
   every intersection type in VISION has a matching drone dataset here (highD:
   110,500 vehicles, 147 h, 25 Hz, positioning error typically <10 cm). Non-commercial
   reviewed application; clarify episode-use terms first.
5. **pNEUMA**: network-scale analysis later — 10-drone swarm over Athens CBD,
   ~1.3 km², ~100 intersections, ~500k trajectories, CC BY 4.0 (we can redistribute
   derived data with attribution). Caveat: signalized urban network, so signal queues
   dominate over spontaneous highway waves.
6. **DIY capture** (later episode segment): fixed vantage first (overpass/garage
   roof — no FAA involvement), drone second under Part 107. geo-trax (MIT license)
   is the most complete free pipeline: YOLOv8s detection (0.951 mAP@50 on 19k aerial
   images) → BoT-SORT tracking → Stabilo homography stabilization → georeferenced
   CSV; validated on 10 drones, 20 intersections, ~700k trajectories.

The analysis stack the tooling must implement:

- **Edie's generalized definitions** — for any space–time region A: q = Σ distance
  traveled in A / |A|, k = Σ time spent in A / |A|, u = q/k. Makes q = k·u an
  identity; the accepted standard for 100%-sampled trajectory data. Network
  extension (3D trajectories → MFD) applies to pNEUMA-scale data.
- **Fundamental diagrams via parallelogram cells** — 2025 open-source method: find
  near-stationary states in space–time cells whose edges align with wave speeds,
  apply Edie per cell. Rectangular cells mix states across wave fronts; slanted
  cells don't.
- **x–t heatmaps** — Edie speed field on an x–t grid → smooth → congestion stripes
  ARE the waves; wave speed = stripe slope. This is also a core viz output (see
  MapLibre realtime article).
- **Adaptive Smoothing Method** (Treiber & Helbing 2002) — reconstruct continuous
  speed fields by smoothing along characteristics: ≈ +80 km/h downstream in free
  flow, ≈ −15 km/h upstream in congestion; two anisotropic filters (typical kernel
  widths τ ≈ 1 min, σ ≈ 0.6 km — verify against the paper, defaults vary by
  publication) blended by a regime-detecting weight.

Empirical anchors our analysis must reproduce (standards-and-patterns §Anchors):
backward wave speed in congestion ≈ **−15 to −20 km/h** (ASM uses −15; NGSIM I-80
measured ~18); free-flow perturbations propagate ≈ **+80 km/h** downstream;
ring-road critical density — jam at ≥22 cars on a 230 m track at 30 km/h.

Design positions adopted for the project:

- **One Edie implementation, two consumers.** The same q/k/u code serves dataset
  analysis and the sim's observability subsystem; design the trajectory format so
  real data and sim frames flow through identical analytics. (No ADR yet ratifies
  this — it is the research recommendation that should shape the observability side
  of the ADR-0006 message planes and the engine's metrics output.)
- **Ring-road scenario as engine acceptance test**: "22 cars on a 230 m ring at
  30 km/h target must produce a backward-rotating jam; 21 must not" — a falsifiable,
  literature-anchored test of string instability, complementing the LWR shock-speed
  oracle from macroscopic flow models (which tests first-order behavior; the ring
  tests the instability LWR cannot produce). Determinism per ADR-0005 is what makes
  this a repeatable CI test.
- **Ship scripts, not data**: the repo carries downloaders/converters per dataset;
  users fetch from the source. This respects every license tier (CC0 → CC BY →
  BY-SA → BY-ND → registration → non-commercial application) uniformly.

## Gotchas

- **NGSIM raw accelerations are artifacts**: Coifman & Li 2017 re-extracted from the
  original video and found piecewise-constant speeds with bursty fake accelerations,
  trajectories overrunning stopped leaders, and missed vehicles (reported ~11% in one
  camera — unverified by audit; check the paper before quoting). "Beyond anything that
  could be corrected strictly through cleaning." Rule: use Edie-based macro
  quantities, which integrate rather than differentiate.
- **Differentiation amplifies noise**: position jitter at 10–25 Hz makes derived
  acceleration meaningless. Smooth trajectories before differentiating; prefer
  integral quantities. Same lesson applies to our own DIY capture — a generic COCO
  detector without stabilization yields unusable acceleration; a fine-tuned aerial
  detector with stabilized hover can reach highD-class (<10 cm) positioning.
- **Rectangular x–t cells corrupt FDs**: they mix traffic states across wave fronts.
  Use parallelogram cells aligned with wave speeds.
- **I-24 raw trajectories are fragmented**: tracking at 4.2-mile scale breaks
  trajectories; use the published "virtual trajectories" stitching tooling
  (arXiv:2311.10888) rather than cleaning naively.
- **License traps in the friendly-looking sets**: NGSIM is CC BY-SA (share-alike
  applies to derivatives we redistribute); AUTOMATUM is CC BY-ND (no redistributing
  modified data); levelXdata requires a reviewed non-commercial application with no
  file redistribution, and monetized-YouTube status is ambiguous (commercial license
  via levelxdata@fka.de); Zen Traffic Data is organizations-only — effectively
  inaccessible without institutional affiliation.
- **FAA §107.145 forbids sustained flight over moving vehicles**: no hovering above
  live lanes without Category 1–4 compliance or a waiver; the lawful pattern is
  hovering beside the roadway with an oblique view. Battery reality is ~25–30
  min/flight — why pNEUMA used a swarm and 30-min session windows. Fixed vantage
  points (overpass, garage roof) involve no FAA rules at all.
- **Every serious dataset ships a cleaning paper** — plan a cleaning stage in our
  pipeline from day one (pNEUMA's noise-treatment code is public).

## Open Questions

- **levelXdata episode terms**: does a monetized-but-educational YouTube episode
  count as commercial? Ask before relying on highD/inD/rounD for episode visuals, or
  price a commercial license.
- **Corrected NGSIM data location**: where is Coifman & Li's corrected I-80 dataset
  published (was "upon publication")? Montanino & Punzo reconstructed CSV mirrors
  need verifying.
- **First-analysis triage**: which specific NGSIM congested window and which I-24
  INCEPTION segments best fit the first wave-measurement exercise — decide hands-on
  after download.
- **ASM parameter verification**: exact kernel defaults (τ, σ) vary by publication;
  pin against the original Treiber & Helbing paper before implementing.

## Related

- [Macroscopic Flow Models](../business-domains/macroscopic-flow-models.md) — supplies the waves we hunt (LWR shock theory, the −15 to −20 km/h anchor) and the complementary shock-speed oracle test
- [Traffic Flow Models (Microscopic)](../business-domains/traffic-flow-models.md) — trajectory sets are the calibration corpus for car-following; Nakayama's OV-model ring fit is the template
- [Congestion Metrics](../business-domains/congestion-metrics.md) — Edie's definitions are the shared metrics primitive: one implementation feeds both real-data analysis and sim observability
- [Scenario Format](../concepts/scenario-format.md) — CitySim ships SUMO/CARLA digital twins per site — precedent for dataset→scenario conversion
- [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) — the x–t heatmap is a core visual for both real and simulated trajectories
- [Time Model](../architecture/time-model.md) — ADR-0005 determinism is what makes the ring-road jam a repeatable engine acceptance test

---
*Raw research: [raw/domain-trajectory-datasets](../../raw/domain-trajectory-datasets/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
