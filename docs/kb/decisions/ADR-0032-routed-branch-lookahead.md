# ADR-0032: Car-following looks down the routed branch

- **Status:** Accepted
- **Date:** 2026-07-28
- **Amends:** ADR-0021 (route following), ADR-0010 (junction right-of-way)
- **Scope:** kernel enforcement only. No NATS subject or payload changes, no
  network-file schema change.

## Context

`leaderAt` walks lane successors when the current lane holds nobody ahead, so
a vehicle can see a queue on the far side of a junction. It took the default
branch at every hop:

```go
next := cur.Successors[0]
```

At a diverge that is the wrong road. A vehicle routed onto the second branch
followed the traffic on the first, and discovered its own branch only at the
moment it crossed onto it.

Measured on `chi-loop-urban`, seed 1000, tick 10,491 → 10,492 — one tick, the
crossing:

| tick | lane | leader gap | leader speed |
|---|---|---:|---:|
| 10,491 | `n48784593_3` (approach) | 831.19 m | 17.19 m/s |
| 10,492 | `i10241972552_0_4` (its branch) | **19.37 m** | **0.00 m/s** |

The 831 m of free-flowing traffic was down `Successors[0]`, a road this
vehicle was not taking. Its own branch held a standing queue that had been
parked for at least the previous 100 ticks — not a sudden stop, just
invisible.

Stopping from 20 m/s at the gate's `SafetyDecel` needs v²/2b = 22.2 m. It had
19.4 m. It braked on the clamp for every one of the next 22 ticks and still
ended **2.83 m inside** the queue tail, where it sat for 437 ticks (44 s)
until the queue ahead moved.

That single pair of vehicles was **437 of the run's 801 collision
observations (55%)** — the whole of the largest remaining hotspot, from one
car and one wrong lookahead.

### Why this survived

The same class of error had already been found and fixed twice, in both
neighbours of this code:

- `updateStats` measures the boundary pair against `pickSuccessor`, with a
  comment explaining that using `Successors[0]` "invents overlaps wherever a
  queue stands at a multi-way junction" — it was once 59% of the run's total.
- `exitBlocked` walks the exit chain with `pickSuccessor`, "v's actual route,
  not the default".

Measurement and junction entry both knew about routing. Car-following was the
last place still reading the default branch.

The entry gate is also why the crash still happened with `exitBlocked`
working correctly: it refuses entry when the exit chain has no ROOM, and at
that moment the branch had 19 m of room. Room is not the same question as
speed. The gate admitted a vehicle that fitted; nothing had told it to slow
down first.

## Decision

**The leader walk follows the successor the vehicle is routed to.**

`leaderAt` keeps its signature and delegates to `leaderAtFor(lane, s, skip,
as)`, where `as` names the routing perspective. Past an empty lane the walk
takes `pickSuccessor(cur, probe)` instead of `Successors[0]`.

Two details carry meaning:

- **Perspective is not the same as exclusion.** `followerCtx` resolves a
  follower's leader with the *ego* excluded — skip is the ego, but the branch
  to look down is the follower's. That call is the reason `as` is a separate
  parameter rather than a reuse of `skip`; reading the ego's route there
  would judge a follower by where a different vehicle is going.
- **A held turn steers the first hop only.** `boundaries()` spends it on the
  crossing, so the probe zeroes it after one hop, exactly as `exitBlocked`'s
  probe does.

With one successor `pickSuccessor` is a no-op, so ring and corridor networks
are bit-identical and every M1–M3 CRC fixture still passes untouched.

### Consequences

Good:

- A vehicle approaching a diverge brakes for the queue it is actually going
  to meet, from up to `maxSightM` away instead of from 19 m.
- Routing is now consistently applied across all four places that walk
  successors: measurement, junction entry, car-following, and the crossing
  itself. Audited after the fix: the only remaining `Successors[0]` reads in
  the kernel are `pickSuccessor`'s own fallbacks, the pre-probe default inside
  the walk, and `metrics.go`'s horizon-overshoot carry — and that last one is
  already guarded (`len(Successors) == 1`, else the carry is dropped and
  counted as `droppedCrossings`). Nothing else was reading the default branch
  where the routed one was meant.

Costs, stated plainly:

- **This changes simulation output**, on top of ADR-0031 the same day. Every
  Chicago figure must be re-measured, not adjusted.
- `pickSuccessor` → `routeNextHop` now runs per hop of the walk rather than
  only at crossings. The route table is cached per destination
  (`e.routeTabs`), so a hop costs a map lookup plus a bounded successor scan,
  and the walk only continues while lanes ahead are EMPTY — in congestion it
  terminates at the first hop. Free flow is where it walks, and free flow is
  where it matters.
- One `Vehicle` copy per walk that reaches a multi-successor lane (the probe).
  Built lazily, only when the walk is entered.

## Tests

`engine/divergelookahead_test.go` — one approach splitting to two branches,
the routed one holding a standing queue against an `EndWall`, the default one
empty to a long exit. The queue is at the wall so it *stays* standing: parked
on an exit lane, IDM accelerates it away and there is nothing left to brake
for by the time the ego arrives.

- `TestLeaderLookaheadFollowsTheRoutedBranch` — the ego resolves the queue on
  its own branch while still on the approach, and completes the run with zero
  collision observations. **Verified to fail without the fix.**
- `TestLeaderLookaheadHonoursHeldTurn` — a held turn of −1 steers the walk to
  the last successor. **Verified to fail without the fix.**
- `TestLeaderLookaheadDefaultsWithoutRoute` — the control: no route and no
  held turn still resolves down `Successors[0]`, pinning the behaviour of
  unrouted vehicles and single-successor networks. Passes both with and
  without the fix, which is the point of it.

## Recordings made before this change

Raised in review (2026-07-29) and worth stating once for ADR-0031/0032/0033
together: all three change trajectories **unconditionally**, so any recording
whose run exercised a diverge or a permissive link will fail CRC verification
when replayed under a binary that has them. The replay player reports this as
a CRC divergence per tick rather than refusing to open, so it is loud.

The position taken: **pre-change recordings are expendable, not pinned.** They
were made to look at, they are cheap to regenerate from a scenario plus a
seed, and none of them backs a published result. Re-record rather than keep an
old binary around. The escape (ADR-0034) is the one that is safe across the
boundary in the other direction — an old registry spec unmarshals
`StrandAfterS` as 0, so the escape stays off and the run is unchanged.

## Not done here

**`prevFollower` walks `Prevs` the same way.** The backward mirror has the
same shape, but not the same defect: who is behind you at a merge is
genuinely ambiguous — any predecessor can feed the lane — where the branch
ahead of you is a fact your route already determines. Left alone deliberately.

**Sight distance at a diverge is still `maxSightM` = 100 m.** The fix aims
the lookahead at the right road; it does not lengthen it. A queue that forms
more than 100 m past a junction while a vehicle is closing on it at 20 m/s is
still discoverable late. No evidence yet that this bites — noted so the next
investigation does not have to rediscover the bound.
