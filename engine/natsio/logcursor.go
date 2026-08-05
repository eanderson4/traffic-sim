package natsio

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// logcursor.go — forward-only, bounded-memory reading of a run's log stream.
//
// The player used to materialize the whole recording up front: fetchFrom from
// sequence 1 into a []*nats.Msg, then indexLogMsgs into per-tick maps, with
// both alive at once. On a city network the driver publishes one intent per
// claimed vehicle per tick (driver cadence 1, ADR-0008), so a 15 sim-minute
// chi-loop cut is ~10M log messages — measured: 9,973,269 intents against
// 9,000 CRCs and 91 keyframes. That is why the 30-minute cut could not be
// opened on a normal box, and it was never a storage problem: the record
// itself is ~2.3 GB, but the []*nats.Msg and the index are several times
// that in Go objects.
//
// Playback is monotonic in tick, so the whole index was never needed. This
// keeps one pull consumer open and holds exactly the messages of the tick
// being served; a seek closes it and re-opens at the landing keyframe's
// sequence. Cost per tick is unchanged (the same decode, once), and peak
// memory no longer scales with the length of the recording.
//
// The RECORD FORMAT is untouched — this is a reader-side change only, so
// every existing recording replays exactly as before (ADR-0024).

// cursorBatch bounds one Fetch in MESSAGES. Sized above a city tick's
// intent count (~1,100 on chi-loop) so a tick is usually one round trip.
const cursorBatch = 2048

// cursorBatchBytes bounds the same Fetch in BYTES. cursorBatch was sized
// when one log message was one ~230-byte intent; a TSLB batch (ADR-0035)
// is up to IntentBatchMax (768 KiB, ~350 KB for a chi-scale tick) in ONE
// message, so 2,048 of them would buffer ~1.5 GiB — three orders of
// magnitude over the bound this reader exists to keep (ADR-0024).
// Whichever bound a fetch reaches first ends it.
const cursorBatchBytes = 16 << 20

// cursorWait bounds a Fetch that has nothing to deliver. The recording is
// immutable by the time a player opens it, so "nothing available" means the
// end of the stream, not a slow writer — this only has to outlast a local
// broker's response.
const cursorWait = 5 * time.Second

// cursorInactive keeps the cursor's ephemeral consumer alive across gaps
// between fetches. The server's default for an ephemeral is a few seconds,
// which is SHORTER than the time a paced player spends playing one batch —
// and far shorter than a replay parked on /pause. Generous on purpose: the
// consumer is deleted explicitly by close(), so this only bounds how long a
// leaked one survives.
const cursorInactive = 2 * time.Hour

// tickRecords is one tick's replayable log content: the arbitrated intents
// and director verbs to re-enqueue before Step, and the rolling CRC to
// verify after it. Keyframes are deliberately absent — they duplicate state
// the CRC chain already pins, and seeks re-fetch them from the sparse
// keyframe subject (findKeyframe).
type tickRecords struct {
	tick    uint64
	intents []engine.KeyedIntent
	verbs   []engine.SpawnDirective
	sverbs  []engine.SignalDirective // ADR-0037 signal_set verbs
	crc     uint64
	hasCRC  bool
}

// add folds one log message into the tick's records. Unknown subjects
// (keyframes, control-plane events) are ignored, matching indexLogMsgs.
func (r *tickRecords) add(m *nats.Msg, run string) error {
	switch m.Subject {
	case SubjectLogIntent(run):
		k, err := decodeLoggedIntent(m.Data)
		if err != nil {
			return err
		}
		r.intents = append(r.intents, k.KeyedIntent)
	case SubjectLogIntents(run):
		// TSLB batch (ADR-0035). Appending in record order keeps the
		// application order. decodeTSLBMsg refuses a batch whose records
		// disagree with the NATS header tick the cursor groups by, so
		// every record here belongs to this tick.
		ks, err := decodeTSLBMsg(m)
		if err != nil {
			return err
		}
		for _, k := range ks {
			r.intents = append(r.intents, k.KeyedIntent)
		}
	case SubjectLogVerb(run):
		s, sig, isSignal, err := decodeLoggedVerbAny(m.Data)
		if err != nil {
			return err
		}
		if isSignal {
			r.sverbs = append(r.sverbs, sig.SignalDirective)
		} else {
			r.verbs = append(r.verbs, s.SpawnDirective)
		}
	case SubjectLogCRC(run):
		crc, err := decodeLoggedCRC(m.Data)
		if err != nil {
			return err
		}
		r.crc, r.hasCRC = crc, true
	}
	return nil
}

// lastLoggedTick reports the highest tick stamped on any log message, and
// whether the stream holds any. The log is written in non-decreasing tick
// order (the recorder awaits each tick's batch before the next), so this is
// the LAST message's tick header — one fetch, not a scan of the record.
func lastLoggedTick(js nats.JetStreamContext, stream, run string) (uint64, bool, error) {
	info, err := js.StreamInfo(stream)
	if err != nil {
		return 0, false, fmt.Errorf("stream info %s: %w", stream, err)
	}
	if info.State.Msgs == 0 || info.State.LastSeq == 0 {
		return 0, false, nil
	}
	msgs, err := fetchAll(js, stream, SubjectLogAll(run), info.State.LastSeq, 1)
	if err != nil {
		return 0, false, err
	}
	if len(msgs) == 0 {
		return 0, false, nil
	}
	tick, err := msgTick(msgs[0])
	if err != nil {
		return 0, false, err
	}
	return tick, true, nil
}

// keyframeScanLimit bounds the duplicate-keyframe probe. Two complete
// keyframes are all the check needs, and a keyframe is at most
// KeyframeChunkMax (768 KB) per message — 256 messages covers any state
// this project produces several times over.
const keyframeScanLimit = 256

// firstKeyframeTicks returns the ticks of the first up-to-two COMPLETE
// keyframes in stream order (a chunked keyframe counts once, at its final
// chunk — ADR-0015). It reads only the sparse keyframe subject, which is
// ~1 message per KeyframeEvery ticks, not the intent firehose.
func firstKeyframeTicks(js nats.JetStreamContext, stream, run string) ([]uint64, error) {
	msgs, err := fetchAll(js, stream, SubjectLogKeyframe(run), 0, keyframeScanLimit)
	if err != nil {
		return nil, err
	}
	var ticks []uint64
	for _, m := range msgs {
		tick, err := msgTick(m)
		if err != nil {
			return nil, err
		}
		hdr := m.Header.Get(headerKeyframeChunk)
		if hdr == "" {
			ticks = append(ticks, tick)
		} else {
			i, n, err := parseChunkHeader(hdr)
			if err != nil {
				return nil, fmt.Errorf("keyframe at tick %d: %w", tick, err)
			}
			if i == n {
				ticks = append(ticks, tick)
			}
		}
		if len(ticks) >= 2 {
			break
		}
	}
	return ticks, nil
}

// logCursor is a forward-only reader over one run's log stream. Not
// goroutine-safe: the player drives it from the Run goroutine only.
type logCursor struct {
	js     nats.JetStreamContext
	stream string
	run    string

	consumer string
	sub      *nats.Subscription

	buf  []*nats.Msg // current fetch batch
	bufI int         // next unread index into buf

	// pending is the first message of a LATER tick: read to discover the
	// group boundary, held back to start the next group.
	pending *nats.Msg

	// curSeq is the stream sequence of the last delivered message; lastSeq
	// is the record's final sequence, read once at open (the recording is
	// immutable by the time a player reads it). The difference is exactly
	// how many messages are left, because the log stream's only subject is
	// SubjectLogAll — every sequence in it matches the cursor's filter.
	// Fetch blocks until it has the FULL batch, the byte cap
	// (cursorBatchBytes) or MaxWait elapses, so
	// asking for more than remain would stall the run goroutine for the
	// whole timeout at the tail of every recording.
	curSeq  uint64
	lastSeq uint64
}

// newLogCursor opens a cursor positioned at stream sequence fromSeq. The
// consumer is ephemeral with AckNone — replay is a read-only path and must
// leave no broker state behind (same contract as fetchAll).
func newLogCursor(js nats.JetStreamContext, stream, run string, fromSeq uint64) (*logCursor, error) {
	c := &logCursor{js: js, stream: stream, run: run}
	if err := c.open(fromSeq); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *logCursor) open(fromSeq uint64) error {
	subj := SubjectLogAll(c.run)
	info, err := c.js.StreamInfo(c.stream)
	if err != nil {
		return fmt.Errorf("stream info %s: %w", c.stream, err)
	}
	cfg := &nats.ConsumerConfig{
		FilterSubject: subj,
		AckPolicy:     nats.AckNonePolicy,
		ReplayPolicy:  nats.ReplayInstantPolicy,
		DeliverPolicy: nats.DeliverByStartSequencePolicy,
		OptStartSeq:   fromSeq,
		// An ephemeral consumer with no threshold is reaped by the server
		// after a few seconds idle, and the gap BETWEEN fetches is exactly
		// how long the player takes to play one batch — at 1x pace a
		// 2048-message batch is ~6 s of wall time, so the consumer dies
		// mid-replay and the next fetch gets "no responders available".
		// Unpaced reads (tests, bake, the A/B that validated this cursor)
		// fetch milliseconds apart and never see it, which is why this only
		// showed up on the paced demo path.
		//
		// The threshold has to outlast a PAUSED replay too, where no fetch
		// happens at all for as long as the operator leaves it paused. This
		// is a backstop, not the lifecycle: close() deletes the consumer,
		// and nextMsg reopens transparently if it disappears anyway.
		InactiveThreshold: cursorInactive,
	}
	if fromSeq == 0 {
		// Sequence 0 is not a valid start sequence; "from the beginning" is
		// DeliverAll (fetchAll draws the same distinction).
		cfg.DeliverPolicy = nats.DeliverAllPolicy
		cfg.OptStartSeq = 0
	}
	ci, err := c.js.AddConsumer(c.stream, cfg)
	if err != nil {
		return fmt.Errorf("replay cursor on %s: %w", c.stream, err)
	}
	sub, err := c.js.PullSubscribe(subj, "", nats.Bind(c.stream, ci.Name))
	if err != nil {
		_ = c.js.DeleteConsumer(c.stream, ci.Name)
		return fmt.Errorf("bind replay cursor: %w", err)
	}
	c.consumer, c.sub = ci.Name, sub
	c.buf, c.bufI, c.pending = nil, 0, nil
	c.lastSeq = info.State.LastSeq
	// Nothing delivered yet: the next message is the one AT fromSeq.
	if fromSeq > 0 {
		c.curSeq = fromSeq - 1
	} else {
		c.curSeq = 0
	}
	return nil
}

// reset repositions the cursor at fromSeq, discarding all buffered state.
// Used by seek, whose landing keyframe fixes a new stream position.
func (c *logCursor) reset(fromSeq uint64) error {
	c.close()
	return c.open(fromSeq)
}

// close tears down the ephemeral consumer. Safe to call more than once.
// vanishedConsumer reports whether a Fetch failed because the cursor's
// consumer no longer exists, as opposed to a broker that is down or a
// request that timed out with work still outstanding. Those two need
// opposite handling: a missing consumer is recoverable by reopening at the
// same sequence, while a real fault must surface rather than be retried
// into a silent truncation.
//
// A fetch against a deleted consumer surfaces as ErrNoResponders (nothing
// is listening on the consumer's NEXT subject); the API also reports it as
// ErrConsumerNotFound / ErrConsumerDeleted depending on path and version,
// so all three are matched, with a string fallback for the versions that
// return a bare *nats.APIError.
func vanishedConsumer(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, nats.ErrConsumerNotFound) ||
		errors.Is(err, nats.ErrConsumerDeleted) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no responders") || strings.Contains(s, "consumer not found")
}

func (c *logCursor) close() {
	if c.sub != nil {
		_ = c.sub.Unsubscribe()
		c.sub = nil
	}
	if c.consumer != "" {
		_ = c.js.DeleteConsumer(c.stream, c.consumer)
		c.consumer = ""
	}
	c.buf, c.bufI, c.pending = nil, 0, nil
}

// nextMsg returns the next message in stream order, or nil once the record
// is exhausted. The sequence bound — not a short batch — is what ends the
// stream: treating short-as-exhausted would silently truncate a replay, and
// asking for a fixed batch would stall on the tail (Fetch waits for the
// full count or MaxWait, and the run goroutine cannot afford either).
func (c *logCursor) nextMsg() (*nats.Msg, error) {
	for c.bufI >= len(c.buf) {
		if c.curSeq >= c.lastSeq {
			return nil, nil // end of the immutable record
		}
		batch := c.lastSeq - c.curSeq
		if batch > cursorBatch {
			batch = cursorBatch
		}
		msgs, err := c.sub.Fetch(int(batch), nats.MaxWait(cursorWait), nats.PullMaxBytes(cursorBatchBytes))
		if err != nil && len(msgs) == 0 && vanishedConsumer(err) {
			// The consumer went away underneath us (reaped, or the broker
			// restarted). Reading forward is idempotent — curSeq records
			// exactly what has been delivered — so reopening at curSeq+1
			// resumes on the same message the dead consumer would have
			// served. Recover once per batch: a genuinely unreachable
			// broker fails on the retry rather than spinning.
			if rerr := c.reset(c.curSeq + 1); rerr != nil {
				return nil, fmt.Errorf("replay cursor reopen on %s at seq %d: %w", c.stream, c.curSeq, rerr)
			}
			msgs, err = c.sub.Fetch(int(batch), nats.MaxWait(cursorWait), nats.PullMaxBytes(cursorBatchBytes))
		}
		if err != nil && len(msgs) == 0 {
			// A timeout with sequences still outstanding is a real fault,
			// not an end: report it rather than truncating the replay
			// silently. (It is reachable only if messages were removed
			// mid-stream — MaxAge expiry — which recordings do not use.)
			return nil, fmt.Errorf("replay cursor fetch on %s at seq %d of %d: %w",
				c.stream, c.curSeq, c.lastSeq, err)
		}
		if len(msgs) == 0 {
			return nil, nil
		}
		md, err := msgs[len(msgs)-1].Metadata()
		if err != nil {
			return nil, fmt.Errorf("replay cursor metadata on %s: %w", c.stream, err)
		}
		c.curSeq = md.Sequence.Stream
		c.buf, c.bufI = msgs, 0
	}
	m := c.buf[c.bufI]
	c.bufI++
	return m, nil
}

// records reads forward and returns tick's log content. Ticks must be
// requested in non-decreasing order — the player advances one tick at a
// time and reset() re-anchors after a seek. A tick the record does not
// cover yields an empty (not nil) tickRecords, which replays as "no
// controller input this tick", exactly as the map lookups it replaces did.
//
// Messages stamped BEFORE the requested tick are dropped: a seek lands on a
// keyframe at or before its target, so the first group after reset can sit
// behind the playhead.
func (c *logCursor) records(tick uint64) (*tickRecords, error) {
	rec := &tickRecords{tick: tick}
	for {
		m := c.pending
		c.pending = nil
		if m == nil {
			var err error
			if m, err = c.nextMsg(); err != nil {
				return nil, err
			}
			if m == nil {
				return rec, nil // end of the record
			}
		}
		t, err := msgTick(m)
		if err != nil {
			return nil, err
		}
		switch {
		case t > tick:
			c.pending = m // starts a later group; hand it back next call
			return rec, nil
		case t < tick:
			continue // behind the playhead
		}
		if err := rec.add(m, c.run); err != nil {
			return nil, err
		}
	}
}
