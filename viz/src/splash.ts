// splash.ts — controller for the landing page (splash.html). The featured
// card is BAKED-FIRST (ADR-0023): when BAKED_FEATURED_URL names a published
// baked replay, the CTA plays it entirely in the browser — no live run, no
// admin gate, unlimited concurrent viewers. With it empty (no bake published
// yet) the card falls back to the live-demosrv behavior: running deep-links
// into the map via deepLinkURL, otherwise a POST to startPath spawns the run
// (demos-core.ts); a 401/403 (public deploy, ADR-0020) or a registry without
// the demo flips the card to watch-only and routes to /demos.html.
// Everything else on the page is static.

import { deepLinkURL, startPath, type DemoInfo, type RunStatus } from "./demos-core.ts";

// Published baked replay URL (phantomjam.com viz + data.phantomjam.com
// artifact: https://phantomjam.com/?bake=<absolute index.json URL> — the
// ONLY camera control is the manifest's own frame fit; config.ts parses
// no center/zoom params). Fill in when the featured bake is uploaded to
// R2; empty = live-demosrv fallback.
const BAKED_FEATURED_URL: string = "";
const baked = BAKED_FEATURED_URL !== "";

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
  // Baked mode is fully static: no live-run badge, no registry notices.
  const running = !baked && status.active === demo.id;
  featured.classList.toggle("running", running);
  badge.hidden = !running;
  cta.textContent = baked
    ? "watch the replay →"
    : starting
      ? "starting…"
      : running
        ? "watch it live →"
        : unregistered || gated
          ? "browse the demos →"
          : "launch live demo →";
}

// activate (live mode only — baked mode never attaches the handler): mirrors
// the demos.html card click for LA (demos.ts): the running card deep-links
// without a start round-trip; the start POST spawns serve and the response
// carries the map URL; unregistered or gated cards route to the demos menu.
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
  if (baked) {
    // A real href, not a JS navigation: middle-click / open-in-new-tab /
    // copy-link all work, and a JS failure can't dead-end the card. No
    // live plane to consult — skip the registry/status fetches entirely.
    featured.setAttribute("href", BAKED_FEATURED_URL);
  } else {
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
  }
  render();
}

main().catch(showError);
