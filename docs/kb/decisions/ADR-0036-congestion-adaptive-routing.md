# ADR-0036: Congestion-adaptive routing

- **Status:** PROPOSED
- **Date:** 2026-07-30
- **Amends:** ADR-0021 (route following), ADR-0006 addendum 2026-07-30
  (route resolution cost model). No subject changes. Two payload surfaces
  move, both additive and gated: the TSKF keyframe payload bumps to v6,
  written ONLY while the flag is on (flag-off states marshal
  byte-identical to v5 and below; readers accept v2–v6 — asyncapi
  Keyframe/KeyframeFrame updated in the same commit; a v6 payload into a
  flag-off spec is rejected loudly, which IS the reader migration note),
  and `RunMeta`'s embedded RunSpec JSON gains the exported
  `Params.AdaptiveRouting` field (old payloads decode it as off; old
  readers ignore it).
- **Builds on:** the free-flow-time weight change (ADR-0006 addendum
  2026-07-30), ADR-0019 (metered route budgets), ADR-0034 (what a stopped
  network costs).

## Context

Route following is static: one destination lane at spawn, next-hop tables
computed once over free-flow weights, never invalidated, never re-planned.
Every vehicle sharing an OD pair takes the identical path no matter how
loaded it is (all-or-nothing assignment), and no vehicle ever diverts
around a jam it drives into. Real fleets do divert — GPS guidance,
local knowledge, visible queues — and that diversion is a load-shedding
mechanism a grid under oversaturation depends on.

Measured on chi-loop-urban (2026-07-30), `data/runs/drain-chi-base/`:
seed 42 at the "half" demand profile collapses from 25 km/h to a FULL stop
by minute ~65 — zero exits, zero movement for the remaining 5+ sim-hours,
6,448 vehicles frozen at the horizon, 766 of 9,335 trips completed. The
ADR-0034 escape stranded 2,121 vehicles without unlocking the core. Seeds
1000–1003 survive the same demand in degraded form (~8.6 km/h and still
falling at 90 min), so the difference between "spiral" and "dead stop" is
partly luck of the realization — an unhealthy property either way: the
network has no mechanism to work through heavy load.

## Decision

Three mechanisms, all deterministic (ADR-0005), gated behind
`Params.AdaptiveRouting` (default OFF until the validation baselines land;
flipping the default is a follow-up with its own measured baselines).

### 1. Per-lane smoothed travel time (the congestion signal)

Each lane carries `ttEMA`, initialized to free-flow time
(`length / speedLimit`). A vehicle leaving a lane contributes one sample —
its dwell time on that lane, `(exitTick − entryTick) · dt` — folded in with
α = 1/8. Independently, every tick every lane's `ttEMA` relaxes back toward
its free-flow time with a 10-minute time constant (dt-derived), so a jam
that has cleared stops repelling traffic on the same timescale it formed.

- `ttEMA` is state that decides behavior → it is KEYFRAMED (the ADR-0034
  stuckTicks precedent: anything that decides what a vehicle does must
  survive a restore exactly; float64, no quantization).
- The vehicle's lane-entry tick is new per-vehicle keyframed state. A
  pre-v6 keyframe restored into a flag-on spec starts every dwell clock at
  the restore tick — "no evidence yet", not a capped run-long poison.
- Dwell samples are capped at `StrandAfterS` (strand removals contribute
  their sample too — the failure mode the feature exists to route around
  must leave evidence) and FLOORED at the lane's free-flow time, so a
  chained same-tick hop cannot make a lane read cheaper than empty. The
  floor means lateral hops and partial traversals contribute a full
  free-flow sample — an accepted dilution of congestion evidence on
  lanes vehicles escape by lane-changing (review question, 2026-07-30);
  restricting samples to full junction-to-junction traversals is a
  refinement, not a correctness fix.

### 2. Epoch-frozen weights, epoch-stamped tables, static-reference
### hysteresis (the re-plan)

The routing weights are FROZEN per epoch: every 60 sim-seconds (dt-derived
— dt is a scenario parameter) each lane's `ttEMA` is copied into `ttSnap`,
a keyframed per-lane array, and table builds read `ttSnap` only. The
memoized per-destination next-hop tables (`routeTabs`) gain an epoch
stamp; accessing a stale table recomputes it by reverse Dijkstra over
`ttSnap` — same algorithm, tie-breaks, and predecessor order as the
static builder — IMMEDIATELY and unmetered. A per-tick recompute budget
(ADR-0019 style, serving stale tables under pressure) was designed here
first and rejected in external review: tables from different epochs
coexisting is exactly what a mid-run restore cannot reproduce (the older
freeze is gone), so every table in play is always current-epoch. The
price is an epoch-boundary CPU spike of one Dijkstra per destination
actually asked for that epoch, on the tick it is first asked — the same
shape as the pre-existing first-use build rule.

Hysteresis: lane i keeps its FREE-FLOW (static-table) next-hop unless the
new path beats the static path's cost under current frozen weights by
more than max(30 s, 15%). Both costs are REALIZED for the candidate (the
mixed chain with the splice installed), while the defended old path is
the pure static chain — a documented asymmetry: if downstream lanes
already accepted splices, the served old path and the compared static
path can differ; the static chain is the stable reference, so that is
the comparison the rule makes. The reference is the static table, NOT the
previous epoch's adaptive table — with hysteresis history, a mid-run
restore (derived tables rebuilt empty) picks hops the live engine had
rejected, an ADR-0029 replay violation found in external review; with the
static reference every table is a pure function of (frozen weights,
network), and `TestAdaptiveRestoreIsCRCExact` pins bit-exact continuation
across a mid-run restore. Oscillation damping is the margin's job either
way; the static reference additionally returns traffic to free-flow
routes as congestion clears. Acyclicity of the mixed (new/static) table
is CONSTRUCTED, not argued: a candidate splice installs only if the mixed
chain — with all earlier candidates already installed — still reaches the
destination within one hop per lane (fixed lane order, inductive
termination at the destination; the proportional-telescoping argument
does not survive the additive 30 s margin, and a free-flow loop would
feed floored dwell samples that never break it — both review findings).

### 3. Scope: whole-fleet, not a seeded fraction

SUMO reroutes a per-vehicle fraction because each of its vehicles owns an
explicit path. Our Route axis is a destination plus shared tables, so the
natural unit of adoption is the table, not the vehicle; a per-vehicle
fraction would require stored per-vehicle paths (memory + a second
route-following mode) for realism that hysteresis already approximates.
If the validation runs show oscillation despite hysteresis, the fallback
is per-vehicle adoption epochs (vehicle adopts the current epoch's table
only if a seeded coin — pure function of (scenario seed, vehicle ID) via a
new `DeriveStreamDomain` label — lands it in the adaptive fraction), which
needs no stored paths, only a keyframed epoch word per vehicle. That
fallback is designed here so a reviewer can see the exit ramp; it is NOT
implemented in v1.

## Consequences

- **Route intent semantics** (ADR-0006): the `route` axis still means
  "destination lane id; persistent". Resolution is now congestion-adaptive
  when the param is on — contract versions version the wire, not the world
  model (2026-07-24 precedent). With the flag OFF behavior is bit-identical
  to today, which keeps every M1–M3 CRC fixture and all existing baselines
  valid.
- **Replay:** with the flag ON, runs are deterministic and CRC-verified
  like any other, and — because tables are pure functions of (frozen
  weights, network) with no hysteresis history — a MID-RUN restore
  continues bit-exactly (`TestAdaptiveRestoreIsCRCExact`). The epoch
  freeze is the mechanism: within an epoch the routing weights are the
  keyframed `ttSnap`, so a rebuilt table is the table the live engine
  served. The price is up to one epoch of lag between congestion evidence
  and routing response. Recordings made with the flag off replay
  bit-identically (flag state lives in RunSpec/Params, which recordings
  carry); a v6 keyframe into a flag-off spec is rejected loudly.
- **Cost:** two float64 per lane per keyframe (ttEMA + ttSnap); one u64
  per vehicle (laneEntryTick); an epoch-boundary spike of one Dijkstra
  (~55k nodes, single-digit ms on chi-loop-urban) per destination actually
  asked for that epoch, plus a per-tick per-lane relaxation multiply.
- **Contract (ADR-0006):** `RunMeta` embeds `RunSpec` as JSON in the KV
  run registry (`engine/natsio/registry.go`), so the new exported
  `Params.AdaptiveRouting` field appears in that payload. The change is
  additive and backward-compatible in both directions: old payloads lack
  the field and decode to the zero value (off — today's behavior); old
  readers ignore the unknown field. No subject, subject schema, or
  version changes; this note is the migration note.
- **Failure mode bounded:** a poisoned or pathological ttEMA field can
  only choose worse paths, never teleport, never abandon the destination —
  the lateral guardrail (ADR-0021) and arrival logic are untouched, and
  with weights ≡ free-flow the mechanism reduces exactly to the static
  tables.

## Validation plan (before the default flips)

1. The seed-42 drain scenario (`data/scenarios/chi-loop-urban-half-base`,
   216,000 ticks): success = no permanent 0.00 km/h plateau; the network
   clears demand after injection stops.
2. Four-seed paired bracket vs the static-routing build
   (`scripts/whatif.py`, seeds 1000–1003, 54k ticks): mean interval speed,
   completions, stranded counts.
3. Oscillation check: per-epoch count of next-hop changes and ttEMA
   variance on the bracket runs — expect bounded flip counts under
   hysteresis; a runaway is the seeded-fallback trigger.
4. Determinism: same-seed CRC equality with the flag on; flag-off
   bit-identity against pre-ADR fixtures.

## Alternatives considered

- **Per-vehicle stored paths (SUMO-style).** Realistic per-vehicle
  heterogeneity; costs a keyframed path array per vehicle, a second
  route-following mode, and path-aware lane-change guardrails. Deferred —
  the shared-table hysteresis design gets most of the effect for a tenth
  of the machinery.
- **Controller-side rerouting over the existing RouteSet intent.** No
  engine change, but async intent latency already proved fidelity-fragile
  (ADR-0035 blocked finding: batching changed which tick intents land on),
  and driver-side routing compute is what ADR-0019 moved engine-side.
  Rejected as the mechanism; remains available to experimenters.
- **Do nothing; fix demand instead.** Rejected: oversaturation happens in
  real cities and they do not dead-stop — the missing mechanism is the
  diversion, and the seed-42 dead stop is a model artifact as much as a
  demand artifact.
