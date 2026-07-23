package engine

import (
	"bytes"
	"encoding/json"
	"sort"
	"testing"
)

// metricsJSONDoc runs one lanedrop run with a kernel and marshals its metric
// output, returning the raw bytes and the decoded generic document.
func metricsJSONDoc(t *testing.T, spec RunSpec) ([]byte, map[string]any) {
	t.Helper()
	_, k := runWithKernel(t, spec, defaultCfg)
	var buf bytes.Buffer
	if err := WriteMetricsJSON(&buf, k, spec.Ticks); err != nil {
		t.Fatalf("WriteMetricsJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	return buf.Bytes(), doc
}

// Round-trip: the document shape, snake_case field names, omitempty pointer
// fields, and the sorted denied-by-lane array (ADR-0014 §6(b) file sink).
func TestWriteMetricsJSONRoundTrip(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 800, 42)
	if err != nil {
		t.Fatal(err)
	}
	spec.Scen.SpawnRatePerLaneHour = 3000
	spec.Scen.DensityTargetPerKm = 80 // overload ⇒ denied demand on A0/A1/A2
	_, doc := metricsJSONDoc(t, spec)

	if doc["schema_version"] != 1.0 {
		t.Errorf("schema_version = %v, want 1", doc["schema_version"])
	}
	if doc["ticks"] != 800.0 || doc["dt"] != 0.1 {
		t.Errorf("ticks/dt = %v/%v, want 800/0.1", doc["ticks"], doc["dt"])
	}

	intervals, ok := doc["intervals"].([]any)
	if !ok || len(intervals) == 0 {
		t.Fatalf("intervals missing or empty: %v", doc["intervals"])
	}
	rec := intervals[0].(map[string]any)
	for _, key := range []string{"schema_version", "set_id", "lane_id", "begin_tick", "end_tick",
		"partial", "sum_dist_m", "sum_time_s", "q", "k", "v", "occupancy", "stops", "time_loss_s"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("interval record missing key %q (keys: %v)", key, rec)
		}
	}
	for _, key := range []string{"setId", "laneId", "beginTick", "sumDistM", "timeLossS"} {
		if _, ok := rec[key]; ok {
			t.Errorf("interval record has camelCase key %q — wire names are snake_case", key)
		}
	}

	trips, ok := doc["trips"].([]any)
	if !ok || len(trips) == 0 {
		t.Fatalf("trips missing or empty: %v", doc["trips"])
	}
	tr := trips[0].(map[string]any)
	for _, key := range []string{"schema_version", "vehicle_id", "type", "origin_lane", "dest_lane",
		"entry_tick", "exit_tick", "distance_m", "time_loss_s", "stops", "stopped_time_s", "completed"} {
		if _, ok := tr[key]; !ok {
			t.Errorf("trip record missing key %q (keys: %v)", key, tr)
		}
	}

	tot := doc["totals"].(map[string]any)
	for _, key := range []string{"completed_trips", "active_at_horizon", "vmt", "vht",
		"total_time_loss_s", "mean_time_loss_s", "denied_wait_s", "denied_pending", "denied_by_lane"} {
		if _, ok := tot[key]; !ok {
			t.Errorf("totals missing key %q (keys: %v)", key, tot)
		}
	}
	denied, ok := tot["denied_by_lane"].([]any)
	if !ok {
		t.Fatalf("denied_by_lane is %T, want an array (never a bare map)", tot["denied_by_lane"])
	}
	if len(denied) == 0 {
		t.Fatal("denied_by_lane empty under overload")
	}
	var got []string
	for _, d := range denied {
		entry := d.(map[string]any)
		for _, key := range []string{"lane", "wait_s", "pending"} {
			if _, ok := entry[key]; !ok {
				t.Errorf("denied entry missing key %q (keys: %v)", key, entry)
			}
		}
		got = append(got, entry["lane"].(string))
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("denied_by_lane not sorted by lane: %v", got)
	}
}

// Pointer fields are omitted, never zero-filled/null (ADR-0014 §2): on a
// lane with zero traffic, SumTimeS == 0 ⇒ no "v" key — while q/k/occupancy
// (defined, exactly 0) stay present.
func TestWriteMetricsJSONOmitsUndefinedV(t *testing.T) {
	spec, err := DefaultSpec("straight", 100, 1) // no demand: empty lane
	if err != nil {
		t.Fatal(err)
	}
	_, doc := metricsJSONDoc(t, spec)
	intervals := doc["intervals"].([]any)
	if len(intervals) == 0 {
		t.Fatal("no interval records")
	}
	for _, r := range intervals {
		rec := r.(map[string]any)
		if rec["sum_time_s"] != 0.0 {
			t.Fatalf("expected an empty lane, got sum_time_s = %v", rec["sum_time_s"])
		}
		if _, ok := rec["v"]; ok {
			t.Errorf("empty-lane record has v = %v — must be omitted, never zero-filled", rec["v"])
		}
		for _, key := range []string{"q", "k", "occupancy"} {
			if _, ok := rec[key]; !ok {
				t.Errorf("empty-lane record missing %q — defined zeros stay present", key)
			}
		}
	}
}

// Byte determinism: two same-seed runs marshal to byte-identical documents
// (the file sink preserves the kernel's determinism — ADR-0014 §1/§6).
func TestWriteMetricsJSONDeterminism(t *testing.T) {
	spec, err := DefaultSpec("lanedrop", 800, 42)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := metricsJSONDoc(t, spec)
	b2, _ := metricsJSONDoc(t, spec)
	if !bytes.Equal(b1, b2) {
		t.Fatal("same-seed runs produced different metric JSON")
	}
}
