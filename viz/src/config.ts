// config.ts — viz runtime config from the URL query string. `?run=` picks
// the run id (subject ts.{run}.state.snap), `?ws=` the engine's WebSocket
// listener, `?buffer=` the interpolation buffer length in ms, `?dt=` the
// engine timestep in seconds (engine DefaultParams Dt = 0.1) — needed to
// derive vehicle speed from SIM time, not wall-clock frame spans.
// `?theme=` picks the palette (theme.ts THEMES; unknown names → navy).
// `?bake=<index.json URL>` selects the baked-replay shim (ADR-0023): the
// recording replays from static artifacts, `?ws=` is inert, and `?run=`
// is display-only (the shim echoes the index's run id).
// `?report=` points the run-statistics panel (statspanel.ts) at a
// runreport.py --json document; the report itself can also be dropped onto
// the map, since reports live in gitignored run directories (ADR-0030).
// `?center=lng,lat` (+ optional `?zoom=`, `?bearing=`, `?pitch=`) pins the
// opening camera instead of fitting the network's bounds — a deep link to
// one intervention, so a demo opens already pointed at the thing being
// discussed rather than at the whole city.

// CameraConfig is a fully-resolved opening camera. It exists only when
// ?center= parsed cleanly; every other field has a default, so a caller
// never has to handle partial cameras.
export interface CameraConfig {
  center: [number, number]; // [lng, lat]
  zoom: number;
  bearing: number;
  pitch: number;
}

// parseCamera reads the camera params, returning null unless ?center= is
// present AND well-formed. Malformed input falls back to the bounds fit
// rather than throwing or landing at null island: a bad deep link should
// degrade to "shows the whole network", which is recoverable on air, not
// to a blank ocean, which is not.
export function parseCamera(p: URLSearchParams): CameraConfig | null {
  const raw = p.get("center");
  if (raw === null || raw === "") return null;
  const parts = raw.split(",");
  if (parts.length !== 2) return null;
  // Empty components are rejected BEFORE Number(), for the same reason the
  // note below gives: Number("") is 0, which is finite and in range, so
  // "?center=,41.88" would otherwise parse as a perfectly valid camera at
  // lng 0 — null island, dressed as success. The half-written deep link is
  // the realistic way to produce this (a template that lost one value), and
  // it is exactly the case the fallback-to-bounds contract exists to catch.
  if (parts.some((s) => s.trim() === "")) return null;
  const lng = Number(parts[0]);
  const lat = Number(parts[1]);
  if (!Number.isFinite(lng) || !Number.isFinite(lat)) return null;
  if (lng < -180 || lng > 180 || lat < -90 || lat > 90) return null;

  // NOTE the explicit null/empty check: Number(null) is 0, which is finite,
  // so a bare Number(p.get(k)) would silently resolve every absent param to
  // 0 and open the map at zoom 0 — the whole globe — instead of the street
  // -level default. Caught by test, not by inspection.
  const num = (key: string, dflt: number, lo: number, hi: number): number => {
    const raw = p.get(key);
    if (raw === null || raw === "") return dflt;
    const v = Number(raw);
    if (!Number.isFinite(v)) return dflt;
    return v < lo ? lo : v > hi ? hi : v;
  };
  return {
    center: [lng, lat],
    // 15 is a street-level default: close enough to read individual
    // vehicles, wide enough to see an intersection and its approaches.
    zoom: num("zoom", 15, 0, 24),
    bearing: num("bearing", 0, -360, 360),
    pitch: num("pitch", 0, 0, 85),
  };
}

export interface VizConfig {
  run: string;
  ws: string;
  bake: string | null; // ?bake= index.json URL — baked mode when set (ADR-0023)
  networkUrl: string;
  zonesUrl: string;
  boundariesUrl: string;
  waterUrl: string;
  buildingsUrl: string;
  reportUrl: string; // ?report= — runreport.py --json document (ADR-0030 schema v1)
  flowUrl: string; // ?flow= — mkflowcurve.py document (flowpanel.ts schema v1)
  bufferMs: number;
  dt: number; // engine timestep, s — sim seconds per tick
  theme: string; // theme.ts THEMES key (navy default, resolved by getTheme)
  bare: boolean; // ?bare=1: hide HUD chrome + loading overlay (clean map shots)
  camera: CameraConfig | null; // ?center=/?zoom=: opening camera, null = fit bounds
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

export function loadConfig(search: string, hostname: string, protocol = "http:"): VizConfig {
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
    // Scheme follows the page: an https page may not open an insecure ws://
    // socket (mixed content), so static hosting over TLS must default wss.
    ws: p.get("ws") ?? `${protocol === "https:" ? "wss" : "ws"}://${hostname}:8443`,
    bake: p.get("bake"),
    networkUrl: p.get("net") ?? "/network.geojson",
    // Static WGS84 overlays (demosrv /overlay/); optional — a 404 just
    // means "no overlay on this demo", the fetch tolerates it.
    zonesUrl: p.get("zones") ?? "/overlay/zones.geojson",
    boundariesUrl: p.get("boundaries") ?? "/overlay/boundaries.geojson",
    waterUrl: p.get("water") ?? "/overlay/water.geojson",
    buildingsUrl: p.get("buildings") ?? "/overlay/buildings.geojson",
    // The checked-in sample so the panel has something real to render on a
    // bare `pnpm dev`; a run's own report is passed with ?report= or
    // dropped on the map.
    reportUrl: p.get("report") ?? "/sample-runreport.json",
    // The flow curve is a SEPARATE document from the report, not a block
    // inside it: runreport.py streams the metrics file and deliberately
    // avoids a second full pass over `trips`, which is exactly what the
    // curve needs. Optional like every other panel source — a 404 hides
    // the panel rather than failing the app.
    flowUrl: p.get("flow") ?? "/sample-flow.json",
    bufferMs: Number.isFinite(buffer) && buffer >= 0 ? buffer : 250,
    dt: Number.isFinite(dt) && dt > 0 ? dt : 0.1,
    theme: resolveThemeName(p.get("theme"), stored),
    bare: p.get("bare") === "1" || p.get("bare") === "true",
    camera: parseCamera(p),
  };
}
