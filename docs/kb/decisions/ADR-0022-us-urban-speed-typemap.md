# ADR-0022: US urban speed typemap for OSM imports

- **Status:** PROPOSED
- **Date:** 2026-07-25
- **Amends:** [ADR-0017](ADR-0017-city-import-decisions.md) (city-scale import posture)
- **Relates to:** [ADR-0009](ADR-0009-osm-import-strategy.md), [ADR-0012](ADR-0012-scenario-format.md)

## Context

netconvert's built-in OSM typemap (`osmNetconvert.typ.xml`) is
German-derived. Its unposted-road defaults encode German practice:

| OSM class | stock default | as km/h |
|---|---|---|
| `highway.secondary` | 27.78 m/s | 100 |
| `highway.primary` | 27.78 m/s | 100 |
| `highway.trunk` | 27.78 m/s | 100 |
| `highway.tertiary` | 22.22 m/s | 80 |
| `highway.residential` | 13.89 m/s | 50 |
| `highway.motorway` | 39.44 m/s | 142 |

An OSM `maxspeed` tag overrides the default, so the error only surfaces
where the tag is absent — which in US extracts is the common case. In
`chi-loop`, only 16.5% of secondary ways carry `maxspeed` — and NOT ONE
of its 193 primary ways does — so 12,945 of
15,975 secondary lanes compiled at 100 km/h. Michigan Avenue, Wells,
Wabash and State Street were all simulated as autobahn.

This is not a tuning nit. Free-flow speed sets the IDM desired speed,
which sets headway, which sets capacity and delay. Every congestion
number produced on these networks — mean speed, time loss, the ranked
worst-streets table — is computed against a free-flow reference that is
2–3× the real posted limit. The delay figures are not merely imprecise;
their denominator is wrong.

**Measured blast radius.** Every US network in `data/networks/` was
imported with the stock typemap. External (non-junction-interior) lanes:

| network | lanes | mean km/h | ≥80 km/h | exactly 100 km/h |
|---|---|---|---|---|
| houston-lean | 323,007 | 88.8 | 61.2% | 51.8% |
| chi-loop | 23,833 | 85.9 | 79.7% | 57.1% |
| atlanta-lean | 155,473 | 80.3 | 35.4% | 25.8% |
| chi-north-lakefront | 24,189 | 79.1 | 64.8% | 48.4% |
| la-arterial | 535,348 | 76.3 | 38.8% | 28.8% |
| chi-kennedy | 7,533 | 75.8 | 51.3% | 20.1% |
| la-lean | 639,856 | 75.1 | 32.7% | 24.2% |
| dallas-lean | 376,684 | 74.6 | 27.0% | 16.8% |
| sf-lean | 140,751 | 74.2 | 36.9% | 28.4% |
| miami-lean | 222,326 | 71.9 | 18.3% | 11.5% |
| houston | 798,814 | 68.4 | 28.2% | 24.3% |
| la | 1,173,489 | 64.5 | 18.8% | 14.1% |
| dallas | 747,158 | 63.4 | 14.8% | 9.4% |
| stress-dtla | 14,903 | 62.2 | 41.7% | 34.5% |
| atlanta | 455,570 | 62.0 | 13.6% | 10.2% |
| miami | 583,054 | 58.6 | 7.7% | 5.1% |

…and the smaller US cuts (`manhattan-grid`, `phoenix-arterial`,
`macarthur-maze`, `sf-octavia`, `la-wilshire`, `merge-101-380`,
`i280-woodside`, `boston-core`) on the same footing. 25 of the 28
networks in the tree are affected. `de-roundabouts` is German and is
correctly served by the stock typemap — which is exactly why the fix
cannot be a global default change.

The `-lean` cuts are consistently *worse* than their full counterparts
because filtering to arterial-and-up raises the share of secondary and
primary lanes, the two classes the stock map misprices hardest. The
networks cut down for demos are the most wrong.

## Decision

1. **US urban imports use an explicit typemap,
   `scripts/osm-urban-us.typ.xml`, passed to netconvert pass 1 via
   `--type-files`.** It is generated from the stock
   `osmNetconvert.typ.xml` with **only the `speed` attribute changed** —
   `numLanes`, `priority`, `oneway`, `width`, `allow`/`disallow` and
   `discard` are copied verbatim, and no type id is added or dropped
   (a missing type would silently fall back to a netconvert default,
   smuggling a topology change in as a speed change).

   **The file being speed-only does NOT make the import speed-only.**
   That inference was made in the first draft of this ADR and it was
   wrong — see §6. netconvert derives right-of-way and signal timing
   *from* speed, so changing speed changes them downstream. The
   speed-only property of the file is worth keeping because it makes
   the diff auditable, not because it bounds the blast radius.

2. **Unposted urban defaults are the statutory urban-district limit,
   30 mph / 13.41 m/s**, applied to `primary`, `secondary` and
   `tertiary`. This is a legal value, not a fitted one: Illinois,
   Texas, Georgia and Florida all set 30 mph as the urban-district
   default for a street that is not posted otherwise. Residential and
   unclassified drop to 25 mph (11.18), service to 15 mph (6.71),
   motorway to 55 mph (24.59) and trunk to 45 mph (20.12).

   **Known approximation:** California's statutory business/residence
   default is 25 mph (CVC 22352), so `sf`, `sf-lean`, `sf-octavia`,
   `la*`, `i280-woodside`, `macarthur-maze` and `merge-101-380` run
   5 mph fast on unposted streets. Accepted rather than forked: a
   per-state typemap is the natural next step if a California scenario
   ever needs the precision, and 30 mph is far closer than 62.

3. **The typemap is region-scoped, and the US map is the DEFAULT —
   opt-out, not opt-in.** `import-city.sh` gains a `TYPEMAP` variable
   defaulting to the US urban map; a non-US import sets `TYPEMAP=`
   (set-but-empty) to get stock behavior. `de-roundabouts` is the
   standing counterexample.

   Defaulting rather than requiring an explicit choice is deliberate:
   every network in this tree except one is US, and the failure mode of
   forgetting the flag is the autobahn bug we are fixing. The cost is
   that a future non-US import gets US speeds unless its author knows
   to opt out — accepted, and the reason the run prints the resolved
   typemap on every import. (An earlier draft of this ADR called the
   scheme "opt-in", which contradicted the implementation; external
   review flagged the inconsistency.)

4. **The typemap file joins the importer identity hash.**
   `import-city.sh`'s `REPO_REV` currently hashes the pipeline scripts
   and the Go importer. The typemap now shapes the compiled network as
   much as any of them, so it is added to that hash — otherwise editing
   a speed silently produces a different network under an unchanged
   provenance string, and ADR-0012 content hashes stop meaning what
   they claim.

   **Known gap — the shipped `chi-loop-urban` does not carry this hash.**
   It was regenerated by hand (netconvert + netimport invoked directly),
   not through `import-city.sh`, so its provenance reads
   `netimport (netconvert 1.27.1 .net.xml, typemap …, right-before-left
   disabled)` with no `import-city.sh@<rev>` stamp at all. External review
   raised this as "either the flag list is a typo, or the network was
   produced outside the shipped script" — it was BOTH, and only the flag
   list was wrong in the way first assumed. The network *content* is the
   right one (the junction-type and lane-id checks above were run against
   these exact files), but the reproducibility claim this section makes
   does not yet hold for it. The *network* is reproducible:
   `data/networks/chi-loop/loop.osm` (11.4 MB, the converted extract) is on
   disk and is exactly what the hand run consumed. What is missing is the
   raw Overpass JSON for Chicago — the tree keeps them for
   la/houston/miami/sf/atlanta/dallas but not chi — and `import-city.sh`
   starts from that JSON, needing it both for `overpass2osm.py` and for
   `osm3s.timestamp_osm_base`, which it refuses to run without. Two small
   ways to close it: re-fetch the bbox, or give the script an `.osm` entry
   point that carries the timestamp forward from existing provenance. Do it
   before any content hash, recording or published consumer binds here.

   **Known gap (accepted).** On the `TYPEMAP=` stock path nothing is
   hashed, yet netconvert's *implicit* built-in typemap is still an
   output-shaping input: a SUMO upgrade that repackages
   `osmNetconvert.typ.xml` under the same reported netconvert version
   would leave `REPO_REV` unchanged while producing a different
   network. Accepted for now because exactly one non-US network exists
   and the netconvert version string is carried separately in the
   provenance. The fix, if a second non-US import ever appears, is to
   resolve and hash the stock file rather than nothing. (Raised by
   external review.)

5. **Existing networks are not silently re-imported.** Networks are
   re-imported deliberately, one at a time, under a new directory name
   (`chi-loop` → `chi-loop-urban`) rather than in place. Recordings and
   scenario hashes bound to the old networks stay valid *as recordings
   of the old network*; nothing is invalidated retroactively, and the
   two can be compared.

6. **Right-before-left junction typing is disabled on US imports**
   (`--junctions.right-before-left.speed-threshold 0`). This is the
   second half of the fix and it is not optional.

   netconvert types an unsignalized junction `right_before_left`
   (yield-to-the-right) when **all** its incoming edges are below
   `--junctions.right-before-left.speed-threshold`, default
   **13.6111 m/s**. The statutory urban limit, 30 mph, is
   **13.41 m/s — 0.2 m/s under that threshold.** So the speed fix on its
   own silently reclassified right-of-way across the network. Measured
   on `chi-loop` with the typemap alone:

   | junction type | chi-loop | typemap only | typemap + rbl off |
   |---|---|---|---|
   | `priority` | 4,829 | 3,611 | 5,104 |
   | `traffic_light` | 2,217 | 2,217 | 2,217 |
   | `right_before_left` | 275 | **1,493** | 0 |
   | `dead_end` | 197 | 197 | 197 |

   1,218 Chicago intersections became yield-to-the-right. Total junction
   count is unchanged (7,518 in all three), so this is pure retyping —
   which is precisely why it was invisible in the lane and connection
   counts the first draft relied on.

   Disabling it is a **model choice, not merely a restore**: it also
   removes the 275 rbl junctions the stock import produced.
   Right-before-left is legally real in the US at genuinely uncontrolled
   intersections. The argument for disabling it anyway rests on the
   **control convention, not on a count we have taken** — we have not
   measured what fraction of Loop intersections is controlled, and the
   earlier phrasing here ("a US city intersection almost always carries a
   signal or a stop sign") asserted a frequency it did not support. What
   is defensible: US intersection control is a signal, an all-way stop, or
   a two-way stop where the major street keeps priority (MUTCD); mutual
   yield-to-the-right is not a US rule the way it is in Germany, which is
   where netconvert's default comes from. Signals are imported; stop signs
   ride the ADR-0017 pass-2 override where the extract has them (chi-loop
   predates that pass and imports none). For the residue, `priority` —
   major road proceeds, minor yields — is the closer approximation.

   The empirical half of the argument is stronger than the modeling half:
   rbl is a well-known SUMO mutual-blocking gridlock source, and the
   defective typemap-only import produced **975,673 collision
   observations at 9,000 veh/h over 54,000 ticks, 79% of them inside a
   single junction interior** (`j:619019057`). That signature is what an
   rbl deadlock looks like under load, and it is absent from the shipped
   import.

   **The generalizable lesson:** netconvert encodes "is this a major
   road" as an *absolute speed*. Lowering every arterial to the
   statutory limit destroys that proxy, so any threshold expressed in
   absolute m/s must be re-examined whenever the speed floor moves. The
   rbl threshold is the one we found. Treat it as a class, not a
   one-off.

## Consequences

- **Verified on chi-loop** (`chi-loop` vs `chi-loop-urban`, same OSM
  extract, same netconvert 1.27.1, same flags — `--proj.utm
  `--no-turnarounds`, and **no** `--junctions.join` (ADR-0017) — with the
  typemap and rbl threshold the only difference). An earlier draft of this
  line listed `--junctions.join` among the flags; that was wrong, and
  `import-city.sh` has never passed it. The evidence that the two
  pipelines really did match is not the flag list but the identical
  external lane **id sets** below: joining junctions rewrites node
  clusters and would not leave 23,833 lane ids bit-identical. The middle
  column is the typemap alone, i.e. the defective first attempt, kept
  because the contrast is the evidence for §6:

  | | chi-loop | typemap only | **shipped** (typemap + rbl off) |
  |---|---|---|---|
  | external lanes | 23,833 | 23,833 | 23,833 |
  | external lane id set | — | identical | identical |
  | origins / exits | 246 / 1,432 | 246 / 1,432 | 246 / 1,432 |
  | signal programs / links | 2,217 / 13,181 | 2,217 / 13,181 | 2,217 / 13,181 |
  | connections | 61,229 | 61,113 (−116) | 61,246 (+17) |
  | internal lanes | 31,705 | 31,587 (−118) | 31,722 (+17) |
  | yield approaches | 1,089 | 987 (−102) | 1,106 (+17) |
  | conflict pairs | 29,360 | 29,104 (−256) | 29,396 (+36) |
  | mean speed limit | 85.9 km/h | 50.5 km/h | 50.5 km/h |
  | lanes ≥ 80 km/h | 79.7% | 4.3% | 4.3% |

  What is preserved exactly is the **external lane id set** — identical,
  not merely equal in count — along with origins, exits and the signal
  program and link counts. What is NOT preserved, and the table says so
  in the same breath, is the junction interior: connections, internal
  lanes, yield approaches and conflict pairs all move (+17/+17/+17/+36),
  because 275 junctions deliberately change right-of-way class. "Topology
  is preserved" without that qualifier is the same overreach §1 retracts;
  state the id-set claim, which is what was measured, and nothing wider.

  The first draft of this ADR read the middle column's −118 / −116 /
  −256 as "speed-dependent junction-interior construction — lower
  approach speeds need fewer in-junction waiting positions." That
  explanation was post-hoc and wrong. Those deltas were the
  right-before-left retyping of §6. With rbl disabled the same counts
  move **+17**, and the residual is fully accounted for by the 275
  junctions deliberately moved from rbl to `priority` (a priority
  junction carries more internal structure and more yield approaches
  than a mutual-yield one). External review caught this; the lane and
  connection counts the draft relied on could not have.

- **Signal phase timing changes, and this is correct.** 1,936 of the
  2,217 programs differ in phase durations (445 also in phase state
  strings); cycle lengths are preserved. netconvert derives yellow time
  from approach speed, so a junction goes e.g. green 79 s / yellow 6 s /
  red 5 s → green 82 / yellow 3 / red 5. Three seconds of yellow at
  30 mph is right (ITE practice), six was an artifact of the autobahn
  speed. This is a **capacity-relevant timing change, not geometry** —
  so "the counts are identical" must not be read as "the signals are
  identical."

  Consequence: **`chi-loop` and `chi-loop-urban` recordings are not
  comparable at junction level or at signal-timing level.** Compare
  whole-network aggregates, not per-junction behavior.

- Residual lanes above 80 km/h in `chi-loop-urban` are 975 motorway
  lanes at 89 km/h plus explicitly tagged `maxspeed` values (64, 72
  km/h). Observed speeds in the compiled network include 24/32/40/48/56
  km/h — the 15/20/25/30/35 mph tag ladder — confirming OSM tags still
  win over the type default.

- **The two confounded results are WITHDRAWN, not re-run.** The earlier
  drafts carried a collision-collapse A/B (14,049 → 135 observations) and
  a matched-demand delay-share figure (65% → 50%), both measured against
  the typemap-only network — the middle column above, before §6. Speed
  and right-of-way had moved at once, so neither could be attributed to
  the speed change. They are deleted rather than re-measured: the
  decision they supported is already made, and a three-arm re-run
  (stock / typemap-only / typemap+rbl-off) costs hours that are better
  spent on demand. **Do not cite either number.** If the attribution
  ever matters, that three-arm run is the experiment.

  What was measured instead, on the SHIPPED import with the fixed
  engine, is that the network now behaves. Bracketing `chi-loop-od`
  demand over 18,000 ticks (30 sim min), flat peak, seed 42:

  | target veh/h | injected | mean speed | delay share | fleet @ 30 min | collisions |
  |---|---|---|---|---|---|
  | 16,000 | 12,960 | 26.0 km/h | 53% | 5,675 | 4,996 |
  | 20,000 | 16,200 | 24.3 km/h | 56% | 7,230 | 8,557 |

  Nothing in a 10k–20k bracket gridlocks. For scale, the same scenario
  family on the DEFECTIVE rbl import logged **975,673 collision
  observations over 54,000 ticks, 774,172 of them inside the single
  junction `j:619019057`** — the mutual-blocking signature §6 exists to
  prevent. That is not a controlled A/B (different horizon and rate),
  but the order of magnitude is the point and it is why §6 is not
  optional.

- **Demand tuned against a fast network does not transfer.**
  `chi-loop-od`'s 12,000 veh/h was set against the 62-mph network. The
  scenario now ships as `chi-loop-od-30m` at a 16,000 veh/h target
  (12,960 injected). Every scenario re-pointed at a re-imported network
  must be re-tuned, and its README tuning log must say which network the
  numbers were taken on.

- **A re-import does NOT congest the expressways, and no `--total` will.**
  Worth recording here because it is the first thing anyone will check
  after a speed fix. `mkod.py` sets portal rates as `class_rate ×
  (total × portal_share / portal_raw)`, so the per-class table is only a
  shape and `--total` sets the level: at 16,000 the scale factor is ≈0.24
  and the Kennedy's two boundary origin lanes inject 337 veh/h/lane,
  about a sixth of a freeway lane's capacity. Every named expressway in
  `chi-loop-od-30m` runs at 72–79 km/h while N Wells sits at 8.4. The
  arterial grid saturates near 16–20k zone total; the freeway portals
  would need ≈67k. This is a demand-generator limit, not an import one —
  but it means **a speed-corrected import will not by itself make a
  freeway bottleneck appear**, and anyone re-importing a city for that
  reason should read `data/scenarios/chi-loop-od-30m/README.md` first.

- Congestion reports produced before this ADR (`scorecard.py`,
  `congestion.py` output archived under `data/scenarios/`) quote delay
  against an autobahn free-flow reference and should be regenerated or
  labelled, not cited.

- Re-importing the remaining 24 networks is deferred. Each is a
  multi-GB netconvert run; they are re-imported when a scenario needs
  them, not en masse. The affected list above is the work queue.

## Alternatives considered

- **Patch speeds post-import in `netimport`.** Rejected: netconvert
  uses edge speed while *building* the network (junction internal
  geometry, right-of-way), so a post-hoc rewrite would produce a
  network whose junctions were laid out for speeds it no longer has —
  strictly worse than the honest re-import, and silently so.

- **Replace SUMO's stock typemap in the venv.** Rejected: invisible,
  unversioned, lost on any SUMO reinstall, and it would break
  `de-roundabouts` without a trace.

- **Infer limits from road class + land use / building density.**
  Rejected for now as a model where a statute is available. Worth
  revisiting once the ADR-0021 building index is a first-class input to
  import rather than a post-processing step.
