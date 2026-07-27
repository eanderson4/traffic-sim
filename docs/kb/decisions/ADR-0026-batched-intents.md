# ADR-0026: Batched intents on the live plane

- Status: Accepted
- Date: 2026-07-26
- Ratified: 2026-07-26 — M0–M3 evidence in engine/BENCHMARKS.md §(d) (batch-mode table, applied-lag coverage), M1–M3 test pins in engine/natsio (tsib_test.go, driver/batch_test.go, driver_test.go); contract published as asyncapi info version 2.5.0.
- Amends: ADR-0006 §2 (self-sufficient live messages), §7 (binary SoA framing), ADR-0008 (controller contract — wire encoding only, semantics unchanged)

## Context

The live plane is asymmetric today. Observations are already batched: the
engine sends **one** TSOB frame per controller per tick on
`ts.{run}.ctl.obs.{id}` (`engine/natsio/contract.go:923`), and one TSSF
snapshot per tick on `ts.{run}.state.snap`. Intents are not: the default
driver publishes **one 44 B message per claimed vehicle per cadence tick**
on `ts.{run}.ctl.intent.{id}` (`engine/natsio/driver/driver.go:350`), and
the engine ingests each via callback → lock → slice append
(`engine/natsio/bus.go:239`).

Per-message cost, not payload bytes, is the wall this hits:

| live vehicles | intent msgs/s @10 Hz | notes |
|---|---|---|
| 14.5k (stress-dtla, largest run to date) | ~145k | works, but each msg is a publish+route+callback+lock round trip on both peers |
| 40k (target fleet) | ~400k | per-message CPU dominates; marshal/alloc churn per vehicle |
| 100k (metro ambition) | ~1M | order-of-magnitude anchor only: the 1.36M msgs/s in `engine/BENCHMARKS.md` is 8-subscriber *delivery* of 2.4 kB snapshot frames, not many-publisher 44 B ingest — M0 establishes the real ingest ceiling |

Payload size is a non-issue by comparison: 44 B/vehicle means even 40k
vehicles is 1.7 MB/tick of raw intent data, well inside the 1 MiB
per-message discipline *when batched* (cap below). The load-bearing
question is message *count*.

The controller-bus architecture is the project's differentiator
(heterogeneous controllers, replay, external clients). Scaling it to
metro fleets requires that message *count* per tick be O(controllers),
not O(vehicles). Region decomposition (horizontal scaling across engine
nodes) is a separate, later ADR; it does not fix per-vehicle intent
traffic inside a region, and this change is a prerequisite for it
(controller shards must already speak batches to align 1:1 with tiles).

## Decision

Add a batched intent message, **TSIB v1**, accepted alongside the existing
per-vehicle intent v2 on the same subject. Batching happens at the wire
boundary only; every downstream semantic is preserved.

### Demux: dedicated header, not magic bytes, not `schema_version`

Subject: `ts.{run}.ctl.intent.{controller_id}` — unchanged. The engine's
wildcard subscription (`...intent.>`) and per-publisher ordering are
unchanged. One message may be either encoding. `onIntent` demuxes on a
dedicated NATS header:

- `intent_encoding` absent ⇒ **v2** (every existing producer, zero
  migration).
- `intent_encoding: tsib` ⇒ **TSIB**.
- Any other value ⇒ **drop + count, loud** (`intentEncodingUnknown`).
  No fall-through to v2 parsing, so a future encoding against an old
  engine fails loudly instead of misparsing.

Two rejected discriminators:

- *Magic-byte sniffing*: a v2 payload begins with an arbitrary
  `vehicle_id u64` whose low bytes can spell `"TSIB"` — in-band magic is
  not a safe demux key. The TSIB payload still begins with a `TSIB`
  magic, validated at parse time as an integrity check only.
- *The existing `schema_version` header*: that header carries the
  **global** contract version (`SchemaVersion = 2`,
  `engine/natsio/server.go:79`) negotiated at hello. Overloading it as an
  intent-encoding discriminator breaks the upgrade path in both
  directions — bumping the global constant to 3 would make old engines
  misread v2 intents as TSIB, and a default-on TSIB driver attaching to
  an old engine would have its batches dropped with no negotiation
  signal. A dedicated key keeps the global version negotiation and the
  codec discriminator independent. (An old engine receiving a
  headered TSIB simply has no demux rule for it — the header sits on a
  subject the old engine already subscribes; deploy order is therefore
  engine-first, drivers-second, recorded in the M4 migration note.)

### Wire format

```
header (24 B):  magic "TSIB" | version u16 = 1 | flags u16 (reserved, 0)
                | tick u64 | count u32 | reserved u32
records:        count × 44 B, byte-identical layout to the fixed section
                of intent v2 (vehicle_id u64 | flags u32 | lane_delta i32 |
                accel f64 | speed_setpoint f64 | signals u32 | turn i32 |
                route_len u16 | reserved u16)
```

- **Batch cap: 20,000 records** (880,024 B payload + header). The
  theoretical 1 MiB ceiling (~23.8k records) leaves ~32 B of headroom —
  approximately the serialized header itself — so the cap is set
  conservatively and pinned by a boundary publish test. A controller with
  more claimed vehicles splits into multiple batches per tick (still ≪
  per-vehicle messages).
- **No seq on the wire — here or in v2.** Sequence numbers are assigned
  engine-side by the contract layer in arrival order (`ArrivedIntent`,
  bus.go:137-143; `engine/intent.go:65-71`); the driver has no seq
  counters. TSIB preserves that: expansion appends records to the buffer
  in record order, so engine-assigned seqs stay monotonic per controller
  exactly as they do for a v2 stream.
- **`tick` is informational only.** It records the source observation
  tick for diagnostics and for the applied-lag metric (M3). It is never
  validated and never gates acceptance: v2 intents carry no tick and
  ingest is arrival-based (applied at drain tick); TSIB inherits that
  semantics verbatim.
- **Route fields are forbidden in batch records.** Any record with
  `route_len ≠ 0` or flag bit4 (route present) set makes the **whole
  batch invalid** — structural rejection, dropped and counted
  (`intentBatchDropped`), logged. A controller sending a route update
  sends it as **one complete standalone v2 intent** carrying all of that
  vehicle's axes for the tick, and omits the vehicle from that tick's
  TSIB. (Splitting fixed axes into TSIB plus route into v2 would create
  competing same-vehicle intents under first-wins arbitration.)
  Fixed-size records keep the batch memcpy-friendly and the parse cheap.
- **Whole-batch structural validity.** Exact length
  (`24 + 44·count`), version, count ≤ cap, plus the route-field rule
  above. A structurally invalid batch is dropped whole — never partially
  applied. Per-record *semantic* checks (NaN/Inf accel/setpoint
  rejection, claim filtering, grant stamping, hold-last) are unchanged
  and happen per expanded record, exactly as per-message today: a
  semantically bad record drops alone, in parity with a bad v2 message
  dropping alone. An M1 test pins a mixed batch (one NaN record among
  valid records) proving only the bad record drops.

### Expand at ingest; nothing downstream changes

`onIntent` expands a valid TSIB into `count` `ArrivedIntent` entries
(controller id from the subject, intent from the record), appended to the
same locked buffer in record order. Everything past that point — claim
filtering, seq stamping, hold-last re-issue, deterministic apply order
(grant desc, vehicle asc, controller, seq, first-per-vehicle wins),
per-applied-intent recording on `ts.{run}.log.intent`, replay, the bake
pipeline — is **identical to today**. The record plane never sees a
batch.

This is the load-bearing choice: batching is a new message contract on
`ctlIntent` (hence this ADR) with **no downstream semantic change**. The
determinism guarantee is **replay determinism within each recorded run**:
a run's recorded intent log replays to a byte-identical CRC, batch mode
or not, because what is recorded is unchanged. What is *not* guaranteed
is byte-identity between a batch-on and a batch-off live run of the same
seed: batching holds all of a tick's intents until the driver finishes
computing them, where v2 publishing streams early vehicles while later
ones compute, so arrival-vs-drain timing can differ and applied ticks can
legitimately diverge. Equivalence between modes is asserted statistically
(paired-seed metrics protocol, ADR-0014), not by CRC.

### Driver aggregates per tick

The default driver collects the intents it computes for a cadence tick
and publishes one TSIB per tick (splitting at the 20k-record cap). Claim
lifecycle and the obs-driven loop are unchanged. Flag
`-intent-batch=on|off`, default **on**; `off` restores exact current
behavior for A/B measurement and debugging.

No claim-protocol change. Sharding remains emergent via exclusive claims
(driver.go:22-25); batching simply makes few-large-controllers the
efficient configuration, which is the shape region decomposition will
want. Explicit shard assignment is out of scope here.

### No tick barrier

The run loop stays fire-and-forget: intents apply whenever drained,
hold-last heals gaps, `PaceFloor` keeps the engine from outrunning
clients. Batching does not introduce a barrier or a deadline. (A barrier
becomes interesting with region decomposition — deliberate then, not
smuggled in now.)

## Consequences

- Intent messages per tick drop from O(vehicles) to
  Σ_controllers ⌈vehicles_c / 20k⌉. At 40k vehicles on 4 controllers
  (10k each): 4 msgs/tick instead of 40,000.
- `contracts/asyncapi.yaml` `ctlIntent` channel gains the TSIB schema —
  a message-contract change, hence this ADR (ADR-0006 §9 gate).
- One more wire codec to own. Mitigations: record layout reuses the v2
  fixed section verbatim; v2 remains a first-class citizen (routes,
  small fleets, debugging); codec table-tests shared between paths.
- Debuggability: per-vehicle traffic is no longer 1:1 with NATS messages.
  Counters (`intentBatches`, `intentRecords`, `intentBatchDropped`,
  `intentEncodingUnknown`) are the observability substitute; a
  `nats sub` still shows rates per controller.
- Header discipline: a TSIB payload published *without* the header is
  read as v2. For multi-record batches this always fails v2 structural
  validation (declared `route_len` at that offset exceeds 48) — loud,
  never silent. For the pathological single-record case (68 B payload,
  bytes 40–41 decoding as `route_len` 24) the v2 length check can pass;
  the resulting garbage-`vehicle_id` intent then dies at **claim
  filtering**, which is the named backstop. M1 pins both behaviors.

## Non-goals

- Region decomposition / multi-node horizontal scaling (later ADR;
  depends on this one).
- GPU controller workers. The SoA-friendly batch layout enables them;
  nothing here requires them.
- Grant policy / auth beyond the existing handshake.
- Batching observations (already per-controller) or the record plane.

## Implementation plan

Sandbox: `~/sandbox/traffic-sim`, branch `batched-intents` (forked from
main @ 081ea60; the `chicago-show` workstream is untouched in the grove
clone). Per AGENTS.md: every code commit passes the staged-diff gate
(Fable + sol, one round, blockers only); M4 gets the `--gemini` milestone
round before any recording or content hash binds the new codec.

- **M0 — Baseline measurement.** Extend the benchmark suite
  (embedded-server pattern of `BenchmarkJetStreamPubAck`) to measure
  intent ingest: N publisher goroutines × per-vehicle v2 intents at
  5k / 15k / 30k vehicles — msgs/s to drain-complete, `onIntent` CPU,
  tick p50/p99. Record in `engine/BENCHMARKS.md`, which today measures
  pubacks at 1–100 intents/tick and snapshot fan-out but has no
  fleet-scale intent-ingest numbers. Acceptance: baseline numbers
  committed.
- **M1 — Codec + engine ingest.** TSIB encode/decode next to
  `EncodeIntent` (`engine/natsio/bus.go:26-135`); `intent_encoding`
  header demux in `onIntent`; whole-batch structural validation
  including the route-field and cap rules; expansion into the existing
  `ArrivedIntent` buffer. Tests: codec round-trip (shared cases with
  the v2 fixed section); malformed/truncated/route-bearing/over-cap
  batch dropped whole; mixed v2+TSIB ordering; mixed NaN batch drops
  only the bad record; header-less TSIB payload behavior as specified
  above (multi-record rejected structurally, single-record backstopped
  by claim filtering); claim filter and hold-last applied to expanded
  records; boundary publish at exactly 20k records; unknown
  `intent_encoding` dropped + counted; determinism suite green with
  zero changes to its expectations. Acceptance: kernel + natsio suites
  green; a seeded scenario records in v2 mode and replays to the same
  CRC as on main.
- **M2 — Driver batching.** Per-tick aggregation, 20k-cap split,
  route-bearing vehicles diverted to standalone v2, `-intent-batch`
  flag. Tests: one batch per cadence tick per controller; expansion
  order preserves record order (engine-assigned seqs monotonic per
  controller); route-update tick sends exactly one complete v2 for that
  vehicle and omits it from the batch; on/off statistical parity on
  paired seeds.
- **M3 — Stress rerun.** M0 scenarios in batch mode. Targets: intent
  msgs/tick ≈ controller count; tick p99 ≤ baseline; **applied-lag
  (source obs tick → `applied_tick`) p50/p99 no worse than baseline** —
  collect-before-publish can shift every intent a tick later while
  engine-side numbers still look improved, so lag percentiles are a
  first-class acceptance metric, and the TSIB header `tick` exists to
  compute them; every recorded batch-mode run replays to a
  byte-identical CRC; batch vs v2 runs agree under the paired-seed
  metrics protocol. Document in `BENCHMARKS.md`.
- **M4 — Contract + ratification.** `asyncapi.yaml` ctlIntent TSIB
  schema (header demux incl. unknown-value rule, record layout,
  route-field rule, cap, tick informational); migration note (deploy
  order engine-first, drivers-second — RECOMMENDED but, per the
  2026-07-26 amendment below, no longer load-bearing: hello capability
  advertisement with v2 fallback covers skew directly); KB index + this
  ADR to Accepted; `--gemini` review round.

## Risks / open questions

- *Emergent claims vs. large batches* — claim racing across few large
  drivers could leave uneven splits; measure in M3 before inventing
  assignment machinery.
- *Late batches* — same semantics as late v2 intents (applied at drain
  tick, hold-last heals); no new failure mode, but M3's applied-lag
  percentiles exist precisely to catch collect-before-publish drift.
- *Driver↔engine version skew* — a TSIB-default driver against a
  pre-TSIB engine: batches are dropped (unknown at best, misparse
  backstopped by claim filtering at worst). Deploy order (engine first)
  plus the hello-version migration note in M4 covers it; capability
  advertisement in hello is deliberately **not** added here — that is
  region-decomposition-era machinery. **(AMENDED 2026-07-26 — see
  below: advertisement WAS added; the skew risk proved worth ~20 lines.)**

## Amendment 2026-07-26 (post-ratification, M4 gate)

**Hello capability advertisement with graceful v2 fallback.** The M4
review gate rejected the "deploy order ONLY" migration story: with
`contract_version` staying 2 and no advertisement, a default-on TSIB
driver could not detect a pre-TSIB engine at all (batches dropped as
malformed v2, no version signal). So the machinery this ADR deferred is
now in, and it is small:

- `HelloReply` gains the additive field `intent_encodings` — `["v2",
  "tsib"]` from this engine onward, omitted by pre-TSIB engines (JSON
  consumers ignore unknown fields in both directions).
- The default driver, when batching is on (the default) and `"tsib"` is
  NOT advertised, logs one line and runs the session in v2 mode — the
  exact `-intent-batch=off` code path, no new config surface.

Deploy order (engine first, drivers second) remains RECOMMENDED but is
no longer load-bearing: a default-on driver against a pre-TSIB engine
now degrades to the v2 stream instead of being silently dropped. The
version-skew risk bullet above is amended accordingly: the
region-decomposition-era deferral ended here because the review rounds
showed the skew risk was worth the ~20 lines. (Contract documented in
`contracts/asyncapi.yaml` at info version 2.5.0 — same additive change
set; engine side `engine/natsio/contract.go` + `bus.go`, driver side
`engine/natsio/driver/driver.go` `applyIntentEncodings`; tests
`TestHelloAdvertisesIntentEncodings`, `TestIntentEncodingFallback`.)
