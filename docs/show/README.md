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

## The scenarios

| # | scenario | network | what it is about |
|---|---|---|---|
| 1 | [Chicago — the Loop and the expressways](chi-loop-options.md) | `chi-loop-urban`, 55,555 lanes, 2,204 lane-km | the hero cut: downtown grid plus the Kennedy, Dan Ryan, Eisenhower, Stevenson, Lake Shore Drive and the Jane Byrne |
| 2 | [Chicago Loop CBD](cbd-options.md) | `chi-loop-cbd`, 11,851 lanes, 208 lane-km | the downtown grid alone — 1,057 signals in 208 lane-km, a signal every ~200 m |
| 3 | [Kennedy corridor](kennedy-options.md) | `chi-kennedy`, 15,315 lanes, 961 lane-km | one expressway and its ramp terminals |

## The answers

Spoilers. Each set is one real winner, one option that actively makes
things worse, and two that do nothing — and none of them is guessable from
the label, which is the point.

| scenario | the winner | the trap | the no-ops |
|---|---|---|---|
| the Loop and the expressways | **Peak-hour freight ban** +2.3% (p=0.0002, VMT **+2.2%**) | Longer greens −3.8% | Widen LSD (+0.5%, below the practical floor), Widen the Kennedy (n.s.) |
| Loop CBD | **Free CTA transfers** +5.0% (p=0.042, VMT flat) | Slow Streets −9.5% | Longer greens, freight ban (both n.s.) |
| Kennedy corridor | **Ramp metering** +1.1% (p=0.0022) | Speed harmonisation −3.1% | Widen the mainline, widen the on-ramps (both n.s.) |

Three things worth saying out loud when the reveal comes:

- **Widening never wins.** Four widening options across three scenarios;
  every one lands between −0.3% and +0.7% and none is practically
  significant. That said, read the model caveat below before making the
  strong version of that claim — for widening specifically, this simulation
  cannot fully back it.
- **The two winners on the small grids are not construction.** They are a
  freight ban and a 5% mode shift. On the Kennedy the winner is metering —
  admitting traffic more slowly. Nothing was built in any of them.
- **Ramp metering carries 1.9% less traffic** while raising speed 1.1%.
  It passes the 3% VMT guard, but it is the weakest of the three winners and
  part of its gain is holding cars back rather than moving them better.

## How to read a result

**The primary metric is network mean speed** (Edie's definition over every
vehicle-second in the measurement window). That choice is not cosmetic — on
the CBD pod, three defensible metrics named three different winners from the
same ten paired seeds:

| metric | winner it names | why it is wrong |
|---|---|---|
| `mean_time_loss_s` | Slow Streets (−11.0%) | measured against each lane's free-flow reference; lowering the speed limit lowers the reference, and this option's real trip times are 5.5% **worse** |
| `mean_trip_s` | shorter cycles (−8.2%) | averages over **completed** trips only; this option completes 7.8% fewer of them, finishing the easy trips and stranding the rest |
| `speed_kmh` | congestion charge (+9.3%) | correct as far as it goes, but see the VMT guard |

**Every result is guarded by VMT** (total vehicle-distance in the window).
An option that raises average speed while moving less traffic has not made
the network better at its job. The tool ranks options that carry their
traffic ahead of those that do not, and annotates the rest.

**Statistical significance is not practical significance.** Six paired seeds
of a low-variance simulation put the seed-to-seed spread on chi-loop-urban
network speed at ~0.45%, so a 0.5% effect clears p<0.05 comfortably while
being invisible to anyone driving in it. Options must clear a 1% practical
floor to be offered as the answer; smaller ones are labelled as such.

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
Shore Drive congests realistically (37 km/h against a real 35–45 km/h AM
peak) and the downtown grid runs at 8–13 km/h, which is right for the Loop.
The expressway *mainlines* run 61–77 km/h against a real 25–45. The reason
is measured and specific: at the demand that would congest them, the
controller cannot drive the fleet — 36% of vehicle-ticks run with no
car-following control at ~12,000 vehicles, against 0.03% at 7,700 — and a
run in that state is not a measurement of anything. See
[ADR-0025](../kb/decisions/ADR-0025-longitudinal-safety-gate.md) and
[Silent Fidelity Failures](../kb/articles/concepts/silent-fidelity-failures.md).

Every run reported here delivered ~100% of its declared demand and ran below
0.1% uncontrolled coasting. Runs that do not meet those bars are called void
by `serve` itself and are not used.
