// artic.ts — client-side trailer articulation for trucks. The engine
// simulates every vehicle as a rigid body, so the trailer is INFERRED here
// with the standard single-track trailer equation: the trailer axle follows
// the path of its hitch point, which is real kinematics, not decoration —
// on a turn the trailer cuts inside exactly as a physical trailer must.
//
// Per render frame, per truck (all inputs already flow through the vehicle
// channel: front-bumper point, heading, client-derived speed):
//
//   tractor center = front − (TRACTOR_M/2)·u(θ_front)
//   hitch H        = front − TRACTOR_M·u(θ_front)
//   θ_trailer     += (v·Δt / TRAILER_M)·sin(θ_front − θ_trailer)
//   trailer center = H − (TRAILER_M/2)·u(θ_trailer)
//
// The sine argument is wrapped to the shortest arc (a ±π crossing is the
// same physical direction, not a full spin). Below 0.1 m/s the angle is
// held — no articulation drift while queued. Δt is clamped (tab-switch
// frame gaps can't kick the trailer). State spawns aligned (θ_tr = θ_front)
// and is dropped on despawn via prune(). Pure and DOM-free so node --test
// can drive it.

import { TRACTOR_M, TRAILER_M } from "./theme.ts";

const HOLD_V = 0.1; // m/s — below this, hold θ_trailer (queued)
const MAX_DT_S = 0.05; // clamp wall-frame dt
const TWO_PI = 2 * Math.PI;

// wrapPi folds an angle difference to [−π, π) — the shortest arc.
export function wrapPi(d: number): number {
  return ((((d + Math.PI) % TWO_PI) + TWO_PI) % TWO_PI) - Math.PI;
}

export interface TrailerPose {
  tractorX: number;
  tractorY: number;
  tractorAngle: number;
  trailerX: number;
  trailerY: number;
  trailerAngle: number;
}

export class Articulator {
  private thetaTrailer = new Map<number, number>();

  // update advances truck id's trailer one frame and returns both body
  // poses in the local metric frame. front = front-bumper point, angles in
  // radians CCW-from-east (wire convention), v in m/s, dtS in seconds.
  update(
    id: number,
    frontX: number,
    frontY: number,
    thetaFront: number,
    v: number,
    dtS: number,
  ): TrailerPose {
    let tr = this.thetaTrailer.get(id);
    if (tr === undefined) {
      tr = thetaFront; // spawn aligned
    } else if (v >= HOLD_V) {
      const dt = Math.min(Math.max(dtS, 0), MAX_DT_S);
      tr += ((v * dt) / TRAILER_M) * Math.sin(wrapPi(thetaFront - tr));
    }
    this.thetaTrailer.set(id, tr);

    const ux = Math.cos(thetaFront);
    const uy = Math.sin(thetaFront);
    const hitchX = frontX - TRACTOR_M * ux;
    const hitchY = frontY - TRACTOR_M * uy;
    return {
      tractorX: frontX - (TRACTOR_M / 2) * ux,
      tractorY: frontY - (TRACTOR_M / 2) * uy,
      tractorAngle: thetaFront,
      trailerX: hitchX - (TRAILER_M / 2) * Math.cos(tr),
      trailerY: hitchY - (TRAILER_M / 2) * Math.sin(tr),
      trailerAngle: tr,
    };
  }

  // prune drops trailer state for ids no longer in the render set
  // (despawned trucks) — call once per frame with the live vehicle set.
  prune(live: ReadonlyMap<number, unknown>): void {
    for (const id of this.thetaTrailer.keys()) {
      if (!live.has(id)) this.thetaTrailer.delete(id);
    }
  }
}
