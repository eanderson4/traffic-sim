# Model Formulation: Macroscopic Flow Models

> Source: domain/web research (no code exists yet — this file holds the mathematical
> formulation that any future implementation must encode; re-audit against code once
> the engine exists) | Researched: 2026-07-15 | Git HEAD: 071f5ad

Primary verified sources: TU Delft OCW "Traffic Flow Theory" chapters 4, 7, 8
(Hoogendoorn/Knoop, ocw.tudelft.nl — read page-by-page) and Helbing & Johansson
(arXiv:0805.3402v2).

## 1. The LWR Model

### Origin

- Lighthill, M.J. & Whitham, G.B. (1955). "On kinematic waves. II. A theory of traffic flow on long crowded roads." *Proc. Royal Society A*, 229(1178), 317–345. [Royal Society](https://royalsocietypublishing.org/rspa/article/229/1178/317/9548/On-kinematic-waves-II-A-theory-of-traffic-flow-on)
- Richards, P.I. (1956). "Shock waves on the highway." *Operations Research* 4(1), 42–51. Independent discovery; hence "LWR."

### Derivation from vehicle conservation

Let k(x,t) (also written ρ) be density [veh/km] and q(x,t) flow [veh/h], both smooth
derivatives of the cumulative vehicle count N(x,t). Vehicles are conserved on a link
without ramps:

```
∂k/∂t + ∂q/∂x = 0        (conservation of vehicles)
```

Closed by the **fundamental diagram** (equilibrium assumption) q = Q(k):

```
∂k/∂t + Q'(k) ∂k/∂x = 0
```

Physical content of the closure: speed reacts **instantaneously and locally** to
density, u(x,t) = V(k(x,t)). Source: TU Delft [Ch. 8](https://ocw.tudelft.nl/wp-content/uploads/Chapter-8.-Shock-wave-analysis.pdf), Sec. 8.1, eqs. 8.1–8.2.

Identity: q = k·u (flow = density × space-mean speed); q(k), u(k), u(q) carry the same
information (TU Delft Ch. 4, Sec. 4.1).

### Fundamental diagram closures

All from TU Delft [Ch. 4](https://ocw.tudelft.nl/wp-content/uploads/Chapter-4.-Fundamental-diagrams.pdf) unless noted. Special points: free speed u₀, capacity q_c,
critical density k_c, capacity speed u_c, jam density k_j.

**Greenshields (1935)** — linear u(k), parabolic q(k):

```
u(k) = u₀ (1 − k/k_j)
q(k) = k u₀ (1 − k/k_j)
k_c = k_j/2,   u_c = u₀/2,   q_c = u₀ k_j / 4
```

Validated originally on **seven** aerial-photography data points; criticized for
motorways: empirically k_c/k_j ≈ 0.2, not 0.5. (Greenshields 1935, *HRB Proc.* 14,
448–477; [TRB Circular EC149, "75 Years of the Fundamental Diagram"](https://onlinepubs.trb.org/onlinepubs/circulars/ec149.pdf).)

**Smulders (1990)** — linear u(k) free branch, hyperbolic congested branch:

```
u(k) = u₀ (1 − k/k_j)          for k < k_c
u(k) = γ (1/k − 1/k_j)         for k > k_c ,   γ = u₀ k_c
```

Calibrated per-lane on a Dutch motorway: u₀ = 110 km/h, k_c = 27, k_j = 110 →
q_c = 2241 veh/h; congested q(k) is linear so wave speed is constant −γ/k_j = −27 km/h.
(TU Delft Ch. 4 eq. 4.12; Smulders 1990, *TR-B* 24(2), 111–132.)

**Triangular / Daganzo (bilinear), associated with Newell** — the engineering default:

```
q(k) = u₀ k                                        for k ≤ k_c    (free branch)
q(k) = w (k_j − k) = q_c (k_j − k)/(k_j − k_c)     for k > k_c    (congested, constant wave speed −w)
```

Three parameters: u₀, q_c (or k_c), k_j. This is the FD of the Cell Transmission Model
(Daganzo 1994, *TR-B* 28(4), 269–287) and the FD for which Newell's simplified theory
is exact. ([Boyles CE 391F notes](https://sboyles.github.io/teaching/ce391f/class4.pdf); [Berkeley FD primer](https://connected-corridors.berkeley.edu/sites/default/files/Fundamental%20Diagram%20of%20Traffic%20Flow.pdf).)

**Trapezoidal**: q(k) = min(u₀k, q_c, w(k_j − k)) — capacity plateau; used in CTM
variants ([KU Leuven H111 part 3](https://www.mech.kuleuven.be/cib/verkeer/dwn/H111part3.pdf)).

**De Romph (1994)** — two branches (TU Delft Ch. 4, Example 40):

```
u(k) = u₀ (1 − αk)              for k < k_c     (free branch)
u(k) = γ (1/k − 1/k_j)^β        for k > k_c     (congested branch)
```

Amsterdam ring road, per-lane: u₀ = 110, k_c = 23, k_j = 100, β = 0.84,
α = 5.7×10⁻³; γ derived from continuity at k_c: γ = u₀(1 − αk_c)/(1/k_c − 1/k_j)^β
= 1672 → q_c = 2215 veh/h. Defect: β < 1 gives wave speed −∞ at jam density
(TU Delft Ch. 4 Remark 41).

**Wu (capacity-drop FD)** — two overlapping regimes (Edie-1961 tradition of a
discontinuous diagram): per-lane u₀ = 110 km/h, platoon speed u_p = 80 km/h,
k_j = 150 veh/km, free headway h_f = 1.2 s, congested h_c = 1.6 s → free capacity
2400 veh/h vs queue-discharge 1895 veh/h (21% drop; real-world drops usually smaller).
(TU Delft Ch. 4, Example 42.)

Others: Greenberg u = u_m ln(k_j/k), Underwood u = u₀e^(−k/k_m), Drake bell curve
([FHWA TFT monograph Ch. 5](https://www.fhwa.dot.gov/publications/research/operations/tft/chap5.pdf)).

### Typical realistic parameters (per lane)

| Quantity | Typical value | Source |
|---|---|---|
| Free-flow speed u₀ (motorway) | 100–120 km/h | Smulders/De Romph calibrations |
| Capacity q_c | 1800–2400 veh/h/lane (HCM ideal capacity grew 2000→2400, 1950→2000, 0.37%/yr; literature range 0.4–1.0%/yr) | TU Delft Ch. 4 fn. 4 |
| Critical density k_c | 23–27 veh/km/lane (k_c/k_j ≈ 0.2) | TU Delft Ch. 4 |
| Jam density k_j | 100–150 veh/km/lane (6.7–10 m gross spacing) | TU Delft Ch. 4 |
| Backward wave speed w | 15–20 km/h upstream, remarkably constant worldwide | Treiber/Kesting/Helbing 2010, [arXiv:1004.5545](https://arxiv.org/abs/1004.5545) |
| Capacity reductions | rain −9%, darkness −5%, combined to −12% | TU Delft Ch. 4, Table 4.1 |

Caveat (TU Delft Ch. 4): "The fundamental diagram is not a physical law" — depends on
road, vehicle mix, weather, lighting, limits; roadway FD ≠ sum of lane FDs because
lane distribution shifts with flow.

## 2. Method of Characteristics

```
k(x,t) = F(x − c t),   c = Q'(k)   (kinematic wave speed)
```

Each density value propagates unchanged along straight characteristics of slope Q'(k)
(TU Delft Ch. 8, eq. 8.3). Key facts:

- c is the speed of *information*, not vehicles. c = Q'(k) = V(k) + kV'(k) ≤ V(k) —
  characteristics never overtake cars in LWR.
- Concave Q ⇒ c decreases from u₀ (k→0) to negative beyond k_c. **Waves travel
  backward in congestion** because past the FD apex, adding density reduces flow.
- Triangular/Smulders FDs: Q' = u₀ on the whole free branch (waves move with traffic);
  congested branch is where disturbances steepen into discontinuities.

## 3. Shockwaves

Weak-solution discontinuity between upstream (k₁,q₁) and downstream (k₂,q₂); flux
continuity across the moving front gives the **Rankine–Hugoniot speed**:

```
ω = (q₁ − q₂) / (k₁ − k₂)      (slope of the chord on the FD)
```

(TU Delft Ch. 8, eqs. 8.4–8.6.) As k₂→k₁ the chord tends to the tangent: kinematic
wave = limiting shock.

**Entropy condition.** Weak solutions are non-unique; the Lax condition selects
physical shocks:

```
Q'(k_up) > ω > Q'(k_down)      (characteristics run INTO the shock)
```

For concave Q: shocks only where density **increases** in the driving direction
(vehicles hitting a queue tail); density decreases must open into rarefaction fans.
"Expansion shocks" are unstable/acausal. ([Clawpack Riemann book](http://www.clawpack.org/riemann_book/html/Traffic_flow.html);
[MIT 18.311 notes](https://math.mit.edu/classes/18.311/WWW2013/Notes/VarLecNotes18311igd.pdf);
[Seibold IPAM tutorial](http://helper.ipam.ucla.edu/publications/avtut/avtut_16972.pdf).)

## 4. Rarefaction Fans (green light)

k_up > k_down (jam behind stop line, empty ahead) → self-similar fan k = G(x/t), G
inverting Q', spanning wave speeds Q'(k_j) < 0 (start wave eating the queue) to u₀
(lead vehicle pulling away). With a **triangular** FD the fan degenerates: single
congested wave speed −w plus jump to the capacity state — which is why piecewise-linear
FDs make x–t shockwave diagrams entirely polygonal (TU Delft Ch. 8, Secs. 8.3–8.4).

## 5. Classic Worked Examples (TU Delft Ch. 8, verified)

### (a) Temporary blockade / traffic light (Sec. 8.3, Figs. 8.4–8.5)

Two-lane road, roadway totals, **triangular FD**: demand q₁ = 2500 veh/h,
q_c = 5000 veh/h, k_j = 250 veh/km, u₀ = 100 km/h ⇒ k_c = q_c/u₀ = 50 veh/km.
Red phase t₀→t₁ creates 4 states (approach, jam, empty, discharge):

- **Stop wave** (1→2): ω = q₁/(k₁ − k_j) = 2500/(25 − 250) = **−11.1 km/h**
- Queue head at stop line: ω = 0; front of last-passed traffic: ω = u₀
- **Start wave** (2→4): ω = q_c/(k_c − k_j) = **−25 km/h**
- Start wave overtakes stop wave at t₂; residual shock (1↔4) then moves downstream.
  Total delay = area between cumulative curves; x–t diagram is a polygon.
- Redone with a **smooth concave Q(k)** (§8.4.1, a deliberately different variant
  with u_c = 80 km/h < u₀): start wave becomes a fan, and theory predicts the first
  vehicle exits at u_c instantly — flagged unrealism: "shock wave theory does not
  describe the acceleration – nor the deceleration – of the first vehicle in the
  queue." (Note u_c < u₀ is a property of the smooth-FD variant only; on a triangular
  FD the capacity-point speed is u₀.)

### (b) Stationary bottleneck

Demand > bottleneck capacity ⇒ **frontal stationary shock** pinned at the bottleneck
entrance, **backward-forming shock** at the queue tail, **backward recovery wave**
after demand subsides. Full front taxonomy (Sec. 8.6, Fig. 8.10): frontal stationary,
backward forming, backward recovery, rear stationary, forward recovery, forward forming.

### (c) Moving bottleneck (Sec. 8.5, Figs. 8.8–8.9)

Slow vehicle at speed v̂ with passing capacity q_bn (worked numbers: roadway capacity
4500 veh/h, u_c = 90, k_j = 250; bottleneck v̂ = 20 km/h over 4 km, capacity 1800).
Two methods:

1. In the q–k plane, admissible up/downstream states lie on the **line of slope v̂
   through the bottleneck capacity point** (transitions must travel at v̂).
2. **Moving-observer transform**: x' = x − v̂t, q' = q − kv̂; conservation still holds
   with FD Q'(k) = Q(k) − kv̂ — reduces to a stationary bottleneck (eqs. 8.7–8.9).
   Later formalized in variational theory. (Also Newell 1998 *TR-B* 32(8) 531–537;
   Muñoz & Daganzo 2002; [Simoni & Claudel](https://www.sciencedirect.com/science/article/pii/S2046043016301459).)

## 6. Micro ↔ Macro Bridge

### Car-following steady states imply an FD

Stationary following with speed-dependent gross spacing s(u) ⇒ k = 1/s(u), q = ku.
Linear rule s = s₀ + T_r·u gives k(u) = 1/(s₀ + T_r u) — a linear congested branch
with jam density 1/s₀ and wave speed −s₀/T_r. Constant net time headway ⇒ triangular
congested branch (TU Delft Ch. 4, eqs. 4.1–4.3, 4.19–4.23). A safe-distance model with
braking terms (eqs. 4.30–4.35) makes capacity an explicit function of reaction time,
jam density, braking capability, aggressiveness — explaining the historical secular
growth of capacity.

### Newell's simplified car-following = LWR with triangular FD

Newell 2002 (*TR-B* 36(3), 195–205): follower trajectory = leader trajectory shifted
by time lag τ and space offset δ:

```
x_i(t) = min( x_i^free(t),  x_{i−1}(t − τ) − δ )
δ = 1/k_j ,   τ = 1/(w k_j) ,   w = δ/τ
```

**The exact solution of this microscopic model coincides with LWR under a triangular
FD (u₀, w, k_j).** Antecedent: Newell 1993 "Simplified theory of kinematic waves"
Parts I–III (*TR-B* 27(4)) — the cumulative-count N-curve reformulation. Used by
mesoscopic simulators (e.g. [UXsim, arXiv:2309.17114](https://arxiv.org/pdf/2309.17114)).
([Wikipedia: Newell's model](https://en.wikipedia.org/wiki/Newell%27s_car-following_model);
[Boyles notes](https://sboyles.github.io/teaching/ce391f/class4.pdf).)

### IDM steady state

IDM desired gap s*(v,Δv) = s₀ + vT + vΔv/(2√(ab)); equilibrium (Δv=0, a=0):
s_e(v) = (s₀ + vT)/√(1 − (v/v₀)^δ) → s ≈ s₀ + vT for v ≪ v₀ — exactly the linear-headway
congested branch. IDM thus embeds a smooth FD closure but can be string-unstable
around it — the ingredient LWR lacks. (Treiber, Hennecke & Helbing 2000, *Phys. Rev. E*
62:1805; [Wikipedia IDM](https://en.wikipedia.org/wiki/Intelligent_driver_model).)

### Variational theory (Daganzo 2005)

Rewrite LWR on the Moskowitz surface N(t,x) (k = −∂N/∂x, q = ∂N/∂t):
Hamilton–Jacobi equation ∂N/∂t = Q(−∂N/∂x). Concave Q ⇒ exact least-cost-path
(Hopf–Lax) solution: N = min over observer paths of boundary value + cost, cost rate =
Legendre transform of Q. For triangular FDs only a sparse set of paths matters ⇒ exact
grid-free solutions ([Mazaré et al. 2011, *TR-B* 45:1727–1748](https://ideas.repec.org/a/eee/transb/v45y2011i10p1727-1748.html)).
Payoffs: exact moving bottlenecks as internal boundary conditions; backbone of the MFD
literature. (Daganzo 2005, *TR-B* 39(2):187–196 and 39(10):934–950.)

## 7. Numerical Methods

### Godunov scheme = demand/supply method

Lebacque (1996, ISTTT 13) showed essentially all sensible LWR discretizations are
Godunov schemes, with the numerical flux at a cell boundary written as
**min(demand, supply)** — which also unifies boundary conditions and intersections.
([Lebacque 1996 record](https://www.semanticscholar.org/paper/d2ec1044c96671fdd7607c3a0a9bcf75005fbcd4);
restated in [Jin, arXiv:1005.4624](https://arxiv.org/pdf/1005.4624).)

For any unimodal FD with critical density ρ_c:

```
Demand  D(ρ) = Q(min(ρ, ρ_c))    — increasing branch, saturating at q_max
Supply  S(ρ) = Q(max(ρ, ρ_c))    — q_max below ρ_c, decreasing branch after
```

Triangular FD specialization:

```
D(ρ) = min( v_f·ρ, q_max )              (sending / demand)
S(ρ) = min( q_max, w·(ρ_jam − ρ) )      (receiving / supply)

q_{i→i+1} = min( D(ρ_i), S(ρ_{i+1}) )
ρ_i(t+Δt) = ρ_i(t) + (Δt/Δx)·( q_{i−1→i} − q_{i→i+1} )
```

### Cell Transmission Model (Daganzo 1994/1995)

Daganzo 1994, *TR-B* 28(4):269–287 (link model); 1995, *TR-B* 29(2):79–93 (networks).
Vehicle-count form (occupancy n_i, verified from [IIT Bombay CTM notes](https://www.civil.iitb.ac.in/tvm/1100_LnTse/514_lnTse/plain/plain.html)):

```
n_i(t+1) = n_i(t) + y_i(t) − y_{i+1}(t)
y_i(t)   = min[ n_{i−1}(t),  Q_i(t),  α·(N_i(t) − n_i(t)) ],   α = w/v
```

with Q_i = max flow per step across boundary i, N_i = holding capacity of cell i.
**Cell size / CFL**: cell length = v_f·Δt exactly (a free-flow vehicle crosses one
cell per step — kills numerical diffusion on the free branch; congested waves with
w < v_f still smear). General stability: Δt ≤ Δx/v_f
([Wikipedia CTM](https://en.wikipedia.org/wiki/Cell_Transmission_Model)). The Lagged
CTM (Daganzo 1999, [Berkeley PDF](http://faculty.ce.berkeley.edu/daganzo/Publications/ISTTT_PA.PDF))
improves congested-wave accuracy via a lagged downstream density.

**Merge (2→1), Daganzo priority rule** — demands S₁, S₂, downstream supply R:
if S₁+S₂ ≤ R both pass; else with priorities p₁+p₂ = 1:

```
y₁ = mid( S₁, R − S₂, p₁·R )
y₂ = mid( S₂, R − S₁, p₂·R )      (mid = median)
```

Typical p_i ∝ upstream capacities (satisfies the invariance principle, §node models
in [standards-and-patterns.md](./standards-and-patterns.md)). The model allocates
capacity; it "does not consider the actual merging process between vehicles"
([Wikipedia: Newell–Daganzo merge model](https://en.wikipedia.org/wiki/Newell%E2%80%93Daganzo_merge_model)).
⚠ mid()-form reconstructed from secondary sources; verify against the
[part II working paper](https://scispace.com/pdf/the-cell-transmission-model-network-traffic-3scj5m3wn2.pdf).

**Diverge (1→2), FIFO rule** — turning fractions β_j:

```
y_total = min( S, min_j( R_j/β_j ) ),    y_j = β_j·y_total
```

Known pathology: one blocked off-ramp (R_j = 0, β_j > 0) freezes ALL flow through the
diverge, including traffic bound for empty links — right for single-lane roads, too
harsh for multilane ("partial FIFO" relaxations exist; Carey et al. 2022,
[ScienceDirect](https://www.sciencedirect.com/science/article/abs/pii/S0191261522000571)).

### Link Transmission Model (Newell 1993 → Yperman 2005/2007)

Newell's **three-detector formula** (exact LWR solution for triangular FD, in
cumulative counts N(x,t)):

```
N(x,t) = min( N(x_up,   t − (x − x_up)/v_f),
              N(x_down, t − (x_down − x)/w) + ρ_jam·(x_down − x) )
```

LTM (Yperman, KU Leuven PhD 2007) tracks N-curves only at link boundaries:

```
Sending:    S_a(t) = min( N_up(t + Δt − L/v_f) − N_down(t),  Q_a·Δt )
Receiving:  R_a(t) = min( N_down(t + Δt − L/w) + ρ_jam·L − N_up(t),  Q_a·Δt )
```

Node models combine S/R across links; transfer flows increment boundary N-curves.
Per-link state is O(1) (two N-curves) vs CTM's per-cell state: complexity ~n× smaller
(n = cells/link), and for triangular FDs + piecewise-constant boundaries LTM is
**exact** — no numerical diffusion. Trade-off: no native interior densities
(reconstruct post-hoc via the three-detector formula).
⚠ Discrete equations stated from secondary literature; verify against Yperman 2007 or
the LIFT paper's Algorithm 1 ([arXiv:2606.09282](https://arxiv.org/html/2606.09282)).

### Signals in macroscopic models

Canonical: time-varying capacity Q_ij(t) = g_ij(t)·Q_sat with g ∈ {0,1} or a green
ratio for coarse steps — CTM part II's Q_i(t) is time-indexed for exactly this.
Refinements model discharge ramping up during early green, turn bays, shared lanes
([Improved CTM for signalized intersections](https://civilejournal.org/index.php/cej/article/view/2077)).
Mesoscopic equivalents: SUMO's `--meso-tls-penalty` / `--meso-tls-flow-penalty`
([SUMO Meso docs](https://sumo.dlr.de/docs/Simulation/Meso.html)).
