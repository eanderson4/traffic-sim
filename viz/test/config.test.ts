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
