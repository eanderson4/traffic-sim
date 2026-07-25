// demos.ts — controller for the demosrv menu page (demos.html): renders
// the registry as a card grid (demos AND recordings — replay entries carry
// a REPLAY chip and a dashed border), polls /api/status every 3 s for the
// running badge, and turns a card click into a start POST (routed by kind:
// /api/demo/{id}/start vs /api/replay/{id}/start — demos-core.ts
// startPath) → the live map. Single-active-run is demosrv's rule (starting
// an entry kills the previous engine); this page only reflects it. Pure
// helpers live in demos-core.ts so node --test can reach them.

import { deepLinkURL, startPath, type DemoInfo, type MenuEntry, type RecordingInfo, type RunStatus } from "./demos-core.ts";

function must(id: string): HTMLElement {
  const el = document.getElementById(id);
  if (!(el instanceof HTMLElement)) throw new Error(`demos: #${id} missing from demos.html`);
  return el;
}
const grid = must("grid");
const notice = must("notice");

let demos: DemoInfo[] = [];
let recordings: RecordingInfo[] = [];
let status: RunStatus = { active: null, pid: 0 };
let engineWs: string | undefined; // /api/demos "ws" — engine port this demosrv spawns to
const starting = new Set<string>(); // start POST in flight — keep the spinner across re-renders

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(url, init);
  if (!resp.ok) {
    const body = await resp.text().catch(() => "");
    throw new Error(`${init?.method ?? "GET"} ${url} → ${resp.status} ${body}`.trim());
  }
  return (await resp.json()) as T;
}

function showError(err: unknown): void {
  notice.textContent = err instanceof Error ? err.message : String(err);
  notice.style.display = "block";
}

function card(d: MenuEntry): HTMLElement {
  const el = document.createElement("article");
  el.className = "card";
  const running = status.active === d.id;
  const isStarting = starting.has(d.id);
  if (running) el.classList.add("running");
  if (isStarting) el.classList.add("starting");
  if (d.kind === "replay") el.classList.add("replay");

  const title = document.createElement("h2");
  title.textContent = d.title;
  el.append(title);

  if (running || isStarting) {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = isStarting ? "starting…" : "running";
    el.append(badge);
  }

  const blurb = document.createElement("p");
  blurb.className = "blurb";
  blurb.textContent = d.blurb;
  el.append(blurb);

  const tags = document.createElement("div");
  tags.className = "tags";
  // Kind chip first: a recording must read as a replay, not a live run.
  if (d.kind === "replay") {
    const chip = document.createElement("span");
    chip.className = "chip replay";
    chip.textContent = "REPLAY";
    tags.append(chip);
  }
  // Recordings carry no tags (they replay their store as written).
  for (const t of "tags" in d ? d.tags : []) {
    const chip = document.createElement("span");
    chip.className = "chip";
    chip.textContent = t;
    tags.append(chip);
  }
  el.append(tags);

  if (running && !isStarting) {
    const stop = document.createElement("button");
    stop.className = "stop";
    stop.textContent = "■ stop";
    stop.addEventListener("click", (ev) => {
      ev.stopPropagation(); // a stop is not a card activation
      void stopRun();
    });
    el.append(stop);
  }

  el.addEventListener("click", () => void activate(d));
  return el;
}

function render(): void {
  grid.replaceChildren(...demos.map(card), ...recordings.map(card));
}

// activate starts the entry's engine (demosrv kills any previous run) and
// navigates to the live map — the start POST is routed by kind (demos
// spawn serve, recordings spawn the replay driver; same {url} response).
// The already-running card skips the start round-trip via the pure
// deepLinkURL — same URL shape as the start response, pinned in
// test/demos.test.ts.
async function activate(d: MenuEntry): Promise<void> {
  if (starting.has(d.id)) return;
  if (status.active === d.id) {
    location.href = deepLinkURL(d, engineWs, status.run);
    return;
  }
  starting.add(d.id);
  render();
  try {
    const resp = await fetchJSON<{ url: string }>(startPath(d), { method: "POST" });
    location.href = resp.url;
  } catch (err) {
    starting.delete(d.id);
    showError(err);
    render();
  }
}

async function stopRun(): Promise<void> {
  try {
    await fetchJSON("/api/demo/stop", { method: "POST" });
  } catch (err) {
    showError(err);
  }
  await refreshStatus();
}

async function refreshStatus(): Promise<void> {
  try {
    status = await fetchJSON<RunStatus>("/api/status");
    render();
  } catch {
    // demosrv gone is not a page-level emergency; the last known cards stay.
  }
}

async function main(): Promise<void> {
  const reg = await fetchJSON<{ demos?: DemoInfo[]; recordings?: RecordingInfo[]; ws?: string }>("/api/demos");
  demos = reg.demos ?? [];
  recordings = reg.recordings ?? [];
  engineWs = reg.ws; // engine port this demosrv spawns to (config.ts default when absent)
  await refreshStatus(); // refreshStatus renders; covers the first paint too
  setInterval(() => void refreshStatus(), 3000);
}

main().catch(showError);
