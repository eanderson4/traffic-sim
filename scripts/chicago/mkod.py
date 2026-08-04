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
import math
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
# An interior freeway injection point needs room for the ADR-0021 clearance
# check to have somewhere to look. The corridor mainlines are nothing like
# tight on this — chi-loop-urban's median corridor lane is 111-245 m — but the
# ramp STUBS are (median 6-8 m), which is why the injection points are taken
# on the mainline rather than on the merge lanes themselves.
MIN_RAMP_LANE_M = 40.0


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


def lateral_links(lanes):
    """Lateral neighbour map, built by netfile.go's rule (engine/netfile.go).

    Same edge, CONSECUTIVE edgeIndex. A gap in the index sequence means a
    filtered lane sat between the two, so they are not physically adjacent and
    do not link. Links are mutual, as they are at every kernel construction
    site. Unequal lengths are fatal rather than skipped, because that is what
    the kernel does — a network carrying such a pair fails to load at all, and
    emitting demand for it would just defer the error to run time.
    """
    by_edge = collections.defaultdict(list)
    for l in lanes.values():
        if l.get("edge"):
            by_edge[l["edge"]].append(l)
    lat = collections.defaultdict(list)
    for edge, group in sorted(by_edge.items()):
        group.sort(key=lambda l: l.get("edgeIndex", 0))
        for a, b in zip(group, group[1:]):
            if b.get("edgeIndex", 0) != a.get("edgeIndex", 0) + 1:
                continue
            if a.get("length") != b.get("length"):
                raise SystemExit(
                    f"edge {edge}: lanes {a['id']} ({a.get('length')} m) and "
                    f"{b['id']} ({b.get('length')} m) differ in length; "
                    f"lateral neighbours must span the same s range and the "
                    f"kernel refuses to load this network (engine/netfile.go)")
            lat[a["id"]].append(b["id"])
            lat[b["id"]].append(a["id"])
    return lat


def can_reach(preds, lat, dest):
    """Lanes that can reach dest, by the kernel's own relation.

    A 0-1 BFS from dest over the reversed lane graph: a successor edge costs
    0 lane changes (the vehicle just drives it) and a lateral link costs 1
    (it has to hop). This mirrors Engine.routeLatDepth (engine/routing.go)
    exactly, including its lack of a hop cap, so a destination this filter
    accepts is one the kernel can actually steer to — and one it rejects is
    one the kernel genuinely cannot reach.

    It replaced a successor-ONLY reverse BFS, which was strictly more
    conservative than the kernel and wrong in a way that hid: a boundary
    origin whose only successors led to an off-ramp reached ~nothing, so
    dests_for() came back empty and blend() collapsed the flow to 100%
    through traffic. Five of the twenty-six Chicago freeway origins were in
    that state, driving two kilometres and leaving. Measured on
    chi-loop-urban: p090 reached 7 lanes of ~55,000 under the old relation.
    """
    R = {dest}
    frontier = [dest]
    while frontier:
        layer = frontier
        i = 0
        while i < len(layer):  # 0-cost closure: everything that can DRIVE in
            for p in preds.get(layer[i], ()):
                if p not in R:
                    R.add(p)
                    layer.append(p)
            i += 1
        frontier = []  # 1-cost step: lateral neighbours seed the next layer
        for u in layer:
            for nb in lat.get(u, ()):
                if nb not in R:
                    R.add(nb)
                    frontier.append(nb)
    return R


def spread(items, n, pos):
    """Up to n items chosen for maximum spatial separation (greedy k-center).

    `pos` maps an item to its (x, y). Taking every n-th item from a list
    sorted by coordinate does NOT spread it: a corridor carries several
    parallel lanes at the same longitudinal position, so evenly spaced
    INDICES land several picks within metres of each other. Measured on the
    Dan Ryan, index spacing put two injection points 10 m apart with a median
    gap of 130 m across a 5.9 km corridor — which is the single-point defect
    --ramp-share exists to remove, merely subdivided.

    Co-located candidates fall out for free: once a position is chosen, any
    other candidate at that position has minimum distance 0 and is never
    preferred. Fewer than n come back when the geometry cannot support n
    separated points; the caller divides the relocated volume by what it
    actually got, so a short pick changes where demand enters, never how
    much of it there is.

    Deterministic (ADR-0005): seeded from items[0] and ties broken by
    position in `items`, so the result depends only on the input order.
    """
    if n <= 0 or not items:
        return []
    chosen = [items[0]]
    if n == 1:
        return chosen
    best = [math.dist(pos(it), pos(items[0])) for it in items]
    while len(chosen) < min(n, len(items)):
        k = max(range(len(items)), key=lambda i: (best[i], -i))
        if best[k] <= 0.0:
            break  # every remaining candidate sits on one already chosen
        chosen.append(items[k])
        for i, it in enumerate(items):
            d = math.dist(pos(it), pos(items[k]))
            if d < best[i]:
                best[i] = d
    order = {id(it): i for i, it in enumerate(items)}
    return sorted(chosen, key=lambda it: order[id(it)])


def build_profile(args):
    """The temporal shape of demand, as (start_s, end_s, fraction-of-peak).

    Explicit spans rather than a bare list of fractions indexed by a global
    slice width, because the width is now a knob and the shape has to be able
    to outlast the peak. A demand program that only ever ramps up answers
    "how fast does the network fill"; adding a taper and a drain lets it
    answer "does the congestion clear", which is the question a peak-period
    study actually asks.

    --flat-peak spans the WHOLE horizon. It used to emit one hard-coded
    0-1800 s slice, so any run longer than 30 simulated minutes silently lost
    its arrival process partway through and drained without anyone asking it
    to — the 36,000-tick runs this function was written for would have
    injected for 30 minutes and then gone quiet.
    """
    peak = max(AM_PROFILE)
    if args.flat_peak:
        span = args.horizon_s or (len(AM_PROFILE) * args.slice_s)
        return [(0.0, span, peak)]
    prof = [(i * args.slice_s, (i + 1) * args.slice_s, f)
            for i, f in enumerate(AM_PROFILE)]
    if args.drain_s > 0:
        end = len(AM_PROFILE) * args.slice_s
        prof.append((end, end + args.drain_s, args.drain_level * peak))
    return prof


class ProfileSet:
    """A library of named temporal shapes plus rules assigning them to flows.

    Chosen over the two obvious alternatives:

    A per-flow rate MATRIX (flows x timesteps) is what `demand/main.yaml`
    already is — every flow carries its own slices. Authoring at that level
    adds no abstraction, cannot survive regeneration, and diffs unreadably.

    A FUNCTION of live state would make demand elastic: congestion feeding
    back into arrivals. That is a legitimate model but a different one, and
    it cannot live here — this generator runs before the simulation and has
    no state to consult. It would also end paired-seed A/B comparison, since
    two runs would no longer share a demand program.

    So: a small library of shapes, assigned by rule, scaled per rule. Rules
    match on `kind` (portal / interior / resident), road `class`, and
    `corridor`; FIRST match wins and `default` catches the rest. A rule that
    matches nothing is fatal, because the alternative is a typo that reads
    exactly like a profile having no effect (the lesson --corridor-scale
    learned).
    """

    KEYS = {"kind", "class", "corridor", "profile", "scale"}

    def __init__(self, cfg, path):
        def bad(msg):
            sys.exit(f"{path}: {msg}")

        if cfg.get("version") != 1:
            bad(f"version {cfg.get('version')!r}: only version 1 is known")
        self.step_s = float(cfg.get("step_s", 0))
        if self.step_s <= 0:
            bad(f"step_s {self.step_s} must be positive")
        raw = cfg.get("profiles")
        if not isinstance(raw, dict) or not raw:
            bad("profiles must be a non-empty object of name -> [fractions]")
        self.profiles = {}
        for name, vals in raw.items():
            if not isinstance(vals, list) or not vals:
                bad(f"profile {name!r} must be a non-empty list of fractions")
            for v in vals:
                if not isinstance(v, (int, float)) or v < 0:
                    bad(f"profile {name!r}: {v!r} is not a fraction >= 0")
            self.profiles[name] = [float(v) for v in vals]
        # Differing lengths are allowed — a freight curve may legitimately
        # outlast a commute one — but they mean the demand program ends at
        # different times for different flows, which is worth saying out loud.
        spans = {name: len(v) * self.step_s for name, v in self.profiles.items()}
        if len(set(spans.values())) > 1:
            print(f"[mkod] NOTE: {path} profiles do not all span the same "
                  f"time: {', '.join(f'{n}={s:.0f}s' for n, s in sorted(spans.items()))}"
                  f". Flows will stop arriving at different moments.",
                  file=sys.stderr)
        self.span_s = max(spans.values())

        self.default = cfg.get("default")
        if self.default is not None and self.default not in self.profiles:
            bad(f"default {self.default!r} names no profile; have "
                f"{', '.join(sorted(self.profiles))}")
        self.rules = []
        for i, r in enumerate(cfg.get("assign", [])):
            if not isinstance(r, dict):
                bad(f"assign[{i}] must be an object")
            # JSON has no comments, so `_`-prefixed keys carry the rationale
            # for a rule next to the rule. Everything else must be known: an
            # unrecognised key is far more likely a misspelling of a real one
            # than a note, and silently ignoring it loses the rule's intent.
            unknown = sorted(k for k in set(r) - self.KEYS
                             if not k.startswith("_"))
            if unknown:
                bad(f"assign[{i}]: unknown key(s) {', '.join(unknown)}; "
                    f"known keys are {', '.join(sorted(self.KEYS))}")
            if r.get("profile") not in self.profiles:
                bad(f"assign[{i}]: profile {r.get('profile')!r} names no "
                    f"profile; have {', '.join(sorted(self.profiles))}")
            scale = r.get("scale", 1.0)
            if not isinstance(scale, (int, float)) or scale < 0:
                bad(f"assign[{i}]: scale {scale!r} must be a number >= 0")
            match = {k: r[k] for k in ("kind", "class", "corridor") if k in r}
            if not match:
                bad(f"assign[{i}] has no match key, so it would swallow every "
                    f"flow; use `default` if that is the intent")
            self.rules.append((match, r["profile"], float(scale)))
        if not self.rules and self.default is None:
            bad("no assign rules and no default: nothing would get a profile")
        self.hits = collections.Counter()
        self.default_hits = 0

    def resolve(self, kind, cls, corridor):
        """(slices, scale) for a flow, or (None, _) if nothing applies."""
        fields = {"kind": kind, "class": cls, "corridor": corridor}
        for i, (match, name, scale) in enumerate(self.rules):
            if all(fields.get(k) == v for k, v in match.items()):
                self.hits[i] += 1
                return self.spans(name), scale
        if self.default is None:
            return None, 0.0
        self.default_hits += 1
        return self.spans(self.default), 1.0

    def spans(self, name):
        return [(i * self.step_s, (i + 1) * self.step_s, f)
                for i, f in enumerate(self.profiles[name])]

    def check_all_rules_fired(self, path):
        dead = [i for i in range(len(self.rules)) if not self.hits[i]]
        if dead:
            lines = "\n".join(
                f"  assign[{i}]: " + ", ".join(f"{k}={v!r}" for k, v in
                                               sorted(self.rules[i][0].items()))
                for i in dead)
            sys.exit(f"{path}: {len(dead)} assign rule(s) matched no flow at "
                     f"all:\n{lines}\nA rule that never fires is "
                     f"indistinguishable from a profile that does nothing, so "
                     f"this is refused rather than warned about. Check the "
                     f"spelling against the corridor and class names mkod "
                     f"prints above.")


def zone_blend(weights, lane2zone, zone_share):
    """Normalized destination weights with per-district shares pinned.

    `weights` is {dest_lane: floor_area} already filtered to what this origin
    can reach. Districts named in `zone_share` receive exactly that share of
    the flow; the rest split what is left in proportion to their floor area,
    which is the unmodified behaviour. Within a district, weight is still
    floor area — pinning changes how much goes downtown, never which downtown
    building it goes to.

    A pin is a REQUEST, not a guarantee. An origin that cannot reach a pinned
    district contributes nothing to it, and its pinned share is spread over
    what it can reach rather than being dropped — losing it would quietly
    shrink the flow. The aggregate realized share is therefore ≤ the pin, and
    mkod prints both so the gap is visible instead of assumed away.
    """
    tot = sum(weights.values())
    if tot <= 0:
        return {}
    if not zone_share:
        return {d: v / tot for d, v in weights.items()}

    by_zone = collections.defaultdict(dict)
    for d, v in weights.items():
        by_zone[lane2zone.get(d)][d] = v

    pinned = {z: s for z, s in zone_share.items() if z in by_zone}
    rest = [z for z in by_zone if z not in pinned]
    rest_area = sum(sum(by_zone[z].values()) for z in rest)
    budget = dict(pinned)
    spare = 1.0 - sum(pinned.values())
    if rest and rest_area > 0:
        for z in rest:
            budget[z] = spare * sum(by_zone[z].values()) / rest_area
    elif spare > 1e-12 and budget:
        # Nothing unpinned is reachable, so the remainder has nowhere to go
        # but back into the pinned districts, pro rata.
        psum = sum(budget.values()) or 1.0
        for z in list(budget):
            budget[z] += spare * budget[z] / psum

    bsum = sum(budget.values())
    if bsum <= 0:
        return {}
    out = {}
    for z, frac in budget.items():
        za = sum(by_zone[z].values())
        # A district pinned to 0 — or squeezed to 0 by other pins summing to
        # 1 — contributes no destinations at all. Emitting them at weight 0
        # would lean on emit_flow's rounding filter to stay loadable, and
        # would report the district as a destination that receives nothing.
        if za <= 0 or frac <= 0:
            continue
        for d, v in by_zone[z].items():
            out[d] = frac / bsum * (v / za)
    return out


def peak_rate(segs):
    """(max over time of the summed rate, the second at which that max starts).

    `segs` is [(start_s, end_s, veh_per_h)] as WRITTEN to the demand file.

    A max over time, not a sum of per-flow peaks. ADR-0028 exists precisely so
    that flows crest at different moments — freight at slice 4, commute at 2-3,
    reverse-commute at 3 — so adding their individual maxima yields a rate no
    instant in the run ever sees, and it grows with the number of DISTINCT
    shapes rather than with demand. Evaluated on the elementary intervals
    between all slice boundaries, so non-uniform spans (--flat-peak, --drain-s,
    or profiles of differing length) need not align on a common grid.

    Half-open on [start, end) to match emit_flow's slices: a boundary second
    belongs to the slice starting there, so back-to-back slices neither
    double-count it nor leave it uncovered.
    """
    if not segs:
        return 0.0, 0.0
    bounds = sorted({b for s, e, _ in segs for b in (s, e)})
    best, at = 0.0, bounds[0]
    for i in range(len(bounds) - 1):
        mid = (bounds[i] + bounds[i + 1]) / 2.0
        tot = sum(r for s, e, r in segs if s <= mid < e)
        if tot > best:
            best, at = tot, bounds[i]
    return best, at


def total_veh(segs):
    """Vehicles the program asks for: rate integrated over each slice's span.

    The right basis for a share or a mass balance, where a peak rate is not:
    two rates only compare if they cover the same span, and once flows run on
    different shapes they do not.
    """
    return sum(r * (e - s) / 3600.0 for s, e, r in segs)


def emit_flow(out, fid, origin, rate, profile, vtypes, dests, offset=None,
              observe=None):
    out.append(f"  - id: {fid}")
    out.append(f"    origin: {origin}")
    if offset is not None:
        out.append(f"    offset_m: {offset:.1f}")
    out.append("    spacing: poisson")
    out.append("    slices:")
    for start, end, f in profile:
        r = rate * f
        # A zero-rate slice is DROPPED, not emitted as veh_per_h: 0.0. That is
        # what makes --drain-level 0 mean "arrivals stop": the flow simply has
        # nothing scheduled past the taper, and the network clears itself.
        if r <= 0:
            continue
        out.append(f"      - {{start_s: {start:.0f}, end_s: {end:.0f}, veh_per_h: {r:.1f}}}")
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
        if observe:
            # VEHICLES, not the rate: observe aggregates a share, and a
            # share is only meaningful on counts integrated over the same
            # span — rates on different profile shapes do not have one (the
            # ADR-0028 correction; the district table divided such rates
            # until then). The slices above are what was WRITTEN, so the
            # count is their integral.
            observe(total_veh([(s, e, rate * f) for s, e, f in profile
                               if rate * f > 0]), kept)


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
    ap.add_argument("--dest-zones",
                    help="districts.json lane->district map (mkzones.py). "
                         "Required by --dest-zone-share.")
    ap.add_argument("--dest-zone-share", default="",
                    help="name=fraction[,...] — pin the share of WORKPLACE-"
                         "destined trips bound for each named district, e.g. "
                         "cbd=0.55. Districts not named split the remainder in "
                         "proportion to their workplace floor area, which is "
                         "the unmodified behaviour. This is the largest single "
                         "lever on an AM peak and until now there was no knob "
                         "for it: destinations were weighted by floor area "
                         "alone, so the CBD got whatever share the building "
                         "extract happened to imply (44%% here). Through "
                         "traffic is unaffected — a vehicle crossing the box "
                         "has no downtown destination to re-aim.")
    ap.add_argument("--corridors",
                    help="corridors.json lane->corridor map. Required by "
                         "--ramp-share, which needs to know which lanes make "
                         "up each expressway.")
    ap.add_argument("--ramp-share", type=float, default=0.0,
                    help="fraction of each CORRIDOR's boundary freeway inflow "
                         "to relocate onto interior points spread along that "
                         "corridor, standing in for the on-ramps a map cut "
                         "removes. Default 0 = all inflow enters at the cut "
                         "face, which is what a boundary-portal model does "
                         "and which puts a corridor's entire volume through "
                         "one point: measured on chi-loop-urban at "
                         "freeway-scale 3.5, that left 84-95%% of every "
                         "corridor's delay inside 1 km of its injection point "
                         "with free flow behind it, and capped delivery at "
                         "62%% because the portal could not accept the demand.")
    ap.add_argument("--corridor-scale", action="append", default=[],
                    metavar="NAME=FACTOR",
                    help="multiply one corridor's inflow by FACTOR, on top of "
                         "--freeway-scale. Repeatable. Needs --corridors. One "
                         "global scalar cannot balance corridors that differ "
                         "in length, portal count and posted limit: measured "
                         "on chi-loop-urban, raising --freeway-scale from 1.5 "
                         "to 2.5 took the Dan Ryan from 71.9 to 53.0 km/h and "
                         "left the Eisenhower at 78.2 -> 75.5, because a "
                         "global factor scales an already-too-light corridor "
                         "and an already-heavy one by the same amount.")
    ap.add_argument("--ramps-per-corridor", type=int, default=12,
                    help="how many interior injection points --ramp-share "
                         "spreads a corridor's relocated inflow across")
    ap.add_argument("--flat-peak", action="store_true",
                    help="emit ONE constant slice at the profile's peak rate "
                         "instead of the AM ramp. For recording a watchable "
                         "cut under peak conditions: a recording covers the "
                         "whole run from tick 0, so a 15-minute store of the "
                         "ramped scenario would only ever capture 06:00-06:15.")
    ap.add_argument("--slice-s", type=float, default=SLICE_S,
                    help=f"seconds per AM_PROFILE slice (default {SLICE_S:.0f}"
                         ", i.e. the real 3-hour 06:00-09:00 peak). Lower it "
                         "to run the same ramp/peak/taper SHAPE over a shorter "
                         "modelled window. Note this shortens the window, it "
                         "does not compress the clock: queues still build in "
                         "real time, the peak simply lasts less long.")
    ap.add_argument("--drain-s", type=float, default=0.0,
                    help="seconds of tail appended AFTER the profile, during "
                         "which demand sits at --drain-level. Without it the "
                         "profile ends mid-peak at 0.60 and the run stops "
                         "while the network is still full, so recovery is "
                         "never observed.")
    ap.add_argument("--drain-level", type=float, default=0.0,
                    help="tail demand as a fraction of the PEAK rate. 0 (the "
                         "default) shuts the arrival process off so the "
                         "network clears; ~0.2-0.3 holds an off-peak daily "
                         "baseline instead of work-trip demand.")
    ap.add_argument("--horizon-s", type=float, default=0.0,
                    help="run length in simulated seconds (ticks * dt). Used "
                         "only to check the demand program against the run; "
                         "mkscenario.sh passes it automatically.")
    ap.add_argument("--profiles",
                    help="JSON demand-profile library: a set of named "
                         "temporal shapes plus rules assigning them to flows "
                         "by kind/class/corridor with a per-rule scale. "
                         "Replaces the single built-in AM profile, so it is "
                         "mutually exclusive with --flat-peak/--slice-s/"
                         "--drain-s. See docs/kb/decisions/ADR-0028.")
    args = ap.parse_args()

    if args.slice_s <= 0:
        sys.exit(f"--slice-s {args.slice_s} must be positive")
    if args.drain_s < 0:
        sys.exit(f"--drain-s {args.drain_s} must not be negative")
    if not 0.0 <= args.drain_level <= 1.0:
        sys.exit(f"--drain-level {args.drain_level} out of range: must be in "
                 f"[0, 1], a fraction of the peak rate")
    if args.flat_peak and args.drain_s:
        sys.exit("--drain-s with --flat-peak: a flat peak has no taper to "
                 "drain from. Drop --flat-peak to get the ramped profile.")
    if args.profiles:
        # Two sources of truth for the shape is how a run ends up executing a
        # program nobody authored. Refuse rather than pick a precedence.
        conflict = [f for f, on in (("--flat-peak", args.flat_peak),
                                    ("--slice-s", args.slice_s != SLICE_S),
                                    ("--drain-s", args.drain_s),
                                    ("--drain-level", args.drain_level)) if on]
        if conflict:
            sys.exit(f"--profiles replaces the built-in profile, so it cannot "
                     f"be combined with {', '.join(conflict)}. Express the "
                     f"same shape in the profile library instead — a drain is "
                     f"trailing zeros, a flat peak is a constant profile.")

    with open(args.buildings) as f:
        bidx = json.load(f)
    with open(args.network) as f:
        net = json.load(f)
    with open(args.portals) as f:
        portals = json.load(f)

    lane_corridor = {}
    if args.corridors:
        with open(args.corridors) as f:
            lane_corridor = json.load(f)["lanes"]
    elif args.ramp_share:
        sys.exit("--ramp-share needs --corridors: without the lane->corridor "
                 "map there is no way to know which lanes an expressway's "
                 "relocated inflow should be spread along")
    corridor_scale = {}
    for spec in args.corridor_scale:
        name, _, factor = spec.partition("=")
        if not _ or not name:
            sys.exit(f"--corridor-scale {spec!r}: expected NAME=FACTOR")
        try:
            corridor_scale[name] = float(factor)
        except ValueError:
            sys.exit(f"--corridor-scale {spec!r}: {factor!r} is not a number")
        if corridor_scale[name] < 0:
            sys.exit(f"--corridor-scale {spec!r}: negative")
    if corridor_scale:
        if not lane_corridor:
            sys.exit("--corridor-scale needs --corridors")
        known = set(lane_corridor.values())
        unknown = sorted(set(corridor_scale) - known)
        if unknown:
            # A typo here is silent otherwise: the factor matches nothing and
            # the corridor keeps its unscaled demand, which reads exactly like
            # the scale having no effect.
            sys.exit(f"--corridor-scale names no such corridor: "
                     f"{', '.join(unknown)}; have {', '.join(sorted(known))}")
    if not 0.0 <= args.ramp_share < 1.0:
        sys.exit(f"--ramp-share {args.ramp_share} out of range: must be in "
                 f"[0, 1). At 1.0 a corridor would take no boundary inflow at "
                 f"all, which is not a map cut, it is a closed road")

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
    exit_set = set(exit_lanes)
    print(f"exits: {len(exit_lanes)} through-traffic destination lanes "
          f"({len(fw_exits)} grade-separated)", file=sys.stderr)

    # Reverse reachability per destination — the filter that keeps a flow
    # from being handed a destination its vehicles can never steer to.
    lat = lateral_links(lanes)
    reach = {d: can_reach(preds, lat, d) for d in dest_lanes}
    for d in exit_lanes:
        reach[d] = can_reach(preds, lat, d)

    # --- Destination districts: how much of the region is aimed downtown.
    #
    # Without this, destination weight is workplace floor area and nothing
    # else, so the CBD's share of trips is whatever the building extract
    # implies — 44% here — and there is no way to ask what happens at 55% or
    # 30%. That share is the single largest lever on an AM peak: the same
    # total demand aimed at one square mile versus spread across nine
    # districts is a different network entirely.
    lane2zone = {}
    zone_share = {}
    if args.dest_zone_share and not args.dest_zones:
        sys.exit("--dest-zone-share needs --dest-zones to know which lanes "
                 "are in which district")
    if args.dest_zones:
        with open(args.dest_zones) as f:
            lane2zone = json.load(f)["lanes"]
    for part in filter(None, (p.strip() for p in args.dest_zone_share.split(","))):
        name, _, val = part.partition("=")
        if not val:
            sys.exit(f"--dest-zone-share {part!r}: expected name=fraction")
        try:
            share = float(val)
        except ValueError:
            sys.exit(f"--dest-zone-share {part!r}: {val!r} is not a number")
        if not 0.0 <= share <= 1.0:
            sys.exit(f"--dest-zone-share {name}={share}: must be in [0,1]")
        zone_share[name.strip()] = share
    if zone_share:
        known = set(lane2zone.values())
        bad = sorted(set(zone_share) - known)
        if bad:
            # The --corridor-scale lesson: a name that matches nothing is far
            # more likely a typo than an intention, and it fails silently.
            sys.exit(f"--dest-zone-share names unknown district(s) "
                     f"{bad}; {args.dest_zones} has {sorted(known)}")
        tot = sum(zone_share.values())
        if tot > 1.0 + 1e-9:
            sys.exit(f"--dest-zone-share sums to {tot:.3f}; the pinned shares "
                     f"are fractions of the same 100% of workplace trips and "
                     f"cannot exceed 1.0")
        empty = sorted(n for n in zone_share
                       if not any(lane2zone.get(d) == n for d in dest_lanes))
        if empty:
            sys.exit(f"--dest-zone-share pins {empty}, but no destination lane "
                     f"is in {'that district' if len(empty) == 1 else 'those districts'}. "
                     f"Raise --dest-lanes (currently {args.dest_lanes}, taken by "
                     f"floor area) or drop the pin — as written it would "
                     f"silently do nothing.")
        print(f"dest zones: pinned " +
              ", ".join(f"{n}={v:.0%}" for n, v in sorted(zone_share.items())) +
              f"; the remaining {1 - tot:.0%} splits by floor area",
              file=sys.stderr)

    # Realized share per district, VEHICLE-weighted, accumulated as flows
    # are emitted. A pin is a REQUEST: an origin that cannot reach the CBD has its
    # pinned share redistributed over what it can reach, so the aggregate can
    # land below what was asked for. Printing it is what makes that visible
    # instead of a number nobody checks.
    zone_realized = collections.defaultdict(float)

    def dests_for(origin):
        w = {d: dest_area[d] for d in dest_lanes if origin in reach[d]}
        return zone_blend(w, lane2zone, zone_share)

    def observe(veh, dests):
        """Tally where the demand file actually sends vehicles.

        Called with the flow's whole-program VEHICLE COUNT (the integral of
        the slices emit_flow wrote — a share over rates would overweight
        peak-shaved shapes, the ADR-0028 correction), after blending and
        after negligible-weight destinations were dropped, so it measures
        the file rather than the intent. Boundary exits are counted
        separately: a through trip has no district, and folding it in would
        make every district's share depend on how much traffic merely
        crosses the box.
        """
        for d, v in dests.items():
            if d in exit_set:
                zone_realized["(through)"] += veh * v
            else:
                zone_realized[lane2zone.get(d) or "(unzoned)"] += veh * v

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

    pset = None
    if args.profiles:
        with open(args.profiles) as f:
            pset = ProfileSet(json.load(f), args.profiles)
        profile = None          # resolved per flow below
        prof_end = pset.span_s
    else:
        profile = build_profile(args)
        prof_end = profile[-1][1]

    def profile_for(kind, cls=None, corridor=None):
        """(slices, rate multiplier) for one flow."""
        if pset is None:
            return profile, 1.0
        return pset.resolve(kind, cls, corridor)
    if args.horizon_s:
        # Truncation is the silent-fidelity failure this whole file keeps
        # running into: a 12,000-tick run against the 3-hour profile executes
        # 1,200 s of a 10,800 s program, so it only ever sees the 0.45 opening
        # ramp and reports it as the AM peak. Refuse rather than warn — nobody
        # has ever wanted the first sixth of a demand program.
        if args.horizon_s < prof_end - 1e-6:
            sys.exit(
                f"demand program runs {prof_end:.0f} s but the scenario "
                f"horizon is {args.horizon_s:.0f} s, so "
                f"{prof_end - args.horizon_s:.0f} s of it would never "
                f"execute. Either raise the tick count to "
                f"{prof_end / 0.1:,.0f}, or lower --slice-s to "
                f"{args.horizon_s / len(AM_PROFILE):.0f} to fit this shape "
                f"into the horizon you have.")
        if args.horizon_s > prof_end + 1e-6 and not args.drain_s:
            print(f"[mkod] NOTE: demand ends at {prof_end:.0f} s but the run "
                  f"continues to {args.horizon_s:.0f} s — the last "
                  f"{args.horizon_s - prof_end:.0f} s drain with no arrivals. "
                  f"Say so with --drain-s if that is the intent.",
                  file=sys.stderr)
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
    # --- Interior corridor injection sites, resolved BEFORE any volume is
    # taken off the cut face. A corridor that yields no usable site simply
    # never gets relocated, so the demand cannot be lost between the two
    # halves of the operation.
    ramp_picks = {}
    if args.ramp_share:
        by_corr = collections.defaultdict(list)
        for lid, lab in sorted(lane_corridor.items()):
            if lid in lanes and lanes[lid]["length"] >= MIN_RAMP_LANE_M:
                by_corr[lab].append(lid)
        for corr, cands in sorted(by_corr.items()):
            # Ordered along the corridor so the picks span it rather than
            # clustering; the first shape point is a stable proxy for
            # position and keeps this deterministic (ADR-0005).
            def mid(lid):
                s = lanes[lid]["shape"]
                return s[len(s) // 2]

            cands.sort(key=lambda lid: (mid(lid), lid))
            chosen = []
            for lid in spread(cands, args.ramps_per_corridor, mid):
                lane = lanes[lid]
                hi = lane["length"] - END_MARGIN_M
                if hi <= MIN_OFFSET_M:
                    continue
                work_d = dests_for(lid)
                thru_d = through_for(lid, "motorway", "")
                d = blend(work_d, thru_d, args.freeway_through_share)
                if not d:
                    continue
                # Same degenerate-pool accounting as the portal loop: report
                # the share the blend used, not the one requested.
                eff = 0.0 if not thru_d else (
                    1.0 if not work_d else args.freeway_through_share)
                chosen.append((lid, min(max(lane["length"] / 2,
                                            MIN_OFFSET_M), hi), d, eff))
            if chosen:
                ramp_picks[corr] = chosen
            else:
                print(f"[mkod] WARNING: corridor {corr} has no interior lane "
                      f"at least {MIN_RAMP_LANE_M:.0f} m long that reaches a "
                      f"destination — its inflow stays entirely at the cut "
                      f"face and will queue there", file=sys.stderr)

    stranded = []
    relocate = collections.Counter()

    # Every (start_s, end_s, veh_per_h) triple this run WRITES, by category.
    #
    # Two different questions need two different reductions over it, and the
    # scalar counter that used to live here answered neither:
    #   * how hard is the network loaded  -> max over TIME of the summed rate
    #   * does the mass balance close     -> the integral, i.e. a vehicle count
    #
    # A sum of per-flow peak rates is not the first one. Flows peak at
    # different moments by design (ADR-0028: reverse-commute crests at slice 3,
    # freight at 4), so adding their individual maxima reports a rate no
    # instant in the run ever sees. Nor is a sum of base rates: emit_flow
    # writes `rate * f`, and `f` tops out at 0.90 for freight and 0.60 for
    # reverse-commute, so the base rate is not in the file at all.
    timeline = collections.defaultdict(list)

    def tally(cat, rate, profile):
        """Record what emit_flow will write, on the same terms it writes it."""
        for s, e, f in profile:
            r = rate * f
            if r > 0:      # emit_flow drops zero slices, so neither counts one
                timeline[cat].append((s, e, r))

    def cats(*names):
        return [sg for n in names for sg in timeline[n]]
    for i, (lid, cls, raw, edge) in enumerate(sorted(entries)):
        share = (args.freeway_through_share if cls in FREEWAY_CLASSES
                 else args.through_share)
        thru = through_for(lid, cls, edge)
        work = dests_for(lid)
        d = blend(work, thru, share)
        if not d:
            stats["portal unreachable"] += 1
            continue
        # The share blend() ACTUALLY used, not the one requested: a degenerate
        # pool collapses the mix, and reporting the request instead of the
        # outcome is how 83% of Chicago's freeway inflow came to leave at the
        # boundary while this line printed 75%.
        eff = 0.0 if not thru else (1.0 if not work else share)
        truck = args.truck if cls in TRUCK_HEAVY else args.truck / 2
        rate = raw * portal_scale
        if cls in FREEWAY_CLASSES:
            rate *= args.freeway_scale
            rate *= corridor_scale.get(lane_corridor.get(lid), 1.0)
        corr = lane_corridor.get(lid)
        # Resolved BEFORE anything is counted, not at the emit call below. The
        # profile rule's `scale` multiplies the emitted rate (ADR-0028: it
        # composes multiplicatively with --freeway-scale and
        # --corridor-scale), so a counter incremented off `rate` reports the
        # demand that was AUTHORED rather than the demand in the file. Those
        # are the same number only while every rule has scale 1.0, which is
        # why this went unnoticed until a library used a scale.
        pf, pscale = profile_for("portal", cls, corr)
        # Relocate a share of this corridor's inflow to interior points. The
        # volume is not lost — it is re-emitted below, spread along the
        # corridor, so the corridor still carries what --freeway-scale asked
        # for. What changes is that it does not all arrive through one lane.
        # Taken off the pre-profile rate because relocation is a split of
        # PHYSICAL demand; the interior flows then carry the interior rule's
        # own scale, which need not equal this one's. Guarded on pf: a
        # profile-less flow is never emitted, and its relocated share must
        # not ship anyway as interior injections — an absent flow appears in
        # NO realized total, including via the detour through relocate.
        if pf is not None and args.ramp_share and corr in ramp_picks and cls in FREEWAY_CLASSES:
            moved = rate * args.ramp_share
            relocate[corr] += moved
            rate -= moved
        # What emit_flow will be handed. A profile-less flow resolves to
        # pscale 0.0 and is not emitted, so it contributes nothing here — an
        # absent flow must not appear in a realized total.
        emitted = rate * pscale
        if not thru:
            stats["portal no reachable exit"] += 1
        if pf is None:
            stats["portal no profile"] += 1
            continue
        if not work:
            stats["portal no reachable workplace"] += 1
            # After the pf check: a profile-less flow is never emitted, so it
            # must not appear in the stranded list either (the tally's rule —
            # an absent flow appears in no realized total — applies here).
            # Counted in VEHICLES over the program, on the same terms as the
            # realized totals.
            stranded.append((f"p{i:03d}-{cls}", lid, cls,
                             total_veh([(s, e, emitted * f) for s, e, f in pf
                                        if emitted * f > 0])))
        # Tallied here rather than above, so a flow that is never emitted is
        # never counted: an absent flow must not appear in a realized total.
        tally("freeway" if cls in FREEWAY_CLASSES else "arterial", emitted, pf)
        tally("through", emitted * eff, pf)
        emit_flow(flows, f"p{i:03d}-{cls}", lid, emitted, pf,
                  {"car": round(1 - truck, 3), "truck": round(truck, 3)}, d,
                  observe=observe)
        stats["portal flows"] += 1
    if stranded:
        # Loud because it is invisible downstream: the flow still loads, still
        # runs, and still produces plausible corridor speeds. It just sends
        # every vehicle straight back out of the box.
        lost = sum(r for _, _, _, r in stranded)
        print(f"[mkod] WARNING: {len(stranded)} portal origin(s) can reach NO "
              f"workplace destination, so 100% of their {lost:,.0f} vehicles "
              f"becomes through traffic regardless of --through-share:",
              file=sys.stderr)
        for fid, lid, cls, veh in stranded:
            print(f"[mkod]   {fid} ({cls}, {veh:,.0f} vehicles) origin {lid}",
                  file=sys.stderr)

    # --- Interior corridor origins: the on-ramps a map cut removes.
    #
    # A boundary-portal model gives a corridor its whole volume at one point,
    # and a single point cannot pass more than one lane's capacity however
    # much demand is aimed at it. What forms instead is a standing queue at
    # the cut face which METERS the mainline: everything downstream sees only
    # what got through, so it runs free no matter how high --freeway-scale
    # goes. The real Kennedy fills from Armitage, North, Division and Ohio,
    # not from one hose at the county line.
    #
    # The injection points are taken on the corridor MAINLINE rather than on
    # the merge lanes that feed it. The merge lanes are the honest geometry,
    # but on chi-loop-urban their median length is 6-8 m against a 16 m
    # minimum for a clearance-checked injection, so using them would silently
    # drop most of the relocated volume. Injecting on the mainline models a
    # merge that has already completed; merge turbulence itself is what the
    # merge-pod scenario is for.
    ramp_stats = collections.Counter()
    for corr, moved in sorted(relocate.items()):
        picks = ramp_picks[corr]
        each = moved / len(picks)
        for j, (lid, off, d, eff) in enumerate(picks):
            truck = args.truck  # a freeway mainline carries the freight mix
            pf, pscale = profile_for("interior", "motorway", corr)
            if pf is None:
                ramp_stats["interior no profile"] += 1
                continue
            emit_flow(flows, f"m{j:02d}-{corr}", lid, each * pscale, pf,
                      {"car": round(1 - truck, 3), "truck": round(truck, 3)},
                      d, offset=off, observe=observe)
            ramp_stats["interior flows"] += 1
            # On the same terms as the portal flows above: the interior rule's
            # scale and shape are its own, and are frequently the ones being
            # tuned — profiles-am.json puts these on `freight`, which never
            # reaches 1.0, so their base rate is not what reaches the network.
            tally("interior", each * pscale, pf)
            tally("through", each * pscale * eff, pf)
    if relocate:
        # `relocate` is a split of PHYSICAL demand, taken before any profile
        # applied, so it says how much moved off the cut face and nothing
        # about what arrives. The interior rule's own scale and shape land on
        # top of it: quoting the split alone reported an unchanged 5,258 veh/h
        # across a pair of libraries whose Kennedy demand differed by 1.57x.
        # Both are printed, on their own terms and labelled as such.
        int_pk, int_at = peak_rate(cats("interior"))
        print(f"[mkod] ramp-share {args.ramp_share}: moved "
              f"{sum(relocate.values()):,.0f} veh/h of base demand off the cut "
              f"face onto {ramp_stats['interior flows']} interior points across "
              f"{len(relocate)} corridors "
              f"({args.ramps_per_corridor} requested per corridor). As "
              f"EMITTED, after each corridor's profile scale and shape: peak "
              f"{int_pk:,.0f} veh/h at t={int_at:.0f}s, "
              f"{total_veh(cats('interior')):,.0f} vehicles.", file=sys.stderr)
        for k, v in sorted(ramp_stats.items()):
            if k != "interior flows":
                print(f"[mkod]   {k}: {v}", file=sys.stderr)

    # Printed here, not with the portal totals, because the interior flows
    # above carry through traffic too and the share has to cover both.
    #
    # On a VEHICLE COUNT, not a rate. A through share is a property of the
    # whole demand program, and the two sides of the ratio only compare if
    # they are integrated over the same span — which peak rates are not, once
    # different flows run on different shapes. Residents are excluded on
    # purpose: they start in-zone and have no boundary-exit movement, so
    # folding them in would shrink the share without any through trip changing.
    inflow_veh = total_veh(cats("arterial", "freeway", "interior"))
    if inflow_veh:
        thru_veh = total_veh(cats("through"))
        print(f"[mkod] through traffic: {thru_veh:,.0f} of {inflow_veh:,.0f} "
              f"vehicles of portal+interior inflow "
              f"({100 * thru_veh / inflow_veh:.0f}%) exits at the boundary; "
              f"the rest terminates in-zone. Through trips CLOSE the mass "
              f"balance — without them inflow has no drain and vehicle count "
              f"grows without bound.", file=sys.stderr)

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
        pf, pscale = profile_for("resident", None, lane_corridor.get(lid))
        if pf is None:
            stats["resident no profile"] += 1
            continue
        tally("resident", rate * pscale, pf)
        emit_flow(flows, f"r{i:03d}-resident", lid, rate * pscale, pf,
                  {"car": 1.0}, d, offset=off, observe=observe)
        stats["resident flows"] += 1

    # The demand level, read off the file rather than off the flags. Printed
    # after the resident loop because residents are inflow too, and after the
    # interior loop because the boundary freeway figure is only a remainder
    # until relocation has been re-emitted — printed earlier it showed a
    # corridor at 1 - --ramp-share of its own demand, which reads as a
    # plausible number rather than a missing one.
    #
    # Peak is a MAX OVER TIME of the summed rate, and the instant is named: a
    # sum of per-flow peaks is a rate the run never sees, because ADR-0028
    # exists precisely so that flows crest at different moments. The vehicle
    # count is the integral, which is the figure to compare against the run
    # report's completed + stranded + active.
    peak, at_s = peak_rate(cats("arterial", "freeway", "interior", "resident"))
    if peak:
        fw_pk, _ = peak_rate(cats("freeway", "interior"))
        art_pk, _ = peak_rate(cats("arterial"))
        res_pk, _ = peak_rate(cats("resident"))
        print(f"[mkod] realized demand: peak {peak:,.0f} veh/h at t={at_s:.0f}s "
              f"(freeway incl. interior {fw_pk:,.0f} + arterial {art_pk:,.0f} + resident "
              f"{res_pk:,.0f}, each at its own crest so the parts need not sum "
              f"to the total), {total_veh(cats('arterial', 'freeway', 'interior', 'resident')):,.0f} "
              f"vehicles over the program. --total {args.total:.0f} sized the "
              f"base; --freeway-scale {args.freeway_scale}, --corridor-scale "
              f"and any profile `scale` and shape fraction multiplied on top. "
              f"Read the demand level here, not off the flags.",
              file=sys.stderr)

    # Where the file actually aims its vehicles. Printed unconditionally,
    # pinned or not: the CBD's share was an unexamined output of the building
    # extract for the whole life of this script, and the only reason nobody
    # questioned 44% is that nothing ever printed it.
    if lane2zone or zone_share:
        tot_r = sum(zone_realized.values())
        in_zone = tot_r - zone_realized.get("(through)", 0.0)
        print(f"[mkod] destinations by district "
              f"({in_zone:,.0f} vehicles terminating in-zone, "
              f"{zone_realized.get('(through)', 0.0):,.0f} through):",
              file=sys.stderr)
        for z, v in sorted(zone_realized.items(), key=lambda kv: -kv[1]):
            if z == "(through)":
                continue
            want = zone_share.get(z)
            note = ""
            if want is not None:
                got = v / in_zone if in_zone else 0.0
                # A pin is a request, not a guarantee: an origin that cannot
                # reach the district has its pinned share spread over what it
                # can reach. Naming the gap is the whole point of this line.
                note = (f"   (pinned {want:.0%}"
                        + (f", SHORT by {100 * (want - got):.1f} pts — some "
                           f"origins cannot reach it" if want - got > 0.005
                           else "") + ")")
            print(f"         {z:14s} {v:9,.0f} veh  "
                  f"{100 * v / in_zone if in_zone else 0:5.1f}%{note}",
                  file=sys.stderr)

    if pset is not None:
        pset.check_all_rules_fired(args.profiles)
        used = [f"assign[{i}]x{n}" for i, n in sorted(pset.hits.items())]
        if pset.default_hits:
            used.append(f"default({pset.default})x{pset.default_hits}")
        print(f"[mkod] profiles {args.profiles}: {len(pset.profiles)} shapes "
              f"over {pset.step_s:.0f}s steps spanning {pset.span_s:.0f}s; "
              f"{' '.join(used)}", file=sys.stderr)

    header = [
        "# Generated by scripts/chicago/mkod.py — building-anchored OD demand",
        "# (ADR-0021). Portal inflow + residential interior origins, both",
        f"# routed to workplace destinations weighted by floor area.",
        f"# Target {args.total:.0f} veh/h peak, {args.resident_share:.0%} originating in-zone.",
        # The REALIZED level and the knobs that produced it, in the artifact
        # rather than only on stderr. --total is a target, not an outcome:
        # --freeway-scale, --corridor-scale and each profile rule's `scale` and
        # shape fraction all multiply on top of it. A shipped scenario whose
        # header recorded only the target could not be checked against its own
        # run report without re-deriving the invocation by trial and error,
        # which is exactly what had to be done to reproduce this file.
        f"# REALIZED: peak {peak:,.0f} veh/h at t={at_s:.0f}s (max over TIME of"
        f" the summed rate, not a sum of per-flow peaks —",
        f"#   ADR-0028 shapes crest at different moments), "
        f"{total_veh(cats('arterial', 'freeway', 'interior', 'resident')):,.0f}"
        f" vehicles over the program.",
        f"#   Compare against the run report's completed + stranded + active."
        f" The count is NOMINAL — the integral the Poisson sampler draws from,"
        f" not the seed-dependent number it emits — so a small gap is sampling;"
        f" a large one is delivery, not demand.",
        f"# Knobs: --total {args.total:.0f} --freeway-scale"
        f" {args.freeway_scale} --ramp-share {args.ramp_share}"
        + (f" --ramps-per-corridor {args.ramps_per_corridor}"
           if args.ramp_share else "")
        + ("".join(f" --corridor-scale {k}={v:g}"
                   for k, v in sorted(corridor_scale.items()))
           if corridor_scale else "")
        + (f" --dest-zone-share {args.dest_zone_share}"
           if args.dest_zone_share else ""),
        f"# {stats['portal flows']} portal flows + {stats['resident flows']} residential flows,",
        f"# Through share {args.through_share:.0%} arterial / "
        f"{args.freeway_through_share:.0%} freeway portals — that fraction of"
        + " portal inflow is destined for a boundary EXIT rather than a",
        f"# workplace, which is what closes the mass balance.",
        f"# {len(dest_lanes)} workplace + {len(exit_lanes)} exit destination"
        f" lanes.",
        # The shapes AND the assignment. Recording only the shapes leaves the
        # header unable to distinguish two libraries that share a shape
        # library and assign it completely differently — and assignment is
        # where the demand level per corridor is actually decided, since a
        # rule carries its own `scale`. First match wins, so the ORDER is part
        # of the meaning and the rules are listed in it, not sorted.
        (f"# Profiles from {args.profiles}: "
         + "; ".join(f"{n}=[{','.join(f'{v:g}' for v in p)}]"
                     for n, p in sorted(pset.profiles.items()))
         + f" over {pset.step_s:.0f}s steps."
         + " Assigned (first match wins): "
         + "; ".join(
             "[" + ", ".join(f"{k}={v}" for k, v in sorted(m.items()))
             + f"] -> {name}" + (f" x{scale:g}" if scale != 1.0 else "")
             for m, name, scale in pset.rules)
         + f"; default {pset.default}."
         if pset is not None else
         "# Profile " + " ".join(f"{a:.0f}-{b:.0f}s@{f:.2f}"
                                 for a, b, f in profile)
         + (" (FLAT PEAK — a recording cut, not the AM ramp)."
            if args.flat_peak else
            f" (ramp/peak/taper over {args.slice_s:.0f}s slices"
            + (f", then {args.drain_s:.0f}s draining at "
               f"{args.drain_level:.0%} of peak)." if args.drain_s else "."))),
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
