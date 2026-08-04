#!/usr/bin/env python3
"""filter.py — turn the raw harvest into a clean on-topic corpus.

The seed queries caught off-field work through ambiguous phrases
("cell transmission model" -> cell biology, "three-phase" -> power/5G,
"stop-and-go waves" -> medical papers; see research/first-pull.md).
Title heuristics have bad false positives (Daganzo's "Network traffic"
means *road* networks), so the filter keys on OpenAlex's own topic and
keyword annotations instead: a work is kept iff it carries at least one
allowlisted road-traffic topic or keyword. The allowlists below are
explicit and were built by inspecting the actual values in
data/works.jsonl.

Garbage handling (cases documented in first-pull.md):
  - blank-title records are dropped (one carries a corrupted 50k
    citation count);
  - HTML tags are stripped from titles (<b>lmerTest</b> et al.);
  - works are deduplicated by DOI, then by normalized title — catches
    near-duplicate records with corrupted years (a 1928-dated duplicate
    of the 2010 "Enhanced intelligent driver model" paper has no DOI);
  - cited_by_count above MAX_CITATIONS is implausible for this corpus
    and the record is dropped (catches the corrupted 50k-count record;
    the only real works it removes — SciPy, QUANTUM ESPRESSO — are
    off-topic and the topic filter would drop them anyway).

usage: filter.py
  reads  data/works.jsonl
  writes data/corpus.jsonl   (works that pass, keep/drop stats on stdout)
"""
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORKS_PATH = os.path.join(ROOT, "data", "works.jsonl")
CORPUS_PATH = os.path.join(ROOT, "data", "corpus.jsonl")

MAX_CITATIONS = 25000   # above this the count is assumed corrupted

# OpenAlex topics that mark a work as road-traffic research. Extends
# harvest.py's ON_TOPIC_TOPICS with two more road-vehicle topics.
# Deliberately NOT allowlisted: "Evacuation and Crowd Dynamics"
# (pedestrian dynamics is a separate lineage), "Vehicular Ad Hoc
# Networks (VANETs)" and "Network Traffic and Congestion Control"
# (comms networks), "Air Traffic Management and Optimization".
TRAFFIC_TOPICS = {
    "Traffic control and management",
    "Transportation Planning and Optimization",
    "Traffic Prediction and Management Techniques",
    "Traffic and Road Safety",
    "Urban Transport and Accessibility",
    "Transportation and Mobility Innovations",
    "Autonomous Vehicle Technology and Safety",
}

# OpenAlex keywords that mark a work as road-traffic research. Several
# are OpenAlex-mislabeled but de-facto road-traffic terms (checked by
# sampling the works carrying them):
#   "Traffic flow (computer networking)"  OpenAlex's generic traffic-flow
#       keyword; pulls a handful of genuine networking papers (~5 of 91
#       works that carry it with no traffic topic) but mostly classics.
#   "Intersection (aeronautics)"          road intersections.
#   "SIGNAL (programming language)"       traffic signals (~1 FP in 17).
TRAFFIC_KEYWORDS = {
    "Traffic flow (computer networking)",
    "Transport engineering",
    "Traffic congestion",
    "Traffic generation model",
    "Traffic simulation",
    "Microscopic traffic flow model",
    "Traffic congestion reconstruction with Kerner's three-phase theory",
    "Three-phase traffic theory",
    "Cell Transmission Model",
    "Traffic wave",
    "Kinematic wave",
    "Traffic model",
    "Traffic optimization",
    "Traffic equations",
    "Microsimulation",
    "Intelligent transportation system",
    "Floating car data",
    "Headway",
    "Platoon",
    "Cruise control",
    "Cooperative Adaptive Cruise Control",
    "Advanced driver assistance systems",
    "Automotive engineering",
    "Travel time",
    "Highway Capacity Manual",
    "Highway engineering",
    "Highway system",
    "Intersection (aeronautics)",
    "SIGNAL (programming language)",
    "Public transport",
}

TAG_RE = re.compile(r"<[^>]+>")
WS_RE = re.compile(r"\s+")
NORM_RE = re.compile(r"[^a-z0-9]+")


def clean_title(title: str) -> str:
    return WS_RE.sub(" ", TAG_RE.sub("", title or "")).strip()


def norm_title(title: str) -> str:
    return NORM_RE.sub("", title.lower())


def rank(work: dict) -> tuple:
    """Dedup preference: real records beat corrupted near-duplicates."""
    return (bool(work["doi"]), bool(work["referenced_works"]),
            work["cited_by_count"], len(work["authors"]))


def traffic_signals(work: dict) -> list:
    """The allowlisted topic/keyword signals a work carries (may be [])."""
    return sorted((TRAFFIC_TOPICS & set(work["topics"]))
                  | (TRAFFIC_KEYWORDS & set(work["keywords"])))


def main() -> None:
    works = [json.loads(line) for line in open(WORKS_PATH)]
    dropped = {"blank title": 0, "corrupt citation count": 0,
               "duplicate (same DOI)": 0, "duplicate (same title)": 0,
               "no traffic topic/keyword signal": 0}

    # Clean titles, drop garbage.
    clean = []
    for w in works:
        w["title"] = clean_title(w["title"])
        if not w["title"]:
            dropped["blank title"] += 1
            continue
        if w["cited_by_count"] > MAX_CITATIONS:
            dropped["corrupt citation count"] += 1
            continue
        clean.append(w)

    # Dedup: by DOI where present, then by normalized title (catches
    # corrupted-year near-duplicates that carry no DOI).
    best = {}
    for w in clean:
        key = ("doi", w["doi"]) if w["doi"] else ("id", w["id"])
        if key in best:
            dropped["duplicate (same DOI)"] += 1
            if rank(w) > rank(best[key]):
                best[key] = w
        else:
            best[key] = w
    by_title = {}
    for w in best.values():
        key = norm_title(w["title"])
        if key in by_title:
            dropped["duplicate (same title)"] += 1
            if rank(w) > rank(by_title[key]):
                by_title[key] = w
        else:
            by_title[key] = w

    # Topic/keyword filter.
    corpus = []
    for w in by_title.values():
        signals = traffic_signals(w)
        if not signals:
            dropped["no traffic topic/keyword signal"] += 1
            continue
        w["traffic_signals"] = signals
        corpus.append(w)
    corpus.sort(key=lambda w: ((w["year"] or 9999), w["id"]))

    with open(CORPUS_PATH, "w") as f:
        for w in corpus:
            f.write(json.dumps(w) + "\n")

    kept_by_decade = {}
    for w in corpus:
        if w["year"]:
            dec = (w["year"] // 10) * 10
            kept_by_decade[dec] = kept_by_decade.get(dec, 0) + 1

    print(f"read {len(works)} works from {WORKS_PATH}")
    for why, n in dropped.items():
        print(f"  dropped {n:>5}  {why}")
    print(f"  kept    {len(corpus):>5}  -> {CORPUS_PATH}")
    print("kept by decade: " + ", ".join(f"{d}s:{n}" for d, n in
                                         sorted(kept_by_decade.items())))
    # Sample of dropped works that *look* traffic-ish, for audit.
    suspicious = [w for w in works
                  if w["title"] and "traffic" in w["title"].lower()
                  and not traffic_signals(w)]
    print(f"audit: {len(suspicious)} dropped works have 'traffic' in the "
          f"title but no signal (sample):")
    for w in suspicious[:8]:
        print(f"  - {w['title'][:75]} ({w['year']})")


if __name__ == "__main__":
    sys.exit(main())
