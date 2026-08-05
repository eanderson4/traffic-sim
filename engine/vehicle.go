package engine

import "math"

// VehicleType carries the per-class parameters of ADR-0007: geometry
// (length, width) plus IDM dynamics (s0, T, a, b, v0). Types mix freely in
// one lane; the bumper-to-bumper gap convention keeps every pair unambiguous.
type VehicleType struct {
	Name   string
	Length float64 // m
	Width  float64 // m
	S0     float64 // jam gap, bumper-to-bumper (m)
	T      float64 // desired time headway (s)
	A      float64 // max acceleration (m/s²)
	B      float64 // comfortable deceleration (m/s²)
	V0     float64 // desired speed (m/s)
}

// Car is the default class. The dynamics are the original IDM paper's
// highway calibration (Treiber, Hennecke & Helbing 2000, cond-mat/0002177:
// v0 = 120 km/h, T = 1.6 s, a = 0.73 m/s², b = 1.67 m/s², s0 = 2 m, δ = 4 —
// the "Wikipedia typical" column of KB implementation.md §1), chosen over the
// string-stable CACAIE reference set (a = 1.0, T = 1.5, b = 2.0) after the M2
// I-80 validation: with troughs creeping at the IDM equilibrium speed the
// chord slope caps at ≈ −12 km/h, while real stop-and-go troughs are
// sub-equilibrium (analysis/ngsim/README.md). This set sits in the
// instability-capable highway regime (KB implementation.md §7; the NGSIM
// calibration literature finds T the most influential parameter,
// arXiv:0803.4063 via standards-and-patterns.md), so jam discharge lags
// equilibrium and the backward wave lands in the −15…−20 km/h anchor band.
// String instability is a requirement here, not a defect: it is the
// phantom-jam mechanism the Sugiyama test pins (sugiyama_test.go).
var Car = VehicleType{Name: "car", Length: 5, Width: 2, S0: 2, T: 1.6, A: 0.73, B: 1.67, V0: 33.3}

// Truck demonstrates first-class multi-class support (ADR-0007). Not part of
// the default spawn mix; values loosely follow the research (trucks 80 km/h):
// slower acceleration and a longer headway than the car, sharing the
// comfortable-braking convention (b is comfort, not capability — the
// convention-split warning of KB implementation.md §3).
var Truck = VehicleType{Name: "truck", Length: 12, Width: 2.5, S0: 3, T: 1.7, A: 0.7, B: 1.67, V0: 80.0 / 3.6}

// emergencyDecel caps IDM deceleration at the physical dry-road limit
// (~9 m/s²; Kesting & Treiber CACAIE p.8 via the research synthesis).
const emergencyDecel = 9.0

// Vehicle is the world-state unit. Position is the front-bumper coordinate s
// along a lane (ADR-0007); gap semantics are bumper-to-bumper everywhere.
type Vehicle struct {
	ID       uint64
	Type     *VehicleType
	TypeIdx  int     // index into the scenario type list (canonical for CRC)
	Lane     *Lane   // current lane; nil while despawned
	S        float64 // front-bumper position along Lane (m)
	V        float64 // speed (m/s), never negative (stopping override)
	F        float64 // per-driver desired-speed factor (speedFactor-style heterogeneity)
	Acc      float64 // acceleration applied this tick (m/s²)
	Cooldown uint64  // ticks until this vehicle may change lanes again
	rng      *Stream // per-vehicle seeded stream (ADR-0007); never shared

	// Persistent controller state (ADR-0008 §2 per-axis persistence): mutated
	// only by applied intents (or by consumption at a junction), held across
	// ticks, restored by keyframes. Not folded into the rolling CRC — the CRC
	// stays the M3 trajectory oracle (their effects reach it through
	// S/V/lane); see computeCRC.
	Cruise   float64 // speed setpoint (m/s); servo target while CruiseOK
	CruiseOK bool    // persistent until replaced or cleared
	HeldTurn int     // turn at junction: +1 left, −1 right, 0 none; held until consumed at a lane crossing
	Signals  int     // 0 off, 1 left, 2 right, 3 hazard (informational in v1)
	Route    string  // routing axis: destination lane id (persistent; informational on single-path networks)

	// Transient per-tick controller intent state (ADR-0008): set by
	// applyIntents at the tick boundary, consumed within the same tick,
	// reset at the top of the next one — like Acc, not part of the CRC'd
	// state (the CRC chain covers their effects through S/V/lane).
	reqAcc   float64 // commanded accel override for this tick (reqAccOK)
	reqAccOK bool
	reqLane  int // commanded lane hop this tick: +1 left, −1 right, 0 none

	// stopDone records that a stop-approach vehicle has completed its
	// mandatory full stop at the current junction's line (ADR-0010); set by
	// the right-of-way gate, reset at every lane crossing. Not folded into
	// the CRC (same precedent as HeldTurn/Cruise), but it IS keyframed
	// (format v5) — see keyframe.go. It was left out at first, on the
	// argument that its worst case is a vehicle stopping twice; under
	// ADR-0029's bit-exact criterion stopping twice IS the failure, and a
	// vehicle already MOVING off the line cannot re-satisfy the test that
	// sets this, so it brakes to a second full stop the original run never
	// made. TestStopDoneSurvivesAKeyframe pins it.
	stopDone bool

	// stuckTicks counts consecutive ticks spent below stuckSpeed; it drives
	// the gridlock escape (ADR-0034, gridlock.go). Not in the CRC but
	// keyframed (format v5), and for a stronger reason than stopDone above:
	// this decides whether a vehicle EXISTS, because strandStuck
	// removes one that reaches StrandAfterS. ReplayFromStream and
	// Player.seek restore from a MID-RUN keyframe and then verify every
	// logged CRC, so a timer zeroed by the restore strands the vehicle a
	// whole StrandAfterS late and diverges the replay.
	// TestStuckTimerSurvivesAKeyframe pins it; do not make this derived.
	stuckTicks uint64

	// laneEntryTick is the tick the vehicle entered its current lane; the
	// difference from e.Tick at the next departure is the dwell sample fed
	// into the lane's travel-time EMA (ADR-0036 §1). Set at injection and at
	// every lane crossing and lateral hop. Keyframed (format v6) for the
	// stuckTicks reason above: a restored zero would attribute the whole
	// pre-restore dwell to the post-restore lane and poison ITS travel time,
	// diverging every later routing decision. Written but never read while
	// Params.AdaptiveRouting is off.
	laneEntryTick uint64
}

// v0eff is the vehicle's desired speed on the given lane: type desired speed
// capped by the lane limit, scaled by the per-driver factor.
func (v *Vehicle) v0eff(lane *Lane) float64 {
	v0 := v.Type.V0
	if lane.SpeedLimit < v0 {
		v0 = lane.SpeedLimit
	}
	return v.F * v0
}

// cruiseAccel is the speed-setpoint servo (ADR-0008 §1 convenience variant):
// reach the setpoint within one tick, clamped to the physical envelope.
func (v *Vehicle) cruiseAccel(dt float64) float64 {
	a := (v.Cruise - v.V) / dt
	if a > v.Type.A {
		a = v.Type.A
	}
	if a < -emergencyDecel {
		a = -emergencyDecel
	}
	return a
}

// idmAccel evaluates the Intelligent Driver Model (Treiber/Hennecke/Helbing
// 2000; implementation.md §1):
//
//	dv/dt = a [ 1 − (v/v0)^4 − (s*/s)² ]
//	s*(v,Δv) = max( s0 + vT + v·Δv / (2√(a·b)), 0 )
//
// with s the bumper-to-bumper gap and Δv = v − v_lead. The gap is clamped at
// 0.1 m for numerical robustness (same guard as the JS prototype) and the
// result is capped at −emergencyDecel.
//
// Note: the docs clamp the whole s* expression at 0; the JS prototype clamps
// only the inner terms (s0 + max(0, …)). Per the task rule "docs win", this
// is the literature form — it differs only when the leader is much faster
// (very negative Δv), where it brakes less.
func idmAccel(t *VehicleType, v0, v float64, lead LeaderInfo) float64 {
	free := t.A * (1 - math.Pow(v/v0, 4))
	if !lead.OK {
		return free
	}
	s := lead.Gap
	if s < 0.1 {
		s = 0.1
	}
	sStar := t.S0 + v*t.T + v*(v-lead.V)/(2*math.Sqrt(t.A*t.B))
	if sStar < 0 {
		sStar = 0
	}
	a := t.A * (1 - math.Pow(v/v0, 4) - (sStar/s)*(sStar/s))
	if a < -emergencyDecel {
		a = -emergencyDecel
	}
	return a
}

// equilibriumSpeed solves the IDM steady-state gap relation
//
//	s_e(v) = (s0 + vT) / √(1 − (v/v0)⁴) = gap
//
// for v ∈ [0, v0] by bisection (s_e is monotonically increasing in v).
// Used to initialize the Sugiyama ring at its uniform-flow fixed point so
// the stability test starts from equilibrium, not a transient.
func equilibriumSpeed(gap, v0 float64, t *VehicleType) float64 {
	lo, hi := 0.0, v0
	for i := 0; i < 100; i++ {
		mid := 0.5 * (lo + hi)
		if mid == lo || mid == hi {
			break
		}
		se := (t.S0 + mid*t.T) / math.Sqrt(1-math.Pow(mid/v0, 4))
		if se < gap {
			lo = mid
		} else {
			hi = mid
		}
	}
	return 0.5 * (lo + hi)
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
