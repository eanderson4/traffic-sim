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
	flag.Parse()

	var spec engine.RunSpec
	var err error
	name := *net
	switch {
	case *scenarioDir != "":
		if *netfile != "" {
			fmt.Fprintln(os.Stderr, "simrun: -scenario and -netfile are mutually exclusive (the scenario names its network)")
			os.Exit(2)
		}
		sc, lerr := scenario.Load(*scenarioDir)
		if lerr != nil {
			fmt.Fprintln(os.Stderr, "simrun:", lerr)
			os.Exit(1)
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
	e, _, err := engine.Run(spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "simrun:", err)
		os.Exit(1)
	}
	fmt.Printf("net=%s ticks=%d seed=%d vehicles=%d spawned=%d despawned=%d lanechanges=%d collisions=%d mingap=%.3f crc=%016x\n",
		name, *ticks, *seed, len(e.Vehicles()), e.Stats.Spawned, e.Stats.Despawned,
		e.Stats.LaneChanges, e.Stats.Collisions, e.Stats.MinGap, e.CRC())
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
