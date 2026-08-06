# ADR-0038: Consolidate sliver-coupled junction clusters at import

- **Status:** Accepted
- **Date:** 2026-08-05
- **Amends:** ADR-0017 (stop-sign override pipeline — the named revisit path
  for `--junctions.join`), ADR-0012 (scenario/network identity — new content
  hashes for every regenerated network, no schema change)
- **Scope:** `engine/netimport` plus ONE kernel change (the seam gate in
  `engine/rightofway.go` — see the review addendum below). Network-format v1
  schema is unchanged (contract text updated for multi-successor internals);
  old compiled networks load unchanged. Every network regenerated with the
  new importer gets a new importer-identity hash and new scenario content
  hashes. Existing recordings stay valid (hash-keyed).

## Context

The Chicago-throughput mission (`docs/chicago-throughput-log.md`) re-ran the
6-sim-hour drain experiment with a valid driver fleet (the original runs
were void — capacity 4, 99.9% coasting). The valid baseline drains: 8,696 of
10,583 injected complete, 299 active at horizon. The dominant remaining
defect is stranding: 1,588 trips (15%) removed by the ADR-0034 gridlock
escape, 100% workplace-bound, concentrated in short block faces between
signalized junctions in the CBD/near-north band
(`analysis/chicago-throughput/stranding-02.md`).

### Root cause

OSM maps divided intersections as one node per carriageway crossing, so the
SUMO source genuinely contains junctions meters apart. netconvert clamps the
connecting edge between the two junction polygons, which nearly touch: an
11.4 m geometric edge compiles to 0.2 m of usable lane. chi-loop-urban has
**1,758 road lanes <5 m (937 <1 m)**, 1,037 of them in the source net, 52%
connecting two traffic lights; 1,707 of them feed a junction box.

The kernel's exit rule (`rightofway.go` `exitWalk`) needs ~7 m of clear room
downstream and stops at the first queue tail. A sliver shorter than one
vehicle therefore has effectively **zero storage**: one vehicle stopped on
the sliver capacity-seals the upstream box, and chains of coupled junctions
strand dozens of vehicles each. Measured: section `1276540715` stranded 57
vehicles behind a single car parked on a 9.7 m pocket lane;
`1028896255#1` seals because its exit lands on `48785101#1`'s sealed
approaches.

### Alternatives considered

- **netconvert `--junctions.join` (regenerate the source net) — measured
  dead.** Dry-run on the real chi net (SUMO 1.27.1): join-dist 10 leaves
  1,041 short edges (from 1,037); aggressive settings (dist 20,
  parallel-threshold 180) make it *worse* (1,147). Refusals are "parallel
  incoming"/"after reduction" — exactly the divided-twin topology we need
  joined. (`/tmp/chidiag/jointest/`, `analysis/chicago-throughput/sliver-merge-03.md`.)
- **Kernel-side rule (treat sub-N-meter downstream storage as part of the
  box) — rejected.** The seal is physically real given the network;
  unblocking it by fiat is sanctioned box-blocking on every network,
  including the CRC fixtures, and hides the import defect in physics code.
- **Do nothing (let the escape strand)** — the escape converts long but
  finite delays into failed trips (~140+ strands at sections that later
  drained on their own); 15% trip failure is not a credible Chicago.

## Decision

Consolidate junction clusters **in netimport** at a ~5 m threshold,
recursive for chains:

1. **Delete** the sliver lane (a road lane whose usable length is below the
   threshold and which connects two junctions).
2. **Rewire** its predecessors into the far junction's internal lanes (those
   are already single-successor, preserving the internal-lane invariant —
   the naive "flag the sliver as internal" alternative breaks it because
   slivers can have 2+ successors).
3. **Extend** the internal lanes' geometry by the sliver length and
   recompute foe sets over the merged cluster with the existing polyline
   code.
4. **Signals** bind per lane, so both member junctions' programs survive;
   the stop line moves upstream by the sliver length — that is the intended
   behavior change (the cluster behaves as one box).
5. Demand files are unaffected: verified that **no demand origin or
   destination lane in chi-loop-urban is <5 m**.

The importer-identity hash changes, which self-documents the regeneration
in every derived network's provenance.

### Riding along

- `scripts/chicago/mknetvariant --add-lane` gains a donor-length guard:
  never clone a sub-threshold lane. (The widen1/widen2/gridwiden variants
  currently carry 7/14/881 sub-5 m `_w1` clones.)
- The `_d2` suffixes are **not** clones — they are netimport's
  sanitize-collision suffix for ± edge-id pairs (5,645 lanes). Not a bug.

## Consequences / migration

- New canonical chi-loop-urban JSON → new ADR-0012 content hashes for the
  17 chi scenario directories; old recordings and bakes remain valid
  (hash-keyed) but are of the old network.
- Base-bound delta variants (narrow1/widen/gridwiden/retime/kennedy)
  regenerate against the new base; derived artifacts
  (buildings/portals/corridors/zones/districts) are script-regenerated.
- Published chi bakes (`bases42x27k`, `chishow`, `cbdbase`, `kenbase`, …)
  rebake before the next publish; quiz/show pages re-pin hashes then.
- ADR-0036/0037 brackets re-baseline against the consolidated network;
  the drain harness (`scripts/sigctl-bracket.py` invocation pattern) is the
  measurement vehicle.
- Warm-start sidecars invalidate by design (ADR-0029).
- ADR-0021 permits ANY non-wall lane as a route destination, and demand
  files name lanes by id. Consolidation deletes sliver lanes, so a demand
  file that names a qualifying sliver as a destination (or origin) would
  fail validation against the consolidated network at load — importers SHOULD
  re-validate demand lane references after regenerating (chi-loop-urban was
  verified: no demand origin or destination lane is <5 m).
- ADR-0036's addendum ("no effect at 2× oversaturation") is **corrected by
  the same campaign**: with a valid driver fleet, adaptive routing cuts
  stranding −29% and time loss −16% at 2× oversaturation (drain2, seed 42).
  The addendum text gets a correction note when the brackets re-baseline.

## Validation

1. M1–M3 CRC fixtures bit-identical where no controlled internal→internal
   seam exists (the importer change is additive to new imports; the seam
   gate — review addendum below — fires only at such boundaries). Measured
   after the review fixes: full `go test ./...` green including the 13
   CRC/persistence/replay/restore tests; **no fixture network carries a
   controlled internal→internal seam**, so fixture coverage of the gate's
   blast radius on old networks is nil — accepted residual risk, stated
   explicitly, with the seam unit tests (`engine/clusterseam_test.go`) as
   the behavioral pin.
2. drain2 rerun on the consolidated network, same harness/seed: strand
   collapse at the stranding-02 top-8 sections, no new overlap sections,
   completions not worse.
3. Spot-check: zero road lanes <5 m connecting two junctions in the new
   canonical JSON; all 317 demand flows still resolve their origin and
   destination lanes.

### Addendum (2026-08-05, implementation notes)

Implementation (`engine/netimport/consolidate.go`) landed with four
clarifications relative to the text above:

1. **Foe sets recompute within junctions, not across the merged cluster.**
   The kernel compiles foes within a junction (`rightofway.go`); merging
   junction IDs would have churned sections/signal grouping/stop-duty for
   no behavioral gain. Point 3's "merged cluster" wording reads broader
   than what ships.
2. The import report's `Lanes` is now the emitted count; deleted slivers
   are audited in `Report.ConsolidatedSlivers`.
3. Rewiring creates **multi-successor internal lanes** (near internals
   inherit the sliver's fan-out). The loader never enforced
   single-successor and kernel probes are successor-general, so this is
   noted rather than prevented.
4. The 5 m threshold does NOT fix stranding section `1276540715` (the #1
   section, 57 strands): it seals via a 9.7 m pocket lane fed through a
   0.3 m *internal* lane. Expected outcome for validation item 2 is
   therefore collapse at 7 of the top 8; `1276540715` is deferred to the
   threshold/metering question, not silently dropped.

### Addendum (2026-08-05, external review rounds 1–2)

Two review rounds against the implementation; the consequential fixes:

1. **Kernel: the seam gate (`rowGate`'s internal branch,
   `engine/rightofway.go`)** — round-1 blocker (both reviewers), CONFIRMED
   before fixing: the old chi network had ZERO cross-junction
   internal→internal links, so every cross-junction movement approached the
   far box on a road lane under the full gate; consolidation created 3,467
   such seams (308 permissive-green, 155 minor, 902 major, 2,102
   protected-signal), and vehicles crossing internal→internal were gated
   only by `exitBlocked`, which skips approaching-foe yields. At any
   CONTROLLED internal→internal boundary the gate now applies: sigGate
   (stop line incl. permissive detection), and `foeApproachConflict` —
   rowConflict's approaching-foe half WITHOUT `boxWalk` (the entry-arm
   chain rule mid-box would re-serialize discharge; sigGate's green path
   runs its own `boxBlocked`, so signalized seams keep the exit-room
   check). **Blast radius on old networks:** any pre-existing controlled
   internal→internal chain (SUMO multi-part internals where only the final
   via carries the class) is now gated where it was not — changed physics
   with no importer-hash marker, since it is a kernel change. Accepted per
   validation item 1 (full suite + 13 CRC/persistence/replay tests green;
   fixtures carry no such seams — the accepted residual risk, stated
   explicitly).
2. **RowStop duty at seams** — round-2 blocker (both reviewers): the seam
   branch also enforces the stop-class full-stop duty with the set-at-line
   side effect (`stopDone` is consumed by the crossing into any internal,
   so a skipped duty is unrecoverable downstream — a no-foe arrival would
   have rolled the stop at speed).
3. **Parallel double-extension guard** — round-2 blocker (Sol): two
   independent slivers feeding one far internal no longer sum lengths and
   shapes; the LONGER sliver's extension replaces the shorter one's, and
   `Report.SharedExtensions` audits every such choice (fires 0 on the real
   chi import — proven, not assumed). Chain extensions still accumulate
   legitimately.
4. **Merge-foe detection by successor-set intersection** (Kimi round 1):
   with multi-successor internals (2,781 on the consolidated chi network —
   quantified), `Successors[0]` equality misses shared later successors.
   `contracts/network-format-v1.md` updated to match reality (internal
   successor wording; `foesMerge` = any shared successor).
5. Regression pins: `engine/clusterseam_test.go` (seam minor yield,
   permissive yield, stop-class full stop, unconflicted free pass) and the
   fan-out consolidation fixture in `engine/netimport/consolidate_test.go`
   (multi-successor rewire order, intersection foes, guarded extension,
   `engine.CompileNet` acceptance).
6. **Deferred, recorded:** Kimi nits 5–8 (round 1: stale comments, metrics
   boundary overshoot, `rep.Connections` units, fixture x= paste slip) and
   round-2 nits 3–4 (succs dedupe — corrupt-input class; /tmp scratch
   paths in analysis docs).

### Addendum (2026-08-05, external review round 3)

1. **Sol's chain claim, verified non-existent on this import.** The claimed
   residual gap was `internal → uncontrolled internal → controlled
   internal` chains, where the seam gate (immediate successor only) cannot
   see the downstream stop line from the upstream box. Measured on the
   consolidated chi network: **0 such chains** (scan: every internal lane's
   successor chain, both hops cross-junction). Deferred with an audit
   counter, same disposition as SharedExtensions:
   `Report.UncontrolledSeamChains` is computed on every import so a future
   network that produces the pattern surfaces it instead of silently
   changing gate coverage. No behavior fix — nothing to fix against.
2. **Kimi's ROW-seam room question, accepted tradeoff.** At non-signalized
   seams the gate omits `boxWalk`, so a minor/major/stop movement can enter
   the far box without clear-through room where the pre-consolidation road
   entry required it. The omission is deliberate: the in-box arm owns room
   there, and the entry-arm chain rule would re-serialize discharge (the
   defect it was written to avoid — measured on fixtures). The aggregate
   evidence says the trade is safe so far: drain3 strands went DOWN, not up
   (1,588 → 1,380 base arm; 1,128 → 813 adaptive arm), against the same
   demand and harness. Per-seam occupancy instrumentation was not built;
   if a future measurement shows seam-adjacent standstills, this is the
   first place to look.
3. Round-3 deferrals, recorded: chain-ancestry misclassification in the
   extension audit (audit-covered by construction), the test double-assign
   nit, and the isSliver silent resolution (corrupt-input class).

Real-import verification (`/tmp/chidiag/chi-loop-urban-consolidated.json`):
sliver road lanes <5 m feeding only internals 1,689 → 0; all road lanes
<5 m 1,758 → 69 (survivors are demand-facing boundary stubs); lanes
55,555 → 53,866; all 317 demand flows resolve.
