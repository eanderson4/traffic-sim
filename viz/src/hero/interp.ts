// hero/interp.ts — the pure math behind fractional-tick playback. Baked
// frames land at the bake stride (2 Hz at dt 0.1, bakeEveryTicks 5); the
// render loop advances a FLOAT tick at display refresh and poses vehicles
// between the two bracketing frames. Kept THREE-free so the unit tests
// exercise the exact functions the render loop runs.

import { bakedTickAt, floorToBakedTick, frameIndexOfTick, type BakedIndex } from "../baked.ts";
import type { VehicleRecord } from "../tssf.ts";

const TWO_PI = 2 * Math.PI;

// lerpAngle is the shortest-path lerp between two headings (rad, wrap at
// ±π): a vehicle swinging 350°→10° lerps through 0°, never the long way.
export function lerpAngle(a: number, b: number, alpha: number): number {
  let d = (b - a) % TWO_PI;
  if (d > Math.PI) d -= TWO_PI;
  else if (d < -Math.PI) d += TWO_PI;
  return a + d * alpha;
}

// Bracket locates a fractional tick between baked frames: k0/tick0 are
// the floor frame (signals and the readout follow it — they are
// discrete), tick1 the next baked tick (=== tick0 past the last frame:
// no extrapolation), alpha the lerp fraction in [0, 1).
export interface Bracket {
  k0: number;
  tick0: number;
  tick1: number;
  alpha: number;
}

// bracketAt fills `out` (no per-frame allocation in the render loop) and
// returns it. t clamps to the baked range; the terminal off-stride frame
// brackets with its true tick spacing (baked.ts's helpers already carry
// that convention).
export function bracketAt(
  index: Pick<BakedIndex, "tickStart" | "tickEnd" | "bakeEveryTicks">,
  frameCount: number,
  t: number,
  out: Bracket,
): Bracket {
  const clamped = Math.min(index.tickEnd, Math.max(index.tickStart, t));
  const tick0 = floorToBakedTick(index, clamped);
  const k0 = frameIndexOfTick(index, tick0);
  const tick1 = k0 + 1 < frameCount ? bakedTickAt(index, k0 + 1) : tick0;
  out.k0 = k0;
  out.tick0 = tick0;
  out.tick1 = tick1;
  out.alpha = tick1 > tick0 ? (clamped - tick0) / (tick1 - tick0) : 0;
  return out;
}

// walkPairs runs the id-sorted two-pointer match between the bracketing
// frames: onPair fires once per id present in BOTH (the lerp set), in
// ascending id order, allocating nothing. Vehicles in only one frame are
// skipped — a spawn/despawn renders only on its own frame (the pop lands
// on the baked boundary nearest its presence; no fading, no ghost pose,
// no extrapolation).
export function walkPairs(
  a: readonly VehicleRecord[],
  b: readonly VehicleRecord[],
  onPair: (va: VehicleRecord, vb: VehicleRecord) => void,
): void {
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    const va = a[i]!;
    const vb = b[j]!;
    if (va.id === vb.id) {
      onPair(va, vb);
      i++;
      j++;
    } else if (va.id < vb.id) {
      i++;
    } else {
      j++;
    }
  }
}
