// timingpanel.ts — the hero page's junction timing overlay ("Beat the
// Signal", docs/show/signal-game-plan-2026-08-29): click a signal head in
// the 3D view (or a design row in the game drawer) → an HTML panel over
// the canvas shows that junction's LIVE phase state and an editable
// timing form. The PRIMARY action is "Apply to design": the edited
// greens/offset are written into the shared design state (hero/gamedrawer.ts)
// and the drawer takes it from there. A small ghost "Run just this
// junction" keeps the rehearsed quick path: POST the single-junction dial
// to /api/timing/run (absent junctions keep base timing, the documented
// endpoint semantics) and navigate to the fresh bake on success.
//
// OVERLAY API for embedders (the host page wraps hero in an iframe):
//   hero → parent, on every junction select (click or ?panel=):
//     { type: "hero:select-junction", junction: "J2" }
//   hero → parent, on deselect (empty-space click, Esc, panel ×):
//     { type: "hero:deselect" }
// Posted via window.parent.postMessage(..., "*") only when hero actually
// runs inside a frame. viz/public/play.html listens and focuses the
// matching round-2 dials.
//
// Debug/capture hook: ?panel=<junctionId> opens the panel pre-selected
// (the click path itself can't be driven by the screenshot harness).
//
// SCENARIO CONSTANTS are copied VERBATIM from viz/public/play.html's dial
// core (the source of truth; it in turn mirrors
// scripts/demos/bottleneck_town.py and mktimingvariant.py's validation) —
// gamedrawer.ts imports them from HERE, so this module is the one home
// for the dial rules. The form always defaults to the DESIGN's current
// values for the junction (base timing until first edit) — never reverse-
// engineered from the bake being viewed, which may itself be a variant.

import type { SigProgram, SignalTable } from "../tssg.ts";

// --- dial constants (copy of play.html — keep in sync) ----------------------
export const JUNCTIONS = ["J1", "J2", "J3", "J4"] as const;
export const BASE_GREENS = [36, 8, 11, 11] as const;
export const FIXED_S = 20; // 4 × (3 s amber + 2 s all-red)
export const GREEN_LABELS = [
  "Main St — through + right, both directions (base 36 s)",
  "Main St — protected lefts, both directions (base 8 s)",
  "Cross street — northbound, all movements (base 11 s)",
  "Cross street — southbound, all movements (base 11 s)",
] as const;
export const GREEN_MIN = 5;
export const GREEN_MAX = 90;
export const CYCLE_MIN = 40;
export const CYCLE_MAX = 150;
export const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
export const NAME_ERR_MSG = "design name must be lowercase letters, digits, dashes (e.g. hero-design).";

// --- pure logic (unit-tested in test/timingpanel.test.ts) -------------------

const num = (v: number): boolean => Number.isFinite(v);
const fmt = (x: number): string => String(Math.round(x * 100) / 100);
const asNum = (x: number): number => Math.round(x * 100) / 100;

// cycleOf: greens + the fixed 20 s clearance; non-numbers count 0 so the
// readout still shows something while a field is half-typed.
export function cycleOf(greens: readonly number[]): number {
  let sum: number = FIXED_S;
  for (const g of greens) sum += num(g) ? g : 0;
  return sum;
}

// validateDial mirrors play.html's (and mktimingvariant.py's) validation
// exactly, message strings included — a design the panel rejects would be
// rejected by the compiler, and vice versa.
export function validateDial(greens: readonly number[], offset: number): { errs: string[]; cycle: number } {
  const errs: string[] = [];
  for (let i = 0; i < 4; i++) {
    const g = greens[i] ?? NaN;
    if (!num(g)) errs.push(`green ${i + 1} needs a number`);
    else if (g < GREEN_MIN || g > GREEN_MAX) {
      errs.push(`${GREEN_LABELS[i]!.split(" (base")[0]}: ${fmt(g)} s is outside [${GREEN_MIN}, ${GREEN_MAX}] s`);
    }
  }
  const cycle = cycleOf(greens);
  if (cycle < CYCLE_MIN || cycle > CYCLE_MAX) {
    errs.push(`cycle ${fmt(cycle)} s is outside [${CYCLE_MIN}, ${CYCLE_MAX}] s (greens + 20 s amber/all-red)`);
  }
  if (!num(offset)) errs.push("offset needs a number (0 is fine)");
  else if (offset < 0 || offset >= cycle) {
    errs.push(`offset ${fmt(offset)} s is outside [0, ${fmt(cycle)}) — offsets wrap at the cycle length`);
  }
  return { errs, cycle };
}

export interface JunctionDial {
  greens: number[];
  offset?: number;
}

export interface RunPayload {
  name: string;
  junctions: Record<string, JunctionDial>;
}

// junctionDialOf mirrors play.html's junctionDial: numbers rounded to 2 dp,
// offset omitted at 0 (like green-wave's J1). Shared by both payload
// builders (single-junction here, full dial in gamedrawer.ts).
export function junctionDialOf(greens: readonly number[], offset: number): JunctionDial {
  const dial: JunctionDial = { greens: greens.map(asNum) };
  if (num(offset) && offset !== 0) dial.offset = asNum(offset);
  return dial;
}

// buildRunPayload assembles the /api/timing/run body for ONE junction (the
// endpoint keeps base timing for absent junctions) — the ghost button's
// quick path.
export function buildRunPayload(
  name: string,
  junction: string,
  greens: readonly number[],
  offset: number,
): RunPayload {
  return { name, junctions: { [junction]: junctionDialOf(greens, offset) } };
}

// isBaseDial: true while a junction's values are exactly the town's base
// timing — drives the drawer's "edited" marker and the panel's note line.
export function isBaseDial(greens: readonly number[], offset: number): boolean {
  return offset === 0 && greens.length === BASE_GREENS.length && greens.every((g, i) => g === BASE_GREENS[i]);
}

// --- view-only mode (shared by the drawer, the panel, and main.ts's probe) ---

// VIEW_ONLY_NOTE is the notice shown where a disabled Run button's status
// lives — one copy for drawer + panel (play.html carries its own).
export const VIEW_ONLY_NOTE =
  "View-only link — designs run on the host's machine during the show; the replay, fleet, and leaderboard all work here.";

// runnerAvailable classifies the health-probe response: the timing-runner
// answers GET /api/timing/run with 200; a static host (the public Pages
// bundle) 404s/405s, and a network error surfaces as null — anything but
// 200 is view-only.
export function runnerAvailable(status: number | null): boolean {
  return status === 200;
}

// movementLabel names the k-th green phase (0-based) after the game's
// movement labels, minus the "(base N s)" suffix.
function movementLabel(greenOrdinal: number): string {
  const l = GREEN_LABELS[greenOrdinal];
  return l !== undefined ? l.split(" (base")[0]! : `green phase ${greenOrdinal + 1}`;
}

// livePhaseLabel renders the program's phase in force at tick as a friendly
// line: the movement + color + whole seconds left in the phase. The walk
// mirrors tssg.ts phaseIndexAt (SUMO offset semantics) but also tracks the
// remaining ticks and the green-phase ordinal. Green phases take their
// movement name; an amber phase belongs to the green it follows; an all-r
// phase is "all-red clearance".
export function livePhaseLabel(p: SigProgram, tick: number, dt: number): string {
  let cycle = 0;
  for (const ph of p.phases) cycle += ph.durationTicks;
  if (cycle === 0 || p.phases.length === 0) return "no program";
  let x = ((tick % cycle) + cycle - (p.offsetTicks % cycle)) % cycle;
  let greenSeen = 0; // green phases BEFORE the current one
  for (const ph of p.phases) {
    const hasG = /[gG]/.test(ph.state);
    if (x < ph.durationTicks) {
      const leftS = Math.max(1, Math.ceil((ph.durationTicks - x) * dt));
      if (hasG) return `${movementLabel(greenSeen)} — green · ${leftS} s left`;
      if (ph.state.includes("y")) {
        const m = greenSeen > 0 ? movementLabel(greenSeen - 1) : "amber";
        return `${m} — amber · ${leftS} s left`;
      }
      return `all-red clearance · ${leftS} s left`;
    }
    if (hasG) greenSeen++;
    x -= ph.durationTicks;
  }
  return "—"; // unreachable (x < cycle), kept total
}

// --- the panel ---------------------------------------------------------------

export interface TimingPanelOpts {
  table: SignalTable; // the viewed bake's program table (for the live label)
  dt: number; // sim seconds per tick
  // prefill supplies the form defaults for a junction — the design's
  // current values (base timing until first edit).
  prefill: (junction: string) => { greens: readonly number[]; offset: number };
  // getName supplies the design name for the ghost single-junction run
  // (the name field lives in the game drawer).
  getName: () => string;
  // onApply is the primary action's sink: the validated greens/offset for
  // the selected junction (the drawer persists + reflects them).
  onApply: (junction: string, greens: number[], offset: number) => void;
}

// postToParent implements the overlay API: only posts when hero is framed.
function postToParent(msg: Record<string, string>): void {
  if (window.parent !== window) window.parent.postMessage(msg, "*");
}

function mk<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  className: string,
  text = "",
): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (className !== "") e.className = className;
  if (text !== "") e.textContent = text;
  return e;
}

// TimingPanel is a self-contained DOM overlay (no framework, hero HUD
// styling). main.ts drives three methods: select(junction) on a signal-head
// click or a drawer row click, deselect() on empty-space/Esc/Apply, and
// setTick(tick) from the render loop (no-op while closed or when the baked
// frame hasn't advanced).
export class TimingPanel {
  readonly root: HTMLDivElement;
  private readonly opts: TimingPanelOpts;
  private junction: string | null = null;
  private program: SigProgram | null = null;
  private lastTick = -1;
  private running = false;
  private elapsedTimer: number | null = null;
  private runnerLive: boolean | null = null; // null = probe in flight

  private readonly titleEl = mk("span", "");
  private readonly liveEl = mk("div", "tp-live");
  private readonly greenInputs: HTMLInputElement[] = [];
  private readonly offsetInput: HTMLInputElement;
  private readonly cycleEl = mk("div", "tp-cycle");
  private readonly noteEl = mk("div", "tp-note");
  private readonly errEl = mk("div", "tp-err");
  private readonly applyBtn = mk("button", "tp-run", "Apply to design");
  private readonly runBtn = mk("button", "tp-run ghost", "Run just this junction");
  private readonly spinEl = mk("span", "tp-spin");
  private readonly elapsedEl = mk("span", "tp-elapsed");
  private readonly capEl = mk(
    "div",
    "tp-cap",
    "usually ~20 seconds; it can take a few minutes when the box is busy — the simulation retries on its own",
  );
  private readonly viewOnlyEl = mk("div", "tp-note", VIEW_ONLY_NOTE);

  constructor(opts: TimingPanelOpts) {
    this.opts = opts;
    this.root = mk("div", "hud");
    this.root.id = "tpanel";
    this.root.hidden = true;

    const head = mk("div", "tp-head");
    head.appendChild(this.titleEl);
    const x = mk("button", "tp-x", "×");
    x.type = "button";
    x.title = "close (Esc)";
    x.addEventListener("click", () => {
      this.deselect();
    });
    head.appendChild(x);
    this.root.appendChild(head);
    this.root.appendChild(this.liveEl);

    const enterApply = (ev: KeyboardEvent): void => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        this.apply();
      }
    };
    GREEN_LABELS.forEach((label, i) => {
      const inp = document.createElement("input");
      inp.type = "number";
      inp.min = String(GREEN_MIN);
      inp.max = String(GREEN_MAX);
      inp.step = "1";
      inp.value = String(BASE_GREENS[i]);
      inp.placeholder = String(BASE_GREENS[i]); // base values as placeholders
      inp.addEventListener("input", () => {
        this.update();
      });
      inp.addEventListener("keydown", enterApply);
      this.greenInputs.push(inp);
      this.root.appendChild(this.row(label, inp));
    });
    this.offsetInput = document.createElement("input");
    this.offsetInput.type = "number";
    this.offsetInput.min = "0";
    this.offsetInput.step = "1";
    this.offsetInput.value = "0";
    this.offsetInput.placeholder = "0";
    this.offsetInput.addEventListener("input", () => {
      this.update();
    });
    this.offsetInput.addEventListener("keydown", enterApply);
    this.root.appendChild(this.row("Offset (s; 0 = starts at sim start)", this.offsetInput));
    this.root.appendChild(this.cycleEl);
    this.root.appendChild(this.noteEl);
    this.root.appendChild(this.errEl);

    const bar = mk("div", "tp-bar");
    this.applyBtn.type = "button";
    this.applyBtn.addEventListener("click", () => {
      this.apply();
    });
    this.runBtn.type = "button";
    this.runBtn.title = "quick path: score this junction's timing on its own (the rest keep base timing)";
    this.runBtn.addEventListener("click", () => {
      void this.run();
    });
    this.spinEl.hidden = true;
    bar.appendChild(this.applyBtn);
    bar.appendChild(this.runBtn);
    bar.appendChild(this.spinEl);
    bar.appendChild(this.elapsedEl);
    this.root.appendChild(bar);
    this.capEl.hidden = true;
    this.root.appendChild(this.capEl);
    this.viewOnlyEl.hidden = true;
    this.root.appendChild(this.viewOnlyEl);

    document.body.appendChild(this.root);
  }

  get selected(): string | null {
    return this.junction;
  }

  // setRunnerAvailable applies the probe verdict (main.ts resolves it once
  // and shares it with the drawer): view-only disables the ghost quick Run
  // and shows the notice; Apply-to-design stays enabled (client-side).
  setRunnerAvailable(live: boolean): void {
    this.runnerLive = live;
    this.runBtn.disabled = !live || this.running;
    this.runBtn.title = live ? "" : "view-only link — the run service isn't on this host";
    this.viewOnlyEl.hidden = live;
  }

  // select opens the panel for a junction, prefilled from the design (base
  // timing until first edit — never reverse-engineered from the viewed
  // bake, which may itself be a guest variant), and notifies an embedding
  // parent frame.
  select(junction: string): void {
    this.junction = junction;
    this.program = this.opts.table.programs.find((p) => p.junction === junction) ?? null;
    this.titleEl.textContent = `${junction} · signal timing`;
    const pre = this.opts.prefill(junction);
    this.greenInputs.forEach((inp, i) => {
      inp.value = String(pre.greens[i] ?? BASE_GREENS[i]);
    });
    this.offsetInput.value = String(pre.offset);
    this.noteEl.textContent = isBaseDial(pre.greens, pre.offset)
      ? "defaults shown are the town's base timing"
      : "showing your current design for this junction";
    this.lastTick = -1; // force the live label to refresh on next setTick
    this.root.hidden = false;
    this.setError("", false);
    this.update();
    if (this.program === null) {
      this.liveEl.textContent = "now: (this bake carries no program for that junction)";
    }
    postToParent({ type: "hero:select-junction", junction });
  }

  deselect(): void {
    if (this.junction === null) return;
    this.junction = null;
    this.root.hidden = true;
    postToParent({ type: "hero:deselect" });
  }

  // setTick refreshes the live phase line. Called from hero's render loop
  // only when the displayed baked frame changes, so playback animates the
  // line at the baked cadence without per-frame work.
  setTick(tick: number): void {
    if (this.junction === null || tick === this.lastTick) return;
    this.lastTick = tick;
    if (this.program !== null) {
      this.liveEl.textContent = `now: ${livePhaseLabel(this.program, tick, this.opts.dt)}`;
    }
  }

  private row(label: string, input: HTMLInputElement): HTMLDivElement {
    const r = mk("div", "tp-row");
    const lab = mk("label", "", label);
    r.appendChild(lab);
    r.appendChild(input);
    return r;
  }

  private setError(msg: string, isNotice: boolean): void {
    this.errEl.textContent = msg;
    this.errEl.classList.toggle("notice", isNotice);
  }

  // update re-validates the form and repaints the cycle readout + inline
  // errors. Returns the validation result (apply/run gate on it).
  private update(): { errs: string[]; cycle: number } {
    const greens = this.greenInputs.map((i) => parseFloat(i.value));
    const off = parseFloat(this.offsetInput.value);
    this.greenInputs.forEach((e, i) => {
      const g = greens[i] ?? NaN;
      e.classList.toggle("bad", !num(g) || g < GREEN_MIN || g > GREEN_MAX);
    });
    this.offsetInput.classList.toggle("bad", !num(off) || off < 0);
    const v = validateDial(greens, num(off) ? off : NaN);
    const parts = greens.map((g) => (num(g) ? fmt(g) : "?")).join(" + ");
    this.cycleEl.textContent = `cycle = ${parts} + ${FIXED_S} s clearance = ${fmt(v.cycle)} s (base runs 86 s)`;
    this.setError(v.errs.join("; "), false);
    return v;
  }

  // apply is the primary action: validate, close, hand the values to the
  // design (the drawer persists, reflects, and flashes the row).
  private apply(): void {
    if (this.running || this.junction === null) return;
    const v = this.update();
    if (v.errs.length > 0) return;
    const j = this.junction;
    const greens = this.greenInputs.map((i) => parseFloat(i.value));
    const off = parseFloat(this.offsetInput.value);
    this.deselect();
    this.opts.onApply(j, greens, off);
  }

  private setRunning(on: boolean): void {
    this.running = on;
    this.runBtn.disabled = on || this.runnerLive === false;
    this.applyBtn.disabled = on;
    this.spinEl.hidden = !on;
    this.capEl.hidden = !on;
    if (!on) this.elapsedEl.textContent = "";
  }

  private stopElapsed(): void {
    if (this.elapsedTimer !== null) {
      clearInterval(this.elapsedTimer);
      this.elapsedTimer = null;
    }
  }

  private fail(msg: string, isConflict: boolean): void {
    this.stopElapsed();
    this.setRunning(false);
    this.setError(msg, isConflict); // the form stays editable; the buttons are back
  }

  // run is the ghost quick path: POST the single-junction dial and, on
  // success, swap the page to the fresh bake. 409 means the shared box is
  // busy — a notice, not an error.
  private async run(): Promise<void> {
    if (this.running || this.junction === null || this.runnerLive === false) return;
    const name = this.opts.getName().trim();
    if (!NAME_RE.test(name)) {
      this.setError(NAME_ERR_MSG, false);
      return;
    }
    const v = this.update();
    if (v.errs.length > 0) return;
    const greens = this.greenInputs.map((i) => parseFloat(i.value));
    const payload = buildRunPayload(name, this.junction, greens, parseFloat(this.offsetInput.value));

    this.setRunning(true);
    this.setError("", false);
    const t0 = Date.now();
    this.elapsedEl.textContent = "0 s";
    this.elapsedTimer = window.setInterval(() => {
      this.elapsedEl.textContent = `${Math.floor((Date.now() - t0) / 1000)} s`;
    }, 1000);

    let resp: Response;
    try {
      resp = await fetch("/api/timing/run", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: `${JSON.stringify(payload, null, 2)}\n`,
      });
    } catch (e) {
      this.fail(`could not reach the run service (${e instanceof Error ? e.message : String(e)})`, false);
      return;
    }
    const data = (await resp.json().catch(() => null)) as Record<string, unknown> | null;
    if (resp.status === 200 && data !== null && data["ok"] === true && typeof data["hero_url"] === "string") {
      this.stopElapsed();
      this.elapsedEl.textContent = "";
      this.spinEl.hidden = true;
      this.capEl.hidden = true;
      this.setError(`rank ${String(data["rank"])} of ${String(data["of"])} — loading your bake…`, true);
      location.assign(data["hero_url"]);
      return; // the page is navigating; leave the buttons disabled
    }
    if (resp.status === 409) {
      this.fail("another run is in progress — give it a moment", true);
      return;
    }
    const msg = data !== null && typeof data["error"] === "string" ? data["error"] : `the run service answered HTTP ${resp.status}`;
    this.fail(msg, false);
  }
}
