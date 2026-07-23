// modelpanel.test.ts — the pure half of the model panel: the resolved
// params JSON → display sections/rows mapping (paramSections). The demo
// registry's seed/ticks/capacity overrides arrive already applied
// (engine/cmd/demosrv/params.go), so the rows just render what's given.

import { test } from "node:test";
import assert from "node:assert/strict";

import { paramSections, type ModelParams } from "../src/modelpanel.ts";

const PARAMS: ModelParams = {
  id: "i280-baseline",
  scenario: { id: "i280-woodside", hash: "abcdef0123456789ffff", network: "i280.json" },
  sim: {
    dtS: 0.1,
    seed: "42",
    ticks: 36000,
    capacity: 1000,
    spawner: { ratePerLaneHour: 600 },
  },
  demand: [
    { origin: "laneA", vehPerH: 600, spacing: "poisson", vtypes: { car: 0.9, truck: 0.1 } },
    { origin: "laneB", slices: 3, spacing: "poisson" },
  ],
  controllers: {
    carFollowing: {
      model: "IDM",
      types: [
        { name: "car", lengthM: 5, widthM: 2, s0M: 2, tS: 1.6, aMps2: 0.73, bMps2: 1.67, v0Mps: 33.3 },
        { name: "truck", lengthM: 12, widthM: 2.5, s0M: 3, tS: 1.7, aMps2: 0.7, bMps2: 1.67, v0Mps: 22.22 },
      ],
    },
    laneChange: {
      model: "MOBIL",
      politeness: 0.3,
      thresholdMps2: 0.2,
      bSafeMps2: 4,
      minGapLCM: 0.5,
      minGapMergeM: 0.3,
      mergeZoneM: 200,
      mergeUrgencyGainMps2: 5,
      lcCooldownTicks: 20,
      spawnCooldownTicks: 10,
    },
    heterogeneity: { speedFactorSigma: 0.1, spawnJitter: 0.3 },
  },
};

test("paramSections: sim section renders run length, seed, capacity, spawner, scenario", () => {
  const secs = paramSections(PARAMS);
  const sim = secs.find((s) => s.title === "sim");
  assert.ok(sim);
  assert.deepEqual(sim.rows[0], ["run length", "36000 ticks × 0.1 s = 01:00:00"]);
  assert.deepEqual(sim.rows[1], ["seed", "42"]);
  assert.deepEqual(sim.rows[2], ["driver capacity", "1000"]);
  assert.deepEqual(sim.rows[3], ["spawner", "600 veh/h/lane"]);
  assert.deepEqual(sim.rows[4], ["scenario", "i280-woodside · abcdef012345"]);
});

test("paramSections: demand rows carry rate/spacing/mix, slices when piecewise", () => {
  const secs = paramSections(PARAMS);
  const dem = secs.find((s) => s.title === "demand");
  assert.ok(dem);
  assert.deepEqual(dem.rows[0], ["laneA", "600 veh/h poisson (90% car, 10% truck)"]);
  assert.deepEqual(dem.rows[1], ["laneB", "3 slice(s), poisson"]);
});

test("paramSections: controllers name the models and their parameters", () => {
  const secs = paramSections(PARAMS);
  const cf = secs.find((s) => s.title === "car-following — IDM");
  assert.ok(cf);
  assert.equal(cf.rows.length, 2);
  assert.equal(cf.rows[0]![0], "car");
  assert.match(cf.rows[0]![1], /v0 33\.3 m\/s · T 1\.6 s · a 0\.73 · b 1\.67 m\/s² · s0 2 m · 5×2 m/);
  const lc = secs.find((s) => s.title === "lane-change — MOBIL");
  assert.ok(lc);
  assert.deepEqual(lc.rows[0], ["incentive", "politeness 0.3 · Δa_th 0.2 · b_safe 4 m/s²"]);
  const het = secs.find((s) => s.title === "driver heterogeneity");
  assert.ok(het);
  assert.deepEqual(het.rows[1], ["spawn interval", "±30% jitter"]);
});

test("paramSections: director-only demand and long flow lists", () => {
  const p: ModelParams = {
    ...PARAMS,
    sim: { ...PARAMS.sim, spawner: null },
    demand: Array.from({ length: 7 }, (_, i) => ({ origin: `lane${i}`, vehPerH: 100 * (i + 1), spacing: "poisson" })),
  };
  const secs = paramSections(p);
  const sim = secs.find((s) => s.title === "sim");
  assert.deepEqual(sim!.rows[3], ["spawner", "off (director demand)"]);
  const dem = secs.find((s) => s.title === "demand");
  assert.equal(dem!.rows.length, 5); // 4 flows + "+N more" tail
  assert.deepEqual(dem!.rows[4], ["…", "+3 more flow(s)"]);
  const empty = paramSections({ ...PARAMS, demand: [] });
  assert.equal(empty.find((s) => s.title === "demand"), undefined);
});
