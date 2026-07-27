# BENCHMARKS.md — M3 NATS bring-up measurements

The deferred bring-up benchmarks from the `arch-nats-backbone` open
questions (ADR-0006 consequences: "benchmark before sizing batch
multipliers"; keyframe chunking "once the vehicle-count/byte curve is
measured"). Run: `go test -run '^$' -bench .` in `engine/`.

- **Date:** 2026-07-17
- **Machine:** AMD Ryzen 7 9700X 8-Core, Linux 6.12 (x86_64), go1.26.5
  linux/amd64
- **Setup:** embedded nats-server v2.14.3 in-process (`DontListen` +
  `nats.InProcessServer`, net.Pipe — **no TCP, no TLS**), JetStream R=1
  file storage on a local temp dir, default sync behavior. Numbers are the
  best-case transport; a socketed compose deployment adds per-message
  network cost but keeps the broker-side behavior measured here.

## (a) JetStream publish-ack latency vs the 100 ms tick

`BenchmarkJetStreamPubAck`: per tick-batch, N intent-log-sized messages
(~24 B payload) with the recorder's `Nats-Msg-Id` + `Nats-Expected-Last-
Sequence` headers, published async, all pubacks awaited (the ADR-0006 §4
record-plane contract the tick depends on asynchronously).

| Intents per tick (rate) | Batch latency | Per message | Share of 100 ms tick |
|---|---|---|---|
| 1 (1×) | 12.0 µs | 12.0 µs | 0.012 % |
| 10 (10×) | 75.4 µs | 7.5 µs | 0.08 % |
| 100 (100×) | 517.6 µs | 5.2 µs | 0.52 % |

**Interpretation.** Puback latency is three orders of magnitude under the
tick budget at every plausible intent rate: amortized cost is ~5 µs per
logged message and sub-linear in batch size (per-batch fixed cost ≈ 7 µs +
≈ 5 µs/msg). The record plane is NOT the pacing constraint — batch
multipliers (faster-than-realtime runs at 10×/100× intent volume) are
bounded by the kernel sweep, not the broker. Caveat: R=1 file storage with
the default sync interval acknowledges from broker memory/OS buffers; a
machine crash has a durability window (synthesis §3), and a socketed or
TLS deployment adds constant per-batch latency — re-measure before sizing
multi-node or WAN deployments.

## (b) Snapshot / keyframe byte curve vs vehicle count

`BenchmarkSnapshotBytes` (24 B/vehicle snapshot record: id u64 + 4 × f32;
~77 B/vehicle keyframe record incl. raw RNG words):

| Vehicles | Snapshot frame | Keyframe | Snapshot encode |
|---|---|---|---|
| 100 | 2,424 B | 7.7 kB | 646 ns |
| 1,000 | 24,024 B | 77 kB | 5.3 µs |
| 10,000 | 240,024 B | 770 kB | 63 µs |

**Interpretation.** Both curves are exactly linear (24.0 B/veh and 77.0
B/veh flat). Against ADR-0002's 1 MB `max_payload` discipline: snapshots
fit to ≈ 43k vehicles; **keyframes hit the wall at ≈ 13.6k vehicles** —
the keyframe-chunking (or Object Store) decision must land before
city-scale networks, as the synthesis predicted. **Resolved 2026-07-24
(ADR-0015):** the WQ-4 stress test hit the wall in production conditions
at ~10.9k vehicles (real keyframe ≥92 B/veh, not the 77 B/veh above —
per-vehicle controller state varies), and keyframes larger than 768 KiB
are now chunked into consecutive log messages (`kf_chunk` header), so
keyframe size is bounded by disk/memory, not `max_payload`. Encode cost
is linear and negligible (63 µs at 10k = 0.06 % of the tick); snapshot
size is a broker bandwidth question, not a tick-budget question. Live
frames at the validated scenario scale (I-80 ≈ 300 vehicles → ≈ 7 kB/tick)
are far under any concern.

## (c) Live-plane fan-out

`BenchmarkLiveFanout`: one 2,424 B snapshot frame (~100 vehicles) published
per op to 1 and 8 concurrent subscribers, all deliveries drained.

| Subscribers | Publish op (tick cost) | Delivered rate |
|---|---|---|
| 1 | 1,278 ns | 772k msgs/s |
| 8 | 5,869 ns | 1.36M msgs/s (aggregate) |

**Interpretation.** Publisher-side cost stays in single-digit microseconds
even as subscribers multiply (the server fans out; the engine just writes
to the pipe) — the live plane cannot stall the tick at any realistic
subscriber count, confirming ADR-0006 §6's stance that slow consumers are
a subscriber problem, never the engine's. At 10 Hz × ~7 kB (I-80 scale) a
single broker core is ~4 orders of magnitude below saturation; headroom
for 10 Hz × 240 kB (10k vehicles) is still ~2 orders.

## (d) Fleet-scale intent ingest (ADR-0026 M0 baseline)

- **Date:** 2026-07-26
- **Machine:** AMD Ryzen 7 9700X 8-Core, Linux 6.12 (x86_64), go1.26.5
  linux/amd64
- **Setup:** embedded nats-server in-process (`DontListen` +
  `nats.InProcessServer`, net.Pipe — **no TCP, no TLS**), as in (a)–(c).

`BenchmarkIntentIngest` (`engine/natsio/bench_intent_test.go`) fills the
fleet-scale intent-ingest gap this file had (pubacks measured only to
100 intents/tick; fan-out measured delivery, not many-publisher ingest).
It is the **baseline the ADR-0026 batched-intents change (TSIB, M1–M3)
is measured against**. K=4 controller connections each publish n/4
per-vehicle v2 intents per tick (44 B, `EncodeIntent`, distinct vehicle
ids) on their own `ts.{run}.ctl.intent.{ctl}` subjects against the REAL
ingest path: broker route → `Bus.onIntent` (callback → lock → append) →
per-tick `Contract.DrainIntents` (claim filter, seq/grant stamping,
hold-last scan). Controllers attach over the wire (hello handshake, drive
grant, claims at attach) so every drained intent passes the claim filter
— asserted per tick (drained == published; no `Held` re-issues; claim
violations == 0 or the benchmark fails). The measured path stops before
kernel apply and record-plane logging. Scope note: the obs/AfterStep
plane is deliberately out of scope — TSIB (M1–M3) does not touch it, so
ingest+drain is the correct comparison scope for M3 (pinned by ADR-0026).
3 warmup + 100 measured ticks per size, `b.N=1`; percentiles are
nearest-rank over the 100 samples (p99 = 2nd-highest). Run:

    go test -run '^$' -bench IntentIngest -benchtime=1x -timeout 20m ./natsio/   # from engine/
    go test -run '^$' -bench OnIntentCPU -benchtime=3s ./natsio/                 # companion, per-callback CPU

Two spans are recorded per tick: **publish start → engine buffer full**
(the delivered-rate ceiling) and **publish start → `DrainIntents`
return** — the **controller-publish→drained latency**, the like-for-like
M0↔M3 comparison baseline ADR-0026 M0 asks for (its p50/p99 and the
drain-complete intents/s below). Runs under a settling external load
(load average ~5) agreed within ~10–15% on means; an earlier pass at
load ~23 ran ~50% slower across the board, so treat absolute numbers as
this-machine-this-load.

| Vehicles (intents/tick) | Publish → buffer | Delivered rate (publish→buffer only) | DrainIntents / tick | Tick p50 (publish→drained) | Tick p99 (publish→drained) | Complete path (drain-complete) |
|---|---|---|---|---|---|---|
| 5,000 | ~2.2 ms | 2.28M msgs/s | 1.11 ms | 3.34 ms | 3.89 ms | 1.51M intents/s |
| 15,000 | ~5.2 ms | 2.86M msgs/s | 3.89 ms | 9.02 ms | 11.58 ms | 1.64M intents/s |
| 30,000 | ~9.6 ms | 3.13M msgs/s | 8.98 ms | 18.27 ms | 22.10 ms | 1.62M intents/s |

**onIntent CPU** (`BenchmarkOnIntentCPU`, the M0 deliverable): the
callback invoked directly — no broker, no delivery goroutine, no drain —
costs **~61 ns/message** (subject tokenize + `DecodeIntent` + lock +
append, 44 B payloads rotating across 4 controller subjects). That is
~1.8 ms of pure callback CPU per 30k tick and a single-delivery-goroutine
ceiling of ~16M msgs/s callback-side. The publish→buffer span in the
table is an **end-to-end proxy**: it is this ~61 ns callback PLUS broker
route and delivery-goroutine scheduling per message — the gap between
~61 ns and the ~320 ns/message the 3.13M msgs/s row implies is broker +
scheduling, not callback work.

**Interpretation.** Two different rates, stated plainly: the
**publish→buffer delivered rate** saturates around ~3.1M msgs/s
in-process, but that number excludes the drain — the **complete
controller-publish→drained path runs at ~1.5–1.6M intents/s at every
size**, and *that* is the like-for-like baseline M3 compares against.
What these spans are NOT: engine tick-budget consumption. The production
run loop never waits for a full buffer — controllers publish
asynchronously while the engine ticks, so the publish→drained latency
lands off the run loop's critical path. The honest run-loop number is
**`DrainIntents`: ~1.1 / 3.9 / 9.0 ms per tick at 5k/15k/30k (≈ 9 % of
the 100 ms tick at 30k)**, plus the ~61 ns/message `onIntent` callback
CPU (~1.8 ms at 30k) on the delivery goroutine. `DrainIntents` grows
super-linearly (15k→30k more than doubles): past the linear
claim-filter/seq-stamp work, the hold-last scan sorts the full
tracked-vehicle id set every drain — O(n log n) at fleet scale. Note the
1.36M msgs/s figure in §(c) is a *different shape* (8-subscriber fan-out
delivery of 2.4 kB frames), exactly the distinction ADR-0026's context
table draws; many-publisher 44 B ingest is its own ceiling, and this
table is it. Caveats: net.Pipe transport is the best case — a
socketed/TLS deployment lowers the delivered rate (the §(a) caveat
applies here doubled, per-message on the ingest side); publish payloads
are pre-encoded and reused, so encode cost is excluded (driver-side
concern, not engine ingest); the buffer-full poll runs at 50 µs
quantization, which inflates the smallest size's publish→buffer figure
by up to ~2% (negligible at 15k/30k); run-to-run noise on this shared
box is ±10–15% on means and worse on p99 (see the load note above).

### Batch mode (ADR-0026 M3)

`BenchmarkIntentIngestBatched` (`engine/natsio/bench_intent_test.go`,
same shared harness body) reruns the M0 scenarios with each controller
publishing **one TSIB per tick** (`natsio.NewTSIBMsg`, informational
header tick patched per tick) instead of n/4 v2 messages — the M2 driver
shape. Measured 2026-07-26 on the M0 machine, near-idle box, v2 baseline
and batch mode interleaved A/B/A/B in the same session (the v2 rerun
landed ~10% faster than the M0 recording above — same machine, less
load; the comparison below is the same-session rerun, M0 table stays the
baseline of record). Ranges are the two A/B pairs; run:

    go test -run '^$' -bench IntentIngest -benchtime=1x -timeout 20m ./natsio/   # v2 and batched, back to back

| Vehicles | Mode | Intent msgs/tick | Delivered rate (records/s) | DrainIntents / tick | Tick p50 | Tick p99 | Complete path |
|---|---|---|---|---|---|---|---|
| 5,000 | v2 (rerun) | 5,000 | 2.40–2.45M | 0.96–1.00 ms | 3.06–3.07 ms | 3.64–3.69 ms | 1.64M intents/s |
| 5,000 | TSIB | **4** | 13.3–14.4M | 0.83–0.85 ms | 1.04–1.07 ms | 2.12–2.31 ms | 4.14–4.18M intents/s |
| 15,000 | v2 (rerun) | 15,000 | 2.98–3.01M | 3.13–3.23 ms | 8.11–8.31 ms | 9.59–9.76 ms | 1.82–1.85M intents/s |
| 15,000 | TSIB | **4** | 23.1–35.0M | 2.95–3.18 ms | 3.49–3.55 ms | 4.51–4.75 ms | 4.16M intents/s |
| 30,000 | v2 (rerun) | 30,000 | 3.20–3.28M | 7.63–7.94 ms | 16.68–17.11 ms | 19.02–21.36 ms | 1.73–1.79M intents/s |
| 30,000 | TSIB | **4** | 21.2–22.5M | 7.16–7.52 ms | 8.25–8.84 ms | 10.54–12.69 ms | 3.36–3.53M intents/s |

**M3 targets, point by point.**

- **Intent msgs/tick ≈ controller count — MET, exactly.** 4 messages per
  tick at every size (vs 5k/15k/30k), asserted per run via
  `Bus.IntentBatchStats` (batches == controllers × ticks, every record
  expanded, zero batch/record drops — the benchmark fails otherwise).
- **Tick p99 ≤ baseline — MET with ~45–55% headroom.** 30k p99 drops
  19.0–21.4 ms → 10.5–12.7 ms; 15k 9.6–9.8 → 4.5–4.8 ms; 5k 3.6–3.7 →
  2.1–2.3 ms. The win lands where M0 said the wall was: per-message
  broker route + delivery scheduling, not payload bytes.
- **`DrainIntents` unchanged — as designed.** 0.83–0.85 / 2.95–3.18 /
  7.16–7.52 ms vs v2's 0.96–1.00 / 3.13–3.23 / 7.63–7.94 ms — within
  noise of identical. Expansion lands the same records upstream of the
  claim filter, so the drain cost (including its super-linear hold-last
  scan) is mode-independent; with the wire cost gone, the drain is now
  ~85–90% of the complete 30k tick span and the next optimization
  target. The complete path improves ~2.5× at 5k/15k (1.64M → ~4.16M
  intents/s) and ~2× at 30k (1.76M → ~3.4M); delivered records/s per
  size: 5.4–6.0× @5k, 7.7–11.7× @15k (widest sample, see caveats),
  6.5–7.0× @30k.
- **Applied lag: batch per-vehicle p50 AND p99 ≤ v2 at production pace,
  plus complete-response equality — MET**, measured by
  `TestBatchAppliedLagBoundary` at 400 vehicles (10 ms pace) and
  confirmed at 5,000 vehicles (100 ms scale leg); the 1.5 ms straddle
  behavior below is report-only mechanics. The claim is exactly that
  measured per-vehicle p50/p99 result — no stronger distributional
  statement (see the neither-dominates mechanics note below) — and it
  rests on parts pinned where provable: (a) a route-update vehicle rides
  the IDENTICAL standalone
  v2 wire shape in both modes, so its lag is unchanged by batching (wire
  shape pinned by `TestBatchRouteUpdateTickStandaloneV2`); (b) a
  route-free response of ≤ `TSIBMaxRecords` vehicles is ONE atomic
  message in batch mode vs an N-message stream in v2 — the batch cannot
  straddle a boundary, the stream can (covered directly by
  `TestBatchAppliedLagBoundary`: batch splits always 0, v2 splits > 0 at
  straddling pace); (c) above the cap the batch splits into ⌈n/20k⌉
  messages — O(1) vs O(n), the same argument with a smaller but still
  decisive margin (reasoning, supported by the delivered-rate data in
  the table above); (d) a batch is published after the driver's per-tick
  compute — the same moment the v2 stream's LAST message leaves — so
  batch complete-delivery ≤ v2 complete-delivery for the same compute,
  BY THE MEASUREMENTS at these scales (the encode tail is ~0.43× of
  v2's, BenchmarkIntentEncode table below; the delivered-rate gap is the
  M3 table) — scoped to the 5k–30k in-process envelope, not a universal
  claim. The measurements below are the
  empirical confirmation, not the basis. `TestBatchAppliedLagBoundary`
  (`engine/natsio/driver_test.go`): a manual-loop harness (real engine +
  Bus + Contract + one real driver, no RunLive) runs DrainIntents → Step
  → AfterStep on ABSOLUTE deadlines (iteration k sleeps until
  start + k×pace; overruns counted and reported — REQUIRED zero on
  acceptance legs, the exact comparison is only valid on an undisturbed
  schedule) — identical fixed schedule in both modes, warm-up quiescence
  hard-guarded — with exact FIFO attribution of every drained fresh
  intent to its source obs tick, recording COMPLETE-application per
  response and application lag per INDIVIDUAL intent. 5/5 consecutive
  runs identical:
  - **Production pace (10 ms, 400 vehicles, 300 ticks):**
    complete-application lag **p50 1 / p99 1, both modes** — the exact
    cross-leg comparison (load-bearing); per-vehicle lag p50/p99 1/1
    both modes (all 120,000 samples at lag 1); splits 0/300 both modes;
    deadline overruns zero. (10 ms, not 3: with the real
    DrainIntents → EnqueueIntent → Step apply in the loop, iteration
    work has occasional ms-scale spikes — log/GC churn — and the
    undisturbed-schedule requirement is load-bearing; the response
    margin stays huge either way at this fleet size.)
  - **Scale leg (100 ms, 5,000 vehicles, 100 ticks):** the same
    undisturbed shape at fleet scale — complete-application **p50 1 /
    p99 1, both modes**; per-vehicle p50/p99 1/1 both (all 500,000
    samples at lag 1); splits 0/100 both modes; deadline overruns zero.
  - **Fast pace (1.5 ms, 400 vehicles, ILLUSTRATIVE):** boundaries
    deliberately fall mid-response; the load-bearing pins are **TSIB
    splits 0/300 (structural) and v2 splits 17–29/300 (~6–10%, case
    exercised)**.
    Percentile numbers are report-only (scheduler-sensitive at this
    pace): complete-application **p50 1 / p99 2, both modes** (one run
    v2 p99 3); per-vehicle **p50 1 / p99 2, both modes** — with the
    spread visible: v2 puts ~8–16% of vehicles at lag 2 (9.7–18.9k of
    120k) vs TSIB's ~7–12% (8.8–14.4k), because v2's straddled streams
    complete a tick later while the batch makes the same drain; on those
    same straddle ticks v2's EARLY vehicles apply a tick earlier than
    the batch's uniform application. Deadline overruns ~4–8% (reported).
  The honest distributional statement: batch applies each tick's fleet
  UNIFORMLY at the complete-response tick; v2 SPREADS application across
  a straddle — a fraction of its vehicles applies a tick EARLIER than
  batch's uniform tick, its tail a tick later. Per-vehicle, neither mode
  dominates; batch's application is uniform and its p99 (the ADR's lag
  metric) is no worse than v2's (per-vehicle p99 2 = 2 at the straddling
  pace — the load-bearing assertion, batch per-vehicle p99 ≤ v2
  per-vehicle p99, exact, holds). What is claimed is exactly the
  percentiles and the observed max: max 1 at both production paces; at
  the straddling pace max 2 (TSIB) vs max 3 (v2 — its streaming tail
  straggles across a second boundary, 120–934 samples of 120k at lag 3;
  TSIB never showed lag ≥ 3 in any run). v2's
  early-application fraction under straddle is expected mechanics, NOT a
  regression: ADR-0026's concern was a systematic +1 shift of the whole
  fleet, which complete-response equality rules out.
  Live-e2e corroboration: `TestBatchAppliedLag` (RunLive, wall-paced;
  p50 1 / p99 1 both modes) is SMOKE ONLY — its passive-tap pairing
  carries a documented ±1 tick of uncertainty and a +1 assertion
  tolerance. The regression coverage for the ADR lag target is
  `TestBatchAppliedLagBoundary` above, full stop. Note the
  ingest-harness `lag_proxy_p50/lag_proxy_p99_ticks` benchmark metrics
  are 0 BY CONSTRUCTION (the harness drains at the publish tick) — they
  only pin the measurement path (demux/expansion adds no engine-side
  tick).

### Driver-side encode tail (ADR-0026 M3, `BenchmarkIntentEncode`)

The complete-delivery claim above also rests on the encode tail: batch
mode pays `EncodeTSIB` (O(n) fixed-section writes + one 24+44n B
allocation per ⌈n/20k⌉ batch) AFTER collection, while v2 encodes
incrementally (N × `EncodeIntent`, one 44 B alloc each, interleaved with
the compute). Measured on the M0 machine, near-idle box, one op = one
tick's encode (`go test -run '^$' -bench IntentEncode ./natsio/`):

| Vehicles | v2: N × EncodeIntent | TSIB: ⌈n/20k⌉ × EncodeTSIB | Ratio |
|---|---|---|---|
| 5,000 | ~97 µs, 5,000 allocs | ~43 µs, 1 alloc | ~0.44× |
| 15,000 | ~293–296 µs, 15,000 allocs | ~125–130 µs, 1 alloc | ~0.43× |
| 30,000 | ~581–586 µs, 30,000 allocs | ~250 µs, 2 allocs | ~0.43× |

The batch encode is ~0.43× of the v2 encode at every size and sub-ms
even at 30k — the post-collection encode tail does NOT trail the v2
incremental encode it replaces; with the delivered-rate gap in the M3
table (one message routed vs N), batch complete-delivery ≤ v2
complete-delivery for the same compute **at these scales** — a measured
fact inside the 5k–30k in-process envelope, not a universal claim.
- **Recorded batch-mode runs replay byte-identical — PROVEN in M2** by
  `TestBatchOnOffParity` (`engine/natsio/driver_test.go`): each leg
  (batch on and off) is re-simulated from its JetStream record and must
  reproduce the live run's CRC chain exactly, and does. Not re-measured
  here.
- **Batch vs v2 paired-seed agreement — PROVEN in M2** by the same test:
  paired-seed lanedrop legs under the established differential
  tolerances; the recorded run's macros were *identical* across modes
  (15 despawned, 21 lane changes, 21.55 m/s mean section-A speed, 0
  collisions, 21,457 driver intents each leg). Not re-measured here.

Caveats: same box-load sensitivity as the M0 table (±10–15% on means,
worse on p99 — the A/B/A/B interleave is the control); the 15k TSIB
delivered-rate range (23–35M records/s) is the widest sample, small-
denominator noise on a ~0.5 ms span. These are INGEST-side numbers:
driver-side per-tick encode differs by construction and is excluded on
both modes from the table above — it is measured SEPARATELY in the
`BenchmarkIntentEncode` subsection below (~0.43× in batch mode's favor),
covering the v2 driver's per-vehicle `EncodeIntent` per tick vs the TSIB
driver's per-tick `EncodeTSIB` per ⌈n/20k⌉ batch (header tick written
directly during the encode in production; the in-place patch is only the
ingest benchmark's payload-reuse trick).

## Consequence flags back to the KB

- The fleet-scale intent-ingest gap is **filled** (§(d), ADR-0026 M0):
  ~3.1M msgs/s in-process publish→buffer delivered rate; the complete
  controller-publish→drained path is **~1.5–1.6M intents/s** — the
  like-for-like M0↔M3 baseline (p50 ~18 ms / p99 ~22 ms per 30k tick).
  These are latency/throughput baselines, NOT engine tick-budget: the
  run loop never waits on delivery; what it pays is `DrainIntents`
  (~9 ms ≈ 9 % of the 100 ms tick at 30k) plus ~61 ns/msg `onIntent`
  callback CPU on the delivery goroutine. Revisit trigger for
  socketed/TLS deployments as with §(a).

- The ADR-0026 batched-intent change (M1–M3) **meets its M3 targets**
  (§(d) batch-mode table): intent traffic drops to O(controllers) per
  tick (exactly 4 vs 5k–30k), tick p99 improves ~45–55%, the complete
  path ~2–2.5×, and the applied-lag target is covered by
  `TestBatchAppliedLagBoundary` — per-vehicle p50/p99 ≤ v2 and
  complete-response equality at production paces (max observed 1), with
  the straddle-pace mechanics documented (TSIB max 2, v2 max 3 — its
  streaming tail straggles across a second boundary; early v2 vehicles
  can beat the uniform batch under straddle — expected mechanics, not a
  systematic shift), and
  `DrainIntents` is unchanged — it is now the dominant term of the
  ingest span (~85–90% at 30k, hold-last sort included), so further
  ingest work should target the drain, not the wire. Replay determinism
  and paired-seed batch/v2 agreement are test-pinned
  (`TestBatchOnOffParity`).

- The `arch-nats-backbone` open question "puback latency vs the 100 ms
  tick at high batch multipliers" is **answered for R=1 local file**:
  ~5 µs/msg amortized — not the constraint. Keep the revisit trigger for
  clustered/TLS/socketed deployments.
- The keyframe chunking scheme must be chosen before ≈ 13.6k vehicles
  (1 MB ÷ 77 B); snapshot chunking before ≈ 43k. Candidates remain
  app-level manifest chunking vs JetStream Object Store (synthesis §4).
