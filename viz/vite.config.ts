// vite.config.ts — the viz is a FIVE-PAGE build: index.html is the splash
// landing page (served at / by demosrv AND by the static Pages site),
// app.html the live map app, demos.html the demos menu, hero.html the 3D
// baked-replay hero page and fleet-review.html the fleet-model review
// harness (ADR-0003 addendum 2026-08-29 — the hero track's standalone
// three.js pages; the map pages never load three). ADR-0003 stands
// (vanilla TS, no framework) — this file only declares the multi-page
// inputs; everything else stays vite defaults.

import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

export default defineConfig({
  build: {
    rollupOptions: {
      input: {
        splash: fileURLToPath(new URL("index.html", import.meta.url)),
        app: fileURLToPath(new URL("app.html", import.meta.url)),
        demos: fileURLToPath(new URL("demos.html", import.meta.url)),
        hero: fileURLToPath(new URL("hero.html", import.meta.url)),
        fleetReview: fileURLToPath(new URL("fleet-review.html", import.meta.url)),
      },
    },
  },
});
