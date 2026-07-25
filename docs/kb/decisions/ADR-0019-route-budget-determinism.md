# ADR-0019: Route-budget determinism bound (driver exit routing)

- **Status:** ACCEPTED
- **Date:** 2026-07-24

## Context

The default driver's exit draw (`pickExit`) needs per-lane reachability,
an O(network) scan per NEW origin lane (memoized in `exitCache`). On
city-scale networks (la-lean: 1.38M lanes) an unbounded spawn burst of
cold origins stalled the obs path — measured as minutes of 100% CPU with
no snapshots (2026-07-24, sf-lean 303k lanes). `Config.RouteBudgetPerTick`
(default 32) meters the draws: over-budget vehicles keep the kernel's
default routing and retry on the next observation.

External review (GPT-5.6-sol) flagged the budget's consequence for
ADR-0008's behaviorally-invisible failover:

1. **Admission timing is per-replica.** Each replica meters its own
   claim set, so a deferred vehicle's route lands up to
   ⌈unrouted/budget⌉ observations late (~0.3 sim-s at demo spawn rates).
   A fork passed in that window is traversed on default routing.
2. **Failover mid-queue loses the frozen lane.** `wantLane` (the
   first-observation lane that keeps the draw a pure function of
   (seed, id)) is process-local; a replacement driver re-freezes at ITS
   first observation and can draw a different exit.

## Decision

The bound is accepted and recorded rather than engineered away today:

- At demo scale the shard case is nominal: one driver at capacity
  50 000 sees the entire fleet (no partitioning), so admission timing is
  identical across replicas. The divergence requires BOTH
  capacity-overflow sharding AND a fork within ~0.3 sim-s of spawn —
  origins sit at the network edge, so the first junction is rarely
  within that window.
- Case 2 is a pre-existing limitation of ANY lane-input draw (failover
  before the FIRST route assignment), not of the budget; the budget
  only widens the window by ⌈unrouted/32⌉ observations. Once assigned,
  routes are persistent and round-trip through the obs echo — failover
  adoption is exact.
- The strict fix removes the budget: precompute reachability
  asynchronously instead of in the obs path — reverse BFS per exit,
  warmed lazily in the background with draws QUEUED (not dropped) until
  their origin's entry is ready, or per-origin forward BFS warmed at
  spawn-announce time (spawn ticks are known before first observation).
  Either preserves the weighted draw over per-lane reachable exits;
  neither fits this commit. When one lands, this ADR and
  `RouteBudgetPerTick` retire.

## Consequences

- `RouteBudgetPerTick` and its timing note stay in the driver config
  with a pointer here.
- The hello retry loop retries only `nats.ErrNoResponders` (the
  startup race it exists for): the attach hello is not idempotent —
  retrying a timeout can register a ghost controller holding claim
  capacity (review blocker, same round).
- `engine/natsio/driver/router.go` (an O(V²)-era unused Dijkstra) was
  deleted: routing lives engine-side in `engine/routing.go`.
