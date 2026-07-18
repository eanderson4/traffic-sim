# Synthesis: OSM Extraction

> Researched: 2026-07-17 | Git HEAD: 6efd963 | Status: complete
> Feeds a future ADR on the OSM import pipeline (unnumbered) and the network
> part of the scenario format. This synthesis recommends; the ADR decides.

## Summary

The research question was how to turn OpenStreetMap data into the lane-level
road graph [[arch-road-graph-model]] specifies: what the OSM data model gives
you (fragmented ways, folksonomy lane tags, relation-based turn restrictions,
unstable IDs), what the tooling does with it (Overpass/osmium acquisition;
osmnx/osm2streets/A-B-Street/netconvert compilation), and what a "bbox → clean
lane graph" pipeline must own itself. The field converges on a seven-pass
pipeline (fetch → clip → re-aggregate → infer lanes → generate turns → apply
restrictions → validate) and splits sharply by ambition: centerline tools
(osmnx, osm2gmns, pt2matsim) drop exactly the data we need (relations,
directional lane tags), while only three codebases do the full job — SUMO
netconvert (EPL-2.0 C++ binary), A/B Street's importer (Apache-2.0 Rust,
slowing), and osm2streets (Apache-2.0 Rust, near-dormant, no turn generation).
Nothing in Go goes beyond parsing. The lane-tagging layer a simulator wants
(`turn:lanes` ~0.7% of motor roads) is too sparse to be a foundation, so the
importer is fundamentally a **defaults-driven inference engine with tag
overrides and fail-soft filtering**, with netconvert available as a
bootstrap preprocessor and permanent correctness oracle.

## Source Files

- [Mechanics: what OSM gives you and how the tools compile it](./implementation.md)
- [Prior art survey: acquisition, graph builders, compilers, routing engines](./competitors.md)
- [Standards (wiki folksonomy, ODbL, PBF, Overpass QL, Overture), patterns, anti-patterns](./standards-and-patterns.md)
- [Field notes: netconvert bootstrap in practice (M5)](./netconvert-bootstrap-notes.md) — post-research operational findings (node ordering, `--no-turnarounds` portals, funnel merges)

## Key Findings → Recommended Decisions (for the import-pipeline ADR)

### 1. Build our own importer in Go; use netconvert as bootstrap and oracle
**Choice:** A Go importer on `paulmach/osm` (MIT) for parsing, implementing the
seven passes itself, with osm2streets' lane-spec schema and A/B Street's
turn-generation/restriction algorithms as templates (both Apache-2.0 — free to
port). In parallel, ship a thin `netconvert → .net.xml → our network format`
bootstrap path so real networks are usable before the Go importer matures;
keep netconvert in CI as a differential-testing oracle afterward.
**Why:** Only netconvert, A/B Street, and (minus turns) osm2streets do the
whole job; the two Rust options have waning maintenance and non-portable
outputs, and neither can carry *our* schema's provenance/guessed flags
([[arch-road-graph-model]] #6) or junction-type compile. EPL-2.0 netconvert is
shell-out friendly (invoking the binary and parsing its output obligates our
code nothing), battle-tested, and documents its own failure modes — ideal
bootstrap/oracle, poor permanent dependency (heavy C++ install against
local-first/docker ADR-0004; heuristics we can't flag at element level). ADR-0001
already commits the engine to Go, and paulmach/osm makes parsing commodity.
**Trade-off:** We re-implement algorithms netconvert has hardened for 20 years
(junction joining, TLS clustering, connection guessing) — mitigated by the
oracle path and by our smaller scope (no rail, no multimodal, no elevation
initially). Two import paths must be kept output-compatible or the bootstrap
becomes a fork.
**Field context:** [implementation §7](./implementation.md),
[competitors positioning](./competitors.md).

### 2. Two acquisition modes: Overpass for interactive, PBF+osmium for reproducible
**Choice:** (a) Interactive mode: one Overpass QL highway query per bbox
(`way["highway"](S,W,N,E); out body; >; out skel qt;`), inside the public
fair-use envelope (~10 k queries/day, ~1 GB/day), for the "pick a region, see
traffic" first-run UX. (b) Reproducible mode: cached Geofabrik regional PBF +
`osmium extract` per scenario, with the raw extract (or its recipe: region,
date, bbox) stored alongside the scenario. **Always extract with a buffer and
clip to the real boundary downstream**, marking border roads as map edges.
**Why:** This is the exact division of labor the tools reveal: osmnx/osm2streets/
A-B-Street-UIs use Overpass for interactivity; A/B Street/moveet use
PBF+osmium for repeatable builds. osmium "will never clip any OSM objects" and
Overpass cuts ways mid-segment — the border problem is unavoidable upstream,
so the importer must own buffer-then-clip; netconvert's warning table
("Discarding way … only 1 node(s)", "Ignoring restriction relation …") is the
symptom list when you don't. Scenario reproducibility ([[concept-scenario-format]]
content hashing) demands a pinned data source, which live Overpass is not.
**Trade-off:** Two fetch paths to maintain; Geofabrik region files are large
(state-to-continent scale) — acceptable disk cost, cached. Map edges become a
first-class graph concept (demand portals) that [[arch-road-graph-model]] must
accommodate.
**Field context:** [implementation §6](./implementation.md).

### 3. Lane inference is defaults-first: a reviewed typemap + tag overrides + validation
**Choice:** Our own class→defaults table (lanes, speed, priority, width per
highway class, per region) as a reviewed artifact — starting from netconvert's
`osmNetconvert.typ.xml` values but not trusting them (its docs: "set-up
ad-hoc and are not yet verified"). Explicit tags override defaults; the
intermediate representation is an osm2streets-style typed lane list (type,
direction, width per lane, left-to-right). Cross-validate tags (lanes count vs
pipe count, width consistency); conflicts degrade to warnings + default
fallback, never import failures.
**Why:** taginfo (2026-07-17) puts `turn:lanes` on ~0.7% of motor-road ways,
`lanes` on ~11%, `width:lanes` on 0.006% — the defaults table *is* the network
for most of the map, and tags are the exception layer. Both surviving
implementations are shaped exactly so: netconvert's typemap+override, and
osm2streets' hard-coded widths (sidewalk 1.5 m etc.). Real tags conflict with
each other in the wild (`lanes=4` vs 5-value turn:lanes — OsmAnd#5221), so
validation is a pass, not an assumption.
**Trade-off:** Our defaults table becomes a value-laden artifact that
determines sim credibility for un-mapped regions (most of the world) — it
needs per-region variants and calibration against [[domain-trajectory-datasets]]
geometry, which is real future work netconvert never did.
**Field context:** [implementation §3, §7](./implementation.md),
[standards-and-patterns patterns](./standards-and-patterns.md).

### 4. Turn fabric: geometry+turn:lanes-guided generation, relations applied post-split, fail-soft
**Choice:** Generate candidate lane-to-lane connections from geometry +
junction topology (A/B Street's lane-type pairing: straight = Cartesian
product, side-lane-originated turns), guided by `turn:lanes` where present
(whitelist the vocabulary; treat partial markings as *unknown*, not
through-lanes — netconvert's documented trap). Parse `type=restriction`
relations (via-node *and* via-way; value whitelist, no prefix matching;
`except=`/mode-scoped/conditional captured), and apply them **after way
splitting, to the correct segment** (A/B Street's split_ways.rs discipline).
Enforce the orphan-lane invariant fail-softly: a filter that would strip a
lane's last movement is ignored with a warning.
**Why:** turn:lanes records *indicated* movements, relations record *legal* ones
— both are needed and they disagree in real data. Via-way relations are the
maturity divider (only netconvert, A/B Street, OSRM, GraphHopper, Valhalla
handle them); the routing engines' permissively licensed parsers are the
executable spec for the pitfall list. A/B Street's "if the filter makes an
incoming lane lose all of its turns, then ignore that tag" is the field's
load-bearing fallback; their retrospective shows the cost of the alternative
(broken turn conflicts → simulation gridlock).
**Trade-off:** via-way resolution requires our re-aggregation to keep a
way-id→segment map through every split/join — more bookkeeping in the
pipeline's middle passes. Conditional restrictions (18.6 k) are probably
v1-flagged-not-applied (open question).
**Field context:** [implementation §5, §8](./implementation.md).

### 5. Two-tier identity and provenance from day one
**Choice:** Importer assigns our durable element IDs at import time; OSM
way/node/relation IDs are stored as provenance only (netconvert's
`origID`/`5677#n`/`cluster_1_2` pattern). Every element also carries origin
flags: `from-tag` vs `from-default` vs `guessed` (consolidated cluster, guessed
signal, fabricated accel lane, fail-soft override). Scenario overlays, metric
bindings, and user edits never key on OSM IDs. The import emits a structured
report (discarded data, applied fallbacks, guesses) as a scenario artifact.
**Why:** OSM IDs churn on splits/merges (JOSM keeps the ID on the longest
segment; merges end histories; the OSM wiki's own Permanent ID page is a
still-unimplemented proposal) — keying anything external on them breaks
silently. The guessed-flag requirement is [[arch-road-graph-model]] #6's
reserved schema slot and the only defense against the documented
import-failure class (heuristics that silently produce jams); the report is
what makes imported networks auditable for the advocacy use case.
**Trade-off:** Re-importing an updated OSM extract cannot automatically carry
over user edits keyed on our old IDs — the durable-edits problem A/B Street
never solved either; mitigated by edits living in scenario overlays
([[concept-scenario-format]]) rather than in the network file, and deferrable
to a future geometry-matching or Overture-GERS-based remap.
**Field context:** [implementation §2](./implementation.md),
[standards-and-patterns patterns](./standards-and-patterns.md).

### 6. Junction typing compiles three tag sources, with consolidation as a mandatory pass
**Choice:** Junction type (matching the [[arch-road-graph-model]] enum:
`allway_stop`, `stop_minor`, `priority`, `signalized` + RTOR flag,
`roundabout`, `uncontrolled`, …) is compiled from: explicit control nodes
(`highway=stop`+`stop=all`, `give_way`, `traffic_signals`), junction tags
(`junction=roundabout/circular`, `mini_roundabout`), and class-based priority
inference — preceded by cluster consolidation (osmnx-style per-node buffer /
netconvert `--junctions.join` semantics) so dual-carriageway crossings and
per-approach signal nodes merge into one junction before typing. Signals
arrive *unsignalized-timing*: OSM carries presence, not programs — guessed
fixed-time plans (A/B Street/netconvert `GS_` precedent) get the `guessed`
flag for [[domain-signal-control]] to refine.
**Why:** OSM has two live traffic-signal conventions ("no well established
convention") and splits control nodes per approach by design (stop/give_way
sit on approach ways, not conflict nodes) — "one OSM node = one junction" is
false in both directions. Unjoined clusters are the anti-pattern with measured
sim consequences ("low throughput, jams and even deadlocks").
**Trade-off:** Consolidation is heuristic and can over-merge (netconvert warns
about its own false positives) — hence flagging + report (#5) rather than
silent correctness. Roundabout vs circular vs signalized-roundabout edge cases
need test fixtures from real data.
**Field context:** [implementation §4](./implementation.md).

### 7. ODbL posture: network file is share-alike data; everything else is ours
**Choice (for the license ADR, jointly with [[domain-simulator-landscape]] #5):**
treat compiled network files as ODbL Derivative Databases — attribution on
every distributed network, ODbL on the network file itself — while demand,
control, metrics definitions, and code remain under our permissive license
(Collective Database layering), and sim outputs/videos are Produced Works
under our terms. Prefer distributing the *recipe* (region, extract date, bbox,
importer version) over the file where practical.
**Why:** This is the OSMF guidelines' direct logic ("intended for the
extraction of the original data ⇒ database"); the recipe-distribution posture
is uniquely available to us because the network is a compiled, re-derivable
artifact — and it doubles as the reproducibility story for scenario content
hashing. Overture does not escape this (transportation is ODbL too).
**Trade-off:** Published scenario packs containing real networks carry ODbL
files — fine for open data, but it must be an explicit, documented choice
(needs the owner's call in the license ADR; the guidelines "carry no formal
legal weight" and we are not getting legal advice from a wiki).
**Field context:** [implementation §10](./implementation.md).

## Compare/Contrast: Us vs the Field

| Dimension | osmnx | osm2streets | A/B Street | netconvert | routing engines (OSRM/GH/Valhalla) | us (proposed) |
|---|---|---|---|---|---|---|
| Fetch | Overpass only | Overpass/OSM XML | osmium+PBF | Overpass/PBF/osmconvert | PBF | **Overpass + PBF/osmium** |
| Graph tier | centerline | lane geometry | lane graph | lane graph | edge+turn costs | **lane graph ([[arch-road-graph-model]])** |
| Lane inference | none (count attr) | tags+hard-coded defaults | tags+defaults | typemap+tags | class profiles | **reviewed typemap + tags + validation** |
| Turn generation | none | planned | yes (lane pairing) | yes (guess+turn:lanes) | turn costs | **yes (A/B-Street-style)** |
| Restrictions | out of scope | raw parse only | yes, fail-soft | yes | **yes, hardened (via-way, conditional)** | **yes, via-node+via-way, fail-soft** |
| Junction typing | consolidation only | collapse transforms | inference | typed junctions+TLS guess | none | **typed compile + consolidation pass** |
| Provenance/guess flags | `osmid`/`osmid_original` | osm ids | osm ids | origID params, GS_ prefix | none | **first-class flags + import report** |
| Output format | networkx | GeoJSON/polygons | Rust blob | .net.xml | routing tiles | **our authoring YAML + compiled form** |
| License / adoptability | MIT (Python) | Apache-2.0 (Rust, dormant) | Apache-2.0 (Rust, slowing) | EPL-2.0 (C++ binary) | BSD/Apache/MIT (C++/Java) | **Go, ours; netconvert as oracle** |

## The Genuine Gap (again)

**A documented, lane-level OSM→sim-graph compiler as a reusable component does
not exist.** The two complete implementations are inseparable from their sims
(netconvert from SUMO's .net.xml semantics, A/B Street's importer from its Rust
map blob), the reusable-schema attempt (osm2streets) stalled before turn
generation, and the routing engines hide the best restriction parsers inside
routing-specific pipelines. Nobody publishes the middle artifact — a typed
lane list + restriction-resolved connection graph with provenance and guess
flags — as a documented interchange. Second: **the lane-inference defaults
problem is confessed but unresearched** — netconvert admits its typemap values
are ad-hoc and unverified, and no surveyed project calibrates class→lane/speed
defaults against observed geometry (the [[domain-trajectory-datasets]] drone
corpus could). A Go-native, flag-everything importer with a calibrated,
per-region defaults table and a published import report would again be writing
near the frontier — and the differential-testing harness against netconvert is
itself an unpublished artifact.

## Open Questions

- ~~**Import-strategy phasing (owner decision, ADR-worthy)**~~ **RESOLVED
  2026-07-17 review:** netconvert bootstrap first (`.net.xml` → our format;
  unblocks Use Case 2 real-network scenarios now), Go importer built
  incrementally in parallel, netconvert kept permanently as the
  differential-testing oracle. Guard against the bootstrap becoming
  load-bearing: the Go importer stays on the roadmap with the oracle diff
  suite as its acceptance gate. To be pinned in an import-strategy ADR.
- **Left-hand traffic:** no reliable per-way OSM tag; global import parameter
  (netconvert `--lefthand` style) vs per-edge override? Needs a decision before
  lane-direction logic is written; no codified country table found in primary
  sources.
- **`restriction:conditional` / time-varying restrictions (18.6 k relations):**
  ignore-with-flag in v1, or carry into scenario time slicing
  ([[concept-scenario-format]])?
- **Demand injection at map edges:** border roads become entry/exit portals —
  is that a network-file concept (typed `MapEdge` junction) or a scenario
  demand concern? Touches [[arch-road-graph-model]] and
  [[concept-scenario-format]].
- **`type=connectivity` relations** (exact lane-to-lane mapping, rare): support
  now or later?
- **Overture GERS adoption timing:** solves ID stability, not lanes (lanes
  property removed 2024-11, still absent); re-evaluate when its lanes redesign
  lands.
- **Defaults-table calibration:** can levelX/highD/NGSIM geometry calibrate
  per-class lane widths and counts? (Links [[domain-trajectory-datasets]].)
- **Re-import/edit carry-over:** when a scenario region is re-imported from
  fresher OSM, how do overlays remap? (A/B Street's unsolved durable-edits
  problem; defer with the overlay architecture or design for it now?)

## Connections to Other Topics

- **Decides into:** a future import-pipeline ADR (this research is its gate);
  feeds the license ADR recommended by [[domain-simulator-landscape]] (#7 here).
- **Depends on:** [[arch-road-graph-model]] (the target schema; its #6 reserves
  the provenance/guess-flag slots this pipeline fills; its anti-pattern catalog
  is our test list), [[concept-scenario-format]] (network part file, content
  hashing needs the pinned-extract recipe, overlay keying drives the two-tier
  ID design), ADR-0001 (Go engine ⇒ Go importer).
- **Constrains:** [[arch-road-graph-model]] (map-edge/border junction concept;
  turn-restriction semantics on connections), [[concept-scenario-format]]
  (import report as scenario artifact; ODbL layering of scenario packs),
  [[domain-signal-control]] (signal presence + RTOR from OSM; programs guessed
  and flagged — their schema must accept guessed plans), the license ADR
  (ODbL network files inside a permissively licensed project).
- **Relates to:** [[domain-simulator-landscape]] (netconvert/SUMO posture,
  EPL-2.0 handling, first-run UX bar), [[integration-maplibre-realtime]]
  (osm2streets-style lane polygons → GeoJSON rendering; OSM basemap
  attribution), [[domain-trajectory-datasets]] (defaults calibration targets;
  intersection footage for import-fixture validation),
  [[domain-congestion-metrics]] (metric bindings must use our stable IDs, not
  OSM IDs), [[domain-traffic-flow-models]] (`change:lanes` → lane-change
  constraint scope), [[arch-nats-backbone]] (nothing direct — network
  distribution is [[arch-road-graph-model]]'s concern).
