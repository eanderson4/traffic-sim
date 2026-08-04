# Bottleneck Town — an authored corridor, four options, measured

A fictitious small town, built by hand rather than imported. Main Street
runs west→east through four signalized cross-street intersections and
carries every through trip in the network. Nothing here is a real road: the
geometry, the signal programs and the origin–destination demand are all
authored in
[`scripts/demos/bottleneck_town.py`](../../scripts/demos/bottleneck_town.py),
which is the durable source — everything under `data/` is gitignored and
regenerated from that file.

```
python3 scripts/demos/bottleneck_town.py --out data/pods/bottleneck-town --ticks 15000
python3 scripts/demos/bottleneck_town.py --check      # the paths the router will pick
scripts/demos/bottleneck-town.sh all 10               # pod + A/B + record + bake
```

The saved reports are checked in under
[`reports/`](reports/), so every table below re-derives without running a
simulation:

```
python3 scripts/whatif.py --report docs/show/reports/bottleneck-town.json --metric speed_kmh
python3 scripts/whatif.py --report docs/show/reports/bottleneck-town.json --metric completed
python3 scripts/whatif.py --report docs/show/reports/bottleneck-town-heldout.json
python3 scripts/whatif.py --report docs/show/reports/bottleneck-town-null.json   # the null test
```

Read [audit.md](audit.md) for the methodology this is held to. Every number
below is a paired-seed A/B against the identical baseline on identical
seeds. Primary metric: network mean speed by Edie's definition
(`sum_dist_m / sum_time_s` over the measurement window, every vehicle-second
in the network, completed trips or not), guarded by total vehicle-distance
so an option cannot win by moving less traffic.

## The town

```
                    N1      N2      N3      N4          ← north cross-street portals
                     |       |       |       |
   W ────╮           |       |       |       |        ╭──── E
          ╲──────────┴───────┴───────┴───────┴────────╱     Main St, 2 lanes each way,
                    J1      J2      J3      J4              50 km/h, signals 480/520/450 m apart
                     |       |       |       |
                    S1      S2      S3      S4          ← south cross-street portals
```

* **Main Street** — 2 lanes per direction, 50 km/h, sagging 300 m south
  through the middle of the map: the old road bends round the town site.
  Four signalized junctions on the straight middle section, 480 / 520 /
  450 m apart. Each approach flares to a left-turn bay at the stop line.
* **Cross streets** — 1 lane per direction, 40 km/h, a portal north and
  south of every junction.
* **Signals** — one 86 s fixed-time program per junction, four green
  phases each followed by 3 s amber and 2 s all-red:

  | phase | green | movements |
  |---|---:|---|
  | 1 | 36 s | Main Street through + right, both directions |
  | 2 | 8 s | Main Street protected lefts, both directions |
  | 3 | 11 s | cross street southbound, all movements |
  | 4 | 11 s | cross street northbound, all movements |

  Split phasing on the cross streets and protected-only lefts on Main
  Street mean **no two movements in a phase ever cross or merge**, so
  `foesCross`/`foesMerge` stay empty at the signalized junctions exactly as
  the netimport-produced networks leave them, and the signal plus the
  kernel's box-exit check do all the adjudication. In the baseline every
  junction runs offset 0 — the lights all change together, which is what
  an uncoordinated small town looks like.
* **Demand** — 480 veh/h in at the west portal, 420 at the east, 75 at each
  of the eight cross-street portals: 1,500 veh/h total, 6% trucks, Poisson
  arrivals, every vehicle given an authored destination portal (ADR-0021
  `destinations`) rather than the driver's own exit draw. 60% of the Main
  Street inflow is through traffic; the rest turns off at a cross street.
* 200 lanes, 4 signal programs, ~4.4 lane-km of Main Street.

## Is it actually congested?

Yes, and the congestion is signal capacity. Baseline, seed 1000, 15,000
ticks, measured from tick 6,000 (`scripts/demos/town_report.py`):

| | |
|---|---:|
| network mean speed | **22.3 km/h** |
| Main Street eastbound | 24.4 km/h (50 km/h posted) |
| Main Street westbound | 23.1 km/h |
| cross streets | 18.3 km/h (40 km/h posted) |
| between-junction free-running sections | 44–46 km/h |

| signalized approach | mean queue | delay | flow |
|---|---:|---:|---:|
| J1 ← Main St, west approach | **82 m** | **111 s/veh** | 427 veh/h |
| J4 ← Main St, east approach | **87 m** | **134 s/veh** | 438 veh/h |
| J2 ← Main St | 40 m | 57 s/veh | 374 veh/h |
| J3 ← Main St | 41 m | 57 s/veh | 416 veh/h |
| J2 ← cross street (SB) | 22 m | 160 s/veh | 81 veh/h |
| J4 ← cross street (NB) | 39 m | 225 s/veh | 91 veh/h |

Queue length is the time-weighted mean occupied metres over the whole
cycle, so the queue at the start of green is roughly twice it — ~170 m,
about 17 vehicles per lane, at J1 and J4. Vehicles run at 44–46 km/h
between junctions and lose 40–130 s at each stop line: the corridor is not
volume-limited, it is red-limited.

**The corridor sits at its signal capacity, and the engine's signal
capacity is not the textbook one.** Under the ADR-0010 don't-block-the-box
discipline a vehicle is held at the stop line until its leader has cleared
the junction box *and* left `length + s0` of room on the exit lane, so
discharge serializes to roughly one vehicle per box traversal. Measured
here: **~200 veh/h per through lane at a 36/86 green ratio — a 7.5 s
saturated headway**, against a textbook 1.5–2.5 s. That is not a defect of
this scenario: the engine's own `signal-4way` fixture measures 7.19 s/veh
and documents the acceptable band as [1.5, 9] s. Demand was calibrated to
the engine's capacity rather than to Webster's, which is why 1,500 veh/h
congests a four-lane main street here. **Do not read the absolute volumes
as realistic.** The corridor is a valid *relative* test bed — every arm
runs under the same discharge model — but a real main street at 22 km/h
would be carrying two to three times this traffic.

### Is the measurement window at equilibrium?

Not exactly, and it matters enough to state. Network mean speed by
1,500-tick bucket, baseline, pooled over 10 seeds:

| ticks | 0–1.5k | 1.5–3k | 3–4.5k | 4.5–6k | 6–7.5k | 7.5–9k | 9–10.5k | 10.5–12k | 12–13.5k | 13.5–15k |
|---|---|---|---|---|---|---|---|---|---|---|
| km/h | 33.8 | 27.9 | 24.5 | 23.5 | 23.9 | 23.5 | 23.4 | 21.8 | 21.8 | 21.0 |

The fill-up transient is over by tick ~4,500, so **the 6,000-tick warmup is
enough** for the purpose it exists for. But the corridor is very slightly
supersaturated, so queues keep creeping: the baseline loses another ~12%
of its speed between tick 6,000 and tick 15,000, and the mean queue on the
two outer Main Street approaches grows from ~250 m at tick 6,300 to ~345 m
at tick 15,000. This is a peak-period build-up, not a steady state. It is
present in every arm and the comparison is paired, so it cancels for the
A/B; it does mean the absolute baseline number depends on where you stop
measuring.

## The options

Four on the menu. Each is a complete authored network — no delta mechanism,
no lane cloning:

| option | what changes | new road |
|---|---|---:|
| `bypass-north` | a new 1-lane-each-way road across the **inside** of Main Street's bend, 3,656 m against Main Street's 3,842 m between the same two points | 3.7 km |
| `connector-south` | a new 1-lane-each-way road between the same two points round the **outside** of the bend, crossing all four south cross-street legs on the way | 4.4 km |
| `retime-short` | 86 s cycle → 66 s, same phase proportions, same geometry | none |
| `green-wave` | same phases, same green times, offsets set for an eastbound progression at 45 km/h | none |

**`add-lane` (Main Street 2 → 3 lanes each way) was measured and then
dropped from the menu.** It works — +16.5%, p<0.0001, and its added lane
genuinely carries traffic (below) — but it is the least legible option on
screen: at corridor zoom a third lane is a slightly thicker line, where a
new road appearing and a signal cascade turning green in sequence are both
things a viewer can watch happen. Its numbers stay in the results table and
in `all_tested` in the quiz JSON, so the curation is auditable rather than
hidden.

## What the router will do, before any traffic runs

This is the most important thing to understand about the two new-road
options, and it is a property of the engine, not of the town.

**The engine routes on static shortest path by distance**
(`engine/routing.go`): a reverse Dijkstra over lane lengths, computed once
per destination, with no congestion feedback and no re-routing. A new road
carries traffic **if and only if it shortens some O-D pair's distance — and
then it carries ALL of that pair's traffic.** There is no equilibrium
assignment and no detour tolerance. A bypass 10% longer than the road it
relieves is a guaranteed no-op *for a routing reason*, and reporting that as
"building does not help" would be wrong.

So the two roads are placed to make that legible instead of hiding it.
`bottleneck_town.py --check` runs the same Dijkstra the kernel will and
prints the answer before a vehicle moves:

| O-D pair | uses the new road? | margin |
|---|---|---:|
| **bypass-north** | | |
| W → E, E → W (through traffic) | **yes** | 133 m shorter |
| W → S4, W → N3, S1 → S4, S1 → E | no | 0 |
| **connector-south** | | |
| W → E, E → W (through traffic) | no | 0 |
| S1 → S4 | **yes** | 899 m shorter |
| W → S4 | **yes** | 212 m shorter |
| S1 → E | **yes** | 196 m shorter |

The two roads therefore serve **disjoint traffic**: the bypass takes the
through movement and nothing else, the connector takes the south-side local
movements and no through traffic at all. That is "build here vs. build
there" with the confound removed — and it is a *distance* question, not a
congestion question, because that is all this router can see.

### Measured, not predicted

Share of network vehicle-distance each new road actually carried, pooled
over the 10 discovery seeds from tick 6,000:

| | share of network veh-km | its own mean speed |
|---|---:|---:|
| bypass-north | **23.4%** | 46.9 km/h |
| connector-south | *(pending re-measure — see below)* | |

The bypass is used heavily, and the prediction and the realisation agree on
*which* traffic uses it. Main Street's own vehicle-distance falls from
7,418 km (baseline, pooled) to 5,286 km with the bypass. For contrast, the
Chicago widening work (see [audit.md](audit.md)) had its added lanes
carrying 4.8–6.6% of the corridor and could not distinguish "widening does
not help" from "we did not really widen it". Nothing like that applies here.

**connector-south's row is blank on purpose.** It read 29.3% / 45.3 km/h,
and Main Street 5,670 km, on the build where the connector crossed the four
cross streets as the priority leg. Signalising those crossings changes that
arm's network, so those three figures describe a road that is no longer the
one being measured. They are not adjusted or estimated here — the pod has no
`corridors.json`, so the A/B report does not carry per-road attribution and
there is nothing to re-derive them from without another instrumented run.
The whole-network result for this arm (the Results table) is unaffected and
current; only the per-road breakdown is missing.

`add-lane` gets the same test, by lane index on Main Street:

| | baseline | add-lane |
|---|---:|---:|
| lane 0 (median) | 56.0% | 46.3% |
| lane 1 | 43.2% | 24.2% |
| lane 2 (the added lane) | — | **28.8%** |
| lane 3 (left-turn bay) | 0.9% | 0.7% |

An added lane carrying 28.8% of Main Street's vehicle-distance where an
evenly used third lane would be 33% is a real widening.

**Known defect in the lane numbering, and it is worse than a label.**
`engine/netfile.go` defines `edgeIndex` 0 as the *rightmost* lane (SUMO
convention), but this generator lays its lanes out along the left normal, so
index 0 is the lane nearest the centreline and the highest index is the kerb.
The labels above follow the network as generated, not the convention.

The junction builder keys off the convention — `right_lane=0`, left bay at
the highest index — so mirrored, **the right turn is issued from the lane
nearest the centreline and crosses the through lane beside it inside the
shared `main_thru` green**, and the two opposed protected lefts cross each
other inside `main_left`. These junctions ship with `foesCross`/`foesMerge`
empty precisely because the phases are supposed to be conflict-free by
construction (see "The town" above), so **nothing arbitrates those
crossings**. External review confirmed both geometrically on the generated
network.

What it does and does not cost: measured impact is 0-1 collision observations
on an idle run, and every arm carries the identical defect, so the A/B deltas
in the Results table stand. But it is a latent conflict source at higher
demand or under box spillback, and it is *visible* in the baked replay —
right-turners swing out of the inside lane across the outside one.

It is not fixed here because the obvious fix is not sufficient, which was
tested rather than assumed. Flipping the offset does correct the movement
assignment, but `chain()` maps lane *i* to lane *i* while the flared approach
carries one extra lane, so every chained lane picks up a 3.50 m — one full
lane width — lateral jog at the bay edge. The flare can only widen kerb-ward
(the opposing carriageway owns the other side of the axis), so under a
corrected index the added lane is necessarily a kerbside *right*-turn pocket,
whereas this scenario is built around a left-turn bay with no upstream
predecessor — the property that keeps every through lane's leftmost successor
a through movement, which the `Successors[0]` routing fallback depends on.
Correcting it properly means redesigning the bay, re-validating that routing
property, and re-running the pod.

## Results

10 paired seeds (1000–1009), 15,000 ticks, warmup 6,000, `-capacity 40000`.
Primary metric network mean speed; every arm delivered 100% of its demand.

| option | network speed | Δ% | p | Cohen's d | VMT Δ% | guard | held-out Δ% |
|---|---:|---:|---:|---:|---:|---|---:|
| **bypass-north** | 28.72 | **+25.3%** | 2.1e-06 | +3.38 | +0.9% (n.s.) | ok | **+24.7%** |
| green-wave | 26.83 | +17.0% | 4.4e-09 | +6.87 | +0.2% (n.s.) | ok | +16.1% |
| *(add-lane, off menu)* | 26.67 | +16.3% | 1.3e-05 | +2.70 | −0.1% (n.s.) | ok | +17.2% |
| connector-south | 24.72 | +7.8% | 4.7e-03 | +1.18 | +13.7% | ok | +10.5% |
| **retime-short** | 20.05 | **−12.5%** | 2.8e-06 | −3.25 | −6.7% | fails | −10.6% |
| baseline | 22.92 | — | — | — | — | — | — |

Held-out column: seeds 2000–2005, six seeds used for nothing else, run
after the discovery set was fixed. Every effect reproduces within 2.7
percentage points and every sign holds.

Four options at α=0.05 is a Bonferroni threshold of 0.0125; the largest
p-value among the four menu options is connector-south's 4.7e-03, so
**every result survives correction**. Worth noting that this is the first
version of this table where correction is doing any work: previously the
largest menu p-value was 3.0e-05, comfortably clear by any standard, and
connector-south now clears the corrected threshold by a factor of 2.7.

**These numbers are the second measurement of this pod, and the first one
ranked differently.** Two things changed between them, and only one of them
mattered:

- **connector-south is now signalised** where it crosses the four cross
  streets. It previously crossed them as the PRIORITY leg, so four existing
  arterials yielded to a brand-new road — worth real time to the connector,
  taken straight out of the cross streets. On the same 86 s fixed-time cycle
  as everything else in the town, its advantage falls from **+19.5% to
  +7.8%**: second place to fourth in this table, or third of the four menu
  options. The first result had the sign right and the reason wrong.
- **the batch was re-run serially.** The earlier one ran 5-up and tripped
  `serve`'s uncontrolled-coasting gate; see
  [Silent Fidelity Failures §7](../kb/articles/concepts/silent-fidelity-failures.md).
  This turned out **not** to have changed any answer — every arm agrees with
  the contaminated batch within 0.7 pp — but it is why these runs are
  quotable and those were not. `docs/show/reports/bottleneck-town.json`
  carries `"voided": []`, which is the record that no run was silently
  dropped.

### Secondary metrics, same 10 paired seeds

The primary metric is not the whole story, and on this corridor it disagrees
with the others in an instructive way:

| option | speed | trips completed | mean trip time | mean time loss |
|---|---:|---:|---:|---:|
| bypass-north | +25.3% | **+15.7%** (p=8e-05) | −15.5% | −37.0% |
| green-wave | +17.0% | +11.0% (p=4e-07) | −9.9% | −24.6% |
| add-lane | +16.3% | +8.0% (p=0.030) | −9.4% | −21.3% |
| connector-south | +7.8% | **−6.2% (n.s., p=0.09)** | **+4.9%** | −2.6% (n.s.) |
| retime-short | −12.5% | −12.8% | −3.6% (n.s.) | +0.5% (n.s.) |

**`connector-south` is the option this table catches**, and the secondary
metrics catch it harder than the headline does. It posts **+7.8%** on
network mean speed while finishing **6.2% FEWER trips** than the baseline
(p=0.09, so not significant — but the point is that it is certainly not
*more*) and taking **4.9% LONGER per trip** (p=0.007). Its network
vehicle-distance rises **13.7%** (p=2e-04).

Put together: it moves traffic further, on a long uncongested road, and
Edie's network mean speed is distance-weighted — so the headline metric goes
up while strictly less gets finished and each journey takes longer. Time
loss barely moves (−2.6%, n.s.). The VMT guard is designed to catch an
option that raises speed by carrying *less* traffic; it does not catch one
that carries the same traffic *further*, which is why trips-completed and
trip time have to be quoted next to speed on this menu.

*(The per-road breakdown that used to sit here — the connector's own share
of network vehicle-distance, its mean speed, and the cross-street slowdown —
was measured on the pre-signalisation build and does not survive the
network change. It needs re-deriving from a corridor-annotated run; the pod
has no `corridors.json`, so the A/B report cannot answer it. Rather than
carry numbers known to be stale, they are omitted until re-measured.)*

That is a real finding about the option, but it is also a warning about the
metric: **the VMT guard catches an option that moves LESS traffic; it does
not catch one that moves the same traffic FURTHER.** Trips completed is what
separates them here.

### The noise floor, measured on this pod

Two byte-identical copies of the baseline directory (`base` and `base2` in
`data/pods/bottleneck-town-null`, `diff -r` clean), 8 shared seeds,
treatment = nothing at all
([`reports/bottleneck-town-null.json`](reports/bottleneck-town-null.json)):

| metric | mean Δ | p | per-seed sd |
|---|---:|---:|---:|
| network speed | −0.003% | 0.954 | **0.49%** |
| vehicle-distance | −0.108% | — | 0.16% |
| trips completed | +0.072% | — | 0.62% |

So the practical floor here is ~0.5%, and the smallest effect on the menu
(connector-south, +7.8%) is 16× it — the next smallest (green-wave, +17.0%)
is 35×. Nothing in this study is close to the noise.

## Why each option does what it does

**`bypass-north` (+25.3%, the winner)** takes every through trip off the
corridor. W→E travel time falls from 543 s to 375 s and the driven distance
falls with it, 3.921 km to 3.839 km (the router's own margin is 133 m on the
lane pair `--check` prints; 82 m averaged over all four), so it is a rare
option that improves speed and distance together —
which is why it is the only arm whose completed-trip count moves as much as
its speed. It carries 23% of the network's vehicle-distance at 47 km/h.

**`connector-south` (+7.8%)** does not touch the through movement at all.
It is the "right amount of concrete in the wrong place" arm — see above. It
is also the arm that changed most between the two measurements of this pod:
it used to post +19.5% while crossing four arterials as the priority leg,
and signalising those crossings — which is what building the road would
actually cost — more than halved it.

**`green-wave` (+17.0%, and it costs nothing)** is 67% of the winner's
effect for zero construction. Offsets are set from the actual inter-junction
distances at a 45 km/h progression speed. Eastbound (the tuned direction)
goes 23.8 → 29.5 km/h; westbound, which gets no deliberate progression at
all, still goes 24.8 → 29.7 km/h — with four junctions at 480/520/450 m and
an 86 s cycle the eastbound solution happens to serve westbound too. It has
the tightest effect of any arm (d = 6.87, the smallest seed-to-seed spread)
and it moves exactly the same vehicle-distance as the baseline: it is pure
delay saving.

**`retime-short` (−12.5%, the trap)** is the reflex "the lights are too
slow, speed them up", and it is the one that makes things worse. Shortening
the cycle from 86 s to 66 s leaves the 20 s of amber and all-red per cycle
untouched, so the lost-time fraction rises from 23% to 30% and Main
Street's green ratio falls from 0.42 to 0.36. The mechanism is visible in
the split: Main Street drops from 23.8/24.8 to 19.5/20.7 km/h while the
**cross streets improve**, 18.2 → 19.8 km/h. Shorter cycles moved capacity
off the corridor and onto the side streets. It is also the only arm that
fails the throughput guard: −6.7% vehicle-distance, −12.8% trips.

**The hypothesis this scenario was built to test — that the timing options
would beat the concrete ones — did not survive.** Building the bypass beats
coordinating the lights, +25.3% against +17.0%, on both the discovery and
the held-out seeds. What *is* true is that the free option gets two-thirds
of the way there, that one of the two roads is worth substantially less than
the other despite being 20% more road, and that the other timing option is
the worst thing on the menu. Cycle length is again the largest lever in both
directions, which is the same thing the Chicago work found.

## Fidelity

Every arm, one run each, seed 1000, 15,000 ticks, **on an otherwise idle
machine**:

| arm | demand delivered | uncontrolled coasting | collision observations |
|---|---:|---:|---:|
| base | 100% | 0.06% | 0 |
| add-lane | 100% | 0.07% | 0 |
| bypass-north | 100% | 0.08% | 1 |
| connector-south | 100% | 0.07% | 0 |
| retime-short | 100% | 0.06% | 0 |
| green-wave | 100% | 0.07% | 0 |

Every arm passes the 0.1% coasting bar and 100% demand delivery. **This is
load-dependent and that took some chasing**, so it is worth writing down:

*Uncontrolled coasting in this scenario is a fixed per-vehicle claim
latency at spawn, not the driver falling behind.* Across five calibration runs
spanning a 1.7× range of demand, the coast count is a near-exact constant
per spawned vehicle:

| spawned | coast-ticks | per vehicle |
|---:|---:|---:|
| 389 | 807 | 2.07 |
| 478 | 991 | 2.07 |
| 519 | 1068 | 2.06 |
| 579 | 1196 | 2.07 |
| 658 | 1331 | 2.02 |

and the engine's own unrelated `fix-signal-4way` fixture gives 550/267 =
2.06. Two ticks — 0.2 s — between injection and the driver's first intent,
on a network whose peak is ~250 simultaneous vehicles, three orders of
magnitude below anything that stresses the controller.

Under CPU contention that latency stretches, and the counter goes with it.
In one 96-run batch executed while another agent was saturating the same
16-core box, the median run showed 9.9 coast-ticks/vehicle (0.33%) and the
worst 27 (0.96%); in a later batch under heavier load the median was 56
(1.8%) and the worst 218 (7.8%). The transition is visible in wall-clock
order within a single experiment — 2.4–4.8% coasting for the first ~400 s of
the run, then 0.10–0.33% for the rest, as the competing load drained. The
same runs also manufacture collision observations: `bypass-north` shows 377
of them across a contended 10-run batch and **1** on the idle machine.

Consequences, stated rather than hidden:

* The **fidelity table above and the recorded/baked run are from serial
  runs on an idle machine** and are clean.
* The **A/B tables were run at `--jobs 3` while the machine was shared**, so
  individual runs in them exceeded the coasting bar. The comparison survives
  because `whatif.py` submits seed-major — every arm's run for a given seed
  lands in the same stretch of the schedule — and because the effects
  reproduce on held-out seeds run later under different load. If they were
  contamination artefacts they would not have.
* Anyone re-running this should give the box to it.

## Two defects found and fixed while building this

Recording these because both are traps for the next authored network.

**1. The leftmost-successor default silently ate 5% of the demand.**
`pickSuccessor` falls back to `Successors[0]` whenever the route table
cannot resolve — and it cannot resolve for any destination that is
unreachable from the vehicle's *current lane*, because lane changes are not
successors. network-format v1 documents successors as ordered left-to-right,
so at a junction whose leftmost movement is a turn, a vehicle that merely
needed to change lanes gets sent round the corner instead. In the first
build of `bypass-north` the bypass left Main Street to the left at BW, which
made it `Successors[0]`: **all 170 pooled W→N trips (5% of demand) were
swallowed by the bypass and none reached a north cross street**, and the
arm's headline effect measured +37.0% instead of the +25.3% it measures now.

The fix is in the generator: the movement that **continues on the same
road** is hoisted to index 0 and the rest keep left-to-right order. The
deviation costs the `HeldTurn` convention (an explicit +1 no longer names
"left" at those junctions); nothing here issues `HeldTurn`, and a wrong
default is a bug in every run where a wrong `HeldTurn` is a bug in no run.

**2. Two internal lanes merging into one exit lane with no `foesMerge`.**
At the T-junctions where a new road meets Main Street, both Main Street
lanes feed the single new-road lane, and the new road's arrival shares an
exit lane with a Main Street through movement. With empty foe sets nothing
serialized them and the kernel booked the overlaps as collisions (35 and 18
per run). Declaring `foesMerge` across every group of internals sharing an
exit lane — at the **unsignalized** junctions only, where there is no phase
plan to do the job — took it to zero.

## How to look at it

```
scripts/demos/bottleneck-town.sh bake              # record 9,000 ticks + bake + furniture
python3 scripts/serve-baked.py --baked data/town/baked/baked --viz viz/dist --port 8791
```

**URL** (the hash is the bake's content key and changes if the scenario
does):

```
http://127.0.0.1:8791/?bake=http://127.0.0.1:8791/baked/townbase/b6f967b72fe4/index.json&center=-98.4840,39.6963&zoom=14.2
```

Pass `?bake=` an **absolute** URL — a root-relative path fails with
`Failed to construct 'URL': Invalid base URL`, because the manifest URL is
the base for resolving every chunk.

* **Opening camera** — `center=-98.4840,39.6963&zoom=14.2` frames the whole
  corridor: Main Street's bend, all four signals, both ends. `zoom=16.4`
  over `-98.4890,39.6963` fills the frame with J1 and J2 and is the shot
  where the signal heads and stop bars read.
* **The town is at 39.70 N, 98.48 W** — open farmland in north-central
  Kansas, ~22 km north of the sibling freeway-merge demo. It is
  **fictitious**; the basemap under it is empty on purpose, so an invented
  road network is never confused with a real one. The frame descriptor is
  emitted by the generator (`cmd/bake` refuses a network without one).
* **Run `node viz/scripts/bake-furniture.mjs <bake-dir>` after every bake.**
  The pipeline does not do it, and without it no signal head renders.
  `scripts/demos/bottleneck-town.sh bake` does it for you.
* **Signal heads render correctly on this network** — 24 heads from 4
  programs, six per junction (through+right and protected-left for each
  Main Street approach, one per cross-street approach), each set back onto
  its own approach with a stop bar across its lanes, lenses live. Whatever
  is wrong with the Chicago signal-head bakes is not a placement bug in
  `signals.ts`: on a clean right-angle network the derivation is right.
* **Load time is a few seconds** — the network GeoJSON is ~500 kB, not the
  34 MB of the Chicago hero.

### The showable window

The bake is 9,000 ticks (15 sim minutes). The best 90 s of it, measured:

| tick window | network speed | mean queue, the two outer Main St approaches |
|---|---:|---:|
| 2,700–3,600 | 22.6 km/h | 226 m |
| **6,300–7,200** | **22.8 km/h** | **251 m** |
| 8,100–9,000 | 20.9 km/h | 237 m |
| 12,600–13,500 | 20.7 km/h | 363 m |

**Play ticks 6,300–7,200 at 1×.** That is 90 s of wall clock and a bit over
one full 86 s signal cycle, so every junction goes through a complete
red→green→red once on camera; the queues are fully developed (~250 m across
the two outer approaches, ~125 m each) but the corridor is not yet in the
late-run deterioration that makes it read as gridlock rather than as signal
queueing. Drag the replay slider to about 70% and press play. The last 900
ticks of the bake (8,100–9,000) are nearly as good and need no seeking.

At 4× the same 90 s covers ticks 6,300–9,900 and about four cycles, which
is the shot for "watch the platoon get stopped four times".

## What is wrong with this result

* **The engine's saturated discharge headway is 7.5 s, not 2 s.** Every
  volume in this document is about a third of what the same geometry would
  carry in reality. Ratios between arms are the deliverable; absolute
  veh/h are not.
* **The router has no congestion feedback.** A new road's usage is decided
  entirely by whether it shortens a path, so both new-road results are
  all-or-nothing assignments of whole O-D pairs. A real network would
  partially load a longer bypass and would stop loading a congested one.
  Both effects would *shrink* these numbers.
* **`connector-south` wins on the primary metric partly by lengthening
  trips.** Called out above; trips-completed is the honest read on it.
* **The baseline is not in steady state.** It loses ~12% of its speed across
  the measurement window. Paired comparison handles it; a quoted absolute
  baseline speed does not survive changing the horizon.
* **The 4-option menu has three real winners.** This corridor is congested
  enough that most interventions help, so the quiz question is "which helps
  *most*", not "which helps". That is a property of the calibration, not a
  finding about traffic.
* **Left turns exist but nothing else does.** No pedestrians, no transit, no
  parking, no driveways, no actuated control, no turn-on-red. It is a clean
  test bed, not a model of a town.
