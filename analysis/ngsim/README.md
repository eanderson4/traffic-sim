# NGSIM I-80 wave analysis

First real-data exercise for the observability layer: download an NGSIM I-80
congested window, compute Edie's generalized q/k/u on an x–t grid, render the
speed heatmap, and measure the backward congestion-wave speed.

Method background: `docs/kb/raw/domain-trajectory-datasets/` (Edie definitions,
x–t heatmaps, ASM characteristic smoothing). Data license: CC BY-SA 3.0 (FHWA
NGSIM). We commit scripts, never data — `data/` is gitignored.

## Usage

```sh
./download-i80.sh 1700-1715        # → data/ngsim/i80-1700-1715.csv (~94 MB)
go build -o ngsim-xt .
./ngsim-xt -in ../../data/ngsim/i80-1700-1715.csv \
  -png ../../data/ngsim/i80-1700-1715-xt.png \
  -field ../../data/ngsim/i80-1700-1715-field.csv
```

Periods: `1600-1615` (lighter), `1700-1715`, `1715-1730` (congested).
`ngsim-xt -h` lists the knobs (grid size, lane filter, color scale).

## What it computes

- **Edie (1963) per x–t cell**: q = Σdistance/|A|, k = Σtime/|A|, u = q/k, from
  consecutive 0.1 s sample pairs assigned by segment midpoint. Integrals, not
  derivatives — robust to NGSIM's known position noise.
- **Heatmap PNG**: x up (direction of travel), t right, sequential blue ramp,
  dark = congested. Stop-and-go waves appear as dark stripes sloping down-right.
- **Wave speed**: scans candidate speeds c; the c whose characteristic lines
  x = x₀ + c·t minimize within-line variance of the congested speed field is the
  dominant wave speed (the Adaptive Smoothing Method insight, inverted into a
  measurement).

## Result (2026-07-15, first run)

I-80 Emeryville, 2005-04-13 5:00–5:15 pm, lanes 1–6, 1,965 vehicles,
1.52 M sample pairs, 1791 ft section, grid 25 ft × 3 s:

```
dominant congestion wave speed: -16.5 ft/s = -18.1 km/h = -11.2 mph
```

Published analyses of this same dataset report shockwaves at ≈ 11 mph
(~18 km/h), and the general literature gives −15…−20 km/h as the universal
backward wave speed — our independent Edie implementation reproduces it.

## Toward the engine

The Edie computation here is the prototype of the engine's observability
service: the same math must later consume simulated vehicle trajectories (over
NATS) so that real data and sim output flow through identical analytics
("one Edie implementation, two consumers" — see
`docs/kb/raw/domain-trajectory-datasets/synthesis.md`). When the sim can
reproduce a −15…−20 km/h wave from car-following dynamics alone, this tool is
the referee.

## Simulation validation (M2, 2026-07-17)

The M2 credibility test runs the stock M1 kernel (IDM + MOBIL-lite, 100 ms
tick, parameters untouched) on an I-80 scenario and measures the simulated
wave speed through the **same code path** as the real field (the estimator is
ported into `engine/xtfield.go`; on the real field CSV it reproduces
−16.5 ft/s = −18.1 km/h exactly).

**Scenario** (`engine/scenario_i80.go`, all derived from the raw CSV):

- Geometry: 6 lanes × 1,791 ft section (lane 1 HOV … lane 6 rightmost),
  on-ramp merging into lane 6 at y ≈ 700 ft, 65 mph limit; 600 m upstream
  runway. The on-ramp (932 veh/h) is the wave trigger.
- Demand (veh/h, upstream entries ×4): lanes 1–6 = 1664/1300/988/1056/972/688;
  ramp 932; total ≈ 7,600. Mix: 96.5% car, 3.5% truck.
- Boundary condition: the real window sits inside a spillback queue whose
  bottleneck is **downstream of the surveyed section** (data demand alone
  cannot overload 6 lanes; verified free-flowing). Represented as a 6→5 lane
  drop past the section + **two-phase demand** (1.20× during a 1,200 s
  warm-up to build the queue — the real queue was built before 17:00 — then
  data demand for the 900 s window). These are boundary conditions, not
  physics tuning.

**Result** (`go test -run TestI80StopAndGo ./...`, or
`engine/cmd/i80xt -field … -png …`; sim field `i80-sim-field.csv`,
heatmap `i80-sim-xt.png` next to the real one):

| | characteristic-scan | FD chord slope | stripes |
|---|---|---|---|
| real field | −18.1 km/h | −15.0 km/h | 5 |
| sim field  | −12.6 km/h | −11.6 km/h | 4 |

**The sim reproduces stop-and-go structure but the wave is ≈ 30% too slow —
outside the −15…−20 km/h band.** Reported honestly per the milestone's
no-physics-tuning rule; the acceptance test pins the achieved envelope and
logs the gap.

**Diagnosis.** The sim's go-state matches reality almost exactly
(q ≈ 1,380 vs 1,346 veh/h/lane, k ≈ 73 vs 67 veh/km), but its jam troughs
creep at the **IDM equilibrium speed** (≈ 3.3 m/s at k ≈ 128 veh/km) while
real troughs are sub-equilibrium (≈ 2.7 m/s, q = 775 vs 984 veh/h/lane at the
same density). The wave speed is the chord slope between those states, so a
kernel whose troughs sit on the equilibrium curve is structurally capped at
≈ −12 km/h. Real stop-and-go requires trough hysteresis (slower jam
discharge than equilibrium — e.g. an instability-capable IDM calibration:
literature highway IDM uses a ≈ 0.73–1.0, T ≈ 1.6–1.7 s vs our M1
string-stable a = 1.0, T = 1.5). That is a physics finding for the vehicle
model, not a scenario deficiency: a ±20% demand sweep and a 5-seed sweep move
the unhijacked scan readings only within ≈ −11…−15.4 km/h and the stripe
count from 0 to 5; no scenario point shows an in-band wave speed AND ≥ 2
stripes at once.

**Physics discrepancies observed (M1 known limitation, confirmed and
quantified).** Negative gaps DO trigger here: ≈ 3,000 collision observations
per reference run (min gap −11.8 m), localized at the merges under sustained
overload — the on-ramp/merge area and the 6→5 funnel (`i80-main`: ≈ 2,350)
and the funnel outflow (`i80-down`: ≈ 640). The instant-hop merge with
b_safe relaxed to 9 m/s² lets followers accept gaps they cannot hold
(closing distance at Δv > 10 m/s exceeds the 0.3 m merge buffer), and the
0.1 m gap clamp then papers over multi-metre overlaps. Side effects: the
funnel discharge caps at ≈ 1,460 veh/h/lane (vs the free 1,780), and overlap
resolution keeps jam cells creeping — plausibly contributing to the shallow
troughs above. This is the top physics debt going into M3.

**Measurement caveat.** The ported variance-scan estimator is hijacked by
mass-dominated fields (near-vertical characteristics cherry-pick uniform
trough samples) — on most sim configurations it reports ≈ 0 despite visible
stripes; the real field is stripe-dominated and unaffected. The FD chord
slope (`XTField.FDWaveSpeed`) is the robust cross-check and agrees with the
scan on both fields where the scan is unhijacked.

## Physics hardening (M3, 2026-07-17)

Both M2 defects addressed, with literature-referenced changes only.

**1. Merge gap enforcement — fixed.** Two root causes found:

- *Urgency-relaxed b_safe admits kinematically unholdable gaps.* MOBIL's
  ã_n ≥ −b_safe evaluates the smooth-IDM acceleration at the hop instant,
  but IDM is collision-free only as an ODE — under the −9 m/s² cap and
  100 ms steps, an instant hop can drop the follower into a state (0.3 m gap
  at Δv > 10 m/s) from which even max braking cannot avoid overlap.
- *Hops near lane boundaries were never gap-checked at all.* `neighbors()`
  sees only the target lane; a follower on the predecessor lane (or a leader
  on the successor) was invisible, so the leader could land with its rear
  overhanging into the predecessor lane onto an unchecked follower.

Fix (`engine/mobil.go`, `engine/engine.go`, `engine/network.go`): a
kinematic collision-freedom floor under the ballistic integrator — gap ≥
(v_f²−v_l²)/(2·b_max) + Δv·Δt with b_max = 9 m/s² (the Gipps braking branch
/ Krauss v_safe condition), which merge urgency never relaxes — plus
boundary-aware neighbor resolution (`Lane.Prevs`, `Engine.prevFollower`).
Result: **collision observations 2,994 → 0** in the I-80 reference run
(min gap −11.785 m → +0.392 m) and **28 → 0** in the lanedrop overload run
(committed as `TestLaneDropOverloadNoOverlap` + `TestKinGapOK`). Funnel
discharge recovered: 7,300 → 7,680 veh/h sustained (measured).

**2. Recalibration — the original IDM paper's highway set.** Car defaults
are now v0 = 33.3 m/s, T = 1.6 s, a = 0.73 m/s², b = 1.67 m/s², s0 = 2 m
(Treiber/Hennecke/Helbing 2000, cond-mat/0002177; KB implementation.md §1)
— the instability-capable regime M2's diagnosis called for, replacing the
string-stable CACAIE set (a = 1.0, T = 1.5). Truck keeps the coherent
convention (a = 0.7, T = 1.7, b = 1.67). This is a default-type calibration
change, not scenario tuning (ADR-0007: values are type config).

**3. Boundary condition re-anchored (not physics).** With merge losses gone,
the 6→5 funnel discharges ≈ 7,680 veh/h ≈ the 7,600 veh/h data demand, so
M2's quasi-stationary window drains the queue (one wave, then near-free
flow). The real queue GREW through the window (tail creep ≈ 4 km/h ⇒
shortfall ≈ 1,500 veh/h). The reference now sustains 1.20× data demand
throughout (shortfall 9,120 − 7,680 ≈ 1,440 veh/h — the real regime:
a slowly growing queue). Discharge measurements: 6→5 = 7,680 veh/h,
6→4 = 5,910 veh/h (solid crawl).

**Result** (`go test -run TestI80StopAndGo ./...`; sim field
`i80-sim-field.csv`, heatmap `i80-sim-xt.png` regenerated):

| | variance scan | per-wave leg median (new, robust) | FD chord | stripes | overlaps |
|---|---|---|---|---|---|
| real field | −18.1 km/h | **−15.0 km/h** | −15.0 km/h | 5 | — |
| M2 sim (a=1.0, merges broken) | −12.6 km/h | — | −11.6 km/h | 4 | 2,994 |
| M3 sim (seed 1) | −13.2 km/h | −7.5 km/h (stalled leg) | n/a (few jam cells) | 2 | **0** |
| M3 sim (seeds 1–5 range) | −13.2…−15.4 km/h | −13.7…−15.0 (seeds 2–5) | −12.3 where computable | 2 (all seeds) | **0 (all)** |

The structural ≈ −12 km/h cap is **broken**: jam troughs are now
sub-equilibrium (full stops with hysteresis), wide-jam triangles cross the
section at every seed, and the best seeds read −15.4 km/h (scan, band edge)
with per-wave leg medians matching the real field's own −15.0 km/h through
the identical estimator. The −15…−20 km/h band is **not** asserted in the
acceptance test — the reference seed does not robustly reach it; the test
pins the achieved envelope (scan −12…−16), ≥2 stripes, and zero overlaps.

**Residual gap.** By the robust per-wave estimator the sim is 0–1.3 km/h
slower than the real median (scan: 2.7–4.9 km/h slow). Traced to IDM's jam
discharge headway τ ≈ T + start-up lag ≈ 1.8 s (a = 0.73 m/s² accelerates
gently) vs ≈ 1.4 s for real drivers, who anticipate several vehicles ahead —
the wave speed is c ≈ −(L+s0)/τ ≈ −14 km/h at 7 m jam spacing. The
Sugiyama ring acceptance test (new, `engine/sugiyama_test.go`) shows the
same offset: spontaneous phantom jam from noise at 87 veh/km, propagating
backward at −13.7 km/h, with a stable control at 61 veh/km. Closing the
residual needs multi-vehicle anticipation (not plain IDM) — a candidate for
M4, along with driver heterogeneity to break the sim's over-coherent wide
jams into the real field's smaller stripes.

**Measurement addition.** `XTField.WaveStripeSpeeds` measures each crossing
wave's lag between section rows (the variance scan is hijack-prone on sim
fields and the FD chord is dragged by trough creep). Cross-validated on the
real field: legs −5.8…−30 ft/s, median −13.6 ft/s = −15.0 km/h — the real
field's own per-wave spread is wide, which single-number anchors hide.
