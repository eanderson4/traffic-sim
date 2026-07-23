// vite.config.ts — the viz is a TWO-PAGE build (M-demo launcher): index.html
// is the live map app, demos.html the demosrv menu page. ADR-0003 stands
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
      },
    },
  },
});
