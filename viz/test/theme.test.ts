// theme.test.ts — the glyph table (one entry per engine class, real
// dimensions) and the heading conversion the vehicles symbol layer
// evaluates as a style expression (CCW-from-east rad → CW-from-north deg),
// plus the swappable-theme contract (THEMES / getTheme) and the theme
// selection precedence (config.ts resolveThemeName — ?theme= over
// localStorage over navy).

import { test } from "node:test";
import assert from "node:assert/strict";

import { GLYPHS, THEME, THEMES, getTheme, glyphByCls, vehicleBearingDeg } from "../src/theme.ts";
import { loadConfig, resolveThemeName } from "../src/config.ts";

const COLOR_RE = /^#([0-9a-fA-F]{6})$|^rgba\(/;

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

test("THEMES has navy and paper with identical key sets", () => {
  assert.ok(THEMES.navy !== undefined, "missing navy theme");
  assert.ok(THEMES.paper !== undefined, "missing paper theme");
  const keys = (t: object): string[] => Object.keys(t).sort();
  assert.deepEqual(keys(THEMES.paper), keys(THEMES.navy));
});

test("every theme color is a #rrggbb or rgba( string", () => {
  for (const [name, spec] of Object.entries(THEMES)) {
    const { glyphColors, ...colors } = spec;
    for (const [key, value] of Object.entries<string>(colors)) {
      assert.ok(COLOR_RE.test(value), `${name}.${key}: bad color ${value}`);
    }
    for (const [cls, color] of Object.entries<string>(glyphColors)) {
      assert.ok(COLOR_RE.test(color), `${name}.glyphColors[${cls}]: bad color ${color}`);
    }
  }
});

test("navy preserves the historical palette exactly (zero visual change)", () => {
  assert.equal(THEME, THEMES.navy); // back-compat alias
  assert.equal(THEMES.navy.bg, "#0e1d5c");
  assert.equal(THEMES.navy.casing, "#122881");
  assert.equal(THEMES.navy.noData, "#7e9dff");
  assert.equal(THEMES.navy.stopped, "#e5484d");
  assert.equal(THEMES.navy.mid, "#e8b43a");
  assert.equal(THEMES.navy.freeFlow, "#1e9e6a");
  assert.equal(THEMES.navy.hudBg, "rgba(14, 29, 92, 0.82)");
  assert.equal(THEMES.navy.hudBorder, "#2e5bff");
  assert.equal(THEMES.navy.hudText, "#d6e1ff");
  assert.equal(THEMES.navy.hudTextDim, "#9fb2e8");
  assert.equal(THEMES.navy.overlayBg, "rgba(14, 29, 92, 0.94)");
  assert.equal(THEMES.navy.sigHousing, "#0a1230");
  assert.equal(THEMES.navy.sigStroke, "rgba(214, 225, 255, 0.55)");
  assert.equal(THEMES.navy.sigDim, "#1d2950");
  assert.equal(THEMES.navy.glyphHalo, "#0e1d5c");
  assert.deepEqual(THEMES.navy.glyphColors, { 0: "#eaf0ff", 1: "#ff7d4d" });
});

test("signals stay semantic green/amber/red in both themes", () => {
  for (const spec of Object.values(THEMES)) {
    assert.equal(spec.signalGreen, "#2ecc71");
    assert.equal(spec.signalAmber, "#f5b301");
    assert.equal(spec.signalRed, "#e5484d");
  }
});

test("the stop sign stays semantic red with a white rim in both themes", () => {
  for (const spec of Object.values(THEMES)) {
    assert.equal(spec.stopFace, "#e5484d");
    assert.equal(spec.stopRim, "#ffffff");
  }
});

test("getTheme resolves known names and falls back to navy", () => {
  assert.equal(getTheme("navy"), THEMES.navy);
  assert.equal(getTheme("paper"), THEMES.paper);
  assert.equal(getTheme("bogus"), THEMES.navy);
  assert.equal(getTheme(""), THEMES.navy);
});

test("glyphByCls takes the glyph color from the theme, dims stay put", () => {
  assert.equal(glyphByCls(0).color, "#eaf0ff"); // default = navy
  assert.equal(glyphByCls(0, THEMES.paper).color, "#26262b");
  assert.equal(glyphByCls(1, THEMES.paper).color, "#2563eb");
  assert.equal(glyphByCls(1, THEMES.paper).lengthM, 12); // dims theme-independent
});

test("resolveThemeName: ?theme= wins over storage, storage over navy", () => {
  assert.equal(resolveThemeName(null, null), "navy");
  assert.equal(resolveThemeName(null, "paper"), "paper");
  assert.equal(resolveThemeName("paper", "navy"), "paper"); // URL beats storage
  assert.equal(resolveThemeName("navy", "paper"), "navy"); // explicit navy wins too
  assert.equal(resolveThemeName("", "paper"), "paper"); // empty param = absent
  assert.equal(resolveThemeName(null, ""), "navy"); // empty storage = absent
  // Unknown names pass through here; getTheme maps them to navy.
  assert.equal(getTheme(resolveThemeName("bogus", "paper")), THEMES.navy);
});

test("loadConfig resolves the theme (no Web Storage in node → navy)", () => {
  assert.equal(loadConfig("?theme=paper", "localhost").theme, "paper");
  assert.equal(loadConfig("", "localhost").theme, "navy");
  assert.equal(loadConfig("?net=/net/x.geojson", "localhost").theme, "navy");
});

test("residential and workplace buildings are distinguishable in every theme", () => {
  for (const [name, spec] of Object.entries(THEMES)) {
    assert.notEqual(
      spec.buildingResidential,
      spec.buildingWorkplace,
      `${name}: the two demand kinds must not share a color — the split is the layer`,
    );
    assert.notEqual(spec.buildingOther, spec.buildingResidential, name);
    assert.notEqual(spec.buildingOther, spec.buildingWorkplace, name);
    // Buildings sit under the roads: they must not be the water fill either.
    assert.notEqual(spec.buildingResidential, spec.water, name);
    assert.notEqual(spec.buildingWorkplace, spec.water, name);
  }
});
