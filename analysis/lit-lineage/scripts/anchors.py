#!/usr/bin/env python3
"""anchors.py — fetch pre-1960 (and other missing) classics into the corpus.

Relevance-ranked search never surfaces the field's roots (thin OpenAlex
coverage of 1930s-60s venues, 600 modern hits outrank them), so the
known classics are fetched directly: by DOI where one is known, else by
title search with the best title/year match taken. Raw responses are
cached under data/raw/anchors/ so re-runs are cheap and auditable.

Anchors already present in the corpus (LWR 1955, Richards 1956, Gipps
1981 surfaced in the harvest) are matched in place by DOI/normalized
title and just flagged. Everything found is marked `anchor: true` —
these become the root nodes of the lineage tree.

usage: anchors.py [--refresh]
  reads  data/corpus.jsonl
  writes data/corpus.jsonl   (anchors merged in, flagged)
  caches data/raw/anchors/<slug>-{doi,search}.json
"""
import argparse
import difflib
import json
import os
import sys
import urllib.error
import urllib.parse

from harvest import fetch, mailto, normalize

API = "https://api.openalex.org/works"
MAX_YEAR_DRIFT = 5      # accept a title match only within +-5 years
MIN_TITLE_RATIO = 0.75  # difflib ratio on normalized titles

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CORPUS_PATH = os.path.join(ROOT, "data", "corpus.jsonl")
CACHE_DIR = os.path.join(ROOT, "data", "raw", "anchors")

# The classic-era roots to hunt. doi=None means no DOI is known (venue
# too old); those go through title search. DOIs given here are the
# publisher DOIs; a 404 falls back to title search too.
ANCHORS = [
    {"slug": "greenshields-1935", "year": 1935, "doi": None,
     "title": "A Study of Traffic Capacity",
     "note": "Highway Research Board proceedings"},
    {"slug": "wardrop-1952", "year": 1952, "doi": None,
     "title": "Some Theoretical Aspects of Road Traffic Research",
     "note": "ICE proceedings"},
    {"slug": "reuschel-1950", "year": 1950, "doi": None,
     "title": "Fahrzeugbewegungen in der Kolonne",
     "note": "Oesterreichisches Ingenieur-Archiv (German)"},
    {"slug": "pipes-1953", "year": 1953, "doi": "10.1063/1.1721265",
     "title": "An Operational Analysis of Traffic Dynamics",
     "note": "Journal of Applied Physics"},
    {"slug": "lighthill-whitham-1955", "year": 1955,
     "doi": "10.1098/rspa.1955.0089",
     "title": "On kinematic waves II. A theory of traffic flow on long "
              "crowded roads", "note": "Proc. Royal Society A"},
    {"slug": "richards-1956", "year": 1956, "doi": "10.1287/opre.4.1.42",
     "title": "Shock Waves on the Highway", "note": "Operations Research"},
    {"slug": "chandler-1958", "year": 1958, "doi": "10.1287/opre.6.2.165",
     "title": "Traffic Dynamics: Studies in Car Following",
     "note": "Operations Research"},
    {"slug": "herman-1959", "year": 1959, "doi": "10.1287/opre.7.1.86",
     "title": "Traffic Dynamics: Analysis of Stability in Car Following",
     "note": "Operations Research"},
    {"slug": "newell-1961", "year": 1961, "doi": "10.1287/opre.9.2.209",
     "title": "Nonlinear Effects in the Dynamics of Car Following",
     "note": "Operations Research"},
    {"slug": "edie-1963", "year": 1963, "doi": None,
     "title": "Discussion of Traffic Stream Measurements and Definitions",
     "note": "Proc. 2nd Int. Symp. Theory of Traffic Flow"},
    {"slug": "gazis-1961", "year": 1961, "doi": "10.1287/opre.9.4.499",
     "title": "Nonlinear follow-the-leader models of traffic flow",
     "note": "Operations Research (GM research labs)"},
    {"slug": "underwood-1961", "year": 1961, "doi": None,
     "title": "Speed, volume, and density relationships",
     "note": "Yale Bureau of Highway Traffic report"},
    {"slug": "gipps-1981", "year": 1981, "doi": "10.1016/0191-2615(81)90037-0",
     "title": "A behavioural car-following model for computer simulation",
     "note": "Transportation Research B"},
    {"slug": "payne-1971", "year": 1971, "doi": None,
     "title": "Models of freeway traffic and control",
     "note": "Simulation Councils proceedings (FREFLO)"},
    {"slug": "whitham-1974", "year": 1974, "doi": None,
     "title": "Linear and Nonlinear Waves", "note": "Wiley monograph"},
]


def norm(s: str) -> str:
    return "".join(c for c in (s or "").lower() if c.isalnum())


def cached(name: str, url: str, refresh: bool) -> dict:
    path = os.path.join(CACHE_DIR, name)
    if os.path.exists(path) and not refresh:
        with open(path) as f:
            return json.load(f)
    try:
        data = fetch(url)
    except urllib.error.HTTPError as e:
        if e.code == 404:
            data = {"_not_found": True}
        else:
            raise
    with open(path, "w") as f:
        json.dump(data, f)
    return data


def pick(results: list, anchor: dict) -> dict:
    """Best title/year match among search results, or None."""
    best, best_score = None, 0.0
    for w in results:
        ratio = difflib.SequenceMatcher(
            None, norm(w.get("title")), norm(anchor["title"])).ratio()
        drift = abs((w.get("publication_year") or 0) - anchor["year"])
        if ratio >= MIN_TITLE_RATIO and drift <= MAX_YEAR_DRIFT:
            score = ratio - drift * 0.01
            if score > best_score:
                best, best_score = w, score
    return best


def find_in_corpus(corpus: list, anchor: dict) -> dict:
    # A DOI-bearing anchor is matched in-corpus only by exact DOI — a
    # fuzzy title hit must not suppress the DOI fetch ("Analysis of
    # Stability in Car Following" 1959 is a near-title of "Studies in
    # Car Following" 1958 and would shadow it).
    if anchor["doi"]:
        doi = f"https://doi.org/{anchor['doi']}"
        for w in corpus:
            if w["doi"] == doi:
                return w
        return None
    best, best_ratio = None, 0.0
    for w in corpus:
        ratio = difflib.SequenceMatcher(
            None, norm(w["title"]), norm(anchor["title"])).ratio()
        if ratio > best_ratio:
            best, best_ratio = w, ratio
    if best_ratio >= MIN_TITLE_RATIO and best and \
            abs((best["year"] or 0) - anchor["year"]) <= MAX_YEAR_DRIFT:
        return best
    return None


def hunt(anchor: dict, params: dict, refresh: bool) -> dict:
    """Fetch one anchor from the API; returns a raw work or None."""
    if anchor["doi"]:
        url = f"{API}/doi:{anchor['doi']}"
        data = cached(f"{anchor['slug']}-doi.json", url, refresh)
        if not data.get("_not_found"):
            return data
    q = dict(params, search=anchor["title"], **{"per-page": 5})
    url = API + "?" + urllib.parse.urlencode(q)
    data = cached(f"{anchor['slug']}-search.json", url, refresh)
    return pick(data.get("results", []), anchor)


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--refresh", action="store_true",
                    help="ignore the anchors cache and re-fetch")
    args = ap.parse_args()

    os.makedirs(CACHE_DIR, exist_ok=True)
    params = {}
    m = mailto()
    if m:
        params["mailto"] = m
        print(f"polite pool: mailto={m}")

    corpus = [json.loads(line) for line in open(CORPUS_PATH)]
    by_id = {w["id"]: w for w in corpus}
    found, missed = [], []
    for a in ANCHORS:
        hit = find_in_corpus(corpus, a)
        if hit:
            hit["anchor"] = True
            found.append((a, hit, "already in corpus"))
            continue
        raw = hunt(a, params, args.refresh)
        if raw is None:
            missed.append(a)
            print(f"  MISS {a['slug']}: {a['title']} ({a['year']})")
            continue
        w = normalize(raw)
        if w["id"] in by_id:      # fetched but already present
            w = by_id[w["id"]]
        else:
            corpus.append(w)
            by_id[w["id"]] = w
        w["anchor"] = True
        w.setdefault("matched_queries", [])
        w["traffic_signals"] = ["anchor (classic fetched directly)"]
        found.append((a, w, "fetched"))

    corpus.sort(key=lambda w: ((w["year"] or 9999), w["id"]))
    with open(CORPUS_PATH, "w") as f:
        for w in corpus:
            f.write(json.dumps(w) + "\n")

    print(f"\nanchors found: {len(found)} of {len(ANCHORS)}")
    print("| anchor | how | OpenAlex match | year | refs | cited by |")
    print("|---|---|---|---|---|---|")
    for a, w, how in found:
        print(f"| {a['slug']} | {how} | {w['title'][:45]} | {w['year']} "
              f"| {len(w['referenced_works'])} | {w['cited_by_count']} |")
    if missed:
        print("\nmissed:")
        for a in missed:
            print(f"  - {a['slug']}: {a['title']} ({a['year']}, {a['note']})")
    no_refs = [w["title"] for _, w, _ in found if not w["referenced_works"]]
    if no_refs:
        print(f"\nanchors with NO referenced_works in OpenAlex: {len(no_refs)}")
        for t in no_refs:
            print(f"  - {t[:70]}")
    print(f"wrote {len(corpus)} works -> {CORPUS_PATH}")


if __name__ == "__main__":
    sys.exit(main())
