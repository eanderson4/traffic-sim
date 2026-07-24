// signals.test.ts — the render glue: movement clustering against the
// static network geometry (one head per program+state-column+approach
// cluster — links that change together forever AND share an approach
// merge — at the centroid of its bound stop-line entries, set back
// HEAD_SETBACK_M along the approach bearing) and per-tick per-head light
// colors derived from the TSSG table (the wire ships programs, never
// states — ADR-0006 M9).

import { test } from "node:test";
import assert from "node:assert/strict";

import { signalHeads, headStatesAtTick, HEAD_SETBACK_M, type SignalHead } from "../src/signals.ts";
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

// Second points are axis-aligned so the setback arithmetic stays exact:
// bearing (1,0), head x = stop-line x − HEAD_SETBACK_M.
const SHAPES = new Map<string, Array<readonly number[]>>([
  ["iJ1_0", [[100, 200], [110, 200]]],
  ["iJ1_1", [[101, 198], [111, 198]]],
  ["iJ2_0", [[-50, 30], [-40, 30]]],
]);

// Strip the derivation refs (program) so positional fields can deepEqual.
const bare = (heads: SignalHead[]): Array<Partial<SignalHead>> =>
  heads.map(({ id, x, y, linkIdx }) => ({ id, x, y, linkIdx }));

test("signalHeads: one head per distinct state column, set back along the approach", () => {
  // sigA's links run DIFFERENT columns ("Gy" vs "rr") → two heads; sigB
  // has one. Each head sits HEAD_SETBACK_M behind its stop-line entry.
  const heads = signalHeads(TABLE, SHAPES);
  assert.deepEqual(bare(heads), [
    { id: "sigA:0", x: 100 - HEAD_SETBACK_M, y: 200, linkIdx: 0 },
    { id: "sigA:1", x: 101 - HEAD_SETBACK_M, y: 198, linkIdx: 1 },
    { id: "sigB:0", x: -50 - HEAD_SETBACK_M, y: 30, linkIdx: 0 },
  ]);
});

test("signalHeads: identical state columns across link indices merge to one head", () => {
  const t: SignalTable = {
    tick: 0,
    programs: [
      {
        id: "p",
        junction: "p",
        offsetTicks: 0,
        phases: [
          { durationTicks: 4, state: "GG" },
          { durationTicks: 2, state: "rr" },
        ],
        links: [
          { linkIdx: 0, laneId: "a" },
          { linkIdx: 1, laneId: "b" },
        ],
      },
    ],
  };
  const shapes = new Map<string, Array<readonly number[]>>([
    ["a", [[0, 0]]],
    ["b", [[10, 20]]],
  ]);
  // Same column ("Gr") on both links → ONE head at the centroid;
  // single-point shapes carry no bearing → no setback.
  assert.deepEqual(bare(signalHeads(t, shapes)), [{ id: "p:0", x: 5, y: 10, linkIdx: 0 }]);
});

test("signalHeads: opposing approaches with identical columns stay TWO heads", () => {
  // The symmetric fixed-time junction: NS-north and NS-south throughs run
  // the same column forever, but merging them would centroid the head
  // into the junction's middle with cancelled bearings — the artifact the
  // clustering exists to prevent.
  const t: SignalTable = {
    tick: 0,
    programs: [
      {
        id: "p",
        junction: "p",
        offsetTicks: 0,
        phases: [
          { durationTicks: 4, state: "GG" },
          { durationTicks: 2, state: "rr" },
        ],
        links: [
          { linkIdx: 0, laneId: "north" },
          { linkIdx: 1, laneId: "south" },
        ],
      },
    ],
  };
  const shapes = new Map<string, Array<readonly number[]>>([
    ["north", [[0, 0], [10, 0]]], // enters heading +x
    ["south", [[60, 0], [50, 0]]], // enters heading −x (the opposite mouth)
  ]);
  // Bearings diverge 180° → two clusters; each head sets back along its
  // OWN approach (north pulls −x, south pulls +x).
  assert.deepEqual(bare(signalHeads(t, shapes)), [
    { id: "p:0", x: 0 - HEAD_SETBACK_M, y: 0, linkIdx: 0 },
    { id: "p:1", x: 60 + HEAD_SETBACK_M, y: 0, linkIdx: 1 },
  ]);
});

test("signalHeads: same-link lanes split by geometry still get unique ids", () => {
  // Two lanes of the SAME link index on opposite mouths (a wide split
  // approach): same column, divergent bearings → two clusters founded on
  // rep 0; the second takes an ordinal suffix so feature-state keys never
  // collide.
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
    ["a", [[0, 0], [10, 0]]],
    ["b", [[60, 0], [50, 0]]],
  ]);
  const heads = signalHeads(t, shapes);
  assert.equal(heads.length, 2);
  assert.deepEqual(
    heads.map((h) => h.id),
    ["p:0", "p:0#1"],
  );
  // Same rep → same derivation → both heads the same light.
  const states = headStatesAtTick(heads, 0);
  assert.equal(states.get("p:0"), "green");
  assert.equal(states.get("p:0#1"), "green");
});

test("signalHeads: skewed arms 60° apart are distinct approaches (the Wilshire X)", () => {
  // Same column, bearings 60° apart — inside the old 90° cone these
  // merged into one head; the 45° cone keeps skew-junction arms separate.
  const t: SignalTable = {
    tick: 0,
    programs: [
      {
        id: "p",
        junction: "p",
        offsetTicks: 0,
        phases: [{ durationTicks: 4, state: "GG" }],
        links: [
          { linkIdx: 0, laneId: "a" },
          { linkIdx: 1, laneId: "b" },
        ],
      },
    ],
  };
  const shapes = new Map<string, Array<readonly number[]>>([
    ["a", [[0, 0], [10, 0]]], // 0°
    ["b", [[60, 0], [65, 8.660254]]], // 60°
  ]);
  const heads = signalHeads(t, shapes);
  assert.equal(heads.length, 2);
  // Near-parallel lanes (10° apart) DO merge — same approach, curved entry.
  const shapes2 = new Map<string, Array<readonly number[]>>([
    ["a", [[0, 0], [10, 0]]], // 0°
    ["b", [[0, 5], [9.848, 6.736]]], // ~10°
  ]);
  assert.equal(signalHeads(t, shapes2).length, 1);
});

test("signalHeads: the stop bar spans the bound lanes square to the approach", () => {
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
    ["a", [[0, 0], [10, 0]]],
    ["b", [[6, 0], [16, 0]]],
  ]);
  const heads = signalHeads(t, shapes);
  assert.equal(heads.length, 1);
  // Bearing +x → bar vertical through the centroid (3,0), entries collapse
  // to one point on the perpendicular axis → exactly ±BAR_EXTEND_M.
  assert.deepEqual(heads[0]!.bar, [3, -1.6, 3, 1.6]);
  // Laterally separated lanes: the bar spans the full lane group, not
  // just the extension — entries (0,0) and (0,6) → bar (0,-1.6)–(0,7.6).
  const shapesWide = new Map<string, Array<readonly number[]>>([
    ["a", [[0, 0], [10, 0]]],
    ["b", [[0, 6], [10, 6]]],
  ]);
  const wide = signalHeads(t, shapesWide)[0]!.bar!;
  const want = [0, -1.6, 0, 7.6];
  for (let i = 0; i < 4; i++) assert.ok(Math.abs(wide[i]! - want[i]!) < 1e-9, `bar[${i}] = ${wide[i]}, want ${want[i]}`);
  // No usable bearings → no bar (the head still renders).
  const shapes3 = new Map<string, Array<readonly number[]>>([["a", [[0, 0]]], ["b", [[6, 0]]]]);
  assert.equal(signalHeads(t, shapes3)[0]!.bar, null);
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
  const partial = new Map<string, Array<readonly number[]>>([["iJ1_0", [[1, 2], [4, 2]]]]);
  assert.deepEqual(bare(signalHeads(TABLE, partial)), [
    { id: "sigA:0", x: 1 - HEAD_SETBACK_M, y: 2, linkIdx: 0 },
  ]);
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
  // Different columns ("G" vs absent) → two heads; link 1 has no char.
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
  // All three links share the column "Gyr" → ONE head (the per-lane
  // duplicate cluster this grouping exists to remove).
  const heads = signalHeads(i280, shapes);
  assert.equal(heads.length, 1);
  assert.equal(headStatesAtTick(heads, 819).get("5464972060:0"), "green");
  assert.equal(headStatesAtTick(heads, 820).get("5464972060:0"), "amber");
  assert.equal(headStatesAtTick(heads, 849).get("5464972060:0"), "amber");
  assert.equal(headStatesAtTick(heads, 850).get("5464972060:0"), "red");
  assert.equal(headStatesAtTick(heads, 899).get("5464972060:0"), "red");
  assert.equal(headStatesAtTick(heads, 900).get("5464972060:0"), "green"); // wrap
});
