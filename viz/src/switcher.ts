// switcher.ts — in-map demo switcher. When the map is served BY demosrv
// (engine/cmd/demosrv, port 8900) the viewer can swap demos without
// returning to the menu: the panel lists the registry (demos AND
// recordings — replay rows are suffixed "· REPLAY"), starts the picked
// engine through the kind-routed start endpoint (demos-core.ts startPath —
// the same routes the menu page uses), and navigates to the fresh run.
// The demosrv API probe doubles as the feature detect — standalone
// `pnpm dev` viewing (no backend in front) hides the switcher entirely
// rather than offering dead buttons.

import { startPath, type MenuEntry } from "./demos-core.ts";

// demoIdFromNetUrl recovers the demo id from a ?net=/net/{id}.geojson URL
// (the shape demosrv's start response and buildAppURL both produce).
export function demoIdFromNetUrl(netUrl: string): string | null {
  const m = /^\/net\/([A-Za-z0-9_-]+)\.geojson$/.exec(netUrl);
  return m && m[1] !== undefined ? m[1] : null;
}

export class DemoSwitcher {
  private entries: MenuEntry[] = [];
  // NOTE: erasable-syntax only (node strip-only mode loads this directly —
  // no parameter properties).
  private root: HTMLElement;
  private currentId: string | null;

  constructor(root: HTMLElement, currentId: string | null) {
    this.root = root;
    this.currentId = currentId;
  }

  // init probes /api/demos; on any failure the switcher hides (no backend
  // to inform). Runs detached — the map must not wait on it.
  async init(): Promise<void> {
    try {
      const resp = await fetch("/api/demos", { signal: AbortSignal.timeout(3000) });
      if (!resp.ok) throw new Error(String(resp.status));
      const body = (await resp.json()) as { demos?: MenuEntry[]; recordings?: MenuEntry[] };
      this.entries = [...(body.demos ?? []), ...(body.recordings ?? [])];
    } catch {
      this.root.style.display = "none";
      return;
    }
    this.renderCollapsed();
  }

  private renderCollapsed(): void {
    this.root.textContent = "";
    const btn = document.createElement("div");
    btn.className = "sw-toggle";
    btn.textContent = "☰ demos";
    btn.onclick = () => this.renderExpanded();
    this.root.appendChild(btn);
  }

  private renderExpanded(): void {
    this.root.textContent = "";
    const head = document.createElement("div");
    head.className = "sw-toggle";
    head.textContent = "☰ demos ▾";
    head.onclick = () => this.renderCollapsed();
    this.root.appendChild(head);
    for (const d of this.entries) {
      const row = document.createElement("div");
      row.className = "sw-row" + (d.id === this.currentId ? " sw-current" : "");
      row.textContent =
        d.title + (d.kind === "replay" ? " · REPLAY" : "") + (d.id === this.currentId ? " ●" : "");
      row.onclick = () => void this.pick(d, row);
      this.root.appendChild(row);
    }
    const menu = document.createElement("div");
    menu.className = "sw-row sw-menu";
    menu.textContent = "all demos →";
    menu.onclick = () => {
      location.href = "/";
    };
    this.root.appendChild(menu);
  }

  // pick starts the entry's engine via demosrv (which kills the previous
  // run — single-active-run), routed by kind (startPath: /api/demo/{id}
  // vs /api/replay/{id}), and navigates once the start URL arrives; the
  // POST already blocks until the new engine's port accepts, so the fresh
  // page opens a live socket.
  private async pick(d: MenuEntry, row: HTMLElement): Promise<void> {
    if (d.id === this.currentId) return;
    for (const el of this.root.querySelectorAll<HTMLElement>(".sw-row")) el.onclick = null;
    row.textContent = d.title + " — starting…";
    try {
      const resp = await fetch(startPath(d), { method: "POST" });
      if (!resp.ok) throw new Error(String(resp.status));
      const body = (await resp.json()) as { url: string };
      location.href = body.url;
    } catch {
      row.textContent = d.title + " — start failed";
      setTimeout(() => this.renderExpanded(), 2000);
    }
  }
}
