package natsio_test

// pauseescape_test.go — the pause-gate deadlock escape hatch (ADR-0008 §6,
// WQ-13): a jammed run's active vehicle count never drops, so the resume
// condition (demand ≤ spare capacity) can be unreachable and the gate would
// wedge the run silently at zero CPU. Pinned here, with the gate engaged by
// a drive controller too small for the spawn demand and NO capacity rescue:
//   - the gate engages and the tick loop freezes (the §6 pause itself);
//   - a heartbeat logs every PauseLogEvery while gated, naming demand,
//     spare capacity, and active vehicles;
//   - after PauseEscapeAfter of persistent deficit the gate escapes: a loud
//     log line, then the run resumes on hold-last and finishes its ticks;
//   - the record plane shows pause → resume where the resume carries
//     demand > available (an escape resume — a capacity-recovery resume
//     always has demand ≤ available);
//   - the escape is dead wall-clock time: the full run replays from its
//     record bit-identically.

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
	"traffic-sim/engine/natsio/driver"
)

func TestPauseGateEscape(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 300, 23)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	h := startLive(t, spec, natsio.RecorderConfig{KeyframeEvery: 100}, natsio.ContractConfig{
		PaceFloor:        2 * time.Millisecond,
		PauseAfterTicks:  3,
		DetachAfterTicks: 1 << 40,
		PauseLogEvery:    20 * time.Millisecond,
		PauseEscapeAfter: 250 * time.Millisecond,
		Log:              log.New(&logs, "", 0),
	})
	ticks := watchTicks(t, h.nc, h.run)

	// A tiny fleet: capacity 3. Demand (unclaimed vehicles) overtakes it
	// quickly on a 3-lane spawn scenario — and nothing ever rescues it.
	a, err := driver.New(h.srv.Connect(t), h.js, driver.Config{Run: h.run, Capacity: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	waitFor(t, "driver A at capacity", 20*time.Second, func() bool { return a.FleetSize() == 3 })

	// The gate engages: snapshot ticks freeze while the run is unfinished.
	var frozen uint64
	waitFor(t, "tick loop pause", 30*time.Second, func() bool {
		frozen = ticks.last.Load()
		if frozen == 0 || frozen >= spec.Ticks {
			return false
		}
		time.Sleep(100 * time.Millisecond)
		return ticks.last.Load() == frozen
	})

	// No capacity rescue: the escape hatch must un-wedge the run on its own.
	out := h.finish(t)
	if out.err != nil {
		t.Fatalf("run: %v", out.err)
	}
	if out.lr.Engine.Tick != spec.Ticks {
		t.Fatalf("engine tick %d, want %d — the gate escaped but the run did not finish",
			out.lr.Engine.Tick, spec.Ticks)
	}

	// Loud logging: at least one heartbeat (demand/capacity/active named)
	// BEFORE the escape line, then the escape naming the persistent deficit.
	// (Reads happen after the run loop returned — no concurrent writer.)
	lines := strings.Split(strings.TrimRight(logs.String(), "\n"), "\n")
	beat, escape := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "waiting for drive capacity") {
			if beat < 0 {
				beat = i
			}
			for _, want := range []string{"demand=", "spare capacity=", "active vehicles="} {
				if !strings.Contains(l, want) {
					t.Errorf("heartbeat line missing %q: %s", want, l)
				}
			}
		}
		if strings.Contains(l, "resuming anyway") {
			escape = i
		}
	}
	if beat < 0 {
		t.Fatalf("no pause-gate heartbeat logged; got:\n%s", logs.String())
	}
	if escape < 0 {
		t.Fatalf("no escape logged; got:\n%s", logs.String())
	}
	if beat > escape {
		t.Fatalf("heartbeat (line %d) must precede the escape (line %d):\n%s", beat, escape, logs.String())
	}

	// Record plane: pause → escape resume (resume with demand > available).
	rec, err := natsio.MaterializeRunRecord(h.js, h.meta(t))
	if err != nil {
		t.Fatal(err)
	}
	var sawPause, sawEscape bool
	for _, evt := range rec.Events {
		if evt.Type == natsio.EventPause {
			sawPause = true
		}
		if evt.Type == natsio.EventResume && sawPause {
			if evt.Demand > evt.Available {
				sawEscape = true
			}
		}
	}
	if !sawPause || !sawEscape {
		t.Fatalf("record events: pause %v escape-resume %v", sawPause, sawEscape)
	}

	// The escape is dead wall-clock time: full replay matches the live CRC.
	rep, err := natsio.ReplayFromStream(h.js, h.meta(t), spec.Ticks)
	if err != nil {
		t.Fatalf("ReplayFromStream(%d): %v", spec.Ticks, err)
	}
	if rep.FinalCRC != out.lr.Engine.CRC() {
		t.Fatalf("replay final %016x, live %016x", rep.FinalCRC, out.lr.Engine.CRC())
	}
}
