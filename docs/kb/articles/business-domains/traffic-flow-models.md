# Traffic Flow Models (Microscopic)

> The engine's driving-model stack: IDM-family car-following + ballistic integration at 100 ms + MOBIL lane changing under a strategic layer, with Newell as validation oracle.

## Overview

Microscopic traffic flow models decide how each vehicle accelerates, follows, and
changes lanes every tick. They are the physical realism of the whole engine: get them
wrong and congestion metrics, upgrade comparisons, and the planner game are all
meaningless. This topic selected the model stack and the validation oracles that
prove it works.

The research converged on the IDM (Intelligent Driver Model) family for car-following
because its parameters are physically interpretable and its string-instability regions
reproduce real phantom jams, hysteresis, and stop-and-go waves. Numerically, a
ballistic integrator with a mandatory stop override at the validated 100 ms tick is
optimal — higher-order schemes buy nothing once lane changes make every scheme
order-1. Lane changing is MOBIL's acceleration-based core wrapped in a
SUMO-LC2013-style strategic layer, and junction right-of-way is factored as
engine-owned conflict data (foes) plus controller-side yield policy.

Several positions from this research have been ratified: the 100 ms tick by
[ADR-0005](../../decisions/ADR-0005-time-model.md); the gap convention, multi-class
parameters, and IDM+MOBIL as the default dynamics shipped by the external
default-driver fleet by [ADR-0007](../../decisions/ADR-0007-vehicle-model.md) and
[ADR-0008](../../decisions/ADR-0008-controller-contract.md) — the engine itself
contains zero driving logic, owning only state authority and the safety backstop.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| IDM + IIDM/ACC fixes | `raw/domain-traffic-flow-models/implementation.md` §1 | Default car-following model; `a` is the string-stability dial |
| Ballistic integrator + stop override | `implementation.md` §8; [ADR-0005](../../decisions/ADR-0005-time-model.md) | One accel eval/vehicle-tick at ~30% of Euler's error; exact stops for queue metrics |
| Newell oracle controller | `implementation.md` §4 | Trajectory-translation model that must match LTM/LWR macro to machine precision |
| MOBIL lane changing | `implementation.md` §9; [ADR-0007](../../decisions/ADR-0007-vehicle-model.md) | Acceleration-based safety (`ã_n ≥ −b_safe`) + incentive with politeness |
| Strategic LC layer (LC2013-style) | `raw/domain-traffic-flow-models/competitors.md` §SUMO | bestLanes / dead-lane urgency / cooperation so vehicles reach turn lanes |
| foes/yields junction factoring | `competitors.md` §SUMO; `standards-and-patterns.md` | Right-of-way as engine-owned map data + recomputable policy overlay |
| Gap acceptance + impatience decay | `implementation.md` §10; `standards-and-patterns.md` | Controller-side t_c/t_f policy with starvation-proof waiting-time decay |
| Sugiyama ring acceptance test | `implementation.md` §7 | CI scenario: 22 vehicles on a 230 m ring must spontaneously jam; 21 must not |
| HCM/NCHRP validation targets | `standards-and-patterns.md` | TWSC t_c table, roundabout capacity curves, AWSC 3.9–7.0 s headways |
| No-teleport policy | `competitors.md` §SUMO; `standards-and-patterns.md` | Prevention + NATS deadlock telemetry + physical-only last resorts |

## How It Works

### Car-following: IDM family as the default dynamics

`dv/dt = a[1 − (v/v0)^δ − (s*/s)²]` with `s* = max[s0 + vT + v·Δv/(2√(ab)), 0]`.
Reference car parameters: v0 ≈ 120 km/h (trucks 80), T = 1.5 s, s0 = 2 m,
a = 0.73–1.0 m/s², b = 1.67–2.0 m/s², δ = 4. Two known pathologies carry mandatory
fixes (ratified as part of the ADR-0007 default dynamics):

1. **Cut-in overreaction** — (s*/s)² explodes when a lane changer drops in close;
   plain IDM emergency-brakes where a relaxed response is realistic. Fix: IIDM
   two-regime reformulation, or the ACC model (IIDM + constant-acceleration
   heuristic), plus a 9 m/s² emergency cap (physical dry-road limit). Canonical
   equations must be transcribed from Treiber & Kesting Ch. 11 or movsim before
   coding (see Open Questions).
2. **Negative speeds at finite steps** — handled by the integrator's stop override (below).

Why IDM over the field: Krauss (SUMO's default) gets breakdown from σ-dawdling noise
rather than deterministic instability — less controllable when authoring congestion
scenarios; Wiedemann (Vissim) has ~10 opaque constants and proprietary calibration
heritage; FVDM calibrates worse than IDM on NGSIM. IDM's calibration is best-in-class:
11–29% typical NGSIM error, T the most influential parameter, intra-driver variability
dominating inter-driver. Parameters vary per vehicle class with per-driver
heterogeneity (speedFactor-style distributions), sampled from per-vehicle seeded RNG
(ADR-0007).

**String instability is a feature we dial.** Long-wavelength instability at small
a ≈ 0.3–0.5 m/s² is the phantom-jam mechanism; a ≥ 1 m/s² is stable for small
reaction times. The **Sugiyama acceptance test** — 22 vehicles on a 230 m ring at
~30 km/h must spontaneously form a jam cluster propagating backward at ~20 km/h, and
21 vehicles must not — becomes an engine CI scenario. A scaling law (rescale time by
T, space by v0·T) transposes parameter sets between speed regimes.

**Newell ships as the validation-oracle controller**: `x_i(t) = min{x_i(t−τ) + u·τ,
x_{i−1}(t−τ) − δ}` with τ = 1/(w·k_j), δ = 1/k_j is *exactly* LWR with a triangular
fundamental diagram — so a Newell platoon must match the LTM/LWR macroscopic oracle
to machine precision. That is the bridge test to the macro layer (see Related).

### Numerics: ballistic integration at the validated 100 ms tick

Per Treiber & Kanagaraj: lane changes make **all** schemes order-1 and RK4 is
empirically *worst* per unit cost; ballistic (`v += hA; x += h·v + ½h²A`) costs the
same single accel evaluation as Euler with ~30% of its error. The stop override is
mandatory: if `v + hA < 0`, then `x += −v²/(2A)`, `v = 0` — it raises consistency
order at stops 1→2 and makes queue/delay metrics well-defined. No adaptive stepping,
branch-light float math — replay-friendly.

Kesting & Treiber directly validate ADR-0005's tick: Δt = 0.1 s reproduces the exact
continuous dynamics (indistinguishable from Δt = 0.01 s). A finite update time acts
like reaction time T′_eff ≈ Δt/2; the stability boundary for reference IDM is
≈ Δt + 2T′ = 2 s. 0.2–0.5 s is a defensible performance fallback; never above 1 s
(SUMO warns models are untested there). Human reaction time is modeled explicitly —
per-driver delayed inputs, SUMO-actionStepLength-style decision cadence — never by
fattening the tick.

### Lane changing: MOBIL core + strategic layer

MOBIL expresses both safety and incentive as car-following accelerations, so any
a(s,v,Δv) model plugs in and collision-freedom is inherited: safety `ã_n ≥ −b_safe`
(b_safe = 4 m/s²), incentive `ã_c − a_c + p·[(ã_n − a_n) + (ã_o − a_o)] > Δa_th`
(politeness p realistically ∈ (0, 0.5], threshold Δa_th = 0.1 m/s², European
keep-right bias Δa_bias = 0.3 m/s²); results are Δt-insensitive — fixed-tick friendly.

MOBIL alone cannot reach turn lanes — it is purely operational. Above it sits a
SUMO-LC2013-style strategic layer, because SUMO's history shows this is where the
real engineering goes (their DK2008→LC2013 rewrite cut wrong-lane teleports 845→7
and jam teleports 464→9, and Braunschweig waiting time 89.73→46.66 s): per-lane
`bestLanes`/`bestLaneOffset` from lane-level routes, dead-lane urgency
(`d − o < lookAheadSpeed × |offset| × f`, f = 10 left / 20 right), accumulator-based
speed-gain and keep-right (halve on sign mismatch to kill oscillation), and
cooperation. Under ADR-0002/ADR-0008, cooperation ("open a gap") is an intent over
NATS with ≥1-tick round trip — vs SUMO's same-step in-process resolution; the
merge-throughput impact is a scheduled experiment (Open Questions). Merges use
MOBIL's virtual standing vehicle at lane end with p = 0 for the merger.

### Intersections: right-of-way as data, policy at the controller

SUMO's factoring, adopted: `foes(a,b)` = immutable conflict geometry per movement
pair (annotated crossing/merging), computed once; `yields(a,b)` = policy from
junction type, signal state, and priority ranks, recomputable. The foes data is part
of the map contract on NATS ([ADR-0006](../../decisions/ADR-0006-nats-message-contract.md)
territory — message contracts are sacred). Stop lines queue FIFO by arrival tick with
explicit deterministic tie-breaks: arrival tick → rightmost relative approach →
stable hash of (vehicle ID, junction ID) — integers only, never floats or map
iteration order (needs its own ADR; see Open Questions).

Gap acceptance (t_c/t_f distributions + impatience decay toward any collision-safe
gap — SUMO: 0→1 over 180 s; Aimsun: initial→final safety margin) is controller
policy. The engine backstop is the same acceleration-based invariant as lane
changing: no foe forced to brake beyond b_safe. Theory blesses the decay:
heterogeneous t_c lowers capacity while impatient inconsistency raises it, and the
errors roughly cancel (Grossmann). Validation targets: HCM TWSC t_c table (4.1–7.5 s
by movement), NCHRP/HCM roundabout capacity curves (envelope; HCM 6 intercept ≈1380
veh/h, +50% over HCM 2010) plus rounD trajectories for behavior, and AWSC departure
headways 3.9–7.0 s by degree-of-conflict case — which should *emerge* from
conflict-scanning delay, not be table-driven. Also copy SUMO's in-box turn-speed cap `√(radius × 5.5)`.

### No teleporting

SUMO's 300 s teleport escape hatch corrupts conservation and local metrics and is
impossible in a multiplayer authoritative engine. Adopted instead: LC2013's
prevention mechanisms (counterLaneChange resolution, space reservation before
multi-lane mandatory changes, keep-clear boxes), SUMO's blocking taxonomy
(wrong lane / yield / jam / blocked) republished as NATS deadlock-telemetry events,
and physical-only last resorts — drive-around after long wait, reroute, despawn.
Prevention removes ~99% of teleport causes per Erdmann's numbers.

## Gotchas

- **Gap convention split**: SUMO's `minGap` sits outside vehicle length; IDM's `s0`
  is bumper-to-bumper. Mixing them in one parameter set silently corrupts every
  safety calculation. RESOLVED 2026-07-17 review: bumper-to-bumper gap is the one
  canonical semantics, position = front-bumper coordinate, jam gap `s0` with
  `length` separate, spacing-derived quantities computed never stored — pinned in
  [ADR-0007](../../decisions/ADR-0007-vehicle-model.md).
- **Comfortable-b vs capability-decel**: physics-literature b is *comfortable*
  deceleration (1.5–2 m/s²); SUMO's `decel` (4.5) is closer to capability. Never mix
  conventions in one parameter set.
- **RK4 is worse, not better**: lane-change discontinuities make all integrators
  order-1; RK4 costs 4 accel evals and loses per unit cost. Higher-order schemes buy
  nothing in a multi-lane sim.
- **Don't model reaction time with a fat tick**: Δt = 1.0 s eats most of the IDM
  stability budget (Δt + 2T′ ≈ 2 s) and reaction time destabilizes ~2× as strongly
  as update time. Keep Δt small; delay the inputs instead.
- **Roundabout lane discipline**: without forcing vehicles toward the inner lane
  until their exit, a 2-lane ring degrades to 1-lane throughput (SUMO special case,
  accepting occasional stranded vehicles).
- **MOBIL has no strategic reach**: deploy it alone and vehicles miss turn lanes —
  the strategic/deadlock machinery is the bulk of the effort, not the incentive
  formula.
- **Teleport counts are defect telemetry, not a feature**: SUMO treats them as
  numbers to drive to zero; any engine that teleports visible vehicles breaks
  conservation, metrics, and multiplayer trust.
- **Krauss's original safety equation is wrong for heterogeneous braking** (follower
  out-brakes leader) — SUMO patched it in implementation; Gipps's collision-free
  proof likewise breaks for heterogeneous fleets.
- **HCM worked examples don't always recompute** (t_c=7.1 example: stated 845 vs
  ≈822 veh/h from base values) — re-derive from full worksheets before citing in a
  calibration ADR.

## Open Questions

- **IIDM/ACC canonical equations** — transcribe from Treiber & Kesting *Traffic Flow
  Dynamics* Ch. 11 (or the movsim reference implementation) before implementing.
- **Sugiyama quantitative parameters** — Nakayama 2016 fitted OV values need a
  browser read (IOP 403s fetchers); OVM instability threshold (V′ > a/2) needs
  verification against Bando 1995; IDM analytic string-stability inequality (book
  Ch. 15) wanted for a test oracle.
- **b_safe vs time-gap engine enforcement** — acceleration-based backstop (unified
  with MOBIL) vs cheaper per-tick time-gap (SUMO `jmTimegapMinor` 1 s): decide with
  the tick-budget benchmark at engine bring-up.
- **Cooperation over NATS** — does the ≥1-tick gap-request latency change merge
  throughput vs same-step resolution? Simulate both.
- **Deterministic stop-line tie-break rule** — affects replay determinism and
  multiplayer fairness perception; needs its own ADR.
- **Emergent vs prescribed AWSC headways** — prototype conflict-scanning delay
  against HCM case numbers (3.9/4.7/5.8/7.0 s).
- **Zipper fairness at saturation** — strict 1:1 alternation vs demand-weighted.
- **Erdmann's ad-hoc constants** (urgency f = 10/20; 20/40 m space reservations) —
  calibration surface from day one.
- **rounD-derived t_c/t_f distributions** — Troutbeck maximum-likelihood estimation
  on drone data; German yield behavior ≠ US HCM values (driver-population parameter,
  maybe per map).
- **HCM 7 exhibit verification** — reportedly leaves TWSC/AWSC substantively
  unchanged; paywalled, verify in a library copy.

## Related

- [Time Model](../architecture/time-model.md) — ADR-0005's 100 ms tick was validated by this research (Δt = 0.1 s is dynamically exact).
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — which side owns gap policy vs the safety backstop; per-driver parameter distributions.
- [Road Graph Model](../architecture/road-graph-model.md) — lane-level successor connectivity and movement/conflict data required by the strategic layer and foes model.
- [Macroscopic Flow Models](../business-domains/macroscopic-flow-models.md) — the Newell platoon must match the LTM/LWR oracle to machine precision.
- [Trajectory Datasets & Overhead Analysis](../business-domains/trajectory-datasets.md) — NGSIM calibration, rounD gap distributions, and the Sugiyama ring ground truth.
- [Signal Control](../business-domains/signal-control.md) — signal state overlays the yields mask in the foes/yields factoring.

---
*Raw research: [raw/domain-traffic-flow-models/](../../raw/domain-traffic-flow-models/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
