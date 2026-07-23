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
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
	"traffic-sim/engine/natsio/demand"
	"traffic-sim/engine/natsio/driver"
	"traffic-sim/engine/scenario"
)

func main() {
	netfile := flag.String("netfile", "", "compiled network JSON (network-format v1); required unless -scenario")
	scenarioDir := flag.String("scenario", "", "ADR-0012 scenario directory (supplies network, demand, types, seed, ticks); explicit -seed/-ticks override the manifest")
	wsAddr := flag.String("ws", "127.0.0.1:8443", "WebSocket listen address for browser clients (host:port)")
	run := flag.String("run", "demo", "run id (single NATS token)")
	ticks := flag.Uint64("ticks", 36000, "ticks to simulate (100 ms tick; 36000 = 1 h)")
	seed := flag.Uint64("seed", 1, "scenario seed")
	rate := flag.Float64("rate", 600, "spawn rate per origin lane (veh/h); 0 disables the built-in spawner (director-driven runs)")
	density := flag.Float64("density", 80, "density cap (veh/lane-km)")
	types := flag.String("types", "car", "comma-separated vehicle-type names for the scenario type list (car,truck); director spawn verbs resolve against this list")
	geojson := flag.String("geojson", "", "also write the network as GeoJSON (local metric frame) to this path")
	metricsOut := flag.String("metrics-out", "", "write M13 metric-kernel output (ADR-0014 §6) as JSON to this path at run end")
	withDriver := flag.Bool("driver", true, "run an in-process default driver replica")
	capacity := flag.Int("capacity", 1000, "driver claim capacity")
	flag.Parse()

	if *scenarioDir != "" && *netfile != "" {
		fmt.Fprintln(os.Stderr, "serve: -scenario and -netfile are mutually exclusive (the scenario names its network)")
		os.Exit(2)
	}
	if *scenarioDir == "" && *netfile == "" {
		fmt.Fprintln(os.Stderr, "serve: -scenario or -netfile is required")
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

	// The scenario type list is what director spawn verbs resolve vtype
	// against; the default ("car") is byte-identical to the pre-flag
	// behavior (NewEngine's [Car] default).
	typeReg := map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}

	// Build the run spec: an ADR-0012 scenario directory, or the flag-era
	// surface (which is a generated-default scenario conceptually).
	var spec engine.RunSpec
	var scen *scenario.Scenario
	if *scenarioDir != "" {
		// The scenario owns network, demand, and the type list; the
		// deliberate sweep overrides (-seed/-ticks) are the only flags
		// allowed beside it.
		conflicting := []string{}
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "rate", "density", "types":
				conflicting = append(conflicting, "-"+f.Name)
			}
		})
		if len(conflicting) > 0 {
			fmt.Fprintf(os.Stderr, "serve: %s are scenario-owned when -scenario is set (only -seed/-ticks override)\n",
				strings.Join(conflicting, ", "))
			os.Exit(2)
		}
		sc, err := scenario.Load(*scenarioDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
		scen = sc
		spec, err = sc.RunSpec(typeReg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
		// Explicit -seed/-ticks override the manifest (seed sweeps derive
		// runs; the content hash is unchanged — ADR-0012 §6).
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "seed":
				spec.Seed = *seed
			case "ticks":
				spec.Ticks = *ticks
			}
		})
		fmt.Printf("serve: scenario %s (hash %s)\n", sc.Manifest.ID, sc.Hash())
	} else {
		var typeList []*engine.VehicleType
		for _, name := range strings.Split(*types, ",") {
			t, ok := typeReg[strings.TrimSpace(name)]
			if !ok {
				fmt.Fprintf(os.Stderr, "serve: unknown vehicle type %q (known: car, truck)\n", name)
				os.Exit(2)
			}
			typeList = append(typeList, t)
		}
		spec = engine.RunSpec{
			Net:    engine.NetSpec{Kind: "file", Path: *netfile},
			Scen:   engine.Scenario{SpawnRatePerLaneHour: *rate, DensityTargetPerKm: *density, Types: typeList},
			Params: engine.DefaultParams(),
			Seed:   *seed,
			Ticks:  *ticks,
		}
	}

	// GeoJSON export first: fail loud on a bad network file before serving.
	if *geojson != "" {
		data, err := os.ReadFile(spec.Net.Path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
		var nf engine.NetFile
		if err := json.Unmarshal(data, &nf); err != nil {
			fmt.Fprintf(os.Stderr, "serve: netfile %s: %v\n", spec.Net.Path, err)
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

	// Metric kernel (ADR-0014 §6 file sink): a read-only observer on the run
	// loop — the run stays bit-identical (CRC-invariance tested in
	// engine/metrics_test.go). The config comes from the scenario's metrics
	// parts when -scenario is in use, else the zero-authoring default.
	var mobs *metricObserver
	if *metricsOut != "" {
		mnet, err := engine.BuildNet(spec.Net)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: metrics:", err)
			os.Exit(1)
		}
		var cfg engine.KernelConfig
		if scen != nil {
			cfg = scen.MetricConfig(mnet)
		} else {
			cfg = engine.DefaultKernelConfig(mnet, spec.Params.Dt)
		}
		mobs = &metricObserver{cfg: cfg}
	}

	runErr := make(chan error, 1)
	go func() {
		// PaceFloor = one tick of wall time: 1× realtime (ADR-0005 §4 — pacing
		// is a wrapper's business; the loop itself never blocks on input).
		cc := natsio.ContractConfig{PaceFloor: time.Duration(spec.Params.Dt * float64(time.Second))}
		if mobs != nil {
			cc.Observer = mobs
		}
		lr, err := natsio.RunLive(nc, js, *run, spec, natsio.RecorderConfig{}, cc)
		if err == nil && mobs != nil {
			err = mobs.finish(lr.Engine, *metricsOut, spec.Ticks)
		}
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

	// Scenario demand parts run through the embedded reference demand
	// director (engine/natsio/demand): sampled with the RUN seed, so the
	// recorded (content-hash, seed) run key covers the demand realization
	// and a same-seed rerun re-issues the identical verb program.
	if scen != nil && len(scen.Demands) > 0 {
		dnc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("demand-director"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: demand director connect:", err)
			os.Exit(1)
		}
		defer dnc.Close()
		djs, err := dnc.JetStream()
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: demand director JetStream:", err)
			os.Exit(1)
		}
		lg := log.New(os.Stderr, "", 0)
		dd, err := demand.Attach(dnc, djs, demand.Config{Run: *run, Seed: spec.Seed, Log: lg}, scen.Demands)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve: demand director:", err)
			os.Exit(1)
		}
		defer dd.Close()
		fmt.Printf("serve: demand director attached (%d demand file(s), run-seeded)\n", len(scen.Demands))
	}

	wsURL := fmt.Sprintf("ws://%s:%d", host, port)
	if host == "" || host == "0.0.0.0" {
		wsURL = fmt.Sprintf("ws://127.0.0.1:%d", port)
	}
	fmt.Printf("serve: run %q on %s (%d ticks @ 1× wall time)\n", *run, wsURL, spec.Ticks)
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
		if mobs != nil {
			fmt.Printf("serve: wrote metrics to %s\n", *metricsOut)
		}
	}
}

// metricObserver adapts the M13 metric kernel to natsio.RunObserver: the
// kernel attaches at tick 0 (Attach, called by RunLive right after engine
// construction) and observes every Step. The kernel is a read-only
// observer (ADR-0014 §1) — it never feeds back into the run.
type metricObserver struct {
	cfg engine.KernelConfig
	k   *engine.Kernel
}

func (m *metricObserver) Attach(e *engine.Engine) error {
	k, err := engine.NewKernel(e, m.cfg)
	if err != nil {
		return err
	}
	m.k = k
	return nil
}

func (m *metricObserver) Observe(e *engine.Engine) { m.k.Observe(e) }

// finish closes the kernel at the horizon and writes the metrics JSON
// atomically (temp + rename — the simrun pattern). Horizon-only: the
// SIGINT path abandons the run (demo mode does no graceful finish), so no
// partial artifact is written there either.
func (m *metricObserver) finish(e *engine.Engine, path string, ticks uint64) error {
	m.k.Finalize(e)
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := engine.WriteMetricsJSON(f, m.k, ticks); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
