// demos.ts — controller for the demosrv menu page (demos.html): renders
// the registry as a card grid, polls /api/status every 3 s for the running
// badge, and turns a card click into POST /api/demo/{id}/start → the live
// map. Single-active-run is demosrv's rule (starting a demo kills the
// previous engine); this page only reflects it. Pure helpers live in
// demos-core.ts so node --test can reach them.

import { buildAppURL, type DemoInfo, type RunStatus } from "./demos-core.ts";

function must(id: string): HTMLElement {
  const el = document.getElementById(id);
  if (!(el instanceof HTMLElement)) throw new Error(`demos: #${id} missing from demos.html`);
  return el;
}
const grid = must("grid");
const notice = must("notice");

let demos: DemoInfo[] = [];
let status: RunStatus = { active: null, pid: 0 };
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

function card(d: DemoInfo): HTMLElement {
  const el = document.createElement("article");
  el.className = "card";
  const running = status.active === d.id;
  const isStarting = starting.has(d.id);
  if (running) el.classList.add("running");
  if (isStarting) el.classList.add("starting");

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
  for (const t of d.tags) {
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
  grid.replaceChildren(...demos.map(card));
}

// activate starts the demo's engine (demosrv kills any previous run) and
// navigates to the live map. The already-running card skips the start
// round-trip via the pure buildAppURL — same URL shape as the start
// response, pinned in test/demos.test.ts.
async function activate(d: DemoInfo): Promise<void> {
  if (starting.has(d.id)) return;
  if (status.active === d.id) {
    location.href = buildAppURL(d);
    return;
  }
  starting.add(d.id);
  render();
  try {
    const resp = await fetchJSON<{ url: string }>(`/api/demo/${d.id}/start`, { method: "POST" });
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
  const reg = await fetchJSON<{ demos: DemoInfo[] }>("/api/demos");
  demos = reg.demos;
  await refreshStatus(); // refreshStatus renders; covers the first paint too
  setInterval(() => void refreshStatus(), 3000);
}

main().catch(showError);
