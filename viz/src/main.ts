// main.ts — M6 MapLibre realtime viz (ADR-0003: MapLibre-first, vanilla
// TS). Four data channels per docs/kb/raw/integration-maplibre-realtime:
//
//   1. static network — network.geojson loaded ONCE with promoteId; only
//      feature-state ever changes on it (never setData);
//   2. vehicles — TSSF v1 binary snapshots off ts.{run}.state.snap (nats.ws
//      WebSocket), decoded (tssf.ts), buffered ~250 ms and lerped at 60 fps
//      (snapshots.ts), applied as updateData diffs on a dedicated small
//      source (vehicles.ts);
//   3. congestion — CLIENT-DERIVED per-lane mean speed (congestion.ts) onto
//      the network source via setFeatureState at ~1 Hz;
//   4. signals — TSSG v1 program table off ts.{run}.state.sig (tssg.ts,
//      ADR-0006 M9 addendum): light states DERIVE from the snapshot tick
//      (signals.ts), painted as stop-line circles via feature-state.
//
// The wire/network coordinates are the engine's local metric frame; proj.ts
// projects to WGS84 (network once at load, vehicles per render frame).

import maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import type { Feature, FeatureCollection, LineString, Point } from "geojson";

import { loadConfig } from "./config.ts";
import { decodeFrame } from "./tssf.ts";
import { decodeSignalFrame, type SigColor, type SignalTable } from "./tssg.ts";
import { laneStatesAtTick, signalStopLines } from "./signals.ts";
import { makeProjector, type LocalFrame } from "./proj.ts";
import { SnapshotBuffer } from "./snapshots.ts";
import { diffVehicles, type RenderedVehicle, type SourceDiff } from "./vehicles.ts";
import { LaneIndex, laneSpeedRatios } from "./congestion.ts";
import { subscribeSnapshots } from "./nats-client.ts";
import { Hud } from "./status.ts";

interface NetworkFile {
  type: string;
  frame?: LocalFrame;
  features: Array<Feature<LineString>>;
}

const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

// Blank dark style — local-first (ADR-0004): no tile-service dependency.
// Navy canvas per the project design tokens (math-900).
const DARK_STYLE: maplibregl.StyleSpecification = {
  version: 8,
  sources: {},
  layers: [{ id: "background", type: "background", paint: { "background-color": "#0e1d5c" } }],
};

async function main(): Promise<void> {
  const cfg = loadConfig(location.search, location.hostname);
  const hud = new Hud("status", "inspect");

  const res = await fetch(cfg.networkUrl);
  if (!res.ok) throw new Error(`fetch ${cfg.networkUrl}: ${res.status} ${res.statusText}`);
  const net = (await res.json()) as NetworkFile;
  if (net.type !== "FeatureCollection" || !Array.isArray(net.features)) {
    throw new Error(`${cfg.networkUrl}: not a FeatureCollection`);
  }
  if (!net.frame) throw new Error(`${cfg.networkUrl}: missing "frame" descriptor`);
  const project = makeProjector(net.frame);

  // Static channel: project lane polylines once (local metric → WGS84).
  // Engine export guarantees [x, y] pairs (engine/geojson.go) — validated.
  const pair = (c: number[]): [number, number] => {
    if (c.length < 2 || c[0] === undefined || c[1] === undefined) {
      throw new Error(`${cfg.networkUrl}: malformed coordinate ${JSON.stringify(c)}`);
    }
    return [c[0], c[1]];
  };
  const lanes: Feature<LineString>[] = net.features.map((f) => ({
    ...f,
    geometry: {
      type: "LineString",
      coordinates: f.geometry.coordinates.map((c) => project(...pair(c))),
    },
  }));
  const networkFC: FeatureCollection = { type: "FeatureCollection", features: lanes };
  const bounds = new maplibregl.LngLatBounds();
  for (const f of lanes) for (const c of f.geometry.coordinates) bounds.extend(pair(c));

  // Congestion inputs stay in the metric frame (vehicle positions arrive
  // metric; nearest-lane matching happens there).
  const laneIndex = new LaneIndex(
    net.features.map((f) => ({
      id: String(f.id ?? f.properties?.["id"]),
      shape: f.geometry.coordinates as Array<[number, number]>,
    })),
  );
  const speedLimitByLane = new Map<string, number>();
  for (const f of net.features) {
    speedLimitByLane.set(String(f.id ?? f.properties?.["id"]), Number(f.properties?.["speedLimit"]));
  }
  // Signal stop-lines resolve against the static geometry (local frame).
  const shapeByLane = new Map<string, Array<[number, number]>>();
  for (const f of net.features) {
    shapeByLane.set(String(f.id ?? f.properties?.["id"]), f.geometry.coordinates as Array<[number, number]>);
  }

  const map = new maplibregl.Map({
    container: "map",
    style: DARK_STYLE,
    bounds,
    fitBoundsOptions: { padding: 40 },
    attributionControl: false,
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");

  const buffer = new SnapshotBuffer(cfg.bufferMs);
  const applied = new Map<number, RenderedVehicle>();
  let mapReady = false;

  // Signal channel state: the latest TSSG table, the stop-line set applied
  // to the "signals" source (keyed by binding signature so a republished
  // table costs no setData), and the last-applied per-lane colors.
  let sigTable: SignalTable | null = null;
  let sigSourceKey = "";
  const prevSigStates = new Map<string, SigColor>();
  const sigPoints: Array<[number, number]> = []; // debug handle (headless proof)
  const sigDebug = { seen: 0, ok: 0, err: "", pts: -1 }; // debug handle: onSignals counters

  // ensureSignalSource (re)builds the stop-line point features when the
  // table's binding changes — the ONLY setData on this source; per-tick
  // color changes ride feature-state below.
  function ensureSignalSource(): void {
    if (!mapReady || sigTable === null) return;
    const pts = signalStopLines(sigTable, shapeByLane);
    sigDebug.pts = pts.length;
    const key = pts.map((p) => p.laneId).join(",");
    if (key === sigSourceKey) return;
    sigSourceKey = key;
    prevSigStates.clear(); // re-apply every state on the next render pass
    const features: Feature<Point>[] = pts.map((p) => ({
      type: "Feature",
      id: p.laneId,
      properties: { id: p.laneId },
      geometry: { type: "Point", coordinates: project(p.x, p.y) },
    }));
    (map.getSource("signals") as maplibregl.GeoJSONSource).setData({
      type: "FeatureCollection",
      features,
    });
    sigPoints.length = 0;
    for (const f of features) sigPoints.push(f.geometry.coordinates as [number, number]);
  }

  // updateSignals derives per-lane light colors at the render tick (the
  // sim tick of the interpolated sample, not wall clock) and applies only
  // the changes — a phase flip is a handful of setFeatureState calls.
  function updateSignals(tick: number): void {
    if (!mapReady || sigTable === null || sigSourceKey === "") return;
    for (const [laneId, color] of laneStatesAtTick(sigTable, tick)) {
      if (prevSigStates.get(laneId) !== color) {
        map.setFeatureState({ source: "signals", id: laneId }, { sig: color });
        prevSigStates.set(laneId, color);
      }
    }
  }

  map.on("load", () => {
    map.addSource("network", { type: "geojson", data: networkFC, promoteId: "id" });
    map.addSource("vehicles", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    map.addLayer({
      id: "network-casing",
      type: "line",
      source: "network",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": "#122881", // math-800 casing
        "line-opacity": 0.9,
        "line-width": ["interpolate", ["linear"], ["zoom"], 11, 2.5, 14, 7, 17, 12],
      },
    });
    map.addLayer({
      id: "network-line",
      type: "line",
      source: "network",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        // feature-state "ratio" (client-derived mean speed / limit); -1 = no data.
        "line-color": [
          "interpolate",
          ["linear"],
          ["coalesce", ["feature-state", "ratio"], -1],
          -1, "#7e9dff", // math-300: no congestion data
          0, "#e5484d", // stopped
          0.35, "#e8b43a", // gold
          0.7, "#1e9e6a", // free flow
          1.5, "#1e9e6a",
        ],
        "line-width": ["interpolate", ["linear"], ["zoom"], 11, 1.2, 14, 4, 17, 8],
      },
    });
    map.addLayer({
      id: "vehicles",
      type: "circle",
      source: "vehicles",
      paint: {
        "circle-color": ["match", ["get", "cls"], 0, "#eaf0ff", "#ff7d4d"],
        "circle-radius": ["match", ["get", "cls"], 0, 3.2, 5],
        "circle-stroke-color": "#0e1d5c",
        "circle-stroke-width": 1,
      },
    });
    // Signal lights (M9): one circle per signal-bound internal lane at its
    // stop-line entry; color rides feature-state "sig" (off = invisible).
    map.addSource("signals", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    map.addLayer({
      id: "signals",
      type: "circle",
      source: "signals",
      paint: {
        "circle-color": [
          "match",
          ["coalesce", ["feature-state", "sig"], "off"],
          "green",
          "#2ecc71",
          "amber",
          "#f5b301",
          "red",
          "#e5484d",
          "#0e1d5c",
        ],
        "circle-opacity": ["match", ["coalesce", ["feature-state", "sig"], "off"], "off", 0, 1],
        "circle-radius": ["interpolate", ["linear"], ["zoom"], 11, 3, 14, 5, 17, 8],
        "circle-stroke-color": "#0e1d5c",
        "circle-stroke-width": 1,
      },
    });
    map.on("click", (e) => {
      const feats = map.queryRenderedFeatures(e.point, { layers: ["vehicles"] });
      const f = feats[0];
      if (!f) {
        hud.inspect(null);
        return;
      }
      hud.inspect({
        id: Number(f.properties?.["id"]),
        cls: Number(f.properties?.["cls"]),
        speed: Number(f.properties?.["speed"]),
      });
    });
    map.on("mousemove", (e) => {
      const feats = map.queryRenderedFeatures(e.point, { layers: ["vehicles"] });
      map.getCanvas().style.cursor = feats.length > 0 ? "pointer" : "";
    });
    mapReady = true;
    ensureSignalSource(); // a table may have arrived before the style
  });

  hud.setConnection(false, `connecting to ${cfg.ws} …`);
  await subscribeSnapshots(
    cfg.ws,
    cfg.run,
    (data) => {
      try {
        buffer.push(decodeFrame(data), performance.now());
      } catch (err) {
        hud.setConnection(false, String(err));
      }
    },
    (data) => {
      sigDebug.seen++;
      try {
        sigTable = decodeSignalFrame(data);
        sigDebug.ok++;
        ensureSignalSource();
      } catch (err) {
        sigDebug.err = String(err);
        hud.setConnection(false, String(err));
      }
    },
    (connected, detail) => hud.setConnection(connected, `${detail}  ·  run ${cfg.run}`),
  );

  // Congestion: recompute per-lane ratios at ~1 Hz (research: feature-state
  // updates are a per-snapshot cadence channel, not per render frame).
  let prevRatios = new Map<string, number>();
  let lastCongestionMs = 0;
  function updateCongestion(nowMs: number): void {
    if (!mapReady || nowMs - lastCongestionMs < 1000) return;
    lastCongestionMs = nowMs;
    const sample = buffer.sample(nowMs);
    if (!sample) return;
    const speedsByLane = new Map<string, number[]>();
    for (const v of sample.vehicles) {
      const laneId = laneIndex.nearestLane(v.x, v.y, 6);
      if (laneId === null) continue;
      const arr = speedsByLane.get(laneId);
      if (arr === undefined) speedsByLane.set(laneId, [v.speed]);
      else arr.push(v.speed);
    }
    const ratios = laneSpeedRatios(speedsByLane, speedLimitByLane);
    for (const [laneId, ratio] of ratios) {
      if (prevRatios.get(laneId) !== ratio) {
        map.setFeatureState({ source: "network", id: laneId }, { ratio });
      }
    }
    for (const laneId of prevRatios.keys()) {
      if (!ratios.has(laneId)) map.removeFeatureState({ source: "network", id: laneId });
    }
    prevRatios = ratios;
  }

  // Render loop: interpolate behind the buffer, apply updateData diffs once
  // per rAF frame — never per incoming message.
  function frame(nowMs: number): void {
    if (mapReady) {
      const sample = buffer.sample(nowMs);
      if (sample) {
        const next = new Map<number, RenderedVehicle>();
        for (const v of sample.vehicles) {
          next.set(v.id, { ...v, lngLat: project(v.x, v.y) });
        }
        const diff: SourceDiff = diffVehicles(applied, next);
        if (diff.add || diff.update || diff.remove) {
          (map.getSource("vehicles") as maplibregl.GeoJSONSource).updateData(
            diff as maplibregl.GeoJSONSourceDiff,
          );
          applied.clear();
          for (const [id, v] of next) applied.set(id, v);
        }
        hud.setFrame(sample.tick, sample.vehicles.length, sample.starved);
        updateCongestion(nowMs);
        updateSignals(sample.tick);
      }
    }
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);

  // Debug/testing handle: lets headless verification (scripts/screenshot.mjs
  // and friends) and the browser console reach the live map + render set.
  (window as unknown as { __viz: unknown }).__viz = { map, applied, signals: prevSigStates, sigPoints, sigDebug };
}

main().catch((err) => {
  const el = document.getElementById("status");
  if (el) el.textContent = `viz failed: ${err instanceof Error ? err.message : String(err)}`;
  console.error(err);
});
