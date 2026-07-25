// stopsign.ts — STOP SIGNS for stop-controlled approaches (ADR-0010
// right-of-way): the network GeoJSON marks internal lanes with
// properties.row === "stop" (engine/geojson.go), and this module clusters
// them to ONE sign per approach and draws the octagon sprite. Static
// geometry only — unlike the signal heads there is no program table, no
// feature-state, no per-tick update; the layer is built once at map load.
//
// Clustering mirrors the signal heads (signals.ts): group by junction,
// then split within the junction by ENTRY bearing (first polyline
// segment's direction — signals.ts:entryBearing) with the same ~45° cone
// against the cluster's running mean, so a multi-lane approach merges to
// one sign while opposing arms of the same junction stay two. No column
// key and a 75 m centroid distance gate (sameCone) here: within one junction the bearing alone is
// the approach identity. Placement is the head pattern too — centroid of
// the cluster's stop-line entries, set back HEAD_SETBACK_M out of the
// junction box along the reverse mean bearing. Degenerate shapes are
// skipped, never thrown on.
//
// The sprite is theme-colored (theme.stopFace/stopRim — semantic red in
// both themes, like the signal lenses). Everything except stopSignImage
// is DOM-free so node --test can pin the placement.

import { entryBearing, HEAD_SETBACK_M } from "./signals.ts";
import { THEMES, type ThemeSpec } from "./theme.ts";

export const STOP_SIGN_IMAGE_ID = "stop-sign";

export interface StopSignLane {
  id: string;
  row: string; // GeoJSON property; only "stop" lanes produce signs
  junction: string; // GeoJSON property; cluster group key
  shape: ReadonlyArray<readonly number[]>; // local metric frame
}

export interface StopSign {
  id: string; // `${junction}#${ordinal}` (or `lane:${laneId}#0` ungrouped)
  x: number; // local metric frame, centroid set back along reverse bearing
  y: number;
}

interface StopCluster {
  sx: number; // stop-line entry sums (centroid = s*/n)
  sy: number;
  bx: number; // entry-bearing sums (mean direction = normalize(b*))
  by: number;
  n: number;
}

// CLUSTER_RADIUS_M bounds how far a lane's stop-line entry may sit from a
// cluster's running centroid and still join it (the signal-head gate,
// signals.ts): parallel same-direction approaches of one big junction
// (frontage roads, divided carriageways) must NOT collapse into one sign
// floating between roads. Matches the head clustering constant.
const CLUSTER_RADIUS_M = 75;

// sameCone: a lane joins a cluster when it sits near the running centroid
// AND its entry bearing agrees with the cluster's running mean within
// ~45° (the signal-head cone, signals.ts sameApproach — lanes of one true
// approach are near-parallel at the stop line, so 45° splits approaches,
// never lanes). A directionless lane or a not-yet-directed cluster cannot
// veto on bearing — distance still gates.
function sameCone(c: StopCluster, x: number, y: number, b: [number, number] | null): boolean {
  if (Math.hypot(x - c.sx / c.n, y - c.sy / c.n) > CLUSTER_RADIUS_M) return false;
  if (b === null) return true;
  const bl = Math.hypot(c.bx, c.by);
  if (bl <= 1e-6) return true;
  return (c.bx / bl) * b[0] + (c.by / bl) * b[1] > Math.SQRT1_2; // cos 45°
}

// stopSigns resolves the network's stop-controlled internal lanes to one
// sign per approach cluster, in the LOCAL METRIC frame (main.ts projects).
// Lanes without a junction (malformed input — the engine only sets row on
// internal lanes, which always carry one) still render, grouped per lane.
export function stopSigns(lanes: ReadonlyArray<StopSignLane>): StopSign[] {
  // Insertion-ordered group map keeps the output deterministic (file order).
  const groups = new Map<string, StopCluster[]>();
  for (const lane of lanes) {
    if (lane.row !== "stop") continue;
    const first = lane.shape[0];
    if (first === undefined || first[0] === undefined || first[1] === undefined) continue;
    const key = lane.junction !== "" ? lane.junction : `lane:${lane.id}`;
    let clusters = groups.get(key);
    if (clusters === undefined) {
      clusters = [];
      groups.set(key, clusters);
    }
    const b = entryBearing(lane.shape);
    let c = clusters.find((c) => sameCone(c, first[0]!, first[1]!, b));
    if (c === undefined) {
      c = { sx: 0, sy: 0, bx: 0, by: 0, n: 0 };
      clusters.push(c);
    }
    c.sx += first[0];
    c.sy += first[1];
    c.n += 1;
    if (b !== null) {
      c.bx += b[0];
      c.by += b[1];
    }
  }
  const out: StopSign[] = [];
  for (const [key, clusters] of groups) {
    for (const [i, c] of clusters.entries()) {
      const bl = Math.hypot(c.bx, c.by);
      const back = bl > 1e-6 ? HEAD_SETBACK_M : 0; // directionless: sit on the centroid
      out.push({
        id: `${key}#${i}`,
        x: c.sx / c.n - (c.bx / (bl || 1)) * back,
        y: c.sy / c.n - (c.by / (bl || 1)) * back,
      });
    }
  }
  return out;
}

// Sprite geometry (pixels at icon-size 1): a flat-top octagon.
const D = 12; // octagon center-to-vertex
const PAD = 2; // margin so the rim stroke doesn't clip

// stopSignImage draws the sign: red octagon face with a white rim (no
// text — it must read at signal-head sizes). Drawn at devicePixelRatio so
// it stays as crisp as the signal sprites on hiDPI screens.
export function stopSignImage(
  theme: ThemeSpec = THEMES.navy,
): { image: ImageData; pixelRatio: number } {
  const dpr = Math.max(1, Math.min(3, Math.floor(globalThis.devicePixelRatio || 1)));
  const size = (2 * D + 2 * PAD) * dpr;
  const canvas = document.createElement("canvas");
  canvas.width = size;
  canvas.height = size;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("stopsign: 2d canvas context unavailable");
  ctx.scale(dpr, dpr);

  const cx = PAD + D;
  const cy = PAD + D;
  ctx.beginPath();
  for (let k = 0; k < 8; k++) {
    // Vertices at 22.5° + k·45° → flat top and bottom edges.
    const a = Math.PI / 8 + (k * Math.PI) / 4;
    const px = cx + D * Math.cos(a);
    const py = cy + D * Math.sin(a);
    if (k === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
  ctx.fillStyle = theme.stopFace;
  ctx.fill();
  ctx.strokeStyle = theme.stopRim;
  ctx.lineWidth = 1.5;
  ctx.stroke();
  return { image: ctx.getImageData(0, 0, size, size), pixelRatio: dpr };
}
