# ADR-0002: NATS as the sole inter-service backbone

- **Status:** Accepted
- **Date:** 2026-07-14

## Context

The system needs: real-time state fan-out to many heterogeneous consumers (AI
controllers, human-driver clients, visualizers), durable event logs for deterministic
replay (the civic-advocacy use case), shared config/state, and the ability to let
many external participants join a live sim. These map directly onto core NATS
(pub/sub), JetStream (durable streams/replay), and NATS KV.

## Decision

All inter-service communication flows over NATS. No direct service-to-service RPC,
no second message broker, no shared database as an integration point.

- **Core NATS** — real-time state publishing and controller intents
- **JetStream** — durable event logs: scenario recordings, replay
- **KV** — shared configuration, scenario metadata, world metadata

## Consequences

- Every component is "just a NATS client" — humans, AIs, and observers plug in
  uniformly; multiplayer chaos demo is architecturally free.
- NATS subjects + payload schemas are the public API of the system; changes require
  an ADR (see AGENTS.md).
- Subject taxonomy, JetStream stream design, and slow-consumer strategy are open —
  see `arch-nats-backbone` research topic.
- Local dev requires a NATS server (docker-compose, or embedded per ADR-0001).

## Clarification (2026-07-17 review)

- "NATS" means the whole NATS family: core, JetStream, KV, Object Store. The
  real-time hot path is small messages only (~8–16 B/vehicle; a 100-vehicle
  observation window ≈ 1–16 kB per tick at 10 Hz — far under the default 1 MB
  max payload). Bulk artifacts — city-scale initial keyframes, compiled network
  packs, replay exports — ride JetStream Object Store or chunked,
  manifest-referenced messages, never core subjects.
- No in-process controller fast path: the engine contains zero driving logic;
  every vehicle is driven via the contract (see `concept-vehicle-controller-interface`
  review decisions, 2026-07-17). Engine-internal ops (tick gating on
  critical-controller health, pause/resume) are not contract exceptions.
