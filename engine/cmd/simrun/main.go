// simrun is the M1 bring-up harness: run a network for N ticks with a given
// seed and print the final state CRC (hex) — the handle later milestones
// (NATS publishing, replay stores, scenario sweeps) build on. With -netfile
// it runs a compiled network JSON (format v1, e.g. netimport's netconvert
// bootstrap output) under the harness IDM policy.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

func main() {
	net := flag.String("net", "ring", "network: ring|lanedrop|straight|i80")
	netfile := flag.String("netfile", "", "compiled network JSON (network-format v1); overrides -net")
	scenarioDir := flag.String("scenario", "", "ADR-0012 scenario directory (overrides -net/-netfile/-rate/-density; explicit -seed/-ticks override the manifest)")
	rate := flag.Float64("rate", 600, "netfile runs: spawn rate per origin lane (veh/h)")
	density := flag.Float64("density", 80, "netfile runs: density cap (veh/lane-km)")
	ticks := flag.Uint64("ticks", 600, "ticks to simulate (default tick = 100 ms)")
	seed := flag.Uint64("seed", 1, "scenario seed")
	verbose := flag.Bool("v", false, "print per-section collision counts")
	metricsOut := flag.String("metrics-out", "", "write M13 metric-kernel output (ADR-0014 §6) as JSON to this path")
	adaptiveRouting := flag.Bool("adaptive-routing", true,
		"congestion-adaptive route resolution (ADR-0036, engine default ON), builtin/-netfile runs only: "+
			"with -scenario the manifest's content-hashed adaptive_routing pin owns this and an explicit flag is rejected")
	flag.Parse()

	var spec engine.RunSpec
	var err error
	var scen *scenario.Scenario
	name := *net
	switch {
	case *scenarioDir != "":
		if *netfile != "" {
			fmt.Fprintln(os.Stderr, "simrun: -scenario and -netfile are mutually exclusive (the scenario names its network)")
			os.Exit(2)
		}
		conflicting := []string{}
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "rate", "density", "net", "adaptive-routing":
				conflicting = append(conflicting, "-"+f.Name)
			}
		})
		if len(conflicting) > 0 {
			fmt.Fprintf(os.Stderr, "simrun: %s are scenario-owned when -scenario is set (only -seed/-ticks override)\n",
				strings.Join(conflicting, ", "))
			os.Exit(2)
		}
		sc, lerr := scenario.Load(*scenarioDir)
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "simrun:", lerr)
			os.Exit(1)
		}
		scen = sc
		if len(sc.Demands) > 0 {
			fmt.Fprintln(os.Stderr, "simrun: scenario declares demand parts — headless runs have no bus for director verbs; use serve (it embeds the demand director, run-seeded)")
			os.Exit(2)
		}
		spec, err = sc.RunSpec(map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck})
		if err != nil {
			fmt.Fprintln(os.Stderr, "simrun:", err)
			os.Exit(1)
		}
		// Explicit -seed/-ticks override the manifest (the content hash is
		// unchanged — run key = (hash, seed), ADR-0012 §6).
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "seed":
				spec.Seed = *seed
			case "ticks":
				spec.Ticks = *ticks
			}
		})
		name = *scenarioDir
		*ticks = spec.Ticks
		*seed = spec.Seed
	case *netfile != "":
		name = *netfile
		spec = engine.RunSpec{
			Net:    engine.NetSpec{Kind: "file", Path: *netfile},
			Scen:   engine.Scenario{SpawnRatePerLaneHour: *rate, DensityTargetPerKm: *density},
			Params: engine.DefaultParams(),
			Seed:   *seed,
			Ticks:  *ticks,
		}
	default:
		spec, err = engine.DefaultSpec(*net, *ticks, *seed)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "simrun:", err)
		os.Exit(2)
	}
	// Explicit-only: with -scenario the flag is rejected above, so a set
	// flag here is always a builtin/-netfile run; the default otherwise
	// leaves DefaultParams/DefaultSpec (and the manifest pin) untouched.
	adaptiveSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "adaptive-routing" {
			adaptiveSet = true
		}
	})
	if adaptiveSet {
		spec.Params.AdaptiveRouting = *adaptiveRouting
	}
	var e *engine.Engine
	var mk *engine.Kernel
	if *metricsOut != "" {
		// Metrics enabled: step manually so the kernel observes every tick.
		// The kernel is a read-only observer (ADR-0014 §1) — the run stays
		// bit-identical to the engine.Run path (CRC-invariance tested in
		// engine/metrics_test.go).
		e, err = engine.NewEngine(spec)
		if err == nil {
			// The scenario's metrics parts drive the kernel when present
			// (mirroring serve); otherwise the zero-authoring default.
			cfg := engine.DefaultKernelConfig(e.Net, e.Params.Dt)
			if scen != nil {
				cfg = scen.MetricConfig(e.Net)
			}
			mk, err = engine.NewKernel(e, cfg)
		}
		if err == nil {
			for e.Tick < spec.Ticks {
				e.Step()
				mk.Observe(e)
			}
			mk.Finalize(e)
		}
	} else {
		e, _, err = engine.Run(spec)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "simrun:", err)
		os.Exit(1)
	}
	fmt.Printf("net=%s ticks=%d seed=%d vehicles=%d spawned=%d despawned=%d lanechanges=%d collisions=%d mingap=%.3f crc=%016x\n",
		name, *ticks, *seed, len(e.Vehicles()), e.Stats.Spawned, e.Stats.Despawned,
		e.Stats.LaneChanges, e.Stats.Collisions, e.Stats.MinGap, e.CRC())
	if mk != nil {
		if err := writeMetricsFile(*metricsOut, mk, spec.Ticks); err != nil {
			fmt.Fprintln(os.Stderr, "simrun: metrics:", err)
			os.Exit(1)
		}
		fmt.Printf("metrics=%s\n", *metricsOut)
	}
	if *verbose && len(e.Stats.CollisionsBySection) > 0 {
		sections := make([]string, 0, len(e.Stats.CollisionsBySection))
		for s := range e.Stats.CollisionsBySection {
			sections = append(sections, s)
		}
		sort.Strings(sections)
		for _, s := range sections {
			fmt.Printf("  collisions[%s] = %d\n", s, e.Stats.CollisionsBySection[s])
		}
	}
}

// writeMetricsFile writes the kernel's metric JSON atomically: temp file
// plus rename (the scenario fmt precedent), so a crashed run never leaves a
// truncated artifact.
func writeMetricsFile(path string, k *engine.Kernel, ticks uint64) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := engine.WriteMetricsJSON(f, k, ticks); err != nil {
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
