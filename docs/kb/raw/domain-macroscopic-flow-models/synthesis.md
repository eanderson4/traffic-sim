# Synthesis: Macroscopic Flow Models

> Researched: 2026-07-15 | Git HEAD: 071f5ad | Status: complete

## Summary

Macroscopic traffic theory (LWR and descendants) treats traffic as a compressible
fluid governed by one conservation law plus a fundamental-diagram closure; it has
closed-form wave solutions, a 70-year empirical literature, and exact fast numerics
(CTM/LTM). For this project it is **not the engine** — it is the engine's *examiner,
calibrator, and analytics language*. The field survey found no Go/Rust CTM/LTM
implementation anywhere: our validation/preview tooling is itself publishable OSS.

## Source Files

- [Model formulation & mathematics](./implementation.md) — LWR, FDs, waves, worked examples, micro↔macro bridge, CTM/LTM numerics
- [Competitor analysis](./competitors.md) — SUMO meso, MATSim qsim, OTM, UXsim, Aimsun/PTV/DynusT, small OSS repos
- [Standards & patterns](./standards-and-patterns.md) — LWR limitations, second-order controversy, empirical stylized facts, node-model theory, calibration, visualization traditions

## Key Architectural Decisions (proposed division of labor)

### 1. Engine stays microscopic; LWR is the validation oracle
**Choice:** car-following micro engine (per VISION); LWR/CTM/LTM used to *test* it.
**Why:** LWR has closed-form answers (Rankine–Hugoniot shock speeds, cumulative-curve
delays) — analytic ground truth for the engine's *emergent* behavior, not just units.
Newell's car-following model is *exactly* LWR with a triangular FD, so implemented as
a reference controller it should match an LTM run to near machine precision.
**Trade-off:** oracle only valid in regimes where LWR is valid (see decision 5).
**Field context:** SUMO-meso's failure to respect LWR produced systematically wrong
congestion ([arXiv:2606.09282](https://arxiv.org/abs/2606.09282)) — the negative
control that proves the oracle matters.

### 2. Calibration runs the micro↔macro bridge in reverse
**Choice:** specify target macro quantities (capacity ~2000 veh/h/lane, backward wave
15–20 km/h, jam density) and *solve* for car-following parameters (time headway T ⇒
capacity, minimum gap s₀ ⇒ jam density), rather than hand-tuning until it "looks right."
**Why:** a car-following model's steady state implies an FD (IDM: s = s₀ + vT);
the mapping is analytic. Cross-check identity: w = q_max/(k_j − k_c).
**Field context:** Dervisoglu et al.'s PeMS calibration pipeline is the data-side
counterpart when we ingest real detector data.

### 3. Observability speaks macroscopic
**Choice:** metrics layer computes ρ, q, u per segment from vehicle trajectories
(Edie's generalized definitions) even though the engine is microscopic.
**Why:** congestion heatmap = ρ(x,t) on the map; the standard analysis visuals
(x–t heatmaps, trajectory/Marey diagrams, oblique N-curves) are all macro quantities.
Our micro engine has *exact* trajectories — better data than the field's helicopters
and loop detectors.
**Informs:** `domain-congestion-metrics`, `integration-maplibre-realtime`.

### 4. Fast preview mode = LTM, if/when we build it
**Choice:** if a fast scenario-screening mode is built, use the Link Transmission
Model (per-link O(1) N-curves, exact for triangular FD), not CTM cells and not an
ad-hoc queue model.
**Why:** LTM is ~n× cheaper than CTM at equal accuracy and exact where CTM diffuses;
ad-hoc queue models (SUMO-meso, MATSim qsim default) demonstrably get congestion
wrong. **Trade-off:** no capacity drop / instability — screening tool, not final
arbiter; the micro engine judges the planner game.

### 5. Know where micro and macro MUST disagree
Expected divergences (not bugs): capacity drop (5–20% discharge deficit — micro yes,
LWR no), spontaneous stop-and-go oscillations (string instability — micro yes, LWR
no), FD scatter/hysteresis, merge micro-behavior. Validation asserts agreement on
shock speeds and queue trajectories; *expects* disagreement on discharge flow and
post-front oscillations.

## Compare/Contrast: Our Approach vs the Field

| Dimension | Field practice | Ours (proposed) |
|---|---|---|
| Fast network model | queue heuristics (SUMO-meso, qsim) or CTM | LTM (exact, O(1)/link) |
| Engine validation | visual/statistical comparison to data | analytic LWR oracle tests in CI |
| Driver param tuning | manual calibration | FD-targeted inversion (T, s₀ from q_max, k_j) |
| Congestion visuals | x–t heatmaps, N-curves (detector-based) | same, from exact trajectories + map heatmap |
| Language/license | C++/Java/Python, mixed licenses | Go/TS, OSS — first Go LTM/CTM |

## Validation Oracle Recipe (for the engine test suite)

1. Derive the equilibrium FD of the car-following model (analytically or ring-road
   parameter sweep); fit triangular v_f, w, k_j.
2. Canonical scenarios: red light on homogeneous road; lane-drop bottleneck; on-ramp
   merge below/above capacity.
3. Assert: queue-tail (stop-wave) speed and start-wave speed match Rankine–Hugoniot
   within tolerance; total delay matches cumulative-curve area; Newell-controller run
   matches LTM near-exactly.
4. Expect and don't assert: capacity drop, oscillations (those get their own
   empirical-plausibility checks instead, e.g. discharge deficit within 5–20%).

## Open Questions

- Cause of capacity drop — unresolved in the literature itself.
- Which FD to default per road class — resolve the jam-density discrepancy
  (100–150 vs 180–200 veh/km/lane) against HCM/Treiber & Kesting.
- Exact Daganzo 1995 merge mid()-formula and Yperman LTM discrete equations —
  reconstructed from secondary sources; verify against primary texts before
  implementing (flagged ⚠ in the raw files).
- Whether preview-mode LTM is worth building before the episode, or post-episode.
- Time model interaction: Aimsun-meso is pure discrete-event — relevant precedent
  for `arch-time-model` (ADR-0005).

## Connections to Other Topics

- **Relates to:** `domain-traffic-flow-models` (micro side of the bridge; FD implied
  by car-following), `domain-congestion-metrics` (macro quantities ARE the metrics)
- **Depends on:** nothing — this is foundational domain theory
- **Informs:** `arch-time-model` (DES precedent, faster-than-realtime batch runs),
  `arch-road-graph-model` (GMNS format seen in DTALite; link/node model interfaces),
  `integration-maplibre-realtime` (x–t heatmaps, map congestion coloring),
  `concept-vehicle-controller-interface` (Newell reference controller),
  future `domain-trajectory-datasets` (overhead/aerial data for our own wave analysis)
