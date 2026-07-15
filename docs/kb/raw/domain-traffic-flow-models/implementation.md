# Model Formulations: Traffic Flow Models (Microscopic)

> Source: web research (greenfield — these are the formulations the engine will
> implement; re-audit against code once the vehicle-dynamics package exists)
> | Researched: 2026-07-15 | Git HEAD: eed427a

## 1. IDM — Intelligent Driver Model (Treiber/Hennecke/Helbing 2000)

Original: Phys. Rev. E 62:1805 ([arXiv:cond-mat/0002177](https://arxiv.org/abs/cond-mat/0002177)).

```
dv/dt = a [ 1 − (v/v0)^δ − (s*(v,Δv)/s)² ]
s*(v,Δv) = max[ s0 + vT + v·Δv / (2√(a·b)), 0 ]
```

s = bumper-to-bumper gap; Δv = v − v_lead (positive when closing). Free part
relaxes to v0 with max accel a; interaction part is a repulsive term growing
with closing rate; the v·Δv/(2√(ab)) term is a kinematic braking strategy that
keeps decelerations near the comfortable b in normal driving
([Kesting & Treiber CACAIE preprint](https://mtreiber.de/publications/timedelay_CACAIE_07.pdf),
[traffic-simulation.de](https://www.traffic-simulation.de/info/info_IDM.html),
[25-Years-of-IDM review, arXiv:2506.05909](https://arxiv.org/html/2506.05909v1)).

**Parameters** (car values across sources — Wikipedia typical / CACAIE Table 1 /
traffic-simulation.de): v0 30 m/s | 120 km/h | 120 km/h (trucks 80); T 1.5 s
(typ. range 0.8–2); s0 2 m; a 0.73 | 1.0 | 0.3 m/s²; b 1.67 | 2.0 | 3.0 m/s²;
δ = 4. **a is the string-stability lever** (§7) — the choice decides whether
phantom jams form.

**Pathologies + fixes:**
1. *Negative speeds at finite steps* — any explicit scheme when a step spans a
   stop. Mandatory **stopping override**: if v + h·a < 0, set
   x += −v²/(2a), v = 0 (raises consistency order at stops 1→2)
   ([Treiber & Kanagaraj, arXiv:1403.4881 Eq. 15](https://arxiv.org/abs/1403.4881)).
2. *Overreaction in cut-ins* — (s*/s)² explodes when a lane change drops a
   vehicle in at small s; plain IDM emergency-brakes where a relaxed response
   is realistic. Fixes: IIDM (two-regime reformulation), ACC model = IIDM +
   constant-acceleration heuristic ([arXiv:0912.3613](https://arxiv.org/abs/0912.3613)).
   Cap emergency decel at ~9 m/s² (physical dry-road limit)
   ([CACAIE p.8](https://mtreiber.de/publications/timedelay_CACAIE_07.pdf)).
3. *Exaggerated braking above v0* (entering lower speed zones) — fixed by IIDM
   ([25-Years review](https://arxiv.org/html/2506.05909v1)).
4. *Collision-free as an ODE*, but Lipschitz fails as s→0; discrete time +
   extreme parameters can overlap — hence the cap + override
   ([arXiv:1403.4881 §2.2](https://arxiv.org/abs/1403.4881)).
- IDM-Plus variant: a = min{a_free, a[1−(s*/s)²]} (kinked — numerics relevant)
  ([arXiv:1403.4881 Eq. 20](https://arxiv.org/abs/1403.4881)). Canonical IIDM/ACC
  equations are in Treiber & Kesting *Traffic Flow Dynamics* Ch. 11 (⚠ transcribe
  from the book or movsim reference implementation before implementing).

**Steady state / implied FD**: `s_e(v) = (s0 + vT)/√(1 − (v/v0)^δ)`
([arXiv:0805.0300](https://arxiv.org/pdf/0805.0300)); k = 1/(s_e + L), q = kv.
Linear s0+vT part ⇒ triangular-ish FD with rounded peak; string-unstable
parameter regions reproduce breakdown, hysteresis, stop-and-go
([cond-mat/0002177](https://arxiv.org/abs/cond-mat/0002177)).

## 2. Gipps (1981) — safety-distance model

Primary: Transp. Res. B 15:105 ([PDF](https://git.noh.am/noham/Stage-2023/raw/commit/b78180a29740b112b094b3151cb099f4e202c83a/DOC/gipps1981.pdf)).

- Acceleration branch (empirical envelope):
  `v(t+τ) ≤ v + 2.5·a·τ·(1 − v/V)·(0.025 + v/V)^½`
- Braking branch: assume the leader may stop at rate b̂ (estimated); ensure own
  stop behind it given reaction time τ + margin θ = τ/2:
  `v(t+τ) ≤ bτ + √(b²τ² − b[2(x_lead − s_lead − x) − vτ − v_lead²/b̂])`
- Model = min of the two. Congestion = braking branch binding.
- **Δt = τ by design**: Gipps's explicit criterion (c) — "well behaved when the
  interval between successive recalculations … is the same as the reaction
  time". Position update is trapezoidal between speed points.
- **Collision-free proof** holds for identical vehicles with b̂ not
  underestimating the leader's braking; heterogeneous fleets break it
  ([Lücken, arXiv:1902.04927](https://arxiv.org/abs/1902.04927)); the square
  root can go negative in unusual states ([Wikipedia](https://en.wikipedia.org/wiki/Gipps%27_model)).
- Gipps's own validation params: a~N(1.7,0.3²), b=−2a, s~N(6.5,0.3²) m,
  V~N(20,3.2²) m/s, τ=2/3 s. Stability knob is b̂: optimistic b̂ amplifies
  disturbances, pessimistic damps them — instability by mis-estimation, not by
  adaptation dynamics.

## 3. Krauss (1998) — SUMO's default (Gipps family)

Safe speed ([Krauss 1998 via TUM thesis Eq. 2.1](https://mediatum.ub.tum.de/doc/1550467/530432.pdf)):

```
v_safe = v_l + ( g − v_l·t_r ) / ( (v_l + v_f)/(2b) + t_r )
```

Update: `v_des = min(v_safe, v + a·Δt, v_max)` then stochastic **dawdling**
(random slowdown scaled by σ). Position: plain Euler. SUMO notes the original
safety equation "is actually invalid if the follower can brake faster than the
leader" — patched in implementation
([sumo#6791](https://github.com/eclipse-sumo/sumo/issues/6791),
["SUMO's Interpretation of the Krauß Model"](https://www.researchgate.net/publication/393736142_SUMO's_Interpretation_of_the_Krauss_Model)).
Why SUMO chose it: collision-free at its native discrete update, one accel eval
per step, σ-noise gives spontaneous breakdown. SUMO defaults (per vClass):
accel 2.6 (truck 1.3), decel 4.5 (truck 4.0), emergencyDecel 9.0, sigma 0.5,
tau 1.0 s, minGap 2.5 m, length 5 m car
([defaults table](https://sumo.dlr.de/docs/Vehicle_Type_Parameter_Defaults.html)).
⚠ convention split: physics-literature b is *comfortable* (1.5–2); SUMO decel
is closer to *capability* (4.5) — never mix conventions in one parameter set.

## 4. Newell (2002) — trajectory translation

```
x_i(t) = min{ x_i(t−τ) + u·τ,  x_{i−1}(t−τ) − δ }
τ = 1/(w·k_j)   δ = 1/k_j      (parameter mapping VERIFIED)
```

Congested spacing d = vτ + δ ⇒ **exactly** LWR with triangular FD (u, −w, k_j)
([Newell-type CF explainer](https://garyounglee.github.io/web/2023/02/07/TRSCF.html);
empirical verification [Ahn/Cassidy](https://www.sciencedirect.com/science/article/abs/pii/S0191261503000742)).
No acceleration bounds, no string instability ⇒ no spontaneous jams; waves must
be injected at boundaries. Equivalence: Newell = OVM with triangular OV-function
+ Euler update at Δt = desired time gap
([arXiv:1403.4881 intro](https://arxiv.org/abs/1403.4881)).
**Role for us: the validation-oracle controller** (Newell platoon ≈ LTM/LWR to
machine precision — ties to [[domain-macroscopic-flow-models]]).

## 5. OVM (Bando 1995) + FVDM (Jiang 2001)

```
OVM:  dv/dt = a·[V(Δx) − v]         V(Δx) = (v_max/2)[tanh((Δx−h_c)/ℓ) + 1]
FVDM: dv/dt = a·[V(Δx) − v] + λ·(v_lead − v)
```

Velocity adaptation time τ_v = 1/a. String instability when the OV-slope at
equilibrium is too steep vs sensitivity (canonical Bando form V′(h*) > a/2 —
⚠ verify the factor of 2 against Bando 1995 before encoding in a test oracle;
secondary source garbled it). Pathology: linear relaxation ⇒ unrealistically
large accelerations (Helbing & Tilch calibration exposed it; motivated their
GFM). FVDM's velocity-difference term stabilizes but calibrates worse than IDM
on NGSIM ([arXiv:0803.4063](https://arxiv.org/abs/0803.4063);
[arXiv:2509.22671](https://arxiv.org/html/2509.22671)).
**OVM is the model class quantitatively fitted to the ring-road experiments**
(Nakayama et al. 2016, [NJP 18:043040](https://iopscience.iop.org/article/10.1088/1367-2630/18/4/043040)
— ⚠ fitted parameter values need a browser read; IOP 403s fetchers).

## 6. Wiedemann (Vissim) — brief, and why not

Psycho-physical threshold model (AX/BX/SDX/SDV/CLDV/OPDV): drivers react only
when perception thresholds cross; produces oscillating "pendulum" following.
W74 urban, W99 freeway; W99 has ~10 opaque constants (CC0–CC9), thresholds are
speed- and driver-dependent — "a full calibration with this third dimension
would be a daunting task" ([TRB naturalistic-data analysis](https://onlinepubs.trb.org/onlinepubs/conferences/2011/RSS/3/Higgs,B.pdf)).
Proprietary heritage, physically uninterpretable parameters ⇒ wrong fit for us.
SUMO ships W99 if comparison is ever needed.

## 7. String stability (Kesting & Treiber, CACAIE 23:125)

Primary: [author PDF](https://mtreiber.de/publications/timedelay_CACAIE_07.pdf).

- **Local** stability: a pair; **string** stability: perturbations damp along a
  platoon — the stricter criterion. Sensible models with zero delay are always
  locally stable; string instability still possible.
- Velocity adaptation time: IDM free traffic τ_v = v0/(4a); OVM τ_v = 1/a.
- **Two instability mechanisms** (100-vehicle IDM platoons):
  1. *Long-wavelength* (the phantom-jam mechanism): driven by small a
     (a = 0.3–0.5 m/s² unstable regardless of reaction time) — grows upstream
     into stop-and-go.
  2. *Short-wavelength local*: needs finite reaction/update time + agile style
     (a = 2.5 with T′ = 0.9 s unstable).
- **Stable region** (reference set v0=120, T=1.5, s0=2, b=2): a ≥ 1 m/s² stable
  for small reaction times; no a is stable at T′ = 1.0 s.
- **Scaling law**: rescale time by T, space by v0T ⇒ ã=Ta/v0 etc. — use to
  transpose parameters to the 30 km/h ring regime.
- Heterogeneous populations ≈ homogeneous with arithmetic-mean parameters.
- **Sugiyama reproduction recipe**: 22 vehicles / 230 m ring; either OVM in its
  unstable region (Nakayama-fitted) or IDM with a ≈ 0.3–0.5 m/s² scaled down;
  expect a jam cluster propagating backward ~20 km/h
  ([Sugiyama 2008](https://iopscience.iop.org/article/10.1088/1367-2630/10/3/033001)).

## 8. Numerics & tick length (owns the ADR-0005 default)

Primary: Treiber & Kanagaraj, Physica A 419:183
([arXiv:1403.4881](https://arxiv.org/abs/1403.4881)); Kesting & Treiber CACAIE.

**Schemes** (cost in accel evals/step): Euler (1); **ballistic** (1):
v += h·a; x += h·v + ½h²·a; trapezoidal/Heun (2); RK4 (4).

**Findings:**
- Smooth case: ballistic always ≈ 30% of Euler's error at identical cost.
- With stops (+ override): orders preserved; RK4 error ×5 but still best.
- **Lane changes make ALL schemes order-1, and RK4 is empirically WORST per
  unit cost** — "the most severe source of discontinuities are active and
  passive lane changes" (§4.5). Higher-order schemes buy nothing in a
  multi-lane sim.
- Recommendation (paper §5): **ballistic or trapezoidal**.

**Update time ↔ reaction time:**
- **Δt = 0.1 s is dynamically exact**: "the update time step was so small
  (Δt = 0.1 s) that it did not have a significant influence as confirmed by
  simulations with … Δt = 0.01 s" (CACAIE §3.3) — direct validation of our
  100 ms tick.
- Finite update time acts like reaction time T′_eff ≈ Δt/2.
- Stability boundary (IDM ref params, a=1): ≈ Δt + 2T′ = 2 s. Reaction time
  destabilizes ~2× as strongly as update time.
- Δt = 0.5 s: defensible fallback (T′_eff ≈ 0.25 s); Δt = 1.0 s: eats most of
  the stability budget; SUMO warns models untested above 1 s and
  tau < step-length "may induce collisions"
  ([SUMO Basic Definition](https://sumo.dlr.de/docs/Simulation/Basic_Definition.html)).
- Deliberate reaction-time modeling: keep Δt small and interpolate delayed
  inputs per driver (CACAIE Eq. 3) — don't couple physics to the tick.
  SUMO's `actionStepLength` decouples decision cadence the same way
  ([Car-Following-Models](https://sumo.dlr.de/docs/Car-Following-Models/index.html)).

**Engine prescription:** 100 ms tick (validated) + ballistic integrator + stop
override; one accel eval/vehicle/tick, branch-light, replay-friendly (no
adaptive stepping).

## 9. MOBIL lane changing (Kesting/Treiber/Helbing 2007)

Primary: [TRR paper PDF](https://www.mtreiber.de/publications/MOBIL_TRR.pdf);
[explainer](https://traffic-simulation.de/info/info_MOBIL.html).

Both utility and risk expressed as *car-following accelerations* (any model of
form a(s,v,Δv) plugs in); collision-freedom inherits from the CF model. With
c = changer, n/o = new/old follower, tildes = post-change:

```
Safety:     ã_n ≥ −b_safe
Incentive:  ã_c − a_c + p·[(ã_n − a_n) + (ã_o − a_o)] > Δa_th
```

- Politeness p: paper Table 1 gives the full range 0…1; p ∈ (0, 0.5] is the
  range described as realistic by
  [traffic-simulation.de](https://traffic-simulation.de/info/info_MOBIL.html),
  not the paper. p=1, Δa_th=0 ⇒ change iff total system acceleration increases
  ("ideal MOBIL"). Critical gaps are NOT explicit — a faster-closing follower
  automatically demands a bigger gap.
- Asymmetric (European) variant: keep-right bias Δa_bias, no right overtaking
  above v_crit ≈ 60 km/h via a_eur = min(a_c, ã_c).
- Paper parameters: p 0…1, Δa_th 0.1 m/s², b_safe 4 m/s², Δa_bias 0.3 m/s².
- **Fixed-tick friendly**: evaluated every step, executed instantly; results
  insensitive to Δt ∈ {0.25, 0.1, 0.01} s.
- Merges: virtual standing vehicle at lane end + p=0 for the merger.
- Limitations: purely operational — no tactical (advance turn positioning) or
  strategic (route) layer; no cooperation/gap-seeking; instant jumps give the
  followers acceleration discontinuities.

## 10. Gap acceptance mathematics

Primary: Troutbeck & Brilon, FHWA Traffic Flow Theory Ch. 8
([PDF](https://www.fhwa.dot.gov/publications/research/operations/tft/chap8.pdf)).

- **Critical gap t_c** (min acceptable major-stream gap; only bracketed between
  largest rejected and accepted gap) and **follow-up time t_f** (queue
  discharge headway into one long gap). Streams have priority Ranks 1–4.
- Capacity, exponential headways at major flow q_p:
  - Harders/HCM potential capacity: `q_m = q_p·e^(−q_p·t_c) / (1 − e^(−q_p·t_f))`
  - **Siegloch**: `q_m = (1/t_f)·e^(−q_p·t_0)`, t_0 = t_c − t_f/2
  - Cowan M3 bunched-headway variants for high flows.
- Estimation: **Troutbeck's maximum likelihood** (log-normal t_c; likelihood
  F(accepted) − F(largest rejected)) — consistent and unbiased vs Raff etc.
- Consistency/homogeneity assumptions are both false but **their errors
  cancel** (Grossmann): heterogeneous t_c lowers capacity, inconsistent
  (impatient) t_c raises it — "the difference … is only a few percent." A
  per-driver sampled t_c + waiting-time decay is *more* realistic and roughly
  capacity-neutral — exactly what SUMO impatience and Aimsun give-way decay
  implement (see competitors.md).
