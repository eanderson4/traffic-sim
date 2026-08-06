# Sliver-merge 03 — design exploration for the sub-5 m lane fix

Date: 2026-08-05. Input to an ADR; no code changed. Evidence: stranding-02.md,
a dry-run netconvert round-trip on the real source net (`/tmp/chidiag/jointest/`),
and reads of `engine/netimport/netimport.go`, `contracts/network-format-v1.md`,
`scripts/import-city.sh`, ADR-0009/0012/0017, `scripts/chicago/mknetvariant.py`.
Also corrects one wrong claim in stranding-02 (the `_d2` suffix — see §5).

## 1. Where slivers are born

The SUMO source genuinely has the junctions meters apart; our importer does not
split anything. Verified on `data/networks/chi-loop-urban/loop-urban.net.xml`:

- OSM maps divided-road intersections as **separate nodes per carriageway
  crossing**. Example (the 435656622 stranding knot): three signalized junctions —
  `cluster_269446276_5701168856` at (8351.7, 9464.8), `5701168857` at
  (8355.7, 9453.2), `27440477` at (8367.1, 9465.4) — a triangle 12–16 m across
  that is one physical intersection.
- netconvert (1.27.1, no `--junctions.join` — see below) keeps them as separate
  junctions connected by short edges, then **clamps each edge's lanes between the
  two junction shape polygons**. The polygons nearly touch or overlap, so the
  usable lane length collapses: edge `518584001` is 11.4 m geometric but compiles
  to **0.2 m** of road lane; edge `435656634#0` is 11.6 m geometric → **4.2 m**.
- `cluster_*` junction ids in the source are netconvert's *coincident-node*
  merges (automatic), not distance joins.
- Pipeline: `scripts/import-city.sh:84-86` deliberately passes **no
  `--junctions.join`** because the stop-sign override (pass 2,
  `scripts/osm-stop-nodes.py`) keys junctions by OSM node id, which joining
  rewrites to cluster ids (ADR-0017 item 1; revisit path named there: teach the
  override to resolve cluster membership). The KB's extraction article
  (`docs/kb/articles/integrations/osm-extraction.md:82`) had recommended cluster
  consolidation upfront — "unjoined clusters have measured sim consequences:
  low throughput, jams and even deadlocks." The 1,588 strandings are that
  prediction, measured.
- Scale in the source net: **1,037 non-internal edges have a lane < 5 m**
  (junction-center distance: 127 under 5 m, 324 at 5–10 m, 274 at 10–15 m).
  **52% (542) connect two traffic-light junctions** — the stranding-knot class.
  (= 1,758 road lanes < 5 m / 937 < 1 m in the compiled file.)

## 2. Merge mechanics (what breaks if slivers become junction interior)

Contract constraints (`contracts/network-format-v1.md`):

- **Internal-lane invariants**: `internal` lanes have exactly one successor
  (contract lane table; netimport.go:299; the kernel assumes it —
  gridlock.go:199's comment "netimport networks, whose internal lanes are
  single-successor"). Slivers can have several (`n518584001_0_d2` has 2). A naive
  "flag the sliver as internal" violates the invariant; the workable variant is
  to **delete** the sliver lanes and rewire their predecessors straight into the
  far junction's internal lanes (which are already single-successor), extending
  the far internals' length/shape by the sliver's.
- **Right-of-way fields are internal-only** (loader rejects `row`/`foes`/`tl`
  on non-internal lanes): reclassified/rewired lanes need valid `junction`,
  `row`, and foe sets. Foe sets are computed per junction by polyline crossing
  (netimport.go:361-379) — extending the computation to a merged cluster is
  mechanical (union the member junctions' internal lanes, same geometry test).
- **Signals bind per lane, not per junction** (`tl`+`tlLink` on internal lanes;
  kernel derives state per lane). A merged cluster may keep both member programs
  bound to different movements — the kernel does not care. The far junction's
  stop line effectively moves upstream by the sliver length (vehicles queue
  behind the pocket instead of inside it): the intended behavior change.
- **Box semantics**: `exitWalk` stops at the first internal lane and hands off
  to "the next box's gate" (rightofway.go:332-350) — internal→internal chains
  are already normal. After consolidation, a stopped vehicle can no longer sit
  *between* the boxes; it sits at the far box's (extended) stop line or inside
  the far box, where the existing gates handle it.
- **Metrics**: sliver sections disappear; internal lanes group under
  `j:<junction>`; occupancy>1 artifacts (occ 25 on a 0.2 m lane) vanish.
- **Lane identity**: sliver lane ids vanish and far internals renumber → new
  network file, new canonical JSON, new ADR-0012 content hash (by design —
  "re-imports produce new files, not edits", contract top matter). Checked:
  **no demand origin or destination in chi-loop-urban-half-base sits on a
  < 5 m lane**, and slivers are never origins/exits — demand `main.yaml` stays
  valid.
- Precedent for junction-interior modeling: `prototypes/3-intersection-interior`
  (the prototype lineage that defined box interior semantics); foe/internal
  model itself is ADR-0010.
- **Signal-program fidelity**: OSM carries presence-only signal nodes; the 2,217
  static programs in the net are netconvert-synthesized defaults (uniform
  ~90 s cycles). Merging twin signals loses no *measured* timing — there is none.

## 3. Alternatives compared

**(a) Import-side consolidation in netimport (RECOMMENDED).** During Convert,
detect non-internal edges whose lanes are all < L (threshold ~5–7 m) between two
junctions; delete them, rewire predecessors into the far junction's internals
(extended by the sliver length), union the member junctions into a cluster id
for foe grouping. Engine untouched; format v1 unchanged (all fields already
exist); deterministic; report counts consolidations like other import decisions.
Blast radius: compiled networks change on next import → new scenario hashes for
re-imported networks only; importer-identity hash (import-city.sh:117) moves for
everyone, which only matters when a network is actually re-imported. Recordings,
bakes, CRC semantics, engine code: untouched. Risk: re-implements a slice of
netconvert's junction joining in our importer — the ADR-0009 concern — but
scoped to one measurable transform with the import report as the audit surface,
and (c) below proves netconvert won't do it for us.

**(b) Kernel-side rule (treat sub-N-m downstream storage as box interior).**
The seal is *physically real* given the network: there is no room, and a stopped
vehicle on a 0.2 m lane does block everything behind it. Any kernel rule that
admits vehicles anyway is box-blocking by fiat — a physics change on every
network incl. M1–M3 CRC fixtures — and it fixes neither the metrics absurdities
(occ 25 lanes) nor the strand miscounting, only re-labels them. **Rejected:**
worst blast radius (all networks, hot path in rightofway.go) for a
representation-level problem.

**(c) netconvert `--junctions.join` at generation — MEASURED, FAILS.** Dry run
on the real source (`/tmp/chidiag/jointest/`): PlainXML round-trip of
`loop-urban.net.xml`, recompile with `--junctions.join --junctions.join-dist 10`:
junctions 9,716 → 9,530, but edges-with-lane<5 m **1,037 → 1,041**; the
stranding knot (5701168857 / cluster_269446276_5701168856 / 27440477) **not
joined**. With `--junctions.join-dist 20 --junctions.join.parallel-threshold 180`:
9,400 junctions, short edges **rise to 1,147**, knot still not joined. Refusals
are logged as "Not joining junctions … (parallel incoming …)" / "(after
reduction)" — exactly the divided-twin topology that births our slivers is the
topology netconvert 1.27.1 refuses to join. Would also have required fixing the
ADR-0017 stop-override keying for nothing. **Rejected on measurement.**

## 4. Regeneration cost (what a network change re-pins)

- **Scenario content hash (ADR-0012)** covers the canonical network JSON
  (engine/scenario/scenario.go:211-233) → every scenario on chi-loop-urban gets
  a new run identity: 17 scenario dirs (`chi-loop-urban-half*`, `chi-loop-od*`,
  `chi-show-fw*`, …). Old recordings/bakes stay valid against the old hashes
  (hash-keyed); nothing retroactively breaks.
- **Variants are delta patches valid against their base import only** (contract
  limitations): narrow1, widen1/2, gridwiden, retime066/125, kennedy — regenerate
  via mknetvariant from the new base.
- **Derived network artifacts** (`data/networks/chi-loop-urban/{buildings,
  portals, corridors, zones, districts}.json`): script-regenerated
  (mkod/boundaries/corridors/mkzones); lane ids of slivers vanish — demand
  verified unaffected (§2).
- **Baked demos** (`data/baked/{bases42x27k,chishow,cbdbase,kenbase,
  narrow1s42x27k,retime125s42x27k}/*`, pinned by scenarioHash; the public site
  `deploy/demos.public.json` replays them): old bakes remain reproducible
  artifacts; new baselines need fresh bakes before any publish.
- **Baselines**: ADR-0036/0037 brackets and docs/show numbers become historical
  on the old network — re-measure on the new base (they were due for re-baseline
  after the drain2 rerun anyway).
- **Warm-start sidecars** (ADR-0029) fingerprint the network → invalid, by design.
- **Provenance chain**: importer-identity hash (import-city.sh:117) covers
  netimport.go, so the fix self-documents into every future import's provenance.

## 5. The `_d2` "cloning bug" — premise correction + a real (small) bug

`_d2` is **not** mknetvariant cloning. It is netimport's sanitize-collision
suffix for edge-id pairs that sanitize identically (`518584001` vs `-518584001`
→ both `n518584001_0`; second in document order gets `_d2`,
netimport/netimport.go:497-499; contract documents it). 5,645 such lanes exist;
`n518584001_0_d2` is a genuine OSM reverse-direction lane. stranding-02.md has
been corrected in place.

The real bug: mknetvariant's `--add-lane` clones the outermost lane of every
corridor edge with **no length guard** (`scripts/chicago/mknetvariant.py:268-277`),
producing `_w1` clones of slivers: **7 sub-5 m clones in widen1, 14 in widen2,
881 in gridwiden** (three of them 0.2 m). This touches only the variant
networks. Assessment: separate small fix (skip donors below a length threshold,
log the skip); ride along with the variant regeneration in §4 rather than its
own ADR.

## Recommendation

**Option (a): import-side junction-cluster consolidation in netimport**, with a
conservative threshold (consolidate non-internal edges with all lanes < 5 m that
connect two junctions; recursively for chains), reported per-cluster in the
import report. Reasons: it fixes the representation where it is wrong (these are
geometrically one intersection — the junction polygons literally overlap); it
keeps all physics in the existing, reviewed kernel paths; it needs no format or
contract change; its blast radius is exactly "new import → new scenario hashes →
regenerate variants/derived data/baselines", which the repo's hash machinery is
built to absorb; and the turnkey alternative (c) is measured not to work on
netconvert 1.27.1.

**ADR-ready decision points**

1. Consolidation rule: edge-level threshold (all lanes < 5 m? 7 m?) vs
   junction-shape overlap test; recursion for multi-sliver chains.
2. Rewire semantics: predecessors connect to the far junction's internals,
   internal lanes extended by sliver length (stop line moves upstream);
   cluster id scheme (`j:<joined member ids>`) for section/foe grouping.
3. Foe-set recomputation across merged clusters (same polyline-crossing code,
   widened scope) and `row` class inheritance for rewired approaches.
4. Signals: keep per-lane `tl`/`tlLink` bindings (both member programs
   survive); document that stop lines move and that programs were synthesized
   defaults anyway.
5. Identity/migration: new network file + hash; regeneration list per §4
   (variants, derived artifacts, bakes, brackets); mknetvariant donor-length
   guard rides along.
6. Validation: re-run the drain2 scenario; success = strand count and the
   top-8 stranding sections from stranding-02 collapse, no new collision or
   occupancy>1 lanes, M1–M3 CRC fixtures bit-identical (engine untouched).
