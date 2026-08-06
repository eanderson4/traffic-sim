// vite.config.ts — the viz is a THREE-PAGE build: index.html is the splash
// landing page (served at / by demosrv AND by the static Pages site),
// app.html the live map app, demos.html the demos menu. ADR-0003 stands
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
      },
    },
  },
});
