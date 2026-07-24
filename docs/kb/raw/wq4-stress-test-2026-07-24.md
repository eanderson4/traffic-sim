# WQ-4 — Big-simulation stress test: stress-dtla at 1200 veh/h/lane

Date: 2026-07-24. Executor: subagent session (WQ-4 dispatch). Machine: this dev box
(Ryzen 7 9700X-class, 123 GB RAM), other tenants present — noted per-run below.

## Verdict

**FAIL (bounded, graceful, and now precisely measured).** The architecture holds to
~10.9k live vehicles, then the record plane kills the run — loudly, by design — when
the full-state keyframe exceeds NATS `max_payload` (1 MB). This is the exact ceiling
BENCHMARKS.md §(b) predicted ("keyframes hit the wall at ≈13.6k vehicles; chunking
decision must land before city-scale"); the real per-vehicle keyframe cost is higher
than the microbench estimate, so the wall arrives at ~10.9k, not 13.6k. Below that
wall everything is healthy: NATS/ws pipeline clean at 276 kB snapshots, metrics
sane, recordings coherent. The kernel itself is compute-bound past ~7.5k vehicles
for realtime (1×) pacing: **kernel Step ≈ 7 ms + 12.4 µs/vehicle per tick** on a
quiet box — the prior "~7 ms/tick at 8k vehicles" figure (freshness note b) is wrong
by ~15× and should be corrected wherever it was recorded.

## Setup

- New scenario `data/scenarios/stress-dtla-high/` (untracked): copy of stress-dtla
  (14903 external lanes, 1202 signal programs), spawner 1200 veh/h/lane, seed 42,
  7200 ticks (12 min sim, dt=0.1s). Scenario hash d66585c7… (same content hash family
  as stress-dtla; new manifest id `stress-dtla-high`).
- Runs used a scratch instrumented serve (`/tmp/wq4srv`, NOT in the repo): identical
  wiring to `cmd/serve` + an inlined copy of `natsio.RunLive`'s loop (same public
  components, same order) with per-tick, per-section wall timing to CSV and a loud
  pause-gate logger. Built against the working tree, which contains the parallel
  session's uncommitted driver/demosrv changes (driver hello-retry block was
  mid-conflict-resolution during the session; the two sides are functionally
  identical — both retry hello until MetaWait).
- `-pace 0` is refused with the driver attached (by design), and driverless runs
  don't drive (holdlast), so realistic congestion requires a finite pace. Both big
  runs used `-pace 4` (25 ms floor), `-capacity 50000`.

## Runs

| Run | Config | Outcome |
|---|---|---|
| A `wq4-stress` | pace 4, keyframe-every 100 (default) | **Aborted at tick 6300** (10,862 vehicles): `log write ts.wq4-stress.log.keyframe tick 6300: nats: maximum payload exceeded`. 398 s loop wall for 630 s sim (1.58× eff.). Recording 980 MB, registry status correctly `aborted`. |
| B `wq4-stress2` | pace 4, keyframe-every 10000 (tick-0 keyframe only) | **Completed 7200 ticks**, 12,778 vehicles at horizon. 705 s loop wall for 720 s sim (1.02× eff., box at load ~21 from other tenants). Metrics JSON written (19.5 MB). Recording 962 MB, status `done`. |

Plus a kernel-only scratch bench (`/tmp/wq4bench`, no NATS, in-kernel IDM harness
policy, same network/demand/seed): peaked at 3,937 vehicles (the kernel reference
policy distributes flow differently; it does not reproduce the driver-driven
gridlock depth) — used only for the cost-curve cross-check below.

## Headline: kernel ms/tick vs live vehicle count

Per-tick section timing from Run A (quiet box, load ~1.5), means per 1k-vehicle bucket:

| vehicles | kernel Step ms | obs gen (AfterStep) | snapshot publish | record LogTick | total work |
|---|---|---|---|---|---|
| <1000 | 7.1 | 0.5 | 0.09 | 1.9 | 11.6 |
| 2000 | 27.1 | 2.2 | 0.47 | 5.8 | 38.7 |
| 4000 | 50.1 | 4.1 | 0.67 | 0.14 | 58.6 |
| 6000 | 73.9 | 6.3 | 0.90 | 0.18 | 85.8 |
| 8000 | 98.8 | 6.0 | 0.84 | 0.14 | 110.0 |
| 10,000 | 131.1 | 6.8 | 0.91 | 0.12 | 143.3 |
| 12,000 (Run B, loaded box) | 188.7 | 9.7 | 1.31 | 0.11 | 205.7 |

- **Kernel Step is linear in vehicles: ≈ 7 ms base + 12.4 µs/vehicle/tick.** The
  kernel-only IDM bench gives ≈ 4 ms + 13 µs/vehicle — statistically the same slope,
  so Step cost is dominated by integration/boundary/occupancy/spawn work, not by
  which side computes the car-following policy.
- Realtime (1×, 100 ms tick) ceiling on this hardware: **~7.5k live vehicles**.
  At pace 4 (25 ms floor) the loop was compute-bound from ~2.5k vehicles upward.
- Everything off the kernel is small and well-behaved: observation generation for
  the driver's 250 m policy-ctx window ≈ 0.8 µs/vehicle (9.7 ms at 12k), snapshot
  encode+publish ≈ 0.1 µs/vehicle (matches BENCHMARKS §(b)/(c)), record-plane
  LogTick ~0.1 ms/tick in congestion (spawn-verb logging makes it 2–6 ms early).
- Run B's >6k buckets run ~1.4–1.9× above Run A's curve — pure multi-tenant
  contention (load 21), included as the "loaded box" envelope, not a regression.

## Scale ceilings found (in order)

1. **Realtime pacing ceiling ~7.5k vehicles** (kernel compute). Graceful: run just
   falls behind pace; no wedge, no error.
2. **Record-plane keyframe wall ~10.9k vehicles — run-killing.** Full-state keyframe
   exceeds the broker's 1 MB `max_payload`; the recorder's fail-loud contract aborts
   the run (registry finalized `aborted`, error printed). Confirms BENCHMARKS.md §(b)
   consequence flag: keyframe chunking (or Object Store) must land before city-scale;
   the microbench's 77 B/veh keyframe estimate underestimates the real frame
   (≥92 B/veh incl. signals/route/type state), so the wall is ~10.9k, not ~13.6k.
   Workaround used for Run B: `-keyframe-every` raised so only the tick-0 keyframe
   exists (replay then must scan from tick 0 — acceptable for a 12-min recording).
   Not attempted: snapshots (24 B/veh → ~43k ceiling) and driver observations
   (flowed fine at 12.8k) — their walls are further out.

## ADR-0008 §6 pause-gate deadlock: NOT reproduced

Zero pause-gate engagements in both runs (loud logger added for exactly this;
grep count 0 in both logs). With `-capacity 50000` >> 12.8k peak active, the gate
never armed. The prior wedge (demand > spare capacity in a jammed run) is avoided by
the documented workaround; the underlying deadlock (resume requires demand ≤ spare
capacity, but a jammed run's active count never drops) remains unfix­ed — WQ-4's
"needs an escape hatch or at least a loud log/metric" stands as an engine TODO. The
scratch harness's loud-pause logger pattern (pause engage/release + 10 s heartbeat
with tick/vehicle count) is a candidate shape for it.

## NATS / ws pipeline health

- Zero errors/drops in both run logs; no slow-consumer statuses on the ws plane.
- ws probe (nats.ws subscriber on the browser listener): Run A — 1,024 snapshots in
  74.9 s, 1:1 with ticks, ~164 kB frames at ~6.8k vehicles; Run B — 327 frames in
  60 s at ~276 kB (~11.5k vehicles), again 1:1. Binary snapshot frames to a browser
  client are solid at this scale (≤ ~1.5 MB/s here; BENCHMARKS §(c) says the broker
  has ~2 orders more headroom).
- The in-process driver kept up at pace 4 through 12.8k claimed vehicles (hold-last
  heals any per-tick lag; no claim-violation aborts). Not instrumented: exact
  intent-lag distribution at peak — a follow-up if pacing above 4× matters.

## Congestion / metrics sanity (Run B, 7200 ticks, horizon state)

- 12,778 active at horizon; only 1,706 completed trips vs 9,552 spawned — deep
  gridlock, metrics-visible exactly as in freshness note (b).
- Mean time-loss 252.6 s/veh (healthy 60-s smokes: 12–25 s/veh); network mean speed
  4.16 km/h (prior gridlock observation: 3.87 km/h). VMT 4,532 lane-km vs VHT
  1,090 veh-h.
- Spawner denial is the pressure gauge: 4.04M s of denied-entry wait, top origin
  lanes each denied ~85,000 s with ≤2 insertions served in 12 min.
- `dropped_crossings = 83,212` — high; consistent with the known `_d2` fragment-lane
  issues from note (b) (item 5); worth its own look, not WQ-4 scope.
- Trajectory (Run B): 1.4k veh @1 min → 2.7k @4 min → 7.0k @8 min → 10.2k @10 min →
  12.8k @12 min and still climbing — congestion had NOT saturated at horizon; a
  longer run would push into the snapshot wall (~43k) only after the keyframe wall
  (~10.9k) already killed it. The keyframe wall is the binding constraint.

## Memory

RSS sampled via ps: Run A 2.75 GB @3.7k veh → 2.23 GB @10k (GC); Run B 2.2 GB @10.5k
→ 2.23 GB @12.8k. Flat ~2.2–2.8 GB working set (27 MB network + engine state +
embedded broker + driver's own net copy + metrics kernel). No growth trend.

## Recordings on disk

- `data/recordings/wq4-stress/` (980 MB, 4,131,872 msgs, status `aborted` — correct)
- `data/recordings/wq4-stress2/` (962 MB, 4,133,711 msgs incl. 7,200 CRC records,
  status `done`, seed 42, net path absolute)
- Both verified by reopening the JetStream stores with a fresh embedded broker:
  streams enumerate, registry meta decodes, subject mix sane
  (keyframe/crc/intent/spawn-verb). NOT done: full replay-with-CRC verification
  (demosrv replay path belongs to the parallel session's processes; replay of
  12.8k-vehicle state is also exactly where the 60-frame SnapshotBuffer and
  keyframe-seek deferrals in WQ-0 live). Note: demos.json deliberately untouched.
- Metrics: `data/recordings/wq4-stress2.metrics.json` (19.5 MB, 39,971 lane
  intervals + 14,484 trip records).

## Follow-ups (for the KB / work queue)

1. Fix freshness note (b)'s "~7 ms/tick at 8k vehicles": measured 12.4 µs/veh/tick
   → ~106 ms/tick at 8k on this box. Realtime ceiling ~7.5k vehicles.
2. Keyframe chunking / Object Store is now a demonstrated run-killer at ~10.9k
   vehicles, not a projection (Run A abort signature above). Bump its priority;
   it gates any city-scale recording. Also correct the 77 B/veh microbench estimate.
3. Pause-gate deadlock escape/loud-metric still needed (not triggered here only
   because capacity was raised 50×).
4. `dropped_crossings` = 83k on stress-dtla-high — correlate with `_d2` fragments.
5. Benchmark Queue (WQ-10) can now take: per-vehicle wire size measured at 11.5–12.8k
   (snapshot 24 B/veh flat to 276 kB; keyframe ≥92 B/veh real), kernel Step slope
   12.4 µs/veh, obs-gen 0.8 µs/veh, ws delivery 1:1 at 276 kB frames.
6. Scratch artifacts (rebuildable, /tmp): `/tmp/wq4srv` (instrumented serve),
   `/tmp/wq4bench` (kernel bench), `/tmp/wq4verify` (store verifier),
   `/tmp/wq4-wsprobe.mjs`, timing CSVs `/tmp/wq4-live-timing{,2}.csv`,
   `/tmp/wq4-kernel-curve.csv`, run logs `/tmp/wq4-run{A,B}.log`.
