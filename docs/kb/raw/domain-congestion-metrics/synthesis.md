# Synthesis: Congestion Metrics

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds a future ADR (observability metric set & message contracts — not yet
> drafted; VISION.md's "Observability" core concept is its mandate). This synthesis
> recommends; the ADR decides.

## Summary

The question was: what must our observability subsystem compute — per lane, per
intersection, per scenario — to credibly rank infrastructure alternatives, and
what does the math-vs-vibes game need for compelling before/after reveals? The
field answer is unusually well documented. HCM Chapter 24 (2010) settled *how* to
measure simulation output (per-vehicle trajectory state machines, computed on the
fly), FHWA's Traffic Analysis Toolbox catalogued *what* agencies report (nine
focus MOEs: LOS, v/c, travel time, speed, delay, queue, stops, density,
travel-time variance), and NCHRP's 2025 synthesis shows practice drifting from
letter grades toward continuous delay/travel-time/reliability measures. Every
definition is a state machine with thresholds, every threshold differs per tool,
and the differences are large enough that FHWA published a whole volume on them.
Our opening: nobody ships the metric pipeline as a documented, streaming,
trajectory-first service — it is always post-hoc files with tool-local
definitions.

## Source Files

- [Mechanics: measurement mechanisms and thresholds](./implementation.md)
- [Prior art survey: SUMO/MATSim/Vissim/Aimsun/Synchro/HCS/SIDRA + DOT practice](./competitors.md)
- [Standards, formalisms, patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions

### 1. Trajectory-first metric kernel; everything else is a derived view
**Choice:** The observability service consumes the trajectory/state stream and
computes two primitive artifacts: (a) per-vehicle trip records (entry/exit times,
distance, time-loss vs desired speed, stop episodes) and (b) Edie q/k/u on x–t
cells — the exact math already proven in `analysis/ngsim` (measured −18.1 km/h
wave on NGSIM I-80). Delay, queue, density, throughput, reliability, and LOS are
derived views over these.
**Why:** HCM Ch.24's core finding — trajectory analysis is the only measurement
approach consistent with HCM definitions, field techniques, and across tools
([Aimsun](https://docs.aimsun.com/next/22.0.4/UsersManual/HCMAlgorithms.html)).
One implementation serving both real data (NGSIM validation, [[domain-trajectory-datasets]])
and sim output is already our working hypothesis and removes a whole class of
sim-vs-field mismatch.
**Trade-off:** Per-vehicle event volume on the bus is larger than pre-aggregated
detector data; the 100 ms tick × fleet size sets the stream budget (bounded by
snapshot rates from [[arch-time-model]]).
**Field context:** Aimsun runs the same state machine on the fly
([competitors](./competitors.md)); SUMO/MATSim emit the primitives as files.

### 2. Detector emulation is a separate, scenario-declared layer
**Choice:** Virtual E1/E2/E3-style detectors (point loops, lane-area, entry/exit)
are declared in the scenario with explicit thresholds and aggregation periods —
used for field calibration and actuated control, not as the primary metric
channel.
**Why:** Calibration data from the field *is* detector data (FHWA capacity
calibration compares discharge rates at detectors, [Vol. III §5.3](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect5.htm));
actuated signal controllers consume detector states (see [[domain-signal-control]]).
Detector outputs are deliberately *not* interchangeable with trajectory measures —
SUMO's own docs warn E2 mean speed ≠ E1 mean speed
([E2 docs](https://sumo.dlr.de/docs/Simulation/Output/Lanearea_Detectors_(E2).html)).
**Trade-off:** Two measurement channels to maintain and keep documented; worth it
because each mirrors a real-world counterpart.
**Field context:** SUMO's E1/E2/E3 + auto-generation scripts are the model
([implementation §7](./implementation.md)).

### 3. The canonical metric set (the deliverable)
**Choice:**
- *Per lane/link (per interval):* Edie q/k/u; density (PCU-converted); mean speed
  (space-mean); occupancy; queued-vehicle fraction; back of queue (avg/max/95th);
  stop count and stop rate; flow/throughput count.
- *Per intersection approach & intersection:* control delay (actual − free-flow
  traversal, decomposed into stopped vs queue move-up where computable); back of
  queue avg/max/95th; stops; v/c or degree of saturation with capacity measured
  under sustained queue discharge; queue storage ratio; throughput by movement.
- *Per scenario/run:* completed trips; VMT; VHT; total and mean delay; mean and
  distribution of trip travel times per OD (→ buffer index, planning time index,
  travel time index); denied-entry (latent) demand and its delay; warmup and
  equilibrium flags.
- *Across replications:* every scenario metric as mean + 95% CI + min/max over
  paired seeds.
**Why:** This is the DOT report card (Indiana/Ohio/FDOT MOE lists, [NCHRP 2025](https://nap.nationalacademies.org/read/29143/chapter/4))
plus FHWA's nine focus MOEs ([FHWA TAT §2.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm))
plus the reliability layer agencies adopted for exactly our "is it better?"
question.
**Trade-off:** 95th-percentile queue and reliability indices need distributions —
we must store samples, not just aggregates (storage cost, trivial at our scale).
**Field context:** [competitors](./competitors.md) practice-layer section.

### 4. LOS is a presentation skin, never the ranking metric
**Choice:** Rank alternatives on continuous metrics (delay, travel time,
reliability, throughput); offer HCM letter grades as a derived view with a pinned
edition (HCM 6th default, thresholds in config) for communication.
**Why:** "LOS is not strictly a performance measure, but a method of reporting…
in a system of easily understandable letter grades" ([FHWA TAT §2.3](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect2.htm));
agencies explicitly distrust LOS straight from microsim (VDOT, NYSDOT, KYTC —
[NCHRP 2025](https://nap.nationalacademies.org/read/29143/chapter/4)); practice
is drifting to continuous measures. For the game, letters are the punchy reveal —
but they must grade *our* continuous numbers, or the reveal is theater.
**Trade-off:** We inherit the obligation to document the edition and the
conversion (PCU density, queue delay vs control delay) wherever we print a letter.
**Field context:** Aimsun grades on the fly with the full threshold tables
([implementation §11](./implementation.md)); FDOT converts sim density to PCU
before grading.

### 5. Experiment protocol: warmup, paired seeds, confidence intervals, median run
**Choice:** Built-in run protocol: (a) warmup auto-detected by network-occupancy
equilibrium (FHWA App. C rule) with congestion-free boundary check; (b) N
replications per scenario, N from n = (s·t/(μ·ε))² after a pilot, floor of ~10;
(c) common random numbers — identical seed sets across alternatives, enabled by
the stream-per-concern RNG [[arch-time-model]] already mandates; (d) report
mean + CI + distribution, never a single run; (e) the median run is the
designated showcase/replay run.
**Why:** Single runs vary 25%+ near capacity ([FHWA Vol. III §6.4.1](https://ops.fhwa.dot.gov/trafficanalysistools/tat_vol3/sect6.htm));
CRN shrinks the variance of the *difference* — the quantity the game actually
reveals ([Rathi 1992](https://ideas.repec.org/a/eee/transb/v26y1992i5p357-363.html));
WSDOT's median-run rule is literally a recipe for "pick the run to show"
([Michigan SPR-1689](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).
**Trade-off:** CRN requires disciplined RNG stream assignment as the engine grows;
median-run showcase must disclose it is the median of N.
**Field context:** [standards-and-patterns](./standards-and-patterns.md)
run-count formalism and patterns.

### 6. Aggregation windows: 15-min reporting, fine sampling for maxima
**Choice:** Default reporting interval 15 min (HCM analysis period); detector
periods configurable (300–900 s typical); queue maxima sampled ≤ 2 min inside
windows; every metric event stamped (interval, element) with HCM Ch.24's
assign-where-it-occurs rule.
**Why:** Matches the field's reporting grammar ([implementation §8](./implementation.md));
SimTraffic's 2-min max-queue sampling and MassDOT's interval-matching guidance
show max-of-window ≫ mean-of-window for queues.
**Trade-off:** Two time resolutions in the metrics stream; acceptable.
**Field context:** SUMO/Vissim/SimTraffic interval practice, all cited in
[implementation §8](./implementation.md).

### 7. Emissions/fuel: deferred, offline, HBEFA-class
**Choice:** Not in the engine. An offline post-processor maps per-vehicle
speed/accel time series (already on the trajectory stream) through an
HBEFA-class emission table, following SUMO's open HBEFA reformulations.
**Why:** Emissions are derived measures of volume/speed/delay/stops per FHWA's
taxonomy; SUMO proves the speed/accel → emission-class mechanism works
post-hoc ([SUMO Emissions](https://sumo.dlr.de/docs/Models/Emissions.html)).
**Trade-off:** No live emissions heatmap in v1; fine — VISION lists them nowhere
as core.
**Field context:** [implementation §12](./implementation.md).

## Compare/Contrast: Metric Pipelines

| Dimension | SUMO | MATSim | Vissim | Aimsun | Synchro/SimTraffic | SIDRA | us (proposed) |
|---|---|---|---|---|---|---|---|
| Core metric form | files (tripinfo, detectors) | events → handlers | configured counters/sections | on-the-fly Ch.24 states | analytical + micro files | analytical report | **streaming trajectory kernel** |
| Delay definition | timeLoss vs ideal | link travel time | actual − ideal per vehicle | segment/queue/stopped split | HCM d1+d2+d3 / vs max speed | control + geometric + stop-line | **actual − free-flow, decomposed** |
| Queue definition | last standing vehicle | none (queue model) | 5→10 km/h hysteresis, 20 m | 1/3→2/3 desired, 20 ft gap | closed-form percentile / 10→15 ft/s, 19.5 ft | percentile back of queue | **documented state machine, configurable thresholds** |
| LOS | none | none | post-processing | full HCM tables on the fly | HCM + ICU / none | LOS from delay | **derived view, pinned edition** |
| Reliability (buffer/PTI) | none built-in | none | across-run percentiles | no | no / no | no | **first-class per-OD distributions** |
| Capacity/v-c | manual | flow caps input | manual discharge meas. | manual | computed (analytical) | computed (DoS) | **measured under sustained queue** |
| Multi-seed stats | user scripts (gap) | N/A (day-to-day ≠ seed) | agency protocol 5–11 runs | n-formula built in | multi-run reporting | N/A | **protocol built in: paired seeds, CI, median run** |

## The Genuine Gap

Two real gaps this time. (1) **No canonical, documented metric schema exists for
open-source microsim** — SUMO's outputs are de facto but carry tool-local
definitions, and FHWA needed a whole report to catalog how the same MOE names mean
different numbers per tool ([FHWA TAT §4](https://ops.fhwa.dot.gov/publications/fhwahop08054/sect4.htm)).
An open "metric contract" (names + state machines + thresholds + interval
semantics) published as versioned message schemas would be a genuine contribution.
(2) **Metrics-as-a-stream is undocumented**: every surveyed tool computes into
files or in-process handlers; no one publishes HCM-Ch.24-grade measures as
first-class messages on a broker that visualization, controllers, and replay all
subscribe to — same "NATS-backed sim" gap [[arch-time-model]] found, now on the
observability side.

## Open Questions

- Metric stream budget: fleet size × 100 ms tick × per-vehicle state size vs NATS
  throughput → benchmark during engine bring-up (with [[arch-nats-backbone]]).
- 95th-percentile queue estimator: empirical distribution (needs stored samples)
  vs mean+1.65σ (SimTraffic) vs percentile-volume formulas (Synchro) — pick after
  we see our own queue time series.
- CRN stream assignment granularity: which concerns share streams across
  alternatives (spawn times yes; lane-change draws?) → experiment once the RNG
  layout from [[arch-time-model]] exists.
- Which reliability baseline for the game: free-flow travel time (PTI) or mean
  (buffer index) — likely scenario-configurable with PTI default for reveals.
- Do we need PCU conversion in v1 (truck share in scenarios?) — HCM grading
  requires it; continuous metrics don't.

## Connections to Other Topics

- **Builds on:** [[domain-trajectory-datasets]] (Edie definitions, NGSIM
  validation targets; `analysis/ngsim` is the prototype of decision 1),
  [[domain-macroscopic-flow-models]] (fundamental diagram gives the q/k/u fields
  their meaning; MFD as a scenario-level view), [[arch-time-model]] (tick clock
  stamps metric intervals; seeded stream-per-concern RNG enables paired-seed CRN;
  replay = metric re-derivation check).
- **Constrains:** [[arch-nats-backbone]] (metric subjects + trajectory event
  budget; metric definitions are versioned message contracts), [[concept-scenario-format]]
  (metric definitions, detector declarations, run protocol: seeds, warmup,
  intervals live in the scenario), [[integration-maplibre-realtime]] (heatmap
  fields = Edie cells; before/after reveal = median-run replay + continuous-metric
  scoreboard + LOS skin).
- **Informs:** [[domain-signal-control]] (control delay is the objective; virtual
  detectors feed actuated control), [[domain-simulator-landscape]] (our metric
  pipeline is the differentiator vs SUMO/Vissim file outputs),
  [[concept-vehicle-controller-interface]] (controller KPIs read from the same
  metric stream).
