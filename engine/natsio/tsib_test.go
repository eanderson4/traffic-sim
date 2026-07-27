package natsio

// tsib_test.go — ADR-0026 M1: the TSIB codec, the intent_encoding header
// demux, expand-at-ingest ordering, and the downstream-unchanged guarantees
// (claim filter, hold-last) applied to expanded records. The Bus/Contract
// wiring reuses the M0 benchmark harness (bench_intent_test.go).

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// tsibCodecCases are shared between the v2 fixed section and TSIB records:
// the layouts are byte-identical by contract (ADR-0026 wire format).
var tsibCodecCases = []engine.Intent{
	{VehicleID: 42, Accel: -2.5, AccelSet: true, SpeedSetpoint: 22.5, SpeedSet: true,
		LaneDelta: -1, Turn: 1, TurnSet: true, Signals: 3, SignalSet: true}, // all axes
	{VehicleID: 1}, // no axes — no change
	{VehicleID: 1<<63 + 7, Accel: 0.5, AccelSet: true},
	{VehicleID: 9, SpeedSetpoint: -1, SpeedSet: true}, // negative clears the cruise setpoint
	{VehicleID: 77, LaneDelta: 1, Turn: -1, TurnSet: true, Signals: 2, SignalSet: true},
}

func tsibMsg(subj string, data []byte) *nats.Msg {
	m := nats.NewMsg(subj)
	m.Data = data
	m.Header.Set(headerIntentEncoding, intentEncodingTSIB)
	return m
}

func TestTSIBCodecRoundTrip(t *testing.T) {
	buf := EncodeTSIB(1234, tsibCodecCases)
	if len(buf) != tsibHeaderBytes+intentFixedBytes*len(tsibCodecCases) {
		t.Fatalf("encoded %d bytes, want %d", len(buf), tsibHeaderBytes+intentFixedBytes*len(tsibCodecCases))
	}
	if string(buf[0:4]) != "TSIB" || binary.LittleEndian.Uint16(buf[4:]) != 1 {
		t.Fatalf("bad header: % x", buf[:tsibHeaderBytes])
	}
	if got := binary.LittleEndian.Uint64(buf[tsibTickOff:]); got != 1234 {
		t.Fatalf("tick = %d, want 1234", got)
	}
	if got := binary.LittleEndian.Uint32(buf[16:]); got != uint32(len(tsibCodecCases)) {
		t.Fatalf("count = %d, want %d", got, len(tsibCodecCases))
	}
	// Records are byte-identical to each intent's v2 fixed section — the
	// shared-table case with the v2 codec the ADR pins.
	for i, in := range tsibCodecCases {
		rec := buf[tsibHeaderBytes+i*intentFixedBytes : tsibHeaderBytes+(i+1)*intentFixedBytes]
		if v2 := EncodeIntent(in); string(rec) != string(v2) {
			t.Fatalf("record %d differs from v2 fixed section:\n tsib % x\n v2   % x", i, rec, v2)
		}
	}
	got, drops, ok := DecodeTSIB(buf)
	if !ok || len(got) != len(tsibCodecCases) || drops != 0 {
		t.Fatalf("round trip = %d records, %d drops (ok %v), want %d", len(got), drops, ok, len(tsibCodecCases))
	}
	for i := range got {
		if got[i] != tsibCodecCases[i] {
			t.Fatalf("record %d = %+v, want %+v", i, got[i], tsibCodecCases[i])
		}
	}

	// Route-bearing intents never enter a batch: EncodeTSIB skips them (the
	// driver diverts them to a standalone v2 intent — M2).
	route := engine.Intent{VehicleID: 5, Route: "B0", RouteSet: true}
	mixed := []engine.Intent{tsibCodecCases[0], route, tsibCodecCases[1]}
	got, _, ok = DecodeTSIB(EncodeTSIB(7, mixed))
	if !ok || len(got) != 2 || got[0] != tsibCodecCases[0] || got[1] != tsibCodecCases[1] {
		t.Fatalf("route-bearing skip: %d records (ok %v): %+v", len(got), ok, got)
	}

	// An empty batch (count 0) is valid and expands to nothing.
	if got, _, ok = DecodeTSIB(EncodeTSIB(7, nil)); !ok || len(got) != 0 {
		t.Fatalf("empty batch: %d records (ok %v)", len(got), ok)
	}

	// Over-cap input encodes nothing (nil) rather than invalid-by-
	// construction wire bytes — splitting is the driver's job (M2). Route-
	// bearing intents don't count toward the cap (they are skipped).
	over := make([]engine.Intent, TSIBMaxRecords+1)
	if buf := EncodeTSIB(1, over); buf != nil {
		t.Fatalf("over-cap encode = %d bytes, want nil", len(buf))
	}
	withRoutes := append(make([]engine.Intent, TSIBMaxRecords), route)
	if buf := EncodeTSIB(1, withRoutes); len(buf) != tsibHeaderBytes+intentFixedBytes*TSIBMaxRecords {
		t.Fatalf("cap route-free + 1 skipped route = %d bytes, want a full valid batch", len(buf))
	}
}

// Structurally invalid batches — bad magic, wrong version, truncated, length
// mismatch, count over the cap, route-bearing record (route_len ≠ 0 or flag
// bit4) — are rejected WHOLE (ok=false) and counted intentBatchDropped.
func TestTSIBMalformed(t *testing.T) {
	good := EncodeTSIB(1, tsibCodecCases)
	patch := func(mut func(b []byte)) []byte {
		b := make([]byte, len(good))
		copy(b, good)
		mut(b)
		return b
	}
	overCap := make([]byte, tsibHeaderBytes+intentFixedBytes*(TSIBMaxRecords+1))
	copy(overCap, "TSIB")
	binary.LittleEndian.PutUint16(overCap[4:], 1)
	binary.LittleEndian.PutUint32(overCap[16:], TSIBMaxRecords+1)

	cases := map[string][]byte{
		"bad magic":       patch(func(b []byte) { b[0] = 'X' }),
		"wrong version":   patch(func(b []byte) { binary.LittleEndian.PutUint16(b[4:], 2) }),
		"truncated hdr":   good[:tsibHeaderBytes-4],
		"truncated rec":   good[:len(good)-10],
		"length mismatch": append(patch(func(b []byte) {}), 0),
		"count over cap":  overCap,
		"route_len rec":   patch(func(b []byte) { binary.LittleEndian.PutUint16(b[tsibHeaderBytes+40:], 1) }),
		"bit4 rec":        patch(func(b []byte) { binary.LittleEndian.PutUint32(b[tsibHeaderBytes+8:], intentFlagRouteSet) }),
	}
	bus := &Bus{} // onIntent touches only counters + the buffer; no broker needed
	for name, payload := range cases {
		if _, _, ok := DecodeTSIB(payload); ok {
			t.Errorf("%s: structurally invalid batch accepted", name)
		}
		bus.onIntent(tsibMsg("ts.r.ctl.intent.alpha", payload))
		if len(bus.buf) != 0 {
			t.Errorf("%s: invalid batch expanded into the buffer", name)
		}
	}
	if _, _, dropped, _, _ := bus.IntentBatchStats(); dropped != uint64(len(cases)) {
		t.Fatalf("intentBatchDropped = %d, want %d", dropped, len(cases))
	}
	if d, _, _ := bus.Stats(); d != 0 {
		t.Fatalf("v2 dropped counter = %d, want 0 (batch rejects count separately)", d)
	}

	// Boundary: exactly TSIBMaxRecords records is a valid batch (the +1
	// over-cap case above pins the other side).
	boundary := make([]engine.Intent, TSIBMaxRecords)
	for i := range boundary {
		boundary[i] = engine.Intent{VehicleID: uint64(i + 1), Accel: 0.5, AccelSet: true}
	}
	got, _, ok := DecodeTSIB(EncodeTSIB(1, boundary))
	if !ok || len(got) != TSIBMaxRecords {
		t.Fatalf("boundary batch: %d records (ok %v), want %d", len(got), ok, TSIBMaxRecords)
	}
}

// A NaN/Inf record among valid records drops ALONE (v2 parity); the rest of
// the batch still decodes and expands in record order. The drop is COUNTED
// (recordDrops / intentRecordDropped) — the batch-level parity of v2's
// dropped counter.
func TestTSIBMixedNaNDropsOnlyBadRecord(t *testing.T) {
	intents := []engine.Intent{
		{VehicleID: 1, Accel: 0.5, AccelSet: true},
		{VehicleID: 2, Accel: math.NaN(), AccelSet: true},
		{VehicleID: 3, SpeedSetpoint: math.Inf(1), SpeedSet: true},
		{VehicleID: 4, Accel: -1, AccelSet: true},
	}
	got, drops, ok := DecodeTSIB(EncodeTSIB(1, intents))
	if !ok || len(got) != 2 || got[0] != intents[0] || got[1] != intents[3] {
		t.Fatalf("mixed NaN batch: %d records (ok %v): %+v", len(got), ok, got)
	}
	if drops != 2 {
		t.Fatalf("recordDrops = %d, want 2 (NaN accel + Inf setpoint)", drops)
	}

	bus := &Bus{}
	bus.onIntent(tsibMsg("ts.r.ctl.intent.alpha", EncodeTSIB(1, intents)))
	if len(bus.buf) != 2 || bus.buf[0].Intent.VehicleID != 1 || bus.buf[1].Intent.VehicleID != 4 {
		t.Fatalf("expansion: %+v", bus.buf)
	}
	for _, a := range bus.buf {
		if a.Controller != "alpha" {
			t.Fatalf("controller = %q, want alpha (from the subject)", a.Controller)
		}
	}
	batches, records, _, recordDropped, _ := bus.IntentBatchStats()
	if batches != 1 || records != 2 || recordDropped != 2 {
		t.Fatalf("counters: batches %d records %d recordDropped %d, want 1/2/2", batches, records, recordDropped)
	}
}

// Header discipline (ADR-0026 consequences): a TSIB payload published
// WITHOUT the header is read as v2. Multi-record: always rejected by v2
// structural validation (loud, never silent). Pathological single-record:
// the 68 B payload's bytes 40–41 (the record's accel low bytes) decode as
// route_len 24, the v2 length check passes, and the garbage-vehicle_id
// intent dies at CLAIM FILTERING — the named backstop.
func TestTSIBHeaderlessReadAsV2(t *testing.T) {
	// Multi-record: structurally rejected as v2, counted as a dropped v2 msg.
	bus := &Bus{}
	multi := EncodeTSIB(1, []engine.Intent{tsibCodecCases[0], tsibCodecCases[1]})
	bus.onIntent(&nats.Msg{Subject: "ts.r.ctl.intent.alpha", Data: multi})
	if len(bus.buf) != 0 {
		t.Fatalf("header-less multi-record TSIB decoded as v2: %+v", bus.buf)
	}
	if d, _, _ := bus.Stats(); d != 1 {
		t.Fatalf("dropped = %d, want 1 (v2 structural rejection)", d)
	}

	// Pathological single-record: accel bits with low u16 = 24 make the v2
	// length check pass (68 = 44 + 24). The decoded intent is garbage but
	// well-formed v2 — it must die at claim filtering, not at decode.
	pather := EncodeTSIB(1, []engine.Intent{{VehicleID: 1, Accel: math.Float64frombits(24), AccelSet: true}})
	if len(pather) != 68 {
		t.Fatalf("pathological payload = %d bytes, want 68", len(pather))
	}
	in, ok := DecodeIntent(pather)
	if !ok {
		t.Fatal("premise broken: pathological payload no longer passes v2 decode")
	}

	srv := NewTestServer(t)
	nc := srv.Connect(t)
	e := benchIngestEngine(t, 4)
	claimed := e.Vehicles()[0].ID
	if in.VehicleID == claimed {
		t.Fatal("premise broken: garbage vehicle_id collides with the claimed vehicle")
	}
	bus2, err := NewBus(nc, "hl1", e)
	if err != nil {
		t.Fatal(err)
	}
	defer bus2.Close()
	contract, err := NewContract(nc, "hl1", ContractConfig{}, bus2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer contract.Close()
	if err := nc.Flush(); err != nil { // SUBs registered before the hello races them
		t.Fatal(err)
	}
	ctl := benchAttachController(t, contract, e, srv.Connect(t), "hl1", []uint64{claimed})

	bus2.onIntent(&nats.Msg{Subject: SubjectCtlIntent("hl1", ctl), Data: pather})
	keyed := contract.DrainIntents(e)
	if len(keyed) != 0 {
		t.Fatalf("garbage-vehicle intent applied: %+v", keyed)
	}
	violations, _, _ := contract.Stats()
	if violations != 1 {
		t.Fatalf("claimViolations = %d, want 1 (claim filter is the backstop)", violations)
	}
}

// An unknown intent_encoding value is dropped + counted, LOUD — never a
// fall-through to v2 parsing, even when the payload is valid v2. Only an
// ABSENT key means v2: a present-but-EMPTY value (or only empty values) is
// unknown, not v2 (ADR-0026). Headers present but none named
// intent_encoding is still v2.
func TestTSIBUnknownEncodingDropped(t *testing.T) {
	bus := &Bus{}
	m := nats.NewMsg("ts.r.ctl.intent.alpha")
	m.Data = EncodeIntent(tsibCodecCases[0]) // valid v2 payload
	m.Header.Set(headerIntentEncoding, "v9")
	bus.onIntent(m)
	if len(bus.buf) != 0 {
		t.Fatalf("unknown encoding fell through to v2 parsing: %+v", bus.buf)
	}

	// Present-but-empty: "" is not a valid encoding and must NOT read as
	// v2 — pin both the Header.Set form and the bare-key (no values) form.
	for name, hdr := range map[string]nats.Header{
		"empty value":  {headerIntentEncoding: {""}},
		"empty values": {headerIntentEncoding: {"", ""}},
		"key no vals":  {headerIntentEncoding: nil},
	} {
		m := nats.NewMsg("ts.r.ctl.intent.alpha")
		m.Data = EncodeIntent(tsibCodecCases[0])
		m.Header = hdr
		bus.onIntent(m)
		if len(bus.buf) != 0 {
			t.Fatalf("%s: present-empty intent_encoding parsed as v2: %+v", name, bus.buf)
		}
	}
	if _, _, _, _, unknown := bus.IntentBatchStats(); unknown != 4 {
		t.Fatalf("intentEncodingUnknown = %d, want 4 (v9 + 3 present-empty forms)", unknown)
	}

	m2 := nats.NewMsg("ts.r.ctl.intent.alpha")
	m2.Data = EncodeIntent(tsibCodecCases[0])
	m2.Header.Set(headerSchemaVersion, "2") // unrelated header, no intent_encoding
	bus.onIntent(m2)
	if len(bus.buf) != 1 || bus.buf[0].Intent != tsibCodecCases[0] {
		t.Fatalf("headered v2 message without intent_encoding not read as v2: %+v", bus.buf)
	}

	// An EXPLICIT intent_encoding: v2 is an exact synonym for absent — the
	// same message works against a pre-TSIB engine (which ignores headers),
	// so rejecting it would break such a producer on upgrade (M4 review).
	m3 := nats.NewMsg("ts.r.ctl.intent.alpha")
	m3.Data = EncodeIntent(tsibCodecCases[1])
	m3.Header.Set(headerIntentEncoding, "v2")
	bus.onIntent(m3)
	if len(bus.buf) != 2 || bus.buf[1].Intent != tsibCodecCases[1] {
		t.Fatalf("explicit intent_encoding=v2 not read as v2: %+v", bus.buf)
	}
}

// tsibSetup wires a real Bus+Contract over the embedded server with one
// attached drive controller holding claims (the M0 harness at test scale).
func tsibSetup(t *testing.T, run string, nVehicles int, claims []uint64) (*Bus, *Contract, *engine.Engine, string) {
	t.Helper()
	srv := NewTestServer(t)
	nc := srv.Connect(t)
	e := benchIngestEngine(t, nVehicles)
	bus, err := NewBus(nc, run, e)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bus.Close)
	contract, err := NewContract(nc, run, ContractConfig{}, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(contract.Close)
	// NewBus/NewContract subscribe without flushing: push the SUBs to the
	// server before another connection's hello/publish can race past them.
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	ctl := benchAttachController(t, contract, e, srv.Connect(t), run, claims)
	return bus, contract, e, ctl
}

// Mixed v2 + TSIB from the same controller across a tick: drain order is
// arrival order and engine-assigned seqs are monotonic, exactly as for a
// pure v2 stream (expansion appends records in record order).
func TestTSIBMixedV2Ordering(t *testing.T) {
	bus, contract, e, ctl := tsibSetup(t, "mix1", 4, []uint64{1, 2, 3, 4})
	subj := SubjectCtlIntent("mix1", ctl)

	mk := func(vid uint64, accel float64) engine.Intent {
		return engine.Intent{VehicleID: vid, Accel: accel, AccelSet: true}
	}
	bus.onIntent(&nats.Msg{Subject: subj, Data: EncodeIntent(mk(1, 0.1))})
	bus.onIntent(tsibMsg(subj, EncodeTSIB(e.Tick, []engine.Intent{mk(2, 0.2), mk(3, 0.3)})))
	bus.onIntent(&nats.Msg{Subject: subj, Data: EncodeIntent(mk(4, 0.4))})

	keyed := contract.DrainIntents(e)
	if len(keyed) != 4 {
		t.Fatalf("drained %d intents, want 4", len(keyed))
	}
	for i, k := range keyed {
		if k.Intent.VehicleID != uint64(i+1) {
			t.Fatalf("keyed[%d] vehicle = %d, want %d (arrival order)", i, k.Intent.VehicleID, i+1)
		}
		if k.Seq != uint64(i+1) {
			t.Fatalf("keyed[%d] seq = %d, want %d (monotonic)", i, k.Seq, i+1)
		}
		if k.Held {
			t.Fatalf("keyed[%d] held: a fresh intent was filtered", i)
		}
	}
}

// Claim filtering and hold-last apply to expanded records exactly as to v2
// messages: a batch record for an unclaimed vehicle is filtered; a claimed
// vehicle absent from the next batch gets a hold-last re-issue.
func TestTSIBClaimFilterAndHoldLast(t *testing.T) {
	ids := []uint64{1, 3}
	bus, contract, e, ctl := tsibSetup(t, "cf1", 4, ids)
	subj := SubjectCtlIntent("cf1", ctl)
	mk := func(vid uint64) engine.Intent {
		return engine.Intent{VehicleID: vid, Accel: 0.3, AccelSet: true}
	}

	// Tick 1 batch: records for claimed 1 and 3 plus UNCLAIMED 2.
	bus.onIntent(tsibMsg(subj, EncodeTSIB(e.Tick, []engine.Intent{mk(1), mk(2), mk(3)})))
	keyed := contract.DrainIntents(e)
	if len(keyed) != 2 || keyed[0].Intent.VehicleID != 1 || keyed[1].Intent.VehicleID != 3 {
		t.Fatalf("tick 1 drain: %+v (want vehicles 1,3 — 2 filtered)", keyed)
	}
	violations, _, _ := contract.Stats()
	if violations != 1 {
		t.Fatalf("claimViolations = %d, want 1 (unclaimed vehicle 2 filtered)", violations)
	}

	// Tick 2 batch: vehicle 1 absent → hold-last re-issues its last intent.
	e.Step()
	bus.onIntent(tsibMsg(subj, EncodeTSIB(e.Tick, []engine.Intent{mk(3)})))
	keyed = contract.DrainIntents(e)
	var fresh, held int
	for _, k := range keyed {
		switch {
		case k.Held && k.Intent.VehicleID == 1:
			held++
			if k.Intent != mk(1) {
				t.Fatalf("held intent = %+v, want re-issue of %+v", k.Intent, mk(1))
			}
		case !k.Held && k.Intent.VehicleID == 3:
			fresh++
		default:
			t.Fatalf("unexpected keyed intent: %+v", k)
		}
	}
	if fresh != 1 || held != 1 {
		t.Fatalf("tick 2 drain: fresh %d held %d, want 1/1", fresh, held)
	}
}

// Boundary publish (ADR-0026): exactly TSIBMaxRecords records — 880,024 B
// with the header — crosses the real broker and expands whole.
func TestTSIBBoundaryPublishMaxRecords(t *testing.T) {
	srv := NewTestServer(t)
	nc := srv.Connect(t)
	run := "cap1"
	e := benchIngestEngine(t, TSIBMaxRecords)
	bus, err := NewBus(nc, run, e)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	contract, err := NewContract(nc, run, ContractConfig{}, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer contract.Close()

	// NewBus/NewContract subscribe without flushing: push the SUBs to the
	// server before another connection's hello/publish can race past them.
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}
	ids := make([]uint64, 0, TSIBMaxRecords)
	intents := make([]engine.Intent, 0, TSIBMaxRecords)
	for _, v := range e.Vehicles() {
		ids = append(ids, v.ID)
		intents = append(intents, engine.Intent{VehicleID: v.ID, Accel: 0.5, AccelSet: true})
	}
	ctl := benchAttachController(t, contract, e, srv.Connect(t), run, ids)

	payload := EncodeTSIB(e.Tick, intents)
	if len(payload) != tsibHeaderBytes+intentFixedBytes*TSIBMaxRecords {
		t.Fatalf("boundary payload = %d bytes, want %d", len(payload), tsibHeaderBytes+intentFixedBytes*TSIBMaxRecords)
	}
	if err := nc.PublishMsg(tsibMsg(SubjectCtlIntent(run, ctl), payload)); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	waitFor(t, "boundary batch delivery", 30*time.Second, func() bool {
		return benchBufferedIntents(bus) == TSIBMaxRecords
	})

	keyed := contract.DrainIntents(e)
	if len(keyed) != TSIBMaxRecords {
		t.Fatalf("drained %d intents, want %d", len(keyed), TSIBMaxRecords)
	}
	for i, k := range keyed {
		if k.Held {
			t.Fatalf("keyed[%d] held: a fresh intent was filtered", i)
		}
		if k.Intent.VehicleID != ids[i] {
			t.Fatalf("keyed[%d] vehicle = %d, want %d (record order)", i, k.Intent.VehicleID, ids[i])
		}
	}
	batches, records, dropped, recordDropped, unknown := bus.IntentBatchStats()
	if batches != 1 || records != TSIBMaxRecords || dropped != 0 || recordDropped != 0 || unknown != 0 {
		t.Fatalf("counters: batches %d records %d dropped %d recordDropped %d unknown %d, want 1/%d/0/0/0",
			batches, records, dropped, recordDropped, unknown, TSIBMaxRecords)
	}
}

// The engine advertises every intent encoding it accepts (ADR-0026 M4
// addendum): HelloReply.intent_encodings = ["v2","tsib"] from this engine
// onward. Additive both ways — pre-TSIB engines omit the field (the
// driver-side fallback signal), pre-field drivers ignore it.
func TestHelloAdvertisesIntentEncodings(t *testing.T) {
	srv := NewTestServer(t)
	nc := srv.Connect(t)
	e := benchIngestEngine(t, 4)
	bus, err := NewBus(nc, "adv1", e)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	contract, err := NewContract(nc, "adv1", ContractConfig{}, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer contract.Close()
	if err := nc.Flush(); err != nil { // SUBs registered before the hello races them
		t.Fatal(err)
	}

	req, _ := json.Marshal(HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "bench",
		Grants: []string{"drive"}, ClaimCapacity: 1,
	})
	resCh := make(chan *nats.Msg, 1)
	go func() {
		m, err := nc.Request(SubjectCtlHello("adv1"), req, 10*time.Second)
		if err == nil {
			resCh <- m
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case m := <-resCh:
			var rep HelloReply
			if err := json.Unmarshal(m.Data, &rep); err != nil {
				t.Fatal(err)
			}
			if !rep.Accepted {
				t.Fatalf("hello rejected: %s", rep.Reason)
			}
			want := []string{"v2", "tsib"}
			if len(rep.IntentEncodings) != len(want) {
				t.Fatalf("intent_encodings = %v, want %v", rep.IntentEncodings, want)
			}
			for i, enc := range want {
				if rep.IntentEncodings[i] != enc {
					t.Fatalf("intent_encodings = %v, want %v", rep.IntentEncodings, want)
				}
			}
			return
		default:
			if time.Now().After(deadline) {
				t.Fatal("hello never answered")
			}
			if err := contract.ProcessControl(e); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
}
