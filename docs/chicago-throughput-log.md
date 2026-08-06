# Chicago Throughput Mission — Log

**Mission statement.** Get the simulation to realistic Chicago throughput: a
heavily-loaded Chicago-scale network should discharge vehicles at rates
comparable to the real city, and should *work through* heavy load the way a
real city does (metering, gating, queues that eventually drain) instead of
collapsing into a 6,300-vehicle dead-stop. Success is measured on
`chi-loop-urban-half-base` (and its variants) with the standard bracket
harness — completions, mean speed, discharge rate at the horizon — across
multiple seeds, and the physics must stay honest (no deletion of vehicles as
a "fix", no teleporting, replay/determinism intact).

**Expected shape.** This will take many iterations; we expect 3–5 sizable
problems along the way. Each problem gets: diagnosis → minimal fix (ADR if it
touches contracts/data models) → bracket measurement → review gate → merge.

**Guardrails.**
- Triage bar per AGENTS.md addendum 2026-08-04: fix only blockers reachable
  in normal operation; defer corrupt-input/hand-crafted-payload findings.
- Flag-gate new behavior OFF by default until measured; default flips are
  their own deliberate decision.
- Determinism/replay is sacred: anything that changes the record plane or
  the run key needs an ADR.

---

## Background state (2026-08-05, mission start)

- The oversaturated-regime ceiling: profile injects ~11,400–12,800 veh/h peak
  into a grid that discharges ~6,000 veh/h. At 2× sustained oversaturation,
  adaptive routing (ADR-0036) and actuated signals (ADR-0037) both make no
  difference — the network dead-stops on schedule (seed 42: 0.10 km/h at
  60 min, 0.00 from 90 min, ~6,300 frozen vehicles).
- In the survivable regime both help: adaptive +22.7% speed / +24%
  completions (p=0.0006); actuated signals +8.4% speed / +10.8% completions
  (p≈0.001).
- Real cities in that state meter and gate. Our demand director currently
  fires every scheduled arrival as a verb regardless of network state; the
  kernel holds-and-retries a blocked entry per origin, but the director's
  own backlog handling and the aggregate demand-vs-capacity mismatch are
  where the ceiling lives.

## Open questions (diagnosis tranche)

1. What does the aggregate carry-over backlog look like at the seed-42 dead
   stop — how many vehicles are waiting to enter vs frozen inside? (Is the
   6k/h discharge a physical geometry limit or a control artifact?)
2. Where does discharge actually happen — how many sinks/exit portals does
   chi-loop-urban-half have, and what is their free-flow capacity sum? Is
   ~6k/h actually near the physical sink capacity (in which case the profile
   is simply too big) or is the network internally throttled (deadlock)?
3. Kernel/directory metering hooks: the spawner has DensityTargetPerKm; the
   demand-director path has none. What is the minimal honest metering
   mechanism — network-level density gate, per-origin queue with bounded
   wait, or ramp-meter-style rate control?

## Iteration log

### Iteration 0 — diagnosis (2026-08-05)

- Read `engine/spawn.go`: kernel spawner holds unmet demand per origin lane
  (one pending vehicle, schedule slips, `st.tick += ticks` from the original
  scheduled tick → effectively unbounded virtual queue at each origin).
- Read `engine/natsio/demand/director.go`: the live path samples the whole
  arrival program and publishes spawn verbs; kernel-side hold-and-retry
  applies per verb. Demand director has NO network-state feedback — it is an
  open-loop injector.
- Full diagnosis report: `analysis/chicago-throughput/diagnosis-01.md`
  (subagent analysis of drain + chi-half artifacts). Headline findings:
  1. **All three 216k-tick drain runs are VOID**: launched with default
     driver capacity 4 against a ~7,600-vehicle fleet → 99.94% of
     vehicle-ticks coasted uncontrolled. The 1.89M "collisions" were
     coasting vehicles parked inside queue tails. ADR-0036's "no effect at
     2× oversaturation / doomed regime" verdict compared two void arms and
     must be re-measured.
  2. Valid baseline (`chi-half-base`, 54k ticks, proper drivers): sinks are
     wide open — 90 exit lanes <20% capacity, 0 exit-bound stranded. The
     ENTIRE backlog is workplace-bound (944/944 stranded, 3,540/3,553
     active). ~6k/h discharge is an internal signalized-grid limit, not
     sink-side.
  3. Gridlock strands ~1,000/run (9–10% of trips), first at min 5–10 —
     some boxes seal from the first demand wave; possible geometry/program
     defect worth its own look.
  4. Completion profile is healthy-looking otherwise: peak ≈5.8k/h at
     min 30–40 decaying to ≈3.5k/h at horizon; mean trip 22.5 min vs
     11.7 min free-flow.

### Iteration 1 — valid drain rerun (2026-08-05, running)

- Rebuilt `engine/serve` at current main (incl. ADR-0036/0037).
- Launched the drain experiment properly, seed 42, 216,000 ticks
  (6 sim-hours), `-capacity 48000 -drivers 8 -pace 0`:
  - base arm: `data/runs/drain2-base/` (adaptive off, scenario pin)
  - adaptive arm: `data/runs/drain2-adaptive/`
- Question it answers: does the network actually dead-stop at 2×
  oversaturation with a functioning driver fleet, and does adaptive routing
  change the drain curve? This re-baselines the "doomed regime" claim
  before any metering/gating design.

**Result (both arms complete): the "doomed regime" was an artifact. The
network drains.** Seed 42, 6 sim-hours, valid drivers:

| metric | base (adaptive off) | adaptive | delta |
|---|---|---|---|
| injected | 10,583 | 10,613 | — |
| completed | 8,696 | 9,024 | +3.8% |
| stranded | 1,588 | 1,128 | **−29%** |
| active @ horizon | 299 | 461 | — |
| mean time loss | 2,251 s | 1,887 s | −16% |
| collisions | 1,263 (noise) | ~same | — |

- ADR-0036's "no effect at 2× oversaturation" verdict is **overturned**:
  adaptive routing cuts stranding by 29% and time loss by 16% in the heavy
  regime. The ADR-0036 addendum should be corrected (KB hygiene).
- The real problem ranking (from `analysis/chicago-throughput/stranding-02.md`):
  1. **Sliver-coupled junction chains (FIXABLE DEFECT)** — netconvert splits
     SUMO junction clusters into separate junctions meters apart, leaving
     1,758 road lanes <5 m (937 <1 m) network-wide, 1,707 feeding a junction
     box. `exitWalk` needs ~7 m room, so ONE vehicle stopped on a sliver
     capacity-seals the upstream box; chains of these strand dozens each.
     The `--add-lane` variant tool even cloned slivers (`_d2` suffixes).
  2. Genuine 2× core-grid oversaturation (INHERENT) — metering/gating,
     ADR-0037 follow-on.
  3. Gridlock escape converts recoverable queues into failed trips (~140+
     strands at sections that later drained) — design question, revisit
     after metering.
  4. Escape-blind permanent pockets (~300 vehicles; one anomaly: a vehicle
     parked 1+ h on a 0.2 m lane with empty forward chain) — FIXABLE, needs
     a keyframe/state dump to classify.
  5. "Sealed from the first wave" REFUTED at scale — only 7 lanes frozen
     from bucket 0–1; strands track the congestion ramp (peak 96/5min at
     min 70–75).

### Iteration 2 — sliver merge in netimport (2026-08-05, implementing)

- Target: cause #1. Merging sub-~5 m sliver lanes back into the junction
  box eliminates the zero-storage coupling that makes box-seals brittle.
- Touches netimport → regenerated networks → new content hashes → ADR
  (network-format contract) + re-measure on the drain harness before any
  scenario rebake.
- Design exploration: `analysis/chicago-throughput/sliver-merge-03.md`.
  Key facts: source net genuinely has junction clusters (OSM divided
  intersections); netconvert `--junctions.join` measured DEAD on SUMO
  1.27.1 (short edges 1,037→1,041 at dist 10, →1,147 aggressive);
  kernel-side unblock rule rejected (sanctioned box-blocking). Winner:
  import-side consolidation — delete sliver, rewire predecessors into far
  junction's internals, extend internals, recompute foe sets, signals
  survive per-lane. No demand origin/destination sits on a <5 m lane, so
  demand files stay valid.
- **ADR-0038 written** (`docs/kb/decisions/ADR-0038-sliver-junction-consolidation.md`)
  covering decision, alternatives, migration (17 chi scenario dirs re-hash,
  variants regenerate, bakes rebake, brackets re-baseline), validation
  criteria.
- Correction logged: `_d2` lanes are netimport sanitize-collision suffixes,
  not clones; the real mknetvariant bug is `--add-lane` cloning sub-5 m
  donors (`_w1`) — gets a donor-length guard riding along.

**Implementation (done, uncommitted):** `engine/netimport/consolidate.go`
(+ tests, 4 new), `netimport.go` audit field, `mknetvariant.py` donor guard
(+ test), importer-identity hash updated in `import-city.sh`. Full suite
green. Real import: sliver road lanes <5 m feeding only internals
1,689 → 0; lanes 55,555 → 53,866; all 317 demand flows resolve. ADR
addendum records 4 implementation clarifications, incl. multi-successor
internals now existing and the #1 stranding section `1276540715` being a
9.7 m pocket (ABOVE threshold) — expected to survive consolidation;
deferred to threshold/metering question.

**Validation (running):** drain3 = drain2 harness on the consolidated
network, both arms, seed 42, 216k ticks: `data/runs/drain3-consol-base/`,
`data/runs/drain3-consol-adaptive/`. Pass criteria per ADR: strand collapse
at 7 of top-8 sections, no new overlap sections, completions not worse.
(First attempt failed on the metrics manifest enumerating deleted lanes —
fixed by filtering exactly the 1,689 consolidated ids from
`metrics/main.yaml`; worth remembering mkmetrics.py needs a rerun when the
canonical regeneration happens.)

**drain3 base-arm result (vs drain2 base, same seed/harness):**
completed 8,696→8,966 (+3.1%), stranded 1,588→1,380 (−13.1%), active at
horizon 299→243, mean time loss 2,251→2,096 s (−6.9%), strand sections
293→227. Per top-8: 3 of drain2's top-8 sections fell out of the list
(−1285040209#2, 1031144476#0, 24115775#1), most others trimmed ~10–20%,
1276540715 57→52 (the expected 9.7 m-pocket survivor), but 435656622 went
35→48 and three new sections entered (31232230:47, 373854037#0:40).
Honest read: consolidation is a real but MODEST improvement — the top
knots are genuine oversaturation, not sliver artifacts. The metering work
(cause #2) is the real lever for stranding.

**drain3 adaptive-arm result (vs drain2 adaptive):** completed 9,024→9,562
(+6.0%), stranded 1,128→813 (−27.9%), sections →166, active at horizon
461→225, mean time loss 1,887→1,488 s (−21.1%). The two fixes compound:
adaptive routing exploits the freed box storage — on the consolidated net
the adaptive advantage over base is stranded −41% and time loss −29%.
Cumulative from mission start (original net, adaptive off → consolidated
net, adaptive on): stranded 1,588→813 (−49%), completions +10%, time loss
−34%. VERDICT: ADR-0038 validated (modest alone, strong with ADR-0036);
proceeds to the review gate.

**Review gate, round 1** (Kimi K3 + GPT-5.6-sol, archive
`docs/kb/raw/reviews/2026-08-05T233032-*`): Kimi no blockers (3
should-fix: contract doc drift on internal-lane successors, merge-foe
`Successors[0]`-only test, fan-out fixture). Sol claimed a BLOCKER:
internal→internal rewires bypass approaching-foe yields (rowGate's
internal special case only runs `exitBlocked`). Verified TRUE: old chi net
had zero cross-junction internal→internal links; consolidation created
3,467 (308 permissive, 155 minor-class, 902 major). Fixed in
`engine/rightofway.go` — controlled internal→internal boundaries now apply
sigGate + a `foeApproachConflict` check (rowConflict's approaching-foe
half, no boxWalk); uncontrolled paths byte-identical, CRC fixtures
bit-identical, suite green + 3 seam regression tests. Also shipped:
contract doc updated (2,781 multi-successor internals documented),
merge-foe set intersection, fan-out fixture, SharedExtensions counter
(fires 0 on chi), ADR migration note on destination-on-sliver validation.
Deferred (commit message): Kimi nits 5–8. Watch item resolved en route:
drain3-base's 16,854 collisions = pre-existing placement physics amplified
by longer queues (one-for-one rewires only; no occ >1.0; adaptive arm
clean) — kernel placement hardening is its own future ADR, not a rider.

**Metering design (next ADR) pre-drafted** at /tmp/chidiag/metering-04.md:
the kernel gate already EXISTS (DensityTargetPerKm covers director
directives, scenario-declared, hashed, replay-free); M1 = hysteresis
floor + gate accounting. Measured MFD target band: cap ≈4,500 active,
resume ≈4,000.
