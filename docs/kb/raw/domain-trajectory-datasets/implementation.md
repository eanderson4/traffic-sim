# Analysis Methods: Trajectory Datasets & Overhead Analysis

> Source: web research (no code yet — these are the methods our analytics tooling
> must implement to "do our own calc" on real overhead data) | Researched: 2026-07-15

## Edie's Generalized Definitions (trajectories → q, k, u)

Edie (1963): for ANY space–time region A with area |A|:

```
flow     q = Σ distance traveled by all vehicles in A / |A|
density  k = Σ time spent by all vehicles in A / |A|
speed    u = q/k = Σ distance / Σ time
```

Holds for arbitrary regions, makes q = k·u an identity, and is the accepted standard
for 100%-sampled trajectory data. This is THE core primitive for both real-data
analysis and our sim's observability layer — same code path for both.
([ISTS 2018 exposition](https://www.cee.ehime-u.ac.jp/~keikaku/ists18/pdf/ISTS_IWTDCS_2018_paper_58.pdf);
[arXiv:2507.09648](https://arxiv.org/pdf/2507.09648)). Network extension (3D
trajectories → MFD): [Saberi et al.](https://www.researchgate.net/publication/264824575_Estimating_Network_Fundamental_Diagram_Using_Three-Dimensional_Vehicle_Trajectories_Extending_Edie's_Definitions_of_Traffic_Flow_Variables_to_Networks) — applies to pNEUMA.

## Fundamental Diagrams from Trajectories

- 2025 automated method + **open-source tool**: find near-stationary states using
  **parallelogram space–time cells** (edges aligned with wave speeds), apply Edie per
  cell → clean FD scatter ([arXiv:2507.09648](https://arxiv.org/abs/2507.09648)).
- Why parallelograms: rectangular cells mix states across wave fronts; slanted cells
  don't ([rect vs parallelogram analysis](https://www.researchgate.net/publication/316167014_Constructing_spatiotemporal_speed_contour_diagrams_using_rectangular_or_non-rectangular_parallelogram_cells)).
- Probe/partial-trajectory FD estimation if subsampled: [arXiv:1804.05927](https://arxiv.org/pdf/1804.05927).

## x–t Heatmaps and Wave-Speed Estimation

Workflow: Edie speed field on an x–t grid → smooth → congestion stripes ARE the
waves; **wave speed = slope of the stripes**. NGSIM I-80 published measurements:
~11 mph (~18 km/h) backward ([figure](https://www.researchgate.net/figure/Vehicle-Trajectories-and-Shockwave-Speeds-NGSIM-I-80-Dataset-515-530-pm_fig5_253891815);
pipeline example [arXiv:2303.02311](https://arxiv.org/pdf/2303.02311);
[NGSIM US-101 tutorial walkthrough](https://medium.com/@saifulamin.buet/next-generation-simulation-ngsim-trajectory-dataset-time-space-diagram-spatiotemporal-e6c484c7f128)).
At scale: automated wave identification via critical-speed thresholding + graph
components on x–t (I-24 MOTION method, [arXiv:2409.00326](https://arxiv.org/abs/2409.00326)).

## Adaptive Smoothing Method (Treiber & Helbing 2002)

Reconstructs a continuous speed field from sparse/noisy data by smoothing **along
characteristics**: downstream ≈ +80 km/h in free flow, upstream ≈ **−15 km/h in
congestion**; two anisotropic low-pass filters (typical kernel widths τ ≈ 1 min,
σ ≈ 0.6 km — ⚠ exact defaults vary by publication, verify against the paper before
implementing) blended by an s-shaped regime-detecting weight.
([arXiv:cond-mat/0210050](https://arxiv.org/abs/cond-mat/0210050);
fast implementation [Treiber PDF](https://www.mtreiber.de/publications/ASMfast_submission.pdf);
empirical −15 km/h basis [arXiv:cond-mat/0408138](https://arxiv.org/pdf/cond-mat/0408138)).
Modern alternative: anisotropic Gaussian-process state estimation
([arXiv:2303.02311](https://arxiv.org/pdf/2303.02311)).

## DIY Overhead Capture

### Fixed vantage (legally simplest — no FAA)

Overpass, parking-garage roof, hillside: free, unlimited duration, no battery limit.
Trade-off: oblique geometry needs homography correction and suffers occlusion — the
documented NGSIM error sources. Precedent: NGSIM (buildings), Zen (light poles),
I-24 MOTION (110-ft poles), TGSIM (infrastructure video).

### Drone capture (US, FAA Part 107)

- Non-recreational use (research, monetized YouTube) requires a **Part 107 Remote
  Pilot Certificate** ([FAA](https://www.faa.gov/uas/commercial_operators)).
- Baseline limits: ≤400 ft AGL, visual line of sight, airspace authorization in
  controlled airspace ([14 CFR Part 107](https://www.ecfr.gov/current/title-14/chapter-I/subchapter-F/part-107)).
- **Key constraint — §107.145 (operations over moving vehicles):** no *sustained*
  flight over moving vehicles (hovering above, circling, back-and-forth) without
  Category 1–4 compliance or waiver; a brief one-time transit is OK. The lawful
  pattern: hover beside the roadway / over median or adjacent land with an oblique
  view ([14 CFR §107.145](https://www.law.cornell.edu/cfr/text/14/107.145);
  [interpretation](https://jrupprechtlaw.com/section-107-39-operation-human-beings/)).
- Battery reality: ~25–30 min/flight — why pNEUMA/Songdo used swarms and 30-min
  session windows.
- Capture technique (from pNEUMA's processors): stable high hover, camera straight
  down, locked exposure ([DataFromSky guide](https://datafromsky.com/news/how-to-capture-a-perfect-drone-video-for-your-ultimate-traffic-survey/)).

### Video → trajectories pipelines

- **geo-trax (MIT license)** — the most complete free pipeline: detection (YOLOv8s
  trained on 19k aerial images, 0.951 mAP@50) → tracking (BoT-SORT; ByteTrack etc.
  options) → stabilization (Stabilo homography) → georeferencing → CSV with lat/lon,
  speed, accel, lane. Validated: 10 drones, 20 intersections, ~700k trajectories
  (Songdo). *TR-C* 2025. ([github](https://github.com/rfonod/geo-trax))
- pNEUMA noise-treatment code: [github](https://github.com/vishalmhjn/pneuma_treatment)
- Commercial zero-effort: DataFromSky TrafficSurvey (processed pNEUMA)
  ([site](https://datafromsky.com/trafficsurvey/))
- Accuracy expectations: fine-tuned aerial detector + stabilized high hover can reach
  highD-class (<10 cm) positioning; generic COCO model without stabilization gives
  jitter that makes acceleration unusable — same lesson as NGSIM. Smooth before
  differentiating; prefer Edie quantities which integrate rather than differentiate.
