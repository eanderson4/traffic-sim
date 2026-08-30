// timingpanel.test.ts — the hero timing panel's pure core: dial validation
// (mirroring play.html / mktimingvariant.py), the single-junction run
// payload shape, the cycle readout sum, the name rule, and the live
// phase label's program walk (offset semantics + green/amber/all-red
// classification on a bottleneck-town-shaped 12-phase program).

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  BASE_GREENS,
  FIXED_S,
  NAME_RE,
  buildRunPayload,
  cycleOf,
  livePhaseLabel,
  runnerAvailable,
  validateDial,
} from "../src/hero/timingpanel.ts";
import type { SigProgram } from "../src/tssg.ts";

const DT = 0.1;

// prog builds a 12-phase fixed-time program in the bottleneck-town shape:
// green / amber / all-red per movement, greens 34/8/11/11 s (the
// rehearsal42 variant's J1 durations) at dt 0.1.
function prog(offsetTicks: number): SigProgram {
  const greens = [340, 80, 110, 110];
  const states = ["GGrGGGGrrrrrrr", "rrGrrrrGrrrrrr", "rrrrrrrrrrrGGG", "rrrrrrrrGGGrrr"];
  const phases = [];
  for (let i = 0; i < 4; i++) {
    phases.push({ durationTicks: greens[i]!, state: states[i]! });
    phases.push({ durationTicks: 30, state: states[i]!.replace(/G/g, "y") });
    phases.push({ durationTicks: 20, state: "rrrrrrrrrrrrrr" });
  }
  return { id: "J1", junction: "J1", offsetTicks, phases, links: [] };
}

test("validateDial: the base dial passes with an 86 s cycle", () => {
  const v = validateDial([...BASE_GREENS], 0);
  assert.deepEqual(v.errs, []);
  assert.equal(v.cycle, 86);
});

test("validateDial: green bounds are 5–90 s, named with the movement", () => {
  const lo = validateDial([4, 8, 11, 11], 0);
  assert.equal(lo.errs.length, 1);
  assert.match(lo.errs[0]!, /through \+ right.*outside \[5, 90\] s/);
  const hi = validateDial([36, 91, 11, 11], 0);
  assert.equal(hi.errs.length, 2); // green AND cycle
  assert.match(hi.errs[0]!, /protected lefts/);
});

test("validateDial: cycle bounds are greens + 20 s clearance, 40–150 s", () => {
  assert.deepEqual(validateDial([5, 5, 5, 5], 0).errs, []); // 40 s: the floor
  assert.deepEqual(validateDial([40, 30, 30, 30], 0).errs, []); // 150 s: the ceiling
  const v = validateDial([41, 40, 30, 30], 0); // 161 s
  assert.equal(v.errs.length, 1);
  assert.match(v.errs[0]!, /cycle 161 s is outside \[40, 150\] s/);
});

test("validateDial: offset wraps at the cycle length", () => {
  assert.deepEqual(validateDial([36, 8, 11, 11], 85).errs, []);
  assert.match(validateDial([36, 8, 11, 11], 86).errs[0]!, /offset 86 s is outside \[0, 86\)/);
  assert.match(validateDial([36, 8, 11, 11], -1).errs[0]!, /outside \[0, 86\)/);
  assert.match(validateDial([36, 8, 11, 11], NaN).errs[0]!, /offset needs a number/);
});

test("validateDial: a half-typed green is a number error, cycle still sums", () => {
  const v = validateDial([NaN, 8, 11, 11], 0);
  assert.deepEqual(v.errs, ["green 1 needs a number"]);
  assert.equal(v.cycle, 8 + 11 + 11 + FIXED_S);
});

test("cycleOf: greens plus the fixed clearance, non-numbers count 0", () => {
  assert.equal(cycleOf([36, 8, 11, 11]), 86);
  assert.equal(cycleOf([NaN, 8, 11, 11]), 50);
});

test("buildRunPayload: single junction, offset omitted at 0 (play.html shape)", () => {
  assert.deepEqual(buildRunPayload("hero-edit", "J2", [36, 8, 11, 11], 0), {
    name: "hero-edit",
    junctions: { J2: { greens: [36, 8, 11, 11] } },
  });
});

test("buildRunPayload: nonzero offset rides, numbers round to 2 dp", () => {
  assert.deepEqual(buildRunPayload("hero-edit", "J3", [36.004, 8, 11, 11], 20.499), {
    name: "hero-edit",
    junctions: { J3: { greens: [36, 8, 11, 11], offset: 20.5 } },
  });
});

test("NAME_RE: lowercase letters, digits, dashes", () => {
  assert.ok(NAME_RE.test("hero-edit"));
  assert.ok(NAME_RE.test("a"));
  assert.ok(!NAME_RE.test("Hero"));
  assert.ok(!NAME_RE.test("-bad"));
  assert.ok(!NAME_RE.test("bad-"));
  assert.ok(!NAME_RE.test("has space"));
});

test("runnerAvailable: only a 200 health probe means live", () => {
  assert.equal(runnerAvailable(200), true);
  assert.equal(runnerAvailable(404), false); // static host
  assert.equal(runnerAvailable(405), false); // static host, method not allowed
  assert.equal(runnerAvailable(500), false);
  assert.equal(runnerAvailable(302), false);
  assert.equal(runnerAvailable(null), false); // network error
});

test("livePhaseLabel: green phase names its movement and counts down", () => {
  const label = livePhaseLabel(prog(0), 0, DT);
  assert.match(label, /through \+ right/);
  assert.match(label, /green · 34 s left/);
  // 5 s into the 34 s green: 29 s left
  assert.match(livePhaseLabel(prog(0), 50, DT), /green · 29 s left/);
});

test("livePhaseLabel: amber belongs to the green it follows", () => {
  // 340 green + 5 ticks into the 30-tick amber: 25 ticks = 2.5 s → ceil 3
  const label = livePhaseLabel(prog(0), 345, DT);
  assert.match(label, /through \+ right/);
  assert.match(label, /amber · 3 s left/);
});

test("livePhaseLabel: the all-r phase reads as all-red clearance", () => {
  // 340 + 30 + 15 ticks into the 20-tick all-red: 5 ticks = 0.5 s → 1 s
  assert.match(livePhaseLabel(prog(0), 385, DT), /all-red clearance · 1 s left/);
});

test("livePhaseLabel: SUMO offset semantics wrap the cycle", () => {
  // offset 200 ticks shifts the 840-tick cycle: tick 730 lands 10 ticks
  // into the third green (cross NB, 110 ticks) — 100 ticks = 10 s left.
  const label = livePhaseLabel(prog(200), 730, DT);
  assert.match(label, /northbound/);
  assert.match(label, /green · 10 s left/);
  // and tick 0 sits 10 ticks into that green's amber (2 s left).
  const amber = livePhaseLabel(prog(200), 0, DT);
  assert.match(amber, /northbound/);
  assert.match(amber, /amber · 2 s left/);
});
