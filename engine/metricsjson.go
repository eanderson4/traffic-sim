package engine

// metricsjson.go — the offline file sink for the M13 metric kernel
// (ADR-0014 §6(b)): same kernel, same numbers as the NATS sink, marshaled as
// one human-diffable JSON document (schema_version 1). simrun -metrics-out
// writes it next to the run artifacts. Wire field names are pinned
// snake_case here so the kernel types stay transport-agnostic; the normative
// metric definitions are ADR-0014 §3, and pointer fields are omitted (never
// zero-filled/null) when their group is off or V is undefined (§2).

import (
	"encoding/json"
	"io"
	"sort"
)

// metricsDoc is the top-level schema_version 1 document.
type metricsDoc struct {
	SchemaVersion int            `json:"schema_version"`
	Ticks         uint64         `json:"ticks"`
	Dt            float64        `json:"dt"`
	Intervals     []intervalJSON `json:"intervals"`
	Trips         []tripJSON     `json:"trips"`
	Totals        totalsJSON     `json:"totals"`
}

type intervalJSON struct {
	SchemaVersion int      `json:"schema_version"`
	SetID         string   `json:"set_id"`
	LaneID        string   `json:"lane_id"`
	BeginTick     uint64   `json:"begin_tick"`
	EndTick       uint64   `json:"end_tick"`
	Partial       bool     `json:"partial"`
	SumDistM      float64  `json:"sum_dist_m"`
	SumTimeS      float64  `json:"sum_time_s"`
	Q             *float64 `json:"q,omitempty"`
	K             *float64 `json:"k,omitempty"`
	V             *float64 `json:"v,omitempty"`
	Occupancy     *float64 `json:"occupancy,omitempty"`
	Stops         *int     `json:"stops,omitempty"`
	TimeLossS     *float64 `json:"time_loss_s,omitempty"`
}

type tripJSON struct {
	SchemaVersion int     `json:"schema_version"`
	VehicleID     uint64  `json:"vehicle_id"`
	TypeName      string  `json:"type"`
	OriginLaneID  string  `json:"origin_lane"`
	DestLaneID    string  `json:"dest_lane"`
	EntryTick     uint64  `json:"entry_tick"`
	ExitTick      uint64  `json:"exit_tick"`
	DistanceM     float64 `json:"distance_m"`
	TimeLossS     float64 `json:"time_loss_s"`
	Stops         int     `json:"stops"`
	StoppedTimeS  float64 `json:"stopped_time_s"`
	Completed     bool    `json:"completed"`
	// Stranded: the gridlock escape ended this trip (ADR-0034). Always with
	// completed=false; omitted on every ordinary trip so existing readers
	// see the document they already knew.
	Stranded bool `json:"stranded,omitempty"`
}

type laneDeniedJSON struct {
	Lane    string  `json:"lane"`
	WaitS   float64 `json:"wait_s"`
	Pending float64 `json:"pending"`
	Served  int     `json:"served"`
}

// demandJSON reports how much of the scenario's demand became vehicles.
// It rides in the metrics document because delivery is a VALIDITY flag on
// every other number here: a run that injected 16% of what its scenario
// asked for is not that scenario, and nothing else in this document would
// say so — expiry is not a denial, so denied_* reads clean through it.
type demandJSON struct {
	Injected       int     `json:"injected"`
	Expired        int     `json:"expired"`
	DeadOnArrival  int     `json:"dead_on_arrival"`
	DeliveredFrac  float64 `json:"delivered_frac"`
	LastInjectTick uint64  `json:"last_inject_tick"`
}

type totalsJSON struct {
	CompletedTrips   int              `json:"completed_trips"`
	StrandedTrips    int              `json:"stranded_trips"`
	ActiveAtHorizon  int              `json:"active_at_horizon"`
	VMT              float64          `json:"vmt"`
	VHT              float64          `json:"vht"`
	TotalTimeLossS   float64          `json:"total_time_loss_s"`
	MeanTimeLossS    float64          `json:"mean_time_loss_s"`
	DeniedWaitS      float64          `json:"denied_wait_s"`
	DeniedPending    float64          `json:"denied_pending"`
	DeniedServed     int              `json:"denied_served"`
	DroppedCrossings int              `json:"dropped_crossings"`
	DeniedByLane     []laneDeniedJSON `json:"denied_by_lane"`
	Demand           *demandJSON      `json:"demand,omitempty"`
}

// DemandDelivery carries the kernel's director spawn tallies into the
// metrics document. Optional: runs with no director demand omit it.
type DemandDelivery struct {
	Injected       int
	Expired        int
	DeadOnArrival  int
	LastInjectTick uint64
}

// WriteMetricsJSON drains k's remaining interval and trip records, reads its
// Totals, and writes the run's metric output as one indented JSON document
// to w. ticks is the run horizon. Drain order is already the canonical sort;
// the denied-by-lane array is sorted by lane ID (the Totals.DeniedByLane
// emission contract — never a bare map).
func WriteMetricsJSON(w io.Writer, k *Kernel, ticks uint64, dd ...DemandDelivery) error {
	intervals := k.DrainIntervals()
	trips := k.DrainTrips()
	tot := k.Totals()

	doc := metricsDoc{
		SchemaVersion: 1,
		Ticks:         ticks,
		Dt:            k.dt,
		Intervals:     make([]intervalJSON, 0, len(intervals)),
		Trips:         make([]tripJSON, 0, len(trips)),
	}
	for _, r := range intervals {
		doc.Intervals = append(doc.Intervals, intervalJSON{
			SchemaVersion: r.SchemaVersion,
			SetID:         r.SetID,
			LaneID:        r.LaneID,
			BeginTick:     r.BeginTick,
			EndTick:       r.EndTick,
			Partial:       r.Partial,
			SumDistM:      r.SumDistM,
			SumTimeS:      r.SumTimeS,
			Q:             r.Q,
			K:             r.K,
			V:             r.V,
			Occupancy:     r.Occupancy,
			Stops:         r.Stops,
			TimeLossS:     r.TimeLossS,
		})
	}
	for _, r := range trips {
		doc.Trips = append(doc.Trips, tripJSON{
			SchemaVersion: r.SchemaVersion,
			VehicleID:     r.VehicleID,
			TypeName:      r.TypeName,
			OriginLaneID:  r.OriginLaneID,
			DestLaneID:    r.DestLaneID,
			EntryTick:     r.EntryTick,
			ExitTick:      r.ExitTick,
			DistanceM:     r.DistanceM,
			TimeLossS:     r.TimeLossS,
			Stops:         r.Stops,
			StoppedTimeS:  r.StoppedTimeS,
			Completed:     r.Completed,
			Stranded:      r.Stranded,
		})
	}
	lanes := make([]string, 0, len(tot.DeniedByLane))
	for id := range tot.DeniedByLane {
		lanes = append(lanes, id)
	}
	sort.Strings(lanes)
	doc.Totals = totalsJSON{
		CompletedTrips:   tot.CompletedTrips,
		StrandedTrips:    tot.StrandedTrips,
		ActiveAtHorizon:  tot.ActiveAtHorizon,
		VMT:              tot.VMT,
		VHT:              tot.VHT,
		TotalTimeLossS:   tot.TotalTimeLossS,
		MeanTimeLossS:    tot.MeanTimeLossS,
		DeniedWaitS:      tot.DeniedWaitS,
		DeniedPending:    tot.DeniedPending,
		DeniedServed:     tot.DeniedServed,
		DroppedCrossings: tot.DroppedCrossings,
		DeniedByLane:     make([]laneDeniedJSON, 0, len(lanes)),
	}
	for _, d := range dd {
		if d.Injected+d.Expired == 0 {
			continue
		}
		doc.Totals.Demand = &demandJSON{
			Injected:       d.Injected,
			Expired:        d.Expired,
			DeadOnArrival:  d.DeadOnArrival,
			DeliveredFrac:  float64(d.Injected) / float64(d.Injected+d.Expired),
			LastInjectTick: d.LastInjectTick,
		}
	}
	for _, id := range lanes {
		ld := tot.DeniedByLane[id]
		doc.Totals.DeniedByLane = append(doc.Totals.DeniedByLane, laneDeniedJSON{
			Lane:    id,
			WaitS:   ld.WaitS,
			Pending: ld.Pending,
			Served:  ld.Served,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
