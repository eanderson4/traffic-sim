# Static network comparison — LA vs NY (WQ-8, 2026-07-24)

GIS-style static analysis of the 13 compiled network-format v1 files in
`data/networks/`. Question: does the static road network alone predict
different congestion behavior for LA vs NY, and is there a podcast-usable
story? Answer: **yes — the two cities' networks jam by different
mechanisms, and the difference is visible in the files before any vehicle
moves.**

Metrics computed by `netstats/` (stdlib-only Go, parses the compiled JSON
directly — format per `contracts/network-format-v1.md`). Reproduce:

```sh
cd analysis/networks/netstats && go run . -dir ../../../data/networks
```

The 14 metro-scale imports (`atlanta`, `la`, `dallas`, `houston`, `miami`,
`sf` + `-lean` variants, `chicago`) exist only as `.net.xml` +
`import-report.json` — no compiled `.json` — and are excluded (LA-metro
would be 1.17 M lanes; compilable on demand via `engine/cmd/netimport`).

## Headline table (sorted by lane-km; LA = la-wilshire + stress-dtla, NY = manhattan-grid)

| network | lanes | junc lanes | edges | lane-km | km² | lane-km/km² | juncs | junc/km² | signal % | yield appr | origins | exits | block m (avg/med/wavg) | frag % | lanes/edge | avg km/h | fwy % |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| chi-north-lakefront | 24189 | 32185 | 14842 | 2290.8 | 398.12 | 5.8 | 7340 | 18.4 | 28 | 1701 | 359 | 1740 | 109/39/378 | 6 | 1.63 | 78 | 65.4 |
| chi-loop | 23833 | 31705 | 13431 | 1859.4 | 168.68 | 11.0 | 7321 | 43.4 | 30 | 1089 | 246 | 1432 | 84/37/250 | 8 | 1.77 | 84 | 76.0 |
| chi-kennedy | 7533 | 7782 | 3199 | 894.4 | 519.80 | 1.7 | 1998 | 3.8 | 24 | 287 | 283 | 713 | 116/45/419 | 4 | 2.35 | 82 | 65.3 |
| phoenix-arterial | 17212 | 39605 | 13888 | 569.7 | 29.72 | 19.2 | 6029 | 202.8 | 2 | 22071 | 44 | 63 | 36/12/192 | 39 | 1.24 | 39 | 14.1 |
| **la-wilshire** | 12258 | 25520 | 8883 | 426.3 | 36.69 | 11.6 | 4037 | 110.0 | **13** | **11834** | 38 | 99 | 41/8/288 | 48 | 1.38 | 49 | 9.9 |
| **stress-dtla** | 14903 | 25068 | 8966 | 341.7 | 10.78 | **31.7** | 4705 | 436.3 | 26 | 8600 | 102 | 162 | 23/7/90 | 47 | 1.66 | 64 | 46.5 |
| macarthur-maze | 6071 | 13429 | 5171 | 303.0 | 14.15 | 21.4 | 2477 | 175.1 | 9 | 6971 | 56 | 89 | 45/19/267 | 28 | 1.17 | 55 | 33.9 |
| de-roundabouts | 4387 | 8784 | 4095 | 147.0 | 6.93 | 21.2 | 2058 | 296.8 | 9 | 4444 | 21 | 25 | 34/18/108 | 28 | 1.07 | 30 | 0.0 |
| **manhattan-grid** | 4028 | 5074 | 1939 | 96.9 | 5.01 | 19.3 | 1448 | 288.8 | **65** | **684** | 74 | 109 | 27/2/120 | 59 | 2.08 | 55 | 24.7 |
| boston-core | 3468 | 4899 | 2297 | 80.0 | 3.34 | 24.0 | 1529 | 458.1 | 24 | 1382 | 53 | 69 | 21/2/138 | 55 | 1.51 | 49 | 12.9 |
| i280-woodside | 187 | 188 | 110 | 55.8 | 18.00 | 3.1 | 66 | 3.7 | 3 | 18 | 15 | 24 | 230/47/1712 | 8 | 1.70 | 88 | 71.6 |
| sf-octavia | 2113 | 3356 | 1429 | 50.0 | 2.33 | 21.4 | 860 | 368.4 | 34 | 1018 | 44 | 46 | 24/2/115 | 54 | 1.48 | 49 | 17.7 |
| merge-101-380 | 1017 | 1669 | 777 | 38.5 | 4.88 | 7.9 | 457 | 93.7 | 2 | 698 | 27 | 47 | 39/19/108 | 17 | 1.31 | 43 | 27.0 |

Column glossary: **junc lanes** = junction-internal lanes; **km²** =
bounding box of lane shapes; **signal %** = junctions with a compiled
fixed-time program / all junctions; **yield appr** = internal lanes with
`row: minor` (no file contains any `row: stop`); **block m** = edge
length mean / median / length-weighted mean; **frag %** = edges < 5 m
(netconvert junction-cluster splits); **fwy %** = lane-km with speed
limit ≥ 22 m/s (~80 km/h); lane-count is per `edge` (one direction).

## LA vs NY face-off

| | manhattan-grid (NY) | stress-dtla (LA) | la-wilshire (LA) |
|---|---|---|---|
| area (shape bbox) | 5.0 km² | 10.8 km² | 36.7 km² |
| road supply (lane-km/km²) | 19.3 | **31.7** | 11.6 |
| signalized junctions | **65 %** (946/1448) | 26 % (1202/4705) | **13 %** (515/4037) |
| yield approaches per unsignalized junc | 1.36 | 2.45 | **3.36** |
| lanes per edge (one direction) | 2.08 | 1.66 | 1.38 |
| block length, length-weighted | 120 m | 90 m | 288 m |
| freeway lane-km share | 24.7 % | 46.5 % | 9.9 % |
| demand portals (origins/km²) | 14.8 | 9.5 | 1.0 |
| capacity proxy (Σ lane-km × km/h) | 5,358 | 21,988 | 20,702 |

## Findings

1. **The control regime is the story.** Manhattan's conflicts are
   metered: 65 % of its junctions run compiled fixed-time signal
   programs, and its unsignalized junctions average only 1.4 yielding
   approaches. LA's networks are priority-rule networks: Wilshire has
   13 % signals and 11,834 yield approaches — 3.4 per unsignalized
   junction. Same car-following physics, different jam mechanism:
   Manhattan queues are red-phase queues that release in platoons and are
   capped by block length; LA queues form at unprotected conflicts and
   merge funnels and grow by gap starvation. "New York waits in line,
   Los Angeles fights for gaps" is a podcast line the static data
   supports.

2. **Manhattan's geometry is a spillback amplifier.** Short blocks
   (length-weighted 120 m vs Wilshire's 288 m), more lanes per street
   (2.08 vs 1.38), and 15 demand portals per km² (vs 1.0) mean queues
   have nowhere to sit: one red-phase platoon is already a block long,
   so box-blocking and gridlock are the expected failure mode. LA's long
   arterials absorb queues in space instead — congestion there should
   show as localized bottlenecks (funnels, cross-street conflicts), not
   lattice-wide lock-up. Static prediction the sim can falsify.

3. **The supply-density surprise: downtown LA is *more* road-dense than
   Manhattan.** stress-dtla packs 31.7 lane-km per km² against
   Manhattan's 19.3 — because the 101/110/10 freeway stack runs through
   the bbox (46.5 % of DTLA lane-km is freeway-class). The "LA = sparse
   sprawl, NY = dense grid" intuition inverts at the network-supply
   level; what differs is *where* the capacity sits (freeways vs surface
   streets) and *how* it's controlled (yield vs signals), not how much
   pavement exists per unit area.

## Caveats — what the static network does NOT tell you

- **No demand data.** The network format carries no demand by design:
  `origin` lanes are flat spawn portals and real OD lives in the scenario
  layer (`Scenario.SpawnRates`). Both smoke scenarios
  (`data/scenarios/manhattan-grid`, `la-wilshire`) spawn a uniform
  600 veh/h/lane at every origin — so the same flat rate injects
  ≈ 8,900 veh/h/km² into Manhattan but only ≈ 620 into Wilshire (14×,
  purely from portal density). Any sim comparison at flat demand
  measures *network response*, not the cities' actual traffic.
- **Congestion needs demand > capacity at a point.** Static metrics say
  where the weak points are (signals, merges, short blocks), not whether
  they're overloaded. Real LA vs NY demand profiles (freeway-dominated
  OD vs taxi/grid circulation) could flip every conclusion above.
- **Speed limits are OSM tags / netconvert defaults**, not measurements
  (Manhattan's 24.7 % "freeway" lane-km is boundary highways caught in
  the extract, e.g. FDR-class roads).
- **Only fixed-time signals compile** (ADR-0011): actuated/adaptive LA
  signals show up here as *unsignalized* — the 13 % Wilshire figure is a
  floor, and unmodeled signalized junctions traverse freely in-sim.

## Data-quality notes (hit while computing)

- **Provenance bboxes underreport coverage** in the 2026-07-23 imports:
  manhattan-grid declares 1.6×1.7 km but its lane shapes span 2.6×1.9 km;
  la-wilshire declares 2.2×3.75 km but spans 6.5×5.7 km. All density
  metrics here use the shape extents, which are the truth the sim loads.
- **Micro-fragments distort raw edge stats**: 27–59 % of edges are < 5 m
  junction-cluster splits (Manhattan: 2,475 lanes under 1 m), which is
  why median block length reads 2–8 m. Use the length-weighted mean.
- **Zero `row: stop` approaches in all 13 files** — stop-sign data never
  made it through the import; minor-road control is uniformly `yield`.
- **Junction counts include netconvert cluster splits**, so junc/km²
  (110–458) is not comparable to urban-planning "intersections per km²".
- Sanity check that passed: netstats counts 946 signalized junctions in
  manhattan-grid; the scenario comment independently says "946 tlLogic
  programs".

## Files

- `netstats/main.go`, `netstats/go.mod` — the tool (stdlib only).
- `netstats/table.md` — raw table output; `netstats/stats.json` — full
  per-network metrics as JSON.
