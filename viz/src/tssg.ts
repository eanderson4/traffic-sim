// tssg.ts — decoder for the live-plane signal-program frame (TSSG v1,
// ADR-0006 2026-07-20 M9 addendum; ADR-0011 §1) plus the chunked-table
// accumulator (ADR-0016): tables over ~768 KiB are published as a
// sequence of complete v1 frames (program_count = programs in THIS
// chunk — the decoder below is unchanged) carrying a `sig_chunk: "i/n"`
// NATS header (1-based, the kf_chunk idiom). A frame with NO sig_chunk
// header is the whole table (v1 back-compat). The wire carries the
// fixed-time program TABLE, never per-tick states: light state is a pure
// function of the tick count and the compiled program, so the client
// derives it by the same integer math as the kernel (phaseIndexAt mirrors
// SignalProgram.phaseAt). Layout (all little-endian):
//
//   header (24 B): magic u32 "TSSG" | schema_version u16 =1 | flags u16 |
//                  tick u64 | program_count u32 | reserved u32
//   per program: id_len u8 | id | junction_len u8 | junction |
//     offset_ticks u64 | phase_count u16 | link_count u16 |
//     per phase: duration_ticks u32 | state_len u8 | state (ASCII tlLogic)
//     per link: link_idx u16 | lane_id_len u8 | internal lane id
//
// The link list binds state-string indices to internal lane ids — the stop
// -line geometry comes from the static network GeoJSON (signals.ts), not
// from a second channel. u64 ticks/offsets arrive as BigInt via DataView
// and are narrowed to number (2^53 is not a live concern).

export const TSSG_MAGIC = 0x47535354; // "TSSG"
export const TSSG_VERSION = 1;
export const TSSG_HEADER_BYTES = 24;

// SIG_CHUNK_HEADER is the NATS header carrying the 1-based "i/n" chunk
// coordinate of a chunked table (ADR-0016; absent = whole table).
export const SIG_CHUNK_HEADER = "sig_chunk";

export type SigColor = "off" | "green" | "amber" | "red";

export interface SigPhase {
  durationTicks: number;
  state: string; // per-link tlLogic chars: g/G go, y amber, r red, else off
}

export interface SigLink {
  linkIdx: number; // index into the phase state strings
  laneId: string; // signal-bound internal lane
}

export interface SigProgram {
  id: string;
  junction: string;
  offsetTicks: number;
  phases: SigPhase[];
  links: SigLink[];
}

export interface SignalTable {
  tick: number; // publish tick (derivation reference; freshness rides TSSF)
  programs: SigProgram[];
}

class Cursor {
  private dv: DataView;
  off = 0;
  constructor(data: Uint8Array) {
    this.dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  }
  take(n: number): number {
    if (this.off + n > this.dv.byteLength) {
      throw new Error(`tssg: short read at ${this.off}, want ${n} of ${this.dv.byteLength}`);
    }
    const at = this.off;
    this.off += n;
    return at;
  }
  u8(): number {
    return this.dv.getUint8(this.take(1));
  }
  u16(): number {
    return this.dv.getUint16(this.take(2), true);
  }
  u32(): number {
    return this.dv.getUint32(this.take(4), true);
  }
  u64(): number {
    return Number(this.dv.getBigUint64(this.take(8), true));
  }
  str(): string {
    const n = this.u8();
    const at = this.take(n);
    let s = "";
    for (let i = 0; i < n; i++) s += String.fromCharCode(this.dv.getUint8(at + i));
    return s;
  }
  get remaining(): number {
    return this.dv.byteLength - this.off;
  }
}

export function decodeSignalFrame(data: Uint8Array): SignalTable {
  if (data.byteLength < TSSG_HEADER_BYTES) {
    throw new Error(`tssg: ${data.byteLength} bytes, want at least ${TSSG_HEADER_BYTES}`);
  }
  const dv = new DataView(data.buffer, data.byteOffset, data.byteLength);
  const magic = dv.getUint32(0, true);
  if (magic !== TSSG_MAGIC) {
    throw new Error(`tssg: bad magic 0x${magic.toString(16).padStart(8, "0")}`);
  }
  const version = dv.getUint16(4, true);
  if (version !== TSSG_VERSION) {
    throw new Error(`tssg: unsupported schema_version ${version}`);
  }
  const tick = Number(dv.getBigUint64(8, true));
  const count = dv.getUint32(16, true);
  const c = new Cursor(data);
  c.off = TSSG_HEADER_BYTES;
  const programs: SigProgram[] = [];
  for (let i = 0; i < count; i++) {
    const id = c.str();
    const junction = c.str();
    const offsetTicks = c.u64();
    const nPhases = c.u16();
    const nLinks = c.u16();
    const phases: SigPhase[] = [];
    for (let j = 0; j < nPhases; j++) {
      const durationTicks = c.u32();
      const state = c.str();
      if (durationTicks === 0) {
        throw new Error(`tssg: program "${id}" phase ${j}: zero duration`);
      }
      phases.push({ durationTicks, state });
    }
    const links: SigLink[] = [];
    for (let j = 0; j < nLinks; j++) {
      links.push({ linkIdx: c.u16(), laneId: c.str() });
    }
    programs.push({ id, junction, offsetTicks, phases, links });
  }
  if (c.remaining !== 0) {
    throw new Error(`tssg: ${c.remaining} trailing bytes`);
  }
  return { tick, programs };
}

// SigChunkCoord is the parsed sig_chunk header value: chunk i of n,
// 1-based (ADR-0016).
export interface SigChunkCoord {
  i: number;
  n: number;
}

// parseSigChunkHeader parses the sig_chunk header value: null when absent
// (a whole-table frame — the v1 back-compat), throws on a malformed one
// (a malformed header must never read as "whole table"). "" counts as
// absent: nats.ws MsgHdrs.get returns "" for a missing key, and rejecting
// it would kill every UNCHUNKED table (most demos).
export function parseSigChunkHeader(value: string | undefined | null): SigChunkCoord | null {
  if (value === undefined || value === null || value === "") return null;
  const m = /^(\d+)\/(\d+)$/.exec(value);
  if (m === null) throw new Error(`tssg: bad sig_chunk header ${JSON.stringify(value)}`);
  const i = Number(m[1]);
  const n = Number(m[2]);
  if (i < 1 || n < 1 || i > n) throw new Error(`tssg: bad sig_chunk header ${JSON.stringify(value)}`);
  return { i, n };
}

export interface AccumResult {
  // A COMPLETE generation (a whole-table frame, or the final chunk of a
  // set) — swap it in atomically. Null while a generation is in flight.
  table: SignalTable | null;
  // True when a partial accumulation was abandoned (gap, index
  // regression, or a count change) — the caller should request a resync.
  gap: boolean;
}

// SignalTableAccumulator reassembles chunked tables (ADR-0016 §4). Chunks
// of one generation arrive in publish order (NATS per-publisher
// ordering), so the rule is simple: collect 1..n; any gap, index
// regression, or chunk-count change resets the partial accumulation and
// waits for the next round (or the caller's resync request). The tick is
// NEVER an identity — a paused replay republishes the same tick — and a
// generation surfaces only when COMPLETE, so the installed table is
// never half-swapped. Duplicate or straggler chunks of an old round read
// as regressions and are dropped with it.
export class SignalTableAccumulator {
  private expected = 0; // next chunk index wanted (1-based); 0 = idle
  private total = 0; // n of the in-flight generation
  private programs: SigProgram[] = [];
  private tick = 0;

  // partial is true while a generation is incomplete (the caller arms its
  // resync timer off this).
  get partial(): boolean {
    return this.expected !== 0;
  }

  feed(frame: SignalTable, chunk: SigChunkCoord | null): AccumResult {
    if (chunk === null) {
      this.reset();
      return { table: frame, gap: false }; // whole table in one frame
    }
    if (chunk.i === 1) {
      // New generation — fresh start, or a regression replacing an
      // abandoned partial one (a round died mid-way).
      const abandoned = this.partial;
      this.total = chunk.n;
      this.programs = [...frame.programs];
      this.tick = frame.tick;
      if (chunk.n === 1) {
        this.reset();
        return { table: frame, gap: abandoned }; // 1-chunk set, complete
      }
      this.expected = 2;
      return { table: null, gap: abandoned };
    }
    if (!this.partial || chunk.i !== this.expected || chunk.n !== this.total) {
      // Gap or regression mid-generation (a dropped chunk, a count
      // change, or a straggler from an older round) — abandon it.
      this.reset();
      return { table: null, gap: true };
    }
    this.programs.push(...frame.programs);
    this.expected++;
    if (this.expected > this.total) {
      const table: SignalTable = { tick: this.tick, programs: this.programs };
      this.reset();
      return { table, gap: false };
    }
    return { table: null, gap: false };
  }

  private reset(): void {
    this.expected = 0;
    this.total = 0;
    this.programs = [];
  }
}

// phaseIndexAt mirrors engine.SignalProgram.phaseAt exactly (SUMO offset
// semantics: phase 0 begins at offsetTicks; before that the cycle wraps).
export function phaseIndexAt(p: SigProgram, tick: number): number {
  let cycle = 0;
  for (const ph of p.phases) cycle += ph.durationTicks;
  if (cycle === 0) return 0;
  let x = (tick % cycle + cycle - (p.offsetTicks % cycle)) % cycle;
  for (let i = 0; i < p.phases.length; i++) {
    const tk = p.phases[i]!.durationTicks;
    if (x < tk) return i;
    x -= tk;
  }
  return p.phases.length - 1; // unreachable (x < cycle), kept total
}

// stateCharAt returns the tlLogic char governing link at tick ("" when the
// link has no state char — the kernel's SigOff fallback).
export function stateCharAt(p: SigProgram, tick: number, linkIdx: number): string {
  const ph = p.phases[phaseIndexAt(p, tick)];
  if (!ph || linkIdx < 0 || linkIdx >= ph.state.length) return "";
  return ph.state[linkIdx]!;
}

// sigColorOf maps one tlLogic char to the render color, mirroring the
// kernel's mapSigChar (ADR-0011 §2): only g/G/y/r exert control; every
// other char (o/O/u, unknown, absent) is off.
export function sigColorOf(char: string): SigColor {
  switch (char) {
    case "g":
    case "G":
      return "green";
    case "y":
      return "amber";
    case "r":
      return "red";
    default:
      return "off";
  }
}
