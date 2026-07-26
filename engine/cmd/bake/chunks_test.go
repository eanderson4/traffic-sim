package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/andybalholm/brotli"
)

// chunks_test.go — the chunk windowing (ADR-0023 §3): contiguous chunk
// lists, empty windows as header-only chunks, mid-bake region backfill,
// and the short final chunk.

// readChunk decompresses one chunk object and decodes its frames.
func readChunk(t *testing.T, path string) (ticks []uint64, counts []int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := io.ReadAll(brotli.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatalf("brotli decode %s: %v", path, err)
	}
	for len(buf) > 0 {
		tick, vehs, rest, err := parseTSRBFrame(buf)
		if err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		ticks = append(ticks, tick)
		counts = append(counts, len(vehs))
		buf = rest
	}
	return ticks, counts
}

func TestChunkWindowing(t *testing.T) {
	dir := t.TempDir()
	const perChunk = 3
	const nFrames = 8
	tickOf := func(i int) uint64 { return uint64(i) * 5 }
	cs := newChunkSet(dir, "frames", ".tsrb.br", perChunk, tickOf,
		func(tick uint64) []byte { return encodeTSRBFrame(tick, nil) })

	real := func(tick uint64) []byte {
		return encodeTSRBFrame(tick, []tsrbVehicle{{ID: 1, X: 1, Y: 2, Angle: 3, Class: 0}})
	}
	presence := map[int]map[string][]byte{}
	for k := 0; k < nFrames; k++ {
		m := map[string][]byte{"z11/1/1": real(tickOf(k))}
		if k == 4 || k == 6 {
			m["z11/2/2"] = real(tickOf(k))
		}
		if k == 4 {
			m["z11/3/3"] = real(tickOf(k))
		}
		presence[k] = m
	}
	for k := 0; k < nFrames; k++ {
		if err := cs.addFrame(k, presence[k]); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs.finish(nFrames); err != nil {
		t.Fatal(err)
	}

	// Region A (present throughout): 3 chunks, counts 3/3/2, ticks exact.
	rA := cs.regions["z11/1/1"]
	if len(rA.entries) != 3 {
		t.Fatalf("region A: %d chunks, want 3", len(rA.entries))
	}
	wantTickStart := []uint64{0, 15, 30}
	wantCount := []int{3, 3, 2}
	for i, e := range rA.entries {
		if e.TickStart != wantTickStart[i] || e.FrameCount != wantCount[i] {
			t.Fatalf("region A chunk %d: tickStart %d frameCount %d, want %d/%d",
				i, e.TickStart, e.FrameCount, wantTickStart[i], wantCount[i])
		}
		if e.Bytes <= 0 {
			t.Fatalf("region A chunk %d: bytes %d", i, e.Bytes)
		}
		wantURL := "frames/z11-1-1/c00" + string(rune('0'+i)) + ".tsrb.br"
		if e.URL != wantURL {
			t.Fatalf("region A chunk %d: url %q, want %q", i, e.URL, wantURL)
		}
	}
	ticks, counts := readChunk(t, filepath.Join(dir, "z11-1-1", "c002.tsrb.br"))
	if len(ticks) != 2 || ticks[0] != 30 || ticks[1] != 35 || counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("A/c002: ticks %v counts %v, want [30 35]/[1 1]", ticks, counts)
	}

	// Region B (appears at frame 4): backfilled c000 is three header-only
	// frames; c001 mixes empty/real/empty; c002 real then empty.
	rB := cs.regions["z11/2/2"]
	if len(rB.entries) != 3 {
		t.Fatalf("region B: %d chunks, want 3 (contiguous from window 0)", len(rB.entries))
	}
	ticks, counts = readChunk(t, filepath.Join(dir, "z11-2-2", "c000.tsrb.br"))
	if len(ticks) != 3 || ticks[0] != 0 || ticks[1] != 5 || ticks[2] != 10 {
		t.Fatalf("B/c000 backfill ticks %v, want [0 5 10]", ticks)
	}
	for i, c := range counts {
		if c != 0 {
			t.Fatalf("B/c000 frame %d has %d vehicles — backfill must be header-only", i, c)
		}
	}
	_, counts = readChunk(t, filepath.Join(dir, "z11-2-2", "c001.tsrb.br"))
	if counts[0] != 0 || counts[1] != 1 || counts[2] != 0 {
		t.Fatalf("B/c001 counts %v, want [0 1 0]", counts)
	}
	_, counts = readChunk(t, filepath.Join(dir, "z11-2-2", "c002.tsrb.br"))
	if counts[0] != 1 || counts[1] != 0 {
		t.Fatalf("B/c002 counts %v, want [1 0]", counts)
	}

	// Region C (present only at frame 4): its final window is entirely
	// empty — still a chunk (header-only frames), never a gap.
	rC := cs.regions["z11/3/3"]
	if len(rC.entries) != 3 {
		t.Fatalf("region C: %d chunks, want 3", len(rC.entries))
	}
	ticks, counts = readChunk(t, filepath.Join(dir, "z11-3-3", "c002.tsrb.br"))
	if len(ticks) != 2 || ticks[0] != 30 || ticks[1] != 35 {
		t.Fatalf("C/c002 ticks %v, want [30 35]", ticks)
	}
	for i, c := range counts {
		if c != 0 {
			t.Fatalf("C/c002 frame %d has %d vehicles — the empty final window must be header-only", i, c)
		}
	}
}

func TestChunkWindowExactMultiple(t *testing.T) {
	// A bake whose frame count is an exact multiple of the window leaves
	// no ragged tail: the last chunk is full, not a zero-frame extra.
	dir := t.TempDir()
	cs := newChunkSet(dir, "frames", ".tsrb.br", 3,
		func(i int) uint64 { return uint64(i) * 5 },
		func(tick uint64) []byte { return encodeTSRBFrame(tick, nil) })
	for k := 0; k < 6; k++ {
		m := map[string][]byte{"z11/1/1": encodeTSRBFrame(uint64(k)*5, nil)}
		if err := cs.addFrame(k, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := cs.finish(6); err != nil {
		t.Fatal(err)
	}
	r := cs.regions["z11/1/1"]
	if len(r.entries) != 2 || r.entries[0].FrameCount != 3 || r.entries[1].FrameCount != 3 {
		t.Fatalf("entries %+v, want two full chunks of 3", r.entries)
	}
}
