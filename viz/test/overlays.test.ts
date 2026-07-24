// overlays.test.ts — the pure overlay helpers: doc validation (a missing
// or garbage /overlay/ response must degrade to "no overlay", never throw
// in main.ts) and the zone style classification stamped for the MapLibre
// layers (zkind/zrun — line-dasharray is not data-driven, so the layers
// filter on these instead of reading kind/status directly).

import { test } from "node:test";
import assert from "node:assert/strict";

import { parseOverlay, prepareZones, zoneKind, zoneRunnable } from "../src/overlays.ts";

test("parseOverlay accepts a FeatureCollection and keeps it verbatim", () => {
  const fc = {
    type: "FeatureCollection",
    features: [
      {
        type: "Feature",
        properties: { name: "Berkeley", admin_level: 8 },
        geometry: { type: "Polygon", coordinates: [[[0, 0], [1, 0], [1, 1], [0, 0]]] },
      },
    ],
  };
  assert.deepEqual(parseOverlay(fc), fc);
});

test("parseOverlay rejects non-FeatureCollection docs as null", () => {
  assert.equal(parseOverlay(null), null);
  assert.equal(parseOverlay(undefined), null);
  assert.equal(parseOverlay("not json"), null);
  assert.equal(parseOverlay({ type: "Feature" }), null);
  assert.equal(parseOverlay({ type: "FeatureCollection" }), null); // no features array
  assert.equal(parseOverlay({ type: "FeatureCollection", features: "x" }), null);
});

test("parseOverlay drops geometry-less features but keeps the valid ones", () => {
  const fc = parseOverlay({
    type: "FeatureCollection",
    features: [
      { type: "Feature", properties: {}, geometry: null },
      {
        type: "Feature",
        properties: { name: "ok" },
        geometry: { type: "Point", coordinates: [0, 0] },
      },
    ],
  });
  assert.equal(fc?.features.length, 1);
  assert.equal(fc?.features[0]?.properties?.["name"], "ok");
});

test("zoneKind: only an explicit corridor is a corridor, unknown kinds degrade to district", () => {
  assert.equal(zoneKind({ kind: "corridor" }), "corridor");
  assert.equal(zoneKind({ kind: "district" }), "district");
  assert.equal(zoneKind({ kind: "region" }), "district");
  assert.equal(zoneKind({}), "district");
  assert.equal(zoneKind(null), "district");
});

test("zoneRunnable: only the literal runnable status is solid, pending is muted", () => {
  assert.equal(zoneRunnable({ status: "runnable" }), true);
  assert.equal(zoneRunnable({ status: "import-pending" }), false);
  assert.equal(zoneRunnable({}), false);
  assert.equal(zoneRunnable(null), false);
});

test("prepareZones stamps zkind/zrun and falls back from label to name", () => {
  const fc = prepareZones({
    type: "FeatureCollection",
    features: [
      {
        type: "Feature",
        properties: { name: "ohare", label: "O'Hare", kind: "district", status: "import-pending" },
        geometry: { type: "Polygon", coordinates: [[[0, 0], [1, 0], [1, 1], [0, 0]]] },
      },
      {
        type: "Feature",
        properties: { name: "i90", kind: "corridor", status: "runnable" },
        geometry: { type: "Polygon", coordinates: [[[0, 0], [1, 0], [1, 1], [0, 0]]] },
      },
    ],
  });
  const [d, c] = fc.features;
  assert.equal(d?.properties?.["zkind"], "district");
  assert.equal(d?.properties?.["zrun"], 0);
  assert.equal(d?.properties?.["label"], "O'Hare");
  assert.equal(c?.properties?.["zkind"], "corridor");
  assert.equal(c?.properties?.["zrun"], 1);
  assert.equal(c?.properties?.["label"], "i90"); // label fell back to name
});
