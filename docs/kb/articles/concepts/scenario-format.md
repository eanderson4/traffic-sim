# Scenario Format

> A scenario is a directory: a strict-YAML manifest referencing network, demand, control, and metrics parts, with kustomize-style overlay variants and (content-hash, seed) run identity.

## Overview

Per the [vision doc](../../../VISION.md), a scenario is road network + demand + control
configuration + metric definitions — and it must be *diffable*, because the headline use
case (math-vs-vibes) is "baseline + N upgrade variants, ranked by metrics". The scenario
format is therefore the project's unit of authorship, review, comparison, and replay: it
is what git diffs, what the engine loads, what seed sweeps run, and what recordings bind
to.

The survey of prior art (SUMO, MATSim, Vissim, Aimsun, OpenSCENARIO/CARLA, CommonRoad)
found every individual piece exists somewhere, but **no system has all of it**. SUMO/MATSim
prove the manifest-of-parts decomposition; Aimsun proves the scenario→experiment→replication
hierarchy and named network variants; Vissim institutionalizes (scenario, seed) run keys and
15-minute demand slicing; OpenSCENARIO proves what a format without demand or metrics costs
(CARLA had to bolt pass/fail criteria on as `criteria_*` StopTriggers). The differentiator —
diffable variants as data — comes from outside the field entirely: Kubernetes kustomize has
spent a decade solving exactly that problem, including a written-from-scars list of features
to refuse.

The recommendation set is mostly assembly: SUMO's file decomposition, kustomize's overlay
mechanism and eschewed-features list, FHWA's seed-sweep statistics, Kubernetes' versioning
rules, written in strict YAML so git can see everything. A content-addressed scenario
identity (hash of the materialized scenario) appears in **no** surveyed traffic tool —
overlays + declarative metrics + content hashing is, as far as the research found,
unpublished. No scenario-format ADR exists yet; this article records the recommended
positions, amended by the 2026-07-17 design review (which settled demand sampling and
network-variant patching via ADR-0008/ADR-0009).

## Key Components

| Component | Location | Purpose |
|---|---|---|
| `scenario.yaml` manifest | raw/concept-scenario-format/implementation.md §1 | Identity, engine params (tick length, default 100 ms per [ADR-0005](../../decisions/ADR-0005-time-model.md)), `format_version`, base seed; points at part files |
| Part files (`network.*`, `demand/`, `control/`, `metrics/`) | implementation.md §1, §7 | Typed content files referenced by relative path; the directory is the real unit |
| Demand primitives | implementation.md §2–§5 | Explicit vehicles, flows (constant / Poisson spacing, piecewise-constant slices), turning-fraction blocks; vType mix per flow |
| Runtime demand director | synthesis.md Open Questions (RESOLVED 2026-07-17); [ADR-0008](../../decisions/ADR-0008-controller-contract.md) | Elevated-grants director client samples OD/arrival definitions at runtime, issues spawn verbs recorded on the record plane |
| Measurement sets | implementation.md §8 | `metrics/*.yaml` declares element × metric-type × window bindings inside the scenario |
| Variant overlays | implementation.md §9; synthesis.md Open Questions (RESOLVED 2026-07-17); [ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md) | `variant.yaml` naming its base + patches addressed by durable ID + added part files; small network-patch grammar |
| Run key & content hash | standards-and-patterns.md §patterns; [ADR-0005](../../decisions/ADR-0005-time-model.md) | Materialized scenario hashes to a content ID; a run is `(content-hash, seed)` |
| Recording manifest | implementation.md §12; [ADR-0005](../../decisions/ADR-0005-time-model.md) | Immutable run artifact: scenario hash, seed, engine version, JetStream stream references |
| `format_version` & migrations | implementation.md §11 | Integer version per file; loader supports N and N−1; Kubernetes round-trip rule |

## How It Works

**1. Composition: manifest-of-parts, directory is the unit.** Four of five surveyed sims
converge on a small config document pointing at typed content files (SUMO `.sumocfg`,
MATSim `config.xml`, Aimsun scenario object); Vissim's monolithic `.inpx` (network + demand
+ evaluation in one document) is the counter-example that shows what monoliths cost
diffability. Parts let variants share unchanged files and give git per-file diffs. A
single-file "pack" export is the sharing form; the directory is the authoring form.

**2. Syntax: strict YAML.** No anchors/aliases/custom tags, explicit quotes where a scalar
could be mistyped, one entity per document, a canonical `scenario fmt` formatter (ours, in
Go) so diffs are semantic and hashing is stable. JSON-Schema validation at load. YAML is
the only mainstream format with comments + clean deep nesting + a decade of "config as
reviewable code" culture; its traps (`NO`→`false`, `9.3`→float) are spec-compliant implicit
typing and must be fenced. TOML is the runner-up, CUE the documented upgrade path if
cross-field constraints outgrow the schema. This one is a *recommendation* awaiting the ADR;
the overlay/versioning decisions are syntax-independent.

**3. Demand: layered primitives, sim-second times, runtime sampling (review-resolved).**
The v1 demand vocabulary mirrors the field's revealed structure: explicit vehicles (scripted
demos), flows with constant or Poisson spacing (SUMO's `period="exp(X)"` gives exponentially
distributed gaps), piecewise-constant time slices in **sim seconds** (never wall clock —
[ADR-0005](../../decisions/ADR-0005-time-model.md)), vehicle-type mix per flow, and
turning-fraction blocks (input-section volumes + junction turn ratios — Aimsun Traffic
States, Vissim inputs+routing coded in 15-minute increments per MDOT guidance). No analytic
rate functions exist anywhere in the field. The 2026-07-17 review **replaced** the
od2trips-style build-time compile step: a **runtime demand director** — an elevated-grants
director client per [ADR-0008](../../decisions/ADR-0008-controller-contract.md) — samples
OD/arrival definitions at runtime and issues spawn verbs, which are recorded on the record
plane ([ADR-0006](../../decisions/ADR-0006-nats-message-contract.md)), so replay never
re-runs the sampler. RNG joins the ADR-0005 seeded-stream discipline keyed **per vehicle**
(failover-invisible, per the default-driver fleet decision). The same sampler runs in an
offline mode emitting a reviewable, diffable spawn table — preserving CRN-identical
realizations across alternatives without a static table as the runtime source of truth.

**4. Variants are overlays — never copies, never templates.** A variant is a directory:
`variant.yaml` naming its base + JSON-Merge-Patch-style patches on named entities + added
part files, materialized into a complete hashable scenario at load. Kustomize's
eschewed-features list is adopted as design constraints: **no templating/parameter
substitution of any kind** ("It's no longer data, it's now logic that must be compiled"),
v1 composes by addition only (split files instead of removal directives). For network
topology the 2026-07-17 review resolved the granularity question: variants are **authored
delta patches** against a base import — in the advocacy use case the delta *is* the
proposal. The v1 patch grammar is small (add-lane, remove-lane, modify-lane-attrs,
modify-connection, modify-junction-control), anchored by durable IDs from our own layer
([ADR-0009](../../decisions/ADR-0009-osm-import-strategy.md)), validated fail-loud at apply
time; derived artifacts (conflict sets, internal lanes) always recompile from the patched
source model. The network-diff tool remains as the *verification* layer (intent vs effective
change), not the authoring format; whole-network replacement stays as the degenerate case.
Fuzzy anchoring (name/shape selectors that survive re-import) is later work, gated on
ID-stability evidence.

**5. Metrics live inside the scenario as measurement sets.** `metrics/*.yaml` declares
which elements (by stable ID or named selection), which metric types, over which
aggregation windows — mirroring SUMO meandata (`<edgeData period begin end edges vTypes>`)
and Vissim's in-file evaluation configuration. Every serious tool embeds measurement in the
project; OpenSCENARIO's omission is the negative control. For math-vs-vibes this is
structural: a variant that forgets a measurement is a broken diff, and unchanged metrics
parts make that structurally impossible under overlays. The metric *catalog* is owned by
congestion-metrics research; this format fixes only the generic binding mechanism
(element × metric-type × window).

**6. Run identity = (content hash, seed); sweeps are tooling.** The materialized
post-overlay, post-format scenario hashes to a content ID; a run key is
`(content-hash, seed)` — already the ADR-0005 run key and Vissim/DOT practice (~10 seeds
averaged; one DOT computed ≥10 runs for a 95% CI; Aimsun's worked example runs 20 seeded
replications). Content hashing — not filename, not git SHA — is what makes "the same
scenario" well-defined across copies, renames, and packs; no surveyed traffic tool has it.
`--seeds 10` sweeps and pairwise statistical comparison of variant runs are built-in
commands, per FHWA's alternatives-analysis process (microsimulation is stochastic; single
runs are "not representative").

**7. A recording is a run artifact, not a scenario — but it can spawn one.** Recording =
manifest (scenario content hash, seed, engine version, format version) + references to its
JetStream streams (keyframes, arbitrated intent log, CRCs per ADR-0005). Recordings are
immutable, never edited into scenarios. The only sanctioned scenario-from-recording path is
explicit: export a keyframe as a new scenario's initial state — which needs a "vehicles
already present at t=0" demand primitive (positions, speeds, routes) not found in any
surveyed format.

**8. Versioning: explicit `format_version`, migrate on load, round-trip rule.** Every file
carries an integer `format_version`; the loader supports N and N−1, migrating silently with
a warning; `scenario migrate` upgrades in place. Kubernetes' round-trip rule applies:
save→load→save across adjacent versions loses no information; field deprecation requires
one release of warnings before removal (their beta window is 9 months / 3 releases).
MATSim's silent parameter drift is the anti-model; Vissim's explicit refusal of unknown
newer attributes and OpenSCENARIO's shipped XSLT migrations are the positive precedents.

## Gotchas

- **YAML implicit typing is spec-compliant, not a bug**: unquoted `NO` parses as `false`
  (the Norway problem), `9.3` as float — the strict subset (quotes, no anchors) and
  canonical formatter exist precisely to fence this.
- **Templating destroys reviewability**: `${VAR}` substitution turns data into "logic that
  must be compiled; errors in the output are disconnected from the edit that caused it"
  (kustomize). CARLA's Python-defined scenarios are the same trap in disguise — our escape
  hatch is controllers over NATS, so the format never needs one.
- **Copy-paste variants drift apart**: the field default (SUMO/MATSim/Vissim) is N
  near-identical directories; FHWA's "sub-variants" are a process rule, not a mechanism.
  Overlays exist because this fails at scale.
- **Compressed primary sources can't be reviewed**: MATSim's transparent `.xml.gz` habit
  makes demand files ungreppable and undiffable. Primary sources stay plain text;
  compression is a pack/transport concern only.
- **Streaming-order footguns**: SUMO requires route files sorted by departure time (it
  streams in 200 s windows) and can infinite-loop on unsorted input with persons. Our
  loader must sort or validate, never assume ordering.
- **Wall-clock timestamps as sim semantics** conflict with ADR-0005 (tick count is the
  clock): demand times are sim seconds; "7:30 AM" is a presentation alias for t=27000,
  nothing more.
- **Metrics as post-hoc scripts** break apples-to-apples comparison: if the measurement
  isn't in the scenario, two variants can't be compared — OpenSCENARIO forced CARLA's
  `criteria_*` bolt-on by omitting them.
- **State snapshots are not self-contained**: SUMO's save/load only guarantees correctness
  "if you use the same options (including all input files!)" and can't fully restore
  unfinished flows — why recordings bind a content hash, not a file path.
- **Hidden nondeterminism in demand generation**: any sampled departure/OD draw needs the
  same seeded-stream discipline as the engine (per-vehicle keyed, ADR-0005/ADR-0007) or
  demand generation silently defeats replay.

## Open Questions

- **Measurement-binding grammar**: flat ID lists vs named selections vs spatial queries —
  constrained by the congestion-metrics catalog being defined concurrently.
- **Signal-program representation** referenced from `control/`: owned by signal-control
  research; the scenario format only fixes *where* it lives.
- **Controller assignment syntax** (which flows/vTypes get AI policy X vs a human slot) —
  depends on the vehicle/controller interface contract.
- **Pack format**: zip/tar/git-URL, and whether packs may reference *remote* part files
  (kustomize allows URL bases; undecided for us).
- **Real-data demand import**: demand calibrated from trajectory datasets (e.g. NGSIM I-80
  flows) as a first-class path, tying to `analysis/ngsim` tooling.
- **Scenario-from-recording schema**: the "vehicles present at t=0" initial-state primitive
  needed for keyframe export.
- The scenario-format **ADR itself** is still unwritten — this research is its gate; the
  strict-YAML choice in particular is a recommendation, not a decision.

## Related

- [Time Model](../architecture/time-model.md) — ADR-0005 supplies the (scenario, seed) run key, the tick-count clock (demand times in sim seconds), and the replay artifact shape recordings reference.
- [State Authority](../architecture/state-authority.md) — seeded stream-per-concern RNG extends to the demand director; keyframes are the scenario-from-recording source.
- [Vehicle & Controller Interface](../concepts/vehicle-controller-interface.md) — the demand director is an elevated-grants director client (ADR-0008); scenarios must name controller assignments for flows/types.
- [Congestion Metrics](../business-domains/congestion-metrics.md) — owns the metric catalog that measurement sets bind to; this format fixes only the binding mechanism.
- [OSM Extraction](../integrations/osm-extraction.md) — produces the network part file and the durable IDs that overlay patches and metric bindings anchor to (ADR-0009).
- [Signal Control](../business-domains/signal-control.md) — owns the signal-program schema referenced from `control/`.

---
*Raw research: [raw/concept-scenario-format/](../../raw/concept-scenario-format/synthesis.md) (plus implementation.md, competitors.md, standards-and-patterns.md in the same directory)*
