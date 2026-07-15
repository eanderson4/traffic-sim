# Standards & Patterns: Macroscopic Flow Models

> Source: academic research (theory agent, sources verified to page level) |
> Researched: 2026-07-15

## Known Limitations of First-Order LWR

Compiled from Daganzo's own words (quoted with page numbers via Helbing & Johansson,
[arXiv:0805.3402](https://arxiv.org/pdf/0805.3402)), TU Delft Chs. 4/7/8, and the
Seibold IPAM tutorial:

1. **Instantaneous, local speed adaptation.** u = V(k) with no relaxation or
   anticipation — vehicles cross shocks with discontinuous speed; acceleration is
   unbounded. Consequence: cannot describe the first vehicle accelerating out of a
   queue (TU Delft Ch. 8, Sec. 8.4.1).
2. **No capacity drop / hysteresis.** Single-valued q = Q(k) can't hold two capacities
   (pre-queue vs discharge) or hysteresis loops; discontinuous-FD fixes (Edie 1961, Wu)
   are ad-hoc extensions.
3. **No spontaneous stop-and-go.** LWR is unconditionally stable — disturbances
   propagate, shocks form, but perturbations never *grow*. Daganzo 1995, p. 285:
   deficiencies include "its failure to describe platoon diffusion properly ... and
   its inability to explain the instability of heavy traffic, which exhibits
   oscillatory phenomena on the order of minutes."
4. **No platoon diffusion** (same source).
5. **Zero-width shocks** — real jam fronts span several vehicles.

Engineering acceptance: for queue length, delay, travel time at bottlenecks,
first-order + triangular FD is usually adequate and is the industrial default (CTM,
METANET-class tools).

## Second-Order Models and the Requiem/Resurrection Controversy

### Payne–Whitham (1971/1974)

Adds a dynamic velocity equation (form verified from arXiv:0805.3402 eqs. 3–4):

```
∂V/∂t + V ∂V/∂x = −(ν/ρ) ∂ρ/∂x + [V_e(ρ) − V]/τ ,   ν = (1/2τ)|dV_e/dρ|
                  anticipation     relaxation
```

Buys: finite acceleration, relaxation, and **linear instability** — Payne criterion
ρ_e|dV_e/dρ| > 1/(2ρ_e τ) (Helbing & Johansson eqs. 38–40). Instability produces
self-sustained traveling waves ("jamitons") — the second-order route to phantom jams
([Seibold et al., arXiv:1204.5510](https://arxiv.org/pdf/1204.5510)).

### Daganzo's Requiem (1995)

Daganzo, "Requiem for second-order fluid approximations of traffic flow," *TR-B*
29(4), 277–286. Criticisms (page numbers via Helbing & Johansson):

1. **Anisotropy violated**: "a car is an anisotropic particle that mostly responds to
   frontal stimuli" [p. 279]; pressure terms let traffic behind push a driver.
2. **A characteristic speed exceeds vehicle speed**: "future conditions of a traffic
   element are, in part, determined by what is happening ... BEHIND IT!" [p. 281].
3. **Negative speeds/flows** possible at a queue tail bordering empty road [pp. 282–283].
4. **Jam fronts too smooth** (viscosity) though "the width of a traffic shock only
   encompasses a few vehicles" [pp. 279, 282].
5. **Drivers aren't molecules**: desired-speed relaxation treats speed distribution as
   "a property of the road and not the drivers" [pp. 279–280].

Recommendation was retreat to first-order — contemporaneous with his own CTM.

### Aw–Rascle–Zhang resurrection (2000/2002)

Aw & Rascle, *SIAM J. Appl. Math.* 60(3):916–938; Zhang, *TR-B* 36(3):275–290.
Fix: advect "pressure" with the cars (Lagrangian marker w = v + p(ρ)):

```
∂ρ/∂t + ∂(ρv)/∂x = 0
∂(v + p(ρ))/∂t + v ∂(v + p(ρ))/∂x = 0 ,   p(ρ) = ρ^γ
```

Characteristic speeds λ₁ = v − ρp'(ρ) ≤ v, λ₂ = v (verified from Helbing & Johansson
eqs. 36–37) — no information travels faster than cars; anisotropy restored; the second
field transports driver-class differences (answers the "personality" point). Adds:
bounded acceleration, hysteresis, instability with relaxation, set-valued congested
states (mechanism for FD scatter). Costs: 2×2 hyperbolic system (harder numerics,
boundary conditions, vacuum pathologies), extra hard-to-calibrate parameters, loss of
the variational/N-curve machinery.

### Resolution (Helbing & Johansson 2009)

*EPJ B* 69(4):549–562, [arXiv:0805.3402](https://arxiv.org/pdf/0805.3402). Daganzo's
findings were "fully justified" for the models of the time but curable without
abandoning second order. The scary fast characteristic has eigenvalue real part
~ −1/τ — a fast-decaying mode that also appears in purely forward-looking microscopic
models (their Fig. 1), so it doesn't violate causality. The slower characteristic at
the instability threshold equals dQ_e/dρ — the LWR wave speed — i.e. backward-moving
information is normal (gap propagation, seen at every green light).

**Status: contested-but-mature.** Mainstream planning/control runs first-order
(CTM/LTM); second-order (ARZ, METANET, gas-kinetic) where oscillations, capacity drop,
bounded acceleration matter. Survey: [arXiv:2111.04955](https://arxiv.org/pdf/2111.04955).

## Empirical Stylized Facts (what any model must confront)

### FD scatter

Free-flow branch tight; congested branch a **2-D scattered cloud**, not a curve
(TU Delft Ch. 7 Figs. 7.3, 7.7b). Fitting is underdetermined: "a given set of data
points can be used to fit quite a few different models" (Ch. 4 Sec. 4.3). Aggregation
window (5–15 min) and detector location relative to the bottleneck determine what you
can observe. Explanations (contested): transients/hysteresis, heterogeneity (ARZ
w-families), jamitons producing set-valued FDs from deterministic dynamics
([arXiv:1204.5510](https://arxiv.org/pdf/1204.5510)).

### Backward wave speed ≈ 15–20 km/h

Congestion fronts and stop-and-go waves propagate upstream at a remarkably
reproducible 15–20 km/h across countries — a headline stylized fact (Treiber, Kesting
& Helbing 2010, *TR-B* 44:983–1000, [arXiv:1004.5545](https://arxiv.org/abs/1004.5545)).
Triangular-FD arithmetic: w = q_c/(k_j − k_c) ≈ 2000/120 ≈ 17 km/h.

### Capacity drop (two-capacity phenomenon)

Queue discharge rate < pre-breakdown maximum flow (TU Delft Ch. 7, Sec. 7.2).
Empirical magnitudes (via Yuan et al. 2015, [TRR 2491](https://journals.sagepub.com/doi/10.3141/2491-08)):
Hall & Agyemang-Duah 1991 ≈6%; Cassidy & Bertini 1999 8–10%; Srivastava & Geroliminis
2013 ~15% (and 8% same site, day-to-day variation); Chung et al. 2007 3–18%;
Cassidy & Rudjanakanoknad 2005 8.3–14.7%. Consensus range **5–20%**. Mechanism
explicitly unresolved: lane changing vs bounded acceleration vs acceleration
differences (TU Delft Ch. 7). Discharge rate rises ~linearly with in-queue speed
(Yuan et al. Fig. 7.4). Measurement: slanted cumulative counts N(t) − q₀t. Control
relevance: ramp metering tries to keep flow below breakdown to preserve the higher
capacity. Modeling fixes: discontinuous FDs, bounded-acceleration LWR,
[Jin et al. kinematic-wave capacity-drop theory](https://www.sciencedirect.com/science/article/abs/pii/S0191261515001678).

### Stop-and-go waves

Short jams with no bottleneck at their downstream end; constant length (both fronts
at the same backward speed), ~10-min recurrence, propagate for dozens of km, pass
through ramps and other congestion unchanged (TU Delft Ch. 7 Sec. 7.3, German A5
data). Outflow = queue discharge rate; front speed = congested FD branch slope.
LWR propagates but cannot generate them.

### Hysteresis

Treiterer & Myers (1974) aerial trajectory study: deceleration and acceleration
branches differ, loops counterclockwise ([TRB EC149](https://onlinepubs.trb.org/onlinepubs/circulars/ec149.pdf)).

### Stability taxonomy (episode-friendly)

TU Delft Ch. 7 Sec. 7.1: **local** stability (follower vs leader), **platoon/string**
stability (disturbance grows along a platoon?), **traffic-flow** stability (jumps
across platoon gaps?). Empirically traffic is "locally stable and mostly platoon
unstable" — the seed of phantom jams. Optimal-velocity criterion dv_o/dd > 1/(2τ)
corresponds exactly to the macroscopic Payne criterion (Helbing & Johansson
eqs. 110–114) — closes the micro-macro loop.

## Kerner's Three-Phase Theory (contrarian view)

Kerner, *The Physics of Traffic* (Springer 2004). Claims: congestion splits into
**synchronized flow S** (2-D region of states in q–k, flows sometimes above free-flow
capacity) and **wide moving jams J** (fronts propagate upstream at characteristic
constant speed, pass intact through bottlenecks); plus free flow F. Breakdown is
probabilistic nucleation; no single capacity but a range [min, max]. Kerner rejects
all FD-based models (LWR, PW, ARZ, IDM).

Counterposition: Treiber, Kesting & Helbing 2010 reproduce the demanded patterns with
two-phase FD models given noise/heterogeneity/finite perturbations; harsher critiques
call three-phase theory "complex, inaccurate, and inconsistent" with "too many
parameters" (compiled at [Wikipedia](https://en.wikipedia.org/wiki/Three-phase_traffic_theory)).
TU Delft's diplomatic line: "not a fully correct description of traffic, [but] it
includes some features which are observed" (Ch. 7 Sec. 7.4).

**Working stance for this project: FD orthodoxy as default; treat three-phase as a
catalogue of empirical phenomena to confront (2-D congested states, nucleation-like
breakdown, constant jam propagation speed).**

## General Node Models (junction theory)

- **Tampère et al. (2011)**, *TR-B* 45(1):289–309: generic requirements a consistent
  first-order node model must satisfy — general in/out link counts, non-negativity,
  demand/supply constraint satisfaction, conservation of turning fractions
  (FIFO-like), **flow maximization** (no holding back), and the **invariance
  principle**. ⚠ Exact seven-item wording reconstructed from the
  [2025 systematic review](https://www.sciencedirect.com/science/article/pii/S0191261525001225)
  abstract (full text 403'd) — re-verify before quoting.
- **Invariance principle** (Lebacque & Khoshyaran 2005, ISTTT 16): node outcomes must
  be unchanged when a queuing in-link's demand is replaced by its capacity (queues
  form instantly). Demand-proportional merge rules violate it; capacity-proportional
  rules satisfy it.
- Family framing: Smits, Bliemer, Pel & van Arem (2015), "A family of macroscopic
  node models" (*TR-B*); instances include Tampère 2011, Flötteröd & Rohde 2011,
  Gibb 2011 ([TRR 2263-13](https://journals.sagepub.com/doi/10.3141/2263-13)).
  Solution algorithms: iterative supply distribution by priority weights until fixed
  point. The 2025 systematic review is the best single map of this landscape.
- **Relevance:** whatever our engine does at intersections microscopically, our
  metrics/validation layer needs these constraints (esp. flow maximization + CTF) to
  judge whether junction throughput is theoretically sane.

## Calibration Practice (fitting the FD from data)

Canonical method — Dervisoglu, Gomes, Kwon, Horowitz, Varaiya (TRB 2009),
[Berkeley PDF](https://horowitz.me.berkeley.edu/Publications_files/All_papers_numbered/174C_Dervisoglu_TRB09.pdf),
on PeMS loop-detector data with triangular FD:

1. Split samples free-flow vs congested by speed (<60 mph ⇒ congested).
2. Free branch: constrained least squares through origin ⇒ v_f.
3. Capacity: max observed flow (triangle apex).
4. Congested branch: constrained least squares ⇒ wave speed w (bounded to 5–20 mph).
5. Separate congested fit quantifies capacity drop.

Follow-up: [Transportmetrica B 2017 automatic fitting](https://www.tandfonline.com/doi/full/10.1080/21680566.2016.1256239)
fits Wu's 5-parameter FD (v_f, w, free-flow capacity, queue discharge rate, k_j) —
capacity drop as a first-class parameter. Congested-branch fits are much less stable
than free-flow fits (scatter grows with density). Probe-trajectory alternatives exist
([Seo et al.](https://t2r2.star.titech.ac.jp/rrws/file/CTT100711992/ATD100000413/));
Bayesian treatments too ([Connected Corridors PGM paper](https://connected-corridors.berkeley.edu/sites/default/files/Probabilistic%20Graphical%20Models%20of%20Fundamental%20Diagram%20Parameters%20for%20Simulations%20of%20Freeway%20Traffic.pdf)).

Parameter cross-check identity (triangular FD): **w = q_max/(k_j − k_c)** — only three
of {v_f, w, q_max, k_j, k_c} are free; calibrate three, derive the rest.

⚠ Open discrepancy: PeMS-derived jam density 180–200 veh/km/lane (via a low-authority
tertiary summary) vs the 100–150 in the TU Delft calibrations — resolve against HCM or
Treiber & Kesting before hardcoding defaults. Urban values (HCM tradition, unverified
this session): saturation flow ≈ 1900 pc/h/lane-green, signal capacity = sat-flow × g/C.

## Visualization Traditions

- **Time-space (trajectory) diagrams / Marey graphs** — Marey's 1878 train-schedule
  graphic; freeway engineers use x–t trajectory plots for jam dynamics, arterial
  engineers for green-wave platoon analysis
  ([MBTA Viz wiki](https://github.com/mbtaviz/mbtaviz.github.io/wiki/The-Trains-Visualization)).
  Treiterer & Myers' helicopter-photo trajectory plots (the "phantom jam" figure) are
  the canonical historical dataset — raw data lost, re-digitized from the plots by
  Coifman et al. ([OSU PDF](https://ceg.osu.edu/sites/default/files/2022-06/Coifman_et_al_2018a.pdf)).
- **x–t speed/density heatmaps** — THE standard operational congestion view; waves
  appear as thin backward-sloping stripes. PeMS (~39k loop detectors, California)
  made these routine ([arXiv:0804.2982](https://arxiv.org/pdf/0804.2982);
  [arXiv:2312.03186](https://arxiv.org/html/2312.03186)). For a CTM engine the cell
  density matrix IS the heatmap.
- **(Oblique) cumulative N-curves** — plot N(t) at successive detectors; delays are
  distances between curves; Cassidy's oblique version (subtract background q₀·t)
  is the standard empirical bottleneck-analysis tool. N-curves are LTM's native state.
- These three are our observability system's target visuals alongside the map-based
  congestion heatmap: x–t heatmap per corridor, trajectory diagram per lane (micro
  engine has exact trajectories — better data than Treiterer's helicopter), N-curves
  per bottleneck.

## Open Questions (honest list)

1. Cause of capacity drop — unresolved in the literature.
2. Kerner vs FD orthodoxy — live dispute, largely definitional/falsifiability.
3. Interpretation of the fast characteristic in second-order models — Helbing &
   Johansson's resolution persuasive but not universally rehearsed.
4. Which FD shape to fit — data underdetermine the family; triangular wins on
   parsimony and exact solvability, not fit quality.
5. Entropy-solution realism at queue discharge — lead vehicle adopts capacity speed
   instantaneously; bounded-acceleration fixes break the clean theory.

## Master Source List

- Video (user-recommended intro): ["Waves, not cars - modelling traffic as a fluid"](https://www.youtube.com/watch?v=uv_pr-U6UTQ) — LWR/fluid view of traffic; good episode-style reference for how to present this material

- Lighthill & Whitham 1955, *Proc. Roy. Soc. A* 229:317–345; Richards 1956, *Oper. Res.* 4:42–51
- TU Delft OCW Traffic Flow Theory: [Ch. 4](https://ocw.tudelft.nl/wp-content/uploads/Chapter-4.-Fundamental-diagrams.pdf) | [Ch. 7](https://ocw.tudelft.nl/wp-content/uploads/Chapter-7-Traffic-states.pdf) | [Ch. 8](https://ocw.tudelft.nl/wp-content/uploads/Chapter-8.-Shock-wave-analysis.pdf); open textbook: Knoop, [*Traffic Flow Theory*](https://books.open.tudelft.nl/home/catalog/book/203)
- [Clawpack Riemann book LWR chapter](http://www.clawpack.org/riemann_book/html/Traffic_flow.html); [MIT 18.311 (Rosales)](https://math.mit.edu/classes/18.311/WWW2013/Notes/VarLecNotes18311igd.pdf); [Seibold IPAM tutorial](http://helper.ipam.ucla.edu/publications/avtut/avtut_16972.pdf); [KU Leuven H111](https://www.mech.kuleuven.be/cib/verkeer/dwn/H111part3.pdf); [FHWA TFT Ch. 5](https://www.fhwa.dot.gov/publications/research/operations/tft/chap5.pdf); [TRB EC149](https://onlinepubs.trb.org/onlinepubs/circulars/ec149.pdf)
- Daganzo 1995 Requiem, *TR-B* 29(4):277–286; Aw & Rascle 2000, *SIAM JAM* 60(3):916–938; Zhang 2002, *TR-B* 36(3):275–290; Helbing & Johansson 2009, [arXiv:0805.3402](https://arxiv.org/pdf/0805.3402)
- Newell 1993 (*TR-B* 27(4), Parts I–III), Newell 2002 (*TR-B* 36:195–205); Daganzo 2005 variational (*TR-B* 39(2), 39(10)); [Boyles CE 391F](https://sboyles.github.io/teaching/ce391f/class4.pdf)
- Treiber, Hennecke & Helbing 2000, *Phys. Rev. E* 62:1805 (IDM)
- Treiber, Kesting & Helbing 2010, [arXiv:1004.5545](https://arxiv.org/abs/1004.5545); Yuan, Knoop & Hoogendoorn 2015, [TRR 2491](https://journals.sagepub.com/doi/10.3141/2491-08); Kerner three-phase, [Wikipedia](https://en.wikipedia.org/wiki/Three-phase_traffic_theory)
- Daganzo 1994 CTM, *TR-B* 28:269–287; [Jin, arXiv:math/0309060](https://arxiv.org/pdf/math/0309060); [arXiv:1204.5510](https://arxiv.org/pdf/1204.5510) (jamitons); [arXiv:2111.04955](https://arxiv.org/pdf/2111.04955) (survey)
