// theme.ts — single source of truth for the viz palette and the per-class
// vehicle glyph table. Every color on the map (background, casing,
// congestion ramp, signals, vehicles) and the legend panel reads from here
// so a visual tweak is a one-line change, and so the legend swatches can
// never drift from what the map actually paints. Pure data + pure math:
// this module must stay DOM-free so node --test can import it.

export const THEME = {
  bg: "#0e1d5c", // math-900 navy canvas
  casing: "#122881", // math-800 lane casing
  noData: "#7e9dff", // math-300: lane with no congestion sample
  stopped: "#e5484d", // congestion ramp: mean speed / limit → 0
  mid: "#e8b43a", // congestion ramp: ~0.35
  freeFlow: "#1e9e6a", // congestion ramp: ≥ 0.7
  signalGreen: "#2ecc71",
  signalAmber: "#f5b301",
  signalRed: "#e5484d",
  // Static WGS84 overlays (main.ts, overlays.ts): admin boundary lines and
  // zone partition — deliberately quieter than the congestion channel, the
  // traffic must stay the loudest thing on the map.
  boundary: "#5c6ba8", // muted slate: admin boundary line + label
  district: "#5f8dff", // zone district outline/fill
  corridor: "#f5b301", // zone corridor outline (amber — distinct from districts)
} as const;

export interface GlyphSpec {
  cls: number; // TSSF v1 vehicle class id
  name: string;
  color: string;
  lengthM: number;
  widthM: number;
}

// Per-class glyph table. Dimensions mirror the engine's authoritative
// VehicleType geometry (engine/vehicle.go: Car 5×2 m, Truck 12×2.5 m); the
// glyph scale between classes derives from these, so a new class with real
// dimensions slots in without retuning.
export const GLYPHS: GlyphSpec[] = [
  { cls: 0, name: "car", color: "#eaf0ff", lengthM: 5, widthM: 2 },
  { cls: 1, name: "truck", color: "#ff7d4d", lengthM: 12, widthM: 2.5 },
];

// Articulated-truck split (viz-only; artic.ts infers the trailer from the
// single-track trailer equation — the engine stays a rigid 12 m body).
// Widths stay the truck's 2.5 m.
export const TRACTOR_M = 4;
export const TRAILER_M = 8;

// glyphByCls looks a class up BY CLS (never positionally — the table
// carries cls precisely so order is not the contract).
export function glyphByCls(cls: number): GlyphSpec {
  const g = GLYPHS.find((g) => g.cls === cls);
  if (!g) throw new Error(`theme: no glyph for cls ${cls}`);
  return g;
}

// vehicleBearingDeg converts the wire heading (radians, CCW from east —
// the math convention of the engine's local metric frame, UTM north-up per
// proj.ts) to MapLibre's icon-rotate (degrees, CW from north). The style
// layer evaluates the same conversion as a MapLibre expression per feature
// (expressions can't call back into TS); this function is the tested spec
// for that math.
export function vehicleBearingDeg(angleRad: number): number {
  const deg = (90 - (angleRad * 180) / Math.PI) % 360;
  return deg < 0 ? deg + 360 : deg;
}
