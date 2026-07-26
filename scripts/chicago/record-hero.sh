#!/usr/bin/env bash
# Record the hero cut and bake it into static replay artifacts (ADR-0023).
#
#   record-hero.sh <scenario-dir> <run-id> <ticks> <out-root> [serve-flags...]
#
# Two phases, and they MUST be serial: exactly one broker may open a
# JetStream store directory at a time, so the recording serve has to have
# exited before bake opens the same store.
#
# The run is checked for fidelity before it is baked. A recording is durable
# — it gets content-hashed, deployed, and pointed at — so baking one whose
# demand never entered or whose fleet spent the run without a controller
# bakes a lie into an artifact that outlives the terminal it was made in.
# The three gates below are exactly the ones serve already prints; this
# refuses to proceed on them rather than leaving it to whoever reads the log.
set -euo pipefail

SC=${1:?scenario dir}
RUN=${2:?run id}
TICKS=${3:?ticks}
OUT=${4:?output root}
shift 4

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
# Binaries are build artifacts, not repo contents. Point at wherever they
# were built; the defaults assume a scratch build rather than dropping
# 25 MB executables into the working tree.
SERVE_BIN=${SERVE_BIN:-$ROOT/engine/serve}
BAKE_BIN=${BAKE_BIN:-$ROOT/engine/bake}
for b in "$SERVE_BIN" "$BAKE_BIN"; do
    [ -x "$b" ] || { echo "record-hero: no executable at $b — build it ""(go build -o \"$b\" ./cmd/...) or set SERVE_BIN/BAKE_BIN" >&2; exit 1; }
done
STORE="$OUT/store-$RUN"
LOG="$OUT/$RUN.log"
mkdir -p "$OUT"

if [ -e "$STORE" ]; then
    echo "record-hero: $STORE exists — serve refuses to append into an "\
"existing recording of the same run id; move it or pick another run id" >&2
    exit 1
fi

echo "[hero] recording $RUN ($TICKS ticks) -> $STORE" >&2
"$SERVE_BIN" -scenario "$SC" -run "$RUN" -ticks "$TICKS" \
    -store "$STORE" -pace 0 -metrics-out "$OUT/$RUN.metrics.json" \
    "$@" 2>&1 | tee "$LOG"

fail=0
check() {  # check <grep pattern> <human message>
    if grep -q "$1" "$LOG"; then
        echo "[hero] REFUSING TO BAKE: $2" >&2
        fail=1
    fi
}
check "of demand never entered the network" \
    "the run did not simulate its own scenario (demand delivery below threshold)"
check "the driver could not keep up" \
    "a material share of vehicle-ticks ran with no car-following control"
check "nats-server not ready" "the broker never came up"
[ "$fail" = 0 ] || exit 1

echo "[hero] baking $RUN -> $OUT" >&2
"$BAKE_BIN" -store "$STORE" -run "$RUN" -out "$OUT"
echo "[hero] done: $OUT/baked/$RUN/" >&2
