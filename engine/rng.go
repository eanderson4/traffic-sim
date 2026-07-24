package engine

import (
	"encoding/binary"
	"hash/fnv"
	"math/rand/v2"
)

// Stream is a counted PCG (math/rand/v2) random stream. Per ADR-0005's
// stream-per-concern discipline and ADR-0007, every stochastic draw in the
// kernel goes through a Stream owned by exactly one vehicle — never a shared
// global source. The draw count is folded into the state CRC so replay also
// verifies RNG-consumption parity.
type Stream struct {
	r *rand.Rand
	p *rand.PCG // same generator as r, kept for exact state capture
	n uint64
}

// deriveStream hashes (scenarioSeed, vehicleID) into the two PCG seed words
// via FNV-1a. Deterministic across runs, independent across vehicles.
func deriveStream(seed, id uint64) *Stream {
	return deriveStreamDomain("traffic-sim/engine/vehicle-stream", seed, id)
}

// spawnAttrStream derives the SIDE stream a pending spawner vehicle's type
// and desired-speed factor are drawn from — separate from the vehicle's
// main stream so the draws are idempotent: a pend held across injection
// retries re-derives identical values, and a keyframe restore (which
// persists a pend as ID + main stream only, keyframe.go) replays the
// identical draw with no state to persist. Distinct domain label keeps it
// independent of the main stream's schedule/policy draws.
func spawnAttrStream(seed, id uint64) *Stream {
	return deriveStreamDomain("traffic-sim/engine/spawn-attr-stream", seed, id)
}

func deriveStreamDomain(domain string, seed, id uint64) *Stream {
	var b [8]byte
	h := fnv.New64a()
	h.Write([]byte(domain))
	binary.LittleEndian.PutUint64(b[:], seed)
	h.Write(b[:])
	binary.LittleEndian.PutUint64(b[:], id)
	h.Write(b[:])
	s1 := h.Sum64()
	h.Reset()
	h.Write([]byte(domain + "/2"))
	binary.LittleEndian.PutUint64(b[:], s1)
	h.Write(b[:])
	p := rand.NewPCG(s1, h.Sum64())
	return &Stream{r: rand.New(p), p: p}
}

// DeriveStream is the exported form of deriveStream for external controllers
// (ADR-0007): policy RNG is seeded per vehicle, keyed off the vehicle ID —
// never per process — so which controller or replica drives a vehicle is
// behaviorally invisible and live-run determinism survives failover.
func DeriveStream(seed, vehicleID uint64) *Stream { return deriveStream(seed, vehicleID) }

// DeriveStreamDomain derives a stream under an explicit domain label. A
// controller-side draw that is a separate concern from the vehicle's live
// policy stream (DeriveStream) must derive under its own label — the same
// discipline as the kernel's spawn-attr side stream. Reusing the main
// stream's domain for a separate concern replays the main stream's exact
// draw sequence, coupling the two concerns fleet-wide (chicago-metro
// review, 2026-07-24).
func DeriveStreamDomain(domain string, seed, id uint64) *Stream {
	return deriveStreamDomain(domain, seed, id)
}

// Float64 draws a uniform in [0,1).
func (s *Stream) Float64() float64 { s.n++; return s.r.Float64() }

// Norm draws a standard normal (Box-Muller inside math/rand/v2).
func (s *Stream) Norm() float64 { s.n++; return s.r.NormFloat64() }

// Draws reports how many draws this stream has produced (part of the CRC).
func (s *Stream) Draws() uint64 { return s.n }

// marshalStream captures the exact generator state (PCG words via
// math/rand/v2's binary form) plus the API draw count. Call-type-agnostic:
// ziggurat Norm draws consume a variable number of PCG words, so replaying
// n API calls would NOT reproduce the state — the raw words must travel.
func (s *Stream) marshalStream() ([]byte, error) {
	return s.p.MarshalBinary()
}

// unmarshalStream rebuilds a stream from marshalStream's output.
func unmarshalStream(state []byte, draws uint64) (*Stream, error) {
	p := &rand.PCG{}
	if err := p.UnmarshalBinary(state); err != nil {
		return nil, err
	}
	return &Stream{r: rand.New(p), p: p, n: draws}, nil
}
