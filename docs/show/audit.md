# Audit — what these results can and cannot carry

Written after the option sets were built, by re-deriving every headline
number from the saved reports in [`reports/`](reports/) rather than from the
notes taken while building them. Three claims did not survive. They are
corrected here and in the documents themselves.

Everything below is reproducible from the checked-in reports without running
a simulation:

```
python3 scripts/whatif.py --report docs/show/reports/chi-loop-urban.json --metric speed_kmh
python3 scripts/nulltest.py docs/show/reports/chi-kennedy.json docs/show/reports/chi-kennedy-truckban.json
```

## 1. The hero scenario has two winners, not one

Re-scoring the saved report on the primary metric, with the throughput guard
applied to every option rather than only to the shortlist:

| option | speed | p | VMT | p | guard |
|---|---:|---:|---:|---:|---|
| **truck-ban** | **+2.28%** | **0.0002** | **+2.22%** | 0.0001 | ok |
| **retime-short** | +2.09% | 0.0021 | +1.51% | 0.0055 | ok |
| transit-5 | +2.09% | 0.0000 | −3.09% | 0.0000 | fails |
| ramp-meter | +1.32% | 0.0009 | −0.80% | 0.0441 | fails |
| lsd-widen | +0.51% | 0.0321 | +0.45% | 0.0586 | ok |
| kennedy-widen | +0.19% | 0.3862 | +0.15% | 0.4497 | ok |
| retime-long | −3.82% | 0.0000 | −2.72% | 0.0001 | fails |

The peak-hour freight ban is a *better* result than the shortlisted winner on
every axis — larger effect, smaller p, more traffic carried. It is not on the
Loop menu, and `curate.py` is built that way on purpose: it takes the best
significant option and then fills the remaining slots with options that are
**not** significant wins, because the game needs exactly one answer.

That is a legitimate thing to do — it is what the shortlist is for — but it
means **"there is exactly one winner" is a property of the menu, not a
finding about Chicago.** The claim the measurements support is narrower: *of
the four options on this menu, exactly one is a real upgrade.* The
per-scenario documents list all seven tested options underneath the menu, so
the curation is auditable rather than hidden.

The freight ban is held out of the Loop menu for a second, independent
reason: it is the answer to the Kennedy scenario, and an answer that repeats
across scenarios stops being a question.

## 2. The noise floor is ~0.3%, which retires one claim

The Kennedy freight ban had to be re-measured in its own pod after a vtype
bug, which left two runs of a **byte-identical scenario on the same eight
seeds** — a paired experiment in which the treatment is nothing at all. Both
are checked in, so the test re-runs from the reports:

| | mean Δ | p | per-seed sd |
|---|---:|---:|---:|
| speed | −0.137% | 0.2720 | 0.324% |
| VMT | −0.127% | **0.0552** | — |

Runs are not a pure function of their seed. The driver is a separate service
over NATS, so under CPU contention it falls behind differently and the
numbers move. Two consequences:

- **`lsd-widen`'s "+0.5%, p=0.032" is inside the noise floor.** It was
  already demoted below the 1% practical floor, so no conclusion changes, but
  it should not be described as statistically real. It is now labelled a
  no-op.
- **The throughput guard has demonstrated false-positive risk at exactly the
  magnitude it rejected `ramp-meter` on** (VMT −0.80%, p=0.044). That
  rejection is not trustworthy. `ramp-meter` is a genuine small upgrade whose
  throughput cost is unproven.

The fix applied to the harness is to submit runs **seed-major** rather than
variant-major, so every arm's run for a given seed lands in the same stretch
of a multi-hour schedule. Variant-major ordering turned load drift into bias
by making it correlate with arm identity.

## 3. No multiple-comparison correction was applied

Seven options per scenario, α = 0.05 each. Bonferroni-corrected that is
0.0071:

| scenario | winner | p | survives correction |
|---|---|---:|---|
| the Loop | retime-short | 0.0021 | yes |
| Kennedy | truck-ban | <0.0001 | yes |
| Loop CBD | transit-5 | 0.0122 | **no** |

The CBD is also the scenario where seed count was extended after seeing a
marginal result (10 → 18 seeds), which inflates the false-positive rate by an
amount that cannot be recovered after the fact. Its point estimate moves
1.6% less vehicle-distance, passing the guard only by not being significant —
a weaker pass than the other two winners, which both carry more traffic than
their baselines.

The CBD grid is intrinsically noisy: baseline network speed varies 6.9–8.6
km/h across seeds, about ±10%, against 0.45% on the Loop. Detecting a 2%
effect there needs many more seeds than the other two scenarios.

## 4. Corrected numbers

| claim | as first reported | correct |
|---|---|---|
| hero network mean speed | 28.8 km/h | **27.68 km/h** |
| README corridor speeds | LSD 37, mainlines 61–77 | those are **pod-run** figures; the recorded hero cut is LSD **35.3**, mainlines **59–76** |

Hero corridor speeds, from the recorded 18,000-tick cut: Lake Shore Drive
35.3, Jane Byrne 35.8, Kennedy 59.0, Stevenson 60.5, Dan Ryan 66.6,
Eisenhower 76.2, network 27.7 km/h.

## 5. Runs are not bit-reproducible

Three serial runs of one scenario at one seed differ in 367 scalars —
~2×10⁻⁹ relative on totals, ~10⁻¹³ on per-trip distance. Far too small to
touch any conclusion here, and the recorded cut is CRC-verified on its own
terms. But this repo treats determinism as an invariant, and **it has not
been isolated whether the drift is in kernel state or in float accumulation
order on the metrics path.** That deserves its own investigation
independent of the show; it is not a show blocker.

## What held up

- Hero fidelity, from the run log: 0.03% uncontrolled coasting (24,264 of
  94.5M vehicle-ticks), 99% of accepted directives delivered.
- The measurement window: the metrics part sets `begin_s: 400`, the first
  interval begins at tick 4000, so the harness's `--warmup 4000` is exactly
  aligned. The primary metric never sees the fill-up transient.
- The metric argument in [README](README.md) — that two of three defensible
  metrics are artifacts — re-derives exactly from the saved reports.
- The `--freeway-scale 2.0` calibration limit and the widening-model
  limitation were disclosed before this audit and are unchanged by it.
