// bake-furniture.mjs — the ADR-0023 §1 node furniture step: with PMTiles
// (or any baked mode) the browser no longer holds full lane geometry, so
// the client-side signal-head/stop-sign derivations (src/signals.ts,
// src/stopsign.ts) run HERE, verbatim, at bake time, emitting
// furniture.geojson in METRIC coordinates (the shim projects them like
// everything else). Property schema (consumed by src/furniture.ts):
//
//   head: Point      { kind: "head", id, program, link }  — program/link
//   bar:  LineString { kind: "bar",  id }                  bind the head to
//   sign: Point      { kind: "sign", id }                  its TSSG program
//
// Inputs (one bake prefix, ADR-0023 §1's layout): index.json (the TSSG
// framing, signals.chunkBytes), signals.tssg (the concatenated TSSG chunk
// set), and the METRIC network GeoJSON (single doc, or an ADR-0018
// chunked manifest — parts are consumed file-by-file, never one parsed
// document at city scale). The step also patches the index's "furniture"
// member when absent (idempotent) so the manifest points at the output.
//
// Usage: pnpm bake-furniture <bake-prefix-dir>   (from viz/)
// Exit 0 = success; prints a one-line summary (heads/bars/signs/bytes).

import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { decodeSignalFrame } from "../src/tssg.ts";
import { signalHeads } from "../src/signals.ts";
import { stopSigns } from "../src/stopsign.ts";

const dir = process.argv[2];
if (dir === undefined) {
  console.error("usage: node scripts/bake-furniture.mjs <bake-prefix-dir>");
  process.exit(2);
}

const readJson = (path) => JSON.parse(readFileSync(path, "utf8"));

// 1. The manifest: TSSG framing + the network entry.
const index = readJson(join(dir, "index.json"));
const chunkBytes = index.signals?.chunkBytes;
if (!Array.isArray(chunkBytes) || chunkBytes.length < 1) {
  throw new Error("index.json: signals.chunkBytes missing or empty");
}
const netEntry = index.network?.geojson ?? "network.geojson"; // small-network bakes

// 2. The TSSG chunk set → one program table (chunk boundaries from the
// index — chunk lengths are only discoverable by a full parse, §5).
const tssg = readFileSync(join(dir, index.signals?.url ?? "signals.tssg"));
const programs = [];
let tick = 0;
let off = 0;
for (const n of chunkBytes) {
  const table = decodeSignalFrame(tssg.subarray(off, off + n));
  if (off === 0) tick = table.tick;
  programs.push(...table.programs);
  off += n;
}
if (off !== tssg.byteLength) {
  throw new Error(`signals.tssg is ${tssg.byteLength} bytes, chunkBytes sum to ${off}`);
}
const table = { tick, programs };

// 3. The metric network (ADR-0018 manifest → parts, consumed one file at
// a time). Geometry stays in the local metric frame — the clustering
// distances in signals.ts/stopsign.ts are metric.
const netDoc = readJson(join(dir, netEntry));
const features = [];
if (Array.isArray(netDoc.parts)) {
  for (const part of netDoc.parts) {
    for (const f of readJson(join(dir, part)).features ?? []) features.push(f);
  }
} else {
  for (const f of netDoc.features ?? []) features.push(f);
}
const shapeByLane = new Map();
const signLanes = [];
for (const f of features) {
  const id = String(f.id ?? f.properties?.["id"]);
  const shape = f.geometry.coordinates;
  shapeByLane.set(id, shape);
  signLanes.push({
    id,
    row: String(f.properties?.["row"] ?? ""),
    junction: String(f.properties?.["junction"] ?? ""),
    shape,
  });
}

// 4. The derivations, verbatim (DOM-free by design — this is why they are).
const heads = signalHeads(table, shapeByLane);
const signs = stopSigns(signLanes);

// 5. Emit furniture.geojson (metric coords, src/furniture.ts's schema).
const out = [];
for (const h of heads) {
  out.push({
    type: "Feature",
    properties: { kind: "head", id: h.id, program: h.program.id, link: h.linkIdx },
    geometry: { type: "Point", coordinates: [h.x, h.y] },
  });
  if (h.bar !== null) {
    out.push({
      type: "Feature",
      properties: { kind: "bar", id: h.id },
      geometry: {
        type: "LineString",
        coordinates: [
          [h.bar[0], h.bar[1]],
          [h.bar[2], h.bar[3]],
        ],
      },
    });
  }
}
for (const s of signs) {
  out.push({
    type: "Feature",
    properties: { kind: "sign", id: s.id },
    geometry: { type: "Point", coordinates: [s.x, s.y] },
  });
}
const doc = JSON.stringify({ type: "FeatureCollection", features: out });
const outPath = join(dir, "furniture.geojson");
writeFileSync(outPath, doc);

// 6. Point the manifest at it (idempotent).
if (typeof index.furniture !== "string") {
  index.furniture = "furniture.geojson";
  writeFileSync(join(dir, "index.json"), JSON.stringify(index, null, 2) + "\n");
  console.log("bake-furniture: patched index.json (furniture member)");
}

const bars = out.length - heads.length - signs.length;
console.log(
  `bake-furniture: ${heads.length} heads (${programs.length} programs), ${bars} bars, ` +
    `${signs.length} signs → ${outPath} (${doc.length} bytes)`,
);
