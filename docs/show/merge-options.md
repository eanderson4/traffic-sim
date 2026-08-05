# Scenario B — "The Merge"

A fictitious two-lane freeway with one heavy on-ramp, authored from scratch
so that **the only bottleneck in the network is the merge**. Nothing here is
imported from OSM; there are no signals except the one a variant adds, no
side streets, no junction guesses. When a vehicle slows down on this network
it slowed down because of the merge, and that is the point of building it
rather than cropping a city.

Everything below is reproducible from checked-in sources. `data/` is
gitignored, so the scenario itself is not — it is generated:

```
python3 scripts/demos/merge-pod.py --pod data/scenarios/merge-pod
```

| durable source | what it is |
|---|---|
| `scripts/demos/merge-pod.py` | the network authoring script, all five scenario dirs, and the corridor map |
| `scripts/demos/merge-serve.sh` | `serve` with `-exit-routing=false`, the configuration this scenario must run in (§"What went wrong first") |
| `scripts/demos/merge-report.py` | space-time diagram, per-lane distance shares, engineer's summary — all read from a `-metrics-out` file |
| `scripts/demos/merge-quiz.py` | the quiz payload, in `curate.py --json` schema |
| `docs/show/reports/merge-pod.json` | the 12-seed paired A/B report every number in the table comes from |
| `docs/show/quiz/merge-pod.json` | the quiz shortlist |
| `docs/show/labels-merge.json` | guest-facing option labels |

Re-derive any A/B number without running a simulation:

```
python3 scripts/whatif.py --report docs/show/reports/merge-pod.json --metric speed_kmh
python3 scripts/whatif.py --report docs/show/reports/merge-pod.json --metric vmt_km
```

## The network

4 km of two-lane mainline, a 300 m acceleration lane, a 1.2 km downstream
tail, and a 3.1 km on-ramp. 44 lanes. The mainline is cut into 250 m cells so
the metric kernel reports a speed per cell per 60 s — that grid *is* the
space-time diagram the shockwave is read off.

```
 y=+7.0  ................................ [X]   third lane (mainline-lane only)
 y=+3.5  =========================================  mainline left   (lane _1)
 y= 0.0  =========================================  mainline right  (lane _0)
 y=-3.5                              [A]===        acceleration lane (endWall)
                                    /
 y=-200  ramp ------------------o--(curve)
                                |
 y=-200                         \.................  frontage road (variant only)
         x=0                  2900  4000 4300 4700   5200
```

The merge is not scripted. `engine/policy.go` already has the primitive: a
lane flagged `endWall` is a dead end, `DecideLaneChange` treats a vehicle on
one as a **mandatory** changer (hard gap drops to `MinGapMerge`, `b_safe`
relaxes by `MergeUrgencyGain` over the last `MergeZone` = 200 m), and
`accelEval` puts a virtual standing vehicle at the lane end so a vehicle that
finds no gap brakes rather than drives off the end. The acceleration lane
*is* the bottleneck; ramp traffic has 300 m to find a gap.

**Demand: 3200 veh/h on the mainline (two portal lanes, 6% trucks) and
1100 veh/h on the ramp (20% trucks), 12,000 ticks = 20 minutes.** Both
portals use constant headways — a measurement, not a preference; see
§"What went wrong first". The site sits in open country at 39.5 N, 98.5 W
so nobody mistakes it for a real interchange.

## The jam is real — this is the headline

The scenario carries a specific burden. The big Chicago run does not congest
its freeways honestly: its expressway mainlines run 59-76 km/h against a real
AM peak of 25-45, because at the demand that *would* congest them the driver
service cannot drive the fleet ([audit.md](audit.md),
[Silent Fidelity Failures](../kb/articles/concepts/silent-fidelity-failures.md)).
This network is two orders of magnitude smaller, so it should be able to
produce a jam with car-following in control the whole time. It does.

From the recorded baseline (`data/mergejam.log`, seed 1000, 12,000 ticks):

| | measured | bar |
|---|---:|---|
| demand delivered | **100.00%** (1435 injected, 0 expired) | ≥ 95% |
| uncontrolled coasting | **0.07%** (3,035 of 4,535,968 vehicle-ticks) | < 0.1% |
| peak simultaneously active vehicles | **588** | knee is ~12,000 |
| collisions | **0** | — |
| longitudinal safety gate engagements | **0** | — |
| boundary crossings landing overlapped | 1 | — |
| lane changes | 340 | > 0, i.e. merging happened |

588 vehicles is 5% of the ~12,000 at which the driver loses 36% of its
vehicle-ticks, and the coasting that does occur is not the saturation mode:
it is the two-to-four ticks between a vehicle spawning and its claim being
granted, bounded at 3 vehicles in any one tick on an unloaded machine. The
run passed `record-hero.sh`'s fidelity gate, which is what allowed it to be
baked at all.

### The shockwave

Right mainline lane, 250 m cells, 60 s intervals, speeds in km/h. This is the
recorded artifact, not a special run:

```
  tick  sim s  up00 up01 up02 up03 up04 up05 up06 up07 up08 up09 up10 up11 up12 up13 up14 up15  mrg
  1800    180    80   72   67   65   67   71   74   77   75   74   74   78   81   82   83   84   77
  2400    240    83   72   69   69   69   67   68   68   69   70   71   72   76   78   79   80   71
  3000    300    80   71   69   68   68   69   69   71   72   71   70   70   69   70   71   72   63
  3600    360    81   71   68   68   68   68   69   70   71   72   73   74   74   74   73   49   14
  4200    420    75   66   65   66   67   67   65   66   68   70   71   72   73   74   73   22   26
  4800    480    81   67   63   64   65   65   65   65   66   67   68   69   69   70   44   18   48
  5400    540    79   70   68   66   65   65   65   64   64   65   65   66   67   61   19   39   56
  6000    600    83   68   63   64   64   64   65   66   67   66   65   65   63   27   33   52   62
  6600    660    80   70   66   63   62   62   63   63   64   67   66   65   32   22   49   57   60
  7200    720    83   73   68   66   64   62   62   63   63   63   64   44   19   44   59   63   63
  7800    780    80   74   73   71   67   63   62   62   63   63   55   16   41   59   60   61   65
  8400    840    73   66   68   71   72   71   69   67   65   61   20   34   56   67   69   63   61
  9000    900    77   65   63   64   65   67   69   71   70   26   26   54   65   70   71   69   64
  9600    960    81   72   65   63   64   64   64   64   45   24   51   60   67   70   72   72   70
 10200   1020    81   70   68   65   64   65   64   61   23   44   60   62   63   67   70   72   73
 10800   1080    79   71   69   67   66   67   67   38   27   54   62   64   63   63   64   65   67
 11400   1140    82   69   66   67   67   66   58   21   46   61   66   68   68   64   63   64   64
```

Read the diagonal. A band of 16-27 km/h forms in the merge cell at tick
3600, appears in `up15` at 4200, and walks upstream one 250 m cell roughly
every 900 ticks, reaching `up07` — 2 km back — by tick 11400. That is
**2000 m in 720 s, about 10 km/h backward**. Real stop-and-go waves run
15-20 km/h, so this is the right phenomenon at the low end of the right
speed. Ahead of the band and behind it the road is at 60-75 km/h, and the
same diagonal appears in the left lane, so this is a full-width freeway
breakdown, not one lane misbehaving.

Reproduce it from the recorded metrics without simulating:

```
python3 scripts/demos/merge-report.py data/mergejam.metrics.json --wave --lane 0
```

### The control that proves it is the merge

Same network, same mainline demand, same seed, **ramp demand switched off**:

| | network mean speed | queue extent | bottleneck throughput | delivered |
|---|---:|---:|---:|---:|
| ramp off (control) | **67.5 km/h** | 0 m | 3043 veh/h | 100% |
| ramp on (baseline) | **41.7 km/h** | 2000 m | 3129 veh/h | 100% |

Nothing else differs. The mainline alone runs free the whole 4 km; add
1100 veh/h of ramp traffic and the same mainline breaks down 2 km upstream
while the throughput past the merge rises by only 3%. That is a merge
bottleneck, definitionally. The control was run three times on separate
occasions under different machine load and landed at 67.41 / 67.47 /
67.52 km/h — the 26 km/h gap is not run-to-run drift.

## The options

Baseline `base`, **12 paired seeds** (seed-major submission), 12,000 ticks,
warmup 3000. Primary metric `speed_kmh` (Edie). Every option carries the
paired VMT guard.

| option | Δ vs base | Δ% | p | d | VMT Δ% | VMT p | guard | verdict |
|---|---:|---:|---:|---:|---:|---:|---|---|
| **Add a third lane through the merge** (`mainline-lane`) | +24.34 | **+58.3%** | 3e-15 | +17.7 | +13.1% | 4e-15 | ok | **UPGRADE** |
| Build a frontage road (`frontage-road`) | +2.90 | +6.9% | 3e-05 | +2.0 | +2.2% | 7e-05 | ok | UPGRADE† |
| Lengthen the merging lane 300→700 m (`accel-extend`) | +1.97 | +4.7% | 8e-06 | +2.3 | +3.2% | 6e-09 | ok | UPGRADE |
| Meter the on-ramp (`ramp-meter`) | −1.95 | −4.7% | 3e-04 | −1.5 | −5.3% | 5e-10 | **fails** | WORSE |

† its diversion share is an assumption we supplied — see the caveats.

**Three of four options genuinely work, and that is the finding.** This menu
does not have the shape the Chicago menus have (one winner, three no-ops),
and it should not be forced into it: a merge bottleneck is the one situation
where several unrelated interventions all help, because they all relieve the
same single constraint. The reveal here is the *ranking* and the *cost*, not
"which one works".

### What the corridor split shows, and the network mean hides

`whatif.py --corridors` splits the same runs into mainline / ramp
(means over 12 seeds, km/h):

| option | mainline | ramp | delivered |
|---|---:|---:|---:|
| base | 66.4 | 13.5 | 100% |
| mainline-lane | **69.5** | **67.7** | 100% |
| frontage-road | 66.7 | 23.2 | 100% |
| accel-extend | **62.8** | 25.6 | 100% |
| ramp-meter | **67.4** | **6.6** | **96%** |

Two results only visible here:

- **The ramp meter does exactly what ramp meters do.** It is the only option
  that improves the mainline *without building anything* (66.3 → 67.3) and it
  pays for that by taking the ramp from 13.5 to 6.6 km/h. The network mean
  scores it −4.7% because it averages the road it fixed with the road it
  sacrificed. It also fails the throughput guard (−5.3% vehicle-distance) and
  drops to 96% demand delivery, because its own ramp queue eventually reaches
  its own portal — a fixed-time meter with no queue override, failing the way
  untuned meters fail in the field. **So the cheap control option does NOT
  compete with concrete here.** It moves the delay; it does not remove it.
- **Lengthening the acceleration lane makes the mainline worse** (66.3 →
  63.2) while making the ramp much better (13.5 → 25.2). More ramp traffic
  succeeds in merging, and the mainline pays for it. Net +4.7% because the
  ramp's gain is larger than the mainline's loss — a real but redistributive
  effect, not a capacity increase.

### Everything tested

Raw significance verdicts before the practical floor is applied:

| option | Δ% | p | d | verdict |
|---|---:|---:|---:|---|
| mainline-lane | 58.3% | 0.0000 | 17.71 | UPGRADE |
| frontage-road | 6.9% | 0.0000 | 1.97 | UPGRADE |
| accel-extend | 4.7% | 0.0000 | 2.25 | UPGRADE |
| ramp-meter | −4.7% | 0.0003 | −1.49 | WORSE |

Every effect here is 4.7-58%, against a documented ~0.32% noise floor and a
1% practical floor. Nothing on this menu is inside the noise, which is the
one respect in which this scenario is easier than the Chicago ones.

### Menu size and visual legibility

The pod is **base plus exactly four options**, which is the menu size the
show wants, so nothing was cut. Ranked by how visible the change is on
screen, which is the tie-breaker if one ever has to go:

1. `mainline-lane` — a third lane appears and the queue vanishes. Unmissable.
2. `ramp-meter` — a signal head appears on the ramp, the ramp queue grows to
   the horizon, the mainline clears. Two visible effects in opposite
   directions; the best *story* on the menu.
3. `frontage-road` — a whole new road appears carrying traffic. Visible, but
   it is a road nobody was looking at.
4. `accel-extend` — 400 m more of one stripe. **Effectively invisible at any
   zoom that also shows the shockwave**, and its measured effect is the
   smallest and the most equivocal (helps the ramp, hurts the mainline). This
   is the one to drop if the menu must shrink.

## Did the new road get used? (the trap-4 check)

Chicago's widening options came back flat and *could not be interpreted*,
because the added lanes carried 4.8-6.6% of their corridor's vehicle-distance
where a real added lane would carry ~20% — the router never sent anyone onto
them, so "widening does not help" and "we did not really widen it" were
indistinguishable. That check is mandatory here.

**`mainline-lane`: the added lane carries 33.2% of the widened section's
vehicle-distance.** An evenly used third of three lanes carries 33.3%. Per
250 m cell, right / left / added:

| cell | right | left | **added** |
|---|---:|---:|---:|
| up12 | 39.8% | 33.5% | **26.7%** |
| up13 | 36.8% | 33.4% | **29.8%** |
| up14 | 35.3% | 33.6% | **31.2%** |
| up15 | 32.8% | 34.0% | **33.2%** |
| mrg | 18.2% | 35.9% | **45.9%** |
| aux | 29.1% | 33.2% | **37.7%** |
| dn0 | 30.1% | 33.5% | **36.4%** |
| dn1 | 30.4% | 33.6% | **36.0%** |

Uptake rises from 27% at the point the lane begins to 46% through the merge
itself — vehicles find it, and they find it hardest exactly where they need
it. **This is a traffic result, not a routing artifact**, and it is the
difference between this widening test and the Chicago one.

**`frontage-road`: the diverted flow is 165 veh/h over a 2.3 km road**, and
the road runs at **48.6 km/h** — free-flowing, so it is absorbing the
diversion rather than becoming a second queue. Network VMT rises **+2.2%**
(p=7e-05), which is the check that matters: the option adds vehicle-distance
instead of re-labelling distance the mainline was already carrying. The road
is used as specified — but the specification is ours (see caveats).

## What went wrong first

Two things in this scenario were measured wrong before they were measured
right. Both are now in the KB
([Silent Fidelity Failures](../kb/articles/concepts/silent-fidelity-failures.md)
§5 and §6). Both produced output that looked exactly like traffic.

**1. Exit routing pinned every vehicle in its lane.** The driver draws each
vehicle a destination among the exit lanes reachable *through successors*
from its current lane. On a freeway every lane has one successor, so every
vehicle drew the lane it was already in, its lateral route depth was 0
everywhere, and `routeHopOK` then vetoed every discretionary lane change as a
move away from route. Measured on the ramp-off control: **1 lane change in
12,000 ticks with routing on, 67 with it off.** Nobody could overtake a
truck, and the "congestion" that produced was worth 4 km/h of network mean
speed. Every run reported here uses `-exit-routing=false` via
`scripts/demos/merge-serve.sh`. This is a city-network feature degenerating
on a corridor, not a bug in the router.

**2. The portal delivers less the harder you push it.** `injectionPlan`
enters a vehicle at the fastest speed from which it could still brake behind
whatever is on the lane; a Poisson burst therefore enters slow, and at
`Car.A = 0.73 m/s²` it takes ~600 m to clear, holding the injection point
while it does. Past a threshold the portal locks into a low-speed platoon and
its throughput *drops*. Measured on one 250 m two-lane portal, Poisson
arrivals:

| requested (veh/h/lane) | delivered | speed in the portal cell |
|---:|---:|---:|
| 1000 | 1007 | 85 km/h |
| 1400 | 1373 | 70 km/h |
| 1800 | **993** | **34 km/h** |
| 2200 | **993** | **34 km/h** |

Asking for 1800 delivers less than asking for 1400 — a capacity drop, not a
ceiling. A demand sweep that turns the knob up until the network congests
sails straight past that knee and measures a *lighter* scenario at a *slower*
portal, then reports it as a jam. `spacing: constant` removes the burst; the
same portal then holds 1600 veh/h/lane at 100% delivery, which is what this
scenario uses.

## The demand boundary

Trap 3 in reverse: this scenario *wants* a queue that backs up, but a queue
that reaches an origin portal stops demand entering and voids the run. The
window is narrow and was mapped by sweep (final geometry, constant headways,
`-exit-routing=false`, seed 1000, warmup 3000):

| mainline/lane | ramp | total veh/h | delivered | network km/h | where the loss is |
|---:|---:|---:|---:|---:|---|
| 1600 | 700 | 3900 | 100.0% | 51.6 | — |
| 1600 | 900 | 4100 | 100.0% | 47.7 | — |
| **1600** | **1100** | **4300** | **100.0%** | **42.7** | **— (chosen)** |
| 1600 | 1300 | 4500 | 100.0% | 36.1 | — |
| 1600 | 1500 | 4700 | 98.8% | 34.5 | ramp portal |
| 1400 | 1100 | 3900 | 100.0% | 44.6 | — |
| 1800 | 1100 | 4700 | **75.9%** | 45.3 | **mainline portals** |
| 2000 | 1100 | 5100 | **66.2%** | 52.1 | **mainline portals** |

Two different walls, and they fail in opposite directions:

- **Above ~1300 veh/h of ramp demand the RAMP portal starves.** The ramp
  queue — which is the thing the scenario is about — grows back 3.1 km and
  reaches its own origin. This is honest congestion doing it, and it is the
  boundary the scenario is designed to sit just inside.
- **Above ~1600 veh/h/lane of mainline demand the MAINLINE portal starves,
  and it does so without the network being congested at all** (note that
  network speed goes *up* at 1800 and 2000 — fewer cars entered). That is
  mechanism 5 above, and it is the more dangerous wall because it looks like
  a better result.

The chosen point, 3200 + 1100 = 4300 veh/h, sits inside both with margin and
delivers 100% of its demand on 12 of 12 A/B seeds.

One caveat on the table: the `queue extent` column in
`merge-report.py --summary` is a *window-mean* threshold, so a wave that
moves through a cell rather than parking in it can read 0 m while the
space-time grid clearly shows it. Read the grid, not the scalar.

## How to watch it

Three baked replays (ADR-0023), all 12,000 ticks at seed 1000, all past
`record-hero.sh`'s fidelity gate:

```
python3 scripts/serve-baked.py                 # serves data/baked + viz/dist on 8790
```

| take | run | bake | delivered | coasting |
|---|---|---|---:|---:|
| the jam (**start here**) | `mergejam` | `/baked/mergejam/017140a46b75/index.json` | 100% | 0.07% |
| the fix | `mergefix` (mainline-lane) | `/baked/mergefix/8f5564dc776c/index.json` | 100% | 0.09% |
| the trap | `mergemeter` (ramp-meter) | `/baked/mergemeter/ed4d8711170e/index.json` | 96% | 0.06% |

```
http://127.0.0.1:8790/?bake=http://127.0.0.1:8790/baked/mergejam/017140a46b75/index.json&center=-98.46977,39.49933&zoom=14.6
```

`?bake=` must be an **absolute** URL — a root-relative path throws
`Failed to construct 'URL': Invalid base URL`, because the manifest URL is
the base for every chunk.

**Opening camera** `-98.46977,39.49933` at **z14.6**: the whole 5.2 km
corridor across the viewport, merge right of centre, upstream queue running
off to the left. Vehicles gate on at z13 in baked mode, so do not open wider.
Two alternatives:

| shot | center | zoom |
|---|---|---|
| whole corridor (recommended) | `-98.46977,39.49933` | 14.6 |
| merge and the queue head | `-98.46279,39.49929` | 15.5 |
| merge close-up, ramp in frame | `-98.46046,39.49901` | 16 |

**The showable window is ticks 4200-10200, played at 8×** — 600 s of sim in
**75 s of wall clock**. That is the wave forming in the merge cell and
walking 2 km upstream: everything before 4200 is the network filling and
everything after 10200 is the same wave continuing. The replay panel's speed
selector caps at 8×, so 8× is the fastest legible pass; seek to tick 4200,
select 8×, play.

**Not a blocker — this was the hidden-tab trap.** An earlier revision of this
file recorded a "source-level regression" here: no map layers for any baked
replay, `network-line` absent from the style, blank map, reproduced on the
shipped `cbdbase` artifact. That diagnosis was wrong, and it was the second
investigation to reach it from the same evidence. MapLibre needs a paint
Chrome does not deliver to a background tab, so the stream, clock and counter
advance while the style stays empty. See [README](README.md) ("A backgrounded
tab never paints at all") — check `document.visibilityState` before debugging
a blank map. The URL above is correct and paints.

Signal heads need a separate step after baking — the pipeline does not do it.
**Already run for `mergemeter`** (1 head, 1 stop bar, `furniture` member
patched into its `index.json`); re-run it after any re-bake:

```
cd viz && node scripts/bake-furniture.mjs ../data/baked/mergemeter/ed4d8711170e
```

Only the `ramp-meter` bake has a signal to draw. `bake-furniture.mjs` throws
on a bake with an empty `signals.chunkBytes`, so do **not** run it on
`mergejam` or `mergefix`.

## What this cannot answer

- **The frontage-road diversion is an input, not a result.** The engine has
  no route-choice model, so nothing in it decides how many drivers prefer a
  50 km/h surface road to a queueing ramp. `--frontage-share` sets it, at 15%
  of ramp demand (165 veh/h), implemented as an ADR-0021 weighted destination
  on a second flow from the same portal. Read that option as "what a
  diversion of this size buys", never as "this is how much traffic would
  divert". It is the only option on this menu whose answer depends on an
  assumption we supplied, and its +6.9% is therefore the least trustworthy
  number here. The share was halved from the 30% this originally shipped
  with, on the grounds that 30% of ramp traffic voluntarily leaving a freeway
  for a surface street is a large claim to make on the audience's behalf;
  the effect roughly halved with it (+16.8% → +6.9%), which is itself the
  clearest evidence of how much this option's headline rests on the input.
- **The ramp meter is fixed-time (2 s green / 2 s red) and has no queue
  override,** and the shipped arm was deliberately not tuned against the
  demand — tuning it until it wins is engineering the answer. Real metering
  systems flush the ramp when the queue nears the surface street.

  **The timing was then swept, and no setting rescues it** (2026-07-27,
  `scripts/demos/meter-sweep.sh`, 12 seeds × 12,000 ticks per arm, 84 runs
  serialised on an idle machine):

  | green:red | ramp veh/h | Δ network speed | p | ramp km/h |
  |---|---:|---:|---:|---:|
  | none (base) | — | — | — | 19.1 |
  | 2:1 | ~2,400 | −4.9% | 0.0215 | 11.4 |
  | 2:2 (shipped) | ~1,800 | −5.9%* | 0.0189 | 9.5 |
  | 2:3 | ~1,440 | −5.5% | 0.0165 | 10.6 |
  | 2:4 | ~1,200 | −6.5% | 0.0109 | 9.4 |
  | 2:6 | ~514 | −7.0% | 0.0037 | 7.3 |
  | 4:2 | ~2,400 | −5.7% | 0.0097 | 10.5 |

  \* The shipped arm measures **−4.7%** in the main table above, not −5.9%.
  The sweep is a separate batch, run at higher concurrency before the
  fidelity gate existed, and `serve`'s coasting figure is contention-
  sensitive (see [Silent Fidelity Failures §7](../kb/articles/concepts/silent-fidelity-failures.md)).
  Its rows are internally comparable — same batch, same machine state, same
  seeds — so the RANKING across timings stands, but the absolute deltas are
  not comparable with the serially-measured table above and should not be
  quoted alongside it. Re-running the sweep serially is tracked; the
  conclusion below does not depend on it, because it rests on the ordering.

  Every timing across a 4.7× range of meter rates is worse than not metering,
  and every one moves delay onto the ramp. The effect is close to monotonic
  in restrictiveness, and the most permissive meter tested — barely a meter
  at all — still costs 4.9%. So the finding is **not** an artifact of one
  badly-chosen cycle: on this geometry, at this demand, the stop-and-go the
  meter imposes on the ramp costs more than the smoother merge buys back.
  What the sweep does not test is a queue-responsive meter, which is a
  different control law rather than a different timing.
- **Both portals use constant headways.** Real arrivals are burstier. The
  alternative was losing demand to the injection rule rather than to traffic,
  which is worse — but a Poisson-arrival version of this scenario would
  congest more easily than this one does, so the demand numbers here are not
  transferable to a Poisson pod.
- **`mainline-lane`'s +58% is a bottleneck being removed, not a road being
  improved.** Three downstream lanes carry ~5,850 veh/h against 4,300 of
  demand, so the merge stops binding entirely. It is the correct answer to
  this scenario and it would not generalise to a corridor whose demand still
  exceeded the widened capacity.
- **This is a fictitious network with no calibration target.** It is not
  claimed to reproduce any real interchange. The claim is narrower and it is
  the one that matters: the congestion in it is car-following and
  lane-changing under full control, and that can be shown.
