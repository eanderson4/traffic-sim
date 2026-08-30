# Implementation: Multimodal Transit (umbrella — trains + buses)

> Source: codebase tracing | Researched: 2026-08-24 | Git HEAD: 2bc98de

Scope note: this is a greenfield topic. "Implementation" here means (a) the
current state of everything transit would touch, and (b) the seams where
transit would attach, with WHY analysis per seam. Mode-specific mechanics
(block signaling, interlockings, dwell models, bunching, berth capacity) are
deliberately excluded — they belong to the sibling topics
`domain-rail-operations` and `domain-bus-operations`.

Working-tree note: HEAD is 2bc98de, but the tree carries UNCOMMITTED work
implementing ADR-0039 (perimeter demand metering): `engine/spawn.go`,
`engine/director.go`, `engine/engine.go`, `engine/keyframe.go`,
`engine/scenario/scenario.go`, `engine/metricsjson.go`,
`engine/cmd/serve/main.go` are modified, and
`docs/kb/decisions/ADR-0039-perimeter-demand-metering.md` +
`engine/densitygate_test.go` are untracked. Line numbers below are the
working-tree numbers, which for those files differ from HEAD.

---

## 0. VISION status: transit is currently a named non-goal

`docs/VISION.md:96-101` lists under "## Non-Goals (for now)":

```
- Photorealistic 3D or driving-game physics (lane-level fidelity is the bar)
- Continuous within-lane vehicle dynamics (swerving)
- Cloud-scale deployment for the episode
- Pedestrians, cyclists, transit (future candidates, keep the door open)
```

So VISION:101 names **transit** (with pedestrians and cyclists) as a
"future candidate, keep the door open" item. Promoting it requires a VISION
amendment — and the doc's own header (`docs/VISION.md:3-6`) says vision
changes must be recorded as a decision record. The natural split the KB
registry already encodes (`docs/kb/INDEX.md:75-79`): trains + buses IN,
pedestrians/bikes/scooters stay OUT.

WHY this matters beyond bookkeeping: the "keep the door open" clause is
exactly what the current import pipeline does NOT do (§1) — the door is
closed at the earliest stage (network compile), not at the vehicle or
controller stage. Every later seam (types, controllers, metrics) is far
more open than the network seam.

---

## 1. Network import: the class filter — where transit currently dies

### 1.1 The filter

`engine/netimport/netimport.go:80-88`:

```go
// motorClasses are the SUMO vehicle classes our engine can carry; lanes
// restricted away from all of them (sidewalks, cycle tracks, rail) are
// skipped. An empty allow list means open to all (kept).
var motorClasses = map[string]bool{
	"passenger": true, "private": true, "bus": true, "coach": true,
	"delivery": true, "truck": true, "trailer": true, "motorcycle": true,
	"moped": true, "emergency": true, "authority": true, "army": true,
	"taxi": true, "hov": true, "evehicle": true, "custom1": true, "custom2": true,
}
```

`motorLane` (`engine/netimport/netimport.go:597-624`) evaluates SUMO's
allow-whitelist/disallow-blacklist against that set; `allLanesNonMotor`
(`engine/netimport/netimport.go:626-633`) applies it edge-wide. The filter
is APPLIED at `engine/netimport/netimport.go:198-209` (edges with no motor
lane are dropped wholesale; individual non-motor lanes on kept edges are
dropped singly) and `:257-259` (a kept-edge-that-loses-all-lanes is a hard
error). Dropped items are only auditable via the import report
(`engine/netimport/netimport.go:45-46`: `SkippedEdges` — comment says
"non-motor edges (rail-only etc.)", `SkippedLanes` — "non-motor lanes on
kept edges (sidewalks, bike lanes)").

### 1.2 What the filter discards — measured on the real Chicago import

On `data/networks/chi-loop-urban/loop-urban.net.xml` (the SUMO source for
the current flagship network), grepping lane permission attributes:

- **185 lane records carry `allow="bus bicycle"`** (a whitelist): 112 on
  junction-internal edges, 73 on ordinary edges — these are the Loop's
  real bus lanes (Loop Link and friends). `motorLane` returns true for
  them (`bus` ∈ motorClasses), so they are KEPT — but as ordinary lanes.
  The exclusivity information is discarded at this point and never
  reaches the compiled network.
- **Zero rail-whitelist lanes exist in the file.** The CTA L is simply not
  in this .net.xml — the Chicago extract keeps only `highway=*` ways
  (`scripts/chicago/extract.py:53-54`, mechanism in §1.3), so the
  question "does netimport drop rail?" is moot for current imports: rail
  never even reaches the filter. (The 5 skipped edges in
  `data/networks/chi-loop-urban/import-report.json` are bicycle-only lanes,
  e.g. OSM edges 421091626/639931328 verified `allow="bicycle"`.)
- Junction-internal lanes mostly carry a `disallow` BLACKLIST of
  non-road classes (`disallow="pedestrian tram rail_urban rail ...
  drone"` — 22,862 records, plus 5,240 without `pedestrian`), which
  `motorLane` keeps (a blacklist that never names a motor class leaves
  every motor class open, `engine/netimport/netimport.go:608-623`);
  2,728 internals carry the road-class allow whitelist and 112 the
  `bus bicycle` whitelist. (A naive `grep 'allow="'` reports 38,761 /
  10,217 for those non-motor class lists — a substring artifact:
  `allow="` matches inside `disallow="`. Those values are blacklists,
  not whitelists; verified by attribute-aware parsing.) The point for
  transit: permission semantics in .net.xml are richer than "motor or
  not", and the import today reduces them to a boolean keep/drop.

WHY the filter exists: the engine has exactly one occupancy model —
vehicles of a `VehicleType` on lanes (`engine/vehicle.go:48-58`) — and no
concept of a mode that cannot mix into the same car-following space.
Dropping rail/sidewalk/bike lanes was the correct v1 call (fewer lanes,
smaller conflict sets, no dead graph elements), but it was made as a
PERMANENT reduction: the compiled network format
(`contracts/network-format-v1.md:67-79`) has no per-lane class/permission
field at all, so the information cannot be recovered downstream. The
contract doc itself acknowledges the side effect at
`contracts/network-format-v1.md:69`: "a filtered-out lane (sidewalk, bike
lane) never links across" — lateral Left/Right chaining skips dropped
lanes.

### 1.3 The seam for transit

The layer map's bottom rung is therefore: **per-lane class masks in the
compiled network** (who may occupy this lane) + **an import that preserves
the SUMO allow/disallow sets instead of booleanizing them** + **a rail
ingestion path**. Where rail currently dies (verified 2026-08-24): the
OSM extract, not the typemap — `scripts/chicago/extract.py:53-54` keeps
only ways with a `highway=*` tag (`hwy = way.tags.get("highway")`;
`railway=*` ways never enter the .osm netconvert consumes), while the
ADR-0022 typemap itself defines 7 railway types
(`scripts/osm-urban-us.typ.xml`: railway.rail, railway.subway,
railway.light_rail, railway.tram, ...). So re-enabling rail is an
extract-query + netconvert-option exercise, not new typemap work.
Everything upstream (OSM ways for the L exist — `railway=rail` on
the Loop structure) is recoverable; the loss is purely in our pipeline.
Bus lanes are the cheapest possible first step: the information is ALREADY
in the .net.xml we already parse (`xmlLane.Allow`/`Disallow` are read at
`engine/netimport/netimport.go:121-122` and then thrown away).

---

## 2. Vehicle model: multi-class exists; persons do not

### 2.1 What a vehicle type is today

`engine/vehicle.go:5-17`:

```go
// VehicleType carries the per-class parameters of ADR-0007: geometry
// (length, width) plus IDM dynamics (s0, T, a, b, v0). Types mix freely in
// one lane; the bumper-to-bumper gap convention keeps every pair unambiguous.
type VehicleType struct {
	Name   string
	Length float64 // m
	Width  float64 // m
	S0     float64 // jam gap, bumper-to-bumper (m)
	T      float64 // desired time headway (s)
	A      float64 // max acceleration (m/s²)
	B      float64 // comfortable deceleration (m/s²)
	V0     float64 // desired speed (m/s)
}
```

Two built-ins: `Car` (`engine/vehicle.go:33`) and `Truck`
(`engine/vehicle.go:40`). ADR-0007 §3
(`docs/kb/decisions/ADR-0007-vehicle-model.md:29-32`) ratified multi-class
types as first-class — "types mix freely in one lane". A `Vehicle` carries
`Type *VehicleType` + `TypeIdx` (`engine/vehicle.go:50-51`; TypeIdx is
"index into the scenario type list (canonical for CRC)").

There is **no occupancy, capacity, or persons field anywhere**: a
case-insensitive, word-bounded grep for
`\bpassenger\b|persons|\boccupant(s)?\b|riders` over `engine/` returns
exactly ONE hit — the SUMO class name in `motorClasses`
(`engine/netimport/netimport.go:84`), i.e. import vocabulary, not a
vehicle attribute.

### 2.2 The type registry is a hardcoded two-entry map

Scenario `types:` names resolve through a registry passed INTO the scenario
package — `Scenario.RunSpec(typeReg map[string]*engine.VehicleType)` at
`engine/scenario/scenario.go:404-417` (unknown name → load error listing
known types). Both callers hardcode the same two entries:

- `engine/cmd/serve/main.go:154`:
  `typeReg := map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}`
- `engine/cmd/simrun/main.go:66`: same map inline.

Spawn-time resolution goes through the scenario type list: director verbs
name a vtype string, `EnqueueSpawn` maps it to the index and rejects
unknowns (`engine/director.go:225-230` — the test at
`engine/director_test.go:37` literally uses `TypeName: "bus"` as the
unknown-type rejection case). The demand director draws types per vehicle
from the flow's `vtypes` weight map (`engine/natsio/demand/director.go:455`
→ `pickType` `:463-473`, drawing over the SORTED key list for float
determinism).

WHY this is friendly to transit: adding a `Bus` type is a one-line
registry addition plus dynamics calibration; the whole spawn/CRN/wire path
is already type-parameterized. The deferred design surface is the ADR-0012
M11 note (`docs/kb/decisions/ADR-0012-scenario-format.md:281-282`):
"vehicle-type DEFINITIONS as part files (`vtypes/*.yaml`) — the hash
currently covers type names, and the IDM parameters behind them ride on
the engine version." Transit types (bus, L car) are the forcing case that
makes `vtypes/*.yaml` worth building — and where an `occupancy` /
`persons_capacity` attribute would live, keeping the kernel's CRC'd
dynamics table clean of presentation-only fields (see §5 for the metric
side of that choice).

### 2.3 Class on the wire

The live snapshot frame (TSSF v1) carries per vehicle
`id u64 | x f32 | y f32 | angle f32 | class f32`
(`engine/natsio/frame.go:41`), where class is the vehicle's TypeIdx
(`engine/natsio/frame.go:120`). Controller observation frames carry TypeIdx
too (`engine/natsio/obsframe.go:88,105`, range-checked at `:312-313`,
re-linked against the run's type table at `:407-408`). So **class identity
already reaches every external client** — a bus-detecting TSP controller
or a transit-only metrics consumer can tell a bus from a car today,
provided a bus type exists in the run's type list.

---

## 3. Controller contract: transit's natural home

ADR-0008 (`docs/kb/decisions/ADR-0008-controller-contract.md`):

- **4-axis intents** (:23-28): longitudinal / lateral / routing / signal,
  one Intent per vehicle per tick, absent = no change. Per-axis persistence
  table at :29-40.
- **Roles** (:53-70): ordinary controllers; the external default driver
  (IDM+MOBIL reference fleet, handoff/orphan re-claim); the **director**
  (elevated grants: spawn/despawn/teleport/trigger — "the OpenSCENARIO
  verbs", :62-64); **signal controllers** (elevated grants over actuation,
  :65-70 — the MMU pattern: engine enforces conflict matrix, min greens,
  clearance regardless of commands).
- **Zero driving logic in the engine** (:71-90): failover is operational,
  uncontrolled vehicles bridge on hold-last.
- M10 clarification (:128-149): the verb channel is request/reply with
  director-grant enforcement, idempotency by request_id; **v1 verb
  vocabulary is `spawn` only**; unknown verbs are rejected, never silently
  ignored (:139-140).

WHY for transit: the architecture already answers "who drives the buses?"
— an external fleet controller, exactly like the default driver, claiming
bus vehicles and emitting the same 4-axis intents (stops become a
controller-level behavior: a held accel=0 plus dwell logic, or a new
director verb if engine-arbitration is wanted). The `spawn`-only verb
vocabulary is the current ceiling: anything a transit layer needs beyond
spawn (e.g. a `hold`/`dispatch` verb for schedule control, or
stop-dwell as an engine-acknowledged state) is an additive verb on an
existing channel — the ADR-0037 `signal_set` precedent (§6) shows exactly
how a new verb lands (channel, idempotency, record plane, keyframe) without
touching the intent axes.

Replay consequence (ADR-0008 :92-101): replay never re-runs controllers;
recorded intents/verbs are re-applied. A transit layer implemented as
external controllers inherits bit-exact replay for free. A transit layer
implemented in-kernel would NOT — it would be new CRC'd state, new
keyframe sections, and a replay-divergence surface.

---

## 4. Demand grammar: flows exist; lines/stops/schedules do not

### 4.1 The scenario surface

Manifest (`engine/scenario/scenario.go:62-74`): identity, seed, ticks,
params (`dt`, `adaptive_routing` — `:78-85`), `network`, `types`,
`spawner`, and part lists `demand`/`control`/`metrics`. The spawner block
(`:90-104`) carries the ADR-0039 gate fields (§7).

Demand parts (`engine/scenario/scenario.go:106-152`): a `DemandFile` is
`flows: []Flow`. A `Flow` (`:122-144`) is origin-anchored: `origin` lane
id, `veh_per_h` or piecewise-constant `slices` (`:146-152`, SIM SECONDS —
ADR-0012 §3, `docs/kb/decisions/ADR-0012-scenario-format.md:65-83`),
`spacing` ("uniform"/"poisson"), `vtypes` weight map, `until_s`, an
ADR-0021 weighted `destinations` lane map, and `offset_m` for
interior-origin injection. ADR-0012 §3 (:65-83) ratified: demand is
sampled at runtime by the director, replay never re-runs the sampler,
"7:30 AM" is a presentation alias for t=27000 — **no wall-clock times in
demand** (permanent refusal, ADR-0012 :195-199).

### 4.2 The runtime sampler

`engine/natsio/demand/director.go:1-28` (package doc): the director reads
the scenario demand files, samples arrivals with per-vehicle keyed RNG
(stream key = (seed, flowKey^ordinal), `:369-399`), and issues spawn verbs
with deterministic request ids (`f{flow}-{ordinal}`, `:345-353`). Draw
order gap → vtype → destination (`:419-422`). `serve` embeds it whenever a
scenario declares demand parts (`:25-27`; simrun refuses them,
`engine/cmd/simrun/main.go:62-65`).

### 4.3 The seam for transit

Nothing in the demand grammar can express: a LINE (an ordered stop
sequence with a path), a STOP (a position on a lane where specific
vehicles halt), a SCHEDULE (absolute arrival/departure times per trip),
or a HEADWAY program tied to a line rather than an origin. GTFS's four
core nouns (stops/routes/trips/stop_times) map onto none of these — the
closest is Flow:slices (a rate program), which is GTFS `frequencies.txt`
semantics minus the line identity. So transit demand is a NEW part type
(e.g. `transit/*.yaml` compiled from GTFS — see standards-and-patterns.md
§1) plus a new controller (§3), NOT an extension of Flow. The scenario's
hash-everything discipline (ADR-0012 §6) then covers transit programs the
same way it covers demand: content-hash, seed identity.

Stations as DEMAND nodes (park-and-ride spawns, alighting-generated car
trips) would reuse the existing Flow machinery with origins near stations
— a demand-modeling question (sibling topic `domain-demand-modeling`),
not a transit-mechanics one.

---

## 5. Metrics: vehicle-weighted today; person-weighting has clean attach points

### 5.1 The primitives (ADR-0014)

`docs/kb/decisions/ADR-0014-observability-metrics.md:46-66` ratified two
primitives — per-vehicle **trip records** and lane-interval **Edie q/k/u**
— with everything else as derived views. In code:

- `TripRecord` (`engine/metrics.go:126-154`): `VehicleID`, **`TypeName`**
  (:138), origin/dest lanes, entry/exit ticks, `DistanceM`, `TimeLossS`,
  `Stops`, `StoppedTimeS`, `Completed`, `Stranded`. The class label is
  already on every record — per-class aggregation (bus vs car travel
  times) is a FILTER, not a schema change.
- `MetricGroups` (`engine/metrics.go:35-40`): `Edie`, `Occupancy`,
  `Stops`, `TimeLoss`; the default set enables all four at 900 s
  (`engine/metrics.go:84-90`).
- `IntervalRecord` fields (`engine/metrics.go:118-124`): Q/K/V pointers,
  `Occupancy *float64` (:121) — "time-weighted space fraction (§3)".
  ADR-0014 §3 pins occupancy as `Σ(time_present × vehicle_length)/(T×L)`
  (`docs/kb/decisions/ADR-0014-observability-metrics.md:83-85`) — **this
  is detector-vocabulary occupancy (fraction of space-time covered),
  NOT persons aboard**. It is accumulated from `dt*v.Type.Length`
  (`engine/metrics.go:493,512,543`).

### 5.2 No persons concept exists

Verified negative (see §2.1): the word-bounded grep finds no
`persons`/`occupant`/`riders` anywhere in `engine/`. Trip records,
interval records, totals
(`engine/metrics.go:180+`), denied-entry (`engine/metrics.go:156-171`),
and the JSON sink (`engine/metricsjson.go`) are all vehicle-denominated.

WHY this is the bus-lane-story-flip hazard: every current run total
answers "how did VEHICLES do". The classic transit-priority evaluation
re-weights by occupancy — a bus carrying 40 people who each save 5 s
outweighs 5 cars losing 30 s each. Person-weighting needs (a) a persons
attribute per vehicle (static per type, or dynamic per vehicle), and (b)
a weighting step at aggregation time. Because trip records carry TypeName
and the kernel is a read-only observer (ADR-0014 §1,
`docs/kb/decisions/ADR-0014-observability-metrics.md:32-44` — never feeds
back into world state, replay re-derivation is a validity check), a
person-weighted view can be built as a DERIVED layer with zero CRC/replay
impact — the same precedent ADR-0014 used for metrics as a whole. The
Edie side has an analogous move: person-delay per lane-interval =
time-loss × occupancy-weight per class, derivable from the same per-tick
observations if the kernel accumulates a per-class breakdown (today it
does not — class enters only via trip records).

Contract note: metric definitions are pinned in `contracts/asyncapi.yaml`
(ADR-0014 §3, :68-104) and changing one is a contract change requiring an
ADR note (AGENTS.md §5). Adding person-weighted FIELDS is additive; the
ADR-0039 metrics addition (`suppressed`, `gated_expired` — optional,
omitempty, `engine/metricsjson.go:81-87`) is the working template for how
to extend the metrics JSON without breaking old recordings.

---

## 6. Signal control: the ADR-0037 seam is TSP-ready — and detectors already see class

ADR-0037 (`docs/kb/decisions/ADR-0037-runtime-signal-control.md`)
implemented runtime signal control as a director VERB (`signal_set`,
:32-41 design, :85-131 implementation): per-phase commands with a bounded
hold (default one cycle, clamped at a cumulative 300 s chain bound),
installed through ONE derivation point (`sigPhaseAt`), with enforcement
(`sigGate`, clearance, permissive yield, box checks) untouched. Milestone
2 (:407-465) shipped the reference actuated controller
`engine/natsio/sigctl`.

The M2 controller's detector model is the TSP hook
(`engine/natsio/sigctl/sigctl.go:9-16`): detectors are VIRTUAL stop-line
loops — presence zones (default 25 m) around each signal link's stop line,
read off TSSF snapshots. And TSSF carries class (§2.3). So the pattern for
TSP is already three-quarters built: an external controller that (a)
selects for `class == bus` (or trains, on their own lanes) in its detector
zones, (b) issues `signal_set` holds/extensions for approaches with an
incoming transit vehicle, (c) inherits the starvation rails and lapse
events for free, and (d) rides the record plane verbatim for replay. What
does not exist: any class-filtering in sigctl (it counts every vehicle),
any priority-request concept in the verb vocabulary (a `signal_set` hold
IS the priority actuation, but there is no "request with justification"
payload — compare NTCIP 1211's priority-request model in
standards-and-patterns.md §4), and the deferred live-plane feedback echo
(ADR-0037 M2 notes: the controller predicts lapses from its own history —
fine for the single-commander reference shape a TSP controller would also
be).

The kernel's fixed-time baseline being a pure function of the tick
(`docs/kb/decisions/ADR-0037-runtime-signal-control.md:14-16,
212-214`) means a TSP-off control arm costs zero engine work — the A/B
bracket pattern is `serve` vs `serve -tsp`, exactly the
`scripts/sigctl-bracket.py` precedent (`:489-498`).

---

## 7. Perimeter demand metering (ADR-0039, uncommitted): the transit-exemption hook

ADR-0039 (`docs/kb/decisions/ADR-0039-perimeter-demand-metering.md`,
accepted 2026-08-06, implementation in this working tree) adds a
density-cap perimeter gate with hysteresis and honest accounting:

- The gate: `Engine.densityGateHold()` (`engine/spawn.go:337-357`) —
  engage at `DensityTargetPerKm`, release below `DensityResumePerKm`
  (default 0.9×cap). The gate bit is engine state (`engine/engine.go:226`),
  keyframed in TSKF v8 only while declared-and-engaged
  (`engine/keyframe.go:219-227`; read path `:690-707` with mid-band
  `RestoreNotice`).
- The deferral clock (`engine/director.go:32-72`): gate-held ticks do NOT
  count toward the 600-tick `DirectorSpawnHoldTicks` origin-blockage
  window — the gate DEFERS, bounded by `GateHoldMaxS` (default
  `DefaultGateHoldMaxS = 1800.0` s, `engine/director.go:57-62`) — because
  the drain4 pair measured that delete-at-60 s cost completions 1:1 with
  suppression. Real perimeter control queues demand for many minutes
  (ADR-0039 :70-74).
- Manifest fields: `spawner.density_per_km`, `density_resume_per_km`,
  `gate_hold_max_s` (`engine/scenario/scenario.go:90-104`), hashed into
  scenario identity per ADR-0012.
- Honest accounting: `suppressed` (gate-held veh·s) and `gated_expired`
  in the demand block (`engine/metricsjson.go:81-87,113-117`).

WHY for transit: real-world perimeter control (San Francisco, Minneapolis,
Zurich practice) typically EXEMPTS transit vehicles — buses jump the
meter; that exemption is part of why the policy is transit-favorable. In
our gate, an exemption is a class check at the hold decision
(`densityGateHold` is called per injection — spawner path
`engine/spawn.go:244`, director path `engine/director.go:304`), i.e. a
one-line policy delta once a bus class exists — but ONLY if the gate's
accounting then separates "suppressed car trips" from "admitted bus
trips", which the current veh·s counters do not disaggregate by class.
This is the concrete, in-flight example of the topic's recurring pattern:
**the machinery is mode-aware in principle and class-blind in practice**;
each blind spot is small, additive, and currently unmeasured.

---

## 8. Adjacent surfaces transit would touch (checked, no blockers)

- **Routing (ADR-0036)**: per-lane travel-time EMA + next-hop tables are
  computed over all vehicles uniformly. Class-restricted lanes (bus lanes)
  would need the next-hop computation to be class-aware (a bus's graph
  differs from a car's) — today there is one graph for everyone.
  `engine/vehicle.go:69` `Route` is a destination lane id; routing
  resolves through the kernel's tables.
- **Gridlock escape (ADR-0034)**: `StrandAfterS` = 300 s default. A
  dwelling transit vehicle is indistinguishable from a stuck one to this
  escape — dwell behavior must either stay under 300 s per stop, be
  exempted by class, or be modeled as a recognized state. Flagged for
  `domain-bus-operations` / `domain-rail-operations`; noted here because
  it is a correctness interaction, not a mechanic.
- **Scenario variants (ADR-0012 §4/M12)**: overlays patch demand flows by
  id and add parts; a transit part type would inherit the addition-only
  composition rule. A "new bus lane" is a NETWORK delta (ADR-0009 patch
  grammar, deferred) or a re-import — the class-mask work (§1.3) decides
  which.
- **Viz**: class-based styling keys off the snapshot class field; a bus/L
  class renders distinctly with no contract change (the 3D fleet topic
  `integration-3d-vehicle-fleet` already lists bus/L-car models as
  targets, `docs/kb/.kb-meta.json` integration-3d-vehicle-fleet hints).

---

## 9. Summary: what exists, what's missing, where it attaches

| Layer | Exists today | Missing for transit | Attach point |
|---|---|---|---|
| Network lanes | Lane graph w/ speed limits, conflict sets, signals | Class masks; rail lanes; stop infrastructure | netimport allow/disallow preservation (§1) |
| Vehicle | Multi-class types (car/truck), CRC-canonical TypeIdx | Bus/L types; persons attribute | type registry + `vtypes/*.yaml` (§2) |
| Control | 4-axis intents, director verbs, signal_set | Transit fleet controller; stop/dwell behavior; priority requests | external controllers per ADR-0008 (§3, §6) |
| Demand | Origin flows, slices, vtype mix, OD weights | Lines/stops/schedules (GTFS nouns) | new `transit/*.yaml` part + compiler (§4) |
| Metrics | Trip records w/ TypeName; Edie q/k/u; detector occupancy | Persons weighting; per-class interval breakdowns | derived metric layer, additive fields (§5) |
| Policy | Perimeter gate w/ honest accounting | Class-aware gate (transit exemption) | one policy line + class-split counters (§7) |
| Replay | Intent/verb log, keyframes, CRC | (nothing — if transit stays external) | keep transit out of the kernel (§3) |

The dominant pattern: **the architecture's external-controller and
derived-metrics disciplines mean transit can be almost entirely additive
— new types, new controllers, new scenario parts, new derived metrics —
with the kernel touched only for per-lane class masks and (optionally)
class-aware routing/gating.** The one genuinely kernel-adjacent decision
is the network class mask, because it changes what a lane IS in the
compiled contract (`contracts/network-format-v1.md`) — a format-versioned
change under AGENTS.md §5.

**Source files traced:** `engine/netimport/netimport.go`,
`engine/vehicle.go`, `engine/scenario/scenario.go`,
`engine/natsio/demand/director.go`, `engine/director.go`,
`engine/metrics.go`, `engine/metricsjson.go`, `engine/spawn.go`,
`engine/keyframe.go`, `engine/engine.go`, `engine/natsio/frame.go`,
`engine/natsio/obsframe.go`, `engine/natsio/sigctl/sigctl.go`,
`engine/cmd/serve/main.go`, `engine/cmd/simrun/main.go`,
`contracts/network-format-v1.md`, `docs/VISION.md`,
`docs/kb/decisions/ADR-0007`, `ADR-0008`, `ADR-0012`, `ADR-0014`,
`ADR-0037`, `ADR-0039`, `data/networks/chi-loop-urban/`
(loop-urban.net.xml, import-report.json).
