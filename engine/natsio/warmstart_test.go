package natsio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"traffic-sim/engine"
)

// warmstart_test.go — the RunLive seam of ADR-0029 phase 1: a run that dumps
// its state at a tick, and a later run that starts from that file. The
// property under test is the same one the engine-level test pins, but over
// the whole live plane: warm start must be bit-exact with the run it was cut
// out of, or it silently changes what is being debugged.

func TestRunLiveWarmStartIsBitExact(t *testing.T) {
	const dumpAt, total = 100, 250
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)

	spec, err := engine.DefaultSpec("lanedrop", total, 7)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.bin")

	// Reference run: cold from tick 0, dropping a state file on the way.
	// The dump must NOT end the run — a run that both records and drops a
	// state partway is the useful shape.
	cold, err := RunLive(nc, js, "warm-src", spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1},
		ContractConfig{StateDumpPath: path, StateDumpTick: dumpAt})
	if err != nil {
		t.Fatalf("cold RunLive: %v", err)
	}
	if cold.Engine.Tick != total {
		t.Fatalf("cold run stopped at tick %d, want %d — the dump must not end the run", cold.Engine.Tick, total)
	}
	data, meta, err := engine.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if meta.Tick != dumpAt {
		t.Fatalf("dumped state is at tick %d, want %d", meta.Tick, dumpAt)
	}
	if meta.Run != "warm-src" || meta.Seed != spec.Seed {
		t.Fatalf("sidecar provenance = run %q seed %d, want warm-src / %d", meta.Run, meta.Seed, spec.Seed)
	}
	if meta.Vehicles == 0 {
		t.Fatal("dumped state holds no vehicles — nothing about lane binding is exercised")
	}

	// Warm run: same spec, started from the file. -ticks is absolute, so it
	// simulates dumpAt+1..total.
	warm, err := RunLive(nc, js, "warm-dst", spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1},
		ContractConfig{InitialState: data, InitialStateMeta: meta})
	if err != nil {
		t.Fatalf("warm RunLive: %v", err)
	}
	if warm.Engine.Tick != total {
		t.Fatalf("warm run ended at tick %d, want %d", warm.Engine.Tick, total)
	}
	if got, want := len(warm.Engine.CRCs), total-dumpAt; got != want {
		t.Fatalf("warm run stepped %d ticks, want %d", got, want)
	}
	for i, crc := range warm.Engine.CRCs {
		if crc != cold.Engine.CRCs[dumpAt+i] {
			t.Fatalf("CRC divergence at tick %d: warm %016x vs cold %016x", dumpAt+i+1, crc, cold.Engine.CRCs[dumpAt+i])
		}
	}
	if warm.Engine.CRC() != cold.Engine.CRC() {
		t.Fatalf("final CRC %016x, want %016x", warm.Engine.CRC(), cold.Engine.CRC())
	}
	t.Logf("warm start at tick %d matched the cold chain over %d ticks (%d vehicles, %d bytes)",
		dumpAt, len(warm.Engine.CRCs), meta.Vehicles, len(data))
}

// The seam's own refusals: state without its sidecar, and a dump tick the
// loop will never reach. Both are silent-failure shapes if allowed through.
func TestRunLiveWarmStartRefusals(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)

	spec, err := engine.DefaultSpec("lanedrop", 60, 9)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.bin")
	if _, err := RunLive(nc, js, "warm-ref", spec, RecorderConfig{KeyframeEvery: 50},
		ContractConfig{StateDumpPath: path, StateDumpTick: 30}); err != nil {
		t.Fatal(err)
	}
	data, meta, err := engine.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := RunLive(nc, js, "warm-nometa", spec, RecorderConfig{},
		ContractConfig{InitialState: data}); err == nil {
		t.Fatal("warm start accepted with no sidecar metadata")
	}
	// A dump tick behind the warm-start tick can never fire.
	_, err = RunLive(nc, js, "warm-badtick", spec, RecorderConfig{},
		ContractConfig{InitialState: data, InitialStateMeta: meta, StateDumpPath: path, StateDumpTick: 10})
	if err == nil || !strings.Contains(err.Error(), "never fire") {
		t.Fatalf("dump tick before the warm-start tick was accepted: %v", err)
	}
	// A dump tick past the run's horizon likewise.
	if _, err := RunLive(nc, js, "warm-pasttick", spec, RecorderConfig{},
		ContractConfig{StateDumpPath: path, StateDumpTick: spec.Ticks + 1}); err == nil {
		t.Fatal("dump tick past the run horizon was accepted")
	}
	// A state at or past the horizon leaves nothing to simulate.
	short := spec
	short.Ticks = meta.Tick
	if _, err := RunLive(nc, js, "warm-short", short, RecorderConfig{},
		ContractConfig{InitialState: data, InitialStateMeta: meta}); err == nil {
		t.Fatal("warm start accepted with no ticks left to simulate")
	}
}

// A warm-started run's record has its first keyframe at the warm-start tick,
// not tick 0, and the replay player anchors on a tick-0 keyframe. This pins
// the boundary that ADR-0029 phase 1 does NOT cross: the recording is
// unopenable, which is why serve refuses -state-in together with -store.
func TestWarmStartRecordHasNoTickZeroKeyframe(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)

	spec, err := engine.DefaultSpec("lanedrop", 60, 4)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.bin")
	if _, err := RunLive(nc, js, "kf-src", spec, RecorderConfig{KeyframeEvery: 50},
		ContractConfig{StateDumpPath: path, StateDumpTick: 30}); err != nil {
		t.Fatal(err)
	}
	data, meta, err := engine.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunLive(nc, js, "kf-warm", spec, RecorderConfig{KeyframeEvery: 50},
		ContractConfig{InitialState: data, InitialStateMeta: meta}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPlayer(nc, js, PlayerConfig{Run: "kf-warm"}); err == nil {
		t.Fatal("a warm-started run's recording opened for replay — the tick-0 keyframe floor is gone, and serve's -state-in/-store refusal can be revisited (ADR-0029 phase 1)")
	} else if !strings.Contains(err.Error(), "keyframe") {
		t.Fatalf("unexpected replay failure: %v", err)
	} else {
		t.Logf("as expected, unopenable: %v", err)
	}
}

// A dump whose destination cannot be written must abort the run rather than
// let it finish looking successful with no state file.
func TestRunLiveStateDumpFailureAborts(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)

	spec, err := engine.DefaultSpec("lanedrop", 40, 2)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "nonexistent")
	_, err = RunLive(nc, js, "dump-fail", spec, RecorderConfig{},
		ContractConfig{StateDumpPath: filepath.Join(dir, "state.bin"), StateDumpTick: 10})
	if err == nil {
		t.Fatal("unwritable state dump did not abort the run")
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatal("failed dump left something behind")
	}
	t.Logf("aborted: %v", err)
}
