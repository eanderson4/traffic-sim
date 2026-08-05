#!/usr/bin/env bash
# Sweep the ramp meter's timing, because on a ramp meter the timing IS the
# intervention.
#
#   scripts/demos/meter-sweep.sh <out-dir> [seeds] [ticks]
#
# The shipped merge pod tests ONE meter: 2 s green, 2 s red, no queue
# override. docs/show/merge-options.md defends that as refusing to tune an
# option until it wins, which is the right instinct about a different
# question. Not tuning guards against searching a parameter space post hoc
# for a winner. It does not license reporting "metering fails" from a single
# arbitrary setting — a 4 s cycle passes roughly 900 veh/h against 1,100
# veh/h of ramp demand, so that particular meter is restrictive enough that
# its own queue reaches its own portal, which is what the shipped result
# actually measured.
#
# So: sweep the operating range, report the whole curve, and let the shape of
# the curve be the finding. If every setting loses, "metering does not
# compete with concrete here" becomes a claim about metering rather than a
# claim about one meter. If some setting wins, the shipped answer was wrong.
# Either way the honest artifact is the curve, not a point.
#
# The metering rate is roughly 3600/(green+red) veh/h at one vehicle per
# green, so the sweep is laid out in rate terms against 1,100 veh/h of ramp
# demand:
#
#   green/red   cycle   ~rate   vs demand
#     2/1        3 s   1200/h   barely restrictive
#     2/2        4 s    900/h   the shipped setting
#     2/3        5 s    720/h
#     2/4        6 s    600/h
#     2/6        8 s    450/h   severe
#     4/2        6 s    600/h   same rate as 2/4, longer platoons
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD

OUT=${1:?output dir}
SEEDS=${2:-12}
TICKS=${3:-12000}

POD=$OUT/pod
# Clean, not just mkdir. `cp -r src "$POD/base"` copies INTO an existing
# directory rather than replacing it, so a second run on the same --out
# nests gen-base/base inside $POD/base while leaving the first run's
# scenario.yaml active at the top level. The sweep then measures the stale
# pod and says nothing.
rm -rf "$POD" "$OUT/gen-base"
mkdir -p "$POD"

# One base for every arm: the meter only exists inside the ramp-meter
# variant, so the baseline is identical across timings and a single shared
# base keeps the A/B paired on exactly the same runs.
python3 scripts/demos/merge-pod.py --pod "$OUT/gen-base" --variants base \
    --ticks "$TICKS" >/dev/null
cp -r "$OUT/gen-base/base" "$POD/base"

for GR in 2:1 2:2 2:3 2:4 2:6 4:2; do
    G=${GR%%:*}; R=${GR##*:}
    NAME="meter-${G}-${R}"
    python3 scripts/demos/merge-pod.py --pod "$OUT/gen-$NAME" \
        --variants ramp-meter --meter-green "$G" --meter-red "$R" \
        --ticks "$TICKS" >/dev/null
    cp -r "$OUT/gen-$NAME/ramp-meter" "$POD/$NAME"
    echo "[meter-sweep] built $NAME (cycle $((G+R))s)" >&2
done

# -exit-routing=false, via the pod's own serve wrapper: on a freeway where
# every lane has one successor, exit routing pins each vehicle to the lane it
# starts in and vetoes every overtake. See scripts/demos/merge-serve.sh.
python3 scripts/whatif.py --pod "$POD" --baseline base \
    --seeds "$SEEDS" --ticks "$TICKS" --jobs 6 --port-base 8700 \
    --serve "$ROOT/scripts/demos/merge-serve.sh" \
    --corridors "$ROOT/data/scenarios/merge-pod/corridors.json" \
    --out "$OUT/meter-sweep.json" | tee "$OUT/meter-sweep.txt"

echo "[meter-sweep] -> $OUT/meter-sweep.json" >&2
