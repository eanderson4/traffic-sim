package natsio

import (
	"encoding/json"

	"traffic-sim/engine"
)

// verb.go — the director spawn verb (ADR-0008 §5 director role;
// scenario-format §3 runtime demand director; ADR-0006 2026-07-20 M10
// addendum). The control-plane shape follows the existing request/reply
// patterns (hello/claim): a director NEEDS the accept/reject answer — it
// paces its own schedule and must learn of validation rejections — so
// fire-and-forget (like raw intents) was rejected. JSON payloads match the
// control plane's low-rate administrative style (the binary frames are for
// the per-tick hot paths).
//
// Idempotency: the director assigns every verb a unique request_id; the
// engine remembers the reply per request_id for the run's lifetime, so a
// retried verb (client reconnect, publish retry) gets the same answer and
// never double-spawns. Only first-seen, accepted verbs reach the kernel's
// injection queue and the record plane.
//
// Record/replay: accepted verbs are stamped with their applied_tick by the
// kernel and logged on ts.{run}.log.verb in the tick's record batch (after
// intents, before keyframe/CRC). Replay re-enqueues them at their recorded
// ticks; the kernel's deterministic hold-and-retry injection queue then
// reproduces the run bit-identically — the demand sampler never re-runs.

// VerbRequest is the payload of ts.{run}.ctl.verb.{controller_id}. v1
// implements verb "spawn" only; the field exists so despawn/teleport/
// trigger (ADR-0008 §5's remaining OpenSCENARIO verbs) land additively on
// the same channel instead of forking subjects per verb.
type VerbRequest struct {
	Verb         string `json:"verb"` // spawn
	RequestID    string `json:"request_id"`
	Origin       string `json:"origin"`        // origin lane id (must be a network origin)
	VType        string `json:"vtype"`         // scenario vehicle-type name
	EarliestTick uint64 `json:"earliest_tick"` // not-before sim tick
}

// VerbReply is the engine's answer. A duplicate request_id replays the
// original reply with Duplicate set — idempotent success/failure, never a
// second spawn.
type VerbReply struct {
	Accepted  bool   `json:"accepted"`
	Verb      string `json:"verb"`
	RequestID string `json:"request_id"`
	Duplicate bool   `json:"duplicate,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// loggedVerb is the record-plane payload (JSON, like log.event — verbs are
// low-rate administrative traffic). TypeIdx is the resolved scenario type
// index (auditability; replay re-resolves Origin/VType against the spec,
// which is deterministic by construction).
type loggedVerb struct {
	Tick         uint64 `json:"tick"`
	RequestID    string `json:"request_id"`
	Origin       string `json:"origin"`
	VType        string `json:"vtype"`
	TypeIdx      int    `json:"type_idx"`
	EarliestTick uint64 `json:"earliest_tick"`
}

func encodeLoggedVerb(s engine.TickedSpawn) ([]byte, error) {
	return json.Marshal(loggedVerb{
		Tick:         s.Tick,
		RequestID:    s.RequestID,
		Origin:       s.Origin,
		VType:        s.TypeName,
		TypeIdx:      s.TypeIdx,
		EarliestTick: s.EarliestTick,
	})
}

func decodeLoggedVerb(data []byte) (engine.TickedSpawn, error) {
	var lv loggedVerb
	if err := json.Unmarshal(data, &lv); err != nil {
		return engine.TickedSpawn{}, err
	}
	return engine.TickedSpawn{
		Tick:    lv.Tick,
		TypeIdx: lv.TypeIdx,
		SpawnDirective: engine.SpawnDirective{
			RequestID:    lv.RequestID,
			Origin:       lv.Origin,
			TypeName:     lv.VType,
			EarliestTick: lv.EarliestTick,
		},
	}, nil
}
