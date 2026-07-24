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

## Operational state (2026-07-23 evening)

- Engine + all fixtures green at `0f14d1f` (see freshness notes (c)–(g) in
  `gaps-and-roadmap.md` for the full found/fixed bug history).
- demosrv serves the demo menu + built viz on http://localhost:8900/.
  Rebuild/restart: `cd engine && go build -o /tmp/demosrv ./cmd/demosrv && /tmp/demosrv`
  (run from repo root; it serves `viz/dist`, so `cd viz && pnpm build` first for viz changes).
- Headless verification: `cd viz && node scripts/screenshot.mjs "<url>" /tmp/x.png <waitMs>`;
  scripted all-demo verification lived at `/tmp/verify-demos.mjs` (NOT committed — gone
  on reboot; regenerate from the recipe in freshness note (d) if needed).
- Demo registry: `data/scenarios/demos.json` (10 imported networks + 4 behavior fixtures).

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

### WQ-3: Junction-interior squiggles at city zoom
`internal` lanes still draw when zoomed out (squiggle clutter at every junction).
Candidate: zoom-gate internal lanes to ≥13 like the signal heads. Quick win.

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

### WQ-11: Keyframe chunking — DONE (2026-07-24, ADR-0015)
Keyframes > `KeyframeChunkMax` (default 768 KiB) are split into consecutive
`ts.{run}.log.keyframe` messages with a `kf_chunk: "i/n"` header; no header =
whole keyframe (old recordings unchanged); the seek anchor is the keyframe's
LAST message so re-sim resumes at seq+1. `findKeyframe` reassembles + fails
loud on malformed groups; `indexLogMsgs` counts one entry per keyframe.
SchemaVersion stays 2. Tests: `engine/natsio/keyframe_chunk_test.go`
(round-trip CRC-verified replay through chunked keyframes; malformed group
errors). Remaining validation: re-run the WQ-4 stress scenario past 10.9k
vehicles.

### WQ-12: Replay materialization footprint vs demosrv readiness timeout
Found wiring the podcast recordings: the replay child materializes a
recording in memory before listening — a 36000-tick i280 recording (3.3 GB)
took ~40 s and ~23 GB RSS, so demosrv's 10 s child-readiness timeout
(`supervisor.go` readyTimeout) tears it down ("did not become ready"). A
9000-tick recording (369 MB) starts in ~5 s at ~2.7 GB RSS — podcast demo
cards therefore point at 15-minute recordings. Real fixes: stream the
materialization, or make readyTimeout generous/configurable (supervisor.go
was mid-refactor by the theme session when found).

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

### WQ-6: Roundabout circulation
Directive expiry + yield conservatism; ADR-level. Fixture: `engine/fixture_roundabout_test.go`
(Circulation SKIP-guarded). Not podcast-blocking.

### WQ-7: stopDone keyframe persistence (v4)
Deferred until recordings exist. Not podcast-blocking.

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

### WQ-10: Benchmark queue items unblocked by the stress test
Feed WQ-4 measurements into the Benchmark Queue table in `gaps-and-roadmap.md`
(per-vehicle wire size at 10k vehicles, nats.ws throughput, MapLibre updateData
fleet ceiling, GC jitter in paced loops).

## Done recently (for orientation, newest first)

- Signal zoom-gating + road width bump at z≤12 (`viz/src/main.ts`).
- Edge-group casing so same-road lanes read as one road (`91bfa51`).
- demosrv params API + viz model panel — controllers + sim params exposed (`bfcf982`).
- Three fixture-found engine bugs fixed: fragment gate-through, red clearance
  window, safe origin injection (`71461b9` + ADR-0010/0011 amendments `0f14d1f`).
- 14/14 demo-swap verification scripted and green (freshness note (d)).
- 10-network demo matrix imported and smoke-tested (freshness note (b)).
