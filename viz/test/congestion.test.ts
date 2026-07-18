// congestion.test.ts — the client-derived congestion proxy: nearest-lane
// grid lookup and mean-speed/limit aggregation.

import { test } from "node:test";
import assert from "node:assert/strict";

import { LaneIndex, laneSpeedRatios } from "../src/congestion.ts";

test("nearestLane finds the segment's lane within threshold", () => {
  const idx = new LaneIndex([
    { id: "lane-a", shape: [[0, 0], [200, 0]] },
    { id: "lane-b", shape: [[0, 100], [200, 100]] },
  ]);
  assert.equal(idx.nearestLane(100, 2.5, 6), "lane-a");
  assert.equal(idx.nearestLane(150, 97, 6), "lane-b");
  assert.equal(idx.nearestLane(100, 50, 6), null); // between lanes, out of threshold
  assert.equal(idx.nearestLane(500, 0, 6), null); // past the polyline end
});

test("nearestLane crosses grid cells for long segments and near boundaries", () => {
  const idx = new LaneIndex([{ id: "diag", shape: [[0, 0], [1000, 1000]] }]);
  assert.equal(idx.nearestLane(500, 503, 6), "diag");
});

test("laneSpeedRatios aggregates mean/limit and clamps", () => {
  const limits = new Map([["a", 29.06]]);
  const ratios = laneSpeedRatios(
    new Map([
      ["a", [29.06, 14.53]], // mean 21.795 → 0.75
      ["unknown-lane", [10]],
    ]),
    limits,
  );
  assert.ok(Math.abs(ratios.get("a")! - 0.75) < 1e-9);
  assert.equal(ratios.has("unknown-lane"), false);
  const jam = laneSpeedRatios(new Map([["a", [0, 0]]]), limits);
  assert.equal(jam.get("a"), 0);
});
