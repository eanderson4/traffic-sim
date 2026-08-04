package engine

import (
	"encoding/binary"
	"fmt"
	"math"
)

// keyframe.go — full-state snapshots (ADR-0005 §5): a keyframe carries the
// complete dynamic state needed to resume the run bit-exactly, so replay
// can seek to the nearest keyframe ≤ target tick and re-simulate forward.
// Everything the rolling CRC covers is here, plus what the CRC deliberately
// omits but the future trajectory needs (raw RNG words, spawner demand
// state) and, since v2, the persistent controller axes (cruise setpoint,
// held turn, signals, route — ADR-0008 §2). Stats are observability, not
// world state: a restored engine starts them fresh.
//
// Binary layout v2 (all little-endian, all integers fixed-size):
//
//	header:  magic u32 "TSKF" | version u16 =2 | flags u16 =0
//	         tick u64 | crc u64 | nextID u64
//	         nVehicles u32 | nOrigins u32 | spawnerSchedNext u32 | reserved u32
//	per vehicle (ID order, matching the CRC's canonical order):
//	  id u64 | laneIdx u32 | typeIdx u32 | S f64 | V f64 | F f64 |
//	  cooldown u64 | rngDraws u64 | rngLen u8 | rng bytes (PCG state) |
//	  cruise f64 | cruiseOK u8 | heldTurn i32 | signals u32 |
//	  routeLen u16 | route bytes
//	per origin lane (spawner order):
//	  laneIdx u32 | rate f64 | spawnTick u64 | pendID u64 | pendRngDraws u64 |
//	  pendRngLen u8 | pend rng bytes
//	v5 only (written solely while some vehicle has a running stuck timer or
//	a discharged stop duty), appended to each vehicle record:
//	  stuckTicks u64 | stopDone u8
//	v6 only (written solely while Params.AdaptiveRouting is on), appended to
//	each vehicle record after the v5 fields:
//	  laneEntryTick u64
//	v6 only, a lane section between the spawner and director sections:
//	  nLanes u32 | per lane (network order): ttEMA f64 | ttSnap f64
//	v3 only (written solely while the director queue is non-empty):
//	  nDirectives u32 | per directive: tick u64 | laneIdx u32 | typeIdx u32 |
//	  earliestTick u64 | reqIDLen u16 | reqID bytes
//
// Payloads run ≥92 B/vehicle measured (engine/BENCHMARKS.md); past the
// broker's 1 MiB max_payload (~10.9k vehicles) the record plane chunks the
// keyframe into consecutive log messages (ADR-0015) — this codec is
// unchanged: chunks reassemble to exactly these bytes.

const (
	// keyframeMagic spells "TSKF" in the byte stream (little-endian u32).
	keyframeMagic = 0x464b5354
	// keyframeVersion is the current write version. Version 2 added the
	// persistent controller axes per vehicle. Version 3 (written only
	// while the director injection queue is non-empty — an empty queue
	// marshals byte-identical v2) appends the pending directives:
	//
	//	nDirectives u32 | per directive:
	//	  tick u64 | laneIdx u32 | typeIdx u32 | earliestTick u64 |
	//	  reqIDLen u16 | reqID bytes
	//
	// Version 4 (ADR-0021, written only while some QUEUED directive carries
	// a destination or a non-zero injection offset — a queue of plain
	// portal spawns still marshals byte-identical v3) appends per directive:
	//
	//	destLen u16 | dest bytes | offsetM f64
	// v5 appends two per-vehicle fields:
	//
	//	stuckTicks u64 | stopDone u8
	//
	// Written only when some vehicle needs one of them, so a state with no
	// timer running and no stop-duty discharged still marshals
	// byte-identical to v4.
	//
	// stuckTicks decides WHETHER A VEHICLE EXISTS: strandStuck removes one
	// that reaches StrandAfterS. ReplayFromStream and Player.seek both
	// restore from the latest keyframe at or before their target and then
	// re-simulate forward verifying every logged CRC, so a timer reset to
	// zero by the restore strands the vehicle StrandAfterS later than the
	// recorded run did — and every tick between the two has a vehicle in one
	// and not the other. That is a CRC divergence and a failed replay, not a
	// rounding difference.
	//
	// stopDone is here for the same reason, arrived at the hard way. It was
	// left out on the grounds that it is derived state whose worst case is
	// "the vehicle stops twice" — but under ADR-0029's bit-exact criterion
	// stopping twice IS the bug. A vehicle that has discharged its stop duty
	// and is ALREADY MOVING toward the line restores with stopDone=false,
	// cannot re-satisfy the `V == 0 && dist <= S0+1` test that sets it, and
	// so must brake to a full stop a second time. That is a different
	// trajectory from the run being continued. (A vehicle still standing at
	// the line merely loses one tick — which is also not bit-exact.)
	// Caught in review; the comment that used to justify the omission was
	// the reason nobody looked twice.
	//
	// v6 (ADR-0036, written ONLY while Params.AdaptiveRouting is on — flag
	// off keeps the v5/v4/v3 bytes exactly) appends one per-vehicle field
	// after the v5 fields and a lane section between the spawner and
	// director sections:
	//
	//	per vehicle: laneEntryTick u64
	//	lanes: nLanes u32 | per lane (network order): ttEMA f64 | ttSnap f64
	//
	// Both decide what vehicles DO — the dwell clock feeds the travel-time
	// samples, the travel times weight the epoch recompute — so both are
	// keyframed on the stuckTicks precedent: anything that decides behavior
	// must survive a restore exactly. ttEMA is float64, no quantization.
	// The next-hop tables and epoch stamps are NOT here: they are derived
	// state, recomputed identically from tick and keyframed state.
	keyframeVersion = 6
	// keyframeAdaptiveVersion is the version the ADR-0036 adaptive-routing
	// state (laneEntryTick, ttEMA) needs.
	keyframeAdaptiveVersion = 6
	// keyframeStuckVersion is the version the stuck timer needs.
	keyframeStuckVersion = 5
	// keyframeDestVersion is the version a director queue entry carrying the
	// ADR-0021 destination/offset fields needs.
	keyframeDestVersion = 4
	// keyframeQueueVersion is the version a non-empty director queue needs
	// when none of its entries use the ADR-0021 fields.
	keyframeQueueVersion = 3
	// keyframeMinVersion is the oldest readable version (v2 recordings
	// predate the director path; their queue is empty by definition).
	keyframeMinVersion = 2
)

// MarshalState serializes the engine's complete dynamic state at the
// current tick boundary.
func (e *Engine) MarshalState() ([]byte, error) {
	w := &byteWriter{}
	w.u32(keyframeMagic)
	version := uint16(keyframeMinVersion)
	if len(e.dirQueue) > 0 {
		version = keyframeQueueVersion
		for _, d := range e.dirQueue {
			if d.Destination != "" || d.OffsetM != 0 {
				version = keyframeDestVersion
				break
			}
		}
	}
	// Lowest version that can represent this state, same rule as above: a
	// state with no timer running does not need v5 and stays byte-identical
	// to what v4 wrote.
	for _, v := range e.order {
		if v.stuckTicks > 0 || v.stopDone {
			version = keyframeStuckVersion
			break
		}
	}
	// Adaptive routing (ADR-0036) needs v6 unconditionally: ttEMA exists on
	// every lane from the first tick, so there is no "nothing to record"
	// state to stay below v6 with. Flag off never reaches this and keeps
	// the v5/v4/v3 bytes exactly.
	if e.Params.AdaptiveRouting {
		version = keyframeAdaptiveVersion
	}
	w.u16(version)
	w.u16(0)
	w.u64(e.Tick)
	w.u64(e.crc)
	w.u64(e.nextID)
	nOrigins := 0
	if e.spawner != nil {
		nOrigins = len(e.spawner.origins)
	}
	w.u32(uint32(len(e.order)))
	w.u32(uint32(nOrigins))
	schedNext := uint32(0)
	if e.spawner != nil {
		schedNext = uint32(e.spawner.next)
	}
	w.u32(schedNext)
	w.u32(0) // reserved
	for _, v := range e.order {
		w.u64(v.ID)
		w.u32(uint32(v.Lane.Index))
		w.u32(uint32(v.TypeIdx))
		w.f64(v.S)
		w.f64(v.V)
		w.f64(v.F)
		w.u64(v.Cooldown)
		rngBytes, err := v.rng.marshalStream()
		if err != nil {
			return nil, fmt.Errorf("keyframe: marshal rng vehicle %d: %w", v.ID, err)
		}
		w.u64(v.rng.Draws())
		w.u8(uint8(len(rngBytes)))
		w.bytes(rngBytes)
		// Persistent controller axes (ADR-0008 §2, keyframe v2).
		w.f64(v.Cruise)
		if v.CruiseOK {
			w.u8(1)
		} else {
			w.u8(0)
		}
		w.u32(uint32(int32(v.HeldTurn)))
		w.u32(uint32(v.Signals))
		w.u16(uint16(len(v.Route)))
		w.bytes([]byte(v.Route))
		if version >= keyframeStuckVersion {
			w.u64(v.stuckTicks) // ADR-0034
			if v.stopDone {
				w.u8(1)
			} else {
				w.u8(0)
			}
		}
		if version >= keyframeAdaptiveVersion {
			w.u64(v.laneEntryTick) // ADR-0036 dwell clock
		}
	}
	if e.spawner != nil {
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			w.u32(uint32(st.lane.Index))
			w.f64(st.rate)
			w.u64(st.tick)
			w.u64(st.pend.ID)
			rngBytes, err := st.pend.rng.marshalStream()
			if err != nil {
				return nil, fmt.Errorf("keyframe: marshal rng pending %d: %w", st.pend.ID, err)
			}
			w.u64(st.pend.rng.Draws())
			w.u8(uint8(len(rngBytes)))
			w.bytes(rngBytes)
		}
	}
	// TSKF v6 (ADR-0036): the per-lane travel-time EMAs and their
	// epoch-frozen routing snapshots, in network order. Placed between the
	// spawner and director sections — the reader is gated on the same
	// version condition, so the two never disagree. ttSnap is keyframed
	// for the same reason ttEMA is: it decides what vehicles do, and a
	// table rebuilt after a restore must be the table the live engine was
	// serving.
	if version >= keyframeAdaptiveVersion {
		w.u32(uint32(len(e.Net.Lanes)))
		for _, l := range e.Net.Lanes {
			w.f64(l.ttEMA)
			w.f64(e.ttSnap[l.Index])
		}
	}
	// TSKF v3+: pending director directives (seek must resume the injection
	// queue bit-exactly). Gated on the VERSION, not on the queue being
	// non-empty, because the reader is gated on the version — and since v5
	// the two are no longer the same condition. A running stuck timer alone
	// now lifts a state to v5, so a v5 state with an empty queue would leave
	// the reader taking a count out of bytes that were never written: a short
	// read that latches r.err, yields n = 0 by accident, and passes only
	// because this is the last section and nothing re-checks r.err after it.
	// Writing an explicit 0 keeps writer and reader on the same condition.
	// v3 and v4 are unaffected — for them version >= 3 still implies a
	// non-empty queue, so their bytes are unchanged.
	if version >= keyframeQueueVersion {
		w.u32(uint32(len(e.dirQueue))) // 0 when empty: writer and reader share one condition
		for _, d := range e.dirQueue {
			w.u64(d.Tick)
			w.u32(uint32(d.LaneIdx))
			w.u32(uint32(d.TypeIdx))
			w.u64(d.EarliestTick)
			w.u16(uint16(len(d.RequestID)))
			w.bytes([]byte(d.RequestID))
			if version >= keyframeDestVersion {
				w.u16(uint16(len(d.Destination)))
				w.bytes([]byte(d.Destination))
				w.f64(d.OffsetM)
			}
		}
	}
	return w.buf, nil
}

// RestoreState rebuilds an engine at a keyframe: the static structure
// (network, scenario, spawner shape) comes from the spec — exactly what
// NewEngine builds — and the dynamic state is then overwritten from the
// keyframe bytes. The spec must be the run's own spec; the keyframe does
// not duplicate it (the record plane pairs them: run registry ↔ log stream).
func RestoreState(spec RunSpec, data []byte) (*Engine, error) {
	return restoreState(spec, data, nil)
}

// restoreState is RestoreState with an optional guard on the network the
// spec builds, run after the network exists and BEFORE any keyframe bytes
// are applied (warm start's network fingerprint — warmstart.go). A guard
// that rejects yields an error and no engine.
func restoreState(spec RunSpec, data []byte, checkNet func(*Network) error) (*Engine, error) {
	r := &byteReader{buf: data}
	if magic := r.u32(); magic != keyframeMagic {
		return nil, fmt.Errorf("keyframe: bad magic %#08x", magic)
	}
	ver := r.u16()
	if ver < keyframeMinVersion || ver > keyframeVersion {
		return nil, fmt.Errorf("keyframe: unsupported version %d", ver)
	}
	r.u16() // flags
	tick := r.u64()
	crc := r.u64()
	nextID := r.u64()
	nVehicles := r.u32()
	nOrigins := r.u32()
	schedNext := r.u32()
	r.u32() // reserved
	if r.err != nil {
		return nil, fmt.Errorf("keyframe: truncated header: %w", r.err)
	}

	e, err := NewEngine(spec)
	if err != nil {
		return nil, err
	}
	// A v6 payload is written only by a flag-ON engine (ADR-0036); reading
	// one into a flag-OFF spec would silently drop the travel-time state
	// the run's routing depends on. Refuse the mismatch loudly instead.
	if ver >= keyframeAdaptiveVersion && !e.Params.AdaptiveRouting {
		return nil, fmt.Errorf("keyframe: v%d payload carries adaptive-routing state but the spec has AdaptiveRouting off", ver)
	}
	// The reverse direction is the designed migration path (ADR-0036: a
	// pre-v6 payload into a flag-ON spec seeds every dwell clock at the
	// restore tick), but since the default flipped ON it also fires on a
	// plain -state-in of an old static keyframe — record it for the edge
	// caller to surface (the sim core cannot log, ADR-0005).
	if ver < keyframeAdaptiveVersion && e.Params.AdaptiveRouting {
		e.RestoreNotice = fmt.Sprintf("keyframe: pre-v%d (static-routing) payload restored into an adaptive-routing spec — routing switches regime at this restore; pin adaptive_routing: false to continue bit-exactly", keyframeAdaptiveVersion)
	}
	if checkNet != nil {
		if err := checkNet(e.Net); err != nil {
			return nil, err
		}
	}
	if int(nOrigins) != len(e.spawnerOrigins()) {
		return nil, fmt.Errorf("keyframe: %d origins, spec builds %d", nOrigins, len(e.spawnerOrigins()))
	}

	e.Tick = tick
	e.crc = crc
	e.nextID = nextID
	e.order = e.order[:0]
	e.index = make(map[uint64]*Vehicle, nVehicles)
	for i := uint32(0); i < nVehicles; i++ {
		id := r.u64()
		laneIdx := r.u32()
		typeIdx := r.u32()
		s := r.f64()
		vv := r.f64()
		f := r.f64()
		cooldown := r.u64()
		rngDraws := r.u64()
		rngState := r.bytesN(int(r.u8()))
		if r.err != nil {
			return nil, fmt.Errorf("keyframe: vehicle %d: %w", i, r.err)
		}
		if int(laneIdx) >= len(e.Net.Lanes) {
			return nil, fmt.Errorf("keyframe: vehicle %d lane index %d out of range", id, laneIdx)
		}
		if int(typeIdx) >= len(e.scen.Types) {
			return nil, fmt.Errorf("keyframe: vehicle %d type index %d out of range", id, typeIdx)
		}
		stream, err := unmarshalStream(rngState, rngDraws)
		if err != nil {
			return nil, fmt.Errorf("keyframe: vehicle %d rng: %w", id, err)
		}
		cruise := r.f64()
		cruiseOK := r.u8() != 0
		heldTurn := int(int32(r.u32()))
		signals := int(r.u32())
		route := string(r.bytesN(int(r.u16())))
		var stuckTicks uint64
		var stopDone bool
		if ver >= keyframeStuckVersion {
			stuckTicks = r.u64() // ADR-0034
			stopDone = r.u8() != 0
		}
		// A pre-v6 keyframe carries no dwell clock. Restoring one into a
		// flag-ON spec with laneEntryTick = 0 would make every vehicle's
		// first departure a capped run-long poison sample; starting the
		// clock at the restore tick is the honest "no evidence yet".
		laneEntryTick := tick
		if ver >= keyframeAdaptiveVersion {
			laneEntryTick = r.u64() // ADR-0036 dwell clock
		}
		if r.err != nil {
			return nil, fmt.Errorf("keyframe: vehicle %d controller state: %w", i, r.err)
		}
		e.register(&Vehicle{
			ID:       id,
			Type:     e.scen.Types[typeIdx],
			TypeIdx:  int(typeIdx),
			Lane:     e.Net.Lanes[laneIdx],
			S:        s,
			V:        vv,
			F:        f,
			Cooldown: cooldown,
			rng:      stream,
			Cruise:   cruise,
			CruiseOK: cruiseOK,
			HeldTurn: heldTurn,
			Signals:  signals,
			Route:    route,

			stuckTicks: stuckTicks,
			stopDone:   stopDone,

			laneEntryTick: laneEntryTick,
		})
	}
	if e.spawner != nil {
		e.spawner.next = int(schedNext)
		for i := range e.spawner.origins {
			st := &e.spawner.origins[i]
			laneIdx := r.u32()
			rate := r.f64()
			spawnTick := r.u64()
			pendID := r.u64()
			pendDraws := r.u64()
			pendRng := r.bytesN(int(r.u8()))
			if r.err != nil {
				return nil, fmt.Errorf("keyframe: origin %d: %w", i, r.err)
			}
			if int(laneIdx) >= len(e.Net.Lanes) || e.spawner.origins[i].lane != e.Net.Lanes[laneIdx] {
				return nil, fmt.Errorf("keyframe: origin %d lane mismatch", i)
			}
			stream, err := unmarshalStream(pendRng, pendDraws)
			if err != nil {
				return nil, fmt.Errorf("keyframe: origin %d rng: %w", i, err)
			}
			st.rate = rate
			st.tick = spawnTick
			st.pend = &Vehicle{ID: pendID, rng: stream}
		}
	}
	// TSKF v6 (ADR-0036): the per-lane travel-time EMAs, in network order,
	// between the spawner and director sections — writer and reader are
	// gated on the same version condition, so the two never disagree. The
	// count must equal the lane count of the network the spec built, or
	// this keyframe pairs with the wrong spec.
	if ver >= keyframeAdaptiveVersion {
		nLanes := r.u32()
		if r.err != nil {
			return nil, fmt.Errorf("keyframe: lane count: %w", r.err)
		}
		if int(nLanes) != len(e.Net.Lanes) {
			return nil, fmt.Errorf("keyframe: %d lanes, spec builds %d", nLanes, len(e.Net.Lanes))
		}
		for _, l := range e.Net.Lanes {
			l.ttEMA = r.f64()
			e.ttSnap[l.Index] = r.f64()
		}
		if r.err != nil {
			return nil, fmt.Errorf("keyframe: lane section: %w", r.err)
		}
	}
	if ver >= 3 {
		// Pending director directives resume with the queue (their recorded
		// applied ticks and request ids ride along so the CRC chain and any
		// later keyframe stay bit-identical to the live run).
		n := r.u32()
		// Check the read BEFORE trusting n. A short read here returns 0,
		// which is indistinguishable from a genuinely empty queue and would
		// make a truncated or writer/reader-mismatched payload restore
		// silently as "no pending directives" — the exact failure the v5
		// version gate could have introduced, and one no later check catches
		// because this is the last section in the payload.
		if r.err != nil {
			return nil, fmt.Errorf("keyframe: director queue count: %w", r.err)
		}
		for i := uint32(0); i < n; i++ {
			d := TickedSpawn{
				Tick:    r.u64(),
				LaneIdx: int(r.u32()),
				TypeIdx: int(r.u32()),
			}
			d.EarliestTick = r.u64()
			d.RequestID = string(r.bytesN(int(r.u16())))
			if ver >= keyframeDestVersion {
				d.Destination = string(r.bytesN(int(r.u16())))
				d.OffsetM = r.f64()
			}
			if r.err != nil {
				return nil, fmt.Errorf("keyframe: directive %d: %w", i, r.err)
			}
			if d.LaneIdx >= len(e.Net.Lanes) || d.TypeIdx >= len(e.scen.Types) {
				return nil, fmt.Errorf("keyframe: directive %d lane/type index out of range", i)
			}
			d.Origin = e.Net.Lanes[d.LaneIdx].ID
			d.TypeName = e.scen.Types[d.TypeIdx].Name
			e.dirQueue = append(e.dirQueue, d)
		}
	}
	if len(r.buf) != 0 {
		return nil, fmt.Errorf("keyframe: %d trailing bytes", len(r.buf))
	}
	e.rebuildOccupancy()
	return e, nil
}

// spawnerOrigins reports the origin count for shape checks (nil-safe).
func (e *Engine) spawnerOrigins() []originState {
	if e.spawner == nil {
		return nil
	}
	return e.spawner.origins
}

// byteWriter/byteReader keep the codec free of error-check noise; the first
// short read sticks in err.
type byteWriter struct{ buf []byte }

func (w *byteWriter) u8(x uint8)   { w.buf = append(w.buf, x) }
func (w *byteWriter) u16(x uint16) { w.buf = binary.LittleEndian.AppendUint16(w.buf, x) }
func (w *byteWriter) u32(x uint32) { w.buf = binary.LittleEndian.AppendUint32(w.buf, x) }
func (w *byteWriter) u64(x uint64) { w.buf = binary.LittleEndian.AppendUint64(w.buf, x) }
func (w *byteWriter) f64(x float64) {
	w.buf = binary.LittleEndian.AppendUint64(w.buf, math.Float64bits(x))
}
func (w *byteWriter) bytes(b []byte) { w.buf = append(w.buf, b...) }

type byteReader struct {
	buf []byte
	err error
}

func (r *byteReader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if len(r.buf) < n {
		r.err = fmt.Errorf("short read: want %d bytes, have %d", n, len(r.buf))
		return nil
	}
	b := r.buf[:n]
	r.buf = r.buf[n:]
	return b
}

func (r *byteReader) u8() uint8 {
	b := r.take(1)
	if b == nil {
		return 0
	}
	return b[0]
}
func (r *byteReader) u16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}
func (r *byteReader) u32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}
func (r *byteReader) u64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}
func (r *byteReader) f64() float64 { return math.Float64frombits(r.u64()) }
func (r *byteReader) bytesN(n int) []byte {
	b := r.take(n)
	if b == nil {
		return nil
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}
