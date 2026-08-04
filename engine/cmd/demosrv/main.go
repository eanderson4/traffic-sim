// demosrv is the local demo launcher: ONE Go process that serves the splash
// landing page (at /), the demo menu page (/demos.html) and the built viz
// (viz/dist), and spawns/kills engine children
// on demand — `serve` for live demos, `replay` (the VCR driver) for
// recorded runs. It is localhost process orchestration in the spirit
// of `pnpm dev` — plain HTTP to the browser, NO NATS here (ADR-0002 covers
// the service planes; the demo's engine child still exposes its own
// WebSocket listener for the live snapshot stream, ADR-0006 §8 unchanged).
// Standard library only, plus the engine packages themselves.
//
// Single-active-run: starting a demo SIGTERMs the previous engine (SIGKILL
// after 2 s) — one engine at a time on the fixed WebSocket port
// 127.0.0.1:8443, which is also the viz client's default ?ws= target.
//
// Paths — the registry's scenarioDir/store values and the -demos/-viz/
// -netcache defaults — are REPO-ROOT-relative: run demosrv from the
// repository root (go run ./engine/cmd/demosrv), or pass explicit flags.
// The serve and replay binaries are built ONCE at startup (pre-warm: the
// first demo start is then a process exec, not a cold compile, and there
// is no `go run` wrapper between demosrv and the engine to eat signals).
// SIGINT/SIGTERM to demosrv kills the active child too.
//
// Public deployment (GKE behind a 443-only Ingress, ADR-0020) adds four
// flags, all defaulting to the local-dev behavior above: -wspublic pins the
// client-facing engine ws URL verbatim (the LB owns the public name and the
// wss scheme — no rewrite of the listen address reconstructs it),
// -admintoken bearer-gates the MUTATING routes (GETs stay public),
// -autostart starts one demo once the listener is up (failure logs and
// keeps serving), and -nobuild execs prebuilt serve/replay binaries from a
// directory instead of the go-build pre-warm (the container-image case).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8900", "listen address for the menu + orchestration API")
	demosPath := flag.String("demos", "data/scenarios/demos.json", "demos registry (repo-root-relative paths inside)")
	vizDir := flag.String("viz", "viz/dist", "built viz to serve (cd viz && pnpm build): splash.html (landing, at /) + demos.html (menu) + index.html (map app)")
	netcacheDir := flag.String("netcache", "data/networks/.geojson-cache", "per-demo network GeoJSON cache")
	overlayDir := flag.String("overlaydir", "data/networks/overlays", "static overlay GeoJSON directory (zones/boundaries, WGS84) — a missing dir just 404s /overlay/*")
	wsAddr := flag.String("ws", "127.0.0.1:8443", "engine WebSocket listen address for spawned runs; override when another process holds 8443 (clients are told via /api/demos + start URLs)")
	wsPublic := flag.String("wspublic", "", "verbatim client-facing engine WebSocket URL (e.g. wss://traffic-ws.example.com) for ALL advertised ws URLs — the TLS-terminating-LB case, where no rewrite of -ws reconstructs what browsers dial; empty = advertise the -ws listen address (legacy). Requires -admintoken/DEMOSRV_ADMIN_TOKEN (public shape must not serve the mutating POSTs open)")
	adminToken := flag.String("admintoken", "", "bearer token required on the mutating endpoints (demo/replay start, demo stop, replay ctl verbs); GETs stay public either way, UNSET flag = DEMOSRV_ADMIN_TOKEN env fallback, unset everywhere = open (local dev); a supplied-but-empty token fatals (fail closed)")
	autostartID := flag.String("autostart", "", "demo id to start once the HTTP listener is up (pod-style deploys); retries with backoff, then logs and KEEPS SERVING — unknown id likewise")
	nobuildDir := flag.String("nobuild", "", "directory with PREBUILT serve+replay binaries: skip the go-build pre-warm and exec <dir>/serve + <dir>/replay (container images); stat-checked at startup, fatal when missing/non-executable")
	flag.Parse()

	wsListenAddr = *wsAddr
	wsPublicURL = *wsPublic

	// Flag validation FIRST — before the registry load and the go-build
	// pre-warm, so a misconfiguration fatals in milliseconds, not after
	// the expensive startup work (Fable, round 8).
	if wsPublicURL != "" {
		if err := validateWsPublic(wsPublicURL); err != nil {
			// The value is NOT echoed: a userinfo-bearing typo would put
			// its password in the startup log (Fable, round 29).
			log.Fatalf("demosrv: -wspublic: %v — it would be advertised verbatim to every client", err)
		}
	}
	// flag.Visit, not the flag VALUE: `-admintoken=` (or a manifest's
	// "$TOK" expanding empty) is indistinguishable from an absent flag
	// through *adminToken, and the fail-closed rule depends on the
	// difference (Fable + sol, round 9).
	adminTokenSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "admintoken" {
			adminTokenSet = true
		}
	})
	adminTok, err := resolveAdminToken(*adminToken, adminTokenSet)
	if err != nil {
		log.Fatalf("demosrv: %v", err)
	}
	if wsPublicURL != "" && adminTok == "" {
		// -wspublic explicitly selects the PUBLIC deployment shape; with
		// no token, every spawn/stop/replay-ctl POST is open to the
		// internet — almost certainly a manifest that dropped the Secret,
		// and fail-closed is the posture of everything else here (sol,
		// round 13).
		log.Fatalf("demosrv: -wspublic set but no admin token (-admintoken or %s) — refusing to serve the mutating POSTs OPEN on the public deployment shape", adminTokenEnv)
	}

	reg, err := LoadRegistry(*demosPath)
	if err != nil {
		log.Fatalf("demosrv: %v", err)
	}
	reg.WS = wsClientURL()
	if err := os.MkdirAll(*netcacheDir, 0o755); err != nil {
		log.Fatalf("demosrv: netcache: %v", err)
	}

	// Engine binaries, one of two sources. Default: build serve and replay
	// ONCE here (pre-warm — the first demo start is then a process exec,
	// not a cold compile, and there is no `go run` wrapper between demosrv
	// and the engine to eat signals). -nobuild instead points at prebuilt
	// binaries (the container image ships them; the pod may not even have
	// a Go toolchain), stat-checked up front by prebuiltBins.
	var serveBin, replayBin string
	if *nobuildDir != "" {
		serveBin, replayBin, err = prebuiltBins(*nobuildDir)
		if err != nil {
			log.Fatalf("demosrv: %v", err)
		}
		log.Printf("demosrv: -nobuild: using prebuilt engines %s, %s", serveBin, replayBin)
	} else {
		engineDir, err := findEngineDir()
		if err != nil {
			log.Fatalf("demosrv: %v", err)
		}
		binDir, err := os.MkdirTemp("", "traffic-sim-demosrv-")
		if err != nil {
			log.Fatalf("demosrv: %v", err)
		}
		defer os.RemoveAll(binDir)
		serveBin = filepath.Join(binDir, "serve")
		replayBin = filepath.Join(binDir, "replay")
		for _, b := range []struct{ bin, pkg string }{{serveBin, "./cmd/serve"}, {replayBin, "./cmd/replay"}} {
			log.Printf("demosrv: building %s (go build %s in %s/)", filepath.Base(b.bin), b.pkg, engineDir)
			build := exec.Command("go", "build", "-o", b.bin, b.pkg)
			build.Dir = engineDir
			// Same stripEnv discipline as the engine children: the admin
			// token is demosrv's own credential (Fable, round 11).
			build.Env = stripEnv(os.Environ(), adminTokenEnv)
			build.Stdout = os.Stderr
			build.Stderr = os.Stderr
			if err := build.Run(); err != nil {
				log.Fatalf("demosrv: go build %s: %v", b.pkg, err)
			}
		}
	}

	lg := log.New(os.Stderr, "", log.LstdFlags)
	sup := newSupervisor(childSpawner(serveBin, replayBin, lg))
	srv := &server{
		reg: reg, sup: sup, viz: *vizDir, nets: &netCache{dir: *netcacheDir}, overlays: *overlayDir,
		replayCtl: "http://" + replayCtlAddr, ctlTimeout: ctlTimeout, seekTimeout: seekTimeout,
		adminToken: adminTok,
		shutdown:   make(chan struct{}),
	}
	if srv.adminToken != "" {
		log.Printf("demosrv: admin gate ON — mutating POSTs require a bearer token (flag or %s)", adminTokenEnv)
	}
	if wsPublicURL != "" {
		// The HTTP gate does NOT cover this: the advertised engine ws is
		// the embedded broker's unauthenticated, publish-capable plane —
		// ADR-0020's go-live precondition, true with or without the token
		// (sol, round 11).
		log.Printf("demosrv: warning: -wspublic advertises the engine's UNAUTHENTICATED ws plane (publish-capable) — broker auth is an ADR-0020 go-live precondition, not part of this build")
	}
	// ReadHeaderTimeout because -admintoken exists precisely for the
	// PUBLIC deployment: slow-loris clients must not hold the menu's
	// connections forever (sol review). The engine's ws server is a
	// different listener (the child's); this covers only demosrv's HTTP.
	hs := &http.Server{Handler: srv.routes(), ReadHeaderTimeout: 10 * time.Second}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("demosrv: %v — killing the active run and exiting", s)
		srv.signalShutdown() // first: autostart's backoff sleep aborts now
		sup.shutdownFinal()  // and the latch refuses any start from here on
		hs.Close()
	}()

	if _, err := os.Stat(filepath.Join(*vizDir, "splash.html")); err != nil {
		log.Printf("demosrv: warning: %s/splash.html not found — build the viz first (cd viz && pnpm build)", *vizDir)
	}
	// Bind BEFORE announcing and BEFORE autostart: with -autostart the demo
	// spawn must follow the listener actually being up (a pod whose port
	// never binds should fatal here, not autostart into the void).
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("demosrv: %v", err)
	}
	log.Printf("demosrv: %d demo(s), %d recording(s) on http://%s/ — single active run, engine ws %s",
		len(reg.Demos), len(reg.Recordings), ln.Addr(), wsListenAddr)
	if *autostartID != "" {
		// Direct supervisor start, bypassing the HTTP route (and with it the
		// adminGate bearer check — the pod itself holds no token). The stop
		// epoch is captured NOW, synchronously: an HTTP stop can only land
		// after Serve begins, and the goroutine's own snapshot could be
		// scheduled after such a stop completed (sol, round 19).
		go srv.autostartDemo(*autostartID, sup.stopEpoch.Load())
	}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		// Kill the engine child BEFORE fataling: with -autostart a live
		// run is the norm, and exiting here would orphan it on the ws
		// port (Fable).
		srv.signalShutdown()
		sup.shutdownFinal()
		log.Fatalf("demosrv: %v", err)
	}
}

// findEngineDir locates the engine module root for the serve build:
// "engine" from the repo root (the documented layout), "." from inside
// engine/ (where the other paths then have to come from flags).
func findEngineDir() (string, error) {
	if _, err := os.Stat("engine/go.mod"); err == nil {
		return "engine", nil
	}
	if _, err := os.Stat("go.mod"); err == nil {
		return ".", nil
	}
	return "", errors.New("cannot locate the engine module — run from the repo root, or from engine/ with explicit -demos/-viz/-netcache")
}

// childSpawner is the real spawnFunc: exec the prebuilt serve or replay
// binary for the target, output teed into demosrv's log with a [id] prefix
// so child lines stay attributable.
func childSpawner(serveBin, replayBin string, lg *log.Logger) spawnFunc {
	return func(t spawnTarget) (*exec.Cmd, error) {
		var bin string
		var args []string
		if t.Kind == "replay" {
			bin = replayBin
			args = []string{"-store", t.Rec.Store, "-run", t.Rec.Run,
				"-ws", wsListenAddr, "-http", replayCtlAddr}
		} else {
			bin, args = serveBin, serveArgs(t.Demo)
		}
		cmd := exec.Command(bin, args...)
		// The admin token is demosrv's own credential — strip it from the
		// child's inherited environment (the engine exposes the
		// unauthenticated ws plane; its env is one /proc read away).
		cmd.Env = stripEnv(os.Environ(), adminTokenEnv)
		w := &prefixWriter{lg: lg, prefix: "[" + t.id() + "] "}
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
}

// advertisedWsURL is the engine ws URL for client-facing responses. Only
// a WILDCARD listen (0.0.0.0/::/empty) is host-substitutable: the engine
// accepts connections on every interface then, and the request's Host
// names the machine the browser already reached (IPv6-safe). A LOOPBACK
// listen passes through verbatim — remote browsers can't use it, but
// substituting the request host would advertise a listener that refuses
// them, which is worse (sol review).
func advertisedWsURL(r *http.Request) string {
	// -wspublic wins outright, VERBATIM: the operator has named the URL
	// browsers dial (TLS-terminating LB, 443 only) and any host/port
	// substitution of the listen address would corrupt it.
	if wsPublicURL != "" {
		return wsPublicURL
	}
	host, port, err := net.SplitHostPort(wsListenAddr)
	if err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		reqHost, _, herr := net.SplitHostPort(r.Host)
		if herr != nil {
			reqHost = r.Host
		}
		if reqHost != "" {
			return "ws://" + net.JoinHostPort(reqHost, port)
		}
	}
	return wsClientURL()
}

// serveArgs builds the serve command line for a live demo: scenario/run
// plus the optional seed/ticks/capacity overrides. The attach timeout is
// raised from serve's 30 s default: the embedded driver attaches only after
// world build + its initial exit-routing pass — ~100 s on sf-lean (303k
// lanes), ~400 s on la-lean (1.38M lanes, the menu's biggest net).
func serveArgs(d *Demo) []string {
	args := []string{"-scenario", d.ScenarioDir, "-run", d.Run, "-ws", wsListenAddr, "-attach-timeout", "600s"}
	if d.Seed != nil {
		args = append(args, "-seed", strconv.FormatUint(*d.Seed, 10))
	}
	if d.Ticks != nil {
		args = append(args, "-ticks", strconv.FormatUint(*d.Ticks, 10))
	}
	if d.Capacity != nil {
		args = append(args, "-capacity", strconv.FormatUint(*d.Capacity, 10))
	}
	return args
}

// tailLines is how many of a child's most recent output lines prefixWriter
// keeps for start-error reporting (supervisor's tailSuffix): enough to carry
// the engine's own failure line ("driver: hello: nats: no responders…")
// into the 502 body without dumping the whole log.
const tailLines = 20

// prefixWriter tees child output into demosrv's log line-by-line with a
// [id] prefix, and keeps the last tailLines complete lines in a ring so a
// failed start can quote the child's own error (supervisor's tailSuffix
// reads it via tail()). A trailing partial line (no \n before exit) is
// dropped — cosmetic only. Write/tail share mu: the child keeps writing
// while the API handler reads the tail.
type prefixWriter struct {
	lg     *log.Logger
	prefix string
	mu     sync.Mutex
	buf    []byte
	ring   []string // last complete lines, oldest first
}

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.lg.Printf("%s%s", w.prefix, line)
		w.ring = append(w.ring, line)
		if len(w.ring) > tailLines {
			w.ring = w.ring[1:]
		}
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// tail returns the last captured lines (oldest first), "" when the child
// printed nothing. Safe against a still-writing child.
func (w *prefixWriter) tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.ring, "\n")
}

type server struct {
	reg  *Registry
	sup  *supervisor
	viz  string
	nets *netCache
	// overlays is the static WGS84 overlay directory (zones/boundaries);
	// served read-only under /overlay/{file}.geojson. Unlike netcache it is
	// NEVER created — overlays are optional, a missing dir is a 404.
	overlays string
	// replayCtl is the replay child's control-plane base URL (loopback;
	// overridable in tests to point at an httptest stub). ctlTimeout covers
	// status/pause/resume/speed; seek gets seekTimeout (its re-simulation
	// can take ~1 s+ on big records).
	replayCtl   string
	ctlTimeout  time.Duration
	seekTimeout time.Duration
	// adminToken is -admintoken: when non-empty, the mutating routes (start/
	// stop/replay-ctl) require it as a bearer token (adminGate). Empty = the
	// open local-dev behavior. LOAD-BEARING: the gate binds at routes()
	// construction — set this before calling routes() (main resolves the
	// flag/env before building the server).
	adminToken string
	// shutdown, when non-nil, is closed by the signal handler BEFORE
	// sup.shutdownFinal(): autostartDemo's retry loop selects on it so
	// its backoff sleep aborts fast (the supervisor's closed latch then
	// atomically refuses any later start — the child would be orphaned
	// on the ws port — Fable, round 2/3). Nil in tests = never fires.
	shutdown chan struct{}
	// shutdownOnce guards close(shutdown): the signal handler AND the
	// hs.Serve error path can both reach it (the same incident produces
	// SIGTERM + an accept error on a public pod — Fable, round 17).
	shutdownOnce sync.Once
}

// signalShutdown closes s.shutdown exactly once (nil-safe for tests).
func (s *server) signalShutdown() {
	s.shutdownOnce.Do(func() {
		if s.shutdown != nil {
			close(s.shutdown)
		}
	})
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleSplash)
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /app/", s.handleApp)
	mux.HandleFunc("GET /api/demos", s.handleDemos)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	// The POSTs are the MUTATING surface (spawn/kill the engine child or
	// drive its replay) — adminGate bearer-checks them when -admintoken is
	// set and is a pass-through otherwise. Everything GET stays public.
	mux.HandleFunc("POST /api/demo/{id}/start", s.adminGate(s.handleStart))
	mux.HandleFunc("GET /api/demo/{id}/params", s.handleParams)
	mux.HandleFunc("POST /api/demo/stop", s.adminGate(s.handleStop))
	mux.HandleFunc("POST /api/replay/{id}/start", s.adminGate(s.handleReplayStart))
	mux.HandleFunc("GET /api/replay/status", s.handleReplayStatus)
	// The four control verbs are registered explicitly (no {action}
	// wildcard — it would conflict with /api/replay/{id}/start over paths
	// like /api/replay/ctl/start): the proxy stays dumb, not open-ended,
	// and unknown verbs 404 at the mux.
	mux.HandleFunc("POST /api/replay/ctl/pause", s.adminGate(s.ctlRoute("/pause", false)))
	mux.HandleFunc("POST /api/replay/ctl/resume", s.adminGate(s.ctlRoute("/resume", false)))
	mux.HandleFunc("POST /api/replay/ctl/speed", s.adminGate(s.ctlRoute("/speed", false)))
	mux.HandleFunc("POST /api/replay/ctl/seek", s.adminGate(s.ctlRoute("/seek", true)))
	// Go's ServeMux wildcards are whole segments, so the .geojson suffix is
	// parsed out of {file} in the handler.
	mux.HandleFunc("GET /net/{file}", s.handleNet)
	// Same whole-segment wildcard as /net — the .geojson suffix is parsed
	// out of {file} in the handler.
	mux.HandleFunc("GET /overlay/{file}", s.handleOverlay)
	// Everything else (the vite /assets/… bundles) is static viz output.
	mux.Handle("GET /", http.FileServer(http.Dir(s.viz)))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

// handleSplash serves the built landing page (viz/splash.html →
// dist/splash.html) at /. The demos menu keeps its own URL — /demos.html
// falls through to the static FileServer below.
func (s *server) handleSplash(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(s.viz, "splash.html"))
}

// handleApp serves the built map app; the run/network selection rides in
// the query string (?run=&net= — viz/src/config.ts), so one built page
// serves every demo.
func (s *server) handleApp(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(s.viz, "index.html"))
}

func (s *server) handleDemos(w http.ResponseWriter, r *http.Request) {
	// Per-request ws advertisement: loopback/wildcard listens resolve to
	// the host the BROWSER reached (advertisedWsURL), so running-card
	// links stay dialable from other machines (sol review). Copy the
	// registry value — the shared one keeps main's flag-based default.
	reg := *s.reg
	reg.WS = advertisedWsURL(r)
	writeJSON(w, http.StatusOK, reg)
}

type statusResponse struct {
	Active    *string `json:"active"`
	Run       string  `json:"run,omitempty"` // live run id (unique per spawn for demos)
	PID       int     `json:"pid"`
	StartedAt string  `json:"startedAt,omitempty"`
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id, run, pid, startedAt, ok := s.sup.status()
	resp := statusResponse{}
	if ok {
		resp.Active = &id
		resp.Run = run
		resp.PID = pid
		resp.StartedAt = startedAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d := s.reg.byID(id)
	if d == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown demo %q", id))
		return
	}
	run, err := s.sup.start(spawnTarget{Kind: "demo", Demo: d}, nil)
	if err != nil {
		code := http.StatusBadGateway
		if errors.Is(err, errStartAborted) {
			// A stop is in progress (the stopPending refusal): the start
			// was deliberately rejected, not broken — 409, "retry after
			// the stop lands" (Fable, rounds 20-21).
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	// A concurrent start can supersede ours between spawn and response:
	// navigate the caller at whatever run of THIS demo is actually alive
	// (a same-demo restart's newer generation), never at a dead namespace.
	// A DIFFERENT demo winning the race means our run is gone either way —
	// 409, not a mismatched run/net pair.
	if liveID, live, _, _, ok := s.sup.status(); ok && live != run {
		if liveID != d.ID {
			writeErr(w, http.StatusConflict, fmt.Errorf("start superseded by demo %q", liveID))
			return
		}
		run = live
	}
	// Same shape as the menu page's buildAppURL (viz/src/demos-core.ts)
	// — they must agree, the running-card deep link depends on it. The ws
	// param pins the engine this demosrv spawns to (viz defaults to 8443,
	// which another process may hold when -ws overrode it).
	writeJSON(w, http.StatusOK, map[string]string{
		"url": fmt.Sprintf("/app/?run=%s&net=/net/%s.geojson&ws=%s", run, d.ID, url.QueryEscape(advertisedWsURL(r))),
	})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.sup.stop()
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

// netScenarioDir resolves a /net/{id} entry (demo OR recording — both
// namespaces share /net/{id}: a recording's scenarioDir is the scenario
// the recording was made from, so the same generation path serves it).
func (s *server) netScenarioDir(id string) (string, bool) {
	if d := s.reg.byID(id); d != nil {
		return d.ScenarioDir, true
	}
	if rec := s.reg.recByID(id); rec != nil {
		return rec.ScenarioDir, true
	}
	return "", false
}

func (s *server) handleNet(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	// Chunked-serving part URLs (geojson.go manifest contract):
	// /net/{id}.geojson.{schema}.{hash12}.part-NNN serves one part of a
	// chunked network. Schema+hash pin the generation: an exporter or
	// scenario change between the client's manifest and part fetches 404s
	// the stale parts (the viz refetches the manifest) instead of mixing
	// two generations.
	if base, rest, ok := strings.Cut(file, ".geojson."); ok {
		schema, rest2, ok := strings.Cut(rest, ".")
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("not a part URL %q", file))
			return
		}
		hash, partStr, ok := strings.Cut(rest2, ".part-")
		if !ok || hash == "" {
			writeErr(w, http.StatusNotFound, fmt.Errorf("not a part URL %q", file))
			return
		}
		idx, err := strconv.Atoi(partStr)
		if err != nil || idx < 0 {
			writeErr(w, http.StatusNotFound, fmt.Errorf("not a part index %q", partStr))
			return
		}
		scenarioDir, ok := s.netScenarioDir(base)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Errorf("unknown demo or recording %q", base))
			return
		}
		path, err := s.nets.part(base, scenarioDir, schema, hash, idx)
		if err != nil {
			switch {
			case errors.Is(err, errStalePart):
				writeErr(w, http.StatusNotFound, err) // retryable: refetch the manifest
			case errors.Is(err, errNoPart):
				writeErr(w, http.StatusNotFound, err) // bad index: plain 404
			default:
				writeErr(w, http.StatusInternalServerError, err) // operational: not a stale generation
			}
			return
		}
		w.Header().Set("Content-Type", "application/geo+json")
		http.ServeFile(w, r, path)
		return
	}
	id, ok := strings.CutSuffix(file, ".geojson")
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not a network geojson path"))
		return
	}
	scenarioDir, ok := s.netScenarioDir(id)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown demo or recording %q", id))
		return
	}
	path, err := s.nets.path(id, scenarioDir)
	if err == nil && isManifest(path) {
		// Manifests must never come from a cache: a stale manifest points
		// at 404ing part generations (ADR-0018) — no-store beats no-cache
		// (304 revalidation on equal-second mtimes would resurrect it).
		// Single-file networks revalidate normally (they are huge).
		w.Header().Set("Cache-Control", "no-store")
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	http.ServeFile(w, r, path)
}

// handleOverlay serves a static overlay GeoJSON file (zones, boundaries —
// already WGS84, the viz adds them to MapLibre directly) from -overlaydir.
// Read-only and name-gated by the registry id charset: no suffix, a bad
// token, a missing directory, or a missing file are all a plain 404 —
// overlays are optional, the viz renders fine without them.
func (s *server) handleOverlay(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("file"), ".geojson")
	if !ok || !validID(name) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not an overlay geojson path"))
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	http.ServeFile(w, r, filepath.Join(s.overlays, name+".geojson"))
}
