# ADR-0006: NATS message contract (planes, taxonomy, pipelines)

- **Status:** ACCEPTED
- **Date:** 2026-07-17 (design review, ratifying `arch-nats-backbone` research)

## Context

ADR-0002 chose NATS as the sole backbone but deferred subject taxonomy and
payload contracts. ADR-0005 fixed the replay machinery (arbitrated intent log +
keyframe snapshots + rolling CRC, all tick-stamped) that the contract must map
onto broker primitives. Research: `docs/kb/raw/arch-nats-backbone/` (planes,
streams, consumers, auth), amended by `arch-state-authority` (no deltas over
core NATS; hold-last + applied-tick echo) and `integration-maplibre-realtime`
(binary SoA vehicle frames). Evidence: NATS's three planes map ~1:1 onto our
needs; Synadia's Cybervet runs embedded NATS + 10 ms tick + KV + WebSocket at
4k players in production; EVE Online fans out ~10k msgs/s over NATS.

## Decision

1. **Three planes, no overlap.** Live state (snapshots ~10/s, dilation scalar,
   raw intents) rides **core NATS** (at-most-once, interest-gated). The
   arbitrated intent log, keyframes, and CRC ride **JetStream** (at-least-once,
   replayable). Scenario/world metadata, run registry, latest scalar for late
   joiners live in **KV** with `watch` as the change feed.
2. **Live messages are self-sufficient** (Tribes "most recent state" semantics).
   No delta-against-acked-baseline on core NATS — there are no per-subscriber
   acks; drops and slow-consumer disconnects are silent. Keyframe+delta exists
   only on the JetStream record plane, where consumer acks exist.
3. **Taxonomy `{ns}.{run}.{plane}.>{ids}`** — e.g. `ts.{run}.state.snap`,
   `ts.{run}.ctl.intent.{controller_id}`, `ts.{run}.log.intent|keyframe|crc`,
   `ts.{run}.metrics.>`. Namespace left, run id second (aggregate boundary),
   plane third, entity ids last; ≤16 tokens. **Tick numbers, schema versions,
   and wall-clock live in payload/headers — never in subjects, never inferred
   from stream sequence.**
4. **Intent pipeline: core-in, arbitrated-log-out, engine sole writer of
   record.** Controllers publish raw intents fire-and-forget; the engine
   buffers, batch-applies at tick boundaries (ADR-0005), then JetStream-
   publishes `(intent, applied_tick)` — the only client with publish rights on
   `...log.>`. Writes carry `Nats-Msg-Id: {run}:{tick}:{seq}` (retry-safe) and
   `Nats-Expected-Last-Sequence` (single-writer assertion). Pubacks awaited
   asynchronously per tick-batch; a failed log write aborts the run loudly.
5. **Keyframes and replay:** one log stream per run (LimitsPolicy, R=1 file
   locally) capturing `ts.{run}.log.>`; keyframes/CRCs are ordinary messages.
   Payloads stay under the default 1 MB `max_payload` via chunking or Object
   Store — never raise the server limit (ADR-0002 clarification). Seek =
   nearest keyframe ≤ target tick, then `DeliverByStartSequence` + re-sim; the
   replayer paces by tick, not `ReplayOriginal` (wall-clock).
6. **Slow consumers: tolerate, drop, resync — never block.** Default pending
   limits; `ErrSlowConsumer` and disconnects are normal events; subscribers
   tolerate dropped snapshots; late joiners resync from KV/last-per-subject,
   not backlog; metrics consumers scale via queue groups; client reconnect
   buffers capped.
7. **Live framing is binary SoA from day one** for vehicle state: ids +
   Float32 x/y/angle/class per frame (~8–16 B/vehicle; ~1–16 kB per
   100-vehicle window per tick). GeoJSON stays client-local. Never
   subject-per-vehicle. Envelopes carry `schema_version` and `tick`; the
   engine echoes `applied_tick` per controller as ack, latency meter, and HUD
   health signal.
8. **Deployment:** embedded in-process server for engine tests and
   single-binary demos (`DontListen` + in-process pipe); docker-compose server
   for dev/demos (ADR-0004); browsers over the server's WebSocket listener
   with binary frames.
9. **Contracts and tenancy:** one AsyncAPI 3.0 document is the source of truth
   for subjects/payloads/headers; TS models codegen'd, Go types hand-rolled or
   spec-generated. v1 auth: single account, per-user allow/deny — a controller
   publishes only `ts.*.ctl.intent.{own_id}`, observers read-only `ts.>`, only
   the engine user publishes `ts.*.log.>`. Account-per-tenant when public
   multiplayer needs it.

## Consequences

- Three consistency models for client authors, mitigated by the single
  AsyncAPI contract doc; contract changes remain ADR-gated per AGENTS.md.
- The tick depends asynchronously on broker persistence health; puback latency
  at faster-than-realtime batch rates is unmeasured — **benchmark before
  sizing batch multipliers** (tracked in `arch-nats-backbone` open questions).
- Per-run streams multiply broker assets; bounded by account `max_streams`.
- Embedded test mode skips real socket behavior (slow-consumer disconnects,
  TLS, auth) — integration tests still run against the container.
- Revisit triggers: puback-vs-tick-budget measurements; stream topology at
  scale (per-run vs single stream); nats.ws throughput for 10 Hz × N-vehicle
  snapshots; keyframe chunking scheme once the vehicle-count/byte curve is
  measured.
