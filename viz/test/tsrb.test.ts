// tsrb.test.ts — TSRB v1 decoder tests against hand-built binary fixtures
// (the bytes are constructed here per the ADR-0023 §2 layout — the Go
// encoder lands with engine/cmd/bake), plus encodeTssf round-trips through
// the UNMODIFIED tssf.ts decoder (the synthetic-frame contract: the rest
// of the pipeline must not be able to tell a baked frame from a wire
// frame).

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  decodeTsrbChunk,
  encodeTssf,
  TSRB_HEADER_BYTES,
  TSRB_MAGIC,
  TSRB_VEHICLE_BYTES,
  TSRB_VERSION,
  type BakedQuant,
} from "../src/tsrb.ts";
import { decodeFrame, TSSF_HEADER_BYTES } from "../src/tssf.ts";

const QUANT: BakedQuant = { xyStepM: 0.1, origin: [1000, 2000] };

// buildChunk is an independent writer for the TSRB v1 layout (ADR-0023
// §2): header+records repeated, x/y quantized against the origin.
function buildChunk(
  frames: Array<{
    tick: number;
    vehicles: Array<{ id: number; x: number; y: number; angleQ: number; cls: number }>;
  }>,
  quant: BakedQuant = QUANT,
): Uint8Array {
  const total = frames.reduce((n, f) => n + TSRB_HEADER_BYTES + f.vehicles.length * TSRB_VEHICLE_BYTES, 0);
  const buf = new ArrayBuffer(total);
  const dv = new DataView(buf);
  let off = 0;
  for (const f of frames) {
    dv.setUint32(off, TSRB_MAGIC, true);
    dv.setUint16(off + 4, TSRB_VERSION, true);
    dv.setUint16(off + 6, 0, true); // flags
    dv.setBigUint64(off + 8, BigInt(f.tick), true);
    dv.setUint32(off + 16, f.vehicles.length, true);
    let r = off + TSRB_HEADER_BYTES;
    for (const v of f.vehicles) {
      dv.setUint32(r, v.id, true);
      dv.setUint32(r + 4, Math.round((v.x - quant.origin[0]) / quant.xyStepM), true);
      dv.setUint32(r + 8, Math.round((v.y - quant.origin[1]) / quant.xyStepM), true);
      dv.setUint8(r + 12, v.angleQ);
      dv.setUint8(r + 13, v.cls);
      r += TSRB_VEHICLE_BYTES;
    }
    off = r;
  }
  return new Uint8Array(buf);
}

test("decodes a multi-frame chunk with the count as the only delimiter", () => {
  const chunk = buildChunk([
    { tick: 0, vehicles: [{ id: 1, x: 1012.3, y: 2005.6, angleQ: 0, cls: 0 }] },
    { tick: 5, vehicles: [] }, // header-only frame (an empty window is still baked)
    {
      tick: 10,
      vehicles: [
        { id: 1, x: 1013.1, y: 2006.2, angleQ: 128, cls: 0 },
        { id: 2, x: 1000.0, y: 2000.0, angleQ: 255, cls: 1 },
      ],
    },
  ]);
  const frames = decodeTsrbChunk(chunk, QUANT);
  assert.equal(frames.length, 3);
  assert.equal(frames[0]!.tick, 0);
  assert.equal(frames[0]!.vehicles.length, 1);
  // Quantized dequantization is exact at the 0.1 m step.
  assert.equal(frames[0]!.vehicles[0]!.x, 1012.3);
  assert.equal(frames[0]!.vehicles[0]!.y, 2005.6);
  assert.equal(frames[1]!.tick, 5);
  assert.equal(frames[1]!.vehicles.length, 0);
  assert.equal(frames[2]!.vehicles.length, 2);
  // angle u8: q × 2π/256 — floor-quantized at bake, multiplied back here.
  assert.equal(frames[2]!.vehicles[0]!.angle, (128 / 256) * 2 * Math.PI);
  assert.equal(frames[2]!.vehicles[1]!.angle, (255 / 256) * 2 * Math.PI);
  assert.equal(frames[2]!.vehicles[1]!.cls, 1);
  assert.equal(frames[2]!.vehicles[1]!.x, 1000); // at the origin
});

test("rejects bad magic, wrong version, truncated header, and an overrun count", () => {
  const good = buildChunk([{ tick: 0, vehicles: [{ id: 1, x: 1001, y: 2001, angleQ: 0, cls: 0 }] }]);
  const badMagic = new Uint8Array(good);
  badMagic[0] = 0x58; // "XSRB"
  assert.throws(() => decodeTsrbChunk(badMagic, QUANT), /bad magic/);
  const badVersion = new Uint8Array(good);
  badVersion[4] = 2;
  assert.throws(() => decodeTsrbChunk(badVersion, QUANT), /schema_version 2/);
  assert.throws(() => decodeTsrbChunk(good.subarray(0, 10), QUANT), /truncated header/);
  const overrun = new Uint8Array(good);
  new DataView(overrun.buffer).setUint32(16, 99, true); // claims 99 vehicles
  assert.throws(() => decodeTsrbChunk(overrun, QUANT), /overruns/);
});

test("encodeTssf emits bytes the unmodified tssf.ts decoder accepts", () => {
  const vehicles = [
    { id: 7, x: 1012.3, y: 2005.6, angle: 1.25, cls: 0 },
    { id: 4294967295, x: -5.5, y: 0, angle: 0, cls: 2 }, // u32 max id rides u64
  ];
  const bytes = encodeTssf(12345, vehicles);
  assert.equal(bytes.byteLength, TSSF_HEADER_BYTES + vehicles.length * 24);
  const frame = decodeFrame(bytes); // THE live decoder, untouched
  assert.equal(frame.tick, 12345);
  assert.equal(frame.vehicles.length, 2);
  assert.equal(frame.vehicles[0]!.id, 7);
  assert.ok(Math.abs(frame.vehicles[0]!.x - 1012.3) < 1e-4); // f32 narrowing
  assert.ok(Math.abs(frame.vehicles[0]!.angle - 1.25) < 1e-6);
  assert.equal(frame.vehicles[1]!.id, 4294967295);
  assert.equal(frame.vehicles[1]!.cls, 2);
});

test("encodeTssf of zero vehicles is the 24 B clock-only frame (§6)", () => {
  const bytes = encodeTssf(500, []);
  assert.equal(bytes.byteLength, TSSF_HEADER_BYTES);
  const frame = decodeFrame(bytes);
  assert.equal(frame.tick, 500);
  assert.equal(frame.vehicles.length, 0);
});
