// splash.ts — controller for the landing page (splash.html): wires the ONE
// featured card to the LA demo exactly like the demos.html card for it —
// already-running deep-links straight into the map via deepLinkURL,
// otherwise a POST to startPath spawns the run and navigates on the {url}
// response (demos-core.ts; demosrv builds the same shapes). Everything else
// on the page is static.

import { deepLinkURL, startPath, type DemoInfo, type RunStatus } from "./demos-core.ts";

const LA: DemoInfo = {
  id: "la",
  title: "Los Angeles — full basin",
  blurb: "",
  tags: [],
  scenarioDir: "",
  run: "la",
  kind: "demo",
};

function must(id: string): HTMLElement {
  const el = document.getElementById(id);
  if (!(el instanceof HTMLElement)) throw new Error(`splash: #${id} missing from splash.html`);
  return el;
}
const featured = must("featured");
const badge = must("featured-badge");
const cta = must("featured-cta");
const notice = must("notice");

let status: RunStatus = { active: null, pid: 0 };
let engineWs: string | undefined; // /api/demos "ws" — engine port this demosrv spawns to
let starting = false;

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(url, init);
  if (!resp.ok) {
    const body = await resp.text().catch(() => "");
    throw new Error(`${init?.method ?? "GET"} ${url} → ${resp.status} ${body}`.trim());
  }
  return (await resp.json()) as T;
}

function showError(err: unknown): void {
  const msg = err instanceof Error ? err.message : String(err);
  notice.textContent =
    msg +
    "\nThe featured demo may not be the live run right now — see what is running on the demos page (/demos.html).";
  notice.style.display = "block";
}

function render(): void {
  const running = status.active === LA.id;
  featured.classList.toggle("running", running);
  badge.hidden = !running;
  cta.textContent = starting ? "starting…" : running ? "watch it live →" : "launch live demo →";
}

// activate mirrors the demos.html card click for LA (demos.ts): the running
// card deep-links without a start round-trip; otherwise the start POST
// spawns serve and the response carries the map URL.
async function activate(): Promise<void> {
  if (starting) return;
  if (status.active === LA.id) {
    location.href = deepLinkURL(LA, engineWs, status.run);
    return;
  }
  starting = true;
  render();
  try {
    const resp = await fetchJSON<{ url: string }>(startPath(LA), { method: "POST" });
    location.href = resp.url;
  } catch (err) {
    starting = false;
    showError(err);
    render();
  }
}

async function main(): Promise<void> {
  featured.addEventListener("click", (ev) => {
    ev.preventDefault(); // the href="#" is a button affordance, not a link
    void activate();
  });
  try {
    const reg = await fetchJSON<{ ws?: string }>("/api/demos");
    engineWs = reg.ws;
    status = await fetchJSON<RunStatus>("/api/status");
  } catch {
    // No demosrv behind the page (static preview): the card stays clickable
    // and activation surfaces the real error in the notice box.
  }
  render();
}

main().catch(showError);
