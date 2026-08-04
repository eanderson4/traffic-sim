# ADR-0027: Baked trip cards — per-vehicle origin, destination and travelled path

- Status: Proposed
- Date: 2026-07-27
- Amends: ADR-0023 (baked replay pipeline — adds two artifacts and two
  `index.json` fields), `contracts/baked-replay-v1.md`
- Does NOT touch: `contracts/asyncapi.yaml`. Nothing here rides NATS.

## Context

Two viz features are wanted for the big Chicago demo:

1. a toggleable layer showing where trips are **born** and where they
   **end**, so the demand pattern is visible on the map instead of only in
   the scenario YAML;
2. clicking a vehicle reveals **where it came from, where it is going, and
   the path it takes**.

Neither is servable from what the baked plane carries today. `TSRB` is 14 B
per vehicle per frame — `id`, quantized `x`/`y`, `angle`, `class`
(`contracts/baked-replay-v1.md`). There is no origin, no destination, no
path. The viz's click handler already exists (`viz/src/main.ts`, the
`vehicles` layer → `hud.inspect`) and can only report id, class and speed,
because that is all a frame holds.

Three facts constrain the design, and each rules out an obvious approach.

**The engine has no route to publish.** `engine/vehicle.go:69` defines
`Route` as the *destination lane id*, annotated "routing axis … informational
on single-path networks". There is no materialized path anywhere in the
kernel: the driver picks an exit lane (`driver/destinations.go pickExit`) and
lane-change decisions consult reachability, but no ordered list of lanes is
ever built or stored. **So "its calculated route" is not a thing that can be
read out of the engine.** What *can* be produced is the path the vehicle
actually travelled, which the bake observes directly.

**A replay has no engine to ask.** The baked plane is static objects behind a
CDN (ADR-0023); at click time there is no process holding world state. So
this data must be *baked*, or it does not exist. The bake is the only moment
it is derivable.

**The obs plane is already at its payload ceiling.** The observation frame
carries every claimed ego in one message and stops fitting the 4 MiB broker
cap near 10,200 egos (measured, `natsio/intentload_test.go`
`TestObsFrameSizeCliff`: 10,500 with empty routes, 10,000 at 24 B routes).
Widening any per-tick frame to carry O/D is therefore not merely wasteful, it
walks toward a known cliff.

## Decision

Bake two new artifacts. Both are derived from the recording during the bake
and are immutable under the existing content-key scheme.

### 1. `trips/od.tsod.br` — the O/D table (feature 1)

One record per vehicle, every vehicle in the run, in ascending id order:

```
header (16 B): magic u32 "TSOD" | schema_version u16 =1 | flags u16 =0 |
               trip_count u32 | reserved u32
per trip (20 B): id u32 | ox u32 | oy u32 | dx u32 | dy u32
```

`ox`/`oy`/`dx`/`dy` reuse **exactly** the TSRB quantization — the same
`index.json.quant` step and origin, the same local metric frame — so the viz
dequantizes with the code path it already has and no second convention
enters the format.

Origin is the vehicle's position at its first baked frame; destination is its
position at its last. Deriving both from *observed positions* rather than
from the scenario's declared portals is deliberate: it means the layer shows
where traffic actually entered and left, including vehicles that expired
without reaching their destination lane, which is a fidelity signal worth
seeing rather than hiding.

One object, not sharded: 20 B × 40,000 vehicles = 800 KB raw, and it is
fetched once, lazily, only when the layer is first switched on.

### 2. `trips/p{block}.tsrp.br` — travelled paths (feature 2)

Sharded by id block, `block = id >> 12` (4,096 vehicles per object), because
a click needs exactly one vehicle's path and must not pay for 40,000.

```
header (16 B): magic u32 "TSRP" | schema_version u16 =1 | flags u16 =0 |
               path_count u32 | reserved u32
per path: id u32 | t0 u32 | t1 u32 | point_count u16 |
          per point: x u32 | y u32
```

`t0`/`t1` are the vehicle's first and last baked tick. Points are its
position at each baked frame, **decimated by perpendicular distance**
(Douglas–Peucker, 2 m tolerance — one lane width is 3.5 m, so 2 m preserves
which lane a path used while collapsing straight running). Same quantization
as above.

This is the path the vehicle **travelled**, and the UI must say so. It is not
a plan, not a shortest path, and not a claim about what the driver intended —
those do not exist in this engine, and labelling a travelled path as a
"route" would invent a routing model the simulation does not have. On a
replay this is strictly more informative anyway: the whole trip is known at
bake time, so a click mid-replay can draw the part already driven solid and
the part still to come dashed.

### 3. `index.json` additions

```
"trips": { "od": "<url>", "paths": "<prefix>", "blockBits": 12,
           "count": <n> }
```

Absent `trips` means a bake predates this ADR; both features degrade to
"unavailable" rather than erroring. The shim must treat it as optional.

## Consequences

- **Bake cost.** The bake already streams every frame, so O/D is free
  (first/last position per id). Paths cost one growing point-list per live
  vehicle — bounded by concurrent vehicles, not total, since a list is
  flushed when its vehicle despawns.
- **Size.** Chicago fw35 at 6,000 ticks: ~12,000 vehicles, ~120 baked frames
  each before decimation, ~25 points after. 12,000 × (14 + 25×8) ≈ 2.6 MB
  raw across 3 objects, well under a megabyte each brotli'd.
- **The O/D layer is honest about expiry.** A vehicle that never reached its
  destination lane still has a destination point — its last observed
  position. The layer will therefore show a cluster of "destinations" at
  whatever queue swallowed them, which on an under-delivering run is a
  visible, useful symptom rather than a hidden one.
- **No live-plane change.** TSOB, TSSF and the intent plane are untouched, so
  the obs cliff is not approached from this direction.

## Alternatives rejected

- **Widen TSRB with O/D per frame.** Origin and destination are constant per
  vehicle; carrying them at 2 Hz multiplies a 14 B record by ~2.4× for data
  that never changes. Rejected on size and on principle.
- **Publish routes on the obs frame.** `ObsEgo` already has a `Route` field
  and it is already implicated in the 4 MiB cliff — 24 B of route moves the
  knee from 10,500 to 10,000 egos. Adding a path would be orders of magnitude
  worse, and the viz does not read TSOB at all.
- **Reconstruct paths client-side from TSRB history.** The shim holds a
  bounded window (ADR-0024) and discards frames outside it, by design. A
  click at tick 5,000 cannot see tick 0.
- **Ship a planned route by adding a router to the kernel.** That is a real
  feature with real consequences for determinism and replay, and it is not
  what "show me where this car is going" requires. If route choice is ever
  added it gets its own ADR; this one deliberately does not prejudge it.

## Open question for ratification

Whether the path artifact should also carry the **lane id sequence** rather
than only geometry. Geometry alone draws the line, which is what the demo
needs; lane ids would additionally let the viz highlight the actual lanes
used, and would make the artifact useful for analysis (corridor attribution
without re-deriving it from points). Deferred until the drawing works, on the
grounds that the analysis use has no consumer yet.
