package engine

import (
	"math"
	"testing"
)

// Projection sanity (network-format v1): the projected (x, y) lies on the
// lane polyline and the angle matches the local tangent.

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

// Straight +x lane: interpolation, endpoint clamping, zero angle.
func TestProjectStraight(t *testing.T) {
	l := &Lane{ID: "L", Length: 100}
	l.SetShape([]Point{{0, 3.25}, {100, 3.25}}, 0)

	x, y, angle, ok := l.Project(50)
	if !ok {
		t.Fatal("Project not ok on shaped lane")
	}
	near(t, "x", x, 50)
	near(t, "y", y, 3.25)
	near(t, "angle", angle, 0)

	// Clamping: before the start and past the end sit on the endpoints.
	x, y, _, _ = l.Project(-5)
	near(t, "x(s<0)", x, 0)
	near(t, "y(s<0)", y, 3.25)
	x, y, _, _ = l.Project(250)
	near(t, "x(s>len)", x, 100)
	near(t, "y(s>len)", y, 3.25)
}

// L-shaped lane: past the corner the point is on the second segment and the
// angle is that segment's tangent (+y ⇒ π/2).
func TestProjectCorner(t *testing.T) {
	l := &Lane{ID: "L", Length: 200}
	l.SetShape([]Point{{0, 0}, {100, 0}, {100, 100}}, 0)

	x, y, angle, ok := l.Project(150)
	if !ok {
		t.Fatal("not ok")
	}
	near(t, "x", x, 100)
	near(t, "y", y, 50)
	near(t, "angle", angle, math.Pi/2)

	// Exactly at the corner (s = 100): end of the first segment, angle 0.
	_, _, angle, _ = l.Project(100)
	near(t, "angle(corner)", angle, 0)
}

// Lateral offset is left-positive: +x travel offsets toward +y, +y travel
// toward −x.
func TestProjectLateralOffset(t *testing.T) {
	l := &Lane{ID: "L", Length: 200}
	l.SetShape([]Point{{0, 0}, {100, 0}, {100, 100}}, 1.75)

	x, y, _, _ := l.Project(50)
	near(t, "x", x, 50)
	near(t, "y(offset left of +x)", y, 1.75)
	x, y, _, _ = l.Project(150)
	near(t, "x(offset left of +y)", x, 100-1.75)
	near(t, "y", y, 50)
}

// When the polyline arc length differs from Lane.Length, s maps
// proportionally so the lane end lands on the polyline end.
func TestProjectScaled(t *testing.T) {
	l := &Lane{ID: "L", Length: 50}
	l.SetShape([]Point{{0, 0}, {100, 0}}, 0)

	x, _, _, _ := l.Project(25)
	near(t, "x", x, 50)
	x, _, _, _ = l.Project(50)
	near(t, "x(end)", x, 100)
}

// No polyline: not ok. A single point — literal, or all-coincident after
// SetShape's duplicate drop — is a VALID degenerate shape (zero-length
// junction-internal lane): sit on the point, face +x, ok=true. Clearing it
// would fall to the placeholder projection and teleport the lane's
// vehicles off-network on the wire.
func TestProjectNoShape(t *testing.T) {
	l := &Lane{ID: "L", Length: 100}
	if _, _, _, ok := l.Project(10); ok {
		t.Fatal("ok on shapeless lane")
	}
	l.SetShape([]Point{{1, 2}}, 0) // literal 1-point shape: kept
	x, y, angle, ok := l.Project(10)
	if !ok {
		t.Fatal("not ok on 1-point shape")
	}
	near(t, "x", x, 1)
	near(t, "y", y, 2)
	near(t, "angle", angle, 0)
	l.SetShape([]Point{{1, 2}, {1, 2}, {1, 2}}, 0) // dedups to the same point
	x, y, angle, ok = l.Project(10)
	if !ok {
		t.Fatal("not ok on all-coincident shape")
	}
	near(t, "x", x, 1)
	near(t, "y", y, 2)
	near(t, "angle", angle, 0)
}

// A consecutive duplicate point (netconvert emits these; 4/375 lanes on the
// I-280 import) must not create a zero-length segment whose tangent reports
// due east: the tangent is correct on both sides of the dropped point.
func TestProjectConsecutiveDuplicate(t *testing.T) {
	l := &Lane{ID: "L", Length: 200}
	l.SetShape([]Point{{0, 0}, {100, 0}, {100, 0}, {100, 100}}, 0)

	// Before the dup: first segment, tangent +x.
	x, y, angle, ok := l.Project(50)
	if !ok {
		t.Fatal("not ok")
	}
	near(t, "x", x, 50)
	near(t, "y", y, 0)
	near(t, "angle", angle, 0)

	// After the dup: second segment, tangent +y — no east-pointing frame.
	x, y, angle, ok = l.Project(150)
	if !ok {
		t.Fatal("not ok")
	}
	near(t, "x", x, 100)
	near(t, "y", y, 50)
	near(t, "angle", angle, math.Pi/2)
}
