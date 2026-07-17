# Prior Art Survey: Signal Control

> Source: web research | Researched: 2026-07-16
> "Competitors" here = two groups whose signal-control designs we can steal from
> or be warned by: (a) simulators and benchmark environments that model signal
> controllers, and (b) the real-world control systems (adaptive packages,
> cabinet firmware, emulation products) our engine will be implicitly compared
> against in the civic-advocacy use case.

## Simulators

### SUMO — stage-based default + a real NEMA module bolted on later
- Default programs are fixed-time: 90 s cycle, equal green splits, yellow
  computed from approach speed; `netconvert` heuristics generate whole-city
  signal plans automatically; actuated default is available via
  `--tls.default-type actuated` ([Traffic Lights docs](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
- The stage-based core: a `<phase>` = duration + full per-link state string; a
  new phase is emitted *whenever any signal changes*, so engineering "phases"
  and transitions explode into many sim phases — SUMO's own docs warn this is
  the #1 import friction with real timing sheets
  ([Traffic Lights docs](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
- `type="actuated"` is German-style gap control: auto-generated induction loops,
  `max-gap` 3.0 s / `detector-gap` 2.0 s / `passing-time` 2.0 s defaults,
  min/max green via `minDur/maxDur` (5–50 s generated); coordination bolted on
  via `earliestEnd/latestEnd` against a `cycleTime`; dynamic phase selection
  (phase skipping) via `next` lists; a mini expression language
  (`z:` gap, `w:` wait, `d:` transit delay, `c:` time-in-cycle) for custom
  rules incl. bus priority ([Traffic Lights docs](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
- `type="delay_based"` prolongs green while vehicles accumulate time-loss
  (Oertel & Wagner 2011) — a two-knob heuristic nobody in the field uses but
  useful as a baseline ([Traffic Lights docs](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
- `type="NEMA"` (since 1.11.0, developed at NREL with DOE VTO funding): full
  dual-ring structure — `ring1/ring2`, `barrierPhases`, `vehExt`,
  `minRecall/maxRecall`, `coordinate-mode`, `fixForceOff`, `controllerType`
  TS2/170; validated against software-in-the-loop Econolite controllers;
  published as Schrader, Wang & Bittle, SUMO Conf Proc 2022; optional
  ATSPM-style phase-change event output
  ([SUMO NEMA docs](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- TraCI can retime signals live (`setNemaOffset`, `setNemaSplits`,
  `setNemaCycleLength`, applied at cycle end)
  ([SUMO NEMA docs](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- **vs traffic-sim (us):** SUMO's trajectory is the strongest evidence for our
  core choice: they shipped stage-based, then had to build a *second* controller
  module to reach NEMA fidelity — and validated it against real cabinet
  firmware. We can skip the detour and make ring-barrier the primary model
  (stage-based is a degenerate case of it). Their import-friction warning
  (phases ≠ transitions) is a design constraint on our scenario format.

### CORSIM (FHWA) — the reference NEMA emulator inside a 40-year-old sim
- The actuated control model is "an implementation of an eight-phase,
  dual-ring NEMA controller, as specified in the NEMA TS 1 and TS 2 standards,"
  also configurable to emulate Model 170 behavior
  ([TAT Vol 4 App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- Feature coverage is the deepest of any sim: fully/semi/coordinated modes,
  max green *and* max extension variants, variable initial (3 kinds), gap
  reduction (3 kinds), min/max recall, dual entry, red revert, simultaneous
  gap-out, conditional service, overlaps, three permissive periods, inhibit
  max termination, fixed conversion of (cycle, split, offset) → (yield point,
  force-off) ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- Pedestrians are never simulated — only their *calls* (three demand modes;
  <100 crossings/h documented as MOE-irrelevant)
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **vs us:** CORSIM is the existence proof that a sim-native dual-ring engine
  with the full NEMA feature zoo works — and its appendix is effectively a
  free requirements spec. Its ped-call emulation licenses our "pedestrian
  timing constraints, not pedestrian agents" scope cut (VISION non-goal).

### PTV Vissim — buy the cabinet firmware, not the model
- Fixed-time control is signal-group/intergreen-matrix based; actuation comes
  as paid add-ons: **RBC** (a ring-barrier controller per the North American
  procedure), **Econolite ASC/3** (emulated controller UI), **VAP** (a logic
  programming language for phase/stage-based actuated control reading sim
  detector variables) ([Vissim 11 manual mirror](https://pdfcoffee.com/vissim-11-manual-2-pdf-free.html),
  [PTV partner module list](https://ptvpartner.ro/en/ptv-vissim/module)).
- Current versions ship "natively embedded Econolite EOS" plus SCATS/SCOOT
  interfaces and an External Controller API — i.e. PTV's answer to signal
  fidelity is to embed the *actual vendor firmware*, not to model it
  ([PTV Vissim FAQ](https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs)).
- **vs us:** the commercial endgame of the fidelity ladder — when consultants
  need defensible numbers they run the real firmware SIL. We can't embed
  proprietary firmware, but a documented dual-ring core validated the way
  SUMO's was (against published controller behavior) covers the advocacy use
  case at zero license cost.

### MATSim — signals as a contrib, fixed-time by default
- The signals contrib simulates signals microscopically with a fixed-time
  default controller; traffic-responsive controllers are pluggable per
  intersection; vocabulary: signal → signal group → signal system → signal
  control ([matsim-libs contribs/signals README](https://github.com/matsim-org/matsim-libs/tree/master/contribs/signals)).
- Signal support is exactly the kind of reactive feature the event-driven
  HERMES rewrite *dropped* ([arch-time-model research](../arch-time-model/competitors.md)).
- **vs us:** MATSim confirms signals are a second-class citizen in
  activity-model sims; not competition for decision-grade intersection metrics.

### CityFlow + the RL benchmark world — signal-as-action, fidelity optional
- CityFlow's roadnet has `lightphases = {time, availableRoadLinks}` — a
  fixed-time list; *all* responsive control is external, via
  `set_tl_phase`-style RL actions at a default 1.0 s interval
  ([CityFlow roadnet docs](https://cityflow.readthedocs.io/en/latest/roadnet.html),
  [quick start](https://cityflow.readthedocs.io/en/latest/start.html)).
- RESCO (NeurIPS 2021) standardizes benchmarks (TAPAS Cologne, InTAS
  Ingolstadt) with MaxPressure and SOTL baselines
  ([RESCO paper](https://people.engr.tamu.edu/guni/papers/NeurIPS-signals.pdf),
  [GitHub](https://github.com/Pi-Star-Lab/RESCO)); LibSignal wraps SUMO +
  CityFlow under unified cross-simulator RL interfaces for fair comparison
  ([arXiv 2211.10649](https://arxiv.org/abs/2211.10649)); SUMO-RL wraps SUMO
  TLS into Gym/PettingZoo envs via TraCI
  ([ITC paper](https://thomasez.folk.ntnu.no/itc34/workshop%20papers/6.pdf)).
- The RL frame discards cabinet structure: agents pick phases directly, with
  clearance modeled (at best) as fixed yellow steps — no min/max green, no
  barrier constraints, no gap-out. LightSim (arXiv 2026) shows the repair
  direction: its "LT-aware MaxPressure" only switches when pressure gain
  exceeds the switching cost (yellow + all-red + half lost time), explicitly
  to avoid capacity collapse from naive switching
  ([arXiv 2602.21852](https://arxiv.org/html/2602.21852v1)).
- **vs us:** this world is a *user* of engines like ours, not a competitor —
  but it's a warning: if we expose signals as free-form external actions
  without barrier/lost-time enforcement, we reproduce the sim-to-cabinet gap
  that makes RL papers untransferable. Deterministic engine-side controllers
  + intent-based TSP requests is the cleaner split.

## Real-world systems (context for what cities run)

### SCOOT — centralized incremental optimization
- Cyclic flow profiles from upstream detectors; split optimizer nudges a few
  seconds per phase change, offset optimizer once per cycle per junction,
  cycle optimizer holds the most-loaded node at 90% saturation; changes are
  small by design ([FHWA STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm)).
- **vs us:** out of scope as a controller, but its *inputs* (per-cycle flow
  profiles, saturation estimates) are exactly the metrics our observability
  layer should produce — [[domain-congestion-metrics]] dependency.

### SCATS — hierarchical plan-selection
- Library of split plans scaled over a common cycle; one critical intersection
  per subsystem; local controllers may shorten/skip phases but must donate
  saved time forward; subsystems merge when cycles converge
  ([FHWA STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm),
  [NASA NTRS](https://ntrs.nasa.gov/api/citations/19930020327/downloads/19930020327.pdf?attachment=true)).
- **vs us:** its "plan library + TOD schedule" pattern is how our scenario
  format should organize timing variants — named plans selected by schedule
  or demand regime, not one monolithic timing block.

### InSync — the state-machine challenger
- Camera-fed cabinet processor; abandons cycle/split/offset entirely for
  "states" (a phase or compatible pair), choosing state/sequence/duration
  against live queues; 900+ intersections in 18 states by 2012
  ([Politecnico survey](https://www.politesi.polimi.it/retrieve/a81cb05a-9988-616b-e053-1605fe0a889a/2013_10_Ketabdari%20(REVISED).pdf));
  FDOT's independent before/after studies of advanced control describe the
  same state-machine mechanics
  ([FDOT BDV32-977-05](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/research/reports/fdot-bdv32-977-05-rpt.pdf)).
- **vs us:** proof that "phases + constraints, no fixed cycle" is
  operationally legitimate — the same shape as max-pressure control, which is
  our recommended adaptive module. InSync's success argues our adaptive option
  doesn't need to fake a cycle it doesn't have.

### Max-pressure — the academic baseline that cities could actually run
- Per-intersection rule: serve the movement maximizing
  Σ s·(q_upstream − q_downstream); throughput-stabilizing, decentralized, no
  forecast needed ([arXiv 2406.19269](https://arxiv.org/html/2406.19269v1),
  [arXiv 2505.05258](https://arxiv.org/html/2505.05258v1)).
- **vs us:** the obvious first adaptive controller to implement: O(#movements)
  per decision, pure function of engine state, deterministic — slots into the
  engine without breaking ADR-0005 replay.

## Cabinet emulation products

- **TS2 Virtual Cabinet (TVC-3500)** emulates a full NEMA TS 2 cabinet —
  TF BIUs, detector-rack BIUs, MMU — speaking SDLC Port 1 frames to a real
  controller for bench testing ([Western Systems](https://www.westernsystems-inc.com/product/ts2-virtual-cabinet/)).
- **SUMO↔Econolite SIL**: SUMO's NEMA module was validated against
  software-in-the-loop Econolite controllers ([SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- **vs us:** there is no *open-source, standalone* ring-barrier library —
  cabinet fidelity exists only inside big sims (SUMO, CORSIM), closed add-ons
  (Vissim RBC/EOS), or hardware test rigs. A clean, documented Go dual-ring
  core would occupy genuinely empty shelf space (see synthesis "Genuine Gap").

## Positioning Summary

| System | Signal model | Actuation fidelity | Coordination | Open? |
|---|---|---|---|---|
| SUMO static/actuated | stage list + state strings | gap-based, no rings | earliestEnd/latestEnd vs cycleTime | yes |
| SUMO NEMA | dual-ring barrier | vehExt/recalls/force-off, SIL-validated | full (offsets TS2/170 style) | yes |
| CORSIM | dual-ring NEMA TS1/TS2 | deepest (variable initial, gap reduction, dual entry…) | permissive periods, yield point | public domain (legacy) |
| Vissim | signal groups + add-ons | up to real vendor firmware (EOS SIL) | yes (commercial) | no |
| MATSim signals | signal groups, lanes | fixed-time default, pluggable | limited | yes |
| CityFlow/RESCO/LibSignal | fixed list + RL actions | none built-in | external RL only | yes |
| **traffic-sim (us, proposed)** | **dual-ring movement-based, primary** | **gap-out/max-out/force-off, recalls, clearance timers on the 100 ms tick** | **master clock = tick count; offsets/force-offs engine-side** | **yes** |
