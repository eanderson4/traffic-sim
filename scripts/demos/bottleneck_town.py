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
    main street). Lanes follow the netfile.go edgeIndex contract (0 =
    rightmost = kerb, SUMO convention). Each signalized approach flares
    to a third KERB-side lane at the stop line — a right-turn pocket with
    no upstream predecessor, reached by lane change via the kernel's
    lateral route guidance (routeLatDepth). The centreline-side lane is
    through+left, the middle lane through-only.
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

    That conflict-free claim is now true BY GEOMETRY, verified on the
    emitted polylines (2026-08-29). It was not always: until then the lane
    indexing was mirrored (index 0 sat at the centreline), so right turns
    issued from the centreline-side lane across the through path beside
    them inside `main_thru`, and the two opposed protected-left tangent
    arcs swept through each other inside `main_left` — 64 crossing-overlap
    episodes in an audit of the hero bake. The fix (see add_edge) put lane
    0 at the kerb and redrew the left arcs to pass clear of each other
    (left_link; the box is too tight for a true offside-to-offside pair,
    so they turn early and pass right-side-to-right-side with ~4-5 m of
    centreline clearance); the same-phase intersection check the build is
    validated with finds no crossings left, so the foe sets stay empty for
    the original reason, not on faith.
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


# End-tangent control distance for junction-internal left turns: the cubic
# turns within this many metres of both ends, which is what swings the
# apex away from the junction centre (see left_link). Capped per-turn at
# 0.4x the entry->exit distance so short cross-street lefts stay sane.
LEFT_HOOK_T = 2.0


def left_link(ps, ds, pe, de, n=8):
    """Protected-left internal path: a cubic Bezier that keeps the exact
    end tangents (enters along the approach lane's direction, leaves along
    the exit lane's) but turns EARLY, hooking round its own near side of
    the box, so an opposed protected pair passes with metres of clearance
    and no path crossing.

    Why not the tangent-intersection arc (bezier_link): an opposed pair of
    those both bow toward the junction centre and their bodies overlap
    (~1.1 m of footprint overlap measured on the pre-2026-08-29 network,
    ~0.02 m centreline clearance). The opposite extreme — proceed deep
    into the box, turn late, pass offside-to-offside — is not realizable
    in THIS box: the entry/exit lanes sit 1.75 m off the centrelines, and
    a symmetric late-arc pair either crosses (radius under ~3.5 m, where
    each arc's entry straight meets the other's exit straight) or passes
    under 2.2 m apart at the apexes (radius over ~3.5 m — footprint
    overlap again). Turning early is the arrangement the geometry admits
    cleanly: each vehicle rounds its own near diagonal, the pair passes
    right-side-to-right-side the way prompt simultaneous opposed lefts do
    at real US signals, and the measured centreline clearance between the
    emitted polylines is ~4-5 m. Endpoints are exact (the cubic starts at
    ps, ends at pe), so the internal lane still meets its approach and
    exit lanes precisely.
    """
    t = min(LEFT_HOOK_T, 0.4 * norm(sub(pe, ps)))
    c1 = add(ps, scale(ds, t))
    c2 = sub(pe, scale(de, t))
    pts = []
    for k in range(n + 1):
        u = k / n
        w0 = (1 - u) ** 3
        w1 = 3 * (1 - u) ** 2 * u
        w2 = 3 * (1 - u) * u ** 2
        w3 = u ** 3
        pts.append((w0 * ps[0] + w1 * c1[0] + w2 * c2[0] + w3 * pe[0],
                    w0 * ps[1] + w1 * c1[1] + w2 * c2[1] + w3 * pe[1]))
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

    Lane indexing follows the netfile.go edgeIndex contract: 0 = RIGHTMOST
    = kerb lane (SUMO convention), index+1 one lane to the LEFT. `nrm` is
    the LEFT normal of the travel direction and lane i is placed
    (nlanes-i-0.5) widths along -nrm, so index 0 is the furthest right and
    the highest index borders the centreline — the layout every US
    arterial driver expects, and the one the engine's lateral chaining
    assumes (netfile.go: "left neighbor = index+1").

    This was not always so, and the failure mode is worth recording.
    Until 2026-08-29 the offset was -(i+0.5)*LANE_W: index 0 sat nearest
    the CENTRELINE and the whole network was mirrored against the
    contract. The junction builder keys off the contract (right turn from
    lane 0, left from the highest index), so right turns issued from the
    centreline-side lane and crossed the through path beside them INSIDE
    the shared `main_thru` green, left turns issued from the kerb-side
    lane, and the engine's Left/Right lateral links pointed the wrong way
    physically. These junctions ship with foesCross/foesMerge empty on the
    grounds that the phases are conflict-free by construction, so nothing
    arbitrated those crossings: an audit of the hero bake counted 64
    crossing-overlap episodes, dominated by right-turn x same-green
    through. The fix is the indexing below plus the matching chain()
    alignment and the opposed-left arc geometry in build_junction; the
    build is then validated by intersecting the same-phase internal
    polylines, which now come back clean.

    A consequence worth knowing: with the indexing corrected, the flare's
    added lane can only be KERB-side — the carriageway widens rightward
    because the opposing direction owns the other side of the axis. The
    pocket is therefore a RIGHT-turn pocket, not the left-turn bay the
    scenario was originally built around; the protected left now shares
    the centreline through lane. The routing property the bay existed for
    is preserved differently: see the Leg docstring.
    """
    u = unit(sub(b, a))
    nrm = left_normal(u)
    length = norm(sub(b, a))
    ids, sp, ep = [], [], []
    for i in range(nlanes):
        off = -(nlanes - i - 0.5) * LANE_W
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
    """lane i of upstream -> lane i+extra of downstream.

    extra = len(downstream) - len(upstream): the only unequal-count link in
    this network is the junction flare, which widens KERB-ward (the opposing
    carriageway owns the centreline side of the axis). Under the corrected
    indexing (0 = rightmost) the added lanes carry the LOW indices, so every
    continuing lane keeps its exact lateral position when it is mapped i ->
    i+extra — no lateral jog at the edge join. Equal-count links get extra=0,
    the identity, exactly as before.
    """
    extra = len(downstream.ids) - len(upstream.ids)
    for i, lid in enumerate(upstream.ids):
        j = min(max(i + extra, 0), len(downstream.ids) - 1)
        net.by_id[lid]["successors"].append(downstream.ids[j])


FLARE_LEN = 180.0   # length of the right-turn pocket at a signalized approach


def build_piece(net, name, piece_pts, nlanes, speed, origin, exit_,
                flare_extra=0):
    """A run of road between two junctions: one straight edge per segment.

    flare_extra > 0 carves the last FLARE_LEN metres off into their own
    edge with that many extra lanes on the KERB side — the right-turn
    pocket. The pocket lane has no upstream successor on purpose: a vehicle
    reaches it by changing lanes (routeLatDepth steers routed vehicles into
    it), so only traffic whose destination needs it ever occupies it, and
    every through lane's leftmost successor stays a through movement (the
    property the Successors[0] routing fallback depends on).
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
    movement, in the netfile.go edgeIndex contract: 0 = rightmost = kerb.
    `thru` is the tuple of through-lane indices in kerb->centreline order;
    the j-th through lane continues into outbound lane j (see
    build_junction), which keeps every through internal path STRAIGHT when
    the approach flares kerb-ward and the exit does not.

    Getting this right is not cosmetic: pickSuccessor falls back to
    Successors[0] — the LEFTMOST successor — whenever the route table
    cannot resolve (which happens for every turn destination when the
    vehicle is sitting in a lane the turn is not issued from, because lane
    changes are not successors). If the leftmost successor of a through
    lane is a turn, that fallback sends through traffic round the corner.
    Every through lane here therefore has its THROUGH movement leftmost
    (the successor sort in build_junction hoists the same-road movement),
    and each turn lives in the lane US convention puts it in: the right
    turn in the kerb lane — at a flared approach a pocket whose only
    successor is the right turn, so nothing but right-turners is ever
    routed into it — and the left in the centreline-side through lane.
    """

    def __init__(self, name, inbound, outbound, thru=None, left_lane=None,
                 right_lane=0):
        self.name = name
        self.inbound = inbound     # Lanes approaching the junction (may be None)
        self.outbound = outbound   # Lanes leaving the junction (may be None)
        n = len(inbound.ids) if inbound else 0
        self.thru = tuple(range(n)) if thru is None else tuple(thru)
        self.left_lane = (n - 1) if left_lane is None else left_lane
        self.right_lane = right_lane


def build_junction(net, jid, legs, rows=None, speed=TURN_SPEED,
                   lane_rules=None):
    """Wire every non-U movement between the legs as an internal lane.

    Lane assignment (edgeIndex 0 = rightmost = kerb, netfile.go):
      through  j-th through lane (kerb->centreline) -> outbound lane
               min(j, nout-1); with a kerb-side flare this keeps every
               through internal path a STRAIGHT line across the box
      right    lane 0 (the kerb lane / pocket) -> outbound lane 0
      left     the highest-index (centreline-side) lane -> outbound nout-1

    Same-phase conflict freedom is a GEOMETRIC property of these paths,
    checked on the emitted polylines at every build (see the module
    docstring): right-from-kerb hugs the corner right of the through path
    beside it, and opposed protected lefts are drawn by left_link to turn
    early and pass each other with metres of clearance (the box is too
    tight for a true offside-to-offside pair — see left_link).

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
                pairs = [(lane, min(j, nout - 1))
                         for j, lane in enumerate(a.thru)]
            elif kind == "right":
                pairs = [(a.right_lane, 0)]
            else:
                pairs = [(a.left_lane, nout - 1)]
            for fi, ti in pairs:
                ps, pe = a.inbound.end_pts[fi], b.outbound.start_pts[ti]
                if kind == "left":
                    shape = left_link(ps, a.inbound.dir_end, pe,
                                      b.outbound.dir_start)
                else:
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


def _seg_intersect(p1, p2, p3, p4):
    """Proper segment intersection point, or None (touches excluded)."""
    d = (p2[0] - p1[0]) * (p4[1] - p3[1]) - (p2[1] - p1[1]) * (p4[0] - p3[0])
    if abs(d) < 1e-12:
        return None
    t = ((p3[0] - p1[0]) * (p4[1] - p3[1]) - (p3[1] - p1[1]) * (p4[0] - p3[0])) / d
    u = ((p3[0] - p1[0]) * (p2[1] - p1[1]) - (p3[1] - p1[1]) * (p2[0] - p1[0])) / d
    if 1e-9 < t < 1 - 1e-9 and 1e-9 < u < 1 - 1e-9:
        return (p1[0] + t * (p2[0] - p1[0]), p1[1] + t * (p2[1] - p1[1]))
    return None


def polys_cross(a, b):
    """Do two polylines properly intersect? A touch within 0.3 m of a
    shared start point is fan-out from one approach lane, not a conflict."""
    for i in range(len(a) - 1):
        for j in range(len(b) - 1):
            p = _seg_intersect(a[i], a[i + 1], b[j], b[j + 1])
            if p is not None and (norm(sub(p, a[0])) > 0.3
                                  or norm(sub(p, b[0])) > 0.3):
                return True
    return False


def cross_foes(net, made, phase_of):
    """Declare foesCross between same-phase internals whose paths cross.

    Derived from the emitted geometry, not from a hand list. At the J1..J4
    split/protected junctions this adds NOTHING and that is the point: the
    phases are conflict-free because the paths were drawn that way
    (right-from-kerb, early-hook opposed lefts), which the empty result
    here verifies at build time. The 2-phase CS junctions are different:
    one phase runs BOTH directions of a road with all its movements, so a
    left across the opposing through stream is a genuine same-phase
    crossing no signal state separates. Declaring it lets the boxBlocked
    foe-occupancy check serialize the pair — a permitted-left yield —
    instead of booking the overlap as a collision.

    Returns the declared (a, b) pairs so protected junctions can ASSERT
    the result is empty — an invariant violation must fail the build, not
    quietly become a permitted-left yield.
    """
    pairs = []
    ids = [lid for _leg, _kind, lid, _to in made]
    for i in range(len(ids)):
        for j in range(i + 1, len(ids)):
            a, b = ids[i], ids[j]
            if phase_of.get(a) != phase_of.get(b):
                continue
            la, lb = net.by_id[a], net.by_id[b]
            if polys_cross(la["shape"], lb["shape"]):
                la.setdefault("foesCross", []).append(b)
                lb.setdefault("foesCross", []).append(a)
                pairs.append((a, b))
    return pairs


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
    # one extra lane on each signalized approach: the right-turn pocket
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
            # main approaches are flared: the added KERB lane (index 0) is
            # the right-turn pocket with no upstream predecessor, lanes
            # 1..K are through, lane K (centreline side) also carries the
            # protected left. Cross approaches are single-lane: every
            # movement issues from lane 0.
            Leg("W", main.fwd_in[jid], main.rev_out[jid],
                thru=range(1, main_lanes + 1),
                left_lane=main_lanes),                         # from the west
            Leg("E", main.rev_in[jid], main.fwd_out[jid],
                thru=range(1, main_lanes + 1),
                left_lane=main_lanes),                         # from the east
            Leg("N", c.fwd_in[jid], c.rev_out[jid]),         # from the north
            Leg("S", c.rev_in[jid], c.fwd_out[jid]),         # from the south
        ]
        made = build_junction(net, jid, legs)
        groups = {g: set() for g in PHASE_ORDER}
        phase_of = {}
        for link, (leg, kind, lid, _to) in enumerate(made):
            net.by_id[lid]["tl"] = jid
            net.by_id[lid]["tlLink"] = link
            if leg in ("W", "E"):
                phase_of[lid] = "main_left" if kind == "left" else "main_thru"
            else:
                phase_of[lid] = "cross_n" if leg == "N" else "cross_s"
            groups[phase_of[lid]].add(link)
        # Split/protected phases must stay crossing-free BY GEOMETRY — a
        # non-empty result here means the junction drawing regressed, so
        # fail the build rather than paper it over with a declared foe.
        foes = cross_foes(net, made, phase_of)
        if foes:
            raise SystemExit(
                f"bottleneck-town: {jid} gained {len(foes)} same-phase foe "
                f"pairs ({', '.join(a + '×' + b for a, b in foes[:3])}"
                + ("…" if len(foes) > 3 else "") + ") — the protected "
                "junction geometry must not cross same-phase paths; fix "
                "the drawing, don't declare the conflict")
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
            phase_of = {}
            for link, (leg, _kind, lid, _to) in enumerate(made):
                net.by_id[lid]["tl"] = jid
                net.by_id[lid]["tlLink"] = link
                phase_of[lid] = "con" if leg in ("W", "E") else "cross"
                groups[phase_of[lid]].add(link)
            # 2-phase, all movements per direction: the left across the
            # opposing through stream is a real same-phase crossing —
            # declare it (permitted-left yield via boxBlocked)
            cross_foes(net, made, phase_of)
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
