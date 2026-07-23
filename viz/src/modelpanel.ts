// modelpanel.ts — the "model & sim" panel: WHICH driver controllers the
// engine is running and with WHAT parameters (IDM car-following per
// vehicle class, MOBIL lane-changing, driver heterogeneity), plus the sim
// parameters (dt, seed, run length, claim capacity, demand). The values
// come from demosrv's /api/demo/{id}/params, which resolves them through
// the same Go path serve uses (engine/cmd/demosrv/params.go) — the panel
// can never drift from the running engine. Read-only by design: the
// engine is authoritative over world state and there is no mid-run
// parameter contract; changing a knob means starting a new run. Like the
// switcher, the panel hides itself when no demosrv answers (standalone
// `pnpm dev` viewing).

export interface TypeParams {
  name: string;
  lengthM: number;
  widthM: number;
  s0M: number; // jam gap, bumper-to-bumper
  tS: number; // desired time headway
  aMps2: number; // max acceleration
  bMps2: number; // comfortable deceleration
  v0Mps: number; // desired speed
}

export interface FlowParams {
  origin: string;
  vehPerH?: number;
  slices?: number;
  spacing: string;
  vtypes?: Record<string, number>;
}

export interface ModelParams {
  id: string;
  scenario: { id: string; hash: string; network: string };
  sim: {
    dtS: number;
    seed: string; // uint64 as string — JSON floats lose ≥ 2^53 (replay identity)
    ticks: number;
    capacity: number;
    spawner: { ratePerLaneHour: number; densityPerKm?: number } | null;
  };
  demand: FlowParams[];
  controllers: {
    carFollowing: { model: string; types: TypeParams[] };
    laneChange: {
      model: string;
      politeness: number;
      thresholdMps2: number;
      bSafeMps2: number;
      minGapLCM: number;
      minGapMergeM: number;
      mergeZoneM: number;
      mergeUrgencyGainMps2: number;
      lcCooldownTicks: number;
      spawnCooldownTicks: number;
    };
    heterogeneity: { speedFactorSigma: number; spawnJitter: number };
  };
}

export interface ParamSection {
  title: string;
  rows: Array<[string, string]>; // [label, value]
}

// num trims float noise for display (0.30000000000000004 → 0.3) without
// padding integers.
const num = (x: number): string => String(Math.round(x * 1000) / 1000);

function hms(sec: number): string {
  const s = Math.round(sec);
  const p = (n: number): string => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
}

// mix renders a vtypes weight map as "90% car, 10% truck" (heaviest first).
function mix(vtypes: Record<string, number> | undefined): string {
  if (!vtypes) return "";
  const parts = Object.entries(vtypes)
    .sort((a, b) => b[1] - a[1])
    .map(([name, w]) => `${Math.round(w * 100)}% ${name}`);
  return parts.length > 0 ? ` (${parts.join(", ")})` : "";
}

// paramSections shapes the resolved params into display rows. Pure — the
// DOM class below only renders what this returns. Demand is capped at
// four rows (dense grids declare dozens of flows) with a "+N more" tail.
export function paramSections(p: ModelParams): ParamSection[] {
  const sections: ParamSection[] = [];

  const simRows: Array<[string, string]> = [
    ["run length", `${p.sim.ticks} ticks × ${num(p.sim.dtS)} s = ${hms(p.sim.ticks * p.sim.dtS)}`],
    ["seed", String(p.sim.seed)],
    ["driver capacity", String(p.sim.capacity)],
    [
      "spawner",
      p.sim.spawner === null
        ? "off (director demand)"
        : `${num(p.sim.spawner.ratePerLaneHour)} veh/h/lane` +
          (p.sim.spawner.densityPerKm ? ` → ${num(p.sim.spawner.densityPerKm)}/km` : ""),
    ],
    ["scenario", `${p.scenario.id} · ${p.scenario.hash.slice(0, 12)}`],
  ];
  sections.push({ title: "sim", rows: simRows });

  if (p.demand.length > 0) {
    const rows: Array<[string, string]> = [];
    for (const f of p.demand.slice(0, 4)) {
      const rate =
        f.slices !== undefined && f.slices > 0
          ? `${f.slices} slice(s), ${f.spacing}`
          : `${num(f.vehPerH ?? 0)} veh/h ${f.spacing}`;
      rows.push([f.origin, rate + mix(f.vtypes)]);
    }
    if (p.demand.length > 4) rows.push(["…", `+${p.demand.length - 4} more flow(s)`]);
    sections.push({ title: "demand", rows });
  }

  sections.push({
    title: `car-following — ${p.controllers.carFollowing.model}`,
    rows: p.controllers.carFollowing.types.map((t) => [
      t.name,
      `v0 ${num(t.v0Mps)} m/s · T ${num(t.tS)} s · a ${num(t.aMps2)} · b ${num(t.bMps2)} m/s² · s0 ${num(t.s0M)} m · ${num(t.lengthM)}×${num(t.widthM)} m`,
    ]),
  });

  const lc = p.controllers.laneChange;
  sections.push({
    title: `lane-change — ${lc.model}`,
    rows: [
      ["incentive", `politeness ${num(lc.politeness)} · Δa_th ${num(lc.thresholdMps2)} · b_safe ${num(lc.bSafeMps2)} m/s²`],
      ["min gap", `${num(lc.minGapLCM)} m discretionary · ${num(lc.minGapMergeM)} m merge`],
      ["merge", `zone ${num(lc.mergeZoneM)} m · urgency gain ${num(lc.mergeUrgencyGainMps2)} m/s²`],
      ["cooldown", `lane change ${lc.lcCooldownTicks} ticks · spawn ${lc.spawnCooldownTicks} ticks`],
    ],
  });

  const h = p.controllers.heterogeneity;
  sections.push({
    title: "driver heterogeneity",
    rows: [
      ["desired speed", `gaussian factor σ ${num(h.speedFactorSigma)}`],
      ["spawn interval", `±${Math.round(h.spawnJitter * 100)}% jitter`],
    ],
  });

  return sections;
}

export class ModelPanel {
  // NOTE: erasable-syntax only (node strip-only mode loads this directly —
  // no parameter properties).
  private root: HTMLElement;
  private demoId: string | null;
  private params: ModelParams | null = null;

  constructor(root: HTMLElement, demoId: string | null) {
    this.root = root;
    this.demoId = demoId;
  }

  // init fetches the resolved params; on any failure (no demosrv, demo
  // unknown) the panel hides — the map must not wait on it.
  async init(): Promise<void> {
    if (this.demoId === null) {
      this.root.style.display = "none";
      return;
    }
    try {
      const resp = await fetch(`/api/demo/${this.demoId}/params`, { signal: AbortSignal.timeout(3000) });
      if (!resp.ok) throw new Error(String(resp.status));
      this.params = (await resp.json()) as ModelParams;
    } catch {
      this.root.style.display = "none";
      return;
    }
    this.renderCollapsed();
  }

  private renderCollapsed(): void {
    this.root.textContent = "";
    const btn = document.createElement("div");
    btn.className = "mp-toggle";
    btn.textContent = "⚙ model";
    btn.onclick = () => this.renderExpanded();
    this.root.appendChild(btn);
  }

  private renderExpanded(): void {
    if (this.params === null) return;
    this.root.textContent = "";
    const head = document.createElement("div");
    head.className = "mp-toggle";
    head.textContent = "⚙ model ▾";
    head.onclick = () => this.renderCollapsed();
    this.root.appendChild(head);
    for (const sec of paramSections(this.params)) {
      const title = document.createElement("div");
      title.className = "mp-sec";
      title.textContent = sec.title;
      this.root.appendChild(title);
      for (const [label, value] of sec.rows) {
        const row = document.createElement("div");
        row.className = "mp-row";
        const l = document.createElement("span");
        l.textContent = label;
        const v = document.createElement("span");
        v.textContent = value;
        row.appendChild(l);
        row.appendChild(v);
        this.root.appendChild(row);
      }
    }
  }
}
