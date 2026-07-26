#!/usr/bin/env bash
# Build a what-if pod for a PORTAL-demand network (mkdemand.py + driver exit
# routing), for the two smaller Chicago cuts.
#
#   pod-portal.sh <pod-dir> <network> <total veh/h> <ticks> <flavour>
#
# flavour = freeway | grid, which picks the candidate set:
#
#   freeway (chi-kennedy) — a corridor whose problem is mainline capacity
#     and merge turbulence. Candidates test capacity, entry control, the
#     vehicle mix, and speed harmonisation.
#
#   grid (chi-loop-cbd) — 1,057 signals in 208 lane-km, so a vehicle meets a
#     signal roughly every 200 m and the network runs at ~10-13 km/h with
#     barely any traffic on it. The binding constraint is signal time, not
#     road space, and the candidate set is built to demonstrate exactly
#     that: the two lane-count options should do close to nothing while the
#     retiming options move the number.
#
# Both sets deliberately include options expected to be no-ops or worse.
set -euo pipefail

POD=${1:?pod directory}
NET=${2:?network name}
TOTAL=${3:?total veh/h}
TICKS=${4:?ticks}
FLAVOUR=${5:?freeway|grid}

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
ND="$ROOT/data/networks/$NET"
CORR="$ND/corridors.json"
MK="$ROOT/scripts/chicago"

mkdir -p "$POD"
"$MK/mkscenario-portal.sh" "$NET" "$POD/base" base "$TICKS" --total "$TOTAL"
BASEDEM="$POD/base/demand/main.yaml"

variant() {
    local name=$1
    mkdir -p "$POD/$name/demand"
    cp "$BASEDEM" "$POD/$name/demand/main.yaml"
    ln -f "$POD/base/$NET.json" "$POD/$name/$NET.json" 2>/dev/null \
        || cp "$POD/base/$NET.json" "$POD/$name/$NET.json"
    sed "s/^id: base$/id: $name/" "$POD/base/scenario.yaml" > "$POD/$name/scenario.yaml"
}

netvar() { python3 "$MK/mknetvariant.py" --network "$ND/$NET.json" --corridors "$CORR" "$@"; }
demvar() { python3 "$MK/scaledemand.py" --in "$BASEDEM" "$@"; }

if [ "$FLAVOUR" = freeway ]; then
    variant mainline-widen
    netvar --add-lane motorway --name "$NET+mainline-widen" --out "$POD/mainline-widen/$NET.json"

    variant ramp-meter
    demvar --out "$POD/ramp-meter/demand/main.yaml" --scale 0.8 --class motorway_link

    variant truck-ban
    demvar --out "$POD/truck-ban/demand/main.yaml" --scale 1.0 --no-trucks --all

    # Speed harmonisation: a lower posted limit smooths the speed
    # distribution and can raise THROUGHPUT even as it caps top speed, so
    # the speed metric alone will read it as a loss. That tension is the
    # point of reporting both.
    variant speed-harmonise
    netvar --speed motorway=80 --name "$NET+speed-harmonise" --out "$POD/speed-harmonise/$NET.json"

    variant ramp-widen
    netvar --add-lane motorway_link --name "$NET+ramp-widen" --out "$POD/ramp-widen/$NET.json"

    variant retime
    netvar --retime 0.66 --name "$NET+retime" --out "$POD/retime/$NET.json"

    variant transit-5
    demvar --out "$POD/transit-5/demand/main.yaml" --scale 0.95 --all
else
    variant retime-short
    netvar --retime 0.66 --name "$NET+retime-short" --out "$POD/retime-short/$NET.json"

    variant retime-long
    netvar --retime 1.4 --name "$NET+retime-long" --out "$POD/retime-long/$NET.json"

    # Road space, on a network whose constraint is signal time. Both of
    # these SHOULD be close to no-ops, in opposite directions.
    variant widen-secondary
    netvar --add-lane secondary --name "$NET+widen-secondary" --out "$POD/widen-secondary/$NET.json"

    variant bus-lane
    netvar --drop-lane secondary --name "$NET+bus-lane" --out "$POD/bus-lane/$NET.json"

    variant cordon-20
    demvar --out "$POD/cordon-20/demand/main.yaml" --scale 0.8 --all

    variant truck-ban
    demvar --out "$POD/truck-ban/demand/main.yaml" --scale 1.0 --no-trucks --all

    variant transit-5
    demvar --out "$POD/transit-5/demand/main.yaml" --scale 0.95 --all
fi

# Whole-network metrics with the fill-up transient excluded.
for d in "$POD"/*/; do
    mkdir -p "$d/metrics"
    python3 "$MK/mkmetrics.py" --network "$d/$NET.json" --out "$d/metrics/main.yaml" \
        --begin-s "$(python3 -c "print($TICKS*0.1/3)")" --period-s 300 2>/dev/null
    grep -q "^metrics:" "$d/scenario.yaml" || printf 'metrics:\n  - metrics/main.yaml\n' >> "$d/scenario.yaml"
done

echo "[pod] $POD: $(ls -d "$POD"/*/ | wc -l) variants ($NET, $TOTAL veh/h, $TICKS ticks, $FLAVOUR)" >&2
ls -1 "$POD"
