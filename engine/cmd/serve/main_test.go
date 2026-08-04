package main

// main_test.go — the -pace/-store flag plumbing: pace→PaceFloor mapping
// (default 1 keeps the pre-flag one-tick floor exactly; 0 is unpaced batch
// mode; negative/NaN is a usage error) and store-dir selection (empty =
// temp dir cleaned up on exit; named = created if missing, kept on exit).
// Plus the client-attach barrier: every expected client must report before
// the start gate opens, and failures name the client (the old maxClientPace
// cap and -pace 0 refusal went away with the barrier — any pace is allowed
// once clients are attached before tick 0).

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"traffic-sim/engine/natsio"
)

func TestPaceFloor(t *testing.T) {
	const dt = 0.1
	tick := time.Duration(dt * float64(time.Second))

	// Default 1: exactly the pre-flag floor (one tick of wall time).
	if d, err := paceFloor(dt, 1); err != nil || d != tick {
		t.Errorf("pace 1: got %v, %v; want %v, nil", d, err, tick)
	}
	// Faster than realtime: the floor divides by the multiplier.
	if d, err := paceFloor(dt, 4); err != nil || d != tick/4 {
		t.Errorf("pace 4: got %v, %v; want %v, nil", d, err, tick/4)
	}
	// Slower than realtime.
	if d, err := paceFloor(dt, 0.5); err != nil || d != 2*tick {
		t.Errorf("pace 0.5: got %v, %v; want %v, nil", d, err, 2*tick)
	}
	// Unpaced batch mode: zero floor, no error.
	if d, err := paceFloor(dt, 0); err != nil || d != 0 {
		t.Errorf("pace 0: got %v, %v; want 0, nil", d, err)
	}
	// Negative and NaN fail loud.
	if _, err := paceFloor(dt, -1); err == nil {
		t.Error("pace -1: want error, got nil")
	}
	if _, err := paceFloor(dt, math.NaN()); err == nil {
		t.Error("pace NaN: want error, got nil")
	}
	// Inf and duration-overflowing pace fail loud (not silently unpaced).
	if _, err := paceFloor(dt, math.Inf(1)); err == nil {
		t.Error("pace +Inf: want error, got nil")
	}
	if _, err := paceFloor(dt, 1e-300); err == nil {
		t.Error("pace 1e-300 (floor overflows time.Duration): want error, got nil")
	}
	// A huge finite pace truncating the floor below 1 ns fails loud too
	// (a zero floor would read as unpaced batch mode, which it isn't).
	if _, err := paceFloor(dt, 1e12); err == nil {
		t.Error("pace 1e12 (floor truncates to 0): want error, got nil")
	}
}

// waitBarrier: the happy path collects one report per expected client; a
// client failure is named verbatim (the attach-failure path serve exits
// with); a timeout names the clients still pending; a dead run names the
// pending clients too. No deadlock: every path returns.
func TestWaitBarrier(t *testing.T) {
	// Happy path: both clients report, in either order.
	barrier := make(chan attachOutcome, 2)
	runErr := make(chan error, 1)
	barrier <- attachOutcome{client: "demand director", desc: "1 demand file(s)"}
	barrier <- attachOutcome{client: "default driver", desc: "id ctl-1"}
	got, err := waitBarrier(barrier, []string{"default driver", "demand director"}, runErr, 5*time.Second)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("happy path: %d reports, want 2", len(got))
	}

	// Client failure (e.g. an impossible run id): the error names the
	// failing client.
	barrier = make(chan attachOutcome, 1)
	barrier <- attachOutcome{client: "default driver",
		err: errors.New(`driver: run "nope" not found in the registry within 30s`)}
	_, err = waitBarrier(barrier, []string{"default driver"}, runErr, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "default driver failed to attach") ||
		!strings.Contains(err.Error(), `run "nope" not found`) {
		t.Fatalf("failure path: %v — want the failing client named", err)
	}

	// Timeout: the clients still pending are named.
	_, err = waitBarrier(make(chan attachOutcome), []string{"default driver", "demand director"},
		runErr, 20*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "attach timeout") ||
		!strings.Contains(err.Error(), "default driver") || !strings.Contains(err.Error(), "demand director") {
		t.Fatalf("timeout path: %v — want the pending clients named", err)
	}

	// The run died (or finished) before the barrier completed.
	runErr2 := make(chan error, 1)
	runErr2 <- errors.New("registry start: bad run id")
	_, err = waitBarrier(make(chan attachOutcome), []string{"default driver"}, runErr2, 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "run aborted") || !strings.Contains(err.Error(), "default driver") {
		t.Fatalf("run-death path: %v", err)
	}

	// No clients expected: returns immediately, nil error.
	if got, err := waitBarrier(make(chan attachOutcome), nil, runErr, time.Millisecond); err != nil || len(got) != 0 {
		t.Fatalf("no clients: %v %v", got, err)
	}
}

// checkFreshRecording: a non-empty stream for the run in a durable store
// must fail (append would interleave two runs); not-found and empty pass.
func TestCheckFreshRecording(t *testing.T) {
	dir := t.TempDir()
	ns, err := server.NewServer(&server.Options{
		DontListen: true,
		JetStream:  true,
		StoreDir:   dir,
	})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ns.Start()
	defer ns.Shutdown()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("server not ready")
	}
	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}

	if err := checkFreshRecording(js, "fresh"); err != nil {
		t.Errorf("unknown run should pass: %v", err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     natsio.StreamName("taken"),
		Subjects: []string{"ts.taken.log.>"},
	}); err != nil {
		t.Fatalf("AddStream: %v", err)
	}
	if err := checkFreshRecording(js, "taken"); err != nil {
		t.Errorf("empty stream should pass: %v", err)
	}
	if _, err := js.Publish("ts.taken.log.crc", []byte("x")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := checkFreshRecording(js, "taken"); err == nil {
		t.Error("non-empty stream must fail (append would interleave two runs)")
	}
}

func TestJetStreamStoreDirEmpty(t *testing.T) {
	dir, cleanup, err := jetStreamStoreDir("")
	if err != nil {
		t.Fatalf("jetStreamStoreDir: %v", err)
	}
	if !strings.HasPrefix(filepath.Base(dir), "ts-serve-js") {
		t.Errorf("temp dir %q does not use the ts-serve-js prefix", dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir not created: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cleanup should remove the temp dir, stat: %v", err)
	}
}

func TestJetStreamStoreDirNamed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "js-store") // missing: must be created
	got, cleanup, err := jetStreamStoreDir(dir)
	if err != nil {
		t.Fatalf("jetStreamStoreDir: %v", err)
	}
	if got != dir {
		t.Errorf("path: got %q, want %q", got, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("named dir not created: %v", err)
	}
	cleanup()
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("cleanup must keep the named dir (durable for replay): %v", err)
	}
	// An existing dir is reused as-is.
	if _, _, err := jetStreamStoreDir(dir); err != nil {
		t.Errorf("existing dir should be accepted: %v", err)
	}
}

// The -state-in/-state-out/-state-at surface (ADR-0029 phase 1). Every rule
// here is a usage error rather than a runtime one: all of them are knowable
// before the run starts, and the alternative is finding out an hour in that
// the state file was never going to be written.
func TestValidateStateFlags(t *testing.T) {
	ok := stateFlags{ticks: 1000}
	cases := []struct {
		name string
		f    stateFlags
		want string // substring of the expected error; "" = must pass
	}{
		{"neither", ok, ""},
		{"dump pair", stateFlags{out: "s.bin", at: 500, atSet: true, ticks: 1000}, ""},
		{"dump at tick 0", stateFlags{out: "s.bin", at: 0, atSet: true, ticks: 1000}, ""},
		{"warm start", stateFlags{in: "s.bin", inTick: 500, ticks: 1000}, ""},
		{"warm start and dump", stateFlags{in: "s.bin", inTick: 500, out: "t.bin", at: 800, atSet: true, ticks: 1000}, ""},

		{"out without at", stateFlags{out: "s.bin", ticks: 1000}, "-state-at"},
		{"at without out", stateFlags{at: 500, atSet: true, ticks: 1000}, "-state-out"},
		{"in with store", stateFlags{in: "s.bin", inTick: 500, store: "/tmp/js", ticks: 1000}, "-store"},
		{"state past horizon", stateFlags{in: "s.bin", inTick: 1000, ticks: 1000}, "nothing left"},
		{"dump past horizon", stateFlags{out: "s.bin", at: 1001, atSet: true, ticks: 1000}, "past -ticks"},
		{"dump before warm start", stateFlags{in: "s.bin", inTick: 500, out: "t.bin", at: 500, atSet: true, ticks: 1000}, "just rewrites the state that was loaded"},

		// A seed mismatch splices two deterministic programs, and -seed
		// defaults to 1, so the way to hit it is to forget the flag.
		{"seed mismatch", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, inSeed: 1000, seed: 1}, "-state-reseed"},
		{"seed mismatch names the saved seed", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, inSeed: 1000, seed: 1}, "-seed 1000"},
		{"seed match", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, inSeed: 1000, seed: 1000}, ""},
		{"seed mismatch opted into", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, inSeed: 1000, seed: 1, reseed: true}, ""},
		// The seed only binds a warm start: -state-reseed without
		// -state-in has nothing to say, and a cold run is not affected.
		{"cold run ignores seeds", stateFlags{ticks: 1000, inSeed: 1000, seed: 1}, ""},

		// The contract plane is not restored, so a warm start with a
		// controller attached diverges on its first resumed tick. -driver
		// DEFAULTS to true, so this is the combination a user gets by
		// simply not passing the flag — the same default-trap shape as the
		// seed mismatch above, and the reason it is refused rather than
		// warned.
		{"warm start with the driver", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, driver: true}, "-driver=false"},
		{"warm start names the resumed tick", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, driver: true}, "(500)"},
		{"warm start without the driver", stateFlags{in: "s.bin", inTick: 500, ticks: 1000, driver: false}, ""},
		// A cold run with the driver is the ordinary case and must stay
		// untouched — the refusal binds the warm start, not the driver.
		{"cold run with the driver", stateFlags{ticks: 1000, driver: true}, ""},
	}
	for _, c := range cases {
		err := validateStateFlags(c.f)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: rejected a valid combination: %v", c.name, err)
		case c.want != "" && err == nil:
			t.Errorf("%s: accepted, want an error naming %q", c.name, c.want)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s: error %q does not name %q", c.name, err, c.want)
		}
	}
}

// probeWritable must catch an unwritable destination and leave nothing
// behind when it does not.
func TestProbeWritable(t *testing.T) {
	dir := t.TempDir()
	if err := probeWritable(filepath.Join(dir, "state.bin")); err != nil {
		t.Fatalf("writable dir rejected: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probe left %d files behind", len(entries))
	}
	if err := probeWritable(filepath.Join(dir, "missing", "state.bin")); err == nil {
		t.Fatal("nonexistent directory accepted")
	}
}
