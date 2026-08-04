package natsio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// recorder.go — the record plane (ADR-0006 §4–§5): one JetStream stream per
// run (LimitsPolicy, R=1 file storage) capturing ts.{run}.log.>. The engine
// run loop is the SOLE WRITER. Every write carries Nats-Msg-Id
// {run}:{tick}:{seq} (retry-safe dedup) and Nats-Expected-Last-Sequence
// (broker-enforced single-writer assertion — a competing writer moves the
// stream sequence and our next write fails). Pubacks are published async
// and awaited per tick-batch; a failed log write aborts the run loudly.

// RecorderConfig tunes the per-run log stream.
type RecorderConfig struct {
	// KeyframeEvery is the full-state keyframe cadence in ticks (default 100).
	KeyframeEvery uint64
	// CRCEvery is the rolling-CRC log cadence in ticks (default 1: every tick).
	CRCEvery uint64
	// AckWait bounds each tick-batch's puback wait (default 2 s). Exceeding
	// it is a failed log write: the run aborts.
	AckWait time.Duration
	// MaxAge bounds stream retention; 0 keeps everything (tests, local runs).
	MaxAge time.Duration
	// DropEngineIntentLog stops the engine retaining its whole-run
	// IntentLog (engine.Engine.DropIntentLog). It rides here because this
	// is the run-tuning struct RunLive already threads and — unlike
	// RunSpec — nothing in it is recorded, so the option cannot leak into
	// the record or the ADR-0012 content hash. The durable log is
	// unaffected either way: the recorder reads AppliedIntents(), the
	// per-tick slice, never IntentLog. What it costs is the in-memory
	// RunLog a headless metrics run never reads (ADR-0024).
	DropEngineIntentLog bool
	// KeyframeChunkMax bounds one keyframe log message's payload in bytes
	// (default 768 KiB — safely under the broker's 1 MiB max_payload with
	// headers). A larger keyframe is split into chunk messages reassembled
	// by the reader (ADR-0015).
	KeyframeChunkMax int
	// UnbatchedIntentLog restores the pre-ADR-0035 record plane: one message
	// per applied intent on ts.{run}.log.intent instead of one TSLB batch per
	// tick on ts.{run}.log.intents. Named for the non-default so the zero
	// value is the batched form, matching DropEngineIntentLog's convention.
	// Kept for A/B measurement and for writing a recording an older reader
	// can consume.
	UnbatchedIntentLog bool
	// IntentBatchMax bounds one TSLB message's payload in bytes (default
	// 768 KiB, same budget and same reasoning as KeyframeChunkMax). A tick
	// with more intents than fit is split across several batch messages.
	IntentBatchMax int
	// UncompressedStore turns OFF JetStream S2 storage compression for the
	// run's stream. Named for the non-default: compression is on, because the
	// log is dominated by a repeated subject, a repeated applied_tick and a
	// repeated controller name per record — measured 6.2x (gzip -3) and 7.2x
	// (zstd -3) on a real stream block. Set this if the write-side CPU cost
	// ever matters more than the bytes; it changes only how the file store
	// persists messages, never their content.
	UncompressedStore bool
}

func (c *RecorderConfig) withDefaults() RecorderConfig {
	out := *c
	if out.KeyframeEvery == 0 {
		out.KeyframeEvery = 100
	}
	if out.KeyframeChunkMax == 0 {
		out.KeyframeChunkMax = 768 * 1024
	}
	if out.IntentBatchMax == 0 {
		out.IntentBatchMax = 768 * 1024
	}
	if out.CRCEvery == 0 {
		out.CRCEvery = 1
	}
	if out.AckWait == 0 {
		out.AckWait = 2 * time.Second
	}
	return out
}

// Recorder writes a run's arbitrated log to its JetStream stream.
type Recorder struct {
	js     nats.JetStreamContext
	run    string
	cfg    RecorderConfig
	stream string

	lastSeq uint64 // last stream sequence we published (OCC cursor)
	batch   []batchEntry

	// Counters (observability). IntentsWritten counts INTENTS either way, so
	// it stays comparable across the format change; IntentBatchesWritten is
	// the message count, and their ratio is the framing saved.
	IntentsWritten       uint64
	IntentBatchesWritten uint64
	KeyframesWritten     uint64
	CRCsWritten          uint64
	EventsWritten        uint64
	VerbsWritten         uint64
}

// Record-plane-only intent flag bits (the wire bits 0–4 are shared with the
// live intent frame, bus.go).
const (
	logFlagHeld       = 1 << 5 // hold-last re-issue synthesized by the contract layer
	logFlagSuperseded = 1 << 6 // lost the same-tick tie-break: recorded, not applied
)

// headerKeyframeChunk marks a keyframe log message as fragment i of n of one
// keyframe (value "i/n", 1-based — ADR-0015). Absent: the message IS the
// whole keyframe (the pre-chunking format, and any keyframe that fits).
const headerKeyframeChunk = "kf_chunk"

// NewRecorder creates (or adopts) the per-run stream.
func NewRecorder(js nats.JetStreamContext, run string, cfg RecorderConfig) (*Recorder, error) {
	if err := validRunID(run); err != nil {
		return nil, err
	}
	r := &Recorder{js: js, run: run, cfg: cfg.withDefaults(), stream: StreamName(run)}
	if r.cfg.KeyframeChunkMax < 1 {
		return nil, fmt.Errorf("KeyframeChunkMax must be ≥ 1, got %d", r.cfg.KeyframeChunkMax)
	}
	if r.cfg.IntentBatchMax < loggedIntentMax {
		// Below one maximal record the budget is meaningless: the first
		// record of a batch always goes in (a batch must hold at least one),
		// so every message would exceed the configured "maximum".
		return nil, fmt.Errorf("IntentBatchMax must be ≥ one maximal record (%d), got %d", loggedIntentMax, r.cfg.IntentBatchMax)
	}
	// S2 storage compression (ADR-0035). A storage-layer setting: payloads,
	// subjects and every reader are untouched, so this is not a change to the
	// message contract — only to how the file store persists blocks.
	compression := nats.S2Compression
	if r.cfg.UncompressedStore {
		compression = nats.NoCompression
	}
	scfg := &nats.StreamConfig{
		Name:        r.stream,
		Subjects:    []string{SubjectLogAll(run)},
		Retention:   nats.LimitsPolicy,
		Storage:     nats.FileStorage,
		Replicas:    1,
		MaxAge:      r.cfg.MaxAge,
		Compression: compression,
	}
	_, err := js.AddStream(scfg)
	if errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
		// The recorder ADOPTS an existing stream, but only an EMPTY one —
		// serve's checkFreshRecording permits exactly that, and the guard
		// below makes the invariant local instead of trusting every caller
		// (RunLive is a public API) to have checked. AddStream refuses to
		// adopt when the config differs, so before this change any config
		// edit — Compression is the first — turned adoption into a hard
		// failure. Converge it instead; compression applies to blocks
		// written from here on, which is all of them for an empty stream.
		// Without the emptiness check this fallback would silently rewrite
		// a PRESERVED recording's subjects, retention, MaxAge and
		// compression on a name collision — config drift applied to data
		// that is someone else's run.
		info, serr := js.StreamInfo(r.stream)
		if serr != nil {
			return nil, fmt.Errorf("stream %s exists but its info cannot be read: %w", r.stream, serr)
		}
		if info.State.Msgs > 0 {
			return nil, fmt.Errorf("record plane: stream %s already holds %d messages — refusing to adopt and reconfigure a non-empty recording", r.stream, info.State.Msgs)
		}
		_, err = js.UpdateStream(scfg)
	}
	if err != nil {
		return nil, fmt.Errorf("add stream %s: %w", r.stream, err)
	}
	return r, nil
}

// StreamInfo returns the current stream state (test support).
func (r *Recorder) StreamInfo() (*nats.StreamInfo, error) {
	return r.js.StreamInfo(r.stream)
}

// LogStart anchors the run with the tick-0 keyframe: every seek target then
// has a keyframe ≤ target (ADR-0006 §5 seek semantics).
func (r *Recorder) LogStart(e *engine.Engine) error {
	if err := r.logKeyframe(e); err != nil {
		return err
	}
	return r.awaitBatch(e.Tick)
}

// LogTick writes one tick's record in stream order — arbitrated intents
// (in application order, each with its applied_tick), then accepted
// director verbs (with theirs), then the keyframe and rolling CRC at their
// cadences — and awaits the batch's pubacks.
func (r *Recorder) LogTick(e *engine.Engine) error {
	tick := e.Tick
	if r.cfg.UnbatchedIntentLog {
		for _, t := range e.AppliedIntents() {
			if err := r.logIntent(t); err != nil {
				return err
			}
		}
	} else if err := r.logIntentBatch(e.AppliedIntents(), tick); err != nil {
		return err
	}
	for _, s := range e.AppliedSpawns() {
		if err := r.logVerb(s); err != nil {
			return err
		}
	}
	if tick%r.cfg.KeyframeEvery == 0 {
		if err := r.logKeyframe(e); err != nil {
			return err
		}
	}
	if tick%r.cfg.CRCEvery == 0 {
		if err := r.logCRC(e); err != nil {
			return err
		}
	}
	return r.awaitBatch(tick)
}

// LogEvent appends a control-plane event (pause/resume, ADR-0008 §6) to the
// record as a JSON message on ts.{run}.log.event and awaits its puback.
// Events are dead-time metadata: they carry no world state and the replayer
// ignores them — tick determinism is unaffected by pauses. May be called
// between ticks (at a frozen tick) — the dedup id stays unique because it
// embeds the predicted stream sequence, not a per-tick counter.
func (r *Recorder) LogEvent(tick uint64, payload []byte) error {
	if err := r.publish(SubjectLogEvent(r.run), tick, payload, 0, 0); err != nil {
		return err
	}
	r.EventsWritten++
	return r.awaitBatch(tick)
}

// publish queues one async write with the dedup + OCC headers. The dedup id
// is {run}:{tick}:{expected-stream-sequence} — unique across the whole run
// (a per-tick counter would collide between a tick's log batch and
// pause/resume events written at the same frozen tick; the predicted
// sequence cannot). Within a tick-batch the expected sequence is PREDICTED
// per batch position (lastSeq+1, +2, …): the engine is the sole writer and
// batches are awaited before the next tick, so the prediction is exact
// unless a competing writer intervenes — which is precisely what the
// assertion exists to catch. Publishes from one connection apply in order,
// so intra-batch order holds too. chunkN > 0 marks the message as fragment
// chunkI of chunkN of a chunked keyframe (ADR-0015).
func (r *Recorder) publish(subject string, tick uint64, data []byte, chunkI, chunkN int) error {
	expected := r.lastSeq + uint64(len(r.batch)) + 1
	msg := nats.NewMsg(subject)
	msg.Data = data
	msg.Header.Set(headerTick, strconv.FormatUint(tick, 10))
	msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
	if chunkN > 0 {
		msg.Header.Set(headerKeyframeChunk, fmt.Sprintf("%d/%d", chunkI, chunkN))
	}
	msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("%s:%d:%d", r.run, tick, expected))
	msg.Header.Set(nats.ExpectedLastSeqHdr, strconv.FormatUint(expected-1, 10))
	f, err := r.js.PublishMsgAsync(msg)
	if err != nil {
		return fmt.Errorf("log write %s tick %d: %w", subject, tick, err)
	}
	r.batch = append(r.batch, batchEntry{future: f, wantSeq: expected})
	return nil
}

// batchEntry pairs a puback future with the stream sequence its message
// must have been assigned.
type batchEntry struct {
	future  nats.PubAckFuture
	wantSeq uint64
}

// awaitBatch waits for every puback of the current tick-batch, in publish
// order, asserting each write landed at its predicted sequence. Any failure
// — timeout, OCC violation, sequence mismatch — is fatal to the run
// (ADR-0006 §4: abort loudly rather than corrupt the record).
func (r *Recorder) awaitBatch(tick uint64) error {
	defer func() { r.batch = r.batch[:0] }()
	for _, be := range r.batch {
		timer := time.NewTimer(r.cfg.AckWait)
		select {
		case ack := <-be.future.Ok():
			timer.Stop()
			if ack.Sequence != be.wantSeq {
				return fmt.Errorf("record plane sequence mismatch at tick %d: got %d, predicted %d (run aborts)",
					tick, ack.Sequence, be.wantSeq)
			}
			r.lastSeq = ack.Sequence
		case err := <-be.future.Err():
			timer.Stop()
			return fmt.Errorf("record plane failed at tick %d (run aborts): %w", tick, err)
		case <-timer.C:
			return fmt.Errorf("record plane puback timeout (%s) at tick %d (run aborts)", r.cfg.AckWait, tick)
		}
	}
	return nil
}

// logIntent payload v2 (little-endian): applied_tick u64 | controller_seq
// u64 | controller_id_len u16 | controller_id | vehicle_id u64 | flags u32 |
// lane_delta i32 | accel f64 | speed_setpoint f64 | signals u32 | turn i32 |
// grant u8 | route_len u16 | route bytes.
//
// flags: bit0 accel present, bit1 speed setpoint, bit2 signals, bit3 turn,
// bit4 route, bit5 held (hold-last re-issue by the contract layer), bit6
// superseded (lost the same-tick tie-break — recorded, not applied).
func (r *Recorder) logIntent(t engine.TickedIntent) error {
	if err := r.publish(SubjectLogIntent(r.run), t.Tick,
		appendLoggedIntent(nil, t), 0, 0); err != nil {
		return err
	}
	r.IntentsWritten++
	return nil
}

// appendLoggedIntent appends ONE v2 intent record to dst and returns the
// grown slice. Extracted from logIntent so the per-message form
// (ts.{run}.log.intent) and the batched form (ts.{run}.log.intents, TSLB —
// ADR-0035) share one encoder: a batched record is byte-identical to the
// per-message payload it replaces, which is what makes the migration
// checkable rather than merely asserted.
func appendLoggedIntent(dst []byte, t engine.TickedIntent) []byte {
	tick := t.Tick
	k := t.KeyedIntent
	var flags uint32
	if k.Intent.AccelSet {
		flags |= intentFlagAccelSet
	}
	if k.Intent.SpeedSet {
		flags |= intentFlagSpeedSet
	}
	if k.Intent.SignalSet {
		flags |= intentFlagSignalSet
	}
	if k.Intent.TurnSet {
		flags |= intentFlagTurnSet
	}
	route := k.Intent.Route
	if k.Intent.RouteSet && route != "" {
		flags |= intentFlagRouteSet
		if len(route) > intentMaxRoute {
			route = route[:intentMaxRoute]
		}
	} else {
		route = ""
	}
	if k.Held {
		flags |= logFlagHeld
	}
	if t.Superseded {
		flags |= logFlagSuperseded
	}
	ctl := []byte(k.Controller)
	data := dst
	data = binary.LittleEndian.AppendUint64(data, tick) // applied_tick
	data = binary.LittleEndian.AppendUint64(data, k.Seq)
	data = binary.LittleEndian.AppendUint16(data, uint16(len(ctl)))
	data = append(data, ctl...)
	data = binary.LittleEndian.AppendUint64(data, k.Intent.VehicleID)
	data = binary.LittleEndian.AppendUint32(data, flags)
	data = binary.LittleEndian.AppendUint32(data, uint32(int32(k.Intent.LaneDelta)))
	data = binary.LittleEndian.AppendUint64(data, math.Float64bits(k.Intent.Accel))
	data = binary.LittleEndian.AppendUint64(data, math.Float64bits(k.Intent.SpeedSetpoint))
	data = binary.LittleEndian.AppendUint32(data, uint32(int32(k.Intent.Signals)))
	data = binary.LittleEndian.AppendUint32(data, uint32(int32(k.Intent.Turn)))
	data = append(data, k.Grant)
	data = binary.LittleEndian.AppendUint16(data, uint16(len(route)))
	data = append(data, route...)
	return data
}

// loggedIntentMax bounds one v2 record: the fixed section, a controller name
// (u16-framed, but controller ids are NATS tokens — bounded well under this)
// and a route already capped at intentMaxRoute by the encoder above. Used to
// size the TSLB batch budget so that a record never fails to fit.
const loggedIntentMax = 18 + 256 + 8 + 4 + 4 + 8 + 8 + 4 + 4 + 1 + 2 + intentMaxRoute

// logIntentBatch writes this tick's applied intents as TSLB batch messages on
// ts.{run}.log.intents (ADR-0035), splitting when a message would outgrow the
// byte budget. Records keep AppliedIntents() order, which IS the application
// order (ADR-0006 §4) — and stream order across split messages preserves it,
// so a split needs no chunk index the way a keyframe does (ADR-0015): each
// batch message is independently decodable.
func (r *Recorder) logIntentBatch(ts []engine.TickedIntent, tick uint64) error {
	if len(ts) == 0 {
		return nil
	}
	budget := r.cfg.IntentBatchMax
	buf := make([]byte, 0, tslbHeader+64*loggedIntentMax)
	n := 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		if err := r.publish(SubjectLogIntents(r.run), tick,
			finishTSLB(buf, tick, n), 0, 0); err != nil {
			return err
		}
		r.IntentsWritten += uint64(n)
		r.IntentBatchesWritten++
		// A FRESH buffer next time, not buf[:0]: the published messages are
		// still in flight (their pubacks are awaited in awaitBatch), and
		// nats.go retains each message — and with it this slice — so a resend
		// would re-publish whatever the buffer holds THEN. Safe today only
		// because async publish leaves retries disabled; if a retry option
		// ever reaches publish, reusing the buffer lets a later batch
		// overwrite an in-flight one, and a split tick's near-uniform batches
		// can resend as byte-valid TSLB with one batch duplicated and another
		// gone. One allocation per message is noise next to the publish.
		buf, n = nil, 0
		return nil
	}
	for _, t := range ts {
		if t.Tick != tick {
			// All of a tick's applied intents carry that tick (engine
			// intent.go applyIntents). A mismatch means the caller mixed
			// ticks into one batch, which would silently misattribute
			// intents on replay — refuse rather than record it.
			return fmt.Errorf("record plane: intent for tick %d in tick %d's batch (run aborts)", t.Tick, tick)
		}
		if n > 0 && len(buf)+loggedIntentMax > budget {
			if err := flush(); err != nil {
				return err
			}
		}
		if len(buf) == 0 {
			buf = beginTSLB(buf)
		}
		buf = appendLoggedIntent(buf, t)
		n++
	}
	return flush()
}

// logVerb appends one accepted director spawn directive to the record
// (ts.{run}.log.verb, JSON — low-rate administrative traffic, same shape
// rationale as log.event). Replay re-enqueues the verb at its recorded
// applied_tick; the kernel's deterministic injection queue reproduces the
// spawn, so the demand sampler never re-runs (ADR-0006 M10 addendum).
func (r *Recorder) logVerb(s engine.TickedSpawn) error {
	data, err := encodeLoggedVerb(s)
	if err != nil {
		return err
	}
	if err := r.publish(SubjectLogVerb(r.run), s.Tick, data, 0, 0); err != nil {
		return err
	}
	r.VerbsWritten++
	return nil
}

func (r *Recorder) logKeyframe(e *engine.Engine) error {
	data, err := e.MarshalState()
	if err != nil {
		return fmt.Errorf("keyframe marshal at tick %d: %w", e.Tick, err)
	}
	subj := SubjectLogKeyframe(r.run)
	max := r.cfg.KeyframeChunkMax
	if len(data) <= max {
		if err := r.publish(subj, e.Tick, data, 0, 0); err != nil {
			return err
		}
	} else {
		// Larger than one message can carry: split into chunk messages,
		// consecutive in stream order (ADR-0015).
		n := (len(data) + max - 1) / max
		for i := 0; i < n; i++ {
			end := (i + 1) * max
			if end > len(data) {
				end = len(data)
			}
			if err := r.publish(subj, e.Tick, data[i*max:end], i+1, n); err != nil {
				return err
			}
		}
	}
	r.KeyframesWritten++
	return nil
}

// logCRC payload: tick u64 | crc u64 (self-sufficient; the stream sequence
// is never the time reference, ADR-0006 §3).
func (r *Recorder) logCRC(e *engine.Engine) error {
	data := make([]byte, 0, 16)
	data = binary.LittleEndian.AppendUint64(data, e.Tick)
	data = binary.LittleEndian.AppendUint64(data, e.CRC())
	if err := r.publish(SubjectLogCRC(r.run), e.Tick, data, 0, 0); err != nil {
		return err
	}
	r.CRCsWritten++
	return nil
}
