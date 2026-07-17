# Prior Art Survey: State Authority

> Source: web research | Researched: 2026-07-17
> "Competitors" here = (a) authoritative game servers whose state-distribution
> and input-handling patterns define the craft, (b) MMOs/frameworks with
> documented interest-management systems, (c) racing/driving netcode — our
> closest genre, (d) simulator and distributed-simulation standards bodies whose
> formalisms predate the game literature, and (e) the rollback/lockstep family
> we explicitly do not join.
> Sibling context: [[arch-time-model]] surveyed these systems' *time* models;
> this file surveys their *authority and distribution* models.

## Game server canon (shooters — where the patterns were forged)

### Quake III Arena — ack-baselined delta snapshots + areamask scoping
- Server 20 Hz; per-client ring of 32 gamestates, deltas computed against the
  newest client-**acknowledged** snapshot, zeroed dummy baseline for full
  refreshes; `areamask` bitfield scopes entities per client; client cvars
  `rate` 3000 B/s, `snaps` 20; pre-fragmentation at 1400 B
  ([Sanglard](https://fabiensanglard.net/quake3/network.php),
  [jfedor](https://www.jfedor.org/quake3/)).
- **vs traffic-sim (us):** the reference implementation of delta compression —
  and the clearest exhibit of why we can't copy it onto core NATS: it requires
  per-client acks and per-client baselines, neither of which broker fan-out
  exposes. Its recovery shape (full gamestate, then resume deltas) is our
  late-joiner/resync recipe, with KV playing the gamestate role.

### Valve Source — decoupled rates, negotiated QoS, capped extrapolation
- 66 Hz tick; snapshot rate decoupled and client-negotiated (`cl_updaterate`
  20 default, `rate` bytes/s, server clamps `sv_min/maxrate`); delta-against-
  acked with `cl_fullupdate` recovery; `cl_interp` 100 ms absorbs one lost
  snapshot; extrapolation capped at 0.25 s; input commands batched 2+/packet at
  `cl_cmdrate` 30 ([Source Multiplayer Networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking);
  ⚠ Anubis bot-wall — quotes via
  [Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)).
- Lag compensation rewinds targets for hitscan only; projectiles excluded.
- **vs us:** the cadence-decoupling (tick ≠ snapshot ≠ input rates) is already
  ours (ADR-0005). The `rate`/`cl_updaterate` negotiation is the model for
  per-client QoS tiers (viz client on Wi-Fi vs local AI controller). Lag
  compensation is the machinery we get to skip — no aim verbs in traffic.

### Overwatch — input-buffer engineering + fairness clamps
- Fixed 16 ms command frames; client clock leads server by RTT/2 + 1 frame;
  per-client input buffer with starvation → duplicate-last-input; buffer-health
  feedback nudges client sim speed (16.0 → ~15.2 ms) to rebuild buffer;
  sliding-window bundling of all unacked inputs heals loss; predict everything
  by default (opt-out); reconciliation = rewind to server snapshot + replay
  buffered inputs; hit-impact prediction disabled above ~220 ms RTT as a
  fairness clamp ([GDC Vault](https://www.gdcvault.com/play/1024001/-Overwatch-Gameplay-Architecture-and),
  [Edgegap summary](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback) —
  mechanics via this secondary summary of the talk).
- Launch snapshot rate 20.8 Hz → 60 Hz "High Bandwidth" update with automatic
  scale-down for weak connections ([kitguru](https://www.kitguru.net/gaming/matthew-wilson/overwatch-high-bandwidth-update-brings-netcode-improvements/)).
- **vs us:** the mature input-side pattern: buffer + hold-last + bundle-unacked
  all transfer at 10 Hz (where one buffered tick is 100 ms — enormous headroom);
  the sim-speed nudge is optional at our rates. Their predict-everything
  philosophy validates ego-vehicle prediction as default, not luxury.

### CS2 sub-tick — timestamps decoupled from ticks
- "Servers know the exact instant that motion starts" — within-tick input
  timestamps over a still-64 Hz sim ([official](https://www.counter-strike.net/cs2),
  [talkesport](https://www.talkesport.com/news/cs2-servers-are-still-64-tick/)).
- **vs us:** even Valve keeps ticks for simulation and uses timestamps for
  inputs — ADR-0005's tick-boundary application + recorded `applied_tick` is
  the same split. If intra-tick ordering of two intents ever matters (two cars,
  one gap), timestamped intents resolved within a fixed tick is the pattern
  (flagged identically by [[arch-time-model]]).

### Valorant — modern tick-rate/budget calibration
- 128 Hz sim both sides; peeker's-advantage budget math: ~35 ms ping target to
  70% of players, ≤0.5 frame server buffering, 1 frame client
  ([Riot](https://technology.riotgames.com/news/peeking-valorants-netcode)).
- **vs us:** calibration only — shooter budgets are ~two orders of magnitude
  tighter than traffic needs (§ human factors in implementation.md).

### TRIBES (1998) — the complete classical design in one paper
- Scope-based ghosting with enter/leave events, priority-ordered packet fill,
  per-class state masks, four delivery classes including "Most Recent State,"
  loss recovery that guarantees *latest* state not every state, client
  bandwidth contracts (2 KB/s on 28.8 modems), 3× input redundancy
  ([paper](https://www.gamedevs.org/uploads/tribes-networking-model.pdf)).
- **vs us:** "Most Recent State" = core-NATS at-most-once semantics verbatim;
  scope/priority/state-mask remains the most complete per-client-subset design
  on record. Oldest paper in this file, still the most directly mappable.

### Halo: Reach — hosted-authority contrast
- One of 16 players is the authority ("hosted client"), reliability classes
  chosen per state by latency sensitivity ([GDC 2011](http://www.gdcvault.com/play/1014345/I-Shot-You-First-Networking),
  summary [networkedgraphics.org](https://networkedgraphics.org/2011/05/13/halo-reach/);
  ⚠ no per-entity byte counts public; video paywalled).
- **vs us:** what we avoid by having a real authority: host advantage, host
  migration, cheating-host trust problems. Listed for the contrast.

## MMOs and multiplayer frameworks

### World of Warcraft — the client-authority cautionary tale
- Clients run the entire movement physics for their own character: "This makes
  the client authoritative about the player's movement… The major downside is
  that it's very exploitable by cheaters" ([TrinityCore](https://trinitycore.atlassian.net/wiki/spaces/tc/pages/721256449/Movement),
  reverse-engineered); speedhack/teleport reports continue on Blizzard's own
  forums ([2024 example](https://us.forums.blizzard.com/en/wow/t/movement-hack-in-battlegrounds/1827111)).
  Other players: client-side dead reckoning with change-triggered updates +
  heartbeat + ~500 ms anti-jitter buffer; server units: spline interpolation.
- **vs us:** 20 years of documented exploitation for the exact boundary we
  decided against. *Also* proof that immediate local ego feedback is expected
  even by MMO players — we keep that UX via prediction while keeping authority
  via intents + clamps.

### EVE Online — 1 Hz intent-level control, TiDi, and NATS in production
- Physics tick 1 Hz; "if an input is sent right before a tick, it feels highly
  responsive. If sent right after, it can feel like it takes a second" — and
  players accept it because the controlled entity is sluggish
  ([CCP dev blog 2025](https://www.eveonline.com/news/view/paint-your-ship-red-and-make-it-faster)).
- Time Dilation: overload slows *simulation time* rather than dropping inputs,
  broadcast as a scalar ([TiDi blog](https://www.eveonline.com/news/view/introducing-time-dilation-tidi));
  grid-scoped visibility ~8000 km cubes ([grid](https://wiki.eveuniversity.org/Grid)).
- The same 2025 dev blog: the new Quasar cosmetics layer "is made possible by
  messaging from the mighty **NATS**, which efficiently handles the roughly
  10,000 messages being sent out to EVE clients every second."
- **vs us:** direct precedent that intent-level control feels fine at 1 Hz for
  sluggish entities — cars at 10 Hz are far inside that envelope. TiDi is
  ADR-0005's dilation scalar in production. And CCP independently chose NATS
  for exactly our fan-out role — the strongest industry validation of
  ADR-0002's live-plane choice found to date.

### Minecraft — two-radius scoping
- `view-distance` (delivery) vs `simulation-distance` (ticking) vs
  `entity-broadcast-range-percentage` (entity sub-range inside chunk range)
  ([server.properties](https://minecraft.wiki/w/Server.properties)).
- **vs us:** delivery radius ≠ simulation radius is a distinction we get free
  (the engine always simulates everything; only delivery is scoped) — but the
  entity-level sub-range inside the chunk range maps to our per-vehicle caps in
  [[concept-vehicle-controller-interface]]'s observation window.

### SpatialOS — interest as a first-class, dynamic query (historical)
- QBI: per-entity interest component mapping component IDs → composable
  constraints (relative sphere/box, entity/component id), dynamically
  updatable; CBI: chunk grid + radius extension; orthogonal static/dynamic
  component filters ([QBI](https://web.archive.org/web/20191019170350id_/https://docs.improbable.io/reference/14.1/shared/authority-and-interest/interest/query-based-interest-qbi),
  [CBI](https://web.archive.org/web/20191115134225id_/https://docs.improbable.io/reference/14.2/shared/authority-and-interest/interest/chunk-based-interest-cbi);
  ⚠ docs offline, Wayback only — company exited games hosting).
- **vs us:** the richest formal model of (entities × components) interest, and
  a cautionary tale: the runtime *re-evaluation* of arbitrary queries was
  SpatialOS's expensive core. Our compiled-to-subjects static cells keep the
  expressiveness that matters (ego-relative windows, viewport rectangles)
  without a query engine.

### Photon / Mirror / Unity Netcode for Entities / Colyseus — framework practice
- Photon: 256 interest groups as sub-channels, "most common use case… Network
  Culling… assign an interest group per 'area'"; only group 0 caches events
  ([interest groups](https://doc.photonengine.com/realtime/current/gameplay/interestgroups)).
- Mirror: spatial-hash/hex/distance/scene AoI built-ins; the threefold
  rationale scale/visibility/cheating ([docs](https://mirror-networking.gitbook.io/docs/manual/interest-management)).
- Unity Netcode for Entities: fixed-size packet per network tick filled by
  per-chunk importance (distance-scaled, age-boosted); snapshot rate decoupled
  from sim rate with round-robin ([optimizations](https://docs.unity3d.com/Packages/com.unity.netcode@1.3/manual/optimizations.html)).
- Colyseus: property-level dirty tracking, "only the latest mutation of each
  property… during the patchRate interval"; join = full state then deltas;
  per-client `StateView`s ([state](https://docs.colyseus.io/state/),
  [views](https://docs.colyseus.io/state/view)).
- **vs us:** Photon groups ≈ NATS subjects with a 256 cap we don't have;
  Unity's importance×age fill is our degradation mode when a cell overfills a
  tick budget; Colyseus's conflation is our publisher-side coalescing model —
  and its reliance on lossless WebSocket marks exactly the delta assumption
  core NATS breaks.

## Racing / driving netcode (our genre)

### Rocket League — the primary source for vehicle prediction under authority
- 120 Hz fixed tick; "Server buffers player inputs. Client predicts
  everything."; correction loop reverts to server frame and re-simulates;
  mispredictions "not as well with cars (unpredictable)… 200ms ping, 120hz =
  24 correction frames"; input buffer "eliminates some cheats (speed, jitter)";
  "100% server authoritative" ([GDC 2018 deck](https://media.gdcvault.com/gdc2018/presentations/Cone_Jared_It_Is_Rocket.pdf)).
- **vs us:** the strongest proof that full ego prediction + server authority
  coexist for *vehicles*, with corrections localized to close interaction. At
  10 Hz our correction cost is ~1.5 ticks per 150 ms RTT — an order of
  magnitude kinder than theirs, and our "collisions" are clamp overrides, not
  rigid-body impulses.

### iRacing — the far client-authoritative end
- Ego car fully client-simulated; other cars predicted locally; concrete
  latency geometry: at 125 mph, a 200 ms-vs-300 ms ping pair means "the
  computer only knows that he is going 125 mph about 75 feet behind you, half a
  second ago"; prediction errors surface as disputed contacts ("phantom 4x");
  mitigation advice is behavioral ([code of uncertainty](https://www.iracing.com/code-of-uncertainty/)).
- **vs us:** the documented failure mode our authority + clamps avoid by
  construction — and again, prediction error only bites at close interaction
  distances.

### Trackmania — zero interaction, replay-as-audit
- Multiplayer cars are non-colliding ghosts by design; deterministic physics →
  replays are input logs ([f1tenth paper](https://iros2023-madgames.f1tenth.org/papers/fay.pdf));
  the 2021 cheating scandal was exposed *because* replays store every input —
  input-spike analysis on them caught slow-motion tooling
  ([investigation](https://donadigo.com/tmx1),
  [PCGamer](https://www.pcgamer.com/cheating-allegations-catch-up-with-some-of-trackmanias-fastest-drivers/));
  Nadeo subsequently wiped records and banned accounts
  ([wiki summary](https://tmnf.miraheze.org/wiki/2021_Cheating_Scandal)).
- **vs us:** the extreme of "prediction stakes are low" (no interaction ⇒ no
  reconciliation). The scandal is the case for our arbitrated intent log
  (ADR-0005) as an *audit* instrument, not just a replay mechanism — post-hoc
  validation of any controller, human or AI.

### Mario Kart 8 — undocumented, P2P
- No reliable public netcode write-up exists; reverse engineering shows
  Nintendo's proprietary Pia peer-to-peer library ([KartLANPwn](https://github.com/chadhyatt/kartlanpwn)).
  ⚠ Listed only to record the absence; do not cite for mechanics.

## Simulators and distributed-simulation standards

### SUMO TraCI context subscriptions — ego-relative windows, pull model
- Subscribe to variables of objects around an EGO within range (m); domains per
  object type; filters: relative lanes, no-opposite, downstream/upstream
  distance, leader/follower, junction foes, vClass, field-of-vision, lateral
  distance; auto-teardown on ego exit ([docs](https://sumo.dlr.de/docs/TraCI/Object_Context_Subscription.html)).
- **vs us:** the requirement list for our ego-window API, written by a traffic
  simulator. Ours differs structurally: TraCI filters pull-side per step (with
  the 11× barrier cost, [[arch-time-model]]); we filter at subscription time
  via subjects, once per window change.

### CARLA — whole-world snapshots, no interest management
- `WorldSnapshot` = timestamp + per-actor transform/velocity/angular-velocity/
  acceleration, same-step consistent even in async mode
  ([core_world](https://carla.readthedocs.io/en/latest/core_world/)).
- **vs us:** no AoI at all — fine at CARLA's single-ego scale, impossible at
  our city scale. Its per-actor field set (incl. acceleration) is the schema
  anchor for our snapshot payload.

### IEEE 1516 HLA DDM — region algebra as a standard
- Routing spaces; rectangular update/subscription regions; overlap ⇒ delivery;
  region-based implementation = "a multicast group… for each publication
  region" (Fujimoto, [PDF](http://simulation.su/uploads/files/default/2017-fujimoto-1.pdf));
  velocity-padded regions for physically correct filtering under latency
  (Van Hook & Calvin, [abstract](https://www.researchgate.net/publication/2267798)).
- **vs us:** multicast-group-per-region *is* subject-per-cell; our drivers'
  ego windows and observers' viewport rectangles are both just subscription
  regions. Velocity padding = pad the window by max-speed × snapshot interval.

### IEEE 1278 DIS — dead reckoning as a publish contract
- Threshold + heartbeat + algorithm-change issuance; SISO defaults 1 m / 3°;
  nine named DR algorithms; consumer-side timeout eviction
  ([draft standard](https://freewrl.sourceforge.io/tests/28_Distributed_interactive_simulation/1278.1-200X%20Draft%2016%20rev%2018.pdf),
  [RPR FOM GRIM](https://cdn.ymaws.com/www.sisostandards.org/resource/resmgr/standards_products/siso-std-001-2015_grim_rpr_f.pdf),
  [Prepar3D](https://www.prepar3d.com/prepar3d/network/distributed_interactive_simulation/distributed_interactive_simulation_overview.html)).
- **vs us:** the standardized version of "conflate steady-state vehicles";
  lane-constrained 1-D motion makes our DR simpler than DIS's 6-DOF worst case.

## The rollback/lockstep family (deliberately not joined)

- **GGPO** (Cannon): speculative execution — "it rewinds the simulation back to
  the first incorrect frame, repredicts the inputs… and advances the
  simulation to the current frame" ([Game Developer](https://www.gamedeveloper.com/programming/the-lag-fighting-techniques-behind-ggpo-s-netcode)).
- **Rollback in fighting games** (Infil): prediction = "duplicate the last
  known input," right ~92–95% of frames; local player's inputs always shown
  immediately ([words.infil.net](https://words.infil.net/w02-netcode.html),
  [Ars cross-post](https://arstechnica.com/gaming/2019/10/explaining-how-fighting-games-use-delay-based-and-rollback-netcode/)).
- **Cost:** NetherRealm spent "two man-years" on per-frame state serialization
  for MKX rollback (Stallone, [8 Frames in 16ms](https://www.gdcvault.com/play/1025471/8-Frames-in-16ms-Rollback)).
- **Factorio** (via [[arch-time-model]]): lockstep with input-log replay —
  authority distributed across peers, needs bit-exactness everywhere.
- **vs us:** world rollback exists to hide peer latency where *no* authority
  exists; we have an authority and async controllers, so the world never
  rewinds (ADR-0005). But note the identity: client-side prediction +
  reconciliation of the ego vehicle **is** rollback scoped to one entity —
  store history, on correction rewind, replay unacked inputs. Rocket League
  runs exactly that loop in a client-server game. No world rollback; yes ego
  rollback.

## Positioning Summary

| System | Authority | Fan-out / AoI | Delta mechanism | Input handling | Prediction scope | Replay/audit |
|---|---|---|---|---|---|---|
| Quake III | dedicated server | areamask per client | ack-baselined delta + dummy full | cmd batching | ego (QuakeWorld) | — |
| Source | dedicated server | PVS per client | ack-baselined delta + `cl_fullupdate` | 2 cmds/packet, `cl_cmdrate` | ego + shared movement code | demo files |
| Overwatch | dedicated server | per-client | delta + heal | **input buffer + health feedback + sliding window** | predict everything | — |
| Rocket League | 100% server | per-client | — | **server input buffer, hold-last, throttles** | ego rigid body + ball | — |
| Valorant | dedicated 128 Hz | per-client | — | tight budgets | ego | — |
| TRIBES | server | **scope + priority + state mask** | latest-state guarantee | 3× redundancy | — | — |
| WoW | **client (movement)** | distance culling | change-triggered + heartbeat | client-simulated | ego = authority | — |
| EVE | server 1 Hz | grid cubes | — | intent-level, TiDi under load | minimal | — |
| Minecraft | server | chunk radius ×3 knobs | full chunk data | — | — | — |
| SpatialOS | server fleet | **QBI queries / CBI chunks** | component deltas | — | — | — |
| Photon/Mirror/Unity/Colyseus | server/host | groups / spatial hash / importance / views | property deltas (reliable transport) | framework-dependent | client libs | — |
| iRacing | host + client-sim | — | — | client-simulated ego | ego + others locally | — |
| Trackmania | — (ghosts) | — | — | — | ego only | **input-log replay = audit** |
| TraCI | engine (barrier) | **ego-relative context subscriptions** | pull per step | blocking barrier | — | re-run |
| CARLA | engine | none (whole world) | pull snapshot | sync/async clients | — | state log |
| HLA/DIS | federation | **DDM regions / DR thresholds** | on-threshold + heartbeat | federate-local | DR extrapolation | — |
| **traffic-sim (us)** | **engine, single writer** | **cell subjects; ego windows + viewport regions compiled to subscriptions** | **self-sufficient per-cell msgs on core NATS; keyframe+delta only on JetStream** | **per-controller buffer, hold-last, bundled unacked intents, `applied_tick` echo** | **ego predicted (clamp-aware), others interpolated 200–300 ms** | **JetStream intent log + keyframes + CRC (audit-grade)** |
