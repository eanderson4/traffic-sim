package main

// supervisor.go — single-active-run lifecycle for engine children (`serve`
// for live demos, `replay` for VCR recordings): spawn, wait for readiness
// (the menu's "you may navigate" signal — the viz opens its socket
// immediately on load), and SIGTERM→SIGKILL(2s) on replace/stop. One mutex
// serializes start/stop; the injectable spawnFunc keeps the tests on
// `sleep`, never the real engine.
//
// Readiness for a DEMO child is two-phase (the port alone proved too weak
// — see waitIdentity): a short TCP probe of the WebSocket port, then an
// identity probe that confirms the broker answering on that port carries
// OUR run's registry entry. Replay children keep the control-port probe
// (their identity check is the /status hash verification in demosrv).

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"traffic-sim/engine/natsio"
)

const (
	// replayCtlAddr is the ONE replay control-plane port (engine/cmd/replay
	// -http), fixed for the same reason as the ws port: single active run
	// makes conflicts impossible. demosrv proxies it under /api/replay/.
	replayCtlAddr = "127.0.0.1:8901"
	// readyTimeout bounds the wait for the engine's listener. The binary is
	// prebuilt at demosrv startup, so this covers process exec + embedded
	// broker startup — the ws listener binds at ns.Start() BEFORE the world
	// build (engine/cmd/serve), so the port opens in ~1 s even on the
	// biggest nets; world build is covered by the identity phase below. A
	// child that DIES aborts this wait immediately (waitPort's done
	// channel), so a generous bound costs nothing on the failure path.
	readyTimeout = 60 * time.Second
	// identityTimeout bounds the registry-identity phase for demo spawns:
	// {run}/meta lands only after world build, which is slow on big
	// networks (sf-lean ~100 s, la-lean ~400 s under load). Matches the
	// -attach-timeout demosrv passes serve (main.go).
	identityTimeout = 660 * time.Second
	// killGrace is the SIGTERM grace before SIGKILL. serve traps SIGTERM and
	// abandons the run (demo mode does no graceful finish — see
	// engine/cmd/serve), so a healthy child exits well within this.
	killGrace = 2 * time.Second
)

// wsListenAddr is the ONE engine WebSocket port. Single active run means no
// port allocation problem; the default matches the viz client's default
// ?ws= target (viz/src/config.ts). A var, not a const: main() overrides it
// from the -ws flag when another process already holds 8443, and the start
// handlers + /api/demos then tell the client where to connect instead.
var wsListenAddr = "127.0.0.1:8443"

// wsPublicURL, when set (-wspublic), is THE client-facing engine WebSocket
// URL, used verbatim everywhere a browser is told where to dial (registry
// WS, /api/demos, start deep links). It exists for the TLS-terminating-LB
// deployment: the public name, scheme (wss), and port (443) belong to the
// load balancer, and no host/port rewrite of wsListenAddr can reconstruct
// them. Empty = derive from wsListenAddr (the legacy local behavior).
var wsPublicURL string

// wsDialURL normalizes a listen address into a dialable ws:// URL:
// wildcard/empty hosts are not dialable and become loopback.
func wsDialURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || (host != "" && host != "0.0.0.0" && host != "::") {
		return "ws://" + addr
	}
	return "ws://127.0.0.1:" + port
}

// wsClientURL is the ws:// URL the viz should dial, for embedding in /app/
// links and the /api/demos payload. VERBATIM listen address — this is a
// public URL: normalizing a wildcard listen to loopback (wsDialURL, the
// probe-side helper) would point remote browsers at themselves; the
// operator owning -ws owns what clients dial. A -wspublic override wins
// over all of this (see wsPublicURL).
func wsClientURL() string {
	if wsPublicURL != "" {
		return wsPublicURL
	}
	return "ws://" + wsListenAddr
}

// spawnTarget identifies the child to exec: a live demo (Kind "demo",
// serve) or a VCR recording (Kind "replay", engine/cmd/replay). Exactly
// one of Demo/Rec is non-nil, matching Kind.
type spawnTarget struct {
	Kind string
	Demo *Demo
	Rec  *Recording
}

func (t spawnTarget) id() string {
	if t.Kind == "replay" {
		return t.Rec.ID
	}
	return t.Demo.ID
}

// spawnFunc starts the target's engine process and returns the running
// command (cmd.Start already called).
type spawnFunc func(t spawnTarget) (*exec.Cmd, error)

type activeRun struct {
	target    spawnTarget
	cmd       *exec.Cmd
	startedAt time.Time
	done      chan struct{} // closed by the reaper when the process exits
}

type supervisor struct {
	mu     sync.Mutex
	active *activeRun
	// closed latches FINAL shutdown (shutdownFinal, the signal handler):
	// once set, every start refuses (errSupervisorClosed) — a start
	// beginning after the last stop would orphan its engine child as
	// demosrv exits. ATOMIC, stored before shutdownFinal's stopEpoch
	// bump and checked under mu both before AND after the start's epoch
	// snapshot, so no start can slip the latch AND dodge the abort
	// (sol, round 4). Distinct from stop(): the menu's stop/start cycle
	// keeps working.
	closed atomic.Bool
	// stopEpoch is the abort mechanism: stop()/shutdownFinal() bump it
	// BEFORE taking mu, and each start snapshots it under mu — the
	// readiness probes abort on any later bump, so an in-flight start
	// fails fast instead of holding mu through a 660 s identity wait
	// with a stop queued behind it. No stamp/clear dance: nothing is
	// ever cleared, so overlapping stops cannot erase each other's
	// abort, and a start that begins after the bump simply snapshots
	// the new epoch (Fable + sol, rounds 14-16).
	stopEpoch atomic.Uint64
	// stopPending counts stops between their epoch bump and their mu
	// acquisition: a start that wins the lock race against a QUEUED stop
	// refuses (errStartAborted) instead of spawning a child the stop
	// will kill after a full readiness wait (sol, round 17).
	stopPending atomic.Int64
	// statusSnap is the lock-free status snapshot: /api/status polls every
	// ~3 s and must not queue behind a long start's mu.
	statusSnap atomic.Value // statusSnapshot

	spawn spawnFunc
	// ready, when non-nil, replaces the default port probe (tests inject a
	// no-op for the sleep-stub lifecycle, or shrink wsAddr/readyTimeout and
	// keep the real probe for the timeout case).
	ready        func() error
	wsAddr       string
	ctlAddr      string // replay control-plane port (replay-kind probes only)
	readyTimeout time.Duration
	// identityTimeout bounds waitIdentity (demo-kind spawns only).
	identityTimeout time.Duration
	killGrace       time.Duration
}

func newSupervisor(spawn spawnFunc) *supervisor {
	return &supervisor{
		spawn:           spawn,
		wsAddr:          wsListenAddr,
		ctlAddr:         replayCtlAddr,
		readyTimeout:    readyTimeout,
		identityTimeout: identityTimeout,
		killGrace:       killGrace,
	}
}

// runNonce suffixes live-demo run ids (start). CRYPTOGRAPHICALLY random —
// the suffix is the readiness ownership proof (a foreign broker can never
// hold the key), and an entropy failure must FAIL THE SPAWN rather than
// silently degrade to the clock-derived uniqueness the proof exists to
// replace. A var so tests can pin it.
var runNonce = func() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("run nonce: crypto/rand unavailable: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// errAlreadyActive is startIfIdle's refusal: something went active before
// autostart's retry took the lock (an operator's authed start) — the
// active run is LEFT ALONE, never replaced.
var errAlreadyActive = errors.New("a run is already active")

// errSupervisorClosed is every start's refusal after shutdownFinal (the
// signal handler): a start that began after the final stop would orphan
// its engine child on the ws port as demosrv exits (Fable + sol, round 3).
var errSupervisorClosed = errors.New("supervisor shut down")

// errStartAborted marks a start aborted by a stop()/shutdownFinal()
// generation stamp (waitPort/waitIdentity): an operator's DELIBERATE stop,
// not a transient failure — autostart must treat it as terminal rather
// than retry the demo the operator just stopped (Fable, round 12).
var errStartAborted = errors.New("start aborted (stop requested)")

// start replaces any active child with the target's engine and blocks
// until the child is ready (see waitReady). Kill-before-spawn covers BOTH
// kinds: a replay child must die before a serve starts and vice versa
// (JetStream store-dir exclusivity + the single ws port). A spawn or
// readiness failure leaves NO process behind (the just-started child is
// killed on the way out).
//
// verify, when non-nil, runs AFTER readiness with the start serialization
// still held: post-ready checks that assume the just-started child is
// still current (demosrv's recording hash check GETs the child's /status)
// must run here — a concurrent start cannot swap the child in between. A
// verify failure kills the child and fails the start.
// start returns the LIVE run id (per-spawn unique for demos; the
// recording's stable id for replays) — the caller must thread exactly
// this into the app URL, so no status re-read can mix generations.
func (s *supervisor) start(t spawnTarget, verify func() error) (string, error) {
	return s.startGuarded(t, verify, false, 0)
}

// startIfIdle is -autostart's start (deploy.go): the idle check rides the
// SAME mu hold as the spawn, so an operator's start can never slip between
// check and act (the check-then-act race Fable + sol flagged in round 2) —
// whatever is active when the lock is taken wins, autostart backs off.
// epoch0 is autostart's stop-epoch snapshot: a stop epoch that moved since
// — even a stop that COMPLETED while this start was queued on mu — refuses
// the start (errStartAborted), so autostart never un-stops what an
// operator stopped (Fable, round 18).
func (s *supervisor) startIfIdle(t spawnTarget, epoch0 uint64) (string, error) {
	return s.startGuarded(t, nil, true, epoch0)
}

// startGuarded is start with the autostart guards. The ORDER of the first
// three checks is load-bearing against stop()'s pending-then-bump order:
// the epoch snapshot comes BEFORE the pending check, so a stop landing
// mid-guard either trips pending (its mark preceded our check) or has its
// epoch bump after our snapshot (the probes abort) — no interleaving
// leaves a start both unrefused and unabortable (sol, round 18).
// onlyIfIdle adds the epoch0 + s.active checks for autostart.
func (s *supervisor) startGuarded(t spawnTarget, verify func() error, onlyIfIdle bool, epoch0 uint64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return "", errSupervisorClosed
	}
	// Epoch snapshotted UNDER mu, BEFORE the pending check: any stop()
	// or shutdownFinal() bump after this point aborts the start at its
	// next probe.
	epoch := s.stopEpoch.Load()
	if s.stopPending.Load() > 0 {
		// A stop is QUEUED behind this lock acquisition: refuse fast
		// rather than spawn a child the stop will kill after a full
		// readiness wait (sol, round 17).
		return "", errStartAborted
	}
	if onlyIfIdle && epoch != epoch0 {
		// A stop landed since the caller's snapshot — possibly one that
		// already COMPLETED while this start queued on mu (Fable, round
		// 18). Terminal for autostart.
		return "", errStartAborted
	}
	if onlyIfIdle && s.active != nil {
		select {
		case <-s.active.done:
			// EXITED but not yet reaped: the reaper clears s.active
			// asynchronously, and errAlreadyActive is TERMINAL for
			// autostart — a corpse in this window must read as idle,
			// not as "someone else won" (sol, round 6).
			s.active = nil
			s.mirrorStatus()
		default:
			return "", fmt.Errorf("%w: %s", errAlreadyActive, s.active.target.id())
		}
	}
	// closed AGAIN (still under mu): shutdownFinal stores closed FIRST,
	// so a shutdown landing between the first check and this one either
	// trips this check, or has already bumped pending (caught above) or
	// the epoch past our snapshot — a start can never slip the latch AND
	// dodge the abort (sol, round 4).
	if s.closed.Load() {
		return "", errSupervisorClosed
	}
	s.stopLocked()
	if t.Kind == "demo" {
		// Unique run id per spawn: the run id IS the registry key and the
		// snapshot-subject namespace, so a foreign broker (another session,
		// a stale engine) can never hold our key — readiness becomes an
		// exact ownership proof, no nonce contract needed. Replay keeps its
		// stable recording-bound id.
		d := *t.Demo
		nonce, err := runNonce()
		if err != nil {
			return "", err
		}
		d.Run = fmt.Sprintf("%s-%s", d.Run, nonce)
		t.Demo = &d
	}
	cmd, err := s.spawn(t)
	if err != nil {
		return "", fmt.Errorf("spawn %s: %w", t.id(), err)
	}
	a := &activeRun{target: t, cmd: cmd, startedAt: time.Now(), done: make(chan struct{})}
	go func() {
		// Reap. The exit error is expected (SIGTERM/SIGKILL is the normal
		// way out) and the child's own log carries anything interesting.
		cmd.Wait()
		close(a.done)
		// A child that dies on its own must not linger as "running" — not
		// in the lock-free status mirror (the menu badge must never claim
		// a corpse is running) and not in s.active (startIfIdle's idle
		// guard reads it under mu; a corpse there would block autostart
		// forever — sol, round 3). Only clear when THIS run is still the
		// active one — a swap already replaced both with the new run's.
		s.mu.Lock()
		if s.active == a {
			s.active = nil
			s.statusSnap.Store(statusSnapshot{})
		}
		s.mu.Unlock()
	}()
	s.active = a
	// "active" = starting-or-running: the status mirror goes up BEFORE
	// readiness so the menu's running card can deep-link into the map's
	// loading overlay (with its watchdog) during a long world build,
	// instead of showing idle and inviting a duplicate start.
	s.mirrorStatus()
	if err := s.waitReady(a, epoch); err != nil {
		s.stopLocked()
		return "", fmt.Errorf("%s did not become ready: %w%s", t.id(), err, tailSuffix(cmd))
	}
	if verify != nil {
		if err := verify(); err != nil {
			s.stopLocked()
			return "", fmt.Errorf("%s failed post-start verification: %w%s", t.id(), err, tailSuffix(cmd))
		}
	}
	// Final guard revalidation, STILL under mu, in the entry guard's
	// pending-then-epoch order: a stop's mark PRECEDES its epoch bump,
	// so a stop preempted between the two would slip an epoch-only check
	// (sol, round 25). A stop (or shutdown) that marked itself during
	// readiness/verify must not see this start return success for a
	// child the stop then immediately kills (sol, round 24). The bumper
	// cannot have completed (it needs mu, which we hold), so a hit
	// always means a stop is queued behind us.
	if s.closed.Load() {
		s.stopLocked()
		return "", errSupervisorClosed
	}
	if s.stopPending.Load() > 0 || s.stopEpoch.Load() != epoch {
		s.stopLocked()
		return "", errStartAborted
	}
	if t.Kind == "replay" {
		return t.Rec.Run, nil
	}
	return t.Demo.Run, nil
}

// tailSuffix renders the child's captured output tail (prefixWriter's ring)
// for inclusion in a start error — the difference between a bare 502 and
// "engine exited during startup: … driver: hello: nats: no responders". ""
// when the spawner captured nothing (the test sleep-stub has no
// prefixWriter). Read after the child is down: no race with its writers.
func tailSuffix(cmd *exec.Cmd) string {
	w, ok := cmd.Stdout.(interface{ tail() string })
	if !ok {
		return ""
	}
	t := w.tail()
	if t == "" {
		return ""
	}
	return "\nengine output (last lines):\n" + t
}

func (s *supervisor) stop() {
	// Bump the epoch BEFORE taking mu: a start can hold mu through a
	// ~660 s identity wait, and its probes must fail fast and release
	// instead of leaving demosrv + child running until the timeout
	// (SIGINT path — sol review). No clear afterwards: overlapping stops
	// can never erase each other's abort, and a start that begins after
	// the bump snapshots the new epoch and is unaffected (Fable + sol,
	// rounds 14-16).
	s.stopPending.Add(1) // pending FIRST: a start between the two marks refuses
	s.stopEpoch.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopPending.Add(-1)
	s.stopLocked()
}

// shutdownFinal is the signal handler's stop: stop() PLUS the closed
// latch, so no start can begin after the final stop (a plain stop leaves
// the gap the shutdown channel only narrows — autostart could pass its
// channel check, then spawn after stop returned; Fable + sol, round 3).
// closed is stored FIRST, then pending, then the epoch bump: startGuarded
// checks them in the same order under mu, so a start that saw the latch
// open either trips pending or has its epoch snapshot covered by the bump
// (sol, round 18 — the bump order is load-bearing).
func (s *supervisor) shutdownFinal() {
	s.closed.Store(true)
	s.stopPending.Add(1)
	s.stopEpoch.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopPending.Add(-1)
	s.stopLocked()
}

// stopLocked SIGTERMs the active child and reaps it, escalating to SIGKILL
// after the grace period. Callers hold s.mu.
func (s *supervisor) stopLocked() {
	a := s.active
	if a == nil {
		s.mirrorStatus()
		return
	}
	s.active = nil
	s.mirrorStatus()
	_ = a.cmd.Process.Signal(syscall.SIGTERM) // already-exited is fine
	select {
	case <-a.done:
	case <-time.After(s.killGrace):
		_ = a.cmd.Process.Kill()
		<-a.done
	}
}

// status reports the active run. An already-exited child (a run that
// reached its tick horizon, or a crash) reads as IDLE — the menu badge
// must never claim a corpse is running.
// statusSnapshot is the lock-free mirror of the active run (statusSnap) —
// the menu polls /api/status every few seconds and must not queue behind
// a start's up-to-660 s identity wait (sol review).
type statusSnapshot struct {
	id        string
	run       string
	pid       int
	startedAt time.Time
}

// status reports the active run WITHOUT taking mu. An already-exited
// child reads as IDLE within one reaper pass (the reaper clears the
// snapshot) — the menu badge must never claim a corpse is running.
func (s *supervisor) status() (id, run string, pid int, startedAt time.Time, ok bool) {
	v := s.statusSnap.Load()
	if v == nil {
		return "", "", 0, time.Time{}, false
	}
	snap := v.(statusSnapshot)
	if snap.id == "" {
		return "", "", 0, time.Time{}, false
	}
	return snap.id, snap.run, snap.pid, snap.startedAt, true
}

// mirrorStatus refreshes the lock-free snapshot from the current active
// run. Callers hold s.mu (or are the reaper, which takes it).
func (s *supervisor) mirrorStatus() {
	a := s.active
	if a == nil {
		s.statusSnap.Store(statusSnapshot{})
		return
	}
	run := ""
	if a.target.Kind == "demo" {
		run = a.target.Demo.Run
	} else {
		run = a.target.Rec.Run
	}
	s.statusSnap.Store(statusSnapshot{id: a.target.id(), run: run, pid: a.cmd.Process.Pid, startedAt: a.startedAt})
}

// errNoActiveReplay is withActiveReplay's result when no replay child is
// live (idle, a demo running, or the replay already exited).
var errNoActiveReplay = errors.New("no active replay")

// withActiveReplay runs fn with the supervisor lock HELD, passing the live
// replay child's run id ({run}-replay). This is the ctl-forwarding
// discipline: the active-replay check and the forward to the fixed control
// port must be atomic, or a concurrent start/stop could swap the child in
// between and a command authorized for one recording would land on another.
// The price: fn blocks start/stop for its duration (a seek forward can
// hold the lock for the seek timeout, ~10 s — acceptable for the demo
// console, where one slow seek simply delays the next start click).
func (s *supervisor) withActiveReplay(fn func(run string) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.active
	if a == nil || a.target.Kind != "replay" {
		return errNoActiveReplay
	}
	select {
	case <-a.done:
		return errNoActiveReplay
	default:
	}
	return fn(a.target.Rec.Run + "-replay")
}

// waitReady probes the child's readiness. A REPLAY child is ready when BOTH
// the WebSocket port and its HTTP control port accept: replay's ws listener
// (the embedded broker) opens before NewPlayer has indexed the recording
// and before the control listener binds (engine/cmd/replay), and demosrv's
// start response sends the viz straight at the control plane — answering
// early would race the replay panel's first probe. A live DEMO adds the
// identity phase after the port probe (waitIdentity): the port accepting is
// not enough to know the broker behind it is ours.
func (s *supervisor) waitReady(a *activeRun, epoch uint64) error {
	if s.ready != nil {
		return s.ready()
	}
	if err := s.waitPort(s.wsAddr, s.readyTimeout, a.done, epoch); err != nil {
		return err
	}
	if a.target.Kind == "replay" {
		return s.waitPort(s.ctlAddr, s.readyTimeout, a.done, epoch)
	}
	return s.waitIdentity(a.target.Demo.Run, a.startedAt, a.done, epoch)
}

// waitIdentity answers the question the raw port probe cannot: the listener
// on the ws port may be a FOREIGN process (another session's engine — the
// classic incident: the port probe saw its listener, declared the child
// ready, and the viz subscribed to a broker that had no such run and sat on
// "waiting for the engine's first snapshot" forever). Our engine is ready
// only once ITS run appears in the KV run registry: reg.Start writes
// {run}/meta inside RunLive before the sim loop starts, after the world
// build — hence the generous identityTimeout where the port probe is short.
//
// done aborts the wait immediately: when the port is held by a foreign
// process our child dies on bind failure, and that error must surface in
// seconds, not after the timeout.
func (s *supervisor) waitIdentity(run string, spawnedAt time.Time, done <-chan struct{}, epoch uint64) error {
	url := wsDialURL(s.wsAddr)
	deadline := time.Now().Add(s.identityTimeout)
	var lastErr error
	for {
		if s.stopEpoch.Load() != epoch {
			// A stop (or shutdown) bumped the epoch after this start
			// snapshotted it: the abort is DELIBERATE, fail fast.
			return errStartAborted
		}
		select {
		case <-done:
			return fmt.Errorf("engine exited during startup: run %q never registered in the %s bucket", run, natsio.RegistryBucket)
		default:
		}
		if lastErr = natsio.ProbeRunMeta(url, run, spawnedAt.Unix()); lastErr == nil {
			// A passing probe is only as good as the child behind it: the
			// run may have registered while our child lost the port race
			// and died — never declare ready over a corpse (sol review).
			select {
			case <-done:
				return fmt.Errorf("engine exited after registering: run %q belongs to a foreign process", run)
			default:
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("run %q not found in the %s registry at %s within %s (last probe: %v) — a foreign process may hold this port",
				run, natsio.RegistryBucket, url, s.identityTimeout, lastErr)
		}
		time.Sleep(time.Second)
	}
}

// waitPort polls until addr accepts a TCP connection or the timeout
// expires. done fails the wait FAST: a child that dies at startup (bad
// scenario, engine panic) must not block start for the full timeout with
// a generic dial error — and once the child is dead, a port that accepts
// belongs to some STRAY process, never to us (report the death, not OK).
// The epoch check is the stop abort (see waitIdentity).
func (s *supervisor) waitPort(addr string, timeout time.Duration, done <-chan struct{}, epoch uint64) error {
	deadline := time.Now().Add(timeout)
	for {
		if s.stopEpoch.Load() != epoch {
			return errStartAborted
		}
		select {
		case <-done:
			return fmt.Errorf("child exited before %s accepted connections (see its log)", addr)
		default:
		}
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not accept connections within %s", addr, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
