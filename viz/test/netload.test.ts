// netload.test.ts — the chunked-manifest merge (src/netload.ts): parts
// reassemble in order onto the manifest, the frame survives, the "parts"
// foreign member is dropped, and a plain small-net document passes
// through untouched.

import { test } from "node:test";
import assert from "node:assert/strict";

import { mergeParts, type NetworkDoc, type NetworkFile } from "../src/netload.ts";

const frame = { projection: "+proj=utm +zone=10", netOffset: [-1000, -2000] as [number, number] };

const part = (ids: string[]): NetworkFile => ({
  type: "FeatureCollection",
  frame,
  features: ids.map((id) => ({
    type: "Feature",
    id,
    properties: { id },
    geometry: { type: "LineString", coordinates: [[0, 0], [1, 1]] },
  })),
});

test("mergeParts reassembles features in order and drops the parts member", () => {
  const manifest: NetworkDoc = {
    type: "FeatureCollection",
    frame,
    features: [],
    parts: ["/net/d1.geojson.part-000", "/net/d1.geojson.part-001"],
  };
  const merged = mergeParts(manifest, [part(["a", "b"]), part(["c"])]);
  assert.deepEqual(merged.features.map((f) => f.id), ["a", "b", "c"]);
  assert.equal(merged.frame, frame);
  assert.equal((merged as NetworkDoc).parts, undefined);
  assert.equal(manifest.features.length, 0); // the manifest is not mutated
});

test("mergeParts preserves the manifest's own features first", () => {
  const manifest: NetworkDoc = { type: "FeatureCollection", frame, features: part(["m"]).features, parts: [] };
  const merged = mergeParts(manifest, [part(["p"])]);
  assert.deepEqual(merged.features.map((f) => f.id), ["m", "p"]);
});

test("mergeParts tolerates parts with missing feature arrays", () => {
  const manifest: NetworkDoc = { type: "FeatureCollection", features: [], parts: ["x"] };
  const merged = mergeParts(manifest, [{ type: "FeatureCollection" } as NetworkFile]);
  assert.deepEqual(merged.features, []);
});
