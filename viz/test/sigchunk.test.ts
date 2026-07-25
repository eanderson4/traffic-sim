// sigchunk.test.ts — the chunked signal-table accumulator (ADR-0016 §4,
// viz/src/tssg.ts): v1 no-header frames install immediately; multi-chunk
// generations assemble in and out of order-of-arrival gaps; gaps,
// regressions, and count changes reset the partial set; a completed
// generation swaps in whole (the old table survives an incomplete new
// one); and the sig_chunk header parser accepts "i/n", tolerates absent,
// rejects malformed.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  parseSigChunkHeader,
  SignalTableAccumulator,
  type SignalTable,
  type SigProgram,
} from "../src/tssg.ts";

const prog = (id: string): SigProgram => ({
  id,
  junction: "J",
  offsetTicks: 0,
  phases: [{ durationTicks: 10, state: "Gr" }],
  links: [{ linkIdx: 0, laneId: "iJ_0" }],
});

const frame = (tick: number, ids: string[]): SignalTable => ({ tick, programs: ids.map(prog) });
const ids = (t: SignalTable): string[] => t.programs.map((p) => p.id);

test("no sig_chunk header: the frame is the whole table, installed immediately", () => {
  const acc = new SignalTableAccumulator();
  const res = acc.feed(frame(7, ["a", "b"]), null);
  assert.equal(res.gap, false);
  assert.deepEqual(ids(res.table!), ["a", "b"]);
  assert.equal(acc.partial, false);
});

test("multi-chunk generation assembles in order and completes whole", () => {
  const acc = new SignalTableAccumulator();
  let res = acc.feed(frame(7, ["a"]), { i: 1, n: 3 });
  assert.equal(res.table, null);
  assert.equal(acc.partial, true);
  res = acc.feed(frame(7, ["b"]), { i: 2, n: 3 });
  assert.equal(res.table, null);
  res = acc.feed(frame(7, ["c"]), { i: 3, n: 3 });
  assert.equal(res.gap, false);
  assert.deepEqual(ids(res.table!), ["a", "b", "c"]);
  assert.equal(res.table!.tick, 7); // the generation's tick (chunk 1's)
  assert.equal(acc.partial, false);
});

test("a gap resets the partial accumulation; the next round completes", () => {
  const acc = new SignalTableAccumulator();
  acc.feed(frame(7, ["a"]), { i: 1, n: 3 });
  const res = acc.feed(frame(7, ["c"]), { i: 3, n: 3 }); // chunk 2 lost
  assert.equal(res.gap, true);
  assert.equal(res.table, null);
  assert.equal(acc.partial, false);
  // The old round's straggler is a regression now: dropped, not merged.
  const stray = acc.feed(frame(7, ["b"]), { i: 2, n: 3 });
  assert.equal(stray.gap, true);
  assert.equal(stray.table, null);
  // A fresh round starts clean and completes.
  acc.feed(frame(8, ["x"]), { i: 1, n: 2 });
  const done = acc.feed(frame(8, ["y"]), { i: 2, n: 2 });
  assert.deepEqual(ids(done.table!), ["x", "y"]);
});

test("an index regression or count change abandons the in-flight round", () => {
  const acc = new SignalTableAccumulator();
  acc.feed(frame(7, ["a"]), { i: 1, n: 3 });
  acc.feed(frame(7, ["b"]), { i: 2, n: 3 });
  // The next round's chunk 1 arrives before this round finished: the
  // partial round is abandoned (gap) and the new one starts.
  const res = acc.feed(frame(8, ["p"]), { i: 1, n: 2 });
  assert.equal(res.gap, true);
  assert.equal(res.table, null);
  const done = acc.feed(frame(8, ["q"]), { i: 2, n: 2 });
  assert.deepEqual(ids(done.table!), ["p", "q"]);

  // Count change mid-generation: 3/9 after 2/10 — not our round.
  const acc2 = new SignalTableAccumulator();
  acc2.feed(frame(7, ["a"]), { i: 1, n: 10 });
  acc2.feed(frame(7, ["b"]), { i: 2, n: 10 });
  const res2 = acc2.feed(frame(7, ["c"]), { i: 3, n: 9 });
  assert.equal(res2.gap, true);
  assert.equal(res2.table, null);
});

test("the installed table survives an incomplete new generation", () => {
  const acc = new SignalTableAccumulator();
  const installed = acc.feed(frame(1, ["old"]), null).table!;
  // A new generation starts but never completes: nothing surfaces.
  acc.feed(frame(2, ["new-a"]), { i: 1, n: 2 });
  assert.equal(acc.partial, true);
  // ...and even a gap keeps the previously completed table with the
  // CALLER (the accumulator never hands back a half-swapped table).
  const res = acc.feed(frame(2, ["stray"]), { i: 2, n: 3 });
  assert.equal(res.gap, true);
  assert.equal(res.table, null);
  assert.deepEqual(ids(installed), ["old"]); // untouched
});

test("a 1-chunk set with an explicit header completes immediately", () => {
  const acc = new SignalTableAccumulator();
  const res = acc.feed(frame(5, ["only"]), { i: 1, n: 1 });
  assert.equal(res.gap, false);
  assert.deepEqual(ids(res.table!), ["only"]);
});

test("same-tick generations are distinguished by order, not tick", () => {
  // A paused replay republishes at one tick: chunk stream 1/2,2/2 then
  // 1/2,2/2 at the SAME tick must assemble twice, not merge.
  const acc = new SignalTableAccumulator();
  acc.feed(frame(9, ["a"]), { i: 1, n: 2 });
  const first = acc.feed(frame(9, ["b"]), { i: 2, n: 2 });
  assert.deepEqual(ids(first.table!), ["a", "b"]);
  acc.feed(frame(9, ["a"]), { i: 1, n: 2 });
  const second = acc.feed(frame(9, ["b"]), { i: 2, n: 2 });
  assert.deepEqual(ids(second.table!), ["a", "b"]);
});

test("sig_chunk header parse: present, absent, malformed", () => {
  assert.deepEqual(parseSigChunkHeader("2/3"), { i: 2, n: 3 });
  assert.deepEqual(parseSigChunkHeader("10/10"), { i: 10, n: 10 });
  assert.equal(parseSigChunkHeader(undefined), null); // absent = whole table
  for (const bad of ["1", "x/y", "0/1", "1/0", "3/2", "1/"]) {
    assert.throws(() => parseSigChunkHeader(bad), /bad sig_chunk/, JSON.stringify(bad));
  }
});

test("empty-string header value counts as absent (nats.ws MsgHdrs.get)", () => {
  assert.equal(parseSigChunkHeader(""), null);
  assert.equal(parseSigChunkHeader(undefined), null);
});
