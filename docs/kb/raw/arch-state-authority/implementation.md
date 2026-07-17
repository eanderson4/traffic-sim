# Mechanics: State Authority

> Source: web research (greenfield — no engine code exists; this file collects the
> *mechanisms* an authoritative state-distribution and input-arbitration layer is
> built from, to be re-audited against the real engine once the message-contract
> ADR lands) | Researched: 2026-07-17 | Git HEAD: 6efd963
>
> Fixed project context (ADR-0002, ADR-0005): single-writer world state on a
> 100 ms tick; controllers are async NATS clients whose intents are buffered and
> batch-applied at tick boundaries; late intents apply later, no rewind/lag
> compensation; replay = keyframes + arbitrated intent log + CRC on JetStream;
> ~10 snapshots/s planned with a ~200–300 ms client interpolation buffer.

## 1. The authority model: intents in, state out

The defining mechanic — the client never asserts state, it requests outcomes:

- Gambetta's authoritative-server canon: "don't trust the player. Always assume
  the worst — that players *will* try to cheat." His protocol sketch has the
  client say "**I want to move one square to the right**" and the server reply
  "**You're at (11, 10)**" — requests in, authoritative state out
  ([client-server-game-architecture](https://www.gabrielgambetta.com/client-server-game-architecture.html)).
- Bernier (Valve, 2001) on why clients can't report position: "This is fine if
  you can trust the client… For Half-Life, this mechanism is unworkable because
  of realistic concerns about cheating. If we encapsulated absolute state data
  in this fashion, we'd raise the motivation to hack the client even higher"
  ([Latency Compensating Methods](https://developer.valvesoftware.com/wiki/Latency_Compensating_Methods_in_Client/Server_In-game_Protocol_Design_and_Optimization);
  ⚠ page is behind an Anubis bot-shield — quotes verified against a
  [full-text mirror](https://github.com/joexi/Latency-Compensating)).
- Fiedler's history of the model: Carmack on QuakeWorld — "I am now allowing the
  client to guess at the results of the users movement until the authoritative
  response from the server comes through. This is a biiiig architectural change";
  Tim Sweeney's Unreal write-up: "The Server Is The Man"
  ([what every programmer needs to know](https://gafferongames.com/post/what_every_programmer_needs_to_know_about_game_networking/)).
- Crucially, authority and prediction are *complements*: "in FPS games it is
  absolutely necessary that the server is authoritative over the state of each
  player character, in-spite of the fact that each player is locally predicting
  the motion of their own character" ([same](https://gafferongames.com/post/what_every_programmer_needs_to_know_about_game_networking/)).

**Analysis:** This is exactly the boundary [[concept-vehicle-controller-interface]]
already decided: controllers emit bounded *intents* (target accel, lane request),
the engine clamps and is the sole writer of vehicle state. The game canon adds two
things: (a) the trust argument applies even to authenticated clients (Source:
packets "could be still modified on a 3rd machine… a 'man-in-the-middle' attack"
— [Source Multiplayer Networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking),
⚠ Anubis-gated, via
[Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking));
(b) the failure case has a 20-year public record — WoW's client-authoritative
movement ("This makes the client authoritative about the player's movement… The
major downside is that it's very exploitable by cheaters,"
[TrinityCore Movement docs](https://trinitycore.atlassian.net/wiki/spaces/tc/pages/721256449/Movement))
with speedhack/teleport-hack reports still open on Blizzard's forums in 2024
([example](https://us.forums.blizzard.com/en/wow/t/movement-hack-in-battlegrounds/1827111)).

## 2. Snapshot mechanics: full vs delta, and what the transport allows

### 2.1 Ack-baselined delta compression (the classical scheme)

- **Quake III:** "For each Client the server keeps the 32 last gamestate sent
  over the network in a cycling array" plus a zeroed "dummy" gamestate used as
  the diff baseline "when there is no 'previous state' available"; field-level
  diffing blindly follows a `netField_t` (name/offset/bits) table; packets
  pre-fragmented at 1400 B ([Sanglard code review](https://fabiensanglard.net/quake3/network.php)).
  Wire detail: "The current snapshot is compressed against an older snapshot,
  not necessarily the previous one… the newest one that the server knows for
  sure that the client has received" — the `delta_num` field names the baseline;
  one "changed" bit per field, integral floats as 13-bit ints
  ([jfedor wire protocol](https://www.jfedor.org/quake3/)).
- **Source:** "the server doesn't send a full world snapshot each time, but
  rather only changes (a delta snapshot) that happened since the last
  acknowledged update… full (non-delta) snapshots are only sent when a game
  starts or a client suffers from heavy packet loss for a couple of seconds.
  Clients can request a full snapshot manually with the `cl_fullupdate` command"
  ([Source Multiplayer Networking](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), ⚠ via
  [Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)).
- **Fiedler's compression worked example:** cube state 321 → 80 bits (10 B);
  quaternion "smallest three" 128 → 29 bits; positions quantized to 50 bits at
  ~2 mm; changed-bits mask (901 bits) vs 10-bit indices crossover at ~90 changed
  cubes; delta positions ≈ 26.1 bits, delta quaternions ≈ 23.3 bits; "Sometimes
  the best bandwidth optimizations… are about what you *don't* send" (linear
  velocity dropped at 60 Hz) ([snapshot compression](https://gafferongames.com/post/snapshot_compression/)).

### 2.2 Why ack-baselined delta can't ride broker fan-out

Core NATS deliberately gives the publisher none of Q3's three requirements
(per-subscriber ack stream, per-subscriber baseline bookkeeping, knowledge of
which message each subscriber last got): "NATS favors the approach of protecting
the system as a whole over accommodating a particular consumer… When detected at
the client, the application is notified and messages are dropped… When detected
in the server, the server will disconnect the connection with the slow consumer
to protect itself"; client pending defaults 65,536 msgs / 64 MiB, then drop —
"This is aligned with NATS at most once delivery. It is up to your application
to detect the missing messages and recover"
([slow consumers](https://docs.nats.io/running-a-nats-service/nats_admin/slow_consumers)).

**Analysis:** Every game delta scheme assumes per-client ACKs (UDP protocols) or
lossless ordered transport (Colyseus over WebSocket, §2.3). Over at-most-once
broker fan-out a dropped delta is indistinguishable from silence, so the
industry's broker-compatible fallbacks are: (a) self-sufficient latest-state
messages (Tribes "Most Recent State", §2.4; Fiedler: "If a snapshot is lost we
can just skip past it and interpolate towards a more recent snapshot… We don't
ever want to stop and wait for a lost snapshot packet to be resent"
([snapshot interpolation](https://gafferongames.com/post/snapshot_interpolation/)));
(b) periodic keyframes + deltas that heal loss at the next keyframe (Q3's dummy
snapshot, Source's `cl_fullupdate`); (c) treat the slow-consumer disconnect as
the recovery trigger and resync on reconnect (§5). Deltas *against an acked
baseline* do fit JetStream (which has consumer acks) — but that's the durable
record path, not the 10 Hz live path ([[arch-nats-backbone]]).

### 2.3 Property-level deltas over reliable transport (the framework approach)

Colyseus tracks per-property dirtiness: "Only the latest mutation of each
property is queued and sent to clients during the patchRate interval"; join
handshake sends "all the types… followed by the full state," then only changes;
each state instance has a `refId` for add/remove/update reference
([Colyseus state sync](https://docs.colyseus.io/state/)). Per-client filtering
exists as manual `StateView`s with `@view()`-tagged fields
([Colyseus views](https://docs.colyseus.io/state/view)).

**Analysis:** "Only the latest mutation of each property per flush interval" is
latest-state *conflation* at a fixed cadence — the right mental model for a
10 Hz publisher coalescing per-vehicle updates between flushes. But Colyseus's
deltas work because WebSocket is lossless and ordered; don't confuse
"property-level delta" (an encoding) with "delta against acked baseline" (a
loss-recovery protocol). The encoding survives on NATS; the protocol doesn't.

### 2.4 TRIBES: scope, priority, state masks, most-recent-state (the complete classical design)

Frohnmayer & Gift's TRIBES model ships four delivery classes including "Most
Recent State" ("volatile data of which only the latest version is of
[interest]"); a ghost manager with scope: "Objects may come in and out of
scope… When an object comes into scope, its ghost is transferred to the remote
host; when an object goes out of scope its ghost is deleted. While an object is
in scope, state data is transferred… at a rate based on its priority and state
mask"; update list "ordered first by status change, then by object priority";
per-class state-mask bits ("typically upwards of 20 state flags"); loss handling
re-sends lost state bits only if no later packet carried that state — guarantee
the *latest* state, not every state; client bandwidth contract (28.8 modem:
"10 packets per second with a size of 200 [bytes]"); input moves sent 3×
redundantly ([TRIBES networking model](https://www.gamedevs.org/uploads/tribes-networking-model.pdf)).

**Analysis:** "Most Recent State" is verbatim core-NATS semantics, and the
scope/priority/state-mask trio is a complete per-client subset design: scope =
AoI membership, priority = fill order under a byte budget, state mask = which
attribute groups are dirty. If a snapshot message ever exceeds the per-tick
budget, this is the fill discipline (cf. Unity's importance queue, §3.6).

## 3. Interest management mechanics (who gets what)

### 3.1 The cost problem that forces it

Mirror's rationale is the canonical threefold: **Scale** ("Sending the whole
world to every single player would be insane"), **Visibility** (fog-of-war),
**Cheating** ("if the whole world state is known in memory, then hackers could
exploit that by showing players behind a wall anyway")
([Mirror interest management](https://mirror-networking.gitbook.io/docs/manual/interest-management)).
The academic warning on naive models: "The drawback of a pure aura-nimbus model
is that it does not scale well because of the cost of computing the intersection
between the area-of-interest and the [nimbus]" (Boulanger, McGill MSc thesis
2006, [PDF](https://www.cs.mcgill.ca/~jboula2/thesis.pdf)). At
C clients × V vehicles, per-tick radius queries are O(C×V) — cell partitioning
exists to make membership incremental.

### 3.2 Cells/grids: the universal answer

- **Minecraft:** two radii worth stealing — `view-distance` (default 10 chunks)
  is "The amount of world data the server sends the client," while
  `simulation-distance` bounds what is *ticked*; a third knob
  `entity-broadcast-range-percentage` (default 100) gates entity visibility
  *inside* the chunk radius ([server.properties](https://minecraft.wiki/w/Server.properties),
  [chunk tickets](https://minecraft.wiki/w/Chunk)).
- **Mirror:** built-in AoI systems — Spatial Hashing ("one global Vis Range"),
  Hex variant, per-identity Distance, Scene, Match, Team, Custom
  ([interest management](https://mirror-networking.gitbook.io/docs/manual/interest-management),
  [spatial hashing](https://mirror-networking.gitbook.io/docs/manual/interest-management/spatial-hashing)).
- **EVE Online:** the grid is "the finite viewable volume surrounding any
  object in space… typically a cube extending to approximately 8000 km in each
  cardinal direction"; objects outside "will not be visible"; grids have walls
  "that ships can pass through into the next grid"
  ([EVE Uni grid](https://wiki.eveuniversity.org/Grid)).
- **Source/Q3:** per-client PVS — Q3's snapshot carries an `areamask` bitfield:
  "The server doesn't necessarily send all the entities, only the ones that the
  player could see or interact with, based on where on the map they are
  currently located" ([jfedor](https://www.jfedor.org/quake3/)).

**Analysis:** For a lane graph the "cell" has two candidate geometries:
Euclidean grid squares (natural for observers with a map viewport — viewport
rectangle → cell set) and graph neighborhoods (k-hop lane/edge sets around the
ego vehicle — natural for drivers; TraCI's ego-relative windows, §4.1, already
work this way). Both compile down to a set of NATS subjects (§3.4).

### 3.3 Declared interest: SpatialOS's formal model (historical canon)

SpatialOS docs are offline (Improbable exited games hosting); recovered from
Wayback — treat as design canon, not a live reference:

- **Query-based interest (QBI):** "a way of specifying the entity components
  that worker types or instances want to receive updates about… Interest is a
  prerequisite for active read access." Each entity carries an `improbable.
  Interest` component mapping component IDs → queries; constraints include
  sphere/cylinder/box **relative to the entity itself**, composable AND/OR;
  result type = which components get delivered for matching entities; "You can
  also update a query dynamically (during runtime)"
  ([QBI](https://web.archive.org/web/20191019170350id_/https://docs.improbable.io/reference/14.1/shared/authority-and-interest/interest/query-based-interest-qbi)).
- **Chunk-based interest (CBI):** "Chunks are a grid over a SpatialOS world… A
  worker instance with write access authority over a component automatically has
  interest in any components in the same chunk," extendable by radius; static
  and dynamic component filters orthogonal to CBI
  ([CBI](https://web.archive.org/web/20191115134225id_/https://docs.improbable.io/reference/14.2/shared/authority-and-interest/interest/chunk-based-interest-cbi)).

**Analysis:** QBI is the cleanest formal statement of our design space:
interest = (which entities) × (which components of those entities), keyed to
something the consumer owns, dynamically updatable, with relative-radius
constraints the common case. This is exactly
[[concept-vehicle-controller-interface]]'s "declared AoI window at attach" —
SpatialOS proves the declaration can be expressive without widening the wire
format.

### 3.4 Interest groups ≈ NATS subjects

Photon's Interest Groups: "sub-channels for conversations in a room: Clients
only get the messages of Interest Groups they subscribed to (and group 0)… up
to 256 interest groups… Most common use case of Interest Groups is Network
Culling… assign an interest group per 'area'"; `OpChangeGroups` mutates
server-side filtering. Caveat: "You can only cache events sent to interest
group 0" ([Photon interest groups](https://doc.photonengine.com/realtime/current/gameplay/interestgroups)).

**Analysis:** Photon groups are literally subject subscriptions (join/leave ≈
sub/unsub, group 0 ≈ broadcast subject). NATS removes the 256-group cap
("10s of millions of subjects" per [subjects](https://docs.nats.io/nats-concepts/subjects)),
so per-cell subjects are free — but Photon's cache caveat maps to a real NATS
limitation too: there is no free "cached latest state" per arbitrary interest
set; catch-up state comes from KV/last-per-subject for whatever was actually
published (§5).

### 3.5 HLA DDM: the simulation-standard version (region algebra)

- "DDM is based on an abstraction called the routing space, that is simply an
  N-dimensional coordinate system. Each message that is sent is associated with
  a rectangular update region. Each federate specifies a rectangular
  subscription region… If the update region associated with a message overlaps
  a federate's subscription region, the federate should receive a copy of the
  message." Implementation: "Perhaps the most direct is the region-based
  approach. A multicast group is defined for each publication region. Each
  federate simply joins those groups that correspond to publication regions that
  overlap with its subscription regions" (Fujimoto,
  [PDF](http://simulation.su/uploads/files/default/2017-fujimoto-1.pdf)).
- Van Hook & Calvin derive "physically correct filters to account for network
  latencies and object movement… mathematically derived extensions of update
  and subscription regions" — i.e., pad regions by max-velocity × latency so
  fast movers don't appear/disappear at the boundary ([abstract](https://www.researchgate.net/publication/2267798)).

**Analysis:** The region-based DDM implementation *is* our design: multicast
group = NATS subject, publication region = cell, subscription region = the
client's declared window (viewport rectangle for observers, ego-relative for
drivers). The velocity-padding trick is the standard answer to "a fast car
teleports into view at the AoI edge" — pad the window by (max vehicle speed ×
snapshot interval), trivial for us since speed is bounded by the road's speed
limit and the window is padded in *graph distance*.

### 3.6 Byte-budget fill: importance queues

Unity Netcode for Entities: "The server operates on a fixed bandwidth and sends
a single packet with snapshot data of customizable size on every network tick.
It fills the packet with the entities of the highest importance… Once a packet
is full, the server sends it and the remaining entities are missing from the
snapshot. Because the age of the entity influences the importance, it is more
likely that the server will include those entities in the next snapshot."
Importance is computed per chunk with distance scaling; snapshot rate
(`NetworkTickRate`) is decoupled from `SimulationTickRate` with round-robin
distribution ([optimizations](https://docs.unity3d.com/Packages/com.unity.netcode@1.3/manual/optimizations.html),
[ghost snapshots](https://docs.unity3d.com/Packages/com.unity.netcode@1.7/manual/ghost-snapshots.html)).

**Analysis:** This is the shipped-game version of TRIBES' priority-ordered fill
and the correct degradation mode when a cell has more vehicles than the per-tick
budget carries: most-important-most-stale first, staleness boosts priority next
tick. We should never need it at demo scale, but the discipline prevents silent
tail drops at city scale.

## 4. Simulator prior art for AoI state delivery

### 4.1 SUMO TraCI context subscriptions — the direct ancestor

"Context subscriptions allow to obtain specific values from surrounding objects
of a certain so-called 'EGO' object… within a certain range." Mechanics:
subscribe with (begin, end, EGO id, **context domain**, **context range in m**
— "the third dimension is neglected!"), variable list; response returns
variables × objects; executed after each `simulationStep`; auto-descheduled
when the EGO leaves the simulation. Filters (vehicle ego): `lanes` (relative
lane list), `no-opposite`, `downstream distance`, `upstream distance`,
`leader/follower` ("only return leader and follower on the specified lanes"),
`turn` ("only return foes on upcoming junctions"), `vClass`, `vType`,
`field of vision` (degrees), `lateral distance`
([Object Context Subscription](https://sumo.dlr.de/docs/TraCI/Object_Context_Subscription.html)).
Python example: `traci.junction.subscribeContext(junctionID,
CMD_GET_VEHICLE_VARIABLE, 42, [VAR_SPEED, VAR_WAITING_TIME])` — "all vehicle
speeds and waiting times within range (42m) of a junction"
([Python interface](https://sumo.dlr.de/docs/TraCI/Interfacing_TraCI_from_Python.html)).

**Analysis:** This is the single most directly applicable prior art — a traffic
simulator already shipping exactly the "state around an ego" primitive our
driver/AV clients need, with a filter list that reads like our requirements doc
(leader/follower, downstream distance, junction foes). Two inversions: TraCI is
pull-per-step over a socket (with the measured 11× barrier cost,
[[arch-time-model]]); ours is brokered push. And TraCI's window is pull-side
filtering of global state; ours must be decided *before* publish (the engine
fans out per-cell, the client subscribes per-cell — filtering happens at
subscription time, which is why cell granularity matters).

### 4.2 CARLA — whole-world pull, useful schema anchor

`world.get_snapshot()` returns a `WorldSnapshot` = `Timestamp` + list of
`ActorSnapshot` (per actor: `get_transform()`, `get_velocity()`,
`get_angular_velocity()`, `get_acceleration()`) — "The information comes from
the same simulation step, even in asynchronous mode"; the spectator is itself an
actor ([core_world](https://carla.readthedocs.io/en/latest/core_world/)).

**Analysis:** CARLA has no interest management at all (whole world per pull) —
its value here is the minimal per-vehicle state schema, notably *including
acceleration*, which is what good client-side interpolation/extrapolation needs
(cf. Fiedler's Hermite-with-velocity point, §7.2).

## 5. Late joiner / resync mechanics

- **Game pattern — full then deltas:** Q3 sends `svc_gamestate` (configstrings +
  entity baselines) on connect, then delta snapshots against it
  ([jfedor](https://www.jfedor.org/quake3/)); Source sends full snapshots "when
  a game starts" and on `cl_fullupdate` (§2.1); Colyseus: "Handshake: When a
  client joins a room, the server sends all the types… followed by the full
  state" ([state sync](https://docs.colyseus.io/state/)).
- **NATS mechanics for the same recipe:** KV `watch` — "the watcher receives
  updates due to put or delete operations on the key pushed to it in real-time";
  buckets "immediately consistent" for monotonic reads, with the documented
  no-read-your-writes-via-direct-get caveat (read from the stream leader for the
  strong variant) ([KV store](https://docs.nats.io/nats-concepts/jetstream/key-value-store)).
  JetStream `DeliverLastPerSubject` = materialized-view catch-up;
  `DeliverByStartSequence`/`DeliverByStartTime` = exact resume
  ([consumers](https://docs.nats.io/nats-concepts/jetstream/consumers)).

**Analysis:** The catch-up recipe: materialize current AoI state from KV
(`latest` keys) or a `DeliverLastPerSubject` pull consumer filtered to the
client's cells, then attach live core-NATS subjects. The snapshot-read /
live-subscribe race is resolved with the tick number every message already
carries (ADR-0005): discard live messages ≤ snapshot tick. This is the same
ordering problem Q3 solves with its gamestate/baseline split.

## 6. Client prediction & reconciliation mechanics (the ego vehicle)

### 6.1 The canonical mechanism

- **Bernier:** the client stores every user command with its generation time;
  "For prediction, the last acknowledged movement from the server is used as a
  starting point"; with 100 ms RTT at 50 fps "the client will have stored up
  five user commands ahead of the last one acknowledged"; minimization of
  divergence via *shared movement code* ("the identical movement code for
  players in both the server-side game code and the client-side game code…
  pm_shared/"); re-sends form "a sliding window in Half-Life's case" of unacked
  commands. His footnote on partial prediction is the argument *for* full
  prediction: it "would still leave the player's movements lagged (often
  described as if you are moving around on ice skates)" ([paper](https://developer.valvesoftware.com/wiki/Latency_Compensating_Methods_in_Client/Server_In-game_Protocol_Design_and_Optimization), ⚠ via
  [full-text mirror](https://github.com/joexi/Latency-Compensating)).
- **Gambetta's worked example:** sequence-numbered inputs; server echoes last
  processed sequence; "the client can calculate the 'present' state of the game
  based on the last authoritative state sent by the server, plus the inputs the
  server hasn't processed yet" ([part 2](https://www.gabrielgambetta.com/client-side-prediction-server-reconciliation.html)).
  Part 3 is almost a design doc for our client: "the game world is updated
  periodically at low frequency, for example 10 times per second… all the
  unprocessed client input is applied… and the new game state is broadcast";
  his racing dead-reckoning paragraph: "assume the car's heading and
  acceleration will remain constant during that 100 ms, and run the car physics
  locally… when the server update arrives, the car's position is corrected…
  if the player crashes against something, the predicted position will be
  extremely wrong" ([part 3](https://www.gabrielgambetta.com/entity-interpolation.html)).
- **Fiedler's reconciliation loop:** "keep a circular buffer of past character
  state and input for the local player on the client, then when the client
  receives a correction from the server, it first discards any buffered state
  older than the corrected state from the server, and replays the state starting
  from the corrected state back to the present 'predicted' time" — an invisible
  rewind/replay of the local player "while holding the rest of the world fixed."
  When corrections fire (Sweeney): "Nearly all the time, the client movement
  simulation exactly mirrors the client movement carried out by the server…
  Only in the rare case, such as a player getting hit by a rocket, or bumping
  into an enemy, will the client's location need to be corrected"
  ([what every programmer needs to know](https://gafferongames.com/post/what_every_programmer_needs_to_know_about_game_networking/)).
  Conditions for clean prediction (Networked Physics): "only if there is a
  clear ownership of objects by clients and these object interact mostly with a
  static world"; not bit-exactness but "a reasonable 1/2 second prediction
  giving approximately the same result"
  ([networked physics 2004](https://gafferongames.com/post/networked_physics_2004/)).

**Analysis:** Our ego vehicle satisfies Fiedler's conditions unusually well:
exclusive ownership per vehicle ([[concept-vehicle-controller-interface]]),
mostly-static world, and lane-constrained motion — the strongest predictability
constraint games don't have. Mispredictions arise exactly where Sweeney says:
close interaction, i.e., the car-following clamp overriding the driver's intent
when a gap closes. The client's predictor therefore needs the clamp rule (gap to
leader → max accel), not the full IDM stack — a "shared movement code" question
(pm_shared precedent) of which subset of engine physics ships to the TS client.

### 6.2 Rocket League: the vehicle-specific proof (primary source)

Jared Cone's GDC 2018 deck: "Fixed tick rate (120hz, 8.33ms)"; constraints:
"Input delay is not an option / Client prediction for rigid-body vehicles /
Server can't wait for client input / Collision with moving objects / 100%
server authoritative." Why not wait for input: "Player inputs to server suffer
jitter, loss. To compensate, server waits for input before running physics. Not
good for rigid-body simulation." Final design: "**Server buffers player inputs.
Client predicts everything.**" Client records input + frame #, runs physics,
records history; server returns frame # + state; "Large difference requires
correction" → "Revert all physics actors to that frame in history" → "Run
multiple physics frames to catch up." Misprediction sources: "Works well with
ball (predictable). **Not as well with cars (unpredictable). No server-side lag
compensation. Expensive corrections. 200ms ping, 120hz = 24 correction
frames.**" Result: "No input delay. High-ping clients don't ruin game… 100%
server authority" ([slide deck PDF](https://media.gdcvault.com/gdc2018/presentations/Cone_Jared_It_Is_Rocket.pdf)).

**Analysis:** The canonical "yes, fully predict the ego car even under a
100%-authoritative server" answer — and the correction-cost math favors us
asymmetrically: at our 10 Hz tick, a 150 ms RTT is ~1.5 ticks of replay versus
their 24 frames at 200 ms/120 Hz. Prediction of the ego is *cheap* at traffic
rates.

### 6.3 Hiding corrections: smoothing, not state pollution

- Source: "the client has to correct its own position, since the server has
  final authority… By gradually correcting this error over a short amount of
  time (`cl_smoothtime`), errors can be smoothly corrected" ([Source wiki](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), ⚠ via
  [Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)).
- Fiedler: smooth position/orientation, snap derivatives — ">2m snap; >0.1m move
  10% of the difference per update"; "I recommend that you perform this
  smoothing for immediate quantities such as position and orientation, while
  directly snapping derivative quantities such as velocity"
  ([networked physics 2004](https://gafferongames.com/post/networked_physics_2004/));
  the error-offset technique renders at simulation position + a decaying visual
  offset so sim state is never polluted ([state synchronization](https://gafferongames.com/post/state_synchronization/)).

**Analysis:** For us the visual-offset pattern keeps the TS client's rendered
ego car honest (rendered ≠ simulated) — important because our replay/CRC
discipline punishes any client-side state mutation leaking back. Corrections in
traffic are small (a clamp caps accel, not teleports), so 100–300 ms smoothing
windows suffice.

## 7. Late/dropped input mechanics (the input side of the tick)

### 7.1 Server-side input buffers (Overwatch / Rocket League pattern)

Overwatch (GDC 2017; mechanics as summarized from the talk — the primary source
is the video, [GDC Vault](https://www.gdcvault.com/play/1024001/-Overwatch-Gameplay-Architecture-and) /
[YouTube](https://www.youtube.com/watch?v=W3aieHjyNvw); detailed secondary
summary [Edgegap](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback)):

- Fixed 16 ms command frames (7 ms tournament); "The client's clock is always
  ahead of the server by half round-trip time plus one buffered command frame."
- Starvation: "When packets are dropped and the server runs out of input to
  simulate, **it duplicates the player's last known input** and hopes for the
  best."
- Buffer-health feedback: server detects starvation, "notifies the client, which
  begins dilating time. Instead of a 16ms fixed timestep, the client treats it
  as approximately 15.2ms, simulating slightly faster and pouring more inputs
  into the network pipe to build up a buffer on the server's side… This
  feedback loop runs constantly."
- Sliding-window bundling (QuakeWorld lineage): "the client bundles **every
  input since the last server-acknowledged movement state** into a single
  packet… If a packet is lost, the next one still carries all the missing
  inputs."

Rocket League independently converged: "Server buffers client input. No need to
pause for input. **Eliminates some cheats (speed, jitter).** Increases average
latency"; "Try to avoid empty buffer (**runs physics using previous player
input**)"; feedback throttles both directions ("Buffer low? Client runs extra
physics frames… Server consumes 0, 1, or 2 inputs per frame")
([deck](https://media.gdcvault.com/gdc2018/presentations/Cone_Jared_It_Is_Rocket.pdf)).

Source's lighter-weight version: client samples input at tick rate but "sends
command packets at a certain rate of packets per second (usually 30). **This
means two or more user commands are transmitted within the same packet**"
([Source wiki](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), ⚠ via
[Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)).

**Analysis:** Scaled to our 100 ms tick: (1) a per-controller intent buffer on
the engine (already implied by ADR-0005's "buffered on arrival, batch-applied");
(2) starvation → re-apply last intent — OW/RL precedent, and for a car "hold
current accel request" ≈ cruise, far safer than holding a strafe key; (3)
bundle unacked intents in every message so one lost NATS publish heals at the
next — loss healing without TCP retransmit stalls, and cheap because intents
are tiny and mostly repeated (drivers hold the wheel, like OW players holding
keys); (4) the OW buffer-health nudge is optional at 10 Hz — one tick of buffer
is 100 ms, enormous relative to internet jitter at human input rates.

### 7.2 Interpolation mechanics for remote vehicles (the other 99.9% of traffic)

- Source defaults: `cl_interp 0.1` — "even if one snapshot is lost, there are
  always two valid snapshots to interpolate between"; extrapolation capped
  (`cl_extrapolate_amount` 0.25 s) ([Source wiki](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), ⚠ via
  [Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)).
- Fiedler's rule: "the interpolation buffer should have enough delay so that I
  can lose two packets in a row and still have something to interpolate
  towards… at 2-5% packet loss [that] is 3X the packet send rate. At 10
  packets per-second this is 300ms… recorded with a delay of 350ms"; Hermite
  splines using per-sample velocity remove artifacts at the *same* send rate —
  send velocity in the snapshot; extrapolation "starts to break down" as soon as
  objects interact ([snapshot interpolation](https://gafferongames.com/post/snapshot_interpolation/)).
- Bernier's identical math at our exact rates: "if the server is sending 10
  updates per second… we might impose 100 milliseconds of interpolation
  delay… we could set the interpolation time as 200 milliseconds instead of
  100… allow us to entirely miss one update and still have the player
  interpolating toward a valid position" ([paper](https://developer.valvesoftware.com/wiki/Latency_Compensating_Methods_in_Client/Server_In-game_Protocol_Design_and_Optimization), ⚠ via
  [full-text mirror](https://github.com/joexi/Latency-Compensating)).

**Analysis:** ADR-0005's "~200–300 ms client buffer" is squarely the 3×-send-
interval rule at 10 Hz. Remote vehicles are *interpolated in the past*, never
predicted — and lane geometry makes interpolation near-trivial: a vehicle's
position is (laneId, s), so interpolation runs along the lane polyline
([[arch-road-graph-model]]) rather than free 3-space; heading is derived, not
shipped. Extrapolation beyond the newest snapshot should be capped tightly
(Source's 0.25 s is the calibration) — brake lights are exactly the
unpredictable event extrapolation mangles.

### 7.3 Dead reckoning as a publish-rate reducer (DIS)

- Issuance rule: an entity-state update is published when (a) "The discrepancy
  between an entity's actual state… and its dead reckoned state… exceeds a
  predetermined threshold", (b) "A predetermined length of real-world time has
  elapsed since the issuing of the last… PDU" (heartbeat), or (c) the DR
  algorithm changes ([IEEE 1278.1 draft](https://freewrl.sourceforge.io/tests/28_Distributed_interactive_simulation/1278.1-200X%20Draft%2016%20rev%2018.pdf)).
- Defaults: `DRA_POS_THRSH_DFLT = 1 meter`, `DRA_ORIENT_THRSH_DFLT = 3 degrees`
  ([SISO-STD-001-2015 RPR FOM GRIM](https://cdn.ymaws.com/www.sisostandards.org/resource/resmgr/standards_products/siso-std-001-2015_grim_rpr_f.pdf)).
- Named algorithms (IEEE 1278.1 Annex B): Static, FPW, RPW, RVW, FVW, FPB, RPB,
  RVB, FVB — Fixed/Rotating rate × Position/Velocity × World/Body coords
  ([Open-DIS javadoc](https://open-dis.sourceforge.net/javadoc/open-dis/docs/edu/nps/moves/deadreckoning/DIS_DeadReckoning.html), ⚠ 403 to fetchers; class list
  verified via search index).
- Practice: ACM flight sim heartbeats every 4.8 s plus threshold-triggered
  updates ([ACM manual](https://www.icosaedro.it/acm/manual/acmdoc-inside.htm));
  Prepar3D's consumer-side **Timeout** — "remote simulation objects must
  heartbeat. If this duration is exceeded, the object will be removed from the
  simulation" ([Prepar3D DIS](https://www.prepar3d.com/prepar3d/network/distributed_interactive_simulation/distributed_interactive_simulation_overview.html)).

**Analysis:** DR is "don't publish what receivers can extrapolate." Lane-
constrained vehicles at steady state (constant speed along a known polyline)
are the best-case DR scenario — error accumulates in one dimension against a
known path. v1 can publish every vehicle every snapshot (10 Hz is cheap at
demo scale, §9), but DR threshold + heartbeat is the measured-bandwidth escape
valve at city scale, and the heartbeat/timeout pair doubles as the AoI-eviction
signal (remove a vehicle from the client's world if no update within N
intervals).

### 7.4 Sub-tick input timestamps (CS2) and fairness clamps

- CS2: "Sub-tick updates are the heart of Counter-Strike 2… **servers know the
  exact instant that motion starts, a shot is fired, or a 'nade is thrown.**"
  ([counter-strike.net/cs2](https://www.counter-strike.net/cs2)); community
  reality check: servers still tick 64 Hz, inputs carry within-tick timestamps
  ([talkesport](https://www.talkesport.com/news/cs2-servers-are-still-64-tick/)).
- Overwatch's fairness clamp: above ~220 ms RTT "hit impact prediction is
  disabled entirely… rather than rewinding a target so far back that a victim
  who successfully dodged behind cover could still die"
  ([Edgegap summary](https://edgegap.com/blog/game-backend-deep-dive-overwatch-2016-netcode-architecture-rollback)).

**Analysis:** Sub-tick validates ADR-0005's split even for a shooter: ticks for
simulation, *timestamps* for inputs. Our version: intents carry the client's
send tick, the engine records and echoes `applied_tick` — the client can then
measure its effective control latency and the engine's intent log gets CS2-
grade ordering evidence for free (already in the log schema per
[[arch-nats-backbone]]). The OW clamp is a reminder that even games with rewind
machinery cap it for fairness — our no-rewind stance is the strict version of
the same ethics.

### 7.5 Surfacing late input to the human

Overwatch's lightning-bolt icon "indicates that the client has not heard
from… the game server for an extended period of time and that a disconnection
is likely" ([Blizzard forums](https://us.forums.blizzard.com/en/overwatch/t/red-square-with-lightning-bolt/520917));
Rocket League added "Quality Connection Status icons" in v1.43
([patch notes](https://www.rocketleague.com/news/patch-notes-v1-43-tournaments-update)).

**Analysis:** A human driver whose intents are silently arriving late will
perceive the car as sluggish and misjudge the sim, not their connection. A
connection-health glyph driven by `applied_tick − send_tick` is one HUD element
that prevents a class of false "the engine feels broken" reports in the chaos
demo.

## 8. Human factors: why no lag compensation and low prediction stakes

- Green's driver reaction-time synthesis: **Expected** (alert, anticipating):
  "The best estimate is **0.7 second**"; **Unexpected** (brake light ahead,
  signal change): "about **1.25 seconds**"; **Surprise** (side incursion):
  "**1.5 seconds**"; steering reactions run 0.15–0.3 s faster than braking;
  detecting *deceleration of the car ahead* is among the slowest cues
  ([visualexpert.com summary](https://www.visualexpert.com/Resources/reactiontime.html);
  paper: Green 2000, "'How Long Does It Take to Stop?' Methodological Analysis
  of Driver Perception-Brake Times," *Transportation Human Factors* 2(3),
  195–216). Green's own caveat: "A 'standard' or 'generally accepted' PRT
  cannot and does not exist."
- AASHTO's design value: 2.5 s perception-brake time "encompasses the
  capabilities of most drivers (including older [drivers])" (Fambro et al.,
  [TRB Circular EC003](http://onlinepubs.trb.org/onlinepubs/circulars/EC003/ch33.pdf),
  ⚠ scanned PDF, quote via search index; corroborated by
  [LA complete streets manual](https://completestreetdesignmanual.engineering.lacity.gov/e-400-general-roadway-design-elements/e-440-sight-distance/e-442-safe-stopping-distances)
  and [UC ITS synthesis](https://escholarship.org/content/qt5hg5m6sm/qt5hg5m6sm_noSplash_07f084f05dcadc13dc6794a6dce9dd3c.pdf)).

**Analysis:** Worst-case machine-added control latency ≈ half-RTT (10–75 ms) +
≤1 tick buffering (100 ms) + display interpolation (~200–300 ms) ≈ 0.3–0.5 s —
below the ~0.7 s floor for an *alert, expecting* human and well below the
1.25–1.5 s typical. The machine's delay is under the human's noise floor; that,
plus the absence of aim mechanics, is the quantitative justification for
ADR-0005's no-lag-compensation rule. Lag compensation exists to make *aiming at
interpolated targets* fair (Bernier); traffic has no such verb. Note the one
nuance that cuts our way: the car-following cue (lead-car deceleration) is the
slowest human response — exactly the interaction where control latency would
matter most.

## 9. Bandwidth and sizing anchors

Published numbers:

- Q3: server 20 Hz; client `rate` default **3000 B/s**, `snaps` 20; per-entity
  cost = bit-marked field diffs ([jfedor](https://www.jfedor.org/quake3/)).
- Source: `rate` guidance 4500 (modem) / 10000+ (DSL) B/s; `cl_updaterate` 20
  default vs 66 Hz tick ([Source wiki](https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking), ⚠ via
  [Wayback](http://web.archive.org/web/20211230133156/https://developer.valvesoftware.com/wiki/Source_Multiplayer_Networking)).
- Fiedler: raw 225–321 bits/entity → ~80 bits (10 B) compressed; 900 entities @
  60 Hz naive = 17.4 Mb/s → 256 kb/s target
  ([snapshot compression](https://gafferongames.com/post/snapshot_compression/)).
- Tribes: 2 KB/s total client contract (10 pps × 200 B) ([paper](https://www.gamedevs.org/uploads/tribes-networking-model.pdf)).
- Valorant: 128 Hz fixed sim both sides; ~35 ms ping target for 70% of players
  ([Riot tech blog](https://technology.riotgames.com/news/peeking-valorants-netcode)).
- EVE: 1 Hz physics tick; new Quasar layer pushes "roughly 10,000 messages
  being sent out to EVE clients every second" over **NATS**
  ([CCP dev blog, Feb 2025](https://www.eveonline.com/news/view/paint-your-ship-red-and-make-it-faster)).

**Explicit finding — no published per-vehicle wire sizes exist** for traffic
simulators (SUMO/CARLA document schemas, not bytes) or for cars in game netcode
talks (all FPS entities). Defensible derivation for us: lane-id (16–24 bits) +
lane offset s (16 bits @ cm on ≤655 m lanes) + quantized speed (8–12 bits) +
accel + lane-change/turn-signal flags ≈ **8–16 B/vehicle uncompressed**. At
10 Hz: a 100-vehicle AoI ≈ 8–16 kB/s (64–128 kbps); a whole 10k-vehicle city
broadcast ≈ 0.8–1.6 MB/s per client — which is precisely why full-world fan-out to every
client dies at city scale and interest gating (§3) is non-negotiable, while
*per-cell* messages on NATS stay trivially within core-NATS territory
(loopback RTT ~65 µs and >1M msgs/s anchors in [[arch-nats-backbone]]).

## Open Questions

- Cell geometry: Euclidean grid (observer-viewport-friendly) vs graph
  neighborhoods (driver-ego-friendly) vs both compiled to the same cell
  subjects — needs [[arch-road-graph-model]] ids and a sizing experiment.
- Which clamp subset ships to the TS client as "shared movement code" for ego
  prediction (pm_shared precedent) — gap-to-leader accel cap only, or IDM-lite?
- DR threshold publishing: adopt at what measured bandwidth, and does the
  heartbeat double as AoI eviction? (v1: publish all, measure.)
- Snapshot message layout: per-cell full-state vs per-vehicle subjects with
  client-side conflation — subject-count vs payload-count trade at 10k vehicles
  ([[arch-nats-backbone]] subject-scale anchors).
