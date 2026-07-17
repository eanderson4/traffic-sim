# Mechanics: NATS Backbone

> Source: web research (greenfield — no bus code exists; this file collects the
> *mechanisms* the backbone is built from, to be re-audited against real streams
> and subjects once ADR-0006 lands) | Researched: 2026-07-16 | Git HEAD: ae75fba

## 1. Subject mechanics: the only addressing primitive

From [Subject-Based Messaging](https://docs.nats.io/nats-concepts/subjects):

- A subject is a dot-tokenized string; publishers always publish to a fully
  specified subject, only subscribers may use wildcards. `*` matches exactly one
  token (never a substring), `>` matches one or more tokens and **only at the
  tail**. `time.us.*` matches `time.us.east` but not `time.us.east.atlanta`.
- NATS is *interest-based*: messages with no subscribers are discarded (core
  NATS). Subscriptions propagate across routed servers; subjects are ephemeral
  and disappear when no longer subscribed.
- Sizing guidance: no hard limit, but keep subjects ≤16 tokens and <256 chars.
  "10s of millions of subjects" are manageable, but >1M subscribed subjects
  costs >1 GB of server memory, growing linearly. Fine-grained per-entity
  addressing is explicitly sanctioned.
- Naming rules: alphanumerics, `-`, `_` recommended (case sensitive); `.`, `*`,
  `>` reserved; `$`-prefixed subjects are system-reserved (`$SYS`, `$JS`,
  `$KV`); `_INBOX` is the request-reply convention. Pedantic client mode
  validates subjects at publish time (off by default for efficiency).
- Design guidelines that read like they were written for us: first token(s)
  establish a namespace, final token(s) carry identifiers; "encode (business)
  intent into the subject, not technical details"; a subject should carry more
  than one message; subscriptions should be stable; prefer wildcard
  subscriptions over many concrete ones; put extra metadata in **headers**, not
  the subject.
- Wire taps: a `>` subscriber (subject to permissions) receives everything —
  our observer/debug tooling is free.

## 2. The three planes: core NATS / JetStream / KV (+ Object Store)

- **Core NATS** is fire-and-forget pub/sub plus request-reply, at-most-once,
  with no persistence — delivery requires temporal coupling (subscriber must be
  connected when the message is published)
  ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream),
  [reconnect buffer](https://docs.nats.io/using-nats/developer/connecting/reconnect/buffer)).
  Client libraries buffer outbound messages during reconnects (configurable
  byte/message caps) but "it is possible that it is never sent."
- **JetStream** is built into `nats-server` (no separate system); a *stream*
  captures messages published on bound subjects and stores them; *consumers*
  are stateful views delivering them later
  ([streams](https://docs.nats.io/nats-concepts/jetstream/streams)). Crucially,
  a **stream captures plain core-NATS publishes on its subjects** — one publish
  can simultaneously fan out to live subscribers and be persisted; live
  delivery does not wait for storage ([streams](https://docs.nats.io/nats-concepts/jetstream/streams),
  [JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
- **KV** is an abstraction over a stream (bucket → `KV_`-prefixed stream):
  put/get/delete/purge/keys, atomic `create` (set-if-absent) and `update`
  (compare-and-set by revision), per-bucket TTL, `watch`/`watch all` for
  realtime change feeds, and `history` (default 1 = latest only). Keys allow
  `a-z A-Z 0-9 _ - . = /`, so dot-hierarchies + wildcard watches work.
  Immediate consistency for monotonic reads/writes, but **no read-your-writes
  via direct-get** (may hit followers); strongest reads go to the stream leader
  ([KV store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)).
- **Object Store** chunks arbitrarily large blobs into message-sized pieces
  over a stream with a SHA-256 digest; the CLI walkthrough stores a 1.5 GiB
  file as 12,656 chunks (≈128 KiB each) and reads it back at 279 MiB/s
  ([obj walkthrough](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_walkthrough),
  [obj store](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_store)).
  This is the broker-native answer to "snapshot bigger than one message."

## 3. Stream mechanics (the record)

All from [streams](https://docs.nats.io/nats-concepts/jetstream/streams) and
[JetStream overview](https://docs.nats.io/nats-concepts/jetstream) unless noted:

- **RetentionPolicy** picks *when stored messages may be deleted*:
  - `LimitsPolicy` (default) — keep for replay until limits hit; the
    event-sourcing/replay choice.
  - `WorkQueuePolicy` — delete on ack; each message consumable **once**;
    enforced by allowing only one consumer per subject (consumer filters must
    not overlap). Messages that exhaust `MaxDeliver` redeliveries **stay** in
    the stream (manual delete required) — a built-in dead-letter pile.
  - `InterestPolicy` — delete once *all* consumers interested in the subject
    have acked; **zero consumers ⇒ messages are deleted on arrival**, so
    consumers must be defined before publishing starts.
  - Limits (`MaxMsgs`, `MaxBytes`, `MaxAge`, `MaxMsgsPerSubject`) act as upper
    bounds under every policy; `DiscardOld` (default) deletes oldest on limit
    vs `DiscardNew` rejecting new writes with a publish error
    (`DiscardNewPerSubject` exists since 2.9).
- A stream binds multiple subjects (wildcards allowed); subjects are editable
  later, and consumers still see already-stored messages on removed subjects.
- **Storage overhead**: on-disk record = 4 B length + 8 B seq + 8 B timestamp +
  2 B subject length + subject + headers + payload + 8 B hash — a 5-byte
  `hello` takes 39 bytes; short subjects pay off at high message rates
  ([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).
- Storage type File (default) or Memory; replicas 1–5 (3 recommended, 2 deemed
  pointless, 5 = max).
- **`Nats-Rollup` header** (requires `AllowRollup`): `sub` purges all prior
  messages on the published subject, `all` purges the stream — docs name the
  use case verbatim: "a common use case for rollup is for state snapshots"
  ([streams](https://docs.nats.io/nats-concepts/jetstream/streams)).
- **RePublish**: server re-publishes stored messages to another subject
  immediately after write (optionally headers-only with `Nats-Msg-Size`) — a
  broker-side "stored → live" bridge without a consumer.
- `FirstSeq` (2.10) seeds a stream's initial sequence; `Compression: s2` (2.10)
  compresses file storage; `AllowAtomicPublish` (2.12) commits N messages
  atomically with per-message consistency checks
  ([2.12 notes](https://docs.nats.io/release_notes/whats_new_212));
  `AllowMsgTTL` + `Nats-TTL` header gives per-message expiry (2.11)
  ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211)).

## 4. Consumer mechanics (reading the record)

All from [consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)
and [model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive):

- **DeliverPolicy** (where to start): `DeliverAll` (earliest available),
  `DeliverLast`, `DeliverLastPerSubject` (materialized-view catch-up),
  `DeliverNew` (live tail only), **`DeliverByStartSequence`** (`OptStartSeq` —
  the seek primitive), `DeliverByStartTime` (`OptStartTime`). It only picks the
  first message; thereafter the consumer resumes from its ack floor.
- **ReplayPolicy**: `ReplayInstant` (default, as fast as acks/MaxAckPending
  allow) vs **`ReplayOriginal`** — "pushed to the client at the same rate they
  were originally received, simulating the original timing." Caution: the model
  deep dive states ReplayPolicy **can only be set on push-based consumers**;
  pacing is by stored wall-clock arrival time, not by any tick field.
- **Ordered consumers**: ephemeral, no acks, automatic flow control,
  single-threaded dispatch, **auto-recreated on gap detection** — the intended
  tool for "replay a stream sequentially for inspection/analysis."
- **AckPolicy**: `AckExplicit` (default, per message), `AckAll` (ack last ⇒
  batch ack), `AckNone`, `AckFlowControl` (2.14, for sourcing/mirroring).
  Wire ack types: `+ACK`, `-NAK` (retry, immediate redelivery), `+WPI`
  (in-progress, extends AckWait), `+NXT` (pull: ack + fetch next), `+TERM`
  (stop redelivery without acking).
- **Flow control knobs**: `MaxAckPending` (default 1000, `-1` = unlimited)
  caps unacked in-flight — the only flow control for push consumers; pull
  consumers are demand-driven (Fetch with batch/timeout/max-bytes). Push
  consumers add per-subscription sliding-window `FlowControl` and
  `IdleHeartbeat`; `RateLimit` throttles delivery in **bits per second**.
- **Durable** consumers persist position and resume across disconnects;
  ephemerals auto-delete after inactivity (`InactiveThreshold`).
  `FilterSubject(s)` give server-side filtering; note the permission footgun:
  single-filter consumers get granular `$JS.API.CONSUMER.CREATE.{stream}.
  {consumer}.{filter}` perms, multi-filter falls back to the filter-less API.
- Pull consumers are meant to be **shared like queue groups** for horizontal
  scaling without partition management; 2.11 adds priority groups
  (pinning/overflow) and consumer **pausing** (`PauseUntil`) for maintenance
  ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211)).
- Ack sampling (`SampleFrequency`) publishes per-ack telemetry for monitoring.

## 5. The event-sourcing toolbox (what the intent log is built from)

- **Publish dedup**: `Nats-Msg-Id` header makes writes idempotent — the server
  tracks IDs in a sliding `DuplicateWindow`, **default 2 minutes**, and docs
  "caution against large windows"; only the ID is compared, never the body
  ([model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).
- **Optimistic concurrency on append**: `Nats-Expected-Last-Sequence`
  (stream-level), `Nats-Expected-Last-Subject-Sequence` (subject-level),
  `Nats-Expected-Last-Msg-Id`; the server rejects stale writes. Per the NATS
  team: subjects are indexed inside a stream so the OCC check adds no overhead,
  you get linearizability per subject plus total order across the stream, and
  filtered replay scans only the blocks between a subject's earliest and latest
  events ([headers](https://docs.nats.io/nats-concepts/jetstream/headers),
  [discussion #3772](https://github.com/nats-io/nats-server/discussions/3772)).
- **Exactly-once is a composition**, not a primitive: publish-side dedup
  (`Nats-Msg-Id`) + consumer-side **double-ack** (`AckSync()` — server acks
  your ack, so a lost ack can't cause redelivery)
  ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream),
  [model deep dive](https://docs.nats.io/using-nats/jetstream/model_deep_dive)).
  Base QoS: core NATS at-most-once; JetStream at-least-once.
- **AckWait/Backoff/MaxDeliver**: unacked messages redeliver after AckWait
  (default redeliver forever, `MaxDeliver` -1); `Backoff` is a delay sequence
  that overrides AckWait entirely; `-NAK` redelivers immediately unless
  `nakWithDelay` ([consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)).
- Sequence numbers are **per-stream** (plus a per-subject sequence surfaced as
  `Nats-Last-Sequence` on republish); a sim tick number must therefore live in
  the payload/headers, never be inferred from stream position
  ([headers](https://docs.nats.io/nats-concepts/jetstream/headers)).

## 6. Message mechanics: size, headers, chunking

- `max_payload` defaults to **1 MB**, is a server config, can be raised to
  64 MB, but docs recommend ≤8 MB; message size counts payload + headers
  ([pub/sub](https://docs.nats.io/nats-concepts/core-nats/pubsub),
  [streams](https://docs.nats.io/nats-concepts/jetstream/streams)).
  The CLI reports it at connect: "Maximum Payload: 1.0 MiB"
  ([obj walkthrough](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_walkthrough)).
- Headers are first-class (arbitrary key/value, `Nats-` namespace reserved);
  subscription modes can deliver **headers only** for cheap metadata scanning;
  `HeadersOnly` consumers do the same against stored messages
  ([subjects](https://docs.nats.io/nats-concepts/subjects),
  [headers](https://docs.nats.io/nats-concepts/jetstream/headers)).
- Application-level large payloads: (a) raise `max_payload` (mildly
  discouraged), (b) chunk it yourself under a manifest subject, or (c) use the
  Object Store (chunks + SHA-256 digest + watch notifications)
  ([obj store](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_store)).
- Atomic multi-message append exists since 2.12 (`Nats-Batch-Id`,
  `Nats-Batch-Sequence`, `Nats-Batch-Commit`) — a per-tick batch of arbitrated
  intents can be committed as one unit
  ([headers](https://docs.nats.io/nats-concepts/jetstream/headers)).

## 7. Backpressure and slow consumers (the asymmetric design)

From [slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers):

- NATS **protects the system over any individual consumer** — the opposite of
  end-to-end backpressure brokers. Detected client-side: pending buffer
  overflows ⇒ messages **dropped**, app notified via async error callback
  (`ErrSlowConsumer`). Detected server-side: the connection's write deadline
  is exceeded ⇒ server **disconnects the client**; count exported as
  `slow_consumers` in `/varz` monitoring.
- Client defaults (nats.go): pending limit **65,536 messages / 64 MiB**
  (`65536*1024` bytes) per subscription, tunable via `SetPendingLimits`;
  increasing buffers "will only postpone slow consumer problems."
- Server knob: `write_deadline` (e.g. `2s`) — how long the server buffers
  outbound data to a stuck connection before calling it slow.
- JetStream flow control is **decoupled**: publisher↔server and
  server↔consumer are flow-controlled separately; no consumer can slow a
  publisher ([JetStream overview](https://docs.nats.io/nats-concepts/jetstream)).
- 2.11 adds stream **ingest rate limiting**: core-NATS publishes into a stream
  are buffered per stream (defaults 128 MB / 10,000 messages), then the server
  returns 429 `JSStreamTooManyRequests` and drops; "it should not generally be
  possible to hit this limit while using JetStream publishes and waiting for
  PubAcks" — i.e. writers of record must use JS publish with acks
  ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211)).
- The JetStream API itself queues at most 10K inflight requests
  (`request_queue_limit`, since 2.10.21), then drops and emits a
  `$JS.EVENT.ADVISORY.API.LIMIT_REACHED` advisory
  ([resource mgmt](https://docs.nats.io/running-a-nats-service/configuration/jetstream-config/resource_management)).
- Graceful exit: **drain** (subscription or connection) processes inflight +
  pending messages before closing — the queue-group-safe shutdown pattern
  ([drain](https://docs.nats.io/using-nats/developing-with-nats/receiving/drain)).
- Documented mitigations menu: scale out with **queue subscribers** (random
  one-of-group delivery, no ordering), partition the subject namespace
  (`Sensors.North/South/...`) so publishers stay untouched while consumers
  shard, meter the publisher (least favored), tune buffers (last resort)
  ([queue groups](https://docs.nats.io/nats-concepts/core-nats/queue),
  [slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).

## 8. Consistency and durability of the record

From [JetStream overview](https://docs.nats.io/nats-concepts/jetstream):

- Clustered JetStream uses a NATS-optimized **RAFT** quorum; writes are
  **linearizable**, and since messages enter a stream in one global order
  (controllable via compare-and-publish), the stream is effectively
  serializable.
- Durability footgun: file streams flush to the OS synchronously but only
  `fsync` on `sync_interval`, **default 2 minutes** — an OS-level failure can
  lose acknowledged messages in a non-replicated setup. `sync_interval: always`
  fsyncs per message at a throughput cost. For a single-node local-first
  deployment this means acknowledged intent-log writes survive a server crash
  but not necessarily a machine crash inside the window.
- Local-first defaults: R=1 file stream is the documented "easily rebuilt or
  temporary" profile; R=3 is the production HA profile we don't need yet.

## 9. Deployment mechanics: embedded vs container vs browser

- **Embedded server**: `nats-server` is a Go library
  (`github.com/nats-io/nats-server/v2/server`); `server.NewServer(opts)` +
  `go ns.Start()` + `ns.ReadyForConnections(timeout)`; client connects to
  `ns.ClientURL()`. With `DontListen: true` and the client's
  `nats.InProcessServer(ns)` option, connection runs over an in-memory
  `net.Pipe` — no TCP listener at all; added for monolith/polylith dual-mode
  apps (mobile/WASM where sockets are impossible)
  ([gosuda deep dive](https://gosuda.org/blog/posts/how-embedded-nats-communicate-with-go-application-z36089af0),
  [NATS clients](https://docs.nats.io/running-a-nats-service/clients),
  [pkg.go.dev server](https://pkg.go.dev/github.com/nats-io/nats-server/v2/server)).
- **Container**: official Docker/Swarm support and a compose-friendly single
  binary ([NATS and Docker](https://docs.nats.io/running-a-nats-service/running/nats_docker));
  server is a small static binary runnable "from large instances in the cloud
  to resource constrained devices like a Raspberry Pi"
  ([compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
- **Browser**: server-side WebSocket listener since 2.2, alongside TCP,
  **binary frames only**, optional TLS/compression/origin checks — the path
  for the TS/MapLibre client ([websocket](https://docs.nats.io/running-a-nats-service/configuration/websocket)).
  The server also speaks MQTT (SparkplugB-aware since 2.11)
  ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211)).
- Client ecosystem: "over 40 client language implementations"
  ([pkg.go.dev nats-server](https://pkg.go.dev/github.com/nats-io/nats-server/v2)).
- Resource governance: global `jetstream { max_mem, max_file }`, per-account
  `max_mem/max_file/max_streams/max_consumers`, `max_ha_assets` for R3/R5
  assets ([resource mgmt](https://docs.nats.io/running-a-nats-service/configuration/jetstream-config/resource_management)).

## 10. Security mechanics: accounts, users, subject permissions

- **Accounts** are isolated subject spaces — multi-tenancy by construction;
  messages in account A are invisible in B unless explicitly exported. Official
  topology guidance: "more accounts with few (even one) clients is a better
  design than a large account with many users with complex authorization
  configuration" ([accounts](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/accounts)).
- Cross-account sharing via **exports/imports** of *streams* (pub flows) and
  *services* (request-reply), optionally restricted to named accounts, with
  prefix/remap at import; JetStream streams can be mirrored/sourced across
  accounts as the "locked-down" sharing pattern ([accounts](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/accounts)).
- **User permissions**: per-user `publish`/`subscribe` allow/deny lists with
  wildcards (`deny` wins on overlap), queue-name-qualified subscribe perms
  (`"foo v1.>"`, deny `"> *.prod"`), `default_permissions` fallback, and
  `allow_responses` to grant temporary publish rights on reply subjects
  (max count + expiry) ([authorization](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/authorization)).
- AuthN menu: username/password, token, NKeys (Ed25519), decentralized
  operator/account/user **JWTs** managed by `nsc`; auth callout for external
  IdPs ([authorization](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/authorization),
  [compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
- `_INBOX.>` is the reply-subject convention; permission configs must not
  break request-reply by forgetting it
  ([authorization](https://docs.nats.io/running-a-nats-service/configuration/securing_nats/authorization)).

## 11. Performance anchors (orders of magnitude for the tick budget)

- Go client, 10 publishers, 16-byte messages: **8.4M msgs/s aggregate**
  (128 MB/s) in a conference-deck `nats-bench` run
  ([OSCON 2019 deck](https://conferences.oreilly.com/oscon/oscon-or-2019/cdn.oreillystatic.com/en/assets/1/event/295/Simple, secure, and reliable_ Building cloud native applications with NATS Presentation.pdf));
  an independent vendor TCO report measured ">1M messages per second" for core
  NATS in their test harness ([Synadia TCO](https://www.synadia.com/downloads/nats-kafka-tco-report.pdf)).
- Loopback RTT as reported by `nats account info`: **~65 µs**
  ([obj walkthrough](https://docs.nats.io/nats-concepts/jetstream/object-store/obj_walkthrough));
  `nats server check connection` on localhost: ~70 µs RTT
  ([NATS clients](https://docs.nats.io/running-a-nats-service/clients)).
  Against a 100 ms tick, broker round-trips are three orders of magnitude under
  budget — the open question is JetStream pub-ack *persistence* latency at
  batch multipliers, not core transport.
- Prior-art calibration: Synadia's Cybervet game ran a 10 ms tick loop over
  embedded NATS with 4,000 scripted simultaneous players at <5% CPU delta on a
  single binary ([cybervet walkthrough](https://github.com/synadia-labs/showcase/blob/main/cybervet/walkthrough.md)).

## 12. Observability and advisories (operations hooks)

- JetStream emits advisories on `$JS.EVENT.ADVISORY.>` (API limits, consumer
  ack samples, max-deliver exhaustion), system events on `$SYS`; server
  metrics via HTTP `/varz` incl. `slow_consumers`
  ([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers),
  [resource mgmt](https://docs.nats.io/running-a-nats-service/configuration/jetstream-config/resource_management)).
- 2.11 adds **distributed message tracing**: set `Nats-Trace-Dest` and every
  server hop emits trace events (account-boundary crossings included);
  `Nats-Trace-Only` traces without delivering
  ([2.11 notes](https://docs.nats.io/release_notes/whats_new_211),
  [headers](https://docs.nats.io/nats-concepts/jetstream/headers)).
- Consumer ack sampling + `nats-top` + Prometheus exporter are the documented
  ops tooling ([consumers](https://docs.nats.io/nats-concepts/jetstream/consumers),
  [compare NATS](https://docs.nats.io/nats-concepts/overview/compare-nats)).
