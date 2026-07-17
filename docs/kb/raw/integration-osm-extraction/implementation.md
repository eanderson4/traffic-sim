# Mechanics: OSM Extraction

> Source: web research (greenfield — no importer code exists; this file collects the
> *mechanisms* by which OSM data becomes a lane-level road graph, as implemented by
> Overpass/osmium, osmnx, osm2streets, A/B Street, SUMO netconvert, and the routing
> engines — to be re-audited against real code once our importer exists) |
> Researched: 2026-07-17 | Git HEAD: 6efd963

## 1. The input: three element types, geometry in nodes only, streets fragmented

Everything downstream is shaped by how little structure OSM gives you:

- **Nodes, ways, relations — nothing else.** Nodes carry the only coordinates
  (lat/lon WGS84); ways are ordered lists of 2–2,000 node references with *no
  geometry of their own*; relations group elements with roles. All three carry
  free-form `key=value` tags (≤255 chars, unique keys) — a folksonomy, not a
  schema: "if there is more than one way to tag a given feature, it's probably
  best to use the most common approach"
  ([Elements](https://wiki.openstreetmap.org/wiki/Elements),
  [Way](https://wiki.openstreetmap.org/wiki/Way),
  [OSM XML](https://wiki.openstreetmap.org/wiki/OSM_XML)).
- **Ways always have a direction** (node order), even for two-way streets, and a
  large family of tags is interpreted relative to it (`lanes:forward`,
  `turn:lanes:backward`, `direction=forward`). Reversing a way is a real hazard:
  editors auto-fix some tags but have historically broken others (§2)
  ([Way](https://wiki.openstreetmap.org/wiki/Way)).
- **A street is not a way.** Ways split whenever *any* attribute changes, and the
  documented split triggers are exactly the attributes a simulator cares about:
  lane count ("If the number of lanes changes, it is necessary to split the OSM
  way… as soon as a new lane has started" — [Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)),
  speed limit ("added to only the segment of roadway for which the speed limit
  applies" — [Key:maxspeed](https://wiki.openstreetmap.org/wiki/Key:maxspeed)),
  bridges ("Split the upper way at each end of the bridge" — [Key:bridge](https://wiki.openstreetmap.org/wiki/Key:bridge)),
  turn restrictions (from/to "must start/end at the via node or the via-way(s),
  otherwise split it!" — [Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction)),
  roundabouts ([junction=roundabout](https://wiki.openstreetmap.org/wiki/Tag:junction%3Droundabout)).
  Dual carriageways are *two separate one-way ways*, not one way
  ([Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)).
- **Scale of the corpus**: >1.1 B ways total (Aug 2025), 264.8 M carrying
  `highway=*` ([Way](https://wiki.openstreetmap.org/wiki/Way),
  [taginfo highway stats](https://taginfo.openstreetmap.org/api/4/key/stats?key=highway)).

**Mechanism takeaway for us:** the importer's first job is *re-aggregation* —
splitting at true junctions while stitching attribute-fragmented ways back into
edge-like segments; OSM's way boundaries are tagging artifacts, not road
structure. Every serious importer does this (osm2streets "split ways at shared
nodes", netconvert "ways without intersections between them get joined").

## 2. Element IDs are database rows, not street identities

- **No stability guarantee.** The OSM wiki documents the problem via the
  (still-proposal) *Permanent ID* page: "A single way representing a street may
  be split into multiple ways due to a partial speed restriction, or may become
  two ways separated by a divider" — IDs denote database rows, and no permanent
  ID mechanism exists today (Wikidata QIDs are suggested as the de-facto most
  stable identifiers) ([Permanent ID](https://wiki.openstreetmap.org/wiki/Permanent_ID)).
  A university GIS wiki states the blunt form: "OSM IDs are declared as not
  being stable. Yet, many software rely on them (probably because of lack of
  alternatives)" ([giswiki.ch](https://www.giswiki.ch/OpenStreetMap) — secondary
  source; stronger wording than osm.org proper).
- **Split/merge behavior is editor-concrete**: JOSM reuses the existing way ID
  for the *longest* segment ("longest = most nodes"); the other segments get
  brand-new IDs ([JOSM SplitWay](https://josm.openstreetmap.de/wiki/Help/Action/SplitWay)).
  Merging ends one object's history — with observed real-world damage to lane
  tags: iD issue "It messed up the following tags: change:lanes, lanes,
  placement, turn:lanes" ([iD#2358](https://github.com/openstreetmap/iD/issues/2358)).
- **History exists server-side** (full editing history per element; full-history
  dumps; Overpass "attic" queries) — so an importer *can* resolve what an old ID
  became, but only as an archaeology step, not a live reference
  ([Elements](https://wiki.openstreetmap.org/wiki/Elements),
  [Attic Data](https://wiki.openstreetmap.org/wiki/Attic_Data)).

**Mechanism takeaway for us:** OSM IDs are **provenance, not identity**. Our
network's stable IDs (needed by scenario overlays and metric bindings per
[[concept-scenario-format]] and [[arch-road-graph-model]]) must be assigned by
our importer, with `osm_way`/`osm_node` recorded as re-derivable hints. Any
workflow that keys user edits to raw OSM IDs inherits the split/merge churn.
Overture's GERS (§standards file) is the field's attempt to fix exactly this.

## 3. The lane-tagging schema: expressive, optional, sparse

### What the tags say

- **`lanes=*`** counts *full-width motorized* lanes — includes bus/HOV/long slip
  lanes, excludes bicycle lanes and (convention varies) short turn pockets
  ([Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)). On a two-way
  road it is the **sum of both directions**; on `oneway=yes`, the single
  direction. Two documented traps: it is "widely misused to mean the lanes in
  each direction", and mappers often tag through-lanes only, so "data consumers
  can mostly treat the lanes tag as a minimum rather than an exact number"
  ([Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)).
  `lanes:forward/backward` handle uneven splits; `lanes:both_ways` +
  `turn:lanes:both_ways=left` marks a center turn lane.
- **`oneway`**: `yes/no/-1/reversible/alternating`; implied by
  `junction=roundabout` and `highway=motorway`; `motorway_link` is "so often one
  way" that some tools treat it as implied unless `oneway=no` — the wiki
  recommends explicit tagging
  ([Key:oneway](https://wiki.openstreetmap.org/wiki/Key:oneway),
  [motorway_link](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmotorway_link)).
  Mode exceptions use `oneway:bicycle=no`-style keys, not access tags.
- **`turn:lanes=*`**: one pipe-separated value per lane, left-to-right in way
  direction; full vocabulary `left, slight_left, sharp_left, through, right,
  slight_right, sharp_right, reverse, merge_to_left, merge_to_right, none`
  (plus rare `slide_*`); `;` combines permitted movements on one lane
  (`through;right`); empty ≡ `none` ([Key:turn](https://wiki.openstreetmap.org/wiki/Key:turn)).
  **Semantic trap**: it records *indicated* turns from markings — "turn:lanes=*
  tagging does not replace turn restrictions" — and exact lane-to-lane
  connectivity beyond its expressiveness has a separate `type=connectivity`
  relation ([Key:turn](https://wiki.openstreetmap.org/wiki/Key:turn)).
  Directional variants (`turn:lanes:forward/backward/both_ways`) and mode
  variants (`turn:bus:lanes`…) exist.
- **`placement=*`**: declares where the way line sits relative to the physical
  road (`middle_of:N`, `right_of:N`, `left_of:N`, `transition`) — the missing
  piece for lane-count transitions and fork geometry, because consumers
  otherwise assume the way is the road centerline. Status: de facto; proposal
  still "Draft (under way)" since 2012
  ([Key:placement](https://wiki.openstreetmap.org/wiki/Key:placement)).
- **Width**: `width=*` is carriageway kerb-to-kerb (with admitted "fuzziness");
  `width:lanes=*` per lane; `est_width` for estimates; `maxwidth` is a *legal*
  limit, not geometry. **No default lane width is codified anywhere** —
  consumers guess from class
  ([Key:width](https://wiki.openstreetmap.org/wiki/Key:width),
  [Lanes](https://wiki.openstreetmap.org/wiki/Lanes)).
- **Per-lane access**: the generic `*:lanes` suffix (`hgv:lanes=no|no|yes`,
  `bus:lanes`, `bicycle:lanes`, `hov:lanes`, `maxspeed:lanes`…). Critical
  semantic: `lanes:bus=1` says *how many*, never *which* — position needs the
  pipe-list form ([Key:lanes](https://wiki.openstreetmap.org/wiki/Key:lanes)).
- **`change:lanes=*`**: per-lane lane-change permission
  (`yes|no|not_right|not_left|only_right|only_left`) — encodes solid/dashed line
  logic; US MUTCD nuance (single solid = discouraged vs double solid =
  prohibited) is conflated in practice
  ([Key:change](https://wiki.openstreetmap.org/wiki/Key:change)).
- **`destination:lanes`** and family carry signposted destinations per lane;
  where destinations differ by approach direction the tag scheme is insufficient
  and a `type=destination_sign` relation is the escape
  ([Key:destination](https://wiki.openstreetmap.org/wiki/Key:destination)).

### How sparse it is (taginfo, 2026-07-17)

Against ~177.9 M motor-road ways (motorway…living_street excluding links;
[highway tag stats](https://taginfo.openstreetmap.org/api/4/key/stats?key=highway)):

| Tag | Ways | Share of motor roads |
|---|---|---|
| `lanes` | 19.77 M | ~11% |
| `maxspeed` | 21.86 M | ~12% |
| `lanes:forward`/`backward` | 1.40 M / 1.42 M | ~0.8% |
| `turn:lanes` | **1.187 M** | **~0.7%** |
| `placement` | 371 k | ~0.2% |
| `change:lanes` | 77 k | ~0.04% |
| `width` | 3.79 M | ~2% |
| `width:lanes` | 9.9 k | ~0.006% |
| `bus:lanes` / `hgv:lanes` | 43.7 k / 24.3 k | ~0.02% |

([lanes](https://taginfo.openstreetmap.org/api/4/key/stats?key=lanes),
[turn:lanes](https://taginfo.openstreetmap.org/api/4/key/stats?key=turn:lanes),
[placement](https://taginfo.openstreetmap.org/api/4/key/stats?key=placement),
[change:lanes](https://taginfo.openstreetmap.org/api/4/key/stats?key=change:lanes),
[width:lanes](https://taginfo.openstreetmap.org/api/4/key/stats?key=width:lanes),
[maxspeed](https://taginfo.openstreetmap.org/api/4/key/stats?key=maxspeed).)
Corroborated independently: "Specialised attributes (lane count, speed
limits) — incomplete even in well-mapped areas"
([atlas.co OSM deep dive](https://atlas.co/courses/gis-basics/openstreetmap-deep-dive/)).

**Mechanism takeaway for us:** OSM lane tags are a *bonus layer*, not a
foundation. The importer must (a) ship a class-based defaults table (§7's
typemap pattern) that produces a plausible network from geometry + `highway`
class alone, (b) treat every lane tag as an override of the default, and
(c) validate tags against each other — real data contains `lanes=4` next to a
5-value `turn:lanes` pipe list ([OsmAnd#5221](https://github.com/osmandapp/OsmAnd/issues/5221)).
StreetComplete's lane/maxspeed quests are the upstream force slowly improving
coverage ([Key:maxspeed](https://wiki.openstreetmap.org/wiki/Key:maxspeed)).

## 4. Junction tagging → control semantics

Every VISION junction type has an OSM source tag, but the conventions differ in
*placement*, which is where import bugs live:

- **`highway=stop`**: on the approach way *at the stop line*, never on the
  conflict node (not all approaches stop); `direction=forward/backward`
  disambiguates. All-way stop = `highway=stop` + `stop=all` on the shared
  junction node (or on all ≥4 approach nodes for split carriageways). 2.44 M
  stop nodes, 118 k `stop=all`
  ([highway=stop](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dstop),
  [taginfo](https://taginfo.openstreetmap.org/api/4/tag/stats?key=highway&value=stop)).
- **`highway=give_way`**: same placement rules; explicitly *not* on roundabout
  conflict points. 1.63 M nodes
  ([highway=give_way](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dgive_way)).
- **`highway=traffic_signals`**: **two competing conventions, both live**: (a)
  tag the shared intersection node; (b) tag separate stop-line nodes on *each
  incoming way* — "no well established convention", and with dual carriageways
  producing multiple signal nodes "it is up to the routing software to count
  nearby signals as one for timing purposes". Right-on-red:
  `red_turn:right=yes` / newer `traffic_signals:turn=right_on_red`. 1.99 M nodes
  ([highway=traffic_signals](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dtraffic_signals)).
  This is exactly why netconvert needs `--tls.guess-signals` (25 m radius) and
  `--tls.join` ([[arch-road-graph-model]] §9).
- **Priority**: `priority_road=designated|yes_unposted|end` on ways (194 k ways)
  for signposted priority-road countries; a new `highway=priority` node tag
  (2024-era, 7 k nodes)
  ([Key:priority_road](https://wiki.openstreetmap.org/wiki/Key:priority_road)).
  Otherwise priority must be *inferred from road class* — netconvert's
  right-of-way sort (priority, speed, lane count) is the reference heuristic
  ([[arch-road-graph-model]] §4).
- **`junction=roundabout`**: ring traffic *always* has right of way (else it's
  `junction=circular`); implies `oneway=yes`; each approach connects at its own
  node. 1.01 M ways. `junction=circular` (52 k) covers rotaries where ring
  traffic yields; `highway=mini_roundabout` (58 k nodes) is node-only with a
  traversable centre — with a data-quality warning that pre-2012 usage was
  often misapplied
  ([junction=roundabout](https://wiki.openstreetmap.org/wiki/Tag:junction%3Droundabout),
  [junction=circular](https://wiki.openstreetmap.org/wiki/Tag:junction%3Dcircular),
  [mini_roundabout](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmini_roundabout)).
- **Ramps**: `highway=motorway_junction` marks the gore node with `ref` = exit
  number (238 k nodes); `*_link` classes tag the ramps themselves — with a
  regional disagreement: internationally only the motorway-exclusive portion is
  `motorway_link`; in the US (DWG consensus) the link runs to where the ramp
  meets the surface street
  ([motorway_junction](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmotorway_junction),
  [motorway_link](https://wiki.openstreetmap.org/wiki/Tag:highway%3Dmotorway_link)).
- **`traffic_calming=bump|hump|table|…`** (1.41 M nodes) exists for completeness
  ([Key:highway](https://wiki.openstreetmap.org/wiki/Key:highway)).

**Mechanism takeaway for us:** junction *type* is a compile from three inputs —
explicit control tags (stop/give_way/signals), junction tags (roundabout…), and
class-based inference — with clustering (dual-carriageway signal nodes, split
stop nodes) as a mandatory pass. The tag placement diversity means "one OSM node
= one junction" is false in both directions.

## 5. Turn restriction relations: the relational half of the turn fabric

- **Structure**: `type=restriction`; members `from` (1 way), `via` (1 node **or**
  1+ ways), `to` (1 way); values `no_left_turn / no_right_turn / no_u_turn /
  no_straight_on` (prohibitory) and `only_left_turn / only_right_turn /
  only_u_turn / only_straight_on` (mandatory), plus rare `no_entry/no_exit`
  ([Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction)).
- **Adoption is real**: 2.274 M restriction relations = 15.6% of all relations
  ([taginfo](https://taginfo.openstreetmap.org/api/4/tag/stats?key=type&value=restriction));
  `restriction:conditional` on 18.6 k
  ([taginfo](https://taginfo.openstreetmap.org/api/4/key/stats?key=restriction:conditional)).
- **Scoping**: `restriction:hgv/bus/bicycle…` per mode; `except=psv;bicycle…`;
  time conditions `restriction:conditional=no_right_turn @ (Mo-Fr 07:00-09:00)`;
  `implicit=yes` for unsigned restrictions
  ([Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction)).
- **Documented parsing pitfalls** (from the wiki page itself):
  - from/to must start/end exactly at via — way splits break relations whose
    members no longer touch ("otherwise split it!").
  - "Some routing software will work only with turn restrictions that contain a
    single node in the via role" — via-way support is the maturity divider (§8).
  - Don't prefix-match `no_*`: `no_entry`/`no_exit` and "no turn on red"
    variants force value whitelisting; OSRM excludes `*_on_red`.
  - `only_left_turn` wrongly prohibits legal U-turns → map as
    `no_straight_on` + `no_right_turn` instead.
  - iD has an open bug creating hidden split segments on roundabouts when adding
    restrictions ([iD#6711](https://github.com/openstreetmap/iD/issues/6711)).
  ([Relation:restriction](https://wiki.openstreetmap.org/wiki/Relation:restriction))

**Mechanism takeaway for us:** restrictions are the *second* source of turn
semantics after geometry+turn:lanes, and the two must be reconciled (markings
say what's indicated, relations say what's legal). They must be applied **after
way splitting, to the correct resulting segment** — A/B Street's
`split_ways.rs` exists precisely to "make sure the destination of the
restriction is actually incident to a particular source road"
([convert_osm doc](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html)).

## 6. Acquisition mechanics: Overpass vs PBF+osmium, and the border problem

- **Overpass API** (AGPL-3.0, C++): read-only OSM query service, "good for a few
  elements … up to roughly 10 million"
  ([Overpass API](https://wiki.openstreetmap.org/wiki/Overpass_API)). The
  standard highway-bbox query (bbox order **south,west,north,east**):
  ```
  [out:json][timeout:25];
  ( way["highway"](S,W,N,E); );
  out body; >; out skel qt;
  ```
  `out body` emits tagged elements, `>;` recurses member nodes,
  `out skel qt` emits bare node geometry; `out geom;` embeds coordinates on ways
  directly ([Overpass API](https://wiki.openstreetmap.org/wiki/Overpass_API),
  [dev.to walkthrough](https://dev.to/toodaniels/how-to-get-streets-data-using-overpass-api-2b2g)).
  Output: OSM XML, JSON, CSV. Data lags the main DB by minutes (`osm_base`
  timestamp in each response).
- **Fair-use envelope** of the main public instance (overpass-api.de):
  **~10,000 queries/day and ~1 GB download/day per IP**; slot-based rate
  limiting with load-scaled cool-down; denials = HTTP 429 (rate) / 504
  (resource); per-query defaults timeout 180 s, maxsize 512 MiB
  ([Overpass Commons manual](https://dev.overpass-api.de/overpass-doc/en/preface/commons.html),
  [instance table](https://wiki.openstreetmap.org/wiki/Overpass_API)).
  Explicit anti-patterns: stitching bboxes to scrape the world; country-sized
  extracts ("use planet.osm mirrors"). Self-hosting is the sanctioned heavy-use
  path.
- **PBF path**: Geofabrik serves region files (`<region>-latest.osm.pbf`,
  updated daily; Europe 32.3 GB, North America 17.9 GB); the planet is
  ~83.5 GiB (Nov 2025) ([Geofabrik](https://download.geofabrik.de/),
  [planet mirror](https://ftp.nluug.nl/maps/planet.openstreetmap.org/pbf/?C=S&O=A)).
  `osmium extract` (GPL-3.0+ CLI) cuts by bbox/polygon with three strategies —
  `simple`, **`complete_ways` (default, 2 passes: ways reference-complete,
  relations not)**, `smart` (3 passes, completes multipolygons)
  ([osmium-extract manpage](https://manpages.debian.org/unstable/osmium-tool/osmium-extract.1.en.html)).
- **The border invariant**: *"osmium extract will never clip any OSM objects"*
  — boundary ways keep out-of-region node references; relations keep dangling
  members. The same disease hits Overpass bboxes: ways cut mid-segment,
  restrictions whose via/to fall outside. Documented symptoms across tools:
  SUMO's "Discarding way … only 1 node(s)" and "Ignoring restriction relation
  … falls outside the boundaries"
  ([SUMO OSM import §Warnings](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html));
  A/B Street's clipped-relation gluing failures ("manually tune the boundary
  polygon when this happens")
  ([convert_osm doc](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html)).
  Universal mitigation: **extract with a buffer, clip precisely downstream** —
  osm2streets marks clipped boundary roads `MapEdge`
  ([how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md));
  osmnx offers `truncate_by_edge=True`
  ([osmnx User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html)).

**Mechanism takeaway for us:** two acquisition modes — Overpass JSON for
interactive "pick a bbox, see traffic" (the VISION/A-B-Street first-run shape),
Geofabrik PBF + `osmium extract` (cached region file) for reproducible/CI
imports. Either way the importer sees a *raw OSM document with dangling border
references* and must own the buffer-then-clip discipline itself; border edges
become demand entry/exit portals and need a first-class "map edge" marker.

## 7. How the tools compile OSM → road graph

### osmnx — the centerline baseline (what you get for free, and what you don't)
- Graph = `networkx.MultiDiGraph`: nodes = OSM node IDs with x/y; edges =
  `(u,v,key)` with `osmid, length, highway, oneway, lanes, maxspeed, name…`;
  two-way ways become two reciprocal directed edges
  ([User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html)).
  Source = Overpass (`graph_from_bbox`).
- **Tag survival is an explicit allowlist**: `useful_tags_way` defaults to
  exactly `["access","area","bridge","est_width","highway","junction","landuse",
  "lanes","maxspeed","name","oneway","ref","service","tunnel","width"]` —
  `turn:lanes`, `lanes:forward/backward` are absent and **relations are never
  read** ([settings.py](https://github.com/gboeing/osmnx/blob/main/osmnx/settings.py)).
  Direction-dependent tags get copied onto *both* reciprocal edges, making them
  ambiguous ([issue #784](https://github.com/gboeing/osmnx/issues/784)).
- **`simplify_graph`** removes degree-2 non-endpoint nodes, preserving original
  curvature in a `geometry` edge attribute; edges spanning multiple OSM ways get
  list-valued attributes (algorithm published: Boeing 2025,
  [arXiv:2407.00258](https://arxiv.org/pdf/2407.00258v1)). This is the canonical
  implementation of §1's re-aggregation — and shows its cost: merged edges carry
  *conflicting* attributes as lists.
- **`consolidate_intersections(tolerance=10)`** merges nearby nodes (the 4 nodes
  of a divided-road crossing) into clusters — tolerance is a per-node buffer
  radius; original IDs preserved in `osmid_original`
  ([User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html)).
- **Turn restrictions: declared out of scope** by the maintainer ("NetworkX's
  path calculation algorithms … don't account for spatial networks with turn
  restrictions" — [issue #22](https://github.com/gboeing/osmnx/issues/22)).
- Speed imputation: `add_edge_speeds` = mean `maxspeed` per highway class with
  global-mean fallback ([User Reference](https://osmnx.readthedocs.io/en/stable/user-reference.html)).
- MIT, 5.8 k★, actively maintained ([repo](https://github.com/gboeing/osmnx)).

### osm2streets — the lane-spec schema and transformation passes
- **Schema**: `Road`s (each connecting exactly two `Intersection`s; a thickened
  line-string + ordered left-to-right lane list of **type, direction, width**)
  and `Intersection`s (polygons; kinds `MapEdge, Terminus, Connection, Fork,
  Intersection`) ([README](https://github.com/a-b-street/osm2streets),
  [how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)).
- **Lane-type enum** (from source): `Driving, Parking(Parallel|Diagonal|
  Perpendicular), Sidewalk, Shoulder, Biking, Bus, SharedLeftTurn, Construction,
  LightRail, Buffer(Stripes|FlexPosts|Planters|JerseyBarrier|Curb|Verge),
  Footway, SharedUse`; hard-coded default widths incl. sidewalk 1.5 m, shoulder
  0.5 m, service road 2.0 m
  ([osm2lanes/src/lib.rs](https://github.com/a-b-street/osm2streets/blob/main/osm2lanes/src/lib.rs)).
- **Pipeline** (`osm_to_street_network`): extract road-like ways → split ways at
  shared nodes into intersections (tiny roundabouts collapsed "as a hack"; raw
  restriction data and signal nodes matched to roads) → parse lanes from tags →
  clip to boundary polygon (boundary roads marked `MapEdge`) → match
  crossings/barriers
  ([how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)).
- **Transformations** (caller-ordered; `standard_for_clipped_areas` preset):
  `RemoveDisconnectedRoads`, `CollapseShortRoads` (incl. `junction=intersection`
  dog-legs), `CollapseDegenerateIntersections`, `MergeDualCarriageways`
  (experimental "sausage links"), `SnapCycleways` (experimental)
  ([how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)).
- **Turn movements: not yet** — "Planned: turning movements and crosswalks"
  ([README](https://github.com/a-b-street/osm2streets)); restriction relations
  are parsed and kept in sync across collapses but no lane-connectivity graph is
  generated ([how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)).
- The standalone `osm2lanes` repo is **archived** — "The lane parsing logic
  lives on in osm2streets" ([osm2lanes README](https://github.com/a-b-street/osm2lanes)).
- Apache-2.0, 145★, last push 2025-10-02 (near-dormant; open issue #235
  "Revival ideas") ([repo](https://github.com/a-b-street/osm2streets)).

### A/B Street's importer — turn generation with fail-soft restriction filtering
- Pipeline: osmium clip to a hand-drawn boundary polygon → `convert_osm`
  (OSM→`RawMap`) → `map_model` (`RawMap`→`Map`)
  ([importing index](https://a-b-street.github.io/docs/tech/map/importing/index.html)).
- `extract.rs` reads road-like ways + traffic-signal nodes + **turn restriction
  relations between OSM ways**; `split_ways.rs` splits at intersections and
  **applies restrictions to the correct resulting segment**, remembering
  way-begin/end segments for per-lane restrictions
  ([convert_osm doc](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html)).
- **Turn generation** (`make/turns.rs`): pairwise lane-type matching between
  roads; straight = Cartesian product of driving lanes (index-mismatched pairs
  classified `LaneChangeLeft/Right`); left/right turns originate only from the
  appropriate side lane; then **filter by restriction relations and per-lane
  restrictions — with the load-bearing fallback**: "Some of these OSM tags are
  just completely wrong sometimes. If the filter makes an incoming lane lose all
  of its turns, then ignore that tag."
  ([rest doc](https://a-b-street.github.io/docs/tech/map/importing/rest.html)).
  This is the concrete implementation of the orphan-lane invariant from
  [[arch-road-graph-model]] §2.
- Also: stop-sign inference, heuristic fixed-time signal generation, border
  "blackhole" SCC handling ([map model doc](https://a-b-street.github.io/docs/tech/map/index.html)).
  Output is a serialized Rust blob, not a portable format. Apache-2.0, 8.1 k★,
  last push 2025-09 ([repo](https://github.com/a-b-street/abstreet)).

### SUMO netconvert — typemaps + option surface (the battle-tested compiler)
- **Typemap mechanism**: netconvert builds a type name `<KEY>.<VALUE>` (e.g.
  `highway.residential`) and looks up default **lane count, speed, priority,
  permissions** in a typemap XML (`data/typemap/osmNetconvert.typ.xml` + patch
  typemaps for urban-DE, pedestrians, bicycle, rail…). Concrete defaults:
  motorway 2 lanes / 39.44 m/s / priority 14 / oneway; trunk 2 / 27.78 / 13;
  primary 2 / 27.78 / 12; secondary 1 / 27.78 / 11; tertiary 1 / 22.22 / 10;
  unclassified 1 / 13.89 / 4; residential 1 / 13.89 / 3; service 1 / 5.56 / 1
  ([osmNetconvert.typ.xml](https://raw.githubusercontent.com/eclipse-sumo/sumo/main/data/typemap/osmNetconvert.typ.xml)).
  The docs confess the values "were set-up ad-hoc and are not yet verified"
  ([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
  **Explicit way tags override the typemap** (`lanes`, `maxspeed`, `oneway`;
  `junction=roundabout` forces oneway) — the defaults-table/override split we
  want, proven.
- **Option surface** (extraction-relevant; defaults from the option reference):
  `--osm.turn-lanes` (use turn:lanes to guide connection generation; **default
  false**; partial-marking trap: "unmarked lanes are interpreted as
  through-lanes. This may not be correct in all cases"),
  `--geometry.remove`, `--edges.join`, `--ramps.guess`, `--junctions.join`,
  `--tls.guess-signals` (25 m), `--tls.join`, `--osm.all-attributes` /
  `--osm.extra-attributes` (tag passthrough as edge `<param>`s),
  `--output.original-names` (per-lane `origID`), `--keep-edges.by-vclass/type`,
  `--osm.elevation`, `--lefthand`
  ([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
  [netconvert options](https://sumo.dlr.de/docs/netconvert.html)).
  Recommended bundle: `--geometry.remove --ramps.guess --junctions.join
  --tls.guess-signals --tls.discard-simple --tls.join --tls.default-type actuated`.
- **Turn semantics**: "By default, lane-to-lane connections are guessed by
  netconvert and only turning restrictions are loaded from OSM to influence
  connection generation"
  ([§Lane-To-Lane Connections](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- **ID mapping**: OSM node 1234 → junction `1234`; way 5677 → edges
  `5677#0..#n` split at intersections, `-5677#n` reverse; intersection-free
  stretches joined under the *first* way's ID; joined clusters `cluster_1_2`
  with internal edges `:cluster_1_2_INDEX`
  ([§OSM-id/SUMO-id relationship](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).
- **Failure symptoms are documented as a warnings table** — "Ignoring
  restriction relation …", "Direction of restriction relation could not be
  determined", "Discarding way … only 1 node(s)"
  ([§Warnings](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)) —
  the shape of the import report we should emit. Junction-join/TLS/turn-lane
  *consequences* (jams, deadlocks) are cataloged in [[arch-road-graph-model]]
  §9/anti-patterns.
- EPL-2.0 C++ binary, 4.1 k★, very active; shell-out is the intended usage
  (`osmWebWizard.py`: bbox → net + demand in 3 clicks; `osmGet.py` tiles large
  areas into multiple Overpass requests)
  ([SUMO OSM import](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html),
  [eclipse-sumo/sumo](https://github.com/eclipse-sumo/sumo)).
  The lane-2-lane connection algorithm itself is explicitly *undocumented* —
  "computation of lane-2-lane connections" sits in the docs' Missing
  Descriptions section: the algorithm is code, not spec
  ([§Missing Descriptions](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)).

## 8. Turn-restriction handling across tools — the maturity divider

| Tool | Parses `type=restriction`? | Notes |
|---|---|---|
| SUMO netconvert | Yes | Influences connection guessing; border-cut relations dropped with warnings ([doc](https://sumo.dlr.de/docs/Networks/Import/OpenStreetMap.html)) |
| A/B Street | Yes | Applied post-split to correct segment; via-way honored; fail-soft per-lane filtering ([convert_osm](https://a-b-street.github.io/docs/tech/map/importing/convert_osm.html), [rest](https://a-b-street.github.io/docs/tech/map/importing/rest.html)) |
| osm2streets | Raw data only | Kept in sync across transformations; no movement generation yet ([how_it_works.md](https://github.com/a-b-street/osm2streets/blob/main/docs/how_it_works.md)) |
| osmnx | **No** | Maintainer-declared out of scope ([issue #22](https://github.com/gboeing/osmnx/issues/22)) |
| OSRM (BSD-2) | Yes | Via-node **and** via-way in current master; `no_entry`/`no_exit` parsed as multi-from/multi-to restrictions; excludes `*_on_red` ([restriction_parser.cpp](https://github.com/Project-OSRM/osrm-backend/blob/master/src/extractor/restriction_parser.cpp), [OSM wiki edge cases](https://wiki.openstreetmap.org/wiki/Relation:restriction)) |
| GraphHopper (Apache-2.0) | Yes | Via-node + via-way; conditional `restriction:bus=` and `except=`; via-way relations with duplicated members ignored ([turn-restrictions.md](https://github.com/graphhopper/graphhopper/blob/master/docs/core/turn-restrictions.md), [OSMRestrictionConverter.java](https://github.com/graphhopper/graphhopper/blob/master/core/src/main/java/com/graphhopper/reader/osm/OSMRestrictionConverter.java)) |
| Valhalla (MIT) | Yes | Incl. `restriction:conditional` time-based; `no_entry/no_exit` treated as ordinary `no_*`; no multi from/to ([graph.lua](https://github.com/valhalla/valhalla/blob/master/lua/graph.lua), [OSM wiki edge cases](https://wiki.openstreetmap.org/wiki/Relation:restriction)) |
| pt2matsim (GPL-2.0) | Yes | → `disallowedNextLinks` on the first link; on by default ([OsmConverterConfigGroup.java](https://github.com/matsim-org/pt2matsim/blob/master/src/main/java/org/matsim/pt2matsim/config/OsmConverterConfigGroup.java)) |
| osm2gmns (GPL-3.0) | Unverified | Generates movement.csv; restriction-relation parsing not confirmed in docs ([docs](https://osm2gmns.readthedocs.io/)) |

The routing engines (OSRM/GraphHopper/Valhalla) are the most mature restriction
*parsers* in open source — production-hardened against exactly the wiki's
pitfall list — but they emit routing-graph turn costs, not sim lane graphs:
useful as **algorithmic references** (all permissively licensed), not as
converters.

## 9. The shared pipeline shape

Every documented end-to-end pipeline — SUMO (`osmWebWizard.py`/`osmGet.py` →
netconvert → polyconvert), A/B Street (osmium → convert_osm → map_model), MOSS
(`mosstool`, which *delegates* OSM handling to SUMO-format conversion —
[moss README](https://github.com/tsinghua-fib-lab/moss)), moveet (Geofabrik
cache → osmium → filter → GeoJSON — [README](https://github.com/ivannovazzi/moveet)),
CommonRoad (`crdesigner osmcr` + GUI repair) — converges on the same seven
passes:

1. **fetch** (Overpass one-shot, or PBF + osmium),
2. **clip** (buffer extract, precise polygon clip downstream, mark map edges),
3. **simplify/re-aggregate** (join attribute-split ways, remove degree-2 nodes,
   consolidate junction clusters),
4. **infer lanes** (class defaults + tag overrides → typed lane list),
5. **generate turns/connections** (geometry + turn:lanes-guided guessing),
6. **apply restrictions** (relations, post-split, fail-soft),
7. **validate & repair** (connectivity/SCC checks, warnings report, optional
   manual fix in JOSM/netedit/CommonRoad GUI).

The order matters and is load-bearing: restrictions before splitting attach to
the wrong segments (§5); consolidation before lane inference corrupts dual
carriageways; validation last catches what the heuristics broke. CommonRoad's
own caveat is the honest summary of pass 4–7 quality: "missing information such
as the course of individual lanes is estimated during the process. These
estimations are imperfect … it is advisable to edit the scenarios by hand"
([CommonRoad designer docs](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-scenario-designer/)).

## 10. Licensing mechanics for derived networks (ODbL)

- OSM data is **ODbL 1.0** since 12 Sep 2012 ([OSMF Licence](https://osmfoundation.org/wiki/Licence)).
  Obligations: **attribution** always ("© OpenStreetMap contributors");
  **share-alike** on the database or any *Derivative Database* publicly
  distributed; **Produced Works** (renderings, videos, sim visuals) may carry
  any license — but ODbL §4.6 requires offering recipients the underlying
  (derivative) database or the means to recreate it; purely internal use
  triggers nothing ([OSMF Legal FAQ](https://wiki.osmfoundation.org/wiki/Licence/Licence_and_Legal_FAQ)).
- **The test** (board-endorsed): "If the published result of your project is
  intended for the extraction of the original data, then it is a database and
  not a Produced Work. Otherwise it is a Produced Work"
  ([Produced Work Guideline](https://osmfoundation.org/wiki/Licence/Community_Guidelines/Produced_Work_-_Guideline)).
- **Applied to us**: a compiled lane-level network derived from OSM, distributed
  as data (scenario network files), is a Derivative Database → ODbL share-alike
  on *that file*; it cannot be relicensed. The simulation's outputs (heatmaps,
  videos, metrics) are Produced Works under our own license + attribution. A
  scenario bundling the network with *independent* layers (demand, control,
  metrics definitions) forms a **Collective Database** — share-alike touches
  only the OSM-derived layer
  ([Community Guidelines](https://wiki.osmfoundation.org/wiki/Licence/Community_Guidelines)).
- **Caveats**: "Substantial" is deliberately context-dependent (100-feature
  quantitative safe harbor in the Substantial guideline); the guidelines "carry
  no formal legal weight"; OSMF gives no legal advice and cannot grant
  alternative licenses ([OSMF FAQ](https://wiki.osmfoundation.org/wiki/Licence/Licence_and_Legal_FAQ)).
- Tool licenses (for the import-vs-shell-out decision): Overpass AGPL-3.0,
  osmium-tool GPL-3.0+, pyosmium/libosmium BSD-2-Clause (libosmium commonly
  Boost — unverified), osmnx MIT, osm2streets & A/B Street Apache-2.0,
  SUMO/netconvert EPL-2.0 (file-level weak copyleft — shelling out to the
  binary or parsing `.net.xml` output creates no obligation on our Go code),
  osm2gmns GPL-3.0, pt2matsim GPL-2.0, CommonRoad designer GPL-3.0,
  OSRM BSD-2-Clause, GraphHopper Apache-2.0, Valhalla MIT, paulmach/osm MIT
  (per-repo license files / docs as linked in §7–§8).

**Mechanism takeaway for us:** the license boundary falls *between* the network
file (ODbL share-alike if distributed) and everything else in the scenario
(ours). This is compatible with the permissive-license recommendation in
[[domain-simulator-landscape]] but must be decided in the license ADR — and it
rewards the "network as compiled artifact, re-derivable from a pinned OSM
extract" design: distributing the *recipe* (bbox + OSM version + importer
version) instead of the database is the cleanest ODbL posture.
