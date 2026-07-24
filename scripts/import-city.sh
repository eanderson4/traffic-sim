#!/usr/bin/env bash
# import-city.sh — import a city road network from cached Overpass JSON into
# a loadable scenario. Recipe per contracts/network-format-v1.md ("Bootstrap
# recipe"): Overpass JSON -> .osm XML -> netconvert -> netimport (format v1).
# Stop signs ride a second netconvert pass from a PlainXML node override
# (scripts/osm-stop-nodes.py — netconvert ignores OSM highway=stop nodes).
#
# Usage: scripts/import-city.sh <name> <S,W,N,E> <overpass.json>
# Example: scripts/import-city.sh sf 37.50,-122.52,38.05,-122.00 \
#            ~/grove/math-vs-vibes/promo/ep-03/countdown-short/data/osm-sf-raw.json
set -euo pipefail

if [ $# -ne 3 ]; then
  echo "usage: $0 <name> <S,W,N,E> <overpass.json>" >&2
  exit 2
fi
NAME="$1"; BBOX="$2"; SRC="$3"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NC="$ROOT/tools/sumo-venv/bin/netconvert"  # stable entry point (not the versioned site-packages path)
[ -x "$NC" ] || { echo "netconvert not found at $NC (SUMO venv missing?)" >&2; exit 1; }
NET_DIR="$ROOT/data/networks/$NAME"
SCN_DIR="$ROOT/data/scenarios/$NAME"
mkdir -p "$NET_DIR" "$SCN_DIR"

echo "== [$NAME] overpass json -> .osm"
python3 "$ROOT/scripts/overpass2osm.py" "$SRC" "$NET_DIR/$NAME.osm"

echo "== [$NAME] netconvert -> .net.xml"
"$NC" --osm-files "$NET_DIR/$NAME.osm" -o "$NET_DIR/$NAME.base.net.xml" \
  --proj.utm --no-turnarounds

# Stop signs: netconvert (1.27.1) ignores OSM highway=stop nodes on import,
# so stop junctions are re-typed in a SECOND pass from a PlainXML node
# override (scripts/osm-stop-nodes.py) — the documented workflow, same as
# TLS overrides. No stops in the extract -> single pass, identical net.
# Pass 2 repeats --no-turnarounds so connection recomputation at retyped
# junctions cannot re-add the U-turn loops pass 1 excluded. No --proj.utm
# here on purpose: the base .net.xml carries its own <location> block.
# Deliberately NO --junctions.join anywhere: the override keys junctions by
# OSM node id, which joining rewrites to cluster ids; the KB's consolidation
# guidance conflicts with that keying and loses to stop fidelity (ADR-0017).
echo "== [$NAME] overpass json -> stops nod.xml"
python3 "$ROOT/scripts/osm-stop-nodes.py" "$SRC" "$NET_DIR/$NAME.nod.xml"
PASSES=single
if [ -s "$NET_DIR/$NAME.nod.xml" ]; then
  PASSES="two-pass stop overrides"
  echo "== [$NAME] netconvert pass 2 (stop overrides) -> .net.xml"
  "$NC" --sumo-net-file "$NET_DIR/$NAME.base.net.xml" \
    --node-files "$NET_DIR/$NAME.nod.xml" \
    --no-turnarounds \
    -o "$NET_DIR/$NAME.net.xml"
else
  mv "$NET_DIR/$NAME.base.net.xml" "$NET_DIR/$NAME.net.xml"
fi
rm -f "$NET_DIR/$NAME.base.net.xml"

echo "== [$NAME] netimport -> network format v1"
NC_VER=$("$NC" --version 2>/dev/null | head -1 || true)
EXTRACT_TS=$(python3 -c "import json,sys; print(json.load(open(sys.argv[1])).get('osm3s',{}).get('timestamp_osm_base',''))" "$SRC" 2>/dev/null || true)
# No extract timestamp -> refuse: the provenance stamp feeds the ADR-0012
# content hash, and a time.Now fallback makes identical inputs hash
# differently (reproducibility rule, sol review round).
[ -n "$EXTRACT_TS" ] || { echo "[$NAME] extract $SRC has no osm3s.timestamp_osm_base — refusing nondeterministic import" >&2; exit 1; }
# Importer identity = content hash of the whole import pipeline (Python
# scripts AND the Go importer): stable across clones and unrelated
# commits, changes exactly when the importer changes (the provenance
# feeds the ADR-0012 content hash).
REPO_REV=$(cat "$ROOT/scripts/overpass2osm.py" "$ROOT/scripts/osm-stop-nodes.py" "$ROOT/scripts/import-city.sh" "$ROOT/engine/cmd/netimport/main.go" "$ROOT/engine/netimport/netimport.go" | sha256sum | cut -c1-12)
(cd "$ROOT/engine" && go run ./cmd/netimport \
  -in "$NET_DIR/$NAME.net.xml" \
  -out "$SCN_DIR/$NAME.json" \
  -name "$NAME" \
  -source "import-city.sh@$REPO_REV ($NC_VER, $PASSES, OSM base $EXTRACT_TS)" \
  -source-file "$NAME.net.xml" \
  -imported "$EXTRACT_TS" \
  -bbox "$BBOX" \
  -report "$NET_DIR/import-report.json")

if [ ! -f "$SCN_DIR/scenario.yaml" ]; then
  cat > "$SCN_DIR/scenario.yaml" <<EOF
# $NAME road network — imported from Overpass bbox $BBOX.
# Spawner-driven smoke demand (600 veh/h per origin lane).
format_version: 1
id: $NAME
seed: 42
ticks: 3600
network: $NAME.json
types:
  - car
  - truck
spawner:
  rate_per_lane_h: 600
EOF
  echo "== [$NAME] wrote $SCN_DIR/scenario.yaml"
fi

echo "== [$NAME] done: $SCN_DIR"
