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
	pace := flag.Float64("pace", 1, "wall-time pace multiplier: PaceFloor = dt/pace (1 = realtime, >1 = faster; 0 = unpaced batch mode — the attach barrier parks tick 0 until embedded clients are ready, so any pace is allowed with the driver/director attached)")
	store := flag.String("store", "", "durable JetStream store directory (created if missing, kept on exit, refuses to append into an existing recording of the same run id); default is a temp dir deleted on exit")
	withDriver := flag.Bool("driver", true, "run an in-process default driver replica")
	capacity := flag.Int("capacity", 1000, "driver claim capacity")
	exitRouting := flag.Bool("exit-routing", true, "driver assigns each claimed vehicle a seeded exit-lane destination (per-vehicle routing; without it vehicles take the kernel's leftmost-successor default)")
	attachTimeout := flag.Duration("attach-timeout", 30*time.Second, "bound on the client-attach barrier: serve fails if an embedded client (driver, demand director) has not reported attached within this")
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
	// time, today's behavior exactly. Any non-negative pace is allowed with
	// embedded clients attached: the client-attach barrier below parks the
	// run loop at tick 0 until the driver/demand director report ready, so
	// the early-tick loss that once motivated a pace cap cannot happen.
	// Pace remains an UNRECORDED run condition — see the note at the
	// barrier for exactly what is and isn't identical across paces.
	paceFloorDur, err := paceFloor(spec.Params.Dt, *pace)
	if err != nil {
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

	// Client-attach barrier: the run loop parks at tick 0 (contract plane
	// served, sim time frozen — natsio.ContractConfig.StartGate) until
	// every embedded client reports attached, so no early tick can outrun
	// an attaching client at any pace. Readiness signals: driver.New
	// returning (hello handshake answered, subscriptions flushed — the
	// replica is ready to claim from tick 0's unclaimed pool) and demand
	// Attach + Director.Ready (snapshot subscription live on the server).
	//
	// DETERMINISM NOTE — pace is an unrecorded run condition. What IS
	// identical across paces for a given (content hash, seed): the
	// director's verb DIRECTIVES — request ids, origins, vtypes, earliest
	// ticks are a pure function of (demand, seed) — and, with the 30-tick
	// lead, every steady-state verb is accepted before its earliest tick,
	// so those vehicles inject at the identical tick at any pace. What is
	// NOT identical: the acceptance tick recorded with each verb (its
	// wall-clock landing, ~1 tick later unpaced), the effect tick of the
	// program's head (verbs with earliest < the lead wait for the first
	// snapshot, so they land at the acceptance boundary), and the tick
	// individual claims/intents land on — client reaction latency is
	// wall-clock, guaranteed only while clients keep up with the loop: at
	// any finite pace the floor gives them dt/pace per tick (ample for
	// in-process replicas); at -pace 0 the only margin is the record
	// plane's per-tick puback, so an unpaced run's alignment is
	// scheduler-dependent in principle. At every pace, whatever the live
	// run applied is recorded verbatim and replays bit-identically (both
	// directions pinned in natsio's startgate test).
	startGate := make(chan struct{})
	runErr := make(chan error, 1)
	go func() {
		// PaceFloor = one tick of wall time at -pace 1 (1× realtime); -pace N
		// divides it by N, -pace 0 disables pacing entirely (batch mode).
		cc := natsio.ContractConfig{PaceFloor: paceFloorDur, StartGate: startGate}
		if mobs != nil {
			cc.Observer = mobs
		}
		lr, err := natsio.RunLive(nc, js, *run, spec, natsio.RecorderConfig{}, cc)
		if err == nil && mobs != nil {
			err = mobs.finish(lr.Engine, *metricsOut, spec.Ticks)
		}
		runErr <- err
	}()

	barrier := make(chan attachOutcome, 2)
	var expected []string
	if *withDriver {
		expected = append(expected, "default driver")
		go func() {
			barrier <- attachDriver(ns, *run, *capacity, *exitRouting, *attachTimeout)
		}()
	}
	// Scenario demand parts run through the embedded reference demand
	// director (engine/natsio/demand): sampled with the RUN seed, so the
	// recorded (content-hash, seed) run key covers the demand realization
	// and a same-seed rerun re-issues the identical verb program.
	if scen != nil && len(scen.Demands) > 0 {
		expected = append(expected, "demand director")
		go func() {
			barrier <- attachDirector(ns, *run, spec.Seed, scen.Demands)
		}()
	}
	attached, err := waitBarrier(barrier, expected, runErr, *attachTimeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	for _, out := range attached {
		defer out.cleanup()
		fmt.Printf("serve: %s attached (%s)\n", out.client, out.desc)
	}
	close(startGate)

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

// attachOutcome is one embedded client's report to the attach barrier.
type attachOutcome struct {
	client  string // barrier identity ("default driver", "demand director")
	desc    string // success-line detail
	cleanup func() // deferred Close calls; nil on failure
	err     error
}

// attachDriver connects and attaches the in-process default driver replica.
// driver.New returning IS the readiness signal: the hello handshake has
// been answered and New's final Flush guarantees the server has processed
// the observation/snapshot/unclaimed subscriptions, so tick 0's unclaimed
// pool reaches this replica.
func attachDriver(ns *server.Server, run string, capacity int, exitRouting bool, metaWait time.Duration) attachOutcome {
	dnc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("default-driver"))
	if err != nil {
		return attachOutcome{client: "default driver", err: err}
	}
	djs, err := dnc.JetStream()
	if err != nil {
		dnc.Close()
		return attachOutcome{client: "default driver", err: err}
	}
	d, err := driver.New(dnc, djs, driver.Config{Run: run, Capacity: capacity, ExitRouting: exitRouting, MetaWait: metaWait})
	if err != nil {
		dnc.Close()
		return attachOutcome{client: "default driver", err: err}
	}
	return attachOutcome{
		client:  "default driver",
		desc:    fmt.Sprintf("id %s, capacity %d", d.ID(), capacity),
		cleanup: func() { d.Close(); dnc.Close() },
	}
}

// attachDirector connects and attaches the embedded demand director, then
// waits for its snapshot subscription to be live on the server
// (Director.Ready) so no early snapshot — tick 0's included — slips past
// the verb loop.
func attachDirector(ns *server.Server, run string, seed uint64, dfs []*scenario.DemandFile) attachOutcome {
	dnc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("demand-director"))
	if err != nil {
		return attachOutcome{client: "demand director", err: err}
	}
	djs, err := dnc.JetStream()
	if err != nil {
		dnc.Close()
		return attachOutcome{client: "demand director", err: err}
	}
	lg := log.New(os.Stderr, "", 0)
	dd, err := demand.Attach(dnc, djs, demand.Config{Run: run, Seed: seed, Log: lg}, dfs)
	if err != nil {
		dnc.Close()
		return attachOutcome{client: "demand director", err: err}
	}
	if err := dd.Ready(); err != nil {
		dd.Close()
		dnc.Close()
		return attachOutcome{client: "demand director", err: err}
	}
	return attachOutcome{
		client:  "demand director",
		desc:    fmt.Sprintf("%d demand file(s), run-seeded", len(dfs)),
		cleanup: func() { dd.Close(); dnc.Close() },
	}
}

// waitBarrier collects one attach report per expected client until all have
// reported, the run dies, or the timeout fires. Failures are fatal before
// tick 0 (never a degraded run), and the error names the client that failed
// — or those still pending at timeout / run death. A failed wait leaks the
// still-attaching goroutines and the parked run loop on purpose: serve
// exits right after, and abandoning mid-attach clients beats second-guessing
// their connection state.
func waitBarrier(barrier <-chan attachOutcome, expected []string, runErr <-chan error, timeout time.Duration) ([]attachOutcome, error) {
	pending := append([]string(nil), expected...)
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var attached []attachOutcome
	for len(pending) > 0 {
		select {
		case out := <-barrier:
			if out.err != nil {
				return nil, fmt.Errorf("%s failed to attach: %w", out.client, out.err)
			}
			found := false
			for i, p := range pending {
				if p == out.client {
					pending = append(pending[:i], pending[i+1:]...)
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("unexpected attach report from %q", out.client)
			}
			attached = append(attached, out)
		case err := <-runErr:
			if err == nil {
				return nil, fmt.Errorf("run finished before the embedded clients attached; still waiting for: %s",
					strings.Join(pending, ", "))
			}
			return nil, fmt.Errorf("run aborted before the embedded clients attached (%v); still waiting for: %s",
				err, strings.Join(pending, ", "))
		case <-timer.C:
			return nil, fmt.Errorf("attach timeout (%s) — still waiting for: %s",
				timeout, strings.Join(pending, ", "))
		}
	}
	return attached, nil
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
