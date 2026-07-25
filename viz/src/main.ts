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

import { loadConfig, THEME_STORAGE_KEY } from "./config.ts";
import { parseOverlay, prepareZones } from "./overlays.ts";
import { decodeFrame } from "./tssf.ts";
import { decodeSignalFrame, parseSigChunkHeader, SignalTableAccumulator, type SigColor, type SignalTable } from "./tssg.ts";
import { headStatesAtTick, signalHeads, type SignalHead } from "./signals.ts";
import { SIGNAL_HEAD, SIGNAL_HEAD_IMAGE_ID, signalHeadImage } from "./signalhead.ts";
import { STOP_SIGN_IMAGE_ID, stopSignImage, stopSigns } from "./stopsign.ts";
import { makeProjector } from "./proj.ts";
import { SnapshotBuffer, SeekGate } from "./snapshots.ts";
import {
  diffVehicles,
  diffTrailers,
  type RenderedTrailer,
  type RenderedVehicle,
  type SourceDiff,
} from "./vehicles.ts";
import { LaneIndex, laneSpeedRatios } from "./congestion.ts";
import { edgeBoundaries } from "./edges.ts";
import { subscribeSnapshots, requestSignalTable } from "./nats-client.ts";
import type { MsgHdrs, NatsConnection } from "nats.ws";
import { Hud } from "./status.ts";
import { Legend } from "./legend.ts";
import { DEFAULT_TOGGLES, layerOpsFor, type ToggleState } from "./layertoggles.ts";
import { DemoSwitcher, demoIdFromNetUrl } from "./switcher.ts";
import { ModelPanel } from "./modelpanel.ts";
import { ReplayPanel } from "./replaypanel.ts";
import { THEMES, getTheme, glyphByCls } from "./theme.ts";
import { bodyImages, glyphImageId, TRACTOR_IMAGE_ID, TRAILER_IMAGE_ID, ICON_SIZE_STOPS } from "./glyphs.ts";
import { Articulator } from "./artic.ts";
import { fetchNetwork, type NetworkFile } from "./netload.ts";

const EMPTY_FC: FeatureCollection = { type: "FeatureCollection", features: [] };

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

async function main(): Promise<void> {
  const cfg = loadConfig(location.search, location.hostname);
  // Resolve the palette once (?theme=, navy default): every MapLibre paint
  // prop, the legend, the signal-head sprite, and the HUD CSS variables
  // below read from this one ThemeSpec.
  const theme = getTheme(cfg.theme);

  // Blank style — local-first (ADR-0004): no tile-service dependency.
  // Canvas color per the active theme (theme.ts).
  const blankStyle: maplibregl.StyleSpecification = {
    version: 8,
    // Vendored SDF font (viz/public/fonts) — text symbol layers (overlay
    // labels) need a glyphs URL, and local-first rules out a font service.
    glyphs: "/fonts/{fontstack}/{range}.pbf",
    sources: {},
    layers: [{ id: "background", type: "background", paint: { "background-color": theme.bg } }],
  };

  // Lens order matches SIGNAL_HEAD.lensOffsetYPx: red top, amber, green.
  const SIG_LENSES: Array<{ color: SigColor; fill: string; offY: number }> = [
    { color: "red", fill: theme.signalRed, offY: SIGNAL_HEAD.lensOffsetYPx[0] },
    { color: "amber", fill: theme.signalAmber, offY: SIGNAL_HEAD.lensOffsetYPx[1] },
    { color: "green", fill: theme.signalGreen, offY: SIGNAL_HEAD.lensOffsetYPx[2] },
  ];

  // HUD chrome (index.html) reads these CSS variables; their :root
  // defaults are the navy values, so anything before this line (and pages
  // without this script, e.g. demos.html) renders navy unchanged.
  const themeCssVars: Record<string, string> = {
    "--t-bg": theme.bg,
    "--t-hud-bg": theme.hudBg,
    "--t-hud-border": theme.hudBorder,
    "--t-text": theme.hudText,
    "--t-text-dim": theme.hudTextDim,
    "--t-overlay": theme.overlayBg,
    "--t-sig-bg": theme.sigHousing,
    "--t-sig-stroke": theme.sigStroke,
  };
  for (const [k, v] of Object.entries(themeCssVars)) {
    document.documentElement.style.setProperty(k, v);
  }

  // Theme toggle (HUD, top-right; index.html #theme-toggle): the label
  // shows the ACTIVE theme, clicking flips to the other one. The choice
  // persists to localStorage (config.ts THEME_STORAGE_KEY — the demos
  // menu's toggle writes the same key) and is reflected into the URL
  // (?theme=) before reloading. A reload, not a live re-paint: the
  // signal-head housing is a baked sprite and the background layer color
  // was fixed at style construction, so a live switch would need a second
  // restyle path — reload reuses the single theme-application path above
  // and the stream simply reconnects.
  const activeThemeName = Object.hasOwn(THEMES, cfg.theme) ? cfg.theme : "navy";
  const themeToggle = document.getElementById("theme-toggle");
  if (themeToggle instanceof HTMLButtonElement) {
    themeToggle.textContent = activeThemeName.toUpperCase();
    themeToggle.addEventListener("click", () => {
      const next = activeThemeName === "navy" ? "paper" : "navy";
      try {
        localStorage.setItem(THEME_STORAGE_KEY, next);
      } catch {
        // Storage denied (private mode) — the URL param still carries it.
      }
      const url = new URL(location.href);
      url.searchParams.set("theme", next);
      history.replaceState(null, "", url);
      location.reload();
    });
  }

  const hud = new Hud("status", "inspect");
  // Legend row clicks → map layers. The pure toggle→ops mapping lives in
  // layertoggles.ts (the legend itself stays MapLibre-free); state is
  // in-memory only (no persistence), default all-on. A click before the
  // style loads just updates the state — the load handler re-applies it.
  const toggles: ToggleState = { ...DEFAULT_TOGGLES };
  // Hoisted above applyLayerToggles (TDZ): a legend click before the style
  // loads must read false, never throw.
  let mapReady = false;
  function applyLayerToggles(): void {
    if (!mapReady) return;
    const ops = layerOpsFor(toggles);
    map.setFilter("vehicles", ops.vehiclesFilter as maplibregl.FilterSpecification | null);
    for (const [id, on] of ops.visibility) {
      map.setLayoutProperty(id, "visibility", on ? "visible" : "none");
    }
  }
  const legend = new Legend("legend", theme, (key, on) => {
    toggles[key] = on;
    applyLayerToggles();
  }, cfg.dt);
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

  // ?bare=1: clean-canvas mode for screenshots — CSS hides the HUD chrome
  // and the loading overlay (which otherwise waits on a live engine).
  if (cfg.bare) document.body.classList.add("bare");

  // Network GeoJSON — chunked manifests (netload.ts) are reassembled
  // here, so everything downstream sees one plain collection.
  const net = await fetchNetwork(cfg.networkUrl);
  if (net.type !== "FeatureCollection" || !Array.isArray(net.features)) {
    throw new Error(`${cfg.networkUrl}: not a FeatureCollection`);
  }
  if (!net.frame) throw new Error(`${cfg.networkUrl}: missing "frame" descriptor`);
  const project = makeProjector(net.frame);

  // Optional static overlays (zones + admin boundaries): already WGS84
  // lon/lat, so unlike the network they go to MapLibre unprojected. A 404
  // (demos without overlays) or a garbage doc just means "no overlay" —
  // parseOverlay/prepareZones (overlays.ts) also stamp the style
  // classification (zkind/zrun) that the zone layers filter on.
  const fetchOverlay = async (url: string): Promise<FeatureCollection | null> => {
    try {
      const res = await fetch(url);
      if (!res.ok) return null;
      return parseOverlay(await res.json());
    } catch {
      return null;
    }
  };
  const [zonesFC, boundariesFC, waterFC] = await Promise.all([
    fetchOverlay(cfg.zonesUrl).then((fc) => (fc === null ? null : prepareZones(fc))),
    fetchOverlay(cfg.boundariesUrl),
    fetchOverlay(cfg.waterUrl),
  ]);

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
  // Stop signs (stopsign.ts, ADR-0010 row/junction lane properties): one
  // sign per stop-controlled approach cluster, resolved ONCE from the
  // static geometry — no feature-state channel, no per-tick update.
  const stopSignPts = stopSigns(
    net.features.map((f) => ({
      id: String(f.id ?? f.properties?.["id"]),
      row: String(f.properties?.["row"] ?? ""),
      junction: String(f.properties?.["junction"] ?? ""),
      shape: f.geometry.coordinates as Array<[number, number]>,
    })),
  );

  const map = new maplibregl.Map({
    container: "map",
    style: blankStyle,
    bounds,
    fitBoundsOptions: { padding: 40 },
    attributionControl: false,
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");

  // currentDt is THE sim timestep for every viz-side sim-math consumer
  // (buffer speeds, seek-gate threshold, artic integration). It starts at
  // the ?dt= URL hint and is REPLACED by the recorded run's authoritative
  // dt on the replay panel's first status probe (the onStatus hook below)
  // — a direct/stale deep link must not leave any consumer on the hint.
  let currentDt = cfg.dt;
  const buffer = new SnapshotBuffer(cfg.bufferMs, currentDt);
  const seekGate = new SeekGate(currentDt);
  const applied = new Map<number, RenderedVehicle>();
  const appliedTrailers = new Map<number, RenderedTrailer>();
  const artic = new Articulator();
  let prevSampleTick: number | null = null; // last rendered sample's tick (sim clock)

  // Signal channel state: the latest TSSG table, the head set applied to
  // the "signals" source (keyed by binding signature so a republished
  // table costs no setData), and the last-applied per-head colors.
  let sigTable: SignalTable | null = null;
  let sigHeads: SignalHead[] = [];
  let sigSourceKey = "";
  const prevSigStates = new Map<string, SigColor>();
  const sigPoints: Array<[number, number]> = []; // debug handle (headless proof)
  const sigDebug = { seen: 0, ok: 0, err: "", pts: -1 }; // debug handle: onSignals counters

  // Chunked tables (ADR-0016): the accumulator reassembles sig_chunk
  // generations; a complete one swaps in atomically. On a gap (a dropped
  // chunk killed the round) or a stalled partial (15 s without the rest —
  // the round simply never completed) the client PULLS the full set via
  // the request/reply subject; one attach-time request covers the
  // late-joiner case without waiting out a catch-up round.
  const sigAccum = new SignalTableAccumulator();
  let wasConnected: boolean | null = null;
  let natsConn: NatsConnection | null = null;
  let sigPartialTimer: number | null = null;
  let sigReqInFlight = false;
  const installSigTable = (table: SignalTable): void => {
    sigTable = table;
    sigDebug.ok++;
    ensureSignalSource();
    clearSigPartialTimer();
  };
  let sigReqRetries = 0;
  const requestSig = (): void => {
    // One request in flight: reply chunks interleave with broadcast rounds
    // and each gap would otherwise fire another full-set request — exactly
    // the busy-tab condition the pull path exists to fix (ADR-0016). Reply
    // chunks feed a DEDICATED accumulator: interleaved broadcast rounds can
    // no longer reset (or be reset by) the pull (sol review). A reply that
    // itself arrives gapped/partial retries — bounded (3) so a chronically
    // dropping path falls back to the broadcast cadence rather than
    // hot-looping full-set requests.
    if (natsConn === null || sigReqInFlight || sigReqRetries >= 3) return;
    sigReqInFlight = true;
    sigReqRetries++;
    const replyAccum = new SignalTableAccumulator();
    let retry = false;
    void (async () => {
      const completed = await requestSignalTable(natsConn!, cfg.run, (data, headers) => {
        sigDebug.seen++;
        try {
          const chunk = parseSigChunkHeader(headers?.get("sig_chunk"));
          const res = replyAccum.feed(decodeSignalFrame(data), chunk);
          if (res.gap) retry = true;
          if (res.table !== null) {
            sigReqRetries = 0; // only a PULL success re-arms (a routine broadcast completing must not defeat the 3-request bound)
            installSigTable(res.table);
          }
          return true;
        } catch (err) {
          sigDebug.err = String(err);
          return false;
        }
      });
      sigReqInFlight = false;
      // Retry on gapped/partial sets AND on a wholesale-dropped reply
      // (!completed) — bounded by sigReqRetries (3), else the broadcast
      // cadence is the fallback.
      if (!completed || retry || replyAccum.partial) requestSig();
    })();
  };
  const clearSigPartialTimer = (): void => {
    if (sigPartialTimer !== null) {
      window.clearTimeout(sigPartialTimer);
      sigPartialTimer = null;
    }
  };
  function handleSigMessage(data: Uint8Array, headers: MsgHdrs | undefined): void {
    sigDebug.seen++;
    try {
      const chunk = parseSigChunkHeader(headers?.get("sig_chunk"));
      const res = sigAccum.feed(decodeSignalFrame(data), chunk);
      if (res.gap) requestSig();
      if (res.table !== null) {
        installSigTable(res.table);
      } else if (sigAccum.partial && sigPartialTimer === null) {
        sigPartialTimer = window.setTimeout(() => {
          sigPartialTimer = null;
          if (sigAccum.partial) requestSig();
        }, 15_000);
      }
    } catch (err) {
      sigDebug.err = String(err);
      hud.setConnection(false, String(err));
    }
  }

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
    // Two features per head — the housing POINT and the stop-bar LINE —
    // sharing the head id so ONE setFeatureState colors both (signals.ts
    // doc). The `kind` property keeps symbol/circle layers off the lines
    // and the line layer off the points.
    const features: Array<Feature<Point | LineString>> = [];
    for (const h of heads) {
      features.push({
        type: "Feature",
        id: h.id,
        properties: { id: h.id, kind: "head" },
        geometry: { type: "Point", coordinates: project(h.x, h.y) },
      });
      if (h.bar !== null) {
        features.push({
          type: "Feature",
          id: h.id,
          properties: { id: h.id, kind: "bar" },
          geometry: {
            type: "LineString",
            coordinates: [project(h.bar[0], h.bar[1]), project(h.bar[2], h.bar[3])],
          },
        });
      }
    }
    (map.getSource("signals") as maplibregl.GeoJSONSource).setData({
      type: "FeatureCollection",
      features,
    });
    sigPoints.length = 0;
    for (const h of heads) sigPoints.push(project(h.x, h.y));
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
    // Water FIRST: the fill must sit under the road lines (the other
    // overlays — boundaries/zones — are added after the roads instead).
    if (waterFC) {
      map.addSource("water", { type: "geojson", data: waterFC });
      map.addLayer({
        id: "water-fill",
        type: "fill",
        source: "water",
        paint: { "fill-color": theme.water },
      });
    }
    map.addSource("network", { type: "geojson", data: networkFC, promoteId: "id" });
    map.addSource("vehicles", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    map.addLayer({
      id: "network-casing",
      type: "line",
      source: "network",
      layout: { "line-cap": "round", "line-join": "round" },
      paint: {
        "line-color": theme.casing,
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
          -1, theme.noData,
          0, theme.stopped,
          0.35, theme.mid,
          0.7, theme.freeFlow,
          1.5, theme.freeFlow,
        ],
        "line-width": ["interpolate", ["linear"], ["zoom"], 11, 1.8, 14, 4, 17, 8],
      },
    });
    // Static overlays: ABOVE the road lines (added after them) but BELOW
    // vehicles/signal heads (added before them). Both sources are optional
    // — null when /overlay/ 404'd at startup. Styling deliberately quieter
    // than the congestion channel (theme.ts); zone kind/status arrive as
    // stamped properties (zkind/zrun, overlays.ts) because line-dasharray
    // is not data-driven in MapLibre.
    if (boundariesFC) {
      map.addSource("boundaries", { type: "geojson", data: boundariesFC });
      map.addLayer({
        id: "boundaries-line",
        type: "line",
        source: "boundaries",
        paint: {
          "line-color": theme.boundary,
          "line-opacity": 0.55,
          // Higher admin levels (county 6, township 7) read a touch heavier
          // than municipal (8).
          "line-width": ["match", ["get", "admin_level"], 6, 1.6, 7, 1.2, 0.9],
          "line-dasharray": [1.5, 2],
        },
      });
      // Zoom-gated like the signal heads (minzoom 13): municipality names
      // at city zoom are pure clutter.
      map.addLayer({
        id: "boundaries-labels",
        type: "symbol",
        source: "boundaries",
        minzoom: 13,
        layout: {
          "text-field": ["get", "name"],
          "text-font": ["open-sans-semibold"],
          "text-size": 11,
        },
        paint: {
          "text-color": theme.boundary,
          "text-halo-color": theme.bg,
          "text-halo-width": 1,
        },
      });
    }
    if (zonesFC) {
      map.addSource("zones", { type: "geojson", data: zonesFC });
      map.addLayer({
        id: "zones-fill",
        type: "fill",
        source: "zones",
        filter: ["==", ["get", "zkind"], "district"],
        paint: { "fill-color": theme.district, "fill-opacity": 0.05 },
      });
      // Runnable zones are solid and prominent; import-pending drops to a
      // muted trace (zrun, overlays.ts).
      map.addLayer({
        id: "zones-district",
        type: "line",
        source: "zones",
        filter: ["==", ["get", "zkind"], "district"],
        paint: {
          "line-color": theme.district,
          "line-opacity": ["case", ["==", ["get", "zrun"], 1], 0.85, 0.3],
          "line-width": ["interpolate", ["linear"], ["zoom"], 10, 1.4, 14, 2.2],
        },
      });
      map.addLayer({
        id: "zones-corridor",
        type: "line",
        source: "zones",
        filter: ["==", ["get", "zkind"], "corridor"],
        paint: {
          "line-color": theme.corridor,
          "line-opacity": ["case", ["==", ["get", "zrun"], 1], 0.8, 0.3],
          "line-width": ["interpolate", ["linear"], ["zoom"], 10, 1.4, 14, 2.2],
          "line-dasharray": [2.5, 1.5],
        },
      });
      map.addLayer({
        id: "zones-labels",
        type: "symbol",
        source: "zones",
        minzoom: 10,
        layout: {
          "text-field": ["get", "label"],
          "text-font": ["open-sans-semibold"],
          "text-size": ["interpolate", ["linear"], ["zoom"], 10, 11, 14, 14],
        },
        paint: {
          "text-color": [
            "match",
            ["get", "zkind"],
            "corridor",
            theme.corridor,
            theme.district,
          ],
          "text-halo-color": theme.bg,
          "text-halo-width": 1,
        },
      });
    }
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
        "icon-color": glyphByCls(1, theme).color,
        "icon-halo-color": theme.glyphHalo,
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
        "icon-color": ["match", ["get", "cls"], 0, glyphByCls(0, theme).color, glyphByCls(1, theme).color],
        "icon-halo-color": theme.glyphHalo,
        "icon-halo-width": 1,
      },
    });
    // Signal lights (M9): one head per signalized APPROACH (state-column +
    // geometry clustering, signals.ts) — the housing sprite carries three
    // dim lenses and one circle layer per lens position paints the active
    // light, gated by feature-state "sig" (off = no lit lens). The stop
    // bar line under each head (added below, same feature id) marks WHICH
    // lanes the head governs. Zoom-gated
    // to ≥13: at city zooms the heads blob into clutter that hides the
    // network itself (zoomed-out detail is the congestion channel's job).
    map.addSource("signals", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    const sigHeadImg = signalHeadImage(theme);
    map.addImage(SIGNAL_HEAD_IMAGE_ID, sigHeadImg.image, { pixelRatio: sigHeadImg.pixelRatio });
    // Stop bars: the "which approach" cue — one line across each head's
    // bound lanes, sharing the head's feature id, so the feature-state
    // color always matches a lens. Off = the dim-lens tone: the bar
    // still marks the signalized stop line when no lens is lit.
    map.addLayer({
      id: "signals-bars",
      type: "line",
      source: "signals",
      minzoom: 13,
      filter: ["==", ["get", "kind"], "bar"],
      paint: {
        "line-color": [
          "match",
          ["coalesce", ["feature-state", "sig"], "off"],
          "green",
          theme.signalGreen,
          "amber",
          theme.signalAmber,
          "red",
          theme.signalRed,
          theme.sigDim,
        ],
        "line-width": ["interpolate", ["linear"], ["zoom"], 13, 2, 17, 5],
        "line-opacity": 0.9,
      },
    });
    map.addLayer({
      id: "signals-housing",
      type: "symbol",
      source: "signals",
      minzoom: 13,
      filter: ["==", ["get", "kind"], "head"],
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
        filter: ["==", ["get", "kind"], "head"],
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
    // Stop signs (stopsign.ts): STATIC — the source is fully built here
    // (no setData/feature-state later). Same zoom gate and size curve as
    // the signal heads: at city zooms signs would blob into the same
    // clutter the heads are gated against.
    map.addSource("stops", {
      type: "geojson",
      data: {
        type: "FeatureCollection",
        features: stopSignPts.map((s) => ({
          type: "Feature",
          id: s.id,
          properties: { id: s.id },
          geometry: { type: "Point", coordinates: project(s.x, s.y) },
        })),
      },
    });
    const stopSignImg = stopSignImage(theme);
    map.addImage(STOP_SIGN_IMAGE_ID, stopSignImg.image, { pixelRatio: stopSignImg.pixelRatio });
    map.addLayer({
      id: "stop-signs",
      type: "symbol",
      source: "stops",
      minzoom: 13,
      layout: {
        "icon-image": STOP_SIGN_IMAGE_ID,
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
    applyLayerToggles(); // a legend click may have beaten the style load
  });

  hud.setConnection(false, `connecting to ${cfg.ws} …`);
  loadingMsg.textContent = `connecting to ${cfg.ws} …`;
  const snapSub = await subscribeSnapshots(
    cfg.ws,
    cfg.run,
    (data) => {
      try {
        const frame = decodeFrame(data);
        // Replay seek (replaypanel.ts): the stream jumps — BACKWARD to a
        // lower tick, or FORWARD past SeekGate's maxJump on a scrub-ahead.
        // Everything the pre-seek stream painted belongs to states the
        // seek abandoned; resetForSeek drops it (a lerp across the jump
        // would smear vehicles and derive bogus speeds, lanes keep stale
        // colors otherwise) and the new frame lands fresh. Duplicate ticks
        // (paused republication) are not seeks; push's own drop handles
        // them.
        if (seekGate.observe(frame.tick)) resetForSeek();
        buffer.push(frame, performance.now());
      } catch (err) {
        hud.setConnection(false, String(err));
      }
    },
    handleSigMessage,
    (connected, detail) => {
      hud.setConnection(connected, `${detail}  ·  run ${cfg.run}`);
      // The overlay covers the HUD until the first sample — mirror the
      // connection state into it or a pre-snapshot drop reads as a hang.
      if (!loadingHidden) {
        loadingMsg.textContent = connected
          ? "connected — waiting for the engine's first snapshot (world build) …"
          : `connection lost before the first snapshot — ${detail}`;
      }
      if (connected && wasConnected === false) {
        // Reconnect: the pull budget may have burned out while down, and
        // paused replay never rebroadcasts — re-arm and pull (sol review).
        sigReqRetries = 0;
        requestSig();
      }
      wasConnected = connected;
    },
  );
  natsConn = snapSub.nc;
  requestSig(); // attach-time pull (ADR-0016 §3): don't wait out a catch-up round

  // Startup watchdog: 20 s connected with no first sample usually means the
  // broker on the ws port isn't streaming THIS run (foreign engine, dead
  // child), not a slow world build — say so, but keep waiting and retrying
  // (non-fatal; a genuinely big world build just needs longer). ?bare=1
  // hides the overlay in CSS either way.
  setTimeout(() => {
    if (loadingHidden) return;
    loadingMsg.textContent =
      `still waiting — the engine for run ${cfg.run} isn't streaming yet; ` +
      `check that it's running (demos menu) — retrying …`;
  }, 20_000);

  // Congestion: recompute per-lane ratios at ~1 Hz (research: feature-state
  // updates are a per-snapshot cadence channel, not per render frame).
  let prevRatios = new Map<string, number>();
  let lastCongestionMs = 0;
  // clearCongestion wipes every painted lane ratio (a replay seek abandons
  // the states they describe): removeFeatureState with no key drops the
  // feature's whole state, so lanes fall back to the no-data color until
  // the post-seek stream repaints them.
  function clearCongestion(): void {
    if (mapReady) {
      for (const laneId of prevRatios.keys()) {
        map.removeFeatureState({ source: "network", id: laneId });
      }
    }
    prevRatios = new Map();
  }
  // resetForSeek drops EVERYTHING the pre-seek stream painted: the
  // interpolation buffer (a lerp across the jump would smear vehicles),
  // the congestion overlay's tick-derived feature-state (lanes keep stale
  // colors otherwise), and the inferred trailer poses (trucks keep their
  // ids across a seek — artic.ts). Shared by the SeekGate backstop (above)
  // and the panel's pre-seek hook (below).
  function resetForSeek(): void {
    buffer.reset();
    clearCongestion();
    artic.reset();
    prevSampleTick = null; // the landing tick must not kick the trailer
  }
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

  // Replay controls (replaypanel.ts); same probe discipline as the other
  // panels — hides unless demosrv's replay proxy answers, so plain live
  // demos never see it. Mounted HERE (not with the other panels) because
  // its two hooks reach into the stream pipeline declared above:
  //   onSeeking — the panel KNOWS when it seeks and fires BEFORE the POST
  //     (the player publishes the landing frame before acking it, so a
  //     post-ack reset could wipe that frame; a paused seek would show
  //     stale vehicles until the ~1 Hz republication). Covers the forward
  //     scrubs ≤ maxJump that SeekGate's heuristic misses; the gate stays
  //     as backstop for non-panel seeks;
  //   onStatus — the status dt is the RECORDED run's authoritative
  //     timestep (the ?dt= URL hint comes from the mutable scenario, and
  //     running-replay deep links carry no dt at all), so it wins on the
  //     first probe: currentDt is re-targeted and EVERY sim-math consumer
  //     follows (buffer speeds via setSimDt, the gate's forward-jump
  //     window, artic integration — both read currentDt). If it differs,
  //     speeds already rendered were off for one buffer-fill — acceptable;
  //     warn and re-target.
  void new ReplayPanel(document.getElementById("replay")!, {
    // The panel only ever controls the replay THIS page displays — every
    // probe/ctl binds cfg.run, so a stale deep link for another recording
    // 409s into the mismatch hint instead of adopting the active replay.
    expectedRun: cfg.run,
    onSeeking: resetForSeek,
    onStatus: (s) => {
      if (s.dt <= 0 || s.dt === currentDt) return; // dt is constant per run
      console.warn(
        `replay status dt ${s.dt} ≠ config dt ${currentDt} — ` +
          `adopting the recorded run's authoritative dt (speeds rendered so far were off)`,
      );
      currentDt = s.dt;
      legend.setDt(s.dt);
      buffer.setSimDt(s.dt);
      seekGate.setSimDt(s.dt);
    },
  }).init();

  // Render loop: interpolate behind the buffer, apply updateData diffs once
  // per rAF frame — never per incoming message.
  function frame(nowMs: number): void {
    if (mapReady) {
      const sample = buffer.sample(nowMs);
      if (sample) {
        hideLoading(); // first renderable sample — the engine is streaming
        // Trailer articulation integrates SIM time, not wall time: at 8×
        // replay the tractor advances 8 sim-seconds per wall second. The
        // sample's tick is the newer source snapshot's (snapshots.ts) —
        // its progression × currentDt is the sim-elapsed for this render
        // frame (0 while the buffer holds one tick; clamped ≥ 0;
        // resetForSeek re-arms prevSampleTick so a seek never kicks the
        // trailer). currentDt, not cfg.dt: the recorded run's dt may have
        // replaced the URL hint (onStatus above).
        const dtS =
          prevSampleTick === null ? 0 : Math.max(0, sample.tick - prevSampleTick) * currentDt;
        prevSampleTick = sample.tick;
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
