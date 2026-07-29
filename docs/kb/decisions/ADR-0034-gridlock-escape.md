# ADR-0034: Gridlock is a modelled failure mode with a bounded escape

- **Status:** Accepted
- **Date:** 2026-07-28
- **Amends:** ADR-0010 (junction right-of-way), ADR-0014 (metrics primitives —
  additive: `stranded` on trip records, `stranded_trips` in totals)
- **Scope:** kernel enforcement, two additive metrics fields, and keyframe
  format **v5** (one `u64` per vehicle, written only when a stuck timer is
  running; v2–v4 stay readable and a state with nothing stopped marshals
  byte-identical to v4). No NATS **subject** changes and no network-file
  schema change — but see the migration note below: the keyframe IS a
  published payload.

### Migration note — keyframe v5 on the record plane

Corrected 2026-07-29 (review): the original scope line said "no NATS subject
or payload changes", which was wrong. Keyframes are published on
`ts.{run}.log.keyframe` and are therefore a public interface under the repo's
message-contract rule, so the addition needs a migration note rather than a
denial:

- **Forward:** a v5 keyframe carries one extra `u64` per vehicle. Readers
  older than this commit reject it loudly on the version byte — they do not
  misparse it — so an old reader against a new recording fails at open, not
  silently.
- **Backward:** the writer picks the LOWEST version that can represent the
  state, so a keyframe in which no vehicle has a running stuck timer or a
  discharged stop duty still emits v4, byte-identical to what it emitted
  before. Existing recordings are readable unchanged.

  > **How often that is actually true (corrected 2026-07-29, review).** An
  > earlier draft of this bullet claimed a recording "that never strands
  > anything is bit-identical to the old format." That is wrong, and wrong in
  > the optimistic direction. `stuckTicks` reaches 1 after a *single* tick
  > below 0.1 m/s — one red light, one queue, one yield — so on a signalised
  > or congested network virtually every mid-run keyframe lifts to v5,
  > stranding or no stranding. The version floor is per keyframe, not per
  > run, and the practical reading is: v4 keyframes appear in free-flow
  > stretches and almost nowhere else. Old readers reject far more new
  > recordings than the original wording implied. `engine/keyframe.go`'s
  > comment was accurate throughout; this bullet was the one that drifted.
- **Consumers:** the only readers are `engine` (restore) and the replay
  player, both in-tree and both updated in this commit. No external consumer
  is pinned to the old layout.

## Context

The Chicago network never clears. Three ADRs have now been aimed at that
symptom — ADR-0031 (permissive greens yield), ADR-0032 (car-following looks
down the routed branch), ADR-0033 (a foe's signal is part of its priority
class) — and each fixed a real defect and improved the run without curing it.
On the ADR-0033 baseline, 4,926 vehicles were still driving at the horizon,
with a median stopped time of 2,275 s and a maximum of 5,363 s out of a
5,400 s run: vehicles that never moved at all.

### What is actually happening

**Gridlock is a circular wait on lane capacity.** Vehicle A cannot move until
there is room on the lane ahead; that lane's head cannot move until there is
room on the lane ahead of IT; around a closed cycle of lanes, back to A.

The important part is that *every gate in the kernel is answering correctly*.
"There is no room on the lane ahead" is true at every link of the ring. The
deadlock is a property of the whole cycle and nothing that reasons one
junction at a time can see it. Once the cycle closes it is permanent, because
nobody in it can move until somebody in it moves.

`engine/gridlock_test.go` reproduces it in a four-junction ring — one city
block, fed from four sides, with vehicles routed three-quarters of the way
round before they leave. It gridlocks in under 40 simulated seconds, and in
the frozen state **no junction box is occupied and every vehicle is queued on
an ordinary lane.** There is no defect in the frozen picture to point at.

### The mechanism this ADR corrects

ADR-0033's "Not done here" section recorded the cause as `exitBlocked`
accumulating exit room across short successor chains without reserving it, so
several vehicles claim the same gap. That hypothesis was written from reading
the code. Measured against the run it is **not the dominant mechanism**:

| final 5-minute interval, chi-loop-urban 90-minute baseline | |
|---|---:|
| lanes holding vehicles with **zero distance travelled**, k > 60 veh/km | 427 |
| ...of which ordinary ROAD lanes | **403** |
| ...of which junction internals (boxes) | **24** |
| frozen lane-km | 25.33 |

94% of the frozen mass is on roads, in legitimate queues. Blocked boxes exist
— 24 of them, and the worst is a 1.7 m stub carrying 2.89 vehicle-lengths of
occupancy, which is a straddle — but they are 6% of the problem, not the
cause of it. The ring fixture reproduces the whole failure with the box
discipline working perfectly. **The correction is recorded in ADR-0033 as
well; the remaining stub-straddle is logged under "Not done here" below.**

### Is it the data? Partly, and not this part

Two checks against the actual demand and the actual run
(`scripts/` work is in the task record; the network is `chi-loop-urban`):

- **Every demanded destination is reachable from its origin.** Over 317 flows
  and 478 distinct destinations, zero demand is routed somewhere it cannot
  get to by driving and changing lanes. The OD file is not asking for
  impossible trips. (A first pass over successors ALONE said 31% of peak
  demand was unroutable — that pass was wrong, because ADR-0021 routing
  crosses lanes as well as driving forward.)
- **But vehicles are not following their routes.** Comparing each trip's
  driven distance with the shortest path from its origin to its destination:
  median 1.40×, and **the network carries 109,676 veh-km to satisfy 66,236
  veh-km of demand — 65.6% excess.** 24% of trips drive more than twice their
  shortest path, and the distribution is the SAME for completed trips as for
  the ones still stuck at the horizon, so this is not a symptom of being
  jammed. It is a network-wide route-following defect that inflates the load
  on every lane by two thirds.

That is a serious finding and it plausibly *feeds* gridlock — a network
carrying 1.66× the traffic it needs to is much likelier to close a cycle —
but it is a different defect with a different fix, and it is tracked
separately rather than bundled here.

### Why not prevent gridlock instead

To know that admitting one more vehicle cannot close a cycle, a junction
would have to reason about every cycle running through it: the deadlock
avoidance problem, and the answer is the Banker's algorithm, not traffic
engineering. Real practice does not attempt it either. It reduces the
probability — don't block the box, signal metering, inflow control — and
accepts that gridlock happens; Manhattan is where the word comes from.

What a *simulator* cannot accept is a network that stops for good. A run that
gridlocks at minute 40 reports nothing about minutes 40–90, and the failure
is silent: the totals still add up, every vehicle is still there, and the
output looks like severe congestion rather than a stopped model. Every
Chicago figure measured to date was measured through that.

## Decision

**A vehicle that has been stopped for `Params.StrandAfterS` while waiting on
a junction it cannot enter is removed from the network and counted as
STRANDED.**

Three conditions, all required (`engine/gridlock.go`):

1. **Stopped** — below 0.1 m/s for `StrandAfterS` consecutive ticks
   (default 300 s, SUMO's `--time-to-teleport` default, chosen for the same
   reason: longer than any signal program or ordinary queue, shorter than a
   run).
2. **Head of its lane** — anything behind the head is stopped for a reason
   the kernel already models, the vehicle in front, and removing it would
   unlock nothing.
3. **Blocked at the box it is routed into** — `boxBlocked` is true: a foe
   inside the junction, or no room on the far side to clear it.

> **What "all required" does and does not mean (clarified 2026-07-29,
> review).** The three are all required *at the moment of removal*, not
> throughout the 300 s. Only condition 1 is cumulative: `stuckTicks` counts
> consecutive STOPPED ticks and is reset by motion (≥0.1 m/s) or by
> `resetStuckBehind`. Conditions 2 and 3 are evaluated only once the timer
> has already expired. So a vehicle that spends 300 s stopped mid-queue is
> **armed**, and then strands on the first tick at which head-of-lane and
> `boxBlocked` happen to coincide — which it can reach without ever moving if
> the vehicles ahead of it leave by lane change.
>
> Sol's round-6 reading, and it is correct. Whether the timer *should*
> instead measure continuous head-and-blocked time is a real design question
> and is tracked as its own task: changing it changes which vehicles the
> escape removes, and every Chicago figure quoted here was measured under the
> rule as built. It is not being changed inside the commit that introduces
> it.

Condition 3 is not a refinement, it is the definition, and it was found by
measurement. With conditions 1–2 alone the rule fired on the I-80 M3 fixture
— a freeway with a growing queue and no gridlock anywhere — stranding 8
vehicles and destroying the stop-and-go wave structure that scenario exists
to validate. Those heads were stopped behind the queue on the next lane,
which is car-following doing its job. **A queue that is merely long is not a
jam the model needs rescuing from. A queue whose head cannot enter the
junction in front of it might be.**

A red light is deliberately not a trigger *on its own*: an ordinary signal
queue has a clear box ahead of it, `boxBlocked` is false, and it never reaches
the escape however punishing the cycle. A signal that never goes green is a
broken program — a different pathology, with its own diagnosis.

> **Corrected 2026-07-29 (review).** The original sentence read "a red light
> is deliberately not a trigger" without that qualifier, which overclaims.
> Red does not preclude `boxBlocked`: a head sitting at red for the full 300 s
> whose routed box is *also* occupied that whole time does strand. The
> discriminator is and always was condition 3, not the signal. That behaviour
> is arguably correct — SUMO teleports there too, and a box blocked for five
> unbroken minutes is a jam whether or not the light is red — but it is
> **emergent, not enforced**, and `TestEscapeIgnoresOrdinarySignalQueue` uses
> an empty exit so it cannot tell the two apart. Pinning the
> cross-traffic-during-red case is tracked as its own task.

**After a strand, the stuck timers of the removed vehicle's lane and of the
lanes feeding it (backward, breadth-first, bounded by `maxLaneHops`) are
reset.** This is what makes the escape minimal rather than blunt. Everything
queued behind the stranded vehicle has been stopped exactly as long and is
exactly as eligible; without the reset the ring fixture loses eight heads on
one tick where one unlocks the ring. Backward and bounded, so a cycle is
covered (Chicago's is 8 lanes) while a long arterial queue is reset only as
far as the limit reaches — vehicles further upstream keep their timers,
correctly, because they are waiting on a different piece of road.

### Consequences

Good:

- **A cycle on ordinary roads cannot stop the network for good.** Every cycle
  has a longest-waiting head, and removing one vehicle from a cycle unstops
  the cycle: room appears behind it, its follower moves, and the wave runs the
  whole way round.

  > **Narrowed after external review, 2026-07-28.** This originally read "the
  > network cannot stop for good", and the measurement does not support that.
  > Condition 3 requires the lane AHEAD to be `Internal`, so a vehicle stuck
  > **inside** a junction box — whose successor is an ordinary road or a
  > boundary stub — can never satisfy it and is never rescued. Measured on
  > `base34`: of the 36 lanes still frozen at the horizon, **5 have no
  > internal successor, and all 5 are themselves internal**. Those five boxes
  > stay occupied for the rest of the run and every movement through them
  > stays blocked. The escape covers the 31 road lanes, which is 94% of the
  > frozen mass and the case the ring fixture models — but "cannot stop for
  > good" was an overclaim, and it is corrected here rather than left to be
  > discovered by someone trusting it. Tracked separately; the fix is to walk
  > to the routed controlled internal lane the way the right-of-way gate
  > does, which changes behaviour and needs its own baseline.
- **It is inert on a healthy network.** Nothing reaches the threshold that is
  merely queueing. Every M1–M3 CRC fixture is bit-identical with it enabled,
  the I-80 stop-and-go validation is unchanged, and the full suite passes.
- **It is deterministic** (ADR-0005): a tick counter, a fixed sweep over
  `e.order`, no clock and no RNG.
- **It is never silent.** `Stats.Stranded` and `Stats.StrandedBySection` say
  how many and where; the metrics kernel emits those trips as INCOMPLETE with
  a `stranded` flag and counts them in `stranded_trips`, disjoint from
  `completed_trips`. A run that strands can never be read as a run that
  merely queued.

### Measured on Chicago

Same scenario, same seed, same horizon as the ADR-0033 baseline; the only
change is the escape (`base33` → `base34`, 2026-07-28):

| 90-min chi-loop-urban, seed 1000 | ADR-0033 | + escape | |
|---|---:|---:|---|
| trips completed | 5,581 | **6,024** | +443 |
| still driving at the horizon | 4,926 | **3,440** | −1,486 |
| stranded | — | **1,055** | over 285 sections |
| frozen lanes, final interval | 427 | **36** | −91% |
| vehicles on frozen lanes | ~3,097 (61.8%) | **~250 (6.9%)** | |
| Edie speed | 17.0 km/h | **19.8 km/h** | |
| mean delay per trip | 1,551 s | **1,420 s** | |
| final-interval network speed | 4.3 km/h | **7.9 km/h** | |

Three readings, and the second is the important one:

- **The escape does not merely delete the jam, it unlocks it.** 1,055 removals
  bought 443 *additional real completions* — trips that finished under their
  own power because the vehicle in front of them was taken out of a cycle.
  Deleting 1,055 vehicles from a wedged network would have moved the
  completion count by roughly nothing.
- **The wedge is gone as a structural feature.** 61.8% of on-road vehicles sat
  on lanes travelling zero distance; now 6.9% do. The network is congested and
  draining rather than stopped. This also retires the "wedged, not queued"
  reading for good.
- **1,055 is 10% of demand, and that is the finding, not the fix.** Gridlock in
  this network is not a rare event at a few bad junctions — it is diffuse,
  across 285 sections, and the escape's job here is to have *measured* it.
  The number is the size of the underlying problem (see route abandonment
  below), and it should fall as that is fixed rather than by tuning the
  threshold.

Collision observations rose 661 → 833, which is expected and worth stating:
more vehicles actually traverse junctions when the cycles release. It is not a
regression in the box discipline.

Costs, stated plainly:

- **Vehicles disappear.** A stranded vehicle did not drive out of the network;
  the kernel took it out. It is not in `Despawned` and not in `Arrived`, and
  it is a real hole in the mass balance — an accounted one.
- **The escape does not make gridlock cheap, and should not.** In the ring
  fixture the block gridlocks, bleeds one vehicle per 300 s for 27 simulated
  minutes, and only then has enough slack to drain: 23 of 32 vehicles served,
  9 stranded. That is the honest price of a cycle that closed, and the
  counter is what makes it visible.
- **The threshold is a policy number, not a physical one.** 300 s is
  inherited from SUMO. `StrandAfterS = 0` disables the escape entirely, which
  is what `TestRingGridlocksWithoutTheEscape` uses to pin the raw phenomenon.
- **`boxBlocked` is evaluated once per stuck head per tick.** Bounded by the
  number of vehicles that have been motionless for five minutes, which on a
  healthy network is zero.
- `stuckTicks` is not in the CRC but **is keyframed** (format v5, written only
  when a timer is actually running, so a state with nothing stopped still
  marshals byte-identical to v4).

  > **Corrected in external review, 2026-07-28.** This ADR originally called
  > the timer derived state on `stopDone`'s precedent and left it out of the
  > keyframe, reasoning that a restore only costs an already-frozen vehicle
  > one more `StrandAfterS`. That is true of ADR-0029 warm start, which is
  > undurable, and **false of replay**. `ReplayFromStream` and `Player.seek`
  > both restore from the latest keyframe *at or before* their target and
  > then re-simulate forward verifying every logged CRC. A timer zeroed by
  > that restore strands the vehicle a whole `StrandAfterS` late, and every
  > tick in between has a vehicle in one run and not the other — a replay
  > divergence, not a rounding difference. `stopDone` survives being derived
  > because it only decides whether a vehicle stops twice; this one decides
  > whether a vehicle exists. Pinned by
  > `TestStuckTimerSurvivesAKeyframe`, verified to fail without the fix.

## Tests

`engine/gridlock_test.go` — a four-junction ring whose segments alternate
40 m and 6 m (sub-vehicle-length, as the measured Chicago cycle's 4.7 m and
8.5 m are), fed by four approaches, every vehicle routed three junctions on.

- `TestRingGridlocksWithoutTheEscape` — `StrandAfterS = 0`: the ring fills
  and stops for good. This pins the phenomenon, so the fixture keeps proving
  what it is about even after the escape makes the other tests pass.
- `TestRingWorksThroughGridlock` — the default threshold: the ring gridlocks,
  works through it, and ends empty with every vehicle accounted for as
  despawned or stranded. It also asserts that *most of the demand was still
  served* — an escape that emptied the network by deleting it would satisfy
  every other assertion in the test.
- `TestEscapeIgnoresOrdinarySignalQueue` — the control: a ten-vehicle queue
  under a 100 s red / 20 s green program for 1,200 s strands nobody.
- `TestSealedJunctionBleedsRatherThanFlushes` — a permanently blocked
  junction with twelve queued behind it loses at most one vehicle per
  threshold interval, not the queue. Fails without `resetStuckBehind`.

The I-80 M3 validation (`TestI80StopAndGo`) is the fourth test in practice:
it is the fixture that caught the over-broad first rule, and it passes
unchanged.

## Not done here

**The stub straddle.** `exitBlocked` hands off — returns "not blocked" — as
soon as its room walk reaches an internal lane, without having accumulated
the `Length + S0` it was looking for. On a chain of sub-vehicle-length stubs
that admits a vehicle whose only room is inside the next junction's box.

Measured before and after the escape, because the before-number was
misleading: **24 frozen internals on the ADR-0033 run, 5 on this one**, and
across the whole final interval only **2 internal lanes are straddled at all**
(occupancy > 1.0; the worst is `i7015531323_0_2`, 0.4 m long at 1.78). Of the
31 frozen road lanes, **1** has a frozen internal successor. So the straddle
is not the seed of the cycles: most of what looked like straddling was a
*consequence* of a stopped network, and it dissolved when the network started
moving. Real, tiny, and no longer the next thing to fix. The proposed remedy
if it is ever worth it: keep walking THROUGH empty internal lanes accumulating
length, rather than removing the hand-off (netimport emits 0.2–3.5 m stubs and
the hand-off is what stops those from being read as walls).

**Route abandonment: 66% excess VMT.** The larger lever on network health —
the load on every lane is inflated by two thirds — and not a gridlock fix, so
not in this ADR. It survives the escape unchanged (113,730 driven veh-km
against 68,429 of shortest path on `base34`, **66.2% excess**, versus 65.6%
before), which confirms the two are independent defects.

**Instrumented and confirmed** (2026-07-28, `ROUTE_DIAG` build over the same
scenario/seed, counting only real crossings in `boundaries()`, not the
`exitBlocked`/right-of-way probes):

| routed junction decisions at lanes with >1 successor | 328,779 |
|---|---:|
| took the route hop | 295,053 (89.7%) |
| **fell back to `Successors[0]`** | **33,726 (10.3%)** |
| ...because of a held turn | 0 |
| ...because the destination was unknown | 0 |
| ...because the destination was **unreachable from that lane** | 33,726 (100%) |

So `pickSuccessor`'s fallback is reached by exactly one path, and ADR-0021
named it correctly. But the lateral depth at the moment of the fallback
**corrects the shape of the problem**:

| lateral depth toward the destination when the fallback fired | |
|---|---:|
| −1 (unreachable at ANY depth — genuinely abandoned) | 114 (0.3%) |
| 1 lane change away | 14,304 |
| 2 lane changes away | 17,427 |
| 3–5 lane changes away | 1,881 |

**99.7% of the time the destination is still reachable — the vehicle is one
or two lane changes from a lane that gets there.** It is in the wrong LANE,
not on the wrong road, and ADR-0021's `tryRouteRecovery` exists precisely to
walk it back down that gradient. Recovery is gated on `ForcedFeasible`, so in
the dense traffic where this matters there is no gap to take, tick after
tick, until the junction arrives — and then the fallback is blind
`Successors[0]`, which points wherever the leftmost branch happens to go.

The consequence is circling, and it is measurable: **106,277 crossings onto a
lane the vehicle had already been on, by 2,133 of 10,097 vehicles** — 21% of
the fleet, averaging 50 re-crossings each. That is where 66% excess VMT comes
from. It is also concentrated: 2,161 sections see a fallback, but the top six
road/junction pairs carry 24% of them.

The fix this points at is small and local: when `routeNextHop` is nil, choose
the successor from which the destination is LEAST far rather than
`Successors[0]`, so a vehicle in the wrong lane still leaves the junction
heading the right way. It is a kernel change that moves every measurement
again, so it gets its own ADR and its own baseline.

**Teleport-forward instead of removal.** SUMO moves a stuck vehicle to the
next free position on its route and only removes it if there is none. In a
gridlocked cycle there is no free position nearby, so the fallback is the
common case; and a teleport would book distance the vehicle never drove
through the metrics observer's position-delta accounting. Removal is the
simpler mechanism with the same effect on the deadlock and no fabricated
travel. Revisit if strand counts turn out to be dominated by non-cyclic
blockages, where a forward hop would preserve the trip.

**Diversion before the cycle closes.** The soft layer this ADR does not
implement: a stuck head re-picking among branches that still reach its
destination, which is what a real driver does and would shed load off a
forming cycle rather than rescuing a formed one. It is a behavioural change
with network-wide effects, it needs the route-abandonment work above to land
first (there is no point improving route choice while routes are being
abandoned), and the escape does not depend on it.
