# Time Model

> How the engine advances time: fixed 100 ms authoritative tick with event-driven edges, async controller intents batch-applied at tick boundaries, and JetStream-backed deterministic replay — ratified by ADR-0005.

## Overview

The time model is the engine's most load-bearing design choice: it determines whether faster-than-realtime batch, realtime multiplayer with human drivers, and trustworthy replay can all come from one core. The research question was fixed-tick vs pure discrete-event (DES) vs hybrid. The survey of ~30 systems is unambiguous: **every microscopic simulator with continuous lane-level dynamics uses a fixed tick** (SUMO, Vissim, Aimsun micro, CARLA, and every authoritative game server); DES wins only where vehicles are reformulated to have sparse events (mesoscopic queue models), which sacrifices the lane-level fidelity the [vision](../../../VISION.md) requires.

The decided shape is a **hybrid: a fixed-tick authoritative core with an internal scheduled-event list and event-driven edges**. The engine advances in fixed 100 ms steps (10 Hz); scheduled events (signal phase changes, vehicle spawns, scenario triggers) fire at tick boundaries before the vehicle sweep — Aimsun micro's documented "event scheduling + activity scanning" pattern. Controllers are asynchronous NATS clients whose intents are buffered and batch-applied at the next tick boundary; the engine never waits for a controller. The tick count is the only clock inside the sim.

Replay is event sourcing on JetStream: periodic full-state keyframes plus a durable log of `(intent, applied_tick)` in the order the engine actually applied them, plus a rolling state CRC. Determinism is scoped honestly: same binary + same GOARCH + same recorded log → identical states, CRC-verified — replay determinism, not lockstep. All of this was ratified in [ADR-0005](../../decisions/ADR-0005-time-model.md).

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Fixed-tick core + scheduled-event list | raw/arch-time-model/implementation.md §3; [ADR-0005](../../decisions/ADR-0005-time-model.md) | Engine advances by fixed Δt; signal changes/spawns fire at tick boundaries before the vehicle sweep |
| Intent buffering & tick-boundary apply | raw/arch-time-model/implementation.md §9; [ADR-0005](../../decisions/ADR-0005-time-model.md) | NATS callbacks enqueue intents; engine batch-applies in deterministic order, never blocking the tick |
| Tick-as-clock & pacing drivers | raw/arch-time-model/implementation.md §5; [ADR-0005](../../decisions/ADR-0005-time-model.md) | `sim_time = tick × Δt`; unpaced / paced / stepped wrappers around one `Tick(n)` with no time syscalls |
| Replay: keyframes + arbitrated intent log + CRC | raw/arch-time-model/implementation.md §8; [ADR-0005](../../decisions/ADR-0005-time-model.md) | JetStream snapshots for seek; recorded applied-order log for re-simulation; CRC verifies reproduction |
| Determinism envelope & engine rules | raw/arch-time-model/implementation.md §6–7; [ADR-0005](../../decisions/ADR-0005-time-model.md) | Same binary + same GOARCH guarantee; single-writer goroutine, sorted slices, seeded per-concern RNG |
| Decoupled cadences (4 knobs) | raw/arch-time-model/implementation.md §4 | Physics step, controller decision cadence, snapshot publish rate, render FPS are independent |
| Message contract (tick-stamped planes) | [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) | Intents carry tick numbers, not wall clocks; record plane persists the arbitrated log |
| Controller latency contract | [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | A controller reacting to tick T influences T+1 at the earliest — 1-tick latency as reaction time |

## How It Works

### 1. Fixed tick, events at boundaries (ADR-0005)

The per-tick sequence (three-phase pattern, per Aimsun micro):

1. Fire scheduled events bound to this tick (signal phase changes, spawn schedules, scenario triggers), with explicit deterministic tie-breaking — order by (time, priority, stable sequence id).
2. Batch-apply buffered controller intents in the recorded deterministic order.
3. Sweep all vehicles (car-following / lane-change dynamics), then publish state.

**Why not pure DES:** car-following is dense continuous dynamics — if N vehicles all update every Δt, the future-event list degenerates into a priority-queue-mediated tick with O(log n) insert/pop per vehicle-update and no skipped time, strictly worse than an array sweep. The strongest field data point: MATSim's event-driven HERMES rewrite gained only ~2.5× on a 1M-agent scenario (3:33 vs 8:45 min/iteration) and had to **drop dynamic routing, within-day replanning, and traffic signals** — exactly the reactive features traffic-sim needs. We give up DES's sparse-time efficiency for near-empty networks; irrelevant, since congestion is the product.

**World tick: 100 ms (10 Hz).** The synthesis deferred the final number pending car-following stability analysis; the 2026-07-17 review resolved it via [ADR-0005](../../decisions/ADR-0005-time-model.md), validated by the traffic-flow-models research. Field calibration: Vissim DOT practice is 10 steps/s (WisDOT default, ODOT-mandated); Aimsun micro allows 0.1–1.5 s; SUMO validates its car-following models only up to 1 s steps. The tick length remains a scenario/config parameter with 100 ms as the validated default — don't hard-code it.

### 2. Never block the tick on controllers (ADR-0005, ADR-0008)

Controllers are async NATS clients; the engine never waits. Intents arriving between ticks are buffered and applied at the next boundary in deterministic order.

- **The measured trap:** SUMO TraCI's per-step client barrier costs 11× slowdown (90 s vs 8 s to retrieve positions on a 9,000-vehicle scenario); CARLA's synchronous mode ties sim rate to the slowest client. Both kill faster-than-realtime batch — a hard requirement for the planner-game use case.
- **The price:** an AI reacting to tick T influences T+1 at the earliest. This 1-tick control latency is physically defensible — a steering intent applying 20–50 ms late reads as human reaction time. Late/lost intents apply later; no rewind or lag compensation (that machinery exists for instant-hit ballistics, which traffic doesn't have).
- **Go idiom:** one goroutine owns world state; NATS subscription callbacks only enqueue intents (Nakama's match loop is the same design in Go: "messages are buffered... handed off as a batch" per tick).

### 3. Tick count IS the clock; pacing is a swappable driver (ADR-0005)

`sim_time = tick × Δt`, uint64 tick in every message, no wall clock or time syscalls in sim math. One step function `Tick(n)` is wrapped by interchangeable drivers:

- **Unpaced** — flat-out batch (Gymnasium `step()` semantics). Enables faster-than-realtime.
- **Paced** — sleep-until-deadline at 1× or k× wall time (Nakama/Ebiten pattern). Required when humans are in the loop: human intents are produced in wall time, so batch→paced mode switch on first human subscription is a driver-layer policy.
- **Stepped** — advance on external command (debugger, RL training).

**Overload policy:** let wall time slip and publish the current dilation scalar (EVE Online Time Dilation precedent — wall time stretches to a 10% floor, broadcast to clients). Never clamp the accumulator ("drop time") in an authoritative sim — that mitigation is for rendering only.

### 4. Replay = keyframes + arbitrated intent log + rolling CRC (ADR-0005, ADR-0006)

- The engine acts as arbiter (Factorio pattern): it logs `(intent, applied_tick)` in the order it actually applied them — log order, never goroutine-arrival order — plus periodic full-state snapshot keyframes and a rolling state CRC.
- Seek = `DeliverByStartSequence` from the nearest keyframe, re-simulate forward; the CRC verifies the replay reproduced history. JetStream `DeliverPolicy`/`ReplayPolicy` map 1:1, including broker-native `ReplayOriginal` realtime pacing.
- **Why hybrid:** pure input-log replay (Factorio) can't scrub and shatters on version changes (Factorio replays have no skip-ahead and no rewind — the cautionary tale); pure state-log replay (CARLA's recorder, ~11.6 Mbit/s measured by Fiedler for a small scene) is robust but huge and gives up re-execution entirely.
- Per ADR-0002's 2026-07-17 clarification and ADR-0006, the record plane on JetStream carries small messages only; the engine is its sole writer. Intents carry tick numbers, not wall clocks.
- **JetStream caveats:** sequence numbers are per-stream, not per-tick — the tick must ride in message headers/payload. Retention limits bound how far back a seek can reach, so snapshot cadence matters. `DeliverAll` = replay from genesis, `DeliverNew` = live tail, `DeliverLastPerSubject` = materialized view. HLA terminology maps cleanly: core NATS delivery is Receive Order; tick-stamped intents plus engine-side deterministic ordering amount to a poor-man's Timestamp Order with the tick as implicit 1-tick lookahead.

The **events-as-observations** pattern (MATSim) completes the picture: the public interface is a stream of tick-stamped event records emitted by the time-stepped core; analysis, scoring, and viz hang off that stream. How time advances and what the world observes are orthogonal — which is why the NATS message contract can be designed independently of loop internals.

### 5. Determinism envelope: same binary + same GOARCH, CRC-verified (ADR-0005)

A single-authority engine needs **replay determinism**, not lockstep — Dawson's easiest tier. Engine rules from day one:

- Single goroutine owns world state; fixed iteration order (sorted slices — **never Go map iteration**, whose order is deliberately randomized and is the #1 practical Go determinism bug).
- Seeded stream-per-concern RNG (SUMO pattern: decoupled MT19937 streams so loading vehicles doesn't perturb earlier behavior; SUMO default seed 23423); hash-based counter RNG — pure hash of (seed, edge, vehicle, step) — if we parallelize (sumo#10292 gold pattern).
- Integer tick clock; no wall clock in sim math; float seconds never accumulate.

**Cross-arch caveat:** Go's spec documents FMA fusion latitude, and the compiler pattern-matches FMA on arm64/ppc64/s390x/riscv64 but **never on amd64** — so un-fenced `x*y + z` diverges across arches (golang/go#71204). `math` transcendentals aren't bit-stable across arches either. An amd64 replay isn't guaranteed bit-exact on arm64 until we fence FMA (`float64(x*y) + z` or `math.FMA` everywhere), vendor transcendentals, or move state to int64 fixed-point (Go integer arithmetic is fully deterministic — a clean escape hatch). Acceptable in v1: civic-advocacy replays can pin an arch, and the CRC detects violations instead of silently corrupting conclusions. Note Factorio does **not** use fixed-point — doubles plus compiler discipline suffice.

### 6. Decoupled cadences (four independent knobs)

Proven separable by SUMO's `action-step-length`, CARLA's physics substepping (≤10 substeps of ≤10 ms), and Source's tick/cmdrate/updaterate split:

- **Physics integration step** — 100 ms default (above).
- **Controller decision cadence** — may be coarser than integration (SUMO switches to ballistic integration when decoupled).
- **Snapshot publish rate** — ~10/s target; Source runs 20/s snapshots against a 66.67 Hz tick.
- **Client render rate** — the engine never interpolates; the TS viz client holds a ~200–300 ms interpolation buffer between published snapshots (Fiedler's ≈3× send interval; Source `cl_interp` 100 ms at 20/s), with velocity in snapshots to enable Hermite interpolation.

## Gotchas

- **The TraCI trap**: blocking the tick on external clients is a measured 11× slowdown (SUMO) and ties sim rate to the slowest client (CARLA sync mode). The entire NATS intent design exists to avoid porting this failure to our bus.
- **MATSim's "event-based" is a myth**: QSim is time-stepped (1 s default); its famous events are telemetry *output*, not the scheduler. Events-as-observations over NATS is orthogonal to how time advances — don't confuse the two when reading prior art.
- **HERMES warning**: MATSim's event-driven rewrite cost exactly the reactive features we need (signals, replanning) for a ~2.5× speedup. Pure DES for dense dynamics is an anti-pattern with a body count.
- **Go map iteration in sim logic**: deliberately randomized order — the #1 practical Go determinism bug. Keep sorted slices.
- **FMA asymmetry**: Go fuses `x*y + z` on arm64 but never amd64, so cross-arch replays silently diverge unless fenced; `math.Sin/Cos/Exp/Pow` aren't bit-stable across arches either.
- **Variable timestep / wall clock / float time base**: all three break reproducibility — framerate-dependent dynamics (Fiedler's exploding springs), unreplayable runs, and drifting clocks. Integer ticks only.
- **Shared/global RNG**: SUMO's bug class; loading vehicles must not perturb existing dynamics. Stream-per-concern or hash-counter RNG.
- **The megapacket cascade**: Factorio clients recovering from lag replayed 400+ buffered inputs in one burst; fan-out to 200+ clients cascaded disconnects. The same shape exists for reconnecting NATS controllers — bound intent buffers and rate-limit reconnect bursts.
- **Sub-second intra-tick ordering is available but unused**: CS2 sub-tick (wall-stamped inputs resolved within a 64 Hz tick) is the pattern if two controllers ever contest the same gap within one tick. v1's batch-apply order is sufficient.
- **Accumulator clamping is rendering-only**: dropping time under overload corrupts an authoritative sim. Slip wall time and publish the dilation scalar instead.

## Open Questions

- **NATS round-trip and JetStream publish-ack budgets vs the 100 ms tick budget** — no prior art exists; benchmark during engine bring-up (localhost/compose).
- **JetStream publish-ack throughput persisting per-tick intent batches at high faster-than-realtime multipliers** — benchmark with the above.
- **Snapshot keyframe cadence** — size from measured re-sim speed once the loop exists (storage vs seek latency trade).
- **Cross-arch determinism upgrade path** (`math.FMA` everywhere vs int64 fixed-point state) — revisit only if civic-advocacy partners need heterogeneous verification.
- **Go GC pause jitter in paced fixed-tick loops at scale** — folklore only; benchmark.
- **Vissim multicore determinism policy and Aimsun's formal determinism guarantee** — paywalled/unpublished; nice-to-have calibration, not blockers.
- ~~Final tick length~~ — **RESOLVED 2026-07-17 review**: 100 ms (10 Hz) validated by car-following research and ratified in [ADR-0005](../../decisions/ADR-0005-time-model.md); still a config parameter, not a constant.

## Related

- [Traffic Flow Models (Microscopic)](../business-domains/traffic-flow-models.md) — IDM/Gipps stability analysis that validated the 100 ms tick; the dynamics that sweep every tick.
- [NATS Backbone](../architecture/nats-backbone.md) — the bus carrying async intents and the JetStream record plane this model depends on; tick numbers in every contract.
- [State Authority](../architecture/state-authority.md) — single-writer goroutine, snapshot/interpolation buffer, and the engine's arbiter role.
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — 1-tick control latency and intent timestamping as seen by controllers.
- [Scenario Format](../concepts/scenario-format.md) — (scenario, seed) as the run key; Vissim-style seed sweeps for batch comparison.
- [ADR Index](../decisions/adrs.md) — ADR-0005 ratified this article's positions; ADR-0002/0006 carry its message-contract consequences.

---
*Raw research: [raw/arch-time-model](../../raw/arch-time-model/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
