package engine

import "sort"

// Intent is one controller command for one vehicle (ADR-0008 §1: four
// orthogonal axes — longitudinal, lateral, routing, signal state). Absent
// fields (the *Set flags) mean "no change". AI, scripted, and human
// controllers all emit this one type. Per-axis persistence (ADR-0008 §2):
//
//	Accel          longitudinal: target acceleration (m/s²), one-shot per
//	               tick (AccelSet: presence is explicit because 0 is a
//	               valid command). Clamped to the vehicle's physical
//	               envelope [−emergencyDecel, Type.A] — the engine enforces
//	               physics limits regardless of commands (MMU pattern,
//	               ADR-0008 §5); collision-freedom of an intent-driven
//	               vehicle is the controller's responsibility.
//	SpeedSetpoint  longitudinal variant: cruise setpoint (m/s), persistent
//	               until replaced; a negative value clears it. Clamped to
//	               [0, v0eff] at application; the engine-side servo reaches
//	               it within one tick within the envelope.
//	LaneDelta      lateral: +1 = hop toward Lane.Left, −1 = toward
//	               Lane.Right, 0 = no request. One-shot: consumed the tick
//	               it is applied, expires if infeasible.
//	Turn           junction choice: +1 left, −1 right (TurnSet); held until
//	               consumed at the next lane crossing, 0 clears. On
//	               single-successor lanes there is no choice to make; the
//	               held value is still consumed on crossing.
//	Route          routing axis: destination lane id (RouteSet), persistent.
//	               Informational on M1's single-path networks.
//	Signals        signal state: 0 off, 1 left, 2 right, 3 hazard
//	               (SignalSet), persistent, informational in v1 — it
//	               appears in other controllers' observations.
//
// Transport hold-last (≤ 2 ticks on message loss) is orthogonal healing done
// by the contract layer re-issuing the last intent — the kernel only ever
// sees plain intents (engine/natsio, ADR-0008 §2).
type Intent struct {
	VehicleID uint64

	Accel         float64 // target acceleration (m/s²); meaningful only if AccelSet
	AccelSet      bool
	SpeedSetpoint float64 // cruise setpoint (m/s); meaningful only if SpeedSet; negative clears
	SpeedSet      bool
	LaneDelta     int // +1 left, −1 right, 0 none
	Turn          int // +1 left, −1 right, 0 clear; meaningful only if TurnSet
	TurnSet       bool
	Route         string // destination lane id; meaningful only if RouteSet
	RouteSet      bool
	Signals       int // 0 off, 1 left, 2 right, 3 hazard; meaningful only if SignalSet
	SignalSet     bool
}

// Grant levels for the deterministic tie-break (ADR-0008 §4): same-tick
// competing intents resolve by (grant level, then vehicle ID) — higher grant
// wins; vehicle ID and then (controller, seq) make the order total. Grant 0
// is the legacy level of unattached publishers (M3 compat); the privileged
// roles are elevated by the attach handshake.
const (
	GrantNone     uint8 = 0 // unattached/legacy publishers
	GrantDrive    uint8 = 1 // ordinary controllers (drivers)
	GrantSignal   uint8 = 2 // signal controllers (stub grant)
	GrantDirector uint8 = 3 // scenario directors (verbs land M5+)
)

// KeyedIntent carries an Intent with its arbitration identity: the
// controller that emitted it, the per-controller arrival sequence, its grant
// level, and whether it is a hold-last re-issue synthesized by the contract
// layer (Held — informational; application semantics are unaffected). The
// deterministic application order at a tick boundary is (grant desc, vehicle
// asc, controller asc, seq asc) (ADR-0008 §4, superseding M3's
// (controller, seq)).
type KeyedIntent struct {
	Controller string
	Seq        uint64
	Grant      uint8
	Held       bool
	Intent     Intent
}

// TickedIntent is an arbitrated log entry: the intent plus the tick at which
// the engine actually applied it (applied_tick, ADR-0006 §4). Superseded
// marks intents that lost a same-tick competition for their vehicle — they
// are recorded (the log reflects exactly what the engine saw) but were not
// applied.
type TickedIntent struct {
	Tick       uint64
	Superseded bool
	KeyedIntent
}

// EnqueueIntent buffers an intent for application at the next tick
// boundary. It must be called only from the goroutine that owns the engine
// (ADR-0005: one goroutine owns world state) — bus layers copy intents off
// the wire under their own lock and hand them over between Steps.
func (e *Engine) EnqueueIntent(k KeyedIntent) {
	e.intentQ = append(e.intentQ, k)
}

// AppliedIntents returns the intents applied during the last Step, in
// application order — including superseded ones (flagged; they are part of
// the arbitrated record but had no effect). The slice is reused by the next
// Step — consume or copy it before Stepping again.
func (e *Engine) AppliedIntents() []TickedIntent { return e.applied }

// applyIntents drains the queue at the tick boundary. Order is the ADR-0008
// §4 deterministic tie-break: grant level descending, then vehicle ID, then
// (controller, seq). For each vehicle the FIRST intent in that order wins;
// later same-tick competitors are recorded as superseded no-ops. Every
// queued intent is recorded in the arbitrated log with the current tick —
// including ones that target unknown or despawned vehicles, which apply as
// no-ops (the record must reflect exactly what the engine saw).
func (e *Engine) applyIntents() {
	e.applied = e.applied[:0]
	if len(e.intentQ) == 0 {
		return
	}
	sort.SliceStable(e.intentQ, func(i, j int) bool {
		a, b := e.intentQ[i], e.intentQ[j]
		if a.Grant != b.Grant {
			return a.Grant > b.Grant
		}
		if a.Intent.VehicleID != b.Intent.VehicleID {
			return a.Intent.VehicleID < b.Intent.VehicleID
		}
		if a.Controller != b.Controller {
			return a.Controller < b.Controller
		}
		return a.Seq < b.Seq
	})
	won := map[uint64]bool{}
	for _, k := range e.intentQ {
		id := k.Intent.VehicleID
		superseded := won[id]
		if !superseded {
			won[id] = true
			if v := e.vehicleByID(id); v != nil {
				e.applyAxes(v, &k.Intent)
			}
		}
		entry := TickedIntent{Tick: e.Tick, Superseded: superseded, KeyedIntent: k}
		e.applied = append(e.applied, entry)
		e.IntentLog = append(e.IntentLog, entry)
	}
	e.intentQ = e.intentQ[:0]
}

// applyAxes applies one winning intent's axes to its vehicle, per the
// persistence table (ADR-0008 §2): one-shot axes arm the per-tick overrides,
// persistent axes mutate held controller state.
func (e *Engine) applyAxes(v *Vehicle, in *Intent) {
	if in.AccelSet {
		v.reqAcc, v.reqAccOK = in.Accel, true
	}
	if in.SpeedSet {
		if in.SpeedSetpoint < 0 {
			v.CruiseOK = false
		} else {
			// Clamp the setpoint to the vehicle's desired-speed envelope
			// (engine-side clamp, ADR-0008 §5).
			sp := in.SpeedSetpoint
			if v0 := v.v0eff(v.Lane); sp > v0 {
				sp = v0
			}
			v.Cruise, v.CruiseOK = sp, true
		}
	}
	if in.LaneDelta != 0 {
		v.reqLane = in.LaneDelta
	}
	if in.TurnSet {
		v.HeldTurn = clampDir(in.Turn)
	}
	if in.RouteSet {
		v.Route = in.Route
	}
	if in.SignalSet {
		v.Signals = clampSignals(in.Signals)
	}
}

// clampDir constrains a direction value to {−1, 0, +1}.
func clampDir(d int) int {
	if d > 0 {
		return 1
	}
	if d < 0 {
		return -1
	}
	return 0
}

// clampSignals constrains a signal state to {0 off, 1 left, 2 right, 3 hazard}.
func clampSignals(s int) int {
	if s < 0 {
		return 0
	}
	if s > 3 {
		return 3
	}
	return s
}

// vehicleByID resolves a live vehicle by ID via the engine's ID index
// (lookup only — e.index is never iterated, ADR-0005). NOTE: e.order is
// spawn order, not ID order — the per-origin spawn schedules interleave —
// so the ID index (not a binary search) is the correct lookup.
func (e *Engine) vehicleByID(id uint64) *Vehicle { return e.index[id] }

// VehicleByID resolves a live vehicle by ID for read-only consumers (the
// contract layer's claim registry and observation builder). Do not mutate.
func (e *Engine) VehicleByID(id uint64) *Vehicle { return e.vehicleByID(id) }

// intentAccel clamps a commanded acceleration to the vehicle's physical
// envelope (the engine-side safety clamp of ADR-0008 §5).
func (v *Vehicle) intentAccel() float64 {
	a := v.reqAcc
	if a > v.Type.A {
		a = v.Type.A
	}
	if a < -emergencyDecel {
		a = -emergencyDecel
	}
	return a
}

// tryForcedLaneChange executes a controller-commanded hop (lateral intent
// axis). The incentive side of MOBIL is bypassed — the command is the
// incentive — but every safety gate of the reference policy still applies:
// hard lateral gaps, the kinematic collision-freedom floor, the changer's
// own b_safe, and the new follower's b_safe. An infeasible command expires
// (one-shot semantics, ADR-0008 §2).
func (e *Engine) tryForcedLaneChange(v *Vehicle) {
	if e.PolicyContext(v).ForcedFeasible(e.Params, v.reqLane) {
		e.execLaneChange(v, v.reqLane)
	}
}
