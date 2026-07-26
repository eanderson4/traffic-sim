package engine

import (
	"math"
	"testing"
)

// The projector is a term-for-term port of viz/src/proj.ts; these oracles
// pin it against known UTM↔WGS84 pairs.

func TestMakeProjectorCentralMeridian(t *testing.T) {
	// Zone 10 central meridian is 123°W; the false easting sits on it and
	// northing 0 is the equator.
	p, err := MakeProjector(LocalFrame{Projection: "+proj=utm +zone=10"})
	if err != nil {
		t.Fatal(err)
	}
	lng, lat := p(utmFalseEasting, 0)
	if math.Abs(lng-(-123)) > 1e-9 || math.Abs(lat) > 1e-9 {
		t.Fatalf("central meridian: got (%v, %v), want (-123, 0)", lng, lat)
	}
}

func TestMakeProjectorKnownPoint(t *testing.T) {
	// Reference pair computed with PROJ (pyproj 3): zone 10,
	// lng -122.0, lat 38.0 → easting 587798.4178, northing 4206286.7581.
	// The inverse series must land back within ~1 cm.
	p, err := MakeProjector(LocalFrame{Projection: "+proj=utm +zone=10"})
	if err != nil {
		t.Fatal(err)
	}
	lng, lat := p(587798.4178348783, 4206286.758051956)
	if math.Abs(lng-(-122.0)) > 1e-7 || math.Abs(lat-38.0) > 1e-7 {
		t.Fatalf("known point: got (%v, %v), want (-122, 38)", lng, lat)
	}
}

func TestMakeProjectorNetOffset(t *testing.T) {
	// netOffset shifts the local frame: local (0,0) is absolute
	// (−offX, −offY) (SUMO convention), so a local coordinate equals the
	// absolute one minus the offset.
	abs, err := MakeProjector(LocalFrame{Projection: "+proj=utm +zone=10"})
	if err != nil {
		t.Fatal(err)
	}
	off, err := MakeProjector(LocalFrame{
		Projection: "+proj=utm +zone=10",
		NetOffset:  [2]float64{-547000, -4185000},
	})
	if err != nil {
		t.Fatal(err)
	}
	lx, ly := 1200.0, 3400.0
	wantLng, wantLat := abs(lx+547000, ly+4185000)
	gotLng, gotLat := off(lx, ly)
	if gotLng != wantLng || gotLat != wantLat {
		t.Fatalf("netOffset: got (%v, %v), want (%v, %v)", gotLng, gotLat, wantLng, wantLat)
	}
}

func TestMakeProjectorRejects(t *testing.T) {
	for _, proj := range []string{
		"+proj=merc",
		"+proj=utm",                 // no zone
		"+proj=utm +zone=0",         // out of range
		"+proj=utm +zone=61",        // out of range
		"+proj=utm +zone=10 +south", // southern hemisphere unsupported
	} {
		if _, err := MakeProjector(LocalFrame{Projection: proj}); err == nil {
			t.Fatalf("projection %q: want error, got nil", proj)
		}
	}
}
