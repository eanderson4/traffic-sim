// baked.test.ts — the baked-replay shim (ADR-0023 §6): index parsing, z11
// region selection, the baked-tick schedule, chunk-window fetching against
// a mock fetch, the region completeness barrier (stall → degrade →
// pop-in), the FetchLike control stub's exact /api/replay/* routes, and
// the below-gate clock-only empty frames. Binary chunks are hand-built
// here per the §2/§4 layouts (the Go encoder lands with engine/cmd/bake).

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  BAKED_VEHICLE_GATE_ZOOM,
  bakedFrameCount,
  bakedTickAt,
  chunkForTick,
  classifyOverlay,
  floorToBakedTick,
  frameIndexOfTick,
  latToTileY,
  lonToTileX,
  parseBakedIndex,
  regionKeysForBounds,
  resolveBakedUrl,
  subscribeBaked,
  type BakedFetch,
  type BakedIndex,
  type BakedRegion,
} from "../src/baked.ts";
import { decodeFrame } from "../src/tssf.ts";
import { parseStatus } from "../src/replaypanel.ts";
import { TSRB_HEADER_BYTES, TSRB_MAGIC, TSRB_VEHICLE_BYTES, TSRB_VERSION } from "../src/tsrb.ts";
import { TSRL_HEADER_BYTES, TSRL_MAGIC, TSRL_PAIR_BYTES, TSRL_VERSION } from "../src/tsrl.ts";
import { TSSG_MAGIC, TSSG_VERSION } from "../src/tssg.ts";

const INDEX_URL = "https://data.example.com/baked/test-run/abc123/index.json";
const QUANT = { xyStepM: 0.1, origin: [0, 0] as [number, number] };

// --- fixture builders (independent writers for the spec layouts) ---------

function tsrbChunk(frames: Array<{ tick: number; vehicles: Array<{ id: number; x: number; y: number }> }>): Uint8Array {
  const total = frames.reduce((n, f) => n + TSRB_HEADER_BYTES + f.vehicles.length * TSRB_VEHICLE_BYTES, 0);
  const buf = new ArrayBuffer(total);
  const dv = new DataView(buf);
  let off = 0;
  for (const f of frames) {
    dv.setUint32(off, TSRB_MAGIC, true);
    dv.setUint16(off + 4, TSRB_VERSION, true);
    dv.setBigUint64(off + 8, BigInt(f.tick), true);
    dv.setUint32(off + 16, f.vehicles.length, true);
    let r = off + TSRB_HEADER_BYTES;
    for (const v of f.vehicles) {
      dv.setUint32(r, v.id, true);
      dv.setUint32(r + 4, Math.round(v.x / QUANT.xyStepM), true);
      dv.setUint32(r + 8, Math.round(v.y / QUANT.xyStepM), true);
      dv.setUint8(r + 12, 0);
      dv.setUint8(r + 13, 0);
      r += TSRB_VEHICLE_BYTES;
    }
    off = r;
  }
  return new Uint8Array(buf);
}

function tsrlChunk(frames: Array<{ tick: number; pairs: Array<{ laneIdx: number; ratioQ: number }> }>): Uint8Array {
  const total = frames.reduce((n, f) => n + TSRL_HEADER_BYTES + f.pairs.length * TSRL_PAIR_BYTES, 0);
  const buf = new ArrayBuffer(total);
  const dv = new DataView(buf);
  let off = 0;
  for (const f of frames) {
    dv.setUint32(off, TSRL_MAGIC, true);
    dv.setUint16(off + 4, TSRL_VERSION, true);
    dv.setBigUint64(off + 8, BigInt(f.tick), true);
    dv.setUint32(off + 16, f.pairs.length, true);
    let r = off + TSRL_HEADER_BYTES;
    for (const p of f.pairs) {
      dv.setUint32(r, p.laneIdx, true);
      dv.setUint8(r + 4, p.ratioQ);
      r += TSRL_PAIR_BYTES;
    }
    off = r;
  }
  return new Uint8Array(buf);
}

// tssgTable is a minimal valid TSSG v1 frame (header, zero programs).
function tssgTable(tick: number): Uint8Array {
  const buf = new ArrayBuffer(24);
  const dv = new DataView(buf);
  dv.setUint32(0, TSSG_MAGIC, true);
  dv.setUint16(4, TSSG_VERSION, true);
  dv.setBigUint64(8, BigInt(tick), true);
  dv.setUint32(16, 0, true);
  return new Uint8Array(buf);
}

// --- mock fetch + manual timers --------------------------------------------

type Route = Uint8Array | object | "deferred";

function makeFetch(routes: Record<string, Route>): {
  fetchFn: BakedFetch;
  log: string[];
  resolve: (url: string, bytes: Uint8Array) => void;
} {
  const log: string[] = [];
  const pending = new Map<string, (bytes: Uint8Array) => void>();
  const fetchFn: BakedFetch = (url) => {
    log.push(url);
    const body = routes[url];
    const notFound = {
      ok: false,
      status: 404,
      arrayBuffer: () => Promise.resolve(new ArrayBuffer(0)),
      json: () => Promise.resolve({}),
    };
    if (body === undefined) return Promise.resolve(notFound);
    if (body === "deferred") {
      return new Promise((resolvePromise) => {
        pending.set(url, (bytes) => {
          resolvePromise({
            ok: true,
            status: 200,
            arrayBuffer: () => Promise.resolve(new Uint8Array(bytes).buffer as ArrayBuffer),
            json: () => Promise.resolve({}),
          });
        });
      });
    }
    if (body instanceof Uint8Array) {
      return Promise.resolve({
        ok: true,
        status: 200,
        arrayBuffer: () =>
          Promise.resolve(body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength) as ArrayBuffer),
        json: () => Promise.resolve({}),
      });
    }
    return Promise.resolve({
      ok: true,
      status: 200,
      arrayBuffer: () => Promise.resolve(new ArrayBuffer(0)),
      json: () => Promise.resolve(body),
    });
  };
  return {
    fetchFn,
    log,
    resolve: (url, bytes) => {
      pending.get(url)?.(bytes);
      pending.delete(url);
    },
  };
}

function manualTimers(): {
  timers: Array<{ handle: number; fn: () => void; ms: number }>;
  set: (fn: () => void, ms: number) => number;
  clear: (h: unknown) => void;
  fire: (ms: number) => void;
} {
  const timers: Array<{ handle: number; fn: () => void; ms: number }> = [];
  let next = 1;
  return {
    timers,
    set: (fn, ms) => {
      const handle = next++;
      timers.push({ handle, fn, ms });
      return handle;
    },
    clear: (h) => {
      const i = timers.findIndex((t) => t.handle === h);
      if (i >= 0) timers.splice(i, 1);
    },
    fire: (ms) => {
      for (const t of [...timers]) {
        if (t.ms !== ms) continue;
        timers.splice(timers.indexOf(t), 1);
        t.fn();
      }
    },
  };
}

const flush = async (): Promise<void> => {
  for (let i = 0; i < 50; i++) await Promise.resolve();
};

// --- the test bake -----------------------------------------------------------

// Two adjacent z11 tiles near San Francisco (verified against the slippy
// map formula): the viewport below intersects exactly these two tiles.
const TILE_X = lonToTileX(-122.4, 11);
const TILE_Y = latToTileY(37.8, 11);
const R1_KEY = `z11/${TILE_X}/${TILE_Y}`;
const R2_KEY = `z11/${TILE_X + 1}/${TILE_Y}`;
const tile2lon = (x: number, z: number): number => (x / 2 ** z) * 360 - 180;
const tile2lat = (y: number, z: number): number => (Math.atan(Math.sinh(Math.PI * (1 - (2 * y) / 2 ** z))) * 180) / Math.PI;
const VIEWPORT: [number, number, number, number] = [
  tile2lon(TILE_X, 11) + 0.001,
  tile2lat(TILE_Y + 1, 11) + 0.0001,
  tile2lon(TILE_X + 2, 11) - 0.001,
  tile2lat(TILE_Y, 11) - 0.0001,
];

const R1_FRAMES_URL = "frames/r1/c000.tsrb.br";
const R2_FRAMES_URL = "frames/r2/c000.tsrb.br";
const R1_LANES_URL = "lanes/r1/c000.tsrl.br";
const R2_LANES_URL = "lanes/r2/c000.tsrl.br";

function region(key: string, framesUrl: string, lanesUrl: string): BakedRegion {
  return {
    key,
    bbox: [0, 0, 0, 0],
    frames: [{ tickStart: 0, frameCount: 5, url: framesUrl, bytes: 100 }],
    lanes: [{ tickStart: 0, frameCount: 3, url: lanesUrl, bytes: 50 }],
  };
}

const TICKS = [0, 5, 10, 15, 20]; // dt 0.1 × stride 5 over [0, 20] (on-stride)
const AGG_TICKS = [0, 10, 20]; // laneEveryFrames 2 → every 10 ticks
// TODO(review 2026-07-26): tickEnd 20 lands exactly ON the aggregate grid,
// so the specified off-stride-terminal-carries-no-aggregate behavior has
// no fixture here. Deferred — the Go e2e covers the on-grid path and the
// shim's lookup is exact-tick-equality either way.

function makeIndex(): BakedIndex {
  return {
    version: 1,
    run: "test-run",
    dt: 0.1,
    frame: { projection: "+proj=utm +zone=10", netOffset: [0, 0] },
    bakeEveryTicks: 5,
    laneEveryFrames: 2,
    tickStart: 0,
    tickEnd: 20,
    quant: QUANT,
    network: { pmtiles: "https://data.example.com/city/abc/network.pmtiles", layer: "lanes", promoteId: "id" },
    bounds: [-123, 37, -122, 38],
    signals: { url: "signals.tssg", chunkBytes: [24] },
    laneIds: "lanes.json",
    regions: [region(R1_KEY, R1_FRAMES_URL, R1_LANES_URL), region(R2_KEY, R2_FRAMES_URL, R2_LANES_URL)],
  };
}

// standardRoutes wires the full bake: index, signals, lanes.json, and both
// regions' frame+lane chunks (R2's frames chunk optionally deferred).
function standardRoutes(opts: { deferR2Frames?: boolean } = {}): Record<string, Route> {
  const r1Frames = tsrbChunk(TICKS.map((tick) => ({ tick, vehicles: [{ id: 1, x: 10 + tick, y: 1 }] })));
  const r2Frames = tsrbChunk(TICKS.map((tick) => ({ tick, vehicles: [{ id: 2, x: 20 + tick, y: 2 }] })));
  return {
    [INDEX_URL]: makeIndex() as unknown as object,
    [resolveBakedUrl(INDEX_URL, "signals.tssg")]: tssgTable(0),
    [resolveBakedUrl(INDEX_URL, "lanes.json")]: ["lane-a", "lane-b"],
    [resolveBakedUrl(INDEX_URL, R1_FRAMES_URL)]: r1Frames,
    [resolveBakedUrl(INDEX_URL, R2_FRAMES_URL)]: opts.deferR2Frames === true ? "deferred" : r2Frames,
    [resolveBakedUrl(INDEX_URL, R1_LANES_URL)]: tsrlChunk(AGG_TICKS.map((tick) => ({ tick, pairs: [{ laneIdx: 0, ratioQ: 170 }] }))),
    [resolveBakedUrl(INDEX_URL, R2_LANES_URL)]: tsrlChunk(AGG_TICKS.map((tick) => ({ tick, pairs: [{ laneIdx: 1, ratioQ: 85 }] }))),
  };
}

interface Harness {
  sub: Awaited<ReturnType<typeof subscribeBaked>>;
  frames: ReturnType<typeof decodeFrame>[];
  sigHeaders: Array<string | null>;
  statuses: Array<[boolean, string]>;
  log: string[];
  resolve: (url: string, bytes: Uint8Array) => void;
  timers: ReturnType<typeof manualTimers>;
}

async function harness(routes: Record<string, Route>, stallMs = 5000): Promise<Harness> {
  const { fetchFn, log, resolve } = makeFetch(routes);
  const timers = manualTimers();
  const frames: Harness["frames"] = [];
  const sigHeaders: Harness["sigHeaders"] = [];
  const statuses: Harness["statuses"] = [];
  const sub = await subscribeBaked(
    INDEX_URL,
    (data) => frames.push(decodeFrame(data)), // through the UNMODIFIED tssf decoder
    (_data, headers) => sigHeaders.push(headers?.get("sig_chunk") ?? null),
    (connected, detail) => statuses.push([connected, detail]),
    { fetchFn, stallMs, setTimeoutFn: timers.set, clearTimeoutFn: timers.clear },
  );
  return { sub, frames, sigHeaders, statuses, log, resolve, timers };
}

// --- index parsing -----------------------------------------------------------

test("parseBakedIndex validates the manifest and rejects malformed ones", () => {
  const idx = parseBakedIndex(JSON.parse(JSON.stringify(makeIndex())));
  assert.equal(idx.run, "test-run");
  assert.equal(idx.dt, 0.1);
  assert.equal(idx.network.layer, "lanes");
  assert.equal(idx.regions.length, 2);
  assert.throws(() => parseBakedIndex({ ...JSON.parse(JSON.stringify(makeIndex())), version: 2 }), /version 2/);
  const noNet = JSON.parse(JSON.stringify(makeIndex())) as Record<string, unknown>;
  noNet["network"] = { layer: "lanes", promoteId: "id" };
  assert.throws(() => parseBakedIndex(noNet), /neither pmtiles nor geojson/);
  assert.throws(() => parseBakedIndex(null), /not an object/);
});

test("resolveBakedUrl resolves prefix-relative URLs, passes absolute through", () => {
  assert.equal(
    resolveBakedUrl(INDEX_URL, "frames/z11-1-2/c000.tsrb.br"),
    "https://data.example.com/baked/test-run/abc123/frames/z11-1-2/c000.tsrb.br",
  );
  assert.equal(resolveBakedUrl(INDEX_URL, "https://cdn.example.com/x.pmtiles"), "https://cdn.example.com/x.pmtiles");
});

test("classifyOverlay maps the demo overlay set by basename", () => {
  assert.equal(classifyOverlay("overlays/water.geojson"), "water");
  assert.equal(classifyOverlay("overlays/boundaries.geojson"), "boundaries");
  assert.equal(classifyOverlay("overlays/zones.geojson"), "zones");
  assert.equal(classifyOverlay("overlays/buildings.geojson"), "buildings");
  assert.equal(classifyOverlay("frames/c000.tsrb.br"), null);
});

// --- region selection math ----------------------------------------------------

test("z11 tile math matches the slippy map reference", () => {
  assert.equal(lonToTileX(-122.4194, 11), 327); // San Francisco
  assert.equal(latToTileY(37.7749, 11), 791);
  assert.equal(lonToTileX(0, 11), 1024);
  assert.equal(latToTileY(0, 11), 1024);
  assert.equal(lonToTileX(-180, 11), 0);
});

test("regionKeysForBounds intersects exactly the viewport's tiles, inflated by ring", () => {
  const plain = regionKeysForBounds(VIEWPORT);
  assert.deepEqual([...plain].sort(), [R1_KEY, R2_KEY].sort());
  const ring = regionKeysForBounds(VIEWPORT, 1);
  assert.equal(ring.size, 12); // 4 × 3 around the 2 × 1 viewport
  assert.ok(ring.has(R1_KEY) && ring.has(R2_KEY));
  assert.ok(ring.has(`z11/${TILE_X - 1}/${TILE_Y - 1}`));
  assert.ok(ring.has(`z11/${TILE_X + 2}/${TILE_Y + 1}`));
});

test("regionKeysForBounds clamps at the world edge", () => {
  const keys = regionKeysForBounds([-180, 85, -179, 85.1]);
  for (const k of keys) {
    const [, x, y] = k.split("/").map(Number);
    assert.ok(x! >= 0 && y! >= 0 && x! < 2048 && y! < 2048);
  }
});

// --- baked-tick schedule -------------------------------------------------------

test("schedule math: on-stride and off-stride (terminal frame) horizons", () => {
  const onStride = { tickStart: 0, tickEnd: 9000, bakeEveryTicks: 5 };
  assert.equal(bakedFrameCount(onStride), 1801);
  assert.equal(bakedTickAt(onStride, 1800), 9000);
  const offStride = { tickStart: 0, tickEnd: 9003, bakeEveryTicks: 5 };
  assert.equal(bakedFrameCount(offStride), 1802); // terminal frame at tickEnd
  assert.equal(bakedTickAt(offStride, 1801), 9003);
  assert.equal(frameIndexOfTick(offStride, 9003), 1801);
  assert.equal(frameIndexOfTick(onStride, 9000), 1800);
  assert.equal(floorToBakedTick(onStride, 123), 120);
  assert.equal(floorToBakedTick(onStride, -5), 0);
  assert.equal(floorToBakedTick(offStride, 99999), 9003); // clamped to tickEnd
});

test("chunkForTick picks the covering window, including the terminal frame's chunk", () => {
  const chunks = [
    { tickStart: 0, frameCount: 2, url: "c000", bytes: 1 },
    { tickStart: 10, frameCount: 2, url: "c001", bytes: 1 },
    { tickStart: 20, frameCount: 1, url: "c002", bytes: 1 }, // terminal frame at tickEnd 20
  ];
  assert.equal(chunkForTick(chunks, 0)?.url, "c000");
  assert.equal(chunkForTick(chunks, 7)?.url, "c000");
  assert.equal(chunkForTick(chunks, 10)?.url, "c001");
  assert.equal(chunkForTick(chunks, 20)?.url, "c002");
  assert.equal(chunkForTick(chunks, -1), null);
});

// --- the shim over the test bake -------------------------------------------------

test("below the z13 gate: clock-only empty frames, no vehicle fetches, TSRL scheduled", async () => {
  const h = await harness(standardRoutes());
  assert.deepEqual(h.sigHeaders, ["1/1"]); // synthetic sig_chunk header
  assert.equal(h.statuses.length, 1);
  assert.equal(h.statuses[0]![0], true);
  // The first frame ships with subscribe (start awaits it): empty, tick 0.
  assert.equal(h.frames.length, 1);
  assert.equal(h.frames[0]!.tick, 0);
  assert.equal(h.frames[0]!.vehicles.length, 0);
  // The clock advances at the baked cadence (500 ms at 2 Hz, 1×).
  h.timers.fire(500);
  await flush();
  assert.equal(h.frames.length, 2);
  assert.equal(h.frames[1]!.tick, 5);
  assert.equal(h.frames[1]!.vehicles.length, 0);
  // No region VEHICLE chunk was fetched (default viewport is below the
  // gate); TSRL for ALL regions was (lane coloring always reads TSRL).
  assert.ok(!h.log.some((u) => u.includes("/frames/")), `no vehicle fetches: ${h.log}`);
  assert.ok(h.log.some((u) => u.includes(R1_LANES_URL)));
  assert.ok(h.log.some((u) => u.includes(R2_LANES_URL)));
  // The TSRL lookup merges both regions' aggregate frames in force.
  const ratios = h.sub.laneRatiosAt(7); // latest aggregate tick ≤ 7 is 0
  assert.ok(ratios);
  assert.equal(ratios.get("lane-a"), 1);
  assert.equal(ratios.get("lane-b"), 0.5);
  // Hold-last (§4): the aggregate holds until the next one lands, and the
  // final aggregate holds to tickEnd.
  // TODO(review 2026-07-26): the fixture emits identical ratioQ at every
  // aggregate tick, so the hold-last assert below cannot distinguish
  // "held tick 10" from "snapped to tick 0" or "read tick 20 early".
  // Vary ratioQ per aggregate tick to make this a real oracle. Deferred.
  assert.equal(h.sub.laneRatiosAt(19)!.get("lane-a"), 1); // held from tick 10's aggregate
  assert.ok(h.sub.laneRatiosAt(20) !== null); // final aggregate at tickEnd itself
});

test("at/above the gate: viewport region frames merge into one synthetic TSSF", async () => {
  const h = await harness(standardRoutes());
  assert.deepEqual([...regionKeysForBounds(VIEWPORT)].sort(), [R1_KEY, R2_KEY].sort());
  h.sub.setViewport(VIEWPORT, BAKED_VEHICLE_GATE_ZOOM);
  h.timers.fire(500);
  await flush();
  const f = h.frames[h.frames.length - 1]!;
  assert.equal(f.tick, 5);
  assert.equal(f.vehicles.length, 2); // the union of both region frames
  const ids = f.vehicles.map((v) => v.id).sort();
  assert.deepEqual(ids, [1, 2]);
  assert.ok(Math.abs(f.vehicles.find((v) => v.id === 1)!.x - 15) < 1e-4);
});

test("completeness barrier: stall ≤ bound, degrade without the laggard, pop in when it lands", async () => {
  const STALL = 5000;
  const h = await harness(standardRoutes({ deferR2Frames: true }), STALL);
  const r2url = resolveBakedUrl(INDEX_URL, R2_FRAMES_URL);
  h.sub.setViewport(VIEWPORT, BAKED_VEHICLE_GATE_ZOOM);
  h.timers.fire(500); // step for tick 5: R1 lands, R2 in flight → barrier waits
  await flush();
  assert.equal(h.frames.length, 1, "stalled: no emission while a viewport region is in flight");
  h.timers.fire(STALL); // the bounded stall expires → degrade
  await flush();
  assert.equal(h.frames.length, 2);
  assert.equal(h.frames[1]!.tick, 5);
  assert.deepEqual(h.frames[1]!.vehicles.map((v) => v.id), [1], "emitted WITHOUT the laggard");
  // Degraded STAYS degraded — the next tick emits without re-stalling
  // (no second stall-timer wait needed for the same laggard).
  h.timers.fire(500);
  await flush();
  assert.equal(h.frames.length, 3, "no 2 s re-stall per tick for the same laggard");
  assert.deepEqual(h.frames[2]!.vehicles.map((v) => v.id), [1]);
  // The laggard's chunk lands → its vehicles pop in at the NEXT tick.
  h.resolve(r2url, tsrbChunk(TICKS.map((tick) => ({ tick, vehicles: [{ id: 2, x: 20 + tick, y: 2 }] }))));
  await flush();
  h.timers.fire(500);
  await flush();
  assert.equal(h.frames.length, 4);
  assert.deepEqual(h.frames[3]!.vehicles.map((v) => v.id).sort(), [1, 2]);
});

test("chunk windows: the next (region, time-chunk) object is fetched only when the clock reaches it", async () => {
  const idx = makeIndex();
  idx.regions = [
    {
      key: R1_KEY,
      bbox: [0, 0, 0, 0],
      frames: [
        { tickStart: 0, frameCount: 2, url: "frames/r1/c000.tsrb.br", bytes: 1 },
        { tickStart: 10, frameCount: 3, url: "frames/r1/c001.tsrb.br", bytes: 1 },
      ],
      lanes: [{ tickStart: 0, frameCount: 3, url: R1_LANES_URL, bytes: 1 }],
    },
  ];
  const routes = standardRoutes();
  routes[INDEX_URL] = idx as unknown as object;
  delete routes[resolveBakedUrl(INDEX_URL, R2_FRAMES_URL)];
  delete routes[resolveBakedUrl(INDEX_URL, R2_LANES_URL)];
  const c000 = resolveBakedUrl(INDEX_URL, "frames/r1/c000.tsrb.br");
  const c001 = resolveBakedUrl(INDEX_URL, "frames/r1/c001.tsrb.br");
  routes[c000] = tsrbChunk([0, 5].map((tick) => ({ tick, vehicles: [{ id: 1, x: 1, y: 1 }] })));
  routes[c001] = tsrbChunk([10, 15, 20].map((tick) => ({ tick, vehicles: [{ id: 1, x: 2, y: 2 }] })));
  const h = await harness(routes);
  h.sub.setViewport(VIEWPORT, BAKED_VEHICLE_GATE_ZOOM);
  await flush();
  h.timers.fire(500); // tick 5
  await flush();
  assert.ok(h.log.includes(c000));
  assert.ok(!h.log.includes(c001), "the next window is not fetched early");
  h.timers.fire(500); // tick 10 crosses into c001
  await flush();
  assert.ok(h.log.includes(c001));
  assert.equal(h.frames[h.frames.length - 1]!.tick, 10);
});

test("FetchLike stub answers the panel's exact routes with ReplayStatus JSON", async () => {
  const h = await harness(standardRoutes());
  const get = async (url: string) => h.sub.fetchFn(url, {});
  const post = async (url: string, body?: unknown) =>
    h.sub.fetchFn(url, body === undefined ? { method: "POST" } : { method: "POST", body: JSON.stringify(body) });

  // Status: parses through the PANEL's own validator; echoes the index's
  // run id and ignores the ?run= binding (never a 409 mismatch).
  const s0 = parseStatus(await (await get("/api/replay/status?run=anything")).json());
  assert.ok(s0);
  assert.equal(s0.run, "test-run");
  assert.equal(s0.replayRun, "test-run");
  assert.equal(s0.tick, 0);
  assert.equal(s0.endTick, 20);
  assert.equal(s0.dt, 0.1);
  assert.equal(s0.speed, 1);
  assert.equal(s0.paused, false);
  assert.equal(s0.done, false);
  assert.equal(s0.crcErrors, 0);
  assert.equal(s0.verbErrors, 0);

  // Pause / resume.
  const s1 = parseStatus(await (await post("/api/replay/ctl/pause?run=x")).json());
  assert.equal(s1!.paused, true);
  const s2 = parseStatus(await (await post("/api/replay/ctl/resume?run=x")).json());
  assert.equal(s2!.paused, false);

  // Speed.
  const s3 = parseStatus(await (await post("/api/replay/ctl/speed?run=x", { speed: 4 })).json());
  assert.equal(s3!.speed, 4);

  // Seek floors to the baked stride and lands the frame synchronously.
  const before = h.frames.length;
  const s4 = parseStatus(await (await post("/api/replay/ctl/seek?run=x", { tick: 13 })).json());
  assert.equal(s4!.tick, 10);
  assert.equal(h.frames.length, before + 1, "the landing frame ships with the 200");
  assert.equal(h.frames[h.frames.length - 1]!.tick, 10);

  // End semantics: seek to tickEnd, run the last arm out → done; resume 409s.
  await post("/api/replay/ctl/seek?run=x", { tick: 20 });
  const done = parseStatus(await (await get("/api/replay/status?run=x")).json());
  assert.equal(done!.tick, 20);
  assert.equal(done!.done, true, "landing on tickEnd reports done");
  const r409 = await post("/api/replay/ctl/resume?run=x");
  assert.equal(r409.status, 409);
  // …until the user seeks back.
  const s5 = parseStatus(await (await post("/api/replay/ctl/seek?run=x", { tick: 0 })).json());
  assert.equal(s5!.done, false);

  // Unknown routes 404.
  const r404 = await get("/api/replay/nope?run=x");
  assert.equal(r404.status, 404);
});

test("a chunked TSSG set feeds the accumulator with i/n sig_chunk headers", async () => {
  const idx = makeIndex();
  idx.signals = { url: "signals.tssg", chunkBytes: [24, 24] };
  const routes = standardRoutes();
  routes[INDEX_URL] = idx as unknown as object;
  const two = new Uint8Array(48);
  two.set(tssgTable(0), 0);
  two.set(tssgTable(0), 24);
  routes[resolveBakedUrl(INDEX_URL, "signals.tssg")] = two;
  const h = await harness(routes);
  assert.deepEqual(h.sigHeaders, ["1/2", "2/2"]);
});
