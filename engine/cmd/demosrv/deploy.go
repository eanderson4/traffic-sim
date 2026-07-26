package main

// deploy.go — the public-deployment machinery behind three of the ADR-0020
// flags (-wspublic itself is a two-line override next to wsListenAddr):
//   -admintoken bearer-gates the MUTATING routes (adminGate) — GETs stay
//     public, the menu/status/network fetches are the audience's;
//   -autostart starts one demo once the listener is up, with retries — a
//     pod whose demo cannot start KEEPS SERVING (debuggable beats
//     crash-looping);
//   -nobuild skips the go-build pre-warm for images that ship serve+replay
//     prebuilt, stat-checked LOUD at startup.
// All three default to the local-dev behavior (open, manual start, build).

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// adminTokenEnv is the token's env intake (resolveAdminToken): the flag
// value is world-readable in argv (/proc/*/cmdline, `kubectl describe`),
// so a pod carries the token in a Secret-backed env var instead.
const adminTokenEnv = "DEMOSRV_ADMIN_TOKEN"

// resolveAdminToken is the -admintoken intake: the flag wins (whitespace
// trimmed — bearer tokens never contain it); an UNSET flag falls back to
// the environment (Fable review — K8s Secret → env keeps the token out of
// the pod spec's args). UNSET everywhere = open (local dev). Presence-
// aware on both sources (flagSet via flag.Visit — flag.String cannot tell
// `-admintoken=` from an absent flag — and os.LookupEnv, which cannot
// tell set-but-empty from unset): an explicitly SUPPLIED token that trims
// to empty means the operator intended a gate and misconfigured it (the
// manifest's `-admintoken="$TOK"` with $TOK expanding empty) — fail
// CLOSED (startup refusal), never silently serve the mutating routes
// unauthenticated on a public pod (Fable + sol, rounds 7-9).
func resolveAdminToken(flagVal string, flagSet bool) (string, error) {
	if flagSet {
		if tok := strings.TrimSpace(flagVal); tok != "" {
			return tok, nil
		}
		return "", fmt.Errorf("-admintoken was supplied empty — refusing to start with the admin gate OPEN (fix or drop the flag; an UNSET flag falls back to %s)", adminTokenEnv)
	}
	raw, set := os.LookupEnv(adminTokenEnv)
	if !set {
		return "", nil
	}
	if tok := strings.TrimSpace(raw); tok != "" {
		return tok, nil
	}
	return "", fmt.Errorf("%s is set but empty — refusing to start with the admin gate OPEN (fix the Secret or unset the variable)", adminTokenEnv)
}

// stripEnv returns env without the named variable: the admin token is
// demosrv's OWN credential and must not ride into engine children — the
// processes that expose the unauthenticated ws plane, where their
// environment is one /proc read away (Fable, round 7).
func stripEnv(env []string, name string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// validateWsPublic checks a -wspublic value. VERBATIM advertisement is
// the design (the operator owns the public URL), but a value that is not
// an ABSOLUTE, fragment-free ws:// or wss:// URL is a typo, not a choice
// — fatal at startup, or the pod serves a healthy-looking menu whose map
// never connects. Browsers reject fragment-bearing WebSocket URLs — and
// a trailing "#" parses to an EMPTY fragment, so the raw string is
// checked, not u.Fragment (sol, rounds 7-10). Hostname, not Host:
// `wss://:443` has a Host but no dialable name; userinfo
// (`wss://user:pass@host`) would be advertised verbatim to every client;
// an out-of-range numeric port parses fine but is undialable (sol,
// round 13).
func validateWsPublic(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" || strings.Contains(raw, "#") || u.User != nil {
		return fmt.Errorf("not an absolute ws:// or wss:// URL")
	}
	// url.Parse rejects a NON-numeric port; an out-of-range numeric one
	// parses fine and yields an undialable URL. Leading zeros pass Atoi
	// but are a typo, not a choice (`wss://host:007` — Fable, round 28).
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 || (len(p) > 1 && p[0] == '0') {
			return fmt.Errorf("invalid port %q", p)
		}
	}
	return nil
}

// adminGate wraps one MUTATING handler with the -admintoken bearer check.
// Empty token = open (local dev — the flag's default). The compare is
// constant-time in the token CONTENT (a length mismatch still returns
// early — acceptable for a high-entropy operator token), and the 401
// carries no hint about the presented credential (no token echo — the
// body is the same "unauthorized" for a missing, malformed, or wrong
// header); WWW-Authenticate names the scheme so API clients know what to
// send.
func (s *server) adminGate(h http.HandlerFunc) http.HandlerFunc {
	if s.adminToken == "" {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		// The scheme is case-insensitive (RFC 7235) — a proxy or
		// hand-rolled client may send "bearer"; the CREDENTIAL compare
		// stays exact and constant-time in content.
		scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || subtle.ConstantTimeCompare([]byte(tok), []byte(s.adminToken)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeErr(w, http.StatusUnauthorized, errors.New("unauthorized"))
			return
		}
		h(w, r)
	}
}

// autostartAttempts bounds -autostart's retry loop. The retries exist for
// TRANSIENT failures (a slow port release right after a pod reschedule);
// a demo that cannot start at all must exhaust them quickly and leave
// demosrv serving, not loop forever.
const autostartAttempts = 4

// autostartBackoff is the first retry delay, doubling per attempt. A var so
// tests do not wait out the real backoff.
var autostartBackoff = 2 * time.Second

// autostartDemo is -autostart: start the named demo directly through the
// supervisor — NOT via the HTTP route, so the internal start bypasses the
// adminGate bearer check (the pod itself holds no token; the gate is for
// the audience). epoch0 is the supervisor's stop epoch, captured by main
// BEFORE this goroutine was launched: a stop that completed in the
// scheduling gap between listener-bind and this first line would
// otherwise be snapshotted as the baseline and un-done (sol, round 19).
// Unknown id, or a start that fails every attempt, logs and
// RETURNS: demosrv keeps serving the menu either way — a pod with no demo
// running is debuggable, a crash-looping one is not. Shutdown aborts the
// retry loop on TWO levels: s.shutdown wakes the backoff sleep fast, and
// the supervisor's closed latch (shutdownFinal, checked under the same mu
// as the spawn) refuses any start that slips past the channel check — an
// engine child spawned while demosrv exits would be orphaned on the ws
// port (Fable + sol, round 3). A nil shutdown channel (tests) simply
// never fires.
func (s *server) autostartDemo(id string, epoch0 uint64) {
	d := s.reg.byID(id)
	if d == nil {
		log.Printf("demosrv: autostart: unknown demo %q — serving the menu with no active run", id)
		return
	}
	backoff := autostartBackoff
	for attempt := 1; ; attempt++ {
		select {
		case <-s.shutdown:
			log.Printf("demosrv: autostart: shutdown before attempt %d — demo %q not started", attempt, id)
			return
		default:
		}
		if s.sup.stopEpoch.Load() != epoch0 {
			log.Printf("demosrv: autostart: demo %q NOT started — a stop was requested since autostart began, honoring it", id)
			return
		}
		// startIfIdle, NOT a status check + sup.start: the idle check, the
		// epoch check, and the spawn hold the supervisor lock TOGETHER, so
		// an operator's start (or stop) during the backoff window wins
		// atomically and is left alone (check-then-act would kill it —
		// Fable + sol, rounds 2/18). sup.start copies the demo (per-spawn
		// run nonce), so reusing d across attempts is safe.
		run, err := s.sup.startIfIdle(spawnTarget{Kind: "demo", Demo: d}, epoch0)
		if errors.Is(err, errAlreadyActive) {
			log.Printf("demosrv: autostart: demo %q NOT started — %v, leaving it alone", id, err)
			return
		}
		if errors.Is(err, errSupervisorClosed) {
			// Shutdown mid-retry: not a transient failure, do NOT back off
			// and retry into a dying process (the log must stay truthful).
			log.Printf("demosrv: autostart: demo %q not started — demosrv is shutting down", id)
			return
		}
		if errors.Is(err, errStartAborted) {
			// An operator's STOP landed mid-start: deliberate, not
			// transient — retrying would un-stop the demo the operator
			// just stopped (Fable, round 12).
			log.Printf("demosrv: autostart: demo %q start aborted by a stop request — NOT retrying", id)
			return
		}
		if err == nil {
			log.Printf("demosrv: autostart: demo %q is live (run %s)", id, run)
			return
		}
		if attempt >= autostartAttempts {
			log.Printf("demosrv: autostart: demo %q failed after %d attempts: %v — KEEPING THE SERVER UP (start a demo from the menu)", id, attempt, err)
			return
		}
		log.Printf("demosrv: autostart: demo %q attempt %d/%d failed (%v) — retrying in %s", id, attempt, autostartAttempts, err, backoff)
		select {
		case <-s.shutdown:
			log.Printf("demosrv: autostart: shutdown during backoff — demo %q not retried", id)
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// prebuiltBins validates -nobuild's directory: <dir>/serve and <dir>/replay
// must exist and be executable (the container-image layout). Fail LOUD at
// startup — a demosrv that only discovers a missing engine on the first
// start click has already answered the menu's GETs and looks healthy.
// The returned paths are ABSOLUTE: filepath.Join(".", "serve") is bare
// "serve", which exec.Command would resolve through $PATH — late-failing
// or running an unrelated binary (sol review).
func prebuiltBins(dir string) (serveBin, replayBin string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", fmt.Errorf("-nobuild: %w", err)
	}
	serveBin = filepath.Join(abs, "serve")
	replayBin = filepath.Join(abs, "replay")
	for _, p := range []string{serveBin, replayBin} {
		fi, err := os.Stat(p)
		if err != nil {
			return "", "", fmt.Errorf("-nobuild: %w", err)
		}
		// Regular executable file ONLY: a directory, FIFO, socket, or
		// device would pass a bare mode&0111 check and fail at first
		// spawn — the exact late failure this stat check exists to
		// prevent (sol review).
		if !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
			return "", "", fmt.Errorf("-nobuild: %s is not an executable file", p)
		}
	}
	return serveBin, replayBin, nil
}
