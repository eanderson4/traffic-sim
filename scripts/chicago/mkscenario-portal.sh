#!/usr/bin/env bash
# Build a scenario directory from a PORTAL inventory alone — no buildings,
# no OD matrix.
#
#   mkscenario-portal.sh <network-name> <out-dir> <id> <ticks> [mkdemand args...]
#
# Where mkod.py routes every trip to a chosen destination lane, this leaves
# destinations to the driver's seeded exit routing. That is the right model
# for a corridor or grid cut whose trips are overwhelmingly THROUGH trips:
# it closes the mass balance for free (every vehicle is heading for an exit)
# instead of needing a through-traffic share tuned by hand, which is the
# failure that made the first chi-loop-urban cut a bathtub with the taps on.
#
# Networks without a buildings.json cannot use mkod.py at all, which is the
# other reason this exists.
set -euo pipefail

NET=${1:?network name}
OUT=${2:?output scenario dir}
ID=${3:?scenario id}
TICKS=${4:?tick count}
shift 4

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ND="$ROOT/data/networks/$NET"
[ -d "$ND" ] || { echo "no such network: $ND" >&2; exit 1; }

mkdir -p "$OUT/demand"
# Hard link to skip a large copy; every tool that rewrites a network must
# write-then-rename (see mkscenario.sh).
ln -f "$ND/$NET.json" "$OUT/$NET.json" 2>/dev/null || cp "$ND/$NET.json" "$OUT/$NET.json"

python3 "$ROOT/scripts/chicago/mkdemand.py" \
    --portals "$ND/portals.json" \
    --out "$OUT/demand/main.yaml" "$@"

cat > "$OUT/scenario.yaml" <<EOF
format_version: 1
id: $ID
seed: 42
ticks: $TICKS
network: $NET.json
types:
  - car
  - truck
demand:
  - demand/main.yaml
EOF

echo "[mkscenario-portal] $OUT ready ($ID, $TICKS ticks)" >&2
