// tssg.test.ts — TSSG v1 decoder + derivation tests against (a) the golden
// frame produced by the Go encoder (engine/natsio/sigframe_test.go's
// sigFixture at tick 0 — the SAME constant pinned there) and (b) fixtures
// from an independent writer covering offsets, unknown chars, and every
// rejection path.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  decodeSignalFrame,
  phaseIndexAt,
  stateCharAt,
  sigColorOf,
  TSSG_MAGIC,
  TSSG_VERSION,
  TSSG_HEADER_BYTES,
  type SigProgram,
} from "../src/tssg.ts";

// Golden: Go natsio.TestSignalFrameGolden, sigFixture — sigA (J1): links
// iJ1_0/iJ1_1, phases 10t "Gr" / 5t "yr", offset 0; sigB (J2): link iJ2_0,
// phases 20t "r" / 10t "G", offset 5 ticks.
const GO_SIG_TICK0 =
  "5453534701000000000000000000000002000000000000000473696741024a310000000000000000020002000a00000002477205000000027972000005694a315f30010005694a315f310473696742024a320500000000000000020001001400000001720a0000000147000005694a325f30";

function fromHex(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(2 * i, 2 * i + 2), 16);
  return out;
}

// buildSignalFrame is an independent writer for the TSSG v1 layout.
function buildSignalFrame(tick: bigint, programs: SigProgram[]): Uint8Array {
  const parts: number[] = [];
  const u16 = (x: number) => parts.push(x & 0xff, (x >> 8) & 0xff);
  const u32 = (x: number) => parts.push(x & 0xff, (x >> 8) & 0xff, (x >> 16) & 0xff, (x >> 24) & 0xff);
  const u64 = (x: bigint) => {
    for (let i = 0; i < 8; i++) parts.push(Number((x >> BigInt(8 * i)) & 0xffn));
  };
  const str = (s: string) => {
    parts.push(s.length);
    for (let i = 0; i < s.length; i++) parts.push(s.charCodeAt(i));
  };
  u32(TSSG_MAGIC);
  u16(TSSG_VERSION);
  u16(0);
  u64(tick);
  u32(programs.length);
  u32(0);
  for (const p of programs) {
    str(p.id);
    str(p.junction);
    u64(BigInt(p.offsetTicks));
    u16(p.phases.length);
    u16(p.links.length);
    for (const ph of p.phases) {
      u32(ph.durationTicks);
      str(ph.state);
    }
    for (const l of p.links) {
      u16(l.linkIdx);
      str(l.laneId);
    }
  }
  return new Uint8Array(parts);
}

const FIXTURE: SigProgram[] = [
  {
    id: "sigA",
    junction: "J1",
    offsetTicks: 0,
    phases: [
      { durationTicks: 10, state: "Gr" },
      { durationTicks: 5, state: "yr" },
    ],
    links: [
      { linkIdx: 0, laneId: "iJ1_0" },
      { linkIdx: 1, laneId: "iJ1_1" },
    ],
  },
  {
    id: "sigB",
    junction: "J2",
    offsetTicks: 5,
    phases: [
      { durationTicks: 20, state: "r" },
      { durationTicks: 10, state: "G" },
    ],
    links: [{ linkIdx: 0, laneId: "iJ2_0" }],
  },
];

test("decodes the Go encoder's tick-0 frame exactly", () => {
  const f = decodeSignalFrame(fromHex(GO_SIG_TICK0));
  assert.equal(f.tick, 0);
  assert.equal(f.programs.length, 2);
  assert.deepEqual(f.programs, FIXTURE);
});

test("the independent writer round-trips", () => {
  const f = decodeSignalFrame(buildSignalFrame(123n, FIXTURE));
  assert.equal(f.tick, 123);
  assert.deepEqual(f.programs, FIXTURE);
});

test("phaseIndexAt: boundaries, wrap, and SUMO offset semantics", () => {
  const [a, b] = FIXTURE as [SigProgram, SigProgram];
  // sigA: 10/5, cycle 15, offset 0 — half-open windows.
  for (const [tick, want] of [[0, 0], [9, 0], [10, 1], [14, 1], [15, 0], [24, 0], [25, 1]] as const) {
    assert.equal(phaseIndexAt(a, tick), want, `sigA tick ${tick}`);
  }
  // sigB: 20/10, cycle 30, offset 5 — phase 0 begins at tick 5; before
  // that the cycle wraps (ticks 0–4 run the previous cycle's tail).
  for (const [tick, want] of [[0, 1], [4, 1], [5, 0], [24, 0], [25, 1], [34, 1], [35, 0]] as const) {
    assert.equal(phaseIndexAt(b, tick), want, `sigB tick ${tick}`);
  }
});

test("stateCharAt: per-link chars and the uncontrolled fallback", () => {
  const [a] = FIXTURE as [SigProgram, SigProgram];
  assert.equal(stateCharAt(a, 0, 0), "G");
  assert.equal(stateCharAt(a, 0, 1), "r");
  assert.equal(stateCharAt(a, 10, 0), "y");
  assert.equal(stateCharAt(a, 0, 2), ""); // link without a state char
  assert.equal(stateCharAt(a, 0, -1), "");
  const noPhases: SigProgram = { id: "x", junction: "x", offsetTicks: 0, phases: [], links: [] };
  assert.equal(stateCharAt(noPhases, 0, 0), "");
  assert.equal(phaseIndexAt(noPhases, 7), 0); // total function, cycle 0
});

test("sigColorOf mirrors the kernel's mapSigChar (ADR-0011 §2)", () => {
  assert.equal(sigColorOf("g"), "green");
  assert.equal(sigColorOf("G"), "green");
  assert.equal(sigColorOf("y"), "amber");
  assert.equal(sigColorOf("r"), "red");
  for (const c of ["o", "O", "u", "s", "", "1"]) assert.equal(sigColorOf(c), "off", `char ${c}`);
});

test("decodes an empty table (run without signals)", () => {
  const f = decodeSignalFrame(buildSignalFrame(42n, []));
  assert.equal(f.tick, 42);
  assert.deepEqual(f.programs, []);
});

test("decodes u64 ticks/offsets beyond 2^32", () => {
  const p: SigProgram = {
    id: "big",
    junction: "big",
    offsetTicks: Number(2n ** 33n + 5n),
    phases: [{ durationTicks: 3, state: "g" }],
    links: [],
  };
  const f = decodeSignalFrame(buildSignalFrame(2n ** 33n + 7n, [p]));
  assert.equal(f.tick, 8589934599);
  assert.equal(f.programs[0]?.offsetTicks, 8589934597);
});

test("rejects short, bad-magic, bad-version, truncated, trailing, zero-duration", () => {
  const good = buildSignalFrame(0n, FIXTURE);
  assert.throws(() => decodeSignalFrame(good.slice(0, 10)), /at least 24/);
  const badMagic = good.slice();
  badMagic[0] = 0x99;
  assert.throws(() => decodeSignalFrame(badMagic), /bad magic/);
  const badVersion = good.slice();
  new DataView(badVersion.buffer).setUint16(4, 2, true);
  assert.throws(() => decodeSignalFrame(badVersion), /schema_version 2/);
  assert.throws(() => decodeSignalFrame(good.slice(0, good.length - 2)), /short read/);
  const withExtra = new Uint8Array(good.length + 1);
  withExtra.set(good);
  assert.throws(() => decodeSignalFrame(withExtra), /trailing bytes/);
  const zeroDur: SigProgram = {
    id: "z",
    junction: "z",
    offsetTicks: 0,
    phases: [{ durationTicks: 0, state: "g" }],
    links: [],
  };
  assert.throws(() => decodeSignalFrame(buildSignalFrame(0n, [zeroDur])), /zero duration/);
});

test("header size matches the Go layout", () => {
  assert.equal(TSSG_HEADER_BYTES, 24);
});
