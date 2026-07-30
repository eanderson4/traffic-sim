package natsio

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// tslb_test.go — the batched intent log (ADR-0035).
//
// The claim this format rests on is that a TSLB batch holds EXACTLY what the
// per-message log held: the same records, byte-identical, in the same order.
// These tests check that by construction rather than trusting it, because the
// record plane is the run — a decode that silently drops or reorders intents
// produces a replay that still simulates and still reports plausible numbers.

func ti(tick, seq, veh uint64, ctl string) engine.TickedIntent {
	var t engine.TickedIntent
	t.Tick = tick
	t.Seq = seq
	t.Controller = ctl
	t.Intent.VehicleID = veh
	t.Intent.Accel = -1.25
	t.Intent.AccelSet = true
	t.Intent.SpeedSetpoint = 13.5
	t.Intent.SpeedSet = true
	t.Intent.LaneDelta = -1
	t.Intent.Signals = 3
	t.Intent.Turn = 2
	t.Grant = 7
	return t
}

// buildBatch encodes ts as one TSLB message the way logIntentBatch does.
func buildBatch(tick uint64, ts []engine.TickedIntent) []byte {
	buf := beginTSLB(nil)
	for _, t := range ts {
		buf = appendLoggedIntent(buf, t)
	}
	return finishTSLB(buf, tick, len(ts))
}

func TestTSLBRecordsAreByteIdenticalToTheV2Payload(t *testing.T) {
	// THE migration claim. If this ever fails, the batched log is a
	// re-encoding rather than a reframing, and every argument about it
	// carrying the same information has to be made again from scratch.
	in := ti(42, 9, 1234, "default-driver-3")
	in.Intent.RouteSet = true
	in.Intent.Route = "lane-7"
	single := appendLoggedIntent(nil, in)
	batch := buildBatch(42, []engine.TickedIntent{in})
	if got := batch[tslbHeader:]; string(got) != string(single) {
		t.Fatalf("batched record differs from the v2 payload:\n batch %x\nsingle %x", got, single)
	}
	// And the v2 decoder reads that record standalone, unchanged.
	back, err := decodeLoggedIntent(single)
	if err != nil {
		t.Fatalf("v2 decode: %v", err)
	}
	if back != in {
		t.Fatalf("v2 round-trip changed the intent:\n got %+v\nwant %+v", back, in)
	}
}

func TestTSLBRoundTripPreservesOrderAndEveryField(t *testing.T) {
	want := []engine.TickedIntent{
		ti(7, 1, 100, "a"),
		ti(7, 2, 101, "default-driver-8"),
		ti(7, 3, 102, ""),
	}
	// Exercise the flag bits the batch must carry and TSIB cannot (ADR-0026
	// forbids route fields in live batch records; the LOG has them).
	want[1].Held = true
	want[2].Superseded = true
	want[2].Intent.RouteSet = true
	want[2].Intent.Route = "n123_0_1"
	want[0].Intent.TurnSet = true
	want[0].Intent.SignalSet = true

	got, err := DecodeTSLB(buildBatch(7, want))
	if err != nil {
		t.Fatalf("DecodeTSLB: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestTSLBEmptyBatchDecodesEmpty(t *testing.T) {
	got, err := DecodeTSLB(buildBatch(3, nil))
	if err != nil {
		t.Fatalf("DecodeTSLB: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d records, want 0", len(got))
	}
}

func TestTSLBRejectsBadMagicAndVersion(t *testing.T) {
	good := buildBatch(1, []engine.TickedIntent{ti(1, 1, 1, "a")})

	bad := append([]byte(nil), good...)
	bad[0] = 'X'
	if _, err := DecodeTSLB(bad); err == nil || !strings.Contains(err.Error(), "magic") {
		t.Errorf("bad magic: got %v, want a magic error", err)
	}

	bad = append([]byte(nil), good...)
	bad[4] = 99
	if _, err := DecodeTSLB(bad); err == nil || !strings.Contains(err.Error(), "version") {
		t.Errorf("bad version: got %v, want a version error", err)
	}

	// The reserved flags byte must stay zero, so a future format that sets it
	// is refused by this build rather than silently misread.
	bad = append([]byte(nil), good...)
	bad[5] = 1
	if _, err := DecodeTSLB(bad); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("reserved flags: got %v, want a reserved-flags error", err)
	}

	if _, err := DecodeTSLB(good[:tslbHeader-1]); err == nil {
		t.Error("a payload shorter than the header must not decode")
	}
}

func TestTSLBRejectsACountThatDisagreesWithTheBytes(t *testing.T) {
	two := []engine.TickedIntent{ti(5, 1, 1, "a"), ti(5, 2, 2, "b")}
	buf := buildBatch(5, two)

	// Undercount: the records are all there but the header claims fewer, so
	// one would be dropped. Trailing bytes catch it — silently returning the
	// first record is the failure mode this guards.
	under := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(under[6:], 1)
	if _, err := DecodeTSLB(under); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Errorf("undercount: got %v, want a trailing-bytes error", err)
	}

	// Overcount: claims a record that is not there.
	over := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(over[6:], 3)
	if _, err := DecodeTSLB(over); err == nil {
		t.Error("overcount must not decode")
	}

	// Absurd overcount: the declared count is used to size the record slice,
	// so a corrupt frame could otherwise demand an allocation the machine
	// cannot make — an OOM where every other malformed input gets an error.
	// The count is refused against the bytes present BEFORE it sizes
	// anything.
	huge := append([]byte(nil), buf...)
	binary.LittleEndian.PutUint32(huge[6:], 1<<32-1)
	if _, err := DecodeTSLB(huge); err == nil || !strings.Contains(err.Error(), "count") {
		t.Errorf("huge count: got %v, want a count error", err)
	}
}

func TestDecodeTSLBMsgRefusesAHeaderPayloadTickMismatch(t *testing.T) {
	// DecodeTSLB ties records to the PAYLOAD tick; readers group by the NATS
	// HEADER tick. decodeTSLBMsg is the cross-check every reader goes
	// through: a message whose two ticks disagree must fail, not apply at
	// whichever tick the reader happened to trust.
	m := &nats.Msg{Header: nats.Header{}, Data: buildBatch(7, []engine.TickedIntent{ti(7, 1, 1, "a")})}
	m.Header.Set(headerTick, "9")
	if _, err := decodeTSLBMsg(m); err == nil || !strings.Contains(err.Error(), "headed tick") {
		t.Errorf("header 9 / payload 7: got %v, want a tick-mismatch error", err)
	}
	m.Header.Set(headerTick, "7")
	if _, err := decodeTSLBMsg(m); err != nil {
		t.Errorf("agreeing ticks refused: %v", err)
	}
}

func TestTSLBRejectsARecordWhoseTickDisagreesWithTheHeader(t *testing.T) {
	// A record carrying a different applied_tick would be replayed at the
	// wrong tick — the intents still apply, so nothing crashes and the run
	// just diverges. Refuse it.
	mixed := []engine.TickedIntent{ti(5, 1, 1, "a"), ti(6, 2, 2, "b")}
	if _, err := DecodeTSLB(buildBatch(5, mixed)); err == nil ||
		!strings.Contains(err.Error(), "applied_tick") {
		t.Errorf("mixed ticks: got %v, want an applied_tick error", err)
	}
}

func TestV2DecoderStillRejectsTrailingBytes(t *testing.T) {
	// Splitting decodeLoggedIntent into a "decode one record at an offset"
	// helper must not loosen the whole-message form: a v2 message is exactly
	// one record and nothing else.
	one := appendLoggedIntent(nil, ti(1, 1, 1, "a"))
	if _, err := decodeLoggedIntent(append(one, 0x00)); err == nil ||
		!strings.Contains(err.Error(), "trailing") {
		t.Errorf("got %v, want a trailing-bytes error", err)
	}
}

func TestLoggedIntentMaxBoundsARealRecord(t *testing.T) {
	// logIntentBatch reserves loggedIntentMax per record when deciding to
	// flush. If a record could exceed it, a batch could overrun the byte
	// budget and the broker would reject the publish mid-run.
	long := ti(1, 1, 1, strings.Repeat("c", 200))
	long.Intent.RouteSet = true
	long.Intent.Route = strings.Repeat("r", 4*intentMaxRoute) // encoder truncates
	if n := len(appendLoggedIntent(nil, long)); n > loggedIntentMax {
		t.Fatalf("record encodes to %d bytes, over the %d-byte bound", n, loggedIntentMax)
	}
}
