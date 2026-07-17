# Congestion Metrics

> Observability design: a streaming, trajectory-first metric kernel computing Edie q/k/u and per-vehicle trip records, from which delay, queues, reliability, and LOS derive — with a built-in multi-seed experiment protocol.

## Overview

Congestion metrics are the measures of effectiveness (MOEs) the simulation computes — per lane, per intersection, per scenario — to answer the question [VISION.md](../../../VISION.md) is built around: which infrastructure alternative is actually better? The topic spans three layers: definitions (delay, queue, capacity, LOS), measurement mechanics (vehicle state machines, virtual detectors, aggregation windows), and the experiment protocol (warmup, seeds, confidence intervals) that makes a comparison statistically credible rather than anecdotal.

This matters doubly to traffic-sim: the math-vs-vibes game needs compelling before/after reveals, and the civic-advocacy use case needs numbers a traffic agency would recognize. The field is unusually well documented. HCM 2010 Chapter 24 settled *how* to measure simulation output (per-vehicle trajectory state machines, computed on the fly); FHWA's Traffic Analysis Toolbox catalogued *what* agencies report (nine focus MOEs: LOS, v/c, travel time, speed, delay, queue, stops, density, travel-time variance); NCHRP's 2025 synthesis shows practice drifting from letter grades toward continuous delay/travel-time/reliability measures. Every tool implements the same metric names with different state machines and thresholds — FHWA published an entire volume on the discrepancies.

The research conclusion: build a **trajectory-first streaming metric kernel** that computes two primitive artifacts — per-vehicle trip records and Edie q/k/u on time–space cells — and derive everything else as views; keep LOS as a pinned-edition presentation skin, never the ranking metric; and ship the multi-seed experiment protocol (paired seeds, CIs, median-run showcase) as a built-in runner. The identified market gap: nobody ships the metric pipeline as a documented, versioned, streaming contract — it is always post-hoc files with tool-local definitions. These positions are **recommendations pending a future observability ADR**; their substrate (NATS streaming, tick clock, seeded RNG, versioned message contracts) is already ratified by ADR-0002/0005/0006/0007.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Trajectory metric kernel | `raw/domain-congestion-metrics/synthesis.md` §1 | One streaming implementation; all metrics are derived views over two primitives |
| Edie q/k/u x–t fields | `raw/domain-congestion-metrics/implementation.md` §1 | Flow/density/speed as region integrals; exact q = k·v; powers heatmaps |
| Time-loss & delay accounting | `raw/domain-congestion-metrics/implementation.md` §2 | Per-vehicle actual-vs-ideal primitive; control/stopped/queue delay are filters over it |
| Queue state machine | `raw/domain-congestion-metrics/implementation.md` §3 | Hysteresis entry/exit thresholds define queue membership; back-of-queue avg/max/95th |
| Capacity & v/c measurement | `raw/domain-congestion-metrics/implementation.md` §4 | Discharge rate measured under ≥15 min sustained queue, never read off demand |
| Reliability indices | `raw/domain-congestion-metrics/implementation.md` §6 | Buffer index, PTI, TTI from per-OD travel-time distributions |
| Virtual detector layer (E1/E2/E3) | `raw/domain-congestion-metrics/implementation.md` §7 | Scenario-declared sensors for field calibration and actuated signal control |
| Experiment protocol | `raw/domain-congestion-metrics/implementation.md` §9–10 | Warmup detection, paired seeds (CRN), confidence intervals, median-run showcase |
| LOS grading skin | `raw/domain-congestion-metrics/implementation.md` §11 | Pinned-edition letter grades derived from continuous metrics, thresholds in config |
| Emissions post-processor | `raw/domain-congestion-metrics/implementation.md` §12 | Deferred: offline HBEFA-class lookup over the speed/accel stream; not in the engine |

## How It Works

**Status.** No observability ADR exists yet — the synthesis feeds one. But the substrate is decided: metrics stream over NATS as first-class messages ([ADR-0002](../../decisions/ADR-0002-nats-backbone.md)); metric definitions land as versioned AsyncAPI 3.0 schemas on the three-plane taxonomy `{ns}.{run}.{plane}.>` ([ADR-0006](../../decisions/ADR-0006-nats-message-contract.md)); the tick count stamps every metric interval and replay (keyframes + intent log + rolling CRC) makes metric re-derivation a validity check ([ADR-0005](../../decisions/ADR-0005-time-model.md)); per-vehicle seeded RNG is the CRN substrate ([ADR-0007](../../decisions/ADR-0007-vehicle-model.md)).

### 1. Trajectory-first kernel

The kernel consumes the vehicle state stream and emits two primitives:

- **Per-vehicle trip records** — entry/exit times, distance, time-loss vs desired speed, stop episodes. Time-loss (SUMO's `timeLoss` = time lost vs ideal speed including the individual speedFactor) is the single actual-vs-ideal primitive; control delay, stopped delay, and queue delay are all filters over it. Aimsun's Ch.24 implementation shows the decomposition: per-step delay = Δt − distance/(desired speed), accumulated as segment / queue / stopped flavors.
- **Edie q/k/u on x–t cells** — q(A) = Σdᵢ/|A|, k(A) = Σtᵢ/|A|, v = q/k over a time–space region A: exact within the region (q = k·v holds identically) and robust to position noise. Already proven in `analysis/ngsim` (25 ft × 3 s cells, 1.52 M sample pairs, measured −18.1 km/h shockwave on NGSIM I-80).

Delay, queue, density, throughput, reliability, and LOS are derived views over these two artifacts. HCM Ch.24 is the authority for this shape: trajectory analysis is the only measurement approach consistent with HCM definitions, field techniques, and across tools. One implementation then serves both real trajectory data (validation) and sim output — removing a whole class of sim-vs-field mismatch. Aimsun proves the full Ch.24 state machine can run on the fly inside a sim loop; SUMO and MATSim emit the primitives as post-hoc files.

### 2. Canonical metric set (the deliverable)

- *Per lane/link, per interval:* Edie q/k/u; density (PCU-converted); space-mean speed; occupancy; queued-vehicle fraction; back of queue (avg/max/95th); stop count and rate; throughput count.
- *Per intersection approach:* control delay (actual − free-flow traversal, decomposed into stopped vs queue move-up where computable; the analytical HCM form is d = d1 uniform + d2 overflow + d3 initial queue); back of queue avg/max/95th; stops; v/c with capacity **measured under sustained queue discharge**; queue storage ratio; throughput by movement.
- *Per scenario/run:* completed trips; VMT; VHT; total and mean delay; per-OD travel-time distributions; **denied-entry (latent) demand and its delay**; warmup and equilibrium flags.
- *Reliability, per OD:* buffer index = (TT₉₅ − TT_mean)/TT_mean ("plan for the 95th percentile; late once a month"); planning time index = TT₉₅/TT_free-flow; travel time index = TT_mean/TT_free-flow; on-time = % trips under 1.1×/1.25× median. Colorado DOT operationalizes PTI as its "operational LOS" for bottleneck decisions.
- *Across replications:* every scenario metric as mean + 95% CI + min/max over paired seeds. Reliability indices need distributions — store samples, not just aggregates (trivial cost at our scale).

### 3. Queues are state machines with hysteresis — document yours

"Queue length" has no single definition; the numbers differ because the machines differ:

- Vissim: queue begins below 5 km/h, ends above 10 km/h (hysteresis so move-ups don't flicker), 20 m max headway to the next queued vehicle.
- Aimsun (HCM Ch.24): enter at gap ≤ 20 ft AND speed ≤ leader's AND speed ≤ 1/3 of desired; exit at ≥ 2/3 of desired; stopped < 5 mi/h.
- SimTraffic: queued below 10 ft/s (released at 15 ft/s), 19.5 ft spacing, no single-vehicle queues except at stop bars, no mid-link queue origin.
- HCM field protocol: fully stopped vehicles (5 mph threshold) counted per lane at green onset per cycle; percentiles 50/85/90/95.

Ours: a documented state machine with configurable thresholds, reporting avg/max/95th-percentile back of queue, with maxima sampled ≤ 2 min inside windows (max-of-window ≫ mean-of-window — MassDOT insists on max back of queue within the recording interval, matched to the field interval, never the average).

### 4. Capacity is a measured output, not an input

Microsims do not output "capacity" — the analyst creates a sustained upstream queue and measures discharge over ≥ 15 min. v/c follows as demand over that measured rate; v/c > 1 at a signal forces LOS F regardless of delay. Ohio DOT adds queue storage ratio (95th-percentile back of queue / available storage, target < 1) and demand/capacity < 0.93; SIDRA reports the same ratio as degree of saturation with practical saturation at 0.9.

### 5. LOS is a presentation skin

Rank alternatives on continuous metrics (delay, travel time, reliability, throughput); grade letters only at report time, with a pinned edition (HCM 6th default, thresholds in config). HCM 6th thresholds:

- Signalized (s/veh control delay): A ≤10, B ≤20, C ≤35, D ≤55, E ≤80, F >80 **or v/c>1**
- Unsignalized & roundabout (s/veh): A ≤10, B ≤15, C ≤25, D ≤35, E ≤50, F >50
- Basic freeway (pc/mi/ln): A ≤11, B ≤18, C ≤26, D ≤35, E ≤45, F >45
- Urban street (% free-flow speed): A >85, B 67–85, C 50–67, D 40–50, E 30–40, F ≤30

Agencies explicitly distrust microsim LOS (VDOT, NYSDOT, KYTC), so wherever we print a letter we document the edition and the conversion (PCU density, queue vs control delay). For the game, letters grade *our* continuous numbers — otherwise the reveal is theater.

### 6. Experiment protocol, built in

1. **Warmup** auto-detected by network-occupancy equilibrium (FHWA App. C rule: the count of vehicles on the network stops increasing) with a congestion-free boundary check; a count that never stabilizes means demand exceeds capacity — the detector doubles as an oversaturation tripwire.
2. **N replications** per scenario, N from n = (s·t/(μ·ε))² after a pilot, floor ~10; WSDOT's floor is 11 — odd, so a median run can be picked.
3. **Common random numbers (CRN):** identical seed sets across alternatives. CRN shrinks the variance of the *difference* — the quantity the game actually reveals; enabled by the seeded RNG layout from ADR-0007.
4. **Report mean + CI + distribution**, never a single run: individual runs vary 25%+ near capacity.
5. **Median run = showcase run** for review and demo videos (WSDOT practice) — exactly the run a game reveal should animate, disclosed as "median of N".

### 7. Aggregation windows

Default reporting interval 15 min (HCM analysis period); detector aggregation periods configurable (300–900 s typical); every metric event stamped (interval, element) under Ch.24's assign-where-it-occurs rule — delay accrues to the link and interval where it happens, even if the cause is spillback from downstream.

### 8. Detector emulation is a separate, scenario-declared layer

Virtual E1/E2/E3-style detectors (point loops, lane-area, entry/exit) with explicit thresholds (SUMO E2 halting: 1 s / 5 km/h / 10 m jam gap) and aggregation periods — used for field calibration (calibration data *is* detector data; acceptance bar: GEH < 5 for > 85% of links, hourly flows within 15%) and for actuated signal control. Not the primary metric channel: detector outputs are deliberately not interchangeable with trajectory measures — SUMO's own docs warn E2 mean speed ≠ E1 mean speed (space-mean vs time-mean).

### 9. Emissions/fuel: deferred, offline, HBEFA-class

An offline post-processor maps per-vehicle speed/accel time series (already on the trajectory stream) through an HBEFA-class emission table, following SUMO's open reformulations. No live emissions heatmap in v1 — VISION lists emissions nowhere as core.

### Prior art read-out

- **SUMO** — output taxonomy worth stealing nearly wholesale (trip records + detector emulation + network totals), but metrics are post-hoc files; multi-seed statistics are user-side scripting the devs themselves call a documentation gap.
- **MATSim** — the events file is the only real output and analysis is interchangeable consumers: validates our NATS events-as-interface design; its queue model caps metric granularity below what lane-level dynamics allow.
- **Vissim** — the DOT-practice reference; matching its vocabulary (travel-time sections, queue counters with hysteresis, latent demand counters) is table stakes for credibility. Seed practice: default 42, +1 per run, agencies require 5–11 runs.
- **Aimsun** — closest existing model: the full HCM Ch.24 state machine streaming inside the sim loop, including LOS grading and inline replication-count estimation.
- **HCS / SIDRA** — acceptance-test oracles: reproduce HCS-grade LOS on undersaturated cases and SIDRA's approach delay/DoS on isolated roundabouts before anyone trusts our oversaturated network numbers (spillback, waves, reliability — what analytical tools can't show).
- **CORSIM / Dynasmart-P** — the failure modes to avoid: "can't answer v/c or LOS from the micro model," undocumented queue definitions.

### The genuine gap

(1) No canonical, documented metric schema exists for open-source microsim — SUMO's outputs are de facto but carry tool-local definitions. An open metric contract (names + state machines + thresholds + interval semantics) published as versioned message schemas would be a genuine contribution. (2) Metrics-as-a-stream is undocumented: every surveyed tool computes into files or in-process handlers; nobody publishes Ch.24-grade measures as first-class broker messages that visualization, controllers, and replay all subscribe to.

## Gotchas

- **Definition drift in "delay" and "queue"**: FHWA needed a whole report to catalog how the same MOE names mean different numbers per tool — SimTraffic's 95th-percentile queue (mean + 1.65σ of 2-min maxima) *can exceed its observed maximum*, and its vehicle-count formula yields zero vehicles on a fully jammed link. Every metric we emit needs a normative definition in the message contract (ADR-0006).
- **Single-run conclusions**: near capacity, run-to-run standard deviation hits 25%+; any point estimate from one seed is noise dressed as signal.
- **Cross-tool LOS comparison**: quoting a microsim's "LOS" against HCM LOS is invalid — VDOT: "LOS shouldn't be used to support the results from microsimulation." Letters cannot cross tool boundaries because the underlying measures differ.
- **Survivorship bias in trip statistics**: averaging only completed trips drops exactly the vehicles suffering the worst congestion. Denied-entry (latent) demand and its delay must be accounted (HCM Ch.24 assigns it to upstream links) or oversaturated comparisons lie.
- **Reading capacity off demand**: throughput at an undersaturated point is demand, not capacity; capacity exists only under sustained upstream queue (≥ 15 min discharge).
- **Mean-only reporting**: averages erase the bad tail; agencies moved to 95th-percentile queues, buffer indices, and on-time measures precisely because means hide unreliability. Reliability needs stored samples/distributions, not just aggregates.
- **Time-mean vs space-mean speed**: an induction loop's arithmetic mean and a lane-area detector's travel-time mean differ even at constant speed — mixing them silently corrupts calibration comparisons.

## Open Questions

- **Metric stream budget**: fleet size × 100 ms tick × per-vehicle state size vs NATS throughput — benchmark during engine bring-up (with the NATS backbone work; ADR-0002's small-messages-on-the-hot-path clarification bounds the design).
- **95th-percentile queue estimator**: empirical distribution (needs stored samples) vs mean + 1.65σ (SimTraffic) vs percentile-volume formulas (Synchro) — pick after we see our own queue time series.
- **CRN stream assignment granularity**: which concerns share RNG streams across alternatives (spawn times yes; lane-change draws?) — experiment once the per-vehicle seeded RNG layout from ADR-0007 exists.
- **Reliability baseline for game reveals**: free-flow travel time (PTI) or mean (buffer index) — likely scenario-configurable with PTI the default for reveals.
- **PCU conversion in v1?** HCM LOS grading requires it (truck share); continuous metrics don't. Depends on whether scenarios model vehicle-class mix.

## Related

- [Trajectory Datasets & Overhead Analysis](../business-domains/trajectory-datasets.md) — Edie definitions and NGSIM validation targets; `analysis/ngsim` is the metric kernel's working prototype
- [Macroscopic Flow Models](../business-domains/macroscopic-flow-models.md) — the fundamental diagram gives Edie q/k/u cells their meaning; MFD as a scenario-level view
- [Time Model](../architecture/time-model.md) — tick count stamps metric intervals; replay makes metric re-derivation a validity check
- [Scenario Format](../concepts/scenario-format.md) — metric definitions, detector declarations, and the run protocol (seeds, warmup, intervals) live in the scenario
- [Signal Control](../business-domains/signal-control.md) — control delay is its objective function; virtual detectors feed actuated control
- [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) — heatmap fields are Edie cells; the before/after reveal is median-run replay + continuous-metric scoreboard + LOS skin

---
*Raw research: [raw/domain-congestion-metrics/](../../raw/domain-congestion-metrics/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
