package natsio

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// player.go — the VCR driver: a paced, PUBLISHING replay of a recorded run.
// Where ReplayFromStream is the read-only audit path (verify, abort on
// divergence, publish nothing), the Player re-simulates the same record for
// a demo audience: it publishes the live plane (snapshots on
// ts.{run}-replay.state.snap, the signal table on ts.{run}-replay.state.sig)
// under a FRESH run id at a configurable pace, with an HTTP control plane
// (pause/resume/speed/seek) for the demo UI.
//
// Deliberate departures from the audit path:
//   - CRC divergence and rejected verbs are logged loudly (stderr, rate-
//     limited) and counted separately in /status, and playback CONTINUES —
//     a demo must not die on air. ReplayFromStream's abort semantics are
//     untouched.
//   - The Player has no Recorder, no Contract, no demand director, and no
//     driver — non-attachment is structural (there are no such fields).
//     The bus is assembled without its intent subscription.
//
// Concurrency: the engine is single-goroutine. The Run goroutine is the
// only one that touches it; HTTP handlers either flip mutex-protected
// control state (speed) or hand a request over the control channel
// (pause/resume/seek) that Run services between ticks, acking with the
// PlayerStatus it builds at completion. Because Run services the channel
// before the step decision of the same iteration, a completed /pause
// guarantees no further tick executes, a /resume can't race the end-of-
// recording hold, and a /seek response reports exactly the landing tick.
//
// End of recording: at spec.Ticks (or log exhaustion) the Player holds
// position — it stays up, paused at the final tick, still serving the
// control plane (a seek moves the playhead back; resume replays from
// there; resume while done is refused with 409). Status exposes both
// "ticks" (the spec's horizon) and "endTick" (the actual end of the
// record); endTick < ticks means the recording is truncated.
//
// While paused (including the end hold) the Player republishes the current
// snapshot and signal table at ~1 Hz so a browser attaching mid-pause
// renders immediately.

// pausedRepublishInterval is the cadence of the paused-state frame
// republication (see the package comment above).
const pausedRepublishInterval = time.Second

// PlayerConfig configures a Player.
type PlayerConfig struct {
	// Run is the recorded run id to replay (required, single token, must
	// not end in "-replay").
	Run string
	// Speed is the initial playback pace multiplier; 0 means 1 (realtime).
	Speed float64
	// Log receives divergence and operational lines (default: stderr).
	Log *log.Logger
}

// PlayerStatus is the GET /status body and the response body of every
// control POST.
type PlayerStatus struct {
	Run       string  `json:"run"`
	ReplayRun string  `json:"replayRun"`
	Tick      uint64  `json:"tick"`
	Ticks     uint64  `json:"ticks"`   // the spec's horizon
	EndTick   uint64  `json:"endTick"` // actual end of the record (< ticks = truncated recording)
	Speed     float64 `json:"speed"`
	Paused    bool    `json:"paused"`
	Done      bool    `json:"done"` // end-of-recording hold
	Dt        float64 `json:"dt"`   // the recorded run's timestep (authoritative — viz ?dt= override)
	// Hash is the recorded run's ADR-0012 scenario content hash (empty for
	// flag-built recordings): demosrv compares it against the scenario it
	// would display to catch scenario-edited-after-recording.
	Hash string `json:"hash,omitempty"`
	// CRCErrors and VerbErrors count divergences during FORWARD PLAYBACK
	// only. Seek re-sim still verifies every logged CRC and re-enqueues
	// every verb (and logs failures loudly), but does not count them —
	// scrubbing across a divergent span must not move the counters.
	CRCErrors  uint64 `json:"crcErrors"`
	VerbErrors uint64 `json:"verbErrors"`
}

// Player is the paced, publishing replay engine driver.
type Player struct {
	js        nats.JetStreamContext
	src       string // recorded run id
	replayRun string // src + "-replay"
	spec      engine.RunSpec
	stream    string
	log       *log.Logger

	cur     *logCursor // forward reader over the log stream (ADR-0024)
	endTick uint64     // min(spec.Ticks, last logged tick)

	bus *Bus
	e   *engine.Engine // owned by the Run goroutine

	mu     sync.Mutex // guards paused, speed
	paused bool
	speed  float64

	curTick  atomic.Uint64 // mirror of e.Tick for the control plane
	crcErrs  atomic.Uint64
	verbErrs atomic.Uint64
	done     atomic.Bool // holding at endTick
	stopping atomic.Bool

	ctrlCh  chan ctrlRequest
	runDone chan struct{} // closed when Run returns

	lastRepublish time.Time // ~1 Hz paused republication; Run-goroutine only
}

// ctrlKind identifies a control-channel request.
type ctrlKind int

const (
	ctrlPause ctrlKind = iota
	ctrlResume
	ctrlSeek
)

// errResumeDone refuses a resume at the end of the record (HTTP 409).
var errResumeDone = errors.New("replay is done (end-of-recording hold) — seek back before resuming")

// ctrlRequest is a serialized control-plane request handed to the Run
// goroutine; ack carries the outcome (and the status the loop builds at
// completion) back to the HTTP handler.
type ctrlRequest struct {
	kind ctrlKind
	tick uint64 // ctrlSeek only
	ack  chan ctrlAck
}

type ctrlAck struct {
	status PlayerStatus
	err    error
}

// NewPlayer prepares a replay of the recorded run cfg.Run: it reads the run
// registry meta (fail loud if the run is unknown to this store), restores
// the tick-0 keyframe, and opens a forward cursor over the log stream.
//
// The cursor replaced a full materialization of the record (ADR-0024). The
// recording is immutable once the serving broker has exited and playback is
// monotonic in tick, so per-tick re-enqueue only ever needs the tick it is
// about to step — holding the whole log in memory made a 30-minute city cut
// unopenable (~10M intent messages per 15 sim-minutes). Seek keyframes are
// still re-fetched on demand from the sparse keyframe subject.
//
// Nothing is published and nothing steps until Run.
func NewPlayer(nc *nats.Conn, js nats.JetStreamContext, cfg PlayerConfig) (*Player, error) {
	// Namespace hygiene: the replay plane of "foo" lives under
	// "foo"+replayRunSuffix, so a source run id already ending in the
	// suffix would collide with another run's replay plane. validRunID
	// refuses it here, just as record-time entry points (RunLive,
	// NewRecorder) refuse recording into the reserved namespace.
	if err := validRunID(cfg.Run); err != nil {
		return nil, err
	}
	replayRun := cfg.Run + replayRunSuffix // valid: token chars + the suffix are all token chars
	speed := cfg.Speed
	if speed == 0 {
		speed = 1
	}
	if err := checkSpeed(speed); err != nil {
		return nil, err
	}
	lg := cfg.Log
	if lg == nil {
		lg = log.New(os.Stderr, "", 0)
	}

	reg, err := NewRegistry(js)
	if err != nil {
		return nil, err
	}
	meta, err := reg.Meta(cfg.Run)
	if err != nil {
		return nil, fmt.Errorf("no run registry meta for %q (was it recorded to this store?): %w", cfg.Run, err)
	}
	spec := meta.Spec
	if v := spec.Params.Dt / speed * float64(time.Second); math.IsInf(v, 0) || v < 1 || v >= float64(math.MaxInt64) {
		return nil, fmt.Errorf("speed %g puts the tick floor outside the representable range", speed)
	}
	stream := StreamName(cfg.Run)

	// Anchor on the tick-0 keyframe (recorder LogStart guarantees it: every
	// seek target ≥ 0 then has a keyframe ≤ target).
	kf, err := findKeyframe(js, stream, cfg.Run, 0)
	if err != nil {
		return nil, fmt.Errorf("run %q: %w", cfg.Run, err)
	}
	e, err := engine.RestoreState(spec, kf.payload)
	if err != nil {
		return nil, fmt.Errorf("run %q: restore tick-0 keyframe: %w", cfg.Run, err)
	}
	// A dirty store (a run id recorded twice into the same stream) can
	// yield two keyframes at the same tick — refuse the corrupt record
	// loud and early rather than seeking into an ambiguous log.
	kfTicks, err := firstKeyframeTicks(js, stream, cfg.Run)
	if err != nil {
		return nil, err
	}
	if len(kfTicks) >= 2 && kfTicks[1] == kfTicks[0] {
		return nil, fmt.Errorf("run %q: corrupt record: duplicate keyframes at tick %d — was this run id recorded twice into the same store?",
			cfg.Run, kfTicks[0])
	}
	lastTick, _, err := lastLoggedTick(js, stream, cfg.Run)
	if err != nil {
		return nil, err
	}
	cur, err := newLogCursor(js, stream, cfg.Run, 1)
	if err != nil {
		return nil, err
	}
	// The horizon the record actually covers. The registry completion
	// status is the proof: a run marked "done" RAN to spec.Ticks — the max
	// logged tick is exact today (the only recorder writes CRCEvery=1) but
	// is not the horizon's proof, and a sparser record would understate it.
	// Any other status means the run was killed or truncated: cap at the
	// last logged tick, because re-simming past the log would invent an
	// under-controlled tail — the post-log controller input never existed.
	end := spec.Ticks
	if meta.Status != StatusDone && lastTick < end {
		end = lastTick
	}

	// The live plane under the fresh replay run id, publish-only: the
	// player has no contract plane, so no intent subscription.
	bus, err := NewPublishBus(nc, replayRun, e)
	if err != nil {
		cur.close() // else the ephemeral consumer outlives the failed player
		return nil, err
	}

	p := &Player{
		js: js, src: cfg.Run, replayRun: replayRun, spec: spec, stream: stream, log: lg,
		cur: cur, endTick: end,
		bus: bus, e: e, speed: speed,
		ctrlCh: make(chan ctrlRequest), runDone: make(chan struct{}),
	}
	p.curTick.Store(e.Tick)
	return p, nil
}

// ReplayRun is the fresh run id the live plane is published under.
func (p *Player) ReplayRun() string { return p.replayRun }

// Done closes when the Run loop exits.
func (p *Player) Done() <-chan struct{} { return p.runDone }

// Stop asks the Run loop to exit (it returns between ticks).
func (p *Player) Stop() { p.stopping.Store(true) }

// Run drives the playback: publish the opening frames, then loop — service
// the control channel (pause/seek, serialized before the step decision),
// honor pause/end-of-recording holds (republishing the current frame at ~1
// Hz for late-attaching browsers), re-simulate one tick, publish at the
// decimated cadence, sleep the remainder of dt/speed. It returns when Stop
// is called; the end of the recording is a hold, not an exit.
func (p *Player) Run() error {
	defer close(p.runDone)
	// Unsubscribe the sig.req responder (and anything else the bus holds)
	// on exit: a stopped player must not keep answering — or sharing —
	// signal-table requests against a replacement.
	defer p.bus.Close()
	// Drop the log cursor's ephemeral consumer with it: replay is a
	// read-only path and must leave no broker state behind.
	defer p.cur.close()
	// Opening frames: the signal table once (republished at the
	// signalCatchUpEvery cadence from here on, mirroring run.go) and the
	// tick-0 snapshot so a browser attaching before the first tick already
	// sees the network.
	p.bus.PublishSignals(p.e)
	p.bus.PublishSnapshot(p.e)
	p.lastRepublish = time.Now()

	for !p.stopping.Load() {
		select {
		case req := <-p.ctrlCh:
			p.handleCtrl(req)
		default:
		}

		p.mu.Lock()
		paused, speed := p.paused, p.speed
		p.mu.Unlock()

		if p.e.Tick >= p.endTick {
			// End of the recording: hold position — stay up, paused, still
			// serving the control plane (a seek moves the playhead back).
			if p.done.CompareAndSwap(false, true) {
				p.mu.Lock()
				p.paused = true
				p.mu.Unlock()
				// Land exactly on the final tick even when the decimation
				// stride would have skipped it.
				p.bus.PublishSnapshot(p.e)
				p.lastRepublish = time.Now()
				p.log.Printf("replay: end of recording at tick %d — holding paused (seek to move, stop to quit)", p.e.Tick)
			}
			p.republishPaused()
			time.Sleep(pausePoll)
			continue
		}
		if paused {
			p.republishPaused()
			time.Sleep(pausePoll)
			continue
		}

		tickStart := time.Now()
		if err := p.stepTick(true); err != nil {
			p.log.Printf("replay: %v — stopping playback", err)
			return err
		}
		if p.e.Tick%decimation(speed, p.spec.Params.Dt) == 0 {
			p.bus.PublishSnapshot(p.e)
		}
		if p.e.Tick%signalCatchUpEvery == 0 {
			// Signal-table catch-up for late joiners (run.go's cadence).
			p.bus.PublishSignals(p.e)
		}
		if p.e.Tick >= p.endTick {
			// Just stepped onto the end of the record: engage the hold on
			// the next iteration NOW — sleeping the pace floor once more
			// first would leave status reading active for a whole floor.
			continue
		}
		// Pacing (ADR-0005 §4) is the wrapper's business: hold each tick to
		// dt/speed of wall time, chunked and re-reading the speed per chunk
		// so controls, /speed changes, and shutdown stay responsive at tiny
		// speeds. Never inside the engine Step path.
		p.paceSleep(tickStart)
	}
	return nil
}

// paceWake bounds how long the pacing sleep ever dozes uninterrupted: at
// speed 0.01 the tick floor is 10 s of wall time, and an unchunked sleep
// would stall /pause, /seek, /speed, and Stop behind it.
const paceWake = 50 * time.Millisecond

// paceSleep paces the just-stepped tick: wait until it has had dt/speed of
// wall time. The remaining wait is recomputed from the CURRENT speed every
// ≤paceWake chunk, so a /speed change takes effect within one chunk even at
// very low speeds (a fixed deadline would make a speed-up look frozen
// behind the old floor). The control channel is serviced between chunks; a
// pause ends the sleep early. Run-goroutine only.
func (p *Player) paceSleep(tickStart time.Time) {
	for !p.stopping.Load() {
		p.mu.Lock()
		speed := p.speed
		p.mu.Unlock()
		remaining := tickFloor(p.spec.Params.Dt, speed) - time.Since(tickStart)
		if remaining <= 0 {
			return
		}
		if remaining > paceWake {
			remaining = paceWake
		}
		time.Sleep(remaining)
		select {
		case req := <-p.ctrlCh:
			p.handleCtrl(req)
			// A pause serviced mid-floor ends the sleep early: draining
			// the rest of the original deadline would just delay the
			// paused-state republication at very slow speeds.
			p.mu.Lock()
			paused := p.paused
			p.mu.Unlock()
			if paused {
				return
			}
		default:
		}
	}
}

// handleCtrl executes one serialized control request and acks it with the
// status built at completion. Run-goroutine only.
func (p *Player) handleCtrl(req ctrlRequest) {
	switch req.kind {
	case ctrlPause:
		p.mu.Lock()
		p.paused = true
		p.mu.Unlock()
		req.ack <- ctrlAck{status: p.Status()}
	case ctrlResume:
		// Serviced on the loop goroutine, so this check is serialized
		// against the end-of-recording hold transition: a resume at (or
		// one tick before) the hold is refused rather than racing it.
		if p.done.Load() || p.e.Tick >= p.endTick {
			req.ack <- ctrlAck{status: p.Status(), err: errResumeDone}
			return
		}
		p.mu.Lock()
		p.paused = false
		p.mu.Unlock()
		req.ack <- ctrlAck{status: p.Status()}
	case ctrlSeek:
		err := p.seek(req.tick)
		req.ack <- ctrlAck{status: p.Status(), err: err}
	}
}

// republishPaused resends the current snapshot at ~1 Hz while the
// playhead is held, so a browser attaching mid-pause renders immediately.
// The signal TABLE does not ride this cadence (ADR-0016 §5): at city
// scale the full chunk set every second is a firehose aimed at exactly
// the busy tabs it targets — paused attaches resync via the request/reply
// path (ts.{run}.state.sig.req) instead. Run-goroutine only.
func (p *Player) republishPaused() {
	if time.Since(p.lastRepublish) >= pausedRepublishInterval {
		p.bus.PublishSnapshot(p.e)
		p.lastRepublish = time.Now()
	}
}

// stepTick advances one tick: re-enqueue the logged intents and director
// verbs for the tick, Step, verify the logged CRC. Divergence policy is
// LOUD-AND-CONTINUE (rate-limited stderr) — ReplayFromStream remains the
// strict audit path. countErrs distinguishes forward playback (counted —
// the /status counters mean "divergences during forward playback") from
// seek re-sim (verified and logged, but NOT counted: scrubbing back and
// forth across a divergent span must not inflate them). Run-goroutine only.
func (p *Player) stepTick(countErrs bool) error {
	next := p.e.Tick + 1
	rec, err := p.cur.records(next)
	if err != nil {
		// A read fault is not a divergence and must not be swallowed: the
		// record is unreadable from here, so stepping anyway would invent an
		// uncontrolled tick, and retrying in place would spin (seek's re-sim
		// loop advances only on a successful step). Fail the caller.
		return fmt.Errorf("read log at tick %d: %w", next, err)
	}
	for _, k := range rec.intents {
		p.e.EnqueueIntent(k)
	}
	for _, d := range rec.verbs {
		if err := p.e.EnqueueSpawn(d); err != nil {
			p.reportDivergence(countErrs, &p.verbErrs,
				fmt.Sprintf("verb %q at tick %d rejected (%v) — record and spec disagree", d.RequestID, next, err))
		}
	}
	p.e.Step()
	if rec.hasCRC && p.e.CRC() != rec.crc {
		p.reportDivergence(countErrs, &p.crcErrs,
			fmt.Sprintf("CRC divergence at tick %d: crc %016x, logged %016x", next, p.e.CRC(), rec.crc))
	}
	p.curTick.Store(p.e.Tick)
	return nil
}

// reportDivergence logs loudly (rate-limited when counting: first 3
// verbatim, then every 100th) and increments the counter during forward
// playback only. Run-goroutine only.
func (p *Player) reportDivergence(count bool, ctr *atomic.Uint64, msg string) {
	if !count {
		p.log.Printf("replay: %s (seek re-sim; not counted)", msg)
		return
	}
	if n := ctr.Add(1); n <= 3 || n%100 == 0 {
		p.log.Printf("replay: %s (divergence #%d; continuing)", msg, n)
	}
}

// seek moves the playhead to target: nearest keyframe ≤ target (clamped to
// the tick-0 keyframe floor and to the end of the record), RestoreState,
// silent re-sim (CRC-verified, nothing published) up to target, then one
// frame at the landing tick — also while paused, so the user sees where
// they landed. Cost ≤ the keyframe cadence in ticks (recorder
// KeyframeEvery, default 100). Run-goroutine only.
func (p *Player) seek(target uint64) error {
	if target > p.endTick {
		target = p.endTick
	}
	kf, err := findKeyframe(p.js, p.stream, p.src, target)
	if err != nil {
		return err
	}
	e, err := engine.RestoreState(p.spec, kf.payload)
	if err != nil {
		return fmt.Errorf("restore keyframe tick %d: %w", kf.tick, err)
	}
	// Re-anchor the forward cursor behind the landing keyframe. kf.seq is the
	// keyframe's LAST message, so the re-sim starts at seq+1 — after the
	// complete keyframe, exactly where ReplayFromStream resumes. Seeking is
	// the only way the playhead moves backwards, so this is the only place
	// the cursor is ever repositioned.
	if err := p.cur.reset(kf.seq + 1); err != nil {
		return err
	}
	p.e = e
	p.curTick.Store(e.Tick)
	for p.e.Tick < target {
		// Verify + log, but the counters are forward-playback only.
		if err := p.stepTick(false); err != nil {
			return err
		}
	}
	p.done.Store(false)
	if target >= p.endTick {
		// Landing on the end of the record re-engages the hold semantics
		// immediately — done (so /resume is a 409) and paused, exactly as
		// if the playhead had run off the end on its own.
		p.done.Store(true)
		p.mu.Lock()
		p.paused = true
		p.mu.Unlock()
	}
	p.bus.PublishSnapshot(p.e)
	p.lastRepublish = time.Now()
	// Unconditional: TSSG is static per run today, but the work-queue's demo
	// program will make tables mutable mid-run — a seek landing must never
	// leave clients deriving light state from the wrong side of the seek.
	p.bus.PublishSignals(p.e)
	return nil
}

// Status snapshots the player for the control plane.
func (p *Player) Status() PlayerStatus {
	p.mu.Lock()
	paused, speed := p.paused, p.speed
	p.mu.Unlock()
	return PlayerStatus{
		Run:        p.src,
		ReplayRun:  p.replayRun,
		Tick:       p.curTick.Load(),
		Ticks:      p.spec.Ticks,
		EndTick:    p.endTick,
		Speed:      speed,
		Paused:     paused,
		Done:       p.done.Load(),
		Dt:         p.spec.Params.Dt,
		Hash:       p.spec.Hash,
		CRCErrors:  p.crcErrs.Load(),
		VerbErrors: p.verbErrs.Load(),
	}
}

// Handler returns the HTTP control plane (stdlib net/http, JSON, no
// framework):
//
//	POST /pause                  hold the playhead (the viz keeps its last frame);
//	                             once it returns, no further tick executes
//	POST /resume                 continue stepping; 409 while done (seek back first)
//	POST /speed  {"speed": N}    live pace change; the publish stride is recomputed
//	POST /seek   {"tick": T}     keyframe ≤ T, silent re-sim, one frame at T
//	                             (also while paused); 200 once the seek executed,
//	                             with the status at the landing tick
//	GET  /status                 PlayerStatus JSON
//
// Every successful POST answers with the PlayerStatus JSON after the change
// took effect. Handlers never touch the engine: /speed sets mutex-protected
// control state; /pause, /resume, and /seek hand the request to the Run
// goroutine, which executes it between ticks (the resume 409 is decided by
// the loop, serialized against the end-of-recording hold).
func (p *Player) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /pause", func(w http.ResponseWriter, _ *http.Request) {
		ack, ok := p.ctrl(ctrlRequest{kind: ctrlPause})
		if !ok {
			http.Error(w, "player stopped", http.StatusServiceUnavailable)
			return
		}
		p.writeStatus(w, ack.status)
	})
	mux.HandleFunc("POST /resume", func(w http.ResponseWriter, _ *http.Request) {
		ack, ok := p.ctrl(ctrlRequest{kind: ctrlResume})
		if !ok {
			http.Error(w, "player stopped", http.StatusServiceUnavailable)
			return
		}
		if ack.err != nil {
			code := http.StatusInternalServerError
			if errors.Is(ack.err, errResumeDone) {
				code = http.StatusConflict
			}
			http.Error(w, ack.err.Error(), code)
			return
		}
		p.writeStatus(w, ack.status)
	})
	mux.HandleFunc("POST /speed", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Speed float64 `json:"speed"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `bad JSON body (want {"speed": N})`, http.StatusBadRequest)
			return
		}
		if err := checkSpeed(body.Speed); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if v := p.spec.Params.Dt / body.Speed * float64(time.Second); math.IsInf(v, 0) || v < 1 || v >= float64(math.MaxInt64) {
			http.Error(w, fmt.Sprintf("speed %g puts the tick floor outside the representable range", body.Speed), http.StatusBadRequest)
			return
		}
		p.mu.Lock()
		p.speed = body.Speed
		p.mu.Unlock()
		p.writeStatus(w, p.Status())
	})
	mux.HandleFunc("POST /seek", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tick *uint64 `json:"tick"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Tick == nil {
			http.Error(w, `bad JSON body (want {"tick": T})`, http.StatusBadRequest)
			return
		}
		ack, ok := p.ctrl(ctrlRequest{kind: ctrlSeek, tick: *body.Tick})
		if !ok {
			http.Error(w, "player stopped", http.StatusServiceUnavailable)
			return
		}
		if ack.err != nil {
			http.Error(w, "seek failed: "+ack.err.Error(), http.StatusInternalServerError)
			return
		}
		p.writeStatus(w, ack.status)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		p.writeStatus(w, p.Status())
	})
	return mux
}

// ctrl posts a serialized control request to the Run goroutine and waits
// for its ack; ok=false means the player has stopped.
func (p *Player) ctrl(req ctrlRequest) (ctrlAck, bool) {
	req.ack = make(chan ctrlAck, 1)
	select {
	case p.ctrlCh <- req:
	case <-p.runDone:
		return ctrlAck{}, false
	}
	select {
	case ack := <-req.ack:
		return ack, true
	case <-p.runDone:
		return ctrlAck{}, false
	}
}

func (p *Player) writeStatus(w http.ResponseWriter, st PlayerStatus) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// decimation is the snapshot publish stride: every k-th tick with
// k = max(1, round(speed × 0.1/dt)). The tick rate is speed/dt per wall
// second, so dividing it by k holds the published rate at ≈10 Hz at any
// pace and any dt (dt = 100 ms, speed 8 → every 8th tick; a pace whose tick
// rate is already ≤10 Hz publishes every tick).
func decimation(speed, dt float64) uint64 {
	k := uint64(math.Round(speed * 0.1 / dt))
	if k < 1 {
		k = 1
	}
	return k
}

// tickFloor is the wall-time budget per tick at the given pace: dt/speed.
func tickFloor(dt, speed float64) time.Duration {
	return time.Duration(dt / speed * float64(time.Second))
}

func checkSpeed(speed float64) error {
	if math.IsNaN(speed) || math.IsInf(speed, 0) || speed <= 0 {
		return fmt.Errorf("speed must be a finite value > 0 (got %g)", speed)
	}
	return nil
}
