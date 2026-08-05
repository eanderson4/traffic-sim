package natsio

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// intentload_test.go — DIAGNOSTIC (2026-07-26), not a behavioral test.
//
// Question it answers: is the controller-saturation knee (~12,000 active
// vehicles → 35.75% of vehicle-ticks with no applied intent, against 0.03%
// at ~7,700 — docs/kb/articles/concepts/silent-fidelity-failures.md) a
// TRANSPORT defect or a compute limit?
//
// It isolates the intent plane from the traffic model completely: no
// driving, no kernel step, no network of any size. It publishes N encoded
// intents per tick onto ts.{run}.ctl.intent.{ctl} at a fixed cadence and
// counts how many the Bus actually hands back from DrainIntents.
//
// The mechanism under suspicion: the driver publishes ONE core-NATS message
// per claimed vehicle per tick (driver.go onObs), while observations travel
// the other way as ONE batched frame. NewBus subscribes the intent wildcard
// with a plain nc.Subscribe and never calls SetPendingLimits, so the
// nats.go defaults apply (65,536 messages / 64 MiB of undelivered backlog).
// Past those the client DROPS and bumps sub.Dropped() — a counter nothing
// in this repo reads. Bus.dropped counts only malformed/oversized payloads,
// so a transport drop is invisible on every plane we currently log.
//
// Gated behind an env var: it is a load sweep, it takes tens of seconds,
// and it asserts nothing about correctness. Run it deliberately:
//
//	TS_INTENT_LOAD=1 go test ./engine/natsio/ -run TestIntentPlaneLoad -v
func TestIntentPlaneLoad(t *testing.T) {
	if os.Getenv("TS_INTENT_LOAD") == "" {
		t.Skip("diagnostic sweep; set TS_INTENT_LOAD=1 to run")
	}

	// Fleet sizes bracketing the reported knee. 7,700 and 12,000 are the two
	// measured points; the rest frame them.
	fleets := []int{1000, 4000, 7700, 12000, 20000, 40000}
	const ticks = 60

	for _, tickHz := range []int{10, 0} {
		label := fmt.Sprintf("%dHz", tickHz)
		if tickHz == 0 {
			label = "free-running"
		}
		t.Run(label, func(t *testing.T) {
			for _, n := range fleets {
				sent, drained, subDropped, badPayload, elapsed := intentSweep(t, n, ticks, tickHz)
				lost := sent - drained
				pct := 0.0
				if sent > 0 {
					pct = 100 * float64(lost) / float64(sent)
				}
				t.Logf("fleet %6d | sent %8d | drained %8d | LOST %8d (%5.2f%%) | sub.Dropped %6d | malformed %d | %v (%.0f ticks/s)",
					n, sent, drained, lost, pct, subDropped, badPayload,
					elapsed.Round(time.Millisecond), float64(ticks)/elapsed.Seconds())
			}
		})
	}
}

// intentSweep publishes n intents per tick for `ticks` ticks from a separate
// connection (the driver is a separate nats.Conn in cmd/serve, sharing the
// process), draining between ticks the way the contract layer does.
// tickHz == 0 means free-running: publish the next tick's batch as soon as
// the previous drain returns, which is what -pace 0 approximates.
func intentSweep(t *testing.T, n, ticks, tickHz int) (sent, drained, subDropped, badPayload uint64, elapsed time.Duration) {
	t.Helper()

	srv := NewTestServer(t)
	nc := srv.Connect(t)

	spec, err := engine.DefaultSpec("lanedrop", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	run := fmt.Sprintf("load%d", n)
	b, err := NewBus(nc, run, e)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	// Separate connection for the publisher: same shape as serve, where the
	// driver replica holds its own conn to the embedded server.
	pub := srv.Connect(t)
	subj := SubjectCtlIntent(run, "drv-load")

	// Encode once per vehicle id — the sweep is measuring transport, and
	// re-encoding every tick would charge the publisher for work the real
	// driver also does but which is not what is under test.
	payloads := make([][]byte, n)
	for i := 0; i < n; i++ {
		payloads[i] = EncodeIntent(engine.Intent{
			VehicleID: uint64(i + 1),
			Accel:     0.5,
			AccelSet:  true,
		})
	}

	var interval time.Duration
	if tickHz > 0 {
		interval = time.Second / time.Duration(tickHz)
	}

	start := time.Now()
	for tk := 0; tk < ticks; tk++ {
		tickStart := time.Now()
		for i := 0; i < n; i++ {
			if err := pub.Publish(subj, payloads[i]); err == nil {
				sent++
			}
		}
		if err := pub.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		drained += uint64(len(b.DrainIntents()))
		if interval > 0 {
			if rest := interval - time.Since(tickStart); rest > 0 {
				time.Sleep(rest)
			}
		}
	}
	elapsed = time.Since(start)

	// Settle: anything still in flight is not a loss, so give the delivery
	// goroutine a chance to finish before the final drain. Generous on
	// purpose — the claim "messages were lost" has to survive the obvious
	// objection that we simply stopped counting too early.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		got := uint64(len(b.DrainIntents()))
		drained += got
		if got == 0 && drained >= sent {
			break
		}
	}

	d, err := b.sub.Dropped()
	if err != nil {
		t.Fatalf("Dropped: %v", err)
	}
	return sent, drained, uint64(d), b.dropped.Load(), elapsed
}

// TestObsFrameSizeCliff — the OTHER direction of the controller plane, and
// the one the intent sweep above exonerates by elimination.
//
// The observation frame is ONE core-NATS message per controller per tick
// carrying every claimed ego (obsframe.go): 24 B header + 392 B per ego
// with the PolicyCtx feature (75 fixed + 317 ctx) + the route string. The
// embedded broker caps a message at 4 MiB (cmd/serve/main.go MaxPayload),
// so the frame stops being deliverable somewhere near 10,000 egos.
//
// Two things make that a silent failure rather than a loud one:
//
//   - contract.go publishObs discards the publish error —
//     `if err := c.nc.PublishMsg(msg); err == nil { c.obsOut.Add(1) }`.
//     Nothing counts or logs the failure. Compare the snapshot plane, which
//     is loud by design, and the signal table, which ADR-0016 CHUNKED for
//     exactly this cap and which refuses to start a run whose chunk cannot
//     fit. The observation plane got neither treatment.
//   - A controller that receives no frame emits no intents, so every
//     claimed vehicle falls out of the hold-last window ((cadence−1) +
//     HoldLastTicks, = 2 ticks at cadence 1) and then coasts at Acc = 0.
//
// That predicts the reported knee (~7,700 vehicles → 0.03% coasting;
// ~12,000 → 35.75%) and it explains the detail that looked like evidence
// for a timing effect — identical coasting at -pace 0 and -pace 4. A
// payload cap is not a timing phenomenon at all; it does not care how fast
// the run goes, which is exactly what was observed.
//
// This test finds the cliff empirically instead of trusting the arithmetic.
func TestObsFrameSizeCliff(t *testing.T) {
	// A bespoke server rather than NewTestServer: that helper also sets the
	// 4 MiB cap (server.go), so the cap is not the reason — this test wants
	// a bare broker with no JetStream and no store, because it publishes
	// ~20 MiB of frames per sweep and none of them are ever consumed.
	// MaxPayload is restated here anyway: it is the quantity under test, and
	// inheriting it silently from a helper would let an edit there move this
	// test's cliff without anything looking wrong.
	ns, err := server.NewServer(&server.Options{
		DontListen: true,
		MaxPayload: 4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server not ready")
	}
	defer ns.Shutdown()

	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	// Route strings are what pushes a marginal frame over: exit-lane ids on
	// an OSM import are long. "" is the floor, 24 B a realistic import.
	cliff := map[int]int{}
	for _, routeLen := range []int{0, 24} {
		t.Run(fmt.Sprintf("route%dB", routeLen), func(t *testing.T) {
			route := ""
			for len(route) < routeLen {
				route += "x"
			}
			var lastOK, firstFail int
			for _, n := range []int{7700, 9000, 10000, 10500, 10700, 11000, 12000, 15000, 20000} {
				egos := make([]ObsEgo, n)
				for i := range egos {
					egos[i] = ObsEgo{
						ID: uint64(i + 1), S: 10, V: 12, F: 1, Acc: 0.2,
						Route: route,
						Ctx:   &engine.PolicyCtx{CurLimit: 13.4, CurLen: 200},
					}
				}
				data := EncodeObs(1, ObsFeaturePolicyCtx, egos, nil)
				// Frame size must be exactly the documented arithmetic; the
				// ~10,200 knee quoted in serve's warning and in the show docs
				// is derived from it, so a layout change that moved it must
				// fail here rather than silently relocate the cliff.
				if want := obsHeader + n*(obsEgoFix+obsCtxSize+routeLen); len(data) != want {
					t.Fatalf("frame for %d egos (route %d B) = %d B, want %d",
						n, routeLen, len(data), want)
				}
				err := nc.Publish(SubjectCtlObs("cliff", "drv"), data)
				status := "ok"
				if err != nil {
					status = "FAILS: " + err.Error()
					if firstFail == 0 {
						firstFail = n
					}
				} else {
					lastOK = n
					if firstFail != 0 {
						t.Fatalf("frame for %d egos published AFTER %d had already failed — the cap is not monotonic in frame size, which invalidates the whole notion of a cliff",
							n, firstFail)
					}
				}
				t.Logf("egos %6d | frame %8d B (%.2f MiB) | %s",
					n, len(data), float64(len(data))/(1<<20), status)
			}
			// The point of the sweep is that a cliff EXISTS and sits where
			// the arithmetic says. Without these the test passes whether
			// every frame is delivered, every frame fails, or the cap has
			// been removed entirely — which is what it did until 2026-07-27.
			if firstFail == 0 {
				t.Fatalf("no frame in the sweep failed to publish (largest %d egos) — the 4 MiB cap is not in force, so this test is not measuring what it claims", lastOK)
			}
			if lastOK == 0 {
				t.Fatalf("even the smallest frame in the sweep (7700 egos) failed — the cap is far below the documented ~10,200 knee")
			}
			// 4 MiB / 392 B ≈ 10,700 egos with an empty route; a 24 B route
			// pulls it to ≈ 10,000. Bound it loosely — the assertion is
			// "near ten thousand, and route length moves it down", not an
			// exact figure that would churn on any layout tweak.
			if lastOK < 8000 || lastOK > 12000 {
				t.Errorf("largest deliverable frame = %d egos, want the documented knee near 10,200 (8,000-12,000)", lastOK)
			}
			cliff[routeLen] = lastOK
			t.Logf("route %d B: largest deliverable frame in this sweep = %d egos", routeLen, lastOK)
		})
	}
	// Longer routes mean fewer egos per frame. This is the reason the knee is
	// quoted as a range rather than a constant, and the reason an OSM import
	// (long exit-lane ids) hits it sooner than a synthetic network.
	if cliff[0] != 0 && cliff[24] != 0 && cliff[24] > cliff[0] {
		t.Errorf("24 B routes fit %d egos vs %d for empty routes — a longer route cannot raise the ceiling",
			cliff[24], cliff[0])
	}
}
