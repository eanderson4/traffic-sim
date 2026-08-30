/// <reference types="vite/client" />
// review.ts — the fleet-review harness (SPEC.md §"Review loop"): renders
// each manifest model (or one via ?model=<id>) on neutral ground with a
// wireframe cage of its declared dims and an axes helper (three.js
// AxesHelper convention: red +X, green +Y, blue +Z — models must sit on
// y=0 facing +Z), under fixed deterministic camera presets
// (?angle=front34|side|rear34|top) for screenshot review. A model that
// throws on import/build is reported in-page (loader.ts records it) and
// the rest keep rendering — this page exists to iterate on broken models,
// so it must survive them.
//
// URL params: ?model=<id> · ?angle=front34|side|rear34|top (default
// front34) · ?bare=1 (hide chrome for captures).

import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";

import { loadFleet, triCountOf, type FleetModel } from "./loader.ts";

const CAGE = "#e8b43a";
const GROUND = "#2c3350";
const GRID = "#3d466b";

interface Shown {
  model: FleetModel;
  tris: number;
  buildError: string | null;
}

function el<T extends HTMLElement>(id: string): T {
  const e = document.getElementById(id);
  if (e === null) throw new Error(`fleet-review.html: #${id} missing`);
  return e as T;
}

// cageOf draws the declared dims as a wireframe box: width on X, height on
// Y from y=0, length on Z centred at the footprint centre — any mismatch
// between dims and the built geometry is immediately visible.
function cageOf(m: FleetModel): THREE.LineSegments {
  const geo = new THREE.BoxGeometry(m.dims.width, m.dims.height, m.dims.length);
  const cage = new THREE.LineSegments(
    new THREE.EdgesGeometry(geo),
    new THREE.LineBasicMaterial({ color: CAGE }),
  );
  cage.position.y = m.dims.height / 2;
  geo.dispose();
  return cage;
}

async function main(): Promise<void> {
  const params = new URLSearchParams(location.search);
  const want = params.get("model");
  const angle = params.get("angle") ?? "front34";

  const fleet = await loadFleet(new URL("./manifest.json", import.meta.url).href);

  const errors: string[] = [...fleet.errors];
  let models: readonly FleetModel[] = fleet.models;
  if (want !== null && want !== "") {
    models = fleet.models.filter((m) => m.id === want);
    if (models.length === 0) {
      const known = fleet.models.map((m) => m.id).join(", ");
      const failed = fleet.errors.find((e) => e.startsWith(`${want}:`));
      errors.push(
        failed !== undefined
          ? `requested model failed to load — ${failed}`
          : `no loaded model "${want}" (loaded: ${known === "" ? "none" : known})`,
      );
    }
  }

  // Build the scene content; a build() that throws here (it was smoke-built
  // by the loader, but belt-and-braces) becomes a red cage + an error line.
  const shown: Shown[] = [];
  const content = new THREE.Group();
  let cursor = 0;
  let maxLen = 1;
  let maxH = 1;
  for (const m of models) {
    const slot = m.dims.width + 3;
    const x = cursor + slot / 2;
    cursor += slot;
    maxLen = Math.max(maxLen, m.dims.length);
    maxH = Math.max(maxH, m.dims.height);
    const holder = new THREE.Group();
    holder.position.x = x;
    let tris = 0;
    let buildError: string | null = null;
    try {
      const g = m.build();
      tris = triCountOf(g);
      holder.add(g);
    } catch (e) {
      buildError = e instanceof Error ? e.message : String(e);
      errors.push(`${m.id}: ${buildError}`);
    }
    holder.add(cageOf(m));
    const axes = new THREE.AxesHelper(Math.max(m.dims.length, m.dims.width) * 0.75 + 1);
    axes.position.y = 0.02;
    holder.add(axes);
    content.add(holder);
    shown.push({ model: m, tris, buildError });
  }
  const rowW = cursor;
  content.position.x = -rowW / 2 + (shown.length > 0 ? 0 : 0);

  // Renderer / scene / neutral ground.
  const canvas = el<HTMLCanvasElement>("view");
  const renderer = new THREE.WebGLRenderer({ canvas, antialias: true });
  renderer.setPixelRatio(Math.min(window.devicePixelRatio, 2));
  const scene = new THREE.Scene();
  scene.background = new THREE.Color("#20263e");
  const ground = new THREE.Mesh(
    new THREE.PlaneGeometry(4000, 4000),
    new THREE.MeshStandardMaterial({ color: GROUND, roughness: 1 }),
  );
  ground.rotation.x = -Math.PI / 2;
  ground.position.y = -0.02;
  scene.add(ground);
  const grid = new THREE.GridHelper(200, 100, GRID, GRID);
  (grid.material as THREE.Material).opacity = 0.35;
  (grid.material as THREE.Material).transparent = true;
  scene.add(grid);
  scene.add(new THREE.HemisphereLight(new THREE.Color("#e6e9f4"), new THREE.Color("#1a1f36"), 1.1));
  const sun = new THREE.DirectionalLight(new THREE.Color("#ffffff"), 1.4);
  sun.position.set(30, 60, 25);
  scene.add(sun);
  scene.add(content);

  // Deterministic camera presets. Vehicles face +Z: front34 sees the front
  // and the +X flank, rear34 the rear and the −X flank, side the +X
  // profile, top straight down.
  const span = Math.max(rowW, maxLen);
  const dist = Math.max(span * 1.5, maxH * 6, 8);
  const target = new THREE.Vector3(0, maxH * 0.35, 0);
  const dirs: Record<string, THREE.Vector3> = {
    front34: new THREE.Vector3(0.72, 0.42, 1),
    side: new THREE.Vector3(1, 0.18, 0),
    rear34: new THREE.Vector3(-0.72, 0.42, -1),
    top: new THREE.Vector3(0.001, 1, 0.001),
  };
  const dir = dirs[angle] ?? dirs["front34"]!;
  const camera = new THREE.PerspectiveCamera(50, 2, 0.1, 4000);
  camera.position.copy(target).addScaledVector(dir.normalize(), dist);
  const controls = new OrbitControls(camera, canvas);
  controls.target.copy(target);
  controls.enableDamping = true;
  controls.update();

  const resize = (): void => {
    renderer.setSize(window.innerWidth, window.innerHeight, false);
    camera.aspect = window.innerWidth / window.innerHeight;
    camera.updateProjectionMatrix();
  };
  window.addEventListener("resize", resize);
  resize();

  renderer.setAnimationLoop(() => {
    controls.update();
    renderer.render(scene, camera);
  });

  // HUD: per-model dims/tris/error lines, error panel, status summary.
  const info = el("info");
  info.textContent = shown
    .map((s) => {
      const d = s.model.dims;
      const dims = `${d.length}×${d.width}×${d.height} m`;
      return s.buildError === null
        ? `${s.model.id}  ${dims} · ${s.tris} tris`
        : `${s.model.id}  BUILD FAILED: ${s.buildError}`;
    })
    .join("\n");
  el("errors").textContent = errors.join("\n");
  el("status").textContent =
    `fleet-review: ${shown.length} shown · ${fleet.models.length} loaded · ${errors.length} errors · angle=${angle}`;
  el("loading").style.display = "none";
  if (params.get("bare") === "1") document.body.classList.add("bare");
}

main().catch((e: unknown) => {
  const msg = e instanceof Error ? e.message : String(e);
  const statusEl = document.getElementById("status");
  if (statusEl !== null) statusEl.textContent = `fleet-review failed: ${msg}`;
  const loading = document.getElementById("loading");
  if (loading !== null) loading.textContent = `fleet-review failed: ${msg}`;
  console.error(e);
});
