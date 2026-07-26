#!/usr/bin/env python3
"""Rank congestion by STREET NAME from an M13 metrics JSON (ADR-0014 §6).

scorecard.py reports the worst lanes by id (`n24203057_1_0`), which is the
right primitive and the wrong unit for a human: nobody recognizes a lane id,
and a single street is split across dozens of them by netconvert. This
resolves lanes back to their OSM way names and aggregates there, so the
output is "N Michigan Ave, 41 min of delay" instead of a list of hashes.

Lane → edge is in the network file; edge → OSM way is netimport's id
convention (the way id, optionally with a leading `-` for the reversed
direction and a `#N` split suffix); way → name/highway comes from the OSM
extract the network was imported from.

Usage:
  congestion.py metrics.json --network chi-loop.json --osm loop.osm \\
      [--top 20] [--by-interval] [--occupancy car=1.2,truck=1.0] \\
      [--corridors corridors.json]

Reports:
  * network totals (VMT, VHT, mean speed, delay share, arrivals)
  * delay PER PERSON and total PERSON-HOURS of delay (see below)
  * the worst streets by total time loss, with their mean speed, the
    share of the network's delay each carries, the delay an individual
    driver loses crossing them, and their person-hour cost
  * the worst individual lanes, named
  * with --corridors, a NAMED-CORRIDOR table (the Kennedy, the Dan Ryan,
    the Eisenhower, the Stevenson, Lake Shore Drive, the Jane Byrne
    Interchange) built by scripts/chicago/corridors.py — the unit a
    validation question is actually asked in. Ranked by delay per lane-km
    rather than by total delay, because the corridors differ in length by
    13x and a raw total just ranks them by size.
  * with --by-interval, how the worst streets evolve across the run — the
    peak-building view a static ranking hides

--------------------------------------------------------------------------
PERSON-DELAY
--------------------------------------------------------------------------
The metric kernel measures VEHICLE delay. The civic question (VISION.md use
case 4 — "this street costs commuters N person-hours every morning") is
about PEOPLE, and the conversion needs an occupancy assumption that the
simulation does not contain. It is therefore an explicit flag, it is printed
with every report, and it is never applied silently:

  --occupancy car=1.2,truck=1.0     (the default)

1.2 is the usual US work-trip auto occupancy (NHTS work-trip vehicle
occupancy sits at ~1.1-1.2; all-purpose travel is ~1.5-1.7, which is the
WRONG number for an AM commute peak). Trucks are one driver. A vehicle type
absent from the map is counted at 1.0 and named in the output.

Two different quantities are reported and they answer different questions:

  * mean delay per person — what a randomly chosen PERSON experiences.
    Every occupant of a vehicle experiences that vehicle's full delay, so
    this is the occupancy-WEIGHTED mean of per-vehicle delay, not the
    per-vehicle delay divided by anything. With a uniform occupancy it
    equals mean delay per vehicle; the two diverge only when the type mix
    and the occupancy assumption disagree.
  * person-hours of delay — the aggregate social cost, delay summed over
    occupants. This is the number that scales with the size of the city and
    the one worth quoting.

Per-street person-hours are the street's vehicle delay times the FLEET mean
occupancy: interval records are not typed, so a per-street type mix is not
available. On a network whose truck share is near-uniform the error is
small; where a street is disproportionately truck (a dock approach, a
freeway) its person-hours are overstated by at most the car/truck occupancy
ratio.

Per-street vehicle counts are Edie traversal-equivalents (sum_dist_m over
lane length), not distinct vehicles: a vehicle that crosses half a lane
counts half. That is the right denominator for "seconds lost per driver
crossing this street" and it is what the interval records can support.

Pure stdlib.
"""
import argparse
import collections
import json
import re
import sys
import xml.etree.ElementTree as ET

# netimport edge id → OSM way id: optional leading '-' (reversed direction)
# and optional '#N' split suffix.
EDGE_RE = re.compile(r"^-?(\d+)(?:#\d+)?$")


def way_attrs(osm_path):
    """way id → (name, highway) from an OSM XML extract, streaming."""
    out = {}
    for _, el in ET.iterparse(osm_path, events=("end",)):
        if el.tag != "way":
            continue
        tags = {t.get("k"): t.get("v") for t in el.findall("tag")}
        if "highway" in tags:
            out[el.get("id")] = (tags.get("name") or "", tags["highway"])
        el.clear()
    return out


def lane_street(lanes, ways):
    """lane id → (street label, highway class). Unnamed ways fall back to a
    class label rather than being dropped: unnamed links and ramps carry
    real delay and hiding them would flatter the network."""
    out = {}
    for l in lanes:
        if l.get("internal"):
            continue
        m = EDGE_RE.match(l.get("edge", ""))
        name, hwy = ("", "")
        if m:
            name, hwy = ways.get(m.group(1), ("", ""))
        label = name or (f"<unnamed {hwy}>" if hwy else "<unknown>")
        out[l["id"]] = (label, hwy)
    return out


DEFAULT_OCCUPANCY = "car=1.2,truck=1.0"


def parse_occupancy(spec):
    """"car=1.2,truck=1.0" -> {"car": 1.2, "truck": 1.0}. Strict: a malformed
    entry or a non-positive value is an error, not a silent default — the
    whole point of the flag is that the assumption is visible."""
    out = {}
    for part in spec.split(","):
        part = part.strip()
        if not part:
            continue
        if "=" not in part:
            raise SystemExit(f"--occupancy: expected type=value, got {part!r}")
        name, _, val = part.partition("=")
        try:
            occ = float(val)
        except ValueError:
            raise SystemExit(f"--occupancy: {name}: {val!r} is not a number")
        if not (occ > 0) or occ != occ or occ == float("inf"):
            raise SystemExit(f"--occupancy: {name}: occupancy must be positive and finite")
        out[name.strip()] = occ
    if not out:
        raise SystemExit("--occupancy: empty")
    return out


def person_delay(trips, occ_map):
    """Person-delay statistics over COMPLETED trips.

    Returns (person_hours, mean_s_per_person, mean_s_per_vehicle, n_trips,
             fleet_mean_occupancy, unknown_types).

    Every occupant of a vehicle experiences that vehicle's full delay, so
    person-seconds = occupancy x vehicle delay, and the mean a PERSON
    experiences is the occupancy-weighted mean of vehicle delay.
    """
    person_s = 0.0
    veh_s = 0.0
    persons = 0.0
    n = 0
    unknown = collections.Counter()
    for t in trips:
        if not t.get("completed"):
            continue
        occ = occ_map.get(t.get("type", ""))
        if occ is None:
            unknown[t.get("type", "")] += 1
            occ = 1.0
        d = t.get("time_loss_s", 0.0)
        person_s += occ * d
        veh_s += d
        persons += occ
        n += 1
    return (person_s / 3600.0,
            person_s / persons if persons > 0 else 0.0,
            veh_s / n if n > 0 else 0.0,
            n,
            persons / n if n > 0 else 0.0,
            unknown)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("metrics")
    ap.add_argument("--network", required=True)
    ap.add_argument("--osm", required=True)
    ap.add_argument("--top", type=int, default=20)
    ap.add_argument("--by-interval", action="store_true")
    ap.add_argument("--corridors", default="",
                    help="corridors.json from scripts/chicago/corridors.py: "
                         "adds a named-corridor table (Kennedy, Dan Ryan, "
                         "Eisenhower, Stevenson, Lake Shore Drive, Jane Byrne)")
    ap.add_argument("--occupancy", default=DEFAULT_OCCUPANCY,
                    help="persons per vehicle by type, e.g. "
                         f"{DEFAULT_OCCUPANCY!r} (the default). The simulation "
                         "measures VEHICLE delay; this is the assumption that "
                         "converts it to PERSON delay, and it is printed with "
                         "every report. US work-trip auto occupancy is ~1.1-1.2.")
    args = ap.parse_args()
    occ_map = parse_occupancy(args.occupancy)

    with open(args.metrics) as f:
        m = json.load(f)
    with open(args.network) as f:
        net = json.load(f)
    ways = way_attrs(args.osm)
    street = lane_street(net["lanes"], ways)
    lane_len = {l["id"]: l["length"] for l in net["lanes"]}

    corridor_of, corridor_label, corridor_km = {}, {}, collections.Counter()
    if args.corridors:
        with open(args.corridors) as f:
            cdoc = json.load(f)
        corridor_of = cdoc["lanes"]
        corridor_label = cdoc["labels"]
        # Lane-km of the whole corridor, not just the lanes that saw traffic:
        # the denominator must not shrink when a corridor runs empty.
        for lid, ck in corridor_of.items():
            corridor_km[ck] += lane_len.get(lid, 0.0) / 1000.0

    tot = m["totals"]
    dt = m["dt"]
    vmt, vht, loss = tot["vmt"], tot["vht"], tot["total_time_loss_s"]

    # One measurement set only — aggregating across overlapping sets would
    # double-count (the scorecard.py rule, kept).
    lanes_by_set, sets_with_tl = collections.defaultdict(set), set()
    for iv in m["intervals"]:
        lanes_by_set[iv["set_id"]].add(iv["lane_id"])
        if "time_loss_s" in iv:
            sets_with_tl.add(iv["set_id"])
    if not lanes_by_set:
        print("no interval records in this metrics file", file=sys.stderr)
        return
    pool = sets_with_tl or set(lanes_by_set)
    set_id = max(pool, key=lambda s: (len(lanes_by_set[s]), s))

    st_loss = collections.Counter()
    st_dist = collections.Counter()
    st_time = collections.Counter()
    st_stops = collections.Counter()
    st_lanes = collections.defaultdict(set)
    st_veh = collections.Counter()   # Edie traversal-equivalents
    lane_loss = collections.Counter()
    co_loss = collections.Counter()
    co_dist = collections.Counter()
    co_time = collections.Counter()
    co_veh = collections.Counter()
    co_stops = collections.Counter()
    co_lanes = collections.defaultdict(set)
    per_interval = collections.defaultdict(collections.Counter)

    for iv in m["intervals"]:
        if iv["set_id"] != set_id:
            continue
        lid = iv["lane_id"]
        s = street.get(lid)
        if s is None:
            continue  # junction interior: delay belongs to its approach
        label = s[0]
        tl = iv.get("time_loss_s", 0.0)
        dist = iv.get("sum_dist_m", 0.0)
        st_loss[label] += tl
        st_dist[label] += dist
        st_time[label] += iv.get("sum_time_s", 0.0)
        st_stops[label] += iv.get("stops", 0)
        st_lanes[label].add(lid)
        # Edie: vehicle-traversals of the lane = travelled distance / length.
        # Guard a zero/absent length rather than dividing by it.
        ll = lane_len.get(lid, 0.0)
        if ll > 0:
            st_veh[label] += dist / ll
        lane_loss[lid] += tl
        ck = corridor_of.get(lid)
        if ck is not None:
            co_loss[ck] += tl
            co_dist[ck] += dist
            co_time[ck] += iv.get("sum_time_s", 0.0)
            co_stops[ck] += iv.get("stops", 0)
            co_lanes[ck].add(lid)
            if ll > 0:
                co_veh[ck] += dist / ll
        if args.by_interval:
            per_interval[iv["begin_tick"]][label] += tl

    print(f"=== {args.metrics}")
    print(f"horizon {m['ticks']} ticks = {m['ticks']*dt/3600:.2f} sim hours (dt={dt})")
    print(f"trips: {tot['completed_trips']:,} completed, {tot['active_at_horizon']:,} still moving at the horizon")
    print(f"VMT {vmt/1000:,.0f} km   VHT {vht/3600:,.0f} h   mean speed {3.6*vmt/vht:.1f} km/h"
          if vht > 0 else "VHT is zero")
    print(f"delay {loss/3600:,.0f} h ({100*loss/vht:.0f}% of all time in the network), "
          f"mean {tot['mean_time_loss_s']:.0f} s/vehicle")
    if tot.get("dropped_crossings"):
        print(f"dropped_crossings {tot['dropped_crossings']:,} "
              f"(metric-attribution integrity signal, not a sim failure)")

    # --- person-delay. ASSUMPTION, stated every time (see module docstring).
    ph, per_person_s, per_veh_s, n_trips, fleet_occ, unknown = person_delay(
        m.get("trips", []), occ_map)
    if n_trips == 0:
        # Nothing completed: fall back to the unweighted mean of the map so the
        # per-street person-hour column is still meaningful, and say so.
        fleet_occ = sum(occ_map.values()) / len(occ_map)
    occ_str = ", ".join(f"{k}={v:g}" for k, v in sorted(occ_map.items()))
    print(f"\n--- person-delay  [ASSUMPTION: occupancy {occ_str}; "
          f"fleet mean {fleet_occ:.3f} persons/veh]")
    if unknown:
        print("  WARNING: no occupancy given for type(s) "
              + ", ".join(f"{t!r} ({c:,} trips)" for t, c in unknown.most_common())
              + " — counted at 1.0")
    if n_trips == 0:
        print("  no completed trips: no person-delay to report "
              f"(per-street person-hours use the unweighted mean {fleet_occ:.3f})")
    else:
        print(f"  completed trips {n_trips:,}")
        # One decimal: with a near-uniform occupancy the two means are nearly
        # equal BY CONSTRUCTION, and rounding them to the same integer reads
        # as a bug rather than as the arithmetic it is.
        print(f"  mean delay per vehicle {per_veh_s:8.1f} s ({per_veh_s/60:.1f} min)"
              "   [completed trips only]")
        print(f"  mean delay per person  {per_person_s:8.1f} s ({per_person_s/60:.1f} min)"
              "   [occupancy-weighted: every occupant loses the whole delay]")
        print(f"  total person-hours of delay {ph:,.0f} over "
              f"{m['ticks']*dt/3600:.2f} sim hours")
        # The civic framing: cost per morning, extrapolated at the run's rate.
        print(f"  = {ph/(m['ticks']*dt/3600):,.0f} person-hours per sim hour")

    print(f"\n--- worst {args.top} streets by total delay (set {set_id})")
    print(f"  [person-h = street vehicle delay x fleet mean occupancy "
          f"{fleet_occ:.3f}; veh = Edie traversal-equivalents]")
    print(f"{'street':30s} {'delay h':>8s} {'% net':>6s} {'km/h':>6s} "
          f"{'veh':>9s} {'s/veh':>7s} {'person-h':>9s} {'lanes':>6s} {'stops':>8s}")
    for label, tl in st_loss.most_common(args.top):
        t, d = st_time[label], st_dist[label]
        kmh = 3.6 * d / t if t > 0 else 0
        veh = st_veh[label]
        s_per_veh = tl / veh if veh > 0 else 0.0
        print(f"{label[:30]:30s} {tl/3600:8.1f} {100*tl/loss:5.1f}% {kmh:6.1f} "
              f"{veh:9,.0f} {s_per_veh:7.1f} {tl*fleet_occ/3600:9,.0f} "
              f"{len(st_lanes[label]):6d} {int(st_stops[label]):8,d}")

    if corridor_of:
        print(f"\n--- NAMED CORRIDORS (ranked by delay per lane-km)")
        print(f"  [corridor lane-km is the WHOLE corridor, so an empty one "
              f"scores 0 rather than dividing by nothing]")
        print(f"{'corridor':42s} {'delay h':>8s} {'% net':>6s} {'h/lane-km':>10s} "
              f"{'km/h':>6s} {'veh':>9s} {'s/veh':>7s} {'person-h':>9s} {'lanes':>6s}")
        rows = []
        for ck, lab in corridor_label.items():
            tl = co_loss.get(ck, 0.0)
            km = corridor_km.get(ck, 0.0)
            rows.append((tl / km if km > 0 else 0.0, ck, lab, tl, km))
        for per_km, ck, lab, tl, km in sorted(rows, reverse=True):
            t, d = co_time.get(ck, 0.0), co_dist.get(ck, 0.0)
            kmh = 3.6 * d / t if t > 0 else 0
            veh = co_veh.get(ck, 0.0)
            print(f"{lab[:42]:42s} {tl/3600:8.1f} {100*tl/loss:5.1f}% "
                  f"{per_km/3600:10.2f} {kmh:6.1f} {veh:9,.0f} "
                  f"{(tl/veh if veh > 0 else 0):7.1f} {tl*fleet_occ/3600:9,.0f} "
                  f"{len(co_lanes.get(ck, ())):6d}")
        rest = loss - sum(co_loss.values())
        print(f"{'(everything else)':42s} {rest/3600:8.1f} {100*rest/loss:5.1f}%")

    print(f"\n--- worst {args.top} individual lanes by delay")
    for lid, tl in lane_loss.most_common(args.top):
        label, hwy = street.get(lid, ("?", "?"))
        print(f"  {label[:30]:30s} {lid:26s} {hwy:14s} {lane_len.get(lid,0):6.0f} m {tl/3600:7.2f} h")

    if args.by_interval:
        worst = [s for s, _ in st_loss.most_common(8)]
        print(f"\n--- delay (h) per interval for the worst 8 streets")
        hdr = "".join(f"{s[:11]:>12s}" for s in worst)
        print(f"{'from (sim h)':>12s}{hdr}")
        for begin in sorted(per_interval):
            row = "".join(f"{per_interval[begin][s]/3600:12.2f}" for s in worst)
            print(f"{begin*dt/3600:12.2f}{row}")


if __name__ == "__main__":
    main()
