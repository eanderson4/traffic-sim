package natsio

import (
	"encoding/binary"
	"fmt"
	"math"

	"traffic-sim/engine"
)

// obsframe.go — the per-controller observation frame (ADR-0008 §3): one
// binary message per tick on ts.{run}.ctl.obs.{controller_id}, shaped by the
// area-of-interest window declared at attach (radius, max neighbors,
// features). Contents: ego block per claimed vehicle (exact state), the
// resolved reference-policy context per ego (feature PolicyCtx — the same
// gather the kernel uses, so a client's decision from it equals the
// engine's), and the sorted/capped neighbor list (feature Neighbors).
//
// Binary layout v1 (all little-endian):
//
//	header (24 B): magic u32 "TSOB" | version u16 =1 | features u16 |
//	               tick u64 | n_ego u32 | n_neighbors u32
//	per ego (75 B + route): id u64 | lane_idx u32 | type_idx u32 | s f64 |
//	  v f64 | f f64 | acc f64 | cooldown u64 | signals u32 | held_turn i32 |
//	  cruise f64 | cruise_ok u8 | route_len u16 | route bytes
//	per ego, if feature PolicyCtx (317 B):
//	  cur_limit f64 | cur_len f64 | cur_endwall u8 | cur_lead (17 B) |
//	  cur_foll (71 B) | left side (106 B) | right side (106 B)
//	per neighbor (44 B): id u64 | lane_idx u32 | type_idx u32 | s f64 |
//	  v f64 | f f64 | signals u32
//
//	LeaderInfo  (17 B): ok u8 | gap f64 | v f64
//	FollowerCtx (71 B): ok u8 | gap f64 | v f64 | f f64 | type_idx u32 |
//	  s f64 | lane_limit f64 | lane_len f64 | lane_endwall u8 |
//	  lead_ok u8 | lead_gap f64 | lead_v f64
//	SideCtx    (106 B): present u8 | limit f64 | length f64 | endwall u8 |
//	  lead (17 B) | foll (71 B)

const (
	obsMagic   = 0x424f5354 // "TSOB"
	obsVersion = 1
	obsHeader  = 24
	obsEgoFix  = 75
	obsCtxSize = 317
	obsNbSize  = 44

	// ObsFeatureNeighbors requests the sorted/capped raw neighbor list.
	ObsFeatureNeighbors uint16 = 1 << 0
	// ObsFeaturePolicyCtx requests the resolved reference-policy context per
	// claimed vehicle (leader/follower contexts, cross-boundary aware).
	ObsFeaturePolicyCtx uint16 = 1 << 1
)

// ObsEgo is the exact per-claimed-vehicle block.
type ObsEgo struct {
	ID       uint64
	LaneIdx  int
	TypeIdx  int
	S, V, F  float64
	Acc      float64
	Cooldown uint64
	Signals  int
	HeldTurn int
	Cruise   float64
	CruiseOK bool
	Route    string
	Ctx      *engine.PolicyCtx // present iff the frame carries feature PolicyCtx
}

// ObsNeighbor is one raw neighbor record (lane-absolute coordinates; the
// ego block carries the ego's own lane/s for relativization).
type ObsNeighbor struct {
	ID      uint64
	LaneIdx int
	TypeIdx int
	S, V, F float64
	Signals int
}

// Obs is one decoded observation frame.
type Obs struct {
	Tick      uint64
	Egos      []ObsEgo
	Neighbors []ObsNeighbor
}

// EncodeObs serializes one observation frame. feature flags decide whether
// ego Ctx blocks and the neighbor list are emitted.
func EncodeObs(tick uint64, features uint16, egos []ObsEgo, nbs []ObsNeighbor) []byte {
	n := obsHeader + obsNbSize*len(nbs)
	for _, e := range egos {
		n += obsEgoFix + len(e.Route)
		if features&ObsFeaturePolicyCtx != 0 {
			n += obsCtxSize
		}
	}
	buf := make([]byte, n)
	binary.LittleEndian.PutUint32(buf[0:], obsMagic)
	binary.LittleEndian.PutUint16(buf[4:], obsVersion)
	binary.LittleEndian.PutUint16(buf[6:], features)
	binary.LittleEndian.PutUint64(buf[8:], tick)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(egos)))
	binary.LittleEndian.PutUint32(buf[20:], uint32(len(nbs)))
	off := obsHeader
	for i := range egos {
		off = putEgo(buf, off, &egos[i], features)
	}
	for i := range nbs {
		off = putNeighbor(buf, off, &nbs[i])
	}
	return buf
}

func putEgo(buf []byte, off int, e *ObsEgo, features uint16) int {
	binary.LittleEndian.PutUint64(buf[off:], e.ID)
	binary.LittleEndian.PutUint32(buf[off+8:], uint32(e.LaneIdx))
	binary.LittleEndian.PutUint32(buf[off+12:], uint32(e.TypeIdx))
	binary.LittleEndian.PutUint64(buf[off+16:], math.Float64bits(e.S))
	binary.LittleEndian.PutUint64(buf[off+24:], math.Float64bits(e.V))
	binary.LittleEndian.PutUint64(buf[off+32:], math.Float64bits(e.F))
	binary.LittleEndian.PutUint64(buf[off+40:], math.Float64bits(e.Acc))
	binary.LittleEndian.PutUint64(buf[off+48:], e.Cooldown)
	binary.LittleEndian.PutUint32(buf[off+56:], uint32(int32(e.Signals)))
	binary.LittleEndian.PutUint32(buf[off+60:], uint32(int32(e.HeldTurn)))
	binary.LittleEndian.PutUint64(buf[off+64:], math.Float64bits(e.Cruise))
	if e.CruiseOK {
		buf[off+72] = 1
	}
	binary.LittleEndian.PutUint16(buf[off+73:], uint16(len(e.Route)))
	off += obsEgoFix
	off += copy(buf[off:], e.Route)
	if features&ObsFeaturePolicyCtx != 0 {
		off = putPolicyCtx(buf, off, e.Ctx)
	}
	return off
}

func putBool(buf []byte, off int, b bool) int {
	if b {
		buf[off] = 1
	}
	return off + 1
}

func putF64(buf []byte, off int, x float64) int {
	binary.LittleEndian.PutUint64(buf[off:], math.Float64bits(x))
	return off + 8
}

func putLeader(buf []byte, off int, l engine.LeaderInfo) int {
	off = putBool(buf, off, l.OK)
	off = putF64(buf, off, l.Gap)
	off = putF64(buf, off, l.V)
	return off
}

func putPolicyCtx(buf []byte, off int, ctx *engine.PolicyCtx) int {
	off = putF64(buf, off, ctx.CurLimit)
	off = putF64(buf, off, ctx.CurLen)
	off = putBool(buf, off, ctx.CurEndWall)
	off = putLeader(buf, off, ctx.CurLead)
	off = putFollowerWithType(buf, off, ctx.CurFoll)
	off = putSideWithType(buf, off, ctx.Left)
	off = putSideWithType(buf, off, ctx.Right)
	return off
}

// putFollowerWithType/putSideWithType carry the follower's type INDEX (the
// wire has no pointers; the decoder re-links against the scenario types).
func putFollowerWithType(buf []byte, off int, f engine.FollowerCtx) int {
	off = putBool(buf, off, f.OK)
	off = putF64(buf, off, f.Gap)
	off = putF64(buf, off, f.V)
	off = putF64(buf, off, f.F)
	binary.LittleEndian.PutUint32(buf[off:], uint32(f.TypeIdx))
	off += 4
	off = putF64(buf, off, f.S)
	off = putF64(buf, off, f.LaneLimit)
	off = putF64(buf, off, f.LaneLen)
	off = putBool(buf, off, f.LaneEndWall)
	off = putBool(buf, off, f.Lead.OK)
	off = putF64(buf, off, f.Lead.Gap)
	off = putF64(buf, off, f.Lead.V)
	return off
}

func putSideWithType(buf []byte, off int, s engine.SideCtx) int {
	off = putBool(buf, off, s.Present)
	off = putF64(buf, off, s.Limit)
	off = putF64(buf, off, s.Length)
	off = putBool(buf, off, s.EndWall)
	off = putLeader(buf, off, s.Lead)
	off = putFollowerWithType(buf, off, s.Foll)
	return off
}

func putNeighbor(buf []byte, off int, n *ObsNeighbor) int {
	binary.LittleEndian.PutUint64(buf[off:], n.ID)
	binary.LittleEndian.PutUint32(buf[off+8:], uint32(n.LaneIdx))
	binary.LittleEndian.PutUint32(buf[off+12:], uint32(n.TypeIdx))
	binary.LittleEndian.PutUint64(buf[off+16:], math.Float64bits(n.S))
	binary.LittleEndian.PutUint64(buf[off+24:], math.Float64bits(n.V))
	binary.LittleEndian.PutUint64(buf[off+32:], math.Float64bits(n.F))
	binary.LittleEndian.PutUint32(buf[off+40:], uint32(int32(n.Signals)))
	return off + obsNbSize
}

// DecodeObs parses one observation frame, re-linking vehicle-type pointers
// against the run's scenario types (out-of-range indices are an error).
func DecodeObs(buf []byte, types []*engine.VehicleType) (Obs, error) {
	var out Obs
	if len(buf) < obsHeader {
		return out, fmt.Errorf("obs: %d bytes, want at least %d", len(buf), obsHeader)
	}
	if magic := binary.LittleEndian.Uint32(buf); magic != obsMagic {
		return out, fmt.Errorf("obs: bad magic %#08x", magic)
	}
	if v := binary.LittleEndian.Uint16(buf[4:]); v != obsVersion {
		return out, fmt.Errorf("obs: unsupported version %d", v)
	}
	features := binary.LittleEndian.Uint16(buf[6:])
	out.Tick = binary.LittleEndian.Uint64(buf[8:])
	nEgo := int(binary.LittleEndian.Uint32(buf[16:]))
	nNb := int(binary.LittleEndian.Uint32(buf[20:]))
	off := obsHeader
	out.Egos = make([]ObsEgo, 0, nEgo)
	for i := 0; i < nEgo; i++ {
		ego, noff, err := getEgo(buf, off, features, types)
		if err != nil {
			return out, fmt.Errorf("obs: ego %d: %w", i, err)
		}
		off = noff
		out.Egos = append(out.Egos, ego)
	}
	out.Neighbors = make([]ObsNeighbor, 0, nNb)
	for i := 0; i < nNb; i++ {
		if off+obsNbSize > len(buf) {
			return out, fmt.Errorf("obs: truncated neighbor %d", i)
		}
		nb := ObsNeighbor{
			ID:      binary.LittleEndian.Uint64(buf[off:]),
			LaneIdx: int(binary.LittleEndian.Uint32(buf[off+8:])),
			TypeIdx: int(binary.LittleEndian.Uint32(buf[off+12:])),
			S:       math.Float64frombits(binary.LittleEndian.Uint64(buf[off+16:])),
			V:       math.Float64frombits(binary.LittleEndian.Uint64(buf[off+24:])),
			F:       math.Float64frombits(binary.LittleEndian.Uint64(buf[off+32:])),
			Signals: int(int32(binary.LittleEndian.Uint32(buf[off+40:]))),
		}
		out.Neighbors = append(out.Neighbors, nb)
		off += obsNbSize
	}
	if off != len(buf) {
		return out, fmt.Errorf("obs: %d trailing bytes", len(buf)-off)
	}
	return out, nil
}

func getEgo(buf []byte, off int, features uint16, types []*engine.VehicleType) (ObsEgo, int, error) {
	var e ObsEgo
	if off+obsEgoFix > len(buf) {
		return e, off, fmt.Errorf("truncated ego block")
	}
	e.ID = binary.LittleEndian.Uint64(buf[off:])
	e.LaneIdx = int(binary.LittleEndian.Uint32(buf[off+8:]))
	e.TypeIdx = int(binary.LittleEndian.Uint32(buf[off+12:]))
	e.S = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+16:]))
	e.V = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+24:]))
	e.F = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+32:]))
	e.Acc = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+40:]))
	e.Cooldown = binary.LittleEndian.Uint64(buf[off+48:])
	e.Signals = int(int32(binary.LittleEndian.Uint32(buf[off+56:])))
	e.HeldTurn = int(int32(binary.LittleEndian.Uint32(buf[off+60:])))
	e.Cruise = math.Float64frombits(binary.LittleEndian.Uint64(buf[off+64:]))
	e.CruiseOK = buf[off+72] != 0
	routeLen := int(binary.LittleEndian.Uint16(buf[off+73:]))
	off += obsEgoFix
	if off+routeLen > len(buf) {
		return e, off, fmt.Errorf("truncated route")
	}
	e.Route = string(buf[off : off+routeLen])
	off += routeLen
	if e.TypeIdx < 0 || e.TypeIdx >= len(types) {
		return e, off, fmt.Errorf("type_idx %d out of range (%d types)", e.TypeIdx, len(types))
	}
	if features&ObsFeaturePolicyCtx != 0 {
		ctx, noff, err := getPolicyCtx(buf, off, &e, types)
		if err != nil {
			return e, off, err
		}
		off = noff
		e.Ctx = ctx
	}
	return e, off, nil
}

type byteCursor struct {
	buf []byte
	off int
	err error
}

func (c *byteCursor) ok() bool {
	if c.off+1 > len(c.buf) {
		c.err = fmt.Errorf("truncated")
		return false
	}
	b := c.buf[c.off] != 0
	c.off++
	return b
}
func (c *byteCursor) f64() float64 {
	if c.off+8 > len(c.buf) {
		c.err = fmt.Errorf("truncated")
		return 0
	}
	x := math.Float64frombits(binary.LittleEndian.Uint64(c.buf[c.off:]))
	c.off += 8
	return x
}
func (c *byteCursor) u32() int {
	if c.off+4 > len(c.buf) {
		c.err = fmt.Errorf("truncated")
		return 0
	}
	x := int(binary.LittleEndian.Uint32(c.buf[c.off:]))
	c.off += 4
	return x
}

func (c *byteCursor) leader() engine.LeaderInfo {
	return engine.LeaderInfo{OK: c.ok(), Gap: c.f64(), V: c.f64()}
}

func (c *byteCursor) follower(types []*engine.VehicleType) (engine.FollowerCtx, error) {
	var f engine.FollowerCtx
	f.OK = c.ok()
	f.Gap = c.f64()
	f.V = c.f64()
	f.F = c.f64()
	ti := c.u32()
	f.S = c.f64()
	f.LaneLimit = c.f64()
	f.LaneLen = c.f64()
	f.LaneEndWall = c.ok()
	f.Lead = engine.LeaderInfo{OK: c.ok(), Gap: c.f64(), V: c.f64()}
	if c.err != nil {
		return f, c.err
	}
	if f.OK {
		if ti < 0 || ti >= len(types) {
			return f, fmt.Errorf("follower type_idx %d out of range", ti)
		}
		f.Type = types[ti]
		f.TypeIdx = ti
	}
	return f, nil
}

func (c *byteCursor) side(types []*engine.VehicleType) (engine.SideCtx, error) {
	var s engine.SideCtx
	s.Present = c.ok()
	s.Limit = c.f64()
	s.Length = c.f64()
	s.EndWall = c.ok()
	s.Lead = c.leader()
	foll, err := c.follower(types)
	if err != nil {
		return s, err
	}
	s.Foll = foll
	return s, c.err
}

func getPolicyCtx(buf []byte, off int, ego *ObsEgo, types []*engine.VehicleType) (*engine.PolicyCtx, int, error) {
	c := &byteCursor{buf: buf, off: off}
	ctx := &engine.PolicyCtx{
		Type:     types[ego.TypeIdx],
		TypeIdx:  ego.TypeIdx,
		LaneIdx:  ego.LaneIdx,
		S:        ego.S,
		V:        ego.V,
		F:        ego.F,
		Cooldown: ego.Cooldown,
	}
	ctx.CurLimit = c.f64()
	ctx.CurLen = c.f64()
	ctx.CurEndWall = c.ok()
	ctx.CurLead = c.leader()
	foll, err := c.follower(types)
	if err != nil {
		return nil, off, fmt.Errorf("cur_foll: %w", err)
	}
	ctx.CurFoll = foll
	left, err := c.side(types)
	if err != nil {
		return nil, off, fmt.Errorf("left: %w", err)
	}
	ctx.Left = left
	right, err := c.side(types)
	if err != nil {
		return nil, off, fmt.Errorf("right: %w", err)
	}
	ctx.Right = right
	if c.err != nil {
		return nil, off, c.err
	}
	return ctx, c.off, nil
}
