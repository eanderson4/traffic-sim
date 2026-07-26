package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"traffic-sim/engine"
	"traffic-sim/engine/natsio"
)

// bake.go — the bake orchestration (ADR-0023): strict re-simulation →
// quantized per-region frames → contiguous chunk windows → content-keyed
// prefix. Chunks stream to a staging dir during the re-sim (the content
// key needs the record digest, known only after the last message is
// consumed); when the digest lands, the staging dir becomes the prefix
// atomically.

// Bake cadence and chunking knobs (ADR-0023 §2–§4; all ride the
// bake-config digest). The bake stride itself is DERIVED from the run's dt
// (see bakeStride), never hardcoded.
const (
	laneEveryFrames = 10  // 0.2 Hz lane-speed aggregates
	chunkFrames     = 120 // 60 s of TSRB frames per chunk
	laneChunkFrames = 12  // 60 s of TSRL frames per chunk
	quantXYStepM    = 0.1 // TSRB x/y quantum (m)

	// quantOriginMarginM lowers the quantization origin below the network
	// bbox min so vehicles on the bbox-edge lanes — projected points shift
	// up to one lane width perpendicular of the polyline — never quantize
	// negative. The origin is carried in index.json, so the margin is
	// decoder-visible, not hidden state.
	quantOriginMarginM = 100.0

	// pmtilesLaneThreshold is the auto net-format cutoff: networks larger
	// than this bake PMTiles (city scale; ADR-0023 §7), smaller bake plain
	// GeoJSON (i280's 375 lanes / 287 KB need no tiles).
	pmtilesLaneThreshold = 100_000
)

// bakeStride derives the bake cadence from the recorded run's dt
// (ADR-0023 §2, post-implementation clarification): bakeEveryTicks =
// max(1, round(0.5/dt)), and the derived rate 1/(bakeEveryTicks×dt) must
// land in the agreed [1,2] Hz band or the dt is rejected (dt 0.1 → 5
// ticks → 2.0 Hz).
func bakeStride(dt float64) (uint64, error) {
	if dt <= 0 || math.IsNaN(dt) || math.IsInf(dt, 0) {
		return 0, fmt.Errorf("dt %v is not a positive finite timestep", dt)
	}
	stride := uint64(math.Max(1, math.Round(0.5/dt)))
	rate := 1 / (float64(stride) * dt)
	if rate < 1 || rate > 2 {
		return 0, fmt.Errorf("dt %v: bake cadence %d ticks lands at %.3g Hz, outside the agreed [1,2] Hz band", dt, stride, rate)
	}
	return stride, nil
}

// bakeParams configures one bake.
type bakeParams struct {
	OutDir      string // output root; the prefix is {OutDir}/baked/{run}/{hash12}
	OverlaysDir string // optional demo overlay set (demosrv's /overlay/* files)
	NetFormat   string // auto | geojson | pmtiles
	BaseURL     string // deployment origin for the absolute network.pmtiles URL
}

// bakeResult summarizes a completed bake.
type bakeResult struct {
	Prefix string     // absolute path of the content-keyed prefix
	Hash12 string     // the content key
	Index  *bakeIndex // the manifest as written
}

// bakedLaneState carries the per-network statics the frame sink needs.
type bakedLaneState struct {
	geoms    []natsio.LaneGeom // placeholder projection for shapeless lanes
	laneHome []string          // region key per lane index (home tile by midpoint)
	laneIdx  map[string]uint32 // occupied-lane id → lanes.json index (first-appearance order)
	laneIDs  []string          // lanes.json content, in index order
}

// bake runs the whole pipeline. On error the staging dir is removed — a
// failed bake leaves nothing under a content key (and nothing half-staged).
func bake(src *natsio.BakeSource, params bakeParams) (*bakeResult, error) {
	meta := src.Meta()
	spec := meta.Spec
	if spec.Net.Kind != "file" {
		return nil, fmt.Errorf("bake needs a compiled network (Net.Kind %q) — region assignment and the WGS84 export read lane geometry", spec.Net.Kind)
	}
	nfBytes, err := os.ReadFile(spec.Net.Path)
	if err != nil {
		return nil, fmt.Errorf("network file: %w", err)
	}
	var nf engine.NetFile
	if err := json.Unmarshal(nfBytes, &nf); err != nil {
		return nil, fmt.Errorf("network file %s: %w", spec.Net.Path, err)
	}
	if nf.Provenance == nil || nf.Provenance.Projection == "" {
		return nil, fmt.Errorf("network %s has no projection provenance — cannot project to WGS84 for region assignment", spec.Net.Path)
	}
	proj, err := engine.MakeProjector(engine.LocalFrame{
		Projection: nf.Provenance.Projection,
		NetOffset:  nf.Provenance.NetOffset,
	})
	if err != nil {
		return nil, err
	}

	// Network bbox (local metric frame) → quantization origin + WGS84 bounds.
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := range nf.Lanes {
		for _, pt := range nf.Lanes[i].Shape {
			minX, minY = math.Min(minX, pt[0]), math.Min(minY, pt[1])
			maxX, maxY = math.Max(maxX, pt[0]), math.Max(maxY, pt[1])
		}
	}
	if math.IsInf(minX, 0) {
		return nil, fmt.Errorf("network %s has no lane geometry", spec.Net.Path)
	}
	origin := [2]float64{minX - quantOriginMarginM, minY - quantOriginMarginM}
	var bounds [4]float64
	{
		w, s, e, n := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, c := range [][2]float64{{minX, minY}, {minX, maxY}, {maxX, minY}, {maxX, maxY}} {
			lng, lat := proj(c[0], c[1])
			w, s = math.Min(w, lng), math.Min(s, lat)
			e, n = math.Max(e, lng), math.Max(n, lat)
		}
		bounds = [4]float64{w, s, e, n}
	}

	// Network artifact format, resolved up front so a missing tippecanoe
	// fails BEFORE the re-sim, not after it.
	format := params.NetFormat
	if format == "auto" {
		format = "geojson"
		if len(nf.Lanes) > pmtilesLaneThreshold {
			format = "pmtiles"
		}
	}
	var tippecanoeBin, tippecanoeVer string
	if format == "pmtiles" {
		tippecanoeBin, err = findTippecanoe()
		if err != nil {
			return nil, err
		}
		tippecanoeVer, err = tippecanoeVersion(tippecanoeBin)
		if err != nil {
			return nil, err
		}
	}

	// Overlays: output-affecting inputs — read now, feed the content key,
	// copy into the prefix (ADR-0023 §1, §8).
	var overlayNames []string
	var overlayBytes [][]byte
	if params.OverlaysDir != "" {
		ents, err := os.ReadDir(params.OverlaysDir)
		if err != nil {
			return nil, fmt.Errorf("overlays: %w", err)
		}
		for _, ent := range ents {
			if ent.IsDir() {
				continue
			}
			data, err := os.ReadFile(filepath.Join(params.OverlaysDir, ent.Name()))
			if err != nil {
				return nil, fmt.Errorf("overlays: %w", err)
			}
			overlayNames = append(overlayNames, ent.Name())
			overlayBytes = append(overlayBytes, data)
		}
		sort.Sort(overlaySorter{overlayNames, overlayBytes})
	}

	// Per-network statics for the frame sink.
	e0 := src.Engine()
	st := &bakedLaneState{
		geoms:   natsio.LaneGeoms(e0.Net),
		laneIdx: map[string]uint32{},
	}
	st.laneHome = make([]string, len(e0.Net.Lanes))
	for _, l := range e0.Net.Lanes {
		x, y, _, ok := l.Project(l.Length / 2)
		if !ok {
			g := st.geoms[l.Index] // placeholder projection, same rule as the wire
			x, y = g.XOff+l.Length/2, g.Y
		}
		lng, lat := proj(x, y)
		tx, ty := mercTile(lng, lat, regionZoom)
		st.laneHome[l.Index] = regionKey(tx, ty)
	}

	// The TSSG table is static per run (ADR-0023 §10) — capture the chunk
	// set at tick 0; the index carries each chunk's byte length as framing.
	sigChunks := natsio.SignalChunks(e0)
	sigBytes := make([]int, len(sigChunks))
	for i, c := range sigChunks {
		sigBytes[i] = len(c)
	}

	// Baked-tick schedule: tickStart + k×stride, plus a TERMINAL frame at
	// tickEnd when it is off-stride (Player parity — it lands exactly on
	// the final tick past the decimation stride). The stride derives from
	// the recorded dt; the [1,2] Hz band is enforced there.
	stride, err := bakeStride(spec.Params.Dt)
	if err != nil {
		return nil, err
	}
	endTick := src.EndTick()
	numStride := endTick/stride + 1
	hasTerminal := (numStride-1)*stride != endTick
	nFrames := int(numStride)
	if hasTerminal {
		nFrames++
	}
	tickOf := func(i int) uint64 {
		if hasTerminal && i == nFrames-1 {
			return endTick
		}
		return uint64(i) * stride
	}
	// TSRL aggregate ticks land EXACTLY on tickStart + a×(stride×
	// laneEveryFrames) — the shim looks up by exact tick equality, so the
	// terminal off-stride TSRB frame never carries an aggregate.
	aggEvery := stride * uint64(laneEveryFrames)
	nAgg := int(endTick/aggEvery) + 1
	aggTickOf := func(a int) uint64 { return uint64(a) * aggEvery }

	// Chunks stream to staging; the content key (which names the prefix)
	// needs the record digest, known only after the re-sim's last message.
	staging := filepath.Join(params.OutDir, ".bake-staging-"+meta.RunID)
	if err := os.RemoveAll(staging); err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging) // no-op after a successful move

	framesSet := newChunkSet(filepath.Join(staging, "frames"), "frames", ".tsrb.br",
		chunkFrames, tickOf,
		func(tick uint64) []byte { return encodeTSRBFrame(tick, nil) })
	lanesSet := newChunkSet(filepath.Join(staging, "lanes"), "lanes", ".tsrl.br",
		laneChunkFrames, aggTickOf,
		func(tick uint64) []byte { return encodeTSRLFrame(tick, nil) })

	k, agg := 0, 0
	runErr := src.Run(func(e *engine.Engine) error {
		tick := e.Tick
		if tick%stride != 0 && tick != endTick {
			return nil
		}
		frames, err := tsrbFrame(e, tick, origin, proj, st)
		if err != nil {
			return err
		}
		if err := framesSet.addFrame(k, frames); err != nil {
			return err
		}
		// Aggregate by TICK, not frame index: an off-stride terminal frame
		// must not emit a TSRL frame at a tick the shim's exact-equality
		// lookup would never ask for.
		if tick%aggEvery == 0 {
			pairs, err := tsrlFrame(e, tick, st)
			if err != nil {
				return err
			}
			if err := lanesSet.addFrame(agg, pairs); err != nil {
				return err
			}
			agg++
		}
		k++
		return nil
	})
	if runErr != nil {
		return nil, runErr
	}
	if k != nFrames {
		return nil, fmt.Errorf("bake schedule mismatch: %d frames baked, schedule said %d — index would lie", k, nFrames)
	}
	if agg != nAgg {
		return nil, fmt.Errorf("bake schedule mismatch: %d aggregate frames baked, schedule said %d", agg, nAgg)
	}
	if err := framesSet.finish(nFrames); err != nil {
		return nil, err
	}
	if err := lanesSet.finish(nAgg); err != nil {
		return nil, err
	}

	// lanes.json — the deduped occupied-lane-id table (lane_idx order).
	if err := writeJSONAtomic(staging, "lanes.json", st.laneIDs); err != nil {
		return nil, err
	}
	// signals.tssg — the TSSG chunk set, concatenated.
	if err := writeFileAtomic(staging, "signals.tssg", func(w io.Writer) error {
		for _, c := range sigChunks {
			if _, err := w.Write(c); err != nil {
				return err
			}
		}
		return nil
	}, nil); err != nil {
		return nil, err
	}
	// overlays/ — copied verbatim.
	var overlayURLs []string
	for i, name := range overlayNames {
		data := overlayBytes[i]
		if err := writeFileAtomic(filepath.Join(staging, "overlays"), name, func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		}, nil); err != nil {
			return nil, err
		}
		overlayURLs = append(overlayURLs, "overlays/"+name)
	}
	if overlayURLs == nil {
		overlayURLs = []string{}
	}

	// Network artifact.
	netDesc := networkDesc{PromoteID: "id"}
	switch format {
	case "geojson":
		if err := writeFileAtomic(staging, "network.geojson", func(w io.Writer) error {
			return engine.WriteGeoJSON(&nf, w)
		}, nil); err != nil {
			return nil, err
		}
		netDesc.GeoJSON = "network.geojson"
	case "pmtiles":
		cityKey := cityHash12(nfBytes, tippecanoeVer)
		cityDir := filepath.Join(params.OutDir, "city", cityKey)
		ndjson := filepath.Join(cityDir, "network.geojson.ndjson")
		if err := writeFileAtomic(cityDir, "network.geojson.ndjson", func(w io.Writer) error {
			return exportNetworkWGS84(&nf, proj, w)
		}, nil); err != nil {
			return nil, err
		}
		pmtPath := filepath.Join(cityDir, "network.pmtiles")
		if err := runTippecanoe(tippecanoeBin, ndjson, pmtPath); err != nil {
			return nil, err
		}
		netDesc.PMTiles = strings.TrimSuffix(params.BaseURL, "/") + "/city/" + cityKey + "/network.pmtiles"
		netDesc.Layer = "lanes"
	default:
		return nil, fmt.Errorf("unknown -net-format %q (want auto|geojson|pmtiles)", params.NetFormat)
	}

	// The content key over the FULL bake identity (ADR-0023 §8).
	// TODO(MVP-deferred): fold an output-manifest digest into the key once
	// the manifest's exact byte shape settles (post-implementation spec
	// clarification, item 9).
	id := bakeIdentity{
		Stream:       src.Stream(),
		Run:          meta.RunID,
		ScenarioHash: spec.Hash,
		Seed:         spec.Seed,
		Ticks:        spec.Ticks,
		RecordDigest: src.RecordDigest(),
		OverlayNames: overlayNames,
		OverlayBytes: overlayBytes,
		ConfigDigest: configDigest(currentBakeConfig(stride)),
	}
	hash12 := id.hash12()
	prefix := filepath.Join(params.OutDir, "baked", meta.RunID, hash12)
	if err := os.RemoveAll(prefix); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(prefix), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(staging, prefix); err != nil {
		return nil, fmt.Errorf("move staging into prefix: %w", err)
	}

	// index.json — written LAST, into the final prefix.
	idx := &bakeIndex{
		Version:         1,
		Run:             meta.RunID,
		ScenarioHash:    spec.Hash,
		Dt:              spec.Params.Dt,
		Frame:           frameDesc{Projection: nf.Provenance.Projection, NetOffset: nf.Provenance.NetOffset},
		BakeEveryTicks:  stride,
		LaneEveryFrames: laneEveryFrames,
		TickStart:       0,
		TickEnd:         endTick,
		Quant:           quantDesc{XYStepM: quantXYStepM, Origin: origin},
		Network:         netDesc,
		Bounds:          bounds,
		Overlays:        overlayURLs,
		Signals:         signalsDesc{URL: "signals.tssg", ChunkBytes: sigBytes},
		LaneIDs:         "lanes.json",
		Regions:         manifestRegions(framesSet, lanesSet),
	}
	if err := writeJSONAtomic(prefix, "index.json", idx); err != nil {
		return nil, err
	}
	return &bakeResult{Prefix: prefix, Hash12: hash12, Index: idx}, nil
}

// tsrbFrame encodes one baked vehicle frame, partitioned by region:
// projected (x, y, angle) quantized per ADR-0023 §2, region assigned by
// the vehicle's projected position. The narrowing guards are the bake's
// fail-loud discipline: any vehicle id above MaxUint32 or type index above
// 255 ABORTS, never truncates.
func tsrbFrame(e *engine.Engine, tick uint64, origin [2]float64, proj engine.Projector, st *bakedLaneState) (map[string][]byte, error) {
	byRegion := map[string][]tsrbVehicle{}
	for _, v := range e.Vehicles() {
		if v.ID > math.MaxUint32 {
			return nil, fmt.Errorf("vehicle id %d exceeds MaxUint32 — the TSRB narrowing is guarded, never assumed", v.ID)
		}
		if v.TypeIdx > 255 {
			return nil, fmt.Errorf("vehicle %d type index %d exceeds 255 — the TSRB class narrowing is guarded, never assumed", v.ID, v.TypeIdx)
		}
		x, y, angle, ok := v.Lane.Project(v.S)
		if !ok {
			g := st.geoms[v.Lane.Index] // placeholder projection (wire rule)
			x, y, angle = g.XOff+v.S, g.Y, 0
		}
		qx, err := quantXY(x, origin[0])
		if err != nil {
			return nil, fmt.Errorf("vehicle %d: %w", v.ID, err)
		}
		qy, err := quantXY(y, origin[1])
		if err != nil {
			return nil, fmt.Errorf("vehicle %d: %w", v.ID, err)
		}
		lng, lat := proj(x, y)
		tx, ty := mercTile(lng, lat, regionZoom)
		key := regionKey(tx, ty)
		byRegion[key] = append(byRegion[key], tsrbVehicle{
			ID: uint32(v.ID), X: qx, Y: qy,
			Angle: quantAngle(angle), Class: uint8(v.TypeIdx),
		})
	}
	frames := make(map[string][]byte, len(byRegion))
	for key, vehs := range byRegion {
		frames[key] = encodeTSRBFrame(tick, vehs)
	}
	return frames, nil
}

// tsrlFrame encodes one lane-speed aggregate frame, partitioned by the
// lanes' HOME tiles (never by vehicle position — every lane has exactly
// one owner region, ADR-0023 §4): instantaneous per-lane mean speed over
// the vehicles on the lane at this tick, divided by the lane's speedLimit,
// quantized. Sparse — only lanes with ≥1 vehicle. Lane ids are assigned
// lanes.json indices in first-appearance order (deterministic: lanes are
// visited in Network.Lanes order).
func tsrlFrame(e *engine.Engine, tick uint64, st *bakedLaneState) (map[string][]byte, error) {
	type acc struct {
		sum float64
		n   int
	}
	occupied := map[*engine.Lane]*acc{}
	for _, v := range e.Vehicles() {
		a := occupied[v.Lane]
		if a == nil {
			a = &acc{}
			occupied[v.Lane] = a
		}
		a.sum += v.V
		a.n++
	}
	byRegion := map[string][]tsrlPair{}
	for _, l := range e.Net.Lanes { // fixed order — deterministic idx assignment
		a := occupied[l]
		if a == nil {
			continue
		}
		if l.SpeedLimit <= 0 {
			continue // no denominator — a ratio cannot be formed
		}
		idx, ok := st.laneIdx[l.ID]
		if !ok {
			idx = uint32(len(st.laneIDs))
			st.laneIdx[l.ID] = idx
			st.laneIDs = append(st.laneIDs, l.ID)
		}
		key := st.laneHome[l.Index]
		byRegion[key] = append(byRegion[key], tsrlPair{
			LaneIdx: idx,
			RatioQ:  quantRatio(a.sum / float64(a.n) / l.SpeedLimit),
		})
	}
	frames := make(map[string][]byte, len(byRegion))
	for key, pairs := range byRegion {
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].LaneIdx < pairs[j].LaneIdx })
		frames[key] = encodeTSRLFrame(tick, pairs)
	}
	return frames, nil
}

// manifestRegions merges the two chunk sets into the manifest's sorted,
// contiguous per-region rows.
func manifestRegions(framesSet, lanesSet *chunkSet) []regionDesc {
	keys := map[string]bool{}
	for key := range framesSet.regions {
		keys[key] = true
	}
	for key := range lanesSet.regions {
		keys[key] = true
	}
	sorted := make([]string, 0, len(keys))
	for key := range keys {
		sorted = append(sorted, key)
	}
	sort.Strings(sorted)
	out := make([]regionDesc, 0, len(sorted))
	for _, key := range sorted {
		x, y, err := parseRegionKey(key)
		if err != nil {
			panic(err) // keys are minted by regionKey
		}
		w, s, e, n := tileBBox(x, y, regionZoom)
		rd := regionDesc{
			Key:    key,
			BBox:   [4]float64{w, s, e, n},
			Frames: []chunkEntry{},
			Lanes:  []chunkEntry{},
		}
		if r, ok := framesSet.regions[key]; ok {
			rd.Frames = r.entries
		}
		if r, ok := lanesSet.regions[key]; ok {
			rd.Lanes = r.entries
		}
		out = append(out, rd)
	}
	return out
}

// cityHash12 is the per-CITY content key for network.pmtiles (ADR-0023
// §8): everything the tile bytes depend on — network bytes (ADR-0018's
// hash12 rule) + tippecanoe version + the exact flag set + the minzoom
// policy + the projection + the derived-property rule set (edgeB) + the
// exporter version.
// TODO(MVP-deferred): fold the produced .pmtiles archive digest into the
// key once the archive bytes are stable across identical inputs
// (post-implementation spec clarification, item 9).
func cityHash12(nfBytes []byte, tippecanoeVer string) string {
	h := sha256.New()
	put := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	h.Write(nfBytes)
	put(tippecanoeVer)
	put(strings.Join(tippecanoeArgs, " "))
	put(minzoomPolicy)
	put("edgeB:v1")
	put(fmt.Sprintf("exporter:%d", bakeToolVersion))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// writeJSONAtomic writes v as indented JSON via temp+rename.
func writeJSONAtomic(dir, name string, v any) error {
	return writeFileAtomic(dir, name, func(w io.Writer) error {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}, nil)
}

// overlaySorter sorts overlay names and bytes together by name.
type overlaySorter struct {
	names []string
	bytes [][]byte
}

func (s overlaySorter) Len() int           { return len(s.names) }
func (s overlaySorter) Less(i, j int) bool { return s.names[i] < s.names[j] }
func (s overlaySorter) Swap(i, j int) {
	s.names[i], s.names[j] = s.names[j], s.names[i]
	s.bytes[i], s.bytes[j] = s.bytes[j], s.bytes[i]
}
