# traffic-sim

An open-source traffic simulation engine built on NATS. Models real road networks
(including OpenStreetMap imports), hosts heterogeneous vehicle controllers — AI
policies and live human drivers alike — and produces decision-grade congestion
metrics for comparing infrastructure alternatives.

**Status: M11 — scenario directories.** Simulation kernel (Go), NATS message
contract, external default driver, OSM/netconvert network import, junction
right-of-way, fixed-time signals, runtime demand director, live MapLibre viz,
and ADR-0012 scenario loading are built and tested — see
`docs/kb/decisions/` for the 12 ADRs they implement.

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

## See It

**[phantomjam.com](https://phantomjam.com)** is the public face of this engine:
an atlas of the imported city networks (LA, SF, Miami, Atlanta, Houston,
Dallas, Chicago — down to the stop signs) and, as they land, in-browser
replays of the simulation experiments, baked to static data and served from
the edge. A live shared sim (`app.phantomjam.com`, deployment in `deploy/`)
follows once ADR-0020's two go-live preconditions (ws-plane broker auth AND
engine `/healthz`) land.

## Repo Map

| Path | What |
|------|------|
| `docs/VISION.md` | Founding document — read this first |
| `docs/kb/` | Knowledge base: research, articles, decision records |
| `AGENTS.md` | Rules for humans and agents working here |
| `engine/` | Go simulation kernel + NATS contract + tools (`simrun`, `serve`, `netimport`, `scenario`, `default-driver`, `demand-director`) |
| `contracts/` | AsyncAPI message contract + network file format v1 |
| `viz/` | MapLibre realtime client (TypeScript, pnpm, no framework) |
| `analysis/ngsim/` | NGSIM x-t field tooling + I-80 wave validation |
| `deploy/` | Public-deployment artifacts: Dockerfile, demos registry, GKE manifests (see `deploy/README.md` — gated on ADR-0020 preconditions) |
| `prototypes/` | Throwaway engine-fork demos (pre-implementation) |

## Getting Started

Run the test suites:

```sh
cd engine && go test ./...
cd viz && pnpm install && pnpm test
```

Run the live demo (real I-280 network, external default driver, browser viz):

```sh
cd engine && go run ./cmd/serve -netfile ../data/networks/i280-woodside/i280.json \
  -run demo -ws 127.0.0.1:8443 -geojson ../viz/public/network.geojson
cd viz && pnpm dev   # open http://localhost:5173/?run=demo&ws=ws://127.0.0.1:8443
```

Or run from an ADR-0012 scenario directory (manifest + demand parts; explicit
`-seed`/`-ticks` override the manifest for sweeps):

```sh
cd engine && go run ./cmd/scenario validate path/to/scenario && go run ./cmd/scenario hash path/to/scenario
go run ./cmd/simrun -scenario path/to/scenario -seed 2
```

`data/networks/` is git-ignored per ADR-0009's recipe-not-file posture; the
bootstrap recipe to regenerate it is in `contracts/network-format-v1.md`.
