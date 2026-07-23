// demosrv is the local demo launcher: ONE Go process that serves the demo
// menu page and the built viz (viz/dist), and spawns/kills `serve` engine
// processes on demand. It is localhost process orchestration in the spirit
// of `pnpm dev` — plain HTTP to the browser, NO NATS here (ADR-0002 covers
// the service planes; the demo's engine child still exposes its own
// WebSocket listener for the live snapshot stream, ADR-0006 §8 unchanged).
// Standard library only, plus the engine packages themselves.
//
// Single-active-run: starting a demo SIGTERMs the previous engine (SIGKILL
// after 2 s) — one engine at a time on the fixed WebSocket port
// 127.0.0.1:8443, which is also the viz client's default ?ws= target.
//
// Paths — the registry's scenarioDir values and the -demos/-viz/-netcache
// defaults — are REPO-ROOT-relative: run demosrv from the repository root
// (go run ./engine/cmd/demosrv), or pass explicit flags. The serve binary
// is built ONCE at startup (pre-warm: the first demo start is then a
// process exec, not a cold compile, and there is no `go run` wrapper
// between demosrv and the engine to eat signals). SIGINT/SIGTERM to
// demosrv kills the active child too.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
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
	vizDir := flag.String("viz", "viz/dist", "built viz to serve (cd viz && pnpm build): demos.html (menu) + index.html (map app)")
	netcacheDir := flag.String("netcache", "data/networks/.geojson-cache", "per-demo network GeoJSON cache")
	flag.Parse()

	reg, err := LoadRegistry(*demosPath)
	if err != nil {
		log.Fatalf("demosrv: %v", err)
	}
	if err := os.MkdirAll(*netcacheDir, 0o755); err != nil {
		log.Fatalf("demosrv: netcache: %v", err)
	}
	engineDir, err := findEngineDir()
	if err != nil {
		log.Fatalf("demosrv: %v", err)
	}

	// Pre-warm: build serve once. Startup cost is ~1 s on a warm build
	// cache; every demo start after that is an exec of THIS binary, so
	// SIGTERM/SIGKILL reach the engine directly.
	binDir, err := os.MkdirTemp("", "traffic-sim-demosrv-")
	if err != nil {
		log.Fatalf("demosrv: %v", err)
	}
	defer os.RemoveAll(binDir)
	serveBin := filepath.Join(binDir, "serve")
	log.Printf("demosrv: building serve (go build ./cmd/serve in %s/)", engineDir)
	build := exec.Command("go", "build", "-o", serveBin, "./cmd/serve")
	build.Dir = engineDir
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		log.Fatalf("demosrv: go build ./cmd/serve: %v", err)
	}

	lg := log.New(os.Stderr, "", log.LstdFlags)
	sup := newSupervisor(serveSpawner(serveBin, lg))
	srv := &server{reg: reg, sup: sup, viz: *vizDir, nets: &netCache{dir: *netcacheDir}}
	hs := &http.Server{Addr: *addr, Handler: srv.routes()}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		s := <-sig
		log.Printf("demosrv: %v — killing the active run and exiting", s)
		sup.stop()
		hs.Close()
	}()

	if _, err := os.Stat(filepath.Join(*vizDir, "demos.html")); err != nil {
		log.Printf("demosrv: warning: %s/demos.html not found — build the viz first (cd viz && pnpm build)", *vizDir)
	}
	log.Printf("demosrv: %d demo(s) on http://%s/ — single active run, engine ws %s", len(reg.Demos), *addr, wsListenAddr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// serveSpawner is the real spawnFunc: exec the prebuilt serve binary with
// the demo's scenario/run (+ optional seed/ticks overrides), output teed
// into demosrv's log with a [id] prefix so child lines stay attributable.
func serveSpawner(bin string, lg *log.Logger) spawnFunc {
	return func(d *Demo) (*exec.Cmd, error) {
		args := []string{"-scenario", d.ScenarioDir, "-run", d.Run, "-ws", wsListenAddr}
		if d.Seed != nil {
			args = append(args, "-seed", strconv.FormatUint(*d.Seed, 10))
		}
		if d.Ticks != nil {
			args = append(args, "-ticks", strconv.FormatUint(*d.Ticks, 10))
		}
		if d.Capacity != nil {
			args = append(args, "-capacity", strconv.FormatUint(*d.Capacity, 10))
		}
		cmd := exec.Command(bin, args...)
		w := &prefixWriter{lg: lg, prefix: "[" + d.ID + "] "}
		cmd.Stdout = w
		cmd.Stderr = w
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
}

// prefixWriter tees child output into demosrv's log line-by-line with a
// [id] prefix. A trailing partial line (no \n before exit) is dropped —
// cosmetic only.
type prefixWriter struct {
	lg     *log.Logger
	prefix string
	mu     sync.Mutex
	buf    []byte
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
		w.lg.Printf("%s%s", w.prefix, w.buf[:i])
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

type server struct {
	reg  *Registry
	sup  *supervisor
	viz  string
	nets *netCache
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleMenu)
	mux.HandleFunc("GET /app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /app/", s.handleApp)
	mux.HandleFunc("GET /api/demos", s.handleDemos)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("POST /api/demo/{id}/start", s.handleStart)
	mux.HandleFunc("GET /api/demo/{id}/params", s.handleParams)
	mux.HandleFunc("POST /api/demo/stop", s.handleStop)
	// Go's ServeMux wildcards are whole segments, so the .geojson suffix is
	// parsed out of {file} in the handler.
	mux.HandleFunc("GET /net/{file}", s.handleNet)
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

// handleMenu serves the built menu page (viz/demos.html → dist/demos.html).
func (s *server) handleMenu(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(s.viz, "demos.html"))
}

// handleApp serves the built map app; the run/network selection rides in
// the query string (?run=&net= — viz/src/config.ts), so one built page
// serves every demo.
func (s *server) handleApp(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, filepath.Join(s.viz, "index.html"))
}

func (s *server) handleDemos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.reg)
}

type statusResponse struct {
	Active    *string `json:"active"`
	PID       int     `json:"pid"`
	StartedAt string  `json:"startedAt,omitempty"`
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	id, pid, startedAt, ok := s.sup.status()
	resp := statusResponse{}
	if ok {
		resp.Active = &id
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
	if err := s.sup.start(d); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// Same shape as the menu page's pure buildAppURL (viz/src/demos-core.ts)
	// — they must agree, the running-card deep link depends on it.
	writeJSON(w, http.StatusOK, map[string]string{
		"url": fmt.Sprintf("/app/?run=%s&net=/net/%s.geojson", d.Run, d.ID),
	})
}

func (s *server) handleStop(w http.ResponseWriter, r *http.Request) {
	s.sup.stop()
	writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
}

func (s *server) handleNet(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("file"), ".geojson")
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("not a network geojson path"))
		return
	}
	d := s.reg.byID(id)
	if d == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown demo %q", id))
		return
	}
	path, err := s.nets.path(d)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/geo+json")
	http.ServeFile(w, r, path)
}
