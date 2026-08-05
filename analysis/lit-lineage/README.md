# lit-lineage — a lineage graph of traffic-flow research

An open, CC0-sourced academic-literature lineage of traffic-flow research:
who published what, who cites whom, and how the field's main ideas (LWR,
car-following, fundamental diagram, MFD, three-phase, IDM, ...) descend
from one another. Built from [OpenAlex](https://openalex.org), a free CC0
catalog of scholarly works.

## Plan

1. **Harvest the corpus.** Bounded seed-query pulls from the OpenAlex
   `/works` API; raw pages cached, merged into a deduplicated works table.
   *Done — see `research/first-pull.md`.*
2. **Analyze.** Citation / co-authorship / main-path analysis over the
   works table → a lineage graph (seminal papers, schools, key edges).
   *Done — filter + classic-era anchors + SPC key-route main path, see
   `research/phase-2.md`; dataset in `data/traffic_lineage.json`.*
3. **Present.** Interactive web viz on the traffic-sim site, plus a Manim
   video for *Math vs Vibes*.

## Re-running the harvest

From the repo root (Python 3.9+, standard library only):

```
python3 analysis/lit-lineage/scripts/harvest.py              # cached pages reused
python3 analysis/lit-lineage/scripts/harvest.py --refresh    # re-fetch everything
python3 analysis/lit-lineage/scripts/harvest.py --max-pages 5
```

Raw API pages land in `data/raw/`, the merged works table in
`data/works.jsonl`, and the corpus summary in `research/first-pull.md`.

## Re-running the analysis

```
python3 analysis/lit-lineage/scripts/filter.py       # works.jsonl -> corpus.jsonl
python3 analysis/lit-lineage/scripts/anchors.py      # fetch/flag classic-era anchors
python3 analysis/lit-lineage/scripts/analyze.py      # graph, main path, dataset JSON
python3 analysis/lit-lineage/scripts/export_csv.py   # dataset JSON -> nodes/edges CSV
```

## Provenance and licensing

All metadata comes from OpenAlex, which is CC0; if you use the data, cite
Priem, Piwowar & Orr (2022), *OpenAlex: A fully-open index of scholarly
works, authors, venues, institutions, and concepts*
(https://arxiv.org/abs/2205.01833). We ship bibliographic metadata and
citation edges only — full text is linked out via DOI, never copied.

## Relation to sci-fi-lineage

This sub-project will later mirror the structure of the sibling
sci-fi-lineage project: a dataset JSON as the source of truth, generated
CSVs, and web + Manim outputs derived from it.
