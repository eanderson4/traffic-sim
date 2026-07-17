# Standards & Patterns: Congestion Metrics

> Source: academic research + pattern identification | Researched: 2026-07-16

## Formalisms

### Highway Capacity Manual LOS (the grading standard)
- LOS is "a qualitative measure used to describe operational conditions," six
  letter grades A (best) to F (worst); each facility type has one designated
  *service measure* — control delay at intersections, density on freeways, speed
  (% FFS) on urban streets ([NCHRP 2025 LOS synthesis](https://nap.nationalacademies.org/read/29143/chapter/4)).
- Critically, LOS is not itself a measurement: "LOS is not strictly a performance
  measure, but a method of reporting one or more selected numerical performance
  measures in a system of easily understandable letter grades"
  ([FHWA TAT §2.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm)).
- Threshold tables (HCM 6th, via
  [Apple Valley](https://applevalley.org/wp-content/uploads/2026/02/general-plan-amendment-2022‑007-zone-change-2022‑005-and-tentative-tract-map-20453-notice-of-intent-to-adopt-a-mitigated-negative-declaration-1.pdf)
  and [Aimsun's HCM implementation](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)):
  - Signalized (s/veh): A ≤10, B ≤20, C ≤35, D ≤55, E ≤80, F >80 **or v/c>1**
  - Unsignalized & roundabout (s/veh): A ≤10, B ≤15, C ≤25, D ≤35, E ≤50, F >50
  - Basic freeway (pc/mi/ln): A ≤11, B ≤18, C ≤26, D ≤35, E ≤45, F >45
  - Merge/diverge & weave (pc/mi/ln): A ≤10, B ≤20, C ≤28, D ≤35, E >35
  - Urban street (% FFS): A >85, B 67–85, C 50–67, D 40–50, E 30–40, F ≤30
- Edition lineage that matters: HCM 2010 added Chapter 24 (simulation trajectory
  procedures); HCM 6th (2016) is today's default in US practice; HCM 7th (2022,
  "A Guide for Multimodal Mobility Analysis," first electronic edition) replaced
  two-lane-highway PTSF with follower density
  ([Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html),
  [TRB HCQS](https://www.hcqstrb.org/hcm),
  [McTrans](https://mctrans.ce.ufl.edu/two-lane-highways-analysis-a-look-ahead-at-the-upcoming-release-of-the-hcm-from-a-practitioners-perspective/page/3/)).
- Design-LOS policy is set per agency, typically D urban / C rural
  ([NCHRP 2025, Table 1](https://nap.nationalacademies.org/read/29143/chapter/4)).

### HCM Chapter 24 — the sim-measurement formalism
The one standard that directly governs us: HCM 2010 Ch.24 defines vehicle states
(stopped, queuing, following), per-time-step delay accounting, back-of-queue,
interval/link assignment of measures, warmup and domain-boundary requirements,
and LOS grading of simulation outputs — all computable on the fly
([Aimsun Next HCM Algorithms](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).

### Edie's generalized q/k/u (the trajectory-measurement formalism)
q(A) = Σdᵢ/|A|, k(A) = Σtᵢ/|A|, v = q/k over a time–space region A
([Edie 1963, cited via MaxAp reference](https://www.maxapress.com/app/reference/65a785fafa6c583a2fcd9868);
formulas in [Seo 2015](https://toruseo.jp/paper/Seo2015probe.pdf)). Exact q = k·v
inside the region; the standard method for trajectories
([arXiv:2512.21425](https://arxiv.org/html/2512.21425v3)). Connects micro outputs
to the fundamental diagram of [[domain-macroscopic-flow-models]].

### FHWA travel-time reliability measures
Buffer index = (TT₉₅ − TT_mean)/TT_mean; planning time index = TT₉₅/TT_ff;
travel time index = TT_mean/TT_ff; on-time/failure = % trips under 1.1×/1.25×
median ([FHWA TTR via MDOT SPR-1716](https://www.michigan.gov/MDOT/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1716-Report.pdf),
[Oregon Metro](https://www.oregonmetro.gov/sites/default/files/2020/06/10/Regional-Mobility-Policy-background-report-20200608.pdf),
[FDOT BDV29-977-61](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/research/reports/fdot-bdv29-977-61-rpt.pdf?sfvrsn=fc553b23_5)).

### Control delay formalism (HCM analytical)
d = d1 + d2 + d3 — Webster uniform delay + overflow delay + initial-queue delay;
saturation flow s, capacity c = s·g/C; field definition = deceleration + queue
move-up + stopped + acceleration delay
([FHWA TAT §4.1.1](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm),
[Corona appendix](https://cdn.prod.website-files.com/65799af8ef225180fdf1ba2e/672f672f5acb5e5a27ef3a9c_xxApp%20M1%20re.pdf)).

### GEH statistic (calibration acceptance)
GEH = √(2(E−V)²/(E+V)) comparing model volume E vs field count V; acceptance
targets: GEH < 5 for > 85% of links, GEH < 4 for summed flows; hourly flows within
15% (700–2700 veh/h), travel times within 15% or 1 min (Wisconsin DOT criteria
table) ([FHWA Vol. III §5.6](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect5.htm)).

### Run-count and confidence-interval formalism
n = (s·t_(α/2)/(μ·ε))² from a pilot sample; 10 runs "usually adequate"; WSDOT
floor of 11 ([FDOT handbook](https://fdotwww.blob.core.windows.net/sitefinity/docs/default-source/planning/systems/systems-management/document-repository/traffic-analysis/traffic-analysis-handbook_10-08-2025.pdf?sfvrsn=e4bbbff8_1),
[Michigan SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).

## Design Patterns Identified

### Trajectory-first metric kernel (one implementation, derived views)
Compute per-vehicle trip records and Edie x–t cells from the trajectory stream;
derive delay, queue, stops, density, throughput, reliability, LOS as views. HCM
Ch.24's premise — trajectory analysis is the only measurement consistent across
sim tools and field techniques
([Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)) —
and the architecture our `analysis/ngsim` prototype already assumes ("one Edie
implementation, two consumers").

### Detector emulation layer (virtual E1/E2/E3)
Scenario-declared virtual sensors with documented thresholds (halting:
1 s / 5 km/h / 10 m jam gap in SUMO E2) and aggregation periods, mirroring the
physical sensors that produce calibration data
([SUMO E2](https://sumo.dlr.de/docs/Simulation/Output/Lanearea_Detectors_(E2).html),
[FHWA Vol. III §5.3](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect5.htm)).
Sim validation compares like-with-like: virtual detector vs real detector.

### Queue state machine with hysteresis
Separate entry and exit thresholds (Vissim 5→10 km/h; SimTraffic 10→15 ft/s;
Aimsun 1/3→2/3 of desired speed) so queue membership doesn't flicker during
move-ups; report avg, max, and 95th-percentile back of queue
([Mississauga](https://www.mississauga.ca/wp-content/uploads/2023/11/Rangeview-Urban-Transportation-Considerations-Sept-2023.pdf),
[FHWA TAT §4.1.4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).

### Occupancy-equilibrium warmup
End warmup when the network vehicle count stops increasing; require
congestion-free domain boundaries; the same detector doubles as an
oversaturation tripwire
([FHWA Vol. III App. C](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sectapp_c.htm),
[Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).

### Paired-seed alternatives comparison (CRN)
Run baseline and variants on identical seed sets; differences become
paired-sample estimates with far lower variance ([Rathi 1992](https://ideas.repec.org/a/eee/transb/v26y1992i5p357-363.html),
[WSC tutorial](https://www.informs-sim.org/wsc99papers/004.PDF),
[arXiv:2512.24145](https://arxiv.org/pdf/2512.24145)). Requires synchronized
stream-per-concern RNG — which [[arch-time-model]] already mandates.

### Median-run showcase
With an odd number of replications, the median run is pickable for review and
demonstration videos (WSDOT practice) — directly the run a before/after game
reveal should animate ([Michigan SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).

### LOS as a derived presentation skin
Keep continuous metrics authoritative; map to letters at report time with a
pinned HCM edition, exactly as agencies convert microsim density to PCU before
grading (FDOT practice) ([NCHRP 2025](https://nap.nationalacademies.org/read/29143/chapter/4)).
For public communication the letter grade and the buffer-index framing ("plan
for the 95th percentile; late once a month") are the two proven translations
([PORTAL](https://www.researchgate.net/publication/228916118_Using_Travel_Time_Reliability_Measures_to_Improve_Regional_Transportation_Planning_and_Operations)).

## Anti-Patterns

### Single-run conclusions
Point estimates from one seed: near-capacity run-to-run standard deviations hit
25%+ ([FHWA Vol. III §6.4.1](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect6.htm)).

### Cross-tool LOS comparison
Quoting a microsim's "LOS" against HCM LOS: VDOT — "LOS shouldn't be used to
support the results from microsimulation"; NYSDOT warns the definitions differ;
KYTC — LOS is "not the most applicable or accurate way to represent the results"
([NCHRP 2025](https://nap.nationalacademies.org/read/29143/chapter/4)).

### Definition drift in "delay" and "queue"
The FHWA toolbox documented tool-by-tool variations in reported MOEs on an
identical test bed — SimTraffic's 95th-percentile queue can exceed its observed
max; its vehicle-count formula yields *zero vehicles* on a fully jammed link;
Synchro's analytical queues can undershoot Vissim's shockwave-inclusive
ones ([FHWA TAT §4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm),
[Mississauga](https://www.mississauga.ca/wp-content/uploads/2023/11/Rangeview-Urban-Transportation-Considerations-Sept-2023.pdf)).
Every metric we emit needs a normative definition in the message contract.

### Survivorship bias in trip statistics
Averaging only completed trips drops exactly the vehicles suffering the worst
congestion: SUMO includes only arrived vehicles in its statistic output by
default ([SUMO Output](https://sumo.dlr.de/docs/Simulation/Output.html)); Vissim
analyses must add latent demand and its delay explicitly
([Monaghan](https://consult.monaghancoco.ie/en/system/files/materials/17/App.%20D%20-%20Traffic%20Analysis.pdf));
HCM Ch.24 requires denied-entry accounting
([Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).

### Reading capacity off demand
Throughput at an undersaturated point is demand, not capacity; capacity must be
measured under sustained upstream queue (≥ 15 min discharge)
([FHWA Vol. III §5.3.2](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect5.htm)).

### Mean-only reporting
Averages erase reliability: agencies moved to 95th-percentile queues, buffer
indices, and on-time measures precisely because means hide the bad tail
([FHWA TAT §2.1](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm),
[MDOT SPR-1716](https://www.michigan.gov/MDOT/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1716-Report.pdf)).
