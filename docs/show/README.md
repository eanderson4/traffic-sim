# Chicago show — three scenarios, measured

Everything here comes from paired-seed A/B runs of the same scenario with
one thing changed. Each option below was simulated against the identical
baseline on the identical seeds, so the seed's own randomness cancels; the
p-values are paired t-tests over the per-seed differences and `d` is Cohen's
d. Reproduce any table with:

```
python3 scripts/whatif.py --pod <pod> --baseline base --seeds N \
    --ticks 12000 --warmup 4000 --metric speed_kmh --out report.json
python3 scripts/chicago/curate.py --report report.json --labels <labels.json>
```

The reports these tables were built from are checked in under
[`reports/`](reports/), so every number here can be re-derived — including
on a different primary metric — without running a simulation:

```
python3 scripts/whatif.py --report docs/show/reports/chi-loop-urban.json \
    --metric mean_trip_s        # watch the winner change
```

**Read [audit.md](audit.md) before quoting any of this.** It re-derives the
headline numbers from those reports and corrects three claims, including
what "one winner" does and does not mean.

## The scenarios

| # | scenario | network | what it is about |
|---|---|---|---|
| 1 | [Chicago — the Loop and the expressways](chi-loop-options.md) | `chi-loop-urban`, 55,555 lanes, 2,204 lane-km | the hero cut: downtown grid plus the Kennedy, Dan Ryan, Eisenhower, Stevenson, Lake Shore Drive and the Jane Byrne |
| 2 | [Chicago Loop CBD](cbd-options.md) | `chi-loop-cbd`, 11,851 lanes, 208 lane-km | the downtown grid alone — 1,057 signals in 208 lane-km, a signal every ~200 m |
| 3 | [Kennedy corridor](kennedy-options.md) | `chi-kennedy`, 15,315 lanes, 961 lane-km | one expressway and its ramp terminals |

## The answers

Spoilers. Each menu is one real winner, one option that actively makes
things worse, and two that do nothing — and none of them is guessable from
the label, which is the point.

| scenario | the winner | the trap | the no-ops |
|---|---|---|---|
| the Loop and the expressways | **Shorter signal cycles** +2.1% (p=0.0021, VMT **+1.5%**) | Longer greens −3.8% | Widen LSD, widen the Kennedy (both n.s. or inside the noise floor) |
| Loop CBD | **Free CTA transfers** +3.4% (p=0.012, 18 seeds, VMT −1.6% n.s.) | Slow Streets −9.9% | Freight ban, longer greens (both n.s.) |
| Kennedy corridor | **Peak-hour freight ban** +3.9% (p<0.0001, VMT **+1.8%**) | Speed harmonisation −3.1% | Widen the mainline, widen the on-ramps (both n.s.) |

Each winner carries at least as much traffic as its baseline, so none of
them is winning by moving less. Two carry measurably more; the CBD winner's
throughput is statistically flat with a −1.6% point estimate.

**"Exactly one winner" is a property of the menu, not of Chicago.** The
shortlist is built to contain one answer, so where the measurements found
more than one real upgrade, the extras were held back — on the Loop, a
peak-hour freight ban is a *stronger* result than the shortlisted winner
(+2.3%, p=0.0002, and it carries 2.2% more traffic). It is held out because
it is the answer to the Kennedy scenario, and an answer that repeats across
scenarios stops being a question. What the measurements support is the
narrower claim: *of the four options on this menu, exactly one is a real
upgrade.* Every tested option is listed under each menu, and
[audit.md](audit.md) works through this and two other corrections.

Three things worth saying out loud when the reveal comes:

- **Widening never wins.** Four widening options across three scenarios;
  every one lands between −0.3% and +0.7% and none is practically
  significant. That said, read the model caveat below before making the
  strong version of that claim — for widening specifically, this simulation
  cannot fully back it.
- **Nothing was built in any of the three winners.** They are a signal
  retiming, a 5% mode shift, and a freight ban. Every option that laid
  concrete came back flat.
- **The same signal change helps and hurts depending on direction.** Shorter
  cycles are the Loop's winner at +2.1%; longer cycles are its trap at
  −3.8%. Cycle length is the single largest lever found anywhere in this
  work, in both directions.
- **The freight ban wins on the Kennedy and does nothing in the CBD**
  (+3.9% against +1.9%, n.s.). Trucks cost you where vehicles are fast and
  merging; in a grid where nothing exceeds 13 km/h, a truck is just another
  vehicle waiting at a light.

## How to read a result

**The primary metric is network mean speed** (Edie's definition over every
vehicle-second in the measurement window). That choice is not cosmetic — on
the CBD pod, three defensible metrics named three different winners from the
same ten paired seeds:

| metric | winner it names | why it is wrong |
|---|---|---|
| `mean_time_loss_s` | Slow Streets (−11.0%) | measured against each lane's free-flow reference; lowering the speed limit lowers the reference, and this option's real trip times are 5.5% **worse** |
| `mean_trip_s` | shorter cycles (−8.2%) | averages over **completed** trips only; this option completes 7.8% fewer of them, finishing the easy trips and stranding the rest |
| `speed_kmh` | congestion charge (+11.7%) | correct as far as it goes, but the charge moves 10% less traffic — see the VMT guard |

**Every result is guarded by VMT** (total vehicle-distance in the window).
An option that raises average speed while moving less traffic has not made
the network better at its job. The guard is itself a paired test over the
per-seed records rather than a hand-picked percentage — whether an option
moves less traffic is a question the data answers. Options that do not
significantly reduce vehicle-distance are ranked ahead of those that do,
and the rest are annotated. On the Loop this is what separates the two
+2.1% results: shorter signal cycles carry 1.5% MORE traffic, while a 5%
mode shift carries 3.1% less.

**Effect estimates shrink with more seeds — expect it.** The CBD winner
measured +5.0% (p=0.042) on ten seeds and +3.4% (p=0.012) on eighteen. The
effect is real and got *more* certain; the point estimate regressed, as a
first significant result usually does. Present the deeper number.

**Statistical significance is not practical significance.** Six paired seeds
of a low-variance simulation put the seed-to-seed spread on chi-loop-urban
network speed at ~0.45%, so a 0.5% effect clears p<0.05 comfortably while
being invisible to anyone driving in it. Options must clear a 1% practical
floor to be offered as the answer; smaller ones are labelled as such.

**There is a floor below which no p-value means anything.** A run is not a
pure function of its seed — the driver is a separate service, so under CPU
contention it falls behind differently. Two runs of a byte-identical
scenario on the same eight seeds differ with a per-seed sd of **0.32%** on
network speed, and the throughput guard came back at p=0.055 against
*nothing at all*. Effects below ~0.3% are not separable from which machine
the run landed on. Reproduce it on the checked-in reports:

```
python3 scripts/nulltest.py docs/show/reports/chi-kennedy.json \
    docs/show/reports/chi-kennedy-truckban.json
```

**No multiple-comparison correction is applied.** Seven options per
scenario at α=0.05 is a Bonferroni threshold of 0.0071. The Loop and
Kennedy winners clear it; the CBD winner (p=0.0122) does not. See
[audit.md](audit.md).

## What the simulation can and cannot answer

Stated up front because two of the tested options are limited by the model
rather than by traffic.

**Sound:** signal retiming, ramp metering, vehicle-mix changes, demand
changes, speed limits. These are represented faithfully — signal phases,
portal inflow rates, vehicle types and posted limits are all first-class in
the model.

**Inconclusive — lane widening.** Adding a lane clones the outermost lane
and wires it to the donor's upstream and downstream connections, but it gets
no junction-internal lanes of its own and routed vehicles keep taking the
canonical next hop. Measured on Lake Shore Drive, the added lanes carry
**6.6%** of the corridor's vehicle-distance where a fully used fifth lane on
a four-lane road would be ~20%. Both widening options therefore come back
near zero, and that result **cannot distinguish "widening does not help
here" from "we did not really widen it."** Do not present either as evidence
about widening.

**Calibration limit.** The scenarios run at `--freeway-scale 2.0`. Lake
Shore Drive congests realistically (35.3 km/h in the recorded hero cut,
against a real 35–45 km/h AM peak) and the downtown grid runs at 8–13 km/h,
which is right for the Loop. The expressway *mainlines* run 59–76 km/h
against a real 25–45 — Kennedy 59.0, Stevenson 60.5, Dan Ryan 66.6,
Eisenhower 76.2, with the network as a whole at 27.7. The reason
is measured and specific: at the demand that would congest them, the
controller cannot drive the fleet — 36% of vehicle-ticks run with no
car-following control at ~12,000 vehicles, against 0.03% at 7,700 — and a
run in that state is not a measurement of anything. See
[ADR-0025](../kb/decisions/ADR-0025-longitudinal-safety-gate.md) and
[Silent Fidelity Failures](../kb/articles/concepts/silent-fidelity-failures.md).

Every run reported here delivered ~100% of its declared demand and ran below
0.1% uncontrolled coasting. Runs that do not meet those bars are called void
by `serve` itself and are not used.
