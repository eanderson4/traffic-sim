package natsio

import (
	"encoding/binary"
	"fmt"

	"traffic-sim/engine"
)

// sigframe.go — the live-plane signal-program frame (ADR-0006, 2026-07-20
// M9 addendum; ADR-0011 §1). Fixed-time light state is a PURE FUNCTION of
// the tick count and the compiled program, so the wire carries the program
// TABLE, never per-tick states: one small self-sufficient frame on
// ts.{run}.state.sig at run start and republished at the keyframe cadence
// (the late-joiner resync rhythm), and clients derive any tick's states by
// the same integer math as the kernel (PhaseAt/StateAt mirror
// engine/signal.go exactly). The 10 Hz vehicle path is untouched — an old
// client decodes new streams without error: it simply never subscribes the
// sig subject (TSSF v1 and TSKF v2 are byte-identical).
//
// Layout v1 (all little-endian), magic "TSSG":
//
//	header (24 B): magic u32 | schema_version u16 =1 | flags u16 =0 |
//	               tick u64 | program_count u32 | reserved u32
//	per program (Network.Signals file order):
//	  id_len u8 | id bytes | junction_len u8 | junction bytes |
//	  offset_ticks u64 | phase_count u16 | link_count u16 |
//	  per phase: duration_ticks u32 | state_len u8 | state bytes (ASCII,
//	    SUMO tlLogic alphabet: g/G go, y amber, r red, anything else off)
//	  per link (signal-bound internal lanes, link-index order):
//	    link_idx u16 | lane_id_len u8 | lane id bytes
//
// The link list binds the state-string indices to the network's internal
// lane ids (the client places lights from the static GeoJSON — no second
// geometry channel). tick is the publish tick: with the table it pins the
// derivation reference; per-tick freshness rides the TSSF headers.

const (
	sigFrameMagic   = 0x47535354 // "TSSG" in the byte stream
	sigFrameVersion = 1
	sigFrameHeader  = 24
)

// SigPhase is one decoded phase: duration in ticks + per-link state string.
type SigPhase struct {
	DurationTicks uint32
	State         string
}

// SigLink binds a state-string index to an internal lane id.
type SigLink struct {
	LinkIdx int
	LaneID  string
}

// SigProgram is one decoded fixed-time program (the derivation inputs).
type SigProgram struct {
	ID          string
	Junction    string
	OffsetTicks uint64
	Phases      []SigPhase
	Links       []SigLink
}

// SigFrame is a decoded signal-program frame.
type SigFrame struct {
	Tick     uint64
	Programs []SigProgram
}

// SignalFrame encodes the network's fixed-time programs (ADR-0011) as a
// self-sufficient TSSG frame at the engine's current tick. Networks without
// signals encode an empty table (program_count 0) — explicit "no signals",
// distinguishable from "table not yet received".
func SignalFrame(e *engine.Engine) []byte {
	progs := e.Net.Signals
	buf := make([]byte, sigFrameHeader)
	binary.LittleEndian.PutUint32(buf[0:], sigFrameMagic)
	binary.LittleEndian.PutUint16(buf[4:], sigFrameVersion)
	binary.LittleEndian.PutUint16(buf[6:], 0)
	binary.LittleEndian.PutUint64(buf[8:], e.Tick)
	binary.LittleEndian.PutUint32(buf[16:], uint32(len(progs)))
	for _, p := range progs {
		phaseTicks, offsetTicks := p.CompiledTicks()
		buf = appendStr(buf, p.ID)
		buf = appendStr(buf, p.Junction)
		buf = binary.LittleEndian.AppendUint64(buf, offsetTicks)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(phaseTicks)))
		links := signalLinks(e.Net, p)
		buf = binary.LittleEndian.AppendUint16(buf, uint16(len(links)))
		for i, tk := range phaseTicks {
			buf = binary.LittleEndian.AppendUint32(buf, uint32(tk))
			buf = appendStr(buf, p.Phases[i].State)
		}
		for _, l := range links {
			buf = binary.LittleEndian.AppendUint16(buf, uint16(l.LinkIdx))
			buf = appendStr(buf, l.LaneID)
		}
	}
	return buf
}

// signalLinks collects the internal lanes bound to program p, ordered by
// link index (Network.Lanes order is fixed at compile; the sort key is the
// link index itself). Exported-shape mirror of the kernel's lane bindings.
func signalLinks(net *engine.Network, p *engine.SignalProgram) []SigLink {
	var links []SigLink
	for _, l := range net.Lanes {
		if l.Signal == p {
			links = append(links, SigLink{LinkIdx: l.LinkIdx, LaneID: l.ID})
		}
	}
	// Insertion sort by link index (≤ a handful of links per junction).
	for i := 1; i < len(links); i++ {
		for j := i; j > 0 && links[j].LinkIdx < links[j-1].LinkIdx; j-- {
			links[j], links[j-1] = links[j-1], links[j]
		}
	}
	return links
}

// appendStr appends a u8-length-prefixed string. Network-file identifiers
// (program/junction/lane ids — SUMO node ids) are far below the 255 B cap;
// the decoder fails loud (short read / trailing bytes) on a violated one.
func appendStr(buf []byte, s string) []byte {
	buf = append(buf, uint8(len(s)))
	return append(buf, s...)
}

// ParseSignalFrame decodes a TSSG frame (client side; also the test oracle).
// Strict: unknown trailing bytes, truncation, and zero-duration phases
// (unrepresentable on the tick grid, ADR-0011 §1) are rejected.
func ParseSignalFrame(buf []byte) (SigFrame, error) {
	if len(buf) < sigFrameHeader {
		return SigFrame{}, fmt.Errorf("sigframe: %d bytes, want at least %d", len(buf), sigFrameHeader)
	}
	if magic := binary.LittleEndian.Uint32(buf); magic != sigFrameMagic {
		return SigFrame{}, fmt.Errorf("sigframe: bad magic %#08x", magic)
	}
	if v := binary.LittleEndian.Uint16(buf[4:]); v != sigFrameVersion {
		return SigFrame{}, fmt.Errorf("sigframe: unsupported schema_version %d", v)
	}
	f := SigFrame{Tick: binary.LittleEndian.Uint64(buf[8:])}
	n := int(binary.LittleEndian.Uint32(buf[16:]))
	r := &sigReader{buf: buf[sigFrameHeader:]}
	for i := 0; i < n; i++ {
		var p SigProgram
		p.ID = r.str()
		p.Junction = r.str()
		p.OffsetTicks = r.u64()
		nPhases := int(r.u16())
		nLinks := int(r.u16())
		for j := 0; j < nPhases; j++ {
			ph := SigPhase{DurationTicks: r.u32()}
			ph.State = r.str()
			if r.err == nil && ph.DurationTicks == 0 {
				r.err = fmt.Errorf("sigframe: program %q phase %d: zero duration", p.ID, j)
			}
			p.Phases = append(p.Phases, ph)
		}
		for j := 0; j < nLinks; j++ {
			l := SigLink{LinkIdx: int(r.u16())}
			l.LaneID = r.str()
			p.Links = append(p.Links, l)
		}
		if r.err != nil {
			return SigFrame{}, fmt.Errorf("sigframe: program %d: %w", i, r.err)
		}
		f.Programs = append(f.Programs, p)
	}
	if r.err != nil {
		return SigFrame{}, fmt.Errorf("sigframe: %w", r.err)
	}
	if len(r.buf) != 0 {
		return SigFrame{}, fmt.Errorf("sigframe: %d trailing bytes", len(r.buf))
	}
	return f, nil
}

// PhaseAt mirrors engine.SignalProgram.phaseAt exactly (integer math over
// the decoded table): the phase index in force at tick, with SUMO offset
// semantics (phase 0 begins at offsetTicks; before that the cycle wraps).
func (p SigProgram) PhaseAt(tick uint64) int {
	var cycle uint64
	for _, ph := range p.Phases {
		cycle += uint64(ph.DurationTicks)
	}
	if cycle == 0 {
		return 0
	}
	x := (tick%cycle + cycle - p.OffsetTicks%cycle) % cycle
	for i, ph := range p.Phases {
		tk := uint64(ph.DurationTicks)
		if x < tk {
			return i
		}
		x -= tk
	}
	return len(p.Phases) - 1 // unreachable (x < cycle), kept total
}

// StateAt returns the tlLogic state char governing link at tick, or 0 when
// the link has no state char (uncontrolled — the kernel's SigOff fallback).
func (p SigProgram) StateAt(tick uint64, link int) byte {
	if len(p.Phases) == 0 {
		return 0
	}
	st := p.Phases[p.PhaseAt(tick)].State
	if link < 0 || link >= len(st) {
		return 0
	}
	return st[link]
}

// sigReader is the decode cursor; the first short read sticks in err.
type sigReader struct {
	buf []byte
	err error
}

func (r *sigReader) take(n int) []byte {
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

func (r *sigReader) u16() uint16 {
	b := r.take(2)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint16(b)
}

func (r *sigReader) u32() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

func (r *sigReader) u64() uint64 {
	b := r.take(8)
	if b == nil {
		return 0
	}
	return binary.LittleEndian.Uint64(b)
}

func (r *sigReader) str() string {
	b := r.take(1)
	if b == nil {
		return ""
	}
	n := int(b[0])
	s := r.take(n)
	if s == nil {
		return ""
	}
	return string(s)
}
