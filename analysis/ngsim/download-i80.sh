#!/usr/bin/env bash
# Download an NGSIM I-80 trajectory window from data.transportation.gov (Socrata API).
#
# Data: FHWA Next Generation Simulation (NGSIM), I-80 Emeryville CA, 2005-04-13.
# License: CC BY-SA 3.0. We ship this script, not the data — run it yourself.
# Dataset: https://catalog.data.gov/dataset/next-generation-simulation-ngsim-vehicle-trajectories-and-supporting-data
#
# Usage: ./download-i80.sh [period]
#   period: 1600-1615 | 1700-1715 (default) | 1715-1730   (local PDT wall clock)
#
# Output: CSV at data/ngsim/i80-<period>.csv (gitignored) with only the columns
# needed for Edie-based x–t analysis. Units are NGSIM-native: feet, ft/s, 0.1 s
# frames; global_time is epoch milliseconds.
set -euo pipefail

PERIOD="${1:-1700-1715}"
case "$PERIOD" in
  # Recording day 2005-04-13; epoch ms for each 15-min analysis period.
  1600-1615) T0=1113433200000; T1=1113434100000 ;;
  1700-1715) T0=1113436800000; T1=1113437700000 ;;
  1715-1730) T0=1113437700000; T1=1113438600000 ;;
  *) echo "unknown period '$PERIOD' (use 1600-1615 | 1700-1715 | 1715-1730)" >&2; exit 1 ;;
esac

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT_DIR="$REPO_ROOT/data/ngsim"
OUT="$OUT_DIR/i80-$PERIOD.csv"
mkdir -p "$OUT_DIR"

BASE="https://data.transportation.gov/resource/8ect-6jqj.csv"
SELECT='vehicle_id,frame_id,global_time,local_y,v_vel,lane_id,v_class,v_length'
WHERE="location='i-80' AND global_time between $T0 and $T1"
PAGE=500000

echo "Downloading I-80 $PERIOD to $OUT ..."
: > "$OUT"
offset=0
while :; do
  chunk="$OUT.chunk"
  curl -sf "$BASE" \
    --data-urlencode "\$select=$SELECT" \
    --data-urlencode "\$where=$WHERE" \
    --data-urlencode "\$order=:id" \
    --data-urlencode "\$limit=$PAGE" \
    --data-urlencode "\$offset=$offset" \
    -G -o "$chunk"
  rows=$(($(wc -l < "$chunk") - 1))
  if [ "$offset" -eq 0 ]; then
    cat "$chunk" >> "$OUT"
  else
    tail -n +2 "$chunk" >> "$OUT"
  fi
  rm -f "$chunk"
  echo "  ...$((offset + rows)) rows"
  [ "$rows" -lt "$PAGE" ] && break
  offset=$((offset + PAGE))
done

echo "Done: $(wc -l < "$OUT") lines (incl. header)"
