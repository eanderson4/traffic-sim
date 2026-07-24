// artic.test.ts — the client-side single-track trailer model: spawn
// alignment, straight tracking, steady-state articulation in a constant
// turn (correct sign — the trailer cuts inside), hold-while-queued, ±π
// wrap, sim-dt substep integration, and seek reset.

import { test } from "node:test";
import assert from "node:assert/strict";

import { Articulator, wrapPi } from "../src/artic.ts";
import { TRACTOR_M, TRAILER_M } from "../src/theme.ts";

const DT = 1 / 60;

// drive runs frames with a per-frame hook supplying (x, y, θ_front, v).
function drive(
  a: Articulator,
  id: number,
  frames: number,
  fn: (i: number) => { x: number; y: number; theta: number; v: number },
): void {
  for (let i = 0; i < frames; i++) {
    const s = fn(i);
    a.update(id, s.x, s.y, s.theta, s.v, DT);
  }
}

test("spawn initializes the trailer aligned with the tractor", () => {
  const a = new Articulator();
  const pose = a.update(1, 100, 50, 0.7, 10, DT);
  assert.equal(pose.trailerAngle, 0.7);
  assert.equal(pose.tractorAngle, 0.7);
  // Tractor center sits TRACTOR_M/2 behind the front bumper along θ.
  assert.ok(Math.abs(pose.tractorX - (100 - (TRACTOR_M / 2) * Math.cos(0.7))) < 1e-12);
  assert.ok(Math.abs(pose.tractorY - (50 - (TRACTOR_M / 2) * Math.sin(0.7))) < 1e-12);
  // Trailer center sits TRAILER_M/2 behind the hitch.
  const hx = 100 - TRACTOR_M * Math.cos(0.7);
  const hy = 50 - TRACTOR_M * Math.sin(0.7);
  assert.ok(Math.abs(pose.trailerX - (hx - (TRAILER_M / 2) * Math.cos(0.7))) < 1e-12);
  assert.ok(Math.abs(pose.trailerY - (hy - (TRAILER_M / 2) * Math.sin(0.7))) < 1e-12);
});

test("straight travel keeps the trailer aligned (Δθ → 0)", () => {
  const a = new Articulator();
  a.update(1, 0, 0, 0.3, 10, DT); // spawn misaligned-ish at 0.3
  // Heading steps to 0 and stays: the trailer must converge onto it.
  drive(a, 1, 600, (i) => ({ x: 10 * (i + 1) * DT, y: 0, theta: 0, v: 10 }));
  const pose = a.update(1, 101, 0, 0, 10, DT);
  assert.ok(Math.abs(pose.trailerAngle) < 1e-3, `trailer angle ${pose.trailerAngle}`);
});

test("constant turn converges to steady articulation, trailer cutting inside", () => {
  for (const omega of [0.2, -0.2]) {
    const a = new Articulator();
    const v = 10;
    let theta = 0;
    a.update(1, 0, 0, theta, v, DT); // spawn aligned
    const gaps: number[] = [];
    for (let i = 0; i < 1200; i++) {
      theta += omega * DT;
      // Front bumper travels its arc.
      const x = (v / omega) * Math.sin(theta);
      const y = (v / omega) * (1 - Math.cos(theta));
      const pose = a.update(1, x, y, theta, v, DT);
      if (i >= 600) gaps.push(wrapPi(theta - pose.trailerAngle));
    }
    // Steady state: the tractor–trailer gap stops changing…
    const tail = gaps.slice(-100);
    const spread = Math.max(...tail) - Math.min(...tail);
    assert.ok(spread < 1e-3, `ω=${omega}: gap not steady (spread ${spread})`);
    // …with the correct sign: CCW turn ⇒ trailer lags (θf − θtr > 0).
    const mean = tail.reduce((s, g) => s + g, 0) / tail.length;
    assert.ok(Math.sign(mean) === Math.sign(omega), `ω=${omega}: gap sign ${mean}`);
    assert.ok(Math.abs(mean) < 0.5, `ω=${omega}: unreasonable articulation ${mean}`);
  }
});

test("stopped vehicle holds its trailer angle exactly", () => {
  const a = new Articulator();
  a.update(1, 0, 0, 0, 10, DT);
  drive(a, 1, 60, (i) => ({ x: i, y: 0, theta: 0.4, v: 10 })); // articulate a bit
  const held = a.update(1, 60, 0, 0.4, 0.05, DT).trailerAngle;
  // Queued creep (v < 0.1): angle frozen even as θ_front wobbles.
  drive(a, 1, 120, (i) => ({ x: 60 + 0.05 * i, y: 0, theta: 0.4 + 0.01 * Math.sin(i), v: 0.05 }));
  assert.equal(a.update(1, 66, 0, 0.4, 0, DT).trailerAngle, held);
});

test("±π wrap does not spin the trailer", () => {
  const a = new Articulator();
  const theta0 = Math.PI - 0.05;
  a.update(1, 0, 0, theta0, 10, DT); // spawn near +π
  // Heading "jumps" to −π+0.05 — the same physical direction +0.1 rad on.
  drive(a, 1, 120, (i) => ({
    x: -10 * (i + 1) * DT * Math.cos(0.05),
    y: 10 * (i + 1) * DT * Math.sin(0.05),
    theta: -Math.PI + 0.05,
    v: 10,
  }));
  const pose = a.update(1, -20, 0, -Math.PI + 0.05, 10, DT);
  const gap = wrapPi(-Math.PI + 0.05 - pose.trailerAngle);
  assert.ok(Math.abs(gap) < 0.05, `trailer spun: gap ${gap}, θtr ${pose.trailerAngle}`);
});

test("large SIM dt integrates in substeps (fast replay), converging like small steps", () => {
  // One 0.8 s sim delta (an 8× replay frame at dt 0.1) vs the equivalent
  // 48 frames of 1/60 s: the trailer must land (nearly) the same — and
  // far past the old 0.05 s wall-clamp.
  const stepped = new Articulator();
  stepped.update(1, 0, 0, 0, 10, DT);
  let small = 0;
  for (let i = 0; i < 48; i++) {
    small = stepped.update(1, 10 * (i + 1) * DT, 0, 0.3, 10, DT).trailerAngle;
  }
  const big = new Articulator();
  big.update(1, 0, 0, 0, 10, DT);
  const bigAngle = big.update(1, 8, 0, 0.3, 10, 0.8).trailerAngle;
  assert.ok(Math.abs(bigAngle - small) < 0.02, `big ${bigAngle} vs stepped ${small}`);
  const oneStep = new Articulator();
  oneStep.update(1, 0, 0, 0, 10, DT);
  const clamped = oneStep.update(1, 8, 0, 0.3, 10, 0.05).trailerAngle;
  assert.ok(bigAngle > clamped * 4, `substeps ${bigAngle} barely beat the old clamp ${clamped}`);
  // A negative dt (a seek the caller didn't reset) integrates nothing.
  const neg = new Articulator();
  neg.update(1, 0, 0, 0, 10, DT);
  assert.equal(neg.update(1, 1, 0, 0.3, 10, -1).trailerAngle, 0);
});

test("wrapPi folds to the shortest arc", () => {
  assert.ok(Math.abs(wrapPi(0.1) - 0.1) < 1e-12);
  // Exact ±π multiples land on π or −π — the same physical direction.
  assert.ok(Math.abs(Math.abs(wrapPi(3 * Math.PI)) - Math.PI) < 1e-12);
  assert.ok(Math.abs(Math.abs(wrapPi(-3 * Math.PI)) - Math.PI) < 1e-12);
  assert.ok(Math.abs(wrapPi(Math.PI + 0.2) - (-Math.PI + 0.2)) < 1e-12);
});

test("reset drops all trailer state — a seek re-spawns trailers aligned", () => {
  const a = new Articulator();
  // Articulate in a steady turn so the trailer angle diverges from the
  // tractor's heading.
  let theta = 0;
  a.update(1, 0, 0, theta, 10, DT);
  drive(a, 1, 300, (i) => {
    theta += 0.2 * DT;
    return { x: 10 * i * DT, y: 0, theta, v: 10 };
  });
  const turned = a.update(1, 60, 0, theta, 10, DT);
  assert.ok(Math.abs(wrapPi(turned.trailerAngle - turned.tractorAngle)) > 0.01, "pre-reset: articulated");
  // A replay seek teleports the truck (same id); the seek path calls
  // reset — the next frame must re-spawn the trailer aligned, and it must
  // hold even at speed 0 (a paused landing never reconverges otherwise).
  a.reset();
  const landed = a.update(1, 500, 500, -1.2, 0, DT);
  assert.equal(landed.trailerAngle, -1.2);
  assert.equal(landed.tractorAngle, -1.2);
});

test("a huge sim delta re-anchors instead of stepping (rAF suspension can't hang the viewer)", () => {
  const a = new Articulator();
  a.update(1, 0, 0, 0, 10, DT);
  // Articulate so the trailer angle diverges from the tractor's heading.
  drive(a, 1, 100, (i) => ({ x: 10 * i * DT, y: 0, theta: 0.3, v: 10 }));
  const before = a.update(1, 20, 0, 0.3, 10, DT).trailerAngle;
  assert.ok(Math.abs(wrapPi(before - 0.3)) > 0.01, "pre-jump: articulated");
  // Backgrounded tab: 300 sim-seconds land in one update. No hang (the
  // test returning at all proves it), and the trailer re-anchors aligned
  // with the tractor, mini-seek style.
  const landed = a.update(1, 3020, 0, 0.3, 10, 300).trailerAngle;
  assert.equal(landed, 0.3);
  // A delta at the clamp boundary still integrates normally.
  const b = new Articulator();
  b.update(1, 0, 0, 0, 10, DT);
  const two = b.update(1, 20, 0, 0.3, 10, 2).trailerAngle;
  assert.ok(two > 0.15 && two < 0.3, `2 s delta integrates (got ${two})`);
});
