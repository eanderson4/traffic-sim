// edges.ts — lane-group (edge) boundaries for the network casing. The
// engine's lateral-chaining group (network-format v1: `edge` + `edgeIndex`,
// 0 = rightmost, loader chains only consecutive indices of one group) is
// what makes N lanes "the same road"; drawing the casing only on each
// group's OUTERMOST lanes makes that grouping visible — a multi-lane road
// reads as one cased band with colored interior stripes instead of N
// independent cased lines. Pure data shaping, no MapLibre APIs.

export interface EdgeLaneRef {
  id: string;
  edge?: string; // absent/"" = no lateral group (junction interiors, in-code nets)
  edgeIndex?: number;
}

// edgeBoundaries returns the ids of the lanes that carry the group's outer
// casing: the min- and max-index lane of every edge group. Ungrouped lanes
// (no edge) are always boundaries — they have no siblings to merge with.
export function edgeBoundaries(lanes: ReadonlyArray<EdgeLaneRef>): Set<string> {
  const groups = new Map<string, { min: number; max: number; minId: string; maxId: string }>();
  const out = new Set<string>();
  for (const l of lanes) {
    if (l.edge === undefined || l.edge === "" || l.edgeIndex === undefined) {
      out.add(l.id);
      continue;
    }
    const g = groups.get(l.edge);
    if (g === undefined) {
      groups.set(l.edge, { min: l.edgeIndex, max: l.edgeIndex, minId: l.id, maxId: l.id });
      continue;
    }
    if (l.edgeIndex < g.min) {
      g.min = l.edgeIndex;
      g.minId = l.id;
    }
    if (l.edgeIndex > g.max) {
      g.max = l.edgeIndex;
      g.maxId = l.id;
    }
  }
  for (const g of groups.values()) {
    out.add(g.minId);
    out.add(g.maxId);
  }
  return out;
}
