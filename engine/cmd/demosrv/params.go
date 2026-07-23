package main

// params.go — GET /api/demo/{id}/params: the resolved run configuration
// (sim parameters, demand, and the driver-controller models WITH their
// parameters) for the viz's model panel. The values come from the SAME
// resolution path serve uses — scenario.Load → RunSpec(typeReg) with the
// demo's -seed/-ticks/-capacity overrides applied the way serveSpawner
// passes them (cmd/serve/main.go). Two serve values are MIRRORED, not
// shared (separate main packages): the typeReg registry and the capacity
// default — keep them in sync with cmd/serve/main.go by hand. Read-only
// by design: the engine is authoritative over world state and there is no
// mid-run parameter contract; changing a knob means starting a new run.
//
// No cache: scenario dirs are files this dev tool exists to iterate on,
// and scenario.Load per request is trivially cheap — a memoized panel
// would drift from a freshly edited scenario (Fable review 2026-07-23).

import (
	"fmt"
	"net/http"
	"strconv"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

// typeReg mirrors serve's vehicle-type registry (cmd/serve/main.go).
var typeReg = map[string]*engine.VehicleType{"car": &engine.Car, "truck": &engine.Truck}

type typeParams struct {
	Name    string  `json:"name"`
	LengthM float64 `json:"lengthM"`
	WidthM  float64 `json:"widthM"`
	S0M     float64 `json:"s0M"`   // jam gap, bumper-to-bumper
	TS      float64 `json:"tS"`    // desired time headway
	AMps2   float64 `json:"aMps2"` // max acceleration
	BMps2   float64 `json:"bMps2"` // comfortable deceleration
	V0Mps   float64 `json:"v0Mps"` // desired speed
}

type flowParams struct {
	Origin  string             `json:"origin"`
	VehPerH float64            `json:"vehPerH,omitempty"`
	Slices  int                `json:"slices,omitempty"`
	Spacing string             `json:"spacing"`
	VTypes  map[string]float64 `json:"vtypes,omitempty"`
}

type spawnerInfo struct {
	RatePerLaneHour float64 `json:"ratePerLaneHour"`
	DensityPerKm    float64 `json:"densityPerKm,omitempty"`
}

type simInfo struct {
	DtS float64 `json:"dtS"`
	// Seed is a STRING: JSON numbers are float64, so a uint64 seed ≥ 2^53
	// would render wrong in the browser — and seed identity is replay
	// identity (ADR-0005). Ticks stays numeric (never near 2^53).
	Seed     string       `json:"seed"`
	Ticks    uint64       `json:"ticks"`
	Capacity int          `json:"capacity"`
	Spawner  *spawnerInfo `json:"spawner"` // nil = built-in spawner disabled
}

type scenarioInfo struct {
	ID      string `json:"id"`
	Hash    string `json:"hash"` // full ADR-0012 content hash; clients shorten for display
	Network string `json:"network"`
}

type carFollowingInfo struct {
	Model string       `json:"model"` // "IDM"
	Types []typeParams `json:"types"`
}

type laneChangeInfo struct {
	Model                string  `json:"model"` // "MOBIL"
	Politeness           float64 `json:"politeness"`
	ThresholdMps2        float64 `json:"thresholdMps2"`
	BSafeMps2            float64 `json:"bSafeMps2"`
	MinGapLCM            float64 `json:"minGapLCM"`
	MinGapMergeM         float64 `json:"minGapMergeM"`
	MergeZoneM           float64 `json:"mergeZoneM"`
	MergeUrgencyGainMps2 float64 `json:"mergeUrgencyGainMps2"`
	LCCooldownTicks      uint64  `json:"lcCooldownTicks"`
	SpawnCooldownTicks   uint64  `json:"spawnCooldownTicks"`
}

type heterogeneityInfo struct {
	SpeedFactorSigma float64 `json:"speedFactorSigma"`
	SpawnJitter      float64 `json:"spawnJitter"`
}

type controllersInfo struct {
	CarFollowing  carFollowingInfo  `json:"carFollowing"`
	LaneChange    laneChangeInfo    `json:"laneChange"`
	Heterogeneity heterogeneityInfo `json:"heterogeneity"`
}

type paramsResponse struct {
	ID          string          `json:"id"`
	Scenario    scenarioInfo    `json:"scenario"`
	Sim         simInfo         `json:"sim"`
	Demand      []flowParams    `json:"demand"`
	Controllers controllersInfo `json:"controllers"`
}

// buildParams resolves the run configuration for d exactly as a serve
// invocation would (RunSpec + the demo's sweep overrides) and shapes it
// for the panel. capacityDefault mirrors serve's -capacity flag default.
func buildParams(d *Demo, sc *scenario.Scenario, capacityDefault int) (paramsResponse, error) {
	spec, err := sc.RunSpec(typeReg)
	if err != nil {
		return paramsResponse{}, err
	}
	// The demo registry's seed/ticks overrides reach serve as flags, which
	// win over the manifest (serveSpawner → serve's flag.Visit overrides).
	if d.Seed != nil {
		spec.Seed = *d.Seed
	}
	if d.Ticks != nil {
		spec.Ticks = *d.Ticks
	}
	capacity := capacityDefault
	if d.Capacity != nil {
		capacity = int(*d.Capacity)
	}

	resp := paramsResponse{ID: d.ID}
	resp.Scenario = scenarioInfo{ID: sc.Manifest.ID, Hash: sc.Hash(), Network: sc.Manifest.Network}
	resp.Sim.DtS = spec.Params.Dt
	resp.Sim.Seed = strconv.FormatUint(spec.Seed, 10)
	resp.Sim.Ticks = spec.Ticks
	resp.Sim.Capacity = capacity
	if spec.Scen.SpawnRatePerLaneHour > 0 {
		resp.Sim.Spawner = &spawnerInfo{
			RatePerLaneHour: spec.Scen.SpawnRatePerLaneHour,
			DensityPerKm:    spec.Scen.DensityTargetPerKm,
		}
	}
	for _, df := range sc.Demands {
		for _, f := range df.Flows {
			resp.Demand = append(resp.Demand, flowParams{
				Origin:  f.Origin,
				VehPerH: f.VehPerH,
				Slices:  len(f.Slices),
				Spacing: f.Spacing,
				VTypes:  f.VTypes,
			})
		}
	}
	p := spec.Params
	resp.Controllers.CarFollowing.Model = "IDM"
	for _, t := range spec.Scen.Types {
		resp.Controllers.CarFollowing.Types = append(resp.Controllers.CarFollowing.Types, typeParams{
			Name: t.Name, LengthM: t.Length, WidthM: t.Width,
			S0M: t.S0, TS: t.T, AMps2: t.A, BMps2: t.B, V0Mps: t.V0,
		})
	}
	resp.Controllers.LaneChange = laneChangeInfo{
		Model: "MOBIL", Politeness: p.Politeness, ThresholdMps2: p.LCThreshold,
		BSafeMps2: p.BSafe, MinGapLCM: p.MinGapLC, MinGapMergeM: p.MinGapMerge,
		MergeZoneM: p.MergeZone, MergeUrgencyGainMps2: p.MergeUrgencyGain,
		LCCooldownTicks: p.LCCooldown, SpawnCooldownTicks: p.SpawnCooldown,
	}
	resp.Controllers.Heterogeneity = heterogeneityInfo{
		SpeedFactorSigma: p.SpeedFactorSigma,
		SpawnJitter:      p.SpawnJitter,
	}
	return resp, nil
}

func (s *server) handleParams(w http.ResponseWriter, r *http.Request) {
	d := s.reg.byID(r.PathValue("id"))
	if d == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("unknown demo %q", r.PathValue("id")))
		return
	}
	sc, err := scenario.Load(d.ScenarioDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// 1000 mirrors serve's -capacity flag default (cmd/serve/main.go).
	p, err := buildParams(d, sc, 1000)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
