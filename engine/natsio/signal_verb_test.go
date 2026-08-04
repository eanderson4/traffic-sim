package natsio

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// signal_verb_test.go — the ADR-0037 milestone-1 acceptance test: the
// signal_set verb rides the same channel as spawn (ts.{run}.ctl.verb.{id},
// request/reply, director grant, request-id idempotency), is logged on
// ts.{run}.log.verb with its applied_tick, and replay from the JetStream
// record reproduces the run bit-identically — including a seek that lands
// on a keyframe taken WHILE a phase is held (TSKF v7 restores the
// override). The network is the signal-4way fixture crop; the verb holds
// its central junction's program on a phase that is red for the
// instrumented approach.

// sig4Spec is a short live-run spec over the signal-4way crop.
func sig4Spec(ticks uint64) engine.RunSpec {
	return engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: "../testdata/signal-4way/network.json"},
		Scen:   engine.Scenario{SpawnRatePerLaneHour: 600},
		Params: engine.DefaultParams(),
		Seed:   7,
		Ticks:  ticks,
	}
}

// sendVerbRaw issues one verb request/reply with an explicitly given
// request — for the cases the typed helpers cannot express (an omitted
// phase, an over-long request id).
func sendVerbRaw(nc *nats.Conn, run, ctl string, vreq VerbRequest) verbResult {
	req, _ := json.Marshal(vreq)
	res := verbResult{id: vreq.RequestID}
	msg, err := nc.Request(SubjectCtlVerb(run, ctl), req, 2*time.Second)
	if err != nil {
		res.reason = "no reply: " + err.Error()
		return res
	}
	var rep VerbReply
	if err := json.Unmarshal(msg.Data, &rep); err != nil {
		res.reason = "bad reply: " + err.Error()
		return res
	}
	res.accepted, res.dup, res.reason = rep.Accepted, rep.Duplicate, rep.Reason
	return res
}

// sendSignalVerb issues one signal_set verb request/reply.
func sendSignalVerb(nc *nats.Conn, run, ctl, id, signal string, phase int, hold uint64) verbResult {
	return sendVerbRaw(nc, run, ctl, VerbRequest{
		Verb: "signal_set", RequestID: id, Signal: signal, Phase: &phase, HoldTicks: hold,
	})
}

// TestLoggedVerbKinds pins the record-plane verb codec: both known kinds
// round-trip through the logged form, an UNKNOWN kind discriminator (a
// recording written by a newer engine carrying a future verb kind) is a
// hard decode error on every reader — never a silent decode as spawn,
// which would re-enqueue the wrong command and call it replay — and a
// signal_set record's signal_idx/phase are always encoded and
// presence-checked on decode, so a phase-0 record is distinguishable from
// a malformed phase-less one (the control path's round-4 discipline,
// round-5 on the record plane).
func TestLoggedVerbKinds(t *testing.T) {
	// Spawn round-trip: no discriminator — the pre-ADR-0037 byte shape.
	spawn := engine.TickedSpawn{
		Tick: 7, TypeIdx: 1,
		SpawnDirective: engine.SpawnDirective{
			RequestID: "r1", Origin: "A0", TypeName: "car", EarliestTick: 3,
		},
	}
	data, err := encodeLoggedVerb(spawn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "verb") {
		t.Fatalf("spawn record carries a discriminator: %s", data)
	}
	gotSpawn, _, isSignal, err := decodeLoggedVerbAny(data)
	if err != nil || isSignal {
		t.Fatalf("spawn decode: isSignal=%v err=%v", isSignal, err)
	}
	if gotSpawn != spawn {
		t.Errorf("spawn round-trip = %+v, want %+v", gotSpawn, spawn)
	}

	// signal_set round-trip — with signal_idx 0 AND phase 0, the values
	// omitempty would have eaten: both fields must be explicitly present
	// in the JSON and survive the decode.
	sig := engine.TickedSignal{
		Tick: 9, SignalIdx: 0,
		SignalDirective: engine.SignalDirective{
			RequestID: "s1", Signal: "42430333", Phase: 0, HoldTicks: 150,
		},
	}
	data, err = encodeLoggedSignalVerb(sig)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"verb":"signal_set"`, `"signal_idx":0`, `"phase":0`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("signal record missing %s: %s", field, data)
		}
	}
	_, gotSig, isSignal, err := decodeLoggedVerbAny(data)
	if err != nil || !isSignal {
		t.Fatalf("signal decode: isSignal=%v err=%v", isSignal, err)
	}
	if gotSig != sig {
		t.Errorf("signal round-trip = %+v, want %+v", gotSig, sig)
	}

	// A signal_set record missing phase (or signal_idx) is malformed: hard
	// decode error, same idiom as the unknown-kind rejection. So is a
	// MISSING hold_ticks — the record always carries the effective hold —
	// while an EXPLICIT 0 is legitimate (a renewal declined at the chain
	// bound enforced nothing, round-8) and decodes through.
	for _, bad := range []string{
		`{"tick":5,"request_id":"x","verb":"signal_set","signal":"J","signal_idx":2,"hold_ticks":50}`,
		`{"tick":5,"request_id":"x","verb":"signal_set","signal":"J","phase":2,"hold_ticks":50}`,
	} {
		if _, _, _, err := decodeLoggedVerbAny([]byte(bad)); err == nil ||
			!strings.Contains(err.Error(), "missing signal_idx or phase") {
			t.Errorf("malformed signal record %s: err = %v, want a loud rejection", bad, err)
		}
	}
	if _, _, _, err := decodeLoggedVerbAny([]byte(
		`{"tick":5,"request_id":"x","verb":"signal_set","signal":"J","signal_idx":2,"phase":2}`)); err == nil ||
		!strings.Contains(err.Error(), "missing hold_ticks") {
		t.Errorf("signal record without hold_ticks: err = %v, want a loud rejection", err)
	}
	_, declined, isSignal, err := decodeLoggedVerbAny([]byte(
		`{"tick":5,"request_id":"x","verb":"signal_set","signal":"J","signal_idx":2,"phase":2,"hold_ticks":0}`))
	if err != nil || !isSignal || declined.HoldTicks != 0 {
		t.Errorf("explicit hold_ticks 0 (declined renewal): isSignal=%v hold=%d err=%v, want a clean decode",
			isSignal, declined.HoldTicks, err)
	}

	// Unknown kind: hard error at the decoder AND at a replay reader.
	bad := []byte(`{"tick":5,"request_id":"x","verb":"teleport","origin":"A0","vtype":"car"}`)
	if _, _, _, err := decodeLoggedVerbAny(bad); err == nil ||
		!strings.Contains(err.Error(), "unknown verb kind") {
		t.Fatalf("unknown kind: err = %v, want a loud rejection", err)
	}
	var rec tickRecords
	m := &nats.Msg{Subject: SubjectLogVerb("run"), Data: bad}
	if err := rec.add(m, "run"); err == nil || !strings.Contains(err.Error(), "unknown verb kind") {
		t.Fatalf("reader: err = %v, want the unknown kind to fail loudly", err)
	}
	if len(rec.verbs) != 0 || len(rec.sverbs) != 0 {
		t.Fatalf("reader queued a rejected record: %+v", rec)
	}
}

// TestSignalVerbValidation pins the contract-boundary validation of
// signal_set (round-4 review): the request_id is length-prefixed with a
// u16 in the TSKF codec (director queue v3, signal overrides v7), so an
// over-long id is rejected where it enters — 65,535 bytes exactly is
// accepted and its keyframe round-trips a restore — and an omitted phase
// is rejected rather than silently commanding phase 0 (JSON cannot tell
// omission from 0, so presence is checked).
func TestSignalVerbValidation(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec := sig4Spec(200)
	run := "sv2"
	const prog = "42430333"

	longOK := strings.Repeat("x", engine.MaxRequestIDBytes)
	longBad := strings.Repeat("y", engine.MaxRequestIDBytes+1)

	results := make(chan verbResult, 8)
	dirConn := srv.Connect(t)
	go func() {
		ctl := directorClient(t, dirConn, run, []string{"director"}, 0)
		plan := map[uint64]bool{}
		_, err := dirConn.Subscribe(SubjectStateSnap(run), func(m *nats.Msg) {
			f, err := ParseFrame(m.Data)
			if err != nil || plan[f.Tick] {
				return
			}
			var r verbResult
			switch f.Tick {
			case 5: // boundary value: exactly the codec limit — accepted
				r = sendSignalVerb(dirConn, run, ctl, longOK, prog, 2, 150)
			case 15: // one byte past — rejected at the contract boundary
				r = sendSignalVerb(dirConn, run, ctl, longBad, prog, 2, 100)
			case 25: // omitted phase — must not silently command phase 0
				r = sendVerbRaw(dirConn, run, ctl, VerbRequest{Verb: "signal_set", RequestID: "s-nophase", Signal: prog, HoldTicks: 40})
			case 35: // explicit phase 0 — accepted and applied as phase 0
				r = sendSignalVerb(dirConn, run, ctl, "s-p0", prog, 0, 30)
			case 45: // spawn without phase — unaffected (phase is a signal_set field)
				r = sendVerb(dirConn, run, ctl, "sp1", "spawn", "n167922072_1_0", "car", 0)
			case 55: // same-boundary supersede on a second program: both
				// verbs are published concurrently so they buffer into the
				// SAME control drain and apply at one boundary; the one
				// processed first is dropped before it governs a tick and
				// its record must show hold 0 (accepted, enforced
				// nothing). Arrival order is a race, so the assertions
				// are order-agnostic.
				go func() { results <- sendSignalVerb(dirConn, run, ctl, "p1", "42430329", 1, 20) }()
				go func() { results <- sendSignalVerb(dirConn, run, ctl, "p2", "42430329", 2, 20) }()
				plan[f.Tick] = true
				return
			default:
				return
			}
			plan[f.Tick] = true
			results <- r
		})
		if err != nil {
			t.Errorf("director subscribe: %v", err)
		}
	}()

	lr, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 20, CRCEvery: 1},
		ContractConfig{PaceFloor: 20 * time.Millisecond}) // 20 ms: the p1/p2 pair reliably shares one control drain
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	e := lr.Engine
	close(results)

	reasons := map[string]verbResult{}
	for r := range results {
		reasons[r.id] = r
	}
	if r := reasons[longOK]; !r.accepted {
		t.Errorf("65535-byte request_id: rejected (%s), want accepted (the codec's exact bound)", r.reason)
	}
	if r := reasons[longBad]; r.accepted || !strings.Contains(r.reason, "too long") {
		t.Errorf("65536-byte request_id: %+v, want rejection naming the length", r)
	}
	if r := reasons["s-nophase"]; r.accepted || !strings.Contains(r.reason, "missing phase") {
		t.Errorf("omitted phase: %+v, want rejection (phase 0 must be explicit)", r)
	}
	if r := reasons["s-p0"]; !r.accepted {
		t.Errorf("explicit phase 0: rejected (%s), want accepted", r.reason)
	}
	if r := reasons["sp1"]; !r.accepted {
		t.Errorf("spawn without phase: rejected (%s), want unaffected", r.reason)
	}
	if r := reasons["p1"]; !r.accepted {
		t.Errorf("superseded p1: rejected (%s), want accepted (it enforced nothing, but the command was valid)", r.reason)
	}
	if r := reasons["p2"]; !r.accepted {
		t.Errorf("superseding p2: rejected (%s), want accepted", r.reason)
	}

	// Engine state: four accepted signal verbs — the boundary-length id
	// holding phase 2, the explicit phase 0 superseding it, and the
	// same-boundary pair on the second program.
	if len(e.SigLog) != 4 {
		t.Fatalf("signal log %d, want 4 (%+v)", len(e.SigLog), e.SigLog)
	}
	if e.SigLog[0].RequestID != longOK || e.SigLog[0].Phase != 2 {
		t.Errorf("first directive = %+v, want the 65535-byte id on phase 2", e.SigLog[0])
	}
	if e.SigLog[1].RequestID != "s-p0" || e.SigLog[1].Phase != 0 {
		t.Errorf("second directive = %+v, want s-p0 phase 0 (explicit zero applied)", e.SigLog[1])
	}
	byID := map[string]engine.TickedSignal{}
	for _, d := range e.SigLog {
		byID[d.RequestID] = d
	}
	// The first-processed of p1/p2 was dropped at the same boundary and
	// recorded hold 0; the second installed with its effective 20. Arrival
	// order is a race — assert the set, not the assignment.
	gotHolds := map[uint64]bool{byID["p1"].HoldTicks: true, byID["p2"].HoldTicks: true}
	if !gotHolds[0] || !gotHolds[20] || byID["p1"].HoldTicks == byID["p2"].HoldTicks {
		t.Errorf("same-boundary pair recorded holds p1=%d p2=%d, want {0, 20}", byID["p1"].HoldTicks, byID["p2"].HoldTicks)
	}

	// The keyframe round-trip with the boundary-length id: s1's hold
	// (~[6,156)) is superseded by s-p0 (~[36,66)) and retained for
	// clearance, so the keyframes at 20/40/60 carry the 65,535-byte
	// request id in the v7 override section; a seek onto one must decode
	// it and re-simulate CRC-exact.
	meta, err := lr.Registry.Meta(run)
	if err != nil {
		t.Fatalf("registry meta: %v", err)
	}
	rep, err := ReplayFromStream(js, meta, 45)
	if err != nil {
		t.Fatalf("ReplayFromStream(45) — keyframe carrying the boundary-length id: %v", err)
	}
	if rep.FinalCRC != e.CRCs[44] {
		t.Fatalf("replay crc %016x, live %016x", rep.FinalCRC, e.CRCs[44])
	}
	if rep.KeyframeTick != 40 {
		t.Fatalf("seek landed on keyframe@%d, want 40 (mid-hold, v7 with the long id)", rep.KeyframeTick)
	}

	// The wire-level record shows the same stamping: the materialized
	// log.verb entries for the same-boundary pair carry {0, 20} (arrival
	// order is a race — assert the set).
	rec, err := MaterializeRunRecord(js, meta)
	if err != nil {
		t.Fatalf("MaterializeRunRecord: %v", err)
	}
	recorded := map[string]uint64{}
	for _, d := range rec.Log.Signals {
		recorded[d.RequestID] = d.HoldTicks
	}
	gotRecorded := map[uint64]bool{recorded["p1"]: true, recorded["p2"]: true}
	if !gotRecorded[0] || !gotRecorded[20] || recorded["p1"] == recorded["p2"] {
		t.Errorf("recorded same-boundary pair holds p1=%d p2=%d, want {0, 20}", recorded["p1"], recorded["p2"])
	}
}

func TestSignalVerbRecordReplay(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec := sig4Spec(300)
	run := "sv1"

	// Program "42430333" is the fixture's central junction (39/6/39/6 over
	// 14 links); phase 2 is red for the instrumented approach's link 10.
	const prog = "42430333"

	// The director drives its verb plan off snapshot ticks, exactly like
	// the spawn-verb acceptance test.
	results := make(chan verbResult, 8)
	dirConn := srv.Connect(t)
	go func() {
		ctl := directorClient(t, dirConn, run, []string{"director"}, 0)
		plan := map[uint64]bool{}
		_, err := dirConn.Subscribe(SubjectStateSnap(run), func(m *nats.Msg) {
			f, err := ParseFrame(m.Data)
			if err != nil || plan[f.Tick] {
				return
			}
			var r verbResult
			switch f.Tick {
			case 5: // hold phase 2 for 150 ticks — lapses mid-run, loudly
				r = sendSignalVerb(dirConn, run, ctl, "s1", prog, 2, 150)
			case 15: // retry of s1: dedup must answer, not re-apply
				r = sendSignalVerb(dirConn, run, ctl, "s1", prog, 2, 150)
			case 25: // unknown signal program
				r = sendSignalVerb(dirConn, run, ctl, "s2", "nope", 0, 100)
			case 35: // phase index out of range (the program has 4)
				r = sendSignalVerb(dirConn, run, ctl, "s3", prog, 4, 100)
			default:
				return
			}
			plan[f.Tick] = true
			results <- r
		})
		if err != nil {
			t.Errorf("director subscribe: %v", err)
		}
	}()

	// A SIGNAL-grant controller (not director): the verb plane is the
	// director grant's, so its signal_set must be rejected — the same
	// not-a-director rejection the spawn test pins, and deliberately NOT
	// the drive grant (an attached drive controller arms the pause gate).
	drvResults := make(chan verbResult, 1)
	drvConn := srv.Connect(t)
	go func() {
		ctl := directorClient(t, drvConn, run, []string{"signal"}, 0)
		drvResults <- sendSignalVerb(drvConn, run, ctl, "sig1", prog, 0, 100)
	}()

	lr, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1},
		ContractConfig{PaceFloor: time.Millisecond})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	e := lr.Engine
	close(results)
	close(drvResults)

	// --- Wire-level assertions: accept/reject/dedup/grant.
	var accepted, rejected, dups int
	reasons := map[string]string{}
	for r := range results {
		switch {
		case r.dup:
			dups++
			if !r.accepted {
				t.Errorf("duplicate of an accepted verb not idempotent: %+v", r)
			}
		case r.accepted:
			accepted++
		default:
			rejected++
			reasons[r.id] = r.reason
		}
	}
	if accepted != 1 || dups != 1 || rejected != 2 {
		t.Fatalf("verb outcomes: accepted=%d dups=%d rejected=%d (want 1/1/2)", accepted, dups, rejected)
	}
	for id, want := range map[string]string{
		"s2": "unknown signal program", "s3": "out of range",
	} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("%s: reason %q, want %q", id, reasons[id], want)
		}
	}
	if r := <-drvResults; r.accepted || !strings.Contains(r.reason, "director grant") {
		t.Fatalf("signal-grant controller verb: %+v, want grant rejection", r)
	}

	// --- Engine/record state: exactly the one first-seen accepted verb.
	if len(e.SigLog) != 1 || lr.Recorder.VerbsWritten != 1 {
		t.Fatalf("signal log %d, verbs written %d — want 1/1", len(e.SigLog), lr.Recorder.VerbsWritten)
	}
	if got := e.SigLog[0]; got.Signal != prog || got.Phase != 2 || got.HoldTicks != 150 {
		t.Fatalf("logged directive = %+v, want %s phase 2 hold 150", got, prog)
	}

	// --- THE acceptance test: replay from the JetStream record reproduces
	// the run bit-identically. The hold spans roughly ticks 6–156, so the
	// keyframes at 50/100/150 are mid-hold (TSKF v7): target 175 seeks one
	// of them and re-simulates THROUGH the lapse, and target 49 re-enqueues
	// the verb itself from ts.{run}.log.verb.
	meta, err := lr.Registry.Meta(run)
	if err != nil {
		t.Fatalf("registry meta: %v", err)
	}
	sawVerbs := false
	for _, target := range []uint64{49, 175, 299, 300} {
		rep, err := ReplayFromStream(js, meta, target)
		if err != nil {
			t.Fatalf("ReplayFromStream(%d): %v", target, err)
		}
		if rep.FinalCRC != e.CRCs[target-1] {
			t.Fatalf("target %d: replay crc %016x, live %016x", target, rep.FinalCRC, e.CRCs[target-1])
		}
		if target != 300 && rep.CRCsVerified == 0 {
			t.Fatalf("target %d: no CRCs verified — replay landed on a keyframe and proved nothing", target)
		}
		if rep.SigVerbsReplayed > 0 {
			sawVerbs = true
		}
		t.Logf("target %d: keyframe@%d, %d intents, %d spawn verbs, %d signal verbs, %d CRCs verified, final %016x",
			target, rep.KeyframeTick, rep.IntentsReplayed, rep.VerbsReplayed, rep.SigVerbsReplayed, rep.CRCsVerified, rep.FinalCRC)
	}
	if !sawVerbs {
		t.Fatal("no replay target re-enqueued a signal verb — the verb path is untested")
	}

	// The materialized record feeds the in-memory replay oracle identically.
	rec, err := MaterializeRunRecord(js, meta)
	if err != nil {
		t.Fatalf("MaterializeRunRecord: %v", err)
	}
	if len(rec.Log.Signals) != 1 {
		t.Fatalf("materialized record holds %d signal directives, want 1", len(rec.Log.Signals))
	}
	if _, err := engine.Replay(rec.Log); err != nil {
		t.Fatalf("in-memory replay of the record: %v", err)
	}

	// The starvation rail is ON THE RECORD (ADR-0037: a lapse is a logged
	// event, never a silent fallback): exactly one signal_lapse event, at
	// the bound tick, carrying the hold's identity. The absolute ticks are
	// taken from the recorded directive (the verb's applied tick is
	// request/reply timing, not pinned); the SPAN is the contract.
	var lapses []ContractEvent
	for _, evt := range rec.Events {
		if evt.Type == EventSignalLapse {
			lapses = append(lapses, evt)
		}
	}
	if len(lapses) != 1 {
		t.Fatalf("record holds %d signal_lapse events, want exactly 1", len(lapses))
	}
	lp := lapses[0]
	applied := e.SigLog[0]
	if lp.Signal != prog || lp.Phase == nil || *lp.Phase != 2 || lp.RequestID != "s1" {
		t.Errorf("lapse event identity = %+v, want %s phase 2 verb s1", lp, prog)
	}
	if lp.Since != applied.Tick || lp.Until != applied.Tick+150 || lp.Tick != lp.Until {
		t.Errorf("lapse event span = [%d, %d) fired at %d, want [%d, %d) at %d",
			lp.Since, lp.Until, lp.Tick, applied.Tick, applied.Tick+150, applied.Tick+150)
	}
}
