# Synthesis: Engine Time Model

> Researched: 2026-07-15 | Git HEAD: c7a1056 | Status: complete
> Feeds ADR-0005 (currently PROPOSED). This synthesis recommends; the ADR decides.

## Summary

The research question was fixed-tick vs pure discrete-event vs hybrid. The answer
from ~30 systems surveyed is unambiguous: **every simulator with continuous
lane-level dynamics uses a fixed tick** (SUMO, Vissim, Aimsun micro, CARLA, and
every authoritative game server), DES wins only where models are reformulated so
vehicles have sparse events (meso queue models — which sacrifice our lane-level
requirement), and the mature production shape is a **hybrid: fixed-tick
authoritative core with an internal scheduled-event list and event-driven edges**.
The vision doc's "leading hypothesis" survives contact with the evidence, with
sharpened specifics below.

## Source Files

- [Mechanics: patterns a time model is built from](./implementation.md)
- [Prior art survey](./competitors.md)
- [Standards, formalisms, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions (for ADR-0005)

### 1. Fixed-tick authoritative core; DES only as internal machinery
**Choice:** Engine advances by fixed Δt; a scheduled-event list (signal phase
changes, vehicle spawns, scenario triggers) fires at tick boundaries (three-phase
pattern, Aimsun micro's documented shape).
**Why:** Car-following = dense continuous dynamics; a pure DES future-event-list
degenerates into an O(log n)-overhead tick when every vehicle updates every Δt.
MATSim's event-driven HERMES rewrite gained only ~2.5× and had to drop traffic
signals and within-day replanning — the exact reactive features we need.
**Trade-off:** We give up DES's sparse-time efficiency for near-empty networks
(irrelevant: congestion is the product).
**Field context:** No counter-example exists among microscopic simulators
([competitors](./competitors.md)).

### 2. Never block the tick on controllers (the TraCI/CARLA trap)
**Choice:** Controllers are asynchronous NATS clients. Intents are buffered as
they arrive and batch-applied at the next tick boundary in a deterministic order.
The engine never waits for a controller.
**Why:** SUMO TraCI's per-step client barrier measures 11× slowdown; CARLA's
synchronous mode ties sim rate to the slowest client. Both kill
faster-than-realtime batch — a hard requirement.
**Trade-off:** An AI controller reacting to tick T influences tick T+1 at the
earliest (1-tick control latency). Physically defensible: it reads as reaction
time. Late/lost intents just apply later — no rewind/lag-compensation needed
(that machinery exists for instant-hit ballistics, which traffic doesn't have).
**Field context:** Nakama's match loop ("messages are buffered... handed off as a
batch" per tick) is the same design in Go
([implementation §9](./implementation.md)).

### 3. Tick count IS the clock; pacing is a swappable driver
**Choice:** `sim_time = tick × Δt`, uint64 tick in every message; no wall clock or
time syscalls inside the sim core. Wrappers: unpaced (batch, flat out), paced
(1× or k× wall time, sleep-until-deadline), stepped (debug/RL). Overload policy:
let wall-time slip and publish the current dilation scalar (EVE TiDi precedent).
**Why:** This one factoring gives faster-than-realtime batch, realtime multiplayer,
and deterministic replay from the same core (Gymnasium/Nakama/EVE triangulation).
Humans in the loop force paced mode; mode switch is driver policy.
**Trade-off:** None found in the literature; this is uniformly how it's done.

### 4. Replay = keyframe snapshots + arbitrated intent log on JetStream
**Choice:** The engine (Factorio-style arbiter) logs `(intent, applied_tick)` in
the order it actually applied them, plus periodic full-state snapshot keyframes
and a rolling state CRC. Seek = `DeliverByStartSequence` from nearest keyframe,
re-simulate forward; CRC verifies the replay reproduced history.
**Why:** Pure input-log replay (Factorio) can't scrub and shatters on version
changes; pure state-log replay (CARLA) is robust but huge and gives up
re-execution. The hybrid is the industry standard (event sourcing + snapshots).
JetStream's `DeliverPolicy`/`ReplayPolicy` (including broker-native
`ReplayOriginal` realtime pacing) map 1:1.
**Trade-off:** Snapshot cadence trades storage vs seek latency — size from
measured re-sim speed later.
**Field context:** [implementation §8](./implementation.md); this decision also
constrains `arch-nats-backbone` (intents carry tick numbers, not wall clocks —
answering the dependency flagged in ADR-0005's consequences).

### 5. Determinism envelope: replay determinism, same binary/arch, CRC-verified
**Choice:** Guarantee: same binary + same GOARCH + same recorded log → identical
states, verified by CRC. Do NOT promise cross-architecture bit-exactness in v1.
Engine rules from day one: single goroutine owns world state; fixed iteration
order (sorted slices, never Go map iteration); seeded stream-per-concern RNG
(SUMO pattern), hash-based counter RNG if we parallelize; no wall clock in sim
math; integer tick clock.
**Why:** Single-authority means we need replay determinism, not lockstep — the
easy tier. Go's cross-arch hazards are real but avoidable later (FMA fusion
differs amd64 vs arm64 per the Go spec's documented fusion latitude; `math`
transcendentals not bit-stable across arches).
**Trade-off:** A replay recorded on amd64 isn't guaranteed bit-exact on arm64
until we fence FMA/vendor math or move state to int64 fixed-point. Acceptable:
civic-advocacy replays can pin an arch; CRC detects violations rather than
letting them silently corrupt conclusions.
**Field context:** Factorio uses doubles + compiler discipline (fixed-point is a
myth); SUMO documents the identical libm hazard in C++
([implementation §7](./implementation.md)).

### 6. Tick rate: decouple the four cadences; world tick likely 10 Hz territory
**Choice (provisional):** Separate knobs for physics integration step, controller
decision/intent cadence, snapshot publish rate, and client render rate — proven
separable by SUMO action-step-length, CARLA substepping, and Source's
tick/cmdrate/updaterate split. World tick provisionally 100 ms (10 Hz): inside
Vissim DOT practice (10 steps/s) and Aimsun's 0.1–1.5 s range; snapshots ~10/s
with ~200–300 ms client-side interpolation buffer (velocity included for Hermite
interpolation).
**Why deferred in part:** The final tick length needs car-following stability
analysis from [[domain-traffic-flow-models]] (unresearched) — don't hard-code it
in the ADR; make it a scenario/config parameter with a validated default.

## Compare/Contrast: Us vs the Field

| Dimension | SUMO | MATSim | CARLA | Factorio | us (proposed) |
|---|---|---|---|---|---|
| Core | fixed 1 s | fixed 1 s queue | fixed 50 ms + substeps | fixed 16.7 ms lockstep | fixed ~100 ms + event list |
| External control | blocking barrier (TraCI) | none (batch) | blocking (sync mode) | inputs scheduled onto ticks | **async intents, tick-boundary apply** |
| Events | outputs (FCD etc.) | **outputs (the interface)** | sensors | input log | **outputs on NATS = the interface** |
| Replay | re-run | re-run | state log | input log, no scrub | **keyframes + arbitrated intent log** |
| Determinism | seeded, default-on | single-thread det. | sync+seed only | bit-exact CRC | same-binary/arch + CRC |
| Faster than realtime | yes (batch) | yes | limited by clients | no (paced 60 UPS) | yes (unpaced driver) |

## The Genuine Gap (again)

NATS-backed authoritative simulation loops are essentially undocumented — the only
written account found (EventStack GamingAPI) doesn't address tick loops. Combined
with the no-Go-CTM/LTM gap from [[domain-macroscopic-flow-models]], the
engineering-blog potential of this project keeps growing.

## Open Questions

- Final tick length → needs [[domain-traffic-flow-models]] (IDM/Gipps stability).
- NATS round-trip and JetStream publish-ack budgets vs tick budget → benchmark
  during engine bring-up (no prior art).
- Snapshot keyframe cadence → size from measured re-sim speed.
- Cross-arch determinism upgrade path (math.FMA everywhere? int64 fixed-point
  state?) → revisit if civic-advocacy partners need heterogeneous verification.
- Go GC pause jitter in paced loops → benchmark; folklore only.

## Connections to Other Topics

- **Decides:** ADR-0005 (time model) — this research fulfills its research gate.
- **Constrains:** [[arch-nats-backbone]] (tick numbers in message contracts;
  intent log + snapshot subjects; JetStream retention vs seek depth),
  [[arch-state-authority]] (single-writer goroutine; snapshot/interp buffer),
  [[concept-vehicle-controller-interface]] (1-tick control latency; intent
  timestamping), [[concept-scenario-format]] ((scenario, seed) as run key —
  Vissim seed-sweep practice).
- **Depends on:** [[domain-traffic-flow-models]] for the tick-length stability
  bound (only remaining blocker on the final number, not on the model choice).
- **Relates to:** [[domain-macroscopic-flow-models]] (Aimsun's FD-based
  micro↔meso boundary glue if we add an LTM preview layer).
