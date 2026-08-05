// flowpanel.ts — the flow panel: cumulative arrivals vs departures, the
// accumulation that follows from them, and the per-minute rates. Data is a
// scripts/show/mkflowcurve.py document (schema_version 1), pointed at with
// ?flow=.
//
// Why this panel exists next to statspanel.ts rather than inside it: the
// stats panel answers "how bad is it and where" from space-time cells. This
// one answers a question those cells cannot — "is the network filling,
// holding, or draining right now" — which is a property of the trip ledger,
// not of any lane. runreport.py deliberately does not make a second full
// pass over `trips` (see its injected_trips note), so the flow curve is its
// own document and its own optional panel.
//
// The cumulative plot is the standard queueing (Newell) diagram, and the
// two things worth saying out loud about it:
//   - the VERTICAL gap between arrivals and total exits is the number of
//     vehicles in the network,
//   - the HORIZONTAL gap is how long a vehicle entering then waits to get out.
// Both are read off one chart, which is why it is drawn cumulatively and not
// as rates alone. Rates hide accumulation, and accumulation is the thing
// that gridlocks.
//
// Exits are STACKED — completions, then completions+strandings — because
// plotting completions alone against arrivals draws a gap that includes
// vehicles the gridlock escape (ADR-0034) deleted. That would claim the
// network is holding cars it has actually removed, which is exactly the
// error the three-channel split exists to prevent.
//
// Split per the repo convention (statspanel.ts/modelpanel.ts/replaypanel.ts):
// the DOM-free half — parsing, scaling, path building — is pure and tested
// (test/flowpanel.test.ts); FlowPanel is the DOM shell.
//
// NOTE: erasable-syntax only (node strip-only mode loads this directly —
// no parameter properties).

export const SCHEMA_VERSION = 1;

export interface FlowBin {
  tick: number;
  min: number;
  arr: number;
  done: number;
  strand: number;
  cumArr: number;
  cumDone: number;
  cumStrand: number;
  inNet: number;
}

export interface FlowTotals {
  injected: number;
  completed: number;
  stranded: number;
  activeAtHorizon: number;
  peakInNet: number;
  peakInNetTick: number;
}

export interface FlowDoc {
  schema_version: number;
  dt: number;
  ticks: number;
  binTicks: number;
  lastEntryTick: number;
  totals: FlowTotals;
  bins: FlowBin[];
}

export function isFlowDoc(v: unknown): v is FlowDoc {
  if (typeof v !== "object" || v === null) return false;
  const d = v as Partial<FlowDoc>;
  return (
    d.schema_version === SCHEMA_VERSION &&
    typeof d.ticks === "number" &&
    typeof d.dt === "number" &&
    Array.isArray(d.bins) &&
    d.bins.length > 0 &&
    typeof d.totals === "object" &&
    d.totals !== null
  );
}

// ── pure geometry ────────────────────────────────────────────────────────

export interface Pad {
  l: number;
  r: number;
  t: number;
  b: number;
}

export interface AxisTick {
  pos: number; // px along the axis
  label: string;
}

export interface Series {
  key: string;
  label: string;
  path: string; // SVG path data
  fill?: string; // SVG path data for the area under/between, when shaded
}

export interface ChartGeom {
  w: number;
  h: number;
  pad: Pad;
  yMax: number;
  xTicks: AxisTick[];
  yTicks: AxisTick[];
  series: Series[];
  // x of the tick after which nothing more arrives, or null when the run
  // never stops loading (in which case there is no drain phase to mark).
  arrivalsStopX: number | null;
}

const PAD: Pad = { l: 44, r: 8, t: 8, b: 16 };

// niceMax rounds a data maximum up to a round number so the axis labels are
// readable. Returns at least 1 so an all-zero series still has an axis.
//
// The ladder is deliberately FINE. A coarse [1,2,5,10] ladder sends 10,517
// to 20,000 and 5,989 to 10,000, which draws both curves inside the bottom
// half of a 108px chart — the shape of the peak is the whole point of the
// panel, so half the height is not spare. Every rung here still produces a
// clean axis label at 2–4 divisions.
const NICE_STEPS = [1, 1.2, 1.5, 2, 2.5, 3, 4, 5, 6, 8, 10];

export function niceMax(v: number): number {
  if (!(v > 0)) return 1;
  const mag = Math.pow(10, Math.floor(Math.log10(v)));
  for (const step of NICE_STEPS) {
    const cand = step * mag;
    if (cand >= v) return cand;
  }
  return 10 * mag;
}

// xTicksFor spaces minute labels so they do not collide at panel width: one
// every 5, 10, 15 or 30 minutes, whichever first gives at most 8 labels.
export function xTicksFor(minutes: number, w: number, pad: Pad): AxisTick[] {
  const step = [5, 10, 15, 30, 60].find((s) => minutes / s <= 8) ?? 60;
  const out: AxisTick[] = [];
  const span = w - pad.l - pad.r;
  for (let m = 0; m <= minutes + 1e-9; m += step) {
    out.push({ pos: pad.l + (m / minutes) * span, label: `${m}′` });
  }
  return out;
}

function yTicksFor(yMax: number, h: number, pad: Pad, n: number): AxisTick[] {
  const out: AxisTick[] = [];
  const span = h - pad.t - pad.b;
  for (let i = 0; i <= n; i++) {
    const v = (yMax / n) * i;
    out.push({ pos: h - pad.b - (v / yMax) * span, label: compact(v) });
  }
  return out;
}

// compact renders axis numbers short enough for a 44px gutter: 5989 → "6.0k".
export function compact(v: number): string {
  if (v >= 1000) return `${(v / 1000).toFixed(1)}k`;
  return String(Math.round(v));
}

function pathOf(
  bins: readonly FlowBin[],
  pick: (b: FlowBin) => number,
  X: (t: number) => number,
  Y: (v: number) => number,
): string {
  return bins
    .map((b, i) => `${i === 0 ? "M" : "L"} ${X(b.tick).toFixed(1)} ${Y(pick(b)).toFixed(1)}`)
    .join(" ");
}

// cumulativeGeom builds the queueing diagram: arrivals, all exits, and
// completions, with the accumulation between arrivals and all exits shaded.
export function cumulativeGeom(doc: FlowDoc, w: number, h: number): ChartGeom {
  const { bins, ticks } = doc;
  const yMax = niceMax(doc.totals.injected);
  const span = { x: w - PAD.l - PAD.r, y: h - PAD.t - PAD.b };
  const X = (t: number) => PAD.l + (t / ticks) * span.x;
  const Y = (v: number) => h - PAD.b - (v / yMax) * span.y;

  const arrPath = pathOf(bins, (b) => b.cumArr, X, Y);
  const outPath = pathOf(bins, (b) => b.cumDone + b.cumStrand, X, Y);
  const donePath = pathOf(bins, (b) => b.cumDone, X, Y);
  // The shaded band: down the arrivals curve, back along total exits.
  const back = bins
    .slice()
    .reverse()
    .map((b) => `L ${X(b.tick).toFixed(1)} ${Y(b.cumDone + b.cumStrand).toFixed(1)}`)
    .join(" ");

  return {
    w,
    h,
    pad: PAD,
    yMax,
    xTicks: xTicksFor((ticks * doc.dt) / 60, w, PAD),
    yTicks: yTicksFor(yMax, h, PAD, 4),
    series: [
      { key: "arr", label: "arrived", path: arrPath, fill: `${arrPath} ${back} Z` },
      { key: "out", label: "all exits", path: outPath },
      { key: "done", label: "completed", path: donePath },
    ],
    arrivalsStopX: doc.lastEntryTick < ticks ? X(doc.lastEntryTick) : null,
  };
}

// accumulationGeom builds the "vehicles in network" area — the vertical gap
// of the diagram above, plotted directly because it is the number the
// question "is it draining" is actually about.
export function accumulationGeom(doc: FlowDoc, w: number, h: number): ChartGeom {
  const { bins, ticks } = doc;
  const yMax = niceMax(doc.totals.peakInNet);
  const span = { x: w - PAD.l - PAD.r, y: h - PAD.t - PAD.b };
  const X = (t: number) => PAD.l + (t / ticks) * span.x;
  const Y = (v: number) => h - PAD.b - (v / yMax) * span.y;
  const p = pathOf(bins, (b) => b.inNet, X, Y);
  const y0 = (h - PAD.b).toFixed(1);
  const last = bins[bins.length - 1]!;
  return {
    w,
    h,
    pad: PAD,
    yMax,
    xTicks: xTicksFor((ticks * doc.dt) / 60, w, PAD),
    yTicks: yTicksFor(yMax, h, PAD, 2),
    series: [
      {
        key: "net",
        label: "in network",
        path: p,
        fill: `${p} L ${X(last.tick).toFixed(1)} ${y0} L ${X(bins[0]!.tick).toFixed(1)} ${y0} Z`,
      },
    ],
    arrivalsStopX: doc.lastEntryTick < ticks ? X(doc.lastEntryTick) : null,
  };
}

// rateGeom builds the per-minute rates. Rates are normalised per MINUTE
// rather than per bin so the axis reads the same whatever --bin-s the
// document was built with.
//
// Bin 0 is excluded from the y scale: every vehicle whose arrival tick is 0
// enters at once, a burst an order of magnitude above any sustained rate,
// and scaling to it flattens the rest of the chart into the bottom eighth.
// It clips, and clipRate() reports by how much so the shell can say so.
export function rateGeom(doc: FlowDoc, w: number, h: number): ChartGeom {
  const { bins, ticks } = doc;
  const perMin = (doc.binTicks * doc.dt) / 60;
  const sustained = bins.slice(1);
  const peak = Math.max(
    ...sustained.map((b) => Math.max(b.arr, b.done) / perMin),
    1,
  );
  const yMax = niceMax(peak * 1.1);
  const span = { x: w - PAD.l - PAD.r, y: h - PAD.t - PAD.b };
  const X = (t: number) => PAD.l + (t / ticks) * span.x;
  // Clamped: bin 0 sits above yMax by construction and an SVG path running
  // off the top of the viewBox would overdraw the chart above it.
  const Y = (v: number) => h - PAD.b - (Math.min(v, yMax) / yMax) * span.y;
  return {
    w,
    h,
    pad: PAD,
    yMax,
    xTicks: xTicksFor((ticks * doc.dt) / 60, w, PAD),
    yTicks: yTicksFor(yMax, h, PAD, 2),
    series: [
      { key: "arr", label: "arriving", path: pathOf(bins, (b) => b.arr / perMin, X, Y) },
      { key: "done", label: "completing", path: pathOf(bins, (b) => b.done / perMin, X, Y) },
      { key: "strand", label: "stranded", path: pathOf(bins, (b) => b.strand / perMin, X, Y) },
    ],
    arrivalsStopX: doc.lastEntryTick < ticks ? X(doc.lastEntryTick) : null,
  };
}

// clipRate is the bin-0 arrival rate per minute, or null when it does not
// exceed the rate chart's axis (a run with no startup burst).
export function clipRate(doc: FlowDoc, yMax: number): number | null {
  const perMin = (doc.binTicks * doc.dt) / 60;
  const r = doc.bins[0]!.arr / perMin;
  return r > yMax ? r : null;
}

// markerX is where the current-tick line goes, clamped into the plot: a
// replay parked at the horizon must land ON the right edge, and a live run
// that has outrun the report must not draw outside it.
export function markerX(doc: FlowDoc, tick: number, w: number): number {
  const t = Math.max(0, Math.min(tick, doc.ticks));
  return PAD.l + (t / doc.ticks) * (w - PAD.l - PAD.r);
}

// binAt is the bin covering a tick — what the readout under the charts
// describes. Bins are uniform, so this is an index, not a search.
export function binAt(doc: FlowDoc, tick: number): FlowBin {
  const i = Math.max(0, Math.min(Math.floor(tick / doc.binTicks), doc.bins.length - 1));
  return doc.bins[i]!;
}

// phaseAt names what the network is doing at a tick, from the accumulation
// slope over the surrounding bins. This is a LABEL for a slope, deliberately
// not a judgement: "draining" says the count is falling, not that the run
// will clear.
export function phaseAt(doc: FlowDoc, tick: number): "filling" | "holding" | "draining" {
  const i = Math.max(0, Math.min(Math.floor(tick / doc.binTicks), doc.bins.length - 1));
  const a = doc.bins[Math.max(0, i - 1)]!.inNet;
  const b = doc.bins[Math.min(doc.bins.length - 1, i + 1)]!.inNet;
  const d = b - a;
  // 1% of peak over two bins is the deadband — without it the label flickers
  // between filling and draining on ordinary bin-to-bin noise at the plateau.
  const eps = Math.max(1, doc.totals.peakInNet * 0.01);
  if (d > eps) return "filling";
  if (d < -eps) return "draining";
  return "holding";
}

// ── DOM shell ────────────────────────────────────────────────────────────

const SVG_NS = "http://www.w3.org/2000/svg";

function svg(tag: string, attrs: Record<string, string>): SVGElement {
  const e = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  return e;
}

function el(tag: string, cls?: string, text?: string): HTMLElement {
  const e = document.createElement(tag);
  if (cls !== undefined) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const fmt = (n: number): string => n.toLocaleString();

export interface FlowPanelOpts {
  url: string;
}

export class FlowPanel {
  private root: HTMLElement;
  private url: string;
  private doc: FlowDoc | null = null;
  private tick = 0;
  // The three marker lines and the readout are the only things that change
  // per tick — the charts themselves are built once. Redrawing 181-point
  // paths every snapshot would be pure waste at 10 Hz.
  private markers: SVGElement[] = [];
  private readout: HTMLElement | null = null;
  private collapsed = true;

  constructor(root: HTMLElement, opts: FlowPanelOpts) {
    this.root = root;
    this.url = opts.url;
  }

  async init(): Promise<void> {
    let doc: unknown;
    try {
      const r = await fetch(this.url, { cache: "no-store" });
      if (!r.ok) throw new Error(String(r.status));
      doc = await r.json();
    } catch {
      // Optional panel: no flow document for this run, so no panel. Same
      // contract as the stats and replay panels.
      this.root.style.display = "none";
      return;
    }
    if (!isFlowDoc(doc)) {
      this.root.style.display = "none";
      return;
    }
    this.doc = doc;
    this.render();
  }

  // setTick moves the marker. Called from the snapshot stream, so it must be
  // cheap and must tolerate being called before init() resolves.
  setTick(tick: number): void {
    this.tick = tick;
    const doc = this.doc;
    if (doc === null) return;
    const x = markerX(doc, tick, this.width());
    for (const m of this.markers) {
      m.setAttribute("x1", String(x));
      m.setAttribute("x2", String(x));
    }
    if (this.readout !== null) {
      const b = binAt(doc, tick);
      const mins = (tick * doc.dt) / 60;
      this.readout.textContent =
        `${mins.toFixed(1)} min · ${fmt(b.inNet)} in network · ${phaseAt(doc, tick)}`;
    }
  }

  private width(): number {
    // Fixed viewBox width; the SVG scales to the panel via CSS. Keeping the
    // coordinate space constant means marker x values stay valid across
    // panel resizes without a re-layout pass.
    return 260;
  }

  private render(): void {
    const doc = this.doc!;
    this.root.replaceChildren();
    this.markers = [];

    const head = el("div", "fp-head");
    const title = el("button", "fp-toggle");
    title.textContent = "flow";
    title.title = "arrivals, departures and accumulation over the run";
    const body = el("div", "fp-body");
    title.onclick = () => {
      this.collapsed = !this.collapsed;
      body.style.display = this.collapsed ? "none" : "block";
      title.classList.toggle("fp-open", !this.collapsed);
    };
    head.appendChild(title);
    this.root.appendChild(head);

    this.readout = el("div", "fp-readout");
    this.root.appendChild(this.readout);

    const T = doc.totals;
    const cards = el("div", "fp-cards");
    for (const [k, v] of [
      ["in", fmt(T.injected)],
      ["done", fmt(T.completed)],
      ["stranded", fmt(T.stranded)],
      ["peak", fmt(T.peakInNet)],
    ] as const) {
      const c = el("div", "fp-card");
      c.appendChild(el("div", "fp-k", k));
      c.appendChild(el("div", "fp-v", v));
      cards.appendChild(c);
    }
    body.appendChild(cards);

    const W = this.width();
    body.appendChild(
      this.chart(
        cumulativeGeom(doc, W, 108),
        "cumulative in vs out",
        "vertical gap = vehicles in network; horizontal gap = how long they wait",
      ),
    );
    body.appendChild(this.chart(accumulationGeom(doc, W, 64), "vehicles in network", null));
    const rg = rateGeom(doc, W, 64);
    const clipped = clipRate(doc, rg.yMax);
    body.appendChild(
      this.chart(
        rg,
        "per minute",
        clipped === null
          ? null
          : `startup burst of ${Math.round(clipped).toLocaleString()}/min clips off the top`,
      ),
    );

    body.style.display = this.collapsed ? "none" : "block";
    this.root.appendChild(body);
    this.setTick(this.tick);
  }

  private chart(g: ChartGeom, label: string, note: string | null): HTMLElement {
    const wrap = el("div", "fp-chart");
    wrap.appendChild(el("div", "fp-sec", label));
    const s = svg("svg", {
      class: "fp-svg",
      viewBox: `0 0 ${g.w} ${g.h}`,
      preserveAspectRatio: "none",
    });
    for (const t of g.yTicks) {
      s.appendChild(
        svg("path", {
          class: "fp-grid",
          d: `M ${g.pad.l} ${t.pos} L ${g.w - g.pad.r} ${t.pos}`,
        }),
      );
      const tx = svg("text", {
        class: "fp-tick",
        x: String(g.pad.l - 3),
        y: String(t.pos + 3),
        "text-anchor": "end",
      });
      tx.textContent = t.label;
      s.appendChild(tx);
    }
    for (const t of g.xTicks) {
      const tx = svg("text", {
        class: "fp-tick",
        x: String(t.pos),
        y: String(g.h - 4),
        "text-anchor": "middle",
      });
      tx.textContent = t.label;
      s.appendChild(tx);
    }
    if (g.arrivalsStopX !== null) {
      s.appendChild(
        svg("line", {
          class: "fp-stop",
          x1: String(g.arrivalsStopX),
          x2: String(g.arrivalsStopX),
          y1: String(g.pad.t),
          y2: String(g.h - g.pad.b),
        }),
      );
    }
    for (const ser of g.series) {
      if (ser.fill !== undefined) {
        s.appendChild(svg("path", { class: `fp-fill fp-s-${ser.key}`, d: ser.fill }));
      }
    }
    for (const ser of g.series) {
      s.appendChild(svg("path", { class: `fp-line fp-s-${ser.key}`, d: ser.path }));
    }
    // The marker is added last so it draws over every series.
    const m = svg("line", {
      class: "fp-now",
      x1: "0",
      x2: "0",
      y1: String(g.pad.t),
      y2: String(g.h - g.pad.b),
    });
    s.appendChild(m);
    this.markers.push(m);
    wrap.appendChild(s);

    const key = el("div", "fp-key");
    for (const ser of g.series) {
      key.appendChild(el("i", `fp-swatch fp-s-${ser.key}`));
      key.appendChild(el("span", undefined, ser.label));
    }
    wrap.appendChild(key);
    if (note !== null) wrap.appendChild(el("div", "fp-note", note));
    return wrap;
  }
}
