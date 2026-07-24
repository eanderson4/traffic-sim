package natsio

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"traffic-sim/engine"
)

// sigframe_test.go — the M9 signal-program frame (ADR-0006, 2026-07-20 M9
// addendum): round-trip against the compiled network, derivation
// equivalence with the kernel over a full cycle sweep, golden bytes (the
// viz decoder test pins the same constant), rejection paths, the empty
// table, old-client tolerance on the live plane (TSSF untouched), and
// late-join catch-up via the keyframe-cadence republication.

// sigFixture is a two-junction network: sigA with two signal-bound internal
// lanes (links 0,1) and phases 1.0 s/0.5 s at offset 0; sigB with one bound
// lane, phases 2.0 s/1.0 s at offset 0.5 s (5 ticks at dt 0.1).
func sigFixture(t *testing.T) engine.RunSpec {
	t.Helper()
	link0, link1 := 0, 1
	nf := engine.NetFile{
		Version: 1,
		Name:    "sigfix",
		Lanes: []engine.NetLane{
			{ID: "nA_0", Section: "A", Length: 200, SpeedLimit: 13.89, Origin: true, Successors: []string{"iJ1_0", "iJ1_1"}},
			{ID: "iJ1_0", Section: "j:J1", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J1", TL: "sigA", TLLink: &link0, Successors: []string{"nX_0"}},
			{ID: "iJ1_1", Section: "j:J1", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J1", TL: "sigA", TLLink: &link1, Successors: []string{"nX_0"}},
			{ID: "nX_0", Section: "X", Length: 200, SpeedLimit: 13.89, Exit: true},
			{ID: "nB_0", Section: "B", Length: 200, SpeedLimit: 13.89, Origin: true, Successors: []string{"iJ2_0"}},
			{ID: "iJ2_0", Section: "j:J2", Length: 10, SpeedLimit: 13.89, Internal: true, Junction: "J2", TL: "sigB", TLLink: &link0, Successors: []string{"nY_0"}},
			{ID: "nY_0", Section: "Y", Length: 200, SpeedLimit: 13.89, Exit: true},
		},
		Signals: []engine.NetSignal{
			{ID: "sigA", Junction: "J1", Phases: []engine.NetSignalPhase{{Duration: 1.0, State: "Gr"}, {Duration: 0.5, State: "yr"}}},
			{ID: "sigB", Junction: "J2", Offset: 0.5, Phases: []engine.NetSignalPhase{{Duration: 2.0, State: "r"}, {Duration: 1.0, State: "G"}}},
		},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "net.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: path},
		Params: engine.DefaultParams(),
		Seed:   1,
		Ticks:  200,
	}
}

func TestSignalFrameRoundTrip(t *testing.T) {
	e, err := engine.NewEngine(sigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseSignalFrame(SignalFrame(e))
	if err != nil {
		t.Fatalf("ParseSignalFrame: %v", err)
	}
	if f.Tick != 0 {
		t.Errorf("tick = %d, want 0", f.Tick)
	}
	if len(f.Programs) != 2 {
		t.Fatalf("programs = %d, want 2 (file order)", len(f.Programs))
	}
	a := f.Programs[0]
	if a.ID != "sigA" || a.Junction != "J1" || a.OffsetTicks != 0 {
		t.Errorf("sigA header = %+v", a)
	}
	if len(a.Phases) != 2 || a.Phases[0].DurationTicks != 10 || a.Phases[0].State != "Gr" ||
		a.Phases[1].DurationTicks != 5 || a.Phases[1].State != "yr" {
		t.Errorf("sigA phases = %+v, want 10 ticks %q / 5 ticks %q", a.Phases, "Gr", "yr")
	}
	if len(a.Links) != 2 || a.Links[0] != (SigLink{LinkIdx: 0, LaneID: "iJ1_0"}) ||
		a.Links[1] != (SigLink{LinkIdx: 1, LaneID: "iJ1_1"}) {
		t.Errorf("sigA links = %+v, want link0=iJ1_0 link1=iJ1_1", a.Links)
	}
	b := f.Programs[1]
	if b.ID != "sigB" || b.Junction != "J2" || b.OffsetTicks != 5 {
		t.Errorf("sigB header = %+v, want offset 5 ticks", b)
	}
	if len(b.Phases) != 2 || b.Phases[0].DurationTicks != 20 || b.Phases[1].DurationTicks != 10 {
		t.Errorf("sigB phases = %+v, want 20/10 ticks", b.Phases)
	}
	if len(b.Links) != 1 || b.Links[0] != (SigLink{LinkIdx: 0, LaneID: "iJ2_0"}) {
		t.Errorf("sigB links = %+v", b.Links)
	}
}

// The decoded table's derivation must equal the kernel's own phase function
// for every tick — the wire ships the inputs, never the states (ADR-0011 §1).
func TestSignalFrameDerivationMatchesKernel(t *testing.T) {
	e, err := engine.NewEngine(sigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseSignalFrame(SignalFrame(e))
	if err != nil {
		t.Fatal(err)
	}
	kernel := map[string]*engine.SignalProgram{}
	for _, p := range e.Net.Signals {
		kernel[p.ID] = p
	}
	for _, dp := range f.Programs {
		kp := kernel[dp.ID]
		if kp == nil {
			t.Fatalf("decoded program %q not in kernel", dp.ID)
		}
		for tick := uint64(0); tick < 240; tick++ { // ≥ 2 full cycles of both
			if got, want := dp.PhaseAt(tick), kp.PhaseAt(tick); got != want {
				t.Fatalf("%s PhaseAt(%d) = %d, kernel %d", dp.ID, tick, got, want)
			}
			// State chars must be the kernel's own phase state string.
			ks := kp.Phases[kp.PhaseAt(tick)].State
			for link := 0; link <= len(ks); link++ { // incl. out-of-range link
				got := dp.StateAt(tick, link)
				want := byte(0)
				if link < len(ks) {
					want = ks[link]
				}
				if got != want {
					t.Fatalf("%s StateAt(%d, %d) = %q, want %q", dp.ID, tick, link, got, want)
				}
			}
		}
	}
}

// Golden bytes at tick 0 — viz/test/tssg.test.ts pins the same constant.
func TestSignalFrameGolden(t *testing.T) {
	e, err := engine.NewEngine(sigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(SignalFrame(e))
	const want = sigGoldenHex
	if got != want {
		t.Errorf("golden mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestParseSignalFrameRejects(t *testing.T) {
	e, err := engine.NewEngine(sigFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	good := SignalFrame(e)
	if _, err := ParseSignalFrame(good[:10]); err == nil {
		t.Error("short frame accepted")
	}
	bad := append([]byte(nil), good...)
	bad[0] ^= 0xff
	if _, err := ParseSignalFrame(bad); err == nil {
		t.Error("bad magic accepted")
	}
	bad = append([]byte(nil), good...)
	bad[4] = 2
	if _, err := ParseSignalFrame(bad); err == nil {
		t.Error("bad version accepted")
	}
	if _, err := ParseSignalFrame(good[:len(good)-3]); err == nil {
		t.Error("truncated frame accepted")
	}
	if _, err := ParseSignalFrame(append(append([]byte(nil), good...), 0)); err == nil {
		t.Error("trailing bytes accepted")
	}
	// Zero-duration phase: unrepresentable on the tick grid (ADR-0011 §1).
	if _, err := ParseSignalFrame(zeroFirstPhaseDuration(t, good)); err == nil {
		t.Error("zero-duration phase accepted")
	}
}

// zeroFirstPhaseDuration returns the frame with program 0 phase 0 duration
// set to 0 (header 24 | id 1+4 | junction 1+2 | offset 8 | counts 4 → 40).
func zeroFirstPhaseDuration(t *testing.T, frame []byte) []byte {
	t.Helper()
	bad := append([]byte(nil), frame...)
	const off = 24 + (1 + 4) + (1 + 2) + 8 + 4
	if len(bad) < off+4 || binary.LittleEndian.Uint32(bad[off:]) == 0 {
		t.Fatalf("fixture offset wrong: duration at %d is %#x", off, bad[off:off+4])
	}
	for i := 0; i < 4; i++ {
		bad[off+i] = 0
	}
	return bad
}

func TestSignalFrameEmptyTable(t *testing.T) {
	spec, err := engine.DefaultSpec("ring", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ParseSignalFrame(SignalFrame(e))
	if err != nil {
		t.Fatalf("ParseSignalFrame: %v", err)
	}
	if len(f.Programs) != 0 {
		t.Errorf("ring: programs = %d, want 0 (explicit empty table)", len(f.Programs))
	}
}

// Live plane, old-client tolerance: while the new sig subject is published,
// a client subscribed only to ts.{run}.state.snap decodes every TSSF v1
// frame without error — the vehicle path is byte-untouched (M9 addendum).
// And the late joiner: core NATS has no retention, so a subscriber
// attaching mid-run misses the tick-0 table; the signalCatchUpEvery
// republication converges it (next table ≤ signalCatchUpEvery ticks
// later), after which the table alone yields every future state
// (derivation).
func TestLiveSignalTablePublishAndLateJoin(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	run := "siglive"
	spec := sigFixture(t)
	spec.Ticks = 160

	// Old client: snapshots only, from before the run.
	oldClient := srv.Connect(t)
	snapSub, err := oldClient.SubscribeSync(SubjectStateSnap(run))
	if err != nil {
		t.Fatal(err)
	}

	type runResult struct {
		lr  *LiveRun
		err error
	}
	done := make(chan runResult, 1)
	go func() {
		lr, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50},
			ContractConfig{PaceFloor: 5 * time.Millisecond})
		done <- runResult{lr, err}
	}()

	// Watcher: wait until the run is genuinely underway (tick ≥ 60).
	watch := srv.Connect(t)
	watchSub, err := watch.SubscribeSync(SubjectStateSnap(run))
	if err != nil {
		t.Fatal(err)
	}
	var sawTick uint64
	for sawTick < 60 {
		msg, err := watchSub.NextMsg(10 * time.Second)
		if err != nil {
			t.Fatalf("watcher: %v", err)
		}
		f, err := ParseFrame(msg.Data) // old decoder on the live stream
		if err != nil {
			t.Fatalf("old-client TSSF decode at tick ?: %v", err)
		}
		sawTick = f.Tick
	}

	// Late joiner: subscribes the sig subject only now (missed the tick-0
	// table and every cadence publish since).
	late := srv.Connect(t)
	sigSub, err := late.SubscribeSync(SubjectStateSig(run))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := sigSub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("late joiner never received a table: %v", err)
	}
	table, err := ParseSignalFrame(msg.Data)
	if err != nil {
		t.Fatalf("late-join table: %v", err)
	}
	// Core NATS has no retention: any received table is a FRESH cadence
	// publish — later than the last tick the watcher observed, and within
	// ~one catch-up cadence of the subscribe instant (3× for connect +
	// scheduling slack; the old keyframe cadence could take 5× that).
	if table.Tick <= sawTick || table.Tick > sawTick+3*signalCatchUpEvery {
		t.Fatalf("late-join table tick = %d, want (%d, %d] (fresh cadence publish after subscribing)",
			table.Tick, sawTick, sawTick+3*signalCatchUpEvery)
	}
	if len(table.Programs) != 2 {
		t.Fatalf("late-join table programs = %d, want 2", len(table.Programs))
	}

	// Convergence: the table alone derives states at ticks well past its
	// publish tick — assert equality with the kernel's own phase function.
	res := <-done
	if res.err != nil {
		t.Fatalf("RunLive: %v", res.err)
	}
	kernel := map[string]*engine.SignalProgram{}
	for _, p := range res.lr.Engine.Net.Signals {
		kernel[p.ID] = p
	}
	for _, dp := range table.Programs {
		for tick := table.Tick; tick < table.Tick+200; tick++ {
			if got, want := dp.PhaseAt(tick), kernel[dp.ID].PhaseAt(tick); got != want {
				t.Fatalf("convergence: %s PhaseAt(%d) = %d, kernel %d", dp.ID, tick, got, want)
			}
		}
	}

	// Drain remaining snapshots through the old decoder: all must parse.
	for {
		msg, err := snapSub.NextMsg(10 * time.Millisecond)
		if err != nil {
			break
		}
		if _, err := ParseFrame(msg.Data); err != nil {
			t.Fatalf("old-client TSSF decode: %v", err)
		}
	}
}

// sigGoldenHex is the TSSG v1 encoding of sigFixture at tick 0. The viz
// decoder test (viz/test/tssg.test.ts) pins the same constant.
const sigGoldenHex = "5453534701000000000000000000000002000000000000000473696741024a310000000000000000020002000a00000002477205000000027972000005694a315f30010005694a315f310473696742024a320500000000000000020001001400000001720a0000000147000005694a325f30"
