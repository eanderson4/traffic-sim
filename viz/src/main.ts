// main.ts — M6 MapLibre realtime viz (ADR-0003: MapLibre-first, vanilla
// TS). Four data channels per docs/kb/raw/integration-maplibre-realtime:
//
//   1. static network — network.geojson loaded ONCE with promoteId; only
//      feature-state ever changes on it (never setData);
//   2. vehicles — TSSF v1 binary snapshots off ts.{run}.state.snap (nats.ws
//      WebSocket), decoded (tssf.ts), buffered ~250 ms and lerped at 60 fps
//      (snapshots.ts), applied as updateData diffs on a dedicated small
//      source (vehicles.ts); trucks render ARTICULATED — the trailer pose
//      is inferred client-side (artic.ts single-track model) onto a
//      parallel "trailers" source under the vehicles layer;
//   3. congestion — CLIENT-DERIVED per-lane mean speed (congestion.ts) onto
//      the network source via setFeatureState at ~1 Hz;
//   4. signals — TSSG v1 program table off ts.{run}.state.sig (tssg.ts,
//      ADR-0006 M9 addendum): light states DERIVE from the snapshot tick
//      (signals.ts), painted as one signal HEAD per movement (grouped
//      stop-lines, signals.ts) — a housing sprite (signalhead.ts) plus
//      three feature-state-gated lens layers, since icon-image cannot
//      read feature-state.
//
// The wire/network coordinates are the engine's local metric frame; proj.ts
// projects to WGS84 (network once at load, vehicles per render frame).

import maplibregl from "maplibre-gl";
import "maplibre-gl/dist/maplibre-gl.css";
import type { Feature, FeatureCollection, LineString, Point } from "geojson";

import { loadConfig } from "./config.ts";
import { decodeFrame } from "./tssf.ts";
import { decodeSignalFrame, type SigColor, type SignalTable } from "./tssg.ts";
import { headStatesAtTick, signalHeads, type SignalHead } from "./signals.ts";
import { SIGNAL_HEAD, SIGNAL_HEAD_IMAGE_ID, signalHeadImage } from "./signalhead.ts";
import { makeProjector, type LocalFrame } from "./proj.ts";
import { SnapshotBuffer } from "./snapshots.ts";
import {
  diffVehicles,
  diffTrailers,
  type RenderedTrailer,
  type RenderedVehicle,
  type SourceDiff,
} from "./vehicles.ts";
import { LaneIndex, laneSpeedRatios } from "./congestion.ts";
import { edgeBoundaries } from "./edges.ts";
import { subscribeSnapshots } from "./nats-client.ts";
import { Hud } from "./status.ts";
import { Legend } from "./legend.ts";
import { DemoSwitcher, demoIdFromNetUrl } from "./switcher.ts";
import { ModelPanel } from "./modelpanel.ts";
import { THEME, glyphByCls } from "./theme.ts";
import { bodyImages, glyphImageId, TRACTOR_IMAGE_ID, TRAILER_IMAGE_ID, ICON_SIZE_STOPS } from "./glyphs.ts";
import { Articulator } from "./artic.ts";

interface NetworkFile {
  type: string;
  frame?: LocalFrame;
  features: Array<Feature<LineString>>;
}

const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

// Blank dark style — local-first (ADR-0004): no tile-service dependency.
// Navy canvas per the project design tokens (math-900, theme.ts).
const DARK_STYLE: maplibregl.StyleSpecification = {
  version: 8,
  sources: {},
  layers: [{ id: "background", type: "background", paint: { "background-color": THEME.bg } }],
};

// Signal-head zoom curve: ONE size interpolation drives both the housing
// sprite (icon-size) and the lit-lens circle layers (radius + translate),
// so a lit lens always lands exactly on its dim counterpart.
const HEAD_SIZE_STOPS: Array<[number, number]> = [
  [11, 0.3],
  [14, 0.62],
  [17, 1.1],
];

function headSizeAt(z: number): number {
  const [z0, s0] = HEAD_SIZE_STOPS[0]!;
  const [z1, s1] = HEAD_SIZE_STOPS[1]!;
  const [z2, s2] = HEAD_SIZE_STOPS[2]!;
  if (z <= z1) return s0 + ((s1 - s0) * (z - z0)) / (z1 - z0);
  return s1 + ((s2 - s1) * (z - z1)) / (z2 - z1);
}

// Lens order matches SIGNAL_HEAD.lensOffsetYPx: red top, amber, green.
const SIG_LENSES: Array<{ color: SigColor; fill: string; offY: number }> = [
  { color: "red", fill: THEME.signalRed, offY: SIGNAL_HEAD.lensOffsetYPx[0] },
  { color: "amber", fill: THEME.signalAmber, offY: SIGNAL_HEAD.lensOffsetYPx[1] },
  { color: "green", fill: THEME.signalGreen, offY: SIGNAL_HEAD.lensOffsetYPx[2] },
];

async function main(): Promise<void> {
  const cfg = loadConfig(location.search, location.hostname);
  const hud = new Hud("status", "inspect");
  const legend = new Legend("legend");
  // In-map demo swap; hides itself when no demosrv answers (detached —
  // the map must not wait on the probe). The model panel (same probe
  // discipline) shows the resolved controllers + sim parameters.
  const demoId = demoIdFromNetUrl(cfg.networkUrl);
  void new DemoSwitcher(document.getElementById("switcher")!, demoId).init();
  void new ModelPanel(document.getElementById("model")!, demoId).init();

  // Loading overlay: page load → network fetch → ws connect → first
  // snapshot can take noticeable seconds on big demos (engine world build
  // happens before the first TSSF frame), and a bare "connecting…" HUD
  // read as a hang. The overlay narrates the stages and lifts on the
  // first renderable sample.
  const loadingEl = document.getElementById("loading");
  const loadingMsg = document.getElementById("loading-msg");
  if (!loadingEl || !loadingMsg) throw new Error("loading overlay: missing DOM elements");
  let loadingHidden = false;
  const hideLoading = (): void => {
    if (loadingHidden) return;
    loadingHidden = true;
    loadingEl.style.display = "none";
  };
  loadingMsg.textContent = `loading network ${cfg.networkUrl} …`;

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
  // Edge-group boundaries (edges.ts): lanes sharing the engine's lateral
  // group are "the same road" — the casing layer reads the edgeB flag to
  // draw the group's outer shell only, so a multi-lane road renders as one
  // band with colored interior stripes instead of N independent roads.
  const edgeBounds = edgeBoundaries(
    net.features.map((f) => ({
      id: String(f.id ?? f.properties?.["id"]),
      edge: f.properties?.["edge"] as string | undefined,
      edgeIndex: f.properties?.["edgeIndex"] as number | undefined,
    })),
  );
  const lanes: Feature<LineString>[] = net.features.map((f) => ({
    ...f,
    properties: {
      ...f.properties,
      edgeB: edgeBounds.has(String(f.id ?? f.properties?.["id"])),
    },
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

  const buffer = new SnapshotBuffer(cfg.bufferMs, cfg.dt);
  const applied = new Map<number, RenderedVehicle>();
  const appliedTrailers = new Map<number, RenderedTrailer>();
  const artic = new Articulator();
  let prevFrameMs = 0;
  let mapReady = false;

  // Signal channel state: the latest TSSG table, the head set applied to
  // the "signals" source (keyed by binding signature so a republished
  // table costs no setData), and the last-applied per-head colors.
  let sigTable: SignalTable | null = null;
  let sigHeads: SignalHead[] = [];
  let sigSourceKey = "";
  const prevSigStates = new Map<string, SigColor>();
  const sigPoints: Array<[number, number]> = []; // debug handle (headless proof)
  const sigDebug = { seen: 0, ok: 0, err: "", pts: -1 }; // debug handle: onSignals counters

  // ensureSignalSource (re)builds the head point features when the table's
  // binding changes — the ONLY setData on this source; per-tick color
  // changes ride feature-state below.
  function ensureSignalSource(): void {
    if (!mapReady || sigTable === null) return;
    const heads = signalHeads(sigTable, shapeByLane);
    sigDebug.pts = heads.length;
    const key = heads.map((h) => h.id).join(",");
    // Always take the LATEST table's program objects: a republished table
    // can carry identical lane bindings (same key) with new phase timing
    // (reconnect re-convergence), and derivation reads sigHeads[i].program.
    sigHeads = heads;
    if (key === sigSourceKey) return;
    sigSourceKey = key;
    prevSigStates.clear(); // re-apply every state on the next render pass
    const features: Feature<Point>[] = heads.map((h) => ({
      type: "Feature",
      id: h.id,
      properties: { id: h.id },
      geometry: { type: "Point", coordinates: project(h.x, h.y) },
    }));
    (map.getSource("signals") as maplibregl.GeoJSONSource).setData({
      type: "FeatureCollection",
      features,
    });
    sigPoints.length = 0;
    for (const f of features) sigPoints.push(f.geometry.coordinates as [number, number]);
  }

  // updateSignals derives per-head light colors at the render tick (the
  // sim tick of the interpolated sample, not wall clock) and applies only
  // the changes — a phase flip is a handful of setFeatureState calls.
  function updateSignals(tick: number): void {
    if (!mapReady || sigTable === null || sigSourceKey === "") return;
    for (const [id, color] of headStatesAtTick(sigHeads, tick)) {
      if (prevSigStates.get(id) !== color) {
        map.setFeatureState({ source: "signals", id }, { sig: color });
        prevSigStates.set(id, color);
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
        "line-color": THEME.casing,
        // Casing on edge-group boundaries only (edgeB, edges.ts): the
        // outer shell of each road. Interior lanes keep a faint trace so
        // adjacent stripes stay separable. Lanes without an edge group
        // (junction interiors, stale caches) are all boundaries, so they
        // degrade to full casing inside edgeBoundaries.
        "line-opacity": ["case", ["boolean", ["get", "edgeB"], true], 0.9, 0.15],
        // Zoomed-out legibility: a touch wider from z=11 (interpolating up
        // to z=14) — the network must read under the vehicle stream before
        // any detail matters.
        "line-width": ["interpolate", ["linear"], ["zoom"], 11, 3.2, 14, 7, 17, 12],
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
          -1, THEME.noData,
          0, THEME.stopped,
          0.35, THEME.mid,
          0.7, THEME.freeFlow,
          1.5, THEME.freeFlow,
        ],
        "line-width": ["interpolate", ["linear"], ["zoom"], 11, 1.8, 14, 4, 17, 8],
      },
    });
    // Vehicle glyphs (2026-07-22 legibility pass): one SDF rectangle per
    // BODY (car, truck tractor, truck trailer — true aspect ratios),
    // tinted and rotated to the wire heading; trucks render articulated,
    // the trailer pose inferred client-side (artic.ts single-track model).
    // icon-rotate evaluates the same conversion as
    // theme.ts:vehicleBearingDeg (CCW-from-east rad → CW-from-north deg)
    // inline, since style expressions can't call back into TS. Sources,
    // promoteIds, and the updateData diff channels mirror the original
    // vehicle channel.
    for (const b of bodyImages()) {
      map.addImage(b.id, b.image, { sdf: true });
    }
    // Trailers UNDER vehicles: the tractor overlaps the trailer nose at
    // the hitch, which reads as the pivot joint.
    map.addSource("trailers", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    map.addLayer({
      id: "trailers",
      type: "symbol",
      source: "trailers",
      layout: {
        "icon-image": TRAILER_IMAGE_ID,
        "icon-rotation-alignment": "map",
        "icon-allow-overlap": true,
        "icon-ignore-placement": true,
        "icon-rotate": ["-", 90, ["*", ["get", "angle"], 180 / Math.PI]],
        "icon-size": [
          "interpolate",
          ["linear"],
          ["zoom"],
          ICON_SIZE_STOPS[0]![0], ICON_SIZE_STOPS[0]![1],
          ICON_SIZE_STOPS[1]![0], ICON_SIZE_STOPS[1]![1],
          ICON_SIZE_STOPS[2]![0], ICON_SIZE_STOPS[2]![1],
        ],
      },
      paint: {
        "icon-color": glyphByCls(1).color,
        "icon-halo-color": THEME.bg,
        "icon-halo-width": 1,
      },
    });
    map.addLayer({
      id: "vehicles",
      type: "symbol",
      source: "vehicles",
      layout: {
        "icon-image": ["match", ["get", "cls"], 0, glyphImageId(glyphByCls(0)), TRACTOR_IMAGE_ID],
        "icon-rotation-alignment": "map",
        "icon-allow-overlap": true,
        "icon-ignore-placement": true,
        "icon-rotate": ["-", 90, ["*", ["get", "angle"], 180 / Math.PI]],
        // One zoom curve for every body — per-class length lives in the
        // image aspect now, not in a size multiplier.
        "icon-size": [
          "interpolate",
          ["linear"],
          ["zoom"],
          ICON_SIZE_STOPS[0]![0], ICON_SIZE_STOPS[0]![1],
          ICON_SIZE_STOPS[1]![0], ICON_SIZE_STOPS[1]![1],
          ICON_SIZE_STOPS[2]![0], ICON_SIZE_STOPS[2]![1],
        ],
      },
      paint: {
        "icon-color": ["match", ["get", "cls"], 0, glyphByCls(0).color, glyphByCls(1).color],
        "icon-halo-color": THEME.bg,
        "icon-halo-width": 1,
      },
    });
    // Signal lights (M9): one head per signalized MOVEMENT (grouped
    // stop-lines, signals.ts) — the housing sprite carries three dim
    // lenses and one circle layer per lens position paints the active
    // light, gated by feature-state "sig" (off = no lit lens). Zoom-gated
    // to ≥13: at city zooms the heads blob into clutter that hides the
    // network itself (zoomed-out detail is the congestion channel's job).
    map.addSource("signals", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    const sigHeadImg = signalHeadImage();
    map.addImage(SIGNAL_HEAD_IMAGE_ID, sigHeadImg.image, { pixelRatio: sigHeadImg.pixelRatio });
    map.addLayer({
      id: "signals-housing",
      type: "symbol",
      source: "signals",
      minzoom: 13,
      layout: {
        "icon-image": SIGNAL_HEAD_IMAGE_ID,
        "icon-allow-overlap": true,
        "icon-ignore-placement": true,
        "icon-size": [
          "interpolate",
          ["linear"],
          ["zoom"],
          HEAD_SIZE_STOPS[0]![0], HEAD_SIZE_STOPS[0]![1],
          HEAD_SIZE_STOPS[1]![0], HEAD_SIZE_STOPS[1]![1],
          HEAD_SIZE_STOPS[2]![0], HEAD_SIZE_STOPS[2]![1],
        ],
      },
    });
    for (const lens of SIG_LENSES) {
      map.addLayer({
        id: `signals-lens-${lens.color}`,
        type: "circle",
        source: "signals",
        minzoom: 13,
        paint: {
          "circle-color": lens.fill,
          "circle-opacity": [
            "match",
            ["coalesce", ["feature-state", "sig"], "off"],
            lens.color,
            1,
            0,
          ],
          // Slight blur reads as lens glow; radius/translate track the
          // housing's icon-size curve (viewport anchor = screen px).
          "circle-blur": 0.25,
          "circle-radius": [
            "interpolate",
            ["linear"],
            ["zoom"],
            11, SIGNAL_HEAD.lensRadiusPx * headSizeAt(11),
            14, SIGNAL_HEAD.lensRadiusPx * headSizeAt(14),
            17, SIGNAL_HEAD.lensRadiusPx * headSizeAt(17),
          ],
          "circle-translate": [
            "interpolate",
            ["linear"],
            ["zoom"],
            11, ["literal", [0, lens.offY * headSizeAt(11)]],
            14, ["literal", [0, lens.offY * headSizeAt(14)]],
            17, ["literal", [0, lens.offY * headSizeAt(17)]],
          ],
          "circle-translate-anchor": "viewport",
        },
      });
    }
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
  loadingMsg.textContent = `connecting to ${cfg.ws} …`;
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
    (connected, detail) => {
      hud.setConnection(connected, `${detail}  ·  run ${cfg.run}`);
      // The overlay covers the HUD until the first sample — mirror the
      // connection state into it or a pre-snapshot drop reads as a hang.
      if (!loadingHidden) {
        loadingMsg.textContent = connected
          ? "connected — waiting for the engine's first snapshot (world build) …"
          : `connection lost before the first snapshot — ${detail}`;
      }
    },
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
        hideLoading(); // first renderable sample — the engine is streaming
        const dtS = prevFrameMs === 0 ? 0 : (nowMs - prevFrameMs) / 1000;
        prevFrameMs = nowMs;
        const next = new Map<number, RenderedVehicle>();
        const nextTrailers = new Map<number, RenderedTrailer>();
        for (const v of sample.vehicles) {
          // The wire position is the FRONT BUMPER (engine Project: s is
          // front-bumper arc length) but the glyph centers on its anchor.
          if (v.cls === 0) {
            // Car: shift back half the length along the heading so the
            // rect spans bumper to tail instead of protruding forward.
            const car = glyphByCls(0);
            const cx = v.x - (car.lengthM / 2) * Math.cos(v.angle);
            const cy = v.y - (car.lengthM / 2) * Math.sin(v.angle);
            next.set(v.id, { ...v, lngLat: project(cx, cy) });
          } else {
            // Truck: articulated — the tractor keeps the wire heading, the
            // trailer follows its hitch (artic.ts single-track model).
            const pose = artic.update(v.id, v.x, v.y, v.angle, v.speed, dtS);
            next.set(v.id, { ...v, lngLat: project(pose.tractorX, pose.tractorY) });
            nextTrailers.set(v.id, {
              lngLat: project(pose.trailerX, pose.trailerY),
              angle: pose.trailerAngle,
            });
          }
        }
        artic.prune(next);
        const diff: SourceDiff = diffVehicles(applied, next);
        if (diff.add || diff.update || diff.remove) {
          (map.getSource("vehicles") as maplibregl.GeoJSONSource).updateData(
            diff as maplibregl.GeoJSONSourceDiff,
          );
          applied.clear();
          for (const [id, v] of next) applied.set(id, v);
        }
        const tDiff: SourceDiff = diffTrailers(appliedTrailers, nextTrailers);
        if (tDiff.add || tDiff.update || tDiff.remove) {
          (map.getSource("trailers") as maplibregl.GeoJSONSource).updateData(
            tDiff as maplibregl.GeoJSONSourceDiff,
          );
          appliedTrailers.clear();
          for (const [id, t] of nextTrailers) appliedTrailers.set(id, t);
        }
        hud.setFrame(sample.tick, sample.vehicles.length, sample.starved);
        legend.setTick(sample.tick);
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
  const msg = `viz failed: ${err instanceof Error ? err.message : String(err)}`;
  const el = document.getElementById("status");
  if (el) el.textContent = msg;
  const lm = document.getElementById("loading-msg");
  if (lm) lm.textContent = msg;
  console.error(err);
});
