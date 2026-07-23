# ADR-0011: Fixed-time signal control (kernel-run programs, data-driven phases)

- **Status:** ACCEPTED (drafted with M8 implementation)
- **Date:** 2026-07-19

## Context

Through M7, signalized junctions were the one junction class with no
right-of-way modeling at all: `traffic_light` approaches compiled to
`RowNone` and traversed freely (ADR-0010 §5), and the importer dropped the
`tlLogic` elements the `.net.xml` already carries — on the I-280 reference
network, two junctions (`5464972060`, `5464972061`) with static 82/3/5 s
green/amber/red programs metering a secondary-road approach.

ADR-0008 §5 fixes the long-term shape: signal *policy* is an external
client role emitting the cabinet vocabulary, with the engine enforcing
safety invariants. That external interface is a message-contract change and
is deliberately NOT this milestone. What lands here is the v1 form the
review asked for: kernel-run fixed-time programs compiled from the network
file, with the phase representation data-driven so external algorithms can
command it later.

## Decision

1. **Model: fixed-time programs as network data.** A program is a phase
   list — per phase a duration (seconds) and a per-link state string in the
   SUMO tlLogic alphabet — plus a cycle offset. Internal lanes bind to
   (program, link index). The light state of an approach is a **pure
   function of the tick count and the compiled program**: durations round
   onto the tick grid at engine build (`Params.Dt`; a phase rounding to 0
   ticks fails the build), phase windows are half-open intervals, the cycle
   wraps, and the offset follows SUMO semantics (phase 0 begins at
   `offsetTicks`). No wall clock, no RNG, no map iteration. Phase state
   needs no CRC or keyframe coverage of its own: it derives from the tick
   count, which the keyframe already restores bit-exactly — save/load
   preserves the lights for free (tested).
2. **Conservative state-char mapping.** Only `g`/`G` (go), `y` (amber),
   `r` (red) exert control. Everything else — `o`/`O` (off, blinking),
   `u` (red-yellow), unknown chars, a link index without a state char —
   means *the signal exerts no control* and the approach falls back to its
   ADR-0010 priority behavior (its compiled `row` class; tl-bound
   approaches carry none, so in practice `RowNone` = free traversal,
   exactly the pre-signal semantics). Documented in the contract.
3. **Enforcement composes with the M7 stop-line guardrail**
   (`rowGate` in `computeAccels`, the shared-path cap every controller
   inherits):
   - **red** — hold at the stop line (the virtual stop-line wall);
   - **amber** — hold only if the vehicle can stop comfortably before the
     line (`v² ≤ 2·d·B`, the same brake-comfort criterion as ADR-0010);
     a vehicle that cannot is committed and proceeds as on green;
   - **green** — flow, but the ADR-0010 box checks still gate: never enter
     a box a conflicting vehicle occupies or whose exit has no room (the
     `boxBlocked` helper is shared verbatim with the priority model).
     Green adjudicates *approaching* conflicts by the light itself, so the
     priority model's approaching-foe checks do not apply.
4. **Format: optional v1 extension, backward compatible.** Top-level
   `signals` list (id, junction, offset, phases) plus `tl`/`tlLink` on
   internal lanes. Files without them load with all junctions
   unsignalized — byte-for-byte the pre-extension semantics; no version
   bump; the migration note lives in the contract. The loader validates
   fail-loud (duplicate/unknown programs, ragged state strings, bad
   durations, link out of range, bindings on non-internal lanes).
5. **netimport compiles static tlLogic only.** `type="static"` programs
   with at least one bound connection are emitted; actuated/other types,
   missing tlLogic elements, and out-of-range link indices are reported
   and their approaches stay unsignalized. The import report gains
   `signalPrograms` and `signalLinks`; `signalizedJunctions` now lists
   only junctions WITHOUT a usable program (the "traversed WITHOUT
   right-of-way" warning flips accordingly).
6. **The data-driven phase state is the external-control seam (D1).** The
   gate reads only `(program, linkIdx, tick)` through `sigState`; an
   external signal controller (ADR-0008 §5 cabinet vocabulary: call /
   hold / force-off / omit / next-phase) later replaces the fixed-time
   derivation of that per-approach state — the link binding, the state
   alphabet, and the enforcement path are unchanged. The command subjects
   and the engine-side clamps (conflict matrix, min greens, clearance)
   are a message-contract change and get their own ADR.

## Explicitly NOT modeled (revisit triggers)

- **NATS signal-command subjects or any message-contract change** (sacred
  per AGENTS.md; the ADR-0008 §5 external interface).
- **TSSF frame changes and viz rendering of lights** — phase state is
  kernel-internal for now.
- **Actuated/adaptive programs** (detectors, gap-out/max-out, max-pressure),
  the **NEMA dual-ring/barrier structure** (static tlLogic is a stage list;
  ring-barrier stays the KB's primary model for the actuation work), and
  **coordination machinery** beyond the single-program offset (offsets
  already derive from the shared tick count — the master-clock finding of
  the KB article).
- **Right-on-red, pedestrian intervals, authored clearance** — phases are
  taken as the source file writes them (the I-280 programs carry their own
  amber/all-red).
- Revisit when: the first external signal controller client is designed;
  actuated/adaptive work lands; the scenario format subsumes the network
  file (the extension migrates with it, same as ADR-0010's); collision
  observations at modeled signalized junctions exceed transient level.

## Consequences

- I-280 acceptance (`cmd/simrun -netfile ../data/networks/i280-woodside/i280.json -density 80 -ticks 3000 -seed 1 -v`, re-imported network):
  **rate 600: 0 collisions** (was 0), crc `19781e2d4f35439e` → `e92229c4a89d3709`;
  **rate 2400: 8 collisions** (was 8), all at `j:1293205808` — the
  `right_before_left` junction whose sub-meter same-path queue-release
  overlaps ADR-0010 documented, unrelated to signals — crc `482b80fbf180401b`
  → `ea2a376959e22284`. The CRC moves are the expected kind: the lights now
  gate (red phases queue vehicles at the two metered approaches), so
  trajectories legitimately differ.
- Ring/lanedrop CRCs are bit-identical (no junctions → the gate never
  fires); networks without a `signals` section keep byte-for-byte behavior
  (the pre-extension i280 file reproduces its M7 CRCs).
- Throughput cost at the metered approaches is the point of the program,
  not a regression: red phases hold demand at the stop line (rate 600:
  276 despawned vs 280 pre-signal; rate 2400: 291 vs 297).
- The kernel gains `engine/signal.go` (program model, tick compilation,
  `sigGate`); `rowConflict` factors out `boxBlocked` unchanged for shared
  use. netimport's report flips both I-280 junctions off the unmodeled
  list; `kernel+netimport` test coverage: phase boundaries and wrap,
  offset semantics, red hold, amber both branches, green box-block,
  off fallback, save/load round-trip, determinism, loader validation.

## Amendment 2026-07-23: red is a conditional wall (clearance window); the gate reaches through fragments

The original decision made red **absolute** ("hold at the stop line").
The behavior fixtures (single-junction OSM crops) falsified the two
premises behind that simplicity: netimport emits sub-vehicle-length
fragment lanes at junction boundaries, so a wall applied only at the
current lane's end engages ~0.2 m before the line — far too late — and an
absolute wall re-captures vehicles that were legally committed at the
amber→red transition. The amended semantics:

- **Gate target (`gateTarget`)**: the gate targets the first internal
  lane that *exerts control this tick* along the vehicle's
  picked-successor chain (bounded by `maxLaneHops` and `maxSightM`),
  walking through non-internal fragments and uncontrolled internal lanes
  (back-to-back boxes). The wall distance is measured through the chain,
  so braking begins on the real approach, not on a 0.2 m stub.
- **Red**: within `clearanceSeconds` (3 s, compiled to ticks per program)
  after an amber→red transition the wall applies the *amber* comfort
  criterion (`v² ≤ 2·d·B`) — amber-committed traffic is never
  re-captured mid-clearance (textbook dilemma-zone clearance). Outside
  the window the wall holds anything it can physically stop
  (`v² ≤ 2·d·emergencyDecel`). Both criteria are stateless functions of
  the program and tick, so keyframe restore replays them bit-exactly.
- **Green and committed** movements remain box-gated, and the box checks
  themselves were generalized (see ADR-0010's same-day amendment):
  exit-room walks short successors, box exits are re-checked from inside
  the box, and a downstream red/stop caps release into it.

Fixture evidence: zero *uncommitted* red crossings on the signal-4way
crop (was: mass red-running from spawn-adjacent fragments); the
clearance crossings that remain are all committed-legal and classified
as such by the fixture.
