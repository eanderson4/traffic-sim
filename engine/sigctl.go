package engine

import (
	"fmt"
	"math"
)

// sigctl.go — runtime signal control (ADR-0037, milestone 1): the kernel
// side of the signal_set director verb. A verb commands one signal program
// onto one of its compiled phases for a bounded hold; while the hold is in
// force the program's per-approach state derives from the COMMANDED phase
// instead of the fixed-time schedule (sigPhaseAt, signal.go), and when the
// hold lapses the fixed-time derivation resumes — the underlying program
// never stopped being a pure function of the tick, so the resume point
// needs no state of its own.
//
// The channel properties come from the verb machinery (engine/natsio
// verb.go), not from anything here: request/reply, request-id idempotency,
// applied_tick stamping, logging on ts.{run}.log.verb, and verbatim
// re-enqueue by replay. What the kernel adds is the part that makes this
// more than "a verb": the starvation rails. An unrailed phase command can
// starve a movement for good — a red that never lifts — and ADR-0034's
// gridlock escape deliberately does not rescue that case (a vehicle held
// at a red stop line is not box-blocked, so it never strands). Every
// command therefore carries a maximum hold: the default is one cycle of
// the commanded program — the natural bounded quantum of that program's
// own making — and an explicit hold is clamped to the dt-compiled
// SignalHoldMaxSeconds bound (signalHoldMaxTicks). The bound is CUMULATIVE
// across a hold CHAIN: an uninterrupted run of same-phase holds on one
// program (each superseding the last with no fixed-time gap) is a
// controller renewing its command, not a new one, so the chain's
// effective end clamps to first-hold-start + bound — otherwise a client
// loop re-commanding the same phase every N ticks would starve a movement
// forever and the advertised bound would never land. A supersede to a
// DIFFERENT phase, or any hold after a fixed-time gap, starts a new chain
// (a real control action, and an interrupted starvation, respectively).
// When a chain reaches its bound the hold LAPSES: enforcement returns to
// the fixed-time program at whatever phase the schedule is in at the
// lapse tick — note that one cycle after the command began the schedule
// is back at the phase that was active WHEN THE COMMAND BEGAN, which is
// not necessarily the COMMANDED phase, so a lapse can jump from the held
// phase to an unrelated schedule phase; the clearance retention below
// covers the transition — and exactly one lapse event fires (SigLapses),
// whether the chain ended by expiry or by a renewal arriving at the bound
// (a renewal at the bound extends nothing and is not installed). Never a
// silent fallback.
//
// Determinism (ADR-0005): the override table is keyed lookup only — never
// iterated; every per-tick use (state derivation, lapse detection,
// retention sweep, the CRC fold) walks the network's program list in
// network order, and the effect of a verb is fixed by its recorded
// applied_tick. The table is behavior-deciding state, so it is keyframed
// (TSKF v7) and folded into the rolling CRC — both CONDITIONAL on the
// table being non-empty, so a run that never receives a signal verb keeps
// the pre-ADR-0037 byte stream exactly.
//
// Enforcement is untouched by design: sigGate, the clearance window, the
// permissive-yield model, and the ADR-0010 box checks all read the state
// through the same three predicates as before. A controller can hold red
// or extend green; it can never make green mean "enter a box you cannot
// exit".

// SignalHoldMaxSeconds bounds the hold a signal_set verb may command, in
// SIM seconds: 300, the gridlock escape's own horizon (Params.StrandAfterS
// default) — a hold past it would starve a movement for longer than the
// model will stay silently stopped anywhere else. A verb asking for more
// is CLAMPED to the bound, not rejected — the rail exists to bound the
// blast radius of a wedged controller, and a clamp applies the bound
// automatically where a rejection would depend on the controller reading
// its reply. The record plane logs the EFFECTIVE hold in ticks, so what
// was applied is on the record (asyncapi LoggedVerbView).
const SignalHoldMaxSeconds = 300.0

// signalHoldMaxTicks compiles the hold bound onto the run's tick grid.
// dt is a scenario parameter (ADR-0005), never a constant baked into a
// tick count: at a non-default dt a fixed 3000-tick bound would no longer
// be 300 sim-seconds. Same dt-derived-constant idiom as routeEpochTicks
// (engine.go initAdaptiveRouting); computed per call — it is one division
// on a verb path that fires a handful of times per run.
func (e *Engine) signalHoldMaxTicks() uint64 {
	dt := e.Params.Dt
	if dt <= 0 { // validated upstream; never divide by a bad tick
		dt = 0.1
	}
	return max(1, uint64(math.Round(SignalHoldMaxSeconds/dt)))
}

// SignalDirective is the kernel form of a signal_set verb: which program,
// which of its phases to hold, and for how long. RequestID is the
// director-assigned idempotency key (dedup lives in the contract layer;
// the kernel carries it for the record, exactly like SpawnDirective).
// HoldTicks is relative to the applied tick — a duration, not an absolute
// tick, so a controller needs no clock agreement with the run; 0 asks for
// the default (one cycle of the commanded program). EnqueueSignal stores
// the EFFECTIVE hold (defaults applied, clamped to the dt-compiled
// SignalHoldMaxSeconds bound), so the buffered directive and the record
// are already the bounded command.
type SignalDirective struct {
	RequestID string // director-assigned idempotency key
	Signal    string // signal program id (the network file's tlLogic id)
	Phase     int    // commanded phase index into the program's phase list
	HoldTicks uint64 // requested hold from the applied tick; 0 = one program cycle
}

// TickedSignal is an accepted signal directive with its resolution and
// applied_tick (parallel to TickedSpawn). It is the record-plane shape:
// replay re-enqueues the directive at Tick and the override table takes
// the identical state.
type TickedSignal struct {
	Tick      uint64 // tick the engine accepted the directive (applied_tick)
	SignalIdx int    // resolved index into Network.Signals
	SignalDirective
}

// sigOverride is one held command: the program shows Phase's state string
// for every tick in [Since, Until) — Until is EXCLUSIVE, so the hold spans
// exactly HoldTicks ticks and the fixed-time program resumes at Until.
// An entry is RETAINED for the program's clearance window past Until (not
// enforced, but visible to sigPhaseAt's lookback): sigInClearance reads
// the phase in force at onset−1, and a hold that ended inside the window
// must still answer for the ticks it covered. That applies to BOTH ways a
// hold ends — lapse at its bound, and SUPERSESSION by a newer verb, whose
// entry keeps the phase it held with Until truncated to the replacement
// tick: a held amber replaced by a commanded red is an amber→red
// transition (clearance applies) whatever the fixed schedule was doing,
// and a held green replaced by red is green→red (no clearance) even if
// the schedule happened to be amber at onset−1. requestID is audit trail
// only — it decides nothing, so the CRC does not fold it (the keyframe
// carries it for record fidelity).
type sigOverride struct {
	phase      int
	since      uint64 // applied_tick; hold starts here
	until      uint64 // exclusive: first tick this entry no longer governs
	chainStart uint64 // first-since of the hold CHAIN (an uninterrupted same-phase run): the starvation bound clamps until to chainStart + bound
	requestID  string
}

// SigLapse records one held phase reaching its bound during the last Step:
// the hold ended and the fixed-time program resumed. This is the starvation
// rail firing — a controller that meant to hold longer has lost the signal —
// so it is surfaced, never silent: the sim core cannot log (ADR-0005), the
// run loop edge-logs every lapse, and tests/assertions read the slice.
// Derived per-tick state — never serialized, not in the CRC (the lapse tick
// is a pure function of the keyframed override), reused by the next Step
// exactly like AppliedSpawns.
type SigLapse struct {
	Signal string // program id
	Phase  int    // the held phase that lapsed
	// Since is the tick the hold CHAIN began — the first hold of an
	// uninterrupted same-phase run — so the event spans the whole starved
	// interval, not just the last renewal. Until is the tick the fixed-time
	// program resumed (the chain's clamped end).
	Since     uint64
	Until     uint64
	RequestID string // the verb that commanded the lapsed hold
}

// EnqueueSignal validates a signal_set directive and buffers it for
// application at the next tick boundary. An unknown signal program id or a
// phase index outside the program's phase list is rejected with a reason
// and the run is unaffected; a hold past the bound is clamped (the
// default, HoldTicks == 0, is one cycle of the commanded program — the
// natural bounded quantum; the resume still follows the fixed-time
// schedule from the lapse tick, which is back at the phase the schedule
// was in WHEN THE COMMAND BEGAN, not necessarily the commanded one).
// Like EnqueueSpawn, it must be called only from the goroutine that owns
// the engine, between Steps.
func (e *Engine) EnqueueSignal(d SignalDirective) error {
	if len(d.RequestID) > MaxRequestIDBytes {
		return fmt.Errorf("request_id too long: %d bytes, want ≤ %d (keyframe codec limit)", len(d.RequestID), MaxRequestIDBytes)
	}
	idx := -1
	for i, p := range e.Net.Signals {
		if p.ID == d.Signal {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("unknown signal program %q", d.Signal)
	}
	p := e.Net.Signals[idx]
	if d.Phase < 0 || d.Phase >= len(p.Phases) {
		return fmt.Errorf("signal program %q has %d phases; phase %d out of range", d.Signal, len(p.Phases), d.Phase)
	}
	hold := d.HoldTicks
	if hold == 0 {
		hold = p.cycle
	}
	if bound := e.signalHoldMaxTicks(); hold > bound {
		hold = bound
	}
	for _, q := range e.sigNew {
		if q.RequestID == d.RequestID {
			return fmt.Errorf("duplicate signal request id %q (already buffered for this boundary)", d.RequestID)
		}
	}
	// The live override history (active holds AND ended ones still in
	// clearance retention) carries every recent command's request id and is
	// keyframed, so this check survives a warm restore: the contract
	// layer's per-process reply cache (Contract.verbs) is empty after one,
	// and a retried id re-applying the command would reset since/until —
	// breaking the run-lifetime idempotency the channel advertises. The
	// sweep bounds the window: an id whose hold lapsed AND left retention
	// is no longer detectable here, same bounded-window semantics as
	// EnqueueSpawn's live-queue check. The scan is validation-only and
	// order-independent (any match rejects), so iterating the map cannot
	// leak nondeterminism into the sim (ADR-0005).
	for _, h := range e.sigOv {
		for _, ov := range h {
			if ov.requestID == d.RequestID {
				return fmt.Errorf("duplicate signal request id %q (still live in the override table)", d.RequestID)
			}
		}
	}
	e.sigNew = append(e.sigNew, TickedSignal{
		SignalIdx: idx,
		SignalDirective: SignalDirective{
			RequestID: d.RequestID,
			Signal:    d.Signal,
			Phase:     d.Phase,
			HoldTicks: hold,
		},
	})
	return nil
}

// AppliedSignals returns the signal directives that took effect during the
// last Step (stamped with their applied_tick and effective hold) — the
// record plane logs exactly these. The slice is reused by the next Step.
func (e *Engine) AppliedSignals() []TickedSignal { return e.appliedSig }

// LapsedSignals returns the holds that reached their bound during the last
// Step (the starvation rail firing). The slice is reused by the next Step.
func (e *Engine) LapsedSignals() []SigLapse { return e.sigLapses }

// markSignalVerbUnenforced stamps this tick's record-plane copies of an
// applied directive that ended up enforcing nothing — a same-boundary
// supersede dropped its override as an empty interval — to hold 0, the
// accepted-but-enforced-nothing marker (the declined-renewal semantics):
// the verb was accepted and logged, and the record must show what was
// enforced. The SigLog scan is bounded by the tick match, so a swept
// older entry sharing the id (the bounded dedup window) is never touched.
func (e *Engine) markSignalVerbUnenforced(requestID string) {
	for i := len(e.appliedSig) - 1; i >= 0; i-- {
		if e.appliedSig[i].RequestID == requestID {
			e.appliedSig[i].HoldTicks = 0
			break
		}
	}
	for i := len(e.SigLog) - 1; i >= 0; i-- {
		if e.SigLog[i].Tick == e.Tick && e.SigLog[i].RequestID == requestID {
			e.SigLog[i].HoldTicks = 0
			break
		}
	}
}

// stepSignalOverrides runs the commanded-override bookkeeping at the events
// phase of Step, after the director spawn queue: newly buffered directives
// are stamped with the current tick and installed in the override table,
// then lapsed and expired holds are swept. The table maps a program id to
// its override HISTORY (oldest→newest; the last entry is the one in
// force): a new verb SUPERSEDES the signal's current override — the
// outgoing entry's Until truncates to the replacement tick and it is
// retained for the clearance window exactly like a lapsed one, because
// sigInClearance's onset−1 lookback must be answered with the phase that
// was actually in force (see sigOverride). All iteration is over the
// network's program list in network order and slices in chronological
// order (the map itself is lookup-only, ADR-0005), so lapse detection and
// retention are as deterministic as the derivation they feed.
func (e *Engine) stepSignalOverrides() {
	e.appliedSig = e.appliedSig[:0]
	e.sigLapses = e.sigLapses[:0]
	if len(e.sigNew) > 0 {
		if e.sigOv == nil {
			e.sigOv = map[string][]sigOverride{}
		}
		bound := e.signalHoldMaxTicks()
		for _, d := range e.sigNew {
			d.Tick = e.Tick
			p := e.Net.Signals[d.SignalIdx]
			h := e.sigOv[p.ID]
			// Chain resolution (the starvation rail, see the file header):
			// a same-phase supersede with no fixed-time gap RENEWS the
			// current chain — it inherits chainStart, so its end clamps to
			// chainStart+bound. A different phase, or any gap, starts a new
			// chain at this tick.
			chainStart := e.Tick
			if n := len(h); n > 0 {
				if last := h[n-1]; last.phase == d.Phase && last.until >= e.Tick {
					chainStart = last.chainStart
				}
				// Supersede, don't discard. An entry truncated to an empty
				// interval governed no tick at all (two verbs for one
				// signal in the same boundary): drop it — and stamp its
				// record-plane copies to hold 0, the
				// accepted-but-enforced-nothing marker (the
				// declined-renewal semantics), because the verb was
				// accepted and logged but its override never governed a
				// tick.
				if h[n-1].until > e.Tick {
					h[n-1].until = e.Tick
				}
				if h[n-1].until <= h[n-1].since {
					e.markSignalVerbUnenforced(h[n-1].requestID)
					h = h[:n-1]
				}
			}
			until := e.Tick + d.HoldTicks
			if chainEnd := chainStart + bound; until > chainEnd {
				until = chainEnd
			}
			if until > e.Tick {
				e.sigOv[p.ID] = append(h, sigOverride{
					phase:      d.Phase,
					since:      e.Tick,
					until:      until,
					chainStart: chainStart,
					requestID:  d.RequestID,
				})
				// The record carries the EFFECTIVE hold (the chain clamp
				// may shorten the requested one), so the logged verb shows
				// what was enforced; replay re-enqueues it and the same
				// chain state re-clamps to the same until.
				d.HoldTicks = until - e.Tick
			} else {
				// A renewal whose clamped end is not past this tick arrived
				// AT the chain's bound: it extends nothing and is not
				// installed — the current entry (already clamped to the
				// bound) lapses this tick below, firing exactly one lapse
				// event. The verb is still recorded as applied — the
				// command reached the engine — with hold_ticks 0 marking
				// "accepted, enforced nothing (declined at the chain
				// bound)"; the lapse event carries the enforced truth.
				// Replay re-derives the decline from the same chain state
				// whatever the recorded value, so determinism holds.
				d.HoldTicks = 0
			}
			e.appliedSig = append(e.appliedSig, d)
			e.SigLog = append(e.SigLog, d)
		}
		e.sigNew = e.sigNew[:0]
	}
	if len(e.sigOv) == 0 {
		return
	}
	for _, p := range e.Net.Signals {
		h, ok := e.sigOv[p.ID]
		if !ok {
			continue
		}
		// The lapse is a pure function of the keyframed state (Until) of the
		// CURRENT (newest) entry — a superseded entry ended by replacement,
		// not by reaching its bound, so it fires no lapse event (the
		// superseding verb is itself on the record). The event fires on
		// exactly this tick in the live run, in replay, and after a
		// mid-hold keyframe restore alike. Since reports the CHAIN start,
		// so a renewed hold's event spans the whole starved interval.
		if last := h[len(h)-1]; e.Tick == last.until {
			e.sigLapses = append(e.sigLapses, SigLapse{
				Signal: p.ID, Phase: last.phase, Since: last.chainStart, Until: last.until,
				RequestID: last.requestID,
			})
		}
		// Retention past Until covers sigPhaseAt's clearance lookback (see
		// sigOverride): the deepest tick sigInClearance can read at T is
		// T−clearance−1, so an entry must answer while that reaches into
		// its held interval — through T == Until+clearance. Past it the
		// entry decides nothing and leaves the table — and with an empty
		// table the CRC fold and the keyframe version drop back to the
		// pre-ADR-0037 shape.
		kept := h[:0]
		for _, ov := range h {
			if e.Tick <= ov.until+p.clearance {
				kept = append(kept, ov)
			}
		}
		if len(kept) == 0 {
			delete(e.sigOv, p.ID)
		} else {
			e.sigOv[p.ID] = kept
		}
	}
}
