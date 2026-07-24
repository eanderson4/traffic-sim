// serve is the M6 live demo server: it runs a compiled network scenario
// (network-format v1, e.g. the i280 netimport bootstrap) live over NATS —
// engine + contract + record planes via natsio.RunLive — with the embedded
// broker's WebSocket listener up for browser clients (ADR-0006 §8:
// "browsers over the server's WebSocket listener with binary frames").
// The loop is paced at 1× wall time by default (ContractConfig.PaceFloor =
// one tick); -pace N multiplies that (N>1 = faster than wall time, 0 =
// unpaced batch mode for fast recording). An in-process default driver
// (ADR-0008 §5) drives the fleet so the demo needs no external controller.
// -geojson additionally exports the network as a GeoJSON artifact for the
// viz client (engine/geojson.go — local metric frame + frame descriptor;
// the client projects to WGS84).
//
// Demo only: shutdown on SIGINT abandons the KV run entry (no graceful
// finish). The recorder's JetStream store is a temp dir deleted on exit
// unless -store names a durable directory (kept on exit, for replay).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"path/filepath"
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
	pace := flag.Float64("pace", 1, "wall-time pace multiplier: PaceFloor = dt/pace (1 = realtime, >1 = faster; 0 = unpaced batch mode, driverless runs only; >10 refused while driver/demand clients are attached)")
	store := flag.String("store", "", "durable JetStream store directory (created if missing, kept on exit, refuses to append into an existing recording of the same run id); default is a temp dir deleted on exit")
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

	// Absolutize the network path: the run meta (registry + durable record)
	// carries spec.Net.Path verbatim, and a relative path makes the recording
	// unreadable from any other working directory (the replay child hit this:
	// recorded from engine/, replayed from the repo root). Fail loud on
	// error rather than silently keep the relative path — filepath.Abs
	// essentially never fails, so a failure here is exactly the case to not
	// paper over.
	abs, err := filepath.Abs(spec.Net.Path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: network path %s: %v\n", spec.Net.Path, err)
		os.Exit(2)
	}
	spec.Net.Path = abs

	// Pacing (ADR-0005 §4 — pacing is a wrapper's business; the loop itself
	// never blocks on input): default 1 keeps PaceFloor = one tick of wall
	// time, today's behavior exactly.
	paceFloorDur, err := paceFloor(spec.Params.Dt, *pace)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(2)
	}
	if paceFloorDur == 0 {
		// Unpaced mode outruns every asynchronous NATS client: the in-process
		// driver replica AND the demand director both attach after the run
		// loop starts, so an unpaced run can finish before either attaches —
		// an empty or scheduler-dependent recording. Refuse the combination.
		switch {
		case *withDriver:
			fmt.Fprintln(os.Stderr, "serve: -pace 0 (unpaced) outruns the in-process driver replica — rerun with -driver=false or a high finite -pace")
			os.Exit(2)
		case scen != nil && len(scen.Demands) > 0:
			fmt.Fprintln(os.Stderr, "serve: -pace 0 (unpaced) outruns the demand director — use a high finite -pace for demand scenarios")
			os.Exit(2)
		}
	}
	if err := checkPaceClients(*pace, *withDriver, scen != nil && len(scen.Demands) > 0); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(2)
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
	storeDir, cleanupStore, err := jetStreamStoreDir(*store)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	defer cleanupStore()
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

	// A durable store survives serve exits, so a rerun with the same run id
	// would otherwise APPEND a second run's log into the recording the store
	// exists to preserve (the recorder adopts an existing stream). Refuse
	// before RunLive touches the registry. Must stay ahead of any future
	// append/resume semantics — today's replay expects one run per stream.
	if *store != "" {
		if err := checkFreshRecording(js, *run); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
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
		// PaceFloor = one tick of wall time at -pace 1 (1× realtime); -pace N
		// divides it by N, -pace 0 disables pacing entirely (batch mode).
		cc := natsio.ContractConfig{PaceFloor: paceFloorDur}
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
	paceDesc := fmt.Sprintf("%g× wall time", *pace)
	if paceFloorDur == 0 {
		paceDesc = "unpaced (batch mode)"
	}
	fmt.Printf("serve: run %q on %s (%d ticks @ %s)\n", *run, wsURL, spec.Ticks, paceDesc)
	fmt.Printf("serve: snapshots on %s — point the viz at it (?run=%s&ws=%s&dt=%g)\n",
		natsio.SubjectStateSnap(*run), *run, wsURL, spec.Params.Dt)

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

// paceFloor maps the -pace multiplier to the run loop's PaceFloor: dt/pace
// of wall time per tick (1 = one tick = realtime, >1 = faster), 0 = unpaced
// (the run loop skips the sleep when the floor is zero). Negative, NaN, and
// Inf are usage errors, as is a floor that overflows time.Duration.
func paceFloor(dt, pace float64) (time.Duration, error) {
	if math.IsNaN(pace) || math.IsInf(pace, 0) || pace < 0 {
		return 0, fmt.Errorf("-pace must be a finite value >= 0 (got %g)", pace)
	}
	if pace == 0 {
		return 0, nil
	}
	v := dt / pace * float64(time.Second)
	// 2^63 as float64 (== float64(math.MaxInt64)) is already out of range for
	// the conversion, and a positive floor that truncates below 1 ns would be
	// silently unpaced — both are usage errors, not batch mode.
	if math.IsInf(v, 0) || v < 1 || v >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("-pace %g puts the tick floor outside the representable range", pace)
	}
	return time.Duration(v), nil
}

// maxClientPace bounds the pace multiplier while asynchronous NATS clients
// (the in-process driver replica, the demand director) are attached. The run
// loop starts before they finish attaching, and client reaction latency in
// TICKS scales with pace: at 10× a ~100 ms attach costs ≤10 early ticks (the
// network is still near-empty — harmless); beyond that the head of the
// recording is increasingly under-controlled hold-last behavior. The cap
// keeps "fast recording" honest; batch mode (-pace 0) remains available for
// driverless runs only. NOTE: pace is an unrecorded run condition — the same
// (scenario, seed) at different paces produces different traffic (client
// latency scales in ticks), so cross-pace metric comparisons are invalid.
const maxClientPace = 10

// checkPaceClients enforces maxClientPace when driver or demand clients are
// attached (see the const's comment).
func checkPaceClients(pace float64, driver, demand bool) error {
	if pace > maxClientPace && (driver || demand) {
		return fmt.Errorf("-pace %g exceeds %g, the safe bound with an attached driver/demand director (their attach and reaction latency scales in ticks) — use -driver=false and no demand parts for faster batch runs", pace, float64(maxClientPace))
	}
	return nil
}

// checkFreshRecording refuses to start a run whose recording stream already
// exists non-empty in a durable store: the recorder ADOPTS an existing
// stream, so a rerun would append a second run's log after the first and
// replay would read interleaved history from two runs. Not-found is the
// happy path; any other lookup error also fails loud (fail-closed).
func checkFreshRecording(js nats.JetStreamContext, run string) error {
	info, err := js.StreamInfo(natsio.StreamName(run))
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return nil
		}
		return fmt.Errorf("-store: cannot inspect recording stream for run %q: %w", run, err)
	}
	if info.State.Msgs > 0 {
		return fmt.Errorf("-store: run %q already has a recording (%d messages) in this store — pick a fresh -run id or a fresh -store dir", run, info.State.Msgs)
	}
	return nil
}

// jetStreamStoreDir resolves the embedded broker's JetStream store
// directory. Empty dir keeps the demo behavior: a fresh temp dir removed on
// exit (cleanup removes it). A named dir is created if missing and kept on
// exit (cleanup is a no-op) so run recordings survive for replay. The store
// is single-server: two brokers on the same dir corrupt it.
func jetStreamStoreDir(dir string) (path string, cleanup func(), err error) {
	if dir == "" {
		tmp, err := os.MkdirTemp("", "ts-serve-js")
		if err != nil {
			return "", nil, err
		}
		return tmp, func() { os.RemoveAll(tmp) }, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("-store: %w", err)
	}
	return dir, func() {}, nil
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
