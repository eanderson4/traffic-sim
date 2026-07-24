package natsio_test

// startgate_test.go — the client-attach barrier (ContractConfig.StartGate,
// backing serve's -attach-timeout): the run loop parks at tick 0 — contract
// plane served, sim time frozen — until the gate opens, so embedded clients
// (default driver, demand director) are attached and subscribed before the
// first Step and no early tick can outrun an attaching client at any pace.
//
// Pinned here:
//   - parked: clients complete their hello handshakes and attach while the
//     gate holds, and not one snapshot is published before it opens;
//   - the verb program is recorded from tick 0 at ANY pace (paced and
//     unpaced runs), and the recorded spawn DIRECTIVES — request id,
//     origin, vtype, earliest tick — are identical across paces, with
//     steady-state acceptance always ahead of the earliest tick (the
//     30-tick director lead absorbs pace differences);
//   - each fixed-pace run replays from its record bit-identically;
//   - the attach-failure signal names the run that never appeared (the
//     error serve's barrier wraps with the failing client's name).

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
	"traffic-sim/engine/natsio/demand"
	"traffic-sim/engine/natsio/driver"
	"traffic-sim/engine/scenario"
)

// gatedRun executes one demand-driven run behind the start gate at the
// given pace floor and returns its materialized record. The demand program:
// constant spacing at 3600 veh/h = one arrival per second = every 10 ticks,
// first at t=0, until 15 s → earliest ticks 0,10,...,140 (15 verbs, request
// ids f0-000000..f0-000014). The driver really driving and the record
// replaying bit-identically are verified here, per run.
func gatedRun(t *testing.T, pace time.Duration) *natsio.RunRecord {
	t.Helper()
	spec, err := engine.DefaultSpec("lanedrop", 200, 11)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 0 // director-driven: no built-in spawner
	dem := &scenario.DemandFile{FormatVersion: 1, Flows: []scenario.Flow{{
		Origin: "A0", VehPerH: 3600, Spacing: "constant", UntilS: 15,
	}}}

	srv := natsio.NewTestServer(t)
	nc, js := srv.JetStream(t)
	run := fmt.Sprintf("gate%d", liveCounter.Add(1))
	gate := make(chan struct{})
	done := make(chan liveOutcome, 1)
	go func() {
		lr, err := natsio.RunLive(nc, js, run, spec, natsio.RecorderConfig{KeyframeEvery: 100},
			natsio.ContractConfig{PaceFloor: pace, StartGate: gate, PauseAfterTicks: 1 << 40})
		done <- liveOutcome{lr, err}
	}()
	waitFor(t, "run start", 10*time.Second, func() bool {
		kv, err := js.KeyValue(natsio.RegistryBucket)
		if err != nil {
			return false
		}
		_, err = kv.Get(run + "/meta")
		return err == nil
	})

	// The barrier's whole point: no snapshot (hence no tick) before the
	// gate opens — watch from before the clients attach.
	ticks := watchTicks(t, nc, run)

	// Both clients attach while the loop is parked: their handshakes are
	// served by the gate's contract rounds.
	d, err := driver.New(srv.Connect(t), js, driver.Config{Run: run, Capacity: 1000})
	if err != nil {
		t.Fatalf("driver attach during gate: %v", err)
	}
	defer d.Close()
	dd, err := demand.Attach(srv.Connect(t), js, demand.Config{Run: run, Seed: spec.Seed},
		[]*scenario.DemandFile{dem})
	if err != nil {
		t.Fatalf("director attach during gate: %v", err)
	}
	defer dd.Close()
	if err := dd.Ready(); err != nil {
		t.Fatalf("director ready: %v", err)
	}

	// Attached, subscriptions live — and still parked.
	time.Sleep(100 * time.Millisecond)
	if got := ticks.last.Load(); got != 0 {
		t.Fatalf("tick loop ran before the start gate opened (snapshot tick %d)", got)
	}

	close(gate)
	var out liveOutcome
	select {
	case out = <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("gated run did not finish in time")
	}
	if out.err != nil {
		t.Fatalf("run: %v", out.err)
	}

	// The driver really drove: fresh (non-held) intents dominate the
	// arbitrated log.
	fresh := 0
	for _, ti := range out.lr.Engine.IntentLog {
		if !ti.Held {
			fresh++
		}
	}
	if fresh < 50 {
		t.Fatalf("pace %s: only %d fresh intents — the driver did not drive the run", pace, fresh)
	}

	kv, err := js.KeyValue(natsio.RegistryBucket)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := kv.Get(run + "/meta")
	if err != nil {
		t.Fatal(err)
	}
	var meta natsio.RunMeta
	if err := json.Unmarshal(entry.Value(), &meta); err != nil {
		t.Fatal(err)
	}

	// This fixed-pace run replays from its record bit-identically.
	rep, err := natsio.ReplayFromStream(js, &meta, spec.Ticks)
	if err != nil {
		t.Fatalf("ReplayFromStream(%d): %v", spec.Ticks, err)
	}
	if rep.FinalCRC != out.lr.Engine.CRC() {
		t.Fatalf("pace %s: replay final %016x, live %016x", pace, rep.FinalCRC, out.lr.Engine.CRC())
	}

	rec, err := natsio.MaterializeRunRecord(js, &meta)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestStartGateBarrier(t *testing.T) {
	// One paced run, one unpaced (batch) run — the combinations the old
	// maxClientPace cap and -pace 0 refusal existed to prevent.
	recPaced := gatedRun(t, 2*time.Millisecond)
	recBatch := gatedRun(t, 0)

	for name, rec := range map[string]*natsio.RunRecord{"paced": recPaced, "batch": recBatch} {
		spawns := rec.Log.Spawns
		if len(spawns) != 15 {
			t.Fatalf("%s: %d recorded spawns, want 15", name, len(spawns))
		}
		for i, s := range spawns {
			// The program is recorded from tick 0: the first arrival's
			// earliest tick is 0 — no early ticks lost to attach latency.
			if want := uint64(i * 10); s.EarliestTick != want {
				t.Errorf("%s: spawn %d earliest tick %d, want %d", name, i, s.EarliestTick, want)
			}
			if want := fmt.Sprintf("f0-%06d", i); s.RequestID != want {
				t.Errorf("%s: spawn %d request id %q, want %q", name, i, s.RequestID, want)
			}
			if s.Origin != "A0" || s.TypeName != "car" {
				t.Errorf("%s: spawn %d %s/%s, want A0/car", name, i, s.Origin, s.TypeName)
			}
		}
	}

	// Cross-pace identity: the verb DIRECTIVES (request id, origin, vtype,
	// earliest tick) are a pure function of (demand, seed) — identical at
	// any pace. And for the steady-state program (earliest ≥ the 30-tick
	// lead) acceptance never outruns the earliest tick, so those vehicles
	// inject at the identical tick at every pace. The acceptance tick
	// itself (the directive's wall-clock landing, ~1 tick later unpaced)
	// and the head of the program (earliest < lead: those verbs wait for
	// the first snapshot, so their effect tick is the landing tick) are
	// NOT pace-invariant — and neither is full CRC identity, since the
	// tick individual claims/intents land on is wall-clock too. (See the
	// determinism note in cmd/serve.)
	for i := range recPaced.Log.Spawns {
		a, b := recPaced.Log.Spawns[i], recBatch.Log.Spawns[i]
		if a.SpawnDirective != b.SpawnDirective {
			t.Errorf("spawn %d directive differs across paces: paced %+v, batch %+v",
				i, a.SpawnDirective, b.SpawnDirective)
		}
		for _, s := range []engine.TickedSpawn{a, b} {
			if s.EarliestTick >= 30 && s.Tick > s.EarliestTick {
				t.Errorf("spawn %d accepted at tick %d, past its earliest tick %d — "+
					"the injection tick is no longer pace-invariant", i, s.Tick, s.EarliestTick)
			}
		}
	}
}

// The attach-failure signal behind serve's barrier error: an impossible
// run id fails driver's New after MetaWait, naming the run.
func TestStartGateAttachFailure(t *testing.T) {
	srv := natsio.NewTestServer(t)
	_, js := srv.JetStream(t)
	_, err := driver.New(srv.Connect(t), js, driver.Config{Run: "no-such-run", MetaWait: 200 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), `"no-such-run"`) {
		t.Fatalf("driver.New with impossible run id: %v — want the run named", err)
	}
}
