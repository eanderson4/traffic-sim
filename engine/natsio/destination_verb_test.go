package natsio

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// destination_verb_test.go — ADR-0021 on the wire: a spawn verb carrying a
// route destination and an interior injection offset must round-trip
// through the record plane and replay bit-identically. This is the
// contract-sacred property — the fields are additive, so the ONLY way they
// can break an existing recording is by moving the replay CRC.

func sendRouted(nc *nats.Conn, run, ctl, id, origin, vtype, dest string, offset float64) VerbReply {
	req, _ := json.Marshal(VerbRequest{
		Verb: "spawn", RequestID: id, Origin: origin, VType: vtype,
		Destination: dest, OffsetM: offset,
	})
	msg, err := nc.Request(SubjectCtlVerb(run, ctl), req, 2*time.Second)
	if err != nil {
		return VerbReply{Reason: "no reply: " + err.Error()}
	}
	var rep VerbReply
	if err := json.Unmarshal(msg.Data, &rep); err != nil {
		return VerbReply{Reason: "bad reply: " + err.Error()}
	}
	return rep
}

// TestRoutedVerbRecordReplay: destination + interior-offset verbs are
// recorded, drive arrivals, and replay to the identical CRC.
func TestRoutedVerbRecordReplay(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, err := engine.DefaultSpec("lanedrop", 300, 11)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.Types = []*engine.VehicleType{&engine.Car, &engine.Truck}
	// Live runs are holdlast by contract (ADR-0008 §6 — the idm harness
	// exists only where no bus is attached). With no drive controller the
	// injected vehicles coast at their entry speed, which is enough to
	// carry them to a destination; the pause gate never arms because no
	// controller ever claims.
	run := "dr1"

	replies := make(chan VerbReply, 8)
	dirConn := srv.Connect(t)
	go func() {
		ctl := directorClient(t, dirConn, run, []string{"director"}, 0)
		plan := map[uint64]bool{}
		_, err := dirConn.Subscribe(SubjectStateSnap(run), func(m *nats.Msg) {
			f, err := ParseFrame(m.Data)
			if err != nil || plan[f.Tick] {
				return
			}
			var rep VerbReply
			switch f.Tick {
			case 5: // portal origin, INTERIOR destination → arrival
				rep = sendRouted(dirConn, run, ctl, "d1", "A0", "car", "A0", 0)
			case 15: // INTERIOR origin (offset opt-in), exit destination
				rep = sendRouted(dirConn, run, ctl, "d2", "B0", "truck", "B1", 220)
			case 25: // interior origin, no destination
				rep = sendRouted(dirConn, run, ctl, "d3", "B1", "car", "", 300)
			case 35: // rejected: offset past the lane end
				rep = sendRouted(dirConn, run, ctl, "d4", "B0", "car", "", 99999)
			case 45: // rejected: unknown destination lane
				rep = sendRouted(dirConn, run, ctl, "d5", "A1", "car", "Z9", 0)
			default:
				return
			}
			plan[f.Tick] = true
			replies <- rep
		})
		if err != nil {
			t.Errorf("director subscribe: %v", err)
		}
	}()

	lr, err := RunLive(nc, js, run, spec, RecorderConfig{KeyframeEvery: 50, CRCEvery: 1},
		ContractConfig{PaceFloor: time.Millisecond})
	if err != nil {
		t.Fatalf("RunLive: %v", err)
	}
	e := lr.Engine
	close(replies)

	var accepted, rejected int
	for r := range replies {
		if r.Accepted {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted != 3 || rejected != 2 {
		t.Fatalf("verb outcomes: accepted=%d rejected=%d (want 3/2)", accepted, rejected)
	}
	if len(e.SpawnLog) != 3 || lr.Recorder.VerbsWritten != 3 {
		t.Fatalf("spawn log %d, verbs written %d — want 3/3", len(e.SpawnLog), lr.Recorder.VerbsWritten)
	}
	// The recorded directives must carry the ADR-0021 fields — a verb whose
	// destination is dropped on the record plane replays as a DIFFERENT run.
	var sawDest, sawOffset bool
	for _, s := range e.SpawnLog {
		if s.Destination != "" {
			sawDest = true
		}
		if s.OffsetM > 0 {
			sawOffset = true
		}
	}
	if !sawDest || !sawOffset {
		t.Fatalf("spawn log lost the ADR-0021 fields: dest=%v offset=%v", sawDest, sawOffset)
	}
	if e.Stats.Arrived == 0 {
		t.Error("no arrival recorded — the interior destination never ended a trip")
	}

	// --- The acceptance test: replay reproduces the run bit-identically.
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
		if rep.VerbsReplayed > 0 {
			sawVerbs = true
		}
	}
	if !sawVerbs {
		t.Fatal("no replay target re-enqueued a verb — the routed-verb path is untested")
	}

	rec, err := MaterializeRunRecord(js, meta)
	if err != nil {
		t.Fatalf("MaterializeRunRecord: %v", err)
	}
	if _, err := engine.Replay(rec.Log); err != nil {
		t.Fatalf("in-memory replay of the record: %v", err)
	}
}
