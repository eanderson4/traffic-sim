// metview is a read-only viewer for M13 metric-kernel JSON output
// (serve/simrun -metrics-out, ADR-0014 §6). It loads one or more metrics
// files — typically a baseline plus upgrade variants of the same scenario —
// and serves a local comparison dashboard: run totals side by side, the
// worst lanes by time loss / density / flow, and trip-time breakdowns by
// vehicle type. The page is SERVER-RENDERED (html/template, link controls)
// — no browser-side code, so ADR-0001's TypeScript-for-web-clients
// invariant has nothing to bite on. /api/* serves the same data as JSON
// for human-local scripting; this is a standalone loopback-only dev tool
// reading files, not an inter-service boundary — the ADR-0002 addendum's
// loopback carve-out, same shape as cmd/replay's HTTP control plane.
//
//	metview -addr 127.0.0.1:8910 base.metrics.json meter.metrics.json
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed page.tmpl
var pageTmpl string

// metricsFile mirrors the simrun file-sink schema v1 (engine/metricsjson.go).
// Hand-duplicated by design (read-only dev tool); a sink rename zeroes
// fields here — check against a real file when the sink schema changes.
// Known limitation: the sink emits interval q/k/occupancy/time_loss_s as
// *float64 and stops as *int, all omitempty (absent when the measurement
// group is off); the scalar fields here decode absence as 0,
// indistinguishable from a true zero. Every current binding enables all
// groups — if a group-off file ever appears, these need pointers and the
// UI needs to suppress the column.
type metricsFile struct {
	SchemaVersion int `json:"schema_version"`
	Ticks         uint64
	Dt            float64
	// Intervals are per-(set, lane) Edie q/k/u accumulations over a tick
	// window; a run can carry several overlapping measurement sets. The
	// horizon-truncated final window is flagged partial — ADR-0014 §3
	// requires comparison tooling to drop partials.
	Intervals []struct {
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
	} `json:"intervals"`
	Trips []struct {
		VehicleID uint64  `json:"vehicle_id"`
		Type      string  `json:"type"`
		EntryTick uint64  `json:"entry_tick"`
		ExitTick  uint64  `json:"exit_tick"`
		DistanceM float64 `json:"distance_m"`
		TimeLossS float64 `json:"time_loss_s"`
		Stops     int     `json:"stops"`
		Completed bool    `json:"completed"`
	} `json:"trips"`
	Totals struct {
		CompletedTrips  int     `json:"completed_trips"`
		ActiveAtHorizon int     `json:"active_at_horizon"`
		VMT             float64 `json:"vmt"`
		VHT             float64 `json:"vht"`
		TotalTimeLossS  float64 `json:"total_time_loss_s"`
		MeanTimeLossS   float64 `json:"mean_time_loss_s"`
		DeniedWaitS     float64 `json:"denied_wait_s"`
		DeniedPending   float64 `json:"denied_pending"` // fractional vehicle backlog
		DeniedServed    int     `json:"denied_served"`
		// ADR-0014's loud integrity signal for unresolved movement
		// attribution — surfaced in the summary so a corrupted run
		// can't pass as a clean comparison.
		DroppedCrossings int `json:"dropped_crossings"`
	} `json:"totals"`
}

type loadedRun struct {
	name string
	m    *metricsFile
	sets []string // measurement set ids, sorted
}

var runs []loadedRun

func main() {
	addr := flag.String("addr", "127.0.0.1:8910", "listen address (loopback only)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: metview [-addr host:port] metrics.json [more.metrics.json ...]")
		os.Exit(2)
	}
	if err := checkLoopback(*addr); err != nil {
		fmt.Fprintln(os.Stderr, "metview:", err)
		os.Exit(2)
	}
	for _, path := range flag.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "metview:", err)
			os.Exit(1)
		}
		var m metricsFile
		if err := json.Unmarshal(data, &m); err != nil {
			fmt.Fprintf(os.Stderr, "metview: %s: %v\n", path, err)
			os.Exit(1)
		}
		if m.SchemaVersion != 1 {
			fmt.Fprintf(os.Stderr, "metview: %s: unsupported schema_version %d (this build reads v1)\n", path, m.SchemaVersion)
			os.Exit(1)
		}
		name := filepath.Base(path)
		sets := map[string]bool{}
		for _, iv := range m.Intervals {
			sets[iv.SetID] = true
		}
		lr := loadedRun{name: name, m: &m}
		for s := range sets {
			lr.sets = append(lr.sets, s)
		}
		sort.Strings(lr.sets)
		runs = append(runs, lr)
		fmt.Printf("metview: %s — %d intervals over %v, %d trips, %d ticks\n",
			name, len(m.Intervals), lr.sets, len(m.Trips), m.Ticks)
		if m.Ticks != runs[0].m.Ticks || m.Dt != runs[0].m.Dt {
			fmt.Fprintf(os.Stderr, "metview: WARNING: %s has ticks=%d dt=%g but %s has ticks=%d dt=%g — cross-run comparison may be invalid\n",
				name, m.Ticks, m.Dt, runs[0].name, runs[0].m.Ticks, runs[0].m.Dt)
		}
	}

	http.HandleFunc("/", handlePage)
	http.HandleFunc("/api/summary", handleSummary)
	http.HandleFunc("/api/lanes", handleLanes)
	http.HandleFunc("/api/trips", handleTrips)
	fmt.Printf("metview: http://%s\n", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		fmt.Fprintln(os.Stderr, "metview:", err)
		os.Exit(1)
	}
}

// checkLoopback refuses non-loopback bind addresses: the viewer is
// unauthenticated. "localhost" is accepted by convention; anything else
// must parse as a loopback IP (a DNS name like "127.example.tld" is not
// one). Same shape as cmd/replay's guard.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("-addr: %w", err)
	}
	if host == "" {
		return fmt.Errorf("-addr: empty host binds ALL interfaces — name a loopback host (e.g. 127.0.0.1:8910)")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("-addr: %q is not a loopback address", host)
}

// summaryRow is one run's headline numbers for the comparison table.
type summaryRow struct {
	Name            string  `json:"name"`
	Ticks           uint64  `json:"ticks"`
	Dt              float64 `json:"dt"`
	CompletedTrips  int     `json:"completed_trips"`
	ActiveAtHorizon int     `json:"active_at_horizon"`
	VMTkm           float64 `json:"vmt_km"`
	VHT             float64 `json:"vht_h"`
	MeanSpeedKMH    float64 `json:"mean_speed_kmh"`
	TotalTimeLossH  float64 `json:"total_time_loss_h"`
	MeanTimeLossS   float64 `json:"mean_time_loss_s"`
	DeniedWaitH     float64 `json:"denied_wait_h"`
	DeniedPending   float64 `json:"denied_pending"`
	// DroppedCrossings is ADR-0014's integrity tripwire: nonzero means the
	// run's lane attribution had unresolved crossings and every derived
	// number on this row deserves suspicion.
	DroppedCrossings int `json:"dropped_crossings"`
}

func computeSummary() []summaryRow {
	out := make([]summaryRow, 0, len(runs))
	for _, lr := range runs {
		t := lr.m.Totals
		row := summaryRow{
			Name: lr.name, Ticks: lr.m.Ticks, Dt: lr.m.Dt,
			CompletedTrips: t.CompletedTrips, ActiveAtHorizon: t.ActiveAtHorizon,
			VMTkm: t.VMT / 1000, VHT: t.VHT / 3600,
			TotalTimeLossH: t.TotalTimeLossS / 3600, MeanTimeLossS: t.MeanTimeLossS,
			DeniedWaitH: t.DeniedWaitS / 3600, DeniedPending: t.DeniedPending,
			DroppedCrossings: t.DroppedCrossings,
		}
		if t.VHT > 0 {
			row.MeanSpeedKMH = t.VMT / t.VHT * 3.6
		}
		out = append(out, row)
	}
	return out
}

func handleSummary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, computeSummary())
}

// laneRow aggregates one lane's intervals within one measurement set.
type laneRow struct {
	LaneID    string  `json:"lane_id"`
	VMTm      float64 `json:"vmt_m"`
	VHTs      float64 `json:"vht_s"`
	TimeLossS float64 `json:"time_loss_s"`
	Stops     float64 `json:"stops"`
	MeanK     float64 `json:"mean_k"` // window-weighted density (veh/km)
	MeanOcc   float64 `json:"mean_occ"`
}

// lanesResponse lists the run's measurement sets plus the selected set's
// worst lanes. Aggregating per (set, lane) keeps overlapping sets from
// double-counting; density/occupancy normalize by the lane's covered
// window, not the run horizon. Horizon-partial intervals are dropped per
// ADR-0014 §3 (counted in DroppedPartials — a run shorter than one full
// window has no complete intervals and its table is legitimately empty).
type lanesResponse struct {
	Sets            []string  `json:"sets"`
	Set             string    `json:"set"`
	DroppedPartials int       `json:"dropped_partials"`
	TotalLanes      int       `json:"total_lanes"` // pre-truncation count (Rows is top-100)
	Rows            []laneRow `json:"rows"`
}

func computeLanes(lr loadedRun, set, sortKey string) lanesResponse {
	if set == "" {
		set = "default"
	}
	found := false
	for _, s := range lr.sets {
		if s == set {
			found = true
			break
		}
	}
	if !found && len(lr.sets) > 0 {
		set = lr.sets[0]
	}
	type accum struct {
		dist, tim, loss, stops, kw, occw, covered float64
	}
	byLane := map[string]*accum{}
	dropped := 0
	for _, iv := range lr.m.Intervals {
		if iv.SetID != set {
			continue
		}
		if iv.Partial {
			dropped++
			continue
		}
		if iv.EndTick < iv.BeginTick {
			dropped++ // corrupt window: drop the whole interval, don't half-count it
			continue
		}
		a := byLane[iv.LaneID]
		if a == nil {
			a = &accum{}
			byLane[iv.LaneID] = a
		}
		a.dist += iv.SumDistM
		a.tim += iv.SumTimeS
		a.loss += iv.TimeLossS
		a.stops += iv.Stops
		w := float64(iv.EndTick - iv.BeginTick)
		a.kw += iv.K * w
		a.occw += iv.Occupancy * w
		a.covered += w
	}
	rows := make([]laneRow, 0, len(byLane))
	for id, a := range byLane {
		row := laneRow{LaneID: id, VMTm: a.dist, VHTs: a.tim, TimeLossS: a.loss, Stops: a.stops}
		if a.covered > 0 {
			// Sink K is veh/m (metrics.go); display veh/km.
			row.MeanK = a.kw / a.covered * 1000
			row.MeanOcc = a.occw / a.covered
		}
		rows = append(rows, row)
	}
	// Deterministic order: metric desc, lane id as the tie-break.
	switch sortKey {
	case "k":
		sort.Slice(rows, func(i, j int) bool { return less(rows, i, j, rows[i].MeanK, rows[j].MeanK) })
	case "occ":
		sort.Slice(rows, func(i, j int) bool { return less(rows, i, j, rows[i].MeanOcc, rows[j].MeanOcc) })
	case "stops":
		sort.Slice(rows, func(i, j int) bool { return less(rows, i, j, rows[i].Stops, rows[j].Stops) })
	default: // time loss
		sort.Slice(rows, func(i, j int) bool { return less(rows, i, j, rows[i].TimeLossS, rows[j].TimeLossS) })
	}
	total := len(rows)
	if len(rows) > 100 {
		rows = rows[:100]
	}
	return lanesResponse{Sets: lr.sets, Set: set, DroppedPartials: dropped, TotalLanes: total, Rows: rows}
}

func less(rows []laneRow, i, j int, a, b float64) bool {
	if a != b {
		return a > b
	}
	return rows[i].LaneID < rows[j].LaneID
}

func handleLanes(w http.ResponseWriter, r *http.Request) {
	lr := runs[fileIdx(r)]
	writeJSON(w, computeLanes(lr, r.URL.Query().Get("set"), r.URL.Query().Get("sort")))
}

// tripBucket aggregates trips by vehicle type for one run. Means cover
// COMPLETED trips only: horizon-censored trips (completed: false, exit =
// horizon) are truncated journeys whose inclusion biases time/loss means
// low, and the bias scales with active_at_horizon — which is exactly what
// differs between variants (ADR-0014 §3 partial discipline, applied to
// trips as to intervals). The censored count is reported alongside.
type tripBucket struct {
	Type         string  `json:"type"`
	Count        int     `json:"count"`
	Completed    int     `json:"completed"`
	Censored     int     `json:"censored"` // horizon-partial trips, excluded from the means
	MeanDistM    float64 `json:"mean_dist_m"`
	MeanTimeS    float64 `json:"mean_time_s"`
	MeanSpeedKMH float64 `json:"mean_speed_kmh"`
	MeanLossS    float64 `json:"mean_loss_s"`
	MeanStops    float64 `json:"mean_stops"`
}

func computeTrips(lr loadedRun) []tripBucket {
	type accum struct {
		n, done, stops  int
		dist, tim, loss float64
	}
	byType := map[string]*accum{}
	censored := map[string]int{}
	for _, tr := range lr.m.Trips {
		if !tr.Completed {
			censored[tr.Type]++
			continue
		}
		if tr.ExitTick < tr.EntryTick {
			continue // corrupt trip: drop, don't poison the means (uint64 underflow)
		}
		a := byType[tr.Type]
		if a == nil {
			a = &accum{}
			byType[tr.Type] = a
		}
		a.n++
		a.done++
		a.dist += tr.DistanceM
		a.tim += float64(tr.ExitTick-tr.EntryTick) * lr.m.Dt
		a.loss += tr.TimeLossS
		a.stops += tr.Stops
	}
	out := make([]tripBucket, 0, len(byType)+len(censored))
	for typ, a := range byType {
		b := tripBucket{
			Type: typ, Count: a.n + censored[typ], Completed: a.done, Censored: censored[typ],
			MeanDistM: a.dist / float64(a.n), MeanTimeS: a.tim / float64(a.n),
			MeanLossS: a.loss / float64(a.n), MeanStops: float64(a.stops) / float64(a.n),
		}
		if a.tim > 0 {
			b.MeanSpeedKMH = a.dist / a.tim * 3.6
		}
		out = append(out, b)
	}
	for typ, c := range censored {
		if _, ok := byType[typ]; !ok {
			out = append(out, tripBucket{Type: typ, Count: c, Censored: c})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func handleTrips(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, computeTrips(runs[fileIdx(r)]))
}

// --- server-rendered page (no browser-side code) ---

type sortLink struct {
	Key, Label string
	On         bool
}

type laneView struct {
	laneRow
	BarPct float64
}

type pageModel struct {
	Files    []string
	Mismatch bool
	Summary  []summaryRow
	Trips    []tripBucket
	Lanes    []laneView
	Sets     []string
	Dropped  int
	Total    int
	File     int
	Sort     string
	Set      string
	Sorts    []sortLink
}

var page = template.Must(template.New("page").Funcs(template.FuncMap{
	"q": url.QueryEscape,
	"f": func(x float64, d int) string {
		return strconv.FormatFloat(x, 'f', d, 64)
	},
}).Parse(pageTmpl))

func handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	fi := fileIdx(r)
	sortKey := q.Get("sort")
	lr := runs[fi]
	lanes := computeLanes(lr, q.Get("set"), sortKey)
	// Bars encode the metric the table is sorted by.
	metric := func(row laneRow) float64 { return row.TimeLossS }
	switch sortKey {
	case "k":
		metric = func(row laneRow) float64 { return row.MeanK }
	case "occ":
		metric = func(row laneRow) float64 { return row.MeanOcc }
	case "stops":
		metric = func(row laneRow) float64 { return row.Stops }
	}
	maxM := 1.0
	for _, row := range lanes.Rows {
		if m := metric(row); m > maxM {
			maxM = m
		}
	}
	pm := pageModel{
		Trips:   computeTrips(lr),
		Sets:    lanes.Sets,
		Dropped: lanes.DroppedPartials,
		Total:   lanes.TotalLanes,
		File:    fi,
		Sort:    sortKey,
		Set:     lanes.Set,
		Summary: computeSummary(),
	}
	for _, n := range runs {
		pm.Files = append(pm.Files, n.name)
		if n.m.Ticks != runs[0].m.Ticks || n.m.Dt != runs[0].m.Dt {
			pm.Mismatch = true
		}
	}
	for _, s := range []struct{ k, l string }{{"loss", "time loss"}, {"k", "density k"}, {"occ", "occupancy"}, {"stops", "stops"}} {
		pm.Sorts = append(pm.Sorts, sortLink{Key: s.k, Label: s.l, On: sortKey == s.k || (sortKey == "" && s.k == "loss")})
	}
	for _, row := range lanes.Rows {
		pm.Lanes = append(pm.Lanes, laneView{laneRow: row, BarPct: 60 * metric(row) / maxM})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.Execute(w, pm); err != nil {
		fmt.Fprintln(os.Stderr, "metview: page render:", err)
	}
}

func fileIdx(r *http.Request) int {
	i := 0
	_, _ = fmt.Sscanf(r.URL.Query().Get("file"), "%d", &i)
	if i < 0 || i >= len(runs) {
		i = 0
	}
	return i
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
