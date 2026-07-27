package driver

// batch_test.go — ADR-0026 M2: the driver's per-tick TSIB aggregation on
// the wire. One batch per cadence tick per controller with records in ego
// (observation slice) order; a route-update vehicle rides ONE complete
// standalone v2 and is omitted from that tick's batch; fleets past
// natsio.TSIBMaxRecords split into well-formed batches covering every
// vehicle exactly once; IntentBatchOff restores the pre-M2 per-vehicle v2
// stream byte-for-byte. The harness is the route-budget tests' pattern: a
// directly-constructed Driver against an in-process broker, obs frames
// crafted via natsio.EncodeObs, the intent subject tapped.

import (
	"encoding/binary"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// The demux header key/value are unexported in natsio (producers go through
// natsio.NewTSIBMsg); the wire contract strings are pinned here literally.
const (
	tapHeaderKey  = "intent_encoding"
	tapHeaderTSIB = "tsib"
)

// wireTap records every message on the driver's intent subject (payload +
// headers) for batch-shape assertions.
type wireTap struct {
	mu   sync.Mutex
	msgs []*nats.Msg
}

func (w *wireTap) subscribe(t *testing.T, nc *nats.Conn, run, id string) {
	t.Helper()
	_, err := nc.Subscribe(natsio.SubjectCtlIntent(run, id), func(m *nats.Msg) {
		w.mu.Lock()
		w.msgs = append(w.msgs, m)
		w.mu.Unlock()
	})
	if err != nil {
		t.Fatalf("subscribe intents: %v", err)
	}
}

// waitCount blocks until at least want messages arrived and returns a copy.
func (w *wireTap) waitCount(t *testing.T, want int) []*nats.Msg {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		n := len(w.msgs)
		w.mu.Unlock()
		if n >= want {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.msgs) < want {
		t.Fatalf("intent messages = %d, want %d", len(w.msgs), want)
	}
	return append([]*nats.Msg(nil), w.msgs...)
}

// settle returns the message count after a quiet interval — the only honest
// way to assert NOTHING further is published (the subscriber runs on a
// delivery goroutine; a single lucky read false-passes, sol review).
func (w *wireTap) settle() int {
	stable, final := 0, -1
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		n := len(w.msgs)
		w.mu.Unlock()
		if n == final {
			stable++
			if stable >= 10 { // ~100 ms quiet
				return final
			}
		} else {
			stable, final = 0, n
		}
		time.Sleep(10 * time.Millisecond)
	}
	return final
}

// batchDriver builds a directly-constructed Driver (the route-budget test
// pattern). No Destination/ExitRouting: every ego adopts routing on arrival
// (routeStep's want == "" branch), so the steady-state batch path is what
// the wire shows unless a test configures otherwise.
func batchDriver(nc *nats.Conn, cfg Config) *Driver {
	cfg.Run = "t"
	d := &Driver{
		nc:        nc,
		cfg:       cfg.withDefaults(),
		id:        "drv",
		spec:      engine.RunSpec{Seed: 42},
		types:     []*engine.VehicleType{&engine.Car},
		fleet:     map[uint64]bool{},
		pending:   map[uint64]bool{},
		routed:    map[uint64]bool{},
		streams:   map[uint64]*engine.Stream{},
		wantRoute: map[uint64]string{},
		wantLane:  map[uint64]int{},
		done:      make(chan struct{}),
	}
	return d
}

func obsMsg(tick uint64, features uint16, egos []natsio.ObsEgo) *nats.Msg {
	return &nats.Msg{Data: natsio.EncodeObs(tick, features, egos, nil)}
}

// decodeTSIBMsg unwraps one tapped message as a TSIB batch, asserting the
// demux header and a clean decode.
func decodeTSIBMsg(t *testing.T, m *nats.Msg, tick uint64) []engine.Intent {
	t.Helper()
	if enc := m.Header.Get(tapHeaderKey); enc != tapHeaderTSIB {
		t.Fatalf("intent_encoding = %q, want %q", enc, tapHeaderTSIB)
	}
	recs, drops, ok := natsio.DecodeTSIB(m.Data)
	if !ok || drops != 0 {
		t.Fatalf("TSIB decode: ok %v drops %d", ok, drops)
	}
	if got := binary.LittleEndian.Uint64(m.Data[8:]); got != tick {
		t.Fatalf("batch header tick = %d, want %d (source obs tick)", got, tick)
	}
	return recs
}

// One observation frame ⇒ exactly ONE TSIB message on the intent subject,
// records in ego (frame slice) order, header tick = source obs tick — one
// batch per cadence tick per controller.
func TestBatchOnePerTickInEgoOrder(t *testing.T) {
	_, nc := startTestBroker(t)
	d := batchDriver(nc, Config{})
	var tap wireTap
	tap.subscribe(t, nc, "t", "drv")

	// Deliberately unsorted ids: record order must be the frame's, not id
	// order — engine-assigned seqs stay monotonic in arrival order.
	egos := []natsio.ObsEgo{{ID: 11}, {ID: 7}, {ID: 42}, {ID: 3}, {ID: 28}}
	d.onObs(obsMsg(1, 0, egos))
	msgs := tap.waitCount(t, 1)
	recs := decodeTSIBMsg(t, msgs[0], 1)
	if len(recs) != len(egos) {
		t.Fatalf("batch records = %d, want %d", len(recs), len(egos))
	}
	for i, r := range recs {
		if r.VehicleID != egos[i].ID {
			t.Fatalf("record %d vehicle = %d, want %d (ego order)", i, r.VehicleID, egos[i].ID)
		}
	}

	// Next cadence tick: exactly one more batch — one per tick, no strays.
	d.onObs(obsMsg(2, 0, egos))
	tap.waitCount(t, 2)
	if got := tap.settle(); got != 2 {
		t.Fatalf("messages after 2 obs ticks = %d, want 2 (one batch per tick)", got)
	}
}

// Route-update tick (ADR-0026): a vehicle whose destination goes out that
// tick rides ONE complete standalone v2 — every computed axis plus the
// route, no intent_encoding header — and is omitted from the tick's batch.
// Once the obs echo confirms the route, the vehicle returns to the batch.
func TestBatchRouteUpdateTickStandaloneV2(t *testing.T) {
	_, nc := startTestBroker(t)
	d := batchDriver(nc, Config{Destination: "D1"})
	var tap wireTap
	tap.subscribe(t, nc, "t", "drv")

	// A minimal policy ctx (Cooldown 1 skips the lateral decision): enough
	// for a finite IDM accel, so the standalone v2 provably carries the
	// computed axes, not just the route.
	ctx := &engine.PolicyCtx{Type: &engine.Car, CurLimit: 33.3, CurLen: 1000, V: 10, F: 1, S: 100}
	wantAccel := ctx.DecideAccel()
	mkEgos := func(route string) []natsio.ObsEgo {
		return []natsio.ObsEgo{
			{ID: 1, Route: route, Cooldown: 1, V: 10, F: 1, S: 100, Ctx: ctx},
			{ID: 2, Route: route, Cooldown: 1, V: 10, F: 1, S: 100, Ctx: ctx},
			{ID: 3, Route: route, Cooldown: 1, V: 10, F: 1, S: 100, Ctx: ctx},
		}
	}

	// Tick 1: every ego needs its route sent → ALL traffic is standalone
	// v2; no batch exists to publish.
	d.onObs(obsMsg(1, natsio.ObsFeaturePolicyCtx, mkEgos("")))
	msgs := tap.waitCount(t, 3)
	if got := tap.settle(); got != 3 {
		t.Fatalf("tick 1 messages = %d, want 3 standalone v2 (no batch)", got)
	}
	for i, m := range msgs {
		if _, present := m.Header[tapHeaderKey]; present {
			t.Fatalf("msg %d: route-update v2 carries an intent_encoding header", i)
		}
		in, ok := natsio.DecodeIntent(m.Data)
		if !ok {
			t.Fatalf("msg %d: not a valid v2 intent", i)
		}
		wantID := uint64(i + 1) // publish order = ego order
		if in.VehicleID != wantID || !in.RouteSet || in.Route != "D1" {
			t.Fatalf("msg %d: %+v, want vehicle %d route D1", i, in, wantID)
		}
		if !in.AccelSet || in.Accel != wantAccel || !in.SignalSet {
			t.Fatalf("msg %d: axes lost in the standalone v2: %+v (want accel %v)", i, in, wantAccel)
		}
	}

	// Tick 2: the obs echoes the destinations (engine applied them) → all
	// three egos routed, all three back in ONE batch, no new v2.
	d.onObs(obsMsg(2, natsio.ObsFeaturePolicyCtx, mkEgos("D1")))
	msgs = tap.waitCount(t, 4)
	if got := tap.settle(); got != 4 {
		t.Fatalf("tick 2 messages = %d, want 4 (3 v2 + 1 batch)", got)
	}
	recs := decodeTSIBMsg(t, msgs[3], 2)
	if len(recs) != 3 {
		t.Fatalf("tick 2 batch records = %d, want 3 (route-confirmed egos back in the batch)", len(recs))
	}
	for i, r := range recs {
		if r.VehicleID != uint64(i+1) || r.RouteSet {
			t.Fatalf("record %d = %+v, want vehicle %d route-free", i, r, i+1)
		}
	}
}

// Past TSIBMaxRecords claimed vehicles the tick splits into multiple
// well-formed batches, every record ≤ cap, every vehicle covered exactly
// once, ego order preserved across the split.
func TestBatchSplitAtCap(t *testing.T) {
	_, nc := startTestBroker(t)
	d := batchDriver(nc, Config{})
	var tap wireTap
	tap.subscribe(t, nc, "t", "drv")

	const extra = 5
	n := natsio.TSIBMaxRecords + extra
	egos := make([]natsio.ObsEgo, n)
	for i := range egos {
		egos[i] = natsio.ObsEgo{ID: uint64(i + 1)}
	}
	d.onObs(obsMsg(1, 0, egos))
	msgs := tap.waitCount(t, 2)
	if got := tap.settle(); got != 2 {
		t.Fatalf("messages = %d, want 2 split batches", got)
	}

	seen := map[uint64]int{}
	var order []uint64
	for i, m := range msgs {
		recs := decodeTSIBMsg(t, m, 1)
		want := natsio.TSIBMaxRecords
		if i == 1 {
			want = extra
		}
		if len(recs) != want {
			t.Fatalf("batch %d records = %d, want %d", i, len(recs), want)
		}
		for _, r := range recs {
			seen[r.VehicleID]++
			order = append(order, r.VehicleID)
		}
	}
	if len(order) != n {
		t.Fatalf("covered %d records, want %d", len(order), n)
	}
	for i, id := range order {
		if id != egos[i].ID {
			t.Fatalf("record %d vehicle = %d, want %d (ego order across the split)", i, id, egos[i].ID)
		}
	}
	for _, e := range egos {
		if seen[e.ID] != 1 {
			t.Fatalf("vehicle %d covered %d times, want exactly once", e.ID, seen[e.ID])
		}
	}
}

// IntentBatchOff is the pre-M2 wire shape byte-for-byte: one headerless 44 B
// v2 message per ego per tick, in ego order.
func TestBatchOffByteIdenticalV2(t *testing.T) {
	_, nc := startTestBroker(t)
	d := batchDriver(nc, Config{IntentBatchOff: true})
	var tap wireTap
	tap.subscribe(t, nc, "t", "drv")

	egos := []natsio.ObsEgo{{ID: 5}, {ID: 9}, {ID: 1}}
	d.onObs(obsMsg(1, 0, egos))
	msgs := tap.waitCount(t, 3)
	if got := tap.settle(); got != 3 {
		t.Fatalf("batch-off messages = %d, want 3 (per-vehicle v2)", got)
	}
	for i, m := range msgs {
		if _, present := m.Header[tapHeaderKey]; present {
			t.Fatalf("msg %d: batch-off v2 carries an intent_encoding header", i)
		}
		want := natsio.EncodeIntent(engine.Intent{VehicleID: egos[i].ID})
		if string(m.Data) != string(want) {
			t.Fatalf("msg %d payload = % x, want the pre-M2 v2 bytes % x", i, m.Data, want)
		}
	}
}

// Capability fallback (ADR-0026 M4 addendum): a hello reply WITHOUT
// intent_encodings (a pre-TSIB engine) flips a batch-default driver into
// v2 mode for the session — the IntentBatchOff path, byte-identical to the
// pre-M2 stream. With "tsib" advertised the batch path stays.
func TestIntentEncodingFallback(t *testing.T) {
	egos := []natsio.ObsEgo{{ID: 5}, {ID: 9}, {ID: 1}}

	t.Run("fallback on missing capability", func(t *testing.T) {
		_, nc := startTestBroker(t)
		d := batchDriver(nc, Config{})
		d.applyIntentEncodings(nil) // pre-TSIB engine: field omitted
		if !d.cfg.IntentBatchOff {
			t.Fatal("fallback did not engage: driver still in batch mode")
		}
		var tap wireTap
		tap.subscribe(t, nc, "t", "drv")
		d.onObs(obsMsg(1, 0, egos))
		msgs := tap.waitCount(t, 3)
		if got := tap.settle(); got != 3 {
			t.Fatalf("fallback session messages = %d, want 3 (per-vehicle v2)", got)
		}
		for i, m := range msgs {
			if _, present := m.Header[tapHeaderKey]; present {
				t.Fatalf("msg %d carries an intent_encoding header after fallback", i)
			}
			want := natsio.EncodeIntent(engine.Intent{VehicleID: egos[i].ID})
			if string(m.Data) != string(want) {
				t.Fatalf("msg %d payload = % x, want the v2 bytes % x", i, m.Data, want)
			}
		}
	})

	t.Run("batch on tsib advertised", func(t *testing.T) {
		_, nc := startTestBroker(t)
		d := batchDriver(nc, Config{})
		d.applyIntentEncodings([]string{"v2", "tsib"})
		if d.cfg.IntentBatchOff {
			t.Fatal("fallback engaged against a TSIB-capable engine")
		}
		var tap wireTap
		tap.subscribe(t, nc, "t", "drv")
		d.onObs(obsMsg(1, 0, egos))
		msgs := tap.waitCount(t, 1)
		if got := tap.settle(); got != 1 {
			t.Fatalf("advertised session messages = %d, want 1 (one TSIB batch)", got)
		}
		if recs := decodeTSIBMsg(t, msgs[0], 1); len(recs) != len(egos) {
			t.Fatalf("batch records = %d, want %d", len(recs), len(egos))
		}
	})
}
