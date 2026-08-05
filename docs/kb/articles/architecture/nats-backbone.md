# NATS Backbone

> How traffic-sim divides live fan-out, durable replay recording, and shared config across core NATS, JetStream, and KV — the three-plane contract ratified by ADR-0002/ADR-0006.

## Overview

NATS is the sole inter-service backbone: every controller, visualizer, metrics
consumer, and recorder is a NATS client, and no service-to-service RPC exists
([ADR-0002](../../decisions/ADR-0002-nats-backbone.md)). The research question
was how to map traffic-sim's three communication needs — ephemeral live state,
a durable replayable record, and shared configuration — onto NATS's three
primitives (core pub/sub, JetStream streams, KV), and how to shape subjects so
the [Time Model](time-model.md)'s replay machinery (arbitrated intent log +
keyframes + CRC, all tick-stamped) lands cleanly on broker primitives.

The conclusion: the mapping is nearly 1:1, and the event-sourcing community has
already validated the pattern on JetStream (subject-per-aggregate, header-based
optimistic concurrency, filtered consumers as read models). The closest prior
art is Synadia's Cybervet — an embedded NATS game server with a 10 ms tick loop,
KV game state, and a browser client over WebSocket that carried 4,000 players —
which proves the whole shape except deterministic replay, which remains
unpublished anywhere. ADR-0006 ratified the recommended contract: three planes,
a `{ns}.{run}.{plane}.>` taxonomy, binary SoA vehicle frames on the wire, the
engine as sole writer of the record plane, and AsyncAPI 3.0 as the contract
source of truth.

The 2026-07-17 design review added two hardening constraints to ADR-0002: only
small messages ride the hot path (large artifacts move by reference or chunking,
never by raising `max_payload`), and there is no in-process controller fast
path — even a co-located controller speaks NATS, keeping one code path and one
permission model.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Live plane (core NATS) | `raw/arch-nats-backbone/implementation.md` §2 | Ephemeral 10 Hz state snapshots + raw intents, at-most-once |
| Record plane (JetStream) | [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md); implementation.md §3 | Per-run stream: arbitrated intent log + keyframes + CRC |
| Config plane (KV) | implementation.md §2 | Scenario/run metadata, latest state for late-joiner resync, `watch` change feed |
| Subject taxonomy | [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md); implementation.md §1 | `{ns}.{run}.{plane}.>{ids}` — namespace left, identifiers right |
| Intent pipeline | synthesis.md §3; implementation.md §5 | Core-in, arbitrated-log-out; engine sole writer of `log.>` |
| Replay seek machinery | implementation.md §4 | Keyframe scan + `DeliverByStartSequence`, paced by tick |
| Slow-consumer stance | implementation.md §7 | Tolerate, drop, resync — never block the tick |
| Deployment modes | implementation.md §9 | Embedded for tests, docker-compose for topology, WebSocket for browsers |
| Contract & tenancy | [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md); implementation.md §10 | AsyncAPI 3.0 source of truth; per-user permissions now, accounts later |
| Prior art survey | `raw/arch-nats-backbone/competitors.md` | Cybervet, EventStack, Kafka/Rabbit/Pulsar, TraCI/MATSim/CARLA |

## How It Works

### Three planes, no overlap (ADR-0002, ADR-0006)

- **Live plane — core NATS.** State snapshots (~10 Hz), the time-dilation
  scalar, and raw controller intents. Fire-and-forget, at-most-once,
  interest-gated: messages with no subscribers are discarded and slow consumers
  are dropped — correct semantics for ephemeral latest-state. Vehicle frames
  are binary SoA, ~8–16 B per vehicle, honoring the "small messages only on the
  hot path" clarification.
- **Record plane — JetStream.** One `LimitsPolicy` stream per run (R=1 file
  storage locally) captures `ts.{run}.log.>`: arbitrated intents, snapshot
  keyframes, and rolling state CRCs as ordinary messages in one ordered record.
  At-least-once, replayable, seekable.
- **Config plane — KV.** Scenario/world metadata, the run registry, and the
  latest dilation scalar for late joiners; `watch` is the change feed, CAS
  (`create`/`update` by revision) handles config mutation.

Each primitive's documented QoS matches its assignment; Cybervet's production
split is identical (stream for queues, KV for match state, core NATS for
telemetry). The cost is three consistency models for client authors, mitigated
by the single AsyncAPI contract document.

### Subject taxonomy (ADR-0006)

`ts.{run_id}.state.snap`, `ts.{run_id}.ctl.intent.{controller_id}`,
`ts.{run_id}.log.intents|intent|keyframe|crc`, `ts.{run_id}.metrics.>` — namespace
first, run id second (the aggregate boundary), plane third, entity identifiers
last. Official guidance: ≤16 tokens, <256 chars, identifiers last, metadata in
headers. Run-as-second-token makes every wildcard and permission rule one token
wide (`ts.run42.state.>`, `ts.*.ctl.intent.{own_id}`). Subject scale is
sanctioned explicitly: 10s of millions of subjects are fine; >1M *subscriptions*
is the real memory line (>1 GB server RAM). **Tick numbers, schema versions,
and wall-clock live in payload/headers — never in subjects, never inferred from
stream sequence** (sequences are per-stream storage positions).

### Intent pipeline (sequential)

1. Controllers publish raw intents fire-and-forget on
   `ts.{run}.ctl.intent.{controller_id}` (core NATS).
2. The engine buffers and batch-applies them at tick boundaries
   ([ADR-0005](../../decisions/ADR-0005-time-model.md)); the tick never blocks.
3. The engine JetStream-publishes the arbitrated `(intent, applied_tick)`
   entries — and is the *only* client with publish permission on `ts.*.log.>`.
4. Log writes carry `Nats-Msg-Id: {run}:{tick}:{seq}` (idempotent retries,
   2-minute dedup window) and `Nats-Expected-Last-Sequence` (OCC — asserts the
   single-writer invariant broker-side at no extra cost; subjects are indexed).
5. Pubacks are awaited asynchronously per tick-batch; a failed log write aborts
   the run loudly rather than corrupting the record.

### Keyframes and replay

Seek = scan the sparse keyframe subject (or a side `tick → seq` index) for the
nearest keyframe ≤ target tick, then `DeliverByStartSequence` from there and
re-simulate over the intent log, CRC-verified. Snapshot payloads stay under the
default 1 MB `max_payload` by chunking (manifest subjects) or Object Store
(≈128 KiB chunks, SHA-256; a 1.5 GiB blob round-trips at 279 MiB/s) — the server
limit is never raised. Keyframes stay plain messages rather than `Nats-Rollup`
replacements to preserve the single ordered record.

### Slow consumers, durability, deployment, tenancy (parallel concerns)

- **Slow consumers:** keep default pending limits (65,536 msgs / 64 MiB per
  subscription) and server `write_deadline`; drops and disconnects are normal
  events. Controllers tolerate dropped snapshots (latest-state semantics);
  late joiners resync from KV / `DeliverLastPerSubject`, never from backlog.
  Metrics consumers scale via queue groups. Buffer tuning is the documented
  last resort.
- **Performance anchors:** core NATS benchmarked at 8.4M msgs/s (10 publishers,
  16 B messages) and >1M msgs/s in an independent harness; loopback RTT
  ~65–70 µs — three orders of magnitude under the 100 ms tick budget. Cybervet
  ran a 10 ms tick over embedded NATS with 4,000 players at <5% CPU delta.
  TraCI's blocking barrier (90 s vs 8 s — 11× — on a 9,000-vehicle scenario) is
  the failure mode this stance avoids.
- **Deployment:** engine tests and single-binary demos run `nats-server`
  in-process (`DontListen: true` + `nats.InProcessServer` over `net.Pipe`, no
  TCP); development runs the docker-compose server
  ([ADR-0004](../../decisions/ADR-0004-local-first.md)); the TS viz client uses
  the server's native WebSocket listener (binary frames). Per the 2026-07-17
  ADR-0002 clarification, co-located controllers still go over the bus — the
  embedded server is a test convenience, not a controller fast path.
- **Contract and tenancy:** one AsyncAPI 3.0 document declares subjects,
  payloads, and headers; TS models are codegen'd from it (Go types hand-rolled
  or spec-generated — Go codegen is less mature). Envelopes carry
  `schema_version` and `tick`. v1 auth is a single account with per-user
  allow/deny (controller publishes only `ts.*.ctl.intent.{own_id}`; observers
  read-only `ts.>`; only the engine publishes `ts.*.log.>`); public multiplayer
  upgrades to account-per-tenant with curated exports (EventStack pattern).

### The genuine gap

Nobody has published a NATS-backed authoritative simulation with deterministic
replay: tick-number-based replay pacing (vs broker `ReplayOriginal` wall-clock),
keyframe chunking practice on JetStream, puback latency inside a fixed tick
budget at faster-than-realtime rates, and CRC-verified re-simulation from a
stream are all unwritten. traffic-sim's benchmarks and contract write-up are at
the frontier.

## Gotchas

- **`ReplayOriginal` paces by wall-clock arrival, push-consumers only**: fine
  for re-watching a 1× realtime run, wrong for batch-recorded or time-dilated
  runs — the replayer must pace by tick itself.
- **Acknowledged ≠ machine-crash durable**: file streams fsync on
  `sync_interval` (default 2 minutes), so a single-node R=1 deployment can lose
  acknowledged intent-log writes in an OS crash inside the window.
  `sync_interval: always` exists at a throughput cost.
- **Exactly-once is impossible in principle** (Two Generals/FLP): compose it
  from `Nats-Msg-Id` dedup + OCC headers + idempotent application — and don't
  lean on dedup beyond its 2-minute window.
- **High-rate record writes must use JetStream publish with acks**: fire-and-
  forget core publishes into a stream trip the 2.11 ingest limiter (default
  128 MB / 10,000 msgs buffered, then 429 + drop).
- **Stream sequence is not sim time**: sequences are per-stream storage
  positions; ADR-0005's tick must ride in payload/headers, always.
- **Bigger buffers don't fix slow consumers**: the docs are blunt — they "will
  only postpone slow consumer problems." Scale out (queue groups), shard the
  namespace, or speed up the consumer.
- **KV has no read-your-writes via direct-get** (followers may answer): use
  leader reads or `watch` for the strong variant.
- **Embedded mode skips real socket behavior** (slow-consumer disconnects, TLS,
  auth): integration tests still need the compose container.
- **Storage overhead punishes long subjects**: a 5-byte payload stores as ~39
  bytes on disk (length + seq + timestamp + subject + hash) — short subjects
  pay off at high message rates.

## Open Questions

- JetStream puback latency (R=1, file, default `sync_interval`) vs the 100 ms
  tick at high faster-than-realtime batch multipliers — no prior art; benchmark
  at engine bring-up (flagged identically by the time-model research).
- Keyframe size as networks grow: when full-state snapshots approach ~1 MB,
  choose the chunking scheme (app-level manifest vs Object Store) — needs a
  measured vehicle-count/byte curve (depends on the road graph model).
- Stream topology at scale: per-run streams vs one stream with per-run
  subjects — consumer-count and asset-lifecycle costs are unpublished.
- nats.ws (browser) throughput/latency vs TCP for the 10 Hz × N-vehicle
  snapshot feed to the MapLibre client; whether headers-only consumers suffice
  for viz heatmaps.
- `Nats-Rollup` interaction with consumer ack floors (undocumented corner) —
  test before ever using rollup on the keyframe subject.
- Auth callout / `nsc` JWT operator mode: deferred until public multiplayer
  tenancy requires it.

## Related

- [Time Model](../architecture/time-model.md) — the tick, intent buffering, and keyframe+CRC replay machinery the three planes carry; tick-in-payload is a joint invariant.
- [State Authority](../architecture/state-authority.md) — the engine's single-writer goroutine is the sole `log.>` publisher; OCC asserts it broker-side.
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — intent envelope (`{run}:{tick}:{seq}` idempotency key, drop-tolerant state subscription) defined on these subjects.
- [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) — browser client over WebSocket binary frames, resyncing from KV, fed by the 10 Hz live plane.
- [Congestion Metrics](../business-domains/congestion-metrics.md) — metrics consumers ride `ts.{run}.metrics.>` and scale horizontally via queue groups.
- [Simulator Landscape](../business-domains/simulator-landscape.md) — TraCI/MATSim/CARLA interface shapes are the blocking-barrier designs this backbone replaces.

---
*Raw research: [raw/arch-nats-backbone/](../../raw/arch-nats-backbone/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
