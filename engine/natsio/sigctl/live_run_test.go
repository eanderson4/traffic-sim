package sigctl

import (
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// live_run_test.go — the M2 acceptance test: the reference actuated
// controller attaches over the live contract plane, its signal_set verbs
// are accepted, and at least one hold DIVERGES from the fixed-time
// schedule (the control is load-bearing, not a restatement of the
// program). The network is the signal-4way fixture crop; control is
// restricted to its central junction's program.

func TestControllerLive(t *testing.T) {
	srv := natsio.NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec := engine.RunSpec{
		Net: engine.NetSpec{Kind: "file", Path: "../../testdata/signal-4way/network.json"},
		Scen: engine.Scenario{
			SpawnRatePerLaneHour: 600,
			SpawnRates:           map[string]float64{"n167922072_1_0": 3000},
		},
		Params: engine.DefaultParams(),
		Seed:   7,
		Ticks:  900,
	}
	const prog = "42430333"
	run := "sc1"

	geom, err := LoadGeom("../../testdata/signal-4way/network.json")
	if err != nil {
		t.Fatal(err)
	}
	if geom.ByProgram[prog] == nil {
		t.Fatalf("fixture drift: no detector geometry for program %s", prog)
	}

	// Attach concurrently with the run: the hello/meta retry loop spans
	// the contract plane coming up (the verb tests' pattern).
	attached := make(chan *Controller, 1)
	go func() {
		cc := srv.Connect(t)
		cfg := Config{Run: run, Programs: []string{prog},
			Log: log.New(io.Discard, "", 0)}
		c, err := Attach(cc, js, cfg, geom)
		if err != nil {
			t.Errorf("controller attach: %v", err)
			attached <- nil
			return
		}
		attached <- c
	}()

	lr, err := natsio.RunLive(nc, js, run, spec, natsio.RecorderConfig{KeyframeEvery: 100, CRCEvery: 1},
		natsio.ContractConfig{PaceFloor: time.Millisecond})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	e := lr.Engine
	ctl := <-attached
	if ctl == nil {
		t.Fatal("controller never attached")
	}

	// Verbs flowed and were accepted.
	ctl.mu.Lock()
	sent, accepted, rejected := ctl.Sent, ctl.Accepted, ctl.Rejected
	ctl.mu.Unlock()
	if sent == 0 || accepted == 0 {
		t.Fatalf("controller sent %d verbs, %d accepted — the controller never commanded", sent, accepted)
	}
	if rejected != 0 {
		t.Errorf("%d verbs rejected", rejected)
	}
	if len(e.SigLog) == 0 {
		t.Fatal("engine SigLog is empty — no signal_set verb was applied")
	}
	for _, d := range e.SigLog {
		if !strings.HasPrefix(d.RequestID, "sigctl-") || !strings.Contains(d.RequestID, "-"+prog+"-") {
			t.Errorf("directive %+v: request id not the controller's deterministic form", d)
		}
		if d.Signal != prog {
			t.Errorf("directive %+v: wrong program", d)
		}
	}
	t.Logf("controller: %d verbs sent, %d accepted; engine applied %d signal directives", sent, accepted, len(e.SigLog))

	// Load-bearing: at least one held tick where the commanded phase is
	// NOT what the fixed-time schedule shows there.
	var fp *engine.SignalProgram
	for _, p := range e.Net.Signals {
		if p.ID == prog {
			fp = p
		}
	}
	if fp == nil {
		t.Fatalf("program %s missing from the built network", prog)
	}
	diverged := false
	for _, d := range e.SigLog {
		for tick := d.Tick; tick < d.Tick+d.HoldTicks && tick <= spec.Ticks; tick++ {
			if fp.PhaseAt(tick) != d.Phase {
				diverged = true
				break
			}
		}
	}
	if !diverged {
		t.Error("no held tick diverges from the fixed-time schedule — the control changed nothing")
	}
}
