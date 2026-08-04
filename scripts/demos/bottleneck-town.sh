#!/usr/bin/env bash
# Bottleneck Town, end to end: author the pod, run the paired-seed A/B,
# record and bake the baseline, and print the viewing URL.
#
#   scripts/demos/bottleneck-town.sh [pod|ab|bake|all] [seeds]
#
# Everything it writes lands under data/ (gitignored). The durable sources
# are scripts/demos/bottleneck_town.py (the network + scenario author) and
# this file.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
STEP=${1:-all}
SEEDS=${2:-16}
POD=$ROOT/data/pods/bottleneck-town
WORK=$ROOT/data/town
TICKS=15000
WARMUP=6000
BAKE_TICKS=9000       # 15 sim minutes: long enough to be fully loaded, short
                      # enough that the replay artifacts stay small

mkdir -p "$WORK"
BIN=$WORK/bin
mkdir -p "$BIN"

build() {
    (cd "$ROOT/engine" && go build -o "$BIN/serve" ./cmd/serve && \
                          go build -o "$BIN/bake" ./cmd/bake)
}

case "$STEP" in
pod|all)
    build
    python3 "$ROOT/scripts/demos/bottleneck_town.py" --out "$POD" --ticks $TICKS
    python3 "$ROOT/scripts/demos/bottleneck_town.py" --out "$POD" --check
    ;;&
ab|all)
    build
    python3 "$ROOT/scripts/whatif.py" --pod "$POD" --baseline base \
        --seeds "$SEEDS" --ticks $TICKS --warmup $WARMUP --jobs 5 \
        --capacity 40000 --serve "$BIN/serve" --port-base 9200 \
        --keep "$WORK/runs" --out "$WORK/whatif.json"
    ;;&
bake|all)
    build
    # record-hero.sh keeps its store at $OUT/store-$RUN and REFUSES to start
    # if it already exists (serve will not append into an existing recording
    # of the same run id), so clear that path, not the old $WORK/store one.
    rm -rf "$WORK/baked"
    # Through record-hero.sh, NOT serve+bake directly. serve exits 0 even on
    # a run it has itself declared void — under-delivered demand, uncontrolled
    # coasting, a controller blind past the hold-last bridge — so calling the
    # two binaries in sequence will happily bake a recording of a scenario
    # that was never simulated. record-hero.sh is where those gates live, and
    # a durable artifact is exactly what must not skip them.
    SERVE_BIN="$BIN/serve" BAKE_BIN="$BIN/bake" \
        "$ROOT/scripts/chicago/record-hero.sh" \
        "$POD/base" townbase "$BAKE_TICKS" "$WORK/baked" \
        -seed 1000 -capacity 40000 -ws 127.0.0.1:8459 \
        -metrics-out "$WORK/townbase-metrics.json"
    PREFIX=$(ls -d "$WORK"/baked/baked/townbase/*/)
    (cd "$ROOT/viz" && node scripts/bake-furniture.mjs "$PREFIX")
    echo
    echo "serve it:   python3 scripts/serve-baked.py --baked $WORK/baked/baked --viz viz/dist --port 8791"
    echo "open:       http://127.0.0.1:8791/?bake=http://127.0.0.1:8791/baked/townbase/$(basename "$PREFIX")/index.json&center=-98.4840,39.6963&zoom=14.2"
    echo "            (drag the replay slider to ~70% and play 90 s: ticks 6300-7200)"
    ;;
esac
