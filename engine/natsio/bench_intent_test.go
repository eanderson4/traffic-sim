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
// (d2) BenchmarkOnIntentCPU — the M0 "onIntent CPU" deliverable: the
//     Bus.onIntent callback invoked directly (no broker), isolating
//     subject tokenize + decode + lock + append per message.
//
// Numbers + interpretation live in engine/BENCHMARKS.md §(d).

import (
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
// self-contained in the natsio test package).
func benchIngestEngine(b *testing.B, n int) *engine.Engine {
	b.Helper()
	spec := engine.RunSpec{
		Net:    engine.NetSpec{Kind: "ring", Length: 8 * float64(n), SpeedLimit: 33.3},
		Scen:   engine.Scenario{InitialVehicles: n},
		Params: engine.DefaultParams(),
		Seed:   1,
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		b.Fatal(err)
	}
	e.Step() // one tick so state is representative
	return e
}

// benchBufferedIntents reads the engine-side intent buffer length (the
// delivery watermark for the publish→buffered measurement). Internal test
// package access; the lock hold is nanoseconds.
func benchBufferedIntents(bus *Bus) int {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	return len(bus.buf)
}

// benchAttachController runs one controller through the real attach
// handshake: hello with the drive grant and its claims at attach, buffered
// by the wire callback and answered when ProcessControl runs (the run-loop
// drain point). Returns the engine-assigned controller id after verifying
// every claim was granted.
func benchAttachController(b *testing.B, c *Contract, e *engine.Engine, conn *nats.Conn, run string, claims []uint64) string {
	b.Helper()
	req, err := json.Marshal(HelloRequest{
		ContractVersion: SchemaVersion,
		ControllerType:  "bench",
		CadenceTicks:    1,
		Grants:          []string{"drive"},
		ClaimCapacity:   len(claims),
		Claims:          claims,
	})
	if err != nil {
		b.Fatal(err)
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
			b.Fatal("hello never reached the contract request buffer")
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.ProcessControl(e); err != nil {
		b.Fatalf("ProcessControl: %v", err)
	}
	res := <-resCh
	if res.err != nil {
		b.Fatalf("hello: %v", res.err)
	}
	if !res.reply.Accepted {
		b.Fatalf("hello rejected: %s", res.reply.Reason)
	}
	if len(res.reply.Claims) != len(claims) {
		b.Fatalf("granted %d/%d claims", len(res.reply.Claims), len(claims))
	}
	return res.reply.ControllerID
}

func BenchmarkIntentIngest(b *testing.B) {
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
			payloads := make([][][]byte, controllers) // pre-encoded, one per claimed vehicle
			for k := 0; k < controllers; k++ {
				lo := k * n / controllers
				hi := (k + 1) * n / controllers
				chunk := ids[lo:hi]
				conn := srv.Connect(b)
				ctlConns[k] = conn
				ctlIDs[k] = benchAttachController(b, contract, e, conn, run, chunk)
				msgs := make([][]byte, len(chunk))
				for i, vid := range chunk {
					msgs[i] = EncodeIntent(engine.Intent{VehicleID: vid, Accel: 0.5, AccelSet: true})
				}
				payloads[k] = msgs
			}

			// One tick: K publishers concurrently push their share on
			// their own subjects and flush; wait until the engine-side
			// buffer holds all n; then time DrainIntents. The complete
			// span (publish start → DrainIntents return) is collected per
			// tick for p50/p99 and the drain-complete rate.
			var deliverElapsed, drainElapsed time.Duration
			tickTotals := make([]time.Duration, 0, measTicks)
			runTick := func(measure bool) {
				start := time.Now()
				var wg sync.WaitGroup
				for k := 0; k < controllers; k++ {
					wg.Add(1)
					go func(k int) {
						defer wg.Done()
						subj := SubjectCtlIntent(run, ctlIDs[k])
						for _, p := range payloads[k] {
							if err := ctlConns[k].Publish(subj, p); err != nil {
								b.Errorf("publish: %v", err)
								return
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
			b.ReportMetric(float64(n), "intents/tick")
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
			b.Logf("ticks=%d, delivered=%.0f intents/s, drain=%.0f µs/tick, tick p50=%v p99=%v, complete=%.0f intents/s",
				int(ticks), float64(n)*ticks/deliverElapsed.Seconds(),
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
