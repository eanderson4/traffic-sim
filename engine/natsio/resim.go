package natsio

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"time"

	server "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// resim.go — support for the offline record-plane consumers (cmd/bake,
// ADR-0023): the no-listener broker over an existing store, and the
// streaming record digest. The shared re-sim CORE itself is the
// workstream's ADR-0023 extraction — findKeyframe, the logCursor forward
// reader, tickRecords/decodeLogged* (logcursor.go, replay.go) — which both
// the Player and the bake source drive; only the divergence POLICY differs
// (the Player logs and continues, bake aborts).

// recordHasher computes the ADR-0023 §8 record digest incrementally:
// sha256 over the log stream messages AS CONSUMED, in stream order, so a
// replaced or regenerated store with identical metadata still digests
// differently. Per message, length-framed (all big-endian):
//
//	u64be stream sequence |
//	u32be subject-len + subject |
//	u32be header-block-len + header block |
//	u32be payload-len + payload
//
// The header block carries, where present and in this fixed order,
// schema_version, tick, kf_chunk, sig_chunk — each as u32be key-len + key,
// u32be value-len + value.
//
// "As consumed" means each message EXACTLY ONCE. Callers that group by tick
// necessarily read one message past the group boundary and hand it to the
// next group (BakeSource.Run's `pending`), so the same message is offered
// twice; folding it twice is deterministic — the stability test still passes
// — but yields a digest no independent verifier reproduces, and the digest
// content-keys the published `baked/{run}/{hash12}/` prefix. Dedup lives here
// rather than at the call site so a second grouping caller cannot
// reintroduce it.
type recordHasher struct {
	h       hash.Hash
	lastSeq uint64 // stream sequences are strictly increasing; 0 is unused
}

func newRecordHasher() *recordHasher {
	return &recordHasher{h: sha256.New()}
}

// digestHeaderKeys are the message headers the record digest covers, in
// block order.
var digestHeaderKeys = []string{headerSchemaVersion, headerTick, headerKeyframeChunk, headerSigChunk}

// add folds one message into the digest. seq is the message's stream
// sequence.
func (r *recordHasher) add(seq uint64, m *nats.Msg) {
	if seq <= r.lastSeq {
		return // re-offered across a tick-group boundary; already folded in
	}
	r.lastSeq = seq
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], seq)
	r.h.Write(b[:])
	put := func(data []byte) {
		binary.BigEndian.PutUint32(b[:4], uint32(len(data)))
		r.h.Write(b[:4])
		r.h.Write(data)
	}
	put([]byte(m.Subject))
	var block []byte
	for _, k := range digestHeaderKeys {
		v := m.Header.Get(k)
		if v == "" {
			continue
		}
		var lb [4]byte
		binary.BigEndian.PutUint32(lb[:], uint32(len(k)))
		block = append(block, lb[:]...)
		block = append(block, k...)
		binary.BigEndian.PutUint32(lb[:], uint32(len(v)))
		block = append(block, lb[:]...)
		block = append(block, v...)
	}
	put(block)
	put(m.Data)
}

// sum returns the digest.
func (r *recordHasher) sum() [32]byte {
	var out [32]byte
	copy(out[:], r.h.Sum(nil))
	return out
}

// OpenRecordingStore boots an embedded broker over an existing JetStream
// store dir with NO listeners (no client port, no WebSocket — the offline
// consumer shape cmd/bake uses, cmd/replay's broker minus the browser
// plane) and returns a JetStream context on an in-process connection.
//
// STORE EXCLUSIVITY: exactly one broker may open a JetStream store dir at a
// time (no locking exists) — the serve that recorded the run must have
// EXITED first. The returned shutdown func closes the connection and the
// broker. Keeping this here preserves the nats.go/nats-server confinement:
// cmd/bake never imports either module.
func OpenRecordingStore(storeDir string) (nats.JetStreamContext, func(), error) {
	ns, err := server.NewServer(&server.Options{
		DontListen: true,
		JetStream:  true,
		StoreDir:   storeDir,
		// 4 MB, same as serve and replay (ADR-0016): headroom for big
		// frames, not a design allowance.
		MaxPayload: 4 << 20,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("nats-server: %w", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		return nil, nil, fmt.Errorf("nats-server not ready")
	}
	nc, err := nats.Connect(nats.DefaultURL, nats.InProcessServer(ns), nats.Name("bake"))
	if err != nil {
		ns.Shutdown()
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, nil, fmt.Errorf("JetStream: %w", err)
	}
	shutdown := func() {
		nc.Close()
		ns.Shutdown()
	}
	return js, shutdown, nil
}
