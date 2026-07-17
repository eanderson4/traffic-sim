# Synthesis: State Authority

> Researched: 2026-07-17 | Git HEAD: 6efd963 | Status: complete
> Feeds the future message-contract ADR (ADR-0006, with [[arch-nats-backbone]])
> and the engine↔controller contract ADR ([[concept-vehicle-controller-interface]]).
> This synthesis recommends; the ADRs decide.

## Summary

The research question: how should an engine that is authoritative over world
state distribute that state to, and accept control input from, external
controllers and observers over NATS — borrowing from authoritative-server
multiplayer game patterns (interest management, client prediction,
reconciliation, late/dropped input handling)?

The game canon turns out to be unusually decisive for us, in both directions.
What we **take**: intents-in/state-out authority (already decided, here
fortified with 20 years of WoW speedhack evidence), per-client interest
management compiled to subject subscriptions (Photon groups, HLA DDM regions),
full client-side prediction of the *ego* vehicle with rewind-replay
reconciliation (Bernier/Gambetta/Fiedler, and Rocket League as the
vehicle-specific primary source), interpolation-in-the-past for everything
else (the 200–300 ms buffer is exactly the field-standard 3× rule at 10 Hz),
and the Overwatch/Rocket-League input-buffer pattern (hold-last on starvation,
bundle-unacked for loss healing). What we **skip**: ack-baselined delta
compression (impossible over core-NATS fan-out — no per-subscriber acks), lag
compensation/server rewind (no aim verbs; machine latency sits under the
human reaction-time floor), and world rollback (a P2P-lockstep solution to a
problem our architecture doesn't have). The sharpest new finding: CCP's 2025
dev blog reveals EVE Online's new fan-out layer pushes ~10k msgs/s to clients
over NATS in production — ADR-0002's live-plane choice is now
industry-validated by a game company.

## Source Files

- [Mechanics: authority model, snapshots, interest management, prediction, input buffers](./implementation.md)
- [Prior art survey](./competitors.md)
- [Standards, formalisms, named patterns, anti-patterns](./standards-and-patterns.md)

## Key Architectural Findings → Recommended Decisions

### 1. Live path = self-sufficient per-cell snapshot messages; deltas live only on the record plane
**Choice:** Every live state message on core NATS is self-sufficient for the
cell it covers (Most-Recent-State semantics, Tribes-style): loss reduces update
rate, never corrupts baselines. Delta-against-baseline encoding is confined to
the JetStream record plane (keyframe + deltas), where consumer acks exist —
and even there, keyframes bound any corruption.
**Why:** Q3/Source delta compression requires per-client ack streams and
per-client baseline bookkeeping ("the newest one that the server knows for
sure that the client has received" — [jfedor](https://www.jfedor.org/quake3/));
core NATS deliberately drops silently client-side and disconnects server-side
("protecting the system as a whole over accommodating a particular consumer" —
[slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).
A dropped delta over ack-less fan-out poisons every subsequent delta until a
full refresh the protocol can't even request. Colyseus's property deltas work
only because WebSocket is lossless — the exception that proves the rule.
**Trade-off:** More bytes per message than deltas (mitigated: our derivation
says 8–16 B/vehicle uncompressed, and lane-constrained quantization is cheap);
no published per-vehicle wire sizes exist anywhere, so we must measure early
(explicit field gap, [implementation §9](./implementation.md)).
**Field context:** [implementation §2](./implementation.md).

### 2. Interest management = declared windows compiled to cell subscriptions
**Choice:** The engine partitions the network into static cells (lane/edge
groups — geometry TBD with [[arch-road-graph-model]]); each cell gets a NATS
subject in the state plane. Two declaration shapes compile to cell
subscription sets: **ego-relative windows** for controllers (drivers see their
claimed vehicle's neighborhood; filter semantics per
[[concept-vehicle-controller-interface]]) and **viewport rectangles** for
observers (MapLibre bbox → cells). Windows are velocity-padded (max road speed
× snapshot interval, HLA's physically-correct-filtering trick) and recomputed
on boundary crossing, not per tick. Late-window entrants arrive via the same
resync path as late joiners (decision 6).
**Why:** Photon's interest groups are literally this with a 256-group cap
("assign an interest group per 'area'" — [Photon](https://doc.photonengine.com/realtime/current/gameplay/interestgroups));
HLA DDM's documented region-based implementation is "a multicast group… for
each publication region" ([Fujimoto](http://simulation.su/uploads/files/default/2017-fujimoto-1.pdf));
NATS removes the cap ("10s of millions of subjects" —
[subjects](https://docs.nats.io/nats-concepts/subjects)). Runtime query
evaluation (SpatialOS QBI, aura/nimbus intersection) is the scaling trap
Boulanger documents ([thesis](https://www.cs.mcgill.ca/~jboula2/thesis.pdf)) —
compile interest to subscriptions instead. TraCI's context-subscription filter
list (leader/follower, downstream distance, junction foes) is the requirement
doc for the ego-window *content* API
([TraCI](https://sumo.dlr.de/docs/TraCI/Object_Context_Subscription.html)).
**Trade-off:** Static cells are a compromise between Euclidean grids
(observer-natural) and graph neighborhoods (driver-natural); cell size vs
payload size vs subject count needs one sizing experiment on a real imported
network. Within-cell overfill degrades by importance × staleness (Unity/Tribes
fill discipline), never silent tail-drops.
**Field context:** [implementation §3–§4](./implementation.md).

### 3. Human clients fully predict the ego vehicle; reconciliation = rewind-replay of unacked intents
**Choice:** The browser driver client runs a local predictor for its claimed
vehicle: shared clamp rule (gap-to-leader accel cap — the pm_shared subset
question), immediate application of local inputs, history of (state, intent);
on each authoritative snapshot, discard acked intents (the echoed
`applied_tick` is the ack), rewind to the authoritative state, replay unacked
intents. Corrections are smoothed as a decaying visual offset; velocity snaps,
position smooths. Everything except the ego vehicle is interpolated in the
past (decision 5).
**Why:** Bernier's footnote — partial prediction "would still leave the
player's movements lagged (often described as if you are moving around on ice
skates)" — plus Rocket League's vehicle-specific proof: "Server buffers player
inputs. Client predicts everything… 100% server authoritative"
([deck](https://media.gdcvault.com/gdc2018/presentations/Cone_Jared_It_Is_Rocket.pdf)).
Our mispredictions are Sweeney's "bumping into an enemy" case — the car-
following clamp overriding intent in close interaction — rare and small. Cost
asymmetry favors us hard: 200 ms RTT costs RL 24 correction frames at 120 Hz;
it costs us ~2 ticks at 10 Hz.
**Trade-off:** A subset of engine physics must exist in TS (the Go→TS shared-
code question, pm_shared precedent) — version it with the contract. Prediction
of the ego is rollback netcode scoped to one entity: the technique is GGPO's,
the blast radius is one car.
**Field context:** [implementation §6](./implementation.md),
[standards: rollback formalism](./standards-and-patterns.md).

### 4. Input pipeline: per-controller buffer, hold-last on starvation, bundle-unacked, echo `applied_tick`
**Choice:** The engine holds a short per-controller intent buffer feeding
ADR-0005's tick-boundary batch application. Starvation re-applies the last
intent (Overwatch: "it duplicates the player's last known input"; Rocket
League: "runs physics using previous player input") — for a car this ≈ cruise,
and if starvation persists it escalates to the disconnect fallback already
decided in [[concept-vehicle-controller-interface]] (IDM or MRM, logged).
Clients bundle all unacked intents in every message (OW's sliding window;
Source's 2-cmds/packet), so single-message loss over at-most-once core NATS
heals at the next send. Every observation echoes the `applied_tick` of the
client's last-applied intent — the ack channel for reconciliation (decision
3), the client's control-latency meter, and the HUD health glyph (OW/RL icon
precedent).
**Why:** This is the Overwatch pattern scaled to 10 Hz, where one buffered
tick is 100 ms — enormous headroom over internet jitter at human input rates,
which is why OW's sim-speed nudge (16.0→15.2 ms dilation) is optional for us.
The bundle-unacked trick gives TCP-grade loss healing without TCP stalls —
the exact property that makes shooter netcode use UDP, applied to intents that
are tiny and mostly repeated (drivers hold the wheel like OW players hold
keys).
**Trade-off:** Bundling requires an idempotency key per intent — already
specified as `{run}:{tick}:{seq}` in [[arch-nats-backbone]]'s recommended
envelope. Hold-last must be specified per intent axis (persistent accel vs
one-shot lane request — the same open table flagged by
[[concept-vehicle-controller-interface]]).
**Field context:** [implementation §7.1](./implementation.md).

### 5. Remote vehicles are interpolated in the past along lane geometry; DR thresholds are the measured-bandwidth escape valve
**Choice:** All non-ego vehicles render 200–300 ms behind the newest snapshot
(ADR-0005's buffer), interpolated along the lane polyline — position is
(laneId, s), heading derived, velocity shipped for Hermite quality.
Extrapolation past the newest snapshot is capped tightly (~0.25 s, Source
calibration). v1 publishes every vehicle every snapshot; the DIS contract
(threshold-breach + heartbeat + timeout-evict, SISO defaults 1 m/3°) is held
in reserve as the publish-rate reducer when measured bandwidth says so.
**Why:** The 200–300 ms buffer is independently confirmed by three sources
(Fiedler's 3×-interval rule at 2–5% loss = 300 ms @ 10 Hz; Source's 100 ms @
20 Hz absorbing one loss; Bernier's identical math at exactly 10 updates/s).
Lane-constrained interpolation is strictly easier than the games' 3-space
problem — geometry-by-reference ([[arch-road-graph-model]]) means clients
already hold the paths. DIS dead reckoning's premise ("don't publish what
receivers can extrapolate") fits steady-state cruising perfectly, but v1
simplicity beats speculative bandwidth saving at 8–16 B/vehicle.
**Trade-off:** Interpolation adds display latency to every non-ego vehicle —
acceptable because it's under the human reaction floor (decision 7) and
because brake-light events are precisely what extrapolation mangles (Fiedler's
warning).
**Field context:** [implementation §7.2–§7.3](./implementation.md).

### 6. Late joiners and recovering clients: materialize, attach, discard-by-tick
**Choice:** Catch-up = read current AoI state from KV latest-keys (or a
`DeliverLastPerSubject` pull consumer filtered to the client's cells), then
attach live cell subscriptions, discarding live messages with tick ≤ the
materialized snapshot tick. A slow-consumer disconnect is a *recovery
trigger*, not an error: reconnect → same resync path.
**Why:** Q3's gamestate/baseline split and Colyseus's handshake (full state,
then deltas) are the game-form of this recipe; NATS provides both halves
natively ([KV watch](https://docs.nats.io/nats-concepts/jetstream/key-value-store),
[consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)). The
discard-by-tick race resolution is free because ADR-0005 puts the tick in
every message.
**Trade-off:** KV direct-get has the documented no-read-your-writes caveat —
read from the stream leader, or accept one snapshot interval of staleness
(harmless: the live stream heals it within 100 ms).
**Field context:** [implementation §5](./implementation.md).

### 7. No lag compensation, ever — the fairness budget is quantitative
**Choice:** Reaffirm ADR-0005: no server rewind, no time-warped adjudication.
Late intents apply later, and the client sees its effective control latency
(decision 4's echo). Anti-cheat = intents-not-state + engine clamps +
audit-grade intent log.
**Why:** Lag compensation exists to make *aiming at interpolated targets* fair
(Bernier); traffic has no aim verbs. The quantitative budget: worst-case
machine-added latency ≈ half-RTT + ≤1 tick + interp buffer ≈ 0.3–0.5 s, under
the 0.7 s *alert-and-expecting* driver floor and far under the 1.25–1.5 s
typical (Green 2000; AASHTO designs for 2.5 s). Even Overwatch, with full
rewind machinery, clamps it above ~220 ms RTT for fairness. WoW supplies the
counterexample for the trust boundary; Trackmania's 2021 scandal supplies the
proof that an arbitrated input log is a working audit instrument (input-spike
analysis on stored replays caught the cheaters).
**Trade-off:** A human on a 500 ms connection will feel their car respond
sluggishly — surfaced via the HUD glyph rather than hidden by machinery that
would compromise determinism.
**Field context:** [implementation §1, §7.4, §8](./implementation.md).

## Compare/Contrast: Our Approach vs the Field

| Dimension | Q3/Source (FPS canon) | Overwatch / Rocket League | WoW / iRacing (client-heavy) | EVE Online | TraCI / CARLA (sim canon) | HLA / DIS (fed canon) | us (recommended) |
|---|---|---|---|---|---|---|---|
| Authority | server | server ("100%") | **client movement** | server | engine | federation | **engine, single writer** |
| Tick | 20–66 Hz | 60–120 Hz | — | **1 Hz** | 1–10 Hz | federate-local | **10 Hz** |
| AoI mechanism | PVS / areamask | per-client | distance cull | grid cubes | **ego context subscriptions (TraCI)** / none (CARLA) | **DDM regions** | **cell subjects, declared windows** |
| Delta scheme | ack-baselined | delta + heal | change-triggered | — | pull per step | on-threshold + heartbeat | **self-sufficient live; keyframe+delta on record** |
| Input handling | cmd batching | **buffer + hold-last + sliding window** | client-simulated | intent-level | **blocking barrier** | federate pull | **buffer + hold-last + bundle-unacked** |
| Prediction | ego + shared code | predict everything | ego = authority | minimal | — | DR extrapolation | **ego predicted, others interpolated** |
| Rewind | lag comp (hitscan) | clamped lag comp | — | TiDi (slows world) | — | — | **none (by ADR-0005)** |
| Loss recovery | `cl_fullupdate` | heal-at-keyframe | — | — | re-pull | heartbeat/timeout | **KV/snapshot resync, discard-by-tick** |
| Audit | demo files | — | — | — | re-run | — | **intent log + keyframes + CRC** |

## The Genuine Gap (narrower than the siblings', still real)

The individual patterns are all published — this topic is better-mapped than
[[arch-time-model]] or [[arch-nats-backbone]] found theirs. What remains
unwritten: (a) an interest-management design compiled to **broker subject
taxonomies** rather than engine-internal filtering (Photon's 256-group cap kept
everyone else from going far down this road; NATS's subject economics remove
it, and nobody has published what they built past it — though CCP's NATS
fan-out layer is now confirmed in production without design details);
(b) client prediction of a **lane-constrained** vehicle, where the predictor
needs a clamp rule, not a physics engine; (c) the combination of ego
prediction + authoritative clamps + an audit-grade arbitrated intent log in
one contract. Our ADR-0006 write-up covers new ground in the specifics, not
the principles.

## Open Questions

- Cell geometry and size: Euclidean grid vs graph neighborhood vs both
  compiled to one registry; needs a sizing experiment on a real imported
  network ([[integration-osm-extraction]], [[arch-road-graph-model]]).
- Ego-predictor fidelity: which clamp rules ship to the TS client (gap-cap
  only, or IDM-lite?), and how the Go↔TS shared logic is kept in lockstep
  (pm_shared problem — codegen constants from the contract?).
- Snapshot payload schema: per-cell packed messages vs per-vehicle subjects
  with client conflation; measure payload count vs subject count at 10k
  vehicles. No published per-vehicle wire sizes exist — our 8–16 B derivation
  needs measurement.
- DR threshold publishing: adopt at what measured per-client bandwidth? v1
  publishes everything every snapshot.
- Observer (viz) windows vs driver windows: same cell registry with two
  compilers, or separate planes? Interacts with
  [[integration-maplibre-realtime]] heatmap needs (heatmaps want *all* cells,
  sparsely — possibly a separate aggregate subject, cf. [[domain-congestion-metrics]]).
- Buffer-health feedback (OW sim-speed nudge): unnecessary at 10 Hz v1, or
  cheap insurance for the chaos demo's worst connections? Defer to the
  contract ADR.
- ~~Intra-tick ordering of competing intents~~ **RESOLVED 2026-07-17 review:**
  deterministic tie-break **list** (e.g. grant level, then vehicle ID), not
  CS2-style arrival-time resolution within the tick — arrival time would leak
  wall-clock nondeterminism into replay. Belongs to the contract ADR.

## Connections to Other Topics

- **Honors constraints from:** [[arch-time-model]] / ADR-0005 — single-writer
  goroutine, tick-boundary intent application, no rewind, 200–300 ms interp
  buffer, intent log + keyframes + CRC (this research validates each against
  the game canon rather than re-opening it); [[arch-nats-backbone]] — core for
  live / JetStream for record / KV for latest, slow-consumer disconnect stance,
  `{run}:{tick}:{seq}` idempotency envelope, subject taxonomy (cells slot into
  the state plane).
- **Constrains:** [[concept-vehicle-controller-interface]] (its declared AoI
  window is *implemented* by this topic's cell subscriptions; hold-last
  semantics per intent axis shared between the two ADRs; `applied_tick` echo
  added to observations), [[integration-maplibre-realtime]] (viewport-rectangle
  subscriptions; interpolation-along-lane client rendering; heatmap aggregation
  question above), future ADR-0006 (message contracts: snapshot schema, window
  declaration, intent bundling, resync protocol).
- **Depends on:** [[arch-road-graph-model]] (cell definitions over the lane
  graph; interpolation along polylines; geometry-by-reference keeps snapshots
  small), [[integration-osm-extraction]] (real network densities size cells).
- **Relates to:** [[domain-traffic-flow-models]] (clamp rule the ego predictor
  shares = IDM's interaction regime; reaction-time evidence corroborates
  T′_eff from ADR-0005), [[domain-congestion-metrics]] (metric consumers are
  observers with whole-network interest — queue-group scaling per
  [[arch-nats-backbone]]), [[concept-scenario-format]] (scenario config chooses
  fallback policy and window defaults), [[domain-simulator-landscape]]
  (positions our authority model against the surveyed simulators),
  [[domain-signal-control]] (signal controllers are controllers too — same
  window/buffer/echo machinery, tighter latency tolerance).
