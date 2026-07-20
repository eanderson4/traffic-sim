// signals.test.ts — the render glue: stop-line resolution against the
// static network geometry and per-tick per-lane light colors derived from
// the TSSG table (the wire ships programs, never states — ADR-0006 M9).

import { test } from "node:test";
import assert from "node:assert/strict";

import { signalStopLines, laneStatesAtTick } from "../src/signals.ts";
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

test("signalStopLines resolves bound lanes to their first polyline point", () => {
  const pts = signalStopLines(TABLE, SHAPES);
  assert.deepEqual(pts, [
    { laneId: "iJ1_0", x: 100, y: 200 },
    { laneId: "iJ1_1", x: 101, y: 198 },
    { laneId: "iJ2_0", x: -50, y: 30 },
  ]);
});

test("signalStopLines skips lanes missing from the static network", () => {
  const partial = new Map<string, Array<readonly number[]>>([["iJ1_0", [[1, 2], [3, 4]]]]);
  const pts = signalStopLines(TABLE, partial);
  assert.deepEqual(pts, [{ laneId: "iJ1_0", x: 1, y: 2 }]);
  assert.deepEqual(signalStopLines(TABLE, new Map()), []);
});

test("laneStatesAtTick: both junctions in step with their programs", () => {
  // tick 0: sigA phase "Gr" → link0 green, link1 red; sigB (offset wrap)
  // runs phase "G" → green.
  assert.deepEqual(
    [...(laneStatesAtTick(TABLE, 0) as Map<string, string>).entries()],
    [
      ["iJ1_0", "green"],
      ["iJ1_1", "red"],
      ["iJ2_0", "green"],
    ],
  );
  // tick 10: sigA flips to "yr" → amber/red; sigB runs its phase 0 "r"
  // (begins at the offset, tick 5) → red.
  assert.deepEqual(
    [...(laneStatesAtTick(TABLE, 10) as Map<string, string>).entries()],
    [
      ["iJ1_0", "amber"],
      ["iJ1_1", "red"],
      ["iJ2_0", "red"],
    ],
  );
  // tick 5: sigB's phase 0 "r" begins at its offset → red.
  assert.equal(laneStatesAtTick(TABLE, 5).get("iJ2_0"), "red");
  // tick 15: sigA back to "Gr".
  assert.equal(laneStatesAtTick(TABLE, 15).get("iJ1_0"), "green");
});

test("laneStatesAtTick: link without a state char renders off", () => {
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
  const states = laneStatesAtTick(t, 0);
  assert.equal(states.get("iP_0"), "green");
  assert.equal(states.get("iP_1"), "off");
});

test("laneStatesAtTick: the i280 program shape (820/30/50, offset 0)", () => {
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
  assert.equal(laneStatesAtTick(i280, 819).get("i5464972060_0_0"), "green");
  assert.equal(laneStatesAtTick(i280, 820).get("i5464972060_0_0"), "amber");
  assert.equal(laneStatesAtTick(i280, 849).get("i5464972060_0_1"), "amber");
  assert.equal(laneStatesAtTick(i280, 850).get("i5464972060_0_2"), "red");
  assert.equal(laneStatesAtTick(i280, 899).get("i5464972060_0_0"), "red");
  assert.equal(laneStatesAtTick(i280, 900).get("i5464972060_0_0"), "green"); // wrap
});
