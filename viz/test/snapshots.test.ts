// snapshots.test.ts — interpolation-buffer behavior: lerp between the two
// bracketing snapshots behind the buffer, spawn/despawn mapping, starvation
// hold (no extrapolation), and out-of-order rejection.

import { test } from "node:test";
import assert from "node:assert/strict";

import { SnapshotBuffer, SeekGate } from "../src/snapshots.ts";
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

// --- Replay seek handling (SeekGate + SnapshotBuffer.reset, wired in ---
// --- main.ts's snapshot callback) ----------------------------------------

test("SeekGate: backward and forward-jump are seeks; first/duplicate/normal ticks are not", () => {
  const g = new SeekGate();
  assert.equal(g.observe(10), false); // first frame — nothing to compare
  assert.equal(g.observe(11), false); // normal increment
  assert.equal(g.observe(11), false); // duplicate (paused republication)
  assert.equal(g.observe(250), false); // +240 — the boundary, still progression
  assert.equal(g.observe(491), true); // +241 — forward scrub-ahead seek
  assert.equal(g.observe(492), false); // normal increment after landing
  assert.equal(g.observe(4), true); // backward seek
  assert.equal(g.observe(5), false); // forward from the landing tick
  assert.equal(g.observe(0), true); // seek to the start
});

test("SeekGate: the forward-jump threshold derives from dt (24 sim-seconds)", () => {
  const g = new SeekGate(0.1); // 240-tick window
  assert.equal(g.observe(10), false);
  assert.equal(g.observe(250), false); // +240 — the boundary, still progression
  assert.equal(g.observe(491), true); // +241 — scrub ahead
  const fine = new SeekGate(0.001); // 24000-tick window
  assert.equal(fine.observe(10), false);
  assert.equal(fine.observe(810), false); // +800 — a normal 8× frame at dt 0.001
  assert.equal(fine.observe(24811), true); // +24001 — scrub ahead
});

test("replay seek: buffer resets and renders the landing frame; duplicates don't reset", () => {
  const b = new SnapshotBuffer(250);
  const g = new SeekGate();
  // The harness mirrors main.ts's snapshot callback: on a seek, reset the
  // buffer and fire the overlay clear; then push the landing frame.
  let clears = 0;
  const onFrame = (tick: number, recvMs: number): void => {
    const f = frame(tick, [[1, tick * 10, 0]]);
    if (g.observe(f.tick)) {
      b.reset();
      clears++;
    }
    b.push(f, recvMs);
  };
  onFrame(10, 1000);
  onFrame(11, 1100);
  onFrame(4, 1200); // backward seek
  assert.equal(clears, 1);
  // The landing frame renders as-is — never lerped against tick 11's
  // abandoned future (x 110).
  const s = b.sample(1500)!;
  assert.equal(s.tick, 4);
  assert.equal(s.vehicles[0]!.x, 40);
  onFrame(4, 1300); // paused republication: duplicate tick, no reset
  assert.equal(clears, 1);
  onFrame(5, 1400); // forward: no reset
  assert.equal(clears, 1);
  const s2 = b.sample(1700)!; // starved-hold on the (4, 5) pair
  assert.equal(s2.tick, 5);
  assert.equal(s2.vehicles[0]!.x, 50);
});

test("replay seek: forward stream after a backward seek lerps normally", () => {
  const b = new SnapshotBuffer(250);
  const g = new SeekGate();
  const onFrame = (tick: number, recvMs: number): void => {
    const f = frame(tick, [[1, tick * 10, 0]]);
    if (g.observe(f.tick)) b.reset();
    b.push(f, recvMs);
  };
  onFrame(10, 1000);
  onFrame(11, 1100);
  onFrame(4, 1200); // seek
  onFrame(5, 1300); // replay resumes forward
  // renderAt = 1250 → t = 0.5 between the landing frame and the next one.
  const s = b.sample(1500)!;
  assert.equal(s.tick, 5);
  assert.equal(s.vehicles[0]!.x, 45);
});

test("replay forward scrub: buffer resets, no lerp across the jump", () => {
  const b = new SnapshotBuffer(250);
  const g = new SeekGate();
  let clears = 0;
  const onFrame = (tick: number, recvMs: number): void => {
    const f = frame(tick, [[1, tick * 10, 0]]);
    if (g.observe(f.tick)) {
      b.reset();
      clears++;
    }
    b.push(f, recvMs);
  };
  onFrame(10, 1000);
  onFrame(11, 1100);
  onFrame(1000, 1200); // scrub ahead — +989 ticks, past SeekGate's maxJump
  assert.equal(clears, 1);
  // The landing frame renders as-is — never lerped against tick 11 (x 110),
  // and no bogus speed is derived across the jump.
  const s = b.sample(1500)!;
  assert.equal(s.tick, 1000);
  assert.equal(s.vehicles[0]!.x, 10000);
  assert.equal(s.vehicles[0]!.speed, 0);
  onFrame(1001, 1300); // resumes forward — no reset
  assert.equal(clears, 1);
});

test("setSimDt re-targets speed derivation (the recorded run's authoritative dt wins)", () => {
  // main.ts's onStatus hook: the replay status dt overrides the ?dt= URL
  // hint on the first probe — already-buffered frames then derive speeds
  // against the corrected timestep.
  const b = new SnapshotBuffer(250, 0.1);
  b.push(frame(1, [[1, 0, 0]]), 1000);
  b.push(frame(2, [[1, 1, 0]]), 1100);
  assert.ok(Math.abs(b.sample(1300)!.vehicles[0]!.speed - 10) < 1e-9);
  b.setSimDt(0.05); // the recording was made at dt 0.05, not the URL's 0.1
  assert.ok(Math.abs(b.sample(1300)!.vehicles[0]!.speed - 20) < 1e-9);
  assert.equal(b.simDt, 0.05);
});

test("SeekGate.setSimDt re-derives the threshold when the recorded dt is adopted", () => {
  // main.ts's onStatus path: the URL hinted dt 0.1, the recording was made
  // at dt 0.001 — after adoption, a normal 8× frame (+800 ticks) must NOT
  // trip the gate, while a real scrub still does.
  const g = new SeekGate(0.1);
  assert.equal(g.observe(10), false);
  g.setSimDt(0.001); // 240-tick window → 24000
  assert.equal(g.observe(810), false); // +800 — a normal 8× frame at dt 0.001
  assert.equal(g.observe(1610), false); // another +800 — still progression
  assert.equal(g.observe(25611), true); // +24001 — a real scrub ahead
});

test("setBufferMs re-sizes without dropping interpolation history (ADR-0023 baked cadence)", () => {
  // The 2 Hz bake delivers frames 500 ms apart; sizing the buffer to the
  // cadence (625 ms at 1×) keeps renderAt trailing the newest frame by
  // more than one interval — and the already-buffered frames survive the
  // resize (a rebuild would drop them mid-play).
  const b = new SnapshotBuffer(250, 0.1);
  b.push(frame(0, [[1, 0, 0]]), 1000);
  b.push(frame(5, [[1, 10, 0]]), 1500);
  // 250 ms buffer: renderAt 1250 lerps midway between the two frames.
  assert.ok(Math.abs(b.sample(1500)!.vehicles[0]!.x - 5) < 1e-9);
  b.setBufferMs(625); // baked mode's 1.25 × 500 ms
  // renderAt 875 — behind the oldest frame, which CLAMPS to it (no drop).
  assert.equal(b.sample(1500)!.vehicles[0]!.x, 0);
});
