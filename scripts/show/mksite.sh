#!/usr/bin/env bash
# mksite.sh — assemble the deployable static-site bundle for the phantomjam
# Cloudflare Pages project: the built viz (splash landing at /, map app at
# /app.html, demos menu — dead without a demosrv backend but shipped anyway)
# plus the baked replays the site deep-links, and the Pages _headers that
# ship with the viz build (viz/public/_headers → dist/_headers).
#
# The bakes are NOT in git (data/ is gitignored content) — they are copied
# from a checkout's data/baked/<run>/<hash>/, defaulting to the main
# checkout ($TRAFFIC_SIM_BAKED overrides, then --baked). Note data/baked
# holds run directories directly (data/baked/<run>/<hash>/); the nested
# data/baked/baked/ level is a separate staging area, not where these runs
# live.
#
# Usage:
#   scripts/show/mksite.sh [--out DIR] [--baked DIR] [--skip-build]
#
# The script prints the wrangler deploy command at the end; it NEVER runs
# it — deploying is a deliberate human act.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/data/.pages-staging-new"
BAKED="${TRAFFIC_SIM_BAKED:-/home/eric/grove/traffic-sim/data/baked}"
SKIP_BUILD=0

while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --baked) BAKED="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    *) echo "mksite: unknown flag $1" >&2; exit 2 ;;
  esac
done

# 1. Build the viz (index.html splash + app.html map + demos.html + public/).
if [ "$SKIP_BUILD" -eq 0 ]; then
  echo "[mksite] building viz…"
  (cd "$ROOT/viz" && pnpm build)
fi
DIST="$ROOT/viz/dist"
[ -f "$DIST/index.html" ] || { echo "mksite: $DIST/index.html missing — build the viz first" >&2; exit 1; }
[ -f "$DIST/app.html" ] || { echo "mksite: $DIST/app.html missing — build the viz first" >&2; exit 1; }
# The _headers ship with the build (viz/public/_headers); without them every
# .br chunk arrives as undecodable brotli bytes and replays show no vehicles.
[ -f "$DIST/_headers" ] || { echo "mksite: $DIST/_headers missing — viz/public/_headers did not ship" >&2; exit 1; }

# 2. Which bakes does the site reference? DERIVE the set from the built
# output (the splash CTA's baked-in URL in dist/assets/splash-*.js, the quiz
# data table in dist/quiz.html) rather than hand-maintaining a list that
# can drift from what the pages actually link.
RUNS="$(grep -rhoE "/baked/[A-Za-z0-9_-]+/[0-9a-f]+/index\.json" "$DIST" \
        | sort -u | sed 's|^/baked/||; s|/index\.json$||')"
[ -n "$RUNS" ] || { echo "mksite: no /baked/ references found in $DIST — nothing to stage" >&2; exit 1; }
# Fail loudly on a missing manifest — half a site deploys fine and breaks
# silently, which is the worse outcome.
for r in $RUNS; do
  if [ ! -f "$BAKED/$r/index.json" ]; then
    echo "mksite: MISSING bake $BAKED/$r/index.json — referenced by the site" >&2
    exit 1
  fi
done

# 3. Fresh staging dir = dist + the referenced bakes.
echo "[mksite] staging $OUT"
rm -rf "$OUT"
mkdir -p "$OUT/baked"
cp -a "$DIST/." "$OUT/"
for r in $RUNS; do
  run="${r%%/*}"
  mkdir -p "$OUT/baked/$run"
  echo "[mksite] copying baked/$r"
  cp -a "$BAKED/$r" "$OUT/baked/$run/"
done

# 4. Brotli-compress every staged network.geojson IN PLACE (kept under its
# original name). Two reasons: chishow's is 34.8 MB raw — over Pages'
# 25 MiB per-file cap — and _headers marks /baked/*/network.geojson as
# Content-Encoding: br, which is only truthful if every one of them is
# actually brotli (a plain-JSON file under that rule fails to decode).
# Browsers decode the encoding transparently; fetch() consumers see JSON.
echo "[mksite] brotli-compressing staged network.geojson files"
find "$OUT/baked" -name network.geojson -print0 | while IFS= read -r -d '' f; do
  python3 - "$f" <<'PY'
import sys
try:
    import brotli
except ImportError:
    sys.exit("mksite: python3 brotli module missing — pip install brotli (or brotli cli)")
path = sys.argv[1]
raw = open(path, "rb").read()
if raw[:1] != b"{":  # not plain JSON → assume already compressed, leave it
    sys.exit(0)
open(path, "wb").write(brotli.compress(raw, quality=11))
PY
done

# 5. Report; print the deploy command but DO NOT run it.
echo "[mksite] done — $(du -sh "$OUT" | cut -f1) in $OUT"
echo
echo "Deploy (manual, NOT run by this script):"
echo "  npx wrangler pages deploy $OUT --project-name=phantomjam"
