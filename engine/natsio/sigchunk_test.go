package natsio

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// sigchunk_test.go — TSSG chunking (ADR-0016): the greedy pack's byte
// boundaries, per-chunk standalone decode and reassembly, the tick patch,
// sig_chunk header parsing, cached encode-once publishing, and the
// request/reply catch-up round-trip against a real embedded server.

func sigEngine(t *testing.T) *engine.Engine {
	t.Helper()
	e, err := engine.NewEngine(sigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func chunkLens(chunks [][]byte) []int {
	out := make([]int, len(chunks))
	for i, c := range chunks {
		out[i] = len(c)
	}
	return out
}

func TestSignalChunksPackBoundaries(t *testing.T) {
	e := sigEngine(t)
	bodies := signalProgramBodies(e)
	if len(bodies) != 2 {
		t.Fatalf("fixture bodies = %d, want 2", len(bodies))
	}
	full := sigFrameHeader + len(bodies[0]) + len(bodies[1])

	// Exact fit: the whole table rides one chunk.
	if chunks := signalChunksMax(e, full); len(chunks) != 1 {
		t.Fatalf("exact fit: %d chunks, want 1", len(chunks))
	}
	// One byte over: sigB spills into a second chunk, and the accounting
	// includes the 24-byte header per chunk.
	chunks := signalChunksMax(e, full-1)
	if len(chunks) != 2 {
		t.Fatalf("one-over: %d chunks, want 2", len(chunks))
	}
	if got, want := len(chunks[0]), sigFrameHeader+len(bodies[0]); got != want {
		t.Fatalf("chunk 0 = %d bytes, want %d (header + sigA body)", got, want)
	}
	if got, want := len(chunks[1]), sigFrameHeader+len(bodies[1]); got != want {
		t.Fatalf("chunk 1 = %d bytes, want %d (header + sigB body)", got, want)
	}

	// Oversized single program: a cap below sigA's own body still yields
	// one chunk per program — sigA rides alone, over the cap.
	chunks = signalChunksMax(e, sigFrameHeader+len(bodies[0])-1)
	if len(chunks) != 2 || len(chunks[0]) != sigFrameHeader+len(bodies[0]) {
		t.Fatalf("oversized single: %d chunks %v, want sigA alone oversized", len(chunks), chunkLens(chunks))
	}
	f, err := ParseSignalFrame(chunks[0])
	if err != nil || len(f.Programs) != 1 || f.Programs[0].ID != "sigA" {
		t.Fatalf("oversized chunk must parse as sigA alone: %+v (%v)", f.Programs, err)
	}
}

func TestSignalChunksRoundTrip(t *testing.T) {
	e := sigEngine(t)
	whole := SignalFrame(e)

	// The common case — one chunk under the real cap — is exactly
	// SignalFrame's bytes (the v1 single-message contract).
	chunks := SignalChunks(e)
	if len(chunks) != 1 || !bytes.Equal(chunks[0], whole) {
		t.Fatalf("single chunk must equal SignalFrame bytes (%d chunks)", len(chunks))
	}

	// Multi-chunk: every chunk decodes STANDALONE (program_count counts
	// this chunk's programs), and concatenation reassembles the whole
	// table in file order.
	chunks = signalChunksMax(e, sigFrameHeader+1) // one program per chunk
	if len(chunks) != 2 {
		t.Fatalf("%d chunks, want 2", len(chunks))
	}
	wf, err := ParseSignalFrame(whole)
	if err != nil {
		t.Fatal(err)
	}
	var got []SigProgram
	for i, c := range chunks {
		f, err := ParseSignalFrame(c)
		if err != nil {
			t.Fatalf("chunk %d does not decode standalone: %v", i, err)
		}
		got = append(got, f.Programs...)
	}
	if len(got) != len(wf.Programs) {
		t.Fatalf("reassembled %d programs, want %d", len(got), len(wf.Programs))
	}
	for i := range wf.Programs {
		if got[i].ID != wf.Programs[i].ID {
			t.Fatalf("program %d = %q, want %q (order not preserved)", i, got[i].ID, wf.Programs[i].ID)
		}
	}

	// Empty table: one explicit empty frame, still a single chunk.
	spec, err := engine.DefaultSpec("ring", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	ring, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	chunks = SignalChunks(ring)
	if len(chunks) != 1 {
		t.Fatalf("empty table: %d chunks, want 1", len(chunks))
	}
	f, err := ParseSignalFrame(chunks[0])
	if err != nil || len(f.Programs) != 0 {
		t.Fatalf("empty table chunk: %+v (%v)", f.Programs, err)
	}
}

func TestWithSigTick(t *testing.T) {
	e := sigEngine(t)
	chunks := SignalChunks(e)
	orig := append([]byte(nil), chunks[0]...)
	patched := withSigTick(chunks[0], 42)
	if got := binary.LittleEndian.Uint64(patched[8:]); got != 42 {
		t.Fatalf("patched tick = %d, want 42", got)
	}
	if !bytes.Equal(patched[:8], orig[:8]) || !bytes.Equal(patched[16:], orig[16:]) {
		t.Fatal("the patch touched bytes outside the tick field")
	}
	if !bytes.Equal(chunks[0], orig) {
		t.Fatal("withSigTick mutated the cached chunk")
	}
}

func TestSigChunkHeaderParse(t *testing.T) {
	if i, n, err := parseChunkHeader("2/3"); err != nil || i != 2 || n != 3 {
		t.Fatalf(`"2/3" → %d/%d (%v)`, i, n, err)
	}
	if i, n, err := parseChunkHeader("10/10"); err != nil || i != 10 || n != 10 {
		t.Fatalf(`"10/10" → %d/%d (%v)`, i, n, err)
	}
	for _, bad := range []string{"", "1", "x/y", "0/1", "1/0", "3/2", "1/"} {
		if _, _, err := parseChunkHeader(bad); err == nil {
			t.Errorf("malformed header %q accepted", bad)
		}
	}
}

// The publish path: cached chunks go out in order with sig_chunk headers
// and a fresh tick (header AND payload); two publishes at the same tick
// are byte-identical (encoded ONCE at NewPublishBus, never re-encoded).
func TestPublishSignalsChunked(t *testing.T) {
	srv := NewTestServer(t)
	nc := srv.Connect(t)
	e := sigEngine(t)
	bus, err := NewPublishBus(nc, "sigchunk", e)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	// The fixture table is one 768 KiB chunk; the multi-chunk plumbing is
	// what this test exercises (the pack itself is covered above).
	bus.sigChunks = signalChunksMax(e, sigFrameHeader+1)
	if len(bus.sigChunks) != 2 {
		t.Fatalf("%d chunks, want 2", len(bus.sigChunks))
	}
	e.Tick = 7

	watch := srv.Connect(t)
	sub, err := watch.SubscribeSync(SubjectStateSig("sigchunk"))
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.Flush(); err != nil { // the server must hold the SUB before the publish
		t.Fatal(err)
	}
	publishAndCollect := func() []*nats.Msg {
		t.Helper()
		bus.PublishSignals(e)
		var msgs []*nats.Msg
		for range bus.sigChunks {
			msg, err := sub.NextMsg(5 * time.Second)
			if err != nil {
				t.Fatalf("watch: %v", err)
			}
			msgs = append(msgs, msg)
		}
		return msgs
	}

	first := publishAndCollect()
	for i, want := range []string{"1/2", "2/2"} {
		if got := first[i].Header.Get(headerSigChunk); got != want {
			t.Fatalf("chunk %d sig_chunk = %q, want %q", i, got, want)
		}
		if got := first[i].Header.Get(headerTick); got != "7" {
			t.Fatalf("chunk %d tick header = %q, want 7", i, got)
		}
		f, err := ParseSignalFrame(first[i].Data)
		if err != nil || f.Tick != 7 {
			t.Fatalf("chunk %d payload tick = %d (%v), want 7", i, f.Tick, err)
		}
	}

	second := publishAndCollect()
	for i := range first {
		if !bytes.Equal(first[i].Data, second[i].Data) {
			t.Fatalf("chunk %d changed between same-tick publishes — the chunks must be cached", i)
		}
	}

	// Whole-table publish (the real cached set: one chunk): NO sig_chunk
	// header — absent means whole table (the v1 back-compat).
	solo, err := NewPublishBus(nc, "sigsolo", e)
	if err != nil {
		t.Fatal(err)
	}
	defer solo.Close()
	sub2, err := watch.SubscribeSync(SubjectStateSig("sigsolo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.Flush(); err != nil { // the server must hold the SUB before the publish
		t.Fatal(err)
	}
	solo.PublishSignals(e)
	msg, err := sub2.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.Header.Get(headerSigChunk); got != "" {
		t.Fatalf("single-chunk table carried sig_chunk %q — it must be absent", got)
	}
	if _, err := sub2.NextMsg(50 * time.Millisecond); err == nil {
		t.Fatal("single-chunk table published more than one message")
	}
}

// The request/reply catch-up: one request on ts.{run}.state.sig.req gets
// the full cached chunk set on the reply inbox, in order, parseable.
func TestSignalTableRequestReply(t *testing.T) {
	srv := NewTestServer(t)
	nc := srv.Connect(t)
	e := sigEngine(t)
	bus, err := NewPublishBus(nc, "sigreq", e)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	bus.sigChunks = signalChunksMax(e, sigFrameHeader+1)

	client := srv.Connect(t)
	inbox := nats.NewInbox()
	replies, err := client.SubscribeSync(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.PublishRequest(SubjectStateSigReq("sigreq"), inbox, nil); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"1/2", "2/2"} {
		msg, err := replies.NextMsg(5 * time.Second)
		if err != nil {
			t.Fatalf("reply %d: %v", i, err)
		}
		if got := msg.Header.Get(headerSigChunk); got != want {
			t.Fatalf("reply %d sig_chunk = %q, want %q", i, got, want)
		}
		if _, err := ParseSignalFrame(msg.Data); err != nil {
			t.Fatalf("reply %d does not parse: %v", i, err)
		}
	}
	if _, err := replies.NextMsg(50 * time.Millisecond); err == nil {
		t.Fatal("more replies than the chunk set")
	}
}
