package main

// replay_test.go — the VCR wiring: registry validation for "recordings"
// (strict fields, store paths, cross-namespace id uniqueness), the replay
// start/stop lifecycle against the `sleep` spawn stub, /net for recording
// ids, and the control proxy against an httptest stub standing in for the
// replay child's loopback control plane. The real replay binary is never
// spawned (covered by engine/cmd/replay's own tests).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

// Recording run ids are deliberately NOT uniqueness-checked: a recording
// whose source run matches a demo's run is the NORMAL record-a-demo
// relationship (the planes are disjoint — ts.foo.* vs ts.foo-replay.*),
// and shared recording run ids are unambiguous because the ctl binding
// compares against the ACTIVE replay only.
func TestLoadRegistryRecordingRunSharing(t *testing.T) {
	path := writeRecordingRegistry(t, `[
		{"id":"r1","title":"t","store":%q,"run":"liverun","scenarioDir":%q},
		{"id":"r2","title":"t","store":%q,"run":"liverun","scenarioDir":%q}
	]`)
	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry with shared recording run ids: %v", err)
	}
	if len(reg.Recordings) != 2 {
		t.Fatalf("got %d recordings, want 2", len(reg.Recordings))
	}
}

// writeScenarioDir builds a minimal on-disk scenario (one origin lane →
// one exit lane, dt pinned to 0.05) for the recording's scenarioDir: the
// replay start handler loads it for the dt hint, /net for the GeoJSON.
func writeScenarioDir(t *testing.T) string {
	t.Helper()
	return writeScenarioDirDt(t, "0.05")
}

// writeScenarioDirDt is writeScenarioDir with a caller-chosen dt — two
// dirs built with different dts differ in content hash.
func writeScenarioDirDt(t *testing.T, dt string) string {
	t.Helper()
	dir := t.TempDir()
	nf := engine.NetFile{
		Version: 1,
		Name:    "rec-test",
		Lanes: []engine.NetLane{
			{ID: "a_0", Section: "a", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{0, 0}, {500, 0}}, Successors: []string{"b_0"}, Origin: true},
			{ID: "b_0", Section: "b", Length: 500, SpeedLimit: 15, Width: 3.2,
				Shape: [][2]float64{{500, 0}, {1000, 0}}, Exit: true},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "network.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `format_version: 1
id: rec-test
seed: 7
ticks: 300
network: network.json
types: [car]
params:
  dt: ` + dt + `
spawner:
  rate_per_lane_h: 600
`
	if err := os.WriteFile(filepath.Join(dir, "scenario.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeRecordingRegistry writes a registry file with one demo and one
// recording, both backed by real dirs (scenarioDir loadable, store an
// empty dir), and returns (registryPath, scenarioDir, storeDir).
func writeRecordingRegistry(t *testing.T, recJSON string) string {
	t.Helper()
	dir := t.TempDir()
	scen := writeScenarioDir(t)
	store := filepath.Join(dir, "store")
	if err := os.Mkdir(store, 0o755); err != nil {
		t.Fatal(err)
	}
	if recJSON == "" {
		recJSON = `[{
			"id": "rec1", "title": "Recorded run", "blurb": "b",
			"store": %q, "run": "recrun", "scenarioDir": %q
		}]`
	}
	// %q slots alternate store, scenarioDir (store, scenarioDir, ...).
	n := strings.Count(recJSON, "%q")
	args := make([]any, 0, n)
	for i := 0; i < n; i++ {
		if i%2 == 0 {
			args = append(args, store)
		} else {
			args = append(args, scen)
		}
	}
	recJSON = fmt.Sprintf(recJSON, args...)
	body := fmt.Sprintf(`{"demos": [{
		"id": "d1", "title": "Demo", "scenarioDir": %q, "run": "liverun"
	}], "recordings": %s}`, scen, recJSON)
	path := filepath.Join(dir, "demos.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRegistryRecordings(t *testing.T) {
	reg, err := LoadRegistry(writeRecordingRegistry(t, ""))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(reg.Recordings) != 1 {
		t.Fatalf("got %d recordings, want 1", len(reg.Recordings))
	}
	rec := reg.Recordings[0]
	if rec.ID != "rec1" || rec.Run != "recrun" || rec.Title == "" {
		t.Errorf("recording decoded wrong: %+v", rec)
	}
	if rec.Kind != "replay" {
		t.Errorf("recording kind = %q, want replay", rec.Kind)
	}
	if reg.Demos[0].Kind != "demo" {
		t.Errorf("demo kind = %q, want demo", reg.Demos[0].Kind)
	}
	if !filepath.IsAbs(rec.Store) || !filepath.IsAbs(rec.ScenarioDir) {
		t.Errorf("paths not made absolute: %+v", rec)
	}
	if reg.recByID("rec1") != rec || reg.recByID("d1") != nil || reg.byID("rec1") != nil {
		t.Errorf("namespace lookups crossed: recByID/byID must stay separate")
	}
}

func TestLoadRegistryRecordingValidation(t *testing.T) {
	cases := []struct {
		name string
		rec  string // raw "recordings" array; %q slots: store, scenarioDir
		want string
	}{
		{"unknown field", `[{"id":"r","title":"t","store":%q,"run":"x","scenarioDir":%q,"bogus":1}]`, "unknown field"},
		{"missing store", `[{"id":"r","title":"t","store":"/no/such/store-anywhere","run":"x","scenarioDir":%q}]`, "store"},
		{"duplicate id across kinds", `[{"id":"d1","title":"t","store":%q,"run":"x","scenarioDir":%q}]`, "duplicate id"},
		{"duplicate recording id", `[
			{"id":"r","title":"t","store":%q,"run":"x","scenarioDir":%q},
			{"id":"r","title":"t","store":%q,"run":"y","scenarioDir":%q}
		]`, "duplicate id"},
		{"missing title", `[{"id":"r","store":%q,"run":"x","scenarioDir":%q}]`, "title"},
		{"run with dot", `[{"id":"r","title":"t","store":%q,"run":"bad.token","scenarioDir":%q}]`, "must match [A-Za-z0-9_-]+"},
		{"run with replay suffix", `[{"id":"r","title":"t","store":%q,"run":"x-replay","scenarioDir":%q}]`, "reserved replay-plane suffix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRecordingRegistry(t, tc.rec)
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

// newReplayTestServer wires a server with the sleep spawn stub and (when
// ctl is non-nil) the stub control plane as the proxy target.
func newReplayTestServer(t *testing.T, ctlURL string) (*server, *[]*exec.Cmd, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	var cmds []*exec.Cmd
	sup := newSupervisor(sleepSpawner(&cmds, &mu))
	sup.ready = func() error { return nil }
	srv := &server{
		reg: &Registry{}, sup: sup, viz: t.TempDir(), nets: &netCache{dir: t.TempDir()},
		replayCtl: ctlURL, ctlTimeout: 2 * time.Second, seekTimeout: 10 * time.Second,
	}
	return srv, &cmds, &mu
}

func TestReplayStartStopAndNet(t *testing.T) {
	scen := writeScenarioDir(t)
	// The start handler verifies the recording against the scenario via
	// the child's /status hash — stand a stub control plane up carrying
	// the fixture scenario's content hash so the check passes.
	loaded, err := scenario.Load(scen)
	if err != nil {
		t.Fatal(err)
	}
	stub := newStubCtl(t, loaded.Hash())
	srv, cmds, mu := newReplayTestServer(t, stub.srv.URL)
	srv.reg = &Registry{
		Demos:      []*Demo{{ID: "d1", Title: "Demo", Run: "liverun", ScenarioDir: scen, Kind: "demo"}},
		Recordings: []*Recording{{ID: "rec1", Title: "Rec", Run: "recrun", Store: t.TempDir(), ScenarioDir: scen, Kind: "replay"}},
	}
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

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

	// Unknown recording.
	if code, _ := post("/api/replay/nope/start"); code != http.StatusNotFound {
		t.Fatalf("start unknown recording = %d, want 404", code)
	}

	// Start the recording: deep link carries {run}-replay and the
	// scenario's dt (%g, like serve's hint).
	code, body := post("/api/replay/rec1/start")
	if code != 200 {
		t.Fatalf("replay start = %d (%v)", code, body)
	}
	if want := "/app/?run=recrun-replay&net=/net/rec1.geojson&dt=0.05"; body["url"] != want {
		t.Errorf("replay start url = %v, want %s", body["url"], want)
	}

	// /net serves the recording's scenario network as GeoJSON.
	resp, err := http.Get(ts.URL + "/net/rec1.geojson")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/geo+json") {
		t.Errorf("GET /net/rec1.geojson = %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// The swap is symmetric: starting a demo kills the replay child.
	if code, body := post("/api/demo/d1/start"); code != 200 {
		t.Fatalf("demo start after replay = %d (%v)", code, body)
	}
	mu.Lock()
	recCmd := (*cmds)[0]
	mu.Unlock()
	waitDead(t, recCmd, "replay child after demo start")

	// And back: starting a replay kills the demo child; stop kills the
	// replay child too (the supervisor is generic over kinds).
	if code, body := post("/api/replay/rec1/start"); code != 200 {
		t.Fatalf("replay restart = %d (%v)", code, body)
	}
	mu.Lock()
	demoCmd := (*cmds)[1]
	mu.Unlock()
	waitDead(t, demoCmd, "demo child after replay restart")
	if code, _ := post("/api/demo/stop"); code != 200 {
		t.Fatalf("stop = %d", code)
	}
	mu.Lock()
	recCmd2 := (*cmds)[2]
	n := len(*cmds)
	mu.Unlock()
	waitDead(t, recCmd2, "replay child after stop")
	if n != 3 {
		t.Fatalf("%d spawns, want 3", n)
	}
}

// stubCtl is an httptest server standing in for the replay child's control
// plane: /status answers a canned player status, the control verbs record
// their bodies and answer the (configurable) status code with the status
// JSON, like natsio.Player.Handler.
type stubCtl struct {
	srv       *httptest.Server
	code      atomic.Int32
	seekDelay atomic.Int64 // ms to sleep before answering /seek
	lastBody  atomic.Value // string of the last control body
}

func newStubCtl(t *testing.T, hash string) *stubCtl {
	t.Helper()
	st := &stubCtl{}
	st.code.Store(200)
	mux := http.NewServeMux()
	status := fmt.Sprintf(`{"run":"recrun","replayRun":"recrun-replay","tick":10,"ticks":300,"endTick":300,"speed":1,"paused":false,"done":false,"dt":0.05,"hash":%q,"crcErrors":0,"verbErrors":0}`, hash)
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, status)
	})
	for _, verb := range []string{"pause", "resume", "speed", "seek"} {
		mux.HandleFunc("POST /"+verb, func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			st.lastBody.Store(verb + ":" + string(body))
			if verb == "seek" && st.seekDelay.Load() > 0 {
				time.Sleep(time.Duration(st.seekDelay.Load()) * time.Millisecond)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(int(st.code.Load()))
			io.WriteString(w, status)
		})
	}
	st.srv = httptest.NewServer(mux)
	t.Cleanup(st.srv.Close)
	return st
}

// TestCheckRecordingHash covers the start-time binding of display scenario
// to recording: match, mismatch (the scenario-edited-after-recording 409),
// and fail-closed on a hashless recording (flag-built: the display network
// cannot be verified, so start is refused).
func TestCheckRecordingHash(t *testing.T) {
	mkStub := func(t *testing.T, hash string) string {
		t.Helper()
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"run":"x","replayRun":"x-replay","tick":0,"hash":%q}`, hash)
		}))
		t.Cleanup(s.Close)
		return s.URL
	}

	match := mkStub(t, "abc123")
	if err := checkRecordingHash(match, "abc123"); err != nil {
		t.Errorf("matching hashes: %v", err)
	}
	if err := checkRecordingHash(match, "different"); !errors.Is(err, errRecordingMismatch) {
		t.Errorf("mismatch: got %v, want errRecordingMismatch", err)
	}
	// Fail closed: the registry always supplies a scenario hash, so an
	// EMPTY recorded hash (flag-built recording) means the display cannot
	// be verified — refuse.
	empty := mkStub(t, "")
	if err := checkRecordingHash(empty, "abc123"); !errors.Is(err, errRecordingUnverifiable) {
		t.Errorf("empty recorded hash: got %v, want errRecordingUnverifiable", err)
	}
	// Defensive branch (unreachable via the registry): an empty scenario
	// hash skips the check rather than failing.
	if err := checkRecordingHash(match, ""); err != nil {
		t.Errorf("empty scenario hash: got %v, want skip", err)
	}
	if err := checkRecordingHash(empty, ""); err != nil {
		t.Errorf("both hashes empty: got %v, want skip", err)
	}
	// A dead control plane is an ordinary error, NOT a binding failure.
	if err := checkRecordingHash("http://127.0.0.1:1", "abc123"); err == nil ||
		errors.Is(err, errRecordingMismatch) || errors.Is(err, errRecordingUnverifiable) {
		t.Errorf("unreachable child: got %v, want a fetch error (not a binding failure)", err)
	}
}

// startSleepReplay puts a (sleep-stub) replay child under the supervisor so
// the proxy's active-kind gate opens.
func startSleepReplay(t *testing.T, srv *server) {
	t.Helper()
	if err := srv.sup.start(spawnTarget{Kind: "replay", Rec: &Recording{ID: "rec1", Run: "recrun"}}, nil); err != nil {
		t.Fatalf("start replay stub: %v", err)
	}
	t.Cleanup(srv.sup.stop)
}

func TestReplayProxy(t *testing.T) {
	stub := newStubCtl(t, "")
	srv, _, _ := newReplayTestServer(t, stub.srv.URL)
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()

	// Idle: nothing to control.
	resp, err := http.Get(ts.URL + "/api/replay/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status while idle = %d, want 404", resp.StatusCode)
	}

	// Live demo active: still nothing to control.
	if err := srv.sup.start(spawnTarget{Kind: "demo", Demo: &Demo{ID: "d1", Run: "r1"}}, nil); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(ts.URL + "/api/replay/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status while demo active = %d, want 404", resp.StatusCode)
	}

	startSleepReplay(t, srv)

	// Status passes through verbatim — unbound (a panel's first probe).
	resp, err = http.Get(ts.URL + "/api/replay/status")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("proxied status = %d", resp.StatusCode)
	}
	var st map[string]any
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("status body: %v", err)
	}
	if st["replayRun"] != "recrun-replay" || st["dt"] != 0.05 || st["tick"] != 10.0 {
		t.Errorf("status not passed through: %s", body)
	}

	// Bound status: matching ?run= is served, a mismatched one gets
	// demosrv's own 409 (never the child's body) so a stale tab notices
	// the swap instead of adopting the replacement replay.
	resp, err = http.Get(ts.URL + "/api/replay/status?run=recrun-replay")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("bound status (match) = %d, want 200", resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/api/replay/status?run=someone-else-replay")
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("bound status (mismatch) = %d, want 409", resp.StatusCode)
	}
	if strings.Contains(string(sb), `"replayRun"`) {
		t.Errorf("mismatched status reached the child: %s", sb)
	}

	// Control verbs: run binding enforced, body forwarded, status code +
	// JSON passed through. The active replay's run is "recrun-replay".
	for _, tc := range []struct{ path, body string }{
		{"/api/replay/ctl/pause?run=recrun-replay", ""},
		{"/api/replay/ctl/resume?run=recrun-replay", ""},
		{"/api/replay/ctl/speed?run=recrun-replay", `{"speed": 4}`},
		{"/api/replay/ctl/seek?run=recrun-replay", `{"tick": 120}`},
	} {
		resp, err := http.Post(ts.URL+tc.path, "application/json", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("POST %s = %d, want 200", tc.path, resp.StatusCode)
		}
		if !strings.Contains(string(rb), `"replayRun"`) {
			t.Errorf("POST %s body = %s, want the player status JSON", tc.path, rb)
		}
		if tc.body != "" {
			if got := stub.lastBody.Load().(string); !strings.HasSuffix(got, ":"+tc.body) {
				t.Errorf("POST %s forwarded body %q, want %q", tc.path, got, tc.body)
			}
		}
	}

	// The child's non-200 (409 while done, e.g. resume-after-end) passes
	// through too.
	stub.code.Store(http.StatusConflict)
	resp, err = http.Post(ts.URL+"/api/replay/ctl/resume?run=recrun-replay", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("resume with child 409 = %d, want 409", resp.StatusCode)
	}
	stub.code.Store(200)

	// Run binding: missing or wrong ?run= is demosrv's OWN 409 — the
	// request must never reach the child (a stale tab must not steer a
	// different recording on the reused control port).
	for _, path := range []string{
		"/api/replay/ctl/pause",                  // missing run
		"/api/replay/ctl/pause?run=someone-else", // wrong run
		"/api/replay/ctl/pause?run=recrun",       // recorded id, not the live replay id
	} {
		resp, err := http.Post(ts.URL+path, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("POST %s = %d, want 409", path, resp.StatusCode)
		}
		if strings.Contains(string(rb), `"replayRun"`) {
			t.Errorf("POST %s reached the child: %s", path, rb)
		}
	}

	// Unknown verbs are rejected by demosrv, never reach the child. (405,
	// not 404: no ctl pattern matches and the GET / static catch-all claims
	// the path for the wrong method.)
	resp, err = http.Post(ts.URL+"/api/replay/ctl/explode", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("unknown ctl verb = %d, want 405", resp.StatusCode)
	}
}

func TestReplaySeekTimeout(t *testing.T) {
	stub := newStubCtl(t, "")
	stub.seekDelay.Store(300)
	srv, _, _ := newReplayTestServer(t, stub.srv.URL)
	srv.seekTimeout = 50 * time.Millisecond // shrink the 10 s default for the test
	ts := httptest.NewServer(srv.routes())
	defer ts.Close()
	startSleepReplay(t, srv)

	start := time.Now()
	resp, err := http.Post(ts.URL+"/api/replay/ctl/seek?run=recrun-replay", "application/json", strings.NewReader(`{"tick": 1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("seek past the proxy timeout = %d, want 502", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("seek blocked %s, want ~seekTimeout", elapsed)
	}
}
