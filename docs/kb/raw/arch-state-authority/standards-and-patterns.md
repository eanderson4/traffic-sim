# Standards & Patterns: State Authority

> Source: academic research + pattern identification | Researched: 2026-07-17

## Standards

### IEEE 1278 (DIS) — Distributed Interactive Simulation
What it defines: entity-state PDU exchange with **dead reckoning** as the core
bandwidth contract — a simulator publishes an update only when (a) the
discrepancy between actual and dead-reckoned state exceeds a threshold, (b) a
heartbeat interval elapses, or (c) the DR algorithm changes; receivers
extrapolate between updates and evict entities whose heartbeat times out
([1278.1 draft](https://freewrl.sourceforge.io/tests/28_Distributed_interactive_simulation/1278.1-200X%20Draft%2016%20rev%2018.pdf),
[Prepar3D consumer behavior](https://www.prepar3d.com/prepar3d/network/distributed_interactive_simulation/distributed_interactive_simulation_overview.html)).
Annex B names nine DR algorithms (Static, FPW, RPW, RVW, FVW, FPB, RPB, RVB,
FVB — Fixed/Rotating × Position/Velocity × World/Body)
([Open-DIS javadoc](https://open-dis.sourceforge.net/javadoc/open-dis/docs/edu/nps/moves/deadreckoning/DIS_DeadReckoning.html), ⚠ 403 to fetchers; class list
via search index).
**How we relate:** we simplify where DIS generalizes — our vehicles extrapolate
in 1-D along a known lane polyline, so a "constant speed along lane" DR model
(FPW-analogue) has bounded error by construction. We adopt the *contract*
(threshold + heartbeat + timeout-evict) as an optional publish-rate reducer,
not the wire format. SISO's RPR FOM defaults (1 m / 3°) are the calibration
starting point ([SISO-STD-001-2015](https://cdn.ymaws.com/www.sisostandards.org/resource/resmgr/standards_products/siso-std-001-2015_grim_rpr_f.pdf)).

### IEEE 1516 (HLA) — Data Distribution Management
What it defines: value-based filtering over an N-dimensional **routing space**:
publishers tag data with update regions, subscribers declare subscription
regions, overlap implies delivery; layered on class-based Declaration
Management (Fujimoto, [PDF](http://simulation.su/uploads/files/default/2017-fujimoto-1.pdf);
NTU thesis distinguishing class- vs value-based filtering,
[PDF](https://dr.ntu.edu.sg/server/api/core/bitstreams/1d15f515-3f5d-479a-925d-e6b0d4101ba0/content)).
Physically correct filtering pads regions for latency and motion (Van Hook &
Calvin, [abstract](https://www.researchgate.net/publication/2267798)).
**How we relate:** our subject-per-cell design is DDM's documented region-based
implementation with NATS subjects as the multicast groups. Class-based
filtering (publish/subscribe by object class) maps to our plane/class token in
the subject taxonomy ([[arch-nats-backbone]]); value-based filtering maps to
cell membership. We comply in spirit, deviate in mechanism: no RTI, no region
re-computation engine — cells are static and compiled to subscription lists.

### AASHTO Green Book / human-factors canon (the fairness standard that isn't one)
AASHTO's 2.5 s design perception-brake time ([TRB EC003](http://onlinepubs.trb.org/onlinepubs/circulars/EC003/ch33.pdf),
⚠ scanned; corroborated by [LA city manual](https://completestreetdesignmanual.engineering.lacity.gov/e-400-general-roadway-design-elements/e-440-sight-distance/e-442-safe-stopping-distances))
and Green 2000's measured values (0.7 s expected / 1.25 s unexpected / 1.5 s
surprise, *Transportation Human Factors* 2(3):195–216,
[author summary](https://www.visualexpert.com/Resources/reactiontime.html)).
**How we relate:** not a software standard — it is the quantitative fairness
budget our latency design must sit under, and it sits comfortably (machine
adds 0.3–0.5 s worst case vs a 0.7 s human floor). Green's caveat — "A
'standard' or 'generally accepted' PRT cannot and does not exist" — applies to
any attempt to tune sim behavior to a single reaction-time number.

### No wire/contract standard exists for this layer
There is no AsyncAPI-equivalent *semantic* standard for "authoritative sim
state fan-out": DIS/HLA are federation standards with their own wire formats;
game netcode is folklore + engine docs. Our contract surface (subjects,
snapshot schema, intent envelope, attach handshake) is therefore defined by
our own ADRs — AsyncAPI for syntax per [[arch-nats-backbone]], semantics here
and in [[concept-vehicle-controller-interface]].

## Formalisms

### Client-side prediction + server reconciliation
Store (state, input) history locally; on each authoritative update, discard
acked inputs, rewind to the authoritative state, replay unacked inputs forward
(Bernier's sliding window; Gambetta's sequence-numbered worked example;
Fiedler's circular-buffer formulation — URLs in implementation.md §6.1).
Prediction quality requires shared movement code (Bernier's `pm_shared/`) and
bounded prediction horizons (Fiedler: "a reasonable 1/2 second prediction
giving approximately the same result," not bit-exactness).
**Mapping:** applies to exactly one entity per human client — the ego vehicle.
Reconciliation inputs: engine snapshot (with `applied_tick` echo) + local
intent history. Corrections are clamp-driven, small, and smoothed visually.

### Entity interpolation (remote entities rendered in the past)
Render remote entities ~2–3 send-intervals behind the newest snapshot; buffer
sized to survive ~2 consecutive losses (Source `cl_interp` 0.1 @ 20 Hz;
Fiedler's 3× rule @ 2–5% loss = 300 ms @ 10 Hz); Hermite splines with shipped
velocity beat linear at equal rate; extrapolation capped (~0.25 s Source).
**Mapping:** our 200–300 ms buffer (ADR-0005) is this rule at our rates;
interpolation along lane polylines replaces 3-space splines
([[arch-road-graph-model]]).

### Lag compensation (server rewind) — formally scoped out
Server reconstructs the world *as the shooter saw it* to adjudicate aim
(Bernier's algorithm; Gambetta part 4). Exists to make aiming at interpolated
targets fair; costs history storage + creates shot-around-corner paradoxes;
Overwatch clamps it above ~220 ms RTT.
**Mapping:** no aim verbs in traffic ⇒ no rewind (ADR-0005 decision 3, reaffirmed by
the human-factors budget in implementation.md §8). If a future scenario needs
"who entered the gap first" adjudication, the arbitrated intent log gives a
deterministic answer without runtime rewind.

### Dead reckoning (DIS formalism)
Publisher and receiver share an extrapolation model; publisher sends on
threshold breach + heartbeat. Formalizes latest-state conflation: the value of
an update is the *error it corrects*, not the state itself.
**Mapping:** optional publish-rate reducer; also supplies the AoI eviction
rule (timeout) and the metric for when a vehicle "needs" publishing.

### Rollback / speculative execution (GGPO family)
Rewind shared world state to first divergent frame and re-simulate; requires
full-state serialization per frame and re-simulation determinism; NetherRealm
paid "two man-years" for serialization alone.
**Mapping:** scoped out for the world (we have an authority); scoped *in* for
the ego vehicle, where the identical loop runs on one entity client-side.
The technique is the same; the blast radius differs by four orders of
magnitude.

### Aura/nimbus and interest cost models
Aura = an entity's presence radius; nimbus = an observer's awareness radius;
interaction requires intersection — and naive pairwise intersection is the
scaling failure ("does not scale well because of the cost of computing the
intersection," Boulanger,
[thesis](https://www.cs.mcgill.ca/~jboula2/thesis.pdf)). Spatial partitioning
(grid/cell hashing) is the standard complexity fix.
**Mapping:** cells are precomputed aura partitions; subscriptions replace
runtime intersection tests. Recompute happens only on cell-boundary crossing
(or window re-declaration), not per tick.

## Design patterns identified

### Intents in, state out (the authority boundary)
Clients send bounded requests; the server clamps and owns all state writes.
Failure mode documented at 20-year scale: WoW client-authoritative movement →
speedhacks. Our clamp chain lives in [[concept-vehicle-controller-interface]];
this topic adds the fan-out and prediction halves of the boundary.

### Self-sufficient snapshot messages (Most-Recent-State delivery)
Each live message carries everything needed to render that tick's cell state;
loss reduces update rate, never corrupts baselines. Required by core-NATS
drop/disconnect semantics; the broker-compatible alternative to ack-baselined
delta. Keyframes+deltas remain legal on the JetStream record plane, where
consumer acks exist ([[arch-nats-backbone]]).

### Interest-as-subscription (compiled, static cells)
Interest sets are compiled to subject subscriptions at attach/window-change
time (Photon groups precedent), not evaluated per publish (SpatialOS QBI's
expensive core). Two declaration shapes compile to the same cell sets:
ego-relative windows (drivers; TraCI filter list as requirement doc) and
viewport rectangles (observers; HLA subscription regions).

### Velocity-padded windows
Pad any spatial window by max-entity-speed × snapshot interval so entrants
appear before they're interactively relevant (Van Hook & Calvin). For us the
padding is graph distance along the lane, and max speed is the road limit.

### Importance × staleness budget fill
When a tick's per-cell payload exceeds budget, fill by importance
(distance-scaled) with staleness boosting priority next tick (Unity Netcode
for Entities; TRIBES priority-ordered fill). The degradation is graceful and
visible (older updates for far vehicles), never silent tail-dropping.

### Input buffer + hold-last + bundled redundancy
Per-controller server-side buffer; starvation re-applies the last intent
(Overwatch, Rocket League); every client message bundles all unacked intents
(OW sliding window; Source 2-cmds/packet) so single-message loss heals
naturally over at-most-once transport. Optional at 10 Hz: buffer-health
feedback nudging client send rate (OW time dilation).

### Ego-predict / others-interpolate split
The only predicted entity is the one the client owns; everything else is
interpolated in the past. Universal in the canon (Bernier, Gambetta part 3,
Rocket League); minimizes both bandwidth and misprediction surface.

### Visual-offset error smoothing
Render at simulated-state + decaying visual offset; snap derivatives
(velocity), smooth positions; never pollute sim state with render corrections
(Fiedler; Source `cl_smoothtime`). Keeps our replay/CRC discipline intact
client-side.

### Heartbeat/timeout eviction
No update within N intervals ⇒ remove entity from the client's world
(Prepar3D DIS timeout). Doubles as AoI-leave signaling when cells are coarse.

### Lag surfacing
Expose connection/control-latency health in the client UI (OW lightning bolt,
RL connection icons); for us driven by the echoed `applied_tick` — the client
can compute its effective control latency every round trip.

### Replay-as-audit
The arbitrated intent log (ADR-0005) doubles as the anti-cheat/forensics
instrument — Trackmania's 2021 scandal was solved from exactly such a log.
Validation after the fact beats validation in the hot path.

## Anti-patterns (documented failures)

1. **Client-authoritative movement/state** — WoW's 20-year speedhack record;
   Bernier: "fine if you can trust the client" is never true on the open net.
2. **Delta-against-acked-baseline over ack-less fan-out** — Q3/Source delta
   needs per-subscriber acks; core NATS drops silently and disconnects the
   slow. A dropped delta chain corrupts all subsequent state until a full
   refresh the protocol can't even request.
3. **Blocking the tick waiting for input** — lockstep's "latency equal to the
   most lagged player" (Fiedler); TraCI's 11× ([[arch-time-model]]); Rocket
   League: "Not good for rigid-body simulation."
4. **Unbounded extrapolation** — "extrapolation starts to break down" on
   interaction (Fiedler); Source caps at 0.25 s. Brake events are traffic's
   non-linear case.
5. **World rollback to hide latency** — two man-years of serialization cost
   (MKX) to solve a P2P problem we don't have; late intents apply later.
6. **Pure aura/nimbus runtime intersection** — O(C×V) per tick (Boulanger);
   partition into cells instead.
7. **Trusting client-reported outcomes even from authenticated clients** —
   Source's man-in-the-middle argument: validation is a property of the
   protocol, not of client identity.
8. **Silent late-input degradation** — a human driver with silently-late
   intents misjudges the sim; surface it (OW/RL icons).

## Empirical anchors

- Interpolation buffer: 3× send interval @ 2–5% loss (Fiedler); Source 100 ms
  @ 20 Hz; our 200–300 ms @ 10 Hz (ADR-0005) — consistent.
- Extrapolation cap: 0.25 s (Source `cl_extrapolate_amount`).
- Per-entity wire size: 321 bits raw → ~80 bits compressed rigid body
  (Fiedler); our derivation 8–16 B/vehicle uncompressed (lane-constrained) —
  no published traffic-sim per-vehicle wire size exists (explicit gap).
- Client budgets: Q3 3000 B/s; Source 4500–10000 B/s guidance; Tribes 2 KB/s;
  OW launch 20.8 Hz → 60 Hz high-bandwidth.
- Correction cost: 24 frames @ 200 ms RTT/120 Hz (Rocket League) vs ~1.5 ticks
  @ 150 ms RTT/10 Hz (us).
- Human PRT: 0.7 s expected / 1.25 s unexpected / 1.5 s surprise (Green 2000);
  2.5 s AASHTO design.
- DIS DR defaults: 1 m / 3°; ACM heartbeat 4.8 s in practice.
- NATS (via [[arch-nats-backbone]]): pending limits 65,536 msgs/64 MiB then
  drop; server `write_deadline` disconnect; loopback RTT ~65 µs; >1M msgs/s.
- EVE Quasar: ~10k msgs/s to clients over NATS in production (CCP, 2025).

## Open Questions

- Ego-prediction fidelity: which clamp rules ship client-side (pm_shared
  subset) — and in which language-shared form (Go→TS port? shared constants)?
- Cell size vs snapshot payload vs subject count: needs measured vehicle
  density on real networks ([[integration-osm-extraction]] feeds).
- Whether viewport-rectangle subscriptions (observers) and ego windows
  (drivers) share one cell registry or two — leaning one, with two compilers.
- DR adoption trigger: what measured per-client bandwidth justifies threshold
  publishing; v1 publishes all vehicles every snapshot.
- Intra-tick intent ordering rule (two controllers, one gap): deterministic
  tie-break spec belongs to the contract ADR (CS2 sub-tick is the pattern if
  arrival-time resolution is wanted).

## Master source list

Game canon: [Bernier 2001](https://developer.valvesoftware.com/wiki/Latency_Compensating_Methods_in_Client/Server_In-game_Protocol_Design_and_Optimization)
(⚠ Anubis-gated; via [full-text mirror](https://github.com/joexi/Latency-Compensating)) ·
[Source Multiplayer Networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)
(⚠ via [Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)) ·
[Gambetta 1](https://www.gabrielgambetta.com/client-server-game-architecture.html) /
[2](https://www.gabrielgambetta.com/client-side-prediction-server-reconciliation.html) /
[3](https://www.gabrielgambetta.com/entity-interpolation.html) /
[4](https://www.gabrielgambetta.com/lag-compensation.html) ·
[Fiedler: history](https://gafferongames.com/post/what_every_programmer_needs_to_know_about_game_networking/) ·
[snapshot interpolation](https://gafferongames.com/post/snapshot_interpolation/) ·
[snapshot compression](https://gafferongames.com/post/snapshot_compression/) ·
[networked physics 2004](https://gafferongames.com/post/networked_physics_2004/) ·
[state synchronization](https://gafferongames.com/post/state_synchronization/) —
Netcode talks: [Overwatch GDC 2017](https://www.gdcvault.com/play/1024001/-Overwatch-Gameplay-Architecture-and) +
[Edgegap summary](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback) ·
[Rocket League GDC 2018 deck](https://media.gdcvault.com/gdc2018/presentations/Cone_Jared_It_Is_Rocket.pdf) ·
[Valorant](https://technology.riotgames.com/news/peeking-valorants-netcode) ·
[CS2 sub-tick](https://www.counter-strike.net/cs2) ·
[Halo Reach](https://networkedgraphics.org/2011/05/13/halo-reach/) —
Classics: [TRIBES model](https://www.gamedevs.org/uploads/tribes-networking-model.pdf) ·
[Q3: Sanglard](https://fabiensanglard.net/quake3/network.php) +
[jfedor](https://www.jfedor.org/quake3/) —
Rollback: [GGPO](https://www.gamedeveloper.com/programming/the-lag-fighting-techniques-behind-ggpo-s-netcode) ·
[Infil](https://words.infil.net/w02-netcode.html) /
[Ars](https://arstechnica.com/gaming/2019/10/explaining-how-fighting-games-use-delay-based-and-rollback-netcode/) ·
[8 Frames in 16ms](https://www.gdcvault.com/play/1025471/8-Frames-in-16ms-Rollback) —
MMO/frameworks: [EVE dev blog](https://www.eveonline.com/news/view/paint-your-ship-red-and-make-it-faster) ·
[TiDi](https://www.eveonline.com/news/view/introducing-time-dilation-tidi) ·
[EVE grid](https://wiki.eveuniversity.org/Grid) ·
[TrinityCore Movement](https://trinitycore.atlassian.net/wiki/spaces/tc/pages/721256449/Movement) ·
[Minecraft server.properties](https://minecraft.wiki/w/Server.properties) ·
[SpatialOS QBI](https://web.archive.org/web/20191019170350id_/https://docs.improbable.io/reference/14.1/shared/authority-and-interest/interest/query-based-interest-qbi) /
[CBI](https://web.archive.org/web/20191115134225id_/https://docs.improbable.io/reference/14.2/shared/authority-and-interest/interest/chunk-based-interest-cbi) (⚠ archive only) ·
[Photon interest groups](https://doc.photonengine.com/realtime/current/gameplay/interestgroups) ·
[Mirror AoI](https://mirror-networking.gitbook.io/docs/manual/interest-management) ·
[Unity Netcode optimizations](https://docs.unity3d.com/Packages/com.unity.netcode@1.3/manual/optimizations.html) ·
[Colyseus state](https://docs.colyseus.io/state/) —
Racing: [iRacing](https://www.iracing.com/code-of-uncertainty/) ·
[Trackmania scandal](https://www.pcgamer.com/cheating-allegations-catch-up-with-some-of-trackmanias-fastest-drivers/) +
[donadigo](https://donadigo.com/tmx1) —
Sim/stards: [TraCI context subscriptions](https://sumo.dlr.de/docs/TraCI/Object_Context_Subscription.html) ·
[CARLA core_world](https://carla.readthedocs.io/en/latest/core_world/) ·
[HLA DDM: Fujimoto](http://simulation.su/uploads/files/default/2017-fujimoto-1.pdf) +
[Van Hook & Calvin](https://www.researchgate.net/publication/2267798) ·
[DIS 1278.1 draft](https://freewrl.sourceforge.io/tests/28_Distributed_interactive_simulation/1278.1-200X%20Draft%2016%20rev%2018.pdf) +
[RPR FOM GRIM](https://cdn.ymaws.com/www.sisostandards.org/resource/resmgr/standards_products/siso-std-001-2015_grim_rpr_f.pdf) +
[Prepar3D DIS](https://www.prepar3d.com/prepar3d/network/distributed_interactive_simulation/distributed_interactive_simulation_overview.html) ·
[Boulanger thesis](https://www.cs.mcgill.ca/~jboula2/thesis.pdf) —
Human factors: [Green summary](https://www.visualexpert.com/Resources/reactiontime.html) (paper: *Transportation
Human Factors* 2(3):195–216, 2000) ·
[TRB EC003](http://onlinepubs.trb.org/onlinepubs/circulars/EC003/ch33.pdf) —
NATS: [slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers) ·
[consumers](https://docs.nats.io/nats-concepts/jetstream/consumers) ·
[KV](https://docs.nats.io/nats-concepts/jetstream/key-value-store)
