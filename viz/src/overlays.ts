// overlays.ts — pure, DOM-free helpers for the static WGS84 overlays
// (zones + administrative boundaries, served by demosrv under /overlay/).
// Unlike the network channel these arrive ALREADY in lon/lat, so there is
// no projection here — only doc validation and the per-feature style
// classification main.ts stamps onto the zone source (MapLibre's
// line-dasharray is NOT data-driven, so kind/status styling is resolved
// into plain properties at load instead of expressions at render).
// Kept DOM-free so node --test can import it (theme.ts precedent).

import type { Feature, FeatureCollection } from "geojson";

export type ZoneKind = "district" | "corridor";

// parseOverlay validates a fetched overlay doc: it must be a
// FeatureCollection with a features array; features without a geometry are
// dropped. Anything else returns null — a missing or garbage overlay
// simply doesn't render (overlays are optional).
export function parseOverlay(json: unknown): FeatureCollection | null {
  if (typeof json !== "object" || json === null) return null;
  const fc = json as { type?: unknown; features?: unknown };
  if (fc.type !== "FeatureCollection" || !Array.isArray(fc.features)) return null;
  return {
    type: "FeatureCollection",
    features: (fc.features as Feature[]).filter(
      (f) => typeof f === "object" && f !== null && f.type === "Feature" && f.geometry != null,
    ),
  };
}

// zoneKind classifies a zone feature's `kind` property; anything that is
// not an explicit "corridor" renders as a district (the fill + prominent
// outline), so an unknown kind never silently disappears.
export function zoneKind(props: Record<string, unknown> | null | undefined): ZoneKind {
  return props?.["kind"] === "corridor" ? "corridor" : "district";
}

// zoneRunnable reads the zone's `status` ("runnable" = solid/prominent,
// anything else — notably "import-pending" — = muted).
export function zoneRunnable(props: Record<string, unknown> | null | undefined): boolean {
  return props?.["status"] === "runnable";
}

// prepareZones stamps the style classification onto every zone feature as
// plain properties (zkind, zrun) for the style layers; the label text
// falls back from `label` to `name` so a zone without a display label
// still gets one.
export function prepareZones(fc: FeatureCollection): FeatureCollection {
  return {
    type: "FeatureCollection",
    features: fc.features.map((f) => {
      const props = (f.properties ?? {}) as Record<string, unknown>;
      return {
        ...f,
        properties: {
          ...props,
          zkind: zoneKind(props),
          zrun: zoneRunnable(props) ? 1 : 0,
          label: typeof props["label"] === "string" ? props["label"] : String(props["name"] ?? ""),
        },
      };
    }),
  };
}
