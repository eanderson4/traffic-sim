// config.test.ts — URL query parsing, focused on ?dt= (the engine
// timestep the sim-time speed derivation divides by).

import { test } from "node:test";
import assert from "node:assert/strict";

import { loadConfig } from "../src/config.ts";

test("dt defaults to the engine's 0.1 s timestep", () => {
  assert.equal(loadConfig("", "localhost").dt, 0.1);
});

test("dt parses from ?dt=", () => {
  assert.equal(loadConfig("?dt=0.05", "localhost").dt, 0.05);
});

test("invalid dt values fall back to 0.1", () => {
  assert.equal(loadConfig("?dt=abc", "localhost").dt, 0.1);
  assert.equal(loadConfig("?dt=0", "localhost").dt, 0.1);
  assert.equal(loadConfig("?dt=-1", "localhost").dt, 0.1);
});

test("overlay URLs default to the demosrv /overlay/ route, overridable", () => {
  const def = loadConfig("", "localhost");
  assert.equal(def.zonesUrl, "/overlay/zones.geojson");
  assert.equal(def.boundariesUrl, "/overlay/boundaries.geojson");
  assert.equal(def.waterUrl, "/overlay/water.geojson");
  assert.equal(def.buildingsUrl, "/overlay/buildings.geojson");
  const custom = loadConfig(
    "?zones=/o/z.geojson&boundaries=/o/b.geojson&water=/o/w.geojson&buildings=/o/bl.geojson",
    "localhost",
  );
  assert.equal(custom.zonesUrl, "/o/z.geojson");
  assert.equal(custom.boundariesUrl, "/o/b.geojson");
  assert.equal(custom.waterUrl, "/o/w.geojson");
  assert.equal(custom.buildingsUrl, "/o/bl.geojson");
});

test("?bake= selects the baked-replay shim (ADR-0023); default is live", () => {
  assert.equal(loadConfig("", "localhost").bake, null);
  assert.equal(
    loadConfig("?bake=https://data.example.com/baked/run/abc123/index.json", "localhost").bake,
    "https://data.example.com/baked/run/abc123/index.json",
  );
});

// --- ?center= opening camera -------------------------------------------
// The camera exists so a demo can deep-link to ONE intervention. The
// failure that matters is a malformed link on air: it must degrade to the
// bounds fit (camera === null), never to a valid-looking camera at the
// wrong place.

test("no ?center= means no camera — the bounds fit is unchanged", () => {
  assert.equal(loadConfig("", "localhost").camera, null);
  assert.equal(loadConfig("?zoom=15&bearing=90", "localhost").camera, null);
});

test("?center= parses lng,lat and defaults zoom/bearing/pitch", () => {
  const c = loadConfig("?center=-87.6298,41.8781", "localhost").camera;
  assert.notEqual(c, null);
  assert.deepEqual(c!.center, [-87.6298, 41.8781]);
  assert.equal(c!.zoom, 15);
  assert.equal(c!.bearing, 0);
  assert.equal(c!.pitch, 0);
});

test("?zoom=/?bearing=/?pitch= ride along with a valid center", () => {
  const c = loadConfig("?center=-87.63,41.88&zoom=17.5&bearing=45&pitch=60", "localhost").camera;
  assert.equal(c!.zoom, 17.5);
  assert.equal(c!.bearing, 45);
  assert.equal(c!.pitch, 60);
});

test("malformed ?center= falls back to the bounds fit, not to null island", () => {
  for (const bad of [
    "?center=",
    "?center=abc",
    "?center=-87.63",
    "?center=-87.63,41.88,7",
    "?center=nan,41.88",
    "?center=-200,41.88", // lng out of range
    "?center=-87.63,99", // lat out of range
    // Empty components: Number("") is 0, so these are the cases that used to
    // parse "successfully" and open at null island / on the equator. A deep
    // link built from a template that lost one value looks exactly like this.
    "?center=,",
    "?center=,41.88",
    "?center=-87.63,",
    "?center= ,41.88",
  ]) {
    assert.equal(loadConfig(bad, "localhost").camera, null, `expected null camera for ${bad}`);
  }
});

test("out-of-range zoom/bearing/pitch clamp instead of disabling the camera", () => {
  const c = loadConfig("?center=-87.63,41.88&zoom=99&pitch=180", "localhost").camera;
  assert.equal(c!.zoom, 24);
  assert.equal(c!.pitch, 85);
  // A garbage zoom is not a reason to throw away a good center.
  assert.equal(loadConfig("?center=-87.63,41.88&zoom=abc", "localhost").camera!.zoom, 15);
});
