// tsrl.test.ts — TSRL v1 decoder tests against hand-built binary fixtures
// (bytes constructed here per the ADR-0023 §4 layout — the Go encoder
// lands with engine/cmd/bake).

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  decodeTsrlChunk,
  TSRL_HEADER_BYTES,
  TSRL_MAGIC,
  TSRL_PAIR_BYTES,
  TSRL_RATIO_SCALE,
  TSRL_VERSION,
} from "../src/tsrl.ts";

// buildChunk is an independent writer for the TSRL v1 layout (ADR-0023
// §4): header+pairs repeated, ratio pre-quantized ×170.
function buildChunk(frames: Array<{ tick: number; pairs: Array<{ laneIdx: number; ratioQ: number }> }>): Uint8Array {
  const total = frames.reduce((n, f) => n + TSRL_HEADER_BYTES + f.pairs.length * TSRL_PAIR_BYTES, 0);
  const buf = new ArrayBuffer(total);
  const dv = new DataView(buf);
  let off = 0;
  for (const f of frames) {
    dv.setUint32(off, TSRL_MAGIC, true);
    dv.setUint16(off + 4, TSRL_VERSION, true);
    dv.setUint16(off + 6, 0, true); // flags
    dv.setBigUint64(off + 8, BigInt(f.tick), true);
    dv.setUint32(off + 16, f.pairs.length, true);
    let r = off + TSRL_HEADER_BYTES;
    for (const p of f.pairs) {
      dv.setUint32(r, p.laneIdx, true);
      dv.setUint8(r + 4, p.ratioQ);
      r += TSRL_PAIR_BYTES;
    }
    off = r;
  }
  return new Uint8Array(buf);
}

test("decodes a multi-frame sparse chunk, ratio dequantized ×170", () => {
  const chunk = buildChunk([
    { tick: 0, pairs: [{ laneIdx: 0, ratioQ: 170 }, { laneIdx: 7, ratioQ: 85 }] },
    { tick: 50, pairs: [] }, // no occupied lanes at the aggregate tick
    { tick: 100, pairs: [{ laneIdx: 3, ratioQ: 255 }] }, // 1.5 clamp at bake
  ]);
  const frames = decodeTsrlChunk(chunk);
  assert.equal(frames.length, 3);
  assert.equal(frames[0]!.tick, 0);
  assert.deepEqual(frames[0]!.pairs, [
    { laneIdx: 0, ratio: 1 },
    { laneIdx: 7, ratio: 0.5 },
  ]);
  assert.equal(frames[1]!.pairs.length, 0);
  assert.equal(frames[2]!.pairs[0]!.ratio, 255 / TSRL_RATIO_SCALE);
});

test("rejects bad magic, wrong version, truncated header, and an overrun count", () => {
  const good = buildChunk([{ tick: 0, pairs: [{ laneIdx: 1, ratioQ: 10 }] }]);
  const badMagic = new Uint8Array(good);
  badMagic[0] = 0x58;
  assert.throws(() => decodeTsrlChunk(badMagic), /bad magic/);
  const badVersion = new Uint8Array(good);
  badVersion[4] = 9;
  assert.throws(() => decodeTsrlChunk(badVersion), /schema_version 9/);
  assert.throws(() => decodeTsrlChunk(good.subarray(0, 12)), /truncated header/);
  const overrun = new Uint8Array(good);
  new DataView(overrun.buffer).setUint32(16, 50, true);
  assert.throws(() => decodeTsrlChunk(overrun), /overruns/);
});
