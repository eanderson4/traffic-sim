package natsio

import (
	"testing"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// keyframe_chunk_test.go — ADR-0015: a keyframe larger than one message can
// carry (broker max_payload) is logged as consecutive chunk messages and
// reassembled by the seek path, with malformed groups failing loud.

// TestChunkedKeyframeRoundTrip records a run whose KeyframeChunkMax forces
// every keyframe into chunks, then verifies the seek/replay path reassembles
// them: CRC-verified replay at several targets, one index entry per
// keyframe, and a resume sequence past the whole chunk group.
func TestChunkedKeyframeRoundTrip(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 200, 7)
	if err != nil {
		t.Fatal(err)
	}
	run := "chunked1"

	lr, err := RunLive(nc, js, run, spec, RecorderConfig{
		KeyframeEvery: 50, CRCEvery: 1, KeyframeChunkMax: 256,
	})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	e := lr.Engine

	// Keyframes at ticks 0, 50, 100, 150, 200 — five keyframes. The tick-0
	// keyframe (empty fleet) fits in one message; every later one must be
	// chunk-headed (a 256 B cap chunks even this small fleet's keyframe).
	subj := SubjectLogKeyframe(run)
	info, err := js.StreamInfo(StreamName(run), &nats.StreamInfoRequest{SubjectsFilter: subj})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := fetchAll(js, StreamName(run), subj, 0, info.State.Subjects[subj])
	if err != nil {
		t.Fatal(err)
	}
	chunked := 0
	for _, m := range msgs {
		if m.Header.Get(headerKeyframeChunk) == "" {
			if m.Header.Get(headerTick) != "0" {
				t.Fatalf("post-warmup keyframe message without chunk header (tick %s)", m.Header.Get(headerTick))
			}
			continue
		}
		chunked++
	}
	if chunked == 0 {
		t.Fatal("no chunked keyframe messages on the stream")
	}
	if lr.Recorder.KeyframesWritten != 5 {
		t.Fatalf("keyframes written = %d, want 5", lr.Recorder.KeyframesWritten)
	}

	// The log index counts one entry per keyframe, not one per chunk.
	all, err := fetchFrom(js, StreamName(run), run, 1)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := indexLogMsgs(all, run)
	if err != nil {
		t.Fatal(err)
	}
	wantTicks := []uint64{0, 50, 100, 150, 200}
	if len(idx.keyframes) != len(wantTicks) {
		t.Fatalf("index keyframes = %v, want %v", idx.keyframes, wantTicks)
	}
	for i, tk := range wantTicks {
		if idx.keyframes[i] != tk {
			t.Fatalf("index keyframes = %v, want %v", idx.keyframes, wantTicks)
		}
	}

	// CRC-verified replay through reassembled keyframes.
	meta, err := lr.Registry.Meta(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []uint64{49, 175, 200} {
		rep, err := ReplayFromStream(js, meta, target)
		if err != nil {
			t.Fatalf("ReplayFromStream(%d): %v", target, err)
		}
		if rep.FinalCRC != e.CRCs[target-1] {
			t.Fatalf("target %d: replay crc %016x, live %016x", target, rep.FinalCRC, e.CRCs[target-1])
		}
	}

	// The seek anchor's sequence must be past the whole chunk group:
	// re-simulation from seq+1 starts on a non-keyframe message.
	kf, err := findKeyframe(js, StreamName(run), run, 200)
	if err != nil {
		t.Fatal(err)
	}
	next, err := fetchAll(js, StreamName(run), SubjectLogAll(run), kf.seq+1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) > 0 && next[0].Subject == subj {
		t.Fatalf("seq after seek anchor is still a keyframe chunk (tick %s)", next[0].Header.Get(headerTick))
	}
}

// TestChunkedKeyframeFailLoud: a malformed chunk group on the stream errors
// the seek path instead of restoring a corrupt keyframe.
func TestChunkedKeyframeFailLoud(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 100, 7)
	if err != nil {
		t.Fatal(err)
	}
	run := "chunked2"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}

	// A lone mid-group chunk (no chunk 1 precedes it) — the recorder is
	// done, so nothing asserts OCC here; this simulates store corruption.
	m := nats.NewMsg(SubjectLogKeyframe(run))
	m.Header.Set(headerTick, "50")
	m.Header.Set(headerKeyframeChunk, "2/3")
	if _, err := js.PublishMsg(m); err != nil {
		t.Fatal(err)
	}
	if _, err := findKeyframe(js, StreamName(run), run, 100); err == nil {
		t.Fatal("findKeyframe accepted a malformed chunk group")
	}
}
