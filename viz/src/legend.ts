// legend.ts — top-right key panel: what the encodings on the map mean.
// Pure DOM (ADR-0003: no framework, no MapLibre APIs), styled with the same
// HUD tokens, and every swatch color comes from theme.ts so the key can't
// drift from the map. Contents, all real data channels:
//
//   - vehicle class glyphs (rects at their true length ratio),
//   - signal light states (green/amber/red),
//   - stop signs for stop-controlled approaches (stopsign.ts),
//   - the client-derived congestion ramp (per-lane mean speed / limit),
//   - a sim-clock readout fed from the render loop's sample tick.
//
// Every row except the sim clock is a TOGGLE: clicking flips the matching
// map layers on/off (off = .legend-off, dimmed + greyed). The legend only
// reports the flip via the onToggle callback — the layer mapping lives in
// layertoggles.ts and the MapLibre calls in main.ts.

import { THEMES, GLYPHS, type ThemeSpec } from "./theme.ts";
import type { ToggleKey } from "./layertoggles.ts";

// Engine timestep default (ADR-0005: dt is configurable; 0.1 s is the
// Kesting & Treiber validated step). The Legend takes the run's actual dt
// — scenarios/replays with another step would otherwise lie on the clock.
export const SIM_DT_S = 0.1;

export function simClockHMS(tick: number, dt: number = SIM_DT_S): string {
  const s = Math.floor(tick * dt);
  const p = (n: number): string => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
}

export class Legend {
  private clockEl: HTMLElement;

  constructor(
    elId: string,
    theme: ThemeSpec = THEMES.navy,
    onToggle?: (key: ToggleKey, on: boolean) => void,
    private dt: number = SIM_DT_S,
  ) {
    const el = document.getElementById(elId);
    if (!el) throw new Error("legend: missing DOM element");

    // Vehicle swatches at the true class length ratio (engine dims via
    // theme.ts), 3.6 px per metre; swatch colors follow the active theme.
    // data-toggle carries the glyph NAME ("car"/"truck") — the ToggleKey
    // for that class.
    const PX_PER_M = 3.6;
    const vehRows = GLYPHS.map(
      (g) =>
        `<div class="legend-row legend-toggle" data-toggle="${g.name}">` +
        `<span class="legend-veh" style="width:${(g.lengthM * PX_PER_M).toFixed(0)}px;` +
        `height:${(g.widthM * PX_PER_M).toFixed(0)}px;background:${theme.glyphColors[g.cls] ?? g.color}"></span>` +
        `<span>${g.name}</span></div>`,
    ).join("");

    // Signal head swatch: mini housing with the three lenses in their
    // real order (red top, amber, green) — the map lights one lens per
    // head (signals.ts + signalhead.ts).
    const sigHead =
      `<span class="legend-sig">` +
      `<i style="background:${theme.signalRed}"></i>` +
      `<i style="background:${theme.signalAmber}"></i>` +
      `<i style="background:${theme.signalGreen}"></i>` +
      `</span>`;

    el.innerHTML =
      `<div class="legend-title">legend</div>` +
      vehRows +
      `<div class="legend-sep"></div>` +
      `<div class="legend-row legend-toggle" data-toggle="signals">${sigHead}<span>signal head</span></div>` +
      `<div class="legend-caption">one per movement; live lens lit</div>` +
      `<div class="legend-sep"></div>` +
      `<div class="legend-row legend-toggle" data-toggle="stops">` +
      `<span class="legend-stop" style="background:${theme.stopFace}"></span>` +
      `<span>stop sign</span></div>` +
      `<div class="legend-caption">one per stop-controlled approach</div>` +
      `<div class="legend-sep"></div>` +
      `<div class="legend-toggle" data-toggle="congestion">` +
      `<div class="legend-ramp" style="background:linear-gradient(90deg,` +
      `${theme.stopped} 0%, ${theme.mid} 35%, ${theme.freeFlow} 70%)"></div>` +
      `<div class="legend-ramp-labels"><span>stopped</span><span>free flow</span></div>` +
      `<div class="legend-caption">lane mean speed / limit</div></div>` +
      `<div class="legend-sep"></div>` +
      `<div class="legend-row"><span>sim time</span><span class="legend-clock">00:00:00</span></div>`;

    const clock = el.querySelector(".legend-clock");
    if (!(clock instanceof HTMLElement)) throw new Error("legend: clock element missing");
    this.clockEl = clock;

    // Toggle wiring: a click flips the row's off-state and reports the NEW
    // state. Rows start on (no .legend-off); classList.toggle returns true
    // when the class is now PRESENT (= off).
    for (const row of el.querySelectorAll<HTMLElement>(".legend-toggle")) {
      row.addEventListener("click", () => {
        const key = row.dataset["toggle"] as ToggleKey | undefined;
        if (key === undefined) return;
        onToggle?.(key, !row.classList.toggle("legend-off"));
      });
    }
  }

  // setDt swaps the timestep when the run's authoritative dt arrives
  // (replay status can correct the URL default).
  setDt(dt: number): void {
    if (dt > 0) this.dt = dt;
  }

  // setTick is called from the render loop with the interpolated sample's
  // sim tick (same tick the HUD and signal derivation use).
  setTick(tick: number): void {
    this.clockEl.textContent = simClockHMS(tick, this.dt);
  }
}
