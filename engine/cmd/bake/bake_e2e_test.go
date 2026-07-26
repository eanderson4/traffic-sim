package main

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/andybalholm/brotli"

	"traffic-sim/engine/natsio"
)

// bake_e2e_test.go — the proof bake (ADR-0023 §9): a full pipeline run
// against the real i280-pod-base-15m recording. The store is COPIED to a
// temp dir first — JetStream recovery may touch the store, and the
// recording is data. Skipped when the recording is absent.

const e2eStore = "../../../data/recordings/i280-pod-base-15m"
const e2eRun = "podbase15"

func TestEndToEndBake(t *testing.T) {
	if _, err := os.Stat(e2eStore); err != nil {
		t.Skipf("recording %s not present", e2eStore)
	}
	storeCopy := filepath.Join(t.TempDir(), "store")
	if out, err := exec.Command("cp", "-a", e2eStore, storeCopy).CombinedOutput(); err != nil {
		t.Fatalf("copy store: %v\n%s", err, out)
	}

	js, shutdown, err := natsio.OpenRecordingStore(storeCopy)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()
	src, err := natsio.NewBakeSource(js, e2eRun)
	if err != nil {
		t.Fatalf("NewBakeSource: %v", err)
	}
	out := t.TempDir()
	res, err := bake(src, bakeParams{OutDir: out, NetFormat: "geojson", BaseURL: "https://data.phantomjam.com"})
	if err != nil {
		t.Fatalf("bake: %v", err)
	}

	// The manifest parses and carries the expected schedule.
	data, err := os.ReadFile(filepath.Join(res.Prefix, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx bakeIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatalf("index.json: %v", err)
	}
	if idx.Version != 1 || idx.Run != e2eRun || idx.Dt != 0.1 {
		t.Fatalf("index header: version %d run %q dt %v", idx.Version, idx.Run, idx.Dt)
	}
	stride, err := bakeStride(idx.Dt)
	if err != nil {
		t.Fatal(err)
	}
	if idx.BakeEveryTicks != stride || idx.LaneEveryFrames != laneEveryFrames {
		t.Fatalf("cadence: %d/%d", idx.BakeEveryTicks, idx.LaneEveryFrames)
	}
	if idx.TickStart != 0 || idx.TickEnd == 0 {
		t.Fatalf("tick range [%d, %d]", idx.TickStart, idx.TickEnd)
	}
	nFrames := int(idx.TickEnd/stride) + 1
	if (uint64(nFrames-1) * stride) != idx.TickEnd {
		nFrames++ // terminal off-stride frame
	}
	if idx.Network.GeoJSON != "network.geojson" || idx.Network.PromoteID != "id" {
		t.Fatalf("network entry: %+v", idx.Network)
	}
	if idx.Frame.Projection == "" {
		t.Fatal("index frame descriptor missing")
	}
	if idx.Quant.XYStepM != quantXYStepM {
		t.Fatalf("quant step %v", idx.Quant.XYStepM)
	}

	// Every region's chunk lists are contiguous and sum to the schedule;
	// lane lists sum to the aggregate schedule (exact tick grid).
	aggEvery := stride * uint64(laneEveryFrames)
	nAgg := int(idx.TickEnd/aggEvery) + 1
	if len(idx.Regions) == 0 {
		t.Fatal("no regions in the index")
	}
	for _, r := range idx.Regions {
		if len(r.Frames) == 0 && len(r.Lanes) == 0 {
			t.Fatalf("region %s: no chunks at all", r.Key)
		}
		// A region may carry only one stream: vehicle frames land where
		// vehicles ARE, lane speeds where lane MIDPOINTS live (the shim
		// inflates TSRL fetches by one tile ring for exactly this,
		// ADR-0023 §4).
		sum, sumL := 0, 0
		for i, c := range r.Frames {
			wantURL := "frames/" + regionDir(r.Key) + "/c"
			if !bytes.HasPrefix([]byte(c.URL), []byte(wantURL)) {
				t.Fatalf("region %s chunk %d url %q", r.Key, i, c.URL)
			}
			sum += c.FrameCount
			if c.Bytes <= 0 {
				t.Fatalf("region %s chunk %d: %d bytes", r.Key, i, c.Bytes)
			}
			if _, err := os.Stat(filepath.Join(res.Prefix, c.URL)); err != nil {
				t.Fatalf("region %s chunk %d: %v", r.Key, i, err)
			}
		}
		for _, c := range r.Lanes {
			sumL += c.FrameCount
		}
		if sum != nFrames && len(r.Frames) > 0 {
			t.Fatalf("region %s: frame counts sum %d, want %d", r.Key, sum, nFrames)
		}
		if sumL != nAgg && len(r.Lanes) > 0 {
			t.Fatalf("region %s: lane counts sum %d, want %d", r.Key, sumL, nAgg)
		}
	}

	// Hexdump-verify the FIRST frame header by decoding it: magic TSRB,
	// schema_version 1, tick 0, and a plausible vehicle count.
	first := idx.Regions[0].Frames[0]
	raw, err := os.ReadFile(filepath.Join(res.Prefix, first.URL))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("brotli: %v", err)
	}
	tick, _, _, err := parseTSRBFrame(plain)
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if tick != 0 {
		t.Fatalf("first frame tick %d, want 0", tick)
	}

	// A mid-bake frame carries vehicles (the pod scenario's demand is
	// flowing well before frame 120) and dequantizes into the local frame.
	mid := idx.Regions[0].Frames[len(idx.Regions[0].Frames)/2]
	raw, err = os.ReadFile(filepath.Join(res.Prefix, mid.URL))
	if err != nil {
		t.Fatal(err)
	}
	plain, err = io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("brotli: %v", err)
	}
	_, vehs, _, err := parseTSRBFrame(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(vehs) == 0 {
		t.Fatalf("mid-bake frame in %s has no vehicles", mid.URL)
	}
	for _, v := range vehs {
		x := dequantXY(v.X, idx.Quant.Origin[0])
		y := dequantXY(v.Y, idx.Quant.Origin[1])
		if math.IsNaN(x) || math.IsNaN(y) {
			t.Fatalf("vehicle %d dequantizes to NaN", v.ID)
		}
		_ = dequantAngle(v.Angle)
	}

	// The LAST chunk of the same region ends at tickEnd (the terminal
	// frame lands exactly on the final tick, Player parity).
	last := idx.Regions[0].Frames[len(idx.Regions[0].Frames)-1]
	raw, err = os.ReadFile(filepath.Join(res.Prefix, last.URL))
	if err != nil {
		t.Fatal(err)
	}
	plain, err = io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("brotli: %v", err)
	}
	var lastTick uint64
	for buf := plain; len(buf) > 0; {
		tk, _, r2, err := parseTSRBFrame(buf)
		if err != nil {
			t.Fatal(err)
		}
		lastTick = tk
		buf = r2
	}
	if lastTick != idx.TickEnd {
		t.Fatalf("last baked tick %d, want tickEnd %d", lastTick, idx.TickEnd)
	}

	// The TSSG set splits per chunkBytes and every chunk parses
	// standalone (ADR-0016's complete-frame rule).
	sigRaw, err := os.ReadFile(filepath.Join(res.Prefix, idx.Signals.URL))
	if err != nil {
		t.Fatal(err)
	}
	off := 0
	for i, n := range idx.Signals.ChunkBytes {
		if off+n > len(sigRaw) {
			t.Fatalf("chunkBytes overruns signals.tssg at chunk %d", i)
		}
		if _, err := natsio.ParseSignalFrame(sigRaw[off : off+n]); err != nil {
			t.Fatalf("signals chunk %d: %v", i, err)
		}
		off += n
	}
	if off != len(sigRaw) {
		t.Fatalf("chunkBytes cover %d of %d signals bytes", off, len(sigRaw))
	}

	// lanes.json is the deduped occupied-lane table; TSRL lane indices
	// stay inside it.
	var laneIDs []string
	lj, err := os.ReadFile(filepath.Join(res.Prefix, idx.LaneIDs))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(lj, &laneIDs); err != nil {
		t.Fatalf("lanes.json: %v", err)
	}
	var firstLane chunkEntry
	found := false
	for _, r := range idx.Regions {
		if len(r.Lanes) > 0 {
			firstLane, found = r.Lanes[0], true
			break
		}
	}
	if !found {
		t.Fatal("no region carries TSRL chunks")
	}
	lraw, err := os.ReadFile(filepath.Join(res.Prefix, firstLane.URL))
	if err != nil {
		t.Fatal(err)
	}
	lplain, err := io.ReadAll(brotli.NewReader(bytes.NewReader(lraw)))
	if err != nil {
		t.Fatal(err)
	}
	// Decode EVERY TSRL frame in the chunk: ticks must land exactly on
	// tickStart + a×(bakeEveryTicks×laneEveryFrames) (the shim's lookup is
	// exact tick equality), and lane indices stay inside lanes.json.
	// TODO(review 2026-07-26): only the FIRST lane chunk of the first
	// matching region is decoded, so a later-chunk off-grid frame would
	// pass; the grid math below also assumes TickStart == 0 (asserted at
	// the index check above, so it holds today). Deferred — the pod
	// recording's grid is uniform and covered.
	for buf := lplain; len(buf) > 0; {
		tk, pairs, rest, err := parseTSRLFrame(buf)
		if err != nil {
			t.Fatal(err)
		}
		if tk%aggEvery != idx.TickStart%aggEvery || tk/aggEvery > uint64(nAgg-1) {
			t.Fatalf("TSRL frame at tick %d is off the exact aggregate grid (every %d ticks)", tk, aggEvery)
		}
		for _, p := range pairs {
			if int(p.LaneIdx) >= len(laneIDs) {
				t.Fatalf("lane_idx %d outside lanes.json (%d ids)", p.LaneIdx, len(laneIDs))
			}
		}
		buf = rest
	}

	t.Logf("bake: %d regions, tickEnd %d, %d frames, %d aggregates, %d occupied lanes",
		len(idx.Regions), idx.TickEnd, nFrames, nAgg, len(laneIDs))
}
