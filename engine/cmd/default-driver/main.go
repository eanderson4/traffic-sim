// default-driver is the external reference controller (ADR-0008 §5): it
// attaches to a live run over NATS with the drive grant, claims unclaimed
// vehicles up to -capacity, and drives them each tick — IDM + MOBIL
// computed client-side from observations (the kernel's shared policy
// functions), Dijkstra routing for -dest, per-vehicle seeded policy RNG
// (ADR-0007). It also serves the introspection request/reply on
// ts.{run}.drive.introspect.
//
// Deploy it SUPERVISED (restart policy): it is critical infrastructure in
// live runs — if every replica dies, the engine's pause gate stops the run
// (ADR-0008 §6). Run N replicas for failover; they shard emergently via
// exclusive claims (no leader election), sized to absorb one peer loss.
// Replay never runs it: recorded intents come from the JetStream log.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine/natsio/driver"
)

func main() {
	url := flag.String("nats", nats.DefaultURL, "NATS server URL")
	run := flag.String("run", "", "run id to attach to (required)")
	capacity := flag.Int("capacity", 1000, "claim capacity (max vehicles this replica holds)")
	dest := flag.String("dest", "", "destination lane id for the routing axis (optional)")
	intentBatch := flag.String("intent-batch", "on", "aggregate each tick's intents into TSIB batches (on|off; off restores the pre-ADR-0026 per-vehicle v2 stream, for A/B measurement and debugging)")
	flag.Parse()
	if *run == "" {
		fmt.Fprintln(os.Stderr, "default-driver: -run is required")
		os.Exit(2)
	}
	if *intentBatch != "on" && *intentBatch != "off" {
		fmt.Fprintf(os.Stderr, "default-driver: -intent-batch must be on|off, got %q\n", *intentBatch)
		os.Exit(2)
	}

	nc, err := nats.Connect(*url,
		nats.Name("default-driver"),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			// Exit non-zero and let the supervisor restart us: the engine
			// bridges orphans on hold-last and gates the run if capacity
			// stays short (ADR-0008 §6).
			fmt.Fprintf(os.Stderr, "default-driver: disconnected: %v\n", err)
			os.Exit(1)
		}),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "default-driver:", err)
		os.Exit(1)
	}
	js, err := nc.JetStream()
	if err != nil {
		fmt.Fprintln(os.Stderr, "default-driver: JetStream:", err)
		os.Exit(1)
	}

	d, err := driver.New(nc, js, driver.Config{
		Run:            *run,
		Capacity:       *capacity,
		Destination:    *dest,
		IntentBatchOff: *intentBatch == "off",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "default-driver:", err)
		os.Exit(1)
	}
	fmt.Printf("default-driver: attached to run %s as %s (capacity %d)\n", *run, d.ID(), *capacity)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		d.Close() // graceful: release claims, detach
	case <-d.Done():
	}
	nc.Close()
}
