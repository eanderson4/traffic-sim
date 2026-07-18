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
)

func main() {
	net := flag.String("net", "ring", "network: ring|lanedrop|straight|i80")
	netfile := flag.String("netfile", "", "compiled network JSON (network-format v1); overrides -net")
	rate := flag.Float64("rate", 600, "netfile runs: spawn rate per origin lane (veh/h)")
	density := flag.Float64("density", 80, "netfile runs: density cap (veh/lane-km)")
	ticks := flag.Uint64("ticks", 600, "ticks to simulate (default tick = 100 ms)")
	seed := flag.Uint64("seed", 1, "scenario seed")
	verbose := flag.Bool("v", false, "print per-section collision counts")
	flag.Parse()

	var spec engine.RunSpec
	var err error
	name := *net
	if *netfile != "" {
		name = *netfile
		spec = engine.RunSpec{
			Net:    engine.NetSpec{Kind: "file", Path: *netfile},
			Scen:   engine.Scenario{SpawnRatePerLaneHour: *rate, DensityTargetPerKm: *density},
			Params: engine.DefaultParams(),
			Seed:   *seed,
			Ticks:  *ticks,
		}
	} else {
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
