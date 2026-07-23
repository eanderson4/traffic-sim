// legend.ts — top-right key panel: what the encodings on the map mean.
// Pure DOM (ADR-0003: no framework, no MapLibre APIs), styled with the same
// HUD tokens, and every swatch color comes from theme.ts so the key can't
// drift from the map. Contents, all real data channels:
//
//   - vehicle class glyphs (rects at their true length ratio),
//   - signal light states (green/amber/red),
//   - the client-derived congestion ramp (per-lane mean speed / limit),
//   - a sim-clock readout fed from the render loop's sample tick.

import { THEME, GLYPHS } from "./theme.ts";

// Engine timestep (ADR-0005: dt = 0.1 s, Kesting & Treiber validated);
// sim seconds = tick × dt.
export const SIM_DT_S = 0.1;

export function simClockHMS(tick: number): string {
  const s = Math.floor(tick * SIM_DT_S);
  const p = (n: number): string => String(n).padStart(2, "0");
  return `${p(Math.floor(s / 3600))}:${p(Math.floor((s % 3600) / 60))}:${p(s % 60)}`;
}

export class Legend {
  private clockEl: HTMLElement;

  constructor(elId: string) {
    const el = document.getElementById(elId);
    if (!el) throw new Error("legend: missing DOM element");

    // Vehicle swatches at the true class length ratio (engine dims via
    // theme.ts), 3.6 px per metre.
    const PX_PER_M = 3.6;
    const vehRows = GLYPHS.map(
      (g) =>
        `<div class="legend-row">` +
        `<span class="legend-veh" style="width:${(g.lengthM * PX_PER_M).toFixed(0)}px;` +
        `height:${(g.widthM * PX_PER_M).toFixed(0)}px;background:${g.color}"></span>` +
        `<span>${g.name}</span></div>`,
    ).join("");

    // Signal head swatch: mini housing with the three lenses in their
    // real order (red top, amber, green) — the map lights one lens per
    // head (signals.ts + signalhead.ts).
    const sigHead =
      `<span class="legend-sig">` +
      `<i style="background:${THEME.signalRed}"></i>` +
      `<i style="background:${THEME.signalAmber}"></i>` +
      `<i style="background:${THEME.signalGreen}"></i>` +
      `</span>`;

    el.innerHTML =
      `<div class="legend-title">legend</div>` +
      vehRows +
      `<div class="legend-sep"></div>` +
      `<div class="legend-row">${sigHead}<span>signal head</span></div>` +
      `<div class="legend-caption">one per movement; live lens lit</div>` +
      `<div class="legend-sep"></div>` +
      `<div class="legend-ramp" style="background:linear-gradient(90deg,` +
      `${THEME.stopped} 0%, ${THEME.mid} 35%, ${THEME.freeFlow} 70%)"></div>` +
      `<div class="legend-ramp-labels"><span>stopped</span><span>free flow</span></div>` +
      `<div class="legend-caption">lane mean speed / limit</div>` +
      `<div class="legend-sep"></div>` +
      `<div class="legend-row"><span>sim time</span><span class="legend-clock">00:00:00</span></div>`;

    const clock = el.querySelector(".legend-clock");
    if (!(clock instanceof HTMLElement)) throw new Error("legend: clock element missing");
    this.clockEl = clock;
  }

  // setTick is called from the render loop with the interpolated sample's
  // sim tick (same tick the HUD and signal derivation use).
  setTick(tick: number): void {
    this.clockEl.textContent = simClockHMS(tick);
  }
}
