// vehicles.ts — the vehicle channel (research decision 1c): a dedicated
// small GeoJSON source updated by updateData diffs keyed on vehicle id
// (feature id = vehicle id, promoted via promoteId). Each interpolated rAF
// frame diffs the new render set against the previously applied one:
// spawn → add, move/class-change → update, despawn → remove. The full
// source is NEVER re-setData'd.

import type { RenderVehicle } from "./snapshots.ts";

export type LngLat = [number, number];

export interface RenderedVehicle extends RenderVehicle {
  lngLat: LngLat;
}

// Minimal structural typings for the MapLibre GeoJSONSourceDiff vocabulary —
// structurally compatible with maplibre-gl's GeoJSONSourceDiff, kept local
// so this module (and its tests) never imports the map renderer.
export interface FeatureDiff {
  id: number;
  newGeometry?: { type: "Point"; coordinates: LngLat };
  addOrUpdateProperties?: Array<{ key: string; value: string | number }>;
}

export interface SourceDiff {
  add?: Array<{
    type: "Feature";
    id: number;
    properties: Record<string, string | number>;
    geometry: { type: "Point"; coordinates: LngLat };
  }>;
  update?: FeatureDiff[];
  remove?: number[];
}

function feature(v: RenderedVehicle): NonNullable<SourceDiff["add"]>[number] {
  return {
    type: "Feature",
    id: v.id,
    properties: { id: v.id, cls: v.cls, angle: v.angle, speed: v.speed },
    geometry: { type: "Point", coordinates: v.lngLat },
  };
}

// diffVehicles computes the updateData diff moving the rendered set from
// prev to next. Moved vehicles re-send geometry + the properties the style
// and the click overlay read (angle, speed); cls only changes on type
// change but is cheap enough to carry on every update.
export function diffVehicles(
  prev: ReadonlyMap<number, RenderedVehicle>,
  next: ReadonlyMap<number, RenderedVehicle>,
): SourceDiff {
  const diff: SourceDiff = {};
  const add: NonNullable<SourceDiff["add"]> = [];
  const update: FeatureDiff[] = [];
  const remove: number[] = [];

  for (const v of next.values()) {
    const p = prev.get(v.id);
    if (p === undefined) {
      add.push(feature(v));
    } else if (p.lngLat[0] !== v.lngLat[0] || p.lngLat[1] !== v.lngLat[1] || p.cls !== v.cls) {
      update.push({
        id: v.id,
        newGeometry: { type: "Point", coordinates: v.lngLat },
        addOrUpdateProperties: [
          { key: "cls", value: v.cls },
          { key: "angle", value: v.angle },
          { key: "speed", value: v.speed },
        ],
      });
    }
  }
  for (const id of prev.keys()) {
    if (!next.has(id)) remove.push(id);
  }
  if (add.length > 0) diff.add = add;
  if (update.length > 0) diff.update = update;
  if (remove.length > 0) diff.remove = remove;
  return diff;
}
