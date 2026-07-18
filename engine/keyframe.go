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
//
// Payloads stay far under the 1 MB max_payload discipline (ADR-0002) at
// current network sizes (~70 B/vehicle); the measured byte curve and the
// chunking decision past it are tracked in engine/BENCHMARKS.md.

const (
	// keyframeMagic spells "TSKF" in the byte stream (little-endian u32).
	keyframeMagic = 0x464b5354
	// keyframeVersion 2 adds the persistent controller axes per vehicle.
	keyframeVersion = 2
)

// MarshalState serializes the engine's complete dynamic state at the
// current tick boundary.
func (e *Engine) MarshalState() ([]byte, error) {
	w := &byteWriter{}
	w.u32(keyframeMagic)
	w.u16(keyframeVersion)
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
	return w.buf, nil
}

// RestoreState rebuilds an engine at a keyframe: the static structure
// (network, scenario, spawner shape) comes from the spec — exactly what
// NewEngine builds — and the dynamic state is then overwritten from the
// keyframe bytes. The spec must be the run's own spec; the keyframe does
// not duplicate it (the record plane pairs them: run registry ↔ log stream).
func RestoreState(spec RunSpec, data []byte) (*Engine, error) {
	r := &byteReader{buf: data}
	if magic := r.u32(); magic != keyframeMagic {
		return nil, fmt.Errorf("keyframe: bad magic %#08x", magic)
	}
	if v := r.u16(); v != keyframeVersion {
		return nil, fmt.Errorf("keyframe: unsupported version %d", v)
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
