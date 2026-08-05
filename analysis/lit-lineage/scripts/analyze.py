#!/usr/bin/env python3
"""analyze.py — build the traffic-flow lineage graph from the corpus.

Reads data/corpus.jsonl (filtered + anchors) and produces:

  - citation graph: edge A->B when A's referenced_works contain B and
    both are in-corpus. References out of corpus are counted but not
    drawn.
  - main path: standard citation-network main-path analysis (Hummon &
    Doreian; Batagelj 2003). Arc traversal weights are Search Path
    Counts (SPC): for arc u->v, (# paths from any source to u) x
    (# paths from v to any sink). The key-route main path starts from
    the maximum-SPC arc and extends both ways along max-weight arcs.
    The graph is made acyclic first by ordering nodes (year, id) and
    dropping the rare "backward" arcs (same-year mutual citations,
    OpenAlex year noise).
  - co-authorship network: author-pair edge weights = co-authored
    in-corpus works; communities = connected components of the graph
    thresholded at MIN_COAUTH weight (simple, deterministic).
  - subfields: each work is bucketed by the first matching rule in
    SUBFIELD_RULES, applied to its OpenAlex topics + keywords. The
    mapping is explicit and documented below; order matters (most
    specific first). Notable approximations: "network / MFD" keys on
    the keyword "Diagram", OpenAlex's mangled form of "fundamental
    diagram"; macroscopic rules come before microscopic so CTM and
    three-phase CA papers land in macroscopic despite often carrying
    the "Microscopic traffic flow model" keyword.

Outputs:
  data/traffic_lineage.json  nodes (label, authors, year, venue,
                             cited_by_count, subfield, tags, note) +
                             citation edges with on_main_path flags
  data/coauthorship.json     separate author-pair edge list + clusters
  stdout                     stats for research/phase-2.md

usage: analyze.py
"""
import json
import os
import sys
from collections import defaultdict, deque

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORPUS_PATH = os.path.join(ROOT, "data", "corpus.jsonl")
LINEAGE_PATH = os.path.join(ROOT, "data", "traffic_lineage.json")
COAUTH_PATH = os.path.join(ROOT, "data", "coauthorship.json")

HUB_TOP_N = 25        # tag this many highest-in-degree works as hubs
MIN_COAUTH = 4        # co-authorship edges below this weight are dropped
LABEL_LEN = 60

# Subfield buckets, first matching rule wins. Each rule is a set of
# OpenAlex topic/keyword display names (see module docstring).
SUBFIELD_RULES = [
    ("ML prediction", {
        "Machine learning", "Deep learning", "Artificial neural network",
        "Reinforcement learning", "Convolutional neural network",
    }),
    ("macroscopic flow", {
        "Kinematic wave", "Traffic wave", "Cell Transmission Model",
        "Three-phase traffic theory",
        "Traffic congestion reconstruction with Kerner's three-phase theory",
        "Traffic equations", "Shock wave", "Jamming", "Density wave theory",
    }),
    ("car-following / microscopic", {
        "Microscopic traffic flow model", "Headway", "Cruise control",
        "Cooperative Adaptive Cruise Control", "Platoon",
        "Advanced driver assistance systems",
        # topic-level:
        "Autonomous Vehicle Technology and Safety",
    }),
    ("network / MFD", {
        "Diagram",   # OpenAlex's mangled "fundamental diagram" keyword
    }),
    ("traffic assignment", {
        "Variational inequality", "Traffic network", "Flow network",
    }),
    ("simulation tools", {
        "Traffic simulation", "Microsimulation", "VisSim",
    }),
    ("signal control", {
        "SIGNAL (programming language)", "Intersection (aeronautics)",
        "Advanced Traffic Management System",
    }),
]
SUBFIELD_OTHER = "other"


def subfield(work: dict) -> str:
    signals = set(work["topics"]) | set(work["keywords"])
    for name, keys in SUBFIELD_RULES:
        if signals & keys:
            return name
    return SUBFIELD_OTHER


def label(title: str) -> str:
    return title if len(title) <= LABEL_LEN else title[:LABEL_LEN - 1] + "…"


def build_graph(corpus: list):
    """Citation edges A->B (A cites B), both in-corpus. Returns
    (edges, out_adj, in_deg, n_refs, n_resolved)."""
    ids = {w["id"] for w in corpus}
    edges = set()
    n_refs = n_resolved = 0
    for w in corpus:
        for ref in w["referenced_works"]:
            n_refs += 1
            if ref in ids:
                n_resolved += 1
                if ref != w["id"]:
                    edges.add((w["id"], ref))
    out_adj = defaultdict(list)
    in_deg = defaultdict(int)
    for a, b in edges:
        out_adj[a].append(b)
        in_deg[b] += 1
    return edges, out_adj, in_deg, n_refs, n_resolved


def dagify(edges: list, order_key) -> list:
    """Keep only arcs pointing strictly backward in (year, id) order —
    a citation graph should be a DAG; this drops the ~1% of arcs that
    aren't (same-year mutual citations, OpenAlex year noise)."""
    keep = [(a, b) for a, b in edges if order_key(a) > order_key(b)]
    return keep


def topo_order(nodes: list, edges: list):
    """Kahn's algorithm; edges point citing->cited (new -> old), so the
    result runs newest-first. Assumes dagify already ran."""
    out_adj = defaultdict(list)
    indeg = {n: 0 for n in nodes}
    for a, b in edges:
        out_adj[a].append(b)
        indeg[b] += 1
    queue = deque(sorted(n for n in nodes if indeg[n] == 0))
    order = []
    while queue:
        u = queue.popleft()
        order.append(u)
        for v in sorted(out_adj[u]):
            indeg[v] -= 1
            if indeg[v] == 0:
                queue.append(v)
    if len(order) != len(nodes):
        raise RuntimeError("graph not acyclic after dagify")
    return order, out_adj


def spc_weights(nodes: list, edges: list, order: list, out_adj: dict):
    """Search Path Count per arc: fp[u] * bp[v]. Python ints are
    arbitrary precision — path counts explode combinatorially and that
    is fine."""
    in_adj = defaultdict(list)
    for a, b in edges:
        in_adj[b].append(a)
    fp = {}
    for u in order:                     # newest -> oldest
        fp[u] = (1 if not in_adj[u] else 0) + sum(fp[p] for p in in_adj[u])
    bp = {}
    for u in reversed(order):           # oldest -> newest
        bp[u] = (1 if not out_adj[u] else 0) + sum(bp[w] for w in out_adj[u])
    return {(a, b): fp[a] * bp[b] for a, b in edges}


def key_route(weights: dict, out_adj: dict):
    """Batagelj's key-route main path: from the max-SPC arc, walk to a
    source along max-weight in-arcs and to a sink along max-weight
    out-arcs. Returns the path newest-first."""
    in_adj = defaultdict(list)
    for a, b in weights:
        in_adj[b].append(a)
    (a, b), _ = max(weights.items(), key=lambda kv: (kv[1], kv[0]))
    head = [a]
    while in_adj[head[-1]]:
        u = head[-1]
        nxt = max(in_adj[u], key=lambda p: (weights[(p, u)], p))
        head.append(nxt)
    tail = [b]
    while out_adj[tail[-1]]:
        u = tail[-1]
        cands = [v for v in out_adj[u] if (u, v) in weights]
        if not cands:
            break
        tail.append(max(cands, key=lambda v: (weights[(u, v)], v)))
    # head was built oldest->newest (a is the older end); reverse it so
    # the whole path runs newest-first like tail does.
    return head[::-1] + tail


def coauthorship(corpus: list, author_cites: dict):
    """Author-pair weights over the corpus; communities = connected
    components at weight >= MIN_COAUTH. Deterministic throughout."""
    pair_w = defaultdict(int)
    for w in corpus:
        aids = sorted({a["id"] for a in w["authors"] if a["id"]})
        for i in range(len(aids)):
            for j in range(i + 1, len(aids)):
                pair_w[(aids[i], aids[j])] += 1
    adj = defaultdict(set)
    weight = {}
    for (a, b), n in pair_w.items():
        if n >= MIN_COAUTH:
            adj[a].add(b)
            adj[b].add(a)
            weight[(a, b)] = n
    seen, clusters = set(), []
    for start in sorted(adj):
        if start in seen:
            continue
        comp, stack = [], [start]
        seen.add(start)
        while stack:
            u = stack.pop()
            comp.append(u)
            for v in sorted(adj[u]):
                if v not in seen:
                    seen.add(v)
                    stack.append(v)
        clusters.append(sorted(comp))
    clusters.sort(key=lambda c: (-len(c), c))
    cluster_of = {}
    for i, c in enumerate(clusters):
        for a in c:
            cluster_of[a] = i
    return weight, clusters, cluster_of


def main() -> None:
    corpus = [json.loads(line) for line in open(CORPUS_PATH)]
    by_id = {w["id"]: w for w in corpus}
    print(f"corpus: {len(corpus)} works")

    # --- citation graph -------------------------------------------------
    edges_all, out_adj_all, in_deg, n_refs, n_resolved = build_graph(corpus)
    print(f"citation edges (in-corpus): {len(edges_all)}")
    print(f"references resolving in-corpus: {n_resolved}/{n_refs} "
          f"({100 * n_resolved / max(1, n_refs):.1f}%)")

    order_key = lambda wid: (by_id[wid]["year"] or 0, wid)
    edges = dagify(sorted(edges_all), order_key)
    print(f"backward arcs dropped for DAG: {len(edges_all) - len(edges)}")
    order, out_adj = topo_order(sorted(by_id), edges)

    # per-decade shape
    dec_nodes = defaultdict(int)
    dec_edges = defaultdict(int)
    for w in corpus:
        if w["year"]:
            dec_nodes[w["year"] // 10 * 10] += 1
    for a, _ in edges_all:
        if by_id[a]["year"]:
            dec_edges[by_id[a]["year"] // 10 * 10] += 1
    print("\ndecade | nodes | in-corpus edges | edges/node")
    for d in sorted(dec_nodes):
        print(f"  {d}s | {dec_nodes[d]:>5} | {dec_edges[d]:>6} | "
              f"{dec_edges[d] / dec_nodes[d]:.2f}")

    # --- main path ------------------------------------------------------
    weights = spc_weights(sorted(by_id), edges, order, out_adj)
    path = key_route(weights, out_adj)
    path_edges = set(zip(path, path[1:]))
    print(f"\nkey-route main path ({len(path)} works, oldest first):")
    for wid in reversed(path):
        w = by_id[wid]
        print(f"  {w['year']}  {w['title'][:72]}")

    # --- co-authorship --------------------------------------------------
    author_name = {}
    author_cites = defaultdict(int)
    for w in corpus:
        for a in w["authors"]:
            if a["id"]:
                author_name[a["id"]] = a["name"]
                author_cites[a["id"]] = max(author_cites[a["id"]],
                                            w["cited_by_count"])
    pair_w, clusters, cluster_of = coauthorship(corpus, author_cites)
    print(f"\nco-authorship: {len(pair_w)} pairs at weight >= {MIN_COAUTH}, "
          f"{len(clusters)} clusters")
    print("top clusters (size, most-cited authors):")
    for c in clusters[:10]:
        top = sorted(c, key=lambda a: (-author_cites[a], a))[:5]
        names = ", ".join(f"{author_name[a]} ({author_cites[a]})"
                          for a in top)
        print(f"  {len(c):>4} authors: {names}")

    # --- subfields ------------------------------------------------------
    for w in corpus:
        w["subfield"] = subfield(w)
    sub_counts = defaultdict(int)
    for w in corpus:
        sub_counts[w["subfield"]] += 1
    print("\nsubfield distribution:")
    for name, n in sorted(sub_counts.items(), key=lambda kv: -kv[1]):
        print(f"  {n:>5}  {name}")

    # --- tags + dataset -------------------------------------------------
    hubs = {wid for wid, _ in sorted(in_deg.items(), key=lambda kv: -kv[1])
            [:HUB_TOP_N]}
    nodes = []
    for w in corpus:
        tags = []
        if w.get("anchor"):
            tags.append("anchor")
        if w["id"] in set(path):
            tags.append("main-path")
        if w["id"] in hubs:
            tags.append("hub")
        nodes.append({
            "id": w["id"].rsplit("/", 1)[1],
            "label": label(w["title"]),
            "authors": [a["name"] for a in w["authors"]],
            "year": w["year"],
            "venue": w["venue"],
            "cited_by_count": w["cited_by_count"],
            "subfield": w["subfield"],
            "tags": tags,
            "note": "",
        })
    lineage = {
        "meta": {
            "source": "OpenAlex (CC0), corpus filtered from "
                      "analysis/lit-lineage/data/works.jsonl",
            "works": len(corpus),
            "citation_edges": len(edges_all),
            "main_path_works": len(path),
        },
        "nodes": nodes,
        "edges": [
            {"source": a.rsplit("/", 1)[1], "target": b.rsplit("/", 1)[1],
             "type": "cites", "on_main_path": (a, b) in path_edges}
            for a, b in sorted(edges_all)
        ],
    }
    with open(LINEAGE_PATH, "w") as f:
        json.dump(lineage, f, indent=1)
    print(f"\nwrote {len(nodes)} nodes, {len(lineage['edges'])} edges "
          f"-> {LINEAGE_PATH}")

    coauth = {
        "meta": {"min_weight": MIN_COAUTH, "pairs": len(pair_w),
                 "clusters": len(clusters)},
        "authors": {aid.rsplit("/", 1)[1]: {
            "name": author_name[aid],
            "max_cited_by": author_cites[aid],
            "cluster": cluster_of.get(aid)}
            for aid in sorted(author_name)},
        "edges": [
            {"source": a.rsplit("/", 1)[1], "target": b.rsplit("/", 1)[1],
             "type": "coauthor", "weight": n}
            for (a, b), n in sorted(pair_w.items())
        ],
    }
    with open(COAUTH_PATH, "w") as f:
        json.dump(coauth, f, indent=1)
    print(f"wrote {len(pair_w)} co-authorship edges -> {COAUTH_PATH}")


if __name__ == "__main__":
    sys.exit(main())
