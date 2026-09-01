/// <reference types="vite/client" />
// hero/main.ts — the 3D hero replay page (ADR-0003 addendum 2026-08-29,
// "hero track"): a standalone three.js renderer over BAKED replay artifacts
// (ADR-0023). No MapLibre, no basemap, no projection, no live NATS — the
// page reads index.json + TSRB/TSSG chunks + network GeoJSON over HTTP and
// renders the network's LOCAL METRIC FRAME directly.
//
// Frame convention (documented choice): engine local metric (x east,
// y north, angle 0 = +x CCW) maps to world (x, 0, −y) — east +X, north −Z,
// up +Y. A top-down view then reads north-up, and the fleet SPEC's
// +Z-forward models take rotation.y = angle + π/2 (angle 0/east → +Z
// rotates onto +X). Engine positions are FRONT BUMPER; models are
// footprint-center anchored, so each group shifts back half its length
// along the heading.
//
// Data flow: the whole bake's TSRB chunks are PRELOADED (pod scale: tens of
// chunks, < 1 MB compressed) into a tick → vehicles map, so any tick is a
// pure lookup — scrubbing and playback render f(tick) with no accumulated
// state. Signal colors for every baked frame are precomputed the same way.
//
// Playback is CONTINUOUS: the rAF loop advances a float tick and poses
// vehicles interpolated between the two bracketing baked frames
// (hero/interp.ts — id-matched lerp, shortest-path heading), so motion
// renders at display refresh instead of the ~2 Hz baked cadence. Signals
// stay discrete: the floor frame's state, no blending.
//
// URL params:
//   ?run=<run>[/<hash12>]   bake under ?base (default /baked/baked/); a bare
//                           run name resolves via the server's directory
//                           listing — first hash12 sorted, warned on console
//                           when several exist (pass run/hash12 to pin)
//   ?bake=<index.json URL>  full control, mirrors app.html's ?bake=
//   ?t=<tick>               initial tick (fractional: lands mid-stride)
//   ?speed=<mult>           initial playback speed (0 = start paused)
//   ?pause=1                start paused (same as speed=0; for captures)
//   ?view=pod|arterial|junction   initial camera preset (keys 1/2/3 switch)
//   ?bare=1                 hide the HUD chrome (screenshot mode)
//   ?panel=<junctionId>     open the timing panel pre-selected (capture hook
//                           for the click path — see hero/timingpanel.ts)
//   ?game=1                 open the "Beat the Signal" game drawer on load
//                           (capture hook — see hero/gamedrawer.ts; key g
//                           toggles it live)
//   ?theme=navy|paper       palette (default navy; theme.ts via getTheme —
//                           pure data, so this page still loads no map code).
//                           View-only: never touches the baked data paths.
//   ?runner=0|1             force the run-service verdict (view-only / live)
//                           and skip the health probe — capture hooks.
//
// Clicking a signal head opens the junction timing panel (retiming overlay,
// "Beat the Signal"): raycast on pointer events ONLY (click = pointerdown/up
// within 5 px so orbit drags never select), never in the rAF loop. The panel
// posts hero:select-junction / hero:deselect to an embedding parent frame.
//
// Serve it exactly like the 2D replay: scripts/serve-baked.py --baked
// data/baked --viz viz/dist --port 8790 (the .br chunks need its
// Content-Encoding: br header), then /hero.html?run=<run>.

import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";

import {
  bakedFrameCount,
  bakedTickAt,
  loadBakedIndex,
  resolveBakedUrl,
  type BakedIndex,
} from "../baked.ts";
import { decodeTsrbChunk } from "../tsrb.ts";
import type { VehicleRecord } from "../tssf.ts";
import {
  decodeSignalFrame,
  sigColorOf,
  stateCharAt,
  type SigColor,
  type SignalTable,
} from "../tssg.ts";
import { HEAD_SETBACK_M, signalHeads, type SignalHead } from "../signals.ts";
import { THEMES, getTheme, type ThemeSpec } from "../theme.ts";
import { BODY_PALETTE, loadFleet, tintFor, type Fleet, type FleetModel } from "../fleet3d/loader.ts";
import { bracketAt, lerpAngle, walkPairs, type Bracket } from "./interp.ts";
import { TimingPanel, runnerAvailable } from "./timingpanel.ts";
import { DesignState, GameDrawer, runSlugOf } from "./gamedrawer.ts";

// --- palette (resolved once from theme.ts — pure data, no map code) ---------
// heroPalette maps a ThemeSpec onto the hero scene: what has a token reads
// the token (bg, casing, signals, sigHousing), what doesn't (centerline,
// guide dashes, mast-arm metal, emissive lifts) derives per theme — navy
// values are the page's original literals, byte-for-byte.
interface HeroPalette {
  bg: string; // scene clear color, fog, ground plane
  casing: string; // lane ribbons
  pavement: string; // junction box fill (navy: = casing; paper: quieter)
  centerline: string;
  guide: string; // dashed turning guides on the pavement
  stopbar: string;
  housing: string; // mast arms, poles, head housings
  laneGlow: number; // emissive lifts: navy floats lanes off the ground…
  pavementGlow: number;
  guideGlow: number;
  centerGlow: number;
  stopbarGlow: number;
  lensGlow: number; // lit lens emissive intensity
  lensOn: Record<"green" | "amber" | "red", string>; // semantic, both themes
  lensDim: string; // unlit lens glass
  hemiSky: string;
  hemiGround: string;
  tints: readonly string[]; // vehicle body tint list (see PAPER_TINTS)
}

// PAPER_TINTS: the paper theme's body-tint list — the navy palette's
// near-whites (#eaf0ff, #9fb2e8) vanish on #fafafa, so paper draws from its
// own vocabulary: ink, accent blue, vibes marker orange, semantic
// red/green, and a gray (fleet3d SPEC.md records the substitution rule).
const PAPER_TINTS = ["#26262b", "#2563eb", "#ED4B16", "#e5484d", "#1e9e6a", "#6e6e76"];

function heroPalette(t: ThemeSpec, paper: boolean): HeroPalette {
  return {
    bg: t.bg,
    casing: t.casing,
    pavement: paper ? t.noData : t.casing, // a big casing-gray box on white is too heavy
    centerline: paper ? t.noData : "#3d5bff",
    guide: paper ? t.casing : "#2c40a8",
    stopbar: paper ? t.stopRim : "#c9d6ff", // paint: light on dark, white on gray paper
    housing: paper ? t.hudText : t.sigHousing, // ink metal on paper, dark navy housing
    laneGlow: paper ? 0 : 0.25, // …but emissive lifts wash light colors to white
    pavementGlow: paper ? 0 : 0.25,
    guideGlow: paper ? 0 : 0.3,
    centerGlow: paper ? 0 : 0.55,
    stopbarGlow: paper ? 0 : 0.3,
    lensGlow: paper ? 1.6 : 2.2,
    lensOn: { green: t.signalGreen, amber: t.signalAmber, red: t.signalRed },
    lensDim: paper ? "#3f3f46" : "#0d1126", // dark glass on the ink housing
    hemiSky: paper ? "#ffffff" : "#d6e1ff",
    hemiGround: paper ? "#d8d8dc" : "#0a1230",
    tints: paper ? PAPER_TINTS : BODY_PALETTE,
  };
}

// hexA renders a #rrggbb token as rgba() (non-hex tokens pass through) —
// the HUD chrome's alpha-stepped borders/hover fills derive from the
// theme's own tokens, so both themes stay on-palette.
function hexA(hex: string, alpha: number): string {
  const m = /^#([0-9a-f]{6})$/i.exec(hex);
  if (m === null) return hex;
  const n = parseInt(m[1]!, 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}

// heroChromeVars maps the resolved theme onto hero.html's --t-* custom
// properties (same pattern as the 2D app's main.ts). Navy values reproduce
// the page's original literals exactly; paper values are ThemeSpec tokens
// except the noted derivations (accent = district blue, marker = vibes
// orange, button ink = housing white).
function heroChromeVars(t: ThemeSpec, paper: boolean): Record<string, string> {
  return {
    "--t-bg": t.bg,
    "--t-hud-bg": t.hudBg,
    "--t-hud-border": t.hudBorder,
    "--t-text": t.hudText,
    "--t-text-dim": t.hudTextDim,
    "--t-overlay": t.overlayBg,
    "--t-edge": hexA(t.hudBorder, 0.55), // interactive borders
    "--t-line": hexA(t.hudBorder, 0.35), // row/track borders
    "--t-sep": hexA(t.hudBorder, 0.2), // list separators
    "--t-hover": hexA(t.hudBorder, 0.25), // button/tab hover fill
    "--t-row-hover": hexA(t.hudBorder, 0.18), // drawer row hover fill
    "--t-accent": paper ? t.district : t.hudBorder, // guest bars, scrubber
    "--t-marker": paper ? t.stopped : "#7aa2ff", // the current-run/"you" marker
    "--t-btn-bg": paper ? t.district : t.hudText,
    "--t-btn-ink": paper ? t.sigHousing : t.bg,
    "--t-btn-hover": paper ? hexA(t.district, 0.8) : t.stopRim,
    "--t-input-bg": hexA(t.sigHousing, 0.8),
    "--t-track-bg": paper ? hexA(t.noData, 0.35) : hexA(t.sigHousing, 0.8),
    "--t-baseline": paper ? t.noData : "#4a5c96", // leaderboard baseline fill
    "--t-warn": paper ? t.corridor : t.mid, // notices, fleet note, edited pill
    "--t-bad": t.signalRed,
    "--t-good": t.signalGreen,
    "--t-on": paper ? t.signalGreen : t.freeFlow, // the playing-state tint
  };
}

// The bake does not carry the scenario's type list; TSRB cls is the
// scenario type index (tssf.ts: 0 = car, 1 = truck). bottleneck-town runs
// types [car, truck] — anything out of range falls back to car dims.
const CLASS_BY_INDEX = ["car", "truck"] as const;

const SIG_COLORS: readonly SigColor[] = ["off", "green", "amber", "red"];

// --- data shapes ------------------------------------------------------------

interface LaneFeature {
  properties: { id?: string; width?: number; internal?: boolean };
  geometry: { type?: string; coordinates?: number[][] };
}

interface HeroData {
  indexUrl: string;
  index: BakedIndex;
  frames: Map<number, VehicleRecord[]>; // baked tick → merged region vehicles
  lanes: LaneFeature[];
  table: SignalTable;
  heads: SignalHead[];
  fleet: Fleet;
}

// --- bake resolution --------------------------------------------------------

// listSubdirs parses a python http.server-style directory listing (the
// serve-baked.py precedent) for subdirectory names. Deterministic: sorted.
async function listSubdirs(url: string): Promise<string[]> {
  const res = await fetch(url, { cache: "no-cache" });
  if (!res.ok) throw new Error(`fetch ${url}: HTTP ${res.status}`);
  const html = await res.text();
  const out = new Set<string>();
  for (const m of html.matchAll(/href="([^"./?#][^"?#/]*)\//g)) out.add(m[1]!);
  return [...out].sort();
}

async function resolveIndexUrl(params: URLSearchParams): Promise<string> {
  const bake = params.get("bake");
  if (bake !== null && bake !== "") return new URL(bake, location.href).href;
  const base = params.get("base") ?? "/baked/baked/";
  const run = params.get("run");
  // baked.ts resolves chunk URLs with new URL(rel, indexUrl), which throws
  // "Invalid base URL" on a root-relative base — return absolute URLs.
  const abs = (path: string): string => new URL(path, location.origin).href;
  if (run !== null && run.includes("/")) return abs(`${base}${run}/index.json`);
  const pickHash = async (name: string): Promise<string> => {
    const hashes = (await listSubdirs(`${base}${name}/`)).filter((d) => /^[0-9a-f]{12}$/.test(d));
    if (hashes.length === 0) throw new Error(`no baked <hash12> under ${base}${name}/`);
    if (hashes.length > 1) {
      console.warn(`hero: run "${name}" has ${hashes.length} hashes; using first sorted`, hashes);
    }
    return abs(`${base}${name}/${hashes[0]!}/index.json`);
  };
  if (run !== null && run !== "") return pickHash(run);
  const runs = await listSubdirs(base);
  if (runs.length === 0) throw new Error(`no baked runs under ${base}`);
  return pickHash(runs[0]!);
}

// --- loading ----------------------------------------------------------------

async function fetchBytes(url: string): Promise<Uint8Array> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`fetch ${url}: HTTP ${res.status}`);
  return new Uint8Array(await res.arrayBuffer());
}

// loadFrames fetches EVERY region's TSRB chunks (bounded concurrency) and
// merges per tick. Pod bakes are tens of chunks; preload makes every tick a
// pure lookup, which is what makes scrubbing drift-free.
async function loadFrames(
  index: BakedIndex,
  indexUrl: string,
  onProgress: (done: number, total: number) => void,
): Promise<Map<number, VehicleRecord[]>> {
  const urls: string[] = [];
  for (const r of index.regions) for (const c of r.frames) urls.push(resolveBakedUrl(indexUrl, c.url));
  const decoded: Array<{ tick: number; vehicles: VehicleRecord[] }[] | null> = new Array(urls.length).fill(null);
  let next = 0;
  let done = 0;
  const worker = async (): Promise<void> => {
    while (next < urls.length) {
      const i = next++;
      decoded[i] = decodeTsrbChunk(await fetchBytes(urls[i]!), index.quant);
      onProgress(++done, urls.length);
    }
  };
  const lanes = Math.min(6, urls.length);
  await Promise.all(Array.from({ length: lanes }, () => worker()));
  const byTick = new Map<number, VehicleRecord[]>();
  for (const fs of decoded) {
    if (fs === null) continue;
    for (const f of fs) {
      const arr = byTick.get(f.tick);
      if (arr !== undefined) arr.push(...f.vehicles);
      else byTick.set(f.tick, [...f.vehicles]);
    }
  }
  // Id-sorted once here so playback's two-pointer frame match
  // (interp.ts walkPairs) stays allocation-free in the rAF hot loop.
  for (const arr of byTick.values()) arr.sort((a, b) => a.id - b.id);
  return byTick;
}

async function loadSignalTable(index: BakedIndex, indexUrl: string): Promise<SignalTable> {
  const data = await fetchBytes(resolveBakedUrl(indexUrl, index.signals.url));
  const sizes = index.signals.chunkBytes;
  const total = sizes.reduce((a, b) => a + b, 0);
  if (total !== data.byteLength) {
    throw new Error(`signals: ${data.byteLength} bytes, chunkBytes sum to ${total}`);
  }
  const programs: SignalTable["programs"] = [];
  let off = 0;
  for (const n of sizes) {
    programs.push(...decodeSignalFrame(data.subarray(off, off + n)).programs);
    off += n;
  }
  return { tick: 0, programs };
}

async function loadLanes(index: BakedIndex, indexUrl: string): Promise<LaneFeature[]> {
  if (typeof index.network.geojson !== "string") {
    throw new Error("hero renders GeoJSON bakes (pods) only — this bake's network is PMTiles (city scale)");
  }
  const res = await fetch(resolveBakedUrl(indexUrl, index.network.geojson));
  if (!res.ok) throw new Error(`fetch network.geojson: HTTP ${res.status}`);
  const raw = (await res.json()) as { features?: LaneFeature[] };
  if (!Array.isArray(raw.features)) throw new Error("network.geojson: no features array");
  return raw.features;
}

async function loadAll(params: URLSearchParams, onStatus: (s: string) => void): Promise<HeroData> {
  onStatus("resolving bake…");
  const indexUrl = await resolveIndexUrl(params);
  const index = await loadBakedIndex(indexUrl);
  onStatus(`bake ${index.run} · loading network…`);
  const lanes = await loadLanes(index, indexUrl);
  onStatus(`bake ${index.run} · loading signals…`);
  const table = await loadSignalTable(index, indexUrl);
  const shapesByLane = new Map<string, ReadonlyArray<readonly number[]>>();
  for (const f of lanes) {
    if (typeof f.properties.id === "string" && Array.isArray(f.geometry.coordinates)) {
      shapesByLane.set(f.properties.id, f.geometry.coordinates);
    }
  }
  const heads = signalHeads(table, shapesByLane);
  onStatus(`bake ${index.run} · loading fleet manifest…`);
  const fleet = await loadFleet(new URL("../fleet3d/manifest.json", import.meta.url).href);
  const frames = await loadFrames(index, indexUrl, (d, t) => onStatus(`bake ${index.run} · frames ${d}/${t}…`));
  return { indexUrl, index, frames, lanes, table, heads, fleet };
}

// --- scene construction -----------------------------------------------------

interface RibbonTarget {
  positions: number[]; // world-space triangle soup (non-indexed)
}

// ribbon emits a flat strip halfW either side of the polyline, with averaged
// vertex normals at joins. mapX/mapZ apply the (x, y) → (x, 0, −y) frame map.
function ribbon(pts: number[][], halfW: number, y: number, out: RibbonTarget): void {
  const n = pts.length;
  if (n < 2) return;
  const normals = new Array<[number, number]>(n);
  for (let i = 0; i < n; i++) {
    let nx = 0;
    let ny = 0;
    for (const j of [i - 1, i]) {
      if (j < 0 || j >= n - 1) continue;
      const a = pts[j]!;
      const b = pts[j + 1]!;
      const dx = b[0]! - a[0]!;
      const dy = b[1]! - a[1]!;
      const len = Math.hypot(dx, dy);
      if (len < 1e-9) continue;
      nx += -dy / len;
      ny += dx / len;
    }
    const nl = Math.hypot(nx, ny);
    normals[i] = nl < 1e-9 ? [0, 1] : [nx / nl, ny / nl];
  }
  for (let i = 0; i < n - 1; i++) {
    const a = pts[i]!;
    const b = pts[i + 1]!;
    const na = normals[i]!;
    const nb = normals[i + 1]!;
    // two triangles: (aL, aR, bL) (aR, bR, bL); world z = −y
    const axl = a[0]! + na[0] * halfW;
    const azl = -(a[1]! + na[1] * halfW);
    const axr = a[0]! - na[0] * halfW;
    const azr = -(a[1]! - na[1] * halfW);
    const bxl = b[0]! + nb[0] * halfW;
    const bzl = -(b[1]! + nb[1] * halfW);
    const bxr = b[0]! - nb[0] * halfW;
    const bzr = -(b[1]! - nb[1] * halfW);
    out.positions.push(axl, y, azl, axr, y, azr, bxl, y, bzl);
    out.positions.push(axr, y, azr, bxr, y, bzr, bxl, y, bzl);
  }
}

function meshOf(target: RibbonTarget, color: string, emissiveIntensity = 0): THREE.Mesh {
  const geo = new THREE.BufferGeometry();
  geo.setAttribute("position", new THREE.Float32BufferAttribute(target.positions, 3));
  geo.computeVertexNormals();
  const mesh = new THREE.Mesh(
    geo,
    new THREE.MeshStandardMaterial({ color, emissive: color, emissiveIntensity, flatShading: true, roughness: 1 }),
  );
  mesh.matrixAutoUpdate = false;
  return mesh;
}

// quad emits two triangles for the metric-frame rectangle corners (x, y),
// winding checked so the face normal points +Y in world (z = −y) whatever
// the corner order.
function quad(corners: number[][], y: number, out: RibbonTarget): void {
  const w = corners.map(([x, yy]) => [x!, y, -yy!] as const);
  const a = w[0]!;
  const b = w[1]!;
  const c = w[2]!;
  const d = w[3]!;
  // y-component of (b−a)×(c−a) with y constant: ≥ 0 means (a,b,c) faces up.
  const crossY = (b[2] - a[2]) * (c[0] - a[0]) - (b[0] - a[0]) * (c[2] - a[2]);
  const t = crossY >= 0 ? [a, b, c, d] : [a, d, c, b];
  out.positions.push(...t[0]!, ...t[1]!, ...t[2]!);
  out.positions.push(...t[0]!, ...t[2]!, ...t[3]!);
}

// dashed emits the polyline as broken paint (dash on / gap off), reusing
// ribbon per on-interval so the guides get the same averaged-join normals.
function dashed(pts: number[][], halfW: number, y: number, out: RibbonTarget, dash = 2, gap = 2): void {
  let phase = dash; // remaining length of the current on/off interval
  let on = true;
  let cur: number[][] = [];
  const flush = (): void => {
    if (cur.length >= 2) ribbon(cur, halfW, y, out);
    cur = [];
  };
  for (let i = 0; i < pts.length - 1; i++) {
    const a = pts[i]!;
    const b = pts[i + 1]!;
    const segLen = Math.hypot(b[0]! - a[0]!, b[1]! - a[1]!);
    let s = 0; // distance walked into this segment
    while (s < segLen - 1e-9) {
      const step = Math.min(phase, segLen - s);
      if (on) {
        const t0 = s / segLen;
        const t1 = (s + step) / segLen;
        if (cur.length === 0) {
          cur.push([a[0]! + (b[0]! - a[0]!) * t0, a[1]! + (b[1]! - a[1]!) * t0]);
        }
        cur.push([a[0]! + (b[0]! - a[0]!) * t1, a[1]! + (b[1]! - a[1]!) * t1]);
      }
      s += step;
      phase -= step;
      if (phase <= 1e-9) {
        if (on) flush();
        on = !on;
        phase = on ? dash : gap;
      }
    }
  }
  flush();
}

interface Bounds {
  minX: number;
  maxX: number;
  minY: number;
  maxY: number;
}

function boundsOf(lanes: LaneFeature[]): Bounds {
  const b = { minX: Infinity, maxX: -Infinity, minY: Infinity, maxY: -Infinity };
  for (const f of lanes) {
    for (const p of f.geometry.coordinates ?? []) {
      if (p[0]! < b.minX) b.minX = p[0]!;
      if (p[0]! > b.maxX) b.maxX = p[0]!;
      if (p[1]! < b.minY) b.minY = p[1]!;
      if (p[1]! > b.maxY) b.maxY = p[1]!;
    }
  }
  if (!Number.isFinite(b.minX)) return { minX: -100, maxX: 100, minY: -100, maxY: 100 };
  return b;
}

// buildLanes draws external lanes as casing + centerline ribbons and every
// SIGNAL-BOUND internal lane's junction as a pavement rectangle in the same
// casing material — the intersection reads as continuous asphalt, not a
// hole. The rectangle is the (u, v)-frame bbox of the bound lanes' END
// SEGMENTS (each lane end contributes its center ± halfW perpendicular to
// its end bearing), u aligned to the first bound lane's entry bearing: the
// edges then land exactly on the outermost incoming/outgoing lane ends.
// Bound internal lanes render as dashed turning guides on the pavement;
// internal lanes no program claims (unsignalized junctions) fall back to
// plain casing ribbons — pavement-colored, never the dark blob.
function buildLanes(scene: THREE.Scene, lanes: LaneFeature[], table: SignalTable, pal: HeroPalette): Bounds {
  const casing: RibbonTarget = { positions: [] };
  const center: RibbonTarget = { positions: [] };
  const pavement: RibbonTarget = { positions: [] };
  const guide: RibbonTarget = { positions: [] };
  const junctionOf = new Map<string, string>();
  for (const p of table.programs) for (const l of p.links) junctionOf.set(l.laneId, p.junction);
  const byJunction = new Map<string, LaneFeature[]>();
  for (const f of lanes) {
    const pts = f.geometry.coordinates;
    if (!Array.isArray(pts) || pts.length < 2) continue;
    const halfW = (typeof f.properties.width === "number" ? f.properties.width : 3.5) / 2;
    if (f.properties.internal !== true) {
      ribbon(pts, halfW, 0.0, casing);
      ribbon(pts, 0.07, 0.04, center);
      continue;
    }
    const j = typeof f.properties.id === "string" ? junctionOf.get(f.properties.id) : undefined;
    if (j === undefined) {
      ribbon(pts, halfW, 0.0, casing); // unsignalized interior: asphalt, not blob
      continue;
    }
    const group = byJunction.get(j) ?? [];
    group.push(f);
    byJunction.set(j, group);
    dashed(pts, 0.06, 0.045, guide);
  }
  for (const group of byJunction.values()) {
    // Frame axis: the first lane's entry bearing (any approach works — the
    // bbox of the end segments is the same rectangle for a right-angle
    // junction; skewed junctions get a best-effort rotated box).
    let u: [number, number] | null = null;
    const ends: number[][] = [];
    for (const f of group) {
      const pts = f.geometry.coordinates!;
      const halfW = (typeof f.properties.width === "number" ? f.properties.width : 3.5) / 2;
      for (const [at, from, to] of [
        [0, 0, 1],
        [pts.length - 1, pts.length - 2, pts.length - 1],
      ] as const) {
        const dx = pts[to]![0]! - pts[from]![0]!;
        const dy = pts[to]![1]! - pts[from]![1]!;
        const len = Math.hypot(dx, dy);
        if (len < 1e-6) continue;
        const bx = dx / len;
        const by = dy / len;
        if (u === null) u = [bx, by];
        // lane end = center ± halfW along the bearing's perpendicular
        ends.push(
          [pts[at]![0]! - by * halfW, pts[at]![1]! + bx * halfW],
          [pts[at]![0]! + by * halfW, pts[at]![1]! - bx * halfW],
        );
      }
    }
    if (u === null || ends.length < 4) continue;
    const [ux, uy] = u;
    const vx = -uy;
    const vy = ux;
    let minU = Infinity;
    let maxU = -Infinity;
    let minV = Infinity;
    let maxV = -Infinity;
    for (const [x, y] of ends) {
      const pu = x! * ux + y! * uy;
      const pv = x! * vx + y! * vy;
      if (pu < minU) minU = pu;
      if (pu > maxU) maxU = pu;
      if (pv < minV) minV = pv;
      if (pv > maxV) maxV = pv;
    }
    quad(
      [
        [ux * minU + vx * minV, uy * minU + vy * minV],
        [ux * maxU + vx * minV, uy * maxU + vy * minV],
        [ux * maxU + vx * maxV, uy * maxU + vy * maxV],
        [ux * minU + vx * maxV, uy * minU + vy * maxV],
      ],
      0.02,
      pavement,
    );
  }
  // A touch of emissive lifts the lanes off the same-hued ground without
  // leaving the theme palette (the 2D viz gets the same separation from
  // casing-vs-fill paint, which a flat 3D ribbon doesn't have). The
  // pavement shares the casing hue so box and lanes read as one surface;
  // guides sit under the centerline brightness (subtle). Paper's glows are
  // 0 — emissive lifts wash light colors out on the white ground.
  scene.add(meshOf(casing, pal.casing, pal.laneGlow));
  scene.add(meshOf(pavement, pal.pavement, pal.pavementGlow));
  scene.add(meshOf(guide, pal.guide, pal.guideGlow));
  scene.add(meshOf(center, pal.centerline, pal.centerGlow));
  return boundsOf(lanes);
}

// --- signals -----------------------------------------------------------------

interface SigRig {
  lenses: [THREE.Mesh, THREE.Mesh, THREE.Mesh]; // stack top→bottom: red, amber, green
  colorIdx: number;
  pick: THREE.Object3D; // head group; userData.junction set, raycast target for the panel
}

const LENS_ORDER = ["red", "amber", "green"] as const; // stack top→bottom
const LENS_ON: THREE.MeshStandardMaterial[] = []; // active lens: emissive-bright
const LENS_DIM: THREE.MeshStandardMaterial[] = []; // idle lens: dark glass, hint of color

function sigMaterials(pal: HeroPalette): void {
  for (const c of LENS_ORDER) {
    const hex = pal.lensOn[c];
    LENS_ON.push(
      new THREE.MeshStandardMaterial({ color: hex, emissive: hex, emissiveIntensity: pal.lensGlow, flatShading: true }),
    );
    LENS_DIM.push(
      new THREE.MeshStandardMaterial({ color: pal.lensDim, emissive: hex, emissiveIntensity: 0.07, flatShading: true }),
    );
  }
}

// Mast-arm dimensions (m), US style: a pole on the RIGHT roadside of the
// approach (in the direction of travel — never in a lane, never in the
// junction interior), a horizontal arm over the lanes at ARM_H, and the
// 3-lens heads hanging from the arm above their lane group's centerline.
const POLE_H = 6;
const ARM_H = 5.5;
const HEAD_H = 1.5; // housing height; lenses stack inside
const ARM_SET_IN = 0.6; // the arm (and heads) sit this far inside the stop line
const POLE_CLEAR = 1.0; // pole stands this far outside the outermost bar end
const ARM_OVERHANG = 1.2; // the arm continues past the farthest head
const LANE_HALF = 1.75; // fallback half-width when a head has no stop bar

// headGeo resolves a head's stop-line centroid and unit entry bearing —
// exported by signals.ts for geometry-clustered heads, inferred from the
// stop bar's perpendicular (oriented at the junction centroid) for baked
// furniture heads. Null when neither exists.
interface HeadGeo {
  cx: number;
  cy: number;
  dx: number;
  dy: number;
}

function headGeo(h: SignalHead, jx: number, jy: number): HeadGeo | null {
  if (h.cx !== undefined && h.cy !== undefined && h.dirX !== undefined && h.dirY !== undefined) {
    return { cx: h.cx, cy: h.cy, dx: h.dirX, dy: h.dirY };
  }
  if (h.bar !== null) {
    let dx = -(h.bar[3] - h.bar[1]);
    let dy = h.bar[2] - h.bar[0];
    const len = Math.hypot(dx, dy);
    if (len < 1e-6) return null;
    dx /= len;
    dy /= len;
    if (dx * (jx - h.x) + dy * (jy - h.y) < 0) {
      dx = -dx;
      dy = -dy;
    }
    return { cx: h.x + dx * HEAD_SETBACK_M, cy: h.y + dy * HEAD_SETBACK_M, dx, dy };
  }
  return null;
}

// buildSignals erects one mast-arm rig per signalized APPROACH: heads are
// grouped per junction, then clustered by entry bearing (same ~45° cone
// rule as signals.ts' approach split). Per approach the pole goes on the
// right roadside at the stop line, the arm spans the approach lanes, and
// each clustered head hangs above ITS cluster's stop-line centroid — so a
// through-group head and a left-turn head share the pole but show their
// own program link's state over their own lanes. Heads without usable
// geometry keep a plain post at their (set-back) point. Stop bars are one
// slim neutral bar per cluster at the actual stop line — the overhead
// heads carry the state now, so the bars no longer need to.
function buildSignals(scene: THREE.Scene, heads: SignalHead[], pal: HeroPalette): SigRig[] {
  const poleGeo = new THREE.CylinderGeometry(0.18, 0.24, POLE_H, 8);
  const armGeo = new THREE.BoxGeometry(1, 0.26, 0.26); // unit length, scaled per arm
  const housingGeo = new THREE.BoxGeometry(0.62, HEAD_H, 0.5);
  const lensGeo = new THREE.SphereGeometry(0.3, 10, 8);
  const dark = new THREE.MeshStandardMaterial({ color: pal.housing, flatShading: true });
  const barMat = new THREE.MeshStandardMaterial({
    color: pal.stopbar,
    emissive: pal.stopbar,
    emissiveIntensity: pal.stopbarGlow,
    flatShading: true,
  });
  // rigs is indexed BY HEAD (render() looks up sigFrames by head index), so
  // approach grouping only decides WHERE things stand, not the array order.
  const rigs: SigRig[] = new Array(heads.length);

  // addHead builds the 3-lens housing at world (x, z), lenses facing local
  // +Z rotated by ry, housing bottom at yBot (hangs from the arm at ARM_H).
  // Each group also carries an invisible pick proxy: the housing is ~0.6 m
  // across (a few px at replay distance), so the timing panel's raycast
  // targets this bigger box instead (opacity 0 writes nothing to the frame;
  // raycast tests geometry, not opacity).
  const pickGeo = new THREE.BoxGeometry(2.8, HEAD_H + 1.6, 2.6);
  const pickMat = new THREE.MeshBasicMaterial({ transparent: true, opacity: 0, depthWrite: false });
  const addHead = (
    x: number,
    z: number,
    ry: number,
    yBot: number,
  ): { group: THREE.Group; lenses: [THREE.Mesh, THREE.Mesh, THREE.Mesh] } => {
    const g = new THREE.Group();
    g.position.set(x, 0, z);
    g.rotation.y = ry;
    const housing = new THREE.Mesh(housingGeo, dark);
    housing.position.y = yBot + HEAD_H / 2;
    g.add(housing);
    const proxy = new THREE.Mesh(pickGeo, pickMat);
    proxy.position.y = yBot + HEAD_H / 2;
    g.add(proxy);
    const lenses: THREE.Mesh[] = [];
    for (let i = 0; i < 3; i++) {
      const lens = new THREE.Mesh(lensGeo, LENS_DIM[i]);
      lens.position.set(0, yBot + HEAD_H / 2 + (1 - i) * 0.48, 0.27);
      g.add(lens);
      lenses.push(lens);
    }
    g.traverse((o) => {
      o.matrixAutoUpdate = false;
      o.updateMatrix();
    });
    g.updateMatrix();
    scene.add(g);
    return { group: g, lenses: lenses as [THREE.Mesh, THREE.Mesh, THREE.Mesh] };
  };

  const geoByIdx = new Map<number, HeadGeo>();
  const byJunction = new Map<string, number[]>();
  for (const [i, h] of heads.entries()) {
    const list = byJunction.get(h.program.junction) ?? [];
    list.push(i);
    byJunction.set(h.program.junction, list);
  }
  const fallback: number[] = [];
  for (const idxs of byJunction.values()) {
    // Junction centroid (mean of stop-line centroids) orients the bar-based
    // bearing fallback and the geometry-less fallback rig.
    let jx = 0;
    let jy = 0;
    for (const i of idxs) {
      const h = heads[i]!;
      jx += h.cx ?? h.x;
      jy += h.cy ?? h.y;
    }
    jx /= idxs.length;
    jy /= idxs.length;
    const approaches: Array<{ bx: number; by: number; idxs: number[] }> = [];
    for (const i of idxs) {
      const g = headGeo(heads[i]!, jx, jy);
      if (g === null) {
        fallback.push(i);
        continue;
      }
      geoByIdx.set(i, g);
      let a = approaches.find((a) => {
        const bl = Math.hypot(a.bx, a.by);
        return bl > 1e-6 && (a.bx / bl) * g.dx + (a.by / bl) * g.dy > Math.SQRT1_2;
      });
      if (a === undefined) {
        a = { bx: 0, by: 0, idxs: [] };
        approaches.push(a);
      }
      a.bx += g.dx;
      a.by += g.dy;
      a.idxs.push(i);
    }

    for (const a of approaches) {
      const bl = Math.hypot(a.bx, a.by);
      const ux = a.bx / bl;
      const uy = a.by / bl; // travel direction INTO the junction
      const rx = uy;
      const ry = -ux; // right of travel
      // Stop line (u) and lateral (d = p·r) extents from the heads' stop
      // bars where they exist, centroids ± half a lane otherwise.
      let su = 0;
      let minD = Infinity;
      let maxD = -Infinity;
      for (const i of a.idxs) {
        const h = heads[i]!;
        const g = geoByIdx.get(i)!;
        su += g.cx * ux + g.cy * uy;
        if (h.bar !== null) {
          for (const [bx, by] of [
            [h.bar[0], h.bar[1]],
            [h.bar[2], h.bar[3]],
          ] as const) {
            const d = bx * rx + by * ry;
            if (d < minD) minD = d;
            if (d > maxD) maxD = d;
          }
        } else {
          const d = g.cx * rx + g.cy * ry;
          if (d - LANE_HALF < minD) minD = d - LANE_HALF;
          if (d + LANE_HALF > maxD) maxD = d + LANE_HALF;
        }
      }
      su /= a.idxs.length;
      const poleD = maxD + POLE_CLEAR;
      const armSu = su + ARM_SET_IN;
      const px = ux * armSu + rx * poleD;
      const py = uy * armSu + ry * poleD;
      const pole = new THREE.Mesh(poleGeo, dark);
      pole.position.set(px, POLE_H / 2, -py);
      pole.updateMatrix();
      pole.matrixAutoUpdate = false;
      scene.add(pole);
      const farD = minD - ARM_OVERHANG;
      const arm = new THREE.Mesh(armGeo, dark);
      arm.scale.x = poleD - farD;
      arm.position.set(ux * armSu + rx * ((poleD + farD) / 2), ARM_H, -(uy * armSu + ry * ((poleD + farD) / 2)));
      // arm's +X runs from the pole over the road: metric −r, world (−rx, +ry)
      arm.rotation.y = Math.atan2(-ry, -rx);
      arm.updateMatrix();
      arm.matrixAutoUpdate = false;
      scene.add(arm);
      for (const i of a.idxs) {
        const g = geoByIdx.get(i)!;
        const d = g.cx * rx + g.cy * ry;
        const hx = ux * armSu + rx * d;
        const hy = uy * armSu + ry * d;
        // lenses face oncoming traffic: metric −u, world (−ux, +uy)
        const { group, lenses } = addHead(hx, -hy, Math.atan2(-ux, uy), ARM_H - HEAD_H);
        group.userData["junction"] = heads[i]!.program.junction;
        rigs[i] = { lenses, colorIdx: 0, pick: group };
      }
      // One slim neutral stop bar per cluster at the actual stop line.
      for (const i of a.idxs) {
        const h = heads[i]!;
        if (h.bar === null) continue;
        const [x1, y1, x2, y2] = h.bar;
        const len = Math.hypot(x2 - x1, y2 - y1);
        if (len < 0.5) continue;
        const bar = new THREE.Mesh(new THREE.BoxGeometry(len, 0.04, 0.38), barMat);
        bar.position.set((x1 + x2) / 2, 0.06, -(y1 + y2) / 2);
        bar.rotation.y = Math.atan2(y2 - y1, x2 - x1);
        bar.updateMatrix();
        bar.matrixAutoUpdate = false;
        scene.add(bar);
      }
    }
  }
  // Geometry-less heads (no cluster geometry, no bar): a plain post with
  // the 3-lens head on top, facing the junction centroid.
  for (const i of fallback) {
    const h = heads[i]!;
    const post = new THREE.Mesh(poleGeo, dark);
    post.position.set(h.x, POLE_H / 2, -h.y);
    post.updateMatrix();
    post.matrixAutoUpdate = false;
    scene.add(post);
    const { group, lenses } = addHead(h.x, -h.y, 0, POLE_H - HEAD_H);
    group.userData["junction"] = h.program.junction;
    rigs[i] = { lenses, colorIdx: 0, pick: group };
  }
  return rigs;
}

// --- vehicles ---------------------------------------------------------------

interface VehicleEntry {
  group: THREE.Group;
  model: FleetModel;
  poolKey: string;
  lastK: number;
}

// makeVehicle builds one tinted instance. build() was smoke-tested at load;
// the try/catch is belt-and-braces so a model that breaks later still
// cannot break the page (SPEC: any build error → the class box). poolKey
// stays the REQUESTED model+tint so pool bookkeeping agrees with apply().
function makeVehicle(fleet: Fleet, model: FleetModel, tint: string): VehicleEntry {
  let group: THREE.Group;
  let m = model;
  try {
    group = model.build();
  } catch {
    m = fleet.pick(0, "\0box"); // forces the ladder to its box end
    group = m.build();
  }
  group.traverse((o) => {
    if (o.name === "body" && o instanceof THREE.Mesh) {
      const src = Array.isArray(o.material) ? o.material[0]! : o.material;
      const tinted = src.clone();
      tinted.color.set(tint);
      o.material = tinted;
    }
  });
  return { group, model: m, poolKey: `${model.id}|${tint}`, lastK: -1 };
}

class VehiclePool {
  private readonly active = new Map<number, VehicleEntry>();
  private readonly free = new Map<string, VehicleEntry[]>();
  private seq = 0; // monotonic render stamp; entries untouched this pass retire
  constructor(
    private readonly scene: THREE.Scene,
    private readonly fleet: Fleet,
    private readonly tints: readonly string[], // theme's body-tint list (pal.tints)
  ) {}

  // acquire returns the live entry for v's id, pulling a pooled vehicle of
  // the right model+tint or building one.
  private acquire(v: VehicleRecord): VehicleEntry {
    let e = this.active.get(v.id);
    if (e === undefined) {
      const cls = CLASS_BY_INDEX[v.cls] ?? "car";
      const model = this.fleet.pick(v.id, cls);
      const tint = tintFor(v.id, this.tints);
      const key = `${model.id}|${tint}`;
      const pooled = this.free.get(key);
      e = pooled !== undefined && pooled.length > 0 ? pooled.pop()! : makeVehicle(this.fleet, model, tint);
      e.model = model;
      this.active.set(v.id, e);
      this.scene.add(e.group);
    }
    return e;
  }

  // pose sets the group transform for a FRONT-BUMPER metric pose: shift
  // back half the model length along the heading (models are footprint-
  // center anchored). y = 0.1 rides above the lane ribbons/centerlines
  // with clear depth separation; invisible as float at replay distance.
  private pose(e: VehicleEntry, x: number, y: number, angle: number): void {
    const half = e.model.dims.length / 2;
    const c = Math.cos(angle);
    const s = Math.sin(angle);
    e.group.position.set(x - c * half, 0.1, -y + s * half);
    e.group.rotation.y = angle + Math.PI / 2;
    e.group.visible = true;
  }

  // retire hides entries this pass didn't touch, returning them to the
  // pool. No allocations once warmed: entries are reused by model+tint.
  private retire(k: number): void {
    for (const [id, e] of this.active) {
      if (e.lastK !== k) {
        e.group.visible = false;
        this.active.delete(id);
        let list = this.free.get(e.poolKey);
        if (list === undefined) {
          list = [];
          this.free.set(e.poolKey, list);
        }
        list.push(e);
      }
    }
  }

  // apply poses one exact baked frame and retires everything else.
  apply(vehicles: readonly VehicleRecord[] | undefined): void {
    const k = ++this.seq;
    if (vehicles !== undefined) {
      for (const v of vehicles) {
        const e = this.acquire(v);
        this.pose(e, v.x, v.y, v.angle);
        e.lastK = k;
      }
    }
    this.retire(k);
  }

  // applyPair poses the fractional tick between two id-sorted frames:
  // id-matched vehicles lerp x/y linearly and the heading shortest-path.
  // A vehicle in only one frame (spawn/despawn) renders ONLY on its own
  // frame — mid-stride it is absent (the pop lands on the baked boundary
  // nearest its presence; no fading, no ghost pose, no extrapolation).
  applyPair(
    a: readonly VehicleRecord[] | undefined,
    b: readonly VehicleRecord[] | undefined,
    alpha: number,
  ): void {
    if (a === undefined || alpha >= 1) {
      this.apply(b);
      return;
    }
    if (b === undefined || alpha <= 0) {
      this.apply(a);
      return;
    }
    const k = ++this.seq;
    walkPairs(a, b, (va, vb) => {
      const e = this.acquire(va);
      this.pose(
        e,
        va.x + (vb.x - va.x) * alpha,
        va.y + (vb.y - va.y) * alpha,
        lerpAngle(va.angle, vb.angle, alpha),
      );
      e.lastK = k;
    });
    this.retire(k);
  }

  get activeCount(): number {
    return this.active.size;
  }
}

// --- page ---------------------------------------------------------------------

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (e === null) throw new Error(`hero.html: #${id} missing`);
  return e as T;
}

function fmtClock(tick: number, dt: number): string {
  const t = Math.round(tick * dt);
  const mm = Math.floor(t / 60);
  const ss = t % 60;
  return `${mm}:${String(ss).padStart(2, "0")}`;
}

async function main(): Promise<void> {
  const params = new URLSearchParams(location.search);
  const statusEl = el("status");
  const loadingEl = el("loading");
  const loadingMsg = el("loading-msg");
  const setLoading = (s: string): void => {
    loadingMsg.textContent = s;
  };

  // Resolve the palette ONCE (?theme=navy|paper, navy default; theme.ts is
  // pure data): the scene palette below and the HUD chrome's --t-* custom
  // properties both derive from this one ThemeSpec. View-only — the baked
  // data paths never see it. Set before loadAll so even the loading
  // overlay is themed.
  const theme = getTheme(params.get("theme") ?? "navy");
  const paper = theme === THEMES.paper;
  const pal = heroPalette(theme, paper);
  for (const [k, v] of Object.entries(heroChromeVars(theme, paper))) {
    document.documentElement.style.setProperty(k, v);
  }

  const data = await loadAll(params, setLoading);
  const { index, frames, heads, fleet } = data;
  const frameCount = bakedFrameCount(index);

  // Renderer / scene / camera.
  const canvas = el<HTMLCanvasElement>("view");
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
  renderer.setClearColor(new THREE.Color(pal.bg));
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  const scene = new THREE.Scene();
  scene.fog = new THREE.Fog(new THREE.Color(pal.bg), 1, 2); // re-ranged below
  const camera = new THREE.PerspectiveCamera(55, 2, 0.5, 100);

  const bounds = buildLanes(scene, data.lanes, data.table, pal);
  const cx = (bounds.minX + bounds.maxX) / 2;
  const cy = (bounds.minY + bounds.maxY) / 2;
  const span = Math.max(bounds.maxX - bounds.minX, bounds.maxY - bounds.minY, 100);

  const ground = new THREE.Mesh(
    new THREE.PlaneGeometry(span * 6, span * 6),
    new THREE.MeshStandardMaterial({ color: pal.bg, roughness: 1 }),
  );
  ground.rotation.x = -Math.PI / 2;
  ground.position.set(cx, -0.5, -cy); // 0.5 m under the lanes: depth-buffer separation
  scene.add(ground);

  scene.add(new THREE.HemisphereLight(new THREE.Color(pal.hemiSky), new THREE.Color(pal.hemiGround), 1.35));
  const sun = new THREE.DirectionalLight(new THREE.Color("#ffffff"), 1.9);
  sun.position.set(cx + span * 0.25, span * 0.5, -cy + span * 0.15);
  scene.add(sun);

  scene.fog = new THREE.Fog(new THREE.Color(pal.bg), span * 1.6, span * 4.5);
  camera.far = span * 5;
  camera.updateProjectionMatrix();

  // Camera presets (keys 1/2/3, ?view=pod|arterial|junction): the same
  // south-and-slightly-east 3/4 look at three scales. Junction positions
  // come from the signal heads' program grouping — pods without signals
  // still get the whole-pod framing.
  const hfovHalf = Math.atan(Math.tan((camera.fov * Math.PI) / 360) * (window.innerWidth / window.innerHeight));
  const fitDist = (s: number): number => ((s / 2) * 1.15) / Math.tan(hfovHalf);
  const VIEW_DIR = new THREE.Vector3(0.18, 0.55, 0.82).normalize();
  const jxs: number[] = [];
  {
    const byJ = new Map<string, { sx: number; n: number }>();
    for (const h of heads) {
      const j = byJ.get(h.program.junction) ?? { sx: 0, n: 0 };
      j.sx += h.x;
      j.n += 1;
      byJ.set(h.program.junction, j);
    }
    for (const k2 of [...byJ.keys()].sort()) jxs.push(byJ.get(k2)!.sx / byJ.get(k2)!.n);
  }
  const jy = heads.length > 0 ? heads.reduce((a, h) => a + h.y, 0) / heads.length : cy;
  const views: Record<string, { tx: number; ty: number; dist: number }> = {
    pod: { tx: cx, ty: cy, dist: fitDist(span) },
  };
  if (jxs.length >= 2) {
    const midIdx = Math.floor((jxs.length - 1) / 2);
    const nextIdx = Math.min(jxs.length - 1, midIdx + 1);
    const gap = jxs[nextIdx]! - jxs[midIdx]!;
    views["arterial"] = {
      tx: (jxs[midIdx]! + jxs[nextIdx]!) / 2,
      ty: jy,
      dist: Math.max(fitDist(gap * 2.3), 260),
    };
    views["junction"] = { tx: jxs[midIdx]!, ty: jy, dist: 330 };
  } else if (jxs.length === 1) {
    views["junction"] = { tx: jxs[0]!, ty: jy, dist: 330 };
    views["arterial"] = views["junction"];
  }
  const controls = new OrbitControls(camera, canvas);
  const applyView = (name: string): void => {
    const v = views[name] ?? views["pod"]!;
    controls.target.set(v.tx, 0, -v.ty);
    camera.position.copy(controls.target).addScaledVector(VIEW_DIR, v.dist);
    controls.update();
  };
  controls.enableDamping = true;
  controls.dampingFactor = 0.08;
  controls.maxPolarAngle = Math.PI * 0.495;
  controls.minDistance = 15;
  controls.maxDistance = span * 4;
  applyView(params.get("view") ?? "pod");
  window.addEventListener("keydown", (ev) => {
    // Form fields own their keys: no hotkeys while typing in an input
    // (the drawer's name field takes "g" and spaces literally).
    const t = ev.target;
    if (t instanceof HTMLInputElement || t instanceof HTMLSelectElement || t instanceof HTMLTextAreaElement) return;
    if (ev.key === "1") applyView("pod");
    else if (ev.key === "2") applyView("arterial");
    else if (ev.key === "3") applyView("junction");
    else if (ev.key === "g") drawer.toggle();
    else if (ev.key === "Escape") panel.deselect(); // the drawer stays open on Esc
    else if (ev.key === " ") {
      ev.preventDefault();
      playBtn.click();
    }
  });

  sigMaterials(pal);
  const rigs = buildSignals(scene, heads, pal);

  // --- junction timing panel (click a signal head → retime that junction) ---
  // and the full-screen game drawer ("Beat the Signal": the sim IS the game,
  // hero/gamedrawer.ts). The panel's Apply writes into the shared design
  // state; the drawer reflects it and runs the full four-junction dial.
  //
  // Raycast on pointer events ONLY, never in the rAF loop. A click is a
  // pointerdown/up pair within 5 px so OrbitControls drags never select;
  // hover just flips the cursor. Hits resolve to a junction via the
  // userData tag buildSignals set on each head group.
  const design = new DesignState();
  // drawer/panel reference each other through these closures (invoked from
  // user events only, after main() has finished wiring).
  const drawer = new GameDrawer({
    design,
    onEditJunction: (j) => panel.select(j),
    // the board row's slug is <slug>; ?run= may pin <slug>/<hash12>
    currentRun: runSlugOf(params.get("run") ?? ""),
  });
  const panel = new TimingPanel({
    table: data.table,
    dt: index.dt,
    prefill: (j) => design.get(j),
    getName: () => design.name,
    onApply: (j, greens, offset) => {
      design.set(j, greens, offset);
      drawer.refreshDesign();
      drawer.open();
      drawer.flashRow(j);
    },
  });

  // --- run-service probe (view-only mode) --------------------------------------
  // GET /api/timing/run is the timing-runner's health probe (serve-baked.py
  // --timing-runner answers 200 + the JSON sentinel); the verdict is the
  // sentinel, not the status — static hosts can 200 an HTML fallback for
  // unmatched paths (Pages without a custom 404.html). One verdict, shared
  // by the drawer and the panel's ghost Run. ?runner=0|1 forces it and
  // skips the probe (capture hooks). Fire-and-forget: first render never
  // waits on the probe.
  const applyRunnerVerdict = (live: boolean): void => {
    drawer.setRunnerAvailable(live);
    panel.setRunnerAvailable(live);
  };
  const runnerParam = params.get("runner");
  if (runnerParam === "0" || runnerParam === "1") {
    applyRunnerVerdict(runnerParam === "1");
  } else {
    fetch("/api/timing/run", { cache: "no-cache" })
      .then((resp) => (resp.ok ? resp.json().catch(() => null) : null))
      .then((body: unknown) => applyRunnerVerdict(runnerAvailable(body)))
      .catch(() => applyRunnerVerdict(runnerAvailable(null)));
  }
  const pickTargets = rigs.map((r) => r.pick);
  const raycaster = new THREE.Raycaster();
  const ndc = new THREE.Vector2();
  const junctionAt = (ev: PointerEvent): string | null => {
    const rect = canvas.getBoundingClientRect();
    ndc.set(((ev.clientX - rect.left) / rect.width) * 2 - 1, -((ev.clientY - rect.top) / rect.height) * 2 + 1);
    raycaster.setFromCamera(ndc, camera);
    for (const hit of raycaster.intersectObjects(pickTargets, true)) {
      let o: THREE.Object3D | null = hit.object;
      while (o !== null) {
        const j: unknown = o.userData["junction"];
        if (typeof j === "string") return j;
        o = o.parent;
      }
    }
    return null;
  };
  let downX = 0;
  let downY = 0;
  canvas.addEventListener("pointerdown", (ev) => {
    if (ev.button === 0) {
      downX = ev.clientX;
      downY = ev.clientY;
    }
  });
  canvas.addEventListener("pointerup", (ev) => {
    if (ev.button !== 0) return;
    if (Math.hypot(ev.clientX - downX, ev.clientY - downY) > 5) return; // orbit drag, not a click
    const j = junctionAt(ev);
    if (j !== null) panel.select(j);
    else panel.deselect(); // empty space deselects
  });
  canvas.addEventListener("pointermove", (ev) => {
    canvas.style.cursor = ev.buttons === 0 && junctionAt(ev) !== null ? "pointer" : "";
  });
  canvas.addEventListener("pointerleave", () => {
    canvas.style.cursor = "";
  });

  // Precompute every head's color at every baked frame (ticks are the only
  // input) — the tick loop then indexes a Uint8Array and allocates nothing.
  const sigFrames = new Uint8Array(rigs.length * frameCount);
  for (let k = 0; k < frameCount; k++) {
    const tick = bakedTickAt(index, k);
    for (let h = 0; h < heads.length; h++) {
      const color = sigColorOf(stateCharAt(heads[h]!.program, tick, heads[h]!.linkIdx));
      sigFrames[h * frameCount + k] = SIG_COLORS.indexOf(color);
    }
  }

  const pool = new VehiclePool(scene, fleet, pal.tints);

  // --- playback --------------------------------------------------------------
  const playBtn = el<HTMLButtonElement>("play");
  const scrub = el<HTMLInputElement>("scrub");
  const speedSel = el<HTMLSelectElement>("speed");
  const readout = el("readout");
  const fleetNote = el("fleetnote");
  // The scrubber is TICK-space and fractional: a drag lands mid-stride
  // and the paused pose renders interpolated.
  scrub.min = String(index.tickStart);
  scrub.max = String(index.tickEnd);
  scrub.step = "any";
  const speedParam = Number(params.get("speed"));
  if (Number.isFinite(speedParam) && speedParam > 0) speedSel.value = String(speedParam);

  let posT = index.tickStart; // float ticks — playback is continuous
  {
    const t = Number(params.get("t"));
    if (Number.isFinite(t) && params.get("t") !== null && params.get("t") !== "") {
      posT = Math.min(index.tickEnd, Math.max(index.tickStart, t));
    }
  }
  let playing = true;
  let scrubbing = false;
  let speed = Number(speedSel.value) || 1;
  let appliedK = -1; // floor frame behind the current signal/readout state
  const bracket: Bracket = { k0: 0, tick0: 0, tick1: 0, alpha: 0 }; // reused every render

  // render poses the world at a fractional tick: vehicles lerp between
  // the bracketing baked frames, while signals and the readout hold the
  // floor frame's state (discrete — the most recent baked frame ≤ t).
  const render = (t: number): void => {
    bracketAt(index, frameCount, t, bracket);
    pool.applyPair(frames.get(bracket.tick0), frames.get(bracket.tick1), bracket.alpha);
    if (bracket.k0 !== appliedK) {
      appliedK = bracket.k0;
      for (let h = 0; h < rigs.length; h++) {
        const rig = rigs[h]!;
        const ci = sigFrames[h * frameCount + bracket.k0]!;
        if (ci !== rig.colorIdx) {
          rig.colorIdx = ci;
          // light the active lens bright, dim the other two (off = all dim)
          const active = SIG_COLORS[ci]!;
          for (let l = 0; l < 3; l++) {
            rig.lenses[l]!.material = active === LENS_ORDER[l] ? LENS_ON[l]! : LENS_DIM[l]!;
          }
        }
      }
      const shown = Math.floor(t);
      readout.textContent = `tick ${shown} / ${index.tickEnd} · t+${fmtClock(shown, index.dt)} · ${pool.activeCount} vehicles`;
    }
    panel.setTick(bracket.tick0); // cheap no-op while closed / same baked frame
    if (!scrubbing) scrub.value = String(t);
  };

  const setPlaying = (p: boolean): void => {
    playing = p;
    playBtn.textContent = p ? "⏸ pause" : "▶ play";
    playBtn.classList.toggle("on", p);
  };
  playBtn.addEventListener("click", () => {
    if (!playing && posT >= index.tickEnd) {
      posT = index.tickStart;
      render(posT);
    }
    setPlaying(!playing);
  });
  scrub.addEventListener("input", () => {
    scrubbing = true;
    posT = Number(scrub.value);
    render(posT);
  });
  scrub.addEventListener("change", () => {
    scrubbing = false;
  });
  speedSel.addEventListener("change", () => {
    speed = Number(speedSel.value) || 1;
  });

  const resize = (): void => {
    const w = window.innerWidth;
    const h = window.innerHeight;
    renderer.setSize(w, h, false);
    camera.aspect = w / h;
    camera.updateProjectionMatrix();
  };
  window.addEventListener("resize", resize);
  resize();

  let lastWall = performance.now();
  renderer.setAnimationLoop(() => {
    const now = performance.now();
    const dtWall = (now - lastWall) / 1000;
    lastWall = now;
    if (playing) {
      // 1 sim second = 1/dt ticks; speed 1 replays real time. Clamp at
      // the last baked tick — never extrapolate past it.
      posT = Math.min(index.tickEnd, posT + (dtWall * speed) / index.dt);
      if (posT >= index.tickEnd) setPlaying(false);
    }
    render(posT);
    controls.update();
    renderer.render(scene, camera);
  });

  render(posT);
  setPlaying(!(params.get("pause") === "1" || Number(params.get("speed")) === 0));
  loadingEl.style.display = "none";

  const hash12 = data.indexUrl.split("/").slice(-2, -1)[0] ?? "";
  const fleetBits =
    fleet.errors.length === 0
      ? `${fleet.models.length} models`
      : `${fleet.models.length} models, ${fleet.errors.length} fleet errors (boxes in use)`;
  fleetNote.textContent = fleet.errors.length === 0 ? "" : `fleet: ${fleet.errors.join(" · ")}`;
  statusEl.textContent =
    `hero: ${index.run} @ ${hash12} · ${data.lanes.length} lanes · ${heads.length} signal heads · ` +
    `${frameCount} frames (${fleetBits}) · tick ${Math.floor(posT)}`;
  if (params.get("bare") === "1") document.body.classList.add("bare");
  // Capture/debug hooks: ?panel=J2 opens the timing panel pre-selected,
  // ?game=1 opens the game drawer.
  const panelParam = params.get("panel");
  if (panelParam !== null && panelParam !== "") panel.select(panelParam);
  if (params.get("game") === "1") drawer.open();
}

main().catch((e: unknown) => {
  const msg = e instanceof Error ? e.message : String(e);
  const loadingMsg = document.getElementById("loading-msg");
  if (loadingMsg !== null) {
    loadingMsg.textContent = `hero failed: ${msg}`;
    loadingMsg.classList.add("err");
  }
  const statusEl = document.getElementById("status");
  if (statusEl !== null) statusEl.textContent = `hero failed: ${msg}`;
  console.error(e);
});
