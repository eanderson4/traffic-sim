// theme.test.ts — the glyph table (one entry per engine class, real
// dimensions) and the heading conversion the vehicles symbol layer
// evaluates as a style expression (CCW-from-east rad → CW-from-north deg).

import { test } from "node:test";
import assert from "node:assert/strict";

import { GLYPHS, vehicleBearingDeg } from "../src/theme.ts";

test("glyph table covers car (cls 0) and truck (cls 1) with positive dims", () => {
  const byCls = new Map(GLYPHS.map((g) => [g.cls, g]));
  for (const cls of [0, 1]) {
    const g = byCls.get(cls);
    assert.ok(g !== undefined, `missing glyph for cls ${cls}`);
    assert.ok(g.lengthM > 0 && g.widthM > 0, `cls ${cls}: dims must be positive`);
    assert.ok(g.lengthM > g.widthM, `cls ${cls}: longer than wide`);
  }
  // Truck reads longer than car (engine: 12 m vs 5 m).
  assert.ok(byCls.get(1)!.lengthM > byCls.get(0)!.lengthM);
});

test("vehicleBearingDeg converts CCW-from-east to CW-from-north", () => {
  // Wrap-aware: 359.999… and 0 are the same bearing (float noise).
  const close = (got: number, want: number): void => {
    const d = Math.abs(got - want) % 360;
    assert.ok(Math.min(d, 360 - d) < 1e-9, `got ${got}, want ${want}`);
  };
  close(vehicleBearingDeg(0), 90); // east → bearing 90
  close(vehicleBearingDeg(Math.PI / 2), 0); // north → bearing 0
  close(vehicleBearingDeg(Math.PI), 270); // west → bearing 270 (≡ -90)
  close(vehicleBearingDeg(-Math.PI / 2), 180); // south (negative angle) → 180
  close(vehicleBearingDeg((3 * Math.PI) / 2), 180); // south via wraparound → 180
  close(vehicleBearingDeg(2 * Math.PI), 90); // full turn ≡ east
});
