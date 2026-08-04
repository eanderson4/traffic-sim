#!/usr/bin/env bash
# Sweep --freeway-scale and report what each setting DELIVERS, not what it asks for.
#
#   fwsweep.sh <out-dir> <ticks> <scale> [scale...]
#
# The 4.15 in chi-show-fw20's header was chosen on a 6,000-tick probe by
# corridor delay share and speed. Neither of the two numbers that decide
# whether a run is admissible was checked at the time:
#
#   * DEMAND DELIVERY. A portal whose lane is occupied cannot inject, so
#     the vehicle expires. Past some scale the network stops accepting the
#     demand and the run silently measures a lighter scenario — and, because
#     the cars that did not enter are exactly the ones that would have
#     queued, it reports a FASTER network the harder you push it.
#   * UNCONTROLLED COASTING. A vehicle no controller claimed gets no
#     car-following term, holds speed into whatever is stopped ahead, and
#     books overlaps as collisions. That reads as congestion and is not.
#
# Both are printed by serve and both are gates in record-hero.sh. This runs
# the sweep serially on an idle machine (they are CPU-bound and contend with
# each other; the coasting figure is contention-sensitive, so running them in
# parallel would measure the harness rather than the scenario) and tabulates
# delivery, coasting and speed against scale.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${1:?output dir}
TICKS=${2:?ticks}
shift 2

NET=$ROOT/data/networks/chi-loop-urban
SERVE=$ROOT/engine/serve
mkdir -p "$OUT"
FAILED=""

for SCALE in "$@"; do
    # Keep the decimal point as 'p': stripping it collides 4.15 with 41.5,
    # and the collision is silent — the second run overwrites the first's
    # metrics under the same tag.
    TAG=fw$(echo "$SCALE" | tr '.' 'p')
    SC=$OUT/scen-$TAG
    echo "[fwsweep] === freeway-scale $SCALE -> $TAG ===" >&2
    mkdir -p "$SC/demand" "$SC/metrics"
    ln -f "$NET/chi-loop-urban.json" "$SC/chi-loop-urban.json"
    python3 "$ROOT/scripts/chicago/mkod.py" \
        --buildings "$NET/buildings.json" --network "$NET/chi-loop-urban.json" \
        --portals "$NET/portals.json" --total 16000 --freeway-scale "$SCALE" \
        --flat-peak --out "$SC/demand/main.yaml" 2> "$OUT/$TAG.mkod.log"
    python3 "$ROOT/scripts/chicago/mkmetrics.py" --network "$SC/chi-loop-urban.json" \
        --out "$SC/metrics/main.yaml" --begin-s 0 --period-s 300 2>/dev/null
    cat > "$SC/scenario.yaml" <<YAML
format_version: 1
id: $TAG
seed: 42
ticks: $TICKS
network: chi-loop-urban.json
types:
  - car
  - truck
demand:
  - demand/main.yaml
metrics:
  - metrics/main.yaml
# Calibrated against the static-routing baseline (docs/show); the engine
# default is adaptive-on since 2026-07-31 (ADR-0036 addendum).
params:
  adaptive_routing: false
YAML
    # Clear BOTH outputs before the run, not just the store. A sweep is
    # normally re-run into the same directory, and `|| true` below means a
    # crashed run leaves the previous attempt's metrics sitting there — the
    # tabulator downstream cannot tell that file apart from a fresh one and
    # would report the old scale's numbers under the new tag, labelled done.
    rm -rf "$OUT/store-$TAG"
    rm -f "$OUT/$TAG.metrics.json"
    rc=0
    "$SERVE" -scenario "$SC" -run "$TAG" -ticks "$TICKS" -pace 0 \
        -drivers 8 -capacity 48000 -store "$OUT/store-$TAG" \
        -metrics-out "$OUT/$TAG.metrics.json" > "$OUT/$TAG.log" 2>&1 || rc=$?
    # A failed scale is a hole in the sweep, and the whole point of the sweep
    # is to find the scale where things start failing — so say so loudly and
    # keep going, rather than either hiding it or aborting the other scales.
    if [ "$rc" != 0 ] || [ ! -s "$OUT/$TAG.metrics.json" ]; then
        echo "[fwsweep] $TAG FAILED (exit $rc) — no metrics; see $OUT/$TAG.log" >&2
        rm -f "$OUT/$TAG.metrics.json"
        rm -rf "$OUT/store-$TAG"
        FAILED="$FAILED $TAG"
        continue
    fi
    # The store is the bulk of the disk cost and nothing downstream reads it;
    # a sweep that fills the disk stops being a sweep.
    rm -rf "$OUT/store-$TAG"
    echo "[fwsweep] $TAG done" >&2
done
if [ -n "$FAILED" ]; then
    echo "[fwsweep] DONE WITH FAILURES:$FAILED — those scales have no metrics" >&2
    exit 1
fi
echo "[fwsweep] ALL DONE" >&2
