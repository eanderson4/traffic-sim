// serve is the M6 live demo server: it runs a compiled network scenario
// (network-format v1, e.g. the i280 netimport bootstrap) live over NATS —
// engine + contract + record planes via natsio.RunLive — with the embedded
// broker's WebSocket listener up for browser clients (ADR-0006 §8:
// "browsers over the server's WebSocket listener with binary frames").
// The loop is paced at 1× wall time (ContractConfig.PaceFloor = one tick),
// and an in-process default driver (ADR-0008 §5) drives the fleet so the
// demo needs no external controller. -geojson additionally exports the
// network as a GeoJSON artifact for the viz client (engine/geojson.go —
// local metric frame + frame descriptor; the client projects to WGS84).
//
// Demo only: shutdown on SIGINT abandons the KV run entry (no graceful
// finish), and the recorder's JetStream store is a temp dir.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
	"traffic-sim/engine/natsio/driver"
)

func main() {
	netfile := flag.String("netfile", "", "compiled network JSON (network-format v1); required")
	wsAddr := flag.String("ws", "127.0.0.1:8443", "WebSocket listen address for browser clients (host:port)")
	run := flag.String("run", "demo", "run id (single NATS token)")
	ticks := flag.Uint64("ticks", 36000, "ticks to simulate (100 ms tick; 36000 = 1 h)")
	seed := flag.Uint64("seed", 1, "scenario seed")
	rate := flag.Float64("rate", 600, "spawn rate per origin lane (veh/h)")
	density := flag.Float64("density", 80, "density cap (veh/lane-km)")
	geojson := flag.String("geojson", "", "also write the network as GeoJSON (local metric frame) to this path")
	withDriver := flag.Bool("driver", true, "run an in-process default driver replica")
	capacity := flag.Int("capacity", 1000, "driver claim capacity")
	flag.Parse()

	if *netfile == "" {
		fmt.Fprintln(os.Stderr, "serve: -netfile is required")
		os.Exit(2)
	}
	host, portStr, err := net.SplitHostPort(*wsAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve: -ws:", err)
		os.Exit(2)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		fmt.Fprintf(os.Stderr, "serve: -ws: bad port %q\n", portStr)
		os.Exit(2)
	}

	// GeoJSON export first: fail loud on a bad network file before serving.
	if *geojson != "" {
		data, err := os.ReadFile(*netfile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
		var nf engine.NetFile
		if err := json.Unmarshal(data, &nf); err != nil {
			fmt.Fprintf(os.Stderr, "serve: netfile %s: %v\n", *netfile, err)
			os.Exit(1)
		}
		f, err := os.Create(*geojson)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: geojson: %v\n", err)
			os.Exit(1)
		}
		if err := engine.WriteGeoJSON(&nf, f); err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "serve: geojson: %v\n", err)
			os.Exit(1)
		}
		f.Close()
		fmt.Printf("serve: wrote %s (%d lanes, local metric frame — see file's \"frame\" member)\n",
			*geojson, len(nf.Lanes))
	}

	// Embedded broker (ADR-0006 §8 single-binary demo): no client-port
	// listener, WebSocket listener for the browser plane.
	storeDir, err := os.MkdirTemp("", "ts-serve-js")
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(storeDir)
	ns, err := server.NewServer(&server.Options{
		DontListen: true,
		JetStream:  true,
		StoreDir:   storeDir,
		Websocket:  server.WebsocketOpts{Host: host, Port: port, NoTLS: true},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve: nats-server:", err)
		os.Exit(1)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		fmt.Fprintln(os.Stderr, "serve: nats-server not ready")
		os.Exit(1)
	}
	defer ns.Shutdown()

	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("engine"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve: connect:", err)
		os.Exit(1)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve: JetStream:", err)
		os.Exit(1)
	}

	spec := engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: *netfile},
		Scen:   engine.Scenario{SpawnRatePerLaneHour: *rate, DensityTargetPerKm: *density},
		Params: engine.DefaultParams(),
		Seed:   *seed,
		Ticks:  *ticks,
	}
	runErr := make(chan error, 1)
	go func() {
		// PaceFloor = one tick of wall time: 1× realtime (ADR-0005 §4 — pacing
		// is a wrapper's business; the loop itself never blocks on input).
		_, err := natsio.RunLive(nc, js, *run, spec, natsio.RecorderConfig{},
			natsio.ContractConfig{PaceFloor: time.Duration(spec.Params.Dt * float64(time.Second))})
		runErr <- err
	}()

	if *withDriver {
		dnc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("default-driver"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: driver connect:", err)
			os.Exit(1)
		}
		defer dnc.Close()
		djs, err := dnc.JetStream()
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: driver JetStream:", err)
			os.Exit(1)
		}
		d, err := driver.New(dnc, djs, driver.Config{Run: *run, Capacity: *capacity})
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: driver:", err)
			os.Exit(1)
		}
		defer d.Close()
		fmt.Printf("serve: default driver attached as %s (capacity %d)\n", d.ID(), *capacity)
	}

	wsURL := fmt.Sprintf("ws://%s:%d", host, port)
	if host == "" || host == "0.0.0.0" {
		wsURL = fmt.Sprintf("ws://127.0.0.1:%d", port)
	}
	fmt.Printf("serve: run %q on %s (%d ticks @ 1× wall time)\n", *run, wsURL, *ticks)
	fmt.Printf("serve: snapshots on %s — point the viz at it (?run=%s&ws=%s)\n",
		natsio.SubjectStateSnap(*run), *run, wsURL)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Println("\nserve: interrupted (run abandoned; demo mode does no graceful finish)")
	case err := <-runErr:
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: run aborted:", err)
			os.Exit(1)
		}
		fmt.Println("serve: run complete")
	}
}
