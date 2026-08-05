#!/usr/bin/env python3
"""harvest.py — bounded first pull of traffic-flow literature from OpenAlex.

OpenAlex (https://api.openalex.org) is a free CC0 catalog of scholarly works:
no auth, ~10 req/s, cursor pagination. We run a fixed list of seed search
phrases covering the subfields of traffic-flow analysis we want represented
(LWR, car-following, MFD, three-phase, IDM, ...), pull a few
relevance-ranked pages per phrase, and cache every raw page verbatim under
data/raw/ so re-runs are cheap and the pull is auditable.

Outputs:
  data/raw/<slug>-p<n>.json  raw API pages, one file per query+page
  data/works.jsonl           deduplicated works table (normalized subset of
                             fields + which seed queries matched each work)
  research/first-pull.md     corpus summary: counts, decade histogram,
                             top-cited works, topic distribution, and a
                             sample of likely off-topic catches for the
                             phase-2 filter design.

usage: harvest.py [--max-pages N] [--refresh]
  --max-pages N  pages per seed query, 200 works/page (default 3)
  --refresh      ignore the raw cache and re-fetch every page
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

API = "https://api.openalex.org/works"
PER_PAGE = 200            # API max
REQUEST_GAP = 0.15        # seconds between requests; limit is ~10 req/s
RETRIES = 4               # per page, on 429/5xx

# Fields we keep. If the API rejects one (select validation changes
# occasionally), it is dropped at runtime and the run continues.
SELECT = ["id", "doi", "title", "publication_year", "authorships",
          "primary_location", "cited_by_count", "referenced_works",
          "topics", "keywords", "type", "open_access"]

SEED_QUERIES = [
    "traffic flow theory",
    "car-following model",
    "Lighthill Whitham Richards",
    "kinematic wave traffic",
    "fundamental diagram traffic flow",
    "cell transmission model",
    "macroscopic fundamental diagram",
    "microscopic traffic simulation",
    "traffic assignment",
    "three-phase traffic theory",
    "intelligent driver model",
    "lane changing model traffic",
    "stop-and-go waves",
    "traffic state estimation",
]

# Titles matching this are probably about networks/comms, not roads —
# used only to surface a sample for the "boundaries observed" note.
OFFTOPIC_RE = re.compile(
    r"(network|data|internet|web|packet|computer|telecommunication|wireless|5g|ip)"
    r".{0,40}traffic|traffic.{0,40}(network|data|internet|packet)", re.I)

# OpenAlex topics that mark a work as genuinely ours. Used for the
# "on-topic top 30" — the raw top-cited list is drowned by mega-cited
# off-field papers the ambiguous seed phrases catch ("cell transmission
# model" matches cell biology, "three-phase" matches power/fluids, ...).
ON_TOPIC_TOPICS = {
    "Traffic control and management",
    "Transportation Planning and Optimization",
    "Traffic Prediction and Management Techniques",
    "Traffic and Road Safety",
    "Urban Transport and Accessibility",
}

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RAW_DIR = os.path.join(ROOT, "data", "raw")
WORKS_PATH = os.path.join(ROOT, "data", "works.jsonl")
SUMMARY_PATH = os.path.join(ROOT, "research", "first-pull.md")


def slug(phrase: str) -> str:
    return re.sub(r"[^a-z0-9]+", "-", phrase.lower()).strip("-")


def mailto() -> str:
    """Polite-pool address: the repo's git user.email, if set."""
    try:
        out = subprocess.run(["git", "config", "user.email"],
                             capture_output=True, text=True, timeout=5)
        return out.stdout.strip()
    except Exception:
        return ""


def fetch(url: str, tries: int = RETRIES) -> dict:
    """GET JSON with polite backoff on 429/5xx; raise on anything else."""
    for attempt in range(tries):
        time.sleep(REQUEST_GAP)
        try:
            with urllib.request.urlopen(url, timeout=60) as r:
                return json.load(r)
        except urllib.error.HTTPError as e:
            if e.code in (429,) or 500 <= e.code < 600:
                if attempt < tries - 1:
                    time.sleep(2 ** attempt)
                    continue
            raise
    raise RuntimeError("unreachable")


def fetch_works_page(query: str, cursor: str, params: dict) -> dict:
    """One /works page. Drops rejected select fields on HTTP 403 and
    retries, so a schema change costs a field, not the run."""
    q = dict(params, search=query, cursor=cursor)
    while True:
        url = API + "?" + urllib.parse.urlencode(q)
        try:
            return fetch(url)
        except urllib.error.HTTPError as e:
            body = e.read().decode("utf-8", "replace") if e.fp else ""
            dropped = [f for f in q.get("select", "").split(",") if f and f in body]
            if e.code in (400, 403) and dropped:
                q["select"] = ",".join(
                    f for f in q["select"].split(",") if f not in dropped)
                print(f"  select: dropping rejected field(s): {', '.join(dropped)}",
                      file=sys.stderr)
                continue
            raise


def normalize(work: dict) -> dict:
    src = (work.get("primary_location") or {}).get("source") or {}
    return {
        "id": work.get("id"),
        "doi": work.get("doi"),
        "title": work.get("title"),
        "year": work.get("publication_year"),
        "type": work.get("type"),
        "authors": [{"id": (a.get("author") or {}).get("id"),
                     "name": (a.get("author") or {}).get("display_name")}
                    for a in work.get("authorships", [])],
        "venue": src.get("display_name"),
        "cited_by_count": work.get("cited_by_count", 0),
        "referenced_works": work.get("referenced_works") or [],
        "topics": [t.get("display_name") for t in work.get("topics", [])],
        "fields": sorted({(t.get("field") or {}).get("display_name")
                          for t in work.get("topics", [])} - {None}),
        "keywords": [k.get("display_name") for k in work.get("keywords", [])],
        "open_access": (work.get("open_access") or {}).get("is_oa"),
    }


def harvest(query: str, max_pages: int, params: dict, refresh: bool) -> list:
    """Pull up to max_pages for one seed query; returns normalized works.
    Cached raw pages are reused unless refresh is set."""
    tag = slug(query)
    works = []
    cursor = "*"
    for page in range(1, max_pages + 1):
        cache = os.path.join(RAW_DIR, f"{tag}-p{page}.json")
        if os.path.exists(cache) and not refresh:
            with open(cache) as f:
                data = json.load(f)
        else:
            data = fetch_works_page(query, cursor, params)
            with open(cache, "w") as f:
                json.dump(data, f)
        results = data.get("results", [])
        works.extend(normalize(w) for w in results)
        cursor = (data.get("meta") or {}).get("next_cursor")
        if not cursor or not results:
            break
    return works


def decade_hist(works: list) -> dict:
    hist = {}
    for w in works:
        if w["year"]:
            hist[(w["year"] // 10) * 10] = hist.get((w["year"] // 10) * 10, 0) + 1
    return dict(sorted(hist.items()))


def top_counts(works: list, key: str, n: int) -> list:
    counts = {}
    for w in works:
        for v in w[key]:
            counts[v] = counts.get(v, 0) + 1
    return sorted(counts.items(), key=lambda kv: -kv[1])[:n]


def bar(count: int, scale: int) -> str:
    return "#" * max(1, count // scale)


def write_summary(works: list, per_query: dict, max_pages: int) -> None:
    lines = []
    add = lines.append
    years = [w["year"] for w in works if w["year"]]
    add("# First pull — OpenAlex traffic-flow corpus")
    add("")
    add(f"Seed queries: {len(SEED_QUERIES)}, up to {max_pages} pages "
        f"({max_pages * PER_PAGE} works) each, relevance-ranked. "
        f"Raw pages cached in `data/raw/`; merged table in `data/works.jsonl`.")
    add("")
    add(f"**Unique works: {len(works)}** "
        f"(year range {min(years)}–{max(years)}).")
    add("")
    add("## Per-query counts (unique works contributed)")
    add("")
    add("| seed query | works | unique to this query |")
    add("|---|---|---|")
    for q in SEED_QUERIES:
        ids = per_query.get(q, [])
        only = sum(1 for w in works if w["matched_queries"] == [q])
        add(f"| {q} | {len(ids)} | {only} |")
    add("")
    add("## Publication-decade histogram")
    add("")
    hist = decade_hist(works)
    scale = max(1, max(hist.values()) // 40)
    add("```")
    for dec, n in hist.items():
        add(f"{dec}s  {n:>5}  {bar(n, scale)}")
    add("```")
    add("")
    add("## Top 30 works by citation count (unfiltered)")
    add("")
    add("Raw relevance pulls catch mega-cited off-field papers — this table "
        "documents that contamination; see the filtered table below.")
    add("")
    add("| # | title | year | venue | cited by |")
    add("|---|---|---|---|---|")
    for i, w in enumerate(sorted(works, key=lambda w: -w["cited_by_count"])[:30], 1):
        title = (w["title"] or "").replace("|", "\\|")
        add(f"| {i} | {title} | {w['year']} | {w['venue']} | {w['cited_by_count']} |")
    add("")
    add("## Top 30 on-topic works by citation count")
    add("")
    add(f"Filtered to works tagged with a traffic OpenAlex topic "
        f"({len(ON_TOPIC_TOPICS)} topics, see harvest.py `ON_TOPIC_TOPICS`). "
        "This is where the classics should surface.")
    add("")
    add("| # | title | year | venue | cited by |")
    add("|---|---|---|---|---|")
    on_topic = [w for w in works if ON_TOPIC_TOPICS & set(w["topics"])]
    add(f"On-topic works: **{len(on_topic)}** of {len(works)}.")
    add("")
    for i, w in enumerate(sorted(on_topic, key=lambda w: -w["cited_by_count"])[:30], 1):
        title = (w["title"] or "").replace("|", "\\|")
        add(f"| {i} | {title} | {w['year']} | {w['venue']} | {w['cited_by_count']} |")
    add("")
    add("## Topics and keywords (top 25 each)")
    add("")
    add("OpenAlex topic | works")
    add("|---|---")
    for name, n in top_counts(works, "topics", 25):
        add(f"{name} | {n}")
    add("")
    add("OpenAlex keyword | works")
    add("|---|---")
    for name, n in top_counts(works, "keywords", 25):
        add(f"{name} | {n}")
    add("")
    add("## Boundaries observed")
    add("")
    off = [w for w in works if w["title"] and OFFTOPIC_RE.search(w["title"])]
    add(f"Heuristic title scan flags **{len(off)} works** as probably about "
        "network/data traffic rather than road traffic (title matches "
        "`(network|data|internet|packet|...)…traffic`). Sample:")
    add("")
    for w in off[:40]:
        add(f"- {w['title']} ({w['year']}, {w['venue']})")
    add("")
    add("<!-- Hand-written filter notes for phase 2 go here, based on "
        "reading the sample above. -->")
    add("")
    with open(SUMMARY_PATH, "w") as f:
        f.write("\n".join(lines))


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--max-pages", type=int, default=3,
                    help="pages per seed query (default 3)")
    ap.add_argument("--refresh", action="store_true",
                    help="ignore the raw cache and re-fetch")
    args = ap.parse_args()

    os.makedirs(RAW_DIR, exist_ok=True)
    params = {"per-page": PER_PAGE, "select": ",".join(SELECT)}
    m = mailto()
    if m:
        params["mailto"] = m
        print(f"polite pool: mailto={m}")

    works_by_id = {}
    per_query = {}
    for q in SEED_QUERIES:
        try:
            got = harvest(q, args.max_pages, params, args.refresh)
        except Exception as e:
            print(f"ERROR query {q!r}: {e} — continuing", file=sys.stderr)
            continue
        per_query[q] = [w["id"] for w in got]
        for w in got:
            works_by_id.setdefault(w["id"], w).setdefault("matched_queries", [])
            if q not in works_by_id[w["id"]]["matched_queries"]:
                works_by_id[w["id"]]["matched_queries"].append(q)
        print(f"{q!r}: {len(got)} works ({len(works_by_id)} unique so far)")

    works = list(works_by_id.values())
    for w in works:
        w["matched_queries"].sort()
    with open(WORKS_PATH, "w") as f:
        for w in works:
            f.write(json.dumps(w) + "\n")
    print(f"wrote {len(works)} unique works -> {WORKS_PATH}")

    write_summary(works, per_query, args.max_pages)
    print(f"wrote summary -> {SUMMARY_PATH}")


if __name__ == "__main__":
    main()
