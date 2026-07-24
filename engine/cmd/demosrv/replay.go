package main

// replay.go — VCR replay wiring: start a recorded run under the supervisor
// (engine/cmd/replay child on the fixed ws + control ports) and proxy the
// child's loopback control plane so the viz panel talks ONLY to demosrv.
// The proxy is deliberately dumb: request bodies go in, status codes and
// the player's status JSON come back out, no caching, no reshaping. The
// timeouts are short because the child is loopback — except seek, whose
// re-simulation can take ~1 s+ on big records.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"traffic-sim/engine/scenario"
)

const (
	// ctlTimeout bounds status/pause/resume/speed proxy calls.
	ctlTimeout = 2 * time.Second
	// seekTimeout bounds seek proxy calls (synchronous re-simulation from
	// the last keyframe — the child answers 200 only after landing).
	seekTimeout = 10 * time.Second
)

// scenarioDisplay resolves the menu-side display facts for a recording's
// scenario: dt for the deep link (exactly as serve's hint prints it,
// scenario.Load → RunSpec → spec.Params.Dt, cmd/serve/main.go's ?dt=%g)
// and the ADR-0012 content hash for the post-start recording check. The
// replay child's /status also carries the AUTHORITATIVE dt and hash from
// the record plane's RunMeta; these are the menu-side values so the viz
// renders at the right pace from the first frame.
func scenarioDisplay(scenarioDir string) (dt float64, hash string, err error) {
	sc, err := scenario.Load(scenarioDir)
	if err != nil {
		return 0, "", err
	}
	spec, err := sc.RunSpec(typeReg)
	if err != nil {
		return 0, "", err
	}
	return spec.Params.Dt, sc.Hash(), nil
}

// errRecordingMismatch marks the scenario-edited-after-recording failure
// (the handler maps it to 409; other checkRecordingHash errors are 502).
var errRecordingMismatch = errors.New("recorded spec hash does not match the scenario")

// errRecordingUnverifiable marks a recording whose status carries NO
// scenario hash (flag-built recording): the registry always supplies a
// scenario for display, and with nothing to bind it against the menu could
// show an unrelated network — fail closed (also 409).
var errRecordingUnverifiable = errors.New("recording has no scenario hash, display binding unverifiable")

// checkRecordingHash binds the display to the recording: it reads the
// replay child's /status and requires the recorded spec hash (ADR-0012) to
// MATCH the scenario's current content hash — a mismatch means the scenario
// was edited after recording, so the registry's scenarioDir no longer
// shows what was recorded. An empty recorded hash (flag-built recording)
// fails CLOSED: the display network cannot be verified against the
// recording. (An empty scenHash would skip with a log note, but the
// registry always loads a scenario, so that branch is defensive only.)
func checkRecordingHash(ctlBase, scenHash string) error {
	ctx, cancel := context.WithTimeout(context.Background(), ctlTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ctlBase+"/status", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("replay status fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("replay status: %s", resp.Status)
	}
	var st struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return fmt.Errorf("replay status decode: %w", err)
	}
	if scenHash == "" {
		log.Printf("demosrv: scenario has no content hash — skipping the recording binding check (defensive: the registry always loads a scenario)")
		return nil
	}
	if st.Hash == "" {
		return fmt.Errorf("%w (recorded run carries no ADR-0012 hash — a flag-built recording cannot be bound to scenarioDir)", errRecordingUnverifiable)
	}
	if st.Hash != scenHash {
		return fmt.Errorf("%w: the scenario was edited after recording (recorded %.12s…, scenario %.12s…)", errRecordingMismatch, st.Hash, scenHash)
	}
	return nil
}

// handleReplayStart swaps the active child for a replay of the recording
// and returns the same deep-link shape as demo start, with run={run}-replay
// (the replay's live plane) and dt from the recording's scenario. The
// display scenario is verified against the recording (hash check) as the
// start's post-ready hook — UNDER the supervisor's start serialization, so
// a concurrent start cannot swap the child between spawn and check.
func (s *server) handleReplayStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.reg.recByID(id)
	if rec == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown recording %q", id))
		return
	}
	dt, scenHash, err := scenarioDisplay(rec.ScenarioDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	verify := func() error { return checkRecordingHash(s.replayCtl, scenHash) }
	if err := s.sup.start(spawnTarget{Kind: "replay", Rec: rec}, verify); err != nil {
		// A verification failure has already killed the child (inside
		// start): 409 for a binding problem with the recording itself,
		// 502 for spawn/readiness/transport failures.
		code := http.StatusBadGateway
		if errors.Is(err, errRecordingMismatch) || errors.Is(err, errRecordingUnverifiable) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"url": fmt.Sprintf("/app/?run=%s-replay&net=/net/%s.geojson&dt=%g", rec.Run, rec.ID, dt),
	})
}

// handleReplayStatus serves the active replay's player status. The ?run=
// param is OPTIONAL: absent (a panel's first probe, before it knows the
// run) the status is served as-is; present, it must name the ACTIVE replay
// ({run}-replay) or the answer is demosrv's own 409 — so a stale tab
// notices the swap instead of silently adopting the replacement replay's
// identity. (Panels bind every poll after their first adoption.)
func (s *server) handleReplayStatus(w http.ResponseWriter, r *http.Request) {
	got := r.URL.Query().Get("run")
	mismatch := ""
	err := s.sup.withActiveReplay(func(active string) error {
		if got != "" && got != active {
			mismatch = active
			return errCtlRunMismatch
		}
		s.forwardCtl(w, r, "/status", s.ctlTimeout)
		return nil
	})
	switch {
	case err == nil:
		// forwarded
	case errors.Is(err, errNoActiveReplay):
		writeErr(w, http.StatusNotFound, err)
	case errors.Is(err, errCtlRunMismatch):
		writeErr(w, http.StatusConflict, fmt.Errorf("status run %q does not match the active replay %q (stale panel? reload from the menu)", got, mismatch))
	}
}

// ctlRoute proxies one control verb to the replay child. seek gets the
// longer timeout (synchronous re-simulation); the rest the short one.
//
// RUN BINDING: the request must name the ACTIVE replay in ?run=
// (/api/replay/ctl/pause?run=signal4-replay). The control port is reused
// across recordings, so without the binding a stale tab (or a second one)
// could pause whatever recording happens to be active — missing or
// mismatched run is a 409, never proxied. The check and the forward happen
// atomically under the supervisor lock (see withActiveReplay), so a slow
// seek forward (~10 s) blocks a concurrent start for that long.
func (s *server) ctlRoute(path string, isSeek bool) http.HandlerFunc {
	timeout := s.ctlTimeout
	if isSeek {
		timeout = s.seekTimeout
	}
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.URL.Query().Get("run")
		mismatch := ""
		// Check AND forward under the supervisor lock (withActiveReplay):
		// releasing it between the binding check and the forward would let
		// a concurrent replacement receive a command authorized for the
		// previous replay (the control port is fixed).
		err := s.sup.withActiveReplay(func(active string) error {
			if got != active {
				mismatch = active
				return errCtlRunMismatch
			}
			s.forwardCtl(w, r, path, timeout)
			return nil
		})
		switch {
		case err == nil:
			// forwarded (or nothing to do)
		case errors.Is(err, errNoActiveReplay):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, errCtlRunMismatch):
			writeErr(w, http.StatusConflict, fmt.Errorf("ctl run %q does not match the active replay %q (stale panel? reload from the menu)", got, mismatch))
		}
	}
}

// errCtlRunMismatch signals withActiveReplay that the request named a
// different replay than the active one (demosrv's own 409).
var errCtlRunMismatch = errors.New("ctl run mismatch")

// forwardCtl forwards one request to the replay child's control plane and
// copies the response back verbatim: status code, content type, body (the
// player's status JSON, or its plain-text error). A child that cannot be
// reached in time (or at all) is a 502 — demosrv's own error, not the
// child's, so it does not masquerade as a player response.
func (s *server) forwardCtl(w http.ResponseWriter, r *http.Request, path string, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, s.replayCtl+path, r.Body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	// No client.Timeout: the request context carries the deadline (per-call
	// — seek gets longer than the rest).
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	// The cap guards demosrv's memory, not the protocol: player responses
	// are small status documents, so hitting it means something is wrong —
	// say so, because the truncated body downstream is malformed JSON.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if len(body) == 1<<20 {
		log.Printf("demosrv: replay ctl %s response hit the 1 MiB proxy cap — body truncated, downstream JSON will be malformed", path)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
