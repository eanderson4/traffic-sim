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
city-scale networks, as the synthesis predicted. Encode cost is linear and
negligible (63 µs at 10k = 0.06 % of the tick); snapshot size is a broker
bandwidth question, not a tick-budget question. Live frames at the
validated scenario scale (I-80 ≈ 300 vehicles → ≈ 7 kB/tick) are far under
any concern.

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

## Consequence flags back to the KB

- The `arch-nats-backbone` open question "puback latency vs the 100 ms
  tick at high batch multipliers" is **answered for R=1 local file**:
  ~5 µs/msg amortized — not the constraint. Keep the revisit trigger for
  clustered/TLS/socketed deployments.
- The keyframe chunking scheme must be chosen before ≈ 13.6k vehicles
  (1 MB ÷ 77 B); snapshot chunking before ≈ 43k. Candidates remain
  app-level manifest chunking vs JetStream Object Store (synthesis §4).
