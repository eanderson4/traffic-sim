# ADR-0017: City-scale OSM import decisions (stops, joins, relations)

- **Status:** ACCEPTED
- **Date:** 2026-07-24

## Context

The ep-03 countdown work imported six city networks (SF, Miami, Atlanta,
Houston, Dallas, LA) from full-road-class Overpass extracts
(`scripts/import-city.sh`). Four consequential choices deviate from
prior guidance and are recorded here so they are read as decisions, not
oversights. Each was surfaced by external review (Claude Fable and/or
GPT-5.6-sol, review rounds archived under `docs/kb/raw/reviews/`
2026-07-24) and deliberately triaged.

## Decision

1. **No `--junctions.join` on city imports.** The KB's consolidation
   guidance (join split-carriageway clusters to avoid microjunction
   jams, `docs/kb/articles/integrations/osm-extraction.md`) conflicts
   with the stop-sign override (`scripts/osm-stop-nodes.py`): the
   override keys junctions by OSM node id, which joining rewrites to
   cluster ids. Stop/signal fidelity wins. Revisit only by teaching the
   override to resolve against joined-cluster membership.
2. **`priority_stop` retypes whole junctions** (PlainXML node override,
   second netconvert pass — netconvert 1.27.1 ignores OSM `highway=stop`
   nodes, eclipse-sumo issue #5244). netconvert then picks WHICH
   connections stop by road priority, so the OSM-signed approach is
   honored only in the common case (signs live on the minor approach).
   Per-approach fidelity requires connection-level overrides the node
   file cannot express; accepted as an approximation for visualization
   and demand modeling, not as a claim of legal-intersection fidelity.
3. **OSM relations are not imported** — including `type=restriction`
   (turn restrictions). The extracts query ways and tagged nodes only.
   This deviates from ADR-0009's intent to honor turn restrictions and
   is accepted for the ep-03 scope (infrastructure visualization +
   congestion demos); importing restrictions is future work and starts
   with fetching `relation["type"="restriction"]` in the extract.
4. **Directionless `highway=stop` nodes infer direction from the way's
   `oneway` tag where every way at the node agrees** (oneway=-1 runs
   against node order), and walk forward otherwise. OSM leaves the
   affected approach ambiguous without a `direction` tag; the resolver
   reports the count it defaulted rather than rejecting the data, since
   the tag is widely absent in US cities. Stops whose explicit direction
   fights the oneway flow are mapper errors and are dropped (an
   against-traffic arm would otherwise risk a spurious all-way
   classification).

## Consequences

- City networks carry stop control at arterial-and-up junctions
  (lean cuts) and full residential coverage (full cuts), with the
  approximations above.
- `contracts/network-format-v1.md` bootstrap recipe documents the
  two-pass workflow and points here.
- None of these choices change recording or wire SCHEMAS; they shape
  imported geometry and right-of-way classes, hence simulation state
  and scenario hashes.
- `provenance.imported` is pinned to the extract's OSM base date by the
  canned pipeline (reproducible ADR-0012 content hashes) rather than
  the import wall time; `contracts/network-format-v1.md` documents the
  field's widened meaning accordingly.
- The two-pass netconvert flow was verified on all six city imports to
  change ONLY the retyped junctions: signal programs (tlLogic) and
  priority approach classes survive pass 2 (`--sumo-net-file` reimport),
  and the boundary stays open with `--no-turnarounds` repeated.
