# Prior Art Survey: OSM Extraction

> Source: web research | Researched: 2026-07-17
> "Competitors" here = tools that turn OSM into something a simulator can drive
> on — acquisition tools, graph builders, lane-level compilers, and the routing
> engines whose tag parsers set the maturity bar — plus one emerging alternative
> *data source* (Overture). For each: what it does, what it preserves/loses, and
> what it means for our Go engine's "bbox → lane graph" pipeline.

## Acquisition layer

### Overpass API — the interactive extractor
- Read-only OSM query service; QL queries like
  `way["highway"](S,W,N,E); out body; >; out skel qt;` (bbox order
  south,west,north,east); output OSM XML / JSON / CSV; `out geom;` embeds way
  coordinates; data lags the main DB by minutes
  ([OSM Wiki](https://wiki.openstreetmap.org/wiki/Overpass_API)).
- Public main instance fair-use envelope: **~10 k queries/day, ~1 GB/day per
  IP**, 180 s / 512 MiB per-query defaults, HTTP 429/504 denials; heavier use
  should self-host (AGPL-3.0) ([Overpass Commons](https://dev.overpass-api.de/overpass-doc/en/preface/commons.html)).
- **vs traffic-sim (us):** the right fetch for the VISION first-run UX ("pick a
  region, see traffic in minutes") — one interactive bbox query is far inside
  the envelope. Not a repeatable/reproducible source: responses have no frozen
  version, so scenario reproducibility needs the PBF path below or a stored raw
  extract. Rate limits rule it out for batch/CI imports.

### Geofabrik + osmium — the reproducible extractor
- Geofabrik: daily regional `.osm.pbf` files (Europe 32.3 GB, North America
  17.9 GB; planet ~83.5 GiB Nov 2025) ([Geofabrik](https://download.geofabrik.de/),
  [planet mirror](https://ftp.nluug.nl/maps/planet.openstreetmap.org/pbf/?C=S&O=A)).
- `osmium extract` (GPL-3.0+ CLI): bbox/polygon extraction with
  `simple`/`complete_ways`(default)/`smart` strategies; **never clips** —
  border ways/relations stay reference-incomplete
  ([manpage](https://manpages.debian.org/unstable/osmium-tool/osmium-extract.1.en.html)).
- **vs us:** the repeatable/CI acquisition mode: download the region once,
  cache, `osmium extract -b` per scenario with a buffer. GPL-3.0 is a CLI
  concern only (we invoke the binary; nothing links). osmconvert/osmfilter are
  the legacy alternative (osmfilter AGPL-3.0; osmconvert license unverified).

## Graph builders (centerline tier)

### osmnx — centerline graph in three lines of Python
- `graph_from_bbox` (Overpass) → `networkx.MultiDiGraph`: nodes carry x/y,
  edges carry `highway, lanes, maxspeed, oneway, length…`; two-way ways become
  reciprocal edge pairs ([User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html)).
- `simplify_graph` re-aggregates attribute-split ways (geometry preserved in a
  `geometry` attribute; merged edges get list-valued attributes);
  `consolidate_intersections(tolerance)` merges divided-road node clusters;
  `add_edge_speeds` imputes per-class mean speeds
  ([User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html),
  [Boeing 2025, arXiv:2407.00258](https://arxiv.org/pdf/2407.00258v1)).
- Tag survival = explicit allowlist (`useful_tags_way`); **`turn:lanes`,
  per-direction lane tags, and all relations are dropped**; directional tags
  land ambiguously on both reciprocal edges ([settings.py](https://github.com/gboeing/osmnx/blob/main/osmnx/settings.py),
  [issue #784](https://github.com/gboeing/osmnx/issues/784)).
  Turn restrictions declared out of scope ([issue #22](https://github.com/gboeing/osmnx/issues/22)).
- MIT, 5.8 k★, very active ([repo](https://github.com/gboeing/osmnx)).
- **vs us:** proof of how little a routing/analysis graph needs — and therefore
  a floor, not a reference, for a sim importer: no lane structure, no
  restrictions, no junction typing. Its `simplify_graph`/`consolidate_intersections`
  algorithms and their documented options (`edge_attrs_differ`, per-node buffer
  tolerance) are worth porting as passes; the Python/networkx in-memory model
  and Overpass-only fetch don't fit a Go, local-first engine.

### osm2gmns — planning-network CSVs with movements
- OSM → GMNS CSV tables (node/link; optional movement/segment): intersection
  consolidation, directed links per bidirectional way, inferred lanes/speed/
  capacity, **movement generation** at link granularity
  ([docs](https://osm2gmns.readthedocs.io/), [quick-start](https://github.com/jiawlu/OSM2GMNS/blob/master/docs/source/quick-start.rst)).
- Lanes are a *count* on the link, not objects; movements are link-to-link, not
  lane-to-lane; restriction-relation parsing unverified. GPL-3.0, 113★, low
  recent activity (last push 2025-05)
  ([repo](https://github.com/jiawlu/OSM2GMNS)).
- **vs us:** one tier above osmnx (movements!) but still not lane-level, and
  GPL-3.0 makes even algorithmic porting a read-ideas-only exercise. Its
  movement.csv is a possible future demand/routing interchange, per
  [[concept-scenario-format]].

### pt2matsim — OSM → MATSim links, restrictions as attributes
- `Osm2MultimodalNetwork`; `parseTurnRestrictions` (default true) writes
  `disallowedNextLinks` onto the first link
  ([OsmConverterConfigGroup.java](https://github.com/matsim-org/pt2matsim/blob/master/src/main/java/org/matsim/pt2matsim/config/OsmConverterConfigGroup.java)).
- GPL-2.0, 62★, active ([repo](https://github.com/matsim-org/pt2matsim)).
- **vs us:** demonstrates the link-level floor for restriction handling (a flat
  disallowed-pairs attribute — the MATSim queue model needs nothing more). Not
  a lane-graph source.

## Lane-level compilers (the real reference class)

### osm2streets — the lane-spec schema we want, minus the turns
- Roads = thickened line-strings + ordered typed lane list (Driving, Parking,
  Sidewalk, Shoulder, Biking, Bus, SharedLeftTurn, Buffer, …) with direction
  and width; intersections = typed polygons; caller-ordered transformation
  passes (collapse degenerate intersections, merge dual carriageways, snap
  cycleways); boundary roads marked `MapEdge`
  ([README](https://github.com/a-b-street/osm2streets),
  [how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)).
- GeoJSON output of lane/intersection polygons; JS/Python bindings.
- **Gaps**: no turning movements yet ("Planned" — [README](https://github.com/a-b-street/osm2streets));
  intersection geometry has known failures ([issue #243](https://github.com/a-b-street/osm2streets/issues));
  "API isn't stable yet"; activity near-dormant (145★, last push 2025-10;
  "Revival ideas" issue #235).
- Apache-2.0 ([repo](https://github.com/a-b-street/osm2streets)).
- **vs us:** the closest schema cousin to the [[arch-road-graph-model]] target
  and the best-documented tag→lane-list inference logic in open source
  (inherited from the archived osm2lanes experiment). But it produces
  *geometry-first* streets without a lane-connectivity graph or junction
  control semantics — the exact half we need most. Apache-2.0 Rust: we can read
  and port freely; we cannot adopt it as a dependency without a Rust toolchain
  step and a dormant upstream.

### A/B Street importer (convert_osm + map_model) — the complete pipeline, Rust-bound
- The full seven-pass pipeline (§9 of implementation): osmium clip → way
  splitting with restriction assignment to correct segments → lane-pairwise
  turn generation → restriction/per-lane filtering with the fail-soft
  "never orphan a lane" fallback → stop-sign/signal inference → SCC validation
  ([importing docs](https://a-b-street.github.io/docs/tech/map/importing/index.html),
  [convert_osm](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html),
  [rest](https://a-b-street.github.io/docs/tech/map/importing/rest.html)).
- Output is a serialized Rust blob tied to the sim; activity slowing (8.1 k★,
  last push 2025-09) ([repo](https://github.com/a-b-street/abstreet)).
- Apache-2.0.
- **vs us:** algorithmically the best template for our passes 5–6 (turn
  generation from lane-type pairing; fail-soft restriction application), and the
  retrospective's failure catalog (broken turn conflicts → gridlock;
  turn-lane tags "often flat-out wrong") is our design constraint list
  ([[arch-road-graph-model]] has it in full). Not adoptable as a component:
  Rust crate graph, non-portable output, waning maintenance.

### SUMO netconvert — the battle-tested compiler you can shell out to
- Typemap-driven class→defaults (lanes/speed/priority/permissions per highway
  class) with per-way tag overrides; turn:lanes-guided connection guessing
  (`--osm.turn-lanes`, default off); restriction relations loaded to influence
  connections; junction joining, TLS guessing/joining, ramp guessing; explicit
  OSM→SUMO ID mapping with provenance params; documented warnings taxonomy
  ([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
  [netconvert options](https://sumo.dlr.de/docs/netconvert.html)).
- Output `.net.xml` is a complete lane-level sim graph (typed junctions,
  per-lane geometry, lane-to-lane connections, compiled right-of-way) — the
  [[arch-road-graph-model]] reference schema.
- Documented weaknesses: ad-hoc typemap values ("set-up ad-hoc and are not yet
  verified"), junction-join false positives, partial-turn-marking misreads,
  border-discard warnings; the lane-2-lane algorithm is code, not spec
  ([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
  [[arch-road-graph-model]] §9).
- EPL-2.0, 4.1 k★, very active; C++ binary intended for shell-out
  ([eclipse-sumo/sumo](https://github.com/eclipse-sumo/sumo)).
- **vs us:** the only option that delivers *everything* today. As a
  **bootstrap preprocessor** (OSM → .net.xml → our network format) it buys real
  networks on day one with zero license friction on our code (EPL-2.0 is
  file-level; invoking the binary and parsing its output obligates nothing) —
  consistent with the read-ideas-not-code SUMO posture in
  [[domain-simulator-landscape]]. As a permanent dependency it costs: a heavy
  C++ install in our local-first/docker story, heuristics we can't flag or
  patch at element level (its "guessed" marks don't survive into a schema we
  own), and an XML artifact model we've already decided not to adopt.

### CommonRoad Scenario Designer (osm2cr) — lanelets with a repair GUI
- OSM → CommonRoad lanelet maps, CLI + GUI; docs admit lane courses are
  *estimated* and "it is advisable to edit the scenarios by hand"
  ([docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-scenario-designer/)).
- GPL-3.0, 86★, beta ([PyPI](https://pypi.org/project/commonroad-scenario-designer/)).
- **vs us:** niche value — its GUI-repair workflow is the honest acknowledgment
  that import quality needs a human-in-the-loop escape hatch; relevant only if
  we ever target the CommonRoad benchmark ecosystem ([[arch-road-graph-model]]
  interchange discussion).

## Routing engines — the restriction-parsing reference shelf

- **OSRM** (C++, BSD-2-Clause, 7.9 k★): Lua profiles classify ways/speeds;
  C++ `RestrictionParser` handles via-node and via-way, and parses
  `no_entry`/`no_exit` as multi-from/multi-to restrictions; whitelists
  restriction values (excludes `*_on_red`)
  ([profiles.md](https://github.com/Project-OSRM/osrm-backend/blob/master/docs/profiles.md),
  [restriction_parser.cpp](https://github.com/Project-OSRM/osrm-backend/blob/master/src/extractor/restriction_parser.cpp)).
- **GraphHopper** (Java, Apache-2.0, 6.6 k★): via-node + via-way conversion
  (`convertForViaWays`/`convertForViaNode`), conditional restrictions
  (`restriction:bus=`), `except=`, duplicate-member via-way relations ignored
  ([turn-restrictions.md](https://github.com/graphhopper/graphhopper/blob/master/docs/core/turn-restrictions.md),
  [OSMRestrictionConverter.java](https://github.com/graphhopper/graphhopper/blob/master/core/src/main/java/com/graphhopper/reader/osm/OSMRestrictionConverter.java)).
- **Valhalla** (C++, MIT, 5.9 k★): Lua tag transforms into fixed structs;
  time-based `restriction:conditional` parsing in graph.lua
  ([tag-parsing doc](https://valhalla.github.io/valhalla/contributing/architecture/mjolnir/tag-parsing/),
  [graph.lua](https://github.com/valhalla/valhalla/blob/master/lua/graph.lua)).
- **vs us:** none emit lane-level graphs — their parsed restrictions become
  routing turn costs. But they are the most production-hardened *tag
  interpreters* available, all permissively licensed, and their source files
  are the executable specification for §5/§8's pitfall list. When our parser
  disagrees with the wiki, check what GraphHopper does.

## Go ecosystem (the build-our-own substrate)

- **paulmach/osm** (MIT, 466★, active): streaming OSM XML / Overpass JSON /
  `.osm.pbf` readers, `osmapi` client, `annotate` (lon/lat onto members),
  replication support ([repo](https://github.com/paulmach/osm)).
- **Nothing further exists**: no Go library does way re-aggregation, lane
  inference, turn generation, or restriction handling (negative search result,
  not proof of absence).
- **vs us:** parsing is solved commodity; everything above the parse layer is
  net-new Go code, with osm2streets/A-B-Street/netconvert/GraphHopper as the
  algorithmic references. Given ADR-0001 (engine in Go) this is the natural
  substrate for an own importer.

## Alternative data source: Overture Maps

- Linux-Foundation foundation (Meta, Microsoft, TomTom, Esri…) publishing
  monthly GeoParquet releases; transportation theme GA Dec 2024; **GERS**
  stable IDs GA June 2025 ("the vast majority of roads are maintaining stable
  IDs") ([Transportation to GA](https://docs.overturemaps.org/blog/2024/12/18/transportation-to-ga/),
  [What is GERS?](https://docs.overturemaps.org/gers/)).
- Typed transportation schema: segments with explicit `connectors`,
  `access_restrictions`, `speed_limits`, and **`prohibited_transitions`**
  (turn restrictions as schema, not relations)
  ([segment schema](https://docs.overturemaps.org/schema/reference/transportation/segment/)).
- **Two catches**: the `lanes` property was *removed* in Nov 2024 because it
  had never been populated — "we removed lanes … to eliminate a significant
  trust violation" — and it has not returned as of schema `main` checked
  2026-07-17; and transportation is **ODbL** (OSM-derived) — the permissive
  CDLA license applies only to non-OSM themes like Places
  ([2024-11-13 release notes](https://docs.overturemaps.org/blog/2024-11-13.0/),
  [Transportation to GA](https://docs.overturemaps.org/blog/2024/12/18/transportation-to-ga/),
  [2024-03-12 alpha notes](https://overturemaps.org/overture-2024-03-12-alpha-0-release-notes/)).
- **vs us:** solves ID stability (GERS + changelogs + bridge files) and
  schema hygiene, but not the lane-attribute problem — which is our hardest
  problem — and not licensing. Watch it; don't build on it yet.

## Positioning Summary

| Tool | Scope | Lane-level | Turn restrictions | Lane-to-lane | License | Fit for us |
|---|---|---|---|---|---|---|
| Overpass API | fetch | — | — | — | AGPL-3.0 (service) | interactive fetch mode |
| osmium/Geofabrik | fetch/clip | — | — | — | GPL-3.0+ (CLI) | reproducible fetch mode |
| osmnx | centerline graph | no (count attr) | no (out of scope) | no | MIT | algorithm donor (simplify/consolidate) |
| osm2gmns | planning CSV | partial | unverified | link-level movements | GPL-3.0 | interchange idea only |
| pt2matsim | MATSim links | no | yes → attribute | no | GPL-2.0 | reference only |
| osm2streets | lane geometry | **yes (typed lanes)** | raw parse only | no (planned) | Apache-2.0 | schema + tag-parsing template |
| A/B Street importer | full pipeline | **yes** | **yes (fail-soft)** | **yes** | Apache-2.0 | algorithm template, not a component |
| SUMO netconvert | full compiler | **yes** | **yes** | **yes** | EPL-2.0 (binary) | bootstrap preprocessor + oracle |
| CommonRoad designer | lanelets | yes (estimated) | partial | partial | GPL-3.0 | niche |
| OSRM / GraphHopper / Valhalla | routing graphs | no | **yes (hardened)** | no | BSD-2 / Apache-2.0 / MIT | restriction-parser references |
| paulmach/osm | Go parser | — | — | — | MIT | own-importer substrate |
| Overture Maps | data source | not yet | yes (schema) | — | ODbL (transport) | future watch item |

Only three codebases deliver the whole "bbox → directed edges + typed lanes +
lane-to-lane connections + restrictions" job (netconvert, A/B Street, and —
minus turn movements — osm2streets). Two of three are Rust with slowing
maintenance; the third is a C++ binary. None is Go. The field leaves a clear
slot: a small, Go-native, provenance-flagging importer that treats netconvert
as its correctness oracle.
