// sigctl is the standalone wrapper for the reference actuated signal
// controller (engine/natsio/sigctl — ADR-0037 milestone 2). It runs the
// gap-out control loop against a live run: signal-program table and
// vehicle snapshots off the live plane, detector geometry from the
// scenario's network file, signal_set verbs on the director verb channel.
// serve embeds the same package behind -sigctl, so standalone use is for
// flag-built runs and bring-up poking.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine/natsio/sigctl"
)

func main() {
	url := flag.String("nats", "ws://127.0.0.1:8443", "NATS server URL (serve's WebSocket listener)")
	run := flag.String("run", "", "run id to attach to (required)")
	netfile := flag.String("netfile", "", "network file (network format v1) for detector geometry (required)")
	cadence := flag.Uint64("cadence", 20, "decision cadence, ticks (verbs are issued only when a decision produces one)")
	hold := flag.Uint64("hold", 100, "hold commanded per verb, ticks (must exceed cadence + renew-below so renewals keep the chain contiguous)")
	renewBelow := flag.Uint64("renew-below", 30, "renew the running hold when fewer ticks remain")
	minGreen := flag.Uint64("min-green", 100, "minimum phase age before a switch, ticks")
	radius := flag.Float64("detect-radius", 25, "detector radius around each stop line, meters")
	minQueue := flag.Int("switch-min-queue", 1, "minimum waiting vehicles on a candidate phase's detectors before switching to it")
	programs := flag.String("programs", "", "comma-separated program ids to control (default: every actuatable program)")
	flag.Parse()
	if *run == "" || *netfile == "" {
		fmt.Fprintln(os.Stderr, "sigctl: -run and -netfile are required")
		os.Exit(2)
	}

	geom, err := sigctl.LoadGeom(*netfile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigctl:", err)
		os.Exit(1)
	}
	cfg := sigctl.Config{
		Run: *run, CadenceTicks: *cadence, HoldTicks: *hold,
		RenewBelow: *renewBelow, MinGreenTicks: *minGreen,
		DetectRadiusM: *radius, SwitchMinQueue: *minQueue, Log: log.New(os.Stderr, "", 0),
	}
	if *programs != "" {
		for _, id := range strings.Split(*programs, ",") {
			if id = strings.TrimSpace(id); id != "" {
				cfg.Programs = append(cfg.Programs, id)
			}
		}
	}

	nc, err := nats.Connect(*url, nats.Name("sigctl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigctl: connect:", err)
		os.Exit(1)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigctl: JetStream:", err)
		os.Exit(1)
	}
	ctl, err := sigctl.Attach(nc, js, cfg, geom)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigctl:", err)
		os.Exit(1)
	}

	// Run-over watchdog: snapshots stop when serve finishes, so silence
	// means the run ended — detach and exit rather than wait for a signal
	// that only a foreground operator would send.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			ctl.Close()
			return
		case <-tick.C:
			if last := ctl.LastSnapshot(); !last.IsZero() && time.Since(last) > 15*time.Second {
				ctl.Close()
				return
			}
		}
	}
}
