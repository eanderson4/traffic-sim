# Silent Fidelity Failures

> How a run reports a clean measurement of a scenario it never simulated —
> eleven independent mechanisms, what each looked like from the outside, and
> the counter that catches it. Most were found on chi-loop-urban at city
> scale; two bite on a 44-lane authored pod; the seventh is not a property of
> any scenario at all (it is the measurement harness) and the eighth is not a
> failure of the simulation at all — it is a correct number answering a
> different question than the one being asked of it. Two are about TIME: a
> demand program longer than the run that executes it, and a mean speed read
> off a network that has not finished filling. The eleventh is about SPACE,
> and it is the one that hid the other ten's consequences the longest: a
> correctly computed average over a network that is not uniform.

## Why this article exists

Every one of these produced *plausible* output. None crashed, none raised an
error, none showed up in `denied_*`, and every one was mistaken for traffic.
The 30-minute Chicago cut reported 20.5 km/h and 243,768 collisions and read
as an over-saturated network; it was a demand director capped at one vehicle
per tick. When that was fixed, the same cut reported 20,222,389 collisions,
which read as *worse* over-saturation; it was a controller whose intents the
kernel carried out into the back of stopped traffic.

The pattern is the same every time: **a component that silently degrades
under load produces output shaped exactly like congestion**, because
congestion is what "things moving slowly and piling up" looks like from the
metrics. The defence is not better physics. It is a counter on every path
that can drop, expire, or skip work, and a loud threshold on each.

## The eleven mechanisms

### 1. Demand that never enters (the director's 1 verb/tick ceiling)

The demand director issued a **synchronous** `nc.Request` per spawn against a
contract layer that drains buffered wire requests once per tick
(`natsio/contract.go`). One blocking send at a time pinned the whole director
to 1 spawn/tick = 36,000 veh/h regardless of what the scenario declared.

Past that ceiling it failed *irreversibly*: the sampler's backlog grew until
its lag exceeded `DirectorSpawnHoldTicks` (600), after which every verb
arrived already expired. Injection stopped at tick 3,033 and never resumed —
not even as the network drained back to 1,368 vehicles.

**Why it was invisible:** expiry is not a denial. `denied_wait_s`,
`denied_pending` and `denied_served` all stayed clean while 84% of the
demand was discarded. A run that injects 16% of its scenario is
indistinguishable, in every other number, from a scenario that only asked
for 16%.

**Counters:** `DirExpired`, `DirInjected`, `DirDeadOnArrival`,
`DirExpiredByLane`, `DirLastInject`, `DirFirstExpire`. `DirDeadOnArrival` is
the one that separates the two diagnoses — a blocked origin means the
network could not absorb the demand, dead-on-arrival means the verb was
stale before it was ever tried.

### 2. Control that never arrives (holdlast coasting)

Under `PolicyHoldLast` the kernel does no driving. `computeAccels`' default
branch sets `Acc = 0` — a coast with **no car-following term** — for any
vehicle without an applied intent. Such a vehicle holds its speed into
whatever is stopped ahead and, since nothing restores a braking term, stays
overlapped for the rest of the run.

The load dependence is steep and it is why a light test proves nothing:

| active vehicles | vehicle-ticks with no intent |
|---|---|
| ~7,700 | 0.03% |
| ~12,000 | 35.75% |

**Why it was invisible:** the resulting overlaps are booked as collisions,
and a large collision count on a busy network reads as congestion.

**Counters:** `CoastVehTicks`, `CoastMovingVehTicks`, `CoastMaxPerTick`
against `VehTicks`. `serve` warns past 0.1%.

### 3. Control that is stale rather than absent

The larger term, and the one that misled the first investigation. An applied
intent is computed from an observation several ticks old and then *re-issued
unchanged* by hold-last for `(cadence−1) + HoldLastTicks` more ticks. A
vehicle can hold a **positive** acceleration for several ticks after its
leader has stopped.

This is **structural, not a performance problem**. Measured identically at
`-pace 0` and `-pace 4`: the round trip is tick-quantized, so no amount of
wall-clock headroom shortens it. Pacing cannot fix it and neither can a
faster driver.

The kernel-side answer is ADR-0025's longitudinal safety gate, which caps
every control path the way the right-of-way gate does at junctions. It took
the 4.15 cut from 20,222,389 collision observations to 138,900.

### 4. Measurement that invents the failure

`updateStats` measured the cross-boundary adjacent pair against
`lane.Successors[0]` regardless of which successor the leading vehicle was
actually routed to. At any multi-way diverge with a queue standing in it,
two vehicles on *different* branches were measured as overlapping. On
chi-loop-urban a single 3-successor tertiary lane accounted for **59%** of a
run's total collisions.

Worth separating from the rest: this one is not a fidelity failure at all,
it is a reporting bug. But it presented identically, and it inflated the
very number being used to diagnose the others.

### 5. A portal that delivers LESS the harder you push it

Found while authoring the fictitious merge scenario (`scripts/demos/merge-pod.py`,
2026-07-26), on a network with 44 lanes and ~550 vehicles — so this one is
not a scale effect and every city scenario has been carrying it.

`injectionPlan` (engine/spawn.go) enters a vehicle at the fastest speed from
which it could still brake behind whatever is on the lane. Under a Poisson
burst the entry gap is small, so the vehicle enters SLOW, and at
`Car.A = 0.73 m/s²` it needs ~600 m to get back to 29 m/s. While it is
slow it holds the injection point, so the next entry is slower still. Past a
threshold the portal never recovers between bursts and locks into a
low-speed platoon.

Measured on a single 250 m two-lane portal, Poisson arrivals, requested vs
delivered:

| requested (veh/h/lane) | delivered | portal speed |
|---:|---:|---:|
| 1000 | 1007 | 85 km/h |
| 1400 | 1373 | 70 km/h |
| 1800 | **993** | **34 km/h** |
| 2200 | **993** | **34 km/h** |

Note the shape: it is not a ceiling, it is a **capacity drop**. Asking for
1800 delivers less than asking for 1400. The failure is silent in the usual
way — the loss shows up only as `DirExpired`, and a scenario tuned by
turning the demand knob up until the network congests will sail past the
knee and measure a *different, lighter* scenario at a *slower* portal.

Two mitigations, both cheap: `spacing: constant` on the flow removes the
burst (the same portal then holds ~1500 veh/h at full delivery), and any
demand-tuning sweep must plot DELIVERED flow, never requested.

### 6. Exit routing that pins every vehicle in its lane

Same authoring session. The driver's exit routing (ADR-0019) draws each
vehicle a destination among the exit lanes reachable **through successors**
from its current lane (`driver/destinations.go` `reachableLanes` — successor
graph only, no lateral links). On a freeway every lane has exactly one
successor, so the only exit reachable from mainline lane 0 is downstream
lane 0. Every vehicle therefore draws the lane it is already in, its
`routeLatDist` is 0 everywhere, and `routeHopOK` (engine/mobil.go) then
vetoes every discretionary lane change as a move away from route.

Measured on a 12,000-tick ramp-off control: **1 lane change with exit
routing on, 67 with it off.** With the ramp on, the same scenario reports
45.7 km/h routed and 49.8 km/h unrouted — the routed arm's extra
"congestion" is nobody being able to overtake a truck.

This is the mirror image of mechanism 4 in the Chicago widening work, where
routing sent too FEW vehicles onto a new lane. Here it sends none anywhere.
The general statement: **`-exit-routing` is a city-network feature and
degenerates on any corridor whose lanes do not cross-connect.** Check the
`lanechanges` counter against a no-routing run before trusting a corridor
result.

### 7. The measurement harness manufacturing its own coasting

Mechanism 2 is load-dependent, and the load it responds to is **CPU
contention, not traffic**. A single driver replica computes every claimed
vehicle's intent in one goroutine per observation; when the box is
oversubscribed that goroutine misses tick deadlines and its vehicles coast —
on a network with only ~200 vehicles, where the driver is nowhere near a
traffic-related ceiling.

Measured on `bottleneck-town` (base arm, seed 2000, 15,000 ticks), N copies
of the SAME run at once on an otherwise idle 16-core box:

| concurrent runs | worst coasting | past the 0.1% warn |
|---|---:|---|
| 1 | 0.06% | no |
| 2 | 0.07% | no |
| 3 | 0.07% | no |
| 4 | 0.15% | **2 of 4** |
| 6 | 0.33% | **5 of 6** |

The knee is between 3 and 4 and it is sharp. The same pod at `--jobs 5`
while an unrelated job also ran voided **31 of 36** runs.

**The same trap is inside a single run: `-drivers` past the core count makes
fidelity WORSE.** Replicas shard the fleet safely, so more of them reads as
strictly more throughput — but they contend with each other, with the engine
loop and with embedded NATS for the same cores. Measured on chi-loop-urban
(2026-07-28) on a Ryzen 7 9700X, **8 physical cores**, 16 threads:

| `-drivers` | vehicles | coasting | while moving |
|---:|---:|---:|---:|
| 8 | 5,417 | 0.07% | 0.06% |
| 8 | 9,435 | 21.03% | 1.81% |
| 16 | 11,169 | 57.05% | 3.32% |

Doubling the replicas roughly tripled the coasting. Vehicle count rose too,
so the rows are not a clean control — but the direction settles it, because
the reason to raise `-drivers` at all is to survive a higher vehicle count
and it did the opposite. Read `-drivers` as bounded by PHYSICAL cores minus
the engine and broker, not by fleet size: 6 is the working figure on this
box. It is a property of the machine, so re-measure it on another one.

**Why this one is worse than it looks.** It is not a noise term that the
paired design cancels. Coasting scales with how many vehicles a run is
holding, so the arms that congest hardest lose the most intents — the
contention lands *hardest on exactly the effect under test* and biases every
comparison toward "this option does nothing". A quietly-thinner batch is
therefore not a weaker measurement, it is a differently-wrong one.

**Why it was invisible:** `scripts/whatif.py` read only the subprocess exit
code, and `serve` exits 0 on runs it has itself declared void. The warnings
were printed into per-run logs in a temp directory that was deleted on
success.

**Counter:** `whatif.py` now scans every run's log for the fidelity warnings
(`fidelity_problems`) and refuses to write a report at all rather than write
one with runs silently dropped, keeping the logs for diagnosis. Batches must
run strictly sequentially and at a calibrated concurrency; the calibration
is a property of the machine, not of the scenario, so it is re-measured
rather than assumed.

### 8. A boundary queue reported as corridor congestion

Found on chi-loop-urban at freeway-scale 3.5 (2026-07-27), while checking a
congestion map against the corridor table that had justified shipping that
scale. The map showed green expressways and an orange downtown; the table said the
expressways ran at 31.7-46.4 km/h. Both were right, and the table was
measuring the edge of the extract.

An extract is a cordon. Demand enters on ORIGIN lanes — lanes with no
predecessor — and those lanes have finite injection capacity. Past it the
queue forms ON the origin lane, which is inside the network and inside the
metric window, so its vehicle-time is booked against whatever corridor the
lane belongs to.

Measured on the `chishow35` bake — `data/scenarios/chi-show-fw35`, seed 42,
6,000 ticks, `-drivers 8 -capacity 48000` — aggregating its own
`-metrics-out` from tick 3,000. `data/` is gitignored, so the 42 MB metrics
file is not in the repo; regenerate it with

```
scripts/chicago/record-hero.sh data/scenarios/chi-show-fw35 chishow35 6000 \
    data/baked -drivers 8 -capacity 48000
python3 scripts/show/mkcongestionmap.py \
    --network data/networks/chi-loop-urban/chi-loop-urban.json \
    --metrics data/baked/chishow35.metrics.json --warmup-tick 3000 \
    --run-label "chishow35 (chi-show-fw35, seed 42, 6000 ticks, -drivers 8)" \
    --out docs/show/img/chi-congestion-fw35.png
```

Other fw35 recordings exist (an earlier sweep run reads Kennedy 47.6 /
51.4 / 8.3% against this run's 46.4 / 50.0 / 8.2%); the conclusion is
insensitive to which, but a table is not, so every figure below is from the
run named above:

| corridor | Edie speed | excluding origin lanes | origin share of time |
|---|---:|---:|---:|
| Dan Ryan | 36.4 km/h | **58.9** | 49.5% |
| Eisenhower | 41.0 | **72.0** | 62.3% |
| Stevenson | 41.3 | **57.6** | 38.8% |
| Lake Shore Drive | 31.7 | **48.8** | 39.9% |
| Kennedy | 46.4 | **50.0** | 8.2% |

Network-wide, origin lanes held **22.6% of all vehicle-time on 10.2% of the
distance**. One 491 m four-lane LSD portal ran at 5-10% of its posted limit
and absorbed a quarter of that corridor's entire vehicle-time.

**Why it was invisible:** every gate passed. Demand delivery was 97%, well
above the 95% bar, because the vehicles DO enter — they enter slowly and sit
on the injection lane. Coasting was 0.07%. No observation frame was lost.
Nothing that the gates watch was dropped, so nothing was counted. (The run
does book 21,070 dropped crossings, which is the separate boundary-overlap
accounting of ADR-0025's known gap, not demand, control or observation
loss.)

**Why it is not mechanism 5.** That one is a *burst* artifact and its
documented mitigation is `spacing: constant`. Tested here: switching all 248
flows from Poisson to constant moved the origin share ACROSS THE FIVE
EXPRESSWAY CORRIDORS from 40.9% to 39.9% (network-wide it is 22.6%; the
per-corridor figures are in the table above) and left every corridor speed
within noise. This is saturation, not
burstiness — the portals cannot pass the demand at any arrival pattern.

**Counter (2026-07-28): `mkod.py --ramp-share`.** The doorway is narrow
because a map cut deletes the on-ramps. The real Kennedy fills from Armitage,
North, Division and Ohio; the extract gave it four flows of 944 veh/h on
adjacent lanes at one coordinate. `--ramp-share F` relocates fraction F of
each corridor's boundary inflow onto interior points spread along that
corridor (`--ramps-per-corridor`, default 12), conserving volume — sites are
resolved before any volume moves, so a corridor with no usable site simply
never gets drained.

Two things to know before using it:

- **Site selection must be spatial, not by index.** A corridor carries
  several parallel lanes at the same longitudinal position, so taking every
  n-th lane from a coordinate-sorted list puts multiple picks metres apart:
  measured on the Dan Ryan, two injection points **10 m apart** with a median
  gap of 130 m across a 5.9 km corridor — the single-point defect, merely
  subdivided. Greedy farthest-point selection gives 0.42-0.94 km minimum gaps
  across all five corridors, which is realistic ramp spacing.
- **It invalidates the freeway-scale calibration, and by a lot.** The old
  scale was tuned while the doorway was METERING the corridor — at fw3.5 only
  62% of directives were delivered, so a third of the demand never reached
  the mainline. Remove the meter and the same scale gridlocks the network
  (every expressway 4.3-8.0 km/h). A scale chosen against a saturated portal
  is a measurement of the portal.

The structural check still applies and is still the thing to report:
partition a corridor's lanes by whether they have a predecessor and give both
figures. A corridor speed quoted without that split cannot distinguish a
congested road from a congested doorway.
`scripts/show/mkcongestionmap.py` makes it visible by colouring each lane
against its own limit, which is how this was caught.

**A note on the diagnostic, since it bit twice.** "Share of delay within 1 km
of an injection point" is a good metric under single-point injection and a
meaningless one under `--ramp-share`, where a point every ~500 m puts every
lane within 1 km of one and pins the number at 100%. Its replacement — share
of delay in the worst 10% of lane-km — turned out to read 84-99% in EVERY
regime including an uncongested arterial grid, because this network's
lane-length distribution is dominated by short fragments that carry no delay.
Neither number discriminates. Use speed against the AM band, density against
the ~25 veh/km/lane critical, delivery, and moving-coasting.

**Consequence for the show, stated carefully — the boundary queue OVERSTATES
the congestion, it does not invent it.** Comparing the same network at
freeway-scale 2.0 (where origin lanes hold just 2.3% of network vehicle-time,
so the doorway is not binding) against 3.5, interior-only:

| corridor | fw20 interior | fw35 interior |
|---|---:|---:|
| Kennedy | 76.1 km/h | **50.0** |
| Stevenson | 80.1 | **57.6** |
| Dan Ryan | 75.7 | **58.9** |
| Eisenhower | 80.4 | 72.0 |
| Lake Shore Drive | 52.1 | 48.8 |

So the mainlines DO congest with demand — the Kennedy loses a third of its
speed — and 50 km/h is far closer to the real 25-45 AM peak than fw20's 76.
What the cordon queue did was inflate that into a headline of 31.7-46.4 km/h
(the Kennedy's 50.0 interior reads as 46.4 with the doorway folded in; the
Dan Ryan's 58.9 reads as 36.4). Both readings mislead if quoted alone:
"36 km/h" credits the road with the doorway's delay, and "not congested"
ignores a 34% drop. Quote the
interior-only figure and say it is interior-only.

### 9. A demand program the horizon never finishes executing

`mkod.py` emits a piecewise profile: `AM_PROFILE = [0.45, 0.70, 1.00, 1.00,
0.80, 0.60]` over half-hour slices, a 3-hour 06:00–09:00 peak = **10,800 s =
108,000 ticks**. Every Chicago run to date was 6,000–12,000 ticks, i.e.
1,200 s. A ramped run therefore executed the first 1,200 s of a 10,800 s
program — it never left the **0.45 opening ramp**, and reported that as the
AM peak.

Nothing in any output said so. The demand file was valid, every slice was
correct, delivery was ~100%, and the run was clean. The scenario simply never
reached the demand it described. The workaround in use (`--flat-peak`) hid
the mismatch rather than exposing it, and carried its own version of the same
bug: it emitted one hard-coded `0–1800 s` slice, so any run past 30 simulated
minutes silently lost its arrival process partway through and drained without
being asked to.

**Counter:** `mkod.py --horizon-s` (passed automatically by
`mkscenario.sh` from `ticks / 10`) refuses a program the run is too short to
execute, and names both fixes — raise the ticks, or lower `--slice-s` to fit
the shape into the horizon you have. Flat peak now spans the horizon.

The general form: **a demand program has a duration, and it is not the run's
duration.** Two independent time axes, no cross-check between them, and the
failure mode is a scenario that is entirely valid and entirely not the one
you meant to run.

### 10. Measuring a network that is still filling

Related but distinct: even with demand correctly executing, a mean speed
taken over a window is only a property of the network if the network has
settled. It usually has not. On a 12,000-tick freeway-scale-3.5 run the
per-interval curve read:

| interval | freeway km/h | freeway veh/km/ln | % of critical |
|---|---:|---:|---:|
| 10–15 min | 39.5 | 5.0 | 20% |
| 15–20 min | 27.6 | 6.2 | 25% |

Speed falling 30% and density still climbing steeply at the horizon: this is
a fill curve, not an equilibrium. The window average over those two intervals
(~33 km/h) sits inside the real AM band and looks like a calibrated result.
It is an artefact of where the run was cut. Worse, it makes the demand knob
and the horizon **confounded** — a scale sweep run this way partly measures
how fast each scale fills, so raising demand and lengthening the run are
indistinguishable in the output.

**Counter:** report the per-interval curve, not just the window mean
(`diagnose.py --curve`), and run a full peak *cycle* — ramp, plateau, taper,
then a drain with arrivals off (`--drain-s`). A network that clears during
the drain was genuinely loaded; one that does not was over-saturated, and the
distinction is invisible from a flat cut that stops mid-peak.

### 11. A mean over a network that is not uniform

**What it looks like:** two figures, both computed correctly, both quoted as
fidelity evidence, both wrong about the thing they were used to claim.

*"Network density is 27% of critical, so we are not congested yet."* True as
arithmetic. It is a mean over 2,203 lane-km that includes every empty
residential street in the box. The same run had **55 lane-km at or above
critical density moving at 6.3 km/h** — real jams, in real places. The mean
had no way to say so.

*"The corridor means are 25–41 km/h, inside the Chicago AM band, so the
expressways are loaded like the real ones."* Also true as arithmetic. The
distribution over the same cells:

| corridor lane-km-hours | share |
|---|---:|
| empty | 6.2% |
| 60+ km/h | 68.0% |
| 45–60 km/h | 14.4% |
| 30–45 km/h | 3.4% |
| 20–30 km/h | 2.2% |
| 10–20 km/h | 2.4% |
| <10 km/h | 3.2% |

The corridor mean was a blend of a few destroyed segments and mostly
free-flowing road. The real Kennedy at 8am is not 5% jammed and 95% empty.
The mean lands in the right band for the wrong reason, and it lands there
*more* convincingly the more heterogeneous the network is.

This one is worse than the other ten because it is not a bug. Nothing
malfunctioned; the number is what it says it is. It survived because the
alternative was never printed.

**Counter:** report **distributions, not means** — share of lane-km (and,
separately, share of VMT) in each density and speed band, over space-time
cells (`scripts/runreport.py`, [ADR-0030](../../decisions/ADR-0030-run-report-protocol.md)).
Keep empty road in its own bucket rather than averaging it in as a zero.
Show the lane-km share and the VMT share together: when they disagree, the
disagreement is the finding. And locate the delay — by corridor where
corridors exist, by district everywhere else, because "the arterial grid" as
one 1,779 lane-km bucket is not a location either.


## The general shape

A component that degrades gracefully under load will produce numbers that
look like the phenomenon it is meant to be simulating. Three consequences:

**Delivery is a precondition, not a result.** Before reading any metric from
a run, check what fraction of the declared demand entered the network and
what fraction of vehicle-ticks had control. `serve` prints both and calls
the run void below threshold, because a 74%-delivered run is not a
measurement of the scenario with error bars — it is a measurement of a
different scenario.

**Load-dependent failures need load-matched tests.** The safety gate looked
useless when tested at 7,700 vehicles (68 engagements in 77.6M vehicle-ticks)
and essential at 12,000 (146× fewer collisions). A test below the knee
proves nothing about behaviour above it.

**Run-to-run variance is the null hypothesis.** Live runs are not
deterministic — intents arrive asynchronously — and the same scenario
produced 32,199, 24,899, 17,453 and 12,401 collisions on four consecutive
runs. Any conclusion from a single run per arm is unsupported; see
`scripts/whatif.py` for the paired-seed protocol.

## Known remaining gap

Boundary crossing is a **placement**: `boundaries()` assigns `v.Lane` and
`v.S` outright, after `computeAccels`. No acceleration guardrail can prevent
a vehicle landing on top of one already on the successor lane, and 822 such
landings were counted on one freeway-scale-1.5 run, every top site a
junction-internal lane, each overlap persisting ~16 ticks. `injectionPlan`
consults `junctionHold` before an injection; the crossing path has no
equivalent occupancy check. Counted by `CrossOverlaps` /
`CrossOverlapsBySection`; recorded in ADR-0025 as a gap rather than fixed.

## See also

- [ADR-0025](../decisions/ADR-0025-longitudinal-safety-gate.md) — the safety gate
- [ADR-0008](../decisions/ADR-0008-controller-contract.md) — hold-last semantics
- [ADR-0014](../decisions/ADR-0014-observability-metrics.md) — the metric kernel
- [Congestion Metrics](../business-domains/congestion-metrics.md) — the multi-seed experiment protocol
