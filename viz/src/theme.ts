// theme.ts — single source of truth for the viz palette and the per-class
// vehicle glyph table. Every color on the map (background, casing,
// congestion ramp, signals, vehicles), the HUD chrome (index.html CSS
// variables), the signal-head sprite, and the legend panel reads from here
// so a visual tweak is a one-line change, and so the legend swatches can
// never drift from what the map actually paints. Pure data + pure math:
// this module must stay DOM-free so node --test can import it.
//
// THEMES holds the swappable palettes (?theme= URL param, config.ts):
//   navy  — the original dark canvas (default; the historical look),
//   paper — minimalist light theme.
// THEME stays exported as an alias for THEMES.navy for back-compat.

export interface ThemeSpec {
  bg: string; // map canvas background + vehicle glyph halo
  casing: string; // lane casing
  noData: string; // lane with no congestion sample
  stopped: string; // congestion ramp: mean speed / limit → 0
  mid: string; // congestion ramp: ~0.35
  freeFlow: string; // congestion ramp: ≥ 0.7
  signalGreen: string;
  signalAmber: string;
  signalRed: string;
  // Static WGS84 overlays (main.ts, overlays.ts): admin boundary lines,
  // zone partition, water fill — deliberately quieter than the congestion
  // channel, the traffic must stay the loudest thing on the map.
  boundary: string; // admin boundary line + label
  district: string; // zone district outline/fill
  corridor: string; // zone corridor outline
  water: string; // water fill, UNDER the road lines
  // Stop sign (stopsign.ts): semantic red in every theme, like the lenses.
  stopFace: string;
  stopRim: string;
  // HUD chrome (index.html CSS variables, set by main.ts).
  hudBg: string;
  hudBorder: string;
  hudText: string;
  hudTextDim: string;
  overlayBg: string; // loading overlay
  // Signal-head sprite (signalhead.ts) + legend swatch housing.
  sigHousing: string;
  sigStroke: string;
  sigDim: string; // unlit lens
  // Vehicle glyphs: per-class icon-color (cls → color) + halo.
  glyphHalo: string;
  glyphColors: Record<number, string>;
}

export const THEMES = {
  navy: {
    bg: "#0e1d5c", // math-900 navy canvas
    casing: "#122881", // math-800 lane casing
    noData: "#7e9dff", // math-300: lane with no congestion sample
    stopped: "#e5484d", // congestion ramp: mean speed / limit → 0
    mid: "#e8b43a", // congestion ramp: ~0.35
    freeFlow: "#1e9e6a", // congestion ramp: ≥ 0.7
    signalGreen: "#2ecc71",
    signalAmber: "#f5b301",
    signalRed: "#e5484d",
    boundary: "#5c6ba8", // muted slate: admin boundary line + label
    district: "#5f8dff", // zone district outline/fill
    corridor: "#f5b301", // zone corridor outline (amber — distinct from districts)
    // Water fill (Lake Michigan, rivers) — a shade off the math-900 canvas
    // so the shoreline reads without competing with congestion.
    water: "#122a70",
    stopFace: "#e5484d", // semantic stop red, both themes
    stopRim: "#ffffff",
    hudBg: "rgba(14, 29, 92, 0.82)",
    hudBorder: "#2e5bff",
    hudText: "#d6e1ff",
    hudTextDim: "#9fb2e8",
    overlayBg: "rgba(14, 29, 92, 0.94)",
    sigHousing: "#0a1230",
    sigStroke: "rgba(214, 225, 255, 0.55)",
    sigDim: "#1d2950",
    glyphHalo: "#0e1d5c",
    glyphColors: { 0: "#eaf0ff", 1: "#ff7d4d" },
  },
  paper: {
    bg: "#fafafa",
    casing: "#9a9aa1", // ink-400: roads read as quiet hairlines on paper
    noData: "#c6c6cb", // ink-300: unsampled lanes stay subtle but visible
    stopped: "#ED4B16", // vibes-600: congestion heats up into the MvV marker orange
    mid: "#FF9E78", // vibes-300
    freeFlow: "#c6c6cb", // free lanes recede to quiet paper gray
    signalGreen: "#2ecc71", // signals stay semantic in both themes
    signalAmber: "#f5b301",
    signalRed: "#e5484d",
    boundary: "#b6b6c2", // quiet ink line on paper
    district: "#2563eb", // the paper accent blue
    corridor: "#d97706", // amber, darkened to read on paper
    water: "#d7e6f5", // pale blue-gray fill under the road hairlines
    stopFace: "#e5484d", // semantic stop red, both themes
    stopRim: "#ffffff",
    hudBg: "#ffffff",
    hudBorder: "#e5e5e8",
    hudText: "#26262b",
    hudTextDim: "#6e6e76",
    overlayBg: "rgba(250, 250, 250, 0.94)",
    sigHousing: "#ffffff",
    sigStroke: "rgba(38, 38, 43, 0.55)",
    sigDim: "#e3e3e6",
    glyphHalo: "#fafafa",
    glyphColors: { 0: "#26262b", 1: "#2563eb" }, // truck = single accent
  },
} satisfies Record<string, ThemeSpec>;

// Back-compat alias: the pre-themeable single palette (navy).
export const THEME: ThemeSpec = THEMES.navy;

// getTheme resolves a ?theme= URL param; unknown names fall back to navy.
export function getTheme(name: string): ThemeSpec {
  // Own-property check: "constructor"/"toString" must fall back, not
  // resolve to inherited Object members.
  return Object.hasOwn(THEMES, name) ? THEMES[name as keyof typeof THEMES] : THEMES.navy;
}

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
// dimensions slots in without retuning. Colors here are the navy defaults;
// glyphByCls overrides them from the active theme's glyphColors.
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
// carries cls precisely so order is not the contract). With a theme, the
// glyph's color comes from theme.glyphColors (dims are theme-independent);
// without one it returns the navy default, so existing single-arg callers
// are unaffected.
export function glyphByCls(cls: number, theme: ThemeSpec = THEMES.navy): GlyphSpec {
  const g = GLYPHS.find((g) => g.cls === cls);
  if (!g) throw new Error(`theme: no glyph for cls ${cls}`);
  const color = theme.glyphColors[cls];
  return color === undefined ? g : { ...g, color };
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
