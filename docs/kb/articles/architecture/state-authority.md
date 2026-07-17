# State Authority

> How the authoritative engine distributes world state and accepts control over NATS: self-sufficient per-cell snapshots, declared interest windows, ego prediction, hold-last input buffers, no lag compensation.

## Overview

State authority is the question of who writes world state and who merely requests changes — and, given that answer, how state flows out to controllers and observers and how their input flows back in. traffic-sim decided early ([VISION](../../../VISION.md), ADR-0005/0008) that the engine is the sole writer and controllers are async NATS clients emitting intents; this topic researches the *mechanics* of that boundary, borrowing from 25 years of authoritative-server game netcode.

It matters because two flagship use cases live on this boundary: the multiplayer chaos demo (humans driving alongside AI over the open internet) and civic advocacy (replay trustworthy enough to show a traffic coordinator). The boundary determines cheat resistance, how a human-driven car *feels* at 150 ms RTT, what a snapshot costs in bytes, and whether replay stays deterministic.

The research found the game canon unusually decisive in both directions. **Take:** intents-in/state-out authority (with 20 years of WoW speedhack evidence for the alternative), interest management compiled to subject subscriptions, full client-side prediction of the ego vehicle with rewind-replay reconciliation, interpolation-in-the-past for everything else, and the Overwatch/Rocket-League input-buffer pattern. **Skip:** ack-baselined delta compression (impossible over ack-less core-NATS fan-out), lag compensation / server rewind (no aim verbs; machine latency sits under the human reaction floor), and world rollback (a P2P-lockstep solution to a problem this architecture doesn't have). A 2025 CCP dev blog — EVE Online's fan-out layer pushing ~10k msgs/s to clients over NATS in production — independently validates [ADR-0002](../../decisions/ADR-0002-nats-backbone.md)'s live-plane choice.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Authority boundary | [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Engine is sole writer; controllers emit one 4-axis Intent under exclusive per-vehicle claims and grants |
| Three-plane contract | [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) | Live (core) / record (JetStream) / config (KV) under `{ns}.{run}.{plane}.>`; engine sole writer of the record plane |
| Self-sufficient cell snapshots | `raw/arch-state-authority/implementation.md §2` | Every live message fully renders its cell for that tick; loss lowers update rate, never corrupts |
| Cell registry + window compilers | `raw/arch-state-authority/implementation.md §3–§4` | Static lane-graph cells as subjects; ego windows and viewport rectangles compile to subscription sets |
| Ego predictor + reconciliation | `raw/arch-state-authority/implementation.md §6` | TS client predicts its claimed vehicle with the shared clamp rule; rewinds/replays unacked intents per snapshot |
| Intent buffer + hold-last | `raw/arch-state-authority/implementation.md §7.1`, ADR-0008 | Per-controller buffer feeding tick-boundary batch application; starvation re-applies last intent per the per-axis persistence table |
| Interpolation buffer | `raw/arch-state-authority/implementation.md §7.2`, ADR-0005 | Non-ego vehicles render 200–300 ms in the past, interpolated along lane polylines |
| Resync path | `raw/arch-state-authority/implementation.md §5` | Late joiners materialize from KV latest-keys / `DeliverLastPerSubject`, attach live, discard by tick |
| `applied_tick` echo | `raw/arch-state-authority/implementation.md §7.4` | Per-observation ack driving reconciliation, the control-latency meter, and the HUD health glyph |
| Arbitrated intent log | [ADR-0005](../../decisions/ADR-0005-time-model.md) | JetStream keyframes + intent log + rolling CRC — replay and audit instrument |

## How It Works

### The boundary: intents in, state out

- Clients never assert state; they request outcomes. Gambetta's protocol sketch ("I want to move right" → "You're at (11, 10)"), Bernier's cheating argument, and WoW's client-authoritative-movement speedhack record (still generating exploit reports in 2024) all point one way. Ratified by [ADR-0008](../../decisions/ADR-0008-controller-contract.md): one 4-axis Intent (longitudinal / lateral / routing / signal), exclusive per-vehicle claims, grants-based roles, zero driving logic in the engine.
- The trust argument applies even to authenticated clients (Source's man-in-the-middle point): validation is a property of the protocol, not of client identity. Anti-cheat = intents-not-state + engine clamps + the audit-grade arbitrated intent log. Trackmania's 2021 scandal was solved by input-spike analysis on exactly such a stored log — validation after the fact beats validation in the hot path.

### Live state: self-sufficient per-cell messages; deltas only on the record plane

Why no Q3/Source-style delta compression on the live path:

1. Ack-baselined deltas require per-subscriber ack streams and per-subscriber baseline bookkeeping ("the newest one that the server knows for sure that the client has received" — Q3 keeps 32 gamestates per client for this).
2. Core NATS gives the publisher neither: it drops silently client-side (pending limits 65,536 msgs / 64 MiB) and disconnects slow consumers server-side — "protecting the system as a whole over accommodating a particular consumer."
3. A dropped delta over ack-less fan-out is indistinguishable from silence and poisons every subsequent delta until a full refresh the protocol cannot even request. Colyseus's property deltas work only because WebSocket is lossless — the exception that proves the rule.

So every live message is self-sufficient for its cell (TRIBES "Most Recent State" semantics — a loss just lowers the update rate). Keyframe+delta encoding is confined to the JetStream record plane, where consumer acks exist and keyframes bound any corruption. [ADR-0006](../../decisions/ADR-0006-nats-message-contract.md) ratified this with binary SoA vehicle frames of ~8–16 B/vehicle uncompressed (lane-id 16–24 bits + offset s 16 bits + quantized speed 8–12 bits + accel + flags — a derivation; no published per-vehicle wire sizes exist anywhere, so measure early). ADR-0002's 2026-07-17 clarification adds: small messages only on the hot path, no in-process controller fast path.

### Interest management: declared windows compiled to cell subscriptions

The engine partitions the network into static cells (lane/edge groups); each cell is a NATS subject in the state plane. Two declaration shapes compile to subscription sets:

- **Ego-relative windows** for drivers — the claimed vehicle's neighborhood. SUMO TraCI's context-subscription filter list (leader/follower, downstream/upstream distance, junction foes, relative lanes) is the requirements doc for window content; TraCI filters pull-side per step (with its measured 11× barrier cost), we filter once per window change at subscription time.
- **Viewport rectangles** for observers — MapLibre bbox → cell set.

Windows are velocity-padded (max road speed × snapshot interval — HLA's physically-correct-filtering trick) so fast entrants don't pop in at the edge, and recomputed on cell-boundary crossing, not per tick. Precedents: Photon's interest groups are literally this with a 256-group cap that NATS removes ("10s of millions of subjects"); HLA DDM's documented region-based implementation is "a multicast group for each publication region." The motivation is quantitative: at 8–16 B/vehicle and 10 Hz, a 100-vehicle window costs ≈ 8–16 kB/s, while whole-city fan-out of 10k vehicles would cost 0.8–1.6 MB/s *per client* — interest gating is non-negotiable at city scale. Within-cell overfill degrades by importance × staleness (Unity/TRIBES fill discipline), never silent tail-drops.

### Ego vehicle: full prediction, rewind-replay reconciliation

The human driver's client predicts its claimed vehicle locally:

1. Apply local inputs immediately against a shared clamp rule (gap-to-leader accel cap — a subset of the engine's IDM regime, [ADR-0007](../../decisions/ADR-0007-vehicle-model.md)); keep a history of (state, intent).
2. On each authoritative snapshot, the echoed `applied_tick` is the ack: discard acked intents, rewind to the authoritative state, replay unacked intents forward.
3. Corrections render as a decaying visual offset — velocity snaps, position smooths; sim state is never polluted, keeping replay/CRC discipline intact client-side.

Evidence: Rocket League is the vehicle-specific primary source — "Server buffers player inputs. Client predicts everything… 100% server authoritative" — and the cost asymmetry favors 10 Hz hard: 200 ms RTT costs RL 24 correction frames at 120 Hz; it costs us ~1.5–2 ticks. Mispredictions are Sweeney's "bumping into an enemy" case — the car-following clamp overriding intent in close interaction — rare and small. Prediction of the ego is rollback netcode scoped to one entity: GGPO's technique, one car of blast radius.

### Input pipeline: buffer, hold-last, bundle-unacked, echo

1. The engine holds a short per-controller intent buffer feeding ADR-0005's tick-boundary batch application — the tick never blocks waiting for input.
2. Starvation re-applies the last intent (Overwatch: "it duplicates the player's last known input"; Rocket League: "runs physics using previous player input") — for a car ≈ cruise. Which axes persist vs one-shot is fixed by ADR-0008's per-axis persistence table. Persistent starvation escalates to failover by the external default-driver fleet (pause-on-capacity-loss, ADR-0008) — superseding the earlier in-engine IDM/MRM fallback idea, consistent with zero driving logic in the engine.
3. Clients bundle all unacked intents in every message (OW's sliding window; Source's 2-cmds/packet), so single-message loss over at-most-once core NATS heals at the next send — TCP-grade loss healing without TCP stalls, cheap because intents are tiny and mostly repeated (drivers hold the wheel like OW players hold keys). The `{run}:{tick}:{seq}` idempotency key makes re-application safe.
4. Every observation echoes `applied_tick` of the last-applied intent: the reconciliation ack, the client's effective-control-latency meter, and the HUD health glyph (OW/RL icon precedent) that prevents "the engine feels broken" misreports.

OW's buffer-health feedback (sim-speed nudge 16.0 → 15.2 ms) is optional at 10 Hz: one buffered tick is 100 ms, enormous headroom over internet jitter at human input rates.

### Remote vehicles: interpolate in the past along the lane

All non-ego vehicles render 200–300 ms behind the newest snapshot — independently confirmed three ways: Fiedler's 3×-send-interval rule at 2–5% loss = 300 ms @ 10 Hz; Source's 100 ms @ 20 Hz absorbing one loss; Bernier's identical math at exactly 10 updates/s. Interpolation runs along the lane polyline: position is (laneId, s) per [ADR-0007](../../decisions/ADR-0007-vehicle-model.md)'s front-bumper convention, heading derived, velocity shipped for Hermite quality. Extrapolation past the newest snapshot is capped tight (~0.25 s, Source calibration) — brake-light events are precisely what extrapolation mangles. v1 publishes every vehicle every snapshot; the DIS dead-reckoning contract (threshold breach + heartbeat + timeout-evict; SISO defaults 1 m / 3°) is held in reserve as the publish-rate reducer when measured bandwidth says so.

### Late joiners and recovery: materialize, attach, discard-by-tick

1. Materialize current window state from KV latest-keys (or a `DeliverLastPerSubject` pull consumer filtered to the client's cells).
2. Attach live cell subscriptions.
3. Discard live messages with tick ≤ the materialized snapshot tick — free because ADR-0005 puts the tick count in every message.

A slow-consumer disconnect is a *recovery trigger*, not an error: reconnect → same resync path. This is Q3's gamestate/baseline split and Colyseus's full-then-deltas handshake, with NATS primitives playing both halves.

### Fairness: no lag compensation, ever

[ADR-0005](../../decisions/ADR-0005-time-model.md)'s no-rewind rule is reaffirmed with a quantitative budget: worst-case machine-added latency ≈ half-RTT (10–75 ms) + ≤1 tick buffering (100 ms) + interp buffer (200–300 ms) ≈ 0.3–0.5 s — under the 0.7 s alert-and-expecting driver floor and far under the 1.25–1.5 s typical (Green 2000; AASHTO designs for 2.5 s). Lag compensation exists to make aiming at interpolated targets fair; traffic has no aim verbs. Even Overwatch, with full rewind machinery, clamps it above ~220 ms RTT for fairness. Intra-tick ordering of competing intents — RESOLVED 2026-07-17 review, ratified in [ADR-0008](../../decisions/ADR-0008-controller-contract.md): a deterministic tie-break **list** (grant level, then vehicle ID), never CS2-style arrival-time resolution, which would leak wall-clock nondeterminism into replay.

## Gotchas

- **Delta compression over ack-less fan-out**: a dropped delta poisons every later delta until a full refresh the protocol can't request. "Property-level delta" (an encoding) survives on NATS; "delta against acked baseline" (a loss-recovery protocol) doesn't.
- **Client-authoritative movement**: WoW's 20-year speedhack/teleport record is the documented failure case for trusting client-reported state — and Source's MITM argument says authentication doesn't fix it; the protocol itself must not depend on client honesty.
- **Blocking the tick for input**: lockstep's latency equals the most lagged player; TraCI's barrier costs 11×; Rocket League: "Not good for rigid-body simulation." Starvation is handled by hold-last, never by waiting (ADR-0005).
- **Unbounded extrapolation**: it "starts to break down" exactly on interaction — brake lights are traffic's non-linear case. Cap at ~0.25 s; interpolate in the past instead.
- **Hold-last without per-axis semantics**: re-applying a one-shot lane-change request every starved tick would re-fire it; ADR-0008's per-axis persistence table (persistent accel vs one-shot requests) is the fix.
- **Runtime interest queries**: pure aura/nimbus intersection is O(C×V) per tick (Boulanger) and was SpatialOS QBI's expensive core — compile interest to static cell subscriptions instead.
- **KV direct-get doesn't read your writes**: read latest state from the stream leader, or accept one snapshot interval of staleness (harmless — the live stream heals it within 100 ms).
- **Arrival-time tie-breaking**: resolving competing intents by within-tick arrival order leaks wall-clock nondeterminism into replay; use the deterministic tie-break list (grant level, then vehicle ID).
- **Silent late input**: a driver whose intents arrive silently late misjudges the sim, not their connection — surface `applied_tick − send_tick` as a HUD glyph (OW/RL precedent).

## Open Questions

- **Cell geometry and size**: Euclidean grid vs graph neighborhood vs both compiled to one registry; needs a sizing experiment on a real imported network ([ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md) feeds the density data).
- **Ego-predictor fidelity**: which clamp rules ship to the TS client (gap-cap only or IDM-lite), and how Go↔TS shared logic stays in lockstep (the pm_shared problem — codegen constants from the AsyncAPI contract?).
- **Snapshot payload schema**: per-cell packed messages vs per-vehicle subjects with client conflation; measure payload count vs subject count at 10k vehicles. The 8–16 B/vehicle figure is a derivation with no published counterpart — measure at engine bring-up.
- **DR threshold publishing**: adopt at what measured per-client bandwidth? v1 publishes everything every snapshot.
- **Observer vs driver windows**: one cell registry with two compilers, or separate planes? Heatmaps want *all* cells sparsely — possibly a separate aggregate subject.
- **Buffer-health feedback** (OW sim-speed nudge): unnecessary at 10 Hz v1, or cheap insurance for the chaos demo's worst connections? Deferred to contract-ADR follow-up.

## Related

- [Time Model](../architecture/time-model.md) — the tick, no-rewind rule, interpolation buffer, and replay machinery this topic distributes and validates against the game canon
- [NATS Backbone](../architecture/nats-backbone.md) — the core/JetStream/KV split, slow-consumer stance, and `{run}:{tick}:{seq}` envelope these mechanics ride on
- [Road Graph Model](../architecture/road-graph-model.md) — cells are defined over the lane graph; geometry-by-reference ((laneId, s)) keeps snapshots small
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — the 4-axis Intent, claims/grants, and per-axis persistence this topic's buffers and windows serve
- [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) — viewport-rectangle subscriptions and interpolate-along-lane rendering on the client end
- [Simulator Landscape](../business-domains/simulator-landscape.md) — where this authority model sits relative to TraCI/CARLA and the surveyed simulators

---
*Raw research: [raw/arch-state-authority](../../raw/arch-state-authority/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
