// screenshot.mjs — browser smoke/proof harness, no new dependencies: drives
// headless Chrome over the DevTools protocol (node ≥22 global WebSocket +
// fetch). Navigates to the viz, waits REAL time (virtual-time budgets race
// ahead of maplibre's workers and the wall-clock-paced snapshot stream),
// captures a screenshot, and prints the HUD status text as the in-browser
// assertion (tick > 0, vehicles > 0 when a run is live).
//
// Usage: node scripts/screenshot.mjs [url] [out.png] [waitMs]
// Requires: chrome/chromium on PATH (env CHROME overrides), the viz dev
// server and (for live data) engine serve running.

import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";

const url = process.argv[2] ?? "http://localhost:5517/?run=demo&ws=ws://127.0.0.1:8443";
const out = process.argv[3] ?? "viz-shot.png";
const waitMs = Number(process.argv[4] ?? 12000);
const port = 9333;
const CHROME = process.env.CHROME ?? "google-chrome";

const chrome = spawn(
  CHROME,
  [
    "--headless",
    "--no-sandbox",
    "--disable-dev-shm-usage",
    "--use-angle=swiftshader",
    "--enable-unsafe-swiftshader",
    `--remote-debugging-port=${port}`,
    "--window-size=1600,1000",
    "about:blank",
  ],
  { stdio: ["ignore", "ignore", "pipe"] },
);
let chromeErr = "";
chrome.stderr.on("data", (d) => (chromeErr += d));

try {
  let version = null;
  for (let i = 0; i < 60 && !version; i++) {
    try {
      version = await (await fetch(`http://127.0.0.1:${port}/json/version`)).json();
    } catch {
      await sleep(250);
    }
  }
  if (!version) throw new Error("devtools endpoint never came up\n" + chromeErr.slice(-2000));
  const target = await (
    await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" })
  ).json();

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = rej;
  });
  let seq = 0;
  const pending = new Map();
  const events = [];
  ws.onmessage = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
    } else if (msg.method) {
      events.push(msg);
    }
  };
  const send = (method, params = {}) =>
    new Promise((res, rej) => {
      const id = ++seq;
      pending.set(id, (m) => (m.error ? rej(new Error(`${method}: ${m.error.message}`)) : res(m)));
      ws.send(JSON.stringify({ id, method, params }));
    });

  await send("Page.enable");
  await send("Runtime.enable");
  await send("Page.navigate", { url });
  await sleep(waitMs);

  const status = await send("Runtime.evaluate", {
    expression: "document.getElementById('status')?.textContent ?? '(no status element)'",
    returnByValue: true,
  });
  console.log("[status]", status.result?.result?.value);
  const shot = await send("Page.captureScreenshot", { format: "png" });
  writeFileSync(out, Buffer.from(shot.result.data, "base64"));
  console.log("[shot] written to", out);

  for (const e of events) {
    if (e.method === "Runtime.consoleAPICalled") {
      console.log(
        "[console]",
        e.params.type,
        e.params.args.map((a) => a.value ?? a.description ?? "").join(" "),
      );
    }
    if (e.method === "Runtime.exceptionThrown") {
      console.log("[exception]", JSON.stringify(e.params.exceptionDetails).slice(0, 600));
    }
  }
  ws.close();
} finally {
  chrome.kill("SIGKILL");
}
