# Prototypes — feeling the engine forks

Throwaway HTML+JS demos, one per **genuinely hard-to-reverse fork** in the engine
design. The point is not code quality — it's to *feel* what each direction is like
before ADRs lock it in. Nothing here is engine code; nothing here has tests, on
purpose. Open any `index.html` directly in a browser (no build, no server needed).

All four dogfood pieces of ADR-0005 where applicable: fixed 100 ms tick with a
wall-clock accumulator loop, ballistic integrator + stop override, seeded
deterministic RNG (mulberry32), and the dark-blue x–t heatmap convention from the
NGSIM analysis (dark = congested, x up).

## The fork map

| # | Fork | Options | Prototype shows |
|---|------|---------|-----------------|
| 1 | **What is the world made of?** | continuous 1-D positions on a lane graph · cellular grid · aggregate densities (CTM) | `1-continuous-micro` vs `2-cellular-automata` |
| 2 | **What happens inside an intersection?** | conflict-point gap acceptance (interior abstract) · internal lanes with physical occupancy | `3-intersection-interior` (side by side, identical seeded demand) |
| 3 | **Where do drivers live?** | in-engine · asynchronous controllers over NATS (stale observations, delayed intents) | `4-controller-latency` |

Deliberately **not** prototyped:
- **CTM/macro as the core engine** — ruled out by VISION (no vehicles → no human
  drivers, no lane-level metrics). It stays as a future validation-oracle /
  fast-preview layer (`docs/kb/raw/domain-macroscopic-flow-models/`).
- **Lane-graph representation** (lanes-first vs edges-with-lanes) — a data-model
  fork you can't feel in a canvas demo; goes through `arch-road-graph-model`
  research instead.
- **Go/NATS themselves** — these demos are about simulation semantics, not the
  runtime stack.

## 1-continuous-micro — the ADR-hypothesis engine

IDM + MOBIL on a 3-lane, 1200 m mainline with an on-ramp (200 m acceleration
lane), instant lane hops, mandatory-merge urgency, **no cooperation**. Live x–t
heatmap. Reproduces: merge-bottleneck jams, string instability at low IDM *a*
(phantom jams), and the SUMO "stuck at end of acceleration lane" failure mode —
the concrete case for cooperation intents over NATS.

## 2-cellular-automata — the grid alternative

Nagel-Schreckenberg on the same road. Timestep (1 s), cell size (7.5 m) and model
are one welded choice; motion is quantized to 27 km/h steps. Wave physics and the
heatmap stay qualitatively right; the benchmark button shows the raw speed
ceiling. Feel-check: could a human player ever drive this?

## 3-intersection-interior — conflict points vs internal occupancy

Two-way-stop intersection, HCM critical gaps, two interior models fed identical
seeded arrivals. Model A decides everything at the stop line and never yields
inside — fast and permissive, but under load vehicles drive through each other
(the interior has no physics). Model B makes the interior real estate: vehicles
wait at conflict-cluster entries, never inside a zone they haven't cleared —
collision-free by construction, lower capacity, physically real spillback (and
gridlock risk) when an exit blocks.

**Finding from building it:** Model B took four successive deadlock-trap fixes
to survive an hour of traffic — vehicles creeping past yield walls, parking
inside zones they'd committed to clear, phantom leaders on laterally-diverged
paths, and false merge-followers comparing exit coordinates across the box. The
surviving structure (zone clusters + wait-only-at-cluster-entry + rank-then-id
tie-breaks) is a miniature of what SUMO's internal-junction machinery does. If
we choose interior occupancy, this semantics IS the work.

## 4-controller-latency — the NATS fork, measured

Same road as #1, but drivers run as asynchronous controllers: observe a snapshot
k ticks old, decide every m ticks, intents apply j ticks later, engine holds last
commanded acceleration and keeps an independent physical backstop
(required-deceleration emergency braking, lane-change validation). Latency
behaves as added reaction time (Kesting & Treiber stability boundary).

**Measured (defaults, 20 min sim, all-remote vs in-engine):** a 0.2 s round trip
(k=1, j=1 — the honest never-block-the-tick baseline from ADR-0005) is
behaviorally invisible: 101 vs 102 km/h mean speed, identical throughput, zero
backstop engagements. 0.6 s: still fine (97 km/h). Between 0.6 s and 1.0 s the
flow **collapses** (13 km/h, hundreds of backstop engagements) — the string-
instability cliff, right where the reaction-time arithmetic predicts. The
architecture's latency budget is real but generous: 1–2 ticks are free.

**Second finding:** engine-side intent validation must be at least as permissive
as any controller policy it hosts — a stricter engine check (gap 0.5 m vs the
controller's urgency-relaxed 0.3 m) produced a merge livelock: the controller
re-proposing forever, the engine rejecting forever. Contract material for
`concept-vehicle-controller-interface`. Also: "what does the engine do between
intents" (hold accel? hold speed? decay?) is itself a contract decision — held
stale acceleration is what makes high latency catastrophic here.

## Questions these should answer before the next ADRs

1. Is continuous-micro visually and dynamically worth its cost over CA? (expected: yes, decisively)
2. Do instant lane hops read acceptably at 100 ms ticks, or do we need
   duration-based lateral motion in v1?
3. Which interior model do we commit to in the map contract — and is Model B's
   gridlock risk acceptable given our no-teleport policy?
4. Is 1-tick controller latency behaviorally invisible (validating async intents),
   and where is the actual latency budget before traffic destabilizes?
5. How much does merge throughput suffer without cooperation — do we need
   cooperation intents in v1?
