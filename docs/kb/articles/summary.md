# traffic-sim — Knowledge Base Summary

> traffic-sim is a standalone, open-source traffic simulation engine — real road networks, heterogeneous vehicle controllers (AI and human), decision-grade congestion metrics, and trustworthy replay — currently in its pre-implementation design phase: 14 research topics and 9 accepted ADRs, no engine code yet.

## Architecture at a Glance

An authoritative **Go engine** advances a lane-level road graph on a fixed 100 ms tick (the tick count is the only clock). All other participants — AI policies, scripted vehicles, human drivers, scenario directors, signal controllers, the default-driver fleet, visualizers, metric services — are external **NATS clients** divided across three planes: core NATS for live self-sufficient state (binary SoA frames), JetStream for the arbitrated intent log + keyframes + CRC that make replay trustworthy, and KV for config and latest-state resync. Scenarios are diffable directories (network + demand + control + metrics) with overlay variants; observability is a streaming trajectory-first metric kernel; visualization is MapLibre GL with a measured escalation ladder to deck.gl. The full rationale lives in [VISION.md](../../VISION.md) and the [ADR index](decisions/adrs.md).

| Subsystem | Purpose | Key Article / ADR |
|-----------|---------|-------------------|
| Engine core (planned, Go) | Authoritative world state, fixed-tick advance, event list, safety clamps | [Time Model](architecture/time-model.md) · ADR-0005 |
| Road graph | Lane-as-atom network, compiled right-of-way, `(laneId, s)` addressing | [Road Graph Model](architecture/road-graph-model.md) |
| NATS backbone | Three-plane messaging: live / record / config | [NATS Backbone](architecture/nats-backbone.md) · ADR-0002, ADR-0006 |
| State distribution | Interest-managed snapshots, ego prediction, hold-last inputs | [State Authority](architecture/state-authority.md) |
| Controllers | 4-axis intents, exclusive claims, grants-based roles, default-driver fleet | [Vehicle & Controller Interface](concepts/vehicle-controller-interface.md) · ADR-0008 |
| Driving models | IDM + MOBIL defaults, validated 100 ms tick, Newell oracle | [Traffic Flow Models](business-domains/traffic-flow-models.md) · ADR-0007 |
| Signal control | NEMA dual-ring model, external cabinet-vocabulary clients, MMU clamps | [Signal Control](business-domains/signal-control.md) |
| Observability | Streaming trajectory-first metrics (Edie q/k/u), experiment protocol | [Congestion Metrics](business-domains/congestion-metrics.md) |
| Scenarios | Manifest-of-parts directories, overlay variants, (content-hash, seed) runs | [Scenario Format](concepts/scenario-format.md) |
| OSM import | netconvert bootstrap + Go importer with permanent oracle | [OSM Extraction](integrations/osm-extraction.md) · ADR-0009 |
| Visualization | MapLibre three-channel live rendering, deck.gl ladder | [MapLibre Realtime Viz](integrations/maplibre-realtime.md) · ADR-0003 |
| Validation data | Real trajectory corpora and wave-analysis tooling | [Trajectory Datasets](business-domains/trajectory-datasets.md) |

## Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Engine language | Go (+ TS for viz) | NATS ecosystem alignment, concurrency, headroom — ADR-0001 |
| Inter-service communication | NATS only (core / JetStream / KV) | Live fan-out, replay logs, and config map 1:1; everyone is just a client — ADR-0002 |
| Visualization | MapLibre-first, vanilla TS | GIS-shaped rendering; deck.gl only on measured need — ADR-0003 |
| Deployment | Local-first docker-compose | Zero ops for the episode; nothing precludes hosting later — ADR-0004 |
| Time model | Hybrid fixed tick (100 ms), event-driven edges | Every lane-level sim uses a fixed tick; blocking on clients is an 11× measured failure — ADR-0005 |
| Message contract | Three planes, `{ns}.{run}.{plane}.>`, AsyncAPI | Ack-less fan-out forces self-sufficient live messages; tick in payload — ADR-0006 |
| Vehicle conventions | Front-bumper `s`, bumper-to-bumper gap, IDM+MOBIL | Matches the literature; no cross-pipeline drift by a vehicle length — ADR-0007 |
| Controller contract | 4-axis intents, exclusive claims, zero engine driving logic | TraCI/CARLA failure modes; failover is operational, not simulative — ADR-0008 |
| OSM import | netconvert bootstrap + Go importer + permanent oracle | Only 3 compilers exist, none Go; lane tags too sparse to trust — ADR-0009 |

## Current State

- **Strengths:** the design phase is unusually well-evidenced — every recommendation cites measurements, every subsystem has a validation oracle, and the 2026-07-17 review ratified the contested seams into ADR-0005..0009. The repo already carries working analysis tooling (`analysis/ngsim` measured a −18.1 km/h wave on NGSIM I-80) and prototype scaffolding (`prototypes/`).
- **In flux:** four research-complete areas await their ADRs — network model, scenario format, observability metric set, project license (see [ADR index](decisions/adrs.md#research-complete-adr-pending)). Nothing is coded yet; contracts are the next artifacts.
- **Known gaps:** a queue of benchmark-at-bring-up measurements (JetStream puback latency, wire sizes, MapLibre throughput), prototype experiments (ring-road acceptance test, AWSC headways, zipper fairness), and external-data errands (dataset licensing, real timing sheets, equation transcription against primary sources) — full list in [Gaps & Roadmap](gaps-and-roadmap.md).

## Reading Paths

**New contributor:**
1. [Summary](summary.md) (you are here)
2. [Simulator Landscape](business-domains/simulator-landscape.md) — the field and our niche
3. [Time Model](architecture/time-model.md) — the keystone decision
4. [Cross-Topic Concerns](cross-topic-concerns.md) — the invariants everything shares

**Engine developer (Go core):**
1. [Time Model](architecture/time-model.md) · ADR-0005
2. [Traffic Flow Models](business-domains/traffic-flow-models.md) + [Road Graph Model](architecture/road-graph-model.md)
3. [NATS Backbone](architecture/nats-backbone.md) + [State Authority](architecture/state-authority.md) · ADR-0006

**Controller / integration developer:**
1. [Vehicle & Controller Interface](concepts/vehicle-controller-interface.md) · ADR-0008
2. [State Authority](architecture/state-authority.md) — windows, prediction, hold-last
3. [Scenario Format](concepts/scenario-format.md) — how runs are defined

**Viz / web developer (TS):**
1. [MapLibre Realtime Viz](integrations/maplibre-realtime.md) · ADR-0003
2. [NATS Backbone](architecture/nats-backbone.md) — wire formats and resync
3. [Congestion Metrics](business-domains/congestion-metrics.md) — what the heatmap shows

**Scenario author / traffic engineer:**
1. [Scenario Format](concepts/scenario-format.md)
2. [Signal Control](business-domains/signal-control.md) + [Congestion Metrics](business-domains/congestion-metrics.md)
3. [OSM Extraction](integrations/osm-extraction.md) — real-network imports and their limits

---
*Distilled from 14 raw research topics (56 files) and 9 ADRs on 2026-07-17*
