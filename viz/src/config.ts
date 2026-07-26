// config.ts — viz runtime config from the URL query string. `?run=` picks
// the run id (subject ts.{run}.state.snap), `?ws=` the engine's WebSocket
// listener, `?buffer=` the interpolation buffer length in ms, `?dt=` the
// engine timestep in seconds (engine DefaultParams Dt = 0.1) — needed to
// derive vehicle speed from SIM time, not wall-clock frame spans.
// `?theme=` picks the palette (theme.ts THEMES; unknown names → navy).
// `?bake=<index.json URL>` selects the baked-replay shim (ADR-0023): the
// recording replays from static artifacts, `?ws=` is inert, and `?run=`
// is display-only (the shim echoes the index's run id).

export interface VizConfig {
  run: string;
  ws: string;
  bake: string | null; // ?bake= index.json URL — baked mode when set (ADR-0023)
  networkUrl: string;
  zonesUrl: string;
  boundariesUrl: string;
  waterUrl: string;
  buildingsUrl: string;
  bufferMs: number;
  dt: number; // engine timestep, s — sim seconds per tick
  theme: string; // theme.ts THEMES key (navy default, resolved by getTheme)
  bare: boolean; // ?bare=1: hide HUD chrome + loading overlay (clean map shots)
}

// localStorage key the theme toggle (main.ts, and the demos menu's inline
// script) writes; read here as the middle precedence tier.
export const THEME_STORAGE_KEY = "viz-theme";

// resolveThemeName is the theme precedence in one pure place: an explicit
// ?theme= param wins, then the persisted toggle choice, then navy. The
// result is a NAME — theme.ts:getTheme still maps unknown names to navy.
export function resolveThemeName(urlParam: string | null, stored: string | null): string {
  if (urlParam !== null && urlParam !== "") return urlParam;
  if (stored !== null && stored !== "") return stored;
  return "navy";
}

export function loadConfig(search: string, hostname: string): VizConfig {
  const p = new URLSearchParams(search);
  const buffer = Number(p.get("buffer") ?? "250");
  const dt = Number(p.get("dt") ?? "0.1");
  // Guarded so node --test (no Web Storage) can import this module — and
  // so storage-DISABLED browsers (property access throws SecurityError)
  // fall through to the default instead of killing loadConfig.
  let stored: string | null = null;
  try {
    stored = typeof localStorage === "undefined" ? null : localStorage.getItem(THEME_STORAGE_KEY);
  } catch {
    stored = null;
  }
  return {
    run: p.get("run") ?? "demo",
    ws: p.get("ws") ?? `ws://${hostname}:8443`,
    bake: p.get("bake"),
    networkUrl: p.get("net") ?? "/network.geojson",
    // Static WGS84 overlays (demosrv /overlay/); optional — a 404 just
    // means "no overlay on this demo", the fetch tolerates it.
    zonesUrl: p.get("zones") ?? "/overlay/zones.geojson",
    boundariesUrl: p.get("boundaries") ?? "/overlay/boundaries.geojson",
    waterUrl: p.get("water") ?? "/overlay/water.geojson",
    buildingsUrl: p.get("buildings") ?? "/overlay/buildings.geojson",
    bufferMs: Number.isFinite(buffer) && buffer >= 0 ? buffer : 250,
    dt: Number.isFinite(dt) && dt > 0 ? dt : 0.1,
    theme: resolveThemeName(p.get("theme"), stored),
    bare: p.get("bare") === "1" || p.get("bare") === "true",
  };
}
