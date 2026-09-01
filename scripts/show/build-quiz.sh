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

# The two presentation Chicago cuts are setup-slide-only like the big one
# (below): their quiz options are retimings, demand shifts and class-wide
# speed caps — none of it visible in a plan view at card size — and their
# networks (11,851 / 15,315 lanes) smear the same way. mkquiz.py's
# all-or-none rule makes setup-only a choice, not an omission, and with no
# option cards there is no --peers bounding box to match. --net names the
# network file because these pods carry it under the import's name, not
# network.json.
python3 scripts/show/mkopenslide.py --pod chi-loop-cbd \
    --root data/pods/chi-loop-cbd/base --net chi-loop-cbd.json \
    --out "$DIAG"

python3 scripts/show/mkopenslide.py --pod chi-kennedy \
    --root data/pods/chi-kennedy/base --net chi-kennedy.json \
    --out "$DIAG"

# The big Chicago cut gets no per-OPTION diagrams: two of its four options
# are signal retimings, and a plan view of a 55,555-lane import at card size
# is a grey smear in which a widened Kennedy is invisible. mkquiz.py enforces
# all-or-none per scenario, so that is a choice rather than an omission.
#
# Its SETUP slide is a congestion map instead, and it must be regenerated
# here rather than left lying in $DIAG. A stale PNG under the right filename
# is indistinguishable from a fresh one, and the first version of this slide
# was accidentally drawn from a DIFFERENT demand (freeway-scale 3.5) than the
# numbers beside it — which is exactly the divergence the no-figure-in-two-
# files rule exists to stop, in picture form.
#
# CHI_METRICS must name the -metrics-out of a run of THE SAME DEMAND AND
# WINDOW the Chicago numbers came from. Not "the run they came from" — there
# isn't one: the quiz table is 6 seeds x 12,000 ticks, so no single
# recording is it. What must match is the scenario and the measured window;
# the sidecar records the digest and label so a reader can check.
#
# The window defaults are the PUBLISHED one — docs/show/chi-loop-options.md
# reports 12,000 ticks with warmup 4,000, so the map is cut the same way at
# both ends. Defaulting the warmup to anything else (it was 6,000) makes the
# canonical rebuild draw a different window than the table it sits beside,
# and --end-tick stops a longer metrics file from quietly widening the other
# end. Override both together or not at all.
#
# REQUIRED, not optional, and not defaulted to a path inside data/
# (which is gitignored): an unset variable used to mean "reuse whatever PNG
# is lying in $DIAG", which reintroduces the divergence this exists to stop —
# the quiz numbers regenerate while the map silently keeps describing an
# older, or entirely different, demand. Fail closed instead, and say what to
# pass. CHI_SKIP_MAP=1 is the deliberate opt-out (Chicago then ships
# text-only, which mkquiz handles).
OMIT=""
if [ "${CHI_SKIP_MAP:-}" = "1" ]; then
    echo "build-quiz: CHI_SKIP_MAP=1 — Chicago ships without a setup map" >&2
    # Suppress the slide; do NOT delete the artwork. The PNG and its sidecar
    # are TRACKED, so removing them leaves a stageable deletion of a shipped
    # asset in the checkout — one `git add -A` away from committing it, for
    # what is meant to be a per-build choice.
    OMIT="--omit-diagram chi-loop-urban__setup"
else
    if [ -z "${CHI_METRICS:-}" ]; then
        cat >&2 <<'MSG'
build-quiz: CHI_METRICS is required.

  It must be the -metrics-out file of the run the Chicago quiz numbers came
  from, so the setup map and the numbers describe the same demand. Passing
  the wrong run is how the map ended up drawn at freeway-scale 3.5 while the
  table beside it was measured at the shipped scale.

    CHI_METRICS=<run>.metrics.json scripts/show/build-quiz.sh
    CHI_SKIP_MAP=1 scripts/show/build-quiz.sh    # text-only Chicago card
MSG
        exit 1
    fi
    [ -r "$CHI_METRICS" ] || {
        echo "build-quiz: CHI_METRICS=$CHI_METRICS is not readable" >&2
        exit 1
    }
    # Both ends or neither. The pair IS the window; moving one end alone
    # silently measures something no published table describes, and the
    # override exists for "I republished against a different horizon",
    # which always moves both.
    if { [ -n "${CHI_WARMUP:-}" ] && [ -z "${CHI_END:-}" ]; } || \
       { [ -z "${CHI_WARMUP:-}" ] && [ -n "${CHI_END:-}" ]; }; then
        echo "build-quiz: set CHI_WARMUP and CHI_END together or neither — "\
"one alone draws a window no results table claims (defaults 4000/12000, "\
"the published Chicago window)" >&2
        exit 1
    fi
    python3 scripts/show/mkcongestionmap.py \
        --network data/networks/chi-loop-urban/chi-loop-urban.json \
        --metrics "$CHI_METRICS" --warmup-tick "${CHI_WARMUP:-4000}" \
        --end-tick "${CHI_END:-12000}" \
        --run-label "${CHI_RUN_LABEL:-$(basename "$CHI_METRICS" .metrics.json)}" \
        --note "${CHI_NOTE:-}" \
        --width 1400 --out "$DIAG/chi-loop-urban__setup.png"
fi

python3 scripts/chicago/mkquiz.py \
    docs/show/quiz/merge-pod.json \
    docs/show/quiz/bottleneck-town.json \
    docs/show/quiz/chi-loop-cbd.json \
    docs/show/quiz/chi-kennedy.json \
    docs/show/quiz/chi-loop-urban.json \
    --diagrams "$DIAG" --baselines docs/show/baselines.json \
    ${OMIT} \
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
