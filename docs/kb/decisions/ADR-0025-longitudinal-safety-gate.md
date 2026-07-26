# ADR-0025: Longitudinal safety gate in the kernel

- Status: Accepted
- Date: 2026-07-26
- Amends: ADR-0008 §6 (uncontrolled-vehicle policy), ADR-0010 (right-of-way gate)

## Context

The kernel is authoritative over world state; controllers only emit intents.
Under `PolicyHoldLast` — the live configuration — the kernel does no driving
at all. `computeAccels` has four branches, and the last one is:

```go
default:
    v.Acc = 0   // holdlast, no applied intent: coast
```

A coasting vehicle has **no car-following term**. It holds its speed into
whatever is stopped ahead, overlaps, and — because nothing ever restores a
braking term — stays overlapped for the rest of the run. `rowGate` (ADR-0010)
caps every control path at junctions; there was no equivalent on the
longitudinal axis.

Two separate mechanisms feed that branch:

1. **Absent intent.** A controller that falls behind past the hold-last
   window (`(cadence−1) + HoldLastTicks`) leaves its vehicles with no intent
   at all. Measured on chi-loop-urban at freeway-scale 1.5: 20,283 of
   77,564,400 vehicle-ticks, **0.03%**. Real, but small.
2. **Stale intent, which is the larger one.** An applied intent is computed
   from an observation several ticks old and then *re-issued unchanged* by
   hold-last for up to `(cadence−1) + HoldLastTicks` more ticks. A vehicle
   can therefore hold a **positive** acceleration for several ticks after its
   leader has already stopped, and the kernel faithfully carries it out.

The consequences were not subtle. chi-loop-urban at freeway-scale 4.15
reported **20,222,389 collision observations** — roughly 1,120
interpenetrating pairs at every tick out of ~14,800 vehicles. Every metric
in the run read as severe congestion. Nothing distinguished it from a
network that was genuinely oversaturated, and the calibration built on top
of it was consequently wrong.

The same scenario run twice produced **32,199 and 12,401** collisions. The
dominant term in the collision count was controller *timing*, not traffic.

## Decision

Add `Params.SafetyDecel`, an emergency deceleration (m/s²) enforcing a
longitudinal guardrail that **caps every control path**, exactly as
`rowGate` does on the junction axis. No controller — in-kernel IDM, an
applied intent, or nothing at all — can drive a vehicle through the one in
front.

The bound is the mutual-braking criterion in its discrete-time (Krauss)
form:

```
v_max = −b·Δt + √( (b·Δt)² + v_lead² + 2·b·(gap − 0.25) )
a_gate = (v_max − v) / Δt,   clamped at −b
```

Three properties matter and each is pinned by a test:

- **It is a guardrail, not a car-following model.** At IDM equilibrium
  spacing (`s₀ + v·T`) at 25 m/s it permits **+86 m/s²** — nowhere near
  binding. A gate that shaped ordinary driving would quietly *become* the
  longitudinal model and mask what controllers actually asked for.
- **The −b·Δt term is load-bearing.** The continuous bound
  `√(v_lead² + 2b·gap)` is exactly the "can still stop" boundary, and the
  ballistic update advances by `½(v+v_max)·Δt`, which eats that margin every
  tick. Measured on the two-car fixture: the continuous form still landed
  the pair at **−2.48 m**. One tick of braking reserve closes it (+0.21 m).
- **It never invents braking the vehicle does not have.** Requests beyond
  `b` clamp at `b`. A collision can still be unavoidable, and when it is,
  the run says so rather than absorbing it.

### Default off in the kernel, on in `serve`

`DefaultParams().SafetyDecel = 0`. The gate changes accelerations, so
enabling it in the kernel default would silently rewrite every recorded CRC
in the repo. `cmd/serve` defaults `-safety-decel 6` (a normal car's ABS stop
is 6–9 m/s²), because absent and stale controllers are a property of *live*
runs specifically. Headless M1–M3 fixtures stay bit-identical.

This is a spec parameter, so it participates in the ADR-0012 content hash: a
run with the gate is a different scenario from a run without it, which is
correct.

## Consequences

**Collisions become a usable signal.** The count now means "the model
failed", not "a controller was late" — a precondition for any A/B comparison
that scores safety.

**Two new observability counters, in the ADR-0014 spirit of the director
tallies.** `SafetyGated` (vehicle-ticks the gate bound) and
`SafetyOverlapped` (vehicle-ticks it found an *already* negative gap — the
gate arriving too late). `serve` prints both. `SafetyOverlapped` is the
honest denominator for "are the remaining collisions real?".

**It masks controller misbehaviour by design.** A controller that drives
badly is now caught by the kernel instead of crashing. That is the intended
trade — the alternative was a world state with vehicles inside each other —
but it is why `SafetyGated` is *reported*, not silently applied: a run where
the gate binds constantly has a controller problem, and the number says so.

**It does not fix the underlying staleness.** A barrier on controller input
would (ADR-0005 §4 explicitly declines one: "never a barrier on controller
input; intents apply whenever they arrive and hold-last heals gaps"). The
gate bounds the *damage* from staleness rather than removing it, which
preserves the no-barrier property that keeps the engine from being held
hostage by a slow client.

## Alternatives rejected

- **Fall back to in-kernel IDM when hold-last expires.** Fixes case 1 only,
  and case 2 is the larger term — a *fresh but stale* intent never reaches
  the fallback branch at all.
- **Barrier the engine on controller input.** Contradicts ADR-0005 §4 and
  makes every run hostage to its slowest client.
- **Shorten the hold-last window.** Trades stale intents for absent ones;
  both branches produce the same overlap.
- **Treat it as a metrics artifact and stop counting these collisions.**
  The overlaps are real world state, not a measurement error. A separate
  measurement bug did exist alongside this one — `updateStats` compared each
  lane against `Successors[0]` regardless of where the vehicle was routed,
  inventing overlaps at every multi-way diverge, and on chi-loop-urban one
  3-successor tertiary lane accounted for 59% of a run's total. That was
  fixed independently; it is not this.
