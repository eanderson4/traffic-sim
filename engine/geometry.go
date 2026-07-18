package engine

import (
	"math"
	"sort"
)

// geometry.go — polyline geometry for lanes (arch-road-graph-model #4:
// metric local coordinates, north-up; (laneId, s) is the universal address;
// polyline s↔xy projection code written once, here). Geometry is display
// data: it never feeds back into the dynamics, so the CRC is unaffected.

// Point is a 2D coordinate in the network's local metric frame (meters,
// north-up, +x east). The frame's provenance (projection, offset) rides in
// the network file, not on each point.
type Point struct {
	X, Y float64
}

// SetShape attaches a per-lane centerline polyline with a constant lateral
// offset (meters, positive = left of the direction of travel; 0 for the
// per-lane centerlines netconvert emits) and precomputes the arc-length
// table used by Project. Fewer than 2 points clears the shape (the lane
// falls back to the placeholder projection).
func (l *Lane) SetShape(pts []Point, latOffset float64) {
	if len(pts) < 2 {
		l.Shape, l.cum, l.LatOffset = nil, nil, 0
		return
	}
	l.Shape = pts
	l.LatOffset = latOffset
	l.cum = make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		l.cum[i] = l.cum[i-1] + math.Hypot(pts[i].X-pts[i-1].X, pts[i].Y-pts[i-1].Y)
	}
}

// Project maps a longitudinal position s (front-bumper arc length on the
// lane) to (x, y, angle): linear interpolation along the lane polyline,
// angle = segment tangent in radians (atan2 convention: 0 = +x/east,
// counter-clockwise positive, north-up). The point is then shifted
// perpendicular-left by LatOffset. s is clamped to the lane; when the
// polyline's arc length differs from Lane.Length (SUMO sometimes shortens
// lanes at junctions), s maps proportionally so the lane end always lands
// on the polyline end. ok=false when the lane has no usable polyline.
func (l *Lane) Project(s float64) (x, y, angle float64, ok bool) {
	n := len(l.Shape)
	if n < 2 {
		return 0, 0, 0, false
	}
	shapeLen := l.cum[n-1]
	if shapeLen == 0 {
		// Degenerate all-coincident polyline: sit on the point, face +x.
		return l.Shape[0].X, l.Shape[0].Y, 0, true
	}
	arc := s
	if l.Length > 0 {
		arc = s * shapeLen / l.Length
	}
	if arc <= 0 {
		x, y, angle = interpSegment(l.Shape[0], l.Shape[1], 0)
		return x - l.LatOffset*math.Sin(angle), y + l.LatOffset*math.Cos(angle), angle, true
	}
	if arc >= shapeLen {
		x, y, angle = interpSegment(l.Shape[n-2], l.Shape[n-1], 1)
		return x - l.LatOffset*math.Sin(angle), y + l.LatOffset*math.Cos(angle), angle, true
	}
	i := sort.Search(n-1, func(i int) bool { return l.cum[i+1] >= arc })
	segLen := l.cum[i+1] - l.cum[i]
	t := 0.0
	if segLen > 0 {
		t = (arc - l.cum[i]) / segLen
	}
	x, y, angle = interpSegment(l.Shape[i], l.Shape[i+1], t)
	return x - l.LatOffset*math.Sin(angle), y + l.LatOffset*math.Cos(angle), angle, true
}

// interpSegment linearly interpolates between a and b at t ∈ [0,1] and
// returns the segment tangent (0 = +x, CCW positive). A zero-length segment
// reports angle 0.
func interpSegment(a, b Point, t float64) (x, y, angle float64) {
	x = a.X + t*(b.X-a.X)
	y = a.Y + t*(b.Y-a.Y)
	dx, dy := b.X-a.X, b.Y-a.Y
	if dx == 0 && dy == 0 {
		return x, y, 0
	}
	return x, y, math.Atan2(dy, dx)
}
