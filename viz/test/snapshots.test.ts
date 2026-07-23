// snapshots.test.ts — interpolation-buffer behavior: lerp between the two
// bracketing snapshots behind the buffer, spawn/despawn mapping, starvation
// hold (no extrapolation), and out-of-order rejection.

import { test } from "node:test";
import assert from "node:assert/strict";

import { SnapshotBuffer } from "../src/snapshots.ts";
import type { SnapshotFrame } from "../src/tssf.ts";

function frame(tick: number, vehicles: Array<[number, number, number]>): SnapshotFrame {
  // [id, x, y]; angle/class 0.
  return {
    tick,
    vehicles: vehicles.map(([id, x, y]) => ({ id, x, y, angle: 0, cls: 0 })),
  };
}

test("empty buffer samples null; single frame renders as-is", () => {
  const b = new SnapshotBuffer(250);
  assert.equal(b.sample(1000), null);
  b.push(frame(1, [[7, 100, 200]]), 1000);
  const s = b.sample(1300);
  assert.ok(s);
  assert.equal(s.vehicles.length, 1);
  assert.equal(s.vehicles[0]?.x, 100);
  assert.equal(s.vehicles[0]?.speed, 0);
  assert.equal(s.tick, 1);
});

test("lerps between the two bracketing snapshots", () => {
  const b = new SnapshotBuffer(250);
  b.push(frame(1, [[1, 0, 0]]), 1000);
  b.push(frame(2, [[1, 10, -4]]), 1100);
  // renderAt = 1050 → t = 0.5
  const s = b.sample(1300)!;
  assert.equal(s.tick, 2);
  const v = s.vehicles[0]!;
  assert.equal(v.x, 5);
  assert.equal(v.y, -2);
  // speed from full snapshot displacement: hypot(10, 4) / 0.1 s
  assert.ok(Math.abs(v.speed - Math.hypot(10, 4) / 0.1) < 1e-9);
});

test("render time before the oldest frame clamps to it", () => {
  const b = new SnapshotBuffer(250);
  b.push(frame(1, [[1, 0, 0]]), 1000);
  b.push(frame(2, [[1, 10, 0]]), 1100);
  const s = b.sample(1200)!; // renderAt = 950 < 1000
  assert.equal(s.vehicles[0]?.x, 0);
  assert.equal(s.starved, false);
});

test("starvation holds the newest snapshot (no extrapolation)", () => {
  const b = new SnapshotBuffer(250);
  b.push(frame(1, [[1, 0, 0]]), 1000);
  b.push(frame(2, [[1, 10, 0]]), 1100);
  const s = b.sample(2000)!; // renderAt = 1750 > newest 1100
  assert.equal(s.starved, true);
  assert.equal(s.vehicles[0]?.x, 10);
});

test("spawn appears at the newer position; despawn leaves the render set", () => {
  const b = new SnapshotBuffer(250);
  b.push(
    frame(1, [
      [1, 0, 0],
      [2, 50, 50],
    ]),
    1000,
  );
  b.push(
    frame(2, [
      [1, 10, 0],
      [3, 90, 90],
    ]),
    1100,
  );
  const s = b.sample(1300)!;
  const byId = new Map(s.vehicles.map((v) => [v.id, v]));
  assert.equal(byId.size, 2);
  assert.equal(byId.get(1)?.x, 5); // lerped
  assert.equal(byId.get(3)?.x, 90); // spawned: newer position as-is
  assert.equal(byId.get(2), undefined); // despawned
});

test("duplicate and out-of-order ticks are dropped", () => {
  const b = new SnapshotBuffer(250);
  b.push(frame(5, [[1, 1, 1]]), 1000);
  b.push(frame(5, [[1, 2, 2]]), 1001); // duplicate
  b.push(frame(4, [[1, 3, 3]]), 1002); // older
  b.push(frame(6, [[1, 4, 4]]), 1003);
  const s = b.sample(2000)!;
  assert.equal(s.tick, 6);
  assert.equal(s.vehicles[0]?.x, 4);
});

test("shortest-arc angle lerp wraps across ±π", () => {
  const b = new SnapshotBuffer(250);
  const a: SnapshotFrame = { tick: 1, vehicles: [{ id: 1, x: 0, y: 0, angle: Math.PI - 0.1, cls: 0 }] };
  const c: SnapshotFrame = { tick: 2, vehicles: [{ id: 1, x: 0, y: 0, angle: -Math.PI + 0.1, cls: 0 }] };
  b.push(a, 1000);
  b.push(c, 1100);
  const s = b.sample(1300)!;
  const angle = s.vehicles[0]!.angle;
  // Midpoint of the short way around: |angle| ≈ π, not 0.
  assert.ok(Math.abs(Math.abs(angle) - Math.PI) < 1e-6, `angle=${angle}`);
});


test("speed derives from sim time (tick delta × dt): 1× and 8× read the same", () => {
  // Same wall-clock arrival pattern (10 Hz delivery), same vehicle speed
  // (10 m/s). At 8× replay each snapshot spans 8 ticks and 8 m of travel;
  // the derived speed must not change with the pace.
  const rt = new SnapshotBuffer(250);
  rt.push(frame(1, [[1, 0, 0]]), 1000);
  rt.push(frame(2, [[1, 1, 0]]), 1100);
  const fast = new SnapshotBuffer(250);
  fast.push(frame(1, [[1, 0, 0]]), 1000);
  fast.push(frame(9, [[1, 8, 0]]), 1100);
  const s1 = rt.sample(1300)!;
  const s8 = fast.sample(1300)!;
  assert.ok(Math.abs(s1.vehicles[0]!.speed - 10) < 1e-9, `1× speed=${s1.vehicles[0]!.speed}`);
  assert.equal(s8.vehicles[0]!.speed, s1.vehicles[0]!.speed);
});

test("speed is independent of the wall-clock arrival rate", () => {
  // 8× realtime, one tick per snapshot delivered at 80 Hz: the wall span
  // is 12.5 ms but the sim span is still 1 tick × 0.1 s.
  const b = new SnapshotBuffer(250);
  b.push(frame(1, [[1, 0, 0]]), 1000);
  b.push(frame(2, [[1, 1, 0]]), 1012.5);
  const s = b.sample(1300)!; // starved-hold on the newest pair
  assert.ok(Math.abs(s.vehicles[0]!.speed - 10) < 1e-9, `speed=${s.vehicles[0]!.speed}`);
});

test("custom dt scales the sim-time speed", () => {
  const b = new SnapshotBuffer(250, 0.05);
  b.push(frame(1, [[1, 0, 0]]), 1000);
  b.push(frame(3, [[1, 1, 0]]), 1100);
  // 2 ticks × 0.05 s = 0.1 s sim span → 10 m/s
  assert.ok(Math.abs(b.sample(1300)!.vehicles[0]!.speed - 10) < 1e-9);
});
