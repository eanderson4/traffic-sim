# ADR-0007: Vehicle model conventions (position, gap, multi-class)

- **Status:** ACCEPTED
- **Date:** 2026-07-17 (design review, ratifying `domain-traffic-flow-models`
  open question)

## Context

Every microscopic model must pin what a vehicle's "position" and "gap" mean,
or model equations, metrics, importers, and the controller contract drift
apart by a vehicle length (~5 m systematic error on every pair — density,
queue length, and wave speed all degrade). SUMO uses front-bumper position +
`minGap` (empty asphalt at standstill); the IDM literature uses the
bumper-to-bumper gap `s` as the primary state variable with jam value `s0`.
Both agree on bumper-to-bumper; the risk was inventing a third convention
(spacing, front-to-front) somewhere in the pipeline. Research:
`docs/kb/raw/domain-traffic-flow-models/`; road-graph occupancy is
`(laneId, s)` per `docs/kb/raw/arch-road-graph-model/`.

## Decision

1. **Position = front-bumper coordinate `s` along a lane** (consistent with
   `(laneId, s)` occupancy and the road-graph geometry-by-reference model).
2. **Gap = bumper-to-bumper empty space, everywhere** — engine state, intents,
   observations, metrics, and scenario files. One jam-gap parameter, named
   **`s0`**, measured bumper-to-bumper; vehicle **`length`** is a separate
   parameter. Spacing-derived quantities (density, spacing itself) are
   *computed, never stored*.
3. **Multi-class vehicles are first-class**: a vehicle type carries `length`,
   `width`, and dynamics parameters (`s0`, `T`, `a`, `b`, MOBIL set); types
   mix freely in one lane. The bumper-to-bumper convention is what makes
   mixed-length traffic uniform — no "whose length?" ambiguity per pair.
4. **Default dynamics: IDM car-following + MOBIL lane-changing** (research
   validation of the 100 ms tick + ballistic integrator + stopping override
   per ADR-0005). Policy randomness (MOBIL draws, routing tie-breaks) uses
   **per-vehicle seeded RNG streams** keyed by vehicle ID, per ADR-0005's
   stream-per-concern discipline — never per process, so fleet failover and
   CRN experiment protocols are behaviorally invisible (ADR-0008).
5. **Boundary conversions are the importers' job**, with tests: NGSIM
   front-center positions, SUMO `minGap`, drone-dataset footprints — all
   convert to the canonical semantics at ingest.

## Consequences

- Calibrated IDM/MOBIL parameters from the literature transfer without
  conversion, since the convention matches the literature's.
- Metrics (density, queue, spacing) derive from gap+length; no second
  representation can drift.
- Scenario vType definitions name `length` and `s0` separately; truck share
  enters as a type distribution, not a special case.
- Revisit triggers: `b_safe` vs time-gap enforcement benchmark at bring-up;
  IIDM/ACC equation transcription from primary sources before coding those
  variants; Erdmann LC2013 constants as a calibration surface.
