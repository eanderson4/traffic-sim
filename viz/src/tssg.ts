// tssg.ts — decoder for the live-plane signal-program frame (TSSG v1,
// ADR-0006 2026-07-20 M9 addendum; ADR-0011 §1), the TS mirror of
// engine/natsio/sigframe.go. The wire carries the fixed-time program TABLE,
// never per-tick states: light state is a pure function of the tick count
// and the compiled program, so the client derives it by the same integer
// math as the kernel (phaseIndexAt mirrors SignalProgram.phaseAt). Layout
// (all little-endian):
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
