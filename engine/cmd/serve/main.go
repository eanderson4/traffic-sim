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
	"sort"
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
	"traffic-sim/engine/natsio/sigctl"
	"traffic-sim/engine/scenario"
)

// demandDeliveryWarn is the delivery fraction below which a run is called
// out as void rather than reported as a result. Set at 95%: ordinary peak
// congestion blocks a few percent of injections at saturated origins, which
// is real physics, while the failure this guards against lost 84%.
const demandDeliveryWarn = 0.95

// natsMaxPayload is the embedded broker's per-message cap. Named because
// two places depend on it: the server option below, and the end-of-run
// diagnosis that derives the observation-frame ego ceiling from it.
const natsMaxPayload = 4 << 20

// coastWarn is the share of vehicle-ticks running with no controller intent
// above which the run's physics are the controller's latency, not the
// network's capacity. Deliberately low: hold-last already covers ordinary
// message loss, so anything past a fraction of a percent is a controller
// that stopped keeping up rather than a dropped packet.
const coastWarn = 0.001

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
	withSigctl := flag.Bool("sigctl", false, "run the in-process reference actuated signal controller (engine/natsio/sigctl, ADR-0037 milestone 2): gap-out actuation over the signal_set verb channel; requires a network file with signal programs and centerline shapes")
	sigctlMinQueue := flag.Int("sigctl-min-queue", 1, "with -sigctl: minimum waiting vehicles on a candidate phase's detectors before the controller switches to it (2+ keeps stragglers from interrupting the major movement at light load)")
	capacity := flag.Int("capacity", 1000, "driver claim capacity (total across all replicas)")
	drivers := flag.Int("drivers", 1,
		"number of in-process default-driver replicas to shard the fleet across. "+
			"One replica computes every claimed vehicle's intent in a single "+
			"goroutine per observation, which is a hard throughput ceiling: on "+
			"chi-loop-urban at ~12,000 vehicles 35.75% of vehicle-ticks ran with "+
			"NO controller intent because the driver could not keep up. Claims are "+
			"exclusive and engine-arbitrated, so replicas shard the fleet safely.")
	exitRouting := flag.Bool("exit-routing", true, "driver assigns each claimed vehicle a seeded exit-lane destination (per-vehicle routing; without it vehicles take the kernel's leftmost-successor default)")
	intentBatch := flag.String("intent-batch", "on", "driver aggregates each tick's intents into TSIB batches (on|off; off restores the pre-ADR-0026 per-vehicle v2 stream, for A/B measurement and debugging)")
	attachTimeout := flag.Duration("attach-timeout", 30*time.Second, "bound on the client-attach barrier: serve fails if an embedded client (driver, demand director) has not reported attached within this")
	safetyDecel := flag.Float64("safety-decel", 6,
		"longitudinal safety gate: max emergency deceleration (m/s²) the kernel "+
			"applies to keep any vehicle out of its leader, capping every control "+
			"path the way the right-of-way gate does; 0 disables it")
	adaptiveRouting := flag.Bool("adaptive-routing", true,
		"congestion-adaptive route resolution (ADR-0036, engine default ON), -netfile runs only: "+
			"with -scenario the manifest's content-hashed adaptive_routing pin owns this and an explicit flag is rejected. "+
			"Pass -adaptive-routing=false when warm-starting (-state-in) a pre-v6 static-routing state whose continuation must stay bit-exact")
	stateOut := flag.String("state-out", "", "write the engine's full state to this file at -state-at, plus FILE.meta.json binding it to this network; the run CONTINUES after the dump (ADR-0029 phase 1)")
	stateAt := flag.Uint64("state-at", 0, "tick at which -state-out writes the state file (required with -state-out, and only with it)")
	stateIn := flag.String("state-in", "", "warm-start from a state file written by -state-out: the run begins at that state's tick with that state instead of an empty network at tick 0. -ticks stays ABSOLUTE, so a state at tick 20000 with -ticks 54000 simulates the remaining 34000. Refuses a state saved against a different network (lane fingerprint), and refuses a missing sidecar")
	stateReseed := flag.Bool("state-reseed", false, "allow -state-in when the run's seed differs from the seed the state was saved under. Off by default: the continuation draws from the RUN's seed, so a mismatch splices two different deterministic programs. Set this when that is what you want (same state, different draws)")
	intentLog := flag.Bool("intent-log", true, "retain the engine's whole-run in-memory intent log; -intent-log=false drops it for long headless runs (the durable JetStream record and replay are unaffected — only the in-memory RunLog is lost). A 54,000-tick chi-loop-urban run at ~5,900 vehicles is ~225M retained intents and was OOM-killed at tick 38,200 on a 123 GB machine, so this is a requirement at that horizon rather than a tuning knob")
	logBatch := flag.Bool("log-batch", true, "record each tick's applied intents as ONE TSLB batch message on ts.{run}.log.intents (ADR-0035) instead of one message per intent on ts.{run}.log.intent. Off restores the pre-ADR-0035 record plane, for A/B measurement and for writing a recording an older reader can consume; replay reads either form")
	storeCompress := flag.Bool("store-compress", true, "S2-compress the recording stream's file storage (ADR-0035). Storage-layer only: payloads and every reader are unchanged. Off if the write-side CPU cost ever matters more than the bytes")
	flag.Parse()

	if *scenarioDir != "" && *netfile != "" {
		fmt.Fprintln(os.Stderr, "serve: -scenario and -netfile are mutually exclusive (the scenario names its network)")
		os.Exit(2)
	}
	if *scenarioDir == "" && *netfile == "" {
		fmt.Fprintln(os.Stderr, "serve: -scenario or -netfile is required")
		os.Exit(2)
	}
	if *intentBatch != "on" && *intentBatch != "off" {
		fmt.Fprintf(os.Stderr, "serve: -intent-batch must be on|off, got %q\n", *intentBatch)
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
		// allowed beside it. adaptive_routing joins the owned set: it is
		// part of the manifest's content hash (ADR-0012), so a CLI
		// override would put two trajectories under one (hash, seed).
		conflicting := []string{}
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "rate", "density", "types", "adaptive-routing":
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
	// Longitudinal safety gate. Live runs need it and headless replays must
	// not get it by surprise, so it is a flag with a live-friendly default
	// rather than a kernel constant: an applied intent is computed from an
	// observation a few ticks old and re-issued by hold-last for a few ticks
	// more, which is long enough for a vehicle to hold a positive accel into
	// a leader that has already stopped. Without the gate the kernel carries
	// that out and books the overlap as a collision.
	// Validate before it reaches the kernel: a negative value silently
	// disables the gate (the gate's own `b <= 0` guard reads it as "off"),
	// and a NaN propagates into every bound comparison, where it neither
	// binds nor errors — the run looks gated and is not.
	if math.IsNaN(*safetyDecel) || math.IsInf(*safetyDecel, 0) || *safetyDecel < 0 {
		fmt.Fprintf(os.Stderr, "serve: -safety-decel must be a finite value >= 0 (got %v); 0 disables the gate\n", *safetyDecel)
		os.Exit(2)
	}
	spec.Params.SafetyDecel = *safetyDecel

	// Warm start and state dump (ADR-0029 phase 1). The state file is read
	// here — before the broker, the store and the embedded clients — so a
	// missing sidecar or a state from another network costs nothing but the
	// message that says so.
	stateAtSet := false
	adaptiveSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "state-at" {
			stateAtSet = true
		}
		if f.Name == "adaptive-routing" {
			adaptiveSet = true
		}
	})
	// Adaptive routing (ADR-0036; default ON since the 2026-07-31 flip).
	// -netfile runs only: with -scenario the manifest owns the flag (it is
	// content-hashed, ADR-0012) and an explicit -adaptive-routing is
	// rejected above. -adaptive-routing=false is the netfile path's opt-out
	// and the warm-start continuation pin for pre-v6 state files.
	if adaptiveSet {
		spec.Params.AdaptiveRouting = *adaptiveRouting
	}
	var initialState []byte
	var initialMeta *engine.StateMeta
	if *stateIn != "" {
		data, meta, err := engine.LoadState(*stateIn)
		if err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
		initialState, initialMeta = data, meta
	}
	startTick := uint64(0)
	inSeed := uint64(0)
	if initialMeta != nil {
		startTick = initialMeta.Tick
		inSeed = initialMeta.Seed
	}
	if err := validateStateFlags(stateFlags{
		in: *stateIn, out: *stateOut, at: *stateAt, atSet: stateAtSet,
		store: *store, metrics: *metricsOut, ticks: spec.Ticks, inTick: startTick,
		inSeed: inSeed, seed: spec.Seed, reseed: *stateReseed,
		demand: scen != nil && len(scen.Demands) > 0,
		driver: *withDriver && *drivers > 0,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(2)
	}
	if *stateOut != "" {
		// Probe the destination now. The dump ABORTS the run on failure
		// (a run that was asked for a state file and silently produces
		// none is the failure this flag exists to avoid), and discovering
		// an unwritable path an hour in is the expensive way to learn it.
		if err := probeWritable(*stateOut); err != nil {
			fmt.Fprintf(os.Stderr, "serve: -state-out %s: %v\n", *stateOut, err)
			os.Exit(1)
		}
	}
	if initialMeta != nil {
		fmt.Printf("serve: warm start from %s: tick %d, %d vehicles, seed %d, run %q, network %s\n",
			*stateIn, initialMeta.Tick, initialMeta.Vehicles, initialMeta.Seed, initialMeta.Run, initialMeta.NetworkPath)
		if initialMeta.Seed != spec.Seed {
			// Reached only with -state-reseed: validateStateFlags rejects
			// the mismatch otherwise. Say plainly what was opted into.
			fmt.Printf("serve: NOTE -state-reseed: state was saved under seed %d, this run uses seed %d — the loaded history and the continuation come from different draws\n",
				initialMeta.Seed, spec.Seed)
		}
		if initialMeta.ScenarioHash != "" && spec.Hash != "" && initialMeta.ScenarioHash != spec.Hash {
			fmt.Printf("serve: NOTE state was saved under scenario hash %s, this run is %s — same network (the fingerprint passed), different scenario content\n",
				initialMeta.ScenarioHash, spec.Hash)
		}
	}

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
		// 4 MB (ADR-0016): per-message discipline is ~768 KiB chunks —
		// the TSSG signal table (measured: sf-lean 2.1 MB, la-lean 7.3 MB)
		// is chunked on the wire, so this is headroom for big-fleet TSSF
		// snapshots (~1.2 MB at 50k vehicles), NOT a design allowance. A
		// pathological frame fails LOUD (the pubErrs logging).
		MaxPayload: natsMaxPayload,
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
	// The recorder publishes one message per claimed vehicle per tick and
	// drains the whole batch in awaitBatch before the next tick, so the async
	// pending window only ever has to hold ONE tick's worth of messages —
	// which the driver's claim capacity already bounds. Sizing it from
	// -capacity keeps the two in step instead of pinning a magic constant:
	// a 1000-vehicle run gets a small window, a 60000-vehicle city run gets
	// one that fits.
	//
	// nats.go defaults this to 4000 (defaultAsyncPubAckInflight), which a
	// city-scale run blows through: a 30-minute chi-loop cut aborted at tick
	// 13,462 with ~8,200 vehicles on "stalled with too many outstanding async
	// published messages". The limit is soft rather than a hard cap — on
	// overflow the client waits stallWait for pubacks to drain and only fails
	// with ErrTooManyStalledMsgs if they don't — which is why the same run
	// survived 4,736 vehicles earlier and only died once publish outran ack
	// as the growing store slowed disk writes.
	//
	// The slack covers what rides along with the intents in a tick batch:
	// spawn/despawn verbs, the rolling CRC, and keyframe chunks. js.pafs is a
	// map grown on demand and maxpa is only compared against its length, so
	// an unreached ceiling costs nothing.
	maxPending := *capacity + 8192
	if maxPending < 4000 {
		maxPending = 4000 // never below the library default
	}
	js, err := nc.JetStream(nats.PublishAsyncMaxPending(maxPending))
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
	// finalStats is published by the run goroutine before it sends on
	// runErr, so the channel receive below is the happens-before edge that
	// makes the read race-free.
	var finalStats engine.Stats
	var finalExpired, finalInjected int
	var finalExpiredByLane map[string]int
	var finalLastInject, finalFirstExpire uint64
	var finalDOA int
	var finalCoast, finalCoastMoving, finalCoastMax int
	var finalCoastFirst uint64
	var finalVehTicks int
	var finalGated, finalOverlapped int
	var finalCross int
	var finalCrossBySec map[string]int
	var finalObsErrs, finalObsWorst, finalObsBridge uint64
	go func() {
		// PaceFloor = one tick of wall time at -pace 1 (1× realtime); -pace N
		// divides it by N, -pace 0 disables pacing entirely (batch mode).
		cc := natsio.ContractConfig{
			PaceFloor: paceFloorDur, StartGate: startGate,
			InitialState: initialState, InitialStateMeta: initialMeta,
			StateDumpPath: *stateOut, StateDumpTick: *stateAt,
		}
		if mobs != nil {
			cc.Observer = mobs
		}
		lr, err := natsio.RunLive(nc, js, *run, spec, natsio.RecorderConfig{
			DropEngineIntentLog: !*intentLog,
			UnbatchedIntentLog:  !*logBatch,
			UncompressedStore:   !*storeCompress,
		}, cc)
		if err == nil && mobs != nil {
			err = mobs.finish(lr.Engine, *metricsOut, spec.Ticks)
		}
		if lr != nil && lr.Engine != nil {
			finalStats = lr.Engine.Stats
			finalExpired, finalInjected = lr.Engine.DirExpired, lr.Engine.DirInjected
			finalExpiredByLane = lr.Engine.DirExpiredByLane
			finalLastInject, finalFirstExpire = lr.Engine.DirLastInject, lr.Engine.DirFirstExpire
			finalDOA = lr.Engine.DirDeadOnArrival
			finalCoast, finalCoastMoving = lr.Engine.CoastVehTicks, lr.Engine.CoastMovingVehTicks
			finalCoastMax, finalCoastFirst = lr.Engine.CoastMaxPerTick, lr.Engine.CoastFirstTick
			finalVehTicks = lr.Engine.VehTicks
			finalGated, finalOverlapped = lr.Engine.SafetyGated, lr.Engine.SafetyOverlapped
			finalCross, finalCrossBySec = lr.Engine.CrossOverlaps, lr.Engine.CrossOverlapsBySection
		}
		if lr != nil && lr.Contract != nil {
			finalObsErrs = lr.Contract.ObsErrs()
			finalObsWorst = lr.Contract.ObsWorstOutage()
			finalObsBridge = lr.Contract.ObsBridgeTicks()
		}
		runErr <- err
	}()

	barrier := make(chan attachOutcome, *drivers+1)
	var expected []string
	if *withDriver {
		if *drivers < 1 {
			fmt.Fprintln(os.Stderr, "serve: -drivers must be >= 1")
			os.Exit(2)
		}
		// Capacity is the FLEET budget, split evenly. Splitting matters: each
		// replica claims up to its own capacity, so N replicas at the full
		// number would let one replica claim the whole fleet and leave the
		// others idle — the ceiling this flag exists to raise.
		per := *capacity / *drivers
		if per < 1 {
			per = 1
		}
		for i := 0; i < *drivers; i++ {
			name := "default driver"
			if *drivers > 1 {
				name = fmt.Sprintf("default driver %d/%d", i+1, *drivers)
			}
			expected = append(expected, name)
			go func(name string) {
				barrier <- attachDriver(ns, *run, per, *exitRouting, *intentBatch == "off", *attachTimeout, name)
			}(name)
		}
	}
	// Scenario demand parts run through the embedded reference demand
	// director (engine/natsio/demand): sampled with the RUN seed, so the
	// recorded (content-hash, seed) run key covers the demand realization
	// and a same-seed rerun re-issues the identical verb program.
	if scen != nil && len(scen.Demands) > 0 {
		expected = append(expected, "demand director")
		go func() {
			barrier <- attachDirector(ns, *run, spec.Seed, startTick, scen.Demands)
		}()
	}
	// The reference actuated signal controller (ADR-0037 milestone 2):
	// in-process like the demand director, but every command rides the
	// signal_set verb channel — the wire path is what it exists to prove.
	if *withSigctl {
		expected = append(expected, "signal controller")
		go func() {
			barrier <- attachSigctl(ns, *run, spec.Net.Path, *sigctlMinQueue)
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
		// Arrivals vs exits (ADR-0021): the ONLY direct readout of whether
		// routed demand is reaching its destinations. A run whose arrivals
		// are a small fraction of despawns has vehicles drifting off-route
		// and leaving via the map edge instead.
		// Director demand delivery. Expiry is SILENT in every other readout —
		// it is not a denial, so denied_* stays at zero while the run quietly
		// loses most of its demand — and a run that injects a fraction of what
		// its scenario asked for is not the scenario anyone calibrated.
		if tot := finalExpired + finalInjected; tot > 0 {
			delivered := float64(finalInjected) / float64(tot)
			fmt.Printf("serve: director demand: injected=%d expired=%d (%.0f%% of accepted directives delivered) lastInject=tick %d firstExpire=tick %d deadOnArrival=%d\n",
				finalInjected, finalExpired, 100*delivered,
				finalLastInject, finalFirstExpire, finalDOA)
			// Loud, because the alternative is a run that reads as a clean
			// measurement of a scenario it never actually simulated. Under-
			// delivery is not a detail of the result, it invalidates it.
			if delivered < demandDeliveryWarn {
				fmt.Printf("serve: WARNING: %.0f%% of demand never entered the network — this run is NOT the scenario as written; treat its metrics as void\n",
					100*(1-delivered))
				if finalDOA > 0 {
					fmt.Printf("serve: WARNING: %d directives arrived past the %d-tick hold window and were never attempted (the director could not keep up with declared demand)\n",
						finalDOA, engine.DirectorSpawnHoldTicks)
				}
			}
			// Localized for the same reason collisions are: a network-wide
			// loss count cannot distinguish "demand is globally too high"
			// from "three origin lanes are wedged".
			if len(finalExpiredByLane) > 0 {
				ls := make([]string, 0, len(finalExpiredByLane))
				for k := range finalExpiredByLane {
					ls = append(ls, k)
				}
				sort.Slice(ls, func(i, j int) bool {
					if a, b := finalExpiredByLane[ls[i]], finalExpiredByLane[ls[j]]; a != b {
						return a > b
					}
					return ls[i] < ls[j]
				})
				if len(ls) > 10 {
					ls = ls[:10]
				}
				fmt.Printf("serve: expiries over %d origin lanes; worst:\n", len(finalExpiredByLane))
				for _, l := range ls {
					fmt.Printf("serve:   %-40s %d\n", l, finalExpiredByLane[l])
				}
			}
		}
		// Observation-frame loss comes FIRST because it is the cause and the
		// coasting line below is its effect: a controller that never received
		// its frame emitted no intents, so its whole claimed fleet coasted.
		//
		// Two different lines, because the two cases have different verdicts.
		// ADR-0008 §2 holds the last intent across (cadence−1) + HoldLastTicks
		// ticks, so an outage no longer than that bridge is HEALED and the
		// fleet stayed controlled — worth reporting, never worth voiding a run
		// over. Only a longer consecutive streak actually blinds a controller,
		// and only that line carries the "FAILED to publish" wording the bake
		// gate (scripts/chicago/record-hero.sh) greps for.
		if finalObsErrs > 0 && finalObsWorst <= finalObsBridge {
			fmt.Printf("serve: note: %d observation frame(s) lost, worst consecutive run %d of a %d-tick hold-last bridge — absorbed, the fleet stayed controlled\n",
				finalObsErrs, finalObsWorst, finalObsBridge)
		}
		if finalObsWorst > finalObsBridge {
			fmt.Printf("serve: WARNING: %d observation frames FAILED to publish, worst consecutive run %d ticks vs a %d-tick hold-last bridge — a controller was blind past the bridge and its vehicles coasted with no car-following control; this is NOT traffic congestion\n",
				finalObsErrs, finalObsWorst, finalObsBridge)
			// Derived from the actual cap and the actual layout, not from
			// remembered numbers: this line is operator guidance, and the
			// figures it quotes decide whether someone reaches for -drivers
			// or goes looking for a traffic explanation that does not exist.
			perEgo := natsio.ObsEgoBytes(natsio.ObsFeaturePolicyCtx)
			knee := natsio.ObsEgoCapacity(natsMaxPayload, natsio.ObsFeaturePolicyCtx, 24)
			fmt.Printf("serve:   most likely the %d MiB broker payload cap: the frame carries every claimed ego in ONE message (%d B each plus its route), so it stops fitting near %d vehicles at a 24 B route. Split the fleet with -drivers N (keep -capacity/N comfortably under that).\n",
				natsMaxPayload>>20, perEgo, knee)
		}
		// Coasting: the driver-side twin of demand expiry, and just as silent.
		// A holdlast vehicle past its hold window gets no car-following term at
		// all, so it holds speed into whatever is stopped ahead — the resulting
		// overlaps are booked as collisions and read as congestion. Reported
		// against total vehicle-ticks because the absolute count means nothing
		// without the population it is drawn from.
		if finalVehTicks > 0 && finalCoast > 0 {
			frac := float64(finalCoast) / float64(finalVehTicks)
			fmt.Printf("serve: uncontrolled coasting: %d of %d vehicle-ticks (%.2f%%) had no controller intent; %d (%.2f%%) while moving >%.0f m/s; worst tick %d vehicles; first at tick %d\n",
				finalCoast, finalVehTicks, 100*frac,
				finalCoastMoving, 100*float64(finalCoastMoving)/float64(finalVehTicks),
				1.0, finalCoastMax, finalCoastFirst)
			if frac > coastWarn {
				fmt.Printf("serve: WARNING: the driver could not keep up — %.1f%% of vehicle-ticks ran with no car-following control, which manufactures overlaps that are NOT traffic congestion\n",
					100*frac)
				// "Could not keep up" has at least three distinct causes and
				// they need different fixes, so print what each replica
				// actually held and what its subscriptions dropped rather than
				// leaving the reader to guess:
				//
				//   capacity-bound  fleet ≈ its capacity -> raise -capacity
				//   obs drops       drops on ctl.obs     -> the replica cannot
				//                   process a frame per tick; add replicas
				//                   with -drivers
				//   snapshot drops  drops on state.snap  -> the CLAIM backstop
				//                   is gone. Spawns are announced ONCE, so a
				//                   vehicle that missed its announcement is
				//                   never re-offered and the unclaimed pool
				//                   grows without bound.
				//
				// The third is invisible in every other number serve prints:
				// capacity looks fine, no frame failed to publish, and the
				// fleet simply stops growing while the vehicle count climbs.
				for _, out := range attached {
					if out.drv == nil {
						continue
					}
					drops := out.drv.SubDrops()
					fmt.Printf("serve:   %-22s fleet %5d  last obs tick %d",
						out.client, out.drv.FleetSize(), out.drv.LastObsTick())
					if len(drops) == 0 {
						fmt.Printf("  no subscription drops\n")
						continue
					}
					fmt.Printf("\n")
					// Sorted: the rest of this report is stable text that gets
					// diffed between runs, and Go randomizes map iteration.
					subjs := make([]string, 0, len(drops))
					for subj := range drops {
						subjs = append(subjs, subj)
					}
					sort.Strings(subjs)
					for _, subj := range subjs {
						fmt.Printf("serve:     DROPPED %d messages on %s\n", drops[subj], subj)
					}
				}
			}
		}
		// Safety gate. Two numbers, and they answer different questions:
		// how often the kernel had to save a controller from its own stale
		// intent, and how often it was already too late. The second is the
		// one that says whether the remaining collisions are real.
		if finalVehTicks > 0 && *safetyDecel > 0 {
			fmt.Printf("serve: safety gate (%.1f m/s²): bound %d of %d vehicle-ticks (%.2f%%); %d saw an already-overlapped pair (%.4f%%)\n",
				*safetyDecel, finalGated, finalVehTicks,
				100*float64(finalGated)/float64(finalVehTicks), finalOverlapped,
				100*float64(finalOverlapped)/float64(finalVehTicks))
		}
		// Crossings that landed on top of somebody. This is the overlap
		// source no acceleration guardrail can reach: a boundary crossing is
		// a PLACEMENT, and the only thing that prevents it is the follower
		// stopping short of the boundary in the first place.
		if finalCross > 0 {
			fmt.Printf("serve: boundary crossings landing overlapped: %d\n", finalCross)
			secs := make([]string, 0, len(finalCrossBySec))
			for k := range finalCrossBySec {
				secs = append(secs, k)
			}
			sort.Slice(secs, func(i, j int) bool {
				if a, b := finalCrossBySec[secs[i]], finalCrossBySec[secs[j]]; a != b {
					return a > b
				}
				return secs[i] < secs[j]
			})
			for _, sec := range secs[:min(len(secs), 8)] {
				fmt.Printf("serve:   %-40s %d\n", sec, finalCrossBySec[sec])
			}
		}
		if st := finalStats; st.Despawned > 0 {
			fmt.Printf("serve: spawned=%d despawned=%d (arrived=%d, %.0f%% of despawns) lanechanges=%d collisions=%d\n",
				st.Spawned, st.Despawned, st.Arrived, 100*float64(st.Arrived)/float64(st.Despawned),
				st.LaneChanges, st.Collisions)
			// Collisions are a model-pathology indicator (ADR-0007): localize
			// them, because a network-wide count says nothing about whether
			// the cause is one bad junction or the whole import.
			if len(st.CollisionsBySection) > 0 {
				secs := make([]string, 0, len(st.CollisionsBySection))
				for k := range st.CollisionsBySection {
					secs = append(secs, k)
				}
				sort.Slice(secs, func(i, j int) bool {
					a, b := st.CollisionsBySection[secs[i]], st.CollisionsBySection[secs[j]]
					if a != b {
						return a > b
					}
					return secs[i] < secs[j]
				})
				top := min(len(secs), 8)
				fmt.Printf("serve: collision observations over %d sections; worst:\n", len(secs))
				for _, sec := range secs[:top] {
					fmt.Printf("serve:   %-40s %d\n", sec, st.CollisionsBySection[sec])
				}
			}
		}
		// Strands are the gridlock indicator (ADR-0034): the kernel removed
		// a vehicle that had been motionless for five minutes at a junction
		// it could not enter. Nonzero means the network gridlocked, and the
		// sections say where.
		//
		// OUTSIDE the despawn block on purpose. Nested under Despawned > 0
		// it went silent on exactly the worst run there is — one where the
		// network seizes early and every vehicle strands rather than
		// arriving — which is the opposite of the "never silent" property
		// this print exists to provide.
		if st := finalStats; st.Stranded > 0 {
			secs := make([]string, 0, len(st.StrandedBySection))
			for k := range st.StrandedBySection {
				secs = append(secs, k)
			}
			sort.Slice(secs, func(i, j int) bool {
				a, b := st.StrandedBySection[secs[i]], st.StrandedBySection[secs[j]]
				if a != b {
					return a > b
				}
				return secs[i] < secs[j]
			})
			fmt.Printf("serve: GRIDLOCK: %d vehicles stranded (stopped >%.0f s at a blocked junction) over %d sections; worst:\n",
				st.Stranded, spec.Params.StrandAfterS, len(secs))
			for _, sec := range secs[:min(len(secs), 8)] {
				fmt.Printf("serve:   %-40s %d\n", sec, st.StrandedBySection[sec])
			}
		}
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

// stateFlags is the -state-in/-state-out/-state-at surface, plus the run
// facts the rules depend on (ticks, and the tick of the state named by
// -state-in).
type stateFlags struct {
	in      string
	out     string
	at      uint64
	atSet   bool
	store   string
	metrics string
	ticks   uint64
	inTick  uint64
	// inSeed is the seed the loaded state was saved under, seed is the
	// seed this run will actually draw from, and reseed is -state-reseed.
	inSeed uint64
	seed   uint64
	reseed bool
	// demand is true when the scenario carries demand parts, so the run
	// would attach the embedded demand director.
	demand bool
	// driver is true when the run would attach the in-process default
	// driver (-driver, default true).
	driver bool
}

// validateStateFlags rejects warm-start/dump combinations that cannot do
// what they say. Everything here is a usage error (exit 2) rather than a
// runtime one, because every one of them is knowable before the run starts
// and the alternative is discovering it after an hour of simulation.
func validateStateFlags(f stateFlags) error {
	if f.out != "" && !f.atSet {
		return fmt.Errorf("-state-out needs -state-at TICK: there is no default tick to dump at, and a run that quietly never writes the file is the failure this flag exists to prevent")
	}
	if f.atSet && f.out == "" {
		return fmt.Errorf("-state-at needs -state-out FILE: a dump tick with nowhere to write it does nothing")
	}
	if f.in != "" && f.metrics != "" {
		// The metrics kernel refuses to attach to an engine that has already
		// stepped (metrics.go: "attach before the first Step"), because its
		// per-vehicle position deltas have no earlier frame to difference
		// against. A warm-started run attaches at the restored tick, so the
		// pair always dies — after the broker and store are up, with an
		// error that reads like an internal ordering bug rather than a flag
		// combination. Knowable here, so it is rejected here.
		return fmt.Errorf("-state-in cannot be combined with -metrics-out: the metrics kernel must attach before the first Step, and a warm-started run begins at tick %d. Measure a cold run, or run the warm start unmeasured", f.inTick)
	}
	if f.in != "" && f.store != "" {
		// The record plane anchors every recording with a keyframe at the
		// run's FIRST tick and replay requires one at tick 0 (player.go:
		// "no keyframe ≤ target tick 0"). A warm-started run's first
		// keyframe is at the warm-start tick, so the recording it produces
		// cannot be opened — it fails loudly at replay time, which is the
		// wrong time. Bending the record plane to accept a nonzero floor is
		// out of scope for ADR-0029 phase 1; warm start is for UNRECORDED
		// debugging runs.
		return fmt.Errorf("-state-in cannot be combined with -store: a warm-started run's first keyframe is at tick %d, not 0, and replay anchors on a tick-0 keyframe — the recording would be unopenable. Run warm starts undurable (no -store), or record a cold run", f.inTick)
	}
	if f.in != "" && !f.reseed && f.inSeed != f.seed {
		// The restored engine draws every future stream from the RUN's
		// seed (keyframe.go rebuilds the engine from the spec; new
		// vehicles derive their RNG from spec.Seed, and the demand
		// director samples from it too). A mismatch therefore splices one
		// deterministic program's history onto another's continuation,
		// which is exactly what ADR-0029's bit-exact criterion rules out.
		//
		// This is a default trap, not a hypothetical: -seed defaults to 1,
		// so warm-starting a state saved under any other seed and simply
		// FORGETTING the flag produces a plausible run from two programs.
		// A printed note is not enough for a failure whose whole signature
		// is that the run looks fine. Reseeding on purpose is legitimate
		// and stays available, but it has to be said out loud.
		return fmt.Errorf("-state-in was saved under seed %d and this run uses seed %d: the continuation draws from the run's seed, so the loaded history and everything after it would come from different deterministic programs. Pass -seed %d to continue that program, or -state-reseed to say the different draws are intended", f.inSeed, f.seed, f.inSeed)
	}
	if f.in != "" && f.driver {
		// The contract plane is NOT part of the restored state, and the
		// first resumed tick therefore runs with a controller that has not
		// caught up yet.
		//
		// A warm restore builds a fresh Contract: empty claims, empty
		// byVehicle, empty hold, empty announced. AfterStep publishes each
		// controller's observation FIRST and only then announces the
		// vehicles it does not have claims for, so on the first resumed
		// tick every restored vehicle is unclaimed AND has no hold-last
		// intent to bridge with (ADR-0008 §6) — it gets the default, not
		// the driver's intent. The cold run at that same tick had the
		// driver's. One tick of different inputs, and ADR-0029's whole
		// claim is bit-exact continuation.
		//
		// Announcing before publishing would not close it either: claiming
		// is a request/reply round trip over NATS, so the claim cannot be
		// guaranteed to land before the step regardless of ordering within
		// the tick. The real fix is to persist and reconstruct contract
		// state — claims, hold-last, sequence numbers — which is ADR-0029
		// phase 2 territory alongside the demand director's position.
		//
		// Refused rather than warned, for the same reason as the seed
		// splice next door: a one-tick divergence is invisible in the
		// output and fatal to a replay CRC. -driver=false gives a
		// controller-free run whose warm start IS bit-exact, because then
		// every tick uses the default on both sides.
		return fmt.Errorf("-state-in cannot be combined with -driver: the contract plane is not restored, so on the first resumed tick (%d) every vehicle is unclaimed with no hold-last intent and gets the default instead of the driver's — a one-tick divergence from the cold run this flag promises to continue exactly. Pass -driver=false to warm-start a controller-free run, or start cold", f.inTick)
	}
	if f.reseed && f.in == "" {
		// Same rule as -state-at without -state-out: a flag that modifies a
		// mode you are not in is a misunderstanding, and silently ignoring
		// it teaches the wrong thing about what it does.
		return fmt.Errorf("-state-reseed needs -state-in: it only relaxes the seed check on a warm start, and there is no warm start here")
	}
	if f.in != "" && f.demand {
		// The director's position cannot yet be restored EXACTLY, and the
		// error is a silent duplicate rather than a loud failure.
		//
		// A cold director at tick N has already sent every arrival up to
		// N+Lead (onSnapshot sends while `arrival <= f.Tick + Lead`), and
		// the engine holds those as accepted directives in dirQueue — which
		// the keyframe restores. But fastForward only skips arrivals
		// <= StartTick, so the warm director resumes INSIDE that window and
		// re-sends the whole lead's worth. The contract's request-id dedup
		// map (`verbs`) is per-process and starts empty, so nothing catches
		// the repeat: those arrivals spawn twice.
		//
		// Fast-forwarding to N+Lead instead is not the fix. The true
		// boundary is whatever the engine had ACCEPTED when the dump fired,
		// and verbs for arrivals near the edge of the window may still be
		// in flight — so N+Lead can skip an arrival that never landed,
		// trading duplicates for losses. Restoring it exactly means
		// reconciling per flow against the restored queue, which is a
		// design question and phase 2's (ADR-0029).
		//
		// Until then this is refused rather than approximated: warm start's
		// whole claim is that the continuation is identical to the cold
		// run's, and a run that quietly double-spawns a lead window of
		// arrivals is precisely the plausible-looking wrong run the sidecar
		// exists to prevent.
		return fmt.Errorf("-state-in cannot be used with a scenario that has demand parts: the demand director cannot yet resume exactly where the saved state left off, and would re-send the arrivals already queued in it (ADR-0029 phase 2). Warm start is for runs whose spawning is built in")
	}
	if f.in != "" && f.inTick >= f.ticks {
		return fmt.Errorf("-state-in is at tick %d and -ticks is %d: nothing left to simulate (-ticks is absolute, not additional)", f.inTick, f.ticks)
	}
	if f.out != "" && f.at > f.ticks {
		return fmt.Errorf("-state-at %d is past -ticks %d: the run would end before the dump fired", f.at, f.ticks)
	}
	if f.out != "" && f.in != "" && f.at <= f.inTick {
		// Not "it would never fire" — RunLive takes a dump requested AT the
		// starting tick before the loop (natsio/run.go), so at == inTick
		// fires and writes back the state just loaded. That is the useless
		// case, and at < inTick is the incoherent one. Both are refused as
		// usage errors; neither is a silent no-op.
		return fmt.Errorf("-state-at %d is not after the warm-start tick %d: at the warm-start tick the dump just rewrites the state that was loaded, and before it there is no such tick in this run", f.at, f.inTick)
	}
	return nil
}

// probeWritable checks that a file can actually be created at path, without
// leaving anything behind if it could not have been.
func probeWritable(path string) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".state-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// attachOutcome is one embedded client's report to the attach barrier.
type attachOutcome struct {
	client  string // barrier identity ("default driver", "demand director")
	desc    string // success-line detail
	cleanup func() // deferred Close calls; nil on failure
	// drv is the driver replica behind this outcome (nil for non-driver
	// clients), retained so the end-of-run report can ask it what it
	// actually held and what its subscriptions dropped. Without a handle,
	// "the driver could not keep up" is a symptom with nowhere to look.
	drv *driver.Driver
	err error
}

// attachDriver connects and attaches the in-process default driver replica.
// driver.New returning IS the readiness signal: the hello handshake has
// been answered and New's final Flush guarantees the server has processed
// the observation/snapshot/unclaimed subscriptions, so tick 0's unclaimed
// pool reaches this replica.
func attachDriver(ns *server.Server, run string, capacity int, exitRouting, intentBatchOff bool, metaWait time.Duration, name string) attachOutcome {
	dnc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name(name))
	if err != nil {
		return attachOutcome{client: name, err: err}
	}
	djs, err := dnc.JetStream()
	if err != nil {
		dnc.Close()
		return attachOutcome{client: name, err: err}
	}
	d, err := driver.New(dnc, djs, driver.Config{Run: run, Capacity: capacity, ExitRouting: exitRouting, IntentBatchOff: intentBatchOff, MetaWait: metaWait})
	if err != nil {
		dnc.Close()
		return attachOutcome{client: name, err: err}
	}
	return attachOutcome{
		client:  name,
		desc:    fmt.Sprintf("id %s, capacity %d", d.ID(), capacity),
		cleanup: func() { d.Close(); dnc.Close() },
		drv:     d,
	}
}

// attachDirector connects and attaches the embedded demand director, then
// waits for its snapshot subscription to be live on the server
// (Director.Ready) so no early snapshot — tick 0's included — slips past
// the verb loop.
// startTick is the warm-start tick (0 for a cold run): the director skips
// the arrivals already contained in the restored state instead of dumping
// the whole backlog at the first snapshot (ADR-0029 phase 1).
func attachDirector(ns *server.Server, run string, seed, startTick uint64, dfs []*scenario.DemandFile) attachOutcome {
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
	dd, err := demand.Attach(dnc, djs, demand.Config{Run: run, Seed: seed, StartTick: startTick, Log: lg}, dfs)
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

// attachSigctl connects and attaches the embedded reference actuated
// signal controller (ADR-0037 milestone 2). Detector geometry comes from
// the run's network file (static scenario content); programs and presence
// arrive over the wire. Ready is the readiness signal (subscriptions
// flushed live), the demand director's pattern.
func attachSigctl(ns *server.Server, run, netPath string, minQueue int) attachOutcome {
	geom, err := sigctl.LoadGeom(netPath)
	if err != nil {
		return attachOutcome{client: "signal controller", err: err}
	}
	snc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("sigctl"))
	if err != nil {
		return attachOutcome{client: "signal controller", err: err}
	}
	sjs, err := snc.JetStream()
	if err != nil {
		snc.Close()
		return attachOutcome{client: "signal controller", err: err}
	}
	lg := log.New(os.Stderr, "", 0)
	ctl, err := sigctl.Attach(snc, sjs, sigctl.Config{Run: run, SwitchMinQueue: minQueue, Log: lg}, geom)
	if err != nil {
		snc.Close()
		return attachOutcome{client: "signal controller", err: err}
	}
	if err := ctl.Ready(); err != nil {
		ctl.Close()
		snc.Close()
		return attachOutcome{client: "signal controller", err: err}
	}
	return attachOutcome{
		client:  "signal controller",
		desc:    "gap-out actuation over signal_set",
		cleanup: func() { ctl.Close(); snc.Close() },
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
	// Delivery rides in the document so an artifact read months later still
	// carries whether the run actually simulated its own scenario.
	dd := engine.DemandDelivery{
		Injected:       e.DirInjected,
		Expired:        e.DirExpired,
		DeadOnArrival:  e.DirDeadOnArrival,
		LastInjectTick: e.DirLastInject,
	}
	if err := engine.WriteMetricsJSON(f, m.k, ticks, dd); err != nil {
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
