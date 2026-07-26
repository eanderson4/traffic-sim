package main

// deploy_test.go — the ADR-0020 deployment flags: -wspublic verbatim vs the
// legacy listen-address advertisement, the -admintoken bearer gate on every
// mutating route class (open by default), -autostart success / unknown-id /
// retry-then-keep-serving, and -nobuild's prebuilt-binary validation + exec.
// The engine children stay `sleep` stubs (demosrv_test.go's discipline) —
// only the ws URL vars are package state, pinned and restored per test.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// pinWSVars swaps the package-level ws advertisement state for the test and
// restores it on cleanup (the other HTTP tests assert the legacy default).
func pinWSVars(t *testing.T, listen, public string) {
	t.Helper()
	oldListen, oldPublic := wsListenAddr, wsPublicURL
	wsListenAddr, wsPublicURL = listen, public
	t.Cleanup(func() { wsListenAddr, wsPublicURL = oldListen, oldPublic })
}

func TestWsPublicVerbatim(t *testing.T) {
	// Wildcard listen + a request Host the legacy path would substitute —
	// -wspublic must bypass ALL of that, verbatim.
	pinWSVars(t, "0.0.0.0:8443", "wss://traffic-ws.example.com")
	r := httptest.NewRequest("GET", "http://traffic.example.com/", nil)
	if got := advertisedWsURL(r); got != "wss://traffic-ws.example.com" {
		t.Errorf("advertisedWsURL = %q, want the -wspublic value verbatim", got)
	}
	if got := wsClientURL(); got != "wss://traffic-ws.example.com" {
		t.Errorf("wsClientURL = %q, want the -wspublic value verbatim", got)
	}

	// End to end: the registry payload and the start deep link both carry it.
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.ready = func() error { return nil }
	reg := &Registry{Demos: []*Demo{{ID: "d1", Title: "Demo One", Run: "r1", ScenarioDir: t.TempDir()}}}
	srv := &server{reg: reg, sup: sup, viz: t.TempDir(), nets: &netCache{dir: t.TempDir()}}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/demos")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if payload["ws"] != "wss://traffic-ws.example.com" {
		t.Errorf("/api/demos ws = %v, want the -wspublic value", payload["ws"])
	}

	old := runNonce
	runNonce = func() (string, error) { return "t9", nil }
	defer func() { runNonce = old }()
	resp, err = http.Post(ts.URL+"/api/demo/d1/start", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := "/app/?run=r1-t9&net=/net/d1.geojson&ws=wss%3A%2F%2Ftraffic-ws.example.com"
	if start["url"] != want {
		t.Errorf("start url = %v, want %s", start["url"], want)
	}
	sup.stop()
}

func TestWsAdvertiseLegacyDefault(t *testing.T) {
	// No -wspublic: byte-for-byte legacy behavior — wildcard listens get the
	// request host substituted WITH the listen port kept, loopback passes
	// through as ws://<listen>.
	pinWSVars(t, "0.0.0.0:8443", "")
	r := httptest.NewRequest("GET", "http://traffic.example.com/", nil)
	if got := advertisedWsURL(r); got != "ws://traffic.example.com:8443" {
		t.Errorf("wildcard advertise = %q, want host substitution with the listen port kept", got)
	}
	pinWSVars(t, "127.0.0.1:8443", "")
	if got := advertisedWsURL(r); got != "ws://127.0.0.1:8443" {
		t.Errorf("loopback advertise = %q, want the listen address verbatim", got)
	}
	if got := wsClientURL(); got != "ws://127.0.0.1:8443" {
		t.Errorf("wsClientURL = %q, want ws:// + listen address", got)
	}
}

// gateServer builds the routed server with the given admin token over the
// sleep-stub supervisor (one demo d1, no recordings).
func gateServer(t *testing.T, token string, cmds *[]*exec.Cmd, mu *sync.Mutex) *httptest.Server {
	t.Helper()
	sup := newSupervisor(sleepSpawner(cmds, mu))
	sup.ready = func() error { return nil }
	reg := &Registry{Demos: []*Demo{{ID: "d1", Title: "Demo One", Run: "r1", ScenarioDir: t.TempDir()}}}
	srv := &server{reg: reg, sup: sup, viz: t.TempDir(), nets: &netCache{dir: t.TempDir()}, adminToken: token}
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(func() { sup.stop(); ts.Close() })
	return ts
}

func postAuth(t *testing.T, url, token string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body, resp.Header
}

func TestAdminTokenGate(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	ts := gateServer(t, "sekrit", &cmds, &mu)

	// Every mutating route class: no token and wrong token are both 401,
	// with a JSON error that echoes NOTHING of the presented credential.
	mutating := []string{
		"/api/demo/d1/start",
		"/api/demo/stop",
		"/api/replay/r1/start",
		"/api/replay/ctl/pause?run=r1-replay",
		"/api/replay/ctl/resume?run=r1-replay",
		"/api/replay/ctl/speed?run=r1-replay",
		"/api/replay/ctl/seek?run=r1-replay",
	}
	for _, path := range mutating {
		for _, tok := range []string{"", "wrong-token"} {
			code, body, hdr := postAuth(t, ts.URL+path, tok)
			if code != http.StatusUnauthorized {
				t.Errorf("POST %s (token %q) = %d, want 401", path, tok, code)
			}
			if !strings.Contains(string(body), "unauthorized") {
				t.Errorf("POST %s: 401 body = %q, want a JSON error", path, body)
			}
			if strings.Contains(string(body), "wrong-token") || strings.Contains(string(body), "sekrit") {
				t.Errorf("POST %s: 401 body echoes a credential: %q", path, body)
			}
			if hdr.Get("WWW-Authenticate") != "Bearer" {
				t.Errorf("POST %s: WWW-Authenticate = %q, want Bearer", path, hdr.Get("WWW-Authenticate"))
			}
		}
	}

	// GETs stay public even with the gate on.
	for _, path := range []string{"/api/demos", "/api/status", "/api/replay/status"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			t.Errorf("GET %s = 401 — GETs must stay public", path)
		}
	}

	// The right token passes the gate: start and stop reach the handler
	// (200 over the sleep stub); the replay routes get past the gate and
	// fail in the HANDLER (unknown recording / no active replay = 404),
	// which is what distinguishes "authenticated" from "authorized".
	old := runNonce
	runNonce = func() (string, error) { return "t9", nil }
	defer func() { runNonce = old }()
	if code, _, _ := postAuth(t, ts.URL+"/api/demo/d1/start", "sekrit"); code != 200 {
		t.Errorf("authed start = %d, want 200", code)
	}
	if code, _, _ := postAuth(t, ts.URL+"/api/demo/stop", "sekrit"); code != 200 {
		t.Errorf("authed stop = %d, want 200", code)
	}
	if code, _, _ := postAuth(t, ts.URL+"/api/replay/nope/start", "sekrit"); code != http.StatusNotFound {
		t.Errorf("authed replay start (unknown recording) = %d, want 404 (gate passed, handler 404)", code)
	}
	if code, _, _ := postAuth(t, ts.URL+"/api/replay/ctl/pause?run=r1-replay", "sekrit"); code != http.StatusNotFound {
		t.Errorf("authed ctl pause (no active replay) = %d, want 404 (gate passed, handler 404)", code)
	}
	// The scheme is case-insensitive (RFC 7235): "bearer" must pass too.
	req, _ := http.NewRequest("POST", ts.URL+"/api/demo/stop", nil)
	req.Header.Set("Authorization", "bearer sekrit")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("lowercase-scheme stop: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("lowercase-scheme stop = %d, want 200", resp.StatusCode)
	}
}

func TestAdminTokenDefaultOpen(t *testing.T) {
	// No -admintoken: the mutating routes stay open (local dev unchanged).
	var mu sync.Mutex
	var cmds []*exec.Cmd
	ts := gateServer(t, "", &cmds, &mu)
	old := runNonce
	runNonce = func() (string, error) { return "t9", nil }
	defer func() { runNonce = old }()
	if code, _, _ := postAuth(t, ts.URL+"/api/demo/d1/start", ""); code != 200 {
		t.Errorf("open start = %d, want 200", code)
	}
	if code, _, _ := postAuth(t, ts.URL+"/api/demo/stop", ""); code != 200 {
		t.Errorf("open stop = %d, want 200", code)
	}
}

// autostartServer is the supervisor-only half of -autostart (no HTTP — the
// internal start must bypass the admin gate entirely).
func autostartServer(t *testing.T, spawn spawnFunc) (*server, *supervisor) {
	t.Helper()
	sup := newSupervisor(spawn)
	sup.ready = func() error { return nil }
	reg := &Registry{Demos: []*Demo{{ID: "d1", Title: "Demo One", Run: "r1", ScenarioDir: t.TempDir()}}}
	old := autostartBackoff
	autostartBackoff = time.Millisecond
	t.Cleanup(func() { autostartBackoff = old; sup.stop() })
	return &server{reg: reg, sup: sup}, sup
}

func TestAutostartSuccess(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	srv, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	srv.autostartDemo("d1", sup.stopEpoch.Load())
	id, run, _, _, ok := sup.status()
	if !ok || id != "d1" {
		t.Fatalf("status after autostart = (%q, %v), want (d1, true)", id, ok)
	}
	if !strings.HasPrefix(run, "r1-") {
		t.Errorf("run = %q, want the per-spawn unique r1-<nonce>", run)
	}
}

func TestAutostartUnknownIDKeepsServing(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	srv, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	srv.autostartDemo("nope", sup.stopEpoch.Load()) // must not panic, must not spawn
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after unknown-id autostart = active, want idle")
	}
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 0 {
		t.Errorf("%d spawns for an unknown demo id, want 0", n)
	}
}

// An operator-started run that appears during the backoff window must
// SURVIVE: the next autostart retry bails instead of letting sup.start
// replace it (single active run — Fable review).
func TestAutostartBailsWhenRunAlreadyActive(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	srv, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	if _, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "manual", Run: "m1"}}, nil); err != nil {
		t.Fatal(err)
	}
	srv.autostartDemo("d1", sup.stopEpoch.Load())
	id, run, _, _, ok := sup.status()
	if !ok || id != "manual" || !strings.HasPrefix(run, "m1-") {
		t.Fatalf("status = (%q, %q, %v), want the operator's run (manual, m1-*, true) untouched", id, run, ok)
	}
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 1 {
		t.Errorf("%d spawns, want 1 — autostart must not replace the active run", n)
	}
}

// A closed shutdown channel aborts autostart BEFORE the first attempt —
// no spawn, no backoff (an engine child started while demosrv exits would
// be orphaned on the ws port — Fable, round 2).
func TestAutostartAbortsOnShutdown(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	srv, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	srv.shutdown = make(chan struct{})
	close(srv.shutdown)
	srv.autostartDemo("d1", sup.stopEpoch.Load())
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after shutdown-aborted autostart = active, want idle")
	}
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 0 {
		t.Errorf("%d spawns after shutdown, want 0", n)
	}
}

// shutdownFinal latches the supervisor closed: EVERY later start refuses
// (errSupervisorClosed), no spawn — the atomic half of the shutdown race
// fix (the channel only narrows the window; the latch closes it).
func TestShutdownFinalRefusesStarts(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	_, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	sup.shutdownFinal()
	if _, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil); !errors.Is(err, errSupervisorClosed) {
		t.Fatalf("start after shutdownFinal = %v, want errSupervisorClosed", err)
	}
	if _, err := sup.startIfIdle(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, sup.stopEpoch.Load()); !errors.Is(err, errSupervisorClosed) {
		t.Fatalf("startIfIdle after shutdownFinal = %v, want errSupervisorClosed", err)
	}
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 0 {
		t.Errorf("%d spawns after shutdownFinal, want 0", n)
	}
}

// A run that EXITS ON ITS OWN must not count as active for startIfIdle's
// idle guard: the reaper clears s.active, not just the status mirror
// (sol, round 3) — otherwise an autostart retry would give up on a corpse.
func TestStartIfIdleAfterChildExit(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	_, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	if _, err := sup.startIfIdle(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, sup.stopEpoch.Load()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	c0 := cmds[0]
	mu.Unlock()
	_ = c0.Process.Kill()
	waitDead(t, c0, "first child")
	// The reaper clears s.active asynchronously — poll the guard.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := sup.startIfIdle(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, sup.stopEpoch.Load())
		if err == nil {
			break
		}
		if !errors.Is(err, errAlreadyActive) || time.Now().After(deadline) {
			t.Fatalf("startIfIdle after the child exited = %v, want success once reaped", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 2 {
		t.Errorf("%d spawns, want 2 (corpse must not block the retry)", n)
	}
}

// An operator's STOP landing mid-start is TERMINAL for autostart (the
// abort error is the errStartAborted sentinel, not a transient failure):
// no retry — the next attempt would un-stop the demo the operator just
// stopped (Fable, round 12).
func TestAutostartStopIsTerminal(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	srv, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	// The readiness phase reports the abort, exactly as a stop() landing
	// during waitReady would (start wraps it with %w — errors.Is holds).
	sup.ready = func() error { return errStartAborted }
	srv.autostartDemo("d1", sup.stopEpoch.Load())
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 1 {
		t.Errorf("%d attempts after an abort, want exactly 1 (no retry)", n)
	}
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after aborted autostart = active, want idle")
	}
}

// A stop landing BETWEEN attempts (during the backoff sleep) is as
// terminal as one landing mid-start: the epoch bump since autostart
// began ends the loop (Fable, round 17) — autostart never un-stops what
// an operator stopped.
func TestAutostartHonorsPriorStop(t *testing.T) {
	var sup *supervisor
	attempts := 0
	spawn := func(tg spawnTarget) (*exec.Cmd, error) {
		attempts++
		sup.stopEpoch.Add(1) // the operator's stop lands during attempt 1
		return nil, errors.New("transient")
	}
	var srv *server
	srv, sup = autostartServer(t, spawn)
	srv.autostartDemo("d1", sup.stopEpoch.Load())
	if attempts != 1 {
		t.Errorf("%d attempts, want exactly 1 — the retry after the stop's epoch bump must not fire", attempts)
	}
}

// A start that wins the mu race against a QUEUED stop refuses fast
// (stopPending) instead of spawning a child the stop would kill after a
// full readiness wait (sol, round 17).
func TestStartRefusesWhileStopPending(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	_, sup := autostartServer(t, sleepSpawner(&cmds, &mu))
	sup.stopPending.Add(1) // what stop() does first: pending mark...
	sup.stopEpoch.Add(1)   // ...THEN the epoch bump (order is load-bearing)
	if _, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil); !errors.Is(err, errStartAborted) {
		t.Fatalf("start with a stop pending = %v, want errStartAborted", err)
	}
	mu.Lock()
	n := len(cmds)
	mu.Unlock()
	if n != 0 {
		t.Errorf("%d spawns with a stop pending, want 0", n)
	}
}

// A stop() epoch bump aborts a start IN its real probe loop — the
// load-bearing path the guard-check tests only reach by injection
// (Fable, round 21): the start must fail with errStartAborted in
// milliseconds, not wait out the 30 s probe timeout, and its child must
// be reaped. The `spawned` channel (closed inside the spawner, which
// runs AFTER the epoch snapshot under mu) is the sync point: the stop's
// bump then STRICTLY follows the snapshot — no scheduling race
// (Fable, round 23).
func TestStopAbortsInFlightStart(t *testing.T) {
	spawned := make(chan struct{})
	var child *exec.Cmd
	spawn := func(tg spawnTarget) (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		child = cmd
		close(spawned)
		return cmd, nil
	}
	sup := newSupervisor(spawn)
	sup.wsAddr = "127.0.0.1:1" // nothing will ever listen: the port probe loops
	sup.readyTimeout = 30 * time.Second
	errCh := make(chan error, 1)
	go func() {
		_, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil)
		errCh <- err
	}()
	<-spawned
	sup.stop()
	select {
	case err := <-errCh:
		if !errors.Is(err, errStartAborted) {
			t.Fatalf("in-flight start error = %v, want errStartAborted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not abort the in-flight start within 5 s")
	}
	waitDead(t, child, "child of the aborted start")
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after aborted start = active, want idle")
	}
}

// A stop that marks itself DURING readiness must not see the start
// return success for a child the stop then immediately kills: the final
// guard revalidation (still under mu) refuses with errStartAborted even
// though the probes passed (sol, round 24).
func TestStopDuringReadinessFailsSuccessfulStart(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.ready = func() error {
		// A stop MARKED itself while the probes ran (pending first, the
		// epoch bump preempted — sol, round 25)...
		sup.stopPending.Add(1)
		return nil // ...and the probes PASSED anyway
	}
	_, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil)
	if !errors.Is(err, errStartAborted) {
		t.Fatalf("start with a stop landing during readiness = %v, want errStartAborted", err)
	}
	mu.Lock()
	n := len(cmds)
	c0 := cmds[0]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d spawns, want 1", n)
	}
	waitDead(t, c0, "child killed by the final guard revalidation")
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after refused start = active, want idle")
	}
}

func TestAutostartRetriesThenKeepsServing(t *testing.T) {
	// Always-failing spawner: exactly autostartAttempts attempts, then the
	// function returns (demosrv would keep serving) with no child behind.
	var mu sync.Mutex
	calls := 0
	srv, sup := autostartServer(t, func(tg spawnTarget) (*exec.Cmd, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil, errors.New("exec: no such binary")
	})
	srv.autostartDemo("d1", sup.stopEpoch.Load())
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != autostartAttempts {
		t.Errorf("%d attempts, want %d", n, autostartAttempts)
	}
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after exhausted autostart = active, want idle")
	}

	// Transient failure: two failures then a healthy spawn succeeds.
	var cmds []*exec.Cmd
	fail := 2
	srv2, sup2 := autostartServer(t, func(tg spawnTarget) (*exec.Cmd, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail > 0 {
			fail--
			return nil, errors.New("transient")
		}
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		cmds = append(cmds, cmd)
		return cmd, nil
	})
	srv2.autostartDemo("d1", sup2.stopEpoch.Load())
	if id, _, _, _, ok := sup2.status(); !ok || id != "d1" {
		t.Fatalf("status after flaky autostart = (%q, %v), want (d1, true)", id, ok)
	}
}

func TestPrebuiltBins(t *testing.T) {
	dir := t.TempDir()

	// Missing entirely: the error names the binary it wanted.
	if _, _, err := prebuiltBins(dir); err == nil || !strings.Contains(err.Error(), "serve") {
		t.Errorf("missing dir: err = %v, want a -nobuild error naming serve", err)
	}

	// Present but not executable.
	serve := filepath.Join(dir, "serve")
	if err := os.WriteFile(serve, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prebuiltBins(dir); err == nil || !strings.Contains(err.Error(), "not an executable") {
		t.Errorf("non-executable serve: err = %v, want the not-executable error", err)
	}

	// serve executable, replay still missing.
	if err := os.Chmod(serve, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prebuiltBins(dir); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Errorf("missing replay: err = %v, want a -nobuild error naming replay", err)
	}

	// Both present and executable: the dir's paths come back.
	replay := filepath.Join(dir, "replay")
	if err := os.WriteFile(replay, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	gotServe, gotReplay, err := prebuiltBins(dir)
	if err != nil {
		t.Fatalf("valid dir: %v", err)
	}
	if gotServe != serve || gotReplay != replay {
		t.Errorf("bins = (%q, %q), want (%q, %q)", gotServe, gotReplay, serve, replay)
	}
	// ABSOLUTE paths: a bare "serve" (from Join(".", "serve")) would make
	// exec.Command search $PATH instead of execing the validated file.
	if !filepath.IsAbs(gotServe) || !filepath.IsAbs(gotReplay) {
		t.Errorf("bins = (%q, %q), want absolute paths", gotServe, gotReplay)
	}

	// An executable FIFO is NOT a regular file (sol review): it would
	// pass a bare mode-bits check and block forever at first spawn.
	fifoDir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(fifoDir, "serve"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prebuiltBins(fifoDir); err == nil || !strings.Contains(err.Error(), "not an executable") {
		t.Errorf("FIFO serve: err = %v, want the not-executable error", err)
	}
}

// resolveAdminToken: the flag wins, the env backs it up (K8s Secret →
// env, keeping the token out of argv), UNSET everywhere = open, and any
// explicitly SUPPLIED token that trims to empty fails CLOSED — presence-
// aware on BOTH sources: flagSet (flag.Visit) catches `-admintoken=`,
// LookupEnv catches a set-but-empty variable (Fable + sol, rounds 7-9).
func TestResolveAdminToken(t *testing.T) {
	orig, wasSet := os.LookupEnv(adminTokenEnv)
	t.Cleanup(func() {
		if wasSet {
			os.Setenv(adminTokenEnv, orig)
		} else {
			os.Unsetenv(adminTokenEnv)
		}
	})

	os.Setenv(adminTokenEnv, "env-tok")
	if got, err := resolveAdminToken("flag-tok", true); err != nil || got != "flag-tok" {
		t.Errorf("flag set = (%q, %v), want the flag value to win", got, err)
	}
	if got, err := resolveAdminToken("", false); err != nil || got != "env-tok" {
		t.Errorf("flag unset = (%q, %v), want the env fallback", got, err)
	}
	// A trailing newline (the echo-into-Secret footgun) is trimmed.
	os.Setenv(adminTokenEnv, "env-tok\n")
	if got, err := resolveAdminToken("", false); err != nil || got != "env-tok" {
		t.Errorf("env with newline = (%q, %v), want it trimmed", got, err)
	}
	// An explicitly supplied flag that trims to EMPTY — whitespace OR the
	// `$TOK`-expands-empty manifest case (`-admintoken=`) — fails closed
	// even with a valid env behind it.
	os.Setenv(adminTokenEnv, "env-tok")
	for _, v := range []string{"  ", ""} {
		if _, err := resolveAdminToken(v, true); err == nil {
			t.Errorf("supplied-empty flag %q: want a fail-closed error", v)
		}
	}
	// Whitespace-only or set-but-EMPTY env: fail closed.
	for _, v := range []string{" \n", ""} {
		os.Setenv(adminTokenEnv, v)
		if _, err := resolveAdminToken("", false); err == nil {
			t.Errorf("env %q: want a fail-closed error", v)
		}
	}
	// Unset everywhere: open (local dev).
	os.Unsetenv(adminTokenEnv)
	if got, err := resolveAdminToken("", false); err != nil || got != "" {
		t.Errorf("unset everywhere = (%q, %v), want open", got, err)
	}
}

// validateWsPublic's accept/reject table — the most intricate validation
// in the change, extracted from main() precisely so it is testable
// (Fable, round 15).
func TestValidateWsPublic(t *testing.T) {
	valid := []string{
		"wss://traffic-ws.example.com",
		"ws://127.0.0.1:8443",
		"wss://host:443/path", // path-routed LB is legitimate
	}
	for _, v := range valid {
		if err := validateWsPublic(v); err != nil {
			t.Errorf("validateWsPublic(%q) = %v, want valid", v, err)
		}
	}
	invalid := []string{
		"https://host",              // wrong scheme
		"traffic-ws.example.com",    // no scheme at all
		"wss://",                    // no host
		"wss://:443",                // port but no hostname
		"wss://host/#",              // trailing # parses to an EMPTY fragment
		"wss://host/#x",             // fragment
		"wss://user:pass@host",      // userinfo, advertised verbatim
		"wss://host:99999",          // out-of-range port
		"wss://host:0",              // port zero
		"wss://host:007",            // leading-zero port: a typo, not a choice
		"wss://user@host:8443/ws?x", // userinfo again, with path+query
	}
	for _, v := range invalid {
		if err := validateWsPublic(v); err == nil {
			t.Errorf("validateWsPublic(%q) = nil, want invalid", v)
		}
	}
}

// stripEnv keeps the admin token out of engine children (Fable, round 7).
func TestStripEnv(t *testing.T) {
	env := []string{"PATH=/bin", adminTokenEnv + "=sekrit", "HOME=/root"}
	got := stripEnv(env, adminTokenEnv)
	if len(got) != 2 || got[0] != "PATH=/bin" || got[1] != "HOME=/root" {
		t.Errorf("stripEnv = %v, want the token entry gone", got)
	}
	for _, e := range got {
		if strings.Contains(e, "sekrit") {
			t.Errorf("stripEnv leaked the token value: %v", got)
		}
	}
	// Absent variable: the slice passes through with everything kept.
	if got := stripEnv([]string{"PATH=/bin", "HOME=/root"}, adminTokenEnv); len(got) != 2 {
		t.Errorf("stripEnv without the var = %v, want both entries", got)
	}
}

// -nobuild's binaries are what actually gets execed: shell stubs in the
// "image" directory stand in for serve/replay, spawned through the REAL
// childSpawner wiring.
func TestNobuildSpawnerExecsDirBinaries(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"serve", "replay"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	serveBin, replayBin, err := prebuiltBins(dir)
	if err != nil {
		t.Fatal(err)
	}
	lg := log.New(io.Discard, "", 0)
	spawn := childSpawner(serveBin, replayBin, lg)

	demo, err := spawn(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}})
	if err != nil {
		t.Fatalf("spawn demo from -nobuild dir: %v", err)
	}
	if demo.Path != serveBin {
		t.Errorf("demo spawned %q, want the -nobuild serve %q", demo.Path, serveBin)
	}
	_ = demo.Process.Kill()
	_, _ = demo.Process.Wait()

	rec, err := spawn(spawnTarget{Kind: "replay", Rec: &Recording{ID: "rec1", Run: "r1", Store: "/tmp/s"}})
	if err != nil {
		t.Fatalf("spawn replay from -nobuild dir: %v", err)
	}
	if rec.Path != replayBin {
		t.Errorf("replay spawned %q, want the -nobuild replay %q", rec.Path, replayBin)
	}
	_ = rec.Process.Kill()
	_, _ = rec.Process.Wait()
}
