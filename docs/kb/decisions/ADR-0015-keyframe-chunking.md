# ADR-0015: Chunked keyframes on the record plane

- **Status:** ACCEPTED
- **Date:** 2026-07-24

## Context

ADR-0006 §4–§5 put the run's full-state keyframes on the JetStream log
stream as one message each, under the broker's 1 MiB `max_payload`
(ADR-0002 discipline). The WQ-4 stress test (2026-07-24,
`docs/kb/raw/wq4-stress-test-2026-07-24.md`) demonstrated the wall: at
10,862 live vehicles the tick-6300 keyframe exceeded `max_payload`, the
publish failed, and the recorder's fail-loud contract aborted the run.
Measured keyframe size is ≥92 B/vehicle (worse than the 77 B/veh
microbench estimate in `engine/BENCHMARKS.md`), so any city-scale
recording — the stress test's own goal — is impossible without a change.

Options considered:

1. **Chunk the keyframe into consecutive log messages** (chosen). The
   record plane keeps one stream, one writer, and the existing dedup/OCC
   machinery; chunks ride the same `publish` path with predicted
   sequences, so a chunk group cannot interleave or reorder.
2. **NATS Object Store for keyframes, pointer messages on the log.**
   Adds a second storage system with its own lifecycle, retention, and
   failure modes for a problem that is "a payload is sometimes >1 MiB".
   Rejected as disproportionate today; revisitable if keyframes grow
   past what chunking handles gracefully (multi-hundred-MB states).
3. **Raise `max_payload`.** Moves the wall rather than removing it, and
   inflates the broker's per-message buffers for every subject.
4. **Raise `-keyframe-every` so only tick-0 keyframes exist.** Already
   the documented workaround; it makes every seek re-simulate from
   tick 0, which is why it is a workaround and not a fix.

## Decision

1. **A keyframe whose marshaled payload exceeds `KeyframeChunkMax`
   (recorder config, default 768 KiB — safely under 1 MiB with headers)
   is published as n consecutive messages** on the same
   `ts.{run}.log.keyframe` subject, each carrying a `kf_chunk: "i/n"`
   header (1-based). Chunks of one keyframe share its tick header and
   are consecutive in stream order (single writer, ordered publish,
   per-tick batch await — the invariants ADR-0006 §4 already enforces).
2. **A message without `kf_chunk` is a whole keyframe** — the
   pre-ADR-0015 format and the steady state for any keyframe that fits.
   Existing recordings remain readable unchanged. Old readers
   encountering a chunked keyframe fail loud (chunk 1 parses as a
   truncated TSKF payload — the codec's short-read error sticks; see
   Consequences); no silent misread is possible.
3. **The seek anchor sequence is the keyframe's LAST message** (final
   chunk, or the whole message when unchunked): re-simulation resumes
   at seq+1, after the complete keyframe. `findKeyframe` reassembles
   chunk groups and fails loud on any malformed group (interrupted,
   out-of-sequence, or incomplete at stream end). `indexLogMsgs` counts
   one index entry per keyframe — at its final chunk — so cadence
   derivation and the duplicate-tick corruption check see keyframes,
   not chunks.
4. **SchemaVersion stays 2.** The extension is a record-plane-only,
   additive header; the live plane and controller contract are
   untouched. Old recordings read identically; old binaries reading new
   recordings fail loud (item 2).

## Consequences

- City-scale recordings are unblocked: keyframe size is now bounded by
  disk and memory, not `max_payload`. The WQ-4 stress scenario can be
  re-run past 10.9k vehicles.
- `MarshalState` still builds the full keyframe in memory; at plausible
  fleet sizes (≤100k vehicles ≈ ≤10 MB) this is fine. Streaming marshal
  is unnecessary today.
- **Retention hazard (deferred, 2026-07-24 review):** with
  `RecorderConfig.MaxAge > 0`, JetStream expires messages individually,
  so a chunk group straddling the expiry frontier leaves an orphan tail
  (chunk 1 gone, 2..n present) that `findKeyframe` treats as a malformed
  group — failing ALL seeks, including to intact later keyframes.
  Accepted today: MaxAge is 0 for tests and local runs (recordings are
  bounded by disk, not retention). If MaxAge is ever set, either ignore
  a retention-truncated prefix at the stream head or make chunk-group
  retention atomic.
- Old readers encountering a chunked keyframe fail loud via the TSKF
  codec's sticky short-read error (chunk 1 parses as a truncated
  keyframe) and the player's duplicate-tick index check — not via the
  magic bytes, which chunk 1 does carry.
- Chunking only affects the record plane; replay determinism is
  unchanged (the reassembled bytes are the same TSKF payload the CRC
  chain pins).
- `contracts/asyncapi.yaml` documents the `kf_chunk` header on the
  keyframe channel.
