# ADR-0008: Controller contract (intents, grants, roles, failover)

- **Status:** ACCEPTED
- **Date:** 2026-07-17 (design review, ratifying
  `concept-vehicle-controller-interface`, `arch-state-authority`, and
  `domain-signal-control` research)

## Context

The engine is authoritative over world state; controllers (AI policies,
scripts, humans, the default driver, scenario directors, signal controllers)
are external NATS clients (ADR-0002, ADR-0005, ADR-0006). This ADR fixes the
engine↔controller contract: the intent vocabulary, what persists across
ticks, how vehicles are claimed, the privileged roles, and what happens when
any controller dies. Key evidence: TraCI's blocking barrier (11× slowdown)
and sticky-command hazards are the failure modes; SMARTS/highway-env prove
capped observation windows support real policies; Rocket League/GGPO netcode
validates ego prediction + input buffering; the NEMA cabinet MMU is the
precedent for engine-enforced safety clamps.

## Decision

1. **One `Intent` message per vehicle per tick, four orthogonal axes:**
   longitudinal (target acceleration as the canonical primitive; optional
   speed-setpoint variant), lateral (lane left/right/none + explicit turn
   choice at junctions), routing (next-link/destination), signal state.
   Absent fields = no change. AI, scripted, and human controllers all emit
   this one type.
2. **Per-axis persistence (specify, don't improvise):**
   | Axis | Semantics |
   |---|---|
   | accel | one-shot per tick, refreshed every tick |
   | speed setpoint | persistent until replaced (cruise) |
   | lane change | one-shot, expires if infeasible |
   | turn at junction | one-shot, held until consumed |
   | routing | persistent |
   | signals | persistent state (informational in v1) |

   Transport hold-last (≤ 1–2 ticks on message loss) is orthogonal healing,
   not semantics.
3. **Observations:** declared area-of-interest window at attach (radius, max
   neighbors, features); per tick the engine publishes own-vehicle exact +
   neighbor list sorted/capped. No per-object query RPC on the hot path.
   Ego prediction in TS clients (gap-cap only in v1) per `arch-state-authority`.
4. **Attach handshake:** contract version, controller type, cadence, claims —
   and **grants** (declared capabilities). Exclusive per-vehicle claim,
   engine-arbitrated. Multi-vehicle claims allowed (fleet controllers, the
   "traffic-god" player). Same-tick competing intents resolve by a
   **deterministic tie-break list** (grant level, then vehicle ID) — never
   arrival-time ordering, which would leak wall-clock nondeterminism into
   replay. Intents apply at tick boundaries with 1-tick minimum latency
   (ADR-0005); the engine echoes `applied_tick` (ADR-0006).
5. **Roles** (one plumbing system, permissions differ):
   - **Ordinary controllers** — drivers, including scripted vehicles (a script
     is a controller, not a privileged channel).
   - **Default driver** — the external reference controller (IDM + MOBIL +
     router, `destination` parameter). Ambient-traffic generator, handoff
     source for human claims, orphan re-claimer, and host of the
     **introspection interface** ("what would you do with this state?") as a
     NATS request/reply side channel — deliberately *not* a field in engine
     observations.
   - **Director** — elevated grants: spawn/despawn/teleport/trigger (the
     OpenSCENARIO verbs), engine-arbitrated, recorded on the record plane.
     The traffic-god player is a human director.
   - **Signal controllers** — elevated grants over signal actuation: the
     cabinet vocabulary (call / hold / force-off / omit / next-phase +
     coordination sync) into which every real algorithm compiles. The engine
     enforces conflict matrix, min greens, and clearance intervals regardless
     of commands (MMU pattern — same clamp philosophy as vehicle intents).
     Interface is fixed now; full NEMA feature coverage is deferred.
6. **No driving logic in the engine — failover is operational.** Uncontrolled
   vehicles bridge on hold-last until a default driver re-claims them via
   unclaimed-vehicle events. The default driver runs as a **supervised
   fleet**: replicas shard emergently via exclusive claims (no leader
   election), sized to absorb one full peer loss. The engine **pauses the
   run** when available claim capacity < demand for T ticks; pause/resume are
   recorded on the record plane and are invisible to tick determinism.
   (2026-07-24 scope note: the detach timeout
   behind this section is a PACED-run regime — the sweep is skipped at
   `PaceFloor == 0`, where tick time outruns any client's wall-clock
   reaction; batch mode is for offline runs with embedded clients. See the
   ADR-0006 2026-07-24 addendum and `ctlHeartbeat` in asyncapi.)

## Consequences

- Contract surface = the AsyncAPI doc (ADR-0006) + this ADR's tables (axes,
  persistence, grants matrix); changes are ADR-gated.
- Replay never runs controllers: recorded intents (and director verbs) come
  from the JetStream log.
- The default driver doubles as the reference controller implementation —
  the contract's sufficiency is dogfooded continuously.
- Determinism requires policy RNG seeded per vehicle (ADR-0007), which makes
  fleet failover and orphan re-claim behaviorally invisible.
- Revisit triggers: AoI window sizing vs tick budget (benchmark);
  buffer-health feedback for worst-case connections; dead-reckoning publish
  thresholds; signal feature coverage when the first advocacy corridor is
  chosen.

## Clarification (2026-07-17, M3/M4 bring-up)

- **Headless harness policy**: for physics validation and CI, the engine keeps
  an optional in-kernel uncontrolled-vehicle policy (the bring-up IDM/MOBIL
  used by the M1/M2 test suites) behind a world-config flag. It is the
  reference implementation the external default driver is differentially
  tested against, and it is **off** in live runs — where uncontrolled vehicles
  follow item 6 (hold-last → re-claim). This does not reintroduce a fast
  path: the flag exists only where no bus is attached.
- **Hold-last is synthesized intents**, flagged `held` in the arbitrated log —
  replay stays verbatim, no expectation reconstruction.
- **Shared reference policy** (`engine/policy.go`): kernel and external driver
  evaluate the same IDM/MOBIL policy code; observations ship the
  engine-resolved policy context per claimed vehicle, so client decisions
  cannot drift from engine semantics.
- **Tie-break implemented** as (grant desc, vehicle asc, controller, seq),
  first-wins with superseded logging.
- **v1 stances**: directors pass 4-axis intents unfiltered (privileged verbs
  land with scenario work); cadence >1 is contract-complete but lightly
  dogfooded; AoI radius currently measures the placeholder lane projection.

## Clarification (2026-07-20, M10 director verbs land)

- **The grant model did NOT extend** (no ADR-0012): the `director` grant
  declared in §5 covers spawn verbs as designed. The verb channel
  (`ts.{run}.ctl.verb.{controller_id}`, request/reply) enforces the grant
  at the contract layer — a verb from a controller without the grant is
  rejected ("verb requires the director grant"); the engine stays
  authoritative over injection (validation, origin clearance, density
  cap), per §5's "engine-arbitrated, recorded on the record plane".
- **v1 verb vocabulary is `spawn` only**; despawn/teleport/trigger remain
  open and land additively on the same channel (unknown verbs are
  rejected, never silently ignored). Directors may still pass 4-axis
  intents unfiltered (the M3/M4 stance).
- **Idempotency is the director's request id**: spawn verbs carry a
  director-assigned `request_id`; the engine dedups per run and replays
  the original answer to retries — a supervised director restarted after
  failure re-issues its deterministic program and cannot double-spawn
  (the §6 failover philosophy applied to the director role).
- **Liveness applies to directors too**: a director that only emits
  sparse verbs must heartbeat inside DetachAfterTicks or it is detached
  like any controller (learned in M10 bring-up; the reference
  `engine/cmd/demand-director` heartbeats on the snapshot rhythm).
