package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// proj.go — projection from the network's local metric frame to WGS84
// (lng, lat), a Go port of the viz's proj.ts (the bake exports WGS84
// GeoJSON for tippecanoe and assigns z11 region tiles from projected
// positions, ADR-0023; the live export at geojson.go stays metric). The
// frame descriptor (projection + netOffset, network-format v1 provenance)
// tells how:
//
//	absolute UTM = local − netOffset      (SUMO netOffset convention)
//	WGS84        = inverse UTM(zone) of the absolute coordinate
//
// Only "+proj=utm +zone=N" on WGS84 is supported — every netimport network
// today (netconvert --proj.utm). The inverse transverse Mercator is the
// standard Snyder/USGS series, good to well under a millimetre here; the
// constants and series mirror proj.ts term for term so both sides place a
// coordinate identically.

const (
	wgs84A  = 6378137.0
	wgs84E2 = 6.69437999014e-3
	utmK0   = 0.9996

	utmFalseEasting = 500000.0
)

// LocalFrame describes a network's local metric frame (network-format v1
// provenance fields).
type LocalFrame struct {
	Projection string
	NetOffset  [2]float64
}

// Projector maps a local-frame (x, y) to WGS84 (lng, lat) in degrees.
type Projector func(x, y float64) (lng, lat float64)

// parseUTMZone extracts the zone from a "+proj=utm +zone=N" string
// (northern hemisphere only).
func parseUTMZone(projection string) (int, error) {
	isUTM := false
	zone := -1
	for _, tok := range strings.Fields(projection) {
		switch {
		case tok == "+proj=utm":
			isUTM = true
		case strings.HasPrefix(tok, "+zone="):
			z, err := strconv.Atoi(strings.TrimPrefix(tok, "+zone="))
			if err != nil {
				return 0, fmt.Errorf("proj: bad zone in %q", projection)
			}
			zone = z
		case tok == "+south":
			return 0, fmt.Errorf("proj: southern-hemisphere UTM not supported: %s", projection)
		}
	}
	if !isUTM || zone < 1 || zone > 60 {
		return 0, fmt.Errorf("proj: only \"+proj=utm +zone=N (1..60)\" is supported, got: %s", projection)
	}
	return zone, nil
}

// MakeProjector compiles a frame descriptor into a local(x,y) → (lng, lat)
// function (degrees). Errors on unsupported projections.
func MakeProjector(f LocalFrame) (Projector, error) {
	zone, err := parseUTMZone(f.Projection)
	if err != nil {
		return nil, err
	}
	lon0 := float64((zone-1)*6-180+3) * (math.Pi / 180) // central meridian
	offX, offY := f.NetOffset[0], f.NetOffset[1]
	e2 := wgs84E2
	ep2 := e2 / (1 - e2)
	sq := math.Sqrt(1 - e2)
	e1 := (1 - sq) / (1 + sq)
	meridional := wgs84A * (1 - e2/4 - (3*e2*e2)/64 - (5*e2*e2*e2)/256)

	return func(x, y float64) (float64, float64) {
		easting := x - offX
		northing := y - offY
		dE := easting - utmFalseEasting
		mu := northing / utmK0 / meridional
		// Footpoint latitude (Snyder series).
		phi1 := mu +
			(3*e1/2-(27*e1*e1*e1)/32)*math.Sin(2*mu) +
			(21*e1*e1/16-(55*e1*e1*e1*e1)/32)*math.Sin(4*mu) +
			(151*e1*e1*e1/96)*math.Sin(6*mu) +
			(1097*e1*e1*e1*e1/512)*math.Sin(8*mu)
		sin1, cos1 := math.Sin(phi1), math.Cos(phi1)
		tan1 := math.Tan(phi1)
		n1 := wgs84A / math.Sqrt(1-e2*sin1*sin1)
		r1 := (wgs84A * (1 - e2)) / math.Pow(1-e2*sin1*sin1, 1.5)
		t1 := tan1 * tan1
		c1 := ep2 * cos1 * cos1
		d := dE / (n1 * utmK0)
		lat := phi1 -
			(n1*tan1)/r1*
				(d*d/2-
					((5+3*t1+10*c1-4*c1*c1-9*ep2)*math.Pow(d, 4))/24+
					((61+90*t1+298*c1+45*t1*t1-252*ep2-3*c1*c1)*math.Pow(d, 6))/720)
		lon := lon0 +
			(d-
				((1+2*t1+c1)*math.Pow(d, 3))/6+
				((5-2*c1+28*t1-3*c1*c1+8*ep2+24*t1*t1)*math.Pow(d, 5))/120)/
				cos1
		deg := 180 / math.Pi
		return lon * deg, lat * deg
	}, nil
}
