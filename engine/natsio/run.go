package natsio

import (
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// run.go — the live run loop: engine kernel + live plane + contract plane +
// record plane + run registry, in the ADR-0005 three-phase order extended
// for the network and the ADR-0008 controller contract:
//
//	process contract requests (hello/claim/release/heartbeat, liveness,
//	resume check) → [pause gate: dead wall-clock time, no ticks] → drain
//	intents through the contract (claim filter, sequencing, hold-last) →
//	kernel Step (events, intents, sweep, CRC) → publish snapshot +
//	applied_tick echoes (core, fire-and-forget) → observations, unclaimed
//	announcements, pause-gate evaluation → write the tick's record
//	(JetStream, pubacks awaited per tick-batch — a failed log write aborts
//	the run loudly) → refresh the late-joiner pointer at keyframe cadence.
//
// The loop is unpaced (batch driver, ADR-0005 §4); pacing is a wrapper's
// business. The tick NEVER blocks on subscribers — only on the record
// plane's per-batch puback, which is the durability contract. While paused
// (ADR-0008 §6) the loop keeps serving the contract plane so a restarting
// driver can attach and resume the run. An optional start gate
// (ContractConfig.StartGate) extends the same idea to BEFORE tick 0: the
// loop parks — contract plane served, sim time frozen — until embedded
// clients have attached, so no early tick outruns an attaching client.

// pausePoll is the wall-clock sleep between contract rounds while the tick
// loop is gated. Sim time is frozen; this is pure scheduling hygiene.
const pausePoll = 5 * time.Millisecond

// signalCatchUpEvery is the live-plane signal-table republish cadence in
// ticks (shared with the replay player). The M9 addendum rode the keyframe
// cadence, but default KeyframeEvery=100 leaves a browser that attaches
// just after the run-start publish waiting up to 10 s at 1× pace before
// any signal head appears; the table is a few hundred bytes, so a 20-tick
// cadence (≤ 2 s at 1×) costs nothing measurable and keeps heads prompt.
const signalCatchUpEvery = 20

// RunObserver is an optional read-only observer on the live run loop — the
// ADR-0014 §1 metric-kernel pattern (the XTField precedent, in-process and
// caller-driven, never the snapshot plane). Attach runs once right after
// engine construction at tick 0 (observers seeding from initial state must
// run there); Observe runs once per Step, immediately after, on the
// run-loop goroutine. Neither may mutate engine state.
type RunObserver interface {
	Attach(e *engine.Engine) error
	Observe(e *engine.Engine)
}

// LiveRun is the result of a completed (or aborted) live run.
type LiveRun struct {
	Engine   *engine.Engine
	Bus      *Bus
	Contract *Contract
	Recorder *Recorder
	Registry *Registry
}

// RunLive executes a run over NATS: registry → recorder → bus → contract →
// loop. It owns e (the run-loop goroutine must be the only one Stepping it)
// and leaves the record plane exactly reflecting what the engine applied.
// On recorder failure the run aborts loudly: the error is returned and the
// registry is marked aborted.
//
// Live runs require the holdlast uncontrolled-policy (ADR-0008
// clarification: the idm harness policy exists only where no bus is
// attached); an empty policy is upgraded, an explicit idm is refused.
func RunLive(nc *nats.Conn, js nats.JetStreamContext, run string, spec engine.RunSpec, recCfg RecorderConfig, contractCfgs ...ContractConfig) (*LiveRun, error) {
	// Live runs occupy the live/record namespace; the -replay suffix is
	// reserved for replay planes (player.go), so refuse it before anything
	// is built or recorded.
	if err := validRunID(run); err != nil {
		return nil, err
	}
	switch spec.Scen.UncontrolledPolicy {
	case "":
		spec.Scen.UncontrolledPolicy = engine.PolicyHoldLast
	case engine.PolicyHoldLast:
	case engine.PolicyIDM:
		return nil, fmt.Errorf("live runs require uncontrolled_policy %q (the idm harness exists only where no bus is attached)", engine.PolicyHoldLast)
	default:
		return nil, fmt.Errorf("unknown uncontrolled_policy %q", spec.Scen.UncontrolledPolicy)
	}
	contractCfg := ContractConfig{}
	if len(contractCfgs) > 0 {
		contractCfg = contractCfgs[0]
	}

	// Warm start (ADR-0029 phase 1): build from a saved state instead of an
	// empty network at tick 0. RestoreStateChecked runs the sidecar's
	// network fingerprint against the network this spec builds BEFORE any
	// vehicle is placed, so a state taken under a different lane array fails
	// here rather than producing a plausible, wrong run.
	var e *engine.Engine
	var err error
	if contractCfg.InitialState != nil {
		e, err = engine.RestoreStateChecked(spec, contractCfg.InitialState, contractCfg.InitialStateMeta)
		if err != nil {
			return nil, err
		}
		if e.Tick >= spec.Ticks {
			return nil, fmt.Errorf("warm start: saved state is at tick %d and the run is %d ticks — nothing left to simulate", e.Tick, spec.Ticks)
		}
	} else {
		e, err = engine.NewEngine(spec)
		if err != nil {
			return nil, err
		}
	}
	// State dump: fires once, when the loop reaches the requested tick.
	// Refuse a tick the loop will never reach — a dump that silently never
	// happens is exactly as bad as one that fails, and costs a whole run to
	// discover.
	dumped := contractCfg.StateDumpPath == ""
	if !dumped {
		if contractCfg.StateDumpTick < e.Tick || contractCfg.StateDumpTick > spec.Ticks {
			return nil, fmt.Errorf("state dump at tick %d is outside this run's %d..%d — it would never fire",
				contractCfg.StateDumpTick, e.Tick, spec.Ticks)
		}
	}
	dumpState := func() error {
		if dumped || e.Tick != contractCfg.StateDumpTick {
			return nil
		}
		if err := engine.SaveState(contractCfg.StateDumpPath, e, spec, run); err != nil {
			return fmt.Errorf("state dump at tick %d: %w", e.Tick, err)
		}
		dumped = true
		return nil
	}
	// Retention opt-out before the first Step, so nothing is accumulated.
	// LiveRun.Log's Intents are empty when set — the durable JetStream
	// record is the log for these runs (ADR-0024).
	e.DropIntentLog = recCfg.DropEngineIntentLog
	if contractCfg.Observer != nil {
		if err := contractCfg.Observer.Attach(e); err != nil {
			return nil, err
		}
	}
	reg, err := NewRegistry(js)
	if err != nil {
		return nil, err
	}
	// e.Tick, not 0: on a warm start the run begins at the restored tick,
	// and the registry is the metadata plane other services read.
	if err := reg.Start(run, spec, e.Tick); err != nil {
		return nil, fmt.Errorf("registry start: %w", err)
	}
	lr := &LiveRun{Engine: e, Registry: reg}
	finish := func(runErr error) (*LiveRun, error) {
		if err := reg.Finish(run, runErr); err != nil && runErr == nil {
			runErr = fmt.Errorf("registry finish: %w", err)
		}
		return lr, runErr
	}

	rec, err := NewRecorder(js, run, recCfg)
	if err != nil {
		return finish(err)
	}
	lr.Recorder = rec
	bus, err := NewBus(nc, run, e)
	if err != nil {
		return finish(err)
	}
	lr.Bus = bus
	defer bus.Close()
	contract, err := NewContract(nc, run, contractCfg, bus, rec)
	if err != nil {
		return finish(err)
	}
	lr.Contract = contract
	defer contract.Close()

	// Anchor the record with the tick-0 keyframe (seek semantics: any
	// target ≥ 0 then has a keyframe ≤ target).
	if err := rec.LogStart(e); err != nil {
		return finish(err)
	}
	// Publish the signal-program table at run start (ADR-0006 M9 addendum);
	// the loop republishes it at the signalCatchUpEvery cadence for late
	// joiners.
	bus.PublishSignals(e)

	// A dump requested AT the starting tick (tick 0, or a warm start's own
	// tick) has no Step to hang off — take it here.
	if err := dumpState(); err != nil {
		return finish(err)
	}

	// Client-attach barrier: with a start gate configured, hold tick 0
	// until it opens (serve closes it once the embedded driver/demand
	// director report attached and ready). The contract plane keeps being
	// served so the attach handshakes the barrier waits on can complete;
	// sim time is frozen — dead wall-clock time, like the pause gate.
	for gate := contractCfg.StartGate; gate != nil; {
		if err := contract.ProcessControl(e); err != nil {
			return finish(err)
		}
		select {
		case <-gate:
			gate = nil
		case <-time.After(pausePoll):
		}
	}

	for e.Tick < spec.Ticks {
		tickStart := time.Now()
		if err := contract.ProcessControl(e); err != nil {
			return finish(err)
		}
		if contract.Paused() {
			time.Sleep(pausePoll)
			continue
		}
		for _, k := range contract.DrainIntents(e) {
			e.EnqueueIntent(k)
		}
		e.Step()
		if contractCfg.Observer != nil {
			contractCfg.Observer.Observe(e)
		}
		bus.PublishSnapshot(e)
		bus.PublishAcks(e.AppliedIntents(), e.Tick)
		if err := contract.AfterStep(e); err != nil {
			return finish(err)
		}
		if err := rec.LogTick(e); err != nil {
			return finish(err)
		}
		// After the tick is recorded, so the dumped tick is also a
		// recorded one; the run carries on either way.
		if err := dumpState(); err != nil {
			return finish(err)
		}
		if e.Tick%signalCatchUpEvery == 0 {
			// Signal-table catch-up for late joiners (ADR-0006 §6 + M9
			// addendum) — a shorter cadence than the keyframe rhythm so
			// heads appear promptly after a browser attaches.
			bus.PublishSignals(e)
		}
		if e.Tick%rec.cfg.KeyframeEvery == 0 {
			if err := reg.UpdateState(run, e.Tick, rec.lastSeq, e.CRC()); err != nil {
				return finish(fmt.Errorf("registry state update at tick %d: %w", e.Tick, err))
			}
		}
		// Pacing (ADR-0005 §4): do not outrun clients — sleep the remainder
		// of the pace floor. Never a barrier on controller input; intents
		// apply whenever they arrive and hold-last heals gaps.
		if floor := contract.PaceFloor(); floor > 0 {
			if d := time.Since(tickStart); d < floor {
				time.Sleep(floor - d)
			}
		}
	}
	return finish(nil)
}
