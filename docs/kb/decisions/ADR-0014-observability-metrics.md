# ADR-0014: Observability — trajectory-first metric kernel, metrics plane, measurement sets

- **Status:** ACCEPTED
- **Date:** 2026-07-21 (design review by Claude Fable + GPT-5.6-sol;
  findings folded in — see `docs/kb/raw/reviews/2026-07-21T124819-*`)

## Context

ADR-0012 §5 put measurement sets inside the scenario (`metrics/*.yaml`,
element × metric-type × window bindings) but explicitly left the metric
*catalog* and the wire contract to this ADR. The `domain-congestion-metrics`
research (KB article + `raw/domain-congestion-metrics/`) closed its gates and
recommends: a trajectory-first streaming kernel emitting two primitives
(per-vehicle trip records; Edie q/k/u on time–space cells) with everything
else as derived views; LOS as a pinned-edition presentation skin; and a
built-in multi-seed experiment protocol (paired seeds, CIs, median-run
showcase). The substrate is ratified: NATS-only communication (ADR-0002),
tick clock + replay (ADR-0005), the three-plane taxonomy which already
reserves `ts.{run}.metrics.>` (ADR-0006), seeded per-vehicle RNG
(ADR-0007), and scenario directories whose hash covers metrics parts
(ADR-0012). VISION's before/after reveal and the civic-advocacy use case
both need numbers a traffic agency would recognize, computed
deterministically, comparable across variants.

Two timing constraints shape the phasing: the first real advocacy-corridor
scenario pair is next on the roadmap, and a public demo deadline wants real
MOEs within days — so v1 is the smallest kernel that produces credible
delay/speed/throughput numbers, with the full catalog ratified but phased.

## Decision

1. **One in-process metric kernel; metrics are derived, never simulated.**
   The kernel lives inside the engine process and reads authoritative world
   state each tick (positions, speeds, desired speeds, lane membership,
   stop episodes). It does NOT consume the live snapshot plane: core-NATS
   drops are silent (ADR-0006 §2/§6) and metric validity cannot tolerate
   gaps. The kernel is a read-only observer following the `XTField`
   precedent (`engine/xtfield.go` — caller-driven `Observe(e)` per tick,
   zero kernel changes) — it never feeds back into world state, so
   CRC/keyframe/replay semantics are untouched, and replay re-derivation
   becomes a validity check on the kernel itself (same recording ⇒
   identical metric stream). Accumulation iterates vehicles and lanes in
   stable (sorted) order — floats are non-associative (ADR-0005
   discipline applies to derived numbers too).

2. **Two primitives; everything else is a view.**
   - **Trip record** — emitted when a vehicle leaves the network, AND as a
     flagged partial record (`completed: false`, exit tick = horizon) for
     every vehicle still in-network at horizon. Entered-but-unfinished
     vehicles are precisely the worst-served; omitting them is the
     survivorship bias §3 forbids. Fields: vehicle id, vType,
     origin/destination, entry/exit ticks, distance, **time-loss** (per
     tick: `Δt − distance_moved/v_desired`, accumulated per vehicle —
     SUMO's `timeLoss`, the single actual-vs-ideal primitive), stop count
     and stopped time, `completed` flag. Control delay, stopped delay, and
     queue delay are later filters over this primitive; distribution
     statistics over trip records state their censored fraction.
   - **Lane-interval record** — ONE message per (measurement set element,
     interval); the metric names in a set (§5) select which field groups
     are computed and present. Core is the Edie aggregate: `Σ distance`,
     `Σ time` over vehicle-ticks present, giving `q = Σd/(T·L)`,
     `k = Σt/(T·L)`, `v = q/k` (Edie; `q = k·v` holds identically within
     the cell; space-mean speed, never time-mean — `v` is omitted when
     `Σt = 0`, never zero-filled). Selectable additions: occupancy, stop
     count, accumulated time-loss (definitions in §3). Already validated
     as a method in `analysis/ngsim`.

3. **Normative definitions live in the contract, not in prose.** Every
   emitted metric's definition is pinned in `contracts/asyncapi.yaml`
   next to its schema — FHWA's cross-tool discrepancy catalog is the
   warning. The v1 pins:
   - *Time-loss*: per tick, `Δt − distance_moved/v_desired` with
     `v_desired = speedFactor × min(vType.v0, lane.speedLimit)`; negative
     contributions clamped to 0 per tick; assign-where-it-occurs (HCM
     Ch.24 — it accrues to the lane/interval where it happens, even when
     caused by downstream spillback).
   - *Lane-boundary crossing within a tick*: a vehicle that changes lane
     during a tick splits its distance and time between the two lanes
     proportionally to distance traveled in each (uniform-speed
     assumption within the tick); time-loss splits by the same
     proportion. No whole-tick attribution to either side.
   - *Occupancy*: `Σ (time_present × vehicle_length) / (T × L)` per
     (lane, interval) — the time-weighted space fraction; unitless, in
     [0, ∞) under queued overlap-free dynamics, named `occupancy` to
     match detector vocabulary while remaining a trajectory measure.
   - *Stop*: a vehicle is stopped in a tick when `v < 0.1 m/s`; a stop
     episode is a maximal run of consecutive stopped ticks; stop count
     counts episode starts; stopped time is episode duration. Interval
     attribution: an episode counts toward the interval containing its
     first stopped tick.
   - *Intervals*: `period_s` and `begin_s` must be integral multiples of
     the tick length (100 ms, ADR-0005) — non-multiples fail at load.
     Intervals align to tick 0 offset by `begin_s` and are stamped with
     tick bounds. A final interval truncated by the horizon IS emitted,
     flagged `partial: true`; comparison tooling drops partials.
   - *Denied-entry (latent) demand*: counted per origin lane from the
     spawner/director pending queues — vehicles requested but not yet
     injected, with accumulated wait (request tick → injection, expiry,
     or horizon). Included in run totals so oversaturated comparisons
     don't lie.
   - *Run totals*: derived from interval and denied-entry accumulations
     — completed trips, VMT, VHT, total/mean time-loss (over ALL
     vehicle-ticks, so unfinished trips contribute), denied-entry count
     and wait — never from completed-trip records alone.

4. **Wire contract: a separate metrics stream on the record plane,
   self-contained interval messages.** New subjects
   `ts.{run}.metrics.interval.{set_id}` (one JSON message per set element
   per closed interval — set ids are stable within the scenario, and
   per-set subjects let consumers filter-subscribe per ADR-0006 §3's
   ids-last taxonomy), `ts.{run}.metrics.trip` (one JSON message per
   trip record, including horizon partials), and
   `ts.{run}.metrics.totals` (once, at horizon). All are captured by a
   DEDICATED JetStream stream `ts-{run}-metrics` over
   `ts.{run}.metrics.>` — NOT the run's log stream: the recorder's OCC
   protocol predicts every next stream sequence (ADR-0006 §4/M4), so
   metric messages must never interleave into `ts-{run}-log`. Record
   plane — not core — because experiment analysis needs complete history,
   while live consumers (viz scoreboard) get ~realtime delivery from a
   push consumer. Each message is self-contained (interval aggregates,
   never cumulative deltas), so a consumer joining mid-run loses nothing
   but history it can fetch. Envelopes carry `schema_version`, run id,
   and interval tick bounds (ticks in payloads, never subjects). Engine
   is sole writer on `metrics.>`. Totals are mirrored into the
   run-registry KV for late joiners.

5. **Scenario binding grammar (concretizing ADR-0012 §5).**
   `metrics/*.yaml`, `format_version: 1`:
   ```yaml
   sets:
     - id: mainline-nb            # stable within the scenario
       elements: [lane-ids...]    # v1: flat lists of stable IDs only
       metrics: [edie, occupancy, stops, time_loss]
       window: {period_s: 900, begin_s: 0}   # end_s defaults to horizon
   trips: {}                      # presence enables trip records
   ```
   A scenario with no `metrics/` parts gets the default set: every lane,
   all four field groups at 900 s, plus trip records — so flag-driven
   and demo runs emit metrics with zero authoring. Unknown metric names
   and unknown element IDs fail loud at load (strict fence, same as
   demand parts). Metric parts hash like every other part: a variant
   that changes measurement is a different scenario (ADR-0012 §5's
   point). Totals are always defined: they derive from interval
   accumulations (§3), so a scenario that disables `trips` still
   produces totals and sweep comparisons — only per-vehicle
   distributions disappear, and the compare tool says so.

6. **The kernel is transport-agnostic; two sinks.** (a) the NATS
   publisher above; (b) a file sink for offline runs — `simrun` writes
   `metrics.json` (intervals + trips + totals) next to the run
   artifacts. Same kernel, same numbers; the file sink is the
   analysis/debug path that needs no broker.

7. **Experiment protocol is built-in tooling, not a script users
   write.** Seed sweeps derive run keys `(content-hash, seed)`
   (ADR-0012 §6): `simrun --seeds N` runs N replications with seeds
   derived from the base seed via the ADR-0005 stream discipline, and a
   compare tool reports, per metric: per-seed PAIRED differences
   (variant − baseline on the same seed), the mean difference with its
   95% CI, and min/max — CRN's point is shrinking the variance of the
   difference, so the difference is the reported statistic, not two
   independent CIs. The showcase seed is chosen ONCE as the median
   baseline run by total time-loss (partials and denied-entry included)
   and that seed's runs are showcased across baseline AND every variant —
   per-scenario independent medians would mix seeds and break the paired
   story. Warmup auto-detection (FHWA occupancy-equilibrium rule) is
   deferred: v1 horizons are set long enough to include warmup and
   `begin_s` excludes it by hand. Single-seed conclusions are explicitly
   unsupported by the tooling's output shape (it prints paired CIs or
   nothing near capacity).

8. **LOS is a presentation skin, pinned edition, never the ranking
   metric.** Alternatives rank on continuous metrics (time-loss, travel
   time, throughput, reliability). Letter grades — when a report wants
   them — derive at presentation time from HCM 6th-edition thresholds
   stored in config, labeled with edition and conversion. Cross-tool LOS
   comparison stays invalid (VDOT/NYSDOT position) and our reports say
   so. Not in v1.

## Phasing (consequences, not separate decisions)

- **M13 (v1):** kernel; trip records (incl. horizon partials);
  lane-interval records with the §3 definitions; totals; NATS + file
  sinks; scenario `metrics/` parts with the §5 grammar; default set.
  This is the demo-critical slice.
- **M14:** seed-sweep runner + paired comparison report (§7).
- **Later (own milestones, this ADR's definitions hold):** queue state
  machine with documented hysteresis thresholds (avg/max/95th back of
  queue, maxima sampled inside windows); control delay decomposition;
  reliability indices (buffer index, PTI, TTI — needs stored per-OD
  samples); LOS skin; warmup auto-detection.

## Explicitly NOT decided (deferred, with owners)

- **Virtual detector layer** (E1/E2/E3 emulation for field calibration
  and actuated control) — deliberately separate from trajectory measures
  (time-mean vs space-mean are not interchangeable); own milestone when
  calibration against field data starts.
- **Emissions/fuel post-processor** — offline HBEFA-class lookup over
  the trajectory stream; deferred per the research; a named consumer of
  trip records when it lands.
- **PCU conversion** — needed only for HCM LOS density grading; lands
  with the LOS skin, depends on whether scenarios model vehicle-class
  mix beyond vType weights.
- **95th-percentile queue estimator** — empirical samples vs mean+1.65σ
  vs percentile-volume formulas; pick after seeing our own queue series
  (research open question stands).
- **CRN stream-assignment granularity** beyond spawn draws (lane-change
  etc.) — experiment once sweeps exist.
- **Richer element selection** (named selections, spatial queries) in
  `metrics/*.yaml` — ADR-0012's deferral stands; flat ID lists until a
  real scenario needs more.
- **TSSF snapshot schema bump** (speed, lane id — the M6 viz gap) —
  unaffected by this ADR (the kernel reads kernel state, not snapshots);
  remains an independent candidate when a second live consumer needs
  kinematics.

## Consequences

- `contracts/asyncapi.yaml` grows the metrics channels and the
  `ts-{run}-metrics` stream (info version bump; additive — old clients
  never subscribe `metrics.>`); the normative definitions of §3 are
  contract text, so changing a definition is a contract change requiring
  an ADR note (AGENTS.md §5).
- The log stream's OCC sequencing is untouched (separate stream); the
  run registry gains metrics totals in KV; recordings implicitly include
  the metric stream, making post-hoc analysis a stream read, not a
  re-simulation.
- Metric values are deterministic functions of the run but are NOT in
  the rolling CRC (they're derived, not state); replay re-derivation
  equality and kernel-attached-vs-free CRC equality are kernel
  regression tests.
- The pending-ADR queue shrinks to two: network model, license.
- Revisit when: the first seed sweep shows whether CRN pairing needs
  finer stream granularity; the first field-calibration scenario forces
  the detector layer; viz wants live Edie heatmaps at <900 s latency
  (window trade-offs); queue definitions prove out against NGSIM.

## Addendum (2026-07-21, M13 implementation)

The metric kernel, the JSON file sink (`simrun`/`serve -metrics-out`), and
the scenario `metrics/*.yaml` bindings all land in THIS commit, after
thirteen Fable+Sol gate rounds (`docs/kb/raw/reviews/2026-07-21T13*`–`T17*`):

- **Whole-run scoping (decision, not omission):** kernel state is NOT
  keyframe/replay-persisted. Metrics are computed over whole runs, and
  mid-run re-derivation is UNSUPPORTED: `NewKernel` rejects attach at any
  tick ≠ 0, and `Observe`/`Finalize` panic on a skipped or doubled tick.
  Replay re-derivation equality remains a regression test over whole
  recordings.
- **Interval conventions as implemented:** observation tick T covers
  (T−1)·dt…T·dt, so intervals hold ticks (B, B+P] and records stamp the
  SIM-TIME tick grid: an interval covering observations (begin, end]
  stamps [begin, end) — BeginTick·dt…EndTick·dt converts naively to sim
  time, with duration (EndTick − BeginTick)·dt. Horizon partials stamp
  end = last observed tick; exact horizons yield no manufactured partial.
  `MetricSetConfig.LastTick` (config) is inclusive; `IntervalRecord.EndTick`
  (record) is exclusive — named differently for exactly that reason.
- **Lane-crossing attribution:** movement attributes to the TRAVELED
  lane(s) — integration/boundaries run before lane changes in Step, so a
  lateral hop is an instantaneous tick-end event (its tick books to the
  pre-hop lane). Multi-boundary chains (sub-4 m OSM internal lanes are
  routine — I-280's minimum is 0.1 m) walk the successor chain by BFS
  and book each intermediate lane its full length. Unresolvable or
  ambiguous-branch cases book conservatively and count
  `Totals.DroppedCrossings` — loud, never silent.
- **Approximations (v1 semantics, documented in code):** despawn
  exit-distance is exact (in-network remainder); exit-TIME uses the
  last-observed speed (the final accel is unobservable — constant-speed
  is the unbiased midpoint); vehicles spawned and crossed/despawned
  within their first tick are unobservable at this layer (unreachable on
  current nets — shortest I-280 origin lane is 13 m ≫ 4.3 m/tick — and
  loud via `DroppedCrossings` if a net ever violates it); spawner
  denied-entry backlog is a per-tick INTEGRATED mean-field estimate
  (newly overdue: 1, the held vehicle — the pairing guard makes
  first-overdue lag always 0; then += rate·dt per overdue tick, −1 per
  injection, floored at 0 — a DemandSchedule rate change applies
  prospectively only); expired director directives keep accrued wait
  (wait-without-pending) and count toward `DeniedServed`.
- **Engine alternatives deliberately not taken:** making `boundaries()`
  cascade crossings regardless of lane index order (so S ≤ Length is a
  real invariant) would change the CRC streams and is ADR-0005-adjacent —
  a non-goal for M13, noted as a revisit candidate; the kernel's
  overshoot-refund bookkeeping is exact in the meantime.
- **SchemaVersion 1** for the metrics schemas: channels version
  independently (the TSSG v1 precedent, ADR-0006 2026-07-20 addendum);
  ADR-0006's "schema_version 2" bound the M4-era envelopes.
- **The §3 contract pin lands with the NATS publisher commit**
  (asyncapi channels + `ts-{run}-metrics` stream — that publisher is the
  one remaining M13 slice, NOT in this commit); the JSON file sink
  (`simrun`/`serve -metrics-out`, schema_version 1) is in THIS commit as
  the demo-critical path.
- **Migration note — `EnqueueSpawn` duplicate-RequestID rejection:**
  wire-produced recordings are UNAFFECTED (the contract layer's run-wide
  first-seen dedup plus its empty-ID rejection means they cannot contain
  simultaneously-live duplicate IDs); only pre-change LOCAL-harness
  recordings with such pairs become retroactively unreplayable. The
  rejection surfaces as a verb rejection, never a run failure.
- **Metrics-part hash transition is intentional (no format_version bump):**
  metrics parts moved from raw-bytes to canonical-YAML hashing without
  touching ADR-0012's promised bump because no scenario in any pinned
  golden vector or in-tree carries a metrics part — no durable hash moves.
  Old free-form metrics parts now fail loudly at load instead of silently
  re-hashing. ADR-0012 §8's "under a format_version bump" note stands
  corrected by ADR-0014 §5.
- Six rounds' real catches before any number became decision-grade:
  ring-wrap distance corruption, pre-placed phantom distance, despawn
  tick accounting, interval off-by-one, hop-attribution to the wrong
  lane, a 10× denied-backlog unit error, and the multi-boundary void.
