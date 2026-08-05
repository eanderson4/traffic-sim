# ADR-0033: The foe's signal is part of its priority class

- **Status:** Accepted
- **Date:** 2026-07-28
- **Amends:** ADR-0031 (permissive green yields), ADR-0010 (junction right-of-way)
- **Scope:** kernel enforcement only. No NATS subject or payload changes, no
  network-file schema change.

## Context

ADR-0031 routed permissive-green approaches into `rowConflict` so they would
yield, which is what a permissive green means. It did not check what
`foeApproachBlocks` would then make of the foe on the other side.

That function decides whether a foe stopped at its own line has priority:

```go
foeYields := foe.Row == RowMinor || foe.Row == RowStop
if egoMinor {
    if !foeYields {
        return true // major/unmodeled foe has priority
    }
    if f.ID < v.ID { // mutual yield: lower vehicle ID first
        return true
    }
    continue
}
```

A signalised internal lane carries **`RowNone`** — the light is its control,
so the priority model records nothing. `RowNone` is not `RowMinor` or
`RowStop`, so `foeYields` is **false**, and the branch reads it as
"major/unmodeled foe has priority".

Now take two permissive greens that are each other's foes — the ordinary
shape of a permissive merge, and of any junction where two `g` movements
share an exit. Both are `egoMinor` (ADR-0031 makes a permissive approach
minor). Both classify the other as a priority foe. **Both yield. Neither ever
moves.** The vehicle-ID tie-break three lines below — the mechanism that
exists precisely to resolve a mutual yield — is unreachable, because it sits
behind a `foeYields` that a signalised foe can never satisfy.

`TestPermissiveGreensDoNotDeadlock` reproduces it in isolation: two vehicles,
one junction, a single green phase with no amber or red anywhere, 120
simulated seconds, neither vehicle ever leaves its approach.

### What it cost

This is the defect behind the non-clearing Chicago network — the thing three
ADRs have now been aimed at. The evidence:

- On the ADR-0031 engine, overlap observations went UP 3.2× (790,454 →
  2,518,364) and on-road vehicles rose at every matched tick (5,373 → 5,801
  at tick 28,000). ADR-0031 was making the network worse, not better.
- ADR-0032 then removed effectively all of the overlaps (2,518,364 → 1,031)
  and changed the drain **not at all**: speed still fell 9.8 → 3.2 km/h over
  minutes 60–90 while density *also* fell 3.2 → 2.8. Fixing the collisions
  proved the two problems were independent.
- Every one of the ten worst delay lanes in that run feeds a **signalised
  cluster junction** with merge foes. Not one is an unsignalised junction.

A permanent mutual yield produces exactly the reported signature: vehicles
that are stopped rather than slow, so the network empties of everyone who can
still move while the standoffs stay put — mean speed falls *because* density
falls, which is the composition effect that made "wedged, not queued" the
right reading and the wrong diagnosis.

## Decision

**A foe's current signal state is part of its priority class**, evaluated
where the foe is stopped at its line:

| foe's state | class | why |
|---|---|---|
| red | `foeHeld` — skip it entirely | its own light forbids it entering; yielding to a vehicle that cannot move is how a permissive movement stalls through the whole of every red the foe gets |
| permissive green (`g`) | `foeYieldClass` | it must yield too: a mutual yield, so the existing ID tie-break decides and exactly one goes |
| protected green (`G`) | `foePriority` | the light adjudicated in its favour; the permissive ego yields, which is ADR-0031's whole point |
| amber | `foePriority` | it may be committed and entitled to clear the box |
| no signal | `foePriority` | falls through to `foe.Row`, the pre-ADR-0033 behaviour exactly |

`foeApproachBlocks` becomes a method on `*Engine` (it needs the tick to read
a phase) and consults `foeSignalClass`. Unsignalised junctions are untouched:
with `foe.Signal == nil` the class is `foePriority` and the original `foe.Row`
logic decides, unchanged.

The red case is a second fix bundled here deliberately. It is the same defect
— the priority model not reading what the signal says about the foe — and
fixing only the mutual yield would leave permissive movements stalling
against red-held foes on every cycle, which is a throughput drag rather than
a deadlock but has the same cause and the same one-line remedy.

### Consequences

Good:

- Permissive greens still yield (ADR-0031 preserved, and its tests still
  pass), but a mutual yield now resolves instead of standing off forever.
- The ID tie-break becomes reachable for signalised foes, which is what makes
  the resolution deterministic and replay-safe — no RNG, no clock.

Costs, stated plainly:

- **This changes simulation output**, the third kernel change in one day.
  Re-measure; do not adjust.
- The ID tie-break is arbitrary as traffic engineering. Two drivers at a
  permissive merge negotiate by eye contact and hesitation; the kernel needs
  something total and deterministic, and lower-ID-first is what the priority
  model already uses for minor-vs-minor. It is a tie-break, not a claim about
  driver behaviour.
- `foeSignalClass` reads the phase per foe per candidate entry. Bounded by
  the foe count of one internal lane and only reached when a foe sits stopped
  at its line.

## Tests

`engine/permissivedeadlock_test.go` — two approaches merging into one exit
under a single-phase program, so each internal lane is the other's merge foe
and nothing is ever amber or red. A vehicle that never moves there was
stopped by the priority model and by nothing else.

- `TestPermissiveGreensDoNotDeadlock` — `"gg"`: both get through, and zero
  collision observations, so the tie broke by one going rather than by both.
  **Verified to fail without the fix**, with the failure message "DEADLOCK:
  neither permissive vehicle ever left its approach".
- `TestPermissiveStillYieldsToProtectedMergeFoe` — `"gG"`: the control. The
  permissive vehicle does not enter ahead of the protected one, and is not
  starved either. A fix written as "permissive foes never block" would pass
  the first test and fail this one.

ADR-0031's `TestPermissiveGreenYields` and `TestProtectedGreenDoesNotYield`
both still pass unchanged, which is the statement that this amends ADR-0031
rather than reversing it.

## Not done here

**Cross-junction gridlock cycles.** Logged here as a hypothetical before the
validation run; the run then measured it, so the numbers are recorded now
rather than left as a guess.

On the ADR-0033 90-minute baseline, **475 lanes end frozen at jam density**
— up to 212 veh/km/lane at exactly 0.00 km/h, with identical values every
interval from minute 55 to minute 90 — holding **3,597 of the 4,926 vehicles
still driving, on 30.04 lane-km (1.36% of the network)**. A successor-walk
over the frozen set finds a closed cycle:

```
n320489617_1_0_d2 (173 m) → n435372236_0_0_d2 (32 m) → n435372248_1_0_d2 (4.7 m)
→ n901108439_0_d2 (89 m) → n435372246_0_0_d2 (26 m) → n435372263_1_0_d2 (8.5 m)
→ n435372266_0_d2 (113 m) → n435372265_0_0_d2 (30 m) → back to the first
```

Every vehicle in the ring waits on one that is waiting on it. It cannot
self-resolve, which is why the network never clears no matter how long the
drain runs or how low the demand goes.

**This is also the resolution of the "wedged, not queued" reading** that
motivated ADR-0031 in the first place. Network mean speed fell while network
mean density *also* fell because 1.4% of the lanes held 73% of the remaining
vehicles: the free-flowing majority drained away and the frozen minority
stayed, so both averages moved down together. The signature was real; the
inference that the whole network was wedged was not.

> **Correction, measured 2026-07-28.** This section originally proposed a
> mechanism — `exitBlocked` accumulating exit room across short successor
> chains without reserving it, so several vehicles claim the same gap — and a
> fix, a per-tick room reservation. That was written from reading the code
> and **it is not the dominant mechanism.** In the final 5-minute interval of
> this run, 427 lanes hold vehicles and travel zero distance: **403 ordinary
> ROAD lanes against 24 junction internals.** 94% of the frozen mass is
> queued on roads, and a four-junction ring fixture reproduces the whole
> failure with no box ever blocked and every gate answering correctly.
>
> Gridlock here is a **circular wait on lane capacity**, not a junction-entry
> defect: each vehicle waits for room on the lane ahead, around a closed
> cycle. Nothing that reasons one junction at a time can see it, so nothing
> local can prevent it. See **ADR-0034**, which reproduces it, explains why
> prevention is the deadlock-avoidance problem rather than a bug fix, and
> gives the network a bounded, counted escape instead.
>
> The unreserved-room straddle IS real — it is those 24 internals, the worst
> a 1.7 m stub carrying 2.89 vehicle-lengths of occupancy — but at 6% of the
> frozen mass it is a smaller, separate item, logged under ADR-0034's "Not
> done here".

**The ID tie-break is not fair over time.** A low-ID vehicle repeatedly wins
against a stream of higher-ID foes. At a merge fed by a long queue this could
bias which branch discharges. Worth measuring once the network clears; there
is no point tuning fairness in a model that was deadlocking.
