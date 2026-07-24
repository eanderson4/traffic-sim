# ADR-0016: Chunked signal tables and request/reply catch-up on the live plane

- **Status:** ACCEPTED
- **Date:** 2026-07-24

## Context

The M9 signal-program table (ADR-0006 addendum, ADR-0011) is one core
NATS message per publish on `ts.{run}.state.sig`. At city scale it is not
small: sf-lean measures 2.1 MB, la-lean 7.3 MB — past the broker's 1 MiB
default `max_payload` and, worse, undeliverable to a busy browser: a tab
mid-parse drains too slowly, the server's slow-consumer policy drops the
7.3 MB frame silently, and every signal head in the city never lights
(the failure that motivated this ADR — diagnosed as zero snapshots, then
as the dropped table).

The first fix shipped was raising `MaxPayload` to 64 MB in serve and
replay. That was the wrong shape: it moves the wall (TSSF snapshots grow
with fleet size and will meet any cap), it inflates per-message buffers
broker-wide, and it does nothing for the slow-consumer drop, which is a
drain-rate problem, not a cap problem. ADR-0002's discipline — bulk
artifacts ride chunked, manifest-referenced messages, never one giant
core subject — was right all along; the table had simply never been big
before. Two design reviews (2026-07-24) settled the shape below.

Options considered:

1. **Chunk on the existing subject with a header coordinate** (chosen) —
   the ADR-0015 keyframe idiom applied to the live plane, plus a
   request/reply resync so a client pulls the table when IT is ready.
2. **Raise `max_payload` only.** Rejected (above): moves the wall, keeps
   the silent drop, violates the standing ADR-0002 rule in letter.
3. **JetStream/KV/Object Store for the table.** Guaranteed retrieval at
   the price of a second storage system and its lifecycle for a table
   that is republished every 20 ticks anyway; disproportionate today.
4. **Per-program subjects.** Thousands of subscriptions per client for
   la-lean; excessive machinery.

## Decision

1. **Chunk metadata rides a NATS header; the payload does not change.**
   A table whose greedy-packed size exceeds `SigChunkMax` (768 KiB, the
   ADR-0015 chunk size) is published as n consecutive messages on
   `ts.{run}.state.sig`, each a COMPLETE v1-layout TSSG frame
   (`program_count` = programs in THIS chunk — every chunk parses
   standalone with the unmodified v1 decoder) carrying a
   `sig_chunk: "i/n"` header (1-based, mirroring `kf_chunk`). A frame
   with NO `sig_chunk` header is the whole table — v1 back-compat for
   free; `schema_version` stays 1. Programs greedy-pack in file order
   (a pure function); a single program larger than the target rides its
   own oversized chunk and is logged.
2. **Encode once, publish from cache.** The chunk set is split and
   encoded once when the live plane attaches (`NewPublishBus`); the
   run-start publish, the 20-tick catch-up republication, and the
   request/reply responder all send the cached slices with only the
   8-byte payload tick patched (a copy per chunk — never a re-encode).
3. **Request/reply catch-up:** new subject `ts.{run}.state.sig.req`
   (queue-subscribed by the run; the replay player answers the same way
   on its `-replay` subjects). A request (empty payload) is answered with
   the full cached chunk set on the reply inbox. Clients request on
   attach and on a detected chunk gap (a partial accumulation reset, or
   ~15 s without completion), converting push-and-pray into
   pull-when-ready — the actual fix for the busy-tab drop, and for
   late-join latency at slow tick rates (a 20-tick round is >1 min at
   la-lean's pace).
4. **Client accumulator rules:** chunks of one generation arrive in
   publish order (NATS per-publisher ordering); collect 1..n. Any gap,
   index regression, or chunk-count change resets the partial
   accumulation and triggers a resync request. A generation is installed
   only when COMPLETE (the old table survives an incomplete new one).
   The tick is never a generation identity (a paused replay republishes
   the same tick).
5. **Paused replay stops re-broadcasting the table.** The ~1 Hz paused
   republication keeps the small snapshot only; a full city table every
   second is a firehose aimed at exactly the busy tabs it targets. Paused
   attaches resync via the request path (item 3).
6. **Loud size failures.** Snapshot publishes get the same rate-limited
   stderr logging the signal table already had (first 3 per run), plus a
   one-time log when any frame crosses 1 MiB, naming the size — the
   silent-`pubErrs` bug class must not regrow.
7. **`MaxPayload` 64 MB → 4 MB** in serve and replay. This ADR explicitly
   AMENDS ADR-0002/ADR-0006's "never raise the server limit": the
   per-message discipline is 1 MiB via chunking everywhere (TSSG chunks,
   ADR-0015 keyframes, the GeoJSON HTTP manifest); the 4 MiB broker cap
   is HEADROOM for big-fleet TSSF snapshots (~1.2 MB at 50k vehicles),
   not a design allowance.
8. **The TSSF wall is documented, not solved.** Snapshots grow
   ~24 B/vehicle at 10 Hz — past ~40k vehicles a browser cannot drink
   the stream at any cap. Interest windows (per-controller areas of
   interest, ADR-0006 §7 doctrine) or size-adaptive decimation are the
   answer; they are future work, noted here so the wall is on record.

## Consequences

- City-scale signal tables reach busy browsers: the wire never carries
  more than ~768 KiB per message, a slow-consumer drop costs one chunk
  and one resync request, and the req/reply path converges late joiners
  in one round-trip regardless of tick rate.
- Old clients decode new streams unchanged (no-header = whole table; old
  binaries simply never subscribe `state.sig.req`). Old PUBLISHERS are
  the only v1-only producers left — recordings contain no TSSG (the
  player re-derives the table, so old recordings replay chunked under a
  new binary).
- Republication cost at city scale drops from re-encoding megabytes
  every 20 ticks to a memcpy per chunk; the responder serves from the
  same cache without touching the run loop.
- TSSG chunks are consecutive per publish (single publisher, ordered
  core NATS) but NOT retained: a dropped chunk abandons the round by
  design (item 4), and the request path is the recovery. Core NATS
  at-most-once semantics are unchanged.
- `contracts/asyncapi.yaml` documents the `sig_chunk` header on
  `state.sig` and the new `state.sig.req` channel.
- The `pubErrs` counters remain, now with loud first-3 logging on both
  frame types; the 1 MiB watermark log fires once per run per type.
