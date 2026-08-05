#!/usr/bin/env bash
# `serve` for the merge scenario: the real binary with -exit-routing=false.
#
# Pass this to scripts/whatif.py as --serve so the A/B harness runs the arms
# the way the scenario is meant to run, WITHOUT patching a script shared with
# the other show scenarios.
#
# WHY -exit-routing=false. The driver's exit routing (ADR-0019) draws each
# vehicle a destination among the exit lanes REACHABLE THROUGH SUCCESSORS
# from its current lane (engine/natsio/driver/destinations.go). On a freeway
# every lane has exactly one successor, so the only exit reachable from
# mainline lane 0 is downstream lane 0 — every vehicle draws the exit lane it
# is already in, its lateral route depth is 0 everywhere, and `routeHopOK`
# (engine/mobil.go) then VETOES every discretionary lane change as a move
# away from the route. Measured on the ramp-off control: 1 lane change in a
# 12,000-tick run with routing on, 67 with it off. A freeway on which no one
# can overtake a truck is not a freeway, and the congestion it produces is
# route-pinning, not merging — the same run measures 45.7 km/h with routing
# on and 49.8 km/h with it off.
#
# Exit routing is right for a city network with many destinations. Here it
# degenerates, and turning it off is the honest configuration, not a
# convenience: nothing in this scenario needs per-vehicle destinations
# because there is one origin-destination pair. The one place a route IS
# needed — sending part of the ramp flow down the frontage road — is done in
# the demand file with ADR-0021 destination weights, which the kernel follows
# regardless of this flag.
set -euo pipefail
exec "$(cd "$(dirname "$0")/../.." && pwd)/engine/serve" "$@" -exit-routing=false
