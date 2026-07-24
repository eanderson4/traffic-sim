# External review brief — chicago-metro milestone

You are reviewing a milestone in the repo at `~/traffic-sim`.
You start with zero context; this brief is self-contained. REVIEW ONLY — do
not modify any files.

## Repo context (one paragraph)

This is a deterministic traffic microsimulation. The engine core is Go
(stdlib-first; the only approved deps are the two official NATS modules in
`engine/natsio/` and `gopkg.in/yaml.v3` in `engine/scenario/`). All
inter-service communication flows over NATS — message subjects and payloads
are public contracts, and changing them requires an ADR (see
`contracts/asyncapi.yaml`, `contracts/network-format-v1.md`). The engine is
authoritative over world state; controllers (drivers, signal programs,
directors) are external NATS clients that emit intents. Determinism is a hard
requirement (ADR-0005): fixed tick loop, no map-iteration-order dependence, no
wall clock in sim logic, seeded RNG. Visualization is MapLibre-first vanilla
TypeScript, no frameworks. Development is ADR-driven; decisions of consequence
get ADRs in `docs/kb/decisions/`. Read `AGENTS.md` for the full rules.

## Scope under review

The `chicago-metro` milestone: tooling to import zoned sub-networks of the
Chicago metro from a Geofabrik OSM PBF clip, generate demand estimates, run
them as demos, and render geographic overlays (zone regions, municipal
boundaries, water) in the viz. Five branch commits plus the merge into main:

```
fa2acef Add Chicago zone import + demand tooling (extract, boundaries, portals, mkdemand, scorecard)
db33fcf Add driver exit routing, serve attach barrier, batch-mode liveness fix
0e55f3e Render zone + admin-boundary overlays in the viz
18ad872 KB: chicago-metro article — zoned pipeline, demand method, tuning scorecard
1512ef3 Add water overlay: extract from PBF, render fill under roads
d4673f8 Merge branch 'chicago-metro'   (conflict resolutions in driver.go, demosrv/main.go, viz theme.ts + main.ts)
```

**Review exactly this diff:** `git diff 651666a..d4673f8`
(651666a is the pre-merge main tip = first parent of the merge; 28 files,
~2041 insertions). Commits before 651666a on main were already reviewed in
earlier rounds — do not re-review them.

**WARNING — dirty working tree:** the checkout contains UNCOMMITTED,
unrelated work-in-progress by another contributor (staged-diff WIP touching
`engine/cmd/demosrv/*`, `engine/geojson.go`, `engine/natsio/driver/driver.go`,
`viz/src/*`, `viz/*.{html,md}`). If you Read a file that `git status` shows as
modified, you are seeing that WIP, not the milestone under review. For any
such file use `git show d4673f8:<path>` to see the committed state, and cite
the committed state only.

## Read first (in order)

1. `AGENTS.md` (rules + invariants) and `docs/kb/articles/chicago-metro.md`
   (the design intent for this milestone — zoned pipeline, demand method,
   tuning scorecard).
2. `git diff 651666a..d4673f8` — the full diff.
3. Then the substantive pieces:
   - `scripts/chicago/{extract,boundaries,mkdemand,scorecard,water}.py` +
     `scripts/chicago/zones.geojson` — offline zone pipeline (Python tooling,
     not sim-runtime, so determinism rules apply only where outputs feed the
     engine).
   - `engine/cmd/portals/main.go` — new CLI.
   - `engine/cmd/serve/main.go` + `main_test.go` — attach barrier and
     batch-mode liveness changes.
   - `engine/natsio/{contract.go,run.go,demand/ready.go}` and
     `engine/natsio/driver/{driver.go,destinations.go,destinations_test.go}` +
     `engine/natsio/startgate_test.go` — contract/driver changes. NOTE: any
     change to NATS subjects or payload schemas is contract-level; check
     whether `contracts/asyncapi.yaml` was updated to match (it is NOT in the
     diff — decide whether it should have been).
   - `viz/src/{overlays,theme,config,main}.ts` + `viz/test/overlays.test.ts` —
     overlay rendering.

## Review focus, in priority order

1. **Correctness bugs.** Real ones, with a concrete failure scenario.
2. **Determinism risks** (ADR-0005): map iteration order leaking into sim
   output, non-associative float accumulation order, wall-clock dependence,
   nondeterministic RNG, anything that would break replay/checksums. The
   `serve` attach-barrier and batch-mode liveness changes deserve special
   scrutiny here — timing/liveness code is where determinism usually leaks.
3. **Contract/consistency issues.** NATS subjects/payloads are sacred. If the
   milestone added or changed a subject, field, or payload semantics, flag any
   mismatch with `contracts/asyncapi.yaml` / `contracts/network-format-v1.md`.
4. **Design weaknesses** relative to the milestone's own stated goals in
   `docs/kb/articles/chicago-metro.md`.
5. **Under-specified things that will bite at the NEXT milestone** (recorded
   deferrals are fine; invisible ones are not).

Calibration: this project explicitly rejects hardening against hypotheticals
and speculative generality. "Too early to need this" is a valid state. Only
flag things that are wrong TODAY or will predictably bite at a milestone
already on the roadmap. One round, blockers matter most.

## Output format

Findings ranked **blocker / should-fix / nit / question**, each cited
`file:line` (against committed state at d4673f8) with a one-paragraph
explanation and, for blockers, a concrete failure scenario. End with a short
verdict section. Do not modify files.
