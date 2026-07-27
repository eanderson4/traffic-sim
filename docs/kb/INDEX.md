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
- [Silent Fidelity Failures](articles/concepts/silent-fidelity-failures.md) — seven ways a run reports a clean measurement of a scenario it never simulated, and the counter that catches each; four are city-scale, two (portal capacity drop, exit-routing lane pinning) bite at 44 lanes (ADR-0025), and the seventh is the A/B harness itself — CPU contention makes the driver miss deadlines, so coasting scales with concurrency rather than traffic and lands hardest on the arms that congest most

### Integrations
- [OSM Extraction](articles/integrations/osm-extraction.md) — seven-pass Go importer bootstrapped and oracle-checked by netconvert, defaults-first lane inference, provenance flags (ADR-0009)
- [MapLibre Realtime Viz](articles/integrations/maplibre-realtime.md) — three rate-split channels into MapLibre, binary SoA wire frames, measured deck.gl escalation ladder (ADR-0003)
- [Chicago Metro](articles/chicago-metro.md) — zoned Geofabrik→netconvert pipeline, portal-weighted napkin demand, driver exit-routing + serve attach barrier it required

### Decisions
- [ADR Index](articles/decisions/adrs.md) — ADR-0001..0022 table with rationales, research basis, and pending-ADR queue

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
- [x] [ADR-0019](decisions/ADR-0019-route-budget-determinism.md) — route-budget determinism bound: per-replica admission timing and mid-queue failover lane re-freeze accepted and bounded; strict fix retires the budget
- [ ] [ADR-0021](decisions/ADR-0021-od-demand-buildings.md) — PROPOSED: origin–destination demand — the route destination becomes a TRIP END (arrival despawn), `destination`/`offset_m` ride the spawn verb (omitempty, TSKF v4 only when used), interior injection is clearance-checked behind, and OD is anchored to OSM building floor area
- [x] [ADR-0020](decisions/ADR-0020-demosrv-public-deployment.md) — demosrv public deployment: -wspublic verbatim ws advertisement, -admintoken bearer gate on the mutating POSTs (GETs public), -autostart with keep-serving failure semantics, -nobuild prebuilt engine binaries; all default to local-dev behavior
- [ ] [ADR-0022](decisions/ADR-0022-us-urban-speed-typemap.md) — PROPOSED: US urban speed typemap — netconvert's German-derived OSM defaults gave unposted secondary/primary 100 km/h across all 25 US networks; an explicit speed-only typemap sets the 30 mph statutory urban default AND disables right-before-left (30 mph sits 0.2 m/s under netconvert's rbl threshold, which silently retyped 1,218 junctions to yield-to-the-right); joins the importer identity hash, region-scoped with the US map as the default
- [ ] [ADR-0024](decisions/ADR-0024-bounded-memory-replay-reader.md) — ACCEPTED (pending review): bounded-memory replay reading — the Player materialized the WHOLE record (`fetchFrom` from seq 1 plus a full per-tick index, both live at once), which is why a 30-minute chi-loop cut could not be opened; measured 9,973,269 intents vs 9,000 CRCs and 91 keyframes in a 15-minute recording, so intents are 99.9% of the record at ~1 per vehicle per tick. Replaced by a forward-only `logCursor` holding one tick at a time; the RECORD FORMAT is untouched, so every existing recording still replays. Also adds `Engine.DropIntentLog` / `serve -intent-log=false` (the flag earlier planning assumed existed, and did not)
- [x] [ADR-0026](decisions/ADR-0026-batched-intents.md) — batched intents (TSIB v1) on the live plane — one TSIB per cadence tick per controller (splitting into ⌈n/20,000⌉ bounded batches above the cap) on the unchanged intent subject, demuxed from per-vehicle v2 by the exact-case `intent_encoding` header; wire-only change (expand-at-ingest, identical downstream semantics), O(controllers) intent messages per tick instead of O(vehicles); M0–M3 (codec + engine ingest, driver per-tick aggregation, batch-mode measurement incl. applied-lag) landed before contract ratification
- [ ] [ADR-0027](decisions/ADR-0027-baked-trip-cards.md) — PROPOSED: baked trip cards — two new baked artifacts (`TSOD` origin/destination table, `TSRP` sharded travelled paths) so the viz can show an arrival/destination layer and answer "where did this car come from and where is it going" on a click. Baked, not live: a replay has no engine to ask, and `engine/vehicle.go:69` shows the kernel never materializes a route at all (`Route` is just the destination LANE ID), so what is baked is the path actually TRAVELLED — labelling it a "route" would invent a routing model this engine does not have. Reuses the TSRB quantization; no live-plane change, so the 4 MiB obs cliff is not approached from this direction

---
*Last distilled: 2026-07-17 | 18 articles from 56 raw research files*
*ADR-0012 ratified 2026-07-21 (design; M11 implementation + review round landed same day)*
*ADR-0013 ratified 2026-07-21 (external-review workflow, following the M11 round)*
*ADR-0014 ratified 2026-07-21 (observability design; Fable+Sol review round, M13 implementation next)*
*ADR-0015 ratified 2026-07-24 (keyframe chunking, after the WQ-4 stress test hit the max_payload wall)*
*ADR-0016 ratified 2026-07-24 (TSSG chunking + pull resync, after the LA busy-tab slow-consumer incident; Fable+Sol design round settled)*
*ADR-0017 ratified 2026-07-24 (city import decisions, ep-03 six-metro imports)*
*ADR-0018 ratified 2026-07-24 (chunked network GeoJSON, after la-lean hit the V8 string cap)*
*ADR-0019 ratified 2026-07-24 (route-budget determinism bound, after the city-scale obs-path stall)*
*ADR-0020 ratified 2026-07-25 (demosrv public deployment: GKE pod behind a 443-only TLS Ingress)*
*ADR-0021 PROPOSED 2026-07-25 (building-anchored OD demand for chi-loop; awaiting the external-review round)*
*ADR-0022 PROPOSED 2026-07-25 (US urban speed typemap; found while chasing chi-loop's collision hotspot — 79.7% of its lanes were ≥80 km/h. Review round: Kimi K3 + GPT-5.6-sol, which caught the right-before-left retyping the first draft missed)*
*ADR-0026 ratified 2026-07-26 (batched intents TSIB on the live plane; M0–M3 evidence in engine/BENCHMARKS.md §(d), contract published as asyncapi info version 2.5.0)*
*ADR-0027 PROPOSED 2026-07-27 (baked trip cards for the O/D layer and click-a-car; written after confirming the kernel has no materialized route to publish)*
*Run `/update-kb` to check freshness*
