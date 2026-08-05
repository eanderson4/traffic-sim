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
import { parseOverlay, prepareBuildings, prepareZones } from "./overlays.ts";
import { decodeFrame } from "./tssf.ts";
import { decodeSignalFrame, parseSigChunkHeader, SignalTableAccumulator, type SigColor, type SignalTable } from "./tssg.ts";
import { headStatesAtTick, signalHeads, type SignalHead } from "./signals.ts";
import { SIGNAL_HEAD, SIGNAL_HEAD_IMAGE_ID, signalHeadImage } from "./signalhead.ts";
import { STOP_SIGN_IMAGE_ID, stopSignImage, stopSigns, type StopSign } from "./stopsign.ts";
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
import {
  BAKED_VEHICLE_GATE_ZOOM,
  classifyOverlay,
  loadBakedIndex,
  resolveBakedUrl,
  subscribeBaked,
  type BakedIndex,
  type BakedSubscription,
} from "./baked.ts";
import { furnitureHeads, parseFurniture, type BakedFurniture } from "./furniture.ts";
import { Protocol as PMTilesProtocol } from "pmtiles";
import type { MsgHdrs, NatsConnection } from "nats.ws";
import { Hud } from "./status.ts";
import { Legend } from "./legend.ts";
import { DEFAULT_TOGGLES, layerOpsFor, type ToggleState } from "./layertoggles.ts";
import { DemoSwitcher, demoIdFromNetUrl } from "./switcher.ts";
import { ModelPanel } from "./modelpanel.ts";
import { StatsPanel } from "./statspanel.ts";
import { FlowPanel } from "./flowpanel.ts";
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
  const cfg = loadConfig(location.search, location.hostname, location.protocol);
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
  loadingMsg.textContent =
    cfg.bake !== null ? "loading baked replay index …" : `loading network ${cfg.networkUrl} …`;

  // ?bare=1: clean-canvas mode for screenshots — CSS hides the HUD chrome
  // and the loading overlay (which otherwise waits on a live engine).
  if (cfg.bare) document.body.classList.add("bare");

  // Baked replay (ADR-0023): ?bake= selects the shim. Startup reads the
  // index manifest (§5/§6.5) — projector from its frame descriptor, map
  // fit from its bounds (replacing the geometry walk), network from its
  // pmtiles/geojson entry, overlays from its overlay list; the chunked
  // netload.ts path is skipped entirely on the PMTiles path.
  const bakedIndex: BakedIndex | null = cfg.bake !== null ? await loadBakedIndex(cfg.bake) : null;
  const bakedPmtiles = bakedIndex?.network.pmtiles !== undefined;
  const netSourceLayer = bakedPmtiles ? (bakedIndex?.network.layer ?? "lanes") : null;

  // Network GeoJSON — chunked manifests (netload.ts) are reassembled
  // here, so everything downstream sees one plain collection. Baked mode
  // loads it only for a small-network GeoJSON bake (ADR-0023 §7: e.g.
  // i280's 375 lanes need no tiles) — the PMTiles path ships no lane
  // geometry to the browser at all.
  const netUrl =
    bakedIndex === null
      ? cfg.networkUrl
      : bakedIndex.network.geojson !== undefined
        ? resolveBakedUrl(cfg.bake!, bakedIndex.network.geojson)
        : null;
  const net = netUrl !== null ? await fetchNetwork(netUrl) : null;
  if (net !== null) {
    if (net.type !== "FeatureCollection" || !Array.isArray(net.features)) {
      throw new Error(`${netUrl}: not a FeatureCollection`);
    }
    if (!net.frame) throw new Error(`${netUrl}: missing "frame" descriptor`);
  }
  const frameDesc = net?.frame ?? bakedIndex?.frame;
  if (frameDesc === undefined) throw new Error("no frame descriptor (network GeoJSON or baked index)");
  const project = makeProjector(frameDesc);

  // Optional static overlays (zones + admin boundaries): already WGS84
  // lon/lat, so unlike the network they go to MapLibre unprojected. A 404
  // (demos without overlays) or a garbage doc just means "no overlay" —
  // parseOverlay/prepareZones (overlays.ts) also stamp the style
  // classification (zkind/zrun) that the zone layers filter on. Baked mode
  // reads the manifest's overlay list (§5: bake copies the demo's
  // /overlay/* set; absent = no overlays, exactly the live 404 semantic).
  const overlayUrl = (kind: "zones" | "boundaries" | "water" | "buildings"): string | null => {
    if (bakedIndex === null) {
      return {
        zones: cfg.zonesUrl,
        boundaries: cfg.boundariesUrl,
        water: cfg.waterUrl,
        buildings: cfg.buildingsUrl,
      }[kind];
    }
    for (const u of bakedIndex.overlays ?? []) {
      if (classifyOverlay(u) === kind) return resolveBakedUrl(cfg.bake!, u);
    }
    return null;
  };
  const fetchOverlay = async (url: string | null): Promise<FeatureCollection | null> => {
    if (url === null) return null;
    try {
      const res = await fetch(url);
      if (!res.ok) return null;
      return parseOverlay(await res.json());
    } catch {
      return null;
    }
  };
  const [zonesFC, boundariesFC, waterFC, buildingsFC] = await Promise.all([
    fetchOverlay(overlayUrl("zones")).then((fc) => (fc === null ? null : prepareZones(fc))),
    fetchOverlay(overlayUrl("boundaries")),
    fetchOverlay(overlayUrl("water")),
    fetchOverlay(overlayUrl("buildings")).then((fc) => (fc === null ? null : prepareBuildings(fc))),
  ]);

  // Static channel: project lane polylines once (local metric → WGS84).
  // Engine export guarantees [x, y] pairs (engine/geojson.go) — validated.
  // Skipped on the PMTiles path (no lane geometry in the browser); in
  // baked GeoJSON mode edgeB is still derived client-side (§7's single
  // rendering contract), but the congestion/signal geometry indexes are
  // LIVE-mode only (§6.1/§6.2: TSRL + furniture replace them).
  let networkFC: FeatureCollection | null = null;
  let laneIndex: LaneIndex | null = null;
  const speedLimitByLane = new Map<string, number>();
  const shapeByLane = new Map<string, Array<[number, number]>>();
  let stopSignPts: StopSign[] = [];
  if (net !== null) {
    const pair = (c: number[]): [number, number] => {
      if (c.length < 2 || c[0] === undefined || c[1] === undefined) {
        throw new Error(`${netUrl}: malformed coordinate ${JSON.stringify(c)}`);
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
    networkFC = { type: "FeatureCollection", features: lanes };
    if (bakedIndex === null) {
      // Congestion inputs stay in the metric frame (vehicle positions arrive
      // metric; nearest-lane matching happens there).
      laneIndex = new LaneIndex(
        net.features.map((f) => ({
          id: String(f.id ?? f.properties?.["id"]),
          shape: f.geometry.coordinates as Array<[number, number]>,
        })),
      );
      for (const f of net.features) {
        speedLimitByLane.set(String(f.id ?? f.properties?.["id"]), Number(f.properties?.["speedLimit"]));
      }
      // Signal stop-lines resolve against the static geometry (local frame).
      for (const f of net.features) {
        shapeByLane.set(String(f.id ?? f.properties?.["id"]), f.geometry.coordinates as Array<[number, number]>);
      }
      // Stop signs (stopsign.ts, ADR-0010 row/junction lane properties): one
      // sign per stop-controlled approach cluster, resolved ONCE from the
      // static geometry — no feature-state channel, no per-tick update.
      stopSignPts = stopSigns(
        net.features.map((f) => ({
          id: String(f.id ?? f.properties?.["id"]),
          row: String(f.properties?.["row"] ?? ""),
          junction: String(f.properties?.["junction"] ?? ""),
          shape: f.geometry.coordinates as Array<[number, number]>,
        })),
      );
    }
  }

  // Map fit: the index's bounds in baked mode (§5 — replaces the geometry
  // walk, which needs lane geometry the PMTiles path never fetches).
  let bounds = new maplibregl.LngLatBounds();
  if (bakedIndex !== null) {
    bounds = new maplibregl.LngLatBounds(
      [bakedIndex.bounds[0], bakedIndex.bounds[1]],
      [bakedIndex.bounds[2], bakedIndex.bounds[3]],
    );
  } else if (networkFC !== null) {
    for (const f of networkFC.features) {
      for (const c of (f.geometry as LineString).coordinates) bounds.extend(c as [number, number]);
    }
  }

  // Baked furniture (§6.2): signal heads, stop bars, and stop signs arrive
  // pre-derived in furniture.geojson (metric coords — the browser holds no
  // lane geometry to derive them from on the PMTiles path). Heads join to
  // TSSG programs by the baked binding when the table lands
  // (ensureSignalSource); stop signs are static like the live path's.
  let furniture: BakedFurniture | null = null;
  if (bakedIndex !== null && bakedIndex.furniture !== undefined) {
    try {
      const res = await fetch(resolveBakedUrl(cfg.bake!, bakedIndex.furniture));
      if (res.ok) {
        furniture = parseFurniture((await res.json()) as FeatureCollection);
        stopSignPts = furniture.signs;
      }
    } catch {
      furniture = null; // a bake without furniture renders without heads/signs
    }
  }

  // PMTiles network source (§7): MapLibre 5.24 bundles no pmtiles
  // protocol — register the official companion package. The archive is
  // served IDENTITY (§8's deployment contract: the client reads it by
  // Range-requested byte offsets, so no Content-Encoding on the object).
  if (bakedPmtiles) {
    const protocol = new PMTilesProtocol();
    maplibregl.addProtocol("pmtiles", protocol.tile);
  }

  // A ?center= deep link REPLACES the bounds fit — MapLibre lets bounds win
  // when both are supplied, so the two are mutually exclusive by
  // construction, not by option precedence. Without a camera param this is
  // byte-for-byte the previous behaviour.
  const cam = cfg.camera;
  const map = new maplibregl.Map({
    container: "map",
    style: blankStyle,
    ...(cam !== null
      ? { center: cam.center, zoom: cam.zoom, bearing: cam.bearing, pitch: cam.pitch }
      : { bounds, fitBoundsOptions: { padding: 40 } }),
    attributionControl: false,
  });
  map.addControl(new maplibregl.NavigationControl({ showCompass: false }), "bottom-right");

  // Run statistics (statspanel.ts): the standard run report — ADR-0030
  // schema v1, written by scripts/runreport.py --json. Mounted here rather
  // than with the other panels because the hotspot rows fly the map: the
  // report's coordinates are the ENGINE's local metric frame, so they go
  // through the same projector the network did. Reports live in gitignored
  // run directories, hence the drop target on the whole document — a
  // report from any run can be inspected without serving it first. Hides
  // itself when ?report= (default /sample-runreport.json) 404s, like every
  // other optional panel.
  void new StatsPanel(document.getElementById("stats")!, {
    url: cfg.reportUrl,
    dropTarget: document.body,
    onHotspot: (h) => {
      // Zoom 16: close enough to see the queue on the lane, wide enough to
      // keep its upstream approach in frame.
      map.flyTo({ center: project(h.x, h.y), zoom: Math.max(map.getZoom(), 16) });
    },
  }).init();

  // Flow (flowpanel.ts): arrivals, departures and the accumulation between
  // them, from a mkflowcurve.py document. Its marker is driven by the
  // SNAPSHOT tick below rather than by polling the replay control plane, so
  // it follows play, pause, speed and seek with no extra machinery — and it
  // works identically on a live run, which has no control plane to poll.
  const flowPanel = new FlowPanel(document.getElementById("flow")!, { url: cfg.flowUrl });
  void flowPanel.init();

  // currentDt is THE sim timestep for every viz-side sim-math consumer
  // (buffer speeds, seek-gate threshold, artic integration). It starts at
  // the ?dt= URL hint and is REPLACED by the recorded run's authoritative
  // dt on the replay panel's first status probe (the onStatus hook below)
  // — a direct/stale deep link must not leave any consumer on the hint.
  // Baked mode starts on the index's dt directly (§6: same authority).
  let currentDt = bakedIndex?.dt ?? cfg.dt;
  const buffer = new SnapshotBuffer(cfg.bufferMs, currentDt);
  // Baked mode sizes the interpolation buffer to the delivery cadence
  // (ADR-0023 §2: at 2 Hz the 500 ms frame interval exceeds the 250 ms
  // default, so the buffer tracks 1.25 × frameInterval / speed — 625 ms
  // at 1×). The panel's onStatus hook re-sizes on speed changes.
  if (bakedIndex !== null) {
    buffer.setBufferMs(Math.max(250, 1.25 * bakedIndex.bakeEveryTicks * bakedIndex.dt * 1000));
  }
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
    // Baked mode joins pre-derived furniture heads to the table's programs
    // by the baked binding (§6.2); the live path derives heads from the
    // static lane geometry (signals.ts).
    const heads = furniture !== null ? furnitureHeads(furniture, sigTable) : signalHeads(sigTable, shapeByLane);
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
    // Buildings SECOND: on top of the water fill (they stand on land) but
    // still under every road layer — footprints are context, they must
    // never hide the network. Source is added even when the overlay 404'd
    // (EMPTY_FC) so the layer ids layertoggles.ts names always exist.
    //
    // Zoom-gated to >=14 like the signal heads / junction interiors (WQ-1,
    // WQ-3): chi-loop alone is 42k polygons and at city zoom they are a
    // solid smear over the roads. A fill (not fill-extrusion) because the
    // camera is top-down — extrusion draws in a separate 3D pass that
    // cannot be ordered under line layers, which is exactly the property
    // this layer needs.
    map.addSource("buildings", {
      type: "geojson",
      data: buildingsFC ?? EMPTY_FC,
    });
    // Kind → hue (residential vs workplace is the point of the layer);
    // both fill and outline read the same expression so they can't drift.
    const buildingColor: maplibregl.ExpressionSpecification = [
      "match",
      ["get", "bkind"],
      "residential", theme.buildingResidential,
      "workplace", theme.buildingWorkplace,
      theme.buildingOther,
    ];
    // Storeys drive opacity, so a 60-storey tower reads as a tower on a
    // flat map: 1 storey barely tints, 40+ is a solid block. Scaled down at
    // the minzoom boundary so the layer fades in instead of popping — and
    // written as ONE top-level zoom interpolate whose stop outputs are the
    // storey ramp, because MapLibre rejects ["zoom"] anywhere but as the
    // direct input of a top-level step/interpolate (so the obvious
    // ["*", zoomRamp, storeyRamp] does not validate).
    const storeyOpacity = (scale: number): maplibregl.ExpressionSpecification =>
      [
        "interpolate", ["linear"], ["to-number", ["get", "blevels"], 1],
        1, 0.16 * scale,
        4, 0.28 * scale,
        20, 0.55 * scale,
        40, 0.72 * scale,
      ] as maplibregl.ExpressionSpecification;
    const buildingWeight: maplibregl.ExpressionSpecification = [
      "interpolate", ["linear"], ["zoom"],
      14, storeyOpacity(0.35),
      15.5, storeyOpacity(1),
    ];
    map.addLayer({
      id: "buildings-fill",
      type: "fill",
      source: "buildings",
      minzoom: 14,
      paint: { "fill-color": buildingColor, "fill-opacity": buildingWeight },
    });
    // Outline only once individual footprints are worth resolving —
    // at z14 it would just thicken the smear.
    map.addLayer({
      id: "buildings-outline",
      type: "line",
      source: "buildings",
      minzoom: 16,
      paint: {
        "line-color": buildingColor,
        "line-opacity": ["interpolate", ["linear"], ["zoom"], 16, 0, 17, 0.5],
        "line-width": 0.8,
      },
    });
    // The network source: PMTiles vector tiles in baked city-scale mode
    // (§7 — promoteId "id" makes tile-split pieces of one lane share its
    // feature-state), GeoJSON otherwise (live, and small-network bakes).
    if (netSourceLayer !== null && bakedIndex?.network.pmtiles !== undefined) {
      map.addSource("network", {
        type: "vector",
        url: `pmtiles://${bakedIndex.network.pmtiles}`,
        promoteId: bakedIndex.network.promoteId,
      });
    } else {
      map.addSource("network", { type: "geojson", data: networkFC ?? EMPTY_FC, promoteId: "id" });
    }
    map.addSource("vehicles", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    // Lane paint shared by the external and internal layer pairs below so
    // the two can't drift (WQ-3). Junction interiors (internal:true) are
    // zoom-gated to >=13 like the signal heads: below that they're squiggle
    // clutter at every junction. External lanes draw at all zooms.
    const casingPaint: maplibregl.LineLayerSpecification["paint"] = {
      "line-color": theme.casing,
      // Casing on edge-group boundaries only (edgeB, edges.ts): the
      // outer shell of each road. Interior lanes keep a faint trace so
      // adjacent stripes stay separable. Lanes without an edge group
      // (junction interiors — now the internal pair below — and stale
      // caches) are all boundaries, so they degrade to full casing inside
      // edgeBoundaries.
      "line-opacity": ["case", ["boolean", ["get", "edgeB"], true], 0.9, 0.15],
      // Zoomed-out legibility: a touch wider from z=11 (interpolating up
      // to z=14) — the network must read under the vehicle stream before
      // any detail matters.
      "line-width": ["interpolate", ["linear"], ["zoom"], 11, 3.2, 14, 7, 17, 12],
    };
    const linePaint: maplibregl.LineLayerSpecification["paint"] = {
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
    };
    const laneLayout: maplibregl.LineLayerSpecification["layout"] = {
      "line-cap": "round",
      "line-join": "round",
    };
    const externalOnly: maplibregl.FilterSpecification = ["!=", ["get", "internal"], true];
    const internalOnly: maplibregl.FilterSpecification = ["==", ["get", "internal"], true];
    // Vector (PMTiles) layers MUST name their source-layer (§6.3); GeoJSON
    // sources must not carry one. One spread keeps the two paths in lockstep.
    const srcLayer = netSourceLayer !== null ? { "source-layer": netSourceLayer } : {};
    // Order matters: BOTH casings below BOTH congestion lines (review
    // round 1) — an internal casing's round end-cap is wider than the
    // line and would overpaint the external lane's congestion color at
    // every junction mouth if it sat above network-line.
    map.addLayer({
      id: "network-casing",
      type: "line",
      source: "network",
      ...srcLayer,
      filter: externalOnly,
      layout: laneLayout,
      paint: casingPaint,
    });
    map.addLayer({
      id: "network-internal-casing",
      type: "line",
      source: "network",
      ...srcLayer,
      minzoom: 13,
      filter: internalOnly,
      layout: laneLayout,
      paint: casingPaint,
    });
    map.addLayer({
      id: "network-line",
      type: "line",
      source: "network",
      ...srcLayer,
      filter: externalOnly,
      layout: laneLayout,
      paint: linePaint,
    });
    map.addLayer({
      id: "network-internal-line",
      type: "line",
      source: "network",
      ...srcLayer,
      minzoom: 13,
      filter: internalOnly,
      layout: laneLayout,
      paint: linePaint,
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
    // the hitch, which reads as the pivot joint. Baked mode gates both
    // vehicle layers to z13 (§6.4 — the zoom at which the last road class
    // appears, so dots never float over invisible streets); below the gate
    // the shim schedules no region fetches and emits clock-only frames.
    const vehicleGate = bakedIndex !== null ? { minzoom: BAKED_VEHICLE_GATE_ZOOM } : {};
    map.addSource("trailers", { type: "geojson", data: EMPTY_FC, promoteId: "id" });
    map.addLayer({
      id: "trailers",
      type: "symbol",
      source: "trailers",
      ...vehicleGate,
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
      ...vehicleGate,
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

  // Baked mode viewport → shim (§6.4/§4): at/above the z13 gate the shim
  // subscribes the viewport's region set and refetches on pan (debounced);
  // below it, no vehicle fetches at all. Crossing BELOW the gate also
  // CLEARS the applied vehicle diff state — today nothing ever hides the
  // layer, so without this the last fetched frame would freeze on screen.
  let bakedSub: BakedSubscription | null = null;
  let aboveVehicleGate = false;
  const reportViewport = (): void => {
    if (bakedSub === null) return;
    const b = map.getBounds();
    bakedSub.setViewport([b.getWest(), b.getSouth(), b.getEast(), b.getNorth()], map.getZoom());
    const above = map.getZoom() >= BAKED_VEHICLE_GATE_ZOOM;
    if (above === aboveVehicleGate) return;
    aboveVehicleGate = above;
    if (!above && mapReady) {
      (map.getSource("vehicles") as maplibregl.GeoJSONSource).updateData({ removeAll: true });
      (map.getSource("trailers") as maplibregl.GeoJSONSource).updateData({ removeAll: true });
      applied.clear();
      appliedTrailers.clear();
      artic.reset();
      prevSampleTick = null; // the post-gate frame must not kick the trailer
    }
  };
  let viewportTimer: number | null = null;
  map.on("moveend", () => {
    if (viewportTimer !== null) window.clearTimeout(viewportTimer);
    viewportTimer = window.setTimeout(() => {
      viewportTimer = null;
      reportViewport();
    }, 200);
  });

  hud.setConnection(false, cfg.bake !== null ? "loading baked replay …" : `connecting to ${cfg.ws} …`);
  loadingMsg.textContent = cfg.bake !== null ? "loading baked replay …" : `connecting to ${cfg.ws} …`;
  const onSnapFrame = (data: Uint8Array): void => {
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
  };
  const onSnapStatus = (connected: boolean, detail: string): void => {
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
  };
  // Baked mode (ADR-0023 §6): the shim replaces the NATS transport —
  // synthetic TSSF/TSSG from static artifacts, nc null (the ADR-0016 pull
  // below no-ops on its guard), the control plane served by its FetchLike
  // stub (injected into the ReplayPanel below).
  const snapSub =
    cfg.bake !== null
      ? (bakedSub = await subscribeBaked(cfg.bake, onSnapFrame, handleSigMessage, onSnapStatus))
      : await subscribeSnapshots(cfg.ws, cfg.run, onSnapFrame, handleSigMessage, onSnapStatus).catch((err) => {
          // The failure is known to be the engine connection, so the
          // guidance can name the target and the fallback without
          // misdiagnosing an unrelated startup error. /quiz.html is the
          // route that works under every static server (Pages pretty-URLs
          // it to /quiz; the local serve-baked does no rewrite).
          const why = err instanceof Error ? err.message : String(err);
          throw new Error(
            `cannot reach a live engine at ${cfg.ws} (${why}) — this page drives a live connection; the recorded scenarios are at /quiz.html`,
          );
        });
  natsConn = snapSub.nc;
  requestSig(); // attach-time pull (ADR-0016 §3): don't wait out a catch-up round
  reportViewport(); // the shim's first region set + vehicle-gate state

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
        map.removeFeatureState(netTarget(laneId));
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
  // netTarget builds a lane's feature-state identifier: PMTiles (vector)
  // sources REQUIRE sourceLayer (§6.3 — omitting it is fine for GeoJSON,
  // mandatory for vector); GeoJSON sources must not carry one.
  const netTarget = (laneId: string): maplibregl.FeatureIdentifier =>
    netSourceLayer !== null
      ? { source: "network", sourceLayer: netSourceLayer, id: laneId }
      : { source: "network", id: laneId };
  function updateCongestion(nowMs: number): void {
    if (!mapReady || nowMs - lastCongestionMs < 1000) return;
    lastCongestionMs = nowMs;
    const sample = buffer.sample(nowMs);
    if (!sample) return;
    if (bakedSub !== null) {
      // Baked mode (§6.1): the TSRL aggregate stream REPLACES the client
      // derivation (§4: instantaneous per-lane mean speed/limit read from
      // engine state at bake time — authoritative, and available at every
      // zoom; lane coloring ALWAYS reads TSRL). Diffed like the live path.
      const ratios = bakedSub.laneRatiosAt(sample.tick);
      if (ratios === null) return; // lanes.json/chunks not landed — keep the previous paint
      for (const [laneId, ratio] of ratios) {
        if (prevRatios.get(laneId) !== ratio) {
          map.setFeatureState(netTarget(laneId), { ratio });
        }
      }
      for (const laneId of prevRatios.keys()) {
        if (!ratios.has(laneId)) map.removeFeatureState(netTarget(laneId));
      }
      prevRatios = ratios;
      return;
    }
    if (laneIndex === null) return; // unreachable live (index built above), keeps TS honest
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
        map.setFeatureState(netTarget(laneId), { ratio });
      }
    }
    for (const laneId of prevRatios.keys()) {
      if (!ratios.has(laneId)) map.removeFeatureState(netTarget(laneId));
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
    // Baked mode (§6): the shim's FetchLike stub answers the panel's exact
    // /api/replay/* routes — the panel code is unchanged, real fetch is
    // never consulted.
    ...(bakedSub !== null ? { fetchFn: bakedSub.fetchFn } : {}),
    onSeeking: resetForSeek,
    onStatus: (s) => {
      // Baked mode (§2): the interpolation buffer tracks the delivery
      // cadence × playback speed — max(250, 1.25 × frameInterval/speed),
      // 625 ms at the 2 Hz bake at 1×.
      if (bakedIndex !== null && s.dt > 0 && s.speed > 0) {
        buffer.setBufferMs(Math.max(250, (1.25 * bakedIndex.bakeEveryTicks * s.dt * 1000) / s.speed));
      }
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
        flowPanel.setTick(sample.tick);
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
