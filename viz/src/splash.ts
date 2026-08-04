// splash.ts — controller for the landing page (splash.html): wires the ONE
// featured card to the LA demo exactly like the demos.html card for it —
// already-running deep-links straight into the map via deepLinkURL,
// otherwise a POST to startPath spawns the run and navigates on the {url}
// response (demos-core.ts; demosrv builds the same shapes). On the public
// deployment the start POST is admin-gated (ADR-0020): a 401/403 flips the
// card into a watch-only posture — the CTA routes to the demos menu instead
// of dead-ending on an error. Everything else on the page is static.

import { deepLinkURL, startPath, type DemoInfo, type RunStatus } from "./demos-core.ts";

// Fallback entry for static previews (no demosrv behind the page); when the
// registry answers, the live entry for id "la" replaces this wholesale.
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

let demo: DemoInfo = LA; // registry entry once /api/demos answers, else the fallback
let status: RunStatus = { active: null, pid: 0 };
let engineWs: string | undefined; // /api/demos "ws" — engine port this demosrv spawns to
let starting = false;
let gated = false; // start POST answered 401/403 — public deploy shape (ADR-0020)
let unregistered = false; // demosrv answered but has no "la" entry

class HttpError extends Error {
  constructor(readonly status: number, message: string) {
    super(message);
  }
}

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(url, init);
  if (!resp.ok) {
    const body = await resp.text().catch(() => "");
    throw new HttpError(resp.status, `${init?.method ?? "GET"} ${url} → ${resp.status} ${body}`.trim());
  }
  return (await resp.json()) as T;
}

function showError(err: unknown): void {
  const msg = err instanceof Error ? err.message : String(err);
  notice.classList.remove("info");
  notice.textContent =
    msg +
    "\nThe featured demo may not be the live run right now — see what is running on the demos page (/demos.html).";
  notice.style.display = "block";
}

function render(): void {
  const running = status.active === demo.id;
  featured.classList.toggle("running", running);
  badge.hidden = !running;
  cta.textContent = starting
    ? "starting…"
    : running
      ? "watch it live →"
      : unregistered || gated
        ? "browse the demos →"
        : "launch live demo →";
}

// activate mirrors the demos.html card click for LA (demos.ts): the running
// card deep-links without a start round-trip; otherwise the start POST
// spawns serve and the response carries the map URL. Two soft landings:
// unregistered or gated cards route to the demos menu instead of POSTing.
async function activate(): Promise<void> {
  if (starting) return;
  if (status.active === demo.id) {
    location.href = deepLinkURL(demo, engineWs, status.run);
    return;
  }
  if (unregistered || gated) {
    location.href = "/demos.html";
    return;
  }
  starting = true;
  render();
  try {
    const resp = await fetchJSON<{ url: string }>(startPath(demo), { method: "POST" });
    location.href = resp.url;
  } catch (err) {
    starting = false;
    if (err instanceof HttpError && (err.status === 401 || err.status === 403)) {
      // Public deploy (ADR-0020): only the operator may start runs. Not an
      // error worth red — flip to watch-only and point at what's live.
      gated = true;
      notice.classList.add("info");
      notice.textContent = "Launching runs is operator-gated on this deployment — see what's live on the demos page.";
      notice.style.display = "block";
    } else {
      showError(err);
    }
    render();
  }
}

async function main(): Promise<void> {
  featured.addEventListener("click", (ev) => {
    ev.preventDefault(); // the href="#" is a button affordance, not a link
    void activate();
  });
  try {
    const reg = await fetchJSON<{ ws?: string; demos?: DemoInfo[] }>("/api/demos");
    engineWs = reg.ws;
    const entry = (reg.demos ?? []).find((d) => d.id === LA.id);
    if (entry) {
      demo = entry;
    } else {
      unregistered = true;
      notice.classList.add("info");
      notice.textContent = "The featured demo isn't in this deployment's registry — browse the demos page instead.";
      notice.style.display = "block";
    }
    status = await fetchJSON<RunStatus>("/api/status");
  } catch {
    // No demosrv behind the page (static preview): the card stays clickable
    // and activation surfaces the real error in the notice box.
  }
  render();
}

main().catch(showError);
