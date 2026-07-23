// demos.test.ts — the menu page's pure URL builder: it must stay
// byte-identical to what demosrv returns from POST /api/demo/{id}/start
// (engine/cmd/demosrv/main.go), because the running-card deep link skips
// the start round-trip and navigates on this function alone.

import { test } from "node:test";
import assert from "node:assert/strict";

import { buildAppURL } from "../src/demos-core.ts";
import { demoIdFromNetUrl } from "../src/switcher.ts";

test("demoIdFromNetUrl recovers the demo id from the cache URL", () => {
  assert.equal(demoIdFromNetUrl("/net/i280-baseline.geojson"), "i280-baseline");
  assert.equal(demoIdFromNetUrl("/net/fix-roundabout.geojson"), "fix-roundabout");
});

test("demoIdFromNetUrl rejects non-cache and malformed URLs", () => {
  assert.equal(demoIdFromNetUrl("/network.geojson"), null);
  assert.equal(demoIdFromNetUrl("/net/../etc/passwd"), null);
  assert.equal(demoIdFromNetUrl("/net/a b.geojson"), null);
  assert.equal(demoIdFromNetUrl(""), null);
});

test("buildAppURL matches the demosrv start-response URL shape", () => {
  assert.equal(
    buildAppURL({ id: "i280-baseline", run: "baseline" }),
    "/app/?run=baseline&net=/net/i280-baseline.geojson",
  );
});

test("buildAppURL keys the map off run and the network cache off id", () => {
  const url = buildAppURL({ id: "i280-evening", run: "seed-7" });
  assert.ok(url.startsWith("/app/?"), "deep link goes to the map app");
  assert.ok(url.includes("run=seed-7"), "run id feeds ?run= (the NATS subject)");
  assert.ok(url.includes("net=/net/i280-evening.geojson"), "demo id feeds the cached network");
});
