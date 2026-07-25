// stopsign.test.ts — stop-sign placement (src/stopsign.ts): row="stop"
// internal lanes cluster to ONE sign per (junction, approach) — merged
// across a multi-lane approach, split across opposing arms — placed at
// the stop-line centroid set back HEAD_SETBACK_M along the reverse mean
// entry bearing, and never throwing on degenerate input.

import { test } from "node:test";
import assert from "node:assert/strict";

import { stopSigns, type StopSignLane } from "../src/stopsign.ts";
import { HEAD_SETBACK_M } from "../src/signals.ts";

const lane = (
  id: string,
  junction: string,
  shape: Array<readonly number[]>,
  row = "stop",
): StopSignLane => ({ id, junction, row, shape });

const close = (got: number, want: number): void => {
  assert.ok(Math.abs(got - want) < 1e-9, `got ${got}, want ${want}`);
};

test("no stop rows → no signs", () => {
  const lanes = [
    lane("a_0", "", [[0, 0], [10, 0]], "major"),
    lane("b_0", "", [[0, 0], [10, 0]], ""),
  ];
  assert.deepEqual(stopSigns(lanes), []);
});

test("multi-lane approach merges to ONE sign at the centroid, set back", () => {
  // Two eastbound lanes of junction J, stop lines at (0,0) and (2,0).
  const signs = stopSigns([
    lane("j_0", "J", [[0, 0], [10, 0]]),
    lane("j_1", "J", [[2, 0], [12, 0]]),
  ]);
  assert.equal(signs.length, 1);
  assert.equal(signs[0]!.id, "J#0");
  close(signs[0]!.x, 1 - HEAD_SETBACK_M); // centroid x=1, reverse of east
  close(signs[0]!.y, 0);
});

test("same junction, opposing approaches stay TWO signs", () => {
  // Eastbound arm entering at (10,0), westbound at (-10,0).
  const signs = stopSigns([
    lane("j_0", "J", [[10, 0], [20, 0]]),
    lane("j_1", "J", [[-10, 0], [-20, 0]]),
  ]);
  assert.equal(signs.length, 2);
  assert.equal(signs[0]!.id, "J#0");
  assert.equal(signs[1]!.id, "J#1");
  close(signs[0]!.x, 10 - HEAD_SETBACK_M); // pushed back toward its own arm
  close(signs[1]!.x, -10 + HEAD_SETBACK_M);
});

test("different junctions never merge", () => {
  const signs = stopSigns([
    lane("j_0", "J", [[0, 0], [10, 0]]),
    lane("k_0", "K", [[2, 0], [12, 0]]), // parallel, close — but another junction
  ]);
  assert.equal(signs.length, 2);
});

test("degenerate shapes are skipped; bearingless lanes join at the centroid", () => {
  const signs = stopSigns([
    lane("empty", "J", []), // no points at all
    lane("blank", "J", [[]]), // first point has no coordinates
    lane("dot", "J", [[5, 5]]), // a point but no direction
  ]);
  assert.equal(signs.length, 1); // only "dot" survives
  close(signs[0]!.x, 5); // no bearing → no setback, sits on the centroid
  close(signs[0]!.y, 5);
});

test("stop lane without a junction still renders (per-lane group)", () => {
  const signs = stopSigns([lane("j_0", "", [[0, 0], [10, 0]])]);
  assert.equal(signs.length, 1);
  assert.equal(signs[0]!.id, "lane:j_0#0");
  close(signs[0]!.x, -HEAD_SETBACK_M);
});

test("skewed approaches within the cone merge; beyond it split", () => {
  // 30° apart: same approach (curved entry). 90° apart: distinct arms.
  const merged = stopSigns([
    lane("j_0", "J", [[0, 0], [10, 0]]),
    lane("j_1", "J", [[0, 1], [10, 1 + 10 * Math.tan(Math.PI / 6)]]),
  ]);
  assert.equal(merged.length, 1);
  const split = stopSigns([
    lane("j_0", "J", [[0, 0], [10, 0]]),
    lane("j_1", "J", [[0, 0], [0, 10]]),
  ]);
  assert.equal(split.length, 2);

});

test("splits parallel approaches beyond the cluster radius (frontage roads)", () => {
  // Same junction, same bearing, but stop lines 200 m apart: two signs,
  // not one floating between the carriageways.
  const split = stopSigns([
    lane("a", "j1", [[0, 0], [10, 0]]),
    lane("b", "j1", [[0, 200], [10, 200]]),
  ]);
  assert.equal(split.length, 2);
});
