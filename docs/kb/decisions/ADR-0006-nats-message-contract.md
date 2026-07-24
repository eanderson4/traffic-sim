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

## Addendum (2026-07-17, M3 bring-up)

- **Ack subject added**: `ts.{run}.ctl.ack.{controller_id}` — the engine echoes
  `applied_tick` here per tick (ack + control-latency meter + HUD health
  signal). Declared in `contracts/asyncapi.yaml`.
- **OCC intra-batch rule**: with multi-message per-tick log batches,
  `Nats-Expected-Last-Sequence` is *predicted* per batch position
  (lastSeq + i); valid because sole-writer + same-connection ordering holds.
- **KV binding**: logical `ts.{run}.meta.>` maps to bucket `ts_runs` with keys
  `{run}/meta`, `{run}/state` (nats.go bucket addressing); recorded as an
  `x-nats` extension in the AsyncAPI doc.
- **Measured at bring-up**: puback ≈ 5–12 µs/intent amortized at 1–100
  intents/tick (~3 orders under the 100 ms budget); SoA snapshots 24 B/veh,
  keyframes 77 B/veh → keyframes cross the 1 MB discipline at ≈13.6k vehicles
  (chunking scheme must land before city scale); live fan-out 1.36M msgs/s
  aggregate to 8 subscribers. Details: `engine/BENCHMARKS.md`.
- **Open (M4 candidates)**: whether the run spec is also stored as the log
  stream's head message (today only in the KV registry); the binary frame
  carries a placeholder lane projection until road-graph geometry lands
  (schema_version bump when it does).

## Addendum (2026-07-17, M4 contract machinery)

- **Schema v2**: intent/log frames v2, keyframes TSKF v2 (carry controller
  persistent axes; not v1-readable — no persistent deployments existed), new
  channels for handshake/claims/observations/events/introspection plus
  `ts.{run}.log.event`; envelope `schema_version: 2` everywhere.
- **Dedup id (v2)**: `Nats-Msg-Id` is `{run}:{tick}:{predicted-stream-seq}` —
  unique run-wide; the per-tick sequence collided when pause/resume events
  shared a frozen tick.
- **Disconnect detection is tick-space liveness** (silence > DetachAfterTicks,
  default 10 ⇒ detach ⇒ unclaimed-vehicle events). Core NATS has no presence
  primitive; `$SYS` account events deferred until auth/accounts land.
- **Keyframe ≈ 96 B/vehicle** with controller axes — the 1 MB chunking
  threshold moves from ≈13.6k to ≈10.9k vehicles (still pre-city-scale).

## Addendum (2026-07-18, M6 browser viz bring-up)

- **§8 confirmed in practice**: the embedded server serves browser clients
  with `DontListen` (no TCP client port) + `Websocket.Port` set — the two
  listeners are independent; nats.ws 1.30.x connects from both Chrome and
  node ≥ 22 (global WebSocket) without a polyfill. `engine/cmd/serve` is the
  single-binary demo shape.
- **Contract gaps revealed by the first real viz client** (all tolerable at
  viz scale, worked around client-side; candidates for the observability /
  schema-v-next ADRs — no contract change made in M6):
  1. **TSSF v1 carries no speed and no lane id.** The viz derives speed from
     inter-snapshot displacement and re-attaches vehicles to lanes by
     nearest-segment lookup for its (labelled, client-derived) congestion
     proxy. Authoritative per-section metrics belong to
     `domain-congestion-metrics`; if consumers keep needing kinematics, a
     schema bump (speed f32, maybe lane id) is cheaper than every client
     re-deriving them.
  2. **Coordinates are the network's local metric frame, and the frame
     descriptor (projection + netOffset) is not discoverable over NATS.**
     M6 ships it as a sidecar foreign member of the static network GeoJSON
     the viz loads out-of-band. A browser client that only has the WS
     endpoint cannot place vehicles on a map; the run registry
     (`{run}/meta`) is the natural home for the projection descriptor.
  3. **Tick length lives only in the run spec.** The client interpolates on
     wall-clock arrival instead — fine at 1× pacing, wrong for
     faster-than-realtime serving; dilation signaling is already a §1
     concern (dilation scalar on the live plane) and remains open.

## Addendum (2026-07-20, M9 signal state on the live plane)

- **New live-plane channel `ts.{run}.state.sig` (TSSG v1)** publishes the
  fixed-time signal-program table (ADR-0011): program ids, junctions,
  offsets, per-phase durations in ticks + tlLogic state strings, and the
  link→internal-lane binding (the stop-line geometry stays client-local in
  the static network GeoJSON). Declared in `contracts/asyncapi.yaml`
  (info version 2.1.0).
- **Why a new subject and not a TSSF signal block:** both shipped TSSF v1
  decoders (Go `ParseFrame`, TS `decodeFrame`) hard-reject on exact-length
  AND exact-version checks, so any in-frame extension — and any TSSF
  version bump — makes old clients error on every 10 Hz snapshot. A
  separate subject is the only strictly-additive shape. **Migration note:
  additive; old clients ignore `ts.{run}.state.sig` entirely — they never
  subscribe it, and TSSF v1 / TSKF v2 payloads are byte-identical.** (The
  M6 addendum's TSSF schema-bump candidates — speed, lane id — remain
  open and unaffected.)
- **Light STATES are derived, never shipped (ADR-0011 §1).** Phase state
  is a pure function of the tick count and the compiled program, so the
  frame carries the program TABLE plus the publish tick and clients
  evaluate the kernel's own integer math (cycle = Σ phase ticks; half-open
  phase windows; phase 0 begins at offset_ticks; the cycle wraps). Phase
  changes need zero messages — the tick already rides every TSSF header.
  Per-tick cost on the vehicle path: unchanged (zero bytes).
- **Late joiners:** the table publishes at run start and republishes at
  the `signalCatchUpEvery` cadence (20 ticks; originally the §6 keyframe
  rhythm, shortened 2026-07-23 because a 100-tick wait read as "signals
  take seconds to appear" after every demo attach); convergence ≤ 20
  ticks (2 s at 1×) — never waiting for a phase change (the I-280 cycle
  is 90 s). Tested in
  `engine/natsio/sigframe_test.go`: a subscriber attaching mid-run
  receives the next cadence table and its derivation matches the kernel's
  `PhaseAt` over a full-cycle sweep; an old (snapshots-only) client
  decodes the whole live stream without error.
- **An empty table is explicit:** runs without signalized junctions
  publish `program_count 0` — distinguishable from "no table yet".
- **Seam for external signal control (ADR-0011 D1):** when commanded
  states replace fixed-time derivation, light state ceases to be
  tick-derivable; this subject is the natural carrier for that evolution,
  which is its own contract ADR.

## Addendum (2026-07-20, M10 runtime demand director)

- **New contract-plane channel `ts.{run}.ctl.verb.{controller_id}`**
  (request/reply, JSON) carries director verbs (ADR-0008 §5's director
  role; scenario-format §3's runtime demand director). The sender must
  hold the **director** grant — the grant model is UNCHANGED (the grant
  has existed since ADR-0008; see its 2026-07-20 clarification). v1
  implements verb `spawn`: origin lane id, vehicle-type name,
  `earliest_tick`, and a director-assigned **`request_id`** that is the
  idempotency key — the engine remembers the reply per id for the run's
  lifetime, so a retried verb (reconnect, publish retry, director
  failover with deterministic ids) is answered `duplicate: true` and
  never double-spawns. Request/reply — not the intents' fire-and-forget —
  because the director paces its sampling schedule on accept/reject, and
  validation rejections (unknown origin, lane not a spawn origin, unknown
  type, unsupported verb) must reach it.
- **New record-plane subject `ts.{run}.log.verb`** (JSON, one message per
  ACCEPTED verb, in the tick's log batch after the arbitrated intents)
  stamped with the verb's applied_tick. Only first-seen accepted verbs
  are recorded — rejections and duplicates are not — so the log is
  exactly the accepted set. Replay re-enqueues verbs at their recorded
  ticks and the kernel's deterministic injection queue (hold-and-retry,
  bounded at 600 ticks past `earliest_tick`; the Spawner's own
  origin-clearance and density-cap rules) reproduces the identical spawn
  ticks, vehicle ids, and per-vehicle streams: **the demand sampler
  never re-runs**, and replay is bit-identical (pinned in
  `engine/natsio/verb_test.go`: live run + verbs → `ReplayFromStream`
  re-simulates through verb re-enqueue and verifies every logged CRC).
- **Migration note: additive; old clients and engines are unaffected.**
  Old clients never subscribe `...ctl.verb.>` and never see
  `...log.verb` (replay consumers filter by subject); old engines simply
  have no verb handler — a director talking to one gets no reply, which
  is the designed absence signal. All M1–M9 payload shapes are
  byte-unchanged. The one visible change is on the keyframe path:
  **TSKF v3** appends the pending-directive queue and is written ONLY
  while that queue is non-empty — an empty queue marshals byte-identical
  v2, so director-free runs produce bit-identical keyframes, CRCs, and
  log streams (the pinned scenario CRCs hold). Readers accept v2|v3;
  pre-M10 binaries reject v3 keyframes with the explicit
  "unsupported version" error — acceptable per the M4 precedent (no
  persistent deployments; recordings are local artifacts).
- **Tick-order point (documented in code, `engine/director.go`):** verbs
  drained between ticks are stamped with the next tick (the intents'
  applied_tick convention) and inject at Step phase 1 (events),
  immediately after the deterministic spawner and before intents.
- **Blocked-origin policy: hold-and-retry, bounded and deterministic**
  (chosen over reject-and-let-director-retry): it matches the Spawner's
  "unmet demand carries over" semantics, keeps the engine authoritative
  over injection timing, and — critically for replay — makes the
  injection outcome a pure function of (recorded verb, world state), so
  no rejection round-trips need recording. Expiry (600 ticks) is likewise
  a pure function and needs no record.
- **Rolling CRC** folds the pending-directive queue (only when non-empty)
  so replay catches queue divergence; TSKF v3 round-trips the same queue
  for seek fidelity (tested: keyframe mid-hold restores and matches the
  uninterrupted run's CRC).
- **Reference client:** `engine/cmd/demand-director` — strict-JSON flows
  (constant | Poisson spacing, piecewise-constant slices in sim seconds,
  per-flow vType weights), per-vehicle keyed sampling via
  `engine.DeriveStream(seed, flowKey^ordinal)` (ADR-0005/ADR-0007
  discipline; deterministic request ids make director failover
  invisible), heartbeats between sparse verbs (the liveness budget
  detaches silent controllers — measured in bring-up).

## Addendum (2026-07-21, M11 scenario content hash in run meta)

- **RunMeta's embedded `spec` may carry `Hash`** — the ADR-0012 scenario
  content hash, present when the run was loaded from a scenario directory.
  Additive and omitempty (old readers decode unchanged; flag-built runs
  emit nothing new), never read by the kernel, not part of the CRC'd world
  state, and `schema_version` stays 2 — this is the metadata plane doing
  its job: (content-hash, seed) is recorded so two runs of "the same
  scenario" are comparable across machines and checkouts. Documented in
  `contracts/asyncapi.yaml` (RunMetaView).

## Addendum (2026-07-23, VCR replay driver: `-replay` namespace + demo HTTP control plane)

The replay milestone (podcast compare-variants; `engine/cmd/replay` +
`natsio.Player`) re-simulates a recorded run from its durable JetStream
record plane and republishes the LIVE plane at a configurable pace. Two
contract-surface decisions, both surfaced by the external reviewers and
recorded here per rule 5:

- **The `-replay` run-id suffix is RESERVED.** The player publishes the
  existing TSSF/TSSG frame schemas (no payload change) on
  `ts.{run}-replay.state.snap` / `ts.{run}-replay.state.sig` — a fresh run
  id, so replay never collides with a live or recorded `{run}` on the same
  broker. To make that collision-free in both directions, `RunLive` and
  `NewRecorder` now REFUSE source run ids ending in `-replay` (a recorded
  `foo-replay` would squat on `foo`'s replay plane). Migration note: such
  ids were previously accepted; none exist in the wild (local-first, no
  published consumers), and AsyncAPI never constrained the run-id token, so
  no schema change. Replay runs are deliberately registry-less and
  contract-less: no Recorder (no OCC, no second record plane), no KV run
  meta, no controllers — consumers must not expect registry entries for
  `-replay` runs. The player IS CRC-audited against the record as it plays
  (divergences are logged and counted, playback continues — demo policy,
  distinct from the audit path's abort).
- **The demo control plane is localhost HTTP, and ADR-0002 stands.**
  pause/resume/speed/seek are served by the replay child over loopback HTTP
  (`Player.Handler()`, stdlib) for the viz's replay panel, proxied by
  demosrv. This is the same carve-out as demosrv's own orchestration HTTP
  (process babysitting/operator console, documented at its introduction):
  ADR-0002 governs the sim service planes — world state, controller
  intents, demand, metrics — which remain NATS-only. The player is
  publish-only; it consumes no intents and holds no authority over any
  live run's state, so engine authority is untouched. If replay control
  ever needs to cross a trust or machine boundary, it moves to
  `ts.{run}-replay.ctl.*` subjects; loopback demo tooling does not.
- **Playback horizon:** a record marked `done` in the run registry ran to
  `spec.Ticks`, so the player re-sims to `spec.Ticks` even when the log's
  last message precedes it (sparse-cadence records; every logged input is
  replayed, so the tail is the original run's tail). A record not marked
  `done` was killed or truncated: playback holds at the last logged tick —
  continuing would invent an under-controlled tail the original run never
  produced. Both horizons are exposed in the player's status.

### RunMeta Net.Path value note (same milestone)

`serve` now absolutizes `spec.Net.Path` before the run starts, so the
RunMeta written to the registry and carried by durable recordings holds an
ABSOLUTE filesystem path (previously it echoed whatever `-netfile`/scenario
resolution produced — usually relative). Migration note: recordings made
before this change with relative paths resolve against the replay process's
working directory (the demosrv child runs from the repo root); recordings
made after are cwd-independent but checkout/machine-bound — moving a store
to another machine or relocating the checkout breaks network loading even
when the scenario hash matches. Acceptable under ADR-0004 (local-first);
portable network identity (content-addressed network in the store) is
future work, logged in the KB work-queue.

The replay player's HTTP status payload (demo control plane, not a NATS
subject) carries the recorded run's ADR-0012 scenario `hash` (omitempty) so
demosrv can bind display metadata to the recording; additive to that
localhost JSON contract.
