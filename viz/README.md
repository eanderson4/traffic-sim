# traffic-sim viz (M6, M9)

MapLibre GL realtime client for the engine's live plane (ADR-0003: MapLibre-first,
vanilla TypeScript, no UI framework; ADR-0006 §8: browsers over the server's
WebSocket listener with binary frames).

Renders the I-280 @ Woodside corridor live: static lane network, animated
vehicles from TSSF v1 binary snapshots at 10 Hz (interpolated to 60 fps behind
a ~250 ms buffer), a clearly-labelled **client-derived** congestion proxy
(per-lane mean speed) painted onto the lanes via feature-state, and the two
signalized junctions' lights (TSSG v1 program table, states derived from the
snapshot tick — ADR-0006 M9 addendum) as stop-line circles.

## Prerequisites

- pnpm (via corepack or standalone), node ≥ 22.18 (type stripping runs the
  tests directly)
- Go ≥ 1.25 for the engine
- The compiled network at `data/networks/i280-woodside/i280.json` (git-ignored;
  regenerate per `contracts/network-format-v1.md` if missing)

## Run the demo (two terminals)

```sh
# 1. Engine serve mode: live run + WebSocket listener + network GeoJSON export
cd engine
go run ./cmd/serve -netfile ../data/networks/i280-woodside/i280.json \
  -run demo -ws 127.0.0.1:8443 -geojson ../viz/public/network.geojson

# 2. Viz dev server
cd viz
pnpm install
pnpm dev        # vite, default port 5173
```

Open http://localhost:5173/?run=demo&ws=ws://127.0.0.1:8443

URL parameters (all optional):

| param | default | meaning |
|---|---|---|
| `run` | `demo` | run id; subscribes `ts.{run}.state.snap` |
| `ws` | `ws://<host>:8443` | engine WebSocket listener |
| `net` | `/network.geojson` | static network GeoJSON URL |
| `buffer` | `250` | interpolation buffer (ms) |
| `theme` | `navy` | palette from `src/theme.ts` THEMES (`navy` \| `paper`); the HUD/menu toggle writes `localStorage["viz-theme"]`, which wins when no param is given |
| `bare` | off | `?bare=1` hides all HUD chrome + the loading overlay (clean-canvas screenshots) |

`serve` flags: `-ticks` (default 36000 = 1 h at the 100 ms tick), `-seed`,
`-rate` (veh/h per origin lane), `-density`, `-driver` (in-process default
driver, default on), `-capacity`.

## What you should see

- Navy canvas, lane polylines with dark casing; the map fits the corridor.
- Pale dots = cars moving along lanes (all class 0 in the default scenario;
  non-car classes render larger and orange). Click a dot → id/class/speed
  overlay (speed is client-derived — TSSF v1 carries no speed field).
- Lanes with traffic tint green→gold→red by mean speed / speed limit
  (feature-state); untravelled lanes stay blue ("no data"). HUD top-left:
  connection, tick, vehicle count.
- Small circles at the two metered junctions' stop lines cycle
  green → amber → red in step with their fixed-time programs (82 s / 3 s /
  5 s on I-280); nothing renders where a signal is off.

## Architecture (four channels, per docs/kb/raw/integration-maplibre-realtime)

1. **Static network** — `network.geojson` (exported by `engine/cmd/serve
   -geojson`; engine/geojson.go) loaded once with `promoteId: "id"`.
   Coordinates are the network's **local metric frame**; the `"frame"`
   foreign member carries `projection`+`netOffset`, and `src/proj.ts`
   (inverse UTM) projects to WGS84 at load. This source is NEVER setData'd.
2. **Vehicles** — TSSF v1 frames decoded in `src/tssf.ts` (DataView, mirror
   of engine/natsio/frame.go), buffered in `src/snapshots.ts` (250 ms, lerp,
   no extrapolation), applied once per rAF as `updateData` diffs keyed by
   vehicle id (`src/vehicles.ts`) on a dedicated small source.
3. **Congestion** — `src/congestion.ts`: vehicles re-attached to lanes by a
   grid spatial index (TSSF v1 carries no lane id), per-lane mean
   speed/limit → `setFeatureState` at ~1 Hz. Client-derived estimate; the
   future observability ADR replaces it with authoritative metrics.
4. **Signals** (M9) — TSSG v1 program table off `ts.{run}.state.sig`,
   decoded in `src/tssg.ts` (mirror of engine/natsio/sigframe.go). The wire
   carries the fixed-time PROGRAMS, never states: light state is a pure
   function of the tick (ADR-0011 §1), so `src/signals.ts` derives per-lane
   colors at each render tick and paints stop-line circles (first point of
   each signal-bound internal lane) on a tiny dedicated source via
   `setFeatureState` — setData only when a table's lane binding changes.
   Late joiners converge on the table's keyframe-cadence republication.

## Verification

```sh
pnpm check      # tsc --noEmit, strict
pnpm test       # node --test: decoders (Go-golden fixtures), projection
                # (PROJ reference), interpolation, diffs, congestion index,
                # signal derivation
pnpm smoke      # no browser: builds cmd/serve, runs i280 live, connects via
                # nats.ws from node, asserts decodable in-bounds frames
pnpm sigsmoke   # M9: asserts the TSSG table, derived junction states flipping
                # at ticks 820/850/900, and late-joiner convergence (~2 min)
node scripts/screenshot.mjs   # headless-Chrome screenshot + HUD readback
                              # (needs google-chrome/chromium; env CHROME=)
node scripts/sigshot.mjs      # M9 headless-Chrome render proof: stop-line
                              # feature-state flips green→amber→red + shots
```

Verified: type-check, 34 unit tests, the node smoke test, and a headless-
Chrome end-to-end render (live ticks, vehicles, congestion tint, click
inspect). Not covered: visual cross-browser testing, fleet sizes beyond a
few hundred (the escalation ladder's microbenchmark remains an open task).
