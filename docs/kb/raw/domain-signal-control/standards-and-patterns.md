# Standards & Patterns: Signal Control

> Source: standards documents + pattern identification | Researched: 2026-07-16

## Standards

### NEMA TS-1 / TS-2 — the cabinet and its function vocabulary
- TS-1 (published in the 1970s) standardized controller functional interfaces
  in parallel with
  the Caltrans/NYSDOT Model 170 hardware spec; volume-density functions
  (variable initial, gap reduction) predate both, from 1950s "Automatic 1022"
  controllers ([FHWA Signal Timing Process §3](https://ops.fhwa.dot.gov/arterial_mgmt/rpt/sig_tim_proc/sect_3.htm)).
- TS-2 (1992, revised 2003) standardized the *cabinet assembly*: Type 1 =
  SDLC Port 1 serial bus linking controller unit, MMU, detector BIUs,
  terminals (no TS-1 compatibility); Type 2 = TS-1-style MS connectors
  ([GlobalSpec TS-2 scope](https://standards.globalspec.com/std/14478563/ts-2),
  [CED Engineering overview](https://www.cedengineering.com/userfiles/C02-056%20-%20Traffic%20Signal%20Controllers%20-%20US.pdf)).
- The MMU conflict monitor is a hardware watchdog on 16 channels × R/Y/G that
  forces flash on conflicting or absent outputs
  ([EDI MMU-16E](https://www.orangetraffic.com/product/edi-mmu-16e-malfunction-management-unit/)).
  **Simulation analog: the barrier matrix is our MMU** — conflicting-concurrent
  greens should be unrepresentable (compile-time), not just checked at runtime.
- ATC lineage: modern controllers are Linux computers behind the same cabinet
  interface ([City of Tacoma Cobalt spec](https://cms.tacoma.gov/purchasing/formalbids/PW25-0197F_Add2.pdf)).

### NTCIP — the data model of a controller
- **NTCIP 1202** "Object Definitions for Actuated Signal Controller (ASC)
  Units": SNMP-accessible MIB for phase, detector, ring, overlap, channel,
  pattern, schedule, and report objects — the closest thing to a standardized
  *signal program schema* ([NTCIP 1202 v03A](https://www.ntcip.org/file/2019/07/NTCIP-1202v0328A.pdf),
  [role summary](https://www.scag.ca.gov/sites/default/files/2024-05/inland_empire_2005update_app_d_its_standards_submittal.pdf)).
- **All timers in tenths of seconds** (e.g. "Phase Added Initial Parameter in
  tenths of seconds (0–25.5 sec)"; gap-reduction parameters likewise)
  ([NTCIP 1202 v01.07](https://www.ntcip.org/file/2018/11/NTCIP1202v0107d.pdf),
  [v02.19](https://www.ntcip.org/wp-content/uploads/2018/11/NTCIP1202v0219f.pdf)).
  NEMA-faithful timers are *exactly* ADR-0005's 100 ms tick — zero rounding
  loss, a coincidence worth designing around.
- **NTCIP 1211** (Signal Control & Prioritization): PRG → PRS → controller
  message architecture for TSP/EVP; priority methods = green
  extension/reduction, phase sequence change
  ([NTCIP 1211 v02](https://www.ntcip.org/file/2018/11/NTCIP1211-v0224j.pdf),
  [NACTO handbook](https://nacto.org/wp-content/uploads/transit_signal_priority_handbook_smith.pdf)).
- Field deployments add business rules on top: conditional priority (only when
  behind schedule), no skipping demanded phases, EVP > TSP precedence, full
  event logging ([MassDOT ATC spec](https://www.mass.gov/doc/2025-standard-specifications-for-highways-and-bridges/download),
  [Monroe County study](https://www.gtcmpo.org/sites/default/files/pdf/2025/monroe_county_traffic_signal_preemption_study_final_report_reduced.pdf)).

### MUTCD — when signals exist and how they must behave
- **Warrants (Ch 4C)**: nine warrants — 8-hour volume, 4-hour volume, peak
  hour, pedestrian volume, school crossing, coordinated signal system, crash
  experience, roadway network, near-grade-crossing; satisfying a warrant is
  necessary-but-not-sufficient (engineering study still required)
  ([MUTCD 2009 Ch 4C](https://mutcd.fhwa.dot.gov/htm/2009/part4/part4c.htm));
  the 11th edition (effective 2024-01-18) retains the nine-warrant structure
  ([FDOT MUTS 2026](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/traffic/trafficservices/studies/muts/muts-2025/manual-on-uniform-traffic-studies-(muts)-2026.pdf?sfvrsn=2d2fb9ee_1),
  [11th-ed timeline](https://carmanah.com/resources/rulemaking-mutcd-traffic-control-devices/)).
  **Scenario-authoring use**: a warrant check is a cheap plausibility gate on
  authored networks ("would this signal exist?").
- **Yellow change interval**: 3–6 s guidance; determination "using engineering
  practices" ([Kittelson](https://www.kittelson.com/ideas/how-long-should-a-yellow-light-be/)).
- **Pedestrian intervals (Ch 4E)**: clearance at 3.5 ft/s; countdown mandatory
  when change interval > 7 s ([MUTCD 2009 Ch 4E](https://mutcd.fhwa.dot.gov/HTM/2009/part4/part4e.htm));
  LPI ≥ 3 s ([MUTCD Part 4](https://mutcd.fhwa.dot.gov/pdfs/2009r1r2/part4.pdf)).
- **Coordination**: signals within 0.5 mi should be coordinated unless on
  different cycles ([FHWA STM ch6 citing MUTCD](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).

### ITE / NCHRP / HCM — the engineering methods
- **ITE kinematic change-period formula**: CP = t + V/(2a + 64.4g) + (W+L)/V,
  t ≈ 1 s, a ≈ 10 ft/s², L ≈ 20 ft ([FHWA-HRT-04-091](https://pdhonline.com/courses/c337/FHWA-HRT-04-091.pdf));
  ~70% of agencies use it ([FHWA HOP-23-037](https://ops.fhwa.dot.gov/publications/fhwahop23037/fhwahop23037.pdf));
  NCHRP 731 is the consolidated guideline ([NCHRP 731](https://onlinepubs.trb.org/onlinepubs/nchrp/docs/NCHRP03-95_FR.pdf)).
- **HCM signalized-intersection LOS** (control delay s/veh): A ≤10, B 10–20,
  C 20–35, D 35–55, E 55–80, F >80 ([FHWA STM ch3 table 3-3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm))
  — the ranking vocabulary the civic-advocacy audience already speaks;
  [[domain-congestion-metrics]] owns the full treatment.
- **Webster 1958**: C_opt = (1.5L+5)/(1−Y), practical range ~40–120 s
  ([arXiv 2602.21852](https://arxiv.org/html/2602.21852v1),
  [bounds walkthrough](https://www.mysimulator.uk/content/articles/traffic-intersection-optimization.html),
  [FHWA STM ch6 fig 6-19](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
- **MAXBAND lineage**: Morgan & Little 1964 → Little 1966 MILP bandwidth
  maximization → MAXBAND-86 (phase sequence) → MULTIBAND-96 (variable bands)
  ([survey refs](https://bcpublication.org/index.php/FSE/article/view/7195),
  [Wei et al.](https://arxiv.org/pdf/1904.08117v3)).
- **GHM dilemma zone**: Gazis–Herman–Maradudin 1960, *Operations Research*
  8(1):112–132 ([TPF summary](https://pooledfund.org/details/study/697)).
- **ATSPM/Purdue measure suite**: phase-termination classification (gap-out /
  max-out / force-off / ped), split monitor, split failure (stop-bar occupancy
  in green + first 5 s of red), coordination diagrams — the de-facto standard
  post-hoc analysis format ([measure table](https://pdfs.semanticscholar.org/30a9/8b19268ce3a482249ed144dab3b1523aeac0.pdf),
  [PIARC overview](https://www.its-knihovna.cz/CDV/media/ITS-Knihovna/Projekty%20a%20studie/big%20data/PIARC-Big-Data-for-Road-Network-Operations.pdf)).

## Design Patterns Identified

### Ring-barrier state machine (the controller core)
Two (or more) rings advance independently through their phase sequences;
concurrency allowed only within a barrier side; barrier crossing is
synchronized across rings. Everything else — actuation, coordination,
recalls — is timers and calls layered on this skeleton
([FHWA STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm),
[SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html)).
**This is a scheduled-event-list citizen**: each timer expiry (min green met,
passage expired, force-off reached) is an engine event on the tick grid —
ADR-0005's internal event list hosts them natively.

### Timing plan as data, three equivalent projections
The field thinks in (cycle, split, offset); the controller executes (yield
point, force-off, permissive period); CORSIM converts the former to the
latter at load time ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
Pattern: store practitioner form (cycle/split/offset) in the scenario;
compile to executor form in the engine — mirrors our scenario-format split
between authored intent and compiled runtime ([[concept-scenario-format]]).

### Master-clock coordination
Real systems distribute a wall-clock master (midnight sync reference, GPS)
and express offsets against it ([FHWA STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
**Our tick count is a perfect master clock** — coordination requires no
communication between intersections at all, only that each controller compute
`(tick × Δt − offset) mod cycle`. Offset drift, a whole real-world failure
class (DST bugs, clock skew — Indiana 2006 reconfiguration,
[STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)),
simply cannot occur.

### Detector abstraction = lane-area queries
Stop-bar presence detectors and upstream passage detectors are both just
spatial queries over the lane graph (occupied? time-since-last-actuation?).
SUMO auto-generates loops at computed offsets; CORSIM places presence/passage
detectors by role ([SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html),
[App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
Depends on [[arch-road-graph-model]] for lane geometry.

### Phase termination as first-class telemetry
ATSPM's entire analytics stack keys off *why* a phase ended
([Purdue Phase Termination](https://pdfs.semanticscholar.org/30a9/8b19268ce3a482249ed144dab3b1523aeac0.pdf)).
Pattern: emit `(tick, phase, new_state, termination_reason)` events on NATS —
viz, metrics, and validation all consume one stream ([[arch-nats-backbone]]).

### Warrant-gated scenario presets
MUTCD warrants + FHWA planning defaults (60/90/120 s cycles,
[STM ch3 table 3-4](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm))
+ Webster cycle sizing = an authoring wizard that produces defensible plans
without the author knowing traffic engineering.

## Anti-patterns (documented or argued failures)

1. **Instant color flips.** Skipping yellow/all-red and start-up lost time
   inflates capacity and destroys delay credibility — the HCM burns 4 s/phase
   by default ([STM ch3 §3.3.3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm)),
   and LightSim had to add switching-cost awareness to keep max-pressure from
   collapsing capacity ([arXiv 2602.21852](https://arxiv.org/html/2602.21852v1)).
2. **RL-style phase-hopping as the control interface.** External agents
   switching phases per action-step, with no barrier constraints, min greens,
   or clearance machinery — the CityFlow/RESCO norm
   ([CityFlow roadnet](https://cityflow.readthedocs.io/en/latest/roadnet.html),
   [RESCO](https://people.engr.tamu.edu/guni/papers/NeurIPS-signals.pdf)) —
   produces controllers that cannot exist in a cabinet and metrics that don't
   transfer.
3. **Signal control as an external networked controller.** Phase changes
   arriving over a message bus would inject latency/nondeterminism into the
   safety core and violate ADR-0005 (phase changes are engine-scheduled
   events). External inputs belong one level up: TSP *requests* and plan
   *selection*, applied at tick boundaries like any intent.
4. **Offsets without a master clock.** Free-running "offset from my neighbor"
   definitions drift; the field solved this with sync references decades ago
   ([STM ch6 §6.3.4](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm)).
5. **Conflating phases with stages/transitions.** SUMO's flat phase list
   (new phase on any signal change) makes practitioner timing sheets painful
   to import ([SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html)).
   Keep movements/timers primary; derive display states.
6. **Simulating pedestrians to get pedestrian delay.** CORSIM documents that
   ped *call emulation* suffices and <100 crossings/h is MOE-irrelevant
   ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)) —
   important given VISION's pedestrian non-goal.
7. **Non-deterministic adaptive logic in the engine.** Any adaptive module
   (max-pressure etc.) must be a pure function of engine state + seeded RNG,
   or replay (ADR-0005) shatters — same rule as vehicle dynamics.

## Empirical anchors

| Quantity | Value | Source |
|---|---|---|
| Controller timer resolution | 0.1 s (tenths) | [NTCIP 1202](https://www.ntcip.org/file/2019/07/NTCIP-1202v0328A.pdf) |
| ATSPM event-log resolution | 0.1 s | [UNR thesis](https://scholarwolf.unr.edu/server/api/core/bitstreams/502a0d42-b1dd-4048-a717-771f7e82cf3e/content) |
| Yellow change interval | 3–6 s | [MUTCD via Kittelson](https://www.kittelson.com/ideas/how-long-should-a-yellow-light-be/) |
| Walk / ped clearance | 4–7 s / 3.5 ft/s | [MUTCD 4E](https://mutcd.fhwa.dot.gov/HTM/2009/part4/part4e.htm) |
| Ped clearance examples | 11/17/23/29 s @ 40/60/80/100 ft | [STM 2nd ed](https://nap.nationalacademies.org/read/22097/chapter/7) |
| LPI | ≥3 s | [MUTCD Part 4](https://mutcd.fhwa.dot.gov/pdfs/2009r1r2/part4.pdf) |
| Typical cycles | 60/90/120 s planning; Webster ~40–120 s | [STM ch3 t3-4](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm), [arXiv 2602.21852](https://arxiv.org/html/2602.21852v1) |
| Saturation flow | 1,500–2,000 veh/h/ln; ideal 1,900 | [STM ch3 §3.3.2](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm) |
| Start-up lost time / total lost time | ~2 s / 4 s per phase | [STM ch3 §3.3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm) |
| v/c stability threshold | 0.85 | [STM ch3 t3-2](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm) |
| HCM LOS A/F bounds | ≤10 / >80 s/veh delay | [STM ch3 t3-3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm) |
| Coordination spacing | ≤0.5 mi (MUTCD) / ≤¾ mi (Shoestring) | [STM ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm) |
| Red revert | ~2 s factory | [App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm) |
| SUMO actuated defaults | max-gap 3.0 s, detector-gap 2.0 s, min/max green 5–50 s | [SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html) |
| SCOOT cycle target | 90% saturation at critical node | [STM ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm) |
| InSync deployment | 900+ intersections, 18 states (2012) | [Politecnico](https://www.politesi.polimi.it/retrieve/a81cb05a-9988-616b-e053-1605fe0a889a/2013_10_Ketabdari%20(REVISED).pdf) |

## Open Questions

- Permissive-period and dual-entry fidelity: needed for credible coordinated
  corridors, or is (yield point + fixed/floating force-off + early return)
  enough for v1? CORSIM suggests the full set matters for lead-lag bandwidth
  tricks ([App F](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm)).
- Dilemma-zone protection (advance detectors, green-extension systems) — in
  scope for high-speed rural scenarios, or noise at urban speeds?
- Whether OSM-derived European-style networks need a stage-based compatibility
  import path ([[integration-osm-extraction]] dependency).
- How much of ATSPM's measure suite our metrics layer should reproduce
  natively ([[domain-congestion-metrics]] dependency).
- TSP request subject design on NATS and its replay semantics
  ([[arch-nats-backbone]] dependency).

## Master source list

FHWA: [STM ch3](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter3.htm) /
[ch6](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter6.htm) /
[ch9](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm) /
[STM 2nd ed (NAP)](https://nap.nationalacademies.org/read/22097/chapter/7) /
[TAT Vol 4 App F (CORSIM actuated)](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol4/app_f.htm) /
[Signal Timing Process](https://ops.fhwa.dot.gov/arterial_mgmt/rpt/sig_tim_proc/sect_3.htm) /
[HOP-23-037 clearance synthesis](https://ops.fhwa.dot.gov/publications/fhwahop23037/fhwahop23037.pdf) /
[HRT-04-091 informational guide](https://pdhonline.com/courses/c337/FHWA-HRT-04-091.pdf) —
MUTCD: [2009 Ch4C](https://mutcd.fhwa.dot.gov/htm/2009/part4/part4c.htm) /
[Ch4E](https://mutcd.fhwa.dot.gov/HTM/2009/part4/part4e.htm) /
[Part 4 PDF](https://mutcd.fhwa.dot.gov/pdfs/2009r1r2/part4.pdf) /
[11th-ed timeline](https://carmanah.com/resources/rulemaking-mutcd-traffic-control-devices/) /
[FDOT MUTS (11th-ed warrants)](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/traffic/trafficservices/studies/muts/muts-2025/manual-on-uniform-traffic-studies-(muts)-2026.pdf?sfvrsn=2d2fb9ee_1) —
Standards: [NTCIP 1202 v03A](https://www.ntcip.org/file/2019/07/NTCIP-1202v0328A.pdf) /
[1202 v01.07](https://www.ntcip.org/file/2018/11/NTCIP1202v0107d.pdf) /
[1211 v02](https://www.ntcip.org/file/2018/11/NTCIP1211-v0224j.pdf) /
[GlobalSpec TS-2](https://standards.globalspec.com/std/14478563/ts-2) /
[CED TS-2 overview](https://www.cedengineering.com/userfiles/C02-056%20-%20Traffic%20Signal%20Controllers%20-%20US.pdf) /
[EDI MMU-16E](https://www.orangetraffic.com/product/edi-mmu-16e-malfunction-management-unit/) —
Guidelines: [NCHRP 731](https://onlinepubs.trb.org/onlinepubs/nchrp/docs/NCHRP03-95_FR.pdf) /
[Bonneson handbook](https://static.tti.tamu.edu/tti.tamu.edu/documents/0-6402-P1.pdf) /
[Kittelson yellow](https://www.kittelson.com/ideas/how-long-should-a-yellow-light-be/) /
[NACTO TSP](https://nacto.org/wp-content/uploads/transit_signal_priority_handbook_smith.pdf) /
[MassDOT ATC spec](https://www.mass.gov/doc/2025-standard-specifications-for-highways-and-bridges/download) —
Classic methods: [GHM 1960 via TPF](https://pooledfund.org/details/study/697) /
[MAXBAND refs](https://bcpublication.org/index.php/FSE/article/view/7195) /
[Wei et al. survey](https://arxiv.org/pdf/1904.08117v3) /
[Webster via LightSim](https://arxiv.org/html/2602.21852v1) —
Adaptive: [STM ch9 SCOOT/SCATS](https://ops.fhwa.dot.gov/publications/fhwahop08024/chapter9.htm) /
[SCATS NTRS](https://ntrs.nasa.gov/api/citations/19930020327/downloads/19930020327.pdf?attachment=true) /
[InSync Politecnico](https://www.politesi.polimi.it/retrieve/a81cb05a-9988-616b-e053-1605fe0a889a/2013_10_Ketabdari%20(REVISED).pdf) /
[FDOT BDV32-977-05](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/research/reports/fdot-bdv32-977-05-rpt.pdf) /
[Max-pressure OCC-MP](https://arxiv.org/html/2406.19269v1) /
[CV-MP](https://arxiv.org/html/2505.05258v1) —
Simulators: [SUMO TLS](https://sumo.dlr.de/docs/Simulation/Traffic_Lights.html) /
[SUMO NEMA](https://sumo.dlr.de/docs/Simulation/NEMA.html) /
[MATSim signals](https://github.com/matsim-org/matsim-libs/tree/master/contribs/signals) /
[CityFlow roadnet](https://cityflow.readthedocs.io/en/latest/roadnet.html) /
[PTV FAQ](https://www.ptvgroup.com/en-us/products/ptv-vissim/faqs) /
[RESCO](https://people.engr.tamu.edu/guni/papers/NeurIPS-signals.pdf) /
[LibSignal](https://arxiv.org/abs/2211.10649) /
[SUMO-RL](https://thomasez.folk.ntnu.no/itc34/workshop%20papers/6.pdf) —
Data/telemetry: [ATSPM measures](https://pdfs.semanticscholar.org/30a9/8b19268ce3a482249ed144dab3b1523aeac0.pdf) /
[UNR high-res thesis](https://scholarwolf.unr.edu/server/api/core/bitstreams/502a0d42-b1dd-4048-a717-771f7e82cf3e/content) /
[OSM traffic_signals](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dtraffic_signals) /
[Toronto open data](https://www.toronto.ca/legdocs/mmis/2026/cc/bgrd/backgroundfile-286314.pdf) —
Cabinets: [TS2 Virtual Cabinet](https://www.westernsystems-inc.com/product/ts2-virtual-cabinet/) /
[Tacoma Cobalt spec](https://cms.tacoma.gov/purchasing/formalbids/PW25-0197F_Add2.pdf)
