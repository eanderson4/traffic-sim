# Mechanics: Signal Control

> Source: web research (greenfield — no engine code exists; this file collects the
> *mechanisms* signal control is built from, to be re-audited against real code once
> a signal-control ADR lands and the ring-barrier core exists) | Researched: 2026-07-16 | Git HEAD: ae75fba

## 1. The vocabulary layer: movements, phases, rings, barriers

The North American structure every serious US-facing sim ends up emulating:

- **Movement → phase.** A NEMA phase is defined by a single traffic *movement* at
  an intersection; multiple NEMA phases may be active concurrently if they don't
  conflict. Numbering convention: odd = left turns, even = through+right;
  phases 2 and 6 are usually the main street; an 8-phase maximum is standard and
  an intersection need not use all 8
  ([SUMO NEMA docs](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- **Ring** = two or more sequentially timed, individually selected *conflicting*
  phases in an established order; one phase per ring active at a time.
  **Barrier** = the safety partition between {1,2,5,6} and {3,4,7,8}: phases on
  opposite sides of a barrier are never concurrent; both rings must cross the
  barrier together ([FHWA TAT Vol 4 App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Ring-barrier beats stage-based for actuation.** With a fixed stage sequence
  (European/SUMO-default style), actuation can only stretch stages; with rings,
  an early gap-out on phase 1 lets ring 1 proceed to 2 while ring 2 is still in
  5 — phase combinations like 2+5 appear that no stage list contains
  ([SUMO NEMA docs](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
  FHWA's own diagrams show the same flexibility: leading, lagging, split, and
  single-ring variants are all parameterizations of the same dual-ring object
  ([App F figs 75–79](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Overlaps** = movements (usually right turns on a 5-section head) allowed to
  run with two "parent" phases — e.g. overlap A green under both phase 4 and
  phase 1 ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).

## 2. The timing-plan parameter set (what "retiming a light" actually edits)

- **Cycle** = time for a complete sequence of indications; **split** = per-phase
  share *including yellow + all-red* (green % < split %); splits in a ring sum
  to the cycle, and splits on one side of a barrier must equal the other ring's
  same-side sum ([FHWA STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm),
  [App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Offset** = time relationship between a local *offset reference point* and a
  shared **master clock**. The reference point differs by controller type:
  NEMA TS1 = start of coordinated phases together; NEMA TS2 = start of green of
  the *first* coordinated phase; Type 170 = start of coordinated-phase yellow —
  only the last is directly observable in the field
  ([FHWA STM ch6 fig 6-7](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
  SUMO's NEMA module exposes exactly this as `controllerType` = TS2 or Type 170,
  and notes its TS2 offset behavior was **validated against software-in-the-loop
  Econolite controllers** ([SUMO NEMA docs](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- **Master clock** = a background time reference (midnight or a configurable
  sync reference, e.g. 2:00 AM) shared by all controllers in a system; offsets
  are relative to it, and modern controllers keep it via GPS time references
  ([FHWA STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm),
  [FHWA interconnected-signals guidance](https://highways.dot.gov/safety/hsip/xings/recording-devices-interconnected-grade-crossing-and-intersection-signal-systems-7)).
  **This maps 1:1 onto ADR-0005's tick count** — our engine's tick counter IS the
  master clock, with none of the real world's clock-drift/DST problems.
- Planning-level cycle lengths: 60 s (permissive lefts), 90 s (protected or
  protected-permissive on one street), 120 s (protected both streets); v/c <
  0.85 is the undersaturation threshold; intersection capacity ~1,530 vphpl in
  the HCM quick-estimation method ([FHWA STM ch3 tables 3-2/3-4](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm)).

## 3. Fixed-time (pretimed) control and its design math

- Pretimed = same plan regardless of demand; still common in dense grids with
  short blocks, and most "pretimed" systems today are actuated controllers with
  all phases on max recall ([FHWA STM ch6 §6.3.6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
- **Capacity model**: c = s·g/C — saturation flow × effective-green fraction.
  Saturation flow ranges 1,500–2,000 veh/h/ln observed; ideal is 1,900 pc/h/ln;
  effective green = green − start-up lost time (~2 s) − clearance lost time;
  HCM default total lost time = 4 s/phase
  ([FHWA STM ch3 §3.3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm)).
- **Webster's optimum cycle** (1958, delay-minimizing, undersaturated):
  C_opt = (1.5L + 5)/(1 − Y), L = total lost time per cycle, Y = sum of critical
  flow ratios; practical bounds ~40–120 s
  ([LightSim controller docs, arXiv 2602.21852](https://arxiv.org/html/2602.21852v1),
  [formula walkthrough](https://www.mysimulator.uk/content/articles/traffic-intersection-optimization.html));
  FHWA's manual teaches it alongside the HCM cycle-estimation figure
  ([STM ch6 figs 6-19/6-20](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
- Webster is a *design-time* formula (compute a plan offline), not a runtime
  controller — exactly the right role for our scenario-authoring presets.

## 4. Actuated control: the timer machine

The NEMA actuated phase is a small state machine of timers — this is the core
mechanism our engine must reproduce:

- **Detectors**: stop-bar presence loops signal demand; upstream passage-mode
  loops extend green / feed volume-density functions
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Green extension loop**: green holds for **minimum green**, then extends by
  **passage time** (unit/vehicle extension) per actuation; the phase
  **gaps out** when a gap > passage time occurs
  ([Bonneson, Traffic Signal Operations Handbook ch2](https://static.tti.tamu.edu/tti.tamu.edu/documents/0-6402-P1.pdf)).
  Termination taxonomy: **gap-out** (gap found), **max-out** (maximum green
  reached under conflicting demand), **force-off** (split exhausted under
  coordination) ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Variable initial**: extend the initial interval by seconds/actuation for
  vehicles counted during the preceding yellow/red, up to a maximum initial —
  lets a queued platoon clear before the passage timer governs
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Gap reduction**: shrink the allowable gap from max-gap to min-gap over a
  "time to reduce" once a conflicting call exists — the classic volume-density
  trick dating to the 1950s "Automatic 1022" volume-density controller
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm),
  [FHWA Signal Timing Process §3](https://ops.fhwa.dot.gov/arterial_mgmt/rpt/sig_tim_proc/sect_3.htm)).
- **Recalls**: min recall = serve at least the minimum every cycle; max recall =
  constant call (≈ fixed time); soft recall / call-to-non-actuated exist in the
  field (CORSIM approximates them with min/max)
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Dual entry**: a lone call on phase 2 also calls compatible phase 6 (pairs
  1+5, 2+6, 3+7, 4+8); common policy is dual entry on even phases only
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Simultaneous gap-out**: both rings must terminate in the same manner before
  crossing the barrier together; disabling it yields shorter cycles
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Red revert**: force ≥ ~2 s of red between yellow and a re-service of the
  same phase ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- **Fully vs semi-actuated**: full = detection everywhere, runs "free" (no
  background cycle); semi = side street only, main street rests in green until
  a call ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).

## 5. Clearance intervals (yellow / all-red) — the safety-critical timers

- **MUTCD guidance**: yellow change interval 3–6 s, longer for higher-speed
  approaches; ~70% of surveyed agencies compute it with ITE's kinematic equation
  ([Kittelson](https://www.kittelson.com/ideas/how-long-should-a-yellow-light-be/),
  [FHWA pooled-fund synthesis HOP-23-037](https://ops.fhwa.dot.gov/publications/fhwahop23037/fhwahop23037.pdf)).
- **ITE kinematic formula**: CP = t + V/(2a + 64.4g) + (W + L)/V with
  perception-reaction t ≈ 1 s, comfortable decel a ≈ 10 ft/s², vehicle length
  L ≈ 20 ft, intersection width W (US customary)
  ([FHWA-HRT-04-091 Signalized Intersections Informational Guide](https://pdhonline.com/courses/c337/FHWA-HRT-04-091.pdf));
  NCHRP Report 731 is the modern guideline, and documents the folk "speed/10"
  rule-of-thumb as an alternative ([NCHRP 731](https://onlinepubs.trb.org/onlinepubs/nchrp/docs/NCHRP03-95_FR.pdf)).
- **Why it exists**: Gazis–Herman–Maradudin 1960 defined the dilemma zone —
  too close to stop comfortably, too far to clear before red
  ([TPF study summary](https://pooledfund.org/details/study/697),
  DOI [10.1287/opre.8.1.112](https://link.springer.com/article/10.1007/s41062-025-01954-7)).
- **Simulation consequence**: clearance + start-up lost time ≈ 4 s/phase
  ([STM ch3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm)) is
  ~7% of a 60 s cycle. A sim that flips colors instantly overstates capacity
  and understates delay — this is the single most important fidelity line for
  credible signal metrics.

## 6. Pedestrian timing as vehicular constraints (not simulated pedestrians)

- **Walk** 4–7 s (7 recommended, 4 absolute minimum); **pedestrian clearance**
  (flashing don't walk) sized at **3.5 ft/s** walking speed over crossing
  distance — e.g. 11/17/23/29 s for 40/60/80/100 ft crossings; FDW may overlap
  the vehicular yellow+all-red ([MUTCD 2009 ch4E](https://mutcd.fhwa.dot.gov/HTM/2009/part4/part4e.htm),
  [Signal Timing Manual 2nd ed ch6](https://nap.nationalacademies.org/read/22097/chapter/7)).
- These times act as **minimum-green constraints** on the parallel vehicular
  phase: ped walk + clearance bounds the phase from below
  ([NIATT lab manual](https://www.webpages.uidaho.edu/niatt_labmanual/Chapters/signaltimingdesign/theoryandconcepts/PedestrianCrossingMinimumGreen.htm)).
- **Leading pedestrian interval**: WALK ≥ 3 s before the parallel vehicular
  green ([MUTCD Part 4](https://mutcd.fhwa.dot.gov/pdfs/2009r1r2/part4.pdf)).
- **Precedent for timing-only peds**: CORSIM never simulates pedestrians at
  all — it emulates ped *calls* (stochastic/deterministic/continuous demand
  modes) and ignores ped demand below 100 crossings/h as MOE-irrelevant
  ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
  VISION.md lists pedestrians as a non-goal; CORSIM shows the field-standard
  middle path: model their timing impact, not their bodies.

## 7. Coordination: green waves and their machinery

- **Structure**: all controllers share a background cycle (double-cycling
  allowed); one coordinated phase (usually 2+6, the main street) is guaranteed
  its split every cycle; non-coordinated phases serve demand between **yield
  point** and **force-off**, inside **permissive periods**; unused time returns
  to the coordinated phase (**early return to green**)
  ([FHWA STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
- **Fixed vs floating force-off**: fixed = force-off points pinned to the cycle,
  later phases may inherit unused time; floating = each non-coordinated phase
  capped at its split, all slack to the coordinated phase. TTI documents the
  trade: fixed helps cyclic side-street demand and suppresses premature
  platoon release; floating maximizes arterial green
  ([STM ch6 fig 6-6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
- **Time-space diagram** is the design tool; **bandwidth** = the window of
  green through which a platoon progresses without stopping. MAXBAND (Little
  1966, *Operations Research* 14:568–594) formulates offset selection as
  mixed-integer LP maximizing two-way bandwidth under a common cycle;
  MULTIBAND (Gartner 1991) generalizes to variable-width bands; MAXBAND-86 adds
  left-turn phase sequence optimization ([Little via survey refs](https://bcpublication.org/index.php/FSE/article/view/7195),
  [Wei et al. survey §MAXBAND](https://arxiv.org/pdf/1904.08117v3),
  [MAXBAND program](https://www.academia.edu/49760027/MAXBAND_A_program_for_setting_signals_on_arteries_and_triangular_networks)).
- **When to coordinate**: MUTCD guidance — signals within 0.5 mi on a corridor
  "should be coordinated" unless on different cycles; FHWA's *Signal Timing on
  a Shoestring* uses ¾ mile as the review threshold
  ([STM ch6 §6.2](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
- **Transition logic**: when a time-of-day plan change or a clock correction
  requires re-syncing to the master clock, controllers walk offsets over
  several cycles rather than jumping — a whole sub-mechanism of real cabinets
  ([STM ch6 §6.5](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).

## 8. Adaptive systems (what cities actually buy)

- **SCOOT** (TRL, Hunt et al. 1981): upstream detectors feed *cyclic flow
  profiles*; three incremental optimizers nudge **split** (a few seconds at a
  phase change), **offset** (once per cycle per junction), and **cycle** (to
  hold the most-loaded node at 90% saturation)
  ([FHWA STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm)).
  Centralized; gradual by design.
- **SCATS** (RTA NSW, Sims & Dobinson ~1980): hierarchical — local controllers
  may shorten/skip phases (saved time passes to the next phase); a regional
  computer picks split plans from a **library** for each subsystem around one
  critical intersection sharing a common cycle; subsystems merge/dissolve as
  cycles converge ([FHWA STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm),
  [NASA NTRS summary](https://ntrs.nasa.gov/api/citations/19930020327/downloads/19930020327.pdf?attachment=true)).
- **InSync** (Rhythm Engineering, 2005): cabinet-resident **state machine**
  fed by cameras (queue + delay per approach); "completely digital," abandons
  cycle/split/offset for *states* = a phase or compatible phase pair, choosing
  state, sequence, and green duration per demand; 900+ intersections across 18
  US states by March 2012 ([Politecnico thesis survey](https://www.politesi.polimi.it/retrieve/a81cb05a-9988-616b-e053-1605fe0a889a/2013_10_Ketabdari%20(REVISED).pdf),
  [FDOT BDV32-977-05](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/research/reports/fdot-bdv32-977-05-rpt.pdf)).
- **Max-pressure** (Varaiya 2013; descends from Tassiulas–Ephremides packet
  scheduling): decentralized per-intersection rule — serve the phase with max
  Σ saturation-flow-weighted (upstream queue − downstream queue); provably
  throughput-stabilizing under assumptions, needs no demand forecast
  ([OCC-MP, arXiv 2406.19269](https://arxiv.org/html/2406.19269v1),
  [CV-MP, arXiv 2505.05258](https://arxiv.org/html/2505.05258v1)).
  Cheap to compute, deterministic given state — the natural "adaptive v1" for
  an engine like ours.

## 9. What the cabinet actually runs (NEMA TS-1/TS-2, NTCIP)

- **NEMA TS-1** (1970s, alongside Caltrans/NYSDOT's Model 170) standardized the
  functional interface; **NEMA TS-2** (1992; 2003 revision current) standardized
  the cabinet: Type 1 uses the SDLC **Port 1** high-speed serial bus connecting
  controller unit, MMU, detector racks, and terminals; Type 2 keeps TS-1
  A/B/C connectors ([FHWA Signal Timing Process §3](https://ops.fhwa.dot.gov/arterial_mgmt/rpt/sig_tim_proc/sect_3.htm),
  [CED Engineering TS-2 overview](https://www.cedengineering.com/userfiles/C02-056%20-%20Traffic%20Signal%20Controllers%20-%20US.pdf),
  [GlobalSpec TS-2 scope](https://standards.globalspec.com/std/14478563/ts-2)).
- **MMU** (malfunction management unit, formerly conflict monitor): independent
  hardware watching 16 channels × red/yellow/green for conflicting or absent
  indications, forcing flash on fault — the safety layer that makes barrier
  violations a hardware event, not a software bug
  ([EDI MMU-16E](https://www.orangetraffic.com/product/edi-mmu-16e-malfunction-management-unit/)).
  Modern ATC controllers (e.g. Econolite Cobalt) run Linux on top of this
  ([City of Tacoma spec](https://cms.tacoma.gov/purchasing/formalbids/PW25-0197F_Add2.pdf)).
- **NTCIP 1202** is the data model of an actuated controller: MIB objects for
  phase parameters, detector parameters, unit parameters, rings, overlaps,
  patterns, TOD schedules — and **all timers are in tenths of seconds** (e.g.
  "Phase Added Initial Parameter in tenths of seconds (0–25.5 sec)")
  ([NTCIP 1202 v03A](https://www.ntcip.org/file/2019/07/NTCIP-1202v0328A.pdf),
  [v01.07](https://www.ntcip.org/file/2018/11/NTCIP1202v0107d.pdf),
  [explainer](https://lyt.ai/blog/ntcip-1202-what-is-it-why-does-it-matter)).
  **Decisecond timers are exactly ADR-0005's 100 ms tick** — a NEMA-faithful
  controller core loses zero timer resolution on our grid.
- **ATSPM**: modern controllers log every phase/detector event at 0.1 s
  resolution; the ATSPM toolkit turns that into measures like Purdue Phase
  Termination (gap-out vs max-out vs force-off vs ped per phase per cycle),
  Split Monitor, Purdue Coordination Diagram
  ([UNR thesis on high-res controller data](https://scholarwolf.unr.edu/server/api/core/bitstreams/502a0d42-b1dd-4048-a717-771f7e82cf3e/content),
  [ATSPM measure table](https://pdfs.semanticscholar.org/30a9/8b19268ce3a482249ed144dab3b1523aeac0.pdf)).
  This is both a validation corpus and a template for our own event stream.

## 10. How signal programs are expressed as sim input (prior formats)

- **SUMO `tlLogic`**: `type` ∈ {static, actuated, delay_based, NEMA}; a phase =
  duration + per-link state string (`G g y r s u o O` — note lower-case `g` =
  green-yield, encoding permitted left turns); a new `<phase>` is required
  *whenever any signal changes*, so one engineering phase becomes several sim
  phases (transitions); actuated adds `minDur/maxDur` + params
  (`max-gap` 3.0 s, `detector-gap` 2.0 s, `passing-time` 2.0 s defaults);
  coordination via `earliestEnd/latestEnd` + `cycleTime` param
  ([SUMO Traffic Lights](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
- **SUMO NEMA module** (since 1.11.0, built by NREL under US DOE VTO):
  `ring1/ring2` phase lists, `barrierPhases`, per-phase `minDur/maxDur/vehExt/
  yellow/red`, `minRecall/maxRecall`, `coordinate-mode`, `fixForceOff`,
  `controllerType` TS2/170, ATSPM-style state output
  ([SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
- **CityFlow**: roadnet JSON `lightphases` = `{time, availableRoadLinks[]}` —
  fixed-time phase list only; any responsive control is an external RL action
  (`rlTrafficLight`, default action interval 1.0 s)
  ([CityFlow roadnet docs](https://cityflow.readthedocs.io/en/latest/roadnet.html),
  [quick start](https://cityflow.readthedocs.io/en/latest/start.html)).
- **MATSim signals contrib**: signal *groups* over lanes with a fixed-time
  default controller; traffic-responsive controllers pluggable per intersection
  ([matsim-libs contribs/signals README](https://github.com/matsim-org/matsim-libs/tree/master/contribs/signals)).
- Format takeaway: two lineages — **stage-based** (SUMO default, CityFlow,
  MATSim groups: enumerate all concurrent states) vs **movement-based**
  (NEMA ring-barrier: enumerate per-movement timers + concurrency rules).
  The NEMA form is smaller, closer to what practitioners author, and carries
  the safety invariants explicitly.

## 11. Transit signal priority and preemption (mechanics, briefly)

- **TSP ≠ preemption**: preemption interrupts normal operation (rail, emergency);
  TSP issues a *request the controller may deny*; normal cycle structure and
  coordination are preserved, and side-street phases are not skipped
  ([FHWA STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm)).
- **NTCIP 1211** standardizes the Signal Control & Prioritization architecture:
  Priority Request Generator (on the vehicle/wayside) → Priority Request Server
  (in the cabinet) → controller; priority achieved by extending/shortening
  greens or reordering phases ([NTCIP 1211 v02](https://www.ntcip.org/file/2018/11/NTCIP1211-v0224j.pdf),
  [NACTO TSP handbook](https://nacto.org/wp-content/uploads/transit_signal_priority_handbook_smith.pdf)).
- **Conditional vs unconditional**: modern practice requests priority only when
  behind schedule / above load threshold
  ([Monroe County preemption study](https://www.gtcmpo.org/sites/default/files/pdf/2025/monroe_county_traffic_signal_preemption_study_final_report_reduced.pdf));
  agency specs require: green extension + non-priority truncation, no skipping
  demanded phases, EVP override of TSP, and time-stamped logging of all TSP
  events ([MassDOT ATC spec §M10.02.0.G](https://www.mass.gov/doc/2025-standard-specifications-for-highways-and-bridges/download)).
- **Design fit for us**: a TSP request is an *external intent* applied at a tick
  boundary (ADR-0005-shaped); the grant logic is engine-internal and
  deterministic. SUMO's expression-based custom switching rules already model
  bus priority this way (a `z:dBus` detector term biasing the gap-out
  condition) ([SUMO TLS §bus prioritization](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
