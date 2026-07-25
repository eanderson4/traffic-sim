package natsio

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// player_test.go — the VCR round trip: a short live run is recorded into a
// durable store by one broker, that broker shuts down (store exclusivity),
// a second broker reopens the store, and the Player re-simulates the record
// while publishing the live plane under {run}-replay — CRC chain verified,
// snapshot decimation at pace, signal table, seek, pause/resume, paused
// republication, and the end-of-recording hold.

// startStoreBroker is NewTestServer with a caller-chosen store dir, so a
// test can reopen the same store with a second broker.
func startStoreBroker(tb testing.TB, dir string) *server.Server {
	tb.Helper()
	ns, err := server.NewServer(&server.Options{DontListen: true, JetStream: true, StoreDir: dir})
	if err != nil {
		tb.Fatalf("nats-server NewServer: %v", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		tb.Fatal("nats-server not ready")
	}
	return ns
}

func dialJetStream(tb testing.TB, ns *server.Server) (*nats.Conn, nats.JetStreamContext) {
	tb.Helper()
	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns))
	if err != nil {
		tb.Fatalf("in-process connect: %v", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		tb.Fatalf("JetStream context: %v", err)
	}
	return nc, js
}

// snapWatcher collects the tick headers of the replay plane's snapshots.
type snapWatcher struct {
	mu    sync.Mutex
	ticks []uint64
}

func (w *snapWatcher) subscribe(t *testing.T, nc *nats.Conn, replayRun string) {
	t.Helper()
	sub, err := nc.Subscribe(SubjectStateSnap(replayRun), func(m *nats.Msg) {
		tick, err := msgTick(m)
		if err != nil {
			t.Errorf("snapshot without tick header: %v", err)
			return
		}
		w.mu.Lock()
		w.ticks = append(w.ticks, tick)
		w.mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
}

func (w *snapWatcher) snaps() []uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uint64(nil), w.ticks...)
}

func (w *snapWatcher) len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.ticks)
}

func (w *snapWatcher) last() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.ticks) == 0 {
		return 0
	}
	return w.ticks[len(w.ticks)-1]
}

func TestPlayerPublishesLivePlane(t *testing.T) {
	storeDir := t.TempDir()
	const run = "vcr1"
	replayRun := run + "-replay"
	spec, err := engine.DefaultSpec("lanedrop", 120, 7)
	if err != nil {
		t.Fatal(err)
	}

	// Phase 1: record a short run into the durable store, then shut the
	// broker down — only one broker may hold the store dir at a time.
	ns1 := startStoreBroker(t, storeDir)
	nc1, js1 := dialJetStream(t, ns1)
	lr, err := RunLive(nc1, js1, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	liveCRC := lr.Engine.CRC()
	nc1.Close()
	ns1.Shutdown()
	ns1.WaitForShutdown()

	// Phase 2: a second broker on the same store; the Player re-simulates
	// and publishes the live plane under vcr1-replay.
	ns2 := startStoreBroker(t, storeDir)
	defer ns2.Shutdown()
	nc2, js2 := dialJetStream(t, ns2)
	defer nc2.Close()

	var snaps snapWatcher
	snaps.subscribe(t, nc2, replayRun)
	var sigs atomic.Uint64
	sigSub, err := nc2.Subscribe(SubjectStateSig(replayRun), func(*nats.Msg) { sigs.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sigSub.Unsubscribe() }()

	p, err := NewPlayer(nc2, js2, PlayerConfig{Run: run, Speed: 8})
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	if p.ReplayRun() != replayRun {
		t.Fatalf("ReplayRun = %q, want %q", p.ReplayRun(), replayRun)
	}
	hs := httptest.NewServer(p.Handler())
	defer hs.Close()

	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (last snap %d, status %+v)", what, snaps.last(), p.Status())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	call := func(method, path, body string) PlayerStatus {
		t.Helper()
		var resp *http.Response
		var err error
		if method == http.MethodGet {
			resp, err = hs.Client().Get(hs.URL + path)
		} else {
			resp, err = hs.Client().Post(hs.URL+path, "application/json", strings.NewReader(body))
		}
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: %s: %s", method, path, resp.Status, data)
		}
		var st PlayerStatus
		if err := json.Unmarshal(data, &st); err != nil {
			t.Fatalf("%s %s: bad status JSON: %v", method, path, err)
		}
		return st
	}

	// (a) Playback at speed 8: let it run past tick 72, then check the
	// decimation cadence k = round(8) = 8 on everything received so far
	// (the tick-0 opening frame is 0 ≡ 0 mod 8 too).
	waitFor("playback past tick 72", func() bool { return p.Status().Tick >= 72 })
	preSeek := snaps.snaps()
	if len(preSeek) < 2 {
		t.Fatalf("only %d snapshots received", len(preSeek))
	}
	for i, tick := range preSeek {
		if tick%8 != 0 {
			t.Fatalf("snapshot %d has tick %d, want a multiple of k=8", i, tick)
		}
		if i > 0 && tick <= preSeek[i-1] {
			t.Fatalf("snapshot ticks not strictly increasing at %d: %v", i, preSeek)
		}
	}
	if got := sigs.Load(); got < 2 {
		t.Fatalf("signal table published %d times, want ≥ 2 (start + keyframe cadence)", got)
	}

	// (e) Pause, serialized through the control channel: once /pause has
	// returned, NO further tick executes. While paused the player
	// republishes the current frame at ~1 Hz for late-attaching browsers —
	// frames keep arriving, but always at the paused tick.
	st := call(http.MethodPost, "/pause", "")
	if !st.Paused {
		t.Fatal("/pause did not report paused")
	}
	pausedTick := st.Tick
	pauseIdx := snaps.len()
	time.Sleep(1200 * time.Millisecond) // spans at least one 1 Hz republication
	if tk := p.Status().Tick; tk != pausedTick {
		t.Fatalf("playhead moved while paused: %d → %d (pause not serialized)", pausedTick, tk)
	}
	repubs := 0
	for _, tick := range snaps.snaps()[pauseIdx:] {
		if tick != pausedTick {
			t.Fatalf("snapshot at tick %d while paused at %d", tick, pausedTick)
		}
		repubs++
	}
	if repubs == 0 {
		t.Fatal("no paused republication within 1.2 s")
	}

	// (d) Seek backwards WHILE PAUSED: lands on the requested tick and
	// publishes a frame there so the user sees where they landed. The ack
	// status is built by the loop at completion — it reports exactly the
	// landing tick.
	st = call(http.MethodPost, "/seek", `{"tick": 40}`)
	if st.Tick != 40 {
		t.Fatalf("/seek reported tick %d, want 40", st.Tick)
	}
	if !st.Paused || st.Done {
		t.Fatalf("/seek while paused: paused=%v done=%v, want paused, not done", st.Paused, st.Done)
	}
	seekIdx := snaps.len()
	waitFor("seek landing frame", func() bool {
		for _, tick := range snaps.snaps()[seekIdx:] {
			if tick == 40 {
				return true
			}
		}
		return false
	})
	time.Sleep(100 * time.Millisecond)
	if tk := p.Status().Tick; tk != 40 {
		t.Fatalf("still paused after seek, but playhead moved to %d", tk)
	}
	for _, tick := range snaps.snaps()[seekIdx:] {
		if tick != 40 {
			t.Fatalf("snapshot at tick %d after seek to 40 (still paused)", tick)
		}
	}

	// Resume from the seek point: publishing continues, monotonically
	// increasing from 40.
	st = call(http.MethodPost, "/resume", "")
	if st.Paused {
		t.Fatal("/resume still reports paused")
	}
	waitFor("playback past tick 64 after seek", func() bool { return p.Status().Tick >= 64 })

	// (b) Live pace change: k recomputes.
	st = call(http.MethodPost, "/speed", `{"speed": 16}`)
	if st.Speed != 16 {
		t.Fatalf("/speed reported speed %g, want 16", st.Speed)
	}
	if resp, err := hs.Client().Post(hs.URL+"/speed", "application/json", strings.NewReader(`{"speed": 0}`)); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("/speed with speed 0: %s, want 400", resp.Status)
		}
	}

	// (a cont.) End of recording: the player holds — paused at the final
	// tick, one exact final frame, done=true, CRC chain clean.
	waitFor("end-of-recording hold", func() bool {
		st := p.Status()
		return st.Tick == spec.Ticks && st.Paused && st.Done
	})
	waitFor("final frame", func() bool { return snaps.last() == spec.Ticks })

	st = call(http.MethodGet, "/status", "")
	if st.Run != run || st.ReplayRun != replayRun {
		t.Fatalf("status run ids = %q/%q, want %q/%q", st.Run, st.ReplayRun, run, replayRun)
	}
	if st.Ticks != spec.Ticks || st.Tick != spec.Ticks || st.EndTick != spec.Ticks {
		t.Fatalf("status tick/ticks/endTick = %d/%d/%d, want %d across the board", st.Tick, st.Ticks, st.EndTick, spec.Ticks)
	}
	if !st.Done || !st.Paused {
		t.Fatalf("status done/paused = %v/%v, want true/true (end hold)", st.Done, st.Paused)
	}
	if st.CRCErrors != 0 || st.VerbErrors != 0 {
		t.Fatalf("crcErrors/verbErrors = %d/%d, want 0/0", st.CRCErrors, st.VerbErrors)
	}

	// Resume while done is refused; seek back first, then resume plays on.
	resp, err := hs.Client().Post(hs.URL+"/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("/resume while done: %s, want 409", resp.Status)
	}
	st = call(http.MethodPost, "/seek", `{"tick": 48}`)
	if st.Tick != 48 || st.Done {
		t.Fatalf("/seek 48 from the end hold: tick=%d done=%v, want 48/false", st.Tick, st.Done)
	}
	st = call(http.MethodPost, "/resume", "")
	if st.Paused {
		t.Fatal("/resume after seek-back still reports paused")
	}
	waitFor("second end-of-recording hold", func() bool { return p.Status().Done })

	p.Stop()
	<-p.Done()
	if err := <-runErr; err != nil {
		t.Fatalf("Player.Run: %v", err)
	}
	if p.e.CRC() != liveCRC {
		t.Fatalf("replay final crc %016x != live run's %016x", p.e.CRC(), liveCRC)
	}

	// Full tick stream shape: never decreasing except at the two seek
	// landings (40 and 48); equal ticks are the paused republications.
	all := snaps.snaps()
	var drops []uint64
	for i := 1; i < len(all); i++ {
		if all[i] < all[i-1] {
			drops = append(drops, all[i])
		}
	}
	if len(drops) != 2 || drops[0] != 40 || drops[1] != 48 {
		t.Fatalf("non-monotonic drops in the snapshot stream = %v, want [40 48] (the two seeks)", drops)
	}
	t.Logf("replay: %d snapshots, %d signal frames, final crc %016x verified against the live run",
		len(all), sigs.Load(), liveCRC)
}

// TestPlayerUnknownRun: the player fails loud when the store has no such
// run, on ids that are not a single token, and on ids in the reserved
// -replay namespace.
func TestPlayerUnknownRun(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	for _, bad := range []string{"nope", "bad.run", "foo-replay"} {
		if _, err := NewPlayer(nc, js, PlayerConfig{Run: bad}); err == nil {
			t.Fatalf("NewPlayer accepted run id %q", bad)
		}
	}
	if _, err := NewPlayer(nc, js, PlayerConfig{Run: "x", Speed: -2}); err == nil {
		t.Fatal("NewPlayer accepted a negative speed")
	}
}

// TestRunLiveRejectsReplaySuffix: the -replay namespace reservation is
// enforced at RECORD time too — a run recorded as "foo-replay" would
// collide with foo's replay plane.
func TestRunLiveRejectsReplaySuffix(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunLive(nc, js, "foo-replay", spec, RecorderConfig{}); err == nil {
		t.Fatal("RunLive accepted a run id in the reserved -replay namespace")
	}
	if _, err := NewRecorder(js, "foo-replay", RecorderConfig{}); err == nil {
		t.Fatal("NewRecorder accepted a run id in the reserved -replay namespace")
	}
	// The replay-plane id itself remains a valid bus id (the player
	// publishes under it).
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPublishBus(nc, "foo-replay", e); err != nil {
		t.Fatalf("NewPublishBus rejected a replay-plane id: %v", err)
	}
}

// TestPlayerDuplicateKeyframeRecordFails: a dirty store with two keyframes
// at the same tick (e.g. a re-recorded run id) is an ambiguous log.
// NewPlayer must refuse it loud.
func TestPlayerDuplicateKeyframeRecordFails(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	const run = "dirty"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	// Forge a duplicate tick-0 keyframe into the log stream — the
	// dirty-store shape. (Re-recording via RunLive can't produce it: the
	// recorder's OCC header aborts the second run, so this test poisons
	// the stream directly, like the competing-writer test does.)
	m := nats.NewMsg(SubjectLogKeyframe(run))
	m.Header.Set(headerTick, "0")
	m.Data = []byte("forged duplicate keyframe")
	if _, err := js.PublishMsg(m); err != nil {
		t.Fatalf("forge keyframe: %v", err)
	}
	if _, err := NewPlayer(nc, js, PlayerConfig{Run: run}); err == nil {
		t.Fatal("NewPlayer accepted a record with duplicate tick-0 keyframes")
	} else if !strings.Contains(err.Error(), "duplicate keyframes") {
		t.Fatalf("NewPlayer error = %v, want a duplicate-keyframe corruption report", err)
	}
}

// TestPlayerSingleKeyframeRecord: a record holding only the tick-0
// keyframe (run shorter than the keyframe cadence) plays to completion —
// the tick-0 keyframe is the seek floor and nothing needs a derived
// cadence.
func TestPlayerSingleKeyframeRecord(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 50, 3)
	if err != nil {
		t.Fatal(err)
	}
	const run = "vcr3"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 100, CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	p, err := NewPlayer(nc, js, PlayerConfig{Run: run, Speed: 100})
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()
	deadline := time.Now().Add(20 * time.Second)
	for !p.Status().Done {
		if time.Now().After(deadline) {
			t.Fatalf("playback never reached the end (tick %d)", p.Status().Tick)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := p.Status(); st.CRCErrors != 0 || st.VerbErrors != 0 {
		t.Fatalf("crcErrors/verbErrors = %d/%d, want 0/0", st.CRCErrors, st.VerbErrors)
	}
}

// TestPlayerSeekClamps: seek targets beyond the record clamp to the end of
// the recording rather than fabricating state, and a /seek ack while
// UNPAUSED reports exactly the landing tick (the loop builds the status).
func TestPlayerSeekClamps(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 60, 5)
	if err != nil {
		t.Fatal(err)
	}
	const run = "vcr2"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}

	p, err := NewPlayer(nc, js, PlayerConfig{Run: run, Speed: 100})
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	hs := httptest.NewServer(p.Handler())
	defer hs.Close()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()

	// Seek while UNPAUSED (the playhead is moving): the response status
	// must be exactly the landing tick, not some later tick the loop
	// reached before the handler read its state.
	deadline := time.Now().Add(20 * time.Second)
	for p.Status().Tick < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("playback never reached tick 20 (tick %d)", p.Status().Tick)
		}
		time.Sleep(2 * time.Millisecond)
	}
	seekTo := func(tick uint64, want uint64) PlayerStatus {
		t.Helper()
		resp, err := hs.Client().Post(hs.URL+"/seek", "application/json",
			strings.NewReader(fmt.Sprintf(`{"tick": %d}`, tick)))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(resp.Body)
			t.Fatalf("/seek %d: %s: %s", tick, resp.Status, data)
		}
		var st PlayerStatus
		if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
			t.Fatal(err)
		}
		if st.Tick != want {
			t.Fatalf("/seek %d status tick = %d, want exactly %d (clamped)", tick, st.Tick, want)
		}
		return st
	}
	st := seekTo(24, 24)
	if st.Paused {
		t.Fatal("seek while unpaused must not pause the player")
	}

	deadline = time.Now().Add(20 * time.Second)
	for p.Status().Tick < spec.Ticks {
		if time.Now().After(deadline) {
			t.Fatalf("playback never reached the end (tick %d)", p.Status().Tick)
		}
		time.Sleep(5 * time.Millisecond)
	}
	seekTo(40, 40)
	// Seeking onto the end of the record re-engages the hold semantics in
	// the ack itself: done and paused, and /resume is refused with 409.
	st = seekTo(9999, spec.Ticks)
	if !st.Done || !st.Paused {
		t.Fatalf("/seek onto endTick: done/paused = %v/%v, want true/true", st.Done, st.Paused)
	}
	resp, err := hs.Client().Post(hs.URL+"/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("/resume after seek onto endTick: %s, want 409", resp.Status)
	}
	if st := p.Status(); st.CRCErrors != 0 || st.VerbErrors != 0 {
		t.Fatalf("crcErrors/verbErrors = %d/%d after seeks, want 0/0", st.CRCErrors, st.VerbErrors)
	}
}

// TestPlayerSlowSpeedControlsResponsive: at speed 0.01 the tick floor is
// 10 s of wall time — /pause, /resume, and Stop must still answer promptly
// (the pacing sleep is chunked at ≤50 ms), not stall behind the floor.
func TestPlayerSlowSpeedControlsResponsive(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 50, 6)
	if err != nil {
		t.Fatal(err)
	}
	const run = "vcr6"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}

	p, err := NewPlayer(nc, js, PlayerConfig{Run: run, Speed: 0.01})
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	hs := httptest.NewServer(p.Handler())
	defer hs.Close()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()

	// Let the player get into its first 10 s tick floor, then pause: the
	// chunked sleep must service the control channel within ~one wake.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	resp, err := hs.Client().Post(hs.URL+"/pause", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/pause: %s", resp.Status)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("/pause took %v behind a 10 s tick floor (chunked sleep not servicing controls)", d)
	}

	// The pause was serviced mid-floor; the loop must NOT keep draining
	// the original 10 s deadline — a late subscriber gets the paused frame
	// from the ~1 Hz republication promptly.
	var got atomic.Uint64
	sub, err := nc.Subscribe(SubjectStateSnap(run+"-replay"), func(*nats.Msg) { got.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	deadline := time.Now().Add(3 * time.Second)
	for got.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("paused republication stalled behind the drained pace floor")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Resume into the next 10 s floor, then speed up MID-floor: the floor
	// is recomputed from the current speed every ≤50 ms chunk, so the
	// speed-up takes effect at once — a fixed deadline would look frozen
	// for the rest of the old 10 s floor.
	if _, err := hs.Client().Post(hs.URL+"/resume", "application/json", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // back inside a 10 s floor
	resp, err = hs.Client().Post(hs.URL+"/speed", "application/json", strings.NewReader(`{"speed": 100}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/speed: %s", resp.Status)
	}
	deadline = time.Now().Add(2 * time.Second)
	for !p.Status().Done {
		if time.Now().After(deadline) {
			t.Fatalf("/speed mid-floor did not take effect promptly (tick %d after 2 s)", p.Status().Tick)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Stop from the end hold: prompt.
	start = time.Now()
	p.Stop()
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete within 1 s")
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Player.Run: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Stop took %v", d)
	}
}

// TestPlayerSeekDoesNotRecountDivergence: the /status counters mean
// "divergences during forward playback". Seek re-sim still verifies every
// logged CRC (and logs loudly), but scrubbing across a divergent span must
// not move the counters.
func TestPlayerSeekDoesNotRecountDivergence(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 60, 11)
	if err != nil {
		t.Fatal(err)
	}
	const run = "vcr8"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 10, CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	// Poison the record: replay with the OTHER uncontrolled policy. The
	// run was driverless — every vehicle is uncontrolled — so dynamics
	// diverge from the logged CRCs from the first occupied tick on.
	reg, err := NewRegistry(js)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := reg.Meta(run)
	if err != nil {
		t.Fatal(err)
	}
	meta.Spec.Scen.UncontrolledPolicy = engine.PolicyIDM
	if err := reg.putMeta(meta); err != nil {
		t.Fatal(err)
	}

	p, err := NewPlayer(nc, js, PlayerConfig{Run: run, Speed: 1000})
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	hs := httptest.NewServer(p.Handler())
	defer hs.Close()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()

	deadline := time.Now().Add(20 * time.Second)
	for !p.Status().Done {
		if time.Now().After(deadline) {
			t.Fatalf("playback never reached the end (tick %d)", p.Status().Tick)
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := p.Status()
	if before.CRCErrors == 0 {
		t.Fatal("poisoned record did not diverge during forward playback — the test proves nothing")
	}

	// Scrub across the divergent span repeatedly: verification runs (and
	// logs), the counters do not move.
	for _, target := range []uint64{25, 5, 45, 12} {
		resp, err := hs.Client().Post(hs.URL+"/seek", "application/json",
			strings.NewReader(fmt.Sprintf(`{"tick": %d}`, target)))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/seek %d: %s", target, resp.Status)
		}
	}
	after := p.Status()
	if after.CRCErrors != before.CRCErrors || after.VerbErrors != before.VerbErrors {
		t.Fatalf("seek re-sim moved the counters: crc %d → %d, verb %d → %d",
			before.CRCErrors, after.CRCErrors, before.VerbErrors, after.VerbErrors)
	}
}

// TestPlayerStatusEndTick: status exposes both the spec horizon (ticks)
// and the actual end of the record (endTick). A KILLED serve (registry
// status never reached done) caps endTick at the last logged tick — the
// truncated-recording signal. A run whose meta says done RAN to spec.Ticks,
// so endTick is the spec horizon even when the log is sparser than that,
// and playback continues past the last logged tick (CRCs verified where
// present).
func TestPlayerStatusEndTick(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 20, 2)
	if err != nil {
		t.Fatal(err)
	}

	// Case A: killed serve — status running, horizon edited past the log.
	const runA = "vcr5a"
	if _, err := RunLive(nc, js, runA, spec, RecorderConfig{CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive A: %v", err)
	}
	reg, err := NewRegistry(js)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := reg.Meta(runA)
	if err != nil {
		t.Fatal(err)
	}
	meta.Spec.Ticks = 9999
	meta.Status = StatusRunning // the kill-before-finish shape
	if err := reg.putMeta(meta); err != nil {
		t.Fatal(err)
	}
	p, err := NewPlayer(nc, js, PlayerConfig{Run: runA})
	if err != nil {
		t.Fatalf("NewPlayer A: %v", err)
	}
	if st := p.Status(); st.Ticks != 9999 || st.EndTick != 20 {
		t.Fatalf("case A (killed): status ticks/endTick = %d/%d, want 9999/20", st.Ticks, st.EndTick)
	}

	// Case B: status done — the horizon is spec.Ticks even where the log
	// ends earlier, and playback runs PAST the last logged tick with the
	// CRCs that exist still verified.
	const runB = "vcr5b"
	if _, err := RunLive(nc, js, runB, spec, RecorderConfig{CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive B: %v", err)
	}
	meta, err = reg.Meta(runB)
	if err != nil {
		t.Fatal(err)
	}
	meta.Spec.Ticks = 40 // status stays done: the run RAN to its horizon
	if err := reg.putMeta(meta); err != nil {
		t.Fatal(err)
	}
	p, err = NewPlayer(nc, js, PlayerConfig{Run: runB, Speed: 100})
	if err != nil {
		t.Fatalf("NewPlayer B: %v", err)
	}
	if st := p.Status(); st.Ticks != 40 || st.EndTick != 40 {
		t.Fatalf("case B (done): status ticks/endTick = %d/%d, want 40/40", st.Ticks, st.EndTick)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()
	deadline := time.Now().Add(20 * time.Second)
	for !p.Status().Done {
		if time.Now().After(deadline) {
			t.Fatalf("case B: playback never reached the end (tick %d)", p.Status().Tick)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := p.Status(); st.Tick != 40 {
		t.Fatalf("case B: playback held at tick %d, want 40 (past the last logged tick 20)", st.Tick)
	}
	if st := p.Status(); st.CRCErrors != 0 || st.VerbErrors != 0 {
		t.Fatalf("case B: crcErrors/verbErrors = %d/%d, want 0/0 (logged CRCs still verify)", st.CRCErrors, st.VerbErrors)
	}
}

// TestDecimationHonorsDt: the publish stride targets ≈10 Hz wall — the
// tick rate is speed/dt per second, so k = max(1, round(speed × 0.1/dt)).
func TestDecimationHonorsDt(t *testing.T) {
	for _, tc := range []struct {
		speed, dt float64
		want      uint64
	}{
		{8, 0.1, 8},
		{1, 0.1, 1},
		{1.4, 0.1, 1},
		{1.5, 0.1, 2},
		{1, 0.2, 1},  // tick rate 5/s — already ≤10 Hz, publish every tick
		{1, 0.05, 2}, // tick rate 20/s → every other tick
		{2, 0.05, 4},
		{0.01, 0.1, 1},
	} {
		if got := decimation(tc.speed, tc.dt); got != tc.want {
			t.Errorf("decimation(%g, %g) = %d, want %d", tc.speed, tc.dt, got, tc.want)
		}
	}
}

// TestPlayerEndHoldEngagesPromptly: stepping onto the final tick must not
// be followed by one more pace-floor sleep before the done hold engages —
// status would read active for a whole floor.
func TestPlayerEndHoldEngagesPromptly(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 5, 4)
	if err != nil {
		t.Fatal(err)
	}
	const run = "vcr7"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	p, err := NewPlayer(nc, js, PlayerConfig{Run: run, Speed: 0.25}) // 400 ms floor
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()

	deadline := time.Now().Add(20 * time.Second)
	for p.Status().Tick < spec.Ticks {
		if time.Now().After(deadline) {
			t.Fatalf("playback never reached the end (tick %d)", p.Status().Tick)
		}
		time.Sleep(2 * time.Millisecond)
	}
	mark := time.Now()
	for !p.Status().Done {
		if time.Since(mark) > 200*time.Millisecond { // half the 400 ms floor
			t.Fatalf("done hold engaged %v after the final tick — the loop slept the pace floor once too often", time.Since(mark))
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestPlayerPausedRepublication: a browser attaching mid-pause gets a
// snapshot within ~1 s — it must not stare at an empty map until the next
// resume. The signal TABLE deliberately does NOT ride the paused
// republish (ADR-0016 §5: a full chunk set every second is a firehose
// aimed at the busy tabs it targets); the paused attach resyncs via the
// request/reply path instead.
func TestPlayerPausedRepublication(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 300, 9)
	if err != nil {
		t.Fatal(err)
	}
	const run = "vcr4"
	if _, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1}); err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	replayRun := run + "-replay"

	p, err := NewPlayer(nc, js, PlayerConfig{Run: run, Speed: 4})
	if err != nil {
		t.Fatalf("NewPlayer: %v", err)
	}
	hs := httptest.NewServer(p.Handler())
	defer hs.Close()
	runErr := make(chan error, 1)
	go func() { runErr <- p.Run() }()
	defer func() { p.Stop(); <-p.Done() }()

	// Pause almost immediately, THEN attach a fresh subscriber (the
	// late-joining browser).
	resp, err := hs.Client().Post(hs.URL+"/pause", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var st PlayerStatus
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := json.Unmarshal(data, &st); err != nil || !st.Paused {
		t.Fatalf("/pause: %s (err %v)", data, err)
	}

	var gotSnap, gotSig atomic.Uint64
	var snapTick atomic.Uint64
	sub, err := nc.Subscribe(SubjectStateSnap(replayRun), func(m *nats.Msg) {
		if tick, err := msgTick(m); err == nil {
			snapTick.Store(tick)
			gotSnap.Add(1)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	sigSub, err := nc.Subscribe(SubjectStateSig(replayRun), func(*nats.Msg) { gotSig.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sigSub.Unsubscribe() }()

	deadline := time.Now().Add(3 * time.Second)
	for gotSnap.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("late subscriber got %d snapshots within 3 s of pause, want ≥ 1", gotSnap.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapTick.Load() != p.Status().Tick {
		t.Fatalf("republished snapshot tick %d != paused tick %d", snapTick.Load(), p.Status().Tick)
	}
	// Let the pause settle past a republish interval: no broadcast table
	// may arrive while paused (the opening publish predates the pause).
	time.Sleep(1500 * time.Millisecond)
	if gotSig.Load() != 0 {
		t.Fatalf("paused replay broadcast %d signal frames, want 0 (attach resync is request/reply now)", gotSig.Load())
	}

	// The pull path: one request gets the table even while paused.
	inbox := nats.NewInbox()
	replies, err := nc.SubscribeSync(inbox)
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.PublishRequest(SubjectStateSigReq(replayRun), inbox, nil); err != nil {
		t.Fatal(err)
	}
	msg, err := replies.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("paused request/reply: no table: %v", err)
	}
	if _, err := ParseSignalFrame(msg.Data); err != nil {
		t.Fatalf("paused request/reply table: %v", err)
	}
}
