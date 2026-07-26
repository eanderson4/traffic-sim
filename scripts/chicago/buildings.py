#!/usr/bin/env python3
"""Extract building footprints for a Chicago zone and snap them to the network.

Two artifacts from one pass, for two consumers:

  (a) --out-geojson  WGS84 polygons for the viz overlay (demosrv -overlaydir,
      served at /overlay/buildings.geojson). Includes `other` buildings —
      they are the urban texture the towers sit in.
  (b) --out-index    the machine-readable building -> lane index the demand
      generator consumes to place trip origins (residential) and trip
      destinations (workplace) on the road network. Contains ONLY
      demand-relevant buildings (residential + workplace); `other` is
      rendering context, never demand.

Usage:
  buildings.py --pbf illinois.osm.pbf --zones zones.geojson --zone loop \
      --network data/networks/chi-loop/chi-loop.json \
      --road-osm data/networks/chi-loop/loop.osm \
      --out-geojson data/networks/chicago/buildings.geojson \
      --out-index   data/networks/chi-loop/buildings.json

Requires: pyosmium, shapely, pyproj (tools/sumo-venv).

--------------------------------------------------------------------------
CLASSIFICATION (kind)
--------------------------------------------------------------------------
`residential` and `workplace` are the demand-relevant kinds; everything else
is `other` and is excluded from the index and from every demand-relevant
total. The two lists below are the OSM `building=*` values that carry an
unambiguous trip-generation signal:

  residential: apartments, residential, dormitory, house, terrace,
               detached, semidetached_house, bungalow, houseboat
  workplace:   office, commercial, retail, hotel, government, civic,
               industrial, university, hospital, public, school, college,
               kindergarten, warehouse, supermarket, train_station,
               fire_station, museum, exhibition_hall, manufacture

`building=yes` is 71% of the footprints in chi-loop (30,804 of 43,281) and
carries NO signal on its own, so it defaults to `other`. It is promoted only
when another tag disambiguates it:

  -> workplace when the way also carries office=*, shop=*, healthcare=*,
     tourism in {hotel,hostel,motel,guest_house}, or amenity in the
     WORKPLACE_AMENITIES set below (restaurants, schools, clinics, civic
     buildings — places people travel TO for a sustained stay).
  -> workplace when building:levels >= YES_TOWER_LEVELS (8) and nothing else
     disambiguates. Rationale: an 8+-storey untagged structure inside this
     zone is a real downtown tower, and dropping it from the demand model
     entirely is a worse error than filing some residential towers under
     workplace. In chi-loop this promotes 89 buildings; the count is
     reported so the call can be revisited with better data.
  -> NEVER promoted to residential: nothing in the chi-loop data
     distinguishes an untagged 2-flat from an untagged storefront (0 of the
     30,804 building=yes ways carry a `residential=*` or `building:use=*`
     tag). This is a KNOWN under-count of residential mass — Chicago's
     ubiquitous 2- and 3-flats are overwhelmingly tagged `building=yes` with
     building:levels 2-3. Treating all of them as residential would be a
     guess; treating them as `other` is merely conservative.

`building=no` and `building:part=*` without a `building=*` tag are dropped
outright (the latter are 3D sub-volumes of a footprint that is already
mapped, and counting them would double-count floor area).

--------------------------------------------------------------------------
LEVELS
--------------------------------------------------------------------------
  1. `building:levels`, if parseable and > 0            -> "tag"
  2. else `height` (m) / STOREY_M (3.5), rounded, min 1 -> "height"
  3. else a per-kind default                            -> "default"

Defaults: residential 3, workplace 3, other 1. Three is the modal tagged
value for both kinds in this zone (Chicago's 3-flat / 3-storey storefront);
one is right for the garages, roofs and sheds that dominate `other`.

Level data is patchy and sometimes plainly wrong — verified in chi-loop:
Willis Tower (108 storeys) carries neither tag and takes the default 3;
Lake Point Tower (197 m) is tagged height=12; Marina City is tagged
height=45 (actual 179 m). floor_area_m2 is therefore a RANKING signal, not
a survey. No attempt is made to repair heights from an external source.

floor_area_m2 = footprint_m2 * levels, footprint measured in UTM 16N metres.

--------------------------------------------------------------------------
ACCESS-LANE SNAPPING
--------------------------------------------------------------------------
Each demand-relevant building is snapped to the nearest ELIGIBLE lane,
measured from the building FOOTPRINT (not its centroid — for a footprint the
size of the Merchandise Mart the centroid can be 90 m from every street and
would pick the wrong frontage).

Eligibility, in order of how much it matters:
  1. NOT a motorway / motorway_link / trunk / trunk_link. A tower does not
     have a driveway onto the Kennedy. Without this filter the whole demand
     model is nonsense.
  2. NOT `internal` (the network JSON's own flag for junction interiors —
     31,705 of chi-loop's 55,538 lanes). Injecting inside a junction is not
     a driveway.
  3. length >= MIN_LANE_M (30 m). netconvert leaves 0.2-3.5 m clipping stubs
     everywhere; they are useless as injection points and the same 30 m bar
     is what mkdemand.py/portals already use for "fragment".

Lane class comes from the edge id: netimport edge ids are the OSM way id
with an optional leading `-` (reverse direction) and an optional `#N` segment
suffix, so `-1125009030#1` -> way 1125009030 -> highway=* from the road
extract. Verified exact: this resolves a class for all 23,833 external lanes
in chi-loop and agrees with portals.json on all 1,663 portal lanes. If a
class ever fails to resolve, the documented FALLBACK is a speed-limit bar
(> FAST_LANE_MPS, 22.3 m/s ~ 80 km/h => treated as motorway-like and
excluded); it fires 0 times on chi-loop and the count is reported.

Among the MAX_CANDIDATES nearest eligible lanes, mid-block access is
preferred over a junction mouth with a soft penalty:

    score = distance_m + (END_PENALTY_M if the nearest point lands within
                          END_ZONE_M of either end of the lane else 0)

with END_ZONE_M = 10 and END_PENALTY_M = 25 — a junction-mouth lane wins only
when it is more than 25 m closer than the best mid-block alternative. The
count of buildings that still ended up within 10 m of a lane end is reported.

Nothing eligible within MAX_SNAP_M (150 m) => access_lane null, counted in
summary.unsnapped. A bad snap is worse than no snap.
"""
import argparse
import json
import re
import sys
import time

import numpy as np
import osmium
import shapely
from pyproj import Transformer
from shapely import STRtree, from_wkb
from shapely.affinity import translate
from shapely.geometry import LineString, mapping, shape
from shapely.ops import nearest_points, transform
from shapely.prepared import prep

TO_UTM = Transformer.from_crs("EPSG:4326", "EPSG:32616", always_xy=True).transform
TO_WGS = Transformer.from_crs("EPSG:32616", "EPSG:4326", always_xy=True).transform

STOREY_M = 3.5  # metres per storey when only `height` is tagged
YES_TOWER_LEVELS = 8  # building=yes at/above this many levels -> workplace
MIN_LANE_M = 30.0  # shorter lanes are netconvert clipping stubs
MAX_SNAP_M = 150.0  # beyond this, record no access lane at all
END_ZONE_M = 10.0  # "at a junction mouth"
END_PENALTY_M = 25.0  # soft penalty for snapping to a junction mouth
MAX_CANDIDATES = 6  # nearest eligible lanes re-ranked with the penalty
FAST_LANE_MPS = 22.3  # ~80 km/h — fallback motorway test when class unknown

RESIDENTIAL_BUILDINGS = frozenset((
    "apartments", "residential", "dormitory", "house", "terrace",
    "detached", "semidetached_house", "bungalow", "houseboat",
))

WORKPLACE_BUILDINGS = frozenset((
    "office", "commercial", "retail", "hotel", "government", "civic",
    "industrial", "university", "hospital", "public", "school", "college",
    "kindergarten", "warehouse", "supermarket", "train_station",
    "fire_station", "museum", "exhibition_hall", "manufacture",
))

# amenity=* values that make an otherwise-untagged building a place people
# travel to and stay at. Deliberately excludes drive-by amenities
# (fuel, car_wash, atm, parking, shelter, toilets, bench...).
WORKPLACE_AMENITIES = frozenset((
    "restaurant", "cafe", "bar", "pub", "fast_food", "nightclub",
    "school", "university", "college", "kindergarten", "childcare",
    "library", "theatre", "cinema", "arts_centre", "community_centre",
    "events_venue", "conference_centre", "exhibition_centre",
    "hospital", "clinic", "doctors", "dentist", "veterinary",
    "townhall", "courthouse", "police", "fire_station", "prison",
    "post_office", "bank", "social_facility", "marketplace",
))

WORKPLACE_TOURISM = frozenset(("hotel", "hostel", "motel", "guest_house"))

# Lane classes a building can never have a driveway onto.
EXCLUDED_LANE_CLASSES = frozenset((
    "motorway", "motorway_link", "trunk", "trunk_link",
))

LEVEL_DEFAULTS = {"residential": 3, "workplace": 3, "other": 1}

EDGE_WAY_RE = re.compile(r"^-?(\d+)")


def parse_number(raw):
    """First numeric value out of a messy OSM tag ("12;14", "40 m", "3,5")."""
    if raw is None:
        return None
    m = re.search(r"-?\d+(?:[.,]\d+)?", str(raw))
    if m is None:
        return None
    try:
        return float(m.group(0).replace(",", "."))
    except ValueError:
        return None


def classify(tags):
    """-> (kind, promoted_from_yes). See the module docstring."""
    b = tags.get("building")
    if b in RESIDENTIAL_BUILDINGS:
        return "residential", False
    if b in WORKPLACE_BUILDINGS:
        return "workplace", False
    if b != "yes":
        return "other", False
    # building=yes: promote only on a disambiguating tag.
    if ("office" in tags or "shop" in tags or "healthcare" in tags
            or tags.get("tourism") in WORKPLACE_TOURISM
            or tags.get("amenity") in WORKPLACE_AMENITIES):
        return "workplace", True
    levels = parse_number(tags.get("building:levels"))
    if levels is not None and levels >= YES_TOWER_LEVELS:
        return "workplace", True
    return "other", False


def resolve_levels(tags, kind):
    """-> (levels:int, source:'tag'|'height'|'default')."""
    lv = parse_number(tags.get("building:levels"))
    if lv is not None and lv >= 1:
        return int(round(lv)), "tag"
    h = parse_number(tags.get("height"))
    if h is None:
        h = parse_number(tags.get("building:height"))
    if h is not None and h > 0:
        return max(1, int(round(h / STOREY_M))), "height"
    return LEVEL_DEFAULTS[kind], "default"


def road_classes(path):
    """way id -> highway=* from the road-only OSM extract that was imported."""
    out = {}
    for obj in osmium.FileProcessor(path):
        if obj.is_way():
            h = obj.tags.get("highway")
            if h:
                out[obj.id] = h
    return out


def edge_way_id(edge):
    m = EDGE_WAY_RE.match(edge or "")
    return int(m.group(1)) if m else None


def load_lanes(net_path, classes):
    """Eligible lanes as (name, offset, ids, classes, lengths, lines) + stats.

    Lane shapes are already in the network-local metric frame; the caller
    converts building geometry into that frame with netOffset.
    """
    with open(net_path) as f:
        net = json.load(f)
    offset = net["provenance"]["netOffset"]
    name = net.get("name", "")
    ids, cls, lens, lines = [], [], [], []
    stats = {"total": 0, "internal": 0, "short": 0, "class": 0,
             "speed_fallback": 0, "degenerate": 0, "eligible": 0}
    for lane in net["lanes"]:
        stats["total"] += 1
        if lane.get("internal"):
            stats["internal"] += 1
            continue
        if lane.get("length", 0.0) < MIN_LANE_M:
            stats["short"] += 1
            continue
        klass = classes.get(edge_way_id(lane.get("edge")))
        if klass is None:
            # Documented fallback: no class => judge by speed limit.
            if lane.get("speedLimit", 0.0) > FAST_LANE_MPS:
                stats["speed_fallback"] += 1
                continue
        elif klass in EXCLUDED_LANE_CLASSES:
            stats["class"] += 1
            continue
        pts = lane.get("shape") or []
        if len(pts) < 2:
            stats["degenerate"] += 1
            continue
        line = LineString(pts)
        if line.length <= 0:
            stats["degenerate"] += 1
            continue
        ids.append(lane["id"])
        cls.append(klass)
        lens.append(float(lane.get("length", line.length)))
        lines.append(line)
        stats["eligible"] += 1
    return name, offset, ids, cls, lens, lines, stats


class BuildingScan:
    """Assembles building areas (closed ways AND multipolygon relations)."""

    def __init__(self, clip):
        self.clip = prep(clip)
        self.bounds = clip.bounds
        self.wkb = osmium.geom.WKBFactory()
        self.rows = []
        self.broken = 0
        self.parts = 0

    def area(self, a):
        tags = a.tags
        b = tags.get("building")
        if b is None:
            # building:part=* with no building=* is a 3D sub-volume of a
            # footprint that is already mapped — counting it double-counts.
            if "building:part" in tags:
                self.parts += 1
            return
        if b == "no":
            return
        try:
            geom = from_wkb(self.wkb.create_multipolygon(a))
        except Exception:
            self.broken += 1
            return
        if geom.is_empty:
            return
        bx = geom.bounds
        if (bx[2] < self.bounds[0] or bx[0] > self.bounds[2]
                or bx[3] < self.bounds[1] or bx[1] > self.bounds[3]):
            return
        if not self.clip.intersects(geom):
            return
        osm_id = ("w" if a.from_way() else "r") + str(a.orig_id())
        self.rows.append((osm_id, dict(tags), geom))


def round_coords(coords, ndigits=6):
    if isinstance(coords[0], (int, float)):
        return [round(c, ndigits) for c in coords]
    return [round_coords(c, ndigits) for c in coords]


def snap(buildings, offset, ids, cls, lens, lines):
    """Attach access-lane fields to every demand-relevant building in place.

    Vectorised: one STRtree dwithin query for the whole batch, one vectorised
    distance call over the resulting pairs, then the mid-block re-rank in
    Python over at most MAX_CANDIDATES lanes per building.
    """
    targets = [b for b in buildings if b["kind"] != "other"]
    for b in buildings:
        b["access"] = None
    if not targets or not lines:
        return targets, {"end_snapped": 0}

    # Building footprints into the network-local metric frame (local = utm +
    # netOffset — see the compiled network's provenance block).
    dx, dy = offset
    local = np.empty(len(targets), dtype=object)
    for i, b in enumerate(targets):
        local[i] = translate(b["utm"], xoff=dx, yoff=dy)
    lanes = np.empty(len(lines), dtype=object)
    for i, ln in enumerate(lines):
        lanes[i] = ln

    tree = STRtree(lines)
    pairs = tree.query(local, predicate="dwithin", distance=MAX_SNAP_M)
    if pairs.size == 0:
        return targets, {"end_snapped": 0}
    dists = shapely.distance(local[pairs[0]], lanes[pairs[1]])

    # Group pairs by building (query returns them sorted by input index).
    order = np.lexsort((dists, pairs[0]))
    bi, li, dd = pairs[0][order], pairs[1][order], dists[order]
    starts = np.searchsorted(bi, np.arange(len(targets)), side="left")
    ends = np.searchsorted(bi, np.arange(len(targets)), side="right")

    end_snapped = 0
    for k, b in enumerate(targets):
        lo, hi = starts[k], min(starts[k] + MAX_CANDIDATES, ends[k])
        if lo >= ends[k]:
            continue
        poly = local[k]
        best = None
        for j in range(lo, hi):
            idx = int(li[j])
            line = lines[idx]
            dist = float(dd[j])
            pt = nearest_points(poly, line)[1]
            off = float(line.project(pt))
            length = lens[idx]
            off = min(max(off, 0.0), length)
            at_end = off < END_ZONE_M or (length - off) < END_ZONE_M
            score = dist + (END_PENALTY_M if at_end else 0.0)
            if best is None or score < best[0]:
                best = (score, idx, dist, off, at_end)
        if best is None:
            continue
        _, idx, dist, off, at_end = best
        if at_end:
            end_snapped += 1
        b["access"] = {
            "lane": ids[idx],
            "dist": round(dist, 2),
            "offset": round(off, 2),
            "class": cls[idx],
            "length": round(lens[idx], 2),
        }
    return targets, {"end_snapped": end_snapped}


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--pbf", required=True)
    ap.add_argument("--zones", required=True)
    ap.add_argument("--zone", required=True)
    ap.add_argument("--network", required=True, help="compiled network JSON")
    ap.add_argument("--road-osm", required=True,
                    help="road-only OSM extract that was imported (way -> highway class)")
    ap.add_argument("--buffer-m", type=float, default=0.0,
                    help="grow the zone polygon before clipping (metres, UTM 16N)")
    ap.add_argument("--simplify-m", type=float, default=0.3,
                    help="footprint simplification tolerance in metres (geojson only)")
    ap.add_argument("--min-footprint-m2", type=float, default=0.0,
                    help="drop footprints smaller than this")
    ap.add_argument("--geojson-kinds", default="residential,workplace,other",
                    help="kinds to emit into the geojson overlay")
    ap.add_argument("--out-geojson", required=True)
    ap.add_argument("--out-index", required=True)
    args = ap.parse_args()

    t0 = time.time()
    with open(args.zones) as f:
        zones = json.load(f)
    feat = next((ft for ft in zones["features"]
                 if ft["properties"].get("name") == args.zone), None)
    if feat is None:
        sys.exit(f"buildings: no zone named {args.zone!r} in {args.zones}")
    clip = shape(feat["geometry"])
    if args.buffer_m > 0:
        clip = transform(TO_WGS, transform(TO_UTM, clip).buffer(args.buffer_m))
    else:
        clip = clip.buffer(0)

    classes = road_classes(args.road_osm)
    network_name, offset, ids, cls, lens, lines, lane_stats = load_lanes(
        args.network, classes)
    print(f"lanes: {lane_stats['eligible']} eligible of {lane_stats['total']} "
          f"(internal {lane_stats['internal']}, <{MIN_LANE_M:g}m {lane_stats['short']}, "
          f"excluded class {lane_stats['class']}, speed fallback "
          f"{lane_stats['speed_fallback']}, degenerate {lane_stats['degenerate']}); "
          f"{len(classes)} classed ways")
    if lane_stats["eligible"] == 0:
        sys.exit("buildings: no eligible lanes — check --network/--road-osm")

    scan = BuildingScan(clip)
    fp = osmium.FileProcessor(args.pbf).with_areas(
        osmium.filter.KeyFilter("building", "building:part"))
    for obj in fp:
        if obj.is_area():
            scan.area(obj)
    print(f"scan: {len(scan.rows)} building areas in zone {args.zone!r} "
          f"({scan.parts} building:part-only skipped, {scan.broken} unassemblable) "
          f"[{time.time() - t0:.0f}s]")

    buildings = []
    promoted = 0
    lvl_src = {"tag": 0, "height": 0, "default": 0}
    for osm_id, tags, geom_wgs in scan.rows:
        utm = transform(TO_UTM, geom_wgs)
        footprint = utm.area
        if footprint < args.min_footprint_m2:
            continue
        kind, from_yes = classify(tags)
        promoted += from_yes
        levels, source = resolve_levels(tags, kind)
        lvl_src[source] += 1
        c = utm.centroid
        buildings.append({
            "osm_id": osm_id,
            "name": tags.get("name", ""),
            "kind": kind,
            "building_tag": tags.get("building", ""),
            "levels": levels,
            "levels_source": source,
            "footprint_m2": round(footprint, 1),
            "floor_area_m2": round(footprint * levels, 1),
            "utm": utm,
            "wgs": geom_wgs,
            "cx": c.x,
            "cy": c.y,
        })

    targets, snap_stats = snap(buildings, offset, ids, cls, lens, lines)
    print(f"snap: {len(targets)} demand-relevant buildings vs "
          f"{len(ids)} eligible lanes [{time.time() - t0:.0f}s]")

    # ---- index (demand-relevant only) ----
    dx, dy = offset
    entries = []
    summary = {"buildings": 0, "residential": 0, "workplace": 0, "unsnapped": 0,
               "residential_floor_area_m2": 0.0, "workplace_floor_area_m2": 0.0}
    dist_samples = []
    for b in targets:
        a = b["access"]
        entries.append({
            "osm_id": b["osm_id"],
            "name": b["name"],
            "kind": b["kind"],
            "building_tag": b["building_tag"],
            "levels": b["levels"],
            "levels_source": b["levels_source"],
            "footprint_m2": b["footprint_m2"],
            "floor_area_m2": b["floor_area_m2"],
            "centroid": {
                "lon": round(b["wgs"].centroid.x, 6),
                "lat": round(b["wgs"].centroid.y, 6),
                "x": round(b["cx"] + dx, 2),
                "y": round(b["cy"] + dy, 2),
            },
            "access_lane": a["lane"] if a else None,
            "access_lane_dist_m": a["dist"] if a else None,
            "access_lane_offset_m": a["offset"] if a else None,
            "access_lane_class": (a["class"] if a else None),
            "access_lane_length_m": a["length"] if a else None,
        })
        summary["buildings"] += 1
        summary[b["kind"]] += 1
        summary[f"{b['kind']}_floor_area_m2"] += b["floor_area_m2"]
        if a is None:
            summary["unsnapped"] += 1
        else:
            dist_samples.append(a["dist"])
    summary["residential_floor_area_m2"] = round(summary["residential_floor_area_m2"], 1)
    summary["workplace_floor_area_m2"] = round(summary["workplace_floor_area_m2"], 1)

    index = {
        "zone": args.zone,
        "network": network_name,
        "generated_from": args.pbf.rsplit("/", 1)[-1],
        "summary": summary,
        # Additive provenance (the six summary fields above are the contract).
        "other_buildings": sum(1 for b in buildings if b["kind"] == "other"),
        "params": {
            "storey_m": STOREY_M,
            "yes_tower_levels": YES_TOWER_LEVELS,
            "level_defaults": LEVEL_DEFAULTS,
            "min_lane_m": MIN_LANE_M,
            "max_snap_m": MAX_SNAP_M,
            "end_zone_m": END_ZONE_M,
            "end_penalty_m": END_PENALTY_M,
            "excluded_lane_classes": sorted(EXCLUDED_LANE_CLASSES),
            "eligible_lanes": len(ids),
            "net_offset": offset,
        },
        "buildings": entries,
    }
    with open(args.out_index, "w") as f:
        json.dump(index, f)

    # ---- geojson overlay ----
    want = set(k.strip() for k in args.geojson_kinds.split(",") if k.strip())
    feats = []
    for b in buildings:
        if b["kind"] not in want:
            continue
        g = b["utm"]
        if args.simplify_m > 0:
            g = g.simplify(args.simplify_m)
        g = transform(TO_WGS, g)
        geo = mapping(g)
        if "coordinates" not in geo:
            continue  # GeometryCollection degeneracy — nothing to fill
        geo["coordinates"] = round_coords(geo["coordinates"])
        feats.append({
            "type": "Feature",
            "properties": {
                "osm_id": b["osm_id"],
                "kind": b["kind"],
                "levels": b["levels"],
                "floor_area_m2": b["floor_area_m2"],
                "name": b["name"],
            },
            "geometry": geo,
        })
    doc = {"type": "FeatureCollection",
           "comment": f"building footprints in zone {args.zone!r} (OSM, simplified) — "
                      f"kind: residential | workplace | other",
           "features": feats}
    with open(args.out_geojson, "w") as f:
        json.dump(doc, f)

    # ---- report ----
    print(f"index:   {summary['buildings']} buildings "
          f"({summary['residential']} residential, {summary['workplace']} workplace, "
          f"{summary['unsnapped']} unsnapped) -> {args.out_index}")
    print(f"         floor area: residential {summary['residential_floor_area_m2']:,.0f} m2, "
          f"workplace {summary['workplace_floor_area_m2']:,.0f} m2")
    print(f"         building=yes promoted to workplace: {promoted}; "
          f"other (geojson only): {index['other_buildings']}")
    print(f"         levels from tag {lvl_src['tag']}, height {lvl_src['height']}, "
          f"default {lvl_src['default']}")
    print(f"         snapped within {END_ZONE_M:g} m of a lane end: "
          f"{snap_stats['end_snapped']}")
    if dist_samples:
        d = sorted(dist_samples)
        def pct(p):
            return d[min(len(d) - 1, int(p * len(d)))]
        print(f"         snap distance m: min {d[0]:.1f} p25 {pct(.25):.1f} "
              f"med {pct(.5):.1f} p75 {pct(.75):.1f} p95 {pct(.95):.1f} max {d[-1]:.1f}")
    print(f"geojson: {len(feats)} features -> {args.out_geojson}")


if __name__ == "__main__":
    main()
