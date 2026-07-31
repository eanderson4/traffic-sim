# Architecture & Data Flow (mermaid)

> Rendered from the code as of 2026-07-31 (`engine/`, `viz/src/`, `contracts/asyncapi.yaml` v2.7.0).
> The message contract is the source of truth for subjects and wire formats
> (`contracts/asyncapi.yaml`); these diagrams are a map, not a contract.
> Drift audit of the same date: see the reconciliation note in `docs/kb/INDEX.md`.
> **Nodes are clickable on GitHub** — they link to the source file each box is drawn from.

## High level

### 1. System architecture

The engine is the single authority over world state; everything else — drivers,
directors, viewers, replay — is a NATS client or a file consumer.

```mermaid
flowchart LR
    NET["Road network<br/>(OSM import)"]
    SCN["Scenario<br/>(demand + control config)"]

    subgraph ENG["Simulation engine (Go) — authoritative"]
        K["Simulation kernel<br/>fixed 100 ms tick"]
    end

    NATS{{"NATS backbone<br/>live · record · config planes"}}
    CTL["Controllers<br/>AI · human · default driver"]
    DIR["Director<br/>demand & signal verbs"]
    VIZ["Visualization<br/>MapLibre browser"]
    REC[("Recordings<br/>JetStream")]
    RPL["Replay & bake<br/>(deterministic re-sim)"]
    WEB["Static replay site<br/>(baked artifacts)"]

    NET --> K
    SCN --> K
    K <--> NATS
    CTL <--> NATS
    DIR --> NATS
    NATS --> VIZ
    K --> REC
    REC --> RPL --> WEB --> VIZ

    click K "https://github.com/eanderson4/traffic-sim/blob/main/engine/engine.go" "engine/engine.go — Engine.Step()"
    click NET "https://github.com/eanderson4/traffic-sim/tree/main/engine/netimport" "engine/netimport — OSM/netconvert importer"
    click SCN "https://github.com/eanderson4/traffic-sim/tree/main/engine/scenario" "engine/scenario — scenario format (ADR-0012)"
    click NATS "https://github.com/eanderson4/traffic-sim/blob/main/contracts/asyncapi.yaml" "contracts/asyncapi.yaml — message contract"
    click CTL "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/default-driver" "engine/cmd/default-driver — reference controller"
    click DIR "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/verb.go" "engine/natsio/verb.go — director verbs"
    click VIZ "https://github.com/eanderson4/traffic-sim/blob/main/viz/src/main.ts" "viz/src/main.ts — MapLibre app"
    click REC "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/recorder.go" "engine/natsio/recorder.go — record plane"
    click RPL "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/replay" "engine/cmd/replay — VCR replay"
    click WEB "https://github.com/eanderson4/traffic-sim/blob/main/contracts/baked-replay-v1.md" "contracts/baked-replay-v1.md — baked format"
```

### 2. Data flow — what crosses the engine boundary

```mermaid
flowchart LR
    subgraph IN["into the engine"]
        A["vehicle intents<br/>(acceleration · lane · route · setpoint)"]
        B["director verbs<br/>(spawn · signal override)"]
        C["claims & heartbeats<br/>(controller liveness)"]
    end

    ENG{"Engine tick<br/>authoritative<br/>world state"}

    subgraph OUT["out of the engine"]
        D["state snapshots<br/>→ live visualization"]
        E["observation frames<br/>→ controllers"]
        F["intents · keyframes · CRC<br/>→ recording (replay!)"]
        G["metrics<br/>→ run reports"]
    end

    IN --> ENG --> OUT

    click A "https://github.com/eanderson4/traffic-sim/blob/main/engine/intent.go" "engine/intent.go — the 4-axis Intent (ADR-0008)"
    click B "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/verb.go" "engine/natsio/verb.go — director verbs"
    click C "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/contract.go" "engine/natsio/contract.go — claims & heartbeats"
    click ENG "https://github.com/eanderson4/traffic-sim/blob/main/engine/engine.go" "engine/engine.go — Engine.Step()"
    click D "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/frame.go" "engine/natsio/frame.go — TSSF snapshot codec"
    click E "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/obsframe.go" "engine/natsio/obsframe.go — TSOB observation frames"
    click F "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/tslb.go" "engine/natsio/tslb.go — TSLB batched intent log"
    click G "https://github.com/eanderson4/traffic-sim/blob/main/engine/metrics.go" "engine/metrics.go — metric kernel (ADR-0014)"
```

### 3. Tick cycle — one 100 ms step

Controllers are asynchronous at the edges; inside the tick everything is
deterministic and ordered. Intents arrive any time and are batch-applied in
phase 2 of the next tick.

```mermaid
flowchart LR
    P0["0 · adaptive routing<br/>freeze epoch weights"]
    P1["1 · events<br/>spawn · directives<br/>signal overrides"]
    P2["2 · apply intents<br/>deterministic order<br/>safety gate"]
    P3["3 · car-following<br/>IDM acceleration"]
    P4["4 · integrate<br/>ballistic motion"]
    P5["5 · lane boundaries<br/>junction gates<br/>spawn / despawn"]
    P6["6 · lane changes<br/>commanded / MOBIL"]
    P7["7 · metrics + CRC<br/>publish snapshot"]

    P0 --> P1 --> P2 --> P3 --> P4 --> P5 --> P6 --> P7
    P7 -->|"next tick (10 Hz)"| P0

    click P0 "https://github.com/eanderson4/traffic-sim/blob/main/engine/routing.go" "engine/routing.go — adaptive routing (ADR-0036)"
    click P1 "https://github.com/eanderson4/traffic-sim/blob/main/engine/spawn.go" "engine/spawn.go — deterministic spawner"
    click P2 "https://github.com/eanderson4/traffic-sim/blob/main/engine/engine.go" "engine/engine.go — applyIntents + safetyGate (ADR-0025)"
    click P3 "https://github.com/eanderson4/traffic-sim/blob/main/engine/vehicle.go" "engine/vehicle.go — idmAccel (IDM)"
    click P4 "https://github.com/eanderson4/traffic-sim/blob/main/engine/engine.go" "engine/engine.go — integrate (ballistic)"
    click P5 "https://github.com/eanderson4/traffic-sim/blob/main/engine/rightofway.go" "engine/rightofway.go — rowGate / rowConflict (ADR-0010)"
    click P6 "https://github.com/eanderson4/traffic-sim/blob/main/engine/mobil.go" "engine/mobil.go — MOBIL lane changes"
    click P7 "https://github.com/eanderson4/traffic-sim/blob/main/engine/metrics.go" "engine/metrics.go — metrics + rolling CRC"
```

---

## Detailed reference

## 4. Live run — components and data flow

One `serve` process *is* the system for a live run: the NATS broker is embedded
in-process, the default driver and demand director run alongside the kernel, and
browsers attach over the broker's WebSocket listener.

```mermaid
flowchart LR
    subgraph browser["Browser (viz — vanilla TS + MapLibre)"]
        NC["nats-client.ts<br/>(nats.ws over WebSocket)"]
        MAP["MapLibre render<br/>vehicles / congestion / signals"]
        NC --> MAP
    end

    subgraph serve["engine/cmd/serve — one Go process"]
        K["kernel<br/>Engine.Step() @ 100 ms tick<br/>(engine/engine.go)"]
        DD["default driver<br/>(-driver, in-proc by default)<br/>IDM+MOBIL, routing"]
        DIR["demand director<br/>(scenario demand parts)<br/>spawn / signal verbs"]
        BR["embedded NATS broker<br/>WS listener :8443"]
        K <--> BR
        DD <--> BR
        DIR <--> BR
    end

    subgraph sinks["kernel outputs"]
        direction TB
        JS[("JetStream TS_LOG_{run}<br/>-store DIR (durable)<br/>TSLB intents · TSKF keyframes<br/>CRC · events · verbs")]
        KV[("KV ts_runs<br/>{run}/meta · {run}/state")]
        MET["-metrics-out JSON<br/>→ metview / runreport.py"]
    end
    EXT["external controllers<br/>(cmd/default-driver, humans, AI)<br/>same contract plane"]

    BR <-."WSS :8443 (binary frames)".-> NC
    EXT <--> BR
    K -- "sole writer" --> JS
    K --> KV
    K --> MET

    click K "https://github.com/eanderson4/traffic-sim/blob/main/engine/engine.go" "engine/engine.go — Engine.Step()"
    click DD "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/default-driver" "engine/cmd/default-driver — reference controller"
    click DIR "https://github.com/eanderson4/traffic-sim/tree/main/engine/natsio/demand" "engine/natsio/demand — demand director"
    click BR "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/server.go" "engine/natsio/server.go — embedded broker + WS listener"
    click NC "https://github.com/eanderson4/traffic-sim/blob/main/viz/src/nats-client.ts" "viz/src/nats-client.ts — browser NATS over WebSocket"
    click JS "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/recorder.go" "engine/natsio/recorder.go — JetStream record plane"
    click KV "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/registry.go" "engine/natsio/registry.go — KV metadata plane"
    click MET "https://github.com/eanderson4/traffic-sim/blob/main/engine/metrics.go" "engine/metrics.go — metric kernel"
    click EXT "https://github.com/eanderson4/traffic-sim/blob/main/contracts/asyncapi.yaml" "contracts/asyncapi.yaml — contract plane"
```

## 5. NATS planes and subjects

Three planes per ADR-0006, taxonomy `ts.{run}.{plane}.>`:

```mermaid
flowchart TB
    subgraph live["LIVE plane — core NATS, at-most-once, slow consumers dropped"]
        S1["ts.{run}.state.snap<br/>TSSF v1 binary SoA vehicle snapshot, 1/tick<br/>engine → viz"]
        S2["ts.{run}.state.sig<br/>TSSG v1 signal table, chunked, 20-tick rebroadcast"]
        S3["ts.{run}.state.sig.req<br/>request/reply catch-up (ADR-0016)"]
    end

    subgraph ctl["CONTRACT plane — core NATS (ADR-0008)"]
        C1["ts.{run}.ctl.hello — attach handshake (grants, cadence)"]
        C2["ts.{run}.ctl.intent.{id}<br/>ControlIntent v2 or TSIB v1 batch (ADR-0026)<br/>controller → engine"]
        C3["ts.{run}.ctl.claim/release/heartbeat.{id}<br/>exclusive per-vehicle claims"]
        C4["ts.{run}.ctl.verb.{id}<br/>director verbs: spawn · signal_set (ADR-0037)"]
        C5["ts.{run}.ctl.obs.{id} — TSOB observation frame<br/>engine → one controller"]
        C6["ts.{run}.ctl.ack.{id} / ctl.events.*<br/>applied_tick echo, claim/pause feed"]
    end

    subgraph rec["RECORD plane — JetStream TS_LOG_{run}, engine sole writer"]
        R1["ts.{run}.log.intents — TSLB v1, one batch/tick (ADR-0035)<br/>(legacy per-intent ts.{run}.log.intent still readable)"]
        R2["ts.{run}.log.keyframe — TSKF v2–v7 full-state snapshots (chunked, ADR-0015)"]
        R3["ts.{run}.log.crc — rolling state CRC (replay verification)"]
        R4["ts.{run}.log.event / log.verb — control events, accepted verbs"]
    end

    subgraph meta["METADATA plane — KV bucket ts_runs"]
        M1["{run}/meta — RunMeta (spec, seed, params, status)"]
        M2["{run}/state — StatePtr (tick, keyframe seq, CRC)<br/>late-joiner resync"]
    end
```

## 6. One tick — sequence

Fixed 100 ms authoritative tick (ADR-0005); controllers are async at the edges,
intents batch-applied inside the tick. Phase order per `engine/engine.go`:

```mermaid
sequenceDiagram
    participant D as Driver(s) / Director
    participant B as NATS broker (embedded)
    participant E as Kernel (Engine.Step)
    participant J as JetStream TS_LOG
    participant V as Viz (browser)

    D->>B: ctl.intent.{id} (v2 / TSIB), claims, verbs
    B->>E: buffered for next tick (never a barrier)
    Note over E: phase 0 — adaptive routing epoch step (ADR-0036)
    Note over E: phase 1 — events: spawner, directives, signal overrides
    Note over E: phase 2 — apply buffered intents (deterministic order)<br/>safety gate caps longitudinal axis (ADR-0025)
    Note over E: phase 3–4 — IDM accel + ballistic integration<br/>right-of-way / signal gates (ADR-0010/0011/0031)
    Note over E: phase 5–6 — lane-boundary handoff, lane changes<br/>gridlock escape (ADR-0034)
    Note over E: phase 7 — metrics + rolling CRC
    E->>B: state.snap (TSSF) · ctl.obs (TSOB) · ctl.ack
    B->>V: TSSF frame → render
    E->>J: log.intents (TSLB) · log.crc · keyframe (cadence)
```

## 7. Recording, replay, and baked replay

```mermaid
flowchart LR
    REC[("recording<br/>JetStream store dir<br/>TS_LOG_{run}")]

    subgraph livepath["VCR replay (cmd/replay)"]
        P["player.go — paced re-publish<br/>under {run}-replay, WS :8443<br/>HTTP ctl :8901 (pause/speed/seek)"]
    end

    subgraph bakepath["bake pipeline (cmd/bake, ADR-0023)"]
        RS["resim.go — strict CRC-verified<br/>re-simulation (divergence aborts)"]
        ART["baked artifacts<br/>TSRB vehicle chunks · TSRL lane-speed<br/>TSSG · lanes.json · PMTiles/GeoJSON<br/>index.json manifest"]
        RS --> ART
    end

    HOST["static hosting<br/>Cloudflare Pages /<br/>scripts/serve-baked.py"]
    VIZ1["browser viz — live path<br/>(nats-client.ts)"]
    VIZ2["browser viz — baked path<br/>baked.ts shim fetches chunks,<br/>re-encodes to synthetic TSSF<br/>→ same render loop"]

    REC --> P --> VIZ1
    REC --> RS
    ART --> HOST --> VIZ2

    click REC "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/recorder.go" "engine/natsio/recorder.go — record plane"
    click P "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/player.go" "engine/natsio/player.go — paced replay publisher"
    click RS "https://github.com/eanderson4/traffic-sim/blob/main/engine/natsio/resim.go" "engine/natsio/resim.go — CRC-verified re-simulation"
    click ART "https://github.com/eanderson4/traffic-sim/blob/main/contracts/baked-replay-v1.md" "contracts/baked-replay-v1.md — baked artifact format"
    click HOST "https://github.com/eanderson4/traffic-sim/blob/main/scripts/serve-baked.py" "scripts/serve-baked.py — local baked server"
    click VIZ1 "https://github.com/eanderson4/traffic-sim/blob/main/viz/src/nats-client.ts" "viz/src/nats-client.ts — live path"
    click VIZ2 "https://github.com/eanderson4/traffic-sim/blob/main/viz/src/baked.ts" "viz/src/baked.ts — baked transport shim"
```

## 8. Network & scenario authoring pipeline

```mermaid
flowchart LR
    OSM["OpenStreetMap extract"] --> NCV["netconvert (SUMO)<br/>scripts/import-city.sh"]
    NCV --> NI["cmd/netimport<br/>.net.xml → network JSON v1<br/>(contracts/network-format-v1.md)"]
    NI --> SC["scenario dir (ADR-0012)<br/>scenario.yaml manifest<br/>+ network + demand parts"]
    SC --> VAL["cmd/scenario<br/>validate / fmt / hash"]
    SC --> SRV["serve -scenario …"]
    PORT["cmd/portals — demand portal inventory"] -.-> SC

    click NCV "https://github.com/eanderson4/traffic-sim/blob/main/scripts/import-city.sh" "scripts/import-city.sh — OSM → netconvert"
    click NI "https://github.com/eanderson4/traffic-sim/tree/main/engine/netimport" "engine/netimport — .net.xml → network JSON"
    click SC "https://github.com/eanderson4/traffic-sim/tree/main/engine/scenario" "engine/scenario — scenario format (ADR-0012)"
    click VAL "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/scenario" "engine/cmd/scenario — validate / fmt / hash"
    click SRV "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/serve" "engine/cmd/serve — live run server"
    click PORT "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/portals" "engine/cmd/portals — demand portal inventory"
```

## 9. Deployment shape (as built)

ADR-0004 says local-first via docker-compose, but no compose file exists; the
actual deploy is a single GKE pod (`deploy/k8s/`): `demosrv` (HTTP :8900, serves
the viz menu + built app, spawns/kills one engine child) supervising `serve` or
`replay` (embedded broker, WS :8443). Baked replays bypass the pod entirely —
the browser reads static files straight from Cloudflare Pages.

```mermaid
flowchart TB
    subgraph pod["GKE pod (app.phantomjam.com)"]
        DS["demosrv :8900<br/>menu · /api/demo/* · /net/* chunked GeoJSON (ADR-0018)<br/>admin POSTs bearer-gated (ADR-0020)"]
        EN["engine child: serve | replay<br/>embedded NATS, WS :8443"]
        DS -->|spawn / kill| EN
    end
    BR2["browser"] -->|"HTTPS :8900"| DS
    BR2 <-->|"wss://ws.phantomjam.com :8443"| EN
    BR2 -->|"baked replays (?bake=)"| PAGES["Cloudflare Pages<br/>data.phantomjam.com"]

    click DS "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/demosrv" "engine/cmd/demosrv — demo launcher + static server"
    click EN "https://github.com/eanderson4/traffic-sim/tree/main/engine/cmd/serve" "engine/cmd/serve / cmd/replay — engine child"
    click PAGES "https://github.com/eanderson4/traffic-sim/blob/main/contracts/baked-replay-v1.md" "contracts/baked-replay-v1.md — baked artifacts"
```
