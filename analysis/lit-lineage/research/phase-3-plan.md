# Phase 3 plan — interactive lineage tree + video

Inputs (phase 2): `data/traffic_lineage.json` (5,466 nodes / 49,973
`cites` edges, SPC main path of 52 works flagged), `data/coauthorship.json`
(511 author-pair edges, 96 school clusters). Reference implementation:
the sibling `sci-fi-lineage` project (337-line D3 `web/main.js`, static,
no build; Manim scenes for the video).

Deliverables: (a) an interactive lineage tree published on the
traffic-sim site, linked from the splash page's analysis row (the
"coming soon" card); (b) Manim scenes for a Math vs Vibes short.

## 1. Curation layer (hand edits, kept separate from generated data)

`data/` is gitignored (root `.gitignore` convention), and generated
files get overwritten by the pipeline — so hand curation lives in a
tracked file: `curation/edits.yaml` (or JSON), applied by a new
`scripts/curate.py` as the LAST pipeline step:

- **Asserted root layer**: Greenshields 1935 → Wardrop 1952 → LWR 1955
  / Richards 1956 → Chandler 1958. Documented influence, but OpenAlex
  has no outgoing refs for these records (phase-2 §2), so no computed
  edge can exist. Each asserted edge carries a `note` with the receipt.
- **Missing nodes**: Reuschel 1950, Edie 1963, Underwood 1961, Godfrey
  1969 — hand-added with `provenance: hand` (phase-2 §8; their absence
  from the index is itself a video beat).
- **Contested-first flags**: `disputed: true` pairs, mirroring the
  sci-fi project's dashed-red edges — (a) first car-following:
  Reuschel vs Pipes vs Chandler–Herman–Montroll; (b) first fundamental
  diagram: Greenshields vs Underwood vs Edie; (c) kinematic-wave
  priority: LWR vs Richards (908 vs 920 in-corpus citations — a tie);
  (d) MFD: Godfrey 1969 vs Daganzo 2007/08 rediscovery.
- **One-line notes** for the ~50 video-relevant nodes (the `note`
  field exists and is empty).

## 2. Pruning for legibility (the open decision)

50k edges is not drawable; 5.5k nodes is not browsable. Plan:

1. Export SPC weights per edge (one-line change in `analyze.py` — the
   weights are already computed) → principled density slider.
2. Default view: anchors + main path + hubs (top in-degree) + asserted
   roots ≈ 150–300 nodes — the "spine" the video also uses.
3. Everything else reachable by interaction: click a node → ego network
   (in/out citations, capped by SPC weight); subfield filter reveals
   that field's local hubs.

Decision needed before viz work: the default-view node budget and
whether the slider is SPC-weight or per-node top-k. Leaning SPC —
it's computed, principled, and gives the video a defensible "why these
edges" answer.

## 3. Web viz

`analysis/lit-lineage/web/` — static, no build, D3 from CDN, cloned
from sci-fi-lineage's `web/` and adapted:

- x = publication year (piecewise scale, 1935→2026), y = layout force;
  node size = cited_by_count; color = subfield (8 buckets from
  `analyze.py:SUBFIELD_RULES`).
- Edge rendering: main-path edges emphasized; `disputed` edges dashed
  red; a "spine only / full ego" density toggle.
- Hover tooltip (title, authors, venue, citation count, note);
  click-to-isolate ego lineage; search box; subfield/school filters.
- A second view (or overlay) for the co-authorship schools from
  `coauthorship.json` — cluster hulls labeled with the school names
  from phase-2 §5.
- Publishing: static files, so the cheapest path is copying the bundle
  into the deployed site (demosrv serves `viz/dist` — either add the
  lineage page as a fourth vite input, or serve it as a plain static
  directory). Decide at build time; splash card already reserves the
  slot. The dataset JSON it loads (~1–2 MB pruned) ships with the
  bundle, not via git.

## 4. Video (Manim)

`analysis/lit-lineage/manim/`, mirroring sci-fi-lineage's scenes
(manim toolchain in a local venv, gitignored, install notes in README):

- `LineageBuild` — the tree grows node-by-node chronologically along
  the main path + asserted roots, running year counter. The Kerner
  2000s block is the "citation mass ≠ textbook canon" beat (phase-2 §4)
  — the graph insists on a story the textbooks don't lead with; that's
  the math-vs-vibes thesis on camera.
- `ContestedBeats` — the four contested-crown cards from §1.
- `SchoolsMap` (stretch) — co-authorship clusters assembling as
  constellations: Dresden, Duisburg, Berkeley, Geroliminis MFD.

## 5. Open questions (for review)

1. SPC-weight slider vs per-node top-k for the default view (§2)?
2. Is the asserted-root mechanism (curation file, receipts in `note`)
   the right way to handle index gaps, or should we backfill via
   `cited_by` reverse-lookups first (phase-2 §8 option)?
3. Dataset distribution: pruned JSON ships inside the web bundle
   (regenerable, gitignored) — any objection to keeping it out of git?
4. Video framing: lean into "the index itself is biased" (Reuschel
   absent, Kerner over-weighted) as the second act?
