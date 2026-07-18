// tssf.ts — decoder for the live-plane snapshot frame (TSSF v1, ADR-0006
// §7), the TS mirror of engine/natsio/frame.go. Layout (all little-endian):
//
//   header (24 B): magic u32 "TSSF" | schema_version u16 =1 | flags u16 |
//                  tick u64 | vehicle_count u32 | reserved u32
//   per vehicle (24 B): id u64 | x f32 | y f32 | angle f32 | class f32
//
// x/y are the network's LOCAL METRIC FRAME (see proj.ts); angle is the lane
// tangent in radians (0 = +x/east, CCW positive, north-up); class is the
// scenario vehicle-type index (0 = car, 1 = truck, …) carried as f32.
// u64 ids/ticks arrive as BigInt via DataView and are narrowed to number —
// engine ids are sequential from 1, so 2^53 is not a live concern.

export const TSSF_MAGIC = 0x46535354; // "TSSF"
export const TSSF_VERSION = 1;
export const TSSF_HEADER_BYTES = 24;
export const TSSF_VEHICLE_BYTES = 24;

export interface VehicleRecord {
  id: number;
  x: number;
  y: number;
  angle: number; // rad, 0 = east, CCW
  cls: number; // scenario type index (0 = car)
}

export interface SnapshotFrame {
  tick: number;
  vehicles: VehicleRecord[];
}

export function decodeFrame(data: Uint8Array): SnapshotFrame {
  if (data.byteLength < TSSF_HEADER_BYTES) {
    throw new Error(`tssf: ${data.byteLength} bytes, want at least ${TSSF_HEADER_BYTES}`);
  }
  const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const magic = dv.getUint32(0, true);
  if (magic !== TSSF_MAGIC) {
    throw new Error(`tssf: bad magic 0x${magic.toString(16).padStart(8, "0")}`);
  }
  const version = dv.getUint16(4, true);
  if (version !== TSSF_VERSION) {
    throw new Error(`tssf: unsupported schema_version ${version}`);
  }
  const tick = Number(dv.getBigUint64(8, true));
  const count = dv.getUint32(16, true);
  const expected = TSSF_HEADER_BYTES + count * TSSF_VEHICLE_BYTES;
  if (data.byteLength !== expected) {
    throw new Error(`tssf: ${data.byteLength} bytes, want ${expected} for ${count} vehicles`);
  }
  const vehicles: VehicleRecord[] = new Array(count);
  let off = TSSF_HEADER_BYTES;
  for (let i = 0; i < count; i++) {
    vehicles[i] = {
      id: Number(dv.getBigUint64(off, true)),
      x: dv.getFloat32(off + 8, true),
      y: dv.getFloat32(off + 12, true),
      angle: dv.getFloat32(off + 16, true),
      cls: dv.getFloat32(off + 20, true),
    };
    off += TSSF_VEHICLE_BYTES;
  }
  return { tick, vehicles };
}
