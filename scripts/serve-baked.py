#!/usr/bin/env python3
"""Serve a baked replay (ADR-0023) and the viz from one local origin.

    scripts/serve-baked.py --baked data/baked --viz viz/dist --port 8790
    # then open http://127.0.0.1:8790/app.html?bake=http://127.0.0.1:8790/baked/<run>/<hash>/index.json

The baked artifacts are built for static hosting on Cloudflare Pages, which
sets two things this replaces locally:

  * `Content-Encoding: br` on the pre-compressed .tsrb.br/.tsrl.br chunks.
    The viz does NOT decompress them itself — it relies on the browser doing
    it transparently, and browsers only do that when the header is present
    (DecompressionStream has no 'br'). Served without it, every chunk
    arrives as brotli bytes the decoder rejects, and the replay comes up
    with a network and no vehicles. `python3 -m http.server` does exactly
    this, which is a bad thing to discover during a recording.
  * one origin for the app and the data, so no CORS preflight.

This is a presenter's tool, not a deployment: single-threaded is fine for
one browser, and it binds loopback only.

With --timing-runner the same port also answers POST /api/timing/run — the
play page's whole backend. The body is a signal-game dial document
(mktimingvariant.py's shape, name included); it is scored at seed 42 and
its run baked for 3D replay in one call via scripts/show/score-timing.py
--bake, which takes ~20 s (cached) to several minutes (-pace 10 fallback
on a loaded box). Runs are serialized — a concurrent POST gets 409 — and
the rewritten leaderboard is copied into the viz root so the served board
is fresh.
"""
import argparse
import functools
import http.server
import json
import os
import posixpath
import re
import shutil
import subprocess
import sys
import threading

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.realpath(os.path.join(HERE, ".."))
sys.path.insert(0, os.path.join(HERE, "show"))
import mktimingvariant  # NAME_RE — one source for the dial name rule

MAX_BODY = 64 * 1024  # a dial is a few hundred bytes; 64 KiB is generous
RUN_TIMEOUT = 900  # seconds; the -pace 10 fallback is ~6 min on a loaded box
RUN_LOCK = threading.Lock()  # a run holds its serve ports + store: serialize


class Handler(http.server.SimpleHTTPRequestHandler):
    # roots is [(url_prefix, fs_dir)], longest prefix first.
    roots = ()
    # None = pure static (today's behavior); --timing-runner sets it to
    # {"viz": served viz root} and turns POST /api/timing/run on.
    timing = None

    def translate_path(self, path):
        p = posixpath.normpath(path.split("?", 1)[0].split("#", 1)[0])
        for prefix, root in self.roots:
            if p == prefix or p.startswith(prefix.rstrip("/") + "/"):
                rel = p[len(prefix.rstrip("/")):].lstrip("/")
                # normpath above already collapsed .. segments, but a path
                # that escapes the root would be a directory traversal, so
                # verify containment rather than assuming.
                full = os.path.realpath(os.path.join(root, rel))
                if full == root or full.startswith(root + os.sep):
                    return full
                return os.path.join(root, "__forbidden__")
        return super().translate_path(path)

    def end_headers(self):
        if self.path.split("?", 1)[0].endswith(".br"):
            # The chunk objects are stored brotli-precompressed and always
            # fetched whole (ADR-0023 §"Content-Encoding"), so this is the
            # representation header, not a transfer encoding applied here.
            self.send_header("Content-Encoding", "br")
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Cache-Control", "no-store")
        super().end_headers()

    def guess_type(self, path):
        if path.endswith(".br"):
            return "application/octet-stream"
        if path.endswith((".tssg", ".tsrb", ".tsrl")):
            return "application/octet-stream"
        return super().guess_type(path)

    def log_message(self, fmt, *a):
        if "--verbose" in sys.argv:
            super().log_message(fmt, *a)

    def do_GET(self):
        # Health probe for the timing runner: the play page and the hero
        # game drawer GET this to tell a live local server (200) from the
        # static public bundle (404/405), and switch to view-only mode.
        if self.timing is not None and \
                self.path.split("?", 1)[0] == "/api/timing/run":
            self._reply(200, {"ok": True, "service": "timing-runner"})
            return
        super().do_GET()

    def do_POST(self):
        if self.timing is None or \
                self.path.split("?", 1)[0] != "/api/timing/run":
            # what BaseHTTPRequestHandler answers when do_POST is absent
            self.send_error(501, "Unsupported method ('POST')")
            return
        try:
            n = int(self.headers.get("Content-Length") or "")
        except ValueError:
            n = -1
        if n < 0:
            self._reply(400, {"ok": False,
                              "error": "a Content-Length is required"})
            return
        if n > MAX_BODY:
            self._reply(413, {"ok": False,
                              "error": f"body too large ({MAX_BODY} bytes max)"})
            return
        code, obj = timing_run(self.rfile.read(n), self.timing["viz"])
        self._reply(code, obj)

    def _reply(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def timing_run(body, viz):
    """One POST /api/timing/run -> (http code, response object).

    Dial JSON in, scored leaderboard row + baked replay out. Never raises
    into the handler: the play page gets one JSON object, no tracebacks.
    """
    if not RUN_LOCK.acquire(blocking=False):
        return 409, {"ok": False, "error": "another run is in progress"}
    try:
        try:
            dial = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError):
            return 400, {"ok": False, "error": "body is not valid JSON"}
        name = dial.get("name") if isinstance(dial, dict) else None
        if not isinstance(name, str) \
                or not mktimingvariant.NAME_RE.fullmatch(name):
            return 400, {"ok": False, "error": "dial needs a slug-safe "
                         "\"name\" (lowercase [a-z0-9-], e.g. \"guest-r1\")"}
        # score-timing keys its cache on this same slug transform; the name
        # already passed NAME_RE, so slug == name here.
        slug = re.sub(r"[^a-z0-9-]+", "-", name.lower()).strip("-")
        if slug in mktimingvariant.RESERVED_NAMES:
            return 400, {"ok": False, "error": f"\"{slug}\" is a published "
                         "baseline — pick a fresh design name"}
        incoming = os.path.join(REPO, "data/runs/timing-game/incoming")
        os.makedirs(incoming, exist_ok=True)
        dial_path = os.path.join(incoming, f"{slug}-{os.getpid()}.dial.json")
        with open(dial_path, "wb") as f:
            f.write(body)
        try:
            proc = subprocess.run(
                [sys.executable, "scripts/show/score-timing.py",
                 "--dial", dial_path, "--name", name, "--bake"],
                cwd=REPO, capture_output=True, text=True, timeout=RUN_TIMEOUT)
        except subprocess.TimeoutExpired as e:
            log = "".join(s for s in (e.stdout, e.stderr)
                          if isinstance(s, str))
            return 500, {"ok": False,
                         "error": f"scoring timed out after {RUN_TIMEOUT} s",
                         "log_tail": log[-1500:]}
        log = proc.stdout + proc.stderr
        if proc.returncode != 0:
            return 500, {"ok": False,
                         "error": f"scoring failed (exit {proc.returncode})",
                         "log_tail": log[-1500:]}
        try:
            with open(os.path.join(REPO, "data/runs/timing-game",
                                   f"{slug}.score.json")) as f:
                row = json.load(f)
            with open(os.path.join(REPO, "viz/public",
                                   "timing-game-results.json")) as f:
                board = json.load(f)
            # the served board lives under the viz root; refresh it from the
            # one score-timing just rewrote
            shutil.copyfile(os.path.join(REPO, "viz/public",
                                         "timing-game-results.json"),
                            os.path.join(viz, "timing-game-results.json"))
        except (OSError, json.JSONDecodeError) as e:
            return 500, {"ok": False,
                         "error": f"scored, but the result could not be "
                                  f"published: {e}",
                         "log_tail": log[-1500:]}
        rows = board.get("rows") if isinstance(board, dict) else None
        rows = rows if isinstance(rows, list) else []
        rank = next((i for i, r in enumerate(rows, 1)
                     if re.sub(r"[^a-z0-9-]+", "-",
                               str(r.get("name", "")).lower()).strip("-")
                     == slug), None)
        shown = rows[rank - 1] if rank else {}
        bake = row.get("bake") if isinstance(row.get("bake"), dict) else None
        # ADR-0023: pin the exact bake hash — a bare run name resolves to
        # whatever hash the directory listing sorts first.
        pin = f"{slug}/{bake['hash']}" if bake and bake.get("hash") else slug
        out = {"ok": True, "name": row.get("name", name), "slug": slug,
               # the board's rounded numbers: what the leaderboard displays
               "mean_time_loss_s": shown.get("mean_time_loss_s",
                                             row.get("mean_time_loss_s")),
               "trips_completed": shown.get("trips_completed",
                                            row.get("trips_completed")),
               "rank": rank, "of": len(rows), "pace": row.get("pace"),
               "hero_url": f"/hero.html?run={pin}&view=junction",
               "log_tail": log[-500:]}
        if bake is not None:
            out["bake"] = bake
        return 200, out
    finally:
        RUN_LOCK.release()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--baked", default="data/baked",
                    help="directory of baked runs (mounted at /baked/)")
    ap.add_argument("--viz", default="viz/dist",
                    help="built viz to serve at / (cd viz && pnpm build)")
    ap.add_argument("--port", type=int, default=8790)
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--timing-runner", action="store_true",
                    help="also answer POST /api/timing/run — score a "
                         "signal-game dial at seed 42 and bake its 3D replay "
                         "(the play page's backend)")
    args = ap.parse_args()

    baked = os.path.realpath(args.baked)
    viz = os.path.realpath(args.viz)
    for label, d in (("--baked", baked), ("--viz", viz)):
        if not os.path.isdir(d):
            sys.exit(f"serve-baked: {label} {d} is not a directory")
    if not os.path.exists(os.path.join(viz, "app.html")):
        sys.exit(f"serve-baked: {viz}/app.html missing — build the viz "
                 f"first (cd viz && pnpm build)")

    Handler.roots = (("/baked/", baked), ("/", viz))
    if args.timing_runner:
        Handler.timing = {"viz": viz}
    httpd = http.server.ThreadingHTTPServer(("127.0.0.1", args.port), Handler)

    runs = []
    for run in sorted(os.listdir(baked)):
        rd = os.path.join(baked, run)
        if not os.path.isdir(rd):
            continue
        for h in sorted(os.listdir(rd)):
            if os.path.exists(os.path.join(rd, h, "index.json")):
                runs.append(f"/baked/{run}/{h}/index.json")
    print(f"serve-baked: http://127.0.0.1:{args.port}/  "
          f"(viz {viz}, baked {baked})")
    if args.timing_runner:
        print(f"serve-baked: timing runner ON — POST /api/timing/run, play "
              f"page at http://127.0.0.1:{args.port}/play.html")
    if not runs:
        print("serve-baked: WARNING — no <run>/<hash>/index.json under "
              f"{baked}; there is nothing to replay")
    for r in runs:
        # ?bake= must be ABSOLUTE — the shim resolves every chunk URL
        # against the manifest with new URL() (baked.ts), which throws
        # "Invalid base URL" on a root-relative base.
        print(f"  replay: http://127.0.0.1:{args.port}/app.html"
                f"?bake=http://127.0.0.1:{args.port}{r}")
    print("  quiz:   http://127.0.0.1:%d/quiz.html" % args.port)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
