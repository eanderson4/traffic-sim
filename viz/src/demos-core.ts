// demos-core.ts — pure, DOM-free helpers for the demosrv menu page
// (demos.ts) and the in-map switcher (switcher.ts). Kept separate so
// node --test can import them exactly like theme.ts/proj.ts: no fetch, no
// document.

// EntryKind is demosrv's output-only discriminator (engine/cmd/demosrv
// registry.go — Load overwrites whatever the JSON carried, so the API
// payload always has it): "demo" spawns serve, "replay" spawns the replay
// driver over a recorded store.
export type EntryKind = "demo" | "replay";

// DemoInfo mirrors one entry of the demosrv registry (demos.json — the Go
// type Demo in engine/cmd/demosrv/registry.go). seed/ticks are optional
// scenario-manifest overrides.
export interface DemoInfo {
  id: string;
  title: string;
  blurb: string;
  tags: string[];
  scenarioDir: string;
  run: string;
  seed?: number;
  ticks?: number;
  kind: EntryKind; // always "demo" in API payloads
}

// RecordingInfo mirrors one "recordings" entry (the Go type Recording): a
// durable JetStream store replayed under the live plane {run}-replay. No
// tags/seed/ticks — a recording replays its store as written; scenarioDir
// is reused for the network GeoJSON (ids are unique ACROSS kinds, they
// share /net/{id}).
export interface RecordingInfo {
  id: string;
  title: string;
  blurb: string;
  store: string;
  run: string;
  scenarioDir: string;
  kind: EntryKind; // always "replay" in API payloads
}

// MenuEntry is anything the menu/switcher can activate.
export type MenuEntry = DemoInfo | RecordingInfo;

// RunStatus mirrors demosrv's GET /api/status payload; active is the
// running child's id (demo OR recording — the supervisor is generic over
// kinds), or null when no engine is up.
export interface RunStatus {
  active: string | null;
  run?: string; // live run id — unique per spawn for demos (demosrv)
  pid: number;
  startedAt?: string;
}

// buildAppURL builds the live-map deep link for a demo — the SAME shape
// demosrv returns from POST /api/demo/{id}/start (engine/cmd/demosrv —
// they must agree; demos.test.ts pins the shape on this side). The menu
// uses it for the already-running card, which navigates straight to the
// map without a start round-trip. Registry-validated single tokens
// ([A-Za-z0-9_-]+ ids, NATS-token runs) need no escaping. ws comes from
// the /api/demos payload: it pins the engine port this demosrv spawns to
// (non-default when another process holds 8443); absent, the viz default
// in config.ts applies.
export function buildAppURL(demo: Pick<DemoInfo, "id" | "run">, ws?: string): string {
  const base = `/app/?run=${demo.run}&net=/net/${demo.id}.geojson`;
  return ws ? `${base}&ws=${encodeURIComponent(ws)}` : base;
}

// buildReplayURL is the already-running RECORDING card's deep link — the
// shape demosrv returns from POST /api/replay/{id}/start MINUS the dt hint
// (the registry carries no dt; config.ts's 0.1 default applies). A
// NON-running recording never takes this path: its activation POSTs
// startPath and navigates on the response, which carries the dt.
export function buildReplayURL(rec: Pick<RecordingInfo, "id" | "run">, ws?: string): string {
  const base = `/app/?run=${rec.run}-replay&net=/net/${rec.id}.geojson`;
  return ws ? `${base}&ws=${encodeURIComponent(ws)}` : base;
}

// deepLinkURL picks the running-card navigation URL by kind. liveRun
// overrides the registry run id: live demos spawn with a per-spawn unique
// run id (demosrv), so the running card must use the id /api/status
// reports, not the registry's.
export function deepLinkURL(entry: Pick<MenuEntry, "id" | "run" | "kind">, ws?: string, liveRun?: string): string {
  if (entry.kind === "replay") return buildReplayURL(entry, ws);
  return buildAppURL({ id: entry.id, run: liveRun ?? entry.run }, ws);
}

// startPath routes an activation POST by kind: demos spawn serve,
// recordings spawn the replay driver (engine/cmd/demosrv main.go — the
// routes are distinct, the {url} response shape is the same).
export function startPath(entry: Pick<MenuEntry, "id" | "kind">): string {
  return entry.kind === "replay" ? `/api/replay/${entry.id}/start` : `/api/demo/${entry.id}/start`;
}
