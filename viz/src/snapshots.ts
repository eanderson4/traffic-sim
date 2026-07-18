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
// displacement between the two source snapshots — a labelled estimate.

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
  readonly bufferMs: number;
  private readonly maxFrames: number;

  // NOTE: erasable-syntax only (node strip-only mode loads this directly).
  constructor(bufferMs = 250, maxFrames = 60 /* 6 s at 10 Hz — ample for the 250 ms buffer */) {
    this.bufferMs = bufferMs;
    this.maxFrames = maxFrames;
  }

  // push appends a decoded frame stamped with its receipt time. Out-of-order
  // or duplicate ticks are dropped (core NATS is at-most-once; reordering is
  // not expected on a single subscriber, and the buffer is not a resync tool).
  push(frame: SnapshotFrame, recvMs: number): void {
    const last = this.frames[this.frames.length - 1];
    if (last && frame.tick <= last.frame.tick) return;
    this.frames.push({ frame, recvMs });
    if (this.frames.length > this.maxFrames) {
      this.frames.splice(0, this.frames.length - this.maxFrames);
    }
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
    const dtSec = span / 1000;

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
        speed: dtSec > 0 ? Math.hypot(v.x - p.x, v.y - p.y) / dtSec : 0,
      });
    }
    return { vehicles, tick: newer.frame.tick, starved };
  }
}
