# competitors.md — integration-3d-vehicle-fleet

> Source: external research (web lookups, license pages, case reporting) | Researched: 2026-08-24 | Git HEAD: 2bc98de

Named sourcing routes and toolchains for the fleet: CC0/CC-BY repositories,
procedural/parametric generation, AI 3D generation (2026 posture), and how
stylized games actually assemble vehicle fleets. For each: what it provides,
the license reality (checked against current terms where possible), and the
fit vs. this project — an open-source, public-website, episode-adjacent
traffic sim that needs ~9 vehicle classes in ONE coherent low-poly style,
anchored to real-world dims, producible by an agent swarm.

## 1. CC0 / CC-BY model repositories

### Kenney.nl — Car Kit, Racing Kit

- What: cohesive low-poly game asset kits; the Car Kit (released 2022,
  remade v2.0, kart racers added v3.0) ships dozens of stylized vehicles in
  glTF/FBX/OBJ. Everything is one artist, one style, game-ready topology,
  shared texture atlases.
- License: **CC0, commercial OK, no attribution** — confirmed on Kenney's
  own support page: "all game assets on the asset pages are public domain
  licensed (CC0) … even in commercial projects. Attribution is not
  required" ([kenney.nl/support](https://kenney.nl/support);
  [kenney.nl/assets/car-kit](https://kenney.nl/assets/car-kit)). Third-party
  2026 surveys rank Kenney the top CC0 staple
  ([Cinevva, 2026-06](https://app.cinevva.com/guides/free-3d-model-sites)).
- Vs. this project: the strongest single base. Designs are already generic
  (zero trade-dress exposure — see standards-and-patterns.md §1), dims can
  be rescaled to engine dims, and the atlas/style is a working reference
  for the style bible. Weakness: everyone in gamedev recognizes Kenney
  cars (cheapens the "hero" look), and there is no bus / semi / L-car in
  matching style — so Kenney is a *substrate + style reference*, not the
  whole fleet.

### KayKit (Kay Lousberg) — City Builder Bits + vehicle work

- What: stylized low-poly packs; City Builder Bits is 32+ city models
  "textured using a single gradient atlas texture (1024×1024) that can be
  downsampled up to 128×128", explicitly for city/sim games
  ([kaylousberg.com/game-assets/city-builder-bits](https://kaylousberg.com/game-assets/city-builder-bits));
  Kay also has vehicle work ([kaylousberg.com/work/3d-vehicles](https://www.kaylousberg.com/work/3d-vehicles)).
- License: **CC0** — "Free for personal and commercial use, no attribution
  required" (same page).
- Vs. this project: the single-gradient-atlas technique is EXACTLY the
  right material strategy for a fleet of hundreds (one texture for all
  models → one material → trivial instancing). Style is chunkier/toy-like
  than Kenney; mixing the two without a unifying style bible would show.

### Quaternius

- What: ~79 packs / ~1,148 stylized low-poly models, GLB/FBX direct
  downloads ([Cinevva game-assets guide, 2026-01](https://app.cinevva.com/guides/game-assets-guide));
  a "Cars Bundle" (7 models) circulates via Poly Pizza
  ([poly.pizza/bundles](https://poly.pizza/bundles)).
- License: **CC0** (same source).
- Vs. this project: vehicle coverage is thin (7 cars, no transit). Useful
  for gap props (emergency vehicles appear in some packs) but not a fleet
  backbone.

### Poly Pizza

- What: aggregator/registry of low-poly CC0/CC-BY models (Kenney,
  Quaternius, Kay Lousberg, Google Poly archive), ~everything downloadable
  as GLB; public API with per-model metadata + direct CDN GLB URLs
  ([poly.pizza/docs/api/v1.1](https://poly.pizza/docs/api/v1.1); API usage
  example: [tessl.io registry](https://tessl.io/registry/skills/github/jasonkneen/tiny-world-builder/poly-pizza-api)).
- License: **per-model CC0 or CC-BY** — "all models are provided for free
  under CC0 (no attribution required) or CC-BY (attribution required)"
  ([Poly.Pizza MCP listing](https://mcp.aibase.com/server/1586804658025013410));
  importers record per-model attribution automatically (e.g.
  [kivloft.com mirror entries](https://kivloft.com/files/poly-pizza-xsfozw8e5n)).
- Vs. this project: the best *searchable index* of the CC0 world, and its
  per-model license metadata is the bookkeeping pattern our provenance
  manifest should copy. CC-BY entries are usable but drag an attribution
  file along; CC0 entries are friction-free.

### Sketchfab (post-Fab migration)

- What: the largest model library; CC-filterable downloads with 3D
  previews.
- License/availability reality, 2026: Epic moved the paid store to **Fab**;
  non-migrated store models are view-only; CC-BY and Fab-Standard content
  could migrate, "CC0, CC-BY-SA, CC-BY-NC, and CC-BY-ND models stay on
  Sketchfab, and as of mid-2026 free downloads and the download API still
  work" ([Cinevva Sketchfab comparison, 2026-06](https://app.cinevva.com/guides/sketchfab-polyhaven-kenney);
  migration notices: [Sketchfab blog, 2024-10](https://sketchfab.com/blogs/community/fab-publishing-portal-open-for-sketchfab-migration/),
  [80 Level, 2024-10](https://80.lv/articles/historians-are-concerned-about-epic-games-sketchfab-to-fab-migration)).
- Vs. this project: high variance in style, topology, and per-model
  license (NC/ND would poison an open-source commercial-adjacent repo);
  most *recognizable real-car* models there are exactly the trademark
  hazard class (§1 of standards-and-patterns). Usable only with a strict
  CC0/CC-BY + generic-design filter, per-model. Treat as a declining,
  last-resort source.

### TurboSquid free tier

- What: large marketplace with some free models.
- License reality: the royalty-free license covers generic content, BUT
  models of branded/trademarked products are labeled **"Editorial Use
  Only"**: "depict branded or trademarked products owned by other
  companies… For any non-editorial purposes, explicit permission from the
  IP owner is required… Can I use 'Editorial Use Only' models in a video
  game? No" ([TurboSquid Editorial Use Information, updated 2026-04](https://www.turbosquid.com/help/en/articles/9937424-editorial-use-information);
  license overview: [blog.turbosquid.com/turbosquid-3d-model-license](https://blog.turbosquid.com/turbosquid-3d-model-license/)).
- Vs. this project: effectively unusable for anything that looks like a
  real car (which is most of its vehicle catalog). The marketplace has
  already priced the legal risk we analyze in standards-and-patterns §1 —
  its label tells you where the line is.

### Objaverse / Objaverse-XL (AI2)

- What: 800K+ (XL: 10M+) annotated 3D objects with an API
  ([allenai/objaverse](https://github.com/allenai/objaverse);
  [objaverse-xl](https://github.com/allenai/objaverse-xl)).
- License: dataset-as-a-whole **ODC-BY v1.0**; per-object CC licenses —
  ~721K CC-BY 4.0, 52K CC-BY-NC-SA, 25K CC-BY-NC (unusable for us), a
  3.5K CC0 1.0 subset in Objaverse 1.0 ([PyPI objaverse](https://pypi.org/project/objaverse/0.0.3/);
  [Objaverse-XL paper](https://arxiv.org/html/2307.05663)).
- Vs. this project: research-grade grab-bag, mostly scraped from Sketchfab
  — style/quality chaos, per-object license filtering required. Not a
  fleet source; potentially a source of single reference meshes for an
  agent to study proportions from (with license checks).

## 2. Procedural / parametric generation

### Blender bpy headless (the agent-native route)

- What: Blender runs fully scriptable headless —
  `blender --background --python build_car.py` — with the Khronos glTF
  exporter bundled ([Blender command-line docs](https://docs.blender.org/manual/en/latest/advanced/command_line/arguments.html);
  [KhronosGroup/glTF-Blender-IO](https://github.com/KhronosGroup/glTF-Blender-IO)).
  Geometry Nodes adds parametric, input-driven variation; the ecosystem
  already sells/shares GN car-generator setups and LLM "GN script
  generator" skills ([LobeHub skill example](https://lobehub.com/tr/skills/agentskillexchange-skills-blender-geometry-nodes-script-generator)).
- License: Blender is GPL, but **output models are the author's property**
  (no license contamination of exported GLBs).
- Vs. this project: the strongest fit for the agent-swarm plan. A car
  becomes *code*: reviewable in diffs (matches the repo's external-review
  gate for source), deterministic (matches the repo's determinism culture),
  parametric per class (length/width as inputs = engine dims enforced by
  construction), and infinitely variant-able (one parametric sedan base →
  N "automaker-flavored" variants by changing grille/light/window
  grammar). Nobody needs to be a 3D artist; the swarm needs to write good
  bpy. This is how you get Synty-style consistency without Synty.

### Sloyd

- What: hosted parametric-template 3D generation: sliders/toggles over
  structured templates (not diffusion), game-ready topology, automatic
  LODs, API for in-app generation ([sloyd.ai/templates](https://www.sloyd.ai/templates);
  [OnyxRanked review, 2026-07](https://onyxranked.com/sloyd-review-2026/)).
- License: customized+exported models are yours, "no royalties or
  attribution required", commercial use OK; paid plans from ~$15/mo
  ([sloyd.ai/templates FAQ](https://www.sloyd.ai/templates);
  [sloyd.ai/pricing](https://www.sloyd.ai/pricing)).
- Vs. this project: closest hosted analog of "parametric base +
  variation". Fine for props; vehicle templates are generic-modern only,
  and per-fleet style control (palette grammar, proportion rules) is
  weaker than owning the generator. A viable fallback if bpy quality
  stalls.

## 3. AI 3D generation (2026 posture)

### Hosted leaders

- **Meshy** (Meshy 5/6): text/image-to-3D in ~45 s–2 min, PBR texturing
  up to 8K, in-browser rigging ([AI Gaming Dev comparison, 2026-07](https://aigamingdev.com/blog/ai-game-asset-tools-comparison/)).
  Free tier: 100 credits/mo, output is **CC BY 4.0 and PUBLIC** ("anyone
  else can use your model too"); paid plans make output private/commercial
  ([Alpha3D](https://www.alpha3d.io/kb/generate-3d/what-is-meshy-ai/);
  [Cinevva AI-3D guide, 2026-08](https://app.cinevva.com/guides/ai-3d-model-generators)).
- **Tripo (Tripo3D)**: free 200 credits/mo (~8 models) under CC BY 4.0;
  paid tiers for commercial use ([Neural4D benchmark, 2026-06](https://www.neural4d.com/features/neural4d-vs-tripo-vs-meshy-vs-rodin);
  [hyper3d.ai Tripo review](https://hyper3d.ai/blog/tripo3d-review)).
- **Rodin (Hyper3D/Deemos)**: commercial usage rights on paid/enterprise
  plans ([hyper3d.ai](https://hyper3d.ai/);
  [hyper3d.ai/pricing](https://hyper3d.ai/pricing)).

### Open-weight models

- **Microsoft TRELLIS / TRELLIS.2** — **MIT license**; TRELLIS.2 is a
  4B-param image-to-3D model (image/text-to-3D was the ORIGINAL TRELLIS)
  producing 512³ assets in ~3 s, 1024³ in ~17 s, and 1536³ in ~60 s
  (35+25), benchmarked on an H100 — a 24 GB GPU is the MINIMUM
  ([github.com/microsoft/trellis.2](https://github.com/microsoft/trellis.2);
  [State of AI 3D Generation 2026](https://www.3daistudio.com/state-of-ai-3d-generation-2026);
  [sorceress.games survey, 2026-05](https://sorceress.games/blog/pluck-an-ai-3d-model-generator-free-browser-native)).
- **TripoSR, Stable Fast 3D, Hi3DGen** — MIT, sub-second draft meshes on
  consumer GPUs (same sources).
- **Hunyuan3D 2.x (Tencent)** — strong quality but under Tencent's
  community license with restrictions (not plain MIT — verify current
  terms before use) ([3daistudio state-of-2026](https://www.3daistudio.com/state-of-ai-3d-generation-2026);
  [Pixazo open-source 3D APIs, 2026-07](https://www.pixazo.ai/blog/best-open-source-3d-model-generation-apis)).

### The copyright wrinkle (applies to ALL AI output)

- US Copyright Office, *Copyright and AI, Part 2: Copyrightability*
  (2025-01-29): purely AI-generated works lack human authorship and are
  **not copyrightable**; human+AI works protect only the human parts
  ([ScenePaper explainer, 2026-08](https://scenepaper.com/blog/ai-screenwriting-credit-copyright);
  [promise.legal, 2026-07](https://blog.promise.legal/ai-game-assets-what-studios-own-2026/)).
  *Thaler v. Perlmutter* (D.C. Cir. 2025) affirmed the human-authorship
  requirement (same sources).
- Vs. this project: mostly benign — an open-source project needs no
  exclusivity, and "not copyrightable" ≈ public-domain-equivalent for our
  purposes. Two real consequences: (1) nobody can enforce the fleet
  against copiers (we don't care), (2) tool ToS still bind the *user*
  (attribution on free tiers, plan gates), so the provenance manifest must
  record which tool/plan produced each model. Bigger practical issue:
  diffusion-style generators produce **inconsistent style across N
  models** and dense topology needing retopo to hit low-poly budgets —
  the exact opposite of fleet consistency. Verdict: AI gen is an
  *ideation/sketch* tool for the swarm (generate 8 silhouettes, pick,
  remodel parametrically), not the production line.

## 4. How stylized games assemble vehicle fleets

### Asset-store kits (Synty POLYGON et al.)

- What: Synty Studios' POLYGON line is the industry benchmark for "N
  models that read as one world" — cohesive palettes, shared texel
  density, consistent proportions across hundreds of packs
  ([80 Level on Synty](https://80.lv/articles/check-out-these-incredible-game-dev-assets-by-synty);
  Unity Asset Store listings, e.g.
  [POLYGON packs](https://assetstore.unity.com/packages/3d/environments/polygon-prototype-pack-art-by-synty-137126)).
- License: Unity Asset Store EULA, per-seat; you may ship built games but
  may NOT redistribute source assets — fatal for an open-source repo whose
  repo *is* the product (license field visible on every store listing).
- Vs. this project: unusable as a source; essential as the **quality and
  consistency bar** the style bible is measured against.

### Outsourced production (what the swarm should imitate)

- Studios hand vendors a **locked art bible** + technical spec (poly/texel
  budgets, acceptance criteria agreed before production), run a **paid
  trial asset / style-match** (typically 3–5 business days) before
  committing volume, keep a single source of truth for style, and review
  with paint-overs on WIPs ([Nasty Rodent outsourcing guide](https://nastyrodent.com/game-art-outsourcing/);
  [Game-Ace](https://game-ace.com/game-art-design/);
  [Juego Studios, 2026-07](https://www.juegostudio.com/blog/outsourcing-game-art-models);
  [Sunstrike Studios](https://sunstrikestudios.com/en/3d-game-art-outsourcing-guide)).
- Vs. this project: map roles 1:1 onto agents — the art bible is the
  style-bible doc, the trial asset is the first-model gate, the paint-over
  review is the consistency pass. The industry has already solved
  "N builders, one style"; we just cast agents in the vendor role.

### In-house pastiche fleets (the GTA pattern)

- Rockstar builds every GTA vehicle in-house as a recognizable-but-invented
  pastiche under fictional marques (Pfister ≈ Porsche, Obey ≈ Audi,
  Übermacht ≈ BMW) — a deliberate legal+creative posture maintained for
  decades at massive commercial scale ([Hemmings on GTA's real-life
  inspirations, 2025-05](https://www.hemmings.com/stories/cars-of-grand-theft-auto-and-the-real-life-vehicles-that-inspired-them/);
  [Retrogems on GTA 6 fictional brands, 2026-07](https://retrogems.fr/en/gta-6-fictional-car-brands-real-life-inspirations/);
  [GTA Base FAQ](https://www.gtabase.com/gta-6/vehicles/)).
- The mirror-image practice: licensed racing games (Gran Turismo, Forza,
  Real Racing 3's "70+ car models licensed from 19 manufacturers" —
  [Arts Law Centre of Australia](https://www.artslaw.com.au/information-sheet/game-design-and-development?action=genpdf&id=11621))
  pay for authenticity, and automakers treat games as marketing — while
  sometimes *withholding* licenses for brand-safety (Toyota absent from
  NFS Heat, [driving.ca, 2019](https://driving.ca/auto-news/news/toyota-uk-called-hypocritical-for-video-game-street-racing-tweet));
  Porsche's EA exclusivity (2000–2016) forced rivals to license **RUF** —
  911-based cars from a technically-separate manufacturer — as the
  workaround ([RacingGames history, 2026-08](https://racinggames.gg/article/from-yellowbird-to-free-agent-a-history-of-porsche-in-racing-games);
  [Yahoo Autos, 2026-03](https://autos.yahoo.com/people-and-culture/articles/why-porsche-cars-were-absent-000000983.html)).
- Vs. this project: GTA's is the exact posture to adopt (generic
  "in-the-style-of" designs, invented naming, no badges) — validated by
  25 years of unchallenged practice, and it matches the user's stated
  plan ("fleet of cars after different auto makers"). The licensing route
  is a non-starter (cost, negotiation, and some makers simply won't).

## 5. Sourcing routes at a glance

| Route | License reality | Style consistency | Effort/fit |
|---|---|---|---|
| Kenney/KayKit/Quaternius CC0 | friction-free | high within one kit | instant base; thin class coverage |
| Poly Pizza CC-BY subset | attribution file needed | mixed | gap-filling |
| Sketchfab CC (2026) | per-model minefield, declining | low | last resort |
| TurboSquid free | branded = editorial-only | low | avoid for vehicles |
| Objaverse | ODC-BY + per-object CC | none | research refs only |
| Blender bpy parametric | output is ours | as designed | **best fit for swarm** |
| Sloyd | commercial OK | medium | fallback |
| Meshy/Tripo/Rodin hosted | free=CC-BY+public; paid=private | low across N models | ideation sketches |
| TRELLIS.2/TripoSR open weights | MIT | low across N models | ideation, self-hosted |
| Synty-style kits | no redistribution | benchmark | bar to match, not a source |
| Commissioned art | clean (work-for-hire) | high | real money; not the plan |
