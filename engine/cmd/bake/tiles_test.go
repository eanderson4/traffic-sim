package main

import (
	"math"
	"testing"
)

// tiles_test.go — the z11 region partitioning (ADR-0023 §3).

func TestMercTileKnown(t *testing.T) {
	// Null island sits exactly on the z0 tile boundary; at z11 the tile
	// covering (0,0) is (1024, 1024).
	if x, y := mercTile(0, 0, regionZoom); x != 1024 || y != 1024 {
		t.Fatalf("(0,0) → z11/%d/%d, want z11/1024/1024", x, y)
	}
	// San Francisco (~37.7749, -122.4194): z11/327/791 is the well-known
	// slippy tile.
	x, y := mercTile(-122.4194, 37.7749, regionZoom)
	if x != 327 || y != 791 {
		t.Fatalf("SF → z11/%d/%d, want z11/327/791", x, y)
	}
	// The ADR's LA example uses z11/352/819-ish tiles; check LA lands in
	// the neighborhood (and inside range).
	x, y = mercTile(-118.2437, 34.0522, regionZoom)
	if x < 350 || x > 354 || y < 817 || y > 821 {
		t.Fatalf("LA → z11/%d/%d, out of the expected range", x, y)
	}
}

func TestMercTileClamps(t *testing.T) {
	if x, y := mercTile(180, 85.0511, regionZoom); x != 2047 || y != 0 {
		t.Fatalf("edge → z11/%d/%d, want z11/2047/0", x, y)
	}
	if x, y := mercTile(-180, -85.0511, regionZoom); x != 0 || y != 2047 {
		t.Fatalf("edge → z11/%d/%d, want z11/0/2047", x, y)
	}
}

func TestTileBBoxRoundTrip(t *testing.T) {
	x, y := mercTile(-122.4194, 37.7749, regionZoom)
	w, s, e, n := tileBBox(x, y, regionZoom)
	if !(-122.4194 >= w && -122.4194 < e && 37.7749 >= s && 37.7749 < n) {
		t.Fatalf("bbox [%v %v %v %v] does not contain the point", w, s, e, n)
	}
	// The inverse lands back on the same tile from the bbox center.
	cx, cy := mercTile((w+e)/2, (s+n)/2, regionZoom)
	if cx != x || cy != y {
		t.Fatalf("bbox center → z11/%d/%d, want z11/%d/%d", cx, cy, x, y)
	}
	// z11 tiles are ~0.088° wide.
	if math.Abs((e-w)-360.0/2048.0) > 1e-12 {
		t.Fatalf("tile width %v, want %v", e-w, 360.0/2048.0)
	}
}

func TestRegionKeyDir(t *testing.T) {
	key := regionKey(352, 819)
	if key != "z11/352/819" {
		t.Fatalf("regionKey = %q", key)
	}
	if d := regionDir(key); d != "z11-352-819" {
		t.Fatalf("regionDir = %q", d)
	}
	x, y, err := parseRegionKey(key)
	if err != nil || x != 352 || y != 819 {
		t.Fatalf("parseRegionKey: %d %d %v", x, y, err)
	}
	if _, _, err := parseRegionKey("z9/1/2"); err == nil {
		t.Fatal("wrong-zoom key accepted")
	}
}
