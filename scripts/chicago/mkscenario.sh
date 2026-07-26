#!/usr/bin/env bash
# Build a runnable ADR-0012 scenario directory for a Chicago network.
#
#   mkscenario.sh <network-name> <out-dir> <scenario-id> <ticks> [mkod args...]
#
# The network JSON is HARD-LINKED rather than copied: chi-loop-urban.json is
# 45 MB and a bracket of six variants would otherwise cost 270 MB of
# identical bytes. Falls back to a copy across filesystems.
#
# Every tool that rewrites a network MUST therefore write-then-rename rather
# than truncate in place (mknetvariant.py does). Truncating a link rewrites
# the shared inode — which means the source network under data/networks and
# every other scenario pointing at it. Read-only sharing is the entire point
# of the link; in-place mutation destroys it.
set -euo pipefail

NET=${1:?network name, e.g. chi-loop-urban}
OUT=${2:?output scenario dir}
ID=${3:?scenario id}
TICKS=${4:?tick count}
shift 4

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ND="$ROOT/data/networks/$NET"
[ -d "$ND" ] || { echo "no such network: $ND" >&2; exit 1; }

mkdir -p "$OUT/demand"
ln -f "$ND/$NET.json" "$OUT/$NET.json" 2>/dev/null || cp "$ND/$NET.json" "$OUT/$NET.json"

python3 "$ROOT/scripts/chicago/mkod.py" \
    --buildings "$ND/buildings.json" \
    --network "$ND/$NET.json" \
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

echo "[mkscenario] $OUT ready ($ID, $TICKS ticks)" >&2
