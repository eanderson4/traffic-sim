# Podcast Demo Work-Queue (agent-dispatchable)

> Working todo for the demo-matrix / trust-building program. Each item is written so a
> fresh agent can pick it up cold. Owner adds items at the bottom; strike or annotate
> when done. Podcast deadline is ~2 days out — prioritize engine trust over polish.
>
> Ground rules: `AGENTS.md` (read `docs/VISION.md`, KB-first, ADRs for consequential
> decisions). Every non-doc commit: run `scripts/external-review.sh` first and triage
> Fable's findings (it prints them even when Sol fails). Sol is quota-dead since
> 2026-07-23; the owner has authorized committing with `EXTERNAL_REVIEW_SKIP=1`
> after that Fable pass, recording Sol's failure count in the commit message, until
> Sol recovers. Do not skip the script itself — that would bypass Fable too. Never push.

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
  the lerp (cosmetic); (b) panel polls have no in-flight guard — a slow (3 s timeout)
  poll can overlap the next and overwrite newer state; (c) demosrv registry accepts
  `"kind"` as an input field and overwrites it — should be rejected by the strict
  fence or moved to an output DTO; (d) the panel's divergence warning shows
  crcErrors only — surface verbErrors too.
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


### WQ-1: Spurious/"extra" signal heads at junctions — FIXED (2026-07-23)
Root cause was per-link duplication, not fragment artifacts: the wire's link
index is one per SUMO connection (from-lane→to-lane), so wide multi-lane
approaches rendered a head per lane-movement (~25 heads at one junction).
Fix in `viz/src/signals.ts`: cluster bound links by (identical state column
across all phases) AND approach geometry (≤75 m to the running centroid,
entry bearing within 90° — opposing approaches at symmetric junctions must
never merge), one head per cluster, set back 3.5 m along the approach
bearing. Wilshire 3038 → 1404 heads, Manhattan 3256 → ~1.4k. Registration
delay fixed engine-side: TSSG catch-up republish moved from the keyframe
cadence (100 ticks = 10 s at 1×) to `signalCatchUpEvery` = 20 ticks
(`engine/natsio/run.go`; contract + ADR-0006 addendum updated). Residual,
deferred: mid-zoom (13–15) declutter at dense junction clusters (e.g. the
San Vicente diagonal) — consider count-badged single heads; greedy-mean
bearing drift noted by Fable (cosmetic, deterministic).

### WQ-2: Trailer jackknife physics
`viz/src/artic.ts` — trailers render perpendicular/detached. Root cause understood
(single-track angle law has a second stable equilibrium at θ_front ± π; a >90° hitch
upset from lane-hop teleports or curved-fragment spawns flips onto the jackknife
branch and never recovers). Fix directions in freshness note (f): clamp |Δθ| per
frame, re-anchor to θ_front on large wraps, smooth hitch on lane hops.

### WQ-3: Junction-interior squiggles at city zoom
`internal` lanes still draw when zoomed out (squiggle clutter at every junction).
Candidate: zoom-gate internal lanes to ≥13 like the signal heads. Quick win.

## Engine trust / architecture

### WQ-4: Big-simulation stress test (owner priority)
Prove the architecture holds at scale. Scripted elevated-demand run of stress-dtla
(14.9k lanes, 1202 signals; gridlocked at 1200 veh/h/lane with ~8k peak vehicles,
~7 ms/tick kernel cost — pacing-bound, note (b)) via demosrv. Quantify: kernel
ms/tick vs vehicle count curve, NATS/ws pipeline health, viz `updateData` behavior
at 5–10k vehicles, mean time-loss / VMT sanity. Watch for the known ADR-0008 §6
pause-gate deadlock on overload runs (resume requires demand ≤ spare capacity;
jammed runs wedge with zero CPU/log; workaround is raised `-capacity`; needs an
escape hatch or at least a loud log/metric).

### WQ-5: Driver-lag divergence (skip-guarded test)
`TestDifferentialLanedrop` wave-envelope assertion skip-guarded on a documented
signature; needs a driver-model fix. Details in the fixture test file.

### WQ-6: Roundabout circulation
Directive expiry + yield conservatism; ADR-level. Fixture: `engine/fixture_roundabout_test.go`
(Circulation SKIP-guarded). Not podcast-blocking.

### WQ-7: stopDone keyframe persistence (v4)
Deferred until recordings exist. Not podcast-blocking.

## Program items (bigger, parallelizable, post-podcast unless cheap)

### WQ-8: GIS network analysis (LA vs NY)
Compare imported networks (lane miles, junction density, signal density, fragment
rates, origin/exit counts) to see if GIS-type analysis yields anything useful for
the episode. `engine/netimport` + `data/networks/`; metrics kernel (M13) can emit
per-lane intervals. Research-flavored; agent-friendly.

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
