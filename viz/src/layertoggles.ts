// layertoggles.ts — pure mapping from legend toggle state to the MapLibre
// operations main.ts applies: which layers hide, and the "vehicles" layer
// class filter. DOM-free and MapLibre-free (the filter is plain data,
// applied by main.ts) so node --test can pin the mapping; legend.ts deals
// only in ToggleKeys, main.ts only in the ops returned here.

export type ToggleKey = "car" | "truck" | "signals" | "stops" | "congestion" | "buildings";

export type ToggleState = Record<ToggleKey, boolean>;

// Default: every channel on (the historical rendering).
export const DEFAULT_TOGGLES: ToggleState = {
  car: true,
  truck: true,
  signals: true,
  stops: true,
  congestion: true,
  buildings: true,
};

export interface LayerOps {
  // setFilter on "vehicles": null = no filter (both classes on); an
  // equality on the surviving class; a never-match (cls -1 doesn't exist)
  // when both are off — the layer itself stays, so toggling a class back
  // on restores it without a re-add.
  vehiclesFilter: unknown[] | null;
  // setLayoutProperty(id, "visibility", on ? "visible" : "none") pairs.
  visibility: Array<[string, boolean]>;
}

// layerOpsFor maps a toggle state to layer operations. The filter is
// BUILT from the state (not swapped between fixed expressions) so the two
// class toggles compose independently. Layer-id notes:
//   - "trailers" follows truck (the trailer is half the articulated
//     glyph, artic.ts);
//   - the five signal layers move as a group: housing + stop bars + the
//     three lit-lens circles (ids mirror main.ts's signals-lens-{color});
//   - "stop-signs" is the static stop-sign layer (stopsign.ts);
//   - "network-line" + "network-internal-line" are the congestion overlay
//     (external lanes / zoom-gated junction interiors, WQ-3);
//     "network-casing" + "network-internal-casing" (the road geometry
//     itself) are NEVER toggled;
//   - "buildings-fill" + "buildings-outline" are the footprint overlay
//     (zoom-gated context UNDER the roads; both move as one group).
export function layerOpsFor(state: ToggleState): LayerOps {
  const cls: number[] = [];
  if (state.car) cls.push(0);
  if (state.truck) cls.push(1);
  const vehiclesFilter =
    cls.length === 2 ? null : ["==", ["get", "cls"], cls.length === 1 ? cls[0]! : -1];
  return {
    vehiclesFilter,
    visibility: [
      ["trailers", state.truck],
      ["signals-bars", state.signals],
      ["signals-housing", state.signals],
      ["signals-lens-red", state.signals],
      ["signals-lens-amber", state.signals],
      ["signals-lens-green", state.signals],
      ["stop-signs", state.stops],
      ["network-line", state.congestion],
      ["network-internal-line", state.congestion],
      ["buildings-fill", state.buildings],
      ["buildings-outline", state.buildings],
    ],
  };
}
