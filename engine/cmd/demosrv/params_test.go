package main

// params_test.go — the params resolution: RunSpec passthrough, the demo
// registry's seed/ticks/capacity overrides winning the way serve's flags
// do, spawner presence, and demand/type mapping. Constructed scenarios
// (no files): RunSpec reads only Manifest/Demands, and Hash is
// deterministic over empty parts — sufficient for shape assertions.

import (
	"testing"

	"traffic-sim/engine"
	"traffic-sim/engine/scenario"
)

func testScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Manifest: scenario.Manifest{
			ID: "test", Seed: 7, Ticks: 100,
			Network: "network.json",
			Types:   []string{"car", "truck"},
			Params:  scenario.Params{Dt: 0.05},
			Spawner: &scenario.Spawner{RatePerLaneHour: 600, DensityPerKm: 12},
		},
		Demands: []*scenario.DemandFile{{FormatVersion: 1, Flows: []scenario.Flow{
			{Origin: "laneA", VehPerH: 900, Spacing: "poisson", VTypes: map[string]float64{"car": 0.9, "truck": 0.1}},
			{Origin: "laneB", Spacing: "uniform", Slices: []scenario.Slice{{StartS: 0, EndS: 60, VehPerH: 300}}},
		}}},
	}
}

func TestBuildParamsOverrides(t *testing.T) {
	seed, ticks, capacity := uint64(42), uint64(200), uint64(5000)
	d := &Demo{ID: "d1", Seed: &seed, Ticks: &ticks, Capacity: &capacity}
	resp, err := buildParams(d, testScenario(), 1000)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	// Demo overrides win (mirroring serve's flag.Visit override pass).
	if resp.Sim.Seed != "42" || resp.Sim.Ticks != 200 || resp.Sim.Capacity != 5000 {
		t.Errorf("overrides not applied: %+v", resp.Sim)
	}
	if resp.Sim.DtS != 0.05 {
		t.Errorf("dt: got %v, want the scenario's 0.05", resp.Sim.DtS)
	}
	if resp.Sim.Spawner == nil || resp.Sim.Spawner.RatePerLaneHour != 600 || resp.Sim.Spawner.DensityPerKm != 12 {
		t.Errorf("spawner: %+v", resp.Sim.Spawner)
	}
	if resp.Scenario.ID != "test" || resp.Scenario.Network != "network.json" || resp.Scenario.Hash == "" {
		t.Errorf("scenario info: %+v", resp.Scenario)
	}
	if len(resp.Demand) != 2 || resp.Demand[0].Origin != "laneA" || resp.Demand[0].VehPerH != 900 ||
		resp.Demand[1].Slices != 1 || resp.Demand[0].VTypes["truck"] != 0.1 {
		t.Errorf("demand: %+v", resp.Demand)
	}
	cf := resp.Controllers.CarFollowing
	if cf.Model != "IDM" || len(cf.Types) != 2 || cf.Types[0].Name != "car" || cf.Types[0].TS != engine.Car.T ||
		cf.Types[1].Name != "truck" || cf.Types[1].V0Mps != engine.Truck.V0 {
		t.Errorf("carFollowing: %+v", cf)
	}
	lc := resp.Controllers.LaneChange
	def := engine.DefaultParams()
	if lc.Model != "MOBIL" || lc.Politeness != def.Politeness || lc.BSafeMps2 != def.BSafe ||
		lc.LCCooldownTicks != def.LCCooldown {
		t.Errorf("laneChange: %+v", lc)
	}
	if resp.Controllers.Heterogeneity.SpeedFactorSigma != def.SpeedFactorSigma {
		t.Errorf("heterogeneity: %+v", resp.Controllers.Heterogeneity)
	}
}

func TestBuildParamsDefaults(t *testing.T) {
	sc := testScenario()
	sc.Manifest.Spawner = nil // director-only demand
	d := &Demo{ID: "d1"}
	resp, err := buildParams(d, sc, 1000)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	if resp.Sim.Seed != "7" || resp.Sim.Ticks != 100 || resp.Sim.Capacity != 1000 {
		t.Errorf("manifest/default values lost: %+v", resp.Sim)
	}
	if resp.Sim.Spawner != nil {
		t.Errorf("spawner should be nil when the manifest disables it: %+v", resp.Sim.Spawner)
	}
}
