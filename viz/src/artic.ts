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
// held — no articulation drift while queued. Δt is SIM time (main.ts
// derives it from the sample's tick progression × dt — at 8× replay the
// tractor advances 8 sim-seconds per wall second), integrated in substeps
// of ≤ MAX_DT_S so a large legitimate delta (up to ~1 sim-second per
// render frame at 8×) converges like the many small steps it replaces. A
// delta beyond MAX_TOTAL_DT_S means the world JUMPED (rAF suspension in a
// backgrounded tab): the trailer re-anchors aligned, mini-seek style,
// instead of stepping through minutes of stale sim time. State spawns
// aligned (θ_tr = θ_front) and is dropped on despawn via prune(); reset()
// drops everything (replay seeks). Pure and DOM-free so node --test can
// drive it.
//
// Glitch clamp: a hitch angle beyond MAX_HITCH_RAD is not a pose a
// forward-driving semi reaches (tight intersection turns peak ~45–60°) —
// it is a lane-hop hitch teleport, a reversed-fragment heading flip, or a
// curved-spawn artifact. The sine law's recovery from there is repelled
// through near-perpendicular angles at a rate ∝ v, and if the truck then
// queues below HOLD_V the angle freezes — the "perpendicular trailer
// stuck in traffic" artifact. The engine has no jackknife crashes to
// honor, so the pose re-anchors aligned, on every path including the
// queued hold.
//
// Two false-positive guards on the clamp (Fable + Sol, WQ-2 review): the
// base threshold sits at 85°, not just past the ~60° a tight turn peaks
// at — a sub-10 m-radius internal lane can hold a stable ~80°
// articulation (sin gap = L/r with L = 8 m), and snapping that glues the
// trailer to the tractor through every legit tight turn. And the
// pre-integration gap is measured against the NEW θ_front, so it
// includes the heading the tractor swept during dtS — at 8× replay a 60°
// steady turn (ω ≈ 62°/s at v = 10) reads ~91° on a 0.5 s frame. The
// limit therefore widens by the sweep the trailer dynamics allow in dtS,
// (v/L)·dtS; only true discontinuities (heading flips, hitch teleports)
// exceed the widened limit. The check runs only on frames with dtS > 0:
// dtS advances in per-snapshot bursts (integer sample ticks) while the
// lerped heading advances every render frame, so an interpolated frame's
// gap growth carries no time credit — but new headings, and therefore
// glitches, only ever arrive on snapshot frames.

import { TRACTOR_M, TRAILER_M } from "./theme.ts";

const HOLD_V = 0.1; // m/s — below this, hold θ_trailer (queued)
const MAX_DT_S = 0.05; // max integration substep, s
const MAX_TOTAL_DT_S = 2; // s — beyond this the world jumped: re-anchor, don't step
const MAX_HITCH_RAD = (85 * Math.PI) / 180; // base glitch threshold, rad
const MAX_GLITCH_LIMIT_RAD = (170 * Math.PI) / 180; // the widened limit never disables the clamp
const TWO_PI = 2 * Math.PI;

// wrapPi folds an angle difference to [−π, π) — the shortest arc.
export function wrapPi(d: number): number {
  return ((((d + Math.PI) % TWO_PI) + TWO_PI) % TWO_PI) - Math.PI;
}

// glitchLimit is the hitch angle past which a pose is a glitch, widened
// by the heading the tractor could legitimately sweep during dtS
// (header): the pre-integration gap is measured against the NEW θ_front,
// so a legit tight turn at fast replay must not trip the base threshold.
// dtS is clamped to [0, MAX_TOTAL_DT_S] — negative seeks integrate
// nothing, and past MAX_TOTAL_DT_S the world-jump path re-anchors anyway.
// The limit is capped at 170°: |wrapPi| ≤ π, so an uncapped limit (a
// highway-speed truck on a fat replay frame) would exceed π and disable
// the clamp exactly when a full heading flip needs it (Sol, WQ-2 round 3).
function glitchLimit(v: number, dtS: number): number {
  const dt = Math.min(Math.max(dtS, 0), MAX_TOTAL_DT_S);
  return Math.min(MAX_HITCH_RAD + (v * dt) / TRAILER_M, MAX_GLITCH_LIMIT_RAD);
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
  // radians CCW-from-east (wire convention), v in m/s, dtS in SIM seconds
  // (main.ts derives it from tick progression — large values from fast
  // replay are legitimate and integrated in substeps).
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
    } else if (dtS > 0 && Math.abs(wrapPi(thetaFront - tr)) > glitchLimit(v, dtS)) {
      // Glitch clamp (header), checked BEFORE the hold/integrate paths: a
      // large fast-replay delta could integrate a glitched angle back
      // under the threshold within one frame and keep it.
      //
      // Gated on dtS > 0: main.ts derives dtS from the INTEGER sample
      // tick, which advances in one burst per snapshot while the lerped
      // heading advances EVERY render frame — on an interpolated (dtS =
      // 0) frame the gap growth is just lerp advance carrying no time
      // credit, and clamping there would read a legit tight turn as a
      // glitch (Sol, WQ-2 round 4). New headings — and therefore
      // glitches — only ARRIVE on snapshot (dtS > 0) frames, so the
      // clamp only applies there.
      tr = thetaFront;
    } else if (v >= HOLD_V) {
      if (dtS > MAX_TOTAL_DT_S) {
        // The world JUMPED (rAF suspension in a backgrounded tab can hand
        // us minutes of sim time): re-anchor aligned, mini-seek style —
        // stepping through it would hang the viewer on millions of
        // substeps, and the stale angle is meaningless anyway.
        tr = thetaFront;
      } else {
        // Substep: explicit Euler with a ~1 s step (8× replay delivers up
        // to ~1 sim-second per render frame) would overshoot; ≤ MAX_DT_S
        // steps converge like the per-frame integration they replace. A
        // negative dt (a seek the caller didn't reset) integrates nothing.
        let rem = Math.max(dtS, 0);
        while (rem > 0) {
          const dt = Math.min(rem, MAX_DT_S);
          tr += ((v * dt) / TRAILER_M) * Math.sin(wrapPi(thetaFront - tr));
          rem -= dt;
        }
      }
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

  // reset drops ALL trailer state. A replay seek teleports every truck
  // (ids survive), so persisted angles belong to the abandoned states —
  // and a paused landing reports speed 0, below HOLD_V, so articulation
  // would never reconverge. The next update re-spawns each trailer
  // aligned with its tractor.
  reset(): void {
    this.thetaTrailer.clear();
  }
}
