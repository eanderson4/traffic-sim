package natsio

import (
	"encoding/json"
	"fmt"

	"traffic-sim/engine"
)

// verb.go — the director verbs (ADR-0008 §5 director role; scenario-format
// §3 runtime demand director; ADR-0006 2026-07-20 M10 addendum; ADR-0037
// runtime signal control). The control-plane shape follows the existing
// request/reply patterns (hello/claim): a director NEEDS the accept/reject
// answer — it paces its own schedule and must learn of validation
// rejections — so fire-and-forget (like raw intents) was rejected. JSON
// payloads match the control plane's low-rate administrative style (the
// binary frames are for the per-tick hot paths).
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
// the same channel instead of forking subjects per verb. ADR-0037 adds the
// second kind, "signal_set", on the same channel and the same four
// properties (request/reply, request-id idempotency, applied_tick logging,
// verbatim replay re-enqueue).
type VerbRequest struct {
	Verb         string `json:"verb"` // spawn | signal_set
	RequestID    string `json:"request_id"`
	Origin       string `json:"origin"`        // origin lane id (a network origin, unless offset_m > 0)
	VType        string `json:"vtype"`         // scenario vehicle-type name
	EarliestTick uint64 `json:"earliest_tick"` // not-before sim tick
	// Destination is an optional route destination lane id (ADR-0021): the
	// engine applies it as the vehicle's Route axis at injection and ends
	// the trip there. Omitted (the pre-ADR-0021 shape) leaves the vehicle
	// unrouted — every extant recording decodes unchanged.
	Destination string `json:"destination,omitempty"`
	// OffsetM is the injection position along the origin lane in meters.
	// Omitted/zero is portal semantics; positive is the interior-origin
	// opt-in (ADR-0021).
	OffsetM float64 `json:"offset_m,omitempty"`
	// The signal_set fields (ADR-0037), all omitempty: a spawn-only verb is
	// byte-identical to the pre-ADR-0037 shape. Signal is the signal
	// program id (the network file's tlLogic id), HoldTicks the requested
	// hold from the applied tick (0 = one cycle of the program; clamped to
	// the dt-compiled engine.SignalHoldMaxSeconds bound — the starvation
	// rail). Phase is the commanded phase index and is a POINTER because
	// JSON cannot distinguish an omitted phase from phase 0 — and an
	// accidentally omitted one silently commanding phase 0 is exactly the
	// kind of quiet wrong this channel exists to refuse: handleVerb
	// presence-checks it for signal_set. It is ignored by spawn (never
	// serialized there), exactly as the spawn fields are ignored by
	// signal_set.
	Signal    string `json:"signal,omitempty"`
	Phase     *int   `json:"phase,omitempty"`
	HoldTicks uint64 `json:"hold_ticks,omitempty"`
}

// VerbReply is the engine's answer. A duplicate request_id replays the
// original reply with Duplicate set — idempotent success/failure, never a
// second spawn.
//
// The request_id length bound lives in the kernel
// (engine.MaxRequestIDBytes — every enqueue path is guarded); the
// contract layer checks the same constant at the wire boundary so the
// rejection carries the wire-facing reason.
type VerbReply struct {
	Accepted  bool   `json:"accepted"`
	Verb      string `json:"verb"`
	RequestID string `json:"request_id"`
	Duplicate bool   `json:"duplicate,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// loggedVerb is the record-plane payload of a spawn verb (JSON, like
// log.event — verbs are low-rate administrative traffic). TypeIdx is the
// resolved scenario type index (auditability; replay re-resolves
// Origin/VType against the spec, which is deterministic by construction).
// There is no kind discriminator on a spawn record: a recording of
// spawn-only demand is byte-identical to a pre-ADR-0037 one, so replay of
// every existing recording is unaffected.
type loggedVerb struct {
	Tick         uint64 `json:"tick"`
	RequestID    string `json:"request_id"`
	Origin       string `json:"origin"`
	VType        string `json:"vtype"`
	TypeIdx      int    `json:"type_idx"`
	EarliestTick uint64 `json:"earliest_tick"`
	// ADR-0021, both omitempty: a recording of portal-only demand is
	// byte-identical to a pre-ADR-0021 one, so replay of every existing
	// recording is unaffected.
	Destination string  `json:"destination,omitempty"`
	OffsetM     float64 `json:"offset_m,omitempty"`
}

// loggedSignalVerb is the record-plane payload of a signal_set verb
// (ADR-0037). Unlike the spawn shape's optional fields, SignalIdx and
// Phase are ALWAYS encoded and are presence-CHECKED on decode (pointer
// fields): 0 is a legitimate value for both, so an omitempty encoding
// would make a phase-0 record indistinguishable from a malformed
// phase-less one — the record plane keeps the control path's
// presence-checking discipline (round-4). HoldTicks is likewise
// presence-checked (a missing field is a hard decode error) and carries
// the EFFECTIVE hold — defaults applied, starvation-bound- and
// chain-clamped at application — so the record shows exactly what the
// engine enforced. An explicit 0 is legitimate and means the verb was
// accepted but enforced nothing (a renewal declined at the hold chain's
// bound, round-8); the lapse event carries the enforced truth.
type loggedSignalVerb struct {
	Tick      uint64  `json:"tick"`
	RequestID string  `json:"request_id"`
	Verb      string  `json:"verb"` // always "signal_set"
	Signal    string  `json:"signal"`
	SignalIdx *int    `json:"signal_idx"` // explicit presence; 0 is legitimate
	Phase     *int    `json:"phase"`      // explicit presence; 0 is legitimate
	HoldTicks *uint64 `json:"hold_ticks"` // explicit presence; 0 = declined at the chain bound
}

func encodeLoggedVerb(s engine.TickedSpawn) ([]byte, error) {
	return json.Marshal(loggedVerb{
		Tick:         s.Tick,
		RequestID:    s.RequestID,
		Origin:       s.Origin,
		VType:        s.TypeName,
		TypeIdx:      s.TypeIdx,
		EarliestTick: s.EarliestTick,
		Destination:  s.Destination,
		OffsetM:      s.OffsetM,
	})
}

// encodeLoggedSignalVerb is the signal_set twin of encodeLoggedVerb
// (ADR-0037): same subject, same batch position, the discriminator field
// set, signal_idx and phase always present.
func encodeLoggedSignalVerb(s engine.TickedSignal) ([]byte, error) {
	idx, phase, hold := s.SignalIdx, s.Phase, s.HoldTicks
	return json.Marshal(loggedSignalVerb{
		Tick:      s.Tick,
		RequestID: s.RequestID,
		Verb:      "signal_set",
		Signal:    s.Signal,
		SignalIdx: &idx,
		Phase:     &phase,
		HoldTicks: &hold,
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
			Destination:  lv.Destination,
			OffsetM:      lv.OffsetM,
		},
	}, nil
}

// decodeLoggedVerbAny parses one logged verb record and returns it in its
// kernel shape: a signal_set record (ADR-0037) or a spawn record (absent
// discriminator — every pre-ADR-0037 recording). An UNKNOWN discriminator
// is a hard decode error, never a fallback to spawn: a recording written
// by a newer engine carrying a future verb kind must fail replay loudly
// rather than be silently re-enqueued as the wrong command. So is a
// signal_set record missing signal_idx or phase — presence is part of the
// kind's shape (0 is a legitimate value; the pointers exist so the check
// can tell).
func decodeLoggedVerbAny(data []byte) (spawn engine.TickedSpawn, sig engine.TickedSignal, isSignal bool, err error) {
	var probe struct {
		Verb      string `json:"verb"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return engine.TickedSpawn{}, engine.TickedSignal{}, false, err
	}
	switch probe.Verb {
	case "":
		s, err := decodeLoggedVerb(data)
		return s, engine.TickedSignal{}, false, err
	case "signal_set":
		var sv loggedSignalVerb
		if err := json.Unmarshal(data, &sv); err != nil {
			return engine.TickedSpawn{}, engine.TickedSignal{}, false, err
		}
		if sv.SignalIdx == nil || sv.Phase == nil {
			return engine.TickedSpawn{}, engine.TickedSignal{}, false, fmt.Errorf(
				"logged signal verb %q: missing signal_idx or phase (both are mandatory and explicit for signal_set records)", sv.RequestID)
		}
		// hold_ticks is presence-checked too: the encoder always writes the
		// EFFECTIVE hold, so a missing field is a malformed record — but an
		// EXPLICIT 0 is legitimate (a renewal declined at the chain bound
		// enforced nothing, round-8) and decodes through.
		if sv.HoldTicks == nil {
			return engine.TickedSpawn{}, engine.TickedSignal{}, false, fmt.Errorf(
				"logged signal verb %q: missing hold_ticks (mandatory for signal_set records; an explicit 0 marks a declined renewal)", sv.RequestID)
		}
		return engine.TickedSpawn{}, engine.TickedSignal{
			Tick:      sv.Tick,
			SignalIdx: *sv.SignalIdx,
			SignalDirective: engine.SignalDirective{
				RequestID: sv.RequestID,
				Signal:    sv.Signal,
				Phase:     *sv.Phase,
				HoldTicks: *sv.HoldTicks,
			},
		}, true, nil
	default:
		return engine.TickedSpawn{}, engine.TickedSignal{}, false, fmt.Errorf(
			"logged verb %q: unknown verb kind %q (record written by a newer engine?)", probe.RequestID, probe.Verb)
	}
}
