// signals.ts — render glue between the TSSG program table (tssg.ts) and
// the map: resolve each signal-bound movement to a SIGNAL HEAD point from
// the STATIC network GeoJSON (stop-line entry = first polyline point of
// each bound internal lane; no second geometry channel, ADR-0006 M9
// addendum), and derive per-head light colors at any tick (the derivation
// inputs are the table; the tick rides every TSSF snapshot header).
//
// One head per (program, state-column, approach) cluster. The wire's link
// index is a SUMO connection index — one per from-lane→to-lane movement —
// so a wide multi-lane approach binds several links that change together
// forever (identical state char in every phase). Those collapse to ONE
// head. The column alone is NOT enough: opposing through movements at a
// symmetric fixed-time junction share a column too, and merging them
// would centroid the head into the middle of the intersection — so a
// cluster additionally requires spatial and directional agreement (close
// to the cluster centroid, entry bearing within 90°). Manhattan 3256 →
// 1100 heads, Wilshire 3038 → 824, stress-DTLA 6025 → 1625.
//
// Placement: the head sits at the centroid of its cluster's stop-line
// entries, set back HEAD_SETBACK_M out of the junction box along the mean
// approach bearing (the reverse of the internal lanes' entry direction),
// so heads read as standing over their own approach instead of floating
// inside the intersection.

import { sigColorOf, stateCharAt, type SigColor, type SigProgram, type SignalTable } from "./tssg.ts";

export interface SignalHead {
  id: string; // `${programId}:${repLinkIdx}` (+ `#N` if the rep repeats) — unique feature id AND feature-state key
  x: number; // local metric frame, centroid of the cluster's stop-line entries, set back
  y: number;
  program: SigProgram; // derivation input for headStatesAtTick
  linkIdx: number; // representative link of the cluster (all members derive identically)
}

// HEAD_SETBACK_M pulls the head out of the junction box toward its
// approach — about half a car length, enough that the housing stops
// overlapping the interior lane geometry at close zoom.
export const HEAD_SETBACK_M = 3.5;

// CLUSTER_RADIUS_M bounds how far a lane's stop-line entry may sit from a
// cluster's running centroid and still join it. Well past any single
// approach's width (a 5-lane stop line spans ~20 m), well under the
// distance between distinct junctions sharing a program.
const CLUSTER_RADIUS_M = 75;

// columnKey is the light-state signature: the link's state char in every
// phase. Absent chars (link index past the state string) join as NUL so
// ("G", absent) never collides with (absent, "G").
function columnKey(p: SigProgram, linkIdx: number): string {
  return p.phases.map((ph) => ph.state[linkIdx] ?? "\0").join("\0");
}

// entryBearing is the unit vector the internal lane leaves its stop line
// on (first nonzero segment); null when the shape is too degenerate to
// tell — the lane then joins on distance alone rather than guessing a
// direction.
function entryBearing(shape: ReadonlyArray<readonly number[]>): [number, number] | null {
  const first = shape[0];
  if (first === undefined) return null;
  for (let i = 1; i < shape.length; i++) {
    const pt = shape[i]!;
    const dx = pt[0]! - first[0]!;
    const dy = pt[1]! - first[1]!;
    const len = Math.hypot(dx, dy);
    if (len > 1e-6) return [dx / len, dy / len];
  }
  return null;
}

interface Cluster {
  key: string;
  rep: number; // first link index seen in the cluster (head id + derivation)
  sx: number; // stop-line entry sums (centroid = s*/n)
  sy: number;
  bx: number; // entry-bearing sums (mean direction = normalize(b*))
  by: number;
  n: number;
}

// sameApproach: a lane joins a cluster only when it sits near the running
// centroid AND its entry bearing agrees with the cluster's mean (dot > 0
// ⇔ within 90°). A directionless lane or a not-yet-directed cluster
// cannot veto on bearing — distance still gates.
function sameApproach(c: Cluster, x: number, y: number, b: [number, number] | null): boolean {
  if (Math.hypot(x - c.sx / c.n, y - c.sy / c.n) > CLUSTER_RADIUS_M) return false;
  if (b === null) return true;
  const bl = Math.hypot(c.bx, c.by);
  if (bl <= 1e-6) return true;
  return (c.bx / bl) * b[0] + (c.by / bl) * b[1] > 0;
}

// signalHeads resolves the table's bound lanes against the static network
// geometry, clustered to one head per synchronized approach. A lane
// missing from the GeoJSON (table/static data mismatch) is skipped; a
// cluster whose lanes are ALL missing produces no head — the render path
// never throws on data shape.
export function signalHeads(
  table: SignalTable,
  shapesByLane: ReadonlyMap<string, ReadonlyArray<readonly number[]>>,
): SignalHead[] {
  const out: SignalHead[] = [];
  for (const p of table.programs) {
    // Greedy first-seen clustering: deterministic (the wire's link order),
    // no approach metadata needed — the geometry is the approach identity.
    const clusters: Cluster[] = [];
    for (const l of p.links) {
      const shape = shapesByLane.get(l.laneId);
      if (shape === undefined) continue;
      const first = shape[0];
      if (first === undefined || first[0] === undefined || first[1] === undefined) continue;
      const key = columnKey(p, l.linkIdx);
      const b = entryBearing(shape);
      let c = clusters.find((c) => c.key === key && sameApproach(c, first[0]!, first[1]!, b));
      if (c === undefined) {
        c = { key, rep: l.linkIdx, sx: 0, sy: 0, bx: 0, by: 0, n: 0 };
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
    // Ids are unique per program by construction: the common case is one
    // cluster per rep link, but same-link lanes CAN split (distance or
    // opposing bearings) — the second cluster with a taken rep gets a
    // cluster-ordinal suffix (feature-state keys must not collide).
    const taken = new Set<string>();
    for (const [i, c] of clusters.entries()) {
      const base = `${p.id}:${c.rep}`;
      const id = taken.has(base) ? `${base}#${i}` : base;
      taken.add(id);
      const bl = Math.hypot(c.bx, c.by);
      const back = bl > 1e-6 ? HEAD_SETBACK_M : 0;
      out.push({
        id,
        x: c.sx / c.n - (c.bx / (bl || 1)) * back,
        y: c.sy / c.n - (c.by / (bl || 1)) * back,
        program: p,
        linkIdx: c.rep,
      });
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
