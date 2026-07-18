package natsio

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"traffic-sim/engine"
)

// Frame round-trip: encode from a live engine, decode, compare fields.
func TestFrameRoundTrip(t *testing.T) {
	spec, err := engine.DefaultSpec("lanedrop", 120, 2)
	if err != nil {
		t.Fatal(err)
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	for e.Tick < 120 {
		e.Step()
	}
	if len(e.Vehicles()) == 0 {
		t.Fatal("no vehicles to snapshot")
	}
	buf := SnapshotFrame(e, LaneGeoms(e.Net))
	wantLen := frameHeader + framePerVeh*len(e.Vehicles())
	if len(buf) != wantLen {
		t.Fatalf("frame is %d bytes, want %d for %d vehicles", len(buf), wantLen, len(e.Vehicles()))
	}
	f, err := ParseFrame(buf)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if f.Tick != e.Tick {
		t.Fatalf("frame tick = %d, want %d", f.Tick, e.Tick)
	}
	if len(f.Vehicles) != len(e.Vehicles()) {
		t.Fatalf("frame has %d vehicles, want %d", len(f.Vehicles), len(e.Vehicles()))
	}
	first := e.Vehicles()[0]
	got := f.Vehicles[0]
	if got.ID != first.ID {
		t.Fatalf("first vehicle id = %d, want %d", got.ID, first.ID)
	}
	if got.Class != float32(first.TypeIdx) {
		t.Fatalf("first vehicle class = %v, want %v", got.Class, first.TypeIdx)
	}
	// Placeholder projection: y is a 3.5 m slot, x is offset arc length.
	g := LaneGeoms(e.Net)[first.Lane.Index]
	if got.Y != float32(g.Y) || got.Angle != 0 {
		t.Fatalf("first vehicle y/angle = %v/%v, want %v/0", got.Y, got.Angle, g.Y)
	}
	t.Logf("frame: %d vehicles, %d bytes (%.1f B/vehicle)", len(f.Vehicles), len(buf),
		float64(len(buf))/float64(len(f.Vehicles)))
}

// Snapshot over a lane with a real polyline (network-format v1): x/y/angle
// are the polyline projection — the point lies on the lane shape and the
// angle matches the local tangent. TSSF stays v1 (values-only change).
func TestFrameRealGeometry(t *testing.T) {
	nf := &engine.NetFile{
		Version: 1,
		Lanes: []engine.NetLane{{
			ID: "L0", Section: "L0", Length: 200, SpeedLimit: 13.9, Width: 3.5,
			Shape:  [][2]float64{{0, 0}, {100, 0}, {100, 100}},
			Origin: true, Exit: true,
		}},
	}
	data, err := json.Marshal(nf)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "l.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	spec := engine.RunSpec{
		Net:    engine.NetSpec{Kind: "file", Path: path},
		Params: engine.DefaultParams(),
		Seed:   1,
	}
	e, err := engine.NewEngine(spec)
	if err != nil {
		t.Fatal(err)
	}
	lane := e.Net.LaneByID("L0")
	v1 := e.AddInitialVehicle(lane, 0, 50, 10, 1)  // first (straight) segment
	v2 := e.AddInitialVehicle(lane, 0, 150, 10, 1) // past the corner

	f, err := ParseFrame(SnapshotFrame(e, LaneGeoms(e.Net)))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Vehicles) != 2 {
		t.Fatalf("%d frame vehicles, want 2", len(f.Vehicles))
	}
	check := func(fv FrameVehicle, id uint64, x, y, angle float32) {
		t.Helper()
		if fv.ID != id {
			t.Fatalf("frame vehicle id = %d, want %d", fv.ID, id)
		}
		if math.Abs(float64(fv.X-x)) > 1e-4 || math.Abs(float64(fv.Y-y)) > 1e-4 || math.Abs(float64(fv.Angle-angle)) > 1e-4 {
			t.Errorf("vehicle %d (x,y,angle) = (%v,%v,%v), want (%v,%v,%v)",
				id, fv.X, fv.Y, fv.Angle, x, y, angle)
		}
	}
	check(f.Vehicles[0], v1.ID, 50, 0, 0)
	check(f.Vehicles[1], v2.ID, 100, 50, float32(math.Pi/2))
}

// Truncated/foreign payloads must be rejected.
func TestFrameRejectsGarbage(t *testing.T) {
	if _, err := ParseFrame([]byte("short")); err == nil {
		t.Fatal("short frame accepted")
	}
	spec, _ := engine.DefaultSpec("ring", 1, 1)
	e, _ := engine.NewEngine(spec)
	buf := SnapshotFrame(e, LaneGeoms(e.Net))
	if _, err := ParseFrame(buf[:len(buf)-1]); err == nil {
		t.Fatal("truncated frame accepted")
	}
	buf[0] ^= 0xff
	if _, err := ParseFrame(buf); err == nil {
		t.Fatal("bad magic accepted")
	}
}

// Intent codec round-trip, including the absent-accel form and NaN/Inf
// rejection at the boundary.
func TestIntentCodec(t *testing.T) {
	in := engine.Intent{VehicleID: 42, Accel: -2.5, AccelSet: true, LaneDelta: -1}
	got, ok := DecodeIntent(EncodeIntent(in))
	if !ok || got != in {
		t.Fatalf("round trip = %+v (ok=%v), want %+v", got, ok, in)
	}
	noAccel := engine.Intent{VehicleID: 7, LaneDelta: 1}
	got, ok = DecodeIntent(EncodeIntent(noAccel))
	if !ok || got != noAccel {
		t.Fatalf("round trip = %+v (ok=%v), want %+v", got, ok, noAccel)
	}
	if _, ok := DecodeIntent([]byte("tiny")); ok {
		t.Fatal("short intent accepted")
	}
	bad := engine.Intent{VehicleID: 1, Accel: math.NaN(), AccelSet: true}
	if _, ok := DecodeIntent(EncodeIntent(bad)); ok {
		t.Fatal("NaN accel accepted")
	}
	bad.Accel = math.Inf(1)
	if _, ok := DecodeIntent(EncodeIntent(bad)); ok {
		t.Fatal("+Inf accel accepted")
	}
}

// Run ids and controller ids are single tokens — dots/wildcards would
// break the taxonomy (ADR-0006 §3).
func TestTokenValidation(t *testing.T) {
	srv := NewTestServer(t)
	nc, js := srv.JetStream(t)
	spec, _ := engine.DefaultSpec("ring", 1, 1)
	if _, err := RunLive(nc, js, "bad.run", spec, RecorderConfig{}); err == nil {
		t.Fatal("run id with a dot accepted")
	}
	if _, err := NewRecorder(js, "bad>run", RecorderConfig{}); err == nil {
		t.Fatal("run id with a wildcard accepted")
	}
}
