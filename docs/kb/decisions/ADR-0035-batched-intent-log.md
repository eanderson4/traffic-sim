# ADR-0035: Batched intent log and compressed record storage

- **Status:** PROPOSED — **BLOCKED, do not enable by default.** The storage
  win is measured and real (11.1× end to end), but so is a fidelity cost found
  while measuring it: batching makes the record plane fast enough to change
  which tick an asynchronous controller's intents land on, and a live run stops
  being reproducible run to run. See "Measured outcome" below.
- **Date:** 2026-07-29
- **Amends:** ADR-0006 §4–§5 (record plane) by adding one subject and one
  payload format. ADR-0026's scope note ("the record plane never sees a
  batch") is superseded — that was a deliberate boundary, and this is the
  follow-up it left open.
- **Does NOT touch:** the live plane (`ts.{run}.ctl.>`), TSIB, TSSF, TSKF, the
  scenario schema, the ADR-0012 content hash, or engine behaviour. Nothing
  here is visible to a simulation: the recorder only observes.

## Context

The record plane wrote **one JetStream message per applied intent**, which is
one message per vehicle per tick at 10 Hz. Measured on the shipped `chihalf`
recording (chi-loop-urban, 54,000 ticks, ~5,900 vehicles at peak):

- an 8 MiB stream block holds **34,794 intent messages, 10 CRC, 4 verb** —
  with `CRCEvery: 1` that is 10 ticks, so ~3,479 intent messages per tick;
- that is **~230 bytes per message** for a record whose fixed section is 61
  bytes. The difference is the subject string (`ts.chihalf.log.intent`, 21 B)
  and JetStream's per-record framing, re-paid ~3,500 times a tick;
- the whole recording is **48 GiB**, essentially all of it this. Across
  `data/recordings/` there is ~120 GiB of it.

It grows as vehicles × ticks, so a larger Chicago at 4× the vehicles over the
same 90 minutes is ~200 GiB *per run*.

**The batching already exists one layer up.** ADR-0026 aggregates each tick's
driver intents into a single TSIB frame of fixed 44 B records. `DecodeTSIB`
expands it, the engine arbitrates into `AppliedIntents()`, and `LogTick`
then loops that flat slice calling `logIntent` once per element. So the driver
sends one ~150 KB batch per tick and the recorder turns it back into ~3,500
separate messages, re-paying exactly the framing the batch had eliminated.
ADR-0026 says so explicitly (§ "Everything past that point … is identical to
today. The record plane never sees a batch") — it was scoped out to keep that
change contained, not overlooked.

## Decision

**1. TSLB v1 — one tick's applied intents in one message.**

New subject `ts.{run}.log.intents`. Layout:

```
offset  size  field
     0     4  magic 'T','S','L','B'
     4     1  version = 1
     5     1  flags (reserved, must be 0)
     6     4  count — records in THIS message
    10     8  tick — the applied_tick every record shares
    18     -  count × v2 intent record, byte-identical to log.intent payloads
```

The records are the existing per-message payloads **byte-identical and in the
same order**. `appendLoggedIntent` writes both forms and
`decodeLoggedIntentAt` reads both, so this is a reframing, not a re-encoding —
which makes "the batched log holds exactly what the per-message log held" a
property a test checks by construction rather than one to be argued.

**2. A new subject, not a new shape on the old one.** Old and new recordings
are distinguished by the subject the broker already delivers. Sniffing a magic
number on `log.intent` would mean reading `TSLB` as a u32 against a v2
record's leading `applied_tick` — a collision at tick 1,112,888,660, which is
implausible rather than impossible, and the failure mode is a misparsed record
on the one plane where that is unrecoverable. `SubjectLogAll`
(`ts.{run}.log.>`) already captures the new subject, so the stream config is
unchanged and a v2 recording keeps replaying through the untouched
`log.intent` path.

**3. Splitting, without a chunk index.** A tick whose records outgrow
`IntentBatchMax` (default 768 KiB, the `KeyframeChunkMax` budget and the same
reasoning) is split across several TSLB messages, each independently decodable
and each naming the same tick. Unlike ADR-0015 keyframes — which reassemble
**one** blob and therefore need `i/n` — concatenating consecutive messages'
records in stream order *is* the original order, because stream order is the
application order (ADR-0006 §4).

**4. S2 storage compression on the run's stream.** `Compression:
nats.S2Compression` on the `StreamConfig`. This is a storage-layer setting:
payloads, subjects and every reader are untouched, so it is **not** a change
to the message contract — only to how the file store persists blocks.

**5. Both are flags, defaulting on.** `serve -log-batch=false` restores the
per-message record plane; `-store-compress=false` turns compression off. In
`RecorderConfig` they are `UnbatchedIntentLog` and `UncompressedStore`, named
for the non-default so the zero value is the new behaviour — the
`DropEngineIntentLog` convention.

**6. `AddStream` falls back to `UpdateStream` on name-already-in-use.** The
recorder *adopts* an existing stream (serve's `checkFreshRecording` permits an
empty one), and `AddStream` refuses to adopt when the config differs. Adding
`Compression` is the first config change since that path was written, so
without this it turns adoption into a hard failure.

## Alternatives rejected

**Hoist the repeated `applied_tick` and controller name out of each record**
(a controller-index table and a route indirection, as TSIB does with its fixed
44 B records). Rejected on measured grounds: those repeated bytes are exactly
what a compressor eats. On a real stream block, **gzip -3 gets 6.2× and
zstd -3 gets 7.2×**. Hoisting would buy perhaps 20% on top of compression
while introducing new invariants — an index table and an indirection — on the
one plane where a decode bug is unrecoverable, since the record *is* the run.
Density is available later if the numbers ever justify it; the version byte
exists for that.

**Reuse TSIB for the log.** Rejected: TSIB cannot carry what the log must.
ADR-0026 §"Route fields are forbidden in batch records" forbids routes, and
the log carries `intentFlagRouteSet` with a route string; TSIB also has no
`Held`, `Superseded`, `Grant`, `Seq` or controller name, all of which the
arbitrated record needs. A batched log format is *not* TSIB verbatim, which is
precisely why it wants an ADR rather than a quick edit.

**Skip recording entirely for measurement runs.** Still worth doing and
orthogonal — nothing downstream of a sweep reads the store (`fwsweep.sh` and
the A/B harness both delete it), yet the run pays for it in disk *and* wall
time, since `LogTick` ends in `awaitBatch` and blocks on pubacks. That is a
separate change with a separate risk profile (it must refuse to combine with
anything needing replay), so it is not bundled here.

## Consequences

Good: the record shrinks by the framing ratio — ~3,500 messages per tick
become 1 — and compression takes another 3–7× on top, without touching a
single reader's understanding of what an intent is. Old recordings replay
unchanged. Both changes are revertible by a flag, not a rebuild.

Costs and risks:

- **Two log formats to maintain.** Mitigated by sharing one record codec
  between them, so the surface that can diverge is the framing only.
- **A reader that predates this cannot read a new recording.** It sees an
  unknown subject and — depending on the reader — ignores it, yielding a run
  with no intents. In-repo readers are all updated (`MaterializeRunRecord`,
  `indexLogMsgs`, `logcursor`); `-log-batch=false` writes a recording an older
  reader can consume. See the migration note.
- **Compression costs write-side CPU.** Unmeasured under load at the time of
  writing; `-store-compress=false` is the escape hatch.
- **This does not fix the harder scaling wall, which is RAM.** A 54,000-tick
  chi-loop-urban run was OOM-killed at tick 38,200 on a 123 GB machine,
  because the engine's whole-run **in-memory** intent log retains ~225M
  `TickedIntent` structs by then. `-intent-log=false` drops it and is a
  requirement at that horizon, not a tuning knob. `scripts/whatif.py` passes
  it; `scripts/chicago/fwsweep.sh` does not. Storage and memory are separate
  ceilings and the memory one binds first.

## Measured outcome (2026-07-29)

6,000 ticks of `chi-loop-urban` (the A/B base demand), `-drivers 8`,
`-intent-log=false`, one config per run, store measured on disk:

| config | store | vs. before |
|---|---|---|
| per-message, uncompressed (before) | 1,214,946,593 B | — |
| **batched**, uncompressed | 354,768,830 B | **3.42×** |
| **batched + S2** | 109,489,567 B | **11.10×** |

So batching is the larger single factor and S2 adds another 3.24× on top.
Stream blocks fell from 145 to 43. Extrapolated, the 48 GiB `chihalf`
recording would be ~4.3 GiB.

**And the blocker.** Repeating the identical run per config, comparing the
engine's own end-of-run counters:

| config | lanechanges over repeated runs | reproducible |
|---|---|---|
| per-message, uncompressed | 2586, 2586, 2586, 2586, 2586 | **yes (5/5)** |
| batched, uncompressed | 2565, 2586, 2580 | **no** |
| batched + S2 | 2562, 2577, 2588 | **no** |

`despawned` moves with it (180–182 against a stable 181). The pre-change
format is bit-stable across five runs; both new configs vary across three
each, so this is **introduced here, not a pre-existing property** — which is
what the repeated pre-change runs were done to establish, after an initial
reading of "compression changed the simulation" turned out to be the wrong
attribution.

The mechanism is speed, not correctness. `LogTick` ends in `awaitBatch`, so
the tick loop blocks on pubacks; going from ~3,500 publishes per tick to one
makes a tick complete far sooner. Under `-pace 0` there is no per-tick barrier
against the driver — only the attach barrier at tick 0 — so the engine races
ahead and a controller's TSIB for tick N can arrive after tick N was stepped,
landing the intents a tick later and taking hold-last re-issues
(`logFlagHeld`) instead. The old format was slow enough that the driver always
kept up. Nothing about the record is wrong: whatever happened is recorded
faithfully and replays exactly, CRC-verified. What breaks is the comparability
of two LIVE runs, which is the foundation of the paired-seed A/B protocol.

This must be resolved before either flag defaults on. Candidate directions,
none yet evaluated: make the engine wait for each controller's cadence-due
intents under `-pace 0` (a real per-tick barrier, which is arguably the right
fix independent of this ADR and would make live runs reproducible for the
first time); or decouple `awaitBatch` from the tick loop so record-plane speed
cannot influence the engine/driver race in either direction.

## Migration note

- **Reading:** every in-repo reader handles both subjects. A recording written
  before this change is byte-for-byte what it was and replays through the
  `log.intent` path; a recording written after it carries `log.intents`.
- **Writing:** `serve -log-batch=false` reproduces the old record plane
  exactly, for an external reader or for A/B measurement.
- **No re-recording is required or offered.** The 48 GiB `chihalf` recording
  and the ~120 GiB in `data/recordings/` stay readable as they are; they do
  not shrink retroactively, and reclaiming that space means re-recording those
  runs, which is a separate decision (#24, #62 already cover re-baking).
- **Compression applies to blocks written from here on.** An existing stream
  is not rewritten.
- **Nothing in the ADR-0012 content hash changes**, so scenario identity and
  paired-seed A/B comparison are unaffected.

## See also

- [ADR-0006](ADR-0006-nats-message-contract.md) §4–§5 — the record plane and
  seek semantics this amends
- [ADR-0026](ADR-0026-batched-intents.md) — TSIB, the live-path batching whose
  scope note this follows up
- [ADR-0015](ADR-0015-keyframe-chunking.md) — keyframe chunking, and why TSLB
  splitting deliberately does *not* copy its `i/n` framing
- [ADR-0024](ADR-0024-bounded-memory-replay-reader.md) — the bounded-memory
  reader, and the in-memory log `-intent-log=false` drops
