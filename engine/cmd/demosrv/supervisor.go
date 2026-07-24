package main

// supervisor.go — single-active-run lifecycle for engine children (`serve`
// for live demos, `replay` for VCR recordings): spawn, wait for the
// WebSocket port to accept TCP (the menu's "you may navigate" signal — the
// viz opens its socket immediately on load), and SIGTERM→SIGKILL(2s) on
// replace/stop. One mutex serializes start/stop; the injectable spawnFunc
// keeps the tests on `sleep`, never the real engine.

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// wsListenAddr is the ONE engine WebSocket port. Single active run means
	// no port allocation problem, and the viz client's default ?ws= target
	// (viz/src/config.ts) already points here, so the menu's deep link never
	// has to carry it.
	wsListenAddr = "127.0.0.1:8443"
	// replayCtlAddr is the ONE replay control-plane port (engine/cmd/replay
	// -http), fixed for the same reason as the ws port: single active run
	// makes conflicts impossible. demosrv proxies it under /api/replay/.
	replayCtlAddr = "127.0.0.1:8901"
	// readyTimeout bounds the wait for the engine's listener. The binary is
	// prebuilt at demosrv startup, so this covers process exec + embedded
	// broker startup, not a compile.
	readyTimeout = 10 * time.Second
	// killGrace is the SIGTERM grace before SIGKILL. serve traps SIGTERM and
	// abandons the run (demo mode does no graceful finish — see
	// engine/cmd/serve), so a healthy child exits well within this.
	killGrace = 2 * time.Second
)

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

	spawn spawnFunc
	// ready, when non-nil, replaces the default port probe (tests inject a
	// no-op for the sleep-stub lifecycle, or shrink wsAddr/readyTimeout and
	// keep the real probe for the timeout case).
	ready        func() error
	wsAddr       string
	ctlAddr      string // replay control-plane port (replay-kind probes only)
	readyTimeout time.Duration
	killGrace    time.Duration
}

func newSupervisor(spawn spawnFunc) *supervisor {
	return &supervisor{
		spawn:        spawn,
		wsAddr:       wsListenAddr,
		ctlAddr:      replayCtlAddr,
		readyTimeout: readyTimeout,
		killGrace:    killGrace,
	}
}

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
func (s *supervisor) start(t spawnTarget, verify func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	cmd, err := s.spawn(t)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", t.id(), err)
	}
	a := &activeRun{target: t, cmd: cmd, startedAt: time.Now(), done: make(chan struct{})}
	go func() {
		// Reap. The exit error is expected (SIGTERM/SIGKILL is the normal
		// way out) and the child's own log carries anything interesting.
		cmd.Wait()
		close(a.done)
	}()
	s.active = a
	if err := s.waitReady(a); err != nil {
		s.stopLocked()
		return fmt.Errorf("%s did not become ready: %w", t.id(), err)
	}
	if verify != nil {
		if err := verify(); err != nil {
			s.stopLocked()
			return fmt.Errorf("%s failed post-start verification: %w", t.id(), err)
		}
	}
	return nil
}

func (s *supervisor) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// stopLocked SIGTERMs the active child and reaps it, escalating to SIGKILL
// after the grace period. Callers hold s.mu.
func (s *supervisor) stopLocked() {
	a := s.active
	if a == nil {
		return
	}
	s.active = nil
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
func (s *supervisor) status() (id string, pid int, startedAt time.Time, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.active
	if a == nil {
		return "", 0, time.Time{}, false
	}
	select {
	case <-a.done:
		return "", 0, time.Time{}, false
	default:
	}
	return a.target.id(), a.cmd.Process.Pid, a.startedAt, true
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

// waitReady probes the child's readiness. A live demo is ready when its
// WebSocket port accepts TCP. A REPLAY child is ready only when BOTH the
// WebSocket port and its HTTP control port accept: replay's ws listener
// (the embedded broker) opens before NewPlayer has indexed the recording
// and before the control listener binds (engine/cmd/replay), and demosrv's
// start response sends the viz straight at the control plane — answering
// early would race the replay panel's first probe.
func (s *supervisor) waitReady(a *activeRun) error {
	if s.ready != nil {
		return s.ready()
	}
	if err := waitPort(s.wsAddr, s.readyTimeout, a.done); err != nil {
		return err
	}
	if a.target.Kind == "replay" {
		return waitPort(s.ctlAddr, s.readyTimeout, a.done)
	}
	return nil
}

// waitPort polls until addr accepts a TCP connection or the timeout
// expires. done fails the wait FAST: a child that dies at startup (bad
// scenario, engine panic) must not block start for the full timeout with
// a generic dial error — and once the child is dead, a port that accepts
// belongs to some STRAY process, never to us (report the death, not OK).
func waitPort(addr string, timeout time.Duration, done <-chan struct{}) error {
	deadline := time.Now().Add(timeout)
	for {
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
