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

# Speed defaults (ADR-0022). netconvert's built-in OSM typemap is
# German-derived — unposted secondary/primary/trunk default to 27.78 m/s
# (100 km/h) — and `maxspeed` is tagged on only ~18% of chi-loop's highway
# ways (16.5% of secondary, 0% of primary), so US imports
# compiled their arterials as autobahn. Free-flow speed is the denominator
# of every congestion number the metric kernel reports, so this is not a
# tuning knob. The US map is the DEFAULT and a non-US import must OPT OUT:
# set TYPEMAP= (empty) for a non-US import (de-roundabouts) to get
# netconvert's stock German behavior. Defaulting to US is deliberate —
# nearly every network here is US, so a forgotten flag should bite in the
# rare case, not the common one (ADR-0022 §3).
TYPEMAP="${TYPEMAP-$ROOT/scripts/osm-urban-us.typ.xml}"
TYPE_ARGS=()
if [ -n "$TYPEMAP" ]; then
  [ -f "$TYPEMAP" ] || { echo "typemap not found: $TYPEMAP (set TYPEMAP= to use netconvert's stock defaults)" >&2; exit 1; }
  # The typemap is HALF the fix. netconvert has a second speed-derived
  # German default: --junctions.right-before-left.speed-threshold (13.6111
  # m/s), which types an unsignalized junction right_before_left when ALL
  # its incoming edges are below that speed. 30 mph = 13.41 m/s is 0.2 m/s
  # under it, so lowering arterials to the statutory urban limit flips
  # right-of-way wholesale — measured on chi-loop, 1,218 junctions went
  # priority -> right_before_left (275 -> 1,493). Setting the threshold to
  # 0 disables rbl entirely.
  #
  # This is a deliberate MODEL choice, not just a restore, and it rests on
  # the CONVENTION rather than on any count we have taken: US intersection
  # control is a signal, an all-way stop, or a two-way stop where the major
  # street keeps priority (MUTCD) — mutual yield-to-the-right is not a US
  # rule the way it is in Germany. Signals are imported; stop signs ride
  # the ADR-0017 pass-2 override where the extract has them. For the
  # residue, `priority` (major road proceeds, minor yields) is the closer
  # approximation, and rbl is additionally a well-known SUMO
  # mutual-blocking gridlock source — 975,673 collision observations on
  # the defective import, 79% inside a single junction. See ADR-0022 §6.
  TYPE_ARGS=(--type-files "$TYPEMAP" --junctions.right-before-left.speed-threshold 0)
  echo "== [$NAME] typemap: $TYPEMAP (right-before-left disabled)"
else
  echo "== [$NAME] typemap: netconvert stock (German OSM defaults)"
fi

echo "== [$NAME] overpass json -> .osm"
python3 "$ROOT/scripts/overpass2osm.py" "$SRC" "$NET_DIR/$NAME.osm"

echo "== [$NAME] netconvert -> .net.xml"
"$NC" --osm-files "$NET_DIR/$NAME.osm" -o "$NET_DIR/$NAME.base.net.xml" \
  "${TYPE_ARGS[@]}" --proj.utm --no-turnarounds

# Stop signs: netconvert (1.27.1) ignores OSM highway=stop nodes on import,
# so stop junctions are re-typed in a SECOND pass from a PlainXML node
# override (scripts/osm-stop-nodes.py) — the documented workflow, same as
# TLS overrides. No stops in the extract -> single pass, identical net.
# Pass 2 repeats --no-turnarounds so connection recomputation at retyped
# junctions cannot re-add the U-turn loops pass 1 excluded. No --proj.utm
# here on purpose: the base .net.xml carries its own <location> block.
# Deliberately NO --type-files here either: pass 2 reimports a .net.xml
# whose edge speeds are already resolved, and ADR-0017 verified pass 2
# changes ONLY the retyped junctions. Re-applying types would put that
# guarantee back in question for no gain.
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
# The typemap is IN the hash (ADR-0022 §4): it shapes the compiled network
# as much as any script here, so editing a speed must move the provenance
# string. Absent (stock netconvert defaults) it contributes nothing, which
# is itself the distinguishing input.
REPO_REV=$(cat "$ROOT/scripts/overpass2osm.py" "$ROOT/scripts/osm-stop-nodes.py" "$ROOT/scripts/import-city.sh" ${TYPEMAP:+"$TYPEMAP"} "$ROOT/engine/cmd/netimport/main.go" "$ROOT/engine/netimport/netimport.go" | sha256sum | cut -c1-12)
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
