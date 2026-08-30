// interp.test.ts — fractional-tick interpolation (hero/interp.ts):
// bracketing (including the off-stride terminal frame and both clamps),
// shortest-path angle lerp across the ±π wrap, and the id-sorted
// two-pointer frame match (spawn/despawn ids are skipped mid-stride).

import { test } from "node:test";
import assert from "node:assert/strict";

import { bracketAt, lerpAngle, walkPairs, type Bracket } from "../src/hero/interp.ts";
import { bakedFrameCount } from "../src/baked.ts";
import type { VehicleRecord } from "../src/tssf.ts";

const INDEX = { tickStart: 0, tickEnd: 15000, bakeEveryTicks: 5 };
const COUNT = bakedFrameCount(INDEX); // 3001

const freshBracket = (): Bracket => ({ k0: -1, tick0: -1, tick1: -1, alpha: -1 });
const bracket = (t: number, index = INDEX, count = COUNT): Bracket => bracketAt(index, count, t, freshBracket());

const veh = (id: number): VehicleRecord => ({ id, x: id * 10, y: 0, angle: 0, cls: 0 });

test("bracket: an exact baked tick sits at alpha 0 on its own frame", () => {
  const b = bracket(5000);
  assert.equal(b.k0, 1000);
  assert.equal(b.tick0, 5000);
  assert.equal(b.tick1, 5005);
  assert.equal(b.alpha, 0);
});

test("bracket: a fractional tick interpolates toward the next baked frame", () => {
  const b = bracket(5002.5);
  assert.equal(b.k0, 1000);
  assert.equal(b.tick0, 5000);
  assert.equal(b.tick1, 5005);
  assert.equal(b.alpha, 0.5);
});

test("bracket: clamps at both ends of the baked range (no extrapolation)", () => {
  const lo = bracket(-50);
  assert.equal(lo.k0, 0);
  assert.equal(lo.tick0, 0);
  assert.equal(lo.alpha, 0);
  const hi = bracket(99999);
  assert.equal(hi.k0, COUNT - 1);
  assert.equal(hi.tick0, 15000);
  assert.equal(hi.tick1, 15000);
  assert.equal(hi.alpha, 0);
});

test("bracket: the off-stride terminal frame uses its true tick spacing", () => {
  const index = { tickStart: 0, tickEnd: 15002, bakeEveryTicks: 5 };
  const count = bakedFrameCount(index); // 3002: …15000, plus terminal 15002
  const mid = bracket(15001, index, count);
  assert.equal(mid.tick0, 15000);
  assert.equal(mid.k0, 3000);
  assert.equal(mid.tick1, 15002); // 2 ticks away, not a full stride
  assert.equal(mid.alpha, 0.5);
  const end = bracket(15002, index, count);
  assert.equal(end.k0, count - 1); // the terminal frame itself
  assert.equal(end.tick0, 15002);
  assert.equal(end.tick1, 15002);
  assert.equal(end.alpha, 0);
});

test("bracketAt reuses the out object (no allocation in the render loop)", () => {
  const out = freshBracket();
  assert.equal(bracketAt(INDEX, COUNT, 10, out), out);
  assert.equal(out.k0, 2);
});

test("lerpAngle wraps the short way across ±π", () => {
  const a = (350 * Math.PI) / 180;
  const b = (10 * Math.PI) / 180;
  const mid = lerpAngle(a, b, 0.5);
  const wrapped = ((mid % (2 * Math.PI)) + 2 * Math.PI) % (2 * Math.PI);
  // Through 0°, not the 340° long way: midpoint is due east (≈0 ≡ 2π).
  assert.ok(Math.min(wrapped, 2 * Math.PI - wrapped) < 1e-12, `mid=${mid}`);
});

test("lerpAngle keeps direction and hits both endpoints", () => {
  assert.ok(Math.abs(lerpAngle(0.1, -0.1, 0.5)) < 1e-12); // not the 2π−0.2 way
  assert.ok(Math.abs(lerpAngle(0.1, -0.1, 0.25) - 0.05) < 1e-12);
  const a = 1.25;
  const b = 2.75;
  assert.equal(lerpAngle(a, b, 0), a);
  assert.equal(lerpAngle(a, b, 1), b);
});

test("walkPairs fires only for ids in both frames, in ascending order", () => {
  const a = [veh(1), veh(3), veh(5), veh(7)];
  const b = [veh(3), veh(4), veh(5), veh(8)];
  const pairs: Array<[number, number]> = [];
  walkPairs(a, b, (va, vb) => {
    assert.equal(va.id, vb.id);
    pairs.push([va.x, vb.x]);
  });
  // ids 1/7 (a-only, despawning) and 4/8 (b-only, spawning) are skipped.
  assert.deepEqual(pairs, [
    [30, 30],
    [50, 50],
  ]);
});

test("walkPairs with an empty side fires nothing", () => {
  let calls = 0;
  walkPairs([], [veh(1)], () => calls++);
  walkPairs([veh(1)], [], () => calls++);
  walkPairs([], [], () => calls++);
  assert.equal(calls, 0);
});
