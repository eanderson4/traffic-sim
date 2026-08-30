# synthesis.md — integration-3d-vehicle-fleet

> Researched: 2026-08-24 | Git HEAD: 2bc98de | Status: complete

A fleet of low-poly stylized 3D vehicle models (sedans/SUVs/vans/trucks/
buses/L cars, "in the style of" different automakers) for the planned
rung-2 three.js hero renderer (`integration-3d-hero-viz`), produced by an
agent swarm. The render substrate is green field — no three.js, no glTF,
no 3D assets anywhere in the repo today (implementation.md §7) — so the
fleet defines its own contract: GLBs in `viz/public/models/` + a manifest
mapping scenario class → model, anchored to the engine's authoritative
class dims (`engine/vehicle.go:33,40`) and front-bumper anchor convention
(`engine/vehicle.go:46-53`).

The three load-bearing findings:

1. **The legal posture is settled by case law, and it blesses the plan —
   with rules.** Exact replicas of recognizable car designs are
   protectable trade dress (*Ferrari v. Roberts*, 6th Cir. 1991), but
   inspired-with-own-identity designs have repeatedly won (*GM v. Urban
   Gorilla*, 10th Cir. 2007; *AM General v. DaimlerChrysler*, 7th Cir.
   2002 — the Jeep-grille case). Depicting real vehicles in expressive
   works is First-Amendment protected (*AM General v. Activision*,
   S.D.N.Y. 2020), but *Jack Daniel's v. VIP Products* (2023) kills that
   shield the moment a mark is used as a source identifier. GTA's
   25-year unchallenged practice — invented marques over recognizable
   archetypes — is the exact posture to adopt: **generic pastiche, no
   badges, no real names, no 1:1 signature elements, policy documented
   in-repo.** (standards-and-patterns.md §1)
2. **Parametric generation in headless Blender (bpy) is the production
   line that fits both the swarm and the repo.** Models as code: diffable
   (fits the external-review gate), deterministic (repo culture),
   class dims enforced by construction (length/width are generator
   inputs), and one parametric base yields N automaker-flavored variants.
   CC0 kits (Kenney/KayKit, confirmed CC0 on their own pages) are the
   style reference + gap fillers; AI 3D tools (Meshy/Tripo/Rodin,
   TRELLIS.2 MIT) are ideation sketchpads, not the line — diffusion-style
   output is style-inconsistent across N models and needs retopo, and
   free tiers make your models public CC-BY. (competitors.md)
3. **Fleet consistency is an industry-solved problem — copy the
   outsourcing playbook onto agents.** Locked art bible → trial asset →
   volume production with per-model briefs → mechanical QA gates
   (gltf-validator, bbox ±2%, tri budgets) → ONE consistency pass over
   contact-sheet renders → manifest integration. One shared palette atlas
   makes palette drift impossible and enables one InstancedMesh per
   class. (standards-and-patterns.md §§5-6)

## Decision candidates

### D1 — Sourcing mix: agent-built parametric core + CC0 gap-fillers + AI ideation

- **Choice:** Production models are authored by agents as parametrized bpy
  generators (or direct mesh edits on CC0 bases), exported to GLB
  headlessly. Kenney/KayKit CC0 assets serve as style anchors, proportion
  references, and stopgaps for unglamorous classes. AI text/image-to-3D
  (self-hosted TRELLIS.2, MIT — image-to-3D only; text-to-3D was the
  original TRELLIS) is allowed for *concept silhouettes only*,
  never shipped meshes.
- **Why:** only parametric-in-our-hands gives (a) engine dims enforced by
  construction, (b) Synty-grade consistency across ~9 classes, (c)
  zero license friction, (d) code-form sources matching the repo's review
  machinery. CC0 kits alone can't cover bus/semi/L-car classes in one
  style; AI alone can't hold a style across N models.
- **Trade-off:** slower first model than downloading a kit; requires the
  style bible + reference model to exist first (spec-first ordering). AI
  ideation adds a provenance-tracking duty.
- **Field context:** Sloyd proves hosted parametric works commercially;
  Kenney/KayKit prove one-author consistency scales; Synty is the quality
  bar. Rejected: Sketchfab/TurboSquid vehicle downloads (license + trade
  dress minefield), commissioned art (money, and not the user's plan).

### D2 — Legal posture: GTA-style pastiche, codified

- **Choice:** Adopt standards-and-patterns.md §1.5 as written fleet policy
  (in the asset README + every brief): original generic designs expressed
  as era/proportion archetypes; no badges/logos/real names in meshes,
  files, manifest, or copy; no copied signature elements; CC0 bases
  preferred; AI output gets the same trade-dress review; provenance
  manifest for every model.
- **Why:** matches the case law (§1.3), matches decades of industry
  practice (GTA), costs nothing, and the residual risk is a per-asset
  takedown at worst.
- **Trade-off:** "in the style of automaker X" can never be *claimed* in
  marketing copy — the flavor is conveyed visually through archetypes
  (wedge/crossover/box-van), not brands. Episode viewers get "that reads
  like a German sedan," not "that's a BMW."
- **Field context:** the alternative (licensing real designs) is a
  non-starter for an open-source project — cost, negotiation, and some
  makers won't license (Toyota/NFS; Porsche's 16-year EA exclusivity).

### D3 — Class taxonomy v1 (9 classes, mapped to engine dims)

| Class | L×W (m) | Anchor authority | Notes |
|---|---|---|---|
| sedan | 5.0×2.0 | engine Car (`vehicle.go:33`) | archetype flavors: wedge, notchback, aero |
| hatchback | 4.2×1.8 | scaled Car | city mix variety |
| SUV/crossover | 4.8×1.9 | Car (L) | taller greenhouse; the modern default |
| van/box-van | 5.5×2.0 | Car+ | Euro-box archetype |
| pickup | 5.4×2.0 | Car+ | US mix variety |
| box truck | 8.0×2.5 | between Car/Truck | single rigid, no articulation |
| semi (tractor+trailer) | 12×2.5 (4+8 split) | engine Truck (`vehicle.go:40`), split `theme.ts:152-153` | TWO meshes, hitch pivot at 4 m |
| transit bus | 12×2.55 (+18 m artic later) | contemplated (`domain-bus-operations/synthesis.md:38-39`) | CTA 40-ft proportions, generic livery |
| L car (married pair) | 14.6/car ×2.9, consists to 117 m | contemplated (`domain-rail-operations/synthesis.md:11,98,114`) | car-chain articulation; generic Chicago proportions, NOT CTA branding |

- **Choice:** v1 = the 9 above; emergency vehicles deferred (no engine
  class, no behavior; revisit with a domain-emergency topic).
- **Why:** covers the engine's two shipped classes, the two contemplated
  sibling-topic classes, and fills the visual mix real cities have
  (hatch/SUV/van/pickup/box-truck) without inventing engine semantics.
- **Trade-off:** sub-car classes (hatch/SUV…) are visual-only variants of
  engine class 0 until the engine grows types — the manifest maps many
  models to one class index and the renderer picks per-instance (seeded by
  vehicle id for replay-stable assignment).

### D4 — Style bible outline (the swarm's single source of truth)

`fleet/style-bible.md`: (1) **palette**: one 32–64 color atlas texture
(all models UV into it; navy+paper legibility check against
`viz/src/theme.ts:56-119`); (2) **proportion rules per class** (cabin
fraction, wheel Ø × length, beltline height); (3) **detail grammar** (one
headlight language, one window-band style, one arch treatment; automaker
flavor = grille bars/roofline within the grammar); (4) **tri budgets**
(sedan ≤800, hatch ≤700, SUV ≤900, van ≤900, pickup ≤900, box truck ≤1k,
semi ≤1.5k total, bus ≤1.2k, L car ≤900/car); (5) **materials**:
unlit/PBR-lite, one atlas, emissive head/tail lights, no transmission;
(6) **conventions**: meters, origin under front bumper, forward +Z,
named wheel/hitch nodes; (7) **legal rules** (D2); (8) **QA checklist +
render rig** (fixed turntable camera/light).
- **Why:** the outsourcing industry holds fleets consistent across dozens
  of *human* vendors with exactly this document + a trial-asset gate;
  agents need it even more (no shared taste).
- **Trade-off:** writing it well is the single highest-leverage task in
  the whole fleet — and the easiest to do lazily.

### D5 — Swarm production plan (roles → files)

1. **Spec author** → `style-bible.md`, reference model + its contact
   sheet, per-model `briefs/*.md`. Gate: reference model passes full QA
   before any builder starts (the trial asset).
2. **Builders** (1 per model, parallel) → `src/<model>.py` (bpy) or
   documented CC0 base + mods, `dist/<model>.glb`, turntable renders,
   provenance entry, self-run QA report.
3. **QA gates** (CI, `node --test` idiom per `viz/package.json:12`) →
   gltf-validator 0 errors; bbox vs D3 dims ±2%; tri budget; 1-atlas
   check; node naming; origin/forward; manifest schema; provenance
   complete. Render-smoke screenshots as artifacts.
4. **Consistency reviewer** → side-by-side contact-sheet pass vs the
   bible + trade-dress sniff test; blockers only, one round (repo's
   triage bar, `AGENTS.md`).
5. **Integrator** → `fleet/manifest.json` + `viz/public/models/` wiring +
   `_headers` cache block.
- **Why:** kills the N-agents-N-styles failure mode structurally; taste
  is applied exactly once (step 4); everything else is mechanical.
- **Trade-off:** strictly serialized start (bible + reference first) —
  builders idle until the gate opens.

### D6 — Integration contract with the hero renderer

- **Choice:** `viz/public/models/<class>/<model>.glb` (immutable-cached,
  brotli like `/baked/*`) + `manifest.json` = the fleet's whole API:
  per model — `id`, `class` (scenario type name + engine class index),
  `dimsM {length,width,height}`, `origin: "front-bumper-ground"`,
  `forward: "+Z"`, `nodes {wheels[], hitch?}`, `triCount`, `palette`,
  `provenance`. Renderer maps TSRB/TSSF class index → manifest entry →
  InstancedMesh pool; per-instance variant pick seeded by vehicle id;
  tractor/trailer pools posed from the articulation port of `artic.ts`.
- **Why:** contracts in this repo pin only the class INTEGER
  (`contracts/asyncapi.yaml:1135`); the name→model binding belongs to a
  versioned fleet manifest, not the wire. Keeps the renderer a pure
  consumer (its registered scope).
- **Trade-off:** another manifest to keep in sync with scenarios; a
  schema test (`node --test`) makes drift a CI failure.

## Sourcing options compared

| Option | License | Consistency | Dims fidelity | Swarm fit | Verdict |
|---|---|---|---|---|---|
| Agent bpy parametric | ours | designed-in | enforced | native | **core** |
| Kenney/KayKit/Quaternius CC0 | friction-free | high in-kit | rescale needed | reference/base | **supporting** |
| Poly Pizza CC-BY | attribution file | mixed | rescale | gap-filler | sparing |
| Sloyd hosted parametric | commercial OK | medium | good | fallback | backup |
| TRELLIS.2/TripoSR (MIT, self-host) | MIT | low across N | poor | ideation only | sketches |
| Meshy/Tripo/Rodin hosted | free=CC-BY+public; paid=private | low | poor | ideation only | sketches |
| Sketchfab CC / Objaverse | per-model minefield | none | no | avoid | refs only |
| TurboSquid "real car" models | editorial-only = **no** | — | — | **avoid** | never |
| Synty-style paid kits | no redistribution | benchmark | no | bar only | measure against |
| Commissioned art | clean | high | good | not the plan | rejected |

## QA automation (checklist for the integrator)

- `gltf-validator` zero errors on every GLB ([KhronosGroup/glTF-Validator](https://github.com/KhronosGroup/glTF-Validator)); TS fork available for in-process `node --test` use.
- Dimension check: parse GLB JSON chunk, compute bbox from accessor
  min/max (spec §3.6.2.5 makes this free), assert D3 dims ±2%.
- Tri budget per D4; material count == 1 atlas; unlit or declared
  PBR-lite; emissive strength within cap.
- Naming/origin: wheels named per convention; hitch node on articulated;
  bbox min-z == 0 (front bumper at origin), forward +Z.
- Contact-sheet render (headless Blender rig or hero-renderer capture)
  committed per model; consistency pass recorded in the review archive
  alongside code reviews.
- Manifest schema test: every scenario type name has ≥1 model; every
  model's class exists in the taxonomy.

## Open questions

1. **EU/UK registered-design exposure** for a globally-accessible site —
   unverified; US reading says low, Ferrari litigates in the EU. If the
   site ever gets EU-heavy traffic, re-check. (flagged, not blocking)
2. **CTA livery/branding on the L cars** — CTA marks exist; keep L cars
   generic-Chicago-proportioned with an invented livery. Exact CTA paint
   scheme = new analysis needed.
3. **Hunyuan3D 2.x license** — community license with restrictions (not
   plain MIT); verify current terms before any use. TRELLIS.2 (MIT) is
   the clean open-weight option regardless.
4. **Sketchfab longevity** — as of mid-2026 CC downloads still work but
   the platform is sunsetting into Fab; do not build sourcing
   dependencies on it.
5. **Variant→instance assignment**: seeded per vehicle id (replay-stable)
   vs per-spawn random (breaks replay byte-identity of *visuals* only —
   acceptable?). Recommend seeded; confirm with hero-viz topic.
6. **Emergency class**: deferred — needs a domain topic for behavior
   before a model means anything.
7. Whether wheel-spin/steering animation is worth its node complexity in
   v1 (cheap to add later — nodes are declared in the manifest).

## Connections

- **integration-3d-hero-viz** (PENDING, `INDEX.md:93`): the fleet's only
  consumer; owns renderer choice (three.js), camera/cinematics, headless
  capture, and the ~10²-vehicle perf budget this fleet's tri budgets are
  calibrated to. D6 is the shared contract — the two topics should agree
  on the manifest schema before either implements.
- **domain-bus-operations** (researched): bus class dims (12/18 m ×
  2.55 m) the bus model must match; bus-lane/TSP visuals may want a
  distinct bus silhouette in hero shots.
- **domain-rail-operations** (researched): L consist dims (14.6 m cars,
  117 m max) and the one-VehicleType-per-consist design that forces
  viz-side car-chain articulation (implementation.md §4).
- **ADR-0003** (`decisions/ADR-0003-maplibre-vis.md:26`): the additive
  three.js consumer boundary the fleet+renderer live inside; a fleet
  milestone may warrant the ADR-0003 addendum the hero-viz registration
  anticipates.
- **ADR-0007**: multi-class vehicle support — the engine-side taxonomy
  the manifest maps to.
- Review machinery (`AGENTS.md`, `scripts/external-review.sh`): GLBs in
  `viz/public/` are gate-exempt; generator sources and the manifest are
  not — the swarm's file placement must respect the split
  (implementation.md §9). The repo's one-round blocker-triage bar is the
  consistency-pass standard.

## Sources (primary)

- Case law: [Ferrari v. Roberts (6th Cir. 1991)](https://law.justia.com/cases/federal/appellate-courts/F2/944/1235/34859/) · [AM General v. Activision (S.D.N.Y. 2020) via Finnegan](https://www.finnegan.com/en/insights/articles/trademark-law-year-in-review-select-cases-from-2020.html) · [Jack Daniel's v. VIP (2023) SCOTUS PDF](https://www.supremecourt.gov/opinions/22pdf/599us1r35_3f14.pdf) · [GM v. Urban Gorilla (10th Cir. 2007)](https://caselaw.findlaw.com/court/us-10th-circuit/1475156.html) · [AM General v. DaimlerChrysler (7th Cir. 2002)](https://law.justia.com/cases/federal/appellate-courts/F3/311/796/570207/) · [LKQ v. GM (Fed. Cir. 2024) via Sterne Kessler](https://www.sternekessler.com/news-insights/publications/2024-federal-circuit-ip-appeals-lkq-corporation-v-gm-global-technology-operations-llc-102-f-4th-1280-fed-cir-2024-en-banc-stoll/)
- Practice: [TurboSquid Editorial Use](https://www.turbosquid.com/help/en/articles/9937424-editorial-use-information) · [Hemmings GTA](https://www.hemmings.com/stories/cars-of-grand-theft-auto-and-the-real-life-vehicles-that-inspired-them/) · [RacingGames Porsche/RUF](https://racinggames.gg/article/from-yellowbird-to-free-agent-a-history-of-porsche-in-racing-games) · [Arts Law AU games+marks](https://www.artslaw.com.au/information-sheet/game-design-and-development?action=genpdf&id=11621) · [driving.ca Toyota/NFS](https://driving.ca/auto-news/news/toyota-uk-called-hypocritical-for-video-game-street-racing-tweet)
- Assets: [Kenney support (CC0)](https://kenney.nl/support) · [Kenney Car Kit](https://kenney.nl/assets/car-kit) · [KayKit City Builder](https://kaylousberg.com/game-assets/city-builder-bits) · [Poly Pizza API](https://poly.pizza/docs/api/v1.1) · [Cinevva free 3D sites 2026](https://app.cinevva.com/guides/free-3d-model-sites) · [Objaverse (PyPI)](https://pypi.org/project/objaverse/0.0.3/)
- AI 3D: [microsoft/TRELLIS.2](https://github.com/microsoft/trellis.2) · [State of AI 3D 2026](https://www.3daistudio.com/state-of-ai-3d-generation-2026) · [Meshy licensing via Alpha3D](https://www.alpha3d.io/kb/generate-3d/what-is-meshy-ai/) · [Neural4D benchmark](https://www.neural4d.com/features/neural4d-vs-tripo-vs-meshy-vs-rodin) · [Sloyd templates/FAQ](https://www.sloyd.ai/templates) · [USCO AI copyright explainer](https://blog.promise.legal/ai-game-assets-what-studios-own-2026/)
- Specs/tools: [glTF 2.0 spec §3.4](https://registry.khronos.org/glTF/specs/2.0/glTF-2.0.html) · [KHR_materials_unlit](https://github.com/KhronosGroup/glTF/tree/main/extensions/2.0/Khronos/KHR_materials_unlit) · [gltfpack](https://www.npmjs.com/package/gltfpack) · [glTF-Validator](https://github.com/KhronosGroup/glTF-Validator) · [three.js InstancedMesh](https://threejs.org/docs/#api/en/objects/InstancedMesh) · [three.js #28102 instancing culling](https://github.com/mrdoob/three.js/issues/28102) · [Blender CLI docs](https://docs.blender.org/manual/en/latest/advanced/command_line/arguments.html) · [glTF-Blender-IO](https://github.com/KhronosGroup/glTF-Blender-IO)
- Outsourcing/style: [Sunstrike](https://sunstrikestudios.com/en/3d-game-art-outsourcing-guide) · [Nasty Rodent](https://nastyrodent.com/game-art-outsourcing/) · [Game-Ace](https://game-ace.com/game-art-design/) · [Juego](https://www.juegostudio.com/blog/outsourcing-game-art-models) · [80 Level on Synty](https://80.lv/articles/check-out-these-incredible-game-dev-assets-by-synty)
