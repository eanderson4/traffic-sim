// snapshots.ts — client-side snapshot interpolation (research decision 2 in
// docs/kb/raw/integration-maplibre-realtime/synthesis.md): the 10 Hz network
// stream is decoupled from the 60 fps render by lerping between the two
// snapshots bracketing a render time that trails the wall clock by a
// ~250 ms buffer. Extrapolation is REJECTED (replay must show what *was*
// simulated) — a starved buffer holds the newest snapshot instead.
//
// Spawn/despawn mapping: the render set is the newer snapshot's vehicles;
// a vehicle present in both snapshots lerps, one only in the newer appears
// at its newer position (spawn), one only in the older is gone (despawn).
// Speed is derived client-side (TSSF v1 carries no speed field) from the
// displacement between the two source snapshots over SIM time
// ((newer.tick - older.tick) × dt) — never the wall-clock arrival span,
// which shrinks under faster-than-realtime replay and would inflate every
// speed (and free-flow the congestion overlay) by the pace factor.

import type { SnapshotFrame } from "./tssf.ts";

export interface TimedFrame {
  frame: SnapshotFrame;
  recvMs: number; // performance.now() at receipt
}

export interface RenderVehicle {
  id: number;
  x: number; // local metric frame
  y: number;
  angle: number; // rad, shortest-arc lerped
  cls: number;
  speed: number; // m/s, client-derived from snapshot displacement
}

export interface Sample {
  vehicles: RenderVehicle[];
  tick: number; // sim tick of the newer source snapshot
  starved: boolean; // render time ran past the newest snapshot (held, not extrapolated)
}

function lerpAngle(a: number, b: number, t: number): number {
  let d = b - a;
  while (d > Math.PI) d -= 2 * Math.PI;
  while (d < -Math.PI) d += 2 * Math.PI;
  return a + d * t;
}

export class SnapshotBuffer {
  private frames: TimedFrame[] = [];
  private bufferMs: number;
  simDt: number; // engine timestep, s — sim seconds per tick (see setSimDt)
  private readonly maxFrames: number;

  // NOTE: erasable-syntax only (node strip-only mode loads this directly).
  constructor(bufferMs = 250, simDt = 0.1, maxFrames = 60 /* 6 s at 10 Hz — ample for the 250 ms buffer */) {
    this.bufferMs = bufferMs;
    this.simDt = simDt;
    this.maxFrames = maxFrames;
  }

  // setBufferMs re-sizes the interpolation buffer WITHOUT a rebuild
  // (buffered frames are interpolation history — dropping them mid-play
  // would smear). Baked replay (ADR-0023 §2) needs this: at the 2 Hz bake
  // cadence the frame interval (500 ms) exceeds the 250 ms default, so the
  // buffer tracks 1.25 × frameInterval / playback speed instead — field
  // assignment only, never a reset.
  setBufferMs(ms: number): void {
    this.bufferMs = ms;
  }

  // setSimDt re-targets the sim seconds-per-tick behind speed derivation.
  // On a replay the status payload carries the RECORDED run's authoritative
  // dt — the ?dt= URL hint comes from the (mutable) scenario — so the
  // replay panel pushes the status value down on its first probe (main.ts).
  setSimDt(dt: number): void {
    this.simDt = dt;
  }

  // push appends a decoded frame stamped with its receipt time. Out-of-order
  // or duplicate ticks are dropped (core NATS is at-most-once; reordering is
  // not expected on a single subscriber, and the buffer is not a resync
  // tool). A replay seek never reaches here as a drop or a jump: the caller
  // detects it first (SeekGate below — backward tick or forward jump), resets
  // the buffer, and the landing frame then pushes fresh.
  push(frame: SnapshotFrame, recvMs: number): void {
    const last = this.frames[this.frames.length - 1];
    if (last && frame.tick <= last.frame.tick) return;
    this.frames.push({ frame, recvMs });
    if (this.frames.length > this.maxFrames) {
      this.frames.splice(0, this.frames.length - this.maxFrames);
    }
  }

  // reset drops every buffered frame. A replay seek abandons the buffered
  // states — lerping the landing frame against them would smear vehicles
  // across the jump — so the caller resets here and pushes the landing
  // frame fresh (it renders as-is until the next frame arrives).
  reset(): void {
    this.frames = [];
  }

  // sample interpolates the render set for wall time nowMs. Returns null
  // before the first frame arrives.
  sample(nowMs: number): Sample | null {
    const fs = this.frames;
    if (fs.length === 0) return null;
    const renderAt = nowMs - this.bufferMs;
    const newest = fs[fs.length - 1]!;

    // Find the pair bracketing renderAt (i < renderAt <= i+1); a render time
    // at/behind the oldest frame clamps to it, past the newest holds newest.
    let older = fs[0]!;
    let newer = fs[0]!;
    let starved = false;
    if (renderAt >= newest.recvMs) {
      starved = renderAt > newest.recvMs + 1;
      older = fs.length > 1 ? fs[fs.length - 2]! : newest;
      newer = newest;
    } else {
      for (let i = fs.length - 1; i > 0; i--) {
        if (fs[i - 1]!.recvMs <= renderAt) {
          older = fs[i - 1]!;
          newer = fs[i]!;
          break;
        }
      }
    }

    const span = newer.recvMs - older.recvMs;
    const t = starved || span <= 0 ? 1 : Math.min(1, Math.max(0, (renderAt - older.recvMs) / span));
    // Speed divides by the SIM-time span, not the wall-clock span above:
    // faster-than-realtime replay delivers the same sim content faster,
    // and speed is a property of the sim, not of the delivery rate.
    const simSec = (newer.frame.tick - older.frame.tick) * this.simDt;

    const prevById = new Map<number, { x: number; y: number; angle: number }>();
    if (older !== newer) {
      for (const v of older.frame.vehicles) prevById.set(v.id, v);
    }
    const vehicles: RenderVehicle[] = [];
    for (const v of newer.frame.vehicles) {
      const p = prevById.get(v.id);
      if (p === undefined || older === newer) {
        vehicles.push({ id: v.id, x: v.x, y: v.y, angle: v.angle, cls: v.cls, speed: 0 });
        continue;
      }
      const x = p.x + (v.x - p.x) * t;
      const y = p.y + (v.y - p.y) * t;
      vehicles.push({
        id: v.id,
        x,
        y,
        angle: lerpAngle(p.angle, v.angle, t),
        cls: v.cls,
        speed: simSec > 0 ? Math.hypot(v.x - p.x, v.y - p.y) / simSec : 0,
      });
    }
    return { vehicles, tick: newer.frame.tick, starved };
  }
}

// SeekGate detects seeks on a replay stream. A live run publishes strictly
// increasing ticks; the replay control plane (demosrv /api/replay/ctl/seek)
// restarts publication at the seek target, and paused replay republishes
// the SAME tick. Two shapes are seeks: a tick DECREASE (scrub back) and a
// forward jump beyond maxJump (scrub ahead — accepted-as-progression would
// lerp across unrelated states and derive bogus speeds). The gate records
// every observed tick and reports whether the stream jumped, so the caller
// (main.ts) can drop the interpolation history (SnapshotBuffer.reset) and
// wipe tick-derived overlay state before accepting the landing frame.
// Equal ticks (paused republication) and normal increments report false —
// push's own duplicate drop handles the former.
export class SeekGate {
  private lastTick: number | null = null;
  private maxJump: number;

  // simDt is the engine timestep (s/tick, config.ts ?dt=): the forward-jump
  // threshold is a 24-sim-second window expressed in ticks — dt 0.1 →
  // 240, dt 0.001 → 24000 (normal 8× frames advance ~800 ticks there, no
  // false seeks). 24 sim-seconds is far beyond any decimated-frame
  // increment or plausible delivery stall at demo paces, so only a
  // deliberate scrub-ahead trips the gate.
  constructor(simDt = 0.1) {
    this.maxJump = Math.ceil(24 / simDt);
  }

  // setSimDt re-derives the threshold when the recorded run's authoritative
  // dt replaces the URL hint (mirrors SnapshotBuffer.setSimDt — main.ts's
  // onStatus re-targets both). Observed ticks are unaffected.
  setSimDt(simDt: number): void {
    this.maxJump = Math.ceil(24 / simDt);
  }

  // observe records tick and returns true when it moved BACKWARD vs the
  // previously observed tick or JUMPED forward past maxJump.
  observe(tick: number): boolean {
    const seek =
      this.lastTick !== null && (tick < this.lastTick || tick - this.lastTick > this.maxJump);
    this.lastTick = tick;
    return seek;
  }
}
