package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// metview_test.go — the aggregation handlers over a tiny two-lane fixture:
// summary derives speed from VMT/VHT, lanes aggregate intervals per lane and
// sort by time loss, trips bucket by vehicle type.

func fixture() *metricsFile {
	var m metricsFile
	m.SchemaVersion = 1
	m.Ticks = 100
	m.Dt = 0.1
	type interval = struct {
		SetID     string  `json:"set_id"`
		LaneID    string  `json:"lane_id"`
		BeginTick uint64  `json:"begin_tick"`
		EndTick   uint64  `json:"end_tick"`
		Partial   bool    `json:"partial"`
		SumDistM  float64 `json:"sum_dist_m"`
		SumTimeS  float64 `json:"sum_time_s"`
		Q         float64 `json:"q"`
		K         float64 `json:"k"`
		Occupancy float64 `json:"occupancy"`
		Stops     float64 `json:"stops"`
		TimeLossS float64 `json:"time_loss_s"`
	}
	m.Intervals = append(m.Intervals,
		// Sink K is veh/m (metricsjson.go:37); the viewer displays veh/km.
		interval{SetID: "default", LaneID: "a_0", EndTick: 100, SumDistM: 1000, SumTimeS: 100, K: 0.01, Occupancy: 0.5, Stops: 2, TimeLossS: 50},
		interval{SetID: "default", LaneID: "b_0", EndTick: 100, SumDistM: 500, SumTimeS: 50, K: 0.04, Occupancy: 0.9, Stops: 9, TimeLossS: 300},
		interval{SetID: "alt", LaneID: "b_0", EndTick: 50, SumDistM: 999, SumTimeS: 99, K: 0.099, Occupancy: 0.99, Stops: 99, TimeLossS: 999},
		// Horizon-partial tail of the default set: dropped per ADR-0014 §3.
		interval{SetID: "default", LaneID: "a_0", BeginTick: 100, EndTick: 137, Partial: true, SumDistM: 777, SumTimeS: 77, K: 0.077, Occupancy: 0.77, Stops: 77, TimeLossS: 777},
	)
	m.Totals.VMT = 1500
	m.Totals.VHT = 150
	m.Totals.CompletedTrips = 3
	return &m
}

func get[T any](t *testing.T, path string, out *T) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/summary", handleSummary)
	mux.HandleFunc("/api/lanes", handleLanes)
	mux.HandleFunc("/api/trips", handleTrips)
	mux.HandleFunc("/", handlePage)
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%s: status %d", path, rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestSummaryDerivesSpeed(t *testing.T) {
	runs = []loadedRun{{name: "f1", m: fixture()}}
	var rows []summaryRow
	get(t, "/api/summary", &rows)
	if len(rows) != 1 || rows[0].MeanSpeedKMH != 36 {
		t.Fatalf("summary = %+v, want mean speed 36 km/h (1500 m / 150 s)", rows)
	}
	if rows[0].VMTkm != 1.5 || rows[0].VHT != 150.0/3600.0 {
		t.Fatalf("summary conversions wrong: %+v", rows[0])
	}
}

func TestLanesAggregateAndSort(t *testing.T) {
	runs = []loadedRun{{name: "f1", m: fixture(), sets: []string{"alt", "default"}}}
	var resp lanesResponse
	get(t, "/api/lanes?file=0&sort=loss", &resp)
	rows := resp.Rows
	if resp.Set != "default" || len(resp.Sets) != 2 {
		t.Fatalf("set selection = %q over %v, want default over [alt default]", resp.Set, resp.Sets)
	}
	if len(rows) != 2 || rows[0].LaneID != "b_0" {
		t.Fatalf("lanes = %+v, want b_0 (300 s loss) first, alt set excluded", rows)
	}
	if rows[0].MeanK != 40 || rows[1].MeanOcc != 0.5 {
		t.Fatalf("window-weighted means wrong: %+v", rows)
	}
	// The horizon-partial interval is dropped (ADR-0014 §3), not merged
	// into a_0's complete window.
	if resp.DroppedPartials != 1 || rows[1].TimeLossS != 50 || rows[1].VMTm != 1000 {
		t.Fatalf("partial handling wrong: dropped=%d rows=%+v", resp.DroppedPartials, rows)
	}
	// The alt set aggregates independently (its 999 s loss must not leak
	// into the default set, and selects on request).
	get(t, "/api/lanes?file=0&set=alt", &resp)
	if len(resp.Rows) != 1 || resp.Rows[0].TimeLossS != 999 || resp.Rows[0].MeanK != 99 {
		t.Fatalf("alt set = %+v, want the single alt interval", resp.Rows)
	}
}

func TestFileIdxClamps(t *testing.T) {
	runs = []loadedRun{{name: "f1", m: fixture()}}
	req := httptest.NewRequest("GET", "/api/lanes?file=99", nil)
	if got := fileIdx(req); got != 0 {
		t.Fatalf("fileIdx(99) = %d, want clamped 0", got)
	}
}

func TestTripsCompletedOnlyMeans(t *testing.T) {
	m := fixture()
	m.Trips = append(m.Trips,
		struct {
			VehicleID uint64  `json:"vehicle_id"`
			Type      string  `json:"type"`
			EntryTick uint64  `json:"entry_tick"`
			ExitTick  uint64  `json:"exit_tick"`
			DistanceM float64 `json:"distance_m"`
			TimeLossS float64 `json:"time_loss_s"`
			Stops     int     `json:"stops"`
			Completed bool    `json:"completed"`
		}{VehicleID: 1, Type: "car", EntryTick: 0, ExitTick: 100, DistanceM: 1000, TimeLossS: 10, Stops: 2, Completed: true},
		struct {
			VehicleID uint64  `json:"vehicle_id"`
			Type      string  `json:"type"`
			EntryTick uint64  `json:"entry_tick"`
			ExitTick  uint64  `json:"exit_tick"`
			DistanceM float64 `json:"distance_m"`
			TimeLossS float64 `json:"time_loss_s"`
			Stops     int     `json:"stops"`
			Completed bool    `json:"completed"`
		}{VehicleID: 2, Type: "car", EntryTick: 90, ExitTick: 100, DistanceM: 50, TimeLossS: 1, Stops: 0, Completed: false},
	)
	runs = []loadedRun{{name: "f1", m: m}}
	var rows []tripBucket
	get(t, "/api/trips?file=0", &rows)
	if len(rows) != 1 {
		t.Fatalf("trips = %+v, want one car bucket", rows)
	}
	b := rows[0]
	if b.Count != 2 || b.Completed != 1 || b.Censored != 1 {
		t.Fatalf("counts = %+v, want 2 total / 1 done / 1 censored", b)
	}
	// Means cover the completed trip only: 1000 m over 100 ticks at dt 0.1
	// = 10 s → 360 km/h; censored 50 m trip must not dilute them.
	if b.MeanDistM != 1000 || b.MeanTimeS != 10 || b.MeanLossS != 10 || b.MeanStops != 2 {
		t.Fatalf("means = %+v, want completed-only (1000 m, 10 s, 10 s loss, 2 stops)", b)
	}
	if b.MeanSpeedKMH != 360 {
		t.Fatalf("mean speed = %v, want 360 km/h", b.MeanSpeedKMH)
	}
}

func TestPageRendersServerSide(t *testing.T) {
	runs = []loadedRun{{name: "f1", m: fixture(), sets: []string{"alt", "default"}}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/?file=0&sort=loss", nil)
	handlePage(rec, req)
	if rec.Code != 200 {
		t.Fatalf("page: status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"b_0", "f1", "horizon-partial", "time loss"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
	// Server-rendered: no script at all (ADR-0001 — nothing for it to bite).
	if strings.Contains(body, "<script") {
		t.Fatal("page contains browser-side code")
	}
}

func TestCheckLoopback(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:8910", "localhost:8910", "[::1]:8910", "127.0.1.2:8910"} {
		if err := checkLoopback(ok); err != nil {
			t.Errorf("checkLoopback(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{":8910", "0.0.0.0:8910", "127.example.com:8910", "example.com:8910"} {
		if err := checkLoopback(bad); err == nil {
			t.Errorf("checkLoopback(%q) = nil, want refusal", bad)
		}
	}
}
