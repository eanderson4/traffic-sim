// i80xt is the M2 credibility harness (docs/kb/decisions ADR-0005/0007
// invariants hold: the sim runs the stock kernel; this is a driver around
// it). It runs the NGSIM I-80 17:00–17:15 scenario (engine/scenario_i80.go),
// exports the Edie x-t speed field in the same schema/binning as
// data/ngsim/i80-1700-1715-field.csv, renders the heatmap PNG, and measures
// the dominant backward wave speed through the ported estimator.
//
// With -in it skips the sim and measures an existing field CSV (real or
// sim) — one measurement code path for both.
package main

import (
	"flag"
	"fmt"
	"os"

	"traffic-sim/engine"
)

func main() {
	in := flag.String("in", "", "measure an existing field CSV and exit (no sim)")
	ticks := flag.Uint64("ticks", 9000, "measurement ticks (default 900 s = the NGSIM window)")
	warmup := flag.Uint64("warmup", 12000, "warm-up ticks before measurement (default 1200 s queue spin-up)")
	seed := flag.Uint64("seed", 1, "scenario seed")
	demand := flag.Float64("demand", 1.0, "scale factor on the reference (1.20x data) spawn rates (sensitivity knob)")
	slow := flag.Float64("slow", 0, "post-drop downstream speed limit (m/s); 0 = same as mainline")
	drop := flag.Int("drop", 5, "lanes remaining past the downstream drop")
	outCSV := flag.String("field", "", "write sim field CSV here (real-field schema)")
	outPNG := flag.String("png", "", "write sim heatmap PNG here")
	congested := flag.Float64("congested", 25, "cells below this speed (ft/s) drive the wave-speed fit")
	vmax := flag.Float64("vmax", 60, "speed (ft/s) mapped to the lightest color")
	flag.Parse()

	if *in != "" {
		u, err := engine.ReadFieldCSV(*in, 25, 3)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read field:", err)
			os.Exit(1)
		}
		c := engine.WaveSpeed(u, 25, 3, *congested)
		fmt.Printf("%s: grid %dx%d, dominant congestion wave speed: %.1f ft/s = %.1f km/h\n",
			*in, len(u), len(u[0]), c, c*engine.FpsToKmh)
		fmt.Printf("crossing wave stripes: %d\n", engine.WaveStripes(u, 25, 3))
		return
	}

	spec := engine.I80Spec(*warmup+*ticks, *warmup, *seed)
	if *demand != 1.0 { // scale the reference demand (sensitivity sweeps)
		for id, r := range spec.Scen.SpawnRates {
			spec.Scen.SpawnRates[id] = r * *demand
		}
	}
	if *drop != 5 {
		spec.Net.DropLanes = *drop
	}
	if *slow > 0 {
		spec.Net.DownstreamLimit = *slow
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "i80xt:", err)
		os.Exit(1)
	}
	spanS := float64(*ticks) * spec.Params.Dt
	xt := engine.NewXTField(engine.I80MeasLanes(), 25, 3, 1791, spanS)
	for e.Tick < spec.Ticks {
		e.Step()
		if e.Tick > *warmup {
			xt.Observe(e)
		}
	}

	u := xt.Speed()
	c := engine.WaveSpeed(u, 25, 3, *congested)
	fd, fdOK := xt.FDWaveSpeed(6, 12, 25, 45)
	fmt.Printf("i80 scenario: seed=%d demand=%.2f slow=%.1f m/s drop=%d warmup=%ds ticks=%d\n",
		*seed, *demand, spec.Net.DownstreamLimit, spec.Net.DropLanes, *warmup/10, *ticks)
	fmt.Printf("vehicles: live=%d spawned=%d despawned=%d lanechanges=%d wallhits=%d\n",
		len(e.Vehicles()), e.Stats.Spawned, e.Stats.Despawned, e.Stats.LaneChanges, e.Stats.WallHits)
	fmt.Printf("pathology: collisions=%d mingap=%.3f m by_section=%v\n", e.Stats.Collisions, e.Stats.MinGap, e.Stats.CollisionsBySection)
	fmt.Printf("dominant congestion wave speed (scan): %.1f ft/s = %.1f km/h   (real: -16.5 ft/s = -18.1 km/h; band -15…-20 km/h)\n",
		c, c*engine.FpsToKmh)
	if fdOK {
		fmt.Printf("FD chord-slope cross-check:            %.1f ft/s = %.1f km/h   (real FD: -13.7 ft/s = -15.0 km/h)\n",
			fd, fd*engine.FpsToKmh)
	}
	legs := engine.WaveStripeSpeeds(u, 25, 3)
	fmt.Printf("per-wave leg speeds: %v ft/s (median %.1f km/h; real-field median -15.0 km/h)\n",
		fmtFloats(legs), engine.MedianSpeeds(legs)*engine.FpsToKmh)
	fmt.Printf("crossing wave stripes: %d (acceptance: >= 2)\n", engine.WaveStripes(u, 25, 3))

	if *outCSV != "" {
		if err := xt.WriteCSV(*outCSV); err != nil {
			fmt.Fprintln(os.Stderr, "field:", err)
			os.Exit(1)
		}
		fmt.Println("field:", *outCSV)
	}
	if *outPNG != "" {
		if err := engine.WritePNG(*outPNG, u, *vmax, 4, 6); err != nil {
			fmt.Fprintln(os.Stderr, "png:", err)
			os.Exit(1)
		}
		fmt.Println("heatmap:", *outPNG)
	}
}

func fmtFloats(s []float64) string {
	out := "["
	for i, v := range s {
		if i > 0 {
			out += " "
		}
		out += fmt.Sprintf("%.1f", v)
	}
	return out + "]"
}
