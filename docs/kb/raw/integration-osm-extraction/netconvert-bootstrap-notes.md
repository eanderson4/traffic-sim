# Field Notes: netconvert bootstrap in practice (M5)

> 2026-07-18, milestone M5 (first real network in the engine). Not a
> research pass — operational facts learned while running the ADR-0009 §1
> bootstrap (Overpass → netconvert → netimport) end-to-end. Complements
> [implementation.md](./implementation.md); the reference import lives in
> `data/networks/i280-woodside/` with the recipe in
> `contracts/network-format-v1.md`.

1. **netconvert needs nodes before ways.** Overpass's two-statement form
   (`way["highway"](bbox); out body; >; out skel qt;` — the KB's canonical
   query) emits ways *first*, and netconvert 1.27.1 fails with
   `Error: No nodes loaded`. The union form emits nodes first and works:
   `(way["highway"~"..."](bbox); >;); out body;`.
2. **Default turnarounds seal the map boundary.** With netconvert's default
   U-turn connections, every way clipped at the extract edge ends in a
   turnaround loop: the converted graph had **zero** no-predecessor lanes
   and zero no-successor lanes — no demand portals at all.
   `--no-turnarounds` is what opens origins/exits (dead end → one exit
   lane + one origin lane). Trade-off noted in ADR-0009 §6 style: real
   dead-end U-turns are lost too; acceptable for corridor imports.
3. **Junction-exit funnels are the first place the missing conflict sets
   bite.** netconvert encodes merges longitudinally: two junction-internal
   lanes feed ONE receiving lane (turn-pocket + through approach into a
   single receiving lane; on-ramp link + mainline lane into one mainline
   lane). With connection-following traversal and no right-of-way,
   simultaneous arrivals overlap (measured on i280-woodside: 13 collision
   observations at two `priority` junctions over 1200 ticks at
   900 veh/h/origin; 2 at 600). SUMO resolves this with the response/foes
   matrix; the connection `state` attribute (`M`/`m`) already encodes
   major/minor and is the natural seed for our compiled conflict sets.
   This is now the *measured* top item for arch-road-graph-model's
   right-of-way compilation (synthesis #3).
4. **eclipse-sumo on PyPI is a clean local netconvert.** The wheel ships
   Linux binaries (`.../site-packages/sumo/bin/netconvert`, 1.27.1) —
   `python3 -m venv tools/sumo-venv && pip install eclipse-sumo` gives a
   working-dir-local bootstrap tool, no system install (ADR-0004-friendly).
5. **Overpass intermittently answers HTTP 406** on well-formed queries;
   retrying after a few seconds succeeds. Worth knowing before scripting.
6. **netconvert quirks observed in warnings:** junction `shape` elements
   can sit ~25 m from the junction's given position ("Shape for junction
   … has distance"); connection speeds are silently reduced by turning
   radius ("Speed of straight connection … reduced by … due to turning
   radius") — the lane `speed` attributes we import already carry these,
   so turn-speed realism comes free with `.net.xml`.
