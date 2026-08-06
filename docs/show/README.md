# The show — five scenarios, measured

Three are cropped from Chicago's OSM import. Two are **authored fictitious
networks** built to carry a burden the Chicago cuts cannot: on the imported
network the expressway mainlines run 59-76 km/h against a real 25-45 (see
[audit.md](audit.md)), and the widening options came back uninterpretable
because the router put only 4.8-6.6% of a corridor's traffic on the lanes
that were added. The two pods are small enough to congest honestly with
car-following in control the whole way, and their added roads carry 23-33%
of their section's traffic — so a null result on them means something.

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

## Replaying it on the day

```
cd viz && pnpm build && cd ..                       # once
python3 scripts/serve-baked.py                      # prints both URLs
```

That serves the baked replay and the viz from **one origin on 127.0.0.1**,
which is not a convenience — it is the only local setup that works. The
frame chunks are stored brotli-precompressed and the viz does not decode
them itself; it relies on the browser doing it transparently, which happens
only when the server sends `Content-Encoding: br`. `python3 -m http.server`
does not, and the failure mode is a replay that loads a network and never
shows a vehicle.

- quiz: `http://127.0.0.1:8790/quiz.html`

### The five baselines

One per scenario, each a "do nothing" run of the same scenario directory the
A/B experiments used. Pass `?bake=` an ABSOLUTE URL (below); a root-relative
path fails with `Failed to construct 'URL': Invalid base URL`, because the
manifest URL is the base for resolving every chunk.

| scenario | run | index.json | opening camera | showable window |
|---|---|---|---|---|
| 1 — the Loop and the expressways | `chishow`, 18000 ticks | `/baked/chishow/f941d8888b18/index.json` | `&center=-87.6298,41.8790&zoom=14.5` | — |
| 2 — Loop CBD | `cbdbase`, 7000 ticks | `/baked/cbdbase/7406456b6031/index.json` | `&center=-87.6293,41.8798&zoom=14.5` | **the whole run** — 7000 ticks is the fidelity-gated cut; at 12000 the grid refuses 7% of demand at injection |
| 3 — Kennedy corridor | `kenbase`, 12000 ticks | `/baked/kenbase/03c7bc162e73/index.json` | `&center=-87.7837,41.9390&zoom=13.5` | **the whole run** — the AM peak builds throughout; the measured window is the last 8000 ticks |
| 4 — The Merge | `mergejam`, 12000 ticks | `/baked/mergejam/017140a46b75/index.json` | `&center=-98.46977,39.49933&zoom=14.6` | **ticks 4200-10200 at 8x** = 75 s wall: the shockwave forming at the merge and walking 2 km upstream |
| 5 — Bottleneck Town | `townbase`, 15000 ticks | `/baked/townbase/b6f967b72fe4/index.json` | `&center=-98.4840,39.6963&zoom=14.2` | **ticks 6300-7200 at 1x** = 90 s, one full 86 s cycle at every junction with queues fully developed |

The two pods are the ones to show on camera. They are ~1,000x smaller than
the hero cut, so they paint immediately, and unlike the Chicago cuts their
congestion is car-following under full control the whole way — the merge pod
produces a textbook backward-propagating shockwave at ~10 km/h against a
field range of 15-20.

**Time to first paint scales with the network, and it is long.** Measured on
this machine with the CPU otherwise idle: CBD (7.4 MB network) ~30 s, Kennedy
(9.3 MB) ~40 s, the hero (34 MB) ~90 s. The replay clock and the vehicle
counter run from the start, so a blank map with a ticking counter is loading,
not broken — see the warning below. Open the tab and let it settle before the
camera is on it.

**A backgrounded tab never paints at all, and it looks identical to a bug.**
MapLibre drives style loading and rendering off `requestAnimationFrame`,
which Chrome does not fire in a hidden tab. The page still connects, the
replay clock still advances and the vehicle counter still counts — but
`map.getStyle()` returns `undefined` and *every* layer query answers
"the layer 'network-line' does not exist in the map's style". Two separate
investigations have now concluded from that evidence that the viz had a
source-level regression; both were driving a background tab, and both were
wrong. Before debugging a blank map, check `document.visibilityState`. This
matters most for automation and for anything driving the page headlessly;
a human with the window in front of them will never see it.

**The CBD baseline is 7000 ticks, not 12000, and that is not arbitrary.** At
12000 the run trips `record-hero.sh`'s own fidelity gate: 7% of its demand
never enters the network, first expiry at tick 7327, because the Loop grid
saturates and vehicles can no longer be injected at their origin lanes. A
7000-tick cut delivers 100% of its demand with 0.07% uncontrolled coasting.
The A/B experiments run the full 12000 and carry that 7% loss in both arms —
paired, so it cancels for the comparison — but a recording is durable and
gets held to the stricter bar.

**Pass `?bake=` an absolute URL.** A root-relative path fails with
`Failed to construct 'URL': Invalid base URL` — the manifest URL is used as
the base for resolving every chunk.

**Some shipped artifacts predate a digest fix.** External review found that
the bake's record digest folded every tick-group boundary message twice, so
the `baked/{run}/{hash12}/` content key the fixed code computes differs from
the one the original bakes landed under. The frames themselves are
unaffected — the bake still CRC-verified every tick against the recording —
so the replays are correct. `cbdbase` and `kenbase` were re-recorded and
re-baked under the fixed digest on 2026-08-06 (the re-records reproduced
the originals tick for tick); `chishow`, `mergejam` and `townbase` still
carry their pre-fix hashes and will move to a new hash directory whenever
they are next re-baked — the content key doing its job. Re-bake before
anything new is published from those three.

**Give it a full minute to load before judging it.** The network is a 34 MB
GeoJSON document; on a loaded machine it is ~40 s before a single lane
paints, while the replay clock and the vehicle count are already running.
A blank map with a ticking counter is loading, not broken.

**Open zoomed in, not on the wide shot.** The default fit shows the whole
extract at about z11.7, where lane lines are hairline and vehicles are
hidden — they gate on at z13, and the frame chunks are not even fetched
below it. It reads as a blank screen even after loading. Zoom to z13-15
over the Loop (`-87.63, 41.879`) and the grid, the congestion colouring and
~400 vehicles come up together.

If the `?center=`/`?zoom=` camera work in `viz/src/config.ts` has landed,
skip the zooming and deep-link the opening shot instead — verified against
this recording:

```
.../app.html?bake=<absolute index.json URL>&center=-87.6298,41.8790&zoom=14.5
```

That opens on the Loop grid and the Jane Byrne with traffic already
running, which is the shot worth starting on. Every reload otherwise
re-fits to the whole extract, so without it the zoom is a per-take action.

## The scenarios

| # | scenario | network | what it is about |
|---|---|---|---|
| 1 | [Chicago — the Loop and the expressways](chi-loop-options.md) | `chi-loop-urban`, 55,555 lanes, 2,204 lane-km | the hero cut: downtown grid plus the Kennedy, Dan Ryan, Eisenhower, Stevenson, Lake Shore Drive and the Jane Byrne |
| 2 | [Chicago Loop CBD](cbd-options.md) | `chi-loop-cbd`, 11,851 lanes, 208 lane-km | the downtown grid alone — 1,057 signals in 208 lane-km, a signal every ~200 m |
| 3 | [Kennedy corridor](kennedy-options.md) | `chi-kennedy`, 15,315 lanes, 961 lane-km | one expressway and its ramp terminals |
| 4 | [The Merge](merge-options.md) | `merge-pod`, 44 lanes, 13.8 lane-km — **authored** | a two-lane freeway with one heavy on-ramp, built so the merge is the only bottleneck in the network |
| 5 | [Bottleneck Town](bottleneck-town.md) | `bottleneck-town`, 200 lanes, 24.6 lane-km — **authored** | a four-signal arterial through a small grid, built so the signals are the only bottleneck |

## The answers

Spoilers. Each Chicago menu is one real winner, one option that actively
makes things worse, and two that do nothing — and none is guessable from the
label, which is the point. The two pods break that shape deliberately: on
them **three of four options genuinely work**, so the question becomes
"which helps most" rather than "which helps at all".

| scenario | the winner | the trap | the no-ops |
|---|---|---|---|
| the Loop and the expressways | **Shorter signal cycles** +2.1% (p=0.0021, VMT **+1.5%**) | Longer greens −3.8% | Widen LSD, widen the Kennedy (both n.s. or inside the noise floor) |
| Loop CBD | **Shorter cycles + freight ban together** +6.7% (p=0.0028, VMT **+7.6%**) | Slow Streets −8.8% | Freight ban alone (−0.1%), free CTA transfers (+4.0%, p=0.070 — a near-miss, not a proven no-op) |
| Kennedy corridor | **Peak-hour freight ban** +3.9% (p<0.0001, VMT **+1.8%**) | Speed harmonisation −3.1% | Widen the mainline, widen the on-ramps (both n.s.) |
| The Merge | **Third mainline lane** +57.3% (p=1e-15, VMT **+12.9%**) | Ramp meter −5.9% (fails the throughput guard: it moves delay onto the ramp, 13.6 → 6.6 km/h) | none — the frontage road (+7.3%) and longer acceleration lane (+3.3%) both help |
| Bottleneck Town | **Northern bypass** +25.3% (p=2e-06, trips completed **+15.7%**) | Shorter cycles −12.5% (fails the guard) | none — green wave +17.0% and add-lane +16.3% both help; the southern connector's +7.8% is a different animal, see below |

**The southern connector is the most instructive number on this page**, and
it got more instructive when it was re-measured. It posts **+7.8%** on
network mean speed while completing **6.2% FEWER trips** (n.s., p=0.09) and
taking **4.9% longer per trip** — on vehicle-distance up 13.7%. It moves the
same traffic *further* along a fast new road, and Edie's mean speed is
distance-weighted, so the headline rises while strictly less gets finished.
The VMT guard catches an option that moves *less* traffic; it does not catch
one that moves the same traffic further. Quote trips-completed alongside
speed, or this one reads as a win it did not earn.

It used to post +19.5%, and the difference is not noise — it is that the
connector originally crossed four arterials **as the priority leg**, so every
cross-street vehicle yielded to it. Signalised on the same 86 s cycle as the
rest of the town, which is what building that road would actually entail, its
advantage more than halves and it drops from second place to fourth. The
first result was right about the sign and wrong about the reason.

**All three winners were confirmed on held-out seeds** (2000+, used for
nothing else — see [audit.md](audit.md) for the rule, written before the
numbers). Each carries measurably MORE traffic than its baseline, so none
of them wins by moving less: +1.9%, +7.6% and +2.0% respectively. One
earlier winner did not survive — the CBD's free-transfers option, replaced
by the combined retiming + freight ban above.

**"Exactly one winner" is a property of the menu, not of Chicago.** The
shortlist is built to contain one answer, so where the measurements found
more than one real upgrade, the extras were held back — on the Loop, a
peak-hour freight ban is *also* a genuine upgrade (+2.3% on the discovery
seeds, +1.8% on held-out, carrying more traffic on both). It is off the menu
because it is the answer to the Kennedy scenario, and an answer that repeats
across scenarios stops being a question. The two are within 0.7 points of
each other and their ranking REVERSED between seed sets, so this study
cannot say which is better — only that both work. What the measurements
support is the narrower claim: *of the four options on this menu, exactly
one is a real upgrade.* Every tested option is listed under each menu, and
[audit.md](audit.md) works through this and the other corrections.

Three things worth saying out loud when the reveal comes:

- **Widening never wins.** Four widening options across three scenarios;
  every one lands between −0.3% and +0.7% and none is practically
  significant. That said, read the model caveat below before making the
  strong version of that claim — for widening specifically, this simulation
  cannot fully back it.
- **Nothing was built in any of the three winners.** They are a signal
  retiming, a retiming paired with a freight ban, and a freight ban. Every
  option that laid concrete came back flat — and the one option that got
  *close* by removing cars (a 5% mode shift) failed on unseen seeds.
- **The same signal change helps and hurts depending on direction.** Shorter
  cycles are the Loop's winner at +2.1%; longer cycles are its trap at
  −3.8%. Cycle length is the single largest lever found anywhere in this
  work, in both directions.
- **The freight ban wins on the Kennedy and does nothing in the CBD on its
  own** (+3.7% against −0.1% on held-out seeds). Trucks cost you where
  vehicles are fast and merging; in a grid where nothing exceeds 13 km/h a
  truck is just another vehicle waiting at a light. But pair that same
  useless-alone freight ban with a retiming that also did not reach
  significance alone (+3.7%, p=0.058), and the CBD gets its biggest result
  anywhere: **+6.7%**. Two nothings make the best something in this dataset.

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

**Routing baseline (2026-07-31).** Every number on this page was measured
on STATIC routing. ADR-0036's congestion-adaptive routing is now the engine
default; re-running `scripts/whatif.py` today measures a different baseline
unless the manifests set `adaptive_routing: false`. Whether each published
effect survives under adaptive routing is unmeasured — the one bracket we
ran (baseline demand, seeds 1000–1003) showed adaptive strictly better than
static, but intervention effects need not move the same way.

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

**The expressways in the hero baseline do NOT congest.** Measured on the
recorded cut over the measurement window (tick 4000+, Edie's definition):

| corridor | speed | veh-km | real AM peak |
|---|---:|---:|---|
| DuSable Lake Shore Drive | **35.3** | 11,120 | 35–45 — right |
| Jane Byrne Interchange | **35.8** | 515 | plausible |
| the grid (everything not a named expressway) | **17.8** | 30,726 | right |
| Kennedy (I-90/94 N) | 59.0 | 8,467 | 25–45 — **too fast** |
| Stevenson (I-55) | 60.4 | 4,939 | **too fast** |
| Dan Ryan (I-90/94 S) | 66.6 | 6,819 | **too fast** |
| Eisenhower (I-290) | 76.2 | 5,461 | ~30 — **far too fast** |
| network | 27.7 | 68,045 | |

So the congestion in this recording is real on Lake Shore Drive, at the Jane
Byrne and across the downtown grid, and absent on four of the five
expressway mainlines — they run at or near free flow. Do not point at the
Kennedy on screen and describe what it shows as rush hour.

This has a consequence for the menus that is easy to miss: **the two freeway
widening options were tested on roads that were never congested.** Adding a
lane to a free-flowing motorway does nothing in any model, so `kennedy-widen`
(+0.19%) and `mainline-widen` (+0.81%) are close to tautologies rather than
findings. `pod-chi-loop.sh` says as much for the Kennedy — it is on the menu
deliberately as a corridor that is NOT the bottleneck, a control on whether
the harness can return "no effect" at all. It passes that test. It is not
evidence about widening.

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

**This bar is met by the two authored pods and NOT by the three Chicago
A/B tables.** Corrected 2026-07-27; the previous version of this paragraph
claimed it for every run on the page, which is false.

- **The Merge and Bottleneck Town**: yes. Both were re-measured serially in
  2026-07-27, both reports carry `"voided": []`, delivery is 100% on every
  arm, and coasting sits at 0.06-0.07% against the 0.1% bar.
- **The recordings**: yes. `chishow` logs 0.03% coasting
  ([audit.md](audit.md)) and `chishow35` 0.07%; `record-hero.sh` refuses to
  bake a run that misses either bar.
- **The Loop-and-expressways A/B table**: **no, and the gap is large.**
  `scripts/whatif.py` passes no `-drivers`, so every arm ran on ONE driver
  replica. Measured on the `chi-loop-urban` baseline arm at 12,000 ticks
  with *eight* replicas: **1.49%** uncontrolled coasting. On one replica it
  is worse — `serve`'s own `-drivers` help records **35.75%** of
  vehicle-ticks with no controller intent at ~12,000 vehicles on this
  network.
- **The CBD and Kennedy A/B tables**: also single-replica, but their
  fleets (~1,000-1,700 active vehicles) sit in the regime one replica
  drives cleanly — the 2026-08-06 re-recordings of both baselines in that
  exact configuration (one driver, capacity 40000) log **0.08%**
  uncontrolled coasting, under the bar. The CBD table's fidelity gap is a
  different one, documented above: at 12,000 ticks the saturated grid
  refuses ~7% of demand at injection, carried paired in both arms.

What that does and does not mean. It does NOT mean the Chicago rankings are
wrong: every arm ran under the same condition on the same seeds, and the
comparison is paired. For the full-network table it DOES mean the absolute
speeds are not a measurement of the scenario as written, that they would be
refused outright by the fidelity gate this repo now applies, and that the
caveat already attached to those rows — expressway mainlines running faster
than the real thing — has this as one of its causes rather than being a
separate quirk. Re-measuring that table with `-drivers` is tracked.
