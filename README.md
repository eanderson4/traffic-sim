# traffic-sim

An open-source traffic simulation engine built on NATS. Models real road networks
(including OpenStreetMap imports), hosts heterogeneous vehicle controllers — AI
policies and live human drivers alike — and produces decision-grade congestion
metrics for comparing infrastructure alternatives.

**Status: pre-scaffold.** Vision and knowledge base first, code second.

## Why

Traffic questions like *"which upgrade actually fixes this intersection?"* deserve
simulation, not vibes. This project powers a [math-vs-vibes](../math-vs-vibes)
episode, but is built standalone to scale well beyond it — up to replaying days of
real traffic to evaluate signal-timing changes.

## Design in One Paragraph

An authoritative **engine** (Go) owns world state over a lane-level road graph and
publishes it via **NATS**; **controllers** (AI or human, any language) subscribe to
state and emit driving intents as events; **visualization** (TypeScript + MapLibre)
renders live vehicles and congestion heatmaps from the same streams. JetStream gives
durable event logs for deterministic replay; scenarios (network + demand + control
config) are first-class and diffable so alternatives can be ranked on metrics.

## Repo Map

| Path | What |
|------|------|
| `docs/VISION.md` | Founding document — read this first |
| `docs/kb/` | Knowledge base: research, articles, decision records |
| `AGENTS.md` | Rules for humans and agents working here |

Code layout (engine, controllers, viz, scenarios) will be established after the
initial KB research pass — see `docs/kb/INDEX.md` for open topics.

## Getting Started

Nothing to run yet. NATS will arrive via `docker-compose up` once the engine
skeleton lands.
