# Implementation: Rail Operations

> Source: codebase tracing | Researched: 2026-08-24 | Git HEAD: 2bc98de
> Trace of every surface a CTA 'L'-style rail layer would touch, with the WHY.
> Uncommitted ADR-0039 (perimeter demand metering) work is in the tree; it does
> not intersect rail except where noted (spawn gating counts).

## 0. TL;DR of the trace

Rail dies in **three** places before it ever reaches the engine today:

1. `scripts/chicago/extract.py:52-55` — the zone extract keeps only ways with a
   `highway` tag. Railway ways are dropped at extract time; the .osm the rest
   of the pipeline sees contains no rail at all.
2. `scripts/overpass-lean.py:43` — the Overpass lean filter likewise keeps only
   `highway`-tagged ways (`e.get("tags", {}).get("highway") in keep`).
3. `engine/netimport/netimport.go:83-88` — even if rail edges arrived in a
   .net.xml, `motorClasses` excludes `rail`, so `motorLane` (netimport.go:599-624)
   rejects rail lanes and `Convert` files them under `SkippedEdges`/
   `SkippedLanes` (netimport.go:198-209, report fields at netimport.go:45-46).

Measured on the shipped networks: **zero** rail lanes exist in any
`data/networks/*/*.net.xml` (grep `allow="rail"` → 0 hits across chi-loop,
chi-loop-urban, chi-kennedy, chi-north-lakefront, and 20+ others) — kills #1/#2
fire first, so #3 is currently exercised only by the test fixture
(`engine/netimport/testdata/corridor.net.xml:25-27` carries edge `R0` with
`allow="rail"`, and `engine/netimport/netimport_test.go:41` asserts it is
skipped: "rail edge and sidewalk lane skipped"). The chi-loop/chi-loop-urban
import reports show 5 skipped edges each — verified bicycle-only
`highway.service` stubs (`allow="bicycle"`, e.g. edge 421091626 in
loop-urban.net.xml), no rail, because rail never reached the importer.

The netconvert invocation itself (`scripts/import-city.sh:70-71`) is
`netconvert --osm-files ... --proj.utm --no-turnarounds` plus the ADR-0022
typemap — no `--railway.*` options, no rail typemap. netconvert's default OSM
typemap *does* know railway=rail/subway/tram/light_rail (see
standards-and-patterns.md §1), so feeding it rail-bearing .osm would emit
rail edges with `allow="rail"` (electrified: also `rail_electric rail_fast`)
without any flag changes — the pipeline kill is our extract filter and our
importer's class list, not netconvert.

## 1. The long-vehicle spike: every place vehicle length meets lane length

The topic's #1 technical question. The model: `Vehicle.S` is the **front-bumper**
coordinate on exactly one lane (`engine/vehicle.go:46-58`); the vehicle's extent
is implicit (`S − Type.Length` rear), and per-lane occupancy `lane.vehs`
(`engine/network.go:55`) contains the vehicle only on its front-bumper lane
(`rebuildOccupancy`, engine.go:549-565). A ~117 m 8-car consist (CTA car =
48 ft = 14.6 m — see standards-and-patterns.md §6) is 23× the `Car.Length` of
5 m (vehicle.go:33) and longer than many imported lane segments. Enumerated
interactions, each with what breaks and what already works:

### 1.1 Car-following gap is length-correct — but sight-bounded at 100 m

- `leaderAt` computes bumper-to-bumper gap with the leader's full length, same
  lane (engine.go:622: `l.S - l.Type.Length - s`) and across lane chains
  (engine.go:658: `dist + l.S - l.Type.Length`). A follower sees the train's
  true rear even through several short lanes. ✔
- **But** the leader walk is bounded: `baseLaneHops = 4`, `maxLaneHops = 12`,
  `maxSightM = 100.0` (engine.go:575-577), calibrated in the comment at
  engine.go:567-574 to car braking (72 m emergency stop from 36 m/s "plus
  margin"). The walk stops when the accumulated distance to the candidate lane
  exceeds 100 m (past the 4 guaranteed hops). A 117 m train's **rear** can be a
  collision-relevant 30 m ahead of a follower while its **front** sits 147 m
  away — outside sight. The follower then reads "no leader" and free-flows
  until the front enters sight, at which point the gap may already be small or
  negative. The same sight bound feeds `safetyGate` (engine.go:780-813 reads
  `e.leader(v)`), so the ADR-0025 guardrail inherits the blindness; it rescues
  the pair only at emergency decel (`SafetyDecel`, engine.go:26-39), not at
  service braking. **Spike finding L1: maxSightM silently assumes vehicle
  extent ≪ 100 m; a >100 m consist needs the sight bound raised (or
  length-aware: sight to leader front ≥ maxSightM + leader length) — or an
  external rail controller that never relies on kernel lookahead.**
- ADR-0032's routed-branch lookahead (engine.go:614-667) is length-neutral and
  works for trains as-is.

### 1.2 Boundary crossing moves the vehicle as a point

`boundaries()` (engine.go:891-974): a vehicle with `v.S > v.Lane.Length`
crosses, `v.S -= lane.Length` (engine.go:929). The implicit rear teleports off
the old lane the same tick the front lands on the new one. Consequences:

- Followers behind are unaffected (they see the train through the cross-
  boundary leader walk, §1.1). ✔
- The landing overlap check *is* length-aware: `rear := v.S - v.Type.Length`,
  then a sorted search of the successor's occupancy (engine.go:937-945) — a
  117 m vehicle landing at S=0.5 has rear −116.5 and any vehicle on the
  successor within that window is counted (`CrossOverlaps`). ✔ detection;
  placement itself is still "put at S regardless" (engine.go:931-936 comment).
- Chained hops per tick (descending-index re-check, engine.go:961-963) let a
  fast train cross several short stubs in one tick. At 24 m/s and dt=0.1 the
  train covers 2.4 m/tick, so netimport's 0.2–3.5 m junction stubs chain
  fine. ✔

### 1.3 Junction box occupancy is front-bumper-based; exit-room is length-aware

This is the sharpest junction interaction.

- `boxWalk` foe occupancy: `len(f.vehs) > 0` on each conflicting internal lane
  (rightofway.go:277-286). A train whose front bumper has left the internal
  lane is **no longer "in the box"** even though its rear physically occupies
  it for many seconds (117 m train, ~10–30 m box: the tail occupies the box
  ~4–8 s after the front exits). A conflicting movement released on that
  information would be routed into the train's tail. The overlap would be
  *observed* — `updateStats` measures cross-boundary pairs with leader length
  (engine.go:1020-1031) — so it is loud, not silent, but it is a modeled
  conflict violation. **Spike finding L2: the conflict model undercounts
  long-vehicle box occupancy at exit.**
- Compensating mechanism at *entry*: `exitWalk` requires
  `need := v.Type.Length + v.Type.S0` (rightofway.go:331) of free room past the
  box before entry is allowed — "don't enter a junction you cannot clear"
  measures the full consist. For a train, need ≈ 119 m. Two corollaries:
  - On rail this accidentally approximates **absolute block working**: a
    following train is held outside the junction until the leader's rear (via
    the tail rule, free += first.S − first.Type.Length, rightofway.go:374) is
    119 m past the box. Prototypical behavior from a road rule. ✔
  - The room walk accumulates through at most `maxLaneHops = 12` lanes and
    **stops at the first internal lane** (rightofway.go:346-406, internal stop
    at :377). Rail edges in OSM are long (station-to-station), so 119 m
    accumulates in 1–2 hops; the road-network stub pathology (0.2 m stubs ×
    12 hops ≈ 40 m max, the motivation at rightofway.go:291-294) does not
    apply to rail geometry. Low risk, but must be re-measured per import —
    **spike finding L3: verify no rail junction's exit chain fails the
    119 m rule structurally (e.g. terminal tail tracks, short pocket tracks),
    or the train is sealed at the interlocking forever** (blocked, holdSeal
    false — which is also the strand-escape trigger, §2).
- In-box discipline: once inside, the tail rule switches to "car-following
  owns it" (rightofway.go:367-370), so a train can stop in the box behind its
  own leader; while its front remains on the internal lane it still counts as
  box occupancy. ✔ until the front exits (then L2).

### 1.4 Spawn / injection clearance

- Portal injection (`injectionPlan`, spawn.go:142-168): entry speed cap
  `v² ≤ v_leader² + 2·B·(gap − s0)` against `leaderAt` from the lane start,
  plus the junction-hold wall (spawn.go:156-159). Nothing checks that the
  origin lane is at least one vehicle-length long; a 117 m train injected at
  S=0 hangs its implicit rear off the map edge at negative s. Harmless at a
  portal (no predecessors — nothing can rear-end it), but worth a comment when
  rail terminals are modeled as portal lanes shorter than a consist.
- Interior injection (ADR-0021) is explicitly length-safe: `rearClear`'s
  footprint guard (spawn.go:184-211, window at :195-199) denies materializing
  on top of anyone, and the follower comfort brake test mirrors the leader
  rule. ✔
- **Director directive expiry**: `DirectorSpawnHoldTicks = 600` (director.go:55)
  — a held spawn directive expires after 60 s of blocked origin. Rail headways
  are 180–480 s, so one blocked terminal slot can silently drop a scheduled
  train (`DirExpired` is counted, director.go — the demand-loss observability
  of engine.go:199-204). For scheduled rail the rail controller must re-issue
  or the window must widen.
- Both spawn paths draw the desired-speed factor F ∈ [0.8, 1.3]
  (spawn.go:285-290; director.go:381-385). A train should run F=1 (schedule,
  not driver heterogeneity); F only matters through `v0eff` (vehicle.go:115-121)
  at injection and the IDM fallback, since the rail controller drives by intent
  — but the IDM fallback path would systematically mis-speed trains by up to
  −20/+30%.

### 1.5 IDM and the control axes for a train

- `idmAccel` (vehicle.go:150-168) is class-parameterized and works with rail
  numbers (a ≈ 1.0 m/s², b ≈ 1.0–1.3 service, V0 ≈ 24 m/s for 55 mph);
  `emergencyDecel = 9.0` (vehicle.go:44) caps decel far above any rail value.
  As a **block approximation**, though, IDM equilibrium following (gap ≈
  s0 + vT; with car T=1.6 at 15 m/s ≈ 26 m) is far inside block spacing —
  two trains would follow at road-following distance unless the controller
  enforces separation. SUMO's answer is `carFollowModel="Rail"` (always keep
  safe-stop distance — competitors.md §1); our analog is the ADR-0025 gate
  with SafetyDecel = rail emergency decel as the *primary* spacing mechanism,
  plus controller-side block discipline. Documented-limit material.
- `cruiseAccel` (vehicle.go:125-134) reaches the setpoint **within one tick**,
  clamped to [−9, +A]: issuing Cruise=0 to a train at 20 m/s is an emergency
  brake application, not a station approach. A rail controller must shape
  braking with `reqAcc` intents or stepped setpoints. Operational note for the
  controller design, not a kernel change.
- Lateral: rail edges are single-lane, so `Left`/`Right` are nil and MOBIL
  no-ops (mobil.go:80-84, :120-135). Caveat: that holds only if the import
  follows SUMO's convention of parallel tracks as **distinct edges**
  (standards-and-patterns.md §1); if netconvert ever emits a multi-lane rail
  edge, a 117 m vehicle becomes eligible for instant lateral hops — physically
  absurd. A class-mask lane layer (decision candidate D2) should also gate
  lateral policy by class. Flagged as an open question for the import spike.

### 1.6 Metrics attribution is front-bumper

`addOccupancy(lane, dt*v.Type.Length, tick)` (engine/metrics.go:493, also
:512/:543) books the train's whole 117 m footprint onto its front-bumper lane:
lane-interval density/occupancy (Edie k) spikes on one lane while the
physically occupied neighbors read empty. Trip-level measures (travel time,
headway at a point) are unaffected. **Spike finding L4: decide whether to
distribute occupancy across spanned lanes or document front-bumper
attribution; matters if rail corridors feed the ADR-0014 metric kernel into
congestion comparisons.**

### 1.7 Vehicle types are code, not scenario authoring

A `train` type cannot be authored in YAML today: the type registry is
hardcoded `{"car", "truck"}` in three binaries — `engine/cmd/serve/main.go:154`,
`engine/cmd/demosrv/params.go:28`, `engine/cmd/simrun/main.go:66` — and the
scenario `types:` list resolves by name against it
(`engine/scenario/scenario.go:404-414`). Adding rail is a small code change
per binary (a `VehicleType` value per vehicle.go:8-17 plus registry entries),
but it *is* a code change, and director spawn verbs resolve `TypeIdx` against
the scenario list order (director.go:356, :380), which feeds the CRC
(`TypeIdx` in computeCRC, engine.go:1079) — so the type list is run identity.

## 2. ADR-0034 strand escape vs a dwelling train

The escape (`engine/gridlock.go`, doctrine at :36-69): stopped below
`stuckSpeed = 0.1` m/s (:74) for `StrandAfterS` (default 300 s, engine.go:88;
`limit` rounded at :103) **and** `jammedAtJunction` → removed, counted by
section. The discriminator (:172-207) has two arms, both requiring
head-of-lane (:173):

- **Entry arm** (:179-183): routed successor is internal → `boxWalk`; stranded
  only if `blocked && !holdSeal`.
- **In-box arm** (:184-206): v itself on an internal lane → `exitWalk`; same
  predicate.

Trace for a train holding at a station (controller-commanded stop, possibly
head-of-lane):

- **Station mid-lane, successor not internal** → `!lane.Internal` → return
  false (:184-185). **Never strands, regardless of dwell length.** Terminal
  layovers on plain track are safe. ✔
- **Station immediately before an interlocking, exit capacity-blocked**
  (another train within the 119 m exit-room rule of §1.3) → blocked, holdSeal
  false → jammed → **stranded after 300 s**. Tower 18/12 sit *between* Loop
  stations, so a train holding at a platform whose routed path crosses a
  capacity-blocked interlocking is exactly this case. 300 s is short in rail
  terms (schedule holds, terminal turns, disruption stacking). **This is the
  rail layer's most dangerous default: scheduled behavior reads as gridlock.**
- **The red-light exemption saves the signal-based design**: a holding stop
  line caps the room walk as `holdSeal` (rightofway.go:388-394) and both arms
  discard it — "a red light is never a trigger" (gridlock.go:166-171). So a
  train held **by a red aspect** (ADR-0037 override or fixed program) can sit
  forever; a train held by *driver intent* at a capacity-blocked junction
  cannot. This asymmetry is a strong argument for the interlocking-as-signal
  decision candidate (§4, synthesis D5) over a dwell-only controller: route
  holds should ride the signal plane, not the accel plane.
- `stuckTicks` is keyframed (TSKF v5, keyframe.go:80-107; vehicle.go:91-100)
  so dwell-then-restore is replay-exact. ✔
- ADR-0039 (uncommitted) does not change this analysis; its gate defers
  *spawns*, and gate-held ticks don't count toward the director expiry window.

## 3. ADR-0010 rowGate as an interlocking

Could the kernel's right-of-way gate approximate Tower 18/12 route locking?
Surfaces: `rowGate` (rightofway.go:77-178), approach classes
(:41-46), conflict evaluation `rowConflict` (:240-259), mutual-hold
resolution by class then lower vehicle ID (:28-31, foeApproachBlocks
:537-578), gate discovery `gateTarget` bounded by `maxLaneHops`/`maxSightM`
(:198-223).

What maps:
- An interlocking plant is a junction with crossing/merging movements —
  exactly what `FoesCross`/`FoesMerge` + internal lanes compile. netconvert
  emits rail switches as junctions; our importer would treat them like road
  junctions (rowClass, netimport.go:556-569).
- The box checks (§1.3) give "one train per conflict zone" semantics with the
  119 m clearing rule — close to route locking with overlap, accidentally.
- Deterministic tie-breaks (lower vehicle ID) — replay-safe.

What breaks:
- **No route locking in advance**: `boxBlocked` evaluates occupancy *now*;
  nothing reserves a route for an approaching train. Two trains converging on
  Tower 18 are adjudicated at the stop line by ID tie-break, not by timetable
  or first-come-route-set. ID order = spawn order — correlates with schedule
  only accidentally.
- **RowStop forces a full stop every traversal** (:163-169) — wrong for a
  clear interlocking (a home signal at proceed doesn't demand a stop).
  RowMajor flows but has no yield discipline toward schedule priority;
  RowNone is free traversal.
- **Foes are compiled within one junction only** (rightofway.go:352-357
  comment; mergeThreat is in-box only, :364) — cross-junction flank
  protection (Tower 18's multiple approach/exit pairs spread over more than
  one netconvert node) has no representation.
- **No approach locking / no overlap (Durchrutschweg)**: the model has no
  notion of a route staying locked behind a passing train for a braking
  distance. The 119 m exit rule is entry-side only.
- Sight: `gateTarget` returns nil past 100 m (:211-213, :218-219) — fine, the
  gate only needs to hold a train *at* the line; but sigGate/rowGate wall
  braking from 24 m/s within 100 m of sight can exceed service braking (the
  IDM wall uses the vehicle's own B; needed decel v²/2d = 2.88 m/s² > B for
  rail B ≈ 1.0). The kernel gate is a backstop; the controller must brake
  earlier. Consistent with L1.

Verdict: rowGate alone approximates an interlocking the way a stop sign
approximates a home signal. The natural kernel mapping is the **signal plane**
(§4), with rowGate as the always-on safety floor it already is.

## 4. ADR-0011 + ADR-0037 signals: interlockings and grade crossings as phases

- Fixed-time programs (ADR-0011) are data-driven per junction; enforcement
  (`sigGate`, signal.go:19-34 doc; mapSigChar :176-180) composes with the box
  checks: **green never means enter a box you cannot exit** — which is exactly
  home-signal-plus-overlap behavior. Rail "aspects" r/y/g map onto the tlLogic
  alphabet directly.
- ADR-0037 (M1+M2 shipped) makes phase state command-driven from outside:
  `signal_set` verb, `EnqueueSignal` (sigctl.go:168), one derivation seam
  (`sigPhaseAt`), bounded holds with lapse logging and a 300 s chain clamp,
  keyframed TSKF v7 and folded into the CRC only while held. An **external
  interlocking controller** (occupancy watcher + route table) issuing
  `signal_set` per home signal is replay-safe by the same intent-log precedent
  as vehicle intents — and strand-safe per §2's red-light exemption.
- **Grade crossing as a signal phase** (deferrable edge): the gate = a phase
  where road approaches are red; default road-green; triggered by approach
  detection (external controller watching TSSF frames, issuing bounded
  `signal_set` holds; lapse bounds a stuck "gate down" at the 300 s chain
  clamp — the fail-safe). FRA numbers for the timing are in
  standards-and-patterns.md §7 (≥20 s warning, 49 CFR 234.225; gate motion
  ≥3 s after flashers, 234.223). Structural gap to note: today the crossing
  itself evaporates at import — connections into skipped rail lanes are
  dropped (netimport.go:294-297, `DroppedConnections`), so the junction where
  road meets rail never compiles. Modeling the gate requires rail lanes to
  exist *and* the crossing junction to survive import — a network-format
  decision riding on D2 (synthesis).
- Feasibility caveat: signals bind to **internal** lanes of a junction
  (netimport.go:402-436: only `Internal` lanes get TL bindings). Block signals
  mid-track (CTA ATC block boundaries between stations) are not junctions —
  representing them needs either synthetic one-lane "junctions" at signal
  locations (SUMO's own advice: split the edge or set the node type, see
  standards-and-patterns.md §1) or a class-aware mid-lane stop line the format
  doesn't have. Decision candidate D6 (synthesis) picks the synthetic-junction
  route to stay inside format v1.

## 5. Keyframes / record plane (TSKF)

- Current write version **8** (keyframe.go:149-179; v8 = ADR-0039 gate bit).
  Per-vehicle record: `typeIdx u32 | S f64 | V f64 | F f64 | cooldown | rng |
  cruise | heldTurn | signals | route` (+ v5 stuckTicks/stopDone, v6
  laneEntryTick). **No per-vehicle length field anywhere** — length rides the
  type, types ride the scenario list, the list is run identity (§1.7). A train
  serializes exactly like a car; ≥92 B/vehicle (keyframe.go:58-61) and rail
  fleets are tiny (dozens), so the ADR-0015 chunking thresholds are
  untouched. ✔
- CRC (engine.go:1068-1085) covers S/V/F/cooldown/draws/typeIdx — no
  magnitude assumptions a 117 m vehicle stresses. ✔
- The known lane-INDEX binding of keyframes (ADR-0029: a keyframe binds
  vehicles to lane index; loading against a re-imported network misplaces
  everything) applies to rail imports equally: adding rail lanes to a network
  changes lane indices → all pre-rail keyframes/recordings for that network
  are invalidated as resume points (they still replay against their own
  network). Rail is a network-content change → new content hash → new run
  identity. Expected, but say it in the ADR.
- Director queue keyframing (v3/v4) covers scheduled-train directives in
  flight; signal override keyframing (v7) covers interlocking holds. The
  replay story for a full rail layer is intact **as long as every rail
  behavior arrives via existing verbs** (spawn, 4-axis intent, signal_set) —
  the replay-safety argument of synthesis D4.

## 6. Netimport notes for a rail-enabled pass

- The skip site is `motorLane`/`allLanesNonMotor` (netimport.go:597-633) fed by
  `motorClasses` (:83-88). Enabling rail = add rail classes to the keep set
  *with a class mask on the emitted lane* — `NetLane` (`engine/netfile.go:48-79`) has
  **no class/allow field today**; allowing rail lanes without a mask would let
  cars drive on track and trains on road (the graph is one lane set). This is
  the importer half of decision D2; the kernel half (routing/lateral/enforcing
  the mask) does not exist either — `pickSuccessor` (engine.go:983-997) and
  routing.go know nothing of classes.
- `durableID`, section naming, origin/exit derivation (netimport.go:326-343)
  all work unchanged on rail edges. Rail portals (terminal tracks cut by the
  extract bbox) become Origin/Exit lanes automatically.
- Rail junctions arrive as ordinary junctions; ADR-0038 consolidation
  (`sliverMaxLengthM = 5.0`, engine/netimport/consolidate.go:52) deletes
  sub-5 m connectors — for rail switches that's probably fine (rail junction
  internals are switch curves, longer than 5 m), but the rail import spike
  must check the report's `ConsolidatedSlivers`/`UncontrolledSeamChains`
  (netimport.go:60-72) on a rail-bearing network.
- The importer's connection pass ranks successors by `dir` for left-to-right
  (netimport.go:508-526) — rail switches have `dir="s"`/`l`/`r` as usual; no
  issue expected.
- GTFS/station data has no home in the network format: `NetLane` has no
  stop/station concept, and `LaneSource` (`engine/netfile.go:102`) tracks
  provenance only. Stations would be an authored scenario-side layer (synthesis D7).

## 7. Surface inventory (what a rail milestone touches)

| Surface | File:line | Nature of change |
|---|---|---|
| Extract filter keeps railway ways | scripts/chicago/extract.py:52-55; scripts/overpass-lean.py:43 | add railway to kept tags |
| Importer class list + class mask | engine/netimport/netimport.go:83-88, 597-633; engine/netfile.go:48-79 | keep rail lanes, emit mask |
| Train vehicle type registry | engine/cmd/serve/main.go:154; engine/cmd/demosrv/params.go:28; engine/cmd/simrun/main.go:66; engine/vehicle.go:33-40 | add type(s) |
| Sight bound vs consist length | engine/engine.go:575-577 | raise or length-aware |
| Box occupancy at exit (tail) | engine/rightofway.go:277-286 | count tail occupancy or document |
| Strand escape vs scheduled holds | engine/gridlock.go:172-207; engine.go:88 | rail exemption or signal-held doctrine |
| Interlocking = signal_set controller | engine/sigctl.go:168; signal.go | new external controller, no kernel change |
| Mid-track block signals | netimport.go:402-436 (internal-only binding) | synthetic junctions at signal points |
| Spawn expiry vs headways | engine/director.go:55 | widen window for scheduled classes or re-issue |
| Metrics footprint attribution | engine/metrics.go:493 | distribute or document |
| Scenario/station layer | (new) | GTFS-derived authored dwell points |
