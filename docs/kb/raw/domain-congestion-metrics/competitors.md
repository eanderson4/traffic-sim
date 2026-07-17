# Prior Art Survey: Congestion Metrics

> Source: web research | Researched: 2026-07-16
> "Competitors" here = the simulators and analysis tools whose metric pipelines set
> the credibility bar our observability system must clear, plus the DOT evaluation
> practice that consumes their outputs. For each: what it computes, how, and what
> we should steal or avoid.

## Simulators with built-in metric pipelines

### SUMO — complete output taxonomy, detectors as first-class objects
- Output families: disaggregated vehicle data (FCD, trajectories, emission output),
  simulated detectors (E1/E2/E3), edge/lane aggregates, queue output, tripinfo,
  summary/statistic outputs — all XML, all opt-in
  ([SUMO Output](https://sumo.dlr.de/docs/Simulation/Output.html)).
- **tripinfo** per completed trip: duration, routeLength, waitingTime (time ≤
  0.1 m/s), waitingCount, timeLoss vs ideal speed, departDelay; optional per-trip
  emission sums ([TripInfo](https://sumo.dlr.de/docs/Simulation/Output/TripInfo.html)).
- E1 loop reports both time-mean (arithmetic) and space-mean (harmonic) speed —
  the docs are explicit about which is which
  ([E1](https://sumo.dlr.de/docs/Simulation/Output/Induction_Loops_Detectors_(E1).html));
  E2 area detector specializes in queue/jam measurement with tunable halting
  thresholds ([E2](https://sumo.dlr.de/docs/Simulation/Output/Lanearea_Detectors_(E2).html)).
- Detector auto-generation scripts for all signalized intersections
  ([SUMO Output](https://sumo.dlr.de/docs/Simulation/Output.html)).
- Multi-seed statistics are a known weak spot: users chain `runSeeds.py` +
  `attributeStats.py`; the devs call the documentation a gap
  ([issue #10870](https://github.com/eclipse-sumo/sumo/issues/10870)).
- No LOS computation, no HCM reports, no built-in reliability measures.
- **vs traffic-sim (us):** the output taxonomy is worth stealing nearly wholesale
  (trip record + detector emulation + network totals), but SUMO's metrics are
  post-hoc files bolted onto the run — ours should be streams on the bus, computed
  from the same trajectory event feed that visualization already consumes, with
  seed-sweep statistics as a first-class runner rather than user-side scripting.

### MATSim — events are the only real output; analysis is downstream
- Standard per-iteration outputs: score statistics, leg travel-distance stats,
  stopwatch, **events file**, plans, leg histograms, trip durations, and LinkStats
  (hourly counts and travel times per link)
  ([STRIDE report §5.2.3.2](https://www.eng.ufl.edu/stride/wp-content/uploads/sites/153/2021/03/STRIDE-Project-B-Final-Report-updated-1.pdf),
  [MATSim book part I](https://www.matsim.org/files/book/partOne-latest.pdf)).
- "The event file is basically the only real output of MATSim and collects all the
  actions of the simulation"; analysis is whatever EventHandlers you write
  ([TechRxiv MATSim-on-HPC paper](https://www.techrxiv.org/users/771945/articles/856451/master/file/data/main_ieee/main_ieee.pdf?inline=true)).
- Travel times per trip feed the *utility score*, which drives replanning —
  metrics are inside the behavioral loop, not just reporting
  ([JTTE traffic-calming paper](https://jtte.chd.edu.cn/cn/article/pdf/preview/10.1016/j.jtte.2023.01.003.pdf)).
- Queue-model mobsim means no car-following trajectories: no wave measurement, no
  stopped-vs-queued distinction, no per-lane delay. Metrics are link-level and
  trip-level only (see [[arch-time-model]] for the QSim mechanics).
- **vs traffic-sim (us):** MATSim validates our NATS design — events as the public
  interface, analysis as interchangeable consumers. But its metric granularity is
  capped by the queue model; our lane-level dynamics support the full HCM Ch.24
  state machine MATSim can't express.

### Vissim — the DOT-practice reference implementation
- Metric primitives: **travel time sections** (per-vehicle actual vs ideal →
  delay), **delay segments** built on them, **queue counters** (begin < 5 km/h,
  end > 10 km/h, max headway 20 m), **data collection points**, and **node
  evaluation** which auto-places all of them per movement and adds person delay
  ([FHWA TAT §4.1.6](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm),
  [Mississauga report](https://www.mississauga.ca/wp-content/uploads/2023/11/Rangeview-Urban-Transportation-Considerations-Sept-2023.pdf),
  [PTV Vissim 3.70 manual](https://pdfcoffee.com/vissim-user-manual-pdf-free.html)).
- Node evaluation also estimates emissions via TRANSYT 7-F formulas + ORNL fleet
  data ([PTV Vissim 2022 help](https://cgi.ptvgroup.com/vision-help/VISSIM_2022_ENG/Content/11_Auswertungen/AuswertungKnotenauswertung.htm)).
- Network results counters: VEHARR, VEHACT, DEMANDLATENT, TRAVTMTOT, DISTTOT,
  DELAYTOT, STOPSAVG — latent demand is a reported quantity
  ([arXiv:2506.11973](https://arxiv.org/html/2506.11973v1)).
- Seed practice: default initial seed 42, +1 per run; agencies require 5–11 runs
  (WSDOT 11 minimum) with results averaged; percentile max-queues reported across
  runs ([Scottsdale](https://www.scottsdaleaz.gov/docs/default-source/scottsdaleaz/transportation/reports-and-studies/2025-supplement-analysis-of-operational-impact-of-lilo-treatments.pdf?sfvrsn=1edc8f58_3),
  [Michigan SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).
- **vs traffic-sim (us):** Vissim is what the engineers we want to convince
  actually use; matching its measurement vocabulary (travel-time sections, queue
  counters with hysteresis, latent demand) is table stakes. Its metrics are
  configured per-network-object and exported to files — we can do better with
  declarative metric definitions in the scenario and streaming aggregates.

### Aimsun Next — HCM Chapter 24 implemented on the fly
- Implements the HCM 2010 Ch.24 (updated toward HCM 7th) trajectory-analysis
  procedures *during* simulation: per-step vehicle states (stopped < 5 mi/h;
  queuing = gap ≤ 20 ft + speed ≤ 1/3 desired; exits at 2/3), per-step delay
  split into segment/queue/stopped, back of queue, % overflow, % queued, % slow,
  PCU density, and LOS grading of approaches, intersections, freeway and urban
  sections ([Aimsun HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Computes the required number of replications for a target confidence/error
  inline; experiment editor has an explicit warmup period
  ([same](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Denied-entry delay surfaced separately as virtual-queue time
  ([same](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- **vs traffic-sim (us):** the closest existing model of what we want — proof that
  the full HCM state machine can run streaming inside a sim loop. We additionally
  want Edie x–t fields (Aimsun doesn't publish them as an analysis surface) and
  the whole pipeline over NATS so real and simulated trajectories share one
  analytics implementation.

### Synchro + SimTraffic — analytical LOS with a microsim sibling
- Synchro reports three signal LOS flavors: HCM LOS (HCM control delay),
  Intersection LOS (percentile-delay method: d1 computed at 10/30/50/70/90th
  percentile Poisson volumes and averaged, plus d3 and a queue-interaction d4),
  and **ICU LOS** graded on intersection capacity utilization (A ≤55% … H >109%),
  deliberately insensitive to signal timing
  ([FHWA TAT §4.1.2](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- Queue output: 50th and 95th percentile queues in feet from closed-form formulas
  (95th percentile volume via Poisson + PHF scaling)
  ([same](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- Stops estimated from delay via a Transyt-7F conversion table (delay 5 s → 84%
  stopping, ≥10 s → 100%) ([same](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- SimTraffic (CORSIM-based micro): total/stopped delay, max & avg & 95th-percentile
  queue (mean + 1.65σ of 2-min maxima), stops with 10 ft/s entry and 15 ft/s
  release, upstream/storage block time, queuing penalty — and no LOS, no density,
  no v/c ([FHWA TAT §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- **vs traffic-sim (us):** the analytical/micro split inside one vendor shows why
  letter grades can't cross tool boundaries; also a menu of derived estimators
  (percentile delay, stops-from-delay) we can compute directly from trajectories
  instead of approximating.

### HCS — the faithful HCM reference
- HCS (McTrans, Univ. of Florida) implements HCM methods exactly: capacity
  equations with full adjustment-factor products, control delay d1+d2+d3, v/c by
  lane group, freeway queue density from a linear flow–density assumption with
  jam density 190 pc/mi/ln; no delay/queue outputs where the HCM has no procedure
  (freeway facilities) — the analyst derives them from speed manually
  ([FHWA TAT §4.1.1](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- DOTs treat it as the default analytical tool whose outputs other tools must be
  comparable to (Illinois, New Mexico, Ohio)
  ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- **vs traffic-sim (us):** HCS is the acceptance-test oracle: our Ch.24-style
  computed delay/density should reproduce HCS-grade LOS on undersaturated cases
  before anyone trusts our oversaturated numbers.

### SIDRA INTERSECTION — the roundabout standard
- Standard outputs: degree of saturation (v/c), average delay, LOS, average and
  95th-percentile queue, stop rate, fuel consumption, operating cost, emissions —
  reported per movement, per lane, and intersection-wide
  ([SIDRA getting started](https://docs.sidrasolutions.com/intersection/intersection/guides/getting-started/),
  [Moyne TIA](https://www.moyne.vic.gov.au/files/assets/public/v/1/planning-applications/2024/pl24096/pl24.096-traffic-impact-assessment.pdf)).
- Richer delay decomposition than HCM: control delay (avg / worst lane / worst
  movement), geometric delay, stop-line delay; models multiple stops in queue
  (queue move-ups), which HCM omits
  ([SIDRA intersection summary](https://www.cliffsnotes.com/study-notes/22369349),
  [SIDRA micro-analytical note](https://www.sidrasolutions.com/media/689/download)).
- DoS acceptance bands used in practice: ≤0.60 excellent … 0.91–1.00 poor, >1.0
  very poor; practical saturation usually 0.9
  ([Moyne TIA](https://www.moyne.vic.gov.au/files/assets/public/v/1/planning-applications/2024/pl24096/pl24.096-traffic-impact-assessment.pdf),
  [Transport for NSW](https://www.transport.nsw.gov.au/sites/default/files/media/documents/rww/projects/01documents/forbes-bridge/camp-street-bridge-ref-appendices.pdf)).
- US DOTs require SIDRA (with HCS) for roundabout analysis; microsim when
  roundabouts interact with a network ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- **vs traffic-sim (us):** for the stop-sign→roundabout game scenario, SIDRA is
  the incumbent answer; our differentiator is showing the *network* effects
  (spillback, waves, reliability) SIDRA's lane-by-lane analytical model can't —
  while still reproducing its approach-level DoS/delay on the isolated case.

### CORSIM / Dynasmart-P (legacy but instructive)
- CORSIM outputs VMT, VHT, delay/veh, move time, queue time, stop time, % storage,
  phase failure, avg/max queue per lane — but no capacity, no v/c, no LOS (needs a
  Transyt-7F postprocessor) ([FHWA TAT §4.1.5](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- Dynasmart-P reports link speed/density/queue plus system VMT/VHT/entry-queue
  time, on 6-second intervals; no variance outputs; queue-identification method
  undocumented even to FHWA ([FHWA TAT §4.1.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- **vs traffic-sim (us):** the "can't answer v/c or LOS from the micro model"
  limitation and Dynasmart's undocumented queue definition are exactly the failure
  modes our documented, trajectory-first metric kernel avoids.

## Practice layer: how DOTs actually compare alternatives

- FHWA's MOE toolbox surveyed DOTs/MPOs: the most-cited measures are LOS, traffic
  volume, VMT, travel time, speed (11/11/10/8/7 of agencies); nine "focus MOEs"
  cover the field: LOS, v/c, travel time, speed, delay, queue, stops, density,
  travel-time variance ([FHWA TAT §2.1–2.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm)).
- Acceptability is judged by letter-grade standards (e.g., LOS D urban / C rural
  is the modal design target across ~16 state DOTs); alternatives comparison uses
  continuous MOEs where "less is better" with no acceptability threshold
  ([FHWA TAT §2.2](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm),
  [NCHRP 2025, Table 1](https://nap.nationalacademies.org/read/29143/chapter/4)).
- Full MOE report cards exist: Indiana DOT requires delay, v/c, average and 95th
  percentile queue, aggregate travel time, average travel speed; Ohio DOT requires
  LOS, delay/density, 95th percentile queue, v/c, and queue-storage ratio < 1;
  FDOT publishes per-facility MOE tables (95th-percentile back of queue in ft and
  veh for signals; control + approach delay for roundabouts)
  ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- There is a documented drift away from letter grades toward continuous
  delay/travel-time/reliability measures (NCHRP 3-68 interviews; KYTC: LOS "not
  the most applicable or accurate way to represent the results"; Colorado's OLOS
  is PTI-based) ([FHWA TAT §2.1](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm),
  [NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- Microsim is the required tool exactly where we plan to live: oversaturated
  conditions, interacting queues, closely spaced intersections — where HCM
  analytical methods are disclaimed ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- **vs traffic-sim (us):** the DOT report card *is* our spec for "decision-grade":
  if our scenario comparison emits their MOE list with their definitions and
  confidence intervals, we speak their language on day one — and the
  letter-vs-continuous drift validates ranking on continuous metrics with LOS as a
  communication skin.
