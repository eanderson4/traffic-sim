# ADR-0031: Permissive green yields

- **Status:** Accepted
- **Date:** 2026-07-28
- **Amends:** ADR-0011 (fixed-time signals), ADR-0010 (junction right-of-way)
- **Scope:** kernel enforcement only. No NATS subject or payload changes, no
  network-file schema change — the `g`/`G` distinction was already in the
  compiled files and was simply not read.

## Context

The SUMO `tlLogic` alphabet spells two different greens. `G` is **protected**:
the movement holds right of way, and the program guarantees its foes are red.
`g` is **permissive**: the movement may proceed *after yielding*, because the
program has deliberately left it green at the same time as the traffic it
crosses or merges into. The permissive left across oncoming traffic, and the
right turn merging into a green through-stream, are the everyday cases. A
permissive green is a signal choosing to **express** a conflict rather than
separate it in time, and delegating the resolution to the driver.

`mapSigChar` folded both into `SigGreen`:

```go
case 'g', 'G':
    return SigGreen
```

That is the right answer to the question `sigGate` asks — *may this vehicle
pass the stop line* — and both greens answer it identically. It is the wrong
answer to the question nobody was asking: *who gives way*.

The consequence lived in `rowGate`. Signalised internal lanes carry
`RowNone` (the light is their control), and the gate returned on that:

```go
if next.Signal != nil {
    if w, gated := e.sigGate(v, next, dist); gated { return w, true }
}
if next.Row == RowNone {
    return 0, false        // ← every green signalised approach left here
}
```

so `rowConflict` — the entire approaching-foe model — never ran for a
signalised approach on green. For protected green that is correct and
deliberate: the light adjudicated, and re-checking foes there would have
signals yield to each other and starve. For permissive green it meant the
yield that *defines* the movement never happened. **Every permissive movement
in every signalised network behaved as protected.**

`boxBlocked` still applied (green never means enter a box you cannot exit),
which is why this survived so long: it stops a vehicle entering against a foe
already *inside* the box. It cannot stop two vehicles entering on the same
tick, and the anti-simultaneous-entry mechanism is exactly the
`foeApproachBlocks` check inside `rowConflict` that was being skipped.

### What it cost

On the `chi-loop-urban` import:

| | |
|---|---:|
| signal links | 13,181 |
| permissive (`g` in at least one phase) | **2,008 (15.2%)** |
| ...of which have foes to yield to | **2,008 — all of them** |
| crossing foe relations never evaluated | 1,460 |
| merge foe relations never evaluated | 5,384 |
| links green in *every* phase | 132 (60 with foes) |

Junction `256591534` alone booked **525,733 of a 90-minute run's 790,454
overlap observations (66%)**. Its program is

```
phase 0  "GGgrrrGGg"  42 s
phase 1  "yyyrrrGyy"   3 s
phase 2  "rrrGGGGrr"  42 s
phase 3  "rrryyyGrr"   3 s
```

Link 2 is permissive, link 6 is green in all four phases, and both discharge
into `n203444889_0_0_d2`. The conflict recurred every cycle, for the whole
run, with nothing evaluating it.

This was not a cosmetic defect: 6,844 foe relations at 2,008 links were
evaluated by nothing at all.

> **Correction, measured the same day.** This ADR originally claimed the
> defect "is why the Chicago network never cleared during a 30-minute drain".
> **That claim is false and the 90-minute run refutes it.** On the fixed
> engine, same seed and scenario, the drain has the identical shape — network
> speed 10.0 → 3.1 km/h over minutes 60–90 while density *also* falls
> 3.1 → 2.7 veh/km/lane, still the wedged-not-queued signature — and total
> collision observations rose from 790,454 to **2,518,364**. Correct
> semantics, wrong causal story. The non-clearing network remains open
> (task #63); see ADR-0032 for the defect that actually converts a queue into
> a permanent wedge, and the note under Consequences below for why yielding
> plausibly made the count worse rather than better.

## Decision

**Read the char. A permissive green consults the priority model; a protected
green does not.**

1. `sigPermissive(l *Lane) bool` (`engine/signal.go`) reports whether the
   state char in force **at the current tick** is exactly `'g'`.
2. `rowGate` exempts a permissive approach from the `RowNone` early return,
   so it reaches `rowConflict` and is evaluated there as a **minor** approach
   (`RowNone != RowMajor`) — yielding to every crossing and merging foe. That
   is precisely the discipline permissive green denotes.

`SigState` deliberately gains no fourth member. Permissive and protected
remain one state for the stop-line question, so every existing `SigGreen`
comparison keeps its meaning; the new predicate asks a different question of
the same char. Adding `SigPermissive` would have forced an audit of every
switch on `SigState` to establish that nothing had silently changed.

### Consequences

Good:

- Permissive movements yield, which is what the signal programs have been
  saying all along. The 6,844 foe relations at those 2,008 links are now
  evaluated instead of ignored.
- No schema, contract, or import change: the data was always there.
- Protected green is untouched, so signalised throughput at properly phased
  junctions is unchanged — the control test asserts this directly.

Costs, stated plainly:

- **This changes simulation output.** Determinism and replay are intact
  (ADR-0005: the gate reads only slices in fixed order, no clock, no RNG),
  but any recording, keyframe, or measurement produced before this ADR
  reflects the old behaviour and is **not comparable** with anything after
  it. Every Chicago figure measured to date is affected; they must be
  re-measured, not adjusted.
- Permissive movements now discharge more slowly, because they now do the
  thing they were skipping. Junction capacity at those links will fall, and
  that is a correction, not a regression.
- **Measured: overlap observations went UP 3.2× (790,454 → 2,518,364).** The
  coherent reading is that lower permissive capacity means more standing
  queues, and a standing queue was exactly what the ADR-0032 lookahead defect
  turned into a permanent wedge — each wedge then booking 10 observations a
  second for the rest of the run. That is a hypothesis about the interaction,
  not a demonstrated chain: it predicts the count falls sharply once ADR-0032
  lands, and that prediction is the thing to check rather than assume. Note
  also that the top site MOVED (`j:cluster_261106906_…` at 1,342,508, 53% of
  the new total) while junction 256591534 fell 525,733 → 369,334 — down, but
  nowhere near gone, so it has a second cause this ADR does not address.
- `boxBlocked` is evaluated twice on a permissive approach — once in
  `sigGate`, once at the head of `rowConflict`. Accepted for clarity: it is
  cheap (a foe-slice scan), and the first call having returned false is
  precisely why the second is reached.

## Tests

`engine/permissivegreen_test.go`, over a two-approach crossing junction under
a single-phase program so that no vehicle is ever amber- or red-committed and
the `g`/`G` char is the only variable:

- `TestPermissiveGreenYields` — `"gG"`: the permissive vehicle sheds speed at
  the line, never shares the box, still gets through (no starvation), zero
  collision observations. **Verified to fail without the fix**: pre-fix, the
  permissive vehicle enters the box ahead of the protected one at tick 20.
- `TestProtectedGreenDoesNotYield` — `"GG"`: the control. Asserts the gate
  does *not* yield under protected green, pinning the half of the behaviour
  that must not change. A fix written as "signalised approaches always
  consult the priority model" would fail here.
- `TestSigPermissiveFollowsThePhase` — the predicate reads the current tick's
  char, not a cached lane property; a link is permissive in one phase and
  protected in the next. Also covers the out-of-range link index.

## Not done here

**All-major crossing pairs.** `rowConflict` skips crossing foes for a
`RowMajor` approach, on the stated assumption that a crossing foe of a major
approach is itself minor and does the yielding. 4,699 of the import's 7,321
junctions have every approach labelled `major`, which looks alarming — but the
exposure is small: of 693 crossing-foe relations involving a major internal
lane, only **38 (5.5%)** have a major foe on the other side. Most all-major
junctions compile no crossing foes at all. Logged, not fixed: at 38 relations
it is below the bar, and unlike permissive green it is not implicated in any
measured hotspot.
