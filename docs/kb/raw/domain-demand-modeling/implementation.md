# Implementation: Demand Modeling

> Source: codebase tracing | Researched: 2026-08-24 | Git HEAD: 2bc98de

How traffic-sim models "people coming from and going to locations" today: the
engine-side demand machinery (spawner, demand director, OD trip ends), the
Chicago generator stack that authors the demand files, and the calibration
gaps the KB already records. Every claim is `file:line`.

---

## 1. The two demand paths share one injection engine

There are exactly two ways vehicles enter the world, and both bottom out in
the same kernel mechanics:

1. **The deterministic spawner** (`engine/spawn.go`) — a fixed schedule per
   origin lane, declared on `engine.Scenario`:
   - `SpawnRatePerLaneHour` (veh/h per origin lane; 0 disables) and optional
     per-lane override map `SpawnRates` — lookup-only, "never iterated,
     ADR-0005" (`engine/spawn.go:12-13`).
   - `DensityTargetPerKm` — stop injecting at/above this network density
     (`engine/spawn.go:14`).
   - `DemandSchedule` — global multiplicative `Scale` steps applied to every
     origin rate at a tick (`engine/spawn.go:53-58`, applied at
     `engine/spawn.go:233-238`). This is the only time-varying demand the
     spawner has: one scalar that hits all origins equally.
   - `TypeWeights` — spawn mix, one uniform draw remapped by weights
     (`engine/spawn.go:262-283`).

2. **The demand DIRECTOR** (`engine/natsio/demand/director.go`) — an
   elevated-grants NATS client that samples `scenario.Flow` arrival programs
   and issues `spawn` verbs; the kernel validates and injects them through
   `stepDirectorSpawns` (`engine/director.go:288-371`).

Both paths reuse identical injection physics: the same `injectionPlan`
clearance rule ("exactly the Spawner's rule (shared helper)",
`engine/director.go:346-347`), the same density gate (`densityGateHold`,
`engine/spawn.go:337-357`, consulted by both `Spawner.step` at
`engine/spawn.go:244` and `stepDirectorSpawns` at `engine/director.go:304`),
the same per-vehicle keyed RNG via `e.newVehicle()`, and the same
desired-speed factor F = 1 + σ·N clamped to [0.8, 1.3] (`engine/spawn.go:285-290`,
`engine/director.go:381-386`). The kernel doc states the design intent:
"the kernel side is a deterministic injection queue that reuses the Spawner's
mechanics exactly… Only the schedule source differs" (`engine/director.go:8-16`).

**WHY:** replay. The spawner's schedule is part of the spec; the director's
verbs are recorded on the record plane ("accepted verbs are recorded on the
record plane, so replay never re-runs this sampler",
`engine/natsio/demand/director.go:8-9`). Demand is a pure function of
(definitions, seed) sampled exactly once; everything downstream is bit-exact
re-derivation. This is ADR-0012 §3's "a demand file IS a director
configuration" (`docs/kb/decisions/ADR-0012-scenario-format.md:65-83`).

**Source files:**
- `engine/spawn.go:60-97` — `Spawner` struct + `newSpawner` (per-origin state,
  rate/3600).
- `engine/spawn.go:101-109` — first spawn staggered uniformly within the mean
  interval, jitter from the pending vehicle's own stream.
- `engine/spawn.go:232-316` — `Spawner.step`: schedule steps first, then per
  origin: gate check → type/F from the SIDE stream (`spawnAttrStream`,
  idempotent across holds and keyframe restore, `engine/spawn.go:250-256`) →
  `injectionPlan` → register → draw next interval jittered by
  `SpawnJitter` from the NEXT vehicle's stream (`engine/spawn.go:304-314`).
- `engine/director.go:8-30` — the kernel-side contract doc.

---

## 2. The Flow grammar (scenario schema)

`scenario.Flow` is the whole demand vocabulary the engine understands
(`engine/scenario/scenario.go:122-144`):

```yaml
- id: p000-tertiary        # optional; overlay-patch anchor
  origin: n1005096065_0_0  # boundary portal (offset_m: 0) or any lane (offset_m > 0)
  offset_m: 62.1           # interior-origin opt-in (ADR-0021); omitted = portal
  veh_per_h: 1400          # flat rate; dead config alongside slices (a load error)
  spacing: poisson         # constant | poisson
  until_s: 0               # program cutoff, sim seconds
  slices:                  # piecewise-constant windows [start_s, end_s), sim seconds
    - {start_s: 0, end_s: 600, veh_per_h: 8.7}
  vtypes:                  # weighted type mix, relative weights
    car: 0.96
    truck: 0.04
  destinations:            # ADR-0021: weighted destination-lane distribution
    n1000805423_0: 0.0052
```

Key validation semantics (`validateDemand`, `engine/scenario/scenario.go:676-774`):
- `veh_per_h` is a **load error** alongside slices — "slices define the whole
  rate program" (`engine/scenario/scenario.go:735-737`). No two sources of
  truth for the rate.
- Slices must be sorted, non-overlapping, half-open; adjacency is fine
  (`engine/scenario/scenario.go:739-754`).
- Every weight (vtype, destination) must be positive and finite; a zero
  weight is an error, "not a silent never-draw" (`engine/scenario/scenario.go:765-767`,
  and ADR-0021 §4 `docs/kb/decisions/ADR-0021-od-demand-buildings.md:253-258`).
- Destinations must name a lane of the network and must not be `EndWall`
  lanes — arrival is `S > Lane.Length` but a wall brakes traffic short of it,
  so a vehicle routed there "stays in the world forever, inflating every
  occupancy metric" (`engine/scenario/scenario.go:323-360`; the identical
  verb-side rule at `engine/director.go:206-221`).
- `offset_m: 0` on a non-portal lane is rejected: "interior injection needs
  an explicit offset_m" (`engine/scenario/scenario.go:713-716`). The
  explicitness IS the safety property — a mistyped portal id cannot silently
  become a mid-network injection (`engine/scenario/scenario.go:137-143`).

**WHY the grammar is this shape:** ADR-0012 §3 fixed "layered primitives in
sim seconds, sampled at runtime by the director… piecewise-constant rate
slices… No OD-matrix compile step, no analytic rate functions"
(`docs/kb/decisions/ADR-0012-scenario-format.md:65-83`). The research behind
it found time-varying demand is "universally piecewise-constant slices,
never analytic functions" across SUMO/MATSim/Vissim/Aimsun
(`docs/kb/decisions/ADR-0012-scenario-format.md:23-31`). Notably there is
**no OD matrix anywhere in the engine or schema** — OD exists only as
per-flow destination *distributions*, which is an origin-anchored
production–attraction form, not a matrix form. Consequence: an
(origin, destination) cell count is not directly authorable; the matrix is
implicit in the cross product of flows and their destination weights.

---

## 3. The director: sampling discipline

`engine/natsio/demand/director.go` is where arrivals become verbs.

- **Per-flow sampler** keyed by (origin, global flow index) — the index is
  global across all demand files so "request ids and RNG keys can never
  collide across files" (`engine/natsio/demand/director.go:15-17`,
  `newFlowSampler` at 385-393; the 8-byte index fixed a 256-flow collision,
  381-384).
- **Per-vehicle keyed stream** `DeriveStream(seed, flowKey^ordinal)`
  (`engine/natsio/demand/director.go:397-399`). Draw order is **gap → vtype
  → destination**, with the destination draw appended LAST and skipped
  entirely when a flow declares none — "every pinned pre-ADR-0021 realization
  is unchanged" (`engine/natsio/demand/director.go:419-461`).
- **Weighted draws walk SORTED key lists** because float addition is
  non-associative and Go map order is random — "a director restart must be
  invisible" (`engine/natsio/demand/director.go:486-509`). Same rule for
  vtypes (`pickType`, 468-473).
- **Rate at draw tick governs** a gap that lands past a slice boundary —
  keeps sampling stateless (`engine/natsio/demand/director.go:22-24`,
  `rateAt` 403-413). First arrival at the first window's start, matching
  SUMO's "flow begins emitting at begin" (19-21).
- **Poisson spacing** = exponential gaps `-ln(1-u)·mean`, the doc noting
  equivalence with SUMO `period="exp(X)"` (`engine/natsio/demand/director.go:451-454`).
- **Verbs are published asynchronously** with a private reply inbox, not
  requested synchronously: a blocking request drains one wire request per
  tick by contract construction, which "pinned the whole director to ONE
  spawn per tick — 36,000 veh/h at dt=0.1". Measured failure on
  chi-loop-urban: 41,133 veh/h declared, 17,998 verbs accepted, 2,946
  injected, injection dead from tick 3,033 (`engine/natsio/demand/director.go:82-100`).
- **Deterministic request IDs** `f{flow}-{ordinal}` make a restarted director
  re-issue the identical program; engine request-id dedup makes the overlap
  harmless ("failover-invisible", `engine/natsio/demand/director.go:11-17`,
  send at 342-367).
- **Warm start** fast-forwards samplers past arrivals already in the
  restored state, consuming the same draws, "so each sampler lands on exactly
  the position the cold run held at that tick" (`engine/natsio/demand/director.go:191-201`,
  `fastForward` 268-288).

**Kernel-side queue** (`engine/director.go`):
- Accepted verbs enter a FIFO injection queue at the tick boundary, stamped
  with `applied_tick`, recorded on the record plane (`TickedSpawn`,
  `engine/director.go:153-162`, queue step 288-299).
- **Blocked-origin policy: bounded hold-and-retry.** A directive past its
  earliest tick retries every tick for `DirectorSpawnHoldTicks` = 600 ticks
  (60 sim s), then expires — counted, never silent (`engine/director.go:32-55`).
  The comment records the past error: believing stale verbs were "superseded
  demand… is what let chi-loop-urban lose 15,052 of 17,998 requested vehicles
  with every metric reading clean" (`engine/director.go:43-48`).
- **The probe carries the route.** The injection-safety probe is a stand-in
  vehicle with `Route: d.Destination` because `gateTarget` follows the routed
  branch; a route-less probe "could clear an injection against the wrong
  light" (`engine/director.go:346-356`).
- Expiry accounting distinguishes origin-blocked from dead-on-arrival and
  gate-caused (`expireDirective`, `engine/director.go:84-115`).

---

## 4. OD trip mechanics (ADR-0021): what "going to a location" means

ADR-0021 (`docs/kb/decisions/ADR-0021-od-demand-buildings.md`) is the decision
that made destinations real. Its context section is the sharpest statement of
the pre-OD model: "Nobody is born inside the network… Nobody arrives
anywhere… a proxy for 'where roads are big', not 'where people are going'"
(lines 14-22).

- **The route destination is a TRIP END.** `boundaries()` despawns a vehicle
  reaching the end of its route lane, counted `Stats.Arrived`
  (`engine/engine.go:870-927`; `Arrived` field at 111-115). The exit case is
  tested first so an exit-lane destination despawns as before
  (`engine/engine.go:876`).
- **`destination`/`offset_m` ride the spawn verb** (omitempty; portal-only
  recordings byte-identical) (`engine/director.go:131-151`;
  `docs/kb/decisions/ADR-0021-od-demand-buildings.md:88-105`). Destination is
  applied as the vehicle's `Route` axis at injection (`engine/director.go:389`).
- **Interior injection is clearance-checked BEHIND as well as ahead**
  (`rearClear`, `engine/spawn.go:170-211`): the nearest follower must brake
  comfortably to the injected rear bumper — "a car nosing out of a garage"
  (`engine/spawn.go:178-183`). Asymmetry: an unsafe leader gap only caps
  entry speed; an unsafe follower gap denies entry outright
  (`engine/spawn.go:178-184`). The footprint guard closes the blind window
  between the two bumper searches (`engine/spawn.go:185-199`).
- **The lateral half of routing (ADR-0021 §3b).** Route following was purely
  longitudinal; route-blind MOBIL lane-changing walked vehicles off their
  route. Measured: 28% of multi-lane positions that can reach a destination
  have a lateral neighbour that cannot, and only 8 of 102 completed trips
  ended at their assigned destination before the fix; after the
  lateral-depth guardrail + recovery, 100%
  (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:176-236`). The
  generator mirrors the kernel's exact reachability relation (§6 below).
- **Memory scales with destination count, not fleet:** each destination lane
  costs a next-hop table plus a lateral-depth table (~444 KB per destination
  on a 56k-lane network), which is why `mkod.py --dest-lanes` is "a real
  memory knob, not just a fidelity one" (`scripts/chicago/mkod.py:509-516`;
  ADR-0021 consequences, lines 378-386).

**What "weights that replicate the real world" means in-engine:** nothing.
The engine draws from whatever distribution the file carries; all realism
lives in the generator (§5-6). This is the ADR-0021 §5 doctrine: "The
generator is a script (`scripts/chicago/`), not engine code: demand
generation is scenario authoring, and its output — a demand YAML — is the
reviewable artifact" (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:280-282`).

---

## 5. The Chicago generator stack, phase 1: portal-weighted napkin demand

`scripts/chicago/mkdemand.py` (108 lines) is the phase-1 generator:

- Each non-fragment boundary origin lane gets veh/h from its **OSM road
  class**: `DEFAULT_RATES` = motorway 1400, trunk 900, primary 500,
  secondary 300, tertiary 200, and `_link` variants (`scripts/chicago/mkdemand.py:21-32`).
  Minor classes (residential/service/unclassified/track/path) are skipped —
  "boundary demand enters on classified roads only" (docstring, 5-7).
- Truck share: 8% on motorway/trunk classes, half that (4%) on arterials
  (`scripts/chicago/mkdemand.py:40-41, 69-71`).
- `--total` scales all rates to a zone total; flows scaled below
  `--min-rate` 60 veh/h are dropped (`scripts/chicago/mkdemand.py:42-46, 73-84`).
- Emission: poisson spacing, flat `veh_per_h`, no slices, no destinations
  (`scripts/chicago/mkdemand.py:88-100`).

**WHY napkin:** the docstring says it outright — "rates are per-lane
peak-hour estimates, anchored to published counts in the scenario README
where they exist" (`scripts/chicago/mkdemand.py:13-16`). The chicago-metro
article names the anchors: "IDOT AADT, cordon counts, households × peak trip
rate" (`docs/kb/articles/chicago-metro.md:263-267`). There are no
destinations: "Every vehicle is born at the map edge and leaves by whichever
exit it drifts to — the delay it produces is diffuse, with no defect lane,
because there are no desire lines" (`docs/kb/articles/chicago-metro.md:263-267`).

---

## 6. The Chicago generator stack, phase 2: building-anchored OD

### 6a. `buildings.py` — from OSM tags to demand-relevant floor area

`scripts/chicago/buildings.py` extracts building footprints and snaps each
demand-relevant one to an access lane. It is where the production/attraction
weights physically come from.

- **Classification** (`classify`, `scripts/chicago/buildings.py:203-220`):
  `residential` and `workplace` are the demand-relevant kinds (explicit tag
  lists at 153-163); everything else is `other`, excluded from the demand
  index. **`building=yes` is 71% of chi-loop footprints (30,804 of 43,281)
  and carries NO signal** (docstring, `scripts/chicago/buildings.py:39-41`).
  It is promoted to workplace only via a disambiguating tag (office/shop/
  healthcare/tourism/amenity, 168-178) or `building:levels >= 8`
  (`YES_TOWER_LEVELS`, 146) — and **never promoted to residential**, because
  "nothing in the chi-loop data distinguishes an untagged 2-flat from an
  untagged storefront" (53-59). This is a **known under-count of residential
  mass** — Chicago's 2- and 3-flats land in `other` and generate no trips.
- **Levels** (`resolve_levels`, 223-233): tag → height/3.5 m → per-kind
  default (residential 3, workplace 3, other 1). The docstring is candid
  about data quality: "Willis Tower (108 storeys) carries neither tag and
  takes the default 3… floor_area_m2 is therefore a RANKING signal, not a
  survey. No attempt is made to repair heights from an external source"
  (76-81). `floor_area_m2 = footprint_m2 × levels` (82).
- **Access-lane snapping** (85-124, 343-409): nearest eligible lane from the
  building FOOTPRINT (not centroid — the Merchandise Mart centroid is 90 m
  from every street). Eligibility excludes motorways/trunks and links ("a
  tower does not have a driveway onto the Kennedy. Without this filter the
  whole demand model is nonsense", 93-95), junction internals, and <30 m
  stubs. Mid-block preferred over junction mouth via a 25 m soft penalty
  (114-120). Beyond 150 m: no access lane at all — "a bad snap is worse than
  no snap" (122-123).

### 6b. `mkod.py` — the OD demand program

`scripts/chicago/mkod.py` (1,352 lines) turns the building index + network +
portals into a demand YAML. Its own docstring states the posture: "Floor
area is a production/attraction PROXY — there is no mode share, no
car-ownership rate and no parking supply here, and downtown Chicago's
transit share is very high. Absolute rates are a calibration target; the
SHAPE (where trips start and end) is what this script exists to get right"
(`scripts/chicago/mkod.py:26-33`).

**Origins (production side):**
- **Portal inflow** rated by `PORTAL_RATES` — deliberately identical to
  mkdemand's table (`scripts/chicago/mkod.py:44-48`) — scaled by
  `portal_scale = --total × portal_share / portal_raw` (926-936). The class
  table is only a SHAPE; `--total` sets the level.
- **`--freeway-scale`** multiplies grade-separated classes on top
  (`FREEWAY_CLASSES` = motorway/trunk + links, 51-58; flag 519-527; applied
  1041-1043). Rationale in the code: "ONE scalar cannot congest both road
  systems… at --total 16000 the factor is ~0.24, so the Kennedy's two
  boundary origin lanes injected 337 veh/h each — a sixth of freeway
  capacity — and it ran at 72 km/h through a simulated AM peak. Raising
  --total until the freeways bite needs ~67,000, which buries the arterial
  grid" (938-953). **`--freeway-scale` deliberately breaks `--total` as a
  grand total** — it becomes the ARTERIAL target, with realized totals
  printed (949-953).
- **`--corridor-scale NAME=FACTOR`** per expressway (577-587, 1043): "a
  global factor scales an already-too-light corridor and an already-heavy
  one by the same amount" (measured: freeway-scale 1.5→2.5 took Dan Ryan
  71.9→53.0 km/h, left Eisenhower 78.2→75.5).
- **`--ramp-share`** relocates a fraction of each corridor's boundary inflow
  onto interior mainline injection points (565-576, 958-996, 1107-1162):
  "a boundary-portal model gives a corridor its whole volume at one point,
  and a single point cannot pass more than one lane's capacity… What forms
  instead is a standing queue at the cut face which METERS the mainline"
  (1107-1115). Measured at freeway-scale 3.5: "84-95% of every corridor's
  delay inside 1 km of its injection point with free flow behind it, and
  capped delivery at 62%" (569-576). Picks spread by greedy k-center
  (`spread`, 162-199) on mainline lanes ≥40 m (`MIN_RAMP_LANE_M`, 74-78) —
  merge lanes are honest geometry but 6-8 m stubs would silently drop the
  volume; "injecting on the mainline models a merge that has already
  completed" (1117-1123).
- **Residential interior origins**: top `--origin-lanes` (default 150)
  residential access lanes by pooled floor area, injected mid-block at
  `offset_m` from the largest building's snap point, clamped ≥8 m from both
  lane ends (1183-1212). Rate = floor area × `res_scale` such that residents
  are `--resident-share` (default 25%) of the total (1184-1186). Below 4
  veh/h a lane is dropped as noise (`MIN_RESIDENT_RATE`, 66-68). Residents
  are always cars: "a tower's garage does not emit semis" (1204).

**Destinations (attraction side):**
- **Workplace destinations**: top `--dest-lanes` (default 120) access lanes
  by pooled workplace floor area (700-721); each flow draws from this pool
  weighted by floor area.
- **Through-traffic destinations**: boundary EXIT lanes (723-761). Without
  them "the demand program has no mass balance… the whole 27,000 veh/h of
  inflow drained through ~120 lanes… Measured at 18,000 ticks: 1,112 trips
  completed against 5,675 still circulating… a bathtub with the taps open"
  (723-733). Shares:
  `--through-share` 0.45 arterial portals, `--freeway-through-share` 0.75
  freeway portals ("the Kennedy carries ~250k AADT past downtown and only a
  minority of it exits into the Loop", 536-541). Freeway origins are held to
  freeway exits, and exits on the origin's own edge are dropped as U-turns
  (`through_for`, 852-866). Degenerate pools collapse the mix rather than
  lose demand, and the EFFECTIVE share is reported, not the requested one
  (`blend`, 868-884; the comment at 1034-1038 records how reporting the
  request instead of the outcome "is how 83% of Chicago's freeway inflow
  came to leave at the boundary while this line printed 75%").
- **Reachability filter = the kernel's own relation** (`can_reach`,
  124-159): a 0-1 BFS from the destination over the reversed lane graph
  (successor = 0 cost, lateral link = 1), mirroring `Engine.routeLatDepth`
  "exactly, including its lack of a hop cap" (124-133). It replaced a
  successor-only BFS that left five freeway origins reaching ~nothing —
  "driving two kilometres and leaving" (133-141).
- **District pins** (`zone_blend`, 350-405): `--dest-zone-share cbd=0.55`
  pins district shares of workplace trips; within a district weight stays
  floor area. A pin is "a REQUEST, not a guarantee" — an origin that can't
  reach a district spreads its pinned share over what it can reach, and the
  realized per-district vehicle-weighted shares are printed with the
  shortfall named (818-821, 1245-1268). The motivation: destination weight
  was "workplace floor area and nothing else, so the CBD's share of trips is
  whatever the building extract implies — 44% here — and there is no way to
  ask what happens at 55% or 30%. That share is the single largest lever on
  an AM peak" (770-777).

**Temporal shape (ADR-0028):**
- Built-in `AM_PROFILE = [0.45, 0.70, 1.00, 1.00, 0.80, 0.60]` — six
  half-hour slices of a 3-hour 06:00-09:00 window (`scripts/chicago/mkod.py:60-64`).
- `--profiles` replaces it with a named profile library (`ProfileSet`,
  230-347): shapes assigned by rules matching `kind`/`class`/`corridor`,
  first match wins, per-rule `scale` composing multiplicatively with
  `--freeway-scale`/`--corridor-scale`; a rule matching nothing is FATAL
  (`check_all_rules_fired`, 335-347). The shipped libraries
  (`scripts/chicago/profiles-am.json`, `profiles-am-kennedy.json`) carry
  trip-purpose shapes — commute-am, freight (earlier, flatter),
  reverse-commute, baseline — 600 s × 9 slices with a trailing-zero drain,
  and kennedy-am at scale 1.5 targeting the corridor with headroom
  (`profiles-am-kennedy.json:26-62`).
- **Horizon guard**: mkod REFUSES a demand program the run's horizon cannot
  finish executing — "nobody has ever wanted the first sixth of a demand
  program" (901-921) — and warns when the run outlasts the program without
  an explicit drain.
- **Realized-demand self-reporting**: every total is computed from the
  slices AS WRITTEN (`peak_rate` = max over time of the summed rate on
  elementary intervals, 408-434; `total_veh` = integral, 437-444; header
  lines 1279-1342 record realized peak, vehicle count, knobs, and the
  ordered assign rules). This is the ADR-0028 correction: counters used to
  report the AUTHORED demand, four distinct errors all in one direction
  (`docs/kb/decisions/ADR-0028-demand-profile-library.md:123-175`). The
  header doctrine: "Read the demand level here, not off the flags" (1231-1239).

**Supporting tools:**
- `scripts/chicago/scaledemand.py` — the demand-side half of a what-if:
  scale flows by class/id/origin/corridor, or strip trucks (80-121). Its
  docstring carries the honest caveat: lowering demand "will always look
  good on a speed metric because the network is carrying less… an 'upgrade'
  that raises speed by admitting fewer vehicles is visible only if you look
  at both" (14-23).
- `scripts/chicago/mkzones.py` — lane→district midpoint tiling (50-82)
  feeding both congestion reporting and `--dest-zones`. The pairing is
  deliberate: mkzones exists because "Two questions need districts and
  neither can be asked without them" (docstring, `scripts/chicago/mkzones.py:9`),
  and ADR-0030 states the doctrine — "'where is congestion' and 'where is
  everyone going' have to be asked in the same coordinates"
  (`docs/kb/decisions/ADR-0030-run-report-protocol.md:79-80`).

A shipped artifact for scale: `data/scenarios/chi-loop-urban-half-kennedy/
demand/main.yaml` is 136,529 lines — 317 flows (109 portal + 148 residential
+ 60 interior ramp flows), all 317 with destination distributions, 208 with
`offset_m`; header: target 8,000, realized peak 13,552 veh/h at t=1800 s,
11,224 vehicles over the program, knobs `--total 8000 --freeway-scale 2.5
--ramp-share 0.6 --ramps-per-corridor 12`, 400 workplace + 80 exit
destination lanes.

---

## 7. The demand-side control loop: perimeter metering (ADR-0039, in tree)

`docs/kb/decisions/ADR-0039-perimeter-demand-metering.md` (accepted
2026-08-06, implemented uncommitted in the working tree) connects demand to
network state — the first and only feedback from congestion back into
admission:

- **Context measurement**: the network drains but injection peaks at 12.8k
  veh/h against a measured maximum discharge of ≈5.7k/h; the drain2 MFD is
  "textbook" — discharge crests at ≈5.7k/h around 4,800-5,100 active
  vehicles (≈2.2 veh/km) and bends down the congested branch
  (`docs/kb/decisions/ADR-0039-perimeter-demand-metering.md:16-29`).
- **The gate already existed**: `DensityTargetPerKm` caps injection
  network-wide for both paths; ADR-0039 adds hysteresis (resume floor,
  default 0.9×cap), honest gate accounting (gate-held veh·ticks, gate-caused
  expiry share), and — iteration 3b, revised by measurement — a separate
  bounded deferral clock (`gate_hold_max_s`, default 1800 s), because the
  drain4 pair showed gate-held time counting toward the 60 s expiry
  "DELETED trips after 60 s at the gate… completed fell 1:1 with
  suppression. Real perimeter control queues demand for many minutes"
  (`docs/kb/decisions/ADR-0039-perimeter-demand-metering.md:63-82`;
  `engine/director.go:32-62`, 310-345; `engine/spawn.go:318-357`).
- The gate bit is keyframed (TSKF v8, only while engaged) because inside the
  [floor, cap) band it is not a function of current density
  (`engine/spawn.go:318-336`).
- **Deferred demand-model alternatives, all demand-relevant** (lines 95-114):
  per-origin bounded waits; per-portal ALINEA ramp meters ("the real Chicago
  analog (Kennedy/Dan Ryan meters exist)"); **workplace-parking capacity**
  ("today's 400 workplace lanes are infinite sinks, which is why 33% of
  demand piles into the core unconditionally. A demand-model change with
  keyframed state: its own ADR"); core-district density as the gate signal.

Note the doctrinal line ADR-0028 drew, which bounds what demand feedback may
ever do: demand-as-a-function-of-live-state was REJECTED — "it breaks replay
determinism (ADR-0005)" and "two arms of an A/B would no longer share a
demand program" (composite of two separate costs in the same paragraph,
`docs/kb/decisions/ADR-0028-demand-profile-library.md:84-93`). The gate is
compatible because it is engine-side deterministic state, not a director
callback.

---

## 8. The recorded calibration gaps (carried into the analysis)

1. **"Demand does not congest the expressways"** (open validation gap,
   `docs/kb/articles/gaps-and-roadmap.md:191-199`): six of the ten
   real-Chicago hotspot corridors are in the extract and all six ran free —
   "Kennedy 72.1 km/h against a real 19.1 mph peak" — carrying 3.3% of
   network delay while the arterial grid took 94.7%. Structural cause:
   `--total` scales every class by one factor; "reaching the class rate
   would need --total ≈ 67,000 — which buries the grid. One scalar cannot
   congest both; freeway portals need their own knob." Partially mitigated
   since: `--freeway-scale` / `--corridor-scale` / `--ramp-share` /
   per-corridor profile rules (§6b). The chi-loop-od-30m README measured the
   first fix: scaling only freeway portals ×4.15 moved expressways from 4.7%
   to 52.9% of network delay and landed the Eisenhower within 4% of its real
   ATRI peak truck speed (`data/scenarios/chi-loop-od-30m/README.md:97-136`).
   Still unsolved at the per-portal level: the Kennedy has 681 lanes inside
   the crop but only 2 boundary origin lanes — "it needs either more origin
   lanes exposed by a wider crop, or an explicit per-corridor rate"
   (`data/scenarios/chi-loop-od-30m/README.md:121-129`).
2. **"71% of building footprints are bare building=yes"** → residential mass
   under-counted (`docs/kb/articles/gaps-and-roadmap.md:203-204`;
   `scripts/chicago/buildings.py:39-59`; ADR-0021 open items,
   `docs/kb/decisions/ADR-0021-od-demand-buildings.md:414-419`). Origin-side
   bias only; workplace side is comparatively well-tagged.
3. **No distance deterrence anywhere.** Destination weight = floor area,
   period (plus zone pins and reachability). There is no trip-length
   distribution, no impedance function, no gravity/radiation term. A vehicle
   born at a portal is as likely to be headed for the farthest workplace
   lane as the nearest one of equal floor area. (No KB note records this
   gap; see synthesis.)
4. **No mode split.** vTypes are car/truck mix only; the model is 100%
   road vehicles. The README caveats say it: "Downtown Chicago's transit
   share is very high" (`data/scenarios/chi-loop-od-30m/README.md:157-167`;
   `scripts/chicago/mkod.py:26-33`). Links to domain-multimodal-transit.
5. **No residential street grid in the extract** (class filter at import):
   107 residential + 108 service lanes of 23,833; "residents pull out onto
   arterials they do not actually front" — residential snap median 46.5 m vs
   10.4 m for workplaces (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:408-413`).
6. **Rates are not calibrated — by declaration.** Every layer says so:
   mkod docstring (`scripts/chicago/mkod.py:26-33`), profiles ("Fractions
   are napkin-anchored to the usual diurnal shapes, NOT calibrated counts",
   `scripts/chicago/profiles-am.json:3-16`), ADR-0028 consequences
   (`docs/kb/decisions/ADR-0028-demand-profile-library.md:116-118`), and the
   scenario README (`data/scenarios/chi-loop-od-30m/README.md:157-167`).
   The chicago-metro article names the intended first real calibration
   target: "TGSIM I-90/94 per-lane rates are the first real calibration
   target" (`docs/kb/articles/chicago-metro.md:276-278`).
7. **Arrival granularity**: trips end at the END of the destination lane,
   not at the building's snapped offset — "At city block lengths the error
   is tens of meters" (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:388-392`).
   And "No return trips / no activity chains… the evening reversal means
   re-running with the OD matrix transposed" (394-397).

---

## 9. Connections within the codebase

- **ADR-0036 (adaptive routing)** is the assignment half of the demand
  story: all-or-nothing static routing meant "Every vehicle sharing an OD
  pair takes the identical path no matter how loaded it is"
  (`docs/kb/decisions/ADR-0036-congestion-adaptive-routing.md:20-27`).
  Demand shapes the congestion; routing decides whether the fleet spreads
  around it. Its measured limit belongs in demand calibration: "at ~2x
  sustained oversaturation adaptive does NOT prevent the dead-stop…
  rerouting spreads congestion, it does not create capacity" (INDEX summary).
- **ADR-0030 (run report) + mkzones** supply the validation coordinates:
  distributions over space-time cells, per-district and per-corridor tables,
  in lane-km and VMT shares (`docs/kb/decisions/ADR-0030-run-report-protocol.md:35-62`).
  This is the machinery a weight-calibration protocol would plug into.
- **ADR-0014 TripRecord** gains meaning from real trip ends: "completed-trip
  travel times become comparable quantities instead of 'time until the
  vehicle wandered off the map'" (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:339-342`).
- **Silent fidelity failures** article: two of the eight failure modes are
  demand-side (portal capacity drop; origin-queue vehicle-time inside the
  metric window) (`docs/kb/INDEX.md:46`).
- The mission log's own diagnosis: "Our demand director currently fires
  every scheduled arrival as a verb regardless of network state… the
  aggregate demand-vs-capacity mismatch is where the ceiling lives"
  (`docs/chicago-throughput-log.md:37-55`) — the line of thinking ADR-0039
  closed for admission control and that remains open for demand LEVELS.

## Open questions from the code

- ADR-0021's open items are all still open: arrival at an offset; interior-
  origin expiry policy (a saturated street silently drops garage demand
  after 600 ticks — "the right policy (queue vs drop) is unexamined",
  `docs/kb/decisions/ADR-0021-od-demand-buildings.md:398-401`); load-time
  destination reachability validation (402-407).
- The `demand_schedule`/`DemandStep` global multiplier (`engine/spawn.go:53-58`)
  predates slices and applies only to the spawner — is it dead grammar in
  the scenario era?
- `building=tower` (Trump Tower) is in neither classification list and drops
  out entirely (`docs/kb/decisions/ADR-0021-od-demand-buildings.md:414-419`).
