// signals.test.ts — the render glue: movement grouping against the static
// network geometry (one head per program+link index at the centroid of
// its bound stop-line entries) and per-tick per-head light colors derived
// from the TSSG table (the wire ships programs, never states — ADR-0006
// M9).

import { test } from "node:test";
import assert from "node:assert/strict";

import { signalHeads, headStatesAtTick, type SignalHead } from "../src/signals.ts";
import type { SignalTable } from "../src/tssg.ts";

// The Go/TS shared fixture (see tssg.test.ts): sigA 10t "Gr" / 5t "yr"
// offset 0; sigB 20t "r" / 10t "G" offset 5.
const TABLE: SignalTable = {
  tick: 0,
  programs: [
    {
      id: "sigA",
      junction: "J1",
      offsetTicks: 0,
      phases: [
        { durationTicks: 10, state: "Gr" },
        { durationTicks: 5, state: "yr" },
      ],
      links: [
        { linkIdx: 0, laneId: "iJ1_0" },
        { linkIdx: 1, laneId: "iJ1_1" },
      ],
    },
    {
      id: "sigB",
      junction: "J2",
      offsetTicks: 5,
      phases: [
        { durationTicks: 20, state: "r" },
        { durationTicks: 10, state: "G" },
      ],
      links: [{ linkIdx: 0, laneId: "iJ2_0" }],
    },
  ],
};

const SHAPES = new Map<string, Array<readonly number[]>>([
  ["iJ1_0", [[100, 200], [110, 205]]],
  ["iJ1_1", [[101, 198], [111, 203]]],
  ["iJ2_0", [[-50, 30], [-40, 30]]],
]);

// Strip the derivation refs (program) so positional fields can deepEqual.
const bare = (heads: SignalHead[]): Array<Partial<SignalHead>> =>
  heads.map(({ id, x, y, linkIdx }) => ({ id, x, y, linkIdx }));

test("signalHeads: one head per program+linkIdx at the stop-line entry", () => {
  const heads = signalHeads(TABLE, SHAPES);
  assert.deepEqual(bare(heads), [
    { id: "sigA:0", x: 100, y: 200, linkIdx: 0 },
    { id: "sigA:1", x: 101, y: 198, linkIdx: 1 },
    { id: "sigB:0", x: -50, y: 30, linkIdx: 0 },
  ]);
});

test("signalHeads: lanes sharing a link index merge to one centroid head", () => {
  const t: SignalTable = {
    tick: 0,
    programs: [
      {
        id: "p",
        junction: "p",
        offsetTicks: 0,
        phases: [{ durationTicks: 4, state: "G" }],
        links: [
          { linkIdx: 0, laneId: "a" },
          { linkIdx: 0, laneId: "b" },
        ],
      },
    ],
  };
  const shapes = new Map<string, Array<readonly number[]>>([
    ["a", [[0, 0]]],
    ["b", [[10, 20]]],
  ]);
  assert.deepEqual(bare(signalHeads(t, shapes)), [{ id: "p:0", x: 5, y: 10, linkIdx: 0 }]);
});

test("signalHeads skips lanes missing from the static network", () => {
  const partial = new Map<string, Array<readonly number[]>>([["iJ1_0", [[1, 2], [3, 4]]]]);
  assert.deepEqual(bare(signalHeads(TABLE, partial)), [{ id: "sigA:0", x: 1, y: 2, linkIdx: 0 }]);
  assert.deepEqual(signalHeads(TABLE, new Map()), []);
});

test("headStatesAtTick: both junctions in step with their programs", () => {
  const heads = signalHeads(TABLE, SHAPES);
  // tick 0: sigA phase "Gr" → link0 green, link1 red; sigB (offset wrap)
  // runs phase "G" → green.
  assert.deepEqual(
    [...(headStatesAtTick(heads, 0) as Map<string, string>).entries()],
    [
      ["sigA:0", "green"],
      ["sigA:1", "red"],
      ["sigB:0", "green"],
    ],
  );
  // tick 10: sigA flips to "yr" → amber/red; sigB runs its phase 0 "r"
  // (begins at the offset, tick 5) → red.
  assert.deepEqual(
    [...(headStatesAtTick(heads, 10) as Map<string, string>).entries()],
    [
      ["sigA:0", "amber"],
      ["sigA:1", "red"],
      ["sigB:0", "red"],
    ],
  );
  // tick 5: sigB's phase 0 "r" begins at its offset → red.
  assert.equal(headStatesAtTick(heads, 5).get("sigB:0"), "red");
  // tick 15: sigA back to "Gr".
  assert.equal(headStatesAtTick(heads, 15).get("sigA:0"), "green");
});

test("headStatesAtTick: link without a state char renders off", () => {
  const t: SignalTable = {
    tick: 0,
    programs: [
      {
        id: "p",
        junction: "p",
        offsetTicks: 0,
        phases: [{ durationTicks: 4, state: "G" }], // 1 char, 2 links
        links: [
          { linkIdx: 0, laneId: "iP_0" },
          { linkIdx: 1, laneId: "iP_1" },
        ],
      },
    ],
  };
  const shapes = new Map<string, Array<readonly number[]>>([
    ["iP_0", [[0, 0]]],
    ["iP_1", [[1, 0]]],
  ]);
  const states = headStatesAtTick(signalHeads(t, shapes), 0);
  assert.equal(states.get("p:0"), "green");
  assert.equal(states.get("p:1"), "off");
});

test("headStatesAtTick: the i280 program shape (820/30/50, offset 0)", () => {
  const i280: SignalTable = {
    tick: 0,
    programs: [
      {
        id: "5464972060",
        junction: "5464972060",
        offsetTicks: 0,
        phases: [
          { durationTicks: 820, state: "GGG" },
          { durationTicks: 30, state: "yyy" },
          { durationTicks: 50, state: "rrr" },
        ],
        links: [
          { linkIdx: 0, laneId: "i5464972060_0_0" },
          { linkIdx: 1, laneId: "i5464972060_0_1" },
          { linkIdx: 2, laneId: "i5464972060_0_2" },
        ],
      },
    ],
  };
  const shapes = new Map<string, Array<readonly number[]>>([
    ["i5464972060_0_0", [[0, 0]]],
    ["i5464972060_0_1", [[3, 0]]],
    ["i5464972060_0_2", [[6, 0]]],
  ]);
  const heads = signalHeads(i280, shapes);
  assert.equal(headStatesAtTick(heads, 819).get("5464972060:0"), "green");
  assert.equal(headStatesAtTick(heads, 820).get("5464972060:0"), "amber");
  assert.equal(headStatesAtTick(heads, 849).get("5464972060:1"), "amber");
  assert.equal(headStatesAtTick(heads, 850).get("5464972060:2"), "red");
  assert.equal(headStatesAtTick(heads, 899).get("5464972060:0"), "red");
  assert.equal(headStatesAtTick(heads, 900).get("5464972060:0"), "green"); // wrap
});
