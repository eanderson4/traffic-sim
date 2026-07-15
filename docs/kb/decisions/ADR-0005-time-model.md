# ADR-0005: Engine time model

- **Status:** ACCEPTED
- **Date:** 2026-07-14 (proposed) / 2026-07-15 (accepted after `arch-time-model` research)

## Context

The engine must advance simulated time in a way that supports: continuous vehicle
dynamics (car-following at lane level), real-time human driver input over NATS,
deterministic replay (civic-advocacy use case), faster-than-realtime batch runs
(scenario comparison), and eventual scale-out. Candidates were fixed-tick loop,
pure discrete-event simulation (DES), and hybrid.

Research: `docs/kb/raw/arch-time-model/` (surveyed SUMO, MATSim, Aimsun, Vissim,
CARLA, Source, Overwatch, Factorio, AoE, EVE, Nakama, mosaik, DES/DEVS/HLA theory).
Key evidence: every simulator with continuous lane-level dynamics uses a fixed
tick; a pure-DES future-event-list degenerates into an expensive tick when all
vehicles update every Δt; MATSim's event-driven rewrite (HERMES) had to drop
traffic signals and replanning; SUMO's TraCI client barrier measures 11× slowdown
— blocking the tick on external clients kills faster-than-realtime.

## Decision

**Hybrid: fixed-tick authoritative core with an internal scheduled-event list and
event-driven edges.**

1. **Fixed tick.** The engine advances by a fixed Δt per tick. Sim time is
   `tick × Δt` with a uint64 tick counter — the tick count IS the clock. No wall
   clock or time syscalls inside the sim core.
2. **Internal event list.** Scheduled occurrences (signal phase changes, vehicle
   spawns, scenario triggers) live in an event queue processed at the start of
   each tick, deterministically ordered by (tick, priority, sequence). Three-phase
   pattern: events fire, then the vehicle sweep runs.
3. **Never block the tick on controllers.** Controllers are asynchronous NATS
   clients. Intents are buffered on arrival and batch-applied at the next tick
   boundary in a deterministic order; a controller reacting to tick T influences
   T+1 at the earliest (reads as reaction time). Late intents apply later; no
   rewind/lag compensation.
4. **Pacing is a swappable driver around `Tick()`**: unpaced (batch, flat out),
   paced (1× or k× wall time), stepped (debug/RL). Overload in paced mode → wall
   time slips and the engine publishes the current dilation scalar (EVE TiDi
   precedent). Batch mode is unavailable while human controllers are attached.
5. **Replay = keyframe snapshots + arbitrated intent log on JetStream.** The
   engine logs `(intent, applied_tick)` in the order it actually applied them,
   plus periodic full-state snapshots and a rolling state CRC. Seek = start from
   nearest keyframe, re-simulate forward; the CRC verifies reproduction.
6. **Determinism envelope: replay determinism** — same binary + same GOARCH +
   same recorded log ⇒ identical states, CRC-verified. Cross-architecture
   bit-exactness is explicitly NOT promised in v1 (Go FMA fusion differs by arch;
   `math` transcendentals are not bit-stable). Engine rules from day one: one
   goroutine owns world state; fixed iteration order (sorted slices — never Go
   map iteration in sim logic); seeded stream-per-concern RNG (hash/counter-based
   if parallelized); integer tick clock.
7. **Tick length is a scenario/config parameter, not a constant.** Default
   100 ms (10 Hz) — **validated** by `domain-traffic-flow-models` research:
   Kesting & Treiber show Δt = 0.1 s reproduces the exact continuous
   car-following dynamics, with a finite step acting like a reaction time
   T′_eff ≈ Δt/2 and stability boundary ≈ Δt + 2T′ = 2 s. Pair with the
   ballistic integrator + stopping override (Treiber & Kanagaraj: lane changes
   make all schemes order-1, RK4 worst per cost). 0.2–0.5 s is a defensible
   performance fallback; never above 1 s. Snapshot publish rate and client
   interpolation remain independent knobs (~10 snapshots/s, ~200–300 ms client
   buffer to start).

## Consequences

- Message contracts (`arch-nats-backbone`) carry **tick numbers, not wall-clock
  timestamps**, as the simulation time reference; wall clock appears only in edge
  metadata. Subjects must accommodate: intent log, snapshot keyframes, state CRC.
- Engine-loop code may now be written against this model.
- The intent log + snapshots must live in JetStream streams whose retention
  bounds seek depth — snapshot cadence to be sized from measured re-sim speed.
- Faster-than-realtime batch and realtime multiplayer are the same core with
  different drivers; scenario comparison runs use (scenario, seed) as the run key
  (Vissim seed-sweep practice).
- Revisit triggers: needing cross-arch replay verification (→ fence FMA / vendor
  math / int64 fixed-point state); tick-budget blowups at scale (→ partitioned
  engines with per-tick conservative sync, the MATSim/HLA pattern).
