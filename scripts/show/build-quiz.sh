#!/usr/bin/env bash
# Rebuild the guest-facing quiz page, diagrams and all.
#
#   scripts/show/build-quiz.sh
#
# Two generators now feed one page, and getting them out of step is silent:
# mkoptiondiag.py reads the arm NETWORKS and mkquiz.py reads the curated
# RESULTS, so a re-authored pod can leave the page showing last week's
# geometry beside this week's numbers with nothing to flag it. Running both
# from one script is the cheap version of not letting that happen.
#
# Scenario order here is presentation order on the page.
set -euo pipefail
cd "$(dirname "$0")/../.."

DIAG=docs/show/diag
mkdir -p "$DIAG"

# --exaggerate --fold-y: the merge pod is 5.2 km long, 20 m wide, and parks
# its on-ramp 200 m off the mainline. Drawn true to scale it is a flat line.
# The town is roughly square and needs neither.
python3 scripts/show/mkoptiondiag.py --pod merge-pod \
    --root data/scenarios/merge-pod --base base \
    --arms mainline-lane frontage-road accel-extend ramp-meter \
    --exaggerate --fold-y 12 --height 120 --out "$DIAG"

python3 scripts/show/mkoptiondiag.py --pod bottleneck-town \
    --root data/pods/bottleneck-town --base base \
    --arms bypass-north connector-south retime-short green-wave \
    --out "$DIAG"

# The opening slide: same network, same frame, marked with where traffic
# enters and leaves. --peers passes the option arms so the bounding box
# matches the cards below it — otherwise the network changes size between
# the setup and the options and reads as a different network.
python3 scripts/show/mkopenslide.py --pod merge-pod \
    --root data/scenarios/merge-pod/base --exaggerate --fold-y 12 \
    --height 120 --out "$DIAG" \
    --peers data/scenarios/merge-pod/mainline-lane \
            data/scenarios/merge-pod/frontage-road \
            data/scenarios/merge-pod/accel-extend

python3 scripts/show/mkopenslide.py --pod bottleneck-town \
    --root data/pods/bottleneck-town/base --out "$DIAG" \
    --peers data/pods/bottleneck-town/bypass-north \
            data/pods/bottleneck-town/connector-south

# The big Chicago cut ships text-only: two of its four options are signal
# retimings, and a plan view of a 55,555-lane import at card size is a grey
# smear in which a widened Kennedy is invisible. mkquiz.py enforces
# all-or-none per scenario, so this is a choice rather than an omission.
python3 scripts/chicago/mkquiz.py \
    docs/show/quiz/merge-pod.json \
    docs/show/quiz/bottleneck-town.json \
    docs/show/quiz/chi-loop-urban.json \
    --diagrams "$DIAG" --baselines docs/show/baselines.json \
    --baked-root data/baked --out viz/public/quiz.html

python3 scripts/show/mkcontactsheet.py "$DIAG"

# viz/public is vite's static root; copying to dist keeps an already-running
# demosrv serving the new page without a rebuild. The contact sheet rides
# along so both are reviewable from the same server.
if [ -d viz/dist ]; then
    cp viz/public/quiz.html viz/dist/quiz.html
    rm -rf viz/dist/diag && cp -r "$DIAG" viz/dist/diag
fi
echo "[build-quiz] done — /quiz.html and /diag/index.html"
