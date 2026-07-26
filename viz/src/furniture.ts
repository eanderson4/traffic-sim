// furniture.ts — the baked furniture.geojson consumer (ADR-0023 §1, §6.2).
// With PMTiles the browser no longer holds full lane geometry, so the
// live signal-head/stop-sign derivations (signals.ts, stopsign.ts) are
// pre-run at bake time and shipped as one GeoJSON doc in METRIC
// coordinates (the shim/main projects them like everything else):
//
//   head: Point      properties { kind: "head", id, program, link }
//   bar:  LineString properties { kind: "bar",  id }   (shares its head's id)
//   sign: Point      properties { kind: "sign", id }
//
// head.program / head.link bind the head to a TSSG program id and its
// representative link index, so furnitureHeads joins heads→programs from
// the table exactly as signalHeads does from geometry today (light state
// derivation, signals.ts headStatesAtTick, is unchanged). Unknown or
// malformed features are skipped — the render path never throws on data
// shape (signals.ts's rule).

import type { Feature, FeatureCollection, LineString, Point } from "geojson";
import type { SignalHead } from "./signals.ts";
import type { SignalTable } from "./tssg.ts";
import type { StopSign } from "./stopsign.ts";

export interface FurnitureHead {
  id: string; // feature id AND feature-state key (signals.ts id scheme)
  programId: string; // TSSG program binding
  linkIdx: number; // representative link of the head's cluster
  x: number; // local metric frame
  y: number;
}

export interface BakedFurniture {
  heads: FurnitureHead[];
  bars: Map<string, readonly [number, number, number, number]>; // head id → bar endpoints
  signs: StopSign[];
}

function pointXY(f: Feature): [number, number] | null {
  if (f.geometry.type !== "Point") return null;
  const c = (f.geometry as Point).coordinates;
  if (typeof c[0] !== "number" || typeof c[1] !== "number") return null;
  return [c[0], c[1]];
}

// parseFurniture sorts the doc's features into heads, stop bars, and stop
// signs, keeping the metric coordinates untouched.
export function parseFurniture(fc: FeatureCollection): BakedFurniture {
  const heads: FurnitureHead[] = [];
  const bars = new Map<string, readonly [number, number, number, number]>();
  const signs: StopSign[] = [];
  for (const f of fc.features ?? []) {
    const p = f.properties ?? {};
    const kind = p["kind"];
    const id = typeof p["id"] === "string" ? p["id"] : null;
    if (id === null) continue;
    if (kind === "head") {
      const programId = p["program"];
      const linkIdx = Number(p["link"]);
      const xy = pointXY(f);
      if (typeof programId !== "string" || !Number.isInteger(linkIdx) || xy === null) continue;
      heads.push({ id, programId, linkIdx, x: xy[0], y: xy[1] });
    } else if (kind === "bar") {
      if (f.geometry.type !== "LineString") continue;
      const c = (f.geometry as LineString).coordinates;
      const a = c[0];
      const b = c[c.length - 1];
      if (a === undefined || b === undefined) continue;
      if (typeof a[0] !== "number" || typeof a[1] !== "number") continue;
      if (typeof b[0] !== "number" || typeof b[1] !== "number") continue;
      bars.set(id, [a[0], a[1], b[0], b[1]]);
    } else if (kind === "sign") {
      const xy = pointXY(f);
      if (xy === null) continue;
      signs.push({ id, x: xy[0], y: xy[1] });
    }
  }
  return { heads, bars, signs };
}

// furnitureHeads joins baked heads to the TSSG table's programs by the
// baked binding — the SignalHead[] the render path consumes, identical in
// shape to signalHeads' output (a head whose program is missing from the
// table is skipped, mirroring the live path's missing-geometry skip).
export function furnitureHeads(f: BakedFurniture, table: SignalTable): SignalHead[] {
  const byId = new Map(table.programs.map((p) => [p.id, p]));
  const out: SignalHead[] = [];
  for (const h of f.heads) {
    const program = byId.get(h.programId);
    if (program === undefined) continue;
    out.push({ id: h.id, x: h.x, y: h.y, bar: f.bars.get(h.id) ?? null, program, linkIdx: h.linkIdx });
  }
  return out;
}
