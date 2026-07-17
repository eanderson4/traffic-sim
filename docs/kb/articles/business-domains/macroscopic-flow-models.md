# Macroscopic Flow Models

> Macroscopic (LWR) traffic theory is not our engine — it is the engine's analytic examiner, calibrator, and metrics language; no Go CTM/LTM exists, so our tooling is greenfield OSS.

## Overview

Macroscopic traffic-flow theory treats traffic as a compressible fluid: one
conservation law (∂k/∂t + ∂q/∂x = 0) closed by an equilibrium **fundamental
diagram** (FD) q = Q(k). This LWR model (Lighthill–Whitham 1955, Richards 1956)
has closed-form wave solutions, a 70-year empirical literature, and exact fast
numerics (Cell Transmission Model, Link Transmission Model). It is the standard
vocabulary of congestion analysis: density, flow, shock speed, capacity, jam
density.

For traffic-sim the research conclusion is a strict division of labor: **the
engine stays microscopic** (per [VISION](../../../VISION.md) and ADR-0007's
IDM+MOBIL default), and macroscopic theory plays three supporting roles —
**oracle** (LWR's closed-form shock speeds and cumulative-curve delays are
analytic ground truth for testing the engine's emergent behavior),
**calibrator** (target macro quantities like capacity and jam density are
inverted into car-following parameters analytically), and **metrics language**
(the congestion heatmap is ρ(x,t); x–t heatmaps, trajectory diagrams, and
cumulative N-curves are the field's standard visuals). If a fast
scenario-screening mode is ever built, it should be an exact LTM, not an ad-hoc
queue model — the field's own failure cases (SUMO-meso) prove why.

A notable finding for positioning: targeted searches found **no Go or Rust
CTM/LTM implementation anywhere**. Validation/preview tooling we build in this
space is itself publishable open source.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| LWR model & FD closures | `raw/domain-macroscopic-flow-models/implementation.md` §1, §4 | Conservation law + fundamental-diagram library (Greenshields, Smulders, triangular, Wu) |
| Shockwave mathematics | `raw/domain-macroscopic-flow-models/implementation.md` §2–5 | Rankine–Hugoniot shock speeds, entropy condition, worked red-light/bottleneck examples — the oracle's answers |
| Micro↔macro bridge | `raw/domain-macroscopic-flow-models/implementation.md` §6 | Car-following steady states imply an FD; Newell's model ≡ LWR with triangular FD |
| CTM / Godunov numerics | `raw/domain-macroscopic-flow-models/implementation.md` §7 | Demand/supply cell scheme; merge/diverge node rules; CFL cell sizing |
| Link Transmission Model | `raw/domain-macroscopic-flow-models/implementation.md` §7 | O(1)-per-link exact N-curve solver for triangular FD — the preview-mode candidate |
| Empirical stylized facts | `raw/domain-macroscopic-flow-models/standards-and-patterns.md` | Backward wave 15–20 km/h, capacity drop 5–20%, stop-and-go waves — plausibility checks |
| Node-model theory | `raw/domain-macroscopic-flow-models/standards-and-patterns.md` | Tampère requirements + invariance principle for judging junction throughput sanity |
| FD calibration practice | `raw/domain-macroscopic-flow-models/standards-and-patterns.md` | Dervisoglu/PeMS pipeline for fitting v_f, w, q_c, k_j from detector data |
| Competitor lessons | `raw/domain-macroscopic-flow-models/competitors.md` | SUMO-meso failure (LIFT fix), OTM's pluggable link models, UXsim's ~1k-LOC LTM |
| Engine/time-model decision | `decisions/ADR-0005-time-model.md` | Fixed 100 ms tick chosen; Aimsun-meso's pure-DES precedent considered and not taken |

## How It Works

### 1. Engine stays microscopic; LWR is the validation oracle

LWR's value is that it has *closed-form answers*. The Rankine–Hugoniot condition
gives the speed of any shock as the chord slope on the FD:
ω = (q₁ − q₂)/(k₁ − k₂). Verified worked examples (TU Delft Ch. 8, triangular
FD, q₁ = 2500, q_c = 5000, k_j = 250, u₀ = 100) give a red-light stop wave of
**−11.1 km/h** and start wave of **−25 km/h**; total delay is the area between
cumulative curves. These are assertions a CI test suite can make about the micro
engine's *emergent* behavior, not just its units. Ratified implicitly by
ADR-0007: the engine's IDM default embeds a smooth FD closure
(s ≈ s₀ + vT at equilibrium), so an implied FD always exists to test against.

The killer cross-check: **Newell's simplified car-following model is exactly
LWR with a triangular FD** (Newell 2002). Implemented as a reference controller
(trajectory = leader shifted by τ = 1/(w·k_j) in time, δ = 1/k_j in space), a
Newell-driven fleet should reproduce an LTM run to near machine precision. This
plugs into the controller interface per ADR-0008 — the reference controller is
just another external client emitting Intents.

The negative control that proves the oracle matters: SUMO's mesoscopic mode
(queue model, up to 100× faster) was found to violate LWR kinematic-wave theory
and to systematically *misplace* congestion (appears later, dissipates earlier
than micro-SUMO; arXiv:2606.09282). Their fix, LIFT, is a discrete-time LTM. A
fast mode that ignores wave theory produces wrong congestion — exactly what the
planner-game use case cannot tolerate.

### 2. Calibration runs the bridge in reverse

Instead of hand-tuning car-following parameters until congestion "looks right",
specify macro targets and solve for micro parameters — the mapping is analytic:

- Time headway T ⇒ capacity (target q_c ≈ 1800–2400 veh/h/lane)
- Minimum gap s₀ ⇒ jam density (target k_j ≈ 100–150 veh/km/lane, TU Delft
  calibrations; a conflicting PeMS-derived 180–200 figure remains open)
- Cross-check identity for a triangular FD: **w = q_max/(k_j − k_c)** — only
  three of {v_f, w, q_max, k_j, k_c} are free; calibrate three, derive the rest.
  Backward wave speed should land in the remarkably stable worldwide range
  **15–20 km/h**.

When real detector data is ingested later, the Dervisoglu et al. PeMS pipeline
is the canonical data-side counterpart (split by speed at 60 mph; free branch by
constrained least squares; capacity = max observed flow; congested branch fit
bounded to w ∈ 5–20 mph).

### 3. Observability speaks macroscopic

The metrics layer computes ρ, q, u per segment from exact vehicle trajectories
(Edie's generalized definitions) even though the engine is microscopic — our
micro engine has *better* data than the field's helicopters and loop detectors.
The standard visuals are the observability targets: x–t density/speed heatmaps
per corridor (waves appear as backward-sloping stripes), trajectory/Marey
diagrams per lane, oblique cumulative N-curves per bottleneck (delays are
distances between curves), plus the map-based congestion heatmap. Feeds
`domain-congestion-metrics` and `integration-maplibre-realtime`.

### 4. Fast preview mode = LTM, if built

If a scenario-screening mode is built, use the **Link Transmission Model**
(Newell 1993 → Yperman 2007): N-curves tracked only at link boundaries, O(1)
state per link vs CTM's per-cell state (~n× cheaper at equal accuracy), and
**exact** for triangular FDs with piecewise-constant boundary conditions — no
numerical diffusion. Explicitly *not* CTM cells and *not* an ad-hoc queue model
(SUMO-meso/MATSim-qsim lesson above). UXsim proves the mechanics fit in ~1k
LOC. Trade-off: no capacity drop or instability — LTM is a screening tool; the
micro engine remains the final arbiter that ranks planner-game upgrades. CTM
details that matter if cells are ever needed: cell length = v_f·Δt exactly
(CFL), Daganzo merge priority rule via mid() = median, FIFO diverge rule with
a known one-blocked-off-ramp-freezes-all pathology.

### 5. Know where micro and macro MUST disagree

Validation asserts agreement on shock speeds and queue trajectories, but
*expects* disagreement on — these are LWR limitations, not engine bugs:

- **Capacity drop**: discharge flow runs 5–20% below pre-breakdown capacity
  (empirical consensus: Hall & Agyemang-Duah ≈6%, Cassidy & Bertini 8–10%,
  Chung et al. 3–18%). Micro reproduces it; single-valued-FD LWR cannot.
- **Spontaneous stop-and-go oscillations**: string instability (micro yes —
  IDM can be string-unstable; LWR unconditionally stable, never amplifies).
- **FD scatter/hysteresis** in the congested branch (2-D cloud, not a curve).
- **Merge micro-behavior**: macro node models allocate capacity; they "do not
  consider the actual merging process between vehicles."

Divergences get their own empirical-plausibility assertions instead (e.g.
discharge deficit within 5–20%).

### 6. Time-model precedent — decided

Aimsun's meso mode is pure discrete-event simulation — a serious commercial
engine chose DES at meso scale. This was an open interaction with
`arch-time-model` in the raw research; it is now resolved by
[ADR-0005](../../decisions/ADR-0005-time-model.md): traffic-sim uses a **fixed
100 ms tick (10 Hz)** with tick count as the clock, not DES. Macro tooling
(CTM/LTM oracles, preview) runs as offline/batch computation against recorded
runs, unconstrained by the live tick.

## Gotchas

- **Fast-but-wrong congestion**: queue-based fast modes (SUMO-meso, MATSim qsim default) lack a backward wave speed and produce systematically wrong congestion timing and extent — measured, not theoretical (arXiv:2606.09282). Never ship an ad-hoc queue model as a preview.
- **The FD is not a physical law**: it shifts with road, vehicle mix, weather, lighting (rain −9% capacity, darkness −5%, combined −12%), and a roadway FD ≠ sum of its lane FDs because lane distribution shifts with flow. Treat any fitted FD as scenario-specific.
- **Congested-branch fits are unstable**: the congested FD is a 2-D scattered cloud; "a given set of data points can be used to fit quite a few different models." Triangular wins on parsimony and exact solvability, not fit quality.
- **LWR can't accelerate the lead vehicle**: entropy solutions discharge a queue at capacity speed instantaneously — no bounded acceleration. Don't assert first-vehicle exit dynamics against LWR.
- **FIFO diverge pathology (CTM)**: one blocked off-ramp (R_j = 0, β_j > 0) freezes *all* flow through a diverge, including traffic bound for empty links — right for single-lane, too harsh for multilane.
- **Second-order models are contested-but-mature**: Daganzo's 1995 "Requiem" criticisms were justified for Payne–Whitham but cured by Aw–Rascle–Zhang (no characteristic faster than traffic). Mainstream stays first-order (CTM/LTM); reach for second order only where oscillations or capacity drop must be modeled macroscopically.
- **⚠ Unverified formulas in raw files**: the Daganzo 1995 merge mid()-formula and the Yperman LTM discrete equations were reconstructed from secondary sources — verify against primary texts before implementing (flagged in `implementation.md` §7).

## Open Questions

- **Cause of capacity drop** — unresolved in the literature itself (lane changing vs bounded acceleration vs acceleration differences); our 5–20% plausibility check works around it.
- **Default FD per road class** — resolve the jam-density discrepancy (TU Delft 100–150 vs PeMS-derived 180–200 veh/km/lane) against HCM / Treiber & Kesting before hardcoding defaults.
- **Verify primary sources** — Daganzo 1995 merge formula and Yperman 2007 LTM equations (⚠ flags in `implementation.md` §7) before any CTM/LTM code is written.
- **Preview-mode LTM timing** — worth building before the episode, or post-episode? Deferred; not required by any current use case.
- **Kerner three-phase vs FD orthodoxy** — live dispute; working stance is FD orthodoxy as default, three-phase as a catalogue of phenomena to confront (2-D congested states, nucleation-like breakdown, constant jam speed).

## Related

- [Traffic Flow Models (Microscopic)](../business-domains/traffic-flow-models.md) — the micro side of the bridge; the car-following models whose steady states imply the FDs calibrated here
- [Congestion Metrics](../business-domains/congestion-metrics.md) — macro quantities (ρ, q, u, delay, N-curves) ARE the metrics layer's vocabulary
- [Simulator Landscape](../business-domains/simulator-landscape.md) — where meso/macro engines (SUMO-meso, MATSim, Aimsun, OTM, UXsim) sit and what we take from each
- [Time Model](../architecture/time-model.md) — ADR-0005's fixed-tick decision, for which Aimsun's pure-DES meso was a considered precedent
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — the Newell reference controller (oracle cross-check) plugs in as an ADR-0008 external controller
- [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) — renders the macro observability outputs: congestion heatmaps and x–t views

---
*Raw research: [raw/domain-macroscopic-flow-models](../../raw/domain-macroscopic-flow-models/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
