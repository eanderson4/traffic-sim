package engine_test

// bench_test.go — the deferred bring-up benchmarks from the
// arch-nats-backbone open questions (ADR-0006 consequences: "benchmark
// before sizing batch multipliers"):
//
//	(a) BenchmarkJetStreamPubAck — JetStream publish-ack latency (R=1,
//	    file, default sync) vs the 100 ms tick, at 1×/10×/100× batch
//	    intent rates.
//	(b) BenchmarkSnapshotBytes — snapshot (and keyframe) byte curve vs
//	    vehicle count (100, 1k, 10k), feeding the keyframe-chunking
//	    decision (ADR-0002's 1 MB discipline).
//	(c) BenchmarkLiveFanout — live-plane fan-out to 1 and 8 subscribers.
//
// Numbers + interpretation live in engine/BENCHMARKS.md.

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// (a) JetStream publish-ack latency per tick-batch: N intent-log-sized
// messages with the recorder's dedup + OCC headers, published async, all
// pubacks awaited (the record-plane contract the tick depends on). One op =
// one tick-batch.
func BenchmarkJetStreamPubAck(b *testing.B) {
	srv := natsio.NewTestServer(b)
	_, js := srv.JetStream(b)

	for _, batch := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("intents=%d", batch), func(b *testing.B) {
			stream := fmt.Sprintf("TS_BENCH_ACK_%d", batch)
			subj := fmt.Sprintf("bench.ack.%d.log.intent", batch)
			if _, err := js.AddStream(&nats.StreamConfig{
				Name:      stream,
				Subjects:  []string{fmt.Sprintf("bench.ack.%d.log.>", batch)},
				Retention: nats.LimitsPolicy,
				Storage:   nats.FileStorage,
				Replicas:  1,
			}); err != nil {
				b.Fatal(err)
			}
			// The benchmark harness may re-enter this closure while the
			// stream persists: sync the OCC cursor instead of assuming 0.
			info, err := js.StreamInfo(stream)
			if err != nil {
				b.Fatal(err)
			}
			payload := natsio.EncodeIntent(engine.Intent{VehicleID: 7, Accel: -1.5, AccelSet: true, LaneDelta: 1})

			lastSeq := info.State.LastSeq
			nonce := time.Now().UnixNano()
			var tick uint64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tick++
				futures := make([]nats.PubAckFuture, 0, batch)
				for j := 0; j < batch; j++ {
					expected := lastSeq + uint64(len(futures)) + 1
					msg := nats.NewMsg(subj)
					msg.Data = payload
					msg.Header.Set("tick", strconv.FormatUint(tick, 10))
					msg.Header.Set("schema_version", "1")
					msg.Header.Set(nats.MsgIdHdr, fmt.Sprintf("bench:%d:%d:%d", nonce, tick, j))
					msg.Header.Set(nats.ExpectedLastSeqHdr, strconv.FormatUint(expected-1, 10))
					f, err := js.PublishMsgAsync(msg)
					if err != nil {
						b.Fatalf("publish: %v", err)
					}
					futures = append(futures, f)
				}
				for _, f := range futures {
					select {
					case ack := <-f.Ok():
						lastSeq = ack.Sequence
					case err := <-f.Err():
						b.Fatalf("puback: %v", err)
					case <-time.After(10 * time.Second):
						b.Fatal("puback timeout")
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(batch), "msgs/op")
		})
	}
}

// (b) Snapshot and keyframe byte curve vs vehicle count. The kernel ring
// places the whole population in one shot (O(n log n) setup).
func benchEngine(b *testing.B, n int) *engine.Engine {
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

func BenchmarkSnapshotBytes(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("vehicles=%d", n), func(b *testing.B) {
			e := benchEngine(b, n)
			geoms := natsio.LaneGeoms(e.Net)
			kf, err := e.MarshalState()
			if err != nil {
				b.Fatal(err)
			}
			snap := natsio.SnapshotFrame(e, geoms)
			b.Logf("snapshot %d B (%.1f B/veh), keyframe %d B (%.1f B/veh)",
				len(snap), float64(len(snap))/float64(n), len(kf), float64(len(kf))/float64(n))
			b.ReportMetric(float64(len(snap)), "snap_bytes")
			b.ReportMetric(float64(len(kf)), "keyframe_bytes")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = natsio.SnapshotFrame(e, geoms)
			}
		})
	}
}

// (c) Live-plane fan-out: one ~100-vehicle snapshot frame published to 1
// and 8 concurrent subscribers. Publisher-side op time is the tick-budget
// number; the delivered-rate metric is the end-to-end fan-out rate.
func BenchmarkLiveFanout(b *testing.B) {
	srv := natsio.NewTestServer(b)
	nc := srv.Connect(b)
	e := benchEngine(b, 100)
	frame := natsio.SnapshotFrame(e, natsio.LaneGeoms(e.Net))
	const subj = "bench.fanout.state.snap"

	for _, subs := range []int{1, 8} {
		b.Run(fmt.Sprintf("subs=%d", subs), func(b *testing.B) {
			var delivered atomic.Uint64
			conns := make([]*nats.Conn, 0, subs)
			for i := 0; i < subs; i++ {
				c := srv.Connect(b)
				conns = append(conns, c)
				sub, err := c.Subscribe(subj, func(*nats.Msg) { delivered.Add(1) })
				if err != nil {
					b.Fatal(err)
				}
				defer func() { _ = sub.Unsubscribe() }()
				// Make the interest visible to the server before the first
				// publish (core NATS drops when there is no interest).
				if err := c.Flush(); err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()
			start := time.Now()
			for i := 0; i < b.N; i++ {
				if err := nc.Publish(subj, frame); err != nil {
					b.Fatalf("publish: %v", err)
				}
			}
			b.StopTimer()
			// Drain: everything published must arrive somewhere.
			for _, c := range conns {
				if err := c.Flush(); err != nil {
					b.Fatal(err)
				}
			}
			deadline := time.Now().Add(30 * time.Second)
			want := uint64(b.N * subs)
			for delivered.Load() < want && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			got := delivered.Load()
			if got != want {
				b.Fatalf("delivered %d/%d messages", got, want)
			}
			rate := float64(got) / time.Since(start).Seconds()
			b.ReportMetric(rate, "delivered/s")
			b.ReportMetric(float64(len(frame)), "bytes")
		})
	}
}
