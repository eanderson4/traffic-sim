package main

import (
	"fmt"
	"math"
	"strings"
)

// tiles.go — the ADR-0023 §3 spatial partitioning: web-mercator z11 tile
// math. Region key = "z11/{x}/{y}"; the object directory replaces the
// slashes ("z11-{x}-{y}", mirroring the index.json example).

// regionZoom is the region tile zoom (ADR-0023 §3: z11 — ~16×16 km at
// LA's 34°N, so a z≥13 viewport intersects 1–4 regions).
const regionZoom = 11

// mercTile returns the web-mercator (slippy map) tile at zoom z covering
// (lng, lat) in degrees.
func mercTile(lng, lat float64, z int) (x, y int) {
	n := float64(int64(1) << z)
	xf := (lng + 180) / 360 * n
	latRad := lat * math.Pi / 180
	yf := (1 - math.Log(math.Tan(latRad)+1/math.Cos(latRad))/math.Pi) / 2 * n
	x, y = int(math.Floor(xf)), int(math.Floor(yf))
	// Clamp the poles/antimeridian edges into range.
	max := int(n) - 1
	if x < 0 {
		x = 0
	}
	if x > max {
		x = max
	}
	if y < 0 {
		y = 0
	}
	if y > max {
		y = max
	}
	return x, y
}

// tileBBox returns the tile's WGS84 bounds (west, south, east, north) in
// degrees — the region manifest bbox.
func tileBBox(x, y, z int) (west, south, east, north float64) {
	n := float64(int64(1) << z)
	west = float64(x)/n*360 - 180
	east = float64(x+1)/n*360 - 180
	north = mercLat(float64(y) / n)
	south = mercLat(float64(y+1) / n)
	return west, south, east, north
}

// mercLat inverts the slippy y fraction to latitude in degrees.
func mercLat(yf float64) float64 {
	return math.Atan(math.Sinh(math.Pi*(1-2*yf))) * 180 / math.Pi
}

// regionKey is the manifest key form: "z11/{x}/{y}".
func regionKey(x, y int) string {
	return fmt.Sprintf("z%d/%d/%d", regionZoom, x, y)
}

// regionDir is the object-directory form of a region key: slashes become
// dashes ("z11/352/819" → "z11-352-819", the index.json example's shape).
func regionDir(key string) string {
	return strings.ReplaceAll(key, "/", "-")
}

// parseRegionKey splits a region key back into tile coordinates (tests).
func parseRegionKey(key string) (x, y int, err error) {
	var z int
	if _, err := fmt.Sscanf(key, "z%d/%d/%d", &z, &x, &y); err != nil {
		return 0, 0, fmt.Errorf("bad region key %q", key)
	}
	if z != regionZoom {
		return 0, 0, fmt.Errorf("region key %q: zoom %d, want %d", key, z, regionZoom)
	}
	return x, y, nil
}
