# Silent Fidelity Failures

> How a city-scale run reports a clean measurement of a scenario it never
> simulated — four independent mechanisms found on chi-loop-urban, what each
> looked like from the outside, and the counter that catches it.

## Why this article exists

Every one of these produced *plausible* output. None crashed, none raised an
error, none showed up in `denied_*`, and all four were mistaken for traffic.
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

## The four mechanisms

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
