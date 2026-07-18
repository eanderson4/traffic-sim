// status.ts — the minimal HUD: connection state, sim tick, vehicle count,
// plus the click-inspection overlay. Vanilla DOM (ADR-0003: no framework).

export class Hud {
  private statusEl: HTMLElement;
  private inspectEl: HTMLElement;
  private connected = false;
  private detail = "connecting…";
  private tick = 0;
  private count = 0;
  private starved = false;

  constructor(statusId: string, inspectId: string) {
    const status = document.getElementById(statusId);
    const inspect = document.getElementById(inspectId);
    if (!status || !inspect) throw new Error("hud: missing DOM elements");
    this.statusEl = status;
    this.inspectEl = inspect;
    this.render();
  }

  setConnection(connected: boolean, detail: string): void {
    this.connected = connected;
    this.detail = detail;
    this.render();
  }

  setFrame(tick: number, count: number, starved: boolean): void {
    this.tick = tick;
    this.count = count;
    this.starved = starved;
    this.render();
  }

  inspect(vehicle: { id: number; cls: number; speed: number } | null): void {
    if (vehicle === null) {
      this.inspectEl.style.display = "none";
      return;
    }
    const kind = vehicle.cls === 0 ? "car" : `class ${vehicle.cls}`;
    this.inspectEl.textContent =
      `vehicle ${vehicle.id}\n` +
      `class  ${kind}\n` +
      `speed  ${vehicle.speed.toFixed(1)} m/s (client-derived)`;
    this.inspectEl.style.display = "block";
  }

  private render(): void {
    const state = this.connected
      ? '<span class="ok">connected</span>'
      : `<span class="bad">disconnected</span>`;
    this.statusEl.innerHTML =
      `${state} — ${this.detail}\n` +
      `tick ${this.tick}  vehicles ${this.count}${this.starved ? "  (buffer starved)" : ""}\n` +
      `congestion overlay: client-derived per-lane mean speed`;
  }
}
