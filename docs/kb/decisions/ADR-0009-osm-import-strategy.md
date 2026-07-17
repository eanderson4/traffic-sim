# ADR-0009: OSM import strategy (bootstrap, oracle, identity)

- **Status:** ACCEPTED
- **Date:** 2026-07-17 (design review, ratifying `integration-osm-extraction`
  research)

## Context

VISION Use Case 2 (real road networks) needs OSM data compiled into the
lane-level road graph of `arch-road-graph-model`. Only three codebases do
full OSM→lane-graph compilation (SUMO netconvert, A/B Street,
osm2streets-minus-turns); nothing in Go goes beyond parsing; OSM lane tagging
is sparse (`turn:lanes` ~0.7%, `lanes` ~11% of motor roads) and OSM element
IDs churn on way splits. Research: `docs/kb/raw/integration-osm-extraction/`.
The owner decision was phasing: netconvert bootstrap first vs Go importer
from day one.

## Decision

1. **Bootstrap with netconvert first.** SUMO's netconvert (EPL-2.0, used as a
   binary preprocessor — `.net.xml` → our compiled network format) unblocks
   real-network scenarios now, with zero license friction. Guard against the
   bootstrap becoming load-bearing: the Go importer stays on the roadmap with
   the oracle diff suite (below) as its acceptance gate.
2. **Own Go importer in parallel** on `paulmach/osm` (MIT), built
   incrementally. **netconvert remains permanently as the differential-testing
   oracle** — the same extract compiled both ways must match within
   tolerances; the diff suite is how the Go importer earns trust.
3. **Two-tier identity, mandatory.** The importer assigns our durable IDs;
   OSM IDs ride as provenance only. Lane inference is **defaults-first**: a
   reviewed per-region class→defaults typemap, tags as overrides, tags
   validated against each other; every inferred element carries a `guessed`
   flag; an import report ships as an auditable scenario artifact.
4. **Acquisition in two modes:** Overpass QL for interactive bbox extracts
   (fair-use limits); Geofabrik PBF + `osmium extract` with buffer for
   reproducible/CI imports. Always extract with buffer and clip downstream.
5. **ODbL posture: distribute the recipe, not the file.** A compiled network
   is a Derivative Database (share-alike if distributed); the recipe —
   region + extract date + bbox + importer version — is what we share.
   Demand/control/metrics layers and code stay ours (Collective Database);
   simulation outputs are Produced Works, unencumbered.
6. **v1 simplifications:** left-hand traffic is a global import parameter;
   `restriction:conditional` relations ignored-with-flag; turn restrictions
   applied after way-splitting to the correct segment; the orphan-lane
   invariant (never strip a lane's last turn) outranks any tag.
7. **Network variants are authored delta patches** anchored on our durable
   IDs (grammar owned by the scenario format): small op set, fail-loud
   validation at apply time, derived artifacts (conflict sets, internal
   lanes) always recompiled from the patched model. A network-diff tool
   verifies intent vs effective change; whole-network replacement is the
   degenerate case.

## Consequences

- Use Case 2 is unblocked on day one via the bootstrap; the Go importer's
  progress is measurable against the oracle instead of aspirational.
- Re-imports of newer OSM extracts produce new IDs; delta patches are valid
  against their base import. Fuzzy anchoring (name/shape selectors that
  survive re-import) is later work, gated on ID-stability evidence.
- The compiled-network artifact carries licensing obligations; the recipe
  distribution posture keeps our distribution clean.
- Open (tracked in `integration-osm-extraction`): whether map-edge demand
  portals are a network-file or scenario concept; stage-based import for
  European-style networks; `restriction:conditional` into scenario time
  slicing.
