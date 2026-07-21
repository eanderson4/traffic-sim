# ADR-0012: Scenario format (manifest-of-parts directory, strict YAML, overlay variants, content-hash run identity)

- **Status:** ACCEPTED (design ratified; implementation is the next milestone)
- **Date:** 2026-07-21 (ratifying `concept-scenario-format` research, gate closed
  2026-07-17; demand-sampling and network-variant questions resolved by the
  2026-07-17 design review and implemented as M10 / ADR-0009)

## Context

Through M10 the engine's configuration surface is command-line flags: `serve`
and `simrun` take `-netfile -rate -density -types -seed -ticks`, and the M10
demand director reads a strict-JSON flow file (`cmd/demand-director/*.json`).
That was right for bring-up, but the founding use case ([VISION.md](../../VISION.md))
is *"baseline + N upgrade variants, ranked by metrics"* — and flags cannot be
diffed, overlaid, content-addressed, or bound to a recording. The scenario is
the project's unit of authorship, review, comparison, and replay: it is what
git diffs, what the engine loads, what seed sweeps run, and what recordings
bind to.

The research (raw/concept-scenario-format, distilled to
[articles/concepts/scenario-format.md](../articles/concepts/scenario-format.md))
surveyed SUMO, MATSim, Vissim, Aimsun, OpenSCENARIO/CARLA, and CommonRoad:
every individual piece exists somewhere, no system has all of it. The findings
this ADR ratifies: manifest-of-parts is the field norm (Vissim's monolith is
the negative control); time-varying demand is universally piecewise-constant
slices, never analytic functions; measurement declarations belong *inside* the
scenario (OpenSCENARIO's omission forced CARLA's `criteria_*` bolt-on);
copy-paste is the field's only variant mechanism and fails at scale — the
overlay mechanism comes from kustomize, including its written-from-scars list
of features to refuse. Content-addressed scenario identity appears in no
surveyed traffic tool.

Two context questions the article left open were resolved before this ADR:
demand is sampled at runtime by an elevated-grants director client (2026-07-17
review → ADR-0008 §5 → implemented M10, ADR-0006 addendum), and network
variants are authored delta patches against a base import anchored by durable
IDs (ADR-0009). This ADR fixes the container those decisions live in.

## Decision

1. **A scenario is a directory; the manifest points at typed part files.** A
   `scenario.yaml` manifest carries identity, `format_version`, engine params
   (tick length — default 100 ms per ADR-0005; horizon; base seed), and
   relative-path references to part files: one `network.*` (network format v1
   per contracts/network-format-v1.md), `demand/*.yaml`, `control/*.yaml`,
   `metrics/*.yaml`. The directory — never the manifest alone — is the unit of
   sharing, diffing, and hashing; parts let variants share unchanged files and
   give git per-file diffs. A single-file "pack" export is the sharing form
   and is deferred (see NOT-decided).

2. **Syntax is strict YAML, with a canonical formatter and schema validation.**
   The strict subset: no anchors/aliases/custom tags, explicit quotes where a
   scalar could be mistyped (the Norway problem is spec-compliant implicit
   typing, and it must be fenced), one entity per document. A canonical
   `scenario fmt` formatter (ours, in Go) makes diffs semantic and the content
   hash stable; JSON-Schema validation at load, fail-loud. YAML is the only
   mainstream format with comments + clean deep nesting + a decade of
   config-as-reviewable-code culture; TOML is the runner-up and CUE the
   documented upgrade path if cross-field constraints outgrow the schema.
   **Dependency note (AGENTS.md conventions):** a YAML library is a new Go
   dependency; per the ADR-0006 precedent it is an approved exception confined
   to the scenario package (`engine/scenario/`), and the kernel stays
   stdlib-only — the engine core consumes the *loaded* model, never YAML.

3. **Demand is layered primitives in sim seconds, sampled at runtime by the
   director.** The v1 vocabulary: explicit vehicles (scripted demos); flows
   with constant or Poisson spacing (`period="exp(X)"` semantics — Poisson
   gaps, not Bernoulli coin flips); piecewise-constant rate slices in **sim
   seconds** (never wall clock — ADR-0005; "7:30 AM" is a presentation alias
   for t=27000, nothing more); per-flow vehicle-type mix; turning-fraction
   blocks (input-section volumes + junction turn ratios). No OD-matrix
   compile step, no analytic rate functions. The runtime path is the M10
   contract exactly as built: a demand file IS a director configuration — the
   demand director samples it with per-vehicle keyed RNG
   (`DeriveStream(seed, flowKey^ordinal)`) and issues spawn verbs recorded on
   `ts.{run}.log.verb`, so replay never re-runs the sampler. The same sampler
   gains an offline mode emitting a reviewable, diffable spawn table —
   preserving CRN-identical realizations across alternatives without a static
   table as the runtime source of truth. The M10 strict-JSON flow file
   (`cmd/demand-director`) is the seed of the demand schema and migrates to
   strict YAML. The loader must sort or validate explicit-vehicle orderings,
   never assume them (SUMO's unsorted-route-file footgun).

4. **Variants are overlays — never copies, never templates.** A variant is a
   directory: `variant.yaml` naming its base + JSON-Merge-Patch-style patches
   addressed to named entities by durable ID + added part files, materialized
   into a complete hashable scenario at load. Kustomize's eschewed-features
   list is adopted as permanent design constraints: **no templating or
   parameter substitution of any kind** ("it's no longer data, it's logic that
   must be compiled"); **v1 composes by addition only** — split files instead
   of removal directives (network topology excepted, next sentence). For
   network topology the variant carries the ADR-0009 authored delta patch: the
   v1 grammar is add-lane, remove-lane, modify-lane-attrs, modify-connection,
   modify-junction-control, anchored by our own durable IDs, validated
   fail-loud at apply time, with derived artifacts (conflict sets, internal
   lanes, signal bindings) always recompiled from the patched source model.
   Whole-network replacement remains as the degenerate case. Fuzzy anchoring
   (name/shape selectors surviving re-import) is later work, gated on
   ID-stability evidence (ADR-0009 consequences). The network-diff tool stays
   the *verification* layer (intent vs effective change), not the authoring
   format.

5. **Metrics live inside the scenario as measurement sets.** `metrics/*.yaml`
   declares element × metric-type × window bindings — which elements (v1:
   flat lists of stable IDs), which metric types, over which aggregation
   windows (SUMO meandata's shape: period, begin/end, filters). A variant that
   forgets a measurement is a broken diff, and unchanged metrics parts make
   that structurally impossible under overlays — for the alternatives-ranking
   use case this is the point of the format. The metric *catalog* is owned by
   the congestion-metrics research (and its own pending ADR); this format
   fixes only the generic binding mechanism.

6. **Run identity = (content-hash, seed); sweeps are tooling.** The
   materialized post-overlay, post-`fmt` scenario hashes to a content ID; a
   run key is `(content-hash, seed)` — the ADR-0005 run key made computable.
   Content hashing — not filename, not git SHA — is what makes "the same
   scenario" well-defined across copies, renames, and packs. The hash covers
   exactly the bytes the loader consumes (canonical formatting is what makes
   this well-defined). Seed sweeps (`--seeds N`) and pairwise statistical
   comparison of variant runs are built-in tooling, per FHWA's
   alternatives-analysis process (microsimulation is stochastic; single runs
   are "not representative"; ~10 seeds is DOT practice) — the format decision
   is only that sweep identity is derived, never authored.

7. **A recording is a run artifact that binds the scenario hash — and can
   spawn a scenario only explicitly.** A recording's manifest carries scenario
   content hash, seed, engine version, `format_version`, and its JetStream
   stream references (keyframes, arbitrated intent log, verb log, CRCs per
   ADR-0005/ADR-0006). Recordings are immutable and never edited into
   scenarios (SUMO's not-self-contained state files are the warning). The only
   sanctioned scenario-from-recording path — exporting a keyframe as a new
   scenario's initial state — needs a "vehicles already present at t=0"
   demand primitive (positions, speeds, routes) that no surveyed format has;
   that schema is deferred, not designed here.

8. **Versioning: explicit `format_version` per file, migrate on load,
   round-trip rule.** The loader supports N and N−1, migrating silently with a
   warning; `scenario migrate` upgrades in place. The Kubernetes round-trip
   rule applies: save→load→save across adjacent versions loses no information;
   field deprecation requires one release of warnings before removal.
   MATSim's silent parameter drift is the anti-model; Vissim's explicit
   refusal of unknown newer attributes and OpenSCENARIO's shipped migrations
   are the positive precedents. Unknown `format_version` newer than the loader
   is a hard error, never a partial read.

## Explicitly NOT decided (deferred, with owners)

- **Pack format** (zip/tar/git-URL; whether packs may reference remote parts —
  kustomize allows URL bases; undecided for us).
- **Controller-assignment syntax** (which flows/vTypes get AI policy X vs a
  human slot) — depends on the vehicle/controller interface contract;
  scenarios must eventually name these, v1 does not.
- **Signal-program schema** — owned by signal-control research (and the future
  external-signal-controller ADR, the ADR-0011 D1 seam); this format fixes
  only that programs are referenced from `control/`.
- **Measurement-binding grammar beyond flat ID lists** (named selections,
  spatial queries) — constrained by the congestion-metrics catalog being
  defined concurrently.
- **Real-data demand import** (NGSIM-calibrated demand as a first-class path,
  tying to `analysis/ngsim` tooling) and the **scenario-from-recording
  initial-state primitive** (§7).
- **Network-model ADR** (lane-as-atom schema, compiled conflict sets) remains
  its own pending decision; this ADR references network format v1 as-is.
- Revisit when: the first overlay variant is authored against a real advocacy
  corridor; the observability ADR lands (measurement bindings concretize);
  re-import/edit carry-over evidence arrives (fuzzy anchoring); the first
  multiplayer/human-driver scenario needs controller assignment.

## Consequences

- **M11 shape:** an `engine/scenario` package (loader, strict-YAML fence,
  canonical formatter, schema validation, content hash) plus `scenario fmt /
  validate / migrate` tooling; `serve`/`simrun` gain `-scenario dir` and the
  demand flags (`-rate -density -types`) become the generated-default scenario
  they already are conceptually. The M10 `cmd/demand-director` flow file moves
  to `demand/*.yaml` — the director's flag surface was always a placeholder
  for this (M10 KB note: "the scenario format ADR is what remains to make
  demand definitions files instead of flags").
- **Run registry gains the content hash** (`ts.{run}.meta` spec entry records
  `(content-hash, seed)`), making two runs of "the same scenario" comparable
  across machines and checkouts. This is additive to the ADR-0006 meta plane;
  no subject or payload-shape change to existing channels.
- **Nothing on the NATS contract changes.** Demand verbs, the record plane,
  and keyframes are untouched — this ADR fixes files, not messages. The M10
  record/replay bit-identity guarantees carry over verbatim: a scenario-driven
  run and a flag-driven run with identical spawned vehicles are the same run.
- **The pending-ADR queue shrinks to three** (network model,
  observability/metric set, license). The observability ADR is the natural
  successor: measurement sets give it a home and it concretizes the §5 binding
  grammar.
- **Dependency exception:** one YAML library, confined to `engine/scenario/`,
  justified above (§2). Justification lives in that package's doc, per the
  ADR-0006 `natsio` pattern.
- **Permanent refusals, restated so they are cheap to defend later:** no
  templating/parameter substitution anywhere in scenario files; no removal
  directives in overlays (addition-only composition, network patch grammar
  excepted); no wall-clock times in demand; no compression of primary source
  files (a pack/transport concern only — MATSim's `.xml.gz` habit makes
  demand ungreppable and undiffable).

## Addendum (2026-07-21, M11 implementation notes)

- **Schema validation is realized as strict decoding + a schema-aware
  node-type check + semantic checks, not a JSON-Schema library.** yaml.v3's
  KnownFields makes unknown fields hard errors; the node-type check closes
  the decoder's silent coercions (a `1.9` truncating into an int field, a
  `true` stringifying into a string field — both verified yaml.v3
  behaviors); the semantic layer (versions, enums, finite-number checks,
  slice windows, type-list and origin-lane references, lexical AND physical
  path confinement) is hand-written and fail-loud. This keeps the
  dependency footprint at the one approved YAML library — a JSON-Schema
  engine would have been a second. If cross-field constraints outgrow
  hand-written checks, the CUE upgrade path (§2) is the answer, not a
  schema library bolt-on.
- **The content hash strips the run coordinates (seed, ticks).** The first
  M11 cut hashed the full manifest, which made the seed both content and
  run-key coordinate — incoherent with §6, caught in external review before
  anything durable bound hashes. The hashed manifest zeroes seed and ticks:
  seed is the second run-key coordinate, and determinism makes a longer run
  of the same scenario a strict trajectory superset of a shorter one, so
  ticks is recorded metadata, not identity. The hash is a domain-separated
  protocol (`traffic-sim/scenario-hash/v1`, length-prefixed slash-normalized
  path framing) with a pinned golden-vector test; bumping the YAML library
  or the canonicalization is a format event under §8, never silent.
  Network parts hash as canonical JSON (checkout EOL rewrites must not move
  identity); control/metrics stay raw bytes until their grammars land
  (§5's deferral) and then move to typed canonical hashing under a
  format_version bump.
- **RunSpec carries the content hash** (`engine.RunSpec.Hash`, additive
  omitempty JSON field, never read by the kernel, not CRC'd): the run
  registry's meta entry records (content-hash, seed). Documented in
  asyncapi RunMetaView and ADR-0006 (2026-07-21 addendum) — the contract
  rule is that additive metadata gets written down, not that it needs a
  schema bump.
- **Scenario demand EXECUTES in serve.** The reference demand director
  moved to `engine/natsio/demand` (library) with `cmd/demand-director` as
  the standalone wrapper; serve embeds it whenever the scenario declares
  demand parts, seeded with the RUN seed (respecting sweep overrides), so
  the recorded (hash, seed) covers the demand realization. simrun refuses
  demand-bearing scenarios (headless runs have no bus for verbs; the
  offline spawn-table mode remains the ADR's deferred answer). Multi-file
  demand shares one flow-index space, so request ids and RNG keys cannot
  collide across files. The flow RNG key now mixes the full 8-byte index
  (the M10 low byte collided at 256 flows per origin — this resamples
  director streams, safe because no pinned realization predates it).
  `pickType` accumulates weights in sorted-key order (float addition is
  non-associative; ADR-0005). First-arrival-at-window-start matches SUMO's
  flow-begin convention and is documented, not changed.
- **fmt preserves comments** (operates on the parsed node tree, sorts
  mapping keys, atomic temp-and-rename) — a formatter that deletes comments
  would defeat §2's reason for choosing YAML. The hash does not depend on
  fmt (it hashes the struct re-encode; comments are correctly invisible to
  identity).
- **The demand-director migrated to the scenario demand schema**; M10-era
  strict-JSON files parse once a `"format_version": 1` key is added (the
  version key is required — strictness is the point; an earlier draft of
  this note said "unchanged", which was wrong). The demo flow file is
  `cmd/demand-director/demo-i280.yaml`. Flows gained an optional
  `id` (omitempty — additive, never moves an existing hash) as the future
  overlay-patch anchor; duplicate ids within a file are rejected.
- **Flag discipline:** `-scenario` refuses scenario-owned flags
  (`-rate`/`-density`/`-types`/`-net`/`-netfile`); only `-seed`/`-ticks`
  override, per §6's sweep model.
- **Verified:** scenario-loaded and flag-built runs are bit-identical
  (I-280, rate 600/density 80/seed 1/3000 ticks → crc `e92229c4a89d3709`
  both ways, matching the M8 acceptance value); `scenario fmt` is
  idempotent, hash-stable, and comment-preserving; the strict fence rejects
  anchors, custom tags, implicit timestamps, multiple documents, unknown
  fields, scalar coercions, non-finite floats, `..`/absolute/backslash/unclean
  part paths, and escaping symlinks; serve with a demand-only scenario
  attaches the embedded director (verbs accepted, run-seeded).
- **Review provenance:** the post-implementation review by three external
  models (Claude Fable, GPT-5.6-sol, Gemini) caught the seed-in-hash
  incoherence, the unexecuted demand parts, the yaml.v3 scalar coercions,
  the non-finite float holes, the `pickType` map-order sum, the low-byte
  flow key, comment-destroying fmt, symlink escape, and the false
  JSON-compat claim — all fixed above before the first durable hash
  binding. Open for overlay time: flow-id namespacing across files,
  patchable entity granularity (JSON Merge Patch replaces arrays wholesale),
  vehicle-type DEFINITIONS as part files (`vtypes/*.yaml` — the hash
  currently covers type names, and the IDM parameters behind them ride on
  the engine version).
- **Not in M11** (per the ADR's deferred list): overlays/variant.yaml,
  control/metrics binding grammars (parts are existence-checked and hashed
  raw), pack format, offline spawn-table export.
