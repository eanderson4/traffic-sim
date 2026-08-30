// gamedrawer.ts — hero's full-screen game mode ("Beat the Signal",
// docs/show/signal-game-plan-2026-08-29): the 3D sim view IS the game. A
// slim always-visible edge tab (or the `g` key) slides a right-side drawer
// over the canvas holding the whole loop: edit the four-junction design
// (rows open the junction timing panel, hero/timingpanel.ts), run it,
// watch the reveal, browse the leaderboard. Pure DOM — no THREE import,
// like timingpanel.ts.
//
// Design state is the PURE half (DesignState + buildFullRunPayload below,
// unit-tested in test/gamedrawer.test.ts): all four junctions start at the
// town's base timing and accumulate "Apply to design" edits from the
// panel. The dial constants and validation live in timingpanel.ts (the
// one home; they copy viz/public/play.html's dial core verbatim).
//
// Capture hook: ?game=1 opens the drawer on load. ?bare=1 hides the
// drawer + tab like the rest of the HUD chrome.

import {
  BASE_GREENS,
  JUNCTIONS,
  NAME_ERR_MSG,
  NAME_RE,
  VIEW_ONLY_NOTE,
  cycleOf,
  isBaseDial,
  junctionDialOf,
  validateDial,
  type JunctionDial,
  type RunPayload,
} from "./timingpanel.ts";

// --- design state (pure, unit-tested) ----------------------------------------

export interface JunctionDesign {
  greens: number[];
  offset: number;
}

export const DEFAULT_DESIGN_NAME = "hero-design";

// DesignState accumulates the game's four-junction design. get returns
// defensive copies and set stores them, so panel/drawer code can never
// alias into the state. "edited" is not a flag — a junction is edited
// exactly while its values differ from the town's base timing.
export class DesignState {
  name: string = DEFAULT_DESIGN_NAME;
  readonly junctions: readonly string[];
  private readonly values = new Map<string, JunctionDesign>();

  constructor(junctions: readonly string[] = JUNCTIONS) {
    this.junctions = junctions;
    for (const j of junctions) this.values.set(j, { greens: [...BASE_GREENS], offset: 0 });
  }

  get(junction: string): JunctionDesign {
    const d = this.values.get(junction) ?? { greens: [...BASE_GREENS], offset: 0 };
    return { greens: [...d.greens], offset: d.offset };
  }

  set(junction: string, greens: readonly number[], offset: number): void {
    this.values.set(junction, { greens: [...greens], offset });
  }

  isEdited(junction: string): boolean {
    const d = this.values.get(junction);
    return d !== undefined && !isBaseDial(d.greens, d.offset);
  }

  // entries iterates in junction order (insertion order of the
  // constructor's reset) — the payload's key order mirrors play.html's.
  entries(): Array<[string, JunctionDesign]> {
    return [...this.values.entries()].map(([j, d]) => [j, { greens: [...d.greens], offset: d.offset }]);
  }
}

// buildFullRunPayload assembles the /api/timing/run body for the FULL dial:
// every junction rides (play.html's round-2 shape), offset omitted at 0.
export function buildFullRunPayload(name: string, design: DesignState): RunPayload {
  const junctions: Record<string, JunctionDial> = {};
  for (const [j, d] of design.entries()) junctions[j] = junctionDialOf(d.greens, d.offset);
  return { name, junctions };
}

// slugifyJS mirrors score-timing.py's slugify (copied in play.html) — the
// leaderboard highlights the row whose slug matches the current ?run=.
export function slugifyJS(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

// runSlugOf extracts the leaderboard slug from a ?run= param: hero URLs
// may pin a bake as <slug>/<hash12> (post-run navigations do), but the
// board row's slug is <slug> — comparing the whole param never matches.
export function runSlugOf(runParam: string): string {
  return runParam.split("/")[0] ?? "";
}

// --- the drawer ----------------------------------------------------------------

export interface GameDrawerOpts {
  design: DesignState;
  // onEditJunction opens the timing panel for a junction (row click).
  onEditJunction: (junction: string) => void;
  // currentRun is the page's ?run= value — the leaderboard highlights it.
  currentRun: string;
}

interface BoardRow {
  name?: unknown;
  kind?: unknown;
  mean_time_loss_s?: unknown;
  trips_completed?: unknown;
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

// GameDrawer is the slide-over panel + its edge tab. It eats only its own
// pointer events (the canvas keeps orbit/picking everywhere else) and
// never closes on Esc — Esc belongs to the junction panel.
export class GameDrawer {
  readonly root: HTMLElement;
  private readonly tab: HTMLButtonElement;
  private readonly opts: GameDrawerOpts;
  private isOpen = false;
  private running = false;
  private elapsedTimer: number | null = null;
  private flashTimer: number | null = null;
  private navTimer: number | null = null;
  private runnerLive: boolean | null = null; // null = probe in flight

  private readonly nameInput: HTMLInputElement;
  private readonly rows = new Map<string, HTMLDivElement>();
  private readonly runBtn = mk("button", "gd-run", "Run my design");
  private readonly spinEl = mk("span", "gd-spin");
  private readonly elapsedEl = mk("span", "gd-elapsed");
  private readonly capEl = mk(
    "div",
    "gd-cap",
    "usually ~20 seconds; it can take a few minutes when the box is busy — the simulation retries on its own",
  );
  private readonly errEl = mk("div", "gd-err");
  private readonly revealEl = mk("div", "gd-reveal");
  private readonly viewOnlyEl = mk("div", "gd-hint", VIEW_ONLY_NOTE);
  private readonly lbStatus = mk("p", "gd-lb-status", "—");
  private readonly lbBars = mk("div", "gd-lbars");

  constructor(opts: GameDrawerOpts) {
    this.opts = opts;

    this.tab = mk("button", "", "Game");
    this.tab.id = "gtab";
    this.tab.type = "button";
    this.tab.title = "open the game drawer (g)";
    this.tab.addEventListener("click", () => {
      this.toggle();
    });
    document.body.appendChild(this.tab);

    this.root = mk("aside", "");
    this.root.id = "gdrawer";

    const head = mk("div", "gd-head");
    head.appendChild(mk("span", "gd-title", "Beat the Signal"));
    const x = mk("button", "gd-x", "×");
    x.type = "button";
    x.title = "close (g)";
    x.addEventListener("click", () => {
      this.close();
    });
    head.appendChild(x);
    this.root.appendChild(head);
    this.root.appendChild(
      mk(
        "p",
        "gd-sub",
        "Same network, same demand, same seed (seed 42, 15,000 ticks ≈ 25 min of traffic) — " +
          "the only thing that changes between runs is the light timing.",
      ),
    );

    // --- DESIGN
    const designSec = mk("div", "gd-sec");
    designSec.appendChild(mk("div", "gd-sectitle", "DESIGN"));
    this.nameInput = document.createElement("input");
    this.nameInput.type = "text";
    this.nameInput.value = opts.design.name;
    this.nameInput.spellcheck = false;
    this.nameInput.pattern = "[a-z0-9]([a-z0-9-]*[a-z0-9])?";
    this.nameInput.addEventListener("input", () => {
      opts.design.name = this.nameInput.value;
    });
    this.nameInput.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        void this.run();
      }
    });
    const nameRow = mk("div", "gd-namerow");
    nameRow.appendChild(mk("label", "", "Design name"));
    nameRow.appendChild(this.nameInput);
    designSec.appendChild(nameRow);
    designSec.appendChild(
      mk("div", "gd-hint", "use a fresh name per run — the compiler refuses a reused name with different timing"),
    );
    for (const j of opts.design.junctions) {
      const row = mk("div", "gd-row");
      row.addEventListener("click", () => {
        this.opts.onEditJunction(j);
      });
      this.rows.set(j, row);
      designSec.appendChild(row);
    }
    this.root.appendChild(designSec);

    // --- RUN
    const runSec = mk("div", "gd-sec");
    runSec.appendChild(mk("div", "gd-sectitle", "RUN"));
    const bar = mk("div", "gd-runbar");
    this.runBtn.type = "button";
    this.runBtn.addEventListener("click", () => {
      void this.run();
    });
    this.spinEl.hidden = true;
    bar.appendChild(this.runBtn);
    bar.appendChild(this.spinEl);
    bar.appendChild(this.elapsedEl);
    runSec.appendChild(bar);
    this.capEl.hidden = true;
    runSec.appendChild(this.capEl);
    runSec.appendChild(this.errEl);
    this.viewOnlyEl.hidden = true;
    runSec.appendChild(this.viewOnlyEl);
    this.revealEl.hidden = true;
    runSec.appendChild(this.revealEl);
    this.root.appendChild(runSec);

    // --- LEADERBOARD
    const lbSec = mk("div", "gd-sec");
    lbSec.appendChild(mk("div", "gd-sectitle", "LEADERBOARD"));
    lbSec.appendChild(this.lbStatus);
    lbSec.appendChild(this.lbBars);
    this.root.appendChild(lbSec);

    document.body.appendChild(this.root);
    this.refreshDesign();
  }

  open(): void {
    if (this.isOpen) return;
    this.isOpen = true;
    this.root.classList.add("open");
    document.body.classList.add("gd-open"); // shifts the timing panel left of the drawer
    this.tab.hidden = true;
    void this.loadBoard();
  }

  close(): void {
    if (!this.isOpen) return;
    this.isOpen = false;
    this.root.classList.remove("open");
    document.body.classList.remove("gd-open");
    this.tab.hidden = false;
  }

  toggle(): void {
    if (this.isOpen) this.close();
    else this.open();
  }

  // setRunnerAvailable applies the probe verdict (main.ts resolves it once
  // and shares it with the timing panel): view-only disables Run my design
  // and shows the notice where the button's status lives. The design
  // itself stays editable — it's client-side state.
  setRunnerAvailable(live: boolean): void {
    this.runnerLive = live;
    this.runBtn.disabled = !live || this.running;
    this.runBtn.title = live ? "" : "view-only link — the run service isn't on this host";
    this.viewOnlyEl.hidden = live;
  }

  // refreshDesign repaints the four junction rows from the design state
  // (values + computed cycle + the edited marker).
  refreshDesign(): void {
    for (const [j, row] of this.rows) {
      const d = this.opts.design.get(j);
      row.textContent = "";
      row.appendChild(mk("span", "gd-jid", j));
      const vals = `${d.greens.join("/")} s · offset ${d.offset} s · cycle ${cycleOf(d.greens)} s`;
      const mid = mk("span", "gd-vals", vals);
      row.appendChild(mid);
      if (this.opts.design.isEdited(j)) row.appendChild(mk("span", "gd-edited", "edited"));
    }
  }

  // flashRow pulses a junction row (after the panel's Apply writes it).
  flashRow(junction: string): void {
    const row = this.rows.get(junction);
    if (row === undefined) return;
    row.classList.remove("flash");
    void row.offsetWidth; // restart the animation on a repeated apply
    row.classList.add("flash");
    if (this.flashTimer !== null) clearTimeout(this.flashTimer);
    this.flashTimer = window.setTimeout(() => row.classList.remove("flash"), 2000);
  }

  private setError(msg: string, isNotice: boolean): void {
    this.errEl.textContent = msg;
    this.errEl.classList.toggle("notice", isNotice);
  }

  private setRunning(on: boolean): void {
    this.running = on;
    this.runBtn.disabled = on || this.runnerLive === false;
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
    this.setError(msg, isConflict); // the design stays editable; the button is back
  }

  // run POSTs the full four-junction dial. On 200 the drawer shows the
  // reveal (big mean time loss, trips, rank, the green-wave line when the
  // design leads), refreshes the board, then navigates to the fresh bake.
  private async run(): Promise<void> {
    if (this.running || this.runnerLive === false) return;
    const name = this.opts.design.name.trim();
    if (!NAME_RE.test(name)) {
      this.setError(NAME_ERR_MSG, false);
      return;
    }
    for (const [j, d] of this.opts.design.entries()) {
      const v = validateDial(d.greens, d.offset);
      if (v.errs.length > 0) {
        this.setError(`${j}: ${v.errs[0]}`, false);
        return;
      }
    }
    const payload = buildFullRunPayload(name, this.opts.design);

    this.setRunning(true);
    this.setError("", false);
    this.revealEl.hidden = true;
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
      this.setRunning(false);
      this.showReveal(data);
      void this.loadBoard();
      const url = data["hero_url"];
      this.navTimer = window.setTimeout(() => location.assign(url), 4000);
      return;
    }
    if (resp.status === 409) {
      this.fail("another run is in progress — give it a moment", true);
      return;
    }
    const msg = data !== null && typeof data["error"] === "string" ? data["error"] : `the run service answered HTTP ${resp.status}`;
    this.fail(msg, false);
  }

  // showReveal fills the in-drawer result card: the big loss number, the
  // trips/rank line, and the green-wave celebration when the design leads.
  private showReveal(data: Record<string, unknown>): void {
    this.revealEl.textContent = "";
    const loss = Number(data["mean_time_loss_s"]);
    const big = mk("div", "gd-big");
    big.appendChild(mk("span", "", Number.isFinite(loss) ? loss.toFixed(1) : "?"));
    big.appendChild(mk("span", "gd-unit", "s/veh mean time loss"));
    this.revealEl.appendChild(big);
    this.revealEl.appendChild(
      mk(
        "div",
        "gd-rsub",
        `${String(data["trips_completed"])} trips completed · rank ${String(data["rank"])} of ${String(data["of"])}`,
      ),
    );
    if (Number(data["rank"]) === 1) {
      this.revealEl.appendChild(mk("div", "gd-beat", "You beat the green wave."));
    }
    this.revealEl.appendChild(mk("div", "gd-hint", "loading your bake…"));
    this.revealEl.hidden = false;
  }

  // --- leaderboard -------------------------------------------------------------

  private async loadBoard(): Promise<void> {
    this.lbStatus.textContent = "Loading…";
    let data: { rows?: BoardRow[] } | null;
    try {
      const resp = await fetch("/timing-game-results.json", { cache: "no-cache" });
      if (resp.status === 404) {
        this.lbStatus.textContent = "No scores yet — run a design and this fills in.";
        this.lbBars.textContent = "";
        return;
      }
      if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
      data = (await resp.json()) as { rows?: BoardRow[] } | null;
    } catch (e) {
      this.lbStatus.textContent = `Could not load scores (${e instanceof Error ? e.message : String(e)}).`;
      return;
    }
    const rows = (data?.rows ?? []).slice();
    if (rows.length === 0) {
      this.lbStatus.textContent = "No scores yet — run a design and this fills in.";
      this.lbBars.textContent = "";
      return;
    }
    this.lbStatus.textContent = "";
    this.renderBars(rows);
  }

  // renderBars: play.html's bar language, lean — rows sorted best-first
  // (lowest time loss wins), bars scale to the WORST row so the bar IS the
  // loss, baselines dim, guest rows accent, the current ?run= row lit.
  private renderBars(rows: BoardRow[]): void {
    this.lbBars.textContent = "";
    const sorted = rows
      .map((r) => ({
        name: String(r.name ?? ""),
        kind: String(r.kind ?? "guest"),
        loss: Number(r.mean_time_loss_s),
        trips: Number(r.trips_completed),
      }))
      .sort((a, b) => a.loss - b.loss);
    let worst = 0;
    for (const r of sorted) worst = Math.max(worst, Number.isFinite(r.loss) ? r.loss : 0);
    if (worst <= 0) worst = 1;
    const youSlug = slugifyJS(this.opts.currentRun);
    const fills: Array<{ el: HTMLElement; pct: number }> = [];
    for (const r of sorted) {
      const guest = r.kind !== "baseline";
      const you = youSlug !== "" && (slugifyJS(r.name) === youSlug || r.name === this.opts.currentRun);
      const row = mk("div", `gd-lrow${guest ? " guest" : ""}${you ? " you" : ""}`);
      row.appendChild(mk("span", "gd-lname", r.name));
      const track = mk("div", "gd-track");
      const fill = mk("div", `gd-fill${guest ? " guest" : ""}`);
      track.appendChild(fill);
      row.appendChild(track);
      const loss = Number.isFinite(r.loss) ? r.loss.toFixed(1) : "?";
      row.appendChild(mk("span", "gd-lval", `${loss} s/veh · ${r.trips} trips`));
      this.lbBars.appendChild(row);
      fills.push({ el: fill, pct: Math.max(2, ((Number.isFinite(r.loss) ? r.loss : 0) / worst) * 100) });
    }
    // widths land a frame after insertion so the CSS transition sweeps in
    requestAnimationFrame(() => {
      requestAnimationFrame(() => {
        for (const f of fills) f.el.style.width = `${f.pct}%`;
      });
    });
  }
}
