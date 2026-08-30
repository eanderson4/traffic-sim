# Signal-Timing Game — Recording Plan for 2026-08-29

> Written 2026-08-28. Goal: a playable signal-timing game on camera **tomorrow**,
> with the 3D hero view as the reveal surface and a v0 vehicle fleet in it.
> This is the one-day core loop + fallback ladder; the full public builds are
> phased afterwards (§6). Grounding: `docs/kb/raw/integration-3d-hero-viz/`,
> `docs/kb/raw/integration-3d-vehicle-fleet/`, `scripts/chicago/mkquiz.py`.

## 1. What "playable tomorrow" means

On camera:
1. The guest designs signal timing for the **bottleneck-town** arterial
   (`data/pods/bottleneck-town` — fictitious, signals are the only bottleneck;
   `retime-short` and `green-wave` arms already exist as reference designs).
2. We compile their timing into a scenario variant and run it **live in batch**
   (pod scale: 200 lanes — a 7,200-tick simrun is seconds-to-a-minute).
3. Score reveals vs baselines: the shipped fixed-time base, `retime-short`,
   `green-wave` (and, if it behaves, the ADR-0037 sigctl actuated controller).
4. The run is watched in the **3D hero view** — orbit camera, signal heads,
   class-shaped vehicles.

Game shape (camera-friendly escalation):
- **Round 1 — one junction.** Dials: cycle length + green split per movement
  (2-3 numbers). Skill = not starving the side street.
- **Round 2 — the full arterial.** Adds per-junction offsets. Skill = building
  a green wave (the pre-authored `green-wave` arm is the bar to beat).

Scoring (keep it sayable on camera): **mean time loss per vehicle** (seconds
lost per trip) and **trips completed**, from the metrics.json totals of a
fixed seed-42 demand program. Same network, same demand, same seed for every
design — say that sentence on camera, it's the fairness story. (Anti-degenerate
guards like worst-movement delay bounds are v2, §6.)

## 2. Pre-flight (first thing, before any new work)

- [ ] **Tree hygiene.** ADR-0039 (perimeter demand metering) is implemented but
      uncommitted in the working tree. Land it or stash it — tomorrow's work
      must branch from a stable tree. Verify `cd engine && go test ./...` green.
- [ ] **Pod sanity.** Run `simrun -scenario data/pods/bottleneck-town/base -seed 42`
      with `-metrics-out`; confirm metrics.json totals look as curated.
- [ ] **Bake sanity.** Run `engine/cmd/bake` on that run's recording and serve
      it via `scripts/serve-baked.py`; confirm the baked path renders in the
      viz app. (This is the hero view's data source — if bake has a pod
      surprise, we need to know at 9am, not at record time.)
- [ ] **Authoring retrace.** Find how `retime-short`/`green-wave` were authored
      (tlLogic edit → netconvert → netimport? `scripts/chicago/mknetvariant.py`?
      hand edits?). This path becomes Workstream A's automation target.
      **If retrace fails, the fallback ladder starts at rung (b).**

## 3. Workstream A — the game core (backend)

Dials → timing → engine → score. Host-operated for tomorrow (guest calls
numbers, host turns dials — no accounts, no public endpoint).

- **A1. Timing compiler.** Script: dial JSON (cycle, per-movement greens,
  offsets) → edited tlLogic → netconvert → netimport → variant scenario dir
  under `data/pods/bottleneck-town/variants/<name>/`. Automate exactly the
  retrace from pre-flight; do not invent a new path.
- **A2. Scoring runner.** Wrapper: `simrun -scenario <variant> -seed 42
  -metrics-out`, then pull mean time loss + completions from metrics.json into
  a one-line result card (JSON + printed). Pre-compute the three baselines
  through the SAME runner so the comparison is apples-to-apples.
- **A3. Minimal dial UI.** One self-contained HTML page (mkquiz precedent:
  no CDN, no build step, works on flaky wifi): sliders for cycle/splits/offsets
  → emits the dial JSON (host pastes to CLI, or a tiny localhost POST if time
  allows). After the run: shows the score + rank vs baselines.
- **A4. Run sheet integration.** Two rounds per §1, ~10 min each on camera.

## 4. Workstream B — 3D hero viz v0

Standalone three.js page; MapLibre app untouched. **Timeboxed hard**: boxes
that render beat pretty that doesn't.

- **B0. ADR-0003 addendum FIRST (30 min).** New dependency (`three`) + new
  renderer class needs the addendum per ADR-0003/AGENTS.md. Use the research's
  proposed boundary: 3D "hero track" for baked replay/quiz only; MapLibre
  remains the live/analytical renderer; note the "rung-2" naming fork
  (map-ladder rung 2 = deck.gl; call this the **hero track**, not rung 2).
- **B1. Page.** New vite entry `hero.html` (multi-page build precedent:
  index/demos). `three` via pnpm.
- **B2. Data.** Consume baked artifacts through the DOM-free decoders we
  already have: `viz/src/tsrb.ts` (poses: x/y/angle/class, 0.1 m quant),
  `viz/src/tssg.ts` (signal table → phase-at-tick math), network GeoJSON.
  Frames are a local metric frame, north-up: x→x, y→−z. **No projection,
  no basemap** (fictitious pod — nothing to miss).
- **B3. Scene v0.** Flat ground; lane polylines as ribbons; vehicles as
  class-scaled boxes (placeholder) upgraded to Workstream C models as they
  land; signal heads = emissive spheres on posts driven by the same
  phase-at-tick derivation `viz/src/signalhead.ts` uses.
- **B4. Camera + playback.** OrbitControls; 2-3 preset cinematic positions;
  tick-stepped playback (deterministic — frames are every 5 ticks from the
  bake) with speed control and pause. URL param: which baked run to load.
- **B5. Per-run bake.** After each game run, bake its recording and hand the
  hero page the new run id — "watch your design run" is the payoff moment.

## 5. Workstream C — vehicle fleet v0 (just enough to not be boxes)

- **C1.** 4-6 parametric low-poly vehicles **as code** (three.js primitives,
  or Blender-bpy → GLB if someone is fast with it): sedan, van/SUV, box truck,
  semi (tractor+trailer as two meshes, 4+8 m hitch per `viz/src/theme.ts:152-153`),
  bus. One shared 6-8 color palette pulled from `viz/src/theme.ts`. Dims
  anchored to engine classes (`engine/vehicle.go:33,40`: car 5×2, truck 12×2.5;
  bus 12×2.55 as a stand-in until the bus class exists).
- **C2.** Manifest JSON: class index → model + dims + anchor (origin under
  front bumper, forward +Z). This is the seam with B3.
- **Explicitly NOT tomorrow**: automaker-styled designs, the agent swarm,
  legal provenance manifest, QA automation (all §6). Generic shapes only —
  which is also the legal-safe posture by construction.

## 6. Fallback ladder (decide in this order, no heroics mid-recording)

- (a) **Full loop** — guest dials → live run → score → 3D replay. The plan.
- (b) **Authoring path broken** → guest picks among pre-authored arms
  (`retime-short`, `green-wave`, base) on the dial page; everything else same.
- (c) **3D broken** → 2D baked replay in the existing viz for the reveal.
- (d) **Everything broken** → the shipped `mkquiz.py` quiz page (camera-proven)
  and play the old format. Nothing lost on camera.

Pre-record checklist (morning of): baselines pre-computed and cached; one
end-to-end rehearsal run through the full loop; hero page loaded once on the
recording machine; quiz page open in a tab as the (d) fallback.

## 7. Post-recording phases (the real builds)

- **Phase 1 — public game infra.** Submission endpoint on the demosrv shape
  (HTTP, off NATS), server-side re-run only, leaderboard as static JSON,
  hidden holdout seed against overfitting, anti-degenerate scoring metric.
  Owes: an ADR (submission contract + scoring definition).
- **Phase 2 — hero viz real build.** Art-direction pass (lighting, shadows,
  palette discipline), deterministic camera-path format keyed to ticks,
  capture pipeline (raw-CDP `beginFrame` precedent in `viz/scripts/screenshot.mjs`),
  quiz/game integration, elevation when the L lands.
- **Phase 3 — fleet program.** Style bible, Blender-bpy agent swarm (models as
  code → review-gate diffable), provenance manifest (generic pastiche, no
  badges/names — the audited legal posture), gltf-validator + dims/tri-budget
  CI, contact-sheet consistency passes.
- Every code commit goes through the external-review gate; docs/KB commits
  pass ungated. ADRs owed along the way: ADR-0003 addendum (B0), game
  submission/scoring ADR, and the transit class-mask ADR when that arc starts.

## 8. Morning checklist (rehearsed end-to-end 2026-08-29 — these exact commands ran)

Rehearsal evidence: dial `/tmp/rehearsal-dial.json` → variant `rehearsal-wave`
→ **148.7 s/veh, 476 trips, rank 2 of 4** → bake `rehearsal42/9da93a51331f` →
hero junction view with all 6 fleet models live. (Numbers from the PRE-FIX
network — see the lane-fix note below; rehearsal-wave re-measures 201.6 on
the fixed network.)

Evening update (branch `show/beat-the-signal`): the unified page `play.html`
now does the whole loop in one step — dials → "Run my design" → score reveal
→ the guest's bake auto-loads in the 3D stage. Hero playback is interpolated
to display refresh (fractional ticks — no more 2 fps stepping), and the fleet
is 12 models (added pickup, minivan, coupe, step-van, school-bus, fire-truck).
Verified live 2026-08-29: fresh POST through the runner scored
integration-test at 160.3 s/veh, rank 3, baked and playable in 24 s. The CLI
flow below is what the page's backend runs; keep it as the fallback.

**Lane fix (2026-08-29 late evening, same branch):** the pod generator's
mirrored lane indexing (lane 0 at the centreline — right turns issued from
the middle lane, lefts from the kerb, right-turn × same-green-through
pass-throughs in the bake) is FIXED in `scripts/demos/bottleneck_town.py`:
lane 0 = kerb, right turns from a kerb pocket, same-phase internal paths
geometry-verified non-crossing, connector-south's inherent 2-phase crossings
now declare `foesCross`. Exhaustive forensics on the new bakes: zero
front-bumper interpenetrations (min separation 2.89 m), zero same-phase path
crossings. The recalibration is uniform across arms (the defective layout's
accidental dedicated left bay is gone): **the board is now green-wave
190.7 (433 trips) / rehearsal-wave 201.6 (425) / base 207.3 (424) /
retime-short 221.0 (410)** — ordering preserved, green-wave still rank 1.
New bakes: `bthero42/f7d90093b24e`, `rehearsal42/53861694079c` (play.html's
stage URL is pinned to the bthero hash — update it if bthero42 is ever
re-baked). Public deploy: https://show-beat-the-signal.phantomjam.pages.dev
(view-only there by design — `GET /api/timing/run` probes liveness; the
public bundle is static, so Run disables itself with a notice).

0. **Static server + runner** —
   `python3 scripts/serve-baked.py --baked data/baked --viz viz/dist --port 8790 --timing-runner`.
   (serve-baked.py's `Content-Encoding: br` is load-bearing — `python3 -m http.server` breaks replay.
   `--timing-runner` enables `POST /api/timing/run`; without it the server is pure static.
   `/tmp/tsim/{serve,bake}` rebuild themselves on demand if /tmp was wiped.)
1. **The one-step page** — `http://127.0.0.1:8790/play.html`. Guest calls
   numbers, host sets dials, "Run my design". ~20–25 s typical (cached design
   ~60 ms; up to ~6 min if the runner falls back to `-pace 10` on a loaded
   box — the page says so while it spins, keep talking). The reveal lands the
   score, rank of N, and animated bars vs the baselines (green-wave 190.7 /
   base 207.3 / retime-short 221.0 s/veh, seed 42); the 3D stage auto-loads
   the guest's fresh bake at 2× speed. "Base timing" shows bthero42 any time.
   Fairness sentence: *same network, same demand, same seed (42), 15,000
   ticks — only the light timing changes.* Caveat if asked: ±0.03 s/veh
   run-to-run jitter vs 20–70 s gaps between designs.
2. **What the page actually runs** — `POST /api/timing/run` with the dial JSON
   → mktimingvariant → `score-timing.py --dial <tmp> --name <name> --bake`
   (one serve run with recording → score row → bake → board rewrite → dist
   copy) → the page re-fetches the board and points the stage at
   `/hero.html?run=<slug>&view=junction`. One run at a time: a concurrent
   POST gets 409 ("another run is in progress") — wait, don't mash.
3. **CLI equivalent (fallback)** — `python3 scripts/show/score-timing.py
   --dial <file> --name <slug> --bake` prints `<slug>: mean time loss Xs/veh,
   Y trips (rank N of M)`, bakes, and rewrites the board. On a loaded box
   (OBS counts) it may void an unpaced attempt and retry, or fall back to
   `-pace 10` (~3 min) — it says so on the line; that's normal.
4. **Hero controls** — camera presets: 1 pod / 2 arterial / 3 junction, space
   play-pause, drag to orbit, `&t=<tick>&speed=4` URL params (fractional
   ticks render interpolated poses). Fleet garage if asked about the cars:
   `/fleet-review.html`. Classic dial-only page: `/signal-game.html`.
5. **Reset between guests** — leaderboard rows cache at
   `data/runs/timing-game/*.score.json`; delete a guest's cache (plus
   `data/pods/bottleneck-town/variants/<slug>/` and
   `data/baked/baked/<slug>/` for the disk) and re-run any score to
   regenerate the board, then `cp viz/public/timing-game-results.json viz/dist/`.
6. **Gotchas** — scoring goes through `serve`, not `simrun` (simrun rejects
   demand-part scenarios). hero.html reads pod GeoJSON bakes only (rejects
   city PMTiles loudly). `?run=<slug>` resolves the hash via dir listing;
   `?run=<slug>/<hash12>` pins it. The served board is `viz/dist/`'s copy —
   the runner endpoint refreshes it; manual CLI runs need the `cp` in step 5.
7. **Fallbacks** (ladder §6) — (b) pre-authored arms
   `data/pods/bottleneck-town/{base,retime-short,green-wave}` score the same
   way; (c) 2D replay: existing baked demo pages; (d) `/quiz.html` tab open.
