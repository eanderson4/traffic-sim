# Signal Control

> Signal control runs as an external grants-based client role over a NEMA dual-ring engine model; the 100 ms tick matches NTCIP decisecond timers exactly, and safety invariants stay engine-side.

## Overview

Signal control is how traffic-sim models signalized intersections — the representation of
signal programs, the fidelity bar for credible metrics, and the architectural question of
where the control logic lives. It matters because two of the four use cases in
[VISION.md](../../../VISION.md) hinge on it: "retime the light" is a canonical planner-game
upgrade, and the civic-advocacy use case must reproduce real North American timing plans
faithfully enough that a city traffic engineer trusts the output.

The research found unusually convergent evidence: real signal control is *already* a
discrete, timer-driven, deterministic machine running on a 0.1 s clock (NTCIP 1202), so
ADR-0005's 100 ms tick reproduces cabinet timing with zero rounding loss. The mature
representation for the North American context is the NEMA dual-ring barrier structure —
SUMO shipped stage-based first, then had to build a separate NEMA module (validated against
Econolite controller firmware) to reach practitioner fidelity. We skip that detour and make
ring-barrier the primary model.

The original synthesis recommended an engine-internal signal module. The 2026-07-17 design
review revised the split (ratified by [ADR-0008](../../decisions/ADR-0008-controller-contract.md)):
signal *policy* is an external client role holding signal-actuation grants — same pattern as
the director — while the engine enforces safety invariants (conflict matrix, min greens,
clearance intervals) regardless of commands received. The internal dual-ring state model and
the timing-plan-as-scenario-data findings stand unchanged.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Dual-ring barrier model | `raw/domain-signal-control/implementation.md` §1 | Movement→phase mapping + ring/barrier table; conflicting concurrent greens unrepresentable by construction |
| Actuated timer machine | `raw/domain-signal-control/implementation.md` §4 | Min/max green, passage time, gap-out / max-out / force-off, recalls, dual entry |
| Coordination machinery | `raw/domain-signal-control/implementation.md` §7 | Background cycle, yield point, fixed/floating force-off, early return to green |
| Clearance intervals | `raw/domain-signal-control/implementation.md` §5 | Yellow (3–6 s, ITE formula) + all-red; the credibility floor for capacity/delay metrics |
| Timing-plan compile | `raw/domain-signal-control/standards-and-patterns.md` §Patterns | Practitioner form (cycle/split/offset) → executor form (yield point, force-off) at load |
| Signal controller role | `decisions/ADR-0008` | External grants-based client emitting cabinet commands; engine clamps to safety invariants |
| Detector abstraction | `raw/domain-signal-control/standards-and-patterns.md` §Patterns | Stop-bar / advance loops as lane-area queries on the road graph |
| ATSPM-style telemetry | `raw/domain-signal-control/synthesis.md` §6 | `(tick, intersection, phase, state, termination_reason)` events on NATS |
| Pedestrian timing constraints | `raw/domain-signal-control/implementation.md` §6 | Walk/FDW times as min-green bounds; stochastic ped calls, not ped agents |
| Max-pressure module | `raw/domain-signal-control/competitors.md` §Max-pressure | First adaptive controller candidate: O(#movements), provably throughput-stabilizing, deterministic |

## How It Works

### 1. Architecture: external policy, engine-side safety (ADR-0008)

1. The engine publishes detector actuations and phase state; the signal controller — an
   ordinary NATS client holding signal-actuation grants — emits the cabinet command
   vocabulary: **call / hold / force-off / omit / next-phase**, plus coordination sync.
2. Commands are intents: buffered and batch-applied at tick boundaries per
   [ADR-0005](../../decisions/ADR-0005-time-model.md), logged for replay. ADR-0002's
   2026-07-17 clarification (no in-process controller fast path) means signal controllers
   ride the bus like everyone else.
3. The engine enforces safety invariants regardless of commands — conflict matrix, minimum
   greens, clearance intervals. This is the cabinet MMU (malfunction management unit)
   pattern: real cabinets give control logic free rein inside the intersection but let
   independent hardware force flash on conflicting outputs. Same clamp philosophy as vehicle
   intents in [ADR-0008](../../decisions/ADR-0008-controller-contract.md).

Every real control algorithm compiles into this vocabulary — fixed-time, actuated,
max-pressure, RL policies — so full NEMA feature coverage is deferred without foreclosing
anything. The superseded position (signal logic as an engine-internal module, never on the
wire) is worth remembering for *why* it lost: the cabinet itself separates policy from
safety enforcement, and making signal control external keeps "zero driving logic in the
engine" uniform across all four Intent axes. What must never be external is the safety
clamp and the deterministic application of phase changes.

### 2. Representation: NEMA dual-ring barrier, stage-based as degenerate case

A signal program = movement→phase mapping + ring/barrier table + per-phase timers (min/max
green, vehicle extension, yellow, red) + optional coordination block (cycle, splits, offset,
force-off mode). Key facts:

- NEMA numbering: odd = left turns, even = through+right; phases 2 and 6 usually the main
  street; 8-phase maximum. A **ring** sequences conflicting phases (one active per ring);
  a **barrier** partitions {1,2,5,6} from {3,4,7,8} — never concurrent, both rings cross
  together. Barrier conflicts are unrepresentable by construction.
- Ring-barrier is strictly more expressive than stage lists: actuation produces phase
  combinations (e.g. 2+5 after an early gap-out) no fixed stage sequence contains.
  European-style stage programs compile down onto the same core (single ring = stage list).
- Evidence: SUMO shipped stage-based `tlLogic`, then built the NEMA module (1.11.0, NREL /
  US DOE funding) and validated its TS2 offset behavior against software-in-the-loop
  Econolite controllers; CORSIM's Appendix F is a full TS1/TS2 dual-ring emulator and
  effectively a free requirements spec; Vissim's answer is embedding actual vendor firmware
  (Econolite EOS) — the commercial endgame of the same fidelity ladder.

### 3. The actuated timer machine and v1 scope

The NEMA actuated phase is a small state machine of timers: green holds for **minimum
green**, extends by **passage time** per detector actuation, and terminates by **gap-out**
(gap > passage time), **max-out** (max green under conflicting demand), or **force-off**
(split exhausted under coordination). Recalls (min/max/soft), dual entry, simultaneous
gap-out, red revert (~2 s) layer on top. NTCIP 1202 defines all these timers **in tenths of
seconds** — exactly the 100 ms tick, zero rounding loss.

v1 scope (per the 2026-07-17 review): ship a **fixed-time + basic-actuated** controller
client. Status 2026-07-19: fixed-time landed kernel-side as
[ADR-0011](../../decisions/ADR-0011-fixed-time-signals.md) — programs are network data, the
light derives from the tick count (item 4 below), and the enforcement seam waits for the
external cabinet client; actuated remains follow-up. Max-pressure is the first adaptive module — O(#movements) per decision, a pure
function of engine state (deterministic, replay-safe), provably throughput-stabilizing;
InSync's state-machine success (900+ intersections across 18 US states by 2012) validates
cycle-free adaptive control as operationally legitimate. Variable initial, gap reduction,
conditional service, and lead-lag authoring ergonomics defer to v2 — full feature and
validation work lands when the first advocacy corridor is chosen and its real phasing is
known. CORSIM App F documents the full zoo if needed.

### 4. Timing plans are scenario data; the tick count is the master clock

- Scenario files carry timing plans in **practitioner form** — rings, phases, timers,
  cycle/split/offset, TOD plan schedule — so scenario diffs read like the timing changes a
  city would actually make. The engine compiles to executor form (yield points, force-offs)
  at load (the CORSIM pattern). Plan libraries + TOD schedules (the SCATS pattern) make
  "AM peak vs off-peak retiming" a scenario *variant*.
- Coordination needs no intersection-to-intersection communication: each controller
  computes `time_in_cycle = (tick × Δt − offset) mod cycle`. ADR-0005's tick count is a
  perfect master clock — the real world's drift/DST failure class is designed out.
- Planning defaults for authored plans: cycles 60 s (permissive lefts) / 90 s / 120 s
  (protected both streets); Webster's C_opt = (1.5L + 5)/(1 − Y) with practical bounds
  ~40–120 s as a design-time preset generator; v/c < 0.85 as the undersaturation bar.
- Note: offset reference points differ by controller type in the field (TS1 vs TS2 vs
  Type 170; only Type 170's start-of-yellow is directly observable) — our scenario format
  should pick one convention (TS2 start-of-green) and state it.

### 5. Fidelity bar: clearance + lost time, not pedestrian agents

- Always model yellow + all-red (3–6 s per MUTCD; ~70% of agencies use the ITE kinematic
  formula CP = t + V/(2a + 64.4g) + (W + L)/V with t ≈ 1 s, a ≈ 10 ft/s², L ≈ 20 ft).
- Start-up lost time (~2 s) emerges from car-following, but effective-green accounting must
  match HCM conventions (4 s/phase default total lost time). Clearance + lost time is **~7%
  of a 60 s cycle** — the difference between credible and inflated capacity. Saturation
  flow: 1,500–2,000 veh/h/ln observed, 1,900 pc/h/ln ideal; capacity c = s·g/C.
- HCM signalized LOS (control delay s/veh: A ≤10, B 10–20, C 20–35, D 35–55, E 55–80,
  F >80) is the vocabulary the advocacy audience already speaks (full metric set owned by
  [Congestion Metrics](../business-domains/congestion-metrics.md)).
- Pedestrians: Walk 4–7 s + clearance at 3.5 ft/s over crossing width (11/17/23/29 s for
  40/60/80/100 ft) act as **minimum-green constraints** on parallel phases; LPIs (≥3 s) are
  expressible as timing offsets. Stochastic ped *calls*, not simulated pedestrians — CORSIM
  documents <100 crossings/h as MOE-irrelevant, and VISION lists pedestrians as a non-goal.

### 6. Telemetry and external requests

- Engine emits `(tick, intersection, phase, state, termination_reason)` — gap-out /
  max-out / force-off classification included — as a subject family under the
  [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) taxonomy. ATSPM (Purdue
  Phase Termination, Split Monitor) is the field's post-hoc analysis currency, logged in the
  field at 0.1 s resolution; emitting it natively makes sim output directly comparable to
  ATSPM field data — a validation channel. SUMO's NEMA module already outputs
  ATSPM-oriented events, so demand for the shape is proven.
- Transit/emergency priority (v2) follows NTCIP 1211's request-not-command shape: an
  external *request* intent, batch-applied at a tick boundary and logged; grant logic
  engine-internal and deterministic; agency rules apply (no skipping demanded phases,
  EVP > TSP, all events logged).

## Gotchas

- **Instant color flips inflate capacity**: skipping yellow/all-red and lost time overstates
  capacity ~7% on a 60 s cycle and destroys delay credibility; LightSim had to add
  switching-cost awareness to stop max-pressure from collapsing capacity.
- **RL-style phase-hopping is not a control interface**: CityFlow/RESCO agents switch phases
  per action-step with no barrier, min green, or clearance machinery — controllers that
  cannot exist in a cabinet and metrics that don't transfer. External signal commands must
  always pass the engine-side safety clamp.
- **Phases ≠ stages/transitions**: SUMO's flat phase list (new `<phase>` whenever any
  signal changes) explodes one engineering phase into many sim phases and is SUMO's own #1
  timing-sheet import friction. Keep movements/timers primary; derive display states.
- **Offsets without a master clock drift**: real coordination fails via clock skew and DST;
  the field solved it with sync references decades ago. Our tick count designs the whole
  failure class out — but only if offsets are defined against the tick count, not neighbors.
- **Real timing plans are not open data**: OSM maps signal locations, directions, and
  right-on-red rules but no phases, cycles, or offsets. Toronto alone executes 1,000–1,500
  long-term + 5,500–6,000 short-term timing changes per year and states the data is not
  machine-readable. Every scenario's timing plan must be authored or inferred — the
  Webster/HCM-QEM preset generator is load-bearing, not a convenience.
- **Simulating pedestrians to get pedestrian delay is wasted fidelity**: ped call emulation
  suffices (CORSIM, <100 crossings/h MOE-irrelevant) — model the timing impact, not bodies.
- **Non-deterministic adaptive logic shatters replay**: any adaptive module (max-pressure
  included) must be a pure function of engine state + seeded RNG — same rule as vehicle
  dynamics under ADR-0005.

## Open Questions

- **Detector placement semantics**: auto-place stop-bar + advance loops (SUMO-style) vs
  authored placement tied to lane geometry — depends on the road graph model.
- **Soft recall / call-to-non-actuated**: real but undocumented-in-standards behaviors
  (CORSIM approximates with min/max recall) — how much fidelity is needed?
- **End-to-end validation path**: obtain a published timing sheet + ATSPM data for one real
  intersection and validate phase-termination distributions (trajectory datasets cover
  vehicles, not controllers).
- **Stage-based import for European-style OSM networks**: needed at all for v1?
- **TSP intent subject design and replay logging format** on the NATS contract.
- **First advocacy corridor**: choosing it unblocks the deferred full NEMA feature and
  validation work (lead-lag, permissive periods, conditional service).

## Related

- [Time Model](../architecture/time-model.md) — the 100 ms tick = NTCIP decisecond timers; phase changes batch-applied at tick boundaries; tick count as coordination master clock
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — the signal axis of the 4-axis Intent; grants-based signal role; engine-side clamp philosophy
- [Scenario Format](../concepts/scenario-format.md) — timing plans as first-class authored data; practitioner→executor compile; TOD plan libraries as variants
- [Congestion Metrics](../business-domains/congestion-metrics.md) — HCM LOS vocabulary, delay metrics, ATSPM measures the metrics layer should reproduce
- [OSM Extraction](../integrations/osm-extraction.md) — provides signalized node locations; timings provably absent, forcing the timing-plan generator
- [Simulator Landscape](../business-domains/simulator-landscape.md) — SUMO/CORSIM/Vissim signal coverage surveyed as prior art

---
*Raw research: [raw/domain-signal-control/](../../raw/domain-signal-control/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
