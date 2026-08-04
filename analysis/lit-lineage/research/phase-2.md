# Phase 2 — from corpus to lineage graph

Inputs: `data/works.jsonl` (7,264 works, phase 1). Pipeline:
`scripts/filter.py` → `scripts/anchors.py` → `scripts/analyze.py` →
`scripts/export_csv.py`. Outputs: `data/corpus.jsonl` (5,466 works),
`data/traffic_lineage.json`, `data/coauthorship.json`,
`data/nodes.csv`, `data/edges.csv`.

## 1. Filter design and keep/drop stats

First-pull showed the seed-query contamination comes from ambiguous
phrases ("cell transmission model" → cell biology, "three-phase" →
power/5G, "stop-and-go waves" → medical), and that title heuristics
have bad false positives ("The cell transmission model, part II:
Network traffic" is about *road* networks). So the filter keys on
OpenAlex's own annotations: a work is kept iff it carries at least one
allowlisted traffic **topic** (7 display names) or **keyword** (28
display names) — the lists are explicit in `filter.py`
(`TRAFFIC_TOPICS`, `TRAFFIC_KEYWORDS`), built by inspecting the actual
values in works.jsonl and sampling borderline cases.

Deliberate boundary calls:

- **Excluded topics**: Evacuation and Crowd Dynamics (pedestrian
  dynamics is a separate lineage), VANETs and Network Traffic and
  Congestion Control (comms networks), Air Traffic Management.
- **Mislabeled-but-ours keywords included**: "Traffic flow (computer
  networking)" is OpenAlex's generic traffic-flow keyword — of the 91
  works carrying it with no traffic topic, only ~5 are genuine
  networking papers ("Traffic theory and the Internet"), the rest are
  road-traffic classics. "Intersection (aeronautics)" = road
  intersections; "SIGNAL (programming language)" = traffic signals
  (~1 false positive in 17).
- **Garbage handling**: blank-title records dropped (one carries a
  corrupted 50,423 citation count); HTML stripped from titles
  (`<b>lmerTest</b>`); cited_by_count > 25,000 dropped as implausible
  for this corpus (only removes SciPy/QUANTUM ESPRESSO, which the
  topic filter would drop anyway); dedup by DOI, then by normalized
  title — the title pass catches the 1928-dated duplicate of the 2010
  "Enhanced intelligent driver model" paper, which carries no DOI.

Result:

| action | works |
|---|---|
| read | 7,264 |
| dropped: blank title | 2 |
| dropped: corrupt citation count | 2 |
| dropped: duplicate (same DOI) | 8 |
| dropped: duplicate (same normalized title) | 186 |
| dropped: no traffic topic/keyword signal | 1,609 |
| **kept** | **5,457** |

Audit of the 243 dropped works that have "traffic" in the title but no
signal: mostly genuine contamination (packet-switched gateways, organ
trade, network anomaly detection) plus some real losses with empty
OpenAlex annotations ("An Introduction to Traffic Flow Theory", 1965 —
a Foote/Gerlough-era piece; "Jamming transition in the traffic-flow
model with two-level crossings", 1993). Precision over recall: we lose
a few dozen real traffic papers that carry no topics/keywords at all.
Acceptable for a lineage graph; noted as a known gap.

Kept by decade: 1950s 6 · 1960s 42 · 1970s 58 · 1980s 73 · 1990s 297 ·
2000s 1,125 · 2010s 2,566 · 2020s 1,288.

## 2. Anchor hunt

Pre-1960 roots were fetched directly from the API (`scripts/anchors.py`,
DOI where known, else title search; raw responses cached in
`data/raw/anchors/`). **12 of 15 found**, flagged `anchor: true` and
merged into the corpus (5,457 → 5,466 works):

| anchor | status | year | refs | cited by |
|---|---|---|---|---|
| Greenshields, A Study of Traffic Capacity | fetched (search) | 1935 | 0 | 1,113 |
| Wardrop, Some Theoretical Aspects of Road Traffic Research | fetched (search) | 1952 | 0 | 2,329 |
| Pipes, An Operational Analysis of Traffic Dynamics | fetched (DOI) | 1953 | 10 | 1,298 |
| Lighthill & Whitham, On kinematic waves II | already in corpus | 1955 | 9 | 4,627 |
| Richards, Shock Waves on the Highway | already in corpus | 1956 | 1 | 3,631 |
| Chandler, Herman & Montroll, Studies in Car Following | fetched (DOI) | 1958 | 3 | 1,454 |
| Herman et al., Analysis of Stability in Car Following | fetched (DOI) | 1959 | 2 | 764 |
| Newell, Nonlinear Effects in the Dynamics of Car Following | fetched (DOI) | 1961 | 15 | 1,106 |
| Gazis, Herman & Rothery, Nonlinear Follow-the-Leader Models | fetched (search; DOI 10.1287/opre.9.4.499 404s) | 1961 | 14 | 1,516 |
| Gipps, A behavioural car-following model | already in corpus | 1981 | 6 | 2,339 |
| Payne, Models of Freeway Traffic and Control (FREFLO) | fetched (search) | 1971 | 0 | 875 |
| Whitham, Linear and Nonlinear Waves | fetched (search; OpenAlex dates it 1975) | 1974 | 0 | 7,121 |

**Missed (not in OpenAlex at all)** — probed with alternate search
strings, no plausible record exists:

- **Reuschel 1950**, *Fahrzeugbewegungen in der Kolonne*
  (Österreichisches Ingenieur-Archiv) — the Austrian car-following
  priority claim. Absent from the index.
- **Edie 1963**, *Discussion of Traffic Stream Measurements and
  Definitions* (conference paper, weak index coverage of that venue).
- **Underwood 1961**, *Speed, volume, and density relationships* (Yale
  Bureau of Highway Traffic report — grey literature).

**Reference coverage of anchors is the expected weak point**: 4 of 12
(Greenshields, Wardrop, Payne, Whitham) have *no* `referenced_works`
in OpenAlex, and Richards has exactly 1. Edges *into* the classics
from later works are well covered (Richards has 920 in-corpus
citations); edges *out of* them are not. Consequence for the main
path: it cannot flow through nodes whose outgoing arcs are missing, so
the early spine is decided by which classics have any reference lists
at all (Chandler 1958, Pipes 1953, Newell 1961).

## 3. Graph shape

- Citation edges (both ends in-corpus): **49,973**.
- Reference resolution: 50,001 of 191,886 corpus references
  (**26.1%**) point at other in-corpus works — stable ~21–28% per
  decade from the 1970s on; the corpus is a bounded relevance pull, so
  most references leave the corpus. The in-corpus graph is the
  backbone of the field, not the full citation web.
- 409 arcs (0.8%) point "forward" in (year, id) order — same-year
  mutual citations and OpenAlex year noise — dropped to make the graph
  a DAG for main-path analysis.

| decade | nodes | in-corpus edges | edges/node |
|---|---|---|---|
| 1930s | 1 | 0 | 0.0 |
| 1950s | 10 | 6 | 0.6 |
| 1960s | 44 | 98 | 2.2 |
| 1970s | 60 | 108 | 1.8 |
| 1980s | 73 | 200 | 2.7 |
| 1990s | 297 | 1,036 | 3.5 |
| 2000s | 1,125 | 6,003 | 5.3 |
| 2010s | 2,566 | 25,034 | 9.8 |
| 2020s | 1,288 | 17,481 | 13.6 |

Top in-corpus in-degree (the `hub` tag, top 25): Richards 1956 (920),
Lighthill–Whitham 1955 (908), CTM I 1994 (590), Kerner & Rehborn 2000
(564), CTM II 1995 (420), FVD model 2001 (397), Gipps 1981 (382),
Gazis et al. 1961 (320), Newell 1961 (305), Chandler 1958 (295),
generalized force model 1998 (290), Pipes 1953 (261), Newell
simplified kinematic waves 1993 (233), Brackstone & McDonald 1999
(231), Daganzo & Geroliminis 2008 MFD (229).

## 4. Main path

Method: Search Path Count traversal weights (Hummon & Doreian) on the
DAG — arc u→v gets (# source→u paths) × (# v→sink paths), computed in
one topological pass each way — then Batagelj's key-route extraction:
start from the maximum-SPC arc, walk to a source and to a sink along
max-weight arcs. 52 works, oldest first:

```
1958  Chandler, Herman & Montroll — Traffic Dynamics: Studies in Car Following
1959  Herman et al. — Traffic Dynamics: Analysis of Stability in Car Following
1959  Car-Following Theory of Steady-State Traffic Flow
1961  Newell — Nonlinear Effects in the Dynamics of Car Following
1988  Traffic Flow for the Morning Commute
1990  What does the entropy condition mean in traffic flow theory?
1995  Daganzo — The cell transmission model, part II: Network traffic
1995  A finite difference approximation of the kinematic wave model of traffic
1996  Derivation and empirical validation of a refined traffic flow model
1997  Modeling and simulation of multilane traffic flow
1998  Two-lane traffic rules for cellular automata: A systematic approach
1999  Macroscopic dynamics of multilane traffic
2000  Kerner & Rehborn — Congested traffic states in empirical observations...
2001  Complexity of Synchronized Flow and Related Problems for Basic Assumptions
2002  Single-vehicle data of highway traffic: Microscopic description...
2002  Helbing — The physics of traffic jams (review)
2003  Microscopic theory of spatial-temporal congested traffic patterns
2004  Spatial–temporal patterns at an isolated on-ramp (cellular automaton)
2005  Microscopic Three-phase Traffic Theory and Its Applications
2007  Empirical Features of Congested Traffic States and Their Implications
2008  Asymmetric Microscopic Driving Behavior Theory
2009  Understanding Stop-and-go Traffic in View of Asymmetric Traffic Theory
2011  Hysteresis Phenomena of a Macroscopic Fundamental Diagram in Freeway Networks
2012  The effect of variability of urban systems characteristics (MFD)
2012  Optimal Perimeter Control for Two Urban Regions With MFDs
2012  On the spatial partitioning of urban transportation networks
2013  Cooperative traffic control of a mixed network with two urban regions
2013  Estimating MFDs in simple networks with route choice
2014  Macroscopic Fundamental Diagrams: A cross-comparison of estimation methods
2014  Robust perimeter control design for an urban region
2015  Dynamics of heterogeneity in urban networks: aggregated traffic modeling
2016  Clustering of heterogeneous networks with directional flows
2016  Enhancing model-based feedback perimeter control with data-driven online...
2017  Macroscopic urban dynamics: Analytical and numerical comparisons
2017  Modeling the dynamics of congestion in large urban networks (MFD)
2018  Introducing a Re-Sampling Methodology for the Estimation of Empirical MFDs
2018  A functional form with a physical meaning for the MFD
2019  Approximative Network Partitioning for MFDs from Stationary Sensor Data
2019  On the modeling of passenger mobility for stochastic bi-modal corridors
2019  Understanding traffic capacity of urban networks
2020  Evaluation of analytical approximation methods for the MFD
2021  Disentangling the city traffic rhythms: A longitudinal analysis of MFDs
2021  Macroscopic network-level traffic models: Bridging fifty years...
2022  Data fusion for estimating MFDs in large-scale networks
2022  Parameter estimation of the MFD: A maximum likelihood approach
2022  A macroscopic dynamic network loading model using variational theory
2022  Alpha-fair large-scale urban network control: perimeter control
2023  Scalable multi-region perimeter metering control for urban networks
2023  Leveraging reinforcement learning for dynamic traffic control: A survey
2024  Spatial-temporal graph convolution network model with traffic fundamental...
2024  Koopman theory meets graph convolutional network
2025  Physics-informed deep operator network for traffic state estimation
```

How this matches the expectation (Greenshields → LWR/Richards →
car-following → CTM → MFD):

- **Got**: car-following school (GM, 1958–61) → CTM/Daganzo (1995) →
  ... → MFD/perimeter-control era (2011–2025). The skeleton is right.
- **Missing from the path**: Greenshields, Wardrop, LWR, Richards.
  Not because they lack influence (Richards and LWR are the top two
  hubs) but because SPC needs *outgoing* arcs and the 1950s records
  have almost none (Richards: 1 reference). The key route therefore
  starts at Chandler 1958, the oldest well-referenced node. This is
  the documented coverage limitation doing exactly what was predicted
  in first-pull.md. For the viz we should draw the anchor layer
  manually on top of the computed path.
- **Surprise**: the 2000–2009 segment is dominated by the Kerner
  three-phase school and its critics (synchronized flow, asymmetric
  driving behavior), and the 1996–1999 segment by the Helbing/Dresden
  gas-kinetic line — not by IDM (2000) or Gipps descendants. SPC
  follows raw citation mass; Kerner's school cites itself densely and
  sits between the car-following giants and the present. Honest
  reading: the main path says where the *bulk* of citation traffic
  flows, which is not the same as the canonical textbook narrative.
  Both stories are worth telling in the video — this tension is a
  feature.
- Also notable: the path enters the MFD era through freeway-network
  hysteresis (2011) and never touches traffic assignment — assignment
  is a separate branch of the tree (Wardrop → Beckmann → ...), thin
  here because our seed queries under-weight it.

## 5. Co-authorship clusters

Author-pair weights = co-authored in-corpus works; communities =
connected components at weight ≥ 4 (deterministic; threshold 2 gives
one 1,006-author blob, 3 still a 258-author giant, 4 splits into
recognizable schools, 5 fragments too far). 511 edges, 96 clusters.
Top 10 by size:

| size | school (most-cited members) |
|---|---|
| 54 | Delft/Laval control line — Bart van Arem, Markos Papageorgiou, Yibing Wang, Jorge Laval, Piet Bovy |
| 39 | Dresden — Martin Treiber, Dirk Helbing, Zuojin Zhu, Qing-Song Wu, Arne Kesting |
| 28 | MFD/perimeter control — Nikolas Geroliminis, Jack Haddad, Mohsen Ramezani, Jie Sun, Anastasios Kouvelas |
| 26 | Work/Seibold traffic-estimation line — Daniel Work, Benjamin Seibold, Jonathan Sprinkle, Rahul Bhadani, Matt Bunting |
| 11 | Kerner/Duisburg — Boris Kerner, Michael Schreckenberg, Andreas Schadschneider, Ludger Santen, Sergey Klenov |
| 10 | MIT/choice-modeling — Haris Koutsopoulos, Charisma Choudhury, Moshe Ben-Akiva, Tomer Toledo, Constantinos Antoniou |
| 10 | Ningbo car-following — Hongxia Ge, Shubing Dai, Rongjun Cheng, Siuming Lo, Jufeng Wang |
| 9 | Cranfield AV — Xiaoxiang Na, Zhongxu Hu, Yang Xing, Chen Lv, Chao Huang |
| 8 | Berkeley/Daganzo — Carlos Daganzo, Byung-Wook Wie, Terry Friesz, Roger Tobin, Vikash Gayah |
| 7 | Northwestern dynamic assignment — Hani Mahmassani, R. Jayakrishnan, Samer Hamdar, Alireza Talebpour, Meead Saberi |

The schools map cleanly onto the subfields, and the main path's
2000s block is exactly the Dresden and Kerner/Duisburg clusters.
Caveat: threshold-4 components favor tight, long-running groups;
big-name loners and one-off collaborations vanish.

## 6. Subfields

Each work is bucketed by the first matching rule in
`analyze.py:SUBFIELD_RULES`, applied to its OpenAlex topics+keywords.
Order matters (most specific first); the mapping is documented in the
code. Known approximations: "network / MFD" keys on the keyword
"Diagram" — OpenAlex's mangled form of "fundamental diagram" (180 of
264 MFD-titled works carry it); macroscopic rules precede microscopic
so CTM and three-phase CA papers land in macroscopic despite often
carrying the "Microscopic traffic flow model" keyword; "other" is
mostly generic traffic-engineering work with only broad signals
("Traffic congestion", "Transport engineering").

| works | subfield |
|---|---|
| 1,917 | other |
| 1,007 | car-following / microscopic |
| 947 | macroscopic flow |
| 580 | ML prediction |
| 361 | simulation tools |
| 285 | network / MFD |
| 211 | traffic assignment |
| 158 | signal control |

## 7. Dataset

`data/traffic_lineage.json` — 5,466 nodes (id, label, authors, year,
venue, cited_by_count, subfield, tags ∈ {anchor, main-path, hub},
note left empty for hand-curation) and 49,973 `cites` edges with
`on_main_path` flags (51 path arcs). `data/coauthorship.json` —
separate author-pair edge list (511 edges, cluster ids) so the tree
viz stays clean. `data/nodes.csv` / `data/edges.csv` — flat exports
via `scripts/export_csv.py`.

## 8. What's missing / next decisions

- **Pruning for legibility.** 50k edges is not drawable. Candidates:
  show only edges into hub/anchor/main-path nodes; or per-node top-k
  in-corpus citations by target in-degree; or SPC-weight thresholding
  (we already compute the weights — exporting them per edge is a
  one-line change and gives a principled slider for edge density).
  Decision needed before any viz work.
- **Hand-drawn root layer.** The computed main path starts at 1958.
  Greenshields 1935 → Wardrop 1952 → LWR 1955/Richards 1956 must be
  asserted manually (documented influence, absent outgoing refs). The
  `note` field is the place to justify each asserted edge.
- **Missing anchors**: Reuschel 1950, Edie 1963, Underwood 1961 are
  not in OpenAlex. If the video wants them, they become hand-added
  nodes with provenance noted — same mechanism as the root layer.
- **Contested-first candidates for the video**: (a) first car-following
  model — Reuschel 1950 vs Pipes 1953 vs Chandler–Herman–Montroll
  1958: the graph has Pipes (261 in-corpus citations) and Chandler
  (295) but not Reuschel, which *is itself the story* (priority went
  to the indexed, English-language, OR-journal papers); (b) first
  fundamental diagram — Greenshields 1935 vs Underwood 1961 (absent)
  vs Edie's measurement definitions (absent); (c) kinematic-wave
  priority — Lighthill–Whitham vs Richards (the dataset shows them
  effectively tied: 908 vs 920 in-corpus citations); (d) MFD —
  Godfrey 1969 vs Daganzo 2007/2008 rediscovery (Godfrey not in
  corpus — check).
- **Coverage backfill option**: 26% reference resolution is decent but
  era-skewed; a bounded `cited_by` reverse-lookup pass for the top
  hubs would thicken the pre-1990 tree cheaply.
- **Encoding bug upstream**: one OpenAlex record ships mojibake
  ("...based on âSn...", a 2016 MFD paper). Left as-is (source data);
  fix at presentation time if the node survives pruning.
