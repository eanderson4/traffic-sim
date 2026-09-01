# standards-and-patterns.md — integration-3d-vehicle-fleet

> Source: external research (case law, spec documents, industry practice) connected to the codebase | Researched: 2026-08-24 | Git HEAD: 2bc98de

The rules the fleet must obey: **§1 the legal core** (automaker
trademark/trade-dress law for car models on a public, episode-adjacent
site — the topic's biggest risk), §2 glTF 2.0 essentials, §3
instancing/LOD for fleets of hundreds, §4 scale convention, §5 the
style-guide pattern, §6 agent-parallel production workflow.

---

## 1. THE LEGAL CORE — automaker trademark & trade dress for 3D car models

Scope note: this is a US-law-centric reading (the project's public site and
videos are published from the US); it is engineering risk analysis, not
legal advice. Every case below was looked up; citations inline.

### 1.1 The three doctrines that reach car designs

1. **Trademark (Lanham Act)** — badges, logos, and *names* ("Mustang",
   "911", the three-pointed star) are registered marks. Using them is the
   clearest infringement path. Non-negotiable for us: **no badges, no
   logos, no real model names anywhere in the assets, manifest, or site
   copy.**
2. **Trade dress (§43(a))** — the *overall appearance* of a product can be
   protected as unregistered trade dress if it is non-functional and has
   acquired distinctiveness (secondary meaning). Car designs qualify:
   - *Ferrari S.p.A. v. Roberts*, 944 F.2d 1235 (6th Cir. 1991): the
     Daytona Spyder and Testarossa exterior designs were held protectable
     trade dress, and fiberglass replica kits "virtually identical in
     appearance" infringed ([Justia full text](https://law.justia.com/cases/federal/appellate-courts/F2/944/1235/34859/);
     [Harvard Berkman mirror](https://cyber.harvard.edu/IPCoop/91ferr1.html)).
     **Lesson: a 1:1 copy of a recognizable silhouette is infringement
     territory, even with zero badges.**
3. **Design patents (35 U.S.C. §171)** — automakers patent vehicle and
   component appearance; infringement is judged by the *ordinary observer*
   test from *Egyptian Goddess v. Swisa* (Fed. Cir. 2008, en banc)
   ([Foley analysis](https://www.foley.com/insights/publications/2008/09/23/federal-circuit-clarifies-the-test-for-design-patent-infringement-the-ordinary-observer-informed-by-the-prior-art/)).
   The doctrine is live and car-specific: *LKQ Corp. v. GM Global
   Technology Operations LLC*, 102 F.4th 1280 (Fed. Cir. 2024) (en banc) —
   a fight over GM's **fender** design patent that rewrote design-patent
   obviousness law ([Sterne Kessler case page](https://www.sternekessler.com/news-insights/publications/2024-federal-circuit-ip-appeals-lkq-corporation-v-gm-global-technology-operations-llc-102-f-4th-1280-fed-cir-2024-en-banc-stoll/);
   [Lexology Panoramic 2025](https://www.lexology.com/library/detail.aspx?g=98f4e693-658f-4b65-97e5-9694a14b5dbc)).
   Copyright is comparatively thin for utilitarian vehicle shapes (no
   separable artistry in a silhouette); trademark/trade-dress/design-patent
   are the real exposure.

### 1.2 The expressive-use shield — and its 2023 limits

Depicting real vehicles inside an *expressive work* (a video, a game, a
simulation visualization) is heavily protected:

- *AM General LLC v. Activision Blizzard, Inc.*, 450 F. Supp. 3d 467
  (S.D.N.Y. 2020): Humvees throughout Call of Duty, no license — dismissed
  under the *Rogers v. Grimaldi* First Amendment test (artistic relevance +
  not explicitly misleading); the court leaned on the game's realism goal
  ([Finnegan year-in-review](https://www.finnegan.com/en/insights/articles/trademark-law-year-in-review-select-cases-from-2020.html);
  [Mintz](https://www.mintz.com/insights-center/viewpoints/2231/2020-05-27-first-amendment-may-protect-use-trademarks-artistic);
  [Sunstein](https://www.sunsteinlaw.com/publications/ec-activision-wins-the-trademark-war-first-amendment-protects-depiction-of-humvees-in-realistic-video-games)).
  EA made the same *Rogers* argument against Textron over Battlefield 3
  helicopters (settled 2013) ([Univ. of Vienna thesis survey](https://phaidra.univie.ac.at/download/o:1602635);
  [Osgoode IPilogue, 2022](https://www.yorku.ca/osgoode/iposgoode/2022/11/23/when-the-game-gets-too-real-video-games-subjected-to-trademark-infringement-suits-for-depicting-real-life-vehicles/)).
- **But** *Jack Daniel's Properties v. VIP Products*, 599 U.S. 140 (2023)
  cut *Rogers* back: it does **not** apply when the mark is used "at least
  in part" **as a source identifier** — then ordinary
  likelihood-of-confusion governs ([supremecourt.gov opinion PDF](https://www.supremecourt.gov/opinions/22pdf/599us1r35_3f14.pdf);
  [Harvard JSEL analysis](https://journals.law.harvard.edu/jsel/2023/12/free-speech-is-a-funny-thing-jack-daniels-properties-v-vip-products-narrows-first-amendment-protections-for-trademark-usage/)).
  **Lesson for the fleet:** our protection is expressive context, so never
  use automaker marks as *branding* — no asset named `tesla-model3.glb`,
  no "drive a Porsche" site copy, no marks in the manifest. GTA-style
  invented names are descriptive world-building, not source claims.

### 1.3 The "in-the-style-of" line — where courts actually drew it

Two auto trade-dress cases *failed* for the rights holder, and they map the
safe zone:

- *General Motors Corp. v. Urban Gorilla, LLC*, 500 F.3d 1222 (10th Cir.
  2007): GM's Hummer trade dress vs. "Urban Gorilla" H2-lookalike body
  kits — the Tenth Circuit affirmed the DENIAL of GM's preliminary
  injunction on likelihood of confusion (own prominent branding,
  different market channels, disclaimers; post-sale confusion considered
  but not enough) ([FindLaw full text](https://caselaw.findlaw.com/court/us-10th-circuit/1475156.html);
  [Journal Record, 2007](https://journalrecord.com/2007/09/17/gm-loses-10th-circuit-case-appeal/)).
- *AM General Corp. v. DaimlerChrysler Corp.*, 311 F.3d 796 (7th Cir.
  2002): Chrysler's Jeep "family of grilles" mark vs. the Hummer H2's
  grille — the Seventh Circuit affirmed the DENIAL of Chrysler's
  preliminary-injunction motion (AM General/GM were plaintiffs-appellees
  on a declaratory-judgment action); Chrysler then lost at summary
  judgment in the district court (March 2003) and dropped everything
  ([Justia full text](https://law.justia.com/cases/federal/appellate-courts/F3/311/796/570207/);
  [Kirkland & Ellis, 2003](https://www.kirkland.com/news/press-release/2003/03/gm-wins-summary-judgment-against-daimlerchrysler-i);
  [Paul, Weiss NYLJ column](https://www.paulweiss.com/media/1932358/webclayton_nylj.pdf)).

Reading *Ferrari v. Roberts* against these two: **exact replicas lose;
identifiably-inspired-but-own-identity designs have real breathing room.**
That is exactly the line GTA has walked for 25 years with fictional marques
(Pfister ≈ Porsche, Obey ≈ Audi — [Hemmings, 2025](https://www.hemmings.com/stories/cars-of-grand-theft-auto-and-the-real-life-vehicles-that-inspired-them/);
[Retrogems, 2026-07](https://retrogems.fr/en/gta-6-fictional-car-brands-real-life-inspirations/))
— unchallenged at the largest commercial scale in the industry. It is also
the user's stated plan ("fleet of cars after different auto makers"), which
is lawful *if executed as pastiche*: change proportions, mix cues from
multiple donors, invent all names, include no badge or signature element
copied 1:1 (a Jeep-style seven-slot grille, BMW kidney grilles, the Ferrari
side-strake — the elements rights holders sue over are specific and
documented by the cases above).

### 1.4 Why games license cars (and why we can't and don't need to)

- Licensed authenticity is a marketing exchange: racing games pay for real
  cars; automakers treat top franchises as advertising (car-in-games was a
  claimed $2.8B industry by 2013 — cited via
  [Charles Univ. thesis](https://dspace.cuni.cz/bitstream/handle/20.500.11956/2115/DPTX_2013_2_11230_0_416749_0_152933.pdf?sequence=1&isAllowed=y));
  Real Racing 3 alone ships "over 70 different car models licensed from
  nineteen different car manufacturers" ([Arts Law Centre of Australia](https://www.artslaw.com.au/information-sheet/game-design-and-development?action=genpdf&id=11621)).
- Licenses are also *withheld* for brand image: Toyota pulled out of NFS
  Heat over illegal-street-racing optics ([driving.ca, 2019](https://driving.ca/auto-news/news/toyota-uk-called-hypocritical-for-video-game-street-racing-tweet)).
  And exclusivity deals shape the market: Porsche was EA-only 2000–2016;
  rivals shipped **RUF** (a separate manufacturer whose products are
  911-based) as the workaround ([RacingGames, 2026-08](https://racinggames.gg/article/from-yellowbird-to-free-agent-a-history-of-porsche-in-racing-games)).
- Marketplace terms confirm where the risk line sits: TurboSquid labels
  branded-vehicle models **"Editorial Use Only"** — games/commercial use
  prohibited without IP-owner permission ([TurboSquid help, 2026-04](https://www.turbosquid.com/help/en/articles/9937424-editorial-use-information)).
  Sketchfab/Fab practice is similar in spirit. **Downloading "real car"
  models is the single most dangerous sourcing move** — the license of the
  *mesh* (even CC-BY) does not clear the *depicted trade dress*.

### 1.5 The posture this project should adopt (decision candidate D2)

1. **Generic pastiche only.** Every model an original design; "in the
   style of" reads through *era/proportion archetypes* (80s wedge, modern
   crossover, Euro box van), never a single donor car.
2. **No badges, logos, or real model/marque names** — in meshes, texture
   atlases, file names, manifest fields, or site copy (the *Jack Daniel's*
   source-identification trap).
3. **No 1:1 signature elements** — no copied grille graphics, light-bar
   signatures, or silhouettes tracing one car (*Ferrari v. Roberts*).
4. **Prefer CC0 generic bases** (Kenney/KayKit) — already trade-dress-free
   by design; modifications keep them generic.
5. **AI-generated meshes get the same review** — a diffusion model can
   regurgitate a recognizable car; the consistency pass doubles as the
   trade-dress pass. (USCO 2025: pure AI output isn't copyrightable anyway
   — [competitors.md §3].)
6. **Document the policy in-repo** (asset README + provenance manifest:
   source, license, tool, prompt log) so the posture is auditable — the
   same transparency the repo applies to review archives.
7. Residual risk accepted: a C&D from an automaker is a badge of honor
   problem, not an existential one — remedy is renaming/removing one GLB.
   The *expressive* context (simulation visualization + videos) is the
   *AM General* heartland; we simply never claim source or sponsorship.

Open legal questions (unverified, flagged): EU/UK registered-design
exposure if the site is accessed there (Ferrari litigates in the EU);
whether any contemplated "CTA L" livery imitation touches transit-agency
marks (CTA's logo/livery are marks too — keep the L cars generic Chicago
*proportions*, not CTA branding).

---

## 2. glTF 2.0 essentials for the fleet

- **Container & conventions** ([Khronos glTF 2.0 spec](https://registry.khronos.org/glTF/specs/2.0/glTF-2.0.html)):
  GLB single-file binary (§4, GLB File Format Specification); **right-handed, +Y up, +Z forward —
  "the front of a glTF asset faces +Z"; all linear units are meters;
  angles radians** (§3.4). This is the fleet's scale/axis authority and it
  matches the engine's meters directly (`engine/vehicle.go:33,40`); only
  the heading handedness needs a documented Y-rotation mapping from the
  wire's CCW-from-east (`viz/src/theme.ts:167-176`).
- **Node structure for vehicles:** keep wheels (and any steering/spin
  targets) as named child nodes (spec §3.5.2 hierarchy) so the hero
  renderer can spin wheels from client-derived speed
  (`viz/src/snapshots.ts:11-15`); tractor/trailer as separate GLBs or
  separate named nodes for the articulation pivot (implementation.md §4).
- **Materials:** core metallic-roughness PBR is available, but the
  stylized look wants **[KHR_materials_unlit](https://github.com/KhronosGroup/glTF/tree/main/extensions/2.0/Khronos/KHR_materials_unlit)**
  (flat palette colors, cheapest shader, immune to lighting-rig
  differences — which is *itself* a fleet-consistency tool) or a minimal
  PBR-lite (flat-shaded normals, low metallic). Headlights/taillights via
  `KHR_materials_emissive_strength`. Windows: dark glass *color*, no
  transmission — transmission/clearcoat are hero-cost features for
  close-ups only.
- **Texture strategy:** one shared **palette atlas** across the whole
  fleet (the KayKit single-gradient-atlas pattern — [competitors.md §1])
  or vertex colors (`COLOR_0`, spec §3.7.2.2). One atlas → one material →
  trivial instancing and guaranteed palette consistency (§5).
- **Compression:** [gltfpack](https://www.npmjs.com/package/gltfpack) with
  `-cc` (EXT_meshopt_compression) + quantization, or Draco via
  [gltf-pipeline](https://github.com/CesiumGS/gltf-pipeline). Meshopt is
  the web-preferred route (faster JS-side decode, no WASM decoder
  requirement; gltfpack also supports `-mi` EXT_mesh_gpu_instancing and
  `-tc` KTX2) ([meshoptimizer.org/gltf](https://meshoptimizer.org/gltf/)).
  At our sizes (≲100 KB/model) even uncompressed GLB + the repo's existing
  brotli-at-the-edge pattern (`viz/public/_headers:5-14`) is acceptable —
  compression is a nice-to-have, not a gate.
- **Validation:** [Khronos glTF-Validator](https://github.com/KhronosGroup/glTF-Validator)
  (npm `gltf-validator`, JSON report output) — the CI gate: zero errors,
  warnings triaged. TS-native forks exist if we want it inside
  `node --test` ([@dcl/gltf-validator-ts](https://www.npmjs.com/package/@dcl/gltf-validator-ts)).

## 3. Instancing / LOD patterns for fleets of hundreds

- **Draw calls are the constraint, not triangles.** ~200 hero-scene
  vehicles × ~800 tris ≈ 160K tris — trivial for any GPU; 200 separate
  `Mesh` objects ≈ 200 draw calls is not. The standard fix is
  [`THREE.InstancedMesh`](https://threejs.org/docs/#api/en/objects/InstancedMesh):
  one draw call per (geometry, material) pair, per-instance matrix +
  color; 5,000 individually-transformed instances is the documented
  routine case ([boids/InstancedMesh tutorial](https://www.mysimulator.uk/content/tutorials/boids-instancedmesh-tutorial.html);
  [three.js perf guide](https://www.mysimulator.uk/content/tutorials/threejs-performance.html)).
  Per-instance body color rides `instanceColor` — the 3D analog of
  `theme.ts` `glyphColors`.
- **One atlas enables one InstancedMesh per class** (or even per
  super-class): merging textures into a single atlas is the prerequisite
  called out in the canonical three.js forum answer on instanced
  multi-texture rendering ([discourse.threejs.org](https://discourse.threejs.org/t/instancedmesh-can-it-be-used-to-draw-hundreds-of-planes-with-different-textures/13221)).
- **Caveats:** InstancedMesh is frustum-culled as one batch, not
  per-instance (three.js issue
  [#28102](https://github.com/mrdoob/three.js/issues/28102)) — set a
  manual bounding sphere around the junction; for multi-material hero
  models use `BatchedMesh` or split body/wheels into two InstancedMeshes
  (wheels get their own per-instance rotation).
- **Articulated vehicles** don't instance naively: tractor and trailer are
  two instanced pools posed independently per frame from the articulation
  model (implementation.md §4) — same as today's two MapLibre layers
  (`viz/src/main.ts:895-949`).
- **LOD:** [`THREE.LOD`](https://threejs.org/docs/#api/en/objects/LOD)
  swaps child levels by distance; gltfpack `-si` generates the simplified
  meshes ([gltfpack](https://www.npmjs.com/package/gltfpack)). At the hero
  renderer's registered budget (~10² vehicles + one junction —
  `.kb-meta.json` research hints for `integration-3d-hero-viz`) LOD is
  skippable in v1; it becomes relevant only if the fleet is reused at
  city scale. `EXT_mesh_gpu_instancing` in the GLB itself is for
  *transmission* of pre-instanced scenes — our instancing is runtime-driven
  from the baked pose stream, so it stays a renderer concern.
- **Determinism note:** poses come from the baked TSRB stream
  (contracts/baked-replay-v1.md); the renderer never simulates. The fleet
  adds zero nondeterminism as long as wheel-spin/articulation derive from
  the same sim-time discipline as `artic.ts` (Δt in sim seconds,
  `viz/src/artic.ts:18-21`).

## 4. SCALE convention (hard contract)

- glTF units are meters (spec §3.4) — anchor every model's bounding box to
  the authoritative class dims: car 5×2 m, truck 12×2.5 m
  (`engine/vehicle.go:33,40`); contemplated bus 12/18×2.55 m
  (`docs/kb/raw/domain-bus-operations/synthesis.md:38-39`); L car 14.6 m,
  consists to ~117 m (`docs/kb/raw/domain-rail-operations/synthesis.md:11,98,114`).
- Vehicle position on the wire is the **front bumper**
  (`engine/vehicle.go:46-53`); the MapLibre app compensates with a
  half-length offset (`viz/src/main.ts:1355-1359`). Fleet convention
  (decision candidate): **origin on the ground plane directly under the
  front bumper center, forward = +Z** — then the renderer places the
  origin at `(x, y)` with a single heading rotation and no per-class
  offset table. Heights are model-true (a van is taller than a sedan);
  only L×W are contract-checked (±2% tolerance).
- Wheelbase/wheel diameter scale *with the model* per the style bible
  (§5), so cars don't share one stretched wheel mesh across classes.

## 5. STYLE GUIDE pattern — how N models read as one world

Industry practice (outsourcing houses whose whole business is "many
builders, one style"): a **locked art bible** as single source of truth;
technical spec with poly/texel budgets and acceptance criteria agreed
*before* production; a paid **trial asset / style-match** before volume;
calibrated look-dev reference scenes; paint-over reviews on WIPs
([Sunstrike](https://sunstrikestudios.com/en/3d-game-art-outsourcing-guide);
[Nasty Rodent](https://nastyrodent.com/game-art-outsourcing/);
[Game-Ace](https://game-ace.com/game-art-design/);
[Juego](https://www.juegostudio.com/blog/outsourcing-game-art-models)).
The consistency levers that actually work, translated to the fleet:

1. **One shared palette atlas** (KayKit gradient-atlas proof,
   competitors.md §1) — every model UVs into the SAME 32–64 color texture;
   palette drift becomes impossible by construction.
2. **Proportion rules per class**, not per model: e.g. "sedan: cabin =
   0.55× length, wheel Ø = 0.14× length, beltline at 0.38× height" —
   builders fill a shared parametric skeleton, so silhouettes harmonize.
3. **A detail grammar**: one headlight language, one window-band style,
   one wheel arch treatment, one bevel weight; "automaker flavor" is
   expressed *within* the grammar (grille bar count, roofline slope) —
   which is also exactly how you stay pastiche (§1.3).
4. **Tri budget per class** (e.g. sedan ≤ 800, semi tractor+trailer ≤
   1,500, L car ≤ 900) with flat shading — budget pressure forces the
   same abstraction level, which IS the style.
5. **One QA render rig**: fixed camera/lighting/world for turntable
   contact sheets, so the consistency review compares like with like
   (calibrated look-dev, Sunstrike above).
6. Theme-awareness: palette chosen to read on both repo themes
   (`viz/src/theme.ts:56-119` navy + paper).

## 6. Agent-parallel production workflow

The user's plan is an agent swarm ("assemble a set of agents that can all
help build out the fleet of cars after different auto makers"). The
outsourcing industry's vendor model (§5 sources) maps cleanly onto agents,
and the repo's own machinery supplies the review culture (external-review
gate + one-round blocker triage, `AGENTS.md`).

**Roles → files produced:**

1. **Spec author (1 agent, runs first)** →
   `style-bible.md` (palette atlas spec, proportion rules, detail grammar,
   tri budgets, material rules, naming/origin conventions, legal rules
   from §1.5), one **reference model** built to the bible (the
   "trial asset" gate — volume production does not start until it passes),
   and per-model **briefs** (`briefs/sedan-aero.md` …): class dims from
   the engine, archetype direction ("80s Japanese wedge", "modern Euro
   crossover" — archetypes, not donor cars), tri budget, node/naming
   requirements (wheels, hitch), acceptance checklist.
2. **Builders (N agents, one model each, parallel)** → per model:
   the source (bpy script — code, diffable, reviewable; or a documented
   CC0 base + modification notes), exported GLB, turntable/contact-sheet
   renders from the standard rig, a `provenance` entry (source license /
   tool+plan / prompt log if AI-assisted), self-run QA report. Builders
   work ONLY from their brief + the bible; they never copy each other's
   in-progress work (drift control).
3. **Automated QA gate (CI, `node --test` idiom — implementation.md §8)**
   → blocks merge on: gltf-validator zero-errors; bbox vs class dims
   ±2%; tri budget; material/texture count (1 atlas); node naming
   (wheels present, hitch node for articulated); origin/forward check;
   manifest schema validation; provenance completeness. Render-smoke
   screenshots (headless Blender re-render or the hero renderer's own
   headless capture — sibling topic) committed as artifacts.
4. **Consistency reviewer (1 agent over the full contact sheet)** →
   cross-model pass against the bible: silhouette family, palette,
   abstraction level, and the trade-dress sniff test (§1.5 rule 5).
   Findings triaged at the repo's bar: blockers only, one round
   (`AGENTS.md` review workflow).
5. **Integrator (1 agent, last)** → the fleet **manifest JSON** (the
   contract with the hero renderer: per model — id, class, dims, GLB URL,
   origin/forward declaration, node map for wheels/hitch, tri count,
   license/provenance pointer) + wiring into `viz/public/models/` with
   `_headers` immutable caching (implementation.md §6).

**Why this shape:** the spec-first/trial-asset ordering kills the classic
swarm failure mode (N agents produce N styles, then an impossible merge);
the QA gates are mechanical so builders self-serve until green; the single
consistency pass is where taste lives, exactly once. File-gate note:
sources/manifest land in gated paths (external review), GLBs+renders land
in `viz/public/` (exempt, like `network.geojson` today) — implementation.md §9.

**Failure modes to preclude in the bible:** per-agent palette invention
(forbidden — atlas only); "more polys = better" (budget cap); donor-car
tracing (legal rules); hero-detail creep (the fleet reads at 10–40 m
camera distance, not 1 m); inconsistent origins (QA-checked).
