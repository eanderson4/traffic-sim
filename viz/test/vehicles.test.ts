// vehicles.test.ts — updateData diff computation: spawn→add, move→update,
// despawn→remove; untouched vehicles produce no diff entries.

import { test } from "node:test";
import assert from "node:assert/strict";

import { diffVehicles, type RenderedVehicle } from "../src/vehicles.ts";

function rv(id: number, lng: number, lat: number, cls = 0): RenderedVehicle {
  return { id, x: 0, y: 0, lngLat: [lng, lat], angle: 0.5, cls, speed: 12.3 };
}

test("spawn adds a full feature; first frame adds everything", () => {
  const prev = new Map<number, RenderedVehicle>();
  const next = new Map([[1, rv(1, -122.25, 37.43)]]);
  const diff = diffVehicles(prev, next);
  assert.equal(diff.add?.length, 1);
  assert.equal(diff.update, undefined);
  assert.equal(diff.remove, undefined);
  const f = diff.add![0]!;
  assert.equal(f.id, 1);
  assert.equal(f.properties["id"], 1);
  assert.deepEqual(f.geometry, { type: "Point", coordinates: [-122.25, 37.43] });
});

test("move updates geometry + style properties; stationary is untouched", () => {
  const prev = new Map([
    [1, rv(1, -122.25, 37.43)],
    [2, rv(2, -122.26, 37.44)],
  ]);
  const moved = rv(1, -122.2501, 37.43);
  const next = new Map([
    [1, moved],
    [2, rv(2, -122.26, 37.44)],
  ]);
  const diff = diffVehicles(prev, next);
  assert.equal(diff.add, undefined);
  assert.equal(diff.update?.length, 1);
  const u = diff.update![0]!;
  assert.equal(u.id, 1);
  assert.deepEqual(u.newGeometry, { type: "Point", coordinates: [-122.2501, 37.43] });
  const keys = u.addOrUpdateProperties!.map((p) => p.key).sort();
  assert.deepEqual(keys, ["angle", "cls", "speed"]);
});

test("despawn removes by id; class change alone triggers an update", () => {
  const prev = new Map([
    [1, rv(1, -122.25, 37.43)],
    [2, rv(2, -122.26, 37.44)],
  ]);
  const next = new Map([[2, rv(2, -122.26, 37.44, 1)]]);
  const diff = diffVehicles(prev, next);
  assert.deepEqual(diff.remove, [1]);
  assert.equal(diff.update?.length, 1);
  assert.equal(diff.update![0]!.id, 2);
});
