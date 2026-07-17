# Mechanics: Congestion Metrics

> Source: web research (greenfield — the only observability code that exists is the
> NGSIM x–t Edie prototype in `analysis/ngsim`; this file collects the *mechanisms*
> a congestion-metrics subsystem is built from, to be re-audited against real code
> once the observability service exists) | Researched: 2026-07-16 | Git HEAD: ae75fba

## 1. Edie's generalized definitions — the x–t region integral

The foundational measurement mechanism for everything below is Edie (1963),
"Discussion of Traffic Stream Measurements and Definitions" (2nd Int. Symposium on
the Theory of Traffic Flow, pp. 139–154,
[citation record](https://www.maxapress.com/app/reference/65a785fafa6c583a2fcd9868)):

- For a time–space region **A** with area |A|: flow **q(A) = d(A)/|A|**, density
  **k(A) = t(A)/|A|**, speed **v(A) = d(A)/t(A)**, where d(A) is total distance
  traveled and t(A) total time spent by all vehicles inside A
  ([Seo 2015 probe-data paper, eqs. 4–6](https://toruseo.jp/paper/Seo2015probe.pdf),
  [textbook treatment, Fig. 7.5](https://kuliahtransportasi.files.wordpress.com/2018/03/ngi_week1_readings_thetransportsystemandtransportpolicy.pdf)).
- Because q, k, v come from *integrals* (accumulated distance and time) rather than
  point samples or derivatives, the identity **q = k·v holds exactly** within the
  region and the estimates are robust to position noise — the property we exploited
  in `analysis/ngsim` (25 ft × 3 s cells on NGSIM I-80, 1.52 M sample pairs,
  measured −18.1 km/h wave; see README there).
- Edie's is "the most widely used method for measuring macroscopic traffic flow
  variables when trajectory data of a traffic stream is available"
  ([arXiv:2512.21425](https://arxiv.org/html/2512.21425v3)) — and a microsim *has*
  full trajectories by construction, so this mechanism costs nothing extra.
- HCM 2010 Chapter 24 settled the same point for practice: "vehicle trajectory
  analysis is the only approach to develop performance measures that are consistent
  with HCM definitions, with field measurement techniques, and with other simulation
  tools," and it specifies computational procedures to run "on the fly" during the
  simulation ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).

## 2. Delay: control delay decomposition and actual-vs-ideal

- **Control delay** (the HCM intersection service measure) is defined as the sum of
  "initial deceleration delay, queue move-up time, stopped delay, and final
  acceleration delay" relative to unimpeded traversal
  ([City of Corona TIA appendix](https://cdn.prod.website-files.com/65799af8ef225180fdf1ba2e/672f672f5acb5e5a27ef3a9c_xxApp%20M1%20re.pdf)).
- The analytical HCM formula is **d = d1 (uniform) + d2 (incremental/overflow) +
  d3 (initial queue)**, with d1 from the Webster-style term C(1−g/C)²/[2(1−min(1,X)g/C)]
  and d2 = 900T[(X−1) + √((X−1)² + 8kIX/capT)]
  ([FHWA Traffic Analysis Toolbox, MOE definitions, eqs. 10–12](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- **Vissim's microscopic definition**: per-vehicle delay = actual travel time −
  "ideal" travel time computed with no other vehicles and no control devices
  (reduced speeds for turns still counted); averaged only over vehicles completing
  the travel-time measurement section
  ([FHWA TAT Vol. on MOEs, §4.1.6](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- **Aimsun's HCM Ch.24 implementation** computes a per-time-step delay =
  Δt − (distance traveled in the step)/(desired speed), then accumulates three
  flavors per vehicle per link: *segment delay* (all steps), *queue delay* (steps in
  queuing state), *stopped delay* (steps in stopped state); link outputs average
  over vehicles exiting during the statistics interval
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- **SimTraffic** defines total delay against the *maximum permitted speed* (speed
  limit or safe turning speed), adds denied-entry delay, and reports stopped delay
  as time below 10 ft/s (3 m/s)
  ([FHWA TAT, §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- Mechanism for us: per-vehicle **time-loss accounting** (SUMO's `timeLoss` = time
  lost vs ideal speed including the individual speedFactor;
  [SUMO TripInfo](https://sumo.dlr.de/docs/Simulation/Output/TripInfo.html)) is the
  same actual-vs-ideal primitive; control delay, stopped delay and queue delay are
  all filters over it.

## 3. Queue measurement: state machines with hysteresis

"Queue length" has no single definition; every tool implements a per-vehicle state
machine with entry/exit thresholds, and the numbers differ because the machines
differ.

- **HCM field definition**: back of queue = "position of the vehicle stopped
  farthest from the stop line"; queue counts only *fully stopped* vehicles with a
  **5 mph** stop threshold; field protocol counts stopped vehicles per lane at the
  onset of green per cycle and builds percentiles (50th/85th/90th/95th)
  ([Ohio DOT OATS manual](https://dam.assets.ohio.gov/image/upload/transportation.ohio.gov/traffic/oats/OATS.pdf)).
- **Vissim queue counter**: queue *begins* below 5 km/h (3.1 mph), *ends* above
  10 km/h (6.2 mph) — hysteresis so queue move-ups don't flicker — with a maximum
  headway of 20 m (65.6 ft) to the next queued vehicle
  ([Mississauga DMP report §8.1.3.2](https://www.mississauga.ca/wp-content/uploads/2023/11/Rangeview-Urban-Transportation-Considerations-Sept-2023.pdf),
  [Georgia Tech Vissim module 6](https://gti.gatech.edu/sites/default/files/Module%206%20-%20Vissim%20Data%20and%20Performace%20Metrics%20submitted.pdf)).
- **Aimsun (HCM Ch.24)**: queuing state entered when gap ≤ 20 ft AND speed ≤
  leader's speed AND speed ≤ 1/3 of desired (or first vehicle ≤ 50 ft from stop
  line and decelerating/stopped); exited when speed ≥ 2/3 of desired; stopped state
  at < 5 mi/h
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- **SimTraffic**: queued = below 10 ft/s AND (at stop bar OR following another
  queued vehicle) — so "single vehicle queues are not possible except at stop
  bars"; front-to-front spacing 19.5 ft; queues cannot originate mid-link
  ([FHWA TAT, §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- **SUMO queue output**: lane queueing length measured from the junction to the
  last standing vehicle; an "experimental" variant uses the last vehicle below
  5 km/h ([SUMO QueueOutput](https://sumo.dlr.de/docs/Simulation/Output/QueueOutput.html)).
- **Reported statistics differ too**: SimTraffic samples the max queue every
  2 min, averages those maxima, and estimates the 95th percentile as mean + 1.65σ —
  so its 95th percentile *can exceed its observed maximum*
  ([FHWA TAT, §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
  MassDOT roundabout guidance insists on the **maximum back of queue within the
  recording interval, NOT the average queue**, matched to the field recording
  interval ([MassDOT roundabout Vissim guidance](https://www.mass.gov/doc/massdot-roundabout-vissim-microsimulation-guidance/download)).
- Vissim queue lengths "allow for the full impact of shockwaves to be captured,"
  so they can run longer than Synchro's analytical 95th-percentile queues
  ([Mississauga DMP report](https://www.mississauga.ca/wp-content/uploads/2023/11/Rangeview-Urban-Transportation-Considerations-Sept-2023.pdf)).

## 4. Capacity and v/c: capacity is a measured output, not an input

- HCM defines capacity as "the maximum sustainable flow rate at which vehicles or
  persons reasonably can be expected to traverse a point or uniform segment… during
  a specified time period"; analytical tools compute it with adjustment-factor
  products (e.g., signal lane group c = s·g/C with saturation flow s and ~10
  adjustment factors) ([FHWA TAT, §4.1.1, eqs. 3–5](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- **Microsimulations do not output "capacity"** — "they output the number of
  vehicles that pass a given point. Thus, the analyst must manipulate the input
  demand as necessary to create a queue upstream of the target section… so that the
  model will report the maximum possible flow rate," averaged over ≥ 15 min of
  sustained queue discharge ([FHWA Vol. III calibration, §5.3.2](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect5.htm)).
- v/c follows as demand (or measured volume) over that measured discharge rate;
  HCM practice treats v/c < 1 as the acceptability line, and at signalized
  intersections v/c > 1 forces LOS F regardless of delay
  ([FHWA TAT, §2.2](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm),
  [Apple Valley HCM6 LOS table](https://applevalley.org/wp-content/uploads/2026/02/general-plan-amendment-2022‑007-zone-change-2022‑005-and-tentative-tract-map-20453-notice-of-intent-to-adopt-a-mitigated-negative-declaration-1.pdf)).
- Ohio DOT adds the **queue storage ratio (QSR = 95th-percentile back of queue /
  available storage)** with target < 1 for all movements, plus demand/capacity
  < 0.93 ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- SIDRA reports the same ratio as **degree of saturation** with practical
  saturation usually taken at 0.9 ([Transport for NSW appendix](https://www.transport.nsw.gov.au/sites/default/files/media/documents/rww/projects/01documents/forbes-bridge/camp-street-bridge-ref-appendices.pdf)).

## 5. Throughput and system totals

- The scenario-scale totals the field actually reports: completed trips, VMT, VHT,
  mean speed, total delay, stops. FHWA's MOE toolbox derives VMT/VHT from volume
  and travel time, and delay as ideal-minus-actual travel time
  ([FHWA TAT, §2.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm)).
- Vissim's network results are read off counters like **VEHARR** (vehicles that
  completed trips), **VEHACT** (still active), **DEMANDLATENT** (unfulfilled
  demand), DISTTOT, TRAVTMTOT, DELAYTOT, STOPSAVG
  ([arXiv:2506.11973, §5](https://arxiv.org/html/2506.11973v1)).
- SUMO's `--duration-log.statistics` prints network averages: route length, trip
  speed, duration, waitingTime, timeLoss, departDelay
  ([SUMO Output](https://sumo.dlr.de/docs/Simulation/Output.html)).
- **Denied-entry / latent demand must be accounted** or oversaturated comparisons
  lie: HCM Ch.24 says denied-entry delay is computed and assigned to upstream links
  (Aimsun exposes it as virtual-queue time)
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html));
  SimTraffic folds denied-entry delay into total delay
  ([FHWA TAT, §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm));
  Vissim analyses add latent delay to total delay over Vehicles Arrived + Active +
  Latent Demand ([Monaghan Co. traffic analysis](https://consult.monaghancoco.ie/en/system/files/materials/17/App.%20D%20-%20Traffic%20Analysis.pdf)).

## 6. Travel-time reliability indices

The FHWA travel-time-reliability measure set (definitions per the
[FHWA TTR report](https://ops.fhwa.dot.gov/publications/tt_reliability/ttr_report.htm),
quoted in [MDOT SPR-1716](https://www.michigan.gov/MDOT/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1716-Report.pdf)
and the [InTrans corridor guide](https://www.intrans.iastate.edu/wp-content/uploads/2018/03/segment-_and_corridor-based_travel_time_reliability_w_cvr.pdf)):

- **Buffer time** = 95th-percentile TT − mean TT; **buffer index** = buffer time /
  mean TT — "the extra time that travelers must add to their trip to ensure on-time
  arrival 95% of the time (or to be late on average once a month)"
  ([PORTAL/ResearchGate](https://www.researchgate.net/publication/228916118_Using_Travel_Time_Reliability_Measures_to_Improve_Regional_Transportation_Planning_and_Operations)).
- **Planning time index** = 95th-percentile TT / free-flow TT; **travel time
  index** = mean TT / free-flow TT ([Oregon Metro background report](https://www.oregonmetro.gov/sites/default/files/2020/06/10/Regional-Mobility-Policy-background-report-20200608.pdf)).
- **Failure/on-time measures** = % trips below 1.1× or 1.25× median TT
  ([FDOT reliability report](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/research/reports/fdot-bdv29-977-61-rpt.pdf?sfvrsn=fc553b23_5));
  **misery index** = (mean travel rate of the worst 20% of trips)/(overall mean
  travel rate) − 1, after Lomax et al. 1997
  ([reliability-measures survey](https://www.researchgate.net/publication/337054339_Analyzing_travel_time_distribution_based_on_different_travel_time_reliability_patterns_by_probe_vehicle_data)).
- Mechanism for us: reliability needs a *distribution* of travel-time samples per
  OD/route — across stochastic replications (seeds) and across demand days — which
  a deterministic single run cannot produce. Colorado DOT operationalizes PTI as
  its "operational LOS" for bottleneck decisions
  ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).

## 7. Detector emulation vs full-trajectory analytics

Two measurement channels exist side by side in every microsim, and both matter:

- **Virtual point detector (SUMO E1 induction loop)**: per aggregation interval
  emits nVehContrib, flow (veh/h), occupancy (% of time a vehicle was on the
  detector), arithmetic mean speed (**time mean speed**) and harmonic mean speed
  (**space mean speed**) — the two are different numbers and the docs say which is
  which ([SUMO E1 docs](https://sumo.dlr.de/docs/Simulation/Output/Induction_Loops_Detectors_(E1).html)).
- **Virtual area detector (SUMO E2 lane-area)**: tracks every vehicle on a lane
  segment; outputs mean/max jam length in vehicles and meters, halting durations,
  occupancy; halting thresholds timeThreshold 1 s, speedThreshold 5/3.6 m/s,
  jamThreshold 10 m; its meanSpeed is detector-length/mean-travel-time — "even if
  all vehicles drive with constant speed the result will differ from the
  measurements of an induction loop"
  ([SUMO E2 docs](https://sumo.dlr.de/docs/Simulation/Output/Lanearea_Detectors_(E2).html)).
- **Entry/exit detector (SUMO E3)**: tracks traffic in an area via entry and exit
  events at defined locations ([SUMO Output](https://sumo.dlr.de/docs/Simulation/Output.html)).
- SUMO ships scripts to auto-generate E1/E2/E3 detectors around every
  signal-controlled intersection ([SUMO Output](https://sumo.dlr.de/docs/Simulation/Output.html)).
- **Why emulate detectors at all when we have trajectories?** Because field
  calibration data *is* detector data: FHWA capacity calibration compares simulated
  vs field queue-discharge flow rates measured at a detector over ≥ 15 min
  ([FHWA Vol. III §5.3](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect5.htm)),
  and actuated signal control consumes detector states, not trajectories
  (E1s auto-created for actuated TLS, [SUMO E1 docs](https://sumo.dlr.de/docs/Simulation/Output/Induction_Loops_Detectors_(E1).html)).
  Trajectory analytics (§1–§3) are the decision metrics; detector emulation is the
  *validation and control* channel. Aimsun runs its HCM Ch.24 state machine "on
  the fly" as a third, hybrid shape
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).

## 8. Aggregation windows and interval semantics

- HCM analyses run on a **15-min analysis period**, with the peak-hour factor
  binding the peak 15-min flow to the hourly average
  ([FHWA TAT, §4.1.1](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
- Detector aggregation periods in practice: 900 s in SUMO's E1 example
  ([SUMO E1 docs](https://sumo.dlr.de/docs/Simulation/Output/Induction_Loops_Detectors_(E1).html));
  300 s queue-counter intervals in the Georgia Tech Vissim course
  ([module 6](https://gti.gatech.edu/sites/default/files/Module%206%20-%20Vissim%20Data%20and%20Performace%20Metrics%20submitted.pdf));
  SimTraffic's 2-min sampling of max queue
  ([FHWA TAT, §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm));
  MassDOT matches the sim recording interval to the field interval (30 s–1 min for
  roundabout back-of-queue) ([MassDOT guidance](https://www.mass.gov/doc/massdot-roundabout-vissim-microsimulation-guidance/download)).
- HCM Ch.24 interval-semantics rule: "all performance measures that accrue over
  time and space shall be assigned to the time interval and link in which they
  occur, no matter if the cause is at some distant point downstream"
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Mechanism for us: every metric event needs (interval id, network element id)
  dimensions; queue *maxima* need fine sampling (≤ 2 min) inside coarser reporting
  windows, because max-of-window ≫ mean-of-window.

## 9. Warmup and steady-state detection

- Simulations start empty; the warmup period "must be excluded from the reported
  statistics." FHWA's rule: warmup ends when **the number of vehicles present on
  the network ceases to increase** by a specified minimum (equilibrium detection on
  the occupancy time series)
  ([FHWA Vol. III, Appendix C](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sectapp_c.htm)).
- HCM Ch.24 user guidelines: "the spatial and temporal boundaries of the analysis
  domain must include a period that is free of congestion on all sides" and "the
  network must be properly warmed-up and stable before LoS measures are made"
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Typical magnitudes: 10-min warmup before a 60-min test period in a TTI
  microsimulation study ([TTI Super 2 report](https://static.tti.tamu.edu/tti.tamu.edu/documents/0-6135-1.pdf));
  ~20 min observed in a JICA Manila model ([JICA report](https://openjicareport.jica.go.jp/pdf/12374856_05.pdf)).
- If vehicle count never stabilizes, demand likely exceeds capacity — the warmup
  detector doubles as an oversaturation tripwire
  ([Aburto thesis citing FHWA 2004](https://repositorioacademico.upc.edu.pe/bitstream/10757/658795/3/Aburto_GJ.pdf)).

## 10. Replication, seeds, confidence intervals, and CRN

- FHWA Vol. III: "no single simulation run can be expected to reflect any specific
  field condition… results from individual runs can vary by 25 percent," higher
  near capacity; run multiple seeds and post-process mean/min/max
  ([FHWA Vol. III, §6.4.1](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect6.htm)).
- Run-count formula used by FDOT and others: **n = (s·t_(α/2) / (μ·ε))²** from an
  initial sample; "ten simulation runs with different random numbers are usually
  adequate" ([FDOT Traffic Analysis Handbook](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/planning/systems/systems-management/document-repository/traffic-analysis/traffic-analysis-handbook_10-08-2025.pdf?sfvrsn=e4bbbff8_1));
  Aimsun prints the same 95%-confidence form n = (s/E_T)²
  ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Agency floors: **WSDOT minimum 11 runs** — odd so a *median* run can be picked
  "to review the model or create demonstrative videos"; ODOT ~10 verified by
  calculation; VDOT uses a sample-size tool ([Michigan DOT SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).
  Vissim's default initial seed is 42, incremented by 1 per run
  ([Scottsdale supplement](https://www.scottsdaleaz.gov/docs/default-source/scottsdaleaz/transportation/reports-and-studies/2025-supplement-analysis-of-operational-impact-of-lilo-treatments.pdf?sfvrsn=1edc8f58_3)).
- **Common random numbers (CRN)**: reuse the same seed set across alternatives so
  differences are attributable to the design, not the draw — reduces the variance
  of the *difference* estimator; requires synchronized streams (Rathi 1992 on
  TRAF-NETSIM, [RePEc](https://ideas.repec.org/a/eee/transb/v26y1992i5p357-363.html);
  [WSC tutorial](https://www.informs-sim.org/wsc99papers/004.PDF); recent evidence
  pairing works when seed-level correlation is strong,
  [arXiv:2512.24145](https://arxiv.org/pdf/2512.24145)).
- SUMO's ecosystem is honest about the tooling gap: multi-run statistics are
  assembled by chaining `runSeeds.py` and `attributeStats.py`, with docs described
  as a gap by the devs themselves ([SUMO issue #10870](https://github.com/eclipse-sumo/sumo/issues/10870)).

## 11. LOS computation from simulation output

- HCM Ch.24 defines how to grade simulation results: queue delay → approach LOS,
  flow-weighted average → intersection LOS; density in PCU per lane → freeway LOS;
  Aimsun implements the threshold tables on the fly, including the F-when-Q/C>1
  override ([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Signalized thresholds (s/veh control delay): A ≤10, B ≤20, C ≤35, D ≤55, E ≤80,
  F >80 or v/c>1; unsignalized & roundabout: A ≤10, B ≤15, C ≤25, D ≤35, E ≤50,
  F >50 ([Apple Valley HCM6 table](https://applevalley.org/wp-content/uploads/2026/02/general-plan-amendment-2022‑007-zone-change-2022‑005-and-tentative-tract-map-20453-notice-of-intent-to-adopt-a-mitigated-negative-declaration-1.pdf),
  [Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Freeway density thresholds (pc/mi/ln): A ≤11, B ≤18, C ≤26, D ≤35, E ≤45, F >45;
  merge/diverge and weaving use A ≤10, B ≤20, C ≤28, D ≤35, E >35
  ([ScienceDirect table 5.7](https://www.sciencedirect.com/topics/engineering/geometric-flow),
  [Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Urban-street LOS is graded on speed as % of free-flow: A >85%, B 67–85, C 50–67,
  D 40–50, E 30–40, F ≤30 ([Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
- Edition drift: HCM 2010 introduced the Ch.24 simulation procedures; the 7th
  edition (2022, "A Guide for Multimodal Mobility Analysis") replaced two-lane
  highway PTSF with **follower density** among other updates
  ([McTrans on HCM7](https://mctrans.ce.ufl.edu/two-lane-highways-analysis-a-look-ahead-at-the-upcoming-release-of-the-hcm-from-a-practitioners-perspective/page/3/),
  [TRB HCQS committee](https://www.hcqstrb.org/hcm)). Any LOS feature must pin an
  edition.
- Agencies warn against naïve LOS-from-microsim: VDOT — "LOS shouldn't be used to
  support the results from microsimulation" because definitions differ from HCM;
  NYSDOT and Washington caution similarly
  ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).

## 12. Emissions and fuel (secondary layer)

- SUMO ships continuous reformulations of **HBEFA v2.1/v3.1/v4.2** (open data) and
  **PHEMlight(5)** (model open, full data commercial); default class
  `HBEFA3/PC_G_EU4`; outputs per-trip sums (CO, CO₂, HC, PMx, NOx, fuel in tripinfo),
  edge/lane aggregates, and per-step emission output; coasting vehicles emit zero
  ([SUMO Emissions](https://sumo.dlr.de/docs/Models/Emissions.html),
  [TripInfo emissions](https://sumo.dlr.de/docs/Simulation/Output/TripInfo.html)).
- Vissim node evaluation estimates emissions from TRANSYT 7-F consumption formulas
  plus Oak Ridge National Laboratory data for a typical North American fleet, with
  no per-vehicle-type differentiation — explicitly "used to compare the emissions
  of different scenarios" ([PTV Vissim 2022 help](https://cgi.ptvgroup.com/vision-help/VISSIM_2022_ENG/Content/11_Auswertungen/AuswertungKnotenauswertung.htm)).
- SIDRA reports fuel consumption, operating cost, and emission estimates as
  standard outputs ([SIDRA getting started](https://docs.sidrasolutions.com/intersection/intersection/guides/getting-started/)).
- FHWA's MOE taxonomy treats noise, fuel, and pollutant emissions as *derived*
  measures computable from volume, speed, delay, and stops
  ([FHWA TAT, §2.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm)).
  Mechanism for us: a speed/accel time series per vehicle (which we already have
  from trajectories) + an emission-class lookup table — an offline post-processor,
  not an engine concern.
