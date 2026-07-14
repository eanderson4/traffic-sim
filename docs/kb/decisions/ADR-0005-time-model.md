# ADR-0005: Engine time model

- **Status:** PROPOSED — pending `arch-time-model` research
- **Date:** 2026-07-14

## Context

The engine must advance simulated time in a way that supports: continuous vehicle
dynamics (car-following at lane level), real-time human driver input over NATS,
deterministic replay (civic-advocacy use case), and eventual scale-out. Candidate
models:

1. **Fixed-tick authoritative loop** — engine steps at a fixed rate; controller
   events are inputs to the next tick (multiplayer game-server pattern; SUMO also
   steps fixed).
2. **Pure discrete-event** — state advances only on events (MATSim-style); scales
   for sparse traffic, awkward for continuous dynamics and real-time humans.
3. **Hybrid** — tick authority with event-driven edges, possibly variable tick rate
   or faster-than-realtime stepping for headless runs.

## Leading Hypothesis

Option 1/3: fixed tick as the single source of truth, controllers emit intents
asynchronously over NATS. Faster-than-realtime stepping needed for batch scenario
comparison; realtime pacing needed when humans participate.

## Decision

**Deferred.** To be finalized after `/research-topic arch-time-model` (compare
game-server prior art, SUMO, MATSim; analyze determinism + replay + human-input
requirements). Update this ADR to Accepted with the outcome.

## Consequences (of deferring)

- No engine loop code should be written until this is Accepted.
- Message contract design (`arch-nats-backbone`) should note where it depends on the
  outcome (e.g. whether intents carry timestamps vs tick numbers).
