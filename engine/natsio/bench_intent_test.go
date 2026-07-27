package natsio

// bench_intent_test.go — ADR-0026 M0 baseline: per-vehicle (v2) intent
// ingest cost at fleet scale, BEFORE batched intents (TSIB) land. The
// existing suite (engine/bench_test.go) measures record-plane pubacks at
// 1–100 intents/tick and snapshot fan-out, but has no fleet-scale
// intent-ingest numbers — this is the gap ADR-0026's context table calls
// out ("M0 establishes the real ingest ceiling"), and the baseline the
// TSIB change (M1–M3) is measured against.
//
// (d) BenchmarkIntentIngest — K=4 controller connections, each publishing
//     n/4 v2 intents per tick on its own subject
//     (ts.{run}.ctl.intent.{ctlID}), payloads via EncodeIntent with
//     distinct vehicle ids, against the REAL ingest path: the embedded
//     server routes to Bus.onIntent (callback → lock → append), and the
//     contract layer drains per tick via Contract.DrainIntents (claim
//     filter, seq/grant stamping, hold-last scan). Controllers attach
//     over the wire with the hello handshake (drive grant, claims at
//     attach) so every drained intent passes the claim filter — the
//     numbers cover the full ingest+drain path, stopping before the
//     kernel apply.
//
//     Per tick the harness records TWO spans: publish start → engine
//     buffer full (the delivered/s ceiling) and publish start →
//     DrainIntents return (the complete per-tick ingest cost). The
//     complete-span samples give tick p50/p99 and the drain-complete
//     intents/s rate ADR-0026 M0 asks for.
//
// (d3) BenchmarkIntentIngestBatched — the M3 batch-mode rerun (ADR-0026):
//     the identical harness, fleet sizes, claim sets, per-vehicle intents,
//     and spans, but each controller publishes ONE TSIB per tick
//     (natsio.NewTSIBMsg, the informational header tick patched per tick)
//     instead of n/4 v2 messages. Same expanded records reach
//     DrainIntents, so the drain span isolates the wire/demux change.
//     Adds two M3 acceptance pins: messages per tick ≈ controller count
//     (asserted exactly, via IntentBatchStats), and the applied-lag proxy
//     (drain tick − batch header tick distribution — structurally 0 in-
//     harness since the harness drains at the publish tick; it pins the
//     measurement path. PRODUCTION lag is measured driver-side: the
//     driver's batch tick vs the applied_tick ack, per the M1 triage).
//
// (d2) BenchmarkOnIntentCPU — the M0 "onIntent CPU" deliverable: the
//     Bus.onIntent callback invoked directly (no broker), isolating
//     subject tokenize + decode + lock + append per message.
//
// Numbers + interpretation live in engine/BENCHMARKS.md §(d).

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// benchIngestEngine builds a ring-net engine with n live vehicles (the
// benchEngine pattern of engine/bench_test.go, kept local so this file is
// self-contained in the natsio test package). testing.TB: the ADR-0026 M1
// tests (tsib_test.go) reuse the same Bus/Contract wiring.
func benchIngestEngine(tb testing.TB, n int) *engine.Engine {
	tb.Helper()
	spec := engine.RunSpec{
		Net:    engine.NetSpec{Kind: "ring", Length: 8 * float64(n), SpeedLimit: 33.3},
		Scen:   engine.Scenario{InitialVehicles: n},
		Params: engine.DefaultParams(),
		Seed:   1,
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		tb.Fatal(err)
	}
	e.Step() // one tick so state is representative
	return e
}

// benchBufferedIntents reads the engine-side intent buffer length (the
// delivery watermark for the publish→buffered measurement).
func benchBufferedIntents(bus *Bus) int {
	return bus.BufferedIntents()
}

// benchAttachController runs one controller through the real attach
// handshake: hello with the drive grant and its claims at attach, buffered
// by the wire callback and answered when ProcessControl runs (the run-loop
// drain point). Returns the engine-assigned controller id after verifying
// every claim was granted. testing.TB: reused by the M1 tests (tsib_test.go).
func benchAttachController(tb testing.TB, c *Contract, e *engine.Engine, conn *nats.Conn, run string, claims []uint64) string {
	tb.Helper()
	req, err := json.Marshal(HelloRequest{
		ContractVersion: SchemaVersion,
		ControllerType:  "bench",
		CadenceTicks:    1,
		Grants:          []string{"drive"},
		ClaimCapacity:   len(claims),
		Claims:          claims,
	})
	if err != nil {
		tb.Fatal(err)
	}
	type result struct {
		reply HelloReply
		err   error
	}
	resCh := make(chan result, 1)
	go func() {
		msg, err := conn.Request(SubjectCtlHello(run), req, 30*time.Second)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		var rep HelloReply
		resCh <- result{reply: rep, err: json.Unmarshal(msg.Data, &rep)}
	}()
	// The reply is produced by ProcessControl; wait for the hello to land
	// in the contract's request buffer before draining it.
	deadline := time.Now().Add(30 * time.Second)
	for {
		c.mu.Lock()
		pending := len(c.reqs)
		c.mu.Unlock()
		if pending > 0 {
			break
		}
		if time.Now().After(deadline) {
			tb.Fatal("hello never reached the contract request buffer")
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.ProcessControl(e); err != nil {
		tb.Fatalf("ProcessControl: %v", err)
	}
	res := <-resCh
	if res.err != nil {
		tb.Fatalf("hello: %v", res.err)
	}
	if !res.reply.Accepted {
		tb.Fatalf("hello rejected: %s", res.reply.Reason)
	}
	if len(res.reply.Claims) != len(claims) {
		tb.Fatalf("granted %d/%d claims", len(res.reply.Claims), len(claims))
	}
	return res.reply.ControllerID
}

func BenchmarkIntentIngest(b *testing.B)        { benchmarkIntentIngest(b, false) }
func BenchmarkIntentIngestBatched(b *testing.B) { benchmarkIntentIngest(b, true) }

// benchmarkIntentIngest is the shared M0/M3 harness (see the file header):
// batched=false is the per-vehicle v2 baseline, batched=true the TSIB
// batch-mode rerun — same fleets, claims, intents, spans, and assertions.
func benchmarkIntentIngest(b *testing.B, batched bool) {
	const (
		controllers = 4
		warmupTicks = 3
		// 100 measured ticks: nearest-rank p99 is then the 2nd-highest
		// sample, not the max — one load spike can't set the percentile.
		measTicks = 100
	)
	srv := NewTestServer(b)

	for _, n := range []int{5000, 15000, 30000} {
		b.Run(fmt.Sprintf("vehicles=%d", n), func(b *testing.B) {
			run := fmt.Sprintf("ing%d", n)
			nc := srv.Connect(b)
			e := benchIngestEngine(b, n)
			bus, err := NewBus(nc, run, e)
			if err != nil {
				b.Fatal(err)
			}
			defer bus.Close()
			// Nil recorder: the pause gate never engages here (every
			// vehicle is claimed, and AfterStep is never called), so
			// emitPause — the only recorder consumer — is unreachable.
			contract, err := NewContract(nc, run, ContractConfig{}, bus, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer contract.Close()

			// Split the fleet into K contiguous claim sets and attach one
			// controller per set over its own connection.
			ids := make([]uint64, 0, n)
			for _, v := range e.Vehicles() {
				ids = append(ids, v.ID)
			}
			ctlIDs := make([]string, controllers)
			ctlConns := make([]*nats.Conn, controllers)
			payloads := make([][][]byte, controllers) // v2: pre-encoded, one per claimed vehicle
			batches := make([]*nats.Msg, controllers) // batched: one TSIB per controller, tick patched per tick
			for k := 0; k < controllers; k++ {
				lo := k * n / controllers
				hi := (k + 1) * n / controllers
				chunk := ids[lo:hi]
				conn := srv.Connect(b)
				ctlConns[k] = conn
				ctlIDs[k] = benchAttachController(b, contract, e, conn, run, chunk)
				intents := make([]engine.Intent, len(chunk))
				for i, vid := range chunk {
					intents[i] = engine.Intent{VehicleID: vid, Accel: 0.5, AccelSet: true}
				}
				if batched {
					// n/4 ≤ 7500 ≪ TSIBMaxRecords, so one batch per
					// controller covers the whole chunk — no split here
					// (the split path is covered by TestBatchSplitAtCap).
					batches[k] = NewTSIBMsg(SubjectCtlIntent(run, ctlIDs[k]), 0, intents)
					if batches[k] == nil {
						b.Fatalf("NewTSIBMsg nil: chunk of %d route-free intents over the %d cap", len(intents), TSIBMaxRecords)
					}
				} else {
					msgs := make([][]byte, len(intents))
					for i, in := range intents {
						msgs[i] = EncodeIntent(in)
					}
					payloads[k] = msgs
				}
			}

			// One tick: K publishers concurrently push their share on
			// their own subjects and flush; wait until the engine-side
			// buffer holds all n; then time DrainIntents. The complete
			// span (publish start → DrainIntents return) is collected per
			// tick for p50/p99 and the drain-complete rate.
			var deliverElapsed, drainElapsed time.Duration
			tickTotals := make([]time.Duration, 0, measTicks)
			var lagSamples []int64 // batched: drain tick − batch header tick
			runTick := func(measure bool) {
				hTick := e.Tick
				start := time.Now()
				var wg sync.WaitGroup
				for k := 0; k < controllers; k++ {
					wg.Add(1)
					go func(k int) {
						defer wg.Done()
						if batched {
							m := batches[k]
							// Patch the informational source-obs tick
							// (payload pre-encoded once — same encode-
							// excluded caveat as the v2 payloads).
							binary.LittleEndian.PutUint64(m.Data[tsibTickOff:], hTick)
							if err := ctlConns[k].PublishMsg(m); err != nil {
								b.Errorf("publish: %v", err)
								return
							}
						} else {
							subj := SubjectCtlIntent(run, ctlIDs[k])
							for _, p := range payloads[k] {
								if err := ctlConns[k].Publish(subj, p); err != nil {
									b.Errorf("publish: %v", err)
									return
								}
							}
						}
						if err := ctlConns[k].Flush(); err != nil {
							b.Errorf("flush: %v", err)
						}
					}(k)
				}
				wg.Wait()
				if b.Failed() {
					// A publisher errored: bail now instead of burning the
					// delivery deadline on a short count.
					b.Fatal("aborting after publisher error")
				}
				deadline := time.Now().Add(5 * time.Minute)
				for benchBufferedIntents(bus) < n {
					if time.Now().After(deadline) {
						b.Fatalf("delivery stalled: %d/%d intents buffered", benchBufferedIntents(bus), n)
					}
					time.Sleep(50 * time.Microsecond)
				}
				if measure {
					deliverElapsed += time.Since(start)
				}
				d0 := time.Now()
				keyed := contract.DrainIntents(e)
				if measure {
					drainElapsed += time.Since(d0)
					tickTotals = append(tickTotals, time.Since(start))
					if batched {
						lag := int64(e.Tick) - int64(hTick)
						if lag != 0 {
							b.Fatalf("applied-lag proxy = %d ticks, want 0 (the harness drains at the publish tick)", lag)
						}
						lagSamples = append(lagSamples, lag)
					}
				}
				if len(keyed) != n {
					b.Fatalf("drained %d intents, want %d (short = claim filter dropped some, over = hold-last/duplicate delivery)", len(keyed), n)
				}
				for _, k := range keyed {
					if k.Held {
						b.Fatalf("hold-last re-issue in drain (controller %s vehicle %d): a fresh intent was filtered",
							k.Controller, k.Intent.VehicleID)
					}
				}
				e.Step() // advance the tick; kernel apply is out of scope
			}

			for i := 0; i < warmupTicks; i++ {
				runTick(false)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for t := 0; t < measTicks; t++ {
					runTick(true)
				}
			}
			b.StopTimer()

			ticks := float64(b.N * measTicks)
			sorted := make([]time.Duration, len(tickTotals))
			copy(sorted, tickTotals)
			sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
			var totalSum time.Duration
			for _, d := range tickTotals {
				totalSum += d
			}
			p50 := sorted[(len(sorted)-1)/2]
			p99 := sorted[int(math.Ceil(0.99*float64(len(sorted))))-1]
			msgsPerTick := float64(n)
			if batched {
				msgsPerTick = controllers
			}
			b.ReportMetric(float64(n), "intents/tick")
			b.ReportMetric(msgsPerTick, "msgs/tick")
			b.ReportMetric(float64(n)*ticks/deliverElapsed.Seconds(), "delivered/s")
			b.ReportMetric(float64(drainElapsed.Microseconds())/ticks, "drain_µs/tick")
			b.ReportMetric(float64(p50.Microseconds()), "tick_p50_µs")
			b.ReportMetric(float64(p99.Microseconds()), "tick_p99_µs")
			b.ReportMetric(float64(n)*ticks/totalSum.Seconds(), "complete_intents/s")
			violations, _, _ := contract.Stats()
			if violations != 0 {
				b.Fatalf("claim violations = %d, want 0: the claim/claim-filter setup is broken and the drain numbers are measuring drops",
					violations)
			}
			if batched {
				// M3 acceptance pins: messages per tick == controller count
				// (exactly, warmups included), every record expanded, no
				// batch or record drops; the lag distribution pinned at 0.
				gotBatches, gotRecords, batchDropped, recordDropped, _ := bus.IntentBatchStats()
				allTicks := uint64(warmupTicks) + uint64(b.N*measTicks)
				if want := uint64(controllers) * allTicks; gotBatches != want {
					b.Fatalf("TSIB batches = %d, want %d (one per controller per tick)", gotBatches, want)
				}
				if want := uint64(n) * allTicks; gotRecords != want {
					b.Fatalf("expanded records = %d, want %d", gotRecords, want)
				}
				if batchDropped != 0 || recordDropped != 0 {
					b.Fatalf("drops: batches %d records %d, want 0/0", batchDropped, recordDropped)
				}
				sortedLag := append([]int64(nil), lagSamples...)
				sort.Slice(sortedLag, func(i, j int) bool { return sortedLag[i] < sortedLag[j] })
				lagP50 := sortedLag[(len(sortedLag)-1)/2]
				lagP99 := sortedLag[int(math.Ceil(0.99*float64(len(sortedLag))))-1]
				b.ReportMetric(float64(lagP50), "lag_proxy_p50_ticks")
				b.ReportMetric(float64(lagP99), "lag_proxy_p99_ticks")
			}
			b.Logf("ticks=%d, msgs/tick=%.0f, delivered=%.0f intents/s, drain=%.0f µs/tick, tick p50=%v p99=%v, complete=%.0f intents/s",
				int(ticks), msgsPerTick, float64(n)*ticks/deliverElapsed.Seconds(),
				float64(drainElapsed.Microseconds())/ticks, p50.Round(time.Microsecond), p99.Round(time.Microsecond),
				float64(n)*ticks/totalSum.Seconds())
		})
	}
}

// BenchmarkOnIntentCPU isolates the per-callback CPU of Bus.onIntent — the
// ADR-0026 M0 "onIntent CPU" deliverable: subject tokenize + DecodeIntent +
// lock + append, with no broker, no delivery goroutine, no drain in the
// loop. The publish→buffer span of BenchmarkIntentIngest remains the
// end-to-end proxy: it is this callback cost PLUS broker route and delivery
// scheduling, and is labeled as such in engine/BENCHMARKS.md §(d).
func BenchmarkOnIntentCPU(b *testing.B) {
	srv := NewTestServer(b)
	nc := srv.Connect(b)
	e := benchIngestEngine(b, 100)
	bus, err := NewBus(nc, "cpubench", e)
	if err != nil {
		b.Fatal(err)
	}
	defer bus.Close()

	// Rotate distinct pre-encoded payloads across the 4 controller subjects
	// so the measured path (split + decode + append) sees realistic variety.
	const variants = 64
	msgs := make([]*nats.Msg, 0, variants*4)
	for k := 0; k < 4; k++ {
		subj := SubjectCtlIntent("cpubench", fmt.Sprintf("ctl-%d", k+1))
		for i := 0; i < variants; i++ {
			msgs = append(msgs, &nats.Msg{
				Subject: subj,
				Data:    EncodeIntent(engine.Intent{VehicleID: uint64(k*variants + i + 1), Accel: 0.5, AccelSet: true}),
			})
		}
	}
	b.SetBytes(intentFixedBytes)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bus.onIntent(msgs[i%len(msgs)])
		if len(bus.buf) >= 1<<16 {
			// Reset instead of growing forever: keeps the append's
			// amortized cost in the fleet-scale regime (a 30k/tick buffer)
			// without dragging DrainIntents' copy-alloc into a callback
			// measurement. Nothing is published in this benchmark, so no
			// delivery goroutine touches buf — the unlocked reset is safe.
			bus.buf = bus.buf[:0]
		}
	}
	b.StopTimer()
}

// BenchmarkIntentEncode isolates the DRIVER-side per-tick encode tail the
// M3 complete-delivery claim (ADR-0026) rests on: the v2 driver encodes
// incrementally (N × EncodeIntent, one 44 B alloc each, interleaved with
// the per-tick compute) while the batch driver pays one EncodeTSIB
// (O(records) fixed-section writes + one 24+44n B allocation) AFTER
// collection. Publish/delivery cost is the M0/M3 delivered-rate data; this
// is the encode half, at the M3 fleet sizes. One op = one tick's encode.
// encodeSink keeps the compiler honest: without it the discarded encode
// results are dead-code eliminated (the 30k TSIB row showed 0 allocs).
var encodeSink []byte

func BenchmarkIntentEncode(b *testing.B) {
	for _, n := range []int{5000, 15000, 30000} {
		intents := make([]engine.Intent, n)
		for i := range intents {
			intents[i] = engine.Intent{VehicleID: uint64(i + 1), Accel: 0.5, AccelSet: true}
		}
		b.Run(fmt.Sprintf("vehicles=%d/v2_NxEncodeIntent", n), func(b *testing.B) {
			b.SetBytes(int64(n * intentFixedBytes))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for j := range intents {
					encodeSink = EncodeIntent(intents[j])
				}
			}
		})
		b.Run(fmt.Sprintf("vehicles=%d/tsib_EncodeTSIB", n), func(b *testing.B) {
			b.SetBytes(int64(tsibHeaderBytes + n*intentFixedBytes))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				// The production shape: ⌈n/TSIBMaxRecords⌉ batches (the M2
				// driver splits at the cap; EncodeTSIB nils over it).
				for off := 0; off < n; off += TSIBMaxRecords {
					encodeSink = EncodeTSIB(1, intents[off:min(off+TSIBMaxRecords, n)])
				}
			}
		})
	}
}
