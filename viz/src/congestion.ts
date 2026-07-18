// congestion.ts — CLIENT-DERIVED congestion proxy (research decision 3):
// per-lane mean speed of the live vehicle set, driven onto the static
// network source via setFeatureState (NEVER setData). This is a viz-side
// estimate, not the engine's congestion metrics: TSSF v1 carries no lane
// id and no speed, so vehicles are re-attached to lanes here by nearest-
// segment lookup (a simple uniform-grid spatial index over lane polylines,
// local metric frame) and speeds come from snapshot displacement
// (snapshots.ts). The future observability ADR (domain-congestion-metrics)
// replaces this with authoritative per-section metrics.

export interface LaneSegment {
  laneId: string;
  ax: number;
  ay: number;
  bx: number;
  by: number;
}

const CELL_M = 100;

function cellKey(cx: number, cy: number): string {
  return `${cx},${cy}`;
}

// LaneIndex is a uniform grid over lane polyline segments, built once from
// the network GeoJSON (local metric coordinates).
export class LaneIndex {
  private grid = new Map<string, LaneSegment[]>();

  constructor(lanes: Array<{ id: string; shape: Array<[number, number]> }>) {
    for (const lane of lanes) {
      for (let i = 0; i + 1 < lane.shape.length; i++) {
        const a = lane.shape[i]!;
        const b = lane.shape[i + 1]!;
        const seg: LaneSegment = { laneId: lane.id, ax: a[0], ay: a[1], bx: b[0], by: b[1] };
        const minX = Math.floor(Math.min(a[0], b[0]) / CELL_M);
        const maxX = Math.floor(Math.max(a[0], b[0]) / CELL_M);
        const minY = Math.floor(Math.min(a[1], b[1]) / CELL_M);
        const maxY = Math.floor(Math.max(a[1], b[1]) / CELL_M);
        for (let cx = minX; cx <= maxX; cx++) {
          for (let cy = minY; cy <= maxY; cy++) {
            const key = cellKey(cx, cy);
            let bucket = this.grid.get(key);
            if (bucket === undefined) {
              bucket = [];
              this.grid.set(key, bucket);
            }
            bucket.push(seg);
          }
        }
      }
    }
  }

  // nearestLane returns the id of the lane with the closest segment within
  // maxDistM of (x, y), or null. Vehicles sit on lane centerlines, so a few
  // metres of threshold suffices.
  nearestLane(x: number, y: number, maxDistM: number): string | null {
    const cx = Math.floor(x / CELL_M);
    const cy = Math.floor(y / CELL_M);
    let bestId: string | null = null;
    let bestD2 = maxDistM * maxDistM;
    const span = Math.ceil(maxDistM / CELL_M);
    for (let ix = cx - span; ix <= cx + span; ix++) {
      for (let iy = cy - span; iy <= cy + span; iy++) {
        const bucket = this.grid.get(cellKey(ix, iy));
        if (bucket === undefined) continue;
        for (const s of bucket) {
          const d2 = pointSegDist2(x, y, s);
          if (d2 < bestD2) {
            bestD2 = d2;
            bestId = s.laneId;
          }
        }
      }
    }
    return bestId;
  }
}

function pointSegDist2(px: number, py: number, s: LaneSegment): number {
  const dx = s.bx - s.ax;
  const dy = s.by - s.ay;
  const len2 = dx * dx + dy * dy;
  let t = 0;
  if (len2 > 0) {
    t = ((px - s.ax) * dx + (py - s.ay) * dy) / len2;
    t = Math.max(0, Math.min(1, t));
  }
  const qx = s.ax + t * dx - px;
  const qy = s.ay + t * dy - py;
  return qx * qx + qy * qy;
}

// laneSpeedRatios aggregates per-vehicle speeds (already lane-attached) into
// mean-speed/speedLimit ratios per lane. ratio 1 = at the limit (free
// flow), 0 = stopped.
export function laneSpeedRatios(
  speedsByLane: ReadonlyMap<string, number[]>,
  speedLimitByLane: ReadonlyMap<string, number>,
): Map<string, number> {
  const ratios = new Map<string, number>();
  for (const [laneId, speeds] of speedsByLane) {
    const limit = speedLimitByLane.get(laneId);
    if (limit === undefined || limit <= 0 || speeds.length === 0) continue;
    let sum = 0;
    for (const s of speeds) sum += s;
    ratios.set(laneId, Math.max(0, Math.min(1.5, sum / speeds.length / limit)));
  }
  return ratios;
}
