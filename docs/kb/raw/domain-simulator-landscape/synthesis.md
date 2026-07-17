# Synthesis: Simulator Landscape

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds a future license ADR (none exists yet) and the designs behind
> [[concept-scenario-format]], [[concept-vehicle-controller-interface]],
> [[arch-road-graph-model]], [[domain-congestion-metrics]].
> This synthesis recommends; the ADR decides.

## Summary

The field stratifies into five camps with almost no overlap in feature sets:
**(1)** open batch toolboxes (SUMO, MATSim, BEAM, SimMobility) — deep models, file
interfaces, no live controllers; **(2)** proprietary professional tools (Vissim,
Aimsun) — polished workflows, closed internals; **(3)** RL-first engines
(CityFlow, Flow, MOSS) — speed and Python bindings, thin scenario/metrics
tooling; **(4)** game-engine driving sims (CARLA) — sensors and photorealism,
weak traffic models; **(5)** civic/UX tools (A/B Street) — unmatched first-run
experience, unmaintained sim core. Time-model conclusions are settled elsewhere
([[arch-time-model]] → ADR-0005). What this survey adds: **no system combines a
lane-level engine, a live multi-controller bus, decision-grade metrics, and
trustworthy replay** — each has at most two of four. Every architectural lesson
below is measured, not conjectured: the TraCI 11× wall, libsumo's escape hatch,
MATSim's event stream, MOSS's 88.9× GPU ceiling, and the governance data on which
projects survive.

## Source Files

- [Mechanics: how the incumbents are built](./implementation.md)
- [Prior art survey (12 systems + calibration points)](./competitors.md)
- [Standards, licenses, patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions

### 1. Steal SUMO's tool-suite decomposition — we already have its modern shape
**Choice:** Engine, OSM-import, demand-gen, and metrics are separate small
binaries/services; shared artifacts (road graph, demand, scenario) are the
contracts, transported over NATS instead of XML files.
**Why:** SUMO's own design rationale — "each is smaller than a monolithic
application… easier extension… faster data structures, each adjusted to the
current purpose" — is the 25-year-validated version of VISION principle 3. MOSS
independently reinvented it as a repo constellation in 2024.
**Trade-off:** SUMO's acknowledged cost ("a little bit uncomfortable" UX) —
file-glue friction. Our message-bus glue avoids it, at the price of running a
broker (ADR-0002/0004 already accept this).
**Field context:** [implementation §1](./implementation.md).

### 2. Contract-first control plane: subscriptions, never per-vehicle polling
**Choice:** Controllers consume pushed snapshot/subscription streams on NATS
subjects and emit intents (ADR-0005 §3). Never design a synchronous
query-vehicle RPC into the hot path. If RL training needs it later, offer an
in-process controller that speaks the *identical* intent/snapshot contract
(libsumo lesson: same API, different transport).
**Why:** TraCI's per-step socket barrier measured 90 s vs 8 s on 9k vehicles
(11×); SUMO's own remedies (subscriptions, then libsumo/libtraci) are the
documented escape sequence. Flow built an RL framework *on* the slow transport
and inherited the wall; CityFlow/MOSS won adoption by going in-process. Also:
third parties re-implement stepping protocols (Veins wrote its own TraCI
client) — the contract outlives the transport, so version it.
**Trade-off:** push streams cost broker fan-out bandwidth even when idle;
in-process fast paths complicate the "NATS is the sole backbone" reading of
ADR-0002 — flag for clarification when the RL use case arrives (same transport
rules, loopback implementation).
**Field context:** [implementation §2](./implementation.md),
[[concept-vehicle-controller-interface]].

### 3. Metrics = stream consumers; adopt SUMO's detector vocabulary as the artifact set
**Choice:** The engine emits an immutable tick-stamped event stream on JetStream
(MATSim pattern); E1/E2/E3-style detectors, tripinfo, queue-length, FCD are
computed by downstream services, not by the engine core.
**Why:** MATSim proves analysis-as-consumers scales to 1M-agent runs without
touching the engine; SUMO's output catalog (FCD, tripinfo, E1/E2/E3, queue,
summary) is the vocabulary traffic engineers already read — adopting its names
and semantics buys instant legitimacy for [[domain-congestion-metrics]].
**Trade-off:** event volume needs stream/retention sizing (already an
[[arch-nats-backbone]] open item); some aggregates are cheaper computed inline —
allow engine-internal cheap counters (SUMO's step-log precedent) without making
them the contract.
**Field context:** [implementation §6](./implementation.md).

### 4. Scenario = directory of typed, text, diffable artifacts; keep the config surface small
**Choice:** One top-level config referencing typed artifacts (network, demand,
signals, metrics definitions) — the SUMO `.sumocfg`/CityFlow `config.json`
shape — all text (no protobuf/binary for v1), versioned, with explicit defaults
and a documented module list that resists MATSim-style accretion.
**Why:** VISION requires diffable scenario variants; text formats + directory
layout is how every open tool does it. MATSim's 15-year module accretion is the
documented failure of the alternative; MOSS's protobuf/YAML split optimizes GPU
ingest we don't have.
**Trade-off:** XML (SUMO) vs JSON (CityFlow) vs YAML (MOSS) is a taste decision
deferred to [[concept-scenario-format]]; text costs parse time at scale —
revisit only if measured.
**Field context:** [implementation §5](./implementation.md).

### 5. License: choose permissive (MIT/Apache-2.0); treat EPL/GPL code as read-only
**Choice:** License traffic-sim permissively. Borrow *code* only from
MIT/Apache/BSD projects (CARLA, CityFlow, Flow, MOSS, A/B Street, OTS) with
attribution; treat SUMO (EPL-2.0, file-level copyleft) and MATSim/BEAM (GPL) as
ideas-and-formats references; never invent a custom license (SimMobility
warning).
**Why:** Every post-2017 entrant that won the RL/benchmark community chose
permissive; copyleft on the engine would poison exactly the embedding/RL use
cases we want; a bespoke license correlates with community failure. This is a
decision of consequence → **needs its own ADR** (none exists).
**Trade-off:** permissive lets competitors embed us unreciprocated — the
accepted cost of the adoption pattern we're copying.
**Field context:** [standards file, license taxonomy](./standards-and-patterns.md).

### 6. UX and community: first-run-in-minutes + docs-as-product from day one
**Choice:** Target A/B Street's onboarding shape — "pick an OSM region, see
traffic in minutes" (browser viz via MapLibre, ADR-0003) — and treat docs
(SUMO's wiki) and reproducible examples (SUMO conference proceedings since
2013, MATSim's open book) as deliverables, not leftovers.
**Why:** A/B Street's 8.1k stars came from UX ambition, not model fidelity;
SUMO/MATSim's 20–25-year survival correlates with institutional homes +
documentation investment; the RL-boom engines (Flow, CityFlow) stalled within
~6 years of their papers.
**Trade-off:** docs/examples time competes with engine features in a small team;
A/B Street also shows the risk of UX-first (sim core unmaintained) — our
validated models ([[domain-traffic-flow-models]]) invert that risk.
**Field context:** [competitors](./competitors.md) governance bullets.

### 7. Performance posture: aim at the SUMO–CityFlow CPU tier; leave GPU as a researched escape hatch
**Choice:** No GPU dependency in v1. Design the single-writer Go core to the
~10⁵ vehicle-update/s tier (SUMO parity) and measure against CityFlow's >20×;
publish the step-log equivalent (real-time factor, updates/s) from the start.
**Why:** VISION's scenarios (4-way stop → district, week replay) live in the
10³–10⁵ vehicle range; CityFlow shows the tier ceiling comes from data
structures, not heroics; MOSS proves an 88.9× GPU move exists if metro scale
ever becomes the requirement — and shows scale alone doesn't produce a
controller bus or replay.
**Trade-off:** GPU + our determinism envelope (same-binary/same-GOARCH,
ADR-0005 §6) is an unverified combination — treat GPU as a separate future ADR
with its own determinism research.
**Field context:** [implementation §8](./implementation.md).

### 8. Avoid by default: replanning loops, federation, game-engine coupling
**Choice:** No MATSim-style between-iteration replanning in v1 (different
research program); no RTI/ambassador co-simulation layer (MOSAIC) — if an
external sim must be ingested, write a one-off NATS adapter; no game-engine
rendering dependency (already ADR-0003/VISION non-goal); no activity-based or
land-use demand modeling (SimMobility scope sink) — start with OD/count-driven
demand (SUMO `dfrouter`/`od2trips` shape).
**Why:** Each is a documented multi-year program that answers a question we
aren't asking; ADR-0005's single-authority model makes an internal RTI pure
overhead.
**Trade-off:** we concede equilibrium-demand and V2X-communication research
territory to MATSim/MOSAIC — consistent with VISION's non-goals.
**Field context:** [implementation §3, §4, §10](./implementation.md).

## Compare/Contrast: Us vs the Field (feature presence)

| Capability | SUMO | MATSim | CARLA | CityFlow/MOSS | A/B Street | **us (target)** |
|---|---|---|---|---|---|---|
| Lane-level validated dynamics | yes | no (queue) | partial | yes | partial (unvalidated) | **yes (IDM/MOBIL)** |
| Live external controllers | TraCI (blocking) | none | RPC sync/async | in-proc Python | none | **NATS async, batch-apply** |
| Humans as controllers | no | no | ego-vehicle clients only | no | no | **yes (multiplayer)** |
| Decision-grade metrics | strong catalog | via events | weak | thin | weak | **events → detectors** |
| Seekable verified replay | re-run | re-run | state log | checkpoint/re-run | re-run | **keyframes + intent log + CRC** |
| Faster-than-realtime batch | yes | yes | limited | yes (the point) | n/a | **yes (unpaced driver)** |
| OSM real-network import | yes | partial | no | via converters | **best in class** | **planned** |
| Permissive license | EPL-2.0 | GPL | MIT (+UE EULA) | Apache/MIT | Apache-2.0 | **TBD → recommend permissive** |
| Governance longevity | 25 yrs, foundation | 20 yrs, universities | foundation (2023) | lab (stalled/watching) | company pivot | **open question** |

## The Genuine Gap (again)

Three holes nobody covers:

1. **Message-bus-native lane-level simulation doesn't exist.** Every live
   control API is a bolted-on stepping RPC (TraCI, CARLA RPC) or an in-process
   library (libsumo, CityFlow, MOSS); the batch toolboxes have no live interface
   at all. A pub/sub backbone with humans as first-class controllers is
   unoccupied territory — consistent with the NATS-tick-loop gap found in
   [[arch-time-model]].
2. **Scrubbable, verified replay is unbuilt.** Re-run is the universal default;
   CARLA records states (no re-execution); MOSS added bare GPU checkpoints only
   in v1.1. Nobody ships ADR-0005's keyframes + arbitrated intent log + CRC — the exact
   artifact the civic-advocacy use case needs to be trustworthy.
3. **Governance longevity is the scarcest resource.** Only institutional homes
   (DLR/Eclipse, TUB/ETH, Fraunhofer, LBNL) kept sims alive past their founding
   papers; the permissively-licensed RL engines all stalled when their labs
   moved on. A small open project with decision-grade ambitions is itself a gap
   the field would notice — the engineering write-up potential keeps growing
   (see also [[arch-time-model]]'s gap).

## Open Questions

- Our own license choice → needs an ADR (recommendation #5).
- SUMO `.net.xml` import compatibility: adopt the gravity well or stay clean?
  → [[arch-road-graph-model]] + [[integration-osm-extraction]].
- Vissim/Aimsun internals remain closed; their public API surfaces (COM, AAPI,
  microSDK) are documented but internal decomposition is inference only.
- MOSS determinism across GPU architectures vs our replay envelope — undocumented
  upstream; relevant only if recommendation #7's escape hatch is ever taken.
- In-process controller fast path vs ADR-0002's "NATS sole backbone" — clarify
  wording when the RL use case lands (recommendation #2's trade-off).
- MATSim config-sprawl quantification (module count) — qualitative claim sourced,
  number not.

## Connections to Other Topics

- **Decided already, referenced throughout:** [[arch-time-model]] / ADR-0005
  (tick model, async intents, replay design, determinism envelope — this survey
  independently confirms each against field practice).
- **Constrains:** [[concept-scenario-format]] (recommendation #4: directory of
  text artifacts, anti-sprawl), [[concept-vehicle-controller-interface]] (#2:
  push contracts, in-process fast path), [[arch-nats-backbone]] (#2/#3:
  subscription subjects, event stream retention), [[domain-congestion-metrics]]
  (#3: detector vocabulary), [[arch-state-authority]] (#1: engine as one small
  service among tools), [[arch-road-graph-model]] (OpenDRIVE/SUMO import
  question), [[integration-osm-extraction]] (#6: first-run UX), a future license
  ADR (#5).
- **Depends on:** nothing blocking — this topic surveys the landscape the other
  topics deepen.
- **Relates to:** [[domain-traffic-flow-models]] (validated models are our
  A/B-Street-risk inversion), [[domain-macroscopic-flow-models]] (Aimsun's
  one-network/three-fidelities as future LTM path), [[domain-trajectory-datasets]]
  (validation targets the field calibrates against), [[domain-signal-control]]
  (Vissim/Aimsun signal-API practice: VAP, AAPI signal functions, external
  signal-control DLLs).
