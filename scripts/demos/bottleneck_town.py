#!/usr/bin/env python3
"""Author "Bottleneck Town" — a fictitious small-town main-street corridor —
and the six-arm what-if pod built on it.

    scripts/demos/bottleneck_town.py --out data/pods/bottleneck-town

WHAT THIS IS
--------------------------------------------------------------------------
Everything under data/ is gitignored, so THIS FILE is the durable source
for the scenario. It writes network-format-v1 JSON (engine/netfile.go),
ADR-0012 scenario directories, ADR-0014 §5 metrics parts and director
demand parts — a complete pod that `scripts/whatif.py --pod` can run.

THE TOWN
--------------------------------------------------------------------------
Main Street runs west→east and sags 300 m south through the middle of the
map: the old road bends around the town site. Four signalized cross-street
intersections (J1..J4, 480/520/450 m apart) sit on the straight middle
section. All through traffic funnels down it. The congestion is SIGNAL
CAPACITY congestion — the corridor carries roughly 0.8-0.9 of its signal
capacity, so it queues at red and discharges on green, rather than being
overwhelmed by raw volume.

Junction model, and the simplifications taken deliberately:

  * Main Street is 2 lanes per direction (the classic American "stroad"
    main street). Right lane = through+right, left lane = through+left.
    No turn bays: the whole approach is 2 lanes, so the kernel's lateral
    route guidance (routeLatDepth) has the full block to sort vehicles.
  * Cross streets are 1 lane per direction.
  * SPLIT PHASING on the cross streets and a PROTECTED-ONLY left phase on
    Main Street. Four green phases per cycle, each internally
    conflict-free by construction:
        1  Main through + right, both directions
        2  Main protected lefts, both directions
        3  Cross street southbound, all movements
        4  Cross street northbound, all movements
    Each green is followed by 3 s amber and 2 s all-red. Because no two
    movements in a phase ever cross or merge, foesCross/foesMerge are left
    empty exactly as the netimport-produced networks leave them, and the
    signal plus the kernel's box-exit check do all the adjudication.
  * No U-turns.

THE OPTIONS
--------------------------------------------------------------------------
  add-lane          Main Street 2 -> 3 lanes per direction, junction
                    internals authored to match (not cloned).
  bypass-north      a new 1-lane-each-way road from BW to BE across the
                    INSIDE of Main Street's bend. 3656 m against Main
                    Street's 3842 m between the same two points.
  connector-south   a new 1-lane-each-way road between the same two
                    points around the OUTSIDE of the bend, crossing all
                    four south cross-street legs on the way. 4386 m.
  retime-short      cycle 86 s -> 66 s, same phase proportions.
  green-wave        same phases and same durations, offsets set for an
                    eastbound progression at 45 km/h.

WHY THE TWO NEW ROADS ARE PLACED LIKE THAT
--------------------------------------------------------------------------
The engine routes on STATIC SHORTEST PATH BY DISTANCE (engine/routing.go):
a reverse Dijkstra over lane lengths, computed once, with no congestion
feedback and no re-routing. A new road therefore carries traffic if and
only if it shortens some O-D pair's distance, and it carries ALL of that
pair's traffic when it does. There is no equilibrium assignment and no
detour tolerance. Any bypass longer than the road it relieves is a
guaranteed no-op FOR A ROUTING REASON, not a traffic reason.

So the two new-road options are placed to make that legible instead of
hiding it: bypass-north cuts the chord of Main Street's bend and is
shorter; connector-south goes round the outside and is longer, but it is
wired into all four south cross-street legs so it still shortens the
south-side local O-D pairs and is not a strawman. --check prints the
shortest path each arm's router will actually choose.
"""
import argparse
import json
import math
import os
import heapq

# ----------------------------------------------------------------- geometry

LANE_W = 3.5
MAIN_SPEED = 50 / 3.6          # 50 km/h
CROSS_SPEED = 40 / 3.6         # 40 km/h
NEWROAD_SPEED = 50 / 3.6       # 50 km/h — same posted limit as Main Street
TURN_SPEED = 25 / 3.6          # junction-internal turning movements
CLEAR = 1.0                    # stop-line setback past the crossing carriageway


def sub(a, b):
    return (a[0] - b[0], a[1] - b[1])


def add(a, b):
    return (a[0] + b[0], a[1] + b[1])


def scale(a, k):
    return (a[0] * k, a[1] * k)


def norm(a):
    return math.hypot(a[0], a[1])


def unit(a):
    n = norm(a)
    return (a[0] / n, a[1] / n)


def left_normal(u):
    """Left-hand normal of a unit travel direction."""
    return (-u[1], u[0])


def plen(pts):
    return sum(norm(sub(pts[i + 1], pts[i])) for i in range(len(pts) - 1))


def cum_s(pts):
    out = [0.0]
    for i in range(len(pts) - 1):
        out.append(out[-1] + norm(sub(pts[i + 1], pts[i])))
    return out


def pt_at_s(pts, s):
    cs = cum_s(pts)
    if s <= 0:
        return pts[0]
    if s >= cs[-1]:
        return pts[-1]
    for i in range(len(pts) - 1):
        if cs[i + 1] >= s:
            t = (s - cs[i]) / (cs[i + 1] - cs[i])
            return (pts[i][0] + t * (pts[i + 1][0] - pts[i][0]),
                    pts[i][1] + t * (pts[i + 1][1] - pts[i][1]))
    return pts[-1]


def s_of_point(pts, p):
    """Arc length of the projection of p onto the polyline (p is on it)."""
    cs = cum_s(pts)
    best, best_s = None, 0.0
    for i in range(len(pts) - 1):
        a, b = pts[i], pts[i + 1]
        ab = sub(b, a)
        L2 = ab[0] ** 2 + ab[1] ** 2
        t = ((p[0] - a[0]) * ab[0] + (p[1] - a[1]) * ab[1]) / L2
        t = max(0.0, min(1.0, t))
        q = (a[0] + t * ab[0], a[1] + t * ab[1])
        d = norm(sub(q, p))
        if best is None or d < best:
            best, best_s = d, cs[i] + t * math.sqrt(L2)
    return best_s


def fillet(pts, radius, segs):
    """Round every interior vertex of a polyline with a circular arc.

    Edges are STRAIGHT by construction in this file (network-format v1
    requires lateral neighbours in one edge to be the same length, which a
    mitred offset through a bend violates), so a road bend becomes a chain
    of short straight edges. Rounding it first keeps the per-edge lateral
    offsets from stepping visibly at the joins.
    """
    if len(pts) < 3:
        return list(pts)
    out = [pts[0]]
    for i in range(1, len(pts) - 1):
        p, c, n = pts[i - 1], pts[i], pts[i + 1]
        u1, u2 = unit(sub(c, p)), unit(sub(n, c))
        cosang = max(-1.0, min(1.0, u1[0] * u2[0] + u1[1] * u2[1]))
        turn = math.acos(cosang)
        if turn < 1e-6:
            out.append(c)
            continue
        r = min(radius, 0.4 * norm(sub(c, p)), 0.4 * norm(sub(n, c)))
        tan = r * math.tan(turn / 2)
        a = add(c, scale(u1, -tan))
        b = add(c, scale(u2, tan))
        out.append(a)
        for k in range(1, segs):
            t = k / segs
            # quadratic Bezier a->c->b approximates the arc closely enough
            # at the bend angles used here (<= 40 deg).
            m = (1 - t) ** 2
            out.append(((1 - t) ** 2 * a[0] + 2 * (1 - t) * t * c[0] + t * t * b[0],
                        (1 - t) ** 2 * a[1] + 2 * (1 - t) * t * c[1] + t * t * b[1]))
        out.append(b)
    out.append(pts[-1])
    # drop coincident points
    ded = [out[0]]
    for q in out[1:]:
        if norm(sub(q, ded[-1])) > 1e-6:
            ded.append(q)
    return ded


def bezier_link(ps, ds, pe, de, n=8):
    """Junction-internal centreline from (point, direction) to (point,
    direction): a quadratic Bezier through the tangent intersection, or a
    straight line when the tangents are parallel."""
    det = ds[0] * de[1] - ds[1] * de[0]
    r = sub(pe, ps)
    if abs(det) < 1e-9:
        return [ps, pe]
    t = (r[0] * de[1] - r[1] * de[0]) / det
    if t <= 0.05:
        return [ps, pe]
    c = add(ps, scale(ds, t))
    pts = []
    for k in range(n + 1):
        u = k / n
        pts.append(((1 - u) ** 2 * ps[0] + 2 * (1 - u) * u * c[0] + u * u * pe[0],
                    (1 - u) ** 2 * ps[1] + 2 * (1 - u) * u * c[1] + u * u * pe[1]))
    return pts


# -------------------------------------------------------------- network bits

# The town is fictitious, but every consumer that puts a network on a map
# projects it through the network-format-v1 frame descriptor (projection +
# netOffset — engine/proj.go, viz/src/proj.ts), and `bake` REFUSES a
# network without one. Local (0,0) is pinned to UTM zone 14N easting
# 542866.14 / northing 4394580.49 — open farmland in north-central Kansas,
# ~22 km north of the sibling freeway-merge demo so the two invented
# networks never overlap on the map, and chosen because there is no real
# road network there for an invented one to be confused with.
PROJECTION = "+proj=utm +zone=14 +ellps=WGS84 +datum=WGS84 +units=m +no_defs"
NET_OFFSET = [-542866.14, -4394580.49]
FICTITIOUS = ("FICTITIOUS. Authored by hand, not imported: this geometry "
              "corresponds to no real road anywhere.")


class Net:
    def __init__(self, name):
        self.name = name
        self.lanes = []
        self.signals = []
        self.by_id = {}

    def add(self, lane):
        assert lane["id"] not in self.by_id, lane["id"]
        self.by_id[lane["id"]] = lane
        self.lanes.append(lane)
        return lane

    def doc(self, note):
        return {
            "version": 1,
            "name": self.name,
            "provenance": {
                "source": "scripts/demos/bottleneck_town.py (authored fictitious network)",
                "projection": PROJECTION,
                "netOffset": NET_OFFSET,
                "notes": note,
            },
            "lanes": self.lanes,
            "signals": self.signals,
        }


class Lanes:
    """One directed edge's lanes: ids plus the geometry the junction
    builder needs (end point and travel direction at each end)."""

    def __init__(self, ids, start_pts, end_pts, dir_start, dir_end):
        self.ids = ids
        self.start_pts = start_pts
        self.end_pts = end_pts
        self.dir_start = dir_start
        self.dir_end = dir_end


def add_edge(net, eid, section, a, b, nlanes, speed, origin=False,
             exit_=False):
    """One straight edge, nlanes lanes.

    KNOWN DEFECT — the lane indexing here is MIRRORED, and the fix is not a
    one-liner. `nrm` is the LEFT normal and the offset is negative, so index
    0 sits nearest the CENTRELINE and the highest index at the kerb. That is
    the inverse of `engine/netfile.go`'s edgeIndex contract ("0 = rightmost",
    SUMO convention), which the sibling merge-pod.py follows.

    What it costs, confirmed on the generated network: the junction builder
    keys off the contract (`right_lane=0`, left bay at the highest index), so
    the right turn is issued from the lane nearest the centreline and crosses
    the through lane beside it INSIDE the shared `main_thru` green, and the
    two opposed protected lefts cross each other inside `main_left`. These
    junctions ship with `foesCross`/`foesMerge` empty on the explicit grounds
    that the phases are conflict-free by construction, so nothing arbitrates
    those crossings. Measured impact is small — 0-1 collision observations on
    an idle run, and every arm carries the same defect, so the A/B deltas
    stand — but it is a latent conflict source and it is visible in the baked
    replay: right-turners swing out of the inside lane.

    Why the obvious fix (`off = -(nlanes-i-0.5)*LANE_W`) is NOT enough, tested
    2026-07-27: it does correct the movement assignment, but `chain()` maps
    lane i to lane i and the flared approach has one extra lane, so every
    chained lane acquires a 3.50 m — one full lane width — lateral jog at the
    bay edge. The flare can only widen KERB-ward: the forward carriageway
    runs -305.25..-301.75 about an axis at -300 and the opposing direction
    owns the other side, so a lane added toward the centreline would overlap
    oncoming traffic. Under a corrected index the added lane is therefore
    necessarily kerbside — a RIGHT-turn pocket — whereas this scenario is
    built around a LEFT-turn bay with no upstream predecessor (see the
    Junction docstring: it is what keeps every through lane's leftmost
    successor a through movement, which the Successors[0] routing fallback
    depends on).

    So correcting this properly means redesigning the bay and re-validating
    that routing property, then re-running the pod. Tracked rather than
    rushed.
    """
    u = unit(sub(b, a))
    nrm = left_normal(u)
    length = norm(sub(b, a))
    ids, sp, ep = [], [], []
    for i in range(nlanes):
        off = -(i + 0.5) * LANE_W
        pa = add(a, scale(nrm, off))
        pb = add(b, scale(nrm, off))
        lid = f"{eid}_{i}"
        net.add({
            "id": lid, "section": section, "edge": eid, "edgeIndex": i,
            "length": round(length, 3), "speedLimit": round(speed, 4),
            "width": LANE_W,
            "shape": [[round(pa[0], 3), round(pa[1], 3)],
                      [round(pb[0], 3), round(pb[1], 3)]],
            "successors": [],
            **({"origin": True} if origin else {}),
            **({"exit": True} if exit_ else {}),
        })
        ids.append(lid)
        sp.append(pa)
        ep.append(pb)
    return Lanes(ids, sp, ep, u, u)


def chain(net, upstream, downstream):
    """lane i of upstream -> lane i of downstream (equal lane counts)."""
    for i, lid in enumerate(upstream.ids):
        j = min(i, len(downstream.ids) - 1)
        net.by_id[lid]["successors"].append(downstream.ids[j])


FLARE_LEN = 180.0   # length of the left-turn bay at a signalized approach


def build_piece(net, name, piece_pts, nlanes, speed, origin, exit_,
                flare_extra=0):
    """A run of road between two junctions: one straight edge per segment.

    flare_extra > 0 carves the last FLARE_LEN metres off into their own
    edge with that many extra lanes on the LEFT — the left-turn bay. The
    bay lane has no upstream successor on purpose: a vehicle reaches it by
    changing lanes (routeLatDepth steers routed vehicles into it), which is
    what keeps every through lane's leftmost successor a through movement.
    """
    pts = list(piece_pts)
    flare_pts = None
    if flare_extra > 0:
        total = plen(pts)
        if total > FLARE_LEN + 20:
            cut = total - FLARE_LEN
            cs = cum_s(pts)
            head = [q for i, q in enumerate(pts) if cs[i] < cut - 1e-6]
            head.append(pt_at_s(pts, cut))
            tail = [pt_at_s(pts, cut)]
            tail += [q for i, q in enumerate(pts) if cs[i] > cut + 1e-6]
            pts, flare_pts = head, tail
        else:
            flare_pts = None
            flare_extra = 0
    edges = []
    for j in range(len(pts) - 1):
        e = add_edge(net, f"{name}_s{j}", name, pts[j], pts[j + 1],
                     nlanes, speed,
                     origin=(origin and j == 0),
                     exit_=(exit_ and flare_pts is None
                            and j == len(pts) - 2))
        if edges:
            chain(net, edges[-1], e)
        edges.append(e)
    if flare_pts is not None:
        for j in range(len(flare_pts) - 1):
            e = add_edge(net, f"{name}_b{j}", name, flare_pts[j],
                         flare_pts[j + 1], nlanes + flare_extra, speed)
            chain(net, edges[-1], e)
            edges.append(e)
    return edges[0], edges[-1]


class Road:
    """A two-way corridor cut by junctions.

    cuts: list of (junction_id, point_on_axis, half_extent_m) in any order.
    After build(), .fwd_in[jid] / .fwd_out[jid] / .rev_in[jid] / .rev_out[jid]
    hold the Lanes objects the junction builder wires together.
    """

    def __init__(self, net, name, axis, nlanes, speed, cuts,
                 fwd_origin=True, fwd_exit=True, rev_origin=True,
                 rev_exit=True, flare=None):
        self.net, self.name, self.axis = net, name, axis
        self.nlanes, self.speed = nlanes, speed
        self.cuts = sorted(((s_of_point(axis, p), jid, half)
                            for jid, p, half in cuts))
        self.flare = flare or {}
        self.fwd_in, self.fwd_out = {}, {}
        self.rev_in, self.rev_out = {}, {}
        self._build(fwd_origin, fwd_exit, rev_origin, rev_exit)

    def _pieces(self):
        cs = cum_s(self.axis)
        bounds = [0.0]
        for s, _, half in self.cuts:
            bounds += [s - half, s + half]
        bounds.append(cs[-1])
        out = []
        for k in range(0, len(bounds), 2):
            s0, s1 = bounds[k], bounds[k + 1]
            if s1 <= s0 + 1.0:
                # The road TERMINATES at this junction (its axis endpoint is
                # a cut): there is no piece on that side, so the junction
                # simply has no approach/exit for this direction.
                out.append(None)
                continue
            pts = [pt_at_s(self.axis, s0)]
            for i, si in enumerate(cs):
                if s0 + 1e-6 < si < s1 - 1e-6:
                    pts.append(self.axis[i])
            pts.append(pt_at_s(self.axis, s1))
            out.append(pts)
        return out

    def _build(self, fo, fe, ro, re):
        pieces = self._pieces()
        n = len(pieces)
        fwd, rev = [], []
        for i, pts in enumerate(pieces):
            if pts is None:
                fwd.append(None)
                continue
            fl = self.flare.get(self.cuts[i][1], 0) if i < len(self.cuts) else 0
            fwd.append(build_piece(self.net, f"{self.name}_f{i}", pts,
                                   self.nlanes, self.speed,
                                   origin=(fo and i == 0),
                                   exit_=(fe and i == n - 1),
                                   flare_extra=fl))
        for i, pts in enumerate(pieces):
            if pts is None:
                rev.append(None)
                continue
            rp = list(reversed(pts))
            fl = self.flare.get(self.cuts[i - 1][1], 0) if i > 0 else 0
            rev.append(build_piece(self.net, f"{self.name}_r{i}", rp,
                                   self.nlanes, self.speed,
                                   origin=(ro and i == n - 1),
                                   exit_=(re and i == 0),
                                   flare_extra=fl))
        for c, (s, jid, half) in enumerate(self.cuts):
            self.fwd_in[jid] = fwd[c][1] if fwd[c] else None
            self.fwd_out[jid] = fwd[c + 1][0] if fwd[c + 1] else None
            self.rev_in[jid] = rev[c + 1][1] if rev[c + 1] else None
            self.rev_out[jid] = rev[c][0] if rev[c] else None


# -------------------------------------------------------------- junctions

def turn_kind(din, dout):
    """'through' | 'left' | 'right' | 'u' from travel directions."""
    ang = math.degrees(math.atan2(din[0] * dout[1] - din[1] * dout[0],
                                  din[0] * dout[0] + din[1] * dout[1]))
    if abs(ang) <= 45:
        return "through"
    if abs(ang) >= 150:
        return "u"
    return "left" if ang > 0 else "right"


class Leg:
    """One arm of a junction.

    thru/left_lane/right_lane say WHICH approach lane carries which
    movement. Getting this right is not cosmetic: pickSuccessor falls back
    to Successors[0] — the LEFTMOST successor — whenever the route table
    cannot resolve (which happens for every right-turn destination when the
    vehicle is sitting in a left lane, because lane changes are not
    successors). If the leftmost successor of a through lane is a left
    turn, that fallback sends through traffic round the corner and parks it
    in the protected-left queue. Every through lane here therefore has its
    THROUGH movement leftmost, and the left turn lives in a bay lane of its
    own whose only successor is the left turn.
    """

    def __init__(self, name, inbound, outbound, thru=None, left_lane=None,
                 right_lane=0):
        self.name = name
        self.inbound = inbound     # Lanes approaching the junction (may be None)
        self.outbound = outbound   # Lanes leaving the junction (may be None)
        n = len(inbound.ids) if inbound else 0
        self.thru = n if thru is None else thru
        self.left_lane = (n - 1) if left_lane is None else left_lane
        self.right_lane = right_lane


def build_junction(net, jid, legs, rows=None, speed=TURN_SPEED,
                   lane_rules=None):
    """Wire every non-U movement between the legs as an internal lane.

    Lane assignment (index 0 = rightmost):
      through  lane i -> outbound lane min(i, nout-1)
      right    lane 0 -> outbound lane 0
      left     lane K-1 -> outbound lane nout-1

    rows: optional {(from_leg, to_leg): "major"|"minor"|"stop"} for
    unsignalized junctions. Returns [(from_leg, kind, internal_lane_id)].
    """
    made = []
    idx = 0
    for a in legs:
        if a.inbound is None:
            continue
        K = len(a.inbound.ids)
        for b in legs:
            if b is a or b.outbound is None:
                continue
            kind = turn_kind(a.inbound.dir_end, b.outbound.dir_start)
            if kind == "u":
                continue
            nout = len(b.outbound.ids)
            if lane_rules and (a.name, b.name) in lane_rules:
                pairs = lane_rules[(a.name, b.name)]
            elif kind == "through":
                pairs = [(i, min(i, nout - 1)) for i in range(a.thru)]
            elif kind == "right":
                pairs = [(a.right_lane, 0)]
            else:
                pairs = [(a.left_lane, nout - 1)]
            for fi, ti in pairs:
                ps, pe = a.inbound.end_pts[fi], b.outbound.start_pts[ti]
                shape = bezier_link(ps, a.inbound.dir_end, pe,
                                    b.outbound.dir_start)
                lid = f"i{jid}_{a.name}{fi}_{b.name}{ti}"
                lane = {
                    "id": lid, "section": f"j:{jid}", "edgeIndex": idx,
                    "length": round(plen(shape), 3),
                    "speedLimit": round(speed, 4),
                    "width": LANE_W,
                    "shape": [[round(p[0], 3), round(p[1], 3)] for p in shape],
                    "successors": [b.outbound.ids[ti]],
                    "internal": True, "junction": jid,
                }
                if kind == "through":
                    lane["speedLimit"] = round(max(speed, MAIN_SPEED * 0.8), 4)
                if rows is not None:
                    lane["row"] = rows.get((a.name, b.name), "major")
                net.add(lane)
                net.by_id[a.inbound.ids[fi]]["successors"].append(lid)
                made.append((a.name, kind, lid, b.name))
                idx += 1
    # Successor order decides the DEFAULT, and the default is not a detail.
    #
    # network-format v1 documents successors as "ordered left-to-right
    # (first = leftmost = default route)", and pickSuccessor takes
    # Successors[0] whenever the route table cannot resolve — which happens
    # for every destination that is unreachable from the vehicle's CURRENT
    # LANE, because lane changes are not successors. Straight left-to-right
    # ordering therefore sends a vehicle that merely needs to change lanes
    # round the leftmost corner instead. Measured on the first build of this
    # scenario: with the bypass leaving Main Street to the left at BW, ALL
    # 170 pooled W->N trips (5% of demand) were swallowed by the bypass and
    # none reached a north cross street; the arm's headline effect was
    # inflated by traffic that never made its turn.
    #
    # So: the movement that CONTINUES ON THE SAME ROAD is hoisted to index 0
    # and the rest keep left-to-right order. The deviation costs the HeldTurn
    # convention (Intent.HeldTurn +1/-1 name Successors[0]/[n-1], so +1 no
    # longer means "left" at a junction whose through movement is not the
    # leftmost); nothing in this scenario issues HeldTurn — the default
    # driver steers with the Route axis — and a wrong default is a routing
    # bug in every run, where a wrong HeldTurn is a bug in no run.
    for a in legs:
        if a.inbound is None:
            continue
        road_in = net.by_id[a.inbound.ids[0]]["section"].split("_")[0]
        for lid in a.inbound.ids:
            succs = net.by_id[lid]["successors"]
            if len(succs) < 2:
                continue

            def key(sid):
                sh = net.by_id[sid]["shape"]
                d = unit(sub(tuple(sh[-1]), tuple(sh[0])))
                bearing = math.atan2(
                    a.inbound.dir_end[0] * d[1] - a.inbound.dir_end[1] * d[0],
                    a.inbound.dir_end[0] * d[0] + a.inbound.dir_end[1] * d[1])
                exit_lane = net.by_id[sid]["successors"][0]
                same = net.by_id[exit_lane]["section"].split("_")[0] == road_in
                return (0 if same else 1, -bearing)
            succs.sort(key=key)
    return made


def merge_foes(net, made):
    """Declare foesMerge among internals that share an exit lane.

    Only used at UNSIGNALIZED junctions. Two internal lanes funnelling into
    one exit lane with nothing between them is an unarbitrated merge, and
    the kernel books the resulting overlaps as collisions: the box-occupancy
    half of boxBlocked is what serializes them, and it reads foesMerge. At a
    signalized junction the phase plan does the same job, so the foe sets
    stay empty there exactly as the netimport networks leave them.
    """
    groups = {}
    for _leg, _kind, lid, _to in made:
        groups.setdefault(net.by_id[lid]["successors"][0], []).append(lid)
    for ids in groups.values():
        if len(ids) < 2:
            continue
        for lid in ids:
            net.by_id[lid]["foesMerge"] = [x for x in ids if x != lid]


# --------------------------------------------------------------- the town

MAIN_AXIS_RAW = [(-500, 0), (0, 0), (450, -300), (2300, -300), (2750, 0),
                 (3250, 0)]
JX = [700, 1180, 1700, 2150]          # x of J1..J4 on the straight middle
JY = -300
NORTH_PORTAL_Y = -20
CS_Y = -750                            # connector-south crossing the S legs
SOUTH_PORTAL_Y = -900
BW = (-250.0, 0.0)
BE = (3000.0, 0.0)
BYPASS_AXIS_RAW = [BW, (800, 200), (1950, 200), BE]
CONN_AXIS_RAW = [BW, (400, CS_Y), (2400, CS_Y), BE]

# signal timing (seconds)
AMBER, ALLRED = 3.0, 2.0
GREENS_BASE = {"main_thru": 36.0, "main_left": 8.0, "cross_s": 11.0,
               "cross_n": 11.0}
GREENS_SHORT = {"main_thru": 24.0, "main_left": 6.0, "cross_s": 8.0,
                "cross_n": 8.0}
PHASE_ORDER = ["main_thru", "main_left", "cross_s", "cross_n"]
WAVE_SPEED = 45 / 3.6

# Where connector-south crosses the four cross streets. Two through roads
# meeting at grade with no signal is the configuration a traffic engineer
# would not sign off on, and the earlier build did worse than that: it made
# the NEW road the priority leg, so four existing arterials had to yield to
# it. Priority control in this engine is a real gap-acceptance yield
# (engine/rightofway.go), not a formality — minor traffic waits for a gap it
# can take without braking harder than comfortable — so that grant was worth
# real time to the connector and taken straight out of the cross streets.
#
# Signalised instead, on the SAME policy the rest of the town runs: identical
# 86 s cycle, identical 3 s amber and 2 s all-red, fixed-time, no offset. Two
# phases because a crossing of two single-lane roads has two, and the split
# is even because splitting it by demand would mean tuning the option that is
# being judged. The cost — four new signals to delay at — is not a penalty
# invented for this arm; it is what building this road actually entails, and
# the reason bypass-north (which crosses nothing) is a different proposition
# rather than the same one facing the other way.
CS_GREENS = {"con": 38.0, "cross": 38.0}
CS_PHASE_ORDER = ["con", "cross"]


def main_axis():
    return fillet(MAIN_AXIS_RAW, 150, 4)


def signal_program(jid, greens, offset, groups, nlinks, order=None):
    """One green phase per group, each followed by amber then all-red."""
    phases = []
    for g in (order or PHASE_ORDER):
        on = groups[g]
        phases.append({"duration": greens[g],
                       "state": "".join("G" if i in on else "r"
                                        for i in range(nlinks))})
        phases.append({"duration": AMBER,
                       "state": "".join("y" if i in on else "r"
                                        for i in range(nlinks))})
        phases.append({"duration": ALLRED, "state": "r" * nlinks})
    sig = {"id": jid, "junction": jid, "phases": phases}
    if offset:
        sig["offset"] = round(offset, 2)
    return sig


def build_town(variant):
    """Build one arm's complete network."""
    main_lanes = 3 if variant == "add-lane" else 2
    has_byp = variant == "bypass-north"
    has_con = variant == "connector-south"
    greens = GREENS_SHORT if variant == "retime-short" else GREENS_BASE
    cycle = sum(greens.values()) + 4 * (AMBER + ALLRED)

    net = Net(f"bottleneck-town-{variant}")
    axis = main_axis()

    main_half = main_lanes * LANE_W          # half the main carriageway width
    cross_half = LANE_W                      # half a 1+1 cross street
    new_half = LANE_W

    cuts = [(f"J{k+1}", (JX[k], JY), cross_half + CLEAR) for k in range(4)]
    if has_byp or has_con:
        cuts.append(("BW", BW, new_half + CLEAR))
        cuts.append(("BE", BE, new_half + CLEAR))
    # one extra lane on each signalized approach: the left-turn bay
    flare = {f"J{k+1}": 1 for k in range(4)}
    main = Road(net, "main", axis, main_lanes, MAIN_SPEED, cuts, flare=flare)

    # cross streets, fwd = southbound
    cross = []
    for k in range(4):
        x = JX[k]
        caxis = [(x, NORTH_PORTAL_Y), (x, SOUTH_PORTAL_Y)]
        ccuts = [(f"J{k+1}", (x, JY), main_half + CLEAR)]
        if has_con:
            ccuts.append((f"CS{k+1}", (x, CS_Y), new_half + CLEAR))
        cross.append(Road(net, f"cross{k+1}", caxis, 1, CROSS_SPEED, ccuts))

    # signalized junctions
    for k in range(4):
        jid = f"J{k+1}"
        c = cross[k]
        legs = [
            # main approaches are flared: lanes 0..K-1 through (lane 0 also
            # right), lane K the left-turn bay
            Leg("W", main.fwd_in[jid], main.rev_out[jid],
                thru=main_lanes, left_lane=main_lanes),      # from the west
            Leg("E", main.rev_in[jid], main.fwd_out[jid],
                thru=main_lanes, left_lane=main_lanes),      # from the east
            Leg("N", c.fwd_in[jid], c.rev_out[jid]),         # from the north
            Leg("S", c.rev_in[jid], c.fwd_out[jid]),         # from the south
        ]
        made = build_junction(net, jid, legs)
        groups = {g: set() for g in PHASE_ORDER}
        for link, (leg, kind, lid, _to) in enumerate(made):
            net.by_id[lid]["tl"] = jid
            net.by_id[lid]["tlLink"] = link
            if leg in ("W", "E"):
                groups["main_left" if kind == "left" else "main_thru"].add(link)
            else:
                groups["cross_n" if leg == "N" else "cross_s"].add(link)
        offset = 0.0
        if variant == "green-wave":
            d = 0.0
            for j in range(k):
                d += JX[j + 1] - JX[j]
            offset = (d / WAVE_SPEED) % cycle
        net.signals.append(signal_program(jid, greens, offset, groups,
                                          len(made)))

    # ---- new roads -----------------------------------------------------
    if has_byp:
        byp = Road(net, "byp", fillet(BYPASS_AXIS_RAW, 300, 4), 1,
                   NEWROAD_SPEED,
                   [("BW", BW, main_half + CLEAR),
                    ("BE", BE, main_half + CLEAR)],
                   fwd_origin=False, fwd_exit=False,
                   rev_origin=False, rev_exit=False)
        _wire_t(net, "BW", main, byp, "out")
        _wire_t(net, "BE", main, byp, "in")

    if has_con:
        con = Road(net, "con", fillet(CONN_AXIS_RAW, 300, 4), 1, NEWROAD_SPEED,
                   [("BW", BW, main_half + CLEAR),
                    ("BE", BE, main_half + CLEAR)] +
                   [(f"CS{k+1}", (JX[k], CS_Y), LANE_W + CLEAR)
                    for k in range(4)],
                   fwd_origin=False, fwd_exit=False,
                   rev_origin=False, rev_exit=False)
        _wire_t(net, "BW", main, con, "out")
        _wire_t(net, "BE", main, con, "in")
        for k in range(4):
            jid = f"CS{k+1}"
            c = cross[k]
            legs = [
                Leg("N", c.fwd_in[jid], c.rev_out[jid]),
                Leg("S", c.rev_in[jid], c.fwd_out[jid]),
                Leg("W", con.fwd_in[jid], con.rev_out[jid]),
                Leg("E", con.rev_in[jid], con.fwd_out[jid]),
            ]
            made = build_junction(net, jid, legs)
            groups = {g: set() for g in CS_PHASE_ORDER}
            for link, (leg, _kind, lid, _to) in enumerate(made):
                net.by_id[lid]["tl"] = jid
                net.by_id[lid]["tlLink"] = link
                groups["con" if leg in ("W", "E") else "cross"].add(link)
            net.signals.append(signal_program(jid, CS_GREENS, 0.0, groups,
                                              len(made),
                                              order=CS_PHASE_ORDER))
    return net, cycle


def _wire_t(net, jid, main, new, side):
    """The T where a new road meets Main Street.

    side="out": the new road DIVERGES eastbound here (Main's west end).
    side="in":  the new road REJOINS eastbound here (Main's east end).
    The new road is the priority leg at the merge — a bypass carries the
    trunk route — so the merging Main Street movement is 'minor'.
    """
    if side == "out":
        legs = [
            Leg("W", main.fwd_in[jid], main.rev_out[jid]),
            Leg("E", main.rev_in[jid], main.fwd_out[jid]),
            Leg("P", new.rev_in[jid], new.fwd_out[jid]),
        ]
    else:
        legs = [
            Leg("W", main.fwd_in[jid], main.rev_out[jid]),
            Leg("E", main.rev_in[jid], main.fwd_out[jid]),
            Leg("P", new.fwd_in[jid], new.rev_out[jid]),
        ]
    rows = {}
    for a in ("W", "E", "P"):
        for b in ("W", "E", "P"):
            rows[(a, b)] = "major"
    # whichever Main Street through movement shares an exit lane with the
    # new road's inbound movement yields to it.
    made = build_junction(net, jid, legs, rows=rows)
    merge_foes(net, made)
    # The new road is the priority leg: whichever Main Street movement
    # shares an exit lane with it gives way.
    tgt = {}
    for leg, kind, lid, to in made:
        tgt.setdefault(net.by_id[lid]["successors"][0], []).append((leg, lid))
    for _exit_lane, group in tgt.items():
        if not any(l == "P" for l, _ in group):
            continue
        for leg, lid in group:
            if leg != "P":
                net.by_id[lid]["row"] = "minor"


# ------------------------------------------------------------------ demand

def portals(net):
    """Named origin/exit lane groups, keyed by portal name."""
    o, e = {}, {}
    for L in net.lanes:
        sec = L["section"]
        if L.get("origin"):
            o.setdefault(_portal_name(sec, "in"), []).append(L["id"])
        if L.get("exit"):
            e.setdefault(_portal_name(sec, "out"), []).append(L["id"])
    return o, e


def _portal_name(section, io):
    """section is "<road>_<dir><piece>", e.g. "main_f0" or "cross3_r2"."""
    road, tail = section.split("_")
    fwd = tail[0] == "f"
    if road == "main":
        # fwd = eastbound: its origin is the W portal, its exit the E portal
        if fwd:
            return "W" if io == "in" else "E"
        return "E" if io == "in" else "W"
    k = road[len("cross"):]
    if fwd:                            # fwd = southbound
        return f"N{k}" if io == "in" else f"S{k}"
    return f"S{k}" if io == "in" else f"N{k}"


# Hourly demand per portal and its destination split. Chosen so the main
# street runs at roughly 0.85 of signal capacity, which is where a
# signalized corridor queues at red but still clears.
MAIN_W_IN = 480.0
MAIN_E_IN = 420.0
CROSS_IN = 75.0
TRUCK_FRAC = 0.06
DEMAND_SCALE = 1.0


def demand_flows(net):
    o, e = portals(net)
    flows = []

    def spread(dsts):
        """{portal: weight} -> {lane: weight} split evenly over its lanes."""
        out = {}
        for name, w in dsts.items():
            lanes = e[name]
            for lid in lanes:
                out[lid] = out.get(lid, 0.0) + w / len(lanes)
        return out

    def emit(fid, origin_lanes, total, dsts):
        d = spread(dsts)
        for i, lid in enumerate(sorted(origin_lanes)):
            flows.append({
                "id": f"{fid}-{i}", "origin": lid, "spacing": "poisson",
                "veh_per_h": round(total / len(origin_lanes), 3),
                "vtypes": {"car": 1 - TRUCK_FRAC, "truck": TRUCK_FRAC},
                "destinations": d,
            })

    k_ = DEMAND_SCALE
    emit("w", o["W"], MAIN_W_IN * k_,
         {"E": 0.60, **{f"S{k}": 0.06 for k in range(1, 5)},
          **{f"N{k}": 0.04 for k in range(1, 5)}})
    emit("e", o["E"], MAIN_E_IN * k_,
         {"W": 0.60, **{f"N{k}": 0.06 for k in range(1, 5)},
          **{f"S{k}": 0.04 for k in range(1, 5)}})
    for k in range(1, 5):
        others = [j for j in range(1, 5) if j != k]
        emit(f"n{k}", o[f"N{k}"], CROSS_IN * k_,
             {f"S{k}": 0.25, "W": 0.25, "E": 0.20,
              **{f"N{j}": 0.10 for j in others}})
        emit(f"s{k}", o[f"S{k}"], CROSS_IN * k_,
             {f"N{k}": 0.25, "W": 0.20, "E": 0.25,
              **{f"S{j}": 0.10 for j in others}})
    return flows


# ------------------------------------------------------------------ output

def write_yaml_demand(path, flows, note):
    with open(path, "w") as f:
        f.write(f"# {note}\n")
        f.write("format_version: 1\nflows:\n")
        for fl in flows:
            f.write(f"  - id: {fl['id']}\n")
            f.write(f"    origin: {fl['origin']}\n")
            f.write(f"    veh_per_h: {fl['veh_per_h']:g}\n")
            f.write("    spacing: poisson\n")
            f.write("    vtypes:\n")
            for t, w in fl["vtypes"].items():
                f.write(f"      {t}: {w:g}\n")
            f.write("    destinations:\n")
            for lid, w in sorted(fl["destinations"].items()):
                f.write(f"      {lid}: {round(w, 6):g}\n")


def write_metrics(path, net, period_s):
    with open(path, "w") as f:
        f.write("# Whole-network measurement set (ADR-0014 §5), authored by\n")
        f.write("# scripts/demos/bottleneck_town.py. period_s splits the run so\n")
        f.write("# whatif.py --warmup can drop the fill-up transient.\n")
        f.write("format_version: 1\ntrips: {}\nsets:\n")
        f.write("  - id: net\n    metrics:\n")
        for m in ("edie", "time_loss", "stops", "occupancy"):
            f.write(f"      - {m}\n")
        f.write(f"    window:\n      period_s: {period_s:g}\n")
        f.write("    elements:\n")
        for L in net.lanes:
            f.write(f"      - {L['id']}\n")


def write_variant(root, variant, ticks, seed, period_s):
    net, cycle = build_town(variant)
    d = os.path.join(root, variant)
    os.makedirs(os.path.join(d, "demand"), exist_ok=True)
    os.makedirs(os.path.join(d, "metrics"), exist_ok=True)
    note = (f"Bottleneck Town — {variant}. 4 signalized cross-street junctions "
            f"on a bowed main street; cycle {cycle:g} s. {FICTITIOUS}")
    with open(os.path.join(d, "network.json"), "w") as f:
        json.dump(net.doc(note), f, separators=(",", ":"))
    write_yaml_demand(os.path.join(d, "demand", "main.yaml"),
                      demand_flows(net),
                      "Authored O-D demand for Bottleneck Town "
                      "(scripts/demos/bottleneck_town.py).")
    write_metrics(os.path.join(d, "metrics", "main.yaml"), net, period_s)
    with open(os.path.join(d, "scenario.yaml"), "w") as f:
        f.write("# Bottleneck Town — generated by scripts/demos/bottleneck_town.py.\n")
        f.write(f"# arm: {variant}; signal cycle {cycle:g} s\n")
        f.write("format_version: 1\n")
        f.write(f"id: {variant}\n")
        f.write(f"seed: {seed}\n")
        f.write(f"ticks: {ticks}\n")
        f.write("network: network.json\n")
        f.write("types:\n  - car\n  - truck\n")
        f.write("demand:\n  - demand/main.yaml\n")
        f.write("metrics:\n  - metrics/main.yaml\n")
        f.write("# Published on the static-routing baseline (docs/show); the engine\n")
        f.write("# default is adaptive-on since 2026-07-31 (ADR-0036 addendum).\n")
        f.write("params:\n  adaptive_routing: false\n")
    return net, cycle


# ------------------------------------------------------------------- checks

def shortest_path(net, src, dst, ban=()):
    """Dijkstra over lane lengths, matching engine/routing.go's weights.

    Lateral neighbours are joined at zero cost: the kernel's next-hop table
    is successor-only, but routeLatDepth steers a routed vehicle sideways
    onto a lane its destination IS reachable from, so the effective path a
    vehicle drives is the one this graph finds.
    """
    lat = {}
    by_edge = {}
    for L in net.lanes:
        if L.get("edge"):
            by_edge.setdefault(L["edge"], []).append(L)
    for group in by_edge.values():
        group.sort(key=lambda L: L["edgeIndex"])
        for i in range(len(group) - 1):
            a, b = group[i]["id"], group[i + 1]["id"]
            lat.setdefault(a, []).append(b)
            lat.setdefault(b, []).append(a)
    d0 = net.by_id[src]["length"]
    dist = {src: d0}
    prev = {}
    pq = [(d0, src)]
    while pq:
        d, u = heapq.heappop(pq)
        if d > dist.get(u, math.inf) + 1e-9:
            continue
        if u == dst:
            break
        nbrs = [(v, net.by_id[v]["length"]) for v in net.by_id[u]["successors"]
                if not v.startswith(ban)]
        nbrs += [(v, 0.0) for v in lat.get(u, ()) if not v.startswith(ban)]
        for v, w in nbrs:
            nd = d + w
            if nd < dist.get(v, math.inf) - 1e-9:
                dist[v] = nd
                prev[v] = u
                heapq.heappush(pq, (nd, v))
    if dst not in dist:
        return None, None
    path, cur = [dst], dst
    while cur in prev:
        cur = prev[cur]
        path.append(cur)
    return list(reversed(path)), dist[dst]


def check(root, variants):
    for v in variants:
        net, cycle = build_town(v)
        o, e = portals(net)
        print(f"\n=== {v}: {len(net.lanes)} lanes, {len(net.signals)} signals, "
              f"cycle {cycle:g} s")
        ban = ("byp_",) if v == "bypass-north" else \
              ("con_",) if v == "connector-south" else ()
        pairs = [("W", "E"), ("E", "W"), ("S1", "S4"), ("W", "S4"),
                 ("W", "N3"), ("S1", "E")]
        for a, b in pairs:
            src, dst = sorted(o[a])[0], sorted(e[b])[0]
            path, d = shortest_path(net, src, dst)
            if path is None:
                print(f"  {a}->{b}: UNREACHABLE")
                continue
            secs = []
            for lid in path:
                s = net.by_id[lid]["section"]
                base = s.split("_")[0] if not s.startswith("j:") else s
                if not secs or secs[-1] != base:
                    secs.append(base)
            alt = ""
            if ban:
                _p2, d2 = shortest_path(net, src, dst, ban=ban)
                used = any(lid.startswith(ban) for lid in path)
                alt = (f"   [{'USES' if used else 'avoids'} the new road; "
                       f"without it {d2:.0f} m, margin {d2 - d:+.0f} m]")
            print(f"  {a}->{b}: {d:8.0f} m  via {' '.join(secs)}{alt}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="data/pods/bottleneck-town")
    ap.add_argument("--ticks", type=int, default=12000)
    ap.add_argument("--seed", type=int, default=1000)
    ap.add_argument("--period-s", type=float, default=60.0)
    ap.add_argument("--check", action="store_true",
                    help="print each arm's router-chosen paths and exit")
    ap.add_argument("--only", default=None)
    ap.add_argument("--demand-scale", type=float, default=1.0,
                    help="multiply every portal flow (calibration sweeps)")
    args = ap.parse_args()
    global DEMAND_SCALE
    DEMAND_SCALE = args.demand_scale

    variants = ["base", "add-lane", "bypass-north", "connector-south",
                "retime-short", "green-wave"]
    if args.only:
        variants = args.only.split(",")
    if args.check:
        check(args.out, variants)
        return
    os.makedirs(args.out, exist_ok=True)
    for v in variants:
        net, cycle = write_variant(args.out, v, args.ticks, args.seed,
                                   args.period_s)
        print(f"[town] {v}: {len(net.lanes)} lanes, {len(net.signals)} "
              f"signals, cycle {cycle:g} s -> {args.out}/{v}")


if __name__ == "__main__":
    main()
