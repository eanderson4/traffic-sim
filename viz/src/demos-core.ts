// demos-core.ts — pure, DOM-free helpers for the demosrv menu page
// (demos.ts). Kept separate so node --test can import them exactly like
// theme.ts/proj.ts: no fetch, no document.

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
}

// RunStatus mirrors demosrv's GET /api/status payload; active is the
// running demo's id, or null when no engine is up.
export interface RunStatus {
  active: string | null;
  pid: number;
  startedAt?: string;
}

// buildAppURL builds the live-map deep link for a demo — the SAME shape
// demosrv returns from POST /api/demo/{id}/start (engine/cmd/demosrv —
// they must agree; demos.test.ts pins the shape on this side). The menu
// uses it for the already-running card, which navigates straight to the
// map without a start round-trip. Registry-validated single tokens
// ([A-Za-z0-9_-]+ ids, NATS-token runs) need no escaping.
export function buildAppURL(demo: Pick<DemoInfo, "id" | "run">): string {
  return `/app/?run=${demo.run}&net=/net/${demo.id}.geojson`;
}
