package main

// supervisor.go — single-active-run lifecycle for `serve` engine children:
// spawn, wait for the WebSocket port to accept TCP (the menu's "you may
// navigate" signal — the viz opens its socket immediately on load), and
// SIGTERM→SIGKILL(2s) on replace/stop. One mutex serializes start/stop; the
// injectable spawnFunc keeps the tests on `sleep`, never the real engine.

import (
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
	// readyTimeout bounds the wait for the engine's listener. The binary is
	// prebuilt at demosrv startup, so this covers process exec + embedded
	// broker startup, not a compile.
	readyTimeout = 10 * time.Second
	// killGrace is the SIGTERM grace before SIGKILL. serve traps SIGTERM and
	// abandons the run (demo mode does no graceful finish — see
	// engine/cmd/serve), so a healthy child exits well within this.
	killGrace = 2 * time.Second
)

// spawnFunc starts the demo's engine process and returns the running
// command (cmd.Start already called).
type spawnFunc func(d *Demo) (*exec.Cmd, error)

type activeRun struct {
	demo      *Demo
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
	readyTimeout time.Duration
	killGrace    time.Duration
}

func newSupervisor(spawn spawnFunc) *supervisor {
	return &supervisor{
		spawn:        spawn,
		wsAddr:       wsListenAddr,
		readyTimeout: readyTimeout,
		killGrace:    killGrace,
	}
}

// start replaces any active run with d's engine and blocks until its
// WebSocket port accepts TCP. A spawn or readiness failure leaves NO
// process behind (the just-started child is killed on the way out).
func (s *supervisor) start(d *Demo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	cmd, err := s.spawn(d)
	if err != nil {
		return fmt.Errorf("spawn %s: %w", d.ID, err)
	}
	a := &activeRun{demo: d, cmd: cmd, startedAt: time.Now(), done: make(chan struct{})}
	go func() {
		// Reap. The exit error is expected (SIGTERM/SIGKILL is the normal
		// way out) and the child's own log carries anything interesting.
		cmd.Wait()
		close(a.done)
	}()
	s.active = a
	if err := s.waitReady(a); err != nil {
		s.stopLocked()
		return fmt.Errorf("%s did not become ready: %w", d.ID, err)
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
	return a.demo.ID, a.cmd.Process.Pid, a.startedAt, true
}

func (s *supervisor) waitReady(a *activeRun) error {
	if s.ready != nil {
		return s.ready()
	}
	return waitPort(s.wsAddr, s.readyTimeout, a.done)
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
