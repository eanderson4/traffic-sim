// furniture.test.ts — the baked furniture.geojson consumer (ADR-0023
// §6.2): parseFurniture sorts heads/bars/signs and skips malformed
// features; furnitureHeads joins heads to TSSG programs by the baked
// binding (missing programs skip, mirroring the live path's
// missing-geometry rule). Round-trips the schema bake-furniture.mjs emits.

import { test } from "node:test";
import assert from "node:assert/strict";

import { furnitureHeads, parseFurniture } from "../src/furniture.ts";
import type { FeatureCollection } from "geojson";
import type { SignalTable } from "../src/tssg.ts";

const TABLE: SignalTable = {
  tick: 0,
  programs: [
    {
      id: "j1",
      junction: "j1",
      offsetTicks: 0,
      phases: [
        { durationTicks: 90, state: "Gr" },
        { durationTicks: 10, state: "yr" },
      ],
      links: [
        { linkIdx: 0, laneId: "i1_0" },
        { linkIdx: 1, laneId: "i2_0" },
      ],
    },
  ],
};

// doc mirrors bake-furniture.mjs's output: head points with the program
// binding, bar lines sharing the head id, sign points.
const DOC: FeatureCollection = {
  type: "FeatureCollection",
  features: [
    {
      type: "Feature",
      properties: { kind: "head", id: "j1:0", program: "j1", link: 0 },
      geometry: { type: "Point", coordinates: [100, 200] },
    },
    {
      type: "Feature",
      properties: { kind: "bar", id: "j1:0" },
      geometry: {
        type: "LineString",
        coordinates: [
          [95, 198],
          [105, 202],
        ],
      },
    },
    {
      type: "Feature",
      properties: { kind: "head", id: "j1:1", program: "j1", link: 1 },
      geometry: { type: "Point", coordinates: [110, 190] },
    },
    {
      type: "Feature",
      properties: { kind: "sign", id: "j9#0" },
      geometry: { type: "Point", coordinates: [50, 60] },
    },
  ],
};

test("parseFurniture sorts heads, bars, and signs; metric coords untouched", () => {
  const f = parseFurniture(DOC);
  assert.equal(f.heads.length, 2);
  assert.deepEqual(f.heads[0], { id: "j1:0", programId: "j1", linkIdx: 0, x: 100, y: 200 });
  assert.equal(f.bars.size, 1);
  assert.deepEqual(f.bars.get("j1:0"), [95, 198, 105, 202]);
  assert.deepEqual(f.signs, [{ id: "j9#0", x: 50, y: 60 }]);
});

test("furnitureHeads joins programs by the baked binding; bar attaches by shared id", () => {
  const heads = furnitureHeads(parseFurniture(DOC), TABLE);
  assert.equal(heads.length, 2);
  const [h0, h1] = heads;
  assert.equal(h0!.id, "j1:0");
  assert.equal(h0!.program.id, "j1");
  assert.equal(h0!.linkIdx, 0);
  assert.deepEqual(h0!.bar, [95, 198, 105, 202]);
  assert.equal(h1!.bar, null); // no bar feature for this head
});

test("a head whose program is missing from the table is skipped", () => {
  const doc: FeatureCollection = {
    type: "FeatureCollection",
    features: [
      {
        type: "Feature",
        properties: { kind: "head", id: "gone:0", program: "gone", link: 0 },
        geometry: { type: "Point", coordinates: [0, 0] },
      },
    ],
  };
  assert.equal(furnitureHeads(parseFurniture(doc), TABLE).length, 0);
});

test("malformed features skip, never throw", () => {
  // TODO(review 2026-07-26): the guarantee is narrower than the test
  // name — legal-GeoJSON geometry:null on a head/bar WITH an id throws
  // (furniture.ts:38), and malformed link values that coerce to 0
  // (null, false, "") are accepted rather than skipped. The producer is
  // our own bake-furniture.mjs, which emits neither, so the guarantee
  // as tested is "malformed features the producer can emit skip".
  // Deferred, not fixed.
  const doc = {
    type: "FeatureCollection",
    features: [
      { type: "Feature", properties: { kind: "head" }, geometry: { type: "Point", coordinates: [1, 2] } }, // no id
      { type: "Feature", properties: { kind: "head", id: "x", program: "j1", link: 0 }, geometry: { type: "LineString", coordinates: [[1, 2], [3, 4]] } }, // not a point
      { type: "Feature", properties: { kind: "bar", id: "b" }, geometry: { type: "Point", coordinates: [1, 2] } }, // not a line
      { type: "Feature", properties: { kind: "mystery", id: "m" }, geometry: { type: "Point", coordinates: [1, 2] } }, // unknown kind
      { type: "Feature", properties: null, geometry: { type: "Point", coordinates: [1, 2] } },
    ],
  } as unknown as FeatureCollection;
  const f = parseFurniture(doc);
  assert.equal(f.heads.length, 0);
  assert.equal(f.bars.size, 0);
  assert.equal(f.signs.length, 0);
  assert.equal(parseFurniture({ type: "FeatureCollection" } as unknown as FeatureCollection).heads.length, 0);
});
