# Datasets Catalog: Trajectory Datasets & Overhead Analysis

> Source: web research | Researched: 2026-07-15
> "Competitors" here = candidate real-world datasets, compared on access, coverage,
> and wave-visibility for our analysis/validation goals.

## NGSIM (FHWA, 2005–06) — the de facto standard, with known noise

- 4 sites: I-80 Emeryville, US-101 LA (freeway); Lankershim, Peachtree (arterial).
  Synchronized cameras on tall buildings; 10 Hz; ~500–640 m sections, <1 h each.
  [data.gov catalog](https://catalog.data.gov/dataset/next-generation-simulation-ngsim-vehicle-trajectories-and-supporting-data)
- **Access: free, no registration, CC BY-SA 3.0.** CSV: vehicle ID, frame, coords,
  speed, accel, lane, leader/follower, headways. **Raw overhead video is also
  public** ([I-80 video](https://data.transportation.gov/Automobiles/Next-Generation-Simulation-NGSIM-Program-I-80-Vide/2577-gpny),
  [US-101 video](https://data.transportation.gov/Automobiles/Next-Generation-Simulation-NGSIM-Program-US-101-Vi/4qzi-thur)) — episode-usable footage.
- **Wave content:** I-80 4:00–4:15pm and 5:00–5:30pm sets are congested; published
  x–t plots show shockwaves at ≈11 mph (~18 km/h)
  ([example figure](https://www.researchgate.net/figure/Vehicle-Trajectories-and-Shockwave-Speeds-NGSIM-I-80-Dataset-515-530-pm_fig5_253891815)).
- **⚠ Known errors:** Coifman & Li 2017 (*TR-B*, [OSU PDF](https://ceg.osu.edu/sites/default/files/2022-06/Coifman_and_Li_2017.pdf))
  re-extracted from original video: piecewise-constant speeds with bursty fake
  accelerations, trajectories overrunning stopped leaders, and missed vehicles
  (⚠ reported as 236 vehicles ≈ 11% in one camera — audit could not re-verify this
  specific figure; check against the paper before quoting) — "beyond anything that
  could be corrected strictly through cleaning."
  Do NOT trust raw accelerations. Edie-based macro quantities are much more robust.
  Corrected versions: Montanino & Punzo reconstruction ([TRR 2013](https://journals.sagepub.com/doi/10.3141/2390-11)).

## TGSIM (FHWA, 2024) — NGSIM's successor, public domain

- Fixed + moving aerial videography + infrastructure video: I-90/I-94 Chicago,
  I-294 Hinsdale, I-395 DC + GWU Foggy Bottom campus; includes L2-automated vs
  human vehicles.
  [ROSA-P](https://rosap.ntl.bts.gov/view/dot/74647), [data.gov](https://catalog.data.gov/dataset/third-generation-simulation-data-tgsim)
- **Access: CC0 (public domain)** — most permissive possible.

## levelXdata drone family (RWTH Aachen / fka GmbH, Germany)

- **highD** (highways): 110,500 vehicles, 147 h, 6 sites, 25 Hz, positioning error
  "typically <10 cm"; mostly free-flow ~420 m segments — check per-recording
  metadata for congestion ([site](https://levelxdata.com/highd-dataset/), [paper](https://arxiv.org/abs/1810.05642))
- **exiD** (highway entries/exits): 69,172 road users, >16 h ([site](https://levelxdata.com/exid-dataset/))
- **rounD** (3 roundabouts): >13,746 road users ([site](https://levelxdata.com/round-dataset))
- **inD** (4 urban intersections): >13,500 road users incl. pedestrians/cyclists ([paper](https://arxiv.org/abs/1911.07602))
- **uniD** (campus): pedestrian-heavy ([site](https://www.unid-dataset.com/))
- **Access ⚠:** free for **non-commercial** use via reviewed application (name,
  address, detailed use case); no redistribution; monetized YouTube is a judgment
  call — commercial license via levelxdata@fka.de. We could publish plots/aggregates
  but not repost files.
- **These are our intersection/roundabout validation targets** — every intersection
  type in VISION has a matching drone dataset here.

## pNEUMA (EPFL, Athens 2018) — most permissive drone dataset

- **10-drone swarm** over Athens CBD: ~1.3 km², >100 km-lanes, ~100 intersections,
  ~500k trajectories; 4 days × six 30-min windows; 25 FPS.
  ([about](https://open-traffic.epfl.ch/index.php/about/), [downloads](https://open-traffic.epfl.ch/index.php/downloads/))
- **Access: instant download, CC BY 4.0** — we CAN redistribute derived data with
  attribution. CSV per region/date/window.
- Noise treatment paper + code: [IEEE T-ITS 2023](https://ieeexplore.ieee.org/document/10113478/),
  [github](https://github.com/vishalmhjn/pneuma_treatment)
- Caveat: urban signalized network — great for network analysis/MFD, signal queues
  dominate over spontaneous highway waves.

## Zen Traffic Data (Hanshin Expressway, Japan)

- Light-pole cameras, **complete trajectories over ~2 km × 1 hour** sections at
  0.1 s — long enough to watch waves propagate kilometers (NGSIM is only ~500 m).
  ([outline](https://zen-traffic-data.net/english/outline/), [datasets](https://zen-traffic-data.net/english/outline/dataset.html))
- **Access ⚠: organizations only** (university/corporation, org-domain email,
  2-year term); personal applications rejected ([FAQ](https://zen-traffic-data.net/english/faq/)).
  Likely inaccessible to us without institutional affiliation.

## I-24 MOTION (Vanderbilt/TDOT, Nashville) — best wave dataset in existence

- 40 poles (110–135 ft) carrying ultra-HD cameras over **4.2 miles of I-24**
  (276 cameras in the 2023 paper, 294 on the current site — system expanded);
  ~230M vehicle-miles/year ([about](https://i24motion.org/about), [paper](https://arxiv.org/pdf/2301.11198))
- **Access: free registration; explicitly OK for academic AND commercial work.**
- The INCEPTION dataset powered automatic detection of hundreds of stop-and-go waves
  — generation, propagation, merging, **bifurcation** — "at a scale that has never
  been observed before," with a public wave-topology gallery
  ([arXiv:2409.00326](https://arxiv.org/abs/2409.00326)).
- Known issue: raw trajectories fragmented/noisy; "virtual trajectories" tooling
  addresses it ([arXiv:2311.10888](https://arxiv.org/abs/2311.10888)).

## AUTOMATUM (German highways)

- ~30 h drone video, 12 scenes; speed error <0.2% validated with reference vehicles;
  **CC BY-ND, no application review** ([paper](https://ieeexplore.ieee.org/document/9575442/)).

## CitySim (UCF)

- 19 h drone video, 12 locations incl. freeway weaving + signalized/stop/uncontrolled
  intersections; oriented bounding boxes; **includes SUMO and CARLA digital-twin
  networks per site** (useful precedent for scenario building). Access via request
  form ([github](https://github.com/UCF-SST-Lab/UCF-SST-CitySim1-Dataset), [paper](https://arxiv.org/pdf/2208.11036)).

## Ring-road experiments (controlled phantom jams)

- **Sugiyama et al. 2008**, "Traffic jams without bottlenecks," *NJP* 10:033001,
  **open access with overhead movies**: 230 m circular track, 30 km/h target, jam
  emerges spontaneously at ≥22 cars — critical density observed live
  ([paper+movies](https://iopscience.iop.org/article/10.1088/1367-2630/10/3/033001/pdf),
  [YouTube clip](https://www.youtube.com/watch?v=7wm-pZp_mi0),
  [analysis walkthrough](https://pjossenbruggen.github.io/cartools/Ring-Road.html))
- **Tadaki et al. 2013** (*NJP* 15:103034, open): Nagoya Dome indoor circuit, laser
  tracking, metastability at intermediate density ([paper](https://iopscience.iop.org/article/10.1088/1367-2630/15/10/103034))
- **Nakayama et al. 2016** (*NJP* 18:043040): fits the optimal-velocity model to the
  circuit data — **direct template for calibrating our engine against ring data**
  ([paper](https://iopscience.iop.org/article/10.1088/1367-2630/18/4/043040))
- **Stern et al. 2018**: 22-vehicle Arizona ring, ONE autonomously-controlled car
  damps the wave — 40% fuel reduction, 15% throughput gain. **Open data**:
  [2016 experiment DOI](https://doi.org/10.15695/vudata.cee.1),
  [ARED 2018 DOI](https://doi.org/10.15695/vudata.cee.2)
  ([arXiv:1705.01693](https://arxiv.org/abs/1705.01693), [datasets page](https://phantomjams.github.io/datasets/))

## Historical: Treiterer & Myers 1974

- Helicopter at 3,000–4,000 ft over Columbus OH, 1 s frame interval, manual
  extraction — the original phantom-jam time-space diagram. Original report:
  Treiterer, OSU report PB 246 094 (1975). The canonical episode-history figure.

## Skip: Waymo/Argoverse

Ego-vehicle sensor datasets, not overhead — wrong geometry for x–t wave analysis.

## Positioning Summary

| Dataset | Type | Wave content | Access | Redistribution |
|---|---|---|---|---|
| NGSIM I-80/US-101 | building cameras, freeway | yes (congested sets) | free, instant | CC BY-SA 3.0 |
| TGSIM | aerial, freeway | check | free, instant | **CC0** |
| I-24 MOTION | pole cameras, 4.2 mi freeway | **best in existence** | free registration, commercial OK | per terms |
| pNEUMA | drone swarm, urban network | signal queues | free, instant | **CC BY 4.0** |
| highD/inD/rounD/exiD | drone, DE | some (highD); intersections/roundabouts | reviewed application, non-commercial | no |
| AUTOMATUM | drone, DE highways | some | free | CC BY-ND |
| Zen Traffic Data | pole cameras, 2 km × 1 h | yes | orgs only ⚠ | no |
| Sugiyama/Stern rings | overhead video, controlled | **pure phantom jams** | open | open |
