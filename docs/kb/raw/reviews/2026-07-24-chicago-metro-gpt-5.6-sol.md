## Blocker

- `engine/natsio/driver/driver.go:310` — Exit routing only emits a turn when the destination is first assigned, then marks the vehicle `routed`. The engine consumes that turn at the next crossing and resets it (`engine/engine.go:428`), so every later junction takes the leftmost default. Additionally, `turnChoice` returns zero for a middle successor (`engine/natsio/driver/driver.go:376`), which also selects the first successor. **Failure scenario:** a Chicago vehicle whose path crosses multiple intersections follows the route at, at most, its first fork; straight/middle movements and subsequent forks go leftmost, so it generally never reaches its selected exit. The milestone’s core claim that exit routing prevents garbage circulation is therefore not implemented.

- `engine/natsio/driver/router.go:24` — The router is plain O(V²), despite the milestone’s networks containing roughly 15k–56k lanes, and initial routing calls it twice per vehicle: once at `engine/natsio/driver/driver.go:318` and again through `turnChoice`. **Failure scenario:** claiming a 1,000-vehicle cohort on chi-loop can require billions of lane comparisons before the observation callback catches up; paced runs fall behind and unpaced runs accumulate severe claim/intent lag. This directly undermines the proposed finite-pace scorecard workaround and likely contributes materially to the documented partially controlled runs.

## Should-Fix

- `engine/natsio/contract.go:340` — Setting `PaceFloor == 0` now disables controller liveness entirely. A driver that crashes during an unpaced run retains its claims forever, preventing another replica from reclaiming those vehicles and violating ADR-0008’s operational failover model. This is externally visible heartbeat/detachment semantics, but `contracts/asyncapi.yaml:566` and ADR-0008 were not updated. Either constrain batch mode to an enforced embedded-client lifecycle or ADR-gate and document the changed contract behavior.

- `engine/cmd/demosrv/main.go:47` — The viz defaults to `/overlay/{zones,boundaries,water}.geojson` via `viz/src/config.ts:28`, served from `data/networks/overlays`, but that directory and its artifacts do not exist in the committed milestone. The only committed zone source is `scripts/chicago/zones.geojson:1`, and no documented command installs it or generated boundary/water outputs into the served directory. A clean checkout therefore receives three 404s and renders none of the milestone’s overlays.

- `scripts/chicago/scorecard.py:24` — “Network totals” are reconstructed by summing every interval record instead of reading the authoritative top-level `totals` object provided by the metrics format (`engine/metricsjson.go:24`). Overlapping measurement sets double-count distance, time and loss; subset-only sets report subset values as network totals; omitted `stops` or `time_loss_s` groups raise `KeyError`. This will produce incorrect comparisons as soon as authored metric sets are used.

- `scripts/chicago/zones.geojson:12` — All three imported starter zones—north-lakefront, loop and Kennedy—remain marked `import-pending` (`scripts/chicago/zones.geojson:18`, `scripts/chicago/zones.geojson:30`), although the milestone article lists them as imported and scorecarded. Since the viz deliberately renders runnable zones prominently and pending zones muted, the source-of-truth metadata presents completed zones as unavailable.

## Verdict

**Not ready to bind as a milestone:** exit routing is functionally incorrect and computationally unsuitable for the Chicago networks. The contract and overlay-delivery inconsistencies should also be resolved or explicitly recorded before the next milestone. No files were modified.
