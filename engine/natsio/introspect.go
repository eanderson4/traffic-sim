package natsio

import (
	"fmt"

	"traffic-sim/engine"
)

// introspect.go — the default driver's introspection side channel
// (ADR-0008 §5, concept-vehicle-controller-interface RESOLVED 2026-07-17):
// a NATS request/reply on ts.{run}.drive.introspect, deliberately NOT a
// field in engine observations and off the hot path. Any client asks
// "given this vehicle state, what would you do?" and gets the reference
// policy's decision — a pure function of state + policy, queryable from
// anywhere (debug tooling, RL harnesses, viz).
//
// The request is a JSON view of the reference-policy context (lane
// references by lane ID, resolved against the run's network); absent
// contexts mean free road / no neighbor / no adjacent lane. The reply is
// the decision the default driver would emit as an intent.

// IntrospectRequest is one introspection query.
type IntrospectRequest struct {
	SchemaVersion int           `json:"schema_version"`
	Vehicle       IntroVehicle  `json:"vehicle"`
	Cur           IntroLaneCtx  `json:"cur"`
	Left          *IntroSideCtx `json:"left,omitempty"`
	Right         *IntroSideCtx `json:"right,omitempty"`
}

// IntroVehicle is the ego state of a query (Lane by lane ID).
type IntroVehicle struct {
	ID       uint64  `json:"id"`
	TypeIdx  int     `json:"type_idx"`
	Lane     string  `json:"lane"`
	S        float64 `json:"s"`
	V        float64 `json:"v"`
	F        float64 `json:"f"`
	Cooldown uint64  `json:"cooldown,omitempty"`
}

// IntroLeader is a car-following leader (ok=false: free road).
type IntroLeader struct {
	OK  bool    `json:"ok"`
	Gap float64 `json:"gap"`
	V   float64 `json:"v"`
}

// IntroFollower is a resolved follower; Lead is its own car-following
// leader (after-ego-departure for the current-lane follower).
type IntroFollower struct {
	OK      bool        `json:"ok"`
	Gap     float64     `json:"gap"`
	V       float64     `json:"v"`
	F       float64     `json:"f"`
	TypeIdx int         `json:"type_idx"`
	S       float64     `json:"s"`
	Lead    IntroLeader `json:"lead"`
}

// IntroLaneCtx is the current-lane context.
type IntroLaneCtx struct {
	Lead IntroLeader   `json:"lead"`
	Foll IntroFollower `json:"foll"`
}

// IntroSideCtx is a lateral candidate lane (Lane by lane ID) with its
// resolved adjacency context.
type IntroSideCtx struct {
	Lane string        `json:"lane"`
	Lead IntroLeader   `json:"lead"`
	Foll IntroFollower `json:"foll"`
}

// IntrospectReply is the reference policy's decision for the queried state:
// exactly the intent the default driver would emit.
type IntrospectReply struct {
	SchemaVersion int     `json:"schema_version"`
	Accel         float64 `json:"accel"`
	LaneDelta     int     `json:"lane_delta"`
	Signals       int     `json:"signals"`
}

// ToPolicyCtx resolves the query against the run's network and scenario
// types into the kernel's shared policy context.
func (r *IntrospectRequest) ToPolicyCtx(net *engine.Network, types []*engine.VehicleType) (*engine.PolicyCtx, error) {
	if r.Vehicle.TypeIdx < 0 || r.Vehicle.TypeIdx >= len(types) {
		return nil, fmt.Errorf("introspect: type_idx %d out of range (%d types)", r.Vehicle.TypeIdx, len(types))
	}
	lane := net.LaneByID(r.Vehicle.Lane)
	if lane == nil {
		return nil, fmt.Errorf("introspect: unknown lane %q", r.Vehicle.Lane)
	}
	ctx := &engine.PolicyCtx{
		Type:       types[r.Vehicle.TypeIdx],
		TypeIdx:    r.Vehicle.TypeIdx,
		LaneIdx:    lane.Index,
		S:          r.Vehicle.S,
		V:          r.Vehicle.V,
		F:          r.Vehicle.F,
		Cooldown:   r.Vehicle.Cooldown,
		CurLimit:   lane.SpeedLimit,
		CurLen:     lane.Length,
		CurEndWall: lane.EndWall,
		CurLead:    introLeader(r.Cur.Lead),
	}
	var err error
	if ctx.CurFoll, err = introFollower(r.Cur.Foll, lane, types); err != nil {
		return nil, fmt.Errorf("introspect: cur.foll: %w", err)
	}
	if r.Left != nil {
		if ctx.Left, err = introSide(r.Left, types, net); err != nil {
			return nil, fmt.Errorf("introspect: left: %w", err)
		}
	}
	if r.Right != nil {
		if ctx.Right, err = introSide(r.Right, types, net); err != nil {
			return nil, fmt.Errorf("introspect: right: %w", err)
		}
	}
	return ctx, nil
}

func introLeader(l IntroLeader) engine.LeaderInfo {
	return engine.LeaderInfo{OK: l.OK, Gap: l.Gap, V: l.V}
}

func introFollower(f IntroFollower, lane *engine.Lane, types []*engine.VehicleType) (engine.FollowerCtx, error) {
	out := engine.FollowerCtx{
		OK: f.OK, Gap: f.Gap, V: f.V, F: f.F, S: f.S, TypeIdx: f.TypeIdx,
		LaneLimit: lane.SpeedLimit, LaneLen: lane.Length, LaneEndWall: lane.EndWall,
		Lead: introLeader(f.Lead),
	}
	if f.OK {
		if f.TypeIdx < 0 || f.TypeIdx >= len(types) {
			return out, fmt.Errorf("type_idx %d out of range", f.TypeIdx)
		}
		out.Type = types[f.TypeIdx]
	}
	return out, nil
}

func introSide(s *IntroSideCtx, types []*engine.VehicleType, net *engine.Network) (engine.SideCtx, error) {
	var out engine.SideCtx
	lane := net.LaneByID(s.Lane)
	if lane == nil {
		return out, fmt.Errorf("unknown lane %q", s.Lane)
	}
	out.Present = true
	out.Limit = lane.SpeedLimit
	out.Length = lane.Length
	out.EndWall = lane.EndWall
	out.Lead = introLeader(s.Lead)
	foll, err := introFollower(s.Foll, lane, types)
	if err != nil {
		return out, err
	}
	out.Foll = foll
	return out, nil
}
