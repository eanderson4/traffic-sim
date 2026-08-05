// statspanel.ts — the run-statistics panel: renders the standard run report
// (scripts/runreport.py --json, schema_version 1, ADR-0030). The report's
// whole reason to exist is that a MEAN over heterogeneous road hides the
// congestion — "27% of critical" over 2,203 lane-km and "55 lane-km at
// 6 km/h" were both true of the same run — so this panel renders
// DISTRIBUTIONS, never re-averages anything, and always shows the lane-km
// share and the VMT share side by side. Their disagreement is the signal:
// 30% of lane-km below 20 km/h carrying 5% of VMT means the jams are real
// but nearly empty; the reverse means the network is fine except exactly
// where everyone is.
//
// The panel derives NOTHING the report does not state — not the band
// order, not the critical density, not a completion rate whose denominator
// the report withheld. Every figure here is a share of lane-km-hours or of
// VMT computed by runreport.py over space-time cells.
//
// Split per the repo convention (modelpanel.ts/replaypanel.ts): the
// DOM-free half — parsing, band ordering, sorting, scaling, formatting —
// is pure and tested (test/statspanel.test.ts); StatsPanel is the DOM
// shell. ADR-0003: vanilla TS, no framework, no charting library — the
// bars are divs and the curve is inline SVG built here.
//
// NOTE: erasable-syntax only (node strip-only mode loads this directly —
// no parameter properties).

export const SCHEMA_VERSION = 1;

// Scope order as runreport.py prints them: whole network first, then the
// two halves it splits into (named corridors vs everything else).
export const SCOPES: readonly string[] = ["network", "corridors", "arterial grid"];

// Only for documents written before runreport.py emitted critical_k. The
// value is never a computation here — it just rescales density into
// "% of critical", exactly as the text report's %crit column does (ADR-0030:
// a rough freeway value applied network-wide, so %crit on the arterial grid
// is not to be read precisely).
export const LEGACY_CRITICAL_K = 25.0;

export interface Band {
  pct_lane_km: number;
  pct_vmt: number;
}

export interface Distribution {
  empty_pct: number; // lane-km-hours with NO vehicles — its own bucket, never a zero-speed average
  band_order?: string[]; // the report's own axis order (see bandOrder)
  bands: Record<string, Band>;
}

export interface GroupRow {
  lane_km: number; // static, from the network — not from the lanes that reported
  kmh: number;
  k: number;
  pct_lane_km_over_critical: number;
  veh_h_lost: number;
}

export interface CurvePoint {
  begin_min: number;
  end_min: number;
  speed: number;
  k: number;
  fw_speed: number;
  fw_k: number;
  pct_over_critical: number;
  pct_fwy_over_critical: number;
}

export interface Hotspot {
  lane: string;
  corridor: string | null;
  district: string | null;
  speed: number;
  veh_h_lost: number;
  x: number | null; // engine local metric frame (proj.ts projects to WGS84)
  y: number | null;
}

export interface RunReport {
  schema_version: number;
  metrics: string;
  ticks: number;
  dt: number;
  critical_k?: number; // veh/km/lane the density bands were cut against
  window: {
    begin_tick: number;
    end_tick: number;
    minutes: number;
    dropped_partial: number;
    skipped_warmup: number;
  };
  totals: {
    lane_km: number;
    corridor_lane_km: number;
    veh_km: number;
    veh_h: number;
    edie_kmh: number;
    completed_trips: number | null;
    active_at_horizon: number | null;
    // null when the metrics file was STREAMED (>200 MB): counting the trip
    // list would cost a second full pass just to get a denominator, so the
    // completion percentage is simply not available — the panel shows the
    // raw completed count instead of inventing a rate.
    injected_trips?: number | null;
    mean_time_loss_s?: number | null;
    total_time_loss_s?: number | null;
  };
  // Which span each `totals` field covers. Travel is summed over the retained
  // post-warmup interval cells; trips and delay come from the kernel's
  // run-total block and cover the whole horizon however the window was cut.
  // Absent on documents written before the split was stated, in which case
  // the panel says nothing rather than guessing which scope it is looking at.
  totals_scope?: {
    window?: string[];
    run?: string[];
    static?: string[];
    window_is_whole_run?: boolean;
  };
  // Whether the measurement set covered the network or a subset of it
  // (ADR-0014 permits either). Absent on older documents.
  coverage?: {
    network?: string;
    network_lanes?: number;
    measured_lanes?: number;
    network_lane_km?: number;
    measured_lane_km?: number;
    covers_network?: boolean;
    zero_length_lanes?: number;
  };
  density: Record<string, Distribution>;
  speed: Record<string, Distribution>;
  // Optional: runreport.py emits corridors/districts only when the
  // matching lane→group map was supplied, and --no-curve drops the curve.
  groups?: Record<string, Record<string, GroupRow>>;
  curve?: CurvePoint[];
  hotspots?: Hotspot[];
}

export type ParseResult = { ok: true; report: RunReport } | { ok: false; error: string };

// parseReport gates on schema_version BEFORE anything binds to key names.
// A v2 document rendered as v1 would not fail loudly — it would draw
// plausible wrong numbers, which is the exact failure mode ADR-0030
// exists to end. So an unknown version is a message, not a render.
export function parseReport(body: unknown): ParseResult {
  if (typeof body !== "object" || body === null || Array.isArray(body)) {
    return { ok: false, error: "not a JSON object" };
  }
  const b = body as Record<string, unknown>;
  const v = b["schema_version"];
  if (typeof v !== "number") {
    return { ok: false, error: "no schema_version — not a runreport.py --json document" };
  }
  if (v !== SCHEMA_VERSION) {
    return {
      ok: false,
      error: `schema_version ${v}; this panel renders v${SCHEMA_VERSION} only — regenerate with scripts/runreport.py --json`,
    };
  }
  for (const key of ["window", "totals", "density", "speed"]) {
    const s = b[key];
    if (typeof s !== "object" || s === null) {
      return { ok: false, error: `schema_version 1 but "${key}" is missing` };
    }
  }
  return { ok: true, report: b as unknown as RunReport };
}

// ---- formatting -------------------------------------------------------
// Fixed locale: a report read in one locale and quoted in another must
// print the same digits.
const GROUPED = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });
const GROUPED1 = new Intl.NumberFormat("en-US", { minimumFractionDigits: 1, maximumFractionDigits: 1 });

export function f0(x: number): string {
  return Number.isFinite(x) ? GROUPED.format(x) : "-";
}

export function f1(x: number): string {
  return Number.isFinite(x) ? GROUPED1.format(x) : "-";
}

export function pct1(x: number): string {
  return Number.isFinite(x) ? `${x.toFixed(1)}%` : "-";
}

// niceCeil rounds a bar/axis maximum up to a round multiple of a power of
// ten, each of which halves cleanly (the axes carry a mid tick). The ladder
// is deliberately finer than 1/2/5/10: a freeway peaking at 71.6 km/h on a
// 0–100 axis spends a quarter of the chart's height empty, which is exactly
// the height the 90-minute drain needs.
const LADDER = [1, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10];

export function niceCeil(x: number): number {
  if (!(x > 0) || !Number.isFinite(x)) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(x)));
  const f = x / mag;
  return (LADDER.find((s) => f <= s) ?? 10) * mag;
}

// ---- distributions ----------------------------------------------------

export interface DistRow {
  band: string;
  laneKm: number; // % of lane-km-hours — "% of road"
  vmt: number | null; // % of veh-km — "% of travel"; null for the empty bucket
  empty: boolean;
}

// bandOrder is the axis order, and it is the REPORT's, never this file's:
// the band names are not sortable strings ("100-150%" sorts before "<25%",
// "10-20" before "<10"), so any local ordering is a chance to silently
// reorder someone else's axis. band_order is what runreport.py cut the
// bands in. Documents written before that field existed fall back to the
// key order of `bands`, which is the same source order — JSON objects
// preserve insertion order for non-integer-like keys, and every band name
// here is non-integer-like.
export function bandOrder(d: Distribution): readonly string[] {
  const order = d.band_order;
  return Array.isArray(order) && order.length > 0 ? order : Object.keys(d.bands ?? {});
}

// distRows lays a distribution out for rendering: the empty bucket first
// exactly as runreport.py's print_dist does, then the bands in the
// report's order. Bands present in the document but missing from
// band_order are appended rather than dropped — an unlisted band must show
// up as an extra row, not vanish.
export function distRows(d: Distribution): DistRow[] {
  const order = bandOrder(d);
  const rows: DistRow[] = [
    // Empty road has no defined speed and no travel, so it carries no VMT
    // share — reporting one would be inventing a denominator.
    { band: "empty", laneKm: d.empty_pct ?? 0, vmt: null, empty: true },
  ];
  const bands = d.bands ?? {};
  for (const name of order) {
    const b = bands[name];
    if (b === undefined) continue;
    rows.push({ band: name, laneKm: b.pct_lane_km, vmt: b.pct_vmt, empty: false });
  }
  for (const [name, b] of Object.entries(bands)) {
    if (order.includes(name)) continue;
    rows.push({ band: name, laneKm: b.pct_lane_km, vmt: b.pct_vmt, empty: false });
  }
  return rows;
}

// barMax is the common scale for one distribution block. Both series share
// it — bars scaled per-series would make a 3% VMT band look like a 60%
// lane-km band, which is the comparison the block exists to make.
export function barMax(rows: readonly DistRow[]): number {
  let m = 0;
  for (const r of rows) m = Math.max(m, r.laneKm, r.vmt ?? 0);
  return niceCeil(m);
}

// scopeLabel renders a distribution's scope name HONESTLY: the report's
// "network" scope is only the network when the measurement set covered it.
// ADR-0014 permits a set over a subset, and runreport.py says which it was in
// `coverage`. Labelling a subset "network" recreates the exact defect ADR-0030
// exists to end — travel over the measured lanes presented as a whole-network
// figure — on the consumer side, after the producer had been fixed.
export function scopeLabel(scope: string, coverage: RunReport["coverage"]): string {
  if (scope !== "network") return scope;
  return coverage?.covers_network === false ? "measured subset" : "network";
}

// coverageRow describes a partial measurement set, or null when the set
// covered the network (in which case there is nothing to warn about) or when
// the report predates the field (in which case nothing is known).
export function coverageRow(coverage: RunReport["coverage"]): [string, string] | null {
  if (!coverage || coverage.covers_network !== false) return null;
  const ml = coverage.measured_lanes;
  const nl = coverage.network_lanes;
  const mkm = coverage.measured_lane_km;
  const nkm = coverage.network_lane_km;
  const lanes = typeof ml === "number" && typeof nl === "number" ? `${f0(ml)} of ${f0(nl)} lanes` : "a subset";
  const km =
    typeof mkm === "number" && typeof nkm === "number" && nkm > 0
      ? ` · ${f1(mkm)} of ${f1(nkm)} lane-km (${((100 * mkm) / nkm).toFixed(1)}%)`
      : "";
  return ["COVERAGE", `${lanes}${km} — figures cover the measured subset; unmeasured lanes are absent, not empty`];
}

// scopeOrder returns the scopes present, in runreport.py's print order.
export function scopeOrder(d: Record<string, Distribution>): string[] {
  const out = SCOPES.filter((s) => Object.hasOwn(d, s));
  for (const s of Object.keys(d)) if (!out.includes(s)) out.push(s);
  return out;
}

// ---- summary ----------------------------------------------------------

export interface ReportSection {
  title: string;
  rows: Array<[string, string]>;
  note?: string;
}

// criticalK is the density the bands were cut against, as STATED by the
// report. Only a document written before runreport.py emitted it falls
// back to the historical value.
export function criticalK(r: RunReport): number {
  const k = r.critical_k;
  return typeof k === "number" && k > 0 ? k : LEGACY_CRITICAL_K;
}

export function summarySections(r: RunReport): ReportSection[] {
  const runMin = (r.ticks * r.dt) / 60;
  const w = r.window;
  const pctOfRun = runMin > 0 ? (100 * w.minutes) / runMin : 0;
  const runRows: Array<[string, string]> = [
    ["horizon", `${f0(r.ticks)} ticks × ${r.dt} s = ${f0(runMin)} min`],
    [
      "window",
      `ticks ${f0(w.begin_tick)}–${f0(w.end_tick)} = ${f1(w.minutes)} min (${pctOfRun.toFixed(0)}% of the run)`,
    ],
    ["dropped", `${f0(w.dropped_partial)} partial interval(s)`],
  ];
  if (w.skipped_warmup > 0) {
    runRows.push(["pre-warmup", `${f0(w.skipped_warmup)} interval(s) skipped`]);
  }

  const t = r.totals;
  // Base for the corridor share. runreport.py narrows corridor_lane_km to the
  // MEASURED corridor lanes under a subset set, so the denominator has to be
  // narrowed the same way — dividing measured corridor km by whole-network km
  // would understate the corridor share by exactly the unmeasured fraction.
  const laneKmBase =
    r.coverage?.covers_network === false && typeof r.coverage.measured_lane_km === "number"
      ? r.coverage.measured_lane_km
      : t.lane_km;
  const corridorShare = laneKmBase > 0 ? (100 * t.corridor_lane_km) / laneKmBase : 0;
  // Two populations in one block, so each row says which it belongs to.
  // Travel is summed over the retained post-warmup interval cells; trips and
  // delay come from the kernel's run-total block and cover the whole horizon
  // however the window was cut. The report states the split in
  // `totals_scope`; when it says the two spans coincide (no warmup cut, full
  // horizon) the tags are noise and are dropped.
  const mixed = r.totals_scope?.window_is_whole_run === false;
  const winTag = mixed ? " · window" : "";
  const runTag = mixed ? " · whole run" : "";
  // A subset set's travel covers only the measured lanes, so the lane-km it
  // is implicitly divided by must be the measured lane-km, and the row must
  // not be called "network".
  const subset = r.coverage?.covers_network === false;
  const cov = coverageRow(r.coverage);
  const totalRows: Array<[string, string]> = [
    [
      subset ? "measured" : "network",
      `${f1(laneKmBase)} lane-km` +
        (t.corridor_lane_km > 0
          ? ` · ${f1(t.corridor_lane_km)} on corridors (${corridorShare.toFixed(1)}%)`
          : ""),
    ],
    ["travel", `${f0(t.veh_km)} veh-km over ${f0(t.veh_h)} veh-h${winTag}`],
    ["Edie speed", `${f1(t.edie_kmh)} km/h (distance ÷ time)${winTag}`],
  ];
  if (t.completed_trips !== null && t.completed_trips !== undefined) {
    // injected_trips is null on a STREAMED metrics file (counting the trip
    // list would be a second full pass just for a denominator), so the
    // completion rate is printed only when the report states the
    // denominator — never inferred from anything else.
    const inj = t.injected_trips;
    const of =
      typeof inj === "number" && inj > 0
        ? ` of ${f0(inj)} injected (${((100 * t.completed_trips) / inj).toFixed(1)}%)`
        : "";
    totalRows.push([
      "trips",
      `${f0(t.completed_trips)} completed${of} · ${f0(t.active_at_horizon ?? 0)} still driving at the horizon${runTag}`,
    ]);
  }
  const mean = t.mean_time_loss_s;
  const lost = t.total_time_loss_s;
  const delay: string[] = [];
  if (typeof mean === "number") delay.push(`${f0(mean)} s mean loss per trip`);
  if (typeof lost === "number") delay.push(`${f0(lost / 3600)} veh-h lost`);
  if (delay.length > 0) totalRows.push(["delay", delay.join(" · ") + runTag]);

  return [
    {
      title: "run",
      rows: runRows,
      note:
        "partial intervals are dropped (ADR-0014 §3): an interval cut short by the " +
        "horizon has a duration the density denominator does not know about, so a " +
        "non-zero count is measurement the window does NOT contain.",
    },
    {
      title: "totals",
      // Coverage first when the set was partial: everything under it is over
      // a subset, and that is the frame for reading the rest, not a footnote.
      rows: cov === null ? totalRows : [cov, ...totalRows],
      note: "denominators are the measured network's, not the occupied part's — a draining network must not read as denser. Travel covers the window; trips and delay cover the whole run.",
    },
  ];
}

// ---- groups -----------------------------------------------------------

export type GroupKey = "name" | "lane_km" | "kmh" | "k" | "pct_lane_km_over_critical" | "veh_h_lost";

export interface GroupColumn {
  key: GroupKey;
  label: string;
  title: string; // hover text — what the column actually measures
}

// %crit sorts on k: it IS k, rescaled by the report's critical density.
export function groupColumns(critical: number): readonly GroupColumn[] {
  return [
    { key: "name", label: "name", title: "group name" },
    { key: "lane_km", label: "lane-km", title: "static lane-km from the network (fixed denominator)" },
    { key: "kmh", label: "km/h", title: "Edie speed: veh-km ÷ veh-h over the window" },
    { key: "k", label: "veh/km/ln", title: "mean density over the group's lane-km-hours" },
    { key: "k", label: "%crit", title: `density as a share of critical (${critical} veh/km/lane)` },
    {
      key: "pct_lane_km_over_critical",
      label: "%km≥crit",
      title: "share of the group's lane-km-hours AT or above critical density — the jammed part",
    },
    { key: "veh_h_lost", label: "veh-h lost", title: "total time loss accumulated in the group" },
  ];
}

// Default sort: veh-h lost, descending — the question a group table is
// opened to answer is "where did the delay happen".
export const GROUP_DEFAULT_KEY: GroupKey = "veh_h_lost";

export function sortGroupRows(
  rows: Record<string, GroupRow>,
  key: GroupKey,
  desc: boolean,
): Array<[string, GroupRow]> {
  const arr = Object.entries(rows);
  arr.sort((a, b) => {
    const c = key === "name" ? a[0].localeCompare(b[0]) : a[1][key] - b[1][key];
    // Ties fall back to name so the order is stable across re-sorts.
    return (desc ? -c : c) || a[0].localeCompare(b[0]);
  });
  return arr;
}

// ---- curve ------------------------------------------------------------

export interface Pt {
  x: number;
  y: number;
}

export interface CurveSeries {
  key: "speed" | "fw_speed" | "pct_fwy_over_critical";
  label: string;
  axis: "kmh" | "pct";
  pts: Pt[];
  path: string; // SVG "M x y L x y …"
}

export interface CurveGeom {
  w: number;
  h: number;
  pad: { l: number; r: number; t: number; b: number };
  xMin: number;
  xMax: number;
  vMax: number; // left axis, km/h
  pMax: number; // right axis, %
  series: CurveSeries[];
  xTicks: Array<{ x: number; label: string }>;
  yLeft: Array<{ y: number; label: string }>;
  yRight: Array<{ y: number; label: string }>;
}

const CURVE_PAD = { l: 26, r: 30, t: 8, b: 14 };

// trim prints an axis label without trailing zeros (50, not 50.0; 2.5 stays).
const trim = (x: number): string => String(Math.round(x * 100) / 100);

// curveGeometry lays the per-interval curve out in SVG user units. The x
// axis is REAL MINUTES (not the interval index): fill, peak and drain are
// only separable if the time between samples is to scale, and a run with
// mixed interval lengths would otherwise be drawn as if evenly spaced.
// Each sample plots at its interval's MIDPOINT, because the value is an
// average over the interval, not a reading at its edge.
export function curveGeometry(curve: readonly CurvePoint[], w = 400, h = 124): CurveGeom | null {
  if (curve.length === 0) return null;
  const first = curve[0]!;
  const last = curve[curve.length - 1]!;
  const xMin = first.begin_min;
  const xMax = Math.max(last.end_min, xMin + 1e-9);
  let vPeak = 0;
  let pPeak = 0;
  for (const c of curve) {
    vPeak = Math.max(vPeak, c.speed, c.fw_speed);
    pPeak = Math.max(pPeak, c.pct_fwy_over_critical);
  }
  const vMax = niceCeil(vPeak);
  const pMax = niceCeil(pPeak);
  const plotW = w - CURVE_PAD.l - CURVE_PAD.r;
  const plotH = h - CURVE_PAD.t - CURVE_PAD.b;
  const xAt = (min: number): number => CURVE_PAD.l + ((min - xMin) / (xMax - xMin)) * plotW;
  const yAt = (v: number, max: number): number => CURVE_PAD.t + (1 - v / max) * plotH;

  const mk = (
    key: CurveSeries["key"],
    label: string,
    axis: CurveSeries["axis"],
    pick: (c: CurvePoint) => number,
  ): CurveSeries => {
    const pts = curve.map((c) => ({
      x: xAt((c.begin_min + c.end_min) / 2),
      y: yAt(pick(c), axis === "kmh" ? vMax : pMax),
    }));
    const path = pts.map((p, i) => `${i === 0 ? "M" : "L"} ${p.x.toFixed(1)} ${p.y.toFixed(1)}`).join(" ");
    return { key, label, axis, pts, path };
  };

  // At most 7 x labels: a 60-interval run must not print 60 of them.
  const stride = Math.max(1, Math.ceil(curve.length / 7));
  const xTicks: Array<{ x: number; label: string }> = [];
  for (let i = 0; i < curve.length; i += stride) {
    const c = curve[i]!;
    xTicks.push({ x: xAt(c.begin_min), label: String(Math.round(c.begin_min)) });
  }
  xTicks.push({ x: xAt(last.end_min), label: String(Math.round(last.end_min)) });

  return {
    w,
    h,
    pad: CURVE_PAD,
    xMin,
    xMax,
    vMax,
    pMax,
    series: [
      mk("speed", "network km/h", "kmh", (c) => c.speed),
      mk("fw_speed", "freeway km/h", "kmh", (c) => c.fw_speed),
      mk("pct_fwy_over_critical", "% fwy ≥ crit", "pct", (c) => c.pct_fwy_over_critical),
    ],
    xTicks,
    // A mid tick on each axis: over a 90-minute run the network speed ends
    // in the bottom tenth of a freeway-scaled axis, and "is that half of
    // where it started" must be readable without arithmetic.
    yLeft: [
      { y: yAt(vMax, vMax), label: trim(vMax) },
      { y: yAt(vMax / 2, vMax), label: trim(vMax / 2) },
      { y: yAt(0, vMax), label: "0" },
    ],
    yRight: [
      { y: yAt(pMax, pMax), label: `${trim(pMax)}%` },
      { y: yAt(pMax / 2, pMax), label: trim(pMax / 2) },
      { y: yAt(0, pMax), label: "0" },
    ],
  };
}

// ---- DOM --------------------------------------------------------------

const SVG_NS = "http://www.w3.org/2000/svg";

function el(tag: string, cls?: string, text?: string): HTMLElement {
  const e = document.createElement(tag);
  if (cls !== undefined) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function svg(tag: string, attrs: Record<string, string>): SVGElement {
  const e = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

export interface StatsPanelOptions {
  // Report URL fetched at init. A 404 is not an error the user must see —
  // the panel just stays hidden until a report is dropped on it.
  url?: string;
  // onHotspot fires when a hotspot row is clicked. x/y are the ENGINE's
  // local metric frame (never lng/lat) — main.ts runs them through proj.ts
  // before flying the map there. Rows without coordinates are not
  // clickable, so x/y are always finite here.
  onHotspot?: (h: Hotspot & { x: number; y: number }) => void;
  // Extra drop target (main.ts passes document.body) so a report can be
  // dragged anywhere onto the map, including while the panel is hidden.
  dropTarget?: HTMLElement;
}

export class StatsPanel {
  // NOTE: erasable-syntax only (node strip-only mode loads this directly —
  // no parameter properties).
  private root: HTMLElement;
  private url: string;
  private onHotspot: ((h: Hotspot & { x: number; y: number }) => void) | null;
  private report: RunReport | null = null;
  private source = ""; // where the loaded report came from (URL or file name)
  private error = "";
  private expanded = false;
  private sort = new Map<string, { key: GroupKey; desc: boolean }>();

  constructor(root: HTMLElement, opts: StatsPanelOptions = {}) {
    this.root = root;
    this.url = opts.url ?? "/sample-runreport.json";
    this.onHotspot = opts.onHotspot ?? null;
    this.enableDrop(root);
    if (opts.dropTarget !== undefined) this.enableDrop(opts.dropTarget);
  }

  // init loads the default report; on any failure the panel hides (the
  // switcher/modelpanel discipline — a live demo with no report must not
  // grow dead chrome). A drop still reveals it.
  async init(): Promise<void> {
    const ok = await this.loadUrl(this.url);
    if (!ok) {
      this.root.style.display = "none";
      return;
    }
    this.render();
  }

  async loadUrl(url: string): Promise<boolean> {
    try {
      const resp = await fetch(url, { signal: AbortSignal.timeout(5000) });
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      return this.accept(await resp.json(), url);
    } catch (err) {
      this.error = `${url}: ${err instanceof Error ? err.message : String(err)}`;
      this.report = null;
      return false;
    }
  }

  // accept validates and installs a parsed document. A schema mismatch
  // KEEPS the panel visible with the message: silently ignoring the file
  // the user just dropped is the one behaviour that reads as a bug.
  accept(body: unknown, source: string): boolean {
    const res = parseReport(body);
    if (!res.ok) {
      this.error = `${source}: ${res.error}`;
      this.report = null;
      return false;
    }
    this.report = res.report;
    this.source = source;
    this.error = "";
    this.sort.clear();
    return true;
  }

  private enableDrop(target: HTMLElement): void {
    target.addEventListener("dragover", (e) => {
      // Files only: a text selection dragged across the map is also a
      // dragover, and it must not arm the drop outline.
      if (e.dataTransfer === null || !e.dataTransfer.types.includes("Files")) return;
      e.preventDefault(); // without this the browser navigates to the file
      e.dataTransfer.dropEffect = "copy";
      this.root.classList.add("sp-drop");
    });
    target.addEventListener("dragleave", () => this.root.classList.remove("sp-drop"));
    target.addEventListener("drop", (e) => {
      this.root.classList.remove("sp-drop");
      const file = e.dataTransfer?.files?.[0];
      if (file === undefined) return;
      e.preventDefault();
      void this.loadFile(file);
    });
  }

  async loadFile(file: File): Promise<void> {
    try {
      this.accept(JSON.parse(await file.text()), file.name);
    } catch (err) {
      this.report = null;
      this.error = `${file.name}: ${err instanceof Error ? err.message : String(err)}`;
    }
    // A dropped report is an explicit request to look at it.
    this.expanded = true;
    this.root.style.display = "";
    this.render();
  }

  private render(): void {
    this.root.textContent = "";
    const head = el("div", "sp-toggle");
    head.appendChild(el("span", undefined, this.expanded ? "▤ stats ▾" : "▤ stats"));
    if (this.source !== "") {
      const src = el("span", "sp-src", this.source);
      src.title = this.source;
      head.appendChild(src);
    }
    head.onclick = () => {
      this.expanded = !this.expanded;
      this.render();
    };
    this.root.appendChild(head);
    if (!this.expanded) return;

    this.root.appendChild(this.buildLoader());
    if (this.error !== "") this.root.appendChild(el("div", "sp-err", `⚠ ${this.error}`));
    const r = this.report;
    if (r === null) return;

    const critical = criticalK(r);
    for (const sec of summarySections(r)) this.appendSection(sec);
    for (const scope of scopeOrder(r.density)) {
      const d = r.density[scope];
      if (d !== undefined) {
        this.appendDist(`density [${scopeLabel(scope, r.coverage)}]`, d, `% of critical (${critical} veh/km/ln)`);
      }
    }
    for (const scope of scopeOrder(r.speed)) {
      const d = r.speed[scope];
      if (d !== undefined) this.appendDist(`speed [${scopeLabel(scope, r.coverage)}]`, d, "km/h");
    }
    for (const [name, rows] of Object.entries(r.groups ?? {})) this.appendGroups(name, rows, critical);
    this.appendCurve(r.curve ?? []);
    this.appendHotspots(r.hotspots ?? []);
  }

  private buildLoader(): HTMLElement {
    const box = el("div", "sp-load");
    const input = document.createElement("input");
    input.type = "text";
    input.value = this.source === "" ? this.url : this.source;
    input.spellcheck = false;
    input.title = "report URL — or drop a runreport JSON anywhere on the map";
    box.appendChild(input);
    const load = el("span", "sp-btn", "load");
    load.onclick = () => {
      void (async () => {
        await this.loadUrl(input.value.trim());
        this.render();
      })();
    };
    box.appendChild(load);
    const label = el("label", "sp-btn", "file");
    const picker = document.createElement("input");
    picker.type = "file";
    picker.accept = "application/json,.json";
    picker.hidden = true;
    picker.onchange = () => {
      const file = picker.files?.[0];
      if (file !== undefined) void this.loadFile(file);
    };
    label.appendChild(picker);
    box.appendChild(label);
    return box;
  }

  private appendSection(sec: ReportSection): void {
    this.root.appendChild(el("div", "sp-sec", sec.title));
    for (const [label, value] of sec.rows) {
      const row = el("div", "sp-row");
      row.appendChild(el("span", undefined, label));
      row.appendChild(el("span", undefined, value));
      this.root.appendChild(row);
    }
    if (sec.note !== undefined) this.root.appendChild(el("div", "sp-note", sec.note));
  }

  // appendDist draws one distribution block: two bars per band on ONE
  // scale — share of road (lane-km-hours) and share of travel (VMT).
  // Both, always: the report's central claim is that they disagree.
  private appendDist(title: string, d: Distribution, unit: string): void {
    const rows = distRows(d);
    const max = barMax(rows);
    this.root.appendChild(el("div", "sp-sec", title));
    const key = el("div", "sp-key");
    key.appendChild(el("i", "sp-road"));
    key.appendChild(el("span", undefined, "% of road (lane-km-h)"));
    key.appendChild(el("i", "sp-vmt"));
    key.appendChild(el("span", undefined, `% of travel (VMT) · ${unit} · bars 0–${max}%`));
    this.root.appendChild(key);
    for (const r of rows) {
      const row = el("div", "sp-band");
      row.appendChild(el("span", "sp-band-l", r.band));
      const bars = el("span", "sp-bars");
      const road = el("span", "sp-bar sp-road");
      road.style.width = `${(100 * r.laneKm) / max}%`;
      bars.appendChild(road);
      const vmt = el("span", "sp-bar sp-vmt");
      // The empty bucket has no travel to have a share of — an absent bar
      // says that; a zero-width bar would read as "0% of travel here".
      vmt.style.width = r.vmt === null ? "0" : `${(100 * r.vmt) / max}%`;
      if (r.vmt === null) vmt.style.visibility = "hidden";
      bars.appendChild(vmt);
      row.appendChild(bars);
      row.appendChild(
        el("span", "sp-band-v", `${r.laneKm.toFixed(1)} / ${r.vmt === null ? "—" : r.vmt.toFixed(1)}`),
      );
      this.root.appendChild(row);
    }
  }

  private appendGroups(name: string, rows: Record<string, GroupRow>, critical: number): void {
    if (Object.keys(rows).length === 0) return;
    const state = this.sort.get(name) ?? { key: GROUP_DEFAULT_KEY, desc: true };
    this.root.appendChild(el("div", "sp-sec", name));
    const table = document.createElement("table");
    const head = document.createElement("tr");
    for (const col of groupColumns(critical)) {
      const th = document.createElement("th");
      th.textContent = col.label;
      th.title = `${col.title} — click to sort`;
      if (col.key === state.key) th.className = state.desc ? "sp-sorted" : "sp-sorted sp-asc";
      th.onclick = () => {
        // Same column flips direction; a new one starts at the reading
        // that answers "who is worst" (desc numeric, A→Z by name).
        const desc = state.key === col.key ? !state.desc : col.key !== "name";
        this.sort.set(name, { key: col.key, desc });
        this.render();
      };
      head.appendChild(th);
    }
    table.appendChild(head);
    for (const [label, g] of sortGroupRows(rows, state.key, state.desc)) {
      const tr = document.createElement("tr");
      const cells = [
        label,
        f1(g.lane_km),
        f1(g.kmh),
        f1(g.k),
        `${((100 * g.k) / critical).toFixed(0)}%`,
        pct1(g.pct_lane_km_over_critical),
        f0(g.veh_h_lost),
      ];
      for (const c of cells) {
        const td = document.createElement("td");
        td.textContent = c;
        tr.appendChild(td);
      }
      table.appendChild(tr);
    }
    this.appendTable(table);
  }

  private appendCurve(curve: readonly CurvePoint[]): void {
    const geom = curveGeometry(curve);
    if (geom === null) return;
    this.root.appendChild(el("div", "sp-sec", "curve (per interval)"));
    // Uniform scaling (no preserveAspectRatio="none"): stretching x would
    // stretch the tick labels with it.
    const chart = svg("svg", { class: "sp-chart", viewBox: `0 0 ${geom.w} ${geom.h}` });
    const y0 = geom.h - geom.pad.b;
    chart.appendChild(
      svg("path", {
        class: "sp-axis",
        d: `M ${geom.pad.l} ${geom.pad.t} L ${geom.pad.l} ${y0} L ${geom.w - geom.pad.r} ${y0}`,
      }),
    );
    // Gridlines at the left axis's non-zero ticks (the baseline is the axis
    // itself): with 18 intervals the eye needs a horizontal reference to
    // read the drain against.
    for (const t of geom.yLeft.slice(0, -1)) {
      chart.appendChild(
        svg("path", { class: "sp-grid", d: `M ${geom.pad.l} ${t.y} L ${geom.w - geom.pad.r} ${t.y}` }),
      );
    }
    for (const t of geom.xTicks) {
      const tx = svg("text", { class: "sp-tick", x: String(t.x), y: String(geom.h - 4), "text-anchor": "middle" });
      tx.textContent = t.label;
      chart.appendChild(tx);
    }
    for (const t of geom.yLeft) {
      const tx = svg("text", { class: "sp-tick", x: String(geom.pad.l - 3), y: String(t.y + 3), "text-anchor": "end" });
      tx.textContent = t.label;
      chart.appendChild(tx);
    }
    for (const t of geom.yRight) {
      const tx = svg("text", {
        class: "sp-tick",
        x: String(geom.w - geom.pad.r + 3),
        y: String(t.y + 3),
        "text-anchor": "start",
      });
      tx.textContent = t.label;
      chart.appendChild(tx);
    }
    for (const s of geom.series) {
      chart.appendChild(svg("path", { class: `sp-line sp-s-${s.key}`, d: s.path }));
      // Dots as well as the line: a two-interval run is otherwise a single
      // segment with no visible samples.
      for (const p of s.pts) {
        chart.appendChild(
          svg("circle", { class: `sp-dot sp-s-${s.key}`, cx: String(p.x), cy: String(p.y), r: "1.6" }),
        );
      }
    }
    this.root.appendChild(chart);
    const key = el("div", "sp-key");
    for (const s of geom.series) {
      key.appendChild(el("i", `sp-swatch sp-s-${s.key}`));
      key.appendChild(el("span", undefined, s.label));
    }
    this.root.appendChild(key);
    this.root.appendChild(
      el(
        "div",
        "sp-note",
        "x is real minutes (interval midpoints), left axis km/h, right axis % — this is what separates fill from peak from drain.",
      ),
    );
  }

  private appendHotspots(hotspots: readonly Hotspot[]): void {
    if (hotspots.length === 0) return;
    this.root.appendChild(el("div", "sp-sec", "hotspots (lanes by delay)"));
    const table = document.createElement("table");
    const head = document.createElement("tr");
    for (const label of ["lane", "corridor", "district", "km/h", "veh-h lost"]) {
      const th = document.createElement("th");
      th.textContent = label;
      head.appendChild(th);
    }
    table.appendChild(head);
    // Source order is runreport.py's: descending by delay, already the
    // only order this table is read in.
    for (const h of hotspots) {
      const tr = document.createElement("tr");
      const cells = [h.lane, h.corridor ?? "-", h.district ?? "-", f1(h.speed), f1(h.veh_h_lost)];
      for (const [i, c] of cells.entries()) {
        const td = document.createElement("td");
        if (i === 0) td.className = "sp-lane";
        td.textContent = c;
        tr.appendChild(td);
      }
      const x = h.x;
      const y = h.y;
      if (this.onHotspot !== null && typeof x === "number" && typeof y === "number") {
        tr.className = "sp-click";
        tr.title = `fly to ${x.toFixed(0)},${y.toFixed(0)}`;
        tr.onclick = () => this.onHotspot?.({ ...h, x, y });
      }
      table.appendChild(tr);
    }
    this.appendTable(table);
  }

  // appendTable wraps a table in its own horizontal scroller: the widest
  // group name plus seven numeric columns overflows the panel, and letting
  // the PANEL scroll sideways would drag every other block off screen.
  private appendTable(table: HTMLElement): void {
    const wrap = el("div", "sp-tw");
    wrap.appendChild(table);
    this.root.appendChild(wrap);
  }
}
