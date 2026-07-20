// signals.ts — render glue between the TSSG program table (tssg.ts) and
// the map: resolve each signal-bound internal lane to its stop-line entry
// from the STATIC network GeoJSON (first polyline point — where vehicles
// enter the box; no second geometry channel, ADR-0006 M9 addendum), and
// derive per-lane light colors at any tick (the derivation inputs are the
// table; the tick rides every TSSF snapshot header).

import { phaseIndexAt, sigColorOf, type SigColor, type SignalTable } from "./tssg.ts";

export interface SignalStopLine {
  laneId: string; // internal lane id = GeoJSON feature id
  x: number; // local metric frame
  y: number;
}

// signalStopLines resolves the table's bound lanes against the static
// network geometry. A lane missing from the GeoJSON (table/static data
// mismatch) is skipped — the render path never throws on data shape.
export function signalStopLines(
  table: SignalTable,
  shapesByLane: ReadonlyMap<string, ReadonlyArray<readonly number[]>>,
): SignalStopLine[] {
  const out: SignalStopLine[] = [];
  for (const p of table.programs) {
    for (const l of p.links) {
      const shape = shapesByLane.get(l.laneId);
      const first = shape?.[0];
      if (first === undefined || first[0] === undefined || first[1] === undefined) continue;
      out.push({ laneId: l.laneId, x: first[0], y: first[1] });
    }
  }
  return out;
}

// laneStatesAtTick derives every bound lane's light color at tick: the
// program's phase in force (kernel-mirrored integer math) → the lane's
// link char → green/amber/red (off = render nothing).
export function laneStatesAtTick(table: SignalTable, tick: number): Map<string, SigColor> {
  const out = new Map<string, SigColor>();
  for (const p of table.programs) {
    const state = p.phases[phaseIndexAt(p, tick)]?.state ?? "";
    for (const l of p.links) {
      const char = l.linkIdx >= 0 && l.linkIdx < state.length ? state[l.linkIdx]! : "";
      out.set(l.laneId, sigColorOf(char));
    }
  }
  return out;
}
