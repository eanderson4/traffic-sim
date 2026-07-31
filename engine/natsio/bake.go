package natsio

import (
	"fmt"
	"log"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// bake.go — the bake pipeline's record-plane consumer (ADR-0023 §1): a
// strict, unpaced re-simulation of a recorded run over the shared re-sim
// core (the workstream's ADR-0023 logCursor extraction — keyframe restore
// via findKeyframe, per-tick re-enqueue from the forward cursor, CRC
// verify), delivering the restored engine to a frame sink instead of
// publishing a live plane. Where the Player's divergence policy is
// loud-and-continue (on-air survival), the bake's is the audit path's: any
// CRC or verb divergence ABORTS — a published artifact must be exact.
//
// The setup below mirrors NewPlayer's (registry meta, tick-0 keyframe,
// duplicate-keyframe and horizon computation) minus the publish bus;
// player.go is the parallel workstream's file, so the entry point is
// assembled here rather than extracted from it.

// BakeSink receives the engine at tick 0 (fresh from the tick-0 keyframe)
// and after every re-simulated tick, in order, up to the record's end.
// The engine is owned by the Run loop — the sink must not retain it, but
// may read anything from it (positions, lane speeds, signal programs); the
// ADR-0023 frame sink deliberately taps a wider surface than the Player's
// publish bus. A non-nil return aborts the bake.
type BakeSink func(e *engine.Engine) error

// BakeSource is a prepared strict re-simulation of one recorded run.
type BakeSource struct {
	js      nats.JetStreamContext
	run     string
	stream  string
	meta    *RunMeta
	e       *engine.Engine // restored at tick 0
	endTick uint64
	digest  [32]byte // valid after Run
}

// NewBakeSource prepares a bake of the recorded run: registry meta,
// tick-0 keyframe restore, the corrupt-record and horizon checks NewPlayer
// performs — minus the publish bus. Nothing steps until Run.
func NewBakeSource(js nats.JetStreamContext, run string) (*BakeSource, error) {
	if err := validRunID(run); err != nil {
		return nil, err
	}
	reg, err := NewRegistry(js)
	if err != nil {
		return nil, err
	}
	meta, err := reg.Meta(run)
	if err != nil {
		return nil, fmt.Errorf("no run registry meta for %q (was it recorded to this store?): %w", run, err)
	}
	spec := meta.Spec
	stream := StreamName(run)

	// Anchor on the tick-0 keyframe (recorder LogStart guarantees it: every
	// seek target ≥ 0 then has a keyframe ≤ target).
	kf, err := findKeyframe(js, stream, run, 0)
	if err != nil {
		return nil, fmt.Errorf("run %q: %w", run, err)
	}
	e, err := engine.RestoreState(spec, kf.payload)
	if err != nil {
		return nil, fmt.Errorf("run %q: restore tick-0 keyframe: %w", run, err)
	}
	if e.RestoreNotice != "" {
		log.Print(e.RestoreNotice)
	}
	// A dirty store (a run id recorded twice into the same stream) can
	// yield two keyframes at the same tick — refuse the corrupt record
	// loud and early rather than seeking into an ambiguous log.
	kfTicks, err := firstKeyframeTicks(js, stream, run)
	if err != nil {
		return nil, err
	}
	if len(kfTicks) >= 2 && kfTicks[1] == kfTicks[0] {
		return nil, fmt.Errorf("run %q: corrupt record: duplicate keyframes at tick %d — was this run id recorded twice into the same store?",
			run, kfTicks[0])
	}
	lastTick, _, err := lastLoggedTick(js, stream, run)
	if err != nil {
		return nil, err
	}
	// The horizon the record actually covers (same rule as the Player):
	// "done" means the run RAN to spec.Ticks; any other status caps at the
	// last logged tick — re-simming past the log would invent an
	// under-controlled tail.
	end := spec.Ticks
	if meta.Status != StatusDone && lastTick < end {
		end = lastTick
	}

	return &BakeSource{js: js, run: run, stream: stream, meta: meta, e: e, endTick: end}, nil
}

// Meta is the recorded run's registry entry (spec, scenario hash, status).
func (b *BakeSource) Meta() *RunMeta { return b.meta }

// Stream is the recording's JetStream stream name (a content-key input).
func (b *BakeSource) Stream() string { return b.stream }

// EndTick is the record's effective horizon (min of the spec's ticks and
// the last logged tick when the run did not complete).
func (b *BakeSource) EndTick() uint64 { return b.endTick }

// Engine exposes the tick-0-restored engine before Run (the bake reads the
// signal table and network geometry from it). Run takes ownership.
func (b *BakeSource) Engine() *engine.Engine { return b.e }

// RecordDigest is sha256 over the log stream messages as consumed during
// Run, in stream order (ADR-0023 §8). Valid only after Run returns nil.
func (b *BakeSource) RecordDigest() [32]byte { return b.digest }

// Run re-simulates the record from tick 0 to EndTick at unlimited speed —
// no broker listeners, no pacing — delivering the engine to sink at tick 0
// and after every tick. The first CRC or verb divergence (or sink error)
// aborts with a non-nil error; a bake that returns nil is provably the
// recorded run.
func (b *BakeSource) Run(sink BakeSink) error {
	cur, err := newLogCursor(b.js, b.stream, b.run, 1)
	if err != nil {
		return err
	}
	defer cur.close() // read-only path: leave no broker state behind

	e := b.e
	if err := sink(e); err != nil {
		return fmt.Errorf("bake sink at tick %d: %w", e.Tick, err)
	}
	h := newRecordHasher()
	var pending *nats.Msg // first message of a later tick (see logCursor.records)
	for e.Tick < b.endTick {
		next := e.Tick + 1
		// Group the tick's log content exactly as logCursor.records does,
		// with the ADR-0023 §8 record digest folded in: every message the
		// cursor delivers is hashed, in stream order, including messages
		// behind the playhead (the tick-0 keyframe at the head) and the
		// subjects tickRecords ignores (keyframes, events).
		rec := &tickRecords{tick: next}
		for {
			m := pending
			pending = nil
			if m == nil {
				m, err = cur.nextMsg()
				if err != nil {
					return fmt.Errorf("bake of run %q: read log at tick %d: %w", b.run, next, err)
				}
				if m == nil {
					break // end of the immutable record
				}
			}
			seq, err := msgSeq(m)
			if err != nil {
				return fmt.Errorf("bake of run %q: %w", b.run, err)
			}
			h.add(seq, m)
			t, err := msgTick(m)
			if err != nil {
				return fmt.Errorf("bake of run %q: %w", b.run, err)
			}
			if t > next {
				pending = m // starts a later group; hand it to the next tick
				break
			}
			if t < next {
				continue // behind the playhead (hashed above)
			}
			if err := rec.add(m, b.run); err != nil {
				return fmt.Errorf("bake of run %q: decode log at tick %d: %w", b.run, next, err)
			}
		}

		// Strict re-enqueue/verify — the audit path's policy: any CRC or
		// verb divergence ABORTS, never logs-and-continues.
		for _, k := range rec.intents {
			e.EnqueueIntent(k)
		}
		for _, d := range rec.verbs {
			if err := e.EnqueueSpawn(d); err != nil {
				return fmt.Errorf("bake of run %q aborted: verb %q at tick %d rejected (%v) — record and spec disagree",
					b.run, d.RequestID, next, err)
			}
		}
		e.Step()
		if rec.hasCRC && e.CRC() != rec.crc {
			return fmt.Errorf("bake of run %q aborted: CRC divergence at tick %d: crc %016x, logged %016x",
				b.run, next, e.CRC(), rec.crc)
		}
		if err := sink(e); err != nil {
			return fmt.Errorf("bake sink at tick %d: %w", e.Tick, err)
		}
	}
	// Drain the rest of the record into the digest: ticks past endTick
	// (possible only on a status-"done" run whose spec.Ticks precedes the
	// last logged tick) are still bytes the bake's identity covers.
	for {
		m := pending
		pending = nil
		if m == nil {
			m, err = cur.nextMsg()
			if err != nil {
				return fmt.Errorf("bake of run %q: read log tail: %w", b.run, err)
			}
			if m == nil {
				break
			}
		}
		seq, err := msgSeq(m)
		if err != nil {
			return fmt.Errorf("bake of run %q: %w", b.run, err)
		}
		h.add(seq, m)
	}
	b.digest = h.sum()
	return nil
}

// msgSeq returns a delivered message's JetStream stream sequence (the
// record digest's framing input).
func msgSeq(m *nats.Msg) (uint64, error) {
	md, err := m.Metadata()
	if err != nil {
		return 0, fmt.Errorf("log message metadata on %s: %w", m.Subject, err)
	}
	return md.Sequence.Stream, nil
}
