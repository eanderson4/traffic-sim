# ADR-0010: Junction right-of-way (priority model, guardrail enforcement)

- **Status:** ACCEPTED (drafted with M7 implementation)
- **Date:** 2026-07-18

## Context

Through M6, junction traversal was connection-following only: vehicles
followed junction-internal lanes with no right-of-way enforcement, so
simultaneous arrivals at conflict points overlapped. The I-280 reference
import measured it at corridor demand (seed 1, 3000 ticks, density 80,
harness IDM policy): 160 collision observations at rate 600, 290 at 1200,
853 at 1800, 984 at 2400 — localized at junction-exit funnels (on-ramp
merges onto the motorway mainline, e.g. sections `27945473#1`,
`-417285724`, `30453365`). `contracts/network-format-v1.md` listed the
missing right-of-way / conflict sets as *the* known gap.

The .net.xml already carries what a priority model needs and the importer
was throwing it away: the connection `state` attribute (SUMO's major/minor
`M`/`m`, stop `s`, allway-stop `w`, equal `=`) and the junction `type`.

## Decision

1. **Model: priority junctions only, as a guardrail — not a gospel.**
   Each junction-internal lane carries its approach class, compiled by
   netimport from the connection state:
   - `major` (SUMO `M`): flows unless the box cannot be cleared.
   - `minor` (`m`, and `=` mapped conservatively): additionally yields to
     conflicting traffic that entering would force to brake harder than
     comfortable (`a_req = v²/2d > b_comfortable` of the foe's class).
   - `stop` (`s`, and allway-stop `w` mapped to a plain stop): holds at the
     line until a full stop is reached there once, then acts as minor.
2. **Conflict sets are compiled, not re-derived at runtime.** netimport
   emits, per internal lane, `foesMerge` (same-junction internal lanes
   sharing the successor lane — the exit funnels where the overlaps
   happened) and `foesCross` (same-junction internal lanes whose shape
   polylines properly cross; merge takes precedence when both hold). This
   is the v1 form of the arch-road-graph-model conflict-set work.
3. **Enforcement is a kernel-side cap in `computeAccels`, shared by every
   controller.** When a vehicle on an approach lane may not enter, its
   accel is capped at the *virtual stop-line wall* — the EndWall mechanism,
   IDM toward a standing vehicle at the lane end — so it brakes smoothly to
   the line and proceeds when the gate opens (`engine/rightofway.go`).
   Harness IDM, the cruise servo, and external drivers (clamped accel
   intents, ADR-0008 §5) all inherit it; there is no policy drift to
   dogfood. The gate holds when:
   - a conflicting (foe) vehicle is inside the box (any class);
   - the box exit has no room for the vehicle behind the exit lane's queue
     **tail** (its first vehicle) — "don't enter a junction you can't
     exit" (measuring room at the exit lane's *end* instead was the bug
     that left residual funnel overlaps in the first iteration);
   - a minor approach has an approaching foe that would be forced to brake
     harder than comfortable; major approaches check this only for
     same-exit (merge) foes — crossing foes of a major approach are minor
     themselves and do the yielding;
   - a stop approach has not yet completed its full stop at the line.
   Mutual holds (both vehicles stopped at their lines) resolve by priority
   class, ties within a class by lower vehicle ID — deterministic, no RNG,
   no map iteration, no wall clock.
4. **Format: optional v1 extension, backward compatible.** Internal lanes
   gain `junction`, `row`, `foesCross`, `foesMerge` (all optional, internal
   lanes only). Files without them load with junctions unmodeled — free
   traversal, byte-for-byte the old semantics. No version bump; the
   migration note lives in the contract.
5. **Signals stay UNMODELED.** `traffic_light` junctions and `tl`-bound or
   state-less connections compile to no `row` and traverse freely, as
   before; the import report keeps listing them.
   *(Superseded by ADR-0011, 2026-07-19: static tlLogic programs now
   compile into the signal extension and the kernel gates their approaches;
   only junctions without a usable program remain unmodeled.)*

## Explicitly NOT modeled (revisit triggers)

- **Signal programs** — needs the signal-phase work (see gaps-and-roadmap).
- **`right_before_left` nuance** — we take the connection states SUMO
  writes for such junctions (mostly `M`, with `=` → minor) rather than
  modeling yield-to-the-right; see KB's allway/right-before-left question.
- **Allway-stop arrival ordering / creep** — `w` compiles to a plain stop.
- **SUMO-exact gap acceptance** — the yield criterion is the brake-comfort
  guardrail above, not calibrated gap acceptance; the stop-line duty is
  per approach (`Vehicle.stopDone`, derived state, not CRC'd — same
  precedent as HeldTurn/Cruise).
- **In-box recovery** — a vehicle that stops inside the box (exit blocked
  after entry) is left to car-following; the gate only guards entry.
- Revisit when: signals land; collision observations at modeled junctions
  exceed the residual-transient level below; the scenario format subsumes
  the network file (the extension migrates with it).

## Consequences

- I-280 acceptance (same configs as the baseline above): **0 collisions at
  rate 600 and 1800; 3 at 1200; 8 at 2400** — all of the latter at one
  `right_before_left` junction (`1293205808`), all sub-meter (< 0.6 m),
  low-speed (1–2 m/s) *same-path* overlaps in queue-release transients at a
  congested box exit: ordinary IDM queue-compression overshoot between a
  leader and its follower, not conflicting-path collisions. Conflicting-path
  (funnel/crossing) collisions are eliminated at every tested demand.
- Metering at box exits costs raw throughput vs. the overlap-riddled
  baseline (fewer vehicles despawn per run at high demand) — the price of
  not merging through traffic, not a regression.
- Ring/lanedrop CRCs are bit-identical (no junctions → the gate never
  fires); the I-80 scenario (no junction objects) is unchanged. All
  pre-existing tests pass unmodified.
- The kernel `Lane` finally wires `Internal` (the field existed in the
  format since ADR-0009 but was dropped at load).
- netimport's report gains `yieldApproaches`, `stopApproaches`,
  `conflictPairs`.
