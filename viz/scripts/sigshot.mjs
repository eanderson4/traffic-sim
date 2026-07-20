// sigshot.mjs — M9 BROWSER proof (the pixels): builds serve, runs i280
// live, drives the real viz in headless Chrome (DevTools protocol, same
// harness pattern as screenshot.mjs), and asserts the RENDERED signal
// feature-state flips in step with the programs: all six stop-line circles
// green in the opening window, amber across ticks 820–849, red across
// 850–899 — with a screenshot per phase as visual evidence.
//
// Usage: node scripts/sigshot.mjs [outPrefix]   (from viz/)
// Requires google-chrome (env CHROME overrides). ~2 min wall time.

import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { writeFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";

const engineDir = new URL("../../engine/", import.meta.url).pathname;
const vizDir = new URL("../", import.meta.url).pathname;
const netfile = new URL("../../data/networks/i280-woodside/i280.json", import.meta.url).pathname;
const outPrefix = process.argv[2] ?? "sigshot";
const WS_PORT = 38443;
const VITE_PORT = 5518;
const CDP_PORT = 9334;
const RUN = "sigshot";
const CHROME = process.env.CHROME ?? "google-chrome";

const failures = [];
function check(name, cond, detail = "") {
  const ok = !!cond;
  console.log(`  ${ok ? "ok" : "FAIL"}  ${name}${ok ? "" : ` — ${detail}`}`);
  if (!ok) failures.push(name);
}

// Global watchdog: never hang past the red window (~90 s of ticking) even
// if a child dies silently. killAll is hoisted.
const watchdog = setTimeout(() => {
  console.log("sigshot: GLOBAL TIMEOUT — a child or phase wait hung");
  void killAll().finally(() => process.exit(2));
}, 280000);

console.log("sigshot: building serve…");
const bin = join(mkdtempSync(join(tmpdir(), "ts-serve-")), "serve");
const build = spawn("go", ["build", "-o", bin, "./cmd/serve"], { cwd: engineDir, stdio: "inherit" });
await new Promise((res, rej) => build.on("exit", (c) => (c === 0 ? res() : rej(new Error(`go build exit ${c}`)))));

console.log("sigshot: starting serve + vite…");
// detached: each child leads its own process group so killAll can reap the
// whole tree (pnpm→sh→vite, chrome's zygotes) — a timeout of the parent
// must not leave port-squatting grandchildren behind.
const serve = spawn(bin, ["-netfile", netfile, "-run", RUN, "-ticks", "950", "-ws", `127.0.0.1:${WS_PORT}`], {
  stdio: ["ignore", "pipe", "inherit"],
  detached: true,
});
serve.stdout.on("data", (d) => process.stdout.write(`  [serve] ${d}`));
const vite = spawn("pnpm", ["exec", "vite", "--port", String(VITE_PORT), "--strictPort"], {
  cwd: vizDir,
  stdio: ["ignore", "pipe", "inherit"],
  detached: true,
});
vite.stdout.on("data", (d) => process.stdout.write(`  [vite] ${d}`));

const chromeProfile = mkdtempSync(join(tmpdir(), "ts-chrome-"));
const chrome = spawn(
  CHROME,
  [
    "--headless",
    "--no-sandbox",
    "--disable-dev-shm-usage",
    "--use-angle=swiftshader",
    "--enable-unsafe-swiftshader",
    `--remote-debugging-port=${CDP_PORT}`,
    `--user-data-dir=${chromeProfile}`,
    "--window-size=1600,1000",
    "about:blank",
  ],
  { stdio: ["ignore", "ignore", "pipe"], detached: true },
);
let chromeErr = "";
chrome.stderr.on("data", (d) => (chromeErr += d));
for (const [name, child] of Object.entries({ serve, vite, chrome })) {
  child.on("error", (e) => console.log(`  [${name}] spawn error: ${e.message}`));
}

async function killAll() {
  for (const child of [chrome, vite, serve]) {
    try {
      process.kill(-child.pid, "SIGKILL"); // the whole process group
    } catch {
      child.kill("SIGKILL");
    }
  }
  await sleep(300);
}

try {
  let version = null;
  for (let i = 0; i < 80 && !version; i++) {
    try {
      version = await (await fetch(`http://127.0.0.1:${CDP_PORT}/json/version`)).json();
    } catch {
      await sleep(250);
    }
  }
  if (!version) throw new Error("devtools endpoint never came up\n" + chromeErr.slice(-2000));
  const target = await (
    await fetch(`http://127.0.0.1:${CDP_PORT}/json/new?about:blank`, { method: "PUT" })
  ).json();

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = rej;
  });
  let seq = 0;
  const pending = new Map();
  const exceptions = [];
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
    } else if (msg.method === "Runtime.exceptionThrown") {
      exceptions.push(JSON.stringify(msg.params.exceptionDetails).slice(0, 400));
    }
  };
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const id = ++seq;
      pending.set(id, (m) => (m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m)));
      ws.send(JSON.stringify({ id, method, params }));
    });
  const evalJs = async (expression) => {
    const r = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    return r.result?.result?.value;
  };

  await send("Page.enable");
  await send("Runtime.enable");
  await send("Page.navigate", { url: `http://localhost:${VITE_PORT}/?run=${RUN}&ws=ws://127.0.0.1:${WS_PORT}` });

  // HUD tick + the map's OWN feature-state per stop-line lane (the ground
  // truth the paint expressions read). Single-char codes: g/a/r/- — CDP
  // returnByValue chokes on Map spreads and on maplibre method returns, so
  // every evaluate returns a plain string ("ok" terminators included).
  const FS_EXPR = `(() => {
    if (!window.__viz) return "no-viz";
    const ids = ["i5464972060_0_0","i5464972060_0_1","i5464972060_0_2","i5464972061_0_0","i5464972061_0_1","i5464972061_0_2"];
    const out = [];
    for (const id of ids) {
      const st = window.__viz.map.getFeatureState({ source: "signals", id });
      out.push(st && st.sig ? st.sig[0] : "-");
    }
    return out.join("");
  })()`;
  const tick = async () =>
    Number(((await evalJs("document.getElementById('status')?.textContent ?? ''") ?? "").match(/tick (\d+)/) ?? [])[1] ?? -1);
  const sigStates = async () => (await evalJs(FS_EXPR)) ?? "error";

  // Wait for the table + stop-lines to be live (feature-state applied),
  // then aim the camera.
  let states = "-";
  for (let i = 0; i < 120 && states.includes("-"); i++) {
    await sleep(500);
    states = await sigStates();
  }
  if (states.includes("-")) {
    // Diagnose before the phase waits burn their deadlines.
    console.log("  diag: status =", JSON.stringify(await evalJs("document.getElementById('status')?.textContent ?? null")));
    console.log("  diag: __viz =", await evalJs("typeof window.__viz"));
    console.log("  diag: sigDebug =", await evalJs("window.__viz ? JSON.stringify(window.__viz.sigDebug) : 'n/a'"));
  }
  check("viz applied signal feature-state (6 stop-line lanes)", /^[gar]{6}$/.test(states), `got ${states}`);
  const pts = JSON.parse((await evalJs("JSON.stringify(window.__viz.sigPoints)")) ?? "[]");
  if (pts.length > 0) {
    const cx = pts.reduce((a, p) => a + p[0], 0) / pts.length;
    const cy = pts.reduce((a, p) => a + p[1], 0) / pts.length;
    await evalJs(`window.__viz.map.jumpTo({center: [${cx}, ${cy}], zoom: 15.2}); "ok"`);
  }

  // Wait for a target phase (all six lanes share it) and screenshot it.
  async function awaitPhase(color, code, deadlineTick, shotFile) {
    let s = "-";
    let tk = -1;
    const deadline = Date.now() + 120000;
    while (Date.now() < deadline) {
      s = await sigStates();
      tk = await tick();
      if (s === code) break;
      if (tk > deadlineTick + 30) break; // missed the window — report
      await sleep(400);
    }
    const inWindow = s === code;
    console.log(`  info: phase ${color} observed at tick ${tk} (fs=${s})`);
    if (shotFile && inWindow) {
      const shot = await send("Page.captureScreenshot", { format: "png" });
      writeFileSync(shotFile, Buffer.from(shot.result.data, "base64"));
      console.log(`  [shot] ${shotFile}`);
    }
    return { inWindow, tk };
  }

  const green = await awaitPhase("green", "gggggg", 819, `${outPrefix}-green.png`);
  check("all 6 stop-lines render GREEN in the opening window", green.inWindow && green.tk <= 819, `tick ${green.tk}`);
  const amber = await awaitPhase("amber", "aaaaaa", 849, `${outPrefix}-amber.png`);
  check("all 6 stop-lines render AMBER within ticks 820–849", amber.inWindow && amber.tk >= 820 && amber.tk <= 849, `tick ${amber.tk}`);
  const red = await awaitPhase("red", "rrrrrr", 899, `${outPrefix}-red.png`);
  check("all 6 stop-lines render RED within ticks 850–899", red.inWindow && red.tk >= 850 && red.tk <= 899, `tick ${red.tk}`);

  check("no page exceptions", exceptions.length === 0, exceptions[0] ?? "");
  ws.close();
} finally {
  await killAll();
}

clearTimeout(watchdog);
if (failures.length > 0) {
  console.log(`sigshot: FAILED (${failures.length} checks)`);
  process.exit(1);
}
console.log("sigshot: PASS");
