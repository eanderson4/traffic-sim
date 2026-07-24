#!/usr/bin/env python3
"""osm-stop-nodes.py — resolve OSM highway=stop nodes to the junctions they
control and emit a PlainXML node-override file for the two-pass netconvert
stop-sign workflow. netconvert (verified on 1.27.1) IGNORES highway=stop
nodes on OSM import — every junction comes out "priority". The override
re-types the junctions in a second pass:

  netconvert --osm-files X.osm -o base.net.xml --proj.utm --no-turnarounds
  netconvert --sumo-net-file base.net.xml --node-files stops.nod.xml -o final.net.xml

Junction ids in the .net.xml are the OSM node ids (no --junctions.join),
so the override keys junctions by node id directly. Re-typed junctions
give their connections "s" (priority_stop) / "w" (allway_stop) states,
which netimport maps to row "stop" (engine/netimport rowClass).

Resolution (input is the NEW Overpass shape: node elements with tags,
ways with "nodes" ref arrays):

  - junction candidate: a node referenced by >= 2 distinct ways (shared
    reference covers way-end-meets-way junctions), EXCLUDING way splits:
    a node where exactly two ways of the same highway class AND same
    non-empty name/ref meet is one road split for mapping convenience,
    not an intersection — walks pass through it;
  - a stop node that IS a junction (tagged at the intersection itself),
    or carries stop=all, marks its junction ALL-way;
  - otherwise, for each way containing the stop node, walk honoring the
    direction tag (forward -> higher node index, backward -> lower,
    absent -> forward, both -> both) to the next junction candidate; that
    junction gets a stop on this DIRECTED arm (way id + walk sign — a
    through way has two arms, one per approach). Reaching the way's end
    without a junction skips (file boundary / dead end);
  - aggregate per junction: any all-way mark, or stopped arms covering
    all incident arms (a way contributes 2 arms when the junction is
    interior to it, 1 when it ends there, 1 when oneway) -> allway_stop;
    else priority_stop.

Known approximation: priority_stop lets netconvert pick WHICH connections
stop by road priority rather than pinning the OSM-signed approach. That
is correct in the common case (stop signs live on the minor approach);
per-approach fidelity would need connection-level overrides, which the
PlainXML node file cannot express. A walk also stops at ANY signalized
node it meets (even a mid-block pedestrian signal unrelated to the
target junction) — conservative under-marking, consistent with never
overriding a light. ADR-0017 records these decisions.

Usage: osm-stop-nodes.py overpass.json stops.nod.xml
"""
import json
import sys
from xml.sax.saxutils import escape

_QUOTES = {'"': "&quot;"}


def _attr(value: object) -> str:
    return escape(str(value), _QUOTES)


_ONEWAY = {"yes", "true", "1", "-1"}


def resolve(data: dict) -> dict[int, str]:
    """Overpass JSON -> {junction node id: "allway_stop"|"priority_stop"}."""
    ways = [el for el in data.get("elements", []) if el.get("type") == "way"]
    nodes = [el for el in data.get("elements", []) if el.get("type") == "node"]
    defaulted = 0  # stops with absent/unrecognized direction (reported)

    # node id -> distinct way ids referencing it (junction = shared by >= 2).
    ways_at: dict[int, set[int]] = {}
    way_nodes: dict[int, list[int]] = {}
    way_tags: dict[int, dict] = {}
    for w in ways:
        refs = w.get("nodes", [])
        way_nodes[w["id"]] = refs
        way_tags[w["id"]] = w.get("tags", {})
        for ref in refs:
            ways_at.setdefault(ref, set()).add(w["id"])

    # Signalized junctions (OSM node tagged traffic_signals) must never
    # receive a stop override: the second netconvert pass would REPLACE
    # the traffic_light with priority_stop/allway_stop and drop its
    # tlLogic — a light lost to a stop sign on one approach.
    signaled = {
        el["id"]
        for el in nodes
        if (el.get("tags") or {}).get("highway") == "traffic_signals"
    }

    def is_split(nid: int) -> bool:
        # Two ways, same road continuing: same class AND same non-empty
        # name/ref. Both-unnamed stays a junction (US city extracts name
        # nearly everything; erring toward junction misplaces a stop by
        # one node, erring toward split could move it whole blocks).
        ws = ways_at.get(nid, ())
        if len(ws) != 2:
            return False
        a, b = sorted(ws)
        ta, tb = way_tags[a], way_tags[b]
        if ta.get("highway") != tb.get("highway"):
            return False
        for key in ("name", "ref"):
            va, vb = ta.get(key, ""), tb.get(key, "")
            if va and va == vb:
                return True
        return False

    def is_junction(nid: int) -> bool:
        return len(ways_at.get(nid, ())) >= 2 and not is_split(nid)

    def oneway_tag(wid: int) -> str:
        # Explicit oneway tag, else OSM-implied oneway: roundabout
        # junction ways and motorway-class roads are one-way by default.
        ow = way_tags[wid].get("oneway", "")
        if ow == "" and way_tags[wid].get("junction") == "roundabout":
            return "yes"
        if ow == "" and way_tags[wid].get("highway", "").startswith("motorway"):
            return "yes"
        return ow

    def incident_arms(jid: int) -> int:
        total = 0
        for wid in ways_at.get(jid, ()):
            refs = way_nodes[wid]
            ow = oneway_tag(wid)
            if ow in _ONEWAY:
                # A oneway contributes an approach only where traffic can
                # ENTER the junction: its exit end (refs[-1], or refs[0]
                # for oneway=-1), or 1 when it passes through. A oneway
                # that merely STARTS here adds no arm at all.
                exit_end = refs[0] if ow == "-1" else refs[-1]
                if jid == exit_end:
                    total += 1
                elif jid != refs[0] and jid != refs[-1]:
                    total += 1  # interior: the movement passes through
            elif len(refs) > 2 and refs[0] == jid and refs[-1] == jid:
                total += 2  # loop way: both ends at this junction
            elif refs[0] == jid or refs[-1] == jid:
                total += 1  # way ends here
            else:
                total += 2  # interior: the way passes through, two approaches
        return total

    # junction id -> {"allway": bool, "arms": set of (way id, walk sign)}
    controlled: dict[int, dict] = {}

    def mark(jid: int, arm: tuple[int, int] | None, allway: bool) -> None:
        j = controlled.setdefault(jid, {"allway": False, "arms": set()})
        if allway:
            j["allway"] = True
        if arm is not None:
            j["arms"].add(arm)

    for el in nodes:
        tags = el.get("tags") or {}
        if tags.get("highway") != "stop":
            continue
        nid = el["id"]
        if nid in signaled:
            continue  # stop tag on the signal junction node itself; keep the light
        allway = tags.get("stop") == "all"
        if is_junction(nid):
            # Tagged AT the intersection. Only stop=all means all-way
            # (KB convention); a bare stop at a junction records stop
            # control but stops only the minor approaches (priority_stop).
            mark(nid, None, allway)
            continue
        direction = tags.get("direction")
        # Travel direction through the stop node when every way agrees on
        # one (explicit or implied oneway); used to reject against-flow
        # arms, which incident_arms() also refuses to count.
        ow_all = {oneway_tag(w) for w in ways_at.get(nid, ())}
        travel = None
        if ow_all and ow_all <= _ONEWAY:
            signs = {-1 if o == "-1" else 1 for o in ow_all}
            if len(signs) == 1:
                travel = signs.pop()
        if direction in ("forward", "backward", "both"):
            steps = {"forward": (1,), "backward": (-1,), "both": (1, -1)}[direction]
            if travel is not None:
                steps = tuple(s for s in steps if s == travel)
        else:
            # Absent/unrecognized direction (ADR-0017 §4): infer from the
            # way's oneway tag when every way here agrees on one travel
            # direction (oneway=-1 runs against node order); else forward.
            defaulted += 1
            ow = {oneway_tag(w) for w in ways_at.get(nid, ())}
            if ow == {"-1"}:
                steps = (-1,)
            elif ow == {"yes"} or ow == {"true"} or ow == {"1"}:
                steps = (1,)
            else:
                steps = (1,)
        for wid in sorted(ways_at.get(nid, ())):
            try:
                i = way_nodes[wid].index(nid)
            except ValueError:
                continue
            for step in steps:
                # Per-step state reset: cur/refs/j/seen — the split hop
                # rebinds refs and it must NOT leak into the next step's
                # walk (direction=both runs two walks over the same way).
                cur, refs, j, seen = wid, way_nodes[wid], i + step, {wid}
                while True:
                    while 0 <= j < len(refs):
                        cand = refs[j]
                        if cand in signaled:
                            break  # never override a signalized junction
                        if is_junction(cand):
                            # Arm = the segment that ENTERS the junction
                            # (after split hops, not the stop's own way).
                            mark(cand, (cur, step), allway)
                            break
                        j += step
                    else:
                        # Way ended. At a way split the road continues on
                        # the other way — hop and keep walking (cycles
                        # guarded by seen). True boundary: skip.
                        end = refs[0] if step < 0 else refs[-1]
                        nxt = (ways_at.get(end, ()) - seen)
                        if not is_split(end) or not nxt:
                            break
                        wid2 = nxt.pop()
                        seen.add(wid2)
                        refs2 = way_nodes[wid2]
                        if refs2[0] == end:
                            cur, refs, j, step = wid2, refs2, 1, 1
                        elif refs2[-1] == end:
                            cur, refs, j, step = wid2, refs2, len(refs2) - 2, -1
                        else:
                            break  # loop way meeting itself; give up
                        continue
                    break

    out: dict[int, str] = {}
    for jid, j in controlled.items():
        total = incident_arms(jid)
        if j["allway"] or (total and len(j["arms"]) >= total):
            out[jid] = "allway_stop"
        else:
            out[jid] = "priority_stop"
    if defaulted:
        print(
            f"osm-stop-nodes: {defaulted} stop nodes had no usable direction tag "
            "(inferred from oneway where possible, else forward — ADR-0017 §4)",
            file=sys.stderr,
        )
    return out


def render(mapping: dict[int, str]) -> str:
    lines = ["<nodes>\n"]
    for nid in sorted(mapping):
        lines.append(f'  <node id="{_attr(nid)}" type="{_attr(mapping[nid])}"/>\n')
    lines.append("</nodes>\n")
    return "".join(lines)


def main() -> None:
    if len(sys.argv) != 3:
        sys.exit("usage: osm-stop-nodes.py overpass.json stops.nod.xml")
    src, dst = sys.argv[1], sys.argv[2]

    with open(src) as f:
        data = json.load(f)

    mapping = resolve(data)
    xml = render(mapping)
    if len(mapping) == 0:
        # Empty override file: callers skip the second netconvert pass.
        xml = ""
    with open(dst, "w") as out:
        out.write(xml)

    n_all = sum(1 for t in mapping.values() if t == "allway_stop")
    n_prio = len(mapping) - n_all
    print(
        f"{src}: {len(mapping)} stop junctions ({n_all} allway_stop, {n_prio} priority_stop) -> {dst}",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
