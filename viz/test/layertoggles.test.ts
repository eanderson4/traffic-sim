// layertoggles.test.ts — the legend toggle → layer ops mapping
// (src/layertoggles.ts): the "vehicles" class filter is BUILT from the
// on/off state so the car/truck toggles compose independently, and each
// non-vehicle key hides exactly its layer group.

import { test } from "node:test";
import assert from "node:assert/strict";

import { DEFAULT_TOGGLES, layerOpsFor } from "../src/layertoggles.ts";

const vis = (ops: ReturnType<typeof layerOpsFor>): Map<string, boolean> => new Map(ops.visibility);

test("default state: no vehicle filter, every layer visible", () => {
  const ops = layerOpsFor(DEFAULT_TOGGLES);
  assert.equal(ops.vehiclesFilter, null); // both classes on → no filter
  for (const [id, on] of ops.visibility) assert.ok(on, `${id} should be visible`);
});

test("car off: vehicles filter keeps cls 1 only; trailers stay (trucks on)", () => {
  const ops = layerOpsFor({ ...DEFAULT_TOGGLES, car: false });
  assert.deepEqual(ops.vehiclesFilter, ["==", ["get", "cls"], 1]);
  assert.equal(vis(ops).get("trailers"), true);
});

test("truck off: vehicles filter keeps cls 0 only; trailers hide too", () => {
  const ops = layerOpsFor({ ...DEFAULT_TOGGLES, truck: false });
  assert.deepEqual(ops.vehiclesFilter, ["==", ["get", "cls"], 0]);
  assert.equal(vis(ops).get("trailers"), false);
});

test("both classes off: never-match filter (layer stays, restores cleanly)", () => {
  const ops = layerOpsFor({ ...DEFAULT_TOGGLES, car: false, truck: false });
  assert.deepEqual(ops.vehiclesFilter, ["==", ["get", "cls"], -1]); // no such class
  assert.equal(vis(ops).get("trailers"), false);
  // Independent re-toggles rebuild the filter from state, not a swap:
  // truck back on → single-class filter; both back on → no filter again.
  assert.deepEqual(
    layerOpsFor({ ...DEFAULT_TOGGLES, car: false }).vehiclesFilter,
    ["==", ["get", "cls"], 1],
  );
  assert.equal(layerOpsFor(DEFAULT_TOGGLES).vehiclesFilter, null);
});

test("signals off: housing, stop bars, and all three lens layers hide", () => {
  const v = vis(layerOpsFor({ ...DEFAULT_TOGGLES, signals: false }));
  for (const id of [
    "signals-bars",
    "signals-housing",
    "signals-lens-red",
    "signals-lens-amber",
    "signals-lens-green",
  ]) {
    assert.equal(v.get(id), false, id);
  }
});

test("stops off: the static stop-signs layer hides", () => {
  const v = vis(layerOpsFor({ ...DEFAULT_TOGGLES, stops: false }));
  assert.equal(v.get("stop-signs"), false);
  // Signal layers are a different toggle — heads stay on.
  assert.equal(v.get("signals-housing"), true);
});

test("congestion off: network-line hides; the casing is never toggled", () => {
  const v = vis(layerOpsFor({ ...DEFAULT_TOGGLES, congestion: false }));
  assert.equal(v.get("network-line"), false);
  assert.ok(!v.has("network-casing"), "casing is road geometry, not the overlay");
});

test("every toggle key off still names only real layers, exactly once", () => {
  const ops = layerOpsFor({ car: false, truck: false, signals: false, stops: false, congestion: false });
  const ids = ops.visibility.map(([id]) => id);
  assert.equal(new Set(ids).size, ids.length);
  for (const [, on] of ops.visibility) assert.equal(on, false);
});
