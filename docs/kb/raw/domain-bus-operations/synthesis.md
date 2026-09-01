# Synthesis: Bus Operations (buses in mixed traffic and dedicated lanes, Chicago/CTA focus)

> Researched: 2026-08-24 | Git HEAD: 2bc98de | Status: complete

## Summary

Buses are the most achievable transit addition this engine could take on:
the multi-class vehicle machinery, the demand type-mix, the wire-level class
identity, and the runtime signal-control channel all already exist, so a
credible bus line is a *vehicle-type registry entry plus two external
controllers* (a bus fleet agent that dwells, and a TSP variant of the sigctl
pattern). The two genuine kernel gaps are (1) per-lane class masks — the
import pipeline parses SUMO `allow`/`disallow` and discards it, so bus lanes
compile as general-purpose lanes — and (2) an occupancy attribute if
person-delay is to be measured. Bunching, dwell-driven capacity loss, and
berth blocking all emerge from existing physics once a controller dwells;
the architecture's recorded-intent replay makes bus *control laws* (holding,
TSP conditionality) first-class, bit-exactly replayable experiments — the
one thing SUMO/TraCI, Aimsun, and Vissim structurally cannot offer.

## Source Files

- [Implementation trace](./implementation.md) — every codebase surface buses touch, with `file:line`
- [Competitor analysis](./competitors.md) — SUMO, MATSim, Aimsun, Vissim, open bunching-simulation lineage
- [Standards & patterns](./standards-and-patterns.md) — Newell–Potts instability, dwell regressions, TCQSM capacity, NACTO, NTCIP 1211/TSP, CTA data

## Key Decision Candidates

### D1. Bus as a vehicle class (copy the Truck precedent)

**Choice:** Add `var Bus` to `engine/vehicle.go` beside `Car`/`Truck`
(`engine/vehicle.go:33,40`), register it in the three registry literals
(`engine/cmd/serve/main.go:154`, `engine/cmd/simrun/main.go:66`,
`engine/cmd/demosrv/params.go:28`), spawn via demand `Flow.vtypes`
(`engine/scenario/scenario.go:127`).
**Why:** ADR-0007 §3 made multi-class first-class; IDM reads all dynamics
from the type (`engine/vehicle.go:150-168`); gap semantics are
length-unambiguous by construction. Parameters: 12 m (CTA 40-ft) and/or
18 m articulated variants, ~2.55 m width, lower `A`, urban `V0` moot under
lane limits.
**Trade-off:** `VehicleType` has **no per-type MOBIL set** despite ADR-0007
§3's wording — MOBIL params are engine-global (`engine/engine.go:15-17`).
Bus lane-change temperament must come from the bus controller, or the struct
grows fields (contract-visible). Also: three hand-mirrored registries invite
drift (demosrv's is a deliberate duplicate).
**Field context:** SUMO models exactly this as a `vType` with `vClass=bus`
([SUMO vehicle definitions](https://sumo.dlr.de/docs/Definition_of_Vehicles,_Vehicle_Types,_and_Routes.html));
the vClass vocabulary doubles as our lane-mask enum (D3).

### D2. Dwell via external controller (replay-safe by construction)

**Choice:** A bus fleet agent claims buses (ADR-0008 grants), drives them
with the same PolicyCtx observations the default driver dogfoods
(`engine/policy.go:5-11`,`:64-84`), brakes into stops itself, and holds with
`SpeedSetpoint: 0` — persistent, clamp-legal, recorded
(`engine/intent.go:42-43`; servo at `engine/vehicle.go:125-134`).
**Why:** Zero driving logic in the kernel (ADR-0008) stays true; intents are
the recorded plane, so dwells replay bit-exactly with the controller absent;
holding-control laws (D6) are then *policy*, swappable per run arm.
**Trade-off:** (a) cruise-0 brakes at up to −9 m/s² if issued carelessly —
the agent owns the approach trajectory; (b) fleet failover to the default
driver turns buses into stop-skipping cars on disconnect — a loud
behavioral discontinuity (good: visible); (c) a *dwell director verb* (the
ADR-0037
pattern) was considered and rejected: signal_set commands a network act, a
dwell verb would command a vehicle act — kernel driving logic.
**Field context:** SUMO/MATSim/Vissim all make dwell kernel-side content
(`<stop duration until>`, schedule offsets, dwell distributions —
[SUMO PT docs](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)).
Our inversion costs us their turnkey stops and buys replayable control.

### D3. Per-lane class masks (shared design with rail)

**Choice:** Extend `NetLane` with an optional class-permission field —
following the two existing "optional v1 extension, absent = old semantics"
precedents (`engine/netfile.go:65-78`) — emit it in netimport from the
already-parsed `Allow`/`Disallow` (`engine/netimport/netimport.go:115-124`,
construction site `:236-247`), carry it on `Lane` (`engine/network.go:9-67`),
and consult it at nine enumerated seams: routing tables
(`engine/routing.go:204,373`), lateral-depth BFS (`routing.go:117`),
routeHopOK (`engine/mobil.go:76`), route recovery (`mobil.go:109`), MOBIL
gather as `SideCtx.Present=false` (`engine/policy.go:55-62,359` — the
highest-leverage single point, inherited by the external driver for free),
forced-hop feasibility, `pickSuccessor` fallbacks (`engine/engine.go:983`),
exit-walk room (`engine/rightofway.go:330`), and spawn validation
(`engine/director.go:177-200`).
**Why:** today a bus-only lane compiles open-to-all — cars would route and
recover *into* bus lanes, silently. Missing any seam is a
silent-fidelity leak.
**Trade-off:** per-(class,dest) routing tables multiply memoized state by
class count (epoch recompute cost in ADR-0036 included); mask joins the
importer identity question (ADR-0022 precedent); real ADR required —
network format + scenario hash consequences.
**Field context:** SUMO enforces the same `allow`/`disallow` we discard
([SUMO vClass](https://sumo.dlr.de/docs/Definition_of_Vehicles,_Vehicle_Types,_and_Routes.html));
NACTO's curbside/offset/queue-jump vocabulary reduces to masks + stop areas
+ TSP ([NACTO](https://nacto.org/publication/urban-street-design-guide/street-design-elements/transit-streets/dedicated-curbside-offset-bus-lanes/)).
Design once for rail too (rail lanes are currently dropped at import —
`netimport.go:80-88`).

### D4. Bus-stop model: in-lane dwell area first, pullouts later

**Choice:** Stops as scenario/controller content: (laneId, s-range) areas
in the bus agent's line table (SUMO's `startPos/endPos` shape), in-lane
dwell via D2. Pullouts (bus bays) deferred — buildable later from a short
auxiliary lane + existing merge behavior.
**Why:** in-lane blocking, on-line berth capacity, and multi-bus berth
blocking are all *emergent* (a dwelling bus is a stopped 12–18 m vehicle;
the TCQSM berth formula is a validation target, not an implementation);
area-not-point is required for >1-berth effects.
**Trade-off:** stop data lives outside the network file (controller config
or scenario part), so the viz/metrics don't see stops without a new
publication channel; far-side stops near junctions must be placed on the
first ordinary lane past the box and the agent must sequence box-crossing
vs. dwell. **The ADR-0034 strand escape is the live edge:** any dwell is
indistinguishable from a freeze below 0.1 m/s (`engine/gridlock.go:74`);
false stranding needs dwell ≥ 300 s + head-of-lane + capacity-blocked routed
box (`gridlock.go:145-207`) — ordinary dwells are safe with 5× margin, but
>5-minute terminus layovers under gridlock strand. Cheapest mitigation:
despawn at end-of-line (ADR-0021 arrival-despawn, `engine/director.go:136-141`)
instead of modeling layovers. Also watch: dwell samples poison lane `ttEMA`
for adaptive routing (`engine/engine.go:531-545`) — needs a measurement
bracket.
**Field context:** SUMO stops are lane areas with person capacity
([docs](https://sumo.dlr.de/docs/Simulation/Public_Transport.html)); TCQSM
quantifies the on-line capacity hit we get emergently
([TCQSM 2nd ed. Part 4](https://onlinepubs.trb.org/onlinepubs/tcrp/docs/tcrp100/Part4.pdf));
pullout re-entry delay is the documented cost we defer
([Pierce Transit guidelines](https://hdp-us-prod-app-rideprt-engage-files.s3.us-west-2.amazonaws.com/6117/6417/8635/PRT_Bus_Stop_Guidelines_3.1_final.pdf)).

### D5. TSP as a sigctl-family controller

**Choice:** New NATS client beside `engine/natsio/sigctl`: same virtual
approach-side stop-line detectors (`sigctl.go:160-223`), plus the snapshot's
existing `Class` field (`engine/natsio/frame.go:130`) to filter buses and
compute stop-line ETAs. Green extension = hold renewal; red truncation =
early phase-walk; conditionality (late-only, Chicago's actual rule) = client
predicate. All commands ride `signal_set`, inheriting the 300 s starvation
rail and recorded replay (ADR-0037).
**Why:** the channel, rails, and enforcement seam (`sigPhaseAt`,
`engine/signal.go:203`) shipped 2026-07-31 (M1); the reference client
followed 2026-08-04 (M2); TSP is
policy on top, not new kernel.
**Trade-off:** inherits the documented M2 gaps — no live-plane override echo
(self-tracking exact only as sole commander; a TSP+actuated pair on one
junction breaks it; the VerbReply applied-tick/effective-hold echo is a
recorded proposal); priority requests are not auditable objects unless a
two-controller (request/grant) variant is built, NTCIP-1211-style.
**Field context:** NTCIP 1211's generator/server split
([TCRP TSP State of Practice](https://nap.nationalacademies.org/read/25816/chapter/3));
FHWA STM ch. 9's "retime, don't skip" coordination rule is our starvation
rail's doctrine ([FHWA](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm));
Chicago's conditional deployments (Jeffery Jump 2014, Ashland 2016, Western
2018 — [ActiveTrans](https://activetrans.org/wp-content/uploads/2024/12/SpeedingupBuses.pdf))
are the exact policy to replicate and bracket.

### D6. Bunching as emergent phenomenon + holding-control reference implementation

**Choice:** No bunching code anywhere. Buses per line = a periodic demand
flow; dwell = boarding-rate function (TCQSM/Tirachini constants, ~2.5–4.2 s
boarding, ~2.1 s alighting, 5.2 s dead time, crowding multiplier) in the bus
agent; headway instability then *emerges* per Newell–Potts. Ship one
reference holding controller (Daganzo 2009 headway-holding at control
points; Bartholdi–Eisenstein 2012 as the schedule-free variant) measured by
paired-seed brackets on headway CV / passenger wait.
**Why:** the mechanism is demand-accumulated dwell variance — client-side
ingredients only; Gershenson & Pineda show instability is generic, so an
engine that doesn't bunch under these conditions is mis-calibrated, not
lucky. The O'Loan/Nagatani ring models are cheap validation fixtures
(Sugiyama-ring analogs).
**Trade-off:** needs boarding-demand content per stop (scenario data or
GTFS-derived rates) that no pipeline produces today — smallest honest start
is uniform stop boarding rates; passenger wait metrics need D7's occupancy.
**Field context:** the whole control literature (Daganzo, Xuan, Eberlein,
Bartholdi–Eisenstein, DRL wave) evaluates on bespoke line simulators; our
recorded-replay bracket harness is the rigorous version of their evaluation
loop ([standards-and-patterns.md §1](./standards-and-patterns.md)).

### D7. Person-delay metrics tie-in

**Choice:** Defer to the multimodal umbrella, but stage it now: occupancy as
a bus-controller-supplied attribute riding `Vehicle` → `tripState`
(`engine/metrics.go:276`) → `TripRecord` (`:135-154`, emission `:1370`) with
a schema-version bump; person-seconds lost alongside vehicle time loss;
class-split delay is already free via `TripRecord.TypeName` (`:138`).
**Why:** "did the bus lane move more *people*" is the deciding metric for
dedicated-lane politics (NACTO framing); vehicle-only metrics systematically
underweight 40-person buses vs 1.2-person cars.
**Trade-off:** kernel must *transport* occupancy (keyframed-not-CRC'd, the
`stopDone` precedent, `engine/vehicle.go:79-89`) — contract/schema change
needing an ADR (message-contract rule); interval-record person-throughput is
a second schema touch. Until then, class-split vehicle delay covers the A/B
question partially.
**Field context:** MATSim is person-native (its entire point); TCQSM LOS is
person-based; SUMO models persons intermodally. We are vehicle-first by
ADR-0014 and add the thinnest person layer that answers the question.

## Compare/Contrast: Our Approach vs the Field

| Dimension | SUMO | MATSim | Aimsun/Vissim | traffic-sim + bus controllers |
|---|---|---|---|---|
| Bus class | vType+vClass | vehicle types | native | `var Bus` (D1) — trivial |
| Stops | kernel lane-areas | schedule facilities | native stop types | controller line table + (laneId, s-range) (D4) |
| Dwell | duration/until/extension | offsets + awaitDeparture | distributions/Viswalk peds | recorded cruise-0 intents (D2) |
| Bus lanes | enforced class perms | n/a | reserved lanes | class masks — kernel gap (D3) |
| TSP | TraCI custom | ✘ | VAP / API | sigctl-family client (D5) |
| Bunching control | external (TraCI) | ✘ | external | holding-law clients, paired-seed brackets (D6) |
| Persons | intermodal | native | ✔ | occupancy attribute later (D7) |
| Replay of control runs | ✘ (TraCI not recorded) | ✘ | ✘ | ✔ bit-exact (ADR-0005/0008) |
| Signal starvation safety | controller's problem | ✘ | controller's problem | kernel rails, 300 s bound, logged lapses |
| Chicago calibration data | GTFS import tool | GTFS converters | manual | CTA GTFS + bustime archive (std §7) |

## Open Questions

1. **Mask enum scope:** fixed SUMO-vClass bitmask vs scenario type indices —
  network content vs scenario identity (affects hashes both ways).
2. **CTA GTFS-RT rail coverage:** CTA DOES publish an official GTFS-RT feed
  (beta) at transitdata.transitchicago.com — service alerts, trip updates,
  vehicle positions, .pb and JSON, free API key required (announced in the
  FY2024 Budget Book; not yet listed on the Developer Center, which is how
  the miss happened; the site 403s headless fetches via bot protection).
  The remaining unknown is whether VehiclePositions covers L RAIL — verify
  with a keyed fetch before building the validation pipeline; bustime
  polling archives remain a supplementary corpus (a Mobility
  Database/OpenMobilityData cross-check is optional).
3. **Terminus modeling:** despawn-and-respawn (ADR-0021) vs layover vehicles
  (ADR-0034 strand exposure past 300 s) — decision interacts with D4.
4. **ttEMA dwell bias:** should scheduled dwells feed the adaptive-routing
  travel-time signal? Measurement bracket needed before bus A/B results are
  trusted under default-ON adaptive routing.
5. **Per-type MOBIL:** struct extension vs controller-side temperament.
6. **TSP + actuated coexistence:** two commanders on one program breaks
  sigctl-style self-tracking; needs the recorded VerbReply-echo contract
  proposal resolved (ADR-0037 M2 deferral).
7. **Stop publication:** do stops stay pure controller config, or does the
  scenario format gain a stop part (viz and stop-level metrics would need it)?
8. **Boarding demand source:** GTFS frequencies give service, not boardings —
  CTA ridership portal aggregates are stop-level daily; per-stop boarding
  rates for the dwell model likely start as authored constants.

## Connections to Other Topics

- **Relates to:** [domain-multimodal-transit](../domain-multimodal-transit/) (umbrella: GTFS as source + person-weighted metrics + the class-mask layer map — this topic is its bus half); [domain-rail-operations](../domain-rail-operations/) (shares D3's class mask and D4's dwell-area design; rail adds guideway/signaling and multi-car vehicles, researched in parallel); [domain-demand-modeling](../domain-demand-modeling/) (stop-level boarding demand, OD flows feeding bus flows); [domain-signal-control](../domain-signal-control/) (actuated control family TSP joins; NEMA/NTCIP context); [domain-congestion-metrics](../domain-congestion-metrics/) (trip-record extensions, class-split delay, person-weighting).
- **Depends on:** ADR-0007 (multi-class), ADR-0008 (controller contract + grants), ADR-0014 (metric kernel), ADR-0021 (destination spawn/despawn — PROPOSED but implemented in the director), ADR-0034 (strand escape — the dwell interaction), ADR-0036 (adaptive routing — ttEMA interaction), ADR-0037 (signal_set channel + sigctl reference).
- **Informs:** a future transit-operations ADR (bus class + class masks + stop model is likely one ADR-implementing milestone); the multimodal umbrella's person-metrics design (D7 stages it); Chicago what-if program (bus-lane and TSP brackets on chi-loop-urban are the obvious next scorecards after sigctl).
