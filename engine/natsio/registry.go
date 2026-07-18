package natsio

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"traffic-sim/engine"
)

// registry.go — the metadata plane (ADR-0006 §1): the KV run registry.
// Bucket ts_runs holds, per run:
//
//	{run}/meta   RunMeta — scenario, seed, params, status (written at run
//	             start, re-written on terminal status)
//	{run}/state  StatePtr — latest state pointer for late joiners (tick,
//	             keyframe stream sequence, CRC; updated at keyframe cadence)
//
// Late joiners read the pointer, attach the live snapshot subject, and
// discard messages with tick ≤ the materialized tick (arch-state-authority
// §6). Wall-clock timestamps appear here only as edge metadata (ADR-0005).

// RunMeta is the {run}/meta value. Spec is embedded in full so replay from
// the record plane needs nothing else (the keyframe format deliberately
// does not duplicate the spec).
type RunMeta struct {
	RunID       string         `json:"run_id"`
	Spec        engine.RunSpec `json:"spec"`
	SchemaVer   int            `json:"schema_version"`
	Status      string         `json:"status"` // running | done | aborted
	Error       string         `json:"error,omitempty"`
	StartedTick uint64         `json:"started_tick"`
	CreatedUnix int64          `json:"created_unix"` // edge metadata (wall clock)
	EndedUnix   int64          `json:"ended_unix,omitempty"`
}

// Run status values.
const (
	StatusRunning = "running"
	StatusDone    = "done"
	StatusAborted = "aborted"
)

// StatePtr is the {run}/state value: the late-joiner resync pointer.
type StatePtr struct {
	Tick        uint64 `json:"tick"`
	KeyframeSeq uint64 `json:"keyframe_seq"` // stream sequence of the latest keyframe
	CRC         uint64 `json:"crc"`          // rolling CRC at Tick (hex in JSON via number)
}

// Registry wraps the KV bucket.
type Registry struct {
	kv nats.KeyValue
}

// NewRegistry creates (or adopts) the run-registry bucket.
func NewRegistry(js nats.JetStreamContext) (*Registry, error) {
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:      RegistryBucket,
		Description: "traffic-sim run registry (ADR-0006 metadata plane)",
		History:     1,
		Storage:     nats.FileStorage,
		Replicas:    1,
	})
	if err != nil {
		return nil, fmt.Errorf("create KV bucket %s: %w", RegistryBucket, err)
	}
	return &Registry{kv: kv}, nil
}

// Start writes the initial {run}/meta (status running).
func (r *Registry) Start(run string, spec engine.RunSpec) error {
	meta := RunMeta{
		RunID:       run,
		Spec:        spec,
		SchemaVer:   SchemaVersion,
		Status:      StatusRunning,
		StartedTick: 0,
		CreatedUnix: time.Now().Unix(),
	}
	return r.putMeta(&meta)
}

// Finish records the terminal status (done, or aborted with the error).
func (r *Registry) Finish(run string, runErr error) error {
	meta, err := r.Meta(run)
	if err != nil {
		return err
	}
	if runErr != nil {
		meta.Status = StatusAborted
		meta.Error = runErr.Error()
	} else {
		meta.Status = StatusDone
	}
	meta.EndedUnix = time.Now().Unix()
	return r.putMeta(meta)
}

func (r *Registry) putMeta(m *RunMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = r.kv.Put(m.RunID+"/meta", data)
	return err
}

// UpdateState refreshes the {run}/state late-joiner pointer.
func (r *Registry) UpdateState(run string, tick, keyframeSeq, crc uint64) error {
	data, err := json.Marshal(StatePtr{Tick: tick, KeyframeSeq: keyframeSeq, CRC: crc})
	if err != nil {
		return err
	}
	_, err = r.kv.Put(run+"/state", data)
	return err
}

// Meta reads {run}/meta.
func (r *Registry) Meta(run string) (*RunMeta, error) {
	entry, err := r.kv.Get(run + "/meta")
	if err != nil {
		return nil, fmt.Errorf("KV get %s/meta: %w", run, err)
	}
	var m RunMeta
	if err := json.Unmarshal(entry.Value(), &m); err != nil {
		return nil, fmt.Errorf("KV meta %s corrupt: %w", run, err)
	}
	return &m, nil
}

// State reads {run}/state.
func (r *Registry) State(run string) (*StatePtr, error) {
	entry, err := r.kv.Get(run + "/state")
	if err != nil {
		return nil, fmt.Errorf("KV get %s/state: %w", run, err)
	}
	var s StatePtr
	if err := json.Unmarshal(entry.Value(), &s); err != nil {
		return nil, fmt.Errorf("KV state %s corrupt: %w", run, err)
	}
	return &s, nil
}
