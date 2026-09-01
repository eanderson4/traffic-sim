# Fleet 3D model spec (v0) — hero track

Contract between the vehicle-model modules in this directory (`<id>.ts`) and the
renderers that consume them (the hero 3D replay page and the fleet-review page).
v0 models are **generic** parametric low-poly vehicles built as code — no
automaker likenesses, no external assets, no textures.

## Module contract

One file per model, named `<id>.ts` (e.g. `sedan.ts`). The ONLY allowed import
is `import * as THREE from 'three'`. Must compile under the viz tsconfig
(strict). Each module exports:

```ts
export interface VehicleDims { length: number; width: number; height: number } // metres
export const dims: VehicleDims
export function build(): THREE.Group
```

- `build()` returns a fresh `THREE.Group` per call; callers may tint/clone it.
- Deterministic: no `Math.random()`, no time, no network.
- ≤ 400 triangles per model. Primitives only: `BoxGeometry`, `CylinderGeometry`,
  `ExtrudeGeometry` (simple 2D shapes). Reuse `Material` instances across meshes
  inside one build; do not share module-level mutable state between calls.

## Frame (non-negotiable — renderers depend on it)

- Units are **metres**.
- Ground plane is `y = 0`: the bottom of the wheels touches y=0, nothing dips below.
- **Forward is `+Z`** (direction of travel), up is `+Y`, `x` is lateral.
- Origin: laterally centred (`x = 0`), and `z = 0` at the **footprint's
  longitudinal centre**. The engine reports vehicle position at the FRONT BUMPER,
  so a model's front bumper sits at `z = +dims.length / 2`; renderers shift by
  half the length when placing. `dims` in the module and in the manifest must
  match the built geometry.

## Look

- Low-poly "toy" aesthetic: `MeshStandardMaterial` with `flatShading: true`.
- Hex colours from the palette below only (copied literals — do NOT import
  theme.ts; this directory stays map-free).
- Named parts so renderers can restyle without knowing internals:
  - `body` — the single main body mesh (renderer tints its material per vehicle).
  - `cabin` — glasshouse / windows band.
  - `wheels` — a group containing every wheel; wheels are black cylinders with
    their axis along `x`.
  - `lights-front` — emissive headlight mesh(es) at the +Z face.
  - `lights-rear` — emissive tail-light mesh(es) at the −Z face.

### Palette (hex literals)

| role | value |
|---|---|
| body options (renderer picks) | `#eaf0ff` `#5f8dff` `#e8b43a` `#e5484d` `#1e9e6a` `#9fb2e8` |
| default body (model's own) | `#5f8dff` |
| cabin / glass | `#1d2950` |
| wheels, tyres | `#101018` |
| wheel hubs / trim | `#5c6ba8` |
| headlights (emissive) | `#fff3c4` |
| tail lights (emissive) | `#e5484d` |
| chassis / underbody | `#0a1230` |

Signal semantics elsewhere reuse `#2ecc71` / `#f5b301` / `#e5484d`.

## Fleet roster and classes

Engine classes (`engine/vehicle.go`): `car` = 5.0 × 2.0 m, `truck` = 12.0 × 2.5 m.
Manifest roster (12 models — v0 six plus the 2026-08-29 variety six):

| id | class | target dims (L×W×H, m) | notes |
|---|---|---|---|
| `sedan` | car | 4.8 × 1.9 × 1.45 | 3-box silhouette |
| `hatchback` | car | 4.2 × 1.8 × 1.5 | short deck, sloped tail |
| `suv` | car | 4.9 × 2.0 × 1.75 | tall cabin, roofline flat |
| `pickup` | car | 5.2 × 2.0 × 1.75 | cab + open bed tub, raised stance |
| `minivan` | car | 5.1 × 1.95 × 1.75 | one-box volume, long glass band |
| `coupe` | car | 4.6 × 1.85 × 1.35 | long hood, fastback, sits low |
| `box-truck` | truck | 11.5 × 2.5 × 3.6 | cab + box body, 6 wheels |
| `step-van` | truck | 8.5 × 2.4 × 3.1 | parcel walk-through, flat front |
| `school-bus` | truck | 11.5 × 2.5 × 3.1 | hood ahead of flat-front cabin, pillar band |
| `fire-truck` | truck | 10.5 × 2.5 × 3.3 | crew cab + equipment body, roof ladder |
| `semi` | semi | 13.5 × 2.5 × 3.8 | tractor 4.5 m + trailer 9 m, visible hitch gap |
| `city-bus` | bus | 12.0 × 2.55 × 3.1 | single rigid, big glass band |

Car-class length tolerance ±0.5 m is fine — renderers scale by `dims`, not by
class. `school-bus`/`fire-truck` map to the `truck` class (they fit its
12.0 × 2.5 m envelope) so pod replays get big-vehicle variety today.
`semi`/`city-bus` have no engine class in the current pod scenarios; they
ship for the multimodal track and the review page, unmapped until then.

## Manifest (`manifest.json`, owned by the renderer, models listed here)

```json
{
  "version": 1,
  "anchor": "footprint-center",
  "models": [
    { "id": "sedan",      "module": "./sedan.ts",      "classes": ["car"],   "weight": 3 },
    { "id": "hatchback",  "module": "./hatchback.ts",  "classes": ["car"],   "weight": 2 },
    { "id": "suv",        "module": "./suv.ts",        "classes": ["car"],   "weight": 2 },
    { "id": "pickup",     "module": "./pickup.ts",     "classes": ["car"],   "weight": 2 },
    { "id": "minivan",    "module": "./minivan.ts",    "classes": ["car"],   "weight": 1 },
    { "id": "coupe",      "module": "./coupe.ts",      "classes": ["car"],   "weight": 1 },
    { "id": "box-truck",  "module": "./box-truck.ts",  "classes": ["truck"], "weight": 2 },
    { "id": "step-van",   "module": "./step-van.ts",   "classes": ["truck"], "weight": 2 },
    { "id": "school-bus", "module": "./school-bus.ts", "classes": ["truck"], "weight": 1 },
    { "id": "fire-truck", "module": "./fire-truck.ts", "classes": ["truck"], "weight": 1 },
    { "id": "semi",       "module": "./semi.ts",       "classes": ["semi"],  "weight": 1 },
    { "id": "city-bus",   "module": "./city-bus.ts",   "classes": ["bus"],   "weight": 1 }
  ],
  "classDefault": { "car": "sedan", "truck": "box-truck", "semi": "semi", "bus": "city-bus" }
}
```

Renderer behaviour: pick per-vehicle model by hashing vehicle id over `weight`
within its class; fall back to `classDefault`, then to a plain box of class
dims, on any load/build error. Tint the `body` material per vehicle from the
body palette by the same hash.

**Theme-aware tint lists.** The palette table above is the navy/default one.
Renderers MAY substitute a theme-appropriate body-tint list when the default
would not read against their canvas (tinting is renderer behaviour; the
models themselves never change). The hero page's paper theme
(`hero.html?theme=paper`, math-vs-vibes light brand) passes its own list —
ink `#26262b`, accent blue `#2563eb`, vibes marker orange `#ED4B16`,
semantic red `#e5484d`, semantic green `#1e9e6a`, gray `#6e6e76` — because
the navy near-whites (`#eaf0ff`, `#9fb2e8`) vanish on `#fafafa`. The hash
and the rest of the renderer contract stay identical.

## Review loop

`fleet-review.html` renders each model on neutral ground with a wireframe
`dims` cage and fixed camera angles (`?model=<id>&angle=front34|side|rear34|top`)
for screenshot review. Review criteria: frame convention respected (sits on
y=0, faces +Z, dims truthful), recognisable silhouette at replay distance,
parts named correctly, tri budget kept.
