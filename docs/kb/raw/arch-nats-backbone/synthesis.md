# Synthesis: NATS Backbone

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds ADR-0006 (message contracts / subject taxonomy — not yet proposed).
> This synthesis recommends; the ADR decides.

## Summary

The research question was how to divide live fan-out, durable recording, and
shared state across core NATS / JetStream / KV, and how to shape subjects so
ADR-0005's replay machinery (arbitrated intent log + keyframes + CRC, all
tick-stamped) maps onto broker primitives. The answer from the primary docs:
NATS's three planes map almost 1:1 onto our three needs, and the event-sourcing
community has already validated the exact pattern (subject-per-aggregate,
header-based optimistic concurrency, filtered consumers as read models). The
surprise finding is a *second* documented NATS game server — Synadia's
Cybervet — which runs an embedded broker, a 10 ms tick loop, KV game state,
and a browser client over WebSocket at 4,000 players, proving the shape
end-to-end except for the deterministic-replay machinery, which remains
unwritten anywhere.

## Source Files

- [Mechanics: subjects, streams, consumers, flow control, security, deployment](./implementation.md)
- [Prior art survey: NATS game systems, alternative brokers, simulator interfaces](./competitors.md)
- [Standards, formalisms, named patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions (for ADR-0006)

### 1. Three planes, no overlap: core for live, JetStream for the record, KV for config/latest
**Choice:** Live state snapshots (~10/s), time-dilation scalar, and raw
controller intents ride **core NATS** (at-most-once, interest-gated). The
arbitrated intent log, snapshot keyframes, and rolling state CRC ride
**JetStream** streams (at-least-once, replayable). Scenario/world metadata,
run registry, and the latest dilation scalar for late joiners live in **KV**
buckets with `watch` as the change feed.
**Why:** Matches each primitive's documented QoS: core NATS discards messages
with no subscribers and drops for slow consumers — correct for ephemeral live
state; streams exist precisely for "replay messages on demand, as many times
as you want"; KV is a stream with rollup semantics plus atomic CAS for config
([JetStream overview](https://docs.nats.io/nats-concepts/jetstream),
[KV](https://docs.nats.io/nats-concepts/jetstream/key-value-store)).
Cybervet's production split is identical: stream for queues, KV for match
state, core NATS for telemetry
([cybervet](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md)).
**Trade-off:** Three consistency models for client authors to learn; mitigated
by a single contract doc (decision 7).
**Field context:** [implementation §2](./implementation.md).

### 2. Taxonomy: `{ns}.{run}.{plane}.>{...identifiers}` — namespace left, ids right, tick in payload
**Choice:** A shape like `ts.{run_id}.state.snap`, `ts.{run_id}.ctl.intent.
{controller_id}`, `ts.{run_id}.log.intent|keyframe|crc`, `ts.{run_id}.metrics.
>`: first token the app namespace, run id second (the aggregate boundary),
plane/class third, entity identifiers last. **Tick numbers, schema versions,
and wall-clock live in payload/headers — never in subjects, never inferred
from stream sequence.**
**Why:** Official guidance verbatim: namespace-first tokens, identifiers last,
"encode business intent into the subject, not technical details," metadata in
headers, ≤16 tokens/<256 chars ([subjects](https://docs.nats.io/nats-concepts/subjects)).
Run-as-second-token makes every wildcard and permission rule one token wide
(`ts.run42.state.>`, `ts.*.ctl.intent.>`), and matches the event-sourcing
practice of subject-per-aggregate with per-subject OCC and indexed filtered
replay ([discussion #3772](https://github.com/nats-io/nats-server/discussions/3772)).
Stream sequences are per-stream storage positions — ADR-0005's tick must ride
inside the message ([headers](https://docs.nats.io/nats-concepts/jetstream/headers)).
**Trade-off:** A run-id token per subject multiplies subject count; sanctioned
explicitly ("10s of millions of subjects"; >1M *subscriptions* is the real
memory line) ([subjects](https://docs.nats.io/nats-concepts/subjects)).
**Field context:** [implementation §1](./implementation.md),
[standards §patterns](./standards-and-patterns.md).

### 3. Intent pipeline: core-in, arbitrated-log-out, engine as sole writer of record
**Choice:** Controllers publish raw intents on core subjects
(`...ctl.intent.{controller_id}`, fire-and-forget). The engine buffers, applies
at tick boundaries (ADR-0005), then JetStream-publishes the arbitrated
`(intent, applied_tick)` entries — and is the *only* client with publish
permission on `...log.>`. Log writes carry `Nats-Msg-Id: {run}:{tick}:{seq}`
(retry-safe) and `Nats-Expected-Last-Sequence` (asserts single-writer).
Pubacks are awaited asynchronously per tick-batch; a failed log write aborts
the run loudly rather than corrupting the record.
**Why:** The dedup header makes writes idempotent (2-min window) and OCC gives
linearizable per-subject append order with a total stream order
([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive),
[discussion #3772](https://github.com/nats-io/nats-server/discussions/3772)).
2.11's ingest limiter is explicit that high-rate writers must use JS publish
with acks ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211)).
Exactly-once is impossible in principle — compose it from dedup + OCC +
idempotent application ([Treat](https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/)).
**Trade-off:** The tick now depends (asynchronously) on broker persistence
health; puback latency at faster-than-realtime rates is unmeasured anywhere —
benchmark before sizing batch multipliers. A single-node file stream only
fsyncs every 2 min by default, so machine-crash durability of acknowledged
intents has a window ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
**Field context:** [implementation §5, §8](./implementation.md).

### 4. Keyframes and replay: per-run log stream, seek by sequence, pace by tick
**Choice:** One log stream per run (`LimitsPolicy`, R=1 file storage locally),
created at run start, capturing `ts.{run}.log.>`; keyframes and CRCs are
ordinary messages in it. Snapshot payloads stay under the default 1 MB
`max_payload` by chunking (per-region/delta manifests) or Object Store (128 KiB
chunks, SHA-256) — do not raise the server limit. Seek = scan the sparse
keyframe subject (or a side index of `tick → seq`) for the nearest keyframe ≤
target tick, then `DeliverByStartSequence` from there and re-simulate with the
intent log.
**Why:** Streams bind wildcard subjects and retention bounds seek depth —
exactly the ADR-0005 requirement; rollup (`Nats-Rollup`) exists for
snapshot-style replacement but keeping keyframes as plain messages preserves
the single ordered record ([streams](https://docs.nats.io/nats-concepts/jetstream/streams),
[headers](https://docs.nats.io/nats-concepts/jetstream/headers)).
**Trade-off:** `ReplayOriginal` pacing replays at *original wall-clock arrival
rate* and is push-consumer-only — correct for re-watching a 1× realtime run,
wrong for batch-recorded or dilated runs; our replayer must pace by tick
itself ([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).
Per-run streams multiply assets; bounded by per-account `max_streams` limits
([resource mgmt](https://docs.nats.io/running-a-nats-service/configuration/jetstream-config/resource_management)).
**Field context:** [implementation §4, §6](./implementation.md).

### 5. Slow-consumer stance: tolerate, drop, resync — never block, never balloon buffers
**Choice:** Keep default pending limits (65,536 msgs / 64 MiB) and server
`write_deadline`; treat `ErrSlowConsumer` and disconnects as normal events.
Controllers must tolerate dropped snapshots (latest-state semantics); late
joiners and recovering clients resync from KV/last-per-subject, not from
backlog. Metrics consumers scale via queue groups. Buffer tuning is the
documented last resort, not a strategy.
**Why:** NATS deliberately protects the system over any consumer — client-side
drop + server-side disconnect are features
([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers));
JetStream flow control is decoupled so no consumer can slow the engine's
publishes ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
TraCI's 11× barrier slowdown is the failure mode this stance avoids
([TraCI](https://sumo.dlr.de/docs/TraCI/index.html)).
**Trade-off:** A disconnected human controller loses steering for a reconnect
window; acceptable per ADR-0005's "late intents apply later" rule. Factorio's
megapacket warning applies to reconnecting clients replaying buffered intents
— cap client reconnect buffers ([[arch-time-model]]).
**Field context:** [implementation §7](./implementation.md).

### 6. Deployment: embedded for tests, compose for topology, WebSocket for browsers
**Choice:** Engine tests and single-binary demos run `nats-server` in-process
(`DontListen: true` + `nats.InProcessServer` over `net.Pipe` — no TCP);
development and demos run the docker-compose server (ADR-0004); the TS viz
client connects over the server's WebSocket listener (binary frames).
**Why:** Embedding is an official deployment mode with a Go library API
([clients](https://docs.nats.io/running-a-nats-service/clients),
[gosuda](https://gosuda.org/blog/posts/how-embedded-nats-communicate-with-go-application-z36089af0));
Cybervet ships the same single-binary shape with self-provisioned streams/KV
([cybervet](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md));
WebSocket support is server-native since 2.2
([websocket](https://docs.nats.io/running-a-nats-service/configuration/websocket)).
**Trade-off:** In-process mode skips real socket behavior (slow-consumer
disconnects, TLS, auth) — integration tests still need the container.
**Field context:** [implementation §9](./implementation.md).

### 7. Contracts and tenancy: AsyncAPI as source of truth; per-user permissions v1, accounts later
**Choice:** One AsyncAPI 3.0 document declares subjects, payloads, and
headers; TS models are codegen'd from it. Message envelopes carry
`schema_version` and `tick`. v1 auth is a single account with per-user
allow/deny: a controller may publish only `ts.*.ctl.intent.{own_id}` and
subscribe `ts.{run}.state.>`; observers get read-only `ts.>` subscribe;
only the engine user may publish `ts.*.log.>`. Public multiplayer upgrades to
account-per-tenant with curated exports (EventStack pattern) when needed.
**Why:** AGENTS.md makes subjects+payloads a sacred public API; AsyncAPI is
the incumbent machine-readable contract standard with NATS bindings and TS
codegen ([Docsio](https://docsio.co/blog/asyncapi),
[tooling update](https://www.eventstack.tech/posts/asyncapi-tooling-update-week-46)).
Subject-level allow/deny with `deny` precedence is the documented primitive;
accounts are the documented isolation upgrade with the "many small accounts"
topology guidance ([authorization](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/authorization),
[accounts](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/accounts)).
**Trade-off:** AsyncAPI codegen for Go is less mature than TS/.NET — hand-roll
Go types against the spec, or generate the spec from Go structs (a tool exists
in the [AsyncAPI ecosystem](https://www.asyncapi.com/tools)); permission rules
add ops surface that the compose stack must template.
**Field context:** [implementation §10](./implementation.md),
[competitors §AsyncAPI](./competitors.md).

## Compare/Contrast: Us vs the Field

| Dimension | TraCI (SUMO) | MATSim events | CARLA | Kafka | Cybervet | us (recommended) |
|---|---|---|---|---|---|---|
| Live fan-out | blocking socket barrier | in-JVM handlers | sync TCP | consumer groups | core NATS + WS | **core NATS, drop-tolerant** |
| Durable record | none (re-run) | XML files | state log | offset log | matchmaking stream | **JetStream intent log + keyframes + CRC** |
| Seek by sim time | n/a | re-parse | file position | by offset/time | n/a | **keyframe + `DeliverByStartSequence`, tick in payload** |
| Slow consumer | stalls sim 11× | per-step barrier | stalls sim | lag tolerated | disconnected | **disconnected, resyncs (tolerated)** |
| QoS live / record | — | — | — | at-least-once | mixed | **at-most-once live / at-least-once record** |
| Embeddable | libsumo (in-proc) | in-proc only | no | no | **yes** | **yes (tests/dev)** |
| Multi-tenancy | none | none | none | none | single | **user perms v1 → accounts later** |

## The Genuine Gap (again, and wider)

[[arch-time-model]] found exactly one documented NATS-backed game system
(EventStack) and no tick loops. This research found the second (Cybervet:
embedded NATS + 10 ms tick loop + KV + WebSocket) — and the gap *still*
holds for the combination that defines us: **nobody has published a
NATS-backed authoritative simulation with deterministic replay.** Unwritten:
tick-number-based replay pacing vs broker `ReplayOriginal` (wall-clock),
snapshot keyframe/chunking practice on JetStream, JetStream puback latency
inside a fixed tick budget at faster-than-realtime rates, and CRC-verified
re-simulation from a stream. Our benchmarks and ADR-0006 write-up are, again,
near the frontier.

## Open Questions

- JetStream puback latency (R=1, file, default `sync_interval`) vs the 100 ms
  tick at high batch multipliers — benchmark at engine bring-up (same flag as
  [[arch-time-model]]).
- Keyframe size as networks grow ([[arch-road-graph-model]]): when full-state
  snapshots exceed ~1 MB, choose chunking scheme (app-level manifest vs Object
  Store) — needs a measured vehicle-count/byte curve.
- Stream topology at scale: per-run streams vs one stream with per-run
  subjects — consumer-count and asset-lifecycle costs are unpublished.
- nats.ws (browser) throughput for 10 Hz × N-vehicle snapshots; whether
  headers-only consumers suffice for viz heatmaps.
- `Nats-Rollup` interaction with consumer ack floors (undocumented corner) if
  rollup is ever used on the keyframe subject.
- Auth callout / `nsc` JWT operator mode: deferred — needed only for public
  multiplayer tenancy.

## Connections to Other Topics

- **Decides:** feeds ADR-0006 (message contracts / subject taxonomy) — the
  follow-up ADR ADR-0002 deferred to.
- **Honors constraints from:** [[arch-time-model]] / ADR-0005 — tick numbers
  in payloads not stream positions; intent log + keyframes + CRC subjects
  specified here; never-block-the-tick maps to NATS's drop/disconnect model.
- **Constrains:** [[concept-vehicle-controller-interface]] (intent envelope:
  `{run}:{tick}:{seq}` idempotency key, 1-tick minimum latency, drop-tolerant
  state subscription), [[arch-state-authority]] (single-writer goroutine =
  sole `log.>` publisher; OCC asserts it broker-side),
  [[concept-scenario-format]] (run/scenario ids anchor the taxonomy),
  [[integration-maplibre-realtime]] (WebSocket binary, 10 Hz snapshot subject,
  KV/last-per-subject resync, ~200–300 ms interpolation buffer from
  [[arch-time-model]]), [[domain-congestion-metrics]] (metrics plane subjects
  + queue-group consumers), [[domain-signal-control]] (a signal controller is
  just another controller on `ctl.intent.*`).
- **Depends on:** [[arch-road-graph-model]] for snapshot size → chunking;
  engine bring-up benchmarks for the puback numbers above.
- **Relates to:** [[domain-simulator-landscape]] (SUMO/MATSim/CARLA interface
  shapes surveyed as the systems we replace), [[integration-osm-extraction]]
  (network size drives snapshot bytes).
