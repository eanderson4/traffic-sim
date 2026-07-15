# Synthesis: Traffic Flow Models (Microscopic)

> Researched: 2026-07-15 | Git HEAD: eed427a | Status: complete

## Summary

This topic picks the engine's driving models and confirms ADR-0005's tick.
Bottom line: **IDM-family car-following (with the IIDM/ACC fixes) + ballistic
integration at the 100 ms tick + MOBIL-core lane changing under a SUMO-style
strategic layer + SUMO's foes/response junction factoring** — with Newell as
the built-in validation-oracle controller and the Sugiyama ring as the
acceptance test for string instability.

## Source Files

- [Model formulations & numerics](./implementation.md)
- [Simulator implementations survey](./competitors.md)
- [HCM values, patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions

### 1. Tick length: 100 ms VALIDATED (closes ADR-0005's open parameter)
**Finding:** Kesting & Treiber show Δt = 0.1 s reproduces the exact continuous
dynamics (indistinguishable from Δt = 0.01 s); a finite update time acts like a
reaction time T′_eff ≈ Δt/2; stability boundary ≈ Δt + 2T′ = 2 s for reference
IDM. SUMO warns against steps above 1 s.
**Decision:** keep 100 ms as the validated default; 0.2–0.5 s is a defensible
performance fallback; never above 1 s. Model human reaction time explicitly
(per-driver delayed inputs / action-step cadence), not via a fat tick.

### 2. Integrator: ballistic + stopping override
**Finding:** lane changes make every scheme order-1 and RK4 is empirically
*worst* per unit cost; ballistic costs the same as Euler with ~30% of its
error (Treiber & Kanagaraj §4.5/§5).
**Decision:** ballistic update (v += hA; x += hv + ½h²A), one accel eval per
vehicle-tick, plus the mandatory stop override (v+hA<0 ⇒ x += −v²/2A, v=0).
Replay-friendly: no adaptive stepping, branch-light float math.

### 3. Car-following: IDM family as default, Newell as oracle, Krauss noted
**Choice:** IDM with IIDM/ACC cut-in fixes and a 9 m/s² emergency cap as the
default AI-driver model; parameters per vehicle class with per-driver
heterogeneity (speedFactor-style distributions).
**Why:** physically interpretable parameters (a is the stability lever we'll
tune to author congestion scenarios); reproduces breakdown/hysteresis/
stop-and-go in known parameter regions; best-in-class NGSIM calibration
robustness (11–29% typical error).
**Newell** (x_i(t) = x_{i-1}(t−τ) − δ) ships as a controller variant: it IS
LWR-triangular, so a Newell platoon must match the LTM/LWR oracle to machine
precision — the bridge test to [[domain-macroscopic-flow-models]].
**Trade-off:** Krauss (SUMO default) gets breakdown from noise (σ-dawdling)
rather than deterministic instability — less controllable for scenario
authoring; Wiedemann rejected (opaque, proprietary-calibrated).

### 4. String instability is a feature we must be able to dial
**Finding:** long-wavelength instability (small a ≈ 0.3–0.5 m/s²) is the
phantom-jam mechanism; a ≥ 1 m/s² is stable. Scaling law transposes parameter
sets between speed regimes.
**Decision:** the **Sugiyama acceptance test** — 22 vehicles on a 230 m ring
must spontaneously jam with a backward cluster at ~20 km/h (and 21 must not) —
becomes an engine CI scenario. This tests exactly what LWR can't produce.
⚠ quantitative OV parameters need the Nakayama 2016 browser read first.

### 5. Lane changing: MOBIL core + strategic layer (the real engineering)
**Choice:** MOBIL safety (ã_n ≥ −b_safe) and incentive (politeness p) as the
operational layer; above it a SUMO-LC2013-style strategic layer: bestLanes /
dead-lane urgency from lane-level routes, cooperation, accumulator-based
speed-gain and keep-right.
**Why:** MOBIL alone cannot reach turn lanes; SUMO's history shows the
strategic/deadlock machinery is where the effort goes (their rewrite cut
wrong-lane teleports 845→7).
**Architecture note:** cooperation ("open a gap") is naturally an intent over
NATS, but its round trip spans ≥1 tick vs SUMO's in-process same-step
resolution — merge-throughput impact needs a simulation experiment.

### 6. Intersections: right-of-way as engine-owned data; policy at the controller
**Choice:** SUMO's factoring — `foes` (immutable conflict geometry per
movement pair, annotated crossing/merging) + `yields` (policy from junction
type/signal/ranks) — as part of the map contract on NATS (ADR territory).
Stop lines: FIFO by arrival tick with explicit deterministic tie-breaks
(arrival tick → rightmost approach → stable ID hash).
Gap acceptance (t_c/t_f distributions + impatience decay) is controller
policy; the engine backstop is acceleration-based (no foe forced beyond
b_safe) — one invariant for both lane changes and junction entry.
**Validation targets:** HCM TWSC t_c table, NCHRP/HCM roundabout capacity
curves (envelope) + rounD trajectories (behavior), AWSC 3.9–7.0 s emergent
departure headways.

### 7. No teleporting — prevention, telemetry, physical resolutions
SUMO's 300 s teleport escape hatch corrupts metrics and is impossible in
multiplayer. We adopt: LC2013's prevention mechanisms; SUMO's blocking
taxonomy (wrong lane/yield/jam/blocked) republished as NATS deadlock-telemetry
events; last resorts stay physical (drive-around after long wait, reroute,
despawn — never mid-map jumps).

## Compare/Contrast: Us vs the Field

| Dimension | SUMO | Vissim | Aimsun | us (proposed) |
|---|---|---|---|---|
| Car-following | Krauss (+IDM et al.) | Wiedemann 74/99 | Gipps-based | IDM family + fixes; Newell oracle |
| Breakdown mechanism | σ-noise | threshold oscillation | b̂ mismatch | tunable string instability (a) |
| Lane change | LC2013 4-layer | rule-based | gap-based | MOBIL core + LC2013-style strategic |
| Junction data | foes/response bitmaps | conflict areas/priority rules | give-way margins | foes/yields as NATS map contract |
| Anti-starvation | impatience 0→1/180 s | — | margin decay | controller-side impatience decay |
| Deadlock cure | teleport after 300 s | — | — | prevention + telemetry + physical only |
| Integrator | Euler | — | — | ballistic + stop override |
| Tick | 1 s default | 100 ms typical | 0.1–1.5 s | 100 ms (validated) |

## Open Questions

Consolidated in [standards-and-patterns.md](./standards-and-patterns.md#open-questions);
the ones needing decisions soon: gap convention (minGap vs s0) → vehicle-model
ADR; b_safe vs time-gap engine enforcement → benchmark; tie-break rule → ADR;
IIDM/ACC exact equations → book/movsim transcription before coding.

## Connections to Other Topics

- **Closes:** ADR-0005's provisional tick (100 ms validated).
- **Feeds:** [[concept-vehicle-controller-interface]] (which side owns gap
  policy vs safety backstop; per-driver parameter distributions),
  [[arch-road-graph-model]] (lane-level successor connectivity required by
  strategic lane changing; movement/conflict-point data),
  [[domain-signal-control]] (signal state overlays the yields mask),
  [[domain-congestion-metrics]] (stop override + arrival ticks make queue/delay
  well-defined).
- **Depends on:** [[domain-macroscopic-flow-models]] (Newell↔LWR oracle),
  [[domain-trajectory-datasets]] (NGSIM calibration, rounD gap distributions,
  Sugiyama ring test).
- **Leaves door open:** pedestrians as non-lane conflict participants in the
  foes model; transit as vehicles + scheduled dwells.
