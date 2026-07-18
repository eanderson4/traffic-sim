package engine

import "math"

// policy.go — the reference driving policy as a pure function of an explicit
// per-vehicle context (ADR-0008 §5–§6). The kernel evaluates the SAME code
// the external default driver evaluates client-side from its observations:
// PolicyContext gathers the context from live world state, the observation
// publisher ships it, and DecideAccel/DecideLaneChange/ForcedFeasible are
// the one shared implementation — the contract's sufficiency is dogfooded
// with zero policy drift (the default driver IS the reference controller).
//
// Bit-identity discipline: every expression here is the verbatim arithmetic
// the M1–M3 inline code (engine.go/mobil.go) ran, restructured as
// gather-then-decide. No float operation was reordered and no RNG draw was
// added or removed on the harness path, so the idm-policy baselines are
// unchanged.

// LeaderInfo is a resolved car-following leader: bumper-to-bumper gap and
// leader speed. OK=false means free road (no leader within search depth).
type LeaderInfo struct {
	OK  bool
	Gap float64
	V   float64
}

// FollowerCtx is a resolved follower relevant to a lateral decision: the old
// follower on the current lane or the prospective new follower on a target
// lane — possibly through a lane-boundary connection (cross-boundary pairs
// are resolved by the gather, never approximated). Lead is the follower's
// own car-following leader; for the CURRENT-lane follower it is the leader
// it would have AFTER the ego departs (the MOBIL Δa_o term), for target-lane
// followers its present leader (the Δa_n baseline).
type FollowerCtx struct {
	OK      bool
	Gap     float64 // bumper-to-bumper gap to the ego (m)
	V       float64 // follower speed (m/s)
	F       float64 // follower desired-speed factor
	S       float64 // follower front-bumper position on its own lane (m)
	Type    *VehicleType
	TypeIdx int // index into the scenario type list (wire form of Type)

	// The follower's own lane (a predecessor lane when the pair spans a
	// boundary): needed for its desired speed and the dead-lane wall.
	LaneLimit   float64
	LaneLen     float64
	LaneEndWall bool

	Lead LeaderInfo
}

// SideCtx is one lateral candidate lane with the resolved adjacency context:
// the car-following leader ahead of the ego's position on that lane
// (cross-boundary aware) and the nearest follower behind it.
type SideCtx struct {
	Present bool
	Limit   float64 // speed limit (m/s)
	Length  float64 // lane length (m)
	EndWall bool    // dead-end lane: virtual standing vehicle at Length
	Lead    LeaderInfo
	Foll    FollowerCtx
}

// PolicyCtx is the complete input to the reference driving policy for one
// vehicle at one tick. Gathered engine-side by PolicyContext (exact, full
// world state) and shipped to controllers in observations, so a client's
// decision from the ctx equals the engine's own decision from live state.
type PolicyCtx struct {
	Type     *VehicleType
	TypeIdx  int
	LaneIdx  int
	S        float64 // front-bumper position on the current lane (m)
	V        float64 // speed (m/s)
	F        float64 // desired-speed factor
	Cooldown uint64  // ticks until a lane change is allowed again

	CurLimit   float64
	CurLen     float64
	CurEndWall bool
	CurLead    LeaderInfo
	CurFoll    FollowerCtx

	Left, Right SideCtx
}

// v0eff is the ego's desired speed on a lane with the given limit.
func (ctx *PolicyCtx) v0eff(limit float64) float64 {
	v0 := ctx.Type.V0
	if limit < v0 {
		v0 = limit
	}
	return ctx.F * v0
}

// v0eff is the follower's desired speed on its own lane.
func (f *FollowerCtx) v0eff() float64 {
	v0 := f.Type.V0
	if f.LaneLimit < v0 {
		v0 = f.LaneLimit
	}
	return f.F * v0
}

// accelEval is IDM plus, on dead-end lanes, the "virtual standing vehicle at
// the lane end" (merge pressure becomes ordinary car-following deceleration).
// The wall applies to any accel evaluated on that lane, real or hypothetical.
// This is the one longitudinal primitive: the kernel's accelOnLane delegates
// here, and so do the client-side policy evaluations.
func accelEval(t *VehicleType, v0eff, v, s, laneLen float64, endWall bool, lead LeaderInfo) float64 {
	a := idmAccel(t, v0eff, v, lead)
	if endWall {
		wall := idmAccel(t, v0eff, v, LeaderInfo{OK: true, Gap: laneLen - s, V: 0})
		if wall < a {
			a = wall
		}
	}
	return a
}

// DecideAccel is the reference longitudinal decision: IDM toward the
// resolved leader on the current lane, with the dead-lane wall if any.
func (ctx *PolicyCtx) DecideAccel() float64 {
	return accelEval(ctx.Type, ctx.v0eff(ctx.CurLimit), ctx.V, ctx.S, ctx.CurLen, ctx.CurEndWall, ctx.CurLead)
}

// sideAccel evaluates the ego's hypothetical accel on a lateral candidate.
func (ctx *PolicyCtx) sideAccel(side *SideCtx) float64 {
	return accelEval(ctx.Type, ctx.v0eff(side.Limit), ctx.V, ctx.S, side.Length, side.EndWall, side.Lead)
}

// accelToward is the follower's accel behind an explicit leader (gap, leadV)
// on its own lane — the post-change pair evaluations of MOBIL.
func (f *FollowerCtx) accelToward(gap, leadV float64) float64 {
	return accelEval(f.Type, f.v0eff(), f.V, f.S, f.LaneLen, f.LaneEndWall, LeaderInfo{OK: true, Gap: gap, V: leadV})
}

// accelCurrent is the follower's accel behind its resolved leader (the MOBIL
// baselines: Δa_n subtracts it; Δa_o's post-departure form uses it directly).
func (f *FollowerCtx) accelCurrent() float64 {
	return accelEval(f.Type, f.v0eff(), f.V, f.S, f.LaneLen, f.LaneEndWall, f.Lead)
}

// evalSide runs the shared candidate gates for one lateral candidate and
// returns whether they pass plus the MOBIL terms ã_c (aNew) and Δa_n (dN,
// zero when the target lane has no follower). minGap and bSafe are the
// (possibly urgency-relaxed) limits — forced commands pass the unrelaxed
// values, discretionary MOBIL the relaxed ones on dead-end lanes.
//
// Gate order and values are the M1–M3 ones: hard lateral gaps, the kinematic
// collision-freedom floor for both new pairs (never urgency-relaxed), the
// changer's own b_safe, then the new follower's b_safe (MOBIL safety).
func (ctx *PolicyCtx) evalSide(p Params, side *SideCtx, minGap, bSafe float64) (ok bool, aNew, dN float64) {
	if side.Lead.OK && side.Lead.Gap < minGap {
		return false, 0, 0 // would overlap the new leader
	}
	if side.Foll.OK && side.Foll.Gap < minGap {
		return false, 0, 0 // would overlap the new follower
	}
	// Kinematic collision-freedom floor for both new pairs.
	if side.Lead.OK && !kinGapOK(side.Lead.Gap, ctx.V, side.Lead.V, p.Dt) {
		return false, 0, 0 // changer cannot hold the gap behind the new leader
	}
	if side.Foll.OK && !kinGapOK(side.Foll.Gap, side.Foll.V, ctx.V, p.Dt) {
		return false, 0, 0 // new follower cannot hold the gap behind the changer
	}
	aNew = ctx.sideAccel(side)
	if aNew < -bSafe {
		return false, 0, 0 // own safety
	}
	if side.Foll.OK {
		aNNew := side.Foll.accelToward(side.Foll.Gap, ctx.V)
		if aNNew < -bSafe {
			return false, 0, 0 // MOBIL safety criterion
		}
		dN = aNNew - side.Foll.accelCurrent()
	}
	return true, aNew, dN
}

// DecideLaneChange is symmetric MOBIL (Kesting/Treiber/Helbing 2007):
//
//	safety:    ã_n ≥ −b_safe
//	incentive: ã_c − a_c + p·[(ã_n − a_n) + (ã_o − a_o)] > Δa_th
//
// for both adjacent lanes; returns +1 (toward Left), −1 (toward Right), or 0
// (keep lane). On a dead-end lane the changer is "mandatory": the hard gap
// relaxes and b_safe grows with urgency toward the wall. Exact incentive ties
// are broken through rng — the vehicle's OWN seeded stream (ADR-0007), so a
// client evaluating this function with the same ctx and stream reaches the
// same decision as the engine. Cooldown is the caller's check (the kernel
// decrements it; clients read it from the ctx).
func (ctx *PolicyCtx) DecideLaneChange(p Params, rng *Stream) int {
	var sides [2]*SideCtx
	n := 0
	if ctx.Left.Present {
		sides[n] = &ctx.Left
		n++
	}
	if ctx.Right.Present {
		sides[n] = &ctx.Right
		n++
	}
	if n == 0 {
		return 0
	}

	aC := ctx.DecideAccel()
	aO, hasO := 0.0, false
	if ctx.CurFoll.OK {
		aO = ctx.CurFoll.accelToward(ctx.CurFoll.Gap, ctx.V)
		hasO = true
	}

	type candidate struct {
		dir int
		inc float64
	}
	var passed []candidate

	for i := 0; i < n; i++ {
		side := sides[i]
		mandatory := ctx.CurEndWall && !side.EndWall
		minGap := p.MinGapLC
		bSafe := p.BSafe
		if mandatory {
			minGap = p.MinGapMerge
			urg := clamp01((ctx.S - (ctx.CurLen - p.MergeZone)) / p.MergeZone)
			bSafe += p.MergeUrgencyGain * urg
		}

		ok, aNew, dN := ctx.evalSide(p, side, minGap, bSafe)
		if !ok {
			continue
		}

		dO := 0.0
		if hasO {
			aONew := ctx.CurFoll.accelCurrent()
			dO = aONew - aO
		}

		inc := aNew - aC + p.Politeness*(dN+dO)
		if inc > p.LCThreshold {
			dir := 1
			if side == &ctx.Right {
				dir = -1
			}
			passed = append(passed, candidate{dir: dir, inc: inc})
		}
	}
	if len(passed) == 0 {
		return 0
	}

	// Winner: max incentive. Exact (±ε) ties — possible in symmetric states —
	// are broken through the vehicle's own stream, never a global source.
	best := 0
	for i := range passed {
		if passed[i].inc > passed[best].inc+mobilTieEps {
			best = i
		}
	}
	var ties []int
	for i := range passed {
		if math.Abs(passed[i].inc-passed[best].inc) <= mobilTieEps {
			ties = append(ties, i)
		}
	}
	pick := best
	if len(ties) > 1 {
		pick = ties[int(rng.Float64()*float64(len(ties)))]
	}
	return passed[pick].dir
}

// ForcedFeasible reports whether a commanded hop toward dir (+1 left, −1
// right) passes every safety gate (hard gaps, kinematic floor, both b_safe
// checks) — without the incentive side of MOBIL: the command is the
// incentive. Forced commands get NO merge-urgency relaxation.
func (ctx *PolicyCtx) ForcedFeasible(p Params, dir int) bool {
	var side *SideCtx
	if dir > 0 {
		side = &ctx.Left
	} else {
		side = &ctx.Right
	}
	if !side.Present {
		return false
	}
	ok, _, _ := ctx.evalSide(p, side, p.MinGapLC, p.BSafe)
	return ok
}

// PolicyContext gathers the reference-policy context for v from live world
// state — the one gather implementation, shared by the kernel's own lateral
// path and the observation publisher so engine and clients can never drift.
func (e *Engine) PolicyContext(v *Vehicle) *PolicyCtx {
	cur := v.Lane
	ctx := &PolicyCtx{
		Type:       v.Type,
		TypeIdx:    v.TypeIdx,
		LaneIdx:    cur.Index,
		S:          v.S,
		V:          v.V,
		F:          v.F,
		Cooldown:   v.Cooldown,
		CurLimit:   cur.SpeedLimit,
		CurLen:     cur.Length,
		CurEndWall: cur.EndWall,
		CurLead:    e.leader(v),
	}
	if _, foll := neighbors(cur, v.S, v); foll != nil {
		ctx.CurFoll = e.followerCtx(foll, cur, v, v.S-v.Type.Length-foll.S)
	}
	if cur.Left != nil {
		ctx.Left = e.sideCtx(v, cur.Left)
		ctx.Left.Present = true
	}
	if cur.Right != nil {
		ctx.Right = e.sideCtx(v, cur.Right)
		ctx.Right.Present = true
	}
	return ctx
}

// followerCtx resolves one follower. gap is the bumper-to-bumper distance to
// the ego. The follower's own leader is resolved with the ego excluded — for
// the current-lane follower that is precisely the leader it keeps after the
// ego departs; on other lanes the ego never leads the follower anyway.
func (e *Engine) followerCtx(f *Vehicle, lane *Lane, ego *Vehicle, gap float64) FollowerCtx {
	return FollowerCtx{
		OK:          true,
		Gap:         gap,
		V:           f.V,
		F:           f.F,
		S:           f.S,
		Type:        f.Type,
		TypeIdx:     f.TypeIdx,
		LaneLimit:   lane.SpeedLimit,
		LaneLen:     lane.Length,
		LaneEndWall: lane.EndWall,
		Lead:        e.leaderAt(lane, f.S, ego),
	}
}

// sideCtx resolves one lateral candidate: the leader ahead of the ego's
// position (on-lane, else through successors) and the nearest follower
// behind it (on-lane, else through predecessors).
func (e *Engine) sideCtx(v *Vehicle, tgt *Lane) SideCtx {
	sc := SideCtx{
		Limit:   tgt.SpeedLimit,
		Length:  tgt.Length,
		EndWall: tgt.EndWall,
		Lead:    e.leaderAt(tgt, v.S, v),
	}
	if _, nbFoll := neighbors(tgt, v.S, v); nbFoll != nil {
		sc.Foll = e.followerCtx(nbFoll, tgt, v, v.S-v.Type.Length-nbFoll.S)
	} else if pf, pl, pg, ok := e.prevFollower(tgt, v.S-v.Type.Length); ok {
		sc.Foll = e.followerCtx(pf, pl, v, pg)
	}
	return sc
}
