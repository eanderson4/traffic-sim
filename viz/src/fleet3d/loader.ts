/// <reference types="vite/client" />
// loader.ts — the fleet loader implementing viz/src/fleet3d/SPEC.md: fetch
// manifest.json, dynamic-import each listed model module, validate its
// contract (dims + build(), smoke-built once here so a throwing module can
// never reach a page), and pick a model per vehicle by hashing the vehicle
// id over the class pool's weights. Fallback ladder per SPEC: weighted hash
// pick → classDefault → a plain box of class dims. A missing/unreadable
// manifest or a model that fails to import/build is recorded in
// Fleet.errors and degraded around — callers always get a working Fleet.
//
// Model modules are discovered with vite's import.meta.glob so a module
// that does not exist yet (or was removed) simply isn't a candidate —
// adding a model file never requires editing this file, only the manifest.

import * as THREE from "three";

export interface VehicleDims {
  length: number; // metres, along +Z (forward)
  width: number; // metres, along X
  height: number; // metres, along +Y (up)
}

// The loose shape a dynamically imported module is validated against.
interface ModelModule {
  dims?: unknown;
  build?: unknown;
}

export interface FleetModel {
  id: string;
  classes: readonly string[];
  weight: number;
  dims: VehicleDims;
  build(): THREE.Group;
}

export interface Fleet {
  readonly models: readonly FleetModel[]; // models that loaded AND smoke-built
  readonly errors: readonly string[]; // human-readable load/build failures
  // pick resolves a vehicle id + class to a model. Never throws, never
  // returns null: the ladder ends at a box of class dims.
  pick(vehicleId: number, cls: string): FleetModel;
}

// CLASS_BOX_DIMS are the engine class footprints (engine/vehicle.go: car
// 5×2, truck 12×2.5) plus SPEC.md's stand-ins for classes with no engine
// class yet. Height is a renderer choice (the engine has none).
const CLASS_BOX_DIMS: Record<string, VehicleDims> = {
  car: { length: 5.0, width: 2.0, height: 1.5 },
  truck: { length: 12.0, width: 2.5, height: 3.4 },
  semi: { length: 13.5, width: 2.5, height: 3.8 },
  bus: { length: 12.0, width: 2.55, height: 3.1 },
};
const DEFAULT_CLASS = "car";

// BODY_PALETTE is SPEC.md's renderer-picked body color set (copied
// literals — this directory stays map-free, no theme.ts import).
export const BODY_PALETTE = ["#eaf0ff", "#5f8dff", "#e8b43a", "#e5484d", "#1e9e6a", "#9fb2e8"];
const DEFAULT_BODY = "#5f8dff";
const CABIN = "#1d2950";

// hashVehicleId is the fleet's one hash: FNV-1a 32 over the id's decimal
// string. The model pick reads the low bits, the body tint the high bits
// (tintFor below) — two folds of the same hash, per SPEC.
export function hashVehicleId(id: number): number {
  let h = 0x811c9dc5;
  const s = String(id);
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

// tintFor picks the per-vehicle body color from the palette by the same
// hash that picks the model (high bits, so model and color decorrelate).
// Renderers may substitute a theme-appropriate palette (SPEC: the listed
// BODY_PALETTE is the navy/default one) — e.g. hero's paper theme passes
// its own list because the near-white navy tints vanish on #fafafa.
export function tintFor(vehicleId: number, palette: readonly string[] = BODY_PALETTE): string {
  return palette[(hashVehicleId(vehicleId) >>> 16) % palette.length]!;
}

// boxModel builds the end of the fallback ladder: a plain box of class
// dims in the SPEC frame (y=0 ground, forward +Z, origin at the footprint
// center). The main mesh is named "body" so renderers tint it exactly like
// a real model's; a smaller "cabin" box keeps the silhouette vehicle-ish.
function boxModel(cls: string): FleetModel {
  const dims = CLASS_BOX_DIMS[cls] ?? CLASS_BOX_DIMS[DEFAULT_CLASS]!;
  return {
    id: `box:${cls}`,
    classes: [cls],
    weight: 1,
    dims,
    build() {
      const g = new THREE.Group();
      const bodyH = dims.height * 0.62;
      const body = new THREE.Mesh(
        new THREE.BoxGeometry(dims.width, bodyH, dims.length),
        new THREE.MeshStandardMaterial({ color: DEFAULT_BODY, flatShading: true }),
      );
      body.name = "body";
      body.position.y = bodyH / 2;
      g.add(body);
      const cabinL = Math.min(dims.length * 0.45, 2.4);
      const cabin = new THREE.Mesh(
        new THREE.BoxGeometry(dims.width * 0.86, dims.height - bodyH, cabinL),
        new THREE.MeshStandardMaterial({ color: CABIN, flatShading: true }),
      );
      cabin.name = "cabin";
      cabin.position.set(0, bodyH + (dims.height - bodyH) / 2, dims.length * 0.04);
      g.add(cabin);
      return g;
    },
  };
}

interface ManifestEntry {
  id: string;
  module: string;
  classes: string[];
  weight: number;
}

interface ManifestDoc {
  models: ManifestEntry[];
  classDefault: Record<string, string>;
}

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// parseManifest validates the manifest enough to load against; malformed
// entries throw and the caller degrades the WHOLE manifest to "boxes only"
// (a half-trusted roster is more surprising than none).
function parseManifest(raw: unknown): ManifestDoc {
  if (typeof raw !== "object" || raw === null) throw new Error("manifest is not an object");
  const o = raw as Record<string, unknown>;
  if (o["version"] !== 1) throw new Error(`unsupported manifest version ${String(o["version"])}`);
  if (!Array.isArray(o["models"])) throw new Error("manifest.models is not an array");
  const models: ManifestEntry[] = o["models"].map((m, i) => {
    const r = m as Record<string, unknown>;
    const id = r?.["id"];
    const modulePath = r?.["module"];
    const classes = r?.["classes"];
    if (typeof id !== "string" || id === "") throw new Error(`models[${i}].id missing`);
    if (typeof modulePath !== "string" || !modulePath.startsWith("./")) {
      throw new Error(`models[${i}] (${id}).module must be a ./ path`);
    }
    if (!Array.isArray(classes) || classes.some((c) => typeof c !== "string")) {
      throw new Error(`models[${i}] (${id}).classes must be strings`);
    }
    const weight = typeof r?.["weight"] === "number" && (r["weight"] as number) > 0 ? (r["weight"] as number) : 1;
    return { id, module: modulePath, classes: classes as string[], weight };
  });
  const classDefault: Record<string, string> = {};
  if (typeof o["classDefault"] === "object" && o["classDefault"] !== null) {
    for (const [k, v] of Object.entries(o["classDefault"] as Record<string, unknown>)) {
      if (typeof v === "string") classDefault[k] = v;
    }
  }
  return { models, classDefault };
}

function validateDims(raw: unknown, id: string): VehicleDims {
  const d = raw as Record<string, unknown> | null | undefined;
  for (const k of ["length", "width", "height"] as const) {
    const v = d?.[k];
    if (typeof v !== "number" || !Number.isFinite(v) || v <= 0) {
      throw new Error(`${id}: dims.${k} must be a positive number`);
    }
  }
  return d as unknown as VehicleDims;
}

// loadFleet fetches the manifest and loads every model it lists. Each
// model is smoke-built once here (the group is discarded): SPEC's "any
// load/build error → fall back" is enforced at load time, so pick() and
// the pages never see a throwing module.
export async function loadFleet(manifestUrl: string): Promise<Fleet> {
  const errors: string[] = [];
  const models: FleetModel[] = [];
  let doc: ManifestDoc | null = null;
  try {
    const res = await fetch(manifestUrl, { cache: "no-cache" });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    doc = parseManifest(await res.json());
  } catch (e) {
    errors.push(`manifest: ${errMsg(e)} — every vehicle renders as a class-dims box`);
  }
  if (doc !== null) {
    // Lazy thunks keyed by ./ path, expanded by vite at build time; a
    // manifest entry naming a file that isn't on disk is just absent here.
    const candidates = import.meta.glob<ModelModule>("./*.ts");
    for (const entry of doc.models) {
      try {
        const thunk = candidates[entry.module];
        if (thunk === undefined) throw new Error(`${entry.module} not found in fleet3d/`);
        const mod = await thunk();
        const dims = validateDims(mod.dims, entry.id);
        if (typeof mod.build !== "function") throw new Error(`${entry.id}: exports no build()`);
        const build = mod.build as () => THREE.Group;
        const probe = build(); // smoke-build; discarded
        if (!(probe instanceof THREE.Group)) throw new Error(`${entry.id}: build() did not return a THREE.Group`);
        models.push({ id: entry.id, classes: entry.classes, weight: entry.weight, dims, build });
      } catch (e) {
        errors.push(`${entry.id}: ${errMsg(e)}`);
      }
    }
  }

  const classDefault = doc?.classDefault ?? {};
  const boxes = new Map<string, FleetModel>(); // one box model per class, on demand
  const pools = new Map<string, FleetModel[]>(); // class → loaded models, on demand
  const boxFor = (cls: string): FleetModel => {
    let b = boxes.get(cls);
    if (b === undefined) {
      b = boxModel(cls);
      boxes.set(cls, b);
    }
    return b;
  };

  return {
    models,
    errors,
    pick(vehicleId: number, cls: string): FleetModel {
      let pool = pools.get(cls);
      if (pool === undefined) {
        pool = models.filter((m) => m.classes.includes(cls));
        pools.set(cls, pool);
      }
      if (pool.length > 0) {
        let total = 0;
        for (const m of pool) total += m.weight;
        let x = hashVehicleId(vehicleId) % total;
        for (const m of pool) {
          if (x < m.weight) return m;
          x -= m.weight;
        }
      }
      const dflt = classDefault[cls];
      const m = dflt === undefined ? undefined : models.find((mm) => mm.id === dflt);
      return m ?? boxFor(cls);
    },
  };
}

// triCountOf totals a built group's triangles — review-page budget display
// (SPEC: ≤ 400 triangles per model).
export function triCountOf(root: THREE.Object3D): number {
  let tris = 0;
  root.traverse((o) => {
    if (o instanceof THREE.Mesh) {
      const g = o.geometry;
      tris += Math.floor((g.index !== null ? g.index.count : g.getAttribute("position")?.count ?? 0) / 3);
    }
  });
  return tris;
}
