# Prior Art Survey: NATS Backbone

> Source: web research | Researched: 2026-07-16
> "Competitors" here = (a) documented systems that already use NATS/JetStream
> for game-server or event-sourced workloads, (b) alternative backbones that
> could plausibly carry the same contracts, and (c) simulator-native client
> interfaces whose shape our NATS design replaces.

## NATS-backed realtime/game systems (the thin file)

### EventStack GamingAPI — JetStream as game-server event log (2022)
- The account [[arch-time-model]] flagged as the only documented one: uses
  JetStream "to have a log of events that happened in the game server, to
  enable server owners to pull those events as needed" — leaderboards, Discord
  bots, async command delivery ("commands... which the game server can consume
  once it is ready to do so")
  ([eventstack.tech](https://www.eventstack.tech/posts/nats-and-game-servers)).
- Multi-tenancy design: **one NATS account per game-server owner**, accounts
  as the security boundary ("two accounts cannot, unless explicitly stated,
  have access to each other's events"), per-account limits like
  `max_consumers`; JWT auth deferred ("haven't quite figured out how").
  Deployment: single docker-swarm NATS server to start, cluster later
  ([same](https://www.eventstack.tech/posts/nats-and-game-servers)).
- Chose NATS partly for **AsyncAPI code generation** in C# and TS; the author
  (Jonas Lagoni) is an AsyncAPI maintainer — the TS template grew JetStream
  fetch/pull/push-subscribe support
  ([AsyncAPI tooling update](https://www.eventstack.tech/posts/asyncapi-tooling-update-week-46)).
- Nothing on tick loops, snapshot cadence, or replay determinism — the log is
  for downstream consumers, not for re-simulation.
- **vs traffic-sim (us):** confirms the account-per-tenant and
  JetStream-as-game-event-log shapes in production, but our deterministic
  replay (arbitrated intent log + keyframes + CRC, per ADR-0005) is beyond
  anything this write-up attempts.

### Synadia Cybervet — embedded NATS, tick loop, KV game state (2023) — NEW
- The second documented account, found this research: an in-browser 1–2 player
  game built by Synadia staff "with game industry experience" to demo
  pub-sub + KV + streaming, "**which all run in the embedded game server**";
  "NATS is embedded within the game server binary and latency is almost
  non-existent" ([cybervet walkthrough](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md)).
- Division of labor, verbatim from their architecture: matchmaking = a
  **stream** (FIFO, discard-old, 30 m TTL) drained by a consumer loop holding
  two player slots; matches and players = **KV buckets** "serviced through the
  tick based game loop"; telemetry = **core NATS pub/sub** to a collector;
  browser client over **NATS WebSocket**; Prometheus metrics page rendered by a
  microservice subscribing to metrics subjects
  ([same](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md)).
- "**The game server, like all games, is based on a set of loops, governed by
  ticks. These ticks are spaced at 10 mS**"; actions span seconds so ticks
  rarely collide, letting one process carry thousands of players. Load tested
  pre-launch (SUSECon EU 23) with **4,000 scripted simultaneous players: CPU
  and memory "barely moved," no more than a 5% CPU increase**
  ([same](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md)).
- Single-binary deployment: Go server + assets + client bin-packed; NATS
  server, streams, and KV stores self-provisioned at startup.
- **vs us:** the closest existence proof for our whole shape — embedded
  broker, tick loop, KV for mutable game state, stream for queues, core NATS
  for fan-out and telemetry, browser over WebSocket. What it does NOT document:
  deterministic replay, snapshot keyframes, intent arbitration — our
  ADR-0005 machinery remains unwritten territory (see synthesis, Genuine Gap).

### JetStream-as-event-store practice (DDD/ES/CQRS community)
- NATS team answer to "Using Nats Jetstream as an event store": yes — subject
  per aggregate (`orders.1`, `orders.2`), optimistic concurrency via
  `Nats-Expected-Last-Sequence` / `Nats-Expected-Last-Subject-Sequence`
  headers, per-subject linearizability with total order across the stream;
  subject indexing makes OCC free and filtered replay scan only relevant
  blocks; CQRS read models = filtered consumers; tiered storage *not* built in
  (archive via a consumer instead)
  ([discussion #3772](https://github.com/nats-io/nats-server/discussions/3772)).
- "Increasing amount of interest from the DDD/ES/CQRS community" but no list
  of production event-sourcing users offered (2023); referenced community
  tooling (Rita) for snapshotting ([same](https://github.com/nats-io/nats-server/discussions/3772)).
- **vs us:** our intent log + keyframe snapshots (ADR-0005) is textbook event
  sourcing; the maintained OCC/index mechanics make per-run subjects the
  natural aggregate boundary. Snapshots live in-stream (rollup) rather than in
  a side archive.

## Alternative backbones (what ADR-0002 chose against)

### Apache Kafka — the default replay log
- Replay by offset, durable consumer groups, log compaction; at-most/at-least/
  exactly-once claimed ([compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
- No multi-tenancy ("not supported"), request-reply needs app-level
  correlation across topics, JVM-heavy sizing guidance (8 cores, 64–128 GB
  RAM, 10-Gig NIC) per the NATS docs' comparison ([same](https://docs.nats.io/nats-concepts/overview/compare-nats)).
- Independent vendor TCO analysis: core NATS ">1M messages per second" in
  their harness; positions NATS as covering "both the messaging and streaming
  needs in one solution" ([Synadia TCO](https://www.synadia.com/downloads/nats-kafka-tco-report.pdf)).
- **vs us:** Kafka could carry the intent log, but nothing like core NATS's
  per-connection slow-consumer disconnect for live fan-out, no embedded Go
  server, no KV/object primitives, and the ops footprint breaks local-first
  (ADR-0004). The division real-time-vs-log in one binary is exactly what we
  need and Kafka splits into Kafka+something.

### RabbitMQ — work-queue broker, no replay
- Queue semantics "vs log" mean **no message replay**; at-most/at-least-once
  only; vhosts give multi-tenancy without cross-vhost data sharing; Erlang VM
  runtime ([compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
- **vs us:** replay is a hard requirement (civic advocacy), so Rabbit is
  structurally out; JetStream's WorkQueuePolicy covers Rabbit's one strong
  pattern inside NATS anyway.

### Apache Pulsar / gRPC — calibrated and dismissed
- Pulsar: tenants with per-tenant authn/z, tiered storage, replay from
  position — but JVM, and its own docs-derived minimum is 6 machines (3
  ZK + 3 broker/Bookie) ([compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
  **vs us:** multi-tenancy is genuinely competitive; the ops footprint is not.
- gRPC: point-to-point, at-most-once, no broker to deploy but "always requires
  additional pieces for production"; 13 languages ([compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
  **vs us:** ADR-0002 already forbids service-to-service RPC; gRPC has no
  fan-out, no replay, no slow-consumer story for 100+ visualizers.

## Simulator-native interfaces (what our subject design replaces)

### SUMO TraCI — blocking TCP RPC barrier
- Per-step client barrier over sockets: "the simulation does not advance to
  the next step until all clients have called the 'simulationStep' command";
  measured **90 s vs 8 s** (11×) to retrieve positions on a 9,000-vehicle
  scenario; multi-client determinism via explicit `SetOrder` numbering
  ([TraCI docs](https://sumo.dlr.de/docs/TraCI/index.html), via [[arch-time-model]]).
- **vs us:** the canonical slow-consumer catastrophe: one stuck client stalls
  the world. Our engine never blocks; NATS drops/disconnects the stuck client
  instead (implementation §7). Deterministic multi-writer ordering moves from
  `SetOrder` to engine-side arbitration recorded in the intent log.

### MATSim events — in-JVM observer bus
- Event stream (LinkEnterEvent etc.) is the official observation interface,
  consumed by in-process EventHandlers or post-hoc XML parsing; parallel event
  handling keeps a per-step barrier
  ([EventHandler doxygen](https://www.matsim.org/doxygen/interfaceorg_1_1matsim_1_1core_1_1events_1_1handler_1_1_event_handler.html),
  [SimStepParallelEventsManagerImpl](https://www.matsim.org/apidocs/core/0.3.0/org/matsim/core/events/parallelEventsHandler/SimStepParallelEventsManagerImpl.html), via [[arch-time-model]]).
- **vs us:** MATSim proves events-as-observations is the right *content* model
  but keeps it in-process; we externalize the same stream onto subjects,
  gaining language-agnostic clients and JetStream persistence for free.

### CARLA — synchronous TCP client + server-side recorder
- Synchronous mode ties sim rate to the slowest client; recorder/replay is
  server-side state-log re-application with no re-execution determinism
  ([synchrony](https://carla.readthedocs.io/en/latest/adv_synchrony_timestep/),
  [recorder](https://carla.readthedocs.io/en/latest/adv_recorder/), via [[arch-time-model]]).
- **vs us:** the two failure modes we avoid: blocking on clients (we batch at
  tick boundaries) and state-log-only replay (we log arbitrated intents +
  keyframes and re-simulate, CRC-verified).

### Nakama (Go game server framework) — own transport, no broker
- Authoritative match loop with client messages "buffered... handed off as a
  batch" per tick; one goroutine owns match state; overflow messages may be
  dropped ([Heroic Labs docs](https://heroiclabs.com/docs/nakama/concepts/multiplayer/authoritative/), via [[arch-time-model]]).
  Transport is Nakama's own socket protocol, not a general-purpose broker.
- **vs us:** same buffer→batch-apply loop shape, but our "match state" spans
  runs, replays, and observers, which is why a durable broker with replay
  semantics sits under the loop instead of a bespoke socket protocol.

## Contract tooling ecosystem

### AsyncAPI — the OpenAPI of message-driven APIs
- Open standard for describing event-driven APIs machine-readably, with NATS
  protocol support among Kafka/AMQP/MQTT/WebSocket
  ([Docsio explainer](https://docsio.co/blog/asyncapi)); AsyncAPI 3.0
  recommended for new specs, with operations separated from channels.
- Codegen exists for TypeScript-NATS incl. JetStream pull/push/fetch and
  .NET-NATS with two serializer options; Modelina generates typed payload
  models ([AsyncAPI tooling update](https://www.eventstack.tech/posts/asyncapi-tooling-update-week-46)).
- **vs us:** ADR-0002 makes subjects+payloads our public API; AsyncAPI is the
  incumbent standard for writing that contract down once and generating the TS
  (viz) side of it. EventStack picked it for exactly this reason.

## Positioning Summary

| System | Live fan-out | Durable replay | QoS | Multi-tenancy | Embeddable in Go | Tick-loop aware |
|---|---|---|---|---|---|---|
| EventStack GamingAPI | core NATS | JetStream event log | at-least-once log | account per server owner | no (swarm) | no |
| Cybervet | core NATS + WS | matchmaking stream only | mixed | single tenant | **yes (in-process)** | **yes (10 ms loop)** |
| Kafka | consumer groups | offset replay + compaction | up to "exactly-once" | none | no | no |
| RabbitMQ | exchanges/queues | **none** | at-least-once | vhosts | no | no |
| Pulsar | subscriptions | position replay + tiers | up to "exactly-once" | tenants | no | no |
| gRPC | none (p2p) | none | at-most-once | n/a | n/a (library) | no |
| SUMO TraCI | sockets, barrier | re-run | — | — | — | blocking barrier |
| MATSim events | in-JVM handlers | XML re-parse | — | — | — | per-step barrier |
| CARLA | sync TCP | state log | — | — | — | blocks on client |
| **traffic-sim (us)** | core NATS subjects | JetStream intent log + keyframes + CRC | at-most-once live / at-least-once record | per-user perms v1, accounts later | **yes (tests/dev)** | **by construction (ADR-0005)** |
