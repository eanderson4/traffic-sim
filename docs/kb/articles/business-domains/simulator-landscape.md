# Simulator Landscape

> Survey of 12 traffic simulators across five camps: none combines lane-level dynamics, a live multi-controller bus, decision-grade metrics, and verified replay — traffic-sim's exact target quadrant.

## Overview

This topic surveys the prior art — every simulator whose architecture, API, licensing, or community traffic-sim can steal from or be warned by. The field stratifies into five camps with almost no feature overlap: open batch toolboxes (SUMO, MATSim, BEAM, SimMobility), proprietary professional tools (Vissim, Aimsun), RL-first engines (CityFlow, Flow, MOSS), game-engine driving sims (CARLA), and civic/UX tools (A/B Street). Each camp optimizes a different corner of the design space, and each carries measured lessons — not conjectures — about what works.

The headline conclusion: **no existing system combines a lane-level validated engine, a live multi-controller message bus, decision-grade metrics, and trustworthy seekable replay** — every incumbent has at most two of those four. That unoccupied quadrant is precisely traffic-sim's mission (see [VISION](../../../VISION.md)). Every live control API in the field is either a bolted-on stepping RPC (TraCI, CARLA RPC) or an in-process library (libsumo, CityFlow, MOSS); the batch toolboxes have no live interface at all. A pub/sub-native engine with humans as first-class controllers is genuinely new territory.

The survey's second contribution is a set of measured failure modes — TraCI's 11× socket wall, MATSim's 15-year config sprawl, A/B Street's abandoned hand-rolled sim core, SimMobility's custom-license community failure — plus governance data showing that only institutional homes keep simulators alive past their founding papers. Time-model conclusions of these systems are settled separately in the [Time Model](../architecture/time-model.md) article (ADR-0005); this survey covers everything *else*: module decomposition, control APIs, metrics pipelines, scenario formats, replay machinery, performance tiers, licenses, and community dynamics.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Tool-suite decomposition | `raw/domain-simulator-landscape/implementation.md` §1 | SUMO's ~14 single-purpose apps over shared formats — the shape we already have as services over NATS |
| TraCI control-API evolution | `raw/domain-simulator-landscape/implementation.md` §2 | Socket → subscription → in-process; the 11× measured wall that ratified push-based contracts |
| MATSim events stream | `raw/domain-simulator-landscape/implementation.md` §3 | Immutable tick-stamped events consumed downstream — the metrics pipeline pattern (JetStream-native) |
| Scenario-as-directory | `raw/domain-simulator-landscape/implementation.md` §5 | Top config referencing typed text artifacts; diffable variants per VISION |
| Detector vocabulary | `raw/domain-simulator-landscape/implementation.md` §6 | SUMO's E1/E2/E3, tripinfo, queue, FCD — the artifact names traffic engineers already read |
| Replay mechanisms | `raw/domain-simulator-landscape/implementation.md` §7 | Re-run everywhere; state logs (CARLA) and checkpoints (MOSS) exist, seekable verified replay does not |
| Performance ladder | `raw/domain-simulator-landscape/implementation.md` §8 | SUMO ~10⁵ updates/s → CityFlow >20× → MOSS 88.9× GPU; sets our CPU-tier target |
| License taxonomy | `raw/domain-simulator-landscape/standards-and-patterns.md` | Permissive / weak copyleft / GPL / custom / proprietary — what is borrowable and what is read-only |
| Positioning matrix | `raw/domain-simulator-landscape/competitors.md` | 12-system table: language, license, control API, scale, replay, governance |
| Controller contract | [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Our ratified answer to the field's control-API lessons (grants-based roles over NATS) |

## How It Works

Eight positions emerged from the survey; each is stated with its evidence. Where a 2026-07-17 design review or ADR ratified/amended the recommendation, that decided position is given.

1. **Steal SUMO's tool-suite decomposition.** Engine, network import, demand generation, and metrics are separate small services; shared artifacts (road graph, demand, scenario) are the contracts, transported over NATS instead of XML files. SUMO's stated rationale — each tool "is smaller than a monolithic application… easier extension… faster data structures" — is the 25-year-validated version of VISION principle 3; MOSS independently reinvented it as a repo constellation in 2024. SUMO's admitted cost ("a little bit uncomfortable" file-glue UX) is avoided by our message-bus glue, at the price of running a broker ([ADR-0002](../../decisions/ADR-0002-nats-backbone.md), [ADR-0004](../../decisions/ADR-0004-local-first.md)).

2. **Push subscriptions, never per-vehicle polling.** Controllers consume pushed snapshot streams and emit intents asynchronously; no synchronous query-vehicle RPC exists in the hot path. Evidence: TraCI's per-step socket barrier measured **90 s vs 8 s on a ~9,000-vehicle scenario (11×)**; SUMO's own remedies (subscriptions, then libsumo/libtraci) are the documented escape sequence, and Flow built an RL framework *on* the slow transport and inherited the wall. The synthesis had floated a future in-process controller fast path speaking the same contract (the libsumo lesson); the **2026-07-17 review resolved this against the fast path** — [ADR-0002](../../decisions/ADR-0002-nats-backbone.md) was clarified: NATS is the sole backbone, small messages only on the hot path, no in-process controller fast path. The ratified contract is [ADR-0008](../../decisions/ADR-0008-controller-contract.md): one 4-axis Intent, exclusive per-vehicle claims, grants-based roles, batch-applied at tick boundaries per [ADR-0005](../../decisions/ADR-0005-time-model.md). Corollary from the field: third parties re-implement published stepping protocols (Veins wrote its own TraCI client) — so the message contract is versioned and AsyncAPI-documented ([ADR-0006](../../decisions/ADR-0006-nats-message-contract.md)); the contract outlives the transport.

3. **Metrics are stream consumers; adopt SUMO's detector vocabulary.** The engine emits an immutable tick-stamped event stream on JetStream (MATSim's events-as-observations pattern, which scales to 1M-agent runs without touching the engine); E1/E2/E3-style detectors, tripinfo, queue-length, and FCD are computed by downstream services, not the engine core. SUMO's output catalog is the vocabulary traffic engineers already read — adopting its names and semantics buys instant legitimacy. Cheap inline counters are allowed (SUMO's step-log precedent) without becoming the contract. This is ratified by [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md): the engine is sole writer of the record plane. See [Congestion Metrics](../business-domains/congestion-metrics.md).

4. **Scenario = directory of typed, text, diffable artifacts.** One top-level config referencing typed artifacts (network, demand, signals, metrics definitions) — the SUMO `.sumocfg` / CityFlow `config.json` shape — all text for v1 (MOSS's protobuf is the outlier, chosen for GPU ingest we don't have), versioned, with explicit defaults and a documented module list that resists MATSim's 15-year config-accretion failure. XML vs JSON vs YAML is a taste decision deferred to the [Scenario Format](../concepts/scenario-format.md) article.

5. **License: permissive (MIT/Apache-2.0) recommended; copyleft code is read-only.** Every post-2017 entrant that won the RL/benchmark community (CityFlow, Flow, MOSS, CARLA, A/B Street) chose permissive; copyleft on our engine would poison exactly the embedding/RL use cases we want. Borrow *code* only from MIT/Apache/BSD projects with attribution; treat SUMO (EPL-2.0, file-level copyleft) and MATSim/BEAM (GPL) as ideas-and-formats references; never invent a custom license (SimMobility warning). **This still needs its own ADR — none exists.** The ODbL recipe-not-file posture for compiled OSM networks ([ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md)) stands regardless, since it follows from OSM's license, not ours.

6. **First-run-in-minutes UX + docs-as-product from day one.** Target A/B Street's onboarding shape — "pick an OSM region, see traffic in minutes" (browser viz via MapLibre, [ADR-0003](../../decisions/ADR-0003-maplibre-vis.md)) — and treat docs and reproducible examples as deliverables: SUMO's wiki plus annual User Conference proceedings since 2013 and MATSim's open book correlate with 20–25-year survival, while the RL-boom engines stalled within ~6 years of their papers (Flow's last push 2024-07; CityFlow sporadic since 2019). A/B Street's 8.1k stars came from UX ambition, not model fidelity — but its hand-rolled DES was declared unmaintained in Sept 2022 ("endlessly hard"), which is why our validated IDM/MOBIL dynamics ([ADR-0007](../../decisions/ADR-0007-vehicle-model.md)) invert that risk.

7. **Performance posture: SUMO–CityFlow CPU tier, GPU as researched escape hatch.** No GPU dependency in v1. Design the single-writer Go core to the ~10⁵ vehicle-updates/s tier (SUMO parity: ~100k updates/s single-thread; FAQ range 80k–700k on desktop), measure against CityFlow's >20× claim, and publish the step-log equivalent (real-time factor, updates/s) from the start. VISION's scenarios live in the 10³–10⁵ vehicle range; MOSS proves an **84.09 Hz city-scale, 88.92× GPU** ceiling move exists if metro scale ever becomes the requirement — and shows scale alone produces neither a controller bus nor replay. GPU + our determinism envelope (same binary + same GOARCH, ADR-0005 §6) is an unverified combination: treat GPU as a separate future ADR with its own determinism research.

8. **Avoid by default: replanning loops, federation, game-engine coupling, land-use scope.** No MATSim-style between-iteration replanning (a different research program — HERMES hit 3:33 min/iteration for 1M agents by *dropping* signals and replanning); no RTI/ambassador co-simulation layer (MOSAIC exists to sync *separate* simulators — ADR-0005's single-authority model dodges HLA time management by design; if an external sim must be ingested, write a one-off NATS adapter); no game-engine rendering dependency (VISION non-goal, ADR-0003); no activity-based or land-use demand modeling (SimMobility's decade-scale scope sink) — start with OD/count-driven demand in SUMO's `dfrouter`/`od2trips` shape.

### Us vs the Field

Feature presence across the surveyed systems (full 12-system matrix in `competitors.md`):

| Capability | SUMO | MATSim | CARLA | CityFlow/MOSS | A/B Street | us (target) |
|---|---|---|---|---|---|---|
| Lane-level validated dynamics | yes | no (queue) | partial | yes | partial (unvalidated) | yes (IDM/MOBIL) |
| Live external controllers | TraCI (blocking) | none | RPC sync/async | in-proc Python | none | NATS async, batch-apply |
| Humans as controllers | no | no | ego-vehicle only | no | no | yes (multiplayer) |
| Decision-grade metrics | strong catalog | via events | weak | thin | weak | events → detectors |
| Seekable verified replay | re-run | re-run | state log | checkpoint/re-run | re-run | keyframes + intent log + CRC |
| OSM real-network import | yes | partial | no | via converters | best in class | planned (ADR-0009) |
| License | EPL-2.0 | GPL | MIT (+UE EULA) | Apache/MIT | Apache-2.0 | TBD → permissive |

Three holes nobody covers — the gaps traffic-sim is built into:

1. **Message-bus-native lane-level simulation doesn't exist.** Every live control API is a bolted-on stepping RPC (TraCI, CARLA RPC) or an in-process library (libsumo, CityFlow, MOSS); the batch toolboxes have no live interface at all.
2. **Scrubbable, verified replay is unbuilt.** Re-run is the universal default; CARLA records states without re-execution; MOSS added bare GPU checkpoints only in v1.1. Nobody ships ADR-0005's keyframes + arbitrated intent log + rolling CRC — the exact artifact the civic-advocacy use case needs.
3. **Governance longevity is the scarcest resource.** Only institutional homes (DLR/Eclipse, TUB/ETH, Fraunhofer, LBNL) kept sims alive past their founding papers; the permissively-licensed RL engines all stalled when their labs moved on.

### Empirical Anchors

Hard numbers from the field, worth keeping at hand when sizing our own engine:

- CPU microsim speed ladder: SUMO ~10⁵ vehicle-updates/s single-thread → CityFlow >20× → MOSS 88.92× on GPU (84.09 Hz city-scale).
- Batch agent-sim pace: 1M agents ≈ 3:33 min/iteration (MATSim/HERMES); 60k agents/day ≈ 15–60 min (BEAM).
- Control-API overhead: 90 s vs 8 s on 9k vehicles (TraCI) — the single most decision-relevant number in the survey.
- Community size (GitHub, 2026-07-16): CARLA 14.2k★, A/B Street 8.1k★, Flow 1.2k★ (stale), CityFlow 1.0k★ (slow), MATSim 0.6k★, MOSAIC 113★.
- Replication practice: ~10 seed runs averaged per scenario (US DOT guidance); SUMO's `--output-prefix` exists for exactly this.

Two more field patterns are worth keeping in the pocket:

- **Replication seed sweeps**: (scenario, seed) as the run key; DOT practice is ~10 seed runs averaged — the field's standard methodology, already in ADR-0005's consequences (per-vehicle seeded RNG is ADR-0007).
- **Two-tier extensibility**: the field separates *observe/influence* APIs (TraCI, Aimsun AAPI) from *replace-the-model* APIs (Aimsun microSDK, Vissim DriverModel.dll). Our controller interface covers the first by design; the second is kept open as per-vehicle-type model selection in scenario config, not a plugin system.

### Interop Standards Actually in Force

- **ASAM OpenDRIVE** — the road-network exchange format: CARLA's native map format, importable by `netconvert`, parsed by OpenTrafficSim. Our road graph doesn't need to *be* OpenDRIVE, but import compatibility is cheap credibility ([Road Graph Model](../architecture/road-graph-model.md)).
- **ASAM OpenSCENARIO** — the maneuver/behavior counterpart to OpenDRIVE's map. The field already separates *network* from *demand/behavior* artifacts; our scenario directory follows that split.
- **TraCI as de-facto protocol** — independently re-implemented by Veins, MOSAIC, and CARLA. Any stepping API we publish becomes a protocol others re-implement: version the contract, not the transport (ADR-0006's AsyncAPI source of truth).
- **SUMO's XML formats as gravity well** — 25 years of tutorials made `.net.xml`/`.rou.xml` the de-facto scenario interop; MOSS ships a SUMO converter as a headline feature. Steal concepts freely; weigh import compatibility deliberately (Open Questions below).
- **GTFS** — the standard if transit demand ever enters (BEAM's R5 router consumes it); out of v1 scope per VISION non-goals.

## Gotchas

- **Blocking the tick on external clients**: TraCI's barrier measured 11× (90 s vs 8 s on 9k vehicles); CARLA's sync mode is the same trap. Every successor (libsumo, CityFlow, MOSS) exists largely to escape it — ADR-0005 §3 decided against it before the first line of code.
- **Config sprawl**: MATSim's XML accreted per-module sections for 15+ years as every contrib added knobs. Counter-pattern: versioned, minimal, scenario-scoped config with explicit defaults — enforced at review, not by tooling.
- **Hand-rolled unvalidated models**: A/B Street's bespoke DES ("not based on any research papers") became the unmaintainable part and was abandoned in 2022 while the tooling around it thrived. Validated dynamics (IDM+MOBIL, ADR-0007) are the inversion.
- **RL-wrapper-over-slow-API**: Flow over TraCI inherits the 11× wall no matter how good the RL library is; faster-than-realtime batch belongs in the engine (unpaced driver, ADR-0005 §4), not in a wrapper.
- **Inventing a license**: SimMobility's custom "Version Control License" correlates with a niche, closed-feeling community despite MIT backing — license ambiguity itself is the barrier.
- **Federation for its own sake**: an RTI/ambassador stack re-imports HLA time-management complexity that a single-authority engine deliberately avoids; federation is what you build when the monolith can't be opened.
- **Contracts outlive transports**: Veins re-implemented TraCI from scratch; CARLA and MOSAIC both couple through it. Any API we publish becomes a protocol others re-implement — hence versioned AsyncAPI contracts (ADR-0006), not README conventions.
- **Governance is the scarcest resource**: only institutional homes (DLR/Eclipse, TUB/ETH, Fraunhofer, LBNL) kept sims alive past their founding papers; every permissively-licensed RL engine stalled when its lab moved on. Docs, examples, and conference-grade write-ups are survival infrastructure, not polish.

## Open Questions

- **Our own license choice** — deferred at the 2026-07-17 review (not gating anything yet; leaning MIT for maximum adoption simplicity over Apache-2.0's patent grant). Needs a dedicated ADR when the repo is published.
- **SUMO `.net.xml` import compatibility** — adopt the field's interop gravity well (MOSS ships a SUMO converter as a headline feature) or stay clean? Deferred to [Road Graph Model](../architecture/road-graph-model.md) + [OSM Extraction](../integrations/osm-extraction.md); ADR-0009 chose netconvert bootstrap + own importer, which keeps the question live for the differential-testing oracle.
- **MATSim config-sprawl quantification** — the module-accretion claim is qualitatively sourced (the book), but an exact module count needs a doxygen crawl.
- **MOSS determinism across GPU architectures** vs our same-binary/same-GOARCH replay envelope — undocumented upstream; relevant only if the GPU escape hatch (position #7) is ever taken.
- **Vissim/Aimsun internal decomposition** — proprietary; their public API surfaces (COM, AAPI, microSDK) are documented but internals remain inference only. Flag where inference stops.

## Related

- [Traffic Flow Models (Microscopic)](../business-domains/traffic-flow-models.md) — the validated IDM/MOBIL dynamics that invert A/B Street's unvalidated-core failure
- [Congestion Metrics](../business-domains/congestion-metrics.md) — adopts SUMO's E1/E2/E3 detector vocabulary over the MATSim events-stream pipeline
- [Time Model](../architecture/time-model.md) — ADR-0005's tick, async-intent, and replay design; this survey independently confirms each against field practice
- [NATS Backbone](../architecture/nats-backbone.md) — the push-subscription transport that answers TraCI's measured 11× wall
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — ADR-0008's grants-based contract, ratifying this survey's subscription-over-polling position
- [Scenario Format](../concepts/scenario-format.md) — the scenario-as-directory design with MATSim config sprawl as its anti-pattern

---
*Raw research: [raw/domain-simulator-landscape/](../../raw/domain-simulator-landscape/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
