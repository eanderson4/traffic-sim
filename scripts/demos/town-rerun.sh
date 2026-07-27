#!/usr/bin/env bash
# Re-measure Bottleneck Town after signalising connector-south's four
# crossings of the cross streets, and regenerate everything downstream.
#
#   SCRATCH=<dir> scripts/demos/town-rerun.sh
#
# QUEUED BEHIND THE OTHER TWO BATCHES. `serve` reports uncontrolled coasting
# — the share of vehicle-ticks with no controller intent — and that figure is
# contention-sensitive: an earlier probe read 13% on a machine also running a
# browser against 0.07% on an idle one. Coasting is a fidelity gate these
# runs are judged against, so two A/B batches at once would each corrupt the
# other's verdict.
#
# EXPECT connector-south TO GET WORSE. It previously crossed four arterials
# unsignalised as the priority leg — every cross-street vehicle yielding to
# it — and now stops at four new fixed-time signals like everything else in
# this town. That is not a penalty invented for the arm; it is what the road
# would cost to build. If the option still places where it placed, the
# earlier result was right for the wrong reason; if it collapses, the earlier
# result was an artifact of the right-of-way grant.
#
# The whole pod is re-run rather than the one changed arm: a report stitched
# from two batches on two machine states cannot be checked for exactly the
# contention effect above.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD
SCR=${SCRATCH:?set SCRATCH to a working directory}
POD=data/pods/bottleneck-town

while pgrep -f "meter-sweep.sh|merge-rerun.sh" >/dev/null; do sleep 30; done
echo "=== merge work clear, re-running the town $(date +%H:%M:%S)"

python3 scripts/demos/bottleneck_town.py --out "$POD" --ticks 15000
python3 scripts/demos/bottleneck_town.py --out "$POD" --check \
    > "$SCR/town-routes.txt" 2>&1

# Same 10 seeds, ticks and warmup as the shipped report, so the new numbers
# are comparable to the old ones rather than merely both correct.
python3 scripts/whatif.py --pod "$POD" --baseline base \
    --seeds 10 --ticks 15000 --warmup 6000 --jobs 5 \
    --capacity 40000 --serve "$ROOT/engine/serve" --port-base 9200 \
    --out docs/show/reports/bottleneck-town.json > "$SCR/town-rerun.txt" 2>&1

python3 scripts/demos/town_quiz.py \
    --report docs/show/reports/bottleneck-town.json \
    --out docs/show/quiz/bottleneck-town.json

scripts/show/build-quiz.sh
echo "=== town re-run done $(date +%H:%M:%S)"
