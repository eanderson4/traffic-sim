// replay is the VCR driver for recorded runs: it re-simulates a run from
// its durable JetStream record plane (written earlier by serve -store DIR)
// and PUBLISHES the live plane — snapshots and the signal table under the
// fresh run id {run}-replay — at a configurable pace, with an HTTP control
// plane (pause/resume/speed/seek) for the demo UI. The MapLibre viz
// subscribes ts.{run}-replay.state.snap / ts.{run}-replay.state.sig over
// the broker's WebSocket listener and renders whatever arrives.
//
// STORE EXCLUSIVITY: exactly one broker may open a JetStream store dir at
// a time (no locking exists). The serve that recorded the run must have
// EXITED before replay starts — demosrv enforces this by kill-before-spawn.
//
// CRC divergence policy: log loudly and continue (counted in /status) — a
// demo must not die on air; natsio.ReplayFromStream remains the strict
// audit path. At the end of the recording the player holds position: it
// stays up, paused at the final tick, still serving /status and /seek.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"traffic-sim/engine/natsio"
)

func main() {
	store := flag.String("store", "", "durable JetStream store dir written by serve -store (required); the recording serve must have exited first — one broker per store dir")
	run := flag.String("run", "", "recorded run id to replay (required); the live plane is published under {run}-replay")
	speed := flag.Float64("speed", 1, "playback pace multiplier (1 = realtime; snapshots decimate toward ~10 Hz wall at any pace, honoring the recorded run's dt)")
	wsAddr := flag.String("ws", "127.0.0.1:8443", "WebSocket listen address for browser clients (host:port)")
	httpAddr := flag.String("http", "127.0.0.1:8901", "HTTP control-plane listen address (host:port; loopback only — the controls are unauthenticated)")
	flag.Parse()

	if *store == "" {
		fmt.Fprintln(os.Stderr, "replay: -store is required")
		os.Exit(2)
	}
	if *run == "" {
		fmt.Fprintln(os.Stderr, "replay: -run is required")
		os.Exit(2)
	}
	host, portStr, err := net.SplitHostPort(*wsAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay: -ws:", err)
		os.Exit(2)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		fmt.Fprintf(os.Stderr, "replay: -ws: bad port %q\n", portStr)
		os.Exit(2)
	}
	if err := checkLoopback(*httpAddr); err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(2)
	}

	// Embedded broker on the recorded store (ADR-0006 §8 single-binary
	// demo): no client-port listener, WebSocket listener for the browser
	// plane. The recording serve must have exited — the store is
	// single-server.
	ns, err := server.NewServer(&server.Options{
		DontListen: true,
		JetStream:  true,
		StoreDir:   *store,
		Websocket:  server.WebsocketOpts{Host: host, Port: port, NoTLS: true},
		// 4 MB, same as serve (ADR-0016): TSSG is chunked at ~768 KiB per
		// message; this is headroom for big-fleet TSSF snapshots, not a
		// design allowance.
		MaxPayload: 4 << 20,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay: nats-server:", err)
		os.Exit(1)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Fprintln(os.Stderr, "replay: nats-server not ready")
		os.Exit(1)
	}
	defer ns.Shutdown()

	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("replay"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay: connect:", err)
		os.Exit(1)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay: JetStream:", err)
		os.Exit(1)
	}

	player, err := natsio.NewPlayer(nc, js, natsio.PlayerConfig{Run: *run, Speed: *speed})
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay:", err)
		os.Exit(1)
	}

	// Bind the control listener synchronously BEFORE starting playback: a
	// replay without its control plane is a demo waiting to go wrong, so a
	// bind failure is fatal here, not a log line after the fact.
	ln, err := net.Listen("tcp", *httpAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replay: http control plane listen:", err)
		os.Exit(1)
	}
	httpSrv := &http.Server{
		Handler:           player.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "replay: http control plane:", err)
		}
	}()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- player.Run() }()

	wsHost := host
	if host == "" || host == "0.0.0.0" {
		wsHost = "127.0.0.1"
	}
	wsURL := "ws://" + net.JoinHostPort(wsHost, strconv.Itoa(port))
	st := player.Status()
	fmt.Printf("replay: run %q → %q (%d ticks @ %gx)\n", st.Run, st.ReplayRun, st.Ticks, st.Speed)
	fmt.Printf("replay: snapshots on %s — point the viz at it (?run=%s&ws=%s&dt=%g)\n",
		natsio.SubjectStateSnap(st.ReplayRun), st.ReplayRun, wsURL, st.Dt)
	fmt.Printf("replay: control plane on http://%s (POST /pause /resume /speed /seek, GET /status)\n", *httpAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	var runErr error
	select {
	case <-sig:
		fmt.Println("\nreplay: interrupted")
		player.Stop()
		runErr = <-runErrCh
	case runErr = <-runErrCh: // the player stopped on its own
	}
	_ = httpSrv.Close()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "replay: player:", runErr)
		os.Exit(1)
	}
}

// checkLoopback refuses non-loopback control-plane bind addresses: the HTTP
// controls are unauthenticated — the ADR-0002 carve-out for them is argued
// on loopback — so binding to a routable address would expose replay
// control to the network (ADR-0002 addendum 2026-07-23).
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-http: %w", err)
	}
	if host == "" {
		return fmt.Errorf("-http: empty host binds ALL interfaces — the unauthenticated control plane must stay on loopback (ADR-0002 addendum 2026-07-23)")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-http: %q is not a loopback address — the unauthenticated control plane must stay on loopback (ADR-0002 addendum 2026-07-23)", host)
	}
	return nil
}
