package natsio

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// tslb_migration_test.go — the batched intent log must record the SAME LOG as
// the per-message form (ADR-0035).
//
// tslb_test.go pins the codec in isolation. This drives the real Recorder over
// a real JetStream stream and reads it back through the production reader
// (MaterializeRunRecord), once per format, over an IDENTICAL synthetic intent
// set. A framing change that loses, reorders or mis-ticks an intent shows up
// here and nowhere else, because a replay built on a subtly wrong log still
// simulates and still produces plausible numbers.
//
// The intent set is synthetic on purpose. Two separate LIVE runs are not
// comparable — controller intents arrive over NATS, so which intent lands on
// which tick varies between runs, and the existing determinism test
// (TestLiveRecordReplayDeterminism) compares a live run to a REPLAY of itself
// rather than to a second live run. Feeding both formats one fixed list is
// what isolates the framing from that timing.

// syntheticIntents builds a deterministic per-tick intent program: tick t
// carries (t%4)+1 intents, so ticks differ in width, and every tenth tick
// carries 16 — at ~62-90 B per record that outgrows the test's
// 3-record budget, so the split path is exercised on those ticks.
func syntheticIntents(ticks uint64) [][]engine.TickedIntent {
	ctls := []string{"alpha", "default-driver-7", "b"}
	out := make([][]engine.TickedIntent, 0, ticks)
	var seq uint64
	for tick := uint64(1); tick <= ticks; tick++ {
		n := int(tick%4) + 1
		if tick%10 == 0 {
			n = 16 // wider than the test budget, so this tick splits
		}
		batch := make([]engine.TickedIntent, 0, n)
		for i := 0; i < n; i++ {
			seq++
			t := ti(tick, seq, 1000+uint64(i), ctls[i%len(ctls)])
			// Vary the optional fields so the comparison covers the flag bits
			// and the variable-length route, not just the fixed section.
			switch i % 3 {
			case 0:
				t.Held = true
			case 1:
				t.Superseded = true
			case 2:
				t.Intent.RouteSet = true
				t.Intent.Route = fmt.Sprintf("lane_%d_%d", tick, i)
			}
			batch = append(batch, t)
		}
		out = append(out, batch)
	}
	return out
}

// writeLog records program through a Recorder in one of the two formats and
// returns the materialized intents plus the message count.
func writeLog(t *testing.T, run string, unbatched bool, program [][]engine.TickedIntent) ([]engine.TickedIntent, uint64) {
	t.Helper()
	srv := NewTestServer(t)
	_, js := srv.JetStream(t)
	r, err := NewRecorder(js, run, RecorderConfig{
		CRCEvery:           1,
		UnbatchedIntentLog: unbatched,
		IntentBatchMax:     tslbHeader + 3*loggedIntentMax, // forces some splits
	})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	var msgs uint64
	for _, batch := range program {
		if len(batch) == 0 {
			continue
		}
		tick := batch[0].Tick
		if unbatched {
			for _, ti := range batch {
				if err := r.logIntent(ti); err != nil {
					t.Fatalf("logIntent: %v", err)
				}
				msgs++
			}
		} else {
			before := r.IntentBatchesWritten
			if err := r.logIntentBatch(batch, tick); err != nil {
				t.Fatalf("logIntentBatch: %v", err)
			}
			msgs += r.IntentBatchesWritten - before
		}
		if err := r.awaitBatch(tick); err != nil {
			t.Fatalf("awaitBatch tick %d: %v", tick, err)
		}
	}
	rec, err := MaterializeRunRecord(js, &RunMeta{RunID: run})
	if err != nil {
		t.Fatalf("materialize (unbatched=%v): %v", unbatched, err)
	}
	return rec.Log.Intents, msgs
}

func TestBatchedLogRecordsTheSameIntentsAsPerMessage(t *testing.T) {
	program := syntheticIntents(60)
	var want []engine.TickedIntent
	for _, b := range program {
		want = append(want, b...)
	}

	v2, v2msgs := writeLog(t, "migv2", true, program)
	tslb, tslbmsgs := writeLog(t, "migtslb", false, program)

	// Both must reproduce the program exactly...
	for name, got := range map[string][]engine.TickedIntent{"v2": v2, "TSLB": tslb} {
		if len(got) != len(want) {
			t.Fatalf("%s: materialized %d intents, wrote %d", name, len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s intent %d differs:\n got %+v\nwant %+v", name, i, got[i], want[i])
			}
		}
	}
	// ...and therefore each other, which is the migration claim.
	for i := range v2 {
		if v2[i] != tslb[i] {
			t.Fatalf("intent %d differs between formats:\n  v2 %+v\nTSLB %+v", i, v2[i], tslb[i])
		}
	}
	if tslbmsgs >= v2msgs {
		t.Errorf("TSLB wrote %d messages for %d intents that took %d before — no framing saved",
			tslbmsgs, len(want), v2msgs)
	}
	if tslbmsgs <= 60 {
		t.Errorf("%d TSLB messages for 60 non-empty ticks — the wide ticks never split, "+
			"so this test covers only the one-message-per-tick path", tslbmsgs)
	}
	t.Logf("%d intents: %d v2 messages -> %d TSLB messages (%.2fx fewer, with splits forced)",
		len(want), v2msgs, tslbmsgs, float64(v2msgs)/float64(tslbmsgs))
}

func TestBatchedLogSplitsAWideTickAcrossMessagesInOrder(t *testing.T) {
	// One tick far wider than the byte budget: it must split, and the records
	// must come back in application order across the message boundary.
	const n = 200
	batch := make([]engine.TickedIntent, 0, n)
	for i := 0; i < n; i++ {
		batch = append(batch, ti(9, uint64(i+1), uint64(2000+i), "alpha"))
	}
	got, msgs := writeLog(t, "migsplit", false, [][]engine.TickedIntent{batch})
	if msgs < 2 {
		t.Fatalf("%d message(s) for %d intents; the split path was never exercised", msgs, n)
	}
	if len(got) != n {
		t.Fatalf("materialized %d intents, wrote %d — a split lost records", len(got), n)
	}
	for i := range batch {
		if got[i] != batch[i] {
			t.Fatalf("intent %d out of order or altered across a split:\n got %+v\nwant %+v",
				i, got[i], batch[i])
		}
	}
	t.Logf("%d intents split across %d TSLB messages, order preserved", n, msgs)
}

func TestRecorderRefusesAMixedTickBatch(t *testing.T) {
	// logIntentBatch is called with one tick's AppliedIntents(). If a caller
	// ever mixes ticks, the records would replay at the wrong tick — the run
	// would still complete and quietly diverge. Refuse at write time.
	srv := NewTestServer(t)
	_, js := srv.JetStream(t)
	r, err := NewRecorder(js, "mixed", RecorderConfig{})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	err = r.logIntentBatch([]engine.TickedIntent{ti(5, 1, 1, "a"), ti(6, 2, 2, "a")}, 5)
	if err == nil {
		t.Fatal("a batch mixing ticks 5 and 6 was accepted")
	}
}

func TestRecorderRefusesToAdoptANonEmptyStream(t *testing.T) {
	// NewRecorder ADOPTS an existing stream, converging its config via
	// UpdateStream where AddStream refuses (Compression is the field that
	// first forced the fallback). That is only safe for an EMPTY stream —
	// serve's checkFreshRecording permits exactly that, but RunLive is a
	// public API, so the guard lives in NewRecorder itself: a name collision
	// with a PRESERVED recording must fail, not silently rewrite its
	// subjects, retention, MaxAge and compression underneath its data.
	srv := NewTestServer(t)
	_, js := srv.JetStream(t)

	// A stream holding a message stands in for a preserved recording. It is
	// created without S2, so the recorder's default config DIFFERS and the
	// adoption fallback is the path under test.
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     StreamName("occupied"),
		Subjects: []string{SubjectLogAll("occupied")},
		Storage:  nats.FileStorage,
	}); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	if _, err := js.Publish(SubjectLogCRC("occupied"), []byte("x")); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if _, err := NewRecorder(js, "occupied", RecorderConfig{}); err == nil {
		t.Fatal("NewRecorder adopted a non-empty stream")
	} else if !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("wrong refusal: %v", err)
	}

	// The case the fallback exists for: an EMPTY stream whose config DIFFERS
	// from the recorder's (seeded without compression, adopted under the S2
	// default) converges via UpdateStream instead of failing. Adopting with
	// UncompressedStore would NOT exercise this — server normalization makes
	// that config identical to the seed's, AddStream succeeds idempotently,
	// and the fallback never runs.
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     StreamName("emptyadopt"),
		Subjects: []string{SubjectLogAll("emptyadopt")},
		Storage:  nats.FileStorage,
	}); err != nil {
		t.Fatalf("seed empty stream: %v", err)
	}
	if _, err := NewRecorder(js, "emptyadopt", RecorderConfig{}); err != nil {
		t.Fatalf("empty-stream adoption with a differing config refused: %v", err)
	}
	info, err := js.StreamInfo(StreamName("emptyadopt"))
	if err != nil {
		t.Fatalf("StreamInfo: %v", err)
	}
	if info.Config.Compression != nats.S2Compression {
		t.Errorf("adopted stream compression = %v, want S2 (the adoption fallback did not converge the config)", info.Config.Compression)
	}
}
