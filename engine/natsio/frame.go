// Package natsio connects the deterministic engine kernel to the NATS
// backbone per ADR-0006: the live plane (core NATS: self-sufficient binary
// snapshots out, raw controller intents in, plus the M9 signal-program
// table on ts.{run}.state.sig — program definitions from which clients
// derive light states by the tick, ADR-0011 §1), the record plane
// (JetStream: arbitrated intent log + keyframes + rolling CRC + control
// events, engine sole writer), and the run registry (KV), plus CRC-verified
// replay from the JetStream record. On top of those planes it implements
// the ADR-0008 engine↔controller contract (contract.go): the attach
// handshake with grants, exclusive engine-arbitrated claims, per-controller
// observation windows, per-axis intent persistence with hold-last healing,
// the (grant, vehicle ID) tie-break, liveness detachment with
// unclaimed-vehicle events, and the capacity pause gate. The external
// default driver (the reference controller) lives in the subpackage
// engine/natsio/driver.
//
// Dependency justification (AGENTS.md "standard library first; justify
// dependencies"): these are the first non-stdlib dependencies of the Go
// engine, and they are exactly the two official NATS modules ADR-0006
// mandates — github.com/nats-io/nats.go (the official client) and
// github.com/nats-io/nats-server/v2 (the official server, used embedded
// in-process for tests and single-binary demos per ADR-0006 §8). The kernel
// package (traffic-sim/engine) remains stdlib-only; every NATS import lives
// in this package and its tests.
package natsio

import (
	"encoding/binary"
	"fmt"
	"math"

	"traffic-sim/engine"
)

// frame.go — the live-plane snapshot frame (ADR-0006 §7): binary SoA,
// self-sufficient per message (no deltas on core NATS), schema_version in
// the header. Layout v1 (all little-endian):
//
//	header (24 B): magic u32 "TSSF" | schema_version u16 =1 | flags u16 =0 |
//	               tick u64 | vehicle_count u32 | reserved u32
//	per vehicle (24 B): id u64 | x f32 | y f32 | angle f32 | class f32
//
// x/y/angle project the vehicle's (laneId, s) through the lane polyline
// (Lane.Project): real metric coordinates, north-up, angle = tangent in
// radians (0 = +x/east, CCW positive). Lanes WITHOUT polylines — the
// in-code M1–M3 test networks — still use the placeholder projection (see
// LaneGeoms): x is chain-offset arc length, y a 3.5 m lateral slot. The
// schema is unchanged (values-only change; TSSF stays v1). class is the
// scenario type index (0 = car, …) carried as f32 per the ADR's 8–16
// B/vehicle float framing.

const (
	frameMagic   = 0x46535354 // "TSSF" in the byte stream
	frameVersion = 1
	frameHeader  = 24
	framePerVeh  = 24
)

// LaneGeom is the deterministic per-lane placement used for the placeholder
// projection on lanes that carry no polyline: longitudinal chain offset and
// lateral slot index.
type LaneGeom struct {
	XOff float64 // sum of predecessor chain lengths (0 on the ring/origin)
	Y    float64 // lateral slot (m): 3.5 × right-chain distance
}

// LaneGeoms computes the placeholder projection for a network, indexed by
// Lane.Index. Cycle-safe (the ring's self-loop resolves to offset 0). Only
// lanes without polylines consult it — shaped lanes project through their
// own geometry in SnapshotFrame.
func LaneGeoms(net *engine.Network) []LaneGeom {
	lanes := net.Lanes
	geoms := make([]LaneGeom, len(lanes))
	for _, l := range lanes {
		off := 0.0
		seen := map[*engine.Lane]bool{l: true}
		cur := l
		for len(cur.Prevs) > 0 {
			prev := cur.Prevs[0]
			if seen[prev] {
				break
			}
			seen[prev] = true
			off += prev.Length
			cur = prev
		}
		lat := 0
		cur = l
		for cur.Right != nil && cur.Right != cur && lat < len(lanes) {
			cur = cur.Right
			lat++
		}
		geoms[l.Index] = LaneGeom{XOff: off, Y: 3.5 * float64(lat)}
	}
	return geoms
}

// SnapshotFrame encodes the current world state as a binary SoA frame.
func SnapshotFrame(e *engine.Engine, geoms []LaneGeom) []byte {
	vehs := e.Vehicles()
	buf := make([]byte, frameHeader+framePerVeh*len(vehs))
	binary.LittleEndian.PutUint32(buf[0:], frameMagic)
	binary.LittleEndian.PutUint16(buf[4:], frameVersion)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], e.Tick)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(vehs)))
	off := frameHeader
	for _, v := range vehs {
		binary.LittleEndian.PutUint64(buf[off:], v.ID)
		if x, y, angle, ok := v.Lane.Project(v.S); ok {
			putF32(buf[off+8:], x)
			putF32(buf[off+12:], y)
			putF32(buf[off+16:], angle)
		} else {
			g := geoms[v.Lane.Index] // placeholder: no polyline on this lane
			putF32(buf[off+8:], g.XOff+v.S)
			putF32(buf[off+12:], g.Y)
			putF32(buf[off+16:], 0) // all in-code M1 lanes run +x
		}
		putF32(buf[off+20:], float64(v.TypeIdx))
		off += framePerVeh
	}
	return buf
}

// FrameVehicle is one decoded vehicle record.
type FrameVehicle struct {
	ID          uint64
	X, Y, Angle float32
	Class       float32
}

// Frame is a decoded snapshot frame.
type Frame struct {
	Tick     uint64
	Vehicles []FrameVehicle
}

// ParseFrame decodes a snapshot frame (client side; also the test oracle).
func ParseFrame(buf []byte) (Frame, error) {
	if len(buf) < frameHeader {
		return Frame{}, fmt.Errorf("frame: %d bytes, want at least %d", len(buf), frameHeader)
	}
	if magic := binary.LittleEndian.Uint32(buf); magic != frameMagic {
		return Frame{}, fmt.Errorf("frame: bad magic %#08x", magic)
	}
	if v := binary.LittleEndian.Uint16(buf[4:]); v != frameVersion {
		return Frame{}, fmt.Errorf("frame: unsupported schema_version %d", v)
	}
	tick := binary.LittleEndian.Uint64(buf[8:])
	n := int(binary.LittleEndian.Uint32(buf[16:]))
	if len(buf) != frameHeader+framePerVeh*n {
		return Frame{}, fmt.Errorf("frame: %d bytes, want %d for %d vehicles", len(buf), frameHeader+framePerVeh*n, n)
	}
	f := Frame{Tick: tick, Vehicles: make([]FrameVehicle, n)}
	off := frameHeader
	for i := range f.Vehicles {
		f.Vehicles[i] = FrameVehicle{
			ID:    binary.LittleEndian.Uint64(buf[off:]),
			X:     math.Float32frombits(binary.LittleEndian.Uint32(buf[off+8:])),
			Y:     math.Float32frombits(binary.LittleEndian.Uint32(buf[off+12:])),
			Angle: math.Float32frombits(binary.LittleEndian.Uint32(buf[off+16:])),
			Class: math.Float32frombits(binary.LittleEndian.Uint32(buf[off+20:])),
		}
		off += framePerVeh
	}
	return f, nil
}

func putF32(b []byte, x float64) {
	binary.LittleEndian.PutUint32(b, math.Float32bits(float32(x)))
}
