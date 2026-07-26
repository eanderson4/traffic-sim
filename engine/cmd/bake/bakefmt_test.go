package main

import (
	"encoding/binary"
	"math"
	"testing"
)

// bakefmt_test.go — TSRB/TSRL wire-format round-trips (ADR-0023 §2, §4).

func TestTSRBRoundTrip(t *testing.T) {
	vehs := []tsrbVehicle{
		{ID: 1, X: 100, Y: 200, Angle: 0, Class: 0},
		{ID: 42, X: math.MaxUint32, Y: 0, Angle: 255, Class: 3},
		{ID: 7, X: 12345, Y: 67890, Angle: 128, Class: 1},
	}
	buf := encodeTSRBFrame(12345, vehs)

	// The exact header layout, byte for byte.
	if len(buf) != tsrbHeader+tsrbPerVeh*3 {
		t.Fatalf("frame size %d, want %d", len(buf), tsrbHeader+tsrbPerVeh*3)
	}
	if got := binary.LittleEndian.Uint32(buf[0:]); got != tsrbMagic {
		t.Fatalf("magic %#08x, want %#08x (TSRB)", got, tsrbMagic)
	}
	if got := binary.LittleEndian.Uint16(buf[4:]); got != tsrbVersion {
		t.Fatalf("version %d, want %d", got, tsrbVersion)
	}
	if got := binary.LittleEndian.Uint64(buf[8:]); got != 12345 {
		t.Fatalf("tick %d, want 12345", got)
	}
	if got := binary.LittleEndian.Uint32(buf[16:]); got != 3 {
		t.Fatalf("vehicle_count %d, want 3", got)
	}

	tick, got, rest, err := parseTSRBFrame(buf)
	if err != nil {
		t.Fatal(err)
	}
	if tick != 12345 || len(rest) != 0 {
		t.Fatalf("tick %d, %d trailing bytes", tick, len(rest))
	}
	if len(got) != len(vehs) {
		t.Fatalf("%d vehicles, want %d", len(got), len(vehs))
	}
	for i, v := range vehs {
		if got[i] != v {
			t.Fatalf("vehicle %d: got %+v, want %+v", i, got[i], v)
		}
	}
}

func TestTSRBChunkIteration(t *testing.T) {
	// A chunk is header+records repeated; vehicle_count is the only frame
	// delimiter. Three frames, the middle one header-only (0 vehicles).
	var chunk []byte
	chunk = append(chunk, encodeTSRBFrame(0, []tsrbVehicle{{ID: 1}})...)
	chunk = append(chunk, encodeTSRBFrame(5, nil)...)
	chunk = append(chunk, encodeTSRBFrame(10, []tsrbVehicle{{ID: 2}, {ID: 3}})...)

	var ticks []uint64
	var counts []int
	for buf := chunk; len(buf) > 0; {
		tick, vehs, rest, err := parseTSRBFrame(buf)
		if err != nil {
			t.Fatal(err)
		}
		ticks = append(ticks, tick)
		counts = append(counts, len(vehs))
		buf = rest
	}
	if len(ticks) != 3 || ticks[0] != 0 || ticks[1] != 5 || ticks[2] != 10 {
		t.Fatalf("chunk ticks %v, want [0 5 10]", ticks)
	}
	if counts[0] != 1 || counts[1] != 0 || counts[2] != 2 {
		t.Fatalf("chunk counts %v, want [1 0 2]", counts)
	}
}

func TestTSRBRejects(t *testing.T) {
	if _, _, _, err := parseTSRBFrame(nil); err == nil {
		t.Fatal("empty buffer accepted")
	}
	buf := encodeTSRBFrame(0, nil)
	buf[0] ^= 0xff
	if _, _, _, err := parseTSRBFrame(buf); err == nil {
		t.Fatal("bad magic accepted")
	}
	buf = encodeTSRBFrame(0, []tsrbVehicle{{ID: 1}})
	if _, _, _, err := parseTSRBFrame(buf[:len(buf)-1]); err == nil {
		t.Fatal("truncated frame accepted")
	}
}

func TestTSRLRoundTrip(t *testing.T) {
	pairs := []tsrlPair{{LaneIdx: 0, RatioQ: 0}, {LaneIdx: 17, RatioQ: 170}, {LaneIdx: 400000, RatioQ: 255}}
	buf := encodeTSRLFrame(777, pairs)
	if len(buf) != tsrlHeader+tsrlPerPair*3 {
		t.Fatalf("frame size %d, want %d", len(buf), tsrlHeader+tsrlPerPair*3)
	}
	if got := binary.LittleEndian.Uint32(buf[0:]); got != tsrlMagic {
		t.Fatalf("magic %#08x, want %#08x (TSRL)", got, tsrlMagic)
	}
	tick, got, rest, err := parseTSRLFrame(buf)
	if err != nil {
		t.Fatal(err)
	}
	if tick != 777 || len(rest) != 0 {
		t.Fatalf("tick %d, %d trailing", tick, len(rest))
	}
	for i, p := range pairs {
		if got[i] != p {
			t.Fatalf("pair %d: got %+v, want %+v", i, got[i], p)
		}
	}
}

func TestQuantXY(t *testing.T) {
	origin := 1000.0
	for _, c := range []float64{1000, 1000.04, 1000.05, 1234.56, 430000.0} {
		q, err := quantXY(c, origin)
		if err != nil {
			t.Fatalf("quantXY(%v): %v", c, err)
		}
		d := dequantXY(q, origin)
		if math.Abs(d-c) > quantXYStepM/2+1e-9 {
			t.Fatalf("quantXY(%v) round-trips to %v (error > half quantum)", c, d)
		}
	}
	if _, err := quantXY(-0.05, 0); err == nil {
		t.Fatal("negative quantized coordinate accepted")
	}
	if _, err := quantXY(math.NaN(), 0); err == nil {
		t.Fatal("NaN accepted")
	}
}

func TestQuantAngle(t *testing.T) {
	if q := quantAngle(0); q != 0 {
		t.Fatalf("angle 0 → %d, want 0", q)
	}
	// Negative headings normalize into [0, 2π) FIRST (math.Mod keeps the
	// sign): -π/2 is 3π/2, not a negative byte.
	if q := quantAngle(-math.Pi / 2); q != 192 {
		t.Fatalf("angle -π/2 → %d, want 192", q)
	}
	// floor, never round: just under 2π is 255, never 256.
	if q := quantAngle(2*math.Pi - 1e-9); q != 255 {
		t.Fatalf("angle ≈2π → %d, want 255", q)
	}
	if q := quantAngle(2 * math.Pi); q != 0 {
		t.Fatalf("angle 2π → %d, want 0 (mod first)", q)
	}
	if q := quantAngle(7 * math.Pi / 4); q != 224 {
		t.Fatalf("angle 7π/4 → %d, want 224", q)
	}
}

func TestQuantRatio(t *testing.T) {
	cases := []struct {
		r    float64
		want uint8
	}{
		{0, 0}, {0.5, 85}, {1.0, 170}, {1.5, 255}, {2.0, 255}, {-1, 0},
	}
	for _, c := range cases {
		if got := quantRatio(c.r); got != c.want {
			t.Fatalf("quantRatio(%v) = %d, want %d", c.r, got, c.want)
		}
	}
}
