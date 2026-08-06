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
"""
import argparse
import functools
import http.server
import os
import posixpath
import sys
import threading


class Handler(http.server.SimpleHTTPRequestHandler):
    # roots is [(url_prefix, fs_dir)], longest prefix first.
    roots = ()

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


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--baked", default="data/baked",
                    help="directory of baked runs (mounted at /baked/)")
    ap.add_argument("--viz", default="viz/dist",
                    help="built viz to serve at / (cd viz && pnpm build)")
    ap.add_argument("--port", type=int, default=8790)
    ap.add_argument("--verbose", action="store_true")
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
