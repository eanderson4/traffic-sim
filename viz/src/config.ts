// config.ts — viz runtime config from the URL query string. `?run=` picks
// the run id (subject ts.{run}.state.snap), `?ws=` the engine's WebSocket
// listener, `?buffer=` the interpolation buffer length in ms, `?dt=` the
// engine timestep in seconds (engine DefaultParams Dt = 0.1) — needed to
// derive vehicle speed from SIM time, not wall-clock frame spans.

export interface VizConfig {
  run: string;
  ws: string;
  networkUrl: string;
  zonesUrl: string;
  boundariesUrl: string;
  waterUrl: string;
  bufferMs: number;
  dt: number; // engine timestep, s — sim seconds per tick
}

export function loadConfig(search: string, hostname: string): VizConfig {
  const p = new URLSearchParams(search);
  const buffer = Number(p.get("buffer") ?? "250");
  const dt = Number(p.get("dt") ?? "0.1");
  return {
    run: p.get("run") ?? "demo",
    ws: p.get("ws") ?? `ws://${hostname}:8443`,
    networkUrl: p.get("net") ?? "/network.geojson",
    // Static WGS84 overlays (demosrv /overlay/); optional — a 404 just
    // means "no overlay on this demo", the fetch tolerates it.
    zonesUrl: p.get("zones") ?? "/overlay/zones.geojson",
    boundariesUrl: p.get("boundaries") ?? "/overlay/boundaries.geojson",
    waterUrl: p.get("water") ?? "/overlay/water.geojson",
    bufferMs: Number.isFinite(buffer) && buffer >= 0 ? buffer : 250,
    dt: Number.isFinite(dt) && dt > 0 ? dt : 0.1,
  };
}
