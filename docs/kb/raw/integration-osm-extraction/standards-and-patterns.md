# Standards & Patterns: OSM Extraction

> Source: standards/governance documents, tag-usage statistics + pattern
> identification | Researched: 2026-07-17

## Standards & Formal Specifications

### The OSM wiki + taginfo — a folksonomy with a measurement layer
- There is no schema authority: "any tags you like", conventions documented on
  wiki pages, approval via proposal process, and actual usage measurable via
  taginfo — "if there is more than one way to tag a given feature, it's
  probably best to use the most common approach"
  ([Elements](https://wiki.openstreetmap.org/wiki/Elements)).
- The pages that function as our *de-facto spec*:
  [Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes),
  [Key:turn](https://wiki.openstreetmap.org/wiki/Key:turn),
  [Key:oneway](https://wiki.openstreetmap.org/wiki/Key:oneway),
  [Key:placement](https://wiki.openstreetmap.org/wiki/Key:placement),
  [Key:change](https://wiki.openstreetmap.org/wiki/Key:change),
  [Lanes overview](https://wiki.openstreetmap.org/wiki/Lanes),
  [Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction),
  [Key:maxspeed](https://wiki.openstreetmap.org/wiki/Key:maxspeed) +
  [Default speed limits](https://wiki.openstreetmap.org/wiki/Default_speed_limits),
  [Key:highway](https://wiki.openstreetmap.org/wiki/Key:highway),
  [junction=roundabout](https://wiki.openstreetmap.org/wiki/Tag:junction%3Droundabout),
  [highway=traffic_signals](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dtraffic_signals),
  [highway=stop](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dstop),
  [highway=give_way](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dgive_way).
- **How our implementation relates:** we comply by *interpreting*, not by
  assuming validity — every spec page carries its own "common mistakes" section,
  and real data violates the spec regularly (lanes/pipe-count mismatches,
  `lanes=1.5`, malformed maxspeed values). The parser must be spec-driven in
  what it *accepts* and statistics-driven in what it *trusts* (see
  Anti-patterns). Where the wiki itself disagrees (three maxspeed zone
  notations; two traffic-signal conventions; US-vs-international `motorway_link`
  extent), the importer needs a per-region parameter, not a global rule.

### ODbL 1.0 — the license that constrains distribution
- OSM data since 2012-09-12; obligations = attribution + share-alike on
  Derivative Databases + produced-work machinery (ODbL §4.6 offer of the
  underlying database) ([OSMF Licence](https://osmfoundation.org/wiki/Licence),
  [Legal FAQ](https://wiki.osmfoundation.org/wiki/Licence/Licence_and_Legal_FAQ)).
- Board-endorsed guidelines (no formal legal weight): [Produced Work](https://osmfoundation.org/wiki/Licence/Community_Guidelines/Produced_Work_-_Guideline)
  ("intended for the extraction of the original data" ⇒ database),
  [Collective Database / Regional Cuts / Horizontal Layers](https://wiki.osmfoundation.org/wiki/Licence/Community_Guidelines)
  (share-alike touches only the OSM-derived layer), [Geocoding guideline](https://wiki.osmfoundation.org/wiki/Licence/Community_Guidelines/Geocoding_-_Guideline)
  (insubstantial extracts; 100-feature safe harbor in the Substantial
  guideline).
- **How we relate:** our compiled network file = Derivative Database (ODbL if
  distributed); scenario demand/control/metrics layers = independent collective
  members (our license); sim outputs = Produced Works (our license +
  attribution). The "distribute the recipe (bbox + OSM timestamp + importer
  version), not the database" posture is the cleanest fit — it also matches the
  provenance design in [[arch-road-graph-model]] §8. Final call belongs to the
  license ADR ([[domain-simulator-landscape]] recommendation #5).

### PBF — the exchange encoding
- Protobuf-based, ~half the size of gzipped XML, ~6× faster to read;
  fileblock-organized with string tables, delta-coded IDs/coords, 100-nanodegree
  granularity (~1 cm); optional `LocationsOnWays`; replication metadata in the
  header keeps extracts updatable ([PBF Format](https://wiki.openstreetmap.org/wiki/PBF_Format)).
- **How we relate:** the input encoding for the reproducible fetch path;
  paulmach/osm streams it natively ([repo](https://github.com/paulmach/osm)).
  OSM XML/JSON remains the Overpass path's encoding — both are parse-commodity.

### Overpass QL — the query dialect
- Statement language with output control (`out body/skel/geom`), recursion
  (`>;`), set operations, bbox in (S,W,N,E); server-side resource governance
  (slots, 180 s timeout, 512 MiB maxsize defaults)
  ([Overpass API](https://wiki.openstreetmap.org/wiki/Overpass_API),
  [Commons](https://dev.overpass-api.de/overpass-doc/en/preface/commons.html)).
- **How we relate:** we need exactly one query template (highway ways + member
  nodes, optionally `out geom;`) plus `--osm.all-attributes`-style tag breadth;
  the governance envelope defines the interactive-mode UX limits.

### Overture transportation schema + GERS (emerging)
- Typed segment/connector schema with `prohibited_transitions`; GERS stable IDs
  GA 2025-06; monthly releases with changelogs/bridge files
  ([segment schema](https://docs.overturemaps.org/schema/reference/transportation/segment/),
  [GERS](https://docs.overturemaps.org/gers/)).
- `lanes` property removed 2024-11 (never populated), not yet redesigned;
  transportation theme is ODbL ([2024-11-13 notes](https://docs.overturemaps.org/blog/2024-11-13.0/)).
- **How we relate:** not adoptable now (no lane attributes, same license); the
  stable-ID architecture is the design to crib if we ever maintain our own
  network↔source ID registry.

## Design Patterns Identified

### Pipeline of single-responsibility passes
Fetch → clip → re-aggregate → infer lanes → generate turns → apply restrictions
→ validate, each pass a pure-ish transformation with its own warnings. Every
surveyed pipeline has this shape (SUMO osmWebWizard/osmGet→netconvert; A/B
Street osmium→convert_osm→map_model; osm2streets' caller-ordered
`transformations`; moveet's download→extract→filter→export→validate)
([implementation §9](./implementation.md)).
Benefits: each pass is testable in isolation; failures are attributable; the
pass order *is* the correctness argument (restrictions after splitting,
consolidation before lane inference).

### Lane-spec intermediate representation (IR)
Decouple "OSM tags → typed lane list" from "lane list → sim graph".
osm2streets' LaneSpec (type, direction, width; enum Driving/Parking/Sidewalk/
Biking/Bus/SharedLeftTurn/Buffer/…) is the reference IR; osm2lanes existed as a
standalone experiment in exactly this decoupling ("transforms OpenStreetMap
tags to a specification of lanes on a street")
([osm2lanes](https://github.com/a-b-street/osm2lanes),
[osm2streets](https://github.com/a-b-street/osm2streets)).
The IR is where region defaults, width inference, and tag validation live; the
graph compiler downstream never sees raw tags.

### Defaults table + tag override (the typemap pattern)
Class → (lanes, speed, priority, permissions) lookup, overridden by explicit
per-way tags. netconvert's `osmNetconvert.typ.xml` is the reference instance
(motorway 2/39.44 m/s/p14 … service 1/5.56/1), with the docs' own confession
that values "were set-up ad-hoc and are not yet verified"
([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
[typ.xml](https://raw.githubusercontent.com/eclipse-sumo/sumo/main/data/typemap/osmNetconvert.typ.xml)).
osmnx independently reinvents it for speeds (`add_edge_speeds` = per-class mean
maxspeed) ([User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html)).
The taginfo sparsity data (turn:lanes on ~0.7% of motor roads) makes this table
the *primary* lane source, so ours must be a reviewed artifact with per-region
overrides — not netconvert's confessed guesses.

### Fail-soft semantic filtering (never orphan a lane)
When a tag/restriction would remove a lane's last legal movement, ignore the
tag and warn. A/B Street: "Some of these OSM tags are just completely wrong
sometimes. If the filter makes an incoming lane lose all of its turns, then
ignore that tag" ([rest doc](https://a-b-street.github.io/docs/tech/map/importing/rest.html)).
Generalizes to: contradictory lane counts (OsmAnd#5221), partial turn-markings
(netconvert's trap), border-cut relations. The invariant — every lane leads
somewhere — outranks any single tag (matches [[arch-road-graph-model]] §2's
completeness invariant).

### Provenance + guessed flags on every element
Carry source IDs (osm way/node/relation) and mark every heuristically produced
element (defaulted lane count, inferred priority, consolidated cluster,
guessed signal, fabricated accel lane). netconvert does the first half
(`origID` params, `5677#n` naming, `cluster_1_2`, `GS_` TLS prefix)
([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html));
[[arch-road-graph-model]] recommendation #6 reserves the schema slots for both
halves. This is the only defense against the documented import-failure class
(heuristics that "silently produce jams").

### Two-tier identity: own stable IDs, OSM IDs as provenance
OSM IDs churn on split/merge (JOSM keeps the ID on the longest segment;
merging ends one object's history) ([Permanent ID](https://wiki.openstreetmap.org/wiki/Permanent_ID),
[JOSM SplitWay](https://josm.openstreetmap.de/wiki/Help/Action/SplitWay)).
So: importer assigns durable IDs at import time; OSM IDs ride along for
re-derivation; scenario overlays and metric bindings ([[concept-scenario-format]])
never key on OSM IDs. Overture GERS is the field-scale version of the same
idea ([GERS docs](https://docs.overturemaps.org/gers/)).

### Junction consolidation as a named pass
Dual-carriageway crossings and split signal nodes must be merged before
control typing: osmnx `consolidate_intersections(tolerance)` (per-node buffer),
netconvert `--junctions.join` (10 m) + `--tls.join` (with false-positive
warnings), osm2streets `CollapseDegenerateIntersections` +
`MergeDualCarriageways`, A/B Street dog-leg collapsing
([osmnx](https://osmnx.readthedocs.io/en/stable/user-reference.html),
[SUMO](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
[osm2streets how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)).
The surveyed failure mode (unjoined clusters → jams/deadlocks) is in
[[arch-road-graph-model]] anti-pattern #2.

### Import report as a first-class artifact
netconvert's warnings table ("Ignoring restriction relation …", "Discarding
way … only 1 node(s)") and A/B Street's "manually tune the boundary polygon"
guidance show the shape: a structured, per-import list of discarded data,
applied fallbacks, and guesses
([SUMO §Warnings](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
[convert_osm](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html)).
For the advocacy use case (trustworthy metrics on real geometry) the report is
the audit trail from OSM tags to scenario.

## Anti-patterns (documented failures)

1. **Trusting OSM IDs as durable references** — splits/merges churn them; any
   external keying (overlays, edits) breaks silently
   ([Permanent ID](https://wiki.openstreetmap.org/wiki/Permanent_ID)).
2. **Treating `lanes=*` as exact** — the wiki itself calls it widely misused
   (per-direction vs total) and a *minimum* in common mapping practice
   ([Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)).
3. **Interpreting partially marked turn:lanes as through-lanes** — netconvert's
   own documented misread ([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
4. **Applying restrictions before way splitting** — they attach to the wrong
   segment; A/B Street's split_ways.rs exists to fix exactly this
   ([convert_osm](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html)).
5. **Prefix-matching restriction values** — `no_entry`/`no_exit` and
   "no turn on red" variants demand whitelists; OSRM excludes `*_on_red`
   ([Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction)).
6. **Assuming one OSM node = one junction** — traffic signals have two live
   placement conventions; dual carriageways split every control node
   ([highway=traffic_signals](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dtraffic_signals)).
7. **Clipping at the extract border** — osmium "will never clip"; Overpass
   cuts ways mid-segment; relations dangle. Extract with buffer, clip
   downstream, mark map edges
   ([osmium-extract manpage](https://manpages.debian.org/unstable/osmium-tool/osmium-extract.1.en.html)).
8. **Hard-failing on bad tags** — a wrong restriction or pipe-count mismatch
   must degrade to a warning + fallback, not an import failure (fail-soft
   pattern; A/B Street).
9. **Inventing a global rule where OSM has regional divergence** — maxspeed
   zone notations (3 schemes), `motorway_link` extent (US vs international),
   traffic-signal placement, left-hand traffic (netconvert's `--lefthand`
   exists because OSM does not reliably tag driving side per way)
   ([Key:maxspeed](https://wiki.openstreetmap.org/wiki/Key:maxspeed),
   [motorway_link](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmotorway_link),
   [netconvert options](https://sumo.dlr.de/docs/netconvert.html)).
10. **Relying on `lanes:bus=N`-style counts for lane position** — counts say
    *how many*, never *which*; only the `bus:lanes=` pipe-list carries position
    ([Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)).
11. **Confusing indicated with legal** — turn:lanes records markings;
    restrictions record law; both must be imported and reconciled
    ([Key:turn](https://wiki.openstreetmap.org/wiki/Key:turn)).
12. **Adopting a compiled artifact you can't re-derive** — the CommonRoad
    caveat ("estimations are imperfect … edit by hand") is what happens when
    the pipeline isn't re-runnable; our stance: everything re-derivable from
    pinned extract + importer version, edits as overlays/patches
    ([CommonRoad docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-scenario-designer/),
    [[concept-scenario-format]]).

## Empirical anchors

- Tag density (taginfo, 2026-07-17): `lanes` 19.77 M (~11% of ~177.9 M
  motor-road ways), `maxspeed` 21.86 M (~12%), `turn:lanes` 1.187 M (~0.7%),
  `lanes:forward/backward` ~1.4 M each, `width` 3.79 M, `width:lanes` 9.9 k,
  `placement` 371 k, `change:lanes` 77 k, `bus:lanes` 43.7 k
  ([implementation §3](./implementation.md) for per-tag links).
- Control furniture: `highway=stop` 2.44 M nodes (`stop=all` 118 k),
  `traffic_signals` 1.99 M, `give_way` 1.63 M, `junction=roundabout` 1.01 M
  ways, `junction=circular` 52 k, `mini_roundabout` 58 k nodes,
  `priority_road` 194 k ways, `motorway_junction` 238 k nodes
  ([implementation §4](./implementation.md)).
- Turn restrictions: 2.274 M relations (15.6% of all relations);
  `restriction:conditional` 18.6 k
  ([taginfo](https://taginfo.openstreetmap.org/api/4/tag/stats?key=type&value=restriction)).
- Overpass governance: ~10 k queries/day, ~1 GB/day per IP; 180 s / 512 MiB
  per-query defaults; 429/504 denial codes
  ([Overpass Commons](https://dev.overpass-api.de/overpass-doc/en/preface/commons.html)).
- Corpus scale: planet .pbf ~83.5 GiB (Nov 2025); Geofabrik Europe 32.3 GB /
  North America 17.9 GB, daily updates
  ([Geofabrik](https://download.geofabrik.de/)).
- netconvert typemap defaults (m/s): motorway 39.44, trunk/primary/secondary
  27.78, tertiary 22.22, unclassified/residential 13.89, service 5.56,
  living_street 2.78; priorities 14→1
  ([typ.xml](https://raw.githubusercontent.com/eclipse-sumo/sumo/main/data/typemap/osmNetconvert.typ.xml)).
- osm2streets hard-coded widths: sidewalk 1.5 m, shoulder 0.5 m, service road
  2.0 m ([osm2lanes/src/lib.rs](https://github.com/a-b-street/osm2streets/blob/main/osm2lanes/src/lib.rs)).
  SUMO lane width default 3.2 m (via [[arch-road-graph-model]]).
- Consolidation distances: osmnx `consolidate_intersections` tolerance default
  10 (per-node buffer); netconvert `--junctions.join-dist` 10 m,
  `--tls.guess-signals.dist` 25 m ([[arch-road-graph-model]] empirical anchors).
- Maintenance signals (GitHub, 2026-07-17): SUMO 4.1 k★ active; A/B Street
  8.1 k★ last push 2025-09; osm2streets 145★ last push 2025-10 ("revival"
  issue open); osmnx 5.8 k★ active; paulmach/osm 466★ active.

## Open Questions

- Left-hand traffic: OSM has no reliable per-way driving-side tag; netconvert's
  global `--lefthand` and CARLA's per-road OpenDRIVE `rule="LHT"` are the two
  poles. Import parameter (per scenario region) vs per-edge override? (No
  primary OSM source found codifying a country table; needs a decision before
  the importer's lane-direction logic is written.)
- `type=connectivity` relation (exact lane-to-lane mapping) — rare in the wild;
  support now or treat as future bonus? ([Key:turn](https://wiki.openstreetmap.org/wiki/Key:turn))
- Whether `change:lanes` should feed MOBIL-level lane-change constraints in our
  model or stay advisory — depends on [[domain-traffic-flow-models]]' final
  lane-change scope.
- `restriction:conditional` (18.6 k relations) and time-of-day semantics:
  ignore in v1 (flagged), or carry as scenario-time-varying control? Ties to
  [[concept-scenario-format]] time slicing.

## Master source list

OSM wiki: [Elements](https://wiki.openstreetmap.org/wiki/Elements) ·
[Way](https://wiki.openstreetmap.org/wiki/Way) ·
[OSM XML](https://wiki.openstreetmap.org/wiki/OSM_XML) ·
[PBF Format](https://wiki.openstreetmap.org/wiki/PBF_Format) ·
[Permanent ID](https://wiki.openstreetmap.org/wiki/Permanent_ID) ·
[Attic Data](https://wiki.openstreetmap.org/wiki/Attic_Data) ·
[Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes) ·
[Lanes](https://wiki.openstreetmap.org/wiki/Lanes) ·
[Key:turn](https://wiki.openstreetmap.org/wiki/Key:turn) ·
[Key:oneway](https://wiki.openstreetmap.org/wiki/Key:oneway) ·
[Key:placement](https://wiki.openstreetmap.org/wiki/Key:placement) ·
[Key:change](https://wiki.openstreetmap.org/wiki/Key:change) ·
[Key:width](https://wiki.openstreetmap.org/wiki/Key:width) ·
[Key:destination](https://wiki.openstreetmap.org/wiki/Key:destination) ·
[Key:maxspeed](https://wiki.openstreetmap.org/wiki/Key:maxspeed) ·
[Default speed limits](https://wiki.openstreetmap.org/wiki/Default_speed_limits) ·
[Key:zone:maxspeed](https://wiki.openstreetmap.org/wiki/Key:zone:maxspeed) ·
[Key:highway](https://wiki.openstreetmap.org/wiki/Key:highway) ·
[Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction) ·
[junction=roundabout](https://wiki.openstreetmap.org/wiki/Tag:junction%3Droundabout) ·
[junction=circular](https://wiki.openstreetmap.org/wiki/Tag:junction%3Dcircular) ·
[mini_roundabout](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmini_roundabout) ·
[highway=stop](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dstop) ·
[highway=give_way](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dgive_way) ·
[highway=traffic_signals](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dtraffic_signals) ·
[Key:priority_road](https://wiki.openstreetmap.org/wiki/Key:priority_road) ·
[motorway_junction](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmotorway_junction) ·
[motorway_link](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmotorway_link) ·
[Key:bridge](https://wiki.openstreetmap.org/wiki/Key:bridge) ·
[Overpass API](https://wiki.openstreetmap.org/wiki/Overpass_API) ·
[ODbL](https://wiki.openstreetmap.org/wiki/Open_Database_License) ·
[JOSM Validator](https://wiki.openstreetmap.org/wiki/JOSM/Validator) ·
[Osmose](https://wiki.openstreetmap.org/wiki/Osmose) —
OSMF: [Licence](https://osmfoundation.org/wiki/Licence) ·
[Legal FAQ](https://wiki.osmfoundation.org/wiki/Licence/Licence_and_Legal_FAQ) ·
[Produced Work guideline](https://osmfoundation.org/wiki/Licence/Community_Guidelines/Produced_Work_-_Guideline) ·
[Community Guidelines](https://wiki.osmfoundation.org/wiki/Licence/Community_Guidelines) ·
[Geocoding guideline](https://wiki.osmfoundation.org/wiki/Licence/Community_Guidelines/Geocoding_-_Guideline) —
Overpass: [Commons manual](https://dev.overpass-api.de/overpass-doc/en/preface/commons.html) ·
[dev.to walkthrough](https://dev.to/toodaniels/how-to-get-streets-data-using-overpass-api-2b2g) —
Extraction: [Geofabrik](https://download.geofabrik.de/) ·
[planet mirror](https://ftp.nluug.nl/maps/planet.openstreetmap.org/pbf/?C=S&O=A) ·
[osmium-extract manpage](https://manpages.debian.org/unstable/osmium-tool/osmium-extract.1.en.html) ·
[osmium-tool](https://github.com/osmcode/osmium-tool) ·
[pyosmium](https://github.com/osmcode/pyosmium) —
Tools: [osmnx User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html) ·
[osmnx settings.py](https://github.com/gboeing/osmnx/blob/main/osmnx/settings.py) ·
[osmnx#22](https://github.com/gboeing/osmnx/issues/22) ·
[osmnx#784](https://github.com/gboeing/osmnx/issues/784) ·
[Boeing 2025 arXiv](https://arxiv.org/pdf/2407.00258v1) ·
[osm2streets README](https://github.com/a-b-street/osm2streets) ·
[osm2streets how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md) ·
[osm2lanes src/lib.rs](https://github.com/a-b-street/osm2streets/blob/main/osm2lanes/src/lib.rs) ·
[osm2lanes (archived)](https://github.com/a-b-street/osm2lanes) ·
[A/B Street importing](https://a-b-street.github.io/docs/tech/map/importing/index.html) ·
[convert_osm](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html) ·
[rest](https://a-b-street.github.io/docs/tech/map/importing/rest.html) ·
[map model](https://a-b-street.github.io/docs/tech/map/index.html) ·
[SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html) ·
[netconvert options](https://sumo.dlr.de/docs/netconvert.html) ·
[osmNetconvert.typ.xml](https://raw.githubusercontent.com/eclipse-sumo/sumo/main/data/typemap/osmNetconvert.typ.xml) ·
[osm2gmns docs](https://osm2gmns.readthedocs.io/) ·
[pt2matsim OsmConverterConfigGroup](https://github.com/matsim-org/pt2matsim/blob/master/src/main/java/org/matsim/pt2matsim/config/OsmConverterConfigGroup.java) ·
[CommonRoad designer docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-scenario-designer/) ·
[OSRM profiles.md](https://github.com/Project-OSRM/osrm-backend/blob/master/docs/profiles.md) ·
[OSRM restriction_parser.cpp](https://github.com/Project-OSRM/osrm-backend/blob/master/src/extractor/restriction_parser.cpp) ·
[GraphHopper turn-restrictions.md](https://github.com/graphhopper/graphhopper/blob/master/docs/core/turn-restrictions.md) ·
[GraphHopper OSMRestrictionConverter.java](https://github.com/graphhopper/graphhopper/blob/master/core/src/main/java/com/graphhopper/reader/osm/OSMRestrictionConverter.java) ·
[Valhalla tag-parsing](https://valhalla.github.io/valhalla/contributing/architecture/mjolnir/tag-parsing/) ·
[Valhalla graph.lua](https://github.com/valhalla/valhalla/blob/master/lua/graph.lua) ·
[paulmach/osm](https://github.com/paulmach/osm) ·
[moveet](https://github.com/ivannovazzi/moveet) ·
[moss](https://github.com/tsinghua-fib-lab/moss) —
Overture: [Transportation to GA](https://docs.overturemaps.org/blog/2024/12/18/transportation-to-ga/) ·
[GERS](https://docs.overturemaps.org/gers/) ·
[segment schema](https://docs.overturemaps.org/schema/reference/transportation/segment/) ·
[2024-11-13 notes](https://docs.overturemaps.org/blog/2024-11-13.0/) ·
[2024-03-12 alpha notes](https://overturemaps.org/overture-2024-03-12-alpha-0-release-notes/) —
QA/editors: [JOSM SplitWay](https://josm.openstreetmap.de/wiki/Help/Action/SplitWay) ·
[iD#2358](https://github.com/openstreetmap/iD/issues/2358) ·
[iD#5828](https://github.com/openstreetmap/iD/issues/5828) ·
[iD#6711](https://github.com/openstreetmap/iD/issues/6711) ·
[OsmAnd#5221](https://github.com/osmandapp/OsmAnd/issues/5221) ·
[giswiki.ch OpenStreetMap](https://www.giswiki.ch/OpenStreetMap) ·
[atlas.co OSM deep dive](https://atlas.co/courses/gis-basics/openstreetmap-deep-dive/)
