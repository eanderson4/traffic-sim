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
