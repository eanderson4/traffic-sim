package engine

import "fmt"

// RunSpec is everything a run consumes: network, scenario, params, seed, and
// tick count. M1 has no external controller intents, so the spec fully
// determines the run.
type RunSpec struct {
	Net    NetSpec
	Scen   Scenario
	Params Params
	Seed   uint64
	Ticks  uint64
}

// RunLog is the in-memory intent/event log of ADR-0005's replay model: the
// full run spec, the arbitrated intent log in application order, and the
// per-tick CRC sequence the run produced. (JetStream persistence and
// keyframes map this exact structure onto the record plane — engine/natsio;
// the verification semantics are identical in memory.)
type RunLog struct {
	Spec    RunSpec
	Intents []TickedIntent // applied intents, chronological (Tick ascending)
	Spawns  []TickedSpawn  // accepted director directives, chronological
	CRCs    []uint64
}

// Run executes a fresh run from a spec and returns the engine (final state)
// and its log.
func Run(spec RunSpec) (*Engine, *RunLog, error) {
	e, err := NewEngine(spec)
	if err != nil {
		return nil, nil, err
	}
	for e.Tick < spec.Ticks {
		e.Step()
	}
	return e, &RunLog{Spec: spec, Intents: e.IntentLog, Spawns: e.SpawnLog, CRCs: e.CRCs}, nil
}

// Replay re-executes a recorded run from its spec and arbitrated intent log
// and verifies that the per-tick CRC sequence reproduces exactly — ADR-0005's
// determinism envelope: same binary + same log ⇒ identical states,
// CRC-verified. Controllers never run during replay (ADR-0008): recorded
// intents come from the log, re-queued at their applied_tick.
func Replay(log *RunLog) (*RunLog, error) {
	e, err := NewEngine(log.Spec)
	if err != nil {
		return nil, err
	}
	ii, si := 0, 0
	for e.Tick < log.Spec.Ticks {
		for ii < len(log.Intents) && log.Intents[ii].Tick <= e.Tick+1 {
			if log.Intents[ii].Tick == e.Tick+1 {
				e.EnqueueIntent(log.Intents[ii].KeyedIntent)
			}
			ii++
		}
		for si < len(log.Spawns) && log.Spawns[si].Tick <= e.Tick+1 {
			if log.Spawns[si].Tick == e.Tick+1 {
				if err := e.EnqueueSpawn(log.Spawns[si].SpawnDirective); err != nil {
					return nil, fmt.Errorf("replay spawn %q at tick %d: %w",
						log.Spawns[si].RequestID, log.Spawns[si].Tick, err)
				}
			}
			si++
		}
		e.Step()
	}
	relog := &RunLog{Spec: log.Spec, Intents: e.IntentLog, Spawns: e.SpawnLog, CRCs: e.CRCs}
	if len(relog.CRCs) != len(log.CRCs) {
		return nil, fmt.Errorf("replay produced %d CRCs, log has %d", len(relog.CRCs), len(log.CRCs))
	}
	for i := range log.CRCs {
		if relog.CRCs[i] != log.CRCs[i] {
			return nil, fmt.Errorf("replay divergence at tick %d: crc %016x, want %016x",
				i+1, relog.CRCs[i], log.CRCs[i])
		}
	}
	return relog, nil
}

// DefaultSpec returns the canonical spec for a named network — the CLI
// harness and the tests share exactly these.
func DefaultSpec(netKind string, ticks, seed uint64) (RunSpec, error) {
	spec := RunSpec{Params: DefaultParams(), Seed: seed, Ticks: ticks}
	switch netKind {
	case "ring":
		spec.Net = NetSpec{Kind: "ring"} // 230 m, 30 km/h
		spec.Scen = Scenario{InitialVehicles: 22}
	case "lanedrop":
		spec.Net = NetSpec{Kind: "lanedrop"} // 600 m × (3 lanes) + 600 m × (2 lanes)
		spec.Scen = Scenario{SpawnRatePerLaneHour: 1500, DensityTargetPerKm: 80}
	case "straight":
		spec.Net = NetSpec{Kind: "straight"} // 5 km single lane, exit at the end
	case "i80":
		spec.Net = NetSpec{Kind: "i80"} // NGSIM I-80 17:00–17:15 credibility scenario
		spec.Scen = I80Scenario()
	default:
		return RunSpec{}, fmt.Errorf("unknown network %q (want ring|lanedrop|straight|i80)", netKind)
	}
	return spec, nil
}
