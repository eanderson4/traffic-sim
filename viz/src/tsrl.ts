// tsrl.ts — decoder for the baked lane-speed aggregate stream (TSRL v1,
// ADR-0023 §4): sparse per-region (lane_idx, ratio) pairs sampled as the
// instantaneous per-lane mean speed / speedLimit at the aggregate tick,
// ratio × 170 quantized to u8 (0–1.5 clamp, mirroring congestion.ts's
// laneSpeedRatios). lane_idx indexes the manifest's deduped occupied-lane
// table (lanes.json) — the shim resolves ids, this decoder stays table-
// free. Layout (all little-endian):
//
//   header (20 B): magic u32 "TSRL" | schema_version u16 =1 | flags u16 |
//                  tick u64 | pair_count u32
//   per pair (5 B): lane_idx u32 | ratio_q u8
//
// A chunk is header+pairs REPEATED (pair_count is the only delimiter,
// same rule as TSRB).

export const TSRL_MAGIC = 0x4c525354; // "TSRL"
export const TSRL_VERSION = 1;
export const TSRL_HEADER_BYTES = 20;
export const TSRL_PAIR_BYTES = 5;

// RATIO_SCALE is the bake's ratio→u8 quantization (ADR-0023 §4): decode by
// dividing back; the 0–1.5 clamp was applied at bake time.
export const TSRL_RATIO_SCALE = 170;

export interface TsrlPair {
  laneIdx: number; // index into the manifest's lanes.json table
  ratio: number; // mean speed / speedLimit, dequantized (q / 170)
}

export interface TsrlFrame {
  tick: number;
  pairs: TsrlPair[];
}

// decodeTsrlChunk decodes one region chunk: header+pairs repeated to EOF.
export function decodeTsrlChunk(data: Uint8Array): TsrlFrame[] {
  const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const frames: TsrlFrame[] = [];
  let off = 0;
  while (off < data.byteLength) {
    if (off + TSRL_HEADER_BYTES > data.byteLength) {
      throw new Error(`tsrl: truncated header at ${off} of ${data.byteLength}`);
    }
    const magic = dv.getUint32(off, true);
    if (magic !== TSRL_MAGIC) {
      throw new Error(`tsrl: bad magic 0x${magic.toString(16).padStart(8, "0")} at ${off}`);
    }
    const version = dv.getUint16(off + 4, true);
    if (version !== TSRL_VERSION) {
      throw new Error(`tsrl: unsupported schema_version ${version}`);
    }
    const tick = Number(dv.getBigUint64(off + 8, true));
    const count = dv.getUint32(off + 16, true);
    const recStart = off + TSRL_HEADER_BYTES;
    if (recStart + count * TSRL_PAIR_BYTES > data.byteLength) {
      throw new Error(`tsrl: ${count} pairs at ${off} overruns ${data.byteLength} bytes`);
    }
    const pairs: TsrlPair[] = new Array(count);
    let r = recStart;
    for (let i = 0; i < count; i++) {
      pairs[i] = { laneIdx: dv.getUint32(r, true), ratio: dv.getUint8(r + 4) / TSRL_RATIO_SCALE };
      r += TSRL_PAIR_BYTES;
    }
    frames.push({ tick, pairs });
    off = recStart + count * TSRL_PAIR_BYTES;
  }
  return frames;
}
