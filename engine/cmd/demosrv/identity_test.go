package main

// identity_test.go — the demo-spawn identity phase (supervisor.waitIdentity)
// against REAL embedded NATS servers with WebSocket listeners (serve's own
// broker shape): the happy path (registry holds {run}/meta), the foreign
// broker (a ws listener that answers the port probe but has no such run),
// and the child that dies mid-probe (the foreign-port incident's failure
// mode — bind failure must surface in seconds, with the child's stderr
// tail, not after the full identity timeout).

import (
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"traffic-sim/engine/natsio"
)

// wsNatsServer starts an embedded NATS server with a WebSocket listener on
// a free loopback port (DontListen for the client port, like serve) and
// returns its host:port. JetStream is on; whether the ts_runs bucket or the
// {run}/meta key exist is the TEST's choice — that difference is exactly
// the identity question.
func wsNatsServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	ns, err := natsserver.NewServer(&natsserver.Options{
		DontListen: true,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		Websocket:  natsserver.WebsocketOpts{Host: "127.0.0.1", Port: port, NoTLS: true},
	})
	if err != nil {
		t.Fatalf("nats-server NewServer: %v", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats-server not ready")
	}
	t.Cleanup(ns.Shutdown)
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// putRunMeta creates the ts_runs bucket and writes {run}/meta — the
// registry state reg.Start leaves behind inside RunLive.
func putRunMeta(t *testing.T, addr, run string) {
	t.Helper()
	nc, err := nats.Connect("ws://" + addr)
	if err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("JetStream: %v", err)
	}
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: natsio.RegistryBucket})
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	if _, err := kv.Put(run+"/meta", []byte(`{"run_id":"`+run+`","status":"running","created_unix":`+fmt.Sprint(time.Now().Unix())+`}`)); err != nil {
		t.Fatalf("put meta: %v", err)
	}
}

// Happy path: the broker on the ws port carries OUR run's registry entry —
// port probe AND identity probe pass, start succeeds.
func TestSupervisorDemoIdentityHappyPath(t *testing.T) {
	addr := wsNatsServer(t)
	old := runNonce
	runNonce = func() (string, error) { return "t1", nil }
	defer func() { runNonce = old }()
	putRunMeta(t, addr, "r1-t1") // start() suffixes live-demo run ids

	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.wsAddr = addr

	if _, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil); err != nil {
		t.Fatalf("start with our run registered: %v", err)
	}
	if id, _, _, _, ok := sup.status(); !ok || id != "d1" {
		t.Fatalf("status after start = (%q, %v), want (d1, true)", id, ok)
	}
	sup.stop()
	mu.Lock()
	c0 := cmds[0]
	mu.Unlock()
	waitDead(t, c0, "demo child after stop")
}

// Foreign broker: a ws listener answers the port probe but its registry has
// no {run}/meta (another session's engine — the incident shape). The child
// stays alive, so the failure comes from the identity phase timeout, and
// the error must name the run id so the user knows WHICH run never showed.
func TestSupervisorDemoIdentityForeignBroker(t *testing.T) {
	addr := wsNatsServer(t) // JetStream up, but no ts_runs bucket at all

	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.wsAddr = addr
	sup.identityTimeout = 1500 * time.Millisecond

	old := runNonce
	runNonce = func() (string, error) { return "t2", nil }
	defer func() { runNonce = old }()
	start := time.Now()
	_, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil)
	if err == nil {
		t.Fatal("start succeeded against a broker with no such run")
	}
	if !strings.Contains(err.Error(), `"r1-t2"`) || !strings.Contains(err.Error(), natsio.RegistryBucket) {
		t.Errorf("error %q, want it to name the run id and the registry bucket", err)
	}
	if elapsed := time.Since(start); elapsed < 1500*time.Millisecond || elapsed > 15*time.Second {
		t.Errorf("start took %s, want ~identityTimeout", elapsed)
	}
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after failed start = active, want idle")
	}
	mu.Lock()
	n := len(cmds)
	c0 := cmds[0]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d spawns, want 1", n)
	}
	waitDead(t, c0, "child after failed identity probe")
}

// Child dies during the identity probe — the foreign-port case in reverse:
// the port is held by a foreign listener, OUR child fails its bind and
// exits, and start must fail in seconds (not after the 120 s identity
// timeout) with the child's captured stderr tail in the error.
func TestSupervisorDemoIdentityChildExits(t *testing.T) {
	addr := wsNatsServer(t) // port answers; no such run

	// Spawn stub shaped like childSpawner: a process that writes its
	// failure to stderr and exits immediately, output teed through a
	// prefixWriter so the supervisor can quote the tail.
	spawner := func(tg spawnTarget) (*exec.Cmd, error) {
		cmd := exec.Command("sh", "-c", "echo 'driver: hello: nats: no responders' >&2; exit 1")
		w := &prefixWriter{lg: log.New(io.Discard, "", 0), prefix: "[" + tg.id() + "] "}
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
	sup := newSupervisor(spawner)
	sup.wsAddr = addr
	sup.identityTimeout = 60 * time.Second // would hang the test if done didn't abort

	start := time.Now()
	_, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil)
	if err == nil {
		t.Fatal("start succeeded with a dead child")
	}
	if !strings.Contains(err.Error(), "engine exited during startup") {
		t.Errorf("error %q, want the child-exit report", err)
	}
	if !strings.Contains(err.Error(), "driver: hello: nats: no responders") {
		t.Errorf("error %q, want the child's stderr tail", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("start blocked %s with a dead child, want seconds", elapsed)
	}
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after failed start = active, want idle")
	}
}

// prefixWriter's ring keeps only the last tailLines lines and joins them
// oldest-first — the tail must carry the FAILURE line, not the child's
// whole log.
func TestPrefixWriterTailRing(t *testing.T) {
	w := &prefixWriter{lg: log.New(io.Discard, "", 0), prefix: "[x] "}
	var sb strings.Builder
	for i := 0; i < tailLines+5; i++ {
		fmt.Fprintf(&sb, "line-%d\n", i)
	}
	if _, err := w.Write([]byte(sb.String())); err != nil {
		t.Fatal(err)
	}
	tail := w.tail()
	lines := strings.Split(tail, "\n")
	if len(lines) != tailLines {
		t.Fatalf("tail has %d lines, want %d", len(lines), tailLines)
	}
	if lines[0] != "line-5" || lines[len(lines)-1] != fmt.Sprintf("line-%d", tailLines+4) {
		t.Errorf("tail spans %q..%q, want line-5..line-%d", lines[0], lines[len(lines)-1], tailLines+4)
	}
	// A partial trailing line never reaches the ring.
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.tail(), "partial") {
		t.Error("tail includes the unterminated partial line")
	}
}
