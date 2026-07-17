# OSM Extraction

> How OpenStreetMap becomes our lane-level road graph: a seven-pass Go importer bootstrapped and permanently oracle-checked by SUMO netconvert, with defaults-first lane inference and provenance flags throughout.

## Overview

Use Case 2 in [VISION.md](../../../VISION.md) — import a real-world region, establish a traffic baseline, evaluate upgrades on real geometry — depends entirely on this pipeline: turning OpenStreetMap data into the lane-level directed graph the engine simulates on. The problem is harder than it looks. OSM gives you three element types (nodes, ways, relations), geometry only on nodes, streets fragmented into attribute-split ways, a folksonomy of lane tags that is expressive but vanishingly sparse (`turn:lanes` covers ~0.7% of motor-road ways), and element IDs that churn on every split or merge. No schema authority exists; the wiki plus taginfo statistics are the de-facto spec.

The research surveyed every serious OSM→road-graph tool — osmnx, osm2streets, A/B Street, SUMO netconvert, osm2gmns, pt2matsim, CommonRoad, and the routing engines (OSRM, GraphHopper, Valhalla) — and found the field converges on one seven-pass pipeline shape (fetch → clip → re-aggregate → infer lanes → generate turns → apply restrictions → validate), while only three codebases do the whole job and none is reusable or Go-native. The lane-tagging layer a simulator wants is too sparse to be a foundation, so the importer is fundamentally a **defaults-driven inference engine with tag overrides and fail-soft filtering**.

The conclusion, ratified by [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md): build our own importer in Go (parsing is commodity via `paulmach/osm`, MIT), but ship a `netconvert → .net.xml → our format` bootstrap path first so real networks are usable immediately — and keep netconvert permanently in CI as a differential-testing oracle, with the oracle diff suite as the Go importer's acceptance gate.

## Key Components

| Component | Location | Purpose |
|---|---|---|
| Seven-pass pipeline | `raw/integration-osm-extraction/implementation.md` §9 | fetch → clip → re-aggregate → infer lanes → generate turns → apply restrictions → validate; pass order is the correctness argument |
| netconvert bootstrap path | [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md) | OSM → `.net.xml` → our network format; real networks before the Go importer matures |
| Go importer | [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md), `raw/integration-osm-extraction/competitors.md` §Go ecosystem | Own seven passes on `paulmach/osm`; algorithms ported from Apache-2.0 osm2streets/A-B-Street templates |
| Oracle diff suite | [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md) | netconvert kept permanently in CI; differential testing guards against the bootstrap becoming load-bearing |
| Dual acquisition modes | `raw/integration-osm-extraction/implementation.md` §6 | Overpass for interactive bbox fetch; Geofabrik PBF + `osmium extract` for reproducible imports |
| Defaults table (typemap) | `raw/integration-osm-extraction/implementation.md` §7 | Reviewed class→(lanes, speed, priority, width) artifact; the primary lane source for most of the map |
| Lane-spec IR | `raw/integration-osm-extraction/standards-and-patterns.md` §Patterns | osm2streets-style typed lane list (type, direction, width) decoupling tag parsing from graph compilation |
| Turn fabric | `raw/integration-osm-extraction/implementation.md` §5, §8 | Geometry+`turn:lanes`-guided lane-to-lane generation; restriction relations applied post-split, fail-soft |
| Two-tier identity + import report | [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md), `raw/integration-osm-extraction/implementation.md` §2 | Durable importer-assigned IDs, OSM IDs as provenance, `guessed` flags, structured per-import report |
| ODbL posture | `raw/integration-osm-extraction/implementation.md` §10 | Network file is ODbL Derivative Database; distribute the recipe (region, date, bbox, importer version) not the file |

## How It Works

### 1. Import strategy: bootstrap, build, oracle (ADR-0009)

Resolved at the 2026-07-17 design review: **netconvert bootstrap first, Go importer incrementally in parallel, netconvert kept permanently as the differential-testing oracle.** Rationale from the research:

- Only netconvert (EPL-2.0 C++ binary), A/B Street's importer (Apache-2.0 Rust, slowing), and osm2streets (Apache-2.0 Rust, near-dormant, no turn generation) do the full job. Nothing in Go goes beyond parsing.
- EPL-2.0 is file-level weak copyleft: shelling out to the binary and parsing its `.net.xml` output obligates our Go code nothing — netconvert is shell-out friendly by design.
- netconvert as a *permanent dependency* was rejected: heavy C++ install against local-first [ADR-0004](../../decisions/ADR-0004-local-first.md), and heuristics we cannot flag at element level (its guessed marks don't survive into our schema).
- The stated guard: the Go importer stays on the roadmap with the oracle diff suite as its acceptance gate, so the bootstrap never becomes load-bearing. The trade-off accepted: we re-implement algorithms netconvert hardened for 20 years (junction joining, TLS clustering, connection guessing), mitigated by the oracle and by smaller scope (no rail, no multimodal, no elevation initially).

### 2. Acquisition: two modes, one border discipline

1. **Interactive mode** — one Overpass QL highway query per bbox (`way["highway"](S,W,N,E); out body; >; out skel qt;`), inside the public fair-use envelope: **~10,000 queries/day, ~1 GB/day per IP**, 180 s / 512 MiB per-query defaults, HTTP 429/504 denials. Powers the "pick a region, see traffic" first-run UX. Not reproducible — responses have no frozen version.
2. **Reproducible mode** — cached Geofabrik regional PBF (Europe 32.3 GB, North America 17.9 GB, planet ~83.5 GiB, daily updates) + `osmium extract` per scenario (`complete_ways` is the default 2-pass strategy). The raw extract or its recipe (region, date, bbox) is stored alongside the scenario — scenario content hashing demands a pinned source.
3. **Border invariant** — `osmium extract` *"will never clip any OSM objects"*; Overpass cuts ways mid-segment. So: **always extract with a buffer, clip to the real boundary downstream**, and mark border roads as first-class map edges (future demand portals). netconvert's warning table ("Discarding way … only 1 node(s)", "Ignoring restriction relation …") is the symptom list when you don't.

### 3. Lane inference: defaults-first, tags as overrides, validation as a pass

Tag sparsity (taginfo, 2026-07-17, against ~177.9 M motor-road ways) is the founding fact: `lanes` on ~11%, `maxspeed` ~12%, `turn:lanes` **~0.7%**, `width:lanes` 0.006%, `placement` ~0.2%. **The defaults table *is* the network for most of the map; tags are the exception layer.**

- Own reviewed class→defaults table (lanes, speed, priority, width per highway class, per region), starting from netconvert's `osmNetconvert.typ.xml` values (motorway 2 lanes / 39.44 m/s / priority 14; residential 1 / 13.89 / 3; service 1 / 5.56 / 1) but **not trusting them** — netconvert's docs confess they "were set-up ad-hoc and are not yet verified."
- Explicit tags override defaults; the intermediate representation is an osm2streets-style typed lane list (type, direction, width, left-to-right) — the graph compiler downstream never sees raw tags.
- **No default lane width is codified anywhere** in OSM: osm2streets hard-codes sidewalk 1.5 m / shoulder 0.5 m / service road 2.0 m; SUMO's lane-width default is 3.2 m. Our table must own this explicitly.
- Cross-validation is a pass, not an assumption: real data carries `lanes=4` next to a 5-value `turn:lanes` pipe list (OsmAnd#5221). Conflicts degrade to **warnings + default fallback, never import failures**.

### 4. Turn fabric: generate from geometry, filter by relations, never orphan a lane

- Generate candidate lane-to-lane connections from geometry + junction topology using A/B Street's lane-type pairing (straight = Cartesian product of driving lanes; side-lane-originated turns), guided by `turn:lanes` where present — treating partial markings as *unknown*, not through-lanes (netconvert's documented trap; `--osm.turn-lanes` defaults off for this reason).
- Parse `type=restriction` relations — **2.274 M of them, 15.6% of all OSM relations** — with via-node *and* via-way support (the maturity divider: only netconvert, A/B Street, OSRM, GraphHopper, Valhalla handle via-way), a value whitelist (no prefix matching), and `except=`/mode-scoped/conditional captured.
- Apply restrictions **after way splitting, to the correct segment** (A/B Street's `split_ways.rs` discipline) — this is why pass order is load-bearing.
- Enforce the orphan-lane invariant fail-softly: *"If the filter makes an incoming lane lose all of its turns, then ignore that tag"* (A/B Street). The alternative's cost is documented: broken turn conflicts → simulation gridlock.

### 5. Junction typing: three sources + mandatory consolidation

Junction type (`allway_stop`, `stop_minor`, `priority`, `signalized` + RTOR flag, `roundabout`, `uncontrolled`, …) compiles from explicit control nodes (`highway=stop` + `stop=all`, `give_way`, `traffic_signals`), junction tags (`junction=roundabout/circular`, `mini_roundabout`), and class-based priority inference. The control furniture is abundant — 2.44 M stop nodes, 1.99 M traffic-signal nodes, 1.63 M give-way nodes, 1.01 M roundabout ways — unlike the lane tags. Typing is **preceded by cluster consolidation** (osmnx `consolidate_intersections` per-node buffer, default tolerance 10; netconvert `--junctions.join-dist` 10 m, `--tls.guess-signals` 25 m). Unjoined clusters have measured sim consequences: "low throughput, jams and even deadlocks." Signals arrive **presence-only** — OSM carries no timing programs — so guessed fixed-time plans (netconvert `GS_` precedent) get the `guessed` flag for the signal-control layer to refine.

### 6. Identity, provenance, licensing (ADR-0009)

- **Two-tier identity**: the importer assigns our durable element IDs at import time; OSM way/node/relation IDs are stored as provenance only (netconvert's `origID` / `5677#n` / `cluster_1_2` pattern). Scenario overlays, metric bindings, and user edits never key on OSM IDs.
- **Flags on every element**: `from-tag` vs `from-default` vs `guessed` (consolidated cluster, guessed signal, fabricated accel lane, fail-soft override), plus a structured import report (discarded data, applied fallbacks, guesses) emitted as a scenario artifact — the audit trail the civic-advocacy use case needs.
- **ODbL posture**: compiled network files are ODbL Derivative Databases (attribution + share-alike on that file); demand, control, and metric layers remain ours (Collective Database layering); sim outputs/videos are Produced Works. Prefer distributing the **recipe** (region, extract date, bbox, importer version) over the file — uniquely available because the network is a re-derivable compiled artifact, and it doubles as the reproducibility story. Overture does not escape this (transportation is ODbL too).
- **Delta-patch network variants**: scenario variants (add a lane, convert a stop to a roundabout) are expressed as patches over a base network, not full copies — keeping "upgrade variants" first-class per VISION without duplicating the ODbL layer.

### 7. The gap this fills

A documented, lane-level OSM→sim-graph compiler as a reusable component does not exist. The survey's positioning:

| Dimension | Centerline tools (osmnx, osm2gmns, pt2matsim) | Lane compilers (netconvert, A/B Street, osm2streets) | Routing engines (OSRM, GraphHopper, Valhalla) | Ours |
|---|---|---|---|---|
| Lane structure | count attribute only | typed lanes | none | typed lanes + provenance flags |
| Turn generation | none / link-level | yes (osm2streets: planned only) | turn costs, not lane graphs | yes, A/B-Street-style pairing |
| Restrictions | out of scope / raw parse | yes, fail-soft (A/B Street) | most hardened parsers (via-way, conditional) | via-node + via-way, fail-soft |
| Adoptability | wrong tier | Rust-bound blobs or heavy C++ install | parsers locked in routing pipelines | Go, ours; netconvert as oracle |

Nobody publishes the middle artifact — a typed lane list + restriction-resolved connection graph with provenance and guess flags — as a documented interchange, and the lane-inference defaults problem is confessed but unresearched (netconvert admits its typemap is unverified; no surveyed project calibrates defaults against observed geometry). A Go-native, flag-everything importer with a calibrated defaults table and a published import report is writing near the frontier; the differential-testing harness against netconvert is itself an unpublished artifact.

## Gotchas

- **OSM IDs are database rows, not identities**: JOSM keeps the way ID on the *longest* segment after a split; merges end one object's history; the wiki's Permanent ID page is a still-unimplemented proposal. Anything keyed externally on OSM IDs breaks silently.
- **`lanes=*` is a floor, not a count**: "widely misused to mean the lanes in each direction" (it's the *sum* on two-way roads), and mappers often tag through-lanes only — "data consumers can mostly treat the lanes tag as a minimum."
- **Partial `turn:lanes` markings are not through-lanes**: netconvert's own documented misread — unmarked lanes in a partial pipe-list must be treated as *unknown*.
- **Restrictions applied before way splitting attach to the wrong segment**: A/B Street's `split_ways.rs` exists precisely to fix this; pass order is the correctness argument.
- **Restriction values need whitelists, not prefix matching**: `no_entry`/`no_exit` and "no turn on red" variants break naive `no_*` matching; OSRM excludes `*_on_red`. Bonus trap: `only_left_turn` wrongly prohibits legal U-turns.
- **One OSM node ≠ one junction, in both directions**: traffic signals have two live placement conventions ("no well established convention"); dual carriageways split every control node per approach. Consolidation is mandatory — and can over-merge (netconvert warns about its own false positives), so flag, don't hide.
- **Nothing clips at the border for you**: osmium never clips; Overpass cuts ways mid-segment; restriction relations dangle. Buffer-then-clip with map-edge marking is the only correct posture.
- **Counts say how many, never which**: `lanes:bus=1` carries no position; only the `bus:lanes=` pipe-list form does.
- **Indicated ≠ legal**: `turn:lanes` records road markings; restriction relations record law. Both must be imported and reconciled — they disagree in real data.
- **Hard-failing on bad tags is an anti-pattern**: a wrong restriction or pipe-count mismatch degrades to warning + fallback. The invariant "every lane leads somewhere" outranks any single tag.
- **No global rules where OSM diverges regionally**: three maxspeed zone notations, US-vs-international `motorway_link` extent, two signal conventions — these need per-region parameters.

## Open Questions

- **Left-hand traffic**: no reliable per-way OSM driving-side tag; netconvert's global `--lefthand` vs per-edge override. Must be decided before lane-direction logic is written; no codified country table found in primary sources.
- **`restriction:conditional` (18.6 k relations)**: ignore-with-flag in v1, or carry into scenario time slicing? Ties to the scenario format's time model.
- **Demand injection at map edges**: are border entry/exit portals a network-file concept (typed `MapEdge` junction) or a scenario demand concern? Touches the road-graph model and scenario format both.
- **`type=connectivity` relations** (exact lane-to-lane mapping, rare in the wild): support now or later?
- **Overture GERS adoption timing**: solves ID stability, not lanes — the `lanes` property was removed 2024-11 as never-populated and is still absent. Re-evaluate when its lanes redesign lands.
- **Defaults-table calibration**: can the trajectory-dataset drone corpora (levelX/highD/NGSIM geometry) calibrate per-class lane widths and counts? Real future work netconvert never did.
- **Re-import/edit carry-over**: when a region is re-imported from fresher OSM, how do overlays and delta patches remap? A/B Street's unsolved durable-edits problem; mitigated by edits living in overlays, deferrable to geometry-matching or a GERS-based remap.

## Related

- [Road Graph Model](../architecture/road-graph-model.md) — the target schema this pipeline fills; reserves the provenance/`guessed`-flag slots and owns the anti-pattern catalog that becomes our test list.
- [Scenario Format](../concepts/scenario-format.md) — networks ship as scenario parts keyed on our durable IDs; content hashing needs the pinned-extract recipe; variants ride as delta patches.
- [Simulator Landscape](../business-domains/simulator-landscape.md) — the SUMO/netconvert posture (read ideas, shell out, never depend) and the license-ADR context for ODbL layering.
- [Signal Control](../business-domains/signal-control.md) — OSM delivers signal *presence* + RTOR only; timing programs arrive guessed and flagged for this layer to refine.
- [Trajectory Datasets & Overhead Analysis](../business-domains/trajectory-datasets.md) — candidate calibration source for the defaults table and import-fixture validation geometry.
- [MapLibre Realtime Viz](../integrations/maplibre-realtime.md) — renders the imported geometry (osm2streets-style lane polygons → GeoJSON) and carries OSM basemap attribution duties.

---
*Raw research: [raw/integration-osm-extraction](../../raw/integration-osm-extraction/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
