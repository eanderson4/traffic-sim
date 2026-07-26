# Podcast Demo Work-Queue (agent-dispatchable)

> Working todo for the demo-matrix / trust-building program. Each item is written so a
> fresh agent can pick it up cold. Owner adds items at the bottom; strike or annotate
> when done. Podcast deadline is ~2 days out — prioritize engine trust over polish.
>
> Ground rules: `AGENTS.md` (read `docs/VISION.md`, KB-first, ADRs for consequential
> decisions). Every non-doc commit: run `scripts/external-review.sh` first and triage
> Fable's findings (it prints them even when Sol fails). Sol recovered 2026-07-24
> morning (credits topped up) — both reviewers live again. (Had Sol still been down,
> the owner authorized committing with `EXTERNAL_REVIEW_SKIP=1` after the Fable pass,
> recording Sol's failure count in the commit message.) Do not skip the script
> itself — that would bypass Fable too. Never push.

## Operational state (2026-07-24 afternoon)

- Engine + all fixtures green. Kernel is ~6× faster post-WQ-15 (41.4 →
  6.7 ms/tick avg on the stress bench; sim hour at 14.7k vehicles:
  905 s → 246 s wall). Realtime ceiling now ~25-30k vehicles (est).
- Recording wall GONE: ADR-0015 keyframe chunking validated to 14.5k
  vehicles (was a run-killer at 10.9k). Commits: 8fbb5a1 (code),
  4026902/6b3ae4b (docs), WQ-15 + metview committed 7dfe5bb (r8 gate clean).
- metview (engine/cmd/metview): `metview -addr 127.0.0.1:8910 *.metrics.json`
  — server-rendered comparison dashboard (neutral per ADR-0014 §7; no
  single-seed ranking colors).
- THEME SESSION owns and has dirty: `viz/src/*`, `engine/cmd/demosrv/*`,
  `engine/geojson.go*`, `engine/natsio/driver/driver.go` (+ new
  router.go/router_test.go/driver_test.go), `engine/cmd/{serve,replay}/main.go`,
  `engine/natsio/bus.go`, untracked `scripts/` importers + `viz/src/{stopsign,layertoggles,netload}.ts`.
  Its arc (from the owner): stop signs + paper theme → 6-city re-import with
  control nodes → LA-scale bugs (heap Dijkstra, 64MB max_payload — REVERTING
  to ~4MB + chunked TSSG table per owner directive, DESIGN PENDING a
  Fable+Sol design round — see below). Demos on :8931. DO NOT stage its files.
- TSSG v2 design brief DELIVERED (2026-07-24, this session):
  `docs/kb/decisions/ADR-0016-tssg-chunking.md` (PROPOSED) — chunked TSSG via
  `sig_chunk: "i/n"` + `sig_gen` headers mirroring ADR-0015 (no-header = whole
  table, no schema bump), per-chunk-valid frames, publish-once-at-start +
  request-reply catch-up (`ts.{run}.state.sig.req`, pull not push — the actual
  busy-tab fix), 20-tick rebroadcast + paused-replay table firehose removed,
  loud publish errors on BOTH live frames, max_payload 64MB→4MB as documented
  headroom (ADR-0006 §5 doctrine amendment), TSSF + TSOB recorded as the next
  unbounded frames. Theme session implements, runs its own Fable+Sol round,
  flips the ADR to ACCEPTED, updates asyncapi + the serve size comment.
- Scratch artifacts in /tmp (regenerable): ts-stress + ts-wq15 (HEAD/patched
  extracts), serve-stress/serve-wq15/metview binaries, wq4store3 +
  wq15store (stress recordings), wq4prof* (kernel bench + pprof profiles),
  chkstress (recording verifier). /tmp/wq4srv = WQ-4 instrumented serve.
- data/ is gitignored: podcast variants + recordings + demos.json never
  touch git.
- Old operational notes (2026-07-23): demosrv :8900 recipe, headless
  screenshot recipe, demo registry — still valid; /tmp verify-demos.mjs
  gone on reboot (regenerate from freshness note (d)).

## Open issues (viz, from live viewing)

### WQ-0: Replay deferrals from the -pace/-store review rounds (2026-07-23)
- **dt from registry meta, not `?dt=`** — the viz trusts a hand-copied URL param
  (silent 0.1 default); authoritative dt lives in `RunMeta.Spec.Params.Dt`. When the
  viz grows registry access (or via the demosrv params API, which already exposes
  dtS), drop the URL param. Until then a stale/omitted dt on a non-0.1 scenario
  silently scales all speeds + the congestion overlay.
- **Pace is an unrecorded run condition** — same (scenario-hash, seed) at different
  paces yields different traffic (client latency scales in ticks). Code-commented at
  `maxClientPace`; cross-pace metric comparisons are INVALID. Stamp pace into the
  registry meta when the meta schema next changes (ADR note needed).
- **SIGINT leaves durable run meta "running"** — with `-store`, an interrupted run's
  KV entry persists as forever-running. Demo-acceptable; finalize as aborted when
  lifecycle matters.
- **60-frame SnapshotBuffer cap** covers the 250 ms buffer only to ~24× wall delivery;
  replay decimates to ~10 Hz so this is moot until someone raises decimation.
- **Chord-vs-path speed underestimate** grows with tick-skip on curved geometry (nit;
  speeds are a labelled client-side estimate).
- **Replay-panel deferrals (Sol, C3 final round, 2026-07-23):** (a) trailer
  articulation integrates `sample.tick` which is the newer snapshot's INTEGER tick —
  sim time advances in one burst per snapshot, so trailers jerk instead of following
  the lerp (cosmetic); (b) ~~panel polls have no in-flight guard~~ FIXED 9cea740;
  (c) demosrv registry accepts `"kind"` as an input field and overwrites it —
  should be rejected by the strict fence or moved to an output DTO; (d) ~~the
  panel's divergence warning shows crcErrors only~~ FIXED 9cea740 (warnLine
  composes both counters). New from the 9cea740 rounds: (e) poll-vs-control-POST
  stale-overwrite race (pre-existing, self-heals within ~1 s — fix via request
  generations when the control plane next changes).
- **Signal-head round-3 deferrals (Fable + Sol, 2026-07-23, commit 62b6237):**
  (a) the late-join test's freshness bound measures from the watcher's last
  observed tick, not the subscribe instant — flake window on a loaded box
  (re-sample after subscribe or widen); (b) `signalCatchUpEvery` is
  tick-denominated — the "~2 s catch-up" promise assumes dt = 0.1; at
  ADR-0005's dt = 1 s ceiling it is 20 s. Derive the cadence from dt or drop
  the wall-time wording if dt ever varies; (c) the duplicate-keyframe
  dirty-store check compares only the first index pair (pre-existing);
  (d) asyncapi `info.version` left at 2.1.0 though the documented
  convergence promise tightened ≤100 → ≤20 ticks (nit); (e) greedy
  running-centroid clustering can drift (early member ends >75 m from the
  final centroid; Y-arms within 90° merge) — deterministic, cosmetic,
  revisit only if a real net shows the artifact.
- **Signal-bar round deferrals (2026-07-23, commit 7aad2a3):** (a) the 45°
  cone is a per-join gate against the running mean — a 0°/40°/60° bearing
  bridge can still merge distinct arms (Sol) — compare against all members
  or a fixed representative if a real net shows it; (b) bars draw through
  the centroid, so entries staggered along the travel axis get a
  misaligned bar (cosmetic); (c) off-state bars share the `noData` lane
  tone and can vanish over no-data lanes (Fable question — accepted);
  (d) coarse left-turn chord bearings can split one true approach into
  two heads (Fable, cosmetic); (e) bar state-coloring is screenshot-
  verified only — no headless assertion on the shared-id feature-state
  path; (f) legend still says "one per movement" — update to "one per
  approach + stop bar" (legend.ts was mid-refactor by the theme session).

- **metview round deferrals (Fable + Sol r3–r8, 2026-07-24):** (a) sink
  interval q/k/occupancy/time_loss_s are `*float64` and stops `*int`, all
  omitempty — a group-off file decodes as real zeros (raised 5× by Sol incl.
  the r7 blocker; HELD — documented at the struct; no current binding
  disables groups, so no such file exists; fix = pointers + column
  suppression when such a file exists); (b) two files sharing a basename
  render indistinguishable chips (use paths or a dedup suffix if it bites);
  (c) the past-map/bucket slices reallocate per tick — Engine-owned scratch
  if GC ever shows (it hasn't); (d) metview's basename→label uses
  filepath.Base; the JSON /api is human-local scripting under the ADR-0002
  loopback carve-out (documented in the package doc, NOT an inter-service
  boundary); (e) `denied_by_lane` is intentionally NOT mirrored into metview
  (per-lane array; the summary already carries the denied-* aggregates —
  Fable r7 question answered); (f) no focused regression test for the WQ-15
  boundary bucketing (Sol r7 should-fix — deferred; the CRC-pinned fixture
  suite + the bit-exact stress bench are the current coverage; add
  lower-index multi-hop/higher-index deferral cases when boundaries() is
  next touched); (g) censored trips are excluded from per-type distance/
  time/loss/stop aggregates (Sol r8 should-fix — survivorship-bias reading
  of ADR-0014; completed-only is cemented by main_test.go, so this needs
  an ADR-0014 interpretation before changing v1 semantics — likely answer:
  surface a censored-inclusive time-loss bound column rather than folding
  partial trips into means); (h) no test asserts the dropped_crossings
  passthrough (Fable r8 nit — one fixture line when next touched). FIXED in r7: `dropped_crossings` mirrored into the totals
  struct + summary column (Sol r7 blocker — ADR-0014's loud integrity
  signal), stale schema-pointer comments, single-seed caution line on the
  comparison page.

- **WQ-3 zoom-gate round deferrals (Fable + Sol, commit f6b9d56):** (a) the
  layertoggles "names only real layers" test checks id uniqueness, not that
  the ids exist in main.ts — a typo'd id silently no-ops the toggle
  (pre-existing weakness; Fable r2 nit); (b) casing comment still explains
  the edgeB fallback in terms of junction interiors when only stale caches
  hit it now (Fable r2 nit, wording). Round-1 finding FIXED before commit:
  internal casing stacked above the external congestion line (both
  reviewers) — order is now both casings below both lines. Fable r2
  question (z11–12 junction mouths) answered by the live zoom-out
  screenshot: crossings read clean on external round caps alone.

- **Keyframe-chunking round deferrals (Fable r3 + Sol r2/r3, commit 8fbb5a1):**
  (a) MaxAge retention expires messages individually — a chunk group
  straddling the frontier leaves an orphan tail that fails ALL seeks, and
  `indexLogMsgs` counts `i==n` without group validation (recorded in
  ADR-0015 consequences; MaxAge is 0 today); (b) asyncapi `kf_chunk`
  pattern admits `0/3` (the parser enforces 1≤i≤n); (c) the header
  canonicalizes to `Kf_chunk` on the wire (pre-existing pattern, matters
  only to case-sensitive non-Go consumers); (d) netstats: last-lane-wins
  edge length, SignalShare numerator/denominator sets differ, unstable
  table sort (analysis-tool-grade); (e) the chunk test's seek-anchor
  assertion is vacuous when the keyframe group is the stream tail
  (harmless: CRC/verb messages always follow in the test).

### WQ-1: Spurious/"extra" signal heads at junctions — FIXED (2026-07-23, 62b6237 + 7aad2a3)
Root cause was per-link duplication, not fragment artifacts: the wire's link
index is one per SUMO connection (from-lane→to-lane), so wide multi-lane
approaches rendered a head per lane-movement (~25 heads at one junction).
Fix in `viz/src/signals.ts`: cluster bound links by (identical state column
across all phases) AND approach geometry (≤75 m to the running centroid,
entry bearing within ~45° of the running mean — tightened from 90° after
skewed Wilshire X-junctions under-merged: 610/824 column-groups mixed
bearing sectors). One head per cluster, set back 3.5 m along the approach
bearing, plus a colored STOP BAR across the cluster's lanes (same feature
id → same state color) as the "which approach" cue. Measured heads
(per-link → column-only → approach split): Manhattan 3256 → 1100 → 1180,
Wilshire 3038 → 824 → 1407, Boston 1085 → 438 → 536, stress-DTLA
6025 → 1625 → 2514. Registration delay fixed engine-side: TSSG catch-up
republish moved from the keyframe cadence (100 ticks = 10 s at 1×) to
`signalCatchUpEvery` = 20 ticks (`engine/natsio/run.go`; contract +
ADR-0006 addendum updated). Residual, deferred: mid-zoom (13–15) declutter
at dense junction clusters (e.g. the San Vicente diagonal) — consider
count-badged single heads.

### WQ-2: Trailer jackknife physics — FIXED (2026-07-24, 1d4b351)
`viz/src/artic.ts` — trailers rendered perpendicular/detached and never
recovered: the sine law's recovery from a >90° hitch upset (lane-hop
teleport, reversed-fragment heading flip) is repelled through
near-perpendicular at a rate ∝ v, and below HOLD_V the angle froze
forever. Fix: a glitch clamp re-anchors the trailer aligned when the
pre-integration hitch angle exceeds `glitchLimit(v, dtS)` — base 85°
(legit sub-10 m-radius turns hold ~80°), widened by (v/L)·dtS (tight
turns at 8× replay sweep ~50°/snapshot), capped at 170° (wrapPi's π
bound), gated on dtS > 0 (dtS bursts per snapshot while the lerped
heading advances every frame; glitches only arrive on snapshot frames).
Five Fable+Sol rounds. Deferrals (accepted, early-project triage bar):
(a) a flip revealed on dtS=0 frames + immediate stream halt leaves the
pose until resume (Sol r5, rejected-blocker: self-heals, frozen view
only); (b) starved-buffer samples report v=0, collapsing the widening
to bare 85° mid-stall (Fable r5); (c) mid-bracket phasing can integrate
part of a flip before the next burst check (Fable r5 question); (d)
r < TRAILER_M geometry (no articulation equilibrium) pops repeatedly —
least-bad rendering of impossible geometry (Fable r4 question);
(e) >86° unsmoothed polyline vertex steps snap rather than swing
(Fable r4 question). The lumped-dtS jerk (WQ-0 replay (a)) is the real
underlying fix for fast-replay articulation accuracy.

### WQ-3: Junction-interior squiggles at city zoom — FIXED (2026-07-25)
Network lane layers split into external + internal pairs in `viz/src/main.ts`:
external lanes draw at all zooms, `internal:true` lanes (junction interiors)
are on `network-internal-{casing,line}` with minzoom 13, matching the signal
heads. Shared paint/layout consts so the pairs can't drift; congestion toggle
in `layertoggles.ts` hides both line layers, casings never toggle. Verified
live on manhattan-grid (zoom-out: clean bands, no squiggles/heads; zoom-in:
detail returns). Note: the theme session's viz rewrite had dropped the old
`internal` reference entirely — junction interiors were drawing as FULL
casing at every zoom (edgeBoundaries treats group-less lanes as boundaries).

## Engine trust / architecture

### WQ-4: Big-simulation stress test (owner priority) — DONE (2026-07-24)
Full report: `docs/kb/raw/wq4-stress-test-2026-07-24.md`. Verdict: **bounded,
graceful FAIL** — the architecture holds to ~10.9k live vehicles, then the
record-plane keyframe exceeds NATS `max_payload` (1 MB) and the run ABORTS
loudly (registry `aborted`, BENCHMARKS.md §(b) confirmed in production
conditions: real keyframe ≥92 B/veh vs the 77 B/veh microbench estimate, so
the wall is ~10.9k not ~13.6k). Headline correction: **kernel Step ≈ 7 ms +
12.4 µs/vehicle/tick** (linear) — realtime ceiling ~7.5k vehicles on this
box; the "~7 ms/tick at 8k" figure in gaps-and-roadmap note (b) is wrong by
~15× (correct it there when the theme session's pending edits to that file
land). Below the wall: NATS/ws pipeline clean at 276 kB snapshots (1:1 with
ticks), RSS flat ~2.2–2.8 GB, metrics sane (12.8k vehicles, mean 4.16 km/h
gridlock, 4.04M s denied-entry wait). Pause-gate deadlock NOT reproduced
(-capacity 50000 kept the gate unarmed) — the underlying deadlock remains
unfixed. Follow-ups (new items below): keyframe chunking/Object Store is now
a DEMONSTRATED run-killer (bump priority — it gates city-scale recordings);
pause-gate escape/loud-metric; `dropped_crossings` = 83k on stress-dtla-high
(correlate with `_d2` fragments). WQ-10 benchmark queue can now take the
measured numbers (report §Follow-ups item 5). Run artifacts:
`data/scenarios/stress-dtla-high/`, `data/recordings/wq4-stress{,2}/` +
`wq4-stress2.metrics.json` (19.5 MB).

### WQ-11: Keyframe chunking — DONE + VALIDATED (2026-07-24, ADR-0015)
Keyframes > `KeyframeChunkMax` (default 768 KiB) are split into consecutive
`ts.{run}.log.keyframe` messages with a `kf_chunk: "i/n"` header; no header =
whole keyframe (old recordings unchanged); the seek anchor is the keyframe's
LAST message so re-sim resumes at seq+1. `findKeyframe` reassembles + fails
loud on malformed/out-of-sequence/stream-non-consecutive groups;
`indexLogMsgs` counts one entry per keyframe. SchemaVersion stays 2. Tests:
`engine/natsio/keyframe_chunk_test.go`. **Production validation:** the WQ-4
stress scenario re-run from 8fbb5a1 (`wq4-stress3`, same config as the Run A
abort) completed all 7200 ticks, status done, 14,537 vehicles at horizon
(+34% past the 10,862 wall); 31 chunked keyframes (n=2) on the stream;
CRC-verified replay to tick 7200 through a chunked anchor. Store:
`/tmp/wq4store3` (517 MB), metrics `/tmp/wq4-stress3.metrics.json` (both
/tmp — regenerate with `-store` into data/recordings/ if worth keeping).

### WQ-12: Replay materialization footprint vs demosrv readiness timeout — DE-RISKED (2026-07-25)
Found wiring the podcast recordings: the replay child materializes a
recording in memory before listening — a 36000-tick i280 recording (3.3 GB)
took ~40 s and ~23 GB RSS, so demosrv's old 10 s child-readiness timeout
tore it down ("did not become ready"). The theme session's supervisor
rework (47935dc) fixed the ordering: readyTimeout is now 60 s and the
replay control listener binds only AFTER NewPlayer materializes
(engine/cmd/replay/main.go:105→114), so demosrv's ctl-port probe covers
materialization — the 40 s i280 hour fits with ~1.5× headroom. Podcast
demo cards point at 15-minute recordings (369 MB, ~5 s, ~2.7 GB RSS).
REMAINING (post-podcast): stream the materialization (NewPlayer holds
~7× the store size in RSS) and bound the wait by recording size, not a
flat 60 s — an LA-scale hour recording would blow both.

### WQ-13: Pause-gate deadlock escape hatch — DONE (2026-07-24)
`engine/natsio/contract.go`: while the gate is engaged the engine logs a
heartbeat every `PauseLogEvery` (default 10 s — demand / spare capacity /
active vehicles), and after `PauseEscapeAfter` (default 60 s) of persistent
deficit it resumes anyway on hold-last (ADR-0008 §6 already sanctions
hold-last bridging) with a loud log; the escape resume is identifiable on
the record plane by `demand > available`, no schema change. One-way latch
prevents pause-livelock; the gate re-arms only after capacity recovers.
Test: `TestPauseGateEscape` (`engine/natsio/pauseescape_test.go`) — wedged
gate, heartbeats, escape, run completes, replay CRC matches. ADR-0008 §6
clarification block added.

### WQ-5: Driver-lag divergence (skip-guarded test)
`TestDifferentialLanedrop` wave-envelope assertion skip-guarded on a documented
signature; needs a driver-model fix. Details in the fixture test file.

### WQ-14: Pause-gate escape semantics (Sol r3 blocker, deferred from 8fbb5a1)
The WQ-13 escape latch disables the capacity gate until capacity recovers
at least once; in a never-recovering jam the run free-runs on hold-last
indefinitely (new unclaimed vehicles coast; finite HoldLastTicks exhausts).
Alternatives on the table: (a) current one-way latch; (b) re-arm per
escape — the gate dwells 60 s, grants ~PauseAfterTicks of progress, dwells
again (honest backpressure, crawl instead of wedge); (c) abort the run
loudly after the dwell. Belongs with the WQ-5 driver-model ADR-level pass.
Context: the gate arming at all is an operator sizing error (WQ-4 ran
-capacity 50000 so it never arms); the escape exists for demo-liveness.

### WQ-15: Kernel hot-spot fixes from the 180 ms/tick profile (2026-07-24)
CPU profile of the stress-dtla kernel (scratch harness, pprof): `boundaries()`
was 67% of Step — an O(lanes × vehicles) sweep (39,971 lanes × 14.5k vehicles
≈ 581M iterations/tick) to find the few hundred vehicles past a lane end;
`TotalLaneKm` 5.7% (40k-lane sum recomputed per density check). Fixes: bucket
past-end vehicles by lane in one O(vehicles) pass (state-identical — per-
vehicle handling is independent; crossers re-enter not-yet-visited buckets
exactly as the sweep re-scanned them), and a lazy cached `totalLaneKm` on
Network (fixed slice order = bit-identical). `leaderAt` (7%) is legitimate
car-following. Full fixture suite (CRC-pinned) green. Before/after stress-net
bench (7200 ticks, seed 42): final CRC `280df03591679821` IDENTICAL both ways;
wall 4m57.7 → 48.3 s (41.4 → 6.7 ms/tick avg, 2.4× → 14.9× realtime at the
harness's 3.9k-vehicle peak — the removed work scales lanes×vehicles, so the
14.5k-vehicle live Step (~180 ms) should drop to ~50 ms). Tool: `metview`
(engine/cmd/metview) — run-comparison dashboard for M13 metrics JSONs
(totals side-by-side, worst lanes by time loss/k/occ/stops, trips by type);
verified against the four i280-pod variants. Parallel-kernel discussion
(14 cores): kernel is single-threaded by design (ADR-0005 bit-exact
determinism); deterministic phased parallelism is an ADR-level option AFTER
accidental quadratics are gone. Controller-side compute (routing, planning)
is already offloaded — controllers are NATS processes/goroutines off the
tick path; the tick is ~92% kernel Step, so Step fixes dominate offloads.

### WQ-16: TSSG v2 — chunked signal table + request-reply resync (THEME SESSION owns)
Design: `docs/kb/decisions/ADR-0016-tssg-chunking.md` (PROPOSED, 2026-07-24,
settled by a Fable+Sol design round). Kills the 64MB max_payload stopgap
(→ 4MB headroom), chunks LA's 7.3MB table into ~10 ≤768KiB messages, and
replaces the wall-clock-blind 20-tick rebroadcast with pull-based catch-up
(`ts.{run}.state.sig.req`) — the piece that actually fixes busy-tab
slow-consumer drops. Implementation + its review round + asyncapi update are
the theme session's; flip the ADR to ACCEPTED when it ships.

### WQ-6: Roundabout circulation
Directive expiry + yield conservatism; ADR-level. Fixture: `engine/fixture_roundabout_test.go`
(Circulation SKIP-guarded). Not podcast-blocking.

### WQ-7: stopDone keyframe persistence (v4)
Deferred until recordings exist. Not podcast-blocking.

### WQ-17: chi-loop origin–destination demand (ADR-0021) — DONE 2026-07-25, review pending
Replaces chi-loop's portal-inflow-with-random-egress demand with a
building-anchored OD program. Scenarios: `data/scenarios/chi-loop-od`
(3 h AM profile, the metrics run) and `chi-loop-od-peak` (flat peak rate,
the recordable cut — a store always starts at tick 0, so recording the
ramped scenario would only ever capture 06:00–06:15). Pipeline:
`scripts/chicago/buildings.py` (OSM footprints → floor area → snapped
access lane) → `mkod.py` (OD demand YAML) → `congestion.py` (delay ranked
by STREET NAME, not lane id). Engine: arrival despawn, verb
`destination`/`offset_m`, rear-clearance-checked interior injection, and
the LATERAL route guardrail — see ADR-0021.

> **Status 2026-07-25 (2nd pass).** The corrected import shipped
> (`--junctions.right-before-left.speed-threshold 0`, ADR-0022 §6) and the
> engine bugs are fixed. Everything below was re-measured on it. The old
> collision A/B and delay-share comparison are WITHDRAWN, not re-run — see
> ADR-0022; the decision they justified is settled.

Closed since first writing:
- **Route recovery now crosses any number of lanes.** The reachability
  predicate became a lateral-depth gradient (`routeLatDepth`: layered 0-1
  BFS from the destination over the reversed lane graph, successor edges
  cost 0, `Left`/`Right` links cost 1). The veto denies any hop that
  increases depth; recovery descends it. `TestRouteRecoveryCrossesTwoLanes`
  pins the case the predicate could not solve. Full Go suite green,
  including the CRC-pinned fixtures — unrouted vehicles are untouched.
- **The network no longer gridlocks.** On the corrected import a 10k–20k
  demand bracket all flows. At the shipped 16,000 veh/h target: 26.0 km/h
  mean network speed, 53% delay share, 96% of despawns are arrivals, 4,996
  collision observations over 18,000 ticks — against **975,673** (774,172 in
  one junction) on the defective rbl import.
- **Demand re-tuned to a 16,000 veh/h target / 12,960 injected**, shipped as
  `data/scenarios/chi-loop-od-30m` (18,000 ticks = 30 sim min, flat peak).
  Chosen over 20,000 because collision observations grow superlinearly past
  it (+25% demand → +71% collisions) for 1.7 km/h of extra congestion.
- **Corridor attribution exists now.** `scripts/chicago/corridors.py` builds
  a lane → named-corridor map (`corridors.json`) for the Kennedy, Dan Ryan,
  Eisenhower, Stevenson, Lake Shore Drive and the Jane Byrne Interchange —
  by OSM name substring, with the Byrne matched geometrically since it has
  no name. `congestion.py --corridors` ranks them by delay per lane-km.
- **`congestion.py` reports delay PER PERSON** and person-hours per street
  behind an explicit `--occupancy car=1.2,truck=1.0` flag that is printed
  with every report (VISION.md use case 4).
- **The recording is watchable again.** The 30-minute cut was 9.1 GB, and
  `replay` materializes the whole store before it serves (~7× RSS ≈ 60 GB),
  so nobody could open it. Re-recorded as a 15-minute cut at the retuned
  rate: **2.3 GB store, 17.0 GB replay RSS**, loads in ~2 min, `/status`
  and `/seek` verified, 0 CRC errors, 0 verb errors. The horizon is now
  documented in `chi-loop-od-peak/scenario.yaml` as a storage budget.
  General lesson for any future city recording: budget ~150 MB per
  simulated minute per 1,000 live vehicles, and remember replay RSS is ~7×
  the store.

Still open on this thread:
- **THE VALIDATION GAP: the expressways do not congest.** Six of the ten
  corridors in the Chicago hotspot research report
  (`~/grove/research-bot/.research/chicago-traffic-hotspots/REPORT.md`) are
  inside the chi-loop extract, and at the shipped rate all six run free —
  Kennedy 72.1 km/h, Eisenhower 79.4, Dan Ryan 78.6, Stevenson 78.3, Lake
  Shore Drive 53.4, Jane Byrne 48.5 — carrying 3.3% of network delay
  between them while the arterial grid takes 94.7%. The report singles the
  Kennedy out as having the **lowest peak truck speed of any Chicago entry
  (19.1 mph / 31 km/h)**; we have it at 72.
  **This is structural, not a tuning knob.** `mkod.py` sets portal rates as
  `class_rate × (total × portal_share / portal_raw)`, so the per-class table
  is only a shape and `--total` sets the level. At `--total 16000` the scale
  factor is ≈0.24 and the Kennedy's two boundary origin lanes inject
  337 veh/h/lane — about a sixth of a freeway lane's capacity, and a sixth
  of the corridor's real ~275,000 veh/day. Per-corridor injection at 16,000:
  Kennedy 674 veh/h (2 origin lanes), Stevenson 674 (2), Dan Ryan 1,348 (4),
  Eisenhower 1,348 (4), LSD 2,092 (8), everything else 10,063 over 216
  lanes. Reaching the motorway class rate needs `--total ≈ 67,000`, which
  buries the arterial grid. **One scalar cannot congest both.**
  **The fix is measured and works.** Multiplying only the `-motorway`/
  `-trunk` portal flows by 4.15 (337 → ~1,400 veh/h/lane, the class rate),
  arterial and residential demand untouched, over matched 6,000-tick runs:
  the expressways go from **4.7% to 52.9% of network delay**, Eisenhower
  82.5 → 37.4 km/h (its real ATRI peak truck speed is 36), Dan Ryan
  74.5 → 27.2, Stevenson 82.6 → 33.1, LSD 60.1 → 30.4 — and the network
  still flows (mean 36.4 → 29.5 km/h, collisions 216 → 1,043, no gridlock).
  Two stay too free: the Kennedy (50.1 vs a real 31) has 681 lanes in the
  crop but only 2 boundary origin lanes, so its inflow spreads thin; and the
  Jane Byrne barely moved because its lanes are `motorway_link` and the
  experiment's filter only matched `-motorway`/`-trunk`.
  Implementation: a `--freeway-scale F` flag on `mkod.py` injecting
  motorway/trunk/`*_link` portals at `F × class_rate` directly, bypassing
  the zone scalar. NOT implemented — it changes what the demand grammar
  means and belongs with the ADR-0021 generator work, not a tuning pass.
- **A batch metrics run is memory-bounded by `Engine.IntentLog`, and the
  3-hour chi-loop AM run does not fit in 123 GB.** `IntentLog` accumulates
  every applied intent for the whole run and is never drained, so RSS grows
  as fleet × ticks. Measured on `chi-loop-od` at 9,000 veh/h with
  `-metrics-out`: **~1 GB per 1,000 ticks at a 3–4k fleet**, and a marginal
  rate of ~2.4 GB/1,000 ticks once the fleet passed 3,900 — 45.8 GB at tick
  47,200, projecting past 120 GB before the 108,000-tick horizon. The run
  was killed and re-run at 54,000 ticks (06:00–07:30: the ramp plus the
  first plateau half-hour), which is what
  `data/scenarios/chi-loop-od/am-peak-build.congestion.txt` reports.
  This is unrecoverable rather than degrading: **metrics are written only at
  run end, and SIGINT abandons the run** ("demo mode does no graceful
  finish"), so an OOM at tick 90,000 loses the whole run. Fix directions, in
  order of cheapness: (1) a `-intent-log=false` flag for batch runs that do
  not need replay-from-log, (2) write metrics incrementally or on SIGINT,
  (3) cap/ring-buffer `IntentLog`. Until then, the 3-hour AM profile in
  `chi-loop-od/scenario.yaml` is a horizon no box here can measure — treat
  `ticks: 108000` as an aspiration, not a runnable default.
- **`dropped_crossings` is one counter mixing ≥4 causes** and is dominated
  on dense grids by the multi-successor overshoot refund, not by anything
  routing-related. Split it per reason before anyone tries to act on it
  (this also answers WQ-4's "correlate with `_d2` fragments").
- **chi-loop has no residential street grid** (107 residential + 108 service
  lanes of 23,833 — the `motorway…tertiary` import filter), so residents
  inject onto arterials they do not front. Re-import decision.
- **Residential mass is under-counted**: 71% of footprints are
  `building=yes` with nothing to disambiguate, so Chicago's 2- and 3-flats
  produce no trips.
- **Every other US network still has autobahn speed limits** (ADR-0022's
  table is the queue): la, sf, miami, atlanta, houston, dallas,
  boston-core, phoenix-arterial, manhattan-grid, stress-dtla, chi-kennedy,
  chi-north-lakefront. Any level-of-service number from those is wrong in
  the same direction.

## Program items (bigger, parallelizable, post-podcast unless cheap)

### WQ-8: GIS network analysis (LA vs NY) — DONE (2026-07-24)
Report: `analysis/networks/README.md` (+ `netstats/` stdlib Go tool, 13
compiled networks). Headlines: (1) control regime is the story — NY's
conflicts are metered (65% signalized) vs LA's gap-acceptance fights (13%
signalized, 3.4 yield approaches/junction): "New York waits in line, LA
fights for gaps"; (2) Manhattan's short blocks + high portal density make it
a spillback amplifier — box-blocking/gridlock is the predicted failure mode,
a falsifiable sim prediction; (3) surprise: DTLA is MORE road-dense than
Manhattan (31.7 vs 19.3 lane-km/km²) thanks to the freeway stack. Caveats:
flat 600 veh/h/lane demand injects ~14× more vehicles/km² into Manhattan;
27–59% of edges are netconvert micro-fragments; zero stop-sign approaches
compile through (all minor control is yield).

### WQ-9: Intersection visualization + explanation layers
What an intersection IS in the sim (stop lines, conflict points, signal programs)
rendered/explained for the episode. Needs design thought; ADR-0003 (MapLibre-first,
no UI framework) governs.

### WQ-10: Benchmark queue items unblocked by the stress test — DONE (2026-07-24)
WQ-4/WQ-15 measurements fed into the Benchmark Queue in `gaps-and-roadmap.md`
(dated status block under the table): per-vehicle wire size (24 B/veh TSSF,
≥92 B/veh keyframe), keyframe wall ~10.9k + ADR-0015 resolution, ws delivery
1:1 @276 kB, MapLibre fleet-vs-static-load ceiling split, kernel Step slope,
memory footprint. Also corrected the ~15×-wrong "~7 ms/tick at 8k" figure in
freshness notes (b)/(g). Still-open rows listed in the status block.

## Done recently (for orientation, newest first)

- Signal zoom-gating + road width bump at z≤12 (`viz/src/main.ts`).
- Edge-group casing so same-road lanes read as one road (`91bfa51`).
- demosrv params API + viz model panel — controllers + sim params exposed (`bfcf982`).
- Three fixture-found engine bugs fixed: fragment gate-through, red clearance
  window, safe origin injection (`71461b9` + ADR-0010/0011 amendments `0f14d1f`).
- 14/14 demo-swap verification scripted and green (freshness note (d)).
- 10-network demo matrix imported and smoke-tested (freshness note (b)).
