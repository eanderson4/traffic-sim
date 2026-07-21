# Knowledge Base Gaps & Roadmap

> Areas that need more research, measurements that must happen at bring-up, and suggested next work. Everything marked "RESOLVED 2026-07-17 review" in the raw research is excluded here — this is only what remains genuinely open.

## Pending ADRs (research complete, decision unwritten)

| Candidate ADR | Gate | Blocks |
|---|---|---|
| Network model | [Road Graph Model](architecture/road-graph-model.md) | Engine world-state schema, compiled network format, importer target |
| Observability / metric set | [Congestion Metrics](business-domains/congestion-metrics.md) | Metric message schemas, detector layer, experiment protocol |
| Project license | [Simulator Landscape](business-domains/simulator-landscape.md) | Nothing yet (deferred 2026-07-17, leaning MIT); ODbL posture stands per ADR-0009 regardless |

## Benchmark Queue (measure at engine/viz bring-up — the literature is silent)

| Measurement | Why it matters | From |
|---|---|---|
| JetStream puback latency vs the 100 ms tick at high batch multipliers | Sizes faster-than-realtime batch multipliers; the tick depends asynchronously on broker persistence health | [NATS Backbone](architecture/nats-backbone.md), [Time Model](architecture/time-model.md), ADR-0006 |
| Per-vehicle wire size and payload shape at 10k vehicles (8–16 B derivation) | No published per-vehicle wire sizes exist anywhere; cell-packed vs per-vehicle subjects decision | [State Authority](architecture/state-authority.md), [MapLibre Realtime Viz](integrations/maplibre-realtime.md) |
| AoI window sizing (radius, neighbor count, features) vs tick budget | The observation window is contract surface; bandwidth grows with fleet size | [Vehicle & Controller Interface](concepts/vehicle-controller-interface.md), [State Authority](architecture/state-authority.md) |
| Cell geometry/size experiment on a real imported network | Interest management is compiled to cell subjects; needs real network densities | [State Authority](architecture/state-authority.md), [OSM Extraction](integrations/osm-extraction.md) |
| Keyframe cadence from measured re-sim speed; keyframe chunking curve vs 1 MB `max_payload` | Bounds replay seek depth; chooses manifest-chunking vs Object Store | [NATS Backbone](architecture/nats-backbone.md) |
| Stream topology at scale: per-run streams vs single stream | Broker asset lifecycle costs are unpublished | [NATS Backbone](architecture/nats-backbone.md) |
| nats.ws browser throughput for 10 Hz × N-vehicle snapshots | Confirms the WebSocket path for the TS viz; headers-only consumer question | [NATS Backbone](architecture/nats-backbone.md), [MapLibre Realtime Viz](integrations/maplibre-realtime.md) |
| MapLibre microbenchmark: `updateData` fleet ceiling and feature-state throughput (1k→100k vehicles × 1/10/30 Hz) | Sets the deck.gl escalation rungs numerically; no published numbers at our shape | [MapLibre Realtime Viz](integrations/maplibre-realtime.md) |
| Metric stream budget: fleet × 100 ms tick × per-vehicle state vs NATS throughput | The trajectory-first metric kernel's stream sizing | [Congestion Metrics](business-domains/congestion-metrics.md) |
| `b_safe` vs time-gap enforcement as the engine safety backstop | Picks the junction/lane-change safety invariant's implementation | [Traffic Flow Models](business-domains/traffic-flow-models.md), ADR-0007 revisit trigger |
| Connection-conflict memory footprint at city scale; protobuf vs Go-native compiled network encoding | Sizes the road-graph representation | [Road Graph Model](architecture/road-graph-model.md) |
| Go GC pause jitter in paced loops | Folklore only; measure before promising smooth 1× pacing | [Time Model](architecture/time-model.md) |

## Prototype Experiments (`prototypes/`)

| Experiment | Question it answers | From |
|---|---|---|
| Sugiyama ring acceptance test (22 cars, 230 m ring; 21 must not jam) | Falsifiable string-instability test for the engine — CI scenario; ⚠ needs Nakayama 2016 OV parameters first | [Traffic Flow Models](business-domains/traffic-flow-models.md), [Trajectory Datasets](business-domains/trajectory-datasets.md) |
| AWSC departure headways (3.9–7.0 s emergent) | Validates the 4-way-stop stop-line model against field measurements | [Traffic Flow Models](business-domains/traffic-flow-models.md) |
| Zipper/merge cooperation over the bus | MOBIL cooperation as a NATS intent spans ≥1 tick round trip vs SUMO's in-process same-step resolution — measure merge-throughput impact | [Traffic Flow Models](business-domains/traffic-flow-models.md) |
| LWR oracle suite (red light, lane drop, on-ramp merge; Newell controller vs LTM) | Asserts shock speeds and delay against analytic ground truth in CI | [Macroscopic Flow Models](business-domains/macroscopic-flow-models.md) |
| CRN stream-assignment granularity | Which concerns share RNG streams across alternatives (spawn times yes; lane-change draws?) — needs the RNG layout to exist first | [Congestion Metrics](business-domains/congestion-metrics.md) |
| Deadlock prevention + telemetry vs SUMO's 300 s teleport | Prove the prevention mechanisms keep gridlock physical and measurable | [Traffic Flow Models](business-domains/traffic-flow-models.md) |

## External-Data Errands

| Errand | Why | From |
|---|---|---|
| levelXdata (inD/rounD) non-commercial terms vs a monetized educational episode — ask | The intersection/roundabout validation targets may need licensing clearance before episode use | [Trajectory Datasets](business-domains/trajectory-datasets.md) |
| Locate Coifman & Li corrected NGSIM I-80 data; Montanino & Punzo CSV mirrors; triage which NGSIM window / I-24 segment for first analysis | The "do our own calc" exercise needs clean source data | [Trajectory Datasets](business-domains/trajectory-datasets.md) |
| Obtain one real published timing sheet + ATSPM data for a validation intersection | End-to-end validation of phase-termination distributions; real timing plans are not open data | [Signal Control](business-domains/signal-control.md) |
| Transcribe IIDM/ACC equations from primary sources (Treiber & Kestor book / movsim) before coding those variants | Secondary-source transcription risk on model equations | [Traffic Flow Models](business-domains/traffic-flow-models.md), ADR-0007 revisit trigger |
| Verify Daganzo 1995 merge `mid()` formula and Yperman LTM discrete equations against primary texts (⚠-flagged in raw files) | The LTM preview/oracle implementation depends on them | [Macroscopic Flow Models](business-domains/macroscopic-flow-models.md) |
| Resolve the jam-density discrepancy (100–150 vs 180–200 veh/km/lane) against HCM / Treiber & Kesting | Picks the default fundamental diagram per road class | [Macroscopic Flow Models](business-domains/macroscopic-flow-models.md) |
| Read SUMO source (or experiment) for the exact `allway_stop` mechanism (arrival ordering, tie-breaking, creep) | The 4-way stop is the least-specified common junction behavior in public docs. **Downgraded 2026-07-18:** ADR-0010 compiles `w` → plain stop by fiat; this errand now only gates upgrading beyond that mapping | [Road Graph Model](architecture/road-graph-model.md), ADR-0010 |
| Calibrate the lane-inference defaults table against drone/dataset geometry (levelX/highD/NGSIM) | netconvert confesses its typemap is unverified; the defaults table *is* most of the network | [OSM Extraction](integrations/osm-extraction.md) |

## Deferred Decisions (consciously parked)

- **In-process controller fast path vs ADR-0002's no-fast-path clarification** — revisit when the RL training use case lands ([Simulator Landscape](business-domains/simulator-landscape.md) vs ADR-0002 2026-07-17 clarification).
- **Left-hand traffic** — global import parameter chosen (ADR-0009 v1); per-edge override question remains before lane-direction logic is written.
- **`restriction:conditional` (18.6 k relations)** — ignored-with-flag in v1; possible scenario time-slicing later ([OSM Extraction](integrations/osm-extraction.md)).
- **Map-edge demand portals** — network-file concept (typed `MapEdge` junction) vs scenario demand concern; touches Road Graph Model + Scenario Format + OSM Extraction.
- **Binary framing tool** for the SoA vehicle subject — hand-rolled header + typed arrays vs flatbuffers/protobuf ([MapLibre Realtime Viz](integrations/maplibre-realtime.md), ADR-0006 territory).
- **Cross-architecture determinism upgrade** (fence FMA / vendor math / int64 fixed-point) — only if civic-advocacy partners need heterogeneous replay verification (ADR-0005 revisit trigger).
- **GPU escape hatch** (MOSS 88.9× precedent) — a separate future ADR with its own determinism research, only if metro scale ever becomes the requirement.
- **Signal feature coverage** (permissive/lead-lag authoring ergonomics, soft recall, stage-based import for European networks, TSP) — lands when the first advocacy corridor is chosen; the ADR-0008 interface already hosts any algorithm.
- **Overture GERS adoption timing** — solves ID stability, not lanes; re-evaluate when its lanes redesign lands.
- **Re-import/edit carry-over** (durable edits across fresher OSM extracts) — fuzzy anchoring is later work, gated on ID-stability evidence (ADR-0009 consequences).
- **95th-percentile queue estimator, reliability baseline (PTI vs buffer index), PCU conversion in v1** — pick after seeing our own queue time series ([Congestion Metrics](business-domains/congestion-metrics.md)).
- **Multi-tenancy** (auth callout, `nsc` JWT, account-per-tenant) — only for public multiplayer (ADR-0006 §9).

## Under-Documented Areas

| Area | Why It Matters | Suggested Topic |
|------|---------------|-----------------|
| Engine internal architecture (module layout around the world-state goroutine, event list, sweep) | ADR-0005 fixes the time model, not the code structure; the first Go milestone needs it | `arch-engine-internals` |
| Human-driver client (input devices/sampling, HUD, latency surfacing, prediction UX) | The multiplayer chaos demo's front end; state-authority covers the netcode, not the client product | `concept-human-driver-client` |
| Multiplayer session management (lobby, run registry UX, claim UI, auth at scale) | The chaos demo needs join/claim flows that no topic covers | `concept-multiplayer-session` |
| Demand modeling depth (od2trips-style compiler, count-driven demand, NGSIM-calibrated demand import) | Scenario format fixes the container; demand estimation itself is unresearched | `domain-demand-modeling` |
| Emissions/fuel post-processor (HBEFA-class tables over the trajectory stream) | Explicitly deferred by metrics research; a stated future consumer | fold into observability ADR follow-ups |

## Suggested Next Research

1. **Draft the three remaining pending ADRs** (network model, observability, license) — their research gates are closed; drafting is the cheapest unblocking step. (Scenario format left this list 2026-07-21 → ADR-0012.)
2. **`arch-engine-internals`** — engine module decomposition against the 10⁵ vehicle-update/s tier target; would absorb several benchmark-queue items into one bring-up plan.
3. **`domain-demand-modeling`** — demand estimation and generation tooling (the field's dfrouter/od2trips shape), including calibration from the trajectory corpora.
4. **`concept-human-driver-client`** — input-to-intent pipeline and HUD, completing the multiplayer story end to end.

## Freshness Notes

- 2026-07-21 (c): M11 review round (external reviews by Claude Fable, GPT-5.6-sol, Gemini — brief and findings in the ADR-0012 addendum's review-provenance note). Fixed before the first durable hash binding: content hash now strips seed/ticks (run coordinates, not content) and is a domain-separated protocol (`traffic-sim/scenario-hash/v1` + golden vector; yaml.v3 bump = format event); scenario demand parts EXECUTE — the reference director moved to `engine/natsio/demand` and serve embeds it run-seeded (simrun refuses demand scenarios; offline spawn table still deferred); strict fence gained a schema-aware node-type check (yaml.v3 silently truncates 1.9→1 into ints and stringifies bools — verified) and finite-number checks; `pickType` sums weights in sorted-key order (non-associative floats, ADR-0005); flow RNG key uses the full 8-byte index (M10 low byte collided at 256); fmt is comment-preserving + atomic; symlink escapes, unclean/backslash refs, duplicate types, and veh_per_h-alongside-slices all fail loud; `-scenario` rejects scenario-owned flags. RunMeta's spec.Hash documented in asyncapi + ADR-0006 addendum. Flows gained optional `id` (overlay anchor). All suites green; scenario-vs-flag CRC equivalence holds.
- 2026-07-21 (b): M11 scenario directories landed (ADR-0012 implementation). `engine/scenario` (yaml.v3 — second approved dependency exception, confined per the natsio precedent) loads a strict-YAML manifest + demand parts into an `engine.RunSpec`; the content hash rides in `RunSpec.Hash` so the run registry records (content-hash, seed). `cmd/scenario` provides validate/fmt/hash/migrate; `serve`/`simrun` take `-scenario` (explicit `-seed`/`-ticks` override the manifest for sweeps); the demand-director moved to the demand schema (M10 JSON parses once `format_version: 1` is added; demo now `demo-i280.yaml`). Schema validation is strict decoding + semantic checks, no JSON-Schema library (ADR-0012 addendum). Scenario-vs-flag runs are bit-identical on I-280 (crc `e92229c4a89d3709`). Not in M11: overlays/variant.yaml, control/metrics binding grammars, pack format, offline spawn-table export — the overlay milestone is next.
- 2026-07-21: ADR-0012 (scenario format) ratified — manifest-of-parts directory in strict YAML, runtime director demand sampling (the M10 contract as-is; demand files ARE director configs), kustomize-style overlay variants (addition-only, no templating, ADR-0009 network delta-patch grammar), run identity = (content-hash, seed), per-file `format_version` with the Kubernetes round-trip rule. One dependency exception: a YAML library confined to `engine/scenario/` (ADR-0006 natsio precedent). Implementation is the next milestone (M11: loader + `scenario fmt/validate/migrate` + `-scenario` on serve/simrun); deferred to later ADRs/work: pack format, controller-assignment syntax, signal-program schema, real-data demand import, scenario-from-recording initial state. Pending-ADR queue is now three: network model, observability/metric set, license.
- 2026-07-20 (b): M10 runtime demand director landed (ADR-0006 addendum, ADR-0008 clarification — grant model UNCHANGED, `director` grant covers verbs). New additive channels: `ts.{run}.ctl.verb.{controller_id}` (request/reply spawn verbs, idempotent by director request id) and `ts.{run}.log.verb` (record plane; replay re-enqueues, the sampler never re-runs — bit-identity pinned in `TestDirectorVerbRecordReplay`). Kernel injection reuses the Spawner's mechanics with bounded hold-and-retry (600 ticks). Reference client `engine/cmd/demand-director` (strict-JSON flows, Poisson/constant, vType weights, per-vehicle keyed sampling). The C2 runtime-director review item is now implemented; the scenario format ADR is what remains to make demand definitions files instead of flags. TSKF keyframes are at v3 (v2 loads; empty queues marshal byte-identical).
- 2026-07-20: M9 signal state on the live plane (ADR-0006 addendum) — new additive subject `ts.{run}.state.sig` (TSSG v1) ships the signal-program TABLE + publish tick; clients derive light states by the kernel's own integer math (phase changes need zero messages; late joiners converge ≤ keyframe cadence). Key finding, now contractual: TSSF v1 decoders hard-reject on exact length AND version, so in-frame extension is never additive — new channels are the only backward-compatible shape (the M6 TSSF bump candidates — speed, lane id — are affected by this when they land). Viz renders the two I-280 junctions' stop-line lights, verified in headless Chrome (green/amber/red cycling, metered queue visible).
- 2026-07-19: M8 fixed-time signals landed (ADR-0011) — static tlLogic compiles to kernel-run programs; phase state is a pure function of the tick count (no CRC/keyframe coverage needed); enforcement composes with the ADR-0010 stop-line guardrail (red hold, amber stop-if-able, green + box checks). I-280: 0 collisions at rate 600, the 8 known right_before_left residuals at 2400 unchanged. The data-driven phase seam (D1) leaves external signal controllers as the next signal milestone — a message-contract change needing its own ADR.
- 2026-07-18: M7 junction right-of-way landed (ADR-0010) — priority model (major/minor/stop) compiled by netimport from SUMO connection states, enforced kernel-side in the shared accel path so all controllers inherit it. Conflicting-path (funnel/crossing) collisions eliminated on the I-280 reference import at every tested demand; residual same-path queue-release overlaps at one `right_before_left` junction documented in ADR-0010 Consequences. Junction right-of-way is off the follow-up list; signal controllers move to the top.
- 14 topics researched 2026-07-15 → 2026-07-17 (56 raw files), all status `complete`; distilled 2026-07-17 into 18 articles.
- Oldest research (2026-07-15): domain-traffic-flow-models, domain-macroscopic-flow-models, domain-trajectory-datasets, arch-time-model — all still current (their open items were ratified by the 2026-07-17 review).
- Run `/update-kb` to check for stale topics after a gap.

---
*Generated: 2026-07-17*
