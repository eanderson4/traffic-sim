#!/usr/bin/env bash
# Build the chi-loop-urban what-if pod: a "do nothing" baseline plus seven
# candidate upgrades, sized so the curated set can contain real no-ops.
#
#   pod-chi-loop.sh <pod-dir> <freeway-scale> <ticks>
#
# The candidates are chosen so the ANSWER is not obvious from the label —
# that is the whole game. Two of them target Lake Shore Drive, which the
# baseline shows carrying the most delay per lane-km of any named corridor;
# two target the Kennedy, which the baseline shows running near free flow
# and where extra capacity should therefore do nothing; and the rest are the
# policy levers people reach for by reflex.
#
# Every variant is a COMPLETE scenario directory rather than an ADR-0012
# patch variant, because two of them replace the network wholesale and the
# patch grammar covers demand only (ADR-0012 addendum §3). The network JSON
# is hard-linked where it is unmodified, so the pod costs one copy per
# infrastructure variant rather than one per option.
set -euo pipefail

POD=${1:?pod directory}
FWSCALE=${2:?freeway scale}
TICKS=${3:?ticks}

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
NET=chi-loop-urban
ND="$ROOT/data/networks/$NET"
CORR="$ND/corridors.json"
MK="$ROOT/scripts/chicago"

ODARGS=(--total 16000 --flat-peak --freeway-scale "$FWSCALE")

mkdir -p "$POD"

# ---- base ---------------------------------------------------------------
"$MK/mkscenario.sh" "$NET" "$POD/base" base "$TICKS" "${ODARGS[@]}"
BASEDEM="$POD/base/demand/main.yaml"

# variant <name> — a scenario dir sharing base's demand and network unless
# a later step overwrites one of them.
variant() {
    local name=$1
    mkdir -p "$POD/$name/demand"
    cp "$BASEDEM" "$POD/$name/demand/main.yaml"
    ln -f "$POD/base/$NET.json" "$POD/$name/$NET.json" 2>/dev/null \
        || cp "$POD/base/$NET.json" "$POD/$name/$NET.json"
    sed "s/^id: base$/id: $name/" "$POD/base/scenario.yaml" > "$POD/$name/scenario.yaml"
}

# ---- A: widen Lake Shore Drive -----------------------------------------
# The corridor the baseline says is worst. Mainline capacity only — the new
# lane inherits its donor's junction connectivity (see mknetvariant.py).
variant lsd-widen
python3 "$MK/mknetvariant.py" --network "$ND/$NET.json" --corridors "$CORR" \
    --add-lane lsd --name "$NET+lsd-widen" --out "$POD/lsd-widen/$NET.json"

# ---- B: widen the Kennedy ----------------------------------------------
# Same intervention on a corridor that is NOT the bottleneck. Should be a
# no-op, and an evaluation that cannot produce that answer is not measuring.
variant kennedy-widen
python3 "$MK/mknetvariant.py" --network "$ND/$NET.json" --corridors "$CORR" \
    --add-lane kennedy --name "$NET+kennedy-widen" --out "$POD/kennedy-widen/$NET.json"

# ---- C: ramp metering --------------------------------------------------
# Hold 20% of freeway on-ramp entries back. Genuinely fewer cars enter, so
# watch throughput alongside speed.
variant ramp-meter
python3 "$MK/scaledemand.py" --in "$BASEDEM" --out "$POD/ramp-meter/demand/main.yaml" \
    --scale 0.8 --class motorway_link --class trunk_link

# ---- D: shorter signal cycles ------------------------------------------
variant retime-short
python3 "$MK/mknetvariant.py" --network "$ND/$NET.json" --retime 0.66 \
    --name "$NET+retime-short" --out "$POD/retime-short/$NET.json"

# ---- E: longer signal cycles -------------------------------------------
# The reflex "give the main street more green". Expected to be worse; it is
# in the set so the curated options are not all improvements.
variant retime-long
python3 "$MK/mknetvariant.py" --network "$ND/$NET.json" --retime 1.4 \
    --name "$NET+retime-long" --out "$POD/retime-long/$NET.json"

# ---- F: downtown truck ban ---------------------------------------------
variant truck-ban
python3 "$MK/scaledemand.py" --in "$BASEDEM" --out "$POD/truck-ban/demand/main.yaml" \
    --scale 1.0 --no-trucks --all

# ---- G: 5% mode shift ---------------------------------------------------
# Deliberately small. A 5% shift is roughly the size of the seed-to-seed
# noise, so this is the option that SHOULD come back statistically
# insignificant — and if it does not, the harness is lying.
variant transit-5
python3 "$MK/scaledemand.py" --in "$BASEDEM" --out "$POD/transit-5/demand/main.yaml" \
    --scale 0.95 --all

echo "[pod] built $(ls -d "$POD"/*/ | wc -l) variants in $POD (freeway-scale $FWSCALE, $TICKS ticks)" >&2
ls -1 "$POD"
