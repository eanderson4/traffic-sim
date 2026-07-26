#!/usr/bin/env python3
"""Generate a building-anchored origin–destination demand YAML (ADR-0021).

Where mkdemand.py produces portal inflow with no destinations — every vehicle
born at the map edge, every vehicle leaving by whichever exit it drifts to —
this produces an AM-peak OD program:

  * INBOUND flows on boundary portals (freeways and arterials), rated by OSM
    road class, carrying weighted destinations.
  * RESIDENTIAL flows injected mid-block (ADR-0021 `offset_m`) at the access
    lanes of apartment/residential buildings, rated by residential floor area.
  * DESTINATIONS drawn from workplace-building access lanes, weighted by
    workplace floor area.
  * A piecewise AM profile via `slices`, so the peak builds and decays
    instead of being a step function.

Buildings come from buildings.py's index; the road network supplies lane
lengths and the successor graph for the reachability filter.

Usage:
  mkod.py --buildings data/networks/chi-loop/buildings.json \\
      --network data/networks/chi-loop/chi-loop.json \\
      --portals data/networks/chi-loop/portals.json \\
      --total 12000 --resident-share 0.25 --out demand/main.yaml

Napkin-math posture (inherited from mkdemand.py and the chi-loop README):
rates are peak-hour estimates anchored to published cordon counts, NOT a
calibrated demand model. Floor area is a production/attraction PROXY — there
is no mode share, no car-ownership rate and no parking supply here, and
downtown Chicago's transit share is very high. Absolute rates are a
calibration target; the SHAPE (where trips start and end) is what this
script exists to get right.

Pure stdlib.
"""
import argparse
import collections
import json
import sys

# Peak-hour veh/h per boundary origin LANE by OSM road class — mkdemand.py's
# table, kept identical so the two generators stay comparable.
PORTAL_RATES = {
    "motorway": 1400, "trunk": 900, "primary": 500, "secondary": 300,
    "tertiary": 200, "motorway_link": 500, "trunk_link": 400,
    "primary_link": 300, "secondary_link": 200, "tertiary_link": 150,
}
TRUCK_HEAVY = {"motorway", "trunk", "motorway_link", "trunk_link"}

# Classes --freeway-scale multiplies. Same membership as TRUCK_HEAVY today,
# but kept separate on purpose: that set answers "what carries freight", this
# one answers "what is grade-separated", and the two would diverge the moment
# either table grows. The _link classes MUST be here — the Jane Byrne
# Interchange is built almost entirely of motorway_link lanes, so a filter
# that matched only motorway/trunk left the single most iconic bottleneck in
# the network essentially untouched (measured).
FREEWAY_CLASSES = {"motorway", "trunk", "motorway_link", "trunk_link"}

# AM profile: fraction of the peak-hour rate in each half hour of a 3-hour
# 06:00–09:00 window. Builds to a 07:30–08:30 plateau and decays — the shape
# every urban peak-period count set shows. Mean ≈ 0.76 of peak.
AM_PROFILE = [0.45, 0.70, 1.00, 1.00, 0.80, 0.60]
SLICE_S = 1800.0

# A residential access lane needs enough demand to be worth a flow of its
# own; below this the arrival process is so sparse it is noise.
MIN_RESIDENT_RATE = 4.0
# Injection offsets are clamped away from both lane ends: an injection at a
# junction mouth is exactly what ADR-0021's offset exists to avoid.
MIN_OFFSET_M = 8.0
END_MARGIN_M = 8.0


def reachable_from(succ, starts):
    """Lanes reachable from any of starts through the successor graph."""
    seen = set(starts)
    q = collections.deque(starts)
    while q:
        for v in succ.get(q.popleft(), ()):
            if v not in seen:
                seen.add(v)
                q.append(v)
    return seen


def can_reach(preds, dest):
    """Lanes that can reach dest through the successor graph (reverse BFS).

    This is SUCCESSOR-ONLY reachability — deliberately the same relation the
    kernel's route table uses (engine/routing.go), so a destination this
    filter accepts is one the kernel can actually steer to. It is
    conservative: with ADR-0021 lateral recovery a vehicle can also change
    lanes back onto a reachable lane, so some rejected pairs would in fact
    work.
    """
    R = {dest}
    q = collections.deque([dest])
    while q:
        for p in preds.get(q.popleft(), ()):
            if p not in R:
                R.add(p)
                q.append(p)
    return R


def emit_flow(out, fid, origin, rate, profile, vtypes, dests, offset=None):
    out.append(f"  - id: {fid}")
    out.append(f"    origin: {origin}")
    if offset is not None:
        out.append(f"    offset_m: {offset:.1f}")
    out.append("    spacing: poisson")
    out.append("    slices:")
    for i, f in enumerate(profile):
        r = rate * f
        if r <= 0:
            continue
        out.append(f"      - {{start_s: {i * SLICE_S:.0f}, end_s: {(i + 1) * SLICE_S:.0f}, veh_per_h: {r:.1f}}}")
    if vtypes:
        out.append("    vtypes:")
        for k in sorted(vtypes):
            out.append(f"      {k}: {vtypes[k]}")
    if dests:
        # %.4f rounds any share below 5e-5 to 0.0000, which the scenario
        # loader rejects outright ("weight must be > 0") — a whole demand
        # file lost to one negligible destination. Drop those instead: a
        # weight that small draws ~never, so omitting it changes nothing
        # except that the file stays loadable. Emitting zero would be the
        # only outcome that is neither faithful nor valid.
        kept = {k: v for k, v in dests.items() if round(v, 4) > 0}
        if not kept:
            raise SystemExit(
                f"flow {fid}: every destination weight rounds to 0 at 4 dp "
                f"(max {max(dests.values()):.2e}) — nothing to emit")
        dropped = len(dests) - len(kept)
        if dropped:
            print(f"note: flow {fid}: dropped {dropped} destination(s) with "
                  f"weight < 5e-5 (would emit 0.0000 and fail load)",
                  file=sys.stderr)
        out.append("    destinations:")
        for lane in sorted(kept):
            out.append(f"      {lane}: {kept[lane]:.4f}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--buildings", required=True)
    ap.add_argument("--network", required=True)
    ap.add_argument("--portals", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--total", type=float, default=12000,
                    help="target peak-hour veh/h across all flows")
    ap.add_argument("--resident-share", type=float, default=0.25,
                    help="fraction of demand originating INSIDE the zone "
                         "(residential buildings) vs entering at a portal")
    ap.add_argument("--dest-lanes", type=int, default=120,
                    help="how many workplace access lanes become destinations. "
                         "Each costs one memoized next-hop table in the kernel "
                         "(~4 bytes/lane, and since ADR-0021 a lateral-depth "
                         "table too: ~444 KB per destination on a 56k-lane "
                         "network), so "
                         "this is a real memory knob, not just a fidelity one.")
    ap.add_argument("--origin-lanes", type=int, default=150,
                    help="how many residential access lanes become interior origins")
    ap.add_argument("--truck", type=float, default=0.08)
    ap.add_argument("--freeway-scale", type=float, default=1.0,
                    help="multiply boundary inflow on grade-separated classes "
                         "(motorway/trunk and their links) by this, ON TOP of "
                         "the --total scaling. Default 1.0 = unchanged. --total "
                         "alone cannot congest the freeways and the arterial "
                         "grid at the same time; with this set, --total sizes "
                         "the arterial side and freeway demand is added on top, "
                         "so the realized grand total exceeds --total (it is "
                         "printed to stderr)")
    ap.add_argument("--through-share", type=float, default=0.45,
                    help="fraction of NON-freeway portal inflow that is "
                         "THROUGH traffic — destined for a boundary exit "
                         "rather than a workplace inside the zone. The "
                         "remainder terminates downtown as before. Residential "
                         "origins are never through traffic (a resident "
                         "driving to work is by definition not passing "
                         "through), so this applies to portals only.")
    ap.add_argument("--freeway-through-share", type=float, default=0.75,
                    help="--through-share for grade-separated origins, which "
                         "run much higher: the Kennedy carries ~250k AADT past "
                         "downtown and only a minority of it exits into the "
                         "Loop. Setting this equal to --through-share collapses "
                         "the distinction.")
    ap.add_argument("--exit-lanes", type=int, default=80,
                    help="how many boundary exit lanes become through-traffic "
                         "destinations, taken by class capacity. Costs the same "
                         "per-destination route table as --dest-lanes.")
    ap.add_argument("--flat-peak", action="store_true",
                    help="emit ONE constant slice at the profile's peak rate "
                         "instead of the AM ramp. For recording a watchable "
                         "cut under peak conditions: a recording covers the "
                         "whole run from tick 0, so a 15-minute store of the "
                         "ramped scenario would only ever capture 06:00-06:15.")
    args = ap.parse_args()

    with open(args.buildings) as f:
        bidx = json.load(f)
    with open(args.network) as f:
        net = json.load(f)
    with open(args.portals) as f:
        portals = json.load(f)

    lanes = {l["id"]: l for l in net["lanes"]}
    succ = {l["id"]: l.get("successors", []) for l in net["lanes"]}
    preds = collections.defaultdict(list)
    for lid, ss in succ.items():
        for s in ss:
            preds[s].append(lid)

    # --- Destinations: workplace floor area pooled by access lane.
    work = collections.defaultdict(float)
    res = collections.defaultdict(float)
    res_offset = {}
    for b in bidx["buildings"]:
        lane = b.get("access_lane")
        if not lane or lane not in lanes:
            continue
        if b["kind"] == "workplace":
            work[lane] += b["floor_area_m2"]
        elif b["kind"] == "residential":
            res[lane] += b["floor_area_m2"]
            # The largest building on a lane sets the injection point.
            cur = res_offset.get(lane)
            if cur is None or b["floor_area_m2"] > cur[1]:
                res_offset[lane] = (b.get("access_lane_offset_m") or 0.0, b["floor_area_m2"])

    dest_lanes = [l for l, _ in sorted(work.items(), key=lambda kv: -kv[1])[:args.dest_lanes]]
    dest_area = {l: work[l] for l in dest_lanes}
    print(f"destinations: {len(dest_lanes)} lanes, "
          f"{100 * sum(dest_area.values()) / sum(work.values()):.0f}% of workplace floor area",
          file=sys.stderr)

    # --- Through-traffic destinations: boundary EXIT lanes.
    #
    # Without these the demand program has no mass balance. Every flow this
    # script emitted — freeway portals included — terminated at one of the
    # workplace lanes above, so the whole 27,000 veh/h of inflow drained
    # through ~120 lanes in the densest part of the grid. Measured at 18,000
    # ticks: 1,112 trips completed against 5,675 still circulating at the
    # horizon. That is not congestion, it is a bathtub with the taps open —
    # vehicle count rises monotonically and no equilibrium exists at ANY
    # horizon, which is why the corridor speeds looked right at 6,000 ticks
    # and had collapsed to 20.5 km/h by 18,000.
    #
    # Real expressway traffic past a downtown is dominantly THROUGH traffic:
    # the Kennedy carries ~250k AADT alongside the Loop and only a minority
    # exits into the CBD. Routing a share of portal inflow to a boundary exit
    # closes the balance, and it does so with a trip that CROSSES the box in
    # a few minutes instead of one that dies in the grid — so freeway trips
    # actually complete inside a recordable horizon and become measurable.
    exit_cap = {}
    exit_edge = {}
    for e in portals["exits"]:
        cls = e.get("class")
        if e.get("fragment") or cls not in PORTAL_RATES or e["id"] not in lanes:
            continue
        exit_cap[e["id"]] = float(PORTAL_RATES[cls])
        exit_edge[e["id"]] = str(e.get("edge", "")).lstrip("-")
    # Freeway exits are taken in full before arterial ones fill the rest: a
    # freeway origin that cannot reach a freeway exit has nowhere plausible
    # to go, so that pool must never be the part that gets truncated.
    fw_exits = sorted(e for e in exit_cap
                      if e in {x["id"] for x in portals["exits"]
                               if x.get("class") in FREEWAY_CLASSES})
    other = sorted((e for e in exit_cap if e not in set(fw_exits)),
                   key=lambda e: (-exit_cap[e], e))
    exit_lanes = fw_exits + other[:max(0, args.exit_lanes - len(fw_exits))]
    fw_exit_set = set(fw_exits)
    print(f"exits: {len(exit_lanes)} through-traffic destination lanes "
          f"({len(fw_exits)} grade-separated)", file=sys.stderr)

    # Reverse reachability per destination — the filter that keeps a flow
    # from being handed a destination its vehicles can never steer to.
    reach = {d: can_reach(preds, d) for d in dest_lanes}
    for d in exit_lanes:
        reach[d] = can_reach(preds, d)

    def dests_for(origin):
        w = {d: dest_area[d] for d in dest_lanes if origin in reach[d]}
        tot = sum(w.values())
        if tot <= 0:
            return {}
        return {d: v / tot for d, v in w.items()}

    def through_for(origin, cls, edge):
        """Normalized boundary-exit weights for a portal origin.

        Freeway origins are held to freeway exits — an interstate's through
        movement leaves on an interstate, not by filtering onto a side
        street. Exits on the origin's own edge are dropped: that pair is a
        U-turn straight back out the way it came in.
        """
        pool = fw_exit_set if cls in FREEWAY_CLASSES else exit_lanes
        w = {d: exit_cap[d] for d in pool
             if origin in reach[d] and exit_edge[d] != edge}
        tot = sum(w.values())
        if tot <= 0:
            return {}
        return {d: v / tot for d, v in w.items()}

    def blend(work_d, thru_d, share):
        """Mix workplace and through destinations at `share` through.

        Degenerate pools collapse the mix rather than losing demand: an
        origin that can reach no exit sends everything downtown, and one
        that can reach no workplace sends everything out.
        """
        if not thru_d:
            share = 0.0
        elif not work_d:
            share = 1.0
        out = collections.defaultdict(float)
        for d, v in work_d.items():
            out[d] += (1.0 - share) * v
        for d, v in thru_d.items():
            out[d] += share * v
        return dict(out)

    profile = [max(AM_PROFILE)] if args.flat_peak else AM_PROFILE
    flows = []
    stats = collections.Counter()

    # --- Portal inflow: commuters and freight entering the zone.
    portal_share = 1.0 - args.resident_share
    entries = []
    for p in portals["origins"]:
        cls = p.get("class")
        if cls not in PORTAL_RATES or p.get("fragment"):
            stats["portal skipped"] += 1
            continue
        entries.append((p["id"], cls, PORTAL_RATES[cls],
                        str(p.get("edge", "")).lstrip("-")))
    portal_raw = sum(r for _, _, r, _ in entries)
    portal_scale = (args.total * portal_share / portal_raw) if portal_raw else 0

    # --freeway-scale exists because ONE scalar cannot congest both road
    # systems. --total scales every class by the same portal_scale, and the
    # grid saturates far below the freeways: at --total 16000 the factor is
    # ~0.24, so the Kennedy's two boundary origin lanes injected 337 veh/h
    # each — a sixth of freeway capacity — and it ran at 72 km/h through a
    # simulated AM peak. Raising --total until the freeways bite needs
    # ~67,000, which buries the arterial grid long before it gets there.
    # Measured: the six corridors the research names as Chicago's real
    # problem areas carried 3.3% of network delay while the side streets
    # took 94.7%.
    #
    # So the freeway classes get their own multiplier on top of the common
    # scale. This deliberately breaks --total as a grand total: it is the
    # ARTERIAL target, and freeway demand is added on top. The realized
    # totals are reported below rather than left implicit, because a flag
    # that silently invalidates another flag's meaning is a trap.
    realized = collections.Counter()
    for i, (lid, cls, raw, edge) in enumerate(sorted(entries)):
        share = (args.freeway_through_share if cls in FREEWAY_CLASSES
                 else args.through_share)
        thru = through_for(lid, cls, edge)
        d = blend(dests_for(lid), thru, share)
        if not d:
            stats["portal unreachable"] += 1
            continue
        truck = args.truck if cls in TRUCK_HEAVY else args.truck / 2
        rate = raw * portal_scale
        if cls in FREEWAY_CLASSES:
            rate *= args.freeway_scale
            realized["freeway"] += rate
        else:
            realized["arterial"] += rate
        realized["through"] += rate * (share if thru else 0.0)
        if not thru:
            stats["portal no reachable exit"] += 1
        emit_flow(flows, f"p{i:03d}-{cls}", lid, rate, profile,
                  {"car": round(1 - truck, 3), "truck": round(truck, 3)}, d)
        stats["portal flows"] += 1
    if args.freeway_scale != 1.0:
        print(f"[mkod] freeway-scale {args.freeway_scale}: portal inflow "
              f"{realized['arterial']:.0f} veh/h arterial + "
              f"{realized['freeway']:.0f} veh/h freeway = "
              f"{realized['arterial'] + realized['freeway']:.0f} veh/h "
              f"(--total {args.total:.0f} sized the base; the arterial side is "
              f"unchanged from freeway-scale 1.0 and the freeway side is "
              f"{args.freeway_scale}x on top)",
              file=sys.stderr)
    portal_tot = realized["arterial"] + realized["freeway"]
    if portal_tot:
        print(f"[mkod] through traffic: {realized['through']:.0f} of "
              f"{portal_tot:.0f} veh/h portal inflow "
              f"({100 * realized['through'] / portal_tot:.0f}%) exits at the "
              f"boundary; the rest terminates in-zone. Through trips CLOSE "
              f"the mass balance — without them inflow has no drain and "
              f"vehicle count grows without bound.", file=sys.stderr)

    # --- Residential interior origins: residents leaving their building.
    top_res = sorted(res.items(), key=lambda kv: -kv[1])[:args.origin_lanes]
    res_raw = sum(a for _, a in top_res)
    res_scale = (args.total * args.resident_share / res_raw) if res_raw else 0

    for i, (lid, area) in enumerate(sorted(top_res)):
        rate = area * res_scale
        if rate < MIN_RESIDENT_RATE:
            stats["resident below min rate"] += 1
            continue
        lane = lanes[lid]
        # Clamp the offset inside the lane, away from both ends.
        hi = lane["length"] - END_MARGIN_M
        if hi <= MIN_OFFSET_M:
            stats["resident lane too short"] += 1
            continue
        off = min(max(res_offset[lid][0], MIN_OFFSET_M), hi)
        d = dests_for(lid)
        if not d:
            stats["resident unreachable"] += 1
            continue
        # Residents are cars: a tower's garage does not emit semis.
        emit_flow(flows, f"r{i:03d}-resident", lid, rate, profile,
                  {"car": 1.0}, d, offset=off)
        stats["resident flows"] += 1

    header = [
        "# Generated by scripts/chicago/mkod.py — building-anchored OD demand",
        "# (ADR-0021). Portal inflow + residential interior origins, both",
        f"# routed to workplace destinations weighted by floor area.",
        f"# Target {args.total:.0f} veh/h peak, {args.resident_share:.0%} originating in-zone.",
        f"# {stats['portal flows']} portal flows + {stats['resident flows']} residential flows,",
        f"# Through share {args.through_share:.0%} arterial / "
        f"{args.freeway_through_share:.0%} freeway portals — that fraction of"
        + " portal inflow is destined for a boundary EXIT rather than a",
        f"# workplace, which is what closes the mass balance.",
        f"# {len(dest_lanes)} workplace + {len(exit_lanes)} exit destination"
        f" lanes. Profile {profile} per half hour"
        + (" (FLAT PEAK — a recording cut, not the AM ramp)." if args.flat_peak else "."),
        "format_version: 1",
        "flows:",
    ]
    with open(args.out, "w") as f:
        f.write("\n".join(header + flows) + "\n")

    for k in sorted(stats):
        print(f"  {k}: {stats[k]}", file=sys.stderr)
    print(f"wrote {args.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
