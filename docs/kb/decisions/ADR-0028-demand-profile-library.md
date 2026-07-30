# ADR-0028: Demand profile library — per-location temporal shapes

- Status: Proposed
- Date: 2026-07-28
- Amends: nothing. Adds an authored input format for `scripts/chicago/mkod.py`.
- Does **NOT** touch: `contracts/asyncapi.yaml`, the scenario schema, or the
  engine. The `slices` field this feeds already accepts arbitrary per-flow
  shapes (ADR-0012); this decides only how they are *authored*.

## Context

Chicago demand had exactly one temporal shape for all 248 flows:

```python
AM_PROFILE = [0.45, 0.70, 1.00, 1.00, 0.80, 0.60]   # six half-hour slices
```

Every flow got that curve scaled by its own rate. That makes freight peak at
the same moment as commuting, sends residential departures downtown on the
inbound commuter curve, and gives every corridor an identical arrival shape.

Two defects surfaced while calibrating the expressways, both about time:

1. **The demand program had its own clock and nothing compared it to the
   run's.** `AM_PROFILE` spans 3 hours = 108,000 ticks; every run was
   6,000–12,000 ticks. A ramped run executed the first tenth of its own
   program, never left the 0.45 opening ramp, and reported that as the AM
   peak. The workaround (`--flat-peak`) emitted a hard-coded `0–1800 s`
   slice, so any run past 30 simulated minutes silently lost its arrival
   process partway through.

2. **The profile never returns to baseline.** It ends mid-peak at 0.60, so
   the run stops while the network is still full and recovery is never
   observed. Whether congestion *clears* is what separates a loaded network
   from an over-saturated one, and no cut ending mid-peak can answer it.

## Decision

A **named profile library plus assignment rules**, as JSON, passed with
`mkod.py --profiles`:

```json
{
  "version": 1,
  "step_s": 600,
  "profiles": {
    "commute-am":      [0.45, 0.70, 1.00, 1.00, 0.80, 0.60, 0.0, 0.0, 0.0],
    "freight":         [0.70, 0.80, 0.85, 0.90, 0.90, 0.85, 0.0, 0.0, 0.0],
    "reverse-commute": [0.30, 0.40, 0.55, 0.60, 0.55, 0.45, 0.0, 0.0, 0.0]
  },
  "default": "commute-am",
  "assign": [
    {"_why": "already downtown; counter-peak",
     "kind": "resident", "profile": "reverse-commute", "scale": 1.0},
    {"kind": "interior", "profile": "freight", "scale": 1.0}
  ]
}
```

- Values are fractions of each flow's own peak rate; slice *i* covers
  `[i*step_s, (i+1)*step_s)`.
- Rules match on `kind` (`portal` / `interior` / `resident`), road `class`,
  and `corridor`. **All keys in a rule must agree; the first matching rule
  wins;** `default` catches the rest.
- `scale` multiplies the flow's rate, composing multiplicatively with
  `--freeway-scale` and `--corridor-scale`.
- A trailing run of zeros **is** the drain: zero-rate slices are dropped
  rather than emitted, so the flow simply has nothing scheduled and the
  network clears.
- `_`-prefixed keys are comments (JSON has none), so a rule's rationale
  lives next to the rule.

`--profiles` is mutually exclusive with `--flat-peak` / `--slice-s` /
`--drain-s` / `--drain-level`. Two sources of truth for the shape is how a
run ends up executing a program nobody authored; the CLI flags remain for
the built-in profile, and the library expresses the same shapes directly (a
flat peak is a constant profile, a drain is trailing zeros).

Separately, `mkscenario.sh` now passes `--horizon-s` (ticks ÷ 10) and
`mkod.py` **refuses** a demand program the horizon cannot finish executing.

## Alternatives rejected

**A function of live state (`f(node, time, state)`).** Rejected on
architecture, not taste. It makes demand elastic — congestion feeding back
into arrivals — which is a legitimate model but a different one, and it
should be an explicit modelling decision rather than a consequence of a
callback signature. Three concrete costs: it breaks replay determinism
(ADR-0005); `mkod.py` runs *before* the simulation and has no state to
consult, so the logic would have to move into the engine's demand director,
making this an engine and message-contract change; and decisively, two arms
of an A/B would no longer share a demand program, ending the paired-seed
comparison protocol that makes those measurements meaningful.

**An N_locations × N_timesteps matrix (CSV).** Rejected because it already
exists and it is the *output*: `demand/main.yaml` is exactly that matrix,
248 flows each carrying its own per-slice rates. Authoring at that level adds
no abstraction over what mkod already emits, cannot survive regeneration
(mkod overwrites it), and produces diffs no reviewer can read. The library is
~30 lines for the same expressive power, and it is the level at which the
*intent* ("freight peaks earlier and flatter") is visible.

## Consequences

Good: trip purposes get distinct shapes; the drain becomes expressible;
shapes are diffable and reviewable; no engine, schema or contract change; the
library is reusable across networks.

Costs and risks:

- **More knobs that multiply.** `scale` × `--corridor-scale` ×
  `--freeway-scale` all hit the same rate, so a demand level is assembled from
  three places. The mitigation this section originally claimed —
  "mkod prints the realized totals" — **was not true when it was written**;
  see the correction below.
- **Fractions are napkin-anchored, not calibrated.** The shapes are the usual
  diurnal curves, not Chicago count data — the same posture as every other
  rate in this scenario, and it must not be quoted as calibrated.
- **Profiles of differing length** are allowed (freight may outlast
  commuting) but mean flows stop arriving at different moments; mkod prints a
  NOTE rather than refusing.

> **What the first implementation got wrong (2026-07-29, first real use)**
>
> This ADR's mitigation for the multiplying-knobs risk was that mkod "prints
> the realized totals". It did not. Every counter behind those prints was
> incremented from the rate *before* the profile rule's `scale` reached it, and
> the shape fraction never entered at all — so the reported demand level was
> the one **authored**, not the one written to the file. The two agree only
> while every rule has `scale: 1.0` and every profile peaks at exactly 1.00,
> which is why nothing surfaced until a library used a scale. Four distinct
> errors, all in the same direction:
>
> 1. **Profile `scale` was missing.** `realized["freeway"] += rate` ran
>    upstream of `emit_flow(..., rate * pscale, ...)`.
> 2. **The shape fraction was missing.** `emit_flow` writes `rate * f`.
>    `freight` peaks at 0.90 and `reverse-commute` at 0.60, so the base rate is
>    not a rate that appears anywhere in the file. Concretely: `--ramp-share`
>    reported 5,258 veh/h relocated onto interior points where the file asks
>    for 4,733 — an 11% overstatement, and the interior injections are the
>    corridor fill, so this was the number a freeway calibration leaned on.
> 3. **A sum of per-flow peaks is not a peak.** This ADR's whole purpose is
>    that flows crest at different moments, which makes that sum a rate no
>    instant in the run ever sees — and one that grows with the number of
>    distinct *shapes* rather than with demand. It is now a max over time, on
>    the elementary intervals between all slice boundaries so that non-uniform
>    spans need not share a grid, and the instant is named.
> 4. **A share was computed on rates.** The through-traffic mass balance
>    divided one peak rate by another. Two rates only compare if they are
>    integrated over the same span, and once flows run on different shapes they
>    are not; it is now a vehicle count on both sides. The same defect sat in
>    the destinations-by-district table, which weight-averaged flows by
>    pre-shape rate — caught in review as the same error class, and now also
>    on vehicle counts (`observe` takes the flow's whole-program count, the
>    integral of the slices emit_flow wrote), as is the stranded-portal
>    warning.
>
> Also fixed, in the same spirit: the boundary-freeway figure was printed
> before relocation had been re-emitted, so it showed a corridor at
> `1 - --ramp-share` of its own demand — a plausible-looking number rather than
> a visibly missing one. And the demand file's header recorded `--total`, a
> *target*, while recording neither the knobs that multiplied it nor the
> realized outcome; reproducing the shipped `chi-loop-urban-half` demand
> therefore meant guessing the invocation. The header now carries the realized
> peak and vehicle count, the knobs, and the ordered assign rules — ordered
> because first-match-wins makes the order part of the meaning.
>
> **A trap worth naming, which the validation does not catch.** Rules are
> first-match-wins, and a generic `{"kind": "interior"}` rule shadows a later
> `{"corridor": "X"}` one for exactly the interior mainline injections that
> fill corridor X. `check_all_rules_fired` cannot see it: the corridor rule
> still fires on that corridor's *portal* and *resident* flows, so it records
> hits and passes clean while the corridor keeps the generic shape. A
> corridor-targeting rule must be `{"kind": ..., "corridor": ...}` and must
> precede the generic rule for its kind.

Validation is deliberately loud, because every failure here is otherwise
silent — a misspelled corridor, an unknown profile name, or a rule that never
fires all yield a perfectly valid demand file that is not the one anybody
authored:

- unknown profile reference, unknown `default`, unknown rule key, bad
  `version`, non-positive `step_s`, negative fraction or scale, empty
  profile, rule with no match key → **fatal**;
- **a rule that matches no flow at all → fatal**, the lesson
  `--corridor-scale` learned: a rule that never fires is indistinguishable
  from a profile that does nothing.

## See also

- [ADR-0021](ADR-0021-od-demand-buildings.md) — the OD demand model
- [ADR-0012](ADR-0012-scenario-format.md) — the `slices` schema this emits
- [Silent Fidelity Failures](../articles/concepts/silent-fidelity-failures.md)
  — mechanisms 9 and 10 are the two defects above
