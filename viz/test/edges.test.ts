// edges.test.ts — edge-group boundary selection: the outermost lanes of
// each lateral-chaining group carry the casing so same-road lanes read as
// one road (viz casing layer).

import { test } from "node:test";
import assert from "node:assert/strict";

import { edgeBoundaries } from "../src/edges.ts";

test("edgeBoundaries: min and max index of each group are boundaries", () => {
  const lanes = [
    { id: "a_0", edge: "a", edgeIndex: 0 },
    { id: "a_1", edge: "a", edgeIndex: 1 },
    { id: "a_2", edge: "a", edgeIndex: 2 },
    { id: "b_0", edge: "b", edgeIndex: 0 },
    { id: "b_1", edge: "b", edgeIndex: 1 },
  ];
  const got = edgeBoundaries(lanes);
  assert.deepEqual([...got].sort(), ["a_0", "a_2", "b_0", "b_1"]);
});

test("edgeBoundaries: ungrouped and single-lane groups are always boundaries", () => {
  const lanes = [
    { id: "int_0" }, // junction interior: no edge
    { id: "int_1", edge: "" },
    { id: "solo_0", edge: "solo", edgeIndex: 0 },
    { id: "legacy_0", edge: "leg" }, // stale cache: edge without index
  ];
  const got = edgeBoundaries(lanes);
  assert.deepEqual([...got].sort(), ["int_0", "int_1", "legacy_0", "solo_0"]);
});

test("edgeBoundaries: non-contiguous indices (filtered lanes) still bound by extremes", () => {
  // A sidewalk/bike lane filtered out between indices leaves a gap; the
  // visual boundary still belongs to the extreme members.
  const lanes = [
    { id: "c_0", edge: "c", edgeIndex: 0 },
    { id: "c_2", edge: "c", edgeIndex: 2 },
    { id: "c_3", edge: "c", edgeIndex: 3 },
  ];
  const got = edgeBoundaries(lanes);
  assert.deepEqual([...got].sort(), ["c_0", "c_3"]);
});
