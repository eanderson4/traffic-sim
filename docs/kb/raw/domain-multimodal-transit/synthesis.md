# Synthesis: Multimodal Transit (umbrella — trains + buses)

> Researched: 2026-08-24 | Git HEAD: 2bc98de | Status: complete

## Summary

Transit in traffic-sim is architecturally foreshadowed but physically
absent: the engine's multi-class vehicle model, external-controller
contract, derived-metrics discipline, and ADR-0037 signal channel were
all designed in ways that admit buses and trains additively — while the
network import actively discards the one thing transit needs first
(per-lane class permissions), reducing SUMO's allow/disallow sets to a
keep/drop boolean and compiling Chicago's bus lanes away as ordinary
lanes. The field
splits into transit-as-vehicles (SUMO, Vissim, Aimsun) and
transit-as-mode-share (MATSim, SimMobility); the research converges on
building the first as external fleet controllers fed by a GTFS→scenario
compiler, and collapsing the second into authored, elasticity-justified
demand variants — never runtime mode-choice in the deterministic kernel.
Chicago is an unusually good target: CTA publishes full static GTFS plus
an official GTFS-RT (vehicle positions, trip updates) under a license
that permits derivative works with optional attribution, subject to a
"promoting public transportation" purpose clause our advocacy use fits.

## Source Files

- [Implementation trace](./implementation.md) — current seams:
  netimport class filter, vehicle model, controller contract, demand
  grammar, metrics, sigctl, ADR-0039 gate; with file:line throughout
- [Competitor analysis](./competitors.md) — SUMO, MATSim, Vissim,
  Aimsun, SimMobility, A/B Street; steal/avoid per tool
- [Standards & patterns](./standards-and-patterns.md) — GTFS, GTFS-RT,
  CTA feeds+license, TCQSM, TSP practice/NTCIP 1211, person-trip
  metrics, mode-share elasticity; each with comply/deviate/simplify

## Key Architectural Decision Candidates

### 1. Per-lane class masks in the compiled network

**Choice:** Add a per-lane class-permission mask to the compiled network
format (a `classes` field on lanes — network format v2), and make
netimport PRESERVE SUMO's allow/disallow sets instead of booleanizing
them through `motorClasses` (`engine/netimport/netimport.go:80-88,
597-633`). Rail lanes additionally need a separate ingestion path —
the CTA L never reaches the filter because the road-only .net.xml
pipeline excludes it (implementation.md §1.2).

**Why:** This is the only genuinely kernel-adjacent change transit
needs. Bus lanes, queue jumps, and rail guideways are all the same
primitive ("who may occupy this lane"), and 73 ordinary edges in
chi-loop-urban already carry `allow="bus bicycle"` lanes whose
exclusivity we currently compile away. The class field already exists on
the wire (TSSF `class`, `engine/natsio/frame.go:41,120`), so
enforcement, routing, and viz can all key off one mask.

**Trade-off:** a compiled-format change (format_version bump under
AGENTS.md §5/ADR-0012 §8 discipline), plus downstream semantics
everywhere a lane assumption hides: lane-change eligibility, routing
(ADR-0036's single next-hop table becomes per-class or mask-filtered),
conflict sets (a car foe is not a bus foe), spawn validation. The cheap
degenerate case — mask present, all lanes all-classes — keeps existing
networks bit-identical only if the default is carefully designed.

**Field context:** every competitor has this atom (SUMO allow/disallow,
Aimsun reserved lanes, A/B Street lane types — competitors.md).
Omission is the outlier. [Details](./implementation.md#1-network-import-the-class-filter--where-transit-currently-dies)

### 2. Transit as external fleet controllers, zero kernel driving logic

**Choice:** Buses (and later trains) are driven by external fleet
controllers over the unchanged ADR-0008 contract — claimed vehicles,
4-axis intents, stops/dwell as controller behavior, spawns via director
verbs or a transit director. TSP is a second controller issuing
`signal_set` verbs with class-aware virtual detectors (sigctl pattern,
`engine/natsio/sigctl/sigctl.go:9-16` + TSSF class).

**Why:** inherits bit-exact replay for free (ADR-0008 :92-101 — replay
never re-runs controllers), keeps the kernel stdlib-only and CRC-stable,
and dogfoods the contract the way the default driver does. The
`signal_set` precedent shows a new privileged verb landing end-to-end
(channel, idempotency, record plane, keyframe, rails) without touching
intent axes.

**Trade-off:** dwell/stop semantics as pure controller behavior means
the kernel cannot distinguish "dwelling bus" from "stuck vehicle" —
the ADR-0034 strand escape (300 s) and denied-entry accounting need
either class exemptions or a recognized dwell state; and a controller
crash pauses the fleet per the ADR-0008 failover rules (operationally
fine, but transit runs need the supervised-fleet posture).

**Field context:** TraCI is the anti-model (blocking barrier, 11×
slowdown — ADR-0008 :14-17); SUMO/Aimsun model stops as engine
infrastructure with until/duration semantics — simpler, but inside the
deterministic core, where every schedule feature becomes keyframed
state. [Details](./implementation.md#3-controller-contract-transits-natural-home)

### 3. Persons as an attribute + derived person-weighted metrics (not simulated persons)

**Choice:** a static persons-per-class occupancy attribute (home: the
deferred `vtypes/*.yaml` part, hashed into scenario identity), consumed
by a DERIVED metrics layer producing person-delay/person-throughput
tables alongside the vehicle-denominated ones. No person agents in the
engine.

**Why:** trip records already carry `TypeName`
(`engine/metrics.go:138`); the metric kernel is a read-only observer
whose derived outputs never touch CRC/replay (ADR-0014 §1); and DOT
practice itself uses average occupancies — the 1975 person-delay
definition, SCAG 2024 guidance, HCM evaluation framing
(standards-and-patterns.md §6). Average occupancy answers the policy
question (the bus-lane story flip) at a fraction of person-simulation
cost.

**Trade-off:** no crush-capacity effects, no boarding-driven dwell
coupling, no individual passenger itineraries — dwell realism then lives
in the transit controller's dwell model (sibling `domain-bus-operations`)
with occupancy as its INPUT, not a simulated output. Sensitivity over
ridership assumptions = scenario variants (cheap, explicit).

**Field context:** SUMO and MATSim both simulate persons (a second
simulation layered on the first); Vissim/Aimsun practice and every DOT
TSP evaluation use occupancy weights. We side with practice.
[Details](./implementation.md#5-metrics-vehicle-weighted-today-person-weighting-has-clean-attach-points)

### 4. GTFS→scenario compiler, offline, narrow scope

**Choice:** an offline tool (script, not engine code) that compiles CTA
GTFS — date-selected — into a new strict-YAML `transit/*.yaml` scenario
part: lines as ordered stop sequences with lane-matched positions,
trips with sim-second schedules (frequencies.txt → headway programs),
an honest unmapped-elements report. The scenario manifest gains a
`transit` part list; the part hashes like every other (ADR-0012).

**Why:** GTFS is the one universal source for lines/stops/schedules AND
CTA's native format; the import/compile boundary with a report is the
project's established pattern (netconvert→netimport+report, ADR-0009);
offline compilation keeps the engine stdlib-pure and the run
deterministic (GTFS wall-clock → sim-seconds re-basing happens once, at
compile time — the ADR-0012 wall-clock refusal stays intact).

**Trade-off:** map-matching GTFS stops/shapes to `(laneId, s)` is the
hard 20% (gtfs2pt.py needs three fallback modes to do it); CTA stop
positions are pole positions on the sidewalk, not lane positions; rail
lines have no lanes to match until decision 1's rail ingestion exists.
Scope guardrail: the compiler emits SUPPLY (lines, stops, schedules,
vehicle types) — ridership/occupancy assumptions ride as attributes,
not as simulated demand.

**Field context:** gtfs2pt.py (SUMO) and pt2matsim are the two working
prior arts; both confirm date selection and unmapped-element reporting
as first-order requirements. [Details](./standards-and-patterns.md#1-gtfs-static-schedule--the-transit-authoring-source)

### 5. VISION amendment: trains + buses in, peds/bikes stay out

**Choice:** amend `docs/VISION.md:96-101` to move "transit" (scoped:
scheduled road and rail transit — buses and trains) out of Non-Goals
into a use case (civic advocacy with person-weighted metrics), leaving
pedestrians and cyclists behind, and record the change as a decision
record per VISION's own header (:3-6).

**Why:** the KB registry and this research assume the split; the CTA
target city makes transit central to any credible advocacy story
(bus lanes, TSP, the L); and the engineering cost is now mapped and
mostly additive.

**Trade-off:** scope growth in a project whose failures have all been
ambition-shaped; the mitigation is the umbrella/sibling topic split
(this topic = system seams; `domain-rail-operations` /
`domain-bus-operations` = mechanics, each separately gated).

**Field context:** every advocacy-adjacent simulator (A/B Street most
loudly) converged on transit as the story that lands with cities.
[Details](./implementation.md#0-vision-status-transit-is-currently-a-named-non-goal)

### 6. Transit-priority policy hooks: TSP + gate exemption

**Choice:** two policy surfaces: (a) a TSP controller per decision 2
(conditional priority — schedule-late buses only — as controller
policy); (b) a class-aware perimeter gate so ADR-0039-style metering
can exempt transit, with gate accounting split by class
(`engine/metricsjson.go:81-87` counters are currently class-blind).

**Why:** these are the two highest-leverage real-world transit-priority
policies, and both attach to machinery that ALREADY exists or is
landing this month (ADR-0037 channel + TSSF class field; ADR-0039 gate
at `engine/spawn.go:337-357`). Conditional priority bounds the
side-street cost (TCRP Synthesis 83 practice); the 300 s starvation
chain bound is the kernel-side backstop.

**Trade-off:** class-aware gating/routing spreads a single-graph,
single-counter design into per-class variants — small each, but the
ADR-0036 routing epoch and ADR-0039 keyframe (TSKF v8) are both
format-sensitive surfaces. TSP inherits sigctl's deferred feedback gap
(no live-plane hold echo; self-tracking exact only for single
commander).

**Field context:** green extension/red truncation + conditional
priority is the canonical agency toolkit (standards-and-patterns.md
§4); transit-exempt metering is standard perimeter-control practice.
[Details](./implementation.md#6-signal-control-the-adr-0037-seam-is-tsp-ready--and-detectors-already-see-class)

### 7. CTA data posture: recipe-not-file, purpose clause on record

**Choice:** fetch CTA GTFS/GTFS-RT via scripts with recorded
(URL, date, zip hash) provenance; never vendor the data; quote the
Developer License Agreement's purpose clause in demo data credits.

**Why:** the license is revocable with a delete-on-termination clause
(§V) and a "sole purpose of assisting riders / promoting public
transportation" restriction (§I.1) — both manageable for us, but only if
the data is reproducible from source and the use is documented as
transit-promoting. Attribution is optional (§III.6); derivative works
are explicitly licensed (§I.1).

**Trade-off:** an API-key requirement for GTFS-RT means archived RT
validation corpora are user-supplied; and the purpose clause is a
judgment call that belongs in the pending license ADR's inputs.

**Field context:** same posture as ADR-0009's ODbL recipe-not-file for
OSM; transit.land indexes CTA as derivatives-allowed.
[Details](./standards-and-patterns.md#3-cta-data-offerings--license--the-target-citys-actual-feeds)

## Compare/Contrast: Our Planned Approach vs the Field

| Dimension | SUMO | MATSim | Vissim/Aimsun | **traffic-sim (planned)** |
|---|---|---|---|---|
| Transit supply model | Stops + until/duration schedules, engine-native | transitSchedule.xml, agent-served | Lines + timetables, engine-native | External fleet controllers + compiled `transit/*.yaml` part |
| Stop abstraction | busStop lane-area + personCapacity | Stop facilities w/ link mapping | Berth-capacity stops (curb/bay/terminal) | (laneId, s-range) annotation + controller dwell |
| Persons | Simulated (intermodal router) | Simulated (the core product) | Occupancy weights | **Occupancy attribute + derived person metrics** |
| Lane permissions | Runtime allow/disallow | Link mode restrictions | Reserved lanes | Per-lane class mask in compiled network (decision 1) |
| Schedules vs wall clock | Sim seconds + human aliases | Schedule times | Clock-bound | Sim seconds only (ADR-0012 refusal) |
| GTFS path | gtfs2pt.py | pt2matsim / GTFS2MATSim | Built-in / via suite | Offline GTFS→scenario compiler (decision 4) |
| TSP | TraCI scripts | Not the focus | Shipping feature | sigctl-pattern controller, class-aware detectors |
| Mode-share coupling | Person sim | Agent mode choice | Static assignment | Authored demand variants (elasticity-justified) |
| Replay/verification | No intent log | Stochastic | None | Bit-exact verb/intent replay, CRC-verified, free for transit |
| Validation corpus | — | — | — | CTA GTFS static + GTFS-RT archives (trajectory-dataset analogue) |

## Open Questions

- **CTA GTFS-RT rail coverage**: the official RT page lists vehicle
  positions + trip updates but 403s headless access without an API key.
  The Train Tracker API DOES offer vehicle positions via `ttpositions`
  (per-route live train runs with per-train lat/lon/heading/next-stop),
  so an official rail-position source exists regardless; whether RT
  VehiclePositions covers L trains (not just buses) remains unverified —
  needs a keyed fetch. Affects how much rail validation data exists for
  `domain-rail-operations`.
- **License purpose clause**: does an open-source simulator + public
  advocacy demos squarely satisfy "assisting mass transportation riders
  or in furtherance of promoting public transportation"? Judgment for
  the license ADR; consider asking CTA directly.
- **Class-mask granularity**: SUMO's 17 motor classes vs a small mask
  (car/bus/rail/emergency)? The .net.xml vocab is available; the kernel
  wants few classes. Mapping table needed at import.
- **Routing under class masks**: per-class next-hop tables (memory ×
  classes) vs mask-filtered on the fly (CPU per query) — ADR-0036's
  epoch-recompute cost model doesn't obviously scale to N class graphs.
- **Occupancy home**: VehicleType field (CRC'd, affects warm-start
  `TypeFingerprint` — `engine/warmstart.go:143-157`) vs vtypes.yaml
  presentation attribute consumed only by metrics. Latter is safer;
  needs the vtypes part built first.
- **Dwell vs strand escape**: is a class exemption from `StrandAfterS`
  acceptable (a wedged transit controller could then hold a lane
  forever), or should dwell be a recognized kernel state? Deferred to
  `domain-bus-operations`.
- **NTCIP 1211 vocabulary**: plain `signal_set` holds vs a
  priority-request payload (class, treatment, ETA) — only matters when
  priority classes multiply (transit + emergency). Defer until second
  class exists.
- **GTFS timepoint discipline**: CTA stop_times at non-timepoints are
  interpolated; how much does schedule-based validation bias if we
  treat them as exact? Needs a small data study.

## Connections to Other Topics

- **Relates to:**
  [domain-congestion-metrics](../domain-congestion-metrics/synthesis.md)
  (person-weighting extends the metric kernel's derived views; ADR-0014
  contract-pin discipline applies to new person metrics),
  [domain-signal-control](../domain-signal-control/synthesis.md) (TSP is
  a signal-control application; NEMA/NTCIP context lives there),
  [domain-demand-modeling](../domain-demand-modeling/synthesis.md)
  (PENDING — mode-share coupling, station-area demand, CTA ridership
  calibration are its inputs; this topic supplies the elasticity rule
  and the supply side),
  [arch-road-graph-model](../arch-road-graph-model/synthesis.md)
  (class masks change what a lane IS in the compiled model),
  [integration-osm-extraction](../integration-osm-extraction/synthesis.md)
  (rail ingestion extends the import pipeline; recipe-not-file posture
  reused for CTA),
  [domain-simulator-landscape](../domain-simulator-landscape/synthesis.md)
  (competitor positioning updated with transit features),
  [integration-3d-vehicle-fleet](../integration-3d-vehicle-fleet/synthesis.md)
  (PENDING — bus/L-car models are its stated targets).
- **Depends on:**
  [domain-traffic-flow-models](../domain-traffic-flow-models/synthesis.md)
  (IDM/MOBIL calibration for heavy vehicles),
  [concept-vehicle-controller-interface](../concept-vehicle-controller-interface/synthesis.md)
  (the contract transit controllers consume),
  [concept-scenario-format](../concept-scenario-format/synthesis.md)
  (the part system `transit/*.yaml` joins).
- **Informs:** `domain-rail-operations` and `domain-bus-operations`
  (PENDING siblings — this umbrella fixes the shared seams: class masks,
  GTFS compiler, person metrics, TSP channel; they own the mechanics:
  block signaling/interlocking/consists, dwell/bunching/berths), the
  pending license ADR (CTA clause), ADR-0039's next iteration
  (class-aware gate), and the `vtypes/*.yaml` deferred item from
  ADR-0012 M11 (occupancy attribute forces it).
- **ADRs touched:** 0007 (multi-class types — holds), 0008 (controller
  contract — holds, no new axes needed), 0009 (import strategy — class
  preservation amends it), 0012 (new part type), 0014 (derived person
  metrics), 0036 (class-aware routing), 0037 (TSP host), 0039 (gate
  exemption), VISION (amendment per decision 5).
