package natsio

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// bus.go — the live plane (ADR-0006 §1, §4, §7): self-sufficient binary
// snapshots out on ts.{run}.state.snap (core NATS, at-most-once,
// fire-and-forget — slow consumers are dropped, never blocked), raw intents
// in on ts.{run}.ctl.intent.> (buffered; the contract layer drains, keys,
// filters, and batch-hands them to the kernel at the tick boundary),
// applied_tick echoed per controller on ts.{run}.ctl.ack.{controller_id}.

// Intent wire format v2 (little-endian; schema_version 2, ADR-0008 §1 —
// four orthogonal axes, absent fields = no change):
//
//	vehicle_id u64 | flags u32 | lane_delta i32 | accel f64 |
//	speed_setpoint f64 | signals u32 | turn i32 | route_len u16 |
//	reserved u16 | route bytes (≤ intentMaxRoute)
//
// flags bit0: accel present; bit1: speed setpoint present (negative clears
// the cruise setpoint); bit2: signals present (0 off, 1 left, 2 right, 3
// hazard); bit3: turn present (+1 left, −1 right, 0 clear — held until
// consumed at a junction); bit4: route present (destination lane id).
// lane_delta: +1 left, −1 right, 0 none. NaN/Inf accel or setpoint is
// rejected at the boundary.
const (
	intentFixedBytes = 44
	intentMaxRoute   = 48

	intentFlagAccelSet  = 1 << 0
	intentFlagSpeedSet  = 1 << 1
	intentFlagSignalSet = 1 << 2
	intentFlagTurnSet   = 1 << 3
	intentFlagRouteSet  = 1 << 4

	headerTick          = "tick"
	headerSchemaVersion = "schema_version"
	headerAppliedTick   = "applied_tick"
	intentSubjectTokens = 5 // ts.{run}.ctl.intent.{controller_id}
)

// EncodeIntent serializes one intent for ts.{run}.ctl.intent.{controller_id}.
// Exported so controllers (and tests) share the codec.
func EncodeIntent(in engine.Intent) []byte {
	var flags uint32
	if in.AccelSet {
		flags |= intentFlagAccelSet
	}
	if in.SpeedSet {
		flags |= intentFlagSpeedSet
	}
	if in.SignalSet {
		flags |= intentFlagSignalSet
	}
	if in.TurnSet {
		flags |= intentFlagTurnSet
	}
	route := in.Route
	if in.RouteSet && route != "" {
		flags |= intentFlagRouteSet
		if len(route) > intentMaxRoute {
			route = route[:intentMaxRoute]
		}
	} else {
		route = ""
	}
	buf := make([]byte, intentFixedBytes+len(route))
	binary.LittleEndian.PutUint64(buf[0:], in.VehicleID)
	binary.LittleEndian.PutUint32(buf[8:], flags)
	binary.LittleEndian.PutUint32(buf[12:], uint32(int32(in.LaneDelta)))
	binary.LittleEndian.PutUint64(buf[16:], math.Float64bits(in.Accel))
	binary.LittleEndian.PutUint64(buf[24:], math.Float64bits(in.SpeedSetpoint))
	binary.LittleEndian.PutUint32(buf[32:], uint32(int32(in.Signals)))
	binary.LittleEndian.PutUint32(buf[36:], uint32(int32(in.Turn)))
	binary.LittleEndian.PutUint16(buf[40:], uint16(len(route)))
	copy(buf[intentFixedBytes:], route)
	return buf
}

// DecodeIntent parses one intent message; ok=false means the message is
// malformed and must be dropped (counted), never applied.
func DecodeIntent(buf []byte) (engine.Intent, bool) {
	if len(buf) < intentFixedBytes {
		return engine.Intent{}, false
	}
	var in engine.Intent
	in.VehicleID = binary.LittleEndian.Uint64(buf[0:])
	flags := binary.LittleEndian.Uint32(buf[8:])
	in.LaneDelta = int(int32(binary.LittleEndian.Uint32(buf[12:])))
	in.Accel = math.Float64frombits(binary.LittleEndian.Uint64(buf[16:]))
	in.SpeedSetpoint = math.Float64frombits(binary.LittleEndian.Uint64(buf[24:]))
	in.Signals = int(int32(binary.LittleEndian.Uint32(buf[32:])))
	in.Turn = int(int32(binary.LittleEndian.Uint32(buf[36:])))
	routeLen := int(binary.LittleEndian.Uint16(buf[40:]))
	if routeLen > intentMaxRoute || len(buf) != intentFixedBytes+routeLen {
		return engine.Intent{}, false
	}
	if flags&intentFlagAccelSet != 0 {
		if math.IsNaN(in.Accel) || math.IsInf(in.Accel, 0) {
			return engine.Intent{}, false
		}
		in.AccelSet = true
	}
	if flags&intentFlagSpeedSet != 0 {
		if math.IsNaN(in.SpeedSetpoint) || math.IsInf(in.SpeedSetpoint, 0) {
			return engine.Intent{}, false
		}
		in.SpeedSet = true
	}
	if flags&intentFlagSignalSet != 0 {
		in.SignalSet = true
	}
	if flags&intentFlagTurnSet != 0 {
		in.TurnSet = true
	}
	if flags&intentFlagRouteSet != 0 {
		in.RouteSet = true
		in.Route = string(buf[intentFixedBytes:])
	}
	return in, true
}

// ArrivedIntent is a raw intent off the wire with its controller identity,
// in arrival order. The contract layer assigns the arbitration key
// (controller seq, grant) and applies claim filtering when it drains.
type ArrivedIntent struct {
	Controller string
	Intent     engine.Intent
}

// Bus is the engine's live-plane endpoint. The subscription callback runs
// on nats.go goroutines; it only copies bytes into a locked buffer. The run
// loop (the single goroutine owning world state, ADR-0005) drains the
// buffer between ticks — the wire never touches the kernel directly.
type Bus struct {
	nc    *nats.Conn
	run   string
	sub   *nats.Subscription
	geoms []LaneGeom

	mu       sync.Mutex
	buf      []ArrivedIntent // arrival order
	lastAppl map[string]uint64

	dropped  atomic.Uint64 // malformed/oversized intent messages dropped
	pubErrs  atomic.Uint64 // core publish errors (counted, not fatal — best effort plane)
	snapsOut atomic.Uint64
}

// NewBus attaches the live plane for a run: subscribes the intent wildcard
// and prepares the snapshot projection. The engine pointer is used only for
// the (static) network geometry.
func NewBus(nc *nats.Conn, run string, e *engine.Engine) (*Bus, error) {
	if err := validToken("run id", run); err != nil {
		return nil, err
	}
	b := &Bus{
		nc:       nc,
		run:      run,
		geoms:    LaneGeoms(e.Net),
		lastAppl: map[string]uint64{},
	}
	sub, err := nc.Subscribe(SubjectCtlIntentAll(run), b.onIntent)
	if err != nil {
		return nil, fmt.Errorf("subscribe intents: %w", err)
	}
	b.sub = sub
	return b, nil
}

// onIntent buffers a raw intent with its controller identity. Called on a
// nats.go delivery goroutine; never touches the engine.
func (b *Bus) onIntent(msg *nats.Msg) {
	tokens := strings.Split(msg.Subject, ".")
	if len(tokens) != intentSubjectTokens {
		b.dropped.Add(1)
		return
	}
	in, ok := DecodeIntent(msg.Data)
	if !ok {
		b.dropped.Add(1)
		return
	}
	b.mu.Lock()
	b.buf = append(b.buf, ArrivedIntent{Controller: tokens[4], Intent: in})
	b.mu.Unlock()
}

// DrainIntents returns the intents buffered since the last drain (arrival
// order). Called by the contract layer between ticks; it keys, filters, and
// hands them to the kernel at the boundary.
func (b *Bus) DrainIntents() []ArrivedIntent {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) == 0 {
		return nil
	}
	out := make([]ArrivedIntent, len(b.buf))
	copy(out, b.buf)
	b.buf = b.buf[:0]
	return out
}

// PublishSnapshot sends the per-tick self-sufficient snapshot
// fire-and-forget (ADR-0006 §6: the live plane never blocks the tick; a
// slow subscriber is the server's problem, not the engine's).
func (b *Bus) PublishSnapshot(e *engine.Engine) {
	msg := nats.NewMsg(SubjectStateSnap(b.run))
	msg.Data = SnapshotFrame(e, b.geoms)
	msg.Header.Set(headerTick, strconv.FormatUint(e.Tick, 10))
	msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
	if err := b.nc.PublishMsg(msg); err != nil {
		b.pubErrs.Add(1)
		return
	}
	b.snapsOut.Add(1)
}

// PublishSignals sends the signal-program table (TSSG v1) on
// ts.{run}.state.sig, fire-and-forget like the snapshot path. The table is
// self-sufficient: with the tick (header + payload) a client derives every
// light state by pure integer math (ADR-0011 §1), so republication at the
// keyframe cadence is the whole late-joiner catch-up story.
func (b *Bus) PublishSignals(e *engine.Engine) {
	msg := nats.NewMsg(SubjectStateSig(b.run))
	msg.Data = SignalFrame(e)
	msg.Header.Set(headerTick, strconv.FormatUint(e.Tick, 10))
	msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
	if err := b.nc.PublishMsg(msg); err != nil {
		b.pubErrs.Add(1)
	}
}

// AckPayload is the JSON body of the applied_tick echo (small; the headers
// carry the same numbers for header-only consumers).
type AckPayload struct {
	Controller  string `json:"controller_id"`
	AppliedTick uint64 `json:"applied_tick"`
}

// PublishAcks echoes the applied_tick per controller on
// ts.{run}.ctl.ack.{controller_id}: the ack channel, control-latency meter,
// and HUD health signal (ADR-0006 §7, arch-state-authority §4). Every
// controller that has ever had an intent applied gets one message per tick
// with its latest applied tick. Superseded intents do NOT refresh the echo —
// they never took effect.
func (b *Bus) PublishAcks(applied []engine.TickedIntent, tick uint64) {
	for _, k := range applied {
		if k.Superseded {
			continue
		}
		b.lastAppl[k.Controller] = tick
	}
	if len(b.lastAppl) == 0 {
		return
	}
	ctls := make([]string, 0, len(b.lastAppl))
	for c := range b.lastAppl {
		ctls = append(ctls, c)
	}
	sort.Strings(ctls)
	for _, c := range ctls {
		appliedTick := b.lastAppl[c]
		payload, _ := json.Marshal(AckPayload{Controller: c, AppliedTick: appliedTick})
		msg := nats.NewMsg(SubjectCtlAck(b.run, c))
		msg.Data = payload
		msg.Header.Set(headerTick, strconv.FormatUint(tick, 10))
		msg.Header.Set(headerAppliedTick, strconv.FormatUint(appliedTick, 10))
		msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
		if err := b.nc.PublishMsg(msg); err != nil {
			b.pubErrs.Add(1)
		}
	}
}

// Stats reports bus counters (observability, not world state).
func (b *Bus) Stats() (droppedIntents, pubErrs, snapshots uint64) {
	return b.dropped.Load(), b.pubErrs.Load(), b.snapsOut.Load()
}

// Close detaches the intent subscription.
func (b *Bus) Close() {
	if b.sub != nil {
		_ = b.sub.Unsubscribe()
	}
}
