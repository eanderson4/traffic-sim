package natsio

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
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

	headerTick           = "tick"
	headerSchemaVersion  = "schema_version"
	headerAppliedTick    = "applied_tick"
	headerSigChunk       = "sig_chunk"       // "i/n" 1-based, ADR-0016 (absent = whole table)
	headerIntentEncoding = "intent_encoding" // ADR-0026 demux: absent = v2, "tsib" = TSIB, else drop+count
	intentEncodingTSIB   = "tsib"
	intentSubjectTokens  = 5 // ts.{run}.ctl.intent.{controller_id}
)

// intentFixedFlags computes the v2 flag bits for the non-route axes.
func intentFixedFlags(in engine.Intent) uint32 {
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
	return flags
}

// putIntentFixed writes the 44-byte fixed section (the layout above) into
// buf with the given flags and route_len. Shared by v2 EncodeIntent (which
// derives both from the route) and TSIB EncodeTSIB (which pins route_len 0)
// so the layout arithmetic lives in exactly one place.
func putIntentFixed(buf []byte, in engine.Intent, flags uint32, routeLen uint16) {
	binary.LittleEndian.PutUint64(buf[0:], in.VehicleID)
	binary.LittleEndian.PutUint32(buf[8:], flags)
	binary.LittleEndian.PutUint32(buf[12:], uint32(int32(in.LaneDelta)))
	binary.LittleEndian.PutUint64(buf[16:], math.Float64bits(in.Accel))
	binary.LittleEndian.PutUint64(buf[24:], math.Float64bits(in.SpeedSetpoint))
	binary.LittleEndian.PutUint32(buf[32:], uint32(int32(in.Signals)))
	binary.LittleEndian.PutUint32(buf[36:], uint32(int32(in.Turn)))
	binary.LittleEndian.PutUint16(buf[40:], routeLen)
}

// getIntentFixed parses the 44-byte fixed section into *in (axis fields),
// returning the raw flags and route_len. Presence bits and semantic checks
// are the caller's — DecodeIntent and DecodeTSIB apply them differently
// (v2 honors the route, TSIB structurally rejects one). Pointer out-param:
// returning the ~80-byte Intent by value doubled DecodeIntent's cost.
func getIntentFixed(buf []byte, in *engine.Intent) (flags uint32, routeLen int) {
	in.VehicleID = binary.LittleEndian.Uint64(buf[0:])
	flags = binary.LittleEndian.Uint32(buf[8:])
	in.LaneDelta = int(int32(binary.LittleEndian.Uint32(buf[12:])))
	in.Accel = math.Float64frombits(binary.LittleEndian.Uint64(buf[16:]))
	in.SpeedSetpoint = math.Float64frombits(binary.LittleEndian.Uint64(buf[24:]))
	in.Signals = int(int32(binary.LittleEndian.Uint32(buf[32:])))
	in.Turn = int(int32(binary.LittleEndian.Uint32(buf[36:])))
	routeLen = int(binary.LittleEndian.Uint16(buf[40:]))
	return flags, routeLen
}

// applyIntentFlags sets in's presence bits from flags, rejecting (false) a
// NaN/Inf accel or setpoint — the boundary rule both v2 messages and TSIB
// records enforce per intent.
func applyIntentFlags(in *engine.Intent, flags uint32) bool {
	if flags&intentFlagAccelSet != 0 {
		if math.IsNaN(in.Accel) || math.IsInf(in.Accel, 0) {
			return false
		}
		in.AccelSet = true
	}
	if flags&intentFlagSpeedSet != 0 {
		if math.IsNaN(in.SpeedSetpoint) || math.IsInf(in.SpeedSetpoint, 0) {
			return false
		}
		in.SpeedSet = true
	}
	if flags&intentFlagSignalSet != 0 {
		in.SignalSet = true
	}
	if flags&intentFlagTurnSet != 0 {
		in.TurnSet = true
	}
	return true
}

// EncodeIntent serializes one intent for ts.{run}.ctl.intent.{controller_id}.
// Exported so controllers (and tests) share the codec.
func EncodeIntent(in engine.Intent) []byte {
	flags := intentFixedFlags(in)
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
	putIntentFixed(buf, in, flags, uint16(len(route)))
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
	flags, routeLen := getIntentFixed(buf, &in)
	if routeLen > intentMaxRoute || len(buf) != intentFixedBytes+routeLen {
		return engine.Intent{}, false
	}
	if !applyIntentFlags(&in, flags) {
		return engine.Intent{}, false
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

	// Signal table (ADR-0016): chunks are split+encoded ONCE here and every
	// publish (the 20-tick cadence AND the request/reply responder) sends
	// the cached slices with only the tick patched — never a re-encode.
	sigChunks   [][]byte
	sigReq      *nats.Subscription
	sigTick     atomic.Uint64 // last tick the table was published at
	sigHiWater  atomic.Bool   // 1 MiB watermark logged (signal chunk)
	snapHiWater atomic.Bool   // 1 MiB watermark logged (snapshot)

	mu       sync.Mutex
	buf      []ArrivedIntent // arrival order
	lastAppl map[string]uint64

	dropped  atomic.Uint64 // malformed/oversized intent messages dropped
	pubErrs  atomic.Uint64 // core publish errors (counted, not fatal — best effort plane)
	snapErrs atomic.Uint64 // snapshot publish failures (separate loud-budget from sigErrs, ADR-0016 §6)
	sigErrs  atomic.Uint64 // signal table publish/reply failures
	snapsOut atomic.Uint64

	// TSIB counters (ADR-0026): the observability substitute for 1:1
	// per-vehicle messages, which batching ends.
	intentBatches         atomic.Uint64 // structurally valid TSIB batches accepted
	intentRecords         atomic.Uint64 // records expanded from accepted batches (post NaN-drop)
	intentBatchDropped    atomic.Uint64 // structurally invalid batches dropped WHOLE
	intentRecordDropped   atomic.Uint64 // NaN/Inf records dropped singly (the batch-level parity of dropped)
	intentEncodingUnknown atomic.Uint64 // unknown (or present-but-empty) intent_encoding header values dropped
}

// NewBus attaches the live plane for a run: subscribes the intent wildcard
// and prepares the snapshot projection. The engine pointer is used only for
// the (static) network geometry.
func NewBus(nc *nats.Conn, run string, e *engine.Engine) (*Bus, error) {
	b, err := NewPublishBus(nc, run, e)
	if err != nil {
		return nil, err
	}
	sub, err := nc.Subscribe(SubjectCtlIntentAll(run), b.onIntent)
	if err != nil {
		// Don't leak the sig.req responder NewPublishBus installed.
		b.Close()
		return nil, fmt.Errorf("subscribe intents: %w", err)
	}
	b.sub = sub
	return b, nil
}

// NewPublishBus attaches a PUBLISH-side live plane for a run — no intent
// subscription, for publishers with no contract plane (the replay Player).
// It is the single Bus construction site (NewBus builds on it), so future
// Bus fields initialize here, not at some hand-rolled literal. The signal
// table is split+encoded into cached chunks HERE (ADR-0016 §2), and the
// request/reply catch-up responder (ts.{run}.state.sig.req) is queue
// -subscribed here too: live runs (via NewBus) and replay get it alike.
func NewPublishBus(nc *nats.Conn, run string, e *engine.Engine) (*Bus, error) {
	if err := validToken("run id", run); err != nil {
		return nil, err
	}
	b := &Bus{
		nc:        nc,
		run:       run,
		geoms:     LaneGeoms(e.Net),
		sigChunks: SignalChunks(e),
		lastAppl:  map[string]uint64{},
	}
	b.sigTick.Store(e.Tick)
	for i, c := range b.sigChunks {
		// The broker counts payload + serialized headers: reserve headroom
		// for tick/schema/sig_chunk headers or a just-under chunk would
		// pass validation and fail every publish (sol review).
		if len(c)+256 > sigMaxPayload {
			// A single program bigger than the broker cap can NEVER be
			// delivered — every broadcast and resync of this table fails
			// forever. Fail the run now, loudly, instead (ADR-0016 §1).
			return nil, fmt.Errorf("signal chunk %d/%d is %d bytes (+headers), over the %d-byte server cap: one program cannot be delivered (split it at the source)", i+1, len(b.sigChunks), len(c), sigMaxPayload)
		}
		if len(c) > SigChunkMax {
			// A single program exceeded the chunk target — it rides its own
			// oversized chunk (still deliverable under the cap). Loud.
			log.Printf("run %s: signal chunk %d/%d is %d bytes (> %d target): one program exceeds the chunk target (server cap 4 MiB)",
				run, i+1, len(b.sigChunks), len(c), SigChunkMax)
		}
	}
	sub, err := nc.QueueSubscribe(SubjectStateSigReq(run), "sig-table", b.onSignalRequest)
	if err != nil {
		return nil, fmt.Errorf("subscribe signal-table requests: %w", err)
	}
	b.sigReq = sub
	return b, nil
}

// onIntent buffers raw intents with their controller identity. Called on a
// nats.go delivery goroutine; never touches the engine. Demuxes on the
// intent_encoding header (ADR-0026): ABSENT = per-vehicle v2 (every existing
// producer, byte-identical path), "tsib" = a TSIB batch expanded into one
// ArrivedIntent per surviving record IN RECORD ORDER (engine-assigned seqs
// stay monotonic per controller exactly as for a v2 stream), anything else —
// including a present-but-EMPTY value — = drop + count, LOUD: never a
// fall-through to v2 parsing, so a future encoding against an old engine
// fails loudly instead of misparsing.
func (b *Bus) onIntent(msg *nats.Msg) {
	tokens := strings.Split(msg.Subject, ".")
	if len(tokens) != intentSubjectTokens {
		b.dropped.Add(1)
		return
	}
	// Headerless (every existing v2 producer) is the hot path: gate the
	// demux on header presence — a MIMEHeader lookup canonicalizes
	// (allocates), which is per-message cost the v2 stream never had.
	if len(msg.Header) == 0 {
		b.bufferV2Intent(tokens[4], msg.Data)
		return
	}
	// Key EXISTENCE, not Get: Get cannot distinguish an absent key (v2)
	// from a present-but-empty one (unknown encoding — ADR-0026). Keys are
	// stored verbatim (nats.Header is case-sensitive, no canonicalization),
	// so the exact contract key is the lookup.
	vals, present := msg.Header[headerIntentEncoding]
	if !present {
		// Headers present but none named intent_encoding: v2.
		b.bufferV2Intent(tokens[4], msg.Data)
		return
	}
	var enc string
	if len(vals) > 0 {
		enc = vals[0]
	}
	switch enc {
	case intentEncodingTSIB:
		intents, recordDrops, ok := DecodeTSIB(msg.Data)
		if !ok {
			// Structurally invalid batch: dropped WHOLE, never partially
			// applied (ADR-0026) — loud, first 3 per run.
			if n := b.intentBatchDropped.Add(1); n <= 3 {
				log.Printf("run %s: TSIB batch from %s dropped (%d bytes): structurally invalid", b.run, tokens[4], len(msg.Data))
			}
			return
		}
		b.intentBatches.Add(1)
		b.intentRecords.Add(uint64(len(intents)))
		b.intentRecordDropped.Add(uint64(recordDrops))
		b.mu.Lock()
		for _, in := range intents {
			b.buf = append(b.buf, ArrivedIntent{Controller: tokens[4], Intent: in})
		}
		b.mu.Unlock()
	default:
		if n := b.intentEncodingUnknown.Add(1); n <= 3 {
			log.Printf("run %s: intent message from %s with unknown intent_encoding %q dropped", b.run, tokens[4], enc)
		}
	}
}

// bufferV2Intent decodes one per-vehicle v2 message and appends it — the
// pre-TSIB callback body, factored out for the two demux exits that mean
// "v2" (headerless; headered without an intent_encoding key).
func (b *Bus) bufferV2Intent(controller string, data []byte) {
	in, ok := DecodeIntent(data)
	if !ok {
		b.dropped.Add(1)
		return
	}
	b.mu.Lock()
	b.buf = append(b.buf, ArrivedIntent{Controller: controller, Intent: in})
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

// sigMaxPayload mirrors the embedded broker's MaxPayload (cmd/serve,
// cmd/replay — 4 MiB): the ceiling a single message can ever cross.
const sigMaxPayload = 4 << 20

// PublishSnapshot sends the per-tick self-sufficient snapshot
// fire-and-forget (ADR-0006 §6: the live plane never blocks the tick; a
// slow subscriber is the server's problem, not the engine's). Publish
// failures are LOUD (first 3 per run) — a silently dropped snapshot plane
// is the failure mode this whole chunking episode came from.
func (b *Bus) PublishSnapshot(e *engine.Engine) {
	msg := nats.NewMsg(SubjectStateSnap(b.run))
	msg.Data = SnapshotFrame(e, b.geoms)
	msg.Header.Set(headerTick, strconv.FormatUint(e.Tick, 10))
	msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
	if err := b.nc.PublishMsg(msg); err != nil {
		b.pubErrs.Add(1)
		if n := b.snapErrs.Add(1); n <= 3 {
			log.Printf("run %s: snapshot publish FAILED (tick %d, %d bytes): %v", b.run, e.Tick, len(msg.Data), err)
		}
		return
	}
	b.noteFrameSize("TSSF snapshot", len(msg.Data), &b.snapHiWater)
	b.snapsOut.Add(1)
}

// noteFrameSize logs ONCE per frame type when a frame crosses the 1 MiB
// per-message discipline (ADR-0016: the 4 MiB server cap is headroom, not
// an allowance) — the first oversized frame names its size.
func (b *Bus) noteFrameSize(kind string, size int, flag *atomic.Bool) {
	if size > 1<<20 && flag.CompareAndSwap(false, true) {
		log.Printf("run %s: %s is %d bytes (> 1 MiB per-message discipline; server cap 4 MiB is headroom, not allowance)", b.run, kind, size)
	}
}

// PublishSignals sends the signal-program table on ts.{run}.state.sig,
// fire-and-forget like the snapshot path: the CACHED chunk set (encoded
// once at NewPublishBus, ADR-0016 §2) with the tick patched per publish
// and a sig_chunk "i/n" header when the table is chunked (n > 1 — absent
// means whole table, the v1 back-compat). The table is self-sufficient:
// with the tick (header + payload) a client derives every light state by
// pure integer math (ADR-0011 §1), so republication at the
// signalCatchUpEvery cadence plus the request/reply catch-up
// (onSignalRequest) is the whole late-joiner story. Publish failures are
// LOUD (first 3 per run): the table is the only path to lit signal heads,
// and a silently dropped city-scale table reads as "no signals in this
// city".
func (b *Bus) PublishSignals(e *engine.Engine) {
	n := len(b.sigChunks)
	for i, chunk := range b.sigChunks {
		data := withSigTick(chunk, e.Tick)
		msg := nats.NewMsg(SubjectStateSig(b.run))
		msg.Data = data
		msg.Header.Set(headerTick, strconv.FormatUint(e.Tick, 10))
		msg.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
		if n > 1 {
			msg.Header.Set(headerSigChunk, fmt.Sprintf("%d/%d", i+1, n))
		}
		if err := b.nc.PublishMsg(msg); err != nil {
			b.pubErrs.Add(1)
			if k := b.sigErrs.Add(1); k <= 3 {
				log.Printf("run %s: signal table publish FAILED (tick %d, chunk %d/%d, %d bytes): %v",
					b.run, e.Tick, i+1, n, len(data), err)
			}
		}
		b.noteFrameSize("TSSG signal chunk", len(data), &b.sigHiWater)
	}
	b.sigTick.Store(e.Tick)
}

// onSignalRequest answers a ts.{run}.state.sig.req request with the full
// cached chunk set on the reply inbox (ADR-0016 §3: pull-when-ready — a
// busy browser that dropped a chunk, or a late joiner, resyncs when IT is
// ready instead of waiting out a catch-up round). Serves from the cached
// slices; never re-encodes.
func (b *Bus) onSignalRequest(msg *nats.Msg) {
	if msg.Reply == "" {
		return
	}
	// Reply-subject confinement: answers go to the requester's own inbox,
	// never an arbitrary subject — without this, a request could make the
	// engine dump the whole (multi-MB) table onto any address it names.
	// Clients with a custom inboxPrefix aren't answered (logged) — the
	// only consumer is our viz, which uses the default prefix.
	if !strings.HasPrefix(msg.Reply, "_INBOX.") {
		log.Printf("run %s: signal table request with non-inbox reply %q ignored", b.run, msg.Reply)
		return
	}
	// The contract payload is EMPTY: a non-empty request is malformed and
	// gets no answer (observers can publish here now — validate at the
	// boundary, never reward a multi-MB malformed request with a multi-MB
	// response set).
	if len(msg.Data) != 0 {
		log.Printf("run %s: signal table request with %d-byte payload ignored (contract: empty)", b.run, len(msg.Data))
		return
	}
	tick := b.sigTick.Load()
	n := len(b.sigChunks)
	for i, chunk := range b.sigChunks {
		m := nats.NewMsg(msg.Reply)
		m.Data = withSigTick(chunk, tick)
		m.Header.Set(headerTick, strconv.FormatUint(tick, 10))
		m.Header.Set(headerSchemaVersion, strconv.Itoa(SchemaVersion))
		if n > 1 {
			m.Header.Set(headerSigChunk, fmt.Sprintf("%d/%d", i+1, n))
		}
		if err := b.nc.PublishMsg(m); err != nil {
			b.pubErrs.Add(1)
			if k := b.sigErrs.Add(1); k <= 3 {
				log.Printf("run %s: signal table reply FAILED (chunk %d/%d): %v", b.run, i+1, n, err)
			}
		}
	}
	// PublishMsg only enqueues; the FLUSH is what proves the resync left
	// the process — a failed flush must be loud too (ADR-0016 §6).
	if err := b.nc.Flush(); err != nil {
		b.pubErrs.Add(1)
		if k := b.sigErrs.Add(1); k <= 3 {
			log.Printf("run %s: signal table reply flush FAILED: %v", b.run, err)
		}
	}
}

// withSigTick returns chunk with the payload tick (header offset 8) set to
// tick. A COPY — the cached chunks stay immutable (the request responder
// reads them from a nats callback goroutine while the run loop publishes).
// A ≤ 768 KiB memcpy per chunk is not a re-encode.
func withSigTick(chunk []byte, tick uint64) []byte {
	buf := make([]byte, len(chunk))
	copy(buf, chunk)
	binary.LittleEndian.PutUint64(buf[8:], tick)
	return buf
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

// IntentBatchStats reports the TSIB counters (ADR-0026): accepted batches,
// expanded records, structurally rejected batches, per-record NaN/Inf drops,
// and unknown/present-empty encoding drops. Kept separate from Stats so the
// v2-plane signature is untouched.
func (b *Bus) IntentBatchStats() (batches, records, batchDropped, recordDropped, encodingUnknown uint64) {
	return b.intentBatches.Load(), b.intentRecords.Load(),
		b.intentBatchDropped.Load(), b.intentRecordDropped.Load(), b.intentEncodingUnknown.Load()
}

// BufferedIntents reports how many arrived intents wait undrained —
// read-only delivery-completeness observability (the ingest benchmark and
// the boundary-controlled applied-lag test poll it).
func (b *Bus) BufferedIntents() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// Close detaches the intent subscription.
func (b *Bus) Close() {
	if b.sub != nil {
		_ = b.sub.Unsubscribe()
	}
	if b.sigReq != nil {
		_ = b.sigReq.Unsubscribe()
	}
}
