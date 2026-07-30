package natsio

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// replay.go — CRC-verified replay from the JetStream record (ADR-0005 §5,
// ADR-0006 §5): seek = nearest keyframe ≤ target tick, re-simulate forward
// from a DeliverByStartSequence cursor with the logged intents, verify the
// logged rolling-CRC sequence. The replayer paces by tick (unpaced here —
// replay never runs controllers, ADR-0008); broker ReplayOriginal wall-clock
// pacing is deliberately not used.

// ReplayReport summarizes a verified replay.
type ReplayReport struct {
	Run             string
	KeyframeTick    uint64 // keyframe the seek landed on
	KeyframeSeq     uint64 // its stream sequence
	ToTick          uint64 // last re-simulated tick
	IntentsReplayed int
	VerbsReplayed   int // director spawn directives re-enqueued
	CRCsVerified    int
	FinalCRC        uint64 // rolling CRC at ToTick
}

// RunRecord is the materialized record plane of a run: the in-memory RunLog
// (spec, arbitrated intent log in stream order, per-tick CRCs) plus the
// control-plane events (pause/resume, ADR-0008 §6) in stream order. The
// audit/test view of the stream — replay logic consumes Log exactly like
// the live loop produced it.
type RunRecord struct {
	Log    *engine.RunLog
	Events []ContractEvent
}

// MaterializeRunRecord rebuilds a run's record from its log stream. meta
// supplies the run spec (run registry ↔ log stream pairing). Keyframes are
// skipped (they duplicate state the CRC chain already pins); intents keep
// stream order, which IS the application order (ADR-0006 §4).
func MaterializeRunRecord(js nats.JetStreamContext, meta *RunMeta) (*RunRecord, error) {
	run := meta.RunID
	msgs, err := fetchFrom(js, StreamName(run), run, 1)
	if err != nil {
		return nil, err
	}
	rec := &RunRecord{Log: &engine.RunLog{Spec: meta.Spec}}
	for _, m := range msgs {
		switch m.Subject {
		case SubjectLogIntent(run):
			k, err := decodeLoggedIntent(m.Data)
			if err != nil {
				return nil, err
			}
			rec.Log.Intents = append(rec.Log.Intents, k)
		case SubjectLogIntents(run):
			// TSLB batch (ADR-0035): the same records the v2 path appends one
			// at a time, in the same order, so stream order remains the
			// application order (ADR-0006 §4).
			ks, err := decodeTSLBMsg(m)
			if err != nil {
				return nil, err
			}
			rec.Log.Intents = append(rec.Log.Intents, ks...)
		case SubjectLogVerb(run):
			s, err := decodeLoggedVerb(m.Data)
			if err != nil {
				return nil, err
			}
			rec.Log.Spawns = append(rec.Log.Spawns, s)
		case SubjectLogCRC(run):
			crc, err := decodeLoggedCRC(m.Data)
			if err != nil {
				return nil, err
			}
			rec.Log.CRCs = append(rec.Log.CRCs, crc)
		case SubjectLogEvent(run):
			var evt ContractEvent
			if err := json.Unmarshal(m.Data, &evt); err != nil {
				return nil, fmt.Errorf("log event: %w", err)
			}
			rec.Events = append(rec.Events, evt)
		}
	}
	return rec, nil
}

// ReplayFromStream re-executes run meta.RunID from its log stream up to
// target tick and verifies every logged CRC on the way. meta supplies the
// run spec (run registry ↔ log stream pairing; the keyframe does not carry
// the spec). Consumers are ephemeral pull consumers with AckNone: replay is
// a read-only audit path and leaves no broker state behind.
func ReplayFromStream(js nats.JetStreamContext, meta *RunMeta, target uint64) (*ReplayReport, error) {
	run := meta.RunID
	stream := StreamName(run)

	kf, err := findKeyframe(js, stream, run, target)
	if err != nil {
		return nil, err
	}
	e, err := engine.RestoreState(meta.Spec, kf.payload)
	if err != nil {
		return nil, fmt.Errorf("restore keyframe tick %d: %w", kf.tick, err)
	}

	msgs, err := fetchFrom(js, stream, run, kf.seq+1)
	if err != nil {
		return nil, err
	}
	idx, err := indexLogMsgs(msgs, run)
	if err != nil {
		return nil, err
	}
	if target > idx.lastTick {
		return nil, fmt.Errorf("stream %s ends at tick %d before target %d", stream, idx.lastTick, target)
	}

	rep := &ReplayReport{Run: run, KeyframeTick: kf.tick, KeyframeSeq: kf.seq}
	for e.Tick < target {
		next := e.Tick + 1
		for _, k := range idx.intents[next] {
			e.EnqueueIntent(k)
			rep.IntentsReplayed++
		}
		for _, d := range idx.verbs[next] {
			// The verb was validated when first accepted; re-resolution
			// against the same spec is deterministic and cannot newly fail
			// — a failure here means the record and spec disagree.
			if err := e.EnqueueSpawn(d); err != nil {
				return nil, fmt.Errorf("replay verb %q at tick %d: %w", d.RequestID, next, err)
			}
			rep.VerbsReplayed++
		}
		e.Step()
		if want, ok := idx.crcs[next]; ok {
			if e.CRC() != want {
				return nil, fmt.Errorf("replay divergence at tick %d: crc %016x, logged %016x",
					next, e.CRC(), want)
			}
			rep.CRCsVerified++
		}
	}
	rep.ToTick = e.Tick
	rep.FinalCRC = e.CRC()
	return rep, nil
}

// logIndex is the materialized per-tick view of a run's log stream:
// arbitrated intents and director verbs by applied tick, rolling CRCs by
// tick, the keyframe ticks in stream order, and the highest tick stamped on
// any message. ReplayFromStream and the Player share it — the Player
// materializes the whole immutable recording up front and re-enqueues from
// the index instead of re-reading the stream per tick.
type logIndex struct {
	intents   map[uint64][]engine.KeyedIntent
	verbs     map[uint64][]engine.SpawnDirective
	crcs      map[uint64]uint64
	keyframes []uint64 // keyframe ticks, stream order (for cadence derivation)
	lastTick  uint64   // highest tick header seen on any message
}

// indexLogMsgs buckets log-stream messages (stream order) by tick. Same
// decoding and errors as the loop ReplayFromStream ran inline before the
// extraction; the only addition is recording keyframe ticks.
func indexLogMsgs(msgs []*nats.Msg, run string) (*logIndex, error) {
	idx := &logIndex{
		intents: map[uint64][]engine.KeyedIntent{},
		verbs:   map[uint64][]engine.SpawnDirective{},
		crcs:    map[uint64]uint64{},
	}
	for _, m := range msgs {
		tick, err := msgTick(m)
		if err != nil {
			return nil, err
		}
		if tick > idx.lastTick {
			idx.lastTick = tick
		}
		switch m.Subject {
		case SubjectLogIntent(run):
			k, err := decodeLoggedIntent(m.Data)
			if err != nil {
				return nil, err
			}
			idx.intents[k.Tick] = append(idx.intents[k.Tick], k.KeyedIntent)
		case SubjectLogIntents(run):
			ks, err := decodeTSLBMsg(m)
			if err != nil {
				return nil, err
			}
			// Keyed by each record's own tick, exactly as the v2 case is.
			// decodeTSLBMsg has already refused a batch whose records
			// disagree with its header tick, so a split tick reassembles in
			// order.
			for _, k := range ks {
				idx.intents[k.Tick] = append(idx.intents[k.Tick], k.KeyedIntent)
			}
		case SubjectLogVerb(run):
			s, err := decodeLoggedVerb(m.Data)
			if err != nil {
				return nil, err
			}
			idx.verbs[s.Tick] = append(idx.verbs[s.Tick], s.SpawnDirective)
		case SubjectLogCRC(run):
			crc, err := decodeLoggedCRC(m.Data)
			if err != nil {
				return nil, err
			}
			idx.crcs[tick] = crc
		case SubjectLogKeyframe(run):
			// Mid-stream keyframes are for later seeks; the CRC chain
			// already verifies the state they capture. A chunked keyframe
			// (ADR-0015) counts once — at its final chunk — so the ticks
			// list holds one entry per keyframe. The player's duplicate-tick
			// corruption check used to read this; it now uses
			// firstKeyframeTicks, which scans only the sparse keyframe
			// subject (ADR-0024).
			hdr := m.Header.Get(headerKeyframeChunk)
			if hdr == "" {
				idx.keyframes = append(idx.keyframes, tick)
			} else {
				i, n, err := parseChunkHeader(hdr)
				if err != nil {
					return nil, fmt.Errorf("keyframe at tick %d: %w", tick, err)
				}
				if i == n {
					idx.keyframes = append(idx.keyframes, tick)
				}
			}
		}
	}
	return idx, nil
}

// keyframeRef is one logged keyframe located by the scan.
type keyframeRef struct {
	tick    uint64
	seq     uint64
	payload []byte
}

// findKeyframe scans the sparse keyframe subject for the nearest keyframe ≤
// target (ADR-0006 §5 seek semantics). Chunked keyframes (ADR-0015) are
// reassembled here; the returned seq is the stream sequence of the keyframe's
// LAST message (chunk or whole), so re-simulation resumes at seq+1, after
// the complete keyframe.
func findKeyframe(js nats.JetStreamContext, stream, run string, target uint64) (*keyframeRef, error) {
	subj := SubjectLogKeyframe(run)
	info, err := js.StreamInfo(stream, &nats.StreamInfoRequest{SubjectsFilter: subj})
	if err != nil {
		return nil, fmt.Errorf("stream info %s: %w", stream, err)
	}
	n := info.State.Subjects[subj]
	if n == 0 {
		return nil, fmt.Errorf("stream %s has no keyframes (run not started?)", stream)
	}
	msgs, err := fetchAll(js, stream, subj, 0, n)
	if err != nil {
		return nil, err
	}
	var best *keyframeRef
	var asm []byte // open chunk assembly (nil = none open)
	var asmTick, asmSeq uint64
	var want, got int
	finish := func(tick, seq uint64, payload []byte) {
		if tick <= target && (best == nil || tick > best.tick) {
			best = &keyframeRef{tick: tick, seq: seq, payload: payload}
		}
	}
	for _, m := range msgs {
		tick, err := msgTick(m)
		if err != nil {
			return nil, err
		}
		md, err := m.Metadata()
		if err != nil {
			return nil, fmt.Errorf("keyframe metadata: %w", err)
		}
		hdr := m.Header.Get(headerKeyframeChunk)
		if hdr == "" {
			if want > 0 {
				return nil, fmt.Errorf("keyframe at tick %d: unchunked message inside a chunk group (%d/%d seen)", asmTick, got, want)
			}
			finish(tick, md.Sequence.Stream, m.Data)
			continue
		}
		i, cn, err := parseChunkHeader(hdr)
		if err != nil {
			return nil, fmt.Errorf("keyframe at tick %d: %w", tick, err)
		}
		switch {
		case i == 1:
			if want > 0 {
				return nil, fmt.Errorf("keyframe at tick %d: new chunk group inside an open one (%d/%d seen)", tick, got, want)
			}
			asm = append([]byte(nil), m.Data...)
			asmTick, asmSeq, want, got = tick, md.Sequence.Stream, cn, 1
		case want == 0 || tick != asmTick || i != got+1 || cn != want || md.Sequence.Stream != asmSeq+1:
			// ADR-0015: chunks are consecutive in stream order — a gap
			// means an interleaved message the seek would silently skip.
			return nil, fmt.Errorf("keyframe at tick %d: chunk %d/%d out of sequence (open group tick %d, %d/%d seen)",
				tick, i, cn, asmTick, got, want)
		default:
			asm = append(asm, m.Data...)
			asmSeq = md.Sequence.Stream
			got++
		}
		if got == want {
			finish(asmTick, md.Sequence.Stream, asm)
			asm, want, got = nil, 0, 0
		}
	}
	if want > 0 {
		return nil, fmt.Errorf("keyframe at tick %d: chunk group incomplete (%d/%d seen)", asmTick, got, want)
	}
	if best == nil {
		return nil, fmt.Errorf("no keyframe ≤ target tick %d in %s", target, stream)
	}
	return best, nil
}

// parseChunkHeader parses the "i/n" kf_chunk header value (ADR-0015).
func parseChunkHeader(hdr string) (i, n int, err error) {
	a, b, ok := strings.Cut(hdr, "/")
	if !ok {
		return 0, 0, fmt.Errorf("bad chunk header %q", hdr)
	}
	i, err1 := strconv.Atoi(a)
	n, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || i < 1 || n < 1 || i > n {
		return 0, 0, fmt.Errorf("bad chunk header %q", hdr)
	}
	return i, n, nil
}

// fetchFrom delivers every log message from stream sequence fromSeq onward
// (stream order). The caller's tick bookkeeping detects "stream ends before
// target".
func fetchFrom(js nats.JetStreamContext, stream, run string, fromSeq uint64) ([]*nats.Msg, error) {
	subj := SubjectLogAll(run)
	info, err := js.StreamInfo(stream)
	if err != nil {
		return nil, fmt.Errorf("stream info %s: %w", stream, err)
	}
	if fromSeq > info.State.LastSeq {
		return nil, nil
	}
	return fetchAll(js, stream, subj, fromSeq, info.State.LastSeq-fromSeq+1)
}

// fetchAll reads up to n messages through an ephemeral pull consumer, in
// stream order. When fromSeq > 0 the consumer starts there
// (DeliverByStartSequence); otherwise it delivers all.
func fetchAll(js nats.JetStreamContext, stream, subj string, fromSeq, n uint64) ([]*nats.Msg, error) {
	cfg := &nats.ConsumerConfig{
		FilterSubject: subj,
		AckPolicy:     nats.AckNonePolicy,
		ReplayPolicy:  nats.ReplayInstantPolicy,
	}
	if fromSeq > 0 {
		cfg.DeliverPolicy = nats.DeliverByStartSequencePolicy
		cfg.OptStartSeq = fromSeq
	} else {
		cfg.DeliverPolicy = nats.DeliverAllPolicy
	}
	ci, err := js.AddConsumer(stream, cfg)
	if err != nil {
		return nil, fmt.Errorf("replay consumer on %s: %w", stream, err)
	}
	defer func() { _ = js.DeleteConsumer(stream, ci.Name) }()
	sub, err := js.PullSubscribe(subj, "", nats.Bind(stream, ci.Name))
	if err != nil {
		return nil, fmt.Errorf("bind replay consumer: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	var out []*nats.Msg
	for uint64(len(out)) < n {
		batch := n - uint64(len(out))
		if batch > 256 {
			batch = 256
		}
		msgs, err := sub.Fetch(int(batch), nats.MaxWait(5*time.Second))
		if err != nil {
			return nil, fmt.Errorf("replay fetch %s: %w", subj, err)
		}
		out = append(out, msgs...)
		if uint64(len(msgs)) < batch {
			break // stream exhausted
		}
	}
	return out, nil
}

// msgTick reads the tick header (ADR-0006 §3: tick lives in headers/payload,
// never inferred from stream sequence).
func msgTick(m *nats.Msg) (uint64, error) {
	h := m.Header.Get(headerTick)
	if h == "" {
		return 0, fmt.Errorf("message on %s without tick header", m.Subject)
	}
	tick, err := strconv.ParseUint(h, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("message on %s: bad tick header %q", m.Subject, h)
	}
	return tick, nil
}

// decodeTSLBMsg decodes one TSLB batch message and cross-checks it against
// its NATS header: DecodeTSLB verifies the records against the batch's
// PAYLOAD tick, but readers group and order by the HEADER tick (msgTick),
// and nothing else ties the two together. A message whose header and
// payload disagree would otherwise apply or index its records at the wrong
// tick — every reader goes through here so the check cannot drift between
// them.
func decodeTSLBMsg(m *nats.Msg) ([]engine.TickedIntent, error) {
	ks, err := DecodeTSLB(m.Data)
	if err != nil {
		return nil, err
	}
	htick, err := msgTick(m)
	if err != nil {
		return nil, err
	}
	for _, k := range ks {
		if k.Tick != htick {
			return nil, fmt.Errorf("TSLB record for tick %d in a message headed tick %d", k.Tick, htick)
		}
	}
	return ks, nil
}

// decodeLoggedIntent parses one whole v2 intent message (see recorder.go for
// the layout and flag bits). The payload must be consumed EXACTLY: a message
// carrying one record and nothing else is the v2 contract, and trailing bytes
// mean the payload is not what this decoder thinks it is.
func decodeLoggedIntent(data []byte) (engine.TickedIntent, error) {
	out, n, err := decodeLoggedIntentAt(data)
	if err != nil {
		return out, err
	}
	if n != len(data) {
		return out, fmt.Errorf("logged intent: %d trailing bytes after the record", len(data)-n)
	}
	return out, nil
}

// decodeLoggedIntentAt parses the v2 record at the front of data and returns
// it with the number of bytes it consumed, leaving anything after it alone.
// Shared with the TSLB batch reader (ADR-0035) so both log forms decode
// through one implementation — the records are byte-identical.
func decodeLoggedIntentAt(data []byte) (engine.TickedIntent, int, error) {
	var out engine.TickedIntent
	if len(data) < 8+8+2 {
		return out, 0, fmt.Errorf("logged intent: %d bytes, too short", len(data))
	}
	out.Tick = binary.LittleEndian.Uint64(data[0:])
	out.Seq = binary.LittleEndian.Uint64(data[8:])
	ctlLen := int(binary.LittleEndian.Uint16(data[16:]))
	rest := data[18:]
	const fixed = 8 + 4 + 4 + 8 + 8 + 4 + 4 + 1 + 2 // through route_len
	if len(rest) < ctlLen+fixed {
		return out, 0, fmt.Errorf("logged intent: %d payload bytes, want at least %d", len(data), 18+ctlLen+fixed)
	}
	out.Controller = string(rest[:ctlLen])
	rest = rest[ctlLen:]
	out.Intent.VehicleID = binary.LittleEndian.Uint64(rest[0:])
	flags := binary.LittleEndian.Uint32(rest[8:])
	out.Intent.LaneDelta = int(int32(binary.LittleEndian.Uint32(rest[12:])))
	out.Intent.Accel = math.Float64frombits(binary.LittleEndian.Uint64(rest[16:]))
	out.Intent.SpeedSetpoint = math.Float64frombits(binary.LittleEndian.Uint64(rest[24:]))
	out.Intent.Signals = int(int32(binary.LittleEndian.Uint32(rest[32:])))
	out.Intent.Turn = int(int32(binary.LittleEndian.Uint32(rest[36:])))
	out.Grant = rest[40]
	routeLen := int(binary.LittleEndian.Uint16(rest[41:]))
	rest = rest[43:]
	if len(rest) < routeLen {
		return out, 0, fmt.Errorf("logged intent: route_len %d, only %d bytes remain", routeLen, len(rest))
	}
	rest = rest[:routeLen]
	used := 18 + ctlLen + fixed + routeLen
	out.Intent.AccelSet = flags&intentFlagAccelSet != 0
	out.Intent.SpeedSet = flags&intentFlagSpeedSet != 0
	out.Intent.SignalSet = flags&intentFlagSignalSet != 0
	out.Intent.TurnSet = flags&intentFlagTurnSet != 0
	if flags&intentFlagRouteSet != 0 {
		out.Intent.RouteSet = true
		out.Intent.Route = string(rest)
	}
	out.Held = flags&logFlagHeld != 0
	out.Superseded = flags&logFlagSuperseded != 0
	return out, used, nil
}

func decodeLoggedCRC(data []byte) (uint64, error) {
	if len(data) != 16 {
		return 0, fmt.Errorf("logged crc: %d bytes, want 16", len(data))
	}
	return binary.LittleEndian.Uint64(data[8:]), nil
}
