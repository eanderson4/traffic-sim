package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// index.go — the index.json manifest (ADR-0023 §5) and the content key
// (§8). The manifest is the baked replay's single entry point: frame
// descriptor, cadence, quantization, network artifact, and the per-region
// chunk tables (which ARE the keyframe index — every frame is a keyframe).

// bakeIndex is the index.json document.
type bakeIndex struct {
	Version         int          `json:"version"`
	Run             string       `json:"run"`
	ScenarioHash    string       `json:"scenarioHash"` // ADR-0012 content hash from the run registry meta
	Dt              float64      `json:"dt"`           // recorded run's authoritative timestep
	Frame           frameDesc    `json:"frame"`        // local-metric-frame descriptor (proj.ts input)
	BakeEveryTicks  uint64       `json:"bakeEveryTicks"`
	LaneEveryFrames int          `json:"laneEveryFrames"`
	TickStart       uint64       `json:"tickStart"`
	TickEnd         uint64       `json:"tickEnd"` // INCLUSIVE — the last baked tick
	Quant           quantDesc    `json:"quant"`
	Network         networkDesc  `json:"network"`
	Bounds          [4]float64   `json:"bounds"` // WGS84 [west, south, east, north]
	Furniture       string       `json:"furniture,omitempty"`
	Overlays        []string     `json:"overlays"`
	Signals         signalsDesc  `json:"signals"`
	LaneIDs         string       `json:"laneIds"`
	Regions         []regionDesc `json:"regions"`
}

// frameDesc is the local-metric-frame descriptor (projection + netOffset,
// network-format v1 provenance) the projector is built from.
type frameDesc struct {
	Projection string     `json:"projection"`
	NetOffset  [2]float64 `json:"netOffset"`
}

// quantDesc carries the TSRB x/y quantization (ADR-0023 §2).
type quantDesc struct {
	XYStepM float64    `json:"xyStepM"`
	Origin  [2]float64 `json:"origin"`
}

// networkDesc names the network artifact: PMTiles (city scale, absolute
// content-keyed URL) or plain GeoJSON (small networks). One rendering
// contract either way: promoteId "id", and "lanes" as the source-layer on
// the vector path.
type networkDesc struct {
	PMTiles   string `json:"pmtiles,omitempty"`
	GeoJSON   string `json:"geojson,omitempty"`
	Layer     string `json:"layer,omitempty"`
	PromoteID string `json:"promoteId"`
}

// signalsDesc frames the concatenated TSSG chunk set: chunkBytes is each
// chunk's byte length so the shim splits before feeding the accumulator.
type signalsDesc struct {
	URL        string `json:"url"`
	ChunkBytes []int  `json:"chunkBytes"`
}

// regionDesc is one region's manifest row: the z11 tile, its WGS84 bbox,
// and the CONTIGUOUS TSRB/TSRL chunk lists.
type regionDesc struct {
	Key    string       `json:"key"`
	BBox   [4]float64   `json:"bbox"`
	Frames []chunkEntry `json:"frames"`
	Lanes  []chunkEntry `json:"lanes"`
}

// bakeToolVersion is the bake tool's own version — an ingredient of both
// content keys (a tool change MUST land artifacts under new keys).
const bakeToolVersion = 1

// bakeConfig is the bake-config digest's canonical content (ADR-0023 §8):
// every parameter that shapes the output bytes. A rebake with different
// parameters lands under a new key.
type bakeConfig struct {
	BakeToolVersion int     `json:"bakeToolVersion"`
	TSRBVersion     int     `json:"tsrbVersion"`
	TSRLVersion     int     `json:"tsrlVersion"`
	BakeEveryTicks  uint64  `json:"bakeEveryTicks"`
	LaneEveryFrames int     `json:"laneEveryFrames"`
	ChunkFrames     int     `json:"chunkFrames"`
	LaneChunkFrames int     `json:"laneChunkFrames"`
	QuantXYStepM    float64 `json:"quantXYStepM"`
	BrotliQuality   int     `json:"brotliQuality"`
	MinzoomPolicy   string  `json:"minzoomPolicy"`
}

// currentBakeConfig returns the config this build bakes with. stride is
// the dt-derived bake cadence (bakeStride).
func currentBakeConfig(stride uint64) bakeConfig {
	return bakeConfig{
		BakeToolVersion: bakeToolVersion,
		TSRBVersion:     tsrbVersion,
		TSRLVersion:     tsrlVersion,
		BakeEveryTicks:  stride,
		LaneEveryFrames: laneEveryFrames,
		ChunkFrames:     chunkFrames,
		LaneChunkFrames: laneChunkFrames,
		QuantXYStepM:    quantXYStepM,
		BrotliQuality:   brotliQuality,
		MinzoomPolicy:   minzoomPolicy,
	}
}

// configDigest is sha256 over the canonical JSON of the bake config.
func configDigest(cfg bakeConfig) [32]byte {
	data, err := json.Marshal(cfg)
	if err != nil {
		panic(err) // static struct of numbers/strings — cannot fail
	}
	return sha256.Sum256(data)
}

// bakeIdentity is the FULL bake identity the per-recording content key
// covers (ADR-0023 §8): recording stream name + run id + scenario hash +
// seed + tick horizon (run identity is (content-hash, seed), ADR-0012) +
// the record digest + overlay bytes + the bake-config digest.
type bakeIdentity struct {
	Stream       string
	Run          string
	ScenarioHash string
	Seed         uint64
	Ticks        uint64
	RecordDigest [32]byte
	OverlayNames []string
	OverlayBytes [][]byte
	ConfigDigest [32]byte
}

// hash12 is the content key: first 12 hex chars of sha256 over the
// length-prefixed identity parts.
func (id bakeIdentity) hash12() string {
	h := sha256.New()
	put := func(b []byte) {
		var lb [8]byte
		for i := 0; i < 8; i++ {
			lb[i] = byte(len(b) >> (8 * i))
		}
		h.Write(lb[:])
		h.Write(b)
	}
	put([]byte(id.Stream))
	put([]byte(id.Run))
	put([]byte(id.ScenarioHash))
	put([]byte(fmt.Sprintf("%d", id.Seed)))
	put([]byte(fmt.Sprintf("%d", id.Ticks)))
	put(id.RecordDigest[:])
	if len(id.OverlayNames) != len(id.OverlayBytes) {
		panic("overlay names/bytes length mismatch")
	}
	for i := range id.OverlayNames {
		put([]byte(id.OverlayNames[i]))
		put(id.OverlayBytes[i])
	}
	put(id.ConfigDigest[:])
	return hex.EncodeToString(h.Sum(nil))[:12]
}
