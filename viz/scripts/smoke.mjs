// smoke.mjs — M6 integration smoke test, no browser: build the engine's
// serve mode, start it on the i280 network, connect a node WebSocket client
// via nats.ws (the same package the browser uses), decode TSSF v1 frames
// with the real decoder module (src/tssf.ts), and assert live, plausible
// vehicle data (positions project into the network's WGS84 bounds).
//
// Usage: pnpm smoke   (from viz/; expects the repo layout viz/ + engine/)
// Exit 0 = pass. Kills the serve child on the way out.

import { spawn } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

import { connect } from "nats.ws";
import { decodeFrame } from "../src/tssf.ts";
import { makeProjector } from "../src/proj.ts";

const engineDir = new URL("../../engine/", import.meta.url).pathname;
const netfile = new URL("../../data/networks/i280-woodside/i280.json", import.meta.url).pathname;
const WS = "ws://127.0.0.1:18443";
const RUN = "smoke";
const COLLECT_MS = 8000;
const MIN_FRAMES = 30; // 10 Hz paced, allow slop

const failures = [];
function check(name, cond, detail = "") {
  const ok = !!cond;
  console.log(`  ${ok ? "ok" : "FAIL"}  ${name}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(name);
}

console.log("smoke: building serve…");
const bin = join(mkdtempSync(join(tmpdir(), "ts-serve-")), "serve");
const build = spawn("go", ["build", "-o", bin, "./cmd/serve"], { cwd: engineDir, stdio: "inherit" });
await new Promise((res, rej) => build.on("exit", (c) => (c === 0 ? res() : rej(new Error(`go build exit ${c}`)))));

const work = mkdtempSync(join(tmpdir(), "ts-smoke-"));
const geojsonPath = join(work, "network.geojson");
console.log("smoke: starting serve (run=smoke, ws=127.0.0.1:18443)…");
const serve = spawn(
  bin,
  ["-netfile", netfile, "-run", RUN, "-ticks", "900", "-ws", "127.0.0.1:18443", "-geojson", geojsonPath],
  { stdio: ["ignore", "pipe", "inherit"] },
);
serve.stdout.on("data", (d) => process.stdout.write(`  [serve] ${d}`));

let nc;
try {
  console.log("smoke: waiting for the WebSocket listener…");
  let lastErr;
  for (let i = 0; i < 60 && !nc; i++) {
    try {
      nc = await connect({ servers: WS, timeout: 1000 });
    } catch (e) {
      lastErr = e;
      await sleep(500);
    }
  }
  if (!nc) throw lastErr ?? new Error("connect timeout");
  console.log("smoke: connected, collecting frames…");

  const frames = [];
  const sub = nc.subscribe(`ts.${RUN}.state.snap`);
  const reader = (async () => {
    for await (const msg of sub) {
      try {
        frames.push(decodeFrame(msg.data));
      } catch (e) {
        frames.push({ error: e });
      }
    }
  })();
  await sleep(COLLECT_MS);
  sub.unsubscribe();
  await reader.catch(() => {});

  // Network bounds from the exported GeoJSON (projected to WGS84).
  const net = JSON.parse(readFileSync(geojsonPath, "utf8"));
  const project = makeProjector(net.frame);
  let [minLon, minLat, maxLon, maxLat] = [Infinity, Infinity, -Infinity, -Infinity];
  for (const f of net.features) {
    for (const [x, y] of f.geometry.coordinates) {
      const [lon, lat] = project(x, y);
      minLon = Math.min(minLon, lon);
      maxLon = Math.max(maxLon, lon);
      minLat = Math.min(minLat, lat);
      maxLat = Math.max(maxLat, lat);
    }
  }
  const pad = 0.002; // ~200 m slack around lane geometry

  const bad = frames.filter((f) => f.error);
  const good = frames.filter((f) => !f.error);
  check("≥ 30 decodable frames in 8 s (10 Hz live plane)", good.length >= MIN_FRAMES, `got ${good.length}`);
  check("no decode errors", bad.length === 0, `${bad.length} bad frames: ${bad[0]?.error}`);
  const ticks = good.map((f) => f.tick);
  check("ticks strictly increasing", ticks.every((t, i) => i === 0 || t > ticks[i - 1]));
  const withVehicles = good.filter((f) => f.vehicles.length > 0);
  check("vehicles present (spawned during collection)", withVehicles.length > 0);
  const last = good[good.length - 1];
  check("final frame has live vehicles", last && last.vehicles.length > 0, last && `count=${last.vehicles.length}`);

  let checked = 0;
  let outOfBounds = 0;
  let dupIds = 0;
  let badAngle = 0;
  let nonzeroClass = 0;
  for (const f of withVehicles) {
    const seen = new Set();
    for (const v of f.vehicles) {
      checked++;
      if (seen.has(v.id)) dupIds++;
      seen.add(v.id);
      if (!(Math.abs(v.angle) <= Math.PI + 1e-6)) badAngle++;
      if (v.cls !== 0) nonzeroClass++;
      const [lon, lat] = project(v.x, v.y);
      if (lon < minLon - pad || lon > maxLon + pad || lat < minLat - pad || lat > maxLat + pad) outOfBounds++;
    }
  }
  check(`all ${checked} vehicle positions inside network WGS84 bounds`, outOfBounds === 0, `${outOfBounds} outside`);
  check("vehicle ids unique per frame", dupIds === 0);
  check("angles within ±π", badAngle === 0);
  console.log(`  info: ${checked} vehicle sightings, class≠0: ${nonzeroClass} (single-type scenario)`);
  console.log(`  info: last tick ${last?.tick}, fleet ${last?.vehicles.length}, bounds lat [${minLat.toFixed(4)}, ${maxLat.toFixed(4)}] lon [${minLon.toFixed(4)}, ${maxLon.toFixed(4)}]`);
} finally {
  if (nc) await nc.close().catch(() => {});
  serve.kill("SIGINT");
  await sleep(300);
  serve.kill("SIGKILL");
}

if (failures.length > 0) {
  console.log(`smoke: FAILED (${failures.length} checks)`);
  process.exit(1);
}
console.log("smoke: PASS");
