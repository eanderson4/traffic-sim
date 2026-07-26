// baked.ts — the baked-replay transport shim (ADR-0023 §6): a drop-in
// replacement for subscribeSnapshots that replays a recording entirely
// from static objects (index.json + TSRB/TSRL chunks + TSSG + furniture,
// §1's layout) behind `?bake=<index URL>` — no server at runtime.
//
//   - index.json (§5) is the manifest: frame descriptor, bounds, chunk
//     tables, TSSG framing, quant origin. Chunk URLs are prefix-relative
//     (network.pmtiles is absolute, §8) and index.json itself is fetched
//     no-cache (netload.ts's manifest rule).
//   - Vehicle frames arrive as TSRB v1 region chunks (§2/§3), merged over
//     the viewport's z11 region set and RE-ENCODED into synthetic TSSF v1
//     bytes (tsrb.ts encodeTssf) — tssf.ts, SnapshotBuffer, the artic
//     channel, and the render loop run untouched.
//   - The TSSG chunk set (§5 signals.chunkBytes is the framing) feeds the
//     unmodified accumulator with synthetic sig_chunk headers; nc stays
//     null so the ADR-0016 request/reply pull no-ops by its existing
//     guard (the baked table is delivered complete at attach).
//   - The control plane (play/pause/speed/seek over a frame scheduler)
//     is exposed as a FetchLike stub answering ReplayPanel's exact
//     /api/replay/* routes — the panel code is unchanged.
//   - The clock never depends on vehicle fetches: below the z13 gate no
//     region stream is scheduled and EMPTY synthetic frames (24 B) advance
//     the sample tick (which drives the TSRL applier, signal derivation,
//     and the HUD). Above the gate the region completeness barrier (§3)
//     stalls the clock ≤ stallMs while a viewport region's chunk is in
//     flight, then degrades — emitting the arrived union — until the
//     laggard lands. Empty frames are emitted ONLY below the gate: above
//     it an empty frame would count as tick T delivered and permanently
//     foreclose the real frame T (SnapshotBuffer.push drops duplicates).

import { headers as natsHeaders, type MsgHdrs } from "nats.ws";

import type { FetchLike, ReplayStatus } from "./replaypanel.ts";
import type { LocalFrame } from "./proj.ts";
import { decodeTsrbChunk, encodeTssf, type BakedFrame, type BakedQuant } from "./tsrb.ts";
import { decodeTsrlChunk, type TsrlFrame } from "./tsrl.ts";
import type { VehicleRecord } from "./tssf.ts";

// BAKED_VEHICLE_GATE_ZOOM is the baked-mode vehicle minzoom (ADR-0023
// §6.4): the zoom at which the last road class (residential) appears, so
// dots never float over invisible streets. Below it the shim schedules no
// frame fetches and emits empty frames; the lane layers and the TSRL
// congestion channel are unaffected (§4: lane coloring ALWAYS reads TSRL).
export const BAKED_VEHICLE_GATE_ZOOM = 13;

// BAKED_REGION_ZOOM is the region-key tile zoom (§3: z11 web-mercator).
export const BAKED_REGION_ZOOM = 11;

// DEFAULT_STALL_MS is the completeness barrier's bounded stall (§3: 2 s).
export const DEFAULT_STALL_MS = 2000;

export interface BakedChunk {
  tickStart: number;
  frameCount: number;
  url: string; // prefix-relative (resolved against the index URL)
  bytes: number;
}

export interface BakedRegion {
  key: string; // "z11/{x}/{y}"
  bbox: [number, number, number, number];
  frames: BakedChunk[]; // contiguous over [tickStart, tickEnd] (§3)
  lanes: BakedChunk[];
}

export interface BakedNetwork {
  pmtiles?: string; // absolute URL (§8: content-keyed per city, shared)
  geojson?: string; // small networks may keep plain GeoJSON (§7)
  layer: string; // PMTiles source-layer ("lanes")
  promoteId: string; // "id"
}

export interface BakedIndex {
  version: number;
  run: string;
  scenarioHash?: string;
  dt: number; // recorded run's authoritative timestep
  frame: LocalFrame;
  bakeEveryTicks: number; // bake stride (5 → 2 Hz at dt 0.1)
  laneEveryFrames: number; // aggregate cadence in baked frames (10 → 0.2 Hz)
  tickStart: number;
  tickEnd: number; // INCLUSIVE — the last baked tick (§5)
  quant: BakedQuant;
  network: BakedNetwork;
  bounds: [number, number, number, number]; // [west, south, east, north]
  furniture?: string;
  overlays?: string[];
  signals: { url: string; chunkBytes: number[] };
  laneIds: string; // lanes.json — the deduped occupied-lane-id table (§4)
  regions: BakedRegion[];
}

function num(v: unknown, what: string): number {
  if (typeof v !== "number" || !Number.isFinite(v)) throw new Error(`baked index: ${what} must be a number`);
  return v;
}

function str(v: unknown, what: string): string {
  if (typeof v !== "string" || v === "") throw new Error(`baked index: ${what} must be a string`);
  return v;
}

function bbox(v: unknown, what: string): [number, number, number, number] {
  if (!Array.isArray(v) || v.length !== 4) throw new Error(`baked index: ${what} must be [w, s, e, n]`);
  return [num(v[0], what), num(v[1], what), num(v[2], what), num(v[3], what)];
}

function chunks(v: unknown, what: string): BakedChunk[] {
  if (!Array.isArray(v)) throw new Error(`baked index: ${what} must be an array`);
  return v.map((c, i) => {
    const o = c as Record<string, unknown>;
    return {
      tickStart: num(o?.["tickStart"], `${what}[${i}].tickStart`),
      frameCount: num(o?.["frameCount"], `${what}[${i}].frameCount`),
      url: str(o?.["url"], `${what}[${i}].url`),
      bytes: num(o?.["bytes"], `${what}[${i}].bytes`),
    };
  });
}

// parseBakedIndex validates the manifest defensively (§5): a malformed
// bake fails loud at attach, never mid-playback.
export function parseBakedIndex(raw: unknown): BakedIndex {
  if (typeof raw !== "object" || raw === null) throw new Error("baked index: not an object");
  const o = raw as Record<string, unknown>;
  const version = num(o["version"], "version");
  if (version !== 1) throw new Error(`baked index: unsupported version ${version}`);
  const frame = o["frame"] as Record<string, unknown>;
  if (typeof frame !== "object" || frame === null) throw new Error("baked index: missing frame");
  const netOffset = frame["netOffset"];
  if (!Array.isArray(netOffset) || netOffset.length !== 2) {
    throw new Error("baked index: frame.netOffset must be [x, y]");
  }
  const quant = o["quant"] as Record<string, unknown>;
  if (typeof quant !== "object" || quant === null) throw new Error("baked index: missing quant");
  const origin = quant["origin"];
  if (!Array.isArray(origin) || origin.length !== 2) throw new Error("baked index: quant.origin must be [x, y]");
  const network = o["network"] as Record<string, unknown>;
  if (typeof network !== "object" || network === null) throw new Error("baked index: missing network");
  const pmtiles = network["pmtiles"];
  const geojson = network["geojson"];
  if (typeof pmtiles !== "string" && typeof geojson !== "string") {
    throw new Error("baked index: network carries neither pmtiles nor geojson");
  }
  const signals = o["signals"] as Record<string, unknown>;
  if (typeof signals !== "object" || signals === null) throw new Error("baked index: missing signals");
  const chunkBytes = signals["chunkBytes"];
  if (!Array.isArray(chunkBytes) || chunkBytes.length < 1) {
    throw new Error("baked index: signals.chunkBytes must be a non-empty array");
  }
  const regions = o["regions"];
  if (!Array.isArray(regions)) throw new Error("baked index: regions must be an array");
  return {
    version,
    run: str(o["run"], "run"),
    ...(typeof o["scenarioHash"] === "string" ? { scenarioHash: o["scenarioHash"] } : {}),
    dt: num(o["dt"], "dt"),
    frame: {
      projection: str(frame["projection"], "frame.projection"),
      netOffset: [num(netOffset[0], "frame.netOffset"), num(netOffset[1], "frame.netOffset")],
    },
    bakeEveryTicks: num(o["bakeEveryTicks"], "bakeEveryTicks"),
    laneEveryFrames: num(o["laneEveryFrames"], "laneEveryFrames"),
    tickStart: num(o["tickStart"], "tickStart"),
    tickEnd: num(o["tickEnd"], "tickEnd"),
    quant: {
      xyStepM: num(quant["xyStepM"], "quant.xyStepM"),
      origin: [num(origin[0], "quant.origin"), num(origin[1], "quant.origin")],
    },
    network: {
      ...(typeof pmtiles === "string" ? { pmtiles } : {}),
      ...(typeof geojson === "string" ? { geojson } : {}),
      // layer is the PMTiles source-layer; GeoJSON-mode bakes omit it
      // (§7's single rendering contract names it "lanes" either way).
      layer: typeof network["layer"] === "string" ? network["layer"] : "lanes",
      promoteId: str(network["promoteId"], "network.promoteId"),
    },
    bounds: bbox(o["bounds"], "bounds"),
    ...(typeof o["furniture"] === "string" ? { furniture: o["furniture"] } : {}),
    ...(Array.isArray(o["overlays"]) ? { overlays: (o["overlays"] as unknown[]).map((u, i) => str(u, `overlays[${i}]`)) } : {}),
    signals: {
      url: str(signals["url"], "signals.url"),
      chunkBytes: chunkBytes.map((b, i) => num(b, `signals.chunkBytes[${i}]`)),
    },
    laneIds: str(o["laneIds"], "laneIds"),
    regions: regions.map((r, i) => {
      const ro = r as Record<string, unknown>;
      return {
        key: str(ro?.["key"], `regions[${i}].key`),
        bbox: bbox(ro?.["bbox"], `regions[${i}].bbox`),
        frames: chunks(ro?.["frames"], `regions[${i}].frames`),
        lanes: chunks(ro?.["lanes"], `regions[${i}].lanes`),
      };
    }),
  };
}

// BakedFetch is the fetch slice the shim needs (chunk/index fetches), so
// tests inject a stub and the shim defaults to the real thing.
export interface BakedFetchResponse {
  ok: boolean;
  status: number;
  arrayBuffer(): Promise<ArrayBuffer>;
  json(): Promise<unknown>;
}
export type BakedFetch = (url: string, init?: { cache?: "no-cache" }) => Promise<BakedFetchResponse>;

const defaultFetch: BakedFetch = (url, init) => fetch(url, init);

// resolveBakedUrl resolves a prefix-relative manifest URL against the
// index URL; absolute URLs (network.pmtiles, §8) pass through.
export function resolveBakedUrl(indexUrl: string, rel: string): string {
  return new URL(rel, indexUrl).href;
}

const indexCache = new Map<string, Promise<BakedIndex>>();

// loadBakedIndex fetches and parses index.json (no-cache, §8's manifest
// rule). Results cache per URL so main.ts's startup load and
// subscribeBaked share one fetch. Injected fetches bypass the cache
// (tests want per-test bytes).
export function loadBakedIndex(indexUrl: string, fetchFn?: BakedFetch): Promise<BakedIndex> {
  if (fetchFn !== undefined) return fetchBakedIndex(indexUrl, fetchFn);
  const cached = indexCache.get(indexUrl);
  if (cached !== undefined) return cached;
  const p = fetchBakedIndex(indexUrl, defaultFetch);
  indexCache.set(indexUrl, p);
  return p;
}

async function fetchBakedIndex(indexUrl: string, fetchFn: BakedFetch): Promise<BakedIndex> {
  const res = await fetchFn(indexUrl, { cache: "no-cache" });
  if (!res.ok) throw new Error(`baked: fetch ${indexUrl}: ${res.status}`);
  return parseBakedIndex(await res.json());
}

// classifyOverlay maps a baked overlay URL back to the viz's four overlay
// channels by basename (the bake copies the demo's /overlay/* set under
// its own names, §1). Unknown names are not overlays the viz renders.
export function classifyOverlay(url: string): "water" | "boundaries" | "zones" | "buildings" | null {
  const base = url.split("/").pop() ?? "";
  if (base.includes("water")) return "water";
  if (base.includes("boundar")) return "boundaries";
  if (base.includes("zone")) return "zones";
  if (base.includes("building")) return "buildings";
  return null;
}

// --- z11 region selection (§3) -------------------------------------------

const MAX_MERCATOR_LAT = 85.05112878;

export function lonToTileX(lon: number, z: number): number {
  return Math.floor(((lon + 180) / 360) * 2 ** z);
}

export function latToTileY(lat: number, z: number): number {
  const clamped = Math.max(-MAX_MERCATOR_LAT, Math.min(MAX_MERCATOR_LAT, lat));
  const r = (clamped * Math.PI) / 180;
  return Math.floor(((1 - Math.log(Math.tan(r) + 1 / Math.cos(r)) / Math.PI) / 2) * 2 ** z);
}

// regionKeysForBounds returns the z11 region keys ("z11/{x}/{y}")
// intersecting [west, south, east, north], inflated by `ring` tiles on
// every side (§4: TSRL is fetched for the viewport's region set inflated
// by one tile ring when zoomed in — a lane's home tile can sit just
// outside the viewport while the lane itself is visible).
export function regionKeysForBounds(
  bounds: readonly [number, number, number, number],
  ring = 0,
): Set<string> {
  const z = BAKED_REGION_ZOOM;
  const max = 2 ** z - 1;
  const [w, s, e, n] = bounds;
  const x0 = Math.max(0, lonToTileX(Math.min(w, e), z) - ring);
  const x1 = Math.min(max, lonToTileX(Math.max(w, e), z) + ring);
  const y0 = Math.max(0, latToTileY(Math.max(s, n), z) - ring); // north = smaller y
  const y1 = Math.min(max, latToTileY(Math.min(s, n), z) + ring);
  const keys = new Set<string>();
  for (let x = x0; x <= x1; x++) {
    for (let y = y0; y <= y1; y++) keys.add(`z${z}/${x}/${y}`);
  }
  return keys;
}

// --- baked-tick schedule (§2/§5) ------------------------------------------

// bakedFrameCount: frames bake at tickStart + k×stride, plus a TERMINAL
// frame at tickEnd when tickEnd is off-stride (§5, Player parity).
export function bakedFrameCount(index: Pick<BakedIndex, "tickStart" | "tickEnd" | "bakeEveryTicks">): number {
  const span = index.tickEnd - index.tickStart;
  const onStride = Math.floor(span / index.bakeEveryTicks) + 1;
  return span % index.bakeEveryTicks === 0 ? onStride : onStride + 1;
}

// bakedTickAt returns the k-th baked frame's tick (the terminal frame
// lands exactly on tickEnd past the decimation stride).
export function bakedTickAt(
  index: Pick<BakedIndex, "tickStart" | "tickEnd" | "bakeEveryTicks">,
  k: number,
): number {
  const t = index.tickStart + k * index.bakeEveryTicks;
  return t > index.tickEnd ? index.tickEnd : t;
}

// floorToBakedTick: a seek lands on the greatest baked tick ≤ the target
// (§2), clamped into [tickStart, tickEnd].
export function floorToBakedTick(
  index: Pick<BakedIndex, "tickStart" | "tickEnd" | "bakeEveryTicks">,
  tick: number,
): number {
  if (tick <= index.tickStart) return index.tickStart;
  if (tick >= index.tickEnd) return index.tickEnd;
  return index.tickStart + Math.floor((tick - index.tickStart) / index.bakeEveryTicks) * index.bakeEveryTicks;
}

// frameIndexOfTick is bakedTickAt's inverse for baked ticks.
export function frameIndexOfTick(
  index: Pick<BakedIndex, "tickStart" | "tickEnd" | "bakeEveryTicks">,
  tick: number,
): number {
  const count = bakedFrameCount(index);
  if (tick === index.tickEnd && (index.tickEnd - index.tickStart) % index.bakeEveryTicks !== 0) {
    return count - 1; // the off-stride terminal frame
  }
  return Math.floor((tick - index.tickStart) / index.bakeEveryTicks);
}

// chunkForTick selects the chunk covering a tick: the last chunk whose
// tickStart ≤ tick. Chunk lists are contiguous over [tickStart, tickEnd]
// (§3 — bake writes even all-empty windows), so this covers the off-
// stride terminal frame (carried by the final chunk, §5) without any
// coverage arithmetic. Null when no chunk starts at/before the tick (a
// manifest gap — loud at bake time, tolerated here as "region empty").
export function chunkForTick(chunks: readonly BakedChunk[], tick: number): BakedChunk | null {
  let best: BakedChunk | null = null;
  for (const c of chunks) {
    if (c.tickStart <= tick && (best === null || c.tickStart > best.tickStart)) best = c;
  }
  return best;
}

// ChunkEntry is a cached chunk fetch: frames populates on success, failed
// on fetch/decode error (the barrier treats both as "not landed" — the
// entry is then evicted so the next tick retries).
interface ChunkEntry<F> {
  promise: Promise<F[] | null>;
  frames: F[] | null;
  failed: boolean;
}

export interface BakedSessionOpts {
  fetchFn?: BakedFetch;
  nowMs?: () => number;
  stallMs?: number; // completeness-barrier bound (tests shrink it)
  setTimeoutFn?: (fn: () => void, ms: number) => unknown; // timer seam for tests
  clearTimeoutFn?: (handle: unknown) => void;
}

export interface BakedSubscription {
  nc: null; // ADR-0016 pull paths no-op on their existing null guards
  close: () => Promise<void>;
  // fetchFn is the control-plane stub injected into ReplayPanel in place
  // of real fetch (§6: the panel code is unchanged).
  fetchFn: FetchLike;
  // setViewport reports the map's bounds + zoom (pan/zoom drive region
  // subscription and the z13 vehicle gate). main.ts debounces pans.
  setViewport: (bounds: readonly [number, number, number, number], zoom: number) => void;
  // laneRatiosAt merges the TSRL aggregate frames in force at tick over
  // the relevant region set (ALL regions zoomed out, viewport + one tile
  // ring zoomed in, §4) — null until laneIds/chunks have landed.
  laneRatiosAt: (tick: number) => Map<string, number> | null;
}

// BakedSession is the shim's state machine: frame scheduler, chunk caches,
// the completeness barrier, and the control-plane stub.
export class BakedSession implements BakedSubscription {
  readonly nc = null;
  private readonly indexUrl: string;
  private readonly index: BakedIndex;
  private readonly onFrame: (data: Uint8Array) => void;
  private readonly onSignals: (data: Uint8Array, headers: MsgHdrs | undefined) => void;
  private readonly onStatus: (connected: boolean, detail: string) => void;
  private readonly rawFetch: BakedFetch;
  private readonly nowMs: () => number;
  private readonly stallMs: number;
  private readonly setTimeoutFn: (fn: () => void, ms: number) => unknown;
  private readonly clearTimeoutFn: (handle: unknown) => void;

  private closed = false;
  private k = 0; // next frame index to emit
  private currentTick: number;
  private speed = 1;
  private paused = false;
  private done = false;
  private timer: unknown = null;
  private gen = 0; // scheduling generation — ctl actions invalidate in-flight steps

  private gated = true; // below the z13 vehicle gate until the first setViewport
  private regionKeys = new Set<string>();
  private laneRegionKeys = new Set<string>();
  private readonly laggards = new Set<string>(); // regions the barrier won't wait for (§3)
  private readonly frameCache = new Map<string, ChunkEntry<BakedFrame>>();
  private readonly laneCache = new Map<string, ChunkEntry<TsrlFrame>>();
  private laneIds: string[] | null = null;
  private laneIdsInFlight = false;

  constructor(
    indexUrl: string,
    index: BakedIndex,
    onFrame: (data: Uint8Array) => void,
    onSignals: (data: Uint8Array, headers: MsgHdrs | undefined) => void,
    onStatus: (connected: boolean, detail: string) => void,
    opts: BakedSessionOpts = {},
  ) {
    this.indexUrl = indexUrl;
    this.index = index;
    this.onFrame = onFrame;
    this.onSignals = onSignals;
    this.onStatus = onStatus;
    this.rawFetch = opts.fetchFn ?? defaultFetch;
    this.nowMs = opts.nowMs ?? (() => performance.now());
    this.stallMs = opts.stallMs ?? DEFAULT_STALL_MS;
    this.setTimeoutFn = opts.setTimeoutFn ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimeoutFn = opts.clearTimeoutFn ?? ((h) => clearTimeout(h as Parameters<typeof clearTimeout>[0]));
    this.currentTick = index.tickStart;
  }

  // start delivers the static channels (TSSG chunk set, lanes.json) and
  // starts the clock — the landing frame at frame 0 ships WITH subscribe.
  async start(): Promise<void> {
    await this.deliverSignals();
    await this.ensureLaneIds();
    this.onStatus(true, `baked replay ${this.index.run}`);
    await this.step();
  }

  async close(): Promise<void> {
    this.closed = true;
    this.gen++;
    this.clearTimer();
  }

  // --- static channels -----------------------------------------------------

  private async fetchBytes(url: string): Promise<Uint8Array> {
    const res = await this.rawFetch(url);
    if (!res.ok) throw new Error(`baked: fetch ${url}: ${res.status}`);
    return new Uint8Array(await res.arrayBuffer());
  }

  // deliverSignals fetches the concatenated TSSG chunk set and feeds each
  // chunk to the accumulator with a synthetic sig_chunk header (§6).
  // signals.chunkBytes is the framing (§5: TSSG chunk lengths are only
  // discoverable by a full structural parse, so the index carries them).
  private async deliverSignals(): Promise<void> {
    const data = await this.fetchBytes(resolveBakedUrl(this.indexUrl, this.index.signals.url));
    const sizes = this.index.signals.chunkBytes;
    const total = sizes.reduce((a, b) => a + b, 0);
    if (total !== data.byteLength) {
      throw new Error(`baked: signals.tssg is ${data.byteLength} bytes, chunkBytes sum to ${total}`);
    }
    let off = 0;
    for (let i = 0; i < sizes.length; i++) {
      const h = natsHeaders();
      h.set("sig_chunk", `${i + 1}/${sizes.length}`);
      this.onSignals(data.subarray(off, off + sizes[i]!), h);
      off += sizes[i]!;
    }
  }

  private async ensureLaneIds(): Promise<void> {
    if (this.laneIds !== null || this.laneIdsInFlight) return;
    this.laneIdsInFlight = true;
    try {
      const res = await this.rawFetch(resolveBakedUrl(this.indexUrl, this.index.laneIds));
      if (!res.ok) throw new Error(`baked: fetch lanes.json: ${res.status}`);
      const raw = (await res.json()) as unknown;
      if (!Array.isArray(raw) || raw.some((s) => typeof s !== "string")) {
        throw new Error("baked: lanes.json is not a string array");
      }
      this.laneIds = raw as string[];
    } finally {
      this.laneIdsInFlight = false;
    }
  }

  // --- viewport / region subscription ---------------------------------------

  setViewport(bounds: readonly [number, number, number, number], zoom: number): void {
    this.gated = zoom < BAKED_VEHICLE_GATE_ZOOM;
    this.regionKeys = regionKeysForBounds(bounds, 0);
    this.laneRegionKeys = regionKeysForBounds(bounds, 1);
    // Prefetch the current position's chunks for the NEW region set so a
    // pan's first emit doesn't wait out the whole stall budget.
    this.ensureTick(this.currentTick);
  }

  private vehicleRegions(): BakedRegion[] {
    return this.index.regions.filter((r) => this.regionKeys.has(r.key));
  }

  private tsrlRegions(): BakedRegion[] {
    // Zoomed out the shim fetches TSRL for ALL regions (the whole city
    // view); zoomed in, the viewport's set inflated by one tile ring (§4).
    return this.gated ? this.index.regions : this.index.regions.filter((r) => this.laneRegionKeys.has(r.key));
  }

  private aggregateTick(tick: number): number {
    const stride = this.index.bakeEveryTicks * this.index.laneEveryFrames;
    return this.index.tickStart + Math.floor((tick - this.index.tickStart) / stride) * stride;
  }

  // ensureTick schedules (fire-and-forget) every chunk covering tick:
  // vehicle chunks for the viewport region set above the gate, TSRL
  // chunks always (lane coloring reads TSRL at every zoom, §4).
  private ensureTick(tick: number): void {
    if (!this.gated) {
      for (const r of this.vehicleRegions()) {
        const chunk = chunkForTick(r.frames, tick);
        if (chunk === null) continue;
        this.ensureChunk(
          this.frameCache,
          resolveBakedUrl(this.indexUrl, chunk.url),
          (buf) => decodeTsrbChunk(buf, this.index.quant),
          r.key,
        );
      }
    }
    const aTick = this.aggregateTick(tick);
    for (const r of this.tsrlRegions()) {
      const chunk = chunkForTick(r.lanes, aTick);
      if (chunk === null) continue;
      this.ensureChunk(this.laneCache, resolveBakedUrl(this.indexUrl, chunk.url), (buf) => decodeTsrlChunk(buf));
    }
    void this.ensureLaneIds();
  }

  // ensureChunk fetches+decodes a chunk once (the cache holds the in-
  // flight promise). A successful land clears the OWNING region's laggard
  // mark — a degraded region pops back in at the next tick, and only its
  // own chunk re-arms it (§3: the barrier stays degraded until the
  // laggard's chunk lands).
  private ensureChunk<F>(
    cache: Map<string, ChunkEntry<F>>,
    url: string,
    decode: (buf: Uint8Array) => F[],
    regionKey?: string,
  ): ChunkEntry<F> {
    const cached = cache.get(url);
    if (cached !== undefined && !cached.failed) return cached;
    const entry: ChunkEntry<F> = { promise: Promise.resolve(null), frames: null, failed: false };
    entry.promise = this.fetchBytes(url)
      .then((buf) => {
        entry.frames = decode(buf);
        if (regionKey !== undefined) this.laggards.delete(regionKey);
        return entry.frames;
      })
      .catch(() => {
        entry.failed = true;
        return null;
      });
    cache.set(url, entry);
    return entry;
  }

  // laneRatiosAt merges the TSRL frames in force at tick (latest
  // aggregate tick ≤ tick) across the region set, resolving lane_idx via
  // lanes.json. Regions whose chunk hasn't landed are omitted — their
  // lanes color in on a later application (the applier diffs). Null while
  // nothing has landed (the applier keeps the previous paint).
  laneRatiosAt(tick: number): Map<string, number> | null {
    if (this.laneIds === null) return null;
    const aTick = this.aggregateTick(tick);
    const out = new Map<string, number>();
    let any = false;
    for (const r of this.tsrlRegions()) {
      const chunk = chunkForTick(r.lanes, aTick);
      if (chunk === null) continue;
      const entry = this.laneCache.get(resolveBakedUrl(this.indexUrl, chunk.url));
      const frames = entry?.frames;
      if (frames == null) continue;
      const frame = frames.find((f) => f.tick === aTick);
      if (frame === undefined) continue;
      for (const p of frame.pairs) {
        const id = this.laneIds[p.laneIdx];
        if (id !== undefined) out.set(id, p.ratio);
      }
      any = true;
    }
    return any ? out : null;
  }

  // --- the clock -------------------------------------------------------------

  private frameIntervalMs(): number {
    return this.index.bakeEveryTicks * this.index.dt * 1000;
  }

  private clearTimer(): void {
    if (this.timer !== null) {
      this.clearTimeoutFn(this.timer);
      this.timer = null;
    }
  }

  private arm(): void {
    if (this.closed || this.paused || this.done) return;
    this.clearTimer();
    this.timer = this.setTimeoutFn(() => void this.step(), this.frameIntervalMs() / this.speed);
  }

  // step emits the current frame and advances the clock. A region stall
  // stalls the CLOCK (§3's barrier): the next frame is scheduled only
  // after this one emits. The generation check drops steps whose schedule
  // a ctl action (pause/seek/speed) invalidated mid-await.
  async step(): Promise<void> {
    if (this.closed || this.paused || this.done) return;
    const gen = this.gen;
    const count = bakedFrameCount(this.index);
    if (this.k >= count) {
      this.done = true;
      return;
    }
    await this.emit(this.k);
    if (gen !== this.gen || this.closed || this.paused || this.done) return;
    this.k++;
    if (this.k >= count) this.done = true;
    else this.arm();
  }

  // emit delivers one baked frame. Below the vehicle gate: an EMPTY
  // synthetic frame — the clock (and with it the TSRL applier, signal
  // derivation, HUD) advances without scheduling vehicle fetches. Above
  // the gate: the merged union of the viewport's region frames, behind
  // the completeness barrier.
  private async emit(k: number): Promise<void> {
    const tick = bakedTickAt(this.index, k);
    this.currentTick = tick;
    this.ensureTick(tick);
    if (this.gated) {
      this.onFrame(encodeTssf(tick, []));
      return;
    }
    const regions = this.vehicleRegions();
    // The completeness barrier (§3): emit tick T only when every
    // subscribed region can cover T. Regions already marked laggard do
    // not re-stall (a 2 s re-stall per tick would be a slideshow).
    const waits: Array<Promise<unknown>> = [];
    for (const r of regions) {
      if (this.laggards.has(r.key)) continue;
      const chunk = chunkForTick(r.frames, tick);
      if (chunk === null) continue;
      const entry = this.frameCache.get(resolveBakedUrl(this.indexUrl, chunk.url));
      if (entry !== undefined && entry.frames === null && !entry.failed) waits.push(entry.promise);
    }
    if (waits.length > 0) {
      await Promise.race([
        Promise.allSettled(waits),
        new Promise<void>((resolve) => this.setTimeoutFn(resolve, this.stallMs)),
      ]);
    }
    const merged: VehicleRecord[] = [];
    for (const r of regions) {
      const chunk = chunkForTick(r.frames, tick);
      if (chunk === null) continue;
      const url = resolveBakedUrl(this.indexUrl, chunk.url);
      const entry = this.frameCache.get(url);
      if (entry === undefined || entry.frames === null) {
        // Still in flight past the deadline, or failed: emit WITHOUT the
        // laggard (a pop at the viewport edge when it lands, never a
        // permanent hole) and evict failures so the next tick retries.
        if (entry !== undefined && entry.failed) this.frameCache.delete(url);
        if (entry !== undefined) this.laggards.add(r.key);
        continue;
      }
      const frame = entry.frames.find((f) => f.tick === tick);
      if (frame !== undefined) merged.push(...frame.vehicles);
    }
    this.onFrame(encodeTssf(tick, merged));
  }

  // --- control plane (the FetchLike stub) -------------------------------------

  private pause(): void {
    this.gen++;
    this.paused = true;
    this.clearTimer();
  }

  private resume(): void {
    this.gen++;
    this.paused = false;
    void this.step();
  }

  private setSpeed(speed: number): void {
    this.gen++;
    this.speed = speed;
    if (!this.paused && !this.done) this.arm();
  }

  // seek floors to the baked stride (§2), emits the landing frame even
  // while paused (the panel's pre-POST onSeeking hook has already reset
  // the stream pipeline), then resumes the schedule when playing.
  private async seek(target: number): Promise<void> {
    this.gen++;
    const gen = this.gen;
    this.clearTimer();
    this.done = false;
    this.k = frameIndexOfTick(this.index, floorToBakedTick(this.index, target));
    await this.emit(this.k);
    if (gen !== this.gen || this.closed) return;
    this.k++;
    if (this.k >= bakedFrameCount(this.index)) this.done = true;
    else if (!this.paused) this.arm();
  }

  private status(): ReplayStatus {
    return {
      run: this.index.run,
      replayRun: this.index.run, // no separate replay run in baked mode; cfg.run is display-only (§6)
      tick: this.currentTick,
      ticks: this.currentTick,
      endTick: this.index.tickEnd,
      speed: this.speed,
      paused: this.paused,
      done: this.done,
      dt: this.index.dt,
      crcErrors: 0, // a divergence would have ABORTED the bake (§1)
      verbErrors: 0,
    };
  }

  // serveControl answers ReplayPanel's exact routes (§6): GET
  // /api/replay/status, POST /api/replay/ctl/{pause,resume,speed,seek}.
  // The ?run= binding is ignored — the stub echoes the index's run id and
  // never 409s a mismatch.
  private async serveControl(
    input: string,
    init?: { method?: string; body?: string; signal?: AbortSignal },
  ): Promise<{ ok: boolean; status: number; json(): Promise<unknown> }> {
    const respond = (status: number, body: unknown) => ({
      ok: status >= 200 && status < 300,
      status,
      json: () => Promise.resolve(body),
    });
    const path = input.split("?")[0] ?? "";
    if (path === "/api/replay/status") return respond(200, this.status());
    const m = /^\/api\/replay\/ctl\/(pause|resume|speed|seek)$/.exec(path);
    if (m === null) return respond(404, { error: `baked: no route ${path}` });
    const verb = m[1]!;
    if (verb === "pause") {
      this.pause();
    } else if (verb === "resume") {
      // End-of-recording hold (§5, Player parity): resume 409s while done
      // until the user seeks back.
      if (this.done) return respond(409, { error: "replay ended — seek back first" });
      this.resume();
    } else if (verb === "speed") {
      const body = JSON.parse(init?.body ?? "{}") as { speed?: unknown };
      if (typeof body.speed !== "number" || !(body.speed > 0)) {
        return respond(400, { error: "baked: bad speed" });
      }
      this.setSpeed(body.speed);
    } else {
      const body = JSON.parse(init?.body ?? "{}") as { tick?: unknown };
      if (typeof body.tick !== "number" || !Number.isFinite(body.tick)) {
        return respond(400, { error: "baked: bad seek tick" });
      }
      await this.seek(body.tick);
    }
    return respond(200, this.status());
  }

  readonly fetchFn: FetchLike = (input, init) => this.serveControl(input, init);
}

// subscribeBaked mirrors subscribeSnapshots (nats-client.ts) over static
// bake artifacts (ADR-0023 §6). The returned surface satisfies
// SnapSubscription (nc null, close) plus the baked-only control seams
// main.ts wires: the ReplayPanel fetch stub, viewport reporting, and the
// TSRL congestion lookup.
export async function subscribeBaked(
  indexUrl: string,
  onFrame: (data: Uint8Array) => void,
  onSignals: (data: Uint8Array, headers: MsgHdrs | undefined) => void,
  onStatus: (connected: boolean, detail: string) => void,
  opts: BakedSessionOpts = {},
): Promise<BakedSubscription> {
  const index = await loadBakedIndex(indexUrl, opts.fetchFn);
  const session = new BakedSession(indexUrl, index, onFrame, onSignals, onStatus, opts);
  await session.start();
  return session;
}
