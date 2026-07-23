// signals.ts — render glue between the TSSG program table (tssg.ts) and
// the map: resolve each signal-bound movement to a SIGNAL HEAD point from
// the STATIC network GeoJSON (stop-line entry = first polyline point of
// each bound internal lane; no second geometry channel, ADR-0006 M9
// addendum), and derive per-head light colors at any tick (the derivation
// inputs are the table; the tick rides every TSSF snapshot header).
//
// One head per (program, link index) — every lane bound to the same state
// char shows the same light at every tick by construction, so a single
// head at the centroid of their stop-line entries carries the same
// information without the per-lane clutter (netimport grids bind several
// fragment lanes per movement).

import { sigColorOf, stateCharAt, type SigColor, type SigProgram, type SignalTable } from "./tssg.ts";

export interface SignalHead {
  id: string; // `${programId}:${linkIdx}` — feature id AND feature-state key
  x: number; // local metric frame, centroid of the movement's stop-line entries
  y: number;
  program: SigProgram; // derivation input for headStatesAtTick
  linkIdx: number;
}

// signalHeads resolves the table's bound lanes against the static network
// geometry, grouped to one head per movement. A lane missing from the
// GeoJSON (table/static data mismatch) is skipped; a movement whose lanes
// are ALL missing produces no head — the render path never throws on data
// shape.
export function signalHeads(
  table: SignalTable,
  shapesByLane: ReadonlyMap<string, ReadonlyArray<readonly number[]>>,
): SignalHead[] {
  const out: SignalHead[] = [];
  for (const p of table.programs) {
    // Preserve first-seen link order so heads track the wire's link list.
    const groups = new Map<number, { sx: number; sy: number; n: number }>();
    const order: number[] = [];
    for (const l of p.links) {
      const shape = shapesByLane.get(l.laneId);
      const first = shape?.[0];
      if (first === undefined || first[0] === undefined || first[1] === undefined) continue;
      let g = groups.get(l.linkIdx);
      if (g === undefined) {
        g = { sx: 0, sy: 0, n: 0 };
        groups.set(l.linkIdx, g);
        order.push(l.linkIdx);
      }
      g.sx += first[0];
      g.sy += first[1];
      g.n += 1;
    }
    for (const linkIdx of order) {
      const g = groups.get(linkIdx)!;
      out.push({ id: `${p.id}:${linkIdx}`, x: g.sx / g.n, y: g.sy / g.n, program: p, linkIdx });
    }
  }
  return out;
}

// headStatesAtTick derives every head's light color at tick: the program's
// phase in force (kernel-mirrored integer math) → the movement's link char
// → green/amber/red (off = render the housing with no lit lens).
export function headStatesAtTick(heads: ReadonlyArray<SignalHead>, tick: number): Map<string, SigColor> {
  const out = new Map<string, SigColor>();
  for (const h of heads) {
    out.set(h.id, sigColorOf(stateCharAt(h.program, tick, h.linkIdx)));
  }
  return out;
}
