package natsio

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// resim_test.go — the ADR-0023 §8 record digest. The contract is "sha256
// over the log messages AS CONSUMED, in stream order": every message
// exactly once. Stability alone does not test that — double-folding is
// perfectly stable and still produces a digest no independent verifier
// reproduces, which matters because the digest content-keys the published
// baked/{run}/{hash12}/ prefix.

func digestMsg(seq uint64, subject, tick, payload string) (uint64, *nats.Msg) {
	m := nats.NewMsg(subject)
	m.Header.Set(headerSchemaVersion, "1")
	m.Header.Set(headerTick, tick)
	m.Data = []byte(payload)
	return seq, m
}

// TestRecordDigestFoldsEachMessageOnce pins the exactly-once rule. A caller
// that groups by tick must read one message past the boundary and re-offer
// it to the next group (BakeSource.Run's `pending`), so add() is called
// twice for that message; the digest must not notice.
func TestRecordDigestFoldsEachMessageOnce(t *testing.T) {
	once := newRecordHasher()
	for _, m := range []struct {
		seq uint64
		msg *nats.Msg
	}{
		{1, nats.NewMsg("x")}, {2, nats.NewMsg("y")}, {3, nats.NewMsg("z")},
	} {
		once.add(m.seq, m.msg)
	}
	want := once.sum()

	// Same stream, but seq 2 re-offered across a group boundary and seq 3
	// re-offered at the tail drain — exactly what Run does.
	reoffered := newRecordHasher()
	reoffered.add(1, nats.NewMsg("x"))
	reoffered.add(2, nats.NewMsg("y"))
	reoffered.add(2, nats.NewMsg("y"))
	reoffered.add(3, nats.NewMsg("z"))
	reoffered.add(3, nats.NewMsg("z"))
	if got := reoffered.sum(); got != want {
		t.Fatalf("re-offering a message across a tick-group boundary changed "+
			"the digest:\n got %x\nwant %x", got, want)
	}
}

// TestRecordDigestDistinguishesContent guards the other direction: dedup
// must key on the stream sequence only, so genuinely different messages at
// different sequences still digest differently.
func TestRecordDigestDistinguishesContent(t *testing.T) {
	a := newRecordHasher()
	a.add(digestMsg(1, "sim.frame", "1", "alpha"))
	a.add(digestMsg(2, "sim.frame", "2", "beta"))

	b := newRecordHasher()
	b.add(digestMsg(1, "sim.frame", "1", "alpha"))
	b.add(digestMsg(2, "sim.frame", "2", "GAMMA"))

	if a.sum() == b.sum() {
		t.Fatal("digest is identical for different payloads at the same sequences")
	}

	// A skipped sequence is a different stream, not the same one.
	c := newRecordHasher()
	c.add(digestMsg(1, "sim.frame", "1", "alpha"))
	c.add(digestMsg(3, "sim.frame", "2", "beta"))
	if a.sum() == c.sum() {
		t.Fatal("digest ignores the stream sequence")
	}
}

// TestRecordDigestRejectsOutOfOrder documents that the dedup is a
// monotonic-sequence rule: an out-of-order replay is dropped rather than
// folded out of stream order. logCursor delivers in sequence order, so this
// is a guard against a future caller, not a live case.
func TestRecordDigestRejectsOutOfOrder(t *testing.T) {
	h := newRecordHasher()
	h.add(digestMsg(1, "a", "1", "one"))
	h.add(digestMsg(5, "b", "2", "five"))
	got := h.sum()

	h2 := newRecordHasher()
	h2.add(digestMsg(1, "a", "1", "one"))
	h2.add(digestMsg(5, "b", "2", "five"))
	h2.add(digestMsg(3, "c", "3", "three")) // behind the playhead: ignored
	if h2.sum() != got {
		t.Fatal("an out-of-order message changed the digest")
	}
}
