# Architecture Decision Records

> Index of the accepted ADRs with one-line rationales — the decided skeleton of the system; research that feeds each ADR is linked for the full argument.

## How to Read

ADRs are the binding decisions; KB articles explain the domain around them. Per
[AGENTS.md](../../../../AGENTS.md), decisions of consequence get an ADR, and message
contracts published on NATS subjects are sacred — changing a subject name or payload
schema requires an ADR and a migration note. ADR-0001..0004 were accepted at the
founding session (2026-07-14); ADR-0005..0009 were ratified after their research gates
closed (2026-07-15..17).

## Accepted ADRs

| ADR | Decision | One-line rationale | Research basis |
|---|---|---|---|
| [ADR-0001](../../decisions/ADR-0001-go-engine.md) | Go engine core, TypeScript visualization | Go aligns with the NATS ecosystem and keeps performance headroom; TS lives where the browser is; the message contract, not a language SDK, is the controller interface | [VISION.md](../../../VISION.md) planning session |
| [ADR-0002](../../decisions/ADR-0002-nats-backbone.md) | NATS (core / JetStream / KV) as the sole inter-service backbone | Pub/sub, durable streams, and KV map 1:1 onto live fan-out, replay recording, and shared config — every participant (human, AI, observer) is just a NATS client | [NATS Backbone](../architecture/nats-backbone.md); clarified 2026-07-17: whole NATS family incl. Object Store, small messages only on the hot path, no in-process controller fast path |
| [ADR-0003](../../decisions/ADR-0003-maplibre-vis.md) | MapLibre-first visualization, vanilla TS, no UI frameworks by default | The rendering needs are GIS-shaped (heatmaps on road geometry, animated vehicles); deck.gl is the pre-approved escalation path, adopted on measured need only | [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) (supplied the 4-rung escalation ladder the ADR deferred) |
| [ADR-0004](../../decisions/ADR-0004-local-first.md) | Local-first deployment via docker-compose | The first deliverable runs on one machine; zero hosting cost now, with the constraint that nothing may preclude hosted deployment later | [VISION.md](../../../VISION.md) planning session |
| [ADR-0005](../../decisions/ADR-0005-time-model.md) | Hybrid time model: fixed 100 ms authoritative tick, internal event list, event-driven edges | Every simulator with continuous lane-level dynamics uses a fixed tick; pure DES degenerates under dense dynamics; a per-step client barrier measures 11× slowdown — so intents are async, batch-applied, and the tick count is the clock | [Time Model](../architecture/time-model.md); 100 ms tick validated by [Traffic Flow Models](../business-domains/traffic-flow-models.md) |
| [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) | Three-plane message contract: core live (self-sufficient, binary SoA) / JetStream record (sole-writer intent log) / KV config; `{ns}.{run}.{plane}.>` taxonomy; AsyncAPI source of truth | Core NATS fan-out has no per-subscriber acks, so live messages must be self-sufficient; the engine alone writes the arbitrated record, and tick numbers live in payloads, never in subjects | [NATS Backbone](../architecture/nats-backbone.md), [State Authority](../architecture/state-authority.md), [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) |
| [ADR-0007](../../decisions/ADR-0007-vehicle-model.md) | Position = front-bumper `s`; gap = bumper-to-bumper everywhere; multi-class types; IDM+MOBIL defaults; per-vehicle seeded RNG | Unpinned position/gap conventions drift by a vehicle length (~5 m) across models, metrics, and importers; matching the IDM literature's convention lets calibrated parameters transfer without conversion | [Traffic Flow Models](../business-domains/traffic-flow-models.md), [Road Graph Model](../architecture/road-graph-model.md) |
| [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Controller contract: one 4-axis Intent with per-axis persistence; grants-based roles (driver / default-driver / director / signal); exclusive claims; zero driving logic in the engine | TraCI's blocking barrier and CARLA's sticky commands are the measured failure modes; failover is operational (external default-driver fleet + pause), never a hidden in-engine model | [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md), [State Authority](../architecture/state-authority.md), [Signal Control](../business-domains/signal-control.md) |
| [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md) | netconvert bootstrap + own Go importer with netconvert as permanent diff-test oracle; two-tier identity with `guessed` flags; ODbL recipe-not-file; delta-patch variants | Only three codebases do full OSM→lane-graph compilation and none is Go or reusable; lane tagging is too sparse (~0.7% `turn:lanes`) to be a foundation, so the oracle guards a 20-year re-implementation gap | [OSM Extraction](../integrations/osm-extraction.md) |
| [ADR-0010](../../decisions/ADR-0010-junction-right-of-way.md) | Priority-junction right-of-way: netimport compiles approach classes (major/minor/stop) and conflict foes (merge/crossing) from connection states + internal-lane geometry; the kernel enforces a stop-line guardrail in the shared accel path; signals stay unmodeled | Junction traversal was connection-following only and simultaneous arrivals overlapped at exit funnels (160+ collision observations on I-280 at corridor demand); enforcement belongs in the kernel so every controller inherits it | M7 implementation (engine/rightofway.go, netimport), [Road Graph Model](../architecture/road-graph-model.md) |
| [ADR-0011](../../decisions/ADR-0011-fixed-time-signals.md) | Fixed-time signal control: netimport compiles static tlLogic into data-driven programs (phases + per-link states); the light derives purely from the tick count; enforcement composes with the ADR-0010 stop-line guardrail (red holds, amber stops-if-able, green flows but box-checked); external command interface deferred | Signalized junctions were the last free-traversal gap; the phase representation must let external algorithms (ADR-0008 §5 cabinet vocabulary) command it later, so the program is data and the gate reads only `(program, link, tick)` | M8 implementation (engine/signal.go, netimport), [Signal Control](../business-domains/signal-control.md) |
| [ADR-0012](../../decisions/ADR-0012-scenario-format.md) | Scenario format: a directory with a strict-YAML manifest referencing network/demand/control/metrics parts; demand is layered primitives in sim seconds sampled at runtime by the M10 director; variants are kustomize-style overlays (addition-only, no templating, ADR-0009 network delta patches); run identity = (content-hash, seed); per-file `format_version` with the Kubernetes round-trip rule | Flags can't be diffed, overlaid, content-addressed, or bound to a recording — and the founding use case is baseline + N variants ranked by metrics; every surveyed sim has pieces of this, none has all of it | [Scenario Format](../concepts/scenario-format.md) (research gate closed 2026-07-17); design ratified 2026-07-21, M11 + review round landed same day |
| [ADR-0013](../../decisions/ADR-0013-external-review-gate.md) | External multi-model review as a pre-commit gate: Claude Fable + GPT-5.6-sol review every staged code diff; a tree-hash stamp proves review happened (fail-closed, loud skip hatch); Gemini joins milestone rounds; the gate reviews itself | The M11 post-implementation round caught two design-level defects and a dozen bugs self-review missed — the evidence that external review diversity earns its keep before durable bindings ship | M11 implementation + the 2026-07-21 review round ([reviews](../../raw/reviews/)) |

## Research Complete, ADR Pending

These areas have finished research (the gate) but no drafted ADR yet — see
[Gaps & Roadmap](../gaps-and-roadmap.md):

| Candidate ADR | Research basis | What it will pin |
|---|---|---|
| Network model | [Road Graph Model](../architecture/road-graph-model.md) | Lane-as-atom schema, compiled conflict sets, internal lanes, geometry-by-reference, file format duality |
| Observability / metric set | [Congestion Metrics](../business-domains/congestion-metrics.md) | Trajectory-first metric kernel, canonical MOE set, detector layer, experiment protocol (warmup, CRN, CIs) |
| Project license | [Simulator Landscape](../business-domains/simulator-landscape.md) | Permissive license choice (deferred 2026-07-17, leaning MIT); ODbL layering per ADR-0009 stands regardless |

## Consequences That Reach Across ADRs

- ADR-0005's tick-count clock constrains every contract: subjects and payloads carry
  tick numbers, never wall-clock (ADR-0006); demand times are sim seconds (scenario
  format); the tick doubles as the signal-coordination master clock (ADR-0008).
- ADR-0008's "zero driving logic in the engine" forces ADR-0007's per-vehicle seeded
  RNG — otherwise default-driver fleet failover would be visible in behavior.
- ADR-0002's no-fast-path clarification plus ADR-0005's determinism envelope are what
  make replay trustworthy enough for the civic-advocacy use case in
  [VISION.md](../../../VISION.md).

---
*Derived from: [decisions/](../../decisions/) ADR-0001..0013 and the raw research syntheses linked above*
