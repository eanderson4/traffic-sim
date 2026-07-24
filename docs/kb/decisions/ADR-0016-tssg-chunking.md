# ADR-0016: Chunked signal table (TSSG) + request-reply resync on the live plane

- **Status:** PROPOSED
- **Date:** 2026-07-24 (design settled by external round: Claude Fable + GPT-5.6-sol)

## Context

ADR-0006's M9 addendum put the fixed-time signal-program table on
`ts.{run}.state.sig` (TSSG v1) as ONE message, published at run start and
republished every `signalCatchUpEvery` (20) ticks — designed when the table
was "a few hundred bytes" (the `engine/natsio/run.go` comment). City-scale
networks broke every part of that assumption on 2026-07-24 (theme-session LA
bring-up):

- Measured tables: SF ≈ 2.1 MB, full LA ≈ 7.3 MB (the `engine/cmd/serve`
  comment claiming 3.5/10 MB is wrong on both).
- The stopgap raised server `max_payload` to 64 MB — violating ADR-0006 §5's
  "never raise the server limit" and ADR-0002's bulk-artifact doctrine — and
  it did not even work: a busy browser tab (parsing the 1.4M-feature LA
  GeoJSON at ~2 GB heap) becomes a slow consumer and the 7.3 MB delivery is
  silently dropped. The 20-tick rebroadcast is wall-clock-blind: ~71 s per
  round at LA's 0.28 ticks/s, and a tab missing one round waits a full round
  to retry. (Probe: 20 min in a loaded tab, no table; one-shot node client
  received it in 143 s.)
- Paused replay republishes the full table every 1 s
  (`pausedRepublishInterval`) — a 7.3 MB/s firehose per subscriber aimed
  precisely at tabs attaching mid-pause.
- `PublishSnapshot` fails silently (increments `pubErrs`, no log) — the same
  bug class as the silent `PublishSignals` failure that made this incident
  take days to find.

The external round's convergence: chunk like ADR-0015, but chunking alone is
push-and-pray — ten 768 KiB chunks are the same 7.3 MB at the same stalled
socket. The piece that fixes the busy tab is **request-reply catch-up**, which
is also ADR-0006 §6's own doctrine: "late joiners resync from KV/last-per-
subject, **not backlog**".

Options considered:

1. **Chunk on the same subject + request-reply resync** (chosen). Mirrors
   ADR-0015's idiom on the live plane; pull-on-demand replaces the
   wall-clock-blind rebroadcast.
2. **Keep the periodic rebroadcast as the only resync.** Rejected: at healthy
   tick rates it is itself a firehose (7.3 MB every 2 s ≈ 3.6 MB/s per
   subscriber at 10 ticks/s) and it cannot help a tab that is busy exactly
   when the burst arrives.
3. **HTTP fetch from demosrv.** Rejected: ADR-0002 — the sim planes are
   NATS-only. The static network GeoJSON rides HTTP because it is client-local
   scenery; the signal table is live-plane run state.
4. **JetStream / KV / Object Store for the table.** Rejected as
   disproportionate (ADR-0015 option 2's argument): the table is re-derivable
   live state, not a durable artifact.
5. **Per-program subjects.** Rejected: thousands of subjects per run, and it
   shatters the atomic table swap.
6. **Raise `max_payload`, keep whole-table messages.** Rejected: moves the
   wall, inflates broker buffers for every subject, and leaves the
   slow-consumer fragility untouched — measured, not hypothetical.

## Decision

1. **Chunked TSSG.** A marshaled table larger than `SignalChunkMax` (default
   768 KiB — same rationale as ADR-0015's `KeyframeChunkMax`: safely under
   1 MiB with headers) is published as n consecutive messages on the same
   `ts.{run}.state.sig` subject, each carrying NATS headers
   `sig_chunk: "i/n"` (1-based, mirroring `kf_chunk`) and `sig_gen: "<hex>"` —
   a truncated SHA-256 (8 bytes, 16 hex chars) of the FULL encoded table,
   identifying the generation. **A message without `sig_chunk` is a whole
   table**: the pre-ADR-0016 format and the steady state for any table that
   fits. No `schema_version` bump; the v1 payload layout is untouched.
2. **Each chunk is a fully valid TSSG v1 frame** whose `program_count` counts
   the programs in that chunk. Programs are never split across chunks; the
   pack is a deterministic greedy fill in the encoder's canonical program
   order (file order / link-index sort — already deterministic). Completeness
   is proven by the headers (`i/n` with matching `sig_gen`), not the payload.
   Rejected alternative: ADR-0015-style byte-range slicing of one frame
   (concatenate-then-parse), which makes old readers fail loud on chunk 1.
   TSSG is live-plane and never recorded — the replay player re-derives it —
   so ADR-0015's old-reader-of-durable-artifact argument does not bind, and
   per-chunk validity keeps every message independently parseable and
   testable. The consequence is recorded below.
3. **Oversized single program** (one program larger than `SignalChunkMax`)
   gets its own chunk; the encoder fails loud (program id, size) if that
   exceeds the server cap. Degenerate today — programs are tens of bytes plus
   state strings — so this is asserted, not engineered.
4. **Publish once at run start; resync is pull, not push.** The 20-tick
   periodic rebroadcast is REMOVED. New contract-plane channel
   `ts.{run}.state.sig.req` (request/reply, empty request payload): the server
   answers on the reply inbox with the cached encoded chunk set — encoded ONCE
   per generation at table build, never re-encoded per request, never touching
   the tick loop. This is ADR-0006 §6's doctrine applied to the signal plane
   and mirrors the existing hello/verb request-reply shapes in asyncapi.
5. **Client accumulation rule.** Clients MUST request the table on attach and
   MUST re-request (with backoff) when a chunk group stays incomplete past a
   timeout. Partial accumulation resets on any `sig_chunk` gap, regression, or
   `sig_gen` change mid-group (NATS per-publisher ordering makes interleave
   impossible; mid-group drops are not). The table swaps atomically when all n
   chunks of one generation are present; the old table stays live until then.
   Tick MUST NOT be used as generation identity — paused replay republishes
   frozen ticks, and mutable mid-run tables are queued work; `sig_gen` is the
   identity.
6. **Replay/pause.** The player subscribes the same
   `...-replay.state.sig.req` and serves from the same cache. The paused 1 Hz
   table republish is removed; the 1 Hz paused cadence remains for the small
   TSSF snapshot only.
7. **Loud publish failures, both live frames.** `PublishSignals` AND
   `PublishSnapshot` log publish errors (rate-limited) and each logs a size
   high-watermark per run. The silent-counter bug class that made this
   incident take days to diagnose ends here.
8. **`max_payload`: 64 MB → 4 MB, as headroom; the doctrine is amended.**
   ADR-0006 §5's "never raise the server limit" is superseded by: **per-message
   payloads stay under 1 MiB BY DESIGN via chunking on both planes; the server
   cap exists as defensive headroom for not-yet-bounded frames and is not a
   design allowance.** 4 MB covers TSSF snapshots to ~174k vehicles
   (24 B/vehicle) with margin. ADR-0002's bulk-artifact doctrine stands:
   chunked ≤1 MiB messages ARE the compliance shape; no designed message may
   approach the cap.
9. **Contract doc.** `contracts/asyncapi.yaml`: `sig_chunk`/`sig_gen` headers
   on the `state.sig` channel; new `state.sig.req` channel (request/reply);
   per-frame size-bound table (TSSF: fleet-bounded, documented wall; TSSG:
   chunked; TSKF: chunked per ADR-0015). Also fix the wrong SF/LA size numbers
   in the `engine/cmd/serve` comment (measured 2.1 / 7.3 MB).

## Consequences

- The 64 MB stopgap is retired; the largest DESIGNED message on any plane is
  again < 1 MiB. LA's table ≈ 10 chunks, SF's ≈ 3.
- **Old viz clients accept a partial table silently.** A pre-ADR-0016 client
  ignores unknown headers and parses chunk 1 of n as a complete (partial) v1
  table — the deliberate price of decision 2. No such clients exist outside
  this repo (local-first, no published consumers; the viz ships in the same
  commit). Accepted; revisit if a third-party client ever ships.
- Busy-tab slow-consumer drops are fixed by the pull path (decisions 4–5), not
  by chunking: chunking alone still bursts 7.3 MB at a stalled socket.
- **TSSF snapshots are the remaining unbounded live frame** (~1.2 MB at 50k
  vehicles; at 10 Hz that is ~15 MB/s per subscriber — undeliverable to a
  browser regardless of any cap). Not fixed here; the wall is now loud
  (decision 7) instead of silent. Bounded later via interest windows
  (ADR-0006 §7 doctrine) or size-adaptive decimation — its own ADR.
- **TSOB is the next unbounded frame after TSSF** (policy-context observations
  ≈ 392 B per claimed ego plus routes; claim capacity and neighbor cap have no
  contract-level byte ceiling — a ~10k-vehicle controller can exceed 4 MB).
  Noted, not fixed; a contract-level byte ceiling belongs to the
  controller-contract hardening pass.
- Run-start-only publish means a client that never requests never sees a
  table; "request on attach, re-request on absence" is now contract, not
  courtesy.
- Mutable mid-run signal tables (external signal control, ADR-0011 D1) become
  event-driven generation changes: new table → new `sig_gen` → publish. The
  chunk/generation machinery needs no change when that lands.
- Determinism/replay: untouched. TSSG is never recorded; the player re-derives
  and re-serves it, and the CRC chain never sees these bytes.
