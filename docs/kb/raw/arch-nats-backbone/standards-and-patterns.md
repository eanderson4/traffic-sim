# Standards & Patterns: NATS Backbone

> Source: academic research + pattern identification | Researched: 2026-07-16

## Formalisms

### Delivery-semantics theory (why "exactly once" is a composition)
- Three semantics exist; the third is impossible in a distributed system per
  the Two Generals Problem and the FLP impossibility result — "FLP and the
  Two Generals Problem are not design complexities, they are *impossibility
  results*" ([Treat, bravenewgeek](https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/)).
- The practical escape: **at-least-once + idempotency or dedup**, mirroring
  Zab's replicated-state-machine design ("guaranteeing at-least once semantics
  is sufficient" when state changes are idempotent and application order
  matches delivery order) ([same](https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/)).
  Design intents so redelivery is harmless: commutative/fact-style messages
  beat side-effecting commands.
- JetStream instantiates exactly this recipe: dedup via `Nats-Msg-Id` on
  publish + double-ack (`AckSync`) on consume
  ([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).

### Event sourcing + snapshots (the replay formalism)
- Fowler: rebuild state by replaying events; snapshots are the standard
  optimization ([Event Sourcing](https://martinfowler.com/eaaDev/EventSourcing.html)).
  JetStream's documented event-sourcing recipe: subject per aggregate,
  `Nats-Expected-Last-Subject-Sequence` for optimistic concurrency, filtered
  consumers as CQRS read models
  ([discussion #3772](https://github.com/nats-io/nats-server/discussions/3772)).
- Our mapping (per ADR-0005): run = aggregate, arbitrated intent log = event
  stream, keyframes = snapshots, state CRC = the verification Fowler leaves to
  the application.

### Consistency models actually guaranteed
- JetStream writes are **linearizable** (RAFT quorum); stream order is a
  single global order, controllable by compare-and-publish — "in essence,
  serializable" ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
- KV: immediate consistency for monotonic reads/writes; **no read-your-writes**
  through direct-get (followers may answer) — leader reads for the strong
  variant ([KV store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)).
- Core NATS: no ordering guarantee across publishers, no delivery guarantee —
  at-most-once, interest-gated ([subjects](https://docs.nats.io/nats-concepts/subjects)).

## Standards and contract artifacts

- **NATS client protocol**: text-based (INFO/CONNECT/PING/PONG/SUB/PUB),
  documented for client authors; headers and `no_responders` are negotiated in
  CONNECT ([NATS clients](https://docs.nats.io/running-a-nats-service/clients)).
- **JetStream wire API**: streams/consumers/KV are administered over ordinary
  subjects under `$JS.API.*` — the management plane is NATS itself; strict
  request validation default-on since 2.12
  ([2.12 notes](https://docs.nats.io/release_notes/whats_new_212)).
- **NATS's own ADR practice**: design docs live in
  [nats-architecture-and-design](https://github.com/nats-io/nats-architecture-and-design)
  (e.g. ADR-31 batched direct-get, ADR-41 tracing, ADR-43 per-msg TTL, ADR-49
  counters, ADR-50 atomic publish, ADR-51 schedules) — cited throughout the
  [headers reference](https://docs.nats.io/nats-concepts/jetstream/headers).
  Precedent for our own ADR discipline.
- **AsyncAPI**: the open standard for machine-readable event-driven API
  contracts; NATS among its protocol bindings; codegen to TS/.NET NATS clients
  incl. JetStream ([Docsio](https://docsio.co/blog/asyncapi),
  [tooling update](https://www.eventstack.tech/posts/asyncapi-tooling-update-week-46)).
- **Reserved namespaces**: `$`-prefixed subjects are system ($SYS, $JS, $KV);
  `_INBOX.` is the reply convention; `Nats-` is the reserved header prefix —
  application taxonomies must stay clear of all three
  ([subjects](https://docs.nats.io/nats-concepts/subjects),
  [headers](https://docs.nats.io/nats-concepts/jetstream/headers)).

## Design patterns identified

### Subject taxonomy conventions (from official best practices)
Namespace-first tokens, identifier-last tokens; ≤16 tokens/<256 chars; encode
business intent not technical details; metadata in headers; wildcard-friendly
(put the dimension you'll shard/subscribe on left of the id)
([subjects](https://docs.nats.io/nats-concepts/subjects)).
Sharding escape hatch that leaves publishers untouched: deepen the namespace
(`Sensors.North`...), swap one wildcard consumer for N concrete ones
([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).

### One publish, two planes
A stream binds live subjects; a single publish both fans out to connected
subscribers (at-most-once) and is persisted (at-least-once). The live path
never waits on storage ([streams](https://docs.nats.io/nats-concepts/jetstream/streams)).
This is the mechanism that lets a state snapshot serve realtime clients and
become a replay keyframe without a second write path.

### Snapshot rollup
`Nats-Rollup: sub|all` replaces history with the latest message — documented
for state snapshots ([streams](https://docs.nats.io/nats-concepts/jetstream/streams)).
Alternative: KV with history=1 as a self-rolling latest-state store with
watch() as the change feed ([KV store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)).

### Materialized-view catch-up
`DeliverLastPerSubject` consumer = "latest value per key" replay for late
joiners ([consumers](https://docs.nats.io/nats-concepts/jetstream/consumers));
KV `get` + `watch` is the KV-shaped equivalent
([KV store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)).

### Queue-group scaling (core) and shared pull consumers (JetStream)
Core: random one-of-group delivery, zero config, drain for lossless scale-down
([queue groups](https://docs.nats.io/nats-concepts/core-nats/queue)).
JetStream: pull consumers shared across app instances "just like queue
groups," no partition management ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).

### Dead-letter pile
WorkQueue stream + `MaxDeliver`: exhausted messages stay in the stream for
manual/API removal instead of vanishing
([streams](https://docs.nats.io/nats-concepts/jetstream/streams)).

### Drain-before-close
Drain connections/subscriptions to process inflight + pending before closing;
required for lossless queue-group member replacement
([drain](https://docs.nats.io/using-nats/developing-with-nats/receiving/drain)).

### Account-per-tenant with curated exports
Isolate tenants into accounts; share deliberately via stream/service
export/import (optionally per-account-scoped, with prefix remap); JetStream
cross-account sharing prefers mirror/source over direct foreign access
([accounts](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/accounts)).
Production precedent: EventStack's account-per-game-owner
([eventstack.tech](https://www.eventstack.tech/posts/nats-and-game-servers)).

### Embedded-for-tests, container-for-topology
In-process server over `net.Pipe` (`DontListen` + `InProcessServer`) for unit
tests and single-binary demos; docker-compose for the real multi-process
topology ([gosuda](https://gosuda.org/blog/posts/how-embedded-nats-communicate-with-go-application-z36089af0),
[clients](https://docs.nats.io/running-a-nats-service/clients)).
Cybervet ships this as a single self-provisioning binary
([cybervet](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md)).

## Anti-patterns (documented failures)

1. **Core-NATS publish into a stream without waiting for pubacks at high
   rate** — trips the 2.11 ingest limiter (429 `JSStreamTooManyRequests`;
   default buffers 128 MB / 10k msgs) and drops messages. Writers of record
   use JetStream publish with acks ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211)).
2. **Enlarging buffers to "fix" slow consumers** — "will only postpone slow
   consumer problems"; the fix is scale-out, namespace sharding, or a faster
   consumer ([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).
3. **InterestPolicy stream with no pre-created consumers** — messages are
   deleted on arrival for lack of interest ([streams](https://docs.nats.io/nats-concepts/jetstream/streams)).
4. **Overlapping consumer filters on a WorkQueue stream** — rejected by
   design (one consumer per subject); pick LimitsPolicy for multi-reader
   replay ([streams](https://docs.nats.io/nats-concepts/jetstream/streams)).
5. **Dedup as correctness crutch** — the `Nats-Msg-Id` window is 2 minutes by
   default and docs "caution against large windows"; long-horizon idempotency
   must live in the application ([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).
6. **Encoding payload data into subject tokens** — docs warn against
   over-loaded subjects ("maybe not so useful" 9-token example); use headers
   ([subjects](https://docs.nats.io/nats-concepts/subjects)).
7. **Relying on stream sequence as sim time** — sequences are per-stream
   storage positions, not ticks; ADR-0005's tick number must ride in
   payload/headers ([headers](https://docs.nats.io/nats-concepts/jetstream/headers)).
8. **Assuming `fsync` per puback** — default `sync_interval` is 2 minutes; an
   OS crash can lose acknowledged writes on a single node
   ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
9. **Expecting exactly-once from the broker** — impossible in principle
   (Two Generals/FLP); compose it from dedup + double-ack + idempotent
   handlers ([Treat](https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/)).
10. **Blocking fan-out on the slowest consumer** (the TraCI disease ported to
    a broker) — NATS deliberately disconnects slow consumers instead; design
    clients to tolerate it and resync ([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).

## Empirical anchors

- Message size: `max_payload` 1 MB default / 8 MB recommended cap / 64 MB hard
  max ([pub/sub](https://docs.nats.io/nats-concepts/core-nats/pubsub)).
- Subscription pending: 65,536 msgs / 64 MiB per sub (client), then drop
  ([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).
- Dedup window: 2 min default; storage record overhead 39 B for a 5-byte
  payload ([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).
- `MaxAckPending` default 1000; `MaxDeliver` default −1 (forever)
  ([consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)).
- JetStream ingest buffers 128 MB / 10,000 msgs per stream (2.11); JS API
  queue 10K requests ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211),
  [resource mgmt](https://docs.nats.io/running-a-nats-service/configuration/jetstream-config/resource_management)).
- `sync_interval` default 2 min; replicas 3 recommended, 5 max
  ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
- Object Store chunks ≈128 KiB with SHA-256 digest; 1.5 GiB read at 279 MiB/s
  ([obj walkthrough](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_walkthrough)).
- Throughput: 8.4M msgs/s (10 pubs, 16 B) conference bench; >1M msgs/s in a
  vendor TCO harness; loopback RTT ~65–70 µs
  ([OSCON deck](https://conferences.oreilly.com/oscon/oscon-or-2019/cdn.oreillystatic.com/en/assets/1/event/295/Simple, secure, and reliable_ Building cloud native applications with NATS Presentation.pdf),
  [Synadia TCO](https://www.synadia.com/downloads/nats-kafka-tco-report.pdf),
  [obj walkthrough](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_walkthrough)).
- Subject scale: 10s of millions of subjects OK; >1M subscriptions ≈ >1 GB
  server RAM ([subjects](https://docs.nats.io/nats-concepts/subjects)).

## Open Questions

- JetStream puback latency distribution (R=1 file stream, `sync_interval`
  default) vs the 100 ms tick at faster-than-realtime multipliers — no prior
  art; benchmark during engine bring-up (flagged identically by
  [[arch-time-model]]).
- `ReplayOriginal` pacing is wall-clock-arrival-based; behavior when a run was
  recorded under time dilation or batch speed is undocumented — verify whether
  broker-side pacing is usable for anything but 1× realtime replays.
- Cost of hundreds of filtered consumers on one intent-log stream (per-run
  replays + live metrics + recorders) — no published scaling curve.
- WebSocket client (nats.ws) throughput/latency vs TCP for the MapLibre
  snapshot feed at 10 Hz × N vehicles — measure; consider headers-only
  conflation.
- Whether `Nats-Rollup` on a keyframe subject interacts with filtered
  consumers' ack floors (undocumented corner) — test before relying on rollup
  for long-lived keyframe streams.

## Master source list

NATS concepts: [subjects](https://docs.nats.io/nats-concepts/subjects) ·
[pub/sub](https://docs.nats.io/nats-concepts/core-nats/pubsub) ·
[queue groups](https://docs.nats.io/nats-concepts/core-nats/queue) ·
[JetStream overview](https://docs.nats.io/nats-concepts/jetstream) ·
[streams](https://docs.nats.io/nats-concepts/jetstream/streams) ·
[consumers](https://docs.nats.io/nats-concepts/jetstream/consumers) ·
[KV](https://docs.nats.io/nats-concepts/jetstream/key-value-store) ·
[object store](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_store) /
[walkthrough](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_walkthrough) ·
[headers](https://docs.nats.io/nats-concepts/jetstream/headers) ·
[model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive) ·
[compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats) —
Ops: [slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers) ·
[drain](https://docs.nats.io/using-nats/developing-with-nats/receiving/drain) ·
[reconnect buffer](https://docs.nats.io/using-nats/developer/connecting/reconnect/buffer) ·
[JetStream resource mgmt](https://docs.nats.io/running-a-nats-service/configuration/jetstream-config/resource_management) ·
[websocket](https://docs.nats.io/running-a-nats-service/configuration/websocket) —
Security: [accounts](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/accounts) ·
[authorization](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/authorization) —
Releases: [2.11](https://docs.nats.io/release_notes/whats_new_211) ·
[2.12](https://docs.nats.io/release_notes/whats_new_212) —
Embedding: [gosuda](https://gosuda.org/blog/posts/how-embedded-nats-communicate-with-go-application-z36089af0) ·
[clients](https://docs.nats.io/running-a-nats-service/clients) ·
[pkg.go.dev server](https://pkg.go.dev/github.com/nats-io/nats-server/v2/server) —
Prior art: [EventStack GamingAPI](https://www.eventstack.tech/posts/nats-and-game-servers) ·
[Cybervet](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md) ·
[event-store discussion #3772](https://github.com/nats-io/nats-server/discussions/3772) ·
[AsyncAPI tooling](https://www.eventstack.tech/posts/asyncapi-tooling-update-week-46) —
Theory: [Treat, exactly-once](https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/) ·
[Fowler, event sourcing](https://martinfowler.com/eaaDev/EventSourcing.html) —
Numbers: [OSCON 2019 deck](https://conferences.oreilly.com/oscon/oscon-or-2019/cdn.oreillystatic.com/en/assets/1/event/295/Simple, secure, and reliable_ Building cloud native applications with NATS Presentation.pdf) ·
[Synadia TCO](https://www.synadia.com/downloads/nats-kafka-tco-report.pdf) —
Sim interfaces: [SUMO TraCI](https://sumo.dlr.de/docs/TraCI/index.html) ·
[MATSim EventHandler](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1events_1_1handler_1_1_event_handler.html) ·
[CARLA synchrony](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/) ·
[Nakama](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/)
