#!/usr/bin/env python3
r"""Scenario B — "The Merge": an AUTHORED fictitious freeway on-ramp bottleneck.

This file is the durable source. `data/` is gitignored, so nothing under
`data/scenarios/merge-*` is checked in — it is all regenerated from here:

    python3 scripts/demos/merge-pod.py --pod data/scenarios/merge-pod

Why authored rather than imported: the point of the scenario is a jam that
comes from MERGE CAPACITY and nothing else. An OSM extract drags in signals,
side streets, speed-limit guesses and junction geometry that all contribute
to whatever congestion appears, and then the honest claim "this queue is a
shockwave from the merge" cannot be made. A 4.2 km two-lane mainline with one
on-ramp has exactly one bottleneck, and every vehicle that slows down slowed
down because of it.

THE MERGE MODEL (engine/policy.go)
----------------------------------------------------------------------------
The kernel already has the primitive: a lane with `endWall: true` is a dead
end, and `DecideLaneChange` treats a vehicle on one as MANDATORY — the hard
lateral gap drops to Params.MinGapMerge and b_safe is relaxed by
MergeUrgencyGain as the vehicle runs out of MergeZone (200 m) of lane.
`accelEval` additionally puts a virtual standing vehicle at the lane end, so
a vehicle that cannot find a gap decelerates against the wall instead of
driving off it.

So the acceleration lane IS the merge: it is the rightmost lane of the merge
section, it is `endWall`, and it has no successors. Ramp traffic must find a
gap in mainline lane 1 before it runs out. That is the whole bottleneck.

GEOMETRY (local metric frame, +x east, +y north, lane width 3.5 m)
----------------------------------------------------------------------------
    y=+7.0   .................. [X]  third mainline lane (mainline-lane only)
    y=+3.5   ============================================  mainline left  (M1)
    y= 0.0   ============================================  mainline right (M0)
    y=-3.5                          [A]===                 accel lane (endWall)
                                   /
    y=-200   ramp ----o--------- (curve)
                      |
    y=-200            \........................            frontage (variant)
             x=0     2900   4000  4300  4700       5200

The upstream mainline is cut into 250 m sections so the metrics kernel
reports a per-section speed every 60 s: that grid IS the space-time diagram
the shockwave shows up in (scripts/demos/merge-report.py --wave).

VARIANTS
----------------------------------------------------------------------------
  base           two-lane mainline, 300 m acceleration lane, no signals
  mainline-lane  a third mainline lane from 1 km upstream to the exit
  accel-extend   the acceleration lane extended 300 m -> 700 m
  frontage-road  a 50 km/h parallel road taking ramp traffic off the freeway
  ramp-meter     a fixed-time signal on the ramp's internal lane

Every variant shares base's geometry exactly except for what it changes; the
ramp always carries a junction-internal lane (`rmpj_0`) so that ramp-meter
adds a signal program and NOTHING else — same lanes, same lengths, same ids.
"""
import argparse
import json
import math
import os
import shutil

# ---------------------------------------------------------------- constants

LANE_W = 3.5
MAINLINE_LIMIT = 29.06   # m/s ~ 65 mph
RAMP_APPROACH_LIMIT = 22.22   # 80 km/h
RAMP_CURVE_LIMIT = 25.0       # 90 km/h
FRONTAGE_LIMIT = 13.89        # 50 km/h

# Lane slots, keyed by lateral offset. edgeIndex is assigned by sorting the
# slots present on a segment by y (0 = rightmost, SUMO convention).
SLOT_Y = {"A": -LANE_W, "M0": 0.0, "M1": LANE_W, "X": 2 * LANE_W}

# Lane ids are {segment}_{slot suffix}, NOT {segment}_{edgeIndex}. An id has
# to name the same physical lane in every variant: accel-extend adds a lane
# at the accel offset on `aux`, which shifts every edgeIndex on that segment,
# so index-based ids would make `aux_0` the acceleration lane in one arm and
# the right mainline lane in another — and the corridor map, the lane-share
# table and any per-lane A/B comparison would silently compare two different
# roads.
SLOT_SUFFIX = {"A": "a", "M0": "0", "M1": "1", "X": "2"}

# Mainline segmentation: 250 m upstream cells so the metrics kernel reports a
# per-cell speed every 60 s — that grid IS the space-time diagram. 16 cells =
# 4 km of upstream road, sized so the ~10 km/h backward wave (measured) has
# room to run for a full 20-minute cut WITHOUT reaching the origin portal,
# where it would silently stop demand entering (KB: silent-fidelity-failures).
N_UP = 16
CELL = 250.0
MERGE_X = N_UP * CELL                        # 4000: where the ramp arrives


def mainline_segments():
    segs = [(f"up{k:02d}", CELL * k, CELL * (k + 1)) for k in range(N_UP)]
    x = MERGE_X
    segs.append(("mrg", x, x + 300.0))        # acceleration lane lives here
    segs.append(("aux", x + 300.0, x + 700.0))  # accel-extend continues into it
    segs.append(("dn0", x + 700.0, x + 950.0))
    segs.append(("dn1", x + 950.0, x + 1200.0))  # exit
    return segs


# Ramp: a straight 700 m approach, a 20 m junction-internal lane (the meter
# site), then a smoothstep curve tangent to the mainline at the merge point.
RAMP_Y = -200.0
RAMP_A = (900.0, 2900.0)           # approach x-range (2 km of ramp storage)
RAMP_J = (2900.0, 2920.0)          # internal lane x-range (the meter site)
RAMP_B = (2920.0, MERGE_X)         # merge curve x-range
FRONTAGE_X = (2900.0, MERGE_X + 1200.0)
FRONTAGE_CELLS = 4

# The site is fictitious; it is placed in open country in the middle of the
# US so no viewer mistakes it for a real interchange. UTM zone 14, anchor
# 39.5 N 98.5 W = local (0,0).
ANCHOR_LON, ANCHOR_LAT = -98.5, 39.5


# ------------------------------------------------------------------ helpers

def utm_forward(lon_deg, lat_deg, zone):
    """WGS84 lon/lat -> UTM easting/northing (northern hemisphere).

    Snyder's series, the forward of engine/proj.go's inverse. Written out
    rather than taken from pyproj because this script must run wherever the
    repo does and the repo does not take numeric dependencies.
    """
    a, e2, k0 = 6378137.0, 6.69437999014e-3, 0.9996
    ep2 = e2 / (1 - e2)
    lon = math.radians(lon_deg)
    lat = math.radians(lat_deg)
    lon0 = math.radians((zone - 1) * 6 - 180 + 3)
    n = a / math.sqrt(1 - e2 * math.sin(lat) ** 2)
    t = math.tan(lat) ** 2
    c = ep2 * math.cos(lat) ** 2
    aa = math.cos(lat) * (lon - lon0)
    m = a * ((1 - e2 / 4 - 3 * e2**2 / 64 - 5 * e2**3 / 256) * lat
             - (3 * e2 / 8 + 3 * e2**2 / 32 + 45 * e2**3 / 1024) * math.sin(2 * lat)
             + (15 * e2**2 / 256 + 45 * e2**3 / 1024) * math.sin(4 * lat)
             - (35 * e2**3 / 3072) * math.sin(6 * lat))
    east = k0 * n * (aa + (1 - t + c) * aa**3 / 6
                     + (5 - 18 * t + t**2 + 72 * c - 58 * ep2) * aa**5 / 120) + 500000.0
    north = k0 * (m + n * math.tan(lat) * (aa**2 / 2
                  + (5 - t + 9 * c + 4 * c**2) * aa**4 / 24
                  + (61 - 58 * t + t**2 + 600 * c - 330 * ep2) * aa**6 / 720))
    return east, north


def polyline_length(pts):
    return sum(math.hypot(pts[i + 1][0] - pts[i][0], pts[i + 1][1] - pts[i][1])
               for i in range(len(pts) - 1))


def smoothstep_curve(x0, x1, y0, y1, n=40):
    """Horizontal-tangent S-curve from (x0,y0) to (x1,y1).

    Tangency at BOTH ends matters: the ramp arrives exactly parallel to the
    mainline, so the acceleration lane is a genuine parallel lane rather than
    a road stubbed in at an angle.
    """
    pts = []
    for i in range(n + 1):
        t = i / n
        s = t * t * (3 - 2 * t)
        pts.append((x0 + (x1 - x0) * t, y0 + (y1 - y0) * s))
    return pts


def lane(lid, section, edge, edge_index, shape, limit, **kw):
    d = {
        "id": lid,
        "section": section,
        "edge": edge,
        "edgeIndex": edge_index,
        "length": round(polyline_length(shape), 4),
        "speedLimit": limit,
        "width": LANE_W,
        "shape": [[round(x, 3), round(y, 3)] for x, y in shape],
    }
    d.update(kw)
    return d


# ------------------------------------------------------------ network build

def build_network(variant, meter=(2.0, 2.0)):
    """Return the network-format-v1 document for one variant."""
    accel_segs = {"mrg"}
    if variant == "accel-extend":
        accel_segs.add("aux")
    aux_segs = set()
    if variant == "mainline-lane":
        # Starts 1 km upstream of the merge and runs to the exit: an
        # auxiliary through lane, the shape a DOT actually builds at a
        # merge bottleneck.
        aux_segs = {f"up{k:02d}" for k in range(N_UP - 4, N_UP)}
        aux_segs |= {"mrg", "aux", "dn0", "dn1"}

    segs = mainline_segments()
    slots_by_seg = {}
    for name, _, _ in segs:
        slots = ["M0", "M1"]
        if name in accel_segs:
            slots.insert(0, "A")
        if name in aux_segs:
            slots.append("X")
        slots_by_seg[name] = slots

    lanes = []
    # Mainline. Lanes of one segment are straight and share an x-range, so
    # their lengths are bit-identical — CompileNet requires that of lateral
    # neighbours.
    for name, x0, x1 in segs:
        seg_len = round(x1 - x0, 4)
        for idx, slot in enumerate(sorted(slots_by_seg[name], key=lambda s: SLOT_Y[s])):
            y = SLOT_Y[slot]
            lid = f"{name}_{SLOT_SUFFIX[slot]}"
            kw = {}
            if name == "up00" and slot in ("M0", "M1"):
                kw["origin"] = True
            if name == "dn1":
                kw["exit"] = True
            ln = lane(lid, name, name, idx, [(x0, y), (x1, y)], MAINLINE_LIMIT, **kw)
            ln["length"] = seg_len          # exact equality across the edge
            ln["_slot"] = slot
            lanes.append(ln)

    by_seg = {}
    for ln in lanes:
        by_seg.setdefault(ln["section"], {})[ln["_slot"]] = ln

    # Successors: slot -> same slot in the next segment. A slot that does not
    # continue is the merge: `A` becomes a dead end (endWall), which is what
    # makes ramp traffic change lanes.
    order = [name for name, _, _ in segs]
    for i, name in enumerate(order):
        nxt = order[i + 1] if i + 1 < len(order) else None
        for slot, ln in by_seg[name].items():
            if name == "dn1":
                continue                      # exit lane
            tgt = by_seg[nxt].get(slot) if nxt else None
            if tgt is None:
                if slot == "A":
                    ln["endWall"] = True      # the acceleration lane ends
                else:
                    raise SystemExit(f"{ln['id']}: mainline slot {slot} dangles")
            else:
                ln["successors"] = [tgt["id"]]

    # Ramp. rmpj is junction-internal in EVERY variant so that ramp-meter is
    # purely additive: it binds a signal program to a lane that already
    # exists rather than changing the geometry under the comparison.
    rmp_a = lane("rmpa_0", "rmpa", "rmpa", 0,
                 [(RAMP_A[0], RAMP_Y), (RAMP_A[1], RAMP_Y)],
                 RAMP_APPROACH_LIMIT, origin=True)
    rmp_j = lane("rmpj_0", "rmpj", "", 0,
                 [(RAMP_J[0], RAMP_Y), (RAMP_J[1], RAMP_Y)],
                 RAMP_CURVE_LIMIT, internal=True, junction="jramp")
    rmp_b = lane("rmpb_0", "rmpb", "rmpb", 0,
                 smoothstep_curve(RAMP_B[0], RAMP_B[1], RAMP_Y, SLOT_Y["A"]),
                 RAMP_CURVE_LIMIT)
    rmp_j["successors"] = [rmp_b["id"]]
    rmp_b["successors"] = [by_seg["mrg"]["A"]["id"]]
    rmp_a["successors"] = [rmp_j["id"]]
    lanes += [rmp_a, rmp_j, rmp_b]

    if variant == "frontage-road":
        x0, x1 = FRONTAGE_X
        step = (x1 - x0) / FRONTAGE_CELLS
        prev = None
        for k in range(FRONTAGE_CELLS):
            a, b = x0 + step * k, x0 + step * (k + 1)
            kw = {"exit": True} if k == FRONTAGE_CELLS - 1 else {}
            ln = lane(f"frt{k}_0", f"frt{k}", f"frt{k}", 0,
                      [(a, RAMP_Y), (b, RAMP_Y)], FRONTAGE_LIMIT, **kw)
            if prev is not None:
                prev["successors"] = [ln["id"]]
            prev = ln
            lanes.append(ln)
        # The ramp portal now forks: stay on the surface road, or get on the
        # freeway. Successors are ordered left-to-right and the freeway leg
        # curves left, so it is first (= the kernel's unrouted default).
        rmp_a["successors"] = [rmp_j["id"], "frt0_0"]

    signals = []
    if variant == "ramp-meter":
        # A plain fixed-time ramp meter: green/red on a short cycle at the
        # ramp's internal lane. Deliberately NOT tuned against the demand —
        # tuning the meter until it wins is engineering the answer. It has
        # no queue-override either, so if the ramp queue reaches the portal
        # it stays there, which is exactly how an untuned meter fails.
        rmp_j["tl"] = "meter"
        rmp_j["tlLink"] = 0
        signals.append({
            "id": "meter", "junction": "jramp",
            "phases": [{"duration": meter[0], "state": "G"},
                       {"duration": meter[1], "state": "r"}],
        })

    for ln in lanes:
        ln.pop("_slot", None)

    zone = int((ANCHOR_LON + 180) // 6) + 1
    east, north = utm_forward(ANCHOR_LON, ANCHOR_LAT, zone)
    doc = {
        "version": 1,
        "name": f"merge-{variant}",
        "provenance": {
            "source": "authored (scripts/demos/merge-pod.py)",
            "imported": "2026-07-26T00:00:00Z",
            "projection": f"+proj=utm +zone={zone} +ellps=WGS84 +datum=WGS84 +units=m +no_defs",
            "netOffset": [-round(east, 2), -round(north, 2)],
            "notes": ("FICTITIOUS. A synthetic two-lane freeway with one heavy "
                      "on-ramp, authored for Scenario B of the show. The site "
                      "coordinates are open country and correspond to no real road."),
        },
        "lanes": lanes,
    }
    if signals:
        doc["signals"] = signals
    return doc


# ----------------------------------------------------------- scenario build

def demand_yaml(args, variant):
    """The three portal flows (four for frontage-road).

    BOTH portals use `constant` spacing, and that is a measured decision
    rather than a stylistic one. A portal injects at the
    largest speed from which the entering vehicle can still brake behind
    whatever is on the lane (engine/spawn.go injectionPlan); a Poisson burst
    therefore enters as a compressed low-speed platoon, and because
    Car.A = 0.73 m/s² that platoon takes ~600 m to clear. Past roughly
    1300 veh/h per lane the portal never recovers between bursts and its
    throughput COLLAPSES from the requested rate to ~1000 veh/h — measured,
    see docs/show/merge-options.md. Constant headways at the mainline
    portal avoid the burst and hold ~1500 veh/h with full delivery, and the
    ramp portal was moved to constant for the same reason.

    That is a modelling
    choice with a cost — real arrivals are burstier — but the alternative is
    worse: with Poisson ramp arrivals at 1100 veh/h this scenario loses 2-4%
    of its demand to the injection rule rather than to traffic, and a run
    that discards demand for a non-physical reason is exactly what the
    silent-fidelity-failures article says not to measure. Measured, same
    seeds: Poisson ramp 98% delivered / 41.4 km/h, constant ramp 100% / 39.9.

    THE FRONTAGE-ROAD SPLIT IS AN ASSUMPTION, NOT A MEASUREMENT. The engine
    has no route-choice model, so nothing in it decides how many drivers
    prefer a 50 km/h surface road to a queueing freeway ramp. The variant
    therefore splits the ramp flow explicitly: --frontage-share of it gets
    an ADR-0021 weighted destination on the frontage exit, the rest is left
    unrouted and takes the freeway. Read the frontage-road result as
    "what a diversion of this size buys", never as "this is how much
    traffic would divert".
    """
    car = round(1.0 - args.truck_frac, 3)
    rcar = round(1.0 - args.ramp_truck_frac, 3)
    ramp_free = args.ramp_vph
    ramp_frontage = 0.0
    if variant == "frontage-road":
        ramp_frontage = args.ramp_vph * args.frontage_share
        ramp_free = args.ramp_vph - ramp_frontage

    def flow(fid, lid, vph, spacing, tf, cf, dest=None):
        s = (f"  - id: {fid}\n"
             f"    origin: {lid}\n"
             f"    veh_per_h: {vph:g}\n"
             f"    spacing: {spacing}\n"
             f"    vtypes:\n"
             f"      car: {cf:g}\n"
             f"      truck: {tf:g}\n")
        if dest:
            s += f"    destinations:\n      {dest}: 1\n"
        return s

    flows = [
        flow("up00_0", "up00_0", args.mainline_vph / 2, args.mainline_spacing,
             args.truck_frac, car),
        flow("up00_1", "up00_1", args.mainline_vph / 2, args.mainline_spacing,
             args.truck_frac, car),
        flow("rmpa_0", "rmpa_0", ramp_free, args.ramp_spacing,
             args.ramp_truck_frac, rcar),
    ]
    if ramp_frontage:
        flows.append(flow("rmpa_0-frontage", "rmpa_0", ramp_frontage,
                          args.ramp_spacing, args.ramp_truck_frac, rcar,
                          dest=f"frt{FRONTAGE_CELLS-1}_0"))
    head = (
        "# Generated by scripts/demos/merge-pod.py — do not edit by hand.\n"
        f"# {args.mainline_vph:g} veh/h mainline (two portal lanes) "
        f"+ {args.ramp_vph:g} veh/h ramp.\n"
        "# Chosen by the sweep in docs/show/merge-options.md: enough to hold the\n"
        "# merge over capacity for the whole run without the queue reaching the\n"
        "# upstream portal, which would silently stop demand entering.\n")
    if ramp_frontage:
        head += (f"# frontage-road: {100*args.frontage_share:g}% of the ramp flow "
                 f"({ramp_frontage:g} veh/h) is ASSUMED to divert; see the\n"
                 "# demand_yaml docstring in merge-pod.py for why that is an input.\n")
    return head + "format_version: 1\nflows:\n" + "".join(flows)


def metrics_yaml(lane_ids, period_s):
    els = "".join(f"      - {lid}\n" for lid in sorted(lane_ids))
    return (
        "# Generated by scripts/demos/merge-pod.py — do not edit by hand.\n"
        "# Every lane, every 60 s: the per-section interval grid IS the\n"
        "# space-time diagram the shockwave is read off (merge-report.py --wave).\n"
        "format_version: 1\n"
        "trips: {}\n"
        "sets:\n"
        "  - id: all\n"
        "    metrics:\n"
        "      - edie\n"
        "      - occupancy\n"
        "      - stops\n"
        "      - time_loss\n"
        "    window:\n"
        f"      period_s: {period_s:g}\n"
        "      begin_s: 0\n"
        "    elements:\n" + els)


def write_variant(pod, variant, args):
    d = os.path.join(pod, variant)
    os.makedirs(os.path.join(d, "demand"), exist_ok=True)
    os.makedirs(os.path.join(d, "metrics"), exist_ok=True)
    net = build_network(variant, meter=(args.meter_green, args.meter_red))
    with open(os.path.join(d, "network.json"), "w") as f:
        json.dump(net, f, separators=(",", ":"))
        f.write("\n")
    with open(os.path.join(d, "demand", "main.yaml"), "w") as f:
        f.write(demand_yaml(args, variant))
    with open(os.path.join(d, "metrics", "main.yaml"), "w") as f:
        f.write(metrics_yaml([l["id"] for l in net["lanes"]], args.period_s))
    with open(os.path.join(d, "scenario.yaml"), "w") as f:
        f.write(
            "# Generated by scripts/demos/merge-pod.py — do not edit by hand.\n"
            f"# Scenario B \"The Merge\", variant: {variant}\n"
            "format_version: 1\n"
            f"id: {variant}\n"
            "seed: 42\n"
            f"ticks: {args.ticks}\n"
            "network: network.json\n"
            "types:\n"
            "  - car\n"
            "  - truck\n"
            "demand:\n"
            "  - demand/main.yaml\n"
            "metrics:\n"
            "  - metrics/main.yaml\n"
            "# Published on the static-routing baseline (docs/show); the engine\n"
            "# default is adaptive-on since 2026-07-31 (ADR-0036 addendum).\n"
            "params:\n"
            "  adaptive_routing: false\n")
    return d


VARIANTS = ["base", "mainline-lane", "accel-extend", "frontage-road", "ramp-meter"]


def corridors_json(pod, variants):
    """A whatif.py --corridors map over the UNION of every variant's lanes.

    Network mean speed alone cannot judge a ramp meter: metering moves delay
    off the freeway and onto the ramp, and a network mean over both is
    exactly the average that hides it. Tagging the lanes lets whatif.py
    report `corridor:mainline` and `corridor:ramp` beside the headline, so
    the trade the meter is actually making is visible in the same table.
    """
    lanes = {}
    for v in variants:
        with open(os.path.join(pod, v, "network.json")) as f:
            for ln in json.load(f)["lanes"]:
                sec = ln["section"]
                if sec.startswith("up") or sec in ("mrg", "aux", "dn0", "dn1"):
                    # `_a` is the accel slot in every variant (SLOT_SUFFIX).
                    key = "accel" if ln["id"].endswith("_a") else "mainline"
                elif sec.startswith("rmp"):
                    key = "ramp"
                elif sec.startswith("frt"):
                    key = "frontage"
                else:
                    continue
                lanes[ln["id"]] = key
    return {"lanes": lanes}


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--pod", required=True, help="output pod directory")
    ap.add_argument("--variants", default=",".join(VARIANTS))
    ap.add_argument("--mainline-vph", type=float, default=3200.0)
    ap.add_argument("--ramp-vph", type=float, default=1100.0)
    ap.add_argument("--truck-frac", type=float, default=0.06,
                    help="truck share of MAINLINE demand")
    ap.add_argument("--ramp-truck-frac", type=float, default=0.20,
                    help="truck share of RAMP demand; trucks merging from a "
                         "standstill are the merge's dominant disturbance")
    ap.add_argument("--meter-green", type=float, default=2.0,
                    help="ramp-meter green duration (s)")
    ap.add_argument("--meter-red", type=float, default=2.0,
                    help="ramp-meter red duration (s)")
    # 15%, not the 30% this shipped with. The number is an assumption either
    # way — the engine has no route choice — so the only defensible basis is
    # whether the assumption is easy to grant. "Three in ten drivers heading
    # for this on-ramp did not actually need the freeway" is not: they chose
    # a freeway ramp. Halving it costs the option some of its measured
    # benefit and buys a claim an audience can accept without argument, which
    # on a result that is an input rather than a finding is the better trade.
    ap.add_argument("--frontage-share", type=float, default=0.15,
                    help="share of ramp demand ASSUMED to divert onto the "
                         "frontage road (frontage-road variant only)")
    ap.add_argument("--mainline-spacing", default="constant",
                    choices=["constant", "poisson"])
    ap.add_argument("--ramp-spacing", default="constant",
                    choices=["constant", "poisson"])
    ap.add_argument("--ticks", type=int, default=12000)
    ap.add_argument("--period-s", type=float, default=60.0)
    ap.add_argument("--clean", action="store_true", help="remove the pod dir first")
    args = ap.parse_args()

    if args.clean and os.path.isdir(args.pod):
        shutil.rmtree(args.pod)
    os.makedirs(args.pod, exist_ok=True)
    built = []
    for v in args.variants.split(","):
        v = v.strip()
        if v not in VARIANTS:
            raise SystemExit(f"unknown variant {v!r} (known: {', '.join(VARIANTS)})")
        d = write_variant(args.pod, v, args)
        n = json.load(open(os.path.join(d, "network.json")))
        print(f"[merge-pod] {d}: {len(n['lanes'])} lanes, "
              f"{len(n.get('signals', []))} signal programs")
        built.append(v)
    cpath = os.path.join(args.pod, "corridors.json")
    with open(cpath, "w") as f:
        json.dump(corridors_json(args.pod, built), f, indent=1)
    print(f"[merge-pod] {cpath}: lane->corridor map for whatif.py --corridors")


if __name__ == "__main__":
    main()
