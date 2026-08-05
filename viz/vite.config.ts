// vite.config.ts — the viz is a THREE-PAGE build (M-demo launcher): splash.html
// is the landing page (served at / by demosrv), index.html the live map app,
// demos.html the demos menu. ADR-0003 stands
// (vanilla TS, no framework) — this file only declares the multi-page
// inputs; everything else stays vite defaults.

import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";

export default defineConfig({
  build: {
    rollupOptions: {
      input: {
        app: fileURLToPath(new URL("index.html", import.meta.url)),
        demos: fileURLToPath(new URL("demos.html", import.meta.url)),
        splash: fileURLToPath(new URL("splash.html", import.meta.url)),
      },
    },
  },
});
