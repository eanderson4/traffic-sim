// config.ts — viz runtime config from the URL query string. `?run=` picks
// the run id (subject ts.{run}.state.snap), `?ws=` the engine's WebSocket
// listener, `?buffer=` the interpolation buffer length in ms.

export interface VizConfig {
  run: string;
  ws: string;
  networkUrl: string;
  bufferMs: number;
}

export function loadConfig(search: string, hostname: string): VizConfig {
  const p = new URLSearchParams(search);
  const buffer = Number(p.get("buffer") ?? "250");
  return {
    run: p.get("run") ?? "demo",
    ws: p.get("ws") ?? `ws://${hostname}:8443`,
    networkUrl: p.get("net") ?? "/network.geojson",
    bufferMs: Number.isFinite(buffer) && buffer >= 0 ? buffer : 250,
  };
}
