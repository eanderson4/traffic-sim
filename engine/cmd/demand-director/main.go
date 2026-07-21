// demand-director is the standalone wrapper for the reference runtime
// demand director (engine/natsio/demand — ADR-0008 §5, ADR-0012 §3). It
// loads one demand definition (strict YAML; M10-era JSON parses once a
// "format_version": 1 key is added) and runs the verb loop against a live
// run. serve embeds the same package when a scenario declares demand parts,
// so standalone use is for flag-built (-netfile) runs and bring-up poking.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine/natsio/demand"
	"traffic-sim/engine/scenario"
)

func main() {
	url := flag.String("nats", "ws://127.0.0.1:8443", "NATS server URL (serve's WebSocket listener)")
	run := flag.String("run", "", "run id to attach to (required)")
	demandFile := flag.String("demand", "", "demand definition (ADR-0012 strict YAML) (required)")
	seed := flag.Uint64("seed", 1, "sampler seed (keys every per-vehicle stream)")
	lead := flag.Uint64("lead", 30, "send verbs this many ticks ahead of their earliest tick")
	flag.Parse()
	if *run == "" || *demandFile == "" {
		fmt.Fprintln(os.Stderr, "demand-director: -run and -demand are required")
		os.Exit(2)
	}

	df, err := scenario.LoadDemandFile(*demandFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demand-director: demand %s: %v\n", *demandFile, err)
		os.Exit(1)
	}

	nc, err := nats.Connect(*url, nats.Name("demand-director"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director: connect:", err)
		os.Exit(1)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director: JetStream:", err)
		os.Exit(1)
	}

	lg := log.New(os.Stderr, "", 0)
	d, err := demand.Attach(nc, js, demand.Config{Run: *run, Seed: *seed, Lead: *lead, Log: lg},
		[]*scenario.DemandFile{df})
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director:", err)
		os.Exit(1)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	d.Close()
}
