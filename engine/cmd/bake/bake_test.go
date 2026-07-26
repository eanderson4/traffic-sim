package main

import (
	"strings"
	"testing"
)

// bake_test.go — the dt-derived cadence rule (ADR-0023 §2 clarification):
// bakeEveryTicks = max(1, round(0.5/dt)); the derived rate must land in
// [1,2] Hz.

func TestBakeStride(t *testing.T) {
	cases := []struct {
		dt     float64
		want   uint64
		reject bool
	}{
		{0.1, 5, false},   // the recorded runs: 2.0 Hz
		{0.05, 10, false}, // 2.0 Hz
		{0.3, 2, false},   // 1.67 Hz
		{0.5, 1, false},   // 2.0 Hz
		{1.0, 1, false},   // 1.0 Hz (floor of the band)
		{0.4, 0, true},    // stride 1 → 2.5 Hz, above the band
		{2.5, 0, true},    // stride 1 → 0.4 Hz, below the band
		{0, 0, true},
		{-0.1, 0, true},
	}
	for _, c := range cases {
		stride, err := bakeStride(c.dt)
		if c.reject {
			if err == nil {
				t.Fatalf("dt %v: stride %d accepted, want rejection", c.dt, stride)
			}
			continue
		}
		if err != nil {
			t.Fatalf("dt %v: %v", c.dt, err)
		}
		if stride != c.want {
			t.Fatalf("dt %v: stride %d, want %d", c.dt, stride, c.want)
		}
		rate := 1 / (float64(stride) * c.dt)
		if rate < 1 || rate > 2 {
			t.Fatalf("dt %v: rate %v outside [1,2] Hz", c.dt, rate)
		}
	}
	if _, err := bakeStride(0.4); err == nil || !strings.Contains(err.Error(), "[1,2] Hz") {
		t.Fatalf("rejection message %v does not name the band", err)
	}
}
