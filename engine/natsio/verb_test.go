package natsio

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// verb_test.go — the M10 acceptance test: director spawn verbs flow through
// the live contract plane and the JetStream record like driver intents, and
// replay from the stream reproduces the run bit-identically WITHOUT
// re-running the demand sampler (scenario-format §3, ADR-0006 M10
// addendum). Also pinned: verb validation rejections, the director-grant
// requirement, and request-id dedup (a retried verb never double-spawns).

// directorClient attaches with the given grants (retrying the hello until
// the run's contract plane is up) and returns the assigned controller id.
func directorClient(t *testing.T, nc *nats.Conn, run string, grants []string, capacity int) string {
	t.Helper()
	req, _ := json.Marshal(HelloRequest{
		ContractVersion: SchemaVersion, ControllerType: "director",
		CadenceTicks: 1, Grants: grants, ClaimCapacity: capacity,
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		msg, err := nc.Request(SubjectCtlHello(run), req, 300*time.Millisecond)
		if err != nil {
			if time.Now().After(deadline) {
				t.Fatalf("hello never answered: %v", err)
			}
			continue
		}
		var rep HelloReply
		if err := json.Unmarshal(msg.Data, &rep); err != nil || !rep.Accepted {
			t.Fatalf("hello rejected: %v %s", err, msg.Data)
		}
		return rep.ControllerID
	}
}

// verbResult is one verb attempt's outcome, collected by the director
// goroutine for post-run assertions.
type verbResult struct {
	id       string
	accepted bool
	dup      bool
	reason   string
}

// sendVerb issues one spawn verb request/reply.
func sendVerb(nc *nats.Conn, run, ctl, id, verb, origin, vtype string, earliest uint64) verbResult {
	req, _ := json.Marshal(VerbRequest{
		Verb: verb, RequestID: id, Origin: origin, VType: vtype, EarliestTick: earliest,
	})
	res := verbResult{id: id}
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

func TestDirectorVerbRecordReplay(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 300, 7)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.Types = []*engine.VehicleType{&engine.Car, &engine.Truck}
	run := "dv1"

	// The director drives its verb plan off snapshot ticks. Paced at 1 ms
	// per tick, the 300-tick run leaves ~300 ms of wall time — ample for
	// in-process request/reply.
	results := make(chan verbResult, 16)
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
			case 5:
				r = sendVerb(dirConn, run, ctl, "r1", "spawn", "A0", "car", 0)
			case 15:
				r = sendVerb(dirConn, run, ctl, "r2", "spawn", "A1", "truck", 0)
			case 25: // retry of r1: dedup must answer, not re-spawn
				r = sendVerb(dirConn, run, ctl, "r1", "spawn", "A0", "car", 0)
			case 35:
				r = sendVerb(dirConn, run, ctl, "r3", "spawn", "Z9", "car", 0)
			case 45:
				r = sendVerb(dirConn, run, ctl, "r4", "spawn", "B0", "car", 0)
			case 55:
				r = sendVerb(dirConn, run, ctl, "r5", "spawn", "A0", "bus", 0)
			case 65:
				r = sendVerb(dirConn, run, ctl, "r6", "spawn", "A1", "truck", 200)
			case 75:
				r = sendVerb(dirConn, run, ctl, "r7", "despawn", "A0", "car", 0)
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

	// A signal-grant controller whose verb must be rejected (grant check).
	// NOT the drive grant: an attached drive controller arms the ADR-0008
	// §6 pause gate (claim capacity < demand), and this client never claims
	// anything — the gate would freeze the run. The signal grant exercises
	// the same "not a director" rejection without arming the gate.
	drvResults := make(chan verbResult, 1)
	drvConn := srv.Connect(t)
	go func() {
		ctl := directorClient(t, drvConn, run, []string{"signal"}, 0)
		drvResults <- sendVerb(drvConn, run, ctl, "sig1", "spawn", "A0", "car", 0)
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
	if accepted != 3 || dups != 1 || rejected != 4 {
		t.Fatalf("verb outcomes: accepted=%d dups=%d rejected=%d (want 3/1/4)", accepted, dups, rejected)
	}
	for id, want := range map[string]string{
		"r3": "unknown origin lane", "r4": "not a spawn origin",
		"r5": "unknown vehicle type", "r7": "unsupported verb",
	} {
		if !strings.Contains(reasons[id], want) {
			t.Errorf("%s: reason %q, want %q", id, reasons[id], want)
		}
	}
	if r := <-drvResults; r.accepted || !strings.Contains(r.reason, "director grant") {
		t.Fatalf("signal-grant controller verb: %+v, want grant rejection", r)
	}

	// --- Engine/record state: exactly the three first-seen accepted verbs.
	if len(e.SpawnLog) != 3 || lr.Recorder.VerbsWritten != 3 {
		t.Fatalf("spawn log %d, verbs written %d — want 3/3", len(e.SpawnLog), lr.Recorder.VerbsWritten)
	}
	trucks := 0
	for _, v := range e.Vehicles() {
		if v.TypeIdx == 1 {
			trucks++
		}
	}
	if trucks == 0 {
		t.Fatal("no director-spawned truck live at run end — verbs had no effect")
	}

	// --- THE acceptance test: replay from the JetStream record reproduces
	// the run bit-identically (sampler never re-runs). Seek targets land
	// BETWEEN keyframes (cadence 50) so re-simulation — including verb
	// re-enqueue from ts.{run}.log.verb — is actually exercised.
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
		if rep.VerbsReplayed > 0 {
			sawVerbs = true
		}
		t.Logf("target %d: keyframe@%d, %d intents, %d verbs, %d CRCs verified, final %016x",
			target, rep.KeyframeTick, rep.IntentsReplayed, rep.VerbsReplayed, rep.CRCsVerified, rep.FinalCRC)
	}
	if !sawVerbs {
		t.Fatal("no replay target re-enqueued a verb — the verb path is untested")
	}

	// The materialized record feeds the in-memory replay oracle identically.
	rec, err := MaterializeRunRecord(js, meta)
	if err != nil {
		t.Fatalf("MaterializeRunRecord: %v", err)
	}
	if len(rec.Log.Spawns) != 3 {
		t.Fatalf("materialized record holds %d directives, want 3", len(rec.Log.Spawns))
	}
	if _, err := engine.Replay(rec.Log); err != nil {
		t.Fatalf("in-memory replay of the record: %v", err)
	}
}
