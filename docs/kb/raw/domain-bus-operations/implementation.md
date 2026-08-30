# Implementation: Bus Operations (buses in mixed traffic and dedicated lanes, Chicago/CTA focus)

> Source: codebase tracing | Researched: 2026-08-24 | Git HEAD: 2bc98de

Scope note: buses interact fully with road traffic — same lanes, same junction
guardrails, same signals. Grade-separated rail (own guideway, block signaling,
multi-car trains) is the sibling topic `domain-rail-operations`; this file only
touches rail where a design surface is shared (per-lane class masks, dwell).

There is no bus, transit, stop, or passenger concept anywhere in the engine
today. Everything below is therefore a *seam analysis*: where each bus
capability would attach, what already exists to build on, and what breaks if
you naively add it.

---

## 1. The vehicle model: multi-class types, and what a `bus` class needs

### 1.1 The type struct and the two existing classes

**Source files:**
- `engine/vehicle.go:8-17` — `VehicleType` struct: `Name, Length, Width, S0, T, A, B, V0`
- `engine/vehicle.go:33` — `Car` (5 m, 2 m wide, s0=2, T=1.6, a=0.73, b=1.67, v0=33.3 m/s)
- `engine/vehicle.go:40` — `Truck` (12 m, 2.5 m wide, s0=3, T=1.7, a=0.7, b=1.67, v0=80 km/h)
- `docs/kb/decisions/ADR-0007-vehicle-model.md` §3 — "Multi-class vehicles are first-class"

`Truck` at `engine/vehicle.go:35-40` is the precedent to copy: a second class
declared as a package-level `var`, with a comment noting it is "not part of
the default spawn mix". A bus class is one more such `var` — plausibly
`Length: 12` (40-ft CTA standard) or `18` (60-ft articulated), `Width: 2.55`,
a lower `A` (loaded transit buses accelerate ~1 m/s² or less, well under the
car's 0.73), `T` slightly longer, `V0` irrelevant on urban streets because
`v0eff` (`engine/vehicle.go:115-121`) caps desired speed at the lane limit
anyway. Nothing in the struct is bus-specific; the class mechanism is fully
general.

**Analysis (WHY):** ADR-0007 §3 pins the multi-class contract: types "mix
freely in one lane", and the bumper-to-bumper gap convention makes mixed
lengths unambiguous — every pair computes its gap from the *leader's* length
(e.g. `updateStats` at `engine/engine.go:1018` subtracts
`l.vehs[i+1].Type.Length`). An 18 m articulated bus behind a 5 m car is
already well-formed physics. `idmAccel` (`engine/vehicle.go:150-168`) reads
all dynamics from the type, so a bus class gets plausible car-following for
free, including the `emergencyDecel = 9.0` cap (`engine/vehicle.go:44`) that
bounds every braking path.

**Discrepancy worth flagging:** ADR-0007 §3 says a type carries "(`s0`, `T`,
`a`, `b`, MOBIL set)" — but `VehicleType` has **no MOBIL fields**. The MOBIL
parameters (`BSafe`, `Politeness`, `LCThreshold`) are engine-global in
`Params` (`engine/engine.go:15-17`, defaults at `:68-70`). A bus that should
change lanes less aggressively (higher politeness, higher threshold) cannot
express that per-type today; it would need either a struct extension
(contract-visible via the type registry) or controller-side policy. The
kernel's own reference MOBIL only runs under the `idm` harness policy anyway
(`engine/mobil.go:21-51`); in live runs lateral behavior is the external
driver's.

### 1.2 Where types enter a run (the registry seam)

**Source files:**
- `engine/cmd/serve/main.go:154` — production registry: `typeReg := map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}`
- `engine/cmd/simrun/main.go:66` — same literal registry in the batch runner
- `engine/cmd/demosrv/params.go:27-28` — demosrv *duplicates* the registry ("mirrors serve's")
- `engine/scenario/scenario.go:404-417` — `RunSpec(typeReg)` maps manifest `types:` names to structs; unknown names are a load error
- `engine/scenario/scenario.go:122-144` — demand `Flow`: `VTypes map[string]float64` (:127) is the per-origin type mix
- `engine/natsio/demand/director.go:455` + `:468-473` — the runtime demand director draws each vehicle's type (`pickType`) from the flow's weighted vtypes over sorted keys
- `engine/spawn.go:250-284` — the deterministic Spawner's own type draw (side stream, `TypeWeights` or uniform), assigning `v.Type, v.TypeIdx` at :284

**Analysis:** "Add a bus class" is mechanically a **three-file registry
change** (`serve`, `simrun`, `demosrv`) plus the `var Bus` in
`engine/vehicle.go` — the scenario format already names types by string and
validates them at load, and demand flows already mix types by weight, so a
bus flow is `vtypes: {bus: 1.0}` with no schema change. The duplication of
the registry across three binaries is the sharp edge: demosrv's comment at
`engine/cmd/demosrv/params.go:6-9` says it deliberately re-declares the
registry rather than sharing a package, so a fourth class added to serve but
not demosrv would make the public demo server reject bus scenarios with
"unknown vehicle type" — a silent-fidelity failure mode this project
explicitly hunts (KB `silent-fidelity-failures`). Note also `TypeIdx`
(`engine/vehicle.go:51`) is the canonical index into the scenario type list
and is what the CRC and the wire both use.

### 1.3 Buses are already identifiable on the wire

**Source files:**
- `engine/natsio/frame.go:130` — TSSF snapshot vehicle record field `Class float32`
- `engine/natsio/frame_test.go:49-50` — pins `Class == float32(TypeIdx)`

**Analysis:** The live snapshot carries the vehicle's class index. This is
what `engine/natsio/sigctl/sigctl.go:9` lists as the snapshot payload
("(id, x, y, angle, class)"). Any external controller — a bus fleet manager,
a TSP controller — can already distinguish buses from cars on the live plane
with zero contract change. The snapshot deliberately does *not* carry lane or
s (`sigctl.go:9-16`), which is why the sigctl detector pattern works in
(x, y) space; a bus controller needs the same treatment or richer
observations (the per-vehicle PolicyCtx observation path, §4.2 below).

---

## 2. Dwell mechanics: how a bus would hold at a stop

Nothing in the kernel knows what a stop is. Two attachment patterns exist,
both already replay-safe; a third (kernel-side stop infrastructure) would be
the SUMO-style route and is **not** how this architecture is shaped.

### 2.1 Option A — external controller intents (the ADR-0008-native way)

**Source files:**
- `engine/intent.go:37-51` — the 4-axis intent; `SpeedSetpoint` at :42-43: "cruise setpoint (m/s), persistent until replaced; a negative value clears it. Clamped to [0, v0eff] at application"
- `engine/vehicle.go:65` — `Cruise` / `CruiseOK` persistent controller state (keyframe-restored, per the comment at :60-64)
- `engine/vehicle.go:125-134` — `cruiseAccel`: servo reaches the setpoint within one tick, clamped to `[−emergencyDecel, Type.A]`
- `engine/intent.go:58-63` — grant levels (`GrantDrive` 1, `GrantSignal` 2, `GrantDirector` 3)

**Analysis:** `SpeedSetpoint: 0` is a persistent, legal, replay-recorded
"hold here" command: 0 is inside the `[0, v0eff]` clamp, and per-axis
persistence means the bus stays held until the controller issues a new
setpoint — no per-tick renewal traffic (contrast `Accel`, which is one-shot
per tick, `intent.go:40-41`). Intents are the recorded plane, so **replay
re-applies the dwell bit-exactly and the bus controller never re-runs**
(ADR-0008/ADR-0005 model) — the same property that makes this the natural
home for schedule/headway holding control: the hold *decision* is controller
policy, the hold *mechanics* is one recorded intent.

Two caveats a bus controller must own:
1. **Approach trajectory.** `cruiseAccel` brakes at up to
   `−emergencyDecel` (9 m/s²) to hit the setpoint within a tick
   (`engine/vehicle.go:130-132`). Issuing setpoint 0 at 12 m/s is a
   physics-legal but passenger-hostile slam; a bus agent must ramp the
   setpoint down into the stop (or use `Accel` overrides), and stop *at* the
   berth coordinate, since the kernel has no stop line to aim it.
2. **Failover semantics.** ADR-0008's fleet failover returns claimed
   vehicles to the default driver on controller disconnect. The default
   driver does not know the stop pattern — a failed bus controller turns
   every bus into an ordinary car that skips its stops. That is a loud
   behavioral discontinuity (good: visible), but it belongs in the design.

### 2.2 Option B — a director verb (the ADR-0037/ADR-0012 precedent)

**Source files:**
- `engine/director.go:131-151` — `SpawnDirective`, the kernel form of the spawn verb (precedent for verb-carried payload: origin, type, earliest tick, `Destination`, `OffsetM`)
- `engine/sigctl.go:88-103` — `SignalDirective` (`Signal`, `Phase`, `HoldTicks`; "0 asks for the default")
- `engine/sigctl.go:158-180` — `EnqueueSignal`: validate, resolve to network index, buffer for next tick boundary
- `engine/natsio/server.go:146-153` — verb subject `ts.{run}.ctl.verb.{controller_id}`, request/reply, director grant required
- `engine/natsio/server.go:190-195` — record subject `ts.{run}.log.verb` ("accepted spawn and signal_set (ADR-0037) directives")
- `engine/natsio/contract.go:752-760` — `handleVerb`: the shared wire checkpoint (validation, idempotency, enqueue)
- `engine/natsio/demand/director.go:345` — the demand director marshaling a `VerbRequest`

**Analysis:** The verb machinery gives idempotency (request_id), applied_tick
stamping, record-plane logging, and verbatim replay re-enqueue for free —
the four properties ADR-0037's design sketch enumerated and reused. A
hypothetical `dwell`/`hold_vehicle` verb would ride the same channel, but it
would be *kernel behavior* (the kernel holding a vehicle is driving logic,
which ADR-0008 bans: "zero driving logic in the engine") unless phrased as a
pure state flag the kernel enforces without deciding. The signal_set design
dodged this cleanly: the verb commands a *network* act (a phase), not a
vehicle act. A dwell verb commands a vehicle act, so Option A is the
architecturally clean path; the verb channel is the right home only for
fleet-level acts (e.g. injecting a bus — already covered by the spawn verb,
whose `TypeName` at `engine/director.go:134` resolves against the scenario
type list and whose `Destination`/`OffsetM` at :136-150 already support
route assignment and mid-block insertion).

### 2.3 The strand escape: false-stranding risk on a dwelling bus (ADR-0034)

**Source files:**
- `engine/gridlock.go:71-74` — `stuckSpeed = 0.1` m/s
- `engine/gridlock.go:81-143` — `strandStuck()`: per tick, `v.stuckTicks++` below stuckSpeed (:109-113); at `limit = round(StrandAfterS/Dt)` (:103) the vehicle is tested for stranding
- `engine/gridlock.go:145-207` — `jammedAtJunction`: the discriminator. Head of lane (`a[len(a)-1] != v` at :173) AND (entry arm: routed successor is internal AND `boxWalk` returns `blocked && !holdSeal`, :179-182; in-box arm: `exitWalk` `blocked && !holdSeal`, :205-206)
- `engine/gridlock.go:166-171` — "A red light is deliberately NOT a trigger": the `holdSeal` out
- `engine/rightofway.go:261-288` — `boxBlocked`/`boxWalk`: foe occupancy in the box, then `exitWalk` room accumulation
- `engine/engine.go:41-50` + `:88` — `Params.StrandAfterS`, default 300 s (SUMO's value)
- `engine/engine.go:498-501` — the escape runs once per tick, after lane changes, before metrics
- `engine/vehicle.go:91-100` — `stuckTicks` is keyframed (format v5) with an explicit do-not-derive warning
- `engine/gridlock.go:209-244` — `resetStuckBehind`: after a strand, timers on the lane and up to `maxLaneHops` of feeders reset

**Analysis — exactly when a dwelling bus strands:**

The stuck timer accumulates on *any* vehicle below 0.1 m/s, regardless of
*why* it is stopped — the kernel cannot tell a cruise-setpoint dwell from a
gridlock freeze. The escape's entire safety case is the `jammedAtJunction`
discriminator, so walk a dwelling bus through it:

- **Ordinary dwell (5–60 s): safe with 5× margin.** `StrandAfterS = 300 s`
  default vs. a CTA stop dwell of seconds to ~1 minute: the timer never gets
  near the 3000-tick limit. Even a long dwell resets on any motion ≥ 0.1 m/s
  (`gridlock.go:109-111`).
- **Head-of-lane matters, not position.** `jammedAtJunction` requires the
  bus to be the head of its lane (`gridlock.go:173`) — the furthest-ahead
  vehicle. A bus dwelling mid-block with cars queued *behind* it is the head
  of lane; with anyone ahead, it is ineligible. But then the second
  condition still must fire: the box the bus is routed into must be
  capacity-blocked (`blocked && !holdSeal`). Note the test is
  **position-independent once head-of-lane**: `boxWalk(v, next)` examines
  the junction box and its exit chain, not the bus's distance to it. A bus
  holding at a stop 300 m upstream of a genuinely gridlocked box is as
  eligible as one stopped at the stop line.
- **The false-stranding scenario is a terminus layover under gridlock.**
  CTA practice schedules end-of-line layovers of 5–15 minutes; 300 s = 5
  minutes. A bus laying over >300 s, as head of its lane, whose routed
  successor box is capacity-blocked (not merely red-held — `holdSeal`
  excludes signals) satisfies every condition and is removed as STRANDED.
  Its trip record is emitted incomplete and flagged
  (`engine/metrics.go:147-153`), so the failure is *loud*, but the bus and
  its line are gone from the run.
- **Mitigation surface is small.** Options: (a) keep modeled dwells under
  300 s and end lines with a despawn instead of a layover (ADR-0021's
  arrival-despawn already ends trips at the destination lane —
  `engine/director.go:136-141` says the kernel "ENDS the trip there"); (b)
  controller wiggles (any motion ≥ 0.1 m/s resets the clock — a hack); (c)
  a dwell marker the escape honors — but that is kernel knowledge of stops
  again. Option (a) is free today.

**Second-order effect — dwell poisons adaptive-routing travel times.** Every
lane departure folds a dwell sample into the lane's `ttEMA`
(`engine/engine.go:531-545`, `noteLaneLeave`): a bus that sat 45 s at an
in-lane stop feeds a 45 s sample (α = 1/8), capped at `StrandAfterS` and
floored at free flow. Under ADR-0036 adaptive routing (default ON since
2026-07-31) that lane looks congested and cars divert around it. Arguably
correct — an in-lane stop *is* a periodic capacity loss — but a high-frequency
bus line will imprint a systematic travel-time bias that has nothing to do
with car congestion. Worth a measurement bracket before bus scenarios are
trusted A/B.

---

## 3. Bus lanes: class masks and where they would be consulted

### 3.1 What the import pipeline does today

**Source files:**
- `engine/netimport/netimport.go:80-88` — `motorClasses`: the SUMO vehicle classes the engine can carry — includes `"bus"`, `"coach"`, etc.
- `engine/netimport/netimport.go:115-124` — `xmlLane` *parses* `Allow` and `Disallow`
- `engine/netimport/netimport.go:597-624` — `motorLane`: whitelist/blacklist evaluation — "reports whether a lane is open to **any** motor vehicle class"
- `engine/netimport/netimport.go:196-259` — application: edges whose lanes are all non-motor are skipped wholesale (:198-203); individual non-motor lanes are dropped (:207-210); both counted in the import report
- `engine/netimport/netimport.go:236-247` — the emitted `NetLane` literal: **no allow/disallow/class field is carried**
- `engine/netfile.go:48-79` — `NetLane` schema (v1): id, section, edge, geometry, successors, origin/exit/endWall, junction row fields, signal binding. No class mask.
- `engine/network.go:9-67` — `Lane`: same absence. Nothing class-aware anywhere in the runtime graph.

**Analysis:** The filter is **existential, not descriptive**: a lane survives
if *any* motor class may use it, and the class detail is then discarded.
Consequences for buses:

1. **A bus-only lane compiles as an ordinary lane.** `allow="bus"` passes
   `motorLane` (bus ∈ motorClasses, `:84`), the lane is kept, and every
   vehicle — cars included — may drive, route, and lane-change onto it.
   Chicago's Loop Link dedicated lanes and any `bus:lanes` tagging in OSM
   are invisible in the compiled network.
2. **The information was in hand at import and dropped.** `xmlLane` parsed
   the exact strings needed (`:121-122`); the `NetLane` construction site at
   `:236` is where a mask field would be emitted.
3. **There is a format-extension precedent.** `NetLane` grew optional
   extensions twice already — junction row fields (`netfile.go:65-71`) and
   the signal binding `TL`/`TLLink` (`netfile.go:73-78`) — each "absent
   means pre-extension semantics". A `classes`/`allow` field can follow that
   pattern without a format-version bump, with absent = open-to-all (the
   current meaning). The identity-hash question (does the mask join the
   importer identity hash like ADR-0022's typemap) is an ADR-level decision.

### 3.2 Seam points where a per-lane class mask must be consulted

Enumerated exhaustively, in decision-path order. Missing any one of these is
a *silent* leak of cars into bus lanes (or buses onto rail), exactly the
failure class the KB's silent-fidelity catalog exists for:

1. **Next-hop routing tables** — `engine/routing.go:204` (`routeTable`),
   `:373` (`routeDijkstra`). Tables are per-destination today, shared by all
   classes. Class-masked lanes require either per-(class,destination) tables
   or a mask filter in the relaxation loop at `:390-398`. Cache keying,
   epoch recomputation (ADR-0036, `:286-361`), and the memoization
   discipline ("derived state, never serialized", `:18-20`) all multiply by
   class count. A car must never receive a next hop onto a bus-only lane.
2. **Lateral-depth tables** — `engine/routing.go:117` (`routeLatDepth`). The
   0-1 BFS at `:129-154` treats `l.Left`/`l.Right` as universally
   traversable; a hop into a bus-only lane costs 1 for a car just like any
   other. The lateral gradient would route cars *toward* bus lanes when that
   shortens the route. The lateral step at `:145-153` needs class-filtered
   neighbors.
3. **Route guardrail** — `engine/mobil.go:76-93` (`routeHopOK`): denies hops
   that increase lateral route depth. Class-blind today; it validates the
   *route* axis only, not lane admissibility.
4. **Route recovery** — `engine/mobil.go:109-145` (`tryRouteRecovery`):
   actively walks an off-route vehicle down the depth gradient — could
   recover it *into* a bus lane.
5. **Reference MOBIL** — `engine/mobil.go:151-156` (`tryLaneChange`) →
   `engine/policy.go:180` (`DecideLaneChange`). The lateral candidate
   context is gathered at `engine/policy.go:359` (and the `SideCtx` at
   `policy.go:55-62` carries `Present`, limits, leader/follower — no
   admissibility bit). A class-closed lane should surface as
   `Present = false` at gather time so every downstream decision (kernel
   reference policy *and* the external default driver, which dogfoods the
   same PolicyCtx observations — `policy.go:5-11`) inherits the constraint
   with zero client change. This is the highest-leverage single seam.
6. **Commanded hops** — `tryForcedLaneChange` (`engine/intent.go:238-242`;
   call site `engine/mobil.go:36-38` inside `laneChanges`) via
   `ForcedFeasible` (`engine/policy.go:276-292`): the forced-command
   gate chain checks kinematic feasibility (`kinGapOK`,
   `engine/mobil.go:197-202`) but not lane permissions. ADR-0008's clamping
   doctrine ("the engine enforces physics limits regardless of commands",
   `intent.go:14-16`) argues a class mask should clamp commanded hops too —
   the same shape as the route guardrail's "caps every control path"
   (`mobil.go:68-72`).
7. **Junction successor choice** — `engine/engine.go:976-997`
   (`pickSuccessor`): `routeNextHop` feeds it (:991-994); if tables are
   class-aware this seam follows, but the `HeldTurn` overrides (:986-989)
   and the `Successors[0]` default (:996) are mask-blind fallbacks — a
   default hop onto a bus-only internal lane is possible if the masks are
   applied only in the tables.
8. **Box/exit room accumulation** — `engine/rightofway.go:274-330`
   (`boxWalk`/`exitWalk`): the exit walk accumulates free room through empty
   successors (`free += exit.Length` at `:400`; the tail-room term is at
   `:374`). Room on a lane the vehicle's class cannot use
   must not count as clearable space, or a car will be released into a box
   whose only exit is a bus-only lane.
9. **Spawn clearance** — `engine/spawn.go:142` (`injectionPlan`),
   `engine/director.go:164-200` (`EnqueueSpawn` validation): origin lanes
   validate as network origins; nothing checks whether the *type* being
   spawned may use the *lane*. A car flow bound to a bus-only portal lane
   (or a bus flow onto a lane it is barred from) should fail at enqueue, the
   same loud-validation idiom as unknown origin/type (`director.go:177-200`).
10. **Car-following** — no change needed: `leaderAt`
    (`engine/engine.go:589`, `:614`) follows occupancy, and a bus ahead is
    just a slower, longer leader. Mixed-class following already works.
11. **Metrics/metrics windows** — per-lane accumulators
    (`engine/metrics.go:288-295`) are class-agnostic; see §5 for the
    occupancy attribute. No mask interaction.

**Shared with rail:** items 1–9 are precisely the seams a rail class mask
would need (rail lanes are currently *dropped* at import —
`netimport.go:80-88` excludes rail from `motorClasses` — so rail needs the
mask *and* the lanes). Design the mask once for both topics.

---

## 4. TSP: extending the ADR-0037 runtime-signal-control pattern

### 4.1 The existing machine

**Source files:**
- `docs/kb/decisions/ADR-0037-runtime-signal-control.md` — design + 11 review-round addenda
- `engine/sigctl.go:63-72` — `SignalHoldMaxSeconds = 300` (the StrandAfterS horizon), clamped not rejected
- `engine/sigctl.go:115-136` — `sigOverride` (`phase/since/until/chainStart`); the cumulative chain bound against starvation-by-renewal
- `engine/signal.go:192-212` — `sigPhaseAt`: the **one** derivation point where commanded control enters; empty table ⇒ byte-identical pre-ADR-0037 behavior
- `engine/signal.go:399-454` — `sigGate`: enforcement reads state through `sigState`; box checks compose ("green never means enter a box you cannot exit", `:447-452`)
- `engine/natsio/sigctl/sigctl.go:9-16` — virtual-detector rationale: snapshot has (x, y) only, so "approach presence is read the way a physical detector reads it: a fixed zone in space"
- `engine/natsio/sigctl/sigctl.go:160-223` — `Detector` (stop-line point + approach-side direction) and `LoadGeom` (geometry from the static network file; dynamic structure over the wire)
- `engine/natsio/sigctl/sigctl.go:82-158` — Config: cadence 20 ticks, hold 100, renew-below 30, min-green 100, max-green-on-call 200, detector radius 25 m
- `engine/natsio/sigctl/sigctl.go:225-240` — `progState`: self-tracked command history predicting the kernel's chain bound (the documented feedback gap workaround)
- `engine/cmd/serve/main.go:98-99` — `-sigctl` in-process embed

**Analysis:** Every TSP primitive maps onto something already built:

- **Approaching-bus detection** = the virtual-detector pattern with a class
  filter. `LoadGeom` already derives approach-side stop-line zones; the TSSF
  snapshot already carries `Class` (`frame.go:130`). A TSP controller is the
  sigctl spatial binning (50 m grid, `sigctl.go:686`, `:744-745`) plus
  `class == bus` and a distance-to-stop-line ETA instead of mere presence.
- **Green extension** = what the gap-out controller already does: renew the
  serving phase's hold when the bus's ETA lands just after phase end
  (`signal_set` with a bounded hold; renewals chain under the 300 s rail).
- **Red truncation (early green)** = the controller's phase-switch walk
  (sigctl.go package doc, "walks the program's table order … commanding
  every intermediate phase at its NATURAL duration"): command the walk to
  the bus's phase early. The clearance transitions are actually simulated,
  so truncation costs the same lost time it costs in reality.
- **Conditional vs unconditional priority is pure client policy.** Chicago's
  deployed TSP is explicitly conditional — "hold green lights longer if the
  buses are running late" (CTA FY17 budget book; see
  standards-and-patterns.md). Conditionality (late-only, load-only,
  headway-deviation-only) is a predicate over the bus controller's own
  schedule state; the kernel never needs to know.
- **The starvation rails are TSP's safety case for free.** Cross-street
  starvation by aggressive priority is bounded by the same 300 s cumulative
  chain bound (`sigctl.go:63-86`), and lapses are logged events
  (`SigLapse`, `sigctl.go:138-156`) — an auditable record of exactly when
  priority overran its bound.
- **Replay is already solved.** Signal verbs are recorded with effective
  holds and re-enqueued verbatim (ADR-0037 addenda); a TSP run replays
  bit-exactly with the controller absent, the property no commercial tool
  offers.

**Gaps a TSP controller would hit (documented, not new):** the M2 feedback
gap — no live-plane echo of held phases or declined renewals, so the
controller self-tracks (exact only as the program's sole commander); the
VerbReply applied-tick/effective-hold echo is a recorded contract proposal
(ADR-0037 M2 round-1 note F). Multi-controller programs (TSP + actuated on
one junction) break the self-tracking assumption — a real contract question
for the design milestone.

### 4.2 What a bus *agent* needs beyond TSP

A full bus line also needs driving observations: the PolicyCtx observation
path (`engine/policy.go:64-84`) ships the same context the reference driver
uses — leader, lateral candidates, limits — per claimed vehicle. A bus
controller claiming its fleet gets driving for free (same dogfood), adds
stop-approach braking, cruise-0 dwell, and door-time logic on top. The only
kernel-visible artifact of all of it is recorded intents.

---

## 5. Metrics: where person-delay would ride

**Source files:**
- `engine/metrics.go:135-154` — `TripRecord`: already carries `TypeName` (:138), origin/dest lanes, entry/exit ticks, distance, `TimeLossS`, stops, stopped time, `Completed`, `Stranded`
- `engine/metrics.go:276-286` — `tripState` (open accumulation): `typeName` at :277
- `engine/metrics.go:399` — trip state seeded with `typeName: v.Type.Name` on first observation
- `engine/metrics.go:1369-1386` — `emitTrip`: the single emission point
- `engine/metrics.go:94-124` — `IntervalRecord` (Edie q/k/u + occupancy/stops/time-loss groups)
- `engine/metrics.go:173-204` — `Totals` (VMT, VHT, time loss, denied entry, stranded)
- `docs/kb/decisions/ADR-0014-observability-metrics.md` — §3 pinned definitions; the PCU-conversion note (:202-204) already anticipates vehicle-class-dependent weighting

**Analysis:** The vehicle-class plumbing already exists end to end:
`TypeName` rides every trip record, so *vehicle* delay split by class is a
pure post-processing group-by today (bus time-loss vs car time-loss per
corridor — the exact quantity a bus-lane A/B arm needs). What is missing for
*person*-delay is one integer:

1. `Vehicle` gains an occupancy/persons field (kernel state — must be
   keyframed and CRC-relevant only if it feeds behavior; as pure cargo for
   metrics it follows the `stopDone` precedent of keyframed-not-CRC'd,
   `engine/vehicle.go:79-89` — but note the two pinned warnings there and at
   :91-100 about what must survive restore).
2. `tripState` (`metrics.go:276`) accumulates person-seconds /
   person-distance alongside vehicle units; `TripRecord` gains the fields at
   `emitTrip` (`:1370-1385`) with a schema-version bump (the file already
   stamps `SchemaVersion: 1`, `:1372`).
3. `IntervalRecord` person-throughput per lane (q × mean occupancy by class)
   answers "did the bus lane move more *people*", the number that justifies
   bus lanes politically (NACTO framing; see standards-and-patterns.md).
4. Contract surface: the JSON sink (`engine/metricsjson.go`) and the NATS
   metric publisher are ADR-0014 §6 consumers — a schema addition there is a
   contract change needing an ADR per the project's message-contract rule.

Occupancy *source* is the bus controller's business (boarding model,
GTFS-anchored loads, or scenario constant); the kernel just transports it.
That keeps the person model out of the CRC'd world state entirely.

---

## 6. Non-obvious interactions and edge cases

- **In-lane stop blocking is emergent and correct.** A dwelling bus is a
  stopped 12–18 m vehicle; cars behind queue or change lanes via ordinary
  MOBIL/lateral intents. That *is* the HCM/TCQSM on-line-stop capacity
  effect (standards-and-patterns.md §4) with no special-case code — the
  simulation gets bus-stop-as-bottleneck for free the day dwell exists.
- **Stop position vs. junction internals.** A stop near a junction sits on
  an ordinary lane; junction-internal lanes are movement-specific
  (`gridlock.go:199-204` notes netimport internals are single-successor) —
  a far-side stop must be modeled on the first ordinary lane past the box,
  and the bus agent must treat the box crossing and the dwell as separate
  acts (the box checks at `sigGate`/`rowGate` gate the crossing, the cruise
  axis owns the dwell).
- **`routeLatDepth` counted bus lanes as recovery hops** (see §3.2): on
  chi-loop-scale networks with `bus:lanes`, class-blind masks would not just
  let cars cheat — they would *attract* route recovery into bus lanes.
- **Bus bunching needs ≥2 buses per line** — trivially a demand `Flow` with
  `vtypes: {bus: 1}` and a period; the flowSampler's exponential-gap option
  (`engine/natsio/demand/director.go:451-453`) already models headway
  noise at dispatch. Bunching then *emerges* from dwell variance at stops —
  see standards-and-patterns.md §1 for why equal headways are unstable.
- **dt interactions are already handled:** any hold/dwell bound must be
  dt-compiled, following `signalHoldMaxTicks()` (`engine/sigctl.go:80-86`)
  and the strand limit's rounded division (`gridlock.go:86-103`) — never
  hard-code tick counts.

## Open questions (code-level)

1. Should the class mask be a bitmask over a *fixed* class enum (SUMO's
   road-motor-vehicle class vocabulary — ~18 of its ~31 vClasses, of which
   we import 17) or over scenario type
   indices? The former is network content; the latter mixes scenario
   identity into the network hash.
2. Does the mask join the importer identity hash (ADR-0022 precedent says
   yes for region-scoped import decisions)?
3. Per-type MOBIL: extend `VehicleType` (contract-visible) or leave lateral
   temperament to the bus controller?
4. Terminus modeling: ADR-0021 arrival-despawn and re-spawn for the return
   trip, or a persistent vehicle with a layover (which re-enters the §2.3
   strand question)?
5. Is the `ttEMA` dwell-sample bias from scheduled dwells acceptable as
   "the stop really costs the lane capacity", or should directed dwells be
   excluded from the routing signal?
