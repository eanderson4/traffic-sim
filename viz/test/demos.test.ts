// demos.test.ts — the menu page's pure URL builder: it must stay
// byte-identical to what demosrv returns from POST /api/demo/{id}/start
// (engine/cmd/demosrv/main.go), because the running-card deep link skips
// the start round-trip and navigates on this function alone.

import { test } from "node:test";
import assert from "node:assert/strict";

import { buildAppURL, buildReplayURL, deepLinkURL, startPath } from "../src/demos-core.ts";
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

test("startPath routes activation by kind (demo → serve, replay → replay driver)", () => {
  assert.equal(startPath({ id: "i280-baseline", kind: "demo" }), "/api/demo/i280-baseline/start");
  assert.equal(startPath({ id: "rec1", kind: "replay" }), "/api/replay/rec1/start");
});

test("buildReplayURL deep-links the replay live plane ({run}-replay, no dt hint)", () => {
  assert.equal(
    buildReplayURL({ id: "rec1", run: "baseline" }),
    "/app/?run=baseline-replay&net=/net/rec1.geojson",
  );
});

test("deepLinkURL uses the live run id when /api/status carries one", () => {
  // Live demos spawn with a per-spawn unique run id; the running card
  // must navigate to THAT, not the registry's.
  assert.equal(
    deepLinkURL({ id: "sf", run: "sf", kind: "demo" }, undefined, "sf-t9"),
    "/app/?run=sf-t9&net=/net/sf.geojson",
  );
});

test("deepLinkURL picks the running-card URL by kind", () => {
  assert.equal(
    deepLinkURL({ id: "i280-baseline", run: "baseline", kind: "demo" }),
    "/app/?run=baseline&net=/net/i280-baseline.geojson",
  );
  assert.equal(
    deepLinkURL({ id: "rec1", run: "baseline", kind: "replay" }),
    "/app/?run=baseline-replay&net=/net/rec1.geojson",
  );
});

test("buildAppURL/buildReplayURL append &ws= when demosrv runs the engine off-port", () => {
  const ws = "ws://127.0.0.1:9443";
  assert.equal(
    buildAppURL({ id: "la", run: "la" }, ws),
    "/app/?run=la&net=/net/la.geojson&ws=ws%3A%2F%2F127.0.0.1%3A9443",
  );
  assert.equal(
    deepLinkURL({ id: "rec1", run: "baseline", kind: "replay" }, ws),
    "/app/?run=baseline-replay&net=/net/rec1.geojson&ws=ws%3A%2F%2F127.0.0.1%3A9443",
  );
});
