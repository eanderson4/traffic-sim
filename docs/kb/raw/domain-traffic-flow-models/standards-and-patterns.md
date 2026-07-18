# Standards & Patterns: Traffic Flow Models (Microscopic)

> Source: academic research + pattern identification | Researched: 2026-07-15

## HCM / NCHRP reference values (validation targets)

### Two-way stop control (TWSC) — HCM 2000 Exhibit 17-5 (unchanged through HCM 6 per state DOT manuals)

| Movement | t_c 2-lane major (s) | t_c 4-lane (s) | t_f (s) |
|---|---|---|---|
| Left from major | 4.1 | 4.1 | 2.2 |
| Right from minor | 6.2 | 6.9 | 3.3 |
| Through on minor | 6.5 | 6.5 | 4.0 |
| Left from minor | 7.1 | 7.5 | 3.5 |

Adjustments: +2.0 s t_c per heavy-vehicle fraction (2-lane), grade term,
two-stage deduction; t_f +1.0 s·P_HV. Potential capacity via the Harders form
(worked example: t_c=7.1, conflicting 150 veh/h → c_p=845 veh/h ⚠ audit
recomputation from the stated base values gives ≈822; the example likely
includes adjustment factors — re-derive from the full worksheet before citing
in a calibration ADR)
([HCM 2000 Ch.17 PDF](https://media.benjaminabram.com/hcm16.pdf);
[IIT-B reproduction](https://www.civil.iitb.ac.in/tvm/nptel/564_UnCotrl/web/web.html)).
⚠ HCM 7 (2022) reportedly leaves TWSC/AWSC substantively unchanged — verify
exhibits in a library copy before stamping values into a calibration ADR.

### Roundabouts — NCHRP 572 → HCM 2010 → HCM 6
- HCM 2010/NCHRP 572 single-lane: t_c = 5.19 s, t_f = 3.19 s. HCM 6:
  t_c = 4.99 s (single) / 4.32 s (multi-left), t_f = 2.609 / 2.536 s — drivers
  acclimated, headways fell
  ([mikeontraffic summary](https://www.mikeontraffic.com/hcm-6th-edition-roundabout/);
  [NCHRP 572 review](https://link.springer.com/article/10.1007/s12544-015-0190-4)).
- Capacity is Siegloch-form `c = A·e^(−B·v_c)`, A = 3600/t_f,
  B = (t_c − t_f/2)/3600. HCM 2010 single-lane `c = 1130·e^(−1.0e−3·v_c)`;
  HCM 6 intercept rises to ≈1380 veh/h (~+50% single-lane)
  ([NCHRP 672](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/content/traffic/doc_library/pdf/nchrp_rpt_672.pdf)).
- Our validation split: HCM/NCHRP curves = capacity envelope; **rounD drone
  data** ([[domain-trajectory-datasets]]) = behavioral ground truth (German
  yield behavior ≠ US HCM values — driver-population parameter, maybe per map).

### All-way stop control (AWSC) — HCM departure-headway method
Not gap acceptance: saturation headway depends on the **degree-of-conflict
case** (which other approaches are occupied). Single-lane base headways
verified from the HCM worked example: Case 1 = 3.9 s, Case 2 = 4.7 s,
Case 3 = 5.8 s, Case 4 = 7.0 s (Case 5 multilane up to ~9 s ⚠ needs HCM 2010
Exhibit 20-14). Adjustments: LT +0.2 s, RT −0.6 s, HV +1.7 s. Iterate
occupancy-probability × headway to convergence; service time = h_d − 2.0 s
move-up; example capacities 610–765 veh/h/approach
([HCM 2000 Ch.17 Example 4](https://media.benjaminabram.com/hcm16.pdf);
[NAP 6339](https://nap.nationalacademies.org/read/6339/chapter/8);
[Wu ACF alternative](https://homepage.rub.de/ning.wu/pdf/AWSC_TRB2000.pdf)).
**Pattern: let these headways EMERGE** from conflict-scanning delay per
occupied approach, then check against HCM numbers — don't table-drive them.

## Design Patterns Identified

### Acceleration-based safety as the universal invariant (MOBIL's insight)
Express lane-change safety AND junction-entry safety as "no foe is forced to
brake harder than b_safe." One invariant unifies both, inherits model
sophistication, and lets controller policies range timid→aggressive without
breaking safety. Alternative: time-gap-based (SUMO jmTimegapMinor 1 s) —
cheaper per tick. Decide with the tick-budget benchmark (open question).

### Strategic/tactical/operational hierarchy (Toledo)
MOBIL = operational only; SUMO LC2013 adds the strategic (route/dead-lane) and
cooperative layers with strict priority ordering. Our controller-side policy
mirrors: strategic > cooperative veto > tactical speed-gain > keep-right.
Strategic needs lane-level successor connectivity — a requirement on
[[arch-road-graph-model]].

### foes/response factoring (right-of-way as data)
`foes(a,b)` = immutable conflict geometry (computed once from lane paths
through the junction, annotated crossing/merging/diverging);
`yields(a,b)` = policy (junction type + signal state + ranks), recomputable.
Runtime question reduces to gap logic over yielded foes. This becomes part of
the **map contract on NATS — changes need an ADR** (message contracts are
sacred). Expose HCM Rank 1–4 semantics for debugging ("why am I waiting").

### Impatience decay (starvation-proof gap acceptance)
All three products converge on t_c decaying with waiting time (SUMO impatience
0→1 over 180 s; Aimsun initial→final safety margin). Theory blesses it:
driver inconsistency raises capacity, heterogeneity lowers it, and the two
roughly cancel (Grossmann via [FHWA Ch.8](https://www.fhwa.dot.gov/publications/research/operations/tft/chap8.pdf)).
Controller-side policy; engine-side backstop stays fixed.

### Deterministic tie-breaking at stop lines
Same-tick arrivals are common at 100 ms ticks. Explicit, stable order:
(1) earlier stop-line arrival tick, (2) rightmost-relative-approach (matches
yield-to-the-right law), (3) stable hash of (vehicle ID, junction ID). Integer
ticks and lane indices only — never floats, never Go map iteration order.
Needs an ADR (affects replay determinism + multiplayer fairness perception).

### Prevention over cure (the no-teleport policy)
SUMO's teleport taxonomy (wrong lane / yield / jam / blocked) becomes NATS
telemetry events, not position hacks. Prevention: counterLaneChange protocol,
space reservation before multi-lane mandatory changes, keep-clear boxes,
impatience decay. Physical-only last resorts: drive-around after long wait
(SUMO `--ignore-junction-blocker` precedent), route abandonment + reroute,
despawn (reads as "car left") — never mid-map teleport. Erdmann's numbers say
prevention removes ~99% of teleport causes (845→7, 464→9).

### Virtual vehicles for merges
MOBIL's virtual standing vehicle at acceleration-lane end converts "mandatory
merge pressure" into ordinary car-following deceleration. SUMO instead uses
dead-lane urgency + capped cooperation (>27 m/s). Both are data-driven ways to
avoid special-casing merges in the dynamics.

### Ballistic integrator + stop override (numerics standard)
See implementation.md §8. Order-1 world (lane changes), so one accel eval per
vehicle-tick is optimal; stop override is mandatory for queue/delay metrics.

## Empirical anchors

- Backward wave from IDM-ish car defaults: w ≈ (s0+L)/T ≈ 17 km/h — consistent
  with −15…−20 km/h target (⚠ derived consistency check, not sourced; validate
  numerically in-engine).
- Sugiyama ring: 22 veh / 230 m / 30 km/h target → jam cluster at ~20 km/h
  backward ([NJP 10:033001](https://iopscience.iop.org/article/10.1088/1367-2630/10/3/033001)).
- Lane-change rate benchmark: 0.26 changes/veh-km (US highways, via Erdmann).
- NGSIM calibration errors: 11–29% typical; intra-driver variability dominates
  inter-driver; T is the most influential parameter
  ([arXiv:0803.4063](https://arxiv.org/abs/0803.4063),
  [25-Years review](https://arxiv.org/html/2506.05909v1)).
- AWSC discharge: 3.9–7.0 s headways by conflict case; ~450–765 veh/h/approach.
- String stability: a ≥ 1 m/s² stable (IDM ref set, small T′);
  Δt + 2T′ ≈ 2 s boundary.

## M3 calibration note (2026-07-17): matching the I-80 wave speed

What the engine learned from reproducing the NGSIM −18.1 km/h backward wave
(full write-up: `analysis/ngsim/README.md`, M2/M3 sections; scenario:
`engine/scenario_i80.go`):

- **Chosen calibration: the original IDM paper's highway set** — v0 = 33.3
  m/s, T = 1.6 s, a = 0.73 m/s², b = 1.67 m/s², s0 = 2 m, δ = 4
  ([cond-mat/0002177](https://arxiv.org/abs/cond-mat/0002177); the "Wikipedia
  typical" column of implementation.md §1). The string-stable CACAIE set
  (a = 1.0, T = 1.5) keeps jam troughs ON the equilibrium curve, which caps
  the FD chord slope at ≈ −12 km/h (M2 diagnosis). With the
  instability-capable set, troughs go sub-equilibrium (full stops with
  hysteresis) and the cap is gone: sim wave speed −13.2…−15.4 km/h (scan,
  seeds 1–5) / −13.7…−15.0 (per-wave leg median, seeds 2–5) vs real −18.1
  (scan) / −15.0 (leg median, same estimator). Residual ≈ 0–1.3 km/h by the
  robust estimator — traced to IDM's discharge headway τ ≈ T + start-up lag
  ≈ 1.8 s (a = 0.73 is gentle) vs ≈ 1.4 s for real drivers, who anticipate
  several vehicles ahead; the Sugiyama ring shows the same offset
  (−13.7 km/h, engine/sugiyama_test.go). Multi-vehicle anticipation (not
  plain IDM) is the candidate fix if the residual must close.
- **b_safe alone is NOT a collision guarantee under discrete time.** MOBIL's
  "collision-freedom inherits from the CF model" holds only because IDM is
  collision-free as an ODE (implementation.md §1); the −9 m/s² cap + 100 ms
  steps + instant hops break the inheritance. The engine now enforces a
  kinematic floor under hop acceptance (Gipps braking branch / Krauss
  v_safe: gap ≥ (v_f²−v_l²)/(2·b_max) + Δv·Δt, b_max = 9 m/s², never relaxed
  by merge urgency) AND resolves hop neighbors across lane boundaries
  (predecessor/successor pairs were invisible to the checks). Merge
  negative-gap events: ≈3,000/run → 0. This partially answers the open
  question below ("b_safe vs time-gap enforcement"): acceleration-based
  b_safe needs a kinematic supplement at merges; a time-gap rule would have
  rejected the same hops (Δv > 10 m/s into 0.3 m), so the benchmark is now
  moot for collision-freedom — the floor is the physics backstop either way.
- **Removing fake merge losses re-anchors boundary conditions.** The 6→5
  funnel discharge rose from ≈ 7,300 veh/h (overlap resolution throttling
  it) to ≈ 7,680 — the same marginally-overloaded regime M2 tuned its
  boundary to must be re-found (here: sustained 1.20× demand = the real
  queue's measured growth rate). Any scenario whose boundary conditions were
  calibrated against a defective kernel needs re-validation after a physics
  fix.
- **Measure wave speed per wave, not by variance fit.** The variance scan is
  hijacked by mass-dominated fields and the FD chord is dragged by trough
  creep; the robust measurement is the per-wave lag between section rows
  (`XTField.WaveStripeSpeeds`; real field: legs −5.8…−30 ft/s, median
  −13.6 ft/s = −15.0 km/h — note the REAL field's own per-wave spread is
  wide; single-number anchors hide realization variance).

## Open Questions

- IIDM/ACC canonical equations (book Ch. 11) — transcribe before implementing.
- Nakayama 2016 fitted OV parameters (IOP 403s fetchers) — browser read needed
  for a quantitative Sugiyama test.
- OVM instability threshold factor (V′ > a/2) — verify against Bando 1995.
- IDM analytic string-stability inequality (book Ch. 15) — for a test oracle.
- b_safe (acceleration-based) vs jmTimegapMinor (time-gap) for engine
  enforcement — benchmark.
- Cooperation over NATS: does ≥1-tick gap-request latency change merge
  throughput? Simulate both.
- Emergent vs prescribed AWSC headways — prototype against HCM numbers.
- Zipper fairness at saturation: strict 1:1 alternation vs demand-weighted.
- ~~Gap convention: SUMO minGap-outside-length vs IDM bumper-to-bumper s0~~
  **RESOLVED 2026-07-17 review**: bumper-to-bumper gap is the one canonical
  semantics; position = front-bumper coordinate; one jam-gap parameter `s0`
  measured bumper-to-bumper with `length` separate; spacing-derived quantities
  computed, never stored. To be pinned in the vehicle-model ADR.
- Erdmann's ad-hoc constants (f=10/20, 20/40 m reservations) — calibration
  surface from day one.
- rounD-derived t_c/t_f distributions (Troutbeck MLM on drone data).
- HCM 7 exhibit verification (paywalled).

## Master source list

IDM: [cond-mat/0002177](https://arxiv.org/abs/cond-mat/0002177) ·
[25 Years of IDM](https://arxiv.org/html/2506.05909v1) ·
[traffic-simulation.de](https://www.traffic-simulation.de/info/info_IDM.html) ·
[ACC/CAH arXiv:0912.3613](https://arxiv.org/abs/0912.3613) —
numerics: [Treiber & Kanagaraj arXiv:1403.4881](https://arxiv.org/abs/1403.4881) ·
[Kesting & Treiber CACAIE](https://mtreiber.de/publications/timedelay_CACAIE_07.pdf) —
calibration: [arXiv:0803.4063](https://arxiv.org/abs/0803.4063) —
Gipps: [1981 PDF](https://git.noh.am/noham/Stage-2023/raw/commit/b78180a29740b112b094b3151cb099f4e202c83a/DOC/gipps1981.pdf) ·
[Wilson 2001](https://www.researchgate.net/publication/31412592_An_analysis_of_Gipps'_car-following_model_of_highway_traffic) ·
[Lücken arXiv:1902.04927](https://arxiv.org/abs/1902.04927) —
Krauss: [TUM thesis w/ Eq. 2.1](https://mediatum.ub.tum.de/doc/1550467/530432.pdf) ·
["SUMO's Interpretation of the Krauß Model"](https://www.researchgate.net/publication/393736142_SUMO's_Interpretation_of_the_Krauss_Model) —
Newell: [explainer](https://garyounglee.github.io/web/2023/02/07/TRSCF.html) ·
[Ahn/Cassidy verification](https://www.sciencedirect.com/science/article/abs/pii/S0191261503000742) —
OVM/rings: [Sugiyama NJP 10:033001](https://iopscience.iop.org/article/10.1088/1367-2630/10/3/033001) ·
[Nakayama NJP 18:043040](https://iopscience.iop.org/article/10.1088/1367-2630/18/4/043040) ·
[arXiv:2509.22671](https://arxiv.org/html/2509.22671) —
Wiedemann: [TRB W99 analysis](https://onlinepubs.trb.org/onlinepubs/conferences/2011/RSS/3/Higgs,B.pdf) —
MOBIL: [TRR PDF](https://www.mtreiber.de/publications/MOBIL_TRR.pdf) —
LC2013: [Erdmann preprint](https://elib.dlr.de/102254/1/Springer-SUMOs_Lane_changing_model.pdf) —
gap theory: [FHWA TFT Ch.8](https://www.fhwa.dot.gov/publications/research/operations/tft/chap8.pdf) —
HCM/NCHRP: [HCM 2000 Ch.17](https://media.benjaminabram.com/hcm16.pdf) ·
[IIT-B tables](https://www.civil.iitb.ac.in/tvm/nptel/564_UnCotrl/web/web.html) ·
[NCHRP 672](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/content/traffic/doc_library/pdf/nchrp_rpt_672.pdf) ·
[HCM6 roundabout](https://www.mikeontraffic.com/hcm-6th-edition-roundabout/) —
SUMO: [Intersections](https://sumo.dlr.de/docs/Simulation/Intersections.html) ·
[PlainXML](https://sumo.dlr.de/docs/Networks/PlainXML.html) ·
[Motorways](https://sumo.dlr.de/docs/Simulation/Motorways.html) ·
[teleporting](https://sumo.dlr.de/docs/Simulation/Why_Vehicles_are_teleporting.html) ·
[vehicle docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html) ·
[type defaults](https://sumo.dlr.de/docs/Vehicle_Type_Parameter_Defaults.html) —
Vissim: [TRB EC083](https://onlinepubs.trb.org/Onlinepubs/circulars/ec083/59_Baredpaper.pdf) ·
[MassDOT](https://www.mass.gov/doc/massdot-roundabout-vissim-microsimulation-guidance/download) —
Aimsun: [give-way](https://docs.aimsun.com/next/22.0.1/UsersManual/MicrosimulationModellingVehicleMovement.html)
