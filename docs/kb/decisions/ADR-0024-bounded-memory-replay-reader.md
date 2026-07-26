# ADR-0024: Bounded-memory replay reading — the log cursor

- **Status:** ACCEPTED (pending external review round)
- **Date:** 2026-07-25

## Context

A 30 sim-minute Chicago cut could not be opened. The scenario manifest
`data/scenarios/chi-loop-od-peak/scenario.yaml` records the symptom in its
own header — "the 30-minute cut of this scenario recorded 9.1 GB, and replay
materializes the whole store before it serves, so the demo could not be
opened on a normal box" — and had already been cut back to `ticks: 9000`
(15 sim minutes) as a *storage budget*. That framing was wrong, and the
wrongness is what this ADR corrects.

### What the record actually contains

Measured directly on `data/recordings/chi-loop-od-peak` (9,000 ticks,
2.3 GB on disk), counting subject occurrences in the JetStream blocks:

| subject | messages |
|---|---|
| `log.intent` | **9,973,269** |
| `log.crc` | 9,000 |
| `log.keyframe` | 91 |
| `log.verb` | 2,144 |
| `log.event` | 0 |

Intents are **99.9%** of the record: ~1,108 per tick, which is one intent
per claimed vehicle per tick. That is not a defect — the default driver
attaches with `CadenceTicks: 1` (`natsio/driver/driver.go:167`) and
publishes an `Accel` intent for every ego on every observation
(`driver.go:335-350`), because the IDM decision is the controller's job
under the ADR-0008 MMU split. The kernel is *supposed* to be told what to
do every tick.

At ~230 bytes per stored message against a ~40-byte binary payload
(`EncodeIntent`, `bus.go:58-88`), most of the on-disk size is JetStream
per-message framing. That is a real inefficiency, but it is **not** what
made the demo unopenable.

### The actual wall was the reader

`NewPlayer` called `fetchFrom(js, stream, run, 1)` — every log message from
sequence 1 into a `[]*nats.Msg` — and then `indexLogMsgs` built
`map[uint64][]KeyedIntent` and friends from it, with **both alive at the
same time**. Ten million `*nats.Msg` (subject string, header map, payload)
plus a ten-million-entry index is several times the 2.3 GB record in live Go
objects. Doubling the horizon to 18,000 ticks doubles it again.

So the constraint was never "the recording is too big to store". It was
"the reader insists on holding all of it at once", and the fix does not have
to touch the record at all.

## Decision

### 1. A forward-only cursor replaces the materialized index (in the Player)

`natsio/logcursor.go` adds `logCursor`: one ephemeral pull consumer held
open over `ts.{run}.log.>`, buffering exactly the messages of the tick being
served. `records(tick)` returns a `tickRecords` — that tick's intents and
verbs in stream order, plus its rolling CRC — and reads forward only.

This is sound because **playback is monotonic in tick**. The whole index was
never needed; it was a convenience that happened to be affordable at
corridor scale and stopped being affordable at city scale.

Peak reader memory now scales with *one tick*, not with the length of the
recording. Per-tick cost is unchanged: the same messages, decoded once.

### 2. Seeks reposition the cursor, they do not rebuild an index

`Player.seek` already located the nearest keyframe ≤ target via
`findKeyframe` and restored from it. It now also calls `cur.reset(kf.seq+1)`
— the keyframe's *last* message sequence plus one, which is exactly where
`ReplayFromStream` resumes. Seeking is the only way the playhead moves
backwards, so it is the only place the cursor is ever repositioned.

### 3. Two construction-time facts are read cheaply instead of scanned

The full index also supplied two things the Player needs at open:

- **the highest logged tick** (`idx.lastTick`), used to cap `endTick` when a
  run was killed or truncated. Now `lastLoggedTick`: the log is written in
  non-decreasing tick order, so this is the tick header of the stream's last
  message — one fetch.
- **the duplicate-keyframe corruption check** (a run id recorded twice into
  one store yields two keyframes at the same tick). Now
  `firstKeyframeTicks`, which reads only the sparse keyframe subject — ~1
  message per `KeyframeEvery` ticks — and stops after two complete
  keyframes.

### 4. Batches are bounded by remaining sequence, not by a fixed count

`Fetch(n, MaxWait)` blocks until it has **n** messages or the timeout
elapses. A fixed batch size therefore stalls for the full `MaxWait` on the
tail of every recording — which is not merely slow, it freezes the Player's
Run goroutine and with it `/pause`, `/seek`, `/speed` and shutdown. This was
caught by `TestPlayerSlowSpeedControlsResponsive` during implementation, not
reasoned about in advance.

The cursor therefore asks for `min(cursorBatch, lastSeq-curSeq)`. This is
exact because the log stream's only configured subject is `SubjectLogAll`
(`recorder.go:105-112`), so every sequence in the stream matches the
cursor's filter. `fetchAll` avoids the same trap by bounding on a known
message count; the cursor bounds on sequence arithmetic instead.

End of stream is decided by the sequence bound, **never** by a short batch.
A timeout with sequences still outstanding is reported as an error rather
than treated as the end, because silently truncating a replay would present
a partial run as a complete one.

### 5. Read faults propagate; they are not divergences

`stepTick` returns an error now. A failed log read is not a CRC divergence:
stepping anyway would invent an uncontrolled tick, and retrying in place
would spin forever inside `seek`'s re-sim loop (which advances only on a
successful step). Forward playback logs and stops; `seek` fails the control
request.

The existing LOUD-AND-CONTINUE divergence policy for *CRC* mismatches is
unchanged, as is `ReplayFromStream` as the strict audit path.

### 6. The whole-run in-memory intent log becomes optional

Separately from the reader: `Engine.IntentLog` is an unconditional append
(`intent.go:142`) retained for the entire run, and there was **no** way to
turn it off — a `-intent-log` flag was assumed to exist during planning and
does not. It grew ~1 GB per 1,000 ticks on chi-loop and made the 3-hour
scenario (`chi-loop-od`, `ticks: 108000`) unrunnable long before it could
write its metrics, which are only emitted at run end.

`Engine.DropIntentLog` (zero value = keep, so nothing changes unless asked)
now guards the append, plumbed through `RecorderConfig.DropEngineIntentLog`
and exposed as `serve -intent-log=false`.

This is safe because **the recorder does not read `IntentLog`** — it reads
`AppliedIntents()`, the per-tick slice reused each Step
(`recorder.go:130`). The durable JetStream record is byte-identical either
way. What is lost is the in-memory `RunLog`, which a headless metrics run
never looks at.

The option deliberately rides `RecorderConfig` rather than `RunSpec`:
nothing in `RecorderConfig` is recorded, so it cannot leak into the record
or the ADR-0012 content hash.

## Consequences

- **The record format is untouched.** Every existing recording replays
  exactly as before. No migration note, no contract change, no re-recording
  — which is the whole reason this shape was chosen over compaction.
- **`natsio` test wall-clock fell from 53.8 s to 25.0 s**, and the
  player/replay subset from 53.8 s to 11.8 s, because every player
  construction in the suite was materializing its whole stream.
- **`indexLogMsgs`, `fetchFrom` and `MaterializeRunRecord` remain** for
  `ReplayFromStream` and the audit/test views. Those are strict, bounded,
  offline paths and are deliberately not converted here. **They keep the
  same memory profile**, so a full-record audit of a 30-minute city cut is
  still not something to run casually — that is a known, accepted gap, not
  an oversight.
- **On-disk size is untouched.** A 30-minute city cut is still ~4.6 GB.
  Batching a tick's intents into one JetStream message would cut the framing
  overhead ~1,000× and is the obvious next lever, but it *is* a record
  format change and would need its own ADR and migration note. Not done
  under time pressure, and not needed for playability.
- **ADR-0023's `engine/cmd/bake` inherits the fix.** That ADR designs its
  offline re-simulation around "keyframe restore + per-tick re-enqueue from
  the log index (player.go:416-440, 461-495)" — the exact code path replaced
  here. Baking a city-scale run would have hit this same wall. `natsio/
  resim.go` already names the cursor as the shared re-sim core.

### Deferred / not addressed

- **Tick-batched intent log messages** (the ~1,000× framing win) — record
  format change, needs its own ADR.
- **`findKeyframe` still fetches every keyframe message** on each seek. At
  ~150 KB per keyframe and ~180 keyframes for 30 minutes that is ~30 MB —
  measured as noise against the intent stream, so left alone.
- **`MaxAge` expiry would break the sequence arithmetic** in §4 by removing
  messages mid-stream. Recordings set `MaxAge: 0`. The failure mode is a
  reported error, not silent truncation.

## Verification

- `TestLogCursorMatchesFullIndex` walks the cursor tick by tick across a
  whole recording and asserts it yields exactly what `indexLogMsgs`
  bucketed — intents in order, verbs in order, CRC presence and value — with
  an explicit non-vacuity guard (the first draft passed vacuously against a
  record containing zero intents, because `RunLive` with no controller
  attached produces none; the guard caught it and the fixture now runs two
  scripted controllers).
- `TestLogCursorResetRewinds` pins the seek path: re-reading a span after
  `reset` yields identical records.
- `TestLastLoggedTickMatchesIndex` pins the cheap tail read against the full
  index's `lastTick`.
- Full suite green (`go test ./...`), `gofmt` and `go vet` clean.
