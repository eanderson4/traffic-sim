// proj.test.ts — inverse-UTM projection tests. Reference values computed
// with PROJ (pyproj) for the i280-woodside frame
// (+proj=utm +zone=10 +ellps=WGS84, netOffset from the net.xml location
// element): absolute UTM = local − netOffset.

import { test } from "node:test";
import assert from "node:assert/strict";

import { makeProjector, type LocalFrame } from "../src/proj.ts";

const FRAME: LocalFrame = {
  projection: "+proj=utm +zone=10 +ellps=WGS84 +datum=WGS84 +units=m +no_defs",
  netOffset: [-562744.68, -4141511.42],
};

// local (x, y) → PROJ (lon, lat), from:
//   Transformer('+proj=utm +zone=10 …', '+proj=longlat +datum=WGS84')
const REFERENCE: Array<[number, number, number, number]> = [
  [3184.38, 1265.59, -122.254818299, 37.429468383],
  [0.0, 0.0, -122.290915644, 37.418282914],
  [3885.4, 4620.4, -122.246592302, 37.459654974],
  [1500.0, 2300.0, -122.273764981, 37.438910042],
];

test("projects local-frame points to WGS84 within 0.1 m of PROJ", () => {
  const project = makeProjector(FRAME);
  for (const [x, y, wantLon, wantLat] of REFERENCE) {
    const [lon, lat] = project(x, y);
    assert.ok(Math.abs(lon - wantLon) < 1e-6, `lon ${lon} vs ${wantLon}`);
    assert.ok(Math.abs(lat - wantLat) < 1e-6, `lat ${lat} vs ${wantLat}`);
  }
});

test("rejects unsupported projections loudly", () => {
  assert.throws(
    () => makeProjector({ projection: "+proj=merc +datum=WGS84", netOffset: [0, 0] }),
    /only "\+proj=utm/,
  );
  assert.throws(
    () => makeProjector({ projection: "+proj=utm +zone=10 +south", netOffset: [0, 0] }),
    /southern-hemisphere/,
  );
  assert.throws(
    () => makeProjector({ projection: "+proj=utm +zone=99", netOffset: [0, 0] }),
    /only "\+proj=utm/,
  );
});
