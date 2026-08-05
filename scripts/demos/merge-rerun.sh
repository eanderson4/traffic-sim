#!/usr/bin/env bash
# Re-measure the merge pod after the frontage-road diversion share changed
# from 30% to 15%, and regenerate every artifact downstream of it.
#
#   scripts/demos/merge-rerun.sh
#
# WAITS FOR THE METER SWEEP TO FINISH FIRST. Two A/B batches running at once
# would each measure the other: `serve` reports uncontrolled coasting — the
# share of vehicle-ticks with no controller intent — and that figure is
# contention-sensitive. An earlier probe on a machine also running a browser
# read 13% coasting where the true figure on an idle box was 0.07%, and
# coasting is one of the fidelity gates these runs are judged against.
#
# Only the frontage-road arm's DEMAND changes; the network geometry is
# identical and the other arms are untouched. The whole pod is re-run anyway
# so every number in the shipped report comes from one batch on one machine
# — a report stitched from two batches cannot be checked for exactly the
# contention effect above.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
SCR=${SCRATCH:?set SCRATCH to a working directory}

while pgrep -f "meter-sweep.sh" >/dev/null; do sleep 30; done
echo "=== meter sweep clear, re-running the merge pod at 15% $(date +%H:%M:%S)"

# Regenerate the pod in place. --frontage-share now defaults to 0.15; passing
# it explicitly would let the shipped pod and the documented default drift.
python3 scripts/demos/merge-pod.py --pod data/scenarios/merge-pod --clean \
    --ticks 12000

# Same 12 seeds, ticks and warmup as the shipped report, so the new numbers
# are comparable to the old ones rather than merely both correct.
python3 scripts/whatif.py --pod data/scenarios/merge-pod --baseline base \
    --seeds 12 --seed-base 1000 --ticks 12000 --warmup 3000 \
    --jobs 6 --port-base 8700 \
    --serve "$ROOT/scripts/demos/merge-serve.sh" \
    --corridors data/scenarios/merge-pod/corridors.json \
    --out docs/show/reports/merge-pod.json > "$SCR/merge-rerun.txt" 2>&1

python3 scripts/demos/merge-quiz.py \
    --report docs/show/reports/merge-pod.json \
    --labels docs/show/labels-merge.json \
    --out docs/show/quiz/merge-pod.json

scripts/show/build-quiz.sh
echo "=== merge re-run done $(date +%H:%M:%S)"
