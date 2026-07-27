# Cross-Topic Concerns

> Patterns, invariants, and conventions that span multiple areas of the traffic-sim knowledge base.

## Architectural Invariants

- **The tick count is the only clock.** Sim time is `tick × Δt` (default 100 ms, validated); wall clock appears only in edge metadata. Spans [Time Model](architecture/time-model.md) (ADR-0005), [NATS Backbone](architecture/nats-backbone.md) (tick in payloads, never subjects or stream sequences), [Signal Control](business-domains/signal-control.md) (tick count doubles as the coordination master clock — designing out real-world clock-drift failures), [Scenario Format](concepts/scenario-format.md) (demand times in sim seconds), [Congestion Metrics](business-domains/congestion-metrics.md) (tick-stamped intervals). Why it matters: this one factoring is what makes replay, CRN experiments, and batch-vs-realtime duality possible at all.

- **Never block the tick on external clients.** Controllers are async; intents buffer and batch-apply at tick boundaries; slow consumers are dropped and resync, never waited on. Spans Time Model, NATS Backbone, [State Authority](architecture/state-authority.md), [Vehicle & Controller Interface](concepts/vehicle-controller-interface.md), Signal Control. The measured reason (TraCI's 11× wall, CARLA sync mode) recurs in five independent research topics — it is the project's most-cited failure mode.

- **The engine is the authoritative single writer; controllers only emit intents.** One goroutine owns world state; the engine is the sole publisher on the JetStream record plane; physics-bypassing verbs exist only on the director channel; safety clamps (vehicle intent guards, signal MMU invariants) are engine-side and non-negotiable. Spans State Authority, NATS Backbone (ADR-0006), Vehicle & Controller Interface (ADR-0008), Signal Control.

- **Zero driving logic in the engine — failover is operational, not simulative.** Every vehicle is always driven via the contract; orphans bridge on hold-last and are re-claimed by the supervised default-driver fleet; the run pauses when claim capacity < demand. Spans Vehicle & Controller Interface, State Authority, Signal Control (same external-role pattern), Scenario Format (demand director is also an elevated-grants client).

- **Determinism is a day-one discipline, not a feature.** Fixed iteration order (sorted slices, never Go map iteration), seeded stream-per-concern RNG keyed per vehicle, no wall clock or time syscalls in the core, integer tick clock, CRC-verified replay. Spans Time Model (ADR-0005), [Traffic Flow Models](business-domains/traffic-flow-models.md) (ADR-0007 per-vehicle streams), Congestion Metrics (CRN paired seeds depend on it), Scenario Format ((content-hash, seed) run key), Vehicle & Controller Interface (deterministic tie-break lists — never arrival-time ordering, which would leak wall-clock into replay).

- **`(laneId, s)` is the universal address; bumper-to-bumper gap the universal distance.** Vehicle addressing, occupancy, metrics cells, interpolation paths, and scenario files all share one position/gap semantics (ADR-0007). Spans [Road Graph Model](architecture/road-graph-model.md), Traffic Flow Models, State Authority, Congestion Metrics, [MapLibre Realtime Viz](integrations/maplibre-realtime.md) (feature-state joins keyed on the same stable segment IDs).

- **Live state is self-sufficient; deltas exist only where acks exist.** Most-Recent-State semantics on core NATS (loss reduces rate, never corrupts); keyframe+delta confined to the JetStream record plane. Spans NATS Backbone, State Authority, MapLibre Realtime Viz. Derived from one fact: ack-less fan-out makes a dropped delta poison every subsequent one.

## Shared Conventions

- **"Research recommends; the ADR decides."** Every synthesis header states it; the 2026-07-17 design review ratified eight of fourteen topics into ADR-0005..0009. Articles are written in the decided present tense where an ADR exists.

- **Measured numbers or it didn't happen.** Every recommendation cites a measurement (11× TraCI barrier, 0.7% `turn:lanes` coverage, 8–16 B/vehicle, ~18 km/h wave speed); where no published measurement exists, the recorded conclusion is "benchmark at bring-up," never a guess. Observed in all 14 topics — see the benchmark queue in [Gaps & Roadmap](gaps-and-roadmap.md).

- **Flag, don't hide: heuristic inference is always auditable.** `guessed` flags + structured import reports ([OSM Extraction](integrations/osm-extraction.md)), SUMO's blocking taxonomy republished as deadlock telemetry (Traffic Flow Models), applied-vs-requested clamp echoes (Vehicle & Controller Interface), `from-tag` vs `from-default` provenance. The civic-advocacy use case is the reason: every inferred fact must be findable.

- **Every subsystem ships with a validation oracle.** LWR/LTM analytic ground truth ([Macroscopic Flow Models](business-domains/macroscopic-flow-models.md)), the Newell controller as micro↔macro bridge test (Traffic Flow Models), the Sugiyama ring as string-instability acceptance test ([Trajectory Datasets](business-domains/trajectory-datasets.md)), netconvert as permanent differential oracle (OSM Extraction), ATSPM-comparable termination telemetry (Signal Control), one Edie implementation serving real data and sim output identically (Congestion Metrics).

- **Text artifacts for humans, compiled artifacts for machines.** Scenario YAML ⇄ compiled pack; network authoring format ⇄ compiled internal form; junction right-of-way compiled to conflict sets at build time; timing plans authored in practitioner form, compiled to executor form at load. The compiled form is never hand-edited; derived artifacts always recompile from source.

- **Game-netcode vocabulary, transferred wholesale.** Snapshot interpolation (200–300 ms buffer), hold-last input buffers, interest management compiled to broker subjects, time-dilation scalar (EVE TiDi), most-recent-state semantics, replay = keyframes + input log (Factorio/event-sourcing). Observed in Time Model, State Authority, NATS Backbone, MapLibre Realtime Viz — the games canon is this project's most consistent outside reference frame.

## Recurring Gotchas

- **The TraCI trap: any synchronous per-step coupling kills faster-than-realtime.** Measured 90 s vs 8 s on 9k vehicles; reappears as CARLA's sync mode and as the reason metrics, signal control, and demand are all stream consumers/clients rather than in-engine hooks. Affects: Time Model, NATS Backbone, Vehicle & Controller Interface, Simulator Landscape, State Authority.

- **Arrival-time ordering leaks nondeterminism into replay.** Same-tick competing intents must resolve by a deterministic tie-break list (grant level, then vehicle ID), never by message arrival — CS2-style arrival resolution would make replay diverge from the live run. Affects: State Authority, Vehicle & Controller Interface (ADR-0008).

- **Sticky last-control is a documented hazard, not an edge case.** CARLA vehicles holding their final command; our contract specifies per-axis persistence explicitly (accel one-shot, setpoint persistent, …) with hold-last capped at 1–2 ticks as transport healing only. Affects: Vehicle & Controller Interface, State Authority.

- **JSON is the viz wall, not the GPU.** MapLibre re-stringifies/re-parses whole sources per update; binary SoA frames on the wire from day one even when the first client is MapLibre-only. Affects: MapLibre Realtime Viz, NATS Backbone (ADR-0006), State Authority (wire-size derivation).

- **OSM data is sparser and dirtier than it looks.** 0.7% lane-tag coverage, conflicting tags in the wild, ID churn on splits, nothing clips at borders. Every import heuristic must be fail-soft with flags. Affects: OSM Extraction, Road Graph Model, Signal Control (timing plans provably absent → generator is load-bearing), Scenario Format.

- **Single runs near capacity vary 25%+.** Ranking alternatives on one run is noise; the experiment protocol (warmup detection, paired CRN seeds, confidence intervals, median-run showcase) is built into the metrics layer, not left to users. Affects: Congestion Metrics, Scenario Format, Time Model.

- **Config sprawl is the 15-year failure mode.** MATSim's module accretion and TraCI's silent protocol renumbering are why contracts are add-only, versioned, and declared in one AsyncAPI document, and why the scenario surface is deliberately small. Affects: Simulator Landscape, NATS Backbone, Vehicle & Controller Interface, Scenario Format.

- **Silent publish failures hide fidelity failures (2026-07-26, ADR-0026 M3 hardening notes — recorded, NOT yet fixed).** (a) `publishObs` (engine/natsio/contract.go) swallows obs publish errors — `obsOut` counts successes only. A 5,000-vehicle obs frame (~1.96 MB with the policy-ctx feature) silently never published under the test broker's then-default 1 MB `max_payload`, stalling the driver with zero log lines; found via the M3 applied-lag harness (the test broker now mirrors the production 4 MiB cap). Recommendation: count obs publish failures loudly, like the snapshot/signal paths' first-3-per-run logging. (b) `NewBus`/`NewContract` subscribe without flushing — a latent subscribe/publish race: a hello or request published from another connection can beat the server-side SUB registration and fast-fail `nats: no responders`, the M1-era explanation for the load-sensitive test flakes (`TestHoldLastThenDecay` and friends under load). New tests work around it with an explicit `nc.Flush()` after construction; the shared harnesses still carry the race. Affects: NATS Backbone, Vehicle & Controller Interface.

## Reading Order

For newcomers, understand topics in this order:

1. [VISION.md](../../VISION.md) → [KB Summary](summary.md) — mission, use cases, and the map of everything else.
2. [Simulator Landscape](business-domains/simulator-landscape.md) — the field's vocabulary and why our quadrant is empty.
3. [Time Model](architecture/time-model.md) (ADR-0005) — the keystone every other topic cites.
4. [Traffic Flow Models](business-domains/traffic-flow-models.md) + [Macroscopic Flow Models](business-domains/macroscopic-flow-models.md) — the physics the engine implements and its analytic examiner.
5. [NATS Backbone](architecture/nats-backbone.md) → [State Authority](architecture/state-authority.md) — how state and control flow (ADR-0006).
6. [Vehicle & Controller Interface](concepts/vehicle-controller-interface.md) (ADR-0008) — the central contract.
7. [Road Graph Model](architecture/road-graph-model.md) — the world representation.
8. Then whatever is relevant to your task: [Signal Control](business-domains/signal-control.md), [Congestion Metrics](business-domains/congestion-metrics.md), [Scenario Format](concepts/scenario-format.md), [OSM Extraction](integrations/osm-extraction.md), [MapLibre Realtime Viz](integrations/maplibre-realtime.md), [Trajectory Datasets](business-domains/trajectory-datasets.md).

## Inconsistencies

- **Signal control location flipped at review.** The signal-control synthesis recommended an engine-internal module ("NEVER an external NATS controller"); the 2026-07-17 review made signal controllers external grants-based clients (ADR-0008), keeping only safety invariants engine-side. Resolution: the ADR wins — it keeps "zero driving logic in the engine" uniform across all four Intent axes, and mirrors how real cabinets separate policy from the MMU. The article reflects the decided position.
- **Vehicle fallback moved out of the engine.** The controller-interface synthesis sketched an in-engine IDM fallback / MRM brake-to-stop; the same review replaced it with hold-last bridging + the external default-driver fleet (pause-on-capacity-loss). Resolved — recorded in the topic's own open-questions log as superseding notes.
- **In-process controller fast path: recommended vs ruled out.** Simulator Landscape recommends a libsumo-style in-process controller speaking the identical contract for future RL training; ADR-0002's 2026-07-17 clarification rules out any in-process fast path (the engine contains zero driving logic; every vehicle is driven via the contract). Latent tension, deferred: revisit when the RL use case lands — a loopback transport honoring the same contract may satisfy both.
- **Demand generation moved from engine to director.** The scenario-format synthesis recommended engine-side flows; the review made a runtime demand director (elevated-grants client) the sampler, with an offline reviewable-table mode. Consistent now, but note the engine's demand surface is thinner than the raw scenario research assumed.
- **Category naming.** `.kb-meta.json` groups domain topics as `business-domains`; the distill skill's generic template suggests other category names. Cosmetic only — articles follow the meta taxonomy.

---

*Derived from: all 14 raw research syntheses in [raw/](../raw/) (domain-simulator-landscape, domain-traffic-flow-models, domain-macroscopic-flow-models, domain-trajectory-datasets, domain-congestion-metrics, domain-signal-control, arch-time-model, arch-nats-backbone, arch-road-graph-model, arch-state-authority, concept-vehicle-controller-interface, concept-scenario-format, integration-osm-extraction, integration-maplibre-realtime) and ADR-0001..0009 in [decisions/](../decisions/)*
