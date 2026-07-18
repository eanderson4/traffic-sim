package natsio

import (
	"encoding/binary"
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
}

func (c *RecorderConfig) withDefaults() RecorderConfig {
	out := *c
	if out.KeyframeEvery == 0 {
		out.KeyframeEvery = 100
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

	// Counters (observability).
	IntentsWritten   uint64
	KeyframesWritten uint64
	CRCsWritten      uint64
	EventsWritten    uint64
}

// Record-plane-only intent flag bits (the wire bits 0–4 are shared with the
// live intent frame, bus.go).
const (
	logFlagHeld       = 1 << 5 // hold-last re-issue synthesized by the contract layer
	logFlagSuperseded = 1 << 6 // lost the same-tick tie-break: recorded, not applied
)

// NewRecorder creates (or adopts) the per-run stream.
func NewRecorder(js nats.JetStreamContext, run string, cfg RecorderConfig) (*Recorder, error) {
	if err := validToken("run id", run); err != nil {
		return nil, err
	}
	r := &Recorder{js: js, run: run, cfg: cfg.withDefaults(), stream: StreamName(run)}
	_, err := js.AddStream(&nats.StreamConfig{
		Name:      r.stream,
		Subjects:  []string{SubjectLogAll(run)},
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		Replicas:  1,
		MaxAge:    r.cfg.MaxAge,
	})
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
// (in application order, each with its applied_tick), then the keyframe and
// rolling CRC at their cadences — and awaits the batch's pubacks.
func (r *Recorder) LogTick(e *engine.Engine) error {
	tick := e.Tick
	for _, t := range e.AppliedIntents() {
		if err := r.logIntent(t); err != nil {
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
	if err := r.publish(SubjectLogEvent(r.run), tick, payload); err != nil {
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
// so intra-batch order holds too.
func (r *Recorder) publish(subject string, tick uint64, data []byte) error {
	expected := r.lastSeq + uint64(len(r.batch)) + 1
	msg := nats.NewMsg(subject)
	msg.Data = data
	msg.Header.Set(headerTick, strconv.FormatUint(tick, 10))
	msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
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
	data := make([]byte, 0, 18+len(ctl)+8+4+4+8+8+4+4+1+2+len(route))
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
	if err := r.publish(SubjectLogIntent(r.run), tick, data); err != nil {
		return err
	}
	r.IntentsWritten++
	return nil
}

func (r *Recorder) logKeyframe(e *engine.Engine) error {
	data, err := e.MarshalState()
	if err != nil {
		return fmt.Errorf("keyframe marshal at tick %d: %w", e.Tick, err)
	}
	if err := r.publish(SubjectLogKeyframe(r.run), e.Tick, data); err != nil {
		return err
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
	if err := r.publish(SubjectLogCRC(r.run), e.Tick, data); err != nil {
		return err
	}
	r.CRCsWritten++
	return nil
}
