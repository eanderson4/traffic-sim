package main

import (
	"encoding/binary"
	"fmt"
	"math"
)

// bakefmt.go — the TSRB v1 and TSRL v1 baked-frame wire formats (ADR-0023
// §2, §4; contracts/baked-replay-v1.md). All little-endian.
//
// TSRB v1 (baked vehicle snapshot):
//
//	header (20 B): magic u32 "TSRB" | schema_version u16 =1 | flags u16 |
//	               tick u64 | vehicle_count u32
//	per vehicle (14 B): id u32 | x u32 | y u32 | angle u8 | class u8
//
// x/y are quantized to 0.1 m steps in the network's local metric frame,
// biased by the origin carried in index.json (q = round((c−origin)/0.1));
// angle is the tangent normalized into [0,2π) then floor(q×256/2π); id is
// the engine id narrowed to u32 (the bake aborts above MaxUint32); class
// is the scenario type index (aborts above 255). A chunk is
// header+records repeated; vehicle_count is the only frame delimiter.
//
// TSRL v1 (baked lane-speed aggregate):
//
//	header (20 B): magic u32 "TSRL" | schema_version u16 =1 | flags u16 |
//	               tick u64 | pair_count u32
//	per pair (5 B): lane_idx u32 | ratio_q u8
//
// lane_idx indexes the deduped occupied-lane-id table (lanes.json);
// ratio_q = round(clamp(meanSpeed/speedLimit, 0, 1.5) × 170). Sparse:
// only lanes with ≥1 vehicle at the aggregate tick.

const (
	tsrbMagic   = 0x42525354 // "TSRB" in the byte stream
	tsrbVersion = 1
	tsrbHeader  = 20
	tsrbPerVeh  = 14

	tsrlMagic   = 0x4C525354 // "TSRL" in the byte stream
	tsrlVersion = 1
	tsrlHeader  = 20
	tsrlPerPair = 5
)

// tsrbVehicle is one TSRB vehicle record (already quantized).
type tsrbVehicle struct {
	ID    uint32
	X, Y  uint32
	Angle uint8
	Class uint8
}

// encodeTSRBFrame encodes one TSRB v1 frame.
func encodeTSRBFrame(tick uint64, vehs []tsrbVehicle) []byte {
	buf := make([]byte, tsrbHeader+tsrbPerVeh*len(vehs))
	binary.LittleEndian.PutUint32(buf[0:], tsrbMagic)
	binary.LittleEndian.PutUint16(buf[4:], tsrbVersion)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], tick)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(vehs)))
	off := tsrbHeader
	for _, v := range vehs {
		binary.LittleEndian.PutUint32(buf[off:], v.ID)
		binary.LittleEndian.PutUint32(buf[off+4:], v.X)
		binary.LittleEndian.PutUint32(buf[off+8:], v.Y)
		buf[off+12] = v.Angle
		buf[off+13] = v.Class
		off += tsrbPerVeh
	}
	return buf
}

// parseTSRBFrame decodes one TSRB frame from the head of buf and returns
// the rest (the chunk iterator's step; the test oracle).
func parseTSRBFrame(buf []byte) (tick uint64, vehs []tsrbVehicle, rest []byte, err error) {
	if len(buf) < tsrbHeader {
		return 0, nil, nil, fmt.Errorf("tsrb: %d bytes, want at least %d", len(buf), tsrbHeader)
	}
	if magic := binary.LittleEndian.Uint32(buf); magic != tsrbMagic {
		return 0, nil, nil, fmt.Errorf("tsrb: bad magic %#08x", magic)
	}
	if v := binary.LittleEndian.Uint16(buf[4:]); v != tsrbVersion {
		return 0, nil, nil, fmt.Errorf("tsrb: unsupported schema_version %d", v)
	}
	tick = binary.LittleEndian.Uint64(buf[8:])
	n := int(binary.LittleEndian.Uint32(buf[16:]))
	size := tsrbHeader + tsrbPerVeh*n
	if len(buf) < size {
		return 0, nil, nil, fmt.Errorf("tsrb: %d bytes, want %d for %d vehicles", len(buf), size, n)
	}
	vehs = make([]tsrbVehicle, n)
	off := tsrbHeader
	for i := range vehs {
		vehs[i] = tsrbVehicle{
			ID:    binary.LittleEndian.Uint32(buf[off:]),
			X:     binary.LittleEndian.Uint32(buf[off+4:]),
			Y:     binary.LittleEndian.Uint32(buf[off+8:]),
			Angle: buf[off+12],
			Class: buf[off+13],
		}
		off += tsrbPerVeh
	}
	return tick, vehs, buf[size:], nil
}

// tsrlPair is one TSRL (lane_idx, ratio_q) record.
type tsrlPair struct {
	LaneIdx uint32
	RatioQ  uint8
}

// encodeTSRLFrame encodes one TSRL v1 frame. Pairs must already be sorted
// by lane index (the encoder's output is byte-deterministic given sorted
// input).
func encodeTSRLFrame(tick uint64, pairs []tsrlPair) []byte {
	buf := make([]byte, tsrlHeader+tsrlPerPair*len(pairs))
	binary.LittleEndian.PutUint32(buf[0:], tsrlMagic)
	binary.LittleEndian.PutUint16(buf[4:], tsrlVersion)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], tick)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(pairs)))
	off := tsrlHeader
	for _, p := range pairs {
		binary.LittleEndian.PutUint32(buf[off:], p.LaneIdx)
		buf[off+4] = p.RatioQ
		off += tsrlPerPair
	}
	return buf
}

// parseTSRLFrame decodes one TSRL frame from the head of buf and returns
// the rest.
func parseTSRLFrame(buf []byte) (tick uint64, pairs []tsrlPair, rest []byte, err error) {
	if len(buf) < tsrlHeader {
		return 0, nil, nil, fmt.Errorf("tsrl: %d bytes, want at least %d", len(buf), tsrlHeader)
	}
	if magic := binary.LittleEndian.Uint32(buf); magic != tsrlMagic {
		return 0, nil, nil, fmt.Errorf("tsrl: bad magic %#08x", magic)
	}
	if v := binary.LittleEndian.Uint16(buf[4:]); v != tsrlVersion {
		return 0, nil, nil, fmt.Errorf("tsrl: unsupported schema_version %d", v)
	}
	tick = binary.LittleEndian.Uint64(buf[8:])
	n := int(binary.LittleEndian.Uint32(buf[16:]))
	size := tsrlHeader + tsrlPerPair*n
	if len(buf) < size {
		return 0, nil, nil, fmt.Errorf("tsrl: %d bytes, want %d for %d pairs", len(buf), size, n)
	}
	pairs = make([]tsrlPair, n)
	off := tsrlHeader
	for i := range pairs {
		pairs[i] = tsrlPair{LaneIdx: binary.LittleEndian.Uint32(buf[off:]), RatioQ: buf[off+4]}
		off += tsrlPerPair
	}
	return tick, pairs, buf[size:], nil
}

// quantXY quantizes one local-frame coordinate to 0.1 m steps biased by
// origin (ADR-0023 §2: q = round((c−origin)/0.1)). Out-of-u32 results are
// an error, never a truncation — same discipline as the id/class narrowing.
func quantXY(c, origin float64) (uint32, error) {
	q := math.Round((c - origin) / quantXYStepM)
	if math.IsNaN(q) || q < 0 || q > math.MaxUint32 {
		return 0, fmt.Errorf("coordinate %v out of the quantized range (origin %v, step %v)", c, origin, quantXYStepM)
	}
	return uint32(q), nil
}

// dequantXY is the decoder's inverse (test oracle).
func dequantXY(q uint32, origin float64) float64 {
	return origin + float64(q)*quantXYStepM
}

// quantAngle normalizes the tangent into [0, 2π) FIRST (math.Mod preserves
// a negative sign), then floor(q × 256 / 2π) — floor, not round, so the
// result is 0..255 and never overflows the byte (ADR-0023 §2).
func quantAngle(a float64) uint8 {
	n := math.Mod(a, 2*math.Pi)
	if n < 0 {
		n += 2 * math.Pi
	}
	q := math.Floor(n * 256 / (2 * math.Pi))
	if q > 255 { // n within fp-epsilon of 2π
		q = 255
	}
	return uint8(q)
}

// dequantAngle is the decoder's inverse (multiply back; no reserved
// values).
func dequantAngle(q uint8) float64 {
	return float64(q) * 2 * math.Pi / 256
}

// quantRatio quantizes a lane speed ratio: clamp to [0, 1.5] (mirroring
// the viz's laneSpeedRatios), then round(ratio × 170) — 1.5 lands exactly
// on 255 (ADR-0023 §4).
func quantRatio(r float64) uint8 {
	if r < 0 {
		r = 0
	}
	if r > 1.5 {
		r = 1.5
	}
	q := math.Round(r * 170)
	if q > 255 {
		q = 255
	}
	return uint8(q)
}
