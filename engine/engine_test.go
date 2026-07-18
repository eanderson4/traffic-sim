package engine

import "testing"

func runCRCs(t *testing.T, spec RunSpec) []uint64 {
	t.Helper()
	_, log, err := Run(spec)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if uint64(len(log.CRCs)) != spec.Ticks {
		t.Fatalf("log has %d CRCs, want %d", len(log.CRCs), spec.Ticks)
	}
	return log.CRCs
}

func equalCRCs(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func assertEqualCRCs(t *testing.T, a, b []uint64) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("CRC sequence lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("CRC divergence at tick %d: %016x vs %016x", i+1, a[i], b[i])
		}
	}
}

// Two independent runs with the same seed must produce identical per-tick
// CRC sequences — on both networks (ADR-0005 determinism envelope).
func TestDeterminismRing(t *testing.T) {
	spec, err := DefaultSpec("ring", 1000, 42)
	if err != nil {
		t.Fatal(err)
	}
	assertEqualCRCs(t, runCRCs(t, spec), runCRCs(t, spec))
}

func TestDeterminismLaneDrop(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 1000, 42)
	if err != nil {
		t.Fatal(err)
	}
	assertEqualCRCs(t, runCRCs(t, spec), runCRCs(t, spec))
}

// Sanity: a different seed must actually change the trajectory.
func TestSeedMatters(t *testing.T) {
	s1, _ := DefaultSpec("lanedrop", 300, 1)
	s2, _ := DefaultSpec("lanedrop", 300, 2)
	if equalCRCs(runCRCs(t, s1), runCRCs(t, s2)) {
		t.Fatal("different seeds produced identical CRC sequences")
	}
}

// Replay must reproduce the recorded CRC sequence exactly.
func TestReplayRing(t *testing.T) {
	spec, _ := DefaultSpec("ring", 1000, 7)
	_, log, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(log); err != nil {
		t.Fatalf("Replay: %v", err)
	}
}

func TestReplayLaneDrop(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 1000, 7)
	_, log, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	relog, err := Replay(log)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	assertEqualCRCs(t, log.CRCs, relog.CRCs)
}

// A corrupted log must be caught by the CRC check.
func TestReplayDetectsCorruption(t *testing.T) {
	spec, _ := DefaultSpec("lanedrop", 200, 3)
	_, log, err := Run(spec)
	if err != nil {
		t.Fatal(err)
	}
	log.CRCs[100] ^= 1
	if _, err := Replay(log); err == nil {
		t.Fatal("Replay accepted a corrupted CRC sequence")
	}
}
