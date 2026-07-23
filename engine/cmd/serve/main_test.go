package main

// main_test.go — the -pace/-store flag plumbing: pace→PaceFloor mapping
// (default 1 keeps the pre-flag one-tick floor exactly; 0 is unpaced batch
// mode; negative/NaN is a usage error) and store-dir selection (empty =
// temp dir cleaned up on exit; named = created if missing, kept on exit).

import (
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

// checkPaceClients: pace above maxClientPace is refused while async clients
// (driver and/or demand director) are attached; at or under the cap, and
// with no clients, anything goes.
func TestCheckPaceClients(t *testing.T) {
	for _, c := range []struct {
		pace           float64
		driver, demand bool
		wantErr        bool
	}{
		{1, true, false, false},
		{maxClientPace, true, true, false},
		{maxClientPace + 1, true, false, true},
		{maxClientPace + 1, false, true, true},
		{100, false, false, false}, // driverless batch: unbounded
	} {
		err := checkPaceClients(c.pace, c.driver, c.demand)
		if (err != nil) != c.wantErr {
			t.Errorf("pace=%g driver=%v demand=%v: err=%v, wantErr=%v",
				c.pace, c.driver, c.demand, err, c.wantErr)
		}
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
