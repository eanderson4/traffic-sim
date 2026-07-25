package main

// demosrv_test.go — registry validation (fail-loud on the config errors
// the menu cannot recover from) and the single-active-run lifecycle against
// an injected `sleep` spawn stub: NO real engine, no ports, no NATS. The
// only real piece under test in the lifecycle is the kill/reap machinery,
// which is exactly the part that must not leak processes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
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

// writeRegistry creates one real scenario directory and a registry file
// referencing it, returning the registry path. demosJSON is the raw value
// of the "demos" array; %s inside it expands to the scenario dir.
func writeRegistry(t *testing.T, demosJSON string) string {
	t.Helper()
	dir := t.TempDir()
	scen := filepath.Join(dir, "scen")
	if err := os.Mkdir(scen, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"demos": %s}`, fmt.Sprintf(demosJSON, scen))
	path := filepath.Join(dir, "demos.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRegistryValid(t *testing.T) {
	path := writeRegistry(t, `[{
		"id": "i280-baseline", "title": "I-280 baseline", "blurb": "b",
		"tags": ["freeway", "baseline"], "scenarioDir": %q,
		"run": "baseline", "seed": 42, "ticks": 36000
	}]`)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(reg.Demos) != 1 {
		t.Fatalf("got %d demos, want 1", len(reg.Demos))
	}
	d := reg.Demos[0]
	if d.ID != "i280-baseline" || d.Run != "baseline" || d.Title == "" {
		t.Errorf("demo decoded wrong: %+v", d)
	}
	if d.Seed == nil || *d.Seed != 42 || d.Ticks == nil || *d.Ticks != 36000 {
		t.Errorf("seed/ticks overrides lost: %+v", d)
	}
	if !filepath.IsAbs(d.ScenarioDir) {
		t.Errorf("scenarioDir not made absolute: %q", d.ScenarioDir)
	}
	if reg.byID("i280-baseline") != d || reg.byID("nope") != nil {
		t.Errorf("byID lookup broken")
	}
}

func TestLoadRegistryValidation(t *testing.T) {
	cases := []struct {
		name  string
		demos string
		want  string // substring of the error
	}{
		{"duplicate id", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"r1"},
			{"id":"a","title":"t","scenarioDir":%q,"run":"r2"}
		]`, "duplicate id"},
		{"duplicate run", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"r1"},
			{"id":"b","title":"t","scenarioDir":%q,"run":"r1"}
		]`, "demo run ids must be unique"},
		{"missing scenario dir", `[
			{"id":"a","title":"t","scenarioDir":"/no/such/dir-anywhere","run":"r1"}
		]`, "scenarioDir"},
		{"scenario dir is a file", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"r1"}
		]`, "not a directory"},
		{"run with dot", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"bad.token"}
		]`, "must match [A-Za-z0-9_-]+"},
		{"run with space", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"has space"}
		]`, "must match [A-Za-z0-9_-]+"},
		{"run with wildcard", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"wild>*"}
		]`, "must match [A-Za-z0-9_-]+"},
		{"run with replay suffix", `[
			{"id":"a","title":"t","scenarioDir":%q,"run":"foo-replay"}
		]`, "reserved replay-plane suffix"},
		{"bad id", `[
			{"id":"a/b","title":"t","scenarioDir":%q,"run":"r1"}
		]`, "id"},
		{"missing title", `[
			{"id":"a","scenarioDir":%q,"run":"r1"}
		]`, "title"},
		{"empty registry", `[]`, "at least one demo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// %q slots: one per scenarioDir reference. For "scenario dir is
			// a file" the slot is replaced with a regular file instead.
			demos := tc.demos
			n := strings.Count(demos, "%q")
			args := make([]any, 0, n)
			for i := 0; i < n; i++ {
				args = append(args, "SCEN")
			}
			demos = fmt.Sprintf(demos, args...)
			dir := t.TempDir()
			scen := filepath.Join(dir, "scen")
			if err := os.Mkdir(scen, 0o755); err != nil {
				t.Fatal(err)
			}
			ref := scen
			if tc.name == "scenario dir is a file" {
				ref = filepath.Join(dir, "file")
				if err := os.WriteFile(ref, []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			demos = strings.ReplaceAll(demos, `"SCEN"`, fmt.Sprintf("%q", ref))
			path := filepath.Join(dir, "demos.json")
			if err := os.WriteFile(path, []byte(`{"demos": `+demos+`}`), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadRegistry(path)
			if err == nil {
				t.Fatalf("LoadRegistry succeeded, want error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// sleepSpawner returns a spawn stub: a plain `sleep 30` per target, tracked
// so the test can assert on kills. The stub carries no scenario semantics —
// it exists to be killed and reaped.
func sleepSpawner(cmds *[]*exec.Cmd, mu *sync.Mutex) spawnFunc {
	return func(t spawnTarget) (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		mu.Lock()
		*cmds = append(*cmds, cmd)
		mu.Unlock()
		return cmd, nil
	}
}

// waitDead asserts the process is gone (signal 0 fails once reaped or
// killed) within the deadline.
func waitDead(t *testing.T, cmd *exec.Cmd, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: process %d still alive", what, cmd.Process.Pid)
}

func TestSupervisorLifecycle(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.ready = func() error { return nil } // sleep never opens a port

	d1 := spawnTarget{Kind: "demo", Demo: &Demo{ID: "demo-one", Run: "r1"}}
	d2 := spawnTarget{Kind: "demo", Demo: &Demo{ID: "demo-two", Run: "r2"}}

	if _, err := sup.start(d1, nil); err != nil {
		t.Fatalf("start d1: %v", err)
	}
	id, _, pid, _, ok := sup.status()
	if !ok || id != "demo-one" {
		t.Fatalf("status after start d1 = (%q, %v), want (demo-one, true)", id, ok)
	}
	if pid != cmds[0].Process.Pid {
		t.Errorf("status pid %d, want child pid %d", pid, cmds[0].Process.Pid)
	}

	// Single active run: starting d2 must kill d1's process.
	if _, err := sup.start(d2, nil); err != nil {
		t.Fatalf("start d2: %v", err)
	}
	waitDead(t, cmds[0], "d1 child after d2 start")
	id, _, _, _, ok = sup.status()
	if !ok || id != "demo-two" {
		t.Fatalf("status after start d2 = (%q, %v), want (demo-two, true)", id, ok)
	}

	sup.stop()
	waitDead(t, cmds[1], "d2 child after stop")
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after stop = active, want idle")
	}
	sup.stop() // idempotent
	if len(cmds) != 2 {
		t.Fatalf("%d spawns, want 2", len(cmds))
	}
}

func TestSupervisorReadyTimeoutLeavesNoZombie(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	// Real port probe against an address nothing will ever listen on.
	sup.wsAddr = "127.0.0.1:1"
	sup.readyTimeout = 500 * time.Millisecond

	start := time.Now()
	_, err := sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "never-ready", Run: "r1"}}, nil)
	if err == nil {
		t.Fatal("start succeeded with no listener; want readiness error")
	}
	if !strings.Contains(err.Error(), "did not accept connections") {
		t.Errorf("error %q, want the port-probe failure", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("start blocked %s, want ~readyTimeout", elapsed)
	}
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after failed start = active, want idle")
	}
	if len(cmds) != 1 {
		t.Fatalf("%d spawns, want 1", len(cmds))
	}
	waitDead(t, cmds[0], "child after failed start")
}

// A failing post-ready verify hook fails the start and leaves NO process
// behind — same discipline as a readiness failure, but for checks that
// need the child UP (demosrv's recording hash check reads its /status).
func TestSupervisorVerifyFailureKillsChild(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.ready = func() error { return nil }
	boom := errors.New("display binding failed")

	_, err := sup.start(spawnTarget{Kind: "replay", Rec: &Recording{ID: "rec1", Run: "r1"}},
		func() error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("start error %v, want the verify error wrapped", err)
	}
	if _, _, _, _, ok := sup.status(); ok {
		t.Fatal("status after failed verify = active, want idle")
	}
	mu.Lock()
	n := len(cmds)
	c0 := cmds[0]
	mu.Unlock()
	if n != 1 {
		t.Fatalf("%d spawns, want 1", n)
	}
	waitDead(t, c0, "child after failed verify")

	// A passing verify is transparent.
	if _, err := sup.start(spawnTarget{Kind: "replay", Rec: &Recording{ID: "rec1", Run: "r1"}},
		func() error { return nil }); err != nil {
		t.Fatalf("start with passing verify: %v", err)
	}
	sup.stop()
}

// Replay-kind spawns are ready only when BOTH the ws port and the HTTP
// control port accept — replay's ws listener opens before its control
// plane binds, and the start response sends the viz at the control plane.
// Stub TCP listeners stand in for the child's two ports; the child itself
// is the usual `sleep`.
func TestSupervisorReplayWaitsForBothPorts(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.readyTimeout = 500 * time.Millisecond
	rec := spawnTarget{Kind: "replay", Rec: &Recording{ID: "rec1", Run: "r1"}}

	wsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wsLn.Close()
	sup.wsAddr = wsLn.Addr().String()

	// ws up, control port down: start must fail on the CONTROL port probe
	// (and leave no process behind, like any readiness failure).
	sup.ctlAddr = "127.0.0.1:1"
	_, err = sup.start(rec, nil)
	if err == nil {
		t.Fatal("replay start succeeded with the control port down")
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") || !strings.Contains(err.Error(), "did not accept connections") {
		t.Errorf("error %q, want the control-port probe failure", err)
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
	waitDead(t, c0, "replay child after failed start")

	// Both ports up: start succeeds.
	ctlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ctlLn.Close()
	sup.ctlAddr = ctlLn.Addr().String()
	if _, err := sup.start(rec, nil); err != nil {
		t.Fatalf("replay start with both ports up: %v", err)
	}
	sup.stop()
	mu.Lock()
	c1 := cmds[1]
	mu.Unlock()
	waitDead(t, c1, "replay child after stop")
}

func TestHTTPEndpoints(t *testing.T) {
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.ready = func() error { return nil }
	reg := &Registry{Demos: []*Demo{
		{ID: "d1", Title: "Demo One", Run: "r1", ScenarioDir: t.TempDir()},
	}}
	srv := &server{reg: reg, sup: sup, viz: t.TempDir(), nets: &netCache{dir: t.TempDir()}}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	get := func(path string) (int, map[string]any) {
		t.Helper()
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return resp.StatusCode, v
	}
	post := func(path string) (int, map[string]any) {
		t.Helper()
		resp, err := http.Post(ts.URL+path, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		return resp.StatusCode, v
	}

	code, body := get("/api/demos")
	if code != 200 {
		t.Fatalf("GET /api/demos = %d", code)
	}
	demos, _ := body["demos"].([]any)
	if len(demos) != 1 {
		t.Fatalf("/api/demos returned %v", body)
	}

	_, body = get("/api/status")
	if body["active"] != nil {
		t.Fatalf("idle status = %v, want active null", body)
	}

	old := runNonce
	runNonce = func() (string, error) { return "t9", nil }
	defer func() { runNonce = old }()
	code, body = post("/api/demo/d1/start")
	if code != 200 {
		t.Fatalf("start = %d (%v)", code, body)
	}
	// Live demos get a per-spawn unique run id (foreign-broker proof).
	if want := "/app/?run=r1-t9&net=/net/d1.geojson&ws=ws%3A%2F%2F127.0.0.1%3A8443"; body["url"] != want {
		t.Errorf("start url = %v, want %s", body["url"], want)
	}

	_, body = get("/api/status")
	if body["active"] != "d1" {
		t.Fatalf("active status = %v, want d1", body)
	}
	if body["run"] != "r1-t9" {
		t.Fatalf("status run = %v, want r1-t9 (the spawned unique id)", body["run"])
	}

	code, _ = post("/api/demo/nope/start")
	if code != http.StatusNotFound {
		t.Errorf("start unknown demo = %d, want 404", code)
	}

	code, _ = post("/api/demo/stop")
	if code != 200 {
		t.Fatalf("stop = %d", code)
	}
	// cmds grows on the handler goroutine (HTTP round-trip creates no
	// happens-before edge) — take mu for the read (go test -race).
	mu.Lock()
	c0 := cmds[0]
	mu.Unlock()
	waitDead(t, c0, "child after HTTP stop")
	_, body = get("/api/status")
	if body["active"] != nil {
		t.Fatalf("status after stop = %v, want idle", body)
	}
}

// TestOverlayEndpoints covers /overlay/{file}.geojson: static WGS84 overlay
// files from -overlaydir, name-gated by the registry id charset, 404 for
// anything else (traversal, non-geojson, missing dir/file — overlays are
// optional, so absence is never a 500).
func TestOverlayEndpoints(t *testing.T) {
	dir := t.TempDir()
	const body = `{"type":"FeatureCollection","features":[]}`
	if err := os.WriteFile(filepath.Join(dir, "zones.geojson"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &server{overlays: dir}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/overlay/zones.geojson")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /overlay/zones.geojson = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/geo+json" {
		t.Errorf("Content-Type = %q, want application/geo+json", ct)
	}
	if string(got) != body {
		t.Errorf("body = %q, want the file verbatim", got)
	}

	for _, path := range []string{
		"/overlay/zones.json",          // only .geojson is served
		"/overlay/zones",               // suffix required
		"/overlay/..%2F..%2Fx.geojson", // traversal never reaches the fs
		"/overlay/missing.geojson",     // absent file
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, resp.StatusCode)
		}
	}

	// A missing overlay directory is a clean 404, not a 500.
	srv2 := &server{overlays: filepath.Join(t.TempDir(), "nope")}
	ts2 := httptest.NewServer(srv2.routes())
	defer ts2.Close()
	resp, err = http.Get(ts2.URL + "/overlay/zones.geojson")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("missing overlaydir: GET = %d, want 404", resp.StatusCode)
	}
}
