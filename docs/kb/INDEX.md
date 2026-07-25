# Knowledge Base: traffic-sim

> NATS-based traffic simulation engine — real road networks, heterogeneous
> vehicle controllers (AI and human), decision-grade congestion metrics.

## How to Use This KB

This knowledge base is organized as independently researched topics.
- **Articles** (`articles/`) contain distilled, cross-referenced knowledge — start here
- **Raw research** (`raw/`) contains deep, source-attributed findings behind every article
- **Decision records** (`decisions/`) capture consequential choices and rationale
- Use `/research-topic [name]` to research individual topics
- Use `/research-topic --all` to research all pending topics
- Use `/distill-kb` to synthesize articles from raw research
- Use `/update-kb` to check freshness and refresh stale topics

The founding document is [`docs/VISION.md`](../VISION.md) — every topic here traces
back to it.

## Start Here

- [Summary](articles/summary.md) — project overview, architecture at a glance, reading paths
- [Cross-Topic Concerns](articles/cross-topic-concerns.md) — invariants, conventions, and gotchas spanning all topics
- [ADR Index](articles/decisions/adrs.md) — all accepted decisions with one-line rationales
- [Gaps & Roadmap](articles/gaps-and-roadmap.md) — benchmark queue, prototype experiments, open questions

## Articles

### Business Domains
- [Simulator Landscape](articles/business-domains/simulator-landscape.md) — 12 simulators in five camps; none combines lane-level dynamics, a live controller bus, decision-grade metrics, and verified replay
- [Traffic Flow Models (Microscopic)](articles/business-domains/traffic-flow-models.md) — IDM car-following + ballistic integration at 100 ms + MOBIL lane changing under a strategic layer; Newell as validation oracle
- [Macroscopic Flow Models](articles/business-domains/macroscopic-flow-models.md) — LWR theory as the engine's analytic examiner, calibrator, and metrics language; first Go CTM/LTM is greenfield OSS
- [Trajectory Datasets & Overhead Analysis](articles/business-domains/trajectory-datasets.md) — NGSIM, I-24 MOTION, drone sets, ring-road experiments as the calibration/validation corpus; Edie's definitions as the shared primitive
- [Congestion Metrics](articles/business-domains/congestion-metrics.md) — streaming trajectory-first metric kernel (Edie q/k/u + trip records) with a built-in multi-seed experiment protocol
- [Signal Control](articles/business-domains/signal-control.md) — external grants-based client role over a NEMA dual-ring engine model; the 100 ms tick matches NTCIP decisecond timers exactly

### Architecture
- [Time Model](articles/architecture/time-model.md) — fixed 100 ms authoritative tick, event-driven edges, async batch-applied intents, JetStream-backed deterministic replay (ADR-0005)
- [NATS Backbone](articles/architecture/nats-backbone.md) — three-plane split: core live / JetStream record / KV config (ADR-0002, ADR-0006)
- [Road Graph Model](articles/architecture/road-graph-model.md) — lane-as-atom multigraph, compiled conflict sets, internal lanes, `(laneId, s)` addressing, authoring ⇄ compiled duality
- [State Authority](articles/architecture/state-authority.md) — self-sufficient per-cell snapshots, declared interest windows, ego prediction, hold-last input buffers, no lag compensation

### Concepts
- [Vehicle & Controller Interface](articles/concepts/vehicle-controller-interface.md) — one 4-axis intent, declared observation windows, exclusive claims, always-on clamping, external default-driver fleet (ADR-0008)
- [Scenario Format](articles/concepts/scenario-format.md) — strict-YAML manifest-of-parts directory, kustomize-style overlay variants, (content-hash, seed) run identity

### Integrations
- [OSM Extraction](articles/integrations/osm-extraction.md) — seven-pass Go importer bootstrapped and oracle-checked by netconvert, defaults-first lane inference, provenance flags (ADR-0009)
- [MapLibre Realtime Viz](articles/integrations/maplibre-realtime.md) — three rate-split channels into MapLibre, binary SoA wire frames, measured deck.gl escalation ladder (ADR-0003)
- [Chicago Metro](articles/chicago-metro.md) — zoned Geofabrik→netconvert pipeline, portal-weighted napkin demand, driver exit-routing + serve attach barrier it required

### Decisions
- [ADR Index](articles/decisions/adrs.md) — ADR-0001..0018 table with rationales, research basis, and pending-ADR queue

## Raw Research

All source-attributed research files: [raw/](raw/) — 14 topics × 4 files
(implementation, competitors, standards-and-patterns, synthesis). Every article
links its source synthesis for traceability.

## Topic Registry

### Business Domains
- [x] [Simulator Landscape](raw/domain-simulator-landscape/synthesis.md) — SUMO, MATSim, CARLA, VISSIM: five camps, licensing map, our niche confirmed; recommends MIT/Apache-2.0 license ADR
- [x] [Traffic Flow Models (Microscopic)](raw/domain-traffic-flow-models/synthesis.md) — car-following (IDM, Gipps), lane-changing (MOBIL), gap acceptance, intersection right-of-way; validates the 100 ms tick + ballistic integrator
- [x] [Macroscopic Flow Models](raw/domain-macroscopic-flow-models/synthesis.md) — LWR kinematic-wave PDE, fundamental diagram, shockwaves/rarefactions, Cell Transmission Model; micro↔macro bridge and when macro beats micro
- [x] [Trajectory Datasets & Overhead Analysis](raw/domain-trajectory-datasets/synthesis.md) — real overhead traffic data (NGSIM, highD/inD/rounD drone sets, pNEUMA, I-24 MOTION, ring-road experiments); DIY capture; computing our own waves/FD from trajectories; sim validation targets for intersections and highways
- [x] [Congestion Metrics](raw/domain-congestion-metrics/synthesis.md) — FHWA MOE set + HCM Ch.24 trajectory state machines; recommends trajectory-first metric kernel, LOS as presentation skin, CRN experiment protocol
- [x] [Signal Control](raw/domain-signal-control/synthesis.md) — NEMA dual-ring as internal model; decisecond cabinet timing maps losslessly onto the 100 ms tick; phase changes are engine events

### Architecture
- [x] [Time Model](raw/arch-time-model/synthesis.md) — tick authority vs discrete-event vs hybrid; game-server prior art; determinism/replay implications → feeds ADR-0005
- [x] [NATS Backbone](raw/arch-nats-backbone/synthesis.md) — three-plane split (core live / JetStream record / KV config), OCC headers, subject taxonomy → feeds ADR-0006
- [x] [Road Graph Model](raw/arch-road-graph-model/synthesis.md) — lane-as-atom, compiled junction conflict sets + internal lanes, (laneId, s) occupancy, geometry-by-reference
- [x] [State Authority](raw/arch-state-authority/synthesis.md) — most-recent-state live plane (deltas on JetStream only), cell-based interest management, ego prediction + hold-last input buffer, no lag comp by the numbers; 8–16 B/vehicle wire anchor

### Concepts
- [x] [Vehicle & Controller Interface](raw/concept-vehicle-controller-interface/synthesis.md) — 4-axis intent vocabulary, SMARTS-style attach handshake, exclusive per-vehicle claims, fallback-to-IDM on disconnect
- [x] [Scenario Format](raw/concept-scenario-format/synthesis.md) — manifest-of-parts directory, strict YAML, kustomize-style overlay variants, metrics embedded in scenario

### Integrations
- [x] [OSM Extraction](raw/integration-osm-extraction/synthesis.md) — own Go importer (paulmach/osm) + netconvert bootstrap/diff-test oracle; defaults-first lane inference with `guessed` flags; durable IDs over OSM provenance; ODbL recipe-not-file posture
- [x] [MapLibre Realtime Viz](raw/integration-maplibre-realtime/synthesis.md) — three channels (load-once network / setFeatureState congestion / updateData vehicles), 4-rung escalation ladder to deck.gl, binary SoA frames over NATS; MapLibre ≥5.21.1 pinned

### Decisions
- [x] [ADR-0001](decisions/ADR-0001-go-engine.md) — Go for the engine core, TypeScript for visualization
- [x] [ADR-0002](decisions/ADR-0002-nats-backbone.md) — NATS as the sole inter-service backbone
- [x] [ADR-0003](decisions/ADR-0003-maplibre-vis.md) — MapLibre-first visualization, no UI frameworks by default
- [x] [ADR-0004](decisions/ADR-0004-local-first.md) — Local-first deployment via docker-compose
- [x] [ADR-0005](decisions/ADR-0005-time-model.md) — Hybrid time model: fixed-tick authoritative core, event-driven edges, tick-count clock, JetStream snapshot+intent-log replay
- [x] [ADR-0006](decisions/ADR-0006-nats-message-contract.md) — Three-plane message contract: core live (self-sufficient, binary SoA) / JetStream record (sole-writer intent log) / KV config; `{ns}.{run}.{plane}.>` taxonomy; AsyncAPI source of truth
- [x] [ADR-0007](decisions/ADR-0007-vehicle-model.md) — Position/gap conventions: front-bumper s, bumper-to-bumper gap, s0+length separate; multi-class vehicle types; IDM+MOBIL defaults; per-vehicle seeded RNG
- [x] [ADR-0008](decisions/ADR-0008-controller-contract.md) — 4-axis intents with per-axis persistence; grants-based roles (driver / default-driver / director / signal); exclusive claims; zero driving logic in engine, fleet failover + pause
- [x] [ADR-0009](decisions/ADR-0009-osm-import-strategy.md) — netconvert bootstrap + Go importer with permanent diff-test oracle; two-tier identity with guessed flags; ODbL recipe-not-file; delta-patch variants
- [x] [ADR-0010](decisions/ADR-0010-junction-right-of-way.md) — priority-junction right-of-way: compiled approach classes + conflict foes, kernel stop-line guardrail shared by all controllers; signals still unmodeled
- [x] [ADR-0011](decisions/ADR-0011-fixed-time-signals.md) — fixed-time signal control: kernel-run programs compiled from tlLogic, phase state as a pure function of the tick count, enforcement composed with the stop-line guardrail; external command interface deferred
- [x] [ADR-0012](decisions/ADR-0012-scenario-format.md) — scenario format: manifest-of-parts directory in strict YAML, runtime director demand sampling (M10 contract), kustomize-style overlay variants, (content-hash, seed) run identity, format_version migrations
- [x] [ADR-0013](decisions/ADR-0013-external-review-gate.md) — external multi-model review (Claude Fable + GPT-5.6-sol) as a pre-commit gate: tree-hash stamp proves review happened, triage stays the committer's job, fail-closed with a loud skip hatch
- [x] [ADR-0014](decisions/ADR-0014-observability-metrics.md) — observability: trajectory-first metric kernel (trip records incl. horizon partials, lane-interval Edie q/k/u), contract-pinned definitions, dedicated metrics JetStream stream + simrun file sink, scenario metrics bindings, paired-seed sweep protocol, LOS as presentation skin
- [x] [ADR-0015](decisions/ADR-0015-keyframe-chunking.md) — record-plane keyframes larger than 768 KiB are chunked into consecutive log messages (`kf_chunk: "i/n"` header); seek anchors on the last chunk; schema stays v2, old recordings read unchanged — unblocks city-scale past the 1 MiB max_payload wall hit at ~10.9k vehicles
- [x] [ADR-0016](decisions/ADR-0016-tssg-chunking.md) — live-plane signal tables (TSSG) chunked like ADR-0015 (`sig_chunk: "i/n"` header, per-chunk-valid frames, no `sig_gen`), 20-tick rebroadcast + request-reply catch-up on `ts.{run}.state.sig.req`, max_payload 64MB→4MB as documented headroom
- [x] [ADR-0017](decisions/ADR-0017-city-import-decisions.md) — city-scale OSM import decisions: no `--junctions.join` (stop override keys by OSM node id), `priority_stop` as whole-junction approximation, relations (turn restrictions) not imported, directionless stops infer from oneway else forward with a reported count
- [x] [ADR-0018](decisions/ADR-0018-chunked-geojson.md) — city-scale network GeoJSON served chunked over HTTP: manifest (`frame` + empty features + `parts`), hash+schema-pinned part URLs that 404 a stale generation, sequential client fetch; small nets byte-identical

---
*Last distilled: 2026-07-17 | 18 articles from 56 raw research files*
*ADR-0012 ratified 2026-07-21 (design; M11 implementation + review round landed same day)*
*ADR-0013 ratified 2026-07-21 (external-review workflow, following the M11 round)*
*ADR-0014 ratified 2026-07-21 (observability design; Fable+Sol review round, M13 implementation next)*
*ADR-0015 ratified 2026-07-24 (keyframe chunking, after the WQ-4 stress test hit the max_payload wall)*
*ADR-0016 ratified 2026-07-24 (TSSG chunking + pull resync, after the LA busy-tab slow-consumer incident; Fable+Sol design round settled)*
*ADR-0017 ratified 2026-07-24 (city import decisions, ep-03 six-metro imports)*
*ADR-0018 ratified 2026-07-24 (chunked network GeoJSON, after la-lean hit the V8 string cap)*
*Run `/update-kb` to check freshness*
