// replaypanel.test.ts — the DOM-free half of the replay panel (the repo
// convention: pure/control logic is tested, the DOM shell is not — see
// modelpanel.test.ts): status parsing, the view model, and the
// ReplayControl state transitions (probe → hide, toggle → pause/resume,
// 409 → seek-back hint, seek round-trip busy flag) against a stubbed
// fetch.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  FailGate,
  REPLAY_MISMATCH_HINT,
  ReplayControl,
  SEEK_BACK_HINT,
  parseStatus,
  probeWithRetry,
  toggleTarget,
  viewModel,
  type FetchLike,
  type ReplayStatus,
} from "../src/replaypanel.ts";
import { SeekGate, SnapshotBuffer } from "../src/snapshots.ts";
import type { SnapshotFrame } from "../src/tssf.ts";

function status(over: Partial<ReplayStatus> = {}): ReplayStatus {
  return {
    run: "baseline",
    replayRun: "baseline-replay",
    tick: 100,
    ticks: 36000,
    endTick: 36000,
    speed: 4,
    paused: false,
    done: false,
    dt: 0.1,
    crcErrors: 0,
    verbErrors: 0,
    ...over,
  };
}

interface Call {
  url: string;
  method: string;
  body?: string;
}

// stubFetch records calls and answers from handler; handler may throw to
// simulate an unreachable backend.
function stubFetch(handler: (url: string, body?: string) => { status: number; json?: unknown }): {
  fn: FetchLike;
  calls: Call[];
} {
  const calls: Call[] = [];
  const fn: FetchLike = (input, init) => {
    calls.push({ url: input, method: init?.method ?? "GET", ...(init?.body !== undefined ? { body: init.body } : {}) });
    const r = handler(input, init?.body);
    return Promise.resolve({
      ok: r.status >= 200 && r.status < 300,
      status: r.status,
      json: () => Promise.resolve(r.json),
    });
  };
  return { fn, calls };
}

test("parseStatus: validates the demosrv payload, tolerates optional fields", () => {
  const s = parseStatus({ tick: 5, endTick: 100, speed: 2, paused: true, done: false, crcErrors: 1 });
  assert.ok(s);
  assert.equal(s.tick, 5);
  assert.equal(s.run, ""); // missing optional string → default
  assert.equal(s.crcErrors, 1);
  assert.equal(parseStatus(null), null);
  assert.equal(parseStatus({ nope: 1 }), null);
  assert.equal(parseStatus({ tick: "5", endTick: 100, speed: 2, paused: true, done: false, crcErrors: 0 }), null);
});

test("toggleTarget: playing pauses; paused or done resumes", () => {
  assert.equal(toggleTarget(status({ paused: false, done: false })), "pause");
  assert.equal(toggleTarget(status({ paused: true })), "resume");
  assert.equal(toggleTarget(status({ done: true })), "resume");
});

test("viewModel: status line, toggle label, ended state, divergence count", () => {
  const playing = viewModel(status({ tick: 1200, endTick: 36000, speed: 4 }));
  assert.equal(playing.statusLine, "tick 1200 / 36000 · 4×");
  assert.equal(playing.toggleLabel, "⏸ pause");
  assert.equal(playing.toggleEndpoint, "pause");
  assert.equal(playing.ended, false);
  assert.equal(playing.divergence, 0);
  assert.equal(playing.verbErrors, 0);

  const paused = viewModel(status({ paused: true }));
  assert.equal(paused.statusLine, "tick 100 / 36000 · paused");
  assert.equal(paused.toggleLabel, "⏵ resume");
  assert.equal(paused.toggleEndpoint, "resume");

  const ended = viewModel(status({ done: true, crcErrors: 3, verbErrors: 2 }));
  assert.equal(ended.statusLine, "tick 100 / 36000 · replay ended");
  assert.equal(ended.ended, true);
  assert.equal(ended.divergence, 3);
  assert.equal(ended.verbErrors, 2);
  assert.equal(ended.warnLine, "⚠ divergence 3 · verb rejects 2");

  assert.equal(playing.warnLine, null); // both counters 0 → hidden
  assert.equal(viewModel(status({ verbErrors: 1 })).warnLine, "⚠ verb rejects 1");
});

test("refresh: 404 or unreachable → null (the panel hides); 200 stores the status", async () => {
  const missing = new ReplayControl(stubFetch(() => ({ status: 404 })).fn, "baseline-replay");
  assert.equal(await missing.refresh(), null);
  assert.equal(missing.status, null);

  const down = new ReplayControl(
    stubFetch(() => {
      throw new Error("connection refused");
    }).fn, "baseline-replay",
  );
  assert.equal(await down.refresh(), null);

  const up = new ReplayControl(stubFetch(() => ({ status: 200, json: status({ tick: 42 }) })).fn, "baseline-replay");
  const s = await up.refresh();
  assert.equal(s?.tick, 42);
  assert.equal(up.status?.speed, 4);

  const malformed = new ReplayControl(stubFetch(() => ({ status: 200, json: { nope: 1 } })).fn, "baseline-replay");
  assert.equal(await malformed.refresh(), null);
});

test("toggle: posts pause when playing, resume when paused; applies the returned status", async () => {
  const { fn, calls } = stubFetch((url) =>
    url.includes("/pause") ? { status: 200, json: status({ paused: true }) } : { status: 200, json: status({ paused: false }) },
  );
  const c = new ReplayControl(fn, "baseline-replay");
  c.status = status({ paused: false });
  await c.toggle();
  assert.equal(calls[0]!.url, "/api/replay/ctl/pause?run=baseline-replay");
  assert.equal(calls[0]!.method, "POST");
  assert.equal(c.status?.paused, true);
  await c.toggle();
  assert.equal(calls[1]!.url, "/api/replay/ctl/resume?run=baseline-replay");
  assert.equal(c.status?.paused, false);
  assert.equal(c.hint, "");
});

test("toggle: 409 while done sets the seek-back hint and keeps the old status", async () => {
  const { fn, calls } = stubFetch(() => ({ status: 409 }));
  const c = new ReplayControl(fn, "baseline-replay");
  c.status = status({ done: true });
  await c.toggle();
  assert.equal(calls[0]!.url, "/api/replay/ctl/resume?run=baseline-replay");
  assert.equal(c.hint, SEEK_BACK_HINT);
  assert.equal(c.status.done, true); // unchanged — no status JSON on a 409
  // A successful ctl afterwards clears the hint.
  const ok = new ReplayControl(stubFetch(() => ({ status: 200, json: status({ tick: 7 }) })).fn, "baseline-replay");
  ok.status = status({ done: true });
  ok.hint = SEEK_BACK_HINT;
  await ok.seek(7);
  assert.equal(ok.hint, "");
  assert.equal(ok.status.tick, 7);
});

test("setSpeed posts the speed body; seek posts the tick body (both carry ?run=)", async () => {
  const { fn, calls } = stubFetch(() => ({ status: 200, json: status() }));
  const c = new ReplayControl(fn, "baseline-replay");
  c.status = status(); // the panel only exists after a first status
  await c.setSpeed(8);
  assert.equal(calls[0]!.url, "/api/replay/ctl/speed?run=baseline-replay");
  assert.equal(calls[0]!.body, JSON.stringify({ speed: 8 }));
  await c.seek(500);
  assert.equal(calls[1]!.url, "/api/replay/ctl/seek?run=baseline-replay");
  assert.equal(calls[1]!.body, JSON.stringify({ tick: 500 }));
});

test("a 409 run mismatch (stale tab) surfaces the mismatch hint, not seek-back", async () => {
  // Resume while NOT done: the player's only 409 is done-resume, so this
  // one is demosrv's run-mismatch — "another replay is active".
  const { fn, calls } = stubFetch(() => ({ status: 409 }));
  const c = new ReplayControl(fn, "baseline-replay");
  c.status = status({ paused: true, done: false });
  await c.toggle();
  assert.equal(calls[0]!.url, "/api/replay/ctl/resume?run=baseline-replay");
  assert.equal(c.hint, REPLAY_MISMATCH_HINT);
  assert.equal(c.status.paused, true); // unchanged — a ctl failure

  // Seek/pause/speed never 409 for done — a 409 there is always a mismatch.
  const c2 = new ReplayControl(fn, "baseline-replay");
  c2.status = status();
  assert.equal(await c2.seek(5), false);
  assert.equal(c2.hint, REPLAY_MISMATCH_HINT);
});

test("seek: busy during the round-trip (slider disables), reentry ignored", async () => {
  let release: () => void = () => {};
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const calls: string[] = [];
  const fn: FetchLike = (input) => {
    calls.push(input);
    return gate.then(() => ({ ok: true, status: 200, json: () => Promise.resolve(status({ tick: 500 })) }));
  };
  const c = new ReplayControl(fn, "baseline-replay");
  c.status = status();
  const p = c.seek(500);
  assert.equal(c.seekBusy, true); // the panel disables the slider now
  await c.seek(600); // reentry mid-flight — must not POST
  assert.deepEqual(calls, ["/api/replay/ctl/seek?run=baseline-replay"]);
  release();
  await p;
  assert.equal(c.seekBusy, false);
  assert.equal(c.status?.tick, 500);
});

test("ctl failures surface as hints, never throw into the panel", async () => {
  const server500 = new ReplayControl(stubFetch(() => ({ status: 500 })).fn, "baseline-replay");
  server500.status = status();
  await server500.toggle();
  assert.match(server500.hint, /pause failed \(500\)/);

  const offline = new ReplayControl(
    stubFetch(() => {
      throw new Error("down");
    }).fn, "baseline-replay",
  );
  offline.status = status();
  await offline.setSpeed(2);
  assert.equal(offline.hint, "control plane unreachable");
});

test("probeWithRetry: retries past startup hiccups, gives up only after the last attempt", async () => {
  // Fails twice, then the control plane answers — the panel must show.
  let calls = 0;
  const flaky = (): Promise<ReplayStatus | null> => {
    calls++;
    return Promise.resolve(calls < 3 ? null : status({ tick: 42 }));
  };
  const s = await probeWithRetry(flaky, 5, 1);
  assert.equal(s?.tick, 42);
  assert.equal(calls, 3);

  // A plain live demo 404s every attempt — null after exactly 5 tries
  // (the panel hides, a few seconds later than before).
  let fails = 0;
  const dead = (): Promise<ReplayStatus | null> => {
    fails++;
    return Promise.resolve(null);
  };
  assert.equal(await probeWithRetry(dead, 5, 1), null);
  assert.equal(fails, 5);

  // An immediate success wastes no retries.
  let once = 0;
  const first = (): Promise<ReplayStatus | null> => {
    once++;
    return Promise.resolve(status());
  };
  await probeWithRetry(first, 5, 1);
  assert.equal(once, 1);
});

test("seek returns whether it LANDED (false on 409/failure)", async () => {
  const conflict = new ReplayControl(stubFetch(() => ({ status: 409 })).fn, "baseline-replay");
  conflict.status = status({ done: true });
  assert.equal(await conflict.seek(5), false);
  assert.equal(conflict.hint, REPLAY_MISMATCH_HINT); // seek 409s only on mismatch

  const ok = new ReplayControl(stubFetch(() => ({ status: 200, json: status({ tick: 5 }) })).fn, "baseline-replay");
  ok.status = status();
  assert.equal(await ok.seek(5), true);
});

test("panel-driven seek resets BEFORE the POST — a landing frame arriving pre-ack survives", async () => {
  // Mirrors main.ts wiring: ReplayPanel.onSeek fires onSeeking
  // (resetForSeek) BEFORE ctl.seek's POST. The player publishes the
  // landing frame before acking, so a post-ack reset could wipe it (a
  // paused seek would show stale pre-seek vehicles until the ~1 Hz
  // republication) — this ordering is the fix. +99 ticks is UNDER
  // SeekGate's maxJump (240): the gate alone would lerp across the jump.
  const b = new SnapshotBuffer(250);
  const g = new SeekGate();
  let gateResets = 0;
  const events: string[] = [];
  const pushTick = (tick: number, recvMs: number): void => {
    const f: SnapshotFrame = { tick, vehicles: [{ id: 1, x: tick, y: 0, angle: 0, cls: 0 }] };
    if (g.observe(f.tick)) {
      b.reset();
      gateResets++;
    }
    b.push(f, recvMs);
  };
  pushTick(1000, 1000);
  pushTick(1001, 1100);
  // The stub fetch delivers the landing frame DURING the round-trip,
  // before the ack resolves — exactly what the player does.
  let ack: () => void = () => {};
  const fn: FetchLike = () => {
    events.push("post");
    pushTick(1100, 1150); // the landing frame arrives before the HTTP ack
    return new Promise((resolve) => {
      ack = () => resolve({ ok: true, status: 200, json: () => Promise.resolve(status({ tick: 1100 })) });
    });
  };
  const c = new ReplayControl(fn, "baseline-replay");
  c.status = status();
  // --- the panel's onSeek sequence: reset, then POST ---
  events.push("reset");
  b.reset(); // main.ts's onSeeking (resetForSeek)
  const p = c.seek(1100); // +99 — under the gate's maxJump
  assert.deepEqual(events, ["reset", "post"]); // the reset PRECEDES the POST
  ack();
  assert.equal(await p, true);
  // The pre-ack landing frame survived in the fresh buffer and renders
  // as-is — never lerped toward tick 1001, no bogus cross-jump speed.
  assert.equal(gateResets, 0); // the gate alone would have missed this seek
  const s = b.sample(1500)!;
  assert.equal(s.tick, 1100);
  assert.equal(s.vehicles[0]!.x, 1100);
  assert.equal(s.vehicles[0]!.speed, 0);
  // The gate stays armed as backstop: a backward seek still resets.
  pushTick(500, 1300);
  assert.equal(gateResets, 1);
});

test("FailGate: fail, fail, success keeps the panel; 3 consecutive failures hide", () => {
  const gate = new FailGate(3);
  assert.equal(gate.fail(), false); // 1st transient
  assert.equal(gate.fail(), false); // 2nd
  gate.ok(); // a success resets the streak — the panel stays
  assert.equal(gate.fail(), false);
  assert.equal(gate.fail(), false);
  assert.equal(gate.fail(), true); // 3rd consecutive — the child is gone
  gate.ok();
  assert.equal(gate.fail(), false); // re-armed after recovery
});

test("refresh: EVERY probe binds the page's run (first included); matching run proceeds", async () => {
  const { fn, calls } = stubFetch(() => ({ status: 200, json: status() }));
  const c = new ReplayControl(fn, "baseline-replay");
  assert.equal((await c.refresh())?.replayRun, "baseline-replay");
  assert.equal(calls[0]!.url, "/api/replay/status?run=baseline-replay"); // bound from the start
  await c.refresh();
  assert.equal(calls[1]!.url, "/api/replay/status?run=baseline-replay");
  assert.equal(c.status?.tick, 100); // the matching run's status is adopted
});

test("refresh: a 409 on the FIRST probe hides — the active replay is not this page's", async () => {
  // A stale deep link for replay A while replay B runs: no adoption, no
  // control — the mismatch hint and a null (init's retries exhaust → the
  // panel hides).
  const c = new ReplayControl(stubFetch(() => ({ status: 409 })).fn, "a-replay");
  assert.equal(await c.refresh(), null);
  assert.equal(c.hint, REPLAY_MISMATCH_HINT);
  assert.equal(c.status, null); // nothing adopted
});

test("refresh: a 409 AFTER adoption does NOT adopt the new identity", async () => {
  let first = true;
  const { fn } = stubFetch(() => {
    if (first) {
      first = false;
      return { status: 200, json: status({ tick: 42 }) };
    }
    return { status: 409 }; // another replay took over
  });
  const c = new ReplayControl(fn, "baseline-replay");
  await c.refresh(); // adopts baseline-replay
  assert.equal(await c.refresh(), null); // 409 → failure (the FailGate counts it)
  assert.equal(c.hint, REPLAY_MISMATCH_HINT);
  assert.equal(c.status?.replayRun, "baseline-replay"); // old identity kept
  assert.equal(c.status?.tick, 42);
});
