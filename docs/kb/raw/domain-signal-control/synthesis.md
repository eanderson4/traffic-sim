# Synthesis: Signal Control

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds a future signal-control ADR (not yet numbered at time of writing).
> This synthesis recommends; the ADR decides.

## Summary

The research question was how traffic-sim should model signalized
intersections — representation, fidelity, and where the logic lives — given
ADR-0005's hybrid time model and the civic-advocacy replay use case. The
evidence is unusually convergent: real signal control is *already* a discrete,
timer-driven, deterministic machine running on a 0.1 s clock
([NTCIP 1202](https://www.ntcip.org/file/2019/07/NTCIP-1202v0328A.pdf)) — our
100 ms tick fits it exactly. The mature representation for the US context is
the NEMA dual-ring barrier structure, and SUMO's history (stage-based first,
NEMA module later, validated against Econolite firmware SIL) tells us to start
there rather than converge on it. Phase changes belong inside the engine on
the scheduled-event list; the only external inputs are TSP-style requests.
The genuine gaps: no standalone open-source ring-barrier engine library
exists, and real-world timing plans are not open data — our scenario format
must treat timing plans as first-class authored artifacts.

## Source Files

- [Mechanics: rings, timers, coordination, cabinets](./implementation.md)
- [Prior art survey: simulators, adaptive systems, cabinet emulation](./competitors.md)
- [Standards, formalisms, patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions (for a signal-control ADR)

### 1. Internal representation: movement-based dual-ring barrier model (NEMA-style), stage-based as the degenerate case
**Choice:** A signal program = movement→phase mapping + ring/barrier table +
per-phase timers (min/max green, vehicle extension, yellow, red) + optional
coordination block (cycle, splits, offset, force-off mode). Barrier conflicts
are unrepresentable by construction (the MMU pattern:
[EDI MMU-16E](https://www.orangetraffic.com/product/edi-mmu-16e-malfunction-management-unit/)).
European-style stage programs compile down onto the same core (single ring =
stage list).
**Why:** The civic-advocacy use case is inherently North-American: timing
sheets, ATSPM data, and practitioners all speak NEMA phases
([STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
Ring-barrier is strictly more expressive than stages — actuation produces
phase combinations (e.g. 2+5 after early gap-out) no stage list contains
([SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)). The dual-ring
object also carries the safety invariants explicitly, which a flat state
string cannot.
**Trade-off:** More implementation work than a stage list; permissive/protected
turn logic (SUMO's `G` vs `g` distinction,
[SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)) still has
to live in the movement layer alongside. Overlaps and lead-lag phasing add
table complexity.
**Field context:** SUMO shipped stage-based tlLogic, then built a separate
NEMA module (1.11.0) with DOE funding and validated it against Econolite
software-in-the-loop controllers
([SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)); Vissim sells
RBC/Econolite add-ons for the same reason
([PTV FAQ](https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs)).
CORSIM's appendix is effectively a free requirements spec for this core
([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).

### 2. Signal controllers are engine-internal modules on the tick grid; phase changes are scheduled engine events
**Choice:** The ring-barrier machine runs inside the engine, owned by the
world-state goroutine; every timer expiry (min-green met, passage-time
expired, force-off, yellow→red) is an event on ADR-0005's internal
scheduled-event list. Signal control is NEVER an external NATS controller.
**Why:** NTCIP controller timers are deciseconds
([NTCIP 1202 v01.07](https://www.ntcip.org/file/2018/11/NTCIP1202v0107d.pdf)) —
the 100 ms tick reproduces cabinet timing with zero rounding loss. External
control would inject network latency into a safety core and break replay;
ADR-0005 already assigns signal phase changes to the internal event list.
The tick count doubles as the coordination master clock (see #4).
**Trade-off:** Researchers can't hot-swap control algorithms over the wire at
runtime the way TraCI allows; algorithm experimentation happens via engine
modules and scenario variants instead. That is the deliberate cost ADR-0005
already paid for determinism (the TraCI trap argument,
[[arch-time-model]]).
**Field context:** Deterministic engines all keep signals internal (SUMO,
CORSIM, Vissim); the field's own analog is the cabinet — control logic runs
in the intersection, only *requests* arrive from outside (NTCIP 1211
PRG→PRS→controller, [NTCIP 1211](https://www.ntcip.org/file/2018/11/NTCIP1211-v0224j.pdf)).

### 3. v1 controller types: fixed-time, actuated, coordinated-actuated; max-pressure as the first adaptive module
**Choice:** Implement (a) fixed-time (degenerate actuated with max recall),
(b) actuated: min/max green, passage time, gap-out/max-out, min/max/soft
recall, dual entry, semi-actuated mode; (c) coordinated-actuated: background
cycle, yield point, fixed/floating force-off, early return to green; (d)
stretch: max-pressure phase selection as an engine module
([Varaiya via arXiv 2406.19269](https://arxiv.org/html/2406.19269v1)).
Defer variable initial, gap reduction, conditional service, permissive-period
fine structure to v2 (CORSIM documents them if needed,
[App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
**Why:** (a)–(c) cover everything the civic-advocacy replay needs — "retime
the light" IS cycle/split/offset edits plus actuated parameters. Max-pressure
is O(#movements) per decision, a pure function of engine state, and
provably throughput-stabilizing — adaptive control without breaking
determinism or inventing a SCOOT clone. InSync's state-machine success
(900+ intersections by 2012,
[Politecnico](https://www.politesi.polimi.it/retrieve/a81cb05a-9988-616b-e053-1605fe0a889a/2013_10_Ketabdari%20(REVISED).pdf))
validates cycle-free adaptive control as operationally legitimate.
**Trade-off:** No SCOOT/SCATS emulation (they're whole centralized systems,
not controllers); permissive-period modeling shortcuts may cost a few percent
fidelity on tightly coordinated corridors until v2.
**Field context:** This mirrors SUMO's own split (actuated / delay_based /
NEMA) minus the delay_based heuristic, plus CORSIM's coordination core.

### 4. Timing plans are scenario data in practitioner form; the tick count is the master clock
**Choice:** Scenario files carry timing plans as (rings, phases, timers,
cycle/split/offset, TOD plan schedule) — the practitioner projection; the
engine compiles them to executor form (yield points, force-offs) at load,
the CORSIM pattern ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
Coordination uses `time_in_cycle = (tick × Δt − offset) mod cycle`; no
intersection-to-intersection communication. Plan libraries + TOD schedules
(SCATS pattern, [STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm))
make "AM peak / PM peak / off-peak" retiming a scenario *variant* — directly
serving the planner-game and advocacy use cases.
**Why:** Real offset coordination depends on a shared master clock and fails
via drift/DST in the field; our tick count is a perfect master clock, so an
entire real-world failure class is designed out
([STM ch6 §6.3.4](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
Keeping practitioner units in the scenario means diffs read like the timing
changes a city would actually make.
**Trade-off:** The scenario format must now own a second compile step
(timing → executor); [[concept-scenario-format]] is being researched
concurrently — this is a hard dependency to hand over, not to decide here.
**Field context:** SUMO's `earliestEnd/latestEnd` + `cycleTime` params are
the same idea bolted onto stage-based phases
([SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)); SUMO's
NEMA module does it natively with `coordinate-mode` + `fixForceOff`
([SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)).

### 5. Fidelity bar: clearance intervals + lost time + detector placement + ped timing constraints — not ped agents
**Choice:** Always model yellow + all-red (ITE formula or user values, 3–6 s
band per [MUTCD/Kittelson](https://www.kittelson.com/ideas/how-long-should-a-yellow-light-be/)),
start-up lost time (~2 s) emerges from car-following but effective-green
accounting must match HCM conventions (4 s/phase default,
[STM ch3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm));
detectors are lane-area queries on the road graph; pedestrian Walk/FDW times
are *minimum-green constraints* on parallel phases (walk 4–7 s, clearance at
3.5 ft/s over crossing width,
[MUTCD 4E](https://mutcd.fhwa.dot.gov/HTM/2009/part4/part4e.htm)), with
stochastic ped *calls*, not simulated pedestrians (CORSIM precedent: ped
demand <100 crossings/h is MOE-irrelevant,
[App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
**Why:** Clearance + lost time is ~7% of a 60 s cycle — the difference
between credible and inflated capacity; HCM LOS thresholds (A ≤10 … F >80
s/veh delay, [STM ch3 t3-3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm))
are the vocabulary our advocacy audience uses ([[domain-congestion-metrics]]
owns the full metric set). VISION lists pedestrians as a non-goal; the
field's own flagship sim gets ped *effects* without ped *agents*.
**Trade-off:** Dilemma-zone behavior at high-speed approaches is only as good
as the car-following model's stop/go decision ([[domain-traffic-flow-models]]
dependency); exclusive ped phases and LPIs (≥3 s,
[MUTCD Part 4](https://mutcd.fhwa.dot.gov/pdfs/2009r1r2/part4.pdf)) are
expressible as timing constraints but not visualized as people.
**Field context:** LightSim's "LT-aware" fix shows what happens when switching
costs are ignored — capacity collapse
([arXiv 2602.21852](https://arxiv.org/html/2602.21852v1)).

### 6. Publish ATSPM-style signal events; TSP requests are the only external signal input
**Choice:** Engine emits `(tick, intersection, phase, state,
termination_reason)` on NATS — gap-out/max-out/force-off classification
included ([ATSPM measures](https://pdfs.semanticscholar.org/30a9/8b19268ce3a482249ed144dab3b1523aeac0.pdf)).
Transit/emergency priority, when added, follows NTCIP 1211's shape: an
external *request* (an intent batch-applied at a tick boundary, logged for
replay), with grant logic engine-internal and deterministic; agency rules
apply — no skipping demanded phases, EVP > TSP, all events logged
([MassDOT ATC spec](https://www.mass.gov/doc/2025-standard-specifications-for-highways-and-bridges/download)).
**Why:** Termination-reason telemetry is the field's standard post-hoc
analysis currency (Purdue Phase Termination, Split Monitor) — emitting it
natively makes our output directly comparable to ATSPM field data, a
validation channel. Requests-not-commands preserves ADR-0005's intent
semantics exactly (SUMO already models bus priority as detector-conditional
switching rules, [SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
**Trade-off:** Adds one subject family to the message contract
([[arch-nats-backbone]] dependency); TSP itself is v2 scope.
**Field context:** SUMO's NEMA module already outputs ATSPM-oriented state
events ([SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)) — demand
for this data shape is proven.

## Compare/Contrast: Us vs the Field

| Dimension | SUMO (default) | SUMO NEMA | CORSIM | Vissim RBC/EOS | CityFlow/RL | us (proposed) |
|---|---|---|---|---|---|---|
| Representation | stage list + state strings | dual-ring barrier | dual-ring TS1/TS2 | signal groups → ring-barrier/firmware | fixed list + actions | **dual-ring barrier (primary)** |
| Actuation | gap-based, no rings | vehExt/recalls/force-off | full NEMA zoo | up to real firmware | none built-in | **gap/max/force-off + recalls** |
| Coordination | earliestEnd/latestEnd | TS2/170 offsets, fixForceOff | yield pt/permissives | commercial | external RL | **tick-count master clock** |
| Pedestrians | simulated (optional) | calls | **calls only (<100/h ignored)** | Viswalk | none | **timing constraints + calls** |
| Clearance/lost time | yellow, optional all-red | yellow+red per phase | full | full | fixed yellow steps | **always on** |
| Termination telemetry | phase tracker | ATSPM-style output | reports | reports | none | **NATS events w/ reasons** |
| Determinism/replay | seeded re-run | seeded re-run | re-run | seed sweeps | seeded | **ADR-0005 keyframes + intent log** |

## The Genuine Gap (again)

Three real ones this time:

1. **No standalone, open-source, documented ring-barrier engine library
   exists.** NEMA-faithful control logic lives only embedded in large sims
   (SUMO's NREL module, CORSIM's 40-year-old core), in closed commercial
   add-ons (Vissim RBC, embedded Econolite EOS), or in hardware test rigs
   (TS2 Virtual Cabinet, [Western Systems](https://www.westernsystems-inc.com/product/ts2-virtual-cabinet/)).
   A clean Go dual-ring core — parameterized by NTCIP-style objects, emitting
   ATSPM-style events — would be reference-grade software that doesn't
   currently exist at any price.
2. **Real timing plans are not open data.** OSM maps signal *locations*,
   directions, right-on-red rules, even push buttons — but no phases, cycles,
   or offsets ([OSM wiki](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dtraffic_signals)).
   Toronto alone executes 1,000–1,500 long-term + 5,500–6,000 short-term
   timing changes per year and states the data is "not in an accessible and
   machine-readable format" ([Toronto report](https://www.toronto.ca/legdocs/mmis/2026/cc/bgrd/backgroundfile-286314.pdf)).
   Consequence: [[integration-osm-extraction]] can give us signalized nodes,
   but *every scenario's timing plan must be authored or inferred* — the
   scenario format's timing-plan generator (Webster/HCM-QEM presets + warrant
   checks) is not a convenience, it's a load-bearing component.
3. **The RL benchmark world and the cabinet world don't share a model.**
   CityFlow/RESCO/LibSignal controllers pick phases with no barrier, no min
   green, no clearance machinery; LightSim's switching-cost patch is the
   first visible repair ([arXiv 2602.21852](https://arxiv.org/html/2602.21852v1)).
   A sim that enforces cabinet constraints while exposing a clean
   request-level interface is unfilled territory.

## Open Questions

- Permissive-period and lead-lag fidelity needed for credible corridor
  bandwidth claims — v2 scope, or required for the first advocacy corridor?
- Detector placement semantics: auto-place stop-bar + advance loops
  (SUMO-style) vs authored placement tied to lane geometry
  ([[arch-road-graph-model]]).
- Soft recall / call-to-non-actuated: real but undocumented-in-standards
  behaviors (CORSIM approximates with min/max recall,
  [App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)) —
  how much do we need?
- Validation path: can we obtain a published timing sheet + ATSPM data for one
  real intersection to validate phase-termination distributions end-to-end?
  ([[domain-trajectory-datasets]] covered vehicle data, not controller data.)
- Stage-based import for European-style OSM networks — needed at all for v1?
- TSP intent subject design and its replay logging format
  ([[arch-nats-backbone]]).

## Connections to Other Topics

- **Constrained by:** [[arch-time-model]] / ADR-0005 — phase changes are
  scheduled engine events on the tick grid; controller timers (0.1 s, NTCIP)
  match the 100 ms tick exactly; the tick count is the coordination master
  clock; adaptive logic must be deterministic for replay.
- **Constrains:** [[concept-scenario-format]] (timing plans as first-class
  scenario data; practitioner form → executor form compile; TOD plan
  libraries; variant diffs = timing edits — concurrent topic, this is the
  hand-off), [[arch-nats-backbone]] (signal-state + termination-reason event
  subjects; TSP request intents), [[arch-road-graph-model]] (movements/lane
  connections as NEMA phases; detectors as lane-area queries),
  [[domain-congestion-metrics]] (clearance/lost-time fidelity as the
  credibility floor for delay/LOS; ATSPM-style measures — concurrent topic),
  [[concept-vehicle-controller-interface]] (signals are NOT vehicle
  controllers — the request/command boundary is the shared design rule),
  [[arch-state-authority]] (signal controller state lives inside world state,
  single-writer goroutine).
- **Depends on:** [[domain-traffic-flow-models]] (stop/go at yellow onset and
  start-up lost time emerge from car-following; 100 ms tick already
  validated), [[integration-osm-extraction]] (signal locations from OSM;
  timings provably absent → scenario generator required).
- **Relates to:** [[domain-simulator-landscape]] (SUMO/Vissim signal coverage
  surveyed here), [[domain-trajectory-datasets]] (validation targets; ATSPM
  controller data complements trajectory data),
  [[integration-maplibre-realtime]] (signal state + phase rendering from the
  NATS event stream), [[domain-macroscopic-flow-models]] (saturation flow
  1,900 pc/h/ln ideal links the FD world to signal capacity accounting).
