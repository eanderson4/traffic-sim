// tsrb.ts — decoder for the baked vehicle-frame stream (TSRB v1, ADR-0023
// §2) plus the encoder for the SYNTHETIC TSSF v1 frames the baked shim
// re-encodes merged TSRB records into (ADR-0023 §6: tssf.ts,
// SnapshotBuffer, the artic channel, and the render loop run byte-for-byte
// untouched). Layout (all little-endian):
//
//   header (20 B): magic u32 "TSRB" | schema_version u16 =1 | flags u16 |
//                  tick u64 | vehicle_count u32
//   per vehicle (14 B): id u32 | x u32 | y u32 | angle u8 | class u8
//
// A chunk is header+records REPEATED (vehicle_count is the only frame
// delimiter, mirroring TSSF's self-delimiting header). x/y are quantized
// to 0.1 m steps in the network's LOCAL METRIC FRAME, biased by the index
// manifest's quant origin: c = origin + q × step. angle u8 is the tangent
// normalized into [0, 2π) then floored to 256 steps (≈1.4°); the decoder
// multiplies back, no reserved values.

import {
  TSSF_HEADER_BYTES,
  TSSF_MAGIC,
  TSSF_VEHICLE_BYTES,
  TSSF_VERSION,
  type VehicleRecord,
} from "./tssf.ts";

export const TSRB_MAGIC = 0x42525354; // "TSRB"
export const TSRB_VERSION = 1;
export const TSRB_HEADER_BYTES = 20;
export const TSRB_VEHICLE_BYTES = 14;

// BakedQuant mirrors index.json's `quant` member: the quantization step
// and the network-bbox origin the u32 coordinates are biased by.
export interface BakedQuant {
  xyStepM: number;
  origin: [number, number];
}

export interface BakedFrame {
  tick: number;
  vehicles: VehicleRecord[];
}

const TWO_PI = 2 * Math.PI;

// decodeTsrbChunk decodes one region chunk: header+records repeated to EOF.
export function decodeTsrbChunk(data: Uint8Array, quant: BakedQuant): BakedFrame[] {
  const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const frames: BakedFrame[] = [];
  let off = 0;
  while (off < data.byteLength) {
    if (off + TSRB_HEADER_BYTES > data.byteLength) {
      throw new Error(`tsrb: truncated header at ${off} of ${data.byteLength}`);
    }
    const magic = dv.getUint32(off, true);
    if (magic !== TSRB_MAGIC) {
      throw new Error(`tsrb: bad magic 0x${magic.toString(16).padStart(8, "0")} at ${off}`);
    }
    const version = dv.getUint16(off + 4, true);
    if (version !== TSRB_VERSION) {
      throw new Error(`tsrb: unsupported schema_version ${version}`);
    }
    const tick = Number(dv.getBigUint64(off + 8, true));
    const count = dv.getUint32(off + 16, true);
    const recStart = off + TSRB_HEADER_BYTES;
    if (recStart + count * TSRB_VEHICLE_BYTES > data.byteLength) {
      throw new Error(`tsrb: ${count} vehicles at ${off} overruns ${data.byteLength} bytes`);
    }
    const vehicles: VehicleRecord[] = new Array(count);
    let r = recStart;
    for (let i = 0; i < count; i++) {
      vehicles[i] = {
        id: dv.getUint32(r, true),
        x: quant.origin[0] + dv.getUint32(r + 4, true) * quant.xyStepM,
        y: quant.origin[1] + dv.getUint32(r + 8, true) * quant.xyStepM,
        angle: (dv.getUint8(r + 12) / 256) * TWO_PI,
        cls: dv.getUint8(r + 13),
      };
      r += TSRB_VEHICLE_BYTES;
    }
    frames.push({ tick, vehicles });
    off = recStart + count * TSRB_VEHICLE_BYTES;
  }
  return frames;
}

// encodeTssf re-encodes decoded (merged, dequantized) vehicle records into
// a synthetic TSSF v1 frame (ADR-0023 §6) — the exact bytes
// subscribeSnapshots would have delivered off the wire, so tssf.ts and
// everything downstream runs untouched. cls rides f32 as on the live wire.
export function encodeTssf(tick: number, vehicles: readonly VehicleRecord[]): Uint8Array {
  const buf = new ArrayBuffer(TSSF_HEADER_BYTES + vehicles.length * TSSF_VEHICLE_BYTES);
  const dv = new DataView(buf);
  dv.setUint32(0, TSSF_MAGIC, true);
  dv.setUint16(4, TSSF_VERSION, true);
  dv.setUint16(6, 0, true); // flags
  dv.setBigUint64(8, BigInt(tick), true);
  dv.setUint32(16, vehicles.length, true);
  dv.setUint32(20, 0, true); // reserved
  let off = TSSF_HEADER_BYTES;
  for (const v of vehicles) {
    dv.setBigUint64(off, BigInt(v.id), true);
    dv.setFloat32(off + 8, v.x, true);
    dv.setFloat32(off + 12, v.y, true);
    dv.setFloat32(off + 16, v.angle, true);
    dv.setFloat32(off + 20, v.cls, true);
    off += TSSF_VEHICLE_BYTES;
  }
  return new Uint8Array(buf);
}
