# Synthesis: Scenario Format

> Researched: 2026-07-16 | Git HEAD: ae75fba | Status: complete
> Feeds a future ADR on the scenario format. This synthesis recommends; the ADR decides.

## Summary

A scenario is road network + demand + control configuration + metric
definitions, and — per the vision doc — it must be *diffable* so upgrade
variants are first-class. The survey shows every piece of this exists
somewhere, but **no system has all of it**: SUMO/MATSim prove the
manifest-of-parts decomposition and (SUMO meandata, MATSim scoring) prove
metrics belong inside the scenario; Aimsun proves the
scenario→experiment→replication hierarchy and named network variants
(geometry configurations); Vissim institutionalizes (scenario, seed) run
keys and 15-minute demand slicing; OpenSCENARIO proves what a scenario
format *without* demand or metrics looks like (and who pays for it — the
tools that adopt it); and Kubernetes/kustomize — a community that has
never talked to the traffic community — has spent a decade solving
exactly our differentiator: baseline + N diffable variants as data, not
copies, not templates. The recommendation set below is mostly assembly:
take SUMO's file decomposition, Aimsun's variant hierarchy, kustomize's
overlay mechanism and eschewed-features list, FHWA's seed-sweep
statistics, and Kubernetes' versioning rules — and write it in strict
YAML so git can see everything.

## Source Files

- [Mechanics: composition, demand primitives, metrics declarations, versioning](./implementation.md)
- [Prior art survey: SUMO, MATSim, Vissim, Aimsun, OpenSCENARIO/CARLA, CommonRoad, kustomize/CUE](./competitors.md)
- [Standards, formalisms, patterns, anti-patterns](./standards-and-patterns.md)

## Key Findings → Recommended Decisions (for the scenario-format ADR)

### 1. A scenario is a directory: one manifest referencing typed part files
**Choice:** `scenario.yaml` manifest (identity, engine parameters,
format version, seeds) referencing part files by relative path:
`network.*`, `demand/*.yaml`, `control/*.yaml`, `metrics/*.yaml`.
Ship a single-file "pack" export for sharing; the directory is the
authoring form.
**Why:** Four of five surveyed sims converge on manifest-of-parts
(SUMO `.sumocfg`, MATSim `config.xml`, Aimsun scenario object); it lets
variants share unchanged parts, keeps each file small enough to review,
and gives git per-file diffs. Vissim's monolithic `.inpx` (network +
demand + evaluation in one document) is the counter-example that shows
what monoliths cost diffability.
**Trade-off:** Two places to look instead of one; needs a pack/export
step for distribution and a content hash for identity (see #6).
**Field context:** [implementation §1](./implementation.md); the manifest
also carries engine parameters — ADR-0005 #7 already makes tick length a
scenario/config parameter, so it lives here (default 100 ms).

### 2. Authoring syntax: strict YAML — a schema-validated, anchor-free subset
**Choice:** YAML with: no anchors/aliases/custom tags, explicit quotes
where a scalar could be mistyped, one entity per document, a canonical
formatter (`scenario fmt`) so diffs are semantic. JSON Schema (or
equivalent) validation at load. TOML is the runner-up; CUE is the
documented upgrade path if cross-field constraints outgrow the schema.
**Why:** YAML is the only mainstream format with comments + clean deep
nesting + a decade of "config as reviewable code" tooling culture
(Kubernetes). Its traps are known and fenceable: `NO`→`false`,
`9.3`→float are *spec-compliant* implicit typing
([StrictYAML](https://hitchdev.com/strictyaml/why/implicit-typing-removed/)).
TOML's "obvious semantics" are real but deep tables get unwieldy
([toml.io](https://toml.io/en/)); XML (the field's default) is
XSD-validatable but hostile to hand-authoring and noisy in diffs.
**Trade-off:** YAML parsers differ across ecosystems; the canonical
formatter must be ours (Go) so formatting is stable. This is a
*recommendation*, not a certainty — if the ADR prefers TOML for its
simpler spec, the overlay/versioning decisions below are unaffected.
**Field context:** [implementation §10](./implementation.md).

### 3. Demand: layered primitives; OD and turning counts as compiled inputs
**Choice:** v1 demand file contains: **explicit vehicles** (scripted
demos), **flows** (rate per entry/OD pair with constant or Poisson
spacing; piecewise-constant time slices in sim seconds; vehicle-type mix
per flow with sampled distributions), and **turning-fraction blocks**
(input-section volumes + junction turn ratios). OD matrices are accepted
as *source files* compiled into flows/vehicles by a tool (od2trips-style)
with a recorded seed — the engine never sees a matrix.
**Why:** This is exactly the field's revealed structure: SUMO's
vehicle/trip/flow ladder with `period="exp(X)"` Poisson
([demand docs](https://sumo.dlr.de/docs/Definition_of_Vehicles%2C_Vehicle_Types%2C_and_Routes.html)),
od2trips compile-time expansion with random-in-cell departure draws
([od2trips](https://sumo.dlr.de/docs/Demand/Importing_O%2FD_Matrices.html)),
Aimsun Traffic States and Vissim inputs+routing-decisions for turning
counts ([Aimsun](https://docs.aimsun.com/next/24.0.3/UsersManual/DemandOverview.html),
[MDOT 15-min coding](https://www.michigan.gov/mdot/-/media/Project/Websites/MDOT/Programs/Research-Administration/Final-Reports/SPR-1689-Report.pdf)).
Piecewise-constant slices are universal — no analytic rate functions
anywhere. Times in **sim seconds**, never wall clock (ADR-0005).
**Trade-off:** Two-step authoring for OD users; the compile step needs
the same seeded-RNG discipline as the engine or demand generation
becomes a hidden nondeterminism source.
**Field context:** [implementation §2–§5](./implementation.md).

### 4. Variants are overlays, never copies, never templates
**Choice:** A variant is a directory: `variant.yaml` naming its base +
JSON-Merge-Patch-style patches on named entities + added part files.
`scenario build --variant roundabout` materializes a complete,
hashable scenario at load. Patches address entities **by stable ID**,
not by file position. v1 offers no removal directives (compose by
addition; split files instead) and **no templating/parameter
substitution of any kind**.
**Why:** kustomize's base/overlay→variant model is a decade of
production proof that this mechanism keeps N environments reviewable in
git ([glossary](https://kubectl.docs.kubernetes.io/references/kustomize/glossary/)).
Its eschewed-features list is written from scars: `${VAR}` templating
makes config "no longer data... logic that must be compiled", and
removal semantics invite inconsistency
([eschewed features](https://kubectl.docs.kubernetes.io/faq/kustomize/eschewedfeatures/)).
In traffic tooling nothing comparable exists — Aimsun's geometry
configurations (named alternative networks referenced by scenarios) are
the closest and are GUI-bound; SUMO/MATSim/Vissim practice is file
copies ([competitors](./competitors.md)).
**Trade-off:** Patching *topology* ("add a lane", "stop sign →
roundabout") is harder than patching parameters — it needs stable lane
IDs that survive network edits, which constrains
[[arch-road-graph-model]] and [[integration-osm-extraction]]. JSON Merge
Patch can't express "insert lane between X and Y"; v1 may need whole-
entity replacement for network changes.
**Field context:** [implementation §9](./implementation.md).

### 5. Metric definitions live inside the scenario as measurement sets
**Choice:** `metrics/*.yaml` declares measurement sets: which elements
(by stable ID or named selection), which metric types, over which
aggregation windows/periods — mirroring SUMO's meandata
(`<edgeData period begin end edges vTypes>`) and Vissim's evaluation
configuration. The metric *catalog* (delay, queue length, travel time,
LOS...) is owned by [[domain-congestion-metrics]] — researched
concurrently; this format only fixes the *binding mechanism*.
**Why:** Every serious tool embeds measurement in the project (SUMO
meandata, Vissim `.inpx` evaluations, Aimsun outputs-to-generate,
MATSim's scoring config inside `config.xml`); OpenSCENARIO's omission
forced CARLA to bolt criteria on as `criteria_*` StopTriggers
([scenario_runner](https://scenario-runner.readthedocs.io/en/latest/openscenario_support/)).
For math-vs-vibes, "same metric bindings on baseline and every variant"
must be *structural* — a variant that forgets a measurement is a broken
diff, and overlays (#4) make that structurally impossible when the
metrics file is unchanged.
**Trade-off:** The binding grammar is coupled to the metrics catalog's
final shape — a real dependency; the ADR should fix bindings generically
(element × metric-type × window) and let the catalog grow.
**Field context:** [implementation §8](./implementation.md).

### 6. Run identity = (scenario content hash, seed); seed sweeps are tooling
**Choice:** The materialized scenario (post-overlay, post-formatting)
hashes to a content ID; a run key is `(content-hash, seed)`. The
manifest carries a base seed; sweeping (`--seeds 10`) and pairwise
statistical comparison of variant runs are built-in commands, per FHWA's
alternatives-analysis process.
**Why:** (scenario, seed) is already the ADR-0005 run key and Vissim/DOT
practice (~10 seeds averaged; one DOT computed ≥10 runs for a 95% CI);
Aimsun's replication object (seed + outputs) is the same shape
([TAT III ch. 6](https://ops.fhwa.dot.gov/publications/fhwahop18036/chapter6.htm),
[Carolina Crossroads](https://www.scdotcarolinacrossroads.com/FEIS-documents/App_D_Alternatives_Traffic_Analysis_Technical_Memo_part_1.pdf),
[Aimsun](https://docs.aimsun.com/next/26.0.0/UsersManual/ScenariosExperimentsResultsReplications.html)).
Content hashing (not filename, not git SHA) is what makes "the same
scenario" well-defined across copies, renames, and packs — no surveyed
traffic tool has this; kustomize's everything-explicit-in-git discipline
implies it.
**Trade-off:** Hash stability requires the canonical formatter (#2) to be
bug-free; engine-version sensitivity belongs to the recording (#7), not
the scenario hash.
**Field context:** [standards §patterns](./standards-and-patterns.md).

### 7. A recording is a run artifact, not a scenario — but it can spawn one
**Choice:** A recording = manifest (scenario content hash, seed, engine
version, format version) + references to its JetStream streams
(keyframes, arbitrated intent log, CRCs — per ADR-0005). Recordings are
immutable and never edited into scenarios. The *only* sanctioned
scenario-from-recording path is explicit: export a keyframe as a new
scenario's initial state.
**Why:** SUMO's state save/load demonstrates the coupling trap — it only
guarantees correctness "if you use the same options (including all input
files!)" and can't fully restore unfinished flows
([sumo#7471](https://github.com/eclipse-sumo/sumo/issues/7471)); CARLA's
recorder shows the other pole (state log, no re-execution). Binding the
recording to a content hash (#6) instead of a file path removes the
whole class of "which net.xml did this run use?" ambiguity.
**Trade-off:** Initial-state export needs a scenario schema for
"vehicles already present at t=0" (positions, speeds, routes) — a new
demand primitive not in the surveyed formats.
**Field context:** [implementation §12](./implementation.md); replay
mechanics are [[arch-time-model]]/[[arch-nats-backbone]] scope.

### 8. Versioning: explicit `format_version`, migrate on load, round-trip rule
**Choice:** Every manifest and part file carries `format_version: N`
(integer). The loader supports N and N−1 minimum, migrating silently
with a warning; a `scenario migrate` command upgrades files in place.
Adopt Kubernetes' round-trip rule: save→load→save across adjacent
versions loses no information. Deprecation of a field requires one
release of warnings before removal.
**Why:** Vissim versions `.inpx` by root attribute and refuses unknown
newer attributes explicitly; Kubernetes formalizes the mature policy
(elements removed only via version bumps; beta deprecations ≥ 9
months/3 releases); CommonRoad runs two format epochs side by side with
one reader. MATSim's "parameters may vary from release to release"
silent drift is the anti-model
([k8s policy](https://kubernetes.io/docs/reference/using-api/deprecation-policy/),
[Vissim manual](https://pdfcoffee.com/manual-vissim-2022-4-pdf-free.html),
[CommonRoad io](https://cps.pages.gitlab.lrz.de/commonroad/commonroad-io/api/common.html),
[MATSim book ch. 2](https://ubiquitypress.com/chapters/33/files/4aae72a7-0714-42a7-b998-b22c1132537d.pdf)).
**Trade-off:** Migration code accrues from day one; N−2 and older means
chained migrations (acceptable at current cadence).
**Field context:** [implementation §11](./implementation.md).

## Compare/Contrast: Us vs the Field

| Dimension | SUMO | MATSim | Vissim | Aimsun | OpenSCENARIO | us (proposed) |
|---|---|---|---|---|---|---|
| Composition | manifest + parts | config + parts | `.inpx` monolith | `.ang` + objects | single `.xosc` + catalogs | **manifest + parts + overlays** |
| Syntax | XML/XSD | XML(+gz) | XML (GUI) | GUI doc ⚠ | XML/XSD | **strict YAML** |
| Demand | vehicles/trips/flows; OD & turns compiled | agent day plans | inputs + routing; OD via assignment | traffic states / OD | none (maneuvers) | **flows; OD & turns compiled** |
| Variants | copies + `--seed` | copies + dirs | copies; seed sweeps | experiments + geometry configs | parameterization | **first-class overlays** |
| Metrics in scenario | meandata sets | scoring config | evaluation config | outputs-to-generate | **none** | **measurement sets (declared)** |
| Run key / record | state XML | output dir | seed + results | replication (seed+outputs) | tool-specific | **(content-hash, seed) + JetStream manifest** |
| Versioning | implicit | silent drift | version attr | — | XSLT migrations | **format_version + round-trip** |

## The Genuine Gap (again)

The overlay mechanism and the traffic scenario have never met: the
kustomize/config-management literature solves diffable variants
rigorously but knows nothing about OD matrices, Poisson arrivals, or
seed sweeps; the traffic literature knows demand and evaluation deeply
but expresses variants as directory copies or GUI duplications, with
**no content-addressed scenario identity anywhere** — nothing binds a
recorded run to *exactly* the input that produced it except folklore
("keep the folder"). A scenario format with overlays + declarative
metrics + content hashing is, as far as this survey found, unpublished.
Second finding of the same kind as [[arch-time-model]]'s: another
engineering-blog-shaped hole in the literature.

## Open Questions

- ~~Network-variant patching granularity~~ **RESOLVED 2026-07-17 review,
  revised same-day after owner pushback:** variants are **authored delta
  patches** against a base import, consistent with the kustomize-style
  overlays used for demand/control — the patch document is the inspectable,
  reviewable artifact (in the advocacy use case the delta *is* the proposal).
  v1 patch grammar is small (add-lane, remove-lane, modify-lane-attrs,
  modify-connection, modify-junction-control), anchored by durable IDs from
  our layer, validated fail-loud at apply time; derived artifacts (conflict
  sets, internal lanes) always recompile from the patched source model. The
  network-diff tool remains as the **verification layer** (intent vs
  effective change), not the authoring format; whole-network replacement
  stays as the degenerate case. Fuzzy anchoring (name/shape selectors that
  survive re-import) is later work, gated on ID-stability evidence.
- ~~OD→flows compile step~~ **RESOLVED 2026-07-17 review:** a runtime demand
  director (elevated-grants client per the director decision) samples
  OD/arrival definitions at runtime and issues spawn verbs — recorded on the
  record plane, so replay never re-runs the sampler. RNG joins the ADR-0005
  seeded-stream discipline, keyed **per vehicle** (failover-invisible, per
  the fleet decision). The same sampler runs in an **offline mode** emitting
  a reviewable/diffable spawn table, preserving the build-time benefits
  (inspect demand, CRN-identical realizations across alternatives) without a
  static table as the runtime source of truth.
- Measurement-binding grammar: flat ID lists vs named selections vs
  spatial queries — constrained by the [[domain-congestion-metrics]]
  catalog (concurrent research).
- Signal-program representation referenced from `control/` — owned by
  [[domain-signal-control]]; the scenario format only fixes *where* it
  lives.
- Controller assignment syntax (which flows/vTypes get AI policy X vs a
  human slot) — depends on [[concept-vehicle-controller-interface]].
- Pack format (zip/tar/git-URL) and whether packs may reference *remote*
  part files (kustomize allows URL bases; do we?).
- Demand calibrated from real trajectories (e.g. NGSIM I-80 flows) as a
  first-class import path — ties to [[domain-trajectory-datasets]] and
  the repo's `analysis/ngsim` tooling.

## Connections to Other Topics

- **Decides into:** the future scenario-format ADR (this research is its gate).
- **Constrained by:** [[arch-time-model]] (ADR-0005: (scenario, seed) run
  key; tick length as scenario parameter; tick-count clock ⇒ demand times
  in sim seconds, no wall clock; replay artifact shape),
  [[arch-nats-backbone]] (recordings reference JetStream streams;
  contracts sacred per AGENTS.md).
- **Constrains:** [[arch-road-graph-model]] (stable element/lane IDs are
  a prerequisite for overlay patches and metric bindings),
  [[concept-vehicle-controller-interface]] (scenario must name controller
  assignments for flows/types), [[arch-state-authority]] (initial-state
  export from recording keyframes).
- **Depends on:** [[domain-congestion-metrics]] (metric catalog being
  defined concurrently — bindings are generic, catalog is theirs),
  [[domain-signal-control]] (signal-program schema),
  [[integration-osm-extraction]] (network part file provenance).
- **Relates to:** [[domain-trajectory-datasets]] (real-data demand
  import), [[domain-traffic-flow-models]] (vType = car-following
  parameters per [[arch-time-model]]'s IDM choice), [[arch-state-authority]]
  (seeded stream-per-concern RNG extends to the demand compiler),
  [[domain-simulator-landscape]] (tool positioning context).
