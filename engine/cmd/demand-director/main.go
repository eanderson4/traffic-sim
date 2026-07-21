// demand-director is the M10 reference runtime demand director
// (scenario-format §3, ADR-0008 §5): it reads a small demand definition —
// flows with constant or Poisson spacing, piecewise-constant time slices in
// SIM SECONDS, a vehicle-type mix per flow — samples arrivals with
// per-vehicle keyed RNG (ADR-0005/ADR-0007 discipline: the sampler's draws
// come from streams derived from (seed, flow, vehicle ordinal), never a
// process-global source), and issues spawn verbs on
// ts.{run}.ctl.verb.{controller_id} (request/reply). The engine validates
// and injects through its own deterministic path; the verbs are recorded
// on the record plane, so replay never re-runs this sampler.
//
// The whole arrival program is a pure function of (demand file, seed), and
// request ids are deterministic (f{flow}-{ordinal}), so a restarted
// director re-issues the identical program and the engine's request-id
// dedup makes the overlap harmless (failover-invisible).
//
// Live mode only. Verbs are sent paced by snapshot ticks with a small lead
// (the engine's hold-and-retry queue absorbs early arrivals up to their
// earliest_tick); the engine — never the director — decides what actually
// enters the network.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// DemandFile is the strict JSON demand definition (stdlib encoding/json —
// no YAML dependency, AGENTS.md stdlib-first; the format is deliberately
// small: flows, spacing, slices, type weights).
type DemandFile struct {
	Flows []Flow `json:"flows"`
}

// Flow is one origin's demand program. Times are SIM SECONDS (never wall
// clock, ADR-0005). With Slices, the rate is piecewise-constant (0 outside
// any slice); without, VehPerH holds for the whole run.
type Flow struct {
	Origin  string             `json:"origin"`            // origin lane id
	VehPerH float64            `json:"veh_per_h"`         // base rate (ignored when slices present)
	Spacing string             `json:"spacing"`           // "poisson" | "constant"
	VTypes  map[string]float64 `json:"vtypes"`            // type name → weight
	Slices  []Slice            `json:"slices,omitempty"`  // piecewise-constant overrides
	UntilS  float64            `json:"until_s,omitempty"` // stop sampling after t (0 = run end)
}

// Slice is one piecewise-constant demand window [StartS, EndS) in sim
// seconds.
type Slice struct {
	StartS  float64 `json:"start_s"`
	EndS    float64 `json:"end_s"`
	VehPerH float64 `json:"veh_per_h"`
}

func (f *Flow) rateAt(tS float64) float64 {
	if len(f.Slices) == 0 {
		return f.VehPerH
	}
	for _, s := range f.Slices {
		if tS >= s.StartS && tS < s.EndS {
			return s.VehPerH
		}
	}
	return 0
}

// flowSampler walks one flow's arrival program. All draws come from the
// per-vehicle stream keyed (seed, flowKey, ordinal) — the same stream
// supplies the type-mix draw and (for Poisson) the gap draw, mirroring the
// kernel Spawner's "jitter from the incoming vehicle's own stream" rule.
type flowSampler struct {
	flow    Flow
	key     uint64
	ordinal uint64
	nextS   float64 // next arrival, sim seconds
}

func newFlowSampler(f Flow, seed uint64, idx int) *flowSampler {
	h := fnv.New64a()
	h.Write([]byte("traffic-sim/demand-director/flow"))
	h.Write([]byte(f.Origin))
	var b [8]byte
	b[0] = byte(idx)
	h.Write(b[:])
	return &flowSampler{flow: f, key: h.Sum64()}
}

// stream returns the ordinal's keyed stream (derived fresh: samplers are
// stateless apart from the schedule, so a restart re-derives identically).
func (fs *flowSampler) stream() *engine.Stream {
	return engine.DeriveStream(seedGlobal, fs.key^fs.ordinal)
}

// next returns the next (arrival sim-seconds, vtype) and advances. ok=false
// when the program is exhausted (past until_s, or a zero rate with slices).
func (fs *flowSampler) next(dt float64) (atS float64, vtype string, ok bool) {
	f := fs.flow
	if f.UntilS > 0 && fs.nextS >= f.UntilS {
		return 0, "", false
	}
	rate := f.rateAt(fs.nextS)
	if rate <= 0 {
		if len(f.Slices) == 0 {
			return 0, "", false
		}
		// Jump to the next slice window.
		best := math.Inf(1)
		for _, s := range f.Slices {
			if s.EndS > fs.nextS && s.StartS < best {
				best = math.Max(s.StartS, fs.nextS)
			}
		}
		if math.IsInf(best, 1) {
			return 0, "", false
		}
		fs.nextS = best
		rate = f.rateAt(fs.nextS)
		if rate <= 0 {
			return 0, "", false
		}
	}
	st := fs.stream()
	gapS := 3600 / rate
	if f.Spacing == "poisson" {
		// Exponential gaps (SUMO period="exp(X)"): -ln(1-u)·mean.
		gapS = -math.Log(1-st.Float64()) * gapS
	}
	vtype = pickType(st, f.VTypes)
	at := fs.nextS
	fs.ordinal++
	fs.nextS = at + gapS
	return at, vtype, true
}

// pickType draws the vehicle type from the per-vehicle stream, weights
// remapping one uniform draw (the kernel Spawner's convention). Weight
// iteration is over a sorted key list — Go map order is forbidden anywhere
// near a sampled path.
func pickType(st *engine.Stream, weights map[string]float64) string {
	if len(weights) == 0 {
		return "car"
	}
	names := make([]string, 0, len(weights))
	tot := 0.0
	for n, w := range weights {
		names = append(names, n)
		tot += w
	}
	sort.Strings(names)
	u := st.Float64() * tot
	cum := 0.0
	for _, n := range names {
		cum += weights[n]
		if u < cum {
			return n
		}
	}
	return names[len(names)-1]
}

var seedGlobal uint64

func main() {
	url := flag.String("nats", "ws://127.0.0.1:8443", "NATS server URL (serve's WebSocket listener)")
	run := flag.String("run", "", "run id to attach to (required)")
	demand := flag.String("demand", "", "demand definition JSON (required)")
	seed := flag.Uint64("seed", 1, "sampler seed (keys every per-vehicle stream)")
	lead := flag.Uint64("lead", 30, "send verbs this many ticks ahead of their earliest tick")
	flag.Parse()
	if *run == "" || *demand == "" {
		fmt.Fprintln(os.Stderr, "demand-director: -run and -demand are required")
		os.Exit(2)
	}
	seedGlobal = *seed

	data, err := os.ReadFile(*demand)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director:", err)
		os.Exit(1)
	}
	var df DemandFile
	if err := json.Unmarshal(data, &df); err != nil {
		fmt.Fprintf(os.Stderr, "demand-director: demand %s: %v\n", *demand, err)
		os.Exit(1)
	}
	if len(df.Flows) == 0 {
		fmt.Fprintln(os.Stderr, "demand-director: demand has no flows")
		os.Exit(2)
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

	// The run spec supplies the tick length (sim seconds ↔ ticks) and the
	// run's tick budget.
	reg, err := natsio.NewRegistry(js)
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director: registry:", err)
		os.Exit(1)
	}
	var meta *natsio.RunMeta
	for i := 0; i < 50; i++ {
		meta, err = reg.Meta(*run)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director: run meta:", err)
		os.Exit(1)
	}
	dt := meta.Spec.Params.Dt
	endS := float64(meta.Spec.Ticks) * dt

	// Attach with the director grant (ADR-0008 §5).
	hello, _ := json.Marshal(natsio.HelloRequest{
		ContractVersion: natsio.SchemaVersion, ControllerType: "director",
		CadenceTicks: 1, Grants: []string{"director"},
	})
	var ctlID string
	for i := 0; i < 50; i++ {
		msg, err := nc.Request(natsio.SubjectCtlHello(*run), hello, 300*time.Millisecond)
		if err == nil {
			var rep natsio.HelloReply
			if json.Unmarshal(msg.Data, &rep) == nil && rep.Accepted {
				ctlID = rep.ControllerID
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ctlID == "" {
		fmt.Fprintln(os.Stderr, "demand-director: attach rejected or timed out")
		os.Exit(1)
	}
	fmt.Printf("demand-director: attached as %s (director grant) to run %q (dt=%.3fs, %d ticks)\n",
		ctlID, *run, dt, meta.Spec.Ticks)

	samplers := make([]*flowSampler, len(df.Flows))
	pending := make([]struct {
		at    float64
		vtype string
	}, len(df.Flows))
	exhausted := make([]bool, len(df.Flows))
	for i, f := range df.Flows {
		samplers[i] = newFlowSampler(f, *seed, i)
		pending[i].at, pending[i].vtype, _ = samplers[i].next(dt)
	}

	var sent, accepted, rejected, claims, unclaimed int
	// Evidence taps: claim/unclaimed events prove director-spawned vehicles
	// join the default driver's fleet.
	_, _ = nc.Subscribe(natsio.SubjectEventClaim(*run), func(m *nats.Msg) {
		var evt natsio.ContractEvent
		if json.Unmarshal(m.Data, &evt) == nil {
			claims += len(evt.VehicleIDs)
		}
	})
	_, _ = nc.Subscribe(natsio.SubjectEventUnclaimed(*run), func(m *nats.Msg) {
		var evt natsio.ContractEvent
		if json.Unmarshal(m.Data, &evt) == nil && evt.Reason == natsio.ReasonSpawn {
			unclaimed += len(evt.VehicleIDs)
		}
	})

	send := func(flowIdx int) {
		fs := samplers[flowIdx]
		atTick := uint64(pending[flowIdx].at/dt + 0.5)
		req, _ := json.Marshal(natsio.VerbRequest{
			Verb:         "spawn",
			RequestID:    fmt.Sprintf("f%d-%06d", flowIdx, fs.ordinal-1),
			Origin:       fs.flow.Origin,
			VType:        pending[flowIdx].vtype,
			EarliestTick: atTick,
		})
		sent++
		msg, err := nc.Request(natsio.SubjectCtlVerb(*run, ctlID), req, 2*time.Second)
		if err != nil {
			fmt.Printf("  verb f%d-%06d (%s@%s tick %d): NO REPLY: %v\n",
				flowIdx, fs.ordinal-1, pending[flowIdx].vtype, fs.flow.Origin, atTick, err)
			return
		}
		var rep natsio.VerbReply
		if json.Unmarshal(msg.Data, &rep) == nil && rep.Accepted {
			accepted++
			if rep.Duplicate {
				fmt.Printf("  verb %s: duplicate (already applied — restart overlap)\n", rep.RequestID)
			}
		} else {
			rejected++
			fmt.Printf("  verb f%d-%06d REJECTED: %s\n", flowIdx, fs.ordinal-1, msg.Data)
		}
		pending[flowIdx].at, pending[flowIdx].vtype, _ = fs.next(dt)
		if pending[flowIdx].at == 0 && fs.nextS == 0 {
			exhausted[flowIdx] = true
		}
	}

	// Pace verbs off snapshot ticks: send when the run is within -lead
	// ticks of the sampled arrival's tick.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	// Heartbeat on the snapshot rhythm: the liveness sweep detaches any
	// controller silent for DetachAfterTicks (10), and a Poisson flow's
	// inter-verb gaps are far longer — heartbeats keep the attachment
	// alive between verbs (their purpose; ADR-0006 M4 addendum).
	hb := natsio.SubjectCtlHeartbeat(*run, ctlID)
	sub, err := nc.Subscribe(natsio.SubjectStateSnap(*run), func(m *nats.Msg) {
		f, err := natsio.ParseFrame(m.Data)
		if err != nil {
			return
		}
		_ = nc.Publish(hb, nil)
		for i := range samplers {
			if exhausted[i] {
				continue
			}
			at := pending[i].at
			if at > endS {
				exhausted[i] = true
				continue
			}
			atTick := uint64(at/dt + 0.5)
			if f.Tick+*lead >= atTick {
				send(i)
			}
		}
		if f.Tick%200 == 0 {
			fmt.Printf("  tick %d: vehicles=%d verbs=%d accepted=%d rejected=%d claimed=%d\n",
				f.Tick, len(f.Vehicles), sent, accepted, rejected, claims)
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "demand-director: subscribe:", err)
		os.Exit(1)
	}
	defer func() { _ = sub.Unsubscribe() }()

	<-stop
	fmt.Printf("demand-director: done — verbs=%d accepted=%d rejected=%d spawn-announcements=%d claims=%d\n",
		sent, accepted, rejected, unclaimed, claims)
}
