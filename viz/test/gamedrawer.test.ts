// gamedrawer.test.ts — the game drawer's pure half: the four-junction
// design state (base defaults, edit accumulation, defensive copies,
// the differs-from-base "edited" marker), the full-dial run payload
// (play.html's round-2 shape: every junction rides, offset omitted at 0),
// and the leaderboard's slug matcher.

import { test } from "node:test";
import assert from "node:assert/strict";

import { DesignState, buildFullRunPayload, slugifyJS } from "../src/hero/gamedrawer.ts";
import { BASE_GREENS, JUNCTIONS } from "../src/hero/timingpanel.ts";

test("DesignState: every junction starts at the town's base timing, unedited", () => {
  const d = new DesignState();
  assert.deepEqual([...JUNCTIONS], [...d.junctions]);
  for (const j of JUNCTIONS) {
    assert.deepEqual(d.get(j), { greens: [...BASE_GREENS], offset: 0 });
    assert.equal(d.isEdited(j), false);
  }
  assert.equal(d.name, "hero-design");
});

test("DesignState: set accumulates an edit; returning to base clears it", () => {
  const d = new DesignState();
  d.set("J2", [40, 8, 11, 11], 5);
  assert.equal(d.isEdited("J2"), true);
  assert.equal(d.isEdited("J1"), false);
  assert.deepEqual(d.get("J2"), { greens: [40, 8, 11, 11], offset: 5 });
  d.set("J2", [...BASE_GREENS], 0);
  assert.equal(d.isEdited("J2"), false);
  // an offset alone is an edit too
  d.set("J3", [...BASE_GREENS], 12);
  assert.equal(d.isEdited("J3"), true);
});

test("DesignState: get and set are defensive copies (no aliasing)", () => {
  const d = new DesignState();
  const greens = [40, 8, 11, 11];
  d.set("J1", greens, 0);
  greens[0] = 99; // mutating the caller's array must not leak in
  assert.equal(d.get("J1").greens[0], 40);
  const out = d.get("J1");
  out.greens[0] = 99; // mutating the returned copy must not leak either
  assert.equal(d.get("J1").greens[0], 40);
});

test("buildFullRunPayload: all four junctions ride, in order, base at load", () => {
  const p = buildFullRunPayload("hero-design", new DesignState());
  assert.deepEqual(p, {
    name: "hero-design",
    junctions: {
      J1: { greens: [36, 8, 11, 11] },
      J2: { greens: [36, 8, 11, 11] },
      J3: { greens: [36, 8, 11, 11] },
      J4: { greens: [36, 8, 11, 11] },
    },
  });
  assert.deepEqual(Object.keys(p.junctions), [...JUNCTIONS]);
});

test("buildFullRunPayload: edits land per junction, offset omitted only at 0", () => {
  const d = new DesignState();
  d.set("J2", [40, 8, 11, 11], 5);
  d.set("J4", [30.004, 10, 12, 12], 20.499);
  const p = buildFullRunPayload("wave-2", d);
  assert.equal(p.name, "wave-2");
  assert.deepEqual(p.junctions["J1"], { greens: [36, 8, 11, 11] }); // base: no offset key
  assert.deepEqual(p.junctions["J2"], { greens: [40, 8, 11, 11], offset: 5 });
  assert.deepEqual(p.junctions["J4"], { greens: [30, 10, 12, 12], offset: 20.5 }); // 2 dp rounding
});

test("slugifyJS: mirrors score-timing's slug for the current-run highlight", () => {
  assert.equal(slugifyJS("overlay-smoke"), "overlay-smoke");
  assert.equal(slugifyJS("Wave 2!"), "wave-2");
  assert.equal(slugifyJS("--Guest_R1--"), "guest-r1");
});
