// replaypanel.ts — control panel for the replay driver (a recorded run
// republished faster than realtime under run id "{src}-replay"). demosrv
// proxies the replay child's control plane:
//
//   GET  /api/replay/status?run={expectedRun} → status JSON; 404 when no
//        replay is active; 409 when the ACTIVE replay is not the one this
//        page displays (expectedRun is the page's ?run= — the panel only
//        ever controls the replay the page is subscribed to, so a stale
//        deep link for replay A can never adopt/control replay B).
//   POST /api/replay/ctl/pause | /resume?run={expectedRun} (409 while
//        done — seek back first)
//   POST /api/replay/ctl/speed | /seek?run={expectedRun}
//
// The ?run= query param is the page's own run (config.ts ?run= — the run
// whose NATS subjects the map subscribes): demosrv 409s a MISMATCH (a
// stale tab vs the active replay) — surfaced as "another replay is
// active". The seek body is {"tick":T} (synchronous — 200 after landing),
// the speed body {"speed":N}.
//
// Every ctl POST returns the status JSON on success. The panel follows the
// switcher/modelpanel probe discipline: init probes the status (5 tries,
// 1 s apart — a startup hiccup must not hide the panel for the session)
// and hides only after the last failure (404 included), so a normal live
// demo never shows replay chrome. The stream side of a seek
// (non-monotonic ticks) is handled in the snapshot pipeline — SeekGate in
// snapshots.ts, wired in main.ts.
//
// Split per the repo convention (demos-core.ts/modelpanel.ts): the DOM-free
// half — ReplayControl plus parseStatus/toggleTarget/viewModel — carries
// every state transition so node --test can drive it with a stubbed fetch;
// ReplayPanel is the thin DOM shell.

export interface ReplayStatus {
  run: string; // source run id
  replayRun: string; // published run id ("{src}-replay")
  tick: number;
  ticks: number;
  endTick: number;
  speed: number;
  paused: boolean;
  done: boolean;
  dt: number;
  crcErrors: number; // replay-vs-recording divergence count — on-air trust signal
  verbErrors: number;
}

// FetchLike is the structural slice of fetch the control plane needs, so
// tests inject a stub and the panel defaults to the real thing.
export type FetchLike = (
  input: string,
  init?: { method?: string; body?: string; signal?: AbortSignal },
) => Promise<{ ok: boolean; status: number; json(): Promise<unknown> }>;

// parseStatus validates the demosrv payload defensively — a malformed body
// is treated like a failed probe (the panel hides).
export function parseStatus(body: unknown): ReplayStatus | null {
  if (typeof body !== "object" || body === null) return null;
  const b = body as Record<string, unknown>;
  const tick = b["tick"];
  const endTick = b["endTick"];
  const speed = b["speed"];
  const paused = b["paused"];
  const done = b["done"];
  const crcErrors = b["crcErrors"];
  if (
    typeof tick !== "number" ||
    typeof endTick !== "number" ||
    typeof speed !== "number" ||
    typeof paused !== "boolean" ||
    typeof done !== "boolean" ||
    typeof crcErrors !== "number"
  ) {
    return null;
  }
  return {
    run: typeof b["run"] === "string" ? b["run"] : "",
    replayRun: typeof b["replayRun"] === "string" ? b["replayRun"] : "",
    tick,
    ticks: typeof b["ticks"] === "number" ? b["ticks"] : 0,
    endTick,
    speed,
    paused,
    done,
    dt: typeof b["dt"] === "number" ? b["dt"] : 0,
    crcErrors,
    verbErrors: typeof b["verbErrors"] === "number" ? b["verbErrors"] : 0,
  };
}

// toggleTarget picks the ctl endpoint for the play/pause button: paused or
// done resumes (done → 409 until the user seeks back), playing pauses.
export function toggleTarget(s: Pick<ReplayStatus, "paused" | "done">): "pause" | "resume" {
  return s.paused || s.done ? "resume" : "pause";
}

export interface ReplayView {
  toggleLabel: string;
  toggleEndpoint: "pause" | "resume";
  ended: boolean;
  statusLine: string; // "tick 1200 / 36000 · 4×" (| paused | replay ended)
  divergence: number; // crcErrors — render prominently when > 0
}

export function viewModel(s: ReplayStatus): ReplayView {
  const endpoint = toggleTarget(s);
  const state = s.done ? "replay ended" : s.paused ? "paused" : `${s.speed}×`;
  return {
    toggleLabel: endpoint === "resume" ? "⏵ resume" : "⏸ pause",
    toggleEndpoint: endpoint,
    ended: s.done,
    statusLine: `tick ${s.tick} / ${s.endTick} · ${state}`,
    divergence: s.crcErrors,
  };
}

// Speed selector options (× realtime). SeekGate's forward-jump window
// (24 sim-seconds — snapshots.ts derives the tick threshold from dt)
// assumes this cap stays ≤ 8×: per-frame tick increments run
// speed · dt⁻¹ / 10 at 10 Hz delivery (8 at dt 0.1, 800 at dt 0.001) —
// far below the window (240 / 24000 ticks), so only deliberate scrubs
// trip the gate.
export const SPEEDS: readonly number[] = [1, 2, 4, 8];

export const SEEK_BACK_HINT = "replay ended — seek back first";
export const REPLAY_MISMATCH_HINT = "another replay is active";

// FailGate counts CONSECUTIVE poll failures: one transient miss (demosrv
// restarting, a dropped connection) must not hide the panel for the
// session — only a streak means the replay child is really gone.
export class FailGate {
  // NOTE: erasable-syntax only (node strip-only mode loads this directly).
  private streak = 0;
  private readonly limit: number;

  constructor(limit: number) {
    this.limit = limit;
  }

  // ok resets the streak on a successful poll.
  ok(): void {
    this.streak = 0;
  }

  // fail records a failed poll; true when the streak reaches the limit
  // (the caller hides the panel).
  fail(): boolean {
    this.streak++;
    return this.streak >= this.limit;
  }
}

// probeWithRetry gives the control plane a startup grace window: a
// one-shot probe that catches demosrv or the replay child mid-hiccup
// would hide the panel for the whole session. probe runs up to `attempts`
// times, `retryMs` apart, and null is returned only after the LAST failure
// — a plain live demo 404s every attempt and still hides, just a few
// seconds later.
export async function probeWithRetry(
  probe: () => Promise<ReplayStatus | null>,
  attempts: number,
  retryMs: number,
): Promise<ReplayStatus | null> {
  for (let i = 1; ; i++) {
    const s = await probe();
    if (s !== null || i >= attempts) return s;
    await new Promise((resolve) => setTimeout(resolve, retryMs));
  }
}

// ReplayControl is the DOM-free state machine over the control plane. The
// panel renders it; tests drive it with a stubbed FetchLike. expectedRun
// is the page's own run (config.ts ?run=): EVERY request binds to it, so
// the panel can only ever control the replay the page displays.
export class ReplayControl {
  // NOTE: erasable-syntax only (node strip-only mode loads this directly —
  // no parameter properties).
  status: ReplayStatus | null = null;
  hint = ""; // transient operator message ("seek back first", ctl failures)
  seekBusy = false; // a /seek round-trip is in flight — the slider disables
  private fetchFn: FetchLike;
  private expectedRun: string;

  constructor(fetchFn: FetchLike, expectedRun: string) {
    this.fetchFn = fetchFn;
    this.expectedRun = expectedRun;
  }

  // refresh GETs the status once, bound to the expected run; null on ANY
  // failure (no replay active, backend gone, malformed body). A 409 means
  // the ACTIVE replay is not the one this page displays (stale deep link,
  // or another replay took over): surface the mismatch hint, do not adopt
  // anything — the caller's FailGate counts the failure toward hiding.
  async refresh(): Promise<ReplayStatus | null> {
    const url = `/api/replay/status?run=${encodeURIComponent(this.expectedRun)}`;
    try {
      const resp = await this.fetchFn(url, { signal: AbortSignal.timeout(3000) });
      if (resp.status === 409) {
        this.hint = REPLAY_MISMATCH_HINT;
        return null;
      }
      if (!resp.ok) return null;
      this.status = parseStatus(await resp.json());
      return this.status;
    } catch {
      return null;
    }
  }

  // toggle posts pause/resume per the current state.
  async toggle(): Promise<void> {
    if (this.status === null) return;
    await this.ctl(toggleTarget(this.status));
  }

  async setSpeed(speed: number): Promise<void> {
    await this.ctl("speed", { speed });
  }

  // seek lands the replay at tick (synchronous server-side) and returns
  // whether it LANDED — the caller (main.ts via the panel's onSeeked hook)
  // resets the stream pipeline only on success. Reentry while a round-trip
  // is in flight is ignored — the slider stays disabled.
  async seek(tick: number): Promise<boolean> {
    if (this.seekBusy) return false;
    this.seekBusy = true;
    try {
      return await this.ctl("seek", { tick });
    } finally {
      this.seekBusy = false;
    }
  }

  // ctl posts one control verb bound to the expected run (?run= — demosrv
  // 409s a mismatch: a stale tab vs the active replay). True when the
  // response carried a fresh status (success), false on any failure (hint
  // set instead). A 409 discriminates by context: resume while done is
  // the player's "seek back first"; everything else is the run mismatch.
  private async ctl(path: string, body?: unknown): Promise<boolean> {
    const run = encodeURIComponent(this.expectedRun);
    try {
      const resp = await this.fetchFn(
        `/api/replay/ctl/${path}?run=${run}`,
        body === undefined ? { method: "POST" } : { method: "POST", body: JSON.stringify(body) },
      );
      if (resp.status === 409) {
        this.hint = path === "resume" && this.status?.done === true ? SEEK_BACK_HINT : REPLAY_MISMATCH_HINT;
        return false;
      }
      if (!resp.ok) {
        this.hint = `${path} failed (${resp.status})`;
        return false;
      }
      const s = parseStatus(await resp.json());
      if (s !== null) {
        this.status = s;
        this.hint = "";
        return true;
      }
      return false;
    } catch {
      this.hint = "control plane unreachable";
      return false;
    }
  }
}

export interface ReplayPanelOptions {
  fetchFn?: FetchLike;
  pollMs?: number;
  // expectedRun is the page's own run (config.ts ?run=): every probe and
  // ctl call binds to it, so a stale deep link can never control — or
  // adopt — a different replay than the one the map displays.
  expectedRun: string;
  // onSeeking fires BEFORE a panel-driven seek POST is sent. The reset
  // must precede the request: the player publishes the landing frame
  // before acking, so a post-ack reset could wipe the just-arrived
  // landing frame (a paused seek would show stale pre-seek vehicles until
  // the ~1 Hz republication). It also covers the scrubs SeekGate's
  // heuristic misses (forward jumps ≤ maxJump) — the panel KNOWS it seeks.
  onSeeking?: () => void;
  // onStatus fires on every successful status refresh. The status dt is
  // the RECORDED run's authoritative timestep — the ?dt= URL hint comes
  // from the (mutable) scenario and running-replay deep links carry none.
  onStatus?: (s: ReplayStatus) => void;
}

export class ReplayPanel {
  // NOTE: erasable-syntax only (node strip-only mode loads this directly).
  private root: HTMLElement;
  private ctl: ReplayControl;
  private pollMs: number;
  private onSeeking: (() => void) | null;
  private onStatus: ((s: ReplayStatus) => void) | null;
  private pollGate = new FailGate(3); // consecutive failures before hiding
  private timer: ReturnType<typeof setInterval> | null = null;
  private dragging = false; // slider thumb held — polls must not move it
  private runEl: HTMLElement | null = null;
  private toggleBtn: HTMLElement | null = null;
  private speedEls = new Map<number, HTMLElement>();
  private slider: HTMLInputElement | null = null;
  private lineEl: HTMLElement | null = null;
  private warnEl: HTMLElement | null = null;
  private hintEl: HTMLElement | null = null;

  constructor(root: HTMLElement, opts: ReplayPanelOptions) {
    this.root = root;
    this.ctl = new ReplayControl(
      opts.fetchFn ?? ((input, init) => fetch(input, init)),
      opts.expectedRun,
    );
    this.pollMs = opts.pollMs ?? 1000;
    this.onSeeking = opts.onSeeking ?? null;
    this.onStatus = opts.onStatus ?? null;
  }

  // init probes the replay status; on persistent failure the panel hides —
  // a normal live demo must be unaffected (the switcher idiom). The probe
  // retries (5×, 1 s apart) so a startup hiccup doesn't hide the panel for
  // the session. Runs detached — the map must not wait on it.
  async init(): Promise<void> {
    const s = await probeWithRetry(() => this.ctl.refresh(), 5, 1000);
    if (s === null) {
      this.root.style.display = "none";
      return;
    }
    this.onStatus?.(s); // first successful probe — the authoritative dt
    this.build();
    this.update();
    this.timer = setInterval(() => void this.poll(), this.pollMs);
  }

  private async poll(): Promise<void> {
    const s = await this.ctl.refresh();
    if (s === null) {
      // One transient failure keeps the panel; only a streak means the
      // replay child is really gone — its chrome goes with it.
      if (this.pollGate.fail()) this.hide();
      return;
    }
    this.pollGate.ok();
    this.onStatus?.(s);
    this.update();
  }

  private hide(): void {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
    this.root.style.display = "none";
  }

  private build(): void {
    this.root.textContent = "";
    const head = document.createElement("div");
    head.className = "rp-head";
    const title = document.createElement("span");
    title.textContent = "⏯ replay";
    head.appendChild(title);
    this.runEl = document.createElement("span");
    this.runEl.className = "rp-run";
    head.appendChild(this.runEl);
    this.root.appendChild(head);

    const controls = document.createElement("div");
    controls.className = "rp-controls";
    this.toggleBtn = document.createElement("div");
    this.toggleBtn.className = "rp-btn";
    this.toggleBtn.onclick = () => void this.onToggle();
    controls.appendChild(this.toggleBtn);
    for (const sp of SPEEDS) {
      const b = document.createElement("div");
      b.className = "rp-btn rp-speed";
      b.textContent = `${sp}×`;
      b.onclick = () => void this.onSpeed(sp);
      this.speedEls.set(sp, b);
      controls.appendChild(b);
    }
    this.root.appendChild(controls);

    this.slider = document.createElement("input");
    this.slider.type = "range";
    this.slider.min = "0";
    this.slider.step = "1";
    this.slider.addEventListener("pointerdown", () => {
      this.dragging = true;
    });
    this.slider.addEventListener("pointerup", () => {
      this.dragging = false;
    });
    this.slider.addEventListener("pointercancel", () => {
      this.dragging = false; // a cancelled touch-drag must not stick the thumb
    });
    // "change" fires on release (and per arrow-key step) — the seek POSTs
    // there, never mid-drag ("input").
    this.slider.addEventListener("change", () => void this.onSeek());
    this.root.appendChild(this.slider);

    this.lineEl = document.createElement("div");
    this.lineEl.className = "rp-line";
    this.root.appendChild(this.lineEl);
    this.warnEl = document.createElement("div");
    this.warnEl.className = "rp-line rp-warn";
    this.root.appendChild(this.warnEl);
    this.hintEl = document.createElement("div");
    this.hintEl.className = "rp-line rp-hint";
    this.root.appendChild(this.hintEl);
  }

  private update(): void {
    const s = this.ctl.status;
    if (s === null) return;
    const v = viewModel(s);
    if (this.runEl) this.runEl.textContent = v.ended ? `${s.run} — replay ended` : s.run;
    if (this.toggleBtn) this.toggleBtn.textContent = v.toggleLabel;
    for (const [sp, el] of this.speedEls) el.classList.toggle("rp-on", sp === s.speed);
    if (this.slider) {
      this.slider.max = String(s.endTick);
      this.slider.disabled = this.ctl.seekBusy;
      // The slider follows the sim while playing — except mid-drag or
      // mid-seek, when the user's thumb owns the position.
      if (!this.dragging && !this.ctl.seekBusy) this.slider.value = String(s.tick);
    }
    if (this.lineEl) this.lineEl.textContent = v.statusLine;
    if (this.warnEl) {
      this.warnEl.style.display = v.divergence > 0 ? "" : "none";
      this.warnEl.textContent = `⚠ divergence ${v.divergence}`;
    }
    if (this.hintEl) {
      this.hintEl.style.display = this.ctl.hint === "" ? "none" : "";
      this.hintEl.textContent = this.ctl.hint;
    }
  }

  private async onToggle(): Promise<void> {
    await this.ctl.toggle();
    this.update();
  }

  private async onSpeed(speed: number): Promise<void> {
    await this.ctl.setSpeed(speed);
    this.update();
  }

  private async onSeek(): Promise<void> {
    this.dragging = false;
    if (this.slider === null) return;
    const tick = Number(this.slider.value);
    this.slider.disabled = true; // no slider moves during the round-trip
    // Reset BEFORE the POST: the player publishes the landing frame before
    // acking, so a post-ack reset could wipe the just-arrived landing
    // frame (a paused seek would then show stale pre-seek vehicles until
    // the ~1 Hz republication). Resetting early is always safe — the
    // SeekGate stays as backstop for non-panel seeks.
    this.onSeeking?.();
    await this.ctl.seek(tick);
    this.update();
  }
}
