package engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
)

// warmstart.go — saving a live state to a file and starting a later run from
// it (ADR-0029 phase 1, same network only).
//
// The serialization is already done: MarshalState/RestoreState (keyframe.go)
// carry the whole dynamic state and resume bit-exactly, which is what the
// record plane and replay ride on. What this file adds is the FILE form —
// the bytes plus a sidecar that binds them to the network they were taken
// against.
//
// The sidecar is not bookkeeping, it is the safety interlock. A keyframe
// stores each vehicle's LANE INDEX and the only load-time check is a range
// check (keyframe.go: `int(laneIdx) >= len(e.Net.Lanes)`). On the same
// network that is correct and free. Against a network whose lane array has
// shifted — one widened corridor, one added turn bay — every vehicle past
// the shift lands on a different, VALID, wrong lane. Nothing errors, the run
// proceeds, and the output looks exactly like output. Phase 1 does not make
// keyframes portable across networks (that is the lane-id format, ADR-0029
// decision 1 — the NEXT version after v5, which is the stuck timer); it
// makes the unportable case fail loudly,
// which is the part that cannot wait.

// StateMetaFormat tags the sidecar. Bump it if the sidecar's meaning
// changes; a reader that does not recognize the tag refuses rather than
// interpreting unknown fields as a passed check.
//
// v2 folded lane LENGTH into LaneFingerprint. A v1 fingerprint is a
// different function of the network, so comparing one against a v2 hash
// would be comparing two different questions — the tag makes that a loud
// refusal instead. v3 added TypeFingerprint and StateDigest, both REQUIRED,
// so a v2 sidecar cannot present two missing guards as two passed ones.
// Free to do now, since no state file predates this.
const StateMetaFormat = "traffic-sim/state-meta v3"

// stateMetaSuffix is appended to the state path to name its sidecar.
const stateMetaSuffix = ".meta.json"

// StateMetaPath names the sidecar that belongs to a state file.
func StateMetaPath(statePath string) string { return statePath + stateMetaSuffix }

// StateMeta is the sidecar written beside a saved state: the network
// fingerprint that guards the load, plus enough provenance for a human to
// tell two state files apart.
type StateMeta struct {
	Format string `json:"format"`
	// Tick the state was taken at; cross-checked against the state bytes'
	// own header on load, so a sidecar paired with the wrong state file is
	// caught rather than trusted.
	Tick uint64 `json:"tick"`
	Seed uint64 `json:"seed"`
	Run  string `json:"run,omitempty"`
	// ScenarioHash is the ADR-0012 §6 content hash when the run came from a
	// scenario directory; empty for flag-built runs.
	ScenarioHash string `json:"scenario_hash,omitempty"`
	NetworkPath  string `json:"network_path,omitempty"`
	// LaneCount and LaneFingerprint are the guard. Fingerprint is
	// %016x of LaneFingerprint over the loading network's lanes.
	LaneCount       int    `json:"lane_count"`
	LaneFingerprint string `json:"lane_fingerprint"`
	// TypeFingerprint guards the OTHER index-based table a keyframe binds
	// to. Vehicles and queued directives store a TYPE ORDINAL
	// (keyframe.go), resolved against the loading scenario's type list, so
	// a reordered or re-edited list silently turns every restored vehicle
	// into a different type — same silent-plausible-wrong-run failure as a
	// shifted lane array, and the lane fingerprint says nothing about it.
	TypeFingerprint string `json:"type_fingerprint"`
	// StateDigest is FNV-1a 64 over the state bytes, %016x. Tick and length
	// alone do not prove a sidecar and a state file belong together — two
	// states of the same tick and size pass — and the whole guard rides on
	// that pairing being true.
	StateDigest string `json:"state_digest"`
	// Vehicles and StateBytes are human provenance — the first question
	// about a state file is always "is it actually loaded?".
	Vehicles   int `json:"vehicles"`
	StateBytes int `json:"state_bytes"`
}

// LaneFingerprint hashes what a saved state's vehicle records actually mean:
// FNV-1a 64 over the lane count and then, in LANE INDEX ORDER, every lane's
// id (length-prefixed) and its LENGTH. Index order is not an implementation
// convenience here — it is exactly the binding a keyframe relies on, so this
// hash changes precisely when a saved state stops meaning what it said.
//
// Length is in the hash because a keyframe stores each vehicle as (lane
// index, S) and S is a distance ALONG that lane. Ids and order alone leave a
// hole: a network with the same lanes in the same order but different
// geometry — a corridor re-imported at a different simplification tolerance,
// a hand-edited length — passes an id-only guard while every S now means a
// different physical point, and an S past the new end is not even rejected
// (the only load-time check is a range check on the index, keyframe.go).
// That is the same silent-plausible-wrong-run failure this guard exists to
// prevent, so it is guarded the same way.
//
// Successor topology is deliberately NOT hashed. Rewiring a junction changes
// where restored vehicles will GO, not where they ARE, and a state is a
// position snapshot; a run on a rewired network is a legitimate thing to
// want. Vehicle positions are what the interlock protects.
//
// Determinism: nothing here iterates a Go map. Network.byID is a map and
// hashing it would give a different answer per process, which would turn
// the guard into a coin flip — the one failure mode a guard must not have.
// Lengths are hashed as their IEEE-754 bits, so the hash never depends on
// float formatting.
func LaneFingerprint(n *Network) uint64 {
	h := fnv.New64a()
	var b [8]byte
	put := func(x uint64) { binary.LittleEndian.PutUint64(b[:], x); h.Write(b[:]) }
	put(uint64(len(n.Lanes)))
	for _, l := range n.Lanes {
		// Length-prefixed: without it "ab"+"c" and "a"+"bc" would hash
		// alike, and lane ids are exactly the kind of string where that
		// happens (edge id + separator + index).
		put(uint64(len(l.ID)))
		h.Write([]byte(l.ID))
		put(math.Float64bits(l.Length))
	}
	return h.Sum64()
}

// laneFingerprintHex is the sidecar's textual form.
func laneFingerprintHex(n *Network) string { return fmt.Sprintf("%016x", LaneFingerprint(n)) }

// TypeFingerprint hashes the vehicle-type table's IDENTITY the same way and
// for the same reason: a keyframe stores each vehicle's TYPE ORDINAL, so the
// binding is to the list's ORDER, not to any name a restored vehicle carries.
// Names are length-prefixed and hashed in index order.
//
// Only names, not the physical parameters behind them. A type list that has
// been re-tuned — same names, different acceleration — is a legitimate thing
// to warm-start onto, and is the analogue of the successor-rewiring case
// LaneFingerprint deliberately allows: it changes how restored vehicles
// BEHAVE, not which vehicle each record refers to.
func TypeFingerprint(types []*VehicleType) uint64 {
	h := fnv.New64a()
	var b [8]byte
	put := func(x uint64) { binary.LittleEndian.PutUint64(b[:], x); h.Write(b[:]) }
	put(uint64(len(types)))
	for _, t := range types {
		name := ""
		if t != nil {
			name = t.Name
		}
		put(uint64(len(name)))
		h.Write([]byte(name))
	}
	return h.Sum64()
}

func typeFingerprintHex(types []*VehicleType) string {
	return fmt.Sprintf("%016x", TypeFingerprint(types))
}

// stateDigestHex is FNV-1a 64 over the state bytes. Not a security hash —
// nothing here defends against a forged state file. It answers the one
// question tick and length cannot: are these the bytes the sidecar was
// written for?
func stateDigestHex(data []byte) string {
	h := fnv.New64a()
	h.Write(data)
	return fmt.Sprintf("%016x", h.Sum64())
}

// CheckNetwork refuses a state whose network is not the one it was saved
// against. It names WHAT differs, because "wrong network" and "same network,
// reordered" call for different fixes.
func (m *StateMeta) CheckNetwork(n *Network) error {
	if m == nil {
		return fmt.Errorf("warm start: no sidecar metadata — refusing to load a state with no proof of which network it was saved against")
	}
	if m.LaneCount != len(n.Lanes) {
		return fmt.Errorf("warm start: state was saved against a network of %d lanes, this one has %d — "+
			"a saved state binds every vehicle to a LANE INDEX, so loading it here would put vehicles on different, valid, WRONG lanes (ADR-0029). "+
			"Re-take the state under this network", m.LaneCount, len(n.Lanes))
	}
	if got := laneFingerprintHex(n); m.LaneFingerprint != got {
		return fmt.Errorf("warm start: network fingerprint mismatch (state %s, this network %s) — "+
			"same lane count, different lane ids, order or lengths, so vehicle lane indices and positions no longer mean the same places and every vehicle would land on a valid WRONG one (ADR-0029). "+
			"Re-take the state under this network", m.LaneFingerprint, got)
	}
	return nil
}

// CheckTypes refuses a state whose vehicle-type table is not the one it was
// saved against, on exactly the grounds of CheckNetwork: the keyframe stores
// type ORDINALS, so a reordered list silently reinterprets every restored
// vehicle as a different type.
//
// An empty fingerprint is a refusal, not a skip. A sidecar written before
// this field existed cannot prove anything about the type table, and "no
// answer" must not read as "passed" (StateMetaFormat v3 makes that
// unreachable in practice; the check is here so it stays unreachable).
func (m *StateMeta) CheckTypes(types []*VehicleType) error {
	if m == nil {
		return fmt.Errorf("warm start: no sidecar metadata — refusing to load a state with no proof of which vehicle types it was saved against")
	}
	if m.TypeFingerprint == "" {
		return fmt.Errorf("warm start: sidecar carries no vehicle-type fingerprint — an empty guard is not a passed guard (ADR-0029)")
	}
	if got := typeFingerprintHex(types); m.TypeFingerprint != got {
		return fmt.Errorf("warm start: vehicle-type fingerprint mismatch (state %s, this scenario %s) — "+
			"a keyframe stores each vehicle's TYPE ORDINAL, so a reordered or re-edited type list turns every restored vehicle into a different type (ADR-0029). "+
			"Re-take the state under this scenario", m.TypeFingerprint, got)
	}
	return nil
}

// SaveState writes the engine's full state to path and its sidecar to
// path+".meta.json". Both writes are atomic (temp file + rename) and the
// state lands before the sidecar, so an interrupted save leaves either
// nothing or a state with no sidecar — and a state with no sidecar is
// refused on load rather than trusted.
func SaveState(path string, e *Engine, spec RunSpec, run string) error {
	data, err := e.MarshalState()
	if err != nil {
		return fmt.Errorf("warm start: marshal state at tick %d: %w", e.Tick, err)
	}
	meta := StateMeta{
		Format:          StateMetaFormat,
		Tick:            e.Tick,
		Seed:            spec.Seed,
		Run:             run,
		ScenarioHash:    spec.Hash,
		NetworkPath:     spec.Net.Path,
		LaneCount:       len(e.Net.Lanes),
		LaneFingerprint: laneFingerprintHex(e.Net),
		TypeFingerprint: typeFingerprintHex(e.scen.Types),
		StateDigest:     stateDigestHex(data),
		Vehicles:        len(e.order),
		StateBytes:      len(data),
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("warm start: encode sidecar: %w", err)
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("warm start: write state %s: %w", path, err)
	}
	if err := writeFileAtomic(StateMetaPath(path), raw); err != nil {
		return fmt.Errorf("warm start: write sidecar %s: %w", StateMetaPath(path), err)
	}
	return nil
}

// LoadState reads a saved state and its sidecar. It refuses a missing or
// unrecognized sidecar, and refuses a sidecar that does not describe THESE
// bytes (tick and length cross-check) — the network check needs a network
// and lives in StateMeta.CheckNetwork / RestoreStateChecked.
func LoadState(path string) ([]byte, *StateMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("warm start: read state: %w", err)
	}
	metaPath := StateMetaPath(path)
	raw, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("warm start: %s has no sidecar %s — the sidecar carries the network fingerprint that keeps a state "+
				"from being loaded against a network whose lane indices have shifted (every vehicle would land on a valid WRONG lane, ADR-0029). "+
				"Refusing rather than assuming it is the same network", path, metaPath)
		}
		return nil, nil, fmt.Errorf("warm start: read sidecar %s: %w", metaPath, err)
	}
	var m StateMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, fmt.Errorf("warm start: sidecar %s: %w", metaPath, err)
	}
	if m.Format != StateMetaFormat {
		return nil, nil, fmt.Errorf("warm start: sidecar %s has format %q, want %q", metaPath, m.Format, StateMetaFormat)
	}
	if m.LaneFingerprint == "" || m.LaneCount == 0 {
		return nil, nil, fmt.Errorf("warm start: sidecar %s carries no network fingerprint — an empty guard is not a passed guard", metaPath)
	}
	if m.StateDigest == "" {
		return nil, nil, fmt.Errorf("warm start: sidecar %s carries no state digest — an empty guard is not a passed guard", metaPath)
	}
	if got := stateDigestHex(data); m.StateDigest != got {
		return nil, nil, fmt.Errorf("warm start: sidecar %s describes state %s, but %s hashes to %s — the sidecar belongs to a different state file, "+
			"and the network fingerprint it carries is therefore a guard for someone else's bytes",
			metaPath, m.StateDigest, path, got)
	}
	tick, err := peekStateTick(data)
	if err != nil {
		return nil, nil, fmt.Errorf("warm start: state %s: %w", path, err)
	}
	if tick != m.Tick {
		return nil, nil, fmt.Errorf("warm start: sidecar %s says tick %d but %s is at tick %d — the sidecar belongs to a different state file",
			metaPath, m.Tick, path, tick)
	}
	if m.StateBytes != 0 && m.StateBytes != len(data) {
		return nil, nil, fmt.Errorf("warm start: sidecar %s says %d state bytes, %s has %d — the sidecar belongs to a different state file",
			metaPath, m.StateBytes, path, len(data))
	}
	return data, &m, nil
}

// RestoreStateChecked is RestoreState with the network guard: the spec's
// network is built first, checked against the sidecar, and only then are the
// saved bytes applied. A mismatch returns an error and NO engine — the whole
// point is that a wrong-network warm start never becomes a run.
func RestoreStateChecked(spec RunSpec, data []byte, meta *StateMeta) (*Engine, error) {
	if meta == nil {
		return nil, fmt.Errorf("warm start: refusing to load a state with no sidecar metadata (ADR-0029)")
	}
	e, err := restoreState(spec, data, meta.CheckNetwork)
	if err != nil {
		return nil, err
	}
	// After the network guard, because the type table only exists once the
	// scenario is built — and before the caller can run a tick with it.
	if err := meta.CheckTypes(e.scen.Types); err != nil {
		return nil, err
	}
	return e, nil
}

// peekStateTick reads the tick out of a state header without building an
// engine (magic + version validated on the way).
func peekStateTick(data []byte) (uint64, error) {
	r := &byteReader{buf: data}
	if magic := r.u32(); magic != keyframeMagic {
		return 0, fmt.Errorf("not a saved state: bad magic %#08x", magic)
	}
	ver := r.u16()
	if ver < keyframeMinVersion || ver > keyframeVersion {
		return 0, fmt.Errorf("unsupported state version %d", ver)
	}
	r.u16() // flags
	tick := r.u64()
	if r.err != nil {
		return 0, fmt.Errorf("truncated header: %w", r.err)
	}
	return tick, nil
}

// writeFileAtomic writes via a temp file in the same directory and renames,
// so a reader never sees a half-written state and a failed write never
// replaces a good file with a truncated one.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
