# Vendored map label glyphs

`open-sans-semibold/0-255.pbf` — SDF glyph PBF (basic Latin range) for
MapLibre text symbol layers, downloaded once from
`https://demotiles.maplibre.org/font/Open%20Sans%20Semibold/0-255.pbf`.

Why vendored: ADR-0004 local-first — the blank dark style
(viz/src/main.ts DARK_STYLE) must not depend on a tile/font service at
runtime. Vite serves this directory at `/fonts/...` in dev and copies it
into `viz/dist/` on build, so demosrv's static file server reaches it too.

The fontstack directory name is the `{fontstack}` the style's `glyphs`
URL substitutes — it must match the layers' `text-font` ("open-sans-semibold").
Only the 0-255 range is vendored: overlay labels (zone names, municipality
names) are ASCII; add further ranges here if a label ever needs them.

Font: Open Sans Semibold, Apache License 2.0.
