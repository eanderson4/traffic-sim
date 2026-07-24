// sigsmoke.mjs — M9 live proof, no browser: build the engine's serve mode,
// run the i280 network live (1× pacing), and over the WebSocket bridge
// assert (a) the TSSG signal-program table arrives on ts.{run}.state.sig
// with the two I-280 junctions' programs (820/30/50 ticks, offset 0);
// (b) client-DERIVED junction states change in step with those programs —
// green until tick 819, amber at 820, red at 850, green again at the 900
// wrap; (c) a LATE JOINER (connects ~30 s in, missing the tick-0 table)
// converges via the keyframe-cadence republication and derives the same
// state at the same tick as the from-start client; (d) old-client
// tolerance: every TSSF snapshot decodes without error throughout.
//
// Usage: pnpm sigsmoke   (from viz/; expects the repo layout viz/ + engine/)
// Exit 0 = pass. Kills the serve child on the way out. ~2 min wall time.

import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

import { connect } from "nats.ws";
import { decodeFrame } from "../src/tssf.ts";
import { decodeSignalFrame } from "../src/tssg.ts";
import { laneStatesAtTick } from "../src/signals.ts";

const engineDir = new URL("../../engine/", import.meta.url).pathname;
const netfile = new URL("../../data/networks/i280-woodside/i280.json", import.meta.url).pathname;
const WS = "ws://127.0.0.1:28443";
const RUN = "sigsmoke";
const TICKS = 1000; // past the 900-tick cycle wrap
const LATE_JOIN_TICK = 300; // ~30 s in, past two cadence tables (0, 100, 200)

const failures = [];
function check(name, cond, detail = "") {
  const ok = !!cond;
  console.log(`  ${ok ? "ok" : "FAIL"}  ${name}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(name);
}

console.log("sigsmoke: building serve…");
const bin = join(mkdtempSync(join(tmpdir(), "ts-serve-")), "serve");
const build = spawn("go", ["build", "-o", bin, "./cmd/serve"], { cwd: engineDir, stdio: "inherit" });
await new Promise((res, rej) => build.on("exit", (c) => (c === 0 ? res() : rej(new Error(`go build exit ${c}`)))));

console.log(`sigsmoke: starting serve (run=${RUN}, ${TICKS} ticks @ 1×)…`);
const serve = spawn(bin, ["-netfile", netfile, "-run", RUN, "-ticks", String(TICKS), "-ws", "127.0.0.1:28443"], {
  stdio: ["ignore", "pipe", "inherit"],
});
serve.stdout.on("data", (d) => process.stdout.write(`  [serve] ${d}`));

// Junction state at a tick: all of a program's bound lanes (uniform on
// I-280) — returns e.g. { "5464972060": "green", "5464972061": "green" }.
function junctionStates(table, tick) {
  const perLane = laneStatesAtTick(table, tick);
  const out = {};
  for (const p of table.programs) {
    const states = p.links.map((l) => perLane.get(l.laneId));
    out[p.junction] = states.every((s) => s === states[0]) ? states[0] : `mixed:${states.join("/")}`;
  }
  return out;
}

let nc;
try {
  console.log("sigsmoke: waiting for the WebSocket listener…");
  let lastErr;
  for (let i = 0; i < 60 && !nc; i++) {
    try {
      nc = await connect({ servers: WS, timeout: 1000 });
    } catch (e) {
      lastErr = e;
      await sleep(500);
    }
  }
  if (!nc) throw lastErr ?? new Error("connect timeout");
  console.log("sigsmoke: early client connected; collecting (this takes ~95 s at 1×)…");

  let table = null;
  let tableCount = 0;
  const timeline = new Map(); // tick → junction states (from the early client's snapshots)
  let snapErrors = 0;
  let lastTick = 0;

  const sigSub = nc.subscribe(`ts.${RUN}.state.sig`);
  const sigReader = (async () => {
    for await (const msg of sigSub) {
      try {
        table = decodeSignalFrame(msg.data);
        tableCount++;
      } catch (e) {
        console.log(`  [sig] decode error: ${e}`);
      }
    }
  })();

  const snapSub = nc.subscribe(`ts.${RUN}.state.snap`);
  const snapReader = (async () => {
    for await (const msg of snapSub) {
      try {
        const f = decodeFrame(msg.data); // the OLD decoder, untouched (tolerance)
        lastTick = f.tick;
        if (table) timeline.set(f.tick, junctionStates(table, f.tick));
      } catch {
        snapErrors++;
      }
    }
  })();

  // Collect until the cycle wrap is comfortably observed (or run ends).
  const deadline = Date.now() + TICKS * 100 + 30000;

  // Late joiner: connect once the run is well underway (missed the tick-0
  // and first cadence tables), subscribe ONLY the sig subject, and time
  // the catch-up. The table's own publish tick is its derivation input.
  let late = null;
  const lateJoiner = (async () => {
    while (lastTick < LATE_JOIN_TICK && Date.now() < deadline) await sleep(500);
    if (lastTick < LATE_JOIN_TICK) return; // run died before the join point
    const t0 = Date.now();
    const nc2 = await connect({ servers: WS, name: "late-joiner" });
    const sub = nc2.subscribe(`ts.${RUN}.state.sig`);
    for await (const msg of sub) {
      const t = decodeSignalFrame(msg.data);
      late = { waitMs: Date.now() - t0, table: t, joinedAtTick: lastTick };
      sub.unsubscribe();
      break;
    }
    await nc2.close();
  })();

  while (lastTick < 910 && Date.now() < deadline) await sleep(500);
  snapSub.unsubscribe();
  sigSub.unsubscribe();
  await Promise.all([snapReader.catch(() => {}), sigReader.catch(() => {}), lateJoiner.catch(() => {})]);

  console.log(`  info: last tick ${lastTick}, ${timeline.size} sampled ticks, ${tableCount} tables, snap decode errors ${snapErrors}`);

  // (a) The table carries both I-280 programs, compiled onto the tick grid.
  check("signal table received", table !== null);
  if (table) {
    check("two programs (I-280 junctions)", table.programs.length === 2, `got ${table.programs.length}`);
    const ids = table.programs.map((p) => p.junction).sort();
    check("junctions 5464972060 + 5464972061", ids.join(",") === "5464972060,5464972061", ids.join(","));
    for (const p of table.programs) {
      const durs = p.phases.map((ph) => ph.durationTicks);
      const states = p.phases.map((ph) => ph.state);
      check(
        `${p.junction}: 820/30/50 ticks, GGG/yyy/rrr, offset 0, 3 links`,
        durs.join("/") === "820/30/50" && states.join("/") === "GGG/yyy/rrr" && p.offsetTicks === 0 && p.links.length === 3,
        `durs=${durs} states=${states} offset=${p.offsetTicks} links=${p.links.length}`,
      );
    }
  }

  // (b) Derived states change in step with the programs.
  const stateAt = (j, t) => timeline.get(t)?.[j];
  const firstTick = Math.min(...timeline.keys());
  const J = "5464972060";
  const J2 = "5464972061";
  let greenAll = true;
  for (let t = Math.max(firstTick, 100); t <= 819; t++) {
    if (timeline.has(t) && stateAt(J, t) !== "green") greenAll = false;
  }
  check(`both junctions green for every sampled tick ≤ 819`, greenAll);
  const firstAmber = [...timeline.keys()].sort((a, b) => a - b).find((t) => stateAt(J, t) === "amber");
  check("first amber at tick 820 (±2 drop slack)", firstAmber !== undefined && firstAmber >= 820 && firstAmber <= 822, `got ${firstAmber}`);
  const firstRed = [...timeline.keys()].sort((a, b) => a - b).find((t) => stateAt(J, t) === "red");
  check("first red at tick 850 (±2)", firstRed !== undefined && firstRed >= 850 && firstRed <= 852, `got ${firstRed}`);
  const firstRegreen = [...timeline.keys()].sort((a, b) => a - b).find((t) => t >= 851 && stateAt(J, t) === "green");
  check("green again at the 900 wrap (±2)", firstRegreen !== undefined && firstRegreen >= 900 && firstRegreen <= 902, `got ${firstRegreen}`);
  check(
    "junctions flip together (identical programs)",
    [...timeline.values()].every((s) => s[J] === s[J2]),
  );
  check("old-client tolerance: every TSSF snapshot decoded", snapErrors === 0, `${snapErrors} errors`);

  // (c) Late-join convergence.
  check("late joiner received a table", late !== null);
  if (late) {
    check(
      `catch-up within 6 s (cadence 20 ticks = 2 s; joined ~tick ${late.joinedAtTick})`,
      late.waitMs <= 6000,
      `waited ${late.waitMs} ms`,
    );
    const lateStates = junctionStates(late.table, late.table.tick);
    const earlyStates = timeline.get(late.table.tick);
    check(
      "late joiner derives the same states at the table tick",
      earlyStates !== undefined && JSON.stringify(lateStates) === JSON.stringify(earlyStates),
      `late=${JSON.stringify(lateStates)} early@${late.table.tick}=${JSON.stringify(earlyStates)}`,
    );
  }
} finally {
  if (nc) await nc.close().catch(() => {});
  serve.kill("SIGINT");
  await sleep(300);
  serve.kill("SIGKILL");
}

if (failures.length > 0) {
  console.log(`sigsmoke: FAILED (${failures.length} checks)`);
  process.exit(1);
}
console.log("sigsmoke: PASS");
