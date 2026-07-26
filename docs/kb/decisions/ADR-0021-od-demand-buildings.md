# ADR-0021: Origin–destination demand — trip ends, interior origins, building anchors

- **Status:** PROPOSED
- **Date:** 2026-07-25

## Context

Demand today is *portal inflow with random egress*. Every flow
(`scenario.Flow`) names a boundary origin lane and a rate; the default
driver then draws each vehicle a destination among the network's EXIT
lanes, weighted by speed limit (`engine/natsio/driver/destinations.go`,
ADR-0019). Two things follow, and both are visible in chi-loop:

1. **Nobody is born inside the network.** All 112 chi-loop flows sit on
   boundary portals. A downtown zone where every vehicle enters from the
   edge has no garages, no residents, no deliveries — the entire fleet is
   through-traffic by construction.
2. **Nobody arrives anywhere.** A trip can only end by leaving the map,
   because despawn fires only on `lane.Exit` (`engine/engine.go`
   `boundaries()`). Destinations are therefore drawn from exits, and the
   speed-limit weighting means high-class edges attract traffic —
   a proxy for "where roads are big", not "where people are going".

The result is diffuse cross-town circulation. chi-loop's own tuning log
records the symptom without naming the cause: at 10k veh/h, "delay SPREAD
across many lanes (no single defect lane)". Real AM-peak congestion is the
opposite shape — convergence on a small number of destinations produces
localized, nameable failures (the Ohio St feeder, the Wacker ramps, the
Lake Shore Drive exits). A Chicago resident recognizes a simulation by
those, not by an average speed.

ADR-0019's deferrals already anticipated this: "a NON-EXIT destination
reactivates routing after the vehicle leaves it (cyclic networks could loop
back) — unreachable today (destinations are exit lanes, arrival =
despawn)". That deferral is now the blocking item.

The demand-modeling gap is separately recorded in
`gaps-and-roadmap.md` under-documented areas (`domain-demand-modeling`:
"Scenario format fixes the container; demand estimation itself is
unresearched"). This ADR does not close that topic — it adds the smallest
mechanism that lets demand be anchored to real land use.

## Decision

### 1. The route destination is a TRIP END

`boundaries()` gains an arrival case: a vehicle that reaches the end of its
route destination lane leaves the world, counted in `Stats.Arrived` (a
breakdown of `Stats.Despawned`, not a second total).

The exit case is tested **first**, so a destination that is itself an exit
lane despawns exactly as it always did.

**On replaying pre-ADR-0021 recordings.** The fixture suite is *not*
sufficient evidence here, and an earlier draft of this section leaned on it
anyway. The exposure is §3b, not this section: `routeHopOK` now gates
`tryForcedLaneChange`, and `ReplayFromStream` re-applies recorded
`LaneDelta` intents through exactly that path — so a recorded hop by a
vehicle carrying a `Route` could be vetoed on replay and diverge the CRC
stream. No fixture sets `Route`, so no fixture can detect it.

Audited directly instead, by replaying archived recordings on this kernel
through the strict `ReplayFromStream` path:

| recording | vehicle-ticks | routed | forced hops | CRCs verified | result |
|---|---|---|---|---|---|
| `wq4-stress2` | 768,096 | 0 | 1,749 | 799 | pass |
| `i280-pod-base` | 7,083 | 0 | 16 | 99 | pass |
| `i280-pod-base-15m` | 7,083 | 0 | 16 | 99 | pass |
| `i280-pod-meter` | 6,100 | 0 | 5 | 99 | pass |
| `signal-4way` | 6,465 | 0 | 12 | 99 | pass |

Every archived recording replays to identical CRCs, with **zero** route
vetoes and zero engine-initiated recoveries. The reason is that no archived
run carries routes at all: route assignment needs `Config.ExitRouting`
(`driver/driver.go`), and although `serve -exit-routing` defaults to true,
every recording in `data/recordings/` predates its use.

So the compatibility claim holds, but state it as what was measured: **the
recordings that exist replay bit-identically, verified**, not "the change
cannot affect replay". A hypothetical old recording containing routed
vehicles WOULD be at risk, and the guard against that is that none exists.

This also resolves the ADR-0019 loop-back deferral by removing its premise:
a routed vehicle never travels *past* its destination, so there is nothing
for the route to re-steer.

### 2. Destination and injection offset ride the spawn verb

`VerbRequest` gains `destination` and `offset_m`, both `omitempty`;
`SpawnDirective` and the record-plane `loggedVerb` carry them likewise.
A recording of portal-only demand is byte-identical to a pre-ADR-0021 one.

- **`destination`** is applied as the vehicle's `Route` axis at injection.
  The kernel follows it (`engine/routing.go`) and now ends the trip there.
  The default driver already prefers an existing `ego.Route` over its own
  exit draw, and the obs frame carries `Route` — so a director-assigned
  destination suppresses `pickExit` with no driver change, and incidentally
  skips the ADR-0019 route budget for that vehicle.
- **`offset_m`** is the injection position along the origin lane. Zero is
  portal semantics and is the ONLY value a network origin accepts;
  positive admits any lane. **The explicit offset IS the interior opt-in** —
  a mistyped portal id cannot silently become a mid-network injection, and
  offset 0 on an interior lane (the junction mouth, the worst possible
  injection point) is unrepresentable.

A destination lane that ends in a **wall** is rejected at validation, on
both the verb path (`director.go`) and the demand-file path
(`scenario.go`). Arrival is detected as `S > Lane.Length`, but an `EndWall`
lane brakes its traffic to a stop *before* that line, so a vehicle routed
there parks at the wall and can never arrive — verified on `lanedrop`:
alive=1, arrived=0, despawned=0, S=598.09/600, V=0 after 4,000 ticks, i.e.
a permanently leaked vehicle inflating every occupancy metric for the rest
of the run. No city import can reach this (netimport marks dead ends as
exits; `chi-loop-urban` has zero `EndWall` lanes), so the guard costs
nothing real and removes a silent failure mode from the synthetic
fixtures. Supporting a genuine cul-de-sac destination means defining
arrival as "stopped at the destination's wall" — deliberately deferred.

The director's injection **probe carries the destination as its `Route`**.
`gateTarget` walks successors through `pickSuccessor`, which follows the
route where there is one, so a route-less probe checked the signal on the
*default* branch while the vehicle `injectDirective` then created took the
*routed* one. On a city network those are different internal lanes, so the
probe could clear an injection against the wrong light.

TSKF keyframes go to **v4**, written only while some QUEUED directive
carries one of the new fields; a queue of plain portal spawns still
marshals v3, and an empty queue still marshals v2.

The director's per-vehicle draw order is **gap → vtype → destination**. The
destination draw is appended LAST and is skipped entirely when a flow
declares no destinations, so every pinned pre-ADR-0021 demand realization
consumes the identical stream sequence (`sampler_test.go` pins this).

### 3. Interior injection is clearance-checked BEHIND as well as ahead

A portal needs no rear check: origin lanes have no predecessors, so nothing
can rear-end a slow entry — that is why `injectionPlan` has no speed floor.
An interior injection has traffic behind it and the reasoning does not
carry. `injectionPlan` therefore additionally requires, when `v.S > 0`,
that the nearest follower can brake comfortably to the injected rear bumper
(`rearClear`, using the follower's own `Type.B`/`S0`, searched on the lane
then through predecessors via `prevFollower`).

Note the deliberate asymmetry: an unsafe LEADER gap only caps the entry
speed, but an unsafe FOLLOWER gap **denies the entry outright**. There is no
entry speed that fixes being materialized on top of someone. Denials carry
over as demand through the existing bounded hold-and-retry — which is also
what a car nosing out of a garage actually does.

**The two gap searches start from opposite bumpers, and the first cut of
this rule left the space between them unguarded.** `leaderAt` searches from
`v.S` (front bumper) and `followerAt` from `v.S − Length` (rear), so a
vehicle whose *front* bumper lay strictly inside `(rear, v.S]` was behind
the leader search and ahead of the follower search — invisible to both, and
the injection was admitted directly on top of it. The window is one
injected-vehicle length wide: 5 m for a car, 12 m for a truck. Measured on
`lanedrop` with an injection at S=250: a stopped vehicle at S=245.1, 247.0
or 249.9 was admitted; at 244.9 and 250.1 it was correctly denied.
`rearClear` now rejects any vehicle occupying the injected footprint before
it measures any gap, because that is a denial and not a gap. Note that the
lane-change path never had this hole — `sideCtx` searches the on-lane
follower at `v.S` and lets the gap go negative, which is the idiom
`followerAt` should have followed.

**Known limitation, deferred:** `prevFollower` walks `Prevs[0]` only, so at
a merge a fast vehicle approaching on a second predecessor is not seen by
the rear check. This is pre-existing behavior shared with the lane-change
safety path, not introduced here, and it is bounded — it can only miss a
follower that is off the injection lane at the moment of injection. Fixing
it means widening `prevFollower` to all predecessors, which changes
lane-change decisions and therefore every recorded CRC; it wants its own
change and its own review round.

### 3b. Route following gains a LATERAL half

Making the destination a trip end exposed that route following was only
half implemented. `routeNextHop` chooses among a lane's *successors*, so it
is purely longitudinal; the lateral policy (MOBIL, run by the external
driver, which never sees a route) is route-blind. A vehicle that changes
into a lane its destination cannot be reached from has silently abandoned
its route, and nothing recovers it — it drifts to whatever exit it meets.

This is not a corner case. Measured on chi-loop: for a given destination,
**28% of multi-lane positions that can reach it have a lateral neighbour
that cannot**, and in the first OD smoke run **only 8 of 102 completed trips
ended at their assigned destination** — the other 94 left via the map edge.

The mechanism is a **lateral-depth table** per destination
(`engine/routing.go`, `routeLatDepth`): a 0-1 BFS from the destination over
the reversed lane graph with successor edges at cost 0 (the vehicle just
drives them) and lateral `Lane.Left`/`Lane.Right` links at cost 1 (it has to
hop). `latDepth[lane]` is "how many lane changes am I still from a lane that
reaches the destination by driving"; 0 means already on route, −1 means
unreachable at any depth. It is written in the layered form — expand the
0-cost closure of the current layer, then take one lateral step to seed the
next — rather than with a deque: same algorithm, same O(V+E), no
`container/list`, and the two edge costs read as two loops.

On that gradient, two additions, both engine-side, both pure functions of
(network, lane, destination) with no RNG and no map iteration:

- **Guardrail (veto).** A lateral hop that *increases* a routed vehicle's
  lateral depth is denied — commanded hops included, the same shape as the
  ADR-0010 right-of-way guardrail that "caps every control path". Sideways
  moves at equal depth stay legal, so MOBIL may still pick the faster of two
  lanes that are equally far from the route. This is not the engine
  overruling a controller: the route is *itself* a controller-set axis, so
  the kernel is enforcing consistency between two axes the controller set,
  and a controller that genuinely wants the hop can clear the route first.
- **Recovery.** A routed vehicle off-route hops toward the neighbour with
  *strictly smaller* depth, subject to the full commanded-hop safety gate.
  Descending a gradient rather than requiring a route-reachable neighbour is
  what lets recovery cross more than one lane: the left lane of a 3-lane
  arterial whose exit is on the right has no route-reachable neighbour at
  all, but it does have one that is a lane change closer. Ties break toward
  the lower lane index. A vehicle off-route does not also run discretionary
  MOBIL that tick.

After both, **100% of completed trips ended at their assigned destination**.

Each lookup is O(1) against the memoized table, so the guardrail costs one
array lookup per evaluated hop, and the table costs one `int32` array per
destination alongside the next-hop table (same never-invalidates grounds:
the network is immutable for the run, so the cache is derived state — not
serialized, not in the CRC).

The first cut of this shipped the special case `latDepth == 0` as a boolean
`routeReaches` predicate, with recovery requiring a neighbour that was
itself route-reachable. That is exactly a one-lane recovery radius, and it
left a vehicle two lanes out stranded forever — pinned by its own guardrail
against a lane it could not use. The gradient subsumes it;
`TestRouteRecoveryCrossesTwoLanes` pins the case the predicate could not
solve (on `lanedrop`, A2's only neighbour A1 is itself off-route, so under
the predicate the vehicle rode A2 to its end wall and never left the world).

### 4. Demand grammar

`scenario.Flow` gains, both `omitempty` so no existing hash moves:

```yaml
- id: r012-tower
  origin: n123456_0     # any lane, given offset_m
  offset_m: 62.1        # interior opt-in: mid-block garage access
  veh_per_h: 12
  spacing: poisson
  destinations:         # weighted; relative weights, drawn on SORTED keys
    n789_0: 0.6
    n456_1: 0.4
```

Validation is strict and load-time (ADR-0012 §2 doctrine): every
destination must name a lane of the network, every weight must be positive
and finite, and an offset must leave the vehicle wholly on its lane. A
zero weight is an error, not a silent never-draw. Adding destinations MOVES
the content hash — two materially different demand programs must never
share a run identity (ADR-0012 §6).

### 5. Building-anchored OD generation

Origins and destinations are derived from **OSM building footprints** in the
zone, snapped to an eligible access lane:

- Residential buildings (`building=apartments|residential|dormitory|…`)
  become interior origins; workplace buildings
  (`building=office|commercial|retail|hotel|…`) become destinations.
- Weight is **floor area** = footprint × levels (`building:levels`, else
  `height`/3.5, else a per-kind default). Floor area is the standard
  first-order proxy for trip production/attraction, and it is the only
  attribute OSM carries densely enough to use.
- Access lanes exclude junction internals, sub-30 m netconvert fragments,
  and **motorways/trunks and their links** — a garage has no driveway onto
  the Kennedy. This filter is load-bearing: without it towers snap to
  freeway lanes and the model is worse than uniform demand.
- Freeway and arterial portal inflow is unchanged in kind (it is real
  through-traffic and commuters from outside the crop) but gains
  destination distributions and an AM peak profile via `slices`.

The generator is a script (`scripts/chicago/`), not engine code: demand
generation is scenario authoring, and its output — a demand YAML — is the
reviewable artifact.

### 6. The scenario runs on a re-imported network with real speed limits

Building the first OD scenario surfaced a defect in the *import*, not in the
demand: netconvert's built-in OSM typemap is German-derived, and only 16% of
chi-loop's `secondary` ways carry a `maxspeed` tag, so 12,945 of its 15,975
secondary lanes compiled at 100 km/h. Michigan Avenue, Wells, Wabash and
State Street were simulated as autobahn. The general decision, the corrected
typemap and the blast radius across every US import are **ADR-0022**; what
belongs here is the scenario-level consequence.

`chi-loop-od-30m` (and the superseded `chi-loop-od`, `chi-loop-od-peak`) run on
`chi-loop-urban` (`data/networks/chi-loop-urban/`, same OSM extract, same
netconvert flags, `-t scripts/osm-urban-us.typ.xml`) rather than on
`chi-loop`, and why that mattered to *this* ADR:

- **The OD program's own quality metric was reading against a wrong
  reference.** Delay is measured against free-flow speed, so an inflated
  free-flow inflates delay. Every "N% of time in the network is delay"
  figure recorded against `chi-loop` is overstated. The quantified
  old-vs-new comparison that used to sit here was taken on the defective
  first import and has been withdrawn rather than re-run — the import
  decision is settled (ADR-0022) and the comparison was only ever
  justification for it.
- **Topology is preserved**, which is what makes the swap safe: the external
  lane set is bit-identical (23,837 lanes, symmetric difference 0), as are
  origins (246), exits (1,432), signal programs (2,217) and signal links
  (13,181). The residual is netconvert's speed-dependent junction-interior
  construction: internal lanes 31,705 → 31,722, connections 61,229 →
  61,246, yield approaches 1,089 → 1,106, conflict pairs 29,360 → 29,396
  (all **+17/+36**, and note the sign — the defective first import LOST 118
  internal lanes, because right-before-left retyping deletes internal
  junctions). Recordings made on the two networks are still NOT comparable
  at junction level.
- **The demand had to be re-tuned.** ADR-0021's original 12,000 veh/h was
  set against the 62-mph network. The scenario now ships as
  `chi-loop-od-30m` at a **16,000 veh/h target (12,960 injected)**, a flat
  peak over 18,000 ticks (30 sim minutes) — chosen by bracketing 10k–20k on
  the corrected import: nothing in that range gridlocks, 16,000 gives
  26.0 km/h mean network speed with arterials at 7–13 km/h, and collision
  observations grow superlinearly past it (+25% demand → +71% collisions).
  See `data/scenarios/chi-loop-od-30m/README.md`.
- **The OD demand does not congest the expressways, and this ADR's
  generator is why.** Six of the ten corridors in the Chicago hotspot
  research report are inside this extract; all six run free (Kennedy
  72.1 km/h, Eisenhower 79.4, Dan Ryan 78.6, Stevenson 78.3, Lake Shore
  Drive 53.4, Jane Byrne 48.5) and carry 3.3% of network delay between
  them. `mkod.py` scales the per-class portal table to hit `--total`, so
  the class table is only a shape: at 16,000 the Kennedy's two boundary
  origin lanes get 337 veh/h/lane. Reaching the motorway class rate needs
  `--total ≈ 67,000`, which buries the arterial grid. **One scalar cannot
  congest both**, and the fix is to exempt freeway portals from the zone
  scaling rather than to raise the total.

## Consequences

- **Trip records gain meaning.** ADR-0014's `TripRecord` already carries
  origin/destination and time loss; with real trip ends, completed-trip
  travel times become comparable quantities instead of "time until the
  vehicle wandered off the map".
- **The despawn-tick metric attribution is now exact for routed vehicles.**
  `exitShares` resolves toward the vehicle's destination when it has one,
  where `exitResolve` could only guess among reachable exits and gave up
  when several were in range. Observer-side only, no kernel feedback — but
  it CHANGES `dropped_crossings` for existing scenarios, a reporting change
  rather than a physics one.
  Measured effect on chi-loop: small. 47,888 → 47,090 over 18,000 ticks,
  because the dominant source on a dense grid is a different site — the
  overshoot refund of a vehicle parked past a lane end whose lane has more
  than one successor (`metrics.go`, the `len(Successors) == 1` branch),
  which is inherent to multi-successor networks and unrelated to routing.
  `dropped_crossings` is a single counter mixing at least four causes;
  splitting it per reason would make it actionable, and is the honest
  follow-up to WQ-4's "correlate with `_d2` fragments" item.
- **Fleet size becomes bounded by trip length rather than map traversal.**
  Interior destinations end trips sooner, so a given veh/h supports a
  smaller steady-state fleet — existing chi-loop tuning numbers do not
  transfer and the scenario must be re-tuned.
- **The zero-weight-is-an-error rule** means a generator cannot emit a
  destination with weight 0 as a placeholder; it must omit it.
- Interior origins have no `denied_by_lane` semantics distinct from
  portals: a denied interior entry accrues wait on its lane exactly like a
  blocked portal. Whether garage demand SHOULD queue indefinitely (the
  600-tick expiry drops it) is left open — see below.

- **The metric kernel had to learn about interior injection.** Its
  first-observation branch assumed a vehicle seen off an origin lane must
  have crossed a boundary within its spawn tick, so it booked the whole
  lane prefix `v.S` as travelled distance and counted a dropped crossing.
  Under ADR-0021 both are wrong on every interior injection — a silent VMT
  inflation proportional to injection offset, once per resident. The engine
  now exposes `InteriorInjections()` (derived per-tick state, never
  serialized, not in the CRC, the `AppliedSpawns` pattern) and the kernel
  books `v.S − entryS` and suppresses the dropped-crossing count for those
  vehicles.
- **Memory scales with the DESTINATION COUNT, not the fleet.** Each
  distinct destination lane costs a memoized next-hop table AND a
  lateral-depth table — 2 × ~222 KB on a 56k-lane network (the per-table
  figure recorded in the chicago-metro review). 120 destinations ≈ 54 MB,
  and both tables are built lazily, so a destination never routed to costs
  nothing. Unlike the driver's exit routing, this count is
  set by the scenario rather than by clients, so it is a config knob
  (`mkod.py --dest-lanes`) rather than the unbounded client-controlled
  growth ADR-0006 §9 flagged.

## Open / deferred

- **Arrival at an offset, not a lane end.** A vehicle arrives at the END of
  its destination lane, so the building is modeled at the downstream end of
  its access lane rather than at the snapped point. At city block lengths
  the error is tens of meters. Adding a destination offset is additive
  (another verb field) if it ever matters.
- **No return trips / no activity chains.** This is a production–attraction
  model, not an activity model: a vehicle that arrives is gone. Modeling
  the evening reversal means re-running with the OD matrix transposed.
- **Interior-origin demand expiry.** Denied garage entries expire after
  `DirectorSpawnHoldTicks` (600) like any directive. On a saturated street
  a garage could silently drop most of its demand; the denied-entry metric
  surfaces it, but the right policy (queue vs drop) is unexamined.
- **Destination reachability is not validated at load.** An unreachable
  destination degrades to default routing and the vehicle never arrives —
  it leaves via an exit instead. A load-time reachability check over the
  whole OD set is O(destinations × network) and was not worth it before
  the first real OD scenario exists; the arrival-rate metric is the
  detector.
- **chi-loop has no residential street grid.** The zone was imported with a
  `motorway…tertiary` class filter, so it holds 107 `residential` and 108
  `service` lanes out of 23,833. Residential buildings therefore snap at a
  median 46.5 m (vs 10.4 m for workplaces) and residents pull out onto
  arterials they do not actually front. Fixing that is a re-import
  decision, not a snapping tweak.
- **Under-counted residential mass.** 71% of footprints are `building=yes`,
  and none of them carry a `residential=*` or `building:use=*` tag to
  promote on — so Chicago's 2- and 3-flats all land in `other` and produce
  no trips. `building=tower` (Trump Tower) is in neither list and drops out
  entirely. Both are known biases in the origin side, not the destination
  side.
- **Floor area is a crude production/attraction proxy.** No mode share, no
  car-ownership rate, no parking supply. Downtown Chicago's transit share
  is very high, so absolute rates are calibration targets, not derivations —
  the same napkin-math posture chi-loop's README already documents.
